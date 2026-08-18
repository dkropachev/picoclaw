package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/prworkspace/lifecycleflow"
	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

func TestPRLifecycleGateConfigsResponsesUseV3ContractAndCanonicalFlow(t *testing.T) {
	_, _, mux := prLifecycleGateConfigTestServer(t)
	wantFlow, wantRevision := lifecycleflow.Default()

	request := httptest.NewRequest(http.MethodGet, "http://launcher.local"+prLifecycleGateConfigsPath, nil)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"gate-configs", "default-gate-config", "repository-assignments", "nudge",
		"scope", "gate-catalog", "flow", "flow-revision",
		"catalog-revision", "config-revision", "effects",
	} {
		if _, exists := raw[key]; !exists {
			t.Fatalf("GET response omits %q: %s", key, recorder.Body.String())
		}
	}
	for _, legacy := range []string{"gate_profiles", "gate-profiles", "default_gate_profile_id"} {
		if _, exists := raw[legacy]; exists {
			t.Fatalf("GET response contains retired key %q", legacy)
		}
	}
	current := decodePRLifecycleGateConfigsResponse(t, recorder.Body.Bytes())
	if current.FlowRevision != wantRevision || !reflect.DeepEqual(current.Flow, wantFlow) {
		t.Fatalf("GET flow revision = %q flow = %#v", current.FlowRevision, current.Flow)
	}
	if current.Effects.GatewayEffect != "applied" || current.Effects.DeferredPolicyEffect != "applied" {
		t.Fatalf("initial effects = %#v", current.Effects)
	}
	builtin := current.GateConfigs[config.DefaultPRLifecycleGateConfigID]
	if builtin.Name != config.DefaultPRLifecycleGateConfigName || len(builtin.Bindings) != 0 {
		t.Fatalf("built-in gate configuration = %#v", builtin)
	}
	if builtin.DeferredIssues.Mode != config.PRLifecycleDeferredIssuesAsk {
		t.Fatalf("built-in deferred issue policy = %#v", builtin.DeferredIssues)
	}
	if len(current.GateCatalog) < 14 || current.GateCatalog["pr.charter.confirm"].GateRef != "gates.charter-confirm" ||
		current.GateCatalog["pr.charter.confirm"].DefaultAction == nil ||
		current.GateCatalog["pr.charter.confirm"].Prompt == "" ||
		len(current.GateCatalog["pr.charter.confirm"].Fields) == 0 {
		t.Fatalf("built-in gate catalog = %#v", current.GateCatalog)
	}
	if current.GateCatalog["pr.charter.confirm"].SourceAISupported ||
		!current.GateCatalog["pr.finding.classify"].SourceAISupported {
		t.Fatalf("source AI gate support = %#v", current.GateCatalog)
	}
}

func TestPRLifecycleGateConfigsPutSavesValidAtomicOverrides(t *testing.T) {
	configPath, _, mux := prLifecycleGateConfigTestServer(t)
	current := getPRLifecycleGateConfigsForTest(t, mux)
	candidate := mixedPRLifecycleGateConfigCandidate()
	request := putPRLifecycleGateConfigsForTest(t, mux, current.ConfigRevision, candidate, nil)
	if request.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body=%s", request.Code, request.Body.String())
	}
	response := decodePRLifecycleGateConfigsResponse(t, request.Body.Bytes())
	if response.ConfigRevision == current.ConfigRevision || response.CatalogRevision == "" ||
		response.Effects.GatewayEffect != "restart-required" {
		t.Fatalf("PUT response = %#v", response)
	}
	reloaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.PRLifecycle.DefaultGateConfigID != "mixed" ||
		reloaded.PRLifecycle.RepositoryAssignments["https://github.com|repo-42"] != "mixed" ||
		len(reloaded.PRLifecycle.GateConfigs["mixed"].Bindings) != 4 {
		t.Fatalf("reloaded lifecycle = %#v", reloaded.PRLifecycle)
	}
	if got := reloaded.PRLifecycle.GateConfigs["mixed"].Bindings[1].Action; got == nil ||
		got.Type != gatetypes.GateActionAI || got.AgentID != "main" {
		t.Fatalf("reloaded AI override = %#v", got)
	}
}

func TestPRLifecycleGateConfigsGatewayEffectTracksExactSavedGeneration(t *testing.T) {
	configPath, handler, mux := prLifecycleGateConfigTestServer(t)
	initial := getPRLifecycleGateConfigsForTest(t, mux)
	candidate := mixedPRLifecycleGateConfigCandidate()
	put := putPRLifecycleGateConfigsForTest(t, mux, initial.ConfigRevision, candidate, nil)
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body=%s", put.Code, put.Body.String())
	}
	if got := getPRLifecycleGateConfigsForTest(t, mux).Effects.GatewayEffect; got != "restart-required" {
		t.Fatalf("saved gateway effect = %q", got)
	}

	handler.markPRLifecycleGatewayApplied(config.DefaultPRLifecycleConfig())
	if got := getPRLifecycleGateConfigsForTest(t, mux).Effects.GatewayEffect; got != "restart-required" {
		t.Fatalf("mismatched gateway effect = %q", got)
	}
	handler.markPRLifecycleGatewayApplied(candidate)
	if got := getPRLifecycleGateConfigsForTest(t, mux).Effects.GatewayEffect; got != "applied" {
		t.Fatalf("matching gateway effect = %q", got)
	}

	restartedHandler := NewHandler(configPath)
	restartedMux := http.NewServeMux()
	restartedHandler.registerPRLifecycleGateConfigRoutes(restartedMux)
	if got := getPRLifecycleGateConfigsForTest(t, restartedMux).Effects.GatewayEffect; got != "applied" {
		t.Fatalf("new handler gateway effect = %q", got)
	}
}

func TestPRLifecycleGateConfigsDeferredEffectTracksOnlyActivePolicyRouting(t *testing.T) {
	_, handler, mux := prLifecycleGateConfigTestServer(t)
	active := config.DefaultPRLifecycleConfig()
	handler.markPRLifecycleGatewayApplied(active)
	initial := getPRLifecycleGateConfigsForTest(t, mux)

	unrelated := active
	unrelated.Nudge.ReviewMinimumAdditional++
	put := putPRLifecycleGateConfigsForTest(t, mux, initial.ConfigRevision, unrelated, nil)
	if put.Code != http.StatusOK {
		t.Fatalf("unrelated PUT status = %d body=%s", put.Code, put.Body.String())
	}
	saved := decodePRLifecycleGateConfigsResponse(t, put.Body.Bytes())
	if saved.Effects.GatewayEffect != "restart-required" || saved.Effects.DeferredPolicyEffect != "applied" {
		t.Fatalf("unrelated effects = %#v", saved.Effects)
	}

	handler.markPRLifecycleGatewayApplied(unrelated)
	current := getPRLifecycleGateConfigsForTest(t, mux)
	changed := unrelated
	defaultConfig := changed.GateConfigs[config.DefaultPRLifecycleGateConfigID]
	defaultConfig.DeferredIssues.Mode = config.PRLifecycleDeferredIssuesOff
	changed.GateConfigs = map[string]config.PRLifecycleGateConfig{
		config.DefaultPRLifecycleGateConfigID: defaultConfig,
	}
	put = putPRLifecycleGateConfigsForTest(t, mux, current.ConfigRevision, changed, nil)
	if put.Code != http.StatusOK {
		t.Fatalf("deferred PUT status = %d body=%s", put.Code, put.Body.String())
	}
	saved = decodePRLifecycleGateConfigsResponse(t, put.Body.Bytes())
	if saved.Effects.DeferredPolicyEffect != "restart-required" {
		t.Fatalf("deferred effects = %#v", saved.Effects)
	}
}

func TestPRLifecycleGateConfigsPutRejectsSemanticErrorsBeforePersistence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*config.PRLifecycleConfig)
	}{
		{name: "duplicate binding", mutate: func(c *config.PRLifecycleConfig) {
			mixed := c.GateConfigs["mixed"]
			mixed.Bindings = append(mixed.Bindings, mixed.Bindings[0])
			c.GateConfigs["mixed"] = mixed
		}},
		{name: "invalid gate ref", mutate: func(c *config.PRLifecycleConfig) {
			mixed := c.GateConfigs["mixed"]
			mixed.Bindings[0].GateRef = "charter-decision"
			c.GateConfigs["mixed"] = mixed
		}},
		{name: "partial action", mutate: func(c *config.PRLifecycleConfig) {
			mixed := c.GateConfigs["mixed"]
			mixed.Bindings[0].Action = &gatetypes.GateAction{Type: gatetypes.GateActionHuman, Prompt: "not allowed"}
			c.GateConfigs["mixed"] = mixed
		}},
		{name: "unknown agent", mutate: func(c *config.PRLifecycleConfig) {
			mixed := c.GateConfigs["mixed"]
			action := *mixed.Bindings[1].Action
			action.AgentID = "missing-reviewer"
			mixed.Bindings[1].Action = &action
			c.GateConfigs["mixed"] = mixed
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configPath, _, mux := prLifecycleGateConfigTestServer(t)
			current := getPRLifecycleGateConfigsForTest(t, mux)
			candidate := mixedPRLifecycleGateConfigCandidate()
			test.mutate(&candidate)
			response := putPRLifecycleGateConfigsForTest(t, mux, current.ConfigRevision, candidate, nil)
			if response.Code != http.StatusUnprocessableEntity ||
				!strings.Contains(response.Body.String(), "invalid_gate_configs") {
				t.Fatalf("PUT status = %d body=%s", response.Code, response.Body.String())
			}
			reloaded, err := config.LoadConfig(configPath)
			if err != nil {
				t.Fatal(err)
			}
			if _, exists := reloaded.PRLifecycle.Effective().GateConfigs["mixed"]; exists {
				t.Fatal("invalid gate configuration was persisted")
			}
		})
	}
}

func TestPRLifecycleGateConfigsPutRejectsStaleRevision(t *testing.T) {
	configPath, _, mux := prLifecycleGateConfigTestServer(t)
	response := putPRLifecycleGateConfigsForTest(
		t, mux, "sha256:stale-config-revision", mixedPRLifecycleGateConfigCandidate(), nil,
	)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "config_revision_mismatch") {
		t.Fatalf("PUT status = %d body=%s", response.Code, response.Body.String())
	}
	reloaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := reloaded.PRLifecycle.Effective().GateConfigs["mixed"]; exists {
		t.Fatal("stale gate configuration update was persisted")
	}
}

func TestPRLifecycleGateConfigsPutRejectsMissingAndEffectfulActionWorkflows(t *testing.T) {
	for _, test := range []struct {
		name string
		ref  string
		body string
	}{
		{name: "missing", ref: "workflows/gate-actions/missing.yml"},
		{
			name: "effectful",
			ref:  "workflows/gate-actions/effectful.yml",
			body: `
name: Effectful action
on:
  workflow_call:
    outputs:
      field-values:
        value: ${{ jobs.run.outputs.field-values }}
jobs:
  run:
    runs-on: picoclaw
    outputs:
      field-values: ${{ steps.send.outputs.field-values }}
    steps:
      - id: send
        uses: function/exfiltrate
`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			configPath, _, mux := prLifecycleGateConfigTestServer(t)
			if test.body != "" {
				cfg, err := config.LoadConfig(configPath)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(cfg.WorkspacePath(), filepath.FromSlash(test.ref))
				if err := os.WriteFile(path, []byte(test.body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			current := getPRLifecycleGateConfigsForTest(t, mux)
			candidate := mixedPRLifecycleGateConfigCandidate()
			mixed := candidate.GateConfigs["mixed"]
			mixed.Bindings[3].Action.WorkflowRef = test.ref
			candidate.GateConfigs["mixed"] = mixed
			response := putPRLifecycleGateConfigsForTest(t, mux, current.ConfigRevision, candidate, nil)
			if response.Code != http.StatusUnprocessableEntity ||
				!strings.Contains(response.Body.String(), "invalid_gate_configs") {
				t.Fatalf("PUT status = %d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestPRLifecycleGateConfigsRequestsAreStrictAndOldRouteIsGone(t *testing.T) {
	_, _, mux := prLifecycleGateConfigTestServer(t)
	oldRequest := httptest.NewRequest(http.MethodGet, "http://launcher.local/api/pr-lifecycle/gate-profiles", nil)
	oldResponse := httptest.NewRecorder()
	mux.ServeHTTP(oldResponse, oldRequest)
	if oldResponse.Code != http.StatusNotFound {
		t.Fatalf("retired route status = %d", oldResponse.Code)
	}

	current := getPRLifecycleGateConfigsForTest(t, mux)
	candidate := mixedPRLifecycleGateConfigCandidate()
	validBody := prLifecycleGateConfigsPutRequestForTest(current.ConfigRevision, candidate)
	encoded, err := json.Marshal(validBody)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name        string
		body        []byte
		contentType string
		fetchSite   string
	}{
		{name: "wrong content type", body: encoded, contentType: "text/plain", fetchSite: "same-origin"},
		{name: "cross site", body: encoded, contentType: "application/json", fetchSite: "cross-site"},
		{name: "snake case field", body: append(append([]byte(nil), encoded[:len(encoded)-1]...), []byte(`,"request_id":"legacy"}`)...), contentType: "application/json", fetchSite: "same-origin"},
		{name: "unknown field", body: append(append([]byte(nil), encoded[:len(encoded)-1]...), []byte(`,"unknown":true}`)...), contentType: "application/json", fetchSite: "same-origin"},
		{name: "trailing value", body: append(append([]byte(nil), encoded...), []byte(` {}`)...), contentType: "application/json", fetchSite: "same-origin"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPut, "http://launcher.local"+prLifecycleGateConfigsPath, bytes.NewReader(test.body))
			request.Header.Set("Content-Type", test.contentType)
			request.Header.Set("Sec-Fetch-Site", test.fetchSite)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func prLifecycleGateConfigTestServer(t *testing.T) (string, *Handler, *http.ServeMux) {
	t.Helper()
	root := t.TempDir()
	configPath := filepath.Join(root, "config.json")
	workspace := filepath.Join(root, "workspace")
	actionDir := filepath.Join(workspace, "workflows", "gate-actions")
	if err := os.MkdirAll(actionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	actionWorkflow := `
name: Publication gate action
gates:
  result:
    prompt: Choose publication action.
    fields:
      - id: action
        type: select
        label: Action
        min-selections: 1
        max-selections: 1
        options:
          - {id: publish, label: Publish}
          - {id: revise, label: Revise}
          - {id: stop, label: Stop}
    default-action:
      type: deterministic
      fields: {action: publish}
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
        with: {gate-ref: gates.result}
`
	if err := os.WriteFile(filepath.Join(actionDir, "publication.yml"), []byte(actionWorkflow), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.PRLifecycle = config.DefaultPRLifecycleConfig()
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(configPath)
	mux := http.NewServeMux()
	handler.registerPRLifecycleGateConfigRoutes(mux)
	return configPath, handler, mux
}

func getPRLifecycleGateConfigsForTest(t *testing.T, mux *http.ServeMux) prLifecycleGateConfigsResponse {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "http://launcher.local"+prLifecycleGateConfigsPath, nil)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%s", response.Code, response.Body.String())
	}
	result := decodePRLifecycleGateConfigsResponse(t, response.Body.Bytes())
	if result.ConfigRevision == "" || result.CatalogRevision == "" {
		t.Fatalf("GET response = %#v", result)
	}
	return result
}

func decodePRLifecycleGateConfigsResponse(t *testing.T, body []byte) prLifecycleGateConfigsResponse {
	t.Helper()
	var result prLifecycleGateConfigsResponse
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func putPRLifecycleGateConfigsForTest(
	t *testing.T,
	mux *http.ServeMux,
	revision string,
	candidate config.PRLifecycleConfig,
	mutate func(*http.Request),
) *httptest.ResponseRecorder {
	t.Helper()
	body := prLifecycleGateConfigsPutRequestForTest(revision, candidate)
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, "http://launcher.local"+prLifecycleGateConfigsPath, bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	if mutate != nil {
		mutate(request)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func prLifecycleGateConfigsPutRequestForTest(
	revision string,
	candidate config.PRLifecycleConfig,
) prLifecycleGateConfigsPutRequest {
	return prLifecycleGateConfigsPutRequest{
		ExpectedConfigRevision: revision,
		RequestID:              "request-save-mixed-gate-config",
		GateConfigs:            candidate.GateConfigs,
		DefaultGateConfigID:    candidate.DefaultGateConfigID,
		RepositoryAssignments:  candidate.RepositoryAssignments,
		Nudge:                  candidate.Nudge,
		Scope:                  candidate.Scope,
	}
}

func mixedPRLifecycleGateConfigCandidate() config.PRLifecycleConfig {
	candidate := config.DefaultPRLifecycleConfig()
	candidate.GateConfigs["mixed"] = config.PRLifecycleGateConfig{
		Name: "Mixed", DeferredIssues: config.PRLifecycleDeferredIssueConfig{Mode: config.PRLifecycleDeferredIssuesAutomatic},
		Bindings: []config.PRLifecycleGateBinding{
			{
				WorkflowRef: "workflows/pr-lifecycle.yml", GateRef: "gates.charter-confirm",
				Action: &gatetypes.GateAction{Type: gatetypes.GateActionHuman},
			},
			{
				WorkflowRef: "workflows/pr-lifecycle.yml", GateRef: "gates.review-complete",
				Action: &gatetypes.GateAction{
					Type: gatetypes.GateActionAI, AgentID: "main", Prompt: "Review the evidence and complete the gate fields.",
					Session: "ephemeral", History: "none", Cache: "none", Tools: "none",
				},
			},
			{
				WorkflowRef: "workflows/pr-lifecycle.yml", GateRef: "gates.review-start",
				Action: &gatetypes.GateAction{
					Type:   gatetypes.GateActionDeterministic,
					Fields: map[string]any{"action": "continue"},
				},
			},
			{
				WorkflowRef: "workflows/pr-lifecycle.yml", GateRef: "gates.review-publish",
				Action: &gatetypes.GateAction{
					Type:        gatetypes.GateActionWorkflow,
					WorkflowRef: "workflows/gate-actions/publication.yml",
				},
			},
		},
	}
	candidate.DefaultGateConfigID = "mixed"
	candidate.RepositoryAssignments["https://github.com|repo-42"] = "mixed"
	return candidate
}
