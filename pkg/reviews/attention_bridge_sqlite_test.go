//go:build !mipsle && !netbsd && !(freebsd && arm)

package reviews

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestAttentionBridgeSQLiteTriggerAndFileRunRestartConverge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, submitted := newSQLiteAttentionTriggerFixture(t, ctx)
	service, err := NewService(ServiceConfig{Store: store})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	workspace := t.TempDir()
	runStore := workflows.NewFileRunStore(workspace)
	executor := &workflows.Executor{WorkspaceDir: workspace, Store: runStore}
	launcher, err := NewAttentionLauncher(AttentionLauncherConfig{
		Service:  service,
		Executor: executor,
		Policies: &attentionTestPolicySource{snapshots: []AttentionPolicySnapshot{
			attentionTestPolicy("sqlite-attention-bridge-v1", []workflows.GateSpec{
				{
					ID: "first", Kind: workflows.GateDeterministic, When: "true",
					Title: "Choose the first option", Questions: []any{"Which option?"},
				},
				{
					ID: "second", Kind: workflows.GateDeterministic, When: "true",
					Title: "Choose the second option", Questions: []any{"Proceed now?"},
				},
			}),
		}},
	})
	if err != nil {
		t.Fatalf("NewAttentionLauncher() error = %v", err)
	}
	worker := &AttentionTriggerWorker{
		Queue: store, Launcher: launcher, WorkerLabel: "attention-bridge-sqlite",
		LeaseDuration: time.Minute,
	}
	processed, err := worker.ProcessOne(ctx)
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = (%v, %v)", processed, err)
	}
	trigger, err := store.GetReviewAttentionTrigger(ctx, submitted.Submission.ID)
	if err != nil || trigger.Status != eventing.ReviewAttentionDelivered ||
		trigger.RunID == "" || trigger.PolicyRevision == "" {
		t.Fatalf("delivered trigger = (%#v, %v)", trigger, err)
	}
	link, err := store.GetReviewDecisionRun(ctx, eventing.ReviewDecisionKey{
		CaseID:         trigger.CaseID,
		CaseVersion:    trigger.CaseVersion,
		DecisionPoint:  trigger.DecisionPoint,
		PolicyRevision: trigger.PolicyRevision,
	})
	if err != nil || link.RunID != trigger.RunID {
		t.Fatalf("decision link = (%#v, %v)", link, err)
	}

	bridge, err := NewAttentionBridge(AttentionBridgeConfig{
		Service: service, Executor: executor, RunStore: runStore,
	})
	if err != nil {
		t.Fatalf("NewAttentionBridge() error = %v", err)
	}
	initial, err := bridge.Project(ctx, submitted.Case.ID)
	if err != nil || initial.Status != AttentionStatusWaiting ||
		!initial.CanRespond || len(initial.Turns) != 1 {
		t.Fatalf("initial Project() = (%#v, %v)", initial, err)
	}
	assertAttentionAuthorityInvariant(t, initial)
	firstToken := initial.Turns[0].ResponseToken
	afterFirst, err := bridge.Respond(ctx, AttentionResponseRequest{
		CaseID: submitted.Case.ID, ExpectedCaseVersion: submitted.Case.Version,
		ResponseToken: firstToken, Response: "Select the bounded option.",
	})
	if err != nil || afterFirst.Status != AttentionStatusWaiting ||
		len(afterFirst.Turns) != 2 || !afterFirst.CanRespond {
		t.Fatalf("first Respond() = (%#v, %v)", afterFirst, err)
	}
	assertAttentionAuthorityInvariant(t, afterFirst)
	secondToken := afterFirst.Turns[1].ResponseToken

	// Rebuild every runtime object around the same SQLite event store and the
	// same on-disk private run directory. The case-owned projection and replay
	// fence must not depend on process-local bridge or executor state.
	restartedService, err := NewService(ServiceConfig{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	restartedRunStore := workflows.NewFileRunStore(workspace)
	readOnlyRestart, err := NewAttentionBridge(AttentionBridgeConfig{
		Service: restartedService, RunStore: restartedRunStore,
	})
	if err != nil {
		t.Fatal(err)
	}
	readOnlyView, err := readOnlyRestart.Project(ctx, submitted.Case.ID)
	if err != nil || readOnlyView.CanRespond || len(readOnlyView.Turns) != 2 ||
		readOnlyView.Turns[1].ResponseToken != "" {
		t.Fatalf("read-only restart Project() = (%#v, %v)", readOnlyView, err)
	}
	assertAttentionAuthorityInvariant(t, readOnlyView)
	replayed, err := readOnlyRestart.Respond(ctx, AttentionResponseRequest{
		CaseID: submitted.Case.ID, ExpectedCaseVersion: submitted.Case.Version,
		ResponseToken: firstToken, Response: "Select the bounded option.",
	})
	if err != nil || !reflect.DeepEqual(replayed, readOnlyView) {
		t.Fatalf("read-only restart replay = (%#v, %v), want %#v", replayed, err, readOnlyView)
	}
	if _, err = readOnlyRestart.Respond(ctx, AttentionResponseRequest{
		CaseID: submitted.Case.ID, ExpectedCaseVersion: submitted.Case.Version,
		ResponseToken: firstToken, Response: "Select a different option.",
	}); !errors.Is(err, eventing.ErrReviewConflict) {
		t.Fatalf("read-only altered replay error = %v, want conflict", err)
	}

	restartedExecutor := &workflows.Executor{
		WorkspaceDir: workspace,
		Store:        restartedRunStore,
	}
	restartedBridge, err := NewAttentionBridge(AttentionBridgeConfig{
		Service: restartedService, Executor: restartedExecutor, RunStore: restartedRunStore,
	})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := restartedBridge.Respond(ctx, AttentionResponseRequest{
		CaseID: submitted.Case.ID, ExpectedCaseVersion: submitted.Case.Version,
		ResponseToken: secondToken, Response: "Proceed after local validation.",
	})
	if err != nil || completed.Status != AttentionStatusCompleted ||
		completed.CanRespond || len(completed.Turns) != 2 {
		t.Fatalf("restarted completion = (%#v, %v)", completed, err)
	}
	assertAttentionAuthorityInvariant(t, completed)

	terminalReadOnly, err := NewAttentionBridge(AttentionBridgeConfig{
		Service:  restartedService,
		RunStore: workflows.NewFileRunStore(workspace),
	})
	if err != nil {
		t.Fatal(err)
	}
	terminalReplay, err := terminalReadOnly.Respond(ctx, AttentionResponseRequest{
		CaseID: submitted.Case.ID, ExpectedCaseVersion: submitted.Case.Version,
		ResponseToken: secondToken, Response: "Proceed after local validation.",
	})
	if err != nil || !reflect.DeepEqual(terminalReplay, completed) {
		t.Fatalf("terminal restart replay = (%#v, %v), want %#v", terminalReplay, err, completed)
	}
}
