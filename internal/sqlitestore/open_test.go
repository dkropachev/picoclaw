//nolint:govet // Independent online-mode assertions intentionally reuse err.
package sqlitestore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/pkg/database"
)

func testOptions() Options {
	return Options{
		Component: "test-store",
		Migrations: []Migration{{
			Version: 1,
			Statements: []string{`CREATE TABLE records (
                id TEXT PRIMARY KEY,
                value TEXT NOT NULL,
                version INTEGER NOT NULL DEFAULT 1 CHECK(version > 0)
            ) STRICT`},
		}},
		Validate: func(ctx context.Context, conn *sql.Conn) error {
			var count int
			return conn.QueryRowContext(
				ctx,
				`SELECT COUNT(*) FROM pragma_table_info('records') WHERE name IN ('id', 'value', 'version')`,
			).Scan(&count)
		},
	}
}

func TestOnlineFenceInitializesOnlyMissingEmptyStore(t *testing.T) {
	home := t.TempDir()
	outdatedPath := filepath.Join(home, "outdated.db")
	outdated, err := sqliteprovider.OpenStore(outdatedPath, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := outdated.Exec(`CREATE TABLE legacy_shape (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := outdated.Close(); err != nil {
		t.Fatal(err)
	}
	preHorizonPath := filepath.Join(home, "pre-horizon.db")
	preHorizon, err := Open(t.Context(), preHorizonPath, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := preHorizon.Exec(`DROP TABLE storage_import_horizons`); err != nil {
		t.Fatal(err)
	}
	if err := preHorizon.Close(); err != nil {
		t.Fatal(err)
	}
	missingHorizonPath := filepath.Join(home, "missing-horizon-row.db")
	horizonOptions := testOptions()
	horizonOptions.Legacy = &LegacyOptions{
		SourceRoot: home, ArchiveRoot: filepath.Join(home, "archive"),
		Sources: func() ([]LegacySource, error) { return nil, nil },
		Import: func(context.Context, *sql.Conn, LegacyInput) (ImportResult, error) {
			return ImportResult{}, nil
		},
	}
	missingHorizon, err := Open(t.Context(), missingHorizonPath, horizonOptions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := missingHorizon.Exec(
		`DELETE FROM storage_import_horizons WHERE component='test-store'`,
	); err != nil {
		t.Fatal(err)
	}
	if err := missingHorizon.Close(); err != nil {
		t.Fatal(err)
	}
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	if opened, err := Open(
		t.Context(),
		outdatedPath,
		testOptions(),
	); opened != nil ||
		database.CodeOf(err) != database.CodeMigrationRequired {
		t.Fatalf("outdated online Open() = %#v, %v", opened, err)
	}
	if opened, err := Open(
		t.Context(),
		preHorizonPath,
		testOptions(),
	); opened != nil || database.CodeOf(err) != database.CodeMigrationRequired {
		t.Fatalf("pre-horizon online Open() = %#v, %v", opened, err)
	}
	if ready, err := sqliteprovider.HasSchemaObjects(
		t.Context(), preHorizonPath, 5*time.Second, "storage_import_horizons",
	); err != nil || ready {
		t.Fatalf("online Open() mutated pre-horizon generation: ready=%v error=%v", ready, err)
	}
	if opened, err := Open(
		t.Context(), missingHorizonPath, horizonOptions,
	); opened != nil || database.CodeOf(err) != database.CodeMigrationRequired {
		t.Fatalf("missing-horizon online Open() = %#v, %v", opened, err)
	}

	freshPath := filepath.Join(home, "fresh.db")
	fresh, err := Open(t.Context(), freshPath, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	version, err := sqliteprovider.SchemaVersion(t.Context(), fresh)
	if err != nil || version != 1 {
		t.Fatalf("fresh schema version = %d, %v", version, err)
	}
}

func TestOpenCreatesDurablePrivateDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "store.db")
	db, err := Open(t.Context(), path, testOptions())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()

	var version, foreignKeys, busyTimeout, synchronous int
	var journal string
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if version != 1 || foreignKeys != 1 || busyTimeout != 5000 || synchronous != 2 ||
		!strings.EqualFold(journal, "wal") {
		t.Fatalf(
			"configuration = version:%d fk:%d busy:%d sync:%d journal:%q",
			version, foreignKeys, busyTimeout, synchronous, journal,
		)
	}
	if runtime.GOOS != "windows" {
		if info, statErr := os.Stat(path); statErr != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("database mode = %v, %v", info, statErr)
		}
		if info, statErr := os.Stat(filepath.Dir(path)); statErr != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("database directory mode = %v, %v", info, statErr)
		}
		if _, execErr := db.Exec(`INSERT INTO records(id, value) VALUES ('companion', 'mode')`); execErr != nil {
			t.Fatal(execErr)
		}
		for _, companion := range []string{path + "-wal", path + "-shm"} {
			info, statErr := os.Stat(companion)
			if statErr != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("SQLite companion %s mode = %v, %v", filepath.Base(companion), info, statErr)
			}
		}
	}
}

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestOpenRejectsTooNewAndRollsBackFailedMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := Open(t.Context(), path, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 9"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(t.Context(), path, testOptions()); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("too-new Open() error = %v", err)
	}

	failedPath := filepath.Join(t.TempDir(), "failed.db")
	options := testOptions()
	options.Migrations = append(options.Migrations, Migration{
		Version:    2,
		Statements: []string{"CREATE TABLE transient (id INTEGER)", "invalid SQL"},
	})
	if _, err := Open(t.Context(), failedPath, options); err == nil {
		t.Fatal("failed migration unexpectedly succeeded")
	}
	raw, err := sql.Open("sqlite", failedPath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var count int
	if err := raw.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'transient'`,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("failed migration left its table behind")
	}
}

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestOpenImportsArchivesAndDoesNotReimport(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(root, "legacy.json")
	if err := os.WriteFile(legacyPath, []byte("first\ninvalid\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	archiveRoot := filepath.Join(root, "legacy-json", "test-store-v1")
	options := testOptions()
	imports := 0
	options.Legacy = &LegacyOptions{
		SourceRoot:  root,
		ArchiveRoot: archiveRoot,
		Sources: func() ([]LegacySource, error) {
			return []LegacySource{{ID: "legacy", Relative: "legacy.json"}}, nil
		},
		Now: func() time.Time { return time.Date(2026, 8, 31, 1, 2, 3, 4, time.UTC) },
		Import: func(ctx context.Context, conn *sql.Conn, input LegacyInput) (ImportResult, error) {
			imports++
			for _, value := range []string{"first", "second"} {
				if _, err := conn.ExecContext(
					ctx,
					`INSERT INTO records(id, value) VALUES (?, ?)`,
					value,
					value,
				); err != nil {
					return ImportResult{}, err
				}
			}
			return ImportResult{
				Imported: 2,
				Skipped:  1,
				Issues: []ImportIssue{{
					Code: "invalid-line", RecordDigest: sha256.Sum256([]byte("invalid")),
				}},
			}, nil
		},
	}
	path := filepath.Join(root, "db", "store.db")
	db, err := Open(t.Context(), path, options)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if imports != 1 {
		t.Fatalf("imports = %d, want 1", imports)
	}
	if _, err := os.Stat(legacyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy source still exists: %v", err)
	}
	archived := filepath.Join(archiveRoot, "legacy.json")
	if data, err := os.ReadFile(archived); err != nil || string(data) != "first\ninvalid\nsecond\n" {
		t.Fatalf("archive = %q, %v", data, err)
	}
	var status string
	if err := db.QueryRow(
		`SELECT archive_status FROM storage_imports WHERE component = 'test-store'`,
	).Scan(&status); err != nil || status != "complete" {
		t.Fatalf("archive status = %q, %v", status, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(t.Context(), path, options)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	defer reopened.Close()
	if imports != 1 {
		t.Fatalf("imports after reopen = %d, want 1", imports)
	}
}

func TestLegacySealRunsForEmptyEnumerationAndAfterFinalizer(t *testing.T) {
	t.Run("empty enumeration", func(t *testing.T) {
		root := t.TempDir()
		options := testOptions()
		options.Migrations[0].Statements = append(
			options.Migrations[0].Statements,
			`CREATE TABLE import_horizon(singleton INTEGER PRIMARY KEY CHECK(singleton = 1)) STRICT`,
		)
		sealCalls := 0
		options.Legacy = &LegacyOptions{
			SourceRoot:  root,
			ArchiveRoot: filepath.Join(root, "archive"),
			Sources:     func() ([]LegacySource, error) { return nil, nil },
			Import: func(context.Context, *sql.Conn, LegacyInput) (ImportResult, error) {
				t.Fatal("empty enumeration invoked importer")
				return ImportResult{}, nil
			},
			Seal: func(ctx context.Context, conn *sql.Conn) error {
				sealCalls++
				_, err := conn.ExecContext(ctx, `INSERT OR IGNORE INTO import_horizon(singleton) VALUES (1)`)
				return err
			},
		}
		path := filepath.Join(root, "store.db")
		for attempt := 1; attempt <= 2; attempt++ {
			database, err := Open(t.Context(), path, options)
			if err != nil {
				t.Fatal(err)
			}
			var rows int
			if err := database.QueryRow(`SELECT COUNT(*) FROM import_horizon`).Scan(&rows); err != nil || rows != 1 {
				t.Fatalf("sealed empty enumeration = rows:%d error:%v", rows, err)
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
		}
		if sealCalls != 2 {
			t.Fatalf("empty-enumeration seal calls = %d", sealCalls)
		}
	})

	t.Run("after finalizer", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "legacy.json"), []byte("value"), 0o600); err != nil {
			t.Fatal(err)
		}
		var order []string
		options := testOptions()
		options.Legacy = &LegacyOptions{
			SourceRoot:  root,
			ArchiveRoot: filepath.Join(root, "archive"),
			Sources: func() ([]LegacySource, error) {
				return []LegacySource{{ID: "legacy", Relative: "legacy.json"}}, nil
			},
			Import: func(context.Context, *sql.Conn, LegacyInput) (ImportResult, error) {
				order = append(order, "import")
				return ImportResult{Imported: 1}, nil
			},
			Finalize: func(context.Context, *sql.Conn, LegacyFinalizeInput) error {
				order = append(order, "finalize")
				return nil
			},
			Seal: func(context.Context, *sql.Conn) error {
				order = append(order, "seal")
				return nil
			},
		}
		database, err := Open(t.Context(), filepath.Join(root, "store.db"), options)
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		if strings.Join(order, ",") != "import,finalize,seal" {
			t.Fatalf("legacy closeout order = %v", order)
		}
	})
}

func TestLegacySealFailureRollsBackMigrationAndPreventsArchive(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "legacy.json")
	if err := os.WriteFile(legacy, []byte("value"), 0o600); err != nil {
		t.Fatal(err)
	}
	options := testOptions()
	options.Legacy = &LegacyOptions{
		SourceRoot:  root,
		ArchiveRoot: filepath.Join(root, "archive"),
		Sources: func() ([]LegacySource, error) {
			return []LegacySource{{ID: "legacy", Relative: "legacy.json"}}, nil
		},
		Import: func(ctx context.Context, conn *sql.Conn, _ LegacyInput) (ImportResult, error) {
			_, err := conn.ExecContext(ctx, `INSERT INTO records(id, value) VALUES ('legacy', 'value')`)
			return ImportResult{Imported: 1}, err
		},
		Seal: func(context.Context, *sql.Conn) error {
			return errors.New("injected seal failure")
		},
	}
	path := filepath.Join(root, "store.db")
	if _, err := Open(t.Context(), path, options); err == nil || !strings.Contains(err.Error(), "seal") {
		t.Fatalf("seal failure Open() error = %v", err)
	}
	if _, err := os.Lstat(legacy); err != nil {
		t.Fatalf("seal failure removed legacy source: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "archive", "legacy.json")); !os.IsNotExist(err) {
		t.Fatalf("seal failure published archive: %v", err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var records int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name = 'records'`).Scan(&records); err != nil {
		t.Fatal(err)
	}
	if records != 0 {
		t.Fatal("seal failure committed schema migration")
	}
}

func TestLegacyImportFailureRollsBackAndUnsafeSourcesAbort(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(root, "legacy.json")
	if err := os.WriteFile(legacyPath, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	options := testOptions()
	options.Legacy = &LegacyOptions{
		SourceRoot:  root,
		ArchiveRoot: filepath.Join(root, "legacy-json", "test-store-v1"),
		Sources: func() ([]LegacySource, error) {
			return []LegacySource{{ID: "legacy", Relative: "legacy.json"}}, nil
		},
		Import: func(ctx context.Context, conn *sql.Conn, _ LegacyInput) (ImportResult, error) {
			if _, err := conn.ExecContext(ctx, `INSERT INTO records(id, value) VALUES ('id', 'value')`); err != nil {
				return ImportResult{}, err
			}
			return ImportResult{}, errors.New("injected failure")
		},
	}
	path := filepath.Join(root, "db", "store.db")
	if _, err := Open(t.Context(), path, options); err == nil {
		t.Fatal("failed import unexpectedly succeeded")
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var count int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'records'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("failed first migration committed schema or data")
	}

	if runtime.GOOS != "windows" {
		unsafeRoot := t.TempDir()
		if err := os.Chmod(unsafeRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		unsafePath := filepath.Join(unsafeRoot, "legacy.json")
		if err := os.WriteFile(unsafePath, []byte("payload"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(unsafePath, 0o622); err != nil {
			t.Fatal(err)
		}
		unsafe := testOptions()
		unsafe.Legacy = &LegacyOptions{
			SourceRoot:  unsafeRoot,
			ArchiveRoot: filepath.Join(unsafeRoot, "legacy-json", "test-store-v1"),
			Sources: func() ([]LegacySource, error) {
				return []LegacySource{{ID: "legacy", Relative: "legacy.json"}}, nil
			},
			Import: func(context.Context, *sql.Conn, LegacyInput) (ImportResult, error) {
				return ImportResult{}, nil
			},
		}
		if _, err := Open(t.Context(), filepath.Join(unsafeRoot, "db", "store.db"), unsafe); err == nil ||
			!strings.Contains(err.Error(), "unsafe") {
			t.Fatalf("unsafe source Open() error = %v", err)
		}
	}
}
