//nolint:govet,golines // Dense failure-boundary fixtures intentionally use narrow scopes.
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

func providerIntegrityOKSteps() []providerScriptStep {
	return []providerScriptStep{
		providerRow("integrity_check", "ok"),
		{query: "foreign_key_check", columns: []string{"table"}},
	}
}

func TestInspectionAdoptTransfersOnlyExactLiveOwner(t *testing.T) {
	if database, err := (Inspection{}).Adopt(); database != nil || err == nil {
		t.Fatalf("empty Inspection.Adopt() = %#v, %v", database, err)
	}

	path := filepath.Join(t.TempDir(), "adopt.db")
	database, err := OpenStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := Configure(t.Context(), database, time.Second, false); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	inspection, err := Inspect(t.Context(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	adopted, err := inspection.Adopt()
	if err != nil || adopted == nil {
		t.Fatalf("Inspection.Adopt() = %#v, %v", adopted, err)
	}
	if err := adopted.PingContext(t.Context()); err != nil {
		t.Fatalf("adopted database is unavailable: %v", err)
	}
	if repeat, err := inspection.Adopt(); repeat != nil || err == nil {
		t.Fatalf("repeat Inspection.Adopt() = %#v, %v", repeat, err)
	}
	if err := inspection.Release(); err != nil {
		t.Fatalf("release after adoption = %v", err)
	}
	if err := adopted.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInspectFailsClosedAcrossInjectedTransitions(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "inspection.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	canary := errors.New("inspection transition canary")

	t.Run("orphan sidecar inspection error", func(t *testing.T) {
		missing := filepath.Join(root, "missing.db")
		ops := defaultInspectionOps()
		ops.lstat = func(candidate string) (os.FileInfo, error) {
			if candidate == missing {
				return nil, os.ErrNotExist
			}
			return nil, canary
		}
		if inspection, err := inspectWithOps(t.Context(), missing, time.Second, ops); inspection.Exists || !errors.Is(err, canary) {
			t.Fatalf("sidecar inspection failure = %#v, %v", inspection, err)
		}
	})

	t.Run("directory security", func(t *testing.T) {
		ops := defaultInspectionOps()
		ops.ensureDirectory = func(string) error { return canary }
		if inspection, err := inspectWithOps(t.Context(), path, time.Second, ops); inspection.Exists || !errors.Is(err, canary) {
			t.Fatalf("directory security failure = %#v, %v", inspection, err)
		}
	})

	t.Run("dsn", func(t *testing.T) {
		ops := defaultInspectionOps()
		ops.dsn = func(string, time.Duration) (string, error) { return "", canary }
		if inspection, err := inspectWithOps(t.Context(), path, time.Second, ops); inspection.Exists || !errors.Is(err, canary) {
			t.Fatalf("DSN failure = %#v, %v", inspection, err)
		}
	})

	t.Run("database open", func(t *testing.T) {
		ops := defaultInspectionOps()
		ops.dsn = func(string, time.Duration) (string, error) { return "script", nil }
		ops.open = func(string) (*sql.DB, error) { return nil, canary }
		if inspection, err := inspectWithOps(t.Context(), path, time.Second, ops); inspection.Exists || !errors.Is(err, canary) {
			t.Fatalf("open failure = %#v, %v", inspection, err)
		}
	})

	t.Run("reused pool cleanup", func(t *testing.T) {
		database := openProviderScript(t)
		ops := defaultInspectionOps()
		ops.poolFor = func(string, inspectedGeneration, time.Duration, *atomic.Bool) (*sql.DB, error) {
			return database, nil
		}
		ops.inspectDatabase = func(
			context.Context, string, inspectedGeneration, *sql.DB,
		) (inspectedGeneration, int, int, error) {
			return inspectedGeneration{}, 0, 0, canary
		}
		releases := 0
		ops.release = func(string, *sql.DB, *atomic.Bool) error {
			releases++
			return nil
		}
		if inspection, err := inspectWithOps(t.Context(), path, time.Second, ops); inspection.Exists || !errors.Is(err, canary) || releases != 1 {
			t.Fatalf("reused-pool failure = %#v, %v; releases=%d", inspection, err, releases)
		}
	})

	t.Run("retain", func(t *testing.T) {
		candidate := openProviderScript(t)
		ops := defaultInspectionOps()
		ops.dsn = func(string, time.Duration) (string, error) { return "script", nil }
		ops.open = func(string) (*sql.DB, error) { return candidate, nil }
		generation, generationErr := captureInspectedGeneration(path)
		if generationErr != nil {
			t.Fatal(generationErr)
		}
		ops.inspectDatabase = func(
			context.Context, string, inspectedGeneration, *sql.DB,
		) (inspectedGeneration, int, int, error) {
			return generation, 0, 0, nil
		}
		ops.retain = func(
			string, *sql.DB, inspectedGeneration, time.Duration, *atomic.Bool,
		) (*sql.DB, error) {
			return nil, canary
		}
		if inspection, err := inspectWithOps(t.Context(), path, time.Second, ops); inspection.Exists || !errors.Is(err, canary) {
			t.Fatalf("retain failure = %#v, %v", inspection, err)
		}
		if err := candidate.Ping(); err == nil {
			t.Fatal("failed retained candidate remained open")
		}
	})
}

func TestInspectionQueriesRejectMissingStructuresAndLateErrors(t *testing.T) {
	canary := errors.New("inspection query canary")
	for _, test := range []struct {
		name string
		db   *sql.DB
		run  func(Inspection) (bool, error)
		want bool
		err  error
	}{
		{
			name: "missing table",
			db:   openProviderScript(t, providerRow("sqlite_schema", int64(0))),
			run: func(inspection Inspection) (bool, error) {
				return inspection.HasTableColumns(t.Context(), "missing", "id")
			},
		},
		{
			name: "column query error",
			db: openProviderScript(t,
				providerRow("sqlite_schema", int64(1)),
				providerScriptStep{query: "pragma_table_info", err: canary},
			),
			run: func(inspection Inspection) (bool, error) {
				return inspection.HasTableColumns(t.Context(), "items", "id")
			},
			err: canary,
		},
		{
			name: "missing horizon table",
			db:   openProviderScript(t, providerRow("sqlite_schema", int64(0))),
			run: func(inspection Inspection) (bool, error) {
				return inspection.HasImportHorizon(t.Context(), "auth")
			},
		},
		{
			name: "horizon query error",
			db: openProviderScript(t,
				providerRow("sqlite_schema", int64(1)),
				providerRow("pragma_table_xinfo", int64(2), int64(1), int64(1)),
				providerScriptStep{query: "storage_import_horizons", err: canary},
			),
			run: func(inspection Inspection) (bool, error) {
				return inspection.HasImportHorizon(t.Context(), "auth")
			},
			err: canary,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			inspection := Inspection{database: test.db, released: new(atomic.Bool)}
			ready, err := test.run(inspection)
			if ready != test.want || test.err == nil && err != nil ||
				test.err != nil && !errors.Is(err, test.err) {
				t.Fatalf("inspection query = %t, %v", ready, err)
			}
		})
	}
}

func TestInspectDatabasePreservesCancellationAndBusyIntegrityErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inspection.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	generation, err := captureInspectedGeneration(path)
	if err != nil {
		t.Fatal(err)
	}
	canary := errors.New("inspection phase canary")
	identity := func(string) (inspectedGeneration, error) { return generation, nil }
	secure := func(string) error { return nil }

	if _, _, _, err := inspectDatabaseWithGeneration(
		t.Context(), path, generation,
		openProviderScript(t, providerScriptStep{query: "integrity_check", err: canary}),
		identity, secure, func(error) bool { return true },
	); !errors.Is(err, errControlUnavailable) || IsInspectionIntegrity(err) {
		t.Fatalf("busy integrity error = %v", err)
	}

	for _, test := range []struct {
		name  string
		steps func(context.CancelFunc) []providerScriptStep
	}{
		{
			name: "integrity",
			steps: func(cancel context.CancelFunc) []providerScriptStep {
				return []providerScriptStep{{query: "integrity_check", err: canary, action: cancel}}
			},
		},
		{
			name: "schema version",
			steps: func(cancel context.CancelFunc) []providerScriptStep {
				return append(providerIntegrityOKSteps(), providerScriptStep{
					query: "user_version", err: canary, action: cancel,
				})
			},
		},
		{
			name: "schema catalog",
			steps: func(cancel context.CancelFunc) []providerScriptStep {
				return append(providerIntegrityOKSteps(),
					providerRow("user_version", int64(2)),
					providerScriptStep{query: "sqlite_schema", err: canary, action: cancel},
				)
			},
		},
	} {
		t.Run(test.name+" cancellation", func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			database := openProviderScript(t, test.steps(cancel)...)
			if _, _, _, err := inspectDatabaseWithGeneration(
				ctx, path, generation, database, identity, secure, func(error) bool { return false },
			); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled %s error = %v", test.name, err)
			}
		})
	}
}

func TestInspectedPoolFailsClosedAcrossOwnerAndGenerationTransitions(t *testing.T) {
	if database, err := inspectedPoolFor("store.db", inspectedGeneration{}, time.Second, nil); database != nil || err == nil {
		t.Fatalf("ownerless inspectedPoolFor() = %#v, %v", database, err)
	}
	if err := releaseInspectedPool("store.db", nil, nil); err != nil {
		t.Fatalf("ownerless release = %v", err)
	}

	t.Run("retain rejects changed existing generation", func(t *testing.T) {
		path, _, _, key, entry := seedInspectionPool(t, time.Second, new(atomic.Bool))
		other := filepath.Join(t.TempDir(), "other.db")
		if err := os.WriteFile(other, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		otherGeneration, err := captureInspectedGeneration(other)
		if err != nil {
			t.Fatal(err)
		}
		entry.generation = otherGeneration
		setInspectionPoolEntry(key, entry)
		candidate, err := OpenStore(":memory:", time.Second)
		if err != nil {
			t.Fatal(err)
		}
		generation, generationErr := captureInspectedGeneration(path)
		if generationErr != nil {
			t.Fatal(generationErr)
		}
		if retained, err := retainInspectedPool(key, candidate, generation, time.Second, new(atomic.Bool)); retained != nil || err == nil {
			t.Fatalf("changed retain = %#v, %v", retained, err)
		}
		if err := candidate.Ping(); err == nil {
			t.Fatal("rejected candidate remained live")
		}
	})

	t.Run("retain initializes owner set", func(t *testing.T) {
		_, generation, database, key, entry := seedInspectionPool(t, time.Second)
		entry.owners = nil
		setInspectionPoolEntry(key, entry)
		owner := new(atomic.Bool)
		candidate, err := OpenStore(":memory:", time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if retained, err := retainInspectedPool(key, candidate, generation, time.Second, owner); err != nil || retained != database {
			t.Fatalf("retain with nil owners = %#v, %v", retained, err)
		}
		if err := releaseInspectedPool(key, database, owner); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("lookup rejects timeout and initializes owner set", func(t *testing.T) {
		_, generation, database, key, entry := seedInspectionPool(t, time.Second)
		if found, err := inspectedPoolFor(key, generation, 2*time.Second, new(atomic.Bool)); found != nil || err == nil {
			t.Fatalf("wrong-timeout lookup = %#v, %v", found, err)
		}
		entry.owners = nil
		setInspectionPoolEntry(key, entry)
		owner := new(atomic.Bool)
		if found, err := inspectedPoolFor(key, generation, time.Second, owner); err != nil || found != database {
			t.Fatalf("owner initialization lookup = %#v, %v", found, err)
		}
		if err := releaseInspectedPool(key, database, owner); err != nil {
			t.Fatal(err)
		}
	})

	for _, test := range []struct {
		name  string
		owner func(*atomic.Bool) *atomic.Bool
	}{
		{name: "nil owner", owner: func(*atomic.Bool) *atomic.Bool { return nil }},
		{name: "released owner", owner: func(owner *atomic.Bool) *atomic.Bool {
			owner.Store(true)
			return owner
		}},
		{name: "foreign owner", owner: func(*atomic.Bool) *atomic.Bool { return new(atomic.Bool) }},
	} {
		t.Run("adopt rejects "+test.name, func(t *testing.T) {
			owner := new(atomic.Bool)
			path, _, database, key, _ := seedInspectionPool(t, time.Second, owner)
			adopted, err := adoptInspectedPoolWithGeneration(
				path, key, test.owner(owner),
				func(string) error { return nil }, func(string) error { return nil },
				captureInspectedGeneration,
			)
			if adopted != nil || err == nil {
				t.Fatalf("invalid-owner adoption = %#v, %v", adopted, err)
			}
			owner.Store(false)
			if err := releaseInspectedPool(key, database, owner); err != nil {
				t.Fatal(err)
			}
		})
	}

	t.Run("adopt rejects generation change after security", func(t *testing.T) {
		owner := new(atomic.Bool)
		path, _, database, key, _ := seedInspectionPool(t, time.Second, owner)
		sidecar := path + "-wal"
		adopted, err := adoptInspectedPoolWithGeneration(
			path, key, owner,
			func(string) error { return nil },
			func(string) error { return os.WriteFile(sidecar, nil, 0o600) },
			captureInspectedGeneration,
		)
		if adopted != nil || err == nil {
			t.Fatalf("changed-generation adoption = %#v, %v", adopted, err)
		}
		if err := os.Remove(sidecar); err != nil {
			t.Fatal(err)
		}
		if err := releaseInspectedPool(key, database, owner); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("adopt rejects generation change between final captures", func(t *testing.T) {
		owner := new(atomic.Bool)
		path, _, database, key, _ := seedInspectionPool(t, time.Second, owner)
		sidecar := path + "-wal"
		captures := 0
		adopted, err := adoptInspectedPoolWithGeneration(
			path, key, owner,
			func(string) error { return nil },
			func(string) error { return nil },
			func(candidate string) (inspectedGeneration, error) {
				captures++
				captured, captureErr := captureInspectedGeneration(candidate)
				if captureErr == nil && captures == 1 {
					captureErr = os.WriteFile(sidecar, nil, 0o600)
				}
				return captured, captureErr
			},
		)
		if adopted != nil || err == nil || captures != 2 {
			t.Fatalf(
				"mid-capture generation change = %#v, %v; captures=%d",
				adopted, err, captures,
			)
		}
		if err := database.Ping(); err != nil {
			t.Fatalf("rejected adoption invalidated readiness pool: %v", err)
		}
		if err := os.Remove(sidecar); err != nil {
			t.Fatal(err)
		}
		if err := releaseInspectedPool(key, database, owner); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("release wins while adoption validates generation", func(t *testing.T) {
		owner := new(atomic.Bool)
		path, _, database, key, _ := seedInspectionPool(t, time.Second, owner)
		secureEntered := make(chan struct{})
		continueAdoption := make(chan struct{})
		adoptedResult := make(chan *sql.DB, 1)
		adoptErr := make(chan error, 1)
		go func() {
			adopted, err := adoptInspectedPoolWithGeneration(
				path, key, owner,
				func(string) error { return nil },
				func(string) error {
					close(secureEntered)
					<-continueAdoption
					return nil
				},
				captureInspectedGeneration,
			)
			adoptedResult <- adopted
			adoptErr <- err
		}()
		<-secureEntered
		releaseErr := make(chan error, 1)
		go func() {
			inspection := Inspection{
				database: database,
				key:      key,
				released: owner,
			}
			releaseErr <- inspection.Release()
		}()
		deadline := time.Now().Add(time.Second)
		for !owner.Load() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if !owner.Load() {
			close(continueAdoption)
			t.Fatal("release did not win the shared owner transition")
		}
		close(continueAdoption)
		if adopted, err := <-adoptedResult, <-adoptErr; adopted != nil || err == nil {
			t.Fatalf("adoption after winning release = %#v, %v", adopted, err)
		}
		if err := <-releaseErr; err != nil {
			t.Fatal(err)
		}
		if err := database.Ping(); err == nil {
			t.Fatal("winning release left inspection pool open")
		}
	})

	t.Run("release ignores foreign owner", func(t *testing.T) {
		owner := new(atomic.Bool)
		_, _, database, key, _ := seedInspectionPool(t, time.Second, owner)
		if err := releaseInspectedPool(key, database, new(atomic.Bool)); err != nil {
			t.Fatal(err)
		}
		if err := database.Ping(); err != nil {
			t.Fatalf("foreign release closed pool: %v", err)
		}
		if err := releaseInspectedPool(key, database, owner); err != nil {
			t.Fatal(err)
		}
	})
}

func TestInspectedPoolPropagatesKeyDerivationFailures(t *testing.T) {
	canary := errors.New("inspection key canary")
	ops := defaultInspectionOps()
	ops.canonicalize = func(string) (string, string, error) { return "", "", canary }
	if inspection, err := inspectWithOps(t.Context(), "store.db", time.Second, ops); inspection.Exists || !errors.Is(err, canary) {
		t.Fatalf("canonicalization failure = %#v, %v", inspection, err)
	}
}

func TestSameInspectedGenerationRejectsMissingAndDifferentMembers(t *testing.T) {
	root := t.TempDir()
	firstPath := filepath.Join(root, "first.db")
	secondPath := filepath.Join(root, "second.db")
	for _, path := range []string{firstPath, secondPath} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	first, err := captureInspectedGeneration(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := captureInspectedGeneration(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	if sameInspectedGeneration(first, inspectedGeneration{}) {
		t.Fatal("missing generation member compared equal")
	}
	if sameInspectedGeneration(first, second) {
		t.Fatal("different generation members compared equal")
	}
}

func seedInspectionPool(
	t *testing.T,
	timeout time.Duration,
	owners ...*atomic.Bool,
) (string, inspectedGeneration, *sql.DB, string, inspectedPool) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pool.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	generation, err := captureInspectedGeneration(path)
	if err != nil {
		t.Fatal(err)
	}
	database, err := OpenStore(":memory:", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := canonicalInspectionLocation(path)
	if err != nil {
		t.Fatal(err)
	}
	ownerSet := make(map[*atomic.Bool]struct{}, len(owners))
	for _, owner := range owners {
		ownerSet[owner] = struct{}{}
	}
	entry := inspectedPool{
		database: database, generation: generation,
		busyTimeout: timeout, owners: ownerSet,
	}
	setInspectionPoolEntry(key, entry)
	t.Cleanup(func() {
		inspectedPools.Lock()
		if inspectedPools.values[key].database == database {
			delete(inspectedPools.values, key)
		}
		inspectedPools.Unlock()
		_ = database.Close()
	})
	return path, generation, database, key, entry
}

func setInspectionPoolEntry(key string, entry inspectedPool) {
	inspectedPools.Lock()
	inspectedPools.values[key] = entry
	inspectedPools.Unlock()
}
