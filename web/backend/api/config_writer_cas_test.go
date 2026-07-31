package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

const concurrentWriterAlias = "concurrent-writer-alias"

func injectConcurrentAliasBeforeCAS(h *Handler) func() bool {
	originalSave := h.saveConfigIfRevision
	injected := false
	h.saveConfigIfRevision = func(
		path string,
		stale *config.Config,
		expectedRevision string,
	) (string, error) {
		if !injected {
			current, revision, err := config.LoadConfigForUpdateSnapshot(path)
			if err != nil {
				return "", fmt.Errorf("load concurrent config: %w", err)
			}
			current.ModelAliases = append(
				current.ModelAliases,
				config.ModelAliasConfig{
					Name:  concurrentWriterAlias,
					Model: "openai/gpt-5.4",
				},
			)
			if _, err := config.SaveConfigIfRevision(path, current, revision); err != nil {
				return "", fmt.Errorf("save concurrent config: %w", err)
			}
			injected = true
		}
		return originalSave(path, stale, expectedRevision)
	}
	return func() bool { return injected }
}

func requireConcurrentAlias(t *testing.T, configPath string) *config.Config {
	t.Helper()
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if _, err := cfg.GetModelAlias(concurrentWriterAlias); err != nil {
		t.Fatalf("concurrent model alias was lost: %v", err)
	}
	return cfg
}

func saveDefaultConfigForCASWriterTest(t *testing.T) string {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := config.SaveConfig(configPath, config.DefaultConfig()); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	return configPath
}

func TestEnsurePicoChannelRejectsStaleGenerationAndPreservesAlias(t *testing.T) {
	configPath := saveDefaultConfigForCASWriterTest(t)
	h := NewHandler(configPath)
	wasInjected := injectConcurrentAliasBeforeCAS(h)

	changed, err := h.EnsurePicoChannel()
	if !errors.Is(err, config.ErrConfigRevisionMismatch) {
		t.Fatalf("EnsurePicoChannel() error = %v, want revision mismatch", err)
	}
	if changed {
		t.Fatal("EnsurePicoChannel() reported a persisted change after CAS rejection")
	}
	if !wasInjected() {
		t.Fatal("concurrent write was not injected")
	}
	cfg := requireConcurrentAlias(t, configPath)
	if pico := cfg.Channels.GetByType(config.ChannelPico); pico != nil && pico.Enabled {
		t.Fatal("stale Pico channel mutation was persisted")
	}
}

func TestHandleRegenPicoTokenRejectsStaleGenerationWithoutUpdatingCache(t *testing.T) {
	configPath := saveDefaultConfigForCASWriterTest(t)
	h := NewHandler(configPath)
	if _, err := h.EnsurePicoChannel(); err != nil {
		t.Fatalf("EnsurePicoChannel() error = %v", err)
	}
	before := requirePicoToken(t, configPath)

	gateway.mu.Lock()
	originalCachedToken := gateway.picoToken
	gateway.picoToken = "cache-before-conflict"
	gateway.mu.Unlock()
	t.Cleanup(func() {
		gateway.mu.Lock()
		gateway.picoToken = originalCachedToken
		gateway.mu.Unlock()
	})

	wasInjected := injectConcurrentAliasBeforeCAS(h)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/pico/token", nil)
	h.handleRegenPicoToken(recorder, request)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", recorder.Code, recorder.Body.String())
	}
	if !wasInjected() {
		t.Fatal("concurrent write was not injected")
	}
	requireConcurrentAlias(t, configPath)
	if after := requirePicoToken(t, configPath); after != before {
		t.Fatalf("persisted token changed after CAS rejection: before=%q after=%q", before, after)
	}
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if gateway.picoToken != "cache-before-conflict" {
		t.Fatalf("gateway token cache = %q, want unchanged", gateway.picoToken)
	}
}

func TestChannelBindingWritersRejectStaleGenerationAndPreserveAlias(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Handler) error
		assert func(*testing.T, *config.Config)
	}{
		{
			name: "wecom",
			mutate: func(h *Handler) error {
				return h.saveWecomBinding("new-bot", "new-secret")
			},
			assert: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				if channel := cfg.Channels.Get(config.ChannelWeCom); channel != nil {
					var settings config.WeComSettings
					if err := channel.Decode(&settings); err != nil {
						t.Fatalf("decode WeCom settings: %v", err)
					}
					if channel.Enabled || settings.BotID == "new-bot" {
						t.Fatal("stale WeCom binding was persisted")
					}
				}
			},
		},
		{
			name: "weixin",
			mutate: func(h *Handler) error {
				return h.saveWeixinBinding("new-token", "new-account")
			},
			assert: func(t *testing.T, cfg *config.Config) {
				t.Helper()
				if channel := cfg.Channels.Get(config.ChannelWeixin); channel != nil {
					var settings config.WeixinSettings
					if err := channel.Decode(&settings); err != nil {
						t.Fatalf("decode Weixin settings: %v", err)
					}
					if channel.Enabled ||
						settings.Token.String() == "new-token" ||
						settings.AccountID == "new-account" {
						t.Fatal("stale Weixin binding was persisted")
					}
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configPath := saveDefaultConfigForCASWriterTest(t)
			h := NewHandler(configPath)
			wasInjected := injectConcurrentAliasBeforeCAS(h)

			err := test.mutate(h)
			if !errors.Is(err, config.ErrConfigRevisionMismatch) {
				t.Fatalf("mutation error = %v, want revision mismatch", err)
			}
			if !wasInjected() {
				t.Fatal("concurrent write was not injected")
			}
			cfg := requireConcurrentAlias(t, configPath)
			test.assert(t, cfg)
		})
	}
}

func TestOAuthAuthMethodClearRejectsStaleGenerationAndPreservesAlias(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{{
		ModelName:  "openai-work",
		Provider:   "openai",
		Model:      "gpt-5.4",
		AuthMethod: "oauth",
		Enabled:    true,
	}}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	wasInjected := injectConcurrentAliasBeforeCAS(h)
	err := h.clearProviderAuthMethod(oauthProviderOpenAI, "")
	if !errors.Is(err, config.ErrConfigRevisionMismatch) {
		t.Fatalf("clearProviderAuthMethod() error = %v, want revision mismatch", err)
	}
	if !wasInjected() {
		t.Fatal("concurrent write was not injected")
	}
	saved := requireConcurrentAlias(t, configPath)
	if got := saved.ModelList[0].AuthMethod; got != "oauth" {
		t.Fatalf("auth_method = %q, want unchanged oauth", got)
	}
}

func requirePicoToken(t *testing.T, configPath string) string {
	t.Helper()
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	channel := cfg.Channels.GetByType(config.ChannelPico)
	if channel == nil {
		t.Fatal("Pico channel is missing")
	}
	decoded, err := channel.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	settings, ok := decoded.(*config.PicoSettings)
	if !ok {
		t.Fatalf("decoded Pico settings = %T", decoded)
	}
	return settings.Token.String()
}
