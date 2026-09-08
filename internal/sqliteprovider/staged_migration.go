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
	"time"

	moderncsqlite "modernc.org/sqlite"

	dblayer "github.com/sipeed/picoclaw/pkg/database"
)

// StagedMigration applies independently-committing third-party schema
// upgrades to a disposable provider generation. The live name is changed only
// after the staged database is closed, versioned, and integrity checked.
type StagedMigration func(context.Context, string) error

// StagedValidation proves the complete domain contract on the disposable
// generation before its live name can be replaced.
type StagedValidation func(context.Context, string) error

type stagedMigrationOps struct {
	replace  func(string, string) (bool, error)
	activate func(context.Context, string, time.Duration, int) error
}

// MigrateStagedOfflineFrom builds a replacement from source while preserving
// target untouched until cutover. Offline migration passes a private generation
// reconstructed from its verified backup as source.
func MigrateStagedOfflineFrom(
	ctx context.Context,
	source string,
	target string,
	busyTimeout time.Duration,
	expectedVersion int,
	migrate StagedMigration,
	validate StagedValidation,
) (returnErr error) {
	if !dblayer.MigrationContextAuthorizes(ctx, target) {
		return errors.New("SQLite staged migration authority is unavailable")
	}
	sourceKey, sourceKeyErr := inspectedPoolKey(source)
	targetKey, targetKeyErr := inspectedPoolKey(target)
	if sourceKeyErr != nil || targetKeyErr != nil || sourceKey == targetKey {
		return errors.Join(
			errors.New("SQLite staged migration source must differ from target"),
			sourceKeyErr,
			targetKeyErr,
		)
	}
	return migrateStagedOffline(
		ctx,
		source,
		target,
		busyTimeout,
		expectedVersion,
		migrate,
		validate,
		stagedMigrationOps{
			replace:  replaceStagedGeneration,
			activate: activateInstalledGeneration,
		},
	)
}

func migrateStagedOffline(
	ctx context.Context,
	source string,
	target string,
	busyTimeout time.Duration,
	expectedVersion int,
	migrate StagedMigration,
	validate StagedValidation,
	ops stagedMigrationOps,
) (returnErr error) {
	if err := validateProviderInput(target, busyTimeout); err != nil || target == ":memory:" {
		return errors.Join(errors.New("SQLite staged migration input is invalid"), err)
	}
	if err := validateProviderInput(source, busyTimeout); err != nil || source == ":memory:" {
		return errors.Join(errors.New("SQLite staged migration source is invalid"), err)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if expectedVersion <= 0 || migrate == nil || validate == nil ||
		ops.replace == nil || ops.activate == nil {
		return errors.New("SQLite staged migration is invalid")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	absoluteTarget, absoluteErr := filepath.Abs(filepath.Clean(target))
	if absoluteErr != nil {
		return absoluteErr
	}
	absoluteSource, absoluteErr := filepath.Abs(filepath.Clean(source))
	if absoluteErr != nil {
		return absoluteErr
	}
	if err := EnsurePrivateDirectory(filepath.Dir(absoluteTarget)); err != nil {
		return fmt.Errorf("prepare SQLite staged migration directory: %w", err)
	}
	if err := validateGenerationMembers(absoluteTarget, false); err != nil {
		return err
	}
	// A clean target generation is required before any provider open. This keeps
	// every pre-replacement failure byte-preserving for the live generation;
	// recovery of an active generation belongs to provider maintenance first.
	if err := requireNoGenerationSidecars(absoluteTarget); err != nil {
		return err
	}
	targetExists, err := regularGenerationExists(absoluteTarget)
	if err != nil {
		return err
	}
	var targetIdentity os.FileInfo
	if targetExists {
		targetIdentity, err = os.Lstat(absoluteTarget)
		if err != nil {
			return err
		}
	}
	if err := validateProviderAncestors(absoluteSource); err != nil {
		return err
	}
	if err := validateGenerationMembers(absoluteSource, false); err != nil {
		return err
	}

	stage, err := unusedStagedGenerationPath(absoluteTarget)
	if err != nil {
		return err
	}
	installed := false
	defer func() {
		if !installed {
			returnErr = errors.Join(returnErr, discardStagedGeneration(stage, busyTimeout))
		}
	}()

	sourceExists, err := regularGenerationExists(absoluteSource)
	if err != nil {
		return err
	}
	if sourceExists != targetExists {
		return errors.New("SQLite staged migration source and target existence differ")
	}
	if sourceExists {
		if err := backupGenerationToStage(ctx, absoluteSource, stage, busyTimeout); err != nil {
			return fmt.Errorf("snapshot SQLite migration stage: %w", err)
		}
	}
	if err := callStagedCallback(ctx, stage, migrate, "migration"); err != nil {
		return fmt.Errorf("apply staged SQLite migration: %w", sanitizePreCutoverError(err))
	}
	if err := validateStagedGeneration(ctx, stage, busyTimeout, expectedVersion); err != nil {
		return fmt.Errorf("validate staged SQLite migration: %w", err)
	}
	if err := callStagedCallback(ctx, stage, validate, "validation"); err != nil {
		return fmt.Errorf("validate staged domain contract: %w", sanitizePreCutoverError(err))
	}
	stageIdentity, err := os.Lstat(stage)
	if err != nil || stageIdentity == nil || !stageIdentity.Mode().IsRegular() ||
		stageIdentity.Mode()&os.ModeSymlink != 0 {
		return errors.Join(errors.New("SQLite staged generation identity is unavailable"), err)
	}
	if targetExists {
		unchanged, identityErr := sameRegularGeneration(absoluteTarget, targetIdentity)
		if identityErr != nil || !unchanged {
			return errors.Join(errors.New("SQLite migration target changed before cutover"), identityErr)
		}
	} else if appeared, identityErr := regularGenerationExists(absoluteTarget); identityErr != nil || appeared {
		return errors.Join(errors.New("SQLite migration target appeared before cutover"), identityErr)
	}
	if err := requireNoGenerationSidecars(absoluteTarget); err != nil {
		return err
	}
	if err := requireNoGenerationSidecars(stage); err != nil {
		return err
	}
	if unchanged, identityErr := sameRegularGeneration(stage, stageIdentity); identityErr != nil || !unchanged {
		return errors.Join(errors.New("SQLite staged generation changed before cutover"), identityErr)
	}
	cutoverComplete, cutoverErr := ops.replace(stage, absoluteTarget)
	installed = cutoverComplete
	if cutoverErr != nil {
		if cutoverComplete {
			return dblayer.NewError(
				dblayer.CodeOutcomeUnknown,
				"staged database replacement completed but durability could not be confirmed",
			)
		}
		return fmt.Errorf(
			"install staged SQLite generation: %w",
			sanitizePreCutoverError(cutoverErr),
		)
	}
	if err := ops.activate(ctx, absoluteTarget, busyTimeout, expectedVersion); err != nil {
		return dblayer.NewError(
			dblayer.CodeOutcomeUnknown,
			"installed database generation could not be revalidated",
		)
	}
	return nil
}

func callStagedCallback(
	ctx context.Context,
	stage string,
	callback func(context.Context, string) error,
	kind string,
) (returnErr error) {
	defer func() {
		if recover() != nil {
			returnErr = errors.New("SQLite staged " + kind + " callback panicked")
		}
	}()
	return callback(ctx, stage)
}

func sanitizePreCutoverError(err error) error {
	if err == nil || dblayer.CodeOf(err) != dblayer.CodeOutcomeUnknown {
		return err
	}
	return errors.New("SQLite staged operation failed before replacement")
}

func sameRegularGeneration(path string, expected os.FileInfo) (bool, error) {
	current, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	if expected == nil || current == nil || !current.Mode().IsRegular() ||
		current.Mode()&os.ModeSymlink != 0 || !os.SameFile(expected, current) {
		return false, nil
	}
	return expected.Size() == current.Size() && expected.ModTime() == current.ModTime(), nil
}

type onlineBackuper interface {
	NewBackup(destination string) (*moderncsqlite.Backup, error)
}

type onlineBackupStepper interface {
	Step(int32) (bool, error)
	Finish() error
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
	if prepareErr := PrepareStore(stage); prepareErr != nil {
		return prepareErr
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
	return backupGenerationConnection(ctx, connection, destination)
}

func backupGenerationConnection(
	ctx context.Context,
	connection *sql.Conn,
	destination string,
) error {
	return connection.Raw(func(driverConnection any) error {
		backuper, ok := driverConnection.(onlineBackuper)
		if !ok {
			return errors.New("SQLite online backup is unavailable")
		}
		backup, backupErr := backuper.NewBackup(destination)
		if backupErr != nil {
			return backupErr
		}
		return stepOnlineBackup(ctx, backup)
	})
}

func stepOnlineBackup(ctx context.Context, backup onlineBackupStepper) (returnErr error) {
	if backup == nil {
		return errors.New("SQLite online backup is unavailable")
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
		var err error
		more, err = backup.Step(256)
		if err != nil {
			return err
		}
	}
	finished = true
	return backup.Finish()
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
	version, err := SchemaVersion(ctx, database)
	if err != nil {
		return err
	}
	if version != expectedVersion {
		return fmt.Errorf("staged schema version is %d, want %d", version, expectedVersion)
	}
	return nil
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
	return activateInstalledGenerationWithOps(
		ctx, path, busyTimeout, expectedVersion, checkpointGeneration, reopenAndValidate,
	)
}

func activateInstalledGenerationWithOps(
	ctx context.Context,
	path string,
	busyTimeout time.Duration,
	expectedVersion int,
	checkpoint func(context.Context, string, time.Duration) error,
	reopen func(context.Context, string, time.Duration) (int, error),
) error {
	if err := checkpoint(ctx, path, busyTimeout); err != nil {
		return err
	}
	version, err := reopen(ctx, path, busyTimeout)
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
		available, err := stagedGenerationNamespaceAvailable(stage)
		if err != nil {
			return "", err
		}
		if available {
			return stage, nil
		}
	}
	return "", errors.New("SQLite staged migration filename space is exhausted")
}

func stagedGenerationNamespaceAvailable(path string) (bool, error) {
	for _, member := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
		if _, err := os.Lstat(member); err == nil {
			return false, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	return true, nil
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
	return discardStagedGenerationWithOps(
		path,
		busyTimeout,
		regularGenerationExists,
		openOfflineStage,
		requireNoGenerationSidecars,
		os.Remove,
		syncStagedMigrationDirectory,
	)
}

func discardStagedGenerationWithOps(
	path string,
	busyTimeout time.Duration,
	exists func(string) (bool, error),
	openStage func(context.Context, string, time.Duration) (*sql.DB, error),
	noSidecars func(string) error,
	remove func(string) error,
	syncDirectory func(string) error,
) error {
	present, err := exists(path)
	if err != nil || !present {
		return err
	}
	database, openErr := openStage(context.Background(), path, busyTimeout)
	if openErr != nil {
		// The disposable generation is deliberately retained when its state cannot
		// be established; returning the open failure would obscure the migration
		// result that triggered best-effort cleanup.
		return fmt.Errorf("SQLite diagnostic migration stage retained at %s", path)
	}
	if integrityErr := maintenanceIntegrity(context.Background(), database); integrityErr != nil {
		_ = database.Close()
		// A corrupt stage is evidence for diagnosing a failed third-party migration.
		return fmt.Errorf("SQLite diagnostic migration stage retained at %s", path)
	}
	closeErr := database.Close()
	if sidecarErr := noSidecars(path); sidecarErr != nil {
		return errors.Join(closeErr, sidecarErr)
	}
	if removeErr := remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return errors.Join(closeErr, removeErr)
	}
	return errors.Join(closeErr, syncDirectory(filepath.Dir(path)))
}
