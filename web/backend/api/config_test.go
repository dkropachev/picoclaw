package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)

func TestValidateConfigRejectsInvalidCommonChannelSettings(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = append(cfg.ModelList, nil)
	cfg.Gateway.Port = -1

	pico := cfg.Channels[config.ChannelPico]
	pico.Enabled = true
	decodedPico, err := pico.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() pico error = %v", err)
	}
	picoSettings := decodedPico.(*config.PicoSettings)
	picoSettings.Token = *config.NewSecureString("")
	picoSettings.Streaming.MinGrowthChars = -1

	telegram := cfg.Channels[config.ChannelTelegram]
	telegram.Enabled = true
	decodedTelegram, err := telegram.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() telegram error = %v", err)
	}
	decodedTelegram.(*config.TelegramSettings).Token = *config.NewSecureString("")

	discord := cfg.Channels[config.ChannelDiscord]
	discord.Enabled = true
	decodedDiscord, err := discord.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() discord error = %v", err)
	}
	decodedDiscord.(*config.DiscordSettings).Token = *config.NewSecureString("")

	wecom := cfg.Channels[config.ChannelWeCom]
	wecom.Enabled = true
	decodedWeCom, err := wecom.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() WeCom error = %v", err)
	}
	wecomSettings := decodedWeCom.(*config.WeComSettings)
	wecomSettings.BotID = ""
	wecomSettings.Secret = *config.NewSecureString("")

	errs := validateConfig(cfg)
	for _, want := range []string{
		"model_list[0]: model config is required",
		"gateway.port -1 is out of valid range",
		"streaming.min_growth_chars must be >= 0",
		"channels.pico.token is required",
		"channels.telegram.token is required",
		"channels.discord.token is required",
		"channels.wecom.bot_id is required",
		"channels.wecom.secret is required",
	} {
		found := false
		for _, got := range errs {
			if strings.Contains(got, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("validateConfig() errors = %#v, want error containing %q", errs, want)
		}
	}
}

func TestValidateConfigRejectsInvalidIsolationEnvironmentAllowlist(t *testing.T) {
	for _, allowlist := range [][]string{
		{"BAD-NAME"},
		{"PATH", "path"},
		func() []string {
			values := make([]string, 129)
			for index := range values {
				values[index] = fmt.Sprintf("P014_VALUE_%03d", index)
			}
			return values
		}(),
	} {
		cfg := config.DefaultConfig()
		cfg.Isolation.EnvironmentAllowlist = allowlist
		errs := validateConfig(cfg)
		found := false
		for _, got := range errs {
			if strings.Contains(got, "isolation:") &&
				strings.Contains(got, "environment_allowlist") {
				found = true
			}
		}
		if !found {
			t.Fatalf("validateConfig(%#v) errors = %#v", allowlist, errs)
		}
	}
}

func TestConfigMutationRejectsInvalidIsolationEnvironmentAllowlist(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		var body []byte
		if method == http.MethodPatch {
			body = []byte(`{"isolation":{"environment_allowlist":["PATH","path"]}}`)
		} else {
			cfg, err := config.LoadConfig(configPath)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Isolation.EnvironmentAllowlist = []string{"PATH", "path"}
			body, err = json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
		}
		req := httptest.NewRequest(method, "/api/config", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest ||
			!strings.Contains(rec.Body.String(), "validation_error") ||
			!strings.Contains(rec.Body.String(), "environment_allowlist") {
			t.Fatalf("%s invalid isolation response = %d %s", method, rec.Code, rec.Body.String())
		}
	}
}

func TestConfigPatchPreservesIsolationAllowlistStates(t *testing.T) {
	tests := []struct {
		name      string
		initial   []string
		patch     string
		wantNil   bool
		wantEmpty bool
		want      []string
	}{
		{
			name:    "omitted preserves custom",
			initial: []string{"PATH", "P014_CUSTOM"},
			patch:   `{"gateway":{"log_level":"info"}}`,
			want:    []string{"PATH", "P014_CUSTOM"},
		},
		{
			name:      "explicit empty",
			initial:   []string{"PATH"},
			patch:     `{"isolation":{"environment_allowlist":[]}}`,
			wantEmpty: true,
		},
		{
			name:    "null selects defaults",
			initial: []string{"PATH"},
			patch:   `{"isolation":{"environment_allowlist":null}}`,
			wantNil: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configPath, cleanup := setupOAuthTestEnv(t)
			defer cleanup()
			cfg, err := config.LoadConfig(configPath)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Isolation.EnvironmentAllowlist = append([]string(nil), test.initial...)
			if err = config.SaveConfig(configPath, cfg); err != nil {
				t.Fatal(err)
			}
			h := NewHandler(configPath)
			mux := http.NewServeMux()
			h.RegisterRoutes(mux)
			req := httptest.NewRequest(http.MethodPatch, "/api/config", strings.NewReader(test.patch))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("PATCH response = %d %s", rec.Code, rec.Body.String())
			}
			loaded, err := config.LoadConfig(configPath)
			if err != nil {
				t.Fatal(err)
			}
			got := loaded.Isolation.EnvironmentAllowlist
			switch {
			case test.wantNil && got != nil:
				t.Fatalf("allowlist = %#v, want nil/default", got)
			case test.wantEmpty && (got == nil || len(got) != 0):
				t.Fatalf("allowlist = %#v, want allocated empty", got)
			case test.want != nil && !reflect.DeepEqual(got, test.want):
				t.Fatalf("allowlist = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestMergeMapDeletesNullValues(t *testing.T) {
	dst := map[string]any{
		"keep":   "value",
		"remove": "value",
	}

	mergeMap(dst, map[string]any{"remove": nil})

	if _, exists := dst["remove"]; exists {
		t.Fatalf("mergeMap() result = %#v, want remove key deleted", dst)
	}
	if got := dst["keep"]; got != "value" {
		t.Fatalf("mergeMap() keep = %#v, want value", got)
	}
}

func TestBroadConfigWritesRejectDevelopmentScopedField(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			response := performConfigAPIRequest(
				t, configPath, method, []byte(`{"development":{}}`),
			)
			if response.Code != http.StatusBadRequest ||
				!strings.Contains(response.Body.String(), "dedicated scoped API") {
				t.Fatalf("%s status=%d body=%s", method, response.Code, response.Body.String())
			}
		})
	}
}

func TestBroadConfigPutAcceptsUnchangedDevelopmentScopedField(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	existing, err := config.LoadConfigForUpdate(configPath)
	if err != nil {
		t.Fatalf("LoadConfigForUpdate() error = %v", err)
	}
	existing.PRLifecycle.Nudge.ReviewMinimumAdditional = 3
	if err = config.SaveConfig(configPath, existing); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	requestConfig, err := config.LoadConfigForUpdate(configPath)
	if err != nil {
		t.Fatalf("LoadConfigForUpdate(request) error = %v", err)
	}
	requestConfig.Gateway.LogLevel = "debug"
	body, err := json.Marshal(requestConfig)
	if err != nil {
		t.Fatalf("Marshal(request config) error = %v", err)
	}

	response := performConfigAPIRequest(t, configPath, http.MethodPut, body)
	if response.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", response.Code, response.Body.String())
	}
	updated, err := config.LoadConfigForUpdate(configPath)
	if err != nil {
		t.Fatalf("LoadConfigForUpdate(updated) error = %v", err)
	}
	if updated.PRLifecycle.Nudge.ReviewMinimumAdditional != 3 {
		t.Fatalf(
			"development nudge minimum = %d, want preserved value 3",
			updated.PRLifecycle.Nudge.ReviewMinimumAdditional,
		)
	}
	if updated.Gateway.LogLevel != "debug" {
		t.Fatalf("gateway log level = %q, want debug", updated.Gateway.LogLevel)
	}
}

func TestNormalizeChannelArrayFieldsIgnoresNonApplicableEntries(t *testing.T) {
	channels := map[string]any{
		"scalar": "leave-me-alone",
		"without-settings": map[string]any{
			"type": config.ChannelTelegram,
		},
		"unknown": map[string]any{
			"type": "unknown-transport",
			"settings": map[string]any{
				"unchanged": "value",
			},
		},
	}
	raw := map[string]any{"channel_list": channels}

	if err := normalizeChannelArrayFields(raw); err != nil {
		t.Fatalf("normalizeChannelArrayFields() error = %v", err)
	}
	if got := channels["scalar"]; got != "leave-me-alone" {
		t.Fatalf("scalar channel = %#v, want unchanged value", got)
	}
	unknown := channels["unknown"].(map[string]any)
	settings := unknown["settings"].(map[string]any)
	if got := settings["unchanged"]; got != "value" {
		t.Fatalf("unknown channel settings = %#v, want unchanged value", got)
	}
}

func TestHandleGetConfigMasksUnresolvableEventWebhookSecret(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	configData := []byte(`{
		"version": 5,
		"events": {
			"ingress": {
				"enabled": true,
				"webhooks": {
					"deploy": {
						"enabled": true,
						"secret": "[NOT_HERE]"
					}
				}
			}
		}
	}`)
	if err := os.WriteFile(configPath, configData, 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
	securityData := []byte(`events:
  ingress:
    webhooks:
      deploy:
        secret: file://missing-webhook-secret.txt
`)
	if err := os.WriteFile(
		filepath.Join(dir, config.SecurityConfigFile),
		securityData,
		0o600,
	); err != nil {
		t.Fatalf("write .security.yml: %v", err)
	}

	if _, err := config.LoadConfig(configPath); err == nil {
		t.Fatal("LoadConfig() error = nil, want unresolved active secret error")
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf(
			"GET /api/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}
	if strings.Contains(rec.Body.String(), "missing-webhook-secret") {
		t.Fatal("GET /api/config exposed the unresolved webhook secret reference")
	}
	if !strings.Contains(rec.Body.String(), `"secret":"[NOT_HERE]"`) {
		t.Fatalf("GET /api/config body=%s, want masked secret marker", rec.Body.String())
	}
}

func TestHandleGetConfigRejectsCredentialBearingEventPublicIdentity(t *testing.T) {
	const secret = "sk-default"

	tests := []struct {
		name    string
		ingress config.EventIngressConfig
	}{
		{
			name: "webhook name",
			ingress: config.EventIngressConfig{
				Webhooks: map[string]config.GenericWebhookConfig{
					"hook-" + secret: {},
				},
			},
		},
		{
			name: "webhook format",
			ingress: config.EventIngressConfig{
				Webhooks: map[string]config.GenericWebhookConfig{
					"hook": {Format: "format-" + secret},
				},
			},
		},
		{
			name: "channel name",
			ingress: config.EventIngressConfig{
				Channels: map[string]config.ChannelEventIngressConfig{
					"channel-" + secret: {},
				},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configPath, cleanup := setupOAuthTestEnv(t)
			defer cleanup()

			cfg, err := config.LoadConfigForUpdate(configPath)
			if err != nil {
				t.Fatalf("LoadConfigForUpdate() error = %v", err)
			}
			cfg.Events.Ingress = test.ingress
			operational, err := json.MarshalIndent(cfg, "", "  ")
			if err != nil {
				t.Fatalf("marshal corrupt operational config: %v", err)
			}
			if err = os.WriteFile(configPath, operational, 0o600); err != nil {
				t.Fatalf("write corrupt operational config: %v", err)
			}

			h := NewHandler(configPath)
			mux := http.NewServeMux()
			h.RegisterRoutes(mux)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(
				rec,
				httptest.NewRequest(http.MethodGet, "/api/config", nil),
			)

			if rec.Code != http.StatusInternalServerError {
				t.Fatalf(
					"GET /api/config status = %d, want %d, body=%s",
					rec.Code,
					http.StatusInternalServerError,
					rec.Body.String(),
				)
			}
			if strings.Contains(rec.Body.String(), secret) {
				t.Fatal("GET /api/config exposed a configured credential")
			}
			if rec.Body.String() != "Failed to project config safely\n" {
				t.Fatalf(
					"GET /api/config body = %q, want opaque projection failure",
					rec.Body.String(),
				)
			}
		})
	}
}

func TestConfigAPIRepairsActiveAdapterWithDisabledChannel(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	configData := []byte(`{
		"version": 5,
		"events": {
			"ingress": {
				"enabled": true,
				"channels": {
					"deltachat": {
						"enabled": true,
						"source": "email",
						"mode": "mirror"
					}
				}
			}
		}
	}`)
	if err := os.WriteFile(configPath, configData, 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	if _, err := config.LoadConfig(configPath); err == nil {
		t.Fatal("LoadConfig() error = nil, want disabled channel dependency error")
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	getRec := httptest.NewRecorder()
	mux.ServeHTTP(
		getRec,
		httptest.NewRequest(http.MethodGet, "/api/config", nil),
	)
	if getRec.Code != http.StatusOK {
		t.Fatalf(
			"GET /api/config status = %d, want %d, body=%s",
			getRec.Code,
			http.StatusOK,
			getRec.Body.String(),
		)
	}

	patchBody := bytes.NewBufferString(`{
		"events": {
			"ingress": {
				"channels": {
					"deltachat": {
						"enabled": false
					}
				}
			}
		}
	}`)
	patchReq := httptest.NewRequest(http.MethodPatch, "/api/config", patchBody)
	patchReq.Header.Set("Content-Type", "application/json")
	patchRec := httptest.NewRecorder()
	mux.ServeHTTP(patchRec, patchReq)
	if patchRec.Code != http.StatusOK {
		t.Fatalf(
			"PATCH /api/config status = %d, want %d, body=%s",
			patchRec.Code,
			http.StatusOK,
			patchRec.Body.String(),
		)
	}

	repaired, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig(repaired) error = %v", err)
	}
	if repaired.Events.Ingress.Channels["deltachat"].Enabled {
		t.Fatal("repaired Delta Chat event adapter is still enabled")
	}
}

func TestHandlePatchConfig_PreservesTurnProfile(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Agents.Defaults.TurnProfile = config.TurnProfileConfig{
		Enabled:      true,
		History:      config.TurnProfileBlock{Mode: config.TurnProfileModeOff},
		SystemPrompt: config.TurnProfileBlock{Mode: config.TurnProfileModeOff},
		Skills:       config.TurnProfileBlock{Mode: config.TurnProfileModeOff},
		Tools: config.TurnProfileBlock{
			Mode:  config.TurnProfileModeCustom,
			Allow: []string{"web_search", "web_fetch"},
		},
	}
	if saveErr := config.SaveConfig(configPath, cfg); saveErr != nil {
		t.Fatalf("SaveConfig() error = %v", saveErr)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"agents": {
			"defaults": {
				"max_tokens": 1234
			}
		}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	updated, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig(updated) error = %v", err)
	}
	profile := updated.Agents.Defaults.TurnProfile
	if profile.Tools.Mode != config.TurnProfileModeCustom ||
		strings.Join(profile.Tools.Allow, ",") != "web_search,web_fetch" {
		t.Fatalf("profile tools = %#v, want custom web_search/web_fetch", profile.Tools)
	}
}

func TestHandlePatchConfig_RejectsInvalidTurnProfile(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"agents": {
			"defaults": {
				"turn_profile": {
					"enabled": true,
					"history": { "mode": "custom" }
				}
			}
		}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf(
			"status = %d, want %d, body=%s",
			rec.Code,
			http.StatusBadRequest,
			rec.Body.String(),
		)
	}
	if !strings.Contains(rec.Body.String(), "history.mode custom is not supported") {
		t.Fatalf("body=%s, want turn profile validation error", rec.Body.String())
	}

	if _, err := config.LoadConfig(configPath); err != nil {
		t.Fatalf("LoadConfig() after rejected patch error = %v", err)
	}
}

func assertGatewayLogLevelApplied(t *testing.T, method, body string, want logger.LogLevel) {
	t.Helper()

	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	initialLevel := logger.GetLevel()
	logger.SetLevel(logger.INFO)
	t.Cleanup(func() {
		logger.SetLevel(initialLevel)
	})

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(method, "/api/config", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf(
			"%s /api/config status = %d, want %d, body=%s",
			method,
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}
	if got := logger.GetLevel(); got != want {
		t.Fatalf("logger.GetLevel() = %v, want %v", got, want)
	}
}

func TestHandleUpdateConfig_PreservesExecAllowRemoteDefaultWhenOmitted(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewBufferString(`{
"version": 5,
		"agents": {
			"defaults": {
				"workspace": "~/.picoclaw/workspace"
			}
		},
		"model_list": [
			{
				"model_name": "custom-default",
				"model": "openai/gpt-4o",
				"api_keys": ["sk-default"]
			}
		]
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if !cfg.Tools.Exec.AllowRemote {
		t.Fatal("tools.exec.allow_remote should remain true when omitted from PUT /api/config")
	}
}

func TestHandleUpdateConfig_DoesNotInheritDefaultModelFields(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewBufferString(`{
		"agents": {
			"defaults": {
				"workspace": "~/.picoclaw/workspace"
			}
		},
		"model_list": [
			{
				"model_name": "custom-default",
				"model": "openai/gpt-4o",
				"api_key": "sk-default"
			}
		]
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got := cfg.ModelList[0].APIBase; got != "" {
		t.Fatalf("model_list[0].api_base = %q, want empty string", got)
	}
}

func TestHandleUpdateConfig_AppliesModelAPIKeysFromPayload(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewBufferString(`{
		"version": 5,
		"agents": {
			"defaults": {
				"workspace": "~/.picoclaw/workspace",
				"account_ref": "custom-default",
				"model_name": "custom-default"
			}
		},
		"model_aliases": [
			{
				"name": "custom-default",
				"model": "openai/gpt-4o"
			}
		],
		"model_list": [
			{
				"model_name": "custom-default",
				"provider": "openai",
				"model": "openai/gpt-4o",
				"api_keys": ["sk-updated"],
				"enabled": true
			}
		]
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got := cfg.ModelList[0].APIKey(); got != "sk-updated" {
		t.Fatalf("model_list[0].APIKey() = %q, want %q", got, "sk-updated")
	}
}

func TestHandleUpdateConfig_PreservesModelAPIKeyWhenModelNameChanges(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewBufferString(`{
		"version": 5,
		"agents": {
			"defaults": {
				"workspace": "~/.picoclaw/workspace",
				"account_ref": "renamed-default",
				"model_name": "renamed-default"
			}
		},
		"model_aliases": [
			{
				"name": "renamed-default",
				"model": "openai/gpt-4o"
			}
		],
		"model_list": [
			{
				"model_name": "renamed-default",
				"provider": "openai",
				"model": "gpt-4o",
				"enabled": true
			}
		]
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got := cfg.ModelList[0].APIKey(); got != "sk-default" {
		t.Fatalf("model_list[0].APIKey() = %q, want %q", got, "sk-default")
	}
}

func TestHandlePatchConfig_RejectsInvalidExecRegexPatterns(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"tools": {
			"exec": {
				"custom_deny_patterns": ["("]
			}
		}
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf(
			"status = %d, want %d, body=%s",
			rec.Code,
			http.StatusBadRequest,
			rec.Body.String(),
		)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("custom_deny_patterns")) {
		t.Fatalf(
			"expected validation error mentioning custom_deny_patterns, body=%s",
			rec.Body.String(),
		)
	}
}

func TestHandlePatchConfig_AllowsInvalidExecRegexPatternsWhenExecDisabled(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"tools": {
			"exec": {
				"enabled": false,
				"custom_deny_patterns": ["("],
				"custom_allow_patterns": ["("]
			}
		}
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestHandlePatchConfig_SavesChannelListSettingsPatch(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"channel_list": {
			"feishu": {
				"enabled": true,
				"allow_from": ["ou_patch_user"],
				"settings": {
					"app_id": "cli_patch_app",
					"app_secret": "patch-secret",
					"is_lark": true
				}
			}
		}
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf(
			"PATCH /api/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	bc := cfg.Channels[config.ChannelFeishu]
	if !bc.Enabled {
		t.Fatal("feishu should be enabled after PATCH")
	}
	if len(bc.AllowFrom) != 1 || bc.AllowFrom[0] != "ou_patch_user" {
		t.Fatalf("feishu allow_from = %#v, want [\"ou_patch_user\"]", bc.AllowFrom)
	}
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	feishuCfg := decoded.(*config.FeishuSettings)
	if got := feishuCfg.AppID; got != "cli_patch_app" {
		t.Fatalf("feishu app_id = %q, want %q", got, "cli_patch_app")
	}
	if got := feishuCfg.AppSecret.String(); got != "patch-secret" {
		t.Fatalf("feishu app_secret = %q, want %q", got, "patch-secret")
	}
	if !feishuCfg.IsLark {
		t.Fatal("feishu is_lark should be true after PATCH")
	}
}

func TestHandlePatchConfig_NormalizesStringChannelArrayFields(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"channel_list": {
			"pico": {
				"type": "pico",
				"allow_from": " ou_a\u200b，\u2060ou_b\tou_c\u202e，ou_a ",
				"group_trigger": {
					"prefixes": "/，!;\n?，/"
				},
				"settings": {
					"allow_origins": "https://a.example.com，http://localhost:5173，https://a.example.com"
				}
			},
			"irc": {
				"type": "irc",
				"settings": {
					"channels": "#ops,\n#dev,\n#ops",
					"request_caps": "multi-prefix，echo-message\tbatch，multi-prefix"
				}
			}
		}
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf(
			"PATCH /api/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	picoChannel := cfg.Channels[config.ChannelPico]
	if len(picoChannel.AllowFrom) != 3 ||
		picoChannel.AllowFrom[0] != "ou_a" ||
		picoChannel.AllowFrom[1] != "ou_b" ||
		picoChannel.AllowFrom[2] != "ou_c" {
		t.Fatalf(
			"pico allow_from = %#v, want [\"ou_a\", \"ou_b\", \"ou_c\"]",
			picoChannel.AllowFrom,
		)
	}
	if len(picoChannel.GroupTrigger.Prefixes) != 3 ||
		picoChannel.GroupTrigger.Prefixes[0] != "/" ||
		picoChannel.GroupTrigger.Prefixes[1] != "!;" ||
		picoChannel.GroupTrigger.Prefixes[2] != "?" {
		t.Fatalf(
			"pico group_trigger.prefixes = %#v, want [\"/\", \"!;\", \"?\"]",
			picoChannel.GroupTrigger.Prefixes,
		)
	}

	decoded, err := picoChannel.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() pico error = %v", err)
	}
	picoCfg := decoded.(*config.PicoSettings)
	if len(picoCfg.AllowOrigins) != 2 ||
		picoCfg.AllowOrigins[0] != "https://a.example.com" ||
		picoCfg.AllowOrigins[1] != "http://localhost:5173" {
		t.Fatalf(
			"pico allow_origins = %#v, want [\"https://a.example.com\", \"http://localhost:5173\"]",
			picoCfg.AllowOrigins,
		)
	}

	ircChannel := cfg.Channels[config.ChannelIRC]
	decoded, err = ircChannel.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() irc error = %v", err)
	}
	ircCfg := decoded.(*config.IRCSettings)
	if len(ircCfg.Channels) != 2 ||
		ircCfg.Channels[0] != "#ops" ||
		ircCfg.Channels[1] != "#dev" {
		t.Fatalf("irc channels = %#v, want [\"#ops\", \"#dev\"]", ircCfg.Channels)
	}
	if len(ircCfg.RequestCaps) != 3 ||
		ircCfg.RequestCaps[0] != "multi-prefix" ||
		ircCfg.RequestCaps[1] != "echo-message" ||
		ircCfg.RequestCaps[2] != "batch" {
		t.Fatalf(
			"irc request_caps = %#v, want [\"multi-prefix\", \"echo-message\", \"batch\"]",
			ircCfg.RequestCaps,
		)
	}
}

func TestHandlePatchConfig_NormalizesSingleNumericAllowFrom(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"channel_list": {
			"telegram": {
				"type": "telegram",
				"allow_from": 123456
			}
		}
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf(
			"PATCH /api/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	telegramChannel := cfg.Channels[config.ChannelTelegram]
	if len(telegramChannel.AllowFrom) != 1 || telegramChannel.AllowFrom[0] != "123456" {
		t.Fatalf("telegram allow_from = %#v, want [\"123456\"]", telegramChannel.AllowFrom)
	}
}

func TestHandlePatchConfig_RejectsInvalidChannelArrayFields(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	telegramChannel := cfg.Channels[config.ChannelTelegram]
	telegramChannel.AllowFrom = config.FlexibleStringSlice{"existing-user"}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	tests := []struct {
		name string
		body string
	}{
		{
			name: "object allow_from",
			body: `{
				"channel_list": {
					"telegram": {
						"type": "telegram",
						"allow_from": {"id": "bad"}
					}
				}
			}`,
		},
		{
			name: "boolean allow_from",
			body: `{
				"channel_list": {
					"telegram": {
						"type": "telegram",
						"allow_from": true
					}
				}
			}`,
		},
		{
			name: "object settings array",
			body: `{
				"channel_list": {
					"irc": {
						"type": "irc",
						"settings": {
							"channels": {"name": "#ops"}
						}
					}
				}
			}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandler(configPath)
			mux := http.NewServeMux()
			h.RegisterRoutes(mux)

			req := httptest.NewRequest(
				http.MethodPatch,
				"/api/config",
				bytes.NewBufferString(tt.body),
			)
			req.Header.Set("Content-Type", "application/json")

			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf(
					"PATCH /api/config status = %d, want %d, body=%s",
					rec.Code,
					http.StatusBadRequest,
					rec.Body.String(),
				)
			}

			cfg, err := config.LoadConfig(configPath)
			if err != nil {
				t.Fatalf("LoadConfig() error = %v", err)
			}
			telegramChannel := cfg.Channels[config.ChannelTelegram]
			if len(telegramChannel.AllowFrom) != 1 ||
				telegramChannel.AllowFrom[0] != "existing-user" {
				t.Fatalf(
					"telegram allow_from = %#v, want unchanged [\"existing-user\"]",
					telegramChannel.AllowFrom,
				)
			}
		})
	}
}

func TestHandlePatchConfig_RejectsNegativeStreamingDeliveryValues(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"channel_list": {
			"pico": {
				"settings": {
					"streaming": {
						"enabled": true,
						"throttle_seconds": -1
					}
				}
			}
		}
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf(
			"PATCH /api/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusBadRequest,
			rec.Body.String(),
		)
	}
	if !strings.Contains(rec.Body.String(), "streaming.throttle_seconds") {
		t.Fatalf(
			"response body = %q, want streaming.throttle_seconds validation error",
			rec.Body.String(),
		)
	}
}

func TestHandlePatchConfig_ClearingAllowFromDoesNotLeaveEmptyStringItem(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	feishuChannel := cfg.Channels[config.ChannelFeishu]
	feishuChannel.Enabled = true
	feishuChannel.AllowFrom = config.FlexibleStringSlice{"ou_existing_user"}
	decoded, err := feishuChannel.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	feishuCfg := decoded.(*config.FeishuSettings)
	feishuCfg.AppID = "cli_existing_app"
	feishuCfg.AppSecret = *config.NewSecureString("existing-secret")
	if err = config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"channel_list": {
			"feishu": {
				"enabled": true,
				"allow_from": "",
				"settings": {
					"app_id": "cli_existing_app"
				}
			}
		}
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf(
			"PATCH /api/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}

	cfg, err = config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	feishuChannel = cfg.Channels[config.ChannelFeishu]
	if len(feishuChannel.AllowFrom) != 0 {
		t.Fatalf("feishu allow_from = %#v, want empty slice", feishuChannel.AllowFrom)
	}

	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(configPath) error = %v", err)
	}
	if strings.Contains(string(configData), `"allow_from": [""]`) {
		t.Fatalf(
			"config file should not contain empty-string allow_from item: %s",
			string(configData),
		)
	}
}

func TestHandlePatchConfig_CreatesMissingChannelWithTypeAndSecret(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	delete(cfg.Channels, config.ChannelIRC)
	if err = config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"channel_list": {
			"irc": {
				"enabled": true,
				"type": "irc",
				"settings": {
					"server": "irc.example.com",
					"password": "irc-patch-password"
				}
			}
		}
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf(
			"PATCH /api/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}

	cfg, err = config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	bc := cfg.Channels[config.ChannelIRC]
	if bc == nil {
		t.Fatal("irc channel should exist after PATCH")
	}
	if got := bc.Type; got != config.ChannelIRC {
		t.Fatalf("irc type = %q, want %q", got, config.ChannelIRC)
	}
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	ircCfg := decoded.(*config.IRCSettings)
	if got := ircCfg.Server; got != "irc.example.com" {
		t.Fatalf("irc server = %q, want %q", got, "irc.example.com")
	}
	if got := ircCfg.Password.String(); got != "irc-patch-password" {
		t.Fatalf("irc password = %q, want %q", got, "irc-patch-password")
	}
	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(configPath) error = %v", err)
	}
	if bytes.Contains(configData, []byte("irc-patch-password")) {
		t.Fatalf("config file leaked irc password: %s", string(configData))
	}
}

// setupPicoEnabledEnv creates a test environment with Pico channel enabled and
// its token stored only in .security.yml (not in the JSON payload).
func setupPicoEnabledEnv(t *testing.T) (string, func()) {
	t.Helper()

	tmp := t.TempDir()
	oldHome := os.Getenv("HOME")
	oldPicoHome := os.Getenv("PICOCLAW_HOME")

	if err := os.Setenv("HOME", tmp); err != nil {
		t.Fatalf("set HOME: %v", err)
	}
	if err := os.Setenv("PICOCLAW_HOME", filepath.Join(tmp, ".picoclaw")); err != nil {
		t.Fatalf("set PICOCLAW_HOME: %v", err)
	}

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
	bc := cfg.Channels["pico"]
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	picoCfg := decoded.(*config.PicoSettings)
	bc.Enabled = true
	picoCfg.Token = *config.NewSecureString("test-pico-token")

	configPath := filepath.Join(tmp, "config.json")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig error: %v", err)
	}

	cleanup := func() {
		_ = os.Setenv("HOME", oldHome)
		if oldPicoHome == "" {
			_ = os.Unsetenv("PICOCLAW_HOME")
		} else {
			_ = os.Setenv("PICOCLAW_HOME", oldPicoHome)
		}
	}
	return configPath, cleanup
}

func TestHandleUpdateConfig_SucceedsWhenPicoTokenInSecurityOnly(t *testing.T) {
	configPath, cleanup := setupPicoEnabledEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// PUT request with pico enabled but no token in JSON — token is in .security.yml
	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewBufferString(`{
		"version": 5,
		"agents": {
			"defaults": {
				"workspace": "~/.picoclaw/workspace",
				"account_ref": "custom-default",
				"model_name": "custom-default"
			}
		},
		"channels": {
			"pico": {
				"enabled": true,
				"ping_interval": 30,
				"read_timeout": 60,
				"write_timeout": 10,
				"max_connections": 100
			}
		},
		"model_aliases": [
			{
				"name": "custom-default",
				"model": "openai/gpt-4o"
			}
		],
		"model_list": [
			{
				"model_name": "custom-default",
				"provider": "openai",
				"model": "openai/gpt-4o",
				"api_keys": ["sk-default"],
				"enabled": true
			}
		]
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf(
			"PUT /api/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}
}

func TestHandlePatchConfig_SucceedsWhenPicoTokenInSecurityOnly(t *testing.T) {
	configPath, cleanup := setupPicoEnabledEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// PATCH request changing an unrelated field — pico token still in .security.yml
	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"gateway": {
			"log_level": "info"
		}
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf(
			"PATCH /api/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}
}

func TestConfigAPIEventWebhookSecretPreservation(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			configPath, cleanup, originalSecret := setupEventWebhookAPIConfig(t)
			defer cleanup()

			body := eventWebhookAPIUpdateBody(t, configPath, method, map[string]map[string]any{
				"deploy": {"secret": "[NOT_HERE]"},
			})
			response := performConfigAPIRequest(t, configPath, method, body)
			if response.Code != http.StatusOK {
				t.Fatalf(
					"%s /api/config status = %d, want %d, body=%s",
					method,
					response.Code,
					http.StatusOK,
					response.Body.String(),
				)
			}

			updated, err := config.LoadConfig(configPath)
			if err != nil {
				t.Fatalf("LoadConfig(updated) error = %v", err)
			}
			if got := configuredEventWebhookSecret(updated, "deploy"); got != originalSecret {
				t.Fatalf("preserved webhook secret = %q, want original value", got)
			}
		})
	}
}

func TestConfigAPIEventWebhookSecretRotationAndAddition(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			configPath, cleanup, originalSecret := setupEventWebhookAPIConfig(t)
			defer cleanup()

			rotatedSecret := eventWebhookAPISecret(0x32)
			addedSecret := eventWebhookAPISecret(0x33)
			body := eventWebhookAPIUpdateBody(t, configPath, method, map[string]map[string]any{
				"deploy": {"secret": rotatedSecret},
				"audit": {
					"enabled": true,
					"secret":  addedSecret,
				},
			})
			response := performConfigAPIRequest(t, configPath, method, body)
			if response.Code != http.StatusOK {
				t.Fatalf(
					"%s /api/config status = %d, want %d, body=%s",
					method,
					response.Code,
					http.StatusOK,
					response.Body.String(),
				)
			}

			updated, err := config.LoadConfig(configPath)
			if err != nil {
				t.Fatalf("LoadConfig(updated) error = %v", err)
			}
			if got := configuredEventWebhookSecret(updated, "deploy"); got != rotatedSecret {
				t.Fatalf("rotated webhook secret = %q, want new value", got)
			}
			if got := configuredEventWebhookSecret(updated, "audit"); got != addedSecret {
				t.Fatalf("added webhook secret = %q, want new connector value", got)
			}

			configJSON, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("ReadFile(config.json) error = %v", err)
			}
			for _, secret := range []string{originalSecret, rotatedSecret, addedSecret} {
				if bytes.Contains(configJSON, []byte(secret)) {
					t.Fatal("config.json exposed an event webhook signing secret")
				}
			}
		})
	}
}

func TestConfigAPIEventWebhookGitHubFormatAndSecretUpdate(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			configPath, cleanup, originalSecret := setupEventWebhookAPIConfig(t)
			defer cleanup()

			gitHubSecret := strings.Repeat("github-webhook-secret-", 2)
			body := eventWebhookAPIUpdateBody(t, configPath, method, map[string]map[string]any{
				"deploy": {
					"format": config.EventWebhookFormatGitHub,
					"secret": gitHubSecret,
				},
			})
			response := performConfigAPIRequest(t, configPath, method, body)
			if response.Code != http.StatusOK {
				t.Fatalf(
					"%s /api/config status = %d, want %d, body=%s",
					method,
					response.Code,
					http.StatusOK,
					response.Body.String(),
				)
			}

			updated, err := config.LoadConfig(configPath)
			if err != nil {
				t.Fatalf("LoadConfig(updated) error = %v", err)
			}
			webhook := updated.Events.Ingress.Webhooks["deploy"]
			if webhook.Format != config.EventWebhookFormatGitHub {
				t.Fatalf("updated webhook format = %q, want github", webhook.Format)
			}
			if got := webhook.Secret.String(); got != gitHubSecret {
				t.Fatalf("updated GitHub webhook secret = %q, want new value", got)
			}

			configJSON, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("ReadFile(config.json) error = %v", err)
			}
			if !bytes.Contains(configJSON, []byte(`"format": "github"`)) {
				t.Fatal("config.json omitted the GitHub webhook format")
			}
			for _, secret := range []string{originalSecret, gitHubSecret} {
				if bytes.Contains(configJSON, []byte(secret)) {
					t.Fatal("config.json exposed an event webhook signing secret")
				}
			}
		})
	}
}

func TestConfigAPIRejectsInvalidEventWebhookSecret(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			configPath, cleanup, originalSecret := setupEventWebhookAPIConfig(t)
			defer cleanup()

			body := eventWebhookAPIUpdateBody(t, configPath, method, map[string]map[string]any{
				"deploy": {"secret": "not-a-standard-webhooks-secret"},
			})
			response := performConfigAPIRequest(t, configPath, method, body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf(
					"%s /api/config status = %d, want %d, body=%s",
					method,
					response.Code,
					http.StatusBadRequest,
					response.Body.String(),
				)
			}
			if !strings.Contains(response.Body.String(), "events.ingress") {
				t.Fatalf("validation response omitted event ingress error: %s", response.Body.String())
			}

			unchanged, err := config.LoadConfig(configPath)
			if err != nil {
				t.Fatalf("LoadConfig(after rejected update) error = %v", err)
			}
			if got := configuredEventWebhookSecret(unchanged, "deploy"); got != originalSecret {
				t.Fatalf("rejected update changed webhook secret to %q", got)
			}
		})
	}
}

func TestConfigAPIRejectsSecretBearingEventWebhookConnectorIdentity(t *testing.T) {
	firstSecret := eventWebhookAPIConnectorNameSafeSecret(0x41)
	secondSecret := eventWebhookAPIConnectorNameSafeSecret(0x42)
	credentialBearingName := "prefix-" + firstSecret + "-suffix"
	identityCases := []struct {
		name           string
		masterDisabled bool
		changes        map[string]map[string]any
	}{
		{
			name: "enabled name equals own secret",
			changes: map[string]map[string]any{
				firstSecret: {
					"enabled": true,
					"secret":  firstSecret,
				},
			},
		},
		{
			name: "enabled name contains other enabled secret",
			changes: map[string]map[string]any{
				"owner": {
					"enabled": true,
					"secret":  firstSecret,
				},
				credentialBearingName: {
					"enabled": true,
					"secret":  secondSecret,
				},
			},
		},
		{
			name: "disabled name contains enabled secret",
			changes: map[string]map[string]any{
				"owner": {
					"enabled": true,
					"secret":  firstSecret,
				},
				credentialBearingName: {
					"enabled": false,
				},
			},
		},
		{
			name: "disabled owner canonical secret",
			changes: map[string]map[string]any{
				"owner": {
					"enabled": false,
					"secret":  firstSecret,
				},
				credentialBearingName: {
					"enabled": false,
				},
			},
		},
		{
			name:           "master disabled canonical secret",
			masterDisabled: true,
			changes: map[string]map[string]any{
				"owner": {
					"enabled": false,
					"secret":  firstSecret,
				},
				credentialBearingName: {
					"enabled": false,
				},
			},
		},
	}

	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		for _, identityCase := range identityCases {
			t.Run(method+"/"+identityCase.name, func(t *testing.T) {
				configPath, cleanup, _ := setupEventWebhookAPIConfig(t)
				defer cleanup()
				securityPath := filepath.Join(filepath.Dir(configPath), config.SecurityConfigFile)
				configBefore, err := os.ReadFile(configPath)
				if err != nil {
					t.Fatalf("ReadFile(config.json before update) error = %v", err)
				}
				securityBefore, err := os.ReadFile(securityPath)
				if err != nil {
					t.Fatalf("ReadFile(.security.yml before update) error = %v", err)
				}

				body := eventWebhookAPIUpdateBody(
					t,
					configPath,
					method,
					identityCase.changes,
				)
				if identityCase.masterDisabled {
					body = eventWebhookAPISetIngressEnabled(t, body, false)
				}
				response := performConfigAPIRequest(t, configPath, method, body)
				if response.Code != http.StatusBadRequest {
					t.Fatalf(
						"%s /api/config status = %d, want %d",
						method,
						response.Code,
						http.StatusBadRequest,
					)
				}
				const opaqueConflictResponse = `{"errors":["events.ingress: ` +
					`webhook connector identity conflicts with a signing secret"],` +
					`"status":"validation_error"}` + "\n"
				if response.Body.String() != opaqueConflictResponse {
					t.Fatal("validation response was not the expected opaque identity conflict")
				}
				for _, sensitive := range []string{
					firstSecret,
					secondSecret,
					credentialBearingName,
				} {
					if strings.Contains(response.Body.String(), sensitive) {
						t.Fatal("validation response exposed a webhook signing credential")
					}
				}

				configAfter, err := os.ReadFile(configPath)
				if err != nil {
					t.Fatalf("ReadFile(config.json after update) error = %v", err)
				}
				securityAfter, err := os.ReadFile(securityPath)
				if err != nil {
					t.Fatalf("ReadFile(.security.yml after update) error = %v", err)
				}
				if !bytes.Equal(configAfter, configBefore) {
					t.Fatal("rejected update changed config.json")
				}
				if !bytes.Equal(securityAfter, securityBefore) {
					t.Fatal("rejected update changed .security.yml")
				}
				if bytes.Contains(configAfter, []byte(firstSecret)) {
					t.Fatal("rejected update persisted a webhook signing credential in config.json")
				}
			})
		}
	}
}

func TestHandleUpdateConfig_AppliesGatewayLogLevel(t *testing.T) {
	assertGatewayLogLevelApplied(t, http.MethodPut, `{
		"version": 5,
		"agents": {
			"defaults": {
				"workspace": "~/.picoclaw/workspace",
				"account_ref": "custom-default",
				"model_name": "custom-default"
			}
		},
		"gateway": {
			"log_level": "error"
		},
		"model_aliases": [
			{
				"name": "custom-default",
				"model": "openai/gpt-4o"
			}
		],
		"model_list": [
			{
				"model_name": "custom-default",
				"provider": "openai",
				"model": "openai/gpt-4o",
				"api_keys": ["sk-default"],
				"enabled": true
			}
		]
	}`, logger.ERROR)
}

func TestHandlePatchConfig_AppliesGatewayLogLevel(t *testing.T) {
	assertGatewayLogLevelApplied(t, http.MethodPatch, `{
		"gateway": {
			"log_level": "debug"
		}
	}`, logger.DEBUG)
}

func TestHandlePatchConfig_PreservesDebugFlagOverride(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	initialLevel := logger.GetLevel()
	logger.SetLevel(logger.INFO)
	t.Cleanup(func() {
		logger.SetLevel(initialLevel)
	})

	h := NewHandler(configPath)
	h.SetDebug(true)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"gateway": {
			"log_level": "error"
		}
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf(
			"PATCH /api/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}
	if got := logger.GetLevel(); got != logger.DEBUG {
		t.Fatalf("logger.GetLevel() = %v, want %v", got, logger.DEBUG)
	}
}

func TestHandlePatchConfig_SavesDiscordTokenFromPayload(t *testing.T) {
	t.Skip("TODO: fix this test")
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"channel_list": [
			{
				"name":"discord",
				"enabled": true,
				"token": "discord-test-token"
			}
		]
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf(
			"PATCH /api/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	bc := cfg.Channels[config.ChannelDiscord]
	if !bc.Enabled {
		t.Fatal("discord should be enabled after PATCH")
	}
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	if got := decoded.(*config.DiscordSettings).Token.String(); got != "discord-test-token" {
		t.Fatalf("discord token = %q, want %q", got, "discord-test-token")
	}
}

func TestHandlePatchConfig_DoesNotPersistShadowRegistryAuthTokenField(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"tools": {
			"skills": {
				"registries": {
					"github": {
						"_auth_token": "ghp-shadow-token"
					}
				}
			}
		}
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf(
			"PATCH /api/config status = %d, want %d, body=%s",
			rec.Code,
			http.StatusOK,
			rec.Body.String(),
		)
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	githubRegistry, ok := cfg.Tools.Skills.Registries.Get("github")
	if !ok {
		t.Fatal("github registry missing after PATCH")
	}
	if got := githubRegistry.AuthToken.String(); got != "ghp-shadow-token" {
		t.Fatalf("github registry auth token = %q, want %q", got, "ghp-shadow-token")
	}
	if got := githubRegistry.BaseURL; got != "https://github.com" {
		t.Fatalf("github registry base_url = %q, want %q", got, "https://github.com")
	}

	rawConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(configPath) error = %v", err)
	}
	if strings.Contains(string(rawConfig), "_auth_token") {
		t.Fatalf(
			"config.json should not persist _auth_token shadow field, got:\n%s",
			string(rawConfig),
		)
	}
}

func TestHandlePatchConfig_AllowsInvalidDenyRegexPatternsWhenDenyPatternsDisabled(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/config", bytes.NewBufferString(`{
		"tools": {
			"exec": {
				"enabled": true,
				"enable_deny_patterns": false,
				"custom_deny_patterns": ["("]
			}
		}
	}`))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

// testCommandPatterns is a helper that sets up a handler and sends a test-command-patterns request.
func testCommandPatterns(t *testing.T, configPath string, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/config/test-command-patterns",
		bytes.NewBufferString(body),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestHandleTestCommandPatterns_MatchesWhitelist(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	rec := testCommandPatterns(t, configPath, `{
		"allow_patterns": ["^echo\\s+hello"],
		"deny_patterns": ["^rm\\s+-rf"],
		"command": "echo hello world"
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"allowed":true`)) {
		t.Fatalf("expected allowed=true, body=%s", rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(`"blocked":true`)) {
		t.Fatalf("expected blocked=false when whitelist matches, body=%s", rec.Body.String())
	}
}

func TestHandleTestCommandPatterns_MatchesBlacklistNotWhitelist(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	rec := testCommandPatterns(t, configPath, `{
		"allow_patterns": ["^echo\\s+hello"],
		"deny_patterns": ["^rm\\s+-rf"],
		"command": "rm -rf /tmp"
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"blocked":true`)) {
		t.Fatalf("expected blocked=true, body=%s", rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(`"allowed":true`)) {
		t.Fatalf(
			"expected allowed=false when blacklist matches but not whitelist, body=%s",
			rec.Body.String(),
		)
	}
}

func TestHandleTestCommandPatterns_MatchesNeither(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	rec := testCommandPatterns(t, configPath, `{
		"allow_patterns": ["^echo\\s+hello"],
		"deny_patterns": ["^rm\\s+-rf"],
		"command": "ls -la"
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(`"allowed":true`)) {
		t.Fatalf("expected allowed=false, body=%s", rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(`"blocked":true`)) {
		t.Fatalf("expected blocked=false, body=%s", rec.Body.String())
	}
}

func TestHandleTestCommandPatterns_CaseInsensitiveWithGoFlag(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	rec := testCommandPatterns(t, configPath, `{
		"allow_patterns": ["(?i)^ECHO"],
		"deny_patterns": [],
		"command": "echo hello"
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"allowed":true`)) {
		t.Fatalf("expected allowed=true with Go (?i) flag, body=%s", rec.Body.String())
	}
}

func TestHandleTestCommandPatterns_EmptyPatterns(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	rec := testCommandPatterns(t, configPath, `{
		"allow_patterns": [],
		"deny_patterns": [],
		"command": "rm -rf /tmp"
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(`"allowed":true`)) {
		t.Fatalf("expected allowed=false with empty patterns, body=%s", rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(`"blocked":true`)) {
		t.Fatalf("expected blocked=false with empty patterns, body=%s", rec.Body.String())
	}
}

func TestHandleTestCommandPatterns_InvalidRegexSkipped(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	rec := testCommandPatterns(t, configPath, `{
		"allow_patterns": ["([[", "^echo"],
		"deny_patterns": [],
		"command": "echo hello"
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"allowed":true`)) {
		t.Fatalf(
			"expected allowed=true, invalid pattern skipped and valid one matched, body=%s",
			rec.Body.String(),
		)
	}
}

func TestHandleTestCommandPatterns_ReturnsMatchedPattern(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	rec := testCommandPatterns(t, configPath, `{
		"allow_patterns": [],
		"deny_patterns": ["\\$(?i)[a-zA-Z_]*(SECRET|KEY|PASSWORD|TOKEN|AUTH)[a-zA-Z0-9_]*"],
		"command": "echo $GITHUB_API_KEY"
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"blocked":true`)) {
		t.Fatalf("expected blocked=true, body=%s", rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`matched_blacklist`)) {
		t.Fatalf("expected matched_blacklist field, body=%s", rec.Body.String())
	}
}

func TestHandleTestCommandPatterns_InvalidJSON(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/config/test-command-patterns",
		bytes.NewBufferString(`{invalid json}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf(
			"status = %d, want %d, body=%s",
			rec.Code,
			http.StatusBadRequest,
			rec.Body.String(),
		)
	}
}

func TestApplyConfigSecretsFromMap_TelegramToken(t *testing.T) {
	cfg := config.DefaultConfig()
	bc := cfg.Channels["telegram"]
	bc.Enabled = true
	// Pre-decode so extend is populated
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	tgCfg := decoded.(*config.TelegramSettings)
	tgCfg.Token = *config.NewSecureString("original-token")

	raw := map[string]any{
		"channel_list": map[string]any{
			"telegram": map[string]any{
				"enabled": true,
				"token":   "secret-from-api",
			},
		},
	}

	if applyErr := applyConfigSecretsFromMap(cfg, raw); applyErr != nil {
		t.Fatalf("applyConfigSecretsFromMap() error = %v", applyErr)
	}

	if got := tgCfg.Token.String(); got != "secret-from-api" {
		t.Fatalf("telegram token = %q, want %q", got, "secret-from-api")
	}
}

func TestApplyConfigSecretsFromMap_TeamsWebhook(t *testing.T) {
	// applyConfigSecretsFromMap recurses into nested maps to find
	// SecureString fields at any depth (e.g. webhook_url inside webhooks map).
	cfg := config.DefaultConfig()
	bc := &config.Channel{Enabled: true, Type: config.ChannelTeamsWebHook}
	cfg.Channels["teams_webhook"] = bc
	target := &config.TeamsWebhookSettings{
		Webhooks: map[string]config.TeamsWebhookTarget{
			"default": {
				WebhookURL: *config.NewSecureString("https://example.com/hook1"),
				Title:      "Default",
			},
		},
	}
	if err := bc.Decode(target); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}

	raw := map[string]any{
		"channel_list": map[string]any{
			"teams_webhook": map[string]any{
				"enabled": true,
				"settings": map[string]any{
					"webhooks": map[string]any{
						"default": map[string]any{
							"webhook_url": "https://example.com/hook-updated",
							"title":       "Default Updated",
						},
					},
				},
			},
		},
	}

	if applyErr := applyConfigSecretsFromMap(cfg, raw); applyErr != nil {
		t.Fatalf("applyConfigSecretsFromMap() error = %v", applyErr)
	}

	// Verify the decoded struct has the updated SecureString value
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	twCfg, ok := decoded.(*config.TeamsWebhookSettings)
	if !ok {
		t.Fatalf("expected *TeamsWebhookSettings, got %T", decoded)
	}

	hookURL := twCfg.Webhooks["default"].WebhookURL
	if got := hookURL.String(); got != "https://example.com/hook-updated" {
		t.Fatalf("webhook_url = %q, want %q", got, "https://example.com/hook-updated")
	}
	// Note: title is a plain string, not a SecureString, so it is NOT updated
	// by applyConfigSecretsFromMap (only secure fields are handled).
}

func TestApplyConfigSecretsFromMap_MultipleChannels(t *testing.T) {
	cfg := config.DefaultConfig()

	// Setup telegram
	bc := cfg.Channels["telegram"]
	bc.Enabled = true
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() telegram error = %v", err)
	}
	tgCfg := decoded.(*config.TelegramSettings)
	tgCfg.Token = *config.NewSecureString("old-telegram-token")

	// Setup discord
	bc = cfg.Channels["discord"]
	bc.Enabled = true
	decoded, err = bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() discord error = %v", err)
	}
	discCfg := decoded.(*config.DiscordSettings)
	discCfg.Token = *config.NewSecureString("old-discord-token")

	raw := map[string]any{
		"channel_list": map[string]any{
			"telegram": map[string]any{
				"enabled": true,
				"settings": map[string]any{
					"token": "new-telegram-token",
				},
			},
			"discord": map[string]any{
				"enabled": true,
				"settings": map[string]any{
					"token": "new-discord-token",
				},
			},
		},
	}

	if applyErr := applyConfigSecretsFromMap(cfg, raw); applyErr != nil {
		t.Fatalf("applyConfigSecretsFromMap() error = %v", applyErr)
	}

	if got := tgCfg.Token.String(); got != "new-telegram-token" {
		t.Fatalf("telegram token = %q, want %q", got, "new-telegram-token")
	}
	if got := discCfg.Token.String(); got != "new-discord-token" {
		t.Fatalf("discord token = %q, want %q", got, "new-discord-token")
	}
}

func TestApplyConfigSecretsFromMap_SkipsNonStringValues(t *testing.T) {
	cfg := config.DefaultConfig()
	bc := cfg.Channels["telegram"]
	bc.Enabled = true
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	tgCfg := decoded.(*config.TelegramSettings)
	tgCfg.Token = *config.NewSecureString("original-token")

	raw := map[string]any{
		"channel_list": map[string]any{
			"telegram": map[string]any{
				"enabled": true,
				"token":   12345, // not a string, should be skipped
			},
		},
	}

	if applyErr := applyConfigSecretsFromMap(cfg, raw); applyErr != nil {
		t.Fatalf("applyConfigSecretsFromMap() error = %v", applyErr)
	}

	if got := tgCfg.Token.String(); got != "original-token" {
		t.Fatalf("telegram token = %q, want %q", got, "original-token")
	}
}

func TestApplyConfigSecretsFromMap_ChannelNotDecodedYet(t *testing.T) {
	cfg := config.DefaultConfig()
	bc := cfg.Channels["telegram"]
	bc.Enabled = true
	// Don't decode — let the function handle lazy decoding
	bc.Type = config.ChannelTelegram

	raw := map[string]any{
		"channel_list": map[string]any{
			"telegram": map[string]any{
				"enabled": true,
				"token":   "lazy-decoded-token",
			},
		},
	}

	if applyErr := applyConfigSecretsFromMap(cfg, raw); applyErr != nil {
		t.Fatalf("applyConfigSecretsFromMap() error = %v", applyErr)
	}

	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	tgCfg := decoded.(*config.TelegramSettings)
	if got := tgCfg.Token.String(); got != "lazy-decoded-token" {
		t.Fatalf("telegram token = %q, want %q", got, "lazy-decoded-token")
	}
}

func setupEventWebhookAPIConfig(t *testing.T) (string, func(), string) {
	t.Helper()
	configPath, cleanup := setupOAuthTestEnv(t)
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		cleanup()
		t.Fatalf("LoadConfig() error = %v", err)
	}
	secret := eventWebhookAPISecret(0x31)
	cfg.Events.Ingress = config.EventIngressConfig{
		Enabled: true,
		Webhooks: map[string]config.GenericWebhookConfig{
			"deploy": {
				Enabled: true,
				Secret:  *config.NewSecureString(secret),
			},
		},
	}
	if err = config.SaveConfig(configPath, cfg); err != nil {
		cleanup()
		t.Fatalf("SaveConfig(event webhook) error = %v", err)
	}
	return configPath, cleanup, secret
}

func eventWebhookAPISecret(fill byte) string {
	return "whsec_" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32))
}

func eventWebhookAPIConnectorNameSafeSecret(fill byte) string {
	return "whsec_" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 33))
}

func configuredEventWebhookSecret(cfg *config.Config, name string) string {
	webhook := cfg.Events.Ingress.Webhooks[name]
	return webhook.Secret.String()
}

func eventWebhookAPIUpdateBody(
	t *testing.T,
	configPath string,
	method string,
	changes map[string]map[string]any,
) []byte {
	t.Helper()

	var raw map[string]any
	if method == http.MethodPut {
		cfg, err := config.LoadConfigForUpdate(configPath)
		if err != nil {
			t.Fatalf("LoadConfigForUpdate(for PUT body) error = %v", err)
		}
		encoded, err := json.Marshal(cfg)
		if err != nil {
			t.Fatalf("Marshal(config for PUT) error = %v", err)
		}
		if err = json.Unmarshal(encoded, &raw); err != nil {
			t.Fatalf("Unmarshal(config map for PUT) error = %v", err)
		}
	} else {
		raw = map[string]any{
			"events": map[string]any{
				"ingress": map[string]any{
					"webhooks": map[string]any{},
				},
			},
		}
	}

	events, ok := raw["events"].(map[string]any)
	if !ok {
		t.Fatal("request config has no events object")
	}
	ingress, ok := events["ingress"].(map[string]any)
	if !ok {
		t.Fatal("request config has no events.ingress object")
	}
	webhooks, ok := ingress["webhooks"].(map[string]any)
	if !ok {
		t.Fatal("request config has no events.ingress.webhooks object")
	}
	for name, fields := range changes {
		webhook, _ := webhooks[name].(map[string]any)
		if webhook == nil {
			webhook = make(map[string]any)
			webhooks[name] = webhook
		}
		for field, value := range fields {
			webhook[field] = value
		}
	}

	body, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("Marshal(config API request) error = %v", err)
	}
	return body
}

func eventWebhookAPISetIngressEnabled(
	t *testing.T,
	body []byte,
	enabled bool,
) []byte {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("Unmarshal(config API request) error = %v", err)
	}
	events, ok := raw["events"].(map[string]any)
	if !ok {
		t.Fatal("request config has no events object")
	}
	ingress, ok := events["ingress"].(map[string]any)
	if !ok {
		t.Fatal("request config has no events.ingress object")
	}
	ingress["enabled"] = enabled
	updated, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("Marshal(config API request) error = %v", err)
	}
	return updated
}

func performConfigAPIRequest(
	t *testing.T,
	configPath string,
	method string,
	body []byte,
) *httptest.ResponseRecorder {
	t.Helper()
	handler := NewHandler(configPath)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	request := httptest.NewRequest(method, "/api/config", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}
