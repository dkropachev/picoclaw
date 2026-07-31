package model

import (
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestValidateAliasForAccountSelectorChecksEveryReachableAccount(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "openai-work",
			Provider:  "openai",
			Enabled:   true,
		},
		{
			ModelName: "anthropic-work",
			Provider:  "anthropic",
			Enabled:   true,
		},
	}
	cfg.ModelAliases = []config.ModelAliasConfig{{
		Name:  "coding",
		Model: "openai/gpt-5.4",
	}}
	cfg.AccountRouters = []config.AccountRouterConfig{{
		Name:    "router-1",
		Enabled: true,
		Entry:   "pool",
		Blocks: []config.AccountRouterBlock{{
			ID:       "pool",
			Type:     config.AccountRouterBlockTypeLoadBalance,
			Accounts: []string{"openai-work", "anthropic-work"},
		}},
	}}

	err := validateAliasForAccountSelector(cfg, "router-1", "coding")
	if err == nil || !strings.Contains(err.Error(), `account "anthropic-work"`) {
		t.Fatalf("error = %v, want incompatible reachable account", err)
	}

	cfg.ModelAliases[0].AccountOverrides = map[string]string{
		"anthropic-work": "anthropic/claude-sonnet-4.6",
	}
	if err := validateAliasForAccountSelector(cfg, "router-1", "coding"); err != nil {
		t.Fatalf("compatible account override rejected: %v", err)
	}
}

func TestValidateAliasForAccountSelectorRejectsRawOrMissingAlias(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "openai-work",
		Provider:  "openai",
		Enabled:   true,
	}}

	err := validateAliasForAccountSelector(cfg, "openai-work", "gpt-5.4")
	if err == nil || !strings.Contains(err.Error(), `model alias "gpt-5.4" is not configured`) {
		t.Fatalf("error = %v, want exact missing-alias error", err)
	}
}
