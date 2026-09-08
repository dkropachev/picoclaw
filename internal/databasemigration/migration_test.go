package databasemigration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/databaseadapter"
	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestDryRunBacksUpGenerationLegacyAndHotSidecarsWithoutOpening(t *testing.T) {
	home := migrationHome(t)
	databasePath := filepath.Join(home, "auth.db")
	databaseBytes := []byte("opaque pre-open generation")
	writeMigrationFile(t, databasePath, databaseBytes)
	writeMigrationFile(t, databasePath+"-wal", []byte("hot wal"))
	writeMigrationFile(t, databasePath+"-shm", []byte("hot shm"))
	legacyPath := filepath.Join(home, "auth.json")
	writeMigrationFile(t, legacyPath, []byte("legacy"))

	called := false
	registry := migrationRegistry(t, databaseadapter.Adapter{
		Domain: "auth",
		Contract: databaseadapter.Contract{
			CurrentVersion: 1, EmptyPolicy: databaseadapter.EmptyMigrateOffline,
		},
		Migrate: func(context.Context, databaseadapter.Target) error {
			called = true
			return nil
		},
	})
	engine := migrationEngine(t, home, registry)
	result, err := engine.Run(context.Background(), Options{
		Stores: []database.StoreID{"global/auth"}, DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if called || !result.DryRun || len(result.Stores) != 1 || result.Stores[0].Migrated {
		t.Fatalf("dry-run result = %#v, called=%t", result, called)
	}
	if got, err := os.ReadFile(databasePath); err != nil || string(got) != string(databaseBytes) {
		t.Fatalf("dry run mutated generation: %q, %v", got, err)
	}
	manifest := readAndVerifyManifest(t, result.BackupDir)
	roles := make(map[string]bool)
	var databaseBackup string
	for _, file := range manifest.Files {
		roles[file.Role] = true
		if file.Role == "database" {
			databaseBackup = filepath.Join(result.BackupDir, filepath.FromSlash(file.Backup))
		}
	}
	for _, role := range []string{"database", "wal", "shm", "legacy"} {
		if !roles[role] {
			t.Fatalf("backup missing %s: %#v", role, manifest.Files)
		}
	}
	// Prove backup is directly restorable to exact pre-migration bytes.
	writeMigrationFile(t, databasePath, []byte("changed"))
	restored, err := os.ReadFile(databaseBackup)
	if err != nil {
		t.Fatal(err)
	}
	writeMigrationFile(t, databasePath, restored)
	got, err := os.ReadFile(databasePath)
	if err != nil || string(got) != string(databaseBytes) {
		t.Fatalf("restored bytes = %q, %v", got, err)
	}
}

func TestMigrationReportsMissingAdapterAfterDurableBackup(t *testing.T) {
	home := migrationHome(t)
	writeMigrationFile(t, filepath.Join(home, "auth.json"), []byte("legacy"))
	registry := migrationRegistry(t, databaseadapter.Adapter{
		Domain: "auth",
		Contract: databaseadapter.Contract{
			CurrentVersion: 1, EmptyPolicy: databaseadapter.EmptyMigrateOffline,
		},
	})
	result, err := migrationEngine(t, home, registry).Run(context.Background(), Options{
		Stores: []database.StoreID{"global/auth"},
	})
	if !errors.Is(err, ErrAdapterRequired) {
		t.Fatalf("Run error = %v", err)
	}
	if result.BackupDir == "" || len(result.Stores) != 1 || !result.Stores[0].AdapterRequired {
		t.Fatalf("result = %#v", result)
	}
	manifest := readAndVerifyManifest(t, result.BackupDir)
	if manifest.Outcome != "failed" || manifest.Error == "" {
		t.Fatalf("manifest = %#v", manifest)
	}
}

func TestFakeAdapterMigratesStagedGenerationAndRestartSkipsCallback(t *testing.T) {
	home := migrationHome(t)
	callbackCount := 0
	contract := databaseadapter.Contract{
		CurrentVersion: 1,
		EmptyPolicy:    databaseadapter.EmptyMigrateOffline,
		RequiredObjects: []databaseadapter.SchemaObject{
			{Type: "table", Name: "items"},
			{Type: "table", Name: "storage_import_horizons"},
		},
		RequiredColumns: []databaseadapter.ColumnSet{{Table: "items", Columns: []string{"id"}}},
		ImportHorizon:   "auth",
	}
	registry := migrationRegistry(t, databaseadapter.Adapter{
		Domain: "auth", Contract: contract,
		Migrate: func(ctx context.Context, target databaseadapter.Target) error {
			callbackCount++
			if target.ID != "global/auth" || target.GenerationPath == "" {
				return errors.New("invalid target")
			}
			return createMigratedTarget(ctx, target.GenerationPath, contract.CurrentVersion)
		},
	})
	engine := migrationEngine(t, home, registry)
	first, err := engine.Run(context.Background(), Options{Stores: []database.StoreID{"global/auth"}})
	if err != nil {
		t.Fatal(err)
	}
	if callbackCount != 1 || len(first.Stores) != 1 || !first.Stores[0].Migrated ||
		first.Stores[0].BeforeVersion != 0 || first.Stores[0].AfterVersion != 1 {
		t.Fatalf("first result = %#v, callbacks=%d", first, callbackCount)
	}
	if readAndVerifyManifest(t, first.BackupDir).Outcome != "complete" {
		t.Fatal("first backup not complete")
	}

	second, err := engine.Run(context.Background(), Options{Stores: []database.StoreID{"global/auth"}})
	if err != nil {
		t.Fatal(err)
	}
	if callbackCount != 1 || len(second.Stores) != 1 || second.Stores[0].Migrated ||
		second.Stores[0].BeforeVersion != 1 || second.Stores[0].AfterVersion != 1 {
		t.Fatalf("restart result = %#v, callbacks=%d", second, callbackCount)
	}
}

func TestEngineFreezesCatalogAndRejectsConcurrentRun(t *testing.T) {
	home := migrationHome(t)
	workspace := filepath.Join(home, "workspace")
	cfg := &config.Config{Agents: config.AgentsConfig{
		Defaults: config.AgentDefaults{Workspace: workspace},
	}}
	registry, err := databaseadapter.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	engine, err := New(storecatalog.Options{Home: home, Config: cfg}, registry)
	if err != nil {
		t.Fatal(err)
	}
	want := engine.catalog.All()
	cfg.Agents.Defaults.Workspace = filepath.Join(home, "mutated")
	got := engine.catalog.All()
	if len(got) != len(want) {
		t.Fatalf("frozen catalog count = %d, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index].ID != want[index].ID || got[index].Path != want[index].Path {
			t.Fatalf("frozen catalog[%d] = %#v, want %#v", index, got[index], want[index])
		}
	}
	engine.running.Store(true)
	defer engine.running.Store(false)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if result, err := engine.Run(ctx, Options{}); !errors.Is(err, context.Canceled) ||
		len(result.Stores) != 0 {
		t.Fatalf("canceled concurrent Run = %#v, %v", result, err)
	}
	if result, err := engine.Run(t.Context(), Options{}); !errors.Is(err, ErrStorageActive) ||
		len(result.Stores) != 0 {
		t.Fatalf("concurrent Run = %#v, %v", result, err)
	}
}

func TestEngineMutatesExactlyOneStorePerRun(t *testing.T) {
	home := migrationHome(t)
	registry, err := databaseadapter.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	result, err := migrationEngine(t, home, registry).Run(t.Context(), Options{})
	if database.CodeOf(err) != database.CodeInvalid || result.BackupDir != "" {
		t.Fatalf("multi-store mutation = %#v, %v", result, err)
	}
	result, err = migrationEngine(t, home, registry).Run(t.Context(), Options{DryRun: true})
	if err != nil || result.BackupDir == "" || len(result.Stores) < 2 {
		t.Fatalf("multi-store dry run = %#v, %v", result, err)
	}
}

func TestFakeAdapterFailureRetainsBackupAndLiveGeneration(t *testing.T) {
	home := migrationHome(t)
	path := filepath.Join(home, "auth.db")
	createVersionOnlyDatabase(t, path, 1)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	callbackErr := errors.New("fake migration failed")
	registry := migrationRegistry(t, databaseadapter.Adapter{
		Domain: "auth",
		Contract: databaseadapter.Contract{
			CurrentVersion: 1,
			EmptyPolicy:    databaseadapter.EmptyMigrateOffline,
			RequiredObjects: []databaseadapter.SchemaObject{
				{Type: "table", Name: "items"},
			},
		},
		Migrate: func(context.Context, databaseadapter.Target) error { return callbackErr },
	})
	result, err := migrationEngine(t, home, registry).Run(context.Background(), Options{
		Stores: []database.StoreID{"global/auth"},
	})
	if !errors.Is(err, callbackErr) || result.BackupDir == "" {
		t.Fatalf("Run() = %#v, %v", result, err)
	}
	if readAndVerifyManifest(t, result.BackupDir).Outcome != "failed" {
		t.Fatal("failed migration backup lacks failed outcome")
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil || string(after) != string(before) {
		t.Fatalf("live generation changed on staged failure: error=%v", readErr)
	}
}

func TestPreCutoverAdapterFailureCannotClaimUnknownOrCompleteOutcome(t *testing.T) {
	for _, test := range []struct {
		name    string
		migrate databaseadapter.MigrateFunc
	}{
		{
			name: "structured unknown",
			migrate: func(context.Context, databaseadapter.Target) error {
				return database.NewError(database.CodeOutcomeUnknown, "disposable stage uncertainty")
			},
		},
		{
			name: "panic",
			migrate: func(context.Context, databaseadapter.Target) error {
				panic("secret panic payload")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := migrationHome(t)
			registry := migrationRegistry(t, databaseadapter.Adapter{
				Domain: "auth",
				Contract: databaseadapter.Contract{
					CurrentVersion: 1, EmptyPolicy: databaseadapter.EmptyMigrateOffline,
				},
				Migrate: test.migrate,
			})
			result, err := migrationEngine(t, home, registry).Run(t.Context(), Options{
				Stores: []database.StoreID{"global/auth"},
			})
			if err == nil || database.CodeOf(err) == database.CodeOutcomeUnknown || result.BackupDir == "" {
				t.Fatalf("pre-cutover failure = %#v, %v", result, err)
			}
			if manifest := readAndVerifyManifest(t, result.BackupDir); manifest.Outcome != "failed" {
				t.Fatalf("pre-cutover manifest outcome = %q", manifest.Outcome)
			}
			if _, statErr := os.Lstat(filepath.Join(home, "auth.db")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("pre-cutover failure installed live database: %v", statErr)
			}
		})
	}
}

func TestMigrationFenceSelectionAndUnknownID(t *testing.T) {
	home := migrationHome(t)
	registry, err := databaseadapter.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	engine := migrationEngine(t, home, registry)
	online, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	if result, runErr := engine.Run(context.Background(), Options{DryRun: true}); !errors.Is(runErr, ErrStorageActive) || result.BackupDir != "" {
		t.Fatalf("fenced Run() = %#v, %v", result, runErr)
	}
	if err := online.Close(); err != nil {
		t.Fatal(err)
	}

	writeMigrationFile(t, filepath.Join(home, "auth.json"), []byte("auth"))
	writeMigrationFile(t, filepath.Join(home, "launcher-config.json"), []byte("launcher"))
	result, err := engine.Run(context.Background(), Options{
		Stores: []database.StoreID{"global/auth"}, DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest := readAndVerifyManifest(t, result.BackupDir)
	if len(manifest.Stores) != 1 || manifest.Stores[0].StoreID != "global/auth" {
		t.Fatalf("selected manifest stores = %#v", manifest.Stores)
	}
	if result, runErr := engine.Run(context.Background(), Options{
		Stores: []database.StoreID{"global/missing"}, DryRun: true,
	}); !errors.Is(runErr, ErrUnknownStore) || result.BackupDir != "" {
		t.Fatalf("unknown Run() = %#v, %v", result, runErr)
	}
	if result, runErr := engine.Run(context.Background(), Options{
		Stores: []database.StoreID{"global/auth", "global/auth"}, DryRun: true,
	}); runErr == nil || result.BackupDir != "" {
		t.Fatalf("duplicate Run() = %#v, %v", result, runErr)
	}
}

func TestMigrationBackupsPrecedeTooNewAndIntegrityErrors(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*testing.T, string)
		want    error
	}{
		{
			name: "too-new",
			prepare: func(t *testing.T, path string) {
				createVersionOnlyDatabase(t, path, 2)
			},
			want: ErrSchemaTooNew,
		},
		{
			name: "integrity",
			prepare: func(t *testing.T, path string) {
				writeMigrationFile(t, path, []byte("corrupt"))
			},
			want: ErrIntegrity,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := migrationHome(t)
			test.prepare(t, filepath.Join(home, "auth.db"))
			registry := migrationRegistry(t, databaseadapter.Adapter{
				Domain: "auth",
				Contract: databaseadapter.Contract{
					CurrentVersion: 1, EmptyPolicy: databaseadapter.EmptyInitializeOnline,
				},
			})
			result, err := migrationEngine(t, home, registry).Run(
				context.Background(), Options{Stores: []database.StoreID{"global/auth"}},
			)
			if !errors.Is(err, test.want) || result.BackupDir == "" {
				t.Fatalf("Run() = %#v, %v; want %v", result, err, test.want)
			}
			if readAndVerifyManifest(t, result.BackupDir).Outcome != "failed" {
				t.Fatal("failure backup lacks failed outcome")
			}
		})
	}
}

func TestBackupVerificationRejectsFileManifestAndHashTampering(t *testing.T) {
	home := migrationHome(t)
	writeMigrationFile(t, filepath.Join(home, "auth.db"), []byte("opaque"))
	registry, err := databaseadapter.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	result, err := migrationEngine(t, home, registry).Run(context.Background(), Options{
		Stores: []database.StoreID{"global/auth"}, DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest := readAndVerifyManifest(t, result.BackupDir)
	session := &backupSession{root: result.BackupDir, manifest: manifest}
	backupPath := filepath.Join(result.BackupDir, filepath.FromSlash(manifest.Files[0].Backup))
	originalFile, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	writeMigrationFile(t, backupPath, []byte("tampered"))
	if err := session.verify(context.Background()); err == nil {
		t.Fatal("verify accepted tampered backup file")
	}
	writeMigrationFile(t, backupPath, originalFile)

	manifestPath := filepath.Join(result.BackupDir, backupManifestName)
	originalManifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	writeMigrationFile(t, manifestPath, append(append([]byte(nil), originalManifest...), '\n'))
	if err := session.verify(context.Background()); err == nil {
		t.Fatal("verify accepted tampered manifest")
	}
	writeMigrationFile(t, manifestPath, originalManifest)

	hashPath := filepath.Join(result.BackupDir, backupManifestHash)
	writeMigrationFile(t, hashPath, []byte(strings.Repeat("0", sha256.Size*2)+"\n"))
	if err := session.verify(context.Background()); err == nil {
		t.Fatal("verify accepted tampered manifest hash")
	}
}

func TestBackupDatabaseCanBeRestoredAndReopened(t *testing.T) {
	home := migrationHome(t)
	path := filepath.Join(home, "auth.db")
	createVersionOnlyDatabase(t, path, 7)
	registry, err := databaseadapter.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	result, err := migrationEngine(t, home, registry).Run(context.Background(), Options{
		Stores: []database.StoreID{"global/auth"}, DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest := readAndVerifyManifest(t, result.BackupDir)
	var backupPath string
	for _, file := range manifest.Files {
		if file.Role == "database" {
			backupPath = filepath.Join(result.BackupDir, filepath.FromSlash(file.Backup))
		}
	}
	payload, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range generationPaths(path) {
		_ = os.Remove(member)
	}
	writeMigrationFile(t, path, payload)
	db, err := sqliteprovider.OpenStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 7 {
		t.Fatalf("restored version = %d, error=%v", version, err)
	}
}

func TestMigrationRejectsSymlinkAndNonregularInputs(t *testing.T) {
	if !supportsMigrationSymlink(t) {
		return
	}
	home := migrationHome(t)
	target := filepath.Join(home, "outside")
	writeMigrationFile(t, target, []byte("legacy"))
	if err := os.Symlink(target, filepath.Join(home, "auth.json")); err != nil {
		t.Fatal(err)
	}
	registry, err := databaseadapter.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := migrationEngine(t, home, registry).Run(context.Background(), Options{DryRun: true})
	if runErr == nil || result.BackupDir != "" {
		t.Fatalf("symlink Run() = %#v, %v", result, runErr)
	}

	home = migrationHome(t)
	if err := os.Mkdir(filepath.Join(home, "auth.db"), 0o700); err != nil {
		t.Fatal(err)
	}
	result, runErr = migrationEngine(t, home, registry).Run(context.Background(), Options{DryRun: true})
	if runErr == nil || result.BackupDir != "" {
		t.Fatalf("nonregular Run() = %#v, %v", result, runErr)
	}
}

func migrationHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	return home
}

func migrationEngine(
	t *testing.T,
	home string,
	registry *databaseadapter.Registry,
) *Engine {
	t.Helper()
	engine, err := New(storecatalog.Options{Home: home, Config: &config.Config{}}, registry)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func migrationRegistry(t *testing.T, adapter databaseadapter.Adapter) *databaseadapter.Registry {
	t.Helper()
	registry, err := databaseadapter.NewRegistry(adapter)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func createMigratedTarget(ctx context.Context, path string, version int) error {
	db, err := sqliteprovider.OpenStore(path, time.Second)
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer db.Close()
	if err := sqliteprovider.ConfigureOffline(ctx, db, time.Second); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, "CREATE TABLE items (id INTEGER PRIMARY KEY)"); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, "CREATE TABLE storage_import_horizons (component TEXT PRIMARY KEY)"); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO storage_import_horizons(component) VALUES ('auth')"); err != nil {
		return err
	}
	return sqliteprovider.SetSchemaVersion(ctx, db, version)
}

func createVersionOnlyDatabase(t *testing.T, path string, version int) {
	t.Helper()
	db, err := sqliteprovider.OpenStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := sqliteprovider.Configure(context.Background(), db, time.Second, false); err != nil {
		t.Fatal(err)
	}
	if err := sqliteprovider.SetSchemaVersion(context.Background(), db, version); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func readAndVerifyManifest(t *testing.T, root string) BackupManifest {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join(root, backupManifestName))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	hashPayload, err := os.ReadFile(filepath.Join(root, backupManifestHash))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(hashPayload)) != hex.EncodeToString(digest[:]) {
		t.Fatal("backup manifest hash mismatch")
	}
	var manifest BackupManifest
	if err := json.Unmarshal(payload, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func writeMigrationFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func supportsMigrationSymlink(t *testing.T) bool {
	t.Helper()
	root := t.TempDir()
	target, link := filepath.Join(root, "target"), filepath.Join(root, "link")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
		return false
	}
	return true
}
