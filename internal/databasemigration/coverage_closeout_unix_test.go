//go:build unix

package databasemigration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/storecatalog"
)

func TestUnixLegacyTraversalRejectsIrregularAndUnreadableInputs(t *testing.T) {
	root := t.TempDir()
	pipe := filepath.Join(root, "legacy-pipe")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := walkLegacyInputs(
		t.Context(), pipe, t.TempDir(), nil, newBackupBudget(), func(string) error { return nil },
	); err == nil {
		t.Fatal("named-pipe legacy root succeeded")
	}
	tree := filepath.Join(root, "tree")
	if err := os.Mkdir(tree, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(tree, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := walkLegacyInputs(
		t.Context(), tree, t.TempDir(), nil, newBackupBudget(), func(string) error { return nil },
	); err == nil {
		t.Fatal("named-pipe legacy tree member succeeded")
	}

	if os.Geteuid() == 0 {
		return
	}
	blocked := filepath.Join(root, "blocked")
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })
	if err := walkLegacyDirectory(
		t.Context(), blocked, ".", t.TempDir(), nil, newBackupBudget(), func(string) error { return nil },
	); err == nil {
		t.Fatal("unreadable legacy directory succeeded")
	}
}

func TestUnixBackupCreationAndCopyPermissionFailures(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses ordinary filesystem permissions")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	if err := ensurePrivateBackupDirectory(filepath.Join(parent, "child")); err == nil {
		t.Fatal("backup directory creation below non-writable parent succeeded")
	}

	sourceRoot := t.TempDir()
	source := filepath.Join(sourceRoot, "source")
	writeMigrationFile(t, source, []byte("payload"))
	if err := os.Chmod(source, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(source, 0o600) })
	if _, err := copyBackupFile(
		t.Context(), t.TempDir(), "global/auth", "legacy", source, "copy", newBackupBudget(),
	); err == nil {
		t.Fatal("unreadable backup source copied")
	}

	blockedRoot := filepath.Join(t.TempDir(), "root-file")
	writeMigrationFile(t, blockedRoot, nil)
	readable := filepath.Join(t.TempDir(), "readable")
	writeMigrationFile(t, readable, []byte("payload"))
	if _, err := copyBackupFile(
		t.Context(), blockedRoot, "global/auth", "legacy", readable,
		filepath.Join("nested", "copy"), newBackupBudget(),
	); err == nil {
		t.Fatal("copy below regular-file backup root succeeded")
	}
}

func TestUnixSnapshotPropagatesUnreadableGenerationAndLegacy(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses ordinary file permissions")
	}
	for _, test := range []struct {
		name   string
		legacy bool
	}{
		{name: "generation"},
		{name: "legacy", legacy: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := migrationHome(t)
			spec := storecatalog.Spec{ID: "global/test", Path: filepath.Join(home, "store.db")}
			source := spec.Path
			if test.legacy {
				source = filepath.Join(home, "legacy.json")
				spec.LegacyRoots = []string{source}
			}
			writeMigrationFile(t, source, []byte("payload"))
			if err := os.Chmod(source, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(source, 0o600) })
			engine := &Engine{now: time.Now}
			session, err := engine.snapshot(
				t.Context(), home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec},
				filepath.Join(home, "backups"),
			)
			if session == nil || err == nil {
				t.Fatalf("unreadable %s snapshot = %#v, %v", test.name, session, err)
			}
		})
	}
}

func TestUnixPrepareLegacyInputsPropagatesMkdirTempFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses ordinary directory permissions")
	}
	session, _ := verifiedBackupFixture(t, []byte("legacy"))
	parent := filepath.Dir(session.root)
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	source := session.manifest.Files[0].Source
	roots, cleanup, err := session.prepareLegacyInputs(context.Background(), storecatalog.Spec{
		ID: "global/auth", LegacyRoots: []string{source},
	})
	if roots != nil || cleanup == nil || err == nil {
		t.Fatalf("unwritable disposable parent = %q, %v", roots, err)
	}
}

func TestMigrationRelativePathsFailFromRemovedWorkingDirectory(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	removed := filepath.Join(t.TempDir(), "removed")
	if err := os.Mkdir(removed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(removed); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(original); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
	if err := os.Remove(removed); err != nil {
		if errors.Is(err, syscall.EBUSY) {
			t.Skip("platform cannot unlink current working directory")
		}
		t.Fatal(err)
	}
	if err := validateBackupAncestors("relative-backup"); err == nil {
		t.Fatal("relative backup ancestor resolved from removed working directory")
	}
	if path, err := validateBackupParent("relative-backup", original, nil); path != "" || err == nil {
		t.Fatalf("relative backup parent = %q, %v", path, err)
	}
}

func TestUnixExistingSymlinkBackupAncestorIsRejected(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := validateBackupAncestors(filepath.Join(alias, "missing")); err == nil ||
		!strings.Contains(err.Error(), "unsafe ancestor") {
		t.Fatalf("symlink backup ancestor = %v", err)
	}
}
