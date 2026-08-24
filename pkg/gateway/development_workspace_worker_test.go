package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/prworkspace"
)

type fakeDevelopmentWorkspaceService struct {
	pages         []prworkspace.Page
	listCalls     []prworkspace.ListFilter
	aggregates    map[string]prworkspace.Aggregate
	claimRequests []prworkspace.ClaimAutonomousWorkRequest
	claimErr      error
	afterGet      func()
}

func (service *fakeDevelopmentWorkspaceService) List(
	_ context.Context,
	filter prworkspace.ListFilter,
) (prworkspace.Page, error) {
	service.listCalls = append(service.listCalls, filter)
	index := len(service.listCalls) - 1
	if index >= len(service.pages) {
		return prworkspace.Page{}, errors.New("unexpected development workspace list call")
	}
	return service.pages[index], nil
}

func (service *fakeDevelopmentWorkspaceService) Get(
	_ context.Context,
	workspaceID string,
) (prworkspace.Aggregate, error) {
	aggregate, ok := service.aggregates[workspaceID]
	if service.afterGet != nil {
		service.afterGet()
	}
	if !ok {
		return prworkspace.Aggregate{}, prworkspace.ErrNotFound
	}
	return aggregate, nil
}

func (service *fakeDevelopmentWorkspaceService) ClaimAutonomousWork(
	_ context.Context,
	request prworkspace.ClaimAutonomousWorkRequest,
) (prworkspace.Aggregate, error) {
	service.claimRequests = append(service.claimRequests, request)
	if service.claimErr != nil {
		return prworkspace.Aggregate{}, service.claimErr
	}
	aggregate := service.aggregates[request.WorkspaceID]
	aggregate.Workspace.Version++
	aggregate.Workspace.ExecutionState = prworkspace.ExecutionRunning
	service.aggregates[request.WorkspaceID] = aggregate
	return aggregate, nil
}

type fakeDevelopmentWorkspaceAdvancer struct {
	ready        func(prworkspace.Aggregate) bool
	claim        func(prworkspace.Aggregate) bool
	advances     []prworkspace.Aggregate
	requestIDs   []string
	contextValue any
	contextKey   any
	err          error
}

func (advancer *fakeDevelopmentWorkspaceAdvancer) AutonomousDevelopmentWorkspaceReady(
	aggregate prworkspace.Aggregate,
) bool {
	return advancer.ready == nil || advancer.ready(aggregate)
}

func (advancer *fakeDevelopmentWorkspaceAdvancer) AutonomousDevelopmentWorkspaceClaimRequired(
	aggregate prworkspace.Aggregate,
) bool {
	return advancer.claim == nil || advancer.claim(aggregate)
}

func (advancer *fakeDevelopmentWorkspaceAdvancer) AdvanceDevelopmentWorkspace(
	ctx context.Context,
	aggregate prworkspace.Aggregate,
	requestID string,
) (prworkspace.Aggregate, error) {
	advancer.advances = append(advancer.advances, aggregate)
	advancer.requestIDs = append(advancer.requestIDs, requestID)
	if advancer.contextKey != nil {
		advancer.contextValue = ctx.Value(advancer.contextKey)
	}
	return aggregate, advancer.err
}

func TestDevelopmentWorkspaceWorkerPagesAndSelectsOldestRunnableWork(t *testing.T) {
	now := time.Date(2026, time.August, 24, 12, 0, 0, 0, time.UTC)
	newerID := "devw_11111111111111111111111111111111"
	olderID := "devw_22222222222222222222222222222222"
	cursor := &prworkspace.WorkspaceCursor{UpdatedAt: now.Add(-time.Minute), ID: newerID}
	service := &fakeDevelopmentWorkspaceService{
		pages: []prworkspace.Page{
			{Workspaces: []prworkspace.Workspace{{ID: newerID}}, Next: cursor},
			{Workspaces: []prworkspace.Workspace{{ID: olderID}}},
		},
		aggregates: map[string]prworkspace.Aggregate{
			newerID: developmentWorkerAggregate(
				newerID,
				prworkspace.PhasePlanning,
				prworkspace.ExecutionQueued,
				4,
				now,
			),
			olderID: developmentWorkerAggregate(
				olderID,
				prworkspace.PhasePlanning,
				prworkspace.ExecutionQueued,
				7,
				now.Add(-time.Hour),
			),
		},
	}
	type generationKey struct{}
	key := generationKey{}
	advancer := &fakeDevelopmentWorkspaceAdvancer{contextKey: key}
	worker := &developmentWorkspaceWorker{service: service, handler: advancer}

	processed, err := worker.ProcessOne(context.WithValue(context.Background(), key, "generation-7"))
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = (%v, %v), want (true, nil)", processed, err)
	}
	if len(service.listCalls) != 2 ||
		!service.listCalls[1].AfterUpdated.Equal(cursor.UpdatedAt) || service.listCalls[1].AfterID != cursor.ID {
		t.Fatalf("cursor traversal = %#v", service.listCalls)
	}
	if len(service.claimRequests) != 1 || service.claimRequests[0].WorkspaceID != olderID ||
		service.claimRequests[0].ExpectedVersion != 7 {
		t.Fatalf("claim = %#v, want oldest workspace version 7", service.claimRequests)
	}
	if len(advancer.advances) != 1 || advancer.advances[0].Workspace.ID != olderID ||
		advancer.advances[0].Workspace.Version != 8 ||
		advancer.advances[0].Workspace.ExecutionState != prworkspace.ExecutionRunning {
		t.Fatalf("advance = %#v", advancer.advances)
	}
	if want := "devauto:" + olderID + ":8"; advancer.requestIDs[0] != want {
		t.Fatalf("request ID = %q, want %q", advancer.requestIDs[0], want)
	}
	if advancer.contextValue != "generation-7" {
		t.Fatalf("runtime generation context = %#v", advancer.contextValue)
	}
}

func TestDevelopmentWorkspaceWorkerPrioritizesInterruptedWork(t *testing.T) {
	now := time.Date(2026, time.August, 24, 12, 0, 0, 0, time.UTC)
	queuedID := "devw_33333333333333333333333333333333"
	runningID := "devw_44444444444444444444444444444444"
	service := &fakeDevelopmentWorkspaceService{
		pages: []prworkspace.Page{{Workspaces: []prworkspace.Workspace{{ID: queuedID}, {ID: runningID}}}},
		aggregates: map[string]prworkspace.Aggregate{
			queuedID: developmentWorkerAggregate(
				queuedID,
				prworkspace.PhasePlanning,
				prworkspace.ExecutionQueued,
				3,
				now.Add(-time.Hour),
			),
			runningID: developmentWorkerAggregate(
				runningID,
				prworkspace.PhaseReview,
				prworkspace.ExecutionRunning,
				9,
				now,
			),
		},
	}
	advancer := &fakeDevelopmentWorkspaceAdvancer{}
	worker := &developmentWorkspaceWorker{service: service, handler: advancer}

	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = (%v, %v), want (true, nil)", processed, err)
	}
	if len(service.claimRequests) != 0 {
		t.Fatalf("recovery was claimed again: %#v", service.claimRequests)
	}
	if len(advancer.advances) != 1 || advancer.advances[0].Workspace.ID != runningID ||
		advancer.requestIDs[0] != "devauto:"+runningID+":9" {
		t.Fatalf("recovery advance = %#v IDs=%#v", advancer.advances, advancer.requestIDs)
	}
}

func TestDevelopmentWorkspaceWorkerStopsOnCancellationBeforeClaim(t *testing.T) {
	now := time.Date(2026, time.August, 24, 12, 0, 0, 0, time.UTC)
	workspaceID := "devw_55555555555555555555555555555555"
	ctx, cancel := context.WithCancel(context.Background())
	service := &fakeDevelopmentWorkspaceService{
		pages: []prworkspace.Page{{Workspaces: []prworkspace.Workspace{{ID: workspaceID}}}},
		aggregates: map[string]prworkspace.Aggregate{
			workspaceID: developmentWorkerAggregate(
				workspaceID,
				prworkspace.PhasePlanning,
				prworkspace.ExecutionQueued,
				2,
				now,
			),
		},
		afterGet: cancel,
	}
	advancer := &fakeDevelopmentWorkspaceAdvancer{}
	worker := &developmentWorkspaceWorker{service: service, handler: advancer}

	processed, err := worker.ProcessOne(ctx)
	if processed || !errors.Is(err, context.Canceled) {
		t.Fatalf("ProcessOne() = (%v, %v), want (false, context.Canceled)", processed, err)
	}
	if len(service.claimRequests) != 0 || len(advancer.advances) != 0 {
		t.Fatalf(
			"canceled work mutated state: claims=%d advances=%d",
			len(service.claimRequests),
			len(advancer.advances),
		)
	}
}

func TestDevelopmentWorkspaceWorkerRejectsRepeatedCursor(t *testing.T) {
	now := time.Date(2026, time.August, 24, 12, 0, 0, 0, time.UTC)
	cursor := &prworkspace.WorkspaceCursor{
		UpdatedAt: now,
		ID:        "devw_66666666666666666666666666666666",
	}
	service := &fakeDevelopmentWorkspaceService{
		pages:      []prworkspace.Page{{Next: cursor}, {Next: cursor}},
		aggregates: map[string]prworkspace.Aggregate{},
	}
	worker := &developmentWorkspaceWorker{service: service, handler: &fakeDevelopmentWorkspaceAdvancer{}}

	processed, err := worker.ProcessOne(context.Background())
	if processed || err == nil {
		t.Fatalf("ProcessOne() = (%v, %v), want repeated cursor error", processed, err)
	}
}

func TestDevelopmentWorkspaceRunnableStopsAtCharterClarification(t *testing.T) {
	aggregate := developmentWorkerAggregate(
		"devw_77777777777777777777777777777777",
		prworkspace.PhaseCharter,
		prworkspace.ExecutionWaitingUser,
		3,
		time.Date(2026, time.August, 24, 12, 0, 0, 0, time.UTC),
	)
	aggregate.Charters = []prworkspace.Charter{
		{ID: "pcr_11111111111111111111111111111111"},
		{
			ID:                    "pcr_22222222222222222222222222222222",
			ClarificationNeeded:   true,
			ClarificationQuestion: "Which compatibility contract should change?",
		},
	}
	if developmentWorkspaceRunnable(aggregate) {
		t.Fatal("latest clarification-needed charter was admitted for automatic confirmation")
	}
}

func developmentWorkerAggregate(
	id string,
	phase prworkspace.Phase,
	state prworkspace.ExecutionState,
	version int64,
	updatedAt time.Time,
) prworkspace.Aggregate {
	return prworkspace.Aggregate{Workspace: prworkspace.Workspace{
		ID: id, Phase: phase, ExecutionState: state, Version: version,
		CreatedAt: updatedAt.Add(-time.Hour), UpdatedAt: updatedAt,
	}}
}
