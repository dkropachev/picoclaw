package sqliteprovider

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	moderncsqlite "modernc.org/sqlite"

	dblayer "github.com/sipeed/picoclaw/pkg/database"
)

// StagedMigration applies independently-committing third-party schema
// upgrades to a disposable provider generation. The live name is changed only
// after the staged database is closed, versioned, and integrity checked.
type StagedMigration func(context.Context, string) error

var (
	stagedCutoverDirectorySync = syncStagedMigrationDirectory
	stagedGenerationActivation = activateInstalledGeneration
)

// MigrateStagedOffline atomically installs a fully validated staged generation.
// The caller must already hold PicoClaw's exclusive migration fence and must
// have completed the mandatory outer backup. SQLite, rather than filesystem
// sidecar manipulation, snapshots and normalizes the source generation.
func MigrateStagedOffline(
	ctx context.Context,
	path string,
	busyTimeout time.Duration,
	expectedVersion int,
	migrate StagedMigration,
) (returnErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(path) == "" || path == ":memory:" ||
		strings.ContainsRune(path, 0) || expectedVersion <= 0 || migrate == nil {
		return errors.New("SQLite staged migration is invalid")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	absolute, absoluteErr := filepath.Abs(filepath.Clean(path))
	if absoluteErr != nil {
		return absoluteErr
	}
	if err := EnsurePrivateDirectory(filepath.Dir(absolute)); err != nil {
		return fmt.Errorf("prepare SQLite staged migration directory: %w", err)
	}
	if err := validateGenerationMembers(absolute, false); err != nil {
		return err
	}

	stage, err := unusedStagedGenerationPath(absolute)
	if err != nil {
		return err
	}
	installed := false
	defer func() {
		if !installed {
			returnErr = errors.Join(returnErr, discardStagedGeneration(stage, busyTimeout))
		}
	}()

	sourceExists, err := regularGenerationExists(absolute)
	if err != nil {
		return err
	}
	if sourceExists {
		if err := backupGenerationToStage(ctx, absolute, stage, busyTimeout); err != nil {
			return fmt.Errorf("snapshot SQLite migration stage: %w", err)
		}
	}
	if err := migrate(ctx, stage); err != nil {
		return fmt.Errorf("apply staged SQLite migration: %w", err)
	}
	if err := validateStagedGeneration(ctx, stage, busyTimeout, expectedVersion); err != nil {
		return fmt.Errorf("validate staged SQLite migration: %w", err)
	}
	if sourceExists {
		if err := normalizeOriginalForCutover(ctx, absolute, busyTimeout); err != nil {
			return fmt.Errorf("normalize SQLite cutover source: %w", err)
		}
	}
	if err := requireNoGenerationSidecars(absolute); err != nil {
		return err
	}
	if err := requireNoGenerationSidecars(stage); err != nil {
		return err
	}
	cutoverComplete, cutoverErr := replaceStagedGeneration(stage, absolute)
	installed = cutoverComplete
	if cutoverErr != nil {
		if cutoverComplete {
			return dblayer.NewError(
				dblayer.CodeOutcomeUnknown,
				"staged database replacement completed but durability could not be confirmed",
			)
		}
		return fmt.Errorf("install staged SQLite generation: %w", cutoverErr)
	}
	if err := stagedGenerationActivation(ctx, absolute, busyTimeout, expectedVersion); err != nil {
		return dblayer.NewError(
			dblayer.CodeOutcomeUnknown,
			"installed database generation could not be revalidated",
		)
	}
	return nil
}

type onlineBackuper interface {
	NewBackup(destination string) (*moderncsqlite.Backup, error)
}

func backupGenerationToStage(
	ctx context.Context,
	source string,
	stage string,
	busyTimeout time.Duration,
) (returnErr error) {
	database, openErr := OpenStore(source, busyTimeout)
	if openErr != nil {
		return openErr
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	defer func() { returnErr = errors.Join(returnErr, database.Close()) }()
	if err := database.PingContext(ctx); err != nil {
		return err
	}
	if err := maintenanceIntegrity(ctx, database); err != nil {
		return err
	}
	destination, err := DSN(stage, busyTimeout)
	if err != nil {
		return err
	}
	connection, err := database.Conn(ctx)
	if err != nil {
		return err
	}
	defer connection.Close()
	return connection.Raw(func(driverConnection any) error {
		backuper, ok := driverConnection.(onlineBackuper)
		if !ok {
			return errors.New("SQLite online backup is unavailable")
		}
		backup, backupErr := backuper.NewBackup(destination)
		if backupErr != nil {
			return backupErr
		}
		finished := false
		defer func() {
			if !finished {
				_ = backup.Finish()
			}
		}()
		more := true
		for more {
			if contextErr := ctx.Err(); contextErr != nil {
				return contextErr
			}
			more, err = backup.Step(256)
			if err != nil {
				return err
			}
		}
		finished = true
		return backup.Finish()
	})
}

func validateStagedGeneration(
	ctx context.Context,
	stage string,
	busyTimeout time.Duration,
	expectedVersion int,
) (returnErr error) {
	database, err := openOfflineStage(ctx, stage, busyTimeout)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, database.Close()) }()
	if err := maintenanceIntegrity(ctx, database); err != nil {
		return err
	}
	var version int
	if err := database.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version != expectedVersion {
		return fmt.Errorf("staged schema version is %d, want %d", version, expectedVersion)
	}
	return nil
}

func normalizeOriginalForCutover(
	ctx context.Context,
	path string,
	busyTimeout time.Duration,
) (returnErr error) {
	database, err := openOfflineStage(ctx, path, busyTimeout)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, database.Close()) }()
	return maintenanceIntegrity(ctx, database)
}

func openOfflineStage(ctx context.Context, path string, busyTimeout time.Duration) (*sql.DB, error) {
	database, err := OpenStore(path, busyTimeout)
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if err := ConfigureOffline(ctx, database, busyTimeout); err != nil {
		_ = database.Close()
		return nil, err
	}
	return database, nil
}

func activateInstalledGeneration(
	ctx context.Context,
	path string,
	busyTimeout time.Duration,
	expectedVersion int,
) error {
	if err := checkpointGeneration(ctx, path, busyTimeout); err != nil {
		return err
	}
	version, err := reopenAndValidate(ctx, path, busyTimeout)
	if err != nil {
		return err
	}
	if version != expectedVersion {
		return fmt.Errorf("installed schema version is %d, want %d", version, expectedVersion)
	}
	return nil
}

func unusedStagedGenerationPath(path string) (string, error) {
	for range 128 {
		random := make([]byte, 16)
		if _, err := rand.Read(random); err != nil {
			return "", err
		}
		stage := filepath.Join(
			filepath.Dir(path),
			"."+filepath.Base(path)+".migration-stage-"+hex.EncodeToString(random)+".db",
		)
		if _, err := os.Lstat(stage); errors.Is(err, os.ErrNotExist) {
			return stage, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", errors.New("SQLite staged migration filename space is exhausted")
}

func regularGenerationExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("SQLite staged migration source is unsafe")
	}
	return true, nil
}

func requireNoGenerationSidecars(path string) error {
	for _, sidecar := range []string{path + "-wal", path + "-shm", path + "-journal"} {
		if _, err := os.Lstat(sidecar); err == nil {
			return errors.New("SQLite staged migration has an active sidecar")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func discardStagedGeneration(path string, busyTimeout time.Duration) error {
	exists, err := regularGenerationExists(path)
	if err != nil || !exists {
		return err
	}
	database, openErr := openOfflineStage(context.Background(), path, busyTimeout)
	if openErr != nil {
		// The disposable generation is deliberately retained when its state cannot
		// be established; returning the open failure would obscure the migration
		// result that triggered best-effort cleanup.
		return nil //nolint:nilerr
	}
	if integrityErr := maintenanceIntegrity(context.Background(), database); integrityErr != nil {
		_ = database.Close()
		// A corrupt stage is evidence for diagnosing a failed third-party migration.
		return nil //nolint:nilerr
	}
	closeErr := database.Close()
	if sidecarErr := requireNoGenerationSidecars(path); sidecarErr != nil {
		return errors.Join(closeErr, sidecarErr)
	}
	if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return errors.Join(closeErr, removeErr)
	}
	return errors.Join(closeErr, syncStagedMigrationDirectory(filepath.Dir(path)))
}
