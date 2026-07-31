package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestConfigureCredentialAccount_DoesNotInventModel(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{ModelName: "raw-model-id"},
		},
	}

	configureCredentialAccount(cfg, "openai", "openai:work", "oauth")

	assert.Equal(t, "credential:openai:work", cfg.Agents.Defaults.AccountRef)
	assert.Empty(t, cfg.Agents.Defaults.ModelName)
	assert.Empty(t, cfg.ModelList)
	assert.Empty(t, cfg.ModelAliases)
}

func TestConfigureCredentialAccount_RetainsExplicitSelection(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				AccountRef: "router-1",
				ModelName:  "coding",
			},
		},
		ModelAliases: []config.ModelAliasConfig{{
			Name:  "coding",
			Model: "chosen-by-user",
		}},
	}

	configureCredentialAccount(cfg, "openai", "openai:work", "oauth")

	assert.Equal(t, "router-1", cfg.Agents.Defaults.AccountRef)
	assert.Equal(t, "coding", cfg.Agents.Defaults.ModelName)
	assert.Empty(t, cfg.ModelList)
}

func TestConfigureCredentialAccount_DoesNotPickFirstConfiguredAlias(t *testing.T) {
	cfg := &config.Config{
		ModelAliases: []config.ModelAliasConfig{{
			Name:  "coding",
			Model: "chosen-by-user",
		}},
	}

	configureCredentialAccount(cfg, "anthropic", "anthropic", "oauth")

	assert.Equal(t, "credential:anthropic", cfg.Agents.Defaults.AccountRef)
	assert.Empty(t, cfg.Agents.Defaults.ModelName)
}

func TestConfigureCredentialAccount_UpdatesOnlyExistingAccountConfig(t *testing.T) {
	cfg := &config.Config{
		ModelList: []*config.ModelConfig{{
			ModelName:    "work-account",
			Provider:     "openai",
			Model:        "chosen-by-user",
			CredentialID: "work",
			Enabled:      true,
		}},
		ModelAliases: []config.ModelAliasConfig{{
			Name:  "coding",
			Model: "chosen-by-user",
		}},
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{ModelName: "coding"},
		},
	}

	configureCredentialAccount(cfg, "openai", "openai:work", "token")

	require.Len(t, cfg.ModelList, 1)
	assert.Equal(t, "token", cfg.ModelList[0].AuthMethod)
	assert.Equal(t, "openai:work", cfg.ModelList[0].CredentialID)
	assert.Equal(t, "credential:openai:work", cfg.Agents.Defaults.AccountRef)
	assert.Equal(t, "coding", cfg.Agents.Defaults.ModelName)
}

func TestCredentialAccountRef(t *testing.T) {
	assert.Equal(t, "credential:anthropic:personal", credentialAccountRef("anthropic:personal"))
}
