package migration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
	"github.com/sipeed/picoclaw/pkg/database/catalog"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestMigrationBacksUpSelectedStoreAndLegacyInputs(t *testing.T) {
	home, workspace, cfg := migrationFixture(t)
	databasePath := filepath.Join(workspace, "state", "workflows.db")
	withMigrationFence(t, home, func() {
		if err := workflows.RunOfflineDatabaseMigration(t.Context(), workspace); err != nil {
			t.Fatal(err)
		}
	})
	// Model a valid current-version generation created before the shared durable
	// import horizon existed. The offline engine must back it up and install the
	// missing provider metadata before importing the retained legacy source.
	preHorizon, err := sqliteprovider.OpenStore(databasePath, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, dropErr := preHorizon.Exec(`DROP TABLE storage_import_horizons`); dropErr != nil {
		t.Fatal(dropErr)
	}
	if closeErr := preHorizon.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	legacyPath := filepath.Join(workspace, "workflow_runs", "run-1", "run.json")
	if mkdirErr := os.MkdirAll(filepath.Dir(legacyPath), 0o700); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	legacyPayload := []byte(`{"id":"run-1"}`)
	if writeErr := os.WriteFile(legacyPath, legacyPayload, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	databasePayload, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	databaseDigest := sha256.Sum256(databasePayload)

	engine, err := New(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	id := mustStoreID(t, "workspace.workflows")
	result, err := engine.Run(t.Context(), Options{
		Stores:    []catalog.StoreID{id},
		BackupDir: filepath.Join(home, "safe-backups"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.BackupDir == "" || len(result.Stores) != 1 || !result.Stores[0].Migrated {
		t.Fatalf("result = %#v", result)
	}
	if result.Stores[0].BeforeVersion != 1 || result.Stores[0].AfterVersion != 1 {
		t.Fatalf("versions = %d -> %d", result.Stores[0].BeforeVersion, result.Stores[0].AfterVersion)
	}

	manifest := readManifest(t, result.BackupDir)
	if manifest.Outcome != "complete" || len(manifest.Stores) != 1 || !manifest.Stores[0].Exists {
		t.Fatalf("manifest = %#v", manifest)
	}
	var databaseFound, legacyFound bool
	for _, file := range manifest.Files {
		switch {
		case file.Role == "database":
			databaseFound = true
			if file.SHA256 != hex.EncodeToString(databaseDigest[:]) || file.Size != int64(len(databasePayload)) {
				t.Errorf("database manifest = %#v", file)
			}
			backedUp, readErr := os.ReadFile(filepath.Join(result.BackupDir, filepath.FromSlash(file.Backup)))
			if readErr != nil || string(backedUp) != string(databasePayload) {
				t.Errorf("database backup mismatch: error=%v", readErr)
			}
		case file.Role == "legacy" && file.Source == legacyPath:
			legacyFound = true
		}
	}
	if !databaseFound || !legacyFound {
		t.Fatalf("backup files missing: database=%v legacy=%v files=%#v", databaseFound, legacyFound, manifest.Files)
	}
	assertWorkflowRunExists(t, databasePath, "run-1")
}

func TestMigrationDryRunCreatesBackupWithoutMutatingStore(t *testing.T) {
	home, workspace, cfg := migrationFixture(t)
	createSQLiteFixture(t, filepath.Join(workspace, "state", "workflows.db"), 1)
	engine, err := New(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	id := mustStoreID(t, "workspace.workflows")
	backupParent := filepath.Join(home, "dry-run-backup")
	result, err := engine.Run(t.Context(), Options{
		Stores: []catalog.StoreID{id}, BackupDir: backupParent, DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.BackupDir == "" || !result.DryRun || len(result.Stores) != 1 || !result.Stores[0].Exists ||
		result.Stores[0].Migrated {
		t.Fatalf("dry-run result = %#v", result)
	}
	manifest := readManifest(t, result.BackupDir)
	if manifest.Outcome != "dry_run" || len(manifest.Files) == 0 {
		t.Fatalf("dry-run backup manifest = %#v", manifest)
	}
	assertSQLiteFixture(t, filepath.Join(workspace, "state", "workflows.db"), 1)
}

func TestMigrationRejectsActiveStorageOwner(t *testing.T) {
	home, _, cfg := migrationFixture(t)
	engine, err := New(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	_, err = engine.Run(t.Context(), Options{DryRun: true})
	if !errors.Is(err, ErrStorageActive) {
		t.Fatalf("Run error = %v, want ErrStorageActive", err)
	}
}

func TestMigrationAcquiresHomeFenceBeforeCatalogInspection(t *testing.T) {
	home, workspace, cfg := migrationFixture(t)
	unsafeSidecar := filepath.Join(workspace, "state", "workflows.db-wal")
	if err := os.MkdirAll(unsafeSidecar, 0o700); err != nil {
		t.Fatal(err)
	}
	engine, err := New(home, cfg)
	if err != nil {
		t.Fatalf("New inspected the physical catalog before fencing: %v", err)
	}

	online, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	_, runErr := engine.Run(t.Context(), Options{DryRun: true})
	if closeErr := online.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if !errors.Is(runErr, ErrStorageActive) {
		t.Fatalf("Run error = %v, want ErrStorageActive before catalog inspection", runErr)
	}

	if _, runErr = engine.Run(t.Context(), Options{DryRun: true}); runErr == nil {
		t.Fatal("Run accepted an unsafe catalog after acquiring the fence")
	}
}

func TestMigrationPreservesBackupWhenGenerationIsCorrupt(t *testing.T) {
	home, _, cfg := migrationFixture(t)
	corruptPath := filepath.Join(home, "auth.db")
	corrupt := []byte("not a SQLite generation")
	if err := os.WriteFile(corruptPath, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	engine, err := New(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	id := mustStoreID(t, "global.auth")
	result, err := engine.Run(t.Context(), Options{
		Stores: []catalog.StoreID{id}, BackupDir: filepath.Join(home, "backups"),
	})
	if err == nil {
		t.Fatal("corrupt database migration succeeded")
	}
	if !errors.Is(err, ErrIntegrity) {
		t.Fatalf("corrupt database error = %v, want ErrIntegrity", err)
	}
	if result.BackupDir == "" {
		t.Fatal("corrupt database did not retain a backup")
	}
	manifest := readManifest(t, result.BackupDir)
	if manifest.Outcome != "failed" || manifest.Error == "" {
		t.Fatalf("failed manifest = %#v", manifest)
	}
	if len(manifest.Files) != 1 || manifest.Files[0].Role != "database" {
		t.Fatalf("failed backup files = %#v", manifest.Files)
	}
	backedUp, readErr := os.ReadFile(filepath.Join(result.BackupDir, filepath.FromSlash(manifest.Files[0].Backup)))
	if readErr != nil || string(backedUp) != string(corrupt) {
		t.Fatalf("failed backup mismatch: data=%q error=%v", backedUp, readErr)
	}
}

func TestMigrationRejectsTooNewSchemaAfterBackup(t *testing.T) {
	home, workspace, cfg := migrationFixture(t)
	databasePath := filepath.Join(workspace, "state", "workflows.db")
	createSQLiteFixture(t, databasePath, 2)
	engine, err := New(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	id := mustStoreID(t, "workspace.workflows")
	result, err := engine.Run(t.Context(), Options{
		Stores: []catalog.StoreID{id}, BackupDir: filepath.Join(home, "backups"),
	})
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("too-new migration error = %v, want ErrSchemaTooNew", err)
	}
	if result.BackupDir == "" || readManifest(t, result.BackupDir).Outcome != "failed" {
		t.Fatalf("too-new migration did not preserve failed backup: %#v", result)
	}
	assertSQLiteFixture(t, databasePath, 2)
}

func TestMigrationRejectsSymlinkedBackupAncestor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not generally available to unprivileged Windows tests")
	}
	home, workspace, cfg := migrationFixture(t)
	createSQLiteFixture(t, filepath.Join(workspace, "state", "workflows.db"), 1)
	realParent := filepath.Join(home, "real-backups")
	if err := os.MkdirAll(realParent, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(home, "backup-alias")
	if err := os.Symlink(realParent, alias); err != nil {
		t.Fatal(err)
	}
	engine, err := New(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	id := mustStoreID(t, "workspace.workflows")
	result, err := engine.Run(t.Context(), Options{
		Stores: []catalog.StoreID{id}, BackupDir: filepath.Join(alias, "nested"),
	})
	if err == nil || result.BackupDir != "" {
		t.Fatalf("symlinked backup result = %#v, error=%v", result, err)
	}
}

func TestMigrationSnapshotsAndRecoversHotWALGeneration(t *testing.T) {
	home, _, cfg := migrationFixture(t)
	databasePath := filepath.Join(home, "auth.db")
	withMigrationFence(t, home, func() {
		if err := auth.RunOfflineDatabaseMigration(t.Context(), home); err != nil {
			t.Fatal(err)
		}
	})
	command := exec.Command(os.Args[0], "-test.run=^TestMigrationHotWALChild$")
	command.Env = append(os.Environ(), "PICOCLAW_MIGRATION_HOT_WAL="+databasePath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("hot-WAL child: %v\n%s", err, output)
	}
	for _, sidecar := range []string{databasePath + "-wal", databasePath + "-shm"} {
		if info, err := os.Stat(sidecar); err != nil || info.Size() == 0 {
			t.Fatalf("hot-WAL sidecar %q: info=%v error=%v", sidecar, info, err)
		}
	}

	engine, err := New(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	id := mustStoreID(t, "global.auth")
	result, err := engine.Run(t.Context(), Options{
		Stores: []catalog.StoreID{id}, BackupDir: filepath.Join(home, "backups"),
	})
	if err != nil {
		t.Fatal(err)
	}
	roles := make(map[string]bool)
	for _, file := range readManifest(t, result.BackupDir).Files {
		roles[file.Role] = true
	}
	for _, role := range []string{"database", "wal", "shm"} {
		if !roles[role] {
			t.Errorf("backup did not contain %s generation member", role)
		}
	}
	database, err := sqliteprovider.OpenStore(databasePath, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var token string
	if err := database.QueryRow(
		`SELECT access_token FROM auth_credentials WHERE credential_id='openai'`,
	).Scan(&token); err != nil || token != "preserved" {
		t.Fatalf("recovered auth token = %q, %v", token, err)
	}
}

func TestMigrationSnapshotsAndRecoversHotRollbackJournal(t *testing.T) {
	home, _, cfg := migrationFixture(t)
	databasePath := filepath.Join(home, "auth.db")
	withMigrationFence(t, home, func() {
		if err := auth.RunOfflineDatabaseMigration(t.Context(), home); err != nil {
			t.Fatal(err)
		}
	})
	command := exec.Command(os.Args[0], "-test.run=^TestMigrationHotRollbackJournalChild$")
	command.Env = append(os.Environ(), "PICOCLAW_MIGRATION_HOT_JOURNAL="+databasePath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("hot-journal child: %v\n%s", err, output)
	}
	journal := databasePath + "-journal"
	if info, err := os.Stat(journal); err != nil || info.Size() == 0 {
		t.Fatalf("hot rollback journal: info=%v error=%v", info, err)
	}

	engine, err := New(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(t.Context(), Options{
		Stores:    []catalog.StoreID{mustStoreID(t, "global.auth")},
		BackupDir: filepath.Join(home, "backups"),
	})
	if err != nil {
		t.Fatal(err)
	}
	journalBackedUp := false
	for _, file := range readManifest(t, result.BackupDir).Files {
		journalBackedUp = journalBackedUp || file.StoreID == "global.auth" && file.Role == "journal"
	}
	if !journalBackedUp {
		t.Fatal("mandatory backup omitted the hot rollback journal")
	}
	db, err := sqliteprovider.OpenStore(databasePath, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var token string
	if err := db.QueryRow(
		"SELECT access_token FROM auth_credentials WHERE credential_id='openai'",
	).Scan(&token); err != nil || token != "preserved" {
		t.Fatalf("rollback-journal recovery token = %q, %v", token, err)
	}
}

func TestCurrentWorkflowRecoverySignatureIsBackedUpRecoveredAndClassified(t *testing.T) {
	home, workspace, cfg := migrationFixture(t)
	databasePath := filepath.Join(workspace, "state", "workflows.db")
	legacyPath := filepath.Join(workspace, "workflow_runs", "known-review-run", "run.json")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	legacyRun := []byte(`{
		"id":"known-review-run",
		"workflow_ref":"workflows/repository-review.yml",
		"status":"succeeded",
		"created_at":"2026-08-01T12:00:00Z",
		"updated_at":"2026-08-01T12:01:00Z"
	}`)
	if err := os.WriteFile(legacyPath, legacyRun, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestMigrationWorkflowRecoveryChild$")
	command.Env = append(os.Environ(), "PICOCLAW_MIGRATION_WORKFLOW_FIXTURE="+databasePath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("workflow recovery child: %v\n%s", err, output)
	}
	info, err := os.Stat(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() < 4096 || info.Size() > 8192 {
		t.Fatalf("workflow fixture database size = %d, want 4 KiB-ish", info.Size())
	}
	for _, sidecar := range []string{databasePath + "-wal", databasePath + "-shm"} {
		if sidecarInfo, statErr := os.Stat(sidecar); statErr != nil || sidecarInfo.Size() == 0 {
			t.Fatalf("workflow hot sidecar %q: info=%v error=%v", sidecar, sidecarInfo, statErr)
		}
	}

	engine, err := New(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	id := mustStoreID(t, "workspace.workflows")
	result, err := engine.Run(t.Context(), Options{
		Stores: []catalog.StoreID{id}, BackupDir: filepath.Join(home, "backups"),
	})
	if err != nil {
		t.Fatalf("workflow migration error = %v", err)
	}
	if result.BackupDir == "" || len(result.Stores) != 1 || !result.Stores[0].Migrated ||
		result.Stores[0].AdapterRequired || result.Stores[0].BeforeVersion != 0 ||
		result.Stores[0].AfterVersion != 1 {
		t.Fatalf("workflow recovery result = %#v", result)
	}
	manifest := readManifest(t, result.BackupDir)
	if manifest.Outcome != "complete" || manifest.Error != "" {
		t.Fatalf("workflow recovery manifest = %#v", manifest)
	}
	roles := make(map[string]bool)
	legacyBackedUp := false
	for _, file := range manifest.Files {
		roles[file.Role] = true
		legacyBackedUp = legacyBackedUp || file.Role == "legacy" && file.Source == legacyPath
	}
	for _, role := range []string{"database", "wal", "shm"} {
		if !roles[role] {
			t.Errorf("workflow backup did not contain %s generation member", role)
		}
	}
	if !legacyBackedUp {
		t.Error("workflow backup did not contain the retained known run")
	}
	if _, statErr := os.Stat(legacyPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("legacy workflow input remains after adapter commit: %v", statErr)
	}
	assertWorkflowRunExists(t, databasePath, "known-review-run")
	database, err := sqliteprovider.OpenStore(databasePath, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var interruptedTables int
	if err := database.QueryRow(
		`SELECT COUNT(*) FROM sqlite_schema WHERE name='interrupted_fixture'`,
	).Scan(&interruptedTables); err != nil || interruptedTables != 0 {
		t.Fatalf("incomplete WAL transaction survived: count=%d error=%v", interruptedTables, err)
	}
}

func TestWorkflowMigrationVerificationFailurePreservesMalformedLegacyInput(t *testing.T) {
	home, workspace, cfg := migrationFixture(t)
	legacyPath := filepath.Join(workspace, "workflow_runs", "known-review-run", "run.json")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte(`{"id":`), 0o600); err != nil {
		t.Fatal(err)
	}
	engine, err := New(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	id := mustStoreID(t, "workspace.workflows")
	result, err := engine.Run(t.Context(), Options{
		Stores: []catalog.StoreID{id}, BackupDir: filepath.Join(home, "backups"),
	})
	if err == nil || result.BackupDir == "" {
		t.Fatalf("malformed workflow migration = %#v, %v", result, err)
	}
	if data, readErr := os.ReadFile(legacyPath); readErr != nil || string(data) != `{"id":` {
		t.Fatalf("malformed workflow source changed = %q, %v", data, readErr)
	}
	archive := filepath.Join(workspace, "legacy-json", "workflows-v1")
	if _, statErr := os.Stat(archive); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("malformed workflow source was archived: %v", statErr)
	}
	manifest := readManifest(t, result.BackupDir)
	if manifest.Outcome != "failed" || manifest.Error == "" {
		t.Fatalf("malformed workflow backup manifest = %#v", manifest)
	}
}

func TestMigrationPreservesCurrent166MiBWorkflowFixtureBackup(t *testing.T) {
	home, workspace, cfg := migrationFixture(t)
	withMigrationFence(t, home, func() {
		if err := workflows.RunOfflineDatabaseMigration(t.Context(), workspace); err != nil {
			t.Fatal(err)
		}
	})
	databasePath := filepath.Join(workspace, "state", "workflows.db")
	const fixtureSize = int64(166 << 20)
	if err := os.Truncate(databasePath, fixtureSize); err != nil {
		t.Fatal(err)
	}
	engine, err := New(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	id := mustStoreID(t, "workspace.workflows")
	result, err := engine.Run(t.Context(), Options{
		Stores: []catalog.StoreID{id}, BackupDir: filepath.Join(home, "backups"),
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest := readManifest(t, result.BackupDir)
	var backedUp bool
	for _, file := range manifest.Files {
		if file.StoreID == "workspace.workflows" && file.Role == "database" {
			backedUp = true
			if file.Size != fixtureSize {
				t.Fatalf("large workflow backup size = %d, want %d", file.Size, fixtureSize)
			}
		}
	}
	if !backedUp || !result.Stores[0].Migrated || result.Stores[0].AfterVersion != 1 {
		t.Fatalf("large workflow migration result = %#v manifest=%#v", result, manifest)
	}
}

// TestMigrationHotWALChild intentionally exits without closing SQLite so its
// parent test can prove the migrator snapshots the whole hot generation before
// asking SQLite to recover it.
func TestMigrationHotWALChild(t *testing.T) {
	path := os.Getenv("PICOCLAW_MIGRATION_HOT_WAL")
	if path == "" {
		return
	}
	database, err := sqliteprovider.OpenStore(path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`PRAGMA wal_autocheckpoint = 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO auth_credentials (
		credential_id, provider, access_token, refresh_token, token_type,
		oauth_token_url, oauth_client_id, oauth_client_secret, oauth_auth_style,
		account_id, expires_at_unix_seconds, expires_at_nanosecond,
		auth_method, email, project_id
	) VALUES (
		'openai', 'openai', 'preserved', '', '', '', '', '', '', '', NULL, NULL,
		'oauth', '', ''
	)`); err != nil {
		t.Fatal(err)
	}
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		if _, err := os.Stat(sidecar); err != nil {
			t.Fatal(err)
		}
	}
	os.Exit(0)
}

func TestMigrationHotRollbackJournalChild(t *testing.T) {
	path := os.Getenv("PICOCLAW_MIGRATION_HOT_JOURNAL")
	if path == "" {
		return
	}
	db, err := sqliteprovider.OpenStore(path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if configureErr := sqliteprovider.ConfigureOffline(t.Context(), db, 5*time.Second); configureErr != nil {
		t.Fatal(configureErr)
	}
	if _, insertErr := db.Exec(`INSERT INTO auth_credentials (
		credential_id, provider, access_token, refresh_token, token_type,
		oauth_token_url, oauth_client_id, oauth_client_secret, oauth_auth_style,
		account_id, expires_at_unix_seconds, expires_at_nanosecond,
		auth_method, email, project_id
	) VALUES (
		'openai', 'openai', 'preserved', '', '', '', '', '', '', '', NULL, NULL,
		'oauth', '', ''
	)`); insertErr != nil {
		t.Fatal(insertErr)
	}
	connection, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(t.Context(), "BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(
		t.Context(),
		"UPDATE auth_credentials SET access_token='uncommitted' WHERE credential_id='openai'",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + "-journal"); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func TestMigrationWorkflowRecoveryChild(t *testing.T) {
	path := os.Getenv("PICOCLAW_MIGRATION_WORKFLOW_FIXTURE")
	if path == "" {
		return
	}
	database, err := sqliteprovider.OpenStore(path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`PRAGMA page_size = 4096`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`PRAGMA wal_autocheckpoint = 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE recovery_seed (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DROP TABLE recovery_seed`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(
		`BEGIN IMMEDIATE`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(
		`CREATE TABLE interrupted_fixture (id INTEGER PRIMARY KEY, value TEXT NOT NULL)`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO interrupted_fixture(id, value) VALUES (1, 'recovered')`); err != nil {
		t.Fatal(err)
	}
	// The transaction intentionally remains uncommitted. Recovery must roll it
	// back before the workflow adapter installs the committed schema.
	os.Exit(0)
}

func TestMigrationAcceptsStableStoreIDPresentInFencedCatalog(t *testing.T) {
	home, _, cfg := migrationFixture(t)
	engine, err := New(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Stable logical identity selects only the matching entry in the catalog
	// rebuilt under this home's fence; it cannot smuggle a physical path.
	id := mustStoreID(t, "global.auth")
	result, err := engine.Run(t.Context(), Options{Stores: []catalog.StoreID{id}, DryRun: true})
	if err != nil || len(result.Stores) != 1 || result.Stores[0].ID.String() != "global.auth" {
		t.Fatalf("stable logical selection = %#v, error=%v", result, err)
	}
}

func migrationFixture(t *testing.T) (string, string, *config.Config) {
	t.Helper()
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Agents:    config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: workspace}},
		Workflows: config.WorkflowsConfig{Enabled: true},
	}
	return home, workspace, cfg
}

func createSQLiteFixture(t *testing.T, path string, version int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sqliteprovider.OpenStore(path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE fixture (id INTEGER PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO fixture(id, value) VALUES (1, 'preserved')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = ` + itoa(version)); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertSQLiteFixture(t *testing.T, path string, version int) {
	t.Helper()
	db, err := sqliteprovider.OpenStore(path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var value string
	if err := db.QueryRow(`SELECT value FROM fixture WHERE id = 1`).Scan(&value); err != nil || value != "preserved" {
		t.Fatalf("fixture value = %q, error=%v", value, err)
	}
	var gotVersion int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&gotVersion); err != nil || gotVersion != version {
		t.Fatalf("fixture version = %d, error=%v", gotVersion, err)
	}
}

func assertWorkflowRunExists(t *testing.T, path, runID string) {
	t.Helper()
	db, err := sqliteprovider.OpenStore(path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var got string
	if err := db.QueryRow(`SELECT run_id FROM workflow_runs WHERE run_id = ?`, runID).Scan(&got); err != nil {
		t.Fatalf("read migrated workflow run %q: %v", runID, err)
	}
	if got != runID {
		t.Fatalf("migrated workflow run = %q, want %q", got, runID)
	}
}

func mustStoreID(t *testing.T, name string) catalog.StoreID {
	t.Helper()
	id, err := database.ParseStoreID(name)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func withMigrationFence(t *testing.T, home string, run func()) {
	t.Helper()
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := fence.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}()
	run()
}

func readManifest(t *testing.T, root string) BackupManifest {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join(root, backupManifestName))
	if err != nil {
		t.Fatal(err)
	}
	var manifest BackupManifest
	if err := json.Unmarshal(payload, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func itoa(value int) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	var buffer [20]byte
	index := len(buffer)
	for value > 0 {
		index--
		buffer[index] = digits[value%10]
		value /= 10
	}
	return string(buffer[index:])
}
