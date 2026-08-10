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
	mu                   sync.Mutex
	orchestration        []eventing.PRDevelopmentRepairOrchestrationRenew
	controllers          []eventing.PRDevelopmentControllerRenew
	resumes              []eventing.PRDevelopmentControllerSuspendedResumeRenew
	orchestrationErr     error
	controllerErr        error
	resumeErr            error
	orchestrationEntered chan<- struct{}
	orchestrationRelease <-chan struct{}
	controllerEntered    chan<- struct{}
	controllerRelease    <-chan struct{}
	resumeEntered        chan<- struct{}
	resumeRelease        <-chan struct{}
}

func (store *controllerHeartbeatStoreFake) RenewPRDevelopmentRepairOrchestration(
	_ context.Context,
	input eventing.PRDevelopmentRepairOrchestrationRenew,
) error {
	store.mu.Lock()
	store.orchestration = append(store.orchestration, input)
	err := store.orchestrationErr
	entered := store.orchestrationEntered
	release := store.orchestrationRelease
	store.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	if release != nil {
		<-release
	}
	return err
}

func (store *controllerHeartbeatStoreFake) RenewPRDevelopmentControllerLease(
	_ context.Context,
	input eventing.PRDevelopmentControllerRenew,
) error {
	store.mu.Lock()
	store.controllers = append(store.controllers, input)
	err := store.controllerErr
	entered := store.controllerEntered
	release := store.controllerRelease
	store.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	if release != nil {
		<-release
	}
	return err
}

func (store *controllerHeartbeatStoreFake) RenewPRDevelopmentControllerSuspendedResume(
	_ context.Context,
	input eventing.PRDevelopmentControllerSuspendedResumeRenew,
) error {
	store.mu.Lock()
	store.resumes = append(store.resumes, input)
	err := store.resumeErr
	entered := store.resumeEntered
	release := store.resumeRelease
	store.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	if release != nil {
		<-release
	}
	return err
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

func TestControllerHeartbeatTransitionsSuspendedResumeIntoMutationLease(t *testing.T) {
	t.Parallel()
	store := &controllerHeartbeatStoreFake{}
	heartbeat := &controllerHeartbeat{
		store:      store,
		attemptID:  "pdr_0123456789abcdef0123456789abcdef",
		claimToken: "orchestration-token",
		lease:      3 * time.Second,
	}
	heartbeat.SetSuspendedResume(eventing.PRDevelopmentControllerSuspendedResumeLease{
		Suspension: eventing.PRDevelopmentControllerSuspension{
			ID:               "pdsi_11111111111111111111111111111111",
			ControllerID:     "pctl_22222222222222222222222222222222",
			ResumeAttemptID:  "pdr_0123456789abcdef0123456789abcdef",
			ResumeClaimID:    "pdsrc_33333333333333333333333333333333",
			ResumeClaimToken: "resume-token",
			ResumeClaimEpoch: 4,
		},
	})
	if err := heartbeat.renew(context.Background()); err != nil {
		t.Fatalf("resume renew() error = %v", err)
	}
	heartbeat.BeginResumeTransition()
	if err := heartbeat.renew(context.Background()); err != nil {
		t.Fatalf("transition renew() error = %v", err)
	}
	heartbeat.SetController(eventing.PRDevelopmentController{
		ID:         "pctl_22222222222222222222222222222222",
		LeaseToken: "mutation-token",
		LeaseEpoch: 9,
	})
	if err := heartbeat.renew(context.Background()); err != nil {
		t.Fatalf("mutation renew() error = %v", err)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.orchestration) != 3 || len(store.resumes) != 1 || len(store.controllers) != 1 {
		t.Fatalf(
			"renewal counts = orchestration %d, resume %d, controller %d; want 3, 1, 1",
			len(store.orchestration), len(store.resumes), len(store.controllers),
		)
	}
	if got := store.resumes[0]; got.ControllerID != "pctl_22222222222222222222222222222222" ||
		got.AttemptID != "pdr_0123456789abcdef0123456789abcdef" ||
		got.SuspensionID != "pdsi_11111111111111111111111111111111" ||
		got.OrchestrationClaimToken != "orchestration-token" ||
		got.ClaimID != "pdsrc_33333333333333333333333333333333" ||
		got.ClaimToken != "resume-token" || got.ClaimEpoch != 4 ||
		got.Lease != 3*time.Second {
		t.Fatalf("resume renewal = %#v", got)
	}
}

func TestControllerHeartbeatNeverRenewsResumeAfterParentLoss(t *testing.T) {
	t.Parallel()
	lost := errors.New("orchestration claim lost")
	store := &controllerHeartbeatStoreFake{orchestrationErr: lost}
	heartbeat := &controllerHeartbeat{
		store:      store,
		attemptID:  "pdr_0123456789abcdef0123456789abcdef",
		claimToken: "orchestration-token",
		lease:      3 * time.Second,
	}
	heartbeat.SetSuspendedResume(eventing.PRDevelopmentControllerSuspendedResumeLease{
		Suspension: eventing.PRDevelopmentControllerSuspension{
			ID:               "pdsi_11111111111111111111111111111111",
			ControllerID:     "pctl_22222222222222222222222222222222",
			ResumeAttemptID:  "pdr_0123456789abcdef0123456789abcdef",
			ResumeClaimID:    "pdsrc_33333333333333333333333333333333",
			ResumeClaimToken: "resume-token",
			ResumeClaimEpoch: 4,
		},
	})
	err := heartbeat.renew(context.Background())
	if !errors.Is(err, lost) {
		t.Fatalf("renew() error = %v, want parent loss %v", err, lost)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.orchestration) != 1 || len(store.resumes) != 0 {
		t.Fatalf("renewal counts = parent %d, resume %d; child must not outlive parent",
			len(store.orchestration), len(store.resumes))
	}
}

func TestControllerHeartbeatResumeBarrierDrainsOnlyResumeRenewal(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	store := &controllerHeartbeatStoreFake{
		resumeErr:     errors.New("stale resume claim"),
		resumeEntered: entered,
		resumeRelease: release,
	}
	heartbeat := &controllerHeartbeat{
		store:      store,
		attemptID:  "pdr_0123456789abcdef0123456789abcdef",
		claimToken: "orchestration-token",
		lease:      3 * time.Second,
	}
	heartbeat.SetSuspendedResume(eventing.PRDevelopmentControllerSuspendedResumeLease{
		Suspension: eventing.PRDevelopmentControllerSuspension{
			ID:               "pdsi_11111111111111111111111111111111",
			ControllerID:     "pctl_22222222222222222222222222222222",
			ResumeAttemptID:  "pdr_0123456789abcdef0123456789abcdef",
			ResumeClaimID:    "pdsrc_33333333333333333333333333333333",
			ResumeClaimToken: "resume-token",
			ResumeClaimEpoch: 4,
		},
	})
	renewed := make(chan error, 1)
	go func() { renewed <- heartbeat.renew(context.Background()) }()
	requireHeartbeatSignal(t, entered, "resume renewal did not start")
	barrierDone := make(chan struct{})
	go func() {
		heartbeat.BeginResumeTransition()
		close(barrierDone)
	}()
	requireHeartbeatResumeTransition(t, heartbeat)
	select {
	case <-barrierDone:
		t.Fatal("resume barrier returned before renewal drained")
	default:
	}
	close(release)
	requireHeartbeatRenewal(t, renewed)
	requireHeartbeatSignal(t, barrierDone, "resume barrier did not return")
	if err := heartbeat.renew(context.Background()); err != nil {
		t.Fatalf("post-barrier renew() error = %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.orchestration) != 2 || len(store.resumes) != 1 || len(store.controllers) != 0 {
		t.Fatalf(
			"post-barrier counts = orchestration %d, resume %d, controller %d",
			len(store.orchestration), len(store.resumes), len(store.controllers),
		)
	}
}

func TestControllerHeartbeatStopsAllRenewalAfterTerminalBarrier(t *testing.T) {
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
	heartbeat.BeginTerminal()
	if err := heartbeat.renew(context.Background()); err != nil {
		t.Fatalf("renew() error = %v", err)
	}
	if err := heartbeat.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.orchestration) != 0 || len(store.controllers) != 0 {
		t.Fatalf("renewal counts = orchestration %d, controller %d", len(store.orchestration), len(store.controllers))
	}
}

func TestControllerHeartbeatTerminalBarrierDrainsOrchestrationRenewal(t *testing.T) {
	testControllerHeartbeatTerminalBarrierDrainsRenewal(t, false)
}

func TestControllerHeartbeatTerminalBarrierDrainsControllerRenewal(t *testing.T) {
	testControllerHeartbeatTerminalBarrierDrainsRenewal(t, true)
}

func testControllerHeartbeatTerminalBarrierDrainsRenewal(
	t *testing.T,
	controllerRenewal bool,
) {
	t.Helper()
	renewalName := "orchestration"
	wantController := 0
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	store := &controllerHeartbeatStoreFake{}
	if controllerRenewal {
		renewalName = "controller"
		wantController = 1
		store.controllerErr = errors.New("stale controller lease error")
		store.controllerEntered = entered
		store.controllerRelease = release
	} else {
		store.orchestrationErr = errors.New("stale orchestration lease error")
		store.orchestrationEntered = entered
		store.orchestrationRelease = release
	}
	workCtx, heartbeat := startControllerHeartbeat(
		context.Background(),
		store,
		"pdr_0123456789abcdef0123456789abcdef",
		"claim-token",
		3*time.Hour,
	)
	heartbeat.SetController(eventing.PRDevelopmentController{
		ID:         "pctl_0123456789abcdef0123456789abcdef",
		LeaseToken: "controller-token",
		LeaseEpoch: 1,
	})

	renewed := make(chan error, 1)
	go func() { renewed <- heartbeat.renew(workCtx) }()
	requireHeartbeatSignal(t, entered, renewalName+" renewal did not start")
	barrierDone := make(chan struct{})
	go func() {
		heartbeat.BeginTerminal()
		close(barrierDone)
	}()
	requireHeartbeatTerminalIntent(t, heartbeat)
	select {
	case <-barrierDone:
		t.Fatalf("terminal barrier returned before %s renewal drained", renewalName)
	default:
	}
	close(release)
	requireHeartbeatRenewal(t, renewed)
	requireHeartbeatSignal(t, barrierDone, "terminal barrier did not return")
	assertTerminalHeartbeat(t, workCtx, heartbeat, store, 1, wantController)
}

func assertTerminalHeartbeat(
	t *testing.T,
	workCtx context.Context,
	heartbeat *controllerHeartbeat,
	store *controllerHeartbeatStoreFake,
	wantOrchestration, wantController int,
) {
	t.Helper()
	if workCtx.Err() != nil {
		t.Fatalf("terminal barrier canceled work context: %v", workCtx.Err())
	}
	if err := heartbeat.renew(workCtx); err != nil {
		t.Fatalf("post-barrier renew() error = %v", err)
	}
	store.mu.Lock()
	orchestrationCount := len(store.orchestration)
	controllerCount := len(store.controllers)
	store.mu.Unlock()
	if orchestrationCount != wantOrchestration || controllerCount != wantController {
		t.Fatalf(
			"post-barrier renewal counts = orchestration %d, controller %d; want %d, %d",
			orchestrationCount,
			controllerCount,
			wantOrchestration,
			wantController,
		)
	}
	if err := heartbeat.Stop(); err != nil {
		t.Fatalf("Stop() reported stale renewal error = %v", err)
	}
}

func requireHeartbeatRenewal(t *testing.T, renewed <-chan error) {
	t.Helper()
	select {
	case err := <-renewed:
		if err != nil {
			t.Fatalf("renew() reported stale error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("renewal did not finish")
	}
}

func requireHeartbeatSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal(message)
	}
}

func requireHeartbeatTerminalIntent(t *testing.T, heartbeat *controllerHeartbeat) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !heartbeat.terminal.Load() {
		select {
		case <-deadline.C:
			t.Fatal("terminal barrier did not publish its intent")
		case <-ticker.C:
		}
	}
}

func requireHeartbeatResumeTransition(t *testing.T, heartbeat *controllerHeartbeat) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !heartbeat.resumeTransition.Load() {
		select {
		case <-deadline.C:
			t.Fatal("resume barrier did not publish its intent")
		case <-ticker.C:
		}
	}
}
