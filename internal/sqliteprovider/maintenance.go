//nolint:govet // Ordered maintenance phases intentionally use narrow error scopes.
package sqliteprovider

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	moderncsqlite "modernc.org/sqlite"
)

var errMaintenanceIntegrity = errors.New("SQLite maintenance integrity failure")

// MaintenanceResult describes an offline provider maintenance boundary. Schema
// changes themselves are supplied by registered domain adapters; this provider
// operation recovers and validates the generation, establishes exclusive
// rollback-journal fencing, restores WAL, checkpoints, and reopens it.
type MaintenanceResult struct {
	BeforeVersion int
	AfterVersion  int
	installed     bool
}

type maintenanceOps struct {
	inspect    func(context.Context, string, time.Duration) (int, error)
	boundary   func(context.Context, string, time.Duration) error
	checkpoint func(context.Context, string, time.Duration) error
	reopen     func(context.Context, string, time.Duration) (int, error)
}

type maintenanceOpenOps struct {
	open      func(string, time.Duration) (*sql.DB, error)
	configure func(context.Context, *sql.DB, time.Duration, bool) error
}

// IsMaintenanceIntegrity reports whether err was produced by an integrity or
// foreign-key check. The migration layer maps this provider detail to its
// backend-neutral maintenance error.
func IsMaintenanceIntegrity(err error) bool { return errors.Is(err, errMaintenanceIntegrity) }

func maintainOffline(
	ctx context.Context,
	path string,
	busyTimeout time.Duration,
	ops maintenanceOps,
) (MaintenanceResult, error) {
	var result MaintenanceResult
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := validateProviderInput(path, busyTimeout); err != nil || path == ":memory:" {
		return result, errors.Join(errors.New("SQLite maintenance input is invalid"), err)
	}
	before, err := ops.inspect(ctx, path, busyTimeout)
	if err != nil {
		return result, err
	}
	result.BeforeVersion = before
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if boundaryErr := ops.boundary(ctx, path, busyTimeout); boundaryErr != nil {
		return result, boundaryErr
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if checkpointErr := ops.checkpoint(ctx, path, busyTimeout); checkpointErr != nil {
		return result, checkpointErr
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	after, err := ops.reopen(ctx, path, busyTimeout)
	if err != nil {
		return result, err
	}
	result.AfterVersion = after
	if before != after {
		return result, errors.New("SQLite schema version changed without a committed domain migration")
	}
	return result, nil
}

func inspectAndRecover(
	ctx context.Context,
	path string,
	busyTimeout time.Duration,
) (_ int, returnErr error) {
	ctx, err := checkedMaintenanceContext(ctx)
	if err != nil {
		return 0, err
	}
	database, err := openMaintenanceStore(ctx, path, busyTimeout)
	if err != nil {
		return 0, err
	}
	defer func() { returnErr = errors.Join(returnErr, database.Close()) }()
	return inspectAndRecoverDatabase(ctx, database)
}

func inspectAndRecoverDatabase(ctx context.Context, database *sql.DB) (int, error) {
	if err := database.PingContext(ctx); err != nil {
		if maintenanceCorruption(err) {
			return 0, fmt.Errorf("%w: %v", errMaintenanceIntegrity, err)
		}
		return 0, fmt.Errorf("recover SQLite generation: %w", err)
	}
	if err := maintenanceIntegrity(ctx, database); err != nil {
		return 0, err
	}
	version, err := SchemaVersion(ctx, database)
	if err != nil {
		return 0, fmt.Errorf("read SQLite schema version: %w", err)
	}
	if err := maintenanceCheckpoint(ctx, database, "FULL"); err != nil {
		return 0, fmt.Errorf("recover SQLite WAL: %w", err)
	}
	return version, nil
}

func exclusiveRollbackBoundary(
	ctx context.Context,
	path string,
	busyTimeout time.Duration,
) (returnErr error) {
	ctx, err := checkedMaintenanceContext(ctx)
	if err != nil {
		return err
	}
	database, err := openMaintenanceStore(ctx, path, busyTimeout)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, database.Close()) }()
	return exclusiveRollbackBoundaryDatabase(ctx, database, path)
}

func exclusiveRollbackBoundaryDatabase(
	ctx context.Context,
	database *sql.DB,
	path string,
) (returnErr error) {
	connection, err := database.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, connection.Close()) }()

	var lockingMode string
	if err := connection.QueryRowContext(ctx, "PRAGMA locking_mode = EXCLUSIVE").Scan(&lockingMode); err != nil {
		return fmt.Errorf("enable exclusive SQLite locking: %w", err)
	}
	if !strings.EqualFold(lockingMode, "exclusive") {
		return fmt.Errorf("enable exclusive SQLite locking: selected %q", lockingMode)
	}
	var journalMode string
	if err := connection.QueryRowContext(ctx, "PRAGMA journal_mode = DELETE").Scan(&journalMode); err != nil {
		return fmt.Errorf("enable SQLite rollback journal: %w", err)
	}
	if !strings.EqualFold(journalMode, "delete") {
		return fmt.Errorf("enable SQLite rollback journal: selected %q", journalMode)
	}
	if _, err := connection.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		return fmt.Errorf("begin exclusive SQLite migration: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	// Domain adapters execute schema and legacy-import commands
	// inside this boundary. For an already-current store the boundary validates
	// and commits without changing application rows.
	if err := maintenanceIntegrity(ctx, connection); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit SQLite migration: %w", err)
	}
	committed = true
	if err := connection.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&journalMode); err != nil {
		return fmt.Errorf("restore SQLite WAL: %w", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		return fmt.Errorf("restore SQLite WAL: selected %q", journalMode)
	}
	if err := connection.QueryRowContext(ctx, "PRAGMA locking_mode = NORMAL").Scan(&lockingMode); err != nil {
		return fmt.Errorf("restore normal SQLite locking: %w", err)
	}
	if !strings.EqualFold(lockingMode, "normal") {
		return fmt.Errorf("restore normal SQLite locking: selected %q", lockingMode)
	}
	return SecureGeneration(path)
}

func checkpointGeneration(ctx context.Context, path string, busyTimeout time.Duration) (returnErr error) {
	ctx, err := checkedMaintenanceContext(ctx)
	if err != nil {
		return err
	}
	database, err := openMaintenanceStore(ctx, path, busyTimeout)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, database.Close()) }()
	return checkpointGenerationDatabase(ctx, database, path)
}

func checkpointGenerationDatabase(ctx context.Context, database *sql.DB, path string) error {
	if err := database.PingContext(ctx); err != nil {
		return err
	}
	if err := maintenanceCheckpoint(ctx, database, "TRUNCATE"); err != nil {
		return fmt.Errorf("checkpoint migrated SQLite generation: %w", err)
	}
	return SecureGeneration(path)
}

func reopenAndValidate(
	ctx context.Context,
	path string,
	busyTimeout time.Duration,
) (_ int, returnErr error) {
	ctx, err := checkedMaintenanceContext(ctx)
	if err != nil {
		return 0, err
	}
	database, err := openMaintenanceStore(ctx, path, busyTimeout)
	if err != nil {
		return 0, err
	}
	defer func() { returnErr = errors.Join(returnErr, database.Close()) }()
	return reopenAndValidateDatabase(ctx, database, path)
}

func reopenAndValidateDatabase(ctx context.Context, database *sql.DB, path string) (int, error) {
	if err := database.PingContext(ctx); err != nil {
		return 0, fmt.Errorf("reopen migrated SQLite generation: %w", err)
	}
	if err := maintenanceIntegrity(ctx, database); err != nil {
		return 0, err
	}
	var journal string
	if err := database.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil {
		return 0, err
	}
	if !strings.EqualFold(journal, "wal") {
		return 0, fmt.Errorf("reopened SQLite journal mode is %q", journal)
	}
	version, err := SchemaVersion(ctx, database)
	if err != nil {
		return 0, err
	}
	return version, SecureGeneration(path)
}

func openMaintenanceStore(
	ctx context.Context,
	path string,
	busyTimeout time.Duration,
) (*sql.DB, error) {
	return openMaintenanceStoreWithOps(ctx, path, busyTimeout, maintenanceOpenOps{
		open: OpenStore, configure: Configure,
	})
}

func openMaintenanceStoreWithOps(
	ctx context.Context,
	path string,
	busyTimeout time.Duration,
	ops maintenanceOpenOps,
) (*sql.DB, error) {
	ctx, err := checkedMaintenanceContext(ctx)
	if err != nil {
		return nil, err
	}
	if ops.open == nil || ops.configure == nil {
		return nil, errors.New("SQLite maintenance provider operations are unavailable")
	}
	database, err := ops.open(path, busyTimeout)
	if err != nil {
		if maintenanceCorruption(err) {
			return nil, fmt.Errorf("%w: %v", errMaintenanceIntegrity, err)
		}
		return nil, fmt.Errorf("open SQLite maintenance provider: %w", err)
	}
	if err := ops.configure(ctx, database, busyTimeout, false); err != nil {
		closeErr := database.Close()
		if maintenanceCorruption(err) {
			return nil, errors.Join(
				fmt.Errorf("%w: %v", errMaintenanceIntegrity, err),
				closeErr,
			)
		}
		return nil, errors.Join(
			fmt.Errorf("configure SQLite maintenance provider: %w", err),
			closeErr,
		)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	database.SetConnMaxLifetime(0)
	return database, nil
}

func checkedMaintenanceContext(ctx context.Context) (context.Context, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return ctx, nil
}

func maintenanceIntegrity(ctx context.Context, queryer controlQueryer) error {
	return maintenanceIntegrityWithClassifier(ctx, queryer, IsBusyOrLocked)
}

func maintenanceIntegrityWithClassifier(
	ctx context.Context,
	queryer controlQueryer,
	busyOrLocked func(error) bool,
) error {
	if err := CheckIntegrity(ctx, queryer); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if busyOrLocked != nil && busyOrLocked(err) {
			return err
		}
		return fmt.Errorf("%w: %v", errMaintenanceIntegrity, err)
	}
	return nil
}

func maintenanceCheckpoint(ctx context.Context, queryer controlQueryer, mode string) error {
	var busy, logFrames, checkpointed int
	query := "PRAGMA wal_checkpoint(" + mode + ")"
	if err := queryer.QueryRowContext(ctx, query).Scan(&busy, &logFrames, &checkpointed); err != nil {
		return err
	}
	if busy != 0 {
		return errors.New("SQLite WAL checkpoint remained busy")
	}
	return nil
}

func maintenanceCorruption(err error) bool {
	var sqliteErr *moderncsqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	switch sqliteErr.Code() & 0xff {
	case 11, 26: // SQLITE_CORRUPT, SQLITE_NOTADB
		return true
	default:
		return false
	}
}
