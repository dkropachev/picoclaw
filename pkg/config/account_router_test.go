package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAccountRouterConfigValidateAcceptsFallbackAndLoadBalance(t *testing.T) {
	cfg := &Config{
		ModelList: []*ModelConfig{
			{ModelName: "primary", Provider: "openai", Model: "gpt-4o", Enabled: true},
			{ModelName: "backup", Provider: "anthropic", Model: "claude-sonnet-4", Enabled: true},
		},
		AccountRouters: []AccountRouterConfig{
			{
				Name:                   "router-main",
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

func TestAccountRouterMaterializesVirtualModelWithoutModel(t *testing.T) {
	cfg := &Config{
		AccountRouters: []AccountRouterConfig{{
			Name:    "router-main",
			Enabled: true,
			Entry:   "account",
			Blocks: []AccountRouterBlock{{
				ID:      "account",
				Type:    AccountRouterBlockTypeAccount,
				Account: "credential:openai:work",
			}},
		}},
	}

	cfg.MaterializeAccountRouterModels()

	model, err := cfg.GetModelConfig("router-main")
	if err != nil {
		t.Fatalf("GetModelConfig(router-main): %v", err)
	}
	if !model.IsVirtual() || !model.IsAccountRouter() {
		t.Fatalf(
			"router model virtual/account-router = %v/%v, want true/true",
			model.IsVirtual(),
			model.IsAccountRouter(),
		)
	}
	if model.Model != "" {
		t.Fatalf("router model ID = %q, want empty", model.Model)
	}
	if model.Router == nil || model.Router.Name != "router-main" {
		t.Fatalf("router config = %#v, want router-main", model.Router)
	}
	if err := cfg.ValidateModelList(); err != nil {
		t.Fatalf("ValidateModelList() error = %v", err)
	}
}

func TestAccountRouterLegacyModelFieldIsIgnoredAndOmitted(t *testing.T) {
	var router AccountRouterConfig
	input := []byte(`{
		"name": "router-main",
		"model": "gpt-5",
		"enabled": true,
		"entry": "account",
		"blocks": [{
			"id": "account",
			"type": "account",
			"account": "credential:openai:work"
		}]
	}`)
	if err := json.Unmarshal(input, &router); err != nil {
		t.Fatalf("Unmarshal legacy account router: %v", err)
	}
	var wrapper struct {
		AccountRouters []AccountRouterConfig `json:"account_routers"`
	}
	wrappedInput := append(
		[]byte(`{"account_routers":[`),
		append(input, []byte(`]}`)...)...,
	)
	if err := decodeJSONWithDiagnostics(wrappedInput, &wrapper, "config.json"); err != nil {
		t.Fatalf("diagnostic decode legacy account router: %v", err)
	}
	if err := router.Validate(); err != nil {
		t.Fatalf("Validate() legacy account router error = %v", err)
	}

	data, err := json.Marshal(router)
	if err != nil {
		t.Fatalf("Marshal account router: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("Unmarshal marshaled account router: %v", err)
	}
	if _, ok := fields["model"]; ok {
		t.Fatalf("marshaled account router contains legacy model field: %s", data)
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
					Enabled:   true,
				})
			},
			want: "ambiguous account",
		},
		{
			name: "disabled account",
			mutate: func(cfg *Config) {
				cfg.ModelList[0].Enabled = false
			},
			want: "disabled account",
		},
		{
			name: "router account",
			mutate: func(cfg *Config) {
				cfg.AccountRouters[0].Blocks[0].Account = "router-main"
			},
			want: "references router",
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
			{ModelName: "primary", Provider: "openai", Model: "gpt-4o", Enabled: true},
			{ModelName: "backup", Provider: "anthropic", Model: "claude-sonnet-4", Enabled: true},
		},
		AccountRouters: []AccountRouterConfig{
			{
				Name:    "router-main",
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
