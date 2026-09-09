//nolint:govet // Independent failure-boundary assertions intentionally reuse narrow error names.
package sqliteprovider

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestCoverageProviderInspectionQueriesAndLifecycle(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "inspection.db")
	database, err := OpenStore(path, 250*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := Configure(t.Context(), database, 250*time.Millisecond, false); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`
		CREATE TABLE storage_import_horizons(
			component TEXT PRIMARY KEY,
			completed_at INTEGER NOT NULL
		) STRICT;
		CREATE TABLE item(id INTEGER PRIMARY KEY, value TEXT NOT NULL) STRICT;
		CREATE UNIQUE INDEX item_value_idx ON item(value);
		INSERT INTO storage_import_horizons(component, completed_at) VALUES ('inspection', 1);
		PRAGMA user_version = 3;
	`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	inspection, err := Inspect(t.Context(), path, 250*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !inspection.Exists || inspection.Empty || inspection.Version != 3 {
		t.Fatalf("inspection = %#v", inspection)
	}
	if ready, err := inspection.HasSchemaObjects(t.Context(), "table", "item"); err != nil || !ready {
		t.Fatalf("schema objects ready=%v err=%v", ready, err)
	}
	if ready, err := inspection.HasSchemaObjects(t.Context(), "index", "item_value_idx"); err != nil || !ready {
		t.Fatalf("schema index ready=%v err=%v", ready, err)
	}
	if ready, err := inspection.HasSchemaObjects(t.Context(), "table", "missing"); err != nil || ready {
		t.Fatalf("missing schema object ready=%v err=%v", ready, err)
	}
	if ready, err := inspection.HasTableColumns(t.Context(), "item", "id", "value"); err != nil || !ready {
		t.Fatalf("table columns ready=%v err=%v", ready, err)
	}
	if ready, err := inspection.HasTableColumns(t.Context(), "item", "missing"); err != nil || ready {
		t.Fatalf("missing table column ready=%v err=%v", ready, err)
	}
	if ready, err := inspection.HasImportHorizon(t.Context(), "inspection"); err != nil || !ready {
		t.Fatalf("import horizon ready=%v err=%v", ready, err)
	}
	if ready, err := inspection.HasImportHorizon(t.Context(), "missing"); err != nil || ready {
		t.Fatalf("missing import horizon ready=%v err=%v", ready, err)
	}

	if err := inspection.Release(); err != nil {
		t.Fatal(err)
	}
	if err := inspection.Release(); err != nil {
		t.Fatalf("repeat release = %v", err)
	}
	if err := (Inspection{}).Release(); err != nil {
		t.Fatalf("empty release = %v", err)
	}
	if _, err := (Inspection{}).HasSchemaObjects(t.Context(), "table", "item"); err == nil {
		t.Fatal("empty inspection accepted schema query")
	}
	if _, err := (Inspection{}).HasTableColumns(t.Context(), "item", "id"); err == nil {
		t.Fatal("empty inspection accepted column query")
	}
	if _, err := (Inspection{}).HasImportHorizon(t.Context(), "item"); err == nil {
		t.Fatal("empty inspection accepted horizon query")
	}
}

func TestCoverageProviderInspectionRejectsUnsafeGenerations(t *testing.T) {
	if IsInspectionIntegrity(errors.New("ordinary")) {
		t.Fatal("ordinary error classified as inspection integrity")
	}

	t.Run("missing", func(t *testing.T) {
		inspection, err := Inspect(t.Context(), filepath.Join(t.TempDir(), "missing.db"), time.Second)
		if err != nil || inspection.Exists {
			t.Fatalf("missing inspection = %#v, %v", inspection, err)
		}
	})
	t.Run("orphan sidecar", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing.db")
		if err := os.WriteFile(path+"-wal", []byte("orphan"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Inspect(t.Context(), path, time.Second); !IsInspectionIntegrity(err) {
			t.Fatalf("orphan sidecar error = %v", err)
		}
	})
	t.Run("directory endpoint", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "store.db")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := Inspect(t.Context(), path, time.Second); !IsInspectionIntegrity(err) {
			t.Fatalf("directory endpoint error = %v", err)
		}
	})
	t.Run("corrupt bytes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "store.db")
		if err := os.WriteFile(path, []byte("not sqlite"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Inspect(t.Context(), path, time.Second); !IsInspectionIntegrity(err) {
			t.Fatalf("corrupt inspection error = %v", err)
		}
	})
	t.Run("canceled", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "store.db")
		database, err := OpenStore(path, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := Inspect(ctx, path, time.Second); err == nil {
			t.Fatal("canceled inspection succeeded")
		}
	})
}

func TestCoverageInspectedPoolIdentityAndCleanup(t *testing.T) {
	root := t.TempDir()
	firstPath := filepath.Join(root, "first.db")
	secondPath := filepath.Join(root, "second.db")
	if err := os.WriteFile(firstPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, firstKey, err := canonicalInspectionLocation(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	_, secondKey, err := canonicalInspectionLocation(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	firstGeneration, err := captureInspectedGeneration(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	secondGeneration, err := captureInspectedGeneration(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	owner1 := new(atomic.Bool)
	owner2 := new(atomic.Bool)
	owner3 := new(atomic.Bool)
	if _, err := retainInspectedPool(firstKey, nil, firstGeneration, time.Second, owner1); err == nil {
		t.Fatal("retained nil pool")
	}
	database, err := OpenStore(":memory:", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := retainInspectedPool(firstKey, database, inspectedGeneration{}, time.Second, owner1); err == nil {
		t.Fatal("retained pool without identity")
	}
	if retained, err := retainInspectedPool(
		firstKey, database, firstGeneration, time.Second, owner1,
	); err != nil || retained != database {
		t.Fatalf("retain = %p, %v", retained, err)
	}
	if retained, err := inspectedPoolFor(
		firstKey, firstGeneration, time.Second, owner2,
	); err != nil || retained != database {
		t.Fatalf("lookup = %p, %v", retained, err)
	}
	if _, err := inspectedPoolFor(firstKey, secondGeneration, time.Second, new(atomic.Bool)); err == nil {
		t.Fatal("lookup accepted changed identity")
	}
	duplicate, err := OpenStore(":memory:", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if retained, err := retainInspectedPool(
		firstKey, duplicate, firstGeneration, time.Second, owner3,
	); err != nil || retained != database {
		t.Fatalf("duplicate retain = %p, %v", retained, err)
	}
	conflicting, err := OpenStore(":memory:", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := retainInspectedPool(
		firstKey, conflicting, firstGeneration, 2*time.Second, new(atomic.Bool),
	); err == nil {
		t.Fatal("duplicate retain accepted conflicting timeout")
	}
	if err := conflicting.Ping(); err == nil {
		t.Fatal("conflicting duplicate pool remained open")
	}
	changed, err := OpenStore(":memory:", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := retainInspectedPool(
		firstKey, changed, secondGeneration, time.Second, new(atomic.Bool),
	); err == nil {
		t.Fatal("duplicate retain accepted changed identity")
	}
	if err := releaseInspectedPool(firstKey, changed, new(atomic.Bool)); err != nil {
		t.Fatalf("mismatched release = %v", err)
	}
	for _, owner := range []*atomic.Bool{owner1, owner2, owner3} {
		if err := releaseInspectedPool(firstKey, database, owner); err != nil {
			t.Fatal(err)
		}
	}
	if retained, err := inspectedPoolFor(
		firstKey, firstGeneration, time.Second, new(atomic.Bool),
	); err != nil || retained != nil {
		t.Fatalf("post-close lookup = %p, %v", retained, err)
	}
	if adopted, err := adoptInspectedPool(
		secondPath, secondKey, new(atomic.Bool),
	); err == nil || adopted != nil {
		t.Fatalf("missing adoption = %p, %v", adopted, err)
	}
}

func TestCoverageInspectionPoolAdoptionFailurePhases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retained.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	generation, err := captureInspectedGeneration(path)
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := canonicalInspectionLocation(path)
	if err != nil {
		t.Fatal(err)
	}
	canary := errors.New("adoption canary")
	retain := func(t *testing.T, references int) (*sql.DB, []*atomic.Bool) {
		t.Helper()
		database, err := OpenStore(":memory:", time.Second)
		if err != nil {
			t.Fatal(err)
		}
		inspectedPools.Lock()
		owners := make([]*atomic.Bool, references)
		ownerSet := make(map[*atomic.Bool]struct{}, references)
		for index := range owners {
			owners[index] = new(atomic.Bool)
			ownerSet[owners[index]] = struct{}{}
		}
		inspectedPools.values[key] = inspectedPool{
			database: database, generation: generation,
			busyTimeout: time.Second, owners: ownerSet,
		}
		inspectedPools.Unlock()
		return database, owners
	}
	noopDirectory := func(string) error { return nil }
	noopSecure := func(string) error { return nil }
	for _, test := range []struct {
		name       string
		references int
		directory  func(string) error
		secure     func(string) error
		capture    func(string) (inspectedGeneration, error)
	}{
		{
			name: "references", references: 2,
			directory: noopDirectory, secure: noopSecure, capture: captureInspectedGeneration,
		},
		{
			name: "capture", references: 1,
			directory: noopDirectory, secure: noopSecure,
			capture: func(string) (inspectedGeneration, error) { return inspectedGeneration{}, canary },
		},
		{
			name: "directory", references: 1,
			directory: func(string) error { return canary }, secure: noopSecure,
			capture: captureInspectedGeneration,
		},
		{
			name: "generation", references: 1,
			directory: noopDirectory, secure: func(string) error { return canary },
			capture: captureInspectedGeneration,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			database, owners := retain(t, test.references)
			adopted, err := adoptInspectedPoolWithGeneration(
				path, key, owners[0], test.directory, test.secure, test.capture,
			)
			if err == nil || adopted != nil {
				t.Fatalf("failed adoption = %#v, %v", adopted, err)
			}
			if err := database.Ping(); err != nil {
				t.Fatalf("failed adoption invalidated readiness pool: %v", err)
			}
			for _, owner := range owners {
				if err := releaseInspectedPool(key, database, owner); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	database, owners := retain(t, 1)
	adopted, err := adoptInspectedPoolWithGeneration(
		path, key, owners[0], noopDirectory, noopSecure, captureInspectedGeneration,
	)
	if err != nil || adopted != database {
		t.Fatalf("successful adoption = %#v, %v", adopted, err)
	}
	if err := adopted.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCoverageInspectionPoolReleaseKeyFailure(t *testing.T) {
	if err := releaseInspectedPool("", nil, new(atomic.Bool)); err != nil {
		t.Fatalf("empty-key release = %v", err)
	}
}

func TestCoverageProviderInspectionQueryFailures(t *testing.T) {
	canary := errors.New("inspection query canary")
	for _, test := range []struct {
		name string
		run  func(Inspection) error
	}{
		{name: "schema", run: func(inspection Inspection) error {
			_, err := inspection.HasSchemaObjects(t.Context(), "table", "item")
			return err
		}},
		{name: "columns", run: func(inspection Inspection) error {
			_, err := inspection.HasTableColumns(t.Context(), "item", "id")
			return err
		}},
		{name: "horizon", run: func(inspection Inspection) error {
			_, err := inspection.HasImportHorizon(t.Context(), "item")
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			inspection := Inspection{database: openProviderScript(t,
				providerScriptStep{query: "COUNT", err: canary}), released: new(atomic.Bool)}
			if err := test.run(inspection); !errors.Is(err, canary) {
				t.Fatalf("inspection query error = %v", err)
			}
		})
	}

	path := filepath.Join(t.TempDir(), "store.db")
	database, err := OpenStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("CREATE TABLE item(id INTEGER PRIMARY KEY, value TEXT)"); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCoverageInspectionDatabasePhases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inspection.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	generation, err := captureInspectedGeneration(path)
	if err != nil {
		t.Fatal(err)
	}
	canary := errors.New("inspection phase canary")
	if _, _, _, err := inspectDatabase(
		t.Context(), path, generation, openConfiguredProviderScript(t, canary),
	); IsInspectionIntegrity(err) || !errors.Is(err, errInspectionUnavailable) {
		t.Fatalf("ping failure = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err := inspectDatabase(
		canceled, path, generation, openProviderScript(t),
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ping = %v", err)
	}
	other := filepath.Join(t.TempDir(), "other.db")
	if err := os.WriteFile(other, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	otherGeneration, err := captureInspectedGeneration(other)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := inspectDatabase(
		t.Context(), path, otherGeneration, openProviderScript(t),
	); !IsInspectionIntegrity(err) {
		t.Fatalf("identity failure = %v", err)
	}
	if _, _, _, err := inspectDatabaseWithGeneration(
		t.Context(), path, generation, openProviderScript(t),
		func(string) (inspectedGeneration, error) { return inspectedGeneration{}, canary },
		func(string) error { return nil },
	); !errors.Is(err, canary) || IsInspectionIntegrity(err) {
		t.Fatalf("stat failure = %v", err)
	}
	if _, _, _, err := inspectDatabaseWithGeneration(
		t.Context(), path, generation, openProviderScript(t),
		func(string) (inspectedGeneration, error) { return generation, nil },
		func(string) error { return canary },
	); !errors.Is(err, canary) || IsInspectionIntegrity(err) {
		t.Fatalf("generation security failure = %v", err)
	}
	busy := func(error) bool { return true }
	if _, _, _, err := inspectDatabaseWithGeneration(
		t.Context(), path, generation, openConfiguredProviderScript(t, canary),
		func(string) (inspectedGeneration, error) { return generation, nil },
		func(string) error { return nil }, busy,
	); !errors.Is(err, canary) {
		t.Fatalf("busy ping = %v", err)
	}
	steps := append(providerIntegrityOKSteps(), providerScriptStep{query: "user_version", err: canary})
	if _, _, _, err := inspectDatabaseWithGeneration(
		t.Context(), path, generation, openProviderScript(t, steps...),
		func(string) (inspectedGeneration, error) { return generation, nil },
		func(string) error { return nil }, busy,
	); !errors.Is(err, canary) {
		t.Fatalf("busy version = %v", err)
	}
	steps = append(providerIntegrityOKSteps(), providerRow("user_version", int64(4)),
		providerScriptStep{query: "sqlite_schema", err: canary})
	if _, _, _, err := inspectDatabaseWithGeneration(
		t.Context(), path, generation, openProviderScript(t, steps...),
		func(string) (inspectedGeneration, error) { return generation, nil },
		func(string) error { return nil }, busy,
	); !errors.Is(err, canary) {
		t.Fatalf("busy catalog = %v", err)
	}
	for _, phase := range []struct {
		name  string
		steps []providerScriptStep
	}{
		{name: "integrity", steps: []providerScriptStep{{query: "integrity_check", err: canary}}},
		{name: "version", steps: append(providerIntegrityOKSteps(),
			providerScriptStep{query: "user_version", err: canary})},
		{name: "catalog", steps: append(providerIntegrityOKSteps(),
			providerRow("user_version", int64(4)),
			providerScriptStep{query: "sqlite_schema", err: canary})},
	} {
		t.Run("canceled "+phase.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, _, _, err := inspectDatabaseWithGeneration(
				ctx, path, generation, openProviderScript(t, phase.steps...),
				func(string) (inspectedGeneration, error) { return generation, nil },
				func(string) error { return nil }, func(error) bool { return false },
			); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled phase error = %v", err)
			}
		})
	}
	if err := os.Link(path, path+".alias"); err == nil {
		if _, _, _, err := inspectDatabase(
			t.Context(), path, generation, openProviderScript(t),
		); !IsInspectionIntegrity(err) {
			t.Fatalf("security failure = %v", err)
		}
		if err := os.Remove(path + ".alias"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, _, err := inspectDatabase(t.Context(), path, generation, openProviderScript(t,
		providerScriptStep{query: "integrity_check", err: canary},
	)); IsInspectionIntegrity(err) || !errors.Is(err, errInspectionUnavailable) {
		t.Fatalf("integrity failure = %v", err)
	}
	steps = append(providerIntegrityOKSteps(), providerScriptStep{query: "user_version", err: canary})
	if _, _, _, err := inspectDatabase(
		t.Context(), path, generation, openProviderScript(t, steps...),
	); IsInspectionIntegrity(err) || !errors.Is(err, errInspectionUnavailable) {
		t.Fatalf("version failure = %v", err)
	}
	steps = append(providerIntegrityOKSteps(), providerRow("user_version", int64(4)),
		providerScriptStep{query: "sqlite_schema", err: canary})
	if _, _, _, err := inspectDatabase(
		t.Context(), path, generation, openProviderScript(t, steps...),
	); IsInspectionIntegrity(err) || !errors.Is(err, errInspectionUnavailable) {
		t.Fatalf("catalog failure = %v", err)
	}
	steps = append(providerIntegrityOKSteps(), providerRow("user_version", int64(4)),
		providerRow("sqlite_schema", int64(2)))
	current, version, objects, err := inspectDatabase(
		t.Context(), path, generation, openProviderScript(t, steps...),
	)
	if err != nil || !current.members[0].present || version != 4 || objects != 2 {
		t.Fatalf("inspection result = %#v, %d, %d, %v", current, version, objects, err)
	}
}
