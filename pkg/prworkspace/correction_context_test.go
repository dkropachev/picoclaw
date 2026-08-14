package prworkspace

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPreCharterCorrectionRemainsWorkspaceScopedAcrossPromptAudiences(t *testing.T) {
	now := time.Date(2026, 8, 14, 4, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	input := testCreateInput()
	created, err := store.Create(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceConfig{Store: store, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	corrected, err := service.AddCorrection(t.Context(), AddCorrectionRequest{
		WorkspaceID: input.Workspace.ID, ExpectedVersion: created.Aggregate.Workspace.Version,
		RequestID: "request-workspace-correction-before-charter",
		Correction: Correction{
			Kind: CorrectionFactual, Applicability: CorrectionReviewAndImpl,
			TargetType: "workspace", TargetID: input.Workspace.ID,
			OriginalClaim: "Compatibility must be retained.",
			Correction:    "No backward compatibility is required for this redesign.",
			Evidence:      "The user explicitly authorized a breaking replacement.",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(corrected.Corrections) != 1 || corrected.Corrections[0].CharterID != "" ||
		corrected.Corrections[0].HeadSHA != input.Provider.HeadSHA {
		t.Fatalf("pre-charter correction = %#v", corrected.Corrections)
	}
	draftBundle := contextBundle(corrected)
	if len(draftBundle.Corrections) != 1 {
		t.Fatalf("draft corrections = %#v", draftBundle.Corrections)
	}
	draftPrompt, err := CompilePrompt(PromptCharterDraft, draftBundle, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(draftPrompt.UserPrompt, corrected.Corrections[0].Correction) {
		t.Fatalf("draft prompt omitted workspace correction: %s", draftPrompt.UserPrompt)
	}

	confirmedAt := now.Add(time.Minute)
	charter := Charter{
		ID: "pcr_44444444444444444444444444444444", Revision: 1, Type: PRTypeRefactor,
		Goal:    "Replace the PR workflow without compatibility shims",
		BaseSHA: input.Provider.BaseSHA, HeadSHA: input.Provider.HeadSHA,
		Confirmed: true, CreatedAt: now, ConfirmedAt: &confirmedAt,
	}
	seeded, err := store.Mutate(t.Context(), Mutation{
		WorkspaceID: input.Workspace.ID, ExpectedVersion: corrected.Workspace.Version,
		RequestID: "request-confirm-after-workspace-correction",
		Patch:     AggregatePatch{ActiveCharterID: &charter.ID, AppendCharters: []Charter{charter}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for stage, bundle := range map[PromptStage]PRContextBundle{
		PromptReviewSearch: reviewContextBundle(seeded.Aggregate),
		PromptRepair:       implementationContextBundle(seeded.Aggregate),
	} {
		if len(bundle.Corrections) != 1 || bundle.Corrections[0].ID != corrected.Corrections[0].ID {
			t.Fatalf("%s corrections = %#v", stage, bundle.Corrections)
		}
		prompt, compileErr := CompilePrompt(stage, bundle, "")
		if compileErr != nil {
			t.Fatal(compileErr)
		}
		if !strings.Contains(prompt.UserPrompt, corrected.Corrections[0].Correction) {
			t.Fatalf("%s prompt omitted workspace correction", stage)
		}
	}
}

func TestAddCorrectionRejectsMissingFindingWithoutMutation(t *testing.T) {
	store := NewMemoryStore()
	input := testCreateInput()
	created, err := store.Create(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceConfig{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.AddCorrection(t.Context(), AddCorrectionRequest{
		WorkspaceID: input.Workspace.ID, ExpectedVersion: created.Aggregate.Workspace.Version,
		RequestID: "request-correct-missing-finding",
		Correction: Correction{
			Kind: CorrectionFactual, Applicability: CorrectionReviewAndImpl,
			TargetType: "finding", TargetID: "pfn_99999999999999999999999999999999",
			OriginalClaim: "missing finding", Correction: "must not persist",
		},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("AddCorrection() error = %v, want invalid", err)
	}
	if result.Workspace.Version != created.Aggregate.Workspace.Version || len(result.Corrections) != 0 {
		t.Fatalf("invalid correction mutated aggregate = %#v", result)
	}
	persisted, err := store.Get(t.Context(), input.Workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Workspace.Version != created.Aggregate.Workspace.Version || len(persisted.Corrections) != 0 {
		t.Fatalf("persisted aggregate changed = %#v", persisted)
	}
}
