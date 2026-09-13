package databasemigration

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/databaseadapter"
	"github.com/sipeed/picoclaw/internal/databaseclaims"
	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestMigrationInventoryRemainingValidationFailures(t *testing.T) {
	auth := database.StoreID("global/auth")
	if err := populateMigrationStoreExistence(nil, nil, nil); err == nil {
		t.Fatal("existence projection accepted nil backup")
	}
	invalid := &backupSession{manifest: BackupManifest{Stores: []BackupStoreManifest{
		{StoreID: "bad id"},
	}}}
	if err := populateMigrationStoreExistence(invalid, nil, nil); err == nil {
		t.Fatal("existence projection accepted invalid stored ID")
	}
	duplicate := &backupSession{manifest: BackupManifest{Stores: []BackupStoreManifest{
		{StoreID: auth.String()},
		{StoreID: auth.String()},
	}}}
	if err := populateMigrationStoreExistence(duplicate, nil, nil); err == nil {
		t.Fatal("existence projection accepted duplicate stored ID")
	}
	if _, err := backupLegacyExists(nil, auth); err == nil {
		t.Fatal("legacy inventory accepted nil backup")
	}
}

func TestMigrationPreflightRemainingEarlyFailures(t *testing.T) {
	if err := (*Engine)(nil).preflight(t.Context(), nil, nil, nil); err == nil {
		t.Fatal("preflight accepted nil engine")
	}

	engine, _ := migrationRunFixture(t)
	spec, found := engine.catalog.Lookup("global/auth")
	if !found {
		t.Fatal("migration fixture omitted global/auth")
	}
	results := []StoreResult{{ID: spec.ID}}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := engine.preflight(canceled, []storecatalog.Spec{spec}, &backupSession{}, results); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("canceled preflight = %v", err)
	}

	emptyRegistry, err := databaseadapter.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	unregistered := &Engine{registry: emptyRegistry}
	if err := unregistered.preflight(
		t.Context(),
		[]storecatalog.Spec{spec},
		&backupSession{},
		results,
	); !errors.Is(err, ErrAdapterRequired) {
		t.Fatalf("unregistered preflight = %v", err)
	}

	if err := engine.preflight(
		t.Context(),
		[]storecatalog.Spec{spec},
		&backupSession{},
		results,
	); err == nil {
		t.Fatal("preflight accepted missing legacy inventory")
	}
	invalidPrepared := &backupSession{manifest: BackupManifest{Stores: []BackupStoreManifest{
		{
			StoreID:         spec.ID.String(),
			LegacyRoots:     append([]string(nil), spec.LegacyRoots...),
			LegacyRootKinds: make([]string, len(spec.LegacyRoots)),
		},
	}}}
	for index := range invalidPrepared.manifest.Stores[0].LegacyRootKinds {
		invalidPrepared.manifest.Stores[0].LegacyRootKinds[index] = "missing"
	}
	if err := engine.preflight(
		t.Context(),
		[]storecatalog.Spec{spec},
		invalidPrepared,
		results,
	); err == nil {
		t.Fatal("preflight accepted unavailable prepared generation")
	}
}

func TestMigrationPreflightPropagatesOperationalInspectionCancellation(t *testing.T) {
	home := migrationHome(t)
	spec := storecatalog.Spec{
		ID: "global/auth", Domain: "auth", Path: filepath.Join(home, "auth.db"),
	}
	backup, err := snapshotBackup(
		t.Context(), time.Now, home,
		[]storecatalog.Spec{spec}, []storecatalog.Spec{spec}, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	registry := migrationRegistry(t, databaseadapter.Adapter{
		Domain: spec.Domain,
		Contract: databaseadapter.Contract{
			CurrentVersion: 1, EmptyPolicy: databaseadapter.EmptyInitializeOnline,
		},
	})
	engine := &Engine{registry: registry}
	// The earlier calls are the outer preflight and prepared-generation seal.
	// Cancel on the first provider-inspection context check to prove that a
	// non-integrity operational error is propagated without reclassification.
	ctx := &cancelAfterMigrationErrChecks{
		Context: context.Background(), allowed: 19,
	}
	results := []StoreResult{{ID: spec.ID}}
	preflightErr := engine.preflight(
		ctx, []storecatalog.Spec{spec}, backup, results,
	)
	if !errors.Is(preflightErr, context.Canceled) {
		t.Fatalf("operational inspection cancellation = %v", preflightErr)
	}
}

func TestMigrationSnapshotStoreAndLimitCloseout(t *testing.T) {
	if backup, err := (*Engine)(nil).snapshot(t.Context(), "", nil, nil, ""); backup != nil || err == nil {
		t.Fatalf("nil engine snapshot = %#v, %v", backup, err)
	}
	tooMany := make([]storecatalog.Spec, maximumMigrationStores+1)
	if selected, err := selectStores(tooMany, nil); selected != nil || err == nil {
		t.Fatalf("all-store limit = %#v, %v", selected, err)
	}
	requested := make([]database.StoreID, maximumMigrationStores+1)
	if selected, err := selectStores(nil, requested); selected != nil || err == nil {
		t.Fatalf("requested-store limit = %#v, %v", selected, err)
	}

	home := migrationHome(t)
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	engine := &Engine{options: storecatalog.Options{Home: home}}
	dummyGuard := &databaseclaims.MigrationRefreshingGuard{}
	if err := engine.migrateStore(
		t.Context(),
		nil,
		dummyGuard,
		storecatalog.Spec{},
		databaseadapter.Adapter{},
		&backupSession{},
		&StoreResult{},
	); err == nil {
		t.Fatal("migrateStore accepted nil fence")
	}
	if err := engine.migrateStore(
		t.Context(),
		fence,
		dummyGuard,
		storecatalog.Spec{ID: "global/auth", Path: filepath.Join(home, "auth.db")},
		databaseadapter.Adapter{},
		&backupSession{},
		&StoreResult{providerRequired: true},
	); err == nil {
		t.Fatal("migrateStore accepted unavailable immutable backup")
	}
	cleanupCanary := errors.New("prepared cleanup canary")
	if err := runMigrationPreparedSource(
		func() error { return cleanupCanary },
		func() error { return nil },
	); !errors.Is(err, errPostMigrationCleanup) || !errors.Is(err, cleanupCanary) {
		t.Fatalf("successful operation cleanup error = %v", err)
	}
}

func TestMigrationStoreRemainingProviderCallbacks(t *testing.T) {
	fixture := newMigrationStoreCloseoutFixture(t, nil)
	canary := errors.New("store callback closeout canary")

	t.Run("provider not required", func(t *testing.T) {
		result := &StoreResult{}
		if err := fixture.engine.migrateStoreWithOps(
			t.Context(),
			fixture.fence,
			fixture.guard,
			sqliteprovider.ImmutableGenerationSource{},
			fixture.spec,
			fixture.adapter,
			fixture.backup,
			result,
			defaultMigrationStoreOps(),
		); err != nil {
			t.Fatal(err)
		}
	})

	validationTests := []struct {
		name    string
		inspect func(context.Context, string, time.Duration) (sqliteprovider.Inspection, error)
		release func(sqliteprovider.Inspection) error
		want    error
	}{
		{
			name: "inspection",
			inspect: func(context.Context, string, time.Duration) (sqliteprovider.Inspection, error) {
				return sqliteprovider.Inspection{}, canary
			},
			release: func(sqliteprovider.Inspection) error { return nil },
			want:    canary,
		},
		{
			name: "release",
			inspect: func(context.Context, string, time.Duration) (sqliteprovider.Inspection, error) {
				return sqliteprovider.Inspection{}, nil
			},
			release: func(sqliteprovider.Inspection) error { return canary },
			want:    canary,
		},
		{
			name: "not ready",
			inspect: func(context.Context, string, time.Duration) (sqliteprovider.Inspection, error) {
				return sqliteprovider.Inspection{}, nil
			},
			release: func(sqliteprovider.Inspection) error { return nil },
			want:    ErrAdapterRequired,
		},
	}
	for _, test := range validationTests {
		t.Run("validation "+test.name, func(t *testing.T) {
			ops := defaultMigrationStoreOps()
			ops.inspect = test.inspect
			releases := 0
			ops.release = func(inspection sqliteprovider.Inspection) error {
				releases++
				return test.release(inspection)
			}
			ops.provider = migrationProviderCallsValidation(filepath.Join(fixture.home, "stage.db"))
			result := &StoreResult{providerRequired: true}
			err := fixture.engine.migrateStoreWithOps(
				t.Context(),
				fixture.fence,
				fixture.guard,
				sqliteprovider.ImmutableGenerationSource{},
				fixture.spec,
				fixture.adapter,
				fixture.backup,
				result,
				ops,
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("validation %s error = %v, want %v", test.name, err, test.want)
			}
			if releases != 1 {
				t.Fatalf("validation %s releases = %d, want 1", test.name, releases)
			}
		})
	}

	t.Run("missing migration callback", func(t *testing.T) {
		result := &StoreResult{providerRequired: true, migrationRequired: true}
		adapter := fixture.adapter
		adapter.Migrate = nil
		err := fixture.engine.migrateStoreWithOps(
			t.Context(), fixture.fence, fixture.guard,
			sqliteprovider.ImmutableGenerationSource{}, fixture.spec, adapter,
			fixture.backup, result, defaultMigrationStoreOps(),
		)
		if !errors.Is(err, ErrAdapterRequired) || !result.AdapterRequired {
			t.Fatalf("missing callback = %#v, %v", result, err)
		}
	})

	t.Run("staged authorization", func(t *testing.T) {
		ops := defaultMigrationStoreOps()
		ops.provider = migrationProviderCallsMigration("")
		result := &StoreResult{providerRequired: true, migrationRequired: true}
		err := fixture.engine.migrateStoreWithOps(
			t.Context(), fixture.fence, fixture.guard,
			sqliteprovider.ImmutableGenerationSource{}, fixture.spec, fixture.adapter,
			fixture.backup, result, ops,
		)
		if database.CodeOf(err) != database.CodeInvalid {
			t.Fatalf("staged authorization error = %v", err)
		}
	})

	t.Run("legacy preparation", func(t *testing.T) {
		ops := defaultMigrationStoreOps()
		ops.provider = migrationProviderCallsMigration(filepath.Join(fixture.home, "stage.db"))
		mismatched := fixture.spec
		mismatched.LegacyRoots = []string{filepath.Join(fixture.home, "different-legacy")}
		result := &StoreResult{providerRequired: true, migrationRequired: true}
		if err := fixture.engine.migrateStoreWithOps(
			t.Context(), fixture.fence, fixture.guard,
			sqliteprovider.ImmutableGenerationSource{}, mismatched, fixture.adapter,
			fixture.backup, result, ops,
		); err == nil {
			t.Fatal("migration accepted mismatched legacy provenance")
		}
	})

	t.Run("no legacy inputs", func(t *testing.T) {
		ops := defaultMigrationStoreOps()
		ops.provider = migrationProviderCallsMigration(filepath.Join(fixture.home, "stage.db"))
		adapter := fixture.adapter
		adapter.Migrate = func(_ context.Context, target databaseadapter.Target) error {
			if target.LegacyRoots != nil {
				return errors.New("adapter received nonnil legacy inputs")
			}
			return nil
		}
		result := &StoreResult{providerRequired: true, migrationRequired: true}
		if err := fixture.engine.migrateStoreWithOps(
			t.Context(), fixture.fence, fixture.guard,
			sqliteprovider.ImmutableGenerationSource{}, fixture.spec, adapter,
			fixture.backup, result, ops,
		); err != nil {
			t.Fatal(err)
		}
	})
}

func TestMigrationProviderRejectsCanceledChildContext(t *testing.T) {
	home := migrationHome(t)
	catalog, err := storecatalog.Project(storecatalog.Options{
		Home: home, Config: &config.Config{},
	})
	if err != nil {
		t.Fatal(err)
	}
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	claimRoot, err := databaseclaims.PrepareRootForTesting(t.TempDir())
	if err != nil {
		_ = fence.Close()
		t.Fatal(err)
	}
	lease, err := databaseclaims.AcquireForTesting(
		storecatalog.Options{},
		catalog,
		fence,
		claimRoot,
	)
	if err != nil {
		_ = fence.Close()
		t.Fatal(err)
	}
	guard, err := lease.GuardStoresMigrating()
	if err != nil {
		_ = lease.Close()
		_ = fence.Close()
		t.Fatal(err)
	}
	defer func() {
		_ = guard.Release()
		_ = lease.Close()
		_ = fence.Close()
	}()
	stores, err := guard.Stores()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := runMigrationProvider(
		ctx,
		guard,
		stores[0],
		sqliteprovider.ImmutableGenerationSource{},
		time.Second,
		1,
		func(context.Context, string) error { return nil },
		func(context.Context, string) error { return nil },
		nil,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled provider child = %v", err)
	}
}

type migrationStoreCloseoutFixture struct {
	home    string
	engine  *Engine
	fence   *database.Fence
	guard   *databaseclaims.MigrationRefreshingGuard
	spec    storecatalog.Spec
	adapter databaseadapter.Adapter
	backup  *backupSession
}

func newMigrationStoreCloseoutFixture(
	t *testing.T,
	legacyRoots []string,
) migrationStoreCloseoutFixture {
	t.Helper()
	home := migrationHome(t)
	spec := storecatalog.Spec{
		ID: "global/auth", Domain: "auth", Path: filepath.Join(home, "auth.db"),
		LegacyRoots: append([]string(nil), legacyRoots...),
	}
	backup, err := snapshotBackup(
		t.Context(), time.Now, home,
		[]storecatalog.Spec{spec}, []storecatalog.Spec{spec}, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fence.Close() })
	return migrationStoreCloseoutFixture{
		home:   home,
		engine: &Engine{options: storecatalog.Options{Home: home}},
		fence:  fence,
		guard:  &databaseclaims.MigrationRefreshingGuard{},
		spec:   spec,
		adapter: databaseadapter.Adapter{
			Domain: spec.Domain,
			Contract: databaseadapter.Contract{
				CurrentVersion: 1, EmptyPolicy: databaseadapter.EmptyMigrateOffline,
			},
			Migrate: func(context.Context, databaseadapter.Target) error { return nil },
		},
		backup: backup,
	}
}

func migrationProviderCallsValidation(stage string) func(
	context.Context,
	*databaseclaims.MigrationRefreshingGuard,
	storecatalog.Spec,
	sqliteprovider.ImmutableGenerationSource,
	time.Duration,
	int,
	sqliteprovider.StagedMigration,
	sqliteprovider.StagedValidation,
	sqliteprovider.StagedLiveVerification,
) (sqliteprovider.MaintenanceResult, error) {
	return func(
		ctx context.Context,
		_ *databaseclaims.MigrationRefreshingGuard,
		_ storecatalog.Spec,
		_ sqliteprovider.ImmutableGenerationSource,
		_ time.Duration,
		_ int,
		_ sqliteprovider.StagedMigration,
		validate sqliteprovider.StagedValidation,
		_ sqliteprovider.StagedLiveVerification,
	) (sqliteprovider.MaintenanceResult, error) {
		return sqliteprovider.MaintenanceResult{}, validate(ctx, stage)
	}
}

func migrationProviderCallsMigration(stage string) func(
	context.Context,
	*databaseclaims.MigrationRefreshingGuard,
	storecatalog.Spec,
	sqliteprovider.ImmutableGenerationSource,
	time.Duration,
	int,
	sqliteprovider.StagedMigration,
	sqliteprovider.StagedValidation,
	sqliteprovider.StagedLiveVerification,
) (sqliteprovider.MaintenanceResult, error) {
	return func(
		ctx context.Context,
		_ *databaseclaims.MigrationRefreshingGuard,
		_ storecatalog.Spec,
		_ sqliteprovider.ImmutableGenerationSource,
		_ time.Duration,
		_ int,
		migrate sqliteprovider.StagedMigration,
		_ sqliteprovider.StagedValidation,
		_ sqliteprovider.StagedLiveVerification,
	) (sqliteprovider.MaintenanceResult, error) {
		return sqliteprovider.MaintenanceResult{AfterVersion: 1}, migrate(ctx, stage)
	}
}
