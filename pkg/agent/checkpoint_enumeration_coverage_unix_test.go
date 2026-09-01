//go:build android || darwin || dragonfly || freebsd || ios || linux || netbsd || openbsd || solaris

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCheckpointArchiveRejectsSpecialFilesAndClosedEnumeration(t *testing.T) {
	checkpointRoot := filepath.Join(t.TempDir(), "active")
	archiveRoot := filepath.Join(checkpointRoot, "legacy-json")
	if err := os.MkdirAll(archiveRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(archiveRoot, "special")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("FIFO creation unavailable: %v", err)
	}
	files, err := agentCheckpointRetainedStateFilesBounded(
		checkpointRoot, archiveRoot, 4, 2, 4, 2,
	)
	if err == nil || files != nil || !strings.Contains(err.Error(), "unsafe file") {
		t.Fatalf("special checkpoint archive = %#v, %v", files, err)
	}

	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
	if err := forEachCheckpointDirectoryEntry(directory, func(os.DirEntry) error { return nil }); err == nil {
		t.Fatal("closed checkpoint directory enumeration succeeded")
	}
}
