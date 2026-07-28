// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package providers

import (
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
)

// CreateProvider creates a provider based on the configuration.
// It uses the model_list configuration (new format) to create providers.
// The old providers config is automatically converted to model_list during config loading.
// Returns the provider, the model selector to keep in agent defaults, and any
// error. For account routers the selector remains the router name; the
// bootstrap provider's concrete model must not replace the router before the
// agent loop builds its account graph.
func CreateProvider(cfg *config.Config) (LLMProvider, string, error) {
	model := cfg.Agents.Defaults.GetModelName()

	if credentialCfg, ok := credentialAccountModelConfig(model, ""); ok {
		if credentialCfg == nil {
			return nil, "", fmt.Errorf("credential account %q cannot be used as the default model", model)
		}
		provider, modelID, err := CreateProviderFromConfig(credentialCfg)
		if err != nil {
			return nil, "", fmt.Errorf("failed to create provider for model %q: %w", model, err)
		}
		return provider, modelID, nil
	}

	// Must have model_list at this point
	if len(cfg.ModelList) == 0 {
		return nil, "", fmt.Errorf("no providers configured. Please add entries to model_list in your config")
	}

	// Get model config from model_list
	modelCfg, err := cfg.GetModelConfig(model)
	if err != nil {
		return nil, "", fmt.Errorf("model %q not found in model_list: %w", model, err)
	}
	if modelCfg.IsAccountRouter() {
		return createAccountRouterBootstrapProvider(cfg, model, modelCfg.Router)
	}

	// Inject global workspace if not set in model config
	if modelCfg.Workspace == "" {
		modelCfg.Workspace = cfg.WorkspacePath()
	}

	// Use factory to create provider
	provider, modelID, err := CreateProviderFromConfig(modelCfg)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create provider for model %q: %w", model, err)
	}

	return provider, modelID, nil
}

func createAccountRouterBootstrapProvider(
	cfg *config.Config,
	routerName string,
	routerCfg *config.AccountRouterConfig,
) (LLMProvider, string, error) {
	accountNames := reachableRouterAccounts(routerCfg)
	if len(accountNames) == 0 {
		return nil, "", fmt.Errorf("account router %q has no reachable account blocks", routerName)
	}

	failures := make([]string, 0, len(accountNames))
	for _, accountName := range accountNames {
		accountCfg, err := accountRouterBootstrapModelConfig(cfg, accountName)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%q: %v", accountName, err))
			continue
		}

		provider, _, err := CreateProviderFromConfig(accountCfg)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%q: %v", accountName, err))
			continue
		}
		return provider, strings.TrimSpace(routerName), nil
	}

	return nil, "", fmt.Errorf(
		"account router %q has no runnable reachable accounts (%s)",
		routerName,
		strings.Join(failures, "; "),
	)
}

func accountRouterBootstrapModelConfig(
	cfg *config.Config,
	accountName string,
) (*config.ModelConfig, error) {
	if credentialCfg, ok := credentialAccountModelConfig(accountName, ""); ok {
		if credentialCfg == nil {
			return nil, fmt.Errorf("credential account is unsupported")
		}
		return credentialCfg, nil
	}

	var match *config.ModelConfig
	for _, modelCfg := range cfg.ModelList {
		if modelCfg == nil ||
			strings.TrimSpace(modelCfg.ModelName) != strings.TrimSpace(accountName) ||
			!modelCfg.Enabled {
			continue
		}
		if match != nil {
			return nil, fmt.Errorf("account is ambiguous in model_list")
		}
		match = modelCfg
	}
	if match == nil {
		return nil, fmt.Errorf("enabled account was not found in model_list")
	}
	if match.IsAccountRouter() || match.IsModelRouter() {
		return nil, fmt.Errorf("account resolves to another router")
	}

	accountCfg := *match
	if accountCfg.Workspace == "" {
		accountCfg.Workspace = cfg.WorkspacePath()
	}
	return &accountCfg, nil
}

func credentialAccountModelConfig(accountName string, model string) (*config.ModelConfig, bool) {
	credentialID, ok := config.AccountRouterCredentialAccountID(accountName)
	if !ok {
		return nil, false
	}
	provider, ok := config.AccountRouterCredentialAccountProvider(accountName)
	if !ok {
		return nil, true
	}
	provider = credentialAccountRuntimeProvider(provider)
	model = strings.TrimSpace(model)
	if model == "" {
		model = credentialAccountDefaultModel(provider)
	}
	if model == "" {
		return nil, true
	}
	return &config.ModelConfig{
		ModelName:    strings.TrimSpace(accountName),
		Provider:     provider,
		Model:        model,
		AuthMethod:   credentialAccountRuntimeAuthMethod(provider),
		CredentialID: credentialID,
		Enabled:      true,
	}, true
}

func credentialAccountDefaultModel(provider string) string {
	return DefaultModelForProvider(provider)
}

func credentialAccountRuntimeProvider(provider string) string {
	switch strings.TrimSpace(provider) {
	case "google-antigravity":
		return "antigravity"
	default:
		return strings.TrimSpace(provider)
	}
}

func credentialAccountRuntimeAuthMethod(provider string) string {
	switch strings.TrimSpace(provider) {
	case "openai", "antigravity":
		return "oauth"
	default:
		return "token"
	}
}

func reachableRouterAccounts(routerCfg *config.AccountRouterConfig) []string {
	if routerCfg == nil {
		return nil
	}
	blocks := make(map[string]config.AccountRouterBlock, len(routerCfg.Blocks))
	for _, block := range routerCfg.Blocks {
		blocks[strings.TrimSpace(block.ID)] = block
	}

	seenBlocks := map[string]bool{}
	seenAccounts := map[string]bool{}
	accounts := make([]string, 0)
	addAccount := func(account string) {
		account = strings.TrimSpace(account)
		if account == "" || seenAccounts[account] {
			return
		}
		seenAccounts[account] = true
		accounts = append(accounts, account)
	}

	var walk func(string)
	walk = func(id string) {
		id = strings.TrimSpace(id)
		if id == "" || seenBlocks[id] {
			return
		}
		seenBlocks[id] = true
		block, ok := blocks[id]
		if !ok {
			return
		}
		switch strings.TrimSpace(block.Type) {
		case config.AccountRouterBlockTypeAccount:
			addAccount(block.Account)
			walk(block.Fallback)
		case config.AccountRouterBlockTypeLoadBalance:
			for _, account := range block.Accounts {
				addAccount(account)
			}
			walk(block.Fallback)
		case config.AccountRouterBlockTypeBranch:
			walk(block.Then)
			walk(block.Else)
			walk(block.Fallback)
		}
	}
	walk(routerCfg.Entry)
	return accounts
}
