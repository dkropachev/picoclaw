package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestOpenValidatesInputsAndLimits(t *testing.T) {
	validPath := func(t *testing.T) string {
		t.Helper()
		return filepath.Join(t.TempDir(), "store.db")
	}
	tests := []struct {
		name    string
		path    func(*testing.T) string
		mutate  func(*Options)
		context func() context.Context
	}{
		{name: "canceled context", path: validPath, context: func() context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}},
		{name: "empty component", path: validPath, mutate: func(options *Options) {
			options.Component = ""
		}},
		{name: "uppercase component", path: validPath, mutate: func(options *Options) {
			options.Component = "Invalid"
		}},
		{name: "long component", path: validPath, mutate: func(options *Options) {
			options.Component = strings.Repeat("a", 81)
		}},
		{name: "missing migrations", path: validPath, mutate: func(options *Options) {
			options.Migrations = nil
		}},
		{name: "empty path", path: func(*testing.T) string { return "" }},
		{name: "padded path", path: func(*testing.T) string { return " store.db " }},
		{name: "NUL path", path: func(*testing.T) string { return "store\x00.db" }},
		{name: "URI path", path: func(*testing.T) string { return "FiLe:store.db" }},
		{name: "short busy timeout", path: validPath, mutate: func(options *Options) {
			options.BusyTimeout = time.Nanosecond
		}},
		{name: "long busy timeout", path: validPath, mutate: func(options *Options) {
			options.BusyTimeout = time.Minute + time.Millisecond
		}},
		{name: "negative connections", path: validPath, mutate: func(options *Options) {
			options.MaxOpenConns = -1
		}},
		{name: "too many connections", path: validPath, mutate: func(options *Options) {
			options.MaxOpenConns = 33
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := testOptions()
			if test.mutate != nil {
				test.mutate(&options)
			}
			ctx := context.Background()
			if test.context != nil {
				ctx = test.context()
			}
			if db, err := Open(ctx, test.path(t), options); err == nil {
				db.Close()
				t.Fatal("Open() unexpectedly succeeded")
			}
		})
	}
}

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestOpenMemoryDatabasesAreIsolatedAndNilContextIsAccepted(t *testing.T) {
	first, err := Open(nil, ":memory:", testOptions())
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	defer first.Close()
	second, err := Open(t.Context(), ":memory:", testOptions())
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	defer second.Close()
	if _, err := first.Exec(`INSERT INTO records(id, value) VALUES ('first', 'one')`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := second.QueryRow(`SELECT COUNT(*) FROM records`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("second in-memory database observed %d rows from first", count)
	}
	firstDSN, err := sqliteDSN(":memory:", DefaultBusyTimeout)
	if err != nil {
		t.Fatal(err)
	}
	secondDSN, err := sqliteDSN(":memory:", DefaultBusyTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if firstDSN == secondDSN || !strings.Contains(firstDSN, "mode=memory") {
		t.Fatalf("in-memory DSNs are not isolated: %q / %q", firstDSN, secondDSN)
	}
}

func TestOpenRejectsUnsafeDatabaseEndpointsAndSidecars(t *testing.T) {
	t.Run("blocked parent", func(t *testing.T) {
		root := t.TempDir()
		blocker := filepath.Join(root, "blocker")
		if err := os.WriteFile(blocker, []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		if db, err := Open(t.Context(), filepath.Join(blocker, "store.db"), testOptions()); err == nil {
			db.Close()
			t.Fatal("Open() accepted a non-directory parent")
		}
	})

	for _, endpoint := range []string{"directory", "symlink"} {
		t.Run(endpoint, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "store.db")
			switch endpoint {
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(root, "target")
				if err := os.WriteFile(target, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			}
			if db, err := Open(t.Context(), path, testOptions()); err == nil {
				db.Close()
				t.Fatalf("Open() accepted %s endpoint", endpoint)
			}
		})
	}

	t.Run("symlinked sidecar", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "store.db")
		target := filepath.Join(root, "target")
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path+"-wal"); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if db, err := Open(t.Context(), path, testOptions()); err == nil {
			db.Close()
			t.Fatal("Open() accepted a symlinked WAL")
		}
	})

	t.Run("corrupt database", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "store.db")
		if err := os.WriteFile(path, []byte("not a SQLite database"), 0o600); err != nil {
			t.Fatal(err)
		}
		if db, err := Open(t.Context(), path, testOptions()); err == nil {
			db.Close()
			t.Fatal("Open() accepted corrupt bytes")
		}
	})
}

func TestPrivateDirectoryAndDatabasePreparationEdges(t *testing.T) {
	for _, invalid := range []string{"", " \t", "bad\x00dir"} {
		if err := EnsurePrivateDir(invalid); err == nil {
			t.Fatalf("EnsurePrivateDir(%q) unexpectedly succeeded", invalid)
		}
	}
	root := t.TempDir()
	private := filepath.Join(root, "private")
	if err := EnsurePrivateDir(private); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePrivateDir(private); err != nil {
		t.Fatalf("EnsurePrivateDir(reopen) error = %v", err)
	}
	if info, err := os.Stat(private); err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Fatalf("private directory = %v, %v", info, err)
	}
	blocked := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePrivateDir(blocked); err == nil {
		t.Fatal("EnsurePrivateDir() accepted a file")
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(private, alias); err == nil {
		if err := EnsurePrivateDir(alias); err == nil {
			t.Fatal("EnsurePrivateDir() accepted a symlink")
		}
	}

	databasePath := filepath.Join(private, "prepared.db")
	if err := prepareDatabaseFile(databasePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(databasePath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := prepareDatabaseFile(databasePath); err != nil {
		t.Fatalf("prepareDatabaseFile(reopen) error = %v", err)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(databasePath); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("prepared database = %v, %v", info, err)
		}
	}
}

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestSQLiteDSNAndConfigurationEdges(t *testing.T) {
	dsn, err := sqliteDSN(filepath.Join(t.TempDir(), "space name.db"), 1234*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"file:", "foreign_keys", "busy_timeout", "synchronous"} {
		if !strings.Contains(dsn, required) {
			t.Fatalf("sqliteDSN() = %q, missing %q", dsn, required)
		}
	}
	if got := sqliteFilePath("C:/data/store.db", "C:"); got != "/C:/data/store.db" {
		t.Fatalf("sqliteFilePath(Windows drive) = %q", got)
	}
	if got := sqliteFilePath("/var/lib/store.db", ""); got != "/var/lib/store.db" {
		t.Fatalf("sqliteFilePath(POSIX) = %q", got)
	}

	closed, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := configure(t.Context(), closed, DefaultBusyTimeout, true, "test"); err == nil {
		t.Fatal("configure() accepted a closed database")
	}

	memory, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	if err := configure(t.Context(), memory, DefaultBusyTimeout, false, "test"); err == nil ||
		!strings.Contains(err.Error(), "selected") {
		t.Fatalf("configure(file contract on memory) error = %v", err)
	}
	if err := configure(t.Context(), memory, DefaultBusyTimeout, true, "test"); err == nil ||
		!strings.Contains(err.Error(), "configuration") {
		t.Fatalf("configure(unconfigured memory) error = %v", err)
	}
}

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestConfigureWALRetriesBusyDatabaseWithinBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "busy.db")
	owner, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	contender, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer contender.Close()
	if _, err := owner.Exec(`CREATE TABLE canary (id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	ownerConn, err := owner.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer ownerConn.Close()
	if _, err := ownerConn.ExecContext(t.Context(), `BEGIN EXCLUSIVE`); err != nil {
		t.Fatal(err)
	}
	released := make(chan error, 1)
	go func() {
		timer := time.NewTimer(20 * time.Millisecond)
		defer timer.Stop()
		<-timer.C
		_, releaseErr := ownerConn.ExecContext(context.Background(), `ROLLBACK`)
		released <- releaseErr
	}()
	var journal string
	if err := configureWAL(t.Context(), contender, time.Second, &journal); err != nil {
		t.Fatalf("configureWAL() error = %v", err)
	}
	if releaseErr := <-released; releaseErr != nil {
		t.Fatal(releaseErr)
	}
	if !strings.EqualFold(journal, "wal") {
		t.Fatalf("journal mode = %q, want wal", journal)
	}

	timeoutPath := filepath.Join(t.TempDir(), "timeout.db")
	timeoutOwner, err := sql.Open("sqlite", timeoutPath)
	if err != nil {
		t.Fatal(err)
	}
	defer timeoutOwner.Close()
	timeoutContender, err := sql.Open("sqlite", timeoutPath)
	if err != nil {
		t.Fatal(err)
	}
	defer timeoutContender.Close()
	if _, err := timeoutOwner.Exec(`CREATE TABLE canary (id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	timeoutConn, err := timeoutOwner.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer timeoutConn.Close()
	if _, err := timeoutConn.ExecContext(t.Context(), `BEGIN EXCLUSIVE`); err != nil {
		t.Fatal(err)
	}
	if err := configureWAL(t.Context(), timeoutContender, 2*time.Millisecond, &journal); err == nil {
		t.Fatal("configureWAL() unexpectedly outwaited its bound")
	}
	if _, err := timeoutConn.ExecContext(t.Context(), `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	if sqliteBusyOrLocked(errors.New("ordinary error")) {
		t.Fatal("sqliteBusyOrLocked() classified an ordinary error as retryable")
	}
}

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestMigrationApplyValidationAndImmediateEdges(t *testing.T) {
	t.Run("apply and validate", func(t *testing.T) {
		options := testOptions()
		options.Migrations = append(options.Migrations, Migration{
			Version: 2,
			Apply: func(ctx context.Context, conn *sql.Conn) error {
				_, err := conn.ExecContext(ctx, `ALTER TABLE records ADD COLUMN applied TEXT`)
				return err
			},
		})
		options.Validate = func(ctx context.Context, conn *sql.Conn) error {
			var count int
			return conn.QueryRowContext(
				ctx,
				`SELECT COUNT(*) FROM pragma_table_info('records') WHERE name = 'applied'`,
			).Scan(&count)
		}
		db, err := Open(t.Context(), filepath.Join(t.TempDir(), "store.db"), options)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
	})

	t.Run("apply failure", func(t *testing.T) {
		options := testOptions()
		options.Migrations[0].Apply = func(context.Context, *sql.Conn) error {
			return errors.New("apply canary")
		}
		if db, err := Open(t.Context(), filepath.Join(t.TempDir(), "store.db"), options); err == nil {
			db.Close()
			t.Fatal("Open() accepted a failed data migration")
		}
	})

	t.Run("validation failure", func(t *testing.T) {
		options := testOptions()
		options.Validate = func(context.Context, *sql.Conn) error {
			return errors.New("validation canary")
		}
		if db, err := Open(t.Context(), filepath.Join(t.TempDir(), "store.db"), options); err == nil ||
			!errors.Is(err, ErrInvalidSchema) {
			if db != nil {
				db.Close()
			}
			t.Fatalf("Open() error = %v, want ErrInvalidSchema", err)
		}
	})

	db, err := Open(t.Context(), filepath.Join(t.TempDir(), "immediate.db"), testOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Immediate(nil, db, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(context.Background(),
			`INSERT INTO records(id, value) VALUES ('nil-context', 'ok')`)
		return err
	}); err != nil {
		t.Fatalf("Immediate(nil) error = %v", err)
	}
	if err := Immediate(t.Context(), db, nil); err == nil {
		t.Fatal("Immediate() accepted nil callback")
	}
	if err := Immediate(t.Context(), db, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(t.Context(), "ROLLBACK")
		return err
	}); err == nil {
		t.Fatal("Immediate() did not report failed COMMIT after callback rollback")
	}
	closed, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Immediate(t.Context(), closed, func(*sql.Conn) error { return nil }); err == nil {
		t.Fatal("Immediate() accepted closed database")
	}
}

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestIntegrityAndSQLiteFileHelpersRejectInvalidState(t *testing.T) {
	closed, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := integrityCheck(t.Context(), closed, "closed"); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("integrityCheck() error = %v, want ErrIntegrity", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	open, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()
	if err := foreignKeyCheckQuery(canceled, open, "canceled"); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("foreignKeyCheckQuery() error = %v, want ErrIntegrity", err)
	}

	root := t.TempDir()
	if err := secureSQLiteFiles(filepath.Join(root, "missing.db")); !os.IsNotExist(err) {
		t.Fatalf("secureSQLiteFiles(missing) error = %v, want not-exist", err)
	}
	regular := filepath.Join(root, "regular.db")
	if err := os.WriteFile(regular, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := secureSQLiteFiles(regular); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(regular); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("secured SQLite file = %v, %v", info, err)
		}
	}
	directory := filepath.Join(root, "directory.db")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := secureSQLiteFiles(directory); err == nil {
		t.Fatal("secureSQLiteFiles() accepted a directory")
	}
	alias := filepath.Join(root, "alias.db")
	if err := os.Symlink(regular, alias); err == nil {
		if err := secureSQLiteFiles(alias); err == nil {
			t.Fatal("secureSQLiteFiles() accepted a symlink")
		}
	}
}

func TestIdentifierAndPathValidationTables(t *testing.T) {
	for _, valid := range []string{"a", "store-1", "store_name", strings.Repeat("z", 80)} {
		if !validIdentifier(valid) {
			t.Fatalf("validIdentifier(%q) = false", valid)
		}
	}
	for _, invalid := range []string{"", "A", "with space", "slash/name", strings.Repeat("z", 81)} {
		if validIdentifier(invalid) {
			t.Fatalf("validIdentifier(%q) = true", invalid)
		}
	}
	for _, invalid := range []string{"", " ", "file:db", "x\x00y"} {
		if err := validateDatabasePath(invalid); err == nil {
			t.Fatalf("validateDatabasePath(%q) unexpectedly succeeded", invalid)
		}
	}
	if err := validateDatabasePath("ordinary.db"); err != nil {
		t.Fatalf("validateDatabasePath(valid) error = %v", err)
	}
}
