package prworkspace

import (
	"context"
	"fmt"
)

// ClaimAutonomousWorkRequest identifies the exact aggregate version a durable
// background worker is about to run. Persisting the running state before an AI
// or repair call makes an interrupted operation discoverable after restart.
type ClaimAutonomousWorkRequest struct {
	WorkspaceID     string
	ExpectedVersion int64
	RequestID       string
}

// ClaimAutonomousWork durably marks one worker-owned lifecycle operation as
// running. The actual stage keeps its own deterministic request ID, so a crash
// after this claim resumes from the same aggregate version instead of creating
// a second logical attempt.
func (service *Service) ClaimAutonomousWork(
	ctx context.Context,
	request ClaimAutonomousWorkRequest,
) (Aggregate, error) {
	if service == nil || !validMutationEnvelope(
		request.WorkspaceID,
		request.ExpectedVersion,
		request.RequestID,
	) {
		return Aggregate{}, ErrInvalid
	}
	aggregate, err := service.store.Get(ctx, request.WorkspaceID)
	if err != nil {
		return Aggregate{}, err
	}
	if aggregate.Workspace.Version != request.ExpectedVersion ||
		!autonomousWorkClaimable(aggregate) {
		return aggregate, ErrConflict
	}

	running := ExecutionRunning
	result, err := service.store.Mutate(ctx, Mutation{
		WorkspaceID:     request.WorkspaceID,
		ExpectedVersion: request.ExpectedVersion,
		RequestID:       request.RequestID,
		Patch: AggregatePatch{
			ExecutionState: &running,
			Activity: []Activity{{
				Kind:      "automation.claimed",
				Actor:     "system",
				Summary:   fmt.Sprintf("Autonomous %s work claimed", aggregate.Workspace.Phase),
				CreatedAt: service.now().UTC(),
			}},
		},
	})
	return result.Aggregate, err
}

func autonomousWorkClaimable(aggregate Aggregate) bool {
	if aggregate.Workspace.ExecutionState == ExecutionRunning {
		return false
	}
	for _, gate := range aggregate.Gates {
		switch gate.State {
		case ExecutionQueued, ExecutionRunning, ExecutionWaitingGate, ExecutionWaitingUser:
			return false
		}
	}
	switch aggregate.Workspace.Phase {
	case PhaseCharter:
		return aggregate.Workspace.ActiveCharterID == "" && len(aggregate.Charters) == 0
	case PhasePlanning, PhaseReview:
		return aggregate.Workspace.ExecutionState == ExecutionQueued
	case PhaseTriage:
		for _, finding := range aggregate.Findings {
			if finding.Disposition == FindingOpen {
				return false
			}
		}
		return true
	case PhaseImplementation:
		return aggregate.Workspace.ExecutionState == ExecutionQueued
	default:
		return false
	}
}
