package databaseclaims

import (
	"context"
	"errors"
	"sync"

	"github.com/sipeed/picoclaw/internal/databaseproviderlease"
	"github.com/sipeed/picoclaw/pkg/database"
)

var errProviderGuardReleased = errors.New("physical database migration guard released")

type providerLeaseChild struct {
	state      *migrationRefreshingGuardState
	lease      *databaseproviderlease.Lease
	revokeOnce sync.Once
	revokeErr  error
}

// NewProviderLease derives one finite, one-consumer provider capability from
// the exact target retained by a live migration guard. At most one child may
// be live and no unresolved replacement pin may cross into another child.
// drain revokes admission first and then waits for every admitted provider
// callback and hook; guard release performs the same drain as a fail-safe.
func (guard *MigrationRefreshingGuard) NewProviderLease(
	parent context.Context,
	id database.StoreID,
) (
	*databaseproviderlease.Lease,
	func(context.Context, error) error,
	error,
) {
	if guard == nil || guard.state == nil || !id.Valid() {
		return nil, nil, database.NewError(
			database.CodeInvalid,
			"physical database provider lease input is invalid",
		)
	}
	state := guard.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.checkLocked(); err != nil {
		return nil, nil, err
	}
	if state.providerChild != nil {
		return nil, nil, database.NewError(
			database.CodeConflict,
			"physical database provider lease is already active",
		)
	}
	if len(state.pinned) != 0 {
		return nil, nil, database.NewError(
			database.CodeConflict,
			"physical database migration has an unresolved replacement pin",
		)
	}
	index, claimed := state.lease.byID[id]
	if !claimed {
		return nil, nil, database.NewError(
			database.CodeInvalid,
			"physical database provider lease target is not claimed",
		)
	}
	if index < 0 || index >= len(state.stores) || state.stores[index].ID != id {
		return nil, nil, state.lease.poison(database.NewError(
			database.CodeIntegrity,
			"physical database provider lease target is unavailable",
		))
	}
	target := state.stores[index].Path
	lease, err := databaseproviderlease.New(
		parent,
		id,
		target,
		databaseproviderlease.Hooks{
			Check: func(ctx context.Context) error {
				return providerLeaseGuardHook(ctx, guard.Check)
			},
			Reconcile: func(ctx context.Context) error {
				return providerLeaseGuardHook(ctx, guard.Reconcile)
			},
			PinReplacement: func(ctx context.Context, path string) error {
				return providerLeaseGuardHook(ctx, func() error {
					return guard.PinReplacement(id, path)
				})
			},
			CheckReplacement: func(ctx context.Context, path string) error {
				return providerLeaseGuardHook(ctx, func() error {
					return guard.CheckReplacement(id, path)
				})
			},
			DiscardReplacement: func(ctx context.Context) error {
				return providerLeaseGuardHook(ctx, func() error {
					return guard.DiscardReplacement(id)
				})
			},
			ReconcileReplacement: func(ctx context.Context) error {
				return providerLeaseGuardHook(ctx, func() error {
					return guard.ReconcileReplacement(id)
				})
			},
		},
	)
	if err != nil {
		return nil, nil, err
	}
	child := &providerLeaseChild{state: state, lease: lease}
	state.providerChild = child
	drain := child.drain
	return lease, drain, nil
}

func (child *providerLeaseChild) drain(waitCtx context.Context, cause error) error {
	if child == nil || child.lease == nil {
		return database.NewError(
			database.CodeUnavailable,
			"physical database provider child is unavailable",
		)
	}
	child.revokeOnce.Do(func() {
		child.revokeErr = databaseproviderlease.Revoke(child.lease, cause)
	})
	waitErr := databaseproviderlease.Wait(waitCtx, child.lease)
	if waitErr == nil && child.state != nil {
		child.state.mu.Lock()
		if child.state.providerChild == child {
			child.state.providerChild = nil
		}
		child.state.mu.Unlock()
	}
	return errors.Join(child.revokeErr, waitErr)
}

func providerLeaseGuardHook(ctx context.Context, hook func() error) error {
	if ctx == nil || hook == nil {
		return database.NewError(
			database.CodeInvalid,
			"physical database provider lease hook is invalid",
		)
	}
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	err := hook()
	if cause := context.Cause(ctx); cause != nil && !errors.Is(err, cause) {
		return errors.Join(err, cause)
	}
	return err
}
