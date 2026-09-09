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

func TestInspectedGenerationPinsEveryStableIdentityAndPresence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	wal := path + "-wal"
	if err := os.WriteFile(wal, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := captureInspectedGeneration(path)
	if err != nil {
		t.Fatal(err)
	}
	if !first.members[0].present || !first.members[1].present || first.members[2].present {
		t.Fatalf("first generation presence = %#v", first)
	}
	if renameErr := os.Rename(wal, wal+".old"); renameErr != nil {
		t.Fatal(renameErr)
	}
	if writeErr := os.WriteFile(wal, []byte("second"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	second, err := captureInspectedGeneration(path)
	if err != nil {
		t.Fatal(err)
	}
	if !sameInspectedMain(first, second) || sameInspectedGeneration(first, second) ||
		first.members[1].identity == second.members[1].identity {
		t.Fatalf("sidecar replacement was not identity-visible: first=%#v second=%#v", first, second)
	}
	if removeErr := os.Remove(wal); removeErr != nil {
		t.Fatal(removeErr)
	}
	withoutWAL, err := captureInspectedGeneration(path)
	if err != nil {
		t.Fatal(err)
	}
	if sameInspectedGeneration(second, withoutWAL) || withoutWAL.members[1].present {
		t.Fatalf("sidecar presence transition was not visible: %#v", withoutWAL)
	}
}

func TestInspectionCapturesAbsoluteKeyOnceAcrossWorkingDirectoryChange(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })
	root := t.TempDir()
	path := filepath.Join(root, "store.db")
	database, err := OpenStore(path, 25*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if closeErr := database.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if chdirErr := os.Chdir(root); chdirErr != nil {
		t.Fatal(chdirErr)
	}
	inspection, err := Inspect(t.Context(), "store.db", 25*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.path != path || inspection.key == "" {
		t.Fatalf("inspection location = %q, %q", inspection.path, inspection.key)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := inspection.Release(); err != nil {
		t.Fatal(err)
	}
	inspectedPools.Lock()
	_, leaked := inspectedPools.values[inspection.key]
	inspectedPools.Unlock()
	if leaked {
		t.Fatal("release recomputed a relative key and leaked the retained pool")
	}
	if err := inspection.database.Ping(); err == nil {
		t.Fatal("released inspection database remained open")
	}
}

func TestInspectBoundsDatabaseAndPostRetainPhases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := 5 * time.Millisecond

	t.Run("database inspection", func(t *testing.T) {
		candidate := openProviderScript(t)
		ops := defaultInspectionOps()
		ops.dsn = func(string, time.Duration) (string, error) { return "script", nil }
		ops.open = func(string) (*sql.DB, error) { return candidate, nil }
		ops.inspectDatabase = func(
			ctx context.Context,
			_ string,
			_ inspectedGeneration,
			_ *sql.DB,
		) (inspectedGeneration, int, int, error) {
			<-ctx.Done()
			return inspectedGeneration{}, 0, 0, ctx.Err()
		}
		started := time.Now()
		inspection, err := inspectWithOps(t.Context(), path, deadline, ops)
		if inspection.Exists || !errors.Is(err, context.DeadlineExceeded) ||
			time.Since(started) > time.Second {
			t.Fatalf("bounded inspection = %#v, %v after %s", inspection, err, time.Since(started))
		}
		if err := candidate.Ping(); err == nil {
			t.Fatal("timed-out inspection candidate remained open")
		}
	})

	t.Run("coalesced retain", func(t *testing.T) {
		shared := openProviderScript(t)
		candidate := openProviderScript(t)
		generation, err := captureInspectedGeneration(path)
		if err != nil {
			t.Fatal(err)
		}
		ops := defaultInspectionOps()
		ops.dsn = func(string, time.Duration) (string, error) { return "script", nil }
		ops.open = func(string) (*sql.DB, error) { return candidate, nil }
		ops.inspectDatabase = func(
			context.Context,
			string,
			inspectedGeneration,
			*sql.DB,
		) (inspectedGeneration, int, int, error) {
			return generation, 0, 0, nil
		}
		ops.retain = func(
			string,
			*sql.DB,
			inspectedGeneration,
			time.Duration,
			*atomic.Bool,
		) (*sql.DB, error) {
			time.Sleep(4 * deadline)
			return shared, nil
		}
		releases := 0
		ops.release = func(string, *sql.DB, *atomic.Bool) error {
			releases++
			return nil
		}
		inspection, err := inspectWithOps(t.Context(), path, deadline, ops)
		if inspection.Exists || !errors.Is(err, context.DeadlineExceeded) || releases != 1 {
			t.Fatalf("post-retain timeout = %#v, %v; releases=%d", inspection, err, releases)
		}
		if err := shared.Ping(); err != nil {
			t.Fatalf("shared retained pool was closed instead of released: %v", err)
		}
	})
}

func TestRetainInspectedPoolPropagatesCandidateCloseFailures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
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
	existing := openProviderScript(t)
	originalOwner := new(atomic.Bool)
	if retained, err := retainInspectedPool(
		key, existing, generation, time.Second, originalOwner,
	); err != nil || retained != existing {
		t.Fatalf("seed retained pool = %p, %v", retained, err)
	}

	closeErr := errors.New("candidate close canary")
	candidate := openConfiguredProviderScriptWithCloseError(t, nil, closeErr)
	if err := candidate.Ping(); err != nil {
		t.Fatal(err)
	}
	if retained, err := retainInspectedPool(
		key, candidate, generation, time.Second, new(atomic.Bool),
	); retained != nil || !errors.Is(err, closeErr) {
		t.Fatalf("coalesced close failure = %p, %v", retained, err)
	}
	inspectedPools.Lock()
	entry := inspectedPools.values[key]
	inspectedPools.Unlock()
	if entry.database != existing || len(entry.owners) != 1 {
		t.Fatalf("failed coalescing mutated existing owners: %#v", entry)
	}
	if err := releaseInspectedPool(key, existing, originalOwner); err != nil {
		t.Fatal(err)
	}
}

func TestInspectionQueriesRejectUnboundedAndMalformedContracts(t *testing.T) {
	inspection := Inspection{database: openProviderScript(t), released: new(atomic.Bool)}
	tooMany := make([]string, maxInspectionSchemaNames+1)
	for index := range tooMany {
		tooMany[index] = "column"
	}
	for _, test := range []struct {
		name string
		run  func() (bool, error)
	}{
		{name: "object type", run: func() (bool, error) {
			return inspection.HasSchemaObjects(t.Context(), "virtual", "items")
		}},
		{name: "empty objects", run: func() (bool, error) {
			return inspection.HasSchemaObjects(t.Context(), "table")
		}},
		{name: "empty object name", run: func() (bool, error) {
			return inspection.HasSchemaObjects(t.Context(), "table", "")
		}},
		{name: "duplicate object", run: func() (bool, error) {
			return inspection.HasSchemaObjects(t.Context(), "table", "items", "items")
		}},
		{name: "empty columns", run: func() (bool, error) {
			return inspection.HasTableColumns(t.Context(), "items")
		}},
		{name: "unbounded columns", run: func() (bool, error) {
			return inspection.HasTableColumns(t.Context(), "items", tooMany...)
		}},
		{name: "empty horizon", run: func() (bool, error) {
			return inspection.HasImportHorizon(t.Context(), "")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if ready, err := test.run(); ready || err == nil {
				t.Fatalf("invalid inspection query = %t, %v", ready, err)
			}
		})
	}

	malformed := Inspection{
		database: openProviderScript(t,
			providerRow("sqlite_schema", int64(1)),
			providerRow("pragma_table_xinfo", int64(2), int64(1), int64(0)),
		),
		released: new(atomic.Bool),
	}
	if ready, err := malformed.HasImportHorizon(t.Context(), "auth"); ready ||
		!IsInspectionIntegrity(err) {
		t.Fatalf("malformed horizon = %t, %v", ready, err)
	}
}

func TestImportHorizonRejectsGeneratedHiddenColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "horizon.db")
	database, err := OpenStore(path, 25*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if configureErr := Configure(t.Context(), database, 25*time.Millisecond, false); configureErr != nil {
		_ = database.Close()
		t.Fatal(configureErr)
	}
	if _, execErr := database.Exec(`
		CREATE TABLE storage_import_horizons (
			component TEXT PRIMARY KEY,
			completed_at INTEGER NOT NULL,
			hidden_completion INTEGER GENERATED ALWAYS AS (completed_at) VIRTUAL
		) STRICT;
		INSERT INTO storage_import_horizons(component, completed_at) VALUES ('auth', 1);
	`); execErr != nil {
		_ = database.Close()
		t.Fatal(execErr)
	}
	if closeErr := database.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	inspection, err := Inspect(t.Context(), path, 25*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inspection.Release() })
	if ready, horizonErr := inspection.HasImportHorizon(t.Context(), "auth"); ready ||
		!IsInspectionIntegrity(horizonErr) {
		t.Fatalf("generated-column horizon = %t, %v", ready, horizonErr)
	}
}

func TestInspectClassifiesOnlyProvenUnsafeAncestorsAsIntegrity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	canary := errors.New("ancestor I/O canary")
	for _, test := range []struct {
		name      string
		err       error
		integrity bool
	}{
		{name: "I/O", err: canary},
		{name: "unsafe", err: errors.Join(errProviderUnsafeBoundary, canary), integrity: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ops := defaultInspectionOps()
			ops.validateAncestors = func(string) error { return test.err }
			inspection, err := inspectWithOps(t.Context(), path, time.Second, ops)
			if inspection.Exists || !errors.Is(err, canary) ||
				IsInspectionIntegrity(err) != test.integrity {
				t.Fatalf("ancestor failure = %#v, %v", inspection, err)
			}
		})
	}
}

func TestInspectClaimedReconcilesBeforeRetainAndCleanup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	generation, err := captureInspectedGeneration(path)
	if err != nil {
		t.Fatal(err)
	}
	candidate := openProviderScript(t)
	canary := errors.New("retain canary")
	events := make([]string, 0, 3)
	ops := defaultInspectionOps()
	ops.dsn = func(string, time.Duration) (string, error) { return "script", nil }
	ops.open = func(string) (*sql.DB, error) { return candidate, nil }
	ops.inspectDatabase = func(
		context.Context,
		string,
		inspectedGeneration,
		*sql.DB,
	) (inspectedGeneration, int, int, error) {
		return generation, 0, 0, nil
	}
	ops.reconcile = func() error {
		events = append(events, "reconcile")
		return nil
	}
	ops.retain = func(
		string,
		*sql.DB,
		inspectedGeneration,
		time.Duration,
		*atomic.Bool,
	) (*sql.DB, error) {
		events = append(events, "retain")
		return nil, canary
	}
	inspection, err := inspectWithOps(t.Context(), path, time.Second, ops)
	if inspection.Exists || !errors.Is(err, canary) {
		t.Fatalf("InspectClaimed retain failure = %#v, %v", inspection, err)
	}
	if len(events) != 2 || events[0] != "reconcile" || events[1] != "retain" {
		t.Fatalf("claim/retain events = %#v", events)
	}
	if err := candidate.Ping(); err == nil {
		t.Fatal("failed retained candidate was not cleaned up")
	}
}

func TestInspectMissingStateRequiresTwoStableCompletePasses(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "missing.db")
	present := filepath.Join(root, "present")
	if err := os.WriteFile(present, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(present)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{path, path + "-wal"} {
		t.Run(filepath.Base(target), func(t *testing.T) {
			ops := defaultInspectionOps()
			calls := 0
			ops.lstat = func(candidate string) (os.FileInfo, error) {
				if candidate == target {
					calls++
					if calls > 1 {
						return info, nil
					}
				}
				return nil, os.ErrNotExist
			}
			inspection, err := inspectWithOps(t.Context(), path, time.Second, ops)
			if inspection.Exists || !IsInspectionIntegrity(err) {
				t.Fatalf("transitioning absence = %#v, %v", inspection, err)
			}
		})
	}
}

func TestInspectReusedPoolRejectsCompleteSidecarDrift(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := captureInspectedGeneration(path)
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(path+"-wal", nil, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	after, err := captureInspectedGeneration(path)
	if err != nil {
		t.Fatal(err)
	}
	database := openProviderScript(t)
	releases := 0
	ops := defaultInspectionOps()
	ops.captureGeneration = func(string) (inspectedGeneration, error) { return before, nil }
	ops.poolFor = func(
		string,
		inspectedGeneration,
		time.Duration,
		*atomic.Bool,
	) (*sql.DB, error) {
		return database, nil
	}
	ops.inspectDatabase = func(
		context.Context,
		string,
		inspectedGeneration,
		*sql.DB,
	) (inspectedGeneration, int, int, error) {
		return after, 0, 0, nil
	}
	ops.release = func(string, *sql.DB, *atomic.Bool) error {
		releases++
		return nil
	}
	inspection, err := inspectWithOps(t.Context(), path, time.Second, ops)
	if inspection.Exists || !IsInspectionIntegrity(err) || releases != 1 {
		t.Fatalf("reused sidecar drift = %#v, %v; releases=%d", inspection, err, releases)
	}
}
