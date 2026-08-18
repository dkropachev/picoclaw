package workflows

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/session"
)

func TestCompileGateWorkflowLowersOrderedMixedGates(t *testing.T) {
	subject := map[string]any{
		"repository": "acme/widgets",
		"risk":       "normal",
		"findings": []any{
			map[string]any{"path": "auth.go", "severity": "high"},
		},
	}
	specs := []GateSpec{
		{ID: "disabled", Kind: GateZero},
		{
			ID:        "policy",
			Kind:      GateDeterministic,
			When:      "${{ inputs.gate_subject.risk == 'critical' }}",
			Title:     "Policy review",
			Questions: []any{map[string]any{"id": "approved", "prompt": "Approve?"}},
		},
		{
			ID:       "isolated",
			Kind:     GateAIIsolatedContext,
			AgentID:  "reviewer",
			Criteria: "Ask only when the supplied findings are ambiguous.",
			Title:    "Resolve isolated review",
		},
		{
			ID:       "working",
			Kind:     GateAIWorkingContext,
			AgentID:  "main",
			Criteria: "Ask only when the active discussion cannot resolve the issue.",
			Title:    "Resolve active discussion",
		},
	}

	compilation, err := CompileGateWorkflow("Review attention", specs, subject)
	if err != nil {
		t.Fatalf("CompileGateWorkflow() error = %v", err)
	}
	if compilation == nil || compilation.Noop || compilation.Workflow == nil {
		t.Fatalf("compilation = %#v, want executable workflow", compilation)
	}
	if !compilation.RequiresSession {
		t.Fatal("RequiresSession = false, want working-context requirement")
	}
	if compilation.RequiredSessionAgentID != "main" {
		t.Fatalf("RequiredSessionAgentID = %q, want main", compilation.RequiredSessionAgentID)
	}
	if !reflect.DeepEqual(
		compilation.GateIDs,
		[]string{"disabled", "policy", "isolated", "working"},
	) {
		t.Fatalf("GateIDs = %#v, want source-order IDs including zero gate", compilation.GateIDs)
	}
	if err := Validate(compilation.Workflow); err != nil {
		t.Fatalf("compiled workflow validation error = %v", err)
	}
	if compilation.Workflow.Name != "Review attention" || compilation.Workflow.On.Manual == nil {
		t.Fatalf("compiled workflow identity/trigger = %#v", compilation.Workflow)
	}
	if len(compilation.Workflow.Jobs) != 1 {
		t.Fatalf("compiled jobs = %#v, want one ordered job", compilation.Workflow.Jobs)
	}
	job, ok := compilation.Workflow.Jobs["gates"]
	if !ok || job.RunsOn != "picoclaw" {
		t.Fatalf("compiled gates job = %#v", job)
	}
	if got, want := gateStepIDs(job.Steps), []string{
		"gate_policy_attention",
		"gate_isolated_decision",
		"gate_isolated_attention",
		"gate_working_decision",
		"gate_working_attention",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("compiled step IDs = %#v, want %#v", got, want)
	}

	deterministic := job.Steps[0]
	if deterministic.Uses != "human/task" ||
		deterministic.If != "${{ private.gate_subject.risk == 'critical' }}" {
		t.Fatalf("deterministic lowering = %#v", deterministic)
	}
	assertCompiledHumanTaskInputReferences(t, deterministic)

	isolatedDecision := job.Steps[1]
	if isolatedDecision.Uses != "agent/reviewer" ||
		isolatedDecision.With["history"] != "none" ||
		isolatedDecision.With["cache"] != "none" ||
		isolatedDecision.With["tools"] != AgentToolsNone {
		t.Fatalf("isolated decision lowering = %#v", isolatedDecision)
	}
	if isolatedDecision.With["session"] != AgentSessionEphemeral {
		t.Fatalf(
			"isolated decision session = %#v, want exact ephemeral profile",
			isolatedDecision.With["session"],
		)
	}
	assertGateDecisionContract(t, isolatedDecision.With["output"])
	assertCompiledAIAttentionStep(t, job.Steps[2], isolatedDecision.ID)

	workingDecision := job.Steps[3]
	if workingDecision.Uses != "agent/main" ||
		workingDecision.With["session"] != "inherit" ||
		workingDecision.With["history"] != "read_only" ||
		workingDecision.With["cache"] != "session" ||
		workingDecision.With["tools"] != AgentToolsNone {
		t.Fatalf("working-context decision lowering = %#v", workingDecision)
	}
	assertGateDecisionContract(t, workingDecision.With["output"])
	assertCompiledAIAttentionStep(t, job.Steps[4], workingDecision.ID)

	// The subject and user-authored gate text belong in the private root. The
	// generated workflow should refer to them instead of treating their content
	// as another workflow expression pass.
	isolatedScope, isolatedScopeOK := isolatedDecision.With["scope"].(map[string]any)
	workingScope, workingScopeOK := workingDecision.With["scope"].(map[string]any)
	if !isolatedScopeOK || !workingScopeOK ||
		!gateValueContainsInputReference(isolatedScope["criteria"]) ||
		!gateValueContainsInputReference(isolatedScope["subject"]) ||
		!gateValueContainsInputReference(workingScope["criteria"]) ||
		!gateValueContainsInputReference(workingScope["subject"]) {
		t.Fatalf("compiled AI inputs are not invocation references: isolated=%#v working=%#v",
			isolatedDecision.With, workingDecision.With)
	}
}

func TestCompiledMixedGateWorkflowWaitsAndResumesWithoutRerunningDecisions(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	order := []string{}
	agents := &scriptedGateAgentRunner{
		order: &order,
		responses: []string{
			`{"ask_user":false,"reason":"evidence is complete","questions":[]}`,
			`{"ask_user":true,"reason":"the discussion leaves two safe choices","questions":["Which compatibility contract should be retained?"]}`,
		},
	}

	specs := []GateSpec{
		{ID: "off", Kind: GateZero},
		{
			ID:        "policy",
			Kind:      GateDeterministic,
			When:      "${{ inputs.gate_subject.risk == 'critical' }}",
			Title:     "Policy approval",
			Questions: []any{map[string]any{"id": "approved", "prompt": "Approve policy exception?"}},
		},
		{
			ID:        "isolated",
			Kind:      GateAIIsolatedContext,
			AgentID:   "reviewer",
			Criteria:  "Escalate only incomplete findings.",
			Title:     "Finding clarification",
			Questions: []any{"Focus on evidence completeness."},
		},
		{
			ID:       "working",
			Kind:     GateAIWorkingContext,
			AgentID:  "main",
			Criteria: "Use the active PR discussion and escalate unresolved product choices.",
			Title:    "PR discussion decision",
		},
	}
	subject := map[string]any{
		"pull_request": "42",
		"risk":         "critical",
		"findings": []any{
			map[string]any{"path": "compat.go", "title": "ambiguous behavior"},
		},
	}
	compilation, err := CompileGateWorkflow("Mixed gates", specs, subject)
	if err != nil {
		t.Fatalf("CompileGateWorkflow() error = %v", err)
	}
	compilation.PrivateRoot.ReadOnlySession = &ReadOnlySessionRef{
		AgentID: "main",
		Session: "agent:main:web:pr-42",
	}

	executor := &Executor{
		WorkspaceDir: workspace,
		Store:        store,
		Agents:       agents,
	}
	started, err := executor.Run(ctx, RunRequest{
		Workflow:    compilation.Workflow,
		WorkflowRef: "workflows/mixed-gates.yml",
		PrivateRoot: compilation.PrivateRoot,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if started == nil || started.Status != RunStatusWaiting {
		t.Fatalf("Run() result = %#v, want waiting", started)
	}
	if len(order) != 0 || len(agents.requests) != 0 {
		t.Fatalf("steps after first waiting gate = order %#v, requests %d", order, len(agents.requests))
	}
	tasks, err := executor.ListHumanTasks(ctx, started.RunID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("ListHumanTasks() tasks=%#v err=%v", tasks, err)
	}
	policyTask := tasks[0]
	if policyTask.StepID != "gate_policy_attention" || policyTask.Title != "Policy approval" {
		t.Fatalf("first waiting task = %#v", policyTask)
	}
	persisted, err := store.GetRun(ctx, started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Steps["gates/gate_policy_attention"].Status != RunStatusWaiting {
		t.Fatalf("first gate steps = %#v", persisted.Steps)
	}
	for _, stepID := range []string{
		"gate_isolated_decision", "gate_isolated_attention",
		"gate_working_decision", "gate_working_attention",
	} {
		if _, exists := persisted.Steps["gates/"+stepID]; exists {
			t.Fatalf("step %q ran before the first response", stepID)
		}
	}

	continued, err := executor.ResumeHumanTask(ctx, started.RunID, policyTask.ID, HumanTaskResumeRequest{
		ExpectedRevision: policyTask.Revision,
		InputHash:        policyTask.InputHash,
		ResponseID:       "policy-answer-1",
		Response:         "approved",
	})
	if err != nil {
		t.Fatalf("first ResumeHumanTask() error = %v", err)
	}
	if continued == nil || continued.Status != RunStatusWaiting {
		t.Fatalf("first ResumeHumanTask() result = %#v, want second wait", continued)
	}
	if !reflect.DeepEqual(order, []string{"agent:reviewer", "agent:main"}) {
		t.Fatalf("order after first response = %#v", order)
	}
	if len(agents.requests) != 2 {
		t.Fatalf("agent requests = %d, want two", len(agents.requests))
	}
	isolatedReq, workingReq := agents.requests[0], agents.requests[1]
	if !isolatedReq.PrivateContext || !isolatedReq.EphemeralSession || isolatedReq.Session != "" ||
		isolatedReq.History != "none" || isolatedReq.Cache != "none" ||
		isolatedReq.Tools != AgentToolsNone ||
		!reflect.DeepEqual(isolatedReq.Delivery, Delivery{}) {
		t.Fatalf("isolated request = %#v", isolatedReq)
	}
	if !workingReq.PrivateContext || workingReq.EphemeralSession ||
		workingReq.History != "read_only" || workingReq.Cache != "session" ||
		workingReq.Tools != AgentToolsNone || workingReq.Session != "" ||
		workingReq.FrozenReadOnlySession == nil ||
		workingReq.FrozenReadOnlySession.Snapshot.Key != "agent:main:web:pr-42" {
		t.Fatalf("working-context request after resume = %#v", workingReq)
	}
	if !reflect.DeepEqual(workingReq.Delivery, Delivery{}) {
		t.Fatalf("working-context delivery = %#v, want none", workingReq.Delivery)
	}
	isolatedScope, isolatedScopeOK := isolatedReq.Scope.(map[string]any)
	workingScope, workingScopeOK := workingReq.Scope.(map[string]any)
	if !isolatedScopeOK || !workingScopeOK ||
		!reflect.DeepEqual(isolatedScope["subject"], subject) ||
		!reflect.DeepEqual(workingScope["subject"], subject) ||
		!reflect.DeepEqual(
			isolatedScope["question_guidance"],
			[]any{"Focus on evidence completeness."},
		) {
		t.Fatalf("agent scopes isolated=%#v working=%#v, want nested subject %#v",
			isolatedReq.Scope, workingReq.Scope, subject)
	}

	tasks, err = executor.ListHumanTasks(ctx, started.RunID)
	if err != nil || len(tasks) != 2 {
		t.Fatalf("ListHumanTasks() after first response tasks=%#v err=%v", tasks, err)
	}
	var workingTask WorkflowHumanTask
	for _, candidate := range tasks {
		if candidate.StepID == "gate_policy_attention" && candidate.Status != HumanTaskStatusAnswered {
			t.Fatalf("first task status = %q, want answered", candidate.Status)
		}
		if candidate.StepID == "gate_working_attention" {
			workingTask = candidate
		}
	}
	if workingTask.ID == "" || workingTask.Status != HumanTaskStatusWaiting ||
		workingTask.Title != "PR discussion decision" {
		t.Fatalf("second waiting task = %#v", workingTask)
	}
	questions, ok := workingTask.Questions.(map[string]any)
	if !ok || questions["gate_id"] != "working" ||
		questions["reason"] != "the discussion leaves two safe choices" ||
		!reflect.DeepEqual(
			questions["questions"],
			[]any{"Which compatibility contract should be retained?"},
		) {
		t.Fatalf("second waiting task questions = %#v", workingTask.Questions)
	}

	persisted, err = store.GetRun(ctx, started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for stepID, wantStatus := range map[string]string{
		"gate_policy_attention":   RunStatusSucceeded,
		"gate_isolated_decision":  RunStatusSucceeded,
		"gate_isolated_attention": RunStatusSkipped,
		"gate_working_decision":   RunStatusSucceeded,
		"gate_working_attention":  RunStatusWaiting,
	} {
		if got := persisted.Steps["gates/"+stepID].Status; got != wantStatus {
			t.Fatalf("step %s status = %q, want %q", stepID, got, wantStatus)
		}
	}
	if _, exists := persisted.Steps["gates/gate_off_attention"]; exists {
		t.Fatal("zero gate created a persisted step")
	}
	response := "retain-v1"
	resumed, err := executor.ResumeHumanTask(ctx, started.RunID, workingTask.ID, HumanTaskResumeRequest{
		ExpectedRevision: workingTask.Revision,
		InputHash:        workingTask.InputHash,
		ResponseID:       "working-answer-1",
		Response:         response,
	})
	if err != nil {
		t.Fatalf("ResumeHumanTask() error = %v", err)
	}
	if resumed == nil || resumed.Status != RunStatusSucceeded {
		t.Fatalf("ResumeHumanTask() result = %#v, want succeeded", resumed)
	}
	if !reflect.DeepEqual(
		order,
		[]string{"agent:reviewer", "agent:main"},
	) {
		t.Fatalf("post-resume order = %#v; decisions were repeated or continuation misplaced", order)
	}
	if len(agents.requests) != 2 {
		t.Fatalf("agent requests after resume = %d, want no reruns", len(agents.requests))
	}
	persisted, err = store.GetRun(ctx, started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Steps["gates/gate_working_attention"].Status != RunStatusSucceeded {
		t.Fatalf("resumed steps = %#v", persisted.Steps)
	}
}

func TestCompiledDeterministicGatesExecuteFalseThenTrueInOrder(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	compilation, err := CompileGateWorkflow("Deterministic gates", []GateSpec{
		{
			ID: "skip", Kind: GateDeterministic,
			When: "${{ inputs.gate_subject.skip == true }}", Title: "Skipped",
			Questions: []any{"This question must not be presented."},
		},
		{
			ID: "ask", Kind: GateDeterministic,
			When: "inputs.gate_subject.ask == true", Title: "Required approval",
			Questions: []any{"Approve the deterministic result?"},
		},
	}, map[string]any{"skip": false, "ask": true})
	if err != nil {
		t.Fatalf("CompileGateWorkflow() error = %v", err)
	}
	executor := &Executor{WorkspaceDir: workspace, Store: store}
	started, err := executor.Run(ctx, RunRequest{
		Workflow: compilation.Workflow, WorkflowRef: "workflows/deterministic-gates.yml",
		PrivateRoot: compilation.PrivateRoot,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if started.Status != RunStatusWaiting {
		t.Fatalf("Run() status = %q, want waiting", started.Status)
	}
	tasks, err := executor.ListHumanTasks(ctx, started.RunID)
	if err != nil {
		t.Fatalf("ListHumanTasks() error = %v", err)
	}
	if len(tasks) != 1 || tasks[0].StepID != "gate_ask_attention" ||
		tasks[0].Title != "Required approval" {
		t.Fatalf("deterministic tasks = %#v, want only true gate", tasks)
	}
	persisted, err := store.GetRun(ctx, started.RunID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if got := persisted.Steps["gates/gate_skip_attention"].Status; got != RunStatusSkipped {
		t.Fatalf("false deterministic gate status = %q, want skipped", got)
	}
	if got := persisted.Steps["gates/gate_ask_attention"].Status; got != RunStatusWaiting {
		t.Fatalf("true deterministic gate status = %q, want waiting", got)
	}
	resumed, err := executor.ResumeHumanTask(ctx, started.RunID, tasks[0].ID, HumanTaskResumeRequest{
		ExpectedRevision: tasks[0].Revision,
		InputHash:        tasks[0].InputHash,
		ResponseID:       "deterministic-answer-1",
		Response:         "approved",
	})
	if err != nil {
		t.Fatalf("ResumeHumanTask() error = %v", err)
	}
	if resumed.Status != RunStatusSucceeded {
		t.Fatalf("ResumeHumanTask() status = %q, want succeeded", resumed.Status)
	}
}

func TestCompileGateWorkflowPreservesLiteralExpressionTextAsData(t *testing.T) {
	literal := "show ${{ secrets.token }} and ${{ steps.fake.outputs.value }} literally"
	modelLiteral := "model says ${{ secrets.token }} and ${{ steps.fake.outputs.value }} literally"
	nested := map[string]any{"message": literal}
	subject := map[string]any{
		"finding": literal,
		"nested":  []any{nested},
	}
	guidanceItems := []any{"retain original guidance", map[string]any{"literal": literal}}
	guidance := map[string]any{"focus": guidanceItems}
	spec := GateSpec{
		ID:        "literal",
		Kind:      GateAIIsolatedContext,
		AgentID:   "reviewer",
		Criteria:  "Evaluate this exact policy text: " + literal,
		Title:     "Clarify " + literal,
		Questions: guidance,
	}
	compilation, err := CompileGateWorkflow("Literal text", []GateSpec{spec}, subject)
	if err != nil {
		t.Fatalf("CompileGateWorkflow() error = %v", err)
	}
	if validationErr := Validate(compilation.Workflow); validationErr != nil {
		t.Fatalf("compiled workflow validation error = %v", validationErr)
	}

	// Mutating caller-owned values after compilation must not change the
	// invocation snapshot retained by the compilation.
	subject["finding"] = "mutated"
	nested["message"] = "mutated"
	guidanceItems[0] = "mutated"
	guidance["added"] = true
	decision, err := json.Marshal(map[string]any{
		"ask_user": true,
		"reason":   modelLiteral,
		"questions": []any{
			modelLiteral,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	agents := &scriptedGateAgentRunner{responses: []string{string(decision)}}
	executor := &Executor{WorkspaceDir: t.TempDir(), Agents: agents}
	result, err := executor.Run(context.Background(), RunRequest{
		Workflow:    compilation.Workflow,
		WorkflowRef: "workflows/literal-gate.yml",
		PrivateRoot: compilation.PrivateRoot,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != RunStatusWaiting || len(agents.requests) != 1 {
		t.Fatalf("Run() result=%#v requests=%d, want one decision and wait", result, len(agents.requests))
	}
	req := agents.requests[0]
	wantScope := map[string]any{
		"gate_id":  "literal",
		"criteria": spec.Criteria,
		"subject": map[string]any{
			"finding": literal,
			"nested":  []any{map[string]any{"message": literal}},
		},
		"question_guidance": map[string]any{
			"focus": []any{"retain original guidance", map[string]any{"literal": literal}},
		},
	}
	if !reflect.DeepEqual(req.Scope, wantScope) {
		t.Fatalf("agent scope = %#v, want immutable literal %#v", req.Scope, wantScope)
	}
	tasks, err := executor.ListHumanTasks(context.Background(), result.RunID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("ListHumanTasks() tasks=%#v err=%v", tasks, err)
	}
	if tasks[0].Title != spec.Title {
		t.Fatalf("task title = %q, want literal %q", tasks[0].Title, spec.Title)
	}
	questions, ok := tasks[0].Questions.(map[string]any)
	if !ok || questions["reason"] != modelLiteral ||
		!reflect.DeepEqual(questions["questions"], []any{modelLiteral}) {
		t.Fatalf("model-authored literal questions = %#v", tasks[0].Questions)
	}

	deterministicQuestions := map[string]any{
		"prompt": literal,
		"nested": []any{map[string]any{"literal": literal}},
	}
	deterministic, err := CompileGateWorkflow("Literal deterministic data", []GateSpec{{
		ID: "literal_policy", Kind: GateDeterministic, When: "true",
		Title: spec.Title, Questions: deterministicQuestions,
	}}, map[string]any{})
	if err != nil {
		t.Fatalf("deterministic CompileGateWorkflow() error = %v", err)
	}
	deterministicQuestions["prompt"] = "mutated"
	deterministicWorkspace := t.TempDir()
	deterministicExecutor := &Executor{WorkspaceDir: deterministicWorkspace}
	deterministicResult, err := deterministicExecutor.Run(
		context.Background(),
		RunRequest{
			Workflow: deterministic.Workflow, WorkflowRef: "workflows/literal-deterministic-gate.yml",
			PrivateRoot: deterministic.PrivateRoot,
		},
	)
	if err != nil {
		t.Fatalf("deterministic Run() error = %v", err)
	}
	deterministicTasks, err := deterministicExecutor.ListHumanTasks(
		context.Background(),
		deterministicResult.RunID,
	)
	if err != nil || len(deterministicTasks) != 1 {
		t.Fatalf("deterministic tasks=%#v err=%v", deterministicTasks, err)
	}
	wantDeterministicQuestions := map[string]any{
		"prompt": literal,
		"nested": []any{map[string]any{"literal": literal}},
	}
	if !reflect.DeepEqual(deterministicTasks[0].Questions, wantDeterministicQuestions) {
		t.Fatalf(
			"deterministic literal questions = %#v, want %#v",
			deterministicTasks[0].Questions,
			wantDeterministicQuestions,
		)
	}
}

func TestCompileGateWorkflowNormalizesAndFreezesTypedJSONInputs(t *testing.T) {
	subject := map[string]string{"risk": "normal"}
	questions := []string{"Approve?", "Explain any exception."}
	compilation, err := CompileGateWorkflow("Normalized inputs", []GateSpec{{
		ID: "policy", Kind: GateDeterministic,
		When: "inputs.gate_subject.risk == 'critical'", Title: "Policy",
		Questions: questions,
	}}, subject)
	if err != nil {
		t.Fatalf("CompileGateWorkflow() error = %v", err)
	}
	subject["risk"] = "critical"
	questions[0] = "mutated"

	gotSubject := compilation.PrivateRoot.Values[workflowGateSubjectInput]
	wantSubject := map[string]any{"risk": "normal"}
	if !reflect.DeepEqual(gotSubject, wantSubject) {
		t.Fatalf("normalized subject = %#v, want %#v", gotSubject, wantSubject)
	}
	gateInputs, ok := compilation.PrivateRoot.Values[workflowGateSpecsInput].(map[string]any)
	if !ok {
		t.Fatalf("normalized gate inputs = %#v", compilation.PrivateRoot.Values[workflowGateSpecsInput])
	}
	policy, ok := gateInputs["policy"].(map[string]any)
	if !ok || !reflect.DeepEqual(
		policy["questions"],
		[]any{"Approve?", "Explain any exception."},
	) {
		t.Fatalf("normalized frozen policy input = %#v", policy)
	}
}

func TestCompiledAIGateRejectsInvalidStructuredDecision(t *testing.T) {
	for _, response := range []string{
		`not JSON`,
		`{"ask_user":true,"reason":"missing questions"}`,
		`{"ask_user":1,"reason":"wrong type","questions":[]}`,
		`{"ask_user":true,"reason":"extra field","questions":[],"action":"merge"}`,
	} {
		t.Run(gateTestName(response), func(t *testing.T) {
			compilation, err := CompileGateWorkflow("Invalid AI decision", []GateSpec{{
				ID: "review", Kind: GateAIIsolatedContext, AgentID: "reviewer",
				Criteria: "Escalate ambiguity.", Title: "Clarify",
			}}, map[string]any{"pr": 42})
			if err != nil {
				t.Fatalf("CompileGateWorkflow() error = %v", err)
			}
			agents := &scriptedGateAgentRunner{responses: []string{response}}
			result, runErr := (&Executor{WorkspaceDir: t.TempDir(), Agents: agents}).Run(
				context.Background(),
				RunRequest{
					Workflow: compilation.Workflow, WorkflowRef: "workflows/invalid-gate.yml",
					PrivateRoot: compilation.PrivateRoot,
				},
			)
			if runErr == nil {
				t.Fatalf("Run() result = %#v, error = nil, want invalid structured output failure", result)
			}
			if len(agents.requests) != 1 || agents.requests[0].Output == nil ||
				agents.requests[0].Output.RepairAttempts != 1 {
				t.Fatalf("invalid-decision requests = %#v", agents.requests)
			}
		})
	}
}

func TestCompileGateWorkflowAllZeroIsNoop(t *testing.T) {
	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	compilation, err := CompileGateWorkflow("No attention", []GateSpec{
		{ID: "global_off", Kind: GateZero},
		{ID: "repo_off", Kind: GateZero},
	}, cyclic)
	if err != nil {
		t.Fatalf("CompileGateWorkflow() error = %v", err)
	}
	if compilation == nil || !compilation.Noop || compilation.Workflow != nil {
		t.Fatalf("all-zero compilation = %#v, want explicit no-op without workflow", compilation)
	}
	if compilation.RequiresSession {
		t.Fatal("all-zero compilation requires a session")
	}
	if compilation.RequiredSessionAgentID != "" {
		t.Fatalf("all-zero RequiredSessionAgentID = %q, want empty", compilation.RequiredSessionAgentID)
	}
	if !reflect.DeepEqual(compilation.GateIDs, []string{"global_off", "repo_off"}) {
		t.Fatalf("GateIDs = %#v, want configured zero gates in order", compilation.GateIDs)
	}
	empty, err := CompileGateWorkflow("Empty", nil, make(chan int))
	if err != nil || empty == nil || !empty.Noop || empty.Workflow != nil {
		t.Fatalf("empty compilation = %#v, error = %v, want no-op before subject validation", empty, err)
	}
}

func TestCompileGateWorkflowValidatesInputsAndBounds(t *testing.T) {
	questions := []any{map[string]any{"id": "approved", "prompt": "Approve?"}}
	validAI := GateSpec{
		ID: "ai", Kind: GateAIIsolatedContext, AgentID: "reviewer",
		Criteria: "Escalate ambiguity.", Title: "Clarify",
	}
	tooMany := make([]GateSpec, MaxWorkflowGateCount+1)
	for index := range tooMany {
		tooMany[index] = GateSpec{ID: fmt.Sprintf("gate_%d", index), Kind: GateZero}
	}
	tests := []struct {
		name    string
		plan    string
		specs   []GateSpec
		subject any
	}{
		{name: "blank workflow name", specs: []GateSpec{validAI}, subject: map[string]any{}},
		{
			name: "oversized workflow name", plan: strings.Repeat("n", MaxWorkflowGateNameBytes+1),
			specs: []GateSpec{validAI}, subject: map[string]any{},
		},
		{name: "too many gates", plan: "Invalid", specs: tooMany, subject: map[string]any{}},
		{
			name: "blank gate id", plan: "Invalid",
			specs: []GateSpec{{Kind: GateZero}}, subject: map[string]any{},
		},
		{
			name: "unsafe gate id", plan: "Invalid",
			specs: []GateSpec{{ID: "bad.id", Kind: GateZero}}, subject: map[string]any{},
		},
		{
			name: "oversized gate id", plan: "Invalid",
			specs:   []GateSpec{{ID: "g" + strings.Repeat("x", MaxWorkflowGateIDBytes), Kind: GateZero}},
			subject: map[string]any{},
		},
		{
			name: "duplicate gate id", plan: "Invalid",
			specs:   []GateSpec{{ID: "same", Kind: GateZero}, {ID: "same", Kind: GateZero}},
			subject: map[string]any{},
		},
		{
			name: "unknown kind", plan: "Invalid",
			specs: []GateSpec{{ID: "unknown", Kind: "surprise"}}, subject: map[string]any{},
		},
		{
			name: "AI missing agent", plan: "Invalid",
			specs:   []GateSpec{{ID: "ai", Kind: GateAIIsolatedContext, Criteria: "decide", Title: "Ask"}},
			subject: map[string]any{},
		},
		{
			name: "AI noncanonical agent", plan: "Invalid",
			specs: []GateSpec{{
				ID: "ai", Kind: GateAIWorkingContext, AgentID: "Main", Criteria: "decide", Title: "Ask",
			}},
			subject: map[string]any{},
		},
		{
			name: "AI missing criteria", plan: "Invalid",
			specs:   []GateSpec{{ID: "ai", Kind: GateAIIsolatedContext, AgentID: "reviewer", Title: "Ask"}},
			subject: map[string]any{},
		},
		{
			name: "AI deterministic condition field", plan: "Invalid",
			specs: []GateSpec{{
				ID: "ai", Kind: GateAIIsolatedContext, AgentID: "reviewer",
				Criteria: "decide", When: "true", Title: "Ask",
			}},
			subject: map[string]any{},
		},
		{
			name: "AI whitespace deterministic condition field", plan: "Invalid",
			specs: []GateSpec{{
				ID: "ai", Kind: GateAIIsolatedContext, AgentID: "reviewer",
				Criteria: "decide", When: " ", Title: "Ask",
			}},
			subject: map[string]any{},
		},
		{
			name: "attention title missing", plan: "Invalid",
			specs:   []GateSpec{{ID: "ai", Kind: GateAIIsolatedContext, AgentID: "reviewer", Criteria: "decide"}},
			subject: map[string]any{},
		},
		{
			name: "deterministic condition missing", plan: "Invalid",
			specs:   []GateSpec{{ID: "policy", Kind: GateDeterministic, Title: "Ask", Questions: questions}},
			subject: map[string]any{},
		},
		{
			name: "deterministic condition unsupported", plan: "Invalid",
			specs: []GateSpec{{
				ID: "policy", Kind: GateDeterministic, When: "inputs.risk && true",
				Title: "Ask", Questions: questions,
			}},
			subject: map[string]any{},
		},
		{
			name: "deterministic condition oversized", plan: "Invalid",
			specs: []GateSpec{{
				ID: "policy", Kind: GateDeterministic,
				When:  "'" + strings.Repeat("x", MaxWorkflowGateConditionBytes) + "'",
				Title: "Ask", Questions: questions,
			}},
			subject: map[string]any{},
		},
		{
			name: "deterministic condition invalid UTF-8", plan: "Invalid",
			specs: []GateSpec{{
				ID: "policy", Kind: GateDeterministic,
				When: string([]byte{0xff}), Title: "Ask", Questions: questions,
			}},
			subject: map[string]any{},
		},
		{
			name: "deterministic condition input missing", plan: "Invalid",
			specs: []GateSpec{{
				ID: "policy", Kind: GateDeterministic,
				When: "inputs.gate_subject.missing == true", Title: "Ask", Questions: questions,
			}},
			subject: map[string]any{"present": true},
		},
		{
			name: "deterministic questions missing", plan: "Invalid",
			specs:   []GateSpec{{ID: "policy", Kind: GateDeterministic, When: "true", Title: "Ask"}},
			subject: map[string]any{},
		},
		{
			name: "deterministic agent field", plan: "Invalid",
			specs: []GateSpec{{
				ID: "policy", Kind: GateDeterministic, AgentID: "main",
				When: "true", Title: "Ask", Questions: questions,
			}},
			subject: map[string]any{},
		},
		{
			name: "deterministic whitespace agent field", plan: "Invalid",
			specs: []GateSpec{{
				ID: "policy", Kind: GateDeterministic, AgentID: " ",
				When: "true", Title: "Ask", Questions: questions,
			}},
			subject: map[string]any{},
		},
		{
			name: "deterministic criteria field", plan: "Invalid",
			specs: []GateSpec{{
				ID: "policy", Kind: GateDeterministic, Criteria: "decide",
				When: "true", Title: "Ask", Questions: questions,
			}},
			subject: map[string]any{},
		},
		{
			name: "zero agent field", plan: "Invalid",
			specs:   []GateSpec{{ID: "off", Kind: GateZero, AgentID: "main"}},
			subject: map[string]any{},
		},
		{
			name: "zero criteria field", plan: "Invalid",
			specs:   []GateSpec{{ID: "off", Kind: GateZero, Criteria: "ignore"}},
			subject: map[string]any{},
		},
		{
			name: "zero condition field", plan: "Invalid",
			specs:   []GateSpec{{ID: "off", Kind: GateZero, When: "false"}},
			subject: map[string]any{},
		},
		{
			name: "zero title field", plan: "Invalid",
			specs:   []GateSpec{{ID: "off", Kind: GateZero, Title: "Off"}},
			subject: map[string]any{},
		},
		{
			name: "zero whitespace title field", plan: "Invalid",
			specs:   []GateSpec{{ID: "off", Kind: GateZero, Title: " "}},
			subject: map[string]any{},
		},
		{
			name: "zero questions field", plan: "Invalid",
			specs:   []GateSpec{{ID: "off", Kind: GateZero, Questions: []any{}}},
			subject: map[string]any{},
		},
		{
			name: "working gates have different session owners", plan: "Invalid",
			specs: []GateSpec{
				{ID: "first", Kind: GateAIWorkingContext, AgentID: "main", Criteria: "decide", Title: "Ask"},
				{ID: "second", Kind: GateAIWorkingContext, AgentID: "reviewer", Criteria: "decide", Title: "Ask"},
			},
			subject: map[string]any{},
		},
		{
			name: "non JSON subject", plan: "Invalid", specs: []GateSpec{validAI},
			subject: make(chan int),
		},
		{
			name: "oversized criteria", plan: "Invalid",
			specs: []GateSpec{{
				ID: "ai", Kind: GateAIIsolatedContext, AgentID: "reviewer",
				Criteria: strings.Repeat("x", MaxWorkflowGateCriteriaBytes+1), Title: "Ask",
			}},
			subject: map[string]any{},
		},
		{
			name: "oversized title", plan: "Invalid",
			specs: []GateSpec{{
				ID: "ai", Kind: GateAIIsolatedContext, AgentID: "reviewer",
				Criteria: "decide", Title: strings.Repeat("x", MaxHumanTaskTitleBytes+1),
			}},
			subject: map[string]any{},
		},
		{
			name: "oversized questions", plan: "Invalid",
			specs: []GateSpec{{
				ID: "ai", Kind: GateAIIsolatedContext, AgentID: "reviewer",
				Criteria: "decide", Title: "Ask",
				Questions: strings.Repeat("x", MaxWorkflowGateQuestionBytes-1),
			}},
			subject: map[string]any{},
		},
		{
			name: "oversized subject", plan: "Invalid", specs: []GateSpec{validAI},
			subject: strings.Repeat("x", MaxWorkflowGateSubjectBytes-1),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := CompileGateWorkflow(test.plan, test.specs, test.subject); err == nil {
				t.Fatal("CompileGateWorkflow() error = nil, want validation failure")
			}
		})
	}
}

func TestCompileGateWorkflowAcceptsExactLimits(t *testing.T) {
	t.Run("text and JSON byte limits", func(t *testing.T) {
		gateID := "g" + strings.Repeat("x", MaxWorkflowGateIDBytes-1)
		compilation, err := CompileGateWorkflow(
			strings.Repeat("n", MaxWorkflowGateNameBytes),
			[]GateSpec{{
				ID:        gateID,
				Kind:      GateAIIsolatedContext,
				AgentID:   "reviewer",
				Criteria:  strings.Repeat("c", MaxWorkflowGateCriteriaBytes),
				Title:     strings.Repeat("t", MaxHumanTaskTitleBytes),
				Questions: strings.Repeat("q", MaxWorkflowGateQuestionBytes-2),
			}},
			strings.Repeat("s", MaxWorkflowGateSubjectBytes-2),
		)
		if err != nil {
			t.Fatalf("CompileGateWorkflow() exact limits error = %v", err)
		}
		if compilation.Workflow == nil || !reflect.DeepEqual(compilation.GateIDs, []string{gateID}) {
			t.Fatalf("exact-limit compilation = %#v", compilation)
		}
	})

	t.Run("condition bytes", func(t *testing.T) {
		condition := "'" + strings.Repeat("x", MaxWorkflowGateConditionBytes-2) + "'"
		compilation, err := CompileGateWorkflow("Exact condition", []GateSpec{{
			ID: "policy", Kind: GateDeterministic, When: condition,
			Title: "Policy", Questions: []any{"Review?"},
		}}, map[string]any{})
		if err != nil {
			t.Fatalf("CompileGateWorkflow() exact condition error = %v", err)
		}
		if got := compilation.Workflow.Jobs[workflowGateJobID].Steps[0].If; got != "${{ "+condition+" }}" {
			t.Fatalf("compiled exact condition = %q", got)
		}
	})

	t.Run("gate count", func(t *testing.T) {
		specs := make([]GateSpec, MaxWorkflowGateCount)
		for index := range specs {
			specs[index] = GateSpec{ID: fmt.Sprintf("gate_%d", index), Kind: GateZero}
		}
		compilation, err := CompileGateWorkflow("Exact count", specs, nil)
		if err != nil {
			t.Fatalf("CompileGateWorkflow() exact count error = %v", err)
		}
		if !compilation.Noop || len(compilation.GateIDs) != MaxWorkflowGateCount {
			t.Fatalf("exact-count compilation = %#v", compilation)
		}
	})

	t.Run("JSON depth", func(t *testing.T) {
		var subject any = "leaf"
		for range MaxWorkflowGateJSONDepth {
			subject = map[string]any{"nested": subject}
		}
		if _, err := CompileGateWorkflow("Exact JSON depth", []GateSpec{{
			ID: "review", Kind: GateAIIsolatedContext, AgentID: "reviewer",
			Criteria: "Review nested evidence.", Title: "Review",
		}}, subject); err != nil {
			t.Fatalf("CompileGateWorkflow() exact JSON depth error = %v", err)
		}
	})

	t.Run("JSON nodes", func(t *testing.T) {
		subject := make([]any, MaxWorkflowGateJSONNodes-1)
		if _, err := CompileGateWorkflow("Exact JSON nodes", []GateSpec{{
			ID: "review", Kind: GateAIIsolatedContext, AgentID: "reviewer",
			Criteria: "Review broad evidence.", Title: "Review",
		}}, subject); err != nil {
			t.Fatalf("CompileGateWorkflow() exact JSON nodes error = %v", err)
		}
	})

	t.Run("aggregate input bytes", func(t *testing.T) {
		specs := make([]GateSpec, 8)
		for index := range specs {
			specs[index] = GateSpec{
				ID: fmt.Sprintf("review_%d", index), Kind: GateAIIsolatedContext,
				AgentID: "reviewer", Criteria: "Review aggregate evidence.", Title: "Review",
			}
			if index < len(specs)-1 {
				specs[index].Questions = strings.Repeat("q", MaxWorkflowGateQuestionBytes-2)
			} else {
				specs[index].Questions = ""
			}
		}
		subject := strings.Repeat("s", MaxWorkflowGateSubjectBytes-2)
		baseline, err := CompileGateWorkflow("Aggregate byte boundary", specs, subject)
		if err != nil {
			t.Fatalf("CompileGateWorkflow() aggregate baseline error = %v", err)
		}
		encoded, err := json.Marshal(baseline.PrivateRoot.Values)
		if err != nil {
			t.Fatal(err)
		}
		remaining := MaxWorkflowGateInputsBytes - len(encoded)
		if remaining < 0 || remaining > MaxWorkflowGateQuestionBytes-2 {
			t.Fatalf("aggregate boundary remainder = %d", remaining)
		}
		specs[len(specs)-1].Questions = strings.Repeat("z", remaining)
		compilation, err := CompileGateWorkflow("Aggregate byte boundary", specs, subject)
		if err != nil {
			t.Fatalf("CompileGateWorkflow() exact aggregate bytes error = %v", err)
		}
		encoded, err = json.Marshal(compilation.PrivateRoot.Values)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) != MaxWorkflowGateInputsBytes {
			t.Fatalf("aggregate inputs encode to %d bytes, want %d", len(encoded), MaxWorkflowGateInputsBytes)
		}
	})

	t.Run("compact numeric subject", func(t *testing.T) {
		subject := make([]int, MaxWorkflowGateSubjectBytes/32)
		encoded, err := json.Marshal(subject)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) >= MaxWorkflowGateSubjectBytes {
			t.Fatalf("numeric regression subject encodes to %d bytes", len(encoded))
		}
		if _, err := CompileGateWorkflow("Compact numeric input", []GateSpec{{
			ID: "review", Kind: GateAIIsolatedContext, AgentID: "reviewer",
			Criteria: "Review compact numeric evidence.", Title: "Review",
		}}, subject); err != nil {
			t.Fatalf("CompileGateWorkflow() compact numeric input error = %v", err)
		}
	})
}

func TestCompileGateWorkflowRestrictsDeterministicExpressionRootsAndPaths(t *testing.T) {
	subject := map[string]any{
		"risk":     "critical",
		"disabled": false,
		"nested":   map[string]any{"approved": true},
		"items":    []any{map[string]any{"approved": true}},
	}
	base := GateSpec{
		ID: "policy", Kind: GateDeterministic, Title: "Policy",
		Questions: []any{"Review?"},
	}
	for _, condition := range []string{
		"true",
		"not inputs.gate_subject.disabled",
		"${{ inputs.gate_subject.risk == 'critical' }}",
		"inputs.gate_subject.nested.approved == true",
		"inputs._gates.policy.title == 'Policy'",
	} {
		t.Run("accept_"+gateTestName(condition), func(t *testing.T) {
			spec := base
			spec.When = condition
			if _, err := CompileGateWorkflow("Valid condition", []GateSpec{spec}, subject); err != nil {
				t.Fatalf("CompileGateWorkflow() condition %q error = %v", condition, err)
			}
		})
	}

	for _, test := range []struct {
		condition string
		want      string
	}{
		{condition: "steps.review.outputs.approved == true", want: "root"},
		{condition: "secrets.token == 'value'", want: "root"},
		{condition: "inputs.gate_subject.missing == true", want: "does not exist"},
		{condition: "inputs.gate_subject.items.0.approved == true", want: "does not exist"},
		{condition: "inputs._gates.missing.title == 'Policy'", want: "does not exist"},
		{condition: "${{ inputs.gate_subject.risk == 'critical'", want: "delimiters"},
	} {
		t.Run("reject_"+gateTestName(test.condition), func(t *testing.T) {
			spec := base
			spec.When = test.condition
			_, err := CompileGateWorkflow("Invalid condition", []GateSpec{spec}, subject)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("CompileGateWorkflow() condition %q error = %v, want %q", test.condition, err, test.want)
			}
		})
	}
}

func TestCompileGateWorkflowUsesExactEphemeralProfileAcrossCompilationsAndRuns(t *testing.T) {
	spec := GateSpec{
		ID: "review", Kind: GateAIIsolatedContext, AgentID: "reviewer",
		Criteria: "Escalate ambiguity.", Title: "Clarify",
	}
	first, err := CompileGateWorkflow("First invocation", []GateSpec{spec}, map[string]any{"pr": 42})
	if err != nil {
		t.Fatalf("first CompileGateWorkflow() error = %v", err)
	}
	second, err := CompileGateWorkflow("Second invocation", []GateSpec{spec}, map[string]any{"pr": 42})
	if err != nil {
		t.Fatalf("second CompileGateWorkflow() error = %v", err)
	}
	firstSession := first.Workflow.Jobs[workflowGateJobID].Steps[0].With["session"]
	secondSession := second.Workflow.Jobs[workflowGateJobID].Steps[0].With["session"]
	if firstSession != AgentSessionEphemeral || secondSession != AgentSessionEphemeral {
		t.Fatalf(
			"isolated sessions = %#v and %#v, want exact %q profile",
			firstSession,
			secondSession,
			AgentSessionEphemeral,
		)
	}

	agents := &scriptedGateAgentRunner{responses: []string{
		`{"ask_user":false,"reason":"first run is complete","questions":[]}`,
		`{"ask_user":false,"reason":"second run is complete","questions":[]}`,
	}}
	executor := &Executor{WorkspaceDir: t.TempDir(), Agents: agents}
	for index, compilation := range []*GateCompilation{first, second} {
		result, runErr := executor.Run(context.Background(), RunRequest{
			Workflow:    compilation.Workflow,
			WorkflowRef: fmt.Sprintf("workflows/ephemeral-gate-%d.yml", index+1),
			PrivateRoot: compilation.PrivateRoot,
		})
		if runErr != nil {
			t.Fatalf("run %d error = %v", index+1, runErr)
		}
		if result.Status != RunStatusSucceeded {
			t.Fatalf("run %d status = %q, want succeeded", index+1, result.Status)
		}
	}
	if len(agents.requests) != 2 {
		t.Fatalf("agent requests = %d, want one per run", len(agents.requests))
	}
	for index, req := range agents.requests {
		if !req.EphemeralSession || req.Session != "" ||
			req.History != "none" || req.Cache != "none" || req.Tools != AgentToolsNone {
			t.Fatalf("run %d isolated request = %#v", index+1, req)
		}
	}
}

func TestCompiledEphemeralGateWorkflowCanRunConcurrently(t *testing.T) {
	const runCount = 8
	compilation, err := CompileGateWorkflow("Concurrent isolated gate", []GateSpec{{
		ID: "review", Kind: GateAIIsolatedContext, AgentID: "reviewer",
		Criteria: "Escalate ambiguity.", Title: "Clarify",
	}}, map[string]any{"pr": 42})
	if err != nil {
		t.Fatalf("CompileGateWorkflow() error = %v", err)
	}

	workspaces := make([]string, runCount)
	for index := range workspaces {
		workspaces[index] = t.TempDir()
	}
	results := make(chan error, runCount)
	for index := range runCount {
		go func() {
			agents := &scriptedGateAgentRunner{responses: []string{
				`{"ask_user":false,"reason":"evidence is complete","questions":[]}`,
			}}
			result, runErr := (&Executor{
				WorkspaceDir: workspaces[index],
				Agents:       agents,
			}).Run(context.Background(), RunRequest{
				Workflow:    compilation.Workflow,
				WorkflowRef: "workflows/concurrent-isolated-gate.yml",
				PrivateRoot: compilation.PrivateRoot,
			})
			if runErr != nil {
				results <- fmt.Errorf("run %d: %w", index, runErr)
				return
			}
			if result.Status != RunStatusSucceeded {
				results <- fmt.Errorf("run %d status = %q", index, result.Status)
				return
			}
			if len(agents.requests) != 1 || !agents.requests[0].EphemeralSession ||
				agents.requests[0].Session != "" {
				results <- fmt.Errorf("run %d request = %#v", index, agents.requests)
				return
			}
			results <- nil
		}()
	}
	for range runCount {
		if runErr := <-results; runErr != nil {
			t.Fatal(runErr)
		}
	}
}

func TestCompileGateWorkflowBoundsJSONBeforeEncoding(t *testing.T) {
	validAI := GateSpec{
		ID: "ai", Kind: GateAIIsolatedContext, AgentID: "reviewer",
		Criteria: "Escalate ambiguity.", Title: "Clarify",
	}

	deep := any("leaf")
	for index := 0; index <= MaxWorkflowGateJSONDepth; index++ {
		deep = map[string]any{"nested": deep}
	}
	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	manyNodes := make([]any, MaxWorkflowGateJSONNodes+1)

	for _, test := range []struct {
		name    string
		subject any
		want    string
	}{
		{name: "depth", subject: deep, want: "JSON depth"},
		{name: "cycle", subject: cyclic, want: "acyclic JSON"},
		{name: "nodes", subject: manyNodes, want: "JSON nodes"},
		{name: "custom JSON marshaler", subject: panickingGateJSONMarshaler("value"), want: "custom marshaler"},
		{name: "custom text marshaler", subject: panickingGateTextMarshaler("value"), want: "custom marshaler"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := CompileGateWorkflow("Bounded JSON", []GateSpec{validAI}, test.subject)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("CompileGateWorkflow() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestWorkflowGateJSONPreflightMatchesEncoding(t *testing.T) {
	values := []any{
		nil,
		true,
		false,
		"escape <html> & control\ntext",
		int64(-42),
		uint64(42),
		float32(1.25),
		float64(1.25e100),
		json.Number("12345678901234567890.125"),
		map[string]any{"html<&": []any{true, nil, "text"}},
		map[string]string{"risk": "critical"},
		[]byte{0, 1, 2, 255},
		[]byte{},
		[]byte(nil),
		[3]int{1, 2, 3},
	}
	for index, value := range values {
		t.Run(fmt.Sprintf("value_%d", index), func(t *testing.T) {
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			budget := &workflowGateJSONBudget{
				label: "test value", maxBytes: len(encoded),
				active: make(map[workflowGateJSONVisit]struct{}),
			}
			if err := budget.visit(reflect.ValueOf(value), 0); err != nil {
				t.Fatalf("exact preflight error = %v", err)
			}
			if budget.bytes != len(encoded) {
				t.Fatalf("preflight bytes = %d, encoding bytes = %d", budget.bytes, len(encoded))
			}
			if err := preflightWorkflowGateJSON("test value", value, len(encoded)-1); err == nil {
				t.Fatal("preflight below exact encoded size succeeded")
			}
		})
	}
}

func TestCompileGateWorkflowBoundsAggregateInputs(t *testing.T) {
	specs := make([]GateSpec, 8)
	for index := range specs {
		specs[index] = GateSpec{
			ID:        fmt.Sprintf("ai_%d", index),
			Kind:      GateAIIsolatedContext,
			AgentID:   "reviewer",
			Criteria:  "Escalate ambiguity.",
			Title:     "Clarify",
			Questions: strings.Repeat("q", MaxWorkflowGateQuestionBytes-2),
		}
	}
	_, err := CompileGateWorkflow(
		"Aggregate bound",
		specs,
		strings.Repeat("s", MaxWorkflowGateSubjectBytes-2),
	)
	if err == nil || !strings.Contains(err.Error(), "gate inputs exceed") {
		t.Fatalf("CompileGateWorkflow() error = %v, want aggregate input bound", err)
	}
}

type panickingGateJSONMarshaler string

func (panickingGateJSONMarshaler) MarshalJSON() ([]byte, error) {
	panic("gate JSON preflight executed a custom JSON marshaler")
}

type panickingGateTextMarshaler string

func (panickingGateTextMarshaler) MarshalText() ([]byte, error) {
	panic("gate JSON preflight executed a custom text marshaler")
}

func TestCompileGateWorkflowAllowsRepeatedKindsForSameDecisionPoint(t *testing.T) {
	compilation, err := CompileGateWorkflow("Layered gates", []GateSpec{
		{
			ID: "global_policy", Kind: GateDeterministic, When: "false", Title: "Global",
			Questions: []any{"global"},
		},
		{
			ID: "repo_policy", Kind: GateDeterministic, When: "false", Title: "Repository",
			Questions: []any{"repo"},
		},
		{
			ID: "global_isolated", Kind: GateAIIsolatedContext, AgentID: "reviewer",
			Criteria: "Apply isolated global criteria.", Title: "Global isolated AI",
		},
		{
			ID: "repo_isolated", Kind: GateAIIsolatedContext, AgentID: "reviewer",
			Criteria: "Apply isolated repository criteria.", Title: "Repository isolated AI",
		},
		{
			ID: "global_ai", Kind: GateAIWorkingContext, AgentID: "main",
			Criteria: "Apply global criteria.", Title: "Global AI",
		},
		{
			ID: "repo_ai", Kind: GateAIWorkingContext, AgentID: "main",
			Criteria: "Apply repository criteria.", Title: "Repository AI",
		},
	}, map[string]any{"same_decision_point": true})
	if err != nil {
		t.Fatalf("CompileGateWorkflow() repeated deterministic gates error = %v", err)
	}
	if !compilation.RequiresSession || compilation.RequiredSessionAgentID != "main" {
		t.Fatalf("repeated working gate session requirement = %#v", compilation)
	}
	job := compilation.Workflow.Jobs["gates"]
	if got, want := gateStepIDs(job.Steps), []string{
		"gate_global_policy_attention", "gate_repo_policy_attention",
		"gate_global_isolated_decision", "gate_global_isolated_attention",
		"gate_repo_isolated_decision", "gate_repo_isolated_attention",
		"gate_global_ai_decision", "gate_global_ai_attention",
		"gate_repo_ai_decision", "gate_repo_ai_attention",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ordered repeated gates = %#v, want %#v", got, want)
	}
	for _, stepIndex := range []int{2, 4} {
		decision := job.Steps[stepIndex]
		if decision.With["session"] != AgentSessionEphemeral ||
			decision.With["history"] != "none" || decision.With["cache"] != "none" ||
			decision.With["tools"] != AgentToolsNone {
			t.Fatalf("repeated isolated decision %q = %#v", decision.ID, decision.With)
		}
	}
}

type scriptedGateAgentRunner struct {
	responses []string
	requests  []AgentRequest
	order     *[]string
	captures  []ReadOnlySessionRef
}

func (r *scriptedGateAgentRunner) CaptureReadOnlySession(
	_ context.Context,
	ref ReadOnlySessionRef,
) (*FrozenReadOnlySession, error) {
	r.captures = append(r.captures, ref)
	return &FrozenReadOnlySession{
		AgentID: ref.AgentID,
		Snapshot: session.SessionSnapshot{
			Key: ref.Session,
		},
		HistoryRevision: "sha256:scripted-gate-snapshot",
		FrozenMedia:     media.FrozenSet{Version: media.FrozenSetVersion},
	}, nil
}

func (r *scriptedGateAgentRunner) RunAgent(
	_ context.Context,
	req AgentRequest,
) (map[string]any, error) {
	r.requests = append(r.requests, req)
	if r.order != nil {
		*r.order = append(*r.order, "agent:"+req.AgentID)
	}
	index := len(r.requests) - 1
	if index >= len(r.responses) {
		return nil, fmt.Errorf("unexpected gate agent call %d", index+1)
	}
	structured := ValidateAgentStructuredOutput(r.responses[index], req.Output)
	if !structured.Valid {
		return nil, fmt.Errorf("scripted gate response is invalid: %s", structured.Error)
	}
	return map[string]any{
		"text":             r.responses[index],
		"structured":       structured.Structured,
		"structured_json":  structured.RawJSON,
		"structured_valid": true,
	}, nil
}

func gateStepIDs(steps []Step) []string {
	ids := make([]string, len(steps))
	for index := range steps {
		ids[index] = steps[index].ID
	}
	return ids
}

func assertCompiledHumanTaskInputReferences(t *testing.T, step Step) {
	t.Helper()
	if !gateValueContainsInputReference(step.With["title"]) ||
		!gateValueContainsInputReference(step.With["questions"]) {
		t.Fatalf("human task does not bind title/questions through inputs: %#v", step.With)
	}
}

func assertCompiledAIAttentionStep(t *testing.T, step Step, decisionID string) {
	t.Helper()
	if step.Uses != "human/task" ||
		step.If != "${{ steps."+decisionID+".outputs.structured.ask_user == true }}" {
		t.Fatalf("AI attention lowering = %#v", step)
	}
	if !gateValueContainsInputReference(step.With["title"]) {
		t.Fatalf("AI attention title is not bound through inputs: %#v", step.With)
	}
	questions, ok := step.With["questions"].(map[string]any)
	if !ok ||
		questions["gate_id"] != strings.TrimSuffix(strings.TrimPrefix(decisionID, "gate_"), "_decision") ||
		questions["reason"] != "${{ steps."+decisionID+".outputs.structured.reason }}" ||
		questions["questions"] != "${{ steps."+decisionID+".outputs.structured.questions }}" {
		t.Fatalf("AI attention questions = %#v", step.With["questions"])
	}
}

func assertGateDecisionContract(t *testing.T, raw any) {
	t.Helper()
	contract, err := ParseAgentOutputContract(raw)
	if err != nil {
		t.Fatalf("ParseAgentOutputContract() error = %v", err)
	}
	if contract == nil || !contract.Enabled() {
		t.Fatalf("decision output contract = %#v", contract)
	}
	valid := ValidateAgentStructuredOutput(
		`{"ask_user":true,"reason":"ambiguous","questions":["Choose one?"]}`,
		contract,
	)
	if !valid.Valid {
		t.Fatalf("required gate decision rejected by contract: %s", valid.Error)
	}
	for _, invalid := range []string{
		`{"reason":"missing bool","questions":[]}`,
		`{"ask_user":"yes","reason":"wrong bool","questions":[]}`,
		`{"ask_user":true,"reason":"wrong questions","questions":"ask"}`,
		`{"ask_user":true,"reason":"extra","questions":[],"action":"merge"}`,
	} {
		if result := ValidateAgentStructuredOutput(invalid, contract); result.Valid {
			t.Fatalf("decision contract accepted %s", invalid)
		}
	}
}

func gateValueContainsInputReference(value any) bool {
	text, ok := value.(string)
	return ok && strings.Contains(text, "${{ private.")
}

func gateTestName(value string) string {
	name := strings.ReplaceAll(strings.TrimSpace(value), "/", "_")
	name = strings.ReplaceAll(name, " ", "_")
	if len(name) > 80 {
		name = name[:80]
	}
	return name
}
