package prworkspace

import (
	"strings"
	"testing"
)

func TestCompilePromptSeparatesStageAuthorityAndSharedFacts(t *testing.T) {
	bundle := PRContextBundle{
		WorkspaceID: "prw_11111111111111111111111111111111",
		Charter:     Charter{ID: "pcr_11111111111111111111111111111111", Confirmed: true, Type: PRTypeFix},
	}
	review, err := CompilePrompt(PromptReviewSearch, bundle, "")
	if err != nil {
		t.Fatal(err)
	}
	repair, err := CompilePrompt(PromptRepair, bundle, "")
	if err != nil {
		t.Fatal(err)
	}
	if review.SystemPrompt == repair.SystemPrompt || review.Digest == repair.Digest {
		t.Fatal("review and repair authority collapsed into one prompt")
	}
	if !strings.Contains(review.UserPrompt, bundle.WorkspaceID) ||
		!strings.Contains(repair.UserPrompt, bundle.WorkspaceID) {
		t.Fatal("shared facts absent from specialized prompt")
	}
	if !strings.Contains(review.SystemPrompt, "NON-OVERRIDABLE FINDING POLICY") ||
		!strings.Contains(review.SystemPrompt, "Never provide") ||
		strings.Contains(repair.SystemPrompt, "NON-OVERRIDABLE FINDING POLICY") {
		t.Fatalf("diagnosis-only review boundary review=%q repair=%q", review.SystemPrompt, repair.SystemPrompt)
	}
	properties := agentFindingSchema()["properties"].(map[string]any)
	if _, exists := properties["recommendation"]; exists {
		t.Fatalf("model finding schema still exposes recommendation: %#v", properties)
	}
}

func TestCompilePromptRejectsUnconfirmedCharterOutsideDraft(t *testing.T) {
	bundle := PRContextBundle{
		WorkspaceID: "prw_11111111111111111111111111111111",
		Charter:     Charter{ID: "pcr_11111111111111111111111111111111", Type: PRTypeFix},
	}
	for _, stage := range []PromptStage{
		PromptReviewSearch, PromptReviewNudge, PromptRepair, PromptScopeAudit,
		PromptLocalReview, PromptCompletionAudit, PromptCompletionNudge,
		PromptGateEvaluation, PromptDeferredIssue,
	} {
		if _, err := CompilePrompt(stage, bundle, ""); err == nil || !strings.Contains(err.Error(), "confirmed") {
			t.Fatalf("stage %q accepted unconfirmed charter: %v", stage, err)
		}
	}
	if _, err := CompilePrompt(PromptCharterDraft, bundle, ""); err != nil {
		t.Fatalf("charter draft rejected unconfirmed facts: %v", err)
	}
}
