package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestHandleGetChannelConfig_ReturnsSecretPresenceWithoutLeakingSecrets(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	bc := cfg.Channels[config.ChannelFeishu]
	bc.Enabled = true
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	bcfg := decoded.(*config.FeishuSettings)
	bcfg.AppID = "cli_test_app"
	bcfg.AppSecret = *config.NewSecureString("feishu-secret-from-security")
	bc.AllowFrom = config.FlexibleStringSlice{"ou_test_user"}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/channels/feishu/config", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf(
			"GET /api/channels/feishu/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}
	if strings.Contains(rec.Body.String(), "feishu-secret-from-security") {
		t.Fatalf("response leaked secret value: %s", rec.Body.String())
	}

	var resp struct {
		Config            map[string]any `json:"config"`
		ConfiguredSecrets []string       `json:"configured_secrets"`
		ConfigKey         string         `json:"config_key"`
		Variant           string         `json:"variant"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if got := resp.ConfigKey; got != "feishu" {
		t.Fatalf("config_key = %q, want %q", got, "feishu")
	}
	if got := resp.Config["app_id"]; got != "cli_test_app" {
		t.Fatalf("config.app_id = %#v, want %q", got, "cli_test_app")
	}
	if got := resp.Config["enabled"]; got != true {
		t.Fatalf("config.enabled = %#v, want true", got)
	}
	allowFrom, ok := resp.Config["allow_from"].([]any)
	if !ok || len(allowFrom) != 1 || allowFrom[0] != "ou_test_user" {
		t.Fatalf("config.allow_from = %#v, want [\"ou_test_user\"]", resp.Config["allow_from"])
	}
	if _, exists := resp.Config["app_secret"]; exists {
		t.Fatalf("config should omit app_secret, got %#v", resp.Config["app_secret"])
	}
	if len(resp.ConfiguredSecrets) != 1 || resp.ConfiguredSecrets[0] != "app_secret" {
		t.Fatalf("configured_secrets = %#v, want [\"app_secret\"]", resp.ConfiguredSecrets)
	}
}

func TestHandleListChannelCatalog_IncludesDeltaChat(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/channels/catalog", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf(
			"GET /api/channels/catalog status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}

	var resp struct {
		Channels []channelCatalogItem `json:"channels"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	for _, channel := range resp.Channels {
		if channel.Name != config.ChannelDeltaChat {
			continue
		}
		if channel.ConfigKey != config.ChannelDeltaChat {
			t.Fatalf("deltachat config_key = %q, want %q", channel.ConfigKey, config.ChannelDeltaChat)
		}
		if channel.DisplayName != "Delta Chat" {
			t.Fatalf("deltachat display_name = %q, want %q", channel.DisplayName, "Delta Chat")
		}
		return
	}
	t.Fatal("catalog does not include deltachat")
}

func TestHandleGetChannelConfig_DeltaChatRequiresEmailAndMasksPassword(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	channel := cfg.Channels.Get(config.ChannelDeltaChat)
	if channel == nil {
		t.Fatal("config is missing the default deltachat channel")
	}
	channel.Enabled = true
	decoded, err := channel.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	settings, ok := decoded.(*config.DeltaChatSettings)
	if !ok {
		t.Fatalf("GetDecoded() type = %T, want *config.DeltaChatSettings", decoded)
	}
	settings.Email = "operator@example.org"
	settings.Password = *config.NewSecureString("legacy-mailbox-password")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/channels/deltachat/config", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf(
			"GET /api/channels/deltachat/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}
	if strings.Contains(rec.Body.String(), "legacy-mailbox-password") {
		t.Fatalf("response leaked Delta Chat password: %s", rec.Body.String())
	}

	var resp struct {
		Config            map[string]any `json:"config"`
		ConfiguredSecrets []string       `json:"configured_secrets"`
		ConfigKey         string         `json:"config_key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if resp.ConfigKey != config.ChannelDeltaChat {
		t.Fatalf("config_key = %q, want %q", resp.ConfigKey, config.ChannelDeltaChat)
	}
	if got := resp.Config["email"]; got != "operator@example.org" {
		t.Fatalf("config.email = %#v, want %q", got, "operator@example.org")
	}
	if got := resp.Config["enabled"]; got != true {
		t.Fatalf("config.enabled = %#v, want true", got)
	}
	if _, exists := resp.Config["password"]; exists {
		t.Fatalf("config should omit password, got %#v", resp.Config["password"])
	}
	if len(resp.ConfiguredSecrets) != 1 || resp.ConfiguredSecrets[0] != "password" {
		t.Fatalf("configured_secrets = %#v, want [\"password\"]", resp.ConfiguredSecrets)
	}
}

func TestHandleGetChannelConfig_DeltaChatPreservesDisabledCommonControls(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	channel := cfg.Channels.Get(config.ChannelDeltaChat)
	if channel == nil {
		t.Fatal("config is missing the default deltachat channel")
	}
	channel.GroupTrigger.MentionOnly = false
	channel.GroupTrigger.Prefixes = nil
	channel.Typing.Enabled = false
	channel.Placeholder.Enabled = false
	channel.Placeholder.Text = nil
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/channels/deltachat/config", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf(
			"GET /api/channels/deltachat/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}

	var resp struct {
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	for field, booleanKey := range map[string]string{
		"group_trigger": "mention_only",
		"typing":        "enabled",
		"placeholder":   "enabled",
	} {
		projected, ok := resp.Config[field].(map[string]any)
		if !ok {
			t.Fatalf("config.%s = %#v, want object", field, resp.Config[field])
		}
		value, exists := projected[booleanKey]
		if !exists || value != false {
			t.Fatalf(
				"config.%s.%s = %#v (exists=%v), want explicit false",
				field,
				booleanKey,
				value,
				exists,
			)
		}
	}
}

func TestHandleGetChannelConfig_ReturnsNotFoundForUnknownChannel(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/channels/not-a-channel/config", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /api/channels/not-a-channel/config status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestHandleGetChannelConfig_ReturnsCommonFieldsWhenSettingsEmpty(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	bc := cfg.Channels[config.ChannelFeishu]
	bc.Enabled = true
	bc.AllowFrom = config.FlexibleStringSlice{"ou_common_user"}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/channels/feishu/config", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf(
			"GET /api/channels/feishu/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}

	var resp struct {
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got := resp.Config["enabled"]; got != true {
		t.Fatalf("config.enabled = %#v, want true", got)
	}
	allowFrom, ok := resp.Config["allow_from"].([]any)
	if !ok || len(allowFrom) != 1 || allowFrom[0] != "ou_common_user" {
		t.Fatalf("config.allow_from = %#v, want [\"ou_common_user\"]", resp.Config["allow_from"])
	}
}

func TestHandleGetChannelConfig_OmitsUnconfiguredStreaming(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/channels/telegram/config", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf(
			"GET /api/channels/telegram/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}

	var resp struct {
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if _, ok := resp.Config["streaming"]; ok {
		t.Fatalf("config.streaming = %#v, want omitted when not configured", resp.Config["streaming"])
	}
}

func TestHandleGetChannelConfig_ReturnsConfiguredStreaming(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	pico := cfg.Channels.Get(config.ChannelPico)
	if pico == nil {
		t.Fatal("missing pico channel")
	}
	pico.Settings = config.RawNode(`{"streaming":{"enabled":true,"throttle_seconds":2,"min_growth_chars":80}}`)
	if err := config.InitChannelList(cfg.Channels); err != nil {
		t.Fatalf("InitChannelList() error = %v", err)
	}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/channels/pico/config", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf(
			"GET /api/channels/pico/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}

	var resp struct {
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	streaming, ok := resp.Config["streaming"].(map[string]any)
	if !ok {
		t.Fatalf("config.streaming = %#v, want object", resp.Config["streaming"])
	}
	if got := streaming["enabled"]; got != true {
		t.Fatalf("config.streaming.enabled = %#v, want true", got)
	}
	if got := streaming["throttle_seconds"]; got != float64(2) {
		t.Fatalf("config.streaming.throttle_seconds = %#v, want 2", got)
	}
	if got := streaming["min_growth_chars"]; got != float64(80) {
		t.Fatalf("config.streaming.min_growth_chars = %#v, want 80", got)
	}
}

func TestHandleGetChannelConfig_ReturnsDefaultShapeForMissingChannel(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	delete(cfg.Channels, config.ChannelIRC)
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/channels/irc/config", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf(
			"GET /api/channels/irc/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}

	var resp struct {
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got := resp.Config["server"]; got != "" {
		t.Fatalf("config.server = %#v, want empty string", got)
	}
	if got := resp.Config["nick"]; got != "picoclaw" {
		t.Fatalf("config.nick = %#v, want %q", got, "picoclaw")
	}
	if got := resp.Config["enabled"]; got != false {
		t.Fatalf("config.enabled = %#v, want false", got)
	}
}
