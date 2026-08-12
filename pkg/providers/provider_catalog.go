package providers

import (
	"sort"
	"strings"

	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

// ModelProviderOptions returns the canonical provider catalog exposed to the Web UI.
func ModelProviderOptions() []ModelProviderOption {
	options := make([]ModelProviderOption, 0, len(modelProviderOptionsByName))
	for _, option := range modelProviderOptionsByName {
		options = append(options, option)
	}
	sort.Slice(options, func(i, j int) bool {
		return options[i].ID < options[j].ID
	})
	return options
}

// IsSupportedModelProvider reports whether provider resolves to a provider ID
// returned by ModelProviderOptions.
func IsSupportedModelProvider(provider string) bool {
	_, ok := modelProviderOptionForName(provider)
	return ok
}

// IsModelProviderFetchable reports whether provider supports upstream /models
// listing through the launcher fetch endpoint.
func IsModelProviderFetchable(provider string) bool {
	option, ok := modelProviderOptionForName(provider)
	return ok && option.SupportsFetch
}

// CommonModelsForProvider returns a defensive copy of the curated model IDs for
// a provider. Unknown providers return nil.
func CommonModelsForProvider(provider string) []string {
	option, ok := modelProviderOptionForName(provider)
	if !ok || len(option.CommonModels) == 0 {
		return nil
	}
	models := make([]string, len(option.CommonModels))
	copy(models, option.CommonModels)
	return models
}

// IsCreatableModelProvider reports whether provider can be selected for a new
// model entry from the Web UI.
func IsCreatableModelProvider(provider string) bool {
	option, ok := modelProviderOptionForName(provider)
	return ok && option.CreateAllowed
}

// SupportsAccountStoreCredentials reports whether the provider's runtime can
// consume a credential managed by the account auth store. Most HTTP API
// providers support a stored token/API key. Managed providers with specialized
// transports are listed explicitly; ElevenLabs remains model-config-only until
// its ASR runtime can resolve account-store credentials.
func SupportsAccountStoreCredentials(provider string) bool {
	option, ok := modelProviderOptionForName(provider)
	if !ok || !option.CreateAllowed {
		return false
	}

	switch option.ID {
	case "openai", "anthropic", "antigravity", "github-copilot":
		return true
	case "elevenlabs":
		return false
	default:
		return option.httpAPI
	}
}

// SplitModelProviderAndID separates a legacy "provider/model" string into its
// effective provider and canonical model ID. Unknown prefixes are treated as
// part of the model ID and fall back to defaultProvider.
func SplitModelProviderAndID(model, defaultProvider string) (provider, modelID string) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", ""
	}

	provider, modelID = splitKnownProviderModel(model)
	if provider != "" || modelID != "" {
		return provider, modelID
	}

	return NormalizeProvider(defaultProvider), model
}

// ResolveModelForProvider resolves a configured concrete model for a selected
// account provider. A recognized provider prefix is treated as an explicit
// constraint and must match the account. Unknown prefixes remain part of the
// upstream model ID (for example, provider-specific namespaces).
func ResolveModelForProvider(accountProvider, configuredModel string) (string, error) {
	return protocoltypes.ResolveModelForProvider(accountProvider, configuredModel)
}

func splitKnownProviderModel(model string) (provider, modelID string) {
	return protocoltypes.SplitKnownProviderModel(model)
}
