package config

import (
	"strings"
	"testing"
)

func TestModelRouterConfigValidateAcceptsFallbackAndLoadBalance(t *testing.T) {
	cfg := &Config{
		ModelList: []*ModelConfig{
			{ModelName: "primary", Provider: "openai", Model: "gpt-4o"},
			{ModelName: "backup", Provider: "anthropic", Model: "claude-sonnet-4"},
			{
				ModelName: "router-main",
				Provider:  ModelRouterProvider,
				Model:     "router-main",
				Router: &ModelRouterConfig{
					Enabled:                true,
					Entry:                  "pool",
					RefreshIntervalSeconds: 60,
					Blocks: []ModelRouterBlock{
						{
							ID:                     "pool",
							Type:                   ModelRouterBlockTypeLoadBalance,
							Accounts:               []string{"primary", "backup"},
							Strategy:               ModelRouterStrategyTokensSpent,
							RefreshIntervalSeconds: 30,
							Fallback:               "fallback",
						},
						{
							ID:      "fallback",
							Type:    ModelRouterBlockTypeAccount,
							Account: "backup",
						},
					},
				},
			},
		},
	}

	if err := cfg.ValidateModelList(); err != nil {
		t.Fatalf("ValidateModelList() error = %v", err)
	}
}

func TestModelRouterConfigValidateRejectsInvalidGraphs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{
			name: "unknown account",
			mutate: func(cfg *Config) {
				cfg.ModelList[2].Router.Blocks[0].Account = "missing"
			},
			want: "unknown account",
		},
		{
			name: "ambiguous account",
			mutate: func(cfg *Config) {
				cfg.ModelList = append(cfg.ModelList, &ModelConfig{
					ModelName: "primary",
					Provider:  "openai",
					Model:     "gpt-4o-mini",
				})
			},
			want: "ambiguous account",
		},
		{
			name: "router account",
			mutate: func(cfg *Config) {
				cfg.ModelList[2].Router.Blocks[0].Account = "router-main"
			},
			want: "references router model",
		},
		{
			name: "fallback cycle",
			mutate: func(cfg *Config) {
				cfg.ModelList[2].Router.Blocks[1].Fallback = "entry"
			},
			want: "fallback cycle",
		},
		{
			name: "duplicate load balance account",
			mutate: func(cfg *Config) {
				cfg.ModelList[2].Router.Entry = "pool"
				cfg.ModelList[2].Router.Blocks[0] = ModelRouterBlock{
					ID:       "pool",
					Type:     ModelRouterBlockTypeLoadBalance,
					Accounts: []string{"primary", "primary"},
				}
				cfg.ModelList[2].Router.Blocks = cfg.ModelList[2].Router.Blocks[:1]
			},
			want: "duplicate accounts",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validRouterConfigForTest()
			tt.mutate(cfg)

			err := cfg.ValidateModelList()
			if err == nil {
				t.Fatal("ValidateModelList() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateModelList() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func validRouterConfigForTest() *Config {
	return &Config{
		ModelList: []*ModelConfig{
			{ModelName: "primary", Provider: "openai", Model: "gpt-4o"},
			{ModelName: "backup", Provider: "anthropic", Model: "claude-sonnet-4"},
			{
				ModelName: "router-main",
				Provider:  ModelRouterProvider,
				Model:     "router-main",
				Router: &ModelRouterConfig{
					Enabled: true,
					Entry:   "entry",
					Blocks: []ModelRouterBlock{
						{
							ID:       "entry",
							Type:     ModelRouterBlockTypeAccount,
							Account:  "primary",
							Fallback: "fallback",
						},
						{
							ID:      "fallback",
							Type:    ModelRouterBlockTypeAccount,
							Account: "backup",
						},
					},
				},
			},
		},
	}
}
