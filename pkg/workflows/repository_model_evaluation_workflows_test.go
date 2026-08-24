package workflows

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sipeed/picoclaw/pkg/reposcope"
)

type repositoryModelEvaluationBatchAgentRunner struct {
	t      *testing.T
	scopes [][]map[string]any
}

func (r *repositoryModelEvaluationBatchAgentRunner) RunAgent(
	_ context.Context,
	req AgentRequest,
) (map[string]any, error) {
	scope, ok := req.Scope.([]map[string]any)
	if !ok || len(scope) != 1 || scope[0]["contentComplete"] != true {
		r.t.Fatalf("evaluation agent scope = %#v", req.Scope)
	}
	r.scopes = append(r.scopes, scope)
	if req.Model == "judge" {
		return map[string]any{
			"structured": map[string]any{
				"evaluations": []map[string]any{{
					"candidateId": "candidate-001", "correctness": 80, "evidence": 80,
					"coverage": 80, "actionability": 80, "overall": 80,
					"verdict": "bounded", "strengths": []string{}, "limitations": []string{},
					"confirmedClaims": 0, "unsupportedClaims": 0,
				}},
				"methodology": "exact source", "warnings": []string{},
			},
		}, nil
	}
	return map[string]any{
		"managed_children": []map[string]any{{
			"model": map[string]any{"requested": "model-a", "selected": "gpt-a"},
			"valid": true, "scope": scope, "usage": []map[string]any{},
			"structured": map[string]any{
				"summary": "checked", "claims": []map[string]any{}, "residualRisks": []string{},
			},
		}},
	}, nil
}

func TestRepositoryModelEvaluationWorkflowContracts(t *testing.T) {
	preflight := parseWorkflow(t, RepositoryModelEvaluationPreflightWorkflowYAML)
	preflightSteps := stepMap(preflight.Jobs["preflight"].Steps)
	for _, id := range []string{"checkout", "inventory", "catalog", "release", "selector", "select"} {
		if _, exists := preflightSteps[id]; !exists {
			t.Fatalf("preflight missing %q: %#v", id, preflightSteps)
		}
	}
	selector := preflightSteps["selector"]
	if selector.With["model"] != "${{ inputs.selector_model }}" ||
		selector.With["tools"] != "none" || selector.With["session"] != "ephemeral" ||
		selector.With["scope_content"] != "metadata" ||
		selector.With["scope"] != "${{ steps.catalog.outputs.candidates }}" {
		t.Fatalf("selector authority = %#v", selector.With)
	}
	selectStep := preflightSteps["select"]
	if selectStep.Uses != "function/evaluation.corpus" || selectStep.With["action"] != "select" {
		t.Fatalf("select = %#v", selectStep)
	}

	batch := parseWorkflow(t, RepositoryModelEvaluationBatchWorkflowYAML)
	batchSteps := stepMap(batch.Jobs["evaluate"].Steps)
	for _, id := range []string{
		"checkout", "validate", "freeze", "release", "candidates", "blind", "judge",
	} {
		if _, exists := batchSteps[id]; !exists {
			t.Fatalf("batch missing %q", id)
		}
	}
	if batchSteps["validate"].With["action"] != "validate" ||
		batchSteps["validate"].With["candidates"] != "${{ inputs.selected_candidates }}" {
		t.Fatalf("batch validation = %#v", batchSteps["validate"])
	}
	if batchSteps["freeze"].With["copies"] != 2 {
		t.Fatalf("batch freeze = %#v", batchSteps["freeze"])
	}
	candidates := batchSteps["candidates"]
	managed, ok := candidates.With["managed"].(map[string]any)
	if !ok || managed["reviewer_models"] != "${{ inputs.candidate_models }}" ||
		managed["include_default_reviewer"] != false || managed["max_parallel_children"] != 3 ||
		managed["max_parallel_per_reviewer"] != 1 {
		t.Fatalf("candidate fairness controls = %#v", candidates.With["managed"])
	}
	if candidates.With["scope_snapshot"] != "${{ steps.freeze.outputs.token }}" ||
		candidates.With["tools"] != "none" || candidates.With["history"] != "none" {
		t.Fatalf("candidate authority = %#v", candidates.With)
	}
	judge := batchSteps["judge"]
	if judge.With["model"] != "${{ inputs.judge_model }}" ||
		judge.With["scope_snapshot"] != "${{ steps.freeze.outputs.secondaryToken }}" ||
		judge.With["tools"] != "none" {
		t.Fatalf("judge authority = %#v", judge.With)
	}
	if batchSteps["blind"].With["managed_children"] != "${{ steps.candidates.outputs.managed_children }}" ||
		batchSteps["blind"].With["candidate_models"] != "${{ inputs.candidate_identity_models }}" {
		t.Fatalf("blind input = %#v", batchSteps["blind"])
	}

	analysis := parseWorkflow(t, RepositoryModelEvaluationAnalysisWorkflowYAML)
	analyze := stepMap(analysis.Jobs["analyze"].Steps)["analyze"]
	if analyze.With["model"] != "${{ inputs.judge_model }}" || analyze.With["tools"] != "none" ||
		analyze.With["scope_content"] != "metadata" {
		t.Fatalf("analysis authority = %#v", analyze.With)
	}
}

func TestRepositoryModelEvaluationBatchConsumesBothFrozenScopeCopies(t *testing.T) {
	fixture := newRepositoryEvaluationFixture(t)
	catalog, err := nativeRepositoryEvaluationCatalog(
		t.Context(), fixture.catalogArgs(), fixture.exec(),
	)
	if err != nil {
		t.Fatal(err)
	}
	candidates := repositoryEvaluationCandidates(t, catalog["candidates"])
	selected := []reposcope.Candidate{
		repositoryEvaluationCandidateByPath(t, candidates, "pkg/service.go"),
	}
	selectedJSON, err := json.Marshal(selected)
	if err != nil {
		t.Fatal(err)
	}
	scopeJSON, err := json.Marshal(map[string]any{"codeTypes": []string{"code"}})
	if err != nil {
		t.Fatal(err)
	}
	tools := &codeReviewTemplateToolRunner{repo: fixture.repo}
	agents := &repositoryModelEvaluationBatchAgentRunner{t: t}
	executor := &Executor{WorkspaceDir: fixture.workspace, Tools: tools, Agents: agents}
	result, err := executor.Run(t.Context(), RunRequest{
		RunID: "wr-evaluation-batch-copies", Workflow: parseWorkflow(t, RepositoryModelEvaluationBatchWorkflowYAML),
		WorkflowRef: RepositoryModelEvaluationBatchWorkflowRef,
		Inputs: map[string]any{
			"repository": "owner/repo", "commit": fixture.commit,
			"inventory_hash": fixture.inventoryHash, "scope": string(scopeJSON),
			"selected_candidates": string(selectedJSON), "candidate_models": "model-a",
			"candidate_identity_models": "model-a",
			"judge_model":               "judge", "evaluation_focus": "exact source",
		},
	})
	if err != nil || result.Status != RunStatusSucceeded {
		t.Fatalf("evaluation batch run = %#v, %v", result, err)
	}
	if !reflect.DeepEqual(tools.actions, []string{"acquire", "release"}) || len(agents.scopes) != 2 ||
		agents.scopes[0][0]["content"] != agents.scopes[1][0]["content"] {
		t.Fatalf("evaluation batch actions=%v scopes=%#v", tools.actions, agents.scopes)
	}
	nativeFrozenGitScopes.Lock()
	remaining := 0
	for _, entry := range nativeFrozenGitScopes.entries {
		if entry.runID == "wr-evaluation-batch-copies" {
			remaining++
		}
	}
	nativeFrozenGitScopes.Unlock()
	if remaining != 0 {
		t.Fatalf("evaluation batch retained %d consumed frozen scopes", remaining)
	}
}

func TestRepositoryModelEvaluationAgentStepsUseIsolatedSystemPolicy(t *testing.T) {
	tests := []struct {
		workflowRef string
		allowed     []string
	}{
		{RepositoryModelEvaluationPreflightWorkflowRef, []string{"selector"}},
		{RepositoryModelEvaluationBatchWorkflowRef, []string{"candidates", "judge"}},
		{RepositoryModelEvaluationAnalysisWorkflowRef, []string{"analyze"}},
	}
	for _, test := range tests {
		for _, step := range test.allowed {
			if !repositoryModelEvaluationAgentStep(test.workflowRef, step) {
				t.Fatalf("%s/%s was not recognized", test.workflowRef, step)
			}
		}
		if repositoryModelEvaluationAgentStep(test.workflowRef, "untrusted-step") {
			t.Fatalf("%s accepted untrusted step", test.workflowRef)
		}
	}
	if repositoryModelEvaluationAgentStep("workflows/other.yml", "selector") {
		t.Fatal("untrusted workflow received evaluation authority")
	}
}

func TestRepositoryModelEvaluationTemplatesAreInBuiltInCatalog(t *testing.T) {
	want := map[string]string{
		RepositoryModelEvaluationPreflightWorkflowName: RepositoryModelEvaluationPreflightWorkflowRef,
		RepositoryModelEvaluationBatchWorkflowName:     RepositoryModelEvaluationBatchWorkflowRef,
		RepositoryModelEvaluationAnalysisWorkflowName:  RepositoryModelEvaluationAnalysisWorkflowRef,
	}
	got := make(map[string]string)
	for _, template := range builtInWorkflowTemplateRegistry {
		if ref, wanted := want[template.name]; wanted {
			got[template.name] = template.ref
			if template.ref != ref {
				t.Fatalf("template %q ref = %q, want %q", template.name, template.ref, ref)
			}
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("evaluation templates = %#v, want %#v", got, want)
	}
}

func stepMap(steps []Step) map[string]Step {
	result := make(map[string]Step, len(steps))
	for _, step := range steps {
		result[step.ID] = step
	}
	return result
}
