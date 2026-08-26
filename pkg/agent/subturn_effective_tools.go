package agent

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/sipeed/picoclaw/pkg/accountrouter"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

const subTurnNativeSearchCapability = "web_search"

var subTurnAlwaysIneligibleTools = map[string]struct{}{
	"cron":         {},
	"exec":         {},
	"exec_command": {},
	"threads":      {},
	"workflow":     {},
	"write_stdin":  {},
}

var subTurnDepthBoundedTools = map[string]struct{}{
	"delegate": {},
	"spawn":    {},
	"subagent": {},
}

type subTurnCapabilitySnapshot struct {
	exact         string
	constructible bool
}

type effectiveSubTurnToolSelection struct {
	roots        []string
	profile      config.EffectiveTurnProfile
	nativeSearch bool
}

type subTurnToolSelectionOptions struct {
	implementationProviderSetProven bool
	parentAuthorityFrozen           bool
	parentAuthority                 subTurnNativeAuthoritySnapshot
}

func selectEffectiveSubTurnTools(
	cfg *config.Config,
	parent *turnState,
	implementationAgent *AgentInstance,
	explicit []tools.Tool,
	childDepth int,
	maxDepth int,
	selectionOptions ...subTurnToolSelectionOptions,
) (effectiveSubTurnToolSelection, error) {
	selection := effectiveSubTurnToolSelection{}
	if parent == nil || parent.agent == nil || parent.agent.Tools == nil {
		return selection, fmt.Errorf("%w: immediate parent tools are unavailable", ErrInvalidSubTurnConfig)
	}
	if implementationAgent == nil || implementationAgent.Tools == nil {
		return selection, fmt.Errorf("%w: child implementation tools are unavailable", ErrInvalidSubTurnConfig)
	}

	profile, err := canonicalTurnProfile(parent.profile)
	if err != nil {
		return selection, fmt.Errorf("%w: %v", ErrInvalidSubTurnConfig, err)
	}
	parentCapabilities := snapshotSubTurnCapabilities(parent.agent.Tools)
	implementationCapabilities := snapshotSubTurnCapabilities(implementationAgent.Tools)
	parentWebPhysical := subTurnPhysicalCapabilityContains(
		parentCapabilities,
		subTurnNativeSearchCapability,
	)
	parentWebKnown := parentWebPhysical || subTurnConfiguredNativeSearchKnown(cfg, parent.agent)
	parentWebViable := parentWebPhysical
	options := subTurnToolSelectionOptions{implementationProviderSetProven: true}
	if len(selectionOptions) > 0 {
		options = selectionOptions[0]
	}
	parentAuthority := parent.nativeSearchAuthoritySnapshot()
	if options.parentAuthorityFrozen {
		parentAuthority = options.parentAuthority
	}
	if parentAuthority.strict {
		parentWebKnown = parentWebKnown || parentAuthority.allowed
		parentWebViable = parentWebViable || parentAuthority.allowed
	} else if parentAuthority.observed {
		parentWebViable = parentWebViable || parentAuthority.allowed
	} else {
		parentWebViable = parentWebViable || subTurnRootNativeSearchAuthority(cfg, parent.agent)
	}

	physical := make(map[string]struct{})
	for name, candidate := range implementationCapabilities {
		parentCandidate, parentHas := parentCapabilities[name]
		parentAuthorized := parentHas && parentCandidate.constructible
		if foldSubTurnToolName(name) == subTurnNativeSearchCapability && parentWebViable {
			parentAuthorized = true
		}
		if !candidate.constructible || !parentAuthorized {
			continue
		}
		folded := foldSubTurnToolName(name)
		if _, excluded := subTurnAlwaysIneligibleTools[folded]; excluded {
			continue
		}
		if childDepth >= maxDepth {
			if _, bounded := subTurnDepthBoundedTools[folded]; bounded {
				continue
			}
		}
		if !turnProfileToolAllowed(profile, name) {
			continue
		}
		physical[name] = struct{}{}
	}

	implementationWebPhysical := subTurnPhysicalCapabilityContains(
		implementationCapabilities,
		subTurnNativeSearchCapability,
	)
	implementationWebKnown := implementationWebPhysical ||
		subTurnConfiguredNativeSearchKnown(cfg, implementationAgent)
	providerSetProven := options.implementationProviderSetProven
	implementationWebViable := implementationWebPhysical ||
		implementationWebKnown && providerSetProven &&
			subTurnAgentProvidersSupportNativeSearch(
				implementationAgent,
			)
	if implementationAgent == parent.agent && parentAuthority.strict &&
		parentAuthority.allowed {
		implementationWebKnown = true
		implementationWebViable = implementationWebPhysical ||
			providerSetProven && subTurnAgentProvidersSupportNativeSearch(implementationAgent)
	}
	nativeSearch := cfg != nil &&
		cfg.Tools.IsToolEnabled("web") &&
		cfg.Tools.Web.PreferNative &&
		parentWebViable &&
		implementationWebViable &&
		turnProfileToolAllowed(profile, subTurnNativeSearchCapability)

	explicitNames, explicitSet, err := resolveExplicitSubTurnToolNames(
		explicit,
		parentCapabilities,
		implementationCapabilities,
		parentWebKnown,
		implementationWebKnown,
	)
	if err != nil {
		return selection, err
	}
	if explicit != nil {
		for name := range physical {
			if _, selected := explicitSet[name]; !selected {
				delete(physical, name)
			}
		}
		if !subTurnPhysicalSetContains(explicitSet, subTurnNativeSearchCapability) {
			nativeSearch = false
		}
	}

	if !subTurnPhysicalSetContains(physical, "spawn") {
		deleteSubTurnPhysicalName(physical, "spawn_status")
	}

	if explicit != nil {
		for _, name := range explicitNames {
			if foldSubTurnToolName(name) == subTurnNativeSearchCapability && nativeSearch &&
				!subTurnPhysicalSetContains(physical, name) {
				continue
			}
			if _, available := physical[name]; !available {
				return selection, fmt.Errorf(
					"%w: explicit child tool %q is unavailable",
					ErrInvalidSubTurnConfig,
					name,
				)
			}
		}
	}

	selection.roots = make([]string, 0, len(physical))
	for name := range physical {
		selection.roots = append(selection.roots, name)
	}
	sort.Strings(selection.roots)
	effectiveNames := append([]string(nil), selection.roots...)
	if nativeSearch && !containsFoldedSubTurnToolName(effectiveNames, subTurnNativeSearchCapability) {
		effectiveNames = append(effectiveNames, subTurnNativeSearchCapability)
		sort.Strings(effectiveNames)
	}
	selection.profile = turnProfileWithExactTools(profile, effectiveNames)
	selection.nativeSearch = nativeSearch
	return selection, nil
}

func snapshotSubTurnCapabilities(
	registry *tools.ToolRegistry,
) map[string]subTurnCapabilitySnapshot {
	capabilities := registry.InstantiationCapabilities()
	result := make(map[string]subTurnCapabilitySnapshot, len(capabilities))
	for _, capability := range capabilities {
		result[capability.Name] = subTurnCapabilitySnapshot{
			exact:         capability.Name,
			constructible: capability.FactoryBacked || capability.ImmutableShared,
		}
	}
	return result
}

func resolveExplicitSubTurnToolNames(
	explicit []tools.Tool,
	parent map[string]subTurnCapabilitySnapshot,
	implementation map[string]subTurnCapabilitySnapshot,
	parentWebAuthority bool,
	implementationWebAuthority bool,
) ([]string, map[string]struct{}, error) {
	if explicit == nil {
		return nil, nil, nil
	}
	targetIndex, targetAmbiguous := foldedSubTurnCapabilityIndex(implementation)
	parentIndex, _ := foldedSubTurnCapabilityIndex(parent)
	if implementationWebAuthority {
		if _, physical := targetIndex[subTurnNativeSearchCapability]; physical {
			// Preserve the target registry's exact physical key.
		} else {
			targetIndex[subTurnNativeSearchCapability] = subTurnNativeSearchCapability
		}
	}
	if parentWebAuthority {
		if _, known := parentIndex[subTurnNativeSearchCapability]; !known {
			parentIndex[subTurnNativeSearchCapability] = subTurnNativeSearchCapability
		}
	}
	resolved := make([]string, 0, len(explicit))
	selected := make(map[string]struct{}, len(explicit))
	for index, selector := range explicit {
		name, err := safeSubTurnSelectorName(selector)
		if err != nil {
			return nil, nil, fmt.Errorf(
				"%w: explicit child tool selector %d %v",
				ErrInvalidSubTurnConfig,
				index,
				err,
			)
		}
		folded := foldSubTurnToolName(name)
		if _, ambiguous := targetAmbiguous[folded]; ambiguous {
			return nil, nil, fmt.Errorf(
				"%w: explicit child tool %q is ambiguous",
				ErrInvalidSubTurnConfig,
				name,
			)
		}
		exact, known := targetIndex[folded]
		if !known {
			if _, parentKnown := parentIndex[folded]; parentKnown {
				return nil, nil, fmt.Errorf(
					"%w: explicit child tool %q is unavailable",
					ErrInvalidSubTurnConfig,
					name,
				)
			}
			return nil, nil, fmt.Errorf(
				"%w: explicit child tool %q is unknown",
				ErrInvalidSubTurnConfig,
				name,
			)
		}
		if _, duplicate := selected[exact]; duplicate {
			continue
		}
		selected[exact] = struct{}{}
		resolved = append(resolved, exact)
	}
	sort.Strings(resolved)
	return resolved, selected, nil
}

func foldedSubTurnCapabilityIndex(
	capabilities map[string]subTurnCapabilitySnapshot,
) (map[string]string, map[string]struct{}) {
	index := make(map[string]string, len(capabilities))
	ambiguous := make(map[string]struct{})
	for exact := range capabilities {
		folded := foldSubTurnToolName(exact)
		if previous, exists := index[folded]; exists && previous != exact {
			delete(index, folded)
			ambiguous[folded] = struct{}{}
			continue
		}
		if _, conflict := ambiguous[folded]; !conflict {
			index[folded] = exact
		}
	}
	return index, ambiguous
}

func safeSubTurnSelectorName(selector tools.Tool) (name string, returnErr error) {
	if selector == nil || isNilSubTurnToolSelector(selector) {
		return "", fmt.Errorf("is nil")
	}
	defer func() {
		if recover() != nil {
			name = ""
			returnErr = fmt.Errorf("name panicked")
		}
	}()
	name = strings.TrimSpace(selector.Name())
	if name == "" {
		return "", fmt.Errorf("has a blank name")
	}
	return name, nil
}

func isNilSubTurnToolSelector(selector tools.Tool) bool {
	value := reflect.ValueOf(selector)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func turnProfileWithExactTools(
	profile config.EffectiveTurnProfile,
	names []string,
) config.EffectiveTurnProfile {
	if !profile.Enabled {
		profile = config.EffectiveTurnProfile{
			Enabled:          true,
			HistoryMode:      config.TurnProfileModeDefault,
			SystemPromptMode: config.TurnProfileModeDefault,
			SkillsMode:       config.TurnProfileModeDefault,
		}
	}
	profile.Enabled = true
	if len(names) == 0 {
		profile.ToolsMode = config.TurnProfileModeOff
		profile.AllowedTools = nil
		return profile
	}
	profile.ToolsMode = config.TurnProfileModeCustom
	profile.AllowedTools = append([]string(nil), names...)
	return profile
}

func subTurnRootNativeSearchAuthority(cfg *config.Config, agent *AgentInstance) bool {
	return subTurnConfiguredNativeSearchKnown(cfg, agent) &&
		subTurnAgentProvidersSupportNativeSearch(agent)
}

func subTurnConfiguredNativeSearchKnown(cfg *config.Config, agent *AgentInstance) bool {
	return cfg != nil && agent != nil && agent.Tools != nil &&
		cfg.Tools.IsToolEnabled("web") && cfg.Tools.Web.PreferNative &&
		agent.Tools.AllowsRegistration(subTurnNativeSearchCapability)
}

// subTurnUsesProvenImplementationProviderSet is deliberately conservative for
// pseudo-only native search. A named target uses its frozen generation policy.
// A same-agent child may use that same proof only when its override resolves to
// the exact configured primary/fallback set; arbitrary turn-local aliases and
// fallback lists require a physical web_search root.
func subTurnUsesProvenImplementationProviderSet(
	agent *AgentInstance,
	turnConfig SubTurnConfig,
) bool {
	if agent == nil {
		return false
	}
	if strings.TrimSpace(turnConfig.TargetAgentID) != "" {
		return true
	}
	if strings.TrimSpace(turnConfig.Model) != strings.TrimSpace(agent.Model) {
		return false
	}
	if turnConfig.ModelFallbacks == nil {
		return true
	}
	if len(turnConfig.ModelFallbacks) != len(agent.Fallbacks) {
		return false
	}
	for index := range turnConfig.ModelFallbacks {
		if strings.TrimSpace(turnConfig.ModelFallbacks[index]) !=
			strings.TrimSpace(agent.Fallbacks[index]) {
			return false
		}
	}
	return true
}

func subTurnAgentProvidersSupportNativeSearch(
	agent *AgentInstance,
) bool {
	if agent == nil || agent.ModelRouter != nil || !isNativeSearchProvider(agent.Provider) {
		return false
	}
	if agent.LightProvider != nil && !isNativeSearchProvider(agent.LightProvider) {
		return false
	}
	candidates := make([]providers.FallbackCandidate, 0,
		len(agent.Candidates)+len(agent.LightCandidates)+len(agent.ImageCandidates))
	candidates = append(candidates, agent.Candidates...)
	candidates = append(candidates, agent.LightCandidates...)
	candidates = append(candidates, agent.ImageCandidates...)
	for _, router := range []*accountrouter.Router{
		agent.AccountRouter,
		agent.LightAccountRouter,
		agent.ImageAccountRouter,
	} {
		if router == nil {
			continue
		}
		for _, account := range router.Accounts {
			candidates = append(candidates, account.Candidates...)
		}
	}
	for _, candidate := range candidates {
		provider := agent.candidateProviderForCandidate(candidate)
		if provider == nil || !isNativeSearchProvider(provider) {
			return false
		}
	}
	return true
}

func subTurnPhysicalCapabilityContains(
	capabilities map[string]subTurnCapabilitySnapshot,
	name string,
) bool {
	for exact, capability := range capabilities {
		if capability.constructible && foldSubTurnToolName(exact) == foldSubTurnToolName(name) {
			return true
		}
	}
	return false
}

func subTurnPhysicalSetContains(values map[string]struct{}, name string) bool {
	for exact := range values {
		if foldSubTurnToolName(exact) == foldSubTurnToolName(name) {
			return true
		}
	}
	return false
}

func subTurnRequiresPseudoOnlyNativeSearch(ts *turnState) bool {
	if ts == nil || ts.agent == nil || ts.agent.Tools == nil {
		return false
	}
	authority := ts.nativeSearchAuthoritySnapshot()
	return authority.strict && authority.allowed && !subTurnPhysicalCapabilityContains(
		snapshotSubTurnCapabilities(ts.agent.Tools),
		subTurnNativeSearchCapability,
	)
}

func subTurnCandidatesSupportNativeSearch(
	agent *AgentInstance,
	candidates []providers.FallbackCandidate,
) bool {
	if agent == nil || len(candidates) == 0 {
		return false
	}
	for _, candidate := range candidates {
		if !isNativeSearchProvider(agent.candidateProviderForCandidate(candidate)) {
			return false
		}
	}
	return true
}

func deleteSubTurnPhysicalName(values map[string]struct{}, name string) {
	for exact := range values {
		if foldSubTurnToolName(exact) == foldSubTurnToolName(name) {
			delete(values, exact)
		}
	}
}

func containsFoldedSubTurnToolName(values []string, name string) bool {
	folded := foldSubTurnToolName(name)
	for _, value := range values {
		if foldSubTurnToolName(value) == folded {
			return true
		}
	}
	return false
}

func foldSubTurnToolName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func subTurnNativeSearchForProvider(
	cfg *config.Config,
	ts *turnState,
	provider providers.LLMProvider,
) bool {
	if cfg == nil || ts == nil || provider == nil ||
		!cfg.Tools.IsToolEnabled("web") || !cfg.Tools.Web.PreferNative ||
		!turnProfileToolAllowed(ts.profile, subTurnNativeSearchCapability) {
		return false
	}
	if ts.toolAuthorityBound && !ts.nativeSearchAllowed {
		return false
	}
	return isNativeSearchProvider(provider)
}

func projectNativeSearchForProvider(
	cfg *config.Config,
	ts *turnState,
	provider providers.LLMProvider,
	nativeAllowed bool,
	definitions []providers.ToolDefinition,
	options map[string]any,
) ([]providers.ToolDefinition, bool) {
	native := nativeAllowed && subTurnNativeSearchForProvider(cfg, ts, provider)
	if options != nil {
		if native {
			options["native_search"] = true
		} else {
			delete(options, "native_search")
		}
	}
	if !native {
		return definitions, false
	}
	filtered := make([]providers.ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		if strings.EqualFold(
			strings.TrimSpace(definition.Function.Name),
			subTurnNativeSearchCapability,
		) {
			continue
		}
		filtered = append(filtered, definition)
	}
	return filtered, true
}

func filterSubTurnHookDefinitionsToPhysicalTools(
	ts *turnState,
	definitions []providers.ToolDefinition,
) []providers.ToolDefinition {
	if ts == nil || !ts.toolAuthorityBound || ts.agent == nil || ts.agent.Tools == nil {
		return definitions
	}
	physical := make(map[string]struct{})
	for _, definition := range ts.agent.Tools.ToProviderDefs() {
		physical[definition.Function.Name] = struct{}{}
	}
	filtered := make([]providers.ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		if _, allowed := physical[definition.Function.Name]; allowed {
			filtered = append(filtered, definition)
		}
	}
	return filtered
}

func toolDefinitionsContainFoldedName(
	definitions []providers.ToolDefinition,
	name string,
) bool {
	folded := foldSubTurnToolName(name)
	for _, definition := range definitions {
		if foldSubTurnToolName(definition.Function.Name) == folded {
			return true
		}
	}
	return false
}
