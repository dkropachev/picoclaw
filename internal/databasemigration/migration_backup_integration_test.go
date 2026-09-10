//nolint:govet // Independent integration assertions use narrow error scopes.
package databasemigration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/databaseadapter"
	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/pkg/database"
	"github.com/sipeed/picoclaw/pkg/fileutil"
)

func TestEngineRejectsLiveSourceUpdateBeforeStagedCutover(t *testing.T) {
	home := migrationHome(t)
	liveLegacy := filepath.Join(home, "auth.json")
	liveDatabase := filepath.Join(home, "auth.db")
	writeMigrationFile(t, liveLegacy, []byte("old"))
	registry := migrationRegistry(t, databaseadapter.Adapter{
		Domain: "auth",
		Contract: databaseadapter.Contract{
			CurrentVersion: 1, EmptyPolicy: databaseadapter.EmptyMigrateOffline,
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
			return os.WriteFile(liveLegacy, []byte("NEW"), 0o600)
		},
	})
	result, err := migrationEngine(t, home, registry).Run(context.Background(), Options{
		Stores: []database.StoreID{"global/auth"},
	})
	if err == nil || result.BackupDir == "" ||
		!strings.Contains(err.Error(), "live database migration sources") {
		t.Fatalf("Run() = %#v, %v", result, err)
	}
	if _, statErr := os.Lstat(liveDatabase); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("lost update reached cutover: %v", statErr)
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
			backupRoot, err := masterBackupForDisposable(home, disposable)
			if err != nil {
				return err
			}
			if outcome := readAndVerifyManifest(t, backupRoot).Outcome; outcome != "migration_in_progress" {
				return errors.New("adapter ran before durable migration-in-progress marker")
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
	if manifest.Outcome != "failed" {
		t.Fatalf("failed adapter manifest outcome = %q", manifest.Outcome)
	}
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
			backupRoot, err := masterBackupForDisposable(home, target.LegacyRoots[0])
			if err != nil {
				return err
			}
			manifest := readAndVerifyManifest(t, backupRoot)
			for _, record := range manifest.Files {
				if record.Role == "legacy" {
					return os.WriteFile(
						filepath.Join(backupRoot, filepath.FromSlash(record.Backup)),
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

func TestEngineRejectsBackupParentOverlapBeforeCreation(t *testing.T) {
	home := migrationHome(t)
	registry, err := databaseadapter.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, parent := range []string{
		filepath.Join(home, "auth.db"),
		filepath.Join(home, "auth.json", "nested"),
		filepath.Join(home, "model-catalogs.db", "nested"),
	} {
		result, runErr := migrationEngine(t, home, registry).Run(t.Context(), Options{
			Stores: []database.StoreID{"global/auth"}, BackupDir: parent, DryRun: true,
		})
		if runErr == nil || result.BackupDir != "" {
			t.Fatalf("Run(%q) = %#v, %v", parent, result, runErr)
		}
	}
}

func TestEngineDryRunProducesOwnerOnlyBackup(t *testing.T) {
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
	result, err := migrationEngine(t, home, registry).Run(t.Context(), Options{
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
}

func TestEngineDryRunHotWALBackupRestoresCommittedRow(t *testing.T) {
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
	restoredPath := filepath.Join(t.TempDir(), "restored.db")
	suffixes := map[string]string{"database": "", "wal": "-wal", "shm": "-shm", "journal": "-journal"}
	for _, record := range readAndVerifyManifest(t, result.BackupDir).Files {
		suffix, generation := suffixes[record.Role]
		if !generation {
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

func masterBackupForDisposable(home, disposable string) (string, error) {
	candidates, err := filepath.Glob(filepath.Join(home, "backups", "database-migrate-*"))
	masters := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		info, statErr := os.Lstat(candidate)
		if statErr == nil && info.IsDir() {
			masters = append(masters, candidate)
		}
	}
	if err != nil || len(masters) != 1 {
		return "", errors.Join(errors.New("cannot locate master backup"), err)
	}
	inside, relativeErr := filepath.Rel(masters[0], disposable)
	outside := inside == ".." || strings.HasPrefix(inside, ".."+string(os.PathSeparator))
	if relativeErr != nil || inside == "." || !outside {
		return "", errors.Join(errors.New("disposable input aliases master backup"), relativeErr)
	}
	return masters[0], nil
}
