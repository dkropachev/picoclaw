package protocoltypes

import (
	"fmt"
	"strings"
)

var canonicalProviders = map[string]string{
	"router":                   "router",
	"account-router":           "router",
	"account_router":           "router",
	"openai":                   "openai",
	"gpt":                      "openai",
	"anthropic":                "anthropic",
	"claude":                   "anthropic",
	"anthropic-messages":       "anthropic-messages",
	"gemini":                   "gemini",
	"google":                   "gemini",
	"deepseek":                 "deepseek",
	"openrouter":               "openrouter",
	"perplexity":               "perplexity",
	"qwen-portal":              "qwen-portal",
	"qwen":                     "qwen-portal",
	"qwen-intl":                "qwen-intl",
	"qwen-international":       "qwen-intl",
	"dashscope-intl":           "qwen-intl",
	"moonshot":                 "moonshot",
	"volcengine":               "volcengine",
	"zhipu":                    "zhipu",
	"glm":                      "zhipu",
	"groq":                     "groq",
	"mistral":                  "mistral",
	"nvidia":                   "nvidia",
	"cerebras":                 "cerebras",
	"azure":                    "azure",
	"azure-openai":             "azure",
	"bedrock":                  "bedrock",
	"github-copilot":           "github-copilot",
	"copilot":                  "github-copilot",
	"antigravity":              "antigravity",
	"google-antigravity":       "antigravity",
	"claude-cli":               "claude-cli",
	"claudecli":                "claude-cli",
	"codex-cli":                "codex-cli",
	"codexcli":                 "codex-cli",
	"ollama":                   "ollama",
	"vllm":                     "vllm",
	"lmstudio":                 "lmstudio",
	"gpt4free":                 "gpt4free",
	"g4f":                      "gpt4free",
	"elevenlabs":               "elevenlabs",
	"venice":                   "venice",
	"nearai":                   "nearai",
	"near-ai":                  "nearai",
	"near-ai-cloud":            "nearai",
	"shengsuanyun":             "shengsuanyun",
	"siliconflow":              "siliconflow",
	"vivgrid":                  "vivgrid",
	"minimax":                  "minimax",
	"longcat":                  "longcat",
	"modelscope":               "modelscope",
	"mimo":                     "mimo",
	"avian":                    "avian",
	"zai":                      "zai",
	"z.ai":                     "zai",
	"z-ai":                     "zai",
	"alibaba-coding":           "alibaba-coding",
	"coding-plan":              "alibaba-coding",
	"qwen-coding":              "alibaba-coding",
	"alibaba-coding-anthropic": "alibaba-coding-anthropic",
	"coding-plan-anthropic":    "alibaba-coding-anthropic",
	"novita":                   "novita",
	"litellm":                  "litellm",
	"qwen-us":                  "qwen-us",
	"dashscope-us":             "qwen-us",
}

// NormalizeProvider canonicalizes a known provider or normalizes the spelling
// of an unknown provider.
func NormalizeProvider(provider string) string {
	normalized := strings.ToLower(strings.TrimSpace(provider))
	if canonical, ok := canonicalProviders[normalized]; ok {
		return canonical
	}
	return normalized
}

// SplitKnownProviderModel separates a recognized provider prefix from a model
// identifier. Unknown prefixes remain part of the model identifier.
func SplitKnownProviderModel(model string) (provider, modelID string) {
	provider, modelID, found := strings.Cut(strings.TrimSpace(model), "/")
	if !found {
		return "", ""
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	modelID = strings.TrimSpace(modelID)
	if provider == "" {
		return "", modelID
	}
	canonical, known := canonicalProviders[provider]
	if !known {
		return "", ""
	}
	return canonical, modelID
}

// ResolveModelForProvider validates an optional provider-qualified model
// against a concrete account provider and returns the upstream model ID.
func ResolveModelForProvider(accountProvider, configuredModel string) (string, error) {
	accountProvider = NormalizeProvider(accountProvider)
	if accountProvider == "" {
		return "", fmt.Errorf("no account provider configured")
	}
	configuredModel, err := RequireModel(configuredModel)
	if err != nil {
		return "", err
	}
	modelProvider, modelID := SplitKnownProviderModel(configuredModel)
	if modelProvider == "" {
		return configuredModel, nil
	}
	if modelProvider != accountProvider {
		return "", fmt.Errorf(
			"model provider %q does not match account provider %q",
			modelProvider,
			accountProvider,
		)
	}
	return RequireModel(modelID)
}
