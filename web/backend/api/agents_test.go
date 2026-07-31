package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/routing"
)

type agentAPITestHarness struct {
	configPath string
	handler    *Handler
	mux        *http.ServeMux
}

func newAgentAPITestHarness(
	t *testing.T,
	configure func(*config.Config),
) *agentAPITestHarness {
	t.Helper()

	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", home, err)
	}
	t.Setenv(config.EnvHome, home)
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = filepath.Join(root, "workspace")
	if configure != nil {
		configure(cfg)
	}
	configPath := filepath.Join(root, "config.json")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	handler := NewHandler(configPath)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	return &agentAPITestHarness{
		configPath: configPath,
		handler:    handler,
		mux:        mux,
	}
}

func (h *agentAPITestHarness) request(
	t *testing.T,
	method string,
	path string,
	payload any,
) *httptest.ResponseRecorder {
	t.Helper()
	var body string
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("json.Marshal() error = %v", err)
		}
		body = string(encoded)
	}
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	h.mux.ServeHTTP(recorder, request)
	return recorder
}

func decodeAgentCollection(
	t *testing.T,
	recorder *httptest.ResponseRecorder,
) agentsCollectionResponse {
	t.Helper()
	if recorder.Code < 200 || recorder.Code >= 300 {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var response agentsCollectionResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	return response
}

func TestAgentsAPIProjectsImplicitMainWithoutMutatingConfig(t *testing.T) {
	resetGatewayTestState(t)
	harness := newAgentAPITestHarness(t, nil)
	before, err := os.ReadFile(harness.configPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	pidPath := filepath.Join(globalConfigDir(), ".picoclaw.pid")
	pidSentinel := []byte(`{"invalid":"stale pid metadata must remain untouched"}`)
	if err = os.WriteFile(pidPath, pidSentinel, 0o600); err != nil {
		t.Fatalf("WriteFile(pid sentinel) error = %v", err)
	}

	response := decodeAgentCollection(
		t,
		harness.request(t, http.MethodGet, "/api/agents", nil),
	)
	if len(response.Agents) != 1 {
		t.Fatalf("agents = %#v, want one implicit agent", response.Agents)
	}
	main := response.Agents[0]
	if main.ID != routing.DefaultAgentID || !main.Implicit || !main.IsDefault ||
		main.DefaultConfigured || response.DefaultAgentID != routing.DefaultAgentID {
		t.Fatalf("implicit main projection = %#v, response=%#v", main, response)
	}
	if response.ConfigRevision == "" ||
		response.Effects.LauncherEffect != "applied" ||
		response.Effects.CatalogEffect != "applied" ||
		response.Effects.GatewayEffect != "applied" {
		t.Fatalf("metadata = %#v", response)
	}

	itemRecorder := harness.request(t, http.MethodGet, "/api/agents/main", nil)
	if itemRecorder.Code != http.StatusOK {
		t.Fatalf("item status = %d, body=%s", itemRecorder.Code, itemRecorder.Body.String())
	}
	var item agentItemResponse
	if err = json.Unmarshal(itemRecorder.Body.Bytes(), &item); err != nil {
		t.Fatalf("item json.Unmarshal() error = %v", err)
	}
	if item.Agent.ID != routing.DefaultAgentID || !item.Agent.Implicit ||
		item.ConfigRevision != response.ConfigRevision {
		t.Fatalf("item = %#v", item)
	}

	after, err := os.ReadFile(harness.configPath)
	if err != nil {
		t.Fatalf("ReadFile(after) error = %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("read-only agent projection changed the config file")
	}
	afterPID, err := os.ReadFile(pidPath)
	if err != nil || string(afterPID) != string(pidSentinel) {
		t.Fatalf(
			"read-only agent projection changed stale PID metadata: data=%q err=%v",
			afterPID,
			err,
		)
	}
}

func TestAgentsResponseEffectsUseCapturedConfigGeneration(t *testing.T) {
	resetGatewayTestState(t)
	harness := newAgentAPITestHarness(t, nil)
	captured, capturedRevision, err := config.LoadCurrentConfigSnapshot(
		harness.configPath,
	)
	if err != nil {
		t.Fatalf("LoadCurrentConfigSnapshot() error = %v", err)
	}
	current, currentRevision, err := config.LoadConfigForUpdateSnapshot(
		harness.configPath,
	)
	if err != nil {
		t.Fatalf("LoadConfigForUpdateSnapshot() error = %v", err)
	}
	current.Agents.List = []config.AgentConfig{{
		ID:      "main",
		Default: true,
		Name:    "Concurrent generation",
	}}
	currentRevision, err = config.SaveConfigIfRevision(
		harness.configPath,
		current,
		currentRevision,
	)
	if err != nil {
		t.Fatalf("SaveConfigIfRevision() error = %v", err)
	}

	process := startLongRunningProcess(t)
	t.Cleanup(func() {
		if process.ProcessState == nil {
			_ = process.Process.Kill()
			_, _ = process.Process.Wait()
		}
	})
	gateway.mu.Lock()
	gateway.cmd = process
	gateway.bootConfigSignature = computeConfigSignature(captured)
	setGatewayRuntimeStatusLocked("running")
	gateway.mu.Unlock()

	capturedResponse := harness.handler.buildAgentsCollectionResponse(
		captured,
		capturedRevision,
	)
	if capturedResponse.Effects.GatewayEffect != "applied" {
		t.Fatalf(
			"captured generation gateway effect = %q, want applied",
			capturedResponse.Effects.GatewayEffect,
		)
	}
	currentResponse := harness.handler.buildAgentsCollectionResponse(
		current,
		currentRevision,
	)
	if currentResponse.Effects.GatewayEffect != "restart_required" {
		t.Fatalf(
			"current generation gateway effect = %q, want restart_required",
			currentResponse.Effects.GatewayEffect,
		)
	}
	if err = process.Process.Kill(); err != nil {
		t.Fatalf("Kill(gateway process) error = %v", err)
	}
	if _, err = process.Process.Wait(); err != nil {
		t.Fatalf("Wait(gateway process) error = %v", err)
	}
	staleRuntimeResponse := harness.handler.buildAgentsCollectionResponse(
		current,
		currentRevision,
	)
	if staleRuntimeResponse.Effects.GatewayEffect != "applied" {
		t.Fatalf(
			"exited gateway effect = %q, want applied",
			staleRuntimeResponse.Effects.GatewayEffect,
		)
	}
}

func TestAgentsAPIDeletingLastExplicitAgentClearsRestartEffect(t *testing.T) {
	resetGatewayTestState(t)
	harness := newAgentAPITestHarness(t, nil)
	initialConfig, initialRevision, err := config.LoadCurrentConfigSnapshot(
		harness.configPath,
	)
	if err != nil {
		t.Fatalf("LoadCurrentConfigSnapshot() error = %v", err)
	}
	process := startLongRunningProcess(t)
	t.Cleanup(func() {
		if process.ProcessState == nil {
			_ = process.Process.Kill()
			_, _ = process.Process.Wait()
		}
	})
	gateway.mu.Lock()
	gateway.bootConfigSignature = computeConfigSignature(initialConfig)
	gateway.cmd = process
	setGatewayRuntimeStatusLocked("running")
	gateway.mu.Unlock()

	materialized := decodeAgentCollection(t, harness.request(
		t,
		http.MethodPut,
		"/api/agents/main",
		agentMutationRequest{
			ExpectedConfigRevision: &initialRevision,
			Agent:                  &agentResource{ID: "main", Name: "Main"},
		},
	))
	if materialized.Effects.GatewayEffect != "restart_required" {
		t.Fatalf(
			"materialized main gateway effect = %q, want restart_required",
			materialized.Effects.GatewayEffect,
		)
	}

	restored := decodeAgentCollection(t, harness.request(
		t,
		http.MethodDelete,
		"/api/agents/main",
		agentRevisionRequest{
			ExpectedConfigRevision: &materialized.ConfigRevision,
		},
	))
	if len(restored.Agents) != 1 || !restored.Agents[0].Implicit {
		t.Fatalf("restored collection = %#v, want implicit main", restored)
	}
	if restored.Effects.GatewayEffect != "applied" {
		t.Fatalf(
			"restored gateway effect = %q, want applied",
			restored.Effects.GatewayEffect,
		)
	}

	reloaded := decodeAgentCollection(
		t,
		harness.request(t, http.MethodGet, "/api/agents", nil),
	)
	if reloaded.ConfigRevision != restored.ConfigRevision ||
		reloaded.Effects.GatewayEffect != restored.Effects.GatewayEffect {
		t.Fatalf(
			"mutation response = %#v, subsequent GET = %#v",
			restored,
			reloaded,
		)
	}
}

func TestAgentsAPICreateMaterializesMainAndPreservesOrderedModelPolicy(t *testing.T) {
	harness := newAgentAPITestHarness(t, func(cfg *config.Config) {
		cfg.Gateway.Port = 28765
		cfg.ModelList = []*config.ModelConfig{{
			ModelName: "secret-model",
			Provider:  "openai",
			Model:     "provider/model",
			APIKeys:   config.SimpleSecureStrings("agent-api-secret"),
			Enabled:   true,
		}}
		cfg.ModelAliases = []config.ModelAliasConfig{{
			Name:  "review-model",
			Model: "provider/model",
		}}
	})
	initial := decodeAgentCollection(
		t,
		harness.request(t, http.MethodGet, "/api/agents", nil),
	)
	noFallbacks := []string{}
	create := agentMutationRequest{
		ExpectedConfigRevision: &initial.ConfigRevision,
		Agent: &agentResource{
			ID:         "reviewer",
			Name:       "  Code reviewer  ",
			Workspace:  "  /srv/reviewer  ",
			AccountRef: "  secret-model  ",
			Model: &agentModelPolicy{
				Primary:   "  review-model  ",
				Fallbacks: &noFallbacks,
			},
			Skills: []string{" code-review ", "summarize"},
			Subagents: &agentSubagentsPolicy{
				AllowAgents: []string{" main "},
			},
		},
	}
	recorder := harness.request(t, http.MethodPost, "/api/agents", create)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("POST status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	created := decodeAgentCollection(t, recorder)
	if len(created.Agents) != 2 ||
		created.Agents[0].ID != "main" ||
		created.Agents[1].ID != "reviewer" ||
		!created.Agents[0].DefaultConfigured ||
		created.DefaultAgentID != "main" {
		t.Fatalf("created collection = %#v", created)
	}
	reviewer := created.Agents[1]
	if reviewer.Name != "Code reviewer" || reviewer.Workspace != "/srv/reviewer" ||
		reviewer.AccountRef != "secret-model" ||
		reviewer.Model == nil || reviewer.Model.Primary != "review-model" ||
		reviewer.Model.Fallbacks == nil || len(*reviewer.Model.Fallbacks) != 0 ||
		reviewer.Subagents == nil ||
		len(reviewer.Subagents.AllowAgents) != 1 ||
		reviewer.Subagents.AllowAgents[0] != "main" {
		t.Fatalf("reviewer projection = %#v", reviewer)
	}

	reloaded := decodeAgentCollection(
		t,
		harness.request(t, http.MethodGet, "/api/agents", nil),
	)
	if reloaded.Agents[1].Model == nil ||
		reloaded.Agents[1].Model.Fallbacks == nil ||
		len(*reloaded.Agents[1].Model.Fallbacks) != 0 {
		t.Fatalf("explicit empty fallbacks did not survive save/load: %#v", reloaded.Agents[1])
	}
	inheritFallbacks := agentMutationRequest{
		ExpectedConfigRevision: &reloaded.ConfigRevision,
		Agent: &agentResource{
			ID:    "planner",
			Model: &agentModelPolicy{Primary: "", Fallbacks: nil},
		},
	}
	planned := decodeAgentCollection(
		t,
		harness.request(t, http.MethodPost, "/api/agents", inheritFallbacks),
	)
	if planned.Agents[0].Model != nil ||
		planned.Agents[2].Model == nil ||
		planned.Agents[2].Model.Primary != "" ||
		planned.Agents[2].Model.Fallbacks != nil {
		t.Fatalf(
			"model null and explicit inherit object were not distinct: %#v",
			planned.Agents,
		)
	}
	reloaded = decodeAgentCollection(
		t,
		harness.request(t, http.MethodGet, "/api/agents", nil),
	)
	if reloaded.Agents[2].Model == nil ||
		reloaded.Agents[2].Model.Primary != "" ||
		reloaded.Agents[2].Model.Fallbacks != nil {
		t.Fatalf("explicit inherited model object did not survive save/load: %#v", reloaded.Agents[2])
	}
	saved, err := config.LoadConfigForUpdate(harness.configPath)
	if err != nil {
		t.Fatalf("LoadConfigForUpdate() error = %v", err)
	}
	if saved.Gateway.Port != 28765 || len(saved.ModelList) != 1 ||
		saved.ModelList[0].APIKey() != "agent-api-secret" {
		t.Fatalf("agent mutation changed unrelated config or secret: %#v", saved)
	}
}

func TestAgentsAPIUpdateDefaultDeleteLifecycle(t *testing.T) {
	agentWorkspace := filepath.Join(t.TempDir(), "reviewer-workspace")
	sessionDir := filepath.Join(agentWorkspace, "sessions")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(sessionDir) error = %v", err)
	}
	sessionSentinel := filepath.Join(sessionDir, "keep.json")
	if err := os.WriteFile(sessionSentinel, []byte(`{"keep":true}`), 0o600); err != nil {
		t.Fatalf("WriteFile(session sentinel) error = %v", err)
	}
	harness := newAgentAPITestHarness(t, func(cfg *config.Config) {
		cfg.ModelAliases = []config.ModelAliasConfig{{
			Name:  "review-model",
			Model: "provider/review-model",
		}}
		cfg.Agents.List = []config.AgentConfig{
			{ID: "main", Default: true},
			{ID: "reviewer", Name: "Old", Workspace: agentWorkspace},
		}
	})
	initial := decodeAgentCollection(
		t,
		harness.request(t, http.MethodGet, "/api/agents", nil),
	)
	update := agentMutationRequest{
		ExpectedConfigRevision: &initial.ConfigRevision,
		Agent: &agentResource{
			ID:    "reviewer",
			Name:  "New",
			Model: &agentModelPolicy{Primary: "review-model"},
		},
	}
	updated := decodeAgentCollection(
		t,
		harness.request(t, http.MethodPut, "/api/agents/reviewer", update),
	)
	if updated.Agents[1].Name != "New" ||
		updated.ConfigRevision == initial.ConfigRevision {
		t.Fatalf("updated collection = %#v", updated)
	}
	if data, err := os.ReadFile(sessionSentinel); err != nil || string(data) != `{"keep":true}` {
		t.Fatalf("agent update changed workspace session sentinel: data=%q err=%v", data, err)
	}

	madeDefault := decodeAgentCollection(t, harness.request(
		t,
		http.MethodPost,
		"/api/agents/reviewer/default",
		agentRevisionRequest{ExpectedConfigRevision: &updated.ConfigRevision},
	))
	if madeDefault.DefaultAgentID != "reviewer" ||
		!madeDefault.Agents[1].DefaultConfigured ||
		madeDefault.Agents[0].DefaultConfigured {
		t.Fatalf("default collection = %#v", madeDefault)
	}

	mainDeleted := decodeAgentCollection(t, harness.request(
		t,
		http.MethodDelete,
		"/api/agents/main",
		agentRevisionRequest{ExpectedConfigRevision: &madeDefault.ConfigRevision},
	))
	if len(mainDeleted.Agents) != 1 || mainDeleted.Agents[0].ID != "reviewer" ||
		mainDeleted.DefaultAgentID != "reviewer" {
		t.Fatalf("main deletion = %#v", mainDeleted)
	}
	lastDeleted := decodeAgentCollection(t, harness.request(
		t,
		http.MethodDelete,
		"/api/agents/reviewer",
		agentRevisionRequest{ExpectedConfigRevision: &mainDeleted.ConfigRevision},
	))
	if len(lastDeleted.Agents) != 1 || lastDeleted.Agents[0].ID != "main" ||
		!lastDeleted.Agents[0].Implicit ||
		lastDeleted.DefaultAgentID != "main" {
		t.Fatalf("last deletion did not restore implicit main: %#v", lastDeleted)
	}
	if data, err := os.ReadFile(sessionSentinel); err != nil || string(data) != `{"keep":true}` {
		t.Fatalf("agent deletion changed workspace session sentinel: data=%q err=%v", data, err)
	}
}

func TestAgentsAPIDeleteConfiguredDefaultPromotesFirstRemainingAgent(t *testing.T) {
	harness := newAgentAPITestHarness(t, func(cfg *config.Config) {
		cfg.Agents.List = []config.AgentConfig{
			{ID: "planner"},
			{ID: "reviewer", Default: true},
			{ID: "worker"},
		}
	})
	initial := decodeAgentCollection(
		t,
		harness.request(t, http.MethodGet, "/api/agents", nil),
	)

	deleted := decodeAgentCollection(t, harness.request(
		t,
		http.MethodDelete,
		"/api/agents/reviewer",
		agentRevisionRequest{ExpectedConfigRevision: &initial.ConfigRevision},
	))
	if len(deleted.Agents) != 2 ||
		deleted.Agents[0].ID != "planner" ||
		deleted.Agents[1].ID != "worker" ||
		deleted.DefaultAgentID != "planner" {
		t.Fatalf("delete response order/default = %#v", deleted)
	}
	configuredDefaults := 0
	effectiveDefaults := 0
	for _, agent := range deleted.Agents {
		if agent.DefaultConfigured {
			configuredDefaults++
		}
		if agent.IsDefault {
			effectiveDefaults++
		}
	}
	if configuredDefaults != 1 || effectiveDefaults != 1 ||
		!deleted.Agents[0].DefaultConfigured ||
		!deleted.Agents[0].IsDefault {
		t.Fatalf("delete response defaults = %#v", deleted)
	}

	saved, err := config.LoadConfig(harness.configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if len(saved.Agents.List) != 2 ||
		saved.Agents.List[0].ID != "planner" ||
		saved.Agents.List[1].ID != "worker" ||
		!saved.Agents.List[0].Default ||
		saved.Agents.List[1].Default {
		t.Fatalf("persisted agents order/default = %#v", saved.Agents.List)
	}

	reloaded := decodeAgentCollection(
		t,
		harness.request(t, http.MethodGet, "/api/agents", nil),
	)
	if reloaded.ConfigRevision != deleted.ConfigRevision ||
		len(reloaded.Agents) != 2 ||
		reloaded.Agents[0].ID != "planner" ||
		reloaded.Agents[1].ID != "worker" ||
		reloaded.DefaultAgentID != "planner" ||
		!reloaded.Agents[0].DefaultConfigured ||
		!reloaded.Agents[0].IsDefault ||
		reloaded.Agents[1].DefaultConfigured ||
		reloaded.Agents[1].IsDefault {
		t.Fatalf("reloaded collection disagrees with delete response: %#v", reloaded)
	}
}

func TestAgentsAPIUpdateImplicitMainAndPreservesHiddenSubagentModel(t *testing.T) {
	t.Run("implicit main materializes", func(t *testing.T) {
		harness := newAgentAPITestHarness(t, nil)
		initial := decodeAgentCollection(
			t,
			harness.request(t, http.MethodGet, "/api/agents", nil),
		)
		update := agentMutationRequest{
			ExpectedConfigRevision: &initial.ConfigRevision,
			Agent: &agentResource{
				ID:   "main",
				Name: "Configured main",
			},
		}
		updated := decodeAgentCollection(
			t,
			harness.request(t, http.MethodPut, "/api/agents/main", update),
		)
		if len(updated.Agents) != 1 || updated.Agents[0].Implicit ||
			!updated.Agents[0].DefaultConfigured ||
			updated.Agents[0].Name != "Configured main" {
			t.Fatalf("materialized main = %#v", updated)
		}
	})

	t.Run("hidden delegation model survives reset", func(t *testing.T) {
		harness := newAgentAPITestHarness(t, func(cfg *config.Config) {
			cfg.ModelAliases = []config.ModelAliasConfig{{
				Name:  "reserved-model",
				Model: "provider/reserved-model",
			}}
			cfg.Agents.List = []config.AgentConfig{
				{ID: "main", Default: true},
				{
					ID: "manager",
					Subagents: &config.SubagentsConfig{
						AllowAgents: []string{"main"},
						Model: &config.AgentModelConfig{
							Primary:   "reserved-model",
							Fallbacks: []string{},
						},
					},
				},
			}
		})
		initial := decodeAgentCollection(
			t,
			harness.request(t, http.MethodGet, "/api/agents", nil),
		)
		update := agentMutationRequest{
			ExpectedConfigRevision: &initial.ConfigRevision,
			Agent: &agentResource{
				ID:        "manager",
				Name:      "Manager",
				Subagents: nil,
			},
		}
		updated := decodeAgentCollection(
			t,
			harness.request(t, http.MethodPut, "/api/agents/manager", update),
		)
		if updated.Agents[1].Subagents != nil {
			t.Fatalf("hidden-only subagents leaked in response: %#v", updated.Agents[1])
		}
		saved, err := config.LoadConfigForUpdate(harness.configPath)
		if err != nil {
			t.Fatalf("LoadConfigForUpdate() error = %v", err)
		}
		model := saved.Agents.List[1].Subagents.Model
		if model == nil || model.Primary != "reserved-model" ||
			model.Fallbacks == nil || len(model.Fallbacks) != 0 {
			t.Fatalf("hidden delegation model was erased: %#v", saved.Agents.List[1].Subagents)
		}
	})
}

func TestAgentsAPIDeleteReportsOnlyDirectConfigurationBlockers(t *testing.T) {
	harness := newAgentAPITestHarness(t, func(cfg *config.Config) {
		cfg.Agents.List = []config.AgentConfig{
			{ID: "main", Default: true},
			{ID: "worker"},
			{
				ID: "manager",
				Subagents: &config.SubagentsConfig{
					AllowAgents: []string{"worker"},
				},
			},
		}
		cfg.Agents.Dispatch = &config.DispatchConfig{
			Rules: []config.DispatchRule{{
				Name:  "support",
				Agent: "worker",
			}},
		}
	})
	initial := decodeAgentCollection(
		t,
		harness.request(t, http.MethodGet, "/api/agents", nil),
	)
	recorder := harness.request(
		t,
		http.MethodDelete,
		"/api/agents/worker",
		agentRevisionRequest{ExpectedConfigRevision: &initial.ConfigRevision},
	)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("DELETE status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var failure agentErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &failure); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if failure.Error != "agent_referenced" || len(failure.Blockers) != 2 ||
		failure.Blockers[0].Kind != "dispatch_rule" ||
		failure.Blockers[0].Name != "support" ||
		failure.Blockers[1].Kind != "subagent_allowlist" ||
		failure.Blockers[1].AgentID != "manager" {
		t.Fatalf("delete blockers = %#v", failure)
	}

	wildcardHarness := newAgentAPITestHarness(t, func(cfg *config.Config) {
		cfg.Agents.List = []config.AgentConfig{
			{ID: "main", Default: true},
			{ID: "worker"},
			{
				ID: "manager",
				Subagents: &config.SubagentsConfig{
					AllowAgents: []string{"*"},
				},
			},
		}
	})
	wildcardInitial := decodeAgentCollection(
		t,
		wildcardHarness.request(t, http.MethodGet, "/api/agents", nil),
	)
	deleted := wildcardHarness.request(
		t,
		http.MethodDelete,
		"/api/agents/worker",
		agentRevisionRequest{
			ExpectedConfigRevision: &wildcardInitial.ConfigRevision,
		},
	)
	if deleted.Code != http.StatusOK {
		t.Fatalf("wildcard DELETE status = %d, body=%s", deleted.Code, deleted.Body.String())
	}
}

func TestAgentsAPIRejectsStaleInvalidAndUnsafeMutations(t *testing.T) {
	harness := newAgentAPITestHarness(t, nil)
	initial := decodeAgentCollection(
		t,
		harness.request(t, http.MethodGet, "/api/agents", nil),
	)

	tests := []struct {
		name        string
		method      string
		path        string
		body        string
		contentType string
		origin      string
		fetchSite   string
		wantStatus  int
		wantError   string
	}{
		{
			name:        "stale revision",
			method:      http.MethodPost,
			path:        "/api/agents",
			body:        `{"expected_config_revision":"stale","agent":{"id":"worker"}}`,
			contentType: "application/json",
			wantStatus:  http.StatusConflict,
			wantError:   "config_revision_mismatch",
		},
		{
			name:        "noncanonical id",
			method:      http.MethodPost,
			path:        "/api/agents",
			body:        `{"expected_config_revision":"` + initial.ConfigRevision + `","agent":{"id":"Worker"}}`,
			contentType: "application/json",
			wantStatus:  http.StatusUnprocessableEntity,
			wantError:   "invalid_agent",
		},
		{
			name:        "duplicate JSON member",
			method:      http.MethodPost,
			path:        "/api/agents",
			body:        `{"expected_config_revision":"` + initial.ConfigRevision + `","expected_config_revision":"other","agent":{"id":"worker"}}`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
			wantError:   "invalid_agent_request",
		},
		{
			name:        "case-insensitive duplicate top-level JSON member",
			method:      http.MethodPost,
			path:        "/api/agents",
			body:        `{"expected_config_revision":"` + initial.ConfigRevision + `","agent":{"id":"worker"},"Agent":{"id":"other"}}`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
			wantError:   "invalid_agent_request",
		},
		{
			name:        "case-insensitive duplicate nested JSON member",
			method:      http.MethodPost,
			path:        "/api/agents",
			body:        `{"expected_config_revision":"` + initial.ConfigRevision + `","agent":{"id":"worker","ID":"other"}}`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
			wantError:   "invalid_agent_request",
		},
		{
			name:       "missing content type",
			method:     http.MethodPost,
			path:       "/api/agents",
			body:       `{"expected_config_revision":"` + initial.ConfigRevision + `","agent":{"id":"worker"}}`,
			wantStatus: http.StatusUnsupportedMediaType,
			wantError:  "json_content_type_required",
		},
		{
			name:        "cross origin",
			method:      http.MethodPost,
			path:        "/api/agents",
			body:        `{"expected_config_revision":"` + initial.ConfigRevision + `","agent":{"id":"worker"}}`,
			contentType: "application/json",
			origin:      "https://evil.example",
			fetchSite:   "cross-site",
			wantStatus:  http.StatusForbidden,
			wantError:   "cross_origin_agent_mutation",
		},
		{
			name:        "unknown field",
			method:      http.MethodPost,
			path:        "/api/agents",
			body:        `{"expected_config_revision":"` + initial.ConfigRevision + `","agent":{"id":"worker","secret":"no"}}`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
			wantError:   "invalid_agent_request",
		},
		{
			name:        "blank revision",
			method:      http.MethodPost,
			path:        "/api/agents",
			body:        `{"expected_config_revision":"   ","agent":{"id":"worker"}}`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
			wantError:   "expected_config_revision_required",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(
				test.method,
				test.path,
				strings.NewReader(test.body),
			)
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			if test.fetchSite != "" {
				request.Header.Set("Sec-Fetch-Site", test.fetchSite)
			}
			recorder := httptest.NewRecorder()
			harness.mux.ServeHTTP(recorder, request)
			if recorder.Code != test.wantStatus ||
				!strings.Contains(recorder.Body.String(), test.wantError) {
				t.Fatalf(
					"response = %d/%q, want %d containing %q",
					recorder.Code,
					recorder.Body.String(),
					test.wantStatus,
					test.wantError,
				)
			}
		})
	}

	oversized := httptest.NewRequest(
		http.MethodPost,
		"/api/agents",
		strings.NewReader(strings.Repeat("x", agentRequestMaxBytes+1)),
	)
	oversized.Header.Set("Content-Type", "application/json")
	oversizedRecorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(oversizedRecorder, oversized)
	if oversizedRecorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf(
			"oversized response = %d/%q",
			oversizedRecorder.Code,
			oversizedRecorder.Body.String(),
		)
	}

	duplicateContentType := httptest.NewRequest(
		http.MethodPost,
		"/api/agents",
		strings.NewReader(
			`{"expected_config_revision":"`+initial.ConfigRevision+
				`","agent":{"id":"worker"}}`,
		),
	)
	duplicateContentType.Header["Content-Type"] = []string{
		"application/json",
		"application/json",
	}
	duplicateContentTypeRecorder := httptest.NewRecorder()
	harness.mux.ServeHTTP(duplicateContentTypeRecorder, duplicateContentType)
	if duplicateContentTypeRecorder.Code != http.StatusUnsupportedMediaType {
		t.Fatalf(
			"duplicate Content-Type response = %d/%q",
			duplicateContentTypeRecorder.Code,
			duplicateContentTypeRecorder.Body.String(),
		)
	}
}

func TestAgentsAPISecretOnlyRevisionChangeFencesStaleMutation(t *testing.T) {
	harness := newAgentAPITestHarness(t, func(cfg *config.Config) {
		cfg.ModelList = []*config.ModelConfig{{
			ModelName: "private-model",
			Provider:  "openai",
			Model:     "provider/model",
			APIKeys:   config.SimpleSecureStrings("first-secret"),
			Enabled:   true,
		}}
	})
	initial := decodeAgentCollection(
		t,
		harness.request(t, http.MethodGet, "/api/agents", nil),
	)
	publicBefore, err := os.ReadFile(harness.configPath)
	if err != nil {
		t.Fatalf("ReadFile(before) error = %v", err)
	}
	securityPath := filepath.Join(
		filepath.Dir(harness.configPath),
		config.SecurityConfigFile,
	)
	securityData, err := os.ReadFile(securityPath)
	if err != nil {
		t.Fatalf("ReadFile(security) error = %v", err)
	}
	if !strings.Contains(string(securityData), "first-secret") {
		t.Fatal("security sidecar does not contain the test credential")
	}
	securityData = []byte(strings.ReplaceAll(
		string(securityData),
		"first-secret",
		"second-secret",
	))
	if err = os.WriteFile(securityPath, securityData, 0o600); err != nil {
		t.Fatalf("WriteFile(security) error = %v", err)
	}
	publicAfter, err := os.ReadFile(harness.configPath)
	if err != nil {
		t.Fatalf("ReadFile(after) error = %v", err)
	}
	if string(publicAfter) != string(publicBefore) {
		t.Fatal("credential-only concurrent change unexpectedly changed public config bytes")
	}

	recorder := harness.request(
		t,
		http.MethodPost,
		"/api/agents",
		agentMutationRequest{
			ExpectedConfigRevision: &initial.ConfigRevision,
			Agent:                  &agentResource{ID: "worker"},
		},
	)
	if recorder.Code != http.StatusConflict ||
		!strings.Contains(recorder.Body.String(), "config_revision_mismatch") {
		t.Fatalf("stale response = %d/%q", recorder.Code, recorder.Body.String())
	}
	saved, err := config.LoadConfigForUpdate(harness.configPath)
	if err != nil {
		t.Fatalf("LoadConfigForUpdate(saved) error = %v", err)
	}
	if len(saved.Agents.List) != 0 ||
		len(saved.ModelList) != 1 ||
		saved.ModelList[0].APIKey() != "second-secret" {
		t.Fatalf("stale mutation overwrote concurrent config or secret: %#v", saved)
	}
}

func TestAgentsAPIValidatesReferencesAndExistingAmbiguity(t *testing.T) {
	harness := newAgentAPITestHarness(t, func(cfg *config.Config) {
		cfg.Agents.List = []config.AgentConfig{
			{ID: "main", Default: true},
			{ID: "manager"},
		}
	})
	initial := decodeAgentCollection(
		t,
		harness.request(t, http.MethodGet, "/api/agents", nil),
	)
	for name, allowAgents := range map[string][]string{
		"missing target": {"missing"},
		"self target":    {"manager"},
		"mixed wildcard": {"*", "main"},
		"duplicate":      {"main", " main "},
	} {
		t.Run(name, func(t *testing.T) {
			update := agentMutationRequest{
				ExpectedConfigRevision: &initial.ConfigRevision,
				Agent: &agentResource{
					ID: "manager",
					Subagents: &agentSubagentsPolicy{
						AllowAgents: allowAgents,
					},
				},
			}
			recorder := harness.request(
				t,
				http.MethodPut,
				"/api/agents/manager",
				update,
			)
			if recorder.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}

	ambiguous := newAgentAPITestHarness(t, func(cfg *config.Config) {
		cfg.Agents.List = []config.AgentConfig{
			{ID: "main", Default: true},
			{ID: "main"},
		}
	})
	recorder := ambiguous.request(t, http.MethodGet, "/api/agents", nil)
	if recorder.Code != http.StatusConflict ||
		!strings.Contains(recorder.Body.String(), "invalid_agent_configuration") {
		t.Fatalf("ambiguous response = %d/%q", recorder.Code, recorder.Body.String())
	}
}
