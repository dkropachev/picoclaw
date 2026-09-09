//go:build linux

package databasemigration

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"golang.org/x/sys/unix"
)

func TestBackupParentContainmentRejectsBindAlias(t *testing.T) {
	legacy := t.TempDir()
	if err := os.Mkdir(filepath.Join(legacy, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Mkdir(alias, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount(legacy, alias, "", unix.MS_BIND, ""); err != nil {
		if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) || errors.Is(err, unix.ENOSYS) {
			t.Skipf("bind mounts unavailable: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := unix.Unmount(alias, 0); err != nil &&
			!errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOENT) {
			t.Errorf("unmount parent-containment bind: %v", err)
		}
	})
	parent := filepath.Join(alias, "nested", "archive")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	spec := storecatalog.Spec{
		ID: "global/test", Path: filepath.Join(t.TempDir(), "store.db"),
		LegacyRoots: []string{legacy},
	}
	if err := validateBackupParentPhysicalAliases(parent, []storecatalog.Spec{spec}); err == nil ||
		!strings.Contains(err.Error(), "physically contains") {
		t.Fatalf("bind-aliased parent containment = %v", err)
	}
}

func TestBackupParentProspectiveBindAliasDoesNotCreateSourceDirectory(t *testing.T) {
	legacy := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Mkdir(alias, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount(legacy, alias, "", unix.MS_BIND, ""); err != nil {
		if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) || errors.Is(err, unix.ENOSYS) {
			t.Skipf("bind mounts unavailable: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := unix.Unmount(alias, 0); err != nil &&
			!errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOENT) {
			t.Errorf("unmount prospective-parent bind: %v", err)
		}
	})
	spec := storecatalog.Spec{
		ID: "global/test", Path: filepath.Join(t.TempDir(), "store.db"),
		LegacyRoots: []string{legacy},
	}
	parent, err := validateBackupParentWithContext(t.Context(), "", alias, []storecatalog.Spec{spec})
	if parent != "" || err == nil || !strings.Contains(err.Error(), "physically contains") {
		t.Fatalf("prospective bind-aliased parent = %q, %v", parent, err)
	}
	for _, path := range []string{filepath.Join(alias, "backups"), filepath.Join(legacy, "backups")} {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("prospective rejection created %q: %v", path, statErr)
		}
	}
}

func TestBackupParentProspectiveBindAliasRejectsGenerationProjection(t *testing.T) {
	physical := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Mkdir(alias, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount(physical, alias, "", unix.MS_BIND, ""); err != nil {
		if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) || errors.Is(err, unix.ENOSYS) {
			t.Skipf("bind mounts unavailable: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := unix.Unmount(alias, 0); err != nil &&
			!errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOENT) {
			t.Errorf("unmount prospective-generation bind: %v", err)
		}
	})
	parent := filepath.Join(alias, "backups")
	spec := storecatalog.Spec{
		ID: "global/test", Path: filepath.Join(physical, "backups", "store.db"),
	}
	selected, err := validateBackupParentWithContext(
		t.Context(), parent, physical, []storecatalog.Spec{spec},
	)
	if selected != "" || err == nil || !strings.Contains(err.Error(), "physically overlaps") {
		t.Fatalf("prospective generation bind alias = %q, %v", selected, err)
	}
	for _, path := range []string{parent, filepath.Join(physical, "backups")} {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("prospective generation rejection created %q: %v", path, statErr)
		}
	}
}

func TestBackupLegacyContainmentChildBindingRejectsReplacement(t *testing.T) {
	parent := t.TempDir()
	leaf := "legacy"
	path, retained := filepath.Join(parent, leaf), filepath.Join(parent, "retained")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	opened, err := openPinnedBackupChild(root, leaf)
	if err != nil {
		t.Fatal(err)
	}
	expected, objectType, err := fileidentity.Opened(opened)
	if err != nil || objectType != fileidentity.ObjectTypeDirectory {
		_ = opened.Close()
		t.Fatalf("opened legacy identity = %#v, %v, %v", expected, objectType, err)
	}
	child, err := openExactBackupRemovalRoot(root, leaf, opened)
	if closeErr := opened.Close(); err != nil || closeErr != nil {
		t.Fatalf("open retained legacy root = %v; close=%v", err, closeErr)
	}
	defer child.Close()
	if err := os.Rename(path, retained); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validateBackupLegacyContainmentChildBinding(
		root, leaf, child, expected,
	); err == nil || !strings.Contains(err.Error(), "binding changed") {
		t.Fatalf("replacement legacy binding = %v", err)
	}
}
