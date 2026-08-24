package prworkspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/workflows"
	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

type prGateV3Agent struct {
	reader      session.SnapshotReader
	fieldValues map[string]any
	captures    []workflows.ReadOnlySessionRef
	requests    []workflows.AgentRequest
}

func (agent *prGateV3Agent) CaptureReadOnlySession(
	ctx context.Context,
	ref workflows.ReadOnlySessionRef,
) (*workflows.FrozenReadOnlySession, error) {
	agent.captures = append(agent.captures, ref)
	if agent.reader == nil {
		return nil, errors.New("session reader is unavailable")
	}
	snapshot, found, err := agent.reader.ReadSessionSnapshot(ctx, ref.Session)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("gate session was not found")
	}
	if ref.ExpectedRevision != "" && snapshot.Revision != ref.ExpectedRevision {
		return nil, errors.New("gate session revision changed before capture")
	}
	return &workflows.FrozenReadOnlySession{
		AgentID: ref.AgentID, Snapshot: snapshot,
		HistoryRevision: "sha256:pr-gate-v3-history",
		FrozenMedia:     media.FrozenSet{Version: media.FrozenSetVersion},
	}, nil
}

func (agent *prGateV3Agent) RunAgent(
	_ context.Context,
	request workflows.AgentRequest,
) (map[string]any, error) {
	agent.requests = append(agent.requests, request)
	return map[string]any{
		"structured": map[string]any{"field-values": cloneAnyMap(agent.fieldValues)},
	}, nil
}

func TestWorkflowGateEvaluatorPresentsGenericHumanFormAndReturnsFieldValues(t *testing.T) {
	workspace := t.TempDir()
	executor := &workflows.Executor{
		WorkspaceDir: workspace,
		Store:        workflows.NewFileRunStore(workspace),
	}
	evaluator := &WorkflowGateEvaluator{Config: config.DefaultPRLifecycleConfig(), Executor: executor}
	request := testPRLifecycleGateRequest("pr.charter.confirm", map[string]any{
		"charter": map[string]any{"type": "feature", "goal": "Add gate forms"},
	})
	gate, err := evaluator.Start(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if gate.State != ExecutionWaitingUser || gate.runtime == nil || gate.runtime.WorkflowConfigurationID != "default" ||
		gate.runtime.WorkflowRunID == "" || len(gate.Turns) != 1 {
		t.Fatalf("started gate = %#v", gate)
	}
	turn := gate.Turns[0]
	if turn.GateForm == nil || turn.GateForm.GateRef != "gates.charter-confirm" ||
		turn.ActorKind != "human" || turn.ExecutionID == "" || turn.ActionRevision == "" ||
		turn.InputHash == "" || len(turn.GateForm.Fields) != 2 {
		t.Fatalf("generic Human turn = %#v", turn)
	}
	reconciledStart, err := evaluator.Start(t.Context(), request)
	if err != nil || reconciledStart.ID != gate.ID || reconciledStart.runtime == nil ||
		reconciledStart.runtime.WorkflowRunID != gate.runtime.WorkflowRunID {
		t.Fatalf("reconciled start = %#v, error = %v", reconciledStart, err)
	}
	runs, err := executor.Store.ListRuns(t.Context())
	if err != nil || len(runs) != 1 {
		t.Fatalf("outer gate runs after replay = %#v, error = %v", runs, err)
	}

	fieldValues := map[string]any{"action": "approve", "explanation": "Scope is explicit."}
	completed, err := evaluator.Respond(t.Context(), gate, fieldValues)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != ExecutionSucceeded || !gateCompletedWith(completed, "approve") ||
		completed.Turns[0].FieldValues["action"] != "approve" ||
		completed.Turns[0].ActorKind != "human" {
		t.Fatalf("completed gate = %#v", completed)
	}
	// A repeated delivery reconciles the durable terminal workflow rather than
	// requiring an impossible second Human response.
	reconciled, err := evaluator.Respond(t.Context(), gate, fieldValues)
	if err != nil || !gateCompletedWith(reconciled, "approve") {
		t.Fatalf("reconciled gate = %#v, error = %v", reconciled, err)
	}
}

func TestWorkflowGateEvaluatorAppliesExactRepositoryActionOverride(t *testing.T) {
	configured := config.DefaultPRLifecycleConfig()
	action := gatetypes.GateAction{
		Type: gatetypes.GateActionDeterministic,
		Fields: map[string]any{
			"action": "revise", "explanation": "Configured policy requires revision.",
		},
	}
	configured.WorkflowConfigurations["automatic"] = config.PRLifecycleWorkflowConfiguration{
		Name:           "Automatic",
		DeferredIssues: config.PRLifecycleDeferredIssueConfig{Mode: config.PRLifecycleDeferredIssuesAsk},
		Bindings: []config.PRLifecycleGateBinding{{
			WorkflowRef: PRLifecycleWorkflowRef,
			GateRef:     "gates.charter-confirm",
			Action:      &action,
		}},
	}
	configured.RepositoryAssignments["https://github.com|repo-v3"] = "automatic"
	if err := configured.Validate(); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	evaluator := &WorkflowGateEvaluator{
		Config: configured,
		Executor: &workflows.Executor{
			WorkspaceDir: workspace,
			Store:        workflows.NewFileRunStore(workspace),
		},
	}
	gate, err := evaluator.Start(t.Context(), testPRLifecycleGateRequest(
		"pr.charter.confirm",
		map[string]any{"charter": map[string]any{"type": "fix"}},
	))
	if err != nil {
		t.Fatal(err)
	}
	if gate.State != ExecutionSucceeded || !gateCompletedWith(gate, "revise") ||
		gate.runtime.WorkflowConfigurationID != "automatic" ||
		len(gate.Turns) != 1 ||
		gate.Turns[0].ActorKind != "deterministic" ||
		gate.Turns[0].FieldValues["action"] != "revise" {
		t.Fatalf("automatic gate = %#v", gate)
	}
}

func TestWorkflowGateEvaluatorUsesDedicatedHardScopeForm(t *testing.T) {
	workspace := t.TempDir()
	evaluator := &WorkflowGateEvaluator{
		Config: config.DefaultPRLifecycleConfig(),
		Executor: &workflows.Executor{
			WorkspaceDir: workspace,
			Store:        workflows.NewFileRunStore(workspace),
		},
	}
	request := testPRLifecycleGateRequest("pr.implementation.hard-scope", map[string]any{
		"scope": map[string]any{"distance": "S3_unrelated"},
	})
	gate, err := evaluator.Start(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	form := gate.Turns[0].GateForm
	if form == nil || form.GateRef != prLifecycleHardScopeGateRef || len(form.Fields) == 0 {
		t.Fatalf("hard-scope form = %#v", form)
	}
	for _, option := range form.Fields[0].Options {
		if option.ID == "approve" {
			t.Fatal("hard-scope form exposed forbidden approve option")
		}
	}
}

func TestWorkflowGateEvaluatorUsesExactOriginatingSessionForSourceAI(t *testing.T) {
	backend, closeStore := newGateWorkingContextTestBackend(t, t.TempDir())
	defer closeStore()
	contextRequest := testGateWorkingContextRequest("main")
	source := &AIExecutionSource{
		ExecutionID: "aix_11111111111111111111111111111111",
		WorkspaceID: contextRequest.WorkspaceID, Binding: "sha256:source-binding",
		AgentID: "main", Tools: workflows.AgentToolsNone,
	}
	source.Session = aiExecutionSourceSessionKey(source)
	scope := aiExecutionSourceSessionScope(source)
	if _, err := backend.AdmitSessionScope(t.Context(), session.SessionScopeAdmission{
		Key: source.Session, Scope: session.CloneScope(&scope), Mode: session.ScopeAdmissionReview,
	}); err != nil {
		t.Fatal(err)
	}
	reserved, found, readErr := backend.ReadSessionSnapshot(t.Context(), source.Session)
	if readErr != nil || !found {
		t.Fatalf("reserved source session = %#v, found=%v, error=%v", reserved, found, readErr)
	}
	if err := backend.ReplaceSessionSnapshot(t.Context(), session.SessionSnapshotReplacement{
		Key: source.Session,
		History: []providers.Message{
			{Role: "user", Content: "Review the exact source change."},
			{Role: "assistant", Content: `{"findings":[{"title":"Source finding"}]}`},
		},
		Scope: session.CloneScope(&scope), ExpectedRevision: reserved.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	persisted, found, readErr := backend.ReadSessionSnapshot(t.Context(), source.Session)
	if readErr != nil || !found || !reflect.DeepEqual(persisted.Scope, &scope) {
		t.Fatalf("persisted source session = %#v, found=%v, error=%v", persisted, found, readErr)
	}
	source.SessionRevision = persisted.Revision
	finding := Finding{
		ID:          "pfn_11111111111111111111111111111111",
		Fingerprint: "sha256:finding",
		Origin:      FindingOriginReview,
		Severity:    "high",
		Title:       "Source finding",
		Message:     "Check it",
		Scope: ScopeAssessment{
			Distance:       ScopeNecessaryAdjacent,
			Size:           ChangeSizeS,
			Presence:       WorkCandidatePresent,
			TypeCompatible: true,
			Confidence:     1,
		},
		Disposition:     FindingOpen,
		SourceAvailable: true,
		source:          source,
	}
	action := gatetypes.GateAction{
		Type: gatetypes.GateActionAI, Session: workflows.AgentSessionSource,
		Prompt: "Reassess the source finding.",
	}
	configured := config.DefaultPRLifecycleConfig()
	configured.WorkflowConfigurations["source"] = config.PRLifecycleWorkflowConfiguration{
		Name:           "Source",
		DeferredIssues: config.PRLifecycleDeferredIssueConfig{Mode: config.PRLifecycleDeferredIssuesAsk},
		Bindings: []config.PRLifecycleGateBinding{{
			WorkflowRef: PRLifecycleWorkflowRef, GateRef: "gates.finding-classify", Action: &action,
		}},
	}
	configured.DefaultWorkflowConfigurationID = "source"
	agent := &prGateV3Agent{reader: backend, fieldValues: map[string]any{
		"action": "keep-in-pr", "explanation": "Confirmed from originating context.",
	}}
	workspace := t.TempDir()
	evaluator := &WorkflowGateEvaluator{
		Config: configured,
		Executor: &workflows.Executor{
			WorkspaceDir: workspace, Store: workflows.NewFileRunStore(workspace), Agents: agent,
		},
	}
	request := testPRLifecycleGateRequest("pr.finding.classify", map[string]any{"finding": finding})
	request.WorkspaceID = contextRequest.WorkspaceID
	gate, err := evaluator.Start(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !gateCompletedWith(gate, "keep-in-pr") || len(agent.captures) != 1 ||
		agent.captures[0].AgentID != source.AgentID ||
		agent.captures[0].Session != source.Session ||
		agent.captures[0].ExpectedRevision != source.SessionRevision ||
		len(agent.requests) != 1 || agent.requests[0].FrozenReadOnlySession == nil ||
		agent.requests[0].Tools != workflows.AgentToolsNone {
		t.Fatalf("source gate = %#v captures=%#v requests=%#v", gate, agent.captures, agent.requests)
	}
}

func TestWorkflowGateEvaluatorProjectsEveryNestedWorkflowHumanTurn(t *testing.T) {
	workspace := t.TempDir()
	actionsDir := filepath.Join(workspace, "workflows", "actions")
	if err := os.MkdirAll(actionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	child := `
name: Multi-step gate action
gates:
  confirm:
    prompt: Confirm the first check.
    fields:
      - id: confirmed
        type: boolean
        label: Confirmed
        required: true
    default-action: {type: human}
  confirm-again:
    prompt: Confirm the second check.
    fields:
      - id: confirmed
        type: boolean
        label: Confirmed
        required: true
    default-action: {type: human}
  result:
    prompt: Produce the application result.
    fields:
      - id: action
        type: select
        label: Action
        min-selections: 1
        max-selections: 1
        options: [{id: approve, label: Approve}]
      - id: explanation
        type: long-text
        label: Explanation
    default-action:
      type: deterministic
      fields: {action: approve, explanation: Checks completed.}
on:
  workflow_call:
    outputs:
      field-values:
        value: ${{ jobs.decide.outputs.field-values }}
jobs:
  decide:
    runs-on: picoclaw
    outputs:
      field-values: ${{ steps.result.outputs.field-values }}
    steps:
      - {id: first, uses: gate/exec, with: {gate-ref: gates.confirm}}
      - {id: second, uses: gate/exec, with: {gate-ref: gates.confirm-again}}
      - {id: result, uses: gate/exec, with: {gate-ref: gates.result}}
`
	if err := os.WriteFile(filepath.Join(actionsDir, "multi.yml"), []byte(child), 0o600); err != nil {
		t.Fatal(err)
	}
	action := gatetypes.GateAction{
		Type: gatetypes.GateActionWorkflow, WorkflowRef: "workflows/actions/multi.yml",
	}
	configured := config.DefaultPRLifecycleConfig()
	configured.WorkflowConfigurations["multi"] = config.PRLifecycleWorkflowConfiguration{
		Name:           "Multi-step",
		DeferredIssues: config.PRLifecycleDeferredIssueConfig{Mode: config.PRLifecycleDeferredIssuesAsk},
		Bindings: []config.PRLifecycleGateBinding{{
			WorkflowRef: PRLifecycleWorkflowRef, GateRef: "gates.charter-confirm", Action: &action,
		}},
	}
	configured.DefaultWorkflowConfigurationID = "multi"
	executor := &workflows.Executor{WorkspaceDir: workspace, Store: workflows.NewFileRunStore(workspace)}
	evaluator := &WorkflowGateEvaluator{Config: configured, Executor: executor}
	gate, err := evaluator.Start(t.Context(), testPRLifecycleGateRequest(
		"pr.charter.confirm", map[string]any{"charter": map[string]any{"type": "feature"}},
	))
	if err != nil || gate.State != ExecutionWaitingUser || len(gate.Turns) != 1 ||
		gate.Turns[0].GateForm == nil || gate.Turns[0].GateForm.GateRef != "gates.confirm" {
		t.Fatalf("first nested form = %#v, error = %v", gate, err)
	}
	gate, err = evaluator.Respond(t.Context(), gate, map[string]any{"confirmed": true})
	if err != nil || gate.State != ExecutionWaitingUser || len(gate.Turns) != 2 ||
		gate.Turns[0].FieldValues["confirmed"] != true ||
		gate.Turns[1].GateForm == nil || gate.Turns[1].GateForm.GateRef != "gates.confirm-again" {
		t.Fatalf("second nested form = %#v, error = %v", gate, err)
	}
	gate, err = evaluator.Respond(t.Context(), gate, map[string]any{"confirmed": true})
	if err != nil || gate.State != ExecutionSucceeded || len(gate.Turns) != 3 ||
		gate.Turns[1].FieldValues["confirmed"] != true || gateAction(gate) != "approve" ||
		gate.Turns[2].ActorKind != "workflow" || gate.Turns[2].ActionRevision == "" {
		t.Fatalf("nested workflow result = %#v, error = %v", gate, err)
	}
}

func TestWorkflowGateEvaluatorRejectsInvalidIdentityAndFieldValues(t *testing.T) {
	workspace := t.TempDir()
	evaluator := &WorkflowGateEvaluator{
		Config: config.DefaultPRLifecycleConfig(),
		Executor: &workflows.Executor{
			WorkspaceDir: workspace,
			Store:        workflows.NewFileRunStore(workspace),
		},
	}
	invalid := testPRLifecycleGateRequest("pr.unknown", map[string]any{"subject": true})
	if _, err := evaluator.Start(t.Context(), invalid); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown decision point error = %v", err)
	}
	waiting, err := evaluator.Start(
		t.Context(),
		testPRLifecycleGateRequest("pr.charter.confirm", map[string]any{"subject": true}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evaluator.Respond(
		t.Context(), waiting, map[string]any{"action": "not-an-option"},
	); !errors.Is(err, workflows.ErrHumanTaskResponseInvalid) {
		t.Fatalf("invalid field-values error = %v", err)
	}
}

func testPRLifecycleGateRequest(decisionPoint string, subject map[string]any) GateRequest {
	digest, _ := fingerprintValue(subject)
	return GateRequest{
		WorkspaceID: "devw_11111111111111111111111111111111", WorkspaceVersion: 7,
		ProviderOrigin: "https://github.com", RepositoryID: "repo-v3",
		DecisionPoint: decisionPoint, Subject: subject, SubjectDigest: digest,
	}
}
