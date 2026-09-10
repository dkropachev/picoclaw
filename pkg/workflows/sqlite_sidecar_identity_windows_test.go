//go:build windows

package workflows

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func retainWorkflowSQLiteSidecarIdentity(t *testing.T, path string) (*os.File, os.FileInfo) {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(
		name,
		windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		t.Fatalf("open shared-delete SQLite sidecar %s: %v", filepath.Base(path), err)
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		t.Fatalf("retain SQLite sidecar %s: file handle unavailable", filepath.Base(path))
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
