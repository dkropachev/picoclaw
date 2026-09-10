package databasemigration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestPreparedGenerationDefensiveBranches(t *testing.T) {
	if err := (*preparedGeneration)(nil).guard(t.Context()); err == nil {
		t.Fatal("nil prepared guard succeeded")
	}
	if err := (*preparedGeneration)(nil).close(); err != nil {
		t.Fatal(err)
	}
	if got := (*preparedGeneration)(nil).String(); got != "<nil>" {
		t.Fatalf("nil prepared String = %q", got)
	}
	if prepared, err := sealPreparedGeneration(
		t.Context(), "relative", validManifestValidationFixture(t), storecatalog.Spec{},
	); prepared != nil || err == nil {
		t.Fatalf("relative prepared seal = %v, %v", prepared, err)
	}

	home := migrationHome(t)
	spec := storecatalog.Spec{ID: "global/auth", Path: filepath.Join(home, "auth.db")}
	writeMigrationFile(t, spec.Path, []byte("database"))
	session, err := snapshotBackup(
		t.Context(), time.Now, home,
		[]storecatalog.Spec{spec}, []storecatalog.Spec{spec}, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	prepared, cleanup, err := session.prepareGeneration(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prepared.String(), spec.ID.String()) {
		t.Fatalf("prepared String = %q", prepared.String())
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if guardErr := prepared.guard(canceled); !errors.Is(guardErr, context.Canceled) {
		t.Fatalf("canceled prepared guard = %v", guardErr)
	}
	if closeErr := prepared.rootHandle.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if guardErr := prepared.guard(t.Context()); guardErr == nil {
		t.Fatal("prepared generation with closed root handle passed")
	}
	// Recreate fixture after intentionally closing its root handle.
	if cleanupErr := cleanup(); cleanupErr != nil && !strings.Contains(cleanupErr.Error(), "closed") {
		t.Fatal(cleanupErr)
	}
	prepared, cleanup, err = session.prepareGeneration(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	path := prepared.path
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := prepared.guard(t.Context()); err == nil {
		t.Fatal("removed prepared member passed guard")
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if err := prepared.guard(t.Context()); err == nil {
		t.Fatal("closed prepared generation passed guard")
	}
	if err := prepared.close(); err != nil {
		t.Fatal(err)
	}
}

func TestBackupPrepareAdditionalFaultBranches(t *testing.T) {
	canary := errors.New("prepare fault canary")
	home := migrationHome(t)
	legacyFile := filepath.Join(home, "legacy-file.json")
	legacyDirectory := filepath.Join(home, "legacy-directory")
	writeMigrationFile(t, legacyFile, []byte("file"))
	writeMigrationFile(t, filepath.Join(legacyDirectory, "nested.json"), []byte("nested"))
	spec := storecatalog.Spec{
		ID: "global/auth", Path: filepath.Join(home, "auth.db"),
		LegacyRoots: []string{legacyFile, legacyDirectory},
	}
	writeMigrationFile(t, spec.Path, []byte("database"))
	session, err := snapshotBackup(
		t.Context(), time.Now, home,
		[]storecatalog.Spec{spec}, []storecatalog.Spec{spec}, "",
	)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name   string
		legacy bool
		mutate func(*backupPrepareOps)
	}{
		{name: "generation verify", mutate: func(ops *backupPrepareOps) {
			ops.verifyStore = func(*backupSession, context.Context, database.StoreID) error { return canary }
		}},
		{name: "generation mkdir", mutate: func(ops *backupPrepareOps) {
			ops.mkdirTemp = func(string, string) (string, error) { return "", canary }
		}},
		{name: "generation missing work identity", mutate: func(ops *backupPrepareOps) {
			ops.mkdirTemp = func(parent, _ string) (string, error) {
				return filepath.Join(parent, "missing-work"), nil
			}
		}},
		{name: "generation secure", mutate: func(ops *backupPrepareOps) {
			ops.secureDir = func(string) error { return canary }
		}},
		{name: "generation sync", mutate: func(ops *backupPrepareOps) {
			calls := 0
			original := ops.syncDir
			ops.syncDir = func(path string) error {
				calls++
				if calls == 1 {
					return canary
				}
				return original(path)
			}
		}},
		{name: "generation reverify", mutate: func(ops *backupPrepareOps) {
			calls := 0
			original := ops.verifyStore
			ops.verifyStore = func(session *backupSession, ctx context.Context, id database.StoreID) error {
				calls++
				if calls == 2 {
					return canary
				}
				return original(session, ctx, id)
			}
		}},
		{name: "legacy verify", legacy: true, mutate: func(ops *backupPrepareOps) {
			ops.verifyStore = func(*backupSession, context.Context, database.StoreID) error { return canary }
		}},
		{name: "legacy mkdir", legacy: true, mutate: func(ops *backupPrepareOps) {
			ops.mkdirTemp = func(string, string) (string, error) { return "", canary }
		}},
		{name: "legacy missing work identity", legacy: true, mutate: func(ops *backupPrepareOps) {
			ops.mkdirTemp = func(parent, _ string) (string, error) {
				return filepath.Join(parent, "missing-input-work"), nil
			}
		}},
		{name: "legacy secure", legacy: true, mutate: func(ops *backupPrepareOps) {
			ops.secureDir = func(string) error { return canary }
		}},
		{name: "legacy sync", legacy: true, mutate: func(ops *backupPrepareOps) {
			calls := 0
			original := ops.syncDir
			ops.syncDir = func(path string) error {
				calls++
				if calls == 1 {
					return canary
				}
				return original(path)
			}
		}},
		{name: "legacy ensure directory", legacy: true, mutate: func(ops *backupPrepareOps) {
			ops.ensureDir = func(string) error { return canary }
		}},
		{name: "legacy relative", legacy: true, mutate: func(ops *backupPrepareOps) {
			ops.rel = func(string, string) (string, error) { return "", canary }
		}},
		{name: "legacy copy", legacy: true, mutate: func(ops *backupPrepareOps) {
			ops.copyFile = func(
				context.Context, string, string, string, string, string, *backupBudget,
			) (BackupFileManifest, error) {
				return BackupFileManifest{}, canary
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ops := defaultBackupPrepareOps()
			test.mutate(&ops)
			if test.legacy {
				roots, cleanup, err := session.prepareLegacyInputsWithOps(t.Context(), spec, ops)
				if roots != nil || cleanup == nil || err == nil {
					t.Fatalf("faulted legacy prepare = %q, %v", roots, err)
				}
				return
			}
			path, cleanup, err := session.prepareGenerationWithOps(t.Context(), spec, ops)
			if path != "" || cleanup == nil || err == nil {
				t.Fatalf("faulted generation prepare = %q, %v", path, err)
			}
		})
	}
}

func TestBackupCleanupRejectsWorkRootReplacement(t *testing.T) {
	home := migrationHome(t)
	spec := storecatalog.Spec{ID: "global/auth", Path: filepath.Join(home, "auth.db")}
	writeMigrationFile(t, spec.Path, []byte("database"))
	session, err := snapshotBackup(
		t.Context(), time.Now, home,
		[]storecatalog.Spec{spec}, []storecatalog.Spec{spec}, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	path, cleanup, err := session.prepareGenerationWithOps(t.Context(), spec, defaultBackupPrepareOps())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(path)
	original := root + ".original"
	if err := os.Rename(root, original); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := cleanup(); err == nil {
		t.Fatal("cleanup removed a replaced work root")
	}
	if _, err := os.Lstat(root); err != nil {
		t.Fatalf("replacement work root was removed: %v", err)
	}
}

func TestPreparedGenerationSealFailureBranches(t *testing.T) {
	manifest := validGenerationManifestFixture(t)
	spec := storecatalog.Spec{ID: "global/auth", Path: manifest.Stores[0].Path}
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "generation.db")
	changed := spec
	changed.Path = filepath.Join(filepath.Dir(spec.Path), "other.db")
	if prepared, err := sealPreparedGeneration(t.Context(), path, manifest, changed); prepared != nil || err == nil {
		t.Fatalf("changed provenance seal = %v, %v", prepared, err)
	}
	if prepared, err := sealPreparedGeneration(t.Context(), path, manifest, spec); prepared == nil || err != nil {
		t.Fatalf("absent generation seal = %v, %v", prepared, err)
	} else if err := prepared.close(); err != nil {
		t.Fatal(err)
	}
	invalidManifest := manifest
	invalidManifest.Version++
	if prepared, err := sealPreparedGeneration(
		t.Context(),
		path,
		invalidManifest,
		spec,
	); prepared != nil ||
		err == nil {
		t.Fatalf("invalid manifest seal = %v, %v", prepared, err)
	}
	if prepared, err := sealPreparedGeneration(
		t.Context(), filepath.Join(root, "missing", "generation.db"), manifest, spec,
	); prepared != nil || err == nil {
		t.Fatalf("missing root seal = %v, %v", prepared, err)
	}
	rootFile := filepath.Join(t.TempDir(), "root-file")
	writeMigrationFile(t, rootFile, nil)
	if prepared, err := sealPreparedGeneration(
		t.Context(), filepath.Join(rootFile, "generation.db"), manifest, spec,
	); prepared != nil || err == nil {
		t.Fatalf("regular root seal = %v, %v", prepared, err)
	}
	unsafeRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(unsafeRoot, "generation.db"), 0o700); err != nil {
		t.Fatal(err)
	}
	if prepared, err := sealPreparedGeneration(
		t.Context(), filepath.Join(unsafeRoot, "generation.db"), manifest, spec,
	); prepared != nil || err == nil {
		t.Fatalf("directory member seal = %v, %v", prepared, err)
	}
	unsafeSidecarRoot := t.TempDir()
	writeMigrationFile(t, filepath.Join(unsafeSidecarRoot, "generation.db"), []byte("database"))
	if err := os.Mkdir(filepath.Join(unsafeSidecarRoot, "generation.db-wal"), 0o700); err != nil {
		t.Fatal(err)
	}
	if prepared, err := sealPreparedGeneration(
		t.Context(), filepath.Join(unsafeSidecarRoot, "generation.db"), manifest, spec,
	); prepared != nil || err == nil {
		t.Fatalf("unsafe sidecar seal = %v, %v", prepared, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if prepared, err := sealPreparedGeneration(canceled, path, manifest, spec); prepared != nil ||
		!errors.Is(err, context.Canceled) {
		t.Fatalf("canceled seal = %v, %v", prepared, err)
	}
	hashCancelRoot := t.TempDir()
	payload := []byte("database")
	writeMigrationFile(t, filepath.Join(hashCancelRoot, "generation.db"), payload)
	hashManifest := validGenerationManifestFixture(t, "database")
	hashSpec := storecatalog.Spec{ID: "global/auth", Path: hashManifest.Stores[0].Path}
	digest := sha256.Sum256(payload)
	hashManifest.Files[0].SHA256 = hex.EncodeToString(digest[:])
	hashManifest.Files[0].Size = int64(len(payload))
	hashCancel := &cancelAfterMigrationErrChecks{Context: context.Background(), allowed: 1}
	if prepared, err := sealPreparedGeneration(
		hashCancel, filepath.Join(hashCancelRoot, "generation.db"), hashManifest, hashSpec,
	); prepared != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("hash-canceled seal = %v, %v", prepared, err)
	}
}

func TestPreparedGenerationHashBranches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	writeMigrationFile(t, path, []byte("payload"))
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hashPreparedGenerationMember(t.Context(), file, 8); err == nil {
		t.Fatal("prepared hash accepted short source")
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := hashPreparedGenerationMember(t.Context(), file, 7); err == nil {
		t.Fatal("prepared hash accepted closed source")
	}
}

func TestPreparedGenerationUseAndGuardBoundaries(t *testing.T) {
	if err := (*preparedGeneration)(nil).use(t.Context(), func(context.Context, string) error {
		return nil
	}); err == nil {
		t.Fatal("nil prepared use succeeded")
	}
	if err := (&preparedGeneration{}).use(t.Context(), nil); err == nil {
		t.Fatal("nil prepared operation succeeded")
	}
	home := migrationHome(t)
	spec := storecatalog.Spec{ID: "global/auth", Path: filepath.Join(home, "auth.db")}
	writeMigrationFile(t, spec.Path, []byte("database"))
	session, err := snapshotBackup(
		t.Context(), time.Now, home,
		[]storecatalog.Spec{spec}, []storecatalog.Spec{spec}, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	prepared, cleanup, err := session.prepareGeneration(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cleanup() })
	if err := prepared.guardLocked(nil); err != nil {
		t.Fatalf("nil-context prepared guard = %v", err)
	}
	canary := errors.New("prepared operation canary")
	if err := prepared.use(t.Context(), func(context.Context, string) error {
		return canary
	}); !errors.Is(err, canary) {
		t.Fatalf("prepared operation error = %v", err)
	}
	if err := prepared.close(); err != nil {
		t.Fatal(err)
	}
	if err := prepared.use(t.Context(), func(context.Context, string) error { return nil }); err == nil {
		t.Fatal("closed prepared use succeeded")
	}
}

func TestPreparedGenerationFinalInventorySeal(t *testing.T) {
	manifest := validGenerationManifestFixture(t)
	spec := storecatalog.Spec{
		ID: "global/auth", Path: manifest.Stores[0].Path,
	}
	root := t.TempDir()
	path := filepath.Join(root, "generation.db")
	writeMigrationFile(t, path, []byte("unmanifested"))
	prepared, err := sealPreparedGeneration(t.Context(), path, manifest, spec)
	if prepared != nil || err == nil || !strings.Contains(err.Error(), "unmanifested") {
		if prepared != nil {
			_ = prepared.close()
		}
		t.Fatalf("extra prepared inventory = %#v, %v", prepared, err)
	}
}

func TestDisposablePreparationAdditionalLifecycleBranches(t *testing.T) {
	t.Run("nil contexts", func(t *testing.T) {
		home := migrationHome(t)
		legacy := filepath.Join(home, "legacy")
		writeMigrationFile(t, filepath.Join(legacy, "a.json"), []byte("a"))
		writeMigrationFile(t, filepath.Join(legacy, "b.json"), []byte("b"))
		spec := storecatalog.Spec{
			ID: "global/auth", Path: filepath.Join(home, "auth.db"), LegacyRoots: []string{legacy},
		}
		writeMigrationFile(t, spec.Path, []byte("database"))
		session, err := snapshotBackup(
			t.Context(), time.Now, home,
			[]storecatalog.Spec{spec}, []storecatalog.Spec{spec}, consumptionBackupParent(t),
		)
		if err != nil {
			t.Fatal(err)
		}
		path, cleanupGeneration, err := session.prepareGenerationWithOps(
			nil, spec, defaultBackupPrepareOps(),
		)
		if err != nil || path == "" {
			t.Fatalf("nil-context generation = %q, %v", path, err)
		}
		if cleanupErr := cleanupGeneration(); cleanupErr != nil {
			t.Fatal(cleanupErr)
		}
		roots, cleanupLegacy, err := session.prepareLegacyInputsWithOps(
			nil, spec, defaultBackupPrepareOps(),
		)
		if err != nil || len(roots) != 1 {
			t.Fatalf("nil-context legacy = %#v, %v", roots, err)
		}
		if err := cleanupLegacy(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("empty legacy roots", func(t *testing.T) {
		home := migrationHome(t)
		spec := storecatalog.Spec{ID: "global/auth", Path: filepath.Join(home, "auth.db")}
		session, err := snapshotBackup(
			t.Context(), time.Now, home,
			[]storecatalog.Spec{spec}, []storecatalog.Spec{spec}, consumptionBackupParent(t),
		)
		if err != nil {
			t.Fatal(err)
		}
		roots, cleanup, err := session.prepareLegacyInputs(t.Context(), spec)
		if err != nil || roots != nil || cleanup == nil {
			t.Fatalf("empty legacy preparation = %#v, %v", roots, err)
		}
		if err := cleanup(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("cleanup refuses replaced root", func(t *testing.T) {
		_, spec, session := additionalLiveSnapshot(t)
		prepared, cleanup, err := session.prepareLegacyInputs(t.Context(), spec)
		if err != nil || prepared == nil || len(prepared.roots) != 1 {
			t.Fatalf("prepared roots = %#v, %v", prepared, err)
		}
		workRoot := prepared.root
		if err := os.Rename(workRoot, workRoot+".old"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(workRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := cleanup(); err == nil || !strings.Contains(err.Error(), "identity changed") {
			t.Fatalf("replaced cleanup root = %v", err)
		}
	})

	t.Run("long disposable parents", func(t *testing.T) {
		home := migrationHome(t)
		spec := storecatalog.Spec{ID: "global/auth", Path: filepath.Join(home, "auth.db")}
		longRoot := filepath.Join(home, strings.Repeat("x", backupMaxPathBytes), "archive")
		session := &backupSession{
			root: longRoot,
			manifest: BackupManifest{Stores: []BackupStoreManifest{{
				StoreID: spec.ID.String(), Path: spec.Path,
				LegacyRoots: []string{}, LegacyRootKinds: []string{},
			}}},
		}
		ops := defaultBackupPrepareOps()
		ops.verifyStore = func(*backupSession, context.Context, database.StoreID) error { return nil }
		if path, cleanup, err := session.prepareGenerationWithOps(t.Context(), spec, ops); path != "" ||
			cleanup == nil || err == nil || !strings.Contains(err.Error(), "too long") {
			t.Fatalf("long generation parent = %q, %v", path, err)
		}
		legacySpec := spec
		legacySpec.LegacyRoots = []string{spec.Path}
		session.manifest.Stores[0].LegacyRoots = []string{spec.Path}
		session.manifest.Stores[0].LegacyRootKinds = []string{"file"}
		if roots, cleanup, err := session.prepareLegacyInputsWithOps(
			t.Context(), legacySpec, ops,
		); roots != nil || cleanup == nil || err == nil || !strings.Contains(err.Error(), "too long") {
			t.Fatalf("long legacy parent = %#v, %v", roots, err)
		}
	})

	t.Run("generation copy failure", func(t *testing.T) {
		home := migrationHome(t)
		spec := storecatalog.Spec{ID: "global/auth", Path: filepath.Join(home, "auth.db")}
		writeMigrationFile(t, spec.Path, []byte("database"))
		session, err := snapshotBackup(
			t.Context(), time.Now, home,
			[]storecatalog.Spec{spec}, []storecatalog.Spec{spec}, consumptionBackupParent(t),
		)
		if err != nil {
			t.Fatal(err)
		}
		canary := errors.New("generation reconstruction canary")
		ops := defaultBackupPrepareOps()
		ops.copyFile = func(
			context.Context, string, string, string, string, string, *backupBudget,
		) (BackupFileManifest, error) {
			return BackupFileManifest{}, canary
		}
		if path, cleanup, err := session.prepareGenerationWithOps(t.Context(), spec, ops); path != "" ||
			cleanup == nil || !errors.Is(err, canary) {
			t.Fatalf("generation reconstruction fault = %q, %v", path, err)
		}
	})
}

func TestBackupConsumptionCoverageCloseout(t *testing.T) {
	t.Run("invalid immutable source preparation", func(t *testing.T) {
		source, cleanup, err := (*backupSession)(nil).prepareImmutableGenerationSource(
			t.Context(), storecatalog.Spec{ID: "global/auth"},
		)
		if err == nil || cleanup == nil || cleanup() != nil {
			t.Fatalf("invalid immutable source = %#v, cleanup=%t, err=%v", source, cleanup != nil, err)
		}
	})

	t.Run("nil provider-source context", func(t *testing.T) {
		_, spec, session := additionalGenerationSnapshot(t)
		prepared, cleanup, err := session.prepareGeneration(t.Context(), spec)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		if err := prepared.use(nil, func(context.Context, string) error { return nil }); err != nil {
			t.Fatalf("nil prepared use context = %v", err)
		}
	})

	t.Run("generation copy result mismatch", func(t *testing.T) {
		_, spec, session := additionalGenerationSnapshot(t)
		ops := defaultBackupPrepareOps()
		ops.copyFile = func(
			context.Context, string, string, string, string, string, *backupBudget,
		) (BackupFileManifest, error) {
			return BackupFileManifest{}, nil
		}
		if path, cleanup, err := session.prepareGenerationWithOps(
			t.Context(), spec, ops,
		); path != "" || cleanup == nil || err == nil {
			t.Fatalf("mismatched generation copy = %q, cleanup=%t, err=%v", path, cleanup != nil, err)
		}
	})

	t.Run("overlong temp prefix", func(t *testing.T) {
		if path, err := createPinnedBackupTempDirectoryWithOps(
			t.TempDir(), strings.Repeat("x", backupMaxComponent), defaultBackupTempDirectoryOps(),
		); path != "" || err == nil {
			t.Fatalf("overlong temp prefix = %q, %v", path, err)
		}
	})

	t.Run("canceled legacy directory guard", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := (&preparedLegacyInputs{}).guardDirectory(
			ctx, ".", nil, nil, nil, new(int),
		)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled legacy directory guard = %v", err)
		}
	})

	t.Run("public legacy member", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "legacy.json")
		writeMigrationFile(t, path, []byte("legacy"))
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			t.Fatal(err)
		}
		identity, objectType, identityErr := fileidentity.Opened(file)
		if identityErr != nil || objectType != fileidentity.ObjectTypeRegular {
			t.Fatalf("legacy member identity = %#v, %v, %v", identity, objectType, identityErr)
		}
		member := &preparedLegacyMember{
			path: path, identity: identity, info: info, file: file,
			size: info.Size(), digest: strings.Repeat("a", 64),
		}
		if err := guardPreparedLegacyMember(t.Context(), member); err == nil {
			t.Fatal("public prepared legacy member passed its seal")
		}
	})

	for _, legacy := range []bool{false, true} {
		name := "generation"
		if legacy {
			name = "legacy"
		}
		t.Run(name+" parent provenance", func(t *testing.T) {
			_, spec, session := additionalLiveSnapshot(t)
			session.parent = filepath.Join(session.parent, "different")
			if legacy {
				if roots, cleanup, err := session.prepareLegacyInputsWithOps(
					t.Context(), spec, defaultBackupPrepareOps(),
				); roots != nil || cleanup == nil || err == nil {
					t.Fatalf("legacy parent provenance = %#v, cleanup=%t, err=%v", roots, cleanup != nil, err)
				}
				return
			}
			if path, cleanup, err := session.prepareGenerationWithOps(
				t.Context(), spec, defaultBackupPrepareOps(),
			); path != "" || cleanup == nil || err == nil {
				t.Fatalf("generation parent provenance = %q, cleanup=%t, err=%v", path, cleanup != nil, err)
			}
		})

		t.Run(name+" parent identity", func(t *testing.T) {
			_, spec, session := additionalLiveSnapshot(t)
			session.parentIdentity = fileidentity.Identity{}
			if legacy {
				if roots, cleanup, err := session.prepareLegacyInputsWithOps(
					t.Context(), spec, defaultBackupPrepareOps(),
				); roots != nil || cleanup == nil || err == nil {
					t.Fatalf("legacy parent identity = %#v, cleanup=%t, err=%v", roots, cleanup != nil, err)
				}
				return
			}
			if path, cleanup, err := session.prepareGenerationWithOps(
				t.Context(), spec, defaultBackupPrepareOps(),
			); path != "" || cleanup == nil || err == nil {
				t.Fatalf("generation parent identity = %q, cleanup=%t, err=%v", path, cleanup != nil, err)
			}
		})
	}

	t.Run("legacy copy result mismatch", func(t *testing.T) {
		_, spec, session := additionalLiveSnapshot(t)
		var legacyRecord BackupFileManifest
		for _, record := range session.manifest.Files {
			if record.Role == "legacy" {
				legacyRecord = record
				break
			}
		}
		ops := defaultBackupPrepareOps()
		ops.copyFile = func(
			context.Context, string, string, string, string, string, *backupBudget,
		) (BackupFileManifest, error) {
			return BackupFileManifest{Size: legacyRecord.Size, SHA256: "different"}, nil
		}
		if roots, cleanup, err := session.prepareLegacyInputsWithOps(
			t.Context(), spec, ops,
		); roots != nil || cleanup == nil || err == nil {
			t.Fatalf("legacy copy mismatch = %#v, cleanup=%t, err=%v", roots, cleanup != nil, err)
		}
	})

	t.Run("legacy archive revalidation", func(t *testing.T) {
		_, spec, session := additionalLiveSnapshot(t)
		canary := errors.New("legacy archive revalidation failed")
		ops := defaultBackupPrepareOps()
		originalVerify := ops.verifyStore
		calls := 0
		ops.verifyStore = func(
			candidate *backupSession, ctx context.Context, id database.StoreID,
		) error {
			calls++
			if calls == 2 {
				return canary
			}
			return originalVerify(candidate, ctx, id)
		}
		if roots, cleanup, err := session.prepareLegacyInputsWithOps(
			t.Context(), spec, ops,
		); roots != nil || cleanup == nil || !errors.Is(err, canary) {
			t.Fatalf("legacy archive revalidation = %#v, cleanup=%t, err=%v", roots, cleanup != nil, err)
		}
	})

	t.Run("generation seal failure cleans reconstruction", func(t *testing.T) {
		_, spec, session := additionalGenerationSnapshot(t)
		canary := errors.New("generation seal failed")
		prepared, cleanup, err := session.prepareGenerationWithSealer(
			t.Context(), spec,
			func(
				context.Context, string, BackupManifest, storecatalog.Spec,
			) (*preparedGeneration, error) {
				return nil, canary
			},
		)
		if prepared != nil || cleanup == nil || !errors.Is(err, canary) || cleanup() != nil {
			t.Fatalf("generation seal failure = %#v, cleanup=%t, err=%v", prepared, cleanup != nil, err)
		}
	})

	t.Run("provider source mint failure cleans seal", func(t *testing.T) {
		_, spec, session := additionalGenerationSnapshot(t)
		canary := errors.New("provider source mint failed")
		source, cleanup, err := session.prepareImmutableGenerationSourceWithMinter(
			t.Context(), spec,
			func(
				database.StoreID,
				func(context.Context, func(context.Context, string) error) error,
			) (sqliteprovider.ImmutableGenerationSource, error) {
				return sqliteprovider.ImmutableGenerationSource{}, canary
			},
		)
		if cleanup == nil || !errors.Is(err, canary) || cleanup() != nil {
			t.Fatalf("provider source mint failure = %#v, cleanup=%t, err=%v", source, cleanup != nil, err)
		}
	})

	t.Run("legacy seal failure cleans reconstruction", func(t *testing.T) {
		_, spec, session := additionalLiveSnapshot(t)
		canary := errors.New("legacy seal failed")
		prepared, cleanup, err := session.prepareLegacyInputsWithSealer(
			t.Context(), spec,
			func(
				context.Context, string, []string, BackupManifest, storecatalog.Spec,
			) (*preparedLegacyInputs, error) {
				return nil, canary
			},
		)
		if prepared != nil || cleanup == nil || !errors.Is(err, canary) || cleanup() != nil {
			t.Fatalf("legacy seal failure = %#v, cleanup=%t, err=%v", prepared, cleanup != nil, err)
		}
	})
}

func consumptionBackupParent(t *testing.T) string {
	t.Helper()
	parent := filepath.Join(t.TempDir(), "backups")
	if err := ensurePrivateBackupDirectory(parent); err != nil {
		t.Fatal(err)
	}
	return parent
}
