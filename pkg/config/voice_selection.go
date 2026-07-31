package config

import (
	"fmt"
	"strings"

	audiocapabilities "github.com/sipeed/picoclaw/pkg/audio/capabilities"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

type voiceModelSelection struct {
	label       string
	accountPath string
	accountRef  string
	aliasPath   string
	alias       string
}

func (c *Config) asrModelSelection() voiceModelSelection {
	return voiceModelSelection{
		label:       "voice transcription",
		accountPath: "voice.account_ref",
		accountRef:  strings.TrimSpace(c.Voice.AccountRef),
		aliasPath:   "voice.model_name",
		alias:       strings.TrimSpace(c.Voice.ModelName),
	}
}

func (c *Config) ttsModelSelection() voiceModelSelection {
	return voiceModelSelection{
		label:       "voice TTS",
		accountPath: "voice.tts_account_ref",
		accountRef:  strings.TrimSpace(c.Voice.TTSAccountRef),
		aliasPath:   "voice.tts_model_name",
		alias:       strings.TrimSpace(c.Voice.TTSModelName),
	}
}

func (selection voiceModelSelection) validatePresence() (bool, error) {
	switch {
	case selection.accountRef == "" && selection.alias == "":
		return false, nil
	case selection.accountRef == "":
		return false, fmt.Errorf(
			"%s is required when %s is configured",
			selection.accountPath,
			selection.aliasPath,
		)
	case selection.alias == "":
		return false, fmt.Errorf("%s: %w", selection.aliasPath, ErrNoModelConfigured)
	default:
		return true, nil
	}
}

type voiceCapabilityValidator func(provider, modelID string) error

func validateASRCapability(provider, modelID string) error {
	_, err := audiocapabilities.ResolveASRRoute(provider, modelID)
	return err
}

func validateTTSCapability(provider, modelID string) error {
	_, err := audiocapabilities.ResolveTTSRoute(provider, modelID)
	return err
}

// ValidateVoiceModelCapabilities verifies that each configured ASR and TTS
// account/alias pair resolves to a transport implemented by the audio runtime.
// Every enabled entry sharing a concrete account name is checked so load
// balancing cannot select an incompatible provider later.
func (c *Config) ValidateVoiceModelCapabilities() error {
	if c == nil {
		return nil
	}
	if err := c.validateVoiceModelCapability(
		c.asrModelSelection(),
		validateASRCapability,
	); err != nil {
		return err
	}
	return c.validateVoiceModelCapability(
		c.ttsModelSelection(),
		validateTTSCapability,
	)
}

func (c *Config) validateVoiceModelCapability(
	selection voiceModelSelection,
	validateCapability voiceCapabilityValidator,
) error {
	configured, err := selection.validatePresence()
	if err != nil || !configured {
		return err
	}

	model, err := c.ResolveModelAlias(selection.alias, selection.accountRef)
	if err != nil {
		return fmt.Errorf("%s: %w", selection.label, err)
	}

	found := false
	for _, account := range c.ModelList {
		if account == nil ||
			!account.Enabled ||
			account.IsAccountRouter() ||
			account.IsModelRouter() ||
			strings.TrimSpace(account.ModelName) != selection.accountRef {
			continue
		}
		found = true

		provider, modelID, resolveErr := resolveVoiceProviderModel(account, model)
		if resolveErr != nil {
			return fmt.Errorf(
				"%s account %q with model alias %q: %w",
				selection.label,
				selection.accountRef,
				selection.alias,
				resolveErr,
			)
		}
		if capabilityErr := validateCapability(provider, modelID); capabilityErr != nil {
			return fmt.Errorf(
				"%s account %q with model alias %q: %w",
				selection.label,
				selection.accountRef,
				selection.alias,
				capabilityErr,
			)
		}
	}
	if !found {
		return fmt.Errorf(
			"%s account %q is not configured or enabled",
			selection.label,
			selection.accountRef,
		)
	}
	return nil
}

// ResolveVoiceASRModelConfig resolves and normalizes the explicitly configured
// voice transcription account and model alias. A nil config with no error
// means ASR is intentionally not configured.
func (c *Config) ResolveVoiceASRModelConfig() (*ModelConfig, error) {
	if c == nil {
		return nil, nil
	}
	return c.resolveVoiceModelConfig(c.asrModelSelection(), validateASRCapability)
}

// ResolveVoiceTTSModelConfig resolves and normalizes the explicitly configured
// speech synthesis account and model alias. A nil config with no error means
// TTS is intentionally not configured.
func (c *Config) ResolveVoiceTTSModelConfig() (*ModelConfig, error) {
	if c == nil {
		return nil, nil
	}
	return c.resolveVoiceModelConfig(c.ttsModelSelection(), validateTTSCapability)
}

func (c *Config) resolveVoiceModelConfig(
	selection voiceModelSelection,
	validateCapability voiceCapabilityValidator,
) (*ModelConfig, error) {
	configured, err := selection.validatePresence()
	if err != nil || !configured {
		return nil, err
	}

	model, err := c.ResolveModelAlias(selection.alias, selection.accountRef)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", selection.label, err)
	}
	account, err := c.GetEnabledModelConfig(selection.accountRef)
	if err != nil {
		return nil, fmt.Errorf(
			"%s account %q is not configured: %w",
			selection.label,
			selection.accountRef,
			err,
		)
	}
	if account.IsAccountRouter() || account.IsModelRouter() {
		return nil, fmt.Errorf(
			"%s account %q is not a concrete account",
			selection.label,
			selection.accountRef,
		)
	}
	provider, modelID, err := resolveVoiceProviderModel(account, model)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", selection.label, err)
	}
	if err := validateCapability(provider, modelID); err != nil {
		return nil, fmt.Errorf(
			"%s account %q with model alias %q: %w",
			selection.label,
			selection.accountRef,
			selection.alias,
			err,
		)
	}
	modelCfg := *account
	modelCfg.Provider = provider
	modelCfg.Model = modelID
	return &modelCfg, nil
}

func resolveVoiceProviderModel(
	account *ModelConfig,
	configuredModel string,
) (provider, modelID string, err error) {
	if account == nil {
		return "", "", fmt.Errorf("model config is nil")
	}

	provider = protocoltypes.NormalizeProvider(account.Provider)
	if provider == "" {
		provider, _ = protocoltypes.SplitKnownProviderModel(account.Model)
		if provider == "" {
			provider = "openai"
		}
	}
	modelID, err = protocoltypes.ResolveModelForProvider(provider, configuredModel)
	if err != nil {
		return "", "", err
	}
	return provider, modelID, nil
}
