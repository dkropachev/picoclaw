package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func validModelSelectionConfig() *Config {
	return &Config{
		Agents: AgentsConfig{
			Defaults: AgentDefaults{
				AccountRef:          "account-router",
				ModelName:           "model-router",
				ModelFallbacks:      []string{"backup"},
				ImageModel:          "vision",
				ImageModelFallbacks: []string{"backup"},
				Routing:             &RoutingConfig{LightModel: "light"},
			},
			List: []AgentConfig{{
				ID:         "worker",
				AccountRef: "account",
				Model: &AgentModelConfig{
					Primary:   "model-router",
					Fallbacks: []string{"backup"},
				},
				Subagents: &SubagentsConfig{Model: &AgentModelConfig{
					Primary:   "model-router",
					Fallbacks: []string{"light"},
				}},
			}},
		},
		ModelList: []*ModelConfig{{
			ModelName: "account",
			Provider:  "openai",
			Enabled:   true,
		}},
		ModelAliases: []ModelAliasConfig{
			{Name: "primary", Model: "gpt-5.4"},
			{Name: "backup", Model: "gpt-5.4-mini"},
			{Name: "vision", Model: "gpt-5.4-vision"},
			{Name: "light", Model: "gpt-5.4-nano"},
			{Name: "asr", Model: "whisper-explicit"},
			{Name: "tts", Model: "tts-explicit"},
		},
		AccountRouters: []AccountRouterConfig{{
			Name:    "account-router",
			Enabled: true,
			Entry:   "account",
			Blocks: []AccountRouterBlock{{
				ID:      "account",
				Type:    AccountRouterBlockTypeAccount,
				Account: "account",
			}},
		}},
		ModelRouters: []ModelRouterConfig{{
			Name:    "model-router",
			Enabled: true,
			Entry:   "primary",
			Blocks: []ModelRouterBlock{{
				ID:    "primary",
				Type:  ModelRouterBlockTypeModel,
				Model: "primary",
			}},
		}},
		Voice: VoiceConfig{
			AccountRef:    "account",
			ModelName:     "asr",
			TTSAccountRef: "account",
			TTSModelName:  "tts",
		},
	}
}

func TestValidateModelSelectionsAcceptsExactConfiguredReferences(t *testing.T) {
	require.NoError(t, validModelSelectionConfig().ValidateModelSelections())
}

func TestValidateModelSelectionsAllowsBlankFirstRunConfiguration(t *testing.T) {
	require.NoError(t, (&Config{}).ValidateModelSelections())
}

func TestValidateModelSelectionsRejectsInvalidModelReferences(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name: "raw default model",
			mutate: func(cfg *Config) {
				cfg.Agents.Defaults.ModelName = "gpt-5.4"
			},
			wantErr: `agents.defaults.model_name "gpt-5.4" is not a configured model alias`,
		},
		{
			name: "case-insensitive alias lookup is forbidden",
			mutate: func(cfg *Config) {
				cfg.Agents.Defaults.ModelName = "Primary"
			},
			wantErr: `agents.defaults.model_name "Primary" is not a configured model alias`,
		},
		{
			name: "model router fallback",
			mutate: func(cfg *Config) {
				cfg.Agents.Defaults.ModelFallbacks = []string{"model-router"}
			},
			wantErr: "model_fallbacks[0]",
		},
		{
			name: "model router image selector",
			mutate: func(cfg *Config) {
				cfg.Agents.Defaults.ImageModel = "model-router"
			},
			wantErr: "image_model",
		},
		{
			name: "model router light selector",
			mutate: func(cfg *Config) {
				cfg.Agents.Defaults.Routing.LightModel = "model-router"
			},
			wantErr: "routing.light_model",
		},
		{
			name: "model router agent fallback",
			mutate: func(cfg *Config) {
				cfg.Agents.List[0].Model.Fallbacks = []string{"model-router"}
			},
			wantErr: "agents.list[0].model.fallbacks[0]",
		},
		{
			name: "model router subagent fallback",
			mutate: func(cfg *Config) {
				cfg.Agents.List[0].Subagents.Model.Fallbacks = []string{"model-router"}
			},
			wantErr: "agents.list[0].subagents.model.fallbacks[0]",
		},
		{
			name: "model router voice selector",
			mutate: func(cfg *Config) {
				cfg.Voice.ModelName = "model-router"
			},
			wantErr: "voice.model_name",
		},
		{
			name: "unknown subscription equivalent alias",
			mutate: func(cfg *Config) {
				cfg.ModelList[0].SubscriptionEquivalentModel = "gpt-5.4"
			},
			wantErr: "model_list[0].subscription_equivalent_model",
		},
		{
			name: "model router subscription equivalent",
			mutate: func(cfg *Config) {
				cfg.ModelList[0].SubscriptionEquivalentModel = "model-router"
			},
			wantErr: "model_list[0].subscription_equivalent_model",
		},
		{
			name: "disabled primary model router",
			mutate: func(cfg *Config) {
				cfg.ModelRouters[0].Enabled = false
			},
			wantErr: "references a disabled model router",
		},
		{
			name: "blank elements remain harmless during first-run editing",
			mutate: func(cfg *Config) {
				cfg.Agents.Defaults.ModelFallbacks = []string{""}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validModelSelectionConfig()
			tt.mutate(cfg)
			err := cfg.ValidateModelSelections()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestValidateModelSelectionsRejectsInvalidAccountReferences(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name: "unknown default account",
			mutate: func(cfg *Config) {
				cfg.Agents.Defaults.AccountRef = "missing"
			},
			wantErr: `agents.defaults.account_ref "missing" is not a configured account`,
		},
		{
			name: "disabled concrete account",
			mutate: func(cfg *Config) {
				cfg.ModelList[0].Enabled = false
				cfg.Agents.List[0].AccountRef = "account"
			},
			wantErr: "references a disabled account",
		},
		{
			name: "model router is not an account",
			mutate: func(cfg *Config) {
				cfg.Agents.List[0].AccountRef = "model-router"
			},
			wantErr: "references a model router, not an account",
		},
		{
			name: "unsupported credential account",
			mutate: func(cfg *Config) {
				cfg.Agents.List[0].AccountRef = "credential:unsupported:work"
			},
			wantErr: "unsupported credential account",
		},
		{
			name: "voice rejects an enabled account router",
			mutate: func(cfg *Config) {
				cfg.Voice.AccountRef = "account-router"
			},
			wantErr: "a concrete account is required",
		},
		{
			name: "tts rejects an enabled account router",
			mutate: func(cfg *Config) {
				cfg.Voice.TTSAccountRef = "account-router"
			},
			wantErr: "a concrete account is required",
		},
		{
			name: "voice rejects a credential account",
			mutate: func(cfg *Config) {
				cfg.Voice.AccountRef = "credential:openai:speech"
			},
			wantErr: "an enabled concrete model_list account is required",
		},
		{
			name: "tts rejects a credential account",
			mutate: func(cfg *Config) {
				cfg.Voice.TTSAccountRef = "credential:openai:speech"
			},
			wantErr: "an enabled concrete model_list account is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validModelSelectionConfig()
			tt.mutate(cfg)
			err := cfg.ValidateModelSelections()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestValidateModelSelectionsChecksEveryReachableAccountAliasPair(t *testing.T) {
	cfg := &Config{
		Agents: AgentsConfig{Defaults: AgentDefaults{
			AccountRef: "router-1",
			ModelName:  "coding",
		}},
		ModelList: []*ModelConfig{
			{ModelName: "openai-work", Provider: "openai", Enabled: true},
			{ModelName: "anthropic-work", Provider: "anthropic", Enabled: true},
		},
		ModelAliases: []ModelAliasConfig{{
			Name:  "coding",
			Model: "openai/gpt-5.4",
		}},
		AccountRouters: []AccountRouterConfig{{
			Name:    "router-1",
			Enabled: true,
			Entry:   "pool",
			Blocks: []AccountRouterBlock{{
				ID:       "pool",
				Type:     AccountRouterBlockTypeLoadBalance,
				Accounts: []string{"openai-work", "anthropic-work"},
			}},
		}},
	}

	require.NoError(t, cfg.ValidateModelList())
	err := cfg.ValidateModelSelections()
	require.ErrorContains(
		t,
		err,
		`model alias "coding" with account "anthropic-work"`,
	)
	require.ErrorContains(t, err, `does not match account provider "anthropic"`)

	cfg.ModelAliases[0].AccountOverrides = map[string]string{
		"anthropic-work": "anthropic/claude-sonnet-4.6",
	}
	require.NoError(t, cfg.ValidateModelSelections())
}

func TestValidateModelSelectionsChecksEveryReachableModelRouterAlias(t *testing.T) {
	cfg := &Config{
		Agents: AgentsConfig{Defaults: AgentDefaults{
			AccountRef: "openai-work",
			ModelName:  "task-router",
		}},
		ModelList: []*ModelConfig{{
			ModelName: "openai-work",
			Provider:  "openai",
			Enabled:   true,
		}},
		ModelAliases: []ModelAliasConfig{
			{Name: "fast", Model: "openai/gpt-5.4-mini"},
			{Name: "deep", Model: "anthropic/claude-sonnet-4.6"},
		},
		ModelRouters: []ModelRouterConfig{{
			Name:    "task-router",
			Enabled: true,
			Entry:   "route",
			Blocks: []ModelRouterBlock{
				{
					ID:       "route",
					Type:     ModelRouterBlockTypeRules,
					Fallback: "fast",
					Rules: []ModelRouterRule{{
						Match:  ModelRouterRuleHasCode,
						Target: "deep",
					}},
				},
				{ID: "fast", Type: ModelRouterBlockTypeModel, Model: "fast"},
				{ID: "deep", Type: ModelRouterBlockTypeModel, Model: "deep"},
			},
		}},
	}

	require.NoError(t, cfg.ValidateModelList())
	err := cfg.ValidateModelSelections()
	require.ErrorContains(
		t,
		err,
		`model alias "deep" with account "openai-work"`,
	)
}

func TestValidateModelSelectionsChecksAccountAndModelRouterCrossProduct(t *testing.T) {
	cfg := &Config{
		Agents: AgentsConfig{Defaults: AgentDefaults{
			AccountRef: "account-router",
			ModelName:  "model-router",
		}},
		ModelList: []*ModelConfig{
			{ModelName: "openai-work", Provider: "openai", Enabled: true},
			{ModelName: "anthropic-work", Provider: "anthropic", Enabled: true},
		},
		ModelAliases: []ModelAliasConfig{
			{
				Name:  "fast",
				Model: "openai/gpt-5.4-mini",
				AccountOverrides: map[string]string{
					"anthropic-work": "anthropic/claude-haiku-4-5",
				},
			},
			{
				Name:  "deep",
				Model: "anthropic/claude-sonnet-4.6",
			},
		},
		AccountRouters: []AccountRouterConfig{{
			Name:    "account-router",
			Enabled: true,
			Entry:   "pool",
			Blocks: []AccountRouterBlock{{
				ID:       "pool",
				Type:     AccountRouterBlockTypeLoadBalance,
				Accounts: []string{"openai-work", "anthropic-work"},
			}},
		}},
		ModelRouters: []ModelRouterConfig{{
			Name:    "model-router",
			Enabled: true,
			Entry:   "route",
			Blocks: []ModelRouterBlock{
				{
					ID:       "route",
					Type:     ModelRouterBlockTypeRules,
					Fallback: "fast",
					Rules: []ModelRouterRule{{
						Match:  ModelRouterRuleHasCode,
						Target: "deep",
					}},
				},
				{ID: "fast", Type: ModelRouterBlockTypeModel, Model: "fast"},
				{ID: "deep", Type: ModelRouterBlockTypeModel, Model: "deep"},
			},
		}},
	}

	require.NoError(t, cfg.ValidateModelList())
	err := cfg.ValidateModelSelections()
	require.ErrorContains(
		t,
		err,
		`model alias "deep" with account "openai-work"`,
	)

	cfg.ModelAliases[1].DisabledAccounts = []string{"openai-work"}
	require.NoError(t, cfg.ValidateModelSelections())

	cfg.ModelAliases[1].DisabledAccounts = []string{
		"openai-work",
		"anthropic-work",
	}
	err = cfg.ValidateModelSelections()
	require.ErrorContains(t, err, `model alias "deep" is disabled for every account`)

	cfg.ModelAliases[1].DisabledAccounts = nil
	cfg.ModelAliases[1].AccountOverrides = map[string]string{
		"openai-work": "openai/gpt-5.4",
	}
	require.NoError(t, cfg.ValidateModelSelections())
}

func TestValidateModelSelectionsChecksInheritedAgentModelsAgainstAgentAccount(t *testing.T) {
	cfg := &Config{
		Agents: AgentsConfig{
			Defaults: AgentDefaults{
				AccountRef:          "openai-work",
				ModelName:           "primary",
				ModelFallbacks:      []string{"fallback"},
				ImageModel:          "image",
				ImageModelFallbacks: []string{"image-fallback"},
				Routing:             &RoutingConfig{LightModel: "light"},
			},
			List: []AgentConfig{{
				ID:         "anthropic-agent",
				AccountRef: "anthropic-work",
			}},
		},
		ModelList: []*ModelConfig{
			{ModelName: "openai-work", Provider: "openai", Enabled: true},
			{ModelName: "anthropic-work", Provider: "anthropic", Enabled: true},
		},
		ModelAliases: []ModelAliasConfig{
			{Name: "primary", Model: "openai/gpt-5.4"},
			{Name: "fallback", Model: "openai/gpt-5.4-mini"},
			{Name: "image", Model: "openai/gpt-image-1"},
			{Name: "image-fallback", Model: "openai/gpt-image-1-mini"},
			{Name: "light", Model: "openai/gpt-5.4-nano"},
		},
	}

	err := cfg.ValidateModelSelections()
	require.ErrorContains(t, err, `agents.list[0] "anthropic-agent"`)
	require.ErrorContains(t, err, `model alias "primary" with account "anthropic-work"`)

	for i := range cfg.ModelAliases {
		cfg.ModelAliases[i].AccountOverrides = map[string]string{
			"anthropic-work": "anthropic/claude-sonnet-4.6",
		}
	}
	require.NoError(t, cfg.ValidateModelSelections())
}

func TestLoadConfigValidatesModelSelectionsAfterMaterialization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"version": 5,
		"agents": {
			"defaults": {
				"account_ref": "account",
				"model_name": "raw-model"
			}
		},
		"model_list": [{
			"model_name": "account",
			"provider": "openai",
			"enabled": true
		}]
	}`), 0o600))

	_, err := LoadConfig(path)
	require.ErrorContains(
		t,
		err,
		`agents.defaults.model_name "raw-model" is not a configured model alias`,
	)
}

func TestLoadConfigRejectsProviderIncompatibleReachableAccountRouterPair(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeConfig := func(override string) {
		t.Helper()
		overrideJSON := ""
		if override != "" {
			overrideJSON = `,"account_overrides":{"anthropic-work":` +
				`"` + override + `"}`
		}
		require.NoError(t, os.WriteFile(path, []byte(`{
			"version": 5,
			"agents": {
				"defaults": {
					"account_ref": "router-1",
					"model_name": "coding"
				}
			},
			"model_list": [
				{"model_name":"openai-work","provider":"openai","enabled":true},
				{"model_name":"anthropic-work","provider":"anthropic","enabled":true}
			],
			"model_aliases": [{
				"name": "coding",
				"model": "openai/gpt-5.4"`+overrideJSON+`
			}],
			"account_routers": [{
				"name": "router-1",
				"enabled": true,
				"entry": "pool",
				"blocks": [{
					"id": "pool",
					"type": "load_balance",
					"accounts": ["openai-work", "anthropic-work"]
				}]
			}]
		}`), 0o600))
	}

	writeConfig("")
	_, err := LoadConfig(path)
	require.ErrorContains(
		t,
		err,
		`model alias "coding" with account "anthropic-work"`,
	)

	writeConfig("anthropic/claude-sonnet-4.6")
	_, err = LoadConfig(path)
	require.NoError(t, err)
}

func TestLoadConfigRejectsUnknownSubscriptionEquivalentAlias(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"version": 5,
		"model_list": [{
			"model_name": "account",
			"provider": "openai",
			"enabled": true,
			"subscription_equivalent_model": "metered-typo"
		}],
		"model_aliases": [{
			"name": "metered",
			"model": "openai/gpt-5.4"
		}]
	}`), 0o600))

	_, err := LoadConfig(path)
	require.ErrorContains(
		t,
		err,
		`model_list[0].subscription_equivalent_model "metered-typo" is not a configured model alias`,
	)
}

func TestLoadConfigAllowsBlankFirstRunModelSelections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"version":4}`), 0o600))
	_, err := LoadConfig(path)
	require.NoError(t, err)
}

func TestValidateModelSelectionsRequiresWebSearchAliasesAndProviderMatch(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*Config)
		wantError string
	}{
		{
			name: "gemini enabled without alias",
			configure: func(cfg *Config) {
				cfg.Tools.Web.Gemini.Enabled = true
			},
			wantError: "tools.web.gemini.model_alias: no model configured",
		},
		{
			name: "gemini rejects openai alias",
			configure: func(cfg *Config) {
				cfg.ModelAliases = []ModelAliasConfig{{
					Name:  "search",
					Model: "openai/gpt-5.4",
				}}
				cfg.Tools.Web.Gemini.Enabled = true
				cfg.Tools.Web.Gemini.ModelAlias = "search"
			},
			wantError: `model provider "openai" does not match account provider "gemini"`,
		},
		{
			name: "perplexity enabled without alias",
			configure: func(cfg *Config) {
				cfg.Tools.Web.Perplexity.Enabled = true
			},
			wantError: "tools.web.perplexity.model_alias: no model configured",
		},
		{
			name: "valid provider-qualified aliases",
			configure: func(cfg *Config) {
				cfg.ModelAliases = []ModelAliasConfig{
					{Name: "google-search", Model: "gemini/gemini-2.5-flash"},
					{Name: "pplx-search", Model: "perplexity/sonar"},
				}
				cfg.Tools.Web.Gemini.Enabled = true
				cfg.Tools.Web.Gemini.ModelAlias = "google-search"
				cfg.Tools.Web.Perplexity.Enabled = true
				cfg.Tools.Web.Perplexity.ModelAlias = "pplx-search"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.configure(cfg)
			err := cfg.ValidateModelSelections()
			if tt.wantError == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantError)
		})
	}
}
