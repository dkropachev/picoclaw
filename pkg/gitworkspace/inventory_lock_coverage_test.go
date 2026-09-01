//go:build android || darwin || dragonfly || freebsd || ios || linux || netbsd || openbsd || solaris

package gitworkspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestInventoryLockRejectsUnsafeFilesDirectoriesAndIdentities(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	blockingParent := filepath.Join(root, "parent-file")
	if err := os.WriteFile(blockingParent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if unlock, err := lockInventoryFile(
		context.Background(), filepath.Join(blockingParent, "lock"),
	); err == nil || unlock != nil {
		t.Fatalf("lock below regular parent = unlock:%v error:%v", unlock != nil, err)
	}
	lockDirectory := filepath.Join(root, "lock-directory")
	if err := os.Mkdir(lockDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if unlock, err := lockInventoryFile(context.Background(), lockDirectory); err == nil || unlock != nil {
		t.Fatalf("directory lock = unlock:%v error:%v", unlock != nil, err)
	}
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.lock")
	if err := os.Symlink(target, link); err == nil {
		if unlock, lockErr := lockInventoryFile(context.Background(), link); lockErr == nil || unlock != nil {
			t.Fatalf("symlink lock = unlock:%v error:%v", unlock != nil, lockErr)
		}
	}

	identity, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", ".", "..", "nested/name"} {
		if unlock, lockErr := lockInventoryFileInDirectory(
			context.Background(), root, name, identity,
		); lockErr == nil || unlock != nil {
			t.Fatalf("invalid relative lock %q = unlock:%v error:%v", name, unlock != nil, lockErr)
		}
	}
	if unlock, lockErr := lockInventoryFileInDirectory(
		context.Background(), blockingParent, "lock", identity,
	); lockErr == nil || unlock != nil {
		t.Fatalf("regular lock root = unlock:%v error:%v", unlock != nil, lockErr)
	}
	if unlock, lockErr := lockInventoryFileInDirectory(
		context.Background(), root, "lock", nil,
	); lockErr == nil || unlock != nil {
		t.Fatalf("nil lock root identity = unlock:%v error:%v", unlock != nil, lockErr)
	}
	if err := os.Mkdir(filepath.Join(root, "child.lock"), 0o700); err != nil {
		t.Fatal(err)
	}
	if unlock, lockErr := lockInventoryFileInDirectory(
		context.Background(), root, "child.lock", identity,
	); lockErr == nil || unlock != nil {
		t.Fatalf("directory child lock = unlock:%v error:%v", unlock != nil, lockErr)
	}
}

func TestOpenedInventoryLockRejectsClosedAndNonRegularHandles(t *testing.T) {
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if secureErr := secureOpenedInventoryLock(directory); secureErr == nil {
		t.Fatal("directory handle was accepted as an inventory lock")
	}
	_ = directory.Close()

	file, err := os.CreateTemp(t.TempDir(), "lock")
	if err != nil {
		t.Fatal(err)
	}
	fd := int(file.Fd())
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := secureOpenedInventoryLock(file); err == nil {
		t.Fatal("closed handle was accepted as an inventory lock")
	}
	if unlock, err := lockOpenedInventoryFile(context.Background(), file, fd); err == nil || unlock != nil {
		t.Fatalf("closed advisory lock = unlock:%v error:%v", unlock != nil, err)
	}
}
