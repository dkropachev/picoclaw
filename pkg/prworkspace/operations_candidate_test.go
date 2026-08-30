package prworkspace

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fixedCandidateEvidenceLoader struct{ value CandidateEvidence }

func (loader fixedCandidateEvidenceLoader) LoadCandidateEvidence(
	context.Context,
	RepairAttempt,
) (CandidateEvidence, error) {
	return loader.value, nil
}

type captureCompletionAI struct{ user string }

func (runner *captureCompletionAI) RunIsolated(_ context.Context, request IsolatedAIRequest) (IsolatedAIResult, error) {
	if request.Operation == "completion.initial" {
		runner.user = request.UserPrompt
		return successfulIsolatedAIResult(map[string]any{
			"summary": "complete", "complete": true,
			"missing_in_scope": []any{}, "out_of_scope": []any{},
			"coverage": map[string]any{
				"reviewed_areas": []any{"exact candidate"}, "unreviewed_areas": []any{},
				"tests_considered": []any{"go test"}, "residual_risks": []any{},
			},
		}), nil
	}
	return IsolatedAIResult{}, context.Canceled
}

type failedMeasuredCompletionAI struct{}

func (failedMeasuredCompletionAI) RunIsolated(
	context.Context,
	IsolatedAIRequest,
) (IsolatedAIResult, error) {
	return IsolatedAIResult{
		Usage: TokenUsage{
			ProviderCalls: 1, UsageReportedCalls: 1,
			PromptTokens: 7, CachedTokens: 3,
			CompletionTokens: 2, ReasoningTokens: 1, TotalTokens: 9,
			LatencyMillis: 11,
		},
		Complete: true,
	}, errors.New("completion provider failed")
}

type mismatchedMeasuredCompletionAI struct{}

func (mismatchedMeasuredCompletionAI) RunIsolated(
	context.Context,
	IsolatedAIRequest,
) (IsolatedAIResult, error) {
	finding := completionFindingJSON("candidate_present", "S2_related_followup", "XS", true)
	finding["file"] = "pkg/other.go"
	result := successfulIsolatedAIResult(map[string]any{
		"summary": "candidate contains scope drift", "complete": true,
		"missing_in_scope": []any{}, "out_of_scope": []any{finding},
		"coverage": coverageJSON(),
	})
	result.Usage = TokenUsage{
		ProviderCalls: 1, UsageReportedCalls: 1,
		PromptTokens: 7, CachedTokens: 3,
		CompletionTokens: 2, ReasoningTokens: 1, TotalTokens: 9,
		LatencyMillis: 11,
	}
	return result, nil
}

func TestStandaloneCompletionAuditPersistsPartialUsageBeforeReturningError(t *testing.T) {
	failed := runMeasuredStandaloneCompletionFailure(
		t,
		"request-initial-implementation-usage",
		"request-failed-completion-usage",
		"sha256:failed-completion-candidate",
		failedMeasuredCompletionAI{},
	)
	last := failed.StageRuns[len(failed.StageRuns)-1]
	if failed.Workspace.ExecutionState != ExecutionFailed || last.Stage != "completion_audit" ||
		last.State != ExecutionFailed || last.PublicError != "completion_audit_failed" {
		t.Fatalf("failed completion aggregate = %#v", failed)
	}
}

func TestStandaloneCompletionAuditPersistsUsageOnCandidateEvidenceMismatch(t *testing.T) {
	failed := runMeasuredStandaloneCompletionFailure(
		t,
		"request-mismatch-implementation-usage",
		"request-mismatched-completion-usage",
		"sha256:mismatched-completion-candidate",
		mismatchedMeasuredCompletionAI{},
	)
	last := failed.StageRuns[len(failed.StageRuns)-1]
	if failed.Workspace.ExecutionState != ExecutionFailed || last.Stage != "completion_audit" ||
		last.State != ExecutionFailed || last.PublicError != "completion_candidate_evidence_mismatch" {
		t.Fatalf("failed completion aggregate = %#v", failed)
	}
}

func runMeasuredStandaloneCompletionFailure(
	t *testing.T,
	implementationRequestID string,
	auditRequestID string,
	evidenceDigest string,
	runner IsolatedAIRunner,
) Aggregate {
	t.Helper()
	service, aggregate := readyImplementationService(t)
	implemented, err := service.RunImplementation(t.Context(), ImplementationConfig{
		Repair: &implementationRepair{}, Validation: implementationValidation{},
	}, RunImplementationRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: implementationRequestID, FindingIDs: []string{aggregate.Findings[0].ID},
		NudgePolicy: ConfiguredNudgePolicy(0, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	repair := implemented.RepairAttempts[len(implemented.RepairAttempts)-1]
	service.candidateEvidence = fixedCandidateEvidenceLoader{value: CandidateEvidence{
		CandidateSHA: repair.CandidateSHA, CandidateDiff: testCandidateDiff,
		Metrics: CandidateMetrics{
			Files: 1, SemanticLines: 10, Modules: 1, ChangedFiles: []string{"pkg/retry.go"},
		},
		EvidenceDigest: evidenceDigest,
	}}
	service.ai.Runner = runner
	failed, err := service.RunCompletionAudit(t.Context(), RunCompletionAuditRequest{
		WorkspaceID: implemented.Workspace.ID, ExpectedVersion: implemented.Workspace.Version,
		RequestID: auditRequestID, NudgePolicy: ConfiguredNudgePolicy(0, 0),
	})
	if err == nil {
		t.Fatal("completion audit failure was not returned")
	}
	implementationStage, ok := findStageRun(failed.StageRuns, repair.StageRunID)
	if !ok || implementationStage.Usage == nil || implementationStage.Usage.Complete ||
		implementationStage.Usage.Audit.ProviderCalls != 3 ||
		implementationStage.Usage.Audit.PromptTokens != 9 ||
		implementationStage.Usage.Audit.TotalTokens != 13 {
		t.Fatalf("implementation stage usage = %#v", implementationStage.Usage)
	}
	return failed
}

func TestStandaloneCompletionAuditLoadsExactCandidateEvidence(t *testing.T) {
	service, aggregate := readyImplementationService(t)
	implemented, err := service.RunImplementation(context.Background(), ImplementationConfig{
		Repair: &implementationRepair{}, Validation: implementationValidation{},
	}, RunImplementationRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-standalone-implementation", FindingIDs: []string{aggregate.Findings[0].ID},
		NudgePolicy: ConfiguredNudgePolicy(0, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	diff := "diff --git a/pkg/retry.go b/pkg/retry.go\n+exact candidate line\n"
	service.candidateEvidence = fixedCandidateEvidenceLoader{value: CandidateEvidence{
		CandidateSHA:  implemented.RepairAttempts[0].CandidateSHA,
		CandidateDiff: diff,
		Metrics: CandidateMetrics{
			Files:         1,
			SemanticLines: 1,
			Modules:       1,
			ChangedFiles:  []string{"pkg/retry.go"},
		},
		EvidenceDigest: "sha256:exact-candidate",
	}}
	ai := &captureCompletionAI{}
	service.ai.Runner = ai
	audited, err := service.RunCompletionAudit(context.Background(), RunCompletionAuditRequest{
		WorkspaceID: implemented.Workspace.ID, ExpectedVersion: implemented.Workspace.Version,
		RequestID: "request-standalone-completion", NudgePolicy: ConfiguredNudgePolicy(0, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ai.user, "exact candidate line") || !strings.Contains(ai.user, `"semantic_lines":1`) {
		t.Fatalf("exact candidate evidence missing from completion prompt: %q", ai.user)
	}
	stage := audited.StageRuns[len(audited.StageRuns)-1]
	if stage.Evidence == nil || stage.Evidence.Coverage.ReviewedAreas[0] != "exact candidate" {
		t.Fatalf("completion evidence = %#v", stage.Evidence)
	}
}
