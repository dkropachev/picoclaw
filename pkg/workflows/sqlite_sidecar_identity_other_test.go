//go:build !unix && !windows

package workflows

import (
	"os"
	"path/filepath"
	"testing"
)

func retainWorkflowSQLiteSidecarIdentity(t *testing.T, path string) (*os.File, os.FileInfo) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open SQLite sidecar %s: %v", filepath.Base(path), err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		t.Fatalf("inspect open SQLite sidecar %s: %v", filepath.Base(path), err)
	}
	return file, info
}
