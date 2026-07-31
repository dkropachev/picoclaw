package model

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/config"
)

var configPath = ""

func initTest(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	configPath = filepath.Join(tmpDir, "config.json")
	t.Setenv("PICOCLAW_CONFIG", configPath)
}

func testConfigRevision(t *testing.T, path string) string {
	t.Helper()
	revision, err := config.ConfigRevision(path)
	require.NoError(t, err)
	return revision
}

func captureStdout(fn func()) string {
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	fn()

	_ = w.Close()
	os.Stdout = oldStdout

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	_ = r.Close()
	return buf.String()
}

func configuredAliases() *config.Config {
	return &config.Config{
		Version: config.CurrentVersion,
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				AccountRef: "account-main",
				ModelName:  "coding",
			},
		},
		ModelList: []*config.ModelConfig{{
			ModelName: "account-main",
			Model:     "provider-model",
			APIKeys:   config.SimpleSecureStrings("test"),
			Enabled:   true,
		}},
		ModelAliases: []config.ModelAliasConfig{
			{Name: "coding", Model: "provider-model"},
			{
				Name:  "fast",
				Model: "fast-model",
				AccountOverrides: map[string]string{
					"account-main": "fast-model-for-main",
				},
			},
		},
	}
}

func TestNewModelCommand(t *testing.T) {
	cmd := NewModelCommand()

	require.NotNil(t, cmd)
	assert.Equal(t, "model [model_alias]", cmd.Use)
	assert.Equal(t, "Show or change the configured model alias", cmd.Short)
	assert.NotNil(t, cmd.RunE)
	assert.NotNil(t, cmd.Args)
}

func TestShowCurrentModel_ShowsAccountAndAliasSeparately(t *testing.T) {
	output := captureStdout(func() {
		showCurrentModel(configuredAliases())
	})

	assert.Contains(t, output, "Current account: account-main")
	assert.Contains(t, output, "Current model alias: coding")
	assert.Contains(t, output, "Available model aliases:")
	assert.Contains(t, output, "> - coding (provider-model)")
	assert.Contains(t, output, "  - fast (fast-model, 1 account override(s))")
}

func TestShowCurrentModel_NoAliasSurfacesExactError(t *testing.T) {
	cfg := configuredAliases()
	cfg.Agents.Defaults.ModelName = ""

	output := captureStdout(func() {
		showCurrentModel(cfg)
	})

	assert.Contains(t, output, "Current model alias: (none) — no model configured")
}

func TestListAvailableModels_Empty(t *testing.T) {
	output := captureStdout(func() {
		listAvailableModels(&config.Config{})
	})

	assert.Contains(t, output, "No model aliases configured")
}

func TestSelectModelAlias_ExactAliasRetainsAccount(t *testing.T) {
	initTest(t)
	cfg := configuredAliases()

	output := captureStdout(func() {
		require.NoError(t, selectModelAlias(
			configPath,
			cfg,
			"fast",
			testConfigRevision(t, configPath),
		))
	})

	assert.Contains(t, output, "Model alias changed from 'coding' to 'fast'")
	assert.Contains(t, output, "Account remains 'account-main'")

	updatedCfg, err := config.LoadConfig(configPath)
	require.NoError(t, err)
	assert.Equal(t, "account-main", updatedCfg.Agents.Defaults.AccountRef)
	assert.Equal(t, "fast", updatedCfg.Agents.Defaults.ModelName)
}

func TestSelectModelAlias_RejectsStaleConfigRevision(t *testing.T) {
	initTest(t)
	require.NoError(t, config.SaveConfig(configPath, configuredAliases()))

	stale, staleRevision, err := config.LoadConfigForUpdateSnapshot(configPath)
	require.NoError(t, err)
	concurrent, concurrentRevision, err := config.LoadConfigForUpdateSnapshot(configPath)
	require.NoError(t, err)
	concurrent.ModelAliases = append(
		concurrent.ModelAliases,
		config.ModelAliasConfig{Name: "concurrent", Model: "new-model"},
	)
	_, err = config.SaveConfigIfRevision(configPath, concurrent, concurrentRevision)
	require.NoError(t, err)

	err = selectModelAlias(configPath, stale, "fast", staleRevision)
	require.ErrorIs(t, err, config.ErrConfigRevisionMismatch)
	assert.Contains(t, err.Error(), "config changed while selecting model alias; reload and retry")

	updated, err := config.LoadConfig(configPath)
	require.NoError(t, err)
	assert.Equal(t, "coding", updated.Agents.Defaults.ModelName)
	require.Len(t, updated.ModelAliases, 3)
	assert.Equal(t, "concurrent", updated.ModelAliases[2].Name)
}

func TestSelectModelAlias_RejectsRawModelID(t *testing.T) {
	initTest(t)
	cfg := configuredAliases()

	err := selectModelAlias(
		configPath,
		cfg,
		"provider-model",
		testConfigRevision(t, configPath),
	)

	require.EqualError(t, err, `model alias "provider-model" is not configured`)
	assert.Equal(t, "coding", cfg.Agents.Defaults.ModelName)
}

func TestSelectModelAlias_EmptyReturnsExactConfigurationError(t *testing.T) {
	initTest(t)
	cfg := configuredAliases()

	err := selectModelAlias(configPath, cfg, "", testConfigRevision(t, configPath))

	require.ErrorIs(t, err, config.ErrNoModelConfigured)
	require.EqualError(t, err, "no model configured")
	assert.Equal(t, "coding", cfg.Agents.Defaults.ModelName)
}

func TestSelectModelAlias_RequiresExplicitAccount(t *testing.T) {
	initTest(t)
	cfg := configuredAliases()
	cfg.Agents.Defaults.AccountRef = ""

	err := selectModelAlias(
		configPath,
		cfg,
		"fast",
		testConfigRevision(t, configPath),
	)

	require.EqualError(t, err, "no account configured")
	assert.Equal(t, "coding", cfg.Agents.Defaults.ModelName)
}

func TestSelectModelAlias_SaveConfigError(t *testing.T) {
	cfg := configuredAliases()

	const invalidPath = "/nonexistent/directory/config.json"
	err := selectModelAlias(
		invalidPath,
		cfg,
		"fast",
		testConfigRevision(t, invalidPath),
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to save config")
}

func TestFormatModelName(t *testing.T) {
	assert.Equal(t, "(none)", formatModelName(""))
	assert.Equal(t, "coding", formatModelName("coding"))
}

func TestModelCommandExecution_SetExactAlias(t *testing.T) {
	initTest(t)
	require.NoError(t, config.SaveConfig(configPath, configuredAliases()))

	cmd := NewModelCommand()
	output := captureStdout(func() {
		require.NoError(t, cmd.RunE(cmd, []string{"fast"}))
	})

	assert.Contains(t, output, "Model alias changed from 'coding' to 'fast'")
}

func TestModelCommandExecution_TooManyArgs(t *testing.T) {
	cmd := NewModelCommand()

	err := cmd.Args(cmd, []string{"one", "two"})

	require.Error(t, err)
}
