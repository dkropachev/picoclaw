package config

import (
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

// ValidateModelSelections validates every runtime account and model selector.
//
// Empty selectors are valid so a first-run configuration can be loaded before
// an account and model are chosen. Non-empty model selectors must name an exact
// configured alias. The only exception is a primary chat selector: defaults,
// an agent primary, and a subagent primary may name an enabled model router.
// Fallback, image, light-routing, ASR, and TTS selectors are always alias-only.
func (c *Config) ValidateModelSelections() error {
	if c == nil {
		return nil
	}

	validator := newModelSelectionValidator(c)
	if err := validator.validateAccountRef(
		"agents.defaults.account_ref",
		c.Agents.Defaults.AccountRef,
		true,
	); err != nil {
		return err
	}
	if err := validator.validateModelSelector(
		"agents.defaults.model_name",
		c.Agents.Defaults.ModelName,
		true,
	); err != nil {
		return err
	}
	if err := validator.validateAliasList(
		"agents.defaults.model_fallbacks",
		c.Agents.Defaults.ModelFallbacks,
	); err != nil {
		return err
	}
	if err := validator.validateModelSelector(
		"agents.defaults.image_model",
		c.Agents.Defaults.ImageModel,
		false,
	); err != nil {
		return err
	}
	if err := validator.validateAliasList(
		"agents.defaults.image_model_fallbacks",
		c.Agents.Defaults.ImageModelFallbacks,
	); err != nil {
		return err
	}
	if c.Agents.Defaults.Routing != nil {
		if err := validator.validateModelSelector(
			"agents.defaults.routing.light_model",
			c.Agents.Defaults.Routing.LightModel,
			false,
		); err != nil {
			return err
		}
	}

	for i, account := range c.ModelList {
		if account == nil {
			continue
		}
		if err := validator.validateModelSelector(
			fmt.Sprintf("model_list[%d].subscription_equivalent_model", i),
			account.SubscriptionEquivalentModel,
			false,
		); err != nil {
			return err
		}
	}

	for i := range c.Agents.List {
		agent := &c.Agents.List[i]
		prefix := fmt.Sprintf("agents.list[%d]", i)
		if err := validator.validateAccountRef(
			prefix+".account_ref",
			agent.AccountRef,
			true,
		); err != nil {
			return err
		}
		if agent.Model != nil {
			if err := validator.validateModelSelector(
				prefix+".model.primary",
				agent.Model.Primary,
				true,
			); err != nil {
				return err
			}
			if err := validator.validateAliasList(
				prefix+".model.fallbacks",
				agent.Model.Fallbacks,
			); err != nil {
				return err
			}
		}
		if agent.Subagents != nil && agent.Subagents.Model != nil {
			if err := validator.validateModelSelector(
				prefix+".subagents.model.primary",
				agent.Subagents.Model.Primary,
				true,
			); err != nil {
				return err
			}
			if err := validator.validateAliasList(
				prefix+".subagents.model.fallbacks",
				agent.Subagents.Model.Fallbacks,
			); err != nil {
				return err
			}
		}
	}

	if err := validator.validateConcreteModelListAccountRef(
		"voice.account_ref",
		c.Voice.AccountRef,
	); err != nil {
		return err
	}
	if err := validator.validateModelSelector(
		"voice.model_name",
		c.Voice.ModelName,
		false,
	); err != nil {
		return err
	}
	if err := validator.validateConcreteModelListAccountRef(
		"voice.tts_account_ref",
		c.Voice.TTSAccountRef,
	); err != nil {
		return err
	}
	if err := validator.validateModelSelector(
		"voice.tts_model_name",
		c.Voice.TTSModelName,
		false,
	); err != nil {
		return err
	}
	if err := c.validateWebSearchModelAlias(
		"tools.web.gemini.model_alias",
		c.Tools.Web.Gemini.ModelAlias,
		c.Tools.Web.Gemini.Enabled,
		"gemini",
	); err != nil {
		return err
	}
	if err := c.validateWebSearchModelAlias(
		"tools.web.perplexity.model_alias",
		c.Tools.Web.Perplexity.ModelAlias,
		c.Tools.Web.Perplexity.Enabled,
		"perplexity",
	); err != nil {
		return err
	}
	if err := c.validateSubscriptionEquivalentModelGraph(); err != nil {
		return err
	}
	if err := c.ValidateModelSelectionCompatibility(); err != nil {
		return err
	}
	return c.ValidateVoiceModelCapabilities()
}

type modelSelectionValidator struct {
	aliases             map[string]struct{}
	enabledModelRouters map[string]struct{}
	disabledModelRouter map[string]struct{}
	enabledAccounts     map[string]struct{}
	disabledAccounts    map[string]struct{}
	enabledRouters      map[string]struct{}
	disabledRouters     map[string]struct{}
}

func newModelSelectionValidator(c *Config) modelSelectionValidator {
	validator := modelSelectionValidator{
		aliases:             make(map[string]struct{}, len(c.ModelAliases)),
		enabledModelRouters: make(map[string]struct{}, len(c.ModelRouters)),
		disabledModelRouter: make(map[string]struct{}, len(c.ModelRouters)),
		enabledAccounts:     make(map[string]struct{}, len(c.ModelList)),
		disabledAccounts:    make(map[string]struct{}, len(c.ModelList)),
		enabledRouters:      make(map[string]struct{}, len(c.AccountRouters)),
		disabledRouters:     make(map[string]struct{}, len(c.AccountRouters)),
	}
	for i := range c.ModelAliases {
		validator.aliases[c.ModelAliases[i].Name] = struct{}{}
	}
	for i := range c.ModelRouters {
		router := &c.ModelRouters[i]
		name := strings.TrimSpace(router.Name)
		if router.Enabled {
			validator.enabledModelRouters[name] = struct{}{}
		} else {
			validator.disabledModelRouter[name] = struct{}{}
		}
	}
	for i := range c.AccountRouters {
		router := &c.AccountRouters[i]
		name := strings.TrimSpace(router.Name)
		if router.Enabled {
			validator.enabledRouters[name] = struct{}{}
		} else {
			validator.disabledRouters[name] = struct{}{}
		}
	}
	for _, account := range c.ModelList {
		if account == nil {
			continue
		}
		name := account.ModelName
		switch {
		case account.IsModelRouter():
			if account.Enabled {
				validator.enabledModelRouters[name] = struct{}{}
			} else {
				validator.disabledModelRouter[name] = struct{}{}
			}
		case account.IsAccountRouter():
			if account.Enabled {
				validator.enabledRouters[name] = struct{}{}
			} else {
				validator.disabledRouters[name] = struct{}{}
			}
		case account.Enabled:
			validator.enabledAccounts[name] = struct{}{}
		default:
			validator.disabledAccounts[name] = struct{}{}
		}
	}
	return validator
}

func (v modelSelectionValidator) validateModelSelector(
	path string,
	selector string,
	allowModelRouter bool,
) error {
	if strings.TrimSpace(selector) == "" {
		return nil
	}
	if _, ok := v.aliases[selector]; ok {
		return nil
	}
	if _, ok := v.enabledModelRouters[selector]; ok {
		if allowModelRouter {
			return nil
		}
		return fmt.Errorf(
			"%s %q references a model router; an exact model alias is required",
			path,
			selector,
		)
	}
	if _, ok := v.disabledModelRouter[selector]; ok {
		return fmt.Errorf("%s %q references a disabled model router", path, selector)
	}
	return fmt.Errorf("%s %q is not a configured model alias", path, selector)
}

func (v modelSelectionValidator) validateAliasList(path string, selectors []string) error {
	for i, selector := range selectors {
		if err := v.validateModelSelector(
			fmt.Sprintf("%s[%d]", path, i),
			selector,
			false,
		); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) validateWebSearchModelAlias(
	path string,
	selector string,
	required bool,
	provider string,
) error {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		if required {
			return fmt.Errorf("%s: %w", path, ErrNoModelConfigured)
		}
		return nil
	}
	alias, err := c.GetModelAlias(selector)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if _, err := protocoltypes.ResolveModelForProvider(provider, alias.Model); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func (v modelSelectionValidator) validateAccountRef(
	path string,
	accountRef string,
	allowAccountRouter bool,
) error {
	if strings.TrimSpace(accountRef) == "" {
		return nil
	}
	if accountRef != strings.TrimSpace(accountRef) {
		return fmt.Errorf("%s %q is not an exact configured account reference", path, accountRef)
	}
	if _, ok := v.enabledModelRouters[accountRef]; ok {
		return fmt.Errorf("%s %q references a model router, not an account", path, accountRef)
	}
	if _, ok := v.disabledModelRouter[accountRef]; ok {
		return fmt.Errorf("%s %q references a model router, not an account", path, accountRef)
	}
	if _, ok := v.enabledRouters[accountRef]; ok {
		if allowAccountRouter {
			return nil
		}
		return fmt.Errorf("%s %q references an account router; a concrete account is required", path, accountRef)
	}
	if _, ok := v.disabledRouters[accountRef]; ok {
		return fmt.Errorf("%s %q references a disabled account router", path, accountRef)
	}
	if _, ok := v.enabledAccounts[accountRef]; ok {
		return nil
	}
	if _, ok := v.disabledAccounts[accountRef]; ok {
		return fmt.Errorf("%s %q references a disabled account", path, accountRef)
	}
	if _, ok := AccountRouterCredentialAccountProvider(accountRef); ok {
		return nil
	}
	if _, credential := AccountRouterCredentialAccountID(accountRef); credential {
		return fmt.Errorf("%s %q references an unsupported credential account", path, accountRef)
	}
	return fmt.Errorf("%s %q is not a configured account", path, accountRef)
}

func (v modelSelectionValidator) validateConcreteModelListAccountRef(
	path string,
	accountRef string,
) error {
	if err := v.validateAccountRef(path, accountRef, false); err != nil {
		return err
	}
	if strings.TrimSpace(accountRef) == "" {
		return nil
	}
	if _, credential := AccountRouterCredentialAccountID(accountRef); credential {
		return fmt.Errorf(
			"%s %q references a credential account; an enabled concrete model_list account is required",
			path,
			accountRef,
		)
	}
	return nil
}

func (c *Config) validateConcreteModelAccountRef(accountRef string) error {
	if strings.TrimSpace(accountRef) == "" {
		return fmt.Errorf("no account configured")
	}
	if c == nil {
		return fmt.Errorf("account %q is not a configured account", accountRef)
	}
	validator := newModelSelectionValidator(c)
	if err := validator.validateAccountRef("account", accountRef, false); err != nil {
		return err
	}
	return nil
}
