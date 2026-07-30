package workflows

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestReviseWorkflowDevelopmentFencedRejectsMismatchWithoutWrite(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	session, err := StartWorkflowDevelopment(
		context.Background(),
		workspace,
		RuntimeCompatibility{},
		WorkflowDevelopmentStartRequest{
			Reason:    WorkflowDevelopmentReasonNew,
			Prompt:    "initial prompt",
			TargetRef: "workflows/initial.yml",
		},
	)
	if err != nil {
		t.Fatalf("StartWorkflowDevelopment() error = %v", err)
	}
	activePath, err := checkedActiveDevelopmentPath(workspace)
	if err != nil {
		t.Fatalf("checkedActiveDevelopmentPath() error = %v", err)
	}
	before, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatalf("os.ReadFile(active) error = %v", err)
	}
	exact := WorkflowDevelopmentTestDraftFence{
		SessionID:               session.ID,
		ExpectedSessionRevision: session.SessionRevision,
		ExpectedDraftRevision:   session.DraftRevision,
	}
	updatedYAML := "name: should not persist\njobs: {}\n"
	tests := []struct {
		name   string
		mutate func(*WorkflowDevelopmentTestDraftFence)
	}{
		{
			name: "missing session ID",
			mutate: func(fence *WorkflowDevelopmentTestDraftFence) {
				fence.SessionID = ""
			},
		},
		{
			name: "wrong session ID",
			mutate: func(fence *WorkflowDevelopmentTestDraftFence) {
				fence.SessionID = "dev_stale"
			},
		},
		{
			name: "wrong session revision",
			mutate: func(fence *WorkflowDevelopmentTestDraftFence) {
				fence.ExpectedSessionRevision = "sha256:stale"
			},
		},
		{
			name: "missing session revision",
			mutate: func(fence *WorkflowDevelopmentTestDraftFence) {
				fence.ExpectedSessionRevision = ""
			},
		},
		{
			name: "wrong draft revision",
			mutate: func(fence *WorkflowDevelopmentTestDraftFence) {
				fence.ExpectedDraftRevision = "sha256:stale"
			},
		},
		{
			name: "missing draft revision",
			mutate: func(fence *WorkflowDevelopmentTestDraftFence) {
				fence.ExpectedDraftRevision = ""
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fence := exact
			test.mutate(&fence)
			revised, reviseErr := ReviseWorkflowDevelopmentFenced(
				workspace,
				fence,
				WorkflowDevelopmentReviseRequest{
					Prompt:    "changed prompt",
					TargetRef: "workflows/changed.yml",
					YAML:      &updatedYAML,
				},
			)
			if revised != nil ||
				!errors.Is(reviseErr, ErrWorkflowDevelopmentFenceMismatch) {
				t.Fatalf(
					"ReviseWorkflowDevelopmentFenced() = %#v, %v",
					revised,
					reviseErr,
				)
			}
			after, readErr := os.ReadFile(activePath)
			if readErr != nil {
				t.Fatalf("os.ReadFile(active) error = %v", readErr)
			}
			if string(after) != string(before) {
				t.Fatalf("active session changed after fence mismatch")
			}
		})
	}
}

func TestReviseWorkflowDevelopmentFencedChecksFenceBeforeBusyState(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	_, err := StartWorkflowDevelopment(
		context.Background(),
		workspace,
		RuntimeCompatibility{},
		WorkflowDevelopmentStartRequest{
			Prompt:    "initial prompt",
			TargetRef: "workflows/initial.yml",
		},
	)
	if err != nil {
		t.Fatalf("StartWorkflowDevelopment() error = %v", err)
	}
	active, err := RecordWorkflowDevelopmentTest(
		workspace,
		&RunResult{RunID: "wr_running", Status: RunStatusRunning},
		nil,
	)
	if err != nil {
		t.Fatalf("RecordWorkflowDevelopmentTest() error = %v", err)
	}
	activePath, err := checkedActiveDevelopmentPath(workspace)
	if err != nil {
		t.Fatalf("checkedActiveDevelopmentPath() error = %v", err)
	}
	before, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatalf("os.ReadFile(active) error = %v", err)
	}
	exact := WorkflowDevelopmentTestDraftFence{
		SessionID:               active.ID,
		ExpectedSessionRevision: active.SessionRevision,
		ExpectedDraftRevision:   active.DraftRevision,
	}
	if _, reviseErr := ReviseWorkflowDevelopmentFenced(
		workspace,
		exact,
		WorkflowDevelopmentReviseRequest{Prompt: "changed"},
	); !errors.Is(reviseErr, ErrDevelopmentBusy) {
		t.Fatalf("exact fence error = %v, want ErrDevelopmentBusy", reviseErr)
	}
	stale := exact
	stale.ExpectedDraftRevision = "sha256:stale"
	if _, reviseErr := ReviseWorkflowDevelopmentFenced(
		workspace,
		stale,
		WorkflowDevelopmentReviseRequest{Prompt: "changed"},
	); !errors.Is(reviseErr, ErrWorkflowDevelopmentFenceMismatch) {
		t.Fatalf(
			"stale fence error = %v, want ErrWorkflowDevelopmentFenceMismatch",
			reviseErr,
		)
	}
	after, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatalf("os.ReadFile(active) error = %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("busy or stale fenced revision changed active bytes")
	}
}

func TestReviseWorkflowDevelopmentFencedAppliesExactRevision(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	session, err := StartWorkflowDevelopment(
		context.Background(),
		workspace,
		RuntimeCompatibility{},
		WorkflowDevelopmentStartRequest{
			Reason:    WorkflowDevelopmentReasonNew,
			Prompt:    "initial prompt",
			TargetRef: "workflows/initial.yml",
		},
	)
	if err != nil {
		t.Fatalf("StartWorkflowDevelopment() error = %v", err)
	}
	exactYAML := "name: revised\non:\n  manual: {}\njobs:\n  main:\n    runs-on: picoclaw\n    steps:\n      - uses: agent/main\n"
	revised, err := ReviseWorkflowDevelopmentFenced(
		workspace,
		WorkflowDevelopmentTestDraftFence{
			SessionID:               session.ID,
			ExpectedSessionRevision: session.SessionRevision,
			ExpectedDraftRevision:   session.DraftRevision,
		},
		WorkflowDevelopmentReviseRequest{
			Prompt:    " revised prompt ",
			TargetRef: "workflows/revised.yml",
			YAML:      &exactYAML,
		},
	)
	if err != nil {
		t.Fatalf("ReviseWorkflowDevelopmentFenced() error = %v", err)
	}
	if revised.Prompt != "revised prompt" ||
		revised.TargetWorkflowRef != "workflows/revised.yml" ||
		revised.YAML != exactYAML ||
		revised.SessionRevision == session.SessionRevision ||
		revised.DraftRevision == session.DraftRevision {
		t.Fatalf("revised session = %#v", revised)
	}
}
