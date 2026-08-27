// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package providers

import (
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/isolation"
)

// CreateProvider creates a provider based on the configuration.
// It uses the model_list configuration (new format) to create providers.
// The old providers config is automatically converted to model_list during config loading.
// Returns the provider, the model alias to keep in agent defaults, and any
// error. Account selection and model selection are independent: account
// routers choose a concrete account, then the configured alias resolves the
// model for that account.
func CreateProvider(cfg *config.Config) (LLMProvider, string, error) {
	isolationCfg := config.DefaultConfig().Isolation
	if cfg != nil {
		isolationCfg = cfg.Isolation
	}
	return CreateProviderWithExecutionPolicy(
		cfg,
		isolation.NewExecutionPolicy(isolationCfg),
	)
}

// CreateProviderWithExecutionPolicy creates a provider bound to one exact
// subprocess execution policy.
func CreateProviderWithExecutionPolicy(
	cfg *config.Config,
	policy isolation.ExecutionPolicy,
) (LLMProvider, string, error) {
	if cfg == nil {
		return nil, "", fmt.Errorf("config is nil")
	}
	modelAlias := strings.TrimSpace(cfg.Agents.Defaults.GetModelName())
	if modelAlias == "" {
		return nil, "", config.ErrNoModelConfigured
	}
	requestedSelector := modelAlias
	if terminalAlias, ok := firstModelRouterTerminalAlias(cfg, modelAlias); ok {
		modelAlias = terminalAlias
	}
	accountRef := strings.TrimSpace(cfg.Agents.Defaults.AccountRef)
	if accountRef == "" {
		return nil, "", fmt.Errorf("no account configured")
	}

	if routerCfg := accountRouterConfigByName(cfg, accountRef); routerCfg != nil {
		provider, _, err := createAccountRouterBootstrapProvider(
			cfg,
			accountRef,
			modelAlias,
			routerCfg,
			policy,
		)
		return provider, requestedSelector, err
	}

	modelID, err := cfg.ResolveModelAlias(modelAlias, accountRef)
	if err != nil {
		return nil, "", err
	}
	if credentialCfg, ok := credentialAccountModelConfig(accountRef, modelID); ok {
		if credentialCfg == nil {
			return nil, "", fmt.Errorf("credential account %q is not supported", accountRef)
		}
		provider, _, createErr := CreateProviderFromConfigWithExecutionPolicy(credentialCfg, policy)
		if createErr != nil {
			return nil, "", fmt.Errorf(
				"failed to create provider for model alias %q with account %q: %w",
				modelAlias,
				accountRef,
				createErr,
			)
		}
		return provider, requestedSelector, nil
	}

	if len(cfg.ModelList) == 0 {
		return nil, "", fmt.Errorf("no accounts configured")
	}

	accountCfg, err := cfg.GetEnabledModelConfig(accountRef)
	if err != nil {
		return nil, "", fmt.Errorf("account %q not found in model_list: %w", accountRef, err)
	}
	if accountCfg.IsAccountRouter() || accountCfg.IsModelRouter() {
		return nil, "", fmt.Errorf("account %q is not a concrete account", accountRef)
	}

	modelCfg, err := resolvedAccountModelConfig(accountCfg, modelID)
	if err != nil {
		return nil, "", fmt.Errorf(
			"failed to resolve model alias %q with account %q: %w",
			modelAlias,
			accountRef,
			err,
		)
	}
	if modelCfg.Workspace == "" {
		modelCfg.Workspace = cfg.WorkspacePath()
	}

	provider, _, err := CreateProviderFromConfigWithExecutionPolicy(modelCfg, policy)
	if err != nil {
		return nil, "", fmt.Errorf(
			"failed to create provider for model alias %q with account %q: %w",
			modelAlias,
			accountRef,
			err,
		)
	}

	return provider, requestedSelector, nil
}

func firstModelRouterTerminalAlias(
	cfg *config.Config,
	selector string,
) (string, bool) {
	if cfg == nil {
		return "", false
	}
	selector = strings.TrimSpace(selector)
	for i := range cfg.ModelRouters {
		router := &cfg.ModelRouters[i]
		if !router.Enabled || strings.TrimSpace(router.Name) != selector {
			continue
		}
		blocks := make(map[string]config.ModelRouterBlock, len(router.Blocks))
		for _, block := range router.Blocks {
			blocks[strings.TrimSpace(block.ID)] = block
		}
		visited := make(map[string]bool, len(blocks))
		var first func(string) string
		first = func(blockID string) string {
			blockID = strings.TrimSpace(blockID)
			if blockID == "" || visited[blockID] {
				return ""
			}
			visited[blockID] = true
			block, ok := blocks[blockID]
			if !ok {
				return ""
			}
			if strings.TrimSpace(block.Type) == config.ModelRouterBlockTypeModel {
				return strings.TrimSpace(block.Model)
			}
			for _, rule := range block.Rules {
				if alias := first(rule.Target); alias != "" {
					return alias
				}
			}
			return first(block.Fallback)
		}
		if alias := first(router.Entry); alias != "" {
			return alias, true
		}
		return "", false
	}
	return "", false
}

func createAccountRouterBootstrapProvider(
	cfg *config.Config,
	routerName string,
	modelAlias string,
	routerCfg *config.AccountRouterConfig,
	policy isolation.ExecutionPolicy,
) (LLMProvider, string, error) {
	accountNames := reachableRouterAccounts(routerCfg)
	if len(accountNames) == 0 {
		return nil, "", fmt.Errorf("account router %q has no reachable account blocks", routerName)
	}

	failures := make([]string, 0, len(accountNames))
	for _, accountName := range accountNames {
		accountCfg, err := accountRouterBootstrapModelConfig(
			cfg,
			accountName,
			modelAlias,
		)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%q: %v", accountName, err))
			continue
		}

		provider, _, err := CreateProviderFromConfigWithExecutionPolicy(accountCfg, policy)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%q: %v", accountName, err))
			continue
		}
		return provider, strings.TrimSpace(modelAlias), nil
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
	modelAlias string,
) (*config.ModelConfig, error) {
	modelID, err := cfg.ResolveModelAlias(modelAlias, accountName)
	if err != nil {
		return nil, err
	}
	if credentialCfg, ok := credentialAccountModelConfig(accountName, modelID); ok {
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

	accountCfg, err := resolvedAccountModelConfig(match, modelID)
	if err != nil {
		return nil, err
	}
	if accountCfg.Workspace == "" {
		accountCfg.Workspace = cfg.WorkspacePath()
	}
	return accountCfg, nil
}

func resolvedAccountModelConfig(
	accountCfg *config.ModelConfig,
	resolvedModel string,
) (*config.ModelConfig, error) {
	if accountCfg == nil {
		return nil, fmt.Errorf("account config is required")
	}
	accountProvider, _ := ExtractProtocol(accountCfg)
	accountProvider = NormalizeProvider(accountProvider)
	if accountProvider == "" {
		return nil, fmt.Errorf("account provider is required")
	}
	resolvedModel, err := ResolveModelForProvider(accountProvider, resolvedModel)
	if err != nil {
		return nil, err
	}
	clone := *accountCfg
	clone.Provider = accountProvider
	clone.Model = resolvedModel
	return &clone, nil
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

func credentialAccountRuntimeProvider(provider string) string {
	switch strings.TrimSpace(provider) {
	case "google-antigravity":
		return "antigravity"
	default:
		return strings.TrimSpace(provider)
	}
}

func accountRouterConfigByName(
	cfg *config.Config,
	name string,
) *config.AccountRouterConfig {
	if cfg == nil {
		return nil
	}
	name = strings.TrimSpace(name)
	for i := range cfg.AccountRouters {
		if cfg.AccountRouters[i].Enabled &&
			strings.TrimSpace(cfg.AccountRouters[i].Name) == name {
			return &cfg.AccountRouters[i]
		}
	}
	if modelCfg, err := cfg.GetEnabledModelConfig(name); err == nil &&
		modelCfg != nil &&
		modelCfg.IsAccountRouter() {
		return modelCfg.Router
	}
	return nil
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
