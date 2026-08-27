// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package providers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/isolation"
	anthropicmessages "github.com/sipeed/picoclaw/pkg/providers/anthropic_messages"
	"github.com/sipeed/picoclaw/pkg/providers/azure"
	"github.com/sipeed/picoclaw/pkg/providers/bedrock"
	"github.com/sipeed/picoclaw/pkg/providers/common"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

// createClaudeAuthProvider creates a Claude provider using OAuth credentials from auth store.
func createClaudeAuthProvider(cfg *config.ModelConfig) (LLMProvider, error) {
	credentialID, err := credentialIDForModel(cfg, "anthropic")
	if err != nil {
		return nil, err
	}
	cred, err := getCredential(credentialID)
	if err != nil {
		return nil, fmt.Errorf("loading auth credentials: %w", err)
	}
	if cred == nil {
		return nil, fmt.Errorf(
			"no credentials for %s. Run: picoclaw auth login --provider anthropic --credential-id %s",
			credentialID,
			credentialID,
		)
	}
	if err := validateCredentialProvider(credentialID, "anthropic", cred); err != nil {
		return nil, err
	}
	return NewClaudeProviderWithTokenSource(cred.AccessToken, createClaudeTokenSourceForCredential(credentialID)), nil
}

// createCodexAuthProvider creates a Codex provider using OAuth credentials from auth store.
func createCodexAuthProvider(cfg *config.ModelConfig) (LLMProvider, error) {
	credentialID, err := credentialIDForModel(cfg, "openai")
	if err != nil {
		return nil, err
	}
	cred, err := getCredential(credentialID)
	if err != nil {
		return nil, fmt.Errorf("loading auth credentials: %w", err)
	}
	if cred == nil {
		return nil, fmt.Errorf(
			"no credentials for %s. Run: picoclaw auth login --provider openai --credential-id %s",
			credentialID,
			credentialID,
		)
	}
	if err := validateCredentialProvider(credentialID, "openai", cred); err != nil {
		return nil, err
	}
	return NewCodexProviderWithTokenSource(
		cred.AccessToken,
		cred.AccountID,
		createCodexTokenSourceForCredential(credentialID),
	), nil
}

// createAntigravityAuthProvider creates an Antigravity provider using a named
// Google Antigravity OAuth credential from the auth store.
func createAntigravityAuthProvider(cfg *config.ModelConfig) (LLMProvider, error) {
	credentialID, err := credentialIDForModel(cfg, "google-antigravity")
	if err != nil {
		return nil, err
	}
	cred, err := getCredential(credentialID)
	if err != nil {
		return nil, fmt.Errorf("loading auth credentials: %w", err)
	}
	if cred == nil {
		return nil, fmt.Errorf(
			"no credentials for %s. Run: picoclaw auth login --provider google-antigravity --credential-id %s",
			credentialID,
			credentialID,
		)
	}
	if err := validateCredentialProvider(credentialID, "google-antigravity", cred); err != nil {
		return nil, err
	}
	return newAntigravityProviderForCredential(credentialID), nil
}

// createGitHubCopilotAuthProvider creates a GitHub Copilot provider using a
// named GitHub token from the auth store.
func createGitHubCopilotAuthProvider(cfg *config.ModelConfig, modelID string) (LLMProvider, error) {
	credentialID, err := credentialIDForModel(cfg, "github-copilot")
	if err != nil {
		return nil, err
	}
	cred, err := getCredential(credentialID)
	if err != nil {
		return nil, fmt.Errorf("loading auth credentials: %w", err)
	}
	if cred == nil {
		return nil, fmt.Errorf(
			"no credentials for %s. Run: picoclaw auth login --provider github-copilot --credential-id %s",
			credentialID,
			credentialID,
		)
	}
	if validationErr := validateCredentialProvider(credentialID, "github-copilot", cred); validationErr != nil {
		return nil, validationErr
	}
	token := strings.TrimSpace(cred.AccessToken)
	if validationErr := auth.ValidateGitHubCopilotToken(token); validationErr != nil {
		return nil, fmt.Errorf("invalid GitHub Copilot credential %s: %w", credentialID, validationErr)
	}
	provider, err := newGitHubCopilotProviderWithToken(token, modelID)
	if err != nil {
		return nil, err
	}
	reloadable, ok := provider.(interface {
		SetTokenSource(tokenSource func() (string, error))
	})
	if !ok {
		return nil, fmt.Errorf("GitHub Copilot token provider does not support credential reload")
	}
	reloadable.SetTokenSource(newCredentialAccessTokenSource(credentialID, "github-copilot"))
	return provider, nil
}

func credentialIDForModel(cfg *config.ModelConfig, provider string) (string, error) {
	if cfg == nil {
		return auth.NormalizeCredentialID(provider, "")
	}
	return auth.NormalizeCredentialID(provider, cfg.CredentialID)
}

func validateCredentialProvider(
	credentialID string,
	requestedProvider string,
	cred *auth.AuthCredential,
) error {
	if cred == nil {
		return fmt.Errorf("credential %s is missing", credentialID)
	}
	expectedProvider, err := auth.NormalizeCredentialID(requestedProvider, "")
	if err != nil {
		return fmt.Errorf("normalizing provider %q: %w", requestedProvider, err)
	}
	actualProvider, err := auth.NormalizeCredentialID(cred.Provider, "")
	if err != nil || actualProvider != expectedProvider {
		return fmt.Errorf(
			"credential %s belongs to provider %q, want %q",
			credentialID,
			cred.Provider,
			expectedProvider,
		)
	}
	return nil
}

func loadCredentialAccessToken(
	lookup func(string) (*auth.AuthCredential, error),
	credentialID string,
	provider string,
) (string, error) {
	cred, err := lookup(credentialID)
	if err != nil {
		return "", fmt.Errorf("loading auth credentials: %w", err)
	}
	if cred == nil {
		return "", fmt.Errorf(
			"no credentials for %s. Run: picoclaw auth login --provider %s --credential-id %s",
			credentialID,
			provider,
			credentialID,
		)
	}
	if err := validateCredentialProvider(credentialID, provider, cred); err != nil {
		return "", err
	}
	return strings.TrimSpace(cred.AccessToken), nil
}

func newCredentialAccessTokenSource(credentialID, provider string) func() (string, error) {
	lookup := getCredential
	return func() (string, error) {
		return loadCredentialAccessToken(lookup, credentialID, provider)
	}
}

func configureCredentialAPIKeySource(
	provider interface {
		SetAPIKeySource(apiKeySource func() (string, error))
	},
	cfg *config.ModelConfig,
	providerName string,
) error {
	if strings.TrimSpace(cfg.APIKey()) != "" ||
		strings.ToLower(strings.TrimSpace(cfg.AuthMethod)) != "token" {
		return nil
	}
	credentialID, err := credentialIDForModel(cfg, providerName)
	if err != nil {
		return err
	}
	provider.SetAPIKeySource(newCredentialAccessTokenSource(credentialID, providerName))
	return nil
}

// ExtractProtocol extracts the effective protocol and model identifier from a
// model configuration.
//
// The explicit Provider field takes precedence. When Provider is empty, the
// protocol is inferred from Model. Plain model names default to "openai".
// Provider-prefixed models strip the first slash-separated segment from the
// returned model ID.
//
// The returned protocol is normalized to the provider's canonical spelling.
// Examples:
//   - Model "openai/gpt-4o" -> ("openai", "gpt-4o")
//   - Model "nvidia/z-ai/glm-5.1" -> ("nvidia", "z-ai/glm-5.1")
//   - Provider "nvidia", Model "z-ai/glm-5.1" -> ("nvidia", "z-ai/glm-5.1")
//   - Provider "openai", Model "openai/gpt-4o" -> ("openai", "openai/gpt-4o")
//   - Model "gpt-4o" -> ("openai", "gpt-4o")
func ExtractProtocol(cfg *config.ModelConfig) (protocol, modelID string) {
	if cfg == nil {
		return "", ""
	}

	model := strings.TrimSpace(cfg.Model)
	if provider := strings.TrimSpace(cfg.Provider); provider != "" {
		return NormalizeProvider(provider), model
	}
	return SplitModelProviderAndID(model, "openai")
}

// ResolveAPIBase returns the configured API base, or the protocol default when
// the model uses an HTTP-based provider family with a known default endpoint.
func ResolveAPIBase(cfg *config.ModelConfig) string {
	if cfg == nil {
		return ""
	}
	if apiBase := strings.TrimSpace(cfg.APIBase); apiBase != "" {
		return strings.TrimRight(apiBase, "/")
	}
	protocol, _ := ExtractProtocol(cfg)
	return strings.TrimRight(getDefaultAPIBase(protocol), "/")
}

// CreateProviderFromConfig creates a provider based on the ModelConfig.
// It uses ExtractProtocol to determine which provider to create.
// Supported protocol families include OpenAI-compatible prefixes (e.g., openai, openrouter, groq),
// Azure OpenAI, Amazon Bedrock, Anthropic (including messages), and various CLI/compatibility shims.
// Generic OpenAI-compatible families are classified by protocoltypes; the
// switch below handles providers with specialized construction.
// Returns the provider, the effective model ID from ExtractProtocol, and any error.
func CreateProviderFromConfig(cfg *config.ModelConfig) (LLMProvider, string, error) {
	return CreateProviderFromConfigWithExecutionPolicy(
		cfg,
		isolation.NewExecutionPolicy(config.DefaultConfig().Isolation),
	)
}

// CreateProviderFromConfigWithExecutionPolicy creates a provider bound to one
// exact subprocess execution policy.
func CreateProviderFromConfigWithExecutionPolicy(
	cfg *config.ModelConfig,
	policy isolation.ExecutionPolicy,
) (LLMProvider, string, error) {
	if cfg == nil {
		return nil, "", fmt.Errorf("config is nil")
	}

	if _, err := protocoltypes.RequireModel(cfg.Model); err != nil {
		return nil, "", err
	}

	protocol, modelID := ExtractProtocol(cfg)
	authMethod := strings.ToLower(strings.TrimSpace(cfg.AuthMethod))

	userAgent := cfg.UserAgent
	if userAgent == "" {
		userAgent = fmt.Sprintf("PicoClaw/%s", config.Version)
	}

	if protocol != "openai" &&
		protocol != "minimax" &&
		protocoltypes.UsesOpenAICompatibleHTTPTransport(protocol) {
		apiKey, err := apiKeyFromConfigOrCredential(cfg, protocol)
		if err != nil {
			return nil, "", err
		}
		if apiKey == "" && cfg.APIBase == "" && !isEmptyAPIKeyAllowed(protocol) {
			return nil, "", fmt.Errorf("api_key or api_base is required for HTTP-based protocol %q", protocol)
		}
		apiBase := cfg.APIBase
		if apiBase == "" {
			apiBase = getDefaultAPIBase(protocol)
		}
		provider := NewHTTPProviderWithMaxTokensFieldAndRequestTimeout(
			apiKey,
			apiBase,
			cfg.Proxy,
			cfg.MaxTokensField,
			userAgent,
			cfg.RequestTimeout,
			cfg.ExtraBody,
			cfg.CustomHeaders,
		)
		provider.SetProviderName(protocol)
		if err := configureCredentialAPIKeySource(provider, cfg, protocol); err != nil {
			return nil, "", err
		}
		return finalizeProviderFromConfig(provider, modelID, cfg)
	}

	switch protocol {
	case config.AccountRouterProvider:
		return nil, "", fmt.Errorf("provider %q is a router config, not an upstream provider", protocol)
	case "openai":
		// OpenAI with OAuth/token auth (Codex-style)
		if authMethod == "oauth" || authMethod == "token" {
			provider, err := createCodexAuthProvider(cfg)
			if err != nil {
				return nil, "", err
			}
			return finalizeProviderFromConfig(provider, modelID, cfg)
		}
		// OpenAI with API key
		if cfg.APIKey() == "" && cfg.APIBase == "" {
			return nil, "", fmt.Errorf("api_key or api_base is required for HTTP-based protocol %q", protocol)
		}
		apiBase := cfg.APIBase
		if apiBase == "" {
			apiBase = getDefaultAPIBase(protocol)
		}
		provider := NewHTTPProviderWithMaxTokensFieldAndRequestTimeout(
			cfg.APIKey(),
			apiBase,
			cfg.Proxy,
			cfg.MaxTokensField,
			userAgent,
			cfg.RequestTimeout,
			cfg.ExtraBody,
			cfg.CustomHeaders,
		)
		provider.SetProviderName(protocol)
		return finalizeProviderFromConfig(provider, modelID, cfg)

	case "azure":
		// Azure OpenAI uses deployment-based URLs. Auth is Bearer token via api_key
		// when set; otherwise falls back to Entra ID (DefaultAzureCredential).
		if cfg.APIBase == "" {
			return nil, "", fmt.Errorf(
				"api_base is required for azure protocol (e.g., https://your-resource.openai.azure.com)",
			)
		}
		if cfg.APIKey() != "" {
			return finalizeProviderFromConfig(azure.NewProviderWithTimeout(
				cfg.APIKey(),
				cfg.APIBase,
				cfg.Proxy,
				userAgent,
				cfg.RequestTimeout,
			), modelID, cfg)
		}
		if authMethod == "token" {
			if _, err := apiKeyFromConfigOrCredential(cfg, protocol); err != nil {
				return nil, "", err
			}
			credentialID, err := credentialIDForModel(cfg, protocol)
			if err != nil {
				return nil, "", err
			}
			tokenSource := newCredentialAccessTokenSource(credentialID, protocol)
			return finalizeProviderFromConfig(azure.NewProviderWithTokenSource(
				cfg.APIBase,
				cfg.Proxy,
				userAgent,
				func(context.Context) (string, error) { return tokenSource() },
				azure.WithRequestTimeout(time.Duration(cfg.RequestTimeout)*time.Second),
			), modelID, cfg)
		}
		provider, err := azure.NewProviderWithIdentityAndTimeout(
			cfg.APIBase,
			cfg.Proxy,
			userAgent,
			cfg.RequestTimeout,
		)
		if err != nil {
			return nil, "", err
		}
		return finalizeProviderFromConfig(provider, modelID, cfg)

	case "bedrock":
		// AWS Bedrock uses AWS SDK credentials (env vars, profiles, IAM roles, etc.)
		// api_base can be:
		//   - A full endpoint URL: https://bedrock-runtime.us-east-1.amazonaws.com
		//   - A region name: us-east-1 (AWS SDK resolves endpoint automatically)
		var opts []bedrock.Option
		if cfg.APIBase != "" {
			if !strings.Contains(cfg.APIBase, "://") {
				// Treat as region: let AWS SDK resolve the correct endpoint
				// (supports all AWS partitions: aws, aws-cn, aws-us-gov, etc.)
				opts = append(opts, bedrock.WithRegion(cfg.APIBase))
			} else {
				// Full endpoint URL provided (for custom endpoints or testing)
				opts = append(opts, bedrock.WithBaseEndpoint(cfg.APIBase))
			}
		}
		// Use a separate timeout for AWS config loading (credential resolution can block)
		initTimeout := 30 * time.Second
		if cfg.RequestTimeout > 0 {
			reqTimeout := time.Duration(cfg.RequestTimeout) * time.Second
			// Set request timeout for API calls
			opts = append(opts, bedrock.WithRequestTimeout(reqTimeout))
			// Ensure init timeout is at least as large as request timeout
			if reqTimeout > initTimeout {
				initTimeout = reqTimeout
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), initTimeout)
		defer cancel()
		// Note: AWS_PROFILE env var is automatically used by AWS SDK
		provider, err := bedrock.NewProvider(ctx, opts...)
		if err != nil {
			return nil, "", fmt.Errorf("creating bedrock provider: %w", err)
		}
		return finalizeProviderFromConfig(provider, modelID, cfg)

	case "gemini":
		apiKey, err := apiKeyFromConfigOrCredential(cfg, protocol)
		if err != nil {
			return nil, "", err
		}
		if apiKey == "" && cfg.APIBase == "" {
			return nil, "", fmt.Errorf("api_key or api_base is required for gemini protocol (model: %s)", cfg.Model)
		}
		apiBase := cfg.APIBase
		if apiBase == "" {
			apiBase = getDefaultAPIBase(protocol)
		}
		provider := NewGeminiProvider(
			apiKey,
			apiBase,
			cfg.Proxy,
			userAgent,
			cfg.RequestTimeout,
			cfg.ExtraBody,
			cfg.CustomHeaders,
		)
		if err := configureCredentialAPIKeySource(provider, cfg, protocol); err != nil {
			return nil, "", err
		}
		return finalizeProviderFromConfig(provider, modelID, cfg)

	case "minimax":
		// Minimax requires reasoning_split: true in the request body
		apiKey, err := apiKeyFromConfigOrCredential(cfg, protocol)
		if err != nil {
			return nil, "", err
		}
		if apiKey == "" && cfg.APIBase == "" {
			return nil, "", fmt.Errorf("api_key or api_base is required for HTTP-based protocol %q", protocol)
		}
		apiBase := cfg.APIBase
		if apiBase == "" {
			apiBase = getDefaultAPIBase(protocol)
		}
		extraBody := cfg.ExtraBody
		if extraBody == nil {
			extraBody = make(map[string]any)
		}
		if _, ok := extraBody["reasoning_split"]; !ok {
			extraBody["reasoning_split"] = true
		}
		provider := NewHTTPProviderWithMaxTokensFieldAndRequestTimeout(
			apiKey,
			apiBase,
			cfg.Proxy,
			cfg.MaxTokensField,
			userAgent,
			cfg.RequestTimeout,
			extraBody,
			cfg.CustomHeaders,
		)
		provider.SetProviderName(protocol)
		if err := configureCredentialAPIKeySource(provider, cfg, protocol); err != nil {
			return nil, "", err
		}
		return finalizeProviderFromConfig(provider, modelID, cfg)

	case "anthropic":
		if authMethod == "oauth" || authMethod == "token" {
			// Use OAuth credentials from auth store
			provider, err := createClaudeAuthProvider(cfg)
			if err != nil {
				return nil, "", err
			}
			return finalizeProviderFromConfig(provider, modelID, cfg)
		}
		// Use API key with HTTP API
		apiBase := common.NormalizeBaseURL(cfg.APIBase, "https://api.anthropic.com/v1", true)
		if cfg.APIKey() == "" {
			return nil, "", fmt.Errorf("api_key is required for anthropic protocol (model: %s)", cfg.Model)
		}
		provider := NewHTTPProviderWithMaxTokensFieldAndRequestTimeout(
			cfg.APIKey(),
			apiBase,
			cfg.Proxy,
			cfg.MaxTokensField,
			userAgent,
			cfg.RequestTimeout,
			cfg.ExtraBody,
			cfg.CustomHeaders,
		)
		provider.SetProviderName(protocol)
		return finalizeProviderFromConfig(provider, modelID, cfg)

	case "anthropic-messages":
		// Anthropic Messages API with native format (HTTP-based, no SDK)
		apiBase := cfg.APIBase
		if apiBase == "" {
			apiBase = "https://api.anthropic.com/v1"
		}
		apiKey, err := apiKeyFromConfigOrCredential(cfg, protocol)
		if err != nil {
			return nil, "", err
		}
		if apiKey == "" {
			return nil, "", fmt.Errorf("api_key is required for anthropic-messages protocol (model: %s)", cfg.Model)
		}
		provider := anthropicmessages.NewProviderWithTimeout(
			apiKey,
			apiBase,
			userAgent,
			cfg.RequestTimeout,
		)
		if err := configureCredentialAPIKeySource(provider, cfg, protocol); err != nil {
			return nil, "", err
		}
		return finalizeProviderFromConfig(provider, modelID, cfg)

	case "alibaba-coding-anthropic":
		// Alibaba Coding Plan with Anthropic-compatible API
		apiBase := cfg.APIBase
		if apiBase == "" {
			apiBase = getDefaultAPIBase(protocol)
		}
		apiKey, err := apiKeyFromConfigOrCredential(cfg, protocol)
		if err != nil {
			return nil, "", err
		}
		if apiKey == "" {
			return nil, "", fmt.Errorf("api_key is required for %q protocol (model: %s)", protocol, cfg.Model)
		}
		provider := anthropicmessages.NewProviderWithTimeout(
			apiKey,
			apiBase,
			userAgent,
			cfg.RequestTimeout,
		)
		if err := configureCredentialAPIKeySource(provider, cfg, protocol); err != nil {
			return nil, "", err
		}
		return finalizeProviderFromConfig(provider, modelID, cfg)

	case "antigravity":
		provider, err := createAntigravityAuthProvider(cfg)
		if err != nil {
			return nil, "", err
		}
		return finalizeProviderFromConfig(provider, modelID, cfg)

	case "claude-cli":
		workspace := cfg.Workspace
		if workspace == "" {
			workspace = "."
		}
		return finalizeProviderFromConfig(
			NewClaudeCliProviderWithExecutionPolicy(workspace, policy),
			modelID,
			cfg,
		)

	case "codex-cli":
		workspace := cfg.Workspace
		if workspace == "" {
			workspace = "."
		}
		return finalizeProviderFromConfig(
			NewCodexCliProviderWithExecutionPolicy(workspace, policy),
			modelID,
			cfg,
		)

	case "github-copilot":
		if authMethod == "oauth" || authMethod == "token" {
			provider, err := createGitHubCopilotAuthProvider(cfg, modelID)
			if err != nil {
				return nil, "", err
			}
			return finalizeProviderFromConfig(provider, modelID, cfg)
		}

		apiBase := cfg.APIBase
		if apiBase == "" {
			apiBase = "localhost:4321"
		}
		connectMode := cfg.ConnectMode
		if connectMode == "" {
			connectMode = "grpc"
		}
		provider, err := NewGitHubCopilotProvider(apiBase, connectMode, modelID)
		if err != nil {
			return nil, "", err
		}
		return finalizeProviderFromConfig(provider, modelID, cfg)

	default:
		return nil, "", fmt.Errorf("unknown protocol %q in model %q", protocol, cfg.Model)
	}
}

func finalizeProviderFromConfig(
	provider LLMProvider,
	modelID string,
	cfg *config.ModelConfig,
) (LLMProvider, string, error) {
	wrapped, err := wrapProviderWithToolSchemaTransform(provider, cfg.ToolSchemaTransform)
	if err != nil {
		return nil, "", err
	}
	return wrapped, modelID, nil
}

func apiKeyFromConfigOrCredential(cfg *config.ModelConfig, provider string) (string, error) {
	apiKey := strings.TrimSpace(cfg.APIKey())
	if apiKey != "" {
		return apiKey, nil
	}
	authMethod := strings.ToLower(strings.TrimSpace(cfg.AuthMethod))
	if authMethod != "token" {
		return "", nil
	}
	credentialID, err := credentialIDForModel(cfg, provider)
	if err != nil {
		return "", err
	}
	return loadCredentialAccessToken(getCredential, credentialID, provider)
}

func isEmptyAPIKeyAllowed(protocol string) bool {
	option, ok := modelProviderOptionForName(protocol)
	return ok && option.EmptyAPIKeyAllowed
}

// IsEmptyAPIKeyAllowedForProtocol reports whether a protocol allows requests
// without api_key when using its default local endpoint.
func IsEmptyAPIKeyAllowedForProtocol(protocol string) bool {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	return isEmptyAPIKeyAllowed(protocol)
}

// IsHTTPAPIProtocol reports whether a provider uses an HTTP API base in the
// model configuration path. This excludes providers such as Bedrock, CLI
// bridges, and OAuth-only managed providers even if they do not require an
// explicit api_key field.
func IsHTTPAPIProtocol(protocol string) bool {
	protocol = NormalizeProvider(protocol)
	option, ok := modelProviderOptionsByName[protocol]
	return ok && option.httpAPI
}

// DefaultAPIBaseForProtocol returns the configured default API base for a protocol.
// It returns empty string if the protocol has no default base.
func DefaultAPIBaseForProtocol(protocol string) string {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	return getDefaultAPIBase(protocol)
}

// getDefaultAPIBase returns the default API base URL for a given protocol.
func getDefaultAPIBase(protocol string) string {
	option, ok := modelProviderOptionForName(protocol)
	if !ok {
		return ""
	}
	return option.DefaultAPIBase
}
