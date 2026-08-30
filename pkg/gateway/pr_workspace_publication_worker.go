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
	List(ctx context.Context, filter prworkspace.ListFilter) (prworkspace.Page, error)
	Get(ctx context.Context, workspaceID string) (prworkspace.Aggregate, error)
	DispatchIssuePublication(
		ctx context.Context,
		publisher prworkspace.IssuePublisher,
		request prworkspace.DispatchIssuePublicationRequest,
	) (prworkspace.Aggregate, error)
	ReconcileIssuePublication(
		ctx context.Context,
		publisher prworkspace.IssuePublisher,
		request prworkspace.ReconcileIssuePublicationRequest,
	) (prworkspace.Aggregate, error)
	DispatchReviewPublication(
		ctx context.Context,
		publisher prworkspace.ReviewPublisher,
		request prworkspace.DispatchPhasePublicationRequest,
	) (prworkspace.Aggregate, error)
	DispatchBranchPublication(
		ctx context.Context,
		publisher prworkspace.BranchPublisher,
		request prworkspace.DispatchPhasePublicationRequest,
	) (prworkspace.Aggregate, error)
	ReconcilePhasePublication(
		ctx context.Context,
		reviewPublisher prworkspace.ReviewPublisher,
		branchPublisher prworkspace.BranchPublisher,
		request prworkspace.ReconcilePhasePublicationRequest,
	) (prworkspace.Aggregate, error)
	FailUnsafeProvider(
		ctx context.Context,
		aggregate prworkspace.Aggregate,
		requestID string,
	) (prworkspace.Aggregate, error)
}

type prWorkspacePublicationWorker struct {
	service prWorkspacePublicationService
	issue   prworkspace.IssuePublisher
	review  prworkspace.ReviewPublisher
	branch  prworkspace.BranchPublisher
	guard   func(context.Context) error
	now     func() time.Time
}

func newPRWorkspacePublicationWorker(
	service *prworkspace.Service,
	issue prworkspace.IssuePublisher,
	review prworkspace.ReviewPublisher,
	branch prworkspace.BranchPublisher,
	guard func(context.Context) error,
) *prWorkspacePublicationWorker {
	if service == nil || issue == nil && review == nil && branch == nil {
		return nil
	}
	return &prWorkspacePublicationWorker{
		service: service,
		issue:   issue,
		review:  review,
		branch:  branch,
		guard:   guard,
		now:     time.Now,
	}
}

type prWorkspacePublicationWork struct {
	workspaceID string
	version     int64
	intent      prworkspace.DevelopmentIntent
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
					intent:      aggregate.Workspace.Intent,
					publication: publication,
				}
				switch publication.State {
				case prworkspace.ExecutionRunning:
					if publication.UpdatedAt.IsZero() ||
						now.Before(publication.UpdatedAt.Add(prWorkspacePublicationRecoveryDelay)) {
						continue
					}
					if interrupted == nil || publicationWorkBefore(candidate, *interrupted) {
						candidateCopy := candidate
						interrupted = &candidateCopy
					}
				case prworkspace.ExecutionQueued:
					if queued == nil || publicationWorkBefore(candidate, *queued) {
						candidateCopy := candidate
						queued = &candidateCopy
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
		if guarded, guardErr := worker.guardBranchPublication(ctx, *interrupted); guarded {
			return true, guardErr
		}
		return worker.reconcile(ctx, *interrupted)
	}
	if queued != nil {
		if guarded, guardErr := worker.guardBranchPublication(ctx, *queued); guarded {
			return true, guardErr
		}
		return worker.dispatch(ctx, *queued)
	}
	return false, nil
}

func (worker *prWorkspacePublicationWorker) guardBranchPublication(
	ctx context.Context,
	work prWorkspacePublicationWork,
) (bool, error) {
	if work.publication.Kind != prworkspace.PublicationBranchPush ||
		work.intent != prworkspace.IntentImplementFeature {
		return false, nil
	}
	if worker.guard == nil {
		return true, errors.New("implement-feature publication guard is unavailable")
	}
	if err := worker.guard(ctx); err != nil {
		if !errors.Is(err, prworkspace.ErrUnsafeProvider) {
			return true, err
		}
		aggregate, getErr := worker.service.Get(ctx, work.workspaceID)
		if getErr != nil {
			return true, publicationWorkerError(getErr)
		}
		_, failErr := worker.service.FailUnsafeProvider(
			ctx,
			aggregate,
			prWorkspacePublicationRequestID("unsafe-provider", work.publication),
		)
		return true, publicationWorkerError(failErr)
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

func (worker *prWorkspacePublicationWorker) dispatch(
	ctx context.Context,
	work prWorkspacePublicationWork,
) (bool, error) {
	requestID := prWorkspacePublicationRequestID("dispatch", work.publication)
	var err error
	switch work.publication.Kind {
	case prworkspace.PublicationGitHubIssue:
		_, err = worker.service.DispatchIssuePublication(ctx, worker.issue, prworkspace.DispatchIssuePublicationRequest{
			WorkspaceID: work.workspaceID, PublicationID: work.publication.ID,
			ExpectedVersion: work.version, RequestID: requestID,
		})
	case prworkspace.PublicationGitHubReview:
		_, err = worker.service.DispatchReviewPublication(
			ctx,
			worker.review,
			prworkspace.DispatchPhasePublicationRequest{
				WorkspaceID: work.workspaceID, PublicationID: work.publication.ID,
				ExpectedVersion: work.version, RequestID: requestID,
			},
		)
	case prworkspace.PublicationBranchPush:
		_, err = worker.service.DispatchBranchPublication(
			ctx,
			worker.branch,
			prworkspace.DispatchPhasePublicationRequest{
				WorkspaceID: work.workspaceID, PublicationID: work.publication.ID,
				ExpectedVersion: work.version, RequestID: requestID,
			},
		)
	default:
		return false, nil
	}
	return true, publicationWorkerError(err)
}

func (worker *prWorkspacePublicationWorker) reconcile(
	ctx context.Context,
	work prWorkspacePublicationWork,
) (bool, error) {
	requestID := prWorkspacePublicationRequestID("reconcile", work.publication)
	var err error
	switch work.publication.Kind {
	case prworkspace.PublicationGitHubIssue:
		_, err = worker.service.ReconcileIssuePublication(
			ctx,
			worker.issue,
			prworkspace.ReconcileIssuePublicationRequest{
				WorkspaceID: work.workspaceID, PublicationID: work.publication.ID,
				ExpectedVersion: work.version, RequestID: requestID,
			},
		)
	case prworkspace.PublicationGitHubReview, prworkspace.PublicationBranchPush:
		_, err = worker.service.ReconcilePhasePublication(
			ctx,
			worker.review,
			worker.branch,
			prworkspace.ReconcilePhasePublicationRequest{
				WorkspaceID: work.workspaceID, PublicationID: work.publication.ID,
				ExpectedVersion: work.version, RequestID: requestID,
			},
		)
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
