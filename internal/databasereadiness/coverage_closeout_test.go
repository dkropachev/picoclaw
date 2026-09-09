//nolint:govet,golines,misspell // Dense failure-boundary fixtures intentionally use narrow scopes.
package databasereadiness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/databaseadapter"
	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestSnapshotBoundariesAndDetachedStatuses(t *testing.T) {
	var nilSnapshot *Snapshot
	if nilSnapshot.Statuses() != nil {
		t.Fatal("nil snapshot exposed statuses")
	}
	if err := nilSnapshot.Close(); err != nil {
		t.Fatalf("nil snapshot Close() = %v", err)
	}
	unbound := &Snapshot{statuses: []database.StoreStatus{{ID: "global/auth", Readiness: database.StoreReady}}}
	if unbound.Statuses() != nil {
		t.Fatal("snapshot without a live lease exposed statuses")
	}
	home := t.TempDir()
	lease, _ := acquireReadinessLease(t, home)
	invalid := &Snapshot{
		statuses: []database.StoreStatus{{ID: "bad id", Readiness: database.StoreReady}},
		lease:    lease,
	}
	if invalid.Statuses() != nil {
		t.Fatal("invalid snapshot exposed statuses")
	}
	valid := &Snapshot{
		statuses: []database.StoreStatus{{ID: "global/auth", Readiness: database.StoreReady}},
		lease:    lease,
	}
	statuses := valid.Statuses()
	if len(statuses) != 1 {
		t.Fatalf("Statuses() = %#v", statuses)
	}
	statuses[0].Readiness = database.StoreUnavailable
	if got := valid.Statuses(); len(got) != 1 || got[0].Readiness != database.StoreReady {
		t.Fatalf("Statuses() aliases snapshot state: %#v", got)
	}
	if err := valid.Close(); err != nil {
		t.Fatal(err)
	}
	if err := valid.Close(); err != nil {
		t.Fatalf("second Close() = %v", err)
	}
}

func TestProbeRejectsInvalidAndExpiredBoundaries(t *testing.T) {
	if snapshot, err := Probe(t.Context(), nil, nil); snapshot != nil ||
		database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("Probe(nil) = %#v, %v", snapshot, err)
	}
	home := t.TempDir()
	lease, fence := acquireReadinessLease(t, home)
	registry := readinessRegistry(t, lease.Stores(), "", databaseadapter.Contract{})
	if snapshot, err := Probe(t.Context(), lease, nil); snapshot != nil ||
		database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("Probe(nil registry) = %#v, %v", snapshot, err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if snapshot, err := Probe(t.Context(), lease, registry); snapshot != nil ||
		database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("Probe(closed lease) = %#v, %v", snapshot, err)
	}
	_ = fence
}

func TestProbeRejectsGenerationChangedAfterClaim(t *testing.T) {
	home := t.TempDir()
	lease, _ := acquireReadinessLease(t, home)
	registry := readinessRegistry(t, lease.Stores(), "", databaseadapter.Contract{})
	auth, ok := lease.Lookup("global/auth")
	if !ok {
		t.Fatal("auth store is absent")
	}
	if err := os.Mkdir(auth.Path, 0o700); err != nil {
		t.Fatal(err)
	}
	if snapshot, err := Probe(t.Context(), lease, registry); snapshot != nil ||
		database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("Probe(unsafe generation) = %#v, %v", snapshot, err)
	}
}

func TestProbeAcceptsNilContextWithoutInitializingMissingStores(t *testing.T) {
	home := t.TempDir()
	lease, _ := acquireReadinessLease(t, home)
	registry := readinessRegistry(t, lease.Stores(), "", databaseadapter.Contract{})
	snapshot, err := Probe(nil, lease, registry)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Statuses()) != len(lease.Stores()) {
		t.Fatalf("status count = %d, want %d", len(snapshot.Statuses()), len(lease.Stores()))
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProbeCancellationAfterBoundarySetupReleasesPartialSnapshot(t *testing.T) {
	home := t.TempDir()
	lease, _ := acquireReadinessLease(t, home)
	registry := readinessRegistry(t, lease.Stores(), "", databaseadapter.Contract{})
	ctx := &cancelAfterErrChecks{Context: context.Background(), allowed: 1}
	snapshot, err := Probe(ctx, lease, registry)
	if snapshot != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Probe(canceled after setup) = %#v, %v", snapshot, err)
	}
}

func TestClassifyInspectionErrorAndContractBoundaries(t *testing.T) {
	spec := storecatalog.Spec{ID: "global/auth", Domain: "auth"}
	contract := readinessContract(2, databaseadapter.EmptyInitializeOnline)

	status := classifyInspection(
		t.Context(), spec, contract, sqliteprovider.Inspection{}, errors.New("provider canary"), generationSet{},
	)
	if status.Readiness != database.StoreUnavailable || database.CodeOf(status.Error) != database.CodeUnavailable {
		t.Fatalf("generic provider failure = %#v", status)
	}

	corruptPath := filepath.Join(t.TempDir(), "corrupt.db")
	writeReadinessFile(t, corruptPath, []byte("not sqlite"))
	inspection, inspectErr := sqliteprovider.Inspect(t.Context(), corruptPath, time.Second)
	if inspectErr == nil {
		_ = inspection.Release()
		t.Fatal("corrupt database inspection succeeded")
	}
	status = classifyInspection(t.Context(), spec, contract, inspection, inspectErr, generationSet{})
	if status.Readiness != database.StoreIntegrityFailed || database.CodeOf(status.Error) != database.CodeIntegrity {
		t.Fatalf("integrity failure = %#v (%v)", status, inspectErr)
	}

	legacyTarget := filepath.Join(t.TempDir(), "legacy.json")
	writeReadinessFile(t, legacyTarget, []byte("legacy"))
	legacyLink := filepath.Join(t.TempDir(), "legacy-link")
	if err := os.Symlink(legacyTarget, legacyLink); err == nil {
		unsafe := spec
		unsafe.LegacyRoots = []string{legacyLink}
		status = classifyInspection(t.Context(), unsafe, contract, sqliteprovider.Inspection{}, nil, generationSet{})
		if status.Readiness != database.StoreIntegrityFailed || database.CodeOf(status.Error) != database.CodeIntegrity {
			t.Fatalf("unsafe legacy status = %#v", status)
		}
	}

	overlong := spec
	overlong.LegacyRoots = []string{filepath.Join(t.TempDir(), strings.Repeat("x", 4096))}
	status = classifyInspection(t.Context(), overlong, contract, sqliteprovider.Inspection{}, nil, generationSet{})
	if status.Readiness != database.StoreIntegrityFailed || database.CodeOf(status.Error) != database.CodeIntegrity {
		t.Fatalf("invalid legacy status = %#v", status)
	}

	for _, test := range []struct {
		name       string
		inspection sqliteprovider.Inspection
		contract   databaseadapter.Contract
		readiness  database.StoreReadiness
		code       database.ErrorCode
	}{
		{
			name: "schema query error", inspection: sqliteprovider.Inspection{Exists: true, Version: 2},
			contract: contract, readiness: database.StoreUnavailable, code: database.CodeUnavailable,
		},
		{
			name: "column query error", inspection: sqliteprovider.Inspection{Exists: true, Version: 2},
			contract: databaseadapter.Contract{
				CurrentVersion:  2,
				RequiredColumns: []databaseadapter.ColumnSet{{Table: "items", Columns: []string{"id"}}},
			},
			readiness: database.StoreUnavailable, code: database.CodeUnavailable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := classifyInspection(t.Context(), spec, test.contract, test.inspection, nil, generationSet{})
			if got.Readiness != test.readiness || database.CodeOf(got.Error) != test.code {
				t.Fatalf("classifyInspection() = %#v", got)
			}
		})
	}

	path := filepath.Join(t.TempDir(), "columns.db")
	createReadinessDatabase(t, path, 2, true, true)
	inspection, err := sqliteprovider.Inspect(t.Context(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inspection.Release() })
	missingColumn := contract
	missingColumn.RequiredColumns = []databaseadapter.ColumnSet{{Table: "items", Columns: []string{"missing"}}}
	status = classifyInspection(t.Context(), spec, missingColumn, inspection, nil, generationSet{})
	if status.Readiness != database.StoreMigrationRequired {
		t.Fatalf("missing-column status = %#v", status)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	status = classifyInspection(
		canceled, spec,
		databaseadapter.Contract{
			CurrentVersion:  2,
			RequiredColumns: []databaseadapter.ColumnSet{{Table: "items", Columns: []string{"id"}}},
		},
		inspection, nil, generationSet{},
	)
	if status.Readiness != database.StoreUnavailable || database.CodeOf(status.Error) != database.CodeUnavailable {
		t.Fatalf("canceled column status = %#v", status)
	}

	missingHorizonPath := filepath.Join(t.TempDir(), "missing-horizon.db")
	db, err := sqliteprovider.OpenStore(missingHorizonPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := sqliteprovider.Configure(t.Context(), db, time.Second, false); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "CREATE TABLE marker (id INTEGER PRIMARY KEY)"); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := sqliteprovider.SetSchemaVersion(t.Context(), db, 2); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	missingHorizon, err := sqliteprovider.Inspect(t.Context(), missingHorizonPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = missingHorizon.Release() })
	status = classifyInspection(
		t.Context(), spec,
		databaseadapter.Contract{CurrentVersion: 2, ImportHorizon: "auth"},
		missingHorizon, nil, generationSet{},
	)
	if status.Readiness != database.StoreMigrationRequired ||
		database.CodeOf(status.Error) != database.CodeMigrationRequired {
		t.Fatalf("invalid horizon status = %#v", status)
	}
}

func TestGenerationExclusionAndLegacyAliasBoundaries(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing.db")
	exclusions, err := generationExclusions([]storecatalog.Spec{{ID: "global/auth", Path: missing}})
	if err != nil || len(exclusions.paths) != 4 || len(exclusions.physical) != 0 {
		t.Fatalf("missing exclusions = %#v, %v", exclusions, err)
	}

	unsafe := filepath.Join(root, "unsafe.db")
	if err := os.Mkdir(unsafe, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := generationExclusions([]storecatalog.Spec{{ID: "global/auth", Path: unsafe}}); err == nil {
		t.Fatal("directory generation was accepted")
	}
	overlong := filepath.Join(root, strings.Repeat("x", 4096))
	if _, err := generationExclusions([]storecatalog.Spec{{ID: "global/auth", Path: overlong}}); err == nil {
		t.Fatal("uninspectable generation was accepted")
	}

	generation := filepath.Join(root, "state.db")
	alias := filepath.Join(root, "legacy.json")
	writeReadinessFile(t, generation, []byte("generation"))
	if err := os.Link(generation, alias); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	exclusions, err = generationExclusions([]storecatalog.Spec{{ID: "global/auth", Path: generation}})
	if err != nil {
		t.Fatal(err)
	}
	if excluded, err := excludedLegacyFile(alias, exclusions); excluded ||
		!errors.Is(err, errLegacyIntegrity) {
		t.Fatalf("hardlinked legacy exclusion = %t, %v", excluded, err)
	}
	if excluded, err := excludedLegacyFile(
		generation, generationSet{paths: map[string]struct{}{generationPathKey(generation): {}}},
	); err != nil || !excluded {
		t.Fatalf("exact generation exclusion = %t, %v", excluded, err)
	}
	if excluded, err := excludedLegacyFile(alias, generationSet{}); err != nil || excluded {
		t.Fatalf("ordinary legacy exclusion = %t, %v", excluded, err)
	}
}

func TestLegacyDiscoveryHelperBoundaries(t *testing.T) {
	if found, err := legacyInputExistsWithin(
		t.Context(), nil, generationSet{}, legacyDiscoveryLimits{},
	); found || !errors.Is(err, errLegacyIntegrity) {
		t.Fatalf("invalid discovery limits = %t, %v", found, err)
	}
	if budget, err := newLegacyDiscoveryBudget(legacyDiscoveryLimits{}); budget != nil ||
		!errors.Is(err, errLegacyIntegrity) {
		t.Fatalf("invalid budget = %#v, %v", budget, err)
	}
	if err := (*legacyDiscoveryBudget)(nil).enter(0, false); !errors.Is(err, errLegacyIntegrity) {
		t.Fatalf("nil budget enter = %v", err)
	}
	budget, err := newLegacyDiscoveryBudget(legacyDiscoveryLimits{maxEntries: 2, maxFiles: 1, maxDepth: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := budget.enter(-1, false); !errors.Is(err, errLegacyIntegrity) {
		t.Fatalf("negative depth enter = %v", err)
	}
	budget.entries = budget.limits.maxEntries
	if err := budget.enter(0, false); !errors.Is(err, errLegacyIntegrity) {
		t.Fatalf("exhausted entry budget = %v", err)
	}
	budget.entries = 0

	root := t.TempDir()
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	if entries, err := readLegacyDirectory(t.Context(), nil, 1); entries != nil ||
		!errors.Is(err, errLegacyIntegrity) {
		t.Fatalf("nil directory read = %#v, %v", entries, err)
	}
	if entries, err := readLegacyDirectory(t.Context(), directory, -1); entries != nil ||
		!errors.Is(err, errLegacyIntegrity) {
		t.Fatalf("negative directory budget = %#v, %v", entries, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if entries, err := readLegacyDirectory(canceled, directory, 1); entries != nil ||
		!errors.Is(err, context.Canceled) {
		t.Fatalf("canceled directory read = %#v, %v", entries, err)
	}
	regularReader, err := os.Open(fileReadinessFixture(t, root))
	if err != nil {
		t.Fatal(err)
	}
	defer regularReader.Close()
	if entries, err := readLegacyDirectory(t.Context(), regularReader, 1); entries != nil || err == nil {
		t.Fatalf("regular-file directory read = %#v, %v", entries, err)
	}

	file := filepath.Join(root, "plain")
	writeReadinessFile(t, file, []byte("legacy"))
	if found, err := walkLegacyDirectory(t.Context(), file, 0, generationSet{}, budget); found ||
		!errors.Is(err, errLegacyIntegrity) {
		t.Fatalf("file expected as directory = %t, %v", found, err)
	}
	freshBudget, err := newLegacyDiscoveryBudget(defaultLegacyDiscoveryLimits())
	if err != nil {
		t.Fatal(err)
	}
	if found, err := walkLegacyDirectory(
		t.Context(), missingReadinessPath(root), 0, generationSet{}, freshBudget,
	); found || !errors.Is(err, errLegacyIntegrity) {
		t.Fatalf("missing directory open = %t, %v", found, err)
	}
	canceledWalk, cancelWalk := context.WithCancel(context.Background())
	cancelWalk()
	if found, err := walkLegacyDirectory(
		canceledWalk, root, 0, generationSet{}, freshBudget,
	); found || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled directory walk = %t, %v", found, err)
	}

	if found, err := legacyInputExistsWithin(nil, []string{file}, generationSet{}, defaultLegacyDiscoveryLimits()); err != nil || !found {
		t.Fatalf("nil-context legacy discovery = %t, %v", found, err)
	}
	if found, err := legacyInputExists(t.Context(), []string{missingReadinessPath(root)}, generationSet{}); err != nil || found {
		t.Fatalf("missing legacy discovery = %t, %v", found, err)
	}
	if found, err := legacyInputExists(t.Context(), []string{root}, generationSet{}); err != nil || !found {
		t.Fatalf("directory legacy discovery = %t, %v", found, err)
	}
	if found, err := legacyInputExistsWithin(
		t.Context(), []string{file, file}, generationSet{},
		legacyDiscoveryLimits{maxEntries: 4, maxFiles: 1, maxDepth: 1},
	); found || !errors.Is(err, errLegacyIntegrity) {
		t.Fatalf("regular-root file budget = %t, %v", found, err)
	}
	finalCanceled, finalCancel := context.WithCancel(context.Background())
	finalCancel()
	if found, err := legacyInputExistsWithin(
		finalCanceled, nil, generationSet{}, defaultLegacyDiscoveryLimits(),
	); found || !errors.Is(err, context.Canceled) {
		t.Fatalf("final context check = %t, %v", found, err)
	}

	skippedRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(skippedRoot, "backups"), 0o700); err != nil {
		t.Fatal(err)
	}
	skippedBudget, err := newLegacyDiscoveryBudget(
		legacyDiscoveryLimits{maxEntries: 4, maxFiles: 4, maxDepth: 0},
	)
	if err != nil {
		t.Fatal(err)
	}
	if found, err := walkLegacyDirectory(
		t.Context(), skippedRoot, 0, generationSet{}, skippedBudget,
	); found || !errors.Is(err, errLegacyIntegrity) {
		t.Fatalf("skipped-tree depth budget = %t, %v", found, err)
	}

	nestedRoot := t.TempDir()
	writeReadinessFile(t, filepath.Join(nestedRoot, "nested", "legacy.json"), []byte("legacy"))
	nestedBudget, err := newLegacyDiscoveryBudget(defaultLegacyDiscoveryLimits())
	if err != nil {
		t.Fatal(err)
	}
	if found, err := walkLegacyDirectory(
		t.Context(), nestedRoot, 0, generationSet{}, nestedBudget,
	); err != nil || !found {
		t.Fatalf("nested legacy discovery = %t, %v", found, err)
	}
	loopCanceled := &cancelAfterErrChecks{Context: context.Background(), allowed: 3}
	loopBudget, err := newLegacyDiscoveryBudget(defaultLegacyDiscoveryLimits())
	if err != nil {
		t.Fatal(err)
	}
	if found, err := walkLegacyDirectory(
		loopCanceled, nestedRoot, 0, generationSet{}, loopBudget,
	); found || !errors.Is(err, context.Canceled) {
		t.Fatalf("directory-entry context cancellation = %t, %v", found, err)
	}
	renamedRoot := t.TempDir()
	renamedBudget, err := newLegacyDiscoveryBudget(defaultLegacyDiscoveryLimits())
	if err != nil {
		t.Fatal(err)
	}
	movedRoot := renamedRoot + "-moved"
	actionContext := &readinessActionContext{
		Context: context.Background(), allowed: 1,
		action: func() { _ = os.Rename(renamedRoot, movedRoot) },
	}
	if found, err := walkLegacyDirectory(
		actionContext, renamedRoot, 0, generationSet{}, renamedBudget,
	); found || !errors.Is(err, errLegacyIntegrity) {
		t.Fatalf("renamed directory discovery = %t, %v", found, err)
	}

	aliasRoot := t.TempDir()
	generation := filepath.Join(aliasRoot, "generation.db")
	legacyAlias := filepath.Join(aliasRoot, "tree", "legacy.json")
	writeReadinessFile(t, generation, []byte("generation"))
	if err := os.Mkdir(filepath.Dir(legacyAlias), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(generation, legacyAlias); err == nil {
		exclusions, exclusionErr := generationExclusions([]storecatalog.Spec{{ID: "global/auth", Path: generation}})
		if exclusionErr != nil {
			t.Fatal(exclusionErr)
		}
		if found, err := legacyInputExists(
			t.Context(), []string{legacyAlias}, exclusions,
		); found || !errors.Is(err, errLegacyIntegrity) {
			t.Fatalf("regular-root generation alias = %t, %v", found, err)
		}
		if found, err := legacyInputExists(
			t.Context(), []string{filepath.Dir(legacyAlias)}, exclusions,
		); found || !errors.Is(err, errLegacyIntegrity) {
			t.Fatalf("nested generation alias = %t, %v", found, err)
		}
	}
}

type cancelAfterErrChecks struct {
	context.Context
	allowed int
}

type readinessActionContext struct {
	context.Context
	allowed int
	action  func()
	done    bool
}

func (ctx *readinessActionContext) Err() error {
	if ctx.allowed > 0 {
		ctx.allowed--
		return nil
	}
	if !ctx.done {
		ctx.done = true
		ctx.action()
	}
	return nil
}

func (ctx *cancelAfterErrChecks) Err() error {
	if ctx.allowed > 0 {
		ctx.allowed--
		return nil
	}
	return context.Canceled
}

func fileReadinessFixture(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "reader-file")
	writeReadinessFile(t, path, []byte("plain"))
	return path
}

func missingReadinessPath(root string) string {
	return filepath.Join(root, "definitely-missing")
}

func TestReadinessGenerationHelpers(t *testing.T) {
	path := filepath.Join("root", "nested", "..", "state.db")
	paths := generationPaths(path)
	if len(paths) != 4 || paths[0] != path || paths[3] != path+"-journal" {
		t.Fatalf("generationPaths() = %#v", paths)
	}
	if got, want := generationPathKey(path), generationPathKey(filepath.Clean(path)); got != want {
		t.Fatalf("generationPathKey() = %q, want %q", got, want)
	}
	status := unavailableStatus("global/auth", database.CodeIntegrity, "broken")
	if status.Readiness != database.StoreIntegrityFailed || database.CodeOf(status.Error) != database.CodeIntegrity {
		t.Fatalf("integrity status = %#v", status)
	}
	status = unavailableStatus("global/auth", database.CodeUnsupported, "new")
	if status.Readiness != database.StoreUnavailable || database.CodeOf(status.Error) != database.CodeUnsupported {
		t.Fatalf("unsupported status = %#v", status)
	}
}

func TestReadinessRegistryHelperUsesEveryCatalogDomain(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	catalog, err := storecatalog.Project(storecatalog.Options{Home: home, Config: &config.Config{}})
	if err != nil {
		t.Fatal(err)
	}
	registry := readinessRegistry(t, catalog.All(), "", databaseadapter.Contract{})
	for _, spec := range catalog.All() {
		if _, ok := registry.Lookup(spec.Domain); !ok {
			t.Fatalf("registry omitted domain %q", spec.Domain)
		}
	}
}
