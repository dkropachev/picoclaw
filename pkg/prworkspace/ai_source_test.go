package prworkspace

import (
	"context"
	"testing"
	"time"
)

func testAIExecutionSource(execution string) *AIExecutionSource {
	source := &AIExecutionSource{
		ExecutionID: execution,
		WorkspaceID: "prw_11111111111111111111111111111111",
		Binding:     "sha256:binding", AgentID: "main",
		SessionRevision: "sha256:revision", Tools: "none",
	}
	source.Session = aiExecutionSourceSessionKey(source)
	return source
}

func TestSourceForGateSubjectRequiresOneExactOriginatingExecution(t *testing.T) {
	first := Finding{source: testAIExecutionSource("aix_11111111111111111111111111111111")}
	selected, err := sourceForGateSubject(map[string]any{
		"finding": first,
	}, first.source.WorkspaceID)
	if err != nil || !sameAIExecutionSource(selected, first.source) {
		t.Fatalf("selected source = %#v, error = %v", selected, err)
	}
	second := Finding{source: testAIExecutionSource("aix_22222222222222222222222222222222")}
	if _, err := sourceForGateSubject(map[string]any{
		"findings": []Finding{first, second},
	}, first.source.WorkspaceID); err == nil {
		t.Fatal("mixed source subject was accepted")
	}
	if _, err := sourceForGateSubject(map[string]any{
		"finding": Finding{},
	}, first.source.WorkspaceID); err == nil {
		t.Fatal("missing source subject was accepted")
	}
}

func TestAIExecutionSourceBindsExactProtectedSessionIdentity(t *testing.T) {
	valid := testAIExecutionSource("aix_11111111111111111111111111111111")
	if !validAIExecutionSource(valid) {
		t.Fatalf("valid source was rejected: %#v", valid)
	}

	for _, test := range []struct {
		name   string
		mutate func(*AIExecutionSource)
	}{
		{
			name: "wrong session",
			mutate: func(source *AIExecutionSource) {
				other := *source
				other.ExecutionID = "aix_22222222222222222222222222222222"
				source.Session = aiExecutionSourceSessionKey(&other)
			},
		},
		{
			name: "workspace changed without rebinding",
			mutate: func(source *AIExecutionSource) {
				source.WorkspaceID = "prw_22222222222222222222222222222222"
			},
		},
		{
			name: "binding changed without rebinding",
			mutate: func(source *AIExecutionSource) {
				source.Binding = "sha256:other-binding"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := *valid
			test.mutate(&candidate)
			if validAIExecutionSource(&candidate) {
				t.Fatalf("cross-bound source was accepted: %#v", candidate)
			}
			if _, err := sourceForGateSubject(
				map[string]any{"finding": Finding{source: &candidate}},
				candidate.WorkspaceID,
			); err == nil {
				t.Fatal("cross-bound Gate subject was accepted")
			}
		})
	}

	if _, err := sourceForGateSubject(
		map[string]any{"finding": Finding{source: valid}},
		"prw_22222222222222222222222222222222",
	); err == nil {
		t.Fatal("source from another workspace was accepted")
	}
}

func TestGateSubjectFingerprintBindsPrivateSourceRevision(t *testing.T) {
	source := testAIExecutionSource("aix_11111111111111111111111111111111")
	finding := Finding{ID: "pfn_11111111111111111111111111111111", source: source}
	first, err := fingerprintGateSubject(map[string]any{"finding": finding})
	if err != nil {
		t.Fatal(err)
	}
	source.SessionRevision = "sha256:changed"
	second, err := fingerprintGateSubject(map[string]any{"finding": finding})
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("source revision did not change gate subject fingerprint")
	}
}

type provenanceReviewAI struct{}

func (provenanceReviewAI) RunIsolated(_ context.Context, request IsolatedAIRequest) (map[string]any, error) {
	source := &AIExecutionSource{
		ExecutionID: request.SourceExecutionID, WorkspaceID: request.SourceWorkspaceID,
		Binding: request.SourceBinding, AgentID: "main",
		SessionRevision: "sha256:source-revision", Tools: "none",
	}
	source.Session = aiExecutionSourceSessionKey(source)
	return map[string]any{
		"summary": "Found one issue.",
		"findings": []any{map[string]any{
			"severity": "high", "title": "Issue", "message": "Fix it",
			"impact": "Correctness", "recommendation": "Repair", "validation": "Test",
			"scope_distance": "S0_exact", "change_size": "XS", "type_compatible": true,
			"scope_confidence": 1.0, "scope_explanation": "Exact", "charter_clauses": []any{"goal"},
		}},
		"coverage": map[string]any{
			"reviewed_areas": []any{}, "unreviewed_areas": []any{},
			"tests_considered": []any{}, "residual_risks": []any{},
		},
		"__source-execution": map[string]any{
			"source_execution_id": request.SourceExecutionID,
			"source_workspace_id": request.SourceWorkspaceID,
			"source_binding":      request.SourceBinding, "source_agent_id": "main",
			"source_session": source.Session, "source_revision": "sha256:source-revision",
			"source_tools": "none",
		},
	}, nil
}

func TestReviewRoundAndMaterializedFindingRetainSourceProvenance(t *testing.T) {
	bundle := testPromptBundle()
	rounds, err := (AIController{Runner: provenanceReviewAI{}}).RunReviewSearch(
		t.Context(), bundle, ConfiguredNudgePolicy(0, 0), nil,
	)
	if err != nil || len(rounds) != 1 || rounds[0].Source == nil {
		t.Fatalf("rounds = %#v, error = %v", rounds, err)
	}
	aggregate := Aggregate{Workspace: Workspace{ID: bundle.WorkspaceID}}
	findings, _ := materializeReviewRounds(
		aggregate, "psr_11111111111111111111111111111111", rounds,
		ConfiguredNudgePolicy(0, 0), time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC),
	)
	if len(findings) != 1 || !findings[0].SourceAvailable || findings[0].source == nil ||
		findings[0].source.Session != aiExecutionSourceSessionKey(findings[0].source) {
		t.Fatalf("findings = %#v", findings)
	}
}

func TestCompletionFindingReusePreservesImmutableSourceState(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	candidate := CompletionFinding{AgentFinding: AgentFinding{
		Severity: "medium", Title: "Missing check", Message: "Add the check",
		ScopeDistance: ScopeExact, ChangeSize: ChangeSizeS, TypeCompatible: true,
	}}
	newSource := testAIExecutionSource("aix_22222222222222222222222222222222")

	withoutSource := Finding{ID: "pfn_11111111111111111111111111111111", Version: 1, CreatedAt: now}
	reused := completionFindingRecord(
		withoutSource, true, withoutSource.ID, "sha256:finding", "psr_run", "pnr_round",
		candidate, FindingInScope, newSource, now,
	)
	if reused.source != nil || reused.SourceAvailable {
		t.Fatalf("source-less finding was retargeted: %#v", reused)
	}

	existingSource := testAIExecutionSource("aix_33333333333333333333333333333333")
	withSource := withoutSource
	withSource.SourceAvailable, withSource.source = true, existingSource
	reused = completionFindingRecord(
		withSource, true, withSource.ID, "sha256:finding", "psr_run", "pnr_round",
		candidate, FindingInScope, newSource, now,
	)
	if !sameAIExecutionSource(reused.source, existingSource) {
		t.Fatalf("existing finding source changed: %#v", reused.source)
	}
}
