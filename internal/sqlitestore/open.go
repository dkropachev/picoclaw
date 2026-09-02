// Package sqlitestore provides the common security, durability, and schema
// contract for PicoClaw-owned SQLite databases.
package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	dblayer "github.com/sipeed/picoclaw/pkg/database"
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

	openSQLiteDatabase             = sqliteprovider.OpenStore
	absoluteSQLitePath             = filepath.Abs
	configureOpenedSQLiteDatabase  = configure
	secureOpenedSQLiteFiles        = sqliteprovider.SecureGeneration
	migrateOpenedSQLiteDatabase    = migrate
	checkOpenedSQLiteIntegrity     = integrityCheck
	archiveOpenedSQLiteLegacyFiles = archiveImportedSources
)

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
	if path != ":memory:" && !dblayer.BrokerAuthorityHeld() && !dblayer.MigrationFenceHeld() &&
		!dblayer.ProviderTestAuthorityHeld() {
		return nil, dblayer.NewError(
			dblayer.CodeUnauthorized,
			"database adapter requires broker or migration authority",
		)
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
	if fileBacked && dblayer.OnlineFenceHeld() {
		legacyExists, legacyErr := onlineLegacySourceExists(options.Legacy)
		if legacyErr != nil {
			return nil, legacyErr
		}
		if legacyExists {
			return nil, dblayer.NewError(
				dblayer.CodeMigrationRequired,
				"offline database migration is required",
			)
		}
	}

	busyTimeout := options.BusyTimeout
	if busyTimeout == 0 {
		busyTimeout = DefaultBusyTimeout
	}
	if busyTimeout < time.Millisecond || busyTimeout > time.Minute {
		return nil, fmt.Errorf("%s busy timeout is outside the supported range", component)
	}
	_, err := sqliteDSN(path, busyTimeout)
	if err != nil {
		return nil, fmt.Errorf("resolve %s database DSN: %w", component, err)
	}
	db, err := openSQLiteDatabase(path, busyTimeout)
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
	if fileBacked && dblayer.MigrationFenceHeld() {
		maxOpen = 1
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
	if dblayer.OnlineFenceHeld() {
		initialize, readinessErr := requireOnlineCurrentStore(ctx, db, options)
		if readinessErr != nil {
			return nil, readinessErr
		}
		if initialize {
			if err = migrateOpenedSQLiteDatabase(ctx, db, options); err != nil {
				return nil, err
			}
		} else if err = validateCurrentStore(ctx, db, options); err != nil {
			return nil, err
		}
	} else if err = migrateOpenedSQLiteDatabase(ctx, db, options); err != nil {
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
	if options.Legacy != nil && !dblayer.OnlineFenceHeld() {
		if err = archiveOpenedSQLiteLegacyFiles(ctx, db, component, *options.Legacy); err != nil {
			return nil, err
		}
	}
	return db, nil
}

func requireOnlineCurrentStore(
	ctx context.Context,
	db *sql.DB,
	options Options,
) (bool, error) {
	version, err := sqliteprovider.SchemaVersion(ctx, db)
	if err != nil {
		return false, err
	}
	latest := len(options.Migrations)
	if version > latest {
		return false, fmt.Errorf(
			"%w: %s database schema version %d is newer than supported version %d",
			ErrTooNew,
			options.Component,
			version,
			latest,
		)
	}
	legacyExists, err := onlineLegacySourceExists(options.Legacy)
	if err != nil {
		return false, err
	}
	var objects int
	if err := db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'`,
	).Scan(&objects); err != nil {
		return false, err
	}
	if version == 0 && objects == 0 && !legacyExists {
		return true, nil
	}
	if version < latest || legacyExists {
		return false, dblayer.NewError(
			dblayer.CodeMigrationRequired,
			"offline database migration is required",
		)
	}
	importSchemaReady, err := sharedImportSchemaObjectsPresent(ctx, db)
	if err != nil {
		return false, err
	}
	if !importSchemaReady {
		// The provider-owned import horizon was added without changing each
		// domain's user_version. A generation at the domain version but missing
		// those shared objects is therefore outdated, not corrupt. Online startup
		// must classify it for fenced offline migration without repairing it.
		return false, dblayer.NewError(
			dblayer.CodeMigrationRequired,
			"offline database migration is required",
		)
	}
	return false, nil
}

func sharedImportSchemaObjectsPresent(ctx context.Context, queryer contextQueryer) (bool, error) {
	var count int
	if err := queryer.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
	    WHERE (type = 'table' AND name IN (
	        'storage_imports', 'storage_import_issues', 'storage_import_horizons'
	    )) OR (type = 'index' AND name = 'storage_imports_archive_status_idx')`).Scan(&count); err != nil {
		return false, err
	}
	return count == 4, nil
}

func onlineLegacySourceExists(options *LegacyOptions) (bool, error) {
	if options == nil || options.Sources == nil {
		return false, nil
	}
	sources, err := options.Sources()
	if err != nil {
		return false, err
	}
	for _, source := range sources {
		relative := filepath.Clean(source.Relative)
		if relative == "." || filepath.IsAbs(relative) || relative == ".." ||
			strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			return false, errors.New("legacy source path is invalid")
		}
		info, statErr := os.Lstat(filepath.Join(options.SourceRoot, relative))
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return false, statErr
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return false, errors.New("legacy source is unsafe")
		}
		return true, nil
	}
	return false, nil
}

func validateCurrentStore(ctx context.Context, db *sql.DB, options Options) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if options.Validate != nil {
		if err := options.Validate(ctx, conn); err != nil {
			return fmt.Errorf("%w: validate %s schema: %v", ErrInvalidSchema, options.Component, err)
		}
	}
	if err := validateImportSchema(ctx, conn); err != nil {
		return fmt.Errorf("%w: validate %s import schema: %v", ErrInvalidSchema, options.Component, err)
	}
	return integrityCheckConn(ctx, conn, options.Component)
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

// EnsurePrivateDir creates path as a private directory and rejects a symlink
// at the database directory boundary.
func EnsurePrivateDir(path string) error { return sqliteprovider.EnsurePrivateDirectory(path) }

func sqliteDSN(path string, busyTimeout time.Duration) (string, error) {
	if path != ":memory:" {
		absolutePath, err := absoluteSQLitePath(path)
		if err != nil {
			return "", err
		}
		path = absolutePath
	}
	return sqliteprovider.DSN(path, busyTimeout)
}

func sqliteFilePath(slashPath, slashVolume string) string {
	return sqliteprovider.FileURLPath(slashPath, slashVolume)
}

func configure(
	ctx context.Context,
	db *sql.DB,
	busyTimeout time.Duration,
	memory bool,
	component string,
) error {
	if !memory && dblayer.MigrationFenceHeld() {
		if err := sqliteprovider.ConfigureOffline(ctx, db, busyTimeout); err != nil {
			return fmt.Errorf("configure %s offline SQLite provider: %w", component, err)
		}
		return nil
	}
	if err := sqliteprovider.Configure(ctx, db, busyTimeout, memory); err != nil {
		return fmt.Errorf("configure %s SQLite provider: %w", component, err)
	}
	return nil
}

func configureWAL(
	ctx context.Context,
	db *sql.DB,
	busyTimeout time.Duration,
	journal *string,
) error {
	selected, err := sqliteprovider.EnableWAL(ctx, db, busyTimeout)
	*journal = selected
	return err
}

func sqliteBusyOrLocked(err error) bool {
	return sqliteprovider.IsBusyOrLocked(err)
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
		current, err := sqliteprovider.SchemaVersion(ctx, conn)
		if err != nil {
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
			if err := sqliteprovider.SetSchemaVersion(ctx, conn, migration.Version); err != nil {
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
	begin := "BEGIN IMMEDIATE"
	if dblayer.MigrationFenceHeld() {
		begin = "BEGIN EXCLUSIVE"
	}
	if _, err = conn.ExecContext(ctx, begin); err != nil {
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
	if err := sqliteprovider.CheckIntegrityOnly(ctx, queryer); err != nil {
		return fmt.Errorf("%w: check %s database: %v", ErrIntegrity, component, err)
	}
	return nil
}

func foreignKeyCheckQuery(ctx context.Context, queryer contextQueryer, component string) error {
	if err := sqliteprovider.CheckForeignKeys(ctx, queryer); err != nil {
		return fmt.Errorf("%w: check %s database foreign keys: %v", ErrIntegrity, component, err)
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
