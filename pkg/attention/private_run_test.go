package attention

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestPrivateLaunchRequestIsJSONPrivate(t *testing.T) {
	t.Parallel()

	const sentinel = "private-attention-capability"
	encoded, err := json.Marshal(PrivateLaunchRequest{
		DecisionKey: sentinel,
		Selector: PolicySelector{
			Repository:    sentinel,
			DecisionPoint: sentinel,
		},
		RevalidateLive: true,
		Subject:        map[string]any{"private": sentinel},
		ReadOnlySession: &workflows.ReadOnlySessionRef{
			AgentID:          "main",
			Session:          sentinel,
			ExpectedRevision: sentinel,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{}` || strings.Contains(string(encoded), sentinel) {
		t.Fatalf("PrivateLaunchRequest JSON = %s", encoded)
	}
}

func TestPrivateRunnerUsesLegacyRunIdentityAndConvergesOnDurableLink(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	snapshot := PolicySnapshot{
		Revision: "generation-v1",
		Global: []workflows.GateSpec{{
			ID: "policy", Kind: workflows.GateDeterministic,
			When: "inputs.gate_subject.ask == true", Title: "Policy",
			Questions: []any{"Approve?"},
		}},
	}
	var policyCalls atomic.Int32
	source := PolicySourceFunc(func(ctx context.Context, _ PolicySelector, use PolicyUse) error {
		policyCalls.Add(1)
		return use(ctx, snapshot)
	})
	prepared, err := PreparePolicy(ctx, source, PolicySelector{})
	if err != nil {
		t.Fatal(err)
	}
	keyValue := struct {
		CaseID         string `json:"case_id"`
		CaseVersion    int64  `json:"case_version"`
		DecisionPoint  string `json:"decision_point"`
		PolicyRevision string `json:"policy_revision"`
	}{"prc_0123456789abcdef0123456789abcdef", 12, "review.ready", prepared.DecisionRevision()}
	key, err := CanonicalDecisionKey(keyValue)
	if err != nil {
		t.Fatal(err)
	}
	wantRunBytes, err := json.Marshal(struct {
		Version int `json:"version"`
		Key     any `json:"key"`
	}{Version: 1, Key: keyValue})
	if err != nil {
		t.Fatal(err)
	}
	wantRunID := legacyAttentionRunID(wantRunBytes)
	if got, runErr := RunIDForDecisionKey(key); runErr != nil || got != wantRunID {
		t.Fatalf("RunIDForDecisionKey() = (%q, %v), want %q", got, runErr, wantRunID)
	}

	workspace := t.TempDir()
	runs := workflows.NewFileRunStore(workspace)
	binding := &testDecisionBinding{links: make(map[string]string)}
	var baseAdmissions atomic.Int32
	runner, err := NewPrivateRunner(PrivateRunnerConfig{
		Executor: &workflows.Executor{
			WorkspaceDir: workspace,
			Store:        runs,
			AdmittedRunCreate: func(
				_ context.Context,
				_ *workflows.Run,
				create func() error,
			) error {
				baseAdmissions.Add(1)
				return create()
			},
		},
		Runs: runs, Policies: source, Decisions: binding,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := PrivateLaunchRequest{
		DecisionKey: key,
		Policy:      prepared,
		Selector: PolicySelector{
			Repository: "acme/widgets", DecisionPoint: "review.ready",
		},
		RevalidateLive: true,
		Subject:        map[string]any{"ask": true},
	}
	first, err := runner.Launch(ctx, request)
	if err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	if first.RunID != wantRunID || first.Status != workflows.RunStatusWaiting ||
		first.Existing || first.Noop {
		t.Fatalf("first launch = %#v", first)
	}
	persisted, err := runs.GetRun(ctx, first.RunID)
	if err != nil || !ValidPrivateRun(persisted, first.RunID) ||
		persisted.WorkflowRef != WorkflowRef || !workflows.IsPrivateWorkflowRun(persisted) {
		t.Fatalf("persisted private run = (%#v, %v)", persisted, err)
	}
	second, err := runner.Launch(ctx, request)
	if err != nil || second.RunID != first.RunID || !second.Existing ||
		second.Status != first.Status {
		t.Fatalf("second launch = (%#v, %v)", second, err)
	}
	if got := baseAdmissions.Load(); got != 1 {
		t.Fatalf("base admissions = %d, want 1", got)
	}
	// One capture prepared the pin and one live capture fenced admission. Exact
	// duplicate recovery performs no policy or workflow side effect.
	if got := policyCalls.Load(); got != 2 {
		t.Fatalf("policy calls = %d, want 2", got)
	}
}

func TestPrivateRunnerQuarantinesPostCreateAdmissionFaultAndConverges(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	snapshot := PolicySnapshot{
		Revision: "post-create-fault-v1",
		Global: []workflows.GateSpec{{
			ID: "policy", Kind: workflows.GateDeterministic,
			When: "true", Title: "Policy", Questions: []any{"Approve?"},
		}},
	}
	prepared, err := PrepareSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name               string
		secondCreate       bool
		postPolicyUseError bool
	}{
		{name: "hook returns private error after create"},
		{name: "hook calls create twice", secondCreate: true},
		{name: "policy source errors after callback", postPolicyUseError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			workspace := t.TempDir()
			runs := workflows.NewFileRunStore(workspace)
			binding := &testDecisionBinding{links: make(map[string]string)}
			privateHookErr := errors.New("private base admission detail")
			source := PolicySourceFunc(func(
				ctx context.Context,
				_ PolicySelector,
				use PolicyUse,
			) error {
				if useErr := use(ctx, snapshot); useErr != nil {
					return useErr
				}
				if test.postPolicyUseError {
					return privateHookErr
				}
				return nil
			})
			var baseAdmissions atomic.Int32
			runner, runnerErr := NewPrivateRunner(PrivateRunnerConfig{
				Executor: &workflows.Executor{
					WorkspaceDir: workspace,
					Store:        runs,
					AdmittedRunCreate: func(
						_ context.Context,
						_ *workflows.Run,
						create func() error,
					) error {
						baseAdmissions.Add(1)
						if createErr := create(); createErr != nil {
							return createErr
						}
						if test.secondCreate {
							return create()
						}
						if test.postPolicyUseError {
							return nil
						}
						return privateHookErr
					},
				},
				Runs: runs, Policies: source, Decisions: binding,
			})
			if runnerErr != nil {
				t.Fatal(runnerErr)
			}
			key, keyErr := CanonicalDecisionKey(map[string]any{"decision": test.name})
			if keyErr != nil {
				t.Fatal(keyErr)
			}
			wantRunID, runIDErr := RunIDForDecisionKey(key)
			if runIDErr != nil {
				t.Fatal(runIDErr)
			}
			request := PrivateLaunchRequest{
				DecisionKey:    key,
				Policy:         prepared,
				RevalidateLive: test.postPolicyUseError,
			}

			first, firstErr := runner.Launch(ctx, request)
			if !errors.Is(firstErr, ErrPrivateRunAdmissionUncertain) ||
				errors.Is(firstErr, privateHookErr) {
				t.Fatalf("first Launch() error = %v, want safe admission uncertainty", firstErr)
			}
			if first != (PrivateLaunchResult{}) {
				t.Fatalf("first Launch() result = %#v, want private quarantine", first)
			}
			linkedRunID, found, findErr := binding.Find(ctx, key)
			if findErr != nil || !found || linkedRunID != wantRunID {
				t.Fatalf("decision link = (%q, %t, %v), want %q", linkedRunID, found, findErr, wantRunID)
			}
			listed, listErr := runs.ListRuns(ctx)
			if listErr != nil || len(listed) != 1 || listed[0].ID != wantRunID ||
				listed[0].Status != workflows.RunStatusFailed ||
				listed[0].Error != privateRunAdmissionUncertainFailure ||
				listed[0].CompletedAt == nil {
				t.Fatalf("quarantined runs = (%#v, %v)", listed, listErr)
			}
			events, eventsErr := runs.Events(ctx, wantRunID)
			if eventsErr != nil || len(events) != 0 {
				t.Fatalf("quarantined run events = (%#v, %v), want none", events, eventsErr)
			}

			second, secondErr := runner.Launch(ctx, request)
			if second != (PrivateLaunchResult{}) ||
				!errors.Is(secondErr, ErrPrivateRunAdmissionUncertain) {
				t.Fatalf(
					"second Launch() = (%#v, %v), want durable admission uncertainty",
					second,
					secondErr,
				)
			}
			if got := baseAdmissions.Load(); got != 1 {
				t.Fatalf("base admissions = %d, want 1", got)
			}
			if got := binding.admissions.Load(); got != 1 {
				t.Fatalf("decision admissions = %d, want 1", got)
			}
		})
	}
}

func TestPrivateRunnerLinkedRunningRunRemainsUncertainWhenQuarantineWriteFails(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	snapshot := PolicySnapshot{
		Revision: "quarantine-write-failure-v1",
		Global: []workflows.GateSpec{{
			ID: "policy", Kind: workflows.GateDeterministic,
			When: "true", Title: "Policy", Questions: []any{"Approve?"},
		}},
	}
	prepared, err := PrepareSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	source := PolicySourceFunc(func(ctx context.Context, _ PolicySelector, use PolicyUse) error {
		return use(ctx, snapshot)
	})
	workspace := t.TempDir()
	underlying := workflows.NewFileRunStore(workspace)
	runs := &failingAttentionUpdateRunStore{RunStore: underlying}
	runs.failUpdates.Store(1)
	binding := &testDecisionBinding{links: make(map[string]string)}
	var baseAdmissions atomic.Int32
	runner, err := NewPrivateRunner(PrivateRunnerConfig{
		Executor: &workflows.Executor{
			WorkspaceDir: workspace,
			Store:        runs,
			AdmittedRunCreate: func(
				_ context.Context,
				_ *workflows.Run,
				create func() error,
			) error {
				baseAdmissions.Add(1)
				if createErr := create(); createErr != nil {
					return createErr
				}
				return errors.New("private post-create hook failure")
			},
		},
		Runs: runs, Policies: source, Decisions: binding,
	})
	if err != nil {
		t.Fatal(err)
	}
	key, err := CanonicalDecisionKey(map[string]any{"decision": "quarantine-write-failure"})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := RunIDForDecisionKey(key)
	if err != nil {
		t.Fatal(err)
	}
	request := PrivateLaunchRequest{DecisionKey: key, Policy: prepared}

	first, firstErr := runner.Launch(ctx, request)
	if first != (PrivateLaunchResult{}) ||
		!errors.Is(firstErr, ErrPrivateRunAdmissionUncertain) {
		t.Fatalf("first Launch() = (%#v, %v)", first, firstErr)
	}
	persisted, loadErr := underlying.GetRun(ctx, runID)
	if loadErr != nil || !ValidPrivateRun(persisted, runID) ||
		persisted.Status != workflows.RunStatusRunning || persisted.CompletedAt != nil ||
		len(persisted.Jobs) != 0 || len(persisted.Steps) != 0 || len(persisted.Outputs) != 0 {
		t.Fatalf("unquarantined run = (%#v, %v)", persisted, loadErr)
	}

	second, secondErr := runner.Launch(ctx, request)
	if second != (PrivateLaunchResult{}) ||
		!errors.Is(secondErr, ErrPrivateRunAdmissionUncertain) {
		t.Fatalf("second Launch() = (%#v, %v), want durable uncertainty", second, secondErr)
	}
	if got := baseAdmissions.Load(); got != 1 {
		t.Fatalf("base admissions = %d, want 1", got)
	}
	if got := binding.admissions.Load(); got != 1 {
		t.Fatalf("decision admissions = %d, want 1", got)
	}
}

func TestPrivateRunnerQuarantinesInFlightPolicyCallbackAfterSourceReturns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	snapshot := PolicySnapshot{
		Revision: "in-flight-source-return-v1",
		Global: []workflows.GateSpec{{
			ID: "policy", Kind: workflows.GateDeterministic,
			When: "true", Title: "Policy", Questions: []any{"Approve?"},
		}},
	}
	prepared, err := PrepareSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	created := make(chan struct{})
	releaseCreate := make(chan struct{})
	sourceReturning := make(chan struct{})
	callbackDone := make(chan error, 1)
	source := PolicySourceFunc(func(ctx context.Context, _ PolicySelector, use PolicyUse) error {
		go func() { callbackDone <- use(ctx, snapshot) }()
		select {
		case <-created:
			close(sourceReturning)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	workspace := t.TempDir()
	runs := workflows.NewFileRunStore(workspace)
	binding := &testDecisionBinding{links: make(map[string]string)}
	runner, err := NewPrivateRunner(PrivateRunnerConfig{
		Executor: &workflows.Executor{
			WorkspaceDir: workspace,
			Store:        runs,
			AdmittedRunCreate: func(
				_ context.Context,
				_ *workflows.Run,
				create func() error,
			) error {
				if createErr := create(); createErr != nil {
					return createErr
				}
				close(created)
				<-releaseCreate
				return nil
			},
		},
		Runs: runs, Policies: source, Decisions: binding,
	})
	if err != nil {
		t.Fatal(err)
	}
	key, err := CanonicalDecisionKey(map[string]any{"decision": "in-flight-source-return"})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := RunIDForDecisionKey(key)
	if err != nil {
		t.Fatal(err)
	}
	type launchOutcome struct {
		result PrivateLaunchResult
		err    error
	}
	launchDone := make(chan launchOutcome, 1)
	go func() {
		result, launchErr := runner.Launch(ctx, PrivateLaunchRequest{
			DecisionKey:    key,
			Policy:         prepared,
			RevalidateLive: true,
		})
		launchDone <- launchOutcome{result: result, err: launchErr}
	}()
	select {
	case <-sourceReturning:
	case <-time.After(5 * time.Second):
		t.Fatal("policy source did not return while its callback was in flight")
	}
	// WithAttentionPolicy has returned on the launch goroutine; allow its
	// immediate guard.finish call to publish sourceReturned before the blocked
	// callback is released. This exercises the contract-violation race under
	// both ordinary and -race scheduling.
	time.Sleep(20 * time.Millisecond)
	close(releaseCreate)

	var outcome launchOutcome
	select {
	case outcome = <-launchDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Launch() did not finish after releasing the in-flight callback")
	}
	if outcome.result != (PrivateLaunchResult{}) ||
		!errors.Is(outcome.err, ErrPrivateRunAdmissionUncertain) {
		t.Fatalf("Launch() = (%#v, %v)", outcome.result, outcome.err)
	}
	select {
	case callbackErr := <-callbackDone:
		if !errors.Is(callbackErr, ErrInvalidPolicySource) {
			t.Fatalf("retained callback error = %v, want invalid source", callbackErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight policy callback did not finish")
	}
	persisted, err := runs.GetRun(ctx, runID)
	if err != nil || !ValidPrivateRun(persisted, runID) ||
		persisted.Status != workflows.RunStatusFailed ||
		persisted.Error != privateRunAdmissionUncertainFailure ||
		persisted.CompletedAt == nil {
		t.Fatalf("quarantined in-flight run = (%#v, %v)", persisted, err)
	}
	events, err := runs.Events(ctx, runID)
	if err != nil || len(events) != 0 {
		t.Fatalf("quarantined in-flight events = (%#v, %v), want none", events, err)
	}
}

func TestPrivateRunnerQuarantinesUnlinkedDeterministicRunAfterFailedReadmission(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	snapshot := PolicySnapshot{
		Revision: "unlinked-v1",
		Global: []workflows.GateSpec{{
			ID: "policy", Kind: workflows.GateDeterministic,
			When: "true", Title: "Policy", Questions: []any{"Approve?"},
		}},
	}
	prepared, err := PrepareSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	source := PolicySourceFunc(func(ctx context.Context, _ PolicySelector, use PolicyUse) error {
		return use(ctx, snapshot)
	})
	workspace := t.TempDir()
	runs := workflows.NewFileRunStore(workspace)
	binding := &testUnlinkedDecisionBinding{}
	var baseAdmissions atomic.Int32
	runner, err := NewPrivateRunner(PrivateRunnerConfig{
		Executor: &workflows.Executor{
			WorkspaceDir: workspace,
			Store:        runs,
			AdmittedRunCreate: func(
				_ context.Context,
				_ *workflows.Run,
				create func() error,
			) error {
				baseAdmissions.Add(1)
				return create()
			},
		},
		Runs: runs, Policies: source, Decisions: binding,
	})
	if err != nil {
		t.Fatal(err)
	}
	key, err := CanonicalDecisionKey(map[string]any{"decision": "unlinked"})
	if err != nil {
		t.Fatal(err)
	}
	wantRunID, err := RunIDForDecisionKey(key)
	if err != nil {
		t.Fatal(err)
	}
	request := PrivateLaunchRequest{DecisionKey: key, Policy: prepared}

	first, firstErr := runner.Launch(ctx, request)
	if !errors.Is(firstErr, ErrPrivateRunAdmissionUncertain) ||
		errors.Is(firstErr, context.Canceled) || first != (PrivateLaunchResult{}) {
		t.Fatalf("first Launch() = (%#v, %v)", first, firstErr)
	}
	persisted, loadErr := runs.GetRun(ctx, wantRunID)
	if loadErr != nil || !ValidPrivateRun(persisted, wantRunID) ||
		persisted.Status != workflows.RunStatusFailed ||
		persisted.Error != privateRunAdmissionUncertainFailure ||
		persisted.CompletedAt == nil {
		t.Fatalf("unlinked run = (%#v, %v)", persisted, loadErr)
	}
	exact, found, exactErr := runner.FindExisting(ctx, key)
	if exact != (PrivateLaunchResult{}) || found ||
		!errors.Is(exactErr, ErrPrivateRunAdmissionUncertain) {
		t.Fatalf(
			"FindExisting(unlinked) = (%#v, %t, %v), want uncertainty",
			exact,
			found,
			exactErr,
		)
	}
	second, secondErr := runner.Launch(ctx, request)
	if second != (PrivateLaunchResult{}) ||
		!errors.Is(secondErr, ErrPrivateRunAdmissionUncertain) {
		t.Fatalf("second Launch() = (%#v, %v), want orphan quarantine", second, secondErr)
	}
	if got := binding.admissions.Load(); got != 2 {
		t.Fatalf("decision admissions = %d, want 2", got)
	}
	if got := baseAdmissions.Load(); got != 2 {
		t.Fatalf("base admissions = %d, want 2", got)
	}

	zero, err := PrepareSnapshot(PolicySnapshot{
		Revision: "zero-v1",
		Global:   []workflows.GateSpec{{ID: "off", Kind: workflows.GateZero}},
	})
	if err != nil {
		t.Fatal(err)
	}
	noop, noopErr := runner.Launch(ctx, PrivateLaunchRequest{DecisionKey: key, Policy: zero})
	if noopErr != nil || !noop.Noop || noop.RunID != "" || noop.Status != "" {
		t.Fatalf("zero Launch() = (%#v, %v)", noop, noopErr)
	}
	if got := binding.admissions.Load(); got != 2 {
		t.Fatalf("zero gate changed decision admissions to %d", got)
	}
}

func TestPrivateRunnerFindExistingTreatsMissingLinkedRunAsRecovery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	workspace := t.TempDir()
	runs := workflows.NewFileRunStore(workspace)
	key, err := CanonicalDecisionKey(map[string]any{"decision": "missing-linked-run"})
	if err != nil {
		t.Fatal(err)
	}
	runID, err := RunIDForDecisionKey(key)
	if err != nil {
		t.Fatal(err)
	}
	binding := &testDecisionBinding{links: map[string]string{key: runID}}
	runner, err := NewPrivateRunner(PrivateRunnerConfig{
		Executor: &workflows.Executor{WorkspaceDir: workspace, Store: runs},
		Runs:     runs,
		Policies: PolicySourceFunc(func(context.Context, PolicySelector, PolicyUse) error {
			return ErrInvalidPolicySource
		}),
		Decisions: binding,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, found, findErr := runner.FindExisting(ctx, key)
	if result != (PrivateLaunchResult{}) || found ||
		!errors.Is(findErr, ErrPrivateRunAdmissionUncertain) {
		t.Fatalf(
			"FindExisting(missing linked run) = (%#v, %t, %v)",
			result,
			found,
			findErr,
		)
	}
}

func TestPrivateRunnerCompatibilityProjectsOnlyExactLinkedAdmissionQuarantine(
	t *testing.T,
) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		malformed bool
	}{
		{name: "exact"},
		{name: "malformed marker shape", malformed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			workspace := t.TempDir()
			baseRuns := workflows.NewFileRunStore(workspace)
			var runs workflows.RunStore = baseRuns
			if test.malformed {
				runs = &malformedAttentionQuarantineRunStore{RunStore: baseRuns}
			}
			binding := &testDecisionBinding{links: make(map[string]string)}
			prepared, err := PrepareSnapshot(PolicySnapshot{
				Revision: "linked-quarantine-compatibility-v1",
				Global: []workflows.GateSpec{{
					ID: "automatic", Kind: workflows.GateDeterministic,
					When: "false", Title: "Automatic", Questions: []any{"Continue?"},
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			runner, err := NewPrivateRunner(PrivateRunnerConfig{
				Executor: &workflows.Executor{
					WorkspaceDir: workspace,
					Store:        runs,
					AdmittedRunCreate: func(
						_ context.Context,
						_ *workflows.Run,
						create func() error,
					) error {
						if createErr := create(); createErr != nil {
							return createErr
						}
						return errors.New("post-create acknowledgement failed")
					},
				},
				Runs: runs,
				Policies: PolicySourceFunc(func(
					context.Context,
					PolicySelector,
					PolicyUse,
				) error {
					return ErrInvalidPolicySource
				}),
				Decisions:                        binding,
				ProjectLinkedAdmissionQuarantine: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			key, err := CanonicalDecisionKey(map[string]any{"decision": test.name})
			if err != nil {
				t.Fatal(err)
			}

			first, firstErr := runner.Launch(ctx, PrivateLaunchRequest{
				DecisionKey: key,
				Policy:      prepared,
			})
			if first != (PrivateLaunchResult{}) ||
				!errors.Is(firstErr, ErrPrivateRunAdmissionUncertain) {
				t.Fatalf("Launch() = (%#v, %v), want quarantine", first, firstErr)
			}
			existing, found, findErr := runner.FindExisting(ctx, key)
			if test.malformed {
				if existing != (PrivateLaunchResult{}) || found ||
					!errors.Is(findErr, ErrPrivateRunAdmissionUncertain) {
					t.Fatalf(
						"FindExisting(malformed) = (%#v, %t, %v)",
						existing,
						found,
						findErr,
					)
				}
				return
			}
			if findErr != nil || !found || !existing.Existing ||
				existing.Status != workflows.RunStatusFailed || existing.RunID == "" {
				t.Fatalf(
					"FindExisting(exact) = (%#v, %t, %v)",
					existing,
					found,
					findErr,
				)
			}
		})
	}
}

func TestPrivateRunnerQuarantinesPostCreateDecisionBindingViolation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	snapshot := PolicySnapshot{
		Revision: "invalid-binding-v1",
		Global: []workflows.GateSpec{{
			ID: "policy", Kind: workflows.GateDeterministic,
			When: "true", Title: "Policy", Questions: []any{"Approve?"},
		}},
	}
	prepared, err := PrepareSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	source := PolicySourceFunc(func(ctx context.Context, _ PolicySelector, use PolicyUse) error {
		return use(ctx, snapshot)
	})

	for _, test := range []struct {
		name    string
		existed bool
	}{
		{name: "wrong linked run"},
		{name: "existing after create", existed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			workspace := t.TempDir()
			runs := workflows.NewFileRunStore(workspace)
			binding := &testPostCreateInvalidDecisionBinding{existed: test.existed}
			runner, runnerErr := NewPrivateRunner(PrivateRunnerConfig{
				Executor: &workflows.Executor{WorkspaceDir: workspace, Store: runs},
				Runs:     runs, Policies: source, Decisions: binding,
			})
			if runnerErr != nil {
				t.Fatal(runnerErr)
			}
			key, keyErr := CanonicalDecisionKey(map[string]any{"decision": test.name})
			if keyErr != nil {
				t.Fatal(keyErr)
			}
			wantRunID, runIDErr := RunIDForDecisionKey(key)
			if runIDErr != nil {
				t.Fatal(runIDErr)
			}

			result, launchErr := runner.Launch(ctx, PrivateLaunchRequest{
				DecisionKey: key,
				Policy:      prepared,
			})
			if result != (PrivateLaunchResult{}) ||
				!errors.Is(launchErr, ErrPrivateRunAdmissionUncertain) {
				t.Fatalf("Launch() = (%#v, %v), want private quarantine", result, launchErr)
			}
			persisted, loadErr := runs.GetRun(ctx, wantRunID)
			if loadErr != nil || !ValidPrivateRun(persisted, wantRunID) ||
				persisted.Status != workflows.RunStatusFailed ||
				persisted.Error != privateRunAdmissionUncertainFailure ||
				persisted.CompletedAt == nil {
				t.Fatalf("quarantined run = (%#v, %v)", persisted, loadErr)
			}
			events, eventsErr := runs.Events(ctx, wantRunID)
			if eventsErr != nil || len(events) != 0 {
				t.Fatalf("quarantined events = (%#v, %v), want none", events, eventsErr)
			}
		})
	}
}

func TestPrivateRunnerNoopHasNoRunOrDecisionEffect(t *testing.T) {
	t.Parallel()
	snapshot := PolicySnapshot{
		Revision: "zero-v1",
		Global:   []workflows.GateSpec{{ID: "off", Kind: workflows.GateZero}},
	}
	source := PolicySourceFunc(func(ctx context.Context, _ PolicySelector, use PolicyUse) error {
		return use(ctx, snapshot)
	})
	prepared, err := PreparePolicy(context.Background(), source, PolicySelector{})
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	runs := workflows.NewFileRunStore(workspace)
	binding := &testDecisionBinding{links: make(map[string]string)}
	runner, err := NewPrivateRunner(PrivateRunnerConfig{
		Executor: &workflows.Executor{WorkspaceDir: workspace, Store: runs},
		Runs:     runs, Policies: source, Decisions: binding,
	})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := CanonicalDecisionKey(map[string]any{"decision": "zero"})
	result, err := runner.Launch(context.Background(), PrivateLaunchRequest{
		DecisionKey: key, Policy: prepared, RevalidateLive: true,
	})
	if err != nil || !result.Noop || result.RunID != "" || result.Status != "" {
		t.Fatalf("Launch(noop) = (%#v, %v)", result, err)
	}
	listed, listErr := runs.ListRuns(context.Background())
	if listErr != nil || len(listed) != 0 || binding.admissions.Load() != 0 {
		t.Fatalf("noop effects runs=%d admissions=%d err=%v", len(listed), binding.admissions.Load(), listErr)
	}
}

func TestPrivateRunnerLivePolicyChangeConflictsBeforeCreate(t *testing.T) {
	t.Parallel()
	first := PolicySnapshot{Revision: "first", Global: []workflows.GateSpec{{
		ID: "policy", Kind: workflows.GateDeterministic,
		When: "false", Title: "Policy", Questions: []any{"Approve?"},
	}}}
	changed := first
	changed.Revision = "changed"
	var calls atomic.Int32
	source := PolicySourceFunc(func(ctx context.Context, _ PolicySelector, use PolicyUse) error {
		if calls.Add(1) == 1 {
			return use(ctx, first)
		}
		return use(ctx, changed)
	})
	prepared, err := PreparePolicy(context.Background(), source, PolicySelector{})
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	runs := workflows.NewFileRunStore(workspace)
	binding := &testDecisionBinding{links: make(map[string]string)}
	runner, err := NewPrivateRunner(PrivateRunnerConfig{
		Executor: &workflows.Executor{WorkspaceDir: workspace, Store: runs},
		Runs:     runs, Policies: source, Decisions: binding,
	})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := CanonicalDecisionKey(map[string]any{"decision": "changed"})
	result, err := runner.Launch(context.Background(), PrivateLaunchRequest{
		DecisionKey: key, Policy: prepared, RevalidateLive: true,
		Subject: map[string]any{"value": true},
	})
	if result != (PrivateLaunchResult{}) ||
		!errors.Is(err, workflows.ErrRunAdmissionConflict) ||
		binding.admissions.Load() != 0 {
		t.Fatalf("Launch(changed) = (%#v, %v), admissions=%d", result, err, binding.admissions.Load())
	}
}

type testDecisionBinding struct {
	mu         sync.Mutex
	links      map[string]string
	admissions atomic.Int32
}

type failingAttentionUpdateRunStore struct {
	workflows.RunStore
	failUpdates atomic.Int32
}

type malformedAttentionQuarantineRunStore struct {
	workflows.RunStore
}

func (store *malformedAttentionQuarantineRunStore) UpdateRun(
	ctx context.Context,
	run *workflows.Run,
) error {
	if run != nil && run.Status == workflows.RunStatusFailed &&
		run.Error == privateRunAdmissionUncertainFailure {
		run.Jobs = map[string]workflows.JobExecution{"unexpected": {}}
	}
	return store.RunStore.UpdateRun(ctx, run)
}

func (store *failingAttentionUpdateRunStore) UpdateRun(
	ctx context.Context,
	run *workflows.Run,
) error {
	if store.failUpdates.Add(-1) >= 0 {
		return errors.New("injected private quarantine update failure")
	}
	return store.RunStore.UpdateRun(ctx, run)
}

type testUnlinkedDecisionBinding struct {
	admissions atomic.Int32
}

type testPostCreateInvalidDecisionBinding struct {
	existed bool
}

func (binding *testPostCreateInvalidDecisionBinding) Find(
	context.Context,
	string,
) (string, bool, error) {
	return "", false, nil
}

func (binding *testPostCreateInvalidDecisionBinding) Admit(
	ctx context.Context,
	key string,
	create func(context.Context) error,
) (string, bool, error) {
	if err := create(ctx); err != nil {
		return "", false, err
	}
	runID, err := RunIDForDecisionKey(key)
	if err != nil {
		return "", false, err
	}
	if binding.existed {
		return runID, true, nil
	}
	return "wr_" + strings.Repeat("0", 32), false, nil
}

func (binding *testUnlinkedDecisionBinding) Find(
	context.Context,
	string,
) (string, bool, error) {
	return "", false, nil
}

func (binding *testUnlinkedDecisionBinding) Admit(
	ctx context.Context,
	_ string,
	create func(context.Context) error,
) (string, bool, error) {
	binding.admissions.Add(1)
	if err := create(ctx); err != nil {
		return "", false, err
	}
	return "", false, errors.Join(ErrPrivateRunAdmissionUncertain, context.Canceled)
}

func (binding *testDecisionBinding) Find(
	_ context.Context,
	key string,
) (string, bool, error) {
	binding.mu.Lock()
	defer binding.mu.Unlock()
	runID, found := binding.links[key]
	return runID, found, nil
}

func (binding *testDecisionBinding) Admit(
	ctx context.Context,
	key string,
	create func(context.Context) error,
) (string, bool, error) {
	binding.mu.Lock()
	defer binding.mu.Unlock()
	if runID, found := binding.links[key]; found {
		return runID, true, nil
	}
	binding.admissions.Add(1)
	runID, err := RunIDForDecisionKey(key)
	if err != nil {
		return "", false, err
	}
	if err = create(ctx); err != nil {
		return "", false, err
	}
	binding.links[key] = runID
	return runID, false, nil
}

func legacyAttentionRunID(canonical []byte) string {
	digest := sha256Sum(canonical)
	return "wr_" + digest[:32]
}

func sha256Sum(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
