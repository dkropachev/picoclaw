package workflows

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkflowDevelopmentDraftRevisionUsesExactLengthDelimitedBytes(t *testing.T) {
	first := WorkflowDevelopmentDraftRevision("workflows/a.yml", "bc")
	second := WorkflowDevelopmentDraftRevision("workflows/a.ymlb", "c")
	if first == second {
		t.Fatal("length-delimited draft revisions collided")
	}
	if first == WorkflowDevelopmentDraftRevision("workflows/a.yml", "bc\n") {
		t.Fatal("draft revision normalized exact YAML bytes")
	}
	if first != WorkflowDevelopmentDraftRevision("workflows/a.yml", "bc") {
		t.Fatal("draft revision is not deterministic")
	}
}

func TestCaptureWorkflowDevelopmentTargetRevisionHonorsDefinitionsDir(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "automation", "triage.yml")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(target, []byte("name: first\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	revision, err := captureWorkflowDevelopmentTargetRevision(
		workspace,
		"workflows/triage.yml",
		WithDefinitionsDir("automation"),
	)
	if err != nil {
		t.Fatalf("captureWorkflowDevelopmentTargetRevision() error = %v", err)
	}
	if revision != workflowContentRevision([]byte("name: first\n")) {
		t.Fatalf("revision = %q", revision)
	}
}

func TestCheckWorkflowDevelopmentPublishRevisionsRejectsEachStaleFence(t *testing.T) {
	session := &WorkflowDevelopmentSession{
		ID:                 "dev_1",
		SessionRevision:    "sha256:session",
		DraftRevision:      "sha256:draft",
		BaseTargetRevision: "sha256:target",
	}
	valid := WorkflowDevelopmentPublishRequest{
		SessionID:                  session.ID,
		ExpectedSessionRevision:    session.SessionRevision,
		ExpectedDraftRevision:      session.DraftRevision,
		ExpectedBaseTargetRevision: session.BaseTargetRevision,
	}
	if err := checkWorkflowDevelopmentPublishRevisions(session, valid, session.BaseTargetRevision); err != nil {
		t.Fatalf("valid revisions error = %v", err)
	}

	staleSession := valid
	staleSession.ExpectedSessionRevision = "sha256:stale"
	if err := checkWorkflowDevelopmentPublishRevisions(
		session,
		staleSession,
		session.BaseTargetRevision,
	); !errors.Is(err, ErrWorkflowSessionRevisionMismatch) {
		t.Fatalf("stale session error = %v", err)
	}

	staleDraft := valid
	staleDraft.ExpectedDraftRevision = "sha256:stale"
	if err := checkWorkflowDevelopmentPublishRevisions(
		session,
		staleDraft,
		session.BaseTargetRevision,
	); !errors.Is(err, ErrWorkflowDraftRevisionMismatch) {
		t.Fatalf("stale draft error = %v", err)
	}

	staleTarget := valid
	if err := checkWorkflowDevelopmentPublishRevisions(
		session,
		staleTarget,
		"sha256:external-edit",
	); !errors.Is(err, ErrWorkflowTargetRevisionMismatch) {
		t.Fatalf("stale target error = %v", err)
	}
}

func TestWorkflowDevelopmentSessionRevisionsFenceEveryMutationAndExactDraft(t *testing.T) {
	workspace := t.TempDir()
	session, err := StartWorkflowDevelopment(
		context.Background(),
		workspace,
		RuntimeCompatibility{PicoclawVersion: "v1.0.0"},
		WorkflowDevelopmentStartRequest{
			Prompt:    "triage events",
			TargetRef: "workflows/triage.yml",
		},
	)
	if err != nil {
		t.Fatalf("StartWorkflowDevelopment() error = %v", err)
	}
	if session.SessionRevision == "" ||
		session.DraftRevision == "" ||
		session.BaseTargetRevision != WorkflowTargetRevisionMissing {
		t.Fatalf("start revisions = %#v", session)
	}
	startSessionRevision := session.SessionRevision
	startDraftRevision := session.DraftRevision

	validated, err := ValidateWorkflowDevelopment(workspace)
	if err != nil {
		t.Fatalf("ValidateWorkflowDevelopment() error = %v", err)
	}
	if validated.SessionRevision == startSessionRevision {
		t.Fatal("validation did not advance session revision")
	}
	if validated.DraftRevision != startDraftRevision {
		t.Fatal("validation changed exact draft revision")
	}

	updatedYAML := validated.YAML + "# exact revision change\n"
	revised, err := ReviseWorkflowDevelopment(
		workspace,
		WorkflowDevelopmentReviseRequest{YAML: &updatedYAML},
	)
	if err != nil {
		t.Fatalf("ReviseWorkflowDevelopment() error = %v", err)
	}
	if revised.SessionRevision == validated.SessionRevision {
		t.Fatal("revise did not advance session revision")
	}
	if revised.DraftRevision == validated.DraftRevision {
		t.Fatal("revise did not advance exact draft revision")
	}
	if revised.DraftRevision != WorkflowDevelopmentDraftRevision(
		revised.TargetWorkflowRef,
		revised.YAML,
	) {
		t.Fatalf("draft revision = %q", revised.DraftRevision)
	}
}

func TestReviseWorkflowDevelopmentPreservesTrailingYAMLBytesAndStalesTest(
	t *testing.T,
) {
	workspace := t.TempDir()
	session, err := StartWorkflowDevelopment(
		context.Background(),
		workspace,
		RuntimeCompatibility{PicoclawVersion: "v1.0.0"},
		WorkflowDevelopmentStartRequest{
			Prompt:    "triage events",
			TargetRef: "workflows/triage.yml",
		},
	)
	if err != nil {
		t.Fatalf("StartWorkflowDevelopment() error = %v", err)
	}
	session, err = RecordWorkflowDevelopmentTest(
		workspace,
		&RunResult{RunID: "wr_exact_yaml", Status: RunStatusSucceeded},
		nil,
	)
	if err != nil {
		t.Fatalf("RecordWorkflowDevelopmentTest() error = %v", err)
	}
	previousDraftRevision := session.DraftRevision
	previousLegacyKey := WorkflowDevelopmentDraftKey(
		session.TargetWorkflowRef,
		session.YAML,
	)
	exactYAML := session.YAML + " \t\n\n"
	if got := WorkflowDevelopmentDraftKey(
		session.TargetWorkflowRef,
		exactYAML,
	); got != previousLegacyKey {
		t.Fatalf("test setup legacy key = %q, want %q", got, previousLegacyKey)
	}

	revised, err := ReviseWorkflowDevelopment(
		workspace,
		WorkflowDevelopmentReviseRequest{YAML: &exactYAML},
	)
	if err != nil {
		t.Fatalf("ReviseWorkflowDevelopment() error = %v", err)
	}
	if revised.YAML != exactYAML {
		t.Fatalf("revised YAML = %q, want exact bytes %q", revised.YAML, exactYAML)
	}
	if revised.DraftRevision == previousDraftRevision ||
		revised.DraftRevision != WorkflowDevelopmentDraftRevision(
			revised.TargetWorkflowRef,
			exactYAML,
		) {
		t.Fatalf("exact draft revision = %q", revised.DraftRevision)
	}
	if revised.LastTest != nil {
		t.Fatalf("last test = %#v, want stale test cleared", revised.LastTest)
	}
}

func TestWorkflowDevelopmentTargetChangeCapturesNewBaseRevision(t *testing.T) {
	workspace := t.TempDir()
	customDefinitions := filepath.Join(workspace, "automation")
	if err := os.MkdirAll(customDefinitions, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	targetBytes := []byte("name: existing\non:\n  manual: {}\njobs: {}\n")
	if err := os.WriteFile(
		filepath.Join(customDefinitions, "existing.yml"),
		targetBytes,
		0o644,
	); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	opts := []LocalOption{WithDefinitionsDir("automation")}
	session, err := StartWorkflowDevelopment(
		context.Background(),
		workspace,
		RuntimeCompatibility{PicoclawVersion: "v1.0.0"},
		WorkflowDevelopmentStartRequest{
			Prompt:    "new workflow",
			TargetRef: "workflows/new.yml",
		},
		opts...,
	)
	if err != nil {
		t.Fatalf("StartWorkflowDevelopment() error = %v", err)
	}
	if session.BaseTargetRevision != WorkflowTargetRevisionMissing {
		t.Fatalf("new base revision = %q", session.BaseTargetRevision)
	}

	revised, err := ReviseWorkflowDevelopment(
		workspace,
		WorkflowDevelopmentReviseRequest{
			TargetRef: "workflows/existing.yml",
		},
		opts...,
	)
	if err != nil {
		t.Fatalf("ReviseWorkflowDevelopment() error = %v", err)
	}
	if revised.BaseTargetRevision != workflowContentRevision(targetBytes) {
		t.Fatalf("existing base revision = %q", revised.BaseTargetRevision)
	}
}
