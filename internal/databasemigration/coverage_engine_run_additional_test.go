package databasemigration

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/internal/databaseadapter"
	"github.com/sipeed/picoclaw/internal/databaseclaims"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestMigrationRunOrdersGuardStatusAndOwnershipRelease(t *testing.T) {
	engine, options := migrationRunFixture(t)
	ops := defaultMigrationRunOps()
	events := make([]string, 0, 20)
	record := func(event string) { events = append(events, event) }
	var activeGuard *databaseclaims.MigrationRefreshingGuard

	acquireFence := ops.acquireFence
	ops.acquireFence = func(home string) (*database.Fence, error) {
		record("acquire fence")
		return acquireFence(home)
	}
	releaseFence := ops.releaseFence
	ops.releaseFence = func(fence *database.Fence) error {
		record("release fence")
		return releaseFence(fence)
	}
	acquireLease := ops.acquireLease
	ops.acquireLease = func(
		catalog *storecatalog.Catalog,
		fence *database.Fence,
	) (*databaseclaims.Lease, error) {
		record("acquire lease")
		return acquireLease(catalog, fence)
	}
	releaseLease := ops.releaseLease
	ops.releaseLease = func(lease *databaseclaims.Lease) error {
		record("release lease")
		return releaseLease(lease)
	}
	acquireGuard := ops.acquireGuard
	ops.acquireGuard = func(
		lease *databaseclaims.Lease,
	) (*databaseclaims.MigrationRefreshingGuard, error) {
		record("acquire guard")
		var err error
		activeGuard, err = acquireGuard(lease)
		return activeGuard, err
	}
	releaseGuard := ops.releaseGuard
	ops.releaseGuard = func(guard *databaseclaims.MigrationRefreshingGuard) error {
		record("release guard")
		return releaseGuard(guard)
	}
	guardStores := ops.guardStores
	ops.guardStores = func(
		guard *databaseclaims.MigrationRefreshingGuard,
	) ([]storecatalog.Spec, error) {
		record("guard stores")
		return guardStores(guard)
	}
	snapshot := ops.snapshot
	ops.snapshot = func(
		engine *Engine,
		ctx context.Context,
		home string,
		all,
		selected []storecatalog.Spec,
		parent string,
	) (*backupSession, error) {
		record("snapshot")
		return snapshot(engine, ctx, home, all, selected, parent)
	}
	verifyBackup := ops.verifyBackup
	ops.verifyBackup = func(backup *backupSession, ctx context.Context) error {
		record("verify backup")
		return verifyBackup(backup, ctx)
	}
	finishBackup := ops.finishBackup
	ops.finishBackup = func(backup *backupSession, outcome string, cause error) error {
		record("status " + outcome)
		if activeGuard == nil {
			return errors.New("migration status lost its claims guard")
		}
		if guardErr := activeGuard.Check(); guardErr != nil {
			return errors.Join(errors.New("migration status guard is unavailable"), guardErr)
		}
		return finishBackup(backup, outcome, cause)
	}
	verifyLive := ops.verifyLive
	ops.verifyLive = func(
		backup *backupSession,
		ctx context.Context,
		spec storecatalog.Spec,
	) error {
		record("verify live")
		return verifyLive(backup, ctx, spec)
	}
	preflight := ops.preflight
	ops.preflight = func(
		engine *Engine,
		ctx context.Context,
		specs []storecatalog.Spec,
		backup *backupSession,
		results []StoreResult,
	) error {
		record("preflight")
		return preflight(engine, ctx, specs, backup, results)
	}
	migrate := ops.migrate
	ops.migrate = func(
		engine *Engine,
		ctx context.Context,
		fence *database.Fence,
		guard *databaseclaims.MigrationRefreshingGuard,
		spec storecatalog.Spec,
		adapter databaseadapter.Adapter,
		backup *backupSession,
		result *StoreResult,
	) error {
		record("migrate")
		return migrate(engine, ctx, fence, guard, spec, adapter, backup, result)
	}

	result, err := engine.runWithOps(t.Context(), options, ops)
	if err != nil {
		t.Fatal(err)
	}
	assertMigrationEventOrder(t, events, []string{
		"acquire fence",
		"acquire lease",
		"acquire guard",
		"guard stores",
		"snapshot",
		"verify backup",
		"verify live",
		"preflight",
		"status migration_in_progress",
		"migrate",
		"status complete",
		"release guard",
		"release lease",
		"release fence",
	})
	status := readAndVerifyManifest(t, result.BackupDir)
	if status.StatusRevision != 2 || status.Outcome != "complete" {
		t.Fatalf("migration status = %#v", status)
	}
}

func TestMigrationRunPreflightFailuresLeaveStatusAbsent(t *testing.T) {
	canary := errors.New("preflight boundary canary")
	for _, test := range []struct {
		name   string
		mutate func(*migrationRunOps)
	}{
		{
			name: "live verification",
			mutate: func(ops *migrationRunOps) {
				ops.verifyLive = func(
					*backupSession,
					context.Context,
					storecatalog.Spec,
				) error {
					return canary
				}
			},
		},
		{
			name: "adapter preflight",
			mutate: func(ops *migrationRunOps) {
				ops.preflight = func(
					*Engine,
					context.Context,
					[]storecatalog.Spec,
					*backupSession,
					[]StoreResult,
				) error {
					return canary
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			engine, options := migrationRunFixture(t)
			ops := defaultMigrationRunOps()
			test.mutate(&ops)
			result, err := engine.runWithOps(t.Context(), options, ops)
			if !errors.Is(err, canary) || result.BackupDir == "" {
				t.Fatalf("preflight failure = %#v, %v", result, err)
			}
			loaded, loadErr := loadBackupSession(t.Context(), result.BackupDir)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			status, exists, statusErr := loaded.readMigrationStatus()
			if statusErr != nil || exists {
				t.Fatalf("preflight status = %#v, %t, %v", status, exists, statusErr)
			}
		})
	}
}

func TestMigrationRunDryRunStopsBeforeMutablePhases(t *testing.T) {
	engine, options := migrationRunFixture(t)
	options.DryRun = true
	ops := defaultMigrationRunOps()
	calls := make(map[string]int)
	ops.verifyLive = func(*backupSession, context.Context, storecatalog.Spec) error {
		calls["verify live"]++
		return nil
	}
	ops.preflight = func(
		*Engine,
		context.Context,
		[]storecatalog.Spec,
		*backupSession,
		[]StoreResult,
	) error {
		calls["preflight"]++
		return nil
	}
	ops.migrate = func(
		*Engine,
		context.Context,
		*database.Fence,
		*databaseclaims.MigrationRefreshingGuard,
		storecatalog.Spec,
		databaseadapter.Adapter,
		*backupSession,
		*StoreResult,
	) error {
		calls["migrate"]++
		return nil
	}
	result, err := engine.runWithOps(t.Context(), options, ops)
	if err != nil {
		t.Fatal(err)
	}
	if calls["verify live"] != 0 || calls["preflight"] != 0 || calls["migrate"] != 0 {
		t.Fatalf("dry-run mutable calls = %#v", calls)
	}
	status := readAndVerifyManifest(t, result.BackupDir)
	if status.StatusRevision != 2 || status.Outcome != "dry_run" {
		t.Fatalf("dry-run status = %#v", status)
	}
}

func TestMigrationRunClassifiesTerminalOutcomes(t *testing.T) {
	canary := errors.New("migration outcome canary")
	tests := []struct {
		name     string
		migrate  func(*StoreResult) error
		outcome  string
		wantCode database.ErrorCode
		wantErr  bool
	}{
		{
			name:    "complete",
			migrate: func(result *StoreResult) error { result.cutover = true; return nil },
			outcome: "complete",
		},
		{
			name:    "known pre-cutover failure",
			migrate: func(*StoreResult) error { return canary },
			outcome: "failed",
			wantErr: true,
		},
		{
			name: "outcome unknown",
			migrate: func(*StoreResult) error {
				return database.NewError(database.CodeOutcomeUnknown, "provider uncertainty")
			},
			outcome:  "outcome_unknown",
			wantCode: database.CodeOutcomeUnknown,
			wantErr:  true,
		},
		{
			name: "committed cleanup failure",
			migrate: func(result *StoreResult) error {
				result.cutover = true
				return errors.Join(errPostMigrationCleanup, canary)
			},
			outcome: "complete_with_cleanup_error",
			wantErr: true,
		},
		{
			name: "panic",
			migrate: func(*StoreResult) error {
				panic("untrusted panic detail")
			},
			outcome: "failed",
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			engine, options := migrationRunFixture(t)
			ops := defaultMigrationRunOps()
			ops.preflight = func(
				_ *Engine,
				_ context.Context,
				_ []storecatalog.Spec,
				_ *backupSession,
				results []StoreResult,
			) error {
				results[0].providerRequired = true
				return nil
			}
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
				return test.migrate(result)
			}
			result, err := engine.runWithOps(t.Context(), options, ops)
			if (err != nil) != test.wantErr {
				t.Fatalf("run error = %v, want error=%t", err, test.wantErr)
			}
			if test.wantCode != "" && database.CodeOf(err) != test.wantCode {
				t.Fatalf("run error code = %s, want %s: %v", database.CodeOf(err), test.wantCode, err)
			}
			if test.name == "known pre-cutover failure" && !errors.Is(err, canary) {
				t.Fatalf("known failure lost cause: %v", err)
			}
			if test.name == "panic" && (!strings.Contains(err.Error(), "panicked") ||
				strings.Contains(err.Error(), "untrusted panic detail")) {
				t.Fatalf("panic result = %v", err)
			}
			status := readAndVerifyManifest(t, result.BackupDir)
			if status.StatusRevision != 2 || status.Outcome != test.outcome {
				t.Fatalf("terminal status = %#v, want %q", status, test.outcome)
			}
		})
	}
}

func TestMigrationRunAcquisitionAndTerminalStatusFaults(t *testing.T) {
	canary := errors.New("migration lifecycle canary")
	t.Run("invalid operations", func(t *testing.T) {
		engine, options := migrationRunFixture(t)
		if result, err := engine.runWithOps(t.Context(), options, migrationRunOps{}); err == nil ||
			result.BackupDir != "" {
			t.Fatalf("invalid operations = %#v, %v", result, err)
		}
	})
	for _, test := range []struct {
		name   string
		mutate func(*migrationRunOps)
	}{
		{
			name: "fence conflict",
			mutate: func(ops *migrationRunOps) {
				ops.acquireFence = func(string) (*database.Fence, error) {
					return nil, database.NewError(database.CodeConflict, "busy")
				}
			},
		},
		{
			name: "lease conflict",
			mutate: func(ops *migrationRunOps) {
				ops.acquireLease = func(
					*storecatalog.Catalog,
					*database.Fence,
				) (*databaseclaims.Lease, error) {
					return nil, database.NewError(database.CodeConflict, "busy")
				}
			},
		},
		{
			name: "guard conflict",
			mutate: func(ops *migrationRunOps) {
				ops.acquireGuard = func(
					*databaseclaims.Lease,
				) (*databaseclaims.MigrationRefreshingGuard, error) {
					return nil, database.NewError(database.CodeConflict, "busy")
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			engine, options := migrationRunFixture(t)
			ops := defaultMigrationRunOps()
			test.mutate(&ops)
			result, err := engine.runWithOps(t.Context(), options, ops)
			if !errors.Is(err, ErrStorageActive) || result.BackupDir != "" {
				t.Fatalf("acquisition conflict = %#v, %v", result, err)
			}
		})
	}

	t.Run("terminal status", func(t *testing.T) {
		engine, options := migrationRunFixture(t)
		ops := defaultMigrationRunOps()
		finish := ops.finishBackup
		ops.finishBackup = func(backup *backupSession, outcome string, cause error) error {
			if outcome != "migration_in_progress" {
				return canary
			}
			return finish(backup, outcome, cause)
		}
		result, err := engine.runWithOps(t.Context(), options, ops)
		if !errors.Is(err, canary) || result.BackupDir == "" {
			t.Fatalf("terminal status fault = %#v, %v", result, err)
		}
		loaded, loadErr := loadBackupSession(t.Context(), result.BackupDir)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		status, exists, statusErr := loaded.readMigrationStatus()
		if statusErr != nil || !exists || status.Revision != 1 ||
			status.Outcome != "migration_in_progress" {
			t.Fatalf("retained status = %#v, %t, %v", status, exists, statusErr)
		}
	})
}

func migrationRunFixture(t *testing.T) (*Engine, Options) {
	t.Helper()
	home := migrationHome(t)
	registry := migrationRegistry(t, databaseadapter.Adapter{
		Domain: "auth",
		Contract: databaseadapter.Contract{
			CurrentVersion: 1,
			EmptyPolicy:    databaseadapter.EmptyInitializeOnline,
		},
	})
	return migrationEngine(t, home, registry), Options{
		Stores: []database.StoreID{"global/auth"},
	}
}

func assertMigrationEventOrder(t *testing.T, got, want []string) {
	t.Helper()
	position := 0
	for _, event := range got {
		if position < len(want) && event == want[position] {
			position++
		}
	}
	if position != len(want) {
		t.Fatalf("migration events = %#v, missing ordered suffix %#v", got, want[position:])
	}
	if slices.Index(got, "status complete") > slices.Index(got, "release guard") {
		t.Fatalf("terminal status was published after guard release: %#v", got)
	}
}
