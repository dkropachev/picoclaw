//go:build linux

package migration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCopyBackupFileRejectsChangingProcGeneration(t *testing.T) {
	source := "/proc/self/status"
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
		t.Skipf("stable proc status fixture unavailable: %#v, %v", info, err)
	}
	backup := t.TempDir()
	_, err = copyBackupFile(
		t.Context(), backup, "global.test", "database", source, filepath.Join("generation", "database"),
	)
	if err == nil || !strings.Contains(err.Error(), "source changed while copying") {
		t.Fatalf("changing proc generation error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(backup, "generation", "database")); !os.IsNotExist(statErr) {
		t.Fatalf("failed backup destination was retained: %v", statErr)
	}
}
