//nolint:govet // Ordered cutover phases intentionally use narrow error scopes.
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

var errStagedReplacementRemainsPinned = errors.New(
	"SQLite staged replacement remains pinned",
)

const maximumStagedCleanupDuration = time.Second

type stagedReplacementPinnedError struct{ cause error }

func (err *stagedReplacementPinnedError) Error() string {
	return "SQLite staged operation failed before replacement and its stage remains pinned"
}

func (err *stagedReplacementPinnedError) Is(target error) bool {
	return target == errStagedReplacementRemainsPinned || errors.Is(err.cause, target)
}

type stagedPreCutoverError struct{ cause error }

func (err *stagedPreCutoverError) Error() string {
	return "SQLite staged operation failed before replacement"
}

func (err *stagedPreCutoverError) Is(target error) bool {
	return errors.Is(err.cause, target)
}

// StagedMigration applies independently-committing third-party schema
// upgrades to a disposable provider generation. The live name is changed only
// after the staged database is closed, versioned, and integrity checked.
type StagedMigration func(context.Context, string) error

// StagedValidation proves the complete domain contract on the disposable
// generation before its live name can be replaced.
type StagedValidation func(context.Context, string) error

type stagedMigrationOps struct {
	replace    func(string, string) (bool, error)
	activate   func(context.Context, string, time.Duration, int) error
	discard    func(string, time.Duration) error
	copySource func(
		context.Context,
		ImmutableGenerationSource,
		string,
	) (bool, *retainedStagedGeneration, error)
}

type stagedMigrationFilesystemOps struct {
	exists     func(string) (bool, error)
	lstat      func(string) (os.FileInfo, error)
	unused     func(string) (string, error)
	prepare    func(string) error
	retain     func(context.Context, string) (*retainedStagedGeneration, error)
	backup     func(context.Context, string, string, time.Duration) error
	validate   func(context.Context, string, time.Duration, int) error
	same       func(string, os.FileInfo) (bool, error)
	noSidecars func(string) error
}

func systemStagedMigrationFilesystemOps() stagedMigrationFilesystemOps {
	return stagedMigrationFilesystemOps{
		exists:     regularGenerationExists,
		lstat:      os.Lstat,
		unused:     unusedStagedGenerationPath,
		prepare:    PrepareStore,
		retain:     retainStagedGeneration,
		backup:     backupGenerationToStage,
		validate:   validateStagedGeneration,
		same:       sameRegularGeneration,
		noSidecars: requireNoGenerationSidecars,
	}
}

func migrateStagedOfflineAuthorized(
	ctx context.Context,
	source ImmutableGenerationSource,
	target string,
	busyTimeout time.Duration,
	expectedVersion int,
	migrate StagedMigration,
	validate StagedValidation,
	authority offlineProviderAuthority,
	ops stagedMigrationOps,
) (result MaintenanceResult, returnErr error) {
	return migrateStagedOfflineAuthorizedWithLiveVerification(
		ctx, source, target, busyTimeout, expectedVersion, migrate, validate, nil, authority, ops,
	)
}

func migrateStagedOfflineAuthorizedWithLiveVerification(
	ctx context.Context,
	source ImmutableGenerationSource,
	target string,
	busyTimeout time.Duration,
	expectedVersion int,
	migrate StagedMigration,
	validate StagedValidation,
	liveVerification StagedLiveVerification,
	authority offlineProviderAuthority,
	ops stagedMigrationOps,
) (result MaintenanceResult, returnErr error) {
	targetReplaced := false
	defer func() {
		if !targetReplaced {
			returnErr = sanitizePreCutoverError(returnErr)
		}
	}()
	if ctx == nil || !source.storeID.Valid() || source.use == nil || authority == nil ||
		expectedVersion <= 0 || int64(expectedVersion) > maxSQLiteSchemaVersion ||
		migrate == nil || validate == nil ||
		ops.replace == nil || ops.activate == nil {
		return result, errors.New("SQLite staged migration is invalid")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := validateProviderInput(target, busyTimeout); err != nil || target == ":memory:" {
		return result, errors.Join(errors.New("SQLite staged migration input is invalid"), err)
	}
	absoluteTarget, err := filepath.Abs(filepath.Clean(target))
	if err != nil {
		return result, err
	}
	if err := authority.Check(ctx); err != nil {
		return result, err
	}
	targetParentPath := filepath.Dir(absoluteTarget)
	targetParent, err := prepareRetainedStagedTargetParent(ctx, targetParentPath)
	if err != nil {
		return result, fmt.Errorf("prepare SQLite staged migration directory: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, targetParent.Close()) }()
	working, err := unusedStagedGenerationPath(absoluteTarget)
	if err != nil {
		return result, err
	}
	discardWorking := ops.discard
	identityManageWorking := discardWorking == nil
	if discardWorking == nil {
		discardWorking = discardStagedGeneration
	}
	var retainedWorking *retainedStagedGeneration
	workingRetentionAttempted := false
	workingValidated := false
	defer func() {
		var cleanupErr error
		if identityManageWorking && workingRetentionAttempted {
			cleanupErr = cleanupRetainedStagedGeneration(
				retainedWorking,
				working,
				requireNoGenerationSidecars,
				busyTimeout,
				workingValidated,
			)
		} else {
			cleanupErr = discardWorking(working, busyTimeout)
		}
		if cleanupErr == nil {
			return
		}
		if targetReplaced {
			returnErr = errors.Join(
				returnErr,
				dblayer.NewError(
					dblayer.CodeOutcomeUnknown,
					"installed database generation left provider working state",
				),
				cleanupErr,
			)
			return
		}
		returnErr = errors.Join(returnErr, cleanupErr)
	}()
	if identityManageWorking {
		workingRetentionAttempted = true
	}
	copySource := ops.copySource
	if copySource == nil {
		copySource = copyImmutableGenerationToRetainedStage
	}
	sourceExists, copiedWorking, err := copySource(
		ctx,
		immutableSourceOutsideLiveTarget(source, absoluteTarget),
		working,
	)
	if err != nil {
		return result, fmt.Errorf("copy immutable SQLite migration source: %w", err)
	}
	if copiedWorking != nil && !identityManageWorking {
		if err := copiedWorking.Close(); err != nil {
			return result, fmt.Errorf("release SQLite migration source retention: %w", err)
		}
		copiedWorking = nil
	}
	if sourceExists {
		if identityManageWorking {
			retainedWorking = copiedWorking
			if retainedWorking == nil {
				return result, errors.New(
					"SQLite migration source retention is unavailable after copy",
				)
			}
		}
		result, err = maintainOfflineAuthorized(
			ctx,
			working,
			busyTimeout,
			authority,
			maintenanceOps{
				inspect: inspectAndRecover, boundary: exclusiveRollbackBoundary,
				checkpoint: checkpointGeneration, reopen: reopenAndValidate,
			},
		)
		if err != nil {
			return result, fmt.Errorf("normalize SQLite migration source: %w", err)
		}
		if result.BeforeVersion > expectedVersion {
			return result, fmt.Errorf(
				"SQLite migration source schema version is %d, newer than expected %d",
				result.BeforeVersion,
				expectedVersion,
			)
		}
		if retainedWorking != nil {
			if err := retainedWorking.Check(ctx, working); err != nil {
				return result, fmt.Errorf(
					"recheck retained SQLite migration source after normalization: %w",
					err,
				)
			}
		}
		workingValidated = true
	}
	if err := authority.Check(ctx); err != nil {
		return result, err
	}
	if retainedWorking != nil {
		if err := retainedWorking.Check(ctx, working); err != nil {
			return result, fmt.Errorf(
				"recheck retained SQLite migration source before staging: %w",
				err,
			)
		}
	}

	wrappedOps := stagedMigrationOps{
		replace: func(stage, live string) (complete bool, returnErr error) {
			if err := validateTargetDerivedReplacementPath(live, stage); err != nil {
				return false, err
			}
			seal, err := sealValidatedReplacementStage(ctx, stage)
			if err != nil {
				return false, err
			}
			defer func() { returnErr = errors.Join(returnErr, seal.close()) }()
			if targetParent != nil {
				if _, err := targetParent.SealSoleStage(
					ctx,
					targetParentPath,
					stage,
					seal.identity,
					seal.handleIdentity,
				); err != nil {
					return false, err
				}
			}
			targetExists, targetIdentity, err := captureStagedTargetIdentity(live)
			if err != nil {
				return false, err
			}
			if err := authority.Check(ctx); err != nil {
				return false, err
			}
			if sourceExists {
				var retirementErr error
				if identityManageWorking {
					if retainedWorking == nil {
						retirementErr = errors.New(
							"normalized SQLite migration source retirement is unavailable",
						)
					} else if sidecarErr := requireNoGenerationSidecars(working); sidecarErr != nil {
						retirementErr = sidecarErr
					} else {
						retirementErr = retainedWorking.Retire(ctx)
					}
				} else {
					retirementErr = discardWorking(working, busyTimeout)
				}
				if retirementErr != nil {
					return false, fmt.Errorf(
						"retire normalized SQLite migration source: %w",
						retirementErr,
					)
				}
				available, err := stagedGenerationNamespaceAvailable(working)
				if err != nil || !available {
					return false, errors.Join(
						errors.New("normalized SQLite migration source was not retired"),
						err,
					)
				}
			}
			if err := authority.Check(ctx); err != nil {
				return false, err
			}
			if err := authority.PinReplacement(ctx, stage); err != nil {
				discardErr := authority.DiscardReplacement(ctx)
				if discardErr != nil {
					return false, errors.Join(
						err,
						errStagedReplacementRemainsPinned,
						discardErr,
					)
				}
				return false, err
			}
			discardPinned := func(cause error) (bool, error) {
				discardErr := authority.DiscardReplacement(ctx)
				if discardErr != nil {
					return false, errors.Join(
						cause,
						errStagedReplacementRemainsPinned,
						discardErr,
					)
				}
				return false, cause
			}
			if err := authority.Check(ctx); err != nil {
				return discardPinned(err)
			}
			if err := authority.CheckReplacement(ctx, stage); err != nil {
				return discardPinned(err)
			}
			if err := seal.verify(ctx); err != nil {
				return discardPinned(err)
			}
			if err := invokeStagedLiveVerificationWithTargetParent(
				ctx,
				source.storeID,
				live,
				seal,
				targetParent,
				liveVerification,
			); err != nil {
				return discardPinned(err)
			}
			if err := authority.Check(ctx); err != nil {
				return discardPinned(err)
			}
			if err := authority.CheckReplacement(ctx, stage); err != nil {
				return discardPinned(err)
			}
			if err := seal.verify(ctx); err != nil {
				return discardPinned(err)
			}
			if targetParent != nil {
				if _, err := targetParent.CheckSoleStage(
					ctx,
					targetParentPath,
					stage,
					seal.identity,
				); err != nil {
					return discardPinned(err)
				}
			}
			if err := recheckStagedTargetIdentity(live, targetExists, targetIdentity); err != nil {
				return discardPinned(err)
			}
			var replaceErr error
			if targetParent != nil {
				complete, replaceErr = targetParent.ReplaceStage(
					ctx,
					stage,
					live,
					seal.identity,
					seal.file,
				)
			} else {
				complete, replaceErr = ops.replace(stage, live)
			}
			if !complete {
				if replaceErr == nil {
					replaceErr = errors.New("SQLite staged replacement did not complete")
				}
				return discardPinned(replaceErr)
			}
			targetReplaced = true
			result.installed = true
			var parentErr error
			if targetParent != nil {
				_, parentErr = targetParent.CheckSoleInstalledTarget(ctx, live)
			}
			return true, errors.Join(
				replaceErr,
				parentErr,
				authority.ReconcileReplacement(ctx),
			)
		},
		activate: func(
			activateCtx context.Context,
			live string,
			timeout time.Duration,
			version int,
		) error {
			activateErr := ops.activate(activateCtx, live, timeout, version)
			reconcileErr := authority.Reconcile(activateCtx)
			var parentErr error
			if targetParent != nil {
				if _, err := targetParent.CheckSoleInstalledTarget(activateCtx, live); err != nil {
					parentErr = errors.Join(
						dblayer.NewError(
							dblayer.CodeOutcomeUnknown,
							"installed database target parent changed during activation",
						),
						err,
					)
				}
			}
			return errors.Join(activateErr, reconcileErr, parentErr)
		},
	}
	if err := migrateStagedOffline(
		ctx,
		working,
		absoluteTarget,
		busyTimeout,
		expectedVersion,
		migrate,
		validate,
		wrappedOps,
	); err != nil {
		return result, err
	}
	if err := authority.Check(ctx); err != nil {
		return result, errors.Join(
			dblayer.NewError(
				dblayer.CodeOutcomeUnknown,
				"installed database generation could not be revalidated",
			),
			err,
		)
	}
	if targetParent != nil {
		if _, err := targetParent.CheckSoleInstalledTarget(ctx, absoluteTarget); err != nil {
			return result, errors.Join(
				dblayer.NewError(
					dblayer.CodeOutcomeUnknown,
					"installed database target parent could not be revalidated",
				),
				err,
			)
		}
	}
	result.AfterVersion = expectedVersion
	return result, nil
}

func captureStagedTargetIdentity(path string) (bool, os.FileInfo, error) {
	exists, err := regularGenerationExists(path)
	if err != nil {
		return false, nil, err
	}
	if err := requireNoGenerationSidecars(path); err != nil {
		return false, nil, err
	}
	if !exists {
		return false, nil, nil
	}
	identity, err := os.Lstat(path)
	if err != nil || identity == nil || !identity.Mode().IsRegular() ||
		identity.Mode()&os.ModeSymlink != 0 {
		return false, nil, errors.Join(
			errors.New("SQLite migration target identity is unavailable"),
			err,
		)
	}
	return true, identity, nil
}

func recheckStagedTargetIdentity(
	path string,
	existed bool,
	expected os.FileInfo,
) error {
	if err := requireNoGenerationSidecars(path); err != nil {
		return err
	}
	if !existed {
		appeared, err := regularGenerationExists(path)
		if err != nil || appeared {
			return errors.Join(
				errors.New("SQLite migration target appeared during final verification"),
				err,
			)
		}
		return nil
	}
	current, err := os.Lstat(path)
	if err != nil || current == nil || expected == nil ||
		!current.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(expected, current) || current.Size() != expected.Size() ||
		current.Mode() != expected.Mode() || !current.ModTime().Equal(expected.ModTime()) {
		return errors.Join(
			errors.New("SQLite migration target changed during final verification"),
			err,
		)
	}
	return nil
}

func immutableSourceOutsideLiveTarget(
	source ImmutableGenerationSource,
	target string,
) ImmutableGenerationSource {
	return ImmutableGenerationSource{
		storeID: source.storeID,
		use: func(
			ctx context.Context,
			use func(context.Context, string) error,
		) error {
			return source.use(ctx, func(sourceCtx context.Context, path string) error {
				if generationNamespacesOverlap(path, target) {
					return errors.New("immutable SQLite migration source aliases the live target")
				}
				overlaps, err := generationNamespacesPhysicallyOverlap(path, target)
				if err != nil {
					return err
				}
				if overlaps {
					return errors.New("immutable SQLite migration source physically aliases the live target")
				}
				return use(sourceCtx, path)
			})
		},
	}
}

func generationNamespacesPhysicallyOverlap(left, right string) (bool, error) {
	leftMembers := [4]string{left, left + "-wal", left + "-shm", left + "-journal"}
	rightMembers := [4]string{right, right + "-wal", right + "-shm", right + "-journal"}
	for _, leftMember := range leftMembers {
		leftInfo, err := os.Lstat(leftMember)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("inspect immutable SQLite migration source identity: %w", err)
		}
		for _, rightMember := range rightMembers {
			rightInfo, err := os.Lstat(rightMember)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return false, fmt.Errorf("inspect live SQLite migration target identity: %w", err)
			}
			if leftInfo != nil && rightInfo != nil && os.SameFile(leftInfo, rightInfo) {
				return true, nil
			}
		}
	}
	return false, nil
}

func generationNamespacesOverlap(left, right string) bool {
	leftMembers := [4]string{left, left + "-wal", left + "-shm", left + "-journal"}
	rightMembers := [4]string{right, right + "-wal", right + "-shm", right + "-journal"}
	for _, leftMember := range leftMembers {
		leftKey, leftErr := inspectedPoolKey(leftMember)
		if leftErr != nil {
			return true
		}
		for _, rightMember := range rightMembers {
			rightKey, rightErr := inspectedPoolKey(rightMember)
			if rightErr != nil || leftKey == rightKey {
				return true
			}
		}
	}
	return false
}

func maintainOfflineAuthorized(
	ctx context.Context,
	path string,
	busyTimeout time.Duration,
	authority offlineProviderAuthority,
	ops maintenanceOps,
) (MaintenanceResult, error) {
	if authority == nil || ops.inspect == nil || ops.boundary == nil ||
		ops.checkpoint == nil || ops.reopen == nil {
		return MaintenanceResult{}, errors.New("SQLite maintenance operations are unavailable")
	}
	check := func() error { return authority.Check(ctx) }
	checked := maintenanceOps{
		inspect: func(
			phaseCtx context.Context,
			phasePath string,
			timeout time.Duration,
		) (int, error) {
			if err := check(); err != nil {
				return 0, err
			}
			return ops.inspect(phaseCtx, phasePath, timeout)
		},
		boundary: func(
			phaseCtx context.Context,
			phasePath string,
			timeout time.Duration,
		) error {
			if err := check(); err != nil {
				return err
			}
			return ops.boundary(phaseCtx, phasePath, timeout)
		},
		checkpoint: func(
			phaseCtx context.Context,
			phasePath string,
			timeout time.Duration,
		) error {
			if err := check(); err != nil {
				return err
			}
			return ops.checkpoint(phaseCtx, phasePath, timeout)
		},
		reopen: func(
			phaseCtx context.Context,
			phasePath string,
			timeout time.Duration,
		) (int, error) {
			if err := check(); err != nil {
				return 0, err
			}
			return ops.reopen(phaseCtx, phasePath, timeout)
		},
	}
	return maintainOffline(ctx, path, busyTimeout, checked)
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
) error {
	return migrateStagedOfflineWithFilesystem(
		ctx, source, target, busyTimeout, expectedVersion, migrate, validate, ops,
		systemStagedMigrationFilesystemOps(),
	)
}

func migrateStagedOfflineWithFilesystem(
	ctx context.Context,
	source string,
	target string,
	busyTimeout time.Duration,
	expectedVersion int,
	migrate StagedMigration,
	validate StagedValidation,
	ops stagedMigrationOps,
	filesystem stagedMigrationFilesystemOps,
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
		ops.replace == nil || ops.activate == nil ||
		(filesystem.prepare == nil) != (filesystem.retain == nil) {
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
	targetExists, err := filesystem.exists(absoluteTarget)
	if err != nil {
		return err
	}
	var targetIdentity os.FileInfo
	if targetExists {
		targetIdentity, err = filesystem.lstat(absoluteTarget)
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

	stage, err := filesystem.unused(absoluteTarget)
	if err != nil {
		return err
	}
	installed := false
	identityManagedStage := filesystem.retain != nil
	stageValidated := false
	var retainedStage *retainedStagedGeneration
	defer func() {
		if identityManagedStage {
			if retainedStage == nil {
				return
			}
			if installed || errors.Is(returnErr, errStagedReplacementRemainsPinned) {
				returnErr = errors.Join(returnErr, retainedStage.Close())
				return
			}
			returnErr = errors.Join(
				returnErr,
				cleanupRetainedStagedGeneration(
					retainedStage,
					stage,
					filesystem.noSidecars,
					busyTimeout,
					stageValidated,
				),
			)
			return
		}
		if !installed && !errors.Is(returnErr, errStagedReplacementRemainsPinned) {
			returnErr = errors.Join(returnErr, discardStagedGeneration(stage, busyTimeout))
		}
	}()

	sourceExists, err := filesystem.exists(absoluteSource)
	if err != nil {
		return err
	}
	if sourceExists != targetExists {
		return errors.New("SQLite staged migration source and target existence differ")
	}
	if sourceExists {
		if err := filesystem.backup(ctx, absoluteSource, stage, busyTimeout); err != nil {
			return fmt.Errorf("snapshot SQLite migration stage: %w", err)
		}
	} else if identityManagedStage {
		if err := filesystem.prepare(stage); err != nil {
			return fmt.Errorf("prepare empty SQLite migration stage: %w", err)
		}
	}
	if identityManagedStage {
		retainedStage, err = filesystem.retain(ctx, stage)
		if err != nil {
			return fmt.Errorf("retain SQLite migration stage: %w", err)
		}
	}
	if err := callStagedCallback(ctx, stage, migrate, "migration"); err != nil {
		return fmt.Errorf("apply staged SQLite migration: %w", sanitizePreCutoverError(err))
	}
	if retainedStage != nil {
		if err := retainedStage.Check(ctx, stage); err != nil {
			return fmt.Errorf("recheck retained SQLite stage after migration: %w", err)
		}
	}
	if err := filesystem.validate(ctx, stage, busyTimeout, expectedVersion); err != nil {
		return fmt.Errorf("validate staged SQLite migration: %w", err)
	}
	validatedStageIdentity, err := filesystem.lstat(stage)
	if err != nil || validatedStageIdentity == nil || !validatedStageIdentity.Mode().IsRegular() ||
		validatedStageIdentity.Mode()&os.ModeSymlink != 0 {
		return errors.Join(errors.New("validated SQLite stage identity is unavailable"), err)
	}
	if err := callStagedCallback(ctx, stage, validate, "validation"); err != nil {
		return fmt.Errorf("validate staged domain contract: %w", sanitizePreCutoverError(err))
	}
	if retainedStage != nil {
		if err := retainedStage.Check(ctx, stage); err != nil {
			return fmt.Errorf("recheck retained SQLite stage after domain validation: %w", err)
		}
	}
	unchanged, identityErr := filesystem.same(stage, validatedStageIdentity)
	if identityErr != nil || !unchanged {
		return errors.Join(
			errors.New("SQLite staged generation changed during domain validation"),
			identityErr,
		)
	}
	if err := filesystem.validate(ctx, stage, busyTimeout, expectedVersion); err != nil {
		return fmt.Errorf("revalidate staged SQLite migration: %w", err)
	}
	stageIdentity, err := filesystem.lstat(stage)
	if err != nil || stageIdentity == nil || !stageIdentity.Mode().IsRegular() ||
		stageIdentity.Mode()&os.ModeSymlink != 0 {
		return errors.Join(errors.New("SQLite staged generation identity is unavailable"), err)
	}
	if targetExists {
		unchanged, identityErr := filesystem.same(absoluteTarget, targetIdentity)
		if identityErr != nil || !unchanged {
			return errors.Join(errors.New("SQLite migration target changed before cutover"), identityErr)
		}
	} else if appeared, identityErr := filesystem.exists(absoluteTarget); identityErr != nil || appeared {
		return errors.Join(errors.New("SQLite migration target appeared before cutover"), identityErr)
	}
	if err := filesystem.noSidecars(absoluteTarget); err != nil {
		return err
	}
	if err := filesystem.noSidecars(stage); err != nil {
		return err
	}
	if unchanged, identityErr := filesystem.same(stage, stageIdentity); identityErr != nil || !unchanged {
		return errors.Join(errors.New("SQLite staged generation changed before cutover"), identityErr)
	}
	if retainedStage != nil {
		if err := retainedStage.Check(ctx, stage); err != nil {
			return fmt.Errorf("recheck retained SQLite stage before cutover: %w", err)
		}
	}
	stageValidated = true
	cutoverComplete, cutoverErr := ops.replace(stage, absoluteTarget)
	installed = cutoverComplete
	if cutoverComplete && retainedStage != nil {
		if checkErr := retainedStage.Check(ctx, absoluteTarget); checkErr != nil {
			return errors.Join(
				dblayer.NewError(
					dblayer.CodeOutcomeUnknown,
					"installed database generation does not match its retained stage",
				),
				cutoverErr,
				checkErr,
			)
		}
	}
	if cutoverErr != nil {
		if cutoverComplete {
			return errors.Join(
				dblayer.NewError(
					dblayer.CodeOutcomeUnknown,
					"staged database replacement completed but durability could not be confirmed",
				),
				cutoverErr,
			)
		}
		return fmt.Errorf(
			"install staged SQLite generation: %w",
			sanitizePreCutoverError(cutoverErr),
		)
	}
	if err := ops.activate(ctx, absoluteTarget, busyTimeout, expectedVersion); err != nil {
		return errors.Join(
			dblayer.NewError(
				dblayer.CodeOutcomeUnknown,
				"installed database generation could not be revalidated",
			),
			err,
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
	if errors.Is(err, errStagedReplacementRemainsPinned) {
		return &stagedReplacementPinnedError{cause: err}
	}
	return &stagedPreCutoverError{cause: err}
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
	Step(pages int32) (bool, error)
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
	defer func() { returnErr = errors.Join(returnErr, connection.Close()) }()
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
		return nil, errors.Join(err, database.Close())
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
		random := make([]byte, stagedReplacementRandomHexLength/2)
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

func cleanupRetainedStagedGeneration(
	retained *retainedStagedGeneration,
	path string,
	noSidecars func(string) error,
	busyTimeout time.Duration,
	validated bool,
) (returnErr error) {
	if retained == nil {
		return nil
	}
	defer func() { returnErr = errors.Join(returnErr, retained.Close()) }()
	if !validated {
		return errors.New("SQLite diagnostic migration stage was retained")
	}
	if noSidecars == nil {
		return errors.New("SQLite staged retirement sidecar validation is unavailable")
	}
	if err := noSidecars(path); err != nil {
		return errors.Join(errors.New("SQLite diagnostic migration stage was retained"), err)
	}
	cleanupTimeout := min(busyTimeout, maximumStagedCleanupDuration)
	cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	return retained.Retire(cleanupCtx)
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
	cleanupTimeout := min(busyTimeout, maximumStagedCleanupDuration)
	cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	present, err := exists(path)
	if err != nil || !present {
		return err
	}
	database, openErr := openStage(cleanupCtx, path, cleanupTimeout)
	if openErr != nil {
		// The disposable generation is deliberately retained when its state cannot
		// be established; returning the open failure would obscure the migration
		// result that triggered best-effort cleanup.
		return errors.Join(
			errors.New("SQLite diagnostic migration stage was retained"),
			openErr,
		)
	}
	if integrityErr := maintenanceIntegrity(cleanupCtx, database); integrityErr != nil {
		closeErr := database.Close()
		// A corrupt stage is evidence for diagnosing a failed third-party migration.
		return errors.Join(
			errors.New("SQLite diagnostic migration stage was retained"),
			integrityErr,
			closeErr,
		)
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
