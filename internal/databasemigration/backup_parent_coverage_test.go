package databasemigration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/internal/storecatalog"
)

func TestBackupParentValidationCoverage(t *testing.T) {
	home := migrationHome(t)
	store := filepath.Join(home, "store.db")
	legacy := filepath.Join(home, "legacy")
	for _, test := range []struct {
		name  string
		ctx   context.Context
		value string
		home  string
		specs []storecatalog.Spec
		want  string
	}{
		{name: "nil context uses default", value: "", home: home},
		{name: "invalid home", ctx: t.Context(), value: "", home: "relative", want: "home is invalid"},
		{name: "unclean configured value", ctx: t.Context(), value: " " + home, home: home, want: "directory is invalid"},
		{name: "generation overlap", ctx: t.Context(), value: store, home: home,
			specs: []storecatalog.Spec{{ID: "global/tree", Path: store}}, want: "overlaps a database generation"},
		{name: "legacy overlap", ctx: t.Context(), value: legacy, home: home,
			specs: []storecatalog.Spec{{ID: "global/tree", Path: store, LegacyRoots: []string{legacy}}},
			want:  "overlaps a legacy input"},
		{name: "canceled catalog iteration", ctx: &cancelAfterMigrationErrChecks{Context: t.Context(), allowed: 1},
			value: filepath.Join(home, "archive"), home: home,
			specs: []storecatalog.Spec{{ID: "global/tree", Path: store}}, want: context.Canceled.Error()},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := validateBackupParentWithContext(test.ctx, test.value, test.home, test.specs)
			if test.want == "" {
				if err != nil || got != filepath.Join(home, "backups") {
					t.Fatalf("validateBackupParentWithContext() = %q, %v", got, err)
				}
				return
			}
			parentTreeRequireError(t, err, test.want)
			if got != "" {
				t.Fatalf("rejected parent = %q", got)
			}
		})
	}

	if owned, err := exclusivelyCreateMissingBackupParent("bad\x00parent", true); err == nil || owned {
		t.Fatalf("invalid exclusive creation = %t, %v", owned, err)
	}
}

func TestBackupParentProspectiveRaceCoverage(t *testing.T) {
	for _, test := range []struct {
		name   string
		parent func(string) string
		action func(*testing.T, string)
		want   string
	}{
		{
			name:   "parent appears",
			parent: func(base string) string { return filepath.Join(base, "archive") },
			action: func(t *testing.T, parent string) {
				if err := os.Mkdir(parent, 0o700); err != nil {
					t.Fatal(err)
				}
			},
			want: "appeared during prospective containment validation",
		},
		{
			name:   "nearest ancestor changes",
			parent: func(base string) string { return filepath.Join(base, "new", "archive") },
			action: func(t *testing.T, parent string) {
				if err := os.Mkdir(filepath.Dir(parent), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			want: "ancestor changed during containment validation",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := t.TempDir()
			parent := test.parent(base)
			ctx := &actionAfterMigrationErrChecks{
				Context: t.Context(), allowed: 2, action: func() { test.action(t, parent) },
			}
			spec := storecatalog.Spec{
				ID: "global/tree", Path: filepath.Join(base, "store", "store.db"),
				LegacyRoots: []string{filepath.Join(base, "absent-legacy")},
			}
			parentTreeRequireError(t,
				validateBackupParentPhysicalAliasesBoundContext(
					ctx, parent, fileidentity.Identity{}, []storecatalog.Spec{spec},
				), test.want)
		})
	}

	base := t.TempDir()
	identity := parentTreeIdentity(t, base)
	if err := validateBackupParentPhysicalAliasesBoundContext(
		nil, filepath.Join(base, "missing"), fileidentity.Identity{}, nil,
	); err != nil {
		t.Fatalf("nil-context prospective parent = %v", err)
	}
	parentTreeRequireError(t, validateBackupParentPhysicalAliasesBoundContext(
		t.Context(), filepath.Join(base, "missing"), identity, nil,
	), "binding changed before containment validation")

	parent := filepath.Join(base, "parent")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	parentTreeRequireError(t, validateBackupParentPhysicalAliasesBoundContext(
		t.Context(), parent, fileidentity.Identity{}, []storecatalog.Spec{{
			ID: "global/tree", Path: filepath.Join(base, "store.db"),
			LegacyRoots: []string{filepath.Join(base, "bad\x00legacy")},
		}},
	), "inspect a legacy input")
}

func TestBackupParentAncestorAndLegacyRootCoverage(t *testing.T) {
	base := t.TempDir()
	identity := parentTreeIdentity(t, base)
	for _, test := range []struct {
		name  string
		path  string
		id    fileidentity.Identity
		state *backupParentAncestorState
		want  string
	}{
		{name: "missing identity", path: base, state: &backupParentAncestorState{seen: map[string]struct{}{}}, want: "state is invalid"},
		{name: "missing state", path: base, id: identity, want: "state is invalid"},
		{name: "canceled", path: base, id: identity,
			state: &backupParentAncestorState{ctx: canceledParentTreeContext(), seen: map[string]struct{}{}},
			want:  context.Canceled.Error()},
		{name: "invalid ancestor path", path: filepath.Join(base, "bad\x00parent", "child"), id: identity,
			state: &backupParentAncestorState{ctx: t.Context(), seen: map[string]struct{}{}},
			want:  "inspect catalog source ancestor"},
	} {
		t.Run(test.name, func(t *testing.T) {
			parentTreeRequireError(t,
				validateBackupParentOutsideSourceAncestors(test.path, test.id, test.state), test.want)
		})
	}

	for _, test := range []struct {
		name  string
		ctx   context.Context
		id    fileidentity.Identity
		roots []string
		want  string
	}{
		{name: "missing parent identity", ctx: t.Context(), want: "identity is unavailable"},
		{name: "canceled roots", ctx: canceledParentTreeContext(), id: identity, roots: []string{base}, want: context.Canceled.Error()},
		{name: "invalid root", ctx: t.Context(), id: identity,
			roots: []string{filepath.Join(base, "bad\x00legacy")}, want: "inspect legacy root"},
	} {
		t.Run(test.name, func(t *testing.T) {
			parentTreeRequireError(t,
				validateBackupParentOutsideLegacyTrees(test.ctx, test.id, test.roots), test.want)
		})
	}

	if _, _, _, err := nearestExistingBackupDirectoryIdentity(filepath.Join(base, "bad\x00ancestor")); err == nil {
		t.Fatal("invalid nearest-ancestor lookup succeeded")
	}
	if !backupPathsOverlap(base, base) {
		t.Fatal("equal backup paths did not overlap")
	}
}

func TestBackupParentLegacyScanRaceCoverage(t *testing.T) {
	t.Run("root binding replacement", func(t *testing.T) {
		base := t.TempDir()
		legacy := filepath.Join(base, "legacy")
		if err := os.Mkdir(legacy, 0o700); err != nil {
			t.Fatal(err)
		}
		writeMigrationFile(t, filepath.Join(legacy, "member"), []byte("member"))
		other := filepath.Join(base, "other")
		if err := os.Mkdir(other, 0o700); err != nil {
			t.Fatal(err)
		}
		ctx := &actionAfterMigrationErrChecks{Context: t.Context(), allowed: 2, action: func() {
			parentTreeReplaceDirectory(t, legacy)
		}}
		err := scanBackupLegacyDirectoriesForParent(legacy, 0, &backupParentLegacyScan{
			ctx: ctx, forbidden: parentTreeIdentity(t, other),
		})
		parentTreeRequireError(t, err, "binding changed")
	})

	t.Run("scan directory input and entry bounds", func(t *testing.T) {
		base := t.TempDir()
		legacy := filepath.Join(base, "legacy")
		if err := os.Mkdir(legacy, 0o700); err != nil {
			t.Fatal(err)
		}
		other := filepath.Join(base, "other")
		if err := os.Mkdir(other, 0o700); err != nil {
			t.Fatal(err)
		}
		forbidden := parentTreeIdentity(t, other)
		parentTreeRequireError(t, scanBackupLegacyDirectoriesForParent(
			legacy, 0, &backupParentLegacyScan{ctx: canceledParentTreeContext(), forbidden: forbidden},
		), context.Canceled.Error())
		writeMigrationFile(t, filepath.Join(base, "regular"), []byte("regular"))
		parentTreeRequireError(t, scanBackupLegacyDirectoriesForParent(
			filepath.Join(base, "regular"), 0, &backupParentLegacyScan{forbidden: forbidden},
		), "directory is unsafe")

		root := parentTreeRoot(t, legacy)
		before, err := os.Lstat(legacy)
		if err != nil {
			t.Fatal(err)
		}
		identity := parentTreeIdentity(t, legacy)
		parentTreeRequireError(t, scanBackupLegacyDirectoryRoot(
			legacy, 0, &backupParentLegacyScan{entries: backupMaxEntries, forbidden: forbidden},
			root, identity, before,
		), "entry limit")
	})

	t.Run("closed root and mismatched identity", func(t *testing.T) {
		legacy := t.TempDir()
		before, err := os.Lstat(legacy)
		if err != nil {
			t.Fatal(err)
		}
		root, err := os.OpenRoot(legacy)
		if err != nil {
			t.Fatal(err)
		}
		if err := root.Close(); err != nil {
			t.Fatal(err)
		}
		identity := parentTreeIdentity(t, legacy)
		state := &backupParentLegacyScan{forbidden: parentTreeIdentity(t, t.TempDir())}
		parentTreeRequireError(t,
			scanBackupLegacyDirectoryRoot(legacy, 0, state, root, identity, before),
			"open legacy containment directory")

		root = parentTreeRoot(t, legacy)
		parentTreeRequireError(t, scanBackupLegacyDirectoryRoot(
			legacy, 0, state, root, parentTreeIdentity(t, t.TempDir()), before,
		), "changed while opening")
	})

	for _, test := range []struct {
		name    string
		entries int
		ctx     func(*testing.T, string) context.Context
		want    string
	}{
		{name: "canceled before child inspection", ctx: func(t *testing.T, _ string) context.Context {
			return &cancelAfterMigrationErrChecks{Context: t.Context(), allowed: 1}
		}, want: context.Canceled.Error()},
		{name: "child vanishes", ctx: func(t *testing.T, child string) context.Context {
			return &actionAfterMigrationErrChecks{Context: t.Context(), allowed: 1, action: func() {
				if err := os.Remove(child); err != nil {
					t.Fatal(err)
				}
			}}
		}, want: "inspect legacy containment child"},
		{name: "regular entry exceeds bound", entries: backupMaxEntries - 1,
			ctx: func(t *testing.T, _ string) context.Context { return t.Context() }, want: "entry limit"},
		{name: "root metadata changes", ctx: func(t *testing.T, child string) context.Context {
			return &actionAfterMigrationErrChecks{Context: t.Context(), allowed: 1, action: func() {
				if err := os.Chmod(filepath.Dir(child), 0o755); err != nil {
					t.Fatal(err)
				}
			}}
		}, want: "directory changed during scan"},
	} {
		t.Run(test.name, func(t *testing.T) {
			legacy := t.TempDir()
			child := filepath.Join(legacy, "member")
			writeMigrationFile(t, child, []byte("member"))
			t.Cleanup(func() { _ = os.Chmod(legacy, 0o700) })
			root := parentTreeRoot(t, legacy)
			before, err := os.Lstat(legacy)
			if err != nil {
				t.Fatal(err)
			}
			state := &backupParentLegacyScan{
				ctx: test.ctx(t, child), entries: test.entries,
				forbidden: parentTreeIdentity(t, t.TempDir()),
			}
			parentTreeRequireError(t, scanBackupLegacyDirectoryRoot(
				legacy, 0, state, root, parentTreeIdentity(t, legacy), before,
			), test.want)
		})
	}

	t.Run("recursive depth", func(t *testing.T) {
		legacy := t.TempDir()
		if err := os.Mkdir(filepath.Join(legacy, "child"), 0o700); err != nil {
			t.Fatal(err)
		}
		root := parentTreeRoot(t, legacy)
		before, err := os.Lstat(legacy)
		if err != nil {
			t.Fatal(err)
		}
		parentTreeRequireError(t, scanBackupLegacyDirectoryRoot(
			legacy, backupMaxDepth,
			&backupParentLegacyScan{ctx: t.Context(), forbidden: parentTreeIdentity(t, t.TempDir())},
			root, parentTreeIdentity(t, legacy), before,
		), "scan limit")
	})

	for _, test := range []struct {
		name string
		ctx  func(*testing.T, string) context.Context
		want string
	}{
		{name: "child binding replacement", ctx: func(t *testing.T, child string) context.Context {
			return &actionAfterMigrationErrChecks{Context: t.Context(), allowed: 3, action: func() {
				parentTreeReplaceDirectory(t, child)
			}}
		}, want: "child binding changed"},
		{name: "canceled after child scan", ctx: func(t *testing.T, _ string) context.Context {
			return &cancelAfterMigrationErrChecks{Context: t.Context(), allowed: 3}
		}, want: context.Canceled.Error()},
		{name: "child changes after binding", ctx: func(t *testing.T, child string) context.Context {
			return &actionAfterMigrationErrChecks{Context: t.Context(), allowed: 3, action: func() {
				parentTreeReplaceDirectory(t, child)
			}}
		}, want: "child changed during scan"},
	} {
		t.Run(test.name, func(t *testing.T) {
			legacy := t.TempDir()
			child := filepath.Join(legacy, "child")
			if err := os.Mkdir(child, 0o700); err != nil {
				t.Fatal(err)
			}
			if test.name == "child binding replacement" {
				writeMigrationFile(t, filepath.Join(child, "member"), []byte("member"))
			}
			root := parentTreeRoot(t, legacy)
			before, err := os.Lstat(legacy)
			if err != nil {
				t.Fatal(err)
			}
			parentTreeRequireError(t, scanBackupLegacyDirectoryRoot(
				legacy, 0,
				&backupParentLegacyScan{ctx: test.ctx(t, child), forbidden: parentTreeIdentity(t, t.TempDir())},
				root, parentTreeIdentity(t, legacy), before,
			), test.want)
		})
	}
}

func TestBackupParentLegacyBindingCoverage(t *testing.T) {
	base := t.TempDir()
	childPath := filepath.Join(base, "child")
	if err := os.Mkdir(childPath, 0o700); err != nil {
		t.Fatal(err)
	}
	parent := parentTreeRoot(t, base)
	child := parentTreeRoot(t, childPath)
	childIdentity := parentTreeIdentity(t, childPath)
	otherIdentity := parentTreeIdentity(t, t.TempDir())

	for _, test := range []struct {
		name     string
		parent   *os.Root
		leaf     string
		child    *os.Root
		expected fileidentity.Identity
		want     string
	}{
		{name: "nil parent", leaf: "child", child: child, expected: childIdentity, want: "binding is invalid"},
		{name: "missing child", parent: parent, leaf: "child", expected: childIdentity, want: "binding is invalid"},
		{name: "invalid leaf", parent: parent, leaf: "../child", child: child, expected: childIdentity, want: "binding is invalid"},
		{name: "missing bound leaf", parent: parent, leaf: "absent", child: child, expected: childIdentity, want: "no such file"},
		{name: "identity mismatch", parent: parent, leaf: "child", child: child, expected: otherIdentity, want: "binding changed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			parentTreeRequireError(t,
				validateBackupLegacyContainmentChildBinding(test.parent, test.leaf, test.child, test.expected),
				test.want)
		})
	}

	closedChild, err := os.OpenRoot(childPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := closedChild.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateBackupLegacyContainmentChildBinding(
		parent, "child", closedChild, childIdentity,
	); err == nil {
		t.Fatal("closed retained child binding succeeded")
	}

	missingPath := filepath.Join(base, "missing", "child")
	parentTreeRequireError(t, validateBackupLegacyContainmentRootPath(
		missingPath, child, childIdentity,
	), "parent is unsafe")
	parentTreeRequireError(t, validateBackupLegacyContainmentRootPath(
		"relative", child, childIdentity,
	), "root binding is invalid")

	filesystemRoot := string(os.PathSeparator)
	root := parentTreeRoot(t, filesystemRoot)
	rootIdentity := parentTreeIdentity(t, filesystemRoot)
	if err := validateBackupLegacyContainmentRootPath(filesystemRoot, root, rootIdentity); err != nil {
		t.Fatalf("filesystem-root binding = %v", err)
	}
	parentTreeRequireError(t, validateBackupLegacyContainmentRootPath(
		filesystemRoot, root, otherIdentity,
	), "root binding changed")
}
func canceledParentTreeContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func parentTreeIdentity(t *testing.T, path string) fileidentity.Identity {
	t.Helper()
	identity, exists, err := fileidentity.Existing(path)
	if err != nil || !exists || !identity.Valid() {
		t.Fatalf("identity for %q = %#v, %t, %v", path, identity, exists, err)
	}
	return identity
}

func parentTreeRoot(t *testing.T, path string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatalf("open root %q: %v", path, err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

func parentTreeReplaceDirectory(t *testing.T, path string) {
	t.Helper()
	if err := os.Rename(path, path+"-replaced"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func parentTreeRequireError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v; want substring %q", err, want)
	}
}
