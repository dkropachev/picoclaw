package cron

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

//nolint:govet // Independent assertions intentionally reuse short declarations.
func TestCronSQLiteSchemaConfigurationPermissionsAndReopen(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cron")
	databasePath := filepath.Join(root, cronDatabaseFilename)
	service, err := NewSQLiteCronService(databasePath, nil)
	if err != nil {
		t.Fatalf("NewSQLiteCronService() error = %v", err)
	}
	every := int64(60_000)
	if _, err := service.AddJob(
		"durable",
		CronSchedule{Kind: "every", EveryMS: &every},
		"hello",
		"cli",
		"direct",
	); err != nil {
		t.Fatalf("AddJob() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, cronLegacyFilename)); !os.IsNotExist(err) {
		t.Fatalf("mutable legacy JSON exists: %v", err)
	}
	db, err := service.storage.open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var userVersion, foreignKeys, busyTimeout, synchronous int
	var journalMode string
	for query, destination := range map[string]any{
		"PRAGMA user_version": &userVersion,
		"PRAGMA foreign_keys": &foreignKeys,
		"PRAGMA busy_timeout": &busyTimeout,
		"PRAGMA synchronous":  &synchronous,
		"PRAGMA journal_mode": &journalMode,
	} {
		if err := db.QueryRowContext(t.Context(), query).Scan(destination); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	if userVersion != 1 || foreignKeys != 1 || busyTimeout != 5000 || synchronous != 2 ||
		!strings.EqualFold(journalMode, "wal") {
		t.Fatalf(
			"PRAGMAs = version:%d foreign:%d busy:%d sync:%d journal:%q",
			userVersion,
			foreignKeys,
			busyTimeout,
			synchronous,
			journalMode,
		)
	}
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCronSchema(t.Context(), conn); err != nil {
		conn.Close()
		t.Fatalf("validateCronSchema() error = %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		assertCronMode(t, root, 0o700)
		assertCronMode(t, databasePath, 0o600)
	}
	reopened, err := NewSQLiteCronService(databasePath, nil)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	jobs := reopened.ListJobs(true)
	if len(jobs) != 1 || jobs[0].Name != "durable" || jobs[0].Schedule.EveryMS == nil ||
		*jobs[0].Schedule.EveryMS != every {
		t.Fatalf("reopened jobs = %#v", jobs)
	}
}

//nolint:govet // Independent assertions intentionally reuse short declarations.
func TestCronSQLiteLegacyImportAuditArchiveAndIdempotence(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cron")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(root, cronLegacyFilename)
	legacy := []byte(`{
  "version": 1,
  "jobs": [
    {"id":"first","name":"first valid","enabled":true,"schedule":{"kind":"every","everyMs":60000},"payload":{"kind":"agent_turn","message":"one"},"state":{},"createdAtMs":1,"updatedAtMs":2,"deleteAfterRun":false},
    {"id":"","name":"invalid","enabled":true,"schedule":{"kind":"every","everyMs":60000},"payload":{"kind":"agent_turn"},"state":{},"createdAtMs":1,"updatedAtMs":2,"deleteAfterRun":false},
    {"id":"first","name":"duplicate","enabled":true,"schedule":{"kind":"every","everyMs":60000},"payload":{"kind":"agent_turn"},"state":{},"createdAtMs":1,"updatedAtMs":2,"deleteAfterRun":false},
    {"id":"second","name":"second valid","enabled":false,"schedule":{"kind":"at","atMs":2000000000000},"payload":{"kind":"agent_turn","message":"two"},"state":{},"createdAtMs":3,"updatedAtMs":4,"deleteAfterRun":true}
  ]
}`)
	if err := os.WriteFile(legacyPath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(legacyPath, 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewCronService(legacyPath, nil)
	if service.initErr != nil {
		t.Fatalf("legacy migration error = %v", service.initErr)
	}
	jobs := service.ListJobs(true)
	if len(jobs) != 2 || jobs[0].ID != "first" || jobs[1].ID != "second" {
		t.Fatalf("migrated jobs = %#v", jobs)
	}
	archivePath := filepath.Join(
		root,
		"legacy-json",
		cronLegacyArchiveLabel,
		cronLegacyFilename,
	)
	archived, err := os.ReadFile(archivePath)
	if err != nil || string(archived) != string(legacy) {
		t.Fatalf("archive = %q, %v", archived, err)
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("legacy source retained: %v", err)
	}
	db, err := service.storage.open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var imported, skipped, issues int
	if err := db.QueryRowContext(t.Context(), `SELECT imported_count, skipped_count
        FROM storage_imports WHERE component = ? AND source_id = ?`,
		cronDatabaseComponent,
		cronLegacySourceID,
	).Scan(&imported, &skipped); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM storage_import_issues
        WHERE component = ? AND source_id = ?`,
		cronDatabaseComponent,
		cronLegacySourceID,
	).Scan(&issues); err != nil {
		t.Fatal(err)
	}
	if imported != 2 || skipped != 2 || issues != 2 {
		t.Fatalf("audit = imported:%d skipped:%d issues:%d", imported, skipped, issues)
	}
	reopened := NewCronService(legacyPath, nil)
	if reopened.initErr != nil || len(reopened.ListJobs(true)) != 2 {
		t.Fatalf("idempotent reopen = %#v, %v", reopened.ListJobs(true), reopened.initErr)
	}
}

func TestCronSQLiteConcurrentServicesDoNotLoseJobs(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "cron", cronDatabaseFilename)
	first, err := NewSQLiteCronService(databasePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewSQLiteCronService(databasePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	const writers = 8
	start := make(chan struct{})
	errorsByWriter := make(chan error, writers)
	var wait sync.WaitGroup
	for index := 0; index < writers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			service := first
			if index%2 != 0 {
				service = second
			}
			every := int64(60_000 + index)
			_, err := service.AddJob(
				fmt.Sprintf("job-%02d", index),
				CronSchedule{Kind: "every", EveryMS: &every},
				fmt.Sprintf("payload-%02d", index),
				"cli",
				"direct",
			)
			errorsByWriter <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errorsByWriter)
	for err := range errorsByWriter {
		if err != nil {
			t.Fatalf("concurrent AddJob() error = %v", err)
		}
	}
	jobs := NewCronService(databasePath, nil).ListJobs(true)
	if len(jobs) != writers {
		t.Fatalf("job count = %d, want %d", len(jobs), writers)
	}
}

func TestCronSQLiteRejectsUnsafeLegacyCorruptionAndFutureSchema(t *testing.T) {
	t.Run("unsafe legacy mode", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("POSIX mode assertion")
		}
		root := filepath.Join(t.TempDir(), "cron")
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		legacyPath := filepath.Join(root, cronLegacyFilename)
		if err := os.WriteFile(legacyPath, []byte(`{"version":1,"jobs":[]}`), 0o666); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(legacyPath, 0o666); err != nil {
			t.Fatal(err)
		}
		if _, err := NewSQLiteCronService(filepath.Join(root, cronDatabaseFilename), nil); err == nil {
			t.Fatal("unsafe legacy mode was accepted")
		}
		assertCronNotArchived(t, root, legacyPath)
	})

	t.Run("corrupt database", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "cron")
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, cronDatabaseFilename)
		if err := os.WriteFile(path, []byte("not sqlite"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewSQLiteCronService(path, nil); err == nil {
			t.Fatal("corrupt SQLite database was accepted")
		}
	})

	t.Run("future schema", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "cron", cronDatabaseFilename)
		service, err := NewSQLiteCronService(path, nil)
		if err != nil {
			t.Fatal(err)
		}
		db, err := service.storage.open(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(), `PRAGMA user_version = 99`); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := NewSQLiteCronService(path, nil); !errors.Is(err, sqlitestore.ErrTooNew) {
			t.Fatalf("future schema error = %v", err)
		}
	})
}

//nolint:govet // Independent assertions intentionally reuse short declarations.
func TestCronSQLiteTransactionRollbackPreservesExistingJobs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cron", cronDatabaseFilename)
	service, err := NewSQLiteCronService(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	every := int64(60_000)
	if _, err := service.AddJob(
		"before",
		CronSchedule{Kind: "every", EveryMS: &every},
		"payload",
		"cli",
		"direct",
	); err != nil {
		t.Fatal(err)
	}
	db, err := service.storage.open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected rollback")
	err = sqlitestore.Immediate(t.Context(), db, func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(t.Context(), `DELETE FROM cron_jobs`); err != nil {
			return err
		}
		return injected
	})
	if closeErr := db.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if !errors.Is(err, injected) {
		t.Fatalf("Immediate() error = %v", err)
	}
	jobs := service.ListJobs(true)
	if len(jobs) != 1 || jobs[0].Name != "before" {
		t.Fatalf("jobs after rollback = %#v", jobs)
	}
}

func assertCronMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode(%s) = %04o, want %04o", path, got, want)
	}
}

func assertCronNotArchived(t *testing.T, root, legacyPath string) {
	t.Helper()
	if _, err := os.Stat(legacyPath); err != nil {
		t.Fatalf("legacy source missing after failed migration: %v", err)
	}
	archivePath := filepath.Join(
		root,
		"legacy-json",
		cronLegacyArchiveLabel,
		cronLegacyFilename,
	)
	if _, err := os.Stat(archivePath); !os.IsNotExist(err) {
		t.Fatalf("failed migration produced archive: %v", err)
	}
}
