package workflows

import (
	"context"
	"errors"
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

func TestRunnableWorkflowSnapshotsHoldMutationLockThroughDurableCreate(
	t *testing.T,
) {
	ctx := context.Background()
	workspace := t.TempDir()
	writeWorkflowFile(t, workspace, "admitted.yml", `
name: Admitted
on:
  manual: {}
jobs:
  admitted:
    runs-on: picoclaw
    steps:
      - uses: function/admitted
`)
	runtime := RuntimeCompatibility{
		PicoclawVersion: "v1.0.0",
		GitCommit:       "snapshot-create-boundary",
	}
	if _, err := RevalidateLocal(ctx, workspace, runtime); err != nil {
		t.Fatalf("RevalidateLocal() error = %v", err)
	}
	snapshot, err := LoadValidatedLocalSnapshot(
		ctx,
		workspace,
		"workflows/admitted.yml",
	)
	if err != nil {
		t.Fatalf("LoadValidatedLocalSnapshot() error = %v", err)
	}
	store := NewFileRunStore(workspace)
	now := time.Now().UTC()
	run := &Run{
		ID:          "wr_snapshot_boundary",
		WorkflowRef: snapshot.Ref,
		Status:      RunStatusRunning,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	mutationAttempted := make(chan struct{})
	mutationDone := make(chan error, 1)
	err = WithRunnableWorkflowSnapshots(
		ctx,
		workspace,
		[]*LocalWorkflowSnapshot{snapshot},
		runtime,
		func() error {
			go func() {
				close(mutationAttempted)
				mutationDone <- WithWorkflowMutationLock(
					workspace,
					func() error { return nil },
				)
			}()
			<-mutationAttempted
			select {
			case mutationErr := <-mutationDone:
				t.Fatalf(
					"workflow mutation acquired before durable create: %v",
					mutationErr,
				)
			case <-time.After(25 * time.Millisecond):
			}
			return store.CreateRun(ctx, run)
		},
	)
	if err != nil {
		t.Fatalf("WithRunnableWorkflowSnapshots() error = %v", err)
	}
	select {
	case mutationErr := <-mutationDone:
		if mutationErr != nil {
			t.Fatalf("workflow mutation after create error = %v", mutationErr)
		}
	case <-time.After(time.Second):
		t.Fatal("workflow mutation remained blocked after durable create")
	}
	if _, err := store.GetRun(ctx, run.ID); err != nil {
		t.Fatalf("durable run was not created inside admission boundary: %v", err)
	}
}

func TestFencedRunnableWorkflowSnapshotsChecksFenceBeforeCompatibility(
	t *testing.T,
) {
	ctx := context.Background()
	workspace := t.TempDir()
	const original = `
name: Original
on:
  manual: {}
jobs:
  original:
    runs-on: picoclaw
    steps:
      - uses: function/original
`
	writeWorkflowFile(t, workspace, "fenced.yml", original)
	runtime := RuntimeCompatibility{
		PicoclawVersion: "v1.0.0",
		GitCommit:       "fence-before-compatibility",
	}
	if _, err := RevalidateLocal(ctx, workspace, runtime); err != nil {
		t.Fatalf("RevalidateLocal(original) error = %v", err)
	}
	snapshot, err := LoadValidatedLocalSnapshot(
		ctx,
		workspace,
		"workflows/fenced.yml",
	)
	if err != nil {
		t.Fatalf("LoadValidatedLocalSnapshot() error = %v", err)
	}
	writeWorkflowFile(t, workspace, "fenced.yml", `
name: Published replacement
on:
  manual: {}
jobs:
  replacement:
    runs-on: picoclaw
    steps:
      - uses: function/replacement
`)
	if _, revalidateErr := RevalidateLocal(ctx, workspace, runtime); revalidateErr != nil {
		t.Fatalf("RevalidateLocal(replacement) error = %v", revalidateErr)
	}

	fenceErr := errors.New("dependency revision mismatch")
	operationCalled := false
	err = WithFencedRunnableWorkflowSnapshots(
		ctx,
		workspace,
		[]*LocalWorkflowSnapshot{snapshot},
		runtime,
		func() error { return fenceErr },
		func() error {
			operationCalled = true
			return nil
		},
	)
	if !errors.Is(err, fenceErr) {
		t.Fatalf("WithFencedRunnableWorkflowSnapshots() error = %v, want fence error", err)
	}
	if operationCalled {
		t.Fatal("durable operation ran after fence rejection")
	}
}

func TestGuardedFencedRunnableWorkflowSnapshotsKeepsGuardThroughCreate(
	t *testing.T,
) {
	ctx := context.Background()
	workspace := t.TempDir()
	writeWorkflowFile(t, workspace, "guarded.yml", `
name: Guarded
on:
  manual: {}
jobs:
  guarded:
    runs-on: picoclaw
    steps:
      - uses: function/guarded
`)
	runtime := RuntimeCompatibility{
		PicoclawVersion: "v1.0.0",
		GitCommit:       "guard-through-create",
	}
	if _, err := RevalidateLocal(ctx, workspace, runtime); err != nil {
		t.Fatalf("RevalidateLocal() error = %v", err)
	}
	snapshot, err := LoadValidatedLocalSnapshot(
		ctx,
		workspace,
		"workflows/guarded.yml",
	)
	if err != nil {
		t.Fatalf("LoadValidatedLocalSnapshot() error = %v", err)
	}

	guardHeld := false
	fenceCalled := false
	operationCalled := false
	err = WithGuardedFencedRunnableWorkflowSnapshots(
		ctx,
		workspace,
		[]*LocalWorkflowSnapshot{snapshot},
		runtime,
		func() error {
			if guardHeld {
				t.Fatal("guard entered before current admission fence")
			}
			fenceCalled = true
			return nil
		},
		func(guarded func() error) error {
			if !fenceCalled {
				t.Fatal("guard entered before fence completed")
			}
			guardHeld = true
			defer func() { guardHeld = false }()
			return guarded()
		},
		func() error {
			if !guardHeld {
				t.Fatal("guard was released before durable create")
			}
			operationCalled = true
			return nil
		},
	)
	if err != nil {
		t.Fatalf("WithGuardedFencedRunnableWorkflowSnapshots() error = %v", err)
	}
	if guardHeld || !fenceCalled || !operationCalled {
		t.Fatalf(
			"guarded boundary state = held %v, fence %v, operation %v",
			guardHeld,
			fenceCalled,
			operationCalled,
		)
	}
}

func TestFencedRunnableWorkflowSnapshotsClassifiesManifestFailureUnavailable(
	t *testing.T,
) {
	ctx := context.Background()
	workspace := t.TempDir()
	writeWorkflowFile(t, workspace, "unavailable.yml", `
name: Unavailable
on:
  manual: {}
jobs:
  unavailable:
    runs-on: picoclaw
    steps:
      - uses: function/unavailable
`)
	runtime := RuntimeCompatibility{
		PicoclawVersion: "v1.0.0",
		GitCommit:       "manifest-unavailable",
	}
	if _, err := RevalidateLocal(ctx, workspace, runtime); err != nil {
		t.Fatalf("RevalidateLocal() error = %v", err)
	}
	snapshot, err := LoadValidatedLocalSnapshot(
		ctx,
		workspace,
		"workflows/unavailable.yml",
	)
	if err != nil {
		t.Fatalf("LoadValidatedLocalSnapshot() error = %v", err)
	}
	db, openErr := openWorkflowDatabase(ctx, workspace)
	if openErr != nil {
		t.Fatal(openErr)
	}
	if _, corruptErr := db.ExecContext(ctx, `ALTER TABLE workflow_compatibility_runtime
		RENAME TO corrupt_workflow_compatibility_runtime`); corruptErr != nil {
		db.Close()
		t.Fatalf("corrupt compatibility schema: %v", corruptErr)
	}
	if closeErr := db.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	err = WithFencedRunnableWorkflowSnapshots(
		ctx,
		workspace,
		[]*LocalWorkflowSnapshot{snapshot},
		runtime,
		func() error { return nil },
		func() error {
			t.Fatal("durable operation ran with unavailable compatibility manifest")
			return nil
		},
	)
	if !errors.Is(err, ErrWorkflowSnapshotAdmissionUnavailable) {
		t.Fatalf(
			"WithFencedRunnableWorkflowSnapshots() error = %v, want unavailable",
			err,
		)
	}
	if errors.Is(err, ErrWorkflowSnapshotsNotRunnable) {
		t.Fatalf("manifest I/O/parse failure was classified as not runnable: %v", err)
	}
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
