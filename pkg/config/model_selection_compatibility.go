package config

import (
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

// ValidateModelSelectionCompatibility verifies that every concrete account and
// terminal model alias reachable from a configured runtime selection can be
// used together. Model aliases may bridge provider families only through an
// explicit override for each concrete account.
func (c *Config) ValidateModelSelectionCompatibility() error {
	if c == nil {
		return nil
	}

	defaultAccount := strings.TrimSpace(c.Agents.Defaults.AccountRef)
	if err := c.validateSelectionPolicyCompatibility(
		defaultAccount,
		strings.TrimSpace(c.Agents.Defaults.ModelName),
		c.Agents.Defaults.ModelFallbacks,
		true,
	); err != nil {
		return fmt.Errorf("agents.defaults: %w", err)
	}
	if err := c.validateSelectionPolicyCompatibility(
		defaultAccount,
		strings.TrimSpace(c.Agents.Defaults.ImageModel),
		c.Agents.Defaults.ImageModelFallbacks,
		true,
	); err != nil {
		return fmt.Errorf("agents.defaults image model: %w", err)
	}
	if routing := c.Agents.Defaults.Routing; routing != nil {
		if err := c.validateSelectionPolicyCompatibility(
			defaultAccount,
			strings.TrimSpace(routing.LightModel),
			nil,
			true,
		); err != nil {
			return fmt.Errorf("agents.defaults.routing.light_model: %w", err)
		}
	}

	for i := range c.Agents.List {
		agent := &c.Agents.List[i]
		accountRef := strings.TrimSpace(agent.AccountRef)
		if accountRef == "" {
			accountRef = defaultAccount
		}

		primary := strings.TrimSpace(c.Agents.Defaults.ModelName)
		fallbacks := c.Agents.Defaults.ModelFallbacks
		if agent.Model != nil {
			if value := strings.TrimSpace(agent.Model.Primary); value != "" {
				primary = value
			}
			if agent.Model.Fallbacks != nil {
				fallbacks = agent.Model.Fallbacks
			}
		}
		if err := c.validateSelectionPolicyCompatibility(
			accountRef,
			primary,
			fallbacks,
			true,
		); err != nil {
			return fmt.Errorf("agents.list[%d] %q: %w", i, agent.ID, err)
		}

		// Image and light-model settings belong to the defaults object, but
		// they run through each agent's effective account selection.
		if err := c.validateSelectionPolicyCompatibility(
			accountRef,
			strings.TrimSpace(c.Agents.Defaults.ImageModel),
			c.Agents.Defaults.ImageModelFallbacks,
			true,
		); err != nil {
			return fmt.Errorf("agents.list[%d] %q image model: %w", i, agent.ID, err)
		}
		if routing := c.Agents.Defaults.Routing; routing != nil {
			if err := c.validateSelectionPolicyCompatibility(
				accountRef,
				strings.TrimSpace(routing.LightModel),
				nil,
				true,
			); err != nil {
				return fmt.Errorf(
					"agents.list[%d] %q routing.light_model: %w",
					i,
					agent.ID,
					err,
				)
			}
		}

		if agent.Subagents != nil {
			subagentPrimary := primary
			subagentFallbacks := fallbacks
			if agent.Subagents.Model != nil {
				if value := strings.TrimSpace(agent.Subagents.Model.Primary); value != "" {
					subagentPrimary = value
				}
				if agent.Subagents.Model.Fallbacks != nil {
					subagentFallbacks = agent.Subagents.Model.Fallbacks
				}
			}
			if err := c.validateSelectionPolicyCompatibility(
				accountRef,
				subagentPrimary,
				subagentFallbacks,
				true,
			); err != nil {
				return fmt.Errorf("agents.list[%d] %q subagents: %w", i, agent.ID, err)
			}
		}
	}

	if err := c.validateSelectionPolicyCompatibility(
		strings.TrimSpace(c.Voice.AccountRef),
		strings.TrimSpace(c.Voice.ModelName),
		nil,
		false,
	); err != nil {
		return fmt.Errorf("voice transcription: %w", err)
	}
	if err := c.validateSelectionPolicyCompatibility(
		strings.TrimSpace(c.Voice.TTSAccountRef),
		strings.TrimSpace(c.Voice.TTSModelName),
		nil,
		false,
	); err != nil {
		return fmt.Errorf("voice TTS: %w", err)
	}
	return nil
}

func (c *Config) validateSelectionPolicyCompatibility(
	accountSelector string,
	primarySelector string,
	fallbackAliases []string,
	requireChatProvider bool,
) error {
	accountSelector = strings.TrimSpace(accountSelector)
	primarySelector = strings.TrimSpace(primarySelector)
	if accountSelector != "" && primarySelector != "" {
		if err := c.validateSelectionCompatibility(
			accountSelector,
			primarySelector,
			requireChatProvider,
		); err != nil {
			return err
		}
	}
	for _, fallback := range fallbackAliases {
		fallback = strings.TrimSpace(fallback)
		if accountSelector == "" || fallback == "" {
			continue
		}
		if err := c.validateSelectionCompatibility(
			accountSelector,
			fallback,
			requireChatProvider,
		); err != nil {
			return fmt.Errorf("fallback %q: %w", fallback, err)
		}
	}
	return nil
}

func (c *Config) validateSelectionCompatibility(
	accountSelector string,
	modelSelector string,
	requireChatProvider bool,
) error {
	accounts, err := c.concreteAccountsForSelection(accountSelector)
	if err != nil {
		return err
	}
	aliases, err := c.terminalAliasesForSelection(modelSelector)
	if err != nil {
		return err
	}
	for _, accountRef := range accounts {
		for _, alias := range aliases {
			if err := c.validateConcreteAccountAliasCompatibility(
				accountRef,
				alias,
				requireChatProvider,
			); err != nil {
				return fmt.Errorf(
					"model alias %q with account %q: %w",
					alias,
					accountRef,
					err,
				)
			}
		}
	}
	return nil
}

func (c *Config) concreteAccountsForSelection(accountSelector string) ([]string, error) {
	accountSelector = strings.TrimSpace(accountSelector)
	if accountSelector == "" {
		return nil, fmt.Errorf("account_ref is required")
	}
	if _, ok := AccountRouterCredentialAccountProvider(accountSelector); ok {
		return []string{accountSelector}, nil
	}
	for i := range c.AccountRouters {
		router := &c.AccountRouters[i]
		if strings.TrimSpace(router.Name) != accountSelector {
			continue
		}
		if !router.Enabled {
			return nil, fmt.Errorf("account router %q is disabled", accountSelector)
		}
		accounts := reachableAccountRouterAccounts(router)
		if len(accounts) == 0 {
			return nil, fmt.Errorf(
				"account router %q has no reachable accounts",
				accountSelector,
			)
		}
		return accounts, nil
	}
	return []string{accountSelector}, nil
}

func (c *Config) terminalAliasesForSelection(modelSelector string) ([]string, error) {
	modelSelector = strings.TrimSpace(modelSelector)
	if modelSelector == "" {
		return nil, ErrNoModelConfigured
	}
	if _, err := c.GetModelAlias(modelSelector); err == nil {
		return []string{modelSelector}, nil
	}
	for i := range c.ModelRouters {
		router := &c.ModelRouters[i]
		if strings.TrimSpace(router.Name) != modelSelector {
			continue
		}
		if !router.Enabled {
			return nil, fmt.Errorf("model router %q is disabled", modelSelector)
		}
		aliases := reachableModelRouterAliases(router)
		if len(aliases) == 0 {
			return nil, fmt.Errorf(
				"model router %q has no reachable terminal aliases",
				modelSelector,
			)
		}
		return aliases, nil
	}
	return nil, fmt.Errorf("model alias %q is not configured", modelSelector)
}

func reachableAccountRouterAccounts(router *AccountRouterConfig) []string {
	if router == nil {
		return nil
	}
	blocks := make(map[string]AccountRouterBlock, len(router.Blocks))
	for _, block := range router.Blocks {
		if id := strings.TrimSpace(block.ID); id != "" {
			blocks[id] = block
		}
	}

	seenBlocks := make(map[string]bool, len(blocks))
	seenAccounts := make(map[string]bool)
	accounts := make([]string, 0)
	addAccount := func(accountRef string) {
		accountRef = strings.TrimSpace(accountRef)
		if accountRef == "" || seenAccounts[accountRef] {
			return
		}
		seenAccounts[accountRef] = true
		accounts = append(accounts, accountRef)
	}

	var walk func(string)
	walk = func(blockID string) {
		blockID = strings.TrimSpace(blockID)
		if blockID == "" || seenBlocks[blockID] {
			return
		}
		block, ok := blocks[blockID]
		if !ok {
			return
		}
		seenBlocks[blockID] = true

		switch strings.TrimSpace(block.Type) {
		case AccountRouterBlockTypeAccount:
			addAccount(block.Account)
		case AccountRouterBlockTypeLoadBalance:
			for _, accountRef := range block.Accounts {
				addAccount(accountRef)
			}
		case AccountRouterBlockTypeBranch:
			walk(block.Then)
			walk(block.Else)
		}
		walk(block.Fallback)
	}
	walk(router.Entry)
	return accounts
}

func reachableModelRouterAliases(router *ModelRouterConfig) []string {
	if router == nil {
		return nil
	}
	blocks := make(map[string]ModelRouterBlock, len(router.Blocks))
	for _, block := range router.Blocks {
		if id := strings.TrimSpace(block.ID); id != "" {
			blocks[id] = block
		}
	}

	seenBlocks := make(map[string]bool, len(blocks))
	seenAliases := make(map[string]bool)
	aliases := make([]string, 0)
	var walk func(string)
	walk = func(blockID string) {
		blockID = strings.TrimSpace(blockID)
		if blockID == "" || seenBlocks[blockID] {
			return
		}
		block, ok := blocks[blockID]
		if !ok {
			return
		}
		seenBlocks[blockID] = true
		if strings.TrimSpace(block.Type) == ModelRouterBlockTypeModel {
			alias := strings.TrimSpace(block.Model)
			if alias != "" && !seenAliases[alias] {
				seenAliases[alias] = true
				aliases = append(aliases, alias)
			}
			return
		}
		for _, rule := range block.Rules {
			walk(rule.Target)
		}
		walk(block.Fallback)
	}
	walk(router.Entry)
	return aliases
}

func (c *Config) validateConcreteAccountAliasCompatibility(
	accountRef string,
	modelAlias string,
	requireChatProvider bool,
) error {
	model, err := c.ResolveModelAlias(modelAlias, accountRef)
	if err != nil {
		return err
	}

	if provider, ok := AccountRouterCredentialAccountProvider(accountRef); ok {
		return validateModelProviderCompatibility(
			provider,
			model,
			requireChatProvider,
		)
	}

	found := false
	for _, account := range c.ModelList {
		if account == nil ||
			strings.TrimSpace(account.ModelName) != accountRef ||
			!account.Enabled ||
			account.IsAccountRouter() ||
			account.IsModelRouter() {
			continue
		}
		found = true
		provider := strings.TrimSpace(account.Provider)
		if provider == "" {
			provider, _ = protocoltypes.SplitKnownProviderModel(account.Model)
			if provider == "" {
				provider = "openai"
			}
		}
		if err := validateModelProviderCompatibility(
			provider,
			model,
			requireChatProvider,
		); err != nil {
			return err
		}
	}
	if !found {
		return fmt.Errorf("account is not configured or enabled")
	}
	return nil
}

func validateModelProviderCompatibility(
	provider string,
	model string,
	requireChatProvider bool,
) error {
	provider = protocoltypes.NormalizeProvider(provider)
	if _, err := protocoltypes.ResolveModelForProvider(provider, model); err != nil {
		return err
	}
	if requireChatProvider && provider == "elevenlabs" {
		return fmt.Errorf("provider %q is not usable for chat", provider)
	}
	return nil
}
