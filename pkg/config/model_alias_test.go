package config

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

func TestModelAliasExactLookupAndResolution(t *testing.T) {
	cfg := &Config{
		ModelList: []*ModelConfig{
			{
				ModelName: "account-personal",
				Provider:  "openai",
				Enabled:   true,
			},
			{
				ModelName: "account-work",
				Provider:  "openai",
				Enabled:   true,
			},
		},
		ModelAliases: []ModelAliasConfig{{
			Name:  "coding",
			Model: "gpt-5.4",
			AccountOverrides: map[string]string{
				"account-work": "gpt-5.4-pro",
			},
		}},
	}

	alias, err := cfg.GetModelAlias("coding")
	require.NoError(t, err)
	require.Equal(t, "gpt-5.4", alias.Model)

	model, err := cfg.ResolveModelAlias("coding", "account-personal")
	require.NoError(t, err)
	require.Equal(t, "gpt-5.4", model)

	model, err = cfg.ResolveModelAlias("coding", "account-work")
	require.NoError(t, err)
	require.Equal(t, "gpt-5.4-pro", model)

	_, err = cfg.GetModelAlias("Coding")
	require.ErrorContains(t, err, `model alias "Coding" is not configured`)
	require.NotErrorIs(t, err, ErrNoModelConfigured)

	_, err = cfg.ResolveModelAlias("", "account-work")
	require.ErrorIs(t, err, ErrNoModelConfigured)
	require.Equal(t, "no model configured", err.Error())
}

func TestResolveModelAliasRequiresConcreteAccount(t *testing.T) {
	cfg := &Config{
		ModelList: []*ModelConfig{
			{ModelName: "enabled", Provider: "openai", Enabled: true},
			{ModelName: "disabled", Provider: "openai"},
		},
		ModelAliases: []ModelAliasConfig{{
			Name:  "coding",
			Model: "gpt-5.4",
			AccountOverrides: map[string]string{
				"credential:openai:work": "gpt-5.4-pro",
			},
		}},
		AccountRouters: []AccountRouterConfig{{Name: "account-router", Enabled: true}},
		ModelRouters:   []ModelRouterConfig{{Name: "model-router", Enabled: true}},
	}

	tests := []struct {
		name       string
		accountRef string
		want       string
		wantErr    string
	}{
		{name: "enabled model-list account", accountRef: "enabled", want: "gpt-5.4"},
		{
			name:       "supported credential account",
			accountRef: "credential:openai:work",
			want:       "gpt-5.4-pro",
		},
		{name: "blank account", wantErr: "no account configured"},
		{
			name:       "account router",
			accountRef: "account-router",
			wantErr:    "a concrete account is required",
		},
		{
			name:       "model router",
			accountRef: "model-router",
			wantErr:    "references a model router, not an account",
		},
		{
			name:       "disabled account",
			accountRef: "disabled",
			wantErr:    "references a disabled account",
		},
		{
			name:       "unsupported credential",
			accountRef: "credential:unsupported:work",
			wantErr:    "unsupported credential account",
		},
		{name: "unknown account", accountRef: "missing", wantErr: "not a configured account"},
		{
			name:       "surrounding whitespace is not fuzzy-matched",
			accountRef: " enabled ",
			wantErr:    "not an exact configured account reference",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := cfg.ResolveModelAlias("coding", tt.accountRef)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestValidateModelAliases(t *testing.T) {
	directAccount := &ModelConfig{
		ModelName: "account-work",
		Provider:  "openai",
		Model:     "gpt-5.4",
		Enabled:   true,
	}
	tests := []struct {
		name    string
		cfg     *Config
		wantErr string
	}{
		{
			name: "direct and credential account overrides",
			cfg: &Config{
				ModelList: []*ModelConfig{directAccount},
				ModelAliases: []ModelAliasConfig{{
					Name:  "coding",
					Model: "gpt-5.4",
					AccountOverrides: map[string]string{
						"account-work":                 "gpt-5.4-pro",
						"credential:github-copilot:me": "gpt-4.1",
					},
				}},
			},
		},
		{
			name: "empty name",
			cfg: &Config{ModelAliases: []ModelAliasConfig{{
				Model: "gpt-5.4",
			}}},
			wantErr: "model_aliases[0].name is required",
		},
		{
			name: "empty model",
			cfg: &Config{ModelAliases: []ModelAliasConfig{{
				Name: "coding",
			}}},
			wantErr: "model_aliases[0]: model is required",
		},
		{
			name: "invalid model identifier",
			cfg: &Config{ModelAliases: []ModelAliasConfig{{
				Name:  "coding",
				Model: "gpt 5.4",
			}}},
			wantErr: "model identifier contains whitespace",
		},
		{
			name: "duplicate name",
			cfg: &Config{ModelAliases: []ModelAliasConfig{
				{Name: "coding", Model: "gpt-5.4"},
				{Name: "coding", Model: "claude-sonnet-4.6"},
			}},
			wantErr: `name "coding" duplicates`,
		},
		{
			name: "model router name conflict",
			cfg: &Config{
				ModelRouters: []ModelRouterConfig{{Name: "task-router"}},
				ModelAliases: []ModelAliasConfig{{
					Name:  "task-router",
					Model: "gpt-5.4",
				}},
			},
			wantErr: `name "task-router" conflicts with a model router`,
		},
		{
			name: "empty override model",
			cfg: &Config{
				ModelList: []*ModelConfig{directAccount},
				ModelAliases: []ModelAliasConfig{{
					Name:             "coding",
					Model:            "gpt-5.4",
					AccountOverrides: map[string]string{"account-work": ""},
				}},
			},
			wantErr: `account_overrides["account-work"]: model is required`,
		},
		{
			name: "account router override",
			cfg: &Config{
				AccountRouters: []AccountRouterConfig{{Name: "router-1"}},
				ModelAliases: []ModelAliasConfig{{
					Name:             "coding",
					Model:            "gpt-5.4",
					AccountOverrides: map[string]string{"router-1": "gpt-5.4-pro"},
				}},
			},
			wantErr: "must reference a concrete account, not an account router",
		},
		{
			name: "model router override",
			cfg: &Config{
				ModelRouters: []ModelRouterConfig{{Name: "model-router-1"}},
				ModelAliases: []ModelAliasConfig{{
					Name:             "coding",
					Model:            "gpt-5.4",
					AccountOverrides: map[string]string{"model-router-1": "gpt-5.4-pro"},
				}},
			},
			wantErr: "must reference a concrete account, not a model router",
		},
		{
			name: "unknown account override",
			cfg: &Config{ModelAliases: []ModelAliasConfig{{
				Name:             "coding",
				Model:            "gpt-5.4",
				AccountOverrides: map[string]string{"missing": "gpt-5.4-pro"},
			}}},
			wantErr: "references an unknown concrete account",
		},
		{
			name: "override provider must match target account",
			cfg: &Config{
				ModelList: []*ModelConfig{{
					ModelName: "anthropic-work",
					Provider:  "anthropic",
					Enabled:   true,
				}},
				ModelAliases: []ModelAliasConfig{{
					Name:  "coding",
					Model: "anthropic/claude-sonnet-4.6",
					AccountOverrides: map[string]string{
						"anthropic-work": "openai/gpt-5.4",
					},
				}},
			},
			wantErr: `model provider "openai" does not match account provider "anthropic"`,
		},
		{
			name: "disabled duplicate provider is ignored",
			cfg: &Config{
				ModelList: []*ModelConfig{
					{
						ModelName: "account-work",
						Provider:  "anthropic",
						Enabled:   false,
					},
					{
						ModelName: "account-work",
						Provider:  "openai",
						Enabled:   true,
					},
				},
				ModelAliases: []ModelAliasConfig{{
					Name:  "coding",
					Model: "gpt-5.4",
					AccountOverrides: map[string]string{
						"account-work": "openai/gpt-5.4-pro",
					},
				}},
			},
		},
		{
			name: "disabled-only override account is not concrete",
			cfg: &Config{
				ModelList: []*ModelConfig{{
					ModelName: "account-work",
					Provider:  "openai",
					Enabled:   false,
				}},
				ModelAliases: []ModelAliasConfig{{
					Name:  "coding",
					Model: "gpt-5.4",
					AccountOverrides: map[string]string{
						"account-work": "openai/gpt-5.4-pro",
					},
				}},
			},
			wantErr: "references an unknown concrete account",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.ValidateModelAliases()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestValidateModelListIncludesModelAliases(t *testing.T) {
	cfg := &Config{
		ModelAliases: []ModelAliasConfig{{
			Name:  "broken",
			Model: "",
		}},
	}
	err := cfg.ValidateModelList()
	require.ErrorContains(t, err, "model_aliases[0]: model is required")
}

func TestErrNoModelConfiguredIdentity(t *testing.T) {
	require.ErrorIs(t, ErrNoModelConfigured, protocoltypes.ErrNoModelConfigured)
	require.Equal(t, "no model configured", ErrNoModelConfigured.Error())
}

func TestModelAliasesAreNotWrittenToSecurityYAML(t *testing.T) {
	cfg := &Config{
		ModelAliases: []ModelAliasConfig{{
			Name:  "coding",
			Model: "gpt-5.4",
		}},
	}
	data, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NotContains(t, string(data), "model_aliases")
	require.NotContains(t, string(data), "gpt-5.4")
}
