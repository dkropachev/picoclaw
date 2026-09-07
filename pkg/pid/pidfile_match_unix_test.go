//go:build !windows

package pid

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRemovePidFileIfMatchPreservesFileWhenRemovalDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can remove files from a non-writable directory")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, pidFileName)
	data := PidFileData{PID: 424242, Token: "matching-generation"}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("Marshal(PidFileData) error = %v", err)
	}
	if err = os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("WriteFile(pid file) error = %v", err)
	}
	if err = os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("Chmod(read-only directory) error = %v", err)
	}
	t.Cleanup(func() {
		if chmodErr := os.Chmod(dir, 0o700); chmodErr != nil {
			t.Errorf("restore directory mode: %v", chmodErr)
		}
	})

	if RemovePidFileIfMatch(dir, data.PID, data.Token) {
		t.Fatal("RemovePidFileIfMatch() reported removal from a non-writable directory")
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatalf("PID file was not preserved after removal failure: %v", err)
	}
}
