package databasemigration

import (
	"context"
	"errors"
	"testing"

	"github.com/sipeed/picoclaw/internal/databaseadapter"
	"github.com/sipeed/picoclaw/internal/databaseclaims"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestMigrationRunAcceptsNilContext(t *testing.T) {
	engine, options := migrationRunFixture(t)
	options.DryRun = true
	result, err := engine.runWithOps(nil, options, defaultMigrationRunOps())
	if err != nil || !result.DryRun || result.BackupDir == "" {
		t.Fatalf("nil-context dry run = %#v, %v", result, err)
	}
}

func TestMigrationRunRemainingAcquisitionAndPhaseFailures(t *testing.T) {
	canary := errors.New("migration run closeout canary")
	tests := []struct {
		name   string
		mutate func(*testing.T, context.CancelFunc, *migrationRunOps)
		dryRun bool
		want   error
	}{
		{
			name: "fence acquisition",
			mutate: func(_ *testing.T, _ context.CancelFunc, ops *migrationRunOps) {
				ops.acquireFence = func(string) (*database.Fence, error) { return nil, canary }
			},
		},
		{
			name: "guard acquisition",
			mutate: func(_ *testing.T, _ context.CancelFunc, ops *migrationRunOps) {
				ops.acquireGuard = func(
					*databaseclaims.Lease,
				) (*databaseclaims.MigrationRefreshingGuard, error) {
					return nil, canary
				}
			},
		},
		{
			name: "guard stores",
			mutate: func(_ *testing.T, _ context.CancelFunc, ops *migrationRunOps) {
				ops.guardStores = func(
					*databaseclaims.MigrationRefreshingGuard,
				) ([]storecatalog.Spec, error) {
					return nil, canary
				}
			},
		},
		{
			name: "initial guard check",
			mutate: func(_ *testing.T, _ context.CancelFunc, ops *migrationRunOps) {
				ops.guardCheck = func(*databaseclaims.MigrationRefreshingGuard) error { return canary }
			},
		},
		{
			name: "backup verification",
			mutate: func(_ *testing.T, _ context.CancelFunc, ops *migrationRunOps) {
				ops.verifyBackup = func(*backupSession, context.Context) error { return canary }
			},
		},
		{
			name: "backup inventory",
			want: nil,
			mutate: func(_ *testing.T, _ context.CancelFunc, ops *migrationRunOps) {
				originalSnapshot := ops.snapshot
				ops.snapshot = func(
					engine *Engine,
					ctx context.Context,
					home string,
					all []storecatalog.Spec,
					selected []storecatalog.Spec,
					parent string,
				) (*backupSession, error) {
					backup, err := originalSnapshot(engine, ctx, home, all, selected, parent)
					if backup != nil {
						backup.manifest.Stores = nil
					}
					return backup, err
				}
				ops.verifyBackup = func(*backupSession, context.Context) error { return nil }
			},
		},
		{
			name:   "dry-run status start",
			dryRun: true,
			mutate: func(_ *testing.T, _ context.CancelFunc, ops *migrationRunOps) {
				ops.finishBackup = func(*backupSession, string, error) error { return canary }
			},
		},
		{
			name: "pre-live guard check",
			mutate: func(_ *testing.T, _ context.CancelFunc, ops *migrationRunOps) {
				failMigrationGuardCheck(ops, 2, canary)
			},
		},
		{
			name: "canceled after preflight",
			mutate: func(_ *testing.T, cancel context.CancelFunc, ops *migrationRunOps) {
				ops.preflight = func(
					*Engine,
					context.Context,
					[]storecatalog.Spec,
					*backupSession,
					[]StoreResult,
				) error {
					cancel()
					return nil
				}
			},
		},
		{
			name: "adapter lookup",
			want: ErrAdapterRequired,
			mutate: func(_ *testing.T, _ context.CancelFunc, ops *migrationRunOps) {
				ops.lookup = func(
					*databaseadapter.Registry,
					string,
				) (databaseadapter.Adapter, bool) {
					return databaseadapter.Adapter{}, false
				}
			},
		},
		{
			name: "pre-status guard check",
			mutate: func(_ *testing.T, _ context.CancelFunc, ops *migrationRunOps) {
				failMigrationGuardCheck(ops, 3, canary)
			},
		},
		{
			name: "migration status start",
			mutate: func(_ *testing.T, _ context.CancelFunc, ops *migrationRunOps) {
				ops.finishBackup = func(*backupSession, string, error) error { return canary }
			},
		},
		{
			name: "post-provider guard check",
			mutate: func(_ *testing.T, _ context.CancelFunc, ops *migrationRunOps) {
				failMigrationGuardCheck(ops, 4, canary)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			engine, options := migrationRunFixture(t)
			options.DryRun = test.dryRun
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ops := defaultMigrationRunOps()
			test.mutate(t, cancel, &ops)
			result, err := engine.runWithOps(ctx, options, ops)
			if err == nil {
				t.Fatalf("run phase %q unexpectedly succeeded: %#v", test.name, result)
			}
			if test.name == "canceled after preflight" && !errors.Is(err, context.Canceled) {
				t.Fatalf("post-preflight cancellation = %v", err)
			}
			want := test.want
			if want == nil && test.name != "backup inventory" && test.name != "canceled after preflight" {
				want = canary
			}
			if want != nil && !errors.Is(err, want) {
				t.Fatalf("run phase %q error = %v, want %v", test.name, err, want)
			}
		})
	}
}

func TestMigrationRunRemainingCleanupAndUncertainOutcomes(t *testing.T) {
	canary := errors.New("migration cleanup closeout canary")
	tests := []struct {
		name     string
		mutate   func(*migrationRunOps)
		wantCode database.ErrorCode
	}{
		{
			name: "known error after marked cutover",
			mutate: func(ops *migrationRunOps) {
				ops.preflight = migrationProviderRequiredPreflight
				ops.migrate = func(
					_ *Engine,
					_ context.Context,
					_ *database.Fence,
					_ *databaseclaims.MigrationRefreshingGuard,
					_ storecatalog.Spec,
					_ databaseadapter.Adapter,
					_ *backupSession,
					result *StoreResult,
				) error {
					result.cutover = true
					return canary
				}
			},
			wantCode: database.CodeOutcomeUnknown,
		},
		{
			name: "terminal status after cutover",
			mutate: func(ops *migrationRunOps) {
				ops.preflight = migrationProviderRequiredPreflight
				ops.migrate = migrationMarkCutover
				finish := ops.finishBackup
				ops.finishBackup = func(backup *backupSession, outcome string, cause error) error {
					if outcome == "complete" {
						return canary
					}
					return finish(backup, outcome, cause)
				}
			},
			wantCode: database.CodeOutcomeUnknown,
		},
		{
			name: "guard release before cutover",
			mutate: func(ops *migrationRunOps) {
				wrapMigrationGuardReleaseError(ops, canary)
			},
		},
		{
			name: "guard release after cutover",
			mutate: func(ops *migrationRunOps) {
				ops.preflight = migrationProviderRequiredPreflight
				ops.migrate = migrationMarkCutover
				wrapMigrationGuardReleaseError(ops, canary)
			},
			wantCode: database.CodeOutcomeUnknown,
		},
		{
			name: "lease release",
			mutate: func(ops *migrationRunOps) {
				original := ops.releaseLease
				ops.releaseLease = func(lease *databaseclaims.Lease) error {
					return errors.Join(original(lease), canary)
				}
			},
		},
		{
			name: "fence release",
			mutate: func(ops *migrationRunOps) {
				original := ops.releaseFence
				ops.releaseFence = func(fence *database.Fence) error {
					return errors.Join(original(fence), canary)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			engine, options := migrationRunFixture(t)
			ops := defaultMigrationRunOps()
			test.mutate(&ops)
			result, err := engine.runWithOps(t.Context(), options, ops)
			if err == nil || !errors.Is(err, canary) {
				t.Fatalf("cleanup outcome %q = %#v, %v", test.name, result, err)
			}
			if test.wantCode != "" && database.CodeOf(err) != test.wantCode {
				t.Fatalf(
					"cleanup outcome %q code = %s, want %s: %v",
					test.name,
					database.CodeOf(err),
					test.wantCode,
					err,
				)
			}
			if test.name == "guard release after cutover" {
				status := readAndVerifyManifest(t, result.BackupDir)
				if status.Outcome != "complete" || status.StatusRevision != 2 {
					t.Fatalf("immutable pre-release terminal status = %#v", status)
				}
			}
		})
	}
}

func failMigrationGuardCheck(ops *migrationRunOps, wanted int, failure error) {
	original := ops.guardCheck
	calls := 0
	ops.guardCheck = func(guard *databaseclaims.MigrationRefreshingGuard) error {
		calls++
		if calls == wanted {
			return failure
		}
		return original(guard)
	}
}

func migrationProviderRequiredPreflight(
	_ *Engine,
	_ context.Context,
	_ []storecatalog.Spec,
	_ *backupSession,
	results []StoreResult,
) error {
	results[0].providerRequired = true
	return nil
}

func migrationMarkCutover(
	_ *Engine,
	_ context.Context,
	_ *database.Fence,
	_ *databaseclaims.MigrationRefreshingGuard,
	_ storecatalog.Spec,
	_ databaseadapter.Adapter,
	_ *backupSession,
	result *StoreResult,
) error {
	result.cutover = true
	return nil
}

func wrapMigrationGuardReleaseError(ops *migrationRunOps, failure error) {
	original := ops.releaseGuard
	ops.releaseGuard = func(guard *databaseclaims.MigrationRefreshingGuard) error {
		return errors.Join(original(guard), failure)
	}
}
