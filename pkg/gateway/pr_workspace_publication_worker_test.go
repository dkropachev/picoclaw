package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/prworkspace"
)

type fakePRWorkspacePublicationService struct {
	pages      []prworkspace.Page
	listCalls  []prworkspace.ListFilter
	aggregates map[string]prworkspace.Aggregate
	getErr     map[string]error

	dispatchIssueRequests  []prworkspace.DispatchIssuePublicationRequest
	reconcileIssueRequests []prworkspace.ReconcileIssuePublicationRequest
	dispatchReviewRequests []prworkspace.DispatchPhasePublicationRequest
	dispatchBranchRequests []prworkspace.DispatchPhasePublicationRequest
	reconcilePhaseRequests []prworkspace.ReconcilePhasePublicationRequest

	dispatchIssuePublisher  prworkspace.IssuePublisher
	reconcileIssuePublisher prworkspace.IssuePublisher
	dispatchReviewPublisher prworkspace.ReviewPublisher
	dispatchBranchPublisher prworkspace.BranchPublisher
	reconcileReview         prworkspace.ReviewPublisher
	reconcileBranch         prworkspace.BranchPublisher

	dispatchErr  error
	reconcileErr error
	failErr      error
	failCalls    int
	failRequest  string
}

func (service *fakePRWorkspacePublicationService) FailUnsafeProvider(
	_ context.Context,
	aggregate prworkspace.Aggregate,
	requestID string,
) (prworkspace.Aggregate, error) {
	service.failCalls++
	service.failRequest = requestID
	return aggregate, service.failErr
}

func (service *fakePRWorkspacePublicationService) List(
	_ context.Context,
	filter prworkspace.ListFilter,
) (prworkspace.Page, error) {
	service.listCalls = append(service.listCalls, filter)
	index := len(service.listCalls) - 1
	if index >= len(service.pages) {
		return prworkspace.Page{}, errors.New("unexpected PR workspace list call")
	}
	return service.pages[index], nil
}

func (service *fakePRWorkspacePublicationService) Get(
	_ context.Context,
	workspaceID string,
) (prworkspace.Aggregate, error) {
	if err := service.getErr[workspaceID]; err != nil {
		return prworkspace.Aggregate{}, err
	}
	aggregate, ok := service.aggregates[workspaceID]
	if !ok {
		return prworkspace.Aggregate{}, prworkspace.ErrNotFound
	}
	return aggregate, nil
}

func (service *fakePRWorkspacePublicationService) DispatchIssuePublication(
	_ context.Context,
	publisher prworkspace.IssuePublisher,
	request prworkspace.DispatchIssuePublicationRequest,
) (prworkspace.Aggregate, error) {
	service.dispatchIssuePublisher = publisher
	service.dispatchIssueRequests = append(service.dispatchIssueRequests, request)
	return prworkspace.Aggregate{}, service.dispatchErr
}

func (service *fakePRWorkspacePublicationService) ReconcileIssuePublication(
	_ context.Context,
	publisher prworkspace.IssuePublisher,
	request prworkspace.ReconcileIssuePublicationRequest,
) (prworkspace.Aggregate, error) {
	service.reconcileIssuePublisher = publisher
	service.reconcileIssueRequests = append(service.reconcileIssueRequests, request)
	return prworkspace.Aggregate{}, service.reconcileErr
}

func (service *fakePRWorkspacePublicationService) DispatchReviewPublication(
	_ context.Context,
	publisher prworkspace.ReviewPublisher,
	request prworkspace.DispatchPhasePublicationRequest,
) (prworkspace.Aggregate, error) {
	service.dispatchReviewPublisher = publisher
	service.dispatchReviewRequests = append(service.dispatchReviewRequests, request)
	return prworkspace.Aggregate{}, service.dispatchErr
}

func (service *fakePRWorkspacePublicationService) DispatchBranchPublication(
	_ context.Context,
	publisher prworkspace.BranchPublisher,
	request prworkspace.DispatchPhasePublicationRequest,
) (prworkspace.Aggregate, error) {
	service.dispatchBranchPublisher = publisher
	service.dispatchBranchRequests = append(service.dispatchBranchRequests, request)
	return prworkspace.Aggregate{}, service.dispatchErr
}

func (service *fakePRWorkspacePublicationService) ReconcilePhasePublication(
	_ context.Context,
	review prworkspace.ReviewPublisher,
	branch prworkspace.BranchPublisher,
	request prworkspace.ReconcilePhasePublicationRequest,
) (prworkspace.Aggregate, error) {
	service.reconcileReview = review
	service.reconcileBranch = branch
	service.reconcilePhaseRequests = append(service.reconcilePhaseRequests, request)
	return prworkspace.Aggregate{}, service.reconcileErr
}

type publicationWorkerIssuePublisher struct{}

func (publicationWorkerIssuePublisher) CreateIssue(
	context.Context,
	prworkspace.IssuePublicationRequest,
) (prworkspace.IssuePublicationResult, error) {
	return prworkspace.IssuePublicationResult{}, nil
}

func (publicationWorkerIssuePublisher) FindIssueByMarker(
	context.Context,
	string,
	string,
	string,
	string,
) (prworkspace.IssuePublicationResult, bool, error) {
	return prworkspace.IssuePublicationResult{}, false, nil
}

type publicationWorkerReviewPublisher struct{}

func (publicationWorkerReviewPublisher) PublishReview(
	context.Context,
	prworkspace.ReviewPublicationRequest,
) (prworkspace.ReviewPublicationResult, error) {
	return prworkspace.ReviewPublicationResult{}, nil
}

func (publicationWorkerReviewPublisher) ReconcileReview(
	context.Context,
	prworkspace.ReviewPublicationRequest,
) (prworkspace.ReviewPublicationResult, bool, error) {
	return prworkspace.ReviewPublicationResult{}, false, nil
}

type publicationWorkerBranchPublisher struct{}

func (publicationWorkerBranchPublisher) PublishBranch(
	context.Context,
	prworkspace.BranchPublicationRequest,
) (prworkspace.BranchPublicationResult, error) {
	return prworkspace.BranchPublicationResult{}, nil
}

func (publicationWorkerBranchPublisher) ReconcileBranch(
	context.Context,
	prworkspace.BranchPublicationRequest,
) (prworkspace.BranchPublicationResult, bool, error) {
	return prworkspace.BranchPublicationResult{}, false, nil
}

func TestPRWorkspacePublicationWorkerPagesAndPrioritizesInterruptedWork(t *testing.T) {
	now := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	queuedWorkspace := "devw_11111111111111111111111111111111"
	runningWorkspace := "devw_22222222222222222222222222222222"
	queued := publicationWorkerPublication(
		"ppb_11111111111111111111111111111111",
		prworkspace.PublicationGitHubIssue,
		prworkspace.ExecutionQueued,
		now.Add(-time.Hour),
	)
	running := publicationWorkerPublication(
		"ppb_22222222222222222222222222222222",
		prworkspace.PublicationGitHubReview,
		prworkspace.ExecutionRunning,
		now.Add(-prWorkspacePublicationRecoveryDelay),
	)
	running.Attempts = 1
	service := &fakePRWorkspacePublicationService{
		pages: []prworkspace.Page{
			{
				Workspaces: []prworkspace.Workspace{{ID: queuedWorkspace}},
				Next:       &prworkspace.WorkspaceCursor{UpdatedAt: now.Add(-time.Minute), ID: queuedWorkspace},
			},
			{Workspaces: []prworkspace.Workspace{{ID: runningWorkspace}}},
		},
		aggregates: map[string]prworkspace.Aggregate{
			queuedWorkspace:  publicationWorkerAggregate(queuedWorkspace, 4, queued),
			runningWorkspace: publicationWorkerAggregate(runningWorkspace, 7, running),
		},
	}
	issue := publicationWorkerIssuePublisher{}
	review := publicationWorkerReviewPublisher{}
	worker := &prWorkspacePublicationWorker{
		service: service,
		issue:   issue,
		review:  review,
		now:     func() time.Time { return now },
	}

	processed, err := worker.ProcessOne(context.Background())
	if err != nil {
		t.Fatalf("ProcessOne() error = %v", err)
	}
	if !processed {
		t.Fatal("ProcessOne() processed = false, want true")
	}
	if len(service.listCalls) != 2 {
		t.Fatalf("List() calls = %d, want 2", len(service.listCalls))
	}
	if got := service.listCalls[1]; !got.AfterUpdated.Equal(now.Add(-time.Minute)) || got.AfterID != queuedWorkspace {
		t.Fatalf("second List() cursor = %#v, want first page cursor", got)
	}
	if len(service.reconcilePhaseRequests) != 1 {
		t.Fatalf("ReconcilePhasePublication() calls = %d, want 1", len(service.reconcilePhaseRequests))
	}
	request := service.reconcilePhaseRequests[0]
	if request.WorkspaceID != runningWorkspace || request.PublicationID != running.ID || request.ExpectedVersion != 7 {
		t.Fatalf("reconciliation request = %#v", request)
	}
	if request.RequestID != "pr-workspace-publication:reconcile:"+running.ID+":1" {
		t.Fatalf("reconciliation request ID = %q", request.RequestID)
	}
	if len(service.dispatchIssueRequests) != 0 {
		t.Fatalf("queued publication dispatched before interrupted work: %#v", service.dispatchIssueRequests)
	}
	if service.reconcileReview != review || service.reconcileBranch != nil {
		t.Fatal("phase reconcilers were not passed through exactly")
	}
}

func TestPRWorkspacePublicationWorkerSkipsUnknownAndRecentRunning(t *testing.T) {
	now := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	workspaceID := "devw_33333333333333333333333333333333"
	unknown := publicationWorkerPublication(
		"ppb_33333333333333333333333333333333",
		prworkspace.PublicationGitHubIssue,
		prworkspace.ExecutionUnknown,
		now.Add(-24*time.Hour),
	)
	recent := publicationWorkerPublication(
		"ppb_44444444444444444444444444444444",
		prworkspace.PublicationGitHubIssue,
		prworkspace.ExecutionRunning,
		now.Add(-prWorkspacePublicationRecoveryDelay+time.Second),
	)
	service := publicationWorkerServiceWithPublications(workspaceID, unknown, recent)
	worker := &prWorkspacePublicationWorker{
		service: service,
		issue:   publicationWorkerIssuePublisher{},
		now:     func() time.Time { return now },
	}

	processed, err := worker.ProcessOne(context.Background())
	if err != nil {
		t.Fatalf("ProcessOne() error = %v", err)
	}
	if processed {
		t.Fatal("ProcessOne() processed human-gated unknown or recent running publication")
	}
	if len(service.reconcileIssueRequests) != 0 || len(service.dispatchIssueRequests) != 0 {
		t.Fatal("ProcessOne() invoked a publisher for ineligible work")
	}
}

func TestPRWorkspacePublicationWorkerDispatchesEachSupportedKind(t *testing.T) {
	now := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		kind      prworkspace.PublicationKind
		configure func(*prWorkspacePublicationWorker)
		assert    func(*testing.T, *fakePRWorkspacePublicationService, any)
	}{
		{
			name: "issue", kind: prworkspace.PublicationGitHubIssue,
			configure: func(worker *prWorkspacePublicationWorker) { worker.issue = publicationWorkerIssuePublisher{} },
			assert: func(t *testing.T, service *fakePRWorkspacePublicationService, publisher any) {
				t.Helper()
				if len(service.dispatchIssueRequests) != 1 || service.dispatchIssuePublisher != publisher {
					t.Fatalf(
						"issue dispatch = %#v, publisher = %#v",
						service.dispatchIssueRequests,
						service.dispatchIssuePublisher,
					)
				}
			},
		},
		{
			name:      "review",
			kind:      prworkspace.PublicationGitHubReview,
			configure: func(worker *prWorkspacePublicationWorker) { worker.review = publicationWorkerReviewPublisher{} },
			assert: func(t *testing.T, service *fakePRWorkspacePublicationService, publisher any) {
				t.Helper()
				if len(service.dispatchReviewRequests) != 1 || service.dispatchReviewPublisher != publisher {
					t.Fatalf(
						"review dispatch = %#v, publisher = %#v",
						service.dispatchReviewRequests,
						service.dispatchReviewPublisher,
					)
				}
			},
		},
		{
			name:      "branch",
			kind:      prworkspace.PublicationBranchPush,
			configure: func(worker *prWorkspacePublicationWorker) { worker.branch = publicationWorkerBranchPublisher{} },
			assert: func(t *testing.T, service *fakePRWorkspacePublicationService, publisher any) {
				t.Helper()
				if len(service.dispatchBranchRequests) != 1 || service.dispatchBranchPublisher != publisher {
					t.Fatalf(
						"branch dispatch = %#v, publisher = %#v",
						service.dispatchBranchRequests,
						service.dispatchBranchPublisher,
					)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspaceID := "devw_55555555555555555555555555555555"
			publication := publicationWorkerPublication(
				"ppb_55555555555555555555555555555555",
				test.kind,
				prworkspace.ExecutionQueued,
				now.Add(-time.Minute),
			)
			publication.Attempts = 2
			service := publicationWorkerServiceWithPublications(workspaceID, publication)
			worker := &prWorkspacePublicationWorker{service: service, now: func() time.Time { return now }}
			test.configure(worker)
			var publisher any
			switch test.kind {
			case prworkspace.PublicationGitHubIssue:
				publisher = worker.issue
			case prworkspace.PublicationGitHubReview:
				publisher = worker.review
			case prworkspace.PublicationBranchPush:
				publisher = worker.branch
			}

			processed, err := worker.ProcessOne(context.Background())
			if err != nil || !processed {
				t.Fatalf("ProcessOne() = (%v, %v), want (true, nil)", processed, err)
			}
			test.assert(t, service, publisher)
			requestID := prWorkspacePublicationRequestID("dispatch", publication)
			if requestID != "pr-workspace-publication:dispatch:"+publication.ID+":3" {
				t.Fatalf("dispatch request ID = %q", requestID)
			}
		})
	}
}

func TestPRWorkspacePublicationWorkerIgnoresUnsupportedKindAndAbsorbsCASRace(t *testing.T) {
	now := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	workspaceID := "devw_66666666666666666666666666666666"
	unsupported := publicationWorkerPublication(
		"ppb_66666666666666666666666666666666",
		prworkspace.PublicationGitHubIssue,
		prworkspace.ExecutionQueued,
		now.Add(-time.Hour),
	)
	supported := publicationWorkerPublication(
		"ppb_77777777777777777777777777777777",
		prworkspace.PublicationGitHubReview,
		prworkspace.ExecutionQueued,
		now,
	)
	service := publicationWorkerServiceWithPublications(workspaceID, unsupported, supported)
	service.dispatchErr = prworkspace.ErrConflict
	worker := &prWorkspacePublicationWorker{
		service: service,
		review:  publicationWorkerReviewPublisher{},
		now:     func() time.Time { return now },
	}

	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = (%v, %v), want (true, nil) for a CAS race", processed, err)
	}
	if len(service.dispatchIssueRequests) != 0 || len(service.dispatchReviewRequests) != 1 {
		t.Fatalf(
			"dispatch calls: issue=%d review=%d",
			len(service.dispatchIssueRequests),
			len(service.dispatchReviewRequests),
		)
	}
}

func TestPRWorkspacePublicationWorkerRejectsRepeatedCursor(t *testing.T) {
	now := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	cursor := &prworkspace.WorkspaceCursor{UpdatedAt: now, ID: "devw_88888888888888888888888888888888"}
	service := &fakePRWorkspacePublicationService{
		pages: []prworkspace.Page{
			{Next: cursor},
			{Next: cursor},
		},
		aggregates: map[string]prworkspace.Aggregate{},
	}
	worker := &prWorkspacePublicationWorker{
		service: service,
		issue:   publicationWorkerIssuePublisher{},
		now:     func() time.Time { return now },
	}

	processed, err := worker.ProcessOne(context.Background())
	if err == nil || processed {
		t.Fatalf("ProcessOne() = (%v, %v), want repeated cursor error", processed, err)
	}
}

func publicationWorkerServiceWithPublications(
	workspaceID string,
	publications ...prworkspace.Publication,
) *fakePRWorkspacePublicationService {
	return &fakePRWorkspacePublicationService{
		pages: []prworkspace.Page{{Workspaces: []prworkspace.Workspace{{ID: workspaceID}}}},
		aggregates: map[string]prworkspace.Aggregate{
			workspaceID: publicationWorkerAggregate(workspaceID, 9, publications...),
		},
	}
}

func publicationWorkerAggregate(
	workspaceID string,
	version int64,
	publications ...prworkspace.Publication,
) prworkspace.Aggregate {
	return prworkspace.Aggregate{
		Workspace:    prworkspace.Workspace{ID: workspaceID, Version: version},
		Publications: append([]prworkspace.Publication(nil), publications...),
	}
}

func publicationWorkerPublication(
	id string,
	kind prworkspace.PublicationKind,
	state prworkspace.ExecutionState,
	timestamp time.Time,
) prworkspace.Publication {
	return prworkspace.Publication{
		ID: id, Kind: kind, State: state,
		CreatedAt: timestamp, UpdatedAt: timestamp,
	}
}
