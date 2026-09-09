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
	"github.com/sipeed/picoclaw/internal/databaseclaims"
	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestSnapshotAdoptRejectsInvalidLifecycleBoundaries(t *testing.T) {
	var absent *Snapshot
	if db, err := absent.Adopt("global/auth"); db != nil ||
		database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("nil Snapshot.Adopt() = %#v, %v", db, err)
	}
	if db, err := (&Snapshot{}).Adopt("global/auth"); db != nil ||
		database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("unbound Snapshot.Adopt() = %#v, %v", db, err)
	}
}

func TestProbeFailsClosedAcrossLifecycleTransitions(t *testing.T) {
	home := t.TempDir()
	lease, _ := acquireReadinessLease(t, home)
	specs := lease.Stores()
	if len(specs) == 0 {
		t.Fatal("claimed catalog is empty")
	}
	registry := readinessRegistry(t, specs, "", databaseadapter.Contract{})
	canary := errors.New("readiness lifecycle canary")
	base := func() readinessProbeOps {
		ops := defaultReadinessProbeOps()
		ops.preflight = func(*databaseclaims.Lease) []storecatalog.Spec {
			return []storecatalog.Spec{specs[0]}
		}
		ops.guardStores = func(
			*databaseclaims.Lease,
		) ([]storecatalog.Spec, func() error, func(), error) {
			return []storecatalog.Spec{specs[0]}, func() error { return nil }, func() {}, nil
		}
		ops.exclusions = func([]storecatalog.Spec) (generationSet, error) {
			return generationSet{}, nil
		}
		ops.inspect = func(context.Context, string, time.Duration, func() error) (sqliteprovider.Inspection, error) {
			return sqliteprovider.Inspection{Exists: true}, nil
		}
		ops.revalidate = func(context.Context, sqliteprovider.Inspection) error { return nil }
		ops.classify = func(
			context.Context,
			storecatalog.Spec,
			databaseadapter.Contract,
			sqliteprovider.Inspection,
			error,
			generationSet,
			*legacyDiscoveryBudget,
		) database.StoreStatus {
			return database.StoreStatus{ID: specs[0].ID, Readiness: database.StoreReady}
		}
		ops.release = func(sqliteprovider.Inspection) error { return nil }
		return ops
	}

	t.Run("empty guarded catalog", func(t *testing.T) {
		ops := base()
		ops.guardStores = func(
			*databaseclaims.Lease,
		) ([]storecatalog.Spec, func() error, func(), error) {
			return nil, func() error { return nil }, func() {}, nil
		}
		if snapshot, err := probeWithOps(t.Context(), lease, registry, ops); snapshot != nil ||
			database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("empty catalog probe = %#v, %v", snapshot, err)
		}
	})

	t.Run("budget construction", func(t *testing.T) {
		ops := base()
		ops.newBudget = func(legacyDiscoveryLimits) (*legacyDiscoveryBudget, error) {
			return nil, canary
		}
		if snapshot, err := probeWithOps(t.Context(), lease, registry, ops); snapshot != nil ||
			!errors.Is(err, canary) {
			t.Fatalf("budget failure probe = %#v, %v", snapshot, err)
		}
	})

	t.Run("canceled after inspection", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		ops := base()
		releases := 0
		ops.inspect = func(context.Context, string, time.Duration, func() error) (sqliteprovider.Inspection, error) {
			cancel()
			return sqliteprovider.Inspection{}, nil
		}
		ops.release = func(sqliteprovider.Inspection) error { releases++; return nil }
		if snapshot, err := probeWithOps(ctx, lease, registry, ops); snapshot != nil ||
			!errors.Is(err, context.Canceled) || releases != 1 {
			t.Fatalf("post-inspection cancellation = %#v, %v; releases=%d", snapshot, err, releases)
		}
	})

	t.Run("canceled after classification", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		ops := base()
		releases := 0
		ops.classify = func(
			context.Context,
			storecatalog.Spec,
			databaseadapter.Contract,
			sqliteprovider.Inspection,
			error,
			generationSet,
			*legacyDiscoveryBudget,
		) database.StoreStatus {
			cancel()
			return database.StoreStatus{ID: specs[0].ID, Readiness: database.StoreReady}
		}
		ops.release = func(sqliteprovider.Inspection) error { releases++; return nil }
		if snapshot, err := probeWithOps(ctx, lease, registry, ops); snapshot != nil ||
			!errors.Is(err, context.Canceled) || releases != 1 {
			t.Fatalf("post-classification cancellation = %#v, %v; releases=%d", snapshot, err, releases)
		}
	})

	t.Run("inspection release failure", func(t *testing.T) {
		ops := base()
		ops.classify = func(
			context.Context,
			storecatalog.Spec,
			databaseadapter.Contract,
			sqliteprovider.Inspection,
			error,
			generationSet,
			*legacyDiscoveryBudget,
		) database.StoreStatus {
			return migrationStatus(specs[0].ID)
		}
		ops.release = func(sqliteprovider.Inspection) error { return canary }
		snapshot, err := probeWithOps(t.Context(), lease, registry, ops)
		if snapshot != nil || !errors.Is(err, canary) || database.CodeOf(err) != database.CodeUnavailable {
			t.Fatalf("release failure probe = %#v, %v", snapshot, err)
		}
	})

	t.Run("status validation failure", func(t *testing.T) {
		ops := base()
		ops.validate = func([]database.StoreStatus) ([]database.StoreStatus, error) {
			return nil, canary
		}
		if snapshot, err := probeWithOps(t.Context(), lease, registry, ops); snapshot != nil ||
			!errors.Is(err, canary) {
			t.Fatalf("validation failure probe = %#v, %v", snapshot, err)
		}
	})
}

func TestClassifyInspectionMapsLegacyAndHorizonFailures(t *testing.T) {
	spec := storecatalog.Spec{ID: "global/auth", Domain: "auth", LegacyRoots: []string{"legacy.json"}}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	status := classifyInspection(
		canceled,
		spec,
		databaseadapter.Contract{EmptyPolicy: databaseadapter.EmptyInitializeOnline},
		sqliteprovider.Inspection{},
		nil,
		generationSet{},
	)
	if status.Readiness != database.StoreUnavailable ||
		database.CodeOf(status.Error) != database.CodeUnavailable {
		t.Fatalf("canceled legacy classification = %#v", status)
	}

	path := filepath.Join(t.TempDir(), "horizon.db")
	createReadinessDatabase(t, path, 2, true, true)
	inspection, err := sqliteprovider.Inspect(t.Context(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inspection.Release() })
	status = classifyInspection(
		canceled,
		storecatalog.Spec{ID: "global/auth", Domain: "auth"},
		databaseadapter.Contract{CurrentVersion: 2, ImportHorizon: "auth"},
		inspection,
		nil,
		generationSet{},
	)
	if status.Readiness != database.StoreUnavailable ||
		database.CodeOf(status.Error) != database.CodeUnavailable {
		t.Fatalf("canceled import-horizon classification = %#v", status)
	}
}

func TestLegacyDiscoveryFailsClosedOnUninspectableRoot(t *testing.T) {
	if found, err := legacyInputExistsWithBudget(
		t.Context(), nil, generationSet{}, nil,
	); found || !errors.Is(err, errLegacyIntegrity) {
		t.Fatalf("nil legacy budget = %t, %v", found, err)
	}

	components := make([]string, 30)
	for index := range components {
		components[index] = strings.Repeat("x", 200)
	}
	root := filepath.Join(append([]string{t.TempDir()}, components...)...)
	if !validLegacyPath(root) {
		t.Fatal("fixture path was rejected before filesystem inspection")
	}
	if found, err := legacyInputExists(t.Context(), []string{root}, generationSet{}); found || err == nil {
		t.Fatalf("uninspectable legacy root = %t, %v", found, err)
	}
}

func TestGenerationExclusionsFailClosedAcrossIdentityTransitions(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "generation.db")
	otherPath := filepath.Join(root, "other.db")
	writeReadinessFile(t, path, []byte("generation"))
	writeReadinessFile(t, otherPath, []byte("other"))
	first, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	firstIdentity, _, err := fileidentity.Existing(path)
	if err != nil {
		t.Fatal(err)
	}
	otherIdentity, _, err := fileidentity.Existing(otherPath)
	if err != nil {
		t.Fatal(err)
	}
	canary := errors.New("generation identity canary")
	specs := []storecatalog.Spec{{ID: "global/auth", Path: path}}

	for _, test := range []struct {
		name   string
		mutate func(*generationExclusionOps)
		want   error
	}{
		{
			name: "identity lookup error",
			mutate: func(ops *generationExclusionOps) {
				ops.identity = func(string) (fileidentity.Identity, bool, error) {
					return fileidentity.Identity{}, false, canary
				}
			},
			want: canary,
		},
		{
			name: "identity disappears",
			mutate: func(ops *generationExclusionOps) {
				ops.identity = func(string) (fileidentity.Identity, bool, error) {
					return fileidentity.Identity{}, false, nil
				}
			},
			want: errors.New("database generation changed during identity lookup"),
		},
		{
			name: "reinspection error",
			mutate: func(ops *generationExclusionOps) {
				calls := 0
				ops.lstat = func(candidate string) (os.FileInfo, error) {
					if candidate != path {
						return os.Lstat(candidate)
					}
					calls++
					if calls == 1 {
						return first, nil
					}
					return nil, canary
				}
			},
			want: canary,
		},
		{
			name: "identity changes",
			mutate: func(ops *generationExclusionOps) {
				calls := 0
				ops.identity = func(candidate string) (fileidentity.Identity, bool, error) {
					calls++
					if calls == 1 {
						return firstIdentity, true, nil
					}
					return otherIdentity, true, nil
				}
			},
			want: errors.New("database generation changed during identity lookup"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ops := generationExclusionOps{lstat: os.Lstat, identity: fileidentity.Existing}
			test.mutate(&ops)
			set, err := generationExclusionsWithOps(specs, ops)
			if len(set.paths) != 0 || len(set.physical) != 0 || err == nil ||
				!strings.Contains(err.Error(), test.want.Error()) {
				t.Fatalf("generationExclusionsWithOps() = %#v, %v; want %v", set, err, test.want)
			}
		})
	}
}

func TestLegacyFileExclusionRejectsUnsafeAndChangedIdentity(t *testing.T) {
	if excluded, err := excludedLegacyFile("missing", generationSet{}); excluded ||
		!errors.Is(err, errLegacyIntegrity) {
		t.Fatalf("nil legacy identity = %t, %v", excluded, err)
	}

	root := t.TempDir()
	first := filepath.Join(root, "first.json")
	second := filepath.Join(root, "second.json")
	writeReadinessFile(t, first, []byte("first"))
	writeReadinessFile(t, second, []byte("second"))
	firstIdentity, _, err := fileidentity.Existing(first)
	if err != nil {
		t.Fatal(err)
	}
	secondIdentity, _, err := fileidentity.Existing(second)
	if err != nil {
		t.Fatal(err)
	}
	if excluded, err := excludedLegacyFile(
		second,
		generationSet{physical: map[fileidentity.Identity]struct{}{secondIdentity: {}}},
	); excluded ||
		!errors.Is(err, errLegacyIntegrity) {
		t.Fatalf("physical legacy alias = %t, %v", excluded, err)
	}
	_ = firstIdentity
}

func TestLegacyDirectoryRejectsInvalidAndDisappearingEntries(t *testing.T) {
	t.Run("invalid component", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, string([]byte{'b', 'a', 'd', 0xff}))
		if err := os.WriteFile(path, []byte("legacy"), 0o600); err != nil {
			t.Skipf("filesystem rejected invalid UTF-8 fixture: %v", err)
		}
		found, err := legacyInputExists(t.Context(), []string{root}, generationSet{})
		if found || !errors.Is(err, errLegacyIntegrity) {
			t.Fatalf("invalid legacy component = %t, %v", found, err)
		}
	})

	t.Run("entry disappears", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "legacy.json")
		writeReadinessFile(t, path, []byte("legacy"))
		ctx := &readinessActionContext{
			Context: context.Background(),
			allowed: 3,
			action:  func() { _ = os.Remove(path) },
		}
		found, err := legacyInputExists(ctx, []string{root}, generationSet{})
		if found || err == nil {
			t.Fatalf("disappearing legacy entry = %t, %v", found, err)
		}
	})
}

func TestLegacyDirectoryRejectsMetadataChangeDuringWalk(t *testing.T) {
	root := t.TempDir()
	writeReadinessFile(t, filepath.Join(root, "legacy.json"), []byte("legacy"))
	ctx := &readinessActionContext{
		Context: context.Background(),
		allowed: 3,
		action: func() {
			when := time.Now().Add(-time.Hour)
			_ = os.Chtimes(root, when, when)
		},
	}
	found, err := legacyInputExists(ctx, []string{root}, generationSet{})
	if found || !errors.Is(err, errLegacyIntegrity) {
		t.Fatalf("changed legacy directory = %t, %v", found, err)
	}
}
