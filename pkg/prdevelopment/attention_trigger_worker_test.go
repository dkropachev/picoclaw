package prdevelopment

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestPRDevelopmentAttentionTriggerWorkerPinsPolicyThenSubjectBeforeDelivery(
	t *testing.T,
) {
	fixture := newAttentionRuntimeFixture(t)
	launcher := fixture.launcher(t, []workflows.GateSpec{{
		ID: "automatic", Kind: workflows.GateDeterministic,
		When: "false", Title: "Automatic", Questions: []any{"Continue?"},
	}}, nil)
	queue := newPRDevelopmentAttentionTriggerQueueFake(fixture.snapshot)
	launcher.executor.AdmittedRunCreate = func(
		_ context.Context,
		_ *workflows.Run,
		create func() error,
	) error {
		pinned := queue.snapshotTrigger()
		if pinned.PolicyRevision == "" || len(pinned.PinnedPolicy) == 0 ||
			pinned.SubjectRevision == "" {
			return errors.New("run admission preceded immutable trigger pins")
		}
		return create()
	}
	worker := newPRDevelopmentAttentionTriggerWorker(queue, launcher)

	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = (%t, %v)", processed, err)
	}
	stored := queue.snapshotTrigger()
	if stored.Status != eventing.PRDevelopmentAttentionTriggerDelivered ||
		stored.PolicyRevision == "" || len(stored.PinnedPolicy) == 0 ||
		stored.SubjectRevision == "" || stored.RunID == "" ||
		stored.LeaseToken != "" || stored.LeaseUntil != nil ||
		stored.CompletedAt == nil {
		t.Fatalf("delivered trigger = %#v", stored)
	}
	if got := queue.operationsSnapshot(); fmt.Sprint(got) !=
		"[claim snapshot policy snapshot subject snapshot complete]" {
		t.Fatalf("trigger operations = %v", got)
	}
	if fixture.runtimeAcquires.Load() != 2 || fixture.runtimeReleases.Load() != 2 ||
		fixture.runtimeActive.Load() || fixture.store.admitCalls.Load() != 1 {
		t.Fatalf(
			"runtime/acquisition effects = %d/%d active=%v admits=%d",
			fixture.runtimeAcquires.Load(),
			fixture.runtimeReleases.Load(),
			fixture.runtimeActive.Load(),
			fixture.store.admitCalls.Load(),
		)
	}
}

func TestPRDevelopmentAttentionTriggerWorkerUsesLeaseFencedAnchoredRefresh(
	t *testing.T,
) {
	fixture := newAttentionRuntimeFixture(t)
	// An automatic occurrence must use the queue's captured conversation
	// prefix, not the launcher's unanchored current-snapshot reader. In the real
	// store this is what permits later chat without changing the occurrence.
	fixture.store.snapshotErr = errors.New("unanchored conversation has advanced")
	launcher := fixture.launcher(t, []workflows.GateSpec{{
		ID: "automatic", Kind: workflows.GateDeterministic,
		When: "false", Title: "Automatic", Questions: []any{"Continue?"},
	}}, nil)
	queue := newPRDevelopmentAttentionTriggerQueueFake(fixture.snapshot)
	worker := newPRDevelopmentAttentionTriggerWorker(queue, launcher)

	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = (%t, %v)", processed, err)
	}
	stored := queue.snapshotTrigger()
	if stored.Status != eventing.PRDevelopmentAttentionTriggerDelivered ||
		stored.RunID == "" || fixture.store.snapshotCalls.Load() != 0 ||
		queue.snapshotCalls != 3 {
		t.Fatalf(
			"anchored delivery=%#v unanchored_reads=%d anchored_reads=%d",
			stored,
			fixture.store.snapshotCalls.Load(),
			queue.snapshotCalls,
		)
	}
}

func TestPRDevelopmentAttentionTriggerWorkerSupersedesAtFinalAnchoredRefreshBeforeSession(
	t *testing.T,
) {
	fixture := newAttentionRuntimeFixture(t)
	agent := &attentionRuntimeGateAgent{
		backend:       fixture.sessions,
		runtimeActive: &fixture.runtimeActive,
	}
	launcher := fixture.launcher(t, []workflows.GateSpec{{
		ID:       "discussion",
		Kind:     workflows.GateAIWorkingContext,
		AgentID:  fixture.snapshot.Controller.AgentID,
		Criteria: "ask when the repair owner needs a product decision",
		Title:    "Discuss the repair",
	}}, agent)
	queue := newPRDevelopmentAttentionTriggerQueueFake(fixture.snapshot)
	queue.snapshotErr = eventing.ErrPRDevelopmentAttentionSuperseded
	queue.snapshotErrAt = 3
	worker := newPRDevelopmentAttentionTriggerWorker(queue, launcher)

	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = (%t, %v)", processed, err)
	}
	stored := queue.snapshotTrigger()
	if stored.Status != eventing.PRDevelopmentAttentionTriggerSuperseded ||
		stored.PolicyRevision == "" || stored.SubjectRevision == "" ||
		stored.RunID != "" || len(agent.captures) != 0 || len(agent.requests) != 0 ||
		fixture.workspace.calls.Load() != 1 || fixture.store.admitCalls.Load() != 0 {
		t.Fatalf(
			"superseded=%#v captures=%d requests=%d git=%d admits=%d",
			stored,
			len(agent.captures),
			len(agent.requests),
			fixture.workspace.calls.Load(),
			fixture.store.admitCalls.Load(),
		)
	}
}

func TestPRDevelopmentAttentionTriggerWorkerPinnedReplayPrecedesSnapshotReload(
	t *testing.T,
) {
	fixture := newAttentionRuntimeFixture(t)
	launcher := fixture.launcher(t, []workflows.GateSpec{{
		ID: "automatic", Kind: workflows.GateDeterministic,
		When: "false", Title: "Automatic", Questions: []any{"Continue?"},
	}}, nil)
	queue := newPRDevelopmentAttentionTriggerQueueFake(fixture.snapshot)
	queue.completeErr = errors.New("ambiguous trigger completion")
	worker := newPRDevelopmentAttentionTriggerWorker(queue, launcher)

	processed, err := worker.ProcessOne(context.Background())
	if !processed || !errors.Is(err, queue.completeErr) {
		t.Fatalf("first ProcessOne() = (%t, %v)", processed, err)
	}
	first := queue.snapshotTrigger()
	if first.Status != eventing.PRDevelopmentAttentionTriggerClaimed ||
		first.PolicyRevision == "" || first.SubjectRevision == "" ||
		fixture.store.admitCalls.Load() != 1 {
		t.Fatalf("post-launch trigger = %#v, admits=%d", first, fixture.store.admitCalls.Load())
	}
	beforeSnapshots := queue.snapshotCalls
	beforeRuntime := fixture.runtimeAcquires.Load()
	queue.advance(2 * defaultPRDevelopmentAttentionTriggerLease)
	queue.completeErr = nil
	queue.snapshotErr = eventing.ErrPRDevelopmentAttentionSuperseded
	// A runtime reload may install a different current policy. Exact replay is
	// still driven solely by the canonical trigger pin.
	worker.Launcher = fixture.launcher(t, []workflows.GateSpec{{
		ID: "now-off", Kind: workflows.GateZero,
	}}, nil)

	processed, err = worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("replay ProcessOne() = (%t, %v)", processed, err)
	}
	replayed := queue.snapshotTrigger()
	if replayed.Status != eventing.PRDevelopmentAttentionTriggerDelivered ||
		replayed.RunID == "" || queue.snapshotCalls != beforeSnapshots ||
		fixture.runtimeAcquires.Load() != beforeRuntime ||
		fixture.store.admitCalls.Load() != 1 {
		t.Fatalf(
			"replayed trigger=%#v snapshots=%d/%d runtime=%d/%d admits=%d",
			replayed,
			queue.snapshotCalls,
			beforeSnapshots,
			fixture.runtimeAcquires.Load(),
			beforeRuntime,
			fixture.store.admitCalls.Load(),
		)
	}
}

func TestPRDevelopmentAttentionTriggerWorkerCorruptPinFailsWithoutSnapshot(t *testing.T) {
	fixture := newAttentionRuntimeFixture(t)
	launcher := fixture.launcher(t, []workflows.GateSpec{{
		ID: "off", Kind: workflows.GateZero,
	}}, nil)
	queue := newPRDevelopmentAttentionTriggerQueueFake(fixture.snapshot)
	queue.trigger.PolicyRevision = "sha256:" + strings.Repeat("a", 64)
	queue.trigger.PinnedPolicy = []byte(`{"invalid":true}`)
	worker := newPRDevelopmentAttentionTriggerWorker(queue, launcher)

	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = (%t, %v)", processed, err)
	}
	stored := queue.snapshotTrigger()
	if stored.Status != eventing.PRDevelopmentAttentionTriggerFailed ||
		stored.CompletedAt == nil || queue.snapshotCalls != 0 ||
		fixture.runtimeAcquires.Load() != 0 || fixture.store.findCalls.Load() != 0 {
		t.Fatalf("corrupt-pin trigger = %#v snapshots=%d", stored, queue.snapshotCalls)
	}
}

func TestPRDevelopmentAttentionTriggerWorkerNoopPinsNoSubjectAndHasZeroEffects(
	t *testing.T,
) {
	fixture := newAttentionRuntimeFixture(t)
	launcher := fixture.launcher(t, []workflows.GateSpec{{
		ID: "off", Kind: workflows.GateZero,
	}}, nil)
	queue := newPRDevelopmentAttentionTriggerQueueFake(fixture.snapshot)
	worker := newPRDevelopmentAttentionTriggerWorker(queue, launcher)

	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = (%t, %v)", processed, err)
	}
	stored := queue.snapshotTrigger()
	if stored.Status != eventing.PRDevelopmentAttentionTriggerNoop ||
		stored.PolicyRevision == "" || len(stored.PinnedPolicy) == 0 ||
		stored.SubjectRevision != "" || stored.RunID != "" ||
		fixture.runtimeAcquires.Load() != 0 ||
		fixture.workspaceFactoryCalls.Load() != 0 ||
		fixture.store.findCalls.Load() != 0 || fixture.store.admitCalls.Load() != 0 {
		t.Fatalf("no-op trigger = %#v", stored)
	}
	if got := queue.operationsSnapshot(); fmt.Sprint(got) !=
		"[claim snapshot policy complete]" {
		t.Fatalf("trigger operations = %v", got)
	}
}

func TestPRDevelopmentAttentionTriggerWorkerIgnoresCanceledRenewalDuringStop(
	t *testing.T,
) {
	fixture := newAttentionRuntimeFixture(t)
	base := newPRDevelopmentAttentionTriggerQueueFake(fixture.snapshot)
	queue := &cancelOnStopAttentionTriggerQueue{
		prDevelopmentAttentionTriggerQueueFake: base,
		renewStarted:                           make(chan struct{}),
	}
	worker := &AttentionTriggerWorker{
		Queue: queue,
		Now:   base.currentTime,
	}
	const lease = 30 * time.Millisecond
	claimed, err := base.ClaimPRDevelopmentAttentionTriggers(
		context.Background(),
		"attention-test",
		1,
		lease,
	)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = (%#v, %v)", claimed, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	errs := make(chan error, 1)
	go worker.renewAttentionTriggerLease(ctx, done, errs, claimed[0], lease, cancel)
	select {
	case <-queue.renewStarted:
	case <-time.After(time.Second):
		t.Fatal("heartbeat did not enter renewal")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("heartbeat did not stop")
	}
	select {
	case renewErr := <-errs:
		t.Fatalf("self-canceled renewal was reported as lease loss: %v", renewErr)
	default:
	}
}

func TestPRDevelopmentAttentionTriggerWorkerRejectsUnsafeShortLease(t *testing.T) {
	fixture := newAttentionRuntimeFixture(t)
	launcher := fixture.launcher(t, []workflows.GateSpec{{
		ID: "off", Kind: workflows.GateZero,
	}}, nil)
	queue := newPRDevelopmentAttentionTriggerQueueFake(fixture.snapshot)
	worker := newPRDevelopmentAttentionTriggerWorker(queue, launcher)
	worker.LeaseDuration = minimumPRDevelopmentAttentionTriggerLease - time.Nanosecond

	processed, err := worker.ProcessOne(context.Background())
	if processed || !errors.Is(err, ErrInvalidRequest) ||
		len(queue.operationsSnapshot()) != 0 {
		t.Fatalf("ProcessOne(short lease) = (%t, %v)", processed, err)
	}
}

func TestPRDevelopmentAttentionTriggerWorkerHighAttemptTransientStillRetries(t *testing.T) {
	fixture := newAttentionRuntimeFixture(t)
	launcher := fixture.launcher(t, []workflows.GateSpec{{
		ID: "off", Kind: workflows.GateZero,
	}}, nil)
	queue := newPRDevelopmentAttentionTriggerQueueFake(fixture.snapshot)
	queue.trigger.Attempts = 100
	transient := errors.New("temporary local store outage")
	queue.snapshotErr = transient
	worker := newPRDevelopmentAttentionTriggerWorker(queue, launcher)

	processed, err := worker.ProcessOne(context.Background())
	if !processed || !errors.Is(err, transient) {
		t.Fatalf("ProcessOne() = (%t, %v)", processed, err)
	}
	stored := queue.snapshotTrigger()
	if stored.Status != eventing.PRDevelopmentAttentionTriggerPending ||
		stored.Attempts != 101 ||
		!stored.AvailableAt.Equal(queue.currentTime().Add(time.Minute)) ||
		stored.CompletedAt != nil || stored.LastError != ErrUnavailable.Error() {
		t.Fatalf("released high-attempt trigger = %#v", stored)
	}
}

func TestPRDevelopmentAttentionTriggerWorkerSupersededIsTerminal(t *testing.T) {
	fixture := newAttentionRuntimeFixture(t)
	launcher := fixture.launcher(t, []workflows.GateSpec{{
		ID: "off", Kind: workflows.GateZero,
	}}, nil)
	queue := newPRDevelopmentAttentionTriggerQueueFake(fixture.snapshot)
	queue.snapshotErr = eventing.ErrPRDevelopmentAttentionSuperseded
	worker := newPRDevelopmentAttentionTriggerWorker(queue, launcher)

	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = (%t, %v)", processed, err)
	}
	stored := queue.snapshotTrigger()
	if stored.Status != eventing.PRDevelopmentAttentionTriggerSuperseded ||
		stored.CompletedAt == nil || stored.RunID != "" ||
		stored.LastError != eventing.ErrPRDevelopmentAttentionSuperseded.Error() {
		t.Fatalf("superseded trigger = %#v", stored)
	}
}

func TestPRDevelopmentAttentionTriggerWorkerAdmissionUncertaintyNeedsRecovery(
	t *testing.T,
) {
	fixture := newAttentionRuntimeFixture(t)
	launcher := fixture.launcher(t, []workflows.GateSpec{{
		ID: "automatic", Kind: workflows.GateDeterministic,
		When: "false", Title: "Automatic", Questions: []any{"Continue?"},
	}}, nil)
	launcher.executor.AdmittedRunCreate = func(
		_ context.Context,
		_ *workflows.Run,
		create func() error,
	) error {
		if err := create(); err != nil {
			return err
		}
		return errors.New("private post-create admission failure")
	}
	queue := newPRDevelopmentAttentionTriggerQueueFake(fixture.snapshot)
	worker := newPRDevelopmentAttentionTriggerWorker(queue, launcher)

	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = (%t, %v)", processed, err)
	}
	stored := queue.snapshotTrigger()
	if stored.Status != eventing.PRDevelopmentAttentionTriggerRecoveryRequired ||
		stored.CompletedAt == nil || stored.RunID != "" ||
		stored.LastError != "attention private workflow admission is uncertain" {
		t.Fatalf("recovery trigger = %#v", stored)
	}
}

func TestPRDevelopmentAttentionTriggerWorkerUncertaintyReplayRemainsRecoveryRequired(
	t *testing.T,
) {
	fixture := newAttentionRuntimeFixture(t)
	launcher := fixture.launcher(t, []workflows.GateSpec{{
		ID: "automatic", Kind: workflows.GateDeterministic,
		When: "false", Title: "Automatic", Questions: []any{"Continue?"},
	}}, nil)
	launcher.executor.AdmittedRunCreate = func(
		_ context.Context,
		_ *workflows.Run,
		create func() error,
	) error {
		if err := create(); err != nil {
			return err
		}
		return errors.New("private post-create admission failure")
	}
	queue := newPRDevelopmentAttentionTriggerQueueFake(fixture.snapshot)
	queue.completeErr = errors.New("ambiguous recovery completion")
	worker := newPRDevelopmentAttentionTriggerWorker(queue, launcher)

	processed, err := worker.ProcessOne(context.Background())
	if !processed || !errors.Is(err, queue.completeErr) {
		t.Fatalf("first ProcessOne() = (%t, %v)", processed, err)
	}
	first := queue.snapshotTrigger()
	if first.Status != eventing.PRDevelopmentAttentionTriggerClaimed ||
		first.PolicyRevision == "" || first.SubjectRevision == "" ||
		fixture.store.admitCalls.Load() != 1 {
		t.Fatalf("uncertain claimed trigger = %#v", first)
	}
	beforeSnapshots := queue.snapshotCalls
	queue.advance(2 * defaultPRDevelopmentAttentionTriggerLease)
	queue.completeErr = nil
	queue.snapshotErr = eventing.ErrPRDevelopmentAttentionSuperseded

	processed, err = worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("replay ProcessOne() = (%t, %v)", processed, err)
	}
	replayed := queue.snapshotTrigger()
	if replayed.Status != eventing.PRDevelopmentAttentionTriggerRecoveryRequired ||
		replayed.RunID != "" || queue.snapshotCalls != beforeSnapshots ||
		fixture.store.admitCalls.Load() != 1 {
		t.Fatalf(
			"uncertain replay=%#v snapshots=%d/%d admits=%d",
			replayed,
			queue.snapshotCalls,
			beforeSnapshots,
			fixture.store.admitCalls.Load(),
		)
	}
}

func TestPRDevelopmentAttentionTriggerWorkerAdmissionConflictRetriesPinnedDecision(
	t *testing.T,
) {
	fixture := newAttentionRuntimeFixture(t)
	launcher := fixture.launcher(t, []workflows.GateSpec{{
		ID: "automatic", Kind: workflows.GateDeterministic,
		When: "false", Title: "Automatic", Questions: []any{"Continue?"},
	}}, nil)
	launcher.executor.AdmittedRunCreate = func(
		context.Context,
		*workflows.Run,
		func() error,
	) error {
		return workflows.ErrRunAdmissionConflict
	}
	queue := newPRDevelopmentAttentionTriggerQueueFake(fixture.snapshot)
	worker := newPRDevelopmentAttentionTriggerWorker(queue, launcher)

	processed, err := worker.ProcessOne(context.Background())
	if !processed || !errors.Is(err, workflows.ErrRunAdmissionConflict) {
		t.Fatalf("ProcessOne() = (%t, %v)", processed, err)
	}
	stored := queue.snapshotTrigger()
	if stored.Status != eventing.PRDevelopmentAttentionTriggerPending ||
		stored.PolicyRevision == "" || stored.SubjectRevision == "" ||
		stored.CompletedAt != nil || stored.RunID != "" ||
		!stored.AvailableAt.Equal(queue.currentTime().Add(time.Second)) {
		t.Fatalf("conflicted trigger = %#v", stored)
	}
	runs, listErr := fixture.runs.ListRuns(context.Background())
	if listErr != nil || len(runs) != 0 {
		t.Fatalf("conflicted runs = (%#v, %v)", runs, listErr)
	}
}

func TestPRDevelopmentAttentionTriggerWorkerSnapshotDriftBeforeSubjectPinRetries(
	t *testing.T,
) {
	fixture := newAttentionRuntimeFixture(t)
	launcher := fixture.launcher(t, []workflows.GateSpec{{
		ID: "automatic", Kind: workflows.GateDeterministic,
		When: "false", Title: "Automatic", Questions: []any{"Continue?"},
	}}, nil)
	queue := newPRDevelopmentAttentionTriggerQueueFake(fixture.snapshot)
	advanced := advancedAttentionTriggerSnapshot(fixture.snapshot)
	queue.snapshotAfterPolicyPin = &advanced
	worker := newPRDevelopmentAttentionTriggerWorker(queue, launcher)

	processed, err := worker.ProcessOne(context.Background())
	if !processed || !errors.Is(err, workflows.ErrRunAdmissionConflict) {
		t.Fatalf("ProcessOne() = (%t, %v)", processed, err)
	}
	stored := queue.snapshotTrigger()
	if stored.Status != eventing.PRDevelopmentAttentionTriggerPending ||
		stored.PolicyRevision == "" || stored.SubjectRevision != "" ||
		stored.CompletedAt != nil || stored.RunID != "" ||
		fixture.workspace.calls.Load() != 0 || fixture.store.admitCalls.Load() != 0 {
		t.Fatalf(
			"pre-subject drift trigger=%#v git=%d admits=%d",
			stored,
			fixture.workspace.calls.Load(),
			fixture.store.admitCalls.Load(),
		)
	}
	if got := queue.operationsSnapshot(); fmt.Sprint(got) !=
		"[claim snapshot policy snapshot release]" {
		t.Fatalf("trigger operations = %v", got)
	}
}

func TestPRDevelopmentAttentionTriggerWorkerSnapshotDriftAfterSubjectPinNeedsRecovery(
	t *testing.T,
) {
	fixture := newAttentionRuntimeFixture(t)
	launcher := fixture.launcher(t, []workflows.GateSpec{{
		ID: "automatic", Kind: workflows.GateDeterministic,
		When: "false", Title: "Automatic", Questions: []any{"Continue?"},
	}}, nil)
	queue := newPRDevelopmentAttentionTriggerQueueFake(fixture.snapshot)
	advanced := advancedAttentionTriggerSnapshot(fixture.snapshot)
	queue.snapshotAfterSubjectPin = &advanced
	worker := newPRDevelopmentAttentionTriggerWorker(queue, launcher)

	processed, err := worker.ProcessOne(context.Background())
	if !processed || err != nil {
		t.Fatalf("ProcessOne() = (%t, %v)", processed, err)
	}
	stored := queue.snapshotTrigger()
	if stored.Status != eventing.PRDevelopmentAttentionTriggerRecoveryRequired ||
		stored.PolicyRevision == "" || stored.SubjectRevision == "" ||
		stored.CompletedAt == nil || stored.RunID != "" ||
		stored.LastError != errPinnedAttentionSubjectDrift.Error() ||
		fixture.workspace.calls.Load() != 1 || fixture.store.admitCalls.Load() != 0 {
		t.Fatalf(
			"post-subject drift trigger=%#v git=%d admits=%d",
			stored,
			fixture.workspace.calls.Load(),
			fixture.store.admitCalls.Load(),
		)
	}
	if got := queue.operationsSnapshot(); fmt.Sprint(got) !=
		"[claim snapshot policy snapshot subject snapshot complete]" {
		t.Fatalf("trigger operations = %v", got)
	}
}

func TestPRDevelopmentAttentionTriggerWorkerPinnedSubjectDriftAfterAdmissionRetryNeedsRecovery(
	t *testing.T,
) {
	fixture := newAttentionRuntimeFixture(t)
	launcher := fixture.launcher(t, []workflows.GateSpec{{
		ID: "automatic", Kind: workflows.GateDeterministic,
		When: "false", Title: "Automatic", Questions: []any{"Continue?"},
	}}, nil)
	launcher.executor.AdmittedRunCreate = func(
		context.Context,
		*workflows.Run,
		func() error,
	) error {
		return workflows.ErrRunAdmissionConflict
	}
	queue := newPRDevelopmentAttentionTriggerQueueFake(fixture.snapshot)
	worker := newPRDevelopmentAttentionTriggerWorker(queue, launcher)

	processed, err := worker.ProcessOne(context.Background())
	if !processed || !errors.Is(err, workflows.ErrRunAdmissionConflict) {
		t.Fatalf("first ProcessOne() = (%t, %v)", processed, err)
	}
	first := queue.snapshotTrigger()
	if first.Status != eventing.PRDevelopmentAttentionTriggerPending ||
		first.PolicyRevision == "" || first.SubjectRevision == "" ||
		fixture.store.admitCalls.Load() != 1 {
		t.Fatalf("first trigger=%#v admits=%d", first, fixture.store.admitCalls.Load())
	}

	launcher.executor.AdmittedRunCreate = nil
	fixture.workspace.snapshot.ChangedPaths = append(
		fixture.workspace.snapshot.ChangedPaths,
		"pkg/another.go",
	)
	queue.advance(time.Second)
	processed, err = worker.ProcessOne(context.Background())
	if !processed || err != nil {
		t.Fatalf("retry ProcessOne() = (%t, %v)", processed, err)
	}
	retried := queue.snapshotTrigger()
	if retried.Status != eventing.PRDevelopmentAttentionTriggerRecoveryRequired ||
		retried.PolicyRevision != first.PolicyRevision ||
		retried.SubjectRevision != first.SubjectRevision ||
		retried.RunID != "" || retried.CompletedAt == nil ||
		retried.LastError != errPinnedAttentionSubjectDrift.Error() ||
		fixture.store.admitCalls.Load() != 1 {
		t.Fatalf("retried trigger=%#v admits=%d", retried, fixture.store.admitCalls.Load())
	}
}

func TestPRDevelopmentAttentionTriggerWorkerOversizedSubjectFailsAfterPolicyPin(
	t *testing.T,
) {
	fixture := newAttentionRuntimeFixture(t)
	fixture.workspace.snapshot.UnifiedDiff = strings.Repeat(
		"x",
		workflows.MaxWorkflowGateSubjectBytes+1,
	)
	launcher := fixture.launcher(t, []workflows.GateSpec{{
		ID: "automatic", Kind: workflows.GateDeterministic,
		When: "false", Title: "Automatic", Questions: []any{"Continue?"},
	}}, nil)
	queue := newPRDevelopmentAttentionTriggerQueueFake(fixture.snapshot)
	worker := newPRDevelopmentAttentionTriggerWorker(queue, launcher)

	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = (%t, %v)", processed, err)
	}
	stored := queue.snapshotTrigger()
	if stored.Status != eventing.PRDevelopmentAttentionTriggerFailed ||
		stored.PolicyRevision == "" || len(stored.PinnedPolicy) == 0 ||
		stored.SubjectRevision != "" || stored.RunID != "" ||
		stored.LastError != ErrAttentionSubjectTooLarge.Error() ||
		fixture.store.admitCalls.Load() != 0 {
		t.Fatalf("oversized trigger = %#v", stored)
	}
}

func TestPRDevelopmentAttentionTriggerWorkerInvalidPinnedCompositionFailsBeforeAdmission(
	t *testing.T,
) {
	fixture := newAttentionRuntimeFixture(t)
	launcher := fixture.launcher(t, []workflows.GateSpec{{
		ID:        "invalid-subject-path",
		Kind:      workflows.GateDeterministic,
		When:      "inputs.gate_subject.missing == true",
		Title:     "Invalid immutable composition",
		Questions: []any{"Continue?"},
	}}, nil)
	queue := newPRDevelopmentAttentionTriggerQueueFake(fixture.snapshot)
	worker := newPRDevelopmentAttentionTriggerWorker(queue, launcher)

	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = (%t, %v)", processed, err)
	}
	stored := queue.snapshotTrigger()
	if stored.Status != eventing.PRDevelopmentAttentionTriggerFailed ||
		stored.PolicyRevision == "" || stored.SubjectRevision == "" ||
		stored.RunID != "" || fixture.store.admitCalls.Load() != 0 {
		t.Fatalf("invalid pinned composition trigger = %#v", stored)
	}
}

func newPRDevelopmentAttentionTriggerWorker(
	queue *prDevelopmentAttentionTriggerQueueFake,
	launcher *AttentionLauncher,
) *AttentionTriggerWorker {
	return &AttentionTriggerWorker{
		Queue:         queue,
		Launcher:      launcher,
		WorkerLabel:   "attention-test",
		LeaseDuration: defaultPRDevelopmentAttentionTriggerLease,
		Now:           queue.currentTime,
	}
}

type prDevelopmentAttentionTriggerQueueFake struct {
	mu                      sync.Mutex
	now                     time.Time
	trigger                 eventing.PRDevelopmentAttentionTrigger
	snapshot                eventing.PRDevelopmentAttentionSnapshot
	snapshotAfterPolicyPin  *eventing.PRDevelopmentAttentionSnapshot
	snapshotAfterSubjectPin *eventing.PRDevelopmentAttentionSnapshot
	operations              []string
	snapshotCalls           int
	snapshotErr             error
	snapshotErrAt           int
	completeErr             error
}

type cancelOnStopAttentionTriggerQueue struct {
	*prDevelopmentAttentionTriggerQueueFake
	renewStarted chan struct{}
	renewOnce    sync.Once
}

func (queue *cancelOnStopAttentionTriggerQueue) RenewPRDevelopmentAttentionTriggerLease(
	ctx context.Context,
	_ string,
	_ string,
	_ time.Duration,
) error {
	queue.renewOnce.Do(func() { close(queue.renewStarted) })
	<-ctx.Done()
	return ctx.Err()
}

func (queue *cancelOnStopAttentionTriggerQueue) GetClaimedPRDevelopmentAttentionSnapshot(
	ctx context.Context,
	reviewEntryID string,
	leaseToken string,
) (eventing.PRDevelopmentAttentionTrigger, eventing.PRDevelopmentAttentionSnapshot, error) {
	select {
	case <-queue.renewStarted:
	case <-ctx.Done():
		return eventing.PRDevelopmentAttentionTrigger{},
			eventing.PRDevelopmentAttentionSnapshot{}, ctx.Err()
	}
	return queue.prDevelopmentAttentionTriggerQueueFake.
		GetClaimedPRDevelopmentAttentionSnapshot(ctx, reviewEntryID, leaseToken)
}

func newPRDevelopmentAttentionTriggerQueueFake(
	snapshot eventing.PRDevelopmentAttentionSnapshot,
) *prDevelopmentAttentionTriggerQueueFake {
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	return &prDevelopmentAttentionTriggerQueueFake{
		now: now,
		trigger: eventing.PRDevelopmentAttentionTrigger{
			ReviewEntryID:       snapshot.ReviewEntry.ID,
			ReviewEntryHash:     snapshot.ReviewEntry.EntryHash,
			CaseID:              snapshot.Case.ID,
			ConversationVersion: snapshot.Conversation.Version,
			TranscriptDigest:    snapshot.HighWater.TranscriptDigest,
			DecisionPoint:       eventing.PRDevelopmentAttentionDecisionReviewRequired,
			Status:              eventing.PRDevelopmentAttentionTriggerPending,
			AvailableAt:         now,
			CreatedAt:           now,
			UpdatedAt:           now,
		},
		snapshot: snapshot,
	}
}

func (queue *prDevelopmentAttentionTriggerQueueFake) GetPRDevelopmentAttentionTrigger(
	_ context.Context,
	reviewEntryID string,
) (eventing.PRDevelopmentAttentionTrigger, error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if reviewEntryID != queue.trigger.ReviewEntryID {
		return eventing.PRDevelopmentAttentionTrigger{}, eventing.ErrNotFound
	}
	return clonePRDevelopmentAttentionTrigger(queue.trigger), nil
}

func (queue *prDevelopmentAttentionTriggerQueueFake) ClaimPRDevelopmentAttentionTriggers(
	_ context.Context,
	_ string,
	limit int,
	lease time.Duration,
) ([]eventing.PRDevelopmentAttentionTrigger, error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if limit != 1 {
		return nil, errors.New("unexpected claim limit")
	}
	claimable := queue.trigger.Status == eventing.PRDevelopmentAttentionTriggerPending &&
		!queue.trigger.AvailableAt.After(queue.now)
	if queue.trigger.Status == eventing.PRDevelopmentAttentionTriggerClaimed &&
		queue.trigger.LeaseUntil != nil && !queue.trigger.LeaseUntil.After(queue.now) {
		claimable = true
	}
	if !claimable {
		return nil, nil
	}
	queue.trigger.Status = eventing.PRDevelopmentAttentionTriggerClaimed
	queue.trigger.Attempts++
	queue.trigger.LeaseToken = fmt.Sprintf("lease-%d", queue.trigger.Attempts)
	deadline := queue.now.Add(lease)
	queue.trigger.LeaseUntil = &deadline
	queue.trigger.UpdatedAt = queue.now
	queue.operations = append(queue.operations, "claim")
	return []eventing.PRDevelopmentAttentionTrigger{
		clonePRDevelopmentAttentionTrigger(queue.trigger),
	}, nil
}

func (queue *prDevelopmentAttentionTriggerQueueFake) RenewPRDevelopmentAttentionTriggerLease(
	_ context.Context,
	reviewEntryID string,
	leaseToken string,
	lease time.Duration,
) error {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if !queue.liveClaim(reviewEntryID, leaseToken) {
		return eventing.ErrStaleLease
	}
	deadline := queue.now.Add(lease)
	queue.trigger.LeaseUntil = &deadline
	return nil
}

func (queue *prDevelopmentAttentionTriggerQueueFake) GetClaimedPRDevelopmentAttentionSnapshot(
	_ context.Context,
	reviewEntryID string,
	leaseToken string,
) (eventing.PRDevelopmentAttentionTrigger, eventing.PRDevelopmentAttentionSnapshot, error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	queue.snapshotCalls++
	queue.operations = append(queue.operations, "snapshot")
	if !queue.liveClaim(reviewEntryID, leaseToken) {
		return eventing.PRDevelopmentAttentionTrigger{},
			eventing.PRDevelopmentAttentionSnapshot{}, eventing.ErrStaleLease
	}
	if queue.snapshotErr != nil &&
		(queue.snapshotErrAt == 0 || queue.snapshotCalls == queue.snapshotErrAt) {
		return eventing.PRDevelopmentAttentionTrigger{},
			eventing.PRDevelopmentAttentionSnapshot{}, queue.snapshotErr
	}
	return clonePRDevelopmentAttentionTrigger(queue.trigger), queue.snapshot, nil
}

func (queue *prDevelopmentAttentionTriggerQueueFake) PinPRDevelopmentAttentionTriggerPolicy(
	_ context.Context,
	input eventing.PRDevelopmentAttentionPolicyPin,
) (eventing.PRDevelopmentAttentionTrigger, error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if !queue.liveClaim(input.ReviewEntryID, input.LeaseToken) ||
		input.Snapshot != queue.snapshot.HighWater {
		return eventing.PRDevelopmentAttentionTrigger{}, eventing.ErrStaleLease
	}
	if queue.trigger.PolicyRevision != "" || len(queue.trigger.PinnedPolicy) != 0 {
		if queue.trigger.PolicyRevision != input.PolicyRevision ||
			!bytesEqual(queue.trigger.PinnedPolicy, input.PinnedPolicy) {
			return eventing.PRDevelopmentAttentionTrigger{},
				eventing.ErrPRDevelopmentAttentionTriggerConflict
		}
		return clonePRDevelopmentAttentionTrigger(queue.trigger), nil
	}
	queue.trigger.PolicyRevision = input.PolicyRevision
	queue.trigger.PinnedPolicy = append([]byte(nil), input.PinnedPolicy...)
	queue.operations = append(queue.operations, "policy")
	if queue.snapshotAfterPolicyPin != nil {
		queue.snapshot = *queue.snapshotAfterPolicyPin
	}
	return clonePRDevelopmentAttentionTrigger(queue.trigger), nil
}

func (queue *prDevelopmentAttentionTriggerQueueFake) PinPRDevelopmentAttentionTriggerSubject(
	_ context.Context,
	input eventing.PRDevelopmentAttentionSubjectPin,
) (eventing.PRDevelopmentAttentionTrigger, error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if !queue.liveClaim(input.ReviewEntryID, input.LeaseToken) ||
		input.Snapshot != queue.snapshot.HighWater ||
		input.PolicyRevision != queue.trigger.PolicyRevision {
		return eventing.PRDevelopmentAttentionTrigger{}, eventing.ErrStaleLease
	}
	if queue.trigger.SubjectRevision != "" &&
		queue.trigger.SubjectRevision != input.SubjectRevision {
		return eventing.PRDevelopmentAttentionTrigger{},
			eventing.ErrPRDevelopmentAttentionTriggerConflict
	}
	queue.trigger.SubjectRevision = input.SubjectRevision
	queue.operations = append(queue.operations, "subject")
	if queue.snapshotAfterSubjectPin != nil {
		queue.snapshot = *queue.snapshotAfterSubjectPin
	}
	return clonePRDevelopmentAttentionTrigger(queue.trigger), nil
}

func (queue *prDevelopmentAttentionTriggerQueueFake) ReleasePRDevelopmentAttentionTrigger(
	_ context.Context,
	input eventing.PRDevelopmentAttentionTriggerRelease,
) error {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if !queue.liveClaim(input.ReviewEntryID, input.LeaseToken) {
		return eventing.ErrStaleLease
	}
	queue.trigger.Status = eventing.PRDevelopmentAttentionTriggerPending
	queue.trigger.LeaseToken = ""
	queue.trigger.LeaseUntil = nil
	queue.trigger.AvailableAt = input.AvailableAt
	queue.trigger.LastError = input.Error
	queue.operations = append(queue.operations, "release")
	return nil
}

func (queue *prDevelopmentAttentionTriggerQueueFake) CompletePRDevelopmentAttentionTrigger(
	_ context.Context,
	input eventing.PRDevelopmentAttentionTriggerCompletion,
) error {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if !queue.liveClaim(input.ReviewEntryID, input.LeaseToken) {
		return eventing.ErrStaleLease
	}
	if queue.completeErr != nil {
		return queue.completeErr
	}
	queue.trigger.Status = input.Status
	queue.trigger.RunID = input.RunID
	queue.trigger.LastError = input.Error
	queue.trigger.LeaseToken = ""
	queue.trigger.LeaseUntil = nil
	completed := queue.now
	queue.trigger.CompletedAt = &completed
	queue.operations = append(queue.operations, "complete")
	return nil
}

func (queue *prDevelopmentAttentionTriggerQueueFake) liveClaim(
	reviewEntryID string,
	leaseToken string,
) bool {
	return reviewEntryID == queue.trigger.ReviewEntryID &&
		leaseToken == queue.trigger.LeaseToken && leaseToken != "" &&
		queue.trigger.Status == eventing.PRDevelopmentAttentionTriggerClaimed &&
		queue.trigger.LeaseUntil != nil && queue.trigger.LeaseUntil.After(queue.now)
}

func (queue *prDevelopmentAttentionTriggerQueueFake) snapshotTrigger() eventing.PRDevelopmentAttentionTrigger {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	return clonePRDevelopmentAttentionTrigger(queue.trigger)
}

func (queue *prDevelopmentAttentionTriggerQueueFake) operationsSnapshot() []string {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	return append([]string(nil), queue.operations...)
}

func (queue *prDevelopmentAttentionTriggerQueueFake) currentTime() time.Time {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	return queue.now
}

func (queue *prDevelopmentAttentionTriggerQueueFake) advance(duration time.Duration) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	queue.now = queue.now.Add(duration)
}

func clonePRDevelopmentAttentionTrigger(
	trigger eventing.PRDevelopmentAttentionTrigger,
) eventing.PRDevelopmentAttentionTrigger {
	trigger.PinnedPolicy = append([]byte(nil), trigger.PinnedPolicy...)
	if trigger.LeaseUntil != nil {
		deadline := *trigger.LeaseUntil
		trigger.LeaseUntil = &deadline
	}
	if trigger.CompletedAt != nil {
		completed := *trigger.CompletedAt
		trigger.CompletedAt = &completed
	}
	return trigger
}

func bytesEqual(left, right []byte) bool {
	return string(left) == string(right)
}

func advancedAttentionTriggerSnapshot(
	snapshot eventing.PRDevelopmentAttentionSnapshot,
) eventing.PRDevelopmentAttentionSnapshot {
	snapshot.Controller.Revision++
	snapshot.HighWater.ControllerRevision = snapshot.Controller.Revision
	return snapshot
}
