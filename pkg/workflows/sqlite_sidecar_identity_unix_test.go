//go:build unix

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
	current := workflowSQLiteSidecarIdentity(t, path)
	if !os.SameFile(info, current) {
		_ = file.Close()
		t.Fatalf("SQLite sidecar %s changed while retaining identity", filepath.Base(path))
	}
	return file, info
}
