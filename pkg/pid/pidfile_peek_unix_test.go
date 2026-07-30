//go:build unix

package pid

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestPeekPidFileRejectsFIFOWithoutBlocking(t *testing.T) {
	dir := tmpDir(t)
	path := filepath.Join(dir, pidFileName)
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("Mkfifo() error = %v", err)
	}

	started := time.Now()
	if got := PeekPidFile(dir); got != nil {
		t.Fatalf("PeekPidFile() = %#v, want nil", got)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("PeekPidFile blocked on FIFO for %s", elapsed)
	}
}

func TestPeekPidFileRejectsSymlinkWithoutMutation(t *testing.T) {
	dir := tmpDir(t)
	targetName := "target.pid"
	targetPath := filepath.Join(dir, targetName)
	targetRaw := []byte(
		"{\n" +
			"  \"pid\": 123,\n" +
			"  \"token\": \"byte-exact-target\",\n" +
			"  \"version\": \"test\",\n" +
			"  \"port\": 18790,\n" +
			"  \"host\": \"127.0.0.1\"\n" +
			"}\n",
	)
	if err := os.WriteFile(targetPath, targetRaw, 0o600); err != nil {
		t.Fatalf("WriteFile(target) error = %v", err)
	}

	path := filepath.Join(dir, pidFileName)
	if err := os.Symlink(targetName, path); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	if got := PeekPidFile(dir); got != nil {
		t.Fatalf("PeekPidFile() = %#v, want nil", got)
	}

	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat(symlink) error = %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("pid path mode = %v, want symlink", info.Mode())
	}
	if got, err := os.Readlink(path); err != nil {
		t.Fatalf("Readlink() error = %v", err)
	} else if got != targetName {
		t.Fatalf("Readlink() = %q, want %q", got, targetName)
	}
	if got, err := os.ReadFile(targetPath); err != nil {
		t.Fatalf("ReadFile(target) error = %v", err)
	} else if !bytes.Equal(got, targetRaw) {
		t.Fatalf("target bytes changed:\ngot  %q\nwant %q", got, targetRaw)
	}
}
