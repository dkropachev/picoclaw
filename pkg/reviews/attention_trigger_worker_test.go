package reviews

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const attentionTriggerTestSubmissionID = "prs_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func TestAttentionTriggerWorkerPinsBeforeEffectAndPolicyDriftRetryConverges(t *testing.T) {
	now := serviceTestTime.Add(30 * time.Minute)
	detail := submittedAttentionTestDetail(serviceTestCaseID, 12)
	store := newAttentionTestStore(detail)
	service := newAttentionTestService(t, store, nil, nil)
	agent := &attentionTestAgent{taskByAgent: map[string]bool{"reviewer": false}}
	workspace := t.TempDir()
	runStore := workflows.NewFileRunStore(workspace)
	firstPolicy := attentionTestPolicy("generation-before-reload", []workflows.GateSpec{{
		ID: "isolated", Kind: workflows.GateAIIsolatedContext,
		AgentID: "reviewer", Criteria: "ask when evidence is incomplete", Title: "Clarify",
	}})
	changedPolicy := attentionTestPolicy("generation-after-reload", []workflows.GateSpec{{
		ID: "changed", Kind: workflows.GateDeterministic,
		When: "true", Title: "Changed policy", Questions: []any{"Use the new policy?"},
	}})
	policies := &attentionTestPolicySource{
		snapshots: []AttentionPolicySnapshot{firstPolicy, changedPolicy},
	}
	launcher := newAttentionTestLauncher(t, service, &workflows.Executor{
		WorkspaceDir: workspace,
		Store:        runStore,
		Agents:       agent,
	}, policies)
	queue := newAttentionTriggerQueueFake(now, detail.Case.ID, detail.Case.Version)
	queue.completeErrors = []error{errors.New("completion response lost before acknowledgement")}
	queue.onPin = func(eventing.ReviewAttentionPolicyPin) {
		requests, captures := agent.observations()
		if len(requests) != 0 || len(captures) != 0 || len(store.linksSnapshot()) != 0 {
			t.Fatalf(
				"effects preceded policy pin: requests=%d captures=%d links=%d",
				len(requests), len(captures), len(store.linksSnapshot()),
			)
		}
	}
	worker := &AttentionTriggerWorker{
		Queue: queue, Launcher: launcher, Now: func() time.Time { return now },
	}
	worker.LeaseDuration = 2 * time.Nanosecond
	if processed, leaseErr := worker.ProcessOne(context.Background()); processed ||
		!errors.Is(leaseErr, ErrInvalidRequest) || queue.claimCalls != 0 {
		t.Fatalf(
			"undersized lease ProcessOne()=(%v,%v) claim_calls=%d",
			processed, leaseErr, queue.claimCalls,
		)
	}
	worker.LeaseDuration = 0

	processed, err := worker.ProcessOne(context.Background())
	if !processed || err == nil || err.Error() != "completion response lost before acknowledgement" {
		t.Fatalf("first ProcessOne() = (%v, %v), want completed-run ack failure", processed, err)
	}
	first := queue.snapshot()
	if first.Status != eventing.ReviewAttentionClaimed || first.PolicyRevision == "" ||
		len(first.PinnedPolicy) == 0 || first.RunID != "" || len(queue.pins) != 1 {
		t.Fatalf("first durable trigger = %#v, pins=%d", first, len(queue.pins))
	}

	// The trigger is reclaimed with its exact pin after a simulated crash. The
	// trusted source now advertises a different policy, but retry must not read
	// it or execute a second model/run.
	now = now.Add(10 * time.Minute)
	queue.setNow(now)
	processed, err = worker.ProcessOne(context.Background())
	if !processed || err != nil {
		t.Fatalf("retry ProcessOne() = (%v, %v)", processed, err)
	}
	terminal := queue.snapshot()
	requests, captures := agent.observations()
	if terminal.Status != eventing.ReviewAttentionDelivered || terminal.RunID == "" ||
		len(queue.pins) != 1 || len(queue.completions) != 2 ||
		len(requests) != 1 || len(captures) != 0 || len(store.linksSnapshot()) != 1 ||
		attentionPolicySourceCalls(policies) != 1 {
		t.Fatalf(
			"retry effects trigger=%#v pins=%d completions=%d requests=%d captures=%d links=%d policy_calls=%d",
			terminal, len(queue.pins), len(queue.completions), len(requests), len(captures),
			len(store.linksSnapshot()), attentionPolicySourceCalls(policies),
		)
	}
	runs, listErr := runStore.ListRuns(context.Background())
	if listErr != nil || len(runs) != 1 || runs[0].ID != terminal.RunID {
		t.Fatalf("runs = (%#v, %v), want one converged run", runs, listErr)
	}
}

func TestAttentionTriggerWorkerPinnedNoopSurvivesCrashAndPolicyReload(t *testing.T) {
	now := serviceTestTime.Add(45 * time.Minute)
	detail := submittedAttentionTestDetail(serviceTestCaseID, 12)
	store := newAttentionTestStore(detail)
	service := newAttentionTestService(t, store, nil, nil)
	workspace := t.TempDir()
	runStore := workflows.NewFileRunStore(workspace)
	policies := &attentionTestPolicySource{snapshots: []AttentionPolicySnapshot{
		{Revision: "zero-generation"},
		attentionTestPolicy("later-active-generation", []workflows.GateSpec{{
			ID: "later", Kind: workflows.GateDeterministic,
			When: "true", Title: "Later", Questions: []any{"Should not run"},
		}}),
	}}
	launcher := newAttentionTestLauncher(t, service, &workflows.Executor{
		WorkspaceDir: workspace,
		Store:        runStore,
	}, policies)
	queue := newAttentionTriggerQueueFake(now, detail.Case.ID, detail.Case.Version)
	queue.completeErrors = []error{errors.New("crash after no-op before acknowledgement")}
	worker := &AttentionTriggerWorker{
		Queue: queue, Launcher: launcher, Now: func() time.Time { return now },
	}

	if processed, err := worker.ProcessOne(context.Background()); !processed || err == nil {
		t.Fatalf("first ProcessOne() = (%v, %v), want ack failure", processed, err)
	}
	pinned := queue.snapshot()
	if pinned.PolicyRevision == "" || len(pinned.PinnedPolicy) == 0 {
		t.Fatalf("no-op policy was not pinned: %#v", pinned)
	}
	now = now.Add(10 * time.Minute)
	queue.setNow(now)
	if processed, err := worker.ProcessOne(context.Background()); !processed || err != nil {
		t.Fatalf("retry ProcessOne() = (%v, %v)", processed, err)
	}
	terminal := queue.snapshot()
	runs, listErr := runStore.ListRuns(context.Background())
	if terminal.Status != eventing.ReviewAttentionNoop || terminal.RunID != "" ||
		len(queue.pins) != 1 || len(store.linksSnapshot()) != 0 ||
		listErr != nil || len(runs) != 0 || attentionPolicySourceCalls(policies) != 1 {
		t.Fatalf(
			"no-op retry trigger=%#v pins=%d links=%d runs=%#v policy_calls=%d err=%v",
			terminal, len(queue.pins), len(store.linksSnapshot()), runs,
			attentionPolicySourceCalls(policies), listErr,
		)
	}
}

func TestAttentionTriggerWorkerCompletesAdmittedFailedRun(t *testing.T) {
	now := serviceTestTime.Add(time.Hour)
	detail := submittedAttentionTestDetail(serviceTestCaseID, 12)
	store := newAttentionTestStore(detail)
	service := newAttentionTestService(t, store, nil, nil)
	secret := "private-provider-diagnostic"
	agent := &attentionTestAgent{runErr: errors.New(secret)}
	workspace := t.TempDir()
	runStore := workflows.NewFileRunStore(workspace)
	policies := &attentionTestPolicySource{snapshots: []AttentionPolicySnapshot{
		attentionTestPolicy("failed-run-generation", []workflows.GateSpec{{
			ID: "isolated", Kind: workflows.GateAIIsolatedContext,
			AgentID: "reviewer", Criteria: "ask when uncertain", Title: "Clarify",
		}}),
	}}
	launcher := newAttentionTestLauncher(t, service, &workflows.Executor{
		WorkspaceDir: workspace,
		Store:        runStore,
		Agents:       agent,
	}, policies)
	queue := newAttentionTriggerQueueFake(now, detail.Case.ID, detail.Case.Version)
	worker := &AttentionTriggerWorker{
		Queue: queue, Launcher: launcher, Now: func() time.Time { return now },
	}

	processed, err := worker.ProcessOne(context.Background())
	terminal := queue.snapshot()
	if !processed || err != nil || terminal.Status != eventing.ReviewAttentionDelivered ||
		terminal.RunID == "" || len(queue.releases) != 0 {
		t.Fatalf(
			"ProcessOne()=(%v,%v) trigger=%#v releases=%#v",
			processed, err, terminal, queue.releases,
		)
	}
	run, getErr := runStore.GetRun(context.Background(), terminal.RunID)
	if getErr != nil || run.Status != workflows.RunStatusFailed {
		t.Fatalf("failed run = (%#v, %v)", run, getErr)
	}
	if bytes.Contains(queue.pins[0].PinnedPolicy, []byte(secret)) ||
		queue.snapshot().LastError != "" {
		t.Fatal("private provider diagnostic entered the attention trigger")
	}
}

func TestAttentionTriggerWorkerReleasesPreAdmissionFailureThenPinsCurrentPolicy(t *testing.T) {
	now := serviceTestTime.Add(75 * time.Minute)
	detail := submittedAttentionTestDetail(serviceTestCaseID, 12)
	store := newAttentionTestStore(detail)
	service := newAttentionTestService(t, store, nil, nil)
	workspace := t.TempDir()
	runStore := workflows.NewFileRunStore(workspace)
	policies := &attentionTestPolicySource{snapshots: []AttentionPolicySnapshot{
		{Revision: ""},
		attentionTestPolicy("recovered-generation", []workflows.GateSpec{{
			ID: "automatic", Kind: workflows.GateDeterministic,
			When: "false", Title: "Automatic", Questions: []any{"Continue?"},
		}}),
	}}
	launcher := newAttentionTestLauncher(t, service, &workflows.Executor{
		WorkspaceDir: workspace,
		Store:        runStore,
	}, policies)
	queue := newAttentionTriggerQueueFake(now, detail.Case.ID, detail.Case.Version)
	worker := &AttentionTriggerWorker{
		Queue: queue, Launcher: launcher, Now: func() time.Time { return now },
	}

	processed, err := worker.ProcessOne(context.Background())
	if !processed || !errors.Is(err, ErrUnavailable) || len(queue.releases) != 1 ||
		queue.releases[0].Error != ErrUnavailable.Error() ||
		!queue.releases[0].AvailableAt.Equal(now.Add(attentionTriggerRetryBase)) ||
		len(queue.pins) != 0 {
		t.Fatalf(
			"first ProcessOne()=(%v,%v) releases=%#v pins=%d",
			processed, err, queue.releases, len(queue.pins),
		)
	}
	now = now.Add(attentionTriggerRetryBase)
	queue.setNow(now)
	processed, err = worker.ProcessOne(context.Background())
	if !processed || err != nil || queue.snapshot().Status != eventing.ReviewAttentionDelivered ||
		len(queue.pins) != 1 || attentionPolicySourceCalls(policies) != 2 {
		t.Fatalf(
			"retry ProcessOne()=(%v,%v) trigger=%#v pins=%d calls=%d",
			processed, err, queue.snapshot(), len(queue.pins), attentionPolicySourceCalls(policies),
		)
	}
}

func TestAttentionTriggerWorkerRejectsStaleVersionBeforePolicyPin(t *testing.T) {
	now := serviceTestTime.Add(90 * time.Minute)
	detail := submittedAttentionTestDetail(serviceTestCaseID, 12)
	store := newAttentionTestStore(detail)
	service := newAttentionTestService(t, store, nil, nil)
	policies := &attentionTestPolicySource{snapshots: []AttentionPolicySnapshot{
		attentionTestPolicy("must-not-be-captured", nil),
	}}
	launcher := newAttentionTestLauncher(t, service, &workflows.Executor{
		WorkspaceDir: t.TempDir(),
	}, policies)
	queue := newAttentionTriggerQueueFake(now, detail.Case.ID, detail.Case.Version-1)
	worker := &AttentionTriggerWorker{
		Queue: queue, Launcher: launcher, Now: func() time.Time { return now },
	}

	processed, err := worker.ProcessOne(context.Background())
	if !processed || !errors.Is(err, workflows.ErrRunAdmissionConflict) ||
		len(queue.releases) != 1 || len(queue.pins) != 0 ||
		attentionPolicySourceCalls(policies) != 0 || len(store.linksSnapshot()) != 0 {
		t.Fatalf(
			"ProcessOne()=(%v,%v) releases=%#v pins=%d policy_calls=%d links=%d",
			processed, err, queue.releases, len(queue.pins),
			attentionPolicySourceCalls(policies), len(store.linksSnapshot()),
		)
	}
}

func TestAttentionTriggerWorkerRejectsCorruptedPinnedPolicyWithoutEffects(t *testing.T) {
	now := serviceTestTime.Add(105 * time.Minute)
	detail := submittedAttentionTestDetail(serviceTestCaseID, 12)
	store := newAttentionTestStore(detail)
	service := newAttentionTestService(t, store, nil, nil)
	workspace := t.TempDir()
	runStore := workflows.NewFileRunStore(workspace)
	policies := &attentionTestPolicySource{snapshots: []AttentionPolicySnapshot{
		{Revision: "corruption-test-generation"},
	}}
	launcher := newAttentionTestLauncher(t, service, &workflows.Executor{
		WorkspaceDir: workspace,
		Store:        runStore,
	}, policies)
	queue := newAttentionTriggerQueueFake(now, detail.Case.ID, detail.Case.Version)
	queue.completeErrors = []error{errors.New("leave exact no-op pin claimed")}
	worker := &AttentionTriggerWorker{
		Queue: queue, Launcher: launcher, Now: func() time.Time { return now },
	}
	if processed, err := worker.ProcessOne(context.Background()); !processed || err == nil {
		t.Fatalf("first ProcessOne() = (%v, %v), want ack failure", processed, err)
	}
	queue.corruptPinnedPolicy()
	now = now.Add(10 * time.Minute)
	queue.setNow(now)
	processed, err := worker.ProcessOne(context.Background())
	runs, listErr := runStore.ListRuns(context.Background())
	if !processed || !errors.Is(err, ErrUnavailable) || len(queue.releases) != 1 ||
		queue.releases[0].Error != ErrUnavailable.Error() ||
		attentionPolicySourceCalls(policies) != 1 || len(store.linksSnapshot()) != 0 ||
		listErr != nil || len(runs) != 0 {
		t.Fatalf(
			"corrupt retry=(%v,%v) releases=%#v policy_calls=%d links=%d runs=%#v list_err=%v",
			processed, err, queue.releases, attentionPolicySourceCalls(policies),
			len(store.linksSnapshot()), runs, listErr,
		)
	}
}

func TestAttentionTriggerWorkerLeaseLossDefersCompletionAndRetryFindsRun(t *testing.T) {
	now := serviceTestTime.Add(2 * time.Hour)
	detail := submittedAttentionTestDetail(serviceTestCaseID, 12)
	store := newAttentionTestStore(detail)
	service := newAttentionTestService(t, store, nil, nil)
	agent := &attentionTriggerBlockingAgent{started: make(chan struct{})}
	workspace := t.TempDir()
	runStore := workflows.NewFileRunStore(workspace)
	policies := &attentionTestPolicySource{snapshots: []AttentionPolicySnapshot{
		attentionTestPolicy("lease-loss-generation", []workflows.GateSpec{{
			ID: "isolated", Kind: workflows.GateAIIsolatedContext,
			AgentID: "reviewer", Criteria: "wait for lease loss", Title: "Wait",
		}}),
	}}
	launcher := newAttentionTestLauncher(t, service, &workflows.Executor{
		WorkspaceDir: workspace,
		Store:        runStore,
		Agents:       agent,
	}, policies)
	queue := newAttentionTriggerQueueFake(now, detail.Case.ID, detail.Case.Version)
	queue.renewErr = eventing.ErrStaleLease
	worker := &AttentionTriggerWorker{
		Queue: queue, Launcher: launcher, LeaseDuration: 20 * time.Millisecond,
		Now: func() time.Time { return now },
	}

	processed, err := worker.ProcessOne(context.Background())
	callsAfterLoss := agent.calls.Load()
	if !processed || !errors.Is(err, eventing.ErrStaleLease) ||
		len(queue.completions) != 0 || len(queue.releases) != 0 ||
		len(store.linksSnapshot()) != 1 || callsAfterLoss > 1 {
		t.Fatalf(
			"lease-loss ProcessOne()=(%v,%v) completions=%d releases=%d links=%d agent_calls=%d",
			processed, err, len(queue.completions), len(queue.releases),
			len(store.linksSnapshot()), agent.calls.Load(),
		)
	}
	queue.mu.Lock()
	queue.renewErr = nil
	queue.mu.Unlock()
	now = now.Add(10 * time.Minute)
	queue.setNow(now)
	worker.LeaseDuration = defaultAttentionTriggerLease
	processed, err = worker.ProcessOne(context.Background())
	if !processed || err != nil || queue.snapshot().Status != eventing.ReviewAttentionDelivered ||
		agent.calls.Load() != callsAfterLoss || attentionPolicySourceCalls(policies) != 1 {
		t.Fatalf(
			"reclaim ProcessOne()=(%v,%v) trigger=%#v agent_calls=%d policy_calls=%d",
			processed, err, queue.snapshot(), agent.calls.Load(), attentionPolicySourceCalls(policies),
		)
	}
}

func TestAttentionTriggerClaimValidationAndPinIdentityAreLeaseFenced(t *testing.T) {
	now := serviceTestTime.Add(3 * time.Hour)
	queue := newAttentionTriggerQueueFake(now, serviceTestCaseID, 12)
	claimed, err := queue.ClaimReviewAttentionTriggers(
		context.Background(), "worker", 1, time.Minute,
	)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = (%#v, %v)", claimed, err)
	}
	valid := claimed[0]
	if _, err := attentionLaunchRequestForTrigger(valid, now); err != nil {
		t.Fatalf("valid trigger rejected: %v", err)
	}

	missingLease := valid
	missingLease.LeaseUntil = nil
	expiredLease := valid
	expired := now
	expiredLease.LeaseUntil = &expired
	for name, trigger := range map[string]eventing.ReviewAttentionTrigger{
		"missing": missingLease,
		"expired": expiredLease,
	} {
		if _, err := attentionLaunchRequestForTrigger(trigger, now); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("%s lease validation error = %v", name, err)
		}
	}

	extended := valid
	extendedDeadline := valid.LeaseUntil.Add(time.Minute)
	extended.LeaseUntil = &extendedDeadline
	if !sameAttentionTriggerClaim(valid, extended) {
		t.Fatal("same token with heartbeat-extended deadline was rejected")
	}
	wrongAttempts := extended
	wrongAttempts.Attempts++
	regressed := valid
	regressedDeadline := valid.LeaseUntil.Add(-time.Second)
	regressed.LeaseUntil = &regressedDeadline
	if sameAttentionTriggerClaim(valid, wrongAttempts) ||
		sameAttentionTriggerClaim(valid, regressed) {
		t.Fatal("changed attempts or regressed lease deadline passed pin identity fence")
	}
}

func submittedAttentionTestDetail(caseID string, version int64) eventing.ReviewCaseDetail {
	detail := workingContextTestDetail(caseID, version)
	detail.Case.Status = eventing.ReviewCaseSubmitted
	resolved := detail.Case.UpdatedAt
	detail.Case.ResolvedAt = &resolved
	detail.Case.SubmittedAt = &resolved
	return detail
}

func attentionPolicySourceCalls(source *attentionTestPolicySource) int {
	source.mu.Lock()
	defer source.mu.Unlock()
	return source.calls
}

type attentionTriggerQueueFake struct {
	mu sync.Mutex

	now            time.Time
	trigger        eventing.ReviewAttentionTrigger
	claimCalls     int
	pins           []eventing.ReviewAttentionPolicyPin
	releases       []eventing.ReviewAttentionTriggerRelease
	completions    []eventing.ReviewAttentionTriggerCompletion
	completeErrors []error
	renewErr       error
	renewCalls     int
	onPin          func(eventing.ReviewAttentionPolicyPin)
}

func newAttentionTriggerQueueFake(
	now time.Time,
	caseID string,
	caseVersion int64,
) *attentionTriggerQueueFake {
	return &attentionTriggerQueueFake{
		now: now.UTC(),
		trigger: eventing.ReviewAttentionTrigger{
			SubmissionID:  attentionTriggerTestSubmissionID,
			CaseID:        caseID,
			CaseVersion:   caseVersion,
			DecisionPoint: eventing.ReviewAttentionDecisionSubmitted,
			Status:        eventing.ReviewAttentionPending,
			AvailableAt:   now.UTC(),
			CreatedAt:     now.UTC(),
			UpdatedAt:     now.UTC(),
		},
	}
}

func (queue *attentionTriggerQueueFake) GetReviewAttentionTrigger(
	ctx context.Context,
	submissionID string,
) (eventing.ReviewAttentionTrigger, error) {
	if err := ctx.Err(); err != nil {
		return eventing.ReviewAttentionTrigger{}, err
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if submissionID != queue.trigger.SubmissionID {
		return eventing.ReviewAttentionTrigger{}, eventing.ErrNotFound
	}
	return cloneAttentionTrigger(queue.trigger), nil
}

func (queue *attentionTriggerQueueFake) ClaimReviewAttentionTriggers(
	ctx context.Context,
	_ string,
	limit int,
	lease time.Duration,
) ([]eventing.ReviewAttentionTrigger, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if limit != 1 || lease <= 0 {
		return nil, eventing.ErrInvalidReview
	}
	if queue.trigger.Status == eventing.ReviewAttentionDelivered ||
		queue.trigger.Status == eventing.ReviewAttentionNoop ||
		queue.trigger.AvailableAt.After(queue.now) {
		return nil, nil
	}
	queue.claimCalls++
	queue.trigger.Status = eventing.ReviewAttentionClaimed
	queue.trigger.Attempts++
	queue.trigger.LeaseToken = fmt.Sprintf("lease-%d", queue.claimCalls)
	leaseUntil := queue.now.Add(lease)
	queue.trigger.LeaseUntil = &leaseUntil
	queue.trigger.UpdatedAt = queue.now
	return []eventing.ReviewAttentionTrigger{cloneAttentionTrigger(queue.trigger)}, nil
}

func (queue *attentionTriggerQueueFake) RenewReviewAttentionTriggerLease(
	ctx context.Context,
	submissionID, leaseToken string,
	lease time.Duration,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	queue.renewCalls++
	if queue.renewErr != nil {
		return queue.renewErr
	}
	if submissionID != queue.trigger.SubmissionID || leaseToken != queue.trigger.LeaseToken ||
		queue.trigger.Status != eventing.ReviewAttentionClaimed {
		return eventing.ErrStaleLease
	}
	leaseUntil := queue.now.Add(lease)
	queue.trigger.LeaseUntil = &leaseUntil
	return nil
}

func (queue *attentionTriggerQueueFake) PinReviewAttentionTriggerPolicy(
	ctx context.Context,
	input eventing.ReviewAttentionPolicyPin,
) (eventing.ReviewAttentionTrigger, error) {
	if err := ctx.Err(); err != nil {
		return eventing.ReviewAttentionTrigger{}, err
	}
	queue.mu.Lock()
	if input.SubmissionID != queue.trigger.SubmissionID ||
		input.LeaseToken != queue.trigger.LeaseToken ||
		queue.trigger.Status != eventing.ReviewAttentionClaimed {
		queue.mu.Unlock()
		return eventing.ReviewAttentionTrigger{}, eventing.ErrStaleLease
	}
	copyInput := input
	copyInput.PinnedPolicy = append([]byte(nil), input.PinnedPolicy...)
	queue.pins = append(queue.pins, copyInput)
	queue.trigger.PolicyRevision = input.PolicyRevision
	queue.trigger.PinnedPolicy = append([]byte(nil), input.PinnedPolicy...)
	queue.trigger.UpdatedAt = queue.now
	result := cloneAttentionTrigger(queue.trigger)
	onPin := queue.onPin
	queue.mu.Unlock()
	if onPin != nil {
		onPin(copyInput)
	}
	return result, nil
}

func (queue *attentionTriggerQueueFake) ReleaseReviewAttentionTrigger(
	ctx context.Context,
	input eventing.ReviewAttentionTriggerRelease,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if input.SubmissionID != queue.trigger.SubmissionID ||
		input.LeaseToken != queue.trigger.LeaseToken ||
		queue.trigger.Status != eventing.ReviewAttentionClaimed {
		return eventing.ErrStaleLease
	}
	queue.releases = append(queue.releases, input)
	queue.trigger.Status = eventing.ReviewAttentionPending
	queue.trigger.LeaseToken = ""
	queue.trigger.LeaseUntil = nil
	queue.trigger.AvailableAt = input.AvailableAt
	queue.trigger.LastError = input.Error
	queue.trigger.UpdatedAt = queue.now
	return nil
}

func (queue *attentionTriggerQueueFake) CompleteReviewAttentionTrigger(
	ctx context.Context,
	input eventing.ReviewAttentionTriggerCompletion,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if input.SubmissionID != queue.trigger.SubmissionID ||
		input.LeaseToken != queue.trigger.LeaseToken ||
		queue.trigger.Status != eventing.ReviewAttentionClaimed {
		return eventing.ErrStaleLease
	}
	queue.completions = append(queue.completions, input)
	if len(queue.completeErrors) != 0 {
		err := queue.completeErrors[0]
		queue.completeErrors = queue.completeErrors[1:]
		return err
	}
	queue.trigger.Status = input.Status
	queue.trigger.RunID = input.RunID
	queue.trigger.LeaseToken = ""
	queue.trigger.LeaseUntil = nil
	queue.trigger.LastError = ""
	completed := queue.now
	queue.trigger.CompletedAt = &completed
	queue.trigger.UpdatedAt = queue.now
	return nil
}

func (queue *attentionTriggerQueueFake) snapshot() eventing.ReviewAttentionTrigger {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	return cloneAttentionTrigger(queue.trigger)
}

func (queue *attentionTriggerQueueFake) setNow(now time.Time) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	queue.now = now.UTC()
}

func (queue *attentionTriggerQueueFake) corruptPinnedPolicy() {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.trigger.PinnedPolicy) == 0 {
		return
	}
	corrupted := append([]byte(nil), queue.trigger.PinnedPolicy...)
	for index, value := range corrupted {
		if value == '1' {
			corrupted[index] = '2'
			queue.trigger.PinnedPolicy = corrupted
			return
		}
	}
	corrupted[len(corrupted)-1] = '!'
	queue.trigger.PinnedPolicy = corrupted
}

func cloneAttentionTrigger(trigger eventing.ReviewAttentionTrigger) eventing.ReviewAttentionTrigger {
	cloned := trigger
	cloned.PinnedPolicy = append([]byte(nil), trigger.PinnedPolicy...)
	if trigger.LeaseUntil != nil {
		value := *trigger.LeaseUntil
		cloned.LeaseUntil = &value
	}
	if trigger.CompletedAt != nil {
		value := *trigger.CompletedAt
		cloned.CompletedAt = &value
	}
	return cloned
}

// Keep atomic imported for the heartbeat-loss extension below; declaring the
// shape here also ensures the worker can be exercised with an agent that exits
// only when lease loss cancels its private execution.
type attentionTriggerBlockingAgent struct {
	calls   atomic.Int32
	started chan struct{}
	once    sync.Once
}

func (agent *attentionTriggerBlockingAgent) RunAgent(
	ctx context.Context,
	_ workflows.AgentRequest,
) (map[string]any, error) {
	agent.calls.Add(1)
	agent.once.Do(func() { close(agent.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}
