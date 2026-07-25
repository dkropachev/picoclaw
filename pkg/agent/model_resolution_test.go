package agent

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestModelNameFromIdentityKey_LegacyProviderModel(t *testing.T) {
	if got := modelNameFromIdentityKey("openai/gpt-5.4"); got != "gpt-5.4" {
		t.Fatalf("modelNameFromIdentityKey() = %q, want %q", got, "gpt-5.4")
	}
}

func TestModelResolutionHelpersCoverFallbackBranches(t *testing.T) {
	if got := ensureProtocolModel(""); got != "" {
		t.Fatalf("ensureProtocolModel(empty) = %q, want empty", got)
	}
	if got := ensureProtocolModel("anthropic/claude"); got != "anthropic/claude" {
		t.Fatalf("ensureProtocolModel(protocol) = %q, want anthropic/claude", got)
	}
	if got := ensureProtocolModel("gpt-4o"); got != "openai/gpt-4o" {
		t.Fatalf("ensureProtocolModel(model) = %q, want openai/gpt-4o", got)
	}

	if got := modelConfigIdentityKey(nil); got != "" {
		t.Fatalf("modelConfigIdentityKey(nil) = %q, want empty", got)
	}
	if got := modelConfigIdentityKey(&config.ModelConfig{}); got != "" {
		t.Fatalf("modelConfigIdentityKey(empty) = %q, want empty", got)
	}

	provider, modelID := modelProviderAndIDForResolution("openai", nil)
	if provider != "" || modelID != "" {
		t.Fatalf("modelProviderAndIDForResolution(nil) = %q/%q, want empty", provider, modelID)
	}

	if candidate, ok := candidateFromModelConfig("openai", nil); ok {
		t.Fatalf("candidateFromModelConfig(nil) = %#v, true; want false", candidate)
	}
	if candidate, ok := candidateFromModelConfig("openai", &config.ModelConfig{
		ModelName: "empty",
		Provider:  "openai",
	}); ok {
		t.Fatalf("candidateFromModelConfig(empty model) = %#v, true; want false", candidate)
	}

	if got := resolvedCandidateProvider(nil, "fallback-provider"); got != "fallback-provider" {
		t.Fatalf("resolvedCandidateProvider(nil) = %q, want fallback-provider", got)
	}
	if got := resolvedCandidateProvider(
		[]providers.FallbackCandidate{{Provider: "anthropic"}},
		"openai",
	); got != "anthropic" {
		t.Fatalf("resolvedCandidateProvider(candidate) = %q, want anthropic", got)
	}

	if got, err := resolvedModelConfig(nil, "model", "/workspace"); err == nil || got != nil {
		t.Fatalf("resolvedModelConfig(nil) = %#v, %v; want nil config and error", got, err)
	}
}

func TestModelNameFromIdentityKey_PreservesNonLegacyIdentity(t *testing.T) {
	if got := modelNameFromIdentityKey("model_name:primary"); got != "model_name:primary" {
		t.Fatalf("modelNameFromIdentityKey() = %q, want %q", got, "model_name:primary")
	}
}

func TestModelAliasFromCandidateIdentityKey(t *testing.T) {
	if got := modelAliasFromCandidateIdentityKey("model_name:primary"); got != "primary" {
		t.Fatalf("modelAliasFromCandidateIdentityKey() = %q, want %q", got, "primary")
	}
	if got := modelAliasFromCandidateIdentityKey("openai/gpt-5.4"); got != "" {
		t.Fatalf("modelAliasFromCandidateIdentityKey() = %q, want empty", got)
	}
}

func TestResolvedCandidateModelName_PrefersIdentityAlias(t *testing.T) {
	got := resolvedCandidateModelName([]providers.FallbackCandidate{
		{Provider: "openai", Model: "gpt-5.4", IdentityKey: "model_name:primary"},
	}, "fallback-model")
	if got != "primary" {
		t.Fatalf("resolvedCandidateModelName() = %q, want %q", got, "primary")
	}
}

func TestResolvedCandidateModelName_DoesNotScanFallbackAliases(t *testing.T) {
	got := resolvedCandidateModelName([]providers.FallbackCandidate{
		{Provider: "openai", Model: "gpt-5.4"},
		{Provider: "openai", Model: "gpt-5.4-mini", IdentityKey: "model_name:fallback"},
	}, "primary-model")
	if got != "primary-model" {
		t.Fatalf("resolvedCandidateModelName() = %q, want %q", got, "primary-model")
	}
}

func TestResolvedCandidateModelName_UsesCandidateDisplayName(t *testing.T) {
	got := resolvedCandidateModelName([]providers.FallbackCandidate{
		{Provider: "openai", Model: "gpt-5.4", DisplayName: "gpt-5.4-display"},
	}, "fallback-model")
	if got != "gpt-5.4-display" {
		t.Fatalf("resolvedCandidateModelName() = %q, want %q", got, "gpt-5.4-display")
	}
}

func TestResolveActiveModelConfig_PrefersCandidateIdentityKey(t *testing.T) {
	cfg := &config.Config{
		ModelList: []*config.ModelConfig{
			{
				ModelName: "glm-4.7",
				Provider:  "zhipu",
				Model:     "glm-4.7",
				Streaming: config.ModelStreamingConfig{Enabled: false},
			},
			{
				ModelName: "suanneng-glm-4.7",
				Provider:  "zhipu",
				Model:     "glm-4.7",
				Streaming: config.ModelStreamingConfig{Enabled: true},
			},
		},
	}

	got := resolveActiveModelConfig(
		cfg,
		"/workspace",
		[]providers.FallbackCandidate{{
			Provider:    "zhipu",
			Model:       "glm-4.7",
			IdentityKey: "model_name:suanneng-glm-4.7",
		}},
		"glm-4.7",
		"openai",
	)

	if got == nil {
		t.Fatal("resolveActiveModelConfig() = nil, want model config")
	}
	if got.ModelName != "suanneng-glm-4.7" {
		t.Fatalf("model_name = %q, want %q", got.ModelName, "suanneng-glm-4.7")
	}
	if !got.Streaming.Enabled {
		t.Fatal("streaming.enabled = false, want true from identity-matched model config")
	}
}

func TestResolveActiveModelConfig_LoadBalancedAliasUsesSelectedCandidate(t *testing.T) {
	cfg := &config.Config{
		ModelList: []*config.ModelConfig{
			{
				ModelName: "lb-model",
				Model:     "openai/primary",
				Streaming: config.ModelStreamingConfig{Enabled: false},
			},
			{
				ModelName: "lb-model",
				Model:     "openai/secondary",
				Streaming: config.ModelStreamingConfig{Enabled: true},
			},
		},
	}

	got := resolveActiveModelConfig(
		cfg,
		"/workspace",
		[]providers.FallbackCandidate{{
			Provider:    "openai",
			Model:       "secondary",
			IdentityKey: "model_name:lb-model",
		}},
		"lb-model",
		"openai",
	)

	if got == nil {
		t.Fatal("resolveActiveModelConfig() = nil, want model config")
	}
	if got.Model != "openai/secondary" {
		t.Fatalf("model = %q, want openai/secondary", got.Model)
	}
	if !got.Streaming.Enabled {
		t.Fatal("streaming.enabled = false, want true from selected load-balanced entry")
	}
}

func TestResolveActiveModelConfig_DoesNotFallbackToOpenAIForDefaultProviderCandidate(t *testing.T) {
	cfg := &config.Config{
		ModelList: []*config.ModelConfig{
			{
				ModelName: "openai-gpt",
				Provider:  "openai",
				Model:     "gpt-4o",
				Streaming: config.ModelStreamingConfig{Enabled: true},
			},
		},
	}

	got := resolveActiveModelConfig(
		cfg,
		"/workspace",
		[]providers.FallbackCandidate{{
			Provider: "nvidia",
			Model:    "gpt-4o",
		}},
		"gpt-4o",
		"nvidia",
	)

	if got != nil {
		t.Fatalf("resolveActiveModelConfig() = %#v, want nil for non-active provider config", got)
	}
}

func TestLookupModelConfigByRefReturnsAccountRouterByName(t *testing.T) {
	cfg := &config.Config{
		ModelList: []*config.ModelConfig{
			{
				ModelName: "empty-regular",
				Provider:  "openai",
			},
			{
				ModelName: "account-a",
				Provider:  "openai",
				Model:     "gpt-4o",
			},
		},
		AccountRouters: []config.AccountRouterConfig{
			{
				Name:    "empty-router",
				Model:   "gpt-4o",
				Enabled: true,
				Entry:   "primary",
				Blocks: []config.AccountRouterBlock{{
					ID:      "primary",
					Type:    config.AccountRouterBlockTypeAccount,
					Account: "account-a",
				}},
			},
		},
	}
	cfg.MaterializeAccountRouterModels()

	got := lookupModelConfigByRef(cfg, "empty-router", "openai")
	if got == nil {
		t.Fatal("lookupModelConfigByRef(router) = nil, want router config")
	}
	if got.ModelName != "empty-router" {
		t.Fatalf("router model_name = %q, want empty-router", got.ModelName)
	}
	if !got.IsAccountRouter() {
		t.Fatal("lookupModelConfigByRef(router) returned non-router config")
	}

	if got := lookupModelConfigByRef(cfg, "empty-regular", "openai"); got != nil {
		t.Fatalf("lookupModelConfigByRef(empty regular) = %#v, want nil", got)
	}
}

func TestResolveModelCandidateRejectsAccountRouterAlias(t *testing.T) {
	cfg := &config.Config{
		ModelList: []*config.ModelConfig{
			{
				ModelName: "account-a",
				Provider:  "openai",
				Model:     "gpt-4o",
			},
		},
		AccountRouters: []config.AccountRouterConfig{
			{
				Name:    "router-main",
				Model:   "gpt-4o",
				Enabled: true,
				Entry:   "primary",
				Blocks: []config.AccountRouterBlock{{
					ID:      "primary",
					Type:    config.AccountRouterBlockTypeAccount,
					Account: "account-a",
				}},
			},
		},
	}
	cfg.MaterializeAccountRouterModels()

	if candidate, ok := resolveModelCandidate(cfg, "openai", "router-main"); ok {
		t.Fatalf("resolveModelCandidate(router) = %#v, true; want false", candidate)
	}
	candidate, ok := resolveModelCandidate(cfg, "openai", "account-a")
	if !ok {
		t.Fatal("resolveModelCandidate(account) ok = false, want true")
	}
	if candidate.IdentityKey != "model_name:account-a" {
		t.Fatalf("account identity = %q, want model_name:account-a", candidate.IdentityKey)
	}
	if candidate.Model != "gpt-4o" {
		t.Fatalf("account model = %q, want gpt-4o", candidate.Model)
	}
}
