//go:build unix

package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestApplyPatchPreflightRejectsSpecialFileSources(t *testing.T) {
	tests := []struct {
		name  string
		patch string
	}{
		{
			name:  "update fifo",
			patch: "*** Begin Patch\n*** Update File: pipe\n@@\n-old\n+new\n*** End Patch",
		},
		{
			name:  "delete fifo",
			patch: "*** Begin Patch\n*** Delete File: pipe\n*** End Patch",
		},
		{
			name:  "add below fifo parent",
			patch: "*** Begin Patch\n*** Add File: pipe/child.txt\n+new\n*** End Patch",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			if err := unix.Mkfifo(filepath.Join(workspace, "pipe"), 0o600); err != nil {
				t.Skipf("mkfifo unavailable: %v", err)
			}
			before := applyPatchSnapshotTree(t, workspace)
			tool := newApplyPatchPreflightTestTool(
				t, workspace, true, true, ApplyPatchPreflightPolicy{},
			)
			result := executeApplyPatch(t, tool, context.Background(), test.patch)
			requireApplyPatchError(t, result)
			assertApplyPatchTreeEqual(t, workspace, before)
		})
	}
}

func TestApplyPatchPreflightSourceOpenDoesNotBlockOnFIFOSwap(t *testing.T) {
	workspace := t.TempDir()
	source := writeApplyPatchFixture(t, workspace, "source.txt", "before\n", 0o751)
	tool := newApplyPatchPreflightTestTool(
		t, workspace, true, true, ApplyPatchPreflightPolicy{},
	)
	var hookErr error
	tool.beforeSourceOpen = func(string) {
		if hookErr = os.Remove(source); hookErr != nil {
			return
		}
		hookErr = unix.Mkfifo(source, 0o600)
	}
	resultDone := make(chan *ToolResult, 1)
	go func() {
		resultDone <- tool.Execute(context.Background(), map[string]any{
			"patch": "*** Begin Patch\n*** Update File: source.txt\n" +
				"@@\n-before\n+after\n*** End Patch",
		})
	}()
	select {
	case result := <-resultDone:
		if hookErr != nil {
			t.Fatalf("install FIFO swap: %v", hookErr)
		}
		requireApplyPatchError(t, result)
	case <-time.After(2 * time.Second):
		t.Fatal("safe source open blocked on FIFO leaf swap")
	}
}

func TestApplyPatchLinkCountAcceptsSignedMetadata(t *testing.T) {
	count, err := applyPatchLinkCount(nil, applyPatchCoverageFileInfo{
		sys: struct{ Nlink int64 }{Nlink: 1},
	})
	if err != nil || count != 1 {
		t.Fatalf("signed link count = %d, %v", count, err)
	}
}
