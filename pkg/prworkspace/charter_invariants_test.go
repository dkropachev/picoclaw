package prworkspace

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestConfirmedCharterCannotBeReplacedBySaveOrAlternateConfirm(t *testing.T) {
	store := NewMemoryStore()
	input := testCreateInput()
	created, err := store.Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	active := charterInvariantRecord(input, "active", 1, now)
	active.Confirmed, active.ConfirmedAt = true, &now
	alternate := charterInvariantRecord(input, "alternate", 2, now)
	reviewPhase := PhaseReview
	seeded, err := store.Mutate(context.Background(), Mutation{
		WorkspaceID: input.Workspace.ID, ExpectedVersion: created.Aggregate.Workspace.Version,
		RequestID: "request-seed-active-and-alternate-charter",
		Patch: AggregatePatch{
			Phase: &reviewPhase, ActiveCharterID: &active.ID,
			AppendCharters: []Charter{active, alternate},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceConfig{Store: store, AI: serviceAI{}, Gates: passingGates{}, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}

	unchanged, err := service.ConfirmCharter(context.Background(), ConfirmCharterRequest{
		WorkspaceID: input.Workspace.ID, CharterID: alternate.ID,
		ExpectedVersion: seeded.Aggregate.Workspace.Version, RequestID: "request-confirm-alternate-charter",
	})
	if !errors.Is(err, ErrConflict) || unchanged.Workspace.ActiveCharterID != active.ID || unchanged.Charters[1].Confirmed {
		t.Fatalf("alternate charter bypassed revision: workspace=%#v charters=%#v err=%v", unchanged.Workspace, unchanged.Charters, err)
	}
	unchanged, err = service.SaveCharter(context.Background(), SaveCharterRequest{
		WorkspaceID: input.Workspace.ID, ExpectedVersion: seeded.Aggregate.Workspace.Version,
		RequestID: "request-save-over-active-charter", Draft: charterInvariantDraft("replacement"),
	})
	if !errors.Is(err, ErrConflict) || len(unchanged.Charters) != 2 || unchanged.Workspace.ActiveCharterID != active.ID {
		t.Fatalf("save bypassed active charter revision: workspace=%#v charters=%#v err=%v", unchanged.Workspace, unchanged.Charters, err)
	}
	unchanged, err = service.DraftCharter(context.Background(), DraftCharterRequest{
		WorkspaceID: input.Workspace.ID, ExpectedVersion: seeded.Aggregate.Workspace.Version,
		RequestID: "request-draft-over-active-charter",
	})
	if !errors.Is(err, ErrConflict) || len(unchanged.Charters) != 2 || unchanged.Workspace.ActiveCharterID != active.ID {
		t.Fatalf("AI draft bypassed active charter revision: workspace=%#v charters=%#v err=%v", unchanged.Workspace, unchanged.Charters, err)
	}
}

func TestOnlyNewestCharterDraftCanBeConfirmed(t *testing.T) {
	store := NewMemoryStore()
	input := testCreateInput()
	created, err := store.Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	first := charterInvariantRecord(input, "first", 1, now)
	latest := charterInvariantRecord(input, "latest", 2, now)
	charterPhase := PhaseCharter
	seeded, err := store.Mutate(context.Background(), Mutation{
		WorkspaceID: input.Workspace.ID, ExpectedVersion: created.Aggregate.Workspace.Version,
		RequestID: "request-seed-two-charter-drafts",
		Patch:     AggregatePatch{Phase: &charterPhase, AppendCharters: []Charter{first, latest}},
	})
	if err != nil {
		t.Fatal(err)
	}
	service, _ := NewService(ServiceConfig{Store: store, Gates: passingGates{}, Now: func() time.Time { return now }})

	rejected, err := service.ConfirmCharter(context.Background(), ConfirmCharterRequest{
		WorkspaceID: input.Workspace.ID, CharterID: first.ID,
		ExpectedVersion: seeded.Aggregate.Workspace.Version, RequestID: "request-confirm-superseded-draft",
	})
	if !errors.Is(err, ErrConflict) || rejected.Workspace.ActiveCharterID != "" {
		t.Fatalf("superseded draft was confirmable: workspace=%#v err=%v", rejected.Workspace, err)
	}
	confirmed, err := service.ConfirmCharter(context.Background(), ConfirmCharterRequest{
		WorkspaceID: input.Workspace.ID, CharterID: latest.ID,
		ExpectedVersion: seeded.Aggregate.Workspace.Version, RequestID: "request-confirm-latest-draft",
	})
	if err != nil || confirmed.Workspace.ActiveCharterID != latest.ID || !confirmed.Charters[1].Confirmed {
		t.Fatalf("latest draft was not confirmed: workspace=%#v charters=%#v err=%v", confirmed.Workspace, confirmed.Charters, err)
	}
}

func TestDelayedCharterGateCannotActivateSupersededDraft(t *testing.T) {
	store := NewMemoryStore()
	input := testCreateInput()
	created, err := store.Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	first := charterInvariantRecord(input, "waiting", 1, now)
	charterPhase := PhaseCharter
	seeded, err := store.Mutate(context.Background(), Mutation{
		WorkspaceID: input.Workspace.ID, ExpectedVersion: created.Aggregate.Workspace.Version,
		RequestID: "request-seed-waiting-charter",
		Patch:     AggregatePatch{Phase: &charterPhase, AppendCharters: []Charter{first}},
	})
	if err != nil {
		t.Fatal(err)
	}
	service, _ := NewService(ServiceConfig{Store: store, Now: func() time.Time { return now }})
	waiting, err := service.ConfirmCharter(context.Background(), ConfirmCharterRequest{
		WorkspaceID: input.Workspace.ID, CharterID: first.ID,
		ExpectedVersion: seeded.Aggregate.Workspace.Version, RequestID: "request-start-waiting-charter-gate",
	})
	if err != nil || len(waiting.Gates) != 1 || waiting.Gates[0].State != ExecutionWaitingUser {
		t.Fatalf("charter gate did not wait: gates=%#v err=%v", waiting.Gates, err)
	}
	duplicate, err := service.ConfirmCharter(context.Background(), ConfirmCharterRequest{
		WorkspaceID: input.Workspace.ID, CharterID: first.ID,
		ExpectedVersion: waiting.Workspace.Version, RequestID: "request-duplicate-waiting-charter-gate",
	})
	if !errors.Is(err, ErrConflict) || len(duplicate.Gates) != 1 {
		t.Fatalf("pending charter confirmation was duplicated: gates=%#v err=%v", duplicate.Gates, err)
	}

	// Simulate an out-of-band/legacy writer appending a newer draft while the
	// human response is delayed. The response-side invariant must still refuse
	// to activate the superseded target.
	latest := charterInvariantRecord(input, "newer", 2, now)
	advanced, err := store.Mutate(context.Background(), Mutation{
		WorkspaceID: input.Workspace.ID, ExpectedVersion: waiting.Workspace.Version,
		RequestID: "request-out-of-band-newer-charter", Patch: AggregatePatch{AppendCharters: []Charter{latest}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := service.RespondGate(context.Background(), RespondGateRequest{
		WorkspaceID: input.Workspace.ID, GateRunID: waiting.Gates[0].ID,
		ExpectedVersion: advanced.Aggregate.Workspace.Version, RequestID: "request-pass-superseded-charter-gate", FieldValues: map[string]any{"action": "approve"},
	})
	if !errors.Is(err, ErrConflict) || rejected.Workspace.ActiveCharterID != "" || rejected.Charters[0].Confirmed {
		t.Fatalf("delayed gate activated superseded charter: workspace=%#v charters=%#v err=%v", rejected.Workspace, rejected.Charters, err)
	}
}

func TestRevisedCharterUsesReconfirmationAndInvalidatesEvidence(t *testing.T) {
	store := NewMemoryStore()
	input := testCreateInput()
	created, err := store.Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	active := charterInvariantRecord(input, "active", 1, now)
	active.Confirmed, active.ConfirmedAt = true, &now
	reviewPhase := PhaseReview
	stage := StageRun{
		ID: stableID("psr_", input.Workspace.ID, "old-charter-stage"), Stage: "review",
		State: ExecutionSucceeded, CharterID: active.ID, HeadSHA: input.Provider.HeadSHA,
		StartedAt: now, FinishedAt: &now,
	}
	publication := Publication{
		ID:   stableID("ppb_", input.Workspace.ID, "old-charter-publication"),
		Kind: PublicationGitHubReview, State: ExecutionQueued, ExpectedHeadSHA: input.Provider.HeadSHA,
		CreatedAt: now, UpdatedAt: now,
	}
	seeded, err := store.Mutate(context.Background(), Mutation{
		WorkspaceID: input.Workspace.ID, ExpectedVersion: created.Aggregate.Workspace.Version,
		RequestID: "request-seed-active-charter-evidence",
		Patch: AggregatePatch{
			Phase: &reviewPhase, ActiveCharterID: &active.ID, AppendCharters: []Charter{active},
			AppendStageRuns: []StageRun{stage}, AppendPublications: []Publication{publication},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	service, _ := NewService(ServiceConfig{Store: store, Gates: passingGates{}, Now: func() time.Time { return now }})
	revised, err := service.ReviseCharter(context.Background(), ReviseCharterRequest{
		SaveCharterRequest: SaveCharterRequest{
			WorkspaceID: input.Workspace.ID, ExpectedVersion: seeded.Aggregate.Workspace.Version,
			RequestID: "request-revise-active-charter", Draft: charterInvariantDraft("revised"),
		},
		ExpectedCharterRevision: active.Revision,
	})
	if err != nil || revised.Workspace.ActiveCharterID != "" || revised.StageRuns[0].State != ExecutionStale ||
		revised.Publications[0].State != ExecutionStale {
		t.Fatalf("revision did not invalidate dependent evidence: workspace=%#v stages=%#v pubs=%#v err=%v", revised.Workspace, revised.StageRuns, revised.Publications, err)
	}
	replacement := revised.Charters[len(revised.Charters)-1]
	confirmed, err := service.ConfirmCharter(context.Background(), ConfirmCharterRequest{
		WorkspaceID: input.Workspace.ID, CharterID: replacement.ID,
		ExpectedVersion: revised.Workspace.Version, RequestID: "request-reconfirm-revised-charter",
	})
	if err != nil || confirmed.Workspace.ActiveCharterID != replacement.ID {
		t.Fatalf("revised charter was not activated: workspace=%#v err=%v", confirmed.Workspace, err)
	}
	gate := confirmed.Gates[len(confirmed.Gates)-1]
	if gate.DecisionPoint != "pr.charter.reconfirm" || !gateCompletedWith(gate, "approve") {
		t.Fatalf("revised charter skipped reconfirmation: %#v", gate)
	}
}

func charterInvariantDraft(goal string) CharterDraftOutput {
	return CharterDraftOutput{
		Type: PRTypeFix, Goal: goal, AcceptanceCriteria: []string{"fix the defect"},
		IncludedAreas: []string{"pkg/retry"}, ExcludedAreas: []string{"unrelated cleanup"},
		NonGoals: []string{"refactoring"},
	}
}

func charterInvariantRecord(input CreateInput, name string, revision int64, now time.Time) Charter {
	draft := charterInvariantDraft(name)
	return Charter{
		ID: stableID("pcr_", input.Workspace.ID, name), Revision: revision,
		Type: draft.Type, Goal: draft.Goal, AcceptanceCriteria: draft.AcceptanceCriteria,
		IncludedAreas: draft.IncludedAreas, ExcludedAreas: draft.ExcludedAreas, NonGoals: draft.NonGoals,
		BaseSHA: input.Provider.BaseSHA, HeadSHA: input.Provider.HeadSHA, CreatedAt: now,
	}
}
