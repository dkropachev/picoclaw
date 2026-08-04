package reviews

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestAttentionLauncherMixedPolicyUsesExactWorkingContextAndIsIdempotent(t *testing.T) {
	detail := workingContextTestDetail(serviceTestCaseID, 12)
	store := newAttentionTestStore(detail)
	backend := newWorkingContextBackend(t)
	var runtimeAcquires atomic.Int32
	service := newAttentionTestService(t, store, backend, &runtimeAcquires)
	agent := &attentionTestAgent{
		reader: backend,
		taskByAgent: map[string]bool{
			"reviewer": false,
			"main":     true,
		},
	}
	workspace := t.TempDir()
	runStore := workflows.NewFileRunStore(workspace)
	executor := &workflows.Executor{WorkspaceDir: workspace, Store: runStore, Agents: agent}
	policy := attentionTestPolicy("source-generation/private-v1", []workflows.GateSpec{
		{
			ID: "isolated", Kind: workflows.GateAIIsolatedContext,
			AgentID: "reviewer", Criteria: "ask when evidence is incomplete",
			Title: "Complete the evidence",
		},
		{
			ID: "automatic", Kind: workflows.GateDeterministic,
			When: "false", Title: "Automatic policy",
			Questions: []any{"Confirm policy"},
		},
		{ID: "off", Kind: workflows.GateZero},
	})
	policy.Repository = &workflows.RepositoryGatePolicy{
		Mode: workflows.GatePolicyOverlay,
		Gates: []workflows.GateSpec{
			{
				ID: "discussion", Kind: workflows.GateAIWorkingContext,
				AgentID: "main", Criteria: "ask when the PR discussion needs a choice",
				Title: "Resolve the PR discussion",
			},
		},
	}
	policies := &attentionTestPolicySource{snapshots: []AttentionPolicySnapshot{policy}}
	launcher := newAttentionTestLauncher(t, service, executor, policies)
	request := AttentionLaunchRequest{
		CaseID: serviceTestCaseID, ExpectedCaseVersion: 12, DecisionPoint: "review.submitted",
	}

	result, err := launcher.Launch(context.Background(), request)
	if err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	if result.RunID == "" || result.Status != workflows.RunStatusWaiting ||
		result.Existing || result.Noop ||
		!strings.HasPrefix(result.PolicyRevision, "sha256:") {
		t.Fatalf("Launch() result = %#v", result)
	}
	if got := runtimeAcquires.Load(); got != 1 {
		t.Fatalf("runtime acquisitions = %d, want 1", got)
	}
	requests, captures := agent.observations()
	if len(requests) != 2 || len(captures) != 1 {
		t.Fatalf("agent requests=%d captures=%d, want 2 and 1", len(requests), len(captures))
	}
	if requests[0].AgentID != "reviewer" || requests[0].FrozenReadOnlySession != nil ||
		!requests[0].EphemeralSession || requests[0].Session != "" {
		t.Fatalf("isolated request = %#v", requests[0])
	}
	if requests[1].AgentID != "main" || requests[1].FrozenReadOnlySession == nil ||
		requests[1].Session != "" || requests[1].EphemeralSession {
		t.Fatalf("working request = %#v", requests[1])
	}
	capture := captures[0]
	if capture.AgentID != "main" || capture.Session == "" || capture.ExpectedRevision == "" {
		t.Fatalf("working capture = %#v", capture)
	}
	links := store.linksSnapshot()
	if len(links) != 1 || links[0].RunID != result.RunID ||
		links[0].Key.PolicyRevision != result.PolicyRevision {
		t.Fatalf("decision links = %#v", links)
	}
	persisted, err := runStore.GetRun(context.Background(), result.RunID)
	if err != nil || !workflows.IsPrivateWorkflowRun(persisted) ||
		persisted.WorkflowRef != reviewAttentionWorkflowRef || persisted.Session != "" ||
		len(persisted.Inputs) != 0 || len(persisted.Event) != 0 {
		t.Fatalf("persisted private run = (%#v, %v)", persisted, err)
	}
	tasks, err := executor.ListHumanTasks(context.Background(), result.RunID)
	if err != nil || len(tasks) != 1 || tasks[0].Status != workflows.HumanTaskStatusWaiting {
		t.Fatalf("human tasks = (%#v, %v), want one waiting", tasks, err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{
		policy.Revision,
		capture.Session,
		detail.Findings[0].Message,
		detail.Messages[0].Content,
	} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("result exposed private value %q: %s", private, encoded)
		}
	}

	// A fresh launcher and run-store object recover the durable waiting run
	// without projecting the review session or executing either model again.
	var restartAcquires atomic.Int32
	restartedService := newAttentionTestService(t, store, backend, &restartAcquires)
	restartedExecutor := &workflows.Executor{
		WorkspaceDir: workspace,
		Store:        workflows.NewFileRunStore(workspace),
		Agents:       agent,
	}
	restarted := newAttentionTestLauncher(
		t,
		restartedService,
		restartedExecutor,
		&attentionTestPolicySource{snapshots: []AttentionPolicySnapshot{policy}},
	)
	recovered, err := restarted.Launch(context.Background(), request)
	if err != nil {
		t.Fatalf("restarted Launch() error = %v", err)
	}
	if recovered.RunID != result.RunID || recovered.Status != workflows.RunStatusWaiting ||
		!recovered.Existing || recovered.PolicyRevision != result.PolicyRevision {
		t.Fatalf("restarted result = %#v, first = %#v", recovered, result)
	}
	requests, captures = agent.observations()
	if restartAcquires.Load() != 0 || len(requests) != 2 || len(captures) != 1 {
		t.Fatalf(
			"restart effects = acquires %d, requests %d, captures %d; want 0, 2, 1",
			restartAcquires.Load(), len(requests), len(captures),
		)
	}
}

func TestAttentionLauncherNoopHasNoSessionRunOrDecisionEffects(t *testing.T) {
	tests := []struct {
		name     string
		global   []workflows.GateSpec
		repo     *workflows.RepositoryGatePolicy
		wantMode workflows.GatePolicyMode
	}{
		{name: "empty"},
		{
			name: "all zero",
			global: []workflows.GateSpec{
				{ID: "global_off", Kind: workflows.GateZero},
				{ID: "another_off", Kind: workflows.GateZero},
			},
		},
		{
			name: "repository disable",
			global: []workflows.GateSpec{{
				ID: "would_ask", Kind: workflows.GateDeterministic,
				When: "true", Title: "Ask", Questions: []any{"Approve?"},
			}},
			repo: &workflows.RepositoryGatePolicy{Mode: workflows.GatePolicyDisable},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detail := workingContextTestDetail(serviceTestCaseID, 12)
			store := newAttentionTestStore(detail)
			var runtimeAcquires atomic.Int32
			service := newAttentionTestService(t, store, nil, &runtimeAcquires)
			agent := &attentionTestAgent{}
			workspace := t.TempDir()
			runStore := workflows.NewFileRunStore(workspace)
			launcher := newAttentionTestLauncher(
				t,
				service,
				&workflows.Executor{WorkspaceDir: workspace, Store: runStore, Agents: agent},
				&attentionTestPolicySource{snapshots: []AttentionPolicySnapshot{{
					Revision: "noop-generation", Global: test.global, Repository: test.repo,
				}}},
			)

			result, err := launcher.Launch(context.Background(), AttentionLaunchRequest{
				CaseID: serviceTestCaseID, ExpectedCaseVersion: 12, DecisionPoint: "review.ready",
			})
			if err != nil || !result.Noop || result.RunID != "" || result.Status != "" ||
				result.Existing {
				t.Fatalf("Launch() = (%#v, %v), want pure no-op", result, err)
			}
			runs, listErr := runStore.ListRuns(context.Background())
			requests, captures := agent.observations()
			if listErr != nil || len(runs) != 0 || len(store.linksSnapshot()) != 0 ||
				runtimeAcquires.Load() != 0 || len(requests) != 0 || len(captures) != 0 {
				t.Fatalf(
					"no-op effects runs=%d links=%d acquires=%d requests=%d captures=%d err=%v",
					len(runs), len(store.linksSnapshot()), runtimeAcquires.Load(),
					len(requests), len(captures), listErr,
				)
			}
		})
	}
}

func TestAttentionLauncherNonWorkingGatesNeverProjectSession(t *testing.T) {
	tests := []struct {
		name       string
		gate       workflows.GateSpec
		ask        bool
		wantStatus string
		wantAgents int
	}{
		{
			name: "isolated AI",
			gate: workflows.GateSpec{
				ID: "isolated", Kind: workflows.GateAIIsolatedContext,
				AgentID: "reviewer", Criteria: "ask only on ambiguity", Title: "Clarify",
			},
			wantStatus: workflows.RunStatusSucceeded,
			wantAgents: 1,
		},
		{
			name: "deterministic",
			gate: workflows.GateSpec{
				ID: "policy", Kind: workflows.GateDeterministic,
				When:  "inputs.gate_subject.case.pull_number == 42",
				Title: "Policy approval", Questions: []any{"Approve?"},
			},
			wantStatus: workflows.RunStatusWaiting,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detail := workingContextTestDetail(serviceTestCaseID, 12)
			store := newAttentionTestStore(detail)
			var runtimeAcquires atomic.Int32
			service := newAttentionTestService(t, store, nil, &runtimeAcquires)
			agent := &attentionTestAgent{taskByAgent: map[string]bool{"reviewer": test.ask}}
			workspace := t.TempDir()
			runStore := workflows.NewFileRunStore(workspace)
			launcher := newAttentionTestLauncher(
				t,
				service,
				&workflows.Executor{WorkspaceDir: workspace, Store: runStore, Agents: agent},
				&attentionTestPolicySource{snapshots: []AttentionPolicySnapshot{
					attentionTestPolicy("generation-1", []workflows.GateSpec{test.gate}),
				}},
			)

			result, err := launcher.Launch(context.Background(), AttentionLaunchRequest{
				CaseID: serviceTestCaseID, ExpectedCaseVersion: 12, DecisionPoint: "review.ready",
			})
			if err != nil || result.Status != test.wantStatus || result.RunID == "" {
				t.Fatalf("Launch() = (%#v, %v), want %q", result, err, test.wantStatus)
			}
			requests, captures := agent.observations()
			if runtimeAcquires.Load() != 0 || len(captures) != 0 ||
				len(requests) != test.wantAgents {
				t.Fatalf(
					"non-working effects acquires=%d captures=%d agents=%d",
					runtimeAcquires.Load(), len(captures), len(requests),
				)
			}
			if len(requests) == 1 && (requests[0].FrozenReadOnlySession != nil ||
				!requests[0].EphemeralSession || requests[0].Session != "") {
				t.Fatalf("isolated request = %#v", requests[0])
			}
			persisted, getErr := runStore.GetRun(context.Background(), result.RunID)
			if getErr != nil || persisted.Session != "" || len(persisted.Inputs) != 0 ||
				!workflows.IsPrivateWorkflowRun(persisted) {
				t.Fatalf("persisted run = (%#v, %v)", persisted, getErr)
			}
		})
	}
}

func TestAttentionLauncherStaleWorkingSessionFailsBeforeDecisionAdmission(t *testing.T) {
	detail := workingContextTestDetail(serviceTestCaseID, 12)
	store := newAttentionTestStore(detail)
	backend := newWorkingContextBackend(t)
	service := newAttentionTestService(t, store, backend, nil)
	agent := &attentionTestAgent{reader: backend}
	agent.beforeCapture = func(ref workflows.ReadOnlySessionRef) {
		backend.AddMessage(ref.Session, "user", "advance after projection")
	}
	workspace := t.TempDir()
	runStore := workflows.NewFileRunStore(workspace)
	launcher := newAttentionTestLauncher(
		t,
		service,
		&workflows.Executor{WorkspaceDir: workspace, Store: runStore, Agents: agent},
		&attentionTestPolicySource{snapshots: []AttentionPolicySnapshot{
			attentionTestPolicy("working-generation", []workflows.GateSpec{{
				ID: "discussion", Kind: workflows.GateAIWorkingContext,
				AgentID: "main", Criteria: "ask when blocked", Title: "Discuss",
			}}),
		}},
	)

	result, err := launcher.Launch(context.Background(), AttentionLaunchRequest{
		CaseID: serviceTestCaseID, ExpectedCaseVersion: 12, DecisionPoint: "review.ready",
	})
	if result != (AttentionLaunchResult{}) || !errors.Is(err, workflows.ErrPrivateWorkflowContext) {
		t.Fatalf("Launch() = (%#v, %v), want private-context failure", result, err)
	}
	runs, listErr := runStore.ListRuns(context.Background())
	requests, captures := agent.observations()
	if listErr != nil || len(runs) != 0 || len(store.linksSnapshot()) != 0 ||
		store.admissionCount() != 0 || len(requests) != 0 || len(captures) != 1 {
		t.Fatalf(
			"stale effects runs=%d links=%d admissions=%d requests=%d captures=%d err=%v",
			len(runs), len(store.linksSnapshot()), store.admissionCount(),
			len(requests), len(captures), listErr,
		)
	}
}

func TestAttentionLauncherFencesCaseAndPolicyAtDurableCreate(t *testing.T) {
	baseGate := workflows.GateSpec{
		ID: "policy", Kind: workflows.GateDeterministic,
		When: "false", Title: "Policy", Questions: []any{"Approve?"},
	}
	tests := []struct {
		name      string
		snapshots []AttentionPolicySnapshot
		mutate    func(*attentionTestStore)
	}{
		{
			name: "case version changes",
			snapshots: []AttentionPolicySnapshot{
				attentionTestPolicy("generation-a", []workflows.GateSpec{baseGate}),
			},
			mutate: func(store *attentionTestStore) {
				store.beforeVersionCheck = func() {
					advanced := store.reviews.detail(serviceTestCaseID)
					advanced.Case.Version++
					store.reviews.set(advanced)
				}
			},
		},
		{
			name: "source revision changes",
			snapshots: []AttentionPolicySnapshot{
				attentionTestPolicy("generation-a", []workflows.GateSpec{baseGate}),
				attentionTestPolicy("generation-b/private", []workflows.GateSpec{baseGate}),
			},
		},
		{
			name: "policy bytes change under reused source revision",
			snapshots: []AttentionPolicySnapshot{
				attentionTestPolicy("generation-reused", []workflows.GateSpec{baseGate}),
				attentionTestPolicy("generation-reused", []workflows.GateSpec{{
					ID: "policy", Kind: workflows.GateDeterministic,
					When: "true", Title: "Changed private title", Questions: []any{"Different?"},
				}}),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newAttentionTestStore(workingContextTestDetail(serviceTestCaseID, 12))
			if test.mutate != nil {
				test.mutate(store)
			}
			service := newAttentionTestService(t, store, nil, nil)
			workspace := t.TempDir()
			runStore := workflows.NewFileRunStore(workspace)
			launcher := newAttentionTestLauncher(
				t,
				service,
				&workflows.Executor{WorkspaceDir: workspace, Store: runStore},
				&attentionTestPolicySource{snapshots: test.snapshots},
			)

			result, err := launcher.Launch(context.Background(), AttentionLaunchRequest{
				CaseID: serviceTestCaseID, ExpectedCaseVersion: 12, DecisionPoint: "review.ready",
			})
			if result != (AttentionLaunchResult{}) ||
				!errors.Is(err, workflows.ErrRunAdmissionConflict) {
				t.Fatalf("Launch() = (%#v, %v), want admission conflict", result, err)
			}
			runs, listErr := runStore.ListRuns(context.Background())
			if listErr != nil || len(runs) != 0 || len(store.linksSnapshot()) != 0 {
				t.Fatalf(
					"fenced effects runs=%d links=%d err=%v",
					len(runs), len(store.linksSnapshot()), listErr,
				)
			}
		})
	}
}

func TestAttentionLauncherRejectsBaseAdmissionThatDoesNotCreate(t *testing.T) {
	store := newAttentionTestStore(workingContextTestDetail(serviceTestCaseID, 12))
	service := newAttentionTestService(t, store, nil, nil)
	workspace := t.TempDir()
	runStore := workflows.NewFileRunStore(workspace)
	executor := &workflows.Executor{
		WorkspaceDir: workspace,
		Store:        runStore,
		AdmittedRunCreate: func(
			context.Context,
			*workflows.Run,
			func() error,
		) error {
			return nil
		},
	}
	launcher := newAttentionTestLauncher(
		t,
		service,
		executor,
		&attentionTestPolicySource{snapshots: []AttentionPolicySnapshot{
			attentionTestPolicy("generation", []workflows.GateSpec{{
				ID: "policy", Kind: workflows.GateDeterministic,
				When: "false", Title: "Policy", Questions: []any{"Approve?"},
			}}),
		}},
	)

	result, err := launcher.Launch(context.Background(), AttentionLaunchRequest{
		CaseID: serviceTestCaseID, ExpectedCaseVersion: 12, DecisionPoint: "review.ready",
	})
	if result != (AttentionLaunchResult{}) ||
		!errors.Is(err, workflows.ErrRunAdmissionUnavailable) {
		t.Fatalf("Launch() = (%#v, %v), want admission unavailable", result, err)
	}
	runs, listErr := runStore.ListRuns(context.Background())
	if listErr != nil || len(runs) != 0 || len(store.linksSnapshot()) != 0 {
		t.Fatalf(
			"invalid base admission effects runs=%d links=%d err=%v",
			len(runs), len(store.linksSnapshot()), listErr,
		)
	}
}

func TestAttentionLauncherConcurrentCreateBeforeLinkCommitConverges(t *testing.T) {
	store := newAttentionTestStore(workingContextTestDetail(serviceTestCaseID, 12))
	created := make(chan struct{})
	releaseCommit := make(chan struct{})
	var blockOnce sync.Once
	store.afterCreate = func() {
		blockOnce.Do(func() {
			close(created)
			<-releaseCommit
		})
	}
	service := newAttentionTestService(t, store, nil, nil)
	agent := &attentionTestAgent{taskByAgent: map[string]bool{"reviewer": false}}
	workspace := t.TempDir()
	runStore := workflows.NewFileRunStore(workspace)
	executor := &workflows.Executor{WorkspaceDir: workspace, Store: runStore, Agents: agent}
	snapshot := attentionTestPolicy("concurrent-generation", []workflows.GateSpec{{
		ID: "isolated", Kind: workflows.GateAIIsolatedContext,
		AgentID: "reviewer", Criteria: "ask when unclear", Title: "Clarify",
	}})
	first := newAttentionTestLauncher(
		t, service, executor,
		&attentionTestPolicySource{snapshots: []AttentionPolicySnapshot{snapshot}},
	)
	second := newAttentionTestLauncher(
		t, service, executor,
		&attentionTestPolicySource{snapshots: []AttentionPolicySnapshot{snapshot}},
	)
	request := AttentionLaunchRequest{
		CaseID: serviceTestCaseID, ExpectedCaseVersion: 12, DecisionPoint: "review.ready",
	}
	type outcome struct {
		result AttentionLaunchResult
		err    error
	}
	firstDone := make(chan outcome, 1)
	secondDone := make(chan outcome, 1)
	go func() {
		result, err := first.Launch(context.Background(), request)
		firstDone <- outcome{result: result, err: err}
	}()
	select {
	case <-created:
	case <-time.After(5 * time.Second):
		t.Fatal("first launch did not persist its run before link commit")
	}
	go func() {
		result, err := second.Launch(context.Background(), request)
		secondDone <- outcome{result: result, err: err}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for store.admissionCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if store.admissionCount() < 2 {
		t.Fatal("second launch did not reach serialized decision admission")
	}
	close(releaseCommit)
	firstOutcome := <-firstDone
	secondOutcome := <-secondDone
	if firstOutcome.err != nil || secondOutcome.err != nil {
		t.Fatalf("concurrent errors = %v, %v", firstOutcome.err, secondOutcome.err)
	}
	if firstOutcome.result.RunID == "" ||
		firstOutcome.result.RunID != secondOutcome.result.RunID {
		t.Fatalf("concurrent results = %#v, %#v", firstOutcome.result, secondOutcome.result)
	}
	if firstOutcome.result.Existing == secondOutcome.result.Existing {
		t.Fatalf(
			"existing flags = %v and %v, want exactly one",
			firstOutcome.result.Existing,
			secondOutcome.result.Existing,
		)
	}
	requests, _ := agent.observations()
	links := store.linksSnapshot()
	runs, listErr := runStore.ListRuns(context.Background())
	if listErr != nil || len(requests) != 1 || len(links) != 1 || len(runs) != 1 ||
		runs[0].Status != workflows.RunStatusSucceeded {
		t.Fatalf(
			"concurrent effects requests=%d links=%d runs=%#v err=%v",
			len(requests), len(links), runs, listErr,
		)
	}
}

func TestAttentionLauncherSanitizesTrustedPolicyAndProviderDiagnostics(t *testing.T) {
	t.Run("policy source", func(t *testing.T) {
		store := newAttentionTestStore(workingContextTestDetail(serviceTestCaseID, 12))
		service := newAttentionTestService(t, store, nil, nil)
		workspace := t.TempDir()
		secret := "policy-database-password"
		launcher := newAttentionTestLauncher(
			t,
			service,
			&workflows.Executor{WorkspaceDir: workspace},
			&attentionTestPolicySource{err: errors.New(secret)},
		)
		result, err := launcher.Launch(context.Background(), AttentionLaunchRequest{
			CaseID: serviceTestCaseID, ExpectedCaseVersion: 12, DecisionPoint: "review.ready",
		})
		if result != (AttentionLaunchResult{}) || !errors.Is(err, ErrUnavailable) ||
			strings.Contains(err.Error(), secret) {
			t.Fatalf("Launch() = (%#v, %v), leaked policy diagnostic", result, err)
		}
	})

	t.Run("private provider", func(t *testing.T) {
		store := newAttentionTestStore(workingContextTestDetail(serviceTestCaseID, 12))
		service := newAttentionTestService(t, store, nil, nil)
		secret := "provider-token-and-private-prompt"
		agent := &attentionTestAgent{runErr: errors.New(secret)}
		workspace := t.TempDir()
		runStore := workflows.NewFileRunStore(workspace)
		executor := &workflows.Executor{WorkspaceDir: workspace, Store: runStore, Agents: agent}
		policy := attentionTestPolicy("private-source-revision", []workflows.GateSpec{{
			ID: "isolated", Kind: workflows.GateAIIsolatedContext,
			AgentID: "reviewer", Criteria: "private criteria", Title: "Clarify",
		}})
		launcher := newAttentionTestLauncher(
			t,
			service,
			executor,
			&attentionTestPolicySource{snapshots: []AttentionPolicySnapshot{policy}},
		)
		result, err := launcher.Launch(context.Background(), AttentionLaunchRequest{
			CaseID: serviceTestCaseID, ExpectedCaseVersion: 12, DecisionPoint: "review.ready",
		})
		if result.RunID == "" || result.Status != workflows.RunStatusFailed ||
			!errors.Is(err, workflows.ErrPrivateWorkflowFailed) ||
			strings.Contains(err.Error(), secret) {
			t.Fatalf("Launch() = (%#v, %v), want sanitized failed run", result, err)
		}
		encoded, encodeErr := json.Marshal(result)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		for _, private := range []string{secret, policy.Revision, "private criteria"} {
			if strings.Contains(string(encoded), private) {
				t.Fatalf("result exposed %q: %s", private, encoded)
			}
		}
		events, eventsErr := runStore.Events(context.Background(), result.RunID)
		if eventsErr != nil {
			t.Fatal(eventsErr)
		}
		for _, event := range events {
			if event.Message != "" || event.Payload != nil ||
				strings.Contains(fmt.Sprintf("%#v", event), secret) {
				t.Fatalf("private runtime event exposed diagnostics: %#v", event)
			}
		}
	})

	t.Run("working-context private provider", func(t *testing.T) {
		store := newAttentionTestStore(workingContextTestDetail(serviceTestCaseID, 12))
		backend := newWorkingContextBackend(t)
		service := newAttentionTestService(t, store, backend, nil)
		secret := "working-provider-token-and-private-prompt"
		agent := &attentionTestAgent{reader: backend, runErr: errors.New(secret)}
		workspace := t.TempDir()
		runStore := workflows.NewFileRunStore(workspace)
		executor := &workflows.Executor{WorkspaceDir: workspace, Store: runStore, Agents: agent}
		policy := attentionTestPolicy("working-private-source-revision", []workflows.GateSpec{{
			ID: "discussion", Kind: workflows.GateAIWorkingContext,
			AgentID: "main", Criteria: "private working criteria", Title: "Discuss",
		}})
		launcher := newAttentionTestLauncher(
			t,
			service,
			executor,
			&attentionTestPolicySource{snapshots: []AttentionPolicySnapshot{policy}},
		)

		result, err := launcher.Launch(context.Background(), AttentionLaunchRequest{
			CaseID: serviceTestCaseID, ExpectedCaseVersion: 12, DecisionPoint: "review.ready",
		})
		if result.RunID == "" || result.Status != workflows.RunStatusFailed ||
			!errors.Is(err, workflows.ErrPrivateWorkflowFailed) ||
			strings.Contains(err.Error(), secret) {
			t.Fatalf("Launch() = (%#v, %v), want sanitized failed working-context run", result, err)
		}
		encoded, encodeErr := json.Marshal(result)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		for _, private := range []string{secret, policy.Revision, "private working criteria"} {
			if strings.Contains(string(encoded), private) {
				t.Fatalf("result exposed %q: %s", private, encoded)
			}
		}
		links := store.linksSnapshot()
		persisted, getErr := runStore.GetRun(context.Background(), result.RunID)
		if getErr != nil || persisted == nil || len(links) != 1 || links[0].RunID != result.RunID ||
			persisted.Status != workflows.RunStatusFailed ||
			!workflows.IsPrivateWorkflowRun(persisted) {
			t.Fatalf("persisted failed run = (%#v, %#v, %v)", links, persisted, getErr)
		}
	})
}

type attentionTestStore struct {
	Store
	reviews *workingContextReviewStore

	admitMu sync.Mutex
	mu      sync.Mutex
	links   map[eventing.ReviewDecisionKey]eventing.ReviewDecisionRunLink

	admissions         atomic.Int32
	beforeVersionCheck func()
	afterCreate        func()
}

func newAttentionTestStore(details ...eventing.ReviewCaseDetail) *attentionTestStore {
	reviews := newWorkingContextReviewStore(details...)
	return &attentionTestStore{
		Store:   reviews,
		reviews: reviews,
		links:   make(map[eventing.ReviewDecisionKey]eventing.ReviewDecisionRunLink),
	}
}

func (store *attentionTestStore) GetReviewCase(
	ctx context.Context,
	caseID string,
) (eventing.ReviewCaseDetail, error) {
	return store.reviews.GetReviewCase(ctx, caseID)
}

func (store *attentionTestStore) GetReviewDecisionRun(
	ctx context.Context,
	key eventing.ReviewDecisionKey,
) (eventing.ReviewDecisionRunLink, error) {
	if err := ctx.Err(); err != nil {
		return eventing.ReviewDecisionRunLink{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	link, ok := store.links[key]
	if !ok {
		return eventing.ReviewDecisionRunLink{}, eventing.ErrNotFound
	}
	return link, nil
}

func (store *attentionTestStore) AdmitReviewDecisionRun(
	ctx context.Context,
	admission eventing.ReviewDecisionRunAdmission,
	create func(context.Context) error,
) (eventing.ReviewDecisionRunLink, bool, error) {
	store.admissions.Add(1)
	store.admitMu.Lock()
	defer store.admitMu.Unlock()
	if err := ctx.Err(); err != nil {
		return eventing.ReviewDecisionRunLink{}, false, err
	}
	store.mu.Lock()
	if existing, ok := store.links[admission.Key]; ok {
		store.mu.Unlock()
		if existing.RunID != admission.RunID {
			return eventing.ReviewDecisionRunLink{}, false, eventing.ErrReviewConflict
		}
		return existing, true, nil
	}
	for _, existing := range store.links {
		if existing.RunID == admission.RunID {
			store.mu.Unlock()
			return eventing.ReviewDecisionRunLink{}, false, eventing.ErrReviewConflict
		}
	}
	store.mu.Unlock()
	if store.beforeVersionCheck != nil {
		store.beforeVersionCheck()
	}
	detail := store.reviews.detail(admission.Key.CaseID)
	if detail.Case.ID == "" {
		return eventing.ReviewDecisionRunLink{}, false, eventing.ErrNotFound
	}
	if detail.Case.Version != admission.Key.CaseVersion {
		return eventing.ReviewDecisionRunLink{}, false, eventing.ErrReviewConflict
	}
	if create == nil {
		return eventing.ReviewDecisionRunLink{}, false, eventing.ErrInvalidReview
	}
	if err := create(ctx); err != nil {
		return eventing.ReviewDecisionRunLink{}, false, err
	}
	if store.afterCreate != nil {
		store.afterCreate()
	}
	link := eventing.ReviewDecisionRunLink{
		Key: admission.Key, RunID: admission.RunID, CreatedAt: time.Now().UTC(),
	}
	store.mu.Lock()
	store.links[admission.Key] = link
	store.mu.Unlock()
	return link, false, nil
}

func (store *attentionTestStore) linksSnapshot() []eventing.ReviewDecisionRunLink {
	store.mu.Lock()
	defer store.mu.Unlock()
	links := make([]eventing.ReviewDecisionRunLink, 0, len(store.links))
	for _, link := range store.links {
		links = append(links, link)
	}
	return links
}

func (store *attentionTestStore) admissionCount() int {
	return int(store.admissions.Load())
}

type attentionTestPolicySource struct {
	mu        sync.Mutex
	snapshots []AttentionPolicySnapshot
	err       error
	calls     int
	selectors []AttentionPolicySelector
}

func (source *attentionTestPolicySource) WithReviewAttentionPolicy(
	ctx context.Context,
	selector AttentionPolicySelector,
	use AttentionPolicyUse,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	source.selectors = append(source.selectors, selector)
	if source.err != nil {
		return source.err
	}
	if len(source.snapshots) == 0 {
		return errors.New("no policy snapshot")
	}
	index := source.calls
	if index >= len(source.snapshots) {
		index = len(source.snapshots) - 1
	}
	source.calls++
	return use(ctx, source.snapshots[index])
}

type attentionTestAgent struct {
	mu            sync.Mutex
	reader        session.SnapshotReader
	beforeCapture func(workflows.ReadOnlySessionRef)
	taskByAgent   map[string]bool
	runErr        error
	requests      []workflows.AgentRequest
	captures      []workflows.ReadOnlySessionRef
}

func (agent *attentionTestAgent) CaptureReadOnlySession(
	ctx context.Context,
	ref workflows.ReadOnlySessionRef,
) (*workflows.FrozenReadOnlySession, error) {
	agent.mu.Lock()
	agent.captures = append(agent.captures, ref)
	beforeCapture := agent.beforeCapture
	reader := agent.reader
	agent.mu.Unlock()
	if beforeCapture != nil {
		beforeCapture(ref)
	}
	if reader == nil {
		return nil, errors.New("session reader unavailable")
	}
	snapshot, found, err := reader.ReadSessionSnapshot(ctx, ref.Session)
	if err != nil || !found {
		return nil, errors.New("session snapshot unavailable")
	}
	return &workflows.FrozenReadOnlySession{
		AgentID:         ref.AgentID,
		Snapshot:        snapshot,
		HistoryRevision: "sha256:" + strings.Repeat("a", 64),
		FrozenMedia:     media.FrozenSet{Version: media.FrozenSetVersion},
	}, nil
}

func (agent *attentionTestAgent) RunAgent(
	_ context.Context,
	request workflows.AgentRequest,
) (map[string]any, error) {
	agent.mu.Lock()
	agent.requests = append(agent.requests, request)
	runErr := agent.runErr
	ask := agent.taskByAgent[request.AgentID]
	agent.mu.Unlock()
	if runErr != nil {
		return nil, runErr
	}
	response, err := json.Marshal(map[string]any{
		"ask_user": ask,
		"reason": func() string {
			if ask {
				return "a user choice is required"
			}
			return "the configured evidence is sufficient"
		}(),
		"questions": func() []any {
			if ask {
				return []any{"Which option should continue?"}
			}
			return []any{}
		}(),
	})
	if err != nil {
		return nil, err
	}
	structured := workflows.ValidateAgentStructuredOutput(string(response), request.Output)
	if !structured.Valid {
		return nil, fmt.Errorf("invalid test structured output: %s", structured.Error)
	}
	return map[string]any{
		"text":             string(response),
		"structured":       structured.Structured,
		"structured_json":  structured.RawJSON,
		"structured_valid": true,
	}, nil
}

func (agent *attentionTestAgent) observations() (
	[]workflows.AgentRequest,
	[]workflows.ReadOnlySessionRef,
) {
	agent.mu.Lock()
	defer agent.mu.Unlock()
	return append([]workflows.AgentRequest(nil), agent.requests...),
		append([]workflows.ReadOnlySessionRef(nil), agent.captures...)
}

func newAttentionTestService(
	t *testing.T,
	store Store,
	sessions session.SessionStore,
	acquires *atomic.Int32,
) *Service {
	t.Helper()
	config := ServiceConfig{Store: store}
	if sessions != nil {
		config.AcquireWorkingContextRuntime = func(
			ctx context.Context,
			_ string,
		) (context.Context, session.SessionStore, func(), error) {
			if acquires != nil {
				acquires.Add(1)
			}
			return ctx, sessions, func() {}, nil
		}
	} else if acquires != nil {
		config.AcquireWorkingContextRuntime = func(
			ctx context.Context,
			_ string,
		) (context.Context, session.SessionStore, func(), error) {
			acquires.Add(1)
			return ctx, nil, func() {}, errors.New("unexpected review session projection")
		}
	}
	service, err := NewService(config)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}

func newAttentionTestLauncher(
	t *testing.T,
	service *Service,
	executor *workflows.Executor,
	policies AttentionPolicySource,
) *AttentionLauncher {
	t.Helper()
	launcher, err := NewAttentionLauncher(AttentionLauncherConfig{
		Service: service, Executor: executor, Policies: policies,
	})
	if err != nil {
		t.Fatalf("NewAttentionLauncher() error = %v", err)
	}
	return launcher
}

func attentionTestPolicy(
	revision string,
	gates []workflows.GateSpec,
) AttentionPolicySnapshot {
	return AttentionPolicySnapshot{Revision: revision, Global: gates}
}

func TestAttentionPolicyRevisionAndRunIdentityAreDeterministicAndPolicyBound(t *testing.T) {
	first, err := resolveAttentionPolicy(attentionTestPolicy(
		"generation",
		[]workflows.GateSpec{{
			ID: "policy", Kind: workflows.GateDeterministic,
			When: "false", Title: "Policy", Questions: []any{"Approve?"},
		}},
	))
	if err != nil {
		t.Fatal(err)
	}
	second, err := resolveAttentionPolicy(attentionTestPolicy(
		"generation",
		[]workflows.GateSpec{{
			ID: "policy", Kind: workflows.GateDeterministic,
			When: "false", Title: "Policy", Questions: []any{"Approve?"},
		}},
	))
	if err != nil {
		t.Fatal(err)
	}
	changed, err := resolveAttentionPolicy(attentionTestPolicy(
		"generation",
		[]workflows.GateSpec{{
			ID: "policy", Kind: workflows.GateDeterministic,
			When: "true", Title: "Policy", Questions: []any{"Approve?"},
		}},
	))
	if err != nil {
		t.Fatal(err)
	}
	if first.decisionRevision != second.decisionRevision ||
		first.decisionRevision == changed.decisionRevision {
		t.Fatalf(
			"policy revisions first=%q second=%q changed=%q",
			first.decisionRevision, second.decisionRevision, changed.decisionRevision,
		)
	}
	key := eventing.ReviewDecisionKey{
		CaseID: serviceTestCaseID, CaseVersion: 12,
		DecisionPoint: "review.ready", PolicyRevision: first.decisionRevision,
	}
	firstRun, err := attentionRunID(key)
	if err != nil {
		t.Fatal(err)
	}
	secondRun, err := attentionRunID(key)
	if err != nil {
		t.Fatal(err)
	}
	changedKey := key
	changedKey.PolicyRevision = changed.decisionRevision
	changedRun, err := attentionRunID(changedKey)
	if err != nil {
		t.Fatal(err)
	}
	if firstRun != secondRun || firstRun == changedRun ||
		!strings.HasPrefix(firstRun, "wr_") || len(firstRun) != len("wr_")+32 {
		t.Fatalf("run identities first=%q second=%q changed=%q", firstRun, secondRun, changedRun)
	}
	if reflect.DeepEqual(first.resolution.Effective, changed.resolution.Effective) {
		t.Fatal("policy mutation did not change detached resolution")
	}
}
