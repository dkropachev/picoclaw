package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func isolateApplyPatchDefaultTransactionState(t *testing.T) {
	t.Helper()
	t.Setenv(config.EnvHome, filepath.Join(t.TempDir(), "picoclaw-home"))
}

func TestApplyPatchTool_AddUpdateDelete(t *testing.T) {
	isolateApplyPatchDefaultTransactionState(t)
	workspace := t.TempDir()
	tool := NewApplyPatchTool(workspace, true)

	add := tool.Execute(context.Background(), map[string]any{
		"patch": "*** Begin Patch\n*** Add File: note.txt\n+hello\n+world\n*** End Patch",
	})
	if add.IsError {
		t.Fatalf("add failed: %s", add.ForLLM)
	}

	update := tool.Execute(context.Background(), map[string]any{
		"patch": "*** Begin Patch\n*** Update File: note.txt\n@@\n hello\n-world\n+codex\n*** End Patch",
	})
	if update.IsError {
		t.Fatalf("update failed: %s", update.ForLLM)
	}
	content, err := os.ReadFile(filepath.Join(workspace, "note.txt"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(content) != "hello\ncodex\n" {
		t.Fatalf("content = %q, want updated content", string(content))
	}

	del := tool.Execute(context.Background(), map[string]any{
		"patch": "*** Begin Patch\n*** Delete File: note.txt\n*** End Patch",
	})
	if del.IsError {
		t.Fatalf("delete failed: %s", del.ForLLM)
	}
	if _, err := os.Stat(filepath.Join(workspace, "note.txt")); !os.IsNotExist(err) {
		t.Fatalf("deleted file still exists, err=%v", err)
	}
}

func TestApplyPatchTool_RejectsOutsideWorkspace(t *testing.T) {
	isolateApplyPatchDefaultTransactionState(t)
	tool := NewApplyPatchTool(t.TempDir(), true)
	result := tool.Execute(context.Background(), map[string]any{
		"patch": "*** Begin Patch\n*** Add File: /tmp/picoclaw-apply-patch-outside.txt\n+nope\n*** End Patch",
	})
	if !result.IsError {
		t.Fatalf("expected outside workspace patch to fail")
	}
	if !strings.Contains(result.ForLLM, "outside") {
		t.Fatalf("error = %q, want outside workspace message", result.ForLLM)
	}
}

func TestApplyPatchTool_RespectsCreatePermission(t *testing.T) {
	isolateApplyPatchDefaultTransactionState(t)
	tool := NewApplyPatchToolWithPermissions(t.TempDir(), true, false, true)
	result := tool.Execute(context.Background(), map[string]any{
		"patch": "*** Begin Patch\n*** Add File: note.txt\n+nope\n*** End Patch",
	})
	if !result.IsError {
		t.Fatal("add succeeded with create permission disabled")
	}
	if !strings.Contains(result.ForLLM, "write_file is disabled") {
		t.Fatalf("error = %q, want write_file disabled", result.ForLLM)
	}
}

func TestApplyPatchTool_RespectsCreatePermissionForMove(t *testing.T) {
	isolateApplyPatchDefaultTransactionState(t)
	workspace := t.TempDir()
	path := filepath.Join(workspace, "note.txt")
	if err := os.WriteFile(path, []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	tool := NewApplyPatchToolWithPermissions(workspace, true, false, true)
	result := tool.Execute(context.Background(), map[string]any{
		"patch": "*** Begin Patch\n*** Update File: note.txt\n*** Move to: moved.txt\n@@\n hello\n-world\n+codex\n*** End Patch",
	})
	if !result.IsError {
		t.Fatal("move succeeded with create permission disabled")
	}
	if !strings.Contains(result.ForLLM, "write_file is disabled") {
		t.Fatalf("error = %q, want write_file disabled", result.ForLLM)
	}
	if _, err := os.Stat(filepath.Join(workspace, "moved.txt")); !os.IsNotExist(err) {
		t.Fatalf("moved file exists despite denied move, err=%v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(content) != "hello\nworld\n" {
		t.Fatalf("source content changed to %q", string(content))
	}
}

func TestApplyPatchTool_RespectsUpdatePermission(t *testing.T) {
	isolateApplyPatchDefaultTransactionState(t)
	workspace := t.TempDir()
	path := filepath.Join(workspace, "note.txt")
	if err := os.WriteFile(path, []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	tool := NewApplyPatchToolWithPermissions(workspace, true, true, false)
	result := tool.Execute(context.Background(), map[string]any{
		"patch": "*** Begin Patch\n*** Update File: note.txt\n@@\n hello\n-world\n+nope\n*** End Patch",
	})
	if !result.IsError {
		t.Fatal("update succeeded with update permission disabled")
	}
	if !strings.Contains(result.ForLLM, "edit_file is disabled") {
		t.Fatalf("error = %q, want edit_file disabled", result.ForLLM)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(content) != "hello\nworld\n" {
		t.Fatalf("content changed to %q", string(content))
	}
}

func TestApplyPatchToolPathGuardChecksCompletePatchBeforeMutation(t *testing.T) {
	isolateApplyPatchDefaultTransactionState(t)
	workspace := t.TempDir()
	allowed := filepath.Join(workspace, "allowed.txt")
	if err := os.WriteFile(allowed, []byte("before\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	tool := NewApplyPatchToolWithPathGuard(
		workspace,
		true,
		func(path string) error {
			if path == "denied.txt" {
				return context.Canceled
			}
			return nil
		},
	)
	result := tool.Execute(context.Background(), map[string]any{
		"patch": "*** Begin Patch\n" +
			"*** Update File: allowed.txt\n@@\n-before\n+after\n" +
			"*** Add File: denied.txt\n+forbidden\n*** End Patch",
	})
	if !result.IsError || !strings.Contains(result.ForLLM, "denied.txt") {
		t.Fatalf("Execute() = %#v, want denied path", result)
	}
	content, err := os.ReadFile(allowed)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(content) != "before\n" {
		t.Fatalf("allowed file changed before late guard failure: %q", content)
	}
}
