//go:build unix

package migration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestWalkLegacyInputsRejectsRealNamedPipes(t *testing.T) {
	root := t.TempDir()
	pipe := filepath.Join(root, "legacy.pipe")
	if err := unix.Mkfifo(pipe, 0o600); err != nil {
		t.Skipf("named pipes unavailable: %v", err)
	}
	if err := walkLegacyInputs(t.Context(), pipe, filepath.Join(root, "backup"), nil, func(string) error {
		return nil
	}); err == nil {
		t.Fatal("named-pipe legacy root was accepted")
	}

	tree := filepath.Join(root, "tree")
	if err := os.Mkdir(tree, 0o700); err != nil {
		t.Fatal(err)
	}
	treePipe := filepath.Join(tree, "nested.pipe")
	if err := unix.Mkfifo(treePipe, 0o600); err != nil {
		t.Skipf("named pipes unavailable: %v", err)
	}
	if err := walkLegacyInputs(t.Context(), tree, filepath.Join(root, "backup"), nil, func(string) error {
		return nil
	}); err == nil {
		t.Fatal("named-pipe legacy tree member was accepted")
	}
}

func TestSnapshotPropagatesRealUnreadableSourceFailures(t *testing.T) {
	for _, test := range []struct {
		name       string
		generation bool
	}{
		{name: "database generation", generation: true},
		{name: "legacy input"},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			source := filepath.Join(home, "source")
			if err := os.WriteFile(source, []byte("private"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(source, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(source, 0o600) })
			if file, err := os.Open(source); err == nil {
				_ = file.Close()
				t.Skip("current user can open mode-000 files")
			}

			spec := storecatalog.Spec{ID: "global.test", Path: filepath.Join(home, "store.db")}
			if test.generation {
				spec.Path = source
			} else {
				spec.LegacyRoots = []string{source}
			}
			physical := &storecatalog.Catalog{Home: home, Specs: []storecatalog.Spec{spec}}
			engine := &Engine{home: home, config: &config.Config{}, now: time.Now}
			session, err := engine.snapshot(
				t.Context(), physical, []storecatalog.Spec{spec}, filepath.Join(home, "backups"),
			)
			if err == nil || session == nil || !strings.Contains(err.Error(), "permission denied") {
				t.Fatalf("unreadable snapshot source = %#v, %v", session, err)
			}
		})
	}
}

func TestBackupHelpersPropagateRealDirectoryPermissionFailures(t *testing.T) {
	t.Run("create backup directory", func(t *testing.T) {
		parent := t.TempDir()
		if err := os.Chmod(parent, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
		target := filepath.Join(parent, "backup")
		if err := ensurePrivateBackupDirectory(target); err == nil {
			_ = os.RemoveAll(target)
			t.Skip("current user can create files in a non-writable directory")
		}
	})

	t.Run("walk unreadable directory", func(t *testing.T) {
		root := t.TempDir()
		locked := filepath.Join(root, "locked")
		if err := os.Mkdir(locked, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(locked, "legacy.json"), []byte("legacy"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(locked, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
		if err := walkLegacyInputs(
			t.Context(), root, filepath.Join(root, "backup"), nil, func(string) error { return nil },
		); err == nil {
			t.Skip("current user can traverse mode-000 directories")
		}
	})
}

func TestValidateBackupAncestorsRejectsExistingSymlinkAlias(t *testing.T) {
	root := t.TempDir()
	realParent := filepath.Join(root, "real")
	child := filepath.Join(realParent, "child")
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(realParent, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := validateBackupAncestors(filepath.Join(alias, "child")); err == nil {
		t.Fatal("existing directory reached through a symlink alias was accepted")
	}
}

func TestWalkLegacyInputsPropagatesConcurrentRemoval(t *testing.T) {
	root := t.TempDir()
	trigger := filepath.Join(root, "a-trigger.json")
	removed := filepath.Join(root, "z-removed.json")
	for _, path := range []string{trigger, removed} {
		if err := os.WriteFile(path, []byte("legacy"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	err := walkLegacyInputs(t.Context(), root, filepath.Join(root, "backup"), nil, func(path string) error {
		if path == trigger {
			return os.Remove(removed)
		}
		return nil
	})
	if err == nil || !os.IsNotExist(err) {
		t.Fatalf("concurrent legacy removal = %v", err)
	}
}
