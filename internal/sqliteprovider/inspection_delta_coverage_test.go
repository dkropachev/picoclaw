package sqliteprovider

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

func TestInspectionDeltaFailsClosedAtInjectedTransitions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transitions.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	generation, err := captureInspectedGeneration(path)
	if err != nil {
		t.Fatal(err)
	}
	canary := errors.New("inspection transition canary")

	t.Run("incomplete operations", func(t *testing.T) {
		ops := defaultInspectionOps()
		ops.release = nil
		if inspection, err := inspectWithOps(t.Context(), path, time.Second, ops); inspection.Exists || err == nil {
			t.Fatalf("incomplete operations = %#v, %v", inspection, err)
		}
	})

	t.Run("canonical invalid result", func(t *testing.T) {
		ops := defaultInspectionOps()
		ops.canonicalize = func(string) (string, string, error) {
			return " invalid.db", "invalid", nil
		}
		if inspection, err := inspectWithOps(t.Context(), path, time.Second, ops); inspection.Exists || err == nil {
			t.Fatalf("invalid canonical result = %#v, %v", inspection, err)
		}
	})

	t.Run("canceled after ancestor validation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		ops := defaultInspectionOps()
		ops.validateAncestors = func(string) error {
			cancel()
			return nil
		}
		if inspection, err := inspectWithOps(ctx, path, time.Second, ops); inspection.Exists ||
			!errors.Is(err, context.Canceled) {
			t.Fatalf("post-ancestor cancellation = %#v, %v", inspection, err)
		}
	})

	t.Run("canceled while checking missing sidecars", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing.db")
		ctx, cancel := context.WithCancel(context.Background())
		ops := defaultInspectionOps()
		ops.lstat = func(candidate string) (os.FileInfo, error) {
			if candidate != missing {
				cancel()
			}
			return nil, os.ErrNotExist
		}
		if inspection, err := inspectWithOps(ctx, missing, time.Second, ops); inspection.Exists ||
			!errors.Is(err, context.Canceled) {
			t.Fatalf("missing-sidecar cancellation = %#v, %v", inspection, err)
		}
	})

	t.Run("canceled after endpoint inspection", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		ops := defaultInspectionOps()
		ops.lstat = func(string) (os.FileInfo, error) {
			cancel()
			return info, nil
		}
		if inspection, err := inspectWithOps(ctx, path, time.Second, ops); inspection.Exists ||
			!errors.Is(err, context.Canceled) {
			t.Fatalf("post-endpoint cancellation = %#v, %v", inspection, err)
		}
	})

	t.Run("unsafe directory boundary", func(t *testing.T) {
		ops := defaultInspectionOps()
		ops.ensureDirectory = func(string) error {
			return errors.Join(errProviderUnsafeBoundary, canary)
		}
		if inspection, err := inspectWithOps(t.Context(), path, time.Second, ops); inspection.Exists ||
			!errors.Is(err, canary) || !IsInspectionIntegrity(err) {
			t.Fatalf("unsafe directory boundary = %#v, %v", inspection, err)
		}
	})

	t.Run("canceled after directory security", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		ops := defaultInspectionOps()
		ops.ensureDirectory = func(string) error {
			cancel()
			return nil
		}
		if inspection, err := inspectWithOps(ctx, path, time.Second, ops); inspection.Exists ||
			!errors.Is(err, context.Canceled) {
			t.Fatalf("post-directory cancellation = %#v, %v", inspection, err)
		}
	})

	t.Run("ordinary generation validation failure", func(t *testing.T) {
		ops := defaultInspectionOps()
		ops.validateGeneration = func(string, bool) error { return canary }
		if inspection, err := inspectWithOps(t.Context(), path, time.Second, ops); inspection.Exists ||
			!errors.Is(err, canary) || IsInspectionIntegrity(err) {
			t.Fatalf("generation validation failure = %#v, %v", inspection, err)
		}
	})

	t.Run("canceled after generation validation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		ops := defaultInspectionOps()
		ops.validateGeneration = func(string, bool) error {
			cancel()
			return nil
		}
		if inspection, err := inspectWithOps(ctx, path, time.Second, ops); inspection.Exists ||
			!errors.Is(err, context.Canceled) {
			t.Fatalf("post-generation cancellation = %#v, %v", inspection, err)
		}
	})

	t.Run("identity capture failure", func(t *testing.T) {
		ops := defaultInspectionOps()
		ops.captureGeneration = func(string) (inspectedGeneration, error) {
			return inspectedGeneration{}, canary
		}
		if inspection, err := inspectWithOps(t.Context(), path, time.Second, ops); inspection.Exists ||
			!errors.Is(err, canary) {
			t.Fatalf("identity capture failure = %#v, %v", inspection, err)
		}
	})

	t.Run("canceled after identity capture", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		ops := defaultInspectionOps()
		ops.captureGeneration = func(string) (inspectedGeneration, error) {
			cancel()
			return generation, nil
		}
		if inspection, err := inspectWithOps(ctx, path, time.Second, ops); inspection.Exists ||
			!errors.Is(err, context.Canceled) {
			t.Fatalf("post-identity cancellation = %#v, %v", inspection, err)
		}
	})

	t.Run("nil database open", func(t *testing.T) {
		ops := defaultInspectionOps()
		ops.dsn = func(string, time.Duration) (string, error) { return "script", nil }
		ops.open = func(string) (*sql.DB, error) { return nil, nil }
		if inspection, err := inspectWithOps(t.Context(), path, time.Second, ops); inspection.Exists || err == nil {
			t.Fatalf("nil database open = %#v, %v", inspection, err)
		}
	})

	t.Run("cleanup joins reconciliation and close failures", func(t *testing.T) {
		closeCanary := errors.New("inspection close canary")
		reconcileCanary := errors.New("inspection reconciliation canary")
		candidate := openConfiguredProviderScriptWithCloseError(t, nil, closeCanary)
		if err := candidate.Ping(); err != nil {
			t.Fatal(err)
		}
		ops := inspectionDeltaOpenedOps(candidate)
		ops.inspectDatabase = func(
			context.Context,
			string,
			inspectedGeneration,
			*sql.DB,
		) (inspectedGeneration, int, int, error) {
			return inspectedGeneration{}, 0, 0, canary
		}
		ops.reconcile = func() error { return reconcileCanary }
		inspection, err := inspectWithOps(t.Context(), path, time.Second, ops)
		if inspection.Exists || !errors.Is(err, canary) || !errors.Is(err, reconcileCanary) ||
			!errors.Is(err, closeCanary) || !IsInspectionInfrastructure(err) {
			t.Fatalf("joined cleanup failures = %#v, %v", inspection, err)
		}
	})

	t.Run("canceled before database inspection", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		candidate := openProviderScript(t)
		ops := inspectionDeltaOpenedOps(candidate)
		ops.open = func(string) (*sql.DB, error) {
			cancel()
			return candidate, nil
		}
		inspection, inspectErr := inspectWithOps(ctx, path, time.Second, ops)
		assertInspectionDatabaseClosed(t, candidate)
		if inspection.Exists || !errors.Is(inspectErr, context.Canceled) {
			t.Fatalf("pre-inspection cancellation = %#v, %v", inspection, inspectErr)
		}
	})

	t.Run("canceled after database inspection", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		candidate := openProviderScript(t)
		ops := inspectionDeltaOpenedOps(candidate)
		ops.inspectDatabase = func(
			context.Context,
			string,
			inspectedGeneration,
			*sql.DB,
		) (inspectedGeneration, int, int, error) {
			cancel()
			return generation, 0, 0, nil
		}
		inspection, inspectErr := inspectWithOps(ctx, path, time.Second, ops)
		assertInspectionDatabaseClosed(t, candidate)
		if inspection.Exists || !errors.Is(inspectErr, context.Canceled) {
			t.Fatalf("post-inspection cancellation = %#v, %v", inspection, inspectErr)
		}
	})

	t.Run("reconciliation failure", func(t *testing.T) {
		candidate := openProviderScript(t)
		ops := inspectionDeltaOpenedOps(candidate)
		ops.inspectDatabase = func(
			context.Context,
			string,
			inspectedGeneration,
			*sql.DB,
		) (inspectedGeneration, int, int, error) {
			return generation, 0, 0, nil
		}
		ops.reconcile = func() error { return canary }
		inspection, err := inspectWithOps(t.Context(), path, time.Second, ops)
		assertInspectionDatabaseClosed(t, candidate)
		if inspection.Exists || !errors.Is(err, canary) || !IsInspectionInfrastructure(err) {
			t.Fatalf("reconciliation failure = %#v, %v", inspection, err)
		}
	})

	t.Run("canceled after reconciliation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		candidate := openProviderScript(t)
		ops := inspectionDeltaOpenedOps(candidate)
		ops.inspectDatabase = func(
			context.Context,
			string,
			inspectedGeneration,
			*sql.DB,
		) (inspectedGeneration, int, int, error) {
			return generation, 0, 0, nil
		}
		ops.reconcile = func() error {
			cancel()
			return nil
		}
		inspection, inspectErr := inspectWithOps(ctx, path, time.Second, ops)
		assertInspectionDatabaseClosed(t, candidate)
		if inspection.Exists || !errors.Is(inspectErr, context.Canceled) {
			t.Fatalf("post-reconciliation cancellation = %#v, %v", inspection, inspectErr)
		}
	})

	t.Run("nil retained database", func(t *testing.T) {
		candidate := openProviderScript(t)
		ops := inspectionDeltaOpenedOps(candidate)
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
			return nil, nil
		}
		inspection, inspectErr := inspectWithOps(t.Context(), path, time.Second, ops)
		assertInspectionDatabaseClosed(t, candidate)
		if inspection.Exists || inspectErr == nil {
			t.Fatalf("nil retained database = %#v, %v", inspection, inspectErr)
		}
	})
}

func assertInspectionDatabaseClosed(t *testing.T, database *sql.DB) {
	t.Helper()
	if database == nil {
		t.Fatal("inspection candidate database is nil")
	}
	if err := database.Ping(); err == nil {
		_ = database.Close()
		t.Fatal("inspection candidate database remained open")
	}
}

func inspectionDeltaOpenedOps(candidate *sql.DB) inspectionOps {
	ops := defaultInspectionOps()
	ops.poolFor = func(
		string,
		inspectedGeneration,
		time.Duration,
		*atomic.Bool,
	) (*sql.DB, error) {
		return nil, nil
	}
	ops.dsn = func(string, time.Duration) (string, error) { return "script", nil }
	ops.open = func(string) (*sql.DB, error) { return candidate, nil }
	return ops
}

func TestInspectionDeltaRevalidatesStableAndChangedStates(t *testing.T) {
	t.Run("stable missing generation", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing.db")
		inspection, err := Inspect(t.Context(), path, 50*time.Millisecond)
		if err != nil || inspection.Exists {
			t.Fatalf("Inspect() = %#v, %v", inspection, err)
		}
		if err := inspection.Revalidate(t.Context()); err != nil {
			t.Fatalf("stable missing Revalidate() = %v", err)
		}

		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		if err := inspection.Revalidate(canceled); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled missing Revalidate() = %v", err)
		}

		if err := os.WriteFile(path+"-wal", []byte("orphan"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := inspection.Revalidate(t.Context()); !IsInspectionIntegrity(err) {
			t.Fatalf("materialized missing Revalidate() = %v", err)
		}
	})

	t.Run("stable existing generation and claimed reconciliation", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "existing.db")
		database, err := OpenStore(path, 50*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		if configureErr := Configure(
			t.Context(), database, 50*time.Millisecond, false,
		); configureErr != nil {
			_ = database.Close()
			t.Fatal(configureErr)
		}
		if closeErr := database.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}

		reconciliations := 0
		inspection, err := InspectClaimed(
			t.Context(), path, 50*time.Millisecond,
			func() error {
				reconciliations++
				return nil
			},
		)
		if inspection.Exists {
			t.Cleanup(func() { _ = inspection.Release() })
		}
		if err != nil || !inspection.Exists || reconciliations != 1 {
			t.Fatalf(
				"InspectClaimed() = %#v, %v; reconciliations=%d",
				inspection,
				err,
				reconciliations,
			)
		}
		if err := inspection.Revalidate(t.Context()); err != nil {
			t.Fatalf("stable existing Revalidate() = %v", err)
		}
		if err := inspection.Release(); err != nil {
			t.Fatal(err)
		}
		if err := inspection.Revalidate(t.Context()); err == nil || IsInspectionIntegrity(err) {
			t.Fatalf("released Revalidate() = %v", err)
		}
	})

	t.Run("replaced existing generation", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "replaced.db")
		if err := os.WriteFile(path, []byte("first"), 0o600); err != nil {
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
		inspection := Inspection{
			Exists:     true,
			database:   openProviderScript(t),
			path:       path,
			key:        key,
			generation: generation,
			released:   new(atomic.Bool),
		}
		if err := os.Rename(path, path+".old"); err != nil {
			t.Skipf("replace inspected file: %v", err)
		}
		if err := os.WriteFile(path, []byte("second"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := inspection.Revalidate(t.Context()); !IsInspectionIntegrity(err) {
			t.Fatalf("replaced generation Revalidate() = %v", err)
		}
	})

	t.Run("unsafe ancestor", func(t *testing.T) {
		root := t.TempDir()
		realDirectory := filepath.Join(root, "real")
		if err := os.Mkdir(realDirectory, 0o700); err != nil {
			t.Fatal(err)
		}
		linkedDirectory := filepath.Join(root, "linked")
		if err := os.Symlink(realDirectory, linkedDirectory); err != nil {
			t.Skipf("create ancestor symlink: %v", err)
		}
		inspection := Inspection{
			path: filepath.Join(linkedDirectory, "missing.db"),
			key:  "unsafe-ancestor",
		}
		if err := inspection.Revalidate(t.Context()); !IsInspectionIntegrity(err) {
			t.Fatalf("unsafe-ancestor Revalidate() = %v", err)
		}
	})

	t.Run("ancestor metadata failure", func(t *testing.T) {
		inspection := Inspection{
			path: filepath.Join(t.TempDir(), strings.Repeat("x", 300), "missing.db"),
			key:  "unavailable-ancestor",
		}
		if err := inspection.Revalidate(t.Context()); err == nil || IsInspectionIntegrity(err) {
			t.Fatalf("unavailable-ancestor Revalidate() = %v", err)
		}
	})

	t.Run("existing main disappeared", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "disappeared.db")
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
		inspection := Inspection{
			Exists:     true,
			database:   openProviderScript(t),
			path:       path,
			key:        key,
			generation: generation,
			released:   new(atomic.Bool),
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := inspection.Revalidate(t.Context()); !IsInspectionIntegrity(err) {
			t.Fatalf("disappeared-main Revalidate() = %v", err)
		}
	})

	if err := (Inspection{}).Revalidate(t.Context()); err == nil {
		t.Fatal("empty inspection revalidated")
	}
	if err := (Inspection{path: "store.db", key: "store.db"}).Revalidate(nil); err == nil {
		t.Fatal("nil-context inspection revalidated")
	}
}

func TestInspectionDeltaMissingValidationFaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	if err := validateAbsentInspection(nil, path, os.Lstat); err == nil {
		t.Fatal("nil-context absence validation succeeded")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := validateAbsentInspection(canceled, path, os.Lstat); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled absence validation = %v", err)
	}
	canary := errors.New("absence metadata canary")
	if err := validateAbsentInspection(
		t.Context(),
		path,
		func(string) (os.FileInfo, error) { return nil, canary },
	); !errors.Is(err, canary) || IsInspectionIntegrity(err) {
		t.Fatalf("absence metadata failure = %v", err)
	}
}

func TestInspectionDeltaPoolMismatchCloseFailures(t *testing.T) {
	root := t.TempDir()
	firstPath := filepath.Join(root, "first.db")
	secondPath := filepath.Join(root, "second.db")
	for _, path := range []string{firstPath, secondPath} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	firstGeneration, err := captureInspectedGeneration(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	secondGeneration, err := captureInspectedGeneration(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := canonicalInspectionLocation(firstPath)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name       string
		generation inspectedGeneration
		timeout    time.Duration
	}{
		{name: "generation", generation: secondGeneration, timeout: time.Second},
		{name: "timeout", generation: firstGeneration, timeout: 2 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			owner := new(atomic.Bool)
			existing := openProviderScript(t)
			t.Cleanup(func() {
				if err := releaseInspectedPool(key, existing, owner); err != nil {
					t.Errorf("release seed pool: %v", err)
				}
			})
			if retained, err := retainInspectedPool(
				key,
				existing,
				firstGeneration,
				time.Second,
				owner,
			); err != nil || retained != existing {
				t.Fatalf("seed retain = %p, %v", retained, err)
			}
			closeCanary := errors.New("candidate close canary")
			candidate := openConfiguredProviderScriptWithCloseError(t, nil, closeCanary)
			if err := candidate.Ping(); err != nil {
				t.Fatal(err)
			}
			retained, err := retainInspectedPool(
				key,
				candidate,
				test.generation,
				test.timeout,
				new(atomic.Bool),
			)
			if retained != nil || !errors.Is(err, closeCanary) ||
				!IsInspectionInfrastructure(err) {
				t.Fatalf("mismatched retain = %p, %v", retained, err)
			}
		})
	}
}

func TestInspectionDeltaGenerationCaptureTaxonomy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	identity, objectType, exists, err := fileidentity.ExistingWithType(path)
	if err != nil || !exists || !identity.Valid() || objectType != fileidentity.ObjectTypeRegular {
		t.Fatalf("ExistingWithType() = %#v, %d, %t, %v", identity, objectType, exists, err)
	}

	if _, err := captureInspectedGenerationWith(path, nil); err == nil {
		t.Fatal("nil identity lookup succeeded")
	}

	ordinary := errors.New("identity lookup canary")
	for _, test := range []struct {
		name      string
		lookup    inspectionIdentityLookup
		integrity bool
		cause     error
	}{
		{
			name: "unsafe lookup",
			lookup: func(string) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
				return fileidentity.Identity{}, 0, false, fileidentity.ErrUnsafeType
			},
			integrity: true,
			cause:     fileidentity.ErrUnsafeType,
		},
		{
			name: "ordinary lookup failure",
			lookup: func(string) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
				return fileidentity.Identity{}, 0, false, ordinary
			},
			cause: ordinary,
		},
		{
			name: "missing main",
			lookup: func(string) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
				return fileidentity.Identity{}, 0, false, nil
			},
			integrity: true,
		},
		{
			name: "invalid identity",
			lookup: func(string) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
				return fileidentity.Identity{}, fileidentity.ObjectTypeRegular, true, nil
			},
			integrity: true,
		},
		{
			name: "wrong object type",
			lookup: func(string) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
				return identity, fileidentity.ObjectTypeDirectory, true, nil
			},
			integrity: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			generation, err := captureInspectedGenerationWith(path, test.lookup)
			if generation != (inspectedGeneration{}) || err == nil ||
				IsInspectionIntegrity(err) != test.integrity ||
				test.cause != nil && !errors.Is(err, test.cause) {
				t.Fatalf("capture = %#v, %v", generation, err)
			}
		})
	}
}

func TestInspectionDeltaFinalIdentityAndQueryFaults(t *testing.T) {
	if _, _, _, err := inspectDatabaseWithGeneration(
		nil,
		"store.db",
		inspectedGeneration{},
		nil,
		nil,
		nil,
	); err == nil {
		t.Fatal("incomplete database-inspection boundary succeeded")
	}

	root := t.TempDir()
	path := filepath.Join(root, "first.db")
	otherPath := filepath.Join(root, "second.db")
	for _, candidate := range []string{path, otherPath} {
		if err := os.WriteFile(candidate, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	generation, err := captureInspectedGeneration(path)
	if err != nil {
		t.Fatal(err)
	}
	otherGeneration, err := captureInspectedGeneration(otherPath)
	if err != nil {
		t.Fatal(err)
	}
	canary := errors.New("final identity canary")
	happySteps := func() []providerScriptStep {
		return append(
			providerIntegrityOKSteps(),
			providerRow("user_version", int64(4)),
			providerRow("sqlite_schema", int64(2)),
		)
	}

	t.Run("final identity lookup failure", func(t *testing.T) {
		captures := 0
		capture := func(string) (inspectedGeneration, error) {
			captures++
			if captures == 2 {
				return inspectedGeneration{}, canary
			}
			return generation, nil
		}
		if _, _, _, err := inspectDatabaseWithGeneration(
			t.Context(),
			path,
			generation,
			openProviderScript(t, happySteps()...),
			capture,
			func(string) error { return nil },
		); !errors.Is(err, canary) || IsInspectionIntegrity(err) {
			t.Fatalf("final identity lookup failure = %v", err)
		}
	})

	t.Run("final generation drift", func(t *testing.T) {
		captures := 0
		capture := func(string) (inspectedGeneration, error) {
			captures++
			if captures == 2 {
				return otherGeneration, nil
			}
			return generation, nil
		}
		if _, _, _, err := inspectDatabaseWithGeneration(
			t.Context(),
			path,
			generation,
			openProviderScript(t, happySteps()...),
			capture,
			func(string) error { return nil },
		); !IsInspectionIntegrity(err) {
			t.Fatalf("final generation drift = %v", err)
		}
	})

	proven := errors.New("proven inspection integrity")
	if err := classifyInspectionDatabaseError(
		errors.Join(proven, canary),
		func(error) bool { return false },
		proven,
	); !IsInspectionIntegrity(err) || !errors.Is(err, proven) {
		t.Fatalf("proven integrity classification = %v", err)
	}

	t.Run("import horizon metadata query failure", func(t *testing.T) {
		inspection := Inspection{
			database: openProviderScript(
				t,
				providerRow("sqlite_schema", int64(1)),
				providerScriptStep{query: "pragma_table_xinfo", err: canary},
			),
			released: new(atomic.Bool),
		}
		if ready, err := inspection.HasImportHorizon(t.Context(), "auth"); ready ||
			!errors.Is(err, canary) || IsInspectionIntegrity(err) {
			t.Fatalf("import horizon metadata failure = %t, %v", ready, err)
		}
	})

	t.Run("duplicate import horizon", func(t *testing.T) {
		inspection := Inspection{
			database: openProviderScript(
				t,
				providerRow("sqlite_schema", int64(1)),
				providerRow("pragma_table_xinfo", int64(2), int64(1), int64(1)),
				providerRow("storage_import_horizons", int64(2), int64(2)),
			),
			released: new(atomic.Bool),
		}
		if ready, err := inspection.HasImportHorizon(t.Context(), "auth"); ready ||
			!IsInspectionIntegrity(err) {
			t.Fatalf("duplicate import horizon = %t, %v", ready, err)
		}
	})

	if database, err := adoptInspectedPoolWithGeneration(
		"",
		"",
		nil,
		nil,
		nil,
		nil,
	); database != nil || err == nil {
		t.Fatalf("incomplete adoption = %p, %v", database, err)
	}
}

func TestInspectionDeltaControlAndInfrastructureTaxonomy(t *testing.T) {
	canary := errors.New("infrastructure canary")
	if IsInspectionInfrastructure(canary) {
		t.Fatal("ordinary error classified as inspection infrastructure")
	}
	if !IsInspectionInfrastructure(errors.Join(errInspectionInfrastructure, canary)) {
		t.Fatal("joined infrastructure error was not classified")
	}

	fallback := errors.New("redacted diagnostic")
	busyErr := inspectionDeltaBusyError(t)
	if got := controlDiagnosticError(t.Context(), busyErr, fallback); !errors.Is(got, busyErr) {
		t.Fatalf("busy diagnostic = %v", got)
	}

	corruptPath := filepath.Join(t.TempDir(), "corrupt.db")
	if err := os.WriteFile(corruptPath, []byte("not a SQLite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	dsn, err := DSN(corruptPath, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	corruptDatabase, err := open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = corruptDatabase.Close() })
	var version int
	corruptErr := corruptDatabase.QueryRowContext(
		t.Context(), "PRAGMA main.user_version",
	).Scan(&version)
	if corruptErr == nil || !isSQLiteIntegrityFailure(corruptErr) {
		t.Fatalf("corrupt SQLite diagnostic = %v", corruptErr)
	}
	if got := controlDiagnosticError(t.Context(), corruptErr, fallback); !errors.Is(got, fallback) {
		t.Fatalf("corrupt diagnostic = %v", got)
	}

	cleanDatabase := openControlTestDatabase(t)
	_, syntaxErr := cleanDatabase.ExecContext(t.Context(), "invalid SQL statement")
	if syntaxErr == nil || isSQLiteIntegrityFailure(syntaxErr) {
		t.Fatalf("syntax error integrity classification = %v", syntaxErr)
	}
}

func inspectionDeltaBusyError(t *testing.T) error {
	t.Helper()
	path := filepath.Join(t.TempDir(), "busy.db")
	owner, err := OpenStore(path, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	if configureErr := ConfigureOffline(
		t.Context(), owner, 50*time.Millisecond,
	); configureErr != nil {
		t.Fatal(configureErr)
	}
	connection, err := owner.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	if _, beginErr := connection.ExecContext(
		t.Context(), "BEGIN EXCLUSIVE",
	); beginErr != nil {
		t.Fatal(beginErr)
	}
	t.Cleanup(func() {
		_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
	})

	dsn, err := DSN(path, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	contender, err := open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = contender.Close() })
	var journal string
	busyErr := contender.QueryRowContext(
		t.Context(), "PRAGMA journal_mode = WAL",
	).Scan(&journal)
	if busyErr == nil || !IsBusyOrLocked(busyErr) {
		t.Fatalf("contended journal diagnostic = %v", busyErr)
	}
	return busyErr
}

func TestInspectionDeltaProviderFilesystemErrorsRemainUnavailable(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "store.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	canary := errors.New("provider filesystem canary")

	t.Run("reinspect main", func(t *testing.T) {
		filesystem := inspectionDeltaFilesystem()
		calls := 0
		filesystem.lstat = func(candidate string) (os.FileInfo, error) {
			if candidate != path {
				return nil, os.ErrNotExist
			}
			calls++
			if calls == 2 {
				return nil, canary
			}
			return info, nil
		}
		if err := validateGenerationMembersWithFilesystem(path, true, filesystem); !errors.Is(err, canary) ||
			errors.Is(err, errProviderUnsafeBoundary) {
			t.Fatalf("main reinspection error = %v", err)
		}
	})

	t.Run("reinspect main for sidecar", func(t *testing.T) {
		filesystem := inspectionDeltaFilesystem()
		mainCalls := 0
		filesystem.lstat = func(candidate string) (os.FileInfo, error) {
			switch candidate {
			case path:
				mainCalls++
				if mainCalls == 3 {
					return nil, canary
				}
				return info, nil
			case path + "-wal":
				return info, nil
			default:
				return nil, os.ErrNotExist
			}
		}
		if err := validateGenerationMembersWithFilesystem(path, true, filesystem); !errors.Is(err, canary) ||
			errors.Is(err, errProviderUnsafeBoundary) {
			t.Fatalf("sidecar main reinspection error = %v", err)
		}
	})
}

func inspectionDeltaFilesystem() providerFilesystem {
	return providerFilesystem{
		secureFile:       func(string) error { return nil },
		validateLiveInfo: func(os.FileInfo) error { return nil },
		linkCount:        func(string, os.FileInfo) generationLinkClass { return generationLinkSingle },
		owner:            func(string, os.FileInfo) generationOwnerClass { return generationOwnerCurrent },
	}
}
