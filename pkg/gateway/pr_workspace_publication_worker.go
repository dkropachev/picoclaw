package gateway

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/sipeed/picoclaw/pkg/prworkspace"
)

const (
	prWorkspacePublicationPageSize      = 100
	prWorkspacePublicationRecoveryDelay = 2 * time.Minute
)

// prWorkspacePublicationService is the narrow durable lifecycle surface used
// by the gateway worker. Production supplies *prworkspace.Service; keeping the
// interface here makes worker ordering, paging, and retry identities directly
// testable without weakening the domain service API.
type prWorkspacePublicationService interface {
	List(context.Context, prworkspace.ListFilter) (prworkspace.Page, error)
	Get(context.Context, string) (prworkspace.Aggregate, error)
	DispatchIssuePublication(context.Context, prworkspace.IssuePublisher, prworkspace.DispatchIssuePublicationRequest) (prworkspace.Aggregate, error)
	ReconcileIssuePublication(context.Context, prworkspace.IssuePublisher, prworkspace.ReconcileIssuePublicationRequest) (prworkspace.Aggregate, error)
	DispatchReviewPublication(context.Context, prworkspace.ReviewPublisher, prworkspace.DispatchPhasePublicationRequest) (prworkspace.Aggregate, error)
	DispatchBranchPublication(context.Context, prworkspace.BranchPublisher, prworkspace.DispatchPhasePublicationRequest) (prworkspace.Aggregate, error)
	ReconcilePhasePublication(context.Context, prworkspace.ReviewPublisher, prworkspace.BranchPublisher, prworkspace.ReconcilePhasePublicationRequest) (prworkspace.Aggregate, error)
}

type prWorkspacePublicationWorker struct {
	service prWorkspacePublicationService
	issue   prworkspace.IssuePublisher
	review  prworkspace.ReviewPublisher
	branch  prworkspace.BranchPublisher
	now     func() time.Time
}

func newPRWorkspacePublicationWorker(
	service *prworkspace.Service,
	issue prworkspace.IssuePublisher,
	review prworkspace.ReviewPublisher,
	branch prworkspace.BranchPublisher,
) *prWorkspacePublicationWorker {
	if service == nil || issue == nil && review == nil && branch == nil {
		return nil
	}
	return &prWorkspacePublicationWorker{
		service: service,
		issue:   issue,
		review:  review,
		branch:  branch,
		now:     time.Now,
	}
}

type prWorkspacePublicationWork struct {
	workspaceID string
	version     int64
	publication prworkspace.Publication
}

// ProcessOne finds and advances one durable publication. Interrupted running
// work has priority over newly queued work so a steady stream of new requests
// cannot starve crash recovery. Provider-unknown results are deliberately
// excluded: the domain requires a human reconciliation gate for those.
func (worker *prWorkspacePublicationWorker) ProcessOne(ctx context.Context) (bool, error) {
	if worker == nil || worker.service == nil {
		return false, errors.New("PR workspace publication worker is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now().UTC()
	if worker.now != nil {
		now = worker.now().UTC()
	}

	var queued *prWorkspacePublicationWork
	var interrupted *prWorkspacePublicationWork
	filter := prworkspace.ListFilter{Limit: prWorkspacePublicationPageSize}
	for {
		page, err := worker.service.List(ctx, filter)
		if err != nil {
			return false, err
		}
		for _, workspace := range page.Workspaces {
			aggregate, getErr := worker.service.Get(ctx, workspace.ID)
			if errors.Is(getErr, prworkspace.ErrNotFound) {
				continue
			}
			if getErr != nil {
				return false, getErr
			}
			for _, publication := range aggregate.Publications {
				if !worker.supports(publication.Kind) {
					continue
				}
				candidate := prWorkspacePublicationWork{
					workspaceID: aggregate.Workspace.ID,
					version:     aggregate.Workspace.Version,
					publication: publication,
				}
				switch publication.State {
				case prworkspace.ExecutionRunning:
					if publication.UpdatedAt.IsZero() || now.Before(publication.UpdatedAt.Add(prWorkspacePublicationRecoveryDelay)) {
						continue
					}
					if interrupted == nil || publicationWorkBefore(candidate, *interrupted) {
						copy := candidate
						interrupted = &copy
					}
				case prworkspace.ExecutionQueued:
					if queued == nil || publicationWorkBefore(candidate, *queued) {
						copy := candidate
						queued = &copy
					}
				}
			}
		}
		if page.Next == nil {
			break
		}
		if page.Next.UpdatedAt.Equal(filter.AfterUpdated) && page.Next.ID == filter.AfterID {
			return false, errors.New("PR workspace publication scan returned a repeated cursor")
		}
		filter.AfterUpdated = page.Next.UpdatedAt
		filter.AfterID = page.Next.ID
	}

	if interrupted != nil {
		return worker.reconcile(ctx, *interrupted)
	}
	if queued != nil {
		return worker.dispatch(ctx, *queued)
	}
	return false, nil
}

func (worker *prWorkspacePublicationWorker) supports(kind prworkspace.PublicationKind) bool {
	switch kind {
	case prworkspace.PublicationGitHubIssue:
		return worker.issue != nil
	case prworkspace.PublicationGitHubReview:
		return worker.review != nil
	case prworkspace.PublicationBranchPush:
		return worker.branch != nil
	default:
		return false
	}
}

func publicationWorkBefore(left, right prWorkspacePublicationWork) bool {
	leftTime := left.publication.CreatedAt
	rightTime := right.publication.CreatedAt
	if left.publication.State == prworkspace.ExecutionRunning {
		leftTime = left.publication.UpdatedAt
	}
	if right.publication.State == prworkspace.ExecutionRunning {
		rightTime = right.publication.UpdatedAt
	}
	if !leftTime.Equal(rightTime) {
		return leftTime.Before(rightTime)
	}
	if left.workspaceID != right.workspaceID {
		return left.workspaceID < right.workspaceID
	}
	return left.publication.ID < right.publication.ID
}

func (worker *prWorkspacePublicationWorker) dispatch(ctx context.Context, work prWorkspacePublicationWork) (bool, error) {
	requestID := prWorkspacePublicationRequestID("dispatch", work.publication)
	var err error
	switch work.publication.Kind {
	case prworkspace.PublicationGitHubIssue:
		_, err = worker.service.DispatchIssuePublication(ctx, worker.issue, prworkspace.DispatchIssuePublicationRequest{
			WorkspaceID: work.workspaceID, PublicationID: work.publication.ID,
			ExpectedVersion: work.version, RequestID: requestID,
		})
	case prworkspace.PublicationGitHubReview:
		_, err = worker.service.DispatchReviewPublication(ctx, worker.review, prworkspace.DispatchPhasePublicationRequest{
			WorkspaceID: work.workspaceID, PublicationID: work.publication.ID,
			ExpectedVersion: work.version, RequestID: requestID,
		})
	case prworkspace.PublicationBranchPush:
		_, err = worker.service.DispatchBranchPublication(ctx, worker.branch, prworkspace.DispatchPhasePublicationRequest{
			WorkspaceID: work.workspaceID, PublicationID: work.publication.ID,
			ExpectedVersion: work.version, RequestID: requestID,
		})
	default:
		return false, nil
	}
	return true, publicationWorkerError(err)
}

func (worker *prWorkspacePublicationWorker) reconcile(ctx context.Context, work prWorkspacePublicationWork) (bool, error) {
	requestID := prWorkspacePublicationRequestID("reconcile", work.publication)
	var err error
	switch work.publication.Kind {
	case prworkspace.PublicationGitHubIssue:
		_, err = worker.service.ReconcileIssuePublication(ctx, worker.issue, prworkspace.ReconcileIssuePublicationRequest{
			WorkspaceID: work.workspaceID, PublicationID: work.publication.ID,
			ExpectedVersion: work.version, RequestID: requestID,
		})
	case prworkspace.PublicationGitHubReview, prworkspace.PublicationBranchPush:
		_, err = worker.service.ReconcilePhasePublication(ctx, worker.review, worker.branch, prworkspace.ReconcilePhasePublicationRequest{
			WorkspaceID: work.workspaceID, PublicationID: work.publication.ID,
			ExpectedVersion: work.version, RequestID: requestID,
		})
	default:
		return false, nil
	}
	return true, publicationWorkerError(err)
}

func publicationWorkerError(err error) error {
	if errors.Is(err, prworkspace.ErrConflict) || errors.Is(err, prworkspace.ErrNotFound) {
		// A user action or another worker won the aggregate CAS. Rescan promptly
		// rather than reporting a production fault and entering the error delay.
		return nil
	}
	return err
}

func prWorkspacePublicationRequestID(action string, publication prworkspace.Publication) string {
	attempt := publication.Attempts
	if action == "dispatch" {
		attempt++
	}
	return fmt.Sprintf(
		"pr-workspace-publication:%s:%s:%s",
		action,
		publication.ID,
		strconv.Itoa(attempt),
	)
}

var _ prWorkspacePublicationService = (*prworkspace.Service)(nil)
