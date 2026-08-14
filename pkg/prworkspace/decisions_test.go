package prworkspace

import (
	"context"
	"testing"
	"time"
)

func TestFallbackCharterGateTypedResponseConfirmsCharter(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	input := testCreateInput()
	_, _ = store.Create(context.Background(), input)
	charter := Charter{
		ID: "pcr_11111111111111111111111111111111", Type: PRTypeFix, Goal: "fix",
		BaseSHA: input.Provider.BaseSHA, HeadSHA: input.Provider.HeadSHA, CreatedAt: now,
	}
	phase := PhaseCharter
	seed, _ := store.Mutate(context.Background(), Mutation{
		WorkspaceID: input.Workspace.ID, ExpectedVersion: 1, RequestID: "request-00000040",
		Patch: AggregatePatch{Phase: &phase, AppendCharters: []Charter{charter}},
	})
	service, _ := NewService(ServiceConfig{Store: store, Now: func() time.Time { return now }})
	waiting, err := service.ConfirmCharter(context.Background(), ConfirmCharterRequest{
		WorkspaceID: input.Workspace.ID, CharterID: charter.ID, ExpectedVersion: seed.Aggregate.Workspace.Version, RequestID: "request-00000041",
	})
	if err != nil || len(waiting.Gates) != 1 || waiting.Gates[0].State != ExecutionWaitingUser {
		t.Fatalf("waiting gate = %#v, %v", waiting.Gates, err)
	}
	confirmed, err := service.RespondGate(context.Background(), RespondGateRequest{
		WorkspaceID: input.Workspace.ID, GateRunID: waiting.Gates[0].ID,
		ExpectedVersion: waiting.Workspace.Version, RequestID: "request-00000042", Decision: GatePass,
	})
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Workspace.Phase != PhaseReview || !confirmed.Charters[0].Confirmed {
		t.Fatalf("confirmed = %#v", confirmed)
	}
}

func TestCorrectionPromotionIsRepositoryScoped(t *testing.T) {
	service, aggregate := readyImplementationService(t)
	correction := Correction{
		Kind: CorrectionRepositoryPreference, Applicability: CorrectionReviewAndImpl,
		TargetType: "workspace", TargetID: aggregate.Workspace.ID,
		OriginalClaim: "Use pattern A", Correction: "Use repository pattern B",
	}
	withCorrection, err := service.AddCorrection(context.Background(), AddCorrectionRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-00000043", Correction: correction,
	})
	if err != nil {
		t.Fatal(err)
	}
	promoted, err := service.PromoteCorrection(context.Background(), PromoteCorrectionRequest{
		WorkspaceID: aggregate.Workspace.ID, CorrectionID: withCorrection.Corrections[0].ID,
		ExpectedVersion: withCorrection.Workspace.Version, RequestID: "request-00000044",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(promoted.RepositoryLessons) != 1 || promoted.RepositoryLessons[0].RepositoryID != aggregate.Workspace.RepositoryID {
		t.Fatalf("lessons = %#v", promoted.RepositoryLessons)
	}
}
