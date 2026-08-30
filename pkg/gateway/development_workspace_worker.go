package gateway

import (
	"context"
	"errors"
	"fmt"

	"github.com/sipeed/picoclaw/pkg/prworkspace"
)

const developmentWorkspacePageSize = 100

type developmentWorkspaceService interface {
	List(ctx context.Context, filter prworkspace.ListFilter) (prworkspace.Page, error)
	Get(ctx context.Context, workspaceID string) (prworkspace.Aggregate, error)
	ClaimAutonomousWork(
		ctx context.Context,
		request prworkspace.ClaimAutonomousWorkRequest,
	) (prworkspace.Aggregate, error)
}

type developmentWorkspaceAdvancer interface {
	AdmitAutonomousDevelopmentWorkspace(
		ctx context.Context,
		aggregate prworkspace.Aggregate,
		requestID string,
	) (prworkspace.Aggregate, bool, error)
	AdvanceDevelopmentWorkspace(
		ctx context.Context,
		aggregate prworkspace.Aggregate,
		requestID string,
	) (prworkspace.Aggregate, error)
	AutonomousDevelopmentWorkspaceReady(aggregate prworkspace.Aggregate) bool
	AutonomousDevelopmentWorkspaceClaimRequired(aggregate prworkspace.Aggregate) bool
}

type developmentWorkspaceWorker struct {
	service developmentWorkspaceService
	handler developmentWorkspaceAdvancer
}

type developmentWorkspaceWork struct {
	aggregate prworkspace.Aggregate
	recovery  bool
}

// ProcessOne scans every keyset page before selecting work. Interrupted work
// has priority, followed by the oldest runnable aggregate, so a busy first page
// cannot permanently hide older recovery or queued work.
func (worker *developmentWorkspaceWorker) ProcessOne(ctx context.Context) (bool, error) {
	if worker == nil || worker.service == nil || worker.handler == nil {
		return false, errors.New("development workspace worker is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var selected *developmentWorkspaceWork
	filter := prworkspace.ListFilter{Limit: developmentWorkspacePageSize}
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		page, err := worker.service.List(ctx, filter)
		if err != nil {
			return false, err
		}
		for _, summary := range page.Workspaces {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			aggregate, getErr := worker.service.Get(ctx, summary.ID)
			if errors.Is(getErr, prworkspace.ErrNotFound) {
				continue
			}
			if getErr != nil {
				return false, getErr
			}
			if !developmentWorkspaceRunnable(aggregate) ||
				!worker.handler.AutonomousDevelopmentWorkspaceReady(aggregate) {
				continue
			}
			candidate := developmentWorkspaceWork{
				aggregate: aggregate,
				recovery:  aggregate.Workspace.ExecutionState == prworkspace.ExecutionRunning,
			}
			if selected == nil || developmentWorkspaceWorkBefore(candidate, *selected) {
				candidateCopy := candidate
				selected = &candidateCopy
			}
		}
		if page.Next == nil {
			break
		}
		if page.Next.UpdatedAt.Equal(filter.AfterUpdated) && page.Next.ID == filter.AfterID {
			return false, errors.New("development workspace scan returned a repeated cursor")
		}
		filter.AfterUpdated = page.Next.UpdatedAt
		filter.AfterID = page.Next.ID
	}

	if selected == nil {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}

	aggregate := selected.aggregate
	admissionID := fmt.Sprintf(
		"devauto:admit:%s:%d",
		aggregate.Workspace.ID,
		aggregate.Workspace.Version,
	)
	admittedAggregate, admitted, admissionErr := worker.handler.AdmitAutonomousDevelopmentWorkspace(
		ctx,
		aggregate,
		admissionID,
	)
	if errors.Is(admissionErr, prworkspace.ErrConflict) ||
		errors.Is(admissionErr, prworkspace.ErrNotFound) {
		return true, nil
	}
	if admissionErr != nil {
		return false, admissionErr
	}
	if !admitted {
		return true, nil
	}
	aggregate = admittedAggregate
	if !selected.recovery && worker.handler.AutonomousDevelopmentWorkspaceClaimRequired(aggregate) {
		claimed, err := worker.service.ClaimAutonomousWork(ctx, prworkspace.ClaimAutonomousWorkRequest{
			WorkspaceID:     aggregate.Workspace.ID,
			ExpectedVersion: aggregate.Workspace.Version,
			RequestID: fmt.Sprintf(
				"devauto:claim:%s:%d",
				aggregate.Workspace.ID,
				aggregate.Workspace.Version,
			),
		})
		if errors.Is(err, prworkspace.ErrConflict) || errors.Is(err, prworkspace.ErrNotFound) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		aggregate = claimed
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}

	requestID := fmt.Sprintf("devauto:%s:%d", aggregate.Workspace.ID, aggregate.Workspace.Version)
	_, err := worker.handler.AdvanceDevelopmentWorkspace(ctx, aggregate, requestID)
	if errors.Is(err, prworkspace.ErrConflict) || errors.Is(err, prworkspace.ErrNotFound) {
		return true, nil
	}
	return true, err
}

func developmentWorkspaceWorkBefore(left, right developmentWorkspaceWork) bool {
	if left.recovery != right.recovery {
		return left.recovery
	}
	leftTime, rightTime := left.aggregate.Workspace.UpdatedAt, right.aggregate.Workspace.UpdatedAt
	if leftTime.IsZero() {
		leftTime = left.aggregate.Workspace.CreatedAt
	}
	if rightTime.IsZero() {
		rightTime = right.aggregate.Workspace.CreatedAt
	}
	if !leftTime.Equal(rightTime) {
		return leftTime.Before(rightTime)
	}
	return left.aggregate.Workspace.ID < right.aggregate.Workspace.ID
}

func developmentWorkspaceRunnable(aggregate prworkspace.Aggregate) bool {
	for _, gate := range aggregate.Gates {
		if gate.State == prworkspace.ExecutionQueued || gate.State == prworkspace.ExecutionWaitingGate ||
			gate.State == prworkspace.ExecutionWaitingUser || gate.State == prworkspace.ExecutionRunning {
			return false
		}
	}
	switch aggregate.Workspace.Phase {
	case prworkspace.PhaseCharter:
		if aggregate.Workspace.ActiveCharterID != "" {
			return false
		}
		return len(aggregate.Charters) == 0 ||
			!aggregate.Charters[len(aggregate.Charters)-1].ClarificationNeeded
	case prworkspace.PhasePlanning, prworkspace.PhaseReview:
		return aggregate.Workspace.ExecutionState == prworkspace.ExecutionQueued ||
			aggregate.Workspace.ExecutionState == prworkspace.ExecutionRunning
	case prworkspace.PhaseTriage:
		for _, finding := range aggregate.Findings {
			if finding.Disposition == prworkspace.FindingOpen {
				return false
			}
		}
		return true
	case prworkspace.PhaseImplementation:
		return aggregate.Workspace.ExecutionState == prworkspace.ExecutionQueued ||
			aggregate.Workspace.ExecutionState == prworkspace.ExecutionRunning
	case prworkspace.PhasePublication:
		return aggregate.Workspace.ExecutionState == prworkspace.ExecutionWaitingUser ||
			aggregate.Workspace.ExecutionState == prworkspace.ExecutionQueued
	default:
		return false
	}
}
