package prworkspace

import (
	"context"
	"time"

	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

type testAllWaitingGates struct{}

func (testAllWaitingGates) Start(_ context.Context, request GateRequest) (GateRun, error) {
	return testWaitingGate(request), nil
}

func (testAllWaitingGates) Respond(
	_ context.Context,
	gate GateRun,
	fieldValues map[string]any,
) (GateRun, error) {
	return answerTestGate(gate, fieldValues), nil
}

func testSucceededGate(request GateRequest) GateRun {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	return GateRun{
		ID:            stableID("pgr_", request.WorkspaceID, request.DecisionPoint, request.SubjectDigest),
		DecisionPoint: request.DecisionPoint, State: ExecutionSucceeded,
		PolicyRevision: "sha256:test-gate-v3", SubjectRevision: request.SubjectDigest,
		Evidence: projectGateEvidence(request.Subject), CreatedAt: now, FinishedAt: &now,
		Turns: []GateTurn{{
			StageID: "gate", Kind: "deterministic", ActorKind: "deterministic", Status: "answered",
			FieldValues: map[string]any{"action": gateProgressAction(request.DecisionPoint)},
		}},
	}
}

func testWaitingGate(request GateRequest) GateRun {
	return testWaitingGateWithActions(request,
		"approve", "continue", "accept", "authorize", "keep-in-pr", "publish", "promote", "recheck-provider",
		"revise", "defer-follow-up", "dismiss", "revise-charter", "stop", "decline", "assume-failed",
	)
}

func testWaitingGateWithActions(request GateRequest, options ...string) GateRun {
	gate := testSucceededGate(request)
	gate.State, gate.FinishedAt = ExecutionWaitingUser, nil
	fieldOptions := make([]gatetypes.GateFieldOption, 0, len(options))
	for _, option := range options {
		fieldOptions = append(fieldOptions, gatetypes.GateFieldOption{ID: option, Label: option})
	}
	gate.Turns = []GateTurn{{
		StageID: "gate", Kind: "human", ActorKind: "human", Status: "waiting",
		GateForm: &GateForm{
			GateRef: "gates.test", Prompt: "Choose an action.",
			Fields: []gatetypes.GateField{{
				ID: "action", Type: gatetypes.GateFieldSelect, Label: "Action",
				MinSelections: 1, MaxSelections: 1, Options: fieldOptions,
			}},
		},
	}}
	return gate
}

func answerTestGate(gate GateRun, fieldValues map[string]any) GateRun {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	gate.State, gate.FinishedAt = ExecutionSucceeded, &now
	for index := range gate.Turns {
		if gate.Turns[index].Status == "waiting" {
			gate.Turns[index].Status = "answered"
			gate.Turns[index].FieldValues = fieldValues
			break
		}
	}
	return gate
}
