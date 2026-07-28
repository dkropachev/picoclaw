package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
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
}

func TestHandleToolAdaptationRoundTrip(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

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
	if getResp.Resolved == nil {
		t.Fatal("GET response Resolved = nil, want resolved state")
	}
	if getResp.Resolved.PinnedToolSurface != "codex" {
		t.Fatalf("resolved pinned surface = %q, want codex", getResp.Resolved.PinnedToolSurface)
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
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
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

func TestProbeModelConfigForConfigUsesConfiguredModelEntry(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = "alias"
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "alias",
		Provider:  "openai",
		Model:     "gpt-test",
		APIBase:   "http://127.0.0.1:1/v1",
	}}

	got := probeModelConfigForConfig(cfg, "fallback", "fallback-model")
	if got.Provider != "openai" || got.Model != "gpt-test" || got.APIBase == "" {
		t.Fatalf("probe model config = %#v, want configured model entry", got)
	}
}

func TestResolveToolAdaptationProfileForConfigSplitsPrefixedAlias(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Provider = "openai"
	cfg.Agents.Defaults.ModelName = "alias"
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "alias",
		Model:     "anthropic/claude-sonnet",
	}}

	provider, model := resolveToolAdaptationProfileForConfig(cfg)
	if provider != "anthropic" || model != "claude-sonnet" {
		t.Fatalf("profile = %s/%s, want anthropic/claude-sonnet", provider, model)
	}
}

func TestBuildToolAdaptationResponseListsAccountRouterProfiles(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Provider = config.AccountRouterProvider
	cfg.Agents.Defaults.ModelName = "router-1"
	cfg.Tools.Adaptation = config.DefaultToolAdaptationConfig()
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "account-a",
			Provider:  "openrouter",
			Model:     "old-account-model",
			Enabled:   true,
		},
		{
			ModelName: "account-a-2",
			Provider:  "openrouter",
			Model:     "another-account-model",
			Enabled:   true,
		},
		{
			ModelName: "account-b",
			Model:     "anthropic/claude-sonnet",
			Enabled:   true,
		},
	}
	cfg.AccountRouters = []config.AccountRouterConfig{{
		Name:    "router-1",
		Model:   "gpt-5.4",
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
	if resp.Resolved.Provider != "openrouter" || resp.Resolved.Model != "gpt-5.4" {
		t.Fatalf(
			"resolved profile = %s/%s, want first effective provider/model",
			resp.Resolved.Provider,
			resp.Resolved.Model,
		)
	}
	if len(resp.Profiles) != 2 {
		t.Fatalf("profiles length = %d, want provider/model profiles only", len(resp.Profiles))
	}

	got := map[string]bool{}
	for _, profile := range resp.Profiles {
		got[profile.Resolved.Provider+"/"+profile.Resolved.Model] = true
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
		"openrouter/gpt-5.4",
		"anthropic/gpt-5.4",
	} {
		if !got[want] {
			t.Fatalf("profiles missing %q: %#v", want, resp.Profiles)
		}
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
		Model:     "openai/gpt-4o",
		APIKeys:   config.SimpleSecureStrings("sk-default"),
	}}
	cfg.Agents.Defaults.ModelName = "custom-default"
	cfg.Tools.Web.PreferNative = true
	cfg.Tools.Web.Provider = "brave"
	cfg.Tools.Web.Sogou.Enabled = false
	cfg.Tools.Web.DuckDuckGo.Enabled = false
	cfg.Tools.Web.Brave.Enabled = true

	if got := resolveCurrentWebSearchProvider(cfg); got != "" {
		t.Fatalf("resolveCurrentWebSearchProvider() = %q, want empty when only native search would be available", got)
	}
}
