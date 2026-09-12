package databasemigration

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/databaseadapter"
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
		if engine, newErr := New(options, registry); engine != nil ||
			database.CodeOf(newErr) != database.CodeInvalid {
			t.Fatalf("New(%#v) = %#v, %v", options, engine, newErr)
		}
	}
	if engine, newErr := New(
		storecatalog.Options{Home: migrationHome(t), Config: &config.Config{}},
		nil,
	); engine != nil || database.CodeOf(newErr) != database.CodeInvalid {
		t.Fatalf("New(nil registry) = %#v, %v", engine, newErr)
	}
	if engine, newErr := New(
		storecatalog.Options{Home: "bad\x00home", Config: &config.Config{}},
		registry,
	); engine != nil || newErr == nil {
		t.Fatalf("New(invalid home) = %#v, %v", engine, newErr)
	}

	var nilEngine *Engine
	if result, runErr := nilEngine.Run(t.Context(), Options{}); runErr == nil ||
		result.BackupDir != "" {
		t.Fatalf("nil Engine.Run() = %#v, %v", result, runErr)
	}
	if result, runErr := (&Engine{}).Run(t.Context(), Options{}); runErr == nil ||
		result.BackupDir != "" {
		t.Fatalf("empty Engine.Run() = %#v, %v", result, runErr)
	}

	home := migrationHome(t)
	engine := migrationEngine(t, home, registry)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if result, runErr := engine.Run(canceled, Options{DryRun: true}); !errors.Is(
		runErr,
		context.Canceled,
	) || !result.DryRun {
		t.Fatalf("canceled Run() = %#v, %v", result, runErr)
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
		if got, selectErr := selectStores(all, requested); got != nil || selectErr == nil {
			t.Errorf("selectStores(%q) = %#v, %v", requested, got, selectErr)
		}
	}
	sorted, err := selectStores(all, []database.StoreID{"workspace/workflows", auth})
	if err != nil || len(sorted) != 2 || sorted[0].ID != auth {
		t.Fatalf("sorted selection = %#v, %v", sorted, err)
	}
}

func TestMigrationStateHelpers(t *testing.T) {
	contract := databaseadapter.Contract{
		CurrentVersion: 2,
		EmptyPolicy:    databaseadapter.EmptyInitializeOnline,
	}
	for _, test := range []struct {
		name       string
		inspection sqliteprovider.Inspection
		legacy     bool
		contract   databaseadapter.Contract
		want       bool
	}{
		{name: "missing online", contract: contract},
		{name: "missing legacy", contract: contract, legacy: true, want: true},
		{
			name: "missing offline",
			contract: databaseadapter.Contract{
				CurrentVersion: 2,
				EmptyPolicy:    databaseadapter.EmptyMigrateOffline,
			},
			want: true,
		},
		{
			name:       "empty legacy",
			inspection: sqliteprovider.Inspection{Exists: true, Empty: true},
			contract:   contract,
			legacy:     true,
			want:       true,
		},
		{
			name:       "old",
			inspection: sqliteprovider.Inspection{Exists: true, Version: 1},
			contract:   contract,
			want:       true,
		},
		{
			name:       "new",
			inspection: sqliteprovider.Inspection{Exists: true, Version: 3},
			contract:   contract,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, stateErr := migrationNeeded(
				t.Context(),
				test.contract,
				test.inspection,
				test.legacy,
			)
			if stateErr != nil || got != test.want {
				t.Fatalf("migrationNeeded() = %t, %v; want %t", got, stateErr, test.want)
			}
		})
	}
	if ready, readyErr := contractReady(
		t.Context(),
		contract,
		sqliteprovider.Inspection{},
	); ready || readyErr != nil {
		t.Fatalf("contractReady(missing) = %t, %v", ready, readyErr)
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
		RequiredColumns: []databaseadapter.ColumnSet{
			{Table: "items", Columns: []string{"id"}},
		},
		ImportHorizon: "auth",
	}
	if ready, readyErr := contractReady(
		t.Context(),
		readyContract,
		inspection,
	); readyErr != nil || !ready {
		t.Fatalf("contractReady(valid) = %t, %v", ready, readyErr)
	}
	for _, mutate := range []func(*databaseadapter.Contract){
		func(value *databaseadapter.Contract) {
			value.RequiredObjects = []databaseadapter.SchemaObject{
				{Type: "table", Name: "missing"},
			}
		},
		func(value *databaseadapter.Contract) {
			value.RequiredColumns = []databaseadapter.ColumnSet{
				{Table: "items", Columns: []string{"missing"}},
			}
		},
		func(value *databaseadapter.Contract) { value.ImportHorizon = "missing" },
	} {
		candidate := readyContract
		mutate(&candidate)
		if ready, readyErr := contractReady(
			t.Context(),
			candidate,
			inspection,
		); readyErr != nil || ready {
			t.Fatalf("contractReady(mismatch) = %t, %v", ready, readyErr)
		}
	}
}

func TestMigrationBackupInventoryHelpers(t *testing.T) {
	auth := database.StoreID("global/auth")
	backup := &backupSession{manifest: BackupManifest{Stores: []BackupStoreManifest{
		{
			StoreID:         auth.String(),
			Exists:          true,
			LegacyRoots:     []string{"/legacy/missing", "/legacy/file"},
			LegacyRootKinds: []string{"missing", "file"},
		},
	}}}
	legacy, err := backupLegacyExists(backup, auth)
	if err != nil || !legacy {
		t.Fatalf("backupLegacyExists() = %t, %v", legacy, err)
	}
	results := []StoreResult{{ID: auth}}
	specs := []storecatalog.Spec{{ID: auth}}
	if err := populateMigrationStoreExistence(backup, specs, results); err != nil ||
		!results[0].Exists {
		t.Fatalf("populate store existence = %#v, %v", results, err)
	}

	missing := database.StoreID("global/missing")
	if _, err := backupLegacyExists(backup, missing); err == nil {
		t.Fatal("legacy lookup accepted missing store")
	}
	invalidKind := *backup
	invalidKind.manifest.Stores = append(
		[]BackupStoreManifest(nil),
		backup.manifest.Stores...,
	)
	invalidKind.manifest.Stores[0].LegacyRootKinds = []string{"missing", "unknown"}
	if _, err := backupLegacyExists(&invalidKind, auth); err == nil {
		t.Fatal("legacy lookup accepted invalid root kind")
	}
	duplicate := *backup
	duplicate.manifest.Stores = append(
		append([]BackupStoreManifest(nil), backup.manifest.Stores...),
		backup.manifest.Stores[0],
	)
	if _, err := backupLegacyExists(&duplicate, auth); err == nil {
		t.Fatal("legacy lookup accepted duplicate store")
	}
	if err := populateMigrationStoreExistence(backup, []storecatalog.Spec{{ID: missing}}, results); err == nil {
		t.Fatal("existence projection accepted missing store")
	}
}

func TestMigrationOutcomeAndCleanupHelpers(t *testing.T) {
	canary := errors.New("helper canary")
	cutover := Result{Stores: []StoreResult{{cutover: true}}}
	tests := []struct {
		name    string
		dryRun  bool
		result  Result
		err     error
		outcome string
	}{
		{name: "dry run", dryRun: true, outcome: "dry_run"},
		{name: "complete", outcome: "complete"},
		{name: "failed", err: canary, outcome: "failed"},
		{
			name: "unknown",
			err: database.NewError(
				database.CodeOutcomeUnknown,
				"unknown",
			),
			outcome: "outcome_unknown",
		},
		{
			name:    "cleanup",
			result:  cutover,
			err:     errors.Join(errPostMigrationCleanup, canary),
			outcome: "complete_with_cleanup_error",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := migrationTerminalOutcome(
				test.dryRun,
				test.result,
				test.err,
			); got != test.outcome {
				t.Fatalf("terminal outcome = %q, want %q", got, test.outcome)
			}
		})
	}
	if !migrationCutoverCompleted(cutover) ||
		!migrationCutoverMayHaveOccurred(cutover, nil) ||
		migrationCutoverCompleted(Result{}) {
		t.Fatal("cutover classification is inconsistent")
	}
	unknown := migrationOutcomeUnknown("uncertain", canary)
	if database.CodeOf(unknown) != database.CodeOutcomeUnknown ||
		!errors.Is(unknown, canary) {
		t.Fatalf("outcome-unknown helper = %v", unknown)
	}
	if callMigrationCleanup(nil) != nil {
		t.Fatal("nil cleanup returned an error")
	}
	if cleanupErr := callMigrationCleanup(func() error { return canary }); !errors.Is(
		cleanupErr,
		canary,
	) {
		t.Fatalf("cleanup helper = %v", cleanupErr)
	}
}

func TestMigrationProviderAndStoreRejectUnavailableAuthority(t *testing.T) {
	if _, err := runMigrationProvider(
		nil,
		nil,
		storecatalog.Spec{},
		sqliteprovider.ImmutableGenerationSource{},
		time.Second,
		1,
		func(context.Context, string) error { return nil },
		func(context.Context, string) error { return nil },
		nil,
	); err == nil {
		t.Fatal("provider accepted unavailable authority")
	}
	engine := &Engine{options: storecatalog.Options{Home: migrationHome(t)}}
	if err := engine.migrateStoreWithOps(
		t.Context(),
		nil,
		nil,
		sqliteprovider.ImmutableGenerationSource{},
		storecatalog.Spec{},
		databaseadapter.Adapter{},
		nil,
		nil,
		migrationStoreOps{},
	); err == nil {
		t.Fatal("store migration accepted unavailable state")
	}
	if err := runMigrationPreparedSource(nil, func() error { return nil }); err == nil {
		t.Fatal("prepared-source runner accepted nil cleanup")
	}
	if _, err := runMigrationProviderChild(nil, func() (
		sqliteprovider.MaintenanceResult,
		error,
	) {
		return sqliteprovider.MaintenanceResult{}, nil
	}); err == nil {
		t.Fatal("provider-child runner accepted nil drain")
	}
}

func TestMigrationProviderPanicDrainsBeforeSourceAndGuardCleanup(t *testing.T) {
	events := make([]string, 0, 3)
	var operationErr error
	func() {
		defer func() { events = append(events, "guard release") }()
		operationErr = runMigrationPreparedSource(
			func() error {
				events = append(events, "source cleanup")
				return nil
			},
			func() error {
				_, err := runMigrationProviderChild(
					func(context.Context, error) error {
						events = append(events, "child drain")
						return nil
					},
					func() (sqliteprovider.MaintenanceResult, error) {
						panic("untrusted provider panic detail")
					},
				)
				return err
			},
		)
	}()
	if database.CodeOf(operationErr) != database.CodeOutcomeUnknown ||
		strings.Contains(operationErr.Error(), "untrusted provider panic detail") {
		t.Fatalf("provider panic result = %v", operationErr)
	}
	want := []string{"child drain", "source cleanup", "guard release"}
	if !slices.Equal(events, want) {
		t.Fatalf("provider panic cleanup order = %#v, want %#v", events, want)
	}
}

func TestMigrationProviderDrainFailureMakesEveryOutcomeUnknown(t *testing.T) {
	providerErr := errors.New("known provider failure")
	drainErr := errors.New("provider drain failure")
	_, err := runMigrationProviderChild(
		func(context.Context, error) error { return drainErr },
		func() (sqliteprovider.MaintenanceResult, error) {
			return sqliteprovider.MaintenanceResult{}, providerErr
		},
	)
	if database.CodeOf(err) != database.CodeOutcomeUnknown ||
		!errors.Is(err, providerErr) || !errors.Is(err, drainErr) {
		t.Fatalf("mixed provider/drain failure = %v", err)
	}
}
