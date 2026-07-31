package config

import (
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

type subscriptionEquivalentModelEdge struct {
	to   string
	path string
}

type subscriptionAliasModel struct {
	provider string
	model    string
}

// validateSubscriptionEquivalentModelGraph rejects recursive price-inheritance
// chains. An edge exists when an alias resolves to the concrete model described
// by a model_list entry that names another alias as its subscription equivalent.
func (c *Config) validateSubscriptionEquivalentModelGraph() error {
	if c == nil || len(c.ModelAliases) == 0 {
		return nil
	}

	aliasModels := make(map[string][]subscriptionAliasModel, len(c.ModelAliases))
	aliasesByName := make(map[string]*ModelAliasConfig, len(c.ModelAliases))
	aliasOrder := make([]string, 0, len(c.ModelAliases))
	for i := range c.ModelAliases {
		alias := &c.ModelAliases[i]
		aliasOrder = append(aliasOrder, alias.Name)
		aliasesByName[alias.Name] = alias
		aliasModels[alias.Name] = subscriptionAliasModels(alias)
	}

	edges := make(map[string][]subscriptionEquivalentModelEdge, len(c.ModelAliases))
	for i, account := range c.ModelList {
		if account == nil {
			continue
		}
		target := strings.TrimSpace(account.SubscriptionEquivalentModel)
		if target == "" {
			continue
		}
		provider, model := subscriptionMetadataModel(account)
		path := fmt.Sprintf("model_list[%d].subscription_equivalent_model", i)
		for _, aliasName := range aliasOrder {
			alias := aliasesByName[aliasName]
			matchesMetadata := model != "" &&
				subscriptionAliasModelsContain(aliasModels[aliasName], provider, model)
			accountModel, accountCompatible := subscriptionAliasModelForAccount(
				alias,
				account.ModelName,
				provider,
			)
			matchesAccountFallback := account.Enabled &&
				!account.IsAccountRouter() &&
				!account.IsModelRouter() &&
				accountCompatible &&
				!subscriptionMetadataModelExists(c.ModelList, provider, accountModel)
			if !matchesMetadata && !matchesAccountFallback {
				continue
			}
			edges[aliasName] = append(edges[aliasName], subscriptionEquivalentModelEdge{
				to:   target,
				path: path,
			})
		}
	}

	const (
		subscriptionAliasUnvisited = iota
		subscriptionAliasVisiting
		subscriptionAliasVisited
	)
	state := make(map[string]int, len(c.ModelAliases))
	stack := make([]string, 0, len(c.ModelAliases))
	var visit func(string) error
	visit = func(alias string) error {
		state[alias] = subscriptionAliasVisiting
		stack = append(stack, alias)
		for _, edge := range edges[alias] {
			switch state[edge.to] {
			case subscriptionAliasVisiting:
				start := 0
				for start < len(stack) && stack[start] != edge.to {
					start++
				}
				cycle := append(append([]string(nil), stack[start:]...), edge.to)
				return fmt.Errorf(
					"%s %q creates a subscription equivalent model cycle: %s",
					edge.path,
					edge.to,
					strings.Join(cycle, " -> "),
				)
			case subscriptionAliasVisited:
				continue
			}
			if err := visit(edge.to); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		state[alias] = subscriptionAliasVisited
		return nil
	}

	for _, alias := range aliasOrder {
		if state[alias] != subscriptionAliasUnvisited {
			continue
		}
		if err := visit(alias); err != nil {
			return err
		}
	}
	return nil
}

func subscriptionAliasModels(alias *ModelAliasConfig) []subscriptionAliasModel {
	if alias == nil {
		return nil
	}
	models := make([]subscriptionAliasModel, 0, len(alias.AccountOverrides)+1)
	add := func(configuredModel string) {
		configuredModel = strings.TrimSpace(configuredModel)
		if configuredModel == "" {
			return
		}
		provider, model := protocoltypes.SplitKnownProviderModel(configuredModel)
		if provider == "" {
			model = configuredModel
		}
		for _, existing := range models {
			if existing.provider == provider && existing.model == model {
				return
			}
		}
		models = append(models, subscriptionAliasModel{
			provider: provider,
			model:    model,
		})
	}
	add(alias.Model)
	for _, model := range alias.AccountOverrides {
		add(model)
	}
	return models
}

func subscriptionMetadataModel(account *ModelConfig) (provider, model string) {
	if account == nil {
		return "", ""
	}
	provider = protocoltypes.NormalizeProvider(account.Provider)
	model = strings.TrimSpace(account.Model)
	if provider != "" {
		return provider, model
	}
	inferredProvider, inferredModel := protocoltypes.SplitKnownProviderModel(model)
	if inferredProvider != "" {
		return inferredProvider, inferredModel
	}
	return "openai", model
}

func subscriptionAliasModelsContain(
	models []subscriptionAliasModel,
	provider string,
	model string,
) bool {
	for _, candidate := range models {
		if candidate.model != model {
			continue
		}
		if candidate.provider == "" || candidate.provider == provider {
			return true
		}
	}
	return false
}

func subscriptionAliasModelForAccount(
	alias *ModelAliasConfig,
	accountRef string,
	accountProvider string,
) (string, bool) {
	if alias == nil || accountProvider == "" {
		return "", false
	}
	configuredModel := alias.Model
	if override, ok := alias.AccountOverrides[accountRef]; ok {
		configuredModel = override
	}
	model, err := protocoltypes.ResolveModelForProvider(accountProvider, configuredModel)
	if err != nil {
		return "", false
	}
	return model, true
}

func subscriptionMetadataModelExists(
	accounts SecureModelList,
	provider string,
	model string,
) bool {
	for _, account := range accounts {
		candidateProvider, candidateModel := subscriptionMetadataModel(account)
		if candidateProvider == provider && candidateModel == model {
			return true
		}
	}
	return false
}
