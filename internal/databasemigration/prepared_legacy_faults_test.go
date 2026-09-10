package databasemigration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/internal/storecatalog"
)

type preparedLegacyFaultFixture struct {
	prepared *preparedLegacyInputs
	cleanup  func() error
	manifest BackupManifest
	spec     storecatalog.Spec
	root     string
	roots    []string
}

func TestExpectedPreparedLegacyTreeRejectsAmbiguousProvenance(t *testing.T) {
	t.Run("valid nested inventory", func(t *testing.T) {
		root, roots, manifest, spec, store := preparedLegacyExpectedFixture(t, "directory")
		nestedSource := filepath.Join(spec.LegacyRoots[0], "nested", "auth.json")
		manifest.Files = []BackupFileManifest{
			preparedLegacyRecord(spec, nestedSource, 0),
			preparedLegacyRecord(storecatalog.Spec{ID: "global/models"}, nestedSource, 0),
		}

		expected, records, err := expectedPreparedLegacyTree(
			root, roots, manifest, backupProvenanceFromSpec(spec), store,
		)
		if err != nil {
			t.Fatal(err)
		}
		relativeFile, err := filepath.Rel(root, filepath.Join(roots[0], "nested", "auth.json"))
		if err != nil {
			t.Fatal(err)
		}
		if expected[relativeFile] != backupTreeFile || len(records) != 1 {
			t.Fatalf("nested expected tree = %#v, records = %#v", expected, records)
		}
	})

	t.Run("ordered root changed", func(t *testing.T) {
		root, roots, manifest, spec, store := preparedLegacyExpectedFixture(t, "directory")
		roots[0] = filepath.Join(root, "wrong-order")
		assertPreparedLegacyFault(t,
			expectedPreparedLegacyTreeError(root, roots, manifest, spec, store), "ordered-root")
	})

	t.Run("unsafe destination root", func(t *testing.T) {
		root, _, manifest, spec, store := preparedLegacyExpectedFixture(t, "directory")
		spec.LegacyRoots[0] = filepath.Join(filepath.Dir(spec.LegacyRoots[0]), strings.Repeat("x", 256))
		store.LegacyRoots = append([]string(nil), spec.LegacyRoots...)
		roots := preparedLegacyOrderedRoots(root, spec)
		assertPreparedLegacyFault(t,
			expectedPreparedLegacyTreeError(root, roots, manifest, spec, store), "root path")
	})

	t.Run("unsafe member component", func(t *testing.T) {
		root, roots, manifest, spec, store := preparedLegacyExpectedFixture(t, "directory")
		source := spec.LegacyRoots[0]
		for range backupMaxArchiveDepth + 1 {
			source = filepath.Join(source, "nested")
		}
		manifest.Files = []BackupFileManifest{preparedLegacyRecord(spec, source, 0)}
		assertPreparedLegacyFault(t,
			expectedPreparedLegacyTreeError(root, roots, manifest, spec, store), "member path")
	})

	t.Run("directory and member collide", func(t *testing.T) {
		root, roots, manifest, spec, store := preparedLegacyExpectedFixture(t, "directory")
		manifest.Files = []BackupFileManifest{preparedLegacyRecord(spec, spec.LegacyRoots[0], 0)}
		assertPreparedLegacyFault(t,
			expectedPreparedLegacyTreeError(root, roots, manifest, spec, store), "path collision")
	})

	t.Run("member and child collide", func(t *testing.T) {
		root, roots, manifest, spec, store := preparedLegacyExpectedFixture(t, "file")
		manifest.Files = []BackupFileManifest{
			preparedLegacyRecord(spec, spec.LegacyRoots[0], 0),
			preparedLegacyRecord(spec, filepath.Join(spec.LegacyRoots[0], "child"), 0),
		}
		assertPreparedLegacyFault(t,
			expectedPreparedLegacyTreeError(root, roots, manifest, spec, store), "file-directory")
	})

	t.Run("duplicate member", func(t *testing.T) {
		root, roots, manifest, spec, store := preparedLegacyExpectedFixture(t, "file")
		record := preparedLegacyRecord(spec, spec.LegacyRoots[0], 0)
		manifest.Files = []BackupFileManifest{record, record}
		assertPreparedLegacyFault(t,
			expectedPreparedLegacyTreeError(root, roots, manifest, spec, store), "path collision")
	})

	t.Run("member outside declared root", func(t *testing.T) {
		root, roots, manifest, spec, store := preparedLegacyExpectedFixture(t, "directory")
		manifest.Files = []BackupFileManifest{preparedLegacyRecord(
			spec, filepath.Join(filepath.Dir(spec.LegacyRoots[0]), "outside.json"), 0,
		)}
		assertPreparedLegacyFault(t,
			expectedPreparedLegacyTreeError(root, roots, manifest, spec, store), "outside")
	})
}

func TestSealPreparedLegacyInputsRejectsInvalidEvidence(t *testing.T) {
	t.Run("nil context retains a valid seal", func(t *testing.T) {
		fixture := newPreparedLegacyFaultFixture(t)
		sealed, err := sealPreparedLegacyInputs(
			nil, fixture.root, fixture.roots, fixture.manifest, fixture.spec,
		)
		if err != nil {
			t.Fatal(err)
		}
		if closeErr := sealed.close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	})

	t.Run("invalid root", func(t *testing.T) {
		fixture := newPreparedLegacyFaultFixture(t)
		sealed, err := sealPreparedLegacyInputs(
			t.Context(), "relative", fixture.roots, fixture.manifest, fixture.spec,
		)
		assertRejectedPreparedLegacySeal(t, sealed, err, "root is invalid")
	})

	t.Run("malformed manifest", func(t *testing.T) {
		fixture := newPreparedLegacyFaultFixture(t)
		fixture.manifest.CaptureMode = "mutable"
		sealed, err := sealPreparedLegacyInputs(
			t.Context(), fixture.root, fixture.roots, fixture.manifest, fixture.spec,
		)
		assertRejectedPreparedLegacySeal(t, sealed, err, "validate")
	})

	t.Run("unknown store", func(t *testing.T) {
		fixture := newPreparedLegacyFaultFixture(t)
		fixture.spec.ID = "global/models"
		sealed, err := sealPreparedLegacyInputs(
			t.Context(), fixture.root, fixture.roots, fixture.manifest, fixture.spec,
		)
		assertRejectedPreparedLegacySeal(t, sealed, err, "missing")
	})

	t.Run("changed root order", func(t *testing.T) {
		fixture := newPreparedLegacyFaultFixture(t)
		fixture.roots[0] = filepath.Join(fixture.root, "wrong")
		sealed, err := sealPreparedLegacyInputs(
			t.Context(), fixture.root, fixture.roots, fixture.manifest, fixture.spec,
		)
		assertRejectedPreparedLegacySeal(t, sealed, err, "ordered-root")
	})

	t.Run("missing root", func(t *testing.T) {
		fixture := newPreparedLegacyFaultFixture(t)
		missing := filepath.Join(filepath.Dir(fixture.root), "missing-prepared-root")
		sealed, err := sealPreparedLegacyInputs(
			t.Context(), missing, preparedLegacyOrderedRoots(missing, fixture.spec),
			fixture.manifest, fixture.spec,
		)
		assertRejectedPreparedLegacySeal(t, sealed, err, "identity is unavailable")
	})

	t.Run("canceled sealing", func(t *testing.T) {
		fixture := newPreparedLegacyFaultFixture(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		sealed, err := sealPreparedLegacyInputs(
			ctx, fixture.root, fixture.roots, fixture.manifest, fixture.spec,
		)
		if sealed != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled seal = %#v, %v", sealed, err)
		}
	})
}

func TestSealPreparedLegacyInputsRejectsTreeDrift(t *testing.T) {
	t.Run("missing directory", func(t *testing.T) {
		fixture := closedPreparedLegacyFaultFixture(t)
		if err := os.RemoveAll(fixture.roots[0]); err != nil {
			t.Fatal(err)
		}
		assertPreparedLegacyFixtureSealFails(t, fixture, "no such file")
	})

	t.Run("unsafe directory", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Unix permission assertion")
		}
		fixture := closedPreparedLegacyFaultFixture(t)
		if err := os.Chmod(fixture.roots[0], 0o755); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(fixture.roots[0], 0o700) })
		assertPreparedLegacyFixtureSealFails(t, fixture, "directory is unsafe")
	})

	t.Run("missing member", func(t *testing.T) {
		fixture := closedPreparedLegacyFaultFixture(t)
		if err := os.Remove(fixture.prepared.members[0].path); err != nil {
			t.Fatal(err)
		}
		assertPreparedLegacyFixtureSealFails(t, fixture, "no such file")
	})

	t.Run("member became directory", func(t *testing.T) {
		fixture := closedPreparedLegacyFaultFixture(t)
		memberPath := fixture.prepared.members[0].path
		if err := os.Remove(memberPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(memberPath, 0o700); err != nil {
			t.Fatal(err)
		}
		assertPreparedLegacyFixtureSealFails(t, fixture, "changed while sealing")
	})

	t.Run("public member", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Unix permission assertion")
		}
		fixture := closedPreparedLegacyFaultFixture(t)
		memberPath := fixture.prepared.members[0].path
		if err := os.Chmod(memberPath, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(memberPath, 0o600) })
		assertPreparedLegacyFixtureSealFails(t, fixture, "validate prepared legacy member")
	})

	t.Run("hard-linked member", func(t *testing.T) {
		fixture := closedPreparedLegacyFaultFixture(t)
		if err := os.Link(fixture.prepared.members[0].path, filepath.Join(t.TempDir(), "alias")); err != nil {
			t.Skipf("hard links unavailable: %v", err)
		}
		assertPreparedLegacyFixtureSealFails(t, fixture, "hard-link")
	})

	t.Run("changed member size", func(t *testing.T) {
		fixture := closedPreparedLegacyFaultFixture(t)
		if err := os.Truncate(fixture.prepared.members[0].path, 1); err != nil {
			t.Fatal(err)
		}
		assertPreparedLegacyFixtureSealFails(t, fixture, "size differs")
	})

	t.Run("changed member bytes", func(t *testing.T) {
		fixture := closedPreparedLegacyFaultFixture(t)
		member := fixture.prepared.members[0]
		mutatePreparedFileBytes(t, member.path, member.info)
		assertPreparedLegacyFixtureSealFails(t, fixture, "bytes differ")
	})

	t.Run("late unexpected member", func(t *testing.T) {
		fixture := closedPreparedLegacyFaultFixture(t)
		ctx := &actionAfterMigrationErrChecks{
			Context: t.Context(),
			action: func() {
				writeMigrationFile(t, filepath.Join(fixture.root, "000-unexpected"), []byte("x"))
			},
		}
		sealed, err := sealPreparedLegacyInputs(
			ctx, fixture.root, fixture.roots, fixture.manifest, fixture.spec,
		)
		if sealed != nil || err == nil ||
			!strings.Contains(err.Error(), "unexpected") &&
				!strings.Contains(err.Error(), "entry limit") {
			t.Fatalf("late unexpected prepared legacy member = %#v, %v", sealed, err)
		}
	})
}

func TestPreparedLegacyGuardRejectsCorruptedSealState(t *testing.T) {
	t.Run("closed seal", func(t *testing.T) {
		fixture := newPreparedLegacyFaultFixture(t)
		if err := fixture.prepared.close(); err != nil {
			t.Fatal(err)
		}
		assertPreparedLegacyFault(t, fixture.prepared.guard(nil), "seal is invalid")
		if err := fixture.prepared.close(); err != nil {
			t.Fatalf("second close = %v", err)
		}
	})

	t.Run("canceled guard and use", func(t *testing.T) {
		fixture := newPreparedLegacyFaultFixture(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := fixture.prepared.guard(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled guard = %v", err)
		}
		called := false
		err := fixture.prepared.use(ctx, func(context.Context, []string) error {
			called = true
			return nil
		})
		if !errors.Is(err, context.Canceled) || called {
			t.Fatalf("canceled use = %v, called = %t", err, called)
		}
	})

	t.Run("closed root descriptor", func(t *testing.T) {
		fixture := newPreparedLegacyFaultFixture(t)
		if err := fixture.prepared.rootFile.Close(); err != nil {
			t.Fatal(err)
		}
		assertPreparedLegacyFault(t, fixture.prepared.guard(t.Context()), "root identity changed")
	})

	t.Run("closed root handle", func(t *testing.T) {
		fixture := newPreparedLegacyFaultFixture(t)
		if err := fixture.prepared.rootHandle.Close(); err != nil {
			t.Fatal(err)
		}
		assertPreparedLegacyFault(t, fixture.prepared.guard(t.Context()), "file already closed")
	})

	t.Run("root path identity changed", func(t *testing.T) {
		fixture := newPreparedLegacyFaultFixture(t)
		moved := fixture.root + ".moved"
		if err := os.Rename(fixture.root, moved); err != nil {
			t.Skipf("cannot rename open prepared root: %v", err)
		}
		if err := os.Mkdir(fixture.root, 0o700); err != nil {
			t.Fatal(err)
		}
		assertPreparedLegacyFault(t, fixture.prepared.guard(t.Context()), "root identity changed")
	})

	t.Run("expected inventory omission", func(t *testing.T) {
		fixture := newPreparedLegacyFaultFixture(t)
		fixture.prepared.expected["sealed-but-absent"] = backupTreeFile
		defer delete(fixture.prepared.expected, "sealed-but-absent")
		assertPreparedLegacyFault(t, fixture.prepared.guard(t.Context()), "omits a sealed path")
	})

	t.Run("directory seal missing", func(t *testing.T) {
		fixture := newPreparedLegacyFaultFixture(t)
		directories := fixture.prepared.directories
		fixture.prepared.directories = nil
		defer func() { fixture.prepared.directories = directories }()
		assertPreparedLegacyFault(t, fixture.prepared.guard(t.Context()), "directory seal is missing")
	})

	t.Run("member seal missing", func(t *testing.T) {
		fixture := newPreparedLegacyFaultFixture(t)
		members := fixture.prepared.members
		fixture.prepared.members = nil
		defer func() { fixture.prepared.members = members }()
		assertPreparedLegacyFault(t, fixture.prepared.guard(t.Context()), "member seal is missing")
	})

	t.Run("inventory entry budget", func(t *testing.T) {
		fixture := newPreparedLegacyFaultFixture(t)
		expected := fixture.prepared.expected
		fixture.prepared.expected = map[string]backupTreeKind{".": backupTreeDirectory}
		defer func() { fixture.prepared.expected = expected }()
		assertPreparedLegacyFault(t, fixture.prepared.guard(t.Context()), "entry limit")
	})
}

func TestPreparedLegacyGuardRejectsPathAndHandleDrift(t *testing.T) {
	t.Run("directory identity changed", func(t *testing.T) {
		fixture := newPreparedLegacyFaultFixture(t)
		directoryPath := fixture.roots[0]
		if err := os.Rename(directoryPath, directoryPath+".moved"); err != nil {
			t.Skipf("cannot rename open prepared directory: %v", err)
		}
		if err := os.Mkdir(directoryPath, 0o700); err != nil {
			t.Fatal(err)
		}
		assertPreparedLegacyFault(t, fixture.prepared.guard(t.Context()), "directory identity changed")
	})

	t.Run("public directory", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Unix permission assertion")
		}
		fixture := newPreparedLegacyFaultFixture(t)
		if err := os.Chmod(fixture.roots[0], 0o755); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(fixture.roots[0], 0o700) })
		assertPreparedLegacyFault(t, fixture.prepared.guard(t.Context()), "validate prepared legacy directory")
	})

	t.Run("member path identity changed", func(t *testing.T) {
		fixture := newPreparedLegacyFaultFixture(t)
		member := fixture.prepared.members[0]
		if err := os.Rename(member.path, member.path+".moved"); err != nil {
			t.Skipf("cannot rename open prepared member: %v", err)
		}
		writeMigrationFile(t, member.path, []byte("legacy payload"))
		err := fixture.prepared.guard(t.Context())
		if err == nil || !strings.Contains(err.Error(), "member seal changed") &&
			!strings.Contains(err.Error(), "unexpected path") {
			t.Fatalf("prepared legacy member path replacement = %v", err)
		}
	})

	t.Run("closed member descriptor", func(t *testing.T) {
		fixture := newPreparedLegacyFaultFixture(t)
		if err := fixture.prepared.members[0].file.Close(); err != nil {
			t.Fatal(err)
		}
		assertPreparedLegacyFault(t, fixture.prepared.guard(t.Context()), "member seal changed")
	})

	t.Run("hash cancellation", func(t *testing.T) {
		fixture := newPreparedLegacyFaultFixture(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		err := guardPreparedLegacyMember(ctx, &fixture.prepared.members[0])
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled member hash = %v", err)
		}
	})

	t.Run("directory walk cancellation", func(t *testing.T) {
		fixture := newPreparedLegacyFaultFixture(t)
		directories, members := preparedLegacySealMaps(fixture.prepared)
		ctx := &cancelAfterMigrationErrChecks{Context: t.Context(), allowed: 1}
		err := fixture.prepared.guardDirectory(
			ctx, ".", directories, members, make(map[string]struct{}), new(int),
		)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled directory walk = %v", err)
		}
	})

	t.Run("duplicate traversal state", func(t *testing.T) {
		fixture := newPreparedLegacyFaultFixture(t)
		directories, members := preparedLegacySealMaps(fixture.prepared)
		firstChild := filepath.Base(filepath.Dir(fixture.roots[0]))
		seen := map[string]struct{}{firstChild: {}}
		err := fixture.prepared.guardDirectory(
			t.Context(), ".", directories, members, seen, new(int),
		)
		assertPreparedLegacyFault(t, err, "duplicate path")
	})
}

func newPreparedLegacyFaultFixture(t *testing.T) preparedLegacyFaultFixture {
	t.Helper()
	_, spec, session := additionalLiveSnapshot(t)
	prepared, cleanup, err := session.prepareLegacyInputs(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	fixture := preparedLegacyFaultFixture{
		prepared: prepared,
		cleanup:  cleanup,
		manifest: session.manifest,
		spec:     spec,
		root:     prepared.root,
		roots:    append([]string(nil), prepared.roots...),
	}
	t.Cleanup(func() { _ = fixture.cleanup() })
	return fixture
}

func closedPreparedLegacyFaultFixture(t *testing.T) preparedLegacyFaultFixture {
	t.Helper()
	fixture := newPreparedLegacyFaultFixture(t)
	if err := fixture.prepared.close(); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func preparedLegacyExpectedFixture(
	t *testing.T,
	kind string,
) (string, []string, BackupManifest, storecatalog.Spec, BackupStoreManifest) {
	t.Helper()
	home := migrationHome(t)
	root := filepath.Join(home, "prepared")
	spec := storecatalog.Spec{
		ID: "global/auth", Path: filepath.Join(home, "auth.db"),
		LegacyRoots: []string{filepath.Join(home, "source")},
	}
	store := BackupStoreManifest{
		StoreID: spec.ID.String(), Path: spec.Path,
		LegacyRoots: append([]string(nil), spec.LegacyRoots...), LegacyRootKinds: []string{kind},
	}
	return root, preparedLegacyOrderedRoots(root, spec), BackupManifest{}, spec, store
}

func preparedLegacyOrderedRoots(root string, spec storecatalog.Spec) []string {
	roots := make([]string, len(spec.LegacyRoots))
	for index, sourceRoot := range spec.LegacyRoots {
		roots[index] = filepath.Join(
			root, fmt.Sprintf("root-%06d", index), filepath.Base(filepath.Clean(sourceRoot)),
		)
	}
	return roots
}

func preparedLegacyRecord(
	spec storecatalog.Spec,
	source string,
	legacyRoot int,
) BackupFileManifest {
	return BackupFileManifest{
		StoreID: spec.ID.String(), Role: "legacy", LegacyRoot: legacyRoot, Source: source,
	}
}

func expectedPreparedLegacyTreeError(
	root string,
	roots []string,
	manifest BackupManifest,
	spec storecatalog.Spec,
	store BackupStoreManifest,
) error {
	_, _, err := expectedPreparedLegacyTree(
		root, roots, manifest, backupProvenanceFromSpec(spec), store,
	)
	return err
}

func assertPreparedLegacyFixtureSealFails(
	t *testing.T,
	fixture preparedLegacyFaultFixture,
	want string,
) {
	t.Helper()
	sealed, err := sealPreparedLegacyInputs(
		t.Context(), fixture.root, fixture.roots, fixture.manifest, fixture.spec,
	)
	assertRejectedPreparedLegacySeal(t, sealed, err, want)
}

func assertRejectedPreparedLegacySeal(
	t *testing.T,
	sealed *preparedLegacyInputs,
	err error,
	want string,
) {
	t.Helper()
	if sealed != nil {
		_ = sealed.close()
		t.Fatalf("unexpected prepared legacy seal: %#v", sealed)
	}
	assertPreparedLegacyFault(t, err, want)
}

func assertPreparedLegacyFault(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("prepared legacy error = %v, want substring %q", err, want)
	}
}

func preparedLegacySealMaps(
	prepared *preparedLegacyInputs,
) (map[string]preparedLegacyDirectory, map[string]*preparedLegacyMember) {
	directories := make(map[string]preparedLegacyDirectory, len(prepared.directories))
	for _, directory := range prepared.directories {
		directories[directory.relative] = directory
	}
	members := make(map[string]*preparedLegacyMember, len(prepared.members))
	for index := range prepared.members {
		members[prepared.members[index].relative] = &prepared.members[index]
	}
	return directories, members
}
