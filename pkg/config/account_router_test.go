package config

import (
	"strings"
	"testing"
)

func TestAccountRouterConfigValidateAcceptsFallbackAndLoadBalance(t *testing.T) {
	cfg := &Config{
		ModelList: []*ModelConfig{
			{ModelName: "primary", Provider: "openai", Model: "gpt-4o"},
			{ModelName: "backup", Provider: "anthropic", Model: "claude-sonnet-4"},
		},
		AccountRouters: []AccountRouterConfig{
			{
				Name:                   "router-main",
				Model:                  "gpt-4o",
				Enabled:                true,
				Entry:                  "pool",
				RefreshIntervalSeconds: 60,
				Blocks: []AccountRouterBlock{
					{
						ID:                     "pool",
						Type:                   AccountRouterBlockTypeLoadBalance,
						Accounts:               []string{"primary", "backup"},
						Strategy:               AccountRouterStrategyTokensSpent,
						RefreshIntervalSeconds: 30,
						Fallback:               "fallback",
					},
					{
						ID:      "fallback",
						Type:    AccountRouterBlockTypeAccount,
						Account: "backup",
					},
				},
			},
		},
	}

	if err := cfg.ValidateModelList(); err != nil {
		t.Fatalf("ValidateModelList() error = %v", err)
	}
}

func TestAccountRouterConfigValidateAcceptsGitHubCopilotCredentialAccountRef(t *testing.T) {
	cfg := &Config{
		AccountRouters: []AccountRouterConfig{
			{
				Name:    "copilot-router",
				Model:   "gpt-5",
				Enabled: true,
				Entry:   "account-1",
				Blocks: []AccountRouterBlock{{
					ID:      "account-1",
					Type:    AccountRouterBlockTypeAccount,
					Account: "credential:github-copilot:gh-copilot",
				}},
			},
		},
	}

	if err := cfg.ValidateModelList(); err != nil {
		t.Fatalf("ValidateModelList() error = %v", err)
	}
}

func TestAccountRouterConfigValidateAcceptsGitHubCopilotCredentialLoadBalanceRefs(t *testing.T) {
	for _, strategy := range []string{
		AccountRouterStrategyBlind,
		AccountRouterStrategyTokensSpent,
		AccountRouterStrategyClosestLimit,
	} {
		t.Run(strategy, func(t *testing.T) {
			cfg := &Config{
				AccountRouters: []AccountRouterConfig{
					{
						Name:    "copilot-router",
						Model:   "gpt-5",
						Enabled: true,
						Entry:   "pool",
						Blocks: []AccountRouterBlock{{
							ID:   "pool",
							Type: AccountRouterBlockTypeLoadBalance,
							Accounts: []string{
								"credential:github-copilot:gh-copilot",
								"credential:github-copilot:backup",
							},
							Strategy: strategy,
						}},
					},
				},
			}

			if err := cfg.ValidateModelList(); err != nil {
				t.Fatalf("ValidateModelList() error = %v", err)
			}
		})
	}
}

func TestAccountRouterConfigValidateAcceptsBranchCondition(t *testing.T) {
	cfg := validRouterConfigForTest()
	cfg.AccountRouters[0].Entry = "limit-branch"
	cfg.AccountRouters[0].Blocks = []AccountRouterBlock{
		{
			ID:   "limit-branch",
			Type: AccountRouterBlockTypeBranch,
			Condition: &AccountRouterCondition{
				Left: AccountRouterExpression{
					Op: AccountRouterMathAdd,
					Left: &AccountRouterExpression{
						Account: "primary",
						Metric:  "rpm",
					},
					Right: &AccountRouterExpression{Value: float64Ptr(10)},
				},
				Operator: AccountRouterBranchOpGT,
				Right:    AccountRouterExpression{Value: float64Ptr(60)},
			},
			Then: "primary-account",
			Else: "backup-account",
		},
		{
			ID:      "primary-account",
			Type:    AccountRouterBlockTypeAccount,
			Account: "primary",
		},
		{
			ID:      "backup-account",
			Type:    AccountRouterBlockTypeAccount,
			Account: "backup",
		},
	}

	if err := cfg.ValidateModelList(); err != nil {
		t.Fatalf("ValidateModelList() error = %v", err)
	}
}

func TestAccountRouterConfigValidateRejectsInvalidGraphs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{
			name: "unknown account",
			mutate: func(cfg *Config) {
				cfg.AccountRouters[0].Blocks[0].Account = "missing"
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
				cfg.AccountRouters[0].Blocks[0].Account = "router-main"
			},
			want: "references router",
		},
		{
			name: "router self model",
			mutate: func(cfg *Config) {
				cfg.AccountRouters[0].Model = "router-main"
			},
			want: "not the router itself",
		},
		{
			name: "fallback cycle",
			mutate: func(cfg *Config) {
				cfg.AccountRouters[0].Blocks[1].Fallback = "entry"
			},
			want: "fallback cycle",
		},
		{
			name: "duplicate load balance account",
			mutate: func(cfg *Config) {
				cfg.AccountRouters[0].Entry = "pool"
				cfg.AccountRouters[0].Blocks[0] = AccountRouterBlock{
					ID:       "pool",
					Type:     AccountRouterBlockTypeLoadBalance,
					Accounts: []string{"primary", "primary"},
				}
				cfg.AccountRouters[0].Blocks = cfg.AccountRouters[0].Blocks[:1]
			},
			want: "duplicate accounts",
		},
		{
			name: "branch missing target",
			mutate: func(cfg *Config) {
				cfg.AccountRouters[0].Entry = "entry"
				cfg.AccountRouters[0].Blocks[0] = AccountRouterBlock{
					ID:   "entry",
					Type: AccountRouterBlockTypeBranch,
					Condition: &AccountRouterCondition{
						Left:     AccountRouterExpression{Account: "primary", Metric: "rpm"},
						Operator: AccountRouterBranchOpGT,
						Right:    AccountRouterExpression{Value: float64Ptr(10)},
					},
					Then: "missing",
					Else: "fallback",
				}
			},
			want: "then",
		},
		{
			name: "branch invalid math",
			mutate: func(cfg *Config) {
				cfg.AccountRouters[0].Entry = "entry"
				cfg.AccountRouters[0].Blocks[0] = AccountRouterBlock{
					ID:   "entry",
					Type: AccountRouterBlockTypeBranch,
					Condition: &AccountRouterCondition{
						Left:     AccountRouterExpression{Op: "pow"},
						Operator: AccountRouterBranchOpGT,
						Right:    AccountRouterExpression{Value: float64Ptr(10)},
					},
					Then: "fallback",
					Else: "fallback",
				}
			},
			want: "math op",
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

func float64Ptr(value float64) *float64 {
	return &value
}

func validRouterConfigForTest() *Config {
	return &Config{
		ModelList: []*ModelConfig{
			{ModelName: "primary", Provider: "openai", Model: "gpt-4o"},
			{ModelName: "backup", Provider: "anthropic", Model: "claude-sonnet-4"},
		},
		AccountRouters: []AccountRouterConfig{
			{
				Name:    "router-main",
				Model:   "gpt-4o",
				Enabled: true,
				Entry:   "entry",
				Blocks: []AccountRouterBlock{
					{
						ID:       "entry",
						Type:     AccountRouterBlockTypeAccount,
						Account:  "primary",
						Fallback: "fallback",
					},
					{
						ID:      "fallback",
						Type:    AccountRouterBlockTypeAccount,
						Account: "backup",
					},
				},
			},
		},
	}
}
