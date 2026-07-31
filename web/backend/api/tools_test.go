package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestHandleListTools(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Tools.ReadFile.Enabled = true
	cfg.Tools.WriteFile.Enabled = false
	cfg.Tools.Cron.Enabled = true
	cfg.Tools.FindSkills.Enabled = true
	cfg.Tools.Skills.Enabled = true
	cfg.Tools.Spawn.Enabled = true
	cfg.Tools.Subagent.Enabled = false
	cfg.Tools.Workflow.Enabled = true
	cfg.Workflows.Enabled = false
	cfg.Tools.MCP.Enabled = true
	cfg.Tools.MCP.Discovery.Enabled = true
	cfg.Tools.MCP.Discovery.UseRegex = true
	cfg.Tools.MCP.Discovery.UseBM25 = false
	err = config.SaveConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/tools", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp toolSupportResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	gotTools := make(map[string]toolSupportItem, len(resp.Tools))
	for _, tool := range resp.Tools {
		gotTools[tool.Name] = tool
	}
	if gotTools["read_file"].Status != "enabled" {
		t.Fatalf("read_file status = %q, want enabled", gotTools["read_file"].Status)
	}
	if gotTools["write_file"].Status != "disabled" {
		t.Fatalf("write_file status = %q, want disabled", gotTools["write_file"].Status)
	}
	if gotTools["cron"].Status != "enabled" {
		t.Fatalf("cron status = %q, want enabled", gotTools["cron"].Status)
	}
	if gotTools["spawn"].Status != "blocked" || gotTools["spawn"].ReasonCode != "requires_subagent" {
		t.Fatalf("spawn = %#v, want blocked/requires_subagent", gotTools["spawn"])
	}
	if gotTools["find_skills"].Status != "enabled" {
		t.Fatalf("find_skills status = %q, want enabled", gotTools["find_skills"].Status)
	}
	if gotTools["workflow"].Status != "blocked" ||
		gotTools["workflow"].ReasonCode != "requires_workflows" {
		t.Fatalf("workflow = %#v, want blocked/requires_workflows", gotTools["workflow"])
	}
	if gotTools["tool_search_tool_regex"].Status != "enabled" {
		t.Fatalf("tool_search_tool_regex status = %q, want enabled", gotTools["tool_search_tool_regex"].Status)
	}
	if gotTools["tool_search_tool_regex"].ConfigKey != "mcp.discovery.use_regex" {
		t.Fatalf(
			"tool_search_tool_regex config_key = %q, want mcp.discovery.use_regex",
			gotTools["tool_search_tool_regex"].ConfigKey,
		)
	}
	if gotTools["tool_search_tool_bm25"].Status != "disabled" {
		t.Fatalf("tool_search_tool_bm25 status = %q, want disabled", gotTools["tool_search_tool_bm25"].Status)
	}
	if gotTools["tool_search_tool_bm25"].ConfigKey != "mcp.discovery.use_bm25" {
		t.Fatalf(
			"tool_search_tool_bm25 config_key = %q, want mcp.discovery.use_bm25",
			gotTools["tool_search_tool_bm25"].ConfigKey,
		)
	}
	if runtime.GOOS == "linux" {
		if gotTools["i2c"].Status != "disabled" {
			t.Fatalf("i2c status = %q, want disabled on linux when config is off", gotTools["i2c"].Status)
		}
		if gotTools["serial"].Status != "disabled" {
			t.Fatalf("serial status = %q, want disabled when config is off", gotTools["serial"].Status)
		}

		cfg.Tools.Serial.Enabled = true
		if err := config.SaveConfig(configPath, cfg); err != nil {
			t.Fatalf("SaveConfig() error = %v", err)
		}

		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodGet, "/api/tools", nil)
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}

		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("Unmarshal() error = %v", err)
		}
		gotTools = make(map[string]toolSupportItem, len(resp.Tools))
		for _, tool := range resp.Tools {
			gotTools[tool.Name] = tool
		}
		if gotTools["serial"].Status != "enabled" {
			t.Fatalf("serial = %#v, want enabled on linux when config is on", gotTools["serial"])
		}
	} else {
		cfg.Tools.I2C.Enabled = true
		cfg.Tools.SPI.Enabled = true
		cfg.Tools.Serial.Enabled = true
		if err := config.SaveConfig(configPath, cfg); err != nil {
			t.Fatalf("SaveConfig() error = %v", err)
		}

		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodGet, "/api/tools", nil)
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}

		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("Unmarshal() error = %v", err)
		}
		gotTools = make(map[string]toolSupportItem, len(resp.Tools))
		for _, tool := range resp.Tools {
			gotTools[tool.Name] = tool
		}

		if gotTools["i2c"].Status != "blocked" || gotTools["i2c"].ReasonCode != "requires_linux" {
			t.Fatalf("i2c = %#v, want blocked/requires_linux", gotTools["i2c"])
		}
		if gotTools["spi"].Status != "blocked" || gotTools["spi"].ReasonCode != "requires_linux" {
			t.Fatalf("spi = %#v, want blocked/requires_linux", gotTools["spi"])
		}
		switch runtime.GOOS {
		case "darwin", "windows":
			if gotTools["serial"].Status != "enabled" {
				t.Fatalf("serial = %#v, want enabled on supported host", gotTools["serial"])
			}
		default:
			if gotTools["serial"].Status != "blocked" || gotTools["serial"].ReasonCode != "requires_serial_platform" {
				t.Fatalf("serial = %#v, want blocked/requires_serial_platform", gotTools["serial"])
			}
		}
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("GET tools response did not disable caching")
	}
}

func TestBuildToolSupportResolvesWorkflowMasterAndToolFlag(t *testing.T) {
	tests := []struct {
		name             string
		workflowsEnabled bool
		toolEnabled      bool
		wantStatus       string
		wantReason       string
	}{
		{
			name:             "raw tool off",
			workflowsEnabled: true,
			toolEnabled:      false,
			wantStatus:       "disabled",
		},
		{
			name:             "raw tool on master off",
			workflowsEnabled: false,
			toolEnabled:      true,
			wantStatus:       "blocked",
			wantReason:       "requires_workflows",
		},
		{
			name:             "both on",
			workflowsEnabled: true,
			toolEnabled:      true,
			wantStatus:       "enabled",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Workflows.Enabled = test.workflowsEnabled
			cfg.Tools.Workflow.Enabled = test.toolEnabled
			var workflow toolSupportItem
			for _, item := range buildToolSupport(cfg) {
				if item.Name == "workflow" {
					workflow = item
					break
				}
			}
			if workflow.Status != test.wantStatus ||
				workflow.ReasonCode != test.wantReason {
				t.Fatalf(
					"workflow support = %#v, want status=%q reason=%q",
					workflow,
					test.wantStatus,
					test.wantReason,
				)
			}
		})
	}
}

func TestHandleToolAdaptationRoundTrip(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	overrides := []config.ToolAdaptationProfileOverride{{
		Provider:           " OpenAI ",
		Model:              "gpt-test",
		VisibleToolSurface: "simple",
	}}
	payload := toolAdaptationConfigRequest{
		Enabled:                true,
		VisibleToolSurface:     "codex",
		LearnFromToolCalls:     true,
		RunModelProbes:         false,
		AllowRuntimeDowngrade:  "auto",
		AllowRuntimePromotion:  "never",
		ApplyVisibleChanges:    "context_boundary",
		CacheSensitiveAPIs:     "auto",
		CacheBreakingDowngrade: true,
		ProfileOverrides:       &overrides,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/tools/adaptation", bytes.NewReader(body))
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var putResp toolAdaptationConfigRequest
	if err := json.Unmarshal(rec.Body.Bytes(), &putResp); err != nil {
		t.Fatalf("Unmarshal PUT response error = %v", err)
	}
	if putResp.VisibleToolSurface != "codex" || putResp.ApplyVisibleChanges != "context_boundary" {
		t.Fatalf("PUT response = %#v, want codex/context_boundary", putResp)
	}
	if putResp.ProfileOverrides == nil || len(*putResp.ProfileOverrides) != 1 ||
		(*putResp.ProfileOverrides)[0].Provider != "openai" {
		t.Fatalf("PUT profile overrides = %#v, want canonical saved override", putResp.ProfileOverrides)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/tools/adaptation", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var getResp toolAdaptationConfigRequest
	if err := json.Unmarshal(rec.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("Unmarshal GET response error = %v", err)
	}
	if getResp.VisibleToolSurface != putResp.VisibleToolSurface ||
		getResp.ApplyVisibleChanges != putResp.ApplyVisibleChanges ||
		getResp.AllowRuntimePromotion != putResp.AllowRuntimePromotion ||
		getResp.CacheBreakingDowngrade != putResp.CacheBreakingDowngrade {
		t.Fatalf("GET response = %#v, want saved fields from %#v", getResp, putResp)
	}
	if getResp.ProfileOverrides == nil || len(*getResp.ProfileOverrides) != 1 {
		t.Fatalf("GET profile overrides = %#v, want saved override", getResp.ProfileOverrides)
	}
	if getResp.Resolved == nil {
		t.Fatal("GET response Resolved = nil, want resolved state")
	}
	if getResp.Resolved.PinnedToolSurface != "codex" {
		t.Fatalf("resolved pinned surface = %q, want codex", getResp.Resolved.PinnedToolSurface)
	}
}

func TestHandleToolAdaptationLegacyPutPreservesProfileOverrides(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Tools.Adaptation = config.DefaultToolAdaptationConfig()
	cfg.Tools.Adaptation.ProfileOverrides = []config.ToolAdaptationProfileOverride{{
		Provider:           "openai",
		Model:              "gpt-test",
		VisibleToolSurface: "simple",
	}}
	if saveErr := config.SaveConfig(configPath, cfg); saveErr != nil {
		t.Fatalf("SaveConfig() error = %v", saveErr)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	body := `{
		"enabled": true,
		"visible_tool_surface": "auto",
		"learn_from_tool_calls": true,
		"run_model_probes": true,
		"allow_runtime_downgrade": "auto",
		"allow_runtime_promotion": "auto",
		"apply_visible_changes": "next_session",
		"cache_sensitive_apis": "auto",
		"cache_breaking_downgrade": false
	}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/tools/adaptation", strings.NewReader(body))
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	updated, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() after PUT error = %v", err)
	}
	if len(updated.Tools.Adaptation.ProfileOverrides) != 1 {
		t.Fatalf(
			"ProfileOverrides = %#v, want legacy PUT to preserve override",
			updated.Tools.Adaptation.ProfileOverrides,
		)
	}
}

func TestHandleToolAdaptationProbeRequiresProbeEnabled(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Tools.Adaptation = config.DefaultToolAdaptationConfig()
	cfg.Tools.Adaptation.Enabled = true
	cfg.Tools.Adaptation.RunModelProbes = false
	if saveErr := config.SaveConfig(configPath, cfg); saveErr != nil {
		t.Fatalf("SaveConfig() error = %v", saveErr)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/tools/adaptation/probe", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("POST status = %d, want %d, body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
}

func TestHandleToolAdaptationProbeTargetsRequestedProfile(t *testing.T) {
	var requestedModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode upstream request: %v", err)
		}
		requestedModel = body.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{
				"message":{
					"role":"assistant",
					"tool_calls":[{
						"id":"probe-call",
						"type":"function",
						"function":{
							"name":"adaptation_probe_echo",
							"arguments":"{\"value\":\"probe-ok\"}"
						}
					}]
				}
			}]
		}`))
	}))
	defer upstream.Close()

	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "default",
			Provider:  "openai",
			Model:     "gpt-default",
			APIBase:   upstream.URL + "/v1",
			APIKeys:   config.SimpleSecureStrings("sk-default"),
			Enabled:   true,
		},
		{
			ModelName: "target",
			Provider:  "openai",
			Model:     "gpt-target",
			APIBase:   upstream.URL + "/v1",
			APIKeys:   config.SimpleSecureStrings("sk-target"),
			Enabled:   true,
		},
	}
	cfg.ModelAliases = []config.ModelAliasConfig{
		{Name: "default-model", Model: "gpt-default"},
		{Name: "target-model", Model: "gpt-target"},
	}
	cfg.Tools.Adaptation = config.DefaultToolAdaptationConfig()
	cfg.Tools.Adaptation.VisibleToolSurface = config.ToolSurfacePicoClaw
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/tools/adaptation/probe",
		strings.NewReader(`{"account_ref":"target","model_alias":"target-model"}`),
	)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if requestedModel != "gpt-target" {
		t.Fatalf("upstream model = %q, want targeted model gpt-target", requestedModel)
	}

	var result struct {
		Success bool `json:"success"`
		Profile struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"profile"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("Unmarshal probe response error = %v", err)
	}
	if !result.Success || result.Profile.Provider != "openai" ||
		result.Profile.Model != "gpt-target" {
		t.Fatalf("probe result = %#v, want successful targeted profile", result)
	}
}

func TestHandleToolAdaptationProbeEmptyBodyResolvesVirtualAccountRouter(t *testing.T) {
	var requestedModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode upstream request: %v", err)
		}
		requestedModel = body.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{
				"message":{
					"role":"assistant",
					"tool_calls":[{
						"id":"probe-call",
						"type":"function",
						"function":{
							"name":"adaptation_probe_echo",
							"arguments":"{\"value\":\"probe-ok\"}"
						}
					}]
				}
			}]
		}`))
	}))
	defer upstream.Close()

	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Agents.Defaults.AccountRef = "router-1"
	cfg.Agents.Defaults.ModelName = "coding"
	cfg.ModelAliases = []config.ModelAliasConfig{{
		Name:  "coding",
		Model: "openai/old-model",
	}}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "account-a",
		Provider:  "openai",
		APIBase:   upstream.URL + "/v1",
		APIKeys:   config.SimpleSecureStrings("sk-router"),
		Enabled:   true,
	}}
	cfg.AccountRouters = []config.AccountRouterConfig{{
		Name:    "router-1",
		Enabled: true,
		Entry:   "account",
		Blocks: []config.AccountRouterBlock{{
			ID:      "account",
			Type:    config.AccountRouterBlockTypeAccount,
			Account: "account-a",
		}},
	}}
	cfg.Tools.Adaptation = config.DefaultToolAdaptationConfig()
	cfg.Tools.Adaptation.VisibleToolSurface = config.ToolSurfacePicoClaw
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/tools/adaptation/probe", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if requestedModel != "old-model" {
		t.Fatalf("upstream model = %q, want account model old-model", requestedModel)
	}

	var result struct {
		Success bool `json:"success"`
		Profile struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"profile"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("Unmarshal probe response error = %v", err)
	}
	if !result.Success || result.Profile.Provider != "openai" ||
		result.Profile.Model != "old-model" {
		t.Fatalf("probe result = %#v, want concrete router profile", result)
	}
}

func TestHandleToolAdaptationProbeRejectsPartialProfile(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/tools/adaptation/probe",
		strings.NewReader(`{"account_ref":"openai-work"}`),
	)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "account_ref and model_alias") {
		t.Fatalf("POST status/body = %d/%q, want partial-profile error", rec.Code, rec.Body.String())
	}
}

func TestHandleToolAdaptationProbeRejectsRawProviderModelTarget(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Tools.Adaptation = config.DefaultToolAdaptationConfig()
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/tools/adaptation/probe",
		strings.NewReader(`{"provider":"openai","model":"gpt-5.4"}`),
	)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), `unknown field "provider"`) {
		t.Fatalf(
			"POST status/body = %d/%q, want raw provider/model rejection",
			rec.Code,
			rec.Body.String(),
		)
	}
}

func TestProbeModelConfigForProfileUsesConfiguredModelEntry(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = "alias"
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "alias",
		Provider:  "openai",
		Model:     "gpt-test",
		APIBase:   "http://127.0.0.1:1/v1",
		Enabled:   true,
	}}

	got, err := probeModelConfigForProfile(cfg, "openai", "gpt-test")
	if err != nil {
		t.Fatalf("probeModelConfigForProfile() error = %v", err)
	}
	if got.Provider != "openai" || got.Model != "gpt-test" || got.APIBase == "" {
		t.Fatalf("probe model config = %#v, want configured model entry", got)
	}
}

func TestProbeModelConfigForProfilePrefersUsableExactCandidate(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "missing-key",
			Provider:  "openai",
			Model:     "gpt-test",
			Enabled:   true,
		},
		{
			ModelName: "configured",
			Provider:  "openai",
			Model:     "gpt-test",
			APIKeys:   config.SimpleSecureStrings("sk-configured"),
			Enabled:   true,
		},
	}

	got, err := probeModelConfigForProfile(cfg, "gpt", "gpt-test")
	if err != nil {
		t.Fatalf("probeModelConfigForProfile() error = %v", err)
	}
	if got.ModelName != "configured" || got.APIKey() != "sk-configured" {
		t.Fatalf("probe model config = %#v, want second usable exact candidate", got)
	}
}

func TestProbeModelConfigForProfileIgnoresDisabledCandidates(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "disabled",
		Provider:  "openai",
		Model:     "gpt-test",
		APIKeys:   config.SimpleSecureStrings("sk-disabled"),
		Enabled:   false,
	}}

	_, err := probeModelConfigForProfile(cfg, "openai", "gpt-test")
	if err == nil || !strings.Contains(err.Error(), "no configured upstream model") {
		t.Fatalf("error = %v, want disabled candidate ignored", err)
	}
}

func TestProbeModelConfigForProfileUsesEnabledAccountsIndependentlyOfRouters(t *testing.T) {
	tests := []struct {
		name           string
		routerEnabled  bool
		accountEnabled bool
		wantFound      bool
	}{
		{name: "disabled router does not hide account", routerEnabled: false, accountEnabled: true, wantFound: true},
		{name: "disabled account is ignored", routerEnabled: true, accountEnabled: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.ModelList = []*config.ModelConfig{{
				ModelName: "account-a",
				Provider:  "openai",
				Model:     "old-model",
				APIKeys:   config.SimpleSecureStrings("sk-test"),
				Enabled:   tt.accountEnabled,
			}}
			cfg.AccountRouters = []config.AccountRouterConfig{{
				Name:    "router-1",
				Enabled: tt.routerEnabled,
				Entry:   "account",
				Blocks: []config.AccountRouterBlock{{
					ID:      "account",
					Type:    config.AccountRouterBlockTypeAccount,
					Account: "account-a",
				}},
			}}
			cfg.MaterializeAccountRouterModels()

			_, err := probeModelConfigForProfile(cfg, "openai", "requested-model")
			if tt.wantFound {
				if err != nil {
					t.Fatalf("error = %v, want enabled direct account available", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "no configured upstream model") {
				t.Fatalf("error = %v, want disabled account ignored", err)
			}
		})
	}
}

func TestProbeModelConfigReadyMatchesProviderFactoryKeyRequirements(t *testing.T) {
	anthropic := &config.ModelConfig{
		Provider: "anthropic",
		Model:    "claude-test",
		APIBase:  "http://127.0.0.1:1/v1",
		Enabled:  true,
	}
	if probeModelConfigReady(anthropic) {
		t.Fatal("Anthropic config with api_base but no key reported probe-ready")
	}

	openAI := &config.ModelConfig{
		Provider: "openai",
		Model:    "gpt-test",
		APIBase:  "http://127.0.0.1:1/v1",
		Enabled:  true,
	}
	if !probeModelConfigReady(openAI) {
		t.Fatal("OpenAI-compatible config with api_base reported unavailable")
	}
}

func TestProbeModelConfigForProfileUsesUsableAccountForRequestedProfile(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "account-missing-key",
			Provider:  "openai",
			Model:     "old-model",
			Enabled:   true,
		},
		{
			ModelName: "account-configured",
			Provider:  "openai",
			Model:     "old-model",
			APIKeys:   config.SimpleSecureStrings("sk-configured"),
			Enabled:   true,
		},
	}
	cfg.AccountRouters = []config.AccountRouterConfig{{
		Name:    "router-1",
		Enabled: true,
		Entry:   "pool",
		Blocks: []config.AccountRouterBlock{{
			ID:       "pool",
			Type:     config.AccountRouterBlockTypeLoadBalance,
			Accounts: []string{"account-missing-key", "account-configured"},
		}},
	}}
	cfg.MaterializeAccountRouterModels()

	got, err := probeModelConfigForProfile(cfg, "openai", "requested-model")
	if err != nil {
		t.Fatalf("probeModelConfigForProfile() error = %v", err)
	}
	if got.ModelName != "account-configured" || got.APIKey() != "sk-configured" {
		t.Fatalf("probe model config = %#v, want usable account for requested profile", got)
	}
}

func TestProbeModelConfigForProfileResolvesCredentialAccountRouter(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = "router-1"
	cfg.AccountRouters = []config.AccountRouterConfig{{
		Name:    "router-1",
		Enabled: true,
		Entry:   "account",
		Blocks: []config.AccountRouterBlock{{
			ID:      "account",
			Type:    config.AccountRouterBlockTypeAccount,
			Account: "credential:github-copilot:work",
		}},
	}}
	cfg.MaterializeAccountRouterModels()

	got, err := probeModelConfigForProfile(cfg, "github-copilot", "gpt-5.4")
	if err != nil {
		t.Fatalf("probeModelConfigForProfile() error = %v", err)
	}
	if got.Provider != "github-copilot" || got.Model != "gpt-5.4" ||
		got.AuthMethod != "token" || got.CredentialID != "github-copilot:work" {
		t.Fatalf("probe model config = %#v, want concrete GitHub Copilot credential config", got)
	}
	if got.IsAccountRouter() {
		t.Fatalf("probe model config remains router: %#v", got)
	}
}

func TestProbeModelConfigForProfileAppliesRequestedModelToRouterAccount(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "account-a",
			Provider:  "openrouter",
			Model:     "old-model",
			APIKeys:   config.SimpleSecureStrings("key-a"),
			Enabled:   true,
		},
		{
			ModelName: "account-b",
			Provider:  "anthropic",
			Model:     "old-model",
			APIKeys:   config.SimpleSecureStrings("key-b"),
			Enabled:   true,
		},
	}
	cfg.AccountRouters = []config.AccountRouterConfig{{
		Name:    "router-1",
		Enabled: true,
		Entry:   "pool",
		Blocks: []config.AccountRouterBlock{{
			ID:       "pool",
			Type:     config.AccountRouterBlockTypeLoadBalance,
			Accounts: []string{"account-a", "account-b"},
		}},
	}}
	cfg.MaterializeAccountRouterModels()

	got, err := probeModelConfigForProfile(cfg, "anthropic", "requested-model")
	if err != nil {
		t.Fatalf("probeModelConfigForProfile() error = %v", err)
	}
	if got.Provider != "anthropic" || got.Model != "requested-model" ||
		got.APIKey() != "key-b" {
		t.Fatalf("probe model config = %#v, want account-b with requested model", got)
	}
}

func TestProbeModelConfigForProfileRejectsUnconfiguredProfile(t *testing.T) {
	cfg := config.DefaultConfig()

	_, err := probeModelConfigForProfile(cfg, "openai", "unconfigured")
	if err == nil || !strings.Contains(err.Error(), "no configured upstream model") {
		t.Fatalf("error = %v, want unconfigured profile error", err)
	}
}

func TestResolveToolAdaptationProfileForConfigSplitsPrefixedAlias(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.AccountRef = "alias-account"
	cfg.Agents.Defaults.ModelName = "alias"
	cfg.ModelAliases = []config.ModelAliasConfig{{
		Name:  "alias",
		Model: "anthropic/claude-sonnet",
	}}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "alias-account",
		Model:     "anthropic/claude-sonnet",
		Enabled:   true,
	}}

	provider, model := resolveToolAdaptationProfileForConfig(cfg)
	if provider != "anthropic" || model != "claude-sonnet" {
		t.Fatalf("profile = %s/%s, want anthropic/claude-sonnet", provider, model)
	}
}

func TestResolveToolAdaptationProfileForConfigCanonicalizesCredentialProvider(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.AccountRef = "router-1"
	cfg.Agents.Defaults.ModelName = "coding"
	cfg.ModelAliases = []config.ModelAliasConfig{{
		Name:  "coding",
		Model: "gpt-4.1",
	}}
	cfg.AccountRouters = []config.AccountRouterConfig{{
		Name:    "router-1",
		Enabled: true,
		Entry:   "account",
		Blocks: []config.AccountRouterBlock{{
			ID:      "account",
			Type:    config.AccountRouterBlockTypeAccount,
			Account: "credential:copilot:work",
		}},
	}}
	cfg.MaterializeAccountRouterModels()

	provider, model := resolveToolAdaptationProfileForConfig(cfg)
	if provider != "github-copilot" || model != "gpt-4.1" {
		t.Fatalf("profile = %s/%s, want github-copilot/gpt-4.1", provider, model)
	}
}

func TestBuildToolAdaptationResponseListsAccountRouterProfiles(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.AccountRef = "router-1"
	cfg.Agents.Defaults.ModelName = "coding"
	cfg.Tools.Adaptation = config.DefaultToolAdaptationConfig()
	cfg.ModelAliases = []config.ModelAliasConfig{{
		Name:  "coding",
		Model: "openrouter/old-account-model",
		AccountOverrides: map[string]string{
			"account-a-2": "openrouter/another-account-model",
			"account-b":   "anthropic/claude-sonnet",
		},
	}}
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "account-a",
			Provider:  "openrouter",
			Enabled:   true,
		},
		{
			ModelName: "account-a-2",
			Provider:  "openrouter",
			Enabled:   true,
		},
		{
			ModelName: "account-b",
			Provider:  "anthropic",
			Enabled:   true,
		},
	}
	cfg.AccountRouters = []config.AccountRouterConfig{{
		Name:    "router-1",
		Enabled: true,
		Entry:   "pool",
		Blocks: []config.AccountRouterBlock{{
			ID:       "pool",
			Type:     config.AccountRouterBlockTypeLoadBalance,
			Accounts: []string{"account-a", "account-a-2", "account-b"},
		}},
	}}
	cfg.MaterializeAccountRouterModels()

	resp := buildToolAdaptationResponse(cfg)
	if resp.Resolved.Provider != "openrouter" || resp.Resolved.Model != "old-account-model" {
		t.Fatalf(
			"resolved profile = %s/%s, want first effective provider/model",
			resp.Resolved.Provider,
			resp.Resolved.Model,
		)
	}
	if len(resp.Profiles) != 3 {
		t.Fatalf("profiles length = %d, want provider/model profiles only", len(resp.Profiles))
	}

	got := map[string]bool{}
	for _, profile := range resp.Profiles {
		got[profile.Resolved.Provider+"/"+profile.Resolved.Model] = true
		if profile.ProbeAccountRef == "" || profile.ProbeModelAlias != "coding" {
			t.Fatalf("profile lacks strict probe selection: %#v", profile)
		}
		if profile.Resolved.Provider == config.AccountRouterProvider {
			t.Fatalf("profile exposes account router as provider: %#v", profile)
		}
		if strings.Contains(profile.Label, "account-") || strings.Contains(profile.Source, "account-") {
			t.Fatalf("profile exposes account identity: %#v", profile)
		}
		if strings.Contains(profile.Source, "account router") {
			t.Fatalf("provider-collapsed profile exposes router mechanism: %#v", profile)
		}
	}
	for _, want := range []string{
		"openrouter/old-account-model",
		"openrouter/another-account-model",
		"anthropic/claude-sonnet",
	} {
		if !got[want] {
			t.Fatalf("profiles missing %q: %#v", want, resp.Profiles)
		}
	}
}

func TestBuildToolAdaptationResponseMarksProfileOverride(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.AccountRef = "primary"
	cfg.Agents.Defaults.ModelName = "primary-alias"
	cfg.ModelAliases = []config.ModelAliasConfig{{
		Name:  "primary-alias",
		Model: "gpt-test",
	}}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "primary",
		Provider:  "openai",
		Model:     "gpt-test",
		APIKeys:   config.SimpleSecureStrings("sk-test"),
		Enabled:   true,
	}}
	cfg.Tools.Adaptation = config.DefaultToolAdaptationConfig()
	cfg.Tools.Adaptation.ProfileOverrides = []config.ToolAdaptationProfileOverride{{
		Provider:           "gpt",
		Model:              "gpt-test",
		VisibleToolSurface: config.ToolSurfaceCodex,
	}, {
		Provider:           "openai",
		Model:              "gpt-test",
		VisibleToolSurface: config.ToolSurfaceSimple,
	}}

	resp := buildToolAdaptationResponse(cfg)
	if resp.ProfileOverrides == nil || len(*resp.ProfileOverrides) != 1 ||
		(*resp.ProfileOverrides)[0].Provider != "openai" {
		t.Fatalf("profile overrides = %#v, want canonical openai identity", resp.ProfileOverrides)
	}
	if len(resp.Profiles) != 1 {
		t.Fatalf("profiles = %#v, want one deduplicated profile", resp.Profiles)
	}
	profile := resp.Profiles[0]
	if !profile.IsOverride || !profile.ProbeAvailable {
		t.Fatalf("profile = %#v, want override and probe available", profile)
	}
	if profile.ProbeAccountRef != "primary" ||
		profile.ProbeModelAlias != "primary-alias" {
		t.Fatalf("profile probe target = %#v, want primary/primary-alias", profile)
	}
	if profile.Resolved.PinnedToolSurface != config.ToolSurfaceSimple {
		t.Fatalf(
			"PinnedToolSurface = %q, want profile override %q",
			profile.Resolved.PinnedToolSurface,
			config.ToolSurfaceSimple,
		)
	}
}

func TestHandleUpdateToolState(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Tools.Spawn.Enabled = false
	cfg.Tools.Subagent.Enabled = false
	cfg.Tools.Cron.Enabled = false
	cfg.Tools.Workflow.Enabled = false
	cfg.Workflows.Enabled = false
	cfg.Tools.MCP.Enabled = false
	cfg.Tools.MCP.Discovery.Enabled = false
	cfg.Tools.MCP.Discovery.UseRegex = false
	err = config.SaveConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPut,
		"/api/tools/spawn/state",
		bytes.NewBufferString(`{"enabled":true}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("spawn status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(
		http.MethodPut,
		"/api/tools/tool_search_tool_regex/state",
		bytes.NewBufferString(`{"enabled":true}`),
	)
	req2.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("regex status = %d, want %d, body=%s", rec2.Code, http.StatusOK, rec2.Body.String())
	}

	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(
		http.MethodPut,
		"/api/tools/cron/state",
		bytes.NewBufferString(`{"enabled":true}`),
	)
	req3.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("cron status = %d, want %d, body=%s", rec3.Code, http.StatusOK, rec3.Body.String())
	}

	updated, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig(updated) error = %v", err)
	}
	if !updated.Tools.Spawn.Enabled || !updated.Tools.Subagent.Enabled {
		t.Fatalf("spawn/subagent should both be enabled: %#v", updated.Tools)
	}
	if !updated.Tools.MCP.Enabled || !updated.Tools.MCP.Discovery.Enabled || !updated.Tools.MCP.Discovery.UseRegex {
		t.Fatalf("mcp regex discovery should be enabled: %#v", updated.Tools.MCP)
	}
	if !updated.Tools.Cron.Enabled {
		t.Fatalf("cron should be enabled: %#v", updated.Tools.Cron)
	}

	rec4 := httptest.NewRecorder()
	req4 := httptest.NewRequest(
		http.MethodPut,
		"/api/tools/serial/state",
		bytes.NewBufferString(`{"enabled":true}`),
	)
	req4.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec4, req4)
	if rec4.Code != http.StatusOK {
		t.Fatalf("serial status = %d, want %d, body=%s", rec4.Code, http.StatusOK, rec4.Body.String())
	}

	updated, err = config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig(updated serial) error = %v", err)
	}
	if !updated.Tools.Serial.Enabled {
		t.Fatalf("serial should be enabled: %#v", updated.Tools.Serial)
	}

	rec5 := httptest.NewRecorder()
	req5 := httptest.NewRequest(
		http.MethodPut,
		"/api/tools/workflow/state",
		bytes.NewBufferString(`{"enabled":true}`),
	)
	req5.Header.Set("Content-Type", "application/json; charset=utf-8")
	mux.ServeHTTP(rec5, req5)
	if rec5.Code != http.StatusOK || rec5.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Fatalf("workflow response = %d/%q", rec5.Code, rec5.Body.String())
	}
	if rec5.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("PUT tool state response did not disable caching")
	}
	updated, err = config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig(updated workflow) error = %v", err)
	}
	if !updated.Tools.Workflow.Enabled {
		t.Fatalf("workflow tool should be enabled: %#v", updated.Tools.Workflow)
	}
	if updated.Workflows.Enabled {
		t.Fatal("enabling the workflow tool must not enable workflow execution")
	}
}

func TestHandleUpdateToolStateRejectsNonStrictRequests(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	tests := []struct {
		name        string
		contentType string
		body        string
		mutate      func(*http.Request)
		wantStatus  int
	}{
		{
			name:       "missing content type",
			body:       `{"enabled":true}`,
			wantStatus: http.StatusUnsupportedMediaType,
		},
		{
			name:        "wrong content type",
			contentType: "text/plain",
			body:        `{"enabled":true}`,
			wantStatus:  http.StatusUnsupportedMediaType,
		},
		{
			name:        "duplicate content type",
			contentType: "application/json",
			body:        `{"enabled":true}`,
			mutate: func(request *http.Request) {
				request.Header["Content-Type"] = []string{
					"application/json",
					"application/json",
				}
			},
			wantStatus: http.StatusUnsupportedMediaType,
		},
		{
			name:        "empty object",
			contentType: "application/json",
			body:        `{}`,
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "null object",
			contentType: "application/json",
			body:        `null`,
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "scalar",
			contentType: "application/json",
			body:        `true`,
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "array",
			contentType: "application/json",
			body:        `[{"enabled":true}]`,
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "unknown field",
			contentType: "application/json",
			body:        `{"enabled":true,"other":false}`,
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "trailing value",
			contentType: "application/json",
			body:        `{"enabled":true}{}`,
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "duplicate enabled",
			contentType: "application/json",
			body:        `{"enabled":true,"enabled":false}`,
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "null enabled",
			contentType: "application/json",
			body:        `{"enabled":null}`,
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "string enabled",
			contentType: "application/json",
			body:        `{"enabled":"true"}`,
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "malformed",
			contentType: "application/json",
			body:        `{"enabled":true`,
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "empty body",
			contentType: "application/json",
			body:        ``,
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "oversized",
			contentType: "application/json",
			body: `{"enabled":true}` +
				strings.Repeat(" ", toolStateRequestMaxBytes),
			wantStatus: http.StatusRequestEntityTooLarge,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before, err := config.ConfigRevision(configPath)
			if err != nil {
				t.Fatalf("ConfigRevision(before) error = %v", err)
			}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(
				http.MethodPut,
				"/api/tools/cron/state",
				strings.NewReader(test.body),
			)
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			if test.mutate != nil {
				test.mutate(request)
			}
			mux.ServeHTTP(recorder, request)
			if recorder.Code != test.wantStatus {
				t.Fatalf(
					"status = %d, want %d, body=%s",
					recorder.Code,
					test.wantStatus,
					recorder.Body.String(),
				)
			}
			if recorder.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("rejected response did not disable caching")
			}
			after, err := config.ConfigRevision(configPath)
			if err != nil {
				t.Fatalf("ConfigRevision(after) error = %v", err)
			}
			if after != before {
				t.Fatalf("invalid request mutated config: %q -> %q", before, after)
			}
		})
	}
}

func TestHandleUpdateToolStateRejectsCASConflictWithoutOverwrite(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	cfg, err := config.LoadConfigForUpdate(configPath)
	if err != nil {
		t.Fatalf("LoadConfigForUpdate() error = %v", err)
	}
	cfg.Tools.Cron.Enabled = false
	cfg.Gateway.Port = 20001
	if saveErr := config.SaveConfig(configPath, cfg); saveErr != nil {
		t.Fatalf("SaveConfig() error = %v", saveErr)
	}

	h := NewHandler(configPath)
	originalSave := h.saveToolStateConfig
	h.saveToolStateConfig = func(
		path string,
		requested *config.Config,
		expectedRevision string,
	) (string, error) {
		external, revision, loadErr := config.LoadConfigForUpdateSnapshot(path)
		if loadErr != nil {
			return "", loadErr
		}
		external.Gateway.Port = 20002
		if _, saveErr := originalSave(path, external, revision); saveErr != nil {
			return "", saveErr
		}
		return originalSave(path, requested, expectedRevision)
	}

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPut,
		"/api/tools/cron/state",
		strings.NewReader(`{"enabled":true}`),
	)
	request.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict ||
		!strings.Contains(recorder.Body.String(), "config_revision_mismatch") {
		t.Fatalf("response = %d/%q", recorder.Code, recorder.Body.String())
	}

	saved, err := config.LoadConfigForUpdate(configPath)
	if err != nil {
		t.Fatalf("LoadConfigForUpdate(saved) error = %v", err)
	}
	if saved.Tools.Cron.Enabled {
		t.Fatal("stale tool-state writer overwrote the requested tool flag")
	}
	if saved.Gateway.Port != 20002 {
		t.Fatalf("external config update was lost: port = %d", saved.Gateway.Port)
	}
}

func TestHandleUpdateToolStateSerializesConcurrentUpdates(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	cfg, err := config.LoadConfigForUpdate(configPath)
	if err != nil {
		t.Fatalf("LoadConfigForUpdate() error = %v", err)
	}
	cfg.Tools.Cron.Enabled = false
	cfg.Tools.Workflow.Enabled = false
	if saveErr := config.SaveConfig(configPath, cfg); saveErr != nil {
		t.Fatalf("SaveConfig() error = %v", saveErr)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	start := make(chan struct{})
	results := make(chan int, 2)
	var wait sync.WaitGroup
	for _, name := range []string{"cron", "workflow"} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(
				http.MethodPut,
				"/api/tools/"+name+"/state",
				strings.NewReader(`{"enabled":true}`),
			)
			request.Header.Set("Content-Type", "application/json")
			mux.ServeHTTP(recorder, request)
			results <- recorder.Code
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	for status := range results {
		if status != http.StatusOK {
			t.Fatalf("concurrent update status = %d, want %d", status, http.StatusOK)
		}
	}

	saved, err := config.LoadConfigForUpdate(configPath)
	if err != nil {
		t.Fatalf("LoadConfigForUpdate(saved) error = %v", err)
	}
	if !saved.Tools.Cron.Enabled || !saved.Tools.Workflow.Enabled {
		t.Fatalf("concurrent updates were not both preserved: %#v", saved.Tools)
	}
}

func TestHandleThreadPolicyConfig(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/tools/thread-policy", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var initial config.ThreadPolicyConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &initial); err != nil {
		t.Fatalf("Unmarshal(GET) error = %v", err)
	}
	if !initial.Enabled || initial.Mode != config.ThreadPolicyModeTool {
		t.Fatalf("initial policy = %#v, want enabled tool", initial)
	}
	if len(initial.Rules) == 0 ||
		initial.Rules[0].MinMessages != 12 ||
		initial.Rules[0].MinTextChars != 6000 ||
		initial.Rules[0].ThresholdLogic != config.ThreadPolicyThresholdAny {
		t.Fatalf("initial policy rules = %#v, want threshold defaults", initial.Rules)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(
		http.MethodPut,
		"/api/tools/thread-policy",
		bytes.NewBufferString(`{
			"enabled": true,
			"mode": "suggest",
			"instructions": "Ask before moving deploy work.",
			"rules": [
				{
					"type": "coding",
					"description": "Move implementation requests into coding threads.",
					"min_messages": 8,
					"min_text_chars": 3000,
					"threshold_logic": "all"
				}
			]
		}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	updated, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig(updated) error = %v", err)
	}
	if updated.Tools.Threads.Policy.Mode != config.ThreadPolicyModeSuggest {
		t.Fatalf("mode = %q, want suggest", updated.Tools.Threads.Policy.Mode)
	}
	if updated.Tools.Threads.Policy.Instructions != "Ask before moving deploy work." {
		t.Fatalf("instructions = %q", updated.Tools.Threads.Policy.Instructions)
	}
	if len(updated.Tools.Threads.Policy.Rules) != 1 ||
		updated.Tools.Threads.Policy.Rules[0].Type != "coding" ||
		updated.Tools.Threads.Policy.Rules[0].MinMessages != 8 ||
		updated.Tools.Threads.Policy.Rules[0].MinTextChars != 3000 ||
		updated.Tools.Threads.Policy.Rules[0].ThresholdLogic != config.ThreadPolicyThresholdAll {
		t.Fatalf("rules = %#v", updated.Tools.Threads.Policy.Rules)
	}
}

func TestHandleListTools_ReportsWebSearchEnabledWhenToolIsOn(t *testing.T) {
	tests := []struct {
		name         string
		preferNative bool
	}{
		{name: "without prefer_native", preferNative: false},
		{name: "with prefer_native", preferNative: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configPath, cleanup := setupOAuthTestEnv(t)
			defer cleanup()

			cfg, err := config.LoadConfig(configPath)
			if err != nil {
				t.Fatalf("LoadConfig() error = %v", err)
			}
			cfg.Tools.Web.PreferNative = tt.preferNative
			cfg.Tools.Web.Provider = "brave"
			cfg.Tools.Web.Sogou.Enabled = false
			cfg.Tools.Web.DuckDuckGo.Enabled = false
			cfg.Tools.Web.Brave.Enabled = true
			cfg.Tools.Web.Brave.SetAPIKeys(nil)
			if err := config.SaveConfig(configPath, cfg); err != nil {
				t.Fatalf("SaveConfig() error = %v", err)
			}

			h := NewHandler(configPath)
			mux := http.NewServeMux()
			h.RegisterRoutes(mux)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/tools", nil)
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
			}

			var resp toolSupportResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}

			for _, tool := range resp.Tools {
				if tool.Name != "web_search" {
					continue
				}
				if tool.Status != "enabled" || tool.ReasonCode != "" {
					t.Fatalf("web_search = %#v, want enabled with no reason code", tool)
				}
				return
			}

			t.Fatal("expected web_search in response")
		})
	}
}

func TestHandleGetWebSearchConfig(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Tools.Web.PreferNative = false
	cfg.Tools.Web.Provider = "sogou"
	cfg.Tools.Web.Sogou.Enabled = true
	cfg.Tools.Web.Sogou.MaxResults = 6
	cfg.Tools.Web.Brave.Enabled = true
	cfg.Tools.Web.Brave.SetAPIKey("brave-test-key")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/tools/web-search-config", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp webSearchConfigResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if resp.Provider != "sogou" {
		t.Fatalf("provider = %q, want sogou", resp.Provider)
	}
	if resp.CurrentService != "sogou" {
		t.Fatalf("current_service = %q, want sogou", resp.CurrentService)
	}
	if !resp.Settings["brave"].APIKeySet {
		t.Fatalf("brave api_key_set should be true: %#v", resp.Settings["brave"])
	}
}

func TestHandleGetWebSearchConfig_DoesNotExposeNativeAsCurrentService(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Tools.Web.PreferNative = true
	cfg.Tools.Web.Provider = "brave"
	cfg.Tools.Web.Sogou.Enabled = false
	cfg.Tools.Web.DuckDuckGo.Enabled = false
	cfg.Tools.Web.Brave.Enabled = true
	cfg.Tools.Web.Brave.SetAPIKeys(nil)
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/tools/web-search-config", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp webSearchConfigResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if !resp.PreferNative {
		t.Fatal("prefer_native should remain true in response")
	}
	if resp.CurrentService != "" {
		t.Fatalf("current_service = %q, want empty when no external provider is ready", resp.CurrentService)
	}
}

func TestHandleUpdateWebSearchConfig(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Tools.Web.Brave.SetAPIKeys([]string{"brave-old-1", "brave-old-2"})
	if saveErr := config.SaveConfig(configPath, cfg); saveErr != nil {
		t.Fatalf("SaveConfig() error = %v", saveErr)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPut,
		"/api/tools/web-search-config",
		bytes.NewBufferString(`{
			"provider":"brave",
			"prefer_native":false,
			"proxy":"http://127.0.0.1:7890",
			"settings":{
				"sogou":{"enabled":true,"max_results":4},
				"brave":{"enabled":true,"max_results":7,"api_key":"brave-new-key"},
				"duckduckgo":{"enabled":false,"max_results":3}
			}
		}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	updated, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if updated.Tools.Web.Provider != "brave" {
		t.Fatalf("provider = %q, want brave", updated.Tools.Web.Provider)
	}
	if updated.Tools.Web.PreferNative {
		t.Fatal("prefer_native should be false after update")
	}
	if updated.Tools.Web.Proxy != "http://127.0.0.1:7890" {
		t.Fatalf("proxy = %q", updated.Tools.Web.Proxy)
	}
	if !updated.Tools.Web.Sogou.Enabled || updated.Tools.Web.Sogou.MaxResults != 4 {
		t.Fatalf("sogou config not updated: %#v", updated.Tools.Web.Sogou)
	}
	if !updated.Tools.Web.Brave.Enabled || updated.Tools.Web.Brave.MaxResults != 7 {
		t.Fatalf("brave config not updated: %#v", updated.Tools.Web.Brave)
	}
	if updated.Tools.Web.Brave.APIKey() != "brave-new-key" {
		t.Fatalf("brave api key not updated")
	}
}

func TestHandleUpdateWebSearchConfigRequiresExactModelAlias(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelAliases = []config.ModelAliasConfig{{
		Name:  "google-search",
		Model: "gemini/gemini-2.5-flash",
	}}
	if saveErr := config.SaveConfig(configPath, cfg); saveErr != nil {
		t.Fatalf("SaveConfig() error = %v", saveErr)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPut,
		"/api/tools/web-search-config",
		bytes.NewBufferString(`{
			"provider":"gemini",
			"settings":{
				"gemini":{
					"enabled":true,
					"api_key":"google-key",
					"model_alias":""
				}
			}
		}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "no model configured") {
		t.Fatalf("body = %q, want no model configured", rec.Body.String())
	}

	unchanged, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() after rejection error = %v", err)
	}
	if unchanged.Tools.Web.Gemini.Enabled {
		t.Fatal("invalid web search selection was persisted")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(
		http.MethodPut,
		"/api/tools/web-search-config",
		bytes.NewBufferString(`{
			"provider":"gemini",
			"settings":{
				"gemini":{
					"enabled":true,
					"api_key":"google-key",
					"model_alias":"google-search"
				}
			}
		}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	updated, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if !updated.Tools.Web.Gemini.Enabled ||
		updated.Tools.Web.Gemini.ModelAlias != "google-search" {
		t.Fatalf("gemini config = %#v", updated.Tools.Web.Gemini)
	}
}

func TestHandleUpdateWebSearchConfig_PreservesAndReplacesMultiKeys(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Tools.Web.Brave.SetAPIKeys([]string{"brave-old-1", "brave-old-2"})
	if saveErr := config.SaveConfig(configPath, cfg); saveErr != nil {
		t.Fatalf("SaveConfig() error = %v", saveErr)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPut,
		"/api/tools/web-search-config",
		bytes.NewBufferString(`{
			"provider":"auto",
			"prefer_native":true,
			"proxy":"",
			"settings":{
				"brave":{"enabled":true,"max_results":7}
			}
		}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	updated, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got := updated.Tools.Web.Brave.APIKeys.Values(); len(got) != 2 ||
		got[0] != "brave-old-1" || got[1] != "brave-old-2" {
		t.Fatalf("brave api keys should be preserved, got %#v", got)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(
		http.MethodPut,
		"/api/tools/web-search-config",
		bytes.NewBufferString(`{
			"provider":"auto",
			"prefer_native":true,
			"proxy":"",
			"settings":{
				"brave":{"enabled":true,"max_results":7,"api_keys":["brave-new-1","brave-new-2","brave-new-1"]}
			}
		}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	updated, err = config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got := updated.Tools.Web.Brave.APIKeys.Values(); len(got) != 2 ||
		got[0] != "brave-new-1" || got[1] != "brave-new-2" {
		t.Fatalf("brave api keys should be replaced by api_keys, got %#v", got)
	}
}

func TestResolveCurrentWebSearchProvider_PrefersConfiguredProvidersInAutoMode(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Web.Provider = "auto"
	cfg.Tools.Web.Sogou.Enabled = true
	cfg.Tools.Web.Brave.Enabled = true
	cfg.Tools.Web.Brave.SetAPIKey("brave-test-key")

	if got := resolveCurrentWebSearchProvider(cfg); got != "brave" {
		t.Fatalf("resolveCurrentWebSearchProvider() = %q, want brave", got)
	}
}

func TestResolveCurrentWebSearchProvider_FallsBackWhenExplicitProviderUnavailable(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Web.Provider = "brave"
	cfg.Tools.Web.Brave.Enabled = true
	cfg.Tools.Web.Sogou.Enabled = true

	if got := resolveCurrentWebSearchProvider(cfg); got != "sogou" {
		t.Fatalf("resolveCurrentWebSearchProvider() = %q, want sogou", got)
	}
}

func TestResolveCurrentWebSearchProvider_FallsBackWhenProviderIsUnknown(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Web.Provider = "totally_unknown"
	cfg.Tools.Web.Sogou.Enabled = true

	if got := resolveCurrentWebSearchProvider(cfg); got != "sogou" {
		t.Fatalf("resolveCurrentWebSearchProvider() = %q, want sogou", got)
	}
}

func TestResolveCurrentWebSearchProvider_PrefersStableDefaultForSogouAndDuckDuckGo(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Web.Provider = "auto"
	cfg.Tools.Web.Sogou.Enabled = true
	cfg.Tools.Web.DuckDuckGo.Enabled = true

	if got := resolveCurrentWebSearchProvider(cfg); got != "sogou" {
		t.Fatalf("resolveCurrentWebSearchProvider() = %q, want sogou", got)
	}
}

func TestResolveCurrentWebSearchProvider_IgnoresPreferNativeInConfigView(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "custom-default",
		Provider:  "openai",
		Model:     "gpt-4o",
		APIKeys:   config.SimpleSecureStrings("sk-default"),
		Enabled:   true,
	}}
	cfg.ModelAliases = []config.ModelAliasConfig{{
		Name:  "custom-default",
		Model: "openai/gpt-4o",
	}}
	cfg.Tools.Web.PreferNative = true
	cfg.Tools.Web.Provider = "brave"
	cfg.Tools.Web.Sogou.Enabled = false
	cfg.Tools.Web.DuckDuckGo.Enabled = false
	cfg.Tools.Web.Brave.Enabled = true

	if got := resolveCurrentWebSearchProvider(cfg); got != "" {
		t.Fatalf("resolveCurrentWebSearchProvider() = %q, want empty when only native search would be available", got)
	}
}
