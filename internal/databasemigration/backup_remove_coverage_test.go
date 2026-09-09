package databasemigration

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

func TestBackupRemovalInjectedFileFaults(t *testing.T) {
	canary := errors.New("removal fault")
	for _, test := range []struct {
		name   string
		mutate func(*backupRemovalOps)
		ok     bool
	}{
		{"invalid ops", func(ops *backupRemovalOps) { ops.rename = nil }, false},
		{"before rename", func(ops *backupRemovalOps) {
			ops.beforeRename = func(*os.Root, string) error { return canary }
		}, false},
		{"collisions with handles", func(ops *backupRemovalOps) {
			ops.rename = func(root *os.Root, source, _ string, _ *os.File) (*os.File, error) {
				exact, err := root.Open(source)
				return exact, errors.Join(os.ErrExist, err)
			}
		}, false},
		{"collisions", func(ops *backupRemovalOps) {
			ops.rename = func(*os.Root, string, string, *os.File) (*os.File, error) { return nil, os.ErrExist }
		}, false},
		{"rename error with handle", func(ops *backupRemovalOps) {
			ops.rename = func(root *os.Root, source, _ string, _ *os.File) (*os.File, error) {
				exact, err := root.Open(source)
				return exact, errors.Join(canary, err)
			}
		}, false},
		{"sync", func(ops *backupRemovalOps) {
			ops.sync = func(*os.Root, string) error { return canary }
		}, false},
		{"sync with exact handle", func(ops *backupRemovalOps) {
			returnExactBackupRemovalHandle(ops)
			ops.sync = func(*os.Root, string) error { return canary }
		}, false},
		{"source reappears", func(ops *backupRemovalOps) {
			rename := ops.rename
			ops.rename = func(root *os.Root, source, target string, file *os.File) (*os.File, error) {
				exact, err := rename(root, source, target, file)
				return exact, errors.Join(err, root.WriteFile(source, []byte("decoy"), 0o600))
			}
		}, false},
		{"source reappears with exact handle", func(ops *backupRemovalOps) {
			returnExactBackupRemovalHandle(ops)
			rename := ops.rename
			ops.rename = func(root *os.Root, source, target string, file *os.File) (*os.File, error) {
				exact, err := rename(root, source, target, file)
				return exact, errors.Join(err, root.WriteFile(source, []byte("decoy"), 0o600))
			}
		}, false},
		{"quarantine substituted", func(ops *backupRemovalOps) {
			rename := ops.rename
			ops.rename = func(root *os.Root, source, target string, file *os.File) (*os.File, error) {
				exact, err := rename(root, source, target, file)
				err = errors.Join(err, root.Rename(target, "saved"), root.WriteFile(target, []byte("decoy"), 0o600))
				return exact, err
			}
		}, false},
		{"substituted with exact handle", func(ops *backupRemovalOps) {
			returnExactBackupRemovalHandle(ops)
			rename := ops.rename
			ops.rename = func(root *os.Root, source, target string, file *os.File) (*os.File, error) {
				exact, err := rename(root, source, target, file)
				err = errors.Join(err, root.Rename(target, "saved"), root.WriteFile(target, []byte("decoy"), 0o600))
				return exact, err
			}
		}, false},
		{"after quarantine", func(ops *backupRemovalOps) {
			ops.afterQuarantine = func(*os.Root, string) error { return canary }
		}, false},
		{"after quarantine with exact handle", func(ops *backupRemovalOps) {
			returnExactBackupRemovalHandle(ops)
			ops.afterQuarantine = func(*os.Root, string) error { return canary }
		}, false},
		{"before remove", func(ops *backupRemovalOps) {
			ops.beforeRemove = func(*os.Root, string) error { return canary }
		}, false},
		{"remove", func(ops *backupRemovalOps) {
			ops.remove = func(*os.Root, string, *os.File, bool) error { return canary }
		}, false},
		{"remove no effect", func(ops *backupRemovalOps) {
			ops.remove = func(*os.Root, string, *os.File, bool) error { return nil }
		}, false},
		{"successful exact handle", func(ops *backupRemovalOps) {
			returnExactBackupRemovalHandle(ops)
		}, true},
		{"closed exact handle", func(ops *backupRemovalOps) {
			returnExactBackupRemovalHandle(ops)
			rename := ops.rename
			ops.rename = func(root *os.Root, source, target string, file *os.File) (*os.File, error) {
				exact, err := rename(root, source, target, file)
				return exact, errors.Join(err, exact.Close())
			}
		}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			path, root, file, identity := newBackupRemovalFileFixture(t)
			ops := defaultBackupRemovalOps()
			test.mutate(&ops)
			err := removeBackupFileDurableWithOps(path, root, "file", file, identity, ops)
			if (err == nil) != test.ok {
				t.Fatalf("removal result = %v, want success %t", err, test.ok)
			}
		})
	}
}

func returnExactBackupRemovalHandle(ops *backupRemovalOps) {
	rename := ops.rename
	ops.rename = func(root *os.Root, source, target string, file *os.File) (*os.File, error) {
		exact, err := rename(root, source, target, file)
		if exact != nil || err != nil {
			return exact, err
		}
		exact, openErr := root.Open(target)
		return exact, errors.Join(err, openErr)
	}
}

func TestBackupRemovalHelperFaults(t *testing.T) {
	path, root, file, identity := newBackupRemovalFileFixture(t)
	var noExact *os.File
	requireBackupRemovalError(t, removeQuarantinedBackupLeaf(
		root, "file", "missing", file, &noExact, identity,
		fileidentity.ObjectTypeRegular, path, defaultBackupRemovalOps(),
	))
	_, _, quarantineErr := quarantineBackupRemovalLeaf(
		nil, "file", file, identity, fileidentity.ObjectTypeRegular,
		path, defaultBackupRemovalOps(),
	)
	requireBackupRemovalError(t, quarantineErr)
	if err := removeQuarantinedBackupLeaf(
		root, "file", "file", file, nil, identity,
		fileidentity.ObjectTypeRegular, path, defaultBackupRemovalOps(),
	); err == nil || !strings.Contains(err.Error(), "exact-handle") {
		t.Fatalf("nil exact state = %v", err)
	}
	requireBackupRemovalError(t, validateBackupRemovalTreeBinding(path, root, "file", nil, identity))
	requireBackupRemovalError(t, validatePinnedBackupRemovalRoot(path, nil, fileidentity.Identity{}))
	if _, identityErr := openedPinnedBackupRemovalRootIdentity("relative", root); identityErr == nil {
		t.Fatal("relative root identity succeeded")
	}
	closedRoot := mustBackupRemovalRoot(t, filepath.Dir(path))
	mustBackupRemovalOK(t, closedRoot.Close())
	_, identityErr := openedPinnedBackupRemovalRootIdentity(filepath.Dir(path), closedRoot)
	requireBackupRemovalError(t, identityErr)
	requireBackupRemovalError(t, requireMissingBackupRemovalRootLeaf(closedRoot, "file"))
	_, renameErr := renameBackupRemovalRootNoReplace(closedRoot, "file", "target", file)
	requireBackupRemovalError(t, renameErr)
	_ = syncBackupRemovalRoot(closedRoot)
	for _, leaf := range []string{"", "missing", "file"} {
		err := requireMissingBackupRemovalRootLeaf(root, leaf)
		if (err == nil) != (leaf == "missing") {
			t.Errorf("missing proof %q = %v", leaf, err)
		}
	}
	tree := filepath.Join(filepath.Dir(path), "tree")
	mustBackupRemovalOK(t, os.Mkdir(tree, 0o700))
	treeIdentity := backupRemovalDirectoryIdentity(t, tree)
	treeRoot := mustBackupRemovalChildRoot(t, root, "tree")
	defer treeRoot.Close()
	requireBackupRemovalError(t, validatePinnedBackupRemovalRoot(tree, treeRoot, identity))
	requireBackupRemovalError(t, matchBackupRemovalIdentity(
		tree, identity, fileidentity.ObjectTypeDirectory, fileidentity.ExistingWithType,
	))
	closedTree := mustBackupRemovalChildRoot(t, root, "tree")
	mustBackupRemovalOK(t, closedTree.Close())
	requireBackupRemovalError(t, validateBackupRemovalTreeBinding(tree, root, "tree", closedTree, treeIdentity))
	requireBackupRemovalError(t, validatePinnedBackupTreeInventory(
		tree, closedTree, treeIdentity,
		map[fileidentity.Identity]string{treeIdentity: tree}, new(int),
	))
	requireBackupRemovalError(t, validateBackupRemovalInventoryPlan(
		tree, closedTree, treeIdentity,
		map[fileidentity.Identity]string{treeIdentity: tree},
	))
	planned := map[fileidentity.Identity]string{treeIdentity: tree, identity: filepath.Join(tree, "missing")}
	requireBackupRemovalError(t, validateBackupRemovalInventoryPlan(tree, treeRoot, treeIdentity, planned))
	entries := 0
	if err := removePinnedBackupTreeContentsBound(
		tree, treeRoot, treeIdentity, planned, &entries, defaultBackupRemovalOps(),
	); err == nil || !strings.Contains(err.Error(), "inventory remains") {
		t.Fatalf("remaining captured inventory = %v", err)
	}
	writeMigrationFile(t, filepath.Join(tree, "late"), []byte("late"))
	entries = 0
	if err := removePinnedBackupTreeContentsBound(
		tree, treeRoot, treeIdentity,
		map[fileidentity.Identity]string{treeIdentity: tree}, &entries,
		defaultBackupRemovalOps(),
	); err == nil || !strings.Contains(err.Error(), "not captured") {
		t.Fatalf("uncaptured child = %v", err)
	}
	if !backupRemovalPlanHasDescendant(planned, tree) || backupRemovalPlanHasDescendant(planned, path) {
		t.Fatal("captured descendant classification is wrong")
	}
	requireBackupRemovalError(t, removePinnedBackupTreeIdentity("relative", treeIdentity))
	requireBackupRemovalError(t, removePinnedBackupTreeContentsBound(
		"relative", nil, fileidentity.Identity{}, nil, new(int), defaultBackupRemovalOps(),
	))
	if err := removePinnedBackupTreeIdentity(filepath.Join(filepath.Dir(path), "missing"), treeIdentity); err != nil {
		t.Fatalf("missing identity removal = %v", err)
	}
	link := filepath.Join(filepath.Dir(path), "tree-link")
	if err := os.Symlink(tree, link); err == nil {
		if err := removePinnedBackupTreeIdentity(link, treeIdentity); err == nil {
			t.Fatal("symlink identity removal succeeded")
		}
	}
	publishSource := filepath.Join(filepath.Dir(path), "publish-source")
	mustBackupRemovalOK(t, os.Mkdir(publishSource, 0o700))
	_ = publishBackupDirectory(publishSource, publishSource+"-target")
}

func TestBackupRemovalPathDriftFaults(t *testing.T) {
	path, root, file, identity := newBackupRemovalFileFixture(t)
	parent := filepath.Dir(path)
	moved := parent + "-moved"
	mustBackupRemovalOK(t, os.Rename(parent, moved))
	mustBackupRemovalOK(t, os.Mkdir(parent, 0o700))
	if err := validateBackupRemovalBinding(
		path, root, "file", file, identity, fileidentity.ObjectTypeRegular,
	); err == nil || !strings.Contains(err.Error(), "parent binding") {
		t.Fatalf("replaced parent binding = %v", err)
	}
	tree := filepath.Join(moved, "tree")
	mustBackupRemovalOK(t, os.Mkdir(tree, 0o700))
	treeIdentity := backupRemovalDirectoryIdentity(t, tree)
	treeRoot := mustBackupRemovalRoot(t, tree)
	defer treeRoot.Close()
	mustBackupRemovalOK(t, os.Rename(tree, tree+"-moved"))
	mustBackupRemovalOK(t, os.Mkdir(tree, 0o700))
	for _, remove := range []func(string, *os.Root, map[fileidentity.Identity]string, *int) error{
		removePinnedBackupTreeContents,
		func(path string, root *os.Root, identities map[fileidentity.Identity]string, entries *int) error {
			return removePinnedBackupTreeContentsWithSync(path, root, identities, entries, func(string) error { return nil })
		},
	} {
		entries := 0
		if err := remove(tree, treeRoot, map[fileidentity.Identity]string{treeIdentity: tree}, &entries); err == nil {
			t.Fatal("replaced root path removal succeeded")
		}
	}
}

func TestBackupRemovalInjectedTreeFaults(t *testing.T) {
	canary := errors.New("tree fault")
	for _, test := range []struct {
		name   string
		mutate func(*backupRemovalOps)
	}{
		{"invalid ops", func(ops *backupRemovalOps) { ops.remove = nil }},
		{"exact close after quarantine", func(ops *backupRemovalOps) {
			returnExactBackupRemovalHandle(ops)
			ops.afterQuarantine = func(*os.Root, string) error { return canary }
		}},
		{"source reappears", func(ops *backupRemovalOps) {
			ops.afterQuarantine = func(root *os.Root, _ string) error { return root.Mkdir("tree", 0o700) }
		}},
		{"nested removal", func(ops *backupRemovalOps) {
			calls := 0
			ops.beforeRename = func(*os.Root, string) error {
				calls++
				if calls > 1 {
					return canary
				}
				return nil
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent := migrationHome(t)
			path := filepath.Join(parent, "tree")
			writeBackupRemovalTree(t, path)
			identity := backupRemovalDirectoryIdentity(t, path)
			root, child := openBackupRemovalTree(t, parent, "tree")
			ops := defaultBackupRemovalOps()
			test.mutate(&ops)
			if err := removeBackupTreeDurableWithOps(
				path, root, "tree", child, identity, ops,
			); err == nil {
				t.Fatal("faulted tree removal succeeded")
			}
		})
	}
}

func TestBackupRemovalSyncWrappers(t *testing.T) {
	canary := errors.New("sync fault")
	path, root, file, identity := newBackupRemovalFileFixture(t)
	if err := removeBackupFileDurableWithSync(
		path, root, "file", file, identity, func(string) error { return canary },
	); !errors.Is(err, canary) {
		t.Fatalf("file sync fault = %v", err)
	}
	parent := migrationHome(t)
	tree := filepath.Join(parent, "tree")
	mustBackupRemovalOK(t, os.Mkdir(tree, 0o700))
	treeIdentity := backupRemovalDirectoryIdentity(t, tree)
	parentRoot, child := openBackupRemovalTree(t, parent, "tree")
	if err := removeBackupTreeDurableWithSync(
		tree, parentRoot, "tree", child, treeIdentity,
		func(string) error { return canary },
	); !errors.Is(err, canary) {
		t.Fatalf("tree sync fault = %v", err)
	}
	for _, err := range []error{
		removeBackupFileDurableWithSync("", nil, "", nil, fileidentity.Identity{}, nil),
		removeBackupTreeDurableWithSync("", nil, "", nil, fileidentity.Identity{}, nil),
	} {
		if err == nil {
			t.Fatal("nil sync operation succeeded")
		}
	}
	empty := migrationHome(t)
	emptyRoot := mustBackupRemovalRoot(t, empty)
	defer emptyRoot.Close()
	emptyIdentity := backupRemovalDirectoryIdentity(t, empty)
	emptyPlan := map[fileidentity.Identity]string{}
	requireBackupRemovalError(t, removePinnedBackupTreeContents(empty, emptyRoot, emptyPlan, new(int)))
	requireBackupRemovalError(t, removePinnedBackupTreeContentsWithSync(
		empty, emptyRoot, emptyPlan, new(int), func(string) error { return nil },
	))
	if err := removePinnedBackupTreeContentsWithSync(
		empty, emptyRoot, map[fileidentity.Identity]string{emptyIdentity: empty}, new(int), nil,
	); err == nil {
		t.Fatal("nil content sync succeeded")
	}
	if err := removePinnedBackupTreeContentsWithSync(
		empty, emptyRoot, map[fileidentity.Identity]string{emptyIdentity: empty}, new(int),
		func(string) error { return nil },
	); err != nil {
		t.Fatalf("empty content sync = %v", err)
	}
	requireBackupRemovalError(t, removePinnedBackupTreeContentsWithSync(
		"", nil, nil, nil, func(string) error { return nil },
	))
	mustBackupRemovalOK(t, emptyRoot.WriteFile("late", nil, 0o600))
	entries := backupMaxEntries
	requireBackupRemovalError(t, removePinnedBackupTreeContentsWithSync(
		empty, emptyRoot, map[fileidentity.Identity]string{emptyIdentity: empty}, &entries,
		func(string) error { return nil },
	))
}

func TestRemoveQuarantinedBackupLeafFaults(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*os.Root, *backupRemovalOps)
	}{
		{"source exists", func(root *os.Root, _ *backupRemovalOps) {
			if err := root.WriteFile("file", []byte("decoy"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"source reappears late", func(_ *os.Root, ops *backupRemovalOps) {
			ops.beforeRemove = func(root *os.Root, _ string) error {
				return root.WriteFile("file", []byte("decoy"), 0o600)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path, root, file, identity := newBackupRemovalFileFixture(t)
			mustBackupRemovalOK(t, root.Rename("file", "quarantine"))
			ops := defaultBackupRemovalOps()
			test.mutate(root, &ops)
			var exact *os.File
			if err := removeQuarantinedBackupLeaf(
				root, "file", "quarantine", file, &exact, identity,
				fileidentity.ObjectTypeRegular, path, ops,
			); err == nil || !strings.Contains(err.Error(), "source name changed") {
				t.Fatalf("quarantined source fault = %v", err)
			}
		})
	}
}

func newBackupRemovalFileFixture(
	t *testing.T,
) (string, *os.Root, *os.File, fileidentity.Identity) {
	t.Helper()
	parent := migrationHome(t)
	path := filepath.Join(parent, "file")
	writeMigrationFile(t, path, []byte("payload"))
	identity := mustBackupRemovalIdentity(t, path, fileidentity.ObjectTypeRegular)
	root := mustBackupRemovalRoot(t, parent)
	file := mustBackupRemovalFile(t, root, "file")
	t.Cleanup(func() { _ = file.Close(); _ = root.Close() })
	return path, root, file, identity
}

func mustBackupRemovalRoot(t *testing.T, path string) *os.Root {
	t.Helper()
	value, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func mustBackupRemovalChildRoot(t *testing.T, root *os.Root, path string) *os.Root {
	t.Helper()
	value, err := root.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func mustBackupRemovalFile(t *testing.T, root *os.Root, path string) *os.File {
	t.Helper()
	value, err := root.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func mustBackupRemovalIdentity(
	t *testing.T,
	path string,
	want fileidentity.ObjectType,
) fileidentity.Identity {
	t.Helper()
	value, err := backupExistingIdentity(path, want)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func mustBackupRemovalOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func requireBackupRemovalError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("unsafe backup removal operation succeeded")
	}
}
