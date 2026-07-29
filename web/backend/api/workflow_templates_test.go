package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestWorkflowTemplateAPIListsInstallsAndExplicitlyRestoresModifiedTemplate(t *testing.T) {
	workspace := t.TempDir()
	configPath := writeWorkflowTemplateAPIConfig(t, workspace)
	h := NewHandler(configPath)

	listRecorder := httptest.NewRecorder()
	h.handleListWorkflowTemplates(
		listRecorder,
		httptest.NewRequest(http.MethodGet, "/api/workflows/templates", nil),
	)
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body=%s", listRecorder.Code, listRecorder.Body.String())
	}
	if strings.Contains(listRecorder.Body.String(), workspace) ||
		strings.Contains(listRecorder.Body.String(), `"path"`) {
		t.Fatalf("GET leaked filesystem details: %s", listRecorder.Body.String())
	}
	var listed struct {
		Templates []workflows.WorkflowTemplateCatalogEntry `json:"templates"`
	}
	if err := json.Unmarshal(listRecorder.Body.Bytes(), &listed); err != nil {
		t.Fatalf("GET response JSON error = %v", err)
	}
	codeReview := workflowTemplateEntryByName(
		t,
		listed.Templates,
		workflows.CodeReviewWorkflowName,
	)
	if codeReview.State != workflows.WorkflowTemplateStateAvailable {
		t.Fatalf("initial code-review state = %q", codeReview.State)
	}

	installRecorder := httptest.NewRecorder()
	installRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/workflows/templates/code-review/install",
		strings.NewReader(`{}`),
	)
	installRequest.SetPathValue("name", workflows.CodeReviewWorkflowName)
	h.handleInstallWorkflowTemplate(installRecorder, installRequest)
	if installRecorder.Code != http.StatusOK {
		t.Fatalf(
			"install status = %d, body=%s",
			installRecorder.Code,
			installRecorder.Body.String(),
		)
	}
	if strings.Contains(installRecorder.Body.String(), workspace) ||
		strings.Contains(installRecorder.Body.String(), `"path"`) {
		t.Fatalf("install leaked filesystem details: %s", installRecorder.Body.String())
	}
	resolved, err := (workflows.Resolver{
		WorkspaceDir:   workspace,
		DefinitionsDir: "automation",
	}).ResolveLocal(workflows.CodeReviewWorkflowRef)
	if err != nil {
		t.Fatalf("ResolveLocal() error = %v", err)
	}
	if data, readErr := os.ReadFile(resolved.Path); readErr != nil ||
		string(data) != workflows.CodeReviewWorkflowYAML {
		t.Fatalf("installed template read error=%v, bytes=%q", readErr, data)
	}

	if err := os.WriteFile(resolved.Path, []byte("operator customization\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(modified) error = %v", err)
	}
	conflictRecorder := httptest.NewRecorder()
	conflictRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/workflows/templates/code-review/install",
		strings.NewReader(`{}`),
	)
	conflictRequest.SetPathValue("name", workflows.CodeReviewWorkflowName)
	h.handleInstallWorkflowTemplate(conflictRecorder, conflictRequest)
	if conflictRecorder.Code != http.StatusConflict ||
		!strings.Contains(conflictRecorder.Body.String(), "template_overwrite_required") {
		t.Fatalf(
			"modified install status = %d, body=%s",
			conflictRecorder.Code,
			conflictRecorder.Body.String(),
		)
	}

	restoreRecorder := httptest.NewRecorder()
	restoreRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/workflows/templates/code-review/install",
		strings.NewReader(`{"overwrite":true}`),
	)
	restoreRequest.SetPathValue("name", workflows.CodeReviewWorkflowName)
	h.handleInstallWorkflowTemplate(restoreRecorder, restoreRequest)
	if restoreRecorder.Code != http.StatusOK {
		t.Fatalf(
			"restore status = %d, body=%s",
			restoreRecorder.Code,
			restoreRecorder.Body.String(),
		)
	}
	if data, readErr := os.ReadFile(resolved.Path); readErr != nil ||
		string(data) != workflows.CodeReviewWorkflowYAML {
		t.Fatalf("restored template read error=%v, bytes=%q", readErr, data)
	}
}

func TestInstallWorkflowTemplateRejectsActiveDevelopmentAndUnsafeRequests(t *testing.T) {
	workspace := t.TempDir()
	configPath := writeWorkflowTemplateAPIConfig(t, workspace)
	h := NewHandler(configPath)
	if _, err := workflows.StartWorkflowDevelopment(
		context.Background(),
		workspace,
		workflows.RuntimeCompatibility{PicoclawVersion: "v1.0.0"},
		workflows.WorkflowDevelopmentStartRequest{
			Prompt:    "active development",
			TargetRef: "workflows/active.yml",
		},
		workflows.WithDefinitionsDir("automation"),
	); err != nil {
		t.Fatalf("StartWorkflowDevelopment() error = %v", err)
	}

	activeRecorder := httptest.NewRecorder()
	activeRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/workflows/templates/code-review/install",
		strings.NewReader(`{}`),
	)
	activeRequest.SetPathValue("name", workflows.CodeReviewWorkflowName)
	h.handleInstallWorkflowTemplate(activeRecorder, activeRequest)
	if activeRecorder.Code != http.StatusConflict ||
		!strings.Contains(activeRecorder.Body.String(), "workflow_development_active") {
		t.Fatalf(
			"active status = %d, body=%s",
			activeRecorder.Code,
			activeRecorder.Body.String(),
		)
	}

	if _, err := workflows.DiscardWorkflowDevelopment(workspace); err != nil {
		t.Fatalf("DiscardWorkflowDevelopment() error = %v", err)
	}
	tests := []struct {
		name       string
		template   string
		body       string
		wantStatus int
	}{
		{
			name:       "unknown template",
			template:   "unknown",
			body:       `{}`,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "unknown request field",
			template:   workflows.CodeReviewWorkflowName,
			body:       `{"path":"/tmp/no"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "trailing JSON",
			template:   workflows.CodeReviewWorkflowName,
			body:       `{}{}`,
			wantStatus: http.StatusBadRequest,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/workflows/templates/"+test.template+"/install",
				strings.NewReader(test.body),
			)
			request.SetPathValue("name", test.template)
			h.handleInstallWorkflowTemplate(recorder, request)
			if recorder.Code != test.wantStatus {
				t.Fatalf(
					"status = %d, want %d, body=%s",
					recorder.Code,
					test.wantStatus,
					recorder.Body.String(),
				)
			}
		})
	}
}

func TestInstallWorkflowTemplateRejectsOversizedRequest(t *testing.T) {
	h := NewHandler(filepath.Join(t.TempDir(), "config.json"))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/workflows/templates/code-review/install",
		strings.NewReader(`{"overwrite":false,"padding":"`+
			strings.Repeat("x", workflowTemplateInstallRequestMaxBytes)+`"}`),
	)
	request.SetPathValue("name", workflows.CodeReviewWorkflowName)
	h.handleInstallWorkflowTemplate(recorder, request)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
}

func writeWorkflowTemplateAPIConfig(t *testing.T, workspace string) string {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Workflows.Enabled = true
	cfg.Workflows.DefinitionsDir = "automation"
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	return configPath
}

func workflowTemplateEntryByName(
	t *testing.T,
	entries []workflows.WorkflowTemplateCatalogEntry,
	name string,
) workflows.WorkflowTemplateCatalogEntry {
	t.Helper()
	for _, entry := range entries {
		if entry.Name == name {
			return entry
		}
	}
	t.Fatalf("template %q absent from %#v", name, entries)
	return workflows.WorkflowTemplateCatalogEntry{}
}
