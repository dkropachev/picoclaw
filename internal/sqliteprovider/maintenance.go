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
// changes themselves are supplied by broker-side domain adapters; this provider
// operation recovers and validates the generation, establishes exclusive
// rollback-journal fencing, restores WAL, checkpoints, and reopens it.
type MaintenanceResult struct {
	BeforeVersion int
	AfterVersion  int
}

// IsMaintenanceIntegrity reports whether err was produced by an integrity or
// foreign-key check. The migration layer maps this provider detail to its
// backend-neutral maintenance error.
func IsMaintenanceIntegrity(err error) bool { return errors.Is(err, errMaintenanceIntegrity) }

// MaintainOffline performs provider-owned maintenance over a storage root that
// the caller has already fenced and backed up. It never removes WAL/SHM files.
func MaintainOffline(
	ctx context.Context,
	path string,
	busyTimeout time.Duration,
) (MaintenanceResult, error) {
	var result MaintenanceResult
	before, err := inspectAndRecover(ctx, path, busyTimeout)
	if err != nil {
		return result, err
	}
	result.BeforeVersion = before
	if boundaryErr := exclusiveRollbackBoundary(ctx, path, busyTimeout); boundaryErr != nil {
		return result, boundaryErr
	}
	if checkpointErr := checkpointGeneration(ctx, path, busyTimeout); checkpointErr != nil {
		return result, checkpointErr
	}
	after, err := reopenAndValidate(ctx, path, busyTimeout)
	if err != nil {
		return result, err
	}
	result.AfterVersion = after
	if before != after {
		return result, errors.New("SQLite schema version changed without a committed domain migration")
	}
	return result, nil
}

func inspectAndRecover(ctx context.Context, path string, busyTimeout time.Duration) (int, error) {
	database, err := openMaintenanceStore(path, busyTimeout)
	if err != nil {
		return 0, err
	}
	defer database.Close()
	if err := database.PingContext(ctx); err != nil {
		if maintenanceCorruption(err) {
			return 0, fmt.Errorf("%w: %v", errMaintenanceIntegrity, err)
		}
		return 0, fmt.Errorf("recover SQLite generation: %w", err)
	}
	if err := maintenanceIntegrity(ctx, database); err != nil {
		return 0, err
	}
	var version int
	if err := database.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
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
	database, err := openMaintenanceStore(path, busyTimeout)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, database.Close()) }()
	connection, err := database.Conn(ctx)
	if err != nil {
		return err
	}
	defer connection.Close()

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
	// Broker-owned domain adapters execute schema and legacy-import commands
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
	database, err := openMaintenanceStore(path, busyTimeout)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, database.Close()) }()
	if err := database.PingContext(ctx); err != nil {
		return err
	}
	if err := maintenanceCheckpoint(ctx, database, "TRUNCATE"); err != nil {
		return fmt.Errorf("checkpoint migrated SQLite generation: %w", err)
	}
	return SecureGeneration(path)
}

func reopenAndValidate(ctx context.Context, path string, busyTimeout time.Duration) (int, error) {
	database, err := openMaintenanceStore(path, busyTimeout)
	if err != nil {
		return 0, err
	}
	defer database.Close()
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
	var version int
	if err := database.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return 0, err
	}
	return version, SecureGeneration(path)
}

func openMaintenanceStore(path string, busyTimeout time.Duration) (*sql.DB, error) {
	database, err := OpenStore(path, busyTimeout)
	if err != nil {
		if maintenanceCorruption(err) {
			return nil, fmt.Errorf("%w: %v", errMaintenanceIntegrity, err)
		}
		return nil, fmt.Errorf("open SQLite maintenance provider: %w", err)
	}
	if err := Configure(context.Background(), database, busyTimeout, false); err != nil {
		_ = database.Close()
		if maintenanceCorruption(err) {
			return nil, fmt.Errorf("%w: %v", errMaintenanceIntegrity, err)
		}
		return nil, fmt.Errorf("configure SQLite maintenance provider: %w", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	database.SetConnMaxLifetime(0)
	return database, nil
}

func maintenanceIntegrity(ctx context.Context, queryer controlQueryer) error {
	var result string
	if err := queryer.QueryRowContext(ctx, "PRAGMA integrity_check(1)").Scan(&result); err != nil {
		return fmt.Errorf("%w: %v", errMaintenanceIntegrity, err)
	}
	if result != "ok" {
		return fmt.Errorf("%w: SQLite reported %q", errMaintenanceIntegrity, result)
	}
	rows, err := queryer.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("%w: %v", errMaintenanceIntegrity, err)
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("%w: foreign-key violation reported", errMaintenanceIntegrity)
	}
	if err := rows.Err(); err != nil {
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
