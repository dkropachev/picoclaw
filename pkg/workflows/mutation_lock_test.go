package workflows

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkflowMutationLockFirstOperationCreatesStateDirectory(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "new", "workspace")
	if err := WithWorkflowMutationLock(workspace, func() error {
		return nil
	}); err != nil {
		t.Fatalf("WithWorkflowMutationLock() error = %v", err)
	}
	info, err := os.Stat(filepath.Join(workspace, workflowMutationStateDir))
	if err != nil {
		t.Fatalf("Stat(workflow state directory) error = %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("workflow state mode = %v, want directory", info.Mode())
	}
}

func TestWorkflowMutationLockSerializesWorkspaceMutations(t *testing.T) {
	workspace := t.TempDir()
	unlockFirst, err := lockWorkflowMutation(workspace)
	if err != nil {
		t.Fatalf("lockWorkflowMutation(first) error = %v", err)
	}

	acquired := make(chan func(), 1)
	failed := make(chan error, 1)
	go func() {
		unlockSecond, lockErr := lockWorkflowMutation(workspace)
		if lockErr != nil {
			failed <- lockErr
			return
		}
		acquired <- unlockSecond
	}()

	select {
	case unlockSecond := <-acquired:
		unlockSecond()
		unlockFirst()
		t.Fatal("second workspace mutation lock was acquired before first released")
	case lockErr := <-failed:
		unlockFirst()
		t.Fatalf("lockWorkflowMutation(second) error = %v", lockErr)
	case <-time.After(25 * time.Millisecond):
	}

	unlockFirst()
	select {
	case unlockSecond := <-acquired:
		unlockSecond()
	case lockErr := <-failed:
		t.Fatalf("lockWorkflowMutation(second) error = %v", lockErr)
	case <-time.After(time.Second):
		t.Fatal("second workspace mutation lock did not acquire after first released")
	}
}

func TestWorkflowMutationLockDoesNotSerializeDifferentWorkspaces(t *testing.T) {
	unlockFirst, err := lockWorkflowMutation(t.TempDir())
	if err != nil {
		t.Fatalf("lockWorkflowMutation(first) error = %v", err)
	}
	defer unlockFirst()

	unlockSecond, err := lockWorkflowMutation(t.TempDir())
	if err != nil {
		t.Fatalf("lockWorkflowMutation(second) error = %v", err)
	}
	unlockSecond()
}

func TestWorkflowMutationCallbackFencesDevelopmentStart(t *testing.T) {
	workspace := t.TempDir()
	entered := make(chan struct{})
	release := make(chan struct{})
	operationDone := make(chan error, 1)
	go func() {
		operationDone <- WithWorkflowMutationLock(workspace, func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	startDone := make(chan error, 1)
	go func() {
		_, err := StartWorkflowDevelopment(
			context.Background(),
			workspace,
			RuntimeCompatibility{PicoclawVersion: "mutation-test"},
			WorkflowDevelopmentStartRequest{
				Prompt:    "must wait",
				TargetRef: "workflows/wait.yml",
			},
		)
		startDone <- err
	}()
	select {
	case err := <-startDone:
		close(release)
		t.Fatalf("development start escaped mutation callback: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if err := <-operationDone; err != nil {
		t.Fatalf("WithWorkflowMutationLock() error = %v", err)
	}
	select {
	case err := <-startDone:
		if err != nil {
			t.Fatalf("development start after release error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("development start remained blocked after mutation callback")
	}
}
