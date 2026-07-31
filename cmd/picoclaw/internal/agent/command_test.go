package agent

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestNewAgentCommand(t *testing.T) {
	cmd := NewAgentCommand()

	require.NotNil(t, cmd)

	assert.Equal(t, "agent", cmd.Use)
	assert.Equal(t, "Interact with the agent directly", cmd.Short)

	assert.Len(t, cmd.Aliases, 0)
	assert.False(t, cmd.HasSubCommands())

	assert.Nil(t, cmd.Run)
	assert.NotNil(t, cmd.RunE)

	assert.Nil(t, cmd.PersistentPreRun)
	assert.Nil(t, cmd.PersistentPostRun)

	assert.True(t, cmd.HasFlags())

	assert.NotNil(t, cmd.Flags().Lookup("debug"))
	assert.NotNil(t, cmd.Flags().Lookup("message"))
	assert.NotNil(t, cmd.Flags().Lookup("session"))
	assert.NotNil(t, cmd.Flags().Lookup("model"))
	assert.Contains(t, cmd.Flags().Lookup("model").Usage, "Configured model alias")
}

func TestAgentCmd_NoAliasReturnsExactConfigurationError(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv(config.EnvConfig, configPath)
	require.NoError(t, config.SaveConfig(configPath, &config.Config{
		Version: config.CurrentVersion,
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{AccountRef: "account-main"},
		},
		ModelList: []*config.ModelConfig{{
			ModelName: "account-main",
			Provider:  "openai",
			Enabled:   true,
		}},
	}))

	err := agentCmd("", "", "", false)

	require.ErrorIs(t, err, config.ErrNoModelConfigured)
	require.EqualError(t, err, "no model configured")
	assert.True(t, errors.Is(err, config.ErrNoModelConfigured))
}

func TestAgentCmd_ModelFlagRejectsRawModelID(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv(config.EnvConfig, configPath)
	require.NoError(t, config.SaveConfig(configPath, &config.Config{
		Version: config.CurrentVersion,
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				AccountRef: "account-main",
				ModelName:  "coding",
			},
		},
		ModelAliases: []config.ModelAliasConfig{{
			Name:  "coding",
			Model: "provider-model",
		}},
		ModelList: []*config.ModelConfig{{
			ModelName: "account-main",
			Provider:  "openai",
			Enabled:   true,
		}},
	}))

	err := agentCmd("", "", "provider-model", false)

	require.EqualError(t, err, `model alias "provider-model" is not configured`)
}
