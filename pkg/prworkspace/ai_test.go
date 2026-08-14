package prworkspace

import (
	"context"
	"strings"
	"testing"
)

type scriptedIsolatedAI struct {
	reviewCalls int
	operations  []string
	planPrompts []string
	planSystem  string
}

func (runner *scriptedIsolatedAI) RunIsolated(_ context.Context, request IsolatedAIRequest) (map[string]any, error) {
	runner.operations = append(runner.operations, request.Operation)
	if request.Operation == "nudge.plan" {
		runner.planPrompts = append(runner.planPrompts, request.UserPrompt)
		runner.planSystem = request.SystemPrompt
		return nil, context.Canceled // force deterministic wording; nudge must still run
	}
	runner.reviewCalls++
	if request.Operation == "completion.initial" || request.Operation == "completion.nudge" {
		return map[string]any{
			"summary": "No missing work found.", "complete": true,
			"missing_in_scope": []any{}, "out_of_scope": []any{},
			"coverage": map[string]any{"reviewed_areas": []any{}, "unreviewed_areas": []any{}, "tests_considered": []any{}, "residual_risks": []any{}},
		}, nil
	}
	return map[string]any{
		"summary": "No findings.", "findings": []any{},
		"coverage": map[string]any{"reviewed_areas": []any{}, "unreviewed_areas": []any{}, "tests_considered": []any{}, "residual_risks": []any{}},
	}, nil
}

func testPromptBundle() PRContextBundle {
	return PRContextBundle{
		WorkspaceID: "prw_11111111111111111111111111111111",
		Charter:     Charter{ID: "pcr_11111111111111111111111111111111", Type: PRTypeFix, Confirmed: true},
	}
}

func TestReviewSearchRunsMandatoryNudgesAfterNoFindings(t *testing.T) {
	runner := &scriptedIsolatedAI{}
	rounds, err := (AIController{Runner: runner}).RunReviewSearch(
		context.Background(), testPromptBundle(), DefaultNudgePolicy(), nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(rounds) != 3 {
		t.Fatalf("rounds = %d, want initial plus two nudges", len(rounds))
	}
	if rounds[1].Challenge == "" || rounds[2].Challenge == "" {
		t.Fatal("automatic nudge wording missing")
	}
	if rounds[1].Strategy == rounds[2].Strategy || rounds[1].VariantDigest == rounds[2].VariantDigest {
		t.Fatalf("mandatory rounds did not explore variants: %#v %#v", rounds[1], rounds[2])
	}
	if rounds[1].State != ExecutionSucceeded || rounds[2].State != ExecutionSucceeded {
		t.Fatalf("nudge attempt states = %q, %q", rounds[1].State, rounds[2].State)
	}
}

func TestCompletionAuditRunsMandatoryNudgesAfterCompleteClaim(t *testing.T) {
	runner := &scriptedIsolatedAI{}
	rounds, err := (AIController{Runner: runner}).RunCompletionAudit(
		context.Background(), testPromptBundle(), DefaultNudgePolicy(), nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(rounds) != 3 {
		t.Fatalf("rounds = %d, want initial plus two nudges", len(rounds))
	}
}

func TestNovelFindingsExtendNudgesToHardCap(t *testing.T) {
	seen := newSemanticFindingSet()
	first := AgentFinding{Severity: "high", Title: "one", Message: "one"}
	if novel, duplicate := countNovelFindings([]AgentFinding{first, first}, seen); novel != 1 || duplicate != 1 {
		t.Fatalf("novel=%d duplicate=%d", novel, duplicate)
	}
}

func TestWordingPlannerReceivesDurableDelayedVariantFeedback(t *testing.T) {
	runner := &scriptedIsolatedAI{}
	bundle := testPromptBundle()
	reward := 1.0
	bundle.NudgeLearning = []NudgeLearningExample{{
		Stage: NudgeReviewSearch, Strategy: NudgeValidation,
		VariantDigest: "sha256:useful", Challenge: "challenge validation evidence",
		Reward: &reward, RewardProvenance: "green_validation",
	}}
	bundle.PriorEvidence = []StageEvidence{{Stage: "review", Summary: "prior coverage", Coverage: Coverage{UnreviewedAreas: []string{"retry cancellation"}}}}
	_, err := (AIController{Runner: runner}).RunReviewSearch(
		context.Background(), bundle, NudgePolicy{MinimumAdditionalRounds: 1, MaximumAdditionalRounds: 1}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.planPrompts) != 1 || !strings.Contains(runner.planPrompts[0], "sha256:useful") ||
		!strings.Contains(runner.planPrompts[0], "green_validation") ||
		!strings.Contains(runner.planPrompts[0], "retry cancellation") {
		t.Fatalf("durable feedback missing from planner request: %#v", runner.planPrompts)
	}
	if !strings.Contains(runner.planSystem, "zero findings is unresolved") {
		t.Fatalf("zero-finding guard missing from planner system prompt: %q", runner.planSystem)
	}
}

func TestManualReviewNudgeChallengesPriorEvidenceWithoutInitialPass(t *testing.T) {
	runner := &scriptedIsolatedAI{}
	bundle := testPromptBundle()
	bundle.PriorEvidence = []StageEvidence{{
		Stage: "review", Summary: "prior review", Coverage: Coverage{UnreviewedAreas: []string{"error recovery"}},
	}}
	round, err := (AIController{Runner: runner}).RunReviewNudge(context.Background(), bundle, nil)
	if err != nil {
		t.Fatal(err)
	}
	if round.Initial || round.State != ExecutionSucceeded {
		t.Fatalf("manual round = %#v", round)
	}
	for _, operation := range runner.operations {
		if operation == "review.initial" {
			t.Fatalf("manual nudge reran initial review: %#v", runner.operations)
		}
	}
	if !strings.Contains(runner.planPrompts[0], "error recovery") {
		t.Fatalf("prior evidence missing from planner: %q", runner.planPrompts[0])
	}
}
