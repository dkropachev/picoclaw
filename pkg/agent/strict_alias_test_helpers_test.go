package agent

import (
	"strings"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/isolation"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// newTestAgentLoopWithStrictModels upgrades legacy unit-test fixtures to the
// v4 account+alias contract. Production paths still use NewAgentLoop directly;
// tests that exercise missing-model failures do the same.
func newTestAgentLoopWithStrictModels(
	cfg *config.Config,
	msgBus *bus.MessageBus,
	provider providers.LLMProvider,
	opts ...AgentLoopOption,
) *AgentLoop {
	hadExplicitAccount := cfg != nil &&
		strings.TrimSpace(cfg.Agents.Defaults.AccountRef) != ""
	ensureStrictTestModelSelection(cfg, provider)
	loop := NewAgentLoop(cfg, msgBus, provider, opts...)
	if !hadExplicitAccount {
		bindLegacyTestProviderToAliases(loop, cfg, provider)
	}
	return loop
}

func newTestAgentLoopWithStrictModelsAndExecutionPolicy(
	cfg *config.Config,
	msgBus *bus.MessageBus,
	provider providers.LLMProvider,
	policy isolation.ExecutionPolicy,
	opts ...AgentLoopOption,
) *AgentLoop {
	hadExplicitAccount := cfg != nil &&
		strings.TrimSpace(cfg.Agents.Defaults.AccountRef) != ""
	ensureStrictTestModelSelection(cfg, provider)
	loop := NewAgentLoopWithExecutionPolicy(
		cfg,
		msgBus,
		provider,
		policy,
		opts...,
	)
	if !hadExplicitAccount {
		bindLegacyTestProviderToAliases(loop, cfg, provider)
	}
	return loop
}

func bindLegacyTestProviderToAliases(
	loop *AgentLoop,
	cfg *config.Config,
	provider providers.LLMProvider,
) {
	if loop == nil || loop.registry == nil || cfg == nil || provider == nil {
		return
	}
	for _, agentID := range loop.registry.ListAgentIDs() {
		agent, ok := loop.registry.GetAgent(agentID)
		if !ok || agent == nil ||
			lookupAccountRouterConfig(cfg, agent.AccountRef) != nil {
			continue
		}
		for _, alias := range cfg.ModelAliases {
			modelCfg, err := concreteAccountModelConfig(
				cfg,
				agent.AccountRef,
				alias.Name,
				agent.Workspace,
			)
			if err != nil {
				continue
			}
			candidate, ok := candidateFromModelConfig("", modelCfg)
			if !ok {
				continue
			}
			candidate.DisplayName = alias.Name
			candidate.IdentityKey = accountAliasIdentityKey(
				agent.AccountRef,
				alias.Name,
			)
			bindBootstrapProvider(
				agent.CandidateProviders,
				candidate,
				provider,
			)
		}
	}
}

func ensureStrictTestModelSelection(
	cfg *config.Config,
	_ providers.LLMProvider,
) {
	if cfg == nil {
		return
	}
	for _, modelCfg := range cfg.ModelList {
		if modelCfg == nil || modelCfg.IsAccountRouter() || modelCfg.IsModelRouter() {
			continue
		}
		ensureTestAccountRunnable(modelCfg)
		ensureTestAlias(
			cfg,
			strings.TrimSpace(modelCfg.ModelName),
			strings.TrimSpace(modelCfg.ModelName),
		)
	}

	defaults := &cfg.Agents.Defaults
	modelName := strings.TrimSpace(defaults.ModelName)
	if modelName == "" {
		modelName = "test-model"
		defaults.ModelName = modelName
	}
	for _, legacySubturnAlias := range []string{"gpt-4o-mini", "slow-model"} {
		if _, err := cfg.GetModelAlias(legacySubturnAlias); err != nil {
			cfg.ModelAliases = append(
				cfg.ModelAliases,
				config.ModelAliasConfig{
					Name:  legacySubturnAlias,
					Model: legacySubturnAlias,
				},
			)
		}
	}

	if lookupTestModelRouter(cfg, modelName) != nil {
		if strings.TrimSpace(defaults.AccountRef) == "" {
			defaults.AccountRef = ensureSyntheticTestAccount(cfg, "test-model")
		}
		ensureTestModelRouterAliases(cfg)
		return
	}

	if router := lookupAccountRouterConfig(cfg, modelName); router != nil &&
		strings.TrimSpace(defaults.AccountRef) == "" {
		defaults.AccountRef = modelName
		modelName = "test-model"
		defaults.ModelName = modelName
		if _, err := cfg.GetModelAlias(modelName); err != nil {
			cfg.ModelAliases = append(cfg.ModelAliases, config.ModelAliasConfig{
				Name:  modelName,
				Model: modelName,
			})
		}
		return
	}

	accountRef := strings.TrimSpace(defaults.AccountRef)
	if accountRef == "" {
		accountRef = testAccountForLegacySelection(cfg, modelName)
		if accountRef == "" {
			accountRef = ensureSyntheticTestAccount(cfg, modelName)
		}
		defaults.AccountRef = accountRef
	}
	ensureTestAlias(cfg, accountRef, modelName)
	for _, fallback := range defaults.ModelFallbacks {
		ensureTestAlias(cfg, accountRef, fallback)
	}
	if defaults.ImageModel != "" {
		ensureTestAlias(cfg, accountRef, defaults.ImageModel)
	}
	for _, fallback := range defaults.ImageModelFallbacks {
		ensureTestAlias(cfg, accountRef, fallback)
	}
	if defaults.Routing != nil && defaults.Routing.LightModel != "" {
		ensureTestAlias(cfg, accountRef, defaults.Routing.LightModel)
	}
	for i := range cfg.Agents.List {
		agentCfg := &cfg.Agents.List[i]
		if agentCfg.AccountRef == "" {
			agentCfg.AccountRef = accountRef
		}
		if agentCfg.Model != nil {
			ensureTestAlias(cfg, agentCfg.AccountRef, agentCfg.Model.Primary)
			for _, fallback := range agentCfg.Model.Fallbacks {
				ensureTestAlias(cfg, agentCfg.AccountRef, fallback)
			}
		}
	}
}

func lookupTestModelRouter(
	cfg *config.Config,
	name string,
) *config.ModelRouterConfig {
	for i := range cfg.ModelRouters {
		if strings.TrimSpace(cfg.ModelRouters[i].Name) == strings.TrimSpace(name) {
			return &cfg.ModelRouters[i]
		}
	}
	return nil
}

func ensureTestModelRouterAliases(cfg *config.Config) {
	for i := range cfg.ModelRouters {
		for _, block := range cfg.ModelRouters[i].Blocks {
			if strings.TrimSpace(block.Type) == config.ModelRouterBlockTypeModel {
				ensureTestAlias(
					cfg,
					cfg.Agents.Defaults.AccountRef,
					block.Model,
				)
			}
		}
	}
}

func testAccountForLegacySelection(cfg *config.Config, selection string) string {
	selection = strings.TrimSpace(selection)
	for _, modelCfg := range cfg.ModelList {
		if modelCfg == nil || modelCfg.IsAccountRouter() || modelCfg.IsModelRouter() {
			continue
		}
		if strings.TrimSpace(modelCfg.ModelName) == selection ||
			strings.TrimSpace(modelCfg.Model) == selection {
			ensureTestAccountRunnable(modelCfg)
			return strings.TrimSpace(modelCfg.ModelName)
		}
	}
	return ""
}

func ensureSyntheticTestAccount(cfg *config.Config, model string) string {
	const accountRef = "__test_account__"
	for _, modelCfg := range cfg.ModelList {
		if modelCfg != nil && modelCfg.ModelName == accountRef {
			return accountRef
		}
	}
	provider, _ := providers.SplitModelProviderAndID(model, "openai")
	cfg.ModelList = append(cfg.ModelList, &config.ModelConfig{
		ModelName: accountRef,
		Provider:  provider,
		APIBase:   "http://example.invalid/v1",
		APIKeys:   config.SimpleSecureStrings("test-key"),
		Enabled:   true,
	})
	return accountRef
}

func ensureTestAccountRunnable(modelCfg *config.ModelConfig) {
	if modelCfg == nil {
		return
	}
	modelCfg.Enabled = true
	if modelCfg.APIBase == "" && modelCfg.APIKey() == "" {
		modelCfg.APIKeys = config.SimpleSecureStrings("test-key")
	}
}

func ensureTestAlias(cfg *config.Config, accountRef, name string) {
	name = strings.TrimSpace(name)
	if name == "" || lookupTestModelRouter(cfg, name) != nil {
		return
	}
	if _, err := cfg.GetModelAlias(name); err == nil {
		return
	}
	concrete := name
	if accountCfg, err := cfg.GetModelConfig(accountRef); err == nil &&
		accountCfg != nil &&
		strings.TrimSpace(accountCfg.Model) != "" &&
		strings.TrimSpace(accountCfg.ModelName) == name {
		concrete = strings.TrimSpace(accountCfg.Model)
	}
	for _, modelCfg := range cfg.ModelList {
		if modelCfg == nil || modelCfg.IsAccountRouter() || modelCfg.IsModelRouter() {
			continue
		}
		if strings.TrimSpace(modelCfg.ModelName) == name &&
			strings.TrimSpace(modelCfg.Model) != "" {
			concrete = strings.TrimSpace(modelCfg.Model)
			break
		}
	}
	cfg.ModelAliases = append(cfg.ModelAliases, config.ModelAliasConfig{
		Name:  name,
		Model: concrete,
	})
}

func createStrictTestProvider(
	cfg *config.Config,
) (providers.LLMProvider, string, error) {
	ensureStrictTestModelSelection(cfg, nil)
	return providers.CreateProvider(cfg)
}

func strictTestCandidate(
	cfg *config.Config,
	agent *AgentInstance,
	provider providers.LLMProvider,
	alias string,
	model string,
) providers.FallbackCandidate {
	if configured, err := cfg.GetModelAlias(alias); err == nil {
		configured.Model = model
	} else {
		cfg.ModelAliases = append(cfg.ModelAliases, config.ModelAliasConfig{
			Name:  alias,
			Model: model,
		})
	}
	accountCfg, _ := cfg.GetModelConfig(agent.AccountRef)
	accountProvider, _ := providers.ExtractProtocol(accountCfg)
	candidate := providers.FallbackCandidate{
		Provider:    accountProvider,
		Model:       model,
		DisplayName: alias,
		IdentityKey: accountAliasIdentityKey(agent.AccountRef, alias),
	}
	bindBootstrapProvider(agent.CandidateProviders, candidate, provider)
	return candidate
}
