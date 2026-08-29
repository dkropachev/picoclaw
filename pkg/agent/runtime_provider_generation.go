package agent

import (
	"reflect"
	"sort"

	"github.com/sipeed/picoclaw/pkg/providers"
)

// agentRegistryProviderGeneration is a detached immutable snapshot of every
// provider binding owned by one published AgentRegistry. Candidate maps may
// contain aliases for the same provider; orderedProviders de-duplicates them
// in stable agent/key order and includes bootstrap/default/light providers.
type agentRegistryProviderGeneration struct {
	bootstrap        providers.LLMProvider
	defaultProvider  providers.LLMProvider
	agentBindings    map[string]map[string]providers.LLMProvider
	agentDirect      map[string]agentDirectProviderBindings
	orderedProviders []providers.LLMProvider
}

type agentDirectProviderBindings struct {
	primary providers.LLMProvider
	light   providers.LLMProvider
}

func snapshotAgentRegistryProviderGeneration(
	registry *AgentRegistry,
) *agentRegistryProviderGeneration {
	if registry == nil {
		return nil
	}
	registry.mu.RLock()
	agentIDs := make([]string, 0, len(registry.agents))
	agents := make(map[string]*AgentInstance, len(registry.agents))
	for agentID, instance := range registry.agents {
		agentIDs = append(agentIDs, agentID)
		agents[agentID] = instance
	}
	bootstrap := registry.bootstrapProvider
	defaultAgentID := registry.defaultAgentIDLocked()
	registry.mu.RUnlock()
	sort.Strings(agentIDs)
	if defaultAgentID == "" && len(agentIDs) > 0 {
		defaultAgentID = agentIDs[0]
	}

	generation := &agentRegistryProviderGeneration{
		bootstrap:     bootstrap,
		agentBindings: make(map[string]map[string]providers.LLMProvider, len(agentIDs)),
		agentDirect:   make(map[string]agentDirectProviderBindings, len(agentIDs)),
	}
	appendProvider := func(provider providers.LLMProvider) {
		if provider == nil {
			return
		}
		for _, existing := range generation.orderedProviders {
			if sameLLMProvider(existing, provider) {
				return
			}
		}
		generation.orderedProviders = append(generation.orderedProviders, provider)
	}

	agentCandidateProvidersMu.RLock()
	for _, agentID := range agentIDs {
		instance := agents[agentID]
		if instance == nil {
			continue
		}
		keys := make([]string, 0, len(instance.CandidateProviders))
		for key := range instance.CandidateProviders {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		bindings := make(map[string]providers.LLMProvider, len(keys))
		for _, key := range keys {
			provider := instance.CandidateProviders[key]
			bindings[key] = provider
			appendProvider(provider)
		}
		generation.agentBindings[agentID] = bindings
		generation.agentDirect[agentID] = agentDirectProviderBindings{
			primary: instance.Provider,
			light:   instance.LightProvider,
		}
		if agentID == defaultAgentID {
			generation.defaultProvider = instance.Provider
		}
		appendProvider(instance.Provider)
		appendProvider(instance.LightProvider)
	}
	agentCandidateProvidersMu.RUnlock()
	appendProvider(bootstrap)
	return generation
}

func (generation *agentRegistryProviderGeneration) directForAgent(
	agentID string,
) agentDirectProviderBindings {
	if generation == nil {
		return agentDirectProviderBindings{}
	}
	return generation.agentDirect[agentID]
}

func (generation *agentRegistryProviderGeneration) bindingsForAgent(
	agentID string,
) map[string]providers.LLMProvider {
	if generation == nil {
		return nil
	}
	source := generation.agentBindings[agentID]
	if len(source) == 0 {
		return nil
	}
	bindings := make(map[string]providers.LLMProvider, len(source))
	for key, provider := range source {
		bindings[key] = provider
	}
	return bindings
}

func (generation *agentRegistryProviderGeneration) constructorProvider() providers.LLMProvider {
	if generation == nil {
		return nil
	}
	if generation.bootstrap != nil {
		return generation.bootstrap
	}
	if generation.defaultProvider != nil {
		return generation.defaultProvider
	}
	if len(generation.orderedProviders) == 0 {
		return nil
	}
	return generation.orderedProviders[0]
}

func (generation *agentRegistryProviderGeneration) legacyRetainedProvider() providers.LLMProvider {
	if generation == nil {
		return nil
	}
	if generation.defaultProvider != nil {
		return generation.defaultProvider
	}
	return generation.constructorProvider()
}

func (generation *agentRegistryProviderGeneration) providerSet() []providers.LLMProvider {
	if generation == nil {
		return nil
	}
	return append([]providers.LLMProvider(nil), generation.orderedProviders...)
}

func sameLLMProvider(first, second providers.LLMProvider) bool {
	if first == nil || second == nil {
		return first == nil && second == nil
	}
	firstType := reflect.TypeOf(first)
	if firstType != reflect.TypeOf(second) || !firstType.Comparable() {
		return false
	}
	return reflect.ValueOf(first).Interface() == reflect.ValueOf(second).Interface()
}

func (generation *agentRegistryProviderGeneration) closeAll() {
	if generation == nil {
		return
	}
	for _, provider := range generation.orderedProviders {
		closeProviderIfStateful(provider)
	}
}

func (generation *agentRegistryProviderGeneration) closeAllExcept(
	retained providers.LLMProvider,
) {
	if generation == nil {
		return
	}
	for _, provider := range generation.orderedProviders {
		if sameLLMProvider(provider, retained) {
			continue
		}
		closeProviderIfStateful(provider)
	}
}
