package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func subscriptionEquivalentTestConfig() *Config {
	return &Config{
		ModelAliases: []ModelAliasConfig{
			{Name: "subscription", Model: "openai/subscription-model"},
			{Name: "standard", Model: "openai/standard-model"},
		},
		ModelList: []*ModelConfig{
			{
				ModelName:                   "subscription-metadata",
				Provider:                    "openai",
				Model:                       "subscription-model",
				Subscription:                true,
				SubscriptionEquivalentModel: "standard",
			},
			{
				ModelName: "standard-metadata",
				Provider:  "openai",
				Model:     "standard-model",
			},
		},
	}
}

func TestValidateModelSelectionsAcceptsAcyclicSubscriptionEquivalentAliases(t *testing.T) {
	cfg := subscriptionEquivalentTestConfig()
	require.NoError(t, cfg.ValidateModelList())
	require.NoError(t, cfg.ValidateModelSelections())
}

func TestValidateModelSelectionsAcceptsAccountFallbackWithPricedTerminalMetadata(
	t *testing.T,
) {
	cfg := subscriptionEquivalentTestConfig()
	cfg.ModelList = []*ModelConfig{
		{
			ModelName:                   "openai-account",
			Provider:                    "openai",
			Enabled:                     true,
			Subscription:                true,
			SubscriptionEquivalentModel: "standard",
		},
		{
			ModelName:          "standard-metadata",
			Provider:           "openai",
			Model:              "standard-model",
			InputPricePerMTok:  1,
			OutputPricePerMTok: 2,
		},
	}

	require.NoError(t, cfg.ValidateModelList())
	require.NoError(t, cfg.ValidateModelSelections())
}

func TestValidateModelSelectionsRejectsSubscriptionEquivalentAliasCycles(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name: "self cycle",
			mutate: func(cfg *Config) {
				cfg.ModelList[0].SubscriptionEquivalentModel = "subscription"
			},
			wantErr: `model_list[0].subscription_equivalent_model "subscription" creates ` +
				`a subscription equivalent model cycle: subscription -> subscription`,
		},
		{
			name: "multi alias cycle",
			mutate: func(cfg *Config) {
				cfg.ModelList[1].SubscriptionEquivalentModel = "subscription"
			},
			wantErr: `model_list[1].subscription_equivalent_model "subscription" creates ` +
				`a subscription equivalent model cycle: subscription -> standard -> subscription`,
		},
		{
			name: "account fallback metadata self cycle",
			mutate: func(cfg *Config) {
				cfg.ModelList = []*ModelConfig{{
					ModelName:                   "openai-account",
					Provider:                    "openai",
					Enabled:                     true,
					Subscription:                true,
					SubscriptionEquivalentModel: "subscription",
				}}
			},
			wantErr: `model_list[0].subscription_equivalent_model "subscription" creates ` +
				`a subscription equivalent model cycle: subscription -> subscription`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := subscriptionEquivalentTestConfig()
			test.mutate(cfg)
			require.NoError(t, cfg.ValidateModelList())
			require.ErrorContains(t, cfg.ValidateModelSelections(), test.wantErr)
		})
	}
}

func TestLoadConfigRejectsSubscriptionEquivalentAliasCycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"version": 4,
		"model_aliases": [{
			"name": "subscription",
			"model": "openai/subscription-model"
		}],
		"model_list": [{
			"model_name": "subscription-metadata",
			"provider": "openai",
			"model": "subscription-model",
			"subscription": true,
			"subscription_equivalent_model": "subscription"
		}]
	}`), 0o600))

	_, err := LoadConfig(path)
	require.ErrorContains(
		t,
		err,
		"subscription equivalent model cycle: subscription -> subscription",
	)
}
