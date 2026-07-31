package agent

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestAccountAliasIdentityRoundTrip(t *testing.T) {
	key := accountAliasIdentityKey("credential:openai:work", "coding")
	if got := accountRefFromCandidateIdentityKey(key); got != "credential:openai:work" {
		t.Fatalf("account ref = %q, want credential account", got)
	}
	if got := modelAliasFromCandidateIdentityKey(key); got != "coding" {
		t.Fatalf("model alias = %q, want coding", got)
	}
}

func TestResolvedCandidateModelNameUsesAliasIdentity(t *testing.T) {
	got := resolvedCandidateModelName([]providers.FallbackCandidate{{
		Provider:    "openai",
		Model:       "gpt-5.4",
		DisplayName: "wrong-display",
		IdentityKey: accountAliasIdentityKey("account-a", "coding"),
	}}, "fallback")
	if got != "coding" {
		t.Fatalf("resolved alias = %q, want coding", got)
	}
}

func TestResolveActiveModelConfigRequiresAccountAliasIdentity(t *testing.T) {
	cfg := strictAliasTestConfig(t)
	candidate := providers.FallbackCandidate{
		Provider:    "openai",
		Model:       "gpt-5.4",
		IdentityKey: accountAliasIdentityKey("account-a", "coding"),
	}
	got := resolveActiveModelConfig(
		cfg,
		cfg.Agents.Defaults.Workspace,
		[]providers.FallbackCandidate{candidate},
		"ignored",
		"ignored",
	)
	if got == nil {
		t.Fatal("resolveActiveModelConfig() = nil")
	}
	if got.ModelName != "account-a" || got.Model != "gpt-5.4" {
		t.Fatalf("resolved config = %#v, want account-a with gpt-5.4", got)
	}

	if got := resolveActiveModelConfig(
		cfg,
		cfg.Agents.Defaults.Workspace,
		[]providers.FallbackCandidate{{
			Provider: "openai",
			Model:    "gpt-5.4",
		}},
		"gpt-5.4",
		"openai",
	); got != nil {
		t.Fatalf("raw candidate resolved to %#v, want nil", got)
	}
}

func strictAliasTestConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
			Workspace:  t.TempDir(),
			AccountRef: "account-a",
			ModelName:  "coding",
			MaxTokens:  1024,
		}},
		ModelAliases: []config.ModelAliasConfig{{
			Name:  "coding",
			Model: "gpt-5.4",
			AccountOverrides: map[string]string{
				"account-b": "claude-sonnet-4-6",
			},
		}},
		ModelList: []*config.ModelConfig{
			{
				ModelName: "account-a",
				Provider:  "openai",
				APIKeys:   config.SimpleSecureStrings("sk-a"),
				Enabled:   true,
			},
			{
				ModelName: "account-b",
				Provider:  "anthropic",
				APIKeys:   config.SimpleSecureStrings("sk-b"),
				Enabled:   true,
			},
		},
	}
}
