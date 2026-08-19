package prworkspace

import (
	"context"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

type multiTurnGateEvaluator struct{}

func (multiTurnGateEvaluator) Start(_ context.Context, request GateRequest) (GateRun, error) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	return GateRun{
		ID:            stableID("pgr_", request.WorkspaceID, request.SubjectDigest, "multi-turn"),
		DecisionPoint: request.DecisionPoint, State: ExecutionWaitingUser,
		PolicyRevision: "sha256:multi-turn", SubjectRevision: request.SubjectDigest,
		Evidence: projectGateEvidence(request.Subject), CreatedAt: now,
		Turns: []GateTurn{{
			StageID: "confirm", Kind: "human", ActorKind: "human", Status: "waiting",
			GateForm: &GateForm{GateRef: "gates.confirm", Prompt: "Confirm the check.", Fields: []gatetypes.GateField{{
				ID: "confirmed", Type: gatetypes.GateFieldBoolean, Label: "Confirmed", Required: true,
			}}},
		}},
	}, nil
}

func (multiTurnGateEvaluator) Respond(
	_ context.Context,
	gate GateRun,
	fieldValues map[string]any,
) (GateRun, error) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	for index := range gate.Turns {
		if gate.Turns[index].Status == "waiting" {
			gate.Turns[index].Status = "answered"
			gate.Turns[index].FieldValues = fieldValues
		}
	}
	if len(gate.Turns) == 1 {
		gate.Turns = append(gate.Turns, GateTurn{
			StageID: "note", Kind: "human", ActorKind: "human", Status: "waiting",
			GateForm: &GateForm{GateRef: "gates.note", Prompt: "Add a note.", Fields: []gatetypes.GateField{{
				ID: "note", Type: gatetypes.GateFieldShortText, Label: "Note", Required: true,
			}}},
		})
		return gate, nil
	}
	gate.State, gate.FinishedAt = ExecutionSucceeded, &now
	gate.Turns = append(gate.Turns, GateTurn{
		StageID: "result", Kind: "workflow", ActorKind: "workflow", Status: "answered",
		FieldValues: map[string]any{"action": "approve"},
	})
	return gate, nil
}

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
		WorkspaceID:     input.Workspace.ID,
		CharterID:       charter.ID,
		ExpectedVersion: seed.Aggregate.Workspace.Version,
		RequestID:       "request-00000041",
	})
	if err != nil || len(waiting.Gates) != 1 || waiting.Gates[0].State != ExecutionWaitingUser {
		t.Fatalf("waiting gate = %#v, %v", waiting.Gates, err)
	}
	confirmed, err := service.RespondGate(context.Background(), RespondGateRequest{
		WorkspaceID:     input.Workspace.ID,
		GateRunID:       waiting.Gates[0].ID,
		ExpectedVersion: waiting.Workspace.Version,
		RequestID:       "request-00000042",
		FieldValues:     map[string]any{"action": "approve"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Workspace.Phase != PhaseReview || !confirmed.Charters[0].Confirmed {
		t.Fatalf("confirmed = %#v", confirmed)
	}
}

func TestPRServicePersistsGenericIntermediateGateTurnsBeforeApplicationAction(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	input := testCreateInput()
	_, _ = store.Create(context.Background(), input)
	charter := Charter{
		ID: "pcr_22222222222222222222222222222222", Type: PRTypeFeature, Goal: "multi-turn gate",
		BaseSHA: input.Provider.BaseSHA, HeadSHA: input.Provider.HeadSHA, CreatedAt: now,
	}
	phase := PhaseCharter
	seed, _ := store.Mutate(context.Background(), Mutation{
		WorkspaceID: input.Workspace.ID, ExpectedVersion: 1, RequestID: "request-seed-multi-turn-gate",
		Patch: AggregatePatch{Phase: &phase, AppendCharters: []Charter{charter}},
	})
	service, _ := NewService(ServiceConfig{
		Store: store, Gates: multiTurnGateEvaluator{}, Now: func() time.Time { return now },
	})
	waiting, err := service.ConfirmCharter(context.Background(), ConfirmCharterRequest{
		WorkspaceID: input.Workspace.ID, CharterID: charter.ID,
		ExpectedVersion: seed.Aggregate.Workspace.Version, RequestID: "request-start-multi-turn-gate",
	})
	if err != nil {
		t.Fatal(err)
	}
	intermediate, err := service.RespondGate(context.Background(), RespondGateRequest{
		WorkspaceID: input.Workspace.ID, GateRunID: waiting.Gates[0].ID,
		ExpectedVersion: waiting.Workspace.Version, RequestID: "request-answer-first-gate-form",
		FieldValues: map[string]any{"confirmed": true},
	})
	if err != nil || len(intermediate.Gates[0].Turns) != 2 ||
		intermediate.Gates[0].Turns[0].FieldValues["confirmed"] != true ||
		intermediate.Gates[0].Turns[1].GateForm == nil ||
		intermediate.Charters[0].Confirmed {
		t.Fatalf("intermediate gate = %#v, error = %v", intermediate.Gates, err)
	}
	completed, err := service.RespondGate(context.Background(), RespondGateRequest{
		WorkspaceID: input.Workspace.ID, GateRunID: intermediate.Gates[0].ID,
		ExpectedVersion: intermediate.Workspace.Version, RequestID: "request-answer-second-gate-form",
		FieldValues: map[string]any{"note": "Both checks passed."},
	})
	if err != nil || len(completed.Gates[0].Turns) != 3 ||
		completed.Gates[0].Turns[1].FieldValues["note"] != "Both checks passed." ||
		gateAction(completed.Gates[0]) != "approve" || !completed.Charters[0].Confirmed {
		t.Fatalf("completed multi-turn gate = %#v, error = %v", completed.Gates, err)
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
	if len(promoted.RepositoryLessons) != 1 ||
		promoted.RepositoryLessons[0].RepositoryID != aggregate.Workspace.RepositoryID {
		t.Fatalf("lessons = %#v", promoted.RepositoryLessons)
	}
}
