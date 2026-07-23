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
// Returns the provider, the model ID to use, and any error.
func CreateProvider(cfg *config.Config) (LLMProvider, string, error) {
	model := cfg.Agents.Defaults.GetModelName()

	// Must have model_list at this point
	if len(cfg.ModelList) == 0 {
		return nil, "", fmt.Errorf("no providers configured. Please add entries to model_list in your config")
	}

	// Get model config from model_list
	modelCfg, err := cfg.GetModelConfig(model)
	if err != nil {
		return nil, "", fmt.Errorf("model %q not found in model_list: %w", model, err)
	}
	if modelCfg.IsModelRouter() {
		accountName := firstRouterAccount(modelCfg.Router)
		if accountName == "" {
			return nil, "", fmt.Errorf("router model %q has no account blocks", model)
		}
		modelCfg, err = cfg.GetModelConfig(accountName)
		if err != nil {
			return nil, "", fmt.Errorf(
				"router model %q account %q not found in model_list: %w",
				model,
				accountName,
				err,
			)
		}
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

func firstRouterAccount(routerCfg *config.ModelRouterConfig) string {
	if routerCfg == nil {
		return ""
	}
	blocks := make(map[string]config.ModelRouterBlock, len(routerCfg.Blocks))
	for _, block := range routerCfg.Blocks {
		blocks[strings.TrimSpace(block.ID)] = block
	}
	seen := map[string]bool{}
	id := strings.TrimSpace(routerCfg.Entry)
	for id != "" && !seen[id] {
		seen[id] = true
		block, ok := blocks[id]
		if !ok {
			return ""
		}
		switch strings.TrimSpace(block.Type) {
		case config.ModelRouterBlockTypeAccount:
			return strings.TrimSpace(block.Account)
		case config.ModelRouterBlockTypeLoadBalance:
			for _, account := range block.Accounts {
				if account = strings.TrimSpace(account); account != "" {
					return account
				}
			}
			id = strings.TrimSpace(block.Fallback)
		default:
			return ""
		}
	}
	return ""
}
