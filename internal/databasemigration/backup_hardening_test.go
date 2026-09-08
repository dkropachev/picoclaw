package databasemigration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/databaseadapter"
	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/database"
	"github.com/sipeed/picoclaw/pkg/fileutil"
)

func TestBackupParentOverlapRejectedBeforeCreation(t *testing.T) {
	home := migrationHome(t)
	registry, err := databaseadapter.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	tests := []string{
		filepath.Join(home, "auth.db"),
		filepath.Join(home, "auth.db", "nested"),
		filepath.Join(home, "auth.json", "nested"),
		filepath.Join(home, "model-catalogs.db", "nested"),
		filepath.Join(home, "model_catalogs.json", "nested"),
	}
	for _, backupParent := range tests {
		t.Run(filepath.Base(filepath.Dir(backupParent))+"-"+filepath.Base(backupParent), func(t *testing.T) {
			result, runErr := migrationEngine(t, home, registry).Run(context.Background(), Options{
				Stores: []database.StoreID{"global/auth"}, BackupDir: backupParent, DryRun: true,
			})
			if runErr == nil || result.BackupDir != "" {
				t.Fatalf("Run() = %#v, %v", result, runErr)
			}
			if _, statErr := os.Lstat(filepath.Join(home, "auth.db")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("overlap validation created database namespace: %v", statErr)
			}
			if _, statErr := os.Lstat(filepath.Join(home, "auth.json")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("overlap validation created legacy namespace: %v", statErr)
			}
		})
	}
}

func TestBackupParentPhysicalAliasesRejected(t *testing.T) {
	root := t.TempDir()
	generationDirectory := filepath.Join(root, "generation")
	legacyDirectory := filepath.Join(root, "legacy")
	for _, directory := range []string{generationDirectory, legacyDirectory} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	spec := storecatalog.Spec{
		ID:          "global/auth",
		Path:        filepath.Join(generationDirectory, "store.db"),
		LegacyRoots: []string{legacyDirectory},
	}

	for _, test := range []struct {
		name string
		root string
		want string
	}{
		{name: "generation directory", root: generationDirectory, want: "generation directory"},
		{name: "legacy directory", root: legacyDirectory, want: "legacy input directory"},
	} {
		t.Run(test.name, func(t *testing.T) {
			aliasParent := filepath.Join(t.TempDir(), "physical-alias")
			if err := os.Symlink(test.root, aliasParent); err != nil {
				t.Skipf("directory symlinks unavailable: %v", err)
			}
			path, err := validateBackupParent(aliasParent, root, []storecatalog.Spec{spec})
			if path != "" || err == nil || !strings.Contains(err.Error(), "physically aliases") ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateBackupParent(alias) = %q, %v", path, err)
			}
		})
	}
}

func TestBackupStoreDirectoryEncodingIsInjective(t *testing.T) {
	first := backupStoreDirectory("a/b.c")
	second := backupStoreDirectory("a.b/c")
	if first == second {
		t.Fatalf("distinct StoreIDs share backup directory %q", first)
	}
	for _, directory := range []string{first, second} {
		if filepath.IsAbs(directory) || strings.Contains(directory, "..") {
			t.Fatalf("unsafe encoded store directory %q", directory)
		}
		for _, component := range strings.Split(directory, string(os.PathSeparator)) {
			if len(component) > backupMaxComponent {
				t.Fatalf("oversized encoded component %q", component)
			}
		}
	}
}

func TestBackupBudgetsBoundEntriesDepthFilesBytesAndManifest(t *testing.T) {
	budget := &backupBudget{
		maxEntries: 1, maxFiles: 1, maxDepth: 1, maxFileBytes: 2, maxBytes: 2,
	}
	if err := budget.enter("one"); err != nil {
		t.Fatal(err)
	}
	if err := budget.enter("two"); err == nil {
		t.Fatal("entry budget accepted excess traversal")
	}
	depthBudget := &backupBudget{maxEntries: 2, maxDepth: 1}
	if err := depthBudget.enter(filepath.Join("one", "two")); err == nil {
		t.Fatal("depth budget accepted excess nesting")
	}
	if err := budget.reserveFile(2); err != nil {
		t.Fatal(err)
	}
	if err := budget.reserveFile(0); err == nil {
		t.Fatal("file budget accepted excess file count")
	}
	byteBudget := &backupBudget{maxFiles: 2, maxFileBytes: 1, maxBytes: 1}
	if err := byteBudget.reserveFile(2); err == nil {
		t.Fatal("file budget accepted oversized file")
	}
	if _, err := marshalBackupManifestLimit(BackupManifest{
		Error: strings.Repeat("x", 64),
	}, 16); err == nil {
		t.Fatal("manifest budget accepted oversized serialization")
	}
}

func TestLegacyWalkAndCopyStopAtConfiguredBudgets(t *testing.T) {
	legacyRoot := filepath.Join(t.TempDir(), "legacy")
	writeMigrationFile(t, filepath.Join(legacyRoot, "one.json"), []byte("1"))
	walkBudget := &backupBudget{
		maxEntries: 1, maxFiles: 10, maxDepth: 10, maxFileBytes: 10, maxBytes: 10,
	}
	if err := walkLegacyInputs(
		context.Background(), legacyRoot, t.TempDir(), map[string]struct{}{}, walkBudget,
		func(string) error { return nil },
	); err == nil {
		t.Fatal("legacy traversal ignored its entry budget")
	}

	backupRoot := t.TempDir()
	if err := secureAndValidateBackupDirectory(backupRoot); err != nil {
		t.Fatal(err)
	}
	copyBudget := &backupBudget{
		maxEntries: 10, maxFiles: 1, maxDepth: 10, maxFileBytes: 0, maxBytes: 10,
	}
	if _, err := copyBackupFile(
		context.Background(), backupRoot, "global/auth", "legacy",
		filepath.Join(legacyRoot, "one.json"), "copy", copyBudget,
	); err == nil {
		t.Fatal("backup copy ignored its byte budget")
	}
	if _, err := os.Lstat(filepath.Join(backupRoot, "copy")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected backup copy created destination: %v", err)
	}
}

func TestBackupOverlapHandlesFilesystemRoot(t *testing.T) {
	root := string(os.PathSeparator)
	if filepath.VolumeName(filepath.Clean(root)) != "" {
		root = filepath.VolumeName(filepath.Clean(root)) + string(os.PathSeparator)
	}
	if !backupPathsOverlap(root, filepath.Join(root, "somewhere", "store.db")) {
		t.Fatal("filesystem root was not treated as a namespace ancestor")
	}
}

func TestBackupFilesAndDirectoriesAreOwnerOnly(t *testing.T) {
	home := migrationHome(t)
	path := filepath.Join(home, "auth.db")
	writeMigrationFile(t, path, []byte("private backup"))
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
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
	rootInfo, err := os.Lstat(result.BackupDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := fileutil.ValidatePrivateDirectory(result.BackupDir, rootInfo); err != nil {
		t.Fatalf("backup root is not private: %v", err)
	}
	manifest := readAndVerifyManifest(t, result.BackupDir)
	for _, record := range manifest.Files {
		backupPath := filepath.Join(result.BackupDir, filepath.FromSlash(record.Backup))
		info, statErr := os.Lstat(backupPath)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if err := fileutil.ValidatePrivateFile(backupPath, info); err != nil {
			t.Fatalf("backup file is not private: %v", err)
		}
	}
	for _, name := range []string{backupManifestName, backupManifestHash} {
		path := filepath.Join(result.BackupDir, name)
		info, statErr := os.Lstat(path)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if err := fileutil.ValidatePrivateFile(path, info); err != nil {
			t.Fatalf("backup control file is not private: %v", err)
		}
	}
}

func TestAdapterReceivesDisposableLegacyBackupCopy(t *testing.T) {
	home := migrationHome(t)
	liveLegacy := filepath.Join(home, "auth.json")
	writeMigrationFile(t, liveLegacy, []byte("original legacy"))
	callbackErr := errors.New("stop after mutating disposable input")
	var disposable string
	registry := migrationRegistry(t, databaseadapter.Adapter{
		Domain: "auth",
		Contract: databaseadapter.Contract{
			CurrentVersion: 1, EmptyPolicy: databaseadapter.EmptyMigrateOffline,
		},
		Migrate: func(ctx context.Context, target databaseadapter.Target) error {
			if !database.MigrationContextAuthorizes(ctx, target.GenerationPath) {
				return errors.New("adapter lacks exact staged migration authority")
			}
			if len(target.LegacyRoots) != 1 || target.LegacyRoots[0] == liveLegacy {
				return errors.New("adapter received a live legacy root")
			}
			disposable = target.LegacyRoots[0]
			payload, err := os.ReadFile(disposable)
			if err != nil || string(payload) != "original legacy" {
				return errors.New("disposable legacy input differs from snapshot")
			}
			if err := os.WriteFile(disposable, []byte("mutated"), 0o600); err != nil {
				return err
			}
			return callbackErr
		},
	})
	result, err := migrationEngine(t, home, registry).Run(context.Background(), Options{
		Stores: []database.StoreID{"global/auth"},
	})
	if !errors.Is(err, callbackErr) || result.BackupDir == "" {
		t.Fatalf("Run() = %#v, %v", result, err)
	}
	if payload, readErr := os.ReadFile(liveLegacy); readErr != nil || string(payload) != "original legacy" {
		t.Fatalf("live legacy input changed: %q, %v", payload, readErr)
	}
	if _, statErr := os.Lstat(disposable); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("disposable legacy input was not removed: %v", statErr)
	}
	manifest := readAndVerifyManifest(t, result.BackupDir)
	for _, record := range manifest.Files {
		if record.Role != "legacy" {
			continue
		}
		payload, readErr := os.ReadFile(filepath.Join(result.BackupDir, filepath.FromSlash(record.Backup)))
		if readErr != nil || string(payload) != "original legacy" {
			t.Fatalf("master legacy backup changed: %q, %v", payload, readErr)
		}
	}
}

func TestDisposableLegacyDirectoryRecreatedFromManifest(t *testing.T) {
	backupRoot := t.TempDir()
	if err := secureAndValidateBackupDirectory(backupRoot); err != nil {
		t.Fatal(err)
	}
	payload := []byte("nested legacy")
	backupRelative := filepath.Join("stores", "auth", "legacy", "file")
	backupPath := filepath.Join(backupRoot, backupRelative)
	writeMigrationFile(t, backupPath, payload)
	digest := sha256.Sum256(payload)
	liveRoot := filepath.Join(t.TempDir(), "never-read-live-root")
	session := &backupSession{root: backupRoot, manifest: BackupManifest{
		Version: backupManifestVersion, CreatedAt: time.Unix(1, 0).UTC(),
		Stores:             []BackupStoreManifest{{StoreID: "global/auth", LegacyRoots: 1}},
		CatalogGenerations: []string{filepath.Join(t.TempDir(), "store.db")},
		Files: []BackupFileManifest{{
			StoreID:        "global/auth",
			Role:           "legacy",
			Source:         filepath.Join(liveRoot, "nested", "file.json"),
			SourceIdentity: "fixture-source",
			Backup:         filepath.ToSlash(backupRelative),
			SHA256:         hex.EncodeToString(digest[:]),
			Size:           int64(len(payload)),
			Mode:           0o600,
			SourceMode:     0o640,
		}},
	}}
	if err := session.finish("snapshot_complete", nil); err != nil {
		t.Fatal(err)
	}
	roots, cleanup, err := session.prepareLegacyInputs(context.Background(), storecatalog.Spec{
		ID: "global/auth", LegacyRoots: []string{liveRoot},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 1 {
		t.Fatalf("disposable roots = %q", roots)
	}
	got, err := os.ReadFile(filepath.Join(roots[0], "nested", "file.json"))
	if err != nil || string(got) != string(payload) {
		t.Fatalf("disposable directory payload = %q, %v", got, err)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(roots[0]); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disposable directory survived cleanup: %v", err)
	}
}

func TestBackupIsReverifiedAfterAdapterAndBeforeCutover(t *testing.T) {
	home := migrationHome(t)
	livePath := filepath.Join(home, "auth.db")
	writeMigrationFile(t, filepath.Join(home, "auth.json"), []byte("legacy"))
	registry := migrationRegistry(t, databaseadapter.Adapter{
		Domain: "auth",
		Contract: databaseadapter.Contract{
			CurrentVersion: 1,
			EmptyPolicy:    databaseadapter.EmptyMigrateOffline,
			RequiredObjects: []databaseadapter.SchemaObject{
				{Type: "table", Name: "items"},
				{Type: "table", Name: "storage_import_horizons"},
			},
			RequiredColumns: []databaseadapter.ColumnSet{{Table: "items", Columns: []string{"id"}}},
			ImportHorizon:   "auth",
		},
		Migrate: func(ctx context.Context, target databaseadapter.Target) error {
			if err := createMigratedTarget(ctx, target.GenerationPath, 1); err != nil {
				return err
			}
			workRoot := filepath.Dir(filepath.Dir(target.LegacyRoots[0]))
			masters, err := filepath.Glob(filepath.Join(filepath.Dir(workRoot), "database-migrate-*"))
			if err != nil || len(masters) != 1 {
				return errors.New("cannot locate master backup")
			}
			manifest := readAndVerifyManifest(t, masters[0])
			for _, record := range manifest.Files {
				if record.Role == "legacy" {
					return os.WriteFile(
						filepath.Join(masters[0], filepath.FromSlash(record.Backup)),
						[]byte("tampered master"), 0o600,
					)
				}
			}
			return errors.New("legacy backup missing")
		},
	})
	result, err := migrationEngine(t, home, registry).Run(context.Background(), Options{
		Stores: []database.StoreID{"global/auth"},
	})
	if err == nil || result.BackupDir == "" {
		t.Fatalf("Run() = %#v, %v", result, err)
	}
	if _, statErr := os.Lstat(livePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("tampered backup reached cutover: %v", statErr)
	}
}

func TestOutcomeUnknownIsPersistedDistinctly(t *testing.T) {
	root := filepath.Join(t.TempDir(), "backup")
	if err := ensurePrivateBackupDirectory(root); err != nil {
		t.Fatal(err)
	}
	session := &backupSession{root: root, manifest: BackupManifest{
		Version: backupManifestVersion, CreatedAt: time.Unix(1, 0).UTC(),
		Stores:             []BackupStoreManifest{{StoreID: "global/auth"}},
		CatalogGenerations: []string{filepath.Join(t.TempDir(), "store.db")},
	}}
	cause := database.NewError(database.CodeOutcomeUnknown, "post-cutover outcome is unknown")
	if outcome := backupOutcome(nil); outcome != "complete" {
		t.Fatalf("successful backup outcome = %q", outcome)
	}
	if outcome := backupOutcome(errors.New("pre-cutover failure")); outcome != "failed" {
		t.Fatalf("failed backup outcome = %q", outcome)
	}
	if err := session.finish(backupOutcome(cause), cause); err != nil {
		t.Fatal(err)
	}
	if outcome := readAndVerifyManifest(t, root).Outcome; outcome != "outcome_unknown" {
		t.Fatalf("backup outcome = %q", outcome)
	}
}

func TestHotWALBackupRestoresCommittedRow(t *testing.T) {
	home := migrationHome(t)
	path := filepath.Join(home, "auth.db")
	ctx := context.Background()
	db, err := sqliteprovider.OpenStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := sqliteprovider.Configure(ctx, db, time.Second, false); err != nil {
		t.Fatal(err)
	}
	connection, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	defer db.Close()
	if _, err := connection.ExecContext(ctx, "PRAGMA wal_autocheckpoint = 0"); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(ctx, "CREATE TABLE hot_rows (value TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	var busy, logFrames, checkpointed int
	if err := connection.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(
		&busy, &logFrames, &checkpointed,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(ctx, "INSERT INTO hot_rows(value) VALUES ('from-wal')"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("hot WAL missing: %#v, %v", info, err)
	}

	registry, err := databaseadapter.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	result, err := migrationEngine(t, home, registry).Run(ctx, Options{
		Stores: []database.StoreID{"global/auth"}, DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest := readAndVerifyManifest(t, result.BackupDir)
	restoredPath := filepath.Join(t.TempDir(), "restored.db")
	roleSuffix := map[string]string{"database": "", "wal": "-wal", "shm": "-shm", "journal": "-journal"}
	for _, record := range manifest.Files {
		suffix, generationMember := roleSuffix[record.Role]
		if !generationMember {
			continue
		}
		payload, readErr := os.ReadFile(filepath.Join(result.BackupDir, filepath.FromSlash(record.Backup)))
		if readErr != nil {
			t.Fatal(readErr)
		}
		writeMigrationFile(t, restoredPath+suffix, payload)
	}
	restored, err := sqliteprovider.OpenStore(restoredPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var value string
	if err := restored.QueryRowContext(ctx, "SELECT value FROM hot_rows").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "from-wal" {
		t.Fatalf("restored value = %q", value)
	}
}
