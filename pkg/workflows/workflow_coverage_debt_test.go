package workflows

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkflowCoverageDebtHelpers(t *testing.T) {
	if draft := fallbackWorkflowDraftYAML("Fallback", "inspect state"); !strings.Contains(draft, `name: "Fallback"`) ||
		!strings.Contains(draft, `prompt: "inspect state"`) {
		t.Fatalf("fallback workflow draft = %q", draft)
	}
	if draft := fallbackRepositoryReviewDraftYAML(
		"Repository review", "Review commit deadbeef for concurrency bugs",
	); !strings.Contains(draft, `name: "Repository review"`) ||
		!strings.Contains(draft, "commit:") || !strings.Contains(draft, "prompt:") {
		t.Fatalf("fallback repository review draft = %q", draft)
	}
	if !nativeTargetSelects("tests", "tests") || nativeTargetSelects("unknown", "tests") {
		t.Fatal("target selection fallback branches are incorrect")
	}

	for name, value := range map[string]string{
		"empty":       "",
		"punctuation": "...---___",
		"long":        strings.Repeat("a", 81),
	} {
		if segment := safeStorageSegment(value); segment == "" || strings.Contains(segment, "/") {
			t.Fatalf("safe storage segment %s = %q", name, segment)
		}
	}

	workspace := t.TempDir()
	if err := nativeEnsureInsideStorageRoot(workspace, workspace, workflowStateDir); err != nil {
		t.Fatalf("short storage path: %v", err)
	}
	if err := nativeEnsureInsideStorageRoot(
		workspace,
		filepath.Join(workspace, "ordinary", "file"),
		"ordinary",
		"file",
	); err != nil {
		t.Fatalf("ordinary workspace path: %v", err)
	}
}
