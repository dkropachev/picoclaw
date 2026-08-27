package providers

import (
	"context"
	"errors"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/isolation"
)

func requireProviderZeroPolicy(t *testing.T, provider LLMProvider) {
	t.Helper()
	_, err := provider.Chat(
		context.Background(),
		[]Message{{Role: "user", Content: "test"}},
		nil,
		"test-model",
		nil,
	)
	if !errors.Is(err, isolation.ErrExecutionPolicyUnavailable) {
		t.Fatalf("Chat() error = %v, want %v", err, isolation.ErrExecutionPolicyUnavailable)
	}
}

func TestCreateProviderFromConfigWithExecutionPolicyRetainsExactPolicy(t *testing.T) {
	provider, _, err := CreateProviderFromConfigWithExecutionPolicy(
		&config.ModelConfig{Model: "claude-cli/claude-model"},
		isolation.ExecutionPolicy{},
	)
	if err != nil {
		t.Fatalf("CreateProviderFromConfigWithExecutionPolicy() error = %v", err)
	}
	requireProviderZeroPolicy(t, provider)
}

func TestCreateProviderWithExecutionPolicyRetainsExactPolicy(t *testing.T) {
	cfg := config.DefaultConfig()
	configureProviderTestSelection(cfg, "codex-account", "coding", "codex-model")
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "codex-account",
			Model:     "codex-cli/codex-model",
			Enabled:   true,
		},
	}

	provider, _, err := CreateProviderWithExecutionPolicy(cfg, isolation.ExecutionPolicy{})
	if err != nil {
		t.Fatalf("CreateProviderWithExecutionPolicy() error = %v", err)
	}
	requireProviderZeroPolicy(t, provider)
}

func TestCreateProviderWithExecutionPolicyThreadsPolicyThroughAccountRouter(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.AccountRef = "cli-router"
	cfg.Agents.Defaults.ModelName = "coding"
	cfg.ModelAliases = []config.ModelAliasConfig{
		{Name: "coding", Model: "claude-model"},
	}
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "claude-account",
			Provider:  "claude-cli",
			Enabled:   true,
		},
	}
	cfg.AccountRouters = []config.AccountRouterConfig{
		{
			Name:    "cli-router",
			Enabled: true,
			Entry:   "primary",
			Blocks: []config.AccountRouterBlock{
				{
					ID:      "primary",
					Type:    config.AccountRouterBlockTypeAccount,
					Account: "claude-account",
				},
			},
		},
	}

	provider, _, err := CreateProviderWithExecutionPolicy(cfg, isolation.ExecutionPolicy{})
	if err != nil {
		t.Fatalf("CreateProviderWithExecutionPolicy() router error = %v", err)
	}
	requireProviderZeroPolicy(t, provider)
}
