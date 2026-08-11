package prdevelopment

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	sharedattention "github.com/sipeed/picoclaw/pkg/attention"
	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type publicationGateProcessorFunc func(
	context.Context,
	eventing.PRDevelopmentPublication,
) (PublicationGateProcessResult, error)

func (process publicationGateProcessorFunc) ProcessClaim(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
) (PublicationGateProcessResult, error) {
	return process(ctx, claim)
}

type publicationGateExecutorFunc func(
	context.Context,
	eventing.PRDevelopmentPublication,
) (PublicationGateExecutionResult, error)

func (execute publicationGateExecutorFunc) ExecuteClaim(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
) (PublicationGateExecutionResult, error) {
	return execute(ctx, claim)
}

type publicationGateLifecycleStoreFake struct {
	publication  eventing.PRDevelopmentPublication
	renews       []eventing.PRDevelopmentPublicationRenew
	requeues     []eventing.PRDevelopmentPublicationRequeue
	waits        []eventing.PRDevelopmentPublicationGateWait
	readies      []eventing.PRDevelopmentPublicationMarkPushReady
	completions  []eventing.PRDevelopmentPublicationPrestartCompletion
	getCalls     int
	requeueErr   error
	renewErr     error
	waitErr      error
	waitApplies  bool
	readyErr     error
	getErr       error
	finishCtxErr error
}

func (store *publicationGateLifecycleStoreFake) GetPRDevelopmentPublication(
	_ context.Context,
	_ string,
) (eventing.PRDevelopmentPublication, error) {
	store.getCalls++
	return store.publication, store.getErr
}

func (store *publicationGateLifecycleStoreFake) RenewPRDevelopmentPublication(
	_ context.Context,
	input eventing.PRDevelopmentPublicationRenew,
) error {
	store.renews = append(store.renews, input)
	return store.renewErr
}

func (store *publicationGateLifecycleStoreFake) RequeuePRDevelopmentPublication(
	ctx context.Context,
	input eventing.PRDevelopmentPublicationRequeue,
) (eventing.PRDevelopmentPublication, bool, error) {
	store.finishCtxErr = ctx.Err()
	store.requeues = append(store.requeues, input)
	store.publication.Status = input.ExpectedClaimFrom
	store.publication.ClaimFrom = ""
	store.publication.ClaimOwner = ""
	store.publication.ClaimToken = ""
	store.publication.ClaimUntil = nil
	store.publication.ClaimEpoch = input.ClaimEpoch
	store.publication.AvailableAt = input.AvailableAt
	return store.publication, true, store.requeueErr
}

func (store *publicationGateLifecycleStoreFake) ReleasePRDevelopmentPublicationGateWait(
	ctx context.Context,
	input eventing.PRDevelopmentPublicationGateWait,
) (eventing.PRDevelopmentPublication, bool, error) {
	store.finishCtxErr = ctx.Err()
	store.waits = append(store.waits, input)
	if store.waitErr == nil || store.waitApplies {
		store.publication.Status = eventing.PRDevelopmentPublicationGateWaiting
		store.publication.ClaimFrom = ""
		store.publication.ClaimOwner = ""
		store.publication.ClaimToken = ""
		store.publication.ClaimUntil = nil
		store.publication.ClaimEpoch = input.ClaimEpoch
		store.publication.DecisionRunID = input.DecisionRunID
		store.publication.AvailableAt = input.AvailableAt
	}
	return store.publication, true, store.waitErr
}

func (store *publicationGateLifecycleStoreFake) MarkPRDevelopmentPublicationPushReady(
	ctx context.Context,
	input eventing.PRDevelopmentPublicationMarkPushReady,
) (eventing.PRDevelopmentPublication, bool, error) {
	store.finishCtxErr = ctx.Err()
	store.readies = append(store.readies, input)
	return store.publication, true, store.readyErr
}

func (store *publicationGateLifecycleStoreFake) CompletePRDevelopmentPublicationPrestart(
	ctx context.Context,
	input eventing.PRDevelopmentPublicationPrestartCompletion,
) (eventing.PRDevelopmentPublication, bool, error) {
	store.finishCtxErr = ctx.Err()
	store.completions = append(store.completions, input)
	return store.publication, true, nil
}

func newPublicationPendingHandlerForTest(
	t *testing.T,
	processor PublicationGateClaimProcessor,
	executor PublicationActiveGateExecutor,
	store *publicationGateLifecycleStoreFake,
	now time.Time,
) *PublicationPendingGateHandler {
	t.Helper()
	handler, err := NewPublicationPendingGateHandler(PublicationPendingGateHandlerConfig{
		Store: store, Processor: processor, Executor: executor,
		LeaseDuration: minimumPublicationGateClaimLease,
		Now:           func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewPublicationPendingGateHandler() error = %v", err)
	}
	return handler
}

func TestPublicationPendingGateHandlerRoutesRunStatuses(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC)
	tests := []struct {
		status         string
		wantWait       bool
		wantReady      bool
		wantCompletion bool
	}{
		{status: workflows.RunStatusWaiting, wantWait: true},
		{status: workflows.RunStatusSucceeded, wantReady: true},
		{status: workflows.RunStatusFailed, wantCompletion: true},
		{status: workflows.RunStatusCanceled, wantCompletion: true},
		{status: workflows.RunStatusSkipped, wantCompletion: true},
	}
	for _, test := range tests {
		t.Run(test.status, func(t *testing.T) {
			t.Parallel()
			claim := publicationGateWaitingClaimForTest(t)
			claim.ClaimFrom = eventing.PRDevelopmentPublicationPending
			runID := claim.DecisionRunID
			store := &publicationGateLifecycleStoreFake{publication: claim}
			handler := newPublicationPendingHandlerForTest(
				t,
				publicationGateProcessorFunc(func(
					context.Context,
					eventing.PRDevelopmentPublication,
				) (PublicationGateProcessResult, error) {
					return PublicationGateProcessResult{Disposition: PublicationGateRequiresExecution}, nil
				}),
				publicationGateExecutorFunc(func(
					context.Context,
					eventing.PRDevelopmentPublication,
				) (PublicationGateExecutionResult, error) {
					return PublicationGateExecutionResult{
						Publication: redactPublicationGateClaim(claim),
						RunID:       runID,
						Status:      test.status,
					}, nil
				}),
				store,
				now,
			)
			if err := handler.HandleClaim(t.Context(), claim); err != nil {
				t.Fatalf("HandleClaim() error = %v", err)
			}
			if got := len(store.waits); got != boolCount(test.wantWait) {
				t.Fatalf("wait transitions = %d", got)
			}
			if got := len(store.readies); got != boolCount(test.wantReady) {
				t.Fatalf("ready transitions = %d", got)
			}
			if got := len(store.completions); got != boolCount(test.wantCompletion) {
				t.Fatalf("terminal transitions = %d", got)
			}
			if test.wantWait {
				wantAvailable := now.Add(PublicationRetryDelay(claim.Claims))
				if store.waits[0].DecisionRunID != runID ||
					!store.waits[0].AvailableAt.Equal(wantAvailable) {
					t.Fatalf("wait transition = %#v", store.waits[0])
				}
			}
			if test.wantCompletion &&
				(store.completions[0].Status != eventing.PRDevelopmentPublicationFailed ||
					store.completions[0].ErrorCode != eventing.PRDevelopmentPublicationErrorGateFailed) {
				t.Fatalf("completion = %#v", store.completions[0])
			}
		})
	}
}

func TestPublicationPendingGateHandlerConvergesFirstRunLostWaitResponseWithNewPins(
	t *testing.T,
) {
	t.Parallel()
	now := time.Date(2026, 8, 11, 14, 30, 0, 0, time.UTC)
	durable := publicationGateWaitingClaimForTest(t)
	durable.ClaimFrom = eventing.PRDevelopmentPublicationPending
	runID := durable.DecisionRunID

	claim := clonePublicationGatePublication(durable)
	claim.DecisionRunID = ""
	claim.PolicyRevision = ""
	claim.PinnedPolicy = nil
	claim.PinnedPolicyHash = ""
	claim.SubjectRevision = ""
	claim.PinnedSubject = nil
	claim.PinnedSubjectHash = ""
	claim.ProviderObservation = eventing.PRDevelopmentPublicationProviderObservation{}
	claim.ProviderObservationJSON = nil
	claim.ProviderObservationHash = ""
	claim.ProviderPinnedAt = nil
	claim.ProviderObservedAt = nil

	resultPublication := redactPublicationGateClaim(durable)
	resultPublication.DecisionRunID = ""
	store := &publicationGateLifecycleStoreFake{
		publication: durable,
		waitErr:     errors.New("lost first-run gate-wait response"),
		waitApplies: true,
	}
	handler := newPublicationPendingHandlerForTest(
		t,
		publicationGateProcessorFunc(func(
			context.Context,
			eventing.PRDevelopmentPublication,
		) (PublicationGateProcessResult, error) {
			return PublicationGateProcessResult{Disposition: PublicationGateRequiresExecution}, nil
		}),
		publicationGateExecutorFunc(func(
			context.Context,
			eventing.PRDevelopmentPublication,
		) (PublicationGateExecutionResult, error) {
			return PublicationGateExecutionResult{
				Publication: resultPublication,
				RunID:       runID,
				Status:      workflows.RunStatusWaiting,
			}, nil
		}),
		store,
		now,
	)
	if err := handler.HandleClaim(t.Context(), claim); err != nil {
		t.Fatalf("HandleClaim() error = %v", err)
	}
	if len(store.waits) != 1 || store.getCalls != 1 || len(store.requeues) != 0 ||
		len(store.completions) != 0 {
		t.Fatalf(
			"waits/readbacks/requeues/completions = %d/%d/%d/%d, want 1/1/0/0",
			len(store.waits), store.getCalls, len(store.requeues), len(store.completions),
		)
	}
	input := store.waits[0]
	if input.PublicationID != claim.ID || input.ClaimToken != claim.ClaimToken ||
		input.ClaimEpoch != claim.ClaimEpoch || input.DecisionRunID != runID {
		t.Fatalf("wait transition authority = %#v", input)
	}
	if !samePublicationGatePins(store.publication, resultPublication) {
		t.Fatal("lost-response readback did not retain executor-authoritative pins")
	}
}

func TestPublicationPendingGateHandlerExecutesRealGateIntoDurableWait(t *testing.T) {
	t.Parallel()
	fixture := newPublicationGateExecutorFixture(t, []workflows.GateSpec{{
		ID:        "approval",
		Kind:      workflows.GateDeterministic,
		When:      "true",
		Title:     "Approve publication",
		Questions: []any{"Publish this exact locally validated candidate?"},
	}})
	lifecycle := &publicationGateLifecycleStoreFake{publication: fixture.claim}
	realExecutor := fixture.executor(t)
	handler := newPublicationPendingHandlerForTest(
		t,
		publicationGateProcessorFunc(func(
			context.Context,
			eventing.PRDevelopmentPublication,
		) (PublicationGateProcessResult, error) {
			return PublicationGateProcessResult{Disposition: PublicationGateRequiresExecution}, nil
		}),
		publicationGateExecutorFunc(func(
			ctx context.Context,
			claim eventing.PRDevelopmentPublication,
		) (PublicationGateExecutionResult, error) {
			result, err := realExecutor.ExecuteClaim(ctx, claim)
			if err == nil {
				current := result.Publication
				current.ClaimToken = claim.ClaimToken
				lifecycle.publication = current
			}
			return result, err
		}),
		lifecycle,
		fixture.claim.ClaimedAt.UTC(),
	)

	if err := handler.HandleClaim(t.Context(), fixture.claim); err != nil {
		t.Fatalf("HandleClaim() error = %v, operations=%v", err, fixture.operations())
	}
	if len(lifecycle.waits) != 1 || len(lifecycle.requeues) != 0 ||
		len(lifecycle.completions) != 0 {
		t.Fatalf(
			"waits/requeues/completions = %d/%d/%d, want 1/0/0",
			len(lifecycle.waits), len(lifecycle.requeues), len(lifecycle.completions),
		)
	}
	wait := lifecycle.waits[0]
	if lifecycle.publication.Status != eventing.PRDevelopmentPublicationGateWaiting ||
		lifecycle.publication.DecisionRunID != wait.DecisionRunID ||
		lifecycle.publication.PolicyRevision == "" ||
		lifecycle.publication.SubjectRevision == "" ||
		lifecycle.publication.ProviderObservationHash == "" {
		t.Fatalf("durable real-executor wait = %#v", lifecycle.publication)
	}
	run, err := fixture.runs.GetRun(t.Context(), wait.DecisionRunID)
	if err != nil || run.Status != workflows.RunStatusWaiting {
		t.Fatalf("durable workflow run = (%#v, %v), want waiting", run, err)
	}
}

func TestPublicationPendingGateHandlerRejectsUnboundExecutionResult(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*PublicationGateExecutionResult)
	}{
		{
			name: "missing authoritative publication",
			mutate: func(result *PublicationGateExecutionResult) {
				result.Publication = eventing.PRDevelopmentPublication{}
			},
		},
		{
			name: "run does not match pins",
			mutate: func(result *PublicationGateExecutionResult) {
				result.RunID = "wr_" + strings.Repeat("f", 32)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			claim := publicationGateWaitingClaimForTest(t)
			claim.ClaimFrom = eventing.PRDevelopmentPublicationPending
			result := PublicationGateExecutionResult{
				Publication: redactPublicationGateClaim(claim),
				RunID:       claim.DecisionRunID,
				Status:      workflows.RunStatusWaiting,
			}
			test.mutate(&result)
			store := &publicationGateLifecycleStoreFake{publication: claim}
			handler := newPublicationPendingHandlerForTest(
				t,
				publicationGateProcessorFunc(func(
					context.Context,
					eventing.PRDevelopmentPublication,
				) (PublicationGateProcessResult, error) {
					return PublicationGateProcessResult{
						Disposition: PublicationGateRequiresExecution,
					}, nil
				}),
				publicationGateExecutorFunc(func(
					context.Context,
					eventing.PRDevelopmentPublication,
				) (PublicationGateExecutionResult, error) {
					return result, nil
				}),
				store,
				time.Now(),
			)
			if err := handler.HandleClaim(t.Context(), claim); err != nil {
				t.Fatalf("HandleClaim() error = %v", err)
			}
			if len(store.waits)+len(store.readies)+len(store.requeues) != 0 ||
				len(store.completions) != 1 ||
				store.completions[0].Status != eventing.PRDevelopmentPublicationRecoveryRequired {
				t.Fatalf("unsafe result transitions = %#v", store)
			}
		})
	}
}

func TestPublicationPendingGateHandlerRequeuesTransientFailureDetached(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 11, 15, 0, 0, 0, time.UTC)
	claim := publicationDispatcherTestClaim(eventing.PRDevelopmentPublicationPending)
	claim.Claims = 4
	claim.ClaimEpoch = 4
	workErr := errors.New("runtime temporarily unavailable")
	store := &publicationGateLifecycleStoreFake{publication: claim}
	handler := newPublicationPendingHandlerForTest(
		t,
		publicationGateProcessorFunc(func(
			context.Context,
			eventing.PRDevelopmentPublication,
		) (PublicationGateProcessResult, error) {
			return PublicationGateProcessResult{}, workErr
		}),
		publicationGateExecutorFunc(func(
			context.Context,
			eventing.PRDevelopmentPublication,
		) (PublicationGateExecutionResult, error) {
			t.Fatal("executor must not run")
			return PublicationGateExecutionResult{}, nil
		}),
		store,
		now,
	)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := handler.HandleClaim(ctx, claim); !errors.Is(err, workErr) {
		t.Fatalf("HandleClaim() error = %v, want work error", err)
	}
	if len(store.requeues) != 1 || store.finishCtxErr != nil {
		t.Fatalf("requeues/context = %#v / %v", store.requeues, store.finishCtxErr)
	}
	want := now.Add(PublicationRetryDelay(claim.Claims))
	if input := store.requeues[0]; input.PublicationID != claim.ID ||
		input.ClaimToken != claim.ClaimToken || input.ClaimEpoch != claim.ClaimEpoch ||
		input.ExpectedClaimFrom != claim.ClaimFrom || !input.AvailableAt.Equal(want) {
		t.Fatalf("requeue = %#v", input)
	}
}

func TestPublicationPendingGateHandlerConvergesLostRequeueResponse(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 11, 16, 0, 0, 0, time.UTC)
	claim := publicationDispatcherTestClaim(eventing.PRDevelopmentPublicationPending)
	workErr := errors.New("transient gate error")
	store := &publicationGateLifecycleStoreFake{
		publication: claim,
		requeueErr:  errors.New("lost response"),
	}
	handler := newPublicationPendingHandlerForTest(
		t,
		publicationGateProcessorFunc(func(
			context.Context,
			eventing.PRDevelopmentPublication,
		) (PublicationGateProcessResult, error) {
			return PublicationGateProcessResult{}, workErr
		}),
		publicationGateExecutorFunc(func(
			context.Context,
			eventing.PRDevelopmentPublication,
		) (PublicationGateExecutionResult, error) {
			return PublicationGateExecutionResult{}, errors.New("unexpected executor")
		}),
		store,
		now,
	)
	if err := handler.HandleClaim(t.Context(), claim); !errors.Is(err, workErr) {
		t.Fatalf("HandleClaim() error = %v, want original work error", err)
	}
}

func TestPublicationPendingGateHandlerAdmissionUncertaintyNeedsRecovery(t *testing.T) {
	t.Parallel()
	claim := publicationDispatcherTestClaim(eventing.PRDevelopmentPublicationPending)
	store := &publicationGateLifecycleStoreFake{publication: claim}
	handler := newPublicationPendingHandlerForTest(
		t,
		publicationGateProcessorFunc(func(
			context.Context,
			eventing.PRDevelopmentPublication,
		) (PublicationGateProcessResult, error) {
			return PublicationGateProcessResult{Disposition: PublicationGateRequiresExecution}, nil
		}),
		publicationGateExecutorFunc(func(
			context.Context,
			eventing.PRDevelopmentPublication,
		) (PublicationGateExecutionResult, error) {
			return PublicationGateExecutionResult{}, sharedattention.ErrPrivateRunAdmissionUncertain
		}),
		store,
		time.Now(),
	)
	if err := handler.HandleClaim(t.Context(), claim); err != nil {
		t.Fatalf("HandleClaim() error = %v", err)
	}
	if len(store.completions) != 1 ||
		store.completions[0].Status != eventing.PRDevelopmentPublicationRecoveryRequired ||
		store.completions[0].ErrorCode != eventing.PRDevelopmentPublicationErrorRecoveryRequired {
		t.Fatalf("completion = %#v", store.completions)
	}
}

func TestPublicationPendingGateHandlerAcceptsProcessorTerminalAndZeroResults(t *testing.T) {
	t.Parallel()
	for _, disposition := range []PublicationGateProcessDisposition{
		PublicationGatePushReady,
		PublicationGateTerminal,
	} {
		t.Run(string(disposition), func(t *testing.T) {
			t.Parallel()
			claim := publicationDispatcherTestClaim(eventing.PRDevelopmentPublicationPending)
			store := &publicationGateLifecycleStoreFake{publication: claim}
			handler := newPublicationPendingHandlerForTest(
				t,
				publicationGateProcessorFunc(func(
					context.Context,
					eventing.PRDevelopmentPublication,
				) (PublicationGateProcessResult, error) {
					return PublicationGateProcessResult{Disposition: disposition}, nil
				}),
				publicationGateExecutorFunc(func(
					context.Context,
					eventing.PRDevelopmentPublication,
				) (PublicationGateExecutionResult, error) {
					t.Fatal("active executor must not run")
					return PublicationGateExecutionResult{}, nil
				}),
				store,
				time.Now(),
			)
			if err := handler.HandleClaim(t.Context(), claim); err != nil {
				t.Fatalf("HandleClaim() error = %v", err)
			}
			if len(store.requeues)+len(store.waits)+len(store.readies)+len(store.completions) != 0 {
				t.Fatal("handler repeated processor-owned transition")
			}
		})
	}
}

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}

type publicationGateRunStoreFake struct {
	run       *workflows.Run
	err       error
	getCalls  int
	beforeGet func()
}

func (*publicationGateRunStoreFake) CreateRun(context.Context, *workflows.Run) error { return nil }
func (*publicationGateRunStoreFake) UpdateRun(context.Context, *workflows.Run) error { return nil }
func (*publicationGateRunStoreFake) CancelRun(context.Context, string, string) (*workflows.Run, error) {
	return nil, nil
}

func (store *publicationGateRunStoreFake) GetRun(context.Context, string) (*workflows.Run, error) {
	store.getCalls++
	if store.beforeGet != nil {
		store.beforeGet()
	}
	return store.run, store.err
}

func (*publicationGateRunStoreFake) ListRuns(context.Context) ([]workflows.Run, error) {
	return nil, nil
}

func (*publicationGateRunStoreFake) AppendEvent(context.Context, workflows.RunEvent) error {
	return nil
}

func (*publicationGateRunStoreFake) Events(context.Context, string) ([]workflows.RunEvent, error) {
	return nil, nil
}
func (*publicationGateRunStoreFake) DeleteRun(context.Context, string) error { return nil }
func (*publicationGateRunStoreFake) PruneTerminalRuns(context.Context, time.Time) (int, error) {
	return 0, nil
}

func publicationGateWaitingClaimForTest(t *testing.T) eventing.PRDevelopmentPublication {
	t.Helper()
	fixture := newPublicationGateProcessorFixture(t, []workflows.GateSpec{
		{ID: "identity", Kind: workflows.GateZero},
		{
			ID: "quality", Kind: workflows.GateDeterministic,
			When: "true", Title: "Check quality", Questions: []any{"Ready?"},
		},
	})
	claim := fixture.store.claim()
	result, err := fixture.processor(t).ProcessClaim(t.Context(), claim)
	if err != nil || result.Disposition != PublicationGateRequiresExecution {
		t.Fatalf("prepare active policy = (%#v, %v)", result, err)
	}
	publication := fixture.store.publication
	policy, found, err := decodePublicationGatePolicy(publication)
	if err != nil || !found {
		t.Fatalf("decode policy = (%t, %v)", found, err)
	}
	_, canonical, revision, err := buildPublicationActiveSubject(
		fixture.store.snapshot,
		policy,
		[]byte(`{"format":"test-publication-model-subject/v1"}`),
	)
	if err != nil {
		t.Fatalf("build active subject: %v", err)
	}
	publication.SubjectRevision = revision
	publication.PinnedSubject = canonical
	publication.PinnedSubjectHash = strings.Repeat("a", 64)
	observedAt := time.Date(2026, 8, 11, 17, 0, 0, 0, time.UTC)
	publication.ProviderObservation = fixture.observed.Observation
	publication.ProviderObservationJSON = []byte(`{"provider":"test"}`)
	publication.ProviderObservationHash = strings.Repeat("b", 64)
	publication.ProviderPinnedAt = &observedAt
	publication.ProviderObservedAt = &observedAt
	runID, err := prDevelopmentPublicationRunID(publicationDecisionKey(publication))
	if err != nil {
		t.Fatalf("derive run ID: %v", err)
	}
	publication.DecisionRunID = runID
	publication.Status = eventing.PRDevelopmentPublicationClaimed
	publication.ClaimFrom = eventing.PRDevelopmentPublicationGateWaiting
	return publication
}

func TestPublicationGateWaitingHandlerObservesWithoutExecution(t *testing.T) {
	t.Parallel()
	for _, status := range []string{
		workflows.RunStatusWaiting,
		workflows.RunStatusRunning,
		workflows.RunStatusSucceeded,
		workflows.RunStatusFailed,
	} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			claim := publicationGateWaitingClaimForTest(t)
			store := &publicationGateLifecycleStoreFake{publication: claim}
			runs := &publicationGateRunStoreFake{run: &workflows.Run{
				ID: claim.DecisionRunID, WorkflowRef: sharedattention.WorkflowRef,
				ContextVisibility: workflows.WorkflowContextVisibilityPrivate,
				Status:            status,
			}}
			runs.beforeGet = func() {
				if len(store.renews) != 1 {
					t.Fatalf("renewals before GetRun() = %d, want 1", len(store.renews))
				}
			}
			handler, err := NewPublicationGateWaitingHandler(
				PublicationGateWaitingHandlerConfig{
					Store: store, Runs: runs,
					LeaseDuration: minimumPublicationGateClaimLease,
					Now:           func() time.Time { return claim.ClaimedAt.UTC() },
				},
			)
			if err != nil {
				t.Fatalf("NewPublicationGateWaitingHandler() error = %v", err)
			}
			if err = handler.HandleClaim(t.Context(), claim); err != nil {
				t.Fatalf("HandleClaim() error = %v", err)
			}
			switch status {
			case workflows.RunStatusWaiting, workflows.RunStatusRunning:
				if len(store.waits) != 1 {
					t.Fatalf("wait transitions = %d", len(store.waits))
				}
			case workflows.RunStatusSucceeded:
				if len(store.readies) != 1 {
					t.Fatalf("ready transitions = %d", len(store.readies))
				}
			case workflows.RunStatusFailed:
				if len(store.completions) != 1 ||
					store.completions[0].ErrorCode != eventing.PRDevelopmentPublicationErrorGateFailed {
					t.Fatalf("completion = %#v", store.completions)
				}
			}
		})
	}
}

func TestPublicationGateWaitingHandlerMalformedIdentityNeedsExactRecovery(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*eventing.PRDevelopmentPublication)
	}{
		{
			name: "missing decision run",
			mutate: func(claim *eventing.PRDevelopmentPublication) {
				claim.DecisionRunID = ""
			},
		},
		{
			name: "mismatched decision run",
			mutate: func(claim *eventing.PRDevelopmentPublication) {
				claim.DecisionRunID = "wr_" + strings.Repeat("f", 32)
			},
		},
		{
			name: "missing policy revision",
			mutate: func(claim *eventing.PRDevelopmentPublication) {
				claim.PolicyRevision = ""
			},
		},
		{
			name: "missing subject revision",
			mutate: func(claim *eventing.PRDevelopmentPublication) {
				claim.SubjectRevision = ""
			},
		},
		{
			name: "missing provider observation",
			mutate: func(claim *eventing.PRDevelopmentPublication) {
				claim.ProviderObservationHash = ""
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			claim := publicationGateWaitingClaimForTest(t)
			test.mutate(&claim)
			store := &publicationGateLifecycleStoreFake{publication: claim}
			runs := &publicationGateRunStoreFake{}
			handler, err := NewPublicationGateWaitingHandler(
				PublicationGateWaitingHandlerConfig{
					Store: store, Runs: runs,
					LeaseDuration: minimumPublicationGateClaimLease,
				},
			)
			if err != nil {
				t.Fatalf("NewPublicationGateWaitingHandler() error = %v", err)
			}
			if err = handler.HandleClaim(t.Context(), claim); err != nil {
				t.Fatalf("HandleClaim() error = %v", err)
			}
			if len(store.renews) != 1 || runs.getCalls != 0 || store.getCalls != 0 {
				t.Fatalf(
					"renewals/run reads/publication reads = %d/%d/%d, want 1/0/0",
					len(store.renews), runs.getCalls, store.getCalls,
				)
			}
			if len(store.completions) != 1 {
				t.Fatalf("recovery transitions = %d, want 1", len(store.completions))
			}
			completion := store.completions[0]
			if completion.PublicationID != claim.ID ||
				completion.ClaimToken != claim.ClaimToken ||
				completion.ClaimEpoch != claim.ClaimEpoch ||
				completion.Status != eventing.PRDevelopmentPublicationRecoveryRequired ||
				completion.ErrorCode != eventing.PRDevelopmentPublicationErrorRecoveryRequired {
				t.Fatalf("recovery transition = %#v", completion)
			}
		})
	}
}

func TestPublicationGateWaitingHandlerStaleLeaseStopsWithoutEffects(t *testing.T) {
	t.Parallel()
	claim := publicationGateWaitingClaimForTest(t)
	store := &publicationGateLifecycleStoreFake{
		publication: claim,
		renewErr:    eventing.ErrStaleLease,
	}
	runs := &publicationGateRunStoreFake{}
	handler, err := NewPublicationGateWaitingHandler(
		PublicationGateWaitingHandlerConfig{
			Store: store, Runs: runs,
			LeaseDuration: minimumPublicationGateClaimLease,
		},
	)
	if err != nil {
		t.Fatalf("NewPublicationGateWaitingHandler() error = %v", err)
	}
	if err = handler.HandleClaim(t.Context(), claim); !errors.Is(err, eventing.ErrStaleLease) {
		t.Fatalf("HandleClaim() error = %v, want stale lease", err)
	}
	if len(store.renews) != 1 || runs.getCalls != 0 || store.getCalls != 0 {
		t.Fatalf(
			"renewals/run reads/publication reads = %d/%d/%d, want 1/0/0",
			len(store.renews), runs.getCalls, store.getCalls,
		)
	}
	if len(store.requeues)+len(store.waits)+len(store.readies)+len(store.completions) != 0 {
		t.Fatal("stale claim caused a scheduling transition")
	}
}

func TestPublicationGateWaitingHandlerConvergesLostWaitReleaseResponse(t *testing.T) {
	t.Parallel()
	claim := publicationGateWaitingClaimForTest(t)
	store := &publicationGateLifecycleStoreFake{
		publication: claim,
		waitErr:     errors.New("lost gate-wait release response"),
		waitApplies: true,
	}
	runs := &publicationGateRunStoreFake{run: &workflows.Run{
		ID: claim.DecisionRunID, WorkflowRef: sharedattention.WorkflowRef,
		ContextVisibility: workflows.WorkflowContextVisibilityPrivate,
		Status:            workflows.RunStatusWaiting,
	}}
	handler, err := NewPublicationGateWaitingHandler(
		PublicationGateWaitingHandlerConfig{
			Store: store, Runs: runs,
			LeaseDuration: minimumPublicationGateClaimLease,
			Now:           func() time.Time { return claim.ClaimedAt.UTC() },
		},
	)
	if err != nil {
		t.Fatalf("NewPublicationGateWaitingHandler() error = %v", err)
	}
	if err = handler.HandleClaim(t.Context(), claim); err != nil {
		t.Fatalf("HandleClaim() error = %v", err)
	}
	if len(store.waits) != 1 || store.getCalls != 1 || len(store.requeues) != 0 {
		t.Fatalf(
			"waits/readbacks/requeues = %d/%d/%d, want 1/1/0",
			len(store.waits), store.getCalls, len(store.requeues),
		)
	}
	wantAvailable := claim.ClaimedAt.UTC().Add(PublicationRetryDelay(claim.Claims))
	if store.publication.Status != eventing.PRDevelopmentPublicationGateWaiting ||
		store.publication.DecisionRunID != claim.DecisionRunID ||
		!store.publication.AvailableAt.Equal(wantAvailable) {
		t.Fatalf("durable wait state = %#v", store.publication)
	}
}

func TestPublicationGateWaitingHandlerClassifiesTransitionConflicts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		status     string
		storeErr   error
		wantStatus eventing.PRDevelopmentPublicationStatus
		wantCode   eventing.PRDevelopmentPublicationErrorCode
	}{
		{
			name: "waiting superseded", status: workflows.RunStatusWaiting,
			storeErr:   eventing.ErrPRDevelopmentPublicationSuperseded,
			wantStatus: eventing.PRDevelopmentPublicationSuperseded,
			wantCode:   eventing.PRDevelopmentPublicationErrorSuperseded,
		},
		{
			name: "waiting conflict", status: workflows.RunStatusWaiting,
			storeErr:   eventing.ErrPRDevelopmentPublicationConflict,
			wantStatus: eventing.PRDevelopmentPublicationConflict,
			wantCode:   eventing.PRDevelopmentPublicationErrorLocalEvidence,
		},
		{
			name: "running superseded", status: workflows.RunStatusRunning,
			storeErr:   eventing.ErrPRDevelopmentPublicationSuperseded,
			wantStatus: eventing.PRDevelopmentPublicationSuperseded,
			wantCode:   eventing.PRDevelopmentPublicationErrorSuperseded,
		},
		{
			name: "running conflict", status: workflows.RunStatusRunning,
			storeErr:   eventing.ErrPRDevelopmentPublicationConflict,
			wantStatus: eventing.PRDevelopmentPublicationConflict,
			wantCode:   eventing.PRDevelopmentPublicationErrorLocalEvidence,
		},
		{
			name: "succeeded superseded", status: workflows.RunStatusSucceeded,
			storeErr:   eventing.ErrPRDevelopmentPublicationSuperseded,
			wantStatus: eventing.PRDevelopmentPublicationSuperseded,
			wantCode:   eventing.PRDevelopmentPublicationErrorSuperseded,
		},
		{
			name: "succeeded conflict", status: workflows.RunStatusSucceeded,
			storeErr:   eventing.ErrPRDevelopmentPublicationConflict,
			wantStatus: eventing.PRDevelopmentPublicationConflict,
			wantCode:   eventing.PRDevelopmentPublicationErrorLocalEvidence,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			claim := publicationGateWaitingClaimForTest(t)
			store := &publicationGateLifecycleStoreFake{publication: claim}
			if test.status == workflows.RunStatusSucceeded {
				store.readyErr = test.storeErr
			} else {
				store.waitErr = test.storeErr
			}
			runs := &publicationGateRunStoreFake{run: &workflows.Run{
				ID: claim.DecisionRunID, WorkflowRef: sharedattention.WorkflowRef,
				ContextVisibility: workflows.WorkflowContextVisibilityPrivate,
				Status:            test.status,
			}}
			handler, err := NewPublicationGateWaitingHandler(
				PublicationGateWaitingHandlerConfig{
					Store: store, Runs: runs,
					LeaseDuration: minimumPublicationGateClaimLease,
				},
			)
			if err != nil {
				t.Fatalf("NewPublicationGateWaitingHandler() error = %v", err)
			}
			if err = handler.HandleClaim(t.Context(), claim); err != nil {
				t.Fatalf("HandleClaim() error = %v", err)
			}
			wantWaits := boolCount(test.status != workflows.RunStatusSucceeded)
			wantReadies := boolCount(test.status == workflows.RunStatusSucceeded)
			if len(store.waits) != wantWaits || len(store.readies) != wantReadies {
				t.Fatalf(
					"wait/ready transitions = %d/%d, want %d/%d",
					len(store.waits), len(store.readies), wantWaits, wantReadies,
				)
			}
			if len(store.requeues) != 0 || len(store.completions) != 1 {
				t.Fatalf(
					"requeues/completions = %d/%d, want 0/1",
					len(store.requeues), len(store.completions),
				)
			}
			completion := store.completions[0]
			if completion.Status != test.wantStatus || completion.ErrorCode != test.wantCode {
				t.Fatalf("completion = %#v", completion)
			}
		})
	}
}

func TestPublicationGateHandlerTypesArePrivateAndRequireSafeLease(t *testing.T) {
	t.Parallel()
	for _, value := range []any{
		PublicationPendingGateHandlerConfig{},
		PublicationGateWaitingHandlerConfig{},
	} {
		raw, err := json.Marshal(value)
		if err != nil || string(raw) != `{}` {
			t.Fatalf("json.Marshal(%T) = %s, %v, want {}", value, raw, err)
		}
	}
	store := &publicationGateLifecycleStoreFake{}
	processor := publicationGateProcessorFunc(func(
		context.Context,
		eventing.PRDevelopmentPublication,
	) (PublicationGateProcessResult, error) {
		return PublicationGateProcessResult{}, nil
	})
	executor := publicationGateExecutorFunc(func(
		context.Context,
		eventing.PRDevelopmentPublication,
	) (PublicationGateExecutionResult, error) {
		return PublicationGateExecutionResult{}, nil
	})
	if handler, err := NewPublicationPendingGateHandler(PublicationPendingGateHandlerConfig{
		Store: store, Processor: processor, Executor: executor,
		LeaseDuration: minimumPublicationGateClaimLease - time.Nanosecond,
	}); handler != nil || !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unsafe lease constructor = (%v, %v)", handler, err)
	}
}
