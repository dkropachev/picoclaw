package prworkspace

import (
	"context"
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

func (runner *captureCompletionAI) RunIsolated(_ context.Context, request IsolatedAIRequest) (map[string]any, error) {
	if request.Operation == "completion.initial" {
		runner.user = request.UserPrompt
		return map[string]any{
			"summary": "complete", "complete": true,
			"missing_in_scope": []any{}, "out_of_scope": []any{},
			"coverage": map[string]any{
				"reviewed_areas": []any{"exact candidate"}, "unreviewed_areas": []any{},
				"tests_considered": []any{"go test"}, "residual_risks": []any{},
			},
		}, nil
	}
	return nil, context.Canceled
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
