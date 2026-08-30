package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/prworkspace"
)

func TestPRWorkspacePublicationWorkerUnsafeGuardPreventsBranchDispatch(t *testing.T) {
	now := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	workspaceID := "devw_99999999999999999999999999999999"
	publication := publicationWorkerPublication(
		"ppb_99999999999999999999999999999999",
		prworkspace.PublicationBranchPush,
		prworkspace.ExecutionQueued,
		now.Add(-time.Minute),
	)
	service := publicationWorkerServiceWithPublications(workspaceID, publication)
	aggregate := service.aggregates[workspaceID]
	aggregate.Workspace.Intent = prworkspace.IntentImplementFeature
	aggregate.Workspace.SourceKind = prworkspace.SourceBrief
	service.aggregates[workspaceID] = aggregate
	guardCalls := 0
	worker := &prWorkspacePublicationWorker{
		service: service,
		branch:  publicationWorkerBranchPublisher{},
		guard: func(context.Context) error {
			guardCalls++
			return prworkspace.ErrUnsafeProvider
		},
		now: func() time.Time { return now },
	}

	processed, err := worker.ProcessOne(t.Context())
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = (%v, %v), want (true, nil)", processed, err)
	}
	if guardCalls != 1 || service.failCalls != 1 || len(service.dispatchBranchRequests) != 0 ||
		len(service.reconcilePhaseRequests) != 0 {
		t.Fatalf(
			"unsafe publication effects = guard=%d fail=%d dispatch=%d reconcile=%d",
			guardCalls,
			service.failCalls,
			len(service.dispatchBranchRequests),
			len(service.reconcilePhaseRequests),
		)
	}
	wantRequestID := "pr-workspace-publication:unsafe-provider:" + publication.ID + ":0"
	if service.failRequest != wantRequestID {
		t.Fatalf("unsafe failure request ID = %q, want %q", service.failRequest, wantRequestID)
	}
}

func TestPRWorkspacePublicationWorkerFeatureBranchFailsClosedWithoutGuard(t *testing.T) {
	now := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	workspaceID := "devw_88888888888888888888888888888888"
	publication := publicationWorkerPublication(
		"ppb_88888888888888888888888888888888",
		prworkspace.PublicationBranchPush,
		prworkspace.ExecutionQueued,
		now.Add(-time.Minute),
	)
	service := publicationWorkerServiceWithPublications(workspaceID, publication)
	aggregate := service.aggregates[workspaceID]
	aggregate.Workspace.Intent = prworkspace.IntentImplementFeature
	aggregate.Workspace.SourceKind = prworkspace.SourceBrief
	service.aggregates[workspaceID] = aggregate
	worker := &prWorkspacePublicationWorker{
		service: service, branch: publicationWorkerBranchPublisher{}, now: func() time.Time { return now },
	}

	processed, err := worker.ProcessOne(t.Context())
	if !processed || err == nil || len(service.dispatchBranchRequests) != 0 || service.failCalls != 0 {
		t.Fatalf(
			"ProcessOne() = processed=%v err=%v dispatch=%d fail=%d",
			processed,
			err,
			len(service.dispatchBranchRequests),
			service.failCalls,
		)
	}
}

func TestPRWorkspacePublicationWorkerPickupBranchDoesNotRequireFeatureGuard(t *testing.T) {
	now := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	workspaceID := "devw_77777777777777777777777777777777"
	publication := publicationWorkerPublication(
		"ppb_77777777777777777777777777777777",
		prworkspace.PublicationBranchPush,
		prworkspace.ExecutionQueued,
		now.Add(-time.Minute),
	)
	service := publicationWorkerServiceWithPublications(workspaceID, publication)
	aggregate := service.aggregates[workspaceID]
	aggregate.Workspace.Intent = prworkspace.IntentPickupPR
	aggregate.Workspace.SourceKind = prworkspace.SourcePullRequest
	service.aggregates[workspaceID] = aggregate
	worker := &prWorkspacePublicationWorker{
		service: service, branch: publicationWorkerBranchPublisher{}, now: func() time.Time { return now },
	}

	processed, err := worker.ProcessOne(t.Context())
	if err != nil || !processed || len(service.dispatchBranchRequests) != 1 {
		t.Fatalf(
			"ProcessOne() = processed=%v err=%v dispatch=%d",
			processed,
			err,
			len(service.dispatchBranchRequests),
		)
	}
}
