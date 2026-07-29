package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const workflowDependencyTestChild = `name: Shared child
on:
  workflow_call:
    inputs:
      value:
        type: string
        required: true
jobs:
  work:
    runs-on: picoclaw
    steps:
      - uses: function/workflow.state
        with:
          action: list
`

const workflowDependencyTestRoot = `name: Root
on:
  manual: {}
jobs:
  shared:
    uses: workflows/child.yml
    with:
      value: ready
  work:
    runs-on: picoclaw
    steps:
      - uses: agent/main
      - uses: tool/read_file
      - uses: mcp/github/comment
`

type workflowDependencyRuntimeStub struct {
	resolve func(workflows.WorkflowDependencyOccurrence) workflows.WorkflowDependencyReadinessCode
}

func (s *workflowDependencyRuntimeStub) ResolveWorkflowDependency(
	_ context.Context,
	dependency workflows.WorkflowDependencyOccurrence,
) workflows.WorkflowDependencyReadinessCode {
	if s != nil && s.resolve != nil {
		return s.resolve(dependency)
	}
	return workflows.WorkflowDependencyReadinessReady
}

func (*workflowDependencyRuntimeStub) Close() error {
	return nil
}

func TestWorkflowDependencyCheckDraftReportsStructuralAndRuntimeReadiness(t *testing.T) {
	workspace := t.TempDir()
	configPath := writeWorkflowDependencyTestConfig(t, workspace, true)
	writeWorkflowDependencyDefinition(
		t,
		workspace,
		"automation",
		"workflows/child.yml",
		workflowDependencyTestChild,
	)
	restore := stubWorkflowDependencyRuntime(t, func(
		dependency workflows.WorkflowDependencyOccurrence,
	) workflows.WorkflowDependencyReadinessCode {
		if dependency.Kind == workflows.WorkflowDependencyKindMCP {
			return workflows.WorkflowDependencyReadinessNotConnected
		}
		return workflows.WorkflowDependencyReadinessReady
	})
	defer restore()

	body, err := json.Marshal(workflowDependencyCheckRequest{
		Draft: &workflowDependencyDraftRequest{
			TargetRef: "workflows/root.yml",
			YAML:      workflowDependencyString(workflowDependencyTestRoot),
		},
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	recorder := httptest.NewRecorder()
	NewHandler(configPath).handleCheckWorkflowDependencies(
		recorder,
		httptest.NewRequest(
			http.MethodPost,
			"/api/workflows/dependencies/check",
			strings.NewReader(string(body)),
		),
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", recorder.Header().Get("Cache-Control"))
	}
	if strings.Contains(recorder.Body.String(), workspace) ||
		strings.Contains(recorder.Body.String(), workflowDependencyTestRoot) {
		t.Fatalf("response leaked path or YAML: %s", recorder.Body.String())
	}
	var response workflowDependencyCheckResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("response JSON error = %v", err)
	}
	if response.RootRef != "workflows/root.yml" ||
		response.Revision == "" ||
		!response.WorkflowEnabled ||
		!response.StructuralReady ||
		response.RuntimeReady ||
		response.Ready {
		t.Fatalf("response = %#v", response)
	}
	if len(response.Dependencies) != 5 {
		t.Fatalf("dependencies = %#v", response.Dependencies)
	}
	var sawMCP bool
	for _, dependency := range response.Dependencies {
		if dependency.Dependency.Kind == workflows.WorkflowDependencyKindMCP {
			sawMCP = true
			if dependency.Code != workflows.WorkflowDependencyReadinessNotConnected ||
				dependency.Ready {
				t.Fatalf("MCP readiness = %#v", dependency)
			}
		}
	}
	if !sawMCP {
		t.Fatal("MCP dependency missing")
	}
}

func TestWorkflowDependencyRevisionFencesChildAndConfigChanges(t *testing.T) {
	workspace := t.TempDir()
	configPath := writeWorkflowDependencyTestConfig(t, workspace, true)
	childPath := writeWorkflowDependencyDefinition(
		t,
		workspace,
		"automation",
		"workflows/child.yml",
		workflowDependencyTestChild,
	)
	restore := stubWorkflowDependencyRuntime(t, nil)
	defer restore()
	handler := NewHandler(configPath)

	first := checkWorkflowDependencyDraft(
		t,
		handler,
		"workflows/root.yml",
		workflowDependencyTestRoot,
	)
	if !first.Ready {
		t.Fatalf("first response = %#v", first)
	}
	if err := os.WriteFile(
		childPath,
		[]byte(strings.Replace(
			workflowDependencyTestChild,
			"name: Shared child",
			"name: Revised shared child",
			1,
		)),
		0o644,
	); err != nil {
		t.Fatalf("WriteFile(child) error = %v", err)
	}
	second := checkWorkflowDependencyDraft(
		t,
		handler,
		"workflows/root.yml",
		workflowDependencyTestRoot,
	)
	if second.Revision == first.Revision {
		t.Fatalf("child change did not change revision %q", first.Revision)
	}

	cfg, err := config.LoadConfigForUpdate(configPath)
	if err != nil {
		t.Fatalf("LoadConfigForUpdate() error = %v", err)
	}
	cfg.Workflows.RetentionDays++
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	third := checkWorkflowDependencyDraft(
		t,
		handler,
		"workflows/root.yml",
		workflowDependencyTestRoot,
	)
	if third.Revision == second.Revision {
		t.Fatalf("config change did not change revision %q", second.Revision)
	}
}

func TestWorkflowDependencyCheckFailsClosedWhenConfigChangesDuringRuntimeInit(
	t *testing.T,
) {
	workspace := t.TempDir()
	configPath := writeWorkflowDependencyTestConfig(t, workspace, true)
	writeWorkflowDependencyDefinition(
		t,
		workspace,
		"automation",
		"workflows/child.yml",
		workflowDependencyTestChild,
	)
	previous := newWorkflowDependencyRuntime
	defer func() { newWorkflowDependencyRuntime = previous }()
	newWorkflowDependencyRuntime = func(
		path string,
		snapshot *config.Config,
	) workflowDependencyRuntime {
		if snapshot == nil || !snapshot.Workflows.Enabled {
			t.Fatalf("runtime snapshot = %#v, want enabled config A", snapshot)
		}
		changed, err := config.LoadConfigForUpdate(path)
		if err != nil {
			t.Fatalf("LoadConfigForUpdate() error = %v", err)
		}
		changed.Workflows.RetentionDays++
		if err := config.SaveConfig(path, changed); err != nil {
			t.Fatalf("SaveConfig(changed) error = %v", err)
		}
		return &workflowDependencyRuntimeStub{}
	}
	body, err := json.Marshal(workflowDependencyCheckRequest{
		Draft: &workflowDependencyDraftRequest{
			TargetRef: "workflows/root.yml",
			YAML:      workflowDependencyString(workflowDependencyTestRoot),
		},
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	recorder := httptest.NewRecorder()
	NewHandler(configPath).handleCheckWorkflowDependencies(
		recorder,
		httptest.NewRequest(
			http.MethodPost,
			"/api/workflows/dependencies/check",
			strings.NewReader(string(body)),
		),
	)
	if recorder.Code != http.StatusServiceUnavailable ||
		!strings.Contains(recorder.Body.String(), "dependency_check_unavailable") {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), workspace) {
		t.Fatalf("response leaked workspace path: %s", recorder.Body.String())
	}
}

func TestWorkflowDependencyCheckPublishedAndDisabledState(t *testing.T) {
	workspace := t.TempDir()
	configPath := writeWorkflowDependencyTestConfig(t, workspace, false)
	writeWorkflowDependencyDefinition(
		t,
		workspace,
		"automation",
		"workflows/child.yml",
		workflowDependencyTestChild,
	)
	writeWorkflowDependencyDefinition(
		t,
		workspace,
		"automation",
		"workflows/root.yml",
		workflowDependencyTestRoot,
	)
	restore := stubWorkflowDependencyRuntime(t, nil)
	defer restore()

	recorder := httptest.NewRecorder()
	NewHandler(configPath).handleCheckWorkflowDependencies(
		recorder,
		httptest.NewRequest(
			http.MethodPost,
			"/api/workflows/dependencies/check",
			strings.NewReader(`{"ref":"workflows/root.yml"}`),
		),
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var response workflowDependencyCheckResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("response JSON error = %v", err)
	}
	if response.WorkflowEnabled || response.Ready ||
		!response.StructuralReady || !response.RuntimeReady {
		t.Fatalf("response = %#v", response)
	}
}

func TestWorkflowDependencyCheckBoundsReusableDefinitionBytes(t *testing.T) {
	workspace := t.TempDir()
	configPath := writeWorkflowDependencyTestConfig(t, workspace, true)
	writeWorkflowDependencyDefinition(
		t,
		workspace,
		"automation",
		"workflows/child.yml",
		strings.Repeat("x", int(workflows.MaxWorkflowDependencyDefinitionBytes)+1),
	)
	restore := stubWorkflowDependencyRuntime(t, nil)
	defer restore()

	response := checkWorkflowDependencyDraft(
		t,
		NewHandler(configPath),
		"workflows/root.yml",
		workflowDependencyTestRoot,
	)
	if response.StructuralReady || response.Ready {
		t.Fatalf("oversized child response = %#v", response)
	}
	if len(response.StructuralIssues) != 1 ||
		response.StructuralIssues[0].Code !=
			workflows.WorkflowDependencyIssueAnalysisLimitExceeded {
		t.Fatalf("structural issues = %#v", response.StructuralIssues)
	}
	if strings.Contains(
		fmt.Sprintf("%#v", response),
		workspace,
	) {
		t.Fatalf("response leaked workspace path: %#v", response)
	}
}

func TestWorkflowDependencyCheckRejectsUnsafeAndNonStrictRequests(t *testing.T) {
	configPath := writeWorkflowDependencyTestConfig(t, t.TempDir(), true)
	restore := stubWorkflowDependencyRuntime(t, nil)
	defer restore()
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "missing selector",
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_dependency_request",
		},
		{
			name:       "both selectors",
			body:       `{"ref":"workflows/a.yml","draft":{"target_ref":"workflows/a.yml","yaml":"name: a"}}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_dependency_request",
		},
		{
			name:       "unsafe ref",
			body:       `{"ref":"../secret.yml"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_dependency_request",
		},
		{
			name:       "missing published",
			body:       `{"ref":"workflows/missing.yml"}`,
			wantStatus: http.StatusNotFound,
			wantCode:   "workflow_not_found",
		},
		{
			name:       "invalid draft",
			body:       `{"draft":{"target_ref":"workflows/a.yml","yaml":"jobs: ["}}`,
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "workflow_invalid",
		},
		{
			name:       "unknown field",
			body:       `{"ref":"workflows/a.yml","path":"/tmp/private"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_dependency_request",
		},
		{
			name:       "trailing JSON",
			body:       `{"ref":"workflows/a.yml"}{}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_dependency_request",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			NewHandler(configPath).handleCheckWorkflowDependencies(
				recorder,
				httptest.NewRequest(
					http.MethodPost,
					"/api/workflows/dependencies/check",
					strings.NewReader(test.body),
				),
			)
			if recorder.Code != test.wantStatus ||
				!strings.Contains(recorder.Body.String(), test.wantCode) {
				t.Fatalf(
					"status = %d, body=%s, want status=%d code=%q",
					recorder.Code,
					recorder.Body.String(),
					test.wantStatus,
					test.wantCode,
				)
			}
			if strings.Contains(recorder.Body.String(), configPath) {
				t.Fatalf("response leaked config path: %s", recorder.Body.String())
			}
		})
	}

	oversized := httptest.NewRecorder()
	NewHandler(configPath).handleCheckWorkflowDependencies(
		oversized,
		httptest.NewRequest(
			http.MethodPost,
			"/api/workflows/dependencies/check",
			strings.NewReader(
				`{"draft":{"target_ref":"workflows/a.yml","yaml":"`+
					strings.Repeat("x", workflowDependencyCheckRequestMaxBytes)+
					`"}}`,
			),
		),
	)
	if oversized.Code != http.StatusRequestEntityTooLarge ||
		!strings.Contains(oversized.Body.String(), "dependency_request_too_large") {
		t.Fatalf("oversized status = %d, body=%s", oversized.Code, oversized.Body.String())
	}
}

func writeWorkflowDependencyTestConfig(
	t *testing.T,
	workspace string,
	enabled bool,
) string {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Workflows.Enabled = enabled
	cfg.Workflows.DefinitionsDir = "automation"
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	return configPath
}

func writeWorkflowDependencyDefinition(
	t *testing.T,
	workspace string,
	definitionsDir string,
	ref string,
	raw string,
) string {
	t.Helper()
	resolved, err := (workflows.Resolver{
		WorkspaceDir:   workspace,
		DefinitionsDir: definitionsDir,
	}).ResolveLocal(ref)
	if err != nil {
		t.Fatalf("ResolveLocal(%q) error = %v", ref, err)
	}
	if err := os.MkdirAll(filepath.Dir(resolved.Path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(resolved.Path, []byte(raw), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return resolved.Path
}

func stubWorkflowDependencyRuntime(
	t *testing.T,
	resolve func(workflows.WorkflowDependencyOccurrence) workflows.WorkflowDependencyReadinessCode,
) func() {
	t.Helper()
	previous := newWorkflowDependencyRuntime
	newWorkflowDependencyRuntime = func(
		string,
		*config.Config,
	) workflowDependencyRuntime {
		return &workflowDependencyRuntimeStub{resolve: resolve}
	}
	return func() {
		newWorkflowDependencyRuntime = previous
	}
}

func checkWorkflowDependencyDraft(
	t *testing.T,
	handler *Handler,
	ref string,
	raw string,
) workflowDependencyCheckResponse {
	t.Helper()
	body, err := json.Marshal(workflowDependencyCheckRequest{
		Draft: &workflowDependencyDraftRequest{
			TargetRef: ref,
			YAML:      workflowDependencyString(raw),
		},
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	recorder := httptest.NewRecorder()
	handler.handleCheckWorkflowDependencies(
		recorder,
		httptest.NewRequest(
			http.MethodPost,
			"/api/workflows/dependencies/check",
			strings.NewReader(string(body)),
		),
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var response workflowDependencyCheckResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("response JSON error = %v", err)
	}
	return response
}

func workflowDependencyString(value string) *string {
	return &value
}
