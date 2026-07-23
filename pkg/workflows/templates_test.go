package workflows

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestInstallCodeReviewWorkflowWritesValidLocalDefinition(t *testing.T) {
	workspace := t.TempDir()
	result, err := InstallCodeReviewWorkflow(context.Background(), workspace, false)
	if err != nil {
		t.Fatalf("InstallCodeReviewWorkflow() error = %v", err)
	}
	if !result.Installed || result.Ref != CodeReviewWorkflowRef {
		t.Fatalf("install result = %#v, want installed code-review ref", result)
	}
	if _, statErr := os.Stat(result.Path); statErr != nil {
		t.Fatalf("installed workflow stat error = %v", statErr)
	}
	workflow, err := LoadLocal(context.Background(), workspace, CodeReviewWorkflowRef)
	if err != nil {
		t.Fatalf("LoadLocal() error = %v", err)
	}
	if validateErr := Validate(workflow); validateErr != nil {
		t.Fatalf("Validate(installed workflow) error = %v", validateErr)
	}

	second, err := InstallCodeReviewWorkflow(context.Background(), workspace, false)
	if err != nil {
		t.Fatalf("second InstallCodeReviewWorkflow() error = %v", err)
	}
	if second.Installed {
		t.Fatalf("second install result = %#v, want idempotent no-op", second)
	}
}

func TestCodeReviewWorkflowRunsWithGitWorkspaceTool(t *testing.T) {
	requireGit(t)
	workspace := t.TempDir()
	repo := filepath.Join(workspace, "review-checkout")
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(repo, "src", "app.go"), "package app\n\nfunc Answer() int { return 42 }\n")
	gitCmd(t, repo, "init")
	gitCmd(t, repo, "config", "user.email", "test@example.com")
	gitCmd(t, repo, "config", "user.name", "Test User")
	gitCmd(t, repo, "add", ".")
	gitCmd(t, repo, "commit", "-m", "initial")

	workflow := parseWorkflow(t, CodeReviewWorkflowYAML)
	toolRunner := &codeReviewTemplateToolRunner{repo: repo}
	agentRunner := &codeReviewTemplateAgentRunner{t: t, repo: repo, toolRunner: toolRunner}
	result, err := (&Executor{
		WorkspaceDir: workspace,
		Tools:        toolRunner,
		Agents:       agentRunner,
	}).Run(context.Background(), RunRequest{
		Workflow:    workflow,
		WorkflowRef: CodeReviewWorkflowRef,
		Inputs: map[string]any{
			"action":       "review",
			"repository":   repo,
			"ref":          "HEAD",
			"base_ref":     "main",
			"review_focus": "Check correctness.",
		},
		Session: "workflow:test",
	})
	if err != nil {
		t.Fatalf("Run(code-review workflow) error = %v", err)
	}
	if result.Status != RunStatusSucceeded {
		t.Fatalf("status = %q, want succeeded", result.Status)
	}
	if !reflect.DeepEqual(toolRunner.actions, []string{"acquire", "release"}) {
		t.Fatalf("git workspace actions = %v, want acquire then release", toolRunner.actions)
	}
	if !agentRunner.called {
		t.Fatal("agent runner was not called")
	}
	if got := result.Outputs["summary"]; got != "No findings in selected files." {
		t.Fatalf("summary = %#v", got)
	}
	if got := result.Outputs["workspacePath"]; got != repo {
		t.Fatalf("workspace_path = %#v, want %q", got, repo)
	}
	if got, ok := result.Outputs["inventoryHash"].(string); !ok || got == "" {
		t.Fatalf("inventoryHash = %#v, want non-empty string", result.Outputs["inventoryHash"])
	}
}

type codeReviewTemplateToolRunner struct {
	repo    string
	actions []string
}

func (r *codeReviewTemplateToolRunner) RunTool(
	_ context.Context,
	req ToolRequest,
) (map[string]any, error) {
	if req.Name != "git_workspace" {
		return nil, &testWorkflowError{message: "unexpected tool " + req.Name}
	}
	action, _ := req.Args["action"].(string)
	r.actions = append(r.actions, action)
	switch action {
	case "acquire":
		return map[string]any{
			"workspace": map[string]any{
				"id":     "gw-review",
				"path":   r.repo,
				"status": "locked",
			},
		}, nil
	case "release":
		return map[string]any{
			"released": []any{
				map[string]any{
					"id":     "gw-review",
					"path":   r.repo,
					"status": "available",
				},
			},
		}, nil
	default:
		return nil, &testWorkflowError{message: "unexpected git_workspace action " + action}
	}
}

type codeReviewTemplateAgentRunner struct {
	t          *testing.T
	repo       string
	toolRunner *codeReviewTemplateToolRunner
	called     bool
}

func (r *codeReviewTemplateAgentRunner) RunAgent(_ context.Context, req AgentRequest) (map[string]any, error) {
	r.called = true
	if !reflect.DeepEqual(r.toolRunner.actions, []string{"acquire", "release"}) {
		r.t.Fatalf("agent called after actions %v, want workspace released first", r.toolRunner.actions)
	}
	if req.AgentID != "main" || req.History != "none" || req.Cache != "session" {
		r.t.Fatalf("agent request = %#v, want main/history none/cache session", req)
	}
	scope, ok := req.Scope.([]map[string]any)
	if !ok {
		r.t.Fatalf("scope = %#v, want selected file list", req.Scope)
	}
	if len(scope) != 1 {
		r.t.Fatalf("scope length = %d, want one selected file", len(scope))
	}
	if got := scope[0]["path"]; got != "src/app.go" {
		r.t.Fatalf("scope[0].path = %#v, want src/app.go", got)
	}
	if content, ok := scope[0]["content"].(string); !ok || content == "" {
		r.t.Fatalf("scope[0].content = %#v, want file content", scope[0]["content"])
	}
	if req.Output == nil || !req.Output.Enabled() {
		r.t.Fatal("agent output contract is not enabled")
	}
	structured := map[string]any{
		"summary":       "No findings in selected files.",
		"findings":      []any{},
		"tests":         []any{"go test ./..."},
		"residualRisks": []any{},
	}
	raw, err := json.Marshal(structured)
	if err != nil {
		r.t.Fatal(err)
	}
	return map[string]any{
		"text":             string(raw),
		"structured":       structured,
		"structured_json":  string(raw),
		"structured_valid": true,
	}, nil
}

type testWorkflowError struct {
	message string
}

func (e *testWorkflowError) Error() string {
	return e.message
}
