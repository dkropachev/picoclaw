// Package sqliteprovider is the only PicoClaw package allowed to bind the
// shipped SQLite driver to database/sql. Higher layers identify stores by
// logical ID and must not construct provider DSNs themselves.
//
//nolint:govet // Filesystem preparation stages intentionally use narrow error scopes.
package sqliteprovider

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	moderncsqlite "modernc.org/sqlite"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

const driverName = "sqlite"

var memoryDatabaseSequence atomic.Uint64

// DriverName is intentionally limited to provider and private compatibility
// bridge code. Application-facing APIs must not expose it.
func DriverName() string { return driverName }

// open creates a database/sql pool using the shipped provider.
func open(dsn string) (*sql.DB, error) { return sql.Open(driverName, dsn) }

// OpenStore securely prepares a physical store, constructs its private DSN,
// and opens the shipped provider. Only broker/provider and temporary bridge
// code may call it.
func OpenStore(path string, busyTimeout time.Duration) (*sql.DB, error) {
	if path == ":memory:" {
		dsn, err := DSN(path, busyTimeout)
		if err != nil {
			return nil, err
		}
		return open(dsn)
	}
	retained, err := adoptInspectedPool(path)
	if err != nil {
		return nil, err
	}
	if retained != nil {
		return retained, nil
	}
	if err := PrepareStore(path); err != nil {
		return nil, err
	}
	dsn, err := DSN(path, busyTimeout)
	if err != nil {
		return nil, err
	}
	database, err := open(dsn)
	if err != nil {
		return nil, err
	}
	if err := SecureGeneration(path); err != nil {
		_ = database.Close()
		return nil, err
	}
	return database, nil
}

// Configure applies and verifies the provider's live durability contract.
func Configure(ctx context.Context, database *sql.DB, busyTimeout time.Duration, memory bool) error {
	if database == nil {
		return errors.New("SQLite provider pool is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	journal, err := EnableWAL(ctx, database, busyTimeout)
	if err != nil {
		return fmt.Errorf("configure SQLite provider journal: %w", err)
	}
	if !memory && !strings.EqualFold(journal, "wal") {
		return fmt.Errorf("configure SQLite provider journal: selected %q", journal)
	}
	var foreignKeys, configuredBusy, synchronous int
	if err := database.QueryRowContext(ctx, `
		SELECT fk.foreign_keys, bt.timeout, sm.synchronous
		  FROM pragma_foreign_keys AS fk
		 CROSS JOIN pragma_busy_timeout AS bt
		 CROSS JOIN pragma_synchronous AS sm
	`).Scan(&foreignKeys, &configuredBusy, &synchronous); err != nil {
		return fmt.Errorf("verify SQLite provider configuration: %w", err)
	}
	if foreignKeys != 1 || configuredBusy != int(busyTimeout.Milliseconds()) || synchronous != 2 {
		return fmt.Errorf(
			"verify SQLite provider configuration: foreign_keys=%d busy_timeout=%d synchronous=%d",
			foreignKeys,
			configuredBusy,
			synchronous,
		)
	}
	return nil
}

// EnableWAL selects WAL with a bounded retry for transient provider locking.
// It exists so compatibility layers never carry provider-control statements.
func EnableWAL(ctx context.Context, database *sql.DB, busyTimeout time.Duration) (string, error) {
	if database == nil {
		return "", errors.New("SQLite provider pool is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	retryCtx, cancel := context.WithTimeout(ctx, busyTimeout)
	defer cancel()
	delay := time.Millisecond
	for {
		var journal string
		err := database.QueryRowContext(retryCtx, "PRAGMA journal_mode = WAL").Scan(&journal)
		if err == nil || !IsBusyOrLocked(err) {
			return journal, err
		}
		timer := time.NewTimer(delay)
		select {
		case <-retryCtx.Done():
			timer.Stop()
			return "", retryCtx.Err()
		case <-timer.C:
		}
		if delay < 50*time.Millisecond {
			delay *= 2
		}
	}
}

// ConfigureOffline establishes the exclusive rollback-journal boundary used
// by domain migrations. Callers must already hold the canonical-home migration
// fence and must constrain the pool to one connection before calling it.
func ConfigureOffline(ctx context.Context, database *sql.DB, busyTimeout time.Duration) error {
	if database == nil {
		return errors.New("SQLite provider pool is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	connection, err := database.Conn(ctx)
	if err != nil {
		return fmt.Errorf("open SQLite offline provider connection: %w", err)
	}
	defer connection.Close()
	var locking, journal string
	if err := connection.QueryRowContext(ctx, "PRAGMA locking_mode = EXCLUSIVE").Scan(&locking); err != nil {
		return fmt.Errorf("configure SQLite offline locking: %w", err)
	}
	if !strings.EqualFold(locking, "exclusive") {
		return fmt.Errorf("configure SQLite offline locking: selected %q", locking)
	}
	if err := connection.QueryRowContext(ctx, "PRAGMA journal_mode = DELETE").Scan(&journal); err != nil {
		return fmt.Errorf("configure SQLite offline journal: %w", err)
	}
	if !strings.EqualFold(journal, "delete") {
		return fmt.Errorf("configure SQLite offline journal: selected %q", journal)
	}
	var foreignKeys, configuredBusy, synchronous int
	if err := connection.QueryRowContext(ctx, `
		SELECT fk.foreign_keys, bt.timeout, sm.synchronous
		  FROM pragma_foreign_keys AS fk
		 CROSS JOIN pragma_busy_timeout AS bt
		 CROSS JOIN pragma_synchronous AS sm
	`).Scan(&foreignKeys, &configuredBusy, &synchronous); err != nil {
		return fmt.Errorf("verify SQLite offline provider configuration: %w", err)
	}
	if foreignKeys != 1 || configuredBusy != int(busyTimeout.Milliseconds()) || synchronous != 2 {
		return fmt.Errorf(
			"verify SQLite offline provider configuration: foreign_keys=%d busy_timeout=%d synchronous=%d",
			foreignKeys,
			configuredBusy,
			synchronous,
		)
	}
	return nil
}

// PrepareStore validates and creates an owner-private empty database endpoint
// without asking SQLite to follow an unsafe path or sidecar.
func PrepareStore(path string) error {
	if strings.TrimSpace(path) == "" || strings.ContainsRune(path, '\x00') ||
		strings.HasPrefix(strings.ToLower(path), "file:") {
		return errors.New("SQLite provider path is invalid")
	}
	parent := filepath.Dir(path)
	if err := fileutil.MkdirAllDurable(parent, 0o700); err != nil {
		return fmt.Errorf("create SQLite provider directory: %w", err)
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("SQLite provider directory is unsafe")
	}
	if err := secureProviderDirectory(parent); err != nil {
		return fmt.Errorf("secure SQLite provider directory: %w", err)
	}
	if err := validateGenerationMembers(path, false); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("prepare SQLite provider store: %w", err)
	}
	info, statErr := file.Stat()
	pathInfo, lstatErr := os.Lstat(path)
	if statErr != nil || lstatErr != nil || !info.Mode().IsRegular() ||
		!pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(info, pathInfo) {
		_ = file.Close()
		return errors.Join(errors.New("SQLite provider store changed while opening"), statErr, lstatErr)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("secure SQLite provider store: %w", err)
	}
	if err := secureProviderFile(path); err != nil {
		_ = file.Close()
		return fmt.Errorf("secure SQLite provider store ACL: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync SQLite provider store: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close prepared SQLite provider store: %w", err)
	}
	return fileutil.SyncDirectory(parent)
}

// EnsurePrivateDirectory creates and validates an owner-private real
// directory for provider generations and lock namespaces.
func EnsurePrivateDirectory(path string) error {
	if strings.TrimSpace(path) == "" || strings.ContainsRune(path, 0) {
		return errors.New("SQLite provider directory is invalid")
	}
	if err := fileutil.MkdirAllDurable(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("SQLite provider directory must be a real directory")
	}
	if err := secureProviderDirectory(path); err != nil {
		return err
	}
	return fileutil.SyncDirectory(path)
}

// SecureGeneration validates existing database, WAL, SHM, and rollback-journal members with
// non-opening metadata operations and enforces owner-only modes.
func SecureGeneration(path string) error { return validateGenerationMembers(path, true) }

func validateGenerationMembers(path string, requireDatabase bool) error {
	for index, member := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
		optional := index > 0
		info, err := os.Lstat(member)
		if errors.Is(err, os.ErrNotExist) {
			if requireDatabase && index == 0 {
				return errors.New("SQLite provider store disappeared")
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect SQLite provider generation: %w", err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("SQLite provider generation member is not a regular file")
		}
		if !generationHasSingleLink(member, info) {
			return errors.New("SQLite provider generation member has a hardlink alias")
		}
		if !generationOwnedByCurrentUser(member, info) {
			return errors.New("SQLite provider generation member is owned by another user")
		}
		if err := secureProviderFile(member); err != nil {
			if optional && errors.Is(err, os.ErrNotExist) {
				if _, currentErr := os.Lstat(member); errors.Is(currentErr, os.ErrNotExist) {
					continue
				}
			}
			return fmt.Errorf("secure SQLite provider generation: %w", err)
		}
		current, currentErr := os.Lstat(member)
		if optional && errors.Is(currentErr, os.ErrNotExist) {
			continue
		}
		if currentErr != nil || current == nil || !current.Mode().IsRegular() ||
			current.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, current) {
			return errors.Join(
				errors.New("SQLite provider generation member changed while securing"),
				currentErr,
			)
		}
	}
	return nil
}

// DSN constructs the durable connection configuration owned by this provider.
func DSN(path string, busyTimeout time.Duration) (string, error) {
	if path == ":memory:" {
		name := "picoclaw-memory-" + strconv.FormatUint(memoryDatabaseSequence.Add(1), 10)
		return "file:" + name + "?mode=memory&cache=shared&_pragma=foreign_keys(1)&_pragma=busy_timeout(" +
			strconv.FormatInt(busyTimeout.Milliseconds(), 10) + ")&_pragma=synchronous(FULL)", nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	slashPath := FileURLPath(
		filepath.ToSlash(abs),
		filepath.ToSlash(filepath.VolumeName(abs)),
	)
	u := &url.URL{Scheme: "file", Path: slashPath}
	query := url.Values{}
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "busy_timeout("+strconv.FormatInt(busyTimeout.Milliseconds(), 10)+")")
	query.Add("_pragma", "synchronous(FULL)")
	u.RawQuery = query.Encode()
	return u.String(), nil
}

// FileURLPath normalizes a physical path for the provider's file URI syntax.
func FileURLPath(slashPath, slashVolume string) string {
	if slashVolume != "" && !strings.HasPrefix(slashPath, "/") {
		return "/" + slashPath
	}
	return slashPath
}

// IsBusyOrLocked reports the two SQLite primary result codes for which a
// bounded read-side retry is safe.
func IsBusyOrLocked(err error) bool {
	var sqliteErr *moderncsqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	switch sqliteErr.Code() & 0xff {
	case 5, 6: // SQLITE_BUSY, SQLITE_LOCKED
		return true
	default:
		return false
	}
}
