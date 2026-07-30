package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestWorkflowSettingsGetAndPatchPreserveUnrelatedConfigAndSecrets(t *testing.T) {
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Gateway.Port = 23456
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "private-model",
		Provider:  "openai",
		Model:     "openai/test",
		APIKeys:   config.SimpleSecureStrings("sk-workflow-settings-secret"),
		Enabled:   true,
	}}
	cfg.Agents.Defaults.ModelName = "private-model"
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	h := NewHandler(configPath)

	getRecorder := httptest.NewRecorder()
	h.handleGetWorkflowSettings(
		getRecorder,
		httptest.NewRequest(http.MethodGet, "/api/workflows/settings", nil),
	)
	if getRecorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body=%s", getRecorder.Code, getRecorder.Body.String())
	}
	if strings.Contains(getRecorder.Body.String(), "sk-workflow-settings-secret") ||
		strings.Contains(getRecorder.Body.String(), "private-model") {
		t.Fatalf("GET leaked unrelated configuration: %s", getRecorder.Body.String())
	}
	var getResponse workflowSettingsResponse
	if err := json.Unmarshal(getRecorder.Body.Bytes(), &getResponse); err != nil {
		t.Fatalf("GET response JSON error = %v", err)
	}
	if getResponse.ConfigRevision == "" ||
		getResponse.Effective.MaxConcurrentRuns <= 0 ||
		getResponse.Effective.DefaultTimeoutSeconds <= 0 {
		t.Fatalf("GET response = %#v", getResponse)
	}
	if !getResponse.Configured.ToolEnabled || !getResponse.Effective.ToolEnabled {
		t.Fatalf("GET workflow tool settings = %#v", getResponse)
	}
	if getRecorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("GET Cache-Control = %q", getRecorder.Header().Get("Cache-Control"))
	}

	patchBody, err := json.Marshal(map[string]any{
		"expected_config_revision": getResponse.ConfigRevision,
		"enabled":                  true,
		"tool_enabled":             false,
		"max_concurrent_runs":      7,
		"retention_days":           45,
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	patchRecorder := httptest.NewRecorder()
	h.handlePatchWorkflowSettings(
		patchRecorder,
		httptest.NewRequest(
			http.MethodPatch,
			"/api/workflows/settings",
			strings.NewReader(string(patchBody)),
		),
	)
	if patchRecorder.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, body=%s", patchRecorder.Code, patchRecorder.Body.String())
	}
	var patchResponse workflowSettingsResponse
	if decodeErr := json.Unmarshal(patchRecorder.Body.Bytes(), &patchResponse); decodeErr != nil {
		t.Fatalf("PATCH response JSON error = %v", decodeErr)
	}
	if !patchResponse.Configured.Enabled ||
		patchResponse.Configured.ToolEnabled ||
		patchResponse.Effective.ToolEnabled ||
		patchResponse.Configured.MaxConcurrentRuns != 7 ||
		patchResponse.Configured.RetentionDays != 45 ||
		patchResponse.ConfigRevision == getResponse.ConfigRevision {
		t.Fatalf("PATCH response = %#v", patchResponse)
	}
	if patchResponse.Effects.LauncherEffect != "applied" ||
		patchResponse.Effects.CatalogEffect != "applied" {
		t.Fatalf("PATCH effects = %#v", patchResponse.Effects)
	}

	saved, err := config.LoadConfigForUpdate(configPath)
	if err != nil {
		t.Fatalf("LoadConfigForUpdate() error = %v", err)
	}
	if saved.Gateway.Port != 23456 ||
		saved.Tools.Workflow.Enabled ||
		saved.Agents.Defaults.GetModelName() != "private-model" ||
		len(saved.ModelList) != 1 ||
		saved.ModelList[0].APIKey() != "sk-workflow-settings-secret" {
		t.Fatalf("PATCH changed unrelated config or secrets: %#v", saved)
	}
}

func TestWorkflowSettingsToolFlagAndMasterRemainIndependent(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Workflows.Enabled = false
	cfg.Tools.Workflow.Enabled = true
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	h := NewHandler(configPath)

	get := func() workflowSettingsResponse {
		t.Helper()
		recorder := httptest.NewRecorder()
		h.handleGetWorkflowSettings(
			recorder,
			httptest.NewRequest(http.MethodGet, "/api/workflows/settings", nil),
		)
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET status = %d, body=%s", recorder.Code, recorder.Body.String())
		}
		var response workflowSettingsResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("GET response JSON error = %v", err)
		}
		return response
	}
	patch := func(revision string, body string) workflowSettingsResponse {
		t.Helper()
		recorder := httptest.NewRecorder()
		h.handlePatchWorkflowSettings(
			recorder,
			httptest.NewRequest(
				http.MethodPatch,
				"/api/workflows/settings",
				strings.NewReader(`{"expected_config_revision":"`+revision+`",`+body+`}`),
			),
		)
		if recorder.Code != http.StatusOK {
			t.Fatalf("PATCH status = %d, body=%s", recorder.Code, recorder.Body.String())
		}
		var response workflowSettingsResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("PATCH response JSON error = %v", err)
		}
		return response
	}

	initial := get()
	if initial.Configured.Enabled ||
		!initial.Configured.ToolEnabled ||
		initial.Effective.Enabled ||
		initial.Effective.ToolEnabled {
		t.Fatalf("initial settings = %#v", initial)
	}

	toolDisabled := patch(initial.ConfigRevision, `"tool_enabled":false`)
	if toolDisabled.Configured.Enabled ||
		toolDisabled.Configured.ToolEnabled ||
		toolDisabled.Effective.Enabled ||
		toolDisabled.Effective.ToolEnabled {
		t.Fatalf("tool-only PATCH changed master or effective state: %#v", toolDisabled)
	}

	masterEnabled := patch(toolDisabled.ConfigRevision, `"enabled":true`)
	if !masterEnabled.Configured.Enabled ||
		masterEnabled.Configured.ToolEnabled ||
		!masterEnabled.Effective.Enabled ||
		masterEnabled.Effective.ToolEnabled {
		t.Fatalf("master-only PATCH changed raw tool flag: %#v", masterEnabled)
	}

	toolEnabled := patch(masterEnabled.ConfigRevision, `"tool_enabled":true`)
	if !toolEnabled.Configured.Enabled ||
		!toolEnabled.Configured.ToolEnabled ||
		!toolEnabled.Effective.Enabled ||
		!toolEnabled.Effective.ToolEnabled {
		t.Fatalf("enabled settings = %#v", toolEnabled)
	}
}

func TestToolStateMutationInvalidatesWorkflowSettingsRevision(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Workflows.Enabled = true
	cfg.Tools.Workflow.Enabled = true
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	initialRecorder := httptest.NewRecorder()
	mux.ServeHTTP(
		initialRecorder,
		httptest.NewRequest(http.MethodGet, "/api/workflows/settings", nil),
	)
	if initialRecorder.Code != http.StatusOK {
		t.Fatalf(
			"initial GET status = %d, body=%s",
			initialRecorder.Code,
			initialRecorder.Body.String(),
		)
	}
	var initial workflowSettingsResponse
	if err := json.Unmarshal(initialRecorder.Body.Bytes(), &initial); err != nil {
		t.Fatalf("initial GET response JSON error = %v", err)
	}

	toolRecorder := httptest.NewRecorder()
	toolRequest := httptest.NewRequest(
		http.MethodPut,
		"/api/tools/workflow/state",
		strings.NewReader(`{"enabled":false}`),
	)
	toolRequest.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(toolRecorder, toolRequest)
	if toolRecorder.Code != http.StatusOK {
		t.Fatalf(
			"tool PUT status = %d, body=%s",
			toolRecorder.Code,
			toolRecorder.Body.String(),
		)
	}

	staleRecorder := httptest.NewRecorder()
	mux.ServeHTTP(
		staleRecorder,
		httptest.NewRequest(
			http.MethodPatch,
			"/api/workflows/settings",
			strings.NewReader(`{"expected_config_revision":"`+
				initial.ConfigRevision+`","retention_days":60}`),
		),
	)
	if staleRecorder.Code != http.StatusConflict ||
		!strings.Contains(staleRecorder.Body.String(), "config_revision_mismatch") {
		t.Fatalf(
			"stale PATCH response = %d/%q",
			staleRecorder.Code,
			staleRecorder.Body.String(),
		)
	}

	currentRecorder := httptest.NewRecorder()
	mux.ServeHTTP(
		currentRecorder,
		httptest.NewRequest(http.MethodGet, "/api/workflows/settings", nil),
	)
	var current workflowSettingsResponse
	if err := json.Unmarshal(currentRecorder.Body.Bytes(), &current); err != nil {
		t.Fatalf("current GET response JSON error = %v", err)
	}
	if !current.Configured.Enabled ||
		current.Configured.ToolEnabled ||
		!current.Effective.Enabled ||
		current.Effective.ToolEnabled ||
		current.ConfigRevision == initial.ConfigRevision {
		t.Fatalf("current workflow settings = %#v", current)
	}
}

func TestPatchWorkflowSettingsRejectsStaleInvalidAndNonStrictRequests(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	revision, err := workflowConfigFileRevision(configPath)
	if err != nil {
		t.Fatalf("workflowConfigFileRevision() error = %v", err)
	}

	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{
			name:       "stale revision",
			body:       `{"expected_config_revision":"sha256:stale","enabled":true}`,
			wantStatus: http.StatusConflict,
		},
		{
			name: "unsafe definitions dir",
			body: `{"expected_config_revision":"` + revision +
				`","definitions_dir":"../outside"}`,
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name: "negative limit",
			body: `{"expected_config_revision":"` + revision +
				`","max_concurrent_runs":-1}`,
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name: "excessive concurrency",
			body: `{"expected_config_revision":"` + revision +
				`","max_concurrent_runs":` +
				strconv.Itoa(config.MaxWorkflowMaxConcurrentRuns+1) + `}`,
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name: "excessive timeout",
			body: `{"expected_config_revision":"` + revision +
				`","default_timeout_seconds":` +
				strconv.Itoa(config.MaxWorkflowDefaultTimeoutSeconds+1) + `}`,
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name: "excessive call depth",
			body: `{"expected_config_revision":"` + revision +
				`","max_call_depth":` +
				strconv.Itoa(config.MaxWorkflowMaxCallDepth+1) + `}`,
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name: "excessive retention",
			body: `{"expected_config_revision":"` + revision +
				`","retention_days":` +
				strconv.Itoa(config.MaxWorkflowRetentionDays+1) + `}`,
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name: "unknown field",
			body: `{"expected_config_revision":"` + revision +
				`","enabled":true,"secret":"no"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "trailing JSON",
			body: `{"expected_config_revision":"` + revision +
				`","enabled":true}{}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing revision",
			body:       `{"enabled":true}`,
			wantStatus: http.StatusBadRequest,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := NewHandler(configPath)
			recorder := httptest.NewRecorder()
			h.handlePatchWorkflowSettings(
				recorder,
				httptest.NewRequest(
					http.MethodPatch,
					"/api/workflows/settings",
					strings.NewReader(test.body),
				),
			)
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

func TestPatchWorkflowSettingsRejectsDefinitionsChangeDuringDevelopment(t *testing.T) {
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	if _, err := workflows.StartWorkflowDevelopment(
		context.Background(),
		workspace,
		workflows.RuntimeCompatibility{PicoclawVersion: "settings-test"},
		workflows.WorkflowDevelopmentStartRequest{
			Prompt:    "active settings fence",
			TargetRef: "workflows/settings-fence.yml",
		},
	); err != nil {
		t.Fatalf("StartWorkflowDevelopment() error = %v", err)
	}
	revision, err := workflowConfigFileRevision(configPath)
	if err != nil {
		t.Fatalf("workflowConfigFileRevision() error = %v", err)
	}

	h := NewHandler(configPath)
	recorder := httptest.NewRecorder()
	h.handlePatchWorkflowSettings(
		recorder,
		httptest.NewRequest(
			http.MethodPatch,
			"/api/workflows/settings",
			strings.NewReader(`{"expected_config_revision":"`+revision+
				`","definitions_dir":"automation/workflows"}`),
		),
	)
	if recorder.Code != http.StatusConflict ||
		!strings.Contains(recorder.Body.String(), "workflow_development_active") {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	saved, err := config.LoadConfigForUpdate(configPath)
	if err != nil {
		t.Fatalf("LoadConfigForUpdate() error = %v", err)
	}
	if saved.Workflows.EffectiveDefinitionsDir() != cfg.Workflows.EffectiveDefinitionsDir() {
		t.Fatalf("definitions dir changed during active development: %#v", saved.Workflows)
	}
}

func TestPatchWorkflowSettingsRejectsOversizedBody(t *testing.T) {
	h := NewHandler(filepath.Join(t.TempDir(), "config.json"))
	recorder := httptest.NewRecorder()
	h.handlePatchWorkflowSettings(
		recorder,
		httptest.NewRequest(
			http.MethodPatch,
			"/api/workflows/settings",
			strings.NewReader(`{"expected_config_revision":"`+
				strings.Repeat("x", workflowSettingsRequestMaxBytes)+`"}`),
		),
	)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestPatchWorkflowSettingsRejectsDefinitionsRootSymlinkEscape(t *testing.T) {
	if testing.Short() {
		t.Skip("symlink test skipped in short mode")
	}
	workspace := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workspace, "automation")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	revision, err := workflowConfigFileRevision(configPath)
	if err != nil {
		t.Fatalf("workflowConfigFileRevision() error = %v", err)
	}
	recorder := httptest.NewRecorder()
	NewHandler(configPath).handlePatchWorkflowSettings(
		recorder,
		httptest.NewRequest(
			http.MethodPatch,
			"/api/workflows/settings",
			strings.NewReader(`{"expected_config_revision":"`+revision+
				`","definitions_dir":"automation"}`),
		),
	)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	saved, err := config.LoadConfigForUpdate(configPath)
	if err != nil {
		t.Fatalf("LoadConfigForUpdate() error = %v", err)
	}
	if saved.Workflows.DefinitionsDir != cfg.Workflows.DefinitionsDir {
		t.Fatalf("unsafe definitions root was saved: %#v", saved.Workflows)
	}
}
