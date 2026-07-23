package agent

import (
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/modelrouter"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestAgentLoopSelectCandidatesUsesBuiltModelRouter(t *testing.T) {
	cfg := &config.Config{
		ModelList: []*config.ModelConfig{
			{
				ModelName: "account-a",
				Provider:  "openai",
				Model:     "gpt-4o",
				APIKeys:   config.SimpleSecureStrings("sk-account-a"),
			},
			{
				ModelName: "account-b",
				Provider:  "openai",
				Model:     "gpt-4o-mini",
				APIKeys:   config.SimpleSecureStrings("sk-account-b"),
			},
			{
				ModelName: "router-main",
				Provider:  config.ModelRouterProvider,
				Model:     "router-main",
				Router: &config.ModelRouterConfig{
					Enabled: true,
					Entry:   "pool",
					Blocks: []config.ModelRouterBlock{{
						ID:       "pool",
						Type:     config.ModelRouterBlockTypeLoadBalance,
						Accounts: []string{"account-a", "account-b"},
						Strategy: config.ModelRouterStrategyTokensSpent,
					}},
				},
			},
		},
	}
	workspace := t.TempDir()
	candidateProviders := map[string]providers.LLMProvider{}
	router := buildModelRouter(cfg, "openai", "router-main", workspace, candidateProviders)
	if router == nil {
		t.Fatal("buildModelRouter() = nil")
	}
	if got := router.StatePath; got != filepath.Join(workspace, "model_router_state.json") {
		t.Fatalf("state path = %q, want workspace model_router_state.json", got)
	}

	loop := &AgentLoop{}
	agent := &AgentInstance{
		ID:    "main",
		Model: "router-main",
		Candidates: []providers.FallbackCandidate{{
			Provider:    "openai",
			Model:       "fallback",
			IdentityKey: "model_name:fallback",
		}},
		ModelRouter: router,
	}

	candidates, model, usedLight, selection := loop.selectCandidates(
		agent,
		"hello",
		nil,
		"session-1",
		modelrouter.SelectReasonInitial,
	)
	if usedLight {
		t.Fatal("usedLight = true, want false")
	}
	if selection.RouterName != "router-main" || selection.SessionKey != "session-1" {
		t.Fatalf("router selection = %#v, want router-main/session-1", selection)
	}
	if len(candidates) != 1 {
		t.Fatalf("len(candidates) = %d, want 1", len(candidates))
	}
	if got := candidates[0].IdentityKey; got != "model_name:account-a" {
		t.Fatalf("candidate identity = %q, want model_name:account-a", got)
	}
	if model != "gpt-4o" {
		t.Fatalf("resolved model = %q, want gpt-4o", model)
	}
	if candidateProviders[providers.ModelKey("openai", "gpt-4o")] == nil {
		t.Fatal("account-a provider was not registered")
	}
}
