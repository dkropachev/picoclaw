package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/prworkspace/lifecycleflow"
	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

func TestPRLifecycleGateProfilesResponsesIncludeCanonicalFlow(t *testing.T) {
	_, _, mux := prLifecycleGateProfileTestServer(t)
	wantFlow, wantRevision := lifecycleflow.Default()

	current := getPRLifecycleGateProfilesForTest(t, mux)
	if current.FlowRevision != wantRevision || !reflect.DeepEqual(current.Flow, wantFlow) {
		t.Fatalf("GET flow revision = %q flow = %#v", current.FlowRevision, current.Flow)
	}
	if current.Flow.Schema != lifecycleflow.SchemaV1 || len(current.Flow.Flows) != 2 ||
		current.Flow.Flows[0].ID != "review" || current.Flow.Flows[1].ID != "implementation" {
		t.Fatalf("GET flow envelope = %#v", current.Flow)
	}

	response := putPRLifecycleGateProfilesForTest(
		t,
		mux,
		current.ConfigRevision,
		mixedPRLifecycleCandidate(),
		nil,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body=%s", response.Code, response.Body.String())
	}
	var saved prLifecycleGateProfilesResponse
	if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.FlowRevision != wantRevision || !reflect.DeepEqual(saved.Flow, wantFlow) {
		t.Fatalf("PUT flow revision = %q flow = %#v", saved.FlowRevision, saved.Flow)
	}
}

func TestDefaultPRLifecycleProfileMatchesFlowDecisionCatalog(t *testing.T) {
	profile := config.DefaultPRLifecycleConfig().GateProfiles[config.DefaultPRLifecycleGateProfileID]
	got := make([]string, 0, len(profile.Workflows))
	for decisionPoint := range profile.Workflows {
		got = append(got, decisionPoint)
	}
	sort.Strings(got)
	if want := lifecycleflow.KnownDecisionPoints(); !reflect.DeepEqual(got, want) {
		t.Fatalf("default profile decision points = %v, want %v", got, want)
	}
}

func TestPRLifecycleGateProfilesPutSavesValidMixedProfile(t *testing.T) {
	configPath, _, mux := prLifecycleGateProfileTestServer(t)
	current := getPRLifecycleGateProfilesForTest(t, mux)
	if current.Effects.GatewayEffect != "applied" {
		t.Fatalf("initial gateway effect = %q, want applied", current.Effects.GatewayEffect)
	}
	candidate := mixedPRLifecycleCandidate()
	request := putPRLifecycleGateProfilesForTest(t, mux, current.ConfigRevision, candidate, nil)
	if request.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body=%s", request.Code, request.Body.String())
	}
	var response prLifecycleGateProfilesResponse
	if err := json.Unmarshal(request.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ConfigRevision == current.ConfigRevision || response.CatalogRevision == "" ||
		response.Effects.GatewayEffect != "restart_required" {
		t.Fatalf("PUT response = %#v", response)
	}
	reloaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.PRLifecycle.DefaultGateProfileID != "mixed" ||
		reloaded.PRLifecycle.RepositoryAssignments["https://github.com|repo-42"] != "mixed" ||
		len(reloaded.PRLifecycle.GateProfiles["mixed"].Workflows["pr.charter.confirm"].Stages) != 5 {
		t.Fatalf("reloaded lifecycle = %#v", reloaded.PRLifecycle)
	}
}

func TestPRLifecycleGateProfilesGatewayEffectTracksHandlerLifecycle(t *testing.T) {
	configPath, handler, mux := prLifecycleGateProfileTestServer(t)
	initial := getPRLifecycleGateProfilesForTest(t, mux)
	if initial.Effects.GatewayEffect != "applied" {
		t.Fatalf("initial gateway effect = %q, want applied", initial.Effects.GatewayEffect)
	}

	candidate := mixedPRLifecycleCandidate()
	put := putPRLifecycleGateProfilesForTest(t, mux, initial.ConfigRevision, candidate, nil)
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body=%s", put.Code, put.Body.String())
	}
	queued := getPRLifecycleGateProfilesForTest(t, mux)
	if queued.Effects.GatewayEffect != "restart_required" {
		t.Fatalf("saved gateway effect = %q, want restart_required", queued.Effects.GatewayEffect)
	}

	// A gateway that loaded an older catalog must not acknowledge a newer save.
	handler.markPRLifecycleGatewayApplied(config.DefaultPRLifecycleConfig())
	stillQueued := getPRLifecycleGateProfilesForTest(t, mux)
	if stillQueued.Effects.GatewayEffect != "restart_required" {
		t.Fatalf("mismatched gateway effect = %q, want restart_required", stillQueued.Effects.GatewayEffect)
	}

	// Starting or restarting with the saved catalog clears the running
	// launcher's pending effect.
	handler.markPRLifecycleGatewayApplied(candidate)
	applied := getPRLifecycleGateProfilesForTest(t, mux)
	if applied.Effects.GatewayEffect != "applied" {
		t.Fatalf("started gateway effect = %q, want applied", applied.Effects.GatewayEffect)
	}

	// Effect state is intentionally process-local: a newly started launcher
	// begins from the config it just loaded and must not resurrect an old banner.
	restartedHandler := NewHandler(configPath)
	restartedMux := http.NewServeMux()
	restartedHandler.registerPRLifecycleGateProfileRoutes(restartedMux)
	afterLauncherRestart := getPRLifecycleGateProfilesForTest(t, restartedMux)
	if afterLauncherRestart.Effects.GatewayEffect != "applied" {
		t.Fatalf(
			"new handler gateway effect = %q, want applied",
			afterLauncherRestart.Effects.GatewayEffect,
		)
	}
}

func TestPRLifecycleGateProfilesGatewayEffectClearsOnlyAfterSuccessfulStart(t *testing.T) {
	resetGatewayTestState(t)
	_, handler, mux := prLifecycleGateProfileTestServer(t)
	initial := getPRLifecycleGateProfilesForTest(t, mux)
	candidate := mixedPRLifecycleCandidate()
	put := putPRLifecycleGateProfilesForTest(t, mux, initial.ConfigRevision, candidate, nil)
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body=%s", put.Code, put.Body.String())
	}

	gatewayExecCommand = func(_ string, _ ...string) *exec.Cmd {
		return exec.Command(filepath.Join(t.TempDir(), "missing-gateway"))
	}
	gateway.mu.Lock()
	_, startErr := handler.startGatewayLocked("starting", 0)
	gateway.mu.Unlock()
	if startErr == nil {
		t.Fatal("startGatewayLocked() error = nil, want failed process start")
	}
	if effect := getPRLifecycleGateProfilesForTest(t, mux).Effects.GatewayEffect; effect != "restart_required" {
		t.Fatalf("failed start gateway effect = %q, want restart_required", effect)
	}

	// A live child plus a successful health probe exercises the same readiness
	// confirmation used by API start and restart operations.
	var child *exec.Cmd
	gatewayExecCommand = func(_ string, _ ...string) *exec.Cmd {
		if runtime.GOOS == "windows" {
			child = exec.Command("powershell", "-NoProfile", "-Command", "Start-Sleep -Seconds 30")
		} else {
			child = exec.Command("sleep", "30")
		}
		return child
	}
	gatewayHealthGet = func(string, time.Duration) (*http.Response, error) {
		return mockGatewayHealthResponse(http.StatusOK, 1), nil
	}
	gateway.mu.Lock()
	_, startErr = handler.startGatewayLocked("starting", 0)
	gateway.mu.Unlock()
	if startErr != nil {
		t.Fatalf("startGatewayLocked() error = %v", startErr)
	}
	t.Cleanup(func() {
		if child != nil && child.Process != nil {
			_ = child.Process.Kill()
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for {
		effect := getPRLifecycleGateProfilesForTest(t, mux).Effects.GatewayEffect
		if effect == "applied" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("healthy start gateway effect = %q, want applied", effect)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestPRLifecycleGateProfilesPutRejectsSemanticErrorsBeforePersistence(t *testing.T) {
	configPath, _, mux := prLifecycleGateProfileTestServer(t)
	current := getPRLifecycleGateProfilesForTest(t, mux)
	candidate := mixedPRLifecycleCandidate()
	profile := candidate.GateProfiles["mixed"]
	workflow := profile.Workflows["pr.charter.confirm"]
	workflow.Stages = append([]gatetypes.GateStageSpec(nil), workflow.Stages...)
	workflow.Stages[1].When = "inputs.gate_subject.charter.type =="
	profile.Workflows["pr.charter.confirm"] = workflow
	candidate.GateProfiles["mixed"] = profile

	response := putPRLifecycleGateProfilesForTest(t, mux, current.ConfigRevision, candidate, nil)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "invalid_gate_profiles") {
		t.Fatalf("PUT status = %d body=%s", response.Code, response.Body.String())
	}
	reloaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := reloaded.PRLifecycle.Effective().GateProfiles["mixed"]; exists {
		t.Fatal("semantically invalid mixed profile was persisted")
	}
}

func TestPRLifecycleGateProfilesPutRejectsUnknownAgentsBeforePersistence(t *testing.T) {
	configPath, _, mux := prLifecycleGateProfileTestServer(t)
	current := getPRLifecycleGateProfilesForTest(t, mux)
	candidate := mixedPRLifecycleCandidate()
	profile := candidate.GateProfiles["mixed"]
	workflow := profile.Workflows["pr.charter.confirm"]
	workflow.Stages = append([]gatetypes.GateStageSpec(nil), workflow.Stages...)
	workflow.Stages[2].AgentID = "missing-reviewer"
	profile.Workflows["pr.charter.confirm"] = workflow
	candidate.GateProfiles["mixed"] = profile

	response := putPRLifecycleGateProfilesForTest(t, mux, current.ConfigRevision, candidate, nil)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "invalid_gate_profiles") {
		t.Fatalf("PUT status = %d body=%s", response.Code, response.Body.String())
	}
	reloaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := reloaded.PRLifecycle.Effective().GateProfiles["mixed"]; exists {
		t.Fatal("profile with an unknown agent was persisted")
	}
}

func TestPRLifecycleGateProfilesPutRejectsDecisionPointOutsideFlow(t *testing.T) {
	configPath, _, mux := prLifecycleGateProfileTestServer(t)
	current := getPRLifecycleGateProfilesForTest(t, mux)
	candidate := config.DefaultPRLifecycleConfig()
	profile := candidate.GateProfiles[config.DefaultPRLifecycleGateProfileID]
	profile.Workflows["pr.custom.undeclared"] = gatetypes.GateWorkflowSpec{
		ID:            "custom-undeclared",
		Name:          "Undeclared decision",
		Purpose:       gatetypes.GatePurposeAuthorization,
		DecisionPoint: "pr.custom.undeclared",
		Stages: []gatetypes.GateStageSpec{{
			ID: "automatic", Kind: gatetypes.GateZero,
		}},
	}
	candidate.GateProfiles[config.DefaultPRLifecycleGateProfileID] = profile
	if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), "unknown decision point") {
		t.Fatalf("core lifecycle validation error = %v", err)
	}

	response := putPRLifecycleGateProfilesForTest(t, mux, current.ConfigRevision, candidate, nil)
	if response.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(response.Body.String(), "invalid_gate_profiles") {
		t.Fatalf("PUT status = %d body=%s", response.Code, response.Body.String())
	}
	reloaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := reloaded.PRLifecycle.Effective().GateProfiles[config.DefaultPRLifecycleGateProfileID].Workflows["pr.custom.undeclared"]; exists {
		t.Fatal("workflow decision point outside the lifecycle flow was persisted")
	}
}

func TestPRLifecycleGateProfilesPutRejectsStaleRevision(t *testing.T) {
	configPath, _, mux := prLifecycleGateProfileTestServer(t)
	response := putPRLifecycleGateProfilesForTest(
		t, mux, "sha256:stale-config-revision", mixedPRLifecycleCandidate(), nil,
	)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "config_revision_mismatch") {
		t.Fatalf("PUT status = %d body=%s", response.Code, response.Body.String())
	}
	reloaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := reloaded.PRLifecycle.Effective().GateProfiles["mixed"]; exists {
		t.Fatal("stale profile update was persisted")
	}
}

func TestPRLifecycleGateProfilesRequestsAreStrict(t *testing.T) {
	_, _, mux := prLifecycleGateProfileTestServer(t)
	current := getPRLifecycleGateProfilesForTest(t, mux)
	candidate := mixedPRLifecycleCandidate()
	validBody := prLifecycleGateProfilesPutRequestForTest(current.ConfigRevision, candidate)
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
		{name: "unknown field", body: append(append([]byte(nil), encoded[:len(encoded)-1]...), []byte(`,"unknown":true}`)...), contentType: "application/json", fetchSite: "same-origin"},
		{name: "trailing value", body: append(append([]byte(nil), encoded...), []byte(` {}`)...), contentType: "application/json", fetchSite: "same-origin"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPut, "http://launcher.local"+prLifecycleGateProfilesPath, bytes.NewReader(test.body))
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

func prLifecycleGateProfileTestServer(t *testing.T) (string, *Handler, *http.ServeMux) {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.PRLifecycle = config.DefaultPRLifecycleConfig()
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(configPath)
	mux := http.NewServeMux()
	handler.registerPRLifecycleGateProfileRoutes(mux)
	return configPath, handler, mux
}

func getPRLifecycleGateProfilesForTest(t *testing.T, mux *http.ServeMux) prLifecycleGateProfilesResponse {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "http://launcher.local"+prLifecycleGateProfilesPath, nil)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%s", response.Code, response.Body.String())
	}
	var result prLifecycleGateProfilesResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ConfigRevision == "" || result.CatalogRevision == "" {
		t.Fatalf("GET response = %#v", result)
	}
	return result
}

func putPRLifecycleGateProfilesForTest(
	t *testing.T,
	mux *http.ServeMux,
	revision string,
	candidate config.PRLifecycleConfig,
	mutate func(*http.Request),
) *httptest.ResponseRecorder {
	t.Helper()
	body := prLifecycleGateProfilesPutRequestForTest(revision, candidate)
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, "http://launcher.local"+prLifecycleGateProfilesPath, bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	if mutate != nil {
		mutate(request)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func prLifecycleGateProfilesPutRequestForTest(
	revision string,
	candidate config.PRLifecycleConfig,
) prLifecycleGateProfilesPutRequest {
	return prLifecycleGateProfilesPutRequest{
		ExpectedConfigRevision: revision,
		RequestID:              "request-save-mixed-gate-profile",
		GateProfiles:           candidate.GateProfiles,
		DefaultGateProfileID:   candidate.DefaultGateProfileID,
		RepositoryAssignments:  candidate.RepositoryAssignments,
		Nudge:                  candidate.Nudge,
		Scope:                  candidate.Scope,
		DeferredIssues:         candidate.DeferredIssues,
	}
}

func mixedPRLifecycleCandidate() config.PRLifecycleConfig {
	candidate := config.DefaultPRLifecycleConfig()
	candidate.GateProfiles["mixed"] = config.PRLifecycleGateProfile{
		Name: "Mixed",
		Workflows: map[string]gatetypes.GateWorkflowSpec{
			"pr.charter.confirm": {
				ID: "mixed-charter", Name: "Mixed charter", Purpose: gatetypes.GatePurposeAuthorization,
				DecisionPoint: "pr.charter.confirm",
				Stages: []gatetypes.GateStageSpec{
					{ID: "zero", Kind: gatetypes.GateZero},
					{ID: "type", Kind: gatetypes.GateDeterministic, Title: "Type selected", When: "inputs.gate_subject.charter.type != ''"},
					{ID: "isolated", Kind: gatetypes.GateAIIsolatedContext, Title: "Isolated", AgentID: "main", Criteria: "Check the charter."},
					{ID: "working", Kind: gatetypes.GateAIWorkingContext, Title: "Working", AgentID: "main", Criteria: "Check workspace guidance."},
					{ID: "human", Kind: gatetypes.GateHuman, Title: "Approve", Questions: []any{"Approve?"}},
				},
			},
		},
	}
	candidate.DefaultGateProfileID = "mixed"
	candidate.RepositoryAssignments["https://github.com|repo-42"] = "mixed"
	return candidate
}
