package api

import (
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestValidateSelectionGraphChecksEveryModelRouterTerminal(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "openai-work",
		Provider:  "openai",
		Enabled:   true,
	}}
	cfg.ModelAliases = []config.ModelAliasConfig{
		{Name: "fast", Model: "openai/gpt-5.4-mini"},
		{Name: "deep", Model: "anthropic/claude-sonnet-4.6"},
	}
	cfg.ModelRouters = []config.ModelRouterConfig{{
		Name:    "task-router",
		Enabled: true,
		Entry:   "route",
		Blocks: []config.ModelRouterBlock{
			{
				ID:       "route",
				Type:     config.ModelRouterBlockTypeRules,
				Fallback: "fast",
				Rules: []config.ModelRouterRule{{
					Match:  config.ModelRouterRuleHasCode,
					Target: "deep",
				}},
			},
			{ID: "fast", Type: config.ModelRouterBlockTypeModel, Model: "fast"},
			{ID: "deep", Type: config.ModelRouterBlockTypeModel, Model: "deep"},
		},
	}}

	err := validateSelectionGraph(cfg, "openai-work", "task-router", true)
	if err == nil ||
		!strings.Contains(err.Error(), `model alias "deep" with account "openai-work"`) {
		t.Fatalf("error = %v, want incompatible second terminal", err)
	}
}

func TestValidateSelectionGraphChecksEveryAccountRouterTerminal(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{
		{ModelName: "openai-work", Provider: "openai", Enabled: true},
		{ModelName: "anthropic-work", Provider: "anthropic", Enabled: true},
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

	err := validateSelectionGraph(cfg, "router-1", "coding", true)
	if err == nil ||
		!strings.Contains(err.Error(), `model alias "coding" with account "anthropic-work"`) {
		t.Fatalf("error = %v, want incompatible second account", err)
	}

	cfg.ModelAliases[0].AccountOverrides = map[string]string{
		"anthropic-work": "anthropic/claude-sonnet-4.6",
	}
	if err := validateSelectionGraph(cfg, "router-1", "coding", true); err != nil {
		t.Fatalf("compatible graph rejected: %v", err)
	}
}

func TestValidateConfiguredModelSelectionGraphsRejectsReferencedAliasMutation(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "openai-work",
		Provider:  "openai",
		Enabled:   true,
	}}
	cfg.ModelAliases = []config.ModelAliasConfig{{
		Name:  "coding",
		Model: "anthropic/claude-sonnet-4.6",
	}}
	cfg.Agents.Defaults.AccountRef = "openai-work"
	cfg.Agents.Defaults.ModelName = "coding"

	err := validateConfiguredModelSelectionGraphs(cfg)
	if err == nil || !strings.Contains(err.Error(), "does not match account provider") {
		t.Fatalf("error = %v, want provider mismatch", err)
	}
}

func TestValidateAPIModelConfigurationRejectsHalfConfiguredSelections(t *testing.T) {
	tests := []struct {
		name      string
		account   string
		model     string
		wantError string
	}{
		{
			name:      "raw model without account",
			model:     "gpt-5.4",
			wantError: `is not a configured model alias`,
		},
		{
			name:      "unknown account without model",
			account:   "missing-account",
			wantError: `is not a configured account`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Agents.Defaults.AccountRef = test.account
			cfg.Agents.Defaults.ModelName = test.model

			err := validateAPIModelConfiguration(cfg)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want %q", err, test.wantError)
			}
		})
	}
}
