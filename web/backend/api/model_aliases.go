package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func normalizeModelAlias(alias *config.ModelAliasConfig) error {
	if alias == nil {
		return fmt.Errorf("model alias is required")
	}
	alias.Name = strings.TrimSpace(alias.Name)
	alias.Model = strings.TrimSpace(alias.Model)
	if len(alias.AccountOverrides) == 0 {
		alias.AccountOverrides = nil
		return nil
	}
	overrides := make(map[string]string, len(alias.AccountOverrides))
	for rawAccountRef, rawModel := range alias.AccountOverrides {
		accountRef := strings.TrimSpace(rawAccountRef)
		model := strings.TrimSpace(rawModel)
		if _, duplicate := overrides[accountRef]; duplicate {
			return fmt.Errorf("duplicate account override %q", accountRef)
		}
		overrides[accountRef] = model
	}
	alias.AccountOverrides = overrides
	return nil
}

func decodeModelAliasRequest(r *http.Request) (config.ModelAliasConfig, error) {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return config.ModelAliasConfig{}, fmt.Errorf("read request body: %w", err)
	}
	var alias config.ModelAliasConfig
	if err := json.Unmarshal(body, &alias); err != nil {
		return config.ModelAliasConfig{}, fmt.Errorf("invalid JSON: %w", err)
	}
	if err := normalizeModelAlias(&alias); err != nil {
		return config.ModelAliasConfig{}, err
	}
	return alias, nil
}

func (h *Handler) handleAddModelAlias(w http.ResponseWriter, r *http.Request) {
	h.configMutationMu.Lock()
	defer h.configMutationMu.Unlock()

	alias, err := decodeModelAliasRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	if _, err := cfg.GetModelAlias(alias.Name); err == nil {
		http.Error(w, fmt.Sprintf("model alias %q already exists", alias.Name), http.StatusConflict)
		return
	}
	cfg.ModelAliases = append(cfg.ModelAliases, alias)
	if err := validateAPIModelConfiguration(cfg); err != nil {
		http.Error(w, fmt.Sprintf("Validation error: %v", err), http.StatusBadRequest)
		return
	}
	if !saveModelConfigMutation(w, h.configPath, cfg, revision) {
		return
	}
	writeJSON(w, map[string]any{
		"status": "ok",
		"index":  len(cfg.ModelAliases) - 1,
	})
}

func (h *Handler) handleUpdateModelAlias(w http.ResponseWriter, r *http.Request) {
	h.configMutationMu.Lock()
	defer h.configMutationMu.Unlock()

	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		http.Error(w, "Invalid index", http.StatusBadRequest)
		return
	}
	alias, err := decodeModelAliasRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	if index < 0 || index >= len(cfg.ModelAliases) {
		http.Error(w, fmt.Sprintf("Index %d out of range", index), http.StatusNotFound)
		return
	}
	if !requireModelListRevision(w, r, revision) {
		return
	}
	existingName := cfg.ModelAliases[index].Name
	if alias.Name != existingName {
		http.Error(
			w,
			"model alias names are immutable; create a new alias before deleting the old one",
			http.StatusBadRequest,
		)
		return
	}
	cfg.ModelAliases[index] = alias
	if err := validateAPIModelConfiguration(cfg); err != nil {
		http.Error(w, fmt.Sprintf("Validation error: %v", err), http.StatusBadRequest)
		return
	}
	if !saveModelConfigMutation(w, h.configPath, cfg, revision) {
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (h *Handler) handleDeleteModelAlias(w http.ResponseWriter, r *http.Request) {
	h.configMutationMu.Lock()
	defer h.configMutationMu.Unlock()

	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		http.Error(w, "Invalid index", http.StatusBadRequest)
		return
	}
	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}
	if index < 0 || index >= len(cfg.ModelAliases) {
		http.Error(w, fmt.Sprintf("Index %d out of range", index), http.StatusNotFound)
		return
	}
	if !requireModelListRevision(w, r, revision) {
		return
	}
	name := cfg.ModelAliases[index].Name
	if references := modelAliasReferences(cfg, name); len(references) > 0 {
		http.Error(
			w,
			fmt.Sprintf("model alias %q is still referenced by %s", name, strings.Join(references, ", ")),
			http.StatusConflict,
		)
		return
	}
	cfg.ModelAliases = append(cfg.ModelAliases[:index], cfg.ModelAliases[index+1:]...)
	if err := validateAPIModelConfiguration(cfg); err != nil {
		http.Error(w, fmt.Sprintf("Validation error: %v", err), http.StatusBadRequest)
		return
	}
	if !saveModelConfigMutation(w, h.configPath, cfg, revision) {
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func saveModelConfigMutation(
	w http.ResponseWriter,
	path string,
	cfg *config.Config,
	expectedRevision string,
) bool {
	if _, err := config.SaveConfigIfRevision(path, cfg, expectedRevision); err != nil {
		if errors.Is(err, config.ErrConfigRevisionMismatch) {
			http.Error(w, "Configuration changed; reload and try again", http.StatusConflict)
			return false
		}
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return false
	}
	return true
}

func modelAliasOrRouterConfigured(cfg *config.Config, name string) bool {
	if cfg == nil {
		return false
	}
	if _, err := cfg.GetModelAlias(strings.TrimSpace(name)); err == nil {
		return true
	}
	for i := range cfg.ModelRouters {
		if cfg.ModelRouters[i].Enabled &&
			strings.TrimSpace(cfg.ModelRouters[i].Name) == strings.TrimSpace(name) {
			return true
		}
	}
	return false
}

func validateAPIModelConfiguration(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("model configuration is required")
	}
	if err := cfg.ValidateModelList(); err != nil {
		return err
	}
	if err := cfg.ValidateModelSelections(); err != nil {
		return err
	}
	return validateConfiguredModelSelectionGraphs(cfg)
}

func validateConfiguredModelSelectionGraphs(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("model configuration is required")
	}
	defaultAccount := strings.TrimSpace(cfg.Agents.Defaults.AccountRef)
	if err := validateSelectionPolicyGraph(
		cfg,
		defaultAccount,
		strings.TrimSpace(cfg.Agents.Defaults.ModelName),
		cfg.Agents.Defaults.ModelFallbacks,
		true,
	); err != nil {
		return fmt.Errorf("agents.defaults: %w", err)
	}
	if err := validateSelectionPolicyGraph(
		cfg,
		defaultAccount,
		strings.TrimSpace(cfg.Agents.Defaults.ImageModel),
		cfg.Agents.Defaults.ImageModelFallbacks,
		true,
	); err != nil {
		return fmt.Errorf("agents.defaults image model: %w", err)
	}
	if routing := cfg.Agents.Defaults.Routing; routing != nil {
		if err := validateSelectionPolicyGraph(
			cfg,
			defaultAccount,
			strings.TrimSpace(routing.LightModel),
			nil,
			true,
		); err != nil {
			return fmt.Errorf("agents.defaults.routing.light_model: %w", err)
		}
	}

	for i := range cfg.Agents.List {
		agent := &cfg.Agents.List[i]
		accountRef := strings.TrimSpace(agent.AccountRef)
		if accountRef == "" {
			accountRef = defaultAccount
		}
		primary := strings.TrimSpace(cfg.Agents.Defaults.ModelName)
		fallbacks := cfg.Agents.Defaults.ModelFallbacks
		if agent.Model != nil {
			if value := strings.TrimSpace(agent.Model.Primary); value != "" {
				primary = value
			}
			if agent.Model.Fallbacks != nil {
				fallbacks = agent.Model.Fallbacks
			}
		}
		if err := validateSelectionPolicyGraph(
			cfg,
			accountRef,
			primary,
			fallbacks,
			true,
		); err != nil {
			return fmt.Errorf("agent %q: %w", agent.ID, err)
		}
		if agent.Subagents != nil && agent.Subagents.Model != nil {
			if err := validateSelectionPolicyGraph(
				cfg,
				accountRef,
				strings.TrimSpace(agent.Subagents.Model.Primary),
				agent.Subagents.Model.Fallbacks,
				true,
			); err != nil {
				return fmt.Errorf("agent %q subagents: %w", agent.ID, err)
			}
		}
	}

	if err := validateSelectionPolicyGraph(
		cfg,
		strings.TrimSpace(cfg.Voice.AccountRef),
		strings.TrimSpace(cfg.Voice.ModelName),
		nil,
		false,
	); err != nil {
		return fmt.Errorf("voice transcription: %w", err)
	}
	if err := validateSelectionPolicyGraph(
		cfg,
		strings.TrimSpace(cfg.Voice.TTSAccountRef),
		strings.TrimSpace(cfg.Voice.TTSModelName),
		nil,
		false,
	); err != nil {
		return fmt.Errorf("voice TTS: %w", err)
	}
	return nil
}

func validateSelectionPolicyGraph(
	cfg *config.Config,
	accountSelector string,
	primarySelector string,
	fallbackAliases []string,
	requireChatProvider bool,
) error {
	accountSelector = strings.TrimSpace(accountSelector)
	primarySelector = strings.TrimSpace(primarySelector)
	if accountSelector != "" && primarySelector != "" {
		if err := validateSelectionGraph(
			cfg,
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
		if err := validateSelectionGraph(
			cfg,
			accountSelector,
			fallback,
			requireChatProvider,
		); err != nil {
			return fmt.Errorf("fallback %q: %w", fallback, err)
		}
	}
	return nil
}

func validateSelectionGraph(
	cfg *config.Config,
	accountSelector string,
	modelSelector string,
	requireChatProvider bool,
) error {
	accounts, err := concreteAccountsForSelection(cfg, accountSelector)
	if err != nil {
		return err
	}
	aliases, err := terminalAliasesForSelection(cfg, modelSelector)
	if err != nil {
		return err
	}
	for _, accountRef := range accounts {
		for _, alias := range aliases {
			if err := validateConcreteAccountAliasPair(
				cfg,
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

func concreteAccountsForSelection(
	cfg *config.Config,
	accountSelector string,
) ([]string, error) {
	accountSelector = strings.TrimSpace(accountSelector)
	if accountSelector == "" {
		return nil, fmt.Errorf("account_ref is required")
	}
	if _, ok := config.AccountRouterCredentialAccountProvider(accountSelector); ok {
		return []string{accountSelector}, nil
	}
	if index := findAccountRouterIndex(cfg, accountSelector); index >= 0 {
		router := &cfg.AccountRouters[index]
		if !router.Enabled {
			return nil, fmt.Errorf("account router %q is disabled", accountSelector)
		}
		accounts := reachableAccountRouterRefs(router)
		if len(accounts) == 0 {
			return nil, fmt.Errorf("account router %q has no reachable accounts", accountSelector)
		}
		return accounts, nil
	}
	return []string{accountSelector}, nil
}

func terminalAliasesForSelection(
	cfg *config.Config,
	modelSelector string,
) ([]string, error) {
	modelSelector = strings.TrimSpace(modelSelector)
	if modelSelector == "" {
		return nil, config.ErrNoModelConfigured
	}
	if _, err := cfg.GetModelAlias(modelSelector); err == nil {
		return []string{modelSelector}, nil
	}
	index := findModelRouterIndex(cfg, modelSelector)
	if index < 0 || !cfg.ModelRouters[index].Enabled {
		return nil, fmt.Errorf("model alias %q is not configured", modelSelector)
	}
	aliases := reachableModelRouterAliases(&cfg.ModelRouters[index])
	if len(aliases) == 0 {
		return nil, fmt.Errorf("model router %q has no reachable terminal aliases", modelSelector)
	}
	return aliases, nil
}

func reachableModelRouterAliases(router *config.ModelRouterConfig) []string {
	if router == nil {
		return nil
	}
	blocks := make(map[string]config.ModelRouterBlock, len(router.Blocks))
	for _, block := range router.Blocks {
		blocks[strings.TrimSpace(block.ID)] = block
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
		seenBlocks[blockID] = true
		block, ok := blocks[blockID]
		if !ok {
			return
		}
		if strings.TrimSpace(block.Type) == config.ModelRouterBlockTypeModel {
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

func validateConcreteAccountAliasPair(
	cfg *config.Config,
	accountRef string,
	modelAlias string,
	requireChatProvider bool,
) error {
	model, err := cfg.ResolveModelAlias(modelAlias, accountRef)
	if err != nil {
		return err
	}
	if _, ok := config.AccountRouterCredentialAccountID(accountRef); ok {
		provider, supported := config.AccountRouterCredentialAccountProvider(accountRef)
		if !supported {
			return fmt.Errorf("credential account provider is unsupported")
		}
		provider = probeCredentialRuntimeProvider(provider)
		if _, err := providers.ResolveModelForProvider(provider, model); err != nil {
			return err
		}
		if requireChatProvider && providers.NormalizeProvider(provider) == "elevenlabs" {
			return fmt.Errorf("provider %q is not usable for chat", provider)
		}
		return nil
	}

	found := false
	for _, account := range cfg.ModelList {
		if account == nil ||
			strings.TrimSpace(account.ModelName) != strings.TrimSpace(accountRef) ||
			!account.Enabled ||
			account.IsAccountRouter() ||
			account.IsModelRouter() {
			continue
		}
		found = true
		provider, _ := providers.ExtractProtocol(account)
		provider = providers.NormalizeProvider(provider)
		if _, err := providers.ResolveModelForProvider(provider, model); err != nil {
			return err
		}
		if requireChatProvider && provider == "elevenlabs" {
			return fmt.Errorf("provider %q is not usable for chat", provider)
		}
	}
	if !found {
		return fmt.Errorf("account is not configured or enabled")
	}
	return nil
}

func validateSelectableAccountRef(cfg *config.Config, accountRef string) error {
	accountRef = strings.TrimSpace(accountRef)
	if accountRef == "" {
		return fmt.Errorf("account_ref is required")
	}
	if credentialAccountAvailable(accountRef) {
		return nil
	}
	if cfg != nil {
		for i := range cfg.AccountRouters {
			if strings.TrimSpace(cfg.AccountRouters[i].Name) != accountRef {
				continue
			}
			if !cfg.AccountRouters[i].Enabled {
				return fmt.Errorf("account router %q is disabled", accountRef)
			}
			return nil
		}
		for _, account := range cfg.ModelList {
			if account == nil || strings.TrimSpace(account.ModelName) != accountRef {
				continue
			}
			if account.IsAccountRouter() || account.IsModelRouter() {
				continue
			}
			if account.Enabled {
				return nil
			}
		}
	}
	return fmt.Errorf("account %q is not configured or enabled", accountRef)
}

func resolveConcreteAccountAliasConfig(
	cfg *config.Config,
	accountRef string,
	modelAlias string,
) (*config.ModelConfig, error) {
	account, err := resolveAccountModelConfig(cfg, accountRef)
	if err != nil {
		return nil, err
	}
	if account.IsAccountRouter() || account.IsModelRouter() {
		return nil, fmt.Errorf("account %q is not a concrete account", accountRef)
	}
	model, err := cfg.ResolveModelAlias(modelAlias, accountRef)
	if err != nil {
		return nil, err
	}
	provider, _ := providers.ExtractProtocol(account)
	model, err = providers.ResolveModelForProvider(provider, model)
	if err != nil {
		return nil, fmt.Errorf(
			"model alias %q for account %q: %w",
			modelAlias,
			accountRef,
			err,
		)
	}
	clone := *account
	clone.Provider = providers.NormalizeProvider(provider)
	clone.Model = model
	return &clone, nil
}

func modelAliasReferences(cfg *config.Config, alias string) []string {
	if cfg == nil || strings.TrimSpace(alias) == "" {
		return nil
	}
	seen := make(map[string]bool)
	out := make([]string, 0)
	add := func(reference string) {
		if reference == "" || seen[reference] {
			return
		}
		seen[reference] = true
		out = append(out, reference)
	}
	match := func(value string) bool {
		return strings.TrimSpace(value) == alias
	}
	contains := func(values []string) bool {
		for _, value := range values {
			if match(value) {
				return true
			}
		}
		return false
	}

	if match(cfg.Agents.Defaults.ModelName) {
		add("agents.defaults.model_name")
	}
	if contains(cfg.Agents.Defaults.ModelFallbacks) {
		add("agents.defaults.model_fallbacks")
	}
	if match(cfg.Agents.Defaults.ImageModel) {
		add("agents.defaults.image_model")
	}
	if contains(cfg.Agents.Defaults.ImageModelFallbacks) {
		add("agents.defaults.image_model_fallbacks")
	}
	if cfg.Agents.Defaults.Routing != nil && match(cfg.Agents.Defaults.Routing.LightModel) {
		add("agents.defaults.routing.light_model")
	}
	for i, account := range cfg.ModelList {
		if account != nil && match(account.SubscriptionEquivalentModel) {
			add(fmt.Sprintf("model_list[%d].subscription_equivalent_model", i))
		}
	}
	if match(cfg.Voice.ModelName) {
		add("voice.model_name")
	}
	if match(cfg.Voice.TTSModelName) {
		add("voice.tts_model_name")
	}
	if match(cfg.Tools.Web.Gemini.ModelAlias) {
		add("tools.web.gemini.model_alias")
	}
	if match(cfg.Tools.Web.Perplexity.ModelAlias) {
		add("tools.web.perplexity.model_alias")
	}
	for i := range cfg.Agents.List {
		agent := &cfg.Agents.List[i]
		if agent.Model != nil &&
			(match(agent.Model.Primary) || contains(agent.Model.Fallbacks)) {
			add(fmt.Sprintf("agents.list[%d].model", i))
		}
		if agent.Subagents != nil && agent.Subagents.Model != nil &&
			(match(agent.Subagents.Model.Primary) || contains(agent.Subagents.Model.Fallbacks)) {
			add(fmt.Sprintf("agents.list[%d].subagents.model", i))
		}
	}
	for i := range cfg.ModelRouters {
		for _, block := range cfg.ModelRouters[i].Blocks {
			if match(block.Model) {
				add(fmt.Sprintf("model_routers[%d]", i))
			}
		}
	}
	return out
}

func modelAccountReferences(cfg *config.Config, accountRef string) []string {
	if cfg == nil || strings.TrimSpace(accountRef) == "" {
		return nil
	}
	accountRef = strings.TrimSpace(accountRef)
	seen := make(map[string]bool)
	out := make([]string, 0)
	add := func(reference string) {
		if reference == "" || seen[reference] {
			return
		}
		seen[reference] = true
		out = append(out, reference)
	}
	match := func(value string) bool {
		return strings.TrimSpace(value) == accountRef
	}

	if match(cfg.Agents.Defaults.AccountRef) {
		add("agents.defaults.account_ref")
	}
	if match(cfg.Voice.AccountRef) {
		add("voice.account_ref")
	}
	if match(cfg.Voice.TTSAccountRef) {
		add("voice.tts_account_ref")
	}
	for i := range cfg.Agents.List {
		if match(cfg.Agents.List[i].AccountRef) {
			add(fmt.Sprintf("agents.list[%d].account_ref", i))
		}
	}
	for i := range cfg.ModelAliases {
		if _, ok := cfg.ModelAliases[i].AccountOverrides[accountRef]; ok {
			add(fmt.Sprintf("model_aliases[%d].account_overrides", i))
		}
	}
	for i := range cfg.AccountRouters {
		for j := range cfg.AccountRouters[i].Blocks {
			block := &cfg.AccountRouters[i].Blocks[j]
			if match(block.Account) {
				add(fmt.Sprintf("account_routers[%d].blocks[%d].account", i, j))
			}
			for _, candidate := range block.Accounts {
				if match(candidate) {
					add(fmt.Sprintf("account_routers[%d].blocks[%d].accounts", i, j))
					break
				}
			}
			if accountRouterConditionReferences(block.Condition, accountRef) {
				add(fmt.Sprintf("account_routers[%d].blocks[%d].condition", i, j))
			}
		}
	}
	return out
}

func accountRouterConditionReferences(
	condition *config.AccountRouterCondition,
	accountRef string,
) bool {
	if condition == nil {
		return false
	}
	return accountRouterExpressionReferences(&condition.Left, accountRef) ||
		accountRouterExpressionReferences(&condition.Right, accountRef)
}

func accountRouterExpressionReferences(
	expression *config.AccountRouterExpression,
	accountRef string,
) bool {
	if expression == nil {
		return false
	}
	if strings.TrimSpace(expression.Account) == strings.TrimSpace(accountRef) {
		return true
	}
	return accountRouterExpressionReferences(expression.Left, accountRef) ||
		accountRouterExpressionReferences(expression.Right, accountRef)
}
