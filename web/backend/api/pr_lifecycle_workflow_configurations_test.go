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

func TestPRLifecycleWorkflowConfigurationsResponsesUseV3ContractAndCanonicalFlow(t *testing.T) {
	_, _, mux := prLifecycleWorkflowConfigurationTestServer(t)
	wantFlow, wantRevision := lifecycleflow.Default()

	request := httptest.NewRequest(http.MethodGet, "http://launcher.local"+prLifecycleWorkflowConfigurationsPath, nil)
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
		"workflow-configurations", "default-workflow-configuration", "nudge",
		"scope", "gate-catalog", "flow", "flow-revision",
		"catalog-revision", "config-revision", "effects",
	} {
		if _, exists := raw[key]; !exists {
			t.Fatalf("GET response omits %q: %s", key, recorder.Body.String())
		}
	}
	if _, exists := raw["repository-assignments"]; exists {
		t.Fatalf("workflow response leaks repository assignments: %s", recorder.Body.String())
	}
	for _, legacy := range []string{"gate_profiles", "gate-profiles", "default_gate_profile_id"} {
		if _, exists := raw[legacy]; exists {
			t.Fatalf("GET response contains retired key %q", legacy)
		}
	}
	current := decodePRLifecycleWorkflowConfigurationsResponse(t, recorder.Body.Bytes())
	if current.FlowRevision != wantRevision || !reflect.DeepEqual(current.Flow, wantFlow) {
		t.Fatalf("GET flow revision = %q flow = %#v", current.FlowRevision, current.Flow)
	}
	if current.Effects.GatewayEffect != "applied" || current.Effects.DeferredPolicyEffect != "applied" {
		t.Fatalf("initial effects = %#v", current.Effects)
	}
	builtin := current.WorkflowConfigurations[config.DefaultPRLifecycleWorkflowConfigurationID]
	if builtin.Name != config.DefaultPRLifecycleWorkflowConfigurationName || len(builtin.Bindings) != 0 {
		t.Fatalf("built-in workflow configuration = %#v", builtin)
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

func TestPRLifecycleWorkflowConfigurationsPutSavesValidAtomicOverrides(t *testing.T) {
	configPath, _, mux := prLifecycleWorkflowConfigurationTestServer(t)
	current := getPRLifecycleWorkflowConfigurationsForTest(t, mux)
	candidate := mixedPRLifecycleWorkflowConfigurationCandidate()
	request := putPRLifecycleWorkflowConfigurationsForTest(t, mux, current.ConfigRevision, candidate, nil)
	if request.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body=%s", request.Code, request.Body.String())
	}
	response := decodePRLifecycleWorkflowConfigurationsResponse(t, request.Body.Bytes())
	if response.ConfigRevision == current.ConfigRevision || response.CatalogRevision == "" ||
		response.Effects.GatewayEffect != "restart-required" {
		t.Fatalf("PUT response = %#v", response)
	}
	reloaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.PRLifecycle.DefaultWorkflowConfigurationID != "mixed" ||
		len(reloaded.PRLifecycle.RepositoryAssignments) != 0 ||
		len(reloaded.PRLifecycle.WorkflowConfigurations["mixed"].Bindings) != 4 {
		t.Fatalf("reloaded lifecycle = %#v", reloaded.PRLifecycle)
	}
	if got := reloaded.PRLifecycle.WorkflowConfigurations["mixed"].Bindings[1].Action; got == nil ||
		got.Type != gatetypes.GateActionAI || got.AgentID != "main" {
		t.Fatalf("reloaded AI override = %#v", got)
	}
}

func TestPRLifecycleWorkflowConfigurationsGatewayEffectTracksExactSavedGeneration(t *testing.T) {
	configPath, handler, mux := prLifecycleWorkflowConfigurationTestServer(t)
	initial := getPRLifecycleWorkflowConfigurationsForTest(t, mux)
	candidate := mixedPRLifecycleWorkflowConfigurationCandidate()
	put := putPRLifecycleWorkflowConfigurationsForTest(t, mux, initial.ConfigRevision, candidate, nil)
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body=%s", put.Code, put.Body.String())
	}
	if got := getPRLifecycleWorkflowConfigurationsForTest(t, mux).Effects.GatewayEffect; got != "restart-required" {
		t.Fatalf("saved gateway effect = %q", got)
	}

	handler.markPRLifecycleGatewayApplied(config.DefaultPRLifecycleConfig())
	if got := getPRLifecycleWorkflowConfigurationsForTest(t, mux).Effects.GatewayEffect; got != "restart-required" {
		t.Fatalf("mismatched gateway effect = %q", got)
	}
	handler.markPRLifecycleGatewayApplied(candidate)
	if got := getPRLifecycleWorkflowConfigurationsForTest(t, mux).Effects.GatewayEffect; got != "applied" {
		t.Fatalf("matching gateway effect = %q", got)
	}

	restartedHandler := NewHandler(configPath)
	restartedMux := http.NewServeMux()
	restartedHandler.registerPRLifecycleWorkflowConfigurationRoutes(restartedMux)
	if got := getPRLifecycleWorkflowConfigurationsForTest(t, restartedMux).Effects.GatewayEffect; got != "applied" {
		t.Fatalf("new handler gateway effect = %q", got)
	}
}

func TestPRLifecycleWorkflowConfigurationsDeferredEffectTracksOnlyActivePolicyRouting(t *testing.T) {
	_, handler, mux := prLifecycleWorkflowConfigurationTestServer(t)
	active := config.DefaultPRLifecycleConfig()
	handler.markPRLifecycleGatewayApplied(active)
	initial := getPRLifecycleWorkflowConfigurationsForTest(t, mux)

	unrelated := active
	unrelated.Nudge.ReviewMinimumAdditional++
	put := putPRLifecycleWorkflowConfigurationsForTest(t, mux, initial.ConfigRevision, unrelated, nil)
	if put.Code != http.StatusOK {
		t.Fatalf("unrelated PUT status = %d body=%s", put.Code, put.Body.String())
	}
	saved := decodePRLifecycleWorkflowConfigurationsResponse(t, put.Body.Bytes())
	if saved.Effects.GatewayEffect != "restart-required" || saved.Effects.DeferredPolicyEffect != "applied" {
		t.Fatalf("unrelated effects = %#v", saved.Effects)
	}

	handler.markPRLifecycleGatewayApplied(unrelated)
	current := getPRLifecycleWorkflowConfigurationsForTest(t, mux)
	changed := unrelated
	defaultConfig := changed.WorkflowConfigurations[config.DefaultPRLifecycleWorkflowConfigurationID]
	defaultConfig.DeferredIssues.Mode = config.PRLifecycleDeferredIssuesOff
	changed.WorkflowConfigurations = map[string]config.PRLifecycleWorkflowConfiguration{
		config.DefaultPRLifecycleWorkflowConfigurationID: defaultConfig,
	}
	put = putPRLifecycleWorkflowConfigurationsForTest(t, mux, current.ConfigRevision, changed, nil)
	if put.Code != http.StatusOK {
		t.Fatalf("deferred PUT status = %d body=%s", put.Code, put.Body.String())
	}
	saved = decodePRLifecycleWorkflowConfigurationsResponse(t, put.Body.Bytes())
	if saved.Effects.DeferredPolicyEffect != "restart-required" {
		t.Fatalf("deferred effects = %#v", saved.Effects)
	}
}

func TestPRLifecycleWorkflowConfigurationsPutRejectsSemanticErrorsBeforePersistence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*config.PRLifecycleConfig)
	}{
		{name: "duplicate binding", mutate: func(c *config.PRLifecycleConfig) {
			mixed := c.WorkflowConfigurations["mixed"]
			mixed.Bindings = append(mixed.Bindings, mixed.Bindings[0])
			c.WorkflowConfigurations["mixed"] = mixed
		}},
		{name: "invalid gate ref", mutate: func(c *config.PRLifecycleConfig) {
			mixed := c.WorkflowConfigurations["mixed"]
			mixed.Bindings[0].GateRef = "charter-decision"
			c.WorkflowConfigurations["mixed"] = mixed
		}},
		{name: "partial action", mutate: func(c *config.PRLifecycleConfig) {
			mixed := c.WorkflowConfigurations["mixed"]
			mixed.Bindings[0].Action = &gatetypes.GateAction{Type: gatetypes.GateActionHuman, Prompt: "not allowed"}
			c.WorkflowConfigurations["mixed"] = mixed
		}},
		{name: "unknown agent", mutate: func(c *config.PRLifecycleConfig) {
			mixed := c.WorkflowConfigurations["mixed"]
			action := *mixed.Bindings[1].Action
			action.AgentID = "missing-reviewer"
			mixed.Bindings[1].Action = &action
			c.WorkflowConfigurations["mixed"] = mixed
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configPath, _, mux := prLifecycleWorkflowConfigurationTestServer(t)
			current := getPRLifecycleWorkflowConfigurationsForTest(t, mux)
			candidate := mixedPRLifecycleWorkflowConfigurationCandidate()
			test.mutate(&candidate)
			response := putPRLifecycleWorkflowConfigurationsForTest(t, mux, current.ConfigRevision, candidate, nil)
			if response.Code != http.StatusUnprocessableEntity ||
				!strings.Contains(response.Body.String(), "invalid_workflow_configurations") {
				t.Fatalf("PUT status = %d body=%s", response.Code, response.Body.String())
			}
			reloaded, err := config.LoadConfig(configPath)
			if err != nil {
				t.Fatal(err)
			}
			if _, exists := reloaded.PRLifecycle.Effective().WorkflowConfigurations["mixed"]; exists {
				t.Fatal("invalid workflow configuration was persisted")
			}
		})
	}
}

func TestPRLifecycleWorkflowConfigurationsPutRejectsStaleRevision(t *testing.T) {
	configPath, _, mux := prLifecycleWorkflowConfigurationTestServer(t)
	response := putPRLifecycleWorkflowConfigurationsForTest(
		t, mux, "sha256:stale-config-revision", mixedPRLifecycleWorkflowConfigurationCandidate(), nil,
	)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "config_revision_mismatch") {
		t.Fatalf("PUT status = %d body=%s", response.Code, response.Body.String())
	}
	reloaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := reloaded.PRLifecycle.Effective().WorkflowConfigurations["mixed"]; exists {
		t.Fatal("stale workflow configuration update was persisted")
	}
}

func TestPRLifecycleWorkflowConfigurationsPutRejectsMissingAndEffectfulActionWorkflows(t *testing.T) {
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
			configPath, _, mux := prLifecycleWorkflowConfigurationTestServer(t)
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
			current := getPRLifecycleWorkflowConfigurationsForTest(t, mux)
			candidate := mixedPRLifecycleWorkflowConfigurationCandidate()
			mixed := candidate.WorkflowConfigurations["mixed"]
			mixed.Bindings[3].Action.WorkflowRef = test.ref
			candidate.WorkflowConfigurations["mixed"] = mixed
			response := putPRLifecycleWorkflowConfigurationsForTest(t, mux, current.ConfigRevision, candidate, nil)
			if response.Code != http.StatusUnprocessableEntity ||
				!strings.Contains(response.Body.String(), "invalid_workflow_configurations") {
				t.Fatalf("PUT status = %d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestPRLifecycleWorkflowConfigurationsRequestsAreStrictAndOldRouteIsGone(t *testing.T) {
	_, _, mux := prLifecycleWorkflowConfigurationTestServer(t)
	for _, path := range []string{"/api/pr-lifecycle/gate-profiles", "/api/pr-lifecycle/gate-configs"} {
		oldRequest := httptest.NewRequest(http.MethodGet, "http://launcher.local"+path, nil)
		oldResponse := httptest.NewRecorder()
		mux.ServeHTTP(oldResponse, oldRequest)
		if oldResponse.Code != http.StatusNotFound {
			t.Fatalf("retired route %q status = %d", path, oldResponse.Code)
		}
	}

	current := getPRLifecycleWorkflowConfigurationsForTest(t, mux)
	candidate := mixedPRLifecycleWorkflowConfigurationCandidate()
	validBody := prLifecycleWorkflowConfigurationsPutRequestForTest(current.ConfigRevision, candidate)
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
		{
			name:        "snake case field",
			body:        append(append([]byte(nil), encoded[:len(encoded)-1]...), []byte(`,"request_id":"legacy"}`)...),
			contentType: "application/json",
			fetchSite:   "same-origin",
		},
		{
			name: "retired assignment field",
			body: append(
				append([]byte(nil), encoded[:len(encoded)-1]...),
				[]byte(`,"repository-assignments":{}}`)...),
			contentType: "application/json",
			fetchSite:   "same-origin",
		},
		{
			name:        "unknown field",
			body:        append(append([]byte(nil), encoded[:len(encoded)-1]...), []byte(`,"unknown":true}`)...),
			contentType: "application/json",
			fetchSite:   "same-origin",
		},
		{
			name:        "trailing value",
			body:        append(append([]byte(nil), encoded...), []byte(` {}`)...),
			contentType: "application/json",
			fetchSite:   "same-origin",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(
				http.MethodPut,
				"http://launcher.local"+prLifecycleWorkflowConfigurationsPath,
				bytes.NewReader(test.body),
			)
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

func TestPRLifecycleRepositoryAssignmentRequestsAreStrict(t *testing.T) {
	_, _, mux := prLifecycleWorkflowConfigurationTestServer(t)
	assignments, _ := getPRLifecycleRepositoryAssignmentsForTest(t, mux)
	valid := prLifecycleRepositoryAssignmentsPutRequest{
		ExpectedConfigRevision: assignments.ConfigRevision,
		RequestID:              "request-strict-repository-assignments",
		RepositoryAssignments:  assignments.RepositoryAssignments,
	}
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	for _, extra := range []string{
		`,"workflow-configurations":{}}`,
		`,"default-workflow-configuration":"default"}`,
		`,"unknown":true}`,
	} {
		body := append(append([]byte(nil), encoded[:len(encoded)-1]...), []byte(extra)...)
		request := httptest.NewRequest(
			http.MethodPut,
			"http://launcher.local"+prLifecycleRepositoryAssignmentsPath,
			bytes.NewReader(body),
		)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Sec-Fetch-Site", "same-origin")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("strict assignment status = %d body=%s", response.Code, response.Body.String())
		}
	}
}

func TestPRLifecycleRepositoryAssignmentsUseSafeIndependentProjection(t *testing.T) {
	configPath, handler, mux := prLifecycleWorkflowConfigurationTestServer(t)
	initial := getPRLifecycleWorkflowConfigurationsForTest(t, mux)
	candidate := mixedPRLifecycleWorkflowConfigurationCandidate()
	put := putPRLifecycleWorkflowConfigurationsForTest(t, mux, initial.ConfigRevision, candidate, nil)
	if put.Code != http.StatusOK {
		t.Fatalf("workflow PUT status = %d body=%s", put.Code, put.Body.String())
	}
	workflowResponse := decodePRLifecycleWorkflowConfigurationsResponse(t, put.Body.Bytes())
	handler.markPRLifecycleGatewayApplied(candidate)

	assignments, raw := getPRLifecycleRepositoryAssignmentsForTest(t, mux)
	if assignments.DefaultWorkflowConfigurationID != "mixed" ||
		assignments.WorkflowConfigurations["mixed"].Name != "Mixed" ||
		assignments.WorkflowConfigurations["mixed"].DeferredIssues.Mode != config.PRLifecycleDeferredIssuesAutomatic {
		t.Fatalf("assignment projection = %#v", assignments)
	}
	for _, forbidden := range []string{`"bindings"`, `"action"`, `"prompt"`, `"gate-catalog"`} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatalf("assignment projection leaks %s: %s", forbidden, raw)
		}
	}

	assignmentPut := putPRLifecycleRepositoryAssignmentsForTest(
		t, mux, assignments.ConfigRevision,
		map[string]string{"https://github.com|repo-42": "mixed"}, nil,
	)
	if assignmentPut.Code != http.StatusOK {
		t.Fatalf("assignment PUT status = %d body=%s", assignmentPut.Code, assignmentPut.Body.String())
	}
	savedAssignments := decodePRLifecycleRepositoryAssignmentsResponse(t, assignmentPut.Body.Bytes())
	if savedAssignments.ConfigRevision == assignments.ConfigRevision ||
		savedAssignments.Effects.GatewayEffect != "restart-required" {
		t.Fatalf("saved assignment response = %#v", savedAssignments)
	}
	afterWorkflow := getPRLifecycleWorkflowConfigurationsForTest(t, mux)
	if afterWorkflow.CatalogRevision != workflowResponse.CatalogRevision {
		t.Fatalf(
			"assignment changed public catalog revision: %q != %q",
			afterWorkflow.CatalogRevision,
			workflowResponse.CatalogRevision,
		)
	}
	reloaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.PRLifecycle.RepositoryAssignments["https://github.com|repo-42"] != "mixed" ||
		len(reloaded.PRLifecycle.WorkflowConfigurations["mixed"].Bindings) != 4 ||
		reloaded.PRLifecycle.Nudge != candidate.Nudge || reloaded.PRLifecycle.Scope != candidate.Scope {
		t.Fatalf("assignment PUT failed to preserve workflow configuration: %#v", reloaded.PRLifecycle)
	}
}

func TestPRLifecycleWorkflowConfigurationsCannotDeleteAssignedConfiguration(t *testing.T) {
	configPath, _, mux := prLifecycleWorkflowConfigurationTestServer(t)
	initial := getPRLifecycleWorkflowConfigurationsForTest(t, mux)
	candidate := mixedPRLifecycleWorkflowConfigurationCandidate()
	put := putPRLifecycleWorkflowConfigurationsForTest(t, mux, initial.ConfigRevision, candidate, nil)
	if put.Code != http.StatusOK {
		t.Fatalf("workflow PUT status = %d body=%s", put.Code, put.Body.String())
	}
	assignments, _ := getPRLifecycleRepositoryAssignmentsForTest(t, mux)
	putAssignments := putPRLifecycleRepositoryAssignmentsForTest(
		t, mux, assignments.ConfigRevision,
		map[string]string{"https://github.com|repo-42": "mixed"}, nil,
	)
	if putAssignments.Code != http.StatusOK {
		t.Fatalf("assignment PUT status = %d body=%s", putAssignments.Code, putAssignments.Body.String())
	}
	current := getPRLifecycleWorkflowConfigurationsForTest(t, mux)
	removeMixed := config.DefaultPRLifecycleConfig()
	response := putPRLifecycleWorkflowConfigurationsForTest(t, mux, current.ConfigRevision, removeMixed, nil)
	if response.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(response.Body.String(), "invalid_workflow_configurations") {
		t.Fatalf("delete assigned configuration status = %d body=%s", response.Code, response.Body.String())
	}
	reloaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := reloaded.PRLifecycle.WorkflowConfigurations["mixed"]; !exists ||
		reloaded.PRLifecycle.RepositoryAssignments["https://github.com|repo-42"] != "mixed" {
		t.Fatalf("assigned configuration deletion was partially persisted: %#v", reloaded.PRLifecycle)
	}
}

func TestPRLifecycleRepositoryAssignmentsRejectMissingTargetAndCrossEndpointStaleRevision(t *testing.T) {
	configPath, _, mux := prLifecycleWorkflowConfigurationTestServer(t)
	workflow := getPRLifecycleWorkflowConfigurationsForTest(t, mux)
	assignments, _ := getPRLifecycleRepositoryAssignmentsForTest(t, mux)
	missing := putPRLifecycleRepositoryAssignmentsForTest(
		t, mux, assignments.ConfigRevision,
		map[string]string{"https://github.com|repo-42": "missing"}, nil,
	)
	if missing.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(missing.Body.String(), "invalid_repository_assignments") {
		t.Fatalf("missing assignment target status = %d body=%s", missing.Code, missing.Body.String())
	}

	candidate := mixedPRLifecycleWorkflowConfigurationCandidate()
	workflowPut := putPRLifecycleWorkflowConfigurationsForTest(t, mux, workflow.ConfigRevision, candidate, nil)
	if workflowPut.Code != http.StatusOK {
		t.Fatalf("workflow PUT status = %d body=%s", workflowPut.Code, workflowPut.Body.String())
	}
	staleAssignment := putPRLifecycleRepositoryAssignmentsForTest(
		t, mux, assignments.ConfigRevision,
		map[string]string{"https://github.com|repo-42": "mixed"}, nil,
	)
	if staleAssignment.Code != http.StatusConflict ||
		!strings.Contains(staleAssignment.Body.String(), "config_revision_mismatch") {
		t.Fatalf("stale assignment status = %d body=%s", staleAssignment.Code, staleAssignment.Body.String())
	}
	reloaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.PRLifecycle.RepositoryAssignments) != 0 {
		t.Fatalf("stale assignment was persisted: %#v", reloaded.PRLifecycle.RepositoryAssignments)
	}
}

func TestPRLifecycleScopedNoOpPutsDoNotRequireRestart(t *testing.T) {
	_, _, mux := prLifecycleWorkflowConfigurationTestServer(t)
	workflow := getPRLifecycleWorkflowConfigurationsForTest(t, mux)
	candidate := config.PRLifecycleConfig{
		WorkflowConfigurations:         workflow.WorkflowConfigurations,
		DefaultWorkflowConfigurationID: workflow.DefaultWorkflowConfigurationID,
		Nudge:                          workflow.Nudge, Scope: workflow.Scope,
	}
	putWorkflow := putPRLifecycleWorkflowConfigurationsForTest(t, mux, workflow.ConfigRevision, candidate, nil)
	if putWorkflow.Code != http.StatusOK {
		t.Fatalf("workflow no-op status = %d body=%s", putWorkflow.Code, putWorkflow.Body.String())
	}
	workflowNoOp := decodePRLifecycleWorkflowConfigurationsResponse(t, putWorkflow.Body.Bytes())
	if workflowNoOp.ConfigRevision != workflow.ConfigRevision || workflowNoOp.Effects.GatewayEffect != "applied" {
		t.Fatalf("workflow no-op response = %#v", workflowNoOp)
	}

	assignments, _ := getPRLifecycleRepositoryAssignmentsForTest(t, mux)
	putAssignments := putPRLifecycleRepositoryAssignmentsForTest(
		t, mux, assignments.ConfigRevision, assignments.RepositoryAssignments, nil,
	)
	if putAssignments.Code != http.StatusOK {
		t.Fatalf("assignment no-op status = %d body=%s", putAssignments.Code, putAssignments.Body.String())
	}
	assignmentNoOp := decodePRLifecycleRepositoryAssignmentsResponse(t, putAssignments.Body.Bytes())
	if assignmentNoOp.ConfigRevision != assignments.ConfigRevision ||
		assignmentNoOp.Effects.GatewayEffect != "applied" {
		t.Fatalf("assignment no-op response = %#v", assignmentNoOp)
	}
}

func TestPRLifecycleBindingReorderIsACompleteNoOp(t *testing.T) {
	_, handler, mux := prLifecycleWorkflowConfigurationTestServer(t)
	initial := getPRLifecycleWorkflowConfigurationsForTest(t, mux)
	candidate := mixedPRLifecycleWorkflowConfigurationCandidate()
	put := putPRLifecycleWorkflowConfigurationsForTest(t, mux, initial.ConfigRevision, candidate, nil)
	if put.Code != http.StatusOK {
		t.Fatalf("initial PUT status = %d body=%s", put.Code, put.Body.String())
	}
	saved := decodePRLifecycleWorkflowConfigurationsResponse(t, put.Body.Bytes())
	handler.markPRLifecycleGatewayApplied(candidate)

	reordered := candidate
	configuration := reordered.WorkflowConfigurations["mixed"]
	for left, right := 0, len(configuration.Bindings)-1; left < right; left, right = left+1, right-1 {
		configuration.Bindings[left], configuration.Bindings[right] = configuration.Bindings[right], configuration.Bindings[left]
	}
	reordered.WorkflowConfigurations = map[string]config.PRLifecycleWorkflowConfiguration{
		config.DefaultPRLifecycleWorkflowConfigurationID: candidate.WorkflowConfigurations[config.DefaultPRLifecycleWorkflowConfigurationID],
		"mixed": configuration,
	}
	put = putPRLifecycleWorkflowConfigurationsForTest(t, mux, saved.ConfigRevision, reordered, nil)
	if put.Code != http.StatusOK {
		t.Fatalf("reorder PUT status = %d body=%s", put.Code, put.Body.String())
	}
	result := decodePRLifecycleWorkflowConfigurationsResponse(t, put.Body.Bytes())
	if result.ConfigRevision != saved.ConfigRevision || result.CatalogRevision != saved.CatalogRevision ||
		result.Effects.GatewayEffect != "applied" {
		t.Fatalf("binding reorder changed configuration: %#v", result)
	}
}

func TestPRLifecycleDeferredEffectIgnoresReassignmentBetweenEqualModes(t *testing.T) {
	_, handler, mux := prLifecycleWorkflowConfigurationTestServer(t)
	initial := getPRLifecycleWorkflowConfigurationsForTest(t, mux)
	candidate := config.DefaultPRLifecycleConfig()
	for _, id := range []string{"left", "right"} {
		candidate.WorkflowConfigurations[id] = config.PRLifecycleWorkflowConfiguration{
			Name: strings.ToUpper(id[:1]) + id[1:], Bindings: []config.PRLifecycleGateBinding{},
			DeferredIssues: config.PRLifecycleDeferredIssueConfig{Mode: config.PRLifecycleDeferredIssuesAsk},
		}
	}
	put := putPRLifecycleWorkflowConfigurationsForTest(t, mux, initial.ConfigRevision, candidate, nil)
	if put.Code != http.StatusOK {
		t.Fatalf("workflow PUT status = %d body=%s", put.Code, put.Body.String())
	}
	assignments, _ := getPRLifecycleRepositoryAssignmentsForTest(t, mux)
	left := putPRLifecycleRepositoryAssignmentsForTest(
		t, mux, assignments.ConfigRevision,
		map[string]string{"https://github.com|repo-42": "left"}, nil,
	)
	if left.Code != http.StatusOK {
		t.Fatalf("left assignment status = %d body=%s", left.Code, left.Body.String())
	}
	leftResponse := decodePRLifecycleRepositoryAssignmentsResponse(t, left.Body.Bytes())
	withLeft := candidate
	withLeft.RepositoryAssignments = map[string]string{"https://github.com|repo-42": "left"}
	handler.markPRLifecycleGatewayApplied(withLeft)
	right := putPRLifecycleRepositoryAssignmentsForTest(
		t, mux, leftResponse.ConfigRevision,
		map[string]string{"https://github.com|repo-42": "right"}, nil,
	)
	if right.Code != http.StatusOK {
		t.Fatalf("right assignment status = %d body=%s", right.Code, right.Body.String())
	}
	rightResponse := decodePRLifecycleRepositoryAssignmentsResponse(t, right.Body.Bytes())
	if rightResponse.Effects.GatewayEffect != "restart-required" ||
		rightResponse.Effects.DeferredPolicyEffect != "applied" {
		t.Fatalf("equal-mode reassignment effects = %#v", rightResponse.Effects)
	}
}

func TestPRLifecycleDeferredEffectIgnoresDefaultEquivalentAssignmentAddAndRemove(t *testing.T) {
	_, handler, mux := prLifecycleWorkflowConfigurationTestServer(t)
	active := config.DefaultPRLifecycleConfig()
	handler.markPRLifecycleGatewayApplied(active)
	assignments, _ := getPRLifecycleRepositoryAssignmentsForTest(t, mux)
	added := putPRLifecycleRepositoryAssignmentsForTest(
		t, mux, assignments.ConfigRevision,
		map[string]string{"https://github.com|repo-42": config.DefaultPRLifecycleWorkflowConfigurationID}, nil,
	)
	if added.Code != http.StatusOK {
		t.Fatalf("add default assignment status = %d body=%s", added.Code, added.Body.String())
	}
	addedResponse := decodePRLifecycleRepositoryAssignmentsResponse(t, added.Body.Bytes())
	if addedResponse.Effects.GatewayEffect != "restart-required" ||
		addedResponse.Effects.DeferredPolicyEffect != "applied" {
		t.Fatalf("default-equivalent add effects = %#v", addedResponse.Effects)
	}
	withAssignment := active
	withAssignment.RepositoryAssignments = map[string]string{
		"https://github.com|repo-42": config.DefaultPRLifecycleWorkflowConfigurationID,
	}
	handler.markPRLifecycleGatewayApplied(withAssignment)
	removed := putPRLifecycleRepositoryAssignmentsForTest(
		t, mux, addedResponse.ConfigRevision, map[string]string{}, nil,
	)
	if removed.Code != http.StatusOK {
		t.Fatalf("remove default assignment status = %d body=%s", removed.Code, removed.Body.String())
	}
	removedResponse := decodePRLifecycleRepositoryAssignmentsResponse(t, removed.Body.Bytes())
	if removedResponse.Effects.GatewayEffect != "restart-required" ||
		removedResponse.Effects.DeferredPolicyEffect != "applied" {
		t.Fatalf("default-equivalent remove effects = %#v", removedResponse.Effects)
	}
}

func TestPRLifecycleCanonicalEquivalentAssignmentIsACompleteNoOp(t *testing.T) {
	_, handler, mux := prLifecycleWorkflowConfigurationTestServer(t)
	assignments, _ := getPRLifecycleRepositoryAssignmentsForTest(t, mux)
	first := putPRLifecycleRepositoryAssignmentsForTest(
		t, mux, assignments.ConfigRevision,
		map[string]string{"https://github.com|repo-42": config.DefaultPRLifecycleWorkflowConfigurationID}, nil,
	)
	if first.Code != http.StatusOK {
		t.Fatalf("initial assignment status = %d body=%s", first.Code, first.Body.String())
	}
	firstResponse := decodePRLifecycleRepositoryAssignmentsResponse(t, first.Body.Bytes())
	active := config.DefaultPRLifecycleConfig()
	active.RepositoryAssignments = map[string]string{
		"https://github.com|repo-42": config.DefaultPRLifecycleWorkflowConfigurationID,
	}
	handler.markPRLifecycleGatewayApplied(active)

	equivalent := putPRLifecycleRepositoryAssignmentsForTest(
		t, mux, firstResponse.ConfigRevision,
		map[string]string{"HTTPS://GITHUB.COM///|REPO-42": config.DefaultPRLifecycleWorkflowConfigurationID}, nil,
	)
	if equivalent.Code != http.StatusOK {
		t.Fatalf("equivalent assignment status = %d body=%s", equivalent.Code, equivalent.Body.String())
	}
	result := decodePRLifecycleRepositoryAssignmentsResponse(t, equivalent.Body.Bytes())
	if result.ConfigRevision != firstResponse.ConfigRevision || result.Effects.GatewayEffect != "applied" ||
		result.RepositoryAssignments["https://github.com|repo-42"] != config.DefaultPRLifecycleWorkflowConfigurationID {
		t.Fatalf("canonical equivalent assignment changed persisted config: %#v", result)
	}
}

func TestPRLifecycleScopedPutsRejectNullCollections(t *testing.T) {
	_, _, mux := prLifecycleWorkflowConfigurationTestServer(t)
	workflow := getPRLifecycleWorkflowConfigurationsForTest(t, mux)
	badWorkflow := config.DefaultPRLifecycleConfig()
	defaultConfiguration := badWorkflow.WorkflowConfigurations[config.DefaultPRLifecycleWorkflowConfigurationID]
	defaultConfiguration.Bindings = nil
	badWorkflow.WorkflowConfigurations[config.DefaultPRLifecycleWorkflowConfigurationID] = defaultConfiguration
	response := putPRLifecycleWorkflowConfigurationsForTest(t, mux, workflow.ConfigRevision, badWorkflow, nil)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("null bindings status = %d body=%s", response.Code, response.Body.String())
	}

	assignments, _ := getPRLifecycleRepositoryAssignmentsForTest(t, mux)
	response = putPRLifecycleRepositoryAssignmentsForTest(t, mux, assignments.ConfigRevision, nil, nil)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("null assignments status = %d body=%s", response.Code, response.Body.String())
	}
}

func prLifecycleWorkflowConfigurationTestServer(t *testing.T) (string, *Handler, *http.ServeMux) {
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
	handler.registerPRLifecycleWorkflowConfigurationRoutes(mux)
	return configPath, handler, mux
}

func getPRLifecycleWorkflowConfigurationsForTest(
	t *testing.T,
	mux *http.ServeMux,
) prLifecycleWorkflowConfigurationsResponse {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "http://launcher.local"+prLifecycleWorkflowConfigurationsPath, nil)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%s", response.Code, response.Body.String())
	}
	result := decodePRLifecycleWorkflowConfigurationsResponse(t, response.Body.Bytes())
	if result.ConfigRevision == "" || result.CatalogRevision == "" {
		t.Fatalf("GET response = %#v", result)
	}
	return result
}

func decodePRLifecycleWorkflowConfigurationsResponse(
	t *testing.T,
	body []byte,
) prLifecycleWorkflowConfigurationsResponse {
	t.Helper()
	var result prLifecycleWorkflowConfigurationsResponse
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func getPRLifecycleRepositoryAssignmentsForTest(
	t *testing.T,
	mux *http.ServeMux,
) (prLifecycleRepositoryAssignmentsResponse, []byte) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "http://launcher.local"+prLifecycleRepositoryAssignmentsPath, nil)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("assignment GET status = %d body=%s", response.Code, response.Body.String())
	}
	return decodePRLifecycleRepositoryAssignmentsResponse(t, response.Body.Bytes()), response.Body.Bytes()
}

func decodePRLifecycleRepositoryAssignmentsResponse(
	t *testing.T,
	body []byte,
) prLifecycleRepositoryAssignmentsResponse {
	t.Helper()
	var result prLifecycleRepositoryAssignmentsResponse
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	if result.ConfigRevision == "" || len(result.WorkflowConfigurations) == 0 {
		t.Fatalf("assignment response = %#v", result)
	}
	return result
}

func putPRLifecycleRepositoryAssignmentsForTest(
	t *testing.T,
	mux *http.ServeMux,
	revision string,
	assignments map[string]string,
	mutate func(*http.Request),
) *httptest.ResponseRecorder {
	t.Helper()
	body := prLifecycleRepositoryAssignmentsPutRequest{
		ExpectedConfigRevision: revision,
		RequestID:              "request-save-repository-assignments",
		RepositoryAssignments:  assignments,
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodPut,
		"http://launcher.local"+prLifecycleRepositoryAssignmentsPath,
		bytes.NewReader(encoded),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	if mutate != nil {
		mutate(request)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func putPRLifecycleWorkflowConfigurationsForTest(
	t *testing.T,
	mux *http.ServeMux,
	revision string,
	candidate config.PRLifecycleConfig,
	mutate func(*http.Request),
) *httptest.ResponseRecorder {
	t.Helper()
	body := prLifecycleWorkflowConfigurationsPutRequestForTest(revision, candidate)
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodPut,
		"http://launcher.local"+prLifecycleWorkflowConfigurationsPath,
		bytes.NewReader(encoded),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	if mutate != nil {
		mutate(request)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func prLifecycleWorkflowConfigurationsPutRequestForTest(
	revision string,
	candidate config.PRLifecycleConfig,
) prLifecycleWorkflowConfigurationsPutRequest {
	return prLifecycleWorkflowConfigurationsPutRequest{
		ExpectedConfigRevision:         revision,
		RequestID:                      "request-save-mixed-workflow-configuration",
		WorkflowConfigurations:         candidate.WorkflowConfigurations,
		DefaultWorkflowConfigurationID: candidate.DefaultWorkflowConfigurationID,
		Nudge:                          candidate.Nudge,
		Scope:                          candidate.Scope,
	}
}

func mixedPRLifecycleWorkflowConfigurationCandidate() config.PRLifecycleConfig {
	candidate := config.DefaultPRLifecycleConfig()
	candidate.WorkflowConfigurations["mixed"] = config.PRLifecycleWorkflowConfiguration{
		Name:           "Mixed",
		DeferredIssues: config.PRLifecycleDeferredIssueConfig{Mode: config.PRLifecycleDeferredIssuesAutomatic},
		Bindings: []config.PRLifecycleGateBinding{
			{
				WorkflowRef: "workflows/pr-lifecycle.yml", GateRef: "gates.charter-confirm",
				Action: &gatetypes.GateAction{Type: gatetypes.GateActionHuman},
			},
			{
				WorkflowRef: "workflows/pr-lifecycle.yml", GateRef: "gates.review-complete",
				Action: &gatetypes.GateAction{
					Type:    gatetypes.GateActionAI,
					AgentID: "main",
					Prompt:  "Review the evidence and complete the gate fields.",
					Session: "ephemeral",
					History: "none",
					Cache:   "none",
					Tools:   "none",
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
	candidate.DefaultWorkflowConfigurationID = "mixed"
	return candidate
}
