//nolint:govet // Independent filesystem boundary assertions intentionally use narrow errors.
package databasemigration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/databaseadapter"
	"github.com/sipeed/picoclaw/internal/databaseclaims"
	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestMigrationEngineAndSelectionBoundaries(t *testing.T) {
	registry, err := databaseadapter.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, options := range []storecatalog.Options{
		{},
		{Home: migrationHome(t)},
	} {
		if engine, err := New(options, registry); engine != nil || database.CodeOf(err) != database.CodeInvalid {
			t.Fatalf("New(%#v) = %#v, %v", options, engine, err)
		}
	}
	if engine, err := New(
		storecatalog.Options{Home: migrationHome(t), Config: &config.Config{}}, nil,
	); engine != nil || database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("New(nil registry) = %#v, %v", engine, err)
	}
	if engine, err := New(
		storecatalog.Options{Home: "bad\x00home", Config: &config.Config{}}, registry,
	); engine != nil || err == nil {
		t.Fatalf("New(invalid home) = %#v, %v", engine, err)
	}

	var nilEngine *Engine
	if result, err := nilEngine.Run(t.Context(), Options{}); err == nil || result.BackupDir != "" {
		t.Fatalf("nil Engine.Run() = %#v, %v", result, err)
	}
	if result, err := (&Engine{}).Run(t.Context(), Options{}); err == nil || result.BackupDir != "" {
		t.Fatalf("empty Engine.Run() = %#v, %v", result, err)
	}
	home := migrationHome(t)
	engine := migrationEngine(t, home, registry)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := engine.Run(canceled, Options{DryRun: true}); !errors.Is(err, context.Canceled) || !result.DryRun {
		t.Fatalf("canceled Run() = %#v, %v", result, err)
	}

	catalog, err := storecatalog.Project(storecatalog.Options{
		Home: home, Config: &config.Config{}, UserHome: migrationHome(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	all := catalog.All()
	selected, err := selectStores(all, nil)
	if err != nil || len(selected) != len(all) || len(selected) == 0 {
		t.Fatalf("selectStores(all) = %d/%d, %v", len(selected), len(all), err)
	}
	selected[0].Path = "mutated"
	if all[0].Path == "mutated" {
		t.Fatal("selectStores exposed source slice")
	}
	if len(selected[0].LegacyRoots) > 0 {
		selected[0].LegacyRoots[0] = "mutated"
		if all[0].LegacyRoots[0] == "mutated" {
			t.Fatal("selectStores exposed legacy roots")
		}
	}

	auth := database.StoreID("global/auth")
	for _, requested := range [][]database.StoreID{
		{"bad id"},
		{auth, auth},
		{"global/unknown"},
	} {
		if got, err := selectStores(all, requested); got != nil || err == nil {
			t.Errorf("selectStores(%q) = %#v, %v", requested, got, err)
		}
	}
	sorted, err := selectStores(all, []database.StoreID{"workspace/workflows", auth})
	if err != nil || len(sorted) != 2 || sorted[0].ID != auth {
		t.Fatalf("sorted selection = %#v, %v", sorted, err)
	}
}

func TestMigrationStateHelpers(t *testing.T) {
	contract := databaseadapter.Contract{CurrentVersion: 2, EmptyPolicy: databaseadapter.EmptyInitializeOnline}
	for _, test := range []struct {
		name       string
		inspection sqliteprovider.Inspection
		legacy     bool
		contract   databaseadapter.Contract
		want       bool
	}{
		{name: "missing online", contract: contract},
		{name: "missing legacy", contract: contract, legacy: true, want: true},
		{name: "missing offline", contract: databaseadapter.Contract{CurrentVersion: 2, EmptyPolicy: databaseadapter.EmptyMigrateOffline}, want: true},
		{name: "empty legacy", inspection: sqliteprovider.Inspection{Exists: true, Empty: true}, contract: contract, legacy: true, want: true},
		{name: "old", inspection: sqliteprovider.Inspection{Exists: true, Version: 1}, contract: contract, want: true},
		{name: "new", inspection: sqliteprovider.Inspection{Exists: true, Version: 3}, contract: contract},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := migrationNeeded(t.Context(), test.contract, test.inspection, test.legacy)
			if err != nil || got != test.want {
				t.Fatalf("migrationNeeded() = %t, %v; want %t", got, err, test.want)
			}
		})
	}
	if ready, err := contractReady(t.Context(), contract, sqliteprovider.Inspection{}); ready || err != nil {
		t.Fatalf("contractReady(missing) = %t, %v", ready, err)
	}
	broken := contract
	broken.RequiredObjects = []databaseadapter.SchemaObject{{Type: "table", Name: "items"}}
	if needed, err := migrationNeeded(
		t.Context(), broken, sqliteprovider.Inspection{Exists: true, Version: 2}, false,
	); !needed || err == nil {
		t.Fatalf("migrationNeeded(unavailable pool) = %t, %v", needed, err)
	}

	path := filepath.Join(migrationHome(t), "contract.db")
	createMigratedTarget(t.Context(), path, 2)
	inspection, err := sqliteprovider.Inspect(t.Context(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer inspection.Release()
	readyContract := databaseadapter.Contract{
		CurrentVersion: 2,
		RequiredObjects: []databaseadapter.SchemaObject{
			{Type: "table", Name: "items"},
			{Type: "table", Name: "storage_import_horizons"},
		},
		RequiredColumns: []databaseadapter.ColumnSet{{Table: "items", Columns: []string{"id"}}},
		ImportHorizon:   "auth",
	}
	if ready, err := contractReady(t.Context(), readyContract, inspection); err != nil || !ready {
		t.Fatalf("contractReady(valid) = %t, %v", ready, err)
	}
	for _, mutate := range []func(*databaseadapter.Contract){
		func(value *databaseadapter.Contract) {
			value.RequiredObjects = []databaseadapter.SchemaObject{{Type: "table", Name: "missing"}}
		},
		func(value *databaseadapter.Contract) {
			value.RequiredColumns = []databaseadapter.ColumnSet{{Table: "items", Columns: []string{"missing"}}}
		},
		func(value *databaseadapter.Contract) { value.ImportHorizon = "missing" },
	} {
		candidate := readyContract
		mutate(&candidate)
		if ready, err := contractReady(t.Context(), candidate, inspection); err != nil || ready {
			t.Fatalf("contractReady(mismatch) = %t, %v", ready, err)
		}
	}
}

func TestMigrationRunAndAdapterValidationBoundaries(t *testing.T) {
	t.Run("nil context missing online store", func(t *testing.T) {
		home := migrationHome(t)
		registry := migrationRegistry(t, databaseadapter.Adapter{
			Domain: "auth",
			Contract: databaseadapter.Contract{
				CurrentVersion: 1, EmptyPolicy: databaseadapter.EmptyInitializeOnline,
			},
		})
		result, err := migrationEngine(t, home, registry).Run(nil, Options{
			Stores: []database.StoreID{"global/auth"},
		})
		if err != nil || len(result.Stores) != 1 || result.Stores[0].Migrated {
			t.Fatalf("missing online Run() = %#v, %v", result, err)
		}
	})

	t.Run("migration fence setup error", func(t *testing.T) {
		homeFile := filepath.Join(t.TempDir(), "home-file")
		writeMigrationFile(t, homeFile, nil)
		registry, err := databaseadapter.NewRegistry()
		if err != nil {
			t.Fatal(err)
		}
		engine := &Engine{
			options:  storecatalog.Options{Home: homeFile, Config: &config.Config{}},
			registry: registry, now: time.Now,
		}
		if result, err := engine.Run(t.Context(), Options{DryRun: true}); err == nil ||
			errors.Is(err, ErrStorageActive) || result.BackupDir != "" {
			t.Fatalf("invalid home Run() = %#v, %v", result, err)
		}
	})

	t.Run("unregistered adapter after backup", func(t *testing.T) {
		home := migrationHome(t)
		registry, err := databaseadapter.NewRegistry()
		if err != nil {
			t.Fatal(err)
		}
		result, err := migrationEngine(t, home, registry).Run(t.Context(), Options{
			Stores: []database.StoreID{"global/auth"},
		})
		if !errors.Is(err, ErrAdapterRequired) || result.BackupDir == "" ||
			len(result.Stores) != 1 || !result.Stores[0].AdapterRequired {
			t.Fatalf("unregistered adapter Run() = %#v, %v", result, err)
		}
	})

	t.Run("physical claim conflict after fence transfer", func(t *testing.T) {
		home := migrationHome(t)
		claimFence, err := database.AcquireOnlineFence(home)
		if err != nil {
			t.Fatal(err)
		}
		lease, err := databaseclaims.Acquire(
			storecatalog.Options{Home: home, Config: &config.Config{}}, claimFence,
		)
		if err != nil {
			_ = claimFence.Close()
			t.Fatal(err)
		}
		defer lease.Close()
		if err := claimFence.Close(); err != nil {
			t.Fatal(err)
		}
		registry, err := databaseadapter.NewRegistry()
		if err != nil {
			t.Fatal(err)
		}
		result, err := migrationEngine(t, home, registry).Run(t.Context(), Options{DryRun: true})
		if !errors.Is(err, ErrStorageActive) || result.BackupDir != "" {
			t.Fatalf("physical claim conflict Run() = %#v, %v", result, err)
		}
	})

	t.Run("context cancellation between selected stores", func(t *testing.T) {
		home := migrationHome(t)
		contract := databaseadapter.Contract{
			CurrentVersion: 1, EmptyPolicy: databaseadapter.EmptyInitializeOnline,
		}
		registry, err := databaseadapter.NewRegistry(
			databaseadapter.Adapter{Domain: "auth", Contract: contract},
			databaseadapter.Adapter{Domain: "launcher-auth", Contract: contract},
		)
		if err != nil {
			t.Fatal(err)
		}
		ctx := &cancelAfterMigrationErrChecks{Context: context.Background(), allowed: 2}
		result, err := migrationEngine(t, home, registry).Run(ctx, Options{
			Stores: []database.StoreID{"global/auth", "launcher/auth"},
			DryRun: true,
		})
		if !errors.Is(err, context.Canceled) || result.BackupDir == "" {
			t.Fatalf("between-store cancellation Run() = %#v, %v", result, err)
		}
	})

	t.Run("staged contract missing object", func(t *testing.T) {
		home := migrationHome(t)
		contract := databaseadapter.Contract{
			CurrentVersion: 1, EmptyPolicy: databaseadapter.EmptyMigrateOffline,
			RequiredObjects: []databaseadapter.SchemaObject{{Type: "table", Name: "required_items"}},
		}
		registry := migrationRegistry(t, databaseadapter.Adapter{
			Domain: "auth", Contract: contract,
			Migrate: func(ctx context.Context, target databaseadapter.Target) error {
				return createVersionedMigrationTarget(ctx, target.GenerationPath, contract.CurrentVersion, "")
			},
		})
		result, err := migrationEngine(t, home, registry).Run(t.Context(), Options{
			Stores: []database.StoreID{"global/auth"},
		})
		if !errors.Is(err, ErrAdapterRequired) || result.BackupDir == "" {
			t.Fatalf("missing-object staged Run() = %#v, %v", result, err)
		}
		if _, statErr := os.Lstat(filepath.Join(home, "auth.db")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("failed validation installed live generation: %v", statErr)
		}
	})

	t.Run("staged contract metadata error", func(t *testing.T) {
		home := migrationHome(t)
		contract := databaseadapter.Contract{
			CurrentVersion: 1, EmptyPolicy: databaseadapter.EmptyMigrateOffline,
			RequiredObjects: []databaseadapter.SchemaObject{{Type: "table", Name: "items"}},
			RequiredColumns: []databaseadapter.ColumnSet{{Table: "items", Columns: []string{"id"}}},
		}
		registry := migrationRegistry(t, databaseadapter.Adapter{
			Domain: "auth", Contract: contract,
			Migrate: func(ctx context.Context, target databaseadapter.Target) error {
				return createVersionedMigrationTarget(
					ctx, target.GenerationPath, contract.CurrentVersion,
					"CREATE VIEW items AS SELECT missing_migration_function() AS id",
				)
			},
		})
		result, err := migrationEngine(t, home, registry).Run(t.Context(), Options{
			Stores: []database.StoreID{"global/auth"},
		})
		if err == nil || result.BackupDir == "" {
			t.Fatalf("malformed-metadata staged Run() = %#v, %v", result, err)
		}
		if _, statErr := os.Lstat(filepath.Join(home, "auth.db")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("metadata error installed live generation: %v", statErr)
		}
	})
}

func TestMigrateStoreRejectsInvalidStateAndUnverifiedLegacyBackup(t *testing.T) {
	home := migrationHome(t)
	engine := &Engine{options: storecatalog.Options{Home: home, Config: &config.Config{}}}
	spec := storecatalog.Spec{ID: "global/auth", Domain: "auth", Path: filepath.Join(home, "auth.db")}
	adapter := databaseadapter.Adapter{
		Domain: "auth",
		Contract: databaseadapter.Contract{
			CurrentVersion: 1, EmptyPolicy: databaseadapter.EmptyMigrateOffline,
		},
		Migrate: func(context.Context, databaseadapter.Target) error {
			return errors.New("adapter must not receive unverified input")
		},
	}
	result := &StoreResult{ID: spec.ID}
	if err := engine.migrateStore(
		t.Context(), nil, func(database.StoreID, string) error { return nil }, spec, adapter, &backupSession{}, result,
	); err == nil {
		t.Fatal("migrateStore accepted nil fence")
	}
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	if err := engine.migrateStore(
		t.Context(), fence, func(database.StoreID, string) error { return nil },
		storecatalog.Spec{ID: spec.ID}, adapter, &backupSession{}, result,
	); err == nil || !strings.Contains(err.Error(), "backup generation") {
		t.Fatalf("migrateStore(invalid path) = %v", err)
	}
	spec.LegacyRoots = []string{filepath.Join(home, "auth.json")}
	if err := engine.migrateStore(
		t.Context(), fence, func(database.StoreID, string) error { return nil }, spec, adapter,
		&backupSession{root: filepath.Join(home, "missing-backup")}, result,
	); err == nil || !strings.Contains(err.Error(), "backup") {
		t.Fatalf("migrateStore(unverified backup) = %v", err)
	}
}

func createVersionedMigrationTarget(ctx context.Context, path string, version int, schema string) error {
	db, err := sqliteprovider.OpenStore(path, time.Second)
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer db.Close()
	if err := sqliteprovider.ConfigureOffline(ctx, db, time.Second); err != nil {
		return err
	}
	if schema != "" {
		if _, err := db.ExecContext(ctx, schema); err != nil {
			return err
		}
	}
	return sqliteprovider.SetSchemaVersion(ctx, db, version)
}

func TestMigrateStoreInjectedProviderBoundaries(t *testing.T) {
	home := migrationHome(t)
	engine := &Engine{options: storecatalog.Options{Home: home, Config: &config.Config{}}}
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	spec := storecatalog.Spec{
		ID: "global/auth", Domain: "auth", Path: filepath.Join(home, "auth.db"),
	}
	dummyMigration := func(context.Context, databaseadapter.Target) error { return nil }
	adapter := databaseadapter.Adapter{
		Domain: "auth",
		Contract: databaseadapter.Contract{
			CurrentVersion: 1, EmptyPolicy: databaseadapter.EmptyMigrateOffline,
		},
		Migrate: dummyMigration,
	}
	baseOps := migrationStoreOps{
		inspect: func(context.Context, string, time.Duration) (sqliteprovider.Inspection, error) {
			return sqliteprovider.Inspection{}, nil
		},
		release: func(inspection sqliteprovider.Inspection) error { return inspection.Release() },
		migrate: func(
			context.Context,
			string,
			time.Duration,
			int,
			sqliteprovider.StagedMigration,
			sqliteprovider.StagedValidation,
		) error {
			return nil
		},
		maintain: func(context.Context, string, time.Duration) (sqliteprovider.MaintenanceResult, error) {
			return sqliteprovider.MaintenanceResult{}, nil
		},
		pin: func(database.StoreID, string) error { return nil },
	}
	newResult := func() *StoreResult { return &StoreResult{ID: spec.ID} }

	if err := engine.migrateStoreWithOps(
		t.Context(), fence, spec, adapter, &backupSession{}, newResult(), migrationStoreOps{},
	); err == nil {
		t.Fatal("migrateStoreWithOps accepted empty operations")
	}
	inspectCanary := errors.New("inspect canary")
	ops := baseOps
	ops.inspect = func(context.Context, string, time.Duration) (sqliteprovider.Inspection, error) {
		return sqliteprovider.Inspection{}, inspectCanary
	}
	if err := engine.migrateStoreWithOps(
		t.Context(), fence, spec, adapter, &backupSession{}, newResult(), ops,
	); !errors.Is(err, inspectCanary) {
		t.Fatalf("initial inspection error = %v", err)
	}

	stateAdapter := adapter
	stateAdapter.Contract.RequiredObjects = []databaseadapter.SchemaObject{{Type: "table", Name: "items"}}
	ops = baseOps
	ops.inspect = func(context.Context, string, time.Duration) (sqliteprovider.Inspection, error) {
		return sqliteprovider.Inspection{Exists: true, Version: 1}, nil
	}
	if err := engine.migrateStoreWithOps(
		t.Context(), fence, spec, stateAdapter, &backupSession{}, newResult(), ops,
	); err == nil || !strings.Contains(err.Error(), "migration state") {
		t.Fatalf("migration-state inspection error = %v", err)
	}
	releaseCanary := errors.New("release canary")
	ops = baseOps
	ops.release = func(sqliteprovider.Inspection) error { return releaseCanary }
	if err := engine.migrateStoreWithOps(
		t.Context(), fence, spec, adapter, &backupSession{}, newResult(), ops,
	); !errors.Is(err, releaseCanary) {
		t.Fatalf("initial inspection release error = %v", err)
	}

	t.Run("callback authorization", func(t *testing.T) {
		ops := baseOps
		ops.migrate = func(
			ctx context.Context,
			_ string,
			_ time.Duration,
			_ int,
			migrate sqliteprovider.StagedMigration,
			_ sqliteprovider.StagedValidation,
		) error {
			return migrate(ctx, "")
		}
		err := engine.migrateStoreWithOps(
			t.Context(), fence, spec, adapter, &backupSession{}, newResult(), ops,
		)
		if database.CodeOf(err) != database.CodeInvalid {
			t.Fatalf("unauthorized staged callback = %v", err)
		}
	})

	t.Run("validation inspection", func(t *testing.T) {
		backup, _ := verifiedBackupFixture(t, []byte("legacy"))
		calls := 0
		ops := baseOps
		ops.inspect = func(context.Context, string, time.Duration) (sqliteprovider.Inspection, error) {
			calls++
			if calls == 1 {
				return sqliteprovider.Inspection{}, nil
			}
			return sqliteprovider.Inspection{}, inspectCanary
		}
		ops.migrate = func(
			ctx context.Context,
			_ string,
			_ time.Duration,
			_ int,
			_ sqliteprovider.StagedMigration,
			validate sqliteprovider.StagedValidation,
		) error {
			return validate(ctx, filepath.Join(home, "stage.db"))
		}
		err := engine.migrateStoreWithOps(t.Context(), fence, spec, adapter, backup, newResult(), ops)
		if !errors.Is(err, inspectCanary) {
			t.Fatalf("staged validation inspection error = %v", err)
		}
	})

	t.Run("validation contract query", func(t *testing.T) {
		backup, _ := verifiedBackupFixture(t, []byte("legacy"))
		calls := 0
		ops := baseOps
		ops.inspect = func(context.Context, string, time.Duration) (sqliteprovider.Inspection, error) {
			calls++
			if calls == 1 {
				return sqliteprovider.Inspection{}, nil
			}
			return sqliteprovider.Inspection{Exists: true, Version: 1}, nil
		}
		ops.migrate = func(
			ctx context.Context,
			_ string,
			_ time.Duration,
			_ int,
			_ sqliteprovider.StagedMigration,
			validate sqliteprovider.StagedValidation,
		) error {
			return validate(ctx, filepath.Join(home, "stage.db"))
		}
		candidate := adapter
		candidate.Contract.RequiredObjects = []databaseadapter.SchemaObject{{Type: "table", Name: "items"}}
		err := engine.migrateStoreWithOps(t.Context(), fence, spec, candidate, backup, newResult(), ops)
		if err == nil {
			t.Fatal("staged contract query error succeeded")
		}
	})

	t.Run("validation inspection release", func(t *testing.T) {
		backup, _ := verifiedBackupFixture(t, []byte("legacy"))
		stagePath := filepath.Join(migrationHome(t), "stage.db")
		if err := createVersionedMigrationTarget(
			t.Context(), stagePath, 1, "CREATE TABLE items (id INTEGER PRIMARY KEY)",
		); err != nil {
			t.Fatal(err)
		}
		calls := 0
		ops := baseOps
		ops.inspect = func(ctx context.Context, _ string, timeout time.Duration) (sqliteprovider.Inspection, error) {
			calls++
			if calls == 1 {
				return sqliteprovider.Inspection{}, nil
			}
			return sqliteprovider.Inspect(ctx, stagePath, timeout)
		}
		releases := 0
		ops.release = func(inspection sqliteprovider.Inspection) error {
			releases++
			if releases == 1 {
				return inspection.Release()
			}
			return errors.Join(inspection.Release(), releaseCanary)
		}
		ops.migrate = func(
			ctx context.Context,
			_ string,
			_ time.Duration,
			_ int,
			_ sqliteprovider.StagedMigration,
			validate sqliteprovider.StagedValidation,
		) error {
			return validate(ctx, stagePath)
		}
		err := engine.migrateStoreWithOps(t.Context(), fence, spec, adapter, backup, newResult(), ops)
		if !errors.Is(err, releaseCanary) {
			t.Fatalf("staged inspection release error = %v", err)
		}
	})

	for _, test := range []struct {
		name     string
		after    sqliteprovider.Inspection
		afterErr error
		contract databaseadapter.Contract
		wantCode database.ErrorCode
	}{
		{name: "cutover reinspection", afterErr: inspectCanary, contract: adapter.Contract, wantCode: database.CodeOutcomeUnknown},
		{
			name: "cutover contract query", after: sqliteprovider.Inspection{Exists: true, Version: 1},
			contract: databaseadapter.Contract{
				CurrentVersion: 1, EmptyPolicy: databaseadapter.EmptyMigrateOffline,
				RequiredObjects: []databaseadapter.SchemaObject{{Type: "table", Name: "items"}},
			},
			wantCode: database.CodeOutcomeUnknown,
		},
		{name: "cutover contract mismatch", contract: adapter.Contract, wantCode: database.CodeOutcomeUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			ops := baseOps
			ops.inspect = func(context.Context, string, time.Duration) (sqliteprovider.Inspection, error) {
				calls++
				if calls == 1 {
					return sqliteprovider.Inspection{}, nil
				}
				return test.after, test.afterErr
			}
			candidate := adapter
			candidate.Contract = test.contract
			err := engine.migrateStoreWithOps(
				t.Context(), fence, spec, candidate, &backupSession{}, newResult(), ops,
			)
			if database.CodeOf(err) != test.wantCode {
				t.Fatalf("post-cutover error = %v", err)
			}
		})
	}

	t.Run("post-cutover inspection release", func(t *testing.T) {
		afterPath := filepath.Join(migrationHome(t), "after.db")
		if err := createVersionedMigrationTarget(
			t.Context(), afterPath, 1, "CREATE TABLE items (id INTEGER PRIMARY KEY)",
		); err != nil {
			t.Fatal(err)
		}
		calls := 0
		ops := baseOps
		ops.inspect = func(ctx context.Context, _ string, timeout time.Duration) (sqliteprovider.Inspection, error) {
			calls++
			if calls == 1 {
				return sqliteprovider.Inspection{}, nil
			}
			return sqliteprovider.Inspect(ctx, afterPath, timeout)
		}
		releases := 0
		ops.release = func(inspection sqliteprovider.Inspection) error {
			releases++
			if releases == 1 {
				return inspection.Release()
			}
			return errors.Join(inspection.Release(), releaseCanary)
		}
		err := engine.migrateStoreWithOps(
			t.Context(), fence, spec, adapter, &backupSession{}, newResult(), ops,
		)
		if database.CodeOf(err) != database.CodeOutcomeUnknown {
			t.Fatalf("post-cutover inspection release error = %v", err)
		}
	})
}

func TestMigrateStoreInjectedMaintenanceBoundaries(t *testing.T) {
	home := migrationHome(t)
	path := filepath.Join(home, "auth.db")
	if err := createVersionedMigrationTarget(
		t.Context(), path, 1, "CREATE TABLE items (id INTEGER PRIMARY KEY)",
	); err != nil {
		t.Fatal(err)
	}
	engine := &Engine{options: storecatalog.Options{Home: home, Config: &config.Config{}}}
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	spec := storecatalog.Spec{ID: "global/auth", Domain: "auth", Path: path}
	adapter := databaseadapter.Adapter{
		Domain: "auth",
		Contract: databaseadapter.Contract{
			CurrentVersion: 1, EmptyPolicy: databaseadapter.EmptyInitializeOnline,
		},
	}
	base := migrationStoreOps{
		inspect: sqliteprovider.Inspect,
		release: func(inspection sqliteprovider.Inspection) error { return inspection.Release() },
		migrate: func(
			context.Context,
			string,
			time.Duration,
			int,
			sqliteprovider.StagedMigration,
			sqliteprovider.StagedValidation,
		) error {
			return nil
		},
		maintain: func(context.Context, string, time.Duration) (sqliteprovider.MaintenanceResult, error) {
			return sqliteprovider.MaintenanceResult{BeforeVersion: 1, AfterVersion: 1}, nil
		},
		pin: func(database.StoreID, string) error { return nil },
	}
	maintainCanary := errors.New("maintain canary")
	ops := base
	ops.maintain = func(context.Context, string, time.Duration) (sqliteprovider.MaintenanceResult, error) {
		return sqliteprovider.MaintenanceResult{BeforeVersion: 1, AfterVersion: 1}, maintainCanary
	}
	if err := engine.migrateStoreWithOps(
		t.Context(), fence, spec, adapter, &backupSession{}, &StoreResult{ID: spec.ID}, ops,
	); !errors.Is(err, maintainCanary) {
		t.Fatalf("maintenance failure = %v", err)
	}

	corruptMaintenancePath := filepath.Join(migrationHome(t), "corrupt-maintenance.db")
	writeMigrationFile(t, corruptMaintenancePath, []byte("not sqlite"))
	maintenanceFence, fenceErr := database.AcquireMigrationFence(filepath.Dir(corruptMaintenancePath))
	if fenceErr != nil {
		t.Fatal(fenceErr)
	}
	maintenanceCtx, fenceErr := maintenanceFence.MigrationContext(
		t.Context(), corruptMaintenancePath,
	)
	if fenceErr != nil {
		_ = maintenanceFence.Close()
		t.Fatal(fenceErr)
	}
	_, maintenanceIntegrityErr := sqliteprovider.MaintainOffline(
		maintenanceCtx, corruptMaintenancePath, time.Second,
	)
	if closeErr := maintenanceFence.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if !sqliteprovider.IsMaintenanceIntegrity(maintenanceIntegrityErr) {
		t.Fatalf("maintenance integrity fixture = %v", maintenanceIntegrityErr)
	}
	ops = base
	ops.maintain = func(context.Context, string, time.Duration) (sqliteprovider.MaintenanceResult, error) {
		return sqliteprovider.MaintenanceResult{}, maintenanceIntegrityErr
	}
	if err := engine.migrateStoreWithOps(
		t.Context(), fence, spec, adapter, &backupSession{}, &StoreResult{ID: spec.ID}, ops,
	); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("maintenance integrity classification = %v", err)
	}

	corruptInspectionPath := filepath.Join(migrationHome(t), "corrupt-inspection.db")
	writeMigrationFile(t, corruptInspectionPath, []byte("not sqlite"))
	_, inspectionIntegrityErr := sqliteprovider.Inspect(t.Context(), corruptInspectionPath, time.Second)
	if !sqliteprovider.IsInspectionIntegrity(inspectionIntegrityErr) {
		t.Fatalf("inspection integrity fixture = %v", inspectionIntegrityErr)
	}
	first, err := sqliteprovider.Inspect(t.Context(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	ops = base
	ops.inspect = func(context.Context, string, time.Duration) (sqliteprovider.Inspection, error) {
		calls++
		if calls == 1 {
			return first, nil
		}
		return sqliteprovider.Inspection{}, inspectionIntegrityErr
	}
	if err := engine.migrateStoreWithOps(
		t.Context(), fence, spec, adapter, &backupSession{}, &StoreResult{ID: spec.ID}, ops,
	); database.CodeOf(err) != database.CodeOutcomeUnknown {
		t.Fatalf("post-maintenance integrity classification = %v", err)
	}

	t.Run("post-maintenance inspection release", func(t *testing.T) {
		releases := 0
		ops := base
		ops.release = func(inspection sqliteprovider.Inspection) error {
			releases++
			if releases == 1 {
				return inspection.Release()
			}
			return errors.Join(inspection.Release(), maintainCanary)
		}
		err := engine.migrateStoreWithOps(
			t.Context(), fence, spec, adapter, &backupSession{}, &StoreResult{ID: spec.ID}, ops,
		)
		if database.CodeOf(err) != database.CodeOutcomeUnknown {
			t.Fatalf("post-maintenance release error = %v", err)
		}
	})

	for _, test := range []struct {
		name     string
		after    sqliteprovider.Inspection
		afterErr error
		want     error
	}{
		{name: "reinspection", afterErr: errors.New("reinspect canary")},
		{name: "contract query", after: sqliteprovider.Inspection{Exists: true, Version: 1}},
		{name: "contract mismatch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			first, err := sqliteprovider.Inspect(t.Context(), path, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			ops := base
			ops.inspect = func(context.Context, string, time.Duration) (sqliteprovider.Inspection, error) {
				calls++
				if calls == 1 {
					return first, nil
				}
				return test.after, test.afterErr
			}
			candidate := adapter
			if test.name == "contract query" {
				candidate.Contract.RequiredObjects = []databaseadapter.SchemaObject{{Type: "table", Name: "items"}}
			}
			err = engine.migrateStoreWithOps(
				t.Context(), fence, spec, candidate, &backupSession{}, &StoreResult{ID: spec.ID}, ops,
			)
			if test.want != nil {
				if !errors.Is(err, test.want) {
					t.Fatalf("post-maintenance result = %v", err)
				}
			} else if err == nil {
				t.Fatal("post-maintenance fault succeeded")
			}
		})
	}
}
