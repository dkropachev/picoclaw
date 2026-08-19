package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestGateV3CompilationRejectsUntrustedInputs(t *testing.T) {
	if _, err := CompileGateWorkflowV3(nil, "gates.decision", nil); err == nil {
		t.Fatal("nil workflow was accepted")
	}
	workflow := testGateV3Workflow(&GateAction{Type: GateActionHuman})
	for _, gateRef := range []string{"decision", " gates.decision", "gates.missing"} {
		if _, err := CompileGateWorkflowV3(workflow, gateRef, nil); err == nil {
			t.Fatalf("gate ref %q was accepted", gateRef)
		}
	}
	invalidGate := testGateV3Workflow(&GateAction{Type: GateActionHuman})
	gate := invalidGate.Gates["decision"]
	gate.Prompt = ""
	invalidGate.Gates["decision"] = gate
	if _, err := CompileGateWorkflowV3(invalidGate, "gates.decision", nil); err == nil {
		t.Fatal("invalid gate catalog was compiled")
	}
	if _, err := CompileGateWorkflowV3(
		workflow,
		"gates.decision",
		map[string]any{"unsupported": make(chan int)},
	); err == nil {
		t.Fatal("non-JSON private values were compiled")
	}

	if _, err := compileGateActionWorkflowV3(nil, nil); err == nil {
		t.Fatal("nil action workflow was accepted")
	}
	noCall := &Workflow{On: WorkflowTriggers{Manual: map[string]any{}}, Jobs: map[string]Job{}}
	if _, err := compileGateActionWorkflowV3(noCall, nil); err == nil {
		t.Fatal("action workflow without workflow-call was accepted")
	}
	invalidActionWorkflow := &Workflow{
		On: WorkflowTriggers{WorkflowCall: &WorkflowCall{}},
		Jobs: map[string]Job{
			"invalid job": {RunsOn: "picoclaw"},
		},
	}
	if _, err := compileGateActionWorkflowV3(invalidActionWorkflow, nil); err == nil {
		t.Fatal("invalid action workflow was compiled")
	}
	validActionWorkflow := &Workflow{
		On: WorkflowTriggers{WorkflowCall: &WorkflowCall{}},
		Jobs: map[string]Job{
			"main": {RunsOn: "picoclaw", Steps: []Step{{Uses: "function/noop"}}},
		},
	}
	if _, err := compileGateActionWorkflowV3(
		validActionWorkflow,
		map[string]any{"unsupported": make(chan int)},
	); err == nil {
		t.Fatal("action workflow accepted non-JSON private values")
	}
	if got := (gateActionWorkflowWaitingError{}).Error(); got == "" {
		t.Fatal("waiting error has no message")
	}
}

func TestGateV3DefinitionResolutionFailsClosed(t *testing.T) {
	if _, err := gateDefinitionForRef(nil, "gates.decision"); err == nil {
		t.Fatal("nil workflow returned a gate")
	}
	if _, err := gateDefinitionForRef(&Workflow{}, "gates.decision"); err == nil {
		t.Fatal("missing gate was resolved")
	}

	workflow := testGateV3Workflow(nil)
	run := gateCoverageRun(t, workflow)
	if _, err := (&Executor{}).resolveGateAction(
		context.Background(), nil, "main", "decide", "gates.decision",
	); err == nil {
		t.Fatal("nil run resolved an action")
	}
	if _, err := (&Executor{}).resolveGateAction(
		context.Background(), run, "main", "decide", "bad-ref",
	); err == nil {
		t.Fatal("invalid gate ref resolved an action")
	}
	if _, err := (&Executor{}).resolveGateAction(
		context.Background(), run, "main", "decide", "gates.missing",
	); err == nil {
		t.Fatal("missing gate resolved an action")
	}
	if _, err := (&Executor{}).resolveGateAction(
		context.Background(), run, "main", "decide", "gates.decision",
	); err == nil {
		t.Fatal("gate without any action resolved")
	}

	resolverFailure := &staticGateActionResolver{err: errors.New("resolver failed")}
	if _, err := (&Executor{GateActions: resolverFailure}).resolveGateAction(
		context.Background(), run, "main", "decide", "gates.decision",
	); err == nil {
		t.Fatal("resolver failure was ignored")
	}
	invalidAction := &staticGateActionResolver{resolution: GateActionResolution{
		Action: &GateAction{Type: GateActionDeterministic},
	}}
	if _, err := (&Executor{GateActions: invalidAction}).resolveGateAction(
		context.Background(), run, "main", "decide", "gates.decision",
	); err == nil {
		t.Fatal("invalid resolved action was accepted")
	}
	invalidRevision := &staticGateActionResolver{resolution: GateActionResolution{
		Action: &GateAction{Type: GateActionHuman}, Revision: " revision ",
	}}
	if _, err := (&Executor{GateActions: invalidRevision}).resolveGateAction(
		context.Background(), run, "main", "decide", "gates.decision",
	); err == nil {
		t.Fatal("non-canonical action revision was accepted")
	}
	tooLongRevision := &staticGateActionResolver{resolution: GateActionResolution{
		Action:   &GateAction{Type: GateActionHuman},
		Revision: strings.Repeat("r", MaxGateActionRevisionBytes+1),
	}}
	if _, err := (&Executor{GateActions: tooLongRevision}).resolveGateAction(
		context.Background(), run, "main", "decide", "gates.decision",
	); err == nil {
		t.Fatal("oversized action revision was accepted")
	}
	missingActionWorkflow := testGateV3Workflow(&GateAction{
		Type: GateActionWorkflow, WorkflowRef: "workflows/actions/missing.yml",
	})
	if _, err := (&Executor{WorkspaceDir: t.TempDir()}).resolveGateAction(
		context.Background(), gateCoverageRun(t, missingActionWorkflow), "main", "decide", "gates.decision",
	); err == nil {
		t.Fatal("missing action workflow was resolved")
	}
}

func TestGateV3RuntimeActionValidationMatrix(t *testing.T) {
	validAI := GateAction{
		Type: GateActionAI, AgentID: "reviewer", Prompt: "Decide",
		Session: AgentSessionEphemeral, History: "none", Cache: "none", Tools: AgentToolsNone,
	}
	tests := []struct {
		name   string
		mutate func(*GateAction)
	}{
		{name: "agent ID", mutate: func(action *GateAction) { action.AgentID = "Reviewer Invalid" }},
		{name: "session", mutate: func(action *GateAction) { action.Session = "durable-forever" }},
		{name: "history", mutate: func(action *GateAction) { action.History = "everything" }},
		{name: "cache", mutate: func(action *GateAction) { action.Cache = "global" }},
		{name: "tools", mutate: func(action *GateAction) { action.Tools = "all" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			action := validAI
			test.mutate(&action)
			if err := validateRuntimeGateAction(action); err == nil {
				t.Fatalf("invalid %s was accepted", test.name)
			}
		})
	}
	if err := validateRuntimeGateAction(GateAction{
		Type: GateActionWorkflow, WorkflowRef: "workflows/actions/../action.yml",
	}); err == nil {
		t.Fatal("non-canonical workflow action ref was accepted")
	}
	if err := validateRuntimeGateAction(GateAction{
		Type: GateActionAI, Prompt: "Source", Session: AgentSessionSource,
	}); err != nil {
		t.Fatalf("valid source action error = %v", err)
	}
}

func TestGateV3FieldValueValidationMatrix(t *testing.T) {
	fields := []GateField{
		{ID: "title", Type: GateFieldShortText, Required: true},
		{ID: "note", Type: GateFieldLongText},
		{ID: "confirmed", Type: GateFieldBoolean},
		{
			ID: "areas", Type: GateFieldSelect, MinSelections: 1, MaxSelections: 2,
			Options: []GateFieldOption{{ID: "code"}, {ID: "tests"}},
		},
	}
	tests := []struct {
		name  string
		value any
	}{
		{name: "not object", value: "invalid"},
		{name: "unknown", value: map[string]any{"unknown": true}},
		{name: "missing required", value: map[string]any{}},
		{name: "non-string", value: map[string]any{"title": true, "areas": []any{"code"}}},
		{name: "blank required", value: map[string]any{"title": " ", "areas": []any{"code"}}},
		{name: "non-boolean", value: map[string]any{"title": "ok", "confirmed": "yes", "areas": []any{"code"}}},
		{name: "selection not array", value: map[string]any{"title": "ok", "areas": "code"}},
		{name: "unknown selection", value: map[string]any{"title": "ok", "areas": []any{"docs"}}},
		{name: "too many selections", value: map[string]any{"title": "ok", "areas": []any{"code", "tests", "code"}}},
		{name: "duplicate selections", value: map[string]any{"title": "ok", "areas": []any{"code", "code"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := validateGateFieldValues(fields, test.value); err == nil {
				t.Fatalf("invalid field values %#v were accepted", test.value)
			}
		})
	}
	valid, err := validateGateFieldValues(fields, map[string]any{
		"title": "Decision", "note": "", "confirmed": true, "areas": []any{"tests"},
	})
	if err != nil || valid["confirmed"] != true {
		t.Fatalf("valid field values = (%#v, %v)", valid, err)
	}
}

func TestGateV3ExecutionRejectsInvalidDepthsAndActions(t *testing.T) {
	workflow := testGateV3Workflow(&GateAction{Type: GateActionHuman})
	run := gateCoverageRun(t, workflow)
	executor := &Executor{}
	for _, depth := range []any{
		json.Number("99"),
		float64(defaultMaxCallDepth + 1),
		defaultMaxCallDepth + 1,
	} {
		if _, _, err := executor.executeGate(
			context.Background(), run, "main", "decide",
			map[string]any{"gate-ref": "gates.decision"},
			ExecutionContext{Inputs: map[string]any{"gate-action-depth": depth}},
			nil, 0,
		); err == nil {
			t.Fatalf("depth %#v was accepted", depth)
		}
	}
	if _, _, err := executor.executeGate(
		context.Background(), run, "main", "decide", map[string]any{"gate-ref": 7},
		ExecutionContext{}, nil, 0,
	); err == nil {
		t.Fatal("non-string gate ref was accepted")
	}
	childRun := gateCoverageRun(t, workflow)
	childRun.ParentRunID = "wr_parent"
	if _, _, err := executor.executeGate(
		context.Background(), childRun, "main", "decide",
		map[string]any{"gate-ref": "gates.decision"}, ExecutionContext{}, nil, 0,
	); !errors.Is(err, ErrHumanTaskUnsupported) {
		t.Fatalf("nested Human gate error = %v", err)
	}

	aiWorkflow := testGateV3Workflow(&GateAction{
		Type: GateActionAI, AgentID: "reviewer", Prompt: "Decide",
		Session: AgentSessionEphemeral, History: "none", Cache: "none", Tools: AgentToolsNone,
	})
	if _, _, err := executor.executeGate(
		context.Background(), gateCoverageRun(t, aiWorkflow), "main", "decide",
		map[string]any{"gate-ref": "gates.decision"}, ExecutionContext{}, nil, 0,
	); err == nil {
		t.Fatal("AI gate ran without an agent runner")
	}

	deterministic := testGateV3Workflow(&GateAction{
		Type:   GateActionDeterministic,
		Fields: map[string]any{"action": "not-an-option"},
	})
	if _, _, err := executor.executeGate(
		context.Background(), gateCoverageRun(t, deterministic), "main", "decide",
		map[string]any{"gate-ref": "gates.decision"}, ExecutionContext{}, nil, 0,
	); err == nil {
		t.Fatal("invalid deterministic gate fields were accepted")
	}
	if _, _, err := executor.executeGateWorkflowAction(
		context.Background(), run, resolvedGateAction{}, ExecutionContext{}, 0,
	); err == nil {
		t.Fatal("missing action workflow snapshot was accepted")
	}
}

func TestGateV3AIExecutionFailureMatrix(t *testing.T) {
	resolved := resolvedGateAction{
		GateRef: "gates.decision",
		Gate: GateDefinition{
			Prompt: "Choose", Fields: []GateField{{ID: "confirmed", Type: GateFieldBoolean, Required: true}},
		},
		Action: GateAction{
			Type: GateActionAI, AgentID: "reviewer", Prompt: "Decide",
			Session: AgentSessionEphemeral, History: "none", Cache: "none", Tools: AgentToolsNone,
		},
	}
	if _, err := (*Executor)(nil).executeAIGateAction(
		context.Background(), resolved, ExecutionContext{},
	); err == nil {
		t.Fatal("nil executor ran an AI gate")
	}

	source := resolved
	source.Action = GateAction{Type: GateActionAI, Prompt: "Source", Session: AgentSessionSource}
	if _, err := (&Executor{Agents: &fakeAgentRunner{}}).executeAIGateAction(
		context.Background(), source, ExecutionContext{},
	); err == nil {
		t.Fatal("source AI gate ran without provenance")
	}
	private := resolved
	private.Action.Session = AgentSessionPrivate
	if _, err := (&Executor{Agents: &fakeAgentRunner{}}).executeAIGateAction(
		context.Background(), private, ExecutionContext{},
	); err == nil {
		t.Fatal("private AI gate ran without frozen session")
	}

	for _, test := range []struct {
		name    string
		action  GateAction
		context ExecutionContext
	}{
		{
			name: "private tools",
			action: GateAction{
				Type: GateActionAI, AgentID: "reviewer", Prompt: "Decide",
				Session: AgentSessionEphemeral, History: "none", Cache: "none", Tools: AgentToolsInherit,
			},
			context: ExecutionContext{privateValues: map[string]any{}},
		},
		{
			name: "private history",
			action: GateAction{
				Type: GateActionAI, AgentID: "reviewer", Prompt: "Decide",
				Session: AgentSessionEphemeral, History: "full", Cache: "none", Tools: AgentToolsNone,
			},
			context: ExecutionContext{privateValues: map[string]any{}},
		},
		{
			name: "read only without snapshot",
			action: GateAction{
				Type: GateActionAI, AgentID: "reviewer", Prompt: "Decide",
				Session: AgentSessionEphemeral, History: "read_only", Cache: "none", Tools: AgentToolsNone,
			},
		},
		{
			name: "snapshot agent mismatch",
			action: GateAction{
				Type: GateActionAI, AgentID: "reviewer", Prompt: "Decide",
				Session: AgentSessionEphemeral, History: "read_only", Cache: "none", Tools: AgentToolsNone,
			},
			context: ExecutionContext{frozenReadOnlySession: &FrozenReadOnlySession{AgentID: "other"}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := resolved
			candidate.Action = test.action
			if _, err := (&Executor{Agents: &fakeAgentRunner{}}).executeAIGateAction(
				context.Background(), candidate, test.context,
			); err == nil {
				t.Fatal("invalid AI execution context was accepted")
			}
		})
	}

	for _, test := range []struct {
		name    string
		outputs map[string]any
		err     error
	}{
		{name: "runner error", err: errors.New("agent failed")},
		{name: "missing structured", outputs: map[string]any{"text": "no JSON"}},
		{name: "missing field values", outputs: map[string]any{"structured": map[string]any{}}},
		{name: "invalid field values", outputs: map[string]any{
			"structured": map[string]any{"field-values": map[string]any{"confirmed": "yes"}},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeAgentRunner{outputs: test.outputs, err: test.err}
			if _, err := (&Executor{Agents: runner}).executeAIGateAction(
				context.Background(), resolved, ExecutionContext{},
			); err == nil {
				t.Fatal("invalid agent response was accepted")
			}
		})
	}
}

func TestGateV3RecoveryHelpersFailClosed(t *testing.T) {
	resolved := resolvedGateAction{
		GateRef: "gates.decision",
		Gate: GateDefinition{Fields: []GateField{{
			ID: "answer", Type: GateFieldShortText, Required: true,
		}}},
		ExecutionID: "ge_test", ActionRevision: "revision", InputHash: "input",
	}
	for _, test := range []struct {
		name  string
		child *Run
	}{
		{name: "nil", child: nil},
		{name: "succeeded without output", child: &Run{ID: "child", Status: RunStatusSucceeded}},
		{name: "succeeded invalid output", child: &Run{
			ID: "child", Status: RunStatusSucceeded,
			Outputs: map[string]any{"field-values": map[string]any{"answer": true}},
		}},
		{name: "running", child: &Run{ID: "child", Status: RunStatusRunning}},
		{name: "failed", child: &Run{ID: "child", Status: RunStatusFailed}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := gateActionWorkflowExistingResult(test.child, resolved); err == nil {
				t.Fatal("invalid child result was accepted")
			}
		})
	}
	waiting := &Run{ID: "child", Status: RunStatusWaiting}
	if _, childID, err := gateActionWorkflowExistingResult(waiting, resolved); err == nil || childID != "child" {
		t.Fatalf("waiting child result = (%q, %v)", childID, err)
	}
	succeeded := &Run{
		ID: "child", Status: RunStatusSucceeded,
		Outputs: map[string]any{"field-values": map[string]any{"answer": "done"}},
	}
	if values, _, err := gateActionWorkflowExistingResult(
		succeeded,
		resolved,
	); err != nil || values["answer"] != "done" {
		t.Fatalf("succeeded child result = (%#v, %v)", values, err)
	}

	if _, err := workflowStepIndex(nil, "main", "step"); err == nil {
		t.Fatal("nil workflow returned a step index")
	}
	if _, err := workflowStepIndex(&Workflow{}, "main", "step"); err == nil {
		t.Fatal("missing job returned a step index")
	}
	if _, err := workflowStepIndex(&Workflow{Jobs: map[string]Job{
		"main": {Steps: []Step{{ID: "other"}}},
	}}, "main", "step"); err == nil {
		t.Fatal("missing step returned an index")
	}

	if gateActionChildRunForTask(nil, "task") != "" || gateActionWaitingChildRun(nil) != "" {
		t.Fatal("nil run returned a child run ID")
	}
	run := &Run{humanTasks: map[string]WorkflowHumanTask{
		"ordinary": {ID: "ordinary", Status: HumanTaskStatusWaiting},
		"proxy": {
			ID: "proxy", Status: HumanTaskStatusWaiting,
			GateWorkflow: &gateActionWorkflowContinuation{ChildRunID: "child"},
		},
	}}
	if gateActionChildRunForTask(run, "proxy") != "child" || gateActionWaitingChildRun(run) != "child" {
		t.Fatalf("proxy child lookup failed: %#v", run.humanTasks)
	}

	if _, _, _, ok := privateCompiledGateExecIdentity(nil); ok {
		t.Fatal("nil workflow produced private gate identity")
	}
	multiple := &Workflow{Jobs: map[string]Job{
		"main": {Steps: []Step{
			{Uses: GateExecUses, With: map[string]any{"gate-ref": "gates.one"}},
			{Uses: GateExecUses, With: map[string]any{"gate-ref": "gates.two"}},
		}},
	}}
	if _, _, _, ok := privateCompiledGateExecIdentity(multiple); ok {
		t.Fatal("multiple gate steps produced a private gate identity")
	}
	badRef := &Workflow{Jobs: map[string]Job{
		"main": {Steps: []Step{{Uses: GateExecUses, With: map[string]any{"gate-ref": true}}}},
	}}
	if _, _, _, ok := privateCompiledGateExecIdentity(badRef); ok {
		t.Fatal("non-string gate ref produced an identity")
	}
	if _, _, _, ok := persistedPrivateGateActionIdentity(nil, "main", "step"); ok {
		t.Fatal("nil run produced persisted gate identity")
	}
	missing := &Run{Steps: map[string]StepExecution{}, humanTasks: map[string]WorkflowHumanTask{}}
	if _, _, _, ok := persistedPrivateGateActionIdentity(missing, "main", "step"); ok {
		t.Fatal("missing step produced persisted gate identity")
	}
}

func gateCoverageRun(t *testing.T, workflow *Workflow) *Run {
	t.Helper()
	execution, err := newWorkflowExecutionState(workflow)
	if err != nil {
		t.Fatal(err)
	}
	return &Run{
		ID: "wr_gate_coverage", WorkflowRef: "workflows/gate.yml",
		execution: execution,
	}
}
