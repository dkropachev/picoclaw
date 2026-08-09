package prdevelopment

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

type controllerHeartbeatStoreFake struct {
	mu               sync.Mutex
	orchestration    []eventing.PRDevelopmentRepairOrchestrationRenew
	controllers      []eventing.PRDevelopmentControllerRenew
	orchestrationErr error
	controllerErr    error
}

func (store *controllerHeartbeatStoreFake) RenewPRDevelopmentRepairOrchestration(
	_ context.Context,
	input eventing.PRDevelopmentRepairOrchestrationRenew,
) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.orchestration = append(store.orchestration, input)
	return store.orchestrationErr
}

func (store *controllerHeartbeatStoreFake) RenewPRDevelopmentControllerLease(
	_ context.Context,
	input eventing.PRDevelopmentControllerRenew,
) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.controllers = append(store.controllers, input)
	return store.controllerErr
}

func TestControllerHeartbeatRenewsClaimThenMutationLease(t *testing.T) {
	t.Parallel()
	store := &controllerHeartbeatStoreFake{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workCtx, heartbeat := startControllerHeartbeat(
		ctx,
		store,
		"pdr_0123456789abcdef0123456789abcdef",
		"claim-token",
		3*time.Second,
	)
	heartbeat.SetController(eventing.PRDevelopmentController{
		ID:         "pctl_0123456789abcdef0123456789abcdef",
		LeaseToken: "controller-token",
		LeaseEpoch: 7,
	})
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		store.mu.Lock()
		claimCount := len(store.orchestration)
		controllerCount := len(store.controllers)
		store.mu.Unlock()
		if claimCount > 0 && controllerCount > 0 {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("heartbeat did not renew both leases")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := heartbeat.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if workCtx.Err() == nil {
		t.Fatal("work context remains live after Stop")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if got := store.orchestration[0]; got.AttemptID != "pdr_0123456789abcdef0123456789abcdef" ||
		got.ClaimToken != "claim-token" || got.Lease != 3*time.Second {
		t.Fatalf("orchestration renewal = %#v", got)
	}
	if got := store.controllers[0]; got.ControllerID != "pctl_0123456789abcdef0123456789abcdef" ||
		got.AttemptID != "pdr_0123456789abcdef0123456789abcdef" ||
		got.LeaseToken != "controller-token" || got.LeaseEpoch != 7 ||
		got.Lease != 3*time.Second {
		t.Fatalf("controller renewal = %#v", got)
	}
}

func TestControllerHeartbeatCancelsWorkAfterLeaseLoss(t *testing.T) {
	t.Parallel()
	lost := errors.New("lease lost")
	store := &controllerHeartbeatStoreFake{controllerErr: lost}
	workCtx, heartbeat := startControllerHeartbeat(
		context.Background(),
		store,
		"pdr_0123456789abcdef0123456789abcdef",
		"claim-token",
		3*time.Second,
	)
	heartbeat.SetController(eventing.PRDevelopmentController{
		ID:         "pctl_0123456789abcdef0123456789abcdef",
		LeaseToken: "controller-token",
		LeaseEpoch: 1,
	})
	select {
	case <-workCtx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("work context was not canceled after controller lease loss")
	}
	if err := heartbeat.Stop(); !errors.Is(err, lost) {
		t.Fatalf("Stop() error = %v, want %v", err, lost)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.orchestration) != 1 || len(store.controllers) != 1 {
		t.Fatalf("renewal counts = orchestration %d, controller %d", len(store.orchestration), len(store.controllers))
	}
}

func TestControllerHeartbeatDoesNotRenewControllerBeforeAcquisition(t *testing.T) {
	t.Parallel()
	store := &controllerHeartbeatStoreFake{}
	ctx, cancel := context.WithCancel(context.Background())
	workCtx, heartbeat := startControllerHeartbeat(
		ctx,
		store,
		"pdr_0123456789abcdef0123456789abcdef",
		"claim-token",
		3*time.Second,
	)
	select {
	case <-time.After(1200 * time.Millisecond):
	case <-workCtx.Done():
		t.Fatalf("work context ended early: %v", workCtx.Err())
	}
	cancel()
	if err := heartbeat.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.orchestration) == 0 || len(store.controllers) != 0 {
		t.Fatalf("renewal counts = orchestration %d, controller %d", len(store.orchestration), len(store.controllers))
	}
}

func TestControllerHeartbeatStopsControllerRenewalAfterClear(t *testing.T) {
	t.Parallel()
	store := &controllerHeartbeatStoreFake{}
	_, heartbeat := startControllerHeartbeat(
		context.Background(),
		store,
		"pdr_0123456789abcdef0123456789abcdef",
		"claim-token",
		3*time.Second,
	)
	heartbeat.SetController(eventing.PRDevelopmentController{
		ID:         "pctl_0123456789abcdef0123456789abcdef",
		LeaseToken: "controller-token",
		LeaseEpoch: 1,
	})
	heartbeat.ClearController()
	if err := heartbeat.renew(context.Background()); err != nil {
		t.Fatalf("renew() error = %v", err)
	}
	if err := heartbeat.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.orchestration) == 0 || len(store.controllers) != 0 {
		t.Fatalf("renewal counts = orchestration %d, controller %d", len(store.orchestration), len(store.controllers))
	}
}
