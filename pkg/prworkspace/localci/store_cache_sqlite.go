package localci

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

const (
	localCICacheDatabaseName      = "cache.db"
	localCICacheComponent         = "local_ci_cache"
	localCICacheArchiveLabel      = "local-ci-cache-v1"
	maximumLocalCICacheEntries    = 100_000
	maximumLegacyCacheIndexBytes  = int64(64 << 10)
	maximumLegacyCacheTotalBytes  = int64(256 << 20)
	maximumLegacyCacheDirectories = 256
	maximumLocalCICacheVersion    = int64(2_147_483_647)
)

const passingCacheSchema = `CREATE TABLE passing_cache (
    result_key              TEXT PRIMARY KEY,
    execution_digest        TEXT NOT NULL,
    execution_status        TEXT NOT NULL CHECK(execution_status = 'passed'),
    created_at_unix_seconds INTEGER NOT NULL,
    created_at_nanosecond   INTEGER NOT NULL,
    expires_at_unix_seconds INTEGER NOT NULL,
    expires_at_nanosecond   INTEGER NOT NULL,
    version                 INTEGER NOT NULL CHECK(version BETWEEN 1 AND 2147483647),
    CHECK(length(result_key) = 64),
    CHECK(result_key = lower(result_key)),
    CHECK(result_key NOT GLOB '*[^0-9a-f]*'),
    CHECK(length(execution_digest) = 64),
    CHECK(execution_digest = lower(execution_digest)),
    CHECK(execution_digest NOT GLOB '*[^0-9a-f]*'),
    CHECK(created_at_unix_seconds BETWEEN -62167219200 AND 253402300799),
    CHECK(created_at_nanosecond BETWEEN 0 AND 999999999),
    CHECK(expires_at_unix_seconds BETWEEN -62167219200 AND 253402300799),
    CHECK(expires_at_nanosecond BETWEEN 0 AND 999999999),
    CHECK(
        expires_at_unix_seconds > created_at_unix_seconds
        OR (
            expires_at_unix_seconds = created_at_unix_seconds
            AND expires_at_nanosecond > created_at_nanosecond
        )
    ),
    CHECK(
        expires_at_unix_seconds - created_at_unix_seconds < 604800
        OR (
            expires_at_unix_seconds - created_at_unix_seconds = 604800
            AND expires_at_nanosecond <= created_at_nanosecond
        )
    )
) STRICT`

const passingCacheExpiryIndexSchema = `CREATE INDEX passing_cache_expiry_idx
    ON passing_cache(expires_at_unix_seconds, expires_at_nanosecond, result_key)`

const passingCacheExecutionIndexSchema = `CREATE INDEX passing_cache_execution_idx
    ON passing_cache(execution_digest, result_key)`

var (
	localCICacheGetExecution = func(
		store *FileEvidenceStore,
		ctx context.Context,
		digest string,
	) (Execution, bool, error) {
		return store.GetExecution(ctx, digest)
	}
	localCICacheRowsAffected = func(result sql.Result) (int64, error) {
		return result.RowsAffected()
	}
)

func (store *FileEvidenceStore) openCacheDatabase(ctx context.Context) (*sql.DB, error) {
	return sqlitestore.Open(
		ctx,
		filepath.Join(store.rootPath, localCICacheDatabaseName),
		store.cacheDatabaseOptions(),
	)
}

func (store *FileEvidenceStore) cacheDatabaseOptions() sqlitestore.Options {
	return sqlitestore.Options{
		Component: localCICacheComponent,
		Migrations: []sqlitestore.Migration{{
			Version: 1,
			Statements: []string{
				passingCacheSchema,
				passingCacheExpiryIndexSchema,
				passingCacheExecutionIndexSchema,
			},
		}},
		Validate: validatePassingCacheSchema,
		Legacy: &sqlitestore.LegacyOptions{
			SourceRoot:    store.rootPath,
			ArchiveRoot:   filepath.Join(store.rootPath, "legacy-json", localCICacheArchiveLabel),
			Sources:       store.legacyCacheSources,
			Import:        store.importLegacyCacheIndex,
			MaxBytes:      maximumLegacyCacheIndexBytes,
			MaxSources:    maximumLocalCICacheEntries,
			MaxTotalBytes: maximumLegacyCacheTotalBytes,
		},
	}
}

func validatePassingCacheSchema(ctx context.Context, conn *sql.Conn) error {
	for _, object := range []struct {
		typeName string
		name     string
		schema   string
	}{
		{typeName: "table", name: "passing_cache", schema: passingCacheSchema},
		{typeName: "index", name: "passing_cache_expiry_idx", schema: passingCacheExpiryIndexSchema},
		{typeName: "index", name: "passing_cache_execution_idx", schema: passingCacheExecutionIndexSchema},
	} {
		if err := sqlitestore.ValidateSchemaObject(
			ctx,
			conn,
			object.typeName,
			object.name,
			object.schema,
		); err != nil {
			return err
		}
	}
	if err := sqlitestore.ValidateUniqueIndexSet(ctx, conn, "passing_cache"); err != nil {
		return err
	}
	var unexpected int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
        WHERE name NOT LIKE 'sqlite_%'
          AND name NOT IN (
              'passing_cache',
              'passing_cache_expiry_idx',
              'passing_cache_execution_idx',
              'storage_imports',
              'storage_import_issues',
              'storage_import_horizons',
              'storage_imports_archive_status_idx'
          )`).Scan(&unexpected); err != nil {
		return err
	}
	if unexpected != 0 {
		return errors.New("local CI passing-cache schema has unexpected objects")
	}
	var count int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM passing_cache`).Scan(&count); err != nil {
		return err
	}
	if count > maximumLocalCICacheEntries {
		return errors.New("local CI passing cache exceeds its entry limit")
	}
	return nil
}

func (store *FileEvidenceStore) lookupPassingCache(
	ctx context.Context,
	resultKey string,
) (Execution, bool, error) {
	if store == nil || store.cacheDB == nil || !validDigest(resultKey) {
		return Execution{}, false, fmt.Errorf("%w: invalid result key", ErrInvalid)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Execution{}, false, err
	}
	var executionDigest, status string
	var createdSeconds, createdNanosecond, expiresSeconds, expiresNanosecond, version int64
	err := store.cacheDB.QueryRowContext(ctx, `SELECT
        execution_digest, execution_status,
        created_at_unix_seconds, created_at_nanosecond,
        expires_at_unix_seconds, expires_at_nanosecond, version
      FROM passing_cache
     WHERE result_key = ?`, resultKey).Scan(
		&executionDigest,
		&status,
		&createdSeconds,
		&createdNanosecond,
		&expiresSeconds,
		&expiresNanosecond,
		&version,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Execution{}, false, nil
	}
	if err != nil {
		return Execution{}, false, err
	}
	created := time.Unix(createdSeconds, createdNanosecond).UTC()
	expires := time.Unix(expiresSeconds, expiresNanosecond).UTC()
	if status != string(StatusPassed) || !validDigest(executionDigest) ||
		version < 1 || version > maximumLocalCICacheVersion ||
		!validCacheTimes(created, expires) {
		return Execution{}, false, ErrEvidenceCorrupt
	}
	if !store.now().UTC().Before(expires) {
		return Execution{}, false, nil
	}
	execution, found, err := localCICacheGetExecution(store, ctx, executionDigest)
	if err != nil || !found || execution.Status != StatusPassed || execution.ResultKey != resultKey {
		if err != nil {
			return Execution{}, false, err
		}
		return Execution{}, false, ErrEvidenceCorrupt
	}
	return execution, true, nil
}

func (store *FileEvidenceStore) promotePassingCache(
	ctx context.Context,
	resultKey, executionDigest string,
) error {
	if store == nil || store.cacheDB == nil {
		return fmt.Errorf("%w: invalid evidence store operation", ErrInvalid)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	execution, found, err := localCICacheGetExecution(store, ctx, executionDigest)
	if err != nil {
		return err
	}
	if !found || execution.Status != StatusPassed || execution.ResultKey != resultKey {
		return fmt.Errorf("%w: cache promotion requires exact passing execution", ErrInvalid)
	}
	now := store.now().UTC()
	record, err := finalizeCacheIndex(cacheIndexRecord{
		Version:         EvidenceVersion,
		ResultKey:       resultKey,
		ExecutionDigest: executionDigest,
		CreatedAt:       now,
		ExpiresAt:       now.Add(store.cacheTTL),
	})
	if err != nil {
		return err
	}
	return sqlitestore.Immediate(ctx, store.cacheDB, func(conn *sql.Conn) error {
		var currentVersion int64
		err := conn.QueryRowContext(
			ctx,
			`SELECT version FROM passing_cache WHERE result_key = ?`,
			resultKey,
		).Scan(&currentVersion)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if capacityErr := makePassingCacheCapacity(
				ctx,
				conn,
				now,
				maximumLocalCICacheEntries,
			); capacityErr != nil {
				return capacityErr
			}
			_, err = conn.ExecContext(ctx, `INSERT INTO passing_cache (
                result_key, execution_digest, execution_status,
                created_at_unix_seconds, created_at_nanosecond,
                expires_at_unix_seconds, expires_at_nanosecond, version
            ) VALUES (?, ?, 'passed', ?, ?, ?, ?, 1)`,
				record.ResultKey,
				record.ExecutionDigest,
				record.CreatedAt.Unix(),
				record.CreatedAt.Nanosecond(),
				record.ExpiresAt.Unix(),
				record.ExpiresAt.Nanosecond(),
			)
			return err
		case err != nil:
			return err
		case currentVersion < 1 || currentVersion >= maximumLocalCICacheVersion:
			return ErrEvidenceConflict
		}
		result, err := conn.ExecContext(ctx, `UPDATE passing_cache SET
                execution_digest = ?,
                execution_status = 'passed',
                created_at_unix_seconds = ?,
                created_at_nanosecond = ?,
                expires_at_unix_seconds = ?,
                expires_at_nanosecond = ?,
                version = version + 1
            WHERE result_key = ? AND version = ?`,
			record.ExecutionDigest,
			record.CreatedAt.Unix(),
			record.CreatedAt.Nanosecond(),
			record.ExpiresAt.Unix(),
			record.ExpiresAt.Nanosecond(),
			record.ResultKey,
			currentVersion,
		)
		if err != nil {
			return err
		}
		updated, err := localCICacheRowsAffected(result)
		if err != nil {
			return err
		}
		if updated != 1 {
			return ErrEvidenceConflict
		}
		return nil
	})
}

func makePassingCacheCapacity(
	ctx context.Context,
	conn *sql.Conn,
	now time.Time,
	maximumEntries int,
) error {
	if maximumEntries < 1 || maximumEntries > maximumLocalCICacheEntries {
		return fmt.Errorf("%w: local CI passing-cache limit is invalid", ErrInvalid)
	}
	var count int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM passing_cache`).Scan(&count); err != nil {
		return err
	}
	if count < maximumEntries {
		return nil
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM passing_cache
        WHERE expires_at_unix_seconds < ?
           OR (expires_at_unix_seconds = ? AND expires_at_nanosecond <= ?)`,
		now.Unix(),
		now.Unix(),
		now.Nanosecond(),
	); err != nil {
		return err
	}
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM passing_cache`).Scan(&count); err != nil {
		return err
	}
	if count >= maximumEntries {
		return fmt.Errorf("%w: local CI passing cache is full", ErrInvalid)
	}
	return nil
}

func validCacheTimes(created, expires time.Time) bool {
	return !created.IsZero() && !expires.IsZero() &&
		created.Location() == time.UTC && expires.Location() == time.UTC &&
		expires.After(created) && expires.Sub(created) <= 7*24*time.Hour
}

func (store *FileEvidenceStore) legacyCacheSources() ([]sqlitestore.LegacySource, error) {
	if store == nil || store.root == nil {
		return nil, errors.New("local CI evidence root is closed")
	}
	info, err := store.root.Lstat("cache")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !privateEvidenceMode(info) {
		return nil, errors.New("legacy local CI cache directory is unsafe")
	}
	entries, err := store.readLegacyCacheDirectory("cache", maximumLocalCICacheEntries+maximumLegacyCacheDirectories)
	if err != nil {
		return nil, err
	}
	sources := make([]sqlitestore.LegacySource, 0, len(entries))
	directoryCount := 0
	for _, entry := range entries {
		relative := filepath.Join("cache", entry.Name())
		entryInfo, statErr := store.root.Lstat(relative)
		if statErr != nil {
			return nil, statErr
		}
		if entryInfo.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("legacy local CI cache contains a symlink")
		}
		if entryInfo.IsDir() {
			directoryCount++
			if directoryCount > maximumLegacyCacheDirectories || !privateEvidenceMode(entryInfo) {
				return nil, errors.New("legacy local CI cache directory limit or mode is invalid")
			}
			children, readErr := store.readLegacyCacheDirectory(
				relative,
				maximumLocalCICacheEntries-len(sources),
			)
			if readErr != nil {
				return nil, readErr
			}
			for _, child := range children {
				childRelative := filepath.Join(relative, child.Name())
				childInfo, childErr := store.root.Lstat(childRelative)
				if childErr != nil {
					return nil, childErr
				}
				if childInfo.Mode()&os.ModeSymlink != 0 || !privateEvidenceFile(childInfo) {
					return nil, errors.New("legacy local CI cache contains an unsafe entry")
				}
				sources = append(sources, legacyCacheSource(childRelative))
			}
			continue
		}
		if !privateEvidenceFile(entryInfo) {
			return nil, errors.New("legacy local CI cache contains an unsafe entry")
		}
		sources = append(sources, legacyCacheSource(relative))
	}
	if len(sources) > maximumLocalCICacheEntries {
		return nil, errors.New("legacy local CI cache source count exceeds its limit")
	}
	sort.Slice(sources, func(left, right int) bool {
		return sources[left].Relative < sources[right].Relative
	})
	return sources, nil
}

func (store *FileEvidenceStore) readLegacyCacheDirectory(
	relative string,
	limit int,
) ([]os.DirEntry, error) {
	if limit < 0 {
		return nil, errors.New("legacy local CI cache enumeration exceeds its limit")
	}
	before, err := store.root.Lstat(relative)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 || !privateEvidenceMode(before) {
		return nil, errors.New("legacy local CI cache directory is unsafe")
	}
	directory, err := store.root.Open(relative)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	opened, openedErr := directory.Stat()
	after, afterErr := store.root.Lstat(relative)
	if openedErr != nil || afterErr != nil || !opened.IsDir() ||
		after.Mode()&os.ModeSymlink != 0 || !after.IsDir() ||
		!privateEvidenceMode(opened) || !privateEvidenceMode(after) ||
		!os.SameFile(before, opened) || !os.SameFile(opened, after) {
		return nil, errors.New("legacy local CI cache directory changed while opening")
	}
	entries, err := directory.ReadDir(limit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > limit {
		return nil, errors.New("legacy local CI cache enumeration exceeds its limit")
	}
	return entries, nil
}

func legacyCacheSource(relative string) sqlitestore.LegacySource {
	relative = filepath.ToSlash(relative)
	digest := sha256.Sum256([]byte(relative))
	return sqlitestore.LegacySource{
		ID:       fmt.Sprintf("cache-%x", digest[:]),
		Relative: relative,
		MaxBytes: maximumLegacyCacheIndexBytes,
	}
}

func (store *FileEvidenceStore) importLegacyCacheIndex(
	ctx context.Context,
	conn *sql.Conn,
	input sqlitestore.LegacyInput,
) (sqlitestore.ImportResult, error) {
	skip := func(code string) (sqlitestore.ImportResult, error) {
		return sqlitestore.ImportResult{
			Skipped: 1,
			Issues: []sqlitestore.ImportIssue{{
				Code:         code,
				RecordDigest: input.Digest,
			}},
		}, nil
	}
	pathKey, validPath := legacyCacheResultKey(input.Relative)
	if !validPath {
		return skip("invalid-identity")
	}
	var record cacheIndexRecord
	if err := decodeStrictEvidence(input.Data, &record); err != nil {
		return skip("malformed-index")
	}
	normalized, err := finalizeCacheIndex(record)
	if err != nil || record.Version != EvidenceVersion || normalized.Digest != record.Digest ||
		normalized.ResultKey != pathKey {
		return skip("invalid-index")
	}
	found, err := store.validateLegacyImmutableEvidencePath("executions", normalized.ExecutionDigest)
	if err != nil {
		return sqlitestore.ImportResult{}, err
	}
	if !found {
		return skip("missing-execution")
	}
	var rawExecution Execution
	if found, err = store.readObject(ctx, "executions", normalized.ExecutionDigest, &rawExecution); err != nil {
		if errors.Is(err, ErrEvidenceCorrupt) {
			return skip("invalid-execution")
		}
		return sqlitestore.ImportResult{}, err
	}
	if !found || !validDigest(rawExecution.Evidence.PlanDigest) {
		return skip("invalid-execution")
	}
	found, err = store.validateLegacyImmutableEvidencePath("plans", rawExecution.Evidence.PlanDigest)
	if err != nil {
		return sqlitestore.ImportResult{}, err
	}
	if !found {
		return skip("missing-plan")
	}
	execution, found, err := localCICacheGetExecution(store, ctx, normalized.ExecutionDigest)
	if err != nil {
		if errors.Is(err, ErrEvidenceCorrupt) || errors.Is(err, ErrInvalid) {
			return skip("invalid-execution")
		}
		return sqlitestore.ImportResult{}, err
	}
	if !found || execution.Status != StatusPassed || execution.ResultKey != normalized.ResultKey {
		return skip("invalid-execution")
	}
	result, err := conn.ExecContext(ctx, `INSERT INTO passing_cache (
        result_key, execution_digest, execution_status,
        created_at_unix_seconds, created_at_nanosecond,
        expires_at_unix_seconds, expires_at_nanosecond, version
    ) VALUES (?, ?, 'passed', ?, ?, ?, ?, 1)
    ON CONFLICT(result_key) DO NOTHING`,
		normalized.ResultKey,
		normalized.ExecutionDigest,
		normalized.CreatedAt.Unix(),
		normalized.CreatedAt.Nanosecond(),
		normalized.ExpiresAt.Unix(),
		normalized.ExpiresAt.Nanosecond(),
	)
	if err != nil {
		return sqlitestore.ImportResult{}, err
	}
	inserted, err := localCICacheRowsAffected(result)
	if err != nil {
		return sqlitestore.ImportResult{}, err
	}
	if inserted == 0 {
		return skip("sqlite-authoritative")
	}
	return sqlitestore.ImportResult{Imported: 1}, nil
}

func legacyCacheResultKey(relative string) (string, bool) {
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) != 3 || parts[0] != "cache" || len(parts[1]) != 2 ||
		!strings.HasSuffix(parts[2], ".json") {
		return "", false
	}
	key := strings.TrimSuffix(parts[2], ".json")
	return key, validDigest(key) && parts[1] == key[:2]
}

func (store *FileEvidenceStore) validateLegacyImmutableEvidencePath(
	kind, digest string,
) (bool, error) {
	if !validDigest(digest) {
		return false, nil
	}
	directory := filepath.Join(kind, digest[:2])
	info, err := store.root.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !privateEvidenceMode(info) {
		return false, errors.New("local CI immutable evidence directory is unsafe")
	}
	path := store.objectRelative(kind, digest)
	info, err = store.root.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !privateEvidenceFile(info) {
		return false, errors.New("local CI immutable evidence file is unsafe")
	}
	return true, nil
}
