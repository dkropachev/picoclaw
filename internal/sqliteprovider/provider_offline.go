package sqliteprovider

import (
	"context"
	"errors"
	"time"

	"github.com/sipeed/picoclaw/internal/databaseproviderlease"
	dblayer "github.com/sipeed/picoclaw/pkg/database"
)

type immutableGenerationUse func(
	ctx context.Context,
	use func(sourceCtx context.Context, sourcePath string) error,
) error

// ImmutableGenerationSource exposes one StoreID-bound, descriptor-pinned
// generation only during its synchronous callback. The provider copies source
// bytes during that scope and never opens or mutates them with SQLite.
type ImmutableGenerationSource struct {
	storeID dblayer.StoreID
	use     immutableGenerationUse
}

// NewImmutableGenerationSource binds a caller-verified StoreID to one
// synchronous source callback. The binding cannot be modified after creation;
// an architecture guard reserves minting for the manifest-validating backup
// bridge.
func NewImmutableGenerationSource(
	storeID dblayer.StoreID,
	use func(context.Context, func(context.Context, string) error) error,
) (ImmutableGenerationSource, error) {
	if !storeID.Valid() || use == nil {
		return ImmutableGenerationSource{}, dblayer.NewError(
			dblayer.CodeInvalid,
			"immutable SQLite generation source is invalid",
		)
	}
	return ImmutableGenerationSource{storeID: storeID, use: use}, nil
}

// offlineProviderAuthority is the target-bound claims authority admitted for
// one synchronous provider operation. Exported method names intentionally let
// databaseproviderlease.Access satisfy the interface without an adapter that
// could accidentally widen or retain its scope.
type offlineProviderAuthority interface {
	Check(ctx context.Context) error
	Reconcile(ctx context.Context) error
	PinReplacement(ctx context.Context, path string) error
	CheckReplacement(ctx context.Context, path string) error
	DiscardReplacement(ctx context.Context) error
	ReconcileReplacement(ctx context.Context) error
}

// MigrateStagedOfflineFrom copies a sealed source into a disposable provider
// stage, migrates and validates that stage, and installs it at the exact target
// carried by lease. No caller-controlled live target or unbound source path is
// accepted.
func MigrateStagedOfflineFrom(
	ctx context.Context,
	lease *databaseproviderlease.Lease,
	source ImmutableGenerationSource,
	busyTimeout time.Duration,
	expectedVersion int,
	migrate StagedMigration,
	validate StagedValidation,
) (result MaintenanceResult, returnErr error) {
	return migrateStagedOfflineFromWithConsumer(
		ctx,
		lease,
		source,
		busyTimeout,
		expectedVersion,
		migrate,
		validate,
		databaseproviderlease.Consume,
	)
}

// MigrateStagedOfflineFromWithLiveVerification is MigrateStagedOfflineFrom
// with one final callback after the exact validated stage is claims-pinned and
// before it can replace the live target. The callback receives no selectable
// path; it must consume the opaque replacement capability synchronously.
func MigrateStagedOfflineFromWithLiveVerification(
	ctx context.Context,
	lease *databaseproviderlease.Lease,
	source ImmutableGenerationSource,
	busyTimeout time.Duration,
	expectedVersion int,
	migrate StagedMigration,
	validate StagedValidation,
	liveVerification StagedLiveVerification,
) (result MaintenanceResult, returnErr error) {
	if liveVerification == nil {
		return result, dblayer.NewError(
			dblayer.CodeInvalid,
			"SQLite offline live verification callback is required",
		)
	}
	return migrateStagedOfflineFromWithConsumerAndLiveVerification(
		ctx,
		lease,
		source,
		busyTimeout,
		expectedVersion,
		migrate,
		validate,
		liveVerification,
		databaseproviderlease.Consume,
	)
}

type offlineProviderLeaseConsumer func(
	context.Context,
	*databaseproviderlease.Lease,
	func(context.Context, databaseproviderlease.Access) error,
) error

func migrateStagedOfflineFromWithConsumer(
	ctx context.Context,
	lease *databaseproviderlease.Lease,
	source ImmutableGenerationSource,
	busyTimeout time.Duration,
	expectedVersion int,
	migrate StagedMigration,
	validate StagedValidation,
	consume offlineProviderLeaseConsumer,
) (result MaintenanceResult, returnErr error) {
	return migrateStagedOfflineFromWithConsumerAndLiveVerification(
		ctx, lease, source, busyTimeout, expectedVersion, migrate, validate, nil, consume,
	)
}

func migrateStagedOfflineFromWithConsumerAndLiveVerification(
	ctx context.Context,
	lease *databaseproviderlease.Lease,
	source ImmutableGenerationSource,
	busyTimeout time.Duration,
	expectedVersion int,
	migrate StagedMigration,
	validate StagedValidation,
	liveVerification StagedLiveVerification,
	consume offlineProviderLeaseConsumer,
) (result MaintenanceResult, returnErr error) {
	if ctx == nil || consume == nil || !source.storeID.Valid() || source.use == nil ||
		!validBusyTimeout(busyTimeout) || expectedVersion <= 0 ||
		int64(expectedVersion) > maxSQLiteSchemaVersion ||
		migrate == nil || validate == nil {
		return result, dblayer.NewError(
			dblayer.CodeInvalid,
			"SQLite offline provider input is invalid",
		)
	}
	returnErr = consume(
		ctx,
		lease,
		func(providerCtx context.Context, access databaseproviderlease.Access) error {
			storeID, target, err := access.Target()
			if err != nil {
				return err
			}
			if source.storeID != storeID {
				return dblayer.NewError(
					dblayer.CodeIntegrity,
					"immutable SQLite generation source does not match its target",
				)
			}
			result, err = migrateStagedOfflineAuthorizedWithLiveVerification(
				providerCtx,
				source,
				target,
				busyTimeout,
				expectedVersion,
				migrate,
				validate,
				liveVerification,
				access,
				stagedMigrationOps{
					replace: replaceStagedGeneration, activate: activateInstalledGeneration,
				},
			)
			return err
		},
	)
	if !result.installed {
		returnErr = sanitizePreCutoverError(returnErr)
	} else if returnErr != nil && dblayer.CodeOf(returnErr) != dblayer.CodeOutcomeUnknown {
		returnErr = errors.Join(
			dblayer.NewError(
				dblayer.CodeOutcomeUnknown,
				"installed database generation completion is uncertain",
			),
			returnErr,
		)
	}
	return result, returnErr
}
