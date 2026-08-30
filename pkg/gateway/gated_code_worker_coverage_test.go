package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/prworkspace"
)

func TestGatedCodeDevelopmentWorkerHandlesAdmissionRacesAndFailures(t *testing.T) {
	now := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name          string
		admissionErr  error
		wantProcessed bool
		wantErr       bool
	}{
		{name: "conflict is absorbed", admissionErr: prworkspace.ErrConflict, wantProcessed: true},
		{name: "not found is absorbed", admissionErr: prworkspace.ErrNotFound, wantProcessed: true},
		{name: "runtime failure is returned", admissionErr: errors.New("runtime audit failed"), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspaceID := "devw_11111111111111111111111111111111"
			aggregate := developmentWorkerAggregate(
				workspaceID,
				prworkspace.PhasePlanning,
				prworkspace.ExecutionQueued,
				7,
				now,
			)
			aggregate.Workspace.Intent = prworkspace.IntentImplementFeature
			aggregate.Workspace.SourceKind = prworkspace.SourceBrief
			service := &fakeDevelopmentWorkspaceService{
				pages:      []prworkspace.Page{{Workspaces: []prworkspace.Workspace{{ID: workspaceID}}}},
				aggregates: map[string]prworkspace.Aggregate{workspaceID: aggregate},
			}
			advancer := &fakeDevelopmentWorkspaceAdvancer{
				admit: func(value prworkspace.Aggregate) (prworkspace.Aggregate, bool, error) {
					return value, false, test.admissionErr
				},
			}
			worker := &developmentWorkspaceWorker{service: service, handler: advancer}
			processed, err := worker.ProcessOne(t.Context())
			if processed != test.wantProcessed || (err != nil) != test.wantErr ||
				len(service.claimRequests) != 0 || len(advancer.advances) != 0 {
				t.Fatalf(
					"processed=%v err=%v claims=%d advances=%d",
					processed,
					err,
					len(service.claimRequests),
					len(advancer.advances),
				)
			}
		})
	}
}

func TestGatedCodeDevelopmentWorkerUsesAdmittedAggregateAndStopsAfterAdmissionCancellation(t *testing.T) {
	now := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	workspaceID := "devw_22222222222222222222222222222222"
	aggregate := developmentWorkerAggregate(
		workspaceID,
		prworkspace.PhasePlanning,
		prworkspace.ExecutionQueued,
		4,
		now,
	)
	service := &fakeDevelopmentWorkspaceService{
		pages:      []prworkspace.Page{{Workspaces: []prworkspace.Workspace{{ID: workspaceID}}}},
		aggregates: map[string]prworkspace.Aggregate{workspaceID: aggregate},
	}
	ctx, cancel := context.WithCancel(context.Background())
	advancer := &fakeDevelopmentWorkspaceAdvancer{
		claim: func(prworkspace.Aggregate) bool { return false },
		admit: func(value prworkspace.Aggregate) (prworkspace.Aggregate, bool, error) {
			value.Workspace.Version = 9
			cancel()
			return value, true, nil
		},
	}
	processed, err := (&developmentWorkspaceWorker{service: service, handler: advancer}).ProcessOne(ctx)
	if processed || !errors.Is(err, context.Canceled) || len(service.claimRequests) != 0 ||
		len(advancer.advances) != 0 {
		t.Fatalf(
			"processed=%v err=%v claims=%d advances=%d",
			processed,
			err,
			len(service.claimRequests),
			len(advancer.advances),
		)
	}
}

func TestGatedCodePublicationWorkerFeatureGuardOutcomes(t *testing.T) {
	now := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	newFixture := func(state prworkspace.ExecutionState) (*fakePRWorkspacePublicationService, prworkspace.Publication) {
		workspaceID := "devw_33333333333333333333333333333333"
		publication := publicationWorkerPublication(
			"ppb_33333333333333333333333333333333",
			prworkspace.PublicationBranchPush,
			state,
			now.Add(-prWorkspacePublicationRecoveryDelay-time.Minute),
		)
		service := publicationWorkerServiceWithPublications(workspaceID, publication)
		aggregate := service.aggregates[workspaceID]
		aggregate.Workspace.Intent = prworkspace.IntentImplementFeature
		aggregate.Workspace.SourceKind = prworkspace.SourceBrief
		service.aggregates[workspaceID] = aggregate
		return service, publication
	}

	t.Run("safe queued work dispatches", func(t *testing.T) {
		service, _ := newFixture(prworkspace.ExecutionQueued)
		worker := &prWorkspacePublicationWorker{
			service: service,
			branch:  publicationWorkerBranchPublisher{},
			guard:   func(context.Context) error { return nil },
			now:     func() time.Time { return now },
		}
		processed, err := worker.ProcessOne(t.Context())
		if err != nil || !processed || len(service.dispatchBranchRequests) != 1 {
			t.Fatalf("processed=%v err=%v dispatch=%d", processed, err, len(service.dispatchBranchRequests))
		}
	})

	t.Run("safe interrupted work reconciles", func(t *testing.T) {
		service, _ := newFixture(prworkspace.ExecutionRunning)
		worker := &prWorkspacePublicationWorker{
			service: service,
			branch:  publicationWorkerBranchPublisher{},
			guard:   func(context.Context) error { return nil },
			now:     func() time.Time { return now },
		}
		processed, err := worker.ProcessOne(t.Context())
		if err != nil || !processed || len(service.reconcilePhaseRequests) != 1 {
			t.Fatalf("processed=%v err=%v reconcile=%d", processed, err, len(service.reconcilePhaseRequests))
		}
	})

	t.Run("transient guard failure prevents dispatch", func(t *testing.T) {
		service, _ := newFixture(prworkspace.ExecutionQueued)
		wantErr := errors.New("runtime audit failed")
		worker := &prWorkspacePublicationWorker{
			service: service,
			branch:  publicationWorkerBranchPublisher{},
			guard:   func(context.Context) error { return wantErr },
			now:     func() time.Time { return now },
		}
		processed, err := worker.ProcessOne(t.Context())
		if !processed || !errors.Is(err, wantErr) || len(service.dispatchBranchRequests) != 0 {
			t.Fatalf("processed=%v err=%v dispatch=%d", processed, err, len(service.dispatchBranchRequests))
		}
	})

	t.Run("unsafe guard absorbs missing aggregate race", func(t *testing.T) {
		service, _ := newFixture(prworkspace.ExecutionQueued)
		workspaceID := "devw_33333333333333333333333333333333"
		service.getErr = map[string]error{workspaceID: prworkspace.ErrNotFound}
		worker := &prWorkspacePublicationWorker{
			service: service,
			branch:  publicationWorkerBranchPublisher{},
			guard:   func(context.Context) error { return prworkspace.ErrUnsafeProvider },
			now:     func() time.Time { return now },
		}
		processed, err := worker.guardBranchPublication(t.Context(), prWorkspacePublicationWork{
			workspaceID: workspaceID,
			intent:      prworkspace.IntentImplementFeature,
			publication: service.aggregates[workspaceID].Publications[0],
		})
		if !processed || err != nil || service.failCalls != 0 {
			t.Fatalf("guarded=%v err=%v fail=%d", processed, err, service.failCalls)
		}
	})

	t.Run("unsafe failure persistence error is returned", func(t *testing.T) {
		service, publication := newFixture(prworkspace.ExecutionQueued)
		service.failErr = errors.New("store unavailable")
		worker := &prWorkspacePublicationWorker{
			service: service,
			branch:  publicationWorkerBranchPublisher{},
			guard:   func(context.Context) error { return prworkspace.ErrUnsafeProvider },
			now:     func() time.Time { return now },
		}
		guarded, err := worker.guardBranchPublication(t.Context(), prWorkspacePublicationWork{
			workspaceID: "devw_33333333333333333333333333333333",
			intent:      prworkspace.IntentImplementFeature,
			publication: publication,
		})
		if !guarded || !errors.Is(err, service.failErr) || service.failCalls != 1 {
			t.Fatalf("guarded=%v err=%v fail=%d", guarded, err, service.failCalls)
		}
	})
}
