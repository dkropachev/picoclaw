package workflows

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestCompileGateWorkflowV2LowersStagedAllOf(t *testing.T) {
	spec := GateWorkflowSpec{
		ID: "charter", Name: "Charter gate", Purpose: GatePurposeAuthorization,
		DecisionPoint: "pr.charter.confirm",
		Stages: []GateStageSpec{
			{ID: "explicit_pass", Kind: GateZero},
			{ID: "identity", Kind: GateDeterministic, Title: "Verified identity", When: "inputs.gate_subject.verified == true"},
			{ID: "quality", Kind: GateAIIsolatedContext, Title: "Charter quality", AgentID: "reviewer", Criteria: "The charter is precise.", Questions: []any{"List ambiguity."}},
			{ID: "confirm", Kind: GateHuman, Title: "Confirm charter", Questions: []any{map[string]any{"id": "decision", "prompt": "Confirm?"}}},
			{ID: "discussion", Kind: GateAIWorkingContext, Title: "Discussion check", AgentID: "main", Criteria: "The discussion has no unresolved choice."},
		},
	}
	compilation, err := CompileGateWorkflowV2(spec, map[string]any{"verified": true})
	if err != nil {
		t.Fatalf("CompileGateWorkflowV2() error = %v", err)
	}
	if compilation.Workflow == nil || compilation.PrivateRoot == nil || compilation.ImmediateOutcome != "" {
		t.Fatalf("compilation = %#v, want runnable non-immediate result", compilation)
	}
	if !compilation.RequiresSession || compilation.RequiredSessionAgentID != "main" {
		t.Fatalf("session requirement = %v/%q, want main", compilation.RequiresSession, compilation.RequiredSessionAgentID)
	}
	if !reflect.DeepEqual(compilation.StageIDs, []string{"explicit_pass", "identity", "quality", "confirm", "discussion"}) {
		t.Fatalf("StageIDs = %#v", compilation.StageIDs)
	}
	job := compilation.Workflow.Jobs[workflowGateJobID]
	if len(job.Steps) != 3 {
		t.Fatalf("steps = %#v, want only AI/human stages", job.Steps)
	}
	quality, confirm, discussion := job.Steps[0], job.Steps[1], job.Steps[2]
	if quality.ID != "gate_v2_quality" || quality.Uses != "agent/reviewer" ||
		quality.If != "${{ private.gate_subject.verified == true }}" ||
		quality.With["session"] != AgentSessionEphemeral || quality.With["history"] != "none" ||
		quality.With["cache"] != "none" || quality.With["tools"] != AgentToolsNone {
		t.Fatalf("quality step = %#v", quality)
	}
	assertGateV2AIContract(t, quality.With["output"])
	wantConfirmIf := "${{ private.gate_subject.verified == true and steps.gate_v2_quality.outputs.structured.outcome == 'pass' }}"
	if confirm.ID != "gate_v2_confirm" || confirm.Uses != "human/task" || confirm.If != wantConfirmIf {
		t.Fatalf("confirm step = %#v, want if %q", confirm, wantConfirmIf)
	}
	assertGateV2HumanSchema(t, confirm.With["response_schema"])
	wantDiscussionIf := wantConfirmIf[:len(wantConfirmIf)-3] + " and steps.gate_v2_confirm.outputs.response.decision == 'pass' }}"
	if discussion.ID != "gate_v2_discussion" || discussion.Uses != "agent/main" ||
		discussion.If != wantDiscussionIf || discussion.With["session"] != "inherit" ||
		discussion.With["history"] != "read_only" || discussion.With["cache"] != "session" {
		t.Fatalf("discussion step = %#v, want if %q", discussion, wantDiscussionIf)
	}
	wantFinal := wantDiscussionIf[:len(wantDiscussionIf)-3] + " and steps.gate_v2_discussion.outputs.structured.outcome == 'pass' }}"
	if job.Outputs[workflowGateV2PassedJobOutput] != wantFinal {
		t.Fatalf("final pass output = %q, want %q", job.Outputs[workflowGateV2PassedJobOutput], wantFinal)
	}
	if err := Validate(compilation.Workflow); err != nil {
		t.Fatalf("Validate(compiled) error = %v", err)
	}
	if len(compilation.CanonicalSpec) == 0 || !strings.HasPrefix(compilation.SpecDigest, "sha256:") {
		t.Fatalf("canonical spec/digest = %q/%q", compilation.CanonicalSpec, compilation.SpecDigest)
	}
}

func TestGateWorkflowV2HumanNonPassStopsLaterStagesWithoutReplay(t *testing.T) {
	ctx := context.Background()
	store := NewFileRunStore(t.TempDir())
	agents := &scriptedGateAgentRunner{responses: []string{
		`{"outcome":"pass","reason":"ready","questions":[]}`,
		`{"outcome":"pass","reason":"must not run","questions":[]}`,
	}}
	compilation, err := CompileGateWorkflowV2(GateWorkflowSpec{
		ID: "publish", Name: "Publish gate", Purpose: GatePurposeAuthorization,
		DecisionPoint: "pr.implementation.publish",
		Stages: []GateStageSpec{
			{ID: "check", Kind: GateAIIsolatedContext, Title: "Check", AgentID: "reviewer", Criteria: "Evidence is green."},
			{ID: "approve", Kind: GateHuman, Title: "Approve", Questions: []any{"Choose disposition."}},
			{ID: "after", Kind: GateAIIsolatedContext, Title: "After", AgentID: "reviewer", Criteria: "This must be skipped after non-pass."},
		},
	}, map[string]any{"head": "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := captureInitialPrivateWorkflow(compilation.Workflow); err != nil {
		t.Fatalf("captureInitialPrivateWorkflow() error = %v", err)
	}
	if err := validatePrivateWorkflowAdmission(compilation.Workflow, RunRequest{
		Workflow: compilation.Workflow, PrivateRoot: compilation.PrivateRoot,
	}); err != nil {
		t.Fatalf("validatePrivateWorkflowAdmission() error = %v", err)
	}
	frozen, err := freezeWorkflowPrivateRoot(ctx, agents, compilation.PrivateRoot)
	if err != nil {
		t.Fatalf("freezeWorkflowPrivateRoot() error = %v", err)
	}
	if err := validatePrivateRootForWorkflow(compilation.Workflow, frozen); err != nil {
		t.Fatalf("validatePrivateRootForWorkflow() error = %v", err)
	}
	executor := &Executor{WorkspaceDir: t.TempDir(), Store: store, Agents: agents}
	started, err := executor.Run(ctx, RunRequest{
		Workflow: compilation.Workflow, WorkflowRef: "inline/gates-v2-publish",
		PrivateRoot: compilation.PrivateRoot,
	})
	if err != nil || started.Status != RunStatusWaiting || len(agents.requests) != 1 {
		t.Fatalf("Run() result=%#v err=%v requests=%d, want one AI then wait", started, err, len(agents.requests))
	}
	tasks, err := executor.ListHumanTasks(ctx, started.RunID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("ListHumanTasks() tasks=%#v err=%v", tasks, err)
	}
	resumed, err := executor.ResumeHumanTask(ctx, started.RunID, tasks[0].ID, HumanTaskResumeRequest{
		ExpectedRevision: tasks[0].Revision,
		InputHash:        tasks[0].InputHash,
		ResponseID:       "decision-1",
		Response: map[string]any{
			"decision": "revise",
			"comment":  "Update charter first.",
		},
	})
	if err != nil || resumed.Status != RunStatusSucceeded {
		t.Fatalf("ResumeHumanTask() result=%#v err=%v", resumed, err)
	}
	if len(agents.requests) != 1 {
		t.Fatalf("agent calls = %d, want no replay and no later stage", len(agents.requests))
	}
	persisted, err := store.GetRun(ctx, started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if got := persisted.Steps[workflowGateJobID+"/gate_v2_after"].Status; got != RunStatusSkipped {
		t.Fatalf("later stage status = %q, want skipped", got)
	}
	if got := persisted.Jobs[workflowGateJobID].Outputs[workflowGateV2PassedJobOutput]; got != false {
		t.Fatalf("gate_passed = %#v, want false", got)
	}
	outcome, stageID, err := ResolveGateWorkflowV2Outcome(compilation, persisted)
	if err != nil || outcome != GateOutcomeRevise || stageID != "approve" {
		t.Fatalf("resolved outcome=%q stage=%q err=%v, want revise/approve", outcome, stageID, err)
	}
}

func TestGateWorkflowV2AINonPassStopsHumanStage(t *testing.T) {
	ctx := context.Background()
	store := NewFileRunStore(t.TempDir())
	agents := &scriptedGateAgentRunner{responses: []string{
		`{"outcome":"defer","reason":"outside charter","questions":[]}`,
	}}
	compilation, err := CompileGateWorkflowV2(GateWorkflowSpec{
		ID: "scope", Name: "Scope gate", Purpose: GatePurposeClassification,
		DecisionPoint: "pr.finding.classify",
		Stages: []GateStageSpec{
			{ID: "classify", Kind: GateAIIsolatedContext, Title: "Classify", AgentID: "reviewer", Criteria: "Classify scope."},
			{ID: "approve", Kind: GateHuman, Title: "Approve", Questions: []any{"Approve classification?"}},
		},
	}, map[string]any{"distance": "S2"})
	if err != nil {
		t.Fatal(err)
	}
	executor := &Executor{WorkspaceDir: t.TempDir(), Store: store, Agents: agents}
	result, err := executor.Run(ctx, RunRequest{
		Workflow: compilation.Workflow, WorkflowRef: "inline/gates-v2-scope",
		PrivateRoot: compilation.PrivateRoot,
	})
	if err != nil || result.Status != RunStatusSucceeded {
		t.Fatalf("Run() result=%#v err=%v", result, err)
	}
	tasks, err := executor.ListHumanTasks(ctx, result.RunID)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("human tasks=%#v err=%v, want none", tasks, err)
	}
	persisted, err := store.GetRun(ctx, result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if got := persisted.Steps[workflowGateJobID+"/gate_v2_approve"].Status; got != RunStatusSkipped {
		t.Fatalf("human stage status = %q, want skipped", got)
	}
	if got := persisted.Jobs[workflowGateJobID].Outputs[workflowGateV2PassedJobOutput]; got != false {
		t.Fatalf("gate_passed = %#v, want false", got)
	}
	outcome, stageID, err := ResolveGateWorkflowV2Outcome(compilation, persisted)
	if err != nil || outcome != GateOutcomeDefer || stageID != "classify" {
		t.Fatalf("resolved outcome=%q stage=%q err=%v, want defer/classify", outcome, stageID, err)
	}
}

func TestResolveGateWorkflowV2AllStagesPass(t *testing.T) {
	ctx := context.Background()
	store := NewFileRunStore(t.TempDir())
	agents := &scriptedGateAgentRunner{responses: []string{
		`{"outcome":"pass","reason":"green","questions":[]}`,
	}}
	compilation, err := CompileGateWorkflowV2(GateWorkflowSpec{
		ID: "complete", Name: "Complete gate", Purpose: GatePurposeAuthorization,
		DecisionPoint: "pr.implementation.complete",
		Stages: []GateStageSpec{
			{ID: "audit", Kind: GateAIIsolatedContext, Title: "Audit", AgentID: "reviewer", Criteria: "Everything is complete."},
			{ID: "green", Kind: GateDeterministic, Title: "Green", When: "inputs.gate_subject.green == true"},
		},
	}, map[string]any{"green": true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := captureInitialPrivateWorkflow(compilation.Workflow); err != nil {
		t.Fatalf("captureInitialPrivateWorkflow() error = %v", err)
	}
	if err := validatePrivateWorkflowAdmission(compilation.Workflow, RunRequest{
		Workflow: compilation.Workflow, PrivateRoot: compilation.PrivateRoot,
	}); err != nil {
		t.Fatalf("validatePrivateWorkflowAdmission() error = %v", err)
	}
	frozen, err := freezeWorkflowPrivateRoot(ctx, agents, compilation.PrivateRoot)
	if err != nil {
		t.Fatalf("freezeWorkflowPrivateRoot() error = %v", err)
	}
	if err := validatePrivateRootForWorkflow(compilation.Workflow, frozen); err != nil {
		t.Fatalf("validatePrivateRootForWorkflow() error = %v", err)
	}
	executor := &Executor{WorkspaceDir: t.TempDir(), Store: store, Agents: agents}
	result, err := executor.Run(ctx, RunRequest{
		Workflow: compilation.Workflow, WorkflowRef: "inline/gates-v2-complete",
		PrivateRoot: compilation.PrivateRoot,
	})
	if err != nil || result.Status != RunStatusSucceeded {
		t.Fatalf("Run() result=%#v err=%v", result, err)
	}
	persisted, err := store.GetRun(ctx, result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	outcome, stageID, err := ResolveGateWorkflowV2Outcome(compilation, persisted)
	if err != nil || outcome != GateOutcomePass || stageID != "" {
		t.Fatalf("resolved outcome=%q stage=%q err=%v, want pass", outcome, stageID, err)
	}
}

func TestGateWorkflowV2ExplicitZeroAndDeterministicImmediateOutcomes(t *testing.T) {
	cycle := map[string]any{}
	cycle["self"] = cycle
	zero, err := CompileGateWorkflowV2(GateWorkflowSpec{
		ID: "automatic", Name: "Automatic", Purpose: GatePurposeAuthorization,
		DecisionPoint: "pr.deferred.publish",
		Stages:        []GateStageSpec{{ID: "allow", Kind: GateZero}},
	}, cycle)
	if err != nil || zero.Workflow != nil || zero.PrivateRoot != nil || zero.ImmediateOutcome != GateOutcomePass {
		t.Fatalf("zero compilation=%#v err=%v, want immediate pass without subject normalization", zero, err)
	}

	for _, test := range []struct {
		name    string
		subject map[string]any
		want    GateOutcome
	}{
		{name: "pass", subject: map[string]any{"owned": true, "writable": true}, want: GateOutcomePass},
		{name: "block", subject: map[string]any{"owned": true, "writable": false}, want: GateOutcomeBlock},
	} {
		t.Run(test.name, func(t *testing.T) {
			compiled, err := CompileGateWorkflowV2(GateWorkflowSpec{
				ID: "eligibility", Name: "Eligibility", Purpose: GatePurposeAuthorization,
				DecisionPoint: "pr.implementation.eligibility",
				Stages: []GateStageSpec{{
					ID: "provider", Kind: GateDeterministic, Title: "Provider authority",
					When: "inputs.gate_subject.owned == true and inputs.gate_subject.writable == true",
				}},
			}, test.subject)
			if err != nil || compiled.Workflow != nil || compiled.ImmediateOutcome != test.want {
				t.Fatalf("compilation=%#v err=%v, want %q", compiled, err, test.want)
			}
			outcome, _, resolveErr := ResolveGateWorkflowV2Outcome(compiled, nil)
			if resolveErr != nil || outcome != test.want {
				t.Fatalf("resolved outcome=%q error=%v, want %q", outcome, resolveErr, test.want)
			}
		})
	}
}

func TestValidateGateWorkflowSpecV2RejectsMalformedStages(t *testing.T) {
	base := GateWorkflowSpec{
		ID: "gate", Name: "Gate", Purpose: GatePurposeAuthorization,
		DecisionPoint: "pr.charter.confirm",
		Stages:        []GateStageSpec{{ID: "allow", Kind: GateZero}},
	}
	tests := []struct {
		name   string
		mutate func(*GateWorkflowSpec)
		want   string
	}{
		{name: "empty stages", mutate: func(spec *GateWorkflowSpec) { spec.Stages = nil }, want: "between 1"},
		{name: "bad purpose", mutate: func(spec *GateWorkflowSpec) { spec.Purpose = "unknown" }, want: "purpose"},
		{name: "duplicate", mutate: func(spec *GateWorkflowSpec) { spec.Stages = append(spec.Stages, spec.Stages[0]) }, want: "duplicated"},
		{name: "human missing questions", mutate: func(spec *GateWorkflowSpec) {
			spec.Stages[0] = GateStageSpec{ID: "human", Kind: GateHuman, Title: "Human"}
		}, want: "questions"},
		{name: "AI missing criteria", mutate: func(spec *GateWorkflowSpec) {
			spec.Stages[0] = GateStageSpec{ID: "ai", Kind: GateAIIsolatedContext, Title: "AI", AgentID: "main"}
		}, want: "criteria"},
		{name: "deterministic unknown root", mutate: func(spec *GateWorkflowSpec) {
			spec.Stages[0] = GateStageSpec{ID: "det", Kind: GateDeterministic, Title: "Det", When: "event.ready == true"}
		}, want: "root"},
		{name: "zero behavior", mutate: func(spec *GateWorkflowSpec) { spec.Stages[0].Title = "Not zero" }, want: "zero stage"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := base
			spec.Stages = append([]GateStageSpec(nil), base.Stages...)
			test.mutate(&spec)
			err := ValidateGateWorkflowSpecV2(spec)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateGateWorkflowSpecV2() error=%v, want containing %q", err, test.want)
			}
		})
	}
}

func TestCompileGateWorkflowV2RejectsMissingDeterministicSubjectPath(t *testing.T) {
	_, err := CompileGateWorkflowV2(GateWorkflowSpec{
		ID: "identity", Name: "Identity", Purpose: GatePurposeAuthorization,
		DecisionPoint: "pr.review.start",
		Stages: []GateStageSpec{{
			ID: "provider", Kind: GateDeterministic, Title: "Provider",
			When: "inputs.gate_subject.provider_verified == true",
		}},
	}, map[string]any{"different": true})
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("CompileGateWorkflowV2() error = %v, want missing subject path", err)
	}
}

func TestExpressionLogicalANDShortCircuitsAndPreservesQuotedLiteral(t *testing.T) {
	ctx := expressionContext{Inputs: map[string]any{"ready": false, "text": "one and two"}}
	value, err := evalExpression("inputs.ready == true and steps.missing.outputs.value == true", ctx)
	if err != nil || value != false {
		t.Fatalf("short-circuit value=%#v err=%v, want false", value, err)
	}
	value, err = evalExpression("inputs.text == 'one and two' and true", ctx)
	if err != nil || value != true {
		t.Fatalf("quoted AND value=%#v err=%v, want true", value, err)
	}
	if err := validateExpressionSyntax("inputs.text == 'one and two' and true"); err != nil {
		t.Fatalf("validate quoted AND error = %v", err)
	}
}

func assertGateV2AIContract(t *testing.T, raw any) {
	t.Helper()
	contract, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("AI contract = %#v", raw)
	}
	schema, ok := contract["schema"].(map[string]any)
	if !ok || schema["additionalProperties"] != false {
		t.Fatalf("AI schema = %#v", contract["schema"])
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("AI properties = %#v", schema["properties"])
	}
	outcome, ok := properties["outcome"].(map[string]any)
	if !ok || !reflect.DeepEqual(outcome["enum"], gateWorkflowV2OutcomeEnum()) {
		t.Fatalf("AI outcome schema = %#v", outcome)
	}
}

func assertGateV2HumanSchema(t *testing.T, raw any) {
	t.Helper()
	schema, ok := raw.(map[string]any)
	if !ok || schema["type"] != "object" || schema["additionalProperties"] != false {
		t.Fatalf("human schema = %#v", raw)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("human properties = %#v", schema["properties"])
	}
	decision, ok := properties["decision"].(map[string]any)
	if !ok || !reflect.DeepEqual(decision["enum"], gateWorkflowV2OutcomeEnum()) {
		t.Fatalf("human decision schema = %#v", decision)
	}
	if _, err := json.Marshal(schema); err != nil {
		t.Fatalf("human schema JSON error = %v", err)
	}
}
