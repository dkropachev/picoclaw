package workflows

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDiscardWorkflowDevelopmentFencedRequiresExactActiveRevision(t *testing.T) {
	workspace := t.TempDir()
	session, err := StartWorkflowDevelopment(
		context.Background(),
		workspace,
		RuntimeCompatibility{PicoclawVersion: "test"},
		WorkflowDevelopmentStartRequest{
			Reason:    WorkflowDevelopmentReasonNew,
			TargetRef: "workflows/fenced.yml",
		},
	)
	if err != nil {
		t.Fatalf("StartWorkflowDevelopment() error = %v", err)
	}
	valid := WorkflowDevelopmentDiscardRequest{
		SessionID:               session.ID,
		ExpectedSessionRevision: session.SessionRevision,
	}
	for _, request := range []WorkflowDevelopmentDiscardRequest{
		{},
		{SessionID: "dev_other", ExpectedSessionRevision: session.SessionRevision},
		{SessionID: session.ID, ExpectedSessionRevision: "sha256:stale"},
	} {
		_, discardErr := DiscardWorkflowDevelopmentFenced(workspace, request)
		if !errors.Is(discardErr, ErrWorkflowSessionRevisionMismatch) {
			t.Fatalf("stale discard error = %v", discardErr)
		}
		active, getErr := GetWorkflowDevelopmentSession(workspace)
		if getErr != nil || active == nil || active.ID != session.ID {
			t.Fatalf("stale discard changed active session: %#v, %v", active, getErr)
		}
	}
	discarded, err := DiscardWorkflowDevelopmentFenced(workspace, valid)
	if err != nil || discarded == nil || discarded.ID != session.ID {
		t.Fatalf("valid discard = %#v, %v", discarded, err)
	}
	if active, getErr := GetWorkflowDevelopmentSession(workspace); getErr != nil || active != nil {
		t.Fatalf("active session after discard = %#v, %v", active, getErr)
	}
	replacement, err := StartWorkflowDevelopment(
		context.Background(),
		workspace,
		RuntimeCompatibility{PicoclawVersion: "test"},
		WorkflowDevelopmentStartRequest{
			Reason:    WorkflowDevelopmentReasonNew,
			TargetRef: "workflows/replacement.yml",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, discardErr := DiscardWorkflowDevelopmentFenced(workspace, valid)
	if !errors.Is(discardErr, ErrWorkflowSessionRevisionMismatch) {
		t.Fatalf("replaced-session discard error = %v", discardErr)
	}
	if active, getErr := GetWorkflowDevelopmentSession(workspace); getErr != nil ||
		active == nil || active.ID != replacement.ID {
		t.Fatalf("stale request discarded replacement: %#v, %v", active, getErr)
	}
}

func TestDiscardWorkflowDevelopmentFencedAndLockedHelperFailureBoundaries(t *testing.T) {
	workspaceFile := filepath.Join(t.TempDir(), "workspace-file")
	if err := os.WriteFile(workspaceFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := DiscardWorkflowDevelopment(workspaceFile); err == nil {
		t.Fatal("legacy discard acquired a mutation lock beneath a regular file")
	}
	if _, err := DiscardWorkflowDevelopmentFenced(
		workspaceFile,
		WorkflowDevelopmentDiscardRequest{SessionID: "dev", ExpectedSessionRevision: "revision"},
	); err == nil {
		t.Fatal("fenced discard acquired a mutation lock beneath a regular file")
	}

	workspace := t.TempDir()
	if _, err := DiscardWorkflowDevelopmentFenced(
		workspace,
		WorkflowDevelopmentDiscardRequest{SessionID: "dev_missing", ExpectedSessionRevision: "revision"},
	); !errors.Is(err, ErrNoActiveDevelopment) {
		t.Fatalf("missing active discard error = %v", err)
	}

	symlinkRoot := t.TempDir()
	symlinkOrSkip(t, t.TempDir(), filepath.Join(symlinkRoot, workflowDatabaseStateDir))
	if _, err := GetWorkflowDevelopmentSession(symlinkRoot); err == nil {
		t.Fatal("development store followed a symlinked database directory")
	}
}
