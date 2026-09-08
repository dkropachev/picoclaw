//go:build unix

package catalog

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestReadinessHomeKeyFallsBackFromRemovedWorkingDirectory(t *testing.T) {
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
			t.Skip("platform does not unlink the current working directory")
		}
		t.Fatal(err)
	}
	if got := readinessHomeKey("relative-home"); got != "relative-home" {
		t.Fatalf("fallback readiness home key = %q", got)
	}
}
