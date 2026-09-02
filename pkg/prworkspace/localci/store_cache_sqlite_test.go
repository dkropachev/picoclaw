package localci

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
)

func TestLocalCIPassingCacheSQLiteSchemaDurabilityAndReopen(t *testing.T) {
	root := filepath.Join(t.TempDir(), "evidence")
	store, err := OpenFileEvidenceStore(root)
	if err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(root, localCICacheDatabaseName)
	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(databasePath)
		if statErr != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("cache database mode = %v, %v", info, statErr)
		}
	}
	var version, foreignKeys, synchronous, busyTimeout int
	var journalMode string
	for query, target := range map[string]any{
		"PRAGMA user_version": &version,
		"PRAGMA foreign_keys": &foreignKeys,
		"PRAGMA synchronous":  &synchronous,
		"PRAGMA busy_timeout": &busyTimeout,
		"PRAGMA journal_mode": &journalMode,
	} {
		if err = store.cacheDB.QueryRow(query).Scan(target); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	if version != 1 || foreignKeys != 1 || synchronous != 2 || busyTimeout != 5000 || journalMode != "wal" {
		t.Fatalf(
			"SQLite configuration = version %d, fk %d, sync %d, busy %d, journal %q",
			version,
			foreignKeys,
			synchronous,
			busyTimeout,
			journalMode,
		)
	}
	for _, index := range []string{"passing_cache_expiry_idx", "passing_cache_execution_idx"} {
		var count int
		if err = store.cacheDB.QueryRow(
			`SELECT COUNT(*) FROM sqlite_schema WHERE type = 'index' AND name = ?`,
			index,
		).Scan(&count); err != nil || count != 1 {
			t.Fatalf("index %s count = %d, %v", index, count, err)
		}
	}
	cacheEntries, err := os.ReadDir(filepath.Join(root, "cache"))
	if err != nil || len(cacheEntries) != 0 {
		t.Fatalf("legacy cache directory entries = %d, %v", len(cacheEntries), err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	reopened, err := OpenFileEvidenceStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err = reopened.cacheDB.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 1 {
		t.Fatalf("reopened user_version = %d, %v", version, err)
	}
}

func TestLocalCIPassingCacheSQLiteConstraintsAndFutureSchema(t *testing.T) {
	t.Run("constraints", func(t *testing.T) {
		store, err := OpenFileEvidenceStore(filepath.Join(t.TempDir(), "evidence"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		_, err = store.cacheDB.Exec(`INSERT INTO passing_cache (
            result_key, execution_digest, execution_status,
            created_at_unix_seconds, created_at_nanosecond,
            expires_at_unix_seconds, expires_at_nanosecond, version
        ) VALUES (?, ?, 'failed', 1, 0, 2, 0, 1)`,
			strings.Repeat("A", 64),
			strings.Repeat("b", 64),
		)
		if err == nil {
			t.Fatal("invalid passing-cache row was accepted")
		}
	})

	t.Run("too new", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "evidence")
		store, err := OpenFileEvidenceStore(root)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.Close(); err != nil {
			t.Fatal(err)
		}
		raw := openRawLocalCICacheDatabase(t, root)
		if _, err = raw.Exec(`PRAGMA user_version = 2`); err != nil {
			t.Fatal(err)
		}
		if err = raw.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err = OpenFileEvidenceStore(root); !errors.Is(err, sqlitestore.ErrTooNew) {
			t.Fatalf("OpenFileEvidenceStore(too new) error = %v", err)
		}
	})

	t.Run("invalid schema", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "evidence")
		store, err := OpenFileEvidenceStore(root)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.Close(); err != nil {
			t.Fatal(err)
		}
		raw := openRawLocalCICacheDatabase(t, root)
		if _, err = raw.Exec(`DROP INDEX passing_cache_execution_idx`); err != nil {
			t.Fatal(err)
		}
		if err = raw.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err = OpenFileEvidenceStore(root); !errors.Is(err, sqlitestore.ErrInvalidSchema) {
			t.Fatalf("OpenFileEvidenceStore(invalid schema) error = %v", err)
		}
	})

	t.Run("unexpected schema object", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "evidence")
		store, err := OpenFileEvidenceStore(root)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.Close(); err != nil {
			t.Fatal(err)
		}
		raw := openRawLocalCICacheDatabase(t, root)
		if _, err = raw.Exec(`CREATE TABLE unexpected (value TEXT) STRICT`); err != nil {
			t.Fatal(err)
		}
		if err = raw.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err = OpenFileEvidenceStore(root); !errors.Is(err, sqlitestore.ErrInvalidSchema) {
			t.Fatalf("OpenFileEvidenceStore(unexpected schema) error = %v", err)
		}
	})

	t.Run("unexpected unique index", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "evidence")
		store, err := OpenFileEvidenceStore(root)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.Close(); err != nil {
			t.Fatal(err)
		}
		raw := openRawLocalCICacheDatabase(t, root)
		if _, err = raw.Exec(`CREATE UNIQUE INDEX passing_cache_unexpected_unique
            ON passing_cache(execution_digest)`); err != nil {
			t.Fatal(err)
		}
		if err = raw.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err = OpenFileEvidenceStore(root); !errors.Is(err, sqlitestore.ErrInvalidSchema) {
			t.Fatalf("OpenFileEvidenceStore(unexpected unique index) error = %v", err)
		}
	})
}

func TestLocalCIPassingCacheConcurrentPromotionsUseVersionFence(t *testing.T) {
	root := filepath.Join(t.TempDir(), "evidence")
	first, err := OpenFileEvidenceStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenFileEvidenceStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	now := time.Date(2026, 8, 31, 12, 0, 0, 123, time.UTC)
	first.now = func() time.Time { return now }
	second.now = func() time.Time { return now }
	plan := validTestPlan(t)
	if err = first.PutPlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	execution := validTestExecution(t, plan, now, StatusPassed)
	if err = first.PutExecution(context.Background(), execution); err != nil {
		t.Fatal(err)
	}

	const writers = 24
	var wait sync.WaitGroup
	errorsSeen := make(chan error, writers)
	for index := range writers {
		wait.Add(1)
		go func(writer *FileEvidenceStore) {
			defer wait.Done()
			errorsSeen <- writer.PromotePassing(
				context.Background(),
				execution.ResultKey,
				execution.Digest,
			)
		}([]*FileEvidenceStore{first, second}[index%2])
	}
	wait.Wait()
	close(errorsSeen)
	for promoteErr := range errorsSeen {
		if promoteErr != nil {
			t.Fatalf("concurrent PromotePassing() error = %v", promoteErr)
		}
	}
	var version int
	if err = first.cacheDB.QueryRow(
		`SELECT version FROM passing_cache WHERE result_key = ?`,
		execution.ResultKey,
	).Scan(&version); err != nil || version != writers {
		t.Fatalf("passing-cache version = %d, %v; want %d", version, err, writers)
	}
	loaded, found, err := second.LookupPassing(context.Background(), execution.ResultKey)
	if err != nil || !found || loaded.Digest != execution.Digest {
		t.Fatalf("LookupPassing() = (%#v, %v, %v)", loaded, found, err)
	}
	if _, statErr := os.Stat(first.objectPath("cache", execution.ResultKey)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("mutable JSON cache index exists: %v", statErr)
	}
}

func TestLocalCIPassingCacheLookupAndPromotionFailureBoundaries(t *testing.T) {
	var nilStore *FileEvidenceStore
	if _, _, err := nilStore.LookupPassing(context.Background(), strings.Repeat("a", 64)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil LookupPassing() error = %v", err)
	}
	root := filepath.Join(t.TempDir(), "evidence")
	store, plan, execution, now := prepareLegacyCacheEvidence(t, root)
	defer store.Close()
	if _, found, err := store.LookupPassing(context.Background(), strings.Repeat("f", 64)); err != nil || found {
		t.Fatalf("missing LookupPassing() = %v, %v", found, err)
	}
	if _, found, err := store.LookupPassing(nil, strings.Repeat("f", 64)); err != nil || found {
		t.Fatalf("nil-context LookupPassing() = %v, %v", found, err)
	}
	if _, _, err := store.LookupPassing(context.Background(), "invalid"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid LookupPassing() error = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := store.LookupPassing(canceled, execution.ResultKey); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled LookupPassing() error = %v", err)
	}
	if err := store.PromotePassing(canceled, execution.ResultKey, execution.Digest); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled PromotePassing() error = %v", err)
	}
	if err := store.PromotePassing(
		context.Background(), execution.ResultKey, strings.Repeat("e", 64),
	); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing-execution PromotePassing() error = %v", err)
	}
	store.now = func() time.Time { return now }
	if err := store.PromotePassing(context.Background(), execution.ResultKey, execution.Digest); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store.objectPath("executions", execution.Digest)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.LookupPassing(
		context.Background(), execution.ResultKey,
	); !errors.Is(err, ErrEvidenceCorrupt) {
		t.Fatalf("LookupPassing(missing execution) error = %v", err)
	}
	if err := store.PutExecution(context.Background(), execution); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		store.objectPath("executions", execution.Digest),
		[]byte("{\"broken\":"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.LookupPassing(
		context.Background(), execution.ResultKey,
	); !errors.Is(err, ErrEvidenceCorrupt) {
		t.Fatalf("LookupPassing(malformed execution) error = %v", err)
	}
	_ = plan
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.LookupPassing(
		context.Background(), execution.ResultKey,
	); !errors.Is(err, ErrInvalid) {
		t.Fatalf("closed LookupPassing() error = %v", err)
	}
	if err := store.PromotePassing(
		context.Background(),
		execution.ResultKey,
		execution.Digest,
	); !errors.Is(
		err,
		ErrInvalid,
	) {
		t.Fatalf("closed PromotePassing() error = %v", err)
	}
}

func TestLocalCIPassingCacheRejectsCorruptRowsAndSQLiteMutationFailures(t *testing.T) {
	t.Run("corrupt typed row", func(t *testing.T) {
		store, err := OpenFileEvidenceStore(filepath.Join(t.TempDir(), "evidence"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		store.cacheDB.SetMaxOpenConns(1)
		if _, err = store.cacheDB.Exec(`PRAGMA ignore_check_constraints = ON`); err != nil {
			t.Fatal(err)
		}
		now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
		key := strings.Repeat("a", 64)
		if _, err = store.cacheDB.Exec(`INSERT INTO passing_cache (
            result_key, execution_digest, execution_status,
            created_at_unix_seconds, created_at_nanosecond,
            expires_at_unix_seconds, expires_at_nanosecond, version
        ) VALUES (?, ?, 'failed', ?, 0, ?, 0, 1)`,
			key,
			strings.Repeat("b", 64),
			now.Unix(),
			now.Add(time.Hour).Unix(),
		); err != nil {
			t.Fatal(err)
		}
		store.now = func() time.Time { return now }
		if _, _, err = store.LookupPassing(context.Background(), key); !errors.Is(err, ErrEvidenceCorrupt) {
			t.Fatalf("LookupPassing(corrupt row) error = %v", err)
		}
	})

	t.Run("closed database lookup", func(t *testing.T) {
		store, err := OpenFileEvidenceStore(filepath.Join(t.TempDir(), "evidence"))
		if err != nil {
			t.Fatal(err)
		}
		if err = store.cacheDB.Close(); err != nil {
			t.Fatal(err)
		}
		if _, _, err = store.LookupPassing(context.Background(), strings.Repeat("a", 64)); err == nil {
			t.Fatal("LookupPassing(closed database) error = nil")
		}
		store.cacheDB = nil
		if err = store.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("promotion validation and SQL failures", func(t *testing.T) {
		store, _, execution, now := prepareLegacyCacheEvidence(
			t,
			filepath.Join(t.TempDir(), "evidence"),
		)
		defer store.Close()
		store.now = func() time.Time { return now }
		store.cacheTTL = 8 * 24 * time.Hour
		if err := store.PromotePassing(nil, execution.ResultKey, execution.Digest); !errors.Is(err, ErrInvalid) {
			t.Fatalf("PromotePassing(overlong TTL) error = %v", err)
		}
		store.cacheTTL = time.Hour
		if err := store.PromotePassing(nil, execution.ResultKey, execution.Digest); err != nil {
			t.Fatal(err)
		}
		if _, err := store.cacheDB.Exec(
			`UPDATE passing_cache SET version = ? WHERE result_key = ?`,
			maximumLocalCICacheVersion,
			execution.ResultKey,
		); err != nil {
			t.Fatal(err)
		}
		if err := store.PromotePassing(
			context.Background(),
			execution.ResultKey,
			execution.Digest,
		); !errors.Is(
			err,
			ErrEvidenceConflict,
		) {
			t.Fatalf("PromotePassing(max version) error = %v", err)
		}
		if _, err := store.cacheDB.Exec(
			`UPDATE passing_cache SET version = 1 WHERE result_key = ?`,
			execution.ResultKey,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := store.cacheDB.Exec(`CREATE TRIGGER passing_cache_reject_update
            BEFORE UPDATE ON passing_cache BEGIN SELECT RAISE(ABORT, 'blocked'); END`); err != nil {
			t.Fatal(err)
		}
		if err := store.PromotePassing(context.Background(), execution.ResultKey, execution.Digest); err == nil {
			t.Fatal("PromotePassing(rejected update) error = nil")
		}
		if _, err := store.cacheDB.Exec(`DROP TRIGGER passing_cache_reject_update`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.cacheDB.Exec(`DELETE FROM passing_cache`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.cacheDB.Exec(`CREATE TRIGGER passing_cache_reject_insert
            BEFORE INSERT ON passing_cache BEGIN SELECT RAISE(ABORT, 'blocked'); END`); err != nil {
			t.Fatal(err)
		}
		if err := store.PromotePassing(context.Background(), execution.ResultKey, execution.Digest); err == nil {
			t.Fatal("PromotePassing(rejected insert) error = nil")
		}
		if _, err := store.cacheDB.Exec(`DROP TRIGGER passing_cache_reject_insert`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.cacheDB.Exec(`DROP TABLE passing_cache`); err != nil {
			t.Fatal(err)
		}
		if err := store.PromotePassing(context.Background(), execution.ResultKey, execution.Digest); err == nil {
			t.Fatal("PromotePassing(missing cache table) error = nil")
		}
	})
}

//nolint:govet // Independent storage assertions intentionally use narrow error scopes.
func TestLocalCIPassingCacheCapacityPrunesOnlyExpiredRows(t *testing.T) {
	store, err := OpenFileEvidenceStore(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	if err = sqlitestore.Immediate(context.Background(), store.cacheDB, func(conn *sql.Conn) error {
		if err := makePassingCacheCapacity(context.Background(), conn, now, 0); !errors.Is(err, ErrInvalid) {
			return errors.New("invalid capacity limit was accepted")
		}
		return makePassingCacheCapacity(context.Background(), conn, now, 2)
	}); err != nil {
		t.Fatal(err)
	}
	insertCacheRow := func(keyCharacter string, expires time.Time) {
		t.Helper()
		_, insertErr := store.cacheDB.Exec(`INSERT INTO passing_cache (
            result_key, execution_digest, execution_status,
            created_at_unix_seconds, created_at_nanosecond,
            expires_at_unix_seconds, expires_at_nanosecond, version
        ) VALUES (?, ?, 'passed', ?, ?, ?, ?, 1)`,
			strings.Repeat(keyCharacter, 64),
			strings.Repeat("e", 64),
			expires.Add(-time.Hour).Unix(),
			expires.Add(-time.Hour).Nanosecond(),
			expires.Unix(),
			expires.Nanosecond(),
		)
		if insertErr != nil {
			t.Fatal(insertErr)
		}
	}
	insertCacheRow("a", now.Add(-time.Second))
	insertCacheRow("b", now)
	if err = sqlitestore.Immediate(context.Background(), store.cacheDB, func(conn *sql.Conn) error {
		return makePassingCacheCapacity(context.Background(), conn, now, 2)
	}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = store.cacheDB.QueryRow(`SELECT COUNT(*) FROM passing_cache`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("cache count after expiry prune = %d, %v", count, err)
	}
	insertCacheRow("c", now.Add(time.Hour))
	insertCacheRow("d", now.Add(time.Hour))
	if err = sqlitestore.Immediate(context.Background(), store.cacheDB, func(conn *sql.Conn) error {
		return makePassingCacheCapacity(context.Background(), conn, now, 2)
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("full active cache capacity error = %v", err)
	}
	if err = store.cacheDB.QueryRow(`SELECT COUNT(*) FROM passing_cache`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("active cache count after rejected prune = %d, %v", count, err)
	}
	if _, err = store.cacheDB.Exec(`DELETE FROM passing_cache`); err != nil {
		t.Fatal(err)
	}
	insertCacheRow("a", now.Add(-time.Second))
	if _, err = store.cacheDB.Exec(`CREATE TRIGGER passing_cache_reject_delete
        BEFORE DELETE ON passing_cache BEGIN SELECT RAISE(ABORT, 'blocked'); END`); err != nil {
		t.Fatal(err)
	}
	if err = sqlitestore.Immediate(context.Background(), store.cacheDB, func(conn *sql.Conn) error {
		return makePassingCacheCapacity(context.Background(), conn, now, 1)
	}); err == nil {
		t.Fatal("capacity prune ignored delete failure")
	}
	closedConn, err := store.cacheDB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = closedConn.Close(); err != nil {
		t.Fatal(err)
	}
	if err = makePassingCacheCapacity(context.Background(), closedConn, now, 1); err == nil {
		t.Fatal("capacity check on closed connection succeeded")
	}
}

func TestLocalCIPassingCacheMigratesValidIndexesAndAuditsSkips(t *testing.T) {
	root := filepath.Join(t.TempDir(), "evidence")
	store, plan, execution, now := prepareUnopenedLegacyCacheEvidence(t, root)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	validRecord := mustLegacyCacheRecord(t, cacheIndexRecord{
		ResultKey:       execution.ResultKey,
		ExecutionDigest: execution.Digest,
		CreatedAt:       now,
		ExpiresAt:       now.Add(time.Hour),
	})
	writeLegacyCacheSource(t, root, execution.ResultKey, validRecord)
	malformedKey := strings.Repeat("a", 64)
	malformed := []byte(`{"secret":"never-record-this","broken":`)
	writeLegacyCacheSource(t, root, malformedKey, malformed)
	missingKey := strings.Repeat("b", 64)
	missingRecord := mustLegacyCacheRecord(t, cacheIndexRecord{
		ResultKey:       missingKey,
		ExecutionDigest: strings.Repeat("c", 64),
		CreatedAt:       now,
		ExpiresAt:       now.Add(time.Hour),
	})
	writeLegacyCacheSource(t, root, missingKey, missingRecord)
	invalidRelative := filepath.Join(root, "cache", "zz", "not-a-digest.json")
	if err := os.MkdirAll(filepath.Dir(invalidRelative), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(invalidRelative, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenFileEvidenceStore(root)
	if err != nil {
		t.Fatal(err)
	}
	reopened.now = func() time.Time { return now.Add(time.Minute) }
	loaded, found, err := reopened.LookupPassing(context.Background(), execution.ResultKey)
	if err != nil || !found || loaded.Digest != execution.Digest || loaded.Evidence.PlanDigest != plan.Digest {
		t.Fatalf("migrated LookupPassing() = (%#v, %v, %v)", loaded, found, err)
	}
	var cacheCount, importCount, importedCount, skippedCount, pendingCount int
	if err = reopened.cacheDB.QueryRow(`SELECT COUNT(*) FROM passing_cache`).Scan(&cacheCount); err != nil {
		t.Fatal(err)
	}
	if err = reopened.cacheDB.QueryRow(`SELECT
        COUNT(*), COALESCE(SUM(imported_count), 0), COALESCE(SUM(skipped_count), 0),
        COALESCE(SUM(CASE archive_status WHEN 'pending' THEN 1 ELSE 0 END), 0)
      FROM storage_imports WHERE component = ?`, localCICacheComponent).Scan(
		&importCount,
		&importedCount,
		&skippedCount,
		&pendingCount,
	); err != nil {
		t.Fatal(err)
	}
	if cacheCount != 1 || importCount != 4 || importedCount != 1 || skippedCount != 3 || pendingCount != 0 {
		t.Fatalf(
			"migration counts = cache %d, imports %d, imported %d, skipped %d, pending %d",
			cacheCount,
			importCount,
			importedCount,
			skippedCount,
			pendingCount,
		)
	}
	rows, err := reopened.cacheDB.Query(`SELECT issue_code FROM storage_import_issues
        WHERE component = ? ORDER BY issue_code`, localCICacheComponent)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var issueCodes []string
	for rows.Next() {
		var code string
		if err = rows.Scan(&code); err != nil {
			t.Fatal(err)
		}
		issueCodes = append(issueCodes, code)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	wantIssues := []string{"invalid-identity", "malformed-index", "missing-execution"}
	if !slices.Equal(issueCodes, wantIssues) {
		t.Fatalf("migration issue codes = %#v, want %#v", issueCodes, wantIssues)
	}

	for _, relative := range []string{
		filepath.Join("cache", execution.ResultKey[:2], execution.ResultKey+".json"),
		filepath.Join("cache", malformedKey[:2], malformedKey+".json"),
		filepath.Join("cache", missingKey[:2], missingKey+".json"),
		filepath.Join("cache", "zz", "not-a-digest.json"),
	} {
		if _, statErr := os.Stat(filepath.Join(root, relative)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("legacy source %s remains: %v", relative, statErr)
		}
		archived, readErr := os.ReadFile(filepath.Join(
			root,
			"legacy-json",
			localCICacheArchiveLabel,
			relative,
		))
		if readErr != nil || len(archived) == 0 {
			t.Fatalf("archived source %s = %d bytes, %v", relative, len(archived), readErr)
		}
	}
	if err = reopened.Close(); err != nil {
		t.Fatal(err)
	}
	for _, databaseFile := range []string{
		filepath.Join(root, localCICacheDatabaseName),
		filepath.Join(root, localCICacheDatabaseName) + "-wal",
		filepath.Join(root, localCICacheDatabaseName) + "-shm",
	} {
		raw, readErr := os.ReadFile(databaseFile)
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
		if bytes.Contains(raw, []byte("never-record-this")) {
			t.Fatalf("migration database retained rejected payload in %s", databaseFile)
		}
	}

	reopened, err = OpenFileEvidenceStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err = reopened.cacheDB.QueryRow(
		`SELECT COUNT(*) FROM storage_imports WHERE component = ?`,
		localCICacheComponent,
	).Scan(&importCount); err != nil || importCount != 4 {
		t.Fatalf("second-open import count = %d, %v", importCount, err)
	}
}

func TestLocalCIPassingCacheArchiveRetryDoesNotReimport(t *testing.T) {
	root := filepath.Join(t.TempDir(), "evidence")
	store, _, execution, now := prepareUnopenedLegacyCacheEvidence(t, root)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	record := mustLegacyCacheRecord(t, cacheIndexRecord{
		ResultKey:       execution.ResultKey,
		ExecutionDigest: execution.Digest,
		CreatedAt:       now,
		ExpiresAt:       now.Add(time.Hour),
	})
	legacyPath := writeLegacyCacheSource(t, root, execution.ResultKey, record)
	relative, err := filepath.Rel(root, legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(root, "legacy-json", localCICacheArchiveLabel, relative)
	if err = os.MkdirAll(filepath.Dir(archivePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(archivePath, []byte("conflict\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenFileEvidenceStore(root); err == nil {
		t.Fatal("OpenFileEvidenceStore(conflicting archive) error = nil")
	}

	raw := openRawLocalCICacheDatabase(t, root)
	var cacheCount, pendingCount int
	if err = raw.QueryRow(`SELECT COUNT(*) FROM passing_cache`).Scan(&cacheCount); err != nil {
		t.Fatal(err)
	}
	if err = raw.QueryRow(`SELECT COUNT(*) FROM storage_imports
        WHERE component = ? AND archive_status = 'pending'`, localCICacheComponent).Scan(
		&pendingCount,
	); err != nil {
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	if cacheCount != 1 || pendingCount != 1 {
		t.Fatalf("post-commit state = cache %d, pending %d", cacheCount, pendingCount)
	}
	if err = os.Remove(archivePath); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFileEvidenceStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var importedCount int
	if err = reopened.cacheDB.QueryRow(`SELECT
        COUNT(*), COALESCE(SUM(imported_count), 0),
        COALESCE(SUM(CASE archive_status WHEN 'pending' THEN 1 ELSE 0 END), 0)
      FROM storage_imports WHERE component = ?`, localCICacheComponent).Scan(
		&cacheCount,
		&importedCount,
		&pendingCount,
	); err != nil {
		t.Fatal(err)
	}
	if cacheCount != 1 || importedCount != 1 || pendingCount != 0 {
		t.Fatalf(
			"recovered import state = sources %d, imported %d, pending %d",
			cacheCount,
			importedCount,
			pendingCount,
		)
	}
	if archived, readErr := os.ReadFile(archivePath); readErr != nil || !bytes.Equal(archived, record) {
		t.Fatalf("recovered archive = %q, %v", archived, readErr)
	}
}

func TestLocalCIPassingCacheMigrationRejectsUnsafeAndOversizedSources(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation requires optional Windows privilege")
		}
		root := filepath.Join(t.TempDir(), "evidence")
		store, err := OpenFileEvidenceStore(root)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.Close(); err != nil {
			t.Fatal(err)
		}
		outside := t.TempDir()
		if err = os.Symlink(outside, filepath.Join(root, "cache", "aa")); err != nil {
			t.Fatal(err)
		}
		if _, err = OpenFileEvidenceStore(root); err == nil {
			t.Fatal("OpenFileEvidenceStore(symlink cache entry) error = nil")
		}
		if entries, readErr := os.ReadDir(outside); readErr != nil || len(entries) != 0 {
			t.Fatalf("outside directory changed: %d entries, %v", len(entries), readErr)
		}
	})

	t.Run("oversized", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "evidence")
		store, err := OpenFileEvidenceStore(root)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.Close(); err != nil {
			t.Fatal(err)
		}
		key := strings.Repeat("d", 64)
		path := writeLegacyCacheSource(
			t,
			root,
			key,
			bytes.Repeat([]byte("x"), int(maximumLegacyCacheIndexBytes)+1),
		)
		if _, err = OpenFileEvidenceStore(root); err == nil {
			t.Fatal("OpenFileEvidenceStore(oversized cache index) error = nil")
		}
		if info, statErr := os.Stat(path); statErr != nil || info.Size() != maximumLegacyCacheIndexBytes+1 {
			t.Fatalf("oversized source = %v, %v", info, statErr)
		}
		raw := openRawLocalCICacheDatabase(t, root)
		defer raw.Close()
		var count int
		if err = raw.QueryRow(`SELECT COUNT(*) FROM storage_imports
            WHERE component = ?`, localCICacheComponent).Scan(&count); err != nil || count != 0 {
			t.Fatalf("failed migration import count = %d, %v", count, err)
		}
	})

	if runtime.GOOS != "windows" {
		t.Run("hardlink", func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "evidence")
			store, _, execution, now := prepareLegacyCacheEvidence(t, root)
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			record := mustLegacyCacheRecord(t, cacheIndexRecord{
				ResultKey: execution.ResultKey, ExecutionDigest: execution.Digest,
				CreatedAt: now, ExpiresAt: now.Add(time.Hour),
			})
			legacyPath := writeLegacyCacheSource(t, root, execution.ResultKey, record)
			alias := filepath.Join(t.TempDir(), "legacy-cache-alias.json")
			if err := os.Link(legacyPath, alias); err != nil {
				t.Skipf("hardlinks unavailable: %v", err)
			}
			if _, err := OpenFileEvidenceStore(root); err == nil {
				t.Fatal("OpenFileEvidenceStore(hard-linked cache index) error = nil")
			}
			if aliased, readErr := os.ReadFile(alias); readErr != nil || !bytes.Equal(aliased, record) {
				t.Fatalf("hardlink alias = %q, %v", aliased, readErr)
			}
		})
	}
}

func TestLocalCIPassingCacheLegacyEnumerationBoundaries(t *testing.T) {
	t.Run("closed and missing", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "evidence")
		store, err := OpenFileEvidenceStore(root)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.Remove(filepath.Join(root, "cache")); err != nil {
			t.Fatal(err)
		}
		if sources, sourceErr := store.legacyCacheSources(); sourceErr != nil || len(sources) != 0 {
			t.Fatalf("missing legacy cache sources = %#v, %v", sources, sourceErr)
		}
		if err = store.Close(); err != nil {
			t.Fatal(err)
		}
		if _, sourceErr := store.legacyCacheSources(); sourceErr == nil {
			t.Fatal("closed legacyCacheSources() error = nil")
		}
	})

	t.Run("closed root handle", func(t *testing.T) {
		store, err := OpenFileEvidenceStore(filepath.Join(t.TempDir(), "evidence"))
		if err != nil {
			t.Fatal(err)
		}
		if err = store.root.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err = store.legacyCacheSources(); err == nil {
			t.Fatal("legacyCacheSources(closed root handle) error = nil")
		}
		store.root = nil
		if err = store.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("direct source and read bounds", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "evidence")
		store, err := OpenFileEvidenceStore(root)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		if err = os.WriteFile(filepath.Join(root, "cache", "direct.json"), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		sources, err := store.legacyCacheSources()
		if err != nil || len(sources) != 1 || sources[0].Relative != "cache/direct.json" {
			t.Fatalf("direct legacy source = %#v, %v", sources, err)
		}
		if _, err = store.readLegacyCacheDirectory("cache", -1); err == nil {
			t.Fatal("negative legacy enumeration bound succeeded")
		}
		if _, err = store.readLegacyCacheDirectory("cache", 0); err == nil {
			t.Fatal("exceeded legacy enumeration bound succeeded")
		}
		if _, err = store.readLegacyCacheDirectory("cache/direct.json", 1); err == nil {
			t.Fatal("regular file accepted as legacy cache directory")
		}
		if _, err = store.readLegacyCacheDirectory("missing", 1); err == nil {
			t.Fatal("missing legacy enumeration directory succeeded")
		}
	})

	t.Run("nested directory", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "evidence")
		store, err := OpenFileEvidenceStore(root)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		if err = os.MkdirAll(filepath.Join(root, "cache", "aa", "nested"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err = store.legacyCacheSources(); err == nil {
			t.Fatal("nested legacy cache directory succeeded")
		}
	})

	if runtime.GOOS != "windows" {
		t.Run("unsafe cache root", func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "evidence")
			store, err := OpenFileEvidenceStore(root)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			cacheRoot := filepath.Join(root, "cache")
			if err = os.Chmod(cacheRoot, 0o722); err != nil {
				t.Fatal(err)
			}
			if _, err = store.legacyCacheSources(); err == nil {
				t.Fatal("writable legacy cache root succeeded")
			}
		})

		t.Run("unreadable cache root", func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "evidence")
			store, err := OpenFileEvidenceStore(root)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			cacheRoot := filepath.Join(root, "cache")
			if err = os.Chmod(cacheRoot, 0o000); err != nil {
				t.Fatal(err)
			}
			if _, err = store.legacyCacheSources(); err == nil {
				t.Fatal("unreadable legacy cache root succeeded")
			}
		})

		t.Run("unreadable prefix", func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "evidence")
			store, err := OpenFileEvidenceStore(root)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			prefix := filepath.Join(root, "cache", "aa")
			if err = os.Mkdir(prefix, 0o000); err != nil {
				t.Fatal(err)
			}
			if _, err = store.legacyCacheSources(); err == nil {
				t.Fatal("unreadable legacy cache prefix succeeded")
			}
		})

		t.Run("unsafe prefix mode", func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "evidence")
			store, err := OpenFileEvidenceStore(root)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			prefix := filepath.Join(root, "cache", "aa")
			if err = os.Mkdir(prefix, 0o700); err != nil {
				t.Fatal(err)
			}
			if err = os.Chmod(prefix, 0o722); err != nil {
				t.Fatal(err)
			}
			if _, err = store.legacyCacheSources(); err == nil {
				t.Fatal("writable legacy cache prefix succeeded")
			}
		})
	}

	t.Run("directory count", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "evidence")
		store, err := OpenFileEvidenceStore(root)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		for index := range maximumLegacyCacheDirectories + 1 {
			if err = os.Mkdir(filepath.Join(root, "cache", fmt.Sprintf("prefix-%03d", index)), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if _, err = store.legacyCacheSources(); err == nil {
			t.Fatal("legacy cache directory count overflow succeeded")
		}
	})
}

func TestLocalCIPassingCacheMigrationClassifiesInvalidEvidence(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*testing.T, string, *FileEvidenceStore, Plan, Execution, time.Time) []byte
		issueCode string
	}{
		{
			name: "invalid index",
			mutate: func(_ *testing.T, _ string, _ *FileEvidenceStore, _ Plan, _ Execution, _ time.Time) []byte {
				return []byte("{}\n")
			},
			issueCode: "invalid-index",
		},
		{
			name: "missing plan",
			mutate: func(t *testing.T, _ string, store *FileEvidenceStore, plan Plan, execution Execution, now time.Time) []byte {
				if err := os.Remove(store.objectPath("plans", plan.Digest)); err != nil {
					t.Fatal(err)
				}
				return mustLegacyCacheRecord(t, cacheIndexRecord{
					ResultKey: execution.ResultKey, ExecutionDigest: execution.Digest,
					CreatedAt: now, ExpiresAt: now.Add(time.Hour),
				})
			},
			issueCode: "missing-plan",
		},
		{
			name: "malformed execution",
			mutate: func(t *testing.T, _ string, store *FileEvidenceStore, _ Plan, execution Execution, now time.Time) []byte {
				if err := os.WriteFile(
					store.objectPath("executions", execution.Digest),
					[]byte("{\"broken\":"),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
				return mustLegacyCacheRecord(t, cacheIndexRecord{
					ResultKey: execution.ResultKey, ExecutionDigest: execution.Digest,
					CreatedAt: now, ExpiresAt: now.Add(time.Hour),
				})
			},
			issueCode: "invalid-execution",
		},
		{
			name: "malformed plan",
			mutate: func(t *testing.T, _ string, store *FileEvidenceStore, plan Plan, execution Execution, now time.Time) []byte {
				if err := os.WriteFile(
					store.objectPath("plans", plan.Digest),
					[]byte("{\"broken\":"),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
				return mustLegacyCacheRecord(t, cacheIndexRecord{
					ResultKey: execution.ResultKey, ExecutionDigest: execution.Digest,
					CreatedAt: now, ExpiresAt: now.Add(time.Hour),
				})
			},
			issueCode: "invalid-execution",
		},
		{
			name: "invalid plan identity",
			mutate: func(t *testing.T, _ string, store *FileEvidenceStore, _ Plan, execution Execution, now time.Time) []byte {
				raw := execution
				raw.Evidence.PlanDigest = "invalid"
				encoded, err := encodeEvidence(raw)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(store.objectPath("executions", execution.Digest), encoded, 0o600); err != nil {
					t.Fatal(err)
				}
				return mustLegacyCacheRecord(t, cacheIndexRecord{
					ResultKey: execution.ResultKey, ExecutionDigest: execution.Digest,
					CreatedAt: now, ExpiresAt: now.Add(time.Hour),
				})
			},
			issueCode: "invalid-execution",
		},
		{
			name: "non-passing execution",
			mutate: func(t *testing.T, _ string, store *FileEvidenceStore, plan Plan, execution Execution, now time.Time) []byte {
				failed := validTestExecution(t, plan, now, StatusFailed)
				if err := store.PutExecution(context.Background(), failed); err != nil {
					t.Fatal(err)
				}
				return mustLegacyCacheRecord(t, cacheIndexRecord{
					ResultKey: execution.ResultKey, ExecutionDigest: failed.Digest,
					CreatedAt: now, ExpiresAt: now.Add(time.Hour),
				})
			},
			issueCode: "invalid-execution",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "evidence")
			store, plan, execution, now := prepareUnopenedLegacyCacheEvidence(t, root)
			record := test.mutate(t, root, store, plan, execution, now)
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			writeLegacyCacheSource(t, root, execution.ResultKey, record)
			reopened, err := OpenFileEvidenceStore(root)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			var issueCode string
			if err = reopened.cacheDB.QueryRow(`SELECT issue_code
                    FROM storage_import_issues WHERE component = ?`,
				localCICacheComponent,
			).Scan(&issueCode); err != nil || issueCode != test.issueCode {
				t.Fatalf("migration issue = %q, %v; want %q", issueCode, err, test.issueCode)
			}
		})
	}
}

func TestLocalCIPassingCacheMigrationRejectsUnsafeImmutableEvidence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX private-mode assertion")
	}
	root := filepath.Join(t.TempDir(), "evidence")
	store, _, execution, now := prepareUnopenedLegacyCacheEvidence(t, root)
	record := mustLegacyCacheRecord(t, cacheIndexRecord{
		ResultKey: execution.ResultKey, ExecutionDigest: execution.Digest,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	})
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	writeLegacyCacheSource(t, root, execution.ResultKey, record)
	if err := os.Chmod(
		filepath.Dir(filepath.Join(root, "executions", execution.Digest[:2], execution.Digest+".json")),
		0o722,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileEvidenceStore(root); err == nil {
		t.Fatal("OpenFileEvidenceStore(unsafe immutable evidence) error = nil")
	}
}

func TestLocalCIPassingCacheMigrationRejectsUnsafePlanEvidence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX private-mode assertion")
	}
	root := filepath.Join(t.TempDir(), "evidence")
	store, plan, execution, now := prepareUnopenedLegacyCacheEvidence(t, root)
	record := mustLegacyCacheRecord(t, cacheIndexRecord{
		ResultKey: execution.ResultKey, ExecutionDigest: execution.Digest,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	})
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	writeLegacyCacheSource(t, root, execution.ResultKey, record)
	if err := os.Chmod(filepath.Join(root, "plans", plan.Digest[:2]), 0o722); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileEvidenceStore(root); err == nil {
		t.Fatal("OpenFileEvidenceStore(unsafe plan evidence) error = nil")
	}
}

func TestLocalCIPassingCacheImporterPropagatesSQLiteFailure(t *testing.T) {
	store, _, execution, now := prepareLegacyCacheEvidence(
		t,
		filepath.Join(t.TempDir(), "evidence"),
	)
	defer store.Close()
	data := mustLegacyCacheRecord(t, cacheIndexRecord{
		ResultKey: execution.ResultKey, ExecutionDigest: execution.Digest,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	})
	conn, err := store.cacheDB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = conn.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = store.importLegacyCacheIndex(context.Background(), conn, sqlitestore.LegacyInput{
		Relative: filepath.ToSlash(filepath.Join(
			"cache",
			execution.ResultKey[:2],
			execution.ResultKey+".json",
		)),
		Data:   data,
		Digest: sha256.Sum256(data),
	}); err == nil {
		t.Fatal("importLegacyCacheIndex(closed connection) error = nil")
	}
}

func TestLocalCIPassingCacheImporterPropagatesCancellation(t *testing.T) {
	store, _, execution, now := prepareLegacyCacheEvidence(
		t,
		filepath.Join(t.TempDir(), "evidence"),
	)
	defer store.Close()
	data := mustLegacyCacheRecord(t, cacheIndexRecord{
		ResultKey: execution.ResultKey, ExecutionDigest: execution.Digest,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.importLegacyCacheIndex(ctx, nil, sqlitestore.LegacyInput{
		Relative: filepath.ToSlash(filepath.Join(
			"cache",
			execution.ResultKey[:2],
			execution.ResultKey+".json",
		)),
		Data: data, Digest: sha256.Sum256(data),
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("importLegacyCacheIndex(canceled) error = %v", err)
	}
}

//nolint:govet // Independent storage assertions intentionally use narrow error scopes.
func TestLocalCIPassingCacheLegacyIdentityAndEvidencePathBoundaries(t *testing.T) {
	key := strings.Repeat("a", 64)
	if got, valid := legacyCacheResultKey("cache/direct.json"); valid || got != "" {
		t.Fatalf("structurally invalid legacy key = %q, %v", got, valid)
	}
	if got, valid := legacyCacheResultKey(
		filepath.ToSlash(filepath.Join("cache", "bb", key+".json")),
	); valid ||
		got != key {
		t.Fatalf("mismatched legacy key = %q, %v", got, valid)
	}
	store, err := OpenFileEvidenceStore(filepath.Join(t.TempDir(), "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if found, err := store.validateLegacyImmutableEvidencePath("plans", "invalid"); err != nil || found {
		t.Fatalf("invalid immutable path = %v, %v", found, err)
	}
	missing := strings.Repeat("d", 64)
	if err = os.MkdirAll(filepath.Join(store.rootPath, "plans", missing[:2]), 0o700); err != nil {
		t.Fatal(err)
	}
	if found, err := store.validateLegacyImmutableEvidencePath("plans", missing); err != nil || found {
		t.Fatalf("missing immutable file = %v, %v", found, err)
	}
	unsafe := strings.Repeat("e", 64)
	unsafePath := store.objectPath("plans", unsafe)
	if err = os.MkdirAll(filepath.Dir(unsafePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(unsafePath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err = os.Chmod(unsafePath, 0o666); err != nil {
			t.Fatal(err)
		}
		if found, pathErr := store.validateLegacyImmutableEvidencePath("plans", unsafe); pathErr == nil || found {
			t.Fatalf("unsafe immutable file = %v, %v", found, pathErr)
		}
	}
	if err = store.root.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = store.validateLegacyImmutableEvidencePath("plans", key); err == nil {
		t.Fatal("immutable path on closed root succeeded")
	}
	store.root = nil
}

func TestLocalCIPassingCachePropagatesEvidenceAndResultDriverFailures(t *testing.T) {
	store, _, execution, now := prepareLegacyCacheEvidence(
		t,
		filepath.Join(t.TempDir(), "evidence"),
	)
	defer store.Close()
	store.now = func() time.Time { return now }
	if err := store.PromotePassing(context.Background(), execution.ResultKey, execution.Digest); err != nil {
		t.Fatal(err)
	}
	data := mustLegacyCacheRecord(t, cacheIndexRecord{
		ResultKey: execution.ResultKey, ExecutionDigest: execution.Digest,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	})
	input := sqlitestore.LegacyInput{
		Relative: filepath.ToSlash(filepath.Join(
			"cache",
			execution.ResultKey[:2],
			execution.ResultKey+".json",
		)),
		Data: data, Digest: sha256.Sum256(data),
	}
	originalGetExecution := localCICacheGetExecution
	originalRowsAffected := localCICacheRowsAffected
	t.Cleanup(func() {
		localCICacheGetExecution = originalGetExecution
		localCICacheRowsAffected = originalRowsAffected
	})
	injected := errors.New("injected local CI evidence failure")
	localCICacheGetExecution = func(
		*FileEvidenceStore,
		context.Context,
		string,
	) (Execution, bool, error) {
		return Execution{}, false, injected
	}
	if _, _, err := store.LookupPassing(context.Background(), execution.ResultKey); !errors.Is(err, injected) {
		t.Fatalf("LookupPassing(injected evidence failure) error = %v", err)
	}
	if err := store.PromotePassing(
		context.Background(),
		execution.ResultKey,
		execution.Digest,
	); !errors.Is(
		err,
		injected,
	) {
		t.Fatalf("PromotePassing(injected evidence failure) error = %v", err)
	}
	conn, err := store.cacheDB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = store.importLegacyCacheIndex(context.Background(), conn, input); !errors.Is(err, injected) {
		t.Fatalf("importLegacyCacheIndex(injected evidence failure) error = %v", err)
	}
	localCICacheGetExecution = originalGetExecution
	localCICacheRowsAffected = func(sql.Result) (int64, error) { return 0, injected }
	if err = store.PromotePassing(
		context.Background(),
		execution.ResultKey,
		execution.Digest,
	); !errors.Is(
		err,
		injected,
	) {
		t.Fatalf("PromotePassing(injected result failure) error = %v", err)
	}
	if _, err = store.importLegacyCacheIndex(context.Background(), conn, input); !errors.Is(err, injected) {
		t.Fatalf("importLegacyCacheIndex(injected result failure) error = %v", err)
	}
	localCICacheRowsAffected = func(sql.Result) (int64, error) { return 0, nil }
	if err = store.PromotePassing(
		context.Background(),
		execution.ResultKey,
		execution.Digest,
	); !errors.Is(
		err,
		ErrEvidenceConflict,
	) {
		t.Fatalf("PromotePassing(zero affected rows) error = %v", err)
	}
}

func TestLocalCIPassingCacheMigrationKeepsSQLiteAuthoritative(t *testing.T) {
	root := filepath.Join(t.TempDir(), "evidence")
	store, _, execution, now := prepareLegacyCacheEvidence(t, root)
	store.now = func() time.Time { return now }
	if err := store.PromotePassing(context.Background(), execution.ResultKey, execution.Digest); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	record := mustLegacyCacheRecord(t, cacheIndexRecord{
		ResultKey: execution.ResultKey, ExecutionDigest: execution.Digest,
		CreatedAt: now.Add(time.Minute), ExpiresAt: now.Add(2 * time.Hour),
	})
	writeLegacyCacheSource(t, root, execution.ResultKey, record)
	reopened, err := OpenFileEvidenceStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var code string
	var version int
	var createdSeconds int64
	if err = reopened.cacheDB.QueryRow(`SELECT issue_code FROM storage_import_issues
		WHERE component = ?`, localCICacheComponent).Scan(&code); err != nil || code != "late-source" {
		t.Fatalf("conflict issue = %q, %v", code, err)
	}
	if err = reopened.cacheDB.QueryRow(`SELECT version, created_at_unix_seconds
        FROM passing_cache WHERE result_key = ?`, execution.ResultKey).Scan(
		&version,
		&createdSeconds,
	); err != nil || version != 1 || createdSeconds != now.Unix() {
		t.Fatalf("authoritative row = version %d, created %d, %v", version, createdSeconds, err)
	}
}

func prepareLegacyCacheEvidence(
	t *testing.T,
	root string,
) (*FileEvidenceStore, Plan, Execution, time.Time) {
	t.Helper()
	store, err := OpenFileEvidenceStore(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 31, 12, 0, 0, 123456789, time.UTC)
	plan := validTestPlan(t)
	if err = store.PutPlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	execution := validTestExecution(t, plan, now, StatusPassed)
	if err = store.PutExecution(context.Background(), execution); err != nil {
		t.Fatal(err)
	}
	return store, plan, execution, now
}

func prepareUnopenedLegacyCacheEvidence(
	t *testing.T,
	root string,
) (*FileEvidenceStore, Plan, Execution, time.Time) {
	t.Helper()
	absolute, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	if mkdirErr := os.MkdirAll(absolute, 0o700); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	if chmodErr := os.Chmod(absolute, 0o700); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	rootHandle, err := os.OpenRoot(absolute)
	if err != nil {
		t.Fatal(err)
	}
	store := &FileEvidenceStore{
		rootPath: absolute,
		root:     rootHandle,
		now:      func() time.Time { return time.Now().UTC() },
		cacheTTL: 24 * time.Hour,
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, directory := range []string{"plans", "executions", "attestations", "cache", "discovery"} {
		if err := rootHandle.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := store.validateDirectory(directory); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 8, 31, 12, 0, 0, 123456789, time.UTC)
	plan := validTestPlan(t)
	if err := store.PutPlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	execution := validTestExecution(t, plan, now, StatusPassed)
	if err := store.PutExecution(context.Background(), execution); err != nil {
		t.Fatal(err)
	}
	return store, plan, execution, now
}

func mustLegacyCacheRecord(t *testing.T, record cacheIndexRecord) []byte {
	t.Helper()
	record.Version = EvidenceVersion
	normalized, err := finalizeCacheIndex(record)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := encodeEvidence(normalized)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func writeLegacyCacheSource(t *testing.T, root, resultKey string, data []byte) string {
	t.Helper()
	path := filepath.Join(root, "cache", resultKey[:2], resultKey+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func openRawLocalCICacheDatabase(t *testing.T, root string) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", filepath.Join(root, localCICacheDatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	return database
}
