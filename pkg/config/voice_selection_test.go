package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func voiceCapabilityConfig(provider string) *Config {
	return &Config{
		ModelList: []*ModelConfig{{
			ModelName: "voice-account",
			Provider:  provider,
			Enabled:   true,
		}},
	}
}

func configureASR(cfg *Config, model string) {
	cfg.Voice.AccountRef = "voice-account"
	cfg.Voice.ModelName = "asr"
	cfg.ModelAliases = append(cfg.ModelAliases, ModelAliasConfig{
		Name:  "asr",
		Model: model,
	})
}

func configureTTS(cfg *Config, model string) {
	cfg.Voice.TTSAccountRef = "voice-account"
	cfg.Voice.TTSModelName = "tts"
	cfg.ModelAliases = append(cfg.ModelAliases, ModelAliasConfig{
		Name:  "tts",
		Model: model,
	})
}

func TestValidateModelSelectionsRejectsUnsupportedVoiceProviders(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*Config)
		wantError string
	}{
		{
			name: "anthropic ASR",
			configure: func(cfg *Config) {
				configureASR(cfg, "claude-sonnet-4-6")
			},
			wantError: `voice transcription account "voice-account" with model alias "asr": provider "anthropic"`,
		},
		{
			name: "anthropic TTS",
			configure: func(cfg *Config) {
				configureTTS(cfg, "claude-sonnet-4-6")
			},
			wantError: `voice TTS account "voice-account" with model alias "tts": provider "anthropic" does not support speech synthesis`,
		},
		{
			name: "unknown TTS provider",
			configure: func(cfg *Config) {
				configureTTS(cfg, "speech-model")
			},
			wantError: `voice TTS account "voice-account" with model alias "tts": provider "custom-openai-compatible" does not support speech synthesis`,
		},
		{
			name: "unsupported ElevenLabs ASR model",
			configure: func(cfg *Config) {
				configureASR(cfg, "scribe_future")
			},
			wantError: `provider "elevenlabs" only supports ASR model "scribe_v1"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := "anthropic"
			if tt.name == "unsupported ElevenLabs ASR model" {
				provider = "elevenlabs"
			} else if tt.name == "unknown TTS provider" {
				provider = "custom-openai-compatible"
			}
			cfg := voiceCapabilityConfig(provider)
			tt.configure(cfg)

			require.ErrorContains(t, cfg.ValidateModelSelections(), tt.wantError)
		})
	}
}

func TestValidateModelSelectionsAcceptsSupportedVoiceRoutes(t *testing.T) {
	tests := []struct {
		name      string
		provider  string
		configure func(*Config)
	}{
		{
			name:     "OpenAI Whisper",
			provider: "openai",
			configure: func(cfg *Config) {
				configureASR(cfg, "whisper-1")
			},
		},
		{
			name:     "Gemini audio model",
			provider: "gemini",
			configure: func(cfg *Config) {
				configureASR(cfg, "gemini-2.5-flash")
			},
		},
		{
			name:     "ElevenLabs Scribe",
			provider: "elevenlabs",
			configure: func(cfg *Config) {
				configureASR(cfg, "scribe_v1")
			},
		},
		{
			name:     "OpenAI speech",
			provider: "openai",
			configure: func(cfg *Config) {
				configureTTS(cfg, "tts-1")
			},
		},
		{
			name:     "OpenRouter speech",
			provider: "openrouter",
			configure: func(cfg *Config) {
				configureTTS(cfg, "microsoft/mai-voice-2")
			},
		},
		{
			name:     "LiteLLM speech proxy",
			provider: "litellm",
			configure: func(cfg *Config) {
				configureTTS(cfg, "tts-1")
			},
		},
		{
			name:     "MiMo speech",
			provider: "mimo",
			configure: func(cfg *Config) {
				configureTTS(cfg, "mimo-v2-tts")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := voiceCapabilityConfig(tt.provider)
			tt.configure(cfg)
			require.NoError(t, cfg.ValidateModelSelections())
		})
	}
}

func TestValidateModelSelectionsRequiresCompleteVoiceSelections(t *testing.T) {
	t.Run("ASR alias requires account", func(t *testing.T) {
		cfg := voiceCapabilityConfig("openai")
		cfg.Voice.ModelName = "asr"
		cfg.ModelAliases = []ModelAliasConfig{{Name: "asr", Model: "whisper-1"}}

		require.ErrorContains(
			t,
			cfg.ValidateModelSelections(),
			"voice.account_ref is required when voice.model_name is configured",
		)
	})

	t.Run("TTS account requires alias", func(t *testing.T) {
		cfg := voiceCapabilityConfig("openai")
		cfg.Voice.TTSAccountRef = "voice-account"

		err := cfg.ValidateModelSelections()
		require.ErrorIs(t, err, ErrNoModelConfigured)
		require.ErrorContains(t, err, "voice.tts_model_name")
	})
}

func TestValidateVoiceModelCapabilitiesChecksEveryLoadBalancedAccount(t *testing.T) {
	cfg := voiceCapabilityConfig("openai")
	cfg.ModelList = append(cfg.ModelList, &ModelConfig{
		ModelName: "voice-account",
		Provider:  "anthropic",
		Enabled:   true,
	})
	configureTTS(cfg, "tts-1")

	require.ErrorContains(
		t,
		cfg.ValidateModelSelections(),
		`provider "anthropic" does not support speech synthesis`,
	)
}

func TestResolveVoiceModelsIgnoreDisabledDuplicateAccountRows(t *testing.T) {
	rrCounter.Store(0)

	cfg := voiceCapabilityConfig("openai")
	cfg.ModelList[0].APIBase = "https://enabled.example.test/v1"
	cfg.ModelList = append(
		[]*ModelConfig{{
			ModelName: "voice-account",
			Provider:  "anthropic",
			APIBase:   "https://disabled.example.test/v1",
			Enabled:   false,
		}},
		cfg.ModelList...,
	)
	configureASR(cfg, "whisper-1")
	configureTTS(cfg, "tts-1")

	require.NoError(t, cfg.ValidateModelSelections())
	for range 8 {
		asrConfig, err := cfg.ResolveVoiceASRModelConfig()
		require.NoError(t, err)
		require.Equal(t, "openai", asrConfig.Provider)
		require.Equal(t, "https://enabled.example.test/v1", asrConfig.APIBase)
		require.True(t, asrConfig.Enabled)

		ttsConfig, err := cfg.ResolveVoiceTTSModelConfig()
		require.NoError(t, err)
		require.Equal(t, "openai", ttsConfig.Provider)
		require.Equal(t, "https://enabled.example.test/v1", ttsConfig.APIBase)
		require.True(t, ttsConfig.Enabled)
	}
}

func TestValidateVoiceModelCapabilitiesInfersProviderFromAccountMetadata(t *testing.T) {
	cfg := voiceCapabilityConfig("")
	cfg.ModelList[0].Model = "anthropic/account-default"
	configureTTS(cfg, "claude-sonnet-4-6")

	require.ErrorContains(
		t,
		cfg.ValidateModelSelections(),
		`provider "anthropic" does not support speech synthesis`,
	)
}
