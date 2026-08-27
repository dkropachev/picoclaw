//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package logger

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestLoggerStateNewFileAndParentsArePrivate(t *testing.T) {
	prepareLoggerStateTest(t)
	oldUmask := syscall.Umask(0)
	defer syscall.Umask(oldUmask)

	root := t.TempDir()
	firstParent := filepath.Join(root, "private")
	secondParent := filepath.Join(firstParent, "nested")
	path := filepath.Join(secondParent, "picoclaw.log")
	if err := EnableFileLogging(path); err != nil {
		t.Fatalf("EnableFileLogging() error = %v", err)
	}
	DisableFileLogging()

	for _, directory := range []string{firstParent, secondParent} {
		info, err := os.Stat(directory)
		if err != nil {
			t.Fatalf("Stat(%q) error = %v", directory, err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Fatalf("new directory %q mode = %#o, want 0700", directory, got)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(log) error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("new log mode = %#o, want 0600", got)
	}
}

func TestLoggerStateExistingModesAreUnchanged(t *testing.T) {
	prepareLoggerStateTest(t)
	root := t.TempDir()
	directory := filepath.Join(root, "existing")
	if err := os.Mkdir(directory, 0o751); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.Chmod(directory, 0o751); err != nil {
		t.Fatalf("Chmod(directory) error = %v", err)
	}
	path := filepath.Join(directory, "existing.log")
	if err := os.WriteFile(path, []byte("existing\n"), 0o640); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatalf("Chmod(file) error = %v", err)
	}

	if err := EnableFileLogging(path); err != nil {
		t.Fatalf("EnableFileLogging() error = %v", err)
	}
	Info("append-without-mode-change")
	DisableFileLogging()

	directoryInfo, err := os.Stat(directory)
	if err != nil {
		t.Fatalf("Stat(directory) error = %v", err)
	}
	if got := directoryInfo.Mode().Perm(); got != 0o751 {
		t.Fatalf("existing directory mode = %#o, want 0751", got)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(file) error = %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o640 {
		t.Fatalf("existing file mode = %#o, want 0640", got)
	}
}
