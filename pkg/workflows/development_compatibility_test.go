package workflows

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkflowDevelopmentLifecyclePublishesAndClearsActiveSession(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	runtime := RuntimeCompatibility{PicoclawVersion: "v1.0.0", GitCommit: "abc123"}

	session, err := StartWorkflowDevelopment(ctx, workspace, runtime, WorkflowDevelopmentStartRequest{
		Prompt: "summarize incoming support requests",
	})
	if err != nil {
		t.Fatalf("StartWorkflowDevelopment() error = %v", err)
	}
	if session.TargetWorkflowRef != "workflows/summarize-incoming-support-requests.yml" {
		t.Fatalf("target ref = %q", session.TargetWorkflowRef)
	}
	if session.Validation == nil || !session.Validation.Valid {
		t.Fatalf("initial generated workflow should validate: %#v", session.Validation)
	}
	if session.Status != WorkflowDevelopmentStatusEditing {
		t.Fatalf("initial status = %q, want editing until a draft test passes", session.Status)
	}

	_, err = StartWorkflowDevelopment(ctx, workspace, runtime, WorkflowDevelopmentStartRequest{
		Prompt: "another workflow",
	})
	if !errors.Is(err, ErrActiveDevelopmentExists) {
		t.Fatalf("second StartWorkflowDevelopment() error = %v, want ErrActiveDevelopmentExists", err)
	}

	if _, recordErr := RecordWorkflowDevelopmentTest(
		workspace,
		&RunResult{RunID: "wr_ready", Status: RunStatusSucceeded},
		nil,
	); recordErr != nil {
		t.Fatalf("RecordWorkflowDevelopmentTest() error = %v", recordErr)
	}

	result, err := PublishWorkflowDevelopment(ctx, workspace, runtime)
	if err != nil {
		t.Fatalf("PublishWorkflowDevelopment() error = %v", err)
	}
	if result.WorkflowRef != session.TargetWorkflowRef {
		t.Fatalf("published ref = %q, want %q", result.WorkflowRef, session.TargetWorkflowRef)
	}
	if _, statErr := os.Stat(filepath.Join(workspace, session.TargetWorkflowRef)); statErr != nil {
		t.Fatalf("published workflow stat error = %v", statErr)
	}
	active, err := GetWorkflowDevelopmentSession(workspace)
	if err != nil {
		t.Fatalf("GetWorkflowDevelopmentSession() error = %v", err)
	}
	if active != nil {
		t.Fatalf("active session after publish = %#v, want nil", active)
	}
	if runnableErr := EnsureWorkflowRunnable(ctx, workspace, session.TargetWorkflowRef, runtime); runnableErr != nil {
		t.Fatalf("EnsureWorkflowRunnable() after publish error = %v", runnableErr)
	}
}

func TestGenerateWorkflowDraftYAMLUsesMainAgent(t *testing.T) {
	draft := GenerateWorkflowDraftYAML("summarize support issues")
	if !strings.Contains(draft, "uses: agent/main") {
		t.Fatalf("generated draft = %s, want agent/main", draft)
	}
	if strings.Contains(draft, "agent/default") {
		t.Fatalf("generated draft = %s, must not reference agent/default", draft)
	}
}

func TestWorkflowDevelopmentPublishRequiresCurrentSuccessfulTest(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	runtime := RuntimeCompatibility{PicoclawVersion: "v1.0.0", GitCommit: "abc123"}

	if _, err := StartWorkflowDevelopment(ctx, workspace, runtime, WorkflowDevelopmentStartRequest{
		Prompt: "summarize incoming support requests",
	}); err != nil {
		t.Fatalf("StartWorkflowDevelopment() error = %v", err)
	}
	if _, err := PublishWorkflowDevelopment(ctx, workspace, runtime); err == nil {
		t.Fatal("PublishWorkflowDevelopment() without test error = nil, want error")
	}

	if _, err := RecordWorkflowDevelopmentTest(
		workspace,
		&RunResult{RunID: "wr_failed", Status: RunStatusFailed},
		nil,
	); err != nil {
		t.Fatalf("RecordWorkflowDevelopmentTest(failed) error = %v", err)
	}
	active, err := GetWorkflowDevelopmentSession(workspace)
	if err != nil {
		t.Fatalf("GetWorkflowDevelopmentSession() after failed test error = %v", err)
	}
	if active == nil || active.Status != WorkflowDevelopmentStatusEditing {
		t.Fatalf("status after failed test = %#v, want editing", active)
	}
	if _, publishErr := PublishWorkflowDevelopment(ctx, workspace, runtime); publishErr == nil {
		t.Fatal("PublishWorkflowDevelopment() after failed test error = nil, want error")
	}

	active, err = RecordWorkflowDevelopmentTest(
		workspace,
		&RunResult{RunID: "wr_ready", Status: RunStatusSucceeded},
		nil,
	)
	if err != nil {
		t.Fatalf("RecordWorkflowDevelopmentTest(succeeded) error = %v", err)
	}
	if active.Status != WorkflowDevelopmentStatusReadyToPublish {
		t.Fatalf("status after successful test = %q, want ready_to_publish", active.Status)
	}
	if _, err := PublishWorkflowDevelopment(ctx, workspace, runtime); err != nil {
		t.Fatalf("PublishWorkflowDevelopment() after successful test error = %v", err)
	}
}

func TestWorkflowDevelopmentNoopRevisePreservesCurrentTest(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	runtime := RuntimeCompatibility{PicoclawVersion: "v1.0.0", GitCommit: "abc123"}

	_, err := StartWorkflowDevelopment(ctx, workspace, runtime, WorkflowDevelopmentStartRequest{
		Prompt: "summarize incoming support requests",
	})
	if err != nil {
		t.Fatalf("StartWorkflowDevelopment() error = %v", err)
	}
	session, err := RecordWorkflowDevelopmentTest(
		workspace,
		&RunResult{RunID: "wr_ready", Status: RunStatusSucceeded},
		nil,
	)
	if err != nil {
		t.Fatalf("RecordWorkflowDevelopmentTest() error = %v", err)
	}
	if session.Status != WorkflowDevelopmentStatusReadyToPublish {
		t.Fatalf("status after successful test = %q, want ready_to_publish", session.Status)
	}

	sameYAML := session.YAML
	session, err = ReviseWorkflowDevelopment(workspace, WorkflowDevelopmentReviseRequest{
		Prompt:    "updated authoring note",
		TargetRef: session.TargetWorkflowRef,
		YAML:      &sameYAML,
	})
	if err != nil {
		t.Fatalf("ReviseWorkflowDevelopment(no-op) error = %v", err)
	}
	if session.Status != WorkflowDevelopmentStatusReadyToPublish {
		t.Fatalf("status after no-op revise = %q, want ready_to_publish", session.Status)
	}
	if session.LastTest == nil ||
		session.LastTest.RunID != "wr_ready" ||
		session.LastTest.Status != RunStatusSucceeded {
		t.Fatalf("LastTest after no-op revise = %#v, want preserved successful snapshot", session.LastTest)
	}
	if _, err := PublishWorkflowDevelopment(ctx, workspace, runtime); err != nil {
		t.Fatalf("PublishWorkflowDevelopment() after no-op revise error = %v", err)
	}
}

func TestWorkflowDevelopmentReviseClearsLastTest(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	runtime := RuntimeCompatibility{PicoclawVersion: "v1.0.0", GitCommit: "abc123"}

	_, err := StartWorkflowDevelopment(ctx, workspace, runtime, WorkflowDevelopmentStartRequest{
		Prompt: "summarize incoming support requests",
	})
	if err != nil {
		t.Fatalf("StartWorkflowDevelopment() error = %v", err)
	}
	session, err := RecordWorkflowDevelopmentTest(
		workspace,
		&RunResult{RunID: "wr_test", Status: RunStatusSucceeded},
		nil,
	)
	if err != nil {
		t.Fatalf("RecordWorkflowDevelopmentTest() error = %v", err)
	}
	if session.LastTest == nil {
		t.Fatal("LastTest after record = nil, want snapshot")
	}

	nextYAML := GenerateWorkflowDraftYAML("route urgent support requests")
	session, err = ReviseWorkflowDevelopment(workspace, WorkflowDevelopmentReviseRequest{
		YAML: &nextYAML,
	})
	if err != nil {
		t.Fatalf("ReviseWorkflowDevelopment() error = %v", err)
	}
	if session.LastTest != nil {
		t.Fatalf("LastTest after revise = %#v, want nil", session.LastTest)
	}
}

func TestWorkflowDevelopmentRejectsMutationWhileCurrentTestRunning(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	runtime := RuntimeCompatibility{PicoclawVersion: "v1.0.0", GitCommit: "abc123"}

	session, err := StartWorkflowDevelopment(ctx, workspace, runtime, WorkflowDevelopmentStartRequest{
		Prompt: "summarize incoming support requests",
	})
	if err != nil {
		t.Fatalf("StartWorkflowDevelopment() error = %v", err)
	}
	if _, recordErr := RecordWorkflowDevelopmentTest(
		workspace,
		&RunResult{RunID: "wr_running", Status: RunStatusRunning},
		nil,
	); recordErr != nil {
		t.Fatalf("RecordWorkflowDevelopmentTest(running) error = %v", recordErr)
	}
	active, err := GetWorkflowDevelopmentSession(workspace)
	if err != nil {
		t.Fatalf("GetWorkflowDevelopmentSession() after running test error = %v", err)
	}
	if active == nil || active.Status != WorkflowDevelopmentStatusTesting {
		t.Fatalf("status after running test = %#v, want testing", active)
	}

	nextYAML := GenerateWorkflowDraftYAML("route urgent support requests")
	if _, reviseErr := ReviseWorkflowDevelopment(workspace, WorkflowDevelopmentReviseRequest{
		YAML: &nextYAML,
	}); !errors.Is(reviseErr, ErrDevelopmentBusy) {
		t.Fatalf("ReviseWorkflowDevelopment() error = %v, want ErrDevelopmentBusy", reviseErr)
	}
	if _, validateErr := ValidateWorkflowDevelopment(workspace); !errors.Is(validateErr, ErrDevelopmentBusy) {
		t.Fatalf("ValidateWorkflowDevelopment() error = %v, want ErrDevelopmentBusy", validateErr)
	}

	active, err = GetWorkflowDevelopmentSession(workspace)
	if err != nil {
		t.Fatalf("GetWorkflowDevelopmentSession() error = %v", err)
	}
	if active == nil ||
		active.LastTest == nil ||
		active.LastTest.RunID != "wr_running" ||
		active.LastTest.Status != RunStatusRunning ||
		active.YAML != session.YAML {
		t.Fatalf("active session after rejected mutation = %#v, want unchanged running test", active)
	}
}

func TestWorkflowDevelopmentAsyncCompletionDoesNotRecordStaleDraft(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	runtime := RuntimeCompatibility{PicoclawVersion: "v1.0.0", GitCommit: "abc123"}

	session, err := StartWorkflowDevelopment(ctx, workspace, runtime, WorkflowDevelopmentStartRequest{
		Prompt: "summarize incoming support requests",
	})
	if err != nil {
		t.Fatalf("StartWorkflowDevelopment() error = %v", err)
	}
	draftKey := WorkflowDevelopmentDraftKey(session.TargetWorkflowRef, session.YAML)
	if _, recordErr := RecordWorkflowDevelopmentTest(
		workspace,
		&RunResult{RunID: "wr_running", Status: RunStatusRunning},
		nil,
	); recordErr != nil {
		t.Fatalf("RecordWorkflowDevelopmentTest(running) error = %v", recordErr)
	}

	nextYAML := GenerateWorkflowDraftYAML("route urgent support requests")
	session.YAML = nextYAML
	session.LastTest = nil
	if writeErr := writeActiveDevelopment(workspace, session); writeErr != nil {
		t.Fatalf("writeActiveDevelopment() error = %v", writeErr)
	}
	active, recorded, err := RecordWorkflowDevelopmentTestIfCurrent(
		workspace,
		session.ID,
		draftKey,
		&RunResult{RunID: "wr_running", Status: RunStatusSucceeded},
		nil,
	)
	if err != nil {
		t.Fatalf("RecordWorkflowDevelopmentTestIfCurrent() error = %v", err)
	}
	if recorded {
		t.Fatal("RecordWorkflowDevelopmentTestIfCurrent() recorded stale draft, want skipped")
	}
	if active == nil || active.LastTest != nil {
		t.Fatalf("active last test = %#v, want stale completion ignored", active)
	}
}

func TestCompatibilityRevalidationBlocksStaleRuntime(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	ref := "workflows/test.yml"
	if err := os.MkdirAll(filepath.Join(workspace, "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(workspace, ref),
		[]byte(GenerateWorkflowDraftYAML("do work")),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	runtimeV1 := RuntimeCompatibility{PicoclawVersion: "v1.0.0", GitCommit: "abc123"}
	summary, err := LoadCompatibilitySummary(ctx, workspace, runtimeV1)
	if err != nil {
		t.Fatalf("LoadCompatibilitySummary() error = %v", err)
	}
	if !summary.HasBlocking || summary.Counts[WorkflowValidationStatusPendingRevalidation] != 1 {
		t.Fatalf("summary before revalidate = %#v, want one pending blocking workflow", summary)
	}
	if runnableErr := EnsureWorkflowRunnable(ctx, workspace, ref, runtimeV1); runnableErr == nil {
		t.Fatal("EnsureWorkflowRunnable() before revalidate error = nil, want error")
	}

	if _, revalidateErr := RevalidateLocal(ctx, workspace, runtimeV1); revalidateErr != nil {
		t.Fatalf("RevalidateLocal() error = %v", revalidateErr)
	}
	if runnableErr := EnsureWorkflowRunnable(ctx, workspace, ref, runtimeV1); runnableErr != nil {
		t.Fatalf("EnsureWorkflowRunnable() after v1 revalidate error = %v", runnableErr)
	}

	runtimeV2 := RuntimeCompatibility{PicoclawVersion: "v1.1.0", GitCommit: "def456"}
	summary, err = LoadCompatibilitySummary(ctx, workspace, runtimeV2)
	if err != nil {
		t.Fatalf("LoadCompatibilitySummary(v2) error = %v", err)
	}
	if !summary.VersionChanged || summary.Counts[WorkflowValidationStatusPendingRevalidation] != 1 {
		t.Fatalf("summary after version change = %#v, want pending revalidation", summary)
	}
	if err := EnsureWorkflowRunnable(ctx, workspace, ref, runtimeV2); err == nil {
		t.Fatal("EnsureWorkflowRunnable() after version change error = nil, want error")
	}
}

func TestCompatibilityRevalidationBlocksChangedWorkflowHash(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	ref := "workflows/test.yml"
	if err := os.MkdirAll(filepath.Join(workspace, "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(workspace, ref),
		[]byte(GenerateWorkflowDraftYAML("do work")),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	runtime := RuntimeCompatibility{PicoclawVersion: "v1.0.0", GitCommit: "abc123"}
	manifest, err := RevalidateLocal(ctx, workspace, runtime)
	if err != nil {
		t.Fatalf("RevalidateLocal() error = %v", err)
	}
	previousHash := manifest.Workflows[ref].WorkflowHash
	if previousHash == "" {
		t.Fatal("validated workflow hash is empty")
	}

	if writeErr := os.WriteFile(
		filepath.Join(workspace, ref),
		[]byte(GenerateWorkflowDraftYAML("do different work")),
		0o644,
	); writeErr != nil {
		t.Fatal(writeErr)
	}
	summary, err := LoadCompatibilitySummary(ctx, workspace, runtime)
	if err != nil {
		t.Fatalf("LoadCompatibilitySummary() error = %v", err)
	}
	if summary.VersionChanged {
		t.Fatalf("VersionChanged = true, want false for workflow-only hash change")
	}
	if !summary.HasBlocking || summary.Counts[WorkflowValidationStatusPendingRevalidation] != 1 {
		t.Fatalf("summary after workflow hash change = %#v, want pending revalidation", summary)
	}
	if len(summary.Workflows) != 1 || summary.Workflows[0].WorkflowHash == previousHash {
		t.Fatalf("summary workflow hash = %#v, want current hash different from %s", summary.Workflows, previousHash)
	}
	if err := EnsureWorkflowRunnable(ctx, workspace, ref, runtime); err == nil {
		t.Fatal("EnsureWorkflowRunnable() after workflow hash change error = nil, want error")
	}
}
