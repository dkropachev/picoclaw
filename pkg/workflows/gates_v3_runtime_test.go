package workflows

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type staticGateActionResolver struct {
	resolution GateActionResolution
	err        error
	requests   []GateActionResolveRequest
}

type failParentGateWaitStore struct {
	*FileRunStore
	parentRunID string
	fail        bool
}

func (store *failParentGateWaitStore) UpdateRun(ctx context.Context, run *Run) error {
	if store.fail && run != nil && run.ID == store.parentRunID && run.Status == RunStatusWaiting {
		store.fail = false
		return errors.New("injected parent wait persistence failure")
	}
	return store.FileRunStore.UpdateRun(ctx, run)
}

func (r *staticGateActionResolver) ResolveGateAction(
	_ context.Context,
	req GateActionResolveRequest,
) (GateActionResolution, error) {
	r.requests = append(r.requests, req)
	return r.resolution, r.err
}

func testGateV3Workflow(defaultAction *GateAction) *Workflow {
	return &Workflow{
		Name: "Test gate",
		On:   WorkflowTriggers{Manual: map[string]any{}},
		Gates: map[string]GateDefinition{
			"decision": {
				Prompt: "Choose what happens.",
				Fields: []GateField{
					{
						ID: "action", Type: GateFieldSelect, Label: "Action",
						MinSelections: 1, MaxSelections: 1,
						Options: []GateFieldOption{
							{ID: "approve", Label: "Approve"},
							{ID: "revise", Label: "Revise"},
						},
					},
					{ID: "explanation", Type: GateFieldLongText, Label: "Explanation"},
					{
						ID: "areas", Type: GateFieldSelect, Label: "Areas",
						MinSelections: 0, MaxSelections: 2,
						Options: []GateFieldOption{
							{ID: "code", Label: "Code"},
							{ID: "tests", Label: "Tests"},
						},
					},
				},
				DefaultAction: defaultAction,
			},
		},
		Jobs: map[string]Job{
			"main": {
				RunsOn: "picoclaw",
				Steps: []Step{{
					ID: "decide", Uses: GateExecUses,
					With: map[string]any{"gate-ref": "gates.decision"},
				}},
			},
		},
	}
}

func TestGateExecHumanSuspendsPinsFormAndResumesWithNormalizedOutputs(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	executor := &Executor{WorkspaceDir: workspace, Store: store}
	workflow := testGateV3Workflow(&GateAction{Type: GateActionHuman})

	started, err := executor.Run(ctx, RunRequest{Workflow: workflow, WorkflowRef: "workflows/test.yml"})
	if err != nil || started.Status != RunStatusWaiting {
		t.Fatalf("Run() = (%#v, %v), want waiting", started, err)
	}
	tasks, err := executor.ListHumanTasks(ctx, started.RunID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("ListHumanTasks() = (%#v, %v)", tasks, err)
	}
	task := tasks[0]
	if task.GateForm == nil || task.GateForm.GateRef != "gates.decision" ||
		len(task.GateForm.Fields) != 3 || task.ActorKind != GateActorHuman ||
		task.ExecutionID == "" || task.ActionRevision == "" {
		t.Fatalf("Human task = %#v", task)
	}
	response := map[string]any{
		"field-values": map[string]any{
			"action":      "revise",
			"explanation": "Clarify scope.",
			"areas":       []any{"code", "tests"},
		},
	}
	resumed, err := executor.ResumeHumanTask(ctx, started.RunID, task.ID, HumanTaskResumeRequest{
		ExpectedRevision: task.Revision,
		InputHash:        task.InputHash,
		ResponseID:       "response-1",
		Response:         response,
	})
	if err != nil || resumed.Status != RunStatusSucceeded {
		t.Fatalf("ResumeHumanTask() = (%#v, %v)", resumed, err)
	}
	run, err := store.GetRun(ctx, started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	outputs := run.Steps["main/decide"].Outputs
	if outputs["actor-kind"] != GateActorHuman || outputs["execution-id"] != task.ExecutionID ||
		outputs["action-revision"] != task.ActionRevision || outputs["input-hash"] != task.InputHash {
		t.Fatalf("gate outputs = %#v", outputs)
	}
	if !reflect.DeepEqual(outputs["field-values"], map[string]any{
		"action": "revise", "explanation": "Clarify scope.", "areas": []any{"code", "tests"},
	}) {
		t.Fatalf("field-values = %#v", outputs["field-values"])
	}
}

func TestGateExecHumanRejectsUnknownInvalidAndDuplicateValues(t *testing.T) {
	for _, test := range []struct {
		name   string
		values map[string]any
	}{
		{name: "missing required", values: map[string]any{}},
		{name: "unknown field", values: map[string]any{"action": "approve", "extra": true}},
		{name: "unknown option", values: map[string]any{"action": "other"}},
		{name: "duplicate multi option", values: map[string]any{
			"action": "approve", "areas": []any{"code", "code"},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			workspace := t.TempDir()
			executor := &Executor{WorkspaceDir: workspace}
			started, err := executor.Run(ctx, RunRequest{
				Workflow: testGateV3Workflow(&GateAction{Type: GateActionHuman}),
			})
			if err != nil {
				t.Fatal(err)
			}
			tasks, _ := executor.ListHumanTasks(ctx, started.RunID)
			task := tasks[0]
			_, err = executor.ResumeHumanTask(ctx, started.RunID, task.ID, HumanTaskResumeRequest{
				ExpectedRevision: task.Revision, InputHash: task.InputHash,
				ResponseID: "response", Response: map[string]any{"field-values": test.values},
			})
			if !errors.Is(err, ErrHumanTaskResponseInvalid) {
				t.Fatalf("ResumeHumanTask() error = %v", err)
			}
		})
	}
}

func TestGateExecResolverOverrideIsAtomicAndPinned(t *testing.T) {
	resolver := &staticGateActionResolver{resolution: GateActionResolution{
		Action: &GateAction{Type: GateActionDeterministic, Fields: map[string]any{
			"action": "revise", "explanation": "configured", "areas": []any{"tests"},
		}},
		Revision: "config:7",
	}}
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	executor := &Executor{WorkspaceDir: workspace, Store: store, GateActions: resolver}
	result, err := executor.Run(context.Background(), RunRequest{
		Workflow:    testGateV3Workflow(&GateAction{Type: GateActionHuman}),
		WorkflowRef: "workflows/pr-lifecycle.yml",
	})
	if err != nil || result.Status != RunStatusSucceeded {
		t.Fatalf("Run() = (%#v, %v)", result, err)
	}
	if len(resolver.requests) != 1 {
		t.Fatalf("resolver requests = %#v", resolver.requests)
	}
	request := resolver.requests[0]
	if request.WorkflowRef != "workflows/pr-lifecycle.yml" || request.GateRef != "gates.decision" ||
		request.DefaultAction == nil || request.DefaultAction.Type != GateActionHuman ||
		request.WorkflowRevision == "" {
		t.Fatalf("resolver request = %#v", request)
	}
	run, _ := store.GetRun(context.Background(), result.RunID)
	outputs := run.Steps["main/decide"].Outputs
	if outputs["actor-kind"] != GateActorDeterministic || outputs["action-revision"] != "config:7" {
		t.Fatalf("outputs = %#v", outputs)
	}
}

func TestGateExecAIUsesStrictGateSchemaAndValidatesResponse(t *testing.T) {
	agents := &fakeAgentRunner{outputs: map[string]any{
		"structured": map[string]any{
			"field-values": map[string]any{"action": "approve", "areas": []any{}},
		},
	}}
	workflow := testGateV3Workflow(&GateAction{
		Type: GateActionAI, AgentID: "reviewer", Prompt: "Decide.",
		Session: AgentSessionEphemeral, History: "none", Cache: "none", Tools: AgentToolsNone,
	})
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	executor := &Executor{WorkspaceDir: workspace, Store: store, Agents: agents}
	result, err := executor.Run(context.Background(), RunRequest{Workflow: workflow})
	if err != nil || result.Status != RunStatusSucceeded {
		t.Fatalf("Run() = (%#v, %v)", result, err)
	}
	if len(agents.requests) != 1 || agents.requests[0].Output == nil ||
		agents.requests[0].Output.Format != "json" || agents.requests[0].Output.Schema == nil {
		t.Fatalf("agent requests = %#v", agents.requests)
	}
	run, _ := store.GetRun(context.Background(), result.RunID)
	outputs := run.Steps["main/decide"].Outputs
	if outputs["actor-kind"] != GateActorAI ||
		!reflect.DeepEqual(outputs["field-values"], map[string]any{"action": "approve", "areas": []any{}}) {
		t.Fatalf("outputs = %#v", outputs)
	}
}

func TestGateExecWorkflowActionConsumesFieldValuesOutput(t *testing.T) {
	workspace := t.TempDir()
	definitions := filepath.Join(workspace, "workflows", "actions")
	if err := os.MkdirAll(definitions, 0o755); err != nil {
		t.Fatal(err)
	}
	child := `
name: Gate action
on:
  workflow_call:
    outputs:
      field-values:
        value: ${{ jobs.decide.outputs.field-values }}
jobs:
  decide:
    runs-on: picoclaw
    outputs:
      field-values: ${{ steps.answer.outputs.field-values }}
    steps:
      - id: answer
        uses: function/answer
`
	if err := os.WriteFile(filepath.Join(definitions, "decide.yml"), []byte(child), 0o600); err != nil {
		t.Fatal(err)
	}
	functions := NewFunctionRegistry()
	if err := functions.Register("answer", func(
		_ context.Context, _ map[string]any, _ ExecutionContext,
	) (map[string]any, error) {
		return map[string]any{"field-values": map[string]any{"action": "approve"}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	store := NewFileRunStore(workspace)
	executor := &Executor{WorkspaceDir: workspace, Store: store, Functions: functions}
	workflow := testGateV3Workflow(&GateAction{
		Type: GateActionWorkflow, WorkflowRef: "workflows/actions/decide.yml",
	})
	result, err := executor.Run(context.Background(), RunRequest{Workflow: workflow})
	if err != nil || result.Status != RunStatusSucceeded {
		t.Fatalf("Run() = (%#v, %v)", result, err)
	}
	run, _ := store.GetRun(context.Background(), result.RunID)
	outputs := run.Steps["main/decide"].Outputs
	if outputs["actor-kind"] != GateActorWorkflow ||
		!reflect.DeepEqual(outputs["field-values"], map[string]any{"action": "approve"}) ||
		len(run.ChildRunIDs) != 1 {
		t.Fatalf("run = %#v outputs = %#v", run, outputs)
	}
}

func TestWorkflowActionRevisionBindsPinnedChildContentAndDuplicateReconciliation(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	definitions := filepath.Join(workspace, "workflows", "actions")
	if err := os.MkdirAll(definitions, 0o755); err != nil {
		t.Fatal(err)
	}
	childPath := filepath.Join(definitions, "revision.yml")
	writeChild := func(action string) {
		t.Helper()
		definition := fmt.Sprintf(`
name: Revision-bound action
gates:
  result:
    prompt: Return the configured action.
    fields:
      - id: action
        type: select
        label: Action
        min-selections: 1
        max-selections: 1
        options:
          - id: approve
            label: Approve
          - id: revise
            label: Revise
    default-action:
      type: deterministic
      fields:
        action: %s
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
      - id: result
        uses: gate/exec
        with:
          gate-ref: gates.result
`, action)
		if err := os.WriteFile(childPath, []byte(definition), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeChild("approve")
	workflow := testGateV3Workflow(&GateAction{
		Type: GateActionWorkflow, WorkflowRef: "workflows/actions/revision.yml",
	})
	compiled, compileErr := CompileGateWorkflowV3(
		workflow, "gates.decision", map[string]any{"gate-subject": map[string]any{"id": "one"}},
	)
	if compileErr != nil {
		t.Fatal(compileErr)
	}
	store := NewFileRunStore(workspace)
	executor := &Executor{WorkspaceDir: workspace, Store: store}
	run := func(runID string) (*RunResult, *Run, error) {
		result, runErr := executor.Run(ctx, RunRequest{
			RunID: runID, Workflow: compiled.Workflow, WorkflowRef: "workflows/pr-lifecycle.yml",
			PrivateRoot: compiled.PrivateRoot,
		})
		persisted, readErr := store.GetRun(ctx, runID)
		if readErr != nil && runErr == nil {
			runErr = readErr
		}
		return result, persisted, runErr
	}
	firstResult, firstRun, err := run("wr_action_revision_content_v1")
	if err != nil || firstResult.Status != RunStatusSucceeded {
		t.Fatalf("v1 Run() = (%#v, %v)", firstResult, err)
	}
	firstStep := firstRun.Steps[workflowGateJobID+"/gate-exec"]
	firstRevision, _ := firstStep.Outputs["action-revision"].(string)
	if firstRevision == "" || firstStep.Outputs["field-values"].(map[string]any)["action"] != "approve" {
		t.Fatalf("v1 outputs = %#v", firstStep.Outputs)
	}

	writeChild("revise")
	secondResult, secondRun, err := run("wr_action_revision_content_v2")
	if err != nil || secondResult.Status != RunStatusSucceeded {
		t.Fatalf("v2 Run() = (%#v, %v)", secondResult, err)
	}
	secondStep := secondRun.Steps[workflowGateJobID+"/gate-exec"]
	secondRevision, _ := secondStep.Outputs["action-revision"].(string)
	if secondRevision == "" || secondRevision == firstRevision ||
		secondStep.Outputs["field-values"].(map[string]any)["action"] != "revise" {
		t.Fatalf("v2 outputs = %#v, v1 revision=%q", secondStep.Outputs, firstRevision)
	}

	_, _, err = run("wr_action_revision_content_v1")
	if !errors.Is(err, ErrRunAdmissionConflict) {
		t.Fatalf("content-changed duplicate Run() error = %v, want ErrRunAdmissionConflict", err)
	}
}

func TestCompileGateWorkflowV3PreservesCatalogAndPrivateRoot(t *testing.T) {
	catalog := testGateV3Workflow(&GateAction{Type: GateActionDeterministic, Fields: map[string]any{
		"action": "${{ private.answer }}",
	}})
	catalog.Gates["other"] = GateDefinition{
		Prompt: "Other", DefaultAction: &GateAction{Type: GateActionHuman},
	}
	compiled, err := CompileGateWorkflowV3(catalog, "gates.decision", map[string]any{"answer": "approve"})
	if err != nil {
		t.Fatalf("CompileGateWorkflowV3() error = %v", err)
	}
	if compiled.GateRef != "gates.decision" || len(compiled.Workflow.Gates) != 2 ||
		compiled.PrivateRoot == nil || compiled.Workflow.privateRootRevision == "" ||
		compiled.PrivateRoot.privateValuesRevision == "" || len(compiled.Workflow.Jobs) != 1 {
		t.Fatalf("compiled = %#v", compiled)
	}
	step := compiled.Workflow.Jobs[workflowGateJobID].Steps[0]
	if step.Uses != GateExecUses || step.With["gate-ref"] != "gates.decision" {
		t.Fatalf("compiled step = %#v", step)
	}
	workspace := t.TempDir()
	result, err := (&Executor{WorkspaceDir: workspace}).Run(context.Background(), RunRequest{
		Workflow: compiled.Workflow, WorkflowRef: "workflows/pr-lifecycle.yml",
		PrivateRoot: compiled.PrivateRoot,
	})
	if err != nil || result.Status != RunStatusSucceeded {
		t.Fatalf("private Run() = (%#v, %v)", result, err)
	}
}

func TestPrivateCompiledGateRunIDReconcilesExactWaitingRun(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	executor := &Executor{WorkspaceDir: workspace, Store: NewFileRunStore(workspace)}
	workflow := testGateV3Workflow(&GateAction{Type: GateActionHuman})
	compiled, err := CompileGateWorkflowV3(
		workflow, "gates.decision", map[string]any{"gate-subject": map[string]any{"id": "one"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	const runID = "wr_private_gate_reconcile_waiting"
	request := RunRequest{
		RunID: runID, Workflow: compiled.Workflow, WorkflowRef: "workflows/pr-lifecycle.yml",
		PrivateRoot: compiled.PrivateRoot,
	}
	first, err := executor.Run(ctx, request)
	if err != nil || first.Status != RunStatusWaiting {
		t.Fatalf("first Run() = (%#v, %v)", first, err)
	}
	second, err := executor.Run(ctx, request)
	if err != nil || second.RunID != runID || second.Status != RunStatusWaiting {
		t.Fatalf("reconciled Run() = (%#v, %v)", second, err)
	}
	tasks, err := executor.ListHumanTasks(ctx, runID)
	if err != nil || len(tasks) != 1 || tasks[0].Status != HumanTaskStatusWaiting {
		t.Fatalf("tasks = (%#v, %v), want one original task", tasks, err)
	}
}

func TestPrivateCompiledGateRunIDReconcilesCanceledHumanRun(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	executor := &Executor{WorkspaceDir: workspace, Store: NewFileRunStore(workspace)}
	compiled, err := CompileGateWorkflowV3(
		testGateV3Workflow(&GateAction{Type: GateActionHuman}),
		"gates.decision",
		map[string]any{"gate-subject": map[string]any{"id": "one"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	request := RunRequest{
		RunID:    "wr_private_gate_reconcile_canceled",
		Workflow: compiled.Workflow, WorkflowRef: "workflows/pr-lifecycle.yml",
		PrivateRoot: compiled.PrivateRoot,
	}
	started, runErr := executor.Run(ctx, request)
	if runErr != nil || started.Status != RunStatusWaiting {
		t.Fatalf("Run() = (%#v, %v)", started, runErr)
	}
	if _, err := executor.CancelRun(ctx, started.RunID, "operator canceled"); err != nil {
		t.Fatal(err)
	}
	reconciled, reconcileErr := executor.Run(ctx, request)
	if !errors.Is(reconcileErr, ErrRunCanceled) || reconciled == nil || reconciled.Status != RunStatusCanceled {
		t.Fatalf("reconciled canceled Run() = (%#v, %v)", reconciled, reconcileErr)
	}
}

func TestPrivateCompiledGateRunIDRejectsInvocationMismatches(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	executor := &Executor{WorkspaceDir: workspace, Store: store}
	workflow := testGateV3Workflow(&GateAction{Type: GateActionHuman})
	first, err := CompileGateWorkflowV3(
		workflow, "gates.decision", map[string]any{"gate-subject": map[string]any{"id": "one"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	const runID = "wr_private_gate_reconcile_mismatch"
	if result, err := executor.Run(ctx, RunRequest{
		RunID: runID, Workflow: first.Workflow, WorkflowRef: "workflows/pr-lifecycle.yml",
		PrivateRoot: first.PrivateRoot,
	}); err != nil || result.Status != RunStatusWaiting {
		t.Fatalf("first Run() = (%#v, %v)", result, err)
	}

	t.Run("private values", func(t *testing.T) {
		changed, compileErr := CompileGateWorkflowV3(
			workflow, "gates.decision", map[string]any{"gate-subject": map[string]any{"id": "two"}},
		)
		if compileErr != nil {
			t.Fatal(compileErr)
		}
		_, runErr := executor.Run(ctx, RunRequest{
			RunID: runID, Workflow: changed.Workflow, WorkflowRef: "workflows/pr-lifecycle.yml",
			PrivateRoot: changed.PrivateRoot,
		})
		if !errors.Is(runErr, ErrRunAdmissionConflict) {
			t.Fatalf("Run() error = %v, want ErrRunAdmissionConflict", runErr)
		}
	})

	t.Run("compiled workflow", func(t *testing.T) {
		changedWorkflow := testGateV3Workflow(&GateAction{
			Type:   GateActionDeterministic,
			Fields: map[string]any{"action": "approve"},
		})
		changed, compileErr := CompileGateWorkflowV3(
			changedWorkflow, "gates.decision", map[string]any{"gate-subject": map[string]any{"id": "one"}},
		)
		if compileErr != nil {
			t.Fatal(compileErr)
		}
		_, runErr := executor.Run(ctx, RunRequest{
			RunID: runID, Workflow: changed.Workflow, WorkflowRef: "workflows/pr-lifecycle.yml",
			PrivateRoot: changed.PrivateRoot,
		})
		if !errors.Is(runErr, ErrRunAdmissionConflict) {
			t.Fatalf("Run() error = %v, want ErrRunAdmissionConflict", runErr)
		}
	})

	t.Run("resolved action", func(t *testing.T) {
		override := &staticGateActionResolver{resolution: GateActionResolution{
			Action: &GateAction{
				Type:   GateActionDeterministic,
				Fields: map[string]any{"action": "approve"},
			},
			Revision: "override-v2",
		}}
		changedExecutor := *executor
		changedExecutor.GateActions = override
		_, runErr := changedExecutor.Run(ctx, RunRequest{
			RunID: runID, Workflow: first.Workflow, WorkflowRef: "workflows/pr-lifecycle.yml",
			PrivateRoot: first.PrivateRoot,
		})
		if !errors.Is(runErr, ErrRunAdmissionConflict) {
			t.Fatalf("Run() error = %v, want ErrRunAdmissionConflict", runErr)
		}
	})
}

func TestPrivateCompiledGateRunIDReconcilesTerminalAIWithoutReplay(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	agents := &fakeAgentRunner{outputs: map[string]any{
		"structured": map[string]any{
			"field-values": map[string]any{"action": "approve"},
		},
	}}
	executor := &Executor{
		WorkspaceDir: workspace, Store: NewFileRunStore(workspace), Agents: agents,
	}
	workflow := testGateV3Workflow(&GateAction{
		Type: GateActionAI, AgentID: "reviewer", Prompt: "Decide",
		Session: AgentSessionEphemeral, History: "none", Cache: "none", Tools: AgentToolsNone,
	})
	compiled, compileErr := CompileGateWorkflowV3(
		workflow, "gates.decision", map[string]any{"gate-subject": map[string]any{"id": "one"}},
	)
	if compileErr != nil {
		t.Fatal(compileErr)
	}
	request := RunRequest{
		RunID:    "wr_private_gate_reconcile_terminal",
		Workflow: compiled.Workflow, WorkflowRef: "workflows/pr-lifecycle.yml",
		PrivateRoot: compiled.PrivateRoot,
	}
	first, err := executor.Run(ctx, request)
	if err != nil || first.Status != RunStatusSucceeded || len(agents.requests) != 1 {
		t.Fatalf("first Run() = (%#v, %v), agent calls=%d", first, err, len(agents.requests))
	}
	second, err := executor.Run(ctx, request)
	if err != nil || second.Status != RunStatusSucceeded || len(agents.requests) != 1 {
		t.Fatalf("reconciled Run() = (%#v, %v), agent calls=%d", second, err, len(agents.requests))
	}
}

func TestGateWorkflowActionPrivateMixedCompositionSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	definitions := filepath.Join(workspace, "workflows", "actions")
	if err := os.MkdirAll(definitions, 0o755); err != nil {
		t.Fatal(err)
	}
	child := `
name: Mixed private gate action
gates:
  confirm:
    prompt: Confirm the automated recommendation.
    fields:
      - id: confirmed
        type: boolean
        label: Confirm
        required: true
    default-action:
      type: human
  confirm-again:
    prompt: Confirm once more after AI inspection.
    fields:
      - id: confirmed
        type: boolean
        label: Confirm
        required: true
    default-action:
      type: human
  inspect:
    prompt: Inspect the frozen gate subject.
    fields:
      - id: checked
        type: boolean
        label: Subject checked
        required: true
    default-action:
      type: ai
      agent-id: reviewer
      prompt: Check the frozen subject and complete the fields.
      session: ephemeral
      history: none
      cache: none
      tools: none
  answer:
    prompt: Produce the final action-workflow response.
    fields:
      - id: action
        type: select
        label: Action
        min-selections: 1
        max-selections: 1
        options:
          - id: approve
            label: Approve
          - id: revise
            label: Revise
      - id: explanation
        type: long-text
        label: Explanation
    default-action:
      type: deterministic
      fields:
        action: approve
        explanation: Human and AI checks completed.
on:
  workflow_call:
    outputs:
      field-values:
        value: ${{ jobs.decide.outputs.field-values }}
jobs:
  decide:
    runs-on: picoclaw
    outputs:
      field-values: ${{ steps.answer.outputs.field-values }}
    steps:
      - id: confirm
        uses: gate/exec
        with:
          gate-ref: gates.confirm
      - id: inspect
        uses: gate/exec
        with:
          gate-ref: gates.inspect
      - id: confirm-again
        uses: gate/exec
        with:
          gate-ref: gates.confirm-again
      - id: answer
        uses: gate/exec
        with:
          gate-ref: gates.answer
`
	if err := os.WriteFile(filepath.Join(definitions, "mixed.yml"), []byte(child), 0o600); err != nil {
		t.Fatal(err)
	}
	agents := &fakeAgentRunner{outputs: map[string]any{
		"structured": map[string]any{
			"field-values": map[string]any{"checked": true},
		},
	}}
	outer := testGateV3Workflow(&GateAction{
		Type: GateActionWorkflow, WorkflowRef: "workflows/actions/mixed.yml",
	})
	compiled, err := CompileGateWorkflowV3(
		outer,
		"gates.decision",
		map[string]any{"gate-subject": map[string]any{"scope": "cache"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	store := NewFileRunStore(workspace)
	executor := &Executor{
		WorkspaceDir: workspace, Store: store, Agents: agents,
	}
	started, err := executor.Run(ctx, RunRequest{
		Workflow: compiled.Workflow, WorkflowRef: "workflows/pr-lifecycle.yml",
		PrivateRoot: compiled.PrivateRoot,
	})
	if err != nil || started.Status != RunStatusWaiting {
		t.Fatalf("Run() = (%#v, %v), want waiting", started, err)
	}
	tasks, err := executor.ListHumanTasks(ctx, started.RunID)
	if err != nil || len(tasks) != 1 || tasks[0].GateWorkflow == nil || tasks[0].GateForm == nil {
		t.Fatalf("ListHumanTasks() = (%#v, %v)", tasks, err)
	}
	task := tasks[0]
	if task.GateForm.GateRef != "gates.confirm" || task.GateWorkflow.ChildRunID == "" {
		t.Fatalf("proxy task = %#v", task)
	}
	request := HumanTaskResumeRequest{
		ExpectedRevision: task.Revision,
		InputHash:        task.InputHash,
		ResponseID:       "mixed-response",
		Response: map[string]any{
			"field-values": map[string]any{"confirmed": true},
		},
	}
	// Recreate the executor to prove that the proxy and both workflow snapshots
	// carry everything needed after a service restart.
	restarted := &Executor{
		WorkspaceDir: workspace, Store: store, Agents: agents,
	}
	resumed, err := restarted.ResumeHumanTask(ctx, started.RunID, task.ID, request)
	if err != nil || resumed.Status != RunStatusWaiting {
		t.Fatalf("first ResumeHumanTask() = (%#v, %v), want waiting", resumed, err)
	}
	if len(agents.requests) != 1 || !agents.requests[0].PrivateContext ||
		agents.requests[0].IsolatedSystemPrompt == "" ||
		agents.requests[0].Inputs["gate-inputs"] == nil {
		t.Fatalf("agent request = %#v", agents.requests)
	}
	tasks, err = restarted.ListHumanTasks(ctx, started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var next *WorkflowHumanTask
	for index := range tasks {
		if tasks[index].Status == HumanTaskStatusWaiting {
			next = &tasks[index]
			break
		}
	}
	if next == nil || next.ID == task.ID || next.GateForm == nil ||
		next.GateForm.GateRef != "gates.confirm-again" {
		t.Fatalf("second proxy task = %#v", next)
	}
	secondRequest := HumanTaskResumeRequest{
		ExpectedRevision: next.Revision,
		InputHash:        next.InputHash,
		ResponseID:       "mixed-response-2",
		Response: map[string]any{
			"field-values": map[string]any{"confirmed": true},
		},
	}
	resumed, err = restarted.ResumeHumanTask(ctx, started.RunID, next.ID, secondRequest)
	if err != nil || resumed.Status != RunStatusSucceeded {
		t.Fatalf("second ResumeHumanTask() = (%#v, %v)", resumed, err)
	}
	run, err := store.GetRun(ctx, started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	outputs := run.Steps[workflowGateJobID+"/gate-exec"].Outputs
	if outputs["actor-kind"] != GateActorWorkflow || !reflect.DeepEqual(
		outputs["field-values"],
		map[string]any{"action": "approve", "explanation": "Human and AI checks completed."},
	) {
		t.Fatalf("outer gate outputs = %#v", outputs)
	}
	duplicate, err := restarted.ResumeHumanTask(ctx, started.RunID, next.ID, secondRequest)
	if err != nil || duplicate.Status != RunStatusSucceeded {
		t.Fatalf("duplicate ResumeHumanTask() = (%#v, %v)", duplicate, err)
	}
	conflicting := secondRequest
	conflicting.ResponseID = "other-response"
	conflicting.Response = map[string]any{"field-values": map[string]any{"confirmed": false}}
	if _, err := restarted.ResumeHumanTask(
		ctx,
		started.RunID,
		next.ID,
		conflicting,
	); !errors.Is(
		err,
		ErrHumanTaskConflict,
	) {
		t.Fatalf("conflicting ResumeHumanTask() error = %v", err)
	}
}

func TestPrivateGateActionWorkflowAdmissionRejectsEffectfulTargets(t *testing.T) {
	base := func(step Step) *Workflow {
		return &Workflow{
			On: WorkflowTriggers{WorkflowCall: &WorkflowCall{Outputs: map[string]Output{
				"field-values": {Value: "${{ jobs.main.outputs.field-values }}"},
			}}},
			Jobs: map[string]Job{"main": {RunsOn: "picoclaw", Steps: []Step{step}}},
		}
	}
	for _, test := range []struct {
		name string
		step Step
	}{
		{name: "function", step: Step{Uses: "function/exfiltrate"}},
		{name: "tool", step: Step{Uses: "tool/send"}},
		{name: "mcp", step: Step{Uses: "mcp/remote/send"}},
		{name: "ordinary agent", step: Step{Uses: "agent/reviewer"}},
		{name: "legacy Human task", step: Step{Uses: "human/task"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validatePrivateGateActionWorkflowAdmission(
				base(test.step),
			); !errors.Is(
				err,
				ErrPrivateWorkflowContext,
			) {
				t.Fatalf("admission error = %v", err)
			}
		})
	}
	unsafeAI := base(Step{Uses: GateExecUses, With: map[string]any{"gate-ref": "gates.inspect"}})
	unsafeAI.Gates = map[string]GateDefinition{
		"inspect": {
			Prompt: "Inspect",
			DefaultAction: &GateAction{
				Type: GateActionAI, AgentID: "reviewer", Prompt: "Inspect",
				Session: AgentSessionEphemeral, History: "none", Cache: "none",
				Tools: AgentToolsInherit,
			},
		},
	}
	if err := validatePrivateGateActionWorkflowAdmission(unsafeAI); !errors.Is(err, ErrPrivateWorkflowContext) {
		t.Fatalf("unsafe AI admission error = %v", err)
	}
	requiredInput := base(Step{Uses: GateExecUses, With: map[string]any{"gate-ref": "gates.confirm"}})
	requiredInput.Gates = map[string]GateDefinition{
		"confirm": {Prompt: "Confirm", DefaultAction: &GateAction{Type: GateActionHuman}},
	}
	requiredInput.On.WorkflowCall.Inputs = map[string]Input{
		"missing": {Type: "string", Required: true},
	}
	if err := validatePrivateGateActionWorkflowAdmission(requiredInput); !errors.Is(err, ErrPrivateWorkflowContext) {
		t.Fatalf("required input admission error = %v", err)
	}
}

func TestGateWorkflowActionReconcilesChildAfterParentWaitCrash(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	definitions := filepath.Join(workspace, "workflows", "actions")
	if err := os.MkdirAll(definitions, 0o755); err != nil {
		t.Fatal(err)
	}
	child := `
name: Waiting action
gates:
  answer:
    prompt: Choose an action.
    fields:
      - id: action
        type: select
        label: Action
        min-selections: 1
        max-selections: 1
        options:
          - id: approve
            label: Approve
          - id: revise
            label: Revise
    default-action:
      type: human
on:
  workflow_call:
    outputs:
      field-values:
        value: ${{ jobs.decide.outputs.field-values }}
jobs:
  decide:
    runs-on: picoclaw
    outputs:
      field-values: ${{ steps.answer.outputs.field-values }}
    steps:
      - id: answer
        uses: gate/exec
        with:
          gate-ref: gates.answer
`
	if err := os.WriteFile(filepath.Join(definitions, "waiting.yml"), []byte(child), 0o600); err != nil {
		t.Fatal(err)
	}
	outer := testGateV3Workflow(&GateAction{
		Type: GateActionWorkflow, WorkflowRef: "workflows/actions/waiting.yml",
	})
	compiled, compileErr := CompileGateWorkflowV3(
		outer, "gates.decision", map[string]any{"gate-subject": map[string]any{"id": "subject"}},
	)
	if compileErr != nil {
		t.Fatal(compileErr)
	}
	const parentRunID = "wr_gate_parent_crash"
	baseStore := NewFileRunStore(workspace)
	store := &failParentGateWaitStore{
		FileRunStore: baseStore, parentRunID: parentRunID, fail: true,
	}
	executor := &Executor{WorkspaceDir: workspace, Store: store}
	if _, err := executor.Run(ctx, RunRequest{
		RunID: parentRunID, Workflow: compiled.Workflow,
		WorkflowRef: "workflows/pr-lifecycle.yml", PrivateRoot: compiled.PrivateRoot,
	}); err == nil {
		t.Fatal("Run() succeeded, want injected persistence failure")
	}
	runs, listErr := baseStore.ListRuns(ctx)
	if listErr != nil {
		t.Fatal(listErr)
	}
	var childRun *Run
	for _, candidate := range runs {
		if candidate.ID != parentRunID {
			candidateCopy := candidate
			childRun = &candidateCopy
		}
	}
	if len(runs) != 2 || childRun == nil || childRun.Status != RunStatusWaiting {
		t.Fatalf("runs after crash = %#v", runs)
	}
	parent, err := baseStore.GetRun(ctx, parentRunID)
	if err != nil {
		t.Fatal(err)
	}
	step := parent.execution.Workflow.Jobs[workflowGateJobID].Steps[0]
	execCtx := ExecutionContext{
		Inputs: map[string]any{}, Event: map[string]any{}, Steps: map[string]StepExecution{},
		Needs: map[string]JobExecution{}, WorkflowRef: parent.WorkflowRef, RunID: parent.ID,
		privateValues: cloneMap(parent.privateRoot.Values),
	}
	stepExecution, err := executor.executeStep(
		ctx, store, parent, workflowGateJobID, 0, step, execCtx, map[string]JobExecution{},
	)
	var waiting workflowWaitingError
	if !errors.As(err, &waiting) || stepExecution.Status != RunStatusWaiting ||
		len(parent.humanTasks) != 1 {
		t.Fatalf("reconciled executeStep() = (%#v, %v), tasks=%#v", stepExecution, err, parent.humanTasks)
	}
	runs, err = baseStore.ListRuns(ctx)
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs after reconciliation = (%#v, %v), want no duplicate child", runs, err)
	}
	proxy := onlyWaitingHumanTask(parent.humanTasks)
	if proxy == nil || proxy.GateWorkflow == nil ||
		proxy.GateWorkflow.ChildRunID != childRun.ID {
		t.Fatalf("proxy = %#v, child = %#v", proxy, childRun)
	}
	for key, expected := range map[string]string{
		"gate-parent-run-id":          parentRunID,
		"gate-parent-execution-id":    proxy.GateWorkflow.ExecutionID,
		"gate-parent-action-revision": proxy.GateWorkflow.ActionRevision,
		"gate-parent-input-hash":      proxy.GateWorkflow.InputHash,
	} {
		if actual, _ := childRun.privateRoot.Values[key].(string); actual != expected {
			t.Fatalf("child private binding %s = %q, want %q", key, actual, expected)
		}
	}
}

func onlyWaitingHumanTask(tasks map[string]WorkflowHumanTask) *WorkflowHumanTask {
	for _, task := range tasks {
		if task.Status == HumanTaskStatusWaiting {
			taskCopy := task
			return &taskCopy
		}
	}
	return nil
}
