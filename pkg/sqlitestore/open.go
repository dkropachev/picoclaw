// Package sqlitestore provides the common security, durability, and schema
// contract for PicoClaw-owned SQLite databases.
package sqlitestore

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

const (
	// DefaultBusyTimeout bounds lock waits while still allowing short concurrent
	// transactions from the gateway and CLI to serialize.
	DefaultBusyTimeout = 5 * time.Second
	defaultOpenConns   = 4
)

var (
	// ErrTooNew reports a database written by a newer PicoClaw schema.
	ErrTooNew = errors.New("SQLite database schema is newer than supported")
	// ErrInvalidSchema reports a missing, unexpected, or invalid schema object.
	ErrInvalidSchema = errors.New("SQLite database schema is invalid")
	// ErrIntegrity reports a failed SQLite integrity check.
	ErrIntegrity = errors.New("SQLite database integrity check failed")

	memoryDatabaseSequence atomic.Uint64

	openSQLiteDatabase             = sql.Open
	absoluteSQLitePath             = filepath.Abs
	lstatSQLitePath                = os.Lstat
	chmodSQLitePath                = os.Chmod
	mkdirAllSQLiteDirectories      = fileutil.MkdirAllDurable
	syncSQLiteDirectory            = fileutil.SyncDirectory
	configureOpenedSQLiteDatabase  = configure
	secureOpenedSQLiteFiles        = secureSQLiteFiles
	migrateOpenedSQLiteDatabase    = migrate
	checkOpenedSQLiteIntegrity     = integrityCheck
	archiveOpenedSQLiteLegacyFiles = archiveImportedSources
	openSQLiteFile                 = func(path string, flag int, mode os.FileMode) (sqliteFile, error) {
		return os.OpenFile(path, flag, mode)
	}
)

type sqliteFile interface {
	Stat() (os.FileInfo, error)
	Chmod(mode os.FileMode) error
	Sync() error
	Close() error
}

// Migration upgrades a database from Version-1 to Version. Versions must be
// contiguous and start at one. Statements run inside one BEGIN IMMEDIATE
// transaction together with any legacy import.
type Migration struct {
	Version    int
	Statements []string
	Apply      func(context.Context, *sql.Conn) error
}

// Options defines one subsystem-local database.
type Options struct {
	Component    string
	Migrations   []Migration
	Validate     func(context.Context, *sql.Conn) error
	Legacy       *LegacyOptions
	BusyTimeout  time.Duration
	MaxOpenConns int
}

// Open securely creates or opens path, applies schema migrations, imports any
// legacy sources, validates the resulting schema, and enables the durability
// PRAGMAs shared by all mutable PicoClaw stores.
func Open(ctx context.Context, path string, options Options) (_ *sql.DB, returnErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	component := strings.TrimSpace(options.Component)
	if !validIdentifier(component) {
		return nil, errors.New("sqlite store component is invalid")
	}
	if err := validateMigrations(options.Migrations); err != nil {
		return nil, fmt.Errorf("%s schema: %w", component, err)
	}
	if err := validateDatabasePath(path); err != nil {
		return nil, fmt.Errorf("%s database path: %w", component, err)
	}

	fileBacked := path != ":memory:"
	if fileBacked {
		if err := prepareDatabaseFile(path); err != nil {
			return nil, fmt.Errorf("prepare %s database: %w", component, err)
		}
		// Reject unsafe or stale sidecars before SQLite has an opportunity to
		// follow them. A second pass below covers sidecars created while opening.
		if err := secureOpenedSQLiteFiles(path); err != nil {
			return nil, fmt.Errorf("secure %s database files: %w", component, err)
		}
	}

	busyTimeout := options.BusyTimeout
	if busyTimeout == 0 {
		busyTimeout = DefaultBusyTimeout
	}
	if busyTimeout < time.Millisecond || busyTimeout > time.Minute {
		return nil, fmt.Errorf("%s busy timeout is outside the supported range", component)
	}
	dsn, err := sqliteDSN(path, busyTimeout)
	if err != nil {
		return nil, fmt.Errorf("resolve %s database DSN: %w", component, err)
	}
	db, err := openSQLiteDatabase("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s database: %w", component, err)
	}
	defer func() {
		if returnErr != nil {
			_ = db.Close()
		}
	}()

	maxOpen := options.MaxOpenConns
	if maxOpen == 0 {
		maxOpen = defaultOpenConns
	}
	if maxOpen < 1 || maxOpen > 32 {
		return nil, fmt.Errorf("%s max open connections is outside the supported range", component)
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxOpen)
	db.SetConnMaxLifetime(0)

	if err = configureOpenedSQLiteDatabase(ctx, db, busyTimeout, !fileBacked, component); err != nil {
		return nil, err
	}
	if fileBacked {
		if err = secureOpenedSQLiteFiles(path); err != nil {
			return nil, fmt.Errorf("secure %s database files: %w", component, err)
		}
	}
	if err = migrateOpenedSQLiteDatabase(ctx, db, options); err != nil {
		return nil, err
	}
	if err = checkOpenedSQLiteIntegrity(ctx, db, component); err != nil {
		return nil, err
	}
	if fileBacked {
		if err = secureOpenedSQLiteFiles(path); err != nil {
			return nil, fmt.Errorf("secure %s database files: %w", component, err)
		}
	}
	if options.Legacy != nil {
		if err = archiveOpenedSQLiteLegacyFiles(ctx, db, component, *options.Legacy); err != nil {
			return nil, err
		}
	}
	return db, nil
}

func validateDatabasePath(path string) error {
	if path == "" || path != strings.TrimSpace(path) {
		return errors.New("path is required")
	}
	if strings.ContainsRune(path, '\x00') {
		return errors.New("path contains a NUL byte")
	}
	if strings.HasPrefix(strings.ToLower(path), "file:") {
		return errors.New("SQLite URIs are not accepted")
	}
	return nil
}

func prepareDatabaseFile(path string) error {
	parent := filepath.Dir(path)
	if err := ensurePrivateDir(parent); err != nil {
		return err
	}
	created := false
	if info, err := lstatSQLitePath(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("database must be a regular file")
		}
	} else if !os.IsNotExist(err) {
		return err
	} else {
		created = true
	}
	file, err := openSQLiteFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	openedInfo, statErr := file.Stat()
	pathInfo, lstatErr := lstatSQLitePath(path)
	if statErr != nil || lstatErr != nil || !openedInfo.Mode().IsRegular() ||
		!pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(openedInfo, pathInfo) {
		_ = file.Close()
		return errors.Join(
			errors.New("database changed while opening"),
			statErr,
			lstatErr,
		)
	}
	if chmodErr := file.Chmod(0o600); chmodErr != nil {
		_ = file.Close()
		return chmodErr
	}
	if syncErr := file.Sync(); syncErr != nil {
		_ = file.Close()
		return syncErr
	}
	if closeErr := file.Close(); closeErr != nil {
		return closeErr
	}
	if created {
		return syncSQLiteDirectory(parent)
	}
	return nil
}

// EnsurePrivateDir creates path as a private directory and rejects a symlink
// at the database directory boundary.
func EnsurePrivateDir(path string) error { return ensurePrivateDir(path) }

func ensurePrivateDir(path string) error {
	if strings.TrimSpace(path) == "" || strings.ContainsRune(path, '\x00') {
		return errors.New("database directory is invalid")
	}
	if err := mkdirAllSQLiteDirectories(path, 0o700); err != nil {
		return err
	}
	info, err := lstatSQLitePath(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("database directory must be a real directory")
	}
	if err := chmodSQLitePath(path, 0o700); err != nil {
		return err
	}
	return nil
}

func sqliteDSN(path string, busyTimeout time.Duration) (string, error) {
	if path == ":memory:" {
		name := "picoclaw-memory-" + strconv.FormatUint(memoryDatabaseSequence.Add(1), 10)
		return "file:" + name + "?mode=memory&cache=shared&_pragma=foreign_keys(1)&_pragma=busy_timeout(" +
			strconv.FormatInt(busyTimeout.Milliseconds(), 10) + ")&_pragma=synchronous(FULL)", nil
	}
	abs, err := absoluteSQLitePath(path)
	if err != nil {
		return "", err
	}
	slashPath := sqliteFilePath(
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

func sqliteFilePath(slashPath, slashVolume string) string {
	if slashVolume != "" && !strings.HasPrefix(slashPath, "/") {
		return "/" + slashPath
	}
	return slashPath
}

func configure(
	ctx context.Context,
	db *sql.DB,
	busyTimeout time.Duration,
	memory bool,
	component string,
) error {
	var journal string
	if err := configureWAL(ctx, db, busyTimeout, &journal); err != nil {
		return fmt.Errorf("enable %s WAL: %w", component, err)
	}
	if !memory && !strings.EqualFold(journal, "wal") {
		return fmt.Errorf("enable %s WAL: SQLite selected %q", component, journal)
	}
	var foreignKeys, configuredBusy, synchronous int
	if err := db.QueryRowContext(ctx, `
		SELECT fk.foreign_keys, bt.timeout, sm.synchronous
		  FROM pragma_foreign_keys AS fk
		 CROSS JOIN pragma_busy_timeout AS bt
		 CROSS JOIN pragma_synchronous AS sm
	`).Scan(&foreignKeys, &configuredBusy, &synchronous); err != nil {
		return fmt.Errorf("verify %s SQLite configuration: %w", component, err)
	}
	if foreignKeys != 1 || configuredBusy != int(busyTimeout.Milliseconds()) || synchronous != 2 {
		return fmt.Errorf(
			"verify %s SQLite configuration: foreign_keys=%d busy_timeout=%d synchronous=%d",
			component, foreignKeys, configuredBusy, synchronous,
		)
	}
	return nil
}

func configureWAL(
	ctx context.Context,
	db *sql.DB,
	busyTimeout time.Duration,
	journal *string,
) error {
	retryCtx, cancel := context.WithTimeout(ctx, busyTimeout)
	defer cancel()
	delay := time.Millisecond
	for {
		err := db.QueryRowContext(retryCtx, "PRAGMA journal_mode = WAL").Scan(journal)
		if err == nil || !sqliteBusyOrLocked(err) {
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-retryCtx.Done():
			timer.Stop()
			return retryCtx.Err()
		case <-timer.C:
		}
		if delay < 50*time.Millisecond {
			delay *= 2
		}
	}
}

func sqliteBusyOrLocked(err error) bool {
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

func validateMigrations(migrations []Migration) error {
	if len(migrations) == 0 {
		return errors.New("at least one schema migration is required")
	}
	for index, migration := range migrations {
		if migration.Version != index+1 {
			return fmt.Errorf("migration version %d is not contiguous", migration.Version)
		}
		if len(migration.Statements) == 0 && migration.Apply == nil {
			return fmt.Errorf("migration version %d has no work", migration.Version)
		}
		for _, statement := range migration.Statements {
			if forbidden, scanErr := migrationStatementForbidden(statement); scanErr != nil {
				return fmt.Errorf("migration version %d contains invalid SQL: %w", migration.Version, scanErr)
			} else if forbidden {
				return fmt.Errorf("migration version %d contains a forbidden statement", migration.Version)
			}
		}
	}
	return nil
}

func migrate(ctx context.Context, db *sql.DB, options Options) error {
	var importSummary legacyImportSummary
	err := Immediate(ctx, db, func(conn *sql.Conn) error {
		// Check the pre-migration image while the same immediate transaction that
		// will perform the upgrade is active. A corrupt database must never enter
		// a committing migration.
		if err := integrityCheckConn(ctx, conn, options.Component); err != nil {
			return err
		}
		var current int
		if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
			return fmt.Errorf("read %s schema version: %w", options.Component, err)
		}
		latest := len(options.Migrations)
		if current > latest {
			return fmt.Errorf(
				"%w: "+
					"%s database schema version %d is newer than supported version %d",
				ErrTooNew, options.Component, current, latest,
			)
		}
		if err := createImportSchema(ctx, conn); err != nil {
			return fmt.Errorf("create %s import schema: %w", options.Component, err)
		}
		for _, migration := range options.Migrations[current:] {
			for _, statement := range migration.Statements {
				if _, err := conn.ExecContext(ctx, statement); err != nil {
					return fmt.Errorf(
						"apply %s schema version %d: %w",
						options.Component, migration.Version, err,
					)
				}
			}
			if migration.Apply != nil {
				if err := migration.Apply(ctx, conn); err != nil {
					return fmt.Errorf(
						"apply %s schema data migration version %d: %w",
						options.Component, migration.Version, err,
					)
				}
			}
			if _, err := conn.ExecContext(
				ctx,
				"PRAGMA user_version = "+strconv.Itoa(migration.Version),
			); err != nil {
				return fmt.Errorf("record %s schema version: %w", options.Component, err)
			}
		}
		if options.Legacy != nil {
			var err error
			importSummary, err = importLegacySources(
				ctx,
				conn,
				options.Component,
				*options.Legacy,
			)
			if err != nil {
				return err
			}
		}
		if options.Validate != nil {
			if err := options.Validate(ctx, conn); err != nil {
				return fmt.Errorf("%w: validate %s schema: %v", ErrInvalidSchema, options.Component, err)
			}
		}
		if err := validateImportSchema(ctx, conn); err != nil {
			return fmt.Errorf("%w: validate %s import schema: %v", ErrInvalidSchema, options.Component, err)
		}
		return integrityCheckConn(ctx, conn, options.Component)
	})
	if err != nil {
		return err
	}
	logLegacyImportSummary(options.Component, importSummary)
	return nil
}

// Immediate executes fn inside an explicit BEGIN IMMEDIATE transaction.
func Immediate(ctx context.Context, db *sql.DB, fn func(*sql.Conn) error) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if fn == nil {
		return errors.New("SQLite transaction callback is required")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if err = fn(conn); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

// RequireOneRow reports the driver's RowsAffected error or conflict unless
// result describes exactly one changed row.
func RequireOneRow(result sql.Result, conflict error) error {
	if result == nil {
		return errors.New("SQLite result is required")
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return conflict
	}
	return nil
}

// ScanStrings consumes a one-column TEXT result set and closes it.
func ScanStrings(rows *sql.Rows) ([]string, error) {
	if rows == nil {
		return nil, errors.New("SQLite rows are required")
	}
	defer rows.Close()
	values := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func integrityCheck(ctx context.Context, db *sql.DB, component string) error {
	if err := integrityCheckQuery(ctx, db, component); err != nil {
		return err
	}
	return foreignKeyCheckQuery(ctx, db, component)
}

type contextQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func integrityCheckConn(ctx context.Context, conn *sql.Conn, component string) error {
	if err := integrityCheckQuery(ctx, conn, component); err != nil {
		return err
	}
	return foreignKeyCheckQuery(ctx, conn, component)
}

func integrityCheckQuery(ctx context.Context, queryer contextQueryer, component string) error {
	var result string
	if err := queryer.QueryRowContext(ctx, "PRAGMA integrity_check(1)").Scan(&result); err != nil {
		return fmt.Errorf("%w: check %s database: %v", ErrIntegrity, component, err)
	}
	if result != "ok" {
		return fmt.Errorf("%w: check %s database: corruption reported", ErrIntegrity, component)
	}
	return nil
}

func foreignKeyCheckQuery(ctx context.Context, queryer contextQueryer, component string) error {
	rows, err := queryer.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("%w: check %s database foreign keys: %v", ErrIntegrity, component, err)
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("%w: check %s database foreign keys: violation reported", ErrIntegrity, component)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%w: check %s database foreign keys: %v", ErrIntegrity, component, err)
	}
	return nil
}

func secureSQLiteFiles(path string) error {
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		info, err := lstatSQLitePath(candidate)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is not a regular SQLite file", filepath.Base(candidate))
		}
		file, err := openSQLiteFile(candidate, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		openedInfo, statErr := file.Stat()
		currentInfo, lstatErr := lstatSQLitePath(candidate)
		if statErr != nil || lstatErr != nil || !openedInfo.Mode().IsRegular() ||
			!currentInfo.Mode().IsRegular() || currentInfo.Mode()&os.ModeSymlink != 0 ||
			!os.SameFile(openedInfo, currentInfo) {
			_ = file.Close()
			return errors.Join(
				fmt.Errorf("%s changed while opening", filepath.Base(candidate)),
				statErr,
				lstatErr,
			)
		}
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
	}
	return nil
}

func validIdentifier(value string) bool {
	if value == "" || len(value) > 80 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}
