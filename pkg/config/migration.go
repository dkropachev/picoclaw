// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// buildModelWithProtocol constructs a model string with protocol prefix.
// If the model already contains a "/" (indicating it has a protocol prefix), it is returned as-is.
// Otherwise, the protocol prefix is added.
func buildModelWithProtocol(protocol, model string) string {
	if strings.Contains(model, "/") {
		// Model already has a protocol prefix, return as-is
		return model
	}
	return protocol + "/" + model
}

type legacyDiagnosticConfig struct {
	Version        int                    `json:"version"`
	Isolation      IsolationConfig        `json:"isolation,omitempty"`
	Agents         legacyDiagnosticAgents `json:"agents,omitempty"`
	Session        SessionConfig          `json:"session,omitempty"`
	Evolution      EvolutionConfig        `json:"evolution,omitempty"`
	Channels       map[string]any         `json:"channels,omitempty"`
	ChannelList    ChannelsConfig         `json:"channel_list,omitempty"`
	ModelList      []map[string]any       `json:"model_list,omitempty"`
	AccountRouters AccountRouterList      `json:"account_routers,omitempty"`
	ModelRouters   ModelRouterList        `json:"model_routers,omitempty"`
	Gateway        GatewayConfig          `json:"gateway,omitempty"`
	Events         EventsConfig           `json:"events,omitempty"`
	PRLifecycle    PRLifecycleConfig      `json:"pr_lifecycle,omitempty"`
	Workflows      WorkflowsConfig        `json:"workflows,omitempty"`
	GitWorkspaces  GitWorkspacesConfig    `json:"git_workspaces,omitempty"`
	Hooks          HooksConfig            `json:"hooks,omitempty"`
	Tools          ToolsConfig            `json:"tools,omitempty"`
	Heartbeat      HeartbeatConfig        `json:"heartbeat,omitempty"`
	Devices        DevicesConfig          `json:"devices,omitempty"`
	Voice          VoiceConfig            `json:"voice,omitempty"`
	BuildInfo      BuildInfo              `json:"build_info,omitempty"`
	Bindings       json.RawMessage        `json:"bindings,omitempty"`
	Providers      json.RawMessage        `json:"providers,omitempty"`
}

type legacyDiagnosticAgents struct {
	Defaults legacyDiagnosticAgentDefaults `json:"defaults,omitempty"`
	List     []AgentConfig                 `json:"list,omitempty"`
	Dispatch *DispatchConfig               `json:"dispatch,omitempty"`
}

type legacyDiagnosticAgentDefaults struct {
	AgentDefaults
	LegacyModel string `json:"model,omitempty"`
}

func validateLegacyConfigDiagnostics(data []byte) error {
	var raw map[string]any
	removedLegacyGeminiModel := false
	if err := json.Unmarshal(data, &raw); err == nil {
		if tools, ok := raw["tools"].(map[string]any); ok {
			if web, ok := tools["web"].(map[string]any); ok {
				if gemini, ok := web["gemini"].(map[string]any); ok {
					// Accepted only by the legacy diagnostics pass; the v3→v4
					// migration consumes this raw selector into model_alias.
					if _, exists := gemini["model"]; exists {
						delete(gemini, "model")
						removedLegacyGeminiModel = true
					}
				}
			}
		}
		if removedLegacyGeminiModel {
			if normalized, err := json.Marshal(raw); err == nil {
				data = normalized
			}
		}
	}
	var cfg legacyDiagnosticConfig
	return decodeJSONWithDiagnostics(data, &cfg, "config.json")
}

func migrateLegacyAgentDefaultsModel(m map[string]any) {
	agents, ok := m["agents"].(map[string]any)
	if !ok {
		return
	}
	defaults, ok := agents["defaults"].(map[string]any)
	if !ok {
		return
	}
	model, hasModel := defaults["model"]
	if !hasModel {
		return
	}
	if _, hasModelName := defaults["model_name"]; !hasModelName {
		defaults["model_name"] = model
	}
	delete(defaults, "model")
}

// loadConfigV1 loads a version 1 config (current schema)
func loadConfig(data []byte) (*Config, error) {
	cfg := DefaultConfig()
	evolutionModeExplicit := configObjectHasField(data, "evolution", "mode")
	evolutionExplicitWithoutMode := configObjectHasTopLevelField(data, "evolution") && !evolutionModeExplicit

	// Pre-scan the JSON to check how many model_list entries the user provided.
	// Go's JSON decoder reuses existing slice backing-array elements rather than
	// zero-initializing them, so fields absent from the user's JSON (e.g. api_base)
	// would silently inherit values from the DefaultConfig template at the same
	// index position. We only reset cfg.ModelList when the user actually provides
	// entries; the current DefaultConfig intentionally has no model entries.
	var tmp Config
	if err := decodeJSONWithDiagnostics(data, &tmp, "config.json"); err != nil {
		return nil, err
	}
	if len(tmp.ModelList) > 0 {
		cfg.ModelList = nil
	}

	if err := decodeJSONWithDiagnostics(data, cfg, "config.json"); err != nil {
		return nil, err
	}
	if evolutionExplicitWithoutMode {
		cfg.Evolution.Mode = ""
	}
	return cfg, nil
}

func configObjectHasTopLevelField(data []byte, field string) bool {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}
	_, ok := raw[field]
	return ok
}

func configObjectHasField(data []byte, objectField, nestedField string) bool {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}
	objectData, ok := raw[objectField]
	if !ok {
		return false
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(objectData, &object); err != nil {
		return false
	}
	_, ok = object[nestedField]
	return ok
}

func mergeAPIKeys(apiKey string, apiKeys []string) []string {
	seen := make(map[string]struct{})
	var all []string

	if k := strings.TrimSpace(apiKey); k != "" {
		if _, exists := seen[k]; !exists {
			seen[k] = struct{}{}
			all = append(all, k)
		}
	}

	for _, k := range apiKeys {
		if trimmed := strings.TrimSpace(k); trimmed != "" {
			if _, exists := seen[trimmed]; !exists {
				seen[trimmed] = struct{}{}
				all = append(all, trimmed)
			}
		}
	}

	return all
}

func compareInt(v any, expected int) bool {
	switch val := v.(type) {
	case int:
		return val == expected
	case float64:
		return val == float64(expected)
	case json.Number:
		parsed, err := val.Int64()
		return err == nil && parsed == int64(expected)
	case nil:
		return expected == 0
	default:
		return false
	}
}

// migrateV0ToV1 converts a V0 (legacy, no version field) config JSON to V1 format:
//  1. Migrates legacy providers to model_list
//  2. Migrates agents.defaults.model → agents.defaults.model_name
//  3. Sets version to 1
func migrateV0ToV1(m map[string]any) error {
	if !compareInt(m["version"], 0) {
		return fmt.Errorf("migrateV0ToV1: expected version 0, got %v", m["version"])
	}

	migrateLegacyAgentDefaultsModel(m)

	// Migrate legacy providers to model_list if no model_list exists
	if _, hasModelList := m["model_list"]; !hasModelList {
		if providers, hasProviders := m["providers"]; hasProviders {
			if provMap, ok := providers.(map[string]any); ok && !isProvidersMapEmpty(provMap) {
				// Extract user's provider and model from agents.defaults
				userProvider := ""
				userModel := ""
				if agents, ok := m["agents"].(map[string]any); ok {
					if defaults, ok := agents["defaults"].(map[string]any); ok {
						if v, ok := defaults["provider"].(string); ok {
							userProvider = v
						}
						// Check both model_name (new) and model (old) fields
						if v, ok := defaults["model_name"].(string); ok && v != "" {
							userModel = v
						} else if v, ok := defaults["model"].(string); ok && v != "" {
							userModel = v
						}
					}
				}

				modelListRaw := v0ProvidersMapToModelList(provMap, userProvider, userModel)
				if len(modelListRaw) > 0 {
					m["model_list"] = modelListRaw
				}
			}
		}
	}

	// Convert model_list api_key → api_keys
	if modelList, ok := m["model_list"].([]any); ok {
		for _, model := range modelList {
			if mVal, ok := model.(map[string]any); ok {
				if ss := toUniqueStrings(mVal["api_key"], mVal["api_keys"]); len(ss) > 0 {
					mVal["api_keys"] = ss
					delete(mVal, "api_key")
				}
			}
		}
	}

	m["version"] = 1

	return nil
}

func toUniqueStrings(s any, ss any) []string {
	set := make(map[string]struct{})

	// process s
	if str, ok := s.(string); ok && str != "" {
		set[str] = struct{}{}
	}

	// process ss as []any (JSON arrays)
	if slice, ok := ss.([]any); ok {
		for _, item := range slice {
			if str, ok := item.(string); ok && str != "" {
				set[str] = struct{}{}
			}
		}
	}

	// process ss as []string
	if slice, ok := ss.([]string); ok {
		for _, item := range slice {
			if item != "" {
				set[item] = struct{}{}
			}
		}
	}

	// map to slice
	result := make([]string, 0, len(set))
	for k := range set {
		result = append(result, k)
	}

	return result
}

// migrateV1ToV2 converts a V1 config JSON to V2 format:
//  1. Migrates legacy "mention_only" to "group_trigger.mention_only"
//  2. Infers "enabled" field for models
//  3. Sets version to 2
func migrateV1ToV2(m map[string]any) error {
	if !compareInt(m["version"], 1) {
		return fmt.Errorf("migrateV1ToV2: expected version 1, got %#v", m["version"])
	}

	// Migrate channels: move "mention_only" to "group_trigger.mention_only"
	if channels, ok := m["channels"]; ok {
		if chMap, ok := channels.(map[string]any); ok {
			for _, ch := range chMap {
				if chVal, ok := ch.(map[string]any); ok {
					if mentionOnly, hasMention := chVal["mention_only"]; hasMention {
						delete(chVal, "mention_only")
						if gt, hasGT := chVal["group_trigger"].(map[string]any); hasGT {
							gt["mention_only"] = mentionOnly
						} else {
							chVal["group_trigger"] = map[string]any{"mention_only": mentionOnly}
						}
					}
				}
			}
		}
	}

	// Infer "enabled" field for models matching configV1.migrateModelEnabled behavior
	if modelList, ok := m["model_list"].([]any); ok {
		// Convert api_key → api_keys for each model
		for _, model := range modelList {
			if mVal, ok := model.(map[string]any); ok {
				if ss := toUniqueStrings(mVal["api_key"], mVal["api_keys"]); len(ss) > 0 {
					mVal["api_keys"] = ss
					delete(mVal, "api_key")
				}
			}
		}

		// Infer enabled status
		for _, model := range modelList {
			if mVal, ok := model.(map[string]any); ok {
				// Skip if explicitly set
				if _, hasEnabled := mVal["enabled"]; hasEnabled {
					continue
				}
				// Models with API keys are considered enabled
				if apiKeys, hasAPIKeys := mVal["api_keys"]; hasAPIKeys {
					// Check for []any or []string
					hasKeys := false
					if keys, ok := apiKeys.([]any); ok {
						hasKeys = len(keys) > 0
					} else if keys, ok := apiKeys.([]string); ok {
						hasKeys = len(keys) > 0
					}
					if hasKeys {
						mVal["enabled"] = true
						continue
					}
				}
				// The reserved "local-model" entry is considered enabled
				if mVal["model_name"] == "local-model" {
					mVal["enabled"] = true
				}
				logger.Infof("model: %v", mVal)
			}
		}
	} else {
		logger.Warnf("model_list is not a slice: %#v", m["model_list"])
	}

	m["version"] = 2

	return nil
}

// migrateV2ToV3 converts a V2 config JSON to V3 format:
//  1. Renames "channels" key to "channel_list"
//  2. Converts flat-format channel entries to nested format (wrapping
//     channel-specific fields in "settings")
//  3. Sets version to 3
func migrateV2ToV3(m map[string]any) error {
	if !compareInt(m["version"], 2) {
		return fmt.Errorf("migrateV2ToV3: expected version 2, got %v", m["version"])
	}

	migrateLegacyAgentDefaultsModel(m)
	delete(m, "bindings")

	// Rename channels → channel_list
	if channels, ok := m["channels"]; ok {
		delete(m, "channels")

		// Convert each channel from flat to nested format
		if chMap, ok := channels.(map[string]any); ok {
			for k, ch := range chMap {
				if chVal, ok := ch.(map[string]any); ok {
					chVal["type"] = k
					// If already has "settings" key, leave as-is
					if _, hasSettings := chVal["settings"]; hasSettings {
						continue
					}

					// Migrate Onebot "group_trigger_prefix" → "group_trigger.prefixes"
					if gtp, hasGTP := chVal["group_trigger_prefix"]; hasGTP {
						if gt, hasGT := chVal["group_trigger"].(map[string]any); hasGT {
							if _, hasPrefixes := gt["prefixes"]; !hasPrefixes {
								gt["prefixes"] = gtp
							}
						} else {
							chVal["group_trigger"] = map[string]any{"prefixes": gtp}
						}
						delete(chVal, "group_trigger_prefix")
					}

					// Separate channel-specific fields into "settings"
					settings := make(map[string]any)
					for fieldKey, v := range chVal {
						if _, exists := BaseFieldNames[fieldKey]; !exists {
							settings[fieldKey] = v
							delete(chVal, fieldKey)
						}
					}
					if len(settings) > 0 {
						chVal["settings"] = settings
					}
				}
			}
		}

		m["channel_list"] = channels
	}

	m["version"] = 3

	return nil
}

type legacyV3ModelAccount struct {
	name     string
	provider string
	model    string
}

// migrateV3ToV4 introduces model aliases and separates the account selector
// from the model alias selected by agents.
//
// Only model_list names that map unambiguously to one concrete model are
// promoted to aliases. The migration never invents a provider default. An old
// agent selector is retained as a model alias only when such an alias was
// generated. A selector is moved to account_ref only when it names a known
// concrete account, enabled account router, or supported credential account.
// Unknown legacy values are cleared so the migrated first-run configuration
// remains loadable without fabricating either half of the selection.
func migrateV3ToV4(m map[string]any) error {
	if !compareInt(m["version"], 3) {
		return fmt.Errorf("migrateV3ToV4: expected version 3, got %v", m["version"])
	}

	type aliasCandidate struct {
		name      string
		model     string
		ambiguous bool
	}
	candidates := make([]aliasCandidate, 0)
	candidateIndex := make(map[string]int)
	accounts := make([]legacyV3ModelAccount, 0)
	configuredAccounts := make(map[string]struct{})
	if modelList, ok := m["model_list"].([]any); ok {
		for _, item := range modelList {
			model, ok := item.(map[string]any)
			if !ok || migrationModelIsRouter(model) {
				continue
			}
			name, _ := model["model_name"].(string)
			concreteModel, _ := model["model"].(string)
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if enabled, _ := model["enabled"].(bool); enabled {
				configuredAccounts[name] = struct{}{}
			}
			if strings.TrimSpace(concreteModel) == "" {
				continue
			}
			provider, _ := model["provider"].(string)
			accounts = append(accounts, legacyV3ModelAccount{
				name:     name,
				provider: strings.TrimSpace(provider),
				model:    strings.TrimSpace(concreteModel),
			})
			if index, exists := candidateIndex[name]; exists {
				if candidates[index].model != concreteModel {
					candidates[index].ambiguous = true
				}
				continue
			}
			candidateIndex[name] = len(candidates)
			candidates = append(candidates, aliasCandidate{name: name, model: concreteModel})
		}
	}
	if routers, ok := m["account_routers"].([]any); ok {
		for _, item := range routers {
			router, ok := item.(map[string]any)
			if !ok {
				continue
			}
			name, _ := router["name"].(string)
			enabled, _ := router["enabled"].(bool)
			if name = strings.TrimSpace(name); name != "" && enabled {
				configuredAccounts[name] = struct{}{}
			}
		}
	}

	generatedAliases := make(map[string]string, len(candidates))
	aliasAccounts := make(map[string]string, len(candidates))
	aliases := make([]any, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.ambiguous {
			continue
		}
		generatedAliases[candidate.name] = candidate.model
		aliasAccounts[candidate.name] = candidate.name
		aliases = append(aliases, map[string]any{
			"name":  candidate.name,
			"model": candidate.model,
		})
	}

	// Older resolution accepted a raw concrete model ID (for example
	// "gpt-4o") even when the account entry was named "openai". Preserve that
	// explicit user selection as an alias only when it resolves to one
	// unambiguous concrete account. Never derive a provider default.
	for _, ref := range legacyV3ModelReferences(m) {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if _, exists := generatedAliases[ref]; exists {
			continue
		}
		account, concreteModel, ok := resolveLegacyV3ConcreteSelection(accounts, ref)
		if !ok {
			continue
		}
		generatedAliases[ref] = concreteModel
		aliasAccounts[ref] = account
		aliases = append(aliases, map[string]any{
			"name":  ref,
			"model": concreteModel,
		})
	}

	ensureWebSearchAlias := func(rawModel, preferredName string) string {
		rawModel = strings.TrimSpace(rawModel)
		if rawModel == "" {
			return ""
		}
		for _, item := range aliases {
			alias, ok := item.(map[string]any)
			if !ok {
				continue
			}
			model, _ := alias["model"].(string)
			if strings.TrimSpace(model) == rawModel {
				name, _ := alias["name"].(string)
				return name
			}
		}
		aliasName := preferredName
		for suffix := 2; ; suffix++ {
			if _, exists := generatedAliases[aliasName]; !exists {
				break
			}
			aliasName = fmt.Sprintf("%s-%d", preferredName, suffix)
		}
		generatedAliases[aliasName] = rawModel
		aliases = append(aliases, map[string]any{
			"name":  aliasName,
			"model": rawModel,
		})
		return aliasName
	}

	// Model-backed web search providers now reference aliases. Preserve the
	// concrete model that a v3 configuration actually used, but never install
	// one for a newly-created v4 configuration.
	if tools, ok := m["tools"].(map[string]any); ok {
		if web, ok := tools["web"].(map[string]any); ok {
			if gemini, ok := web["gemini"].(map[string]any); ok {
				rawModel, _ := gemini["model"].(string)
				delete(gemini, "model")
				if aliasName := ensureWebSearchAlias(rawModel, "web-search-gemini"); aliasName != "" {
					gemini["model_alias"] = aliasName
				}
			}
			if perplexity, ok := web["perplexity"].(map[string]any); ok {
				enabled, _ := perplexity["enabled"].(bool)
				if enabled {
					// v3 hardcoded this concrete model in the request path.
					perplexity["model_alias"] = ensureWebSearchAlias(
						"perplexity/sonar",
						"web-search-perplexity",
					)
				}
			}
		}
	}
	m["model_aliases"] = aliases

	migrateV3AgentModelSelections(m, generatedAliases, aliasAccounts, configuredAccounts)
	migrateV3VoiceModelSelections(m, generatedAliases, aliasAccounts, configuredAccounts)
	m["version"] = 4
	return nil
}

// migrateV4ToV5 removes aliases that v3 migration mechanically derived from
// account names or concrete model IDs. Those values expose provider details
// and do not describe a stable task role. Explicit custom aliases survive.
// Legacy web-search aliases remain explicit custom aliases; predefined roles
// must stay unconfigured until the user maps them deliberately. Removed
// references are cleared instead of guessing a replacement model.
func migrateV4ToV5(m map[string]any) error {
	if !compareInt(m["version"], 4) {
		return fmt.Errorf("migrateV4ToV5: expected version 4, got %v", m["version"])
	}

	accounts := make([]legacyV3ModelAccount, 0)
	if modelList, ok := m["model_list"].([]any); ok {
		for _, item := range modelList {
			model, ok := item.(map[string]any)
			if !ok || migrationModelIsRouter(model) {
				continue
			}
			name, _ := model["model_name"].(string)
			concreteModel, _ := model["model"].(string)
			provider, _ := model["provider"].(string)
			if strings.TrimSpace(name) == "" || strings.TrimSpace(concreteModel) == "" {
				continue
			}
			accounts = append(accounts, legacyV3ModelAccount{
				name:     strings.TrimSpace(name),
				provider: strings.TrimSpace(provider),
				model:    strings.TrimSpace(concreteModel),
			})
		}
	}

	aliases, _ := m["model_aliases"].([]any)
	rewrites := make(map[string]string)
	retained := make([]any, 0, len(aliases))
	for _, item := range aliases {
		alias, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := alias["name"].(string)
		model, _ := alias["model"].(string)
		name = strings.TrimSpace(name)
		model = strings.TrimSpace(model)
		if name == "" {
			continue
		}

		generated := false
		if !IsDeveloperModelAlias(name) {
			for _, account := range accounts {
				if account.model == model && legacyV3AccountMatchesModelRef(account, name) {
					generated = true
					break
				}
			}
		}
		if generated {
			rewrites[name] = ""
			continue
		}
		retained = append(retained, alias)
	}
	m["model_aliases"] = retained
	migrationRewriteModelAliasReferences(m, rewrites)
	m["version"] = 5
	return nil
}

// migrateV5ToV6 introduces trusted review-attention policy persistence. The
// shape is additive, so migration only advances the version and deliberately
// preserves any already-present policy maps, including empty policies.
func migrateV5ToV6(m map[string]any) error {
	if !compareInt(m["version"], 5) {
		return fmt.Errorf("migrateV5ToV6: expected version 5, got %v", m["version"])
	}
	m["version"] = 6
	return nil
}

func migrationRewriteModelAliasReferences(m map[string]any, rewrites map[string]string) {
	if len(rewrites) == 0 {
		return
	}
	rewriteField := func(container map[string]any, field string) {
		value, ok := container[field].(string)
		if !ok {
			return
		}
		if replacement, found := rewrites[strings.TrimSpace(value)]; found {
			container[field] = replacement
		}
	}
	rewriteList := func(container map[string]any, field string) {
		values, ok := container[field].([]any)
		if !ok {
			return
		}
		out := make([]any, 0, len(values))
		for _, value := range values {
			name, ok := value.(string)
			if !ok {
				continue
			}
			if replacement, found := rewrites[strings.TrimSpace(name)]; found {
				if replacement != "" {
					out = append(out, replacement)
				}
				continue
			}
			out = append(out, name)
		}
		container[field] = out
	}
	rewritePolicy := func(container map[string]any, field string) {
		switch policy := container[field].(type) {
		case string:
			rewriteField(container, field)
		case map[string]any:
			rewriteField(policy, "primary")
			rewriteList(policy, "fallbacks")
		}
	}

	if agents, ok := m["agents"].(map[string]any); ok {
		if defaults, ok := agents["defaults"].(map[string]any); ok {
			rewriteField(defaults, "model_name")
			rewriteList(defaults, "model_fallbacks")
			rewriteField(defaults, "image_model")
			rewriteList(defaults, "image_model_fallbacks")
			if routing, ok := defaults["routing"].(map[string]any); ok {
				rewriteField(routing, "light_model")
			}
		}
		if list, ok := agents["list"].([]any); ok {
			for _, value := range list {
				agent, ok := value.(map[string]any)
				if !ok {
					continue
				}
				rewritePolicy(agent, "model")
				if subagents, ok := agent["subagents"].(map[string]any); ok {
					rewritePolicy(subagents, "model")
				}
			}
		}
	}
	if voice, ok := m["voice"].(map[string]any); ok {
		rewriteField(voice, "model_name")
		rewriteField(voice, "tts_model_name")
	}
	if tools, ok := m["tools"].(map[string]any); ok {
		if web, ok := tools["web"].(map[string]any); ok {
			if gemini, ok := web["gemini"].(map[string]any); ok {
				rewriteField(gemini, "model_alias")
			}
			if perplexity, ok := web["perplexity"].(map[string]any); ok {
				rewriteField(perplexity, "model_alias")
			}
		}
	}
	if modelList, ok := m["model_list"].([]any); ok {
		for _, value := range modelList {
			if model, ok := value.(map[string]any); ok {
				rewriteField(model, "subscription_equivalent_model")
			}
		}
	}
	if routers, ok := m["model_routers"].([]any); ok {
		retainedRouters := make([]any, 0, len(routers))
		removedRouters := make(map[string]string)
		for _, value := range routers {
			router, ok := value.(map[string]any)
			if !ok {
				continue
			}
			valid := true
			if blocks, ok := router["blocks"].([]any); ok {
				for _, blockValue := range blocks {
					block, ok := blockValue.(map[string]any)
					if !ok {
						continue
					}
					rewriteField(block, "model")
					if blockType, _ := block["type"].(string); blockType == ModelRouterBlockTypeModel {
						model, _ := block["model"].(string)
						valid = valid && strings.TrimSpace(model) != ""
					}
				}
			}
			if valid {
				retainedRouters = append(retainedRouters, router)
			} else if name, _ := router["name"].(string); strings.TrimSpace(name) != "" {
				removedRouters[strings.TrimSpace(name)] = ""
			}
		}
		m["model_routers"] = retainedRouters
		migrationRewriteModelAliasReferences(m, removedRouters)
	}
}

func legacyV3ModelReferences(m map[string]any) []string {
	refs := make([]string, 0)
	addString := func(value any) {
		if value, ok := value.(string); ok && strings.TrimSpace(value) != "" {
			refs = append(refs, value)
		}
	}
	addList := func(value any) {
		switch values := value.(type) {
		case []any:
			for _, value := range values {
				addString(value)
			}
		case []string:
			for _, value := range values {
				addString(value)
			}
		}
	}
	addModelPolicy := func(value any) {
		switch model := value.(type) {
		case string:
			addString(model)
		case map[string]any:
			addString(model["primary"])
			addList(model["fallbacks"])
		}
	}

	if agents, ok := m["agents"].(map[string]any); ok {
		if defaults, ok := agents["defaults"].(map[string]any); ok {
			addString(defaults["model_name"])
			addList(defaults["model_fallbacks"])
			addString(defaults["image_model"])
			addList(defaults["image_model_fallbacks"])
			if routing, ok := defaults["routing"].(map[string]any); ok {
				addString(routing["light_model"])
			}
		}
		if list, ok := agents["list"].([]any); ok {
			for _, value := range list {
				agent, ok := value.(map[string]any)
				if !ok {
					continue
				}
				addModelPolicy(agent["model"])
				if subagents, ok := agent["subagents"].(map[string]any); ok {
					addModelPolicy(subagents["model"])
				}
			}
		}
	}
	if voice, ok := m["voice"].(map[string]any); ok {
		addString(voice["model_name"])
		addString(voice["tts_model_name"])
	}
	if routers, ok := m["model_routers"].([]any); ok {
		for _, value := range routers {
			router, ok := value.(map[string]any)
			if !ok {
				continue
			}
			blocks, _ := router["blocks"].([]any)
			for _, blockValue := range blocks {
				block, ok := blockValue.(map[string]any)
				if ok {
					addString(block["model"])
				}
			}
		}
	}
	return refs
}

func resolveLegacyV3ConcreteSelection(
	accounts []legacyV3ModelAccount,
	ref string,
) (string, string, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", false
	}

	type match struct {
		account string
		model   string
	}
	matches := make(map[string]match)
	for _, account := range accounts {
		if !legacyV3AccountMatchesModelRef(account, ref) {
			continue
		}
		key := account.name + "\x00" + account.model
		matches[key] = match{account: account.name, model: account.model}
	}
	if len(matches) != 1 {
		return "", "", false
	}
	for _, result := range matches {
		return result.account, result.model, true
	}
	return "", "", false
}

func legacyV3AccountMatchesModelRef(account legacyV3ModelAccount, ref string) bool {
	if account.name == ref || account.model == ref {
		return true
	}
	provider := strings.TrimSpace(account.provider)
	if provider != "" {
		if provider+"/"+account.model == ref {
			return true
		}
		if strings.TrimPrefix(account.model, provider+"/") == ref &&
			strings.HasPrefix(account.model, provider+"/") {
			return true
		}
		return false
	}
	_, modelID, found := strings.Cut(account.model, "/")
	return found && strings.TrimSpace(modelID) == ref
}

func migrationModelIsRouter(model map[string]any) bool {
	provider, _ := model["provider"].(string)
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case AccountRouterProvider, ModelRouterProvider:
		return true
	}
	return model["router"] != nil || model["model_router"] != nil
}

func migrateV3AgentModelSelections(
	m map[string]any,
	aliases map[string]string,
	aliasAccounts map[string]string,
	configuredAccounts map[string]struct{},
) {
	agents, ok := m["agents"].(map[string]any)
	if !ok {
		return
	}
	if defaults, defaultsOK := agents["defaults"].(map[string]any); defaultsOK {
		migrateV3DefaultModelSelections(
			defaults,
			aliases,
			aliasAccounts,
			configuredAccounts,
		)
	}
	agentList, ok := agents["list"].([]any)
	if !ok {
		return
	}
	for _, item := range agentList {
		agent, ok := item.(map[string]any)
		if !ok {
			continue
		}
		migrateV3AgentModelSelection(
			agent,
			aliases,
			aliasAccounts,
			configuredAccounts,
		)
		if subagents, ok := agent["subagents"].(map[string]any); ok {
			migrateV3NestedModelPolicy(subagents, aliases)
		}
	}
}

func migrateV3VoiceModelSelections(
	m map[string]any,
	aliases map[string]string,
	aliasAccounts map[string]string,
	configuredAccounts map[string]struct{},
) {
	voice, ok := m["voice"].(map[string]any)
	if !ok {
		return
	}
	migrate := func(modelField, accountField string) {
		selector, _ := voice[modelField].(string)
		selector = strings.TrimSpace(selector)
		if selector == "" {
			return
		}
		voice[accountField] = migrationAccountRef(
			selector,
			aliasAccounts,
			configuredAccounts,
		)
		if _, found := aliases[selector]; !found {
			voice[modelField] = ""
		}
	}
	migrate("model_name", "account_ref")
	migrate("tts_model_name", "tts_account_ref")
}

func migrateV3DefaultModelSelections(
	defaults map[string]any,
	aliases map[string]string,
	aliasAccounts map[string]string,
	configuredAccounts map[string]struct{},
) {
	if selector, ok := defaults["model_name"].(string); ok &&
		strings.TrimSpace(selector) != "" {
		defaults["account_ref"] = migrationAccountRef(
			selector,
			aliasAccounts,
			configuredAccounts,
		)
		if _, found := aliases[selector]; !found {
			defaults["model_name"] = ""
		}
	}
	migrationFilterAliasListField(defaults, "model_fallbacks", aliases)
	if imageModel, ok := defaults["image_model"].(string); ok &&
		strings.TrimSpace(imageModel) != "" {
		if _, found := aliases[imageModel]; !found {
			defaults["image_model"] = ""
		}
	}
	migrationFilterAliasListField(defaults, "image_model_fallbacks", aliases)
	if routing, ok := defaults["routing"].(map[string]any); ok {
		if lightModel, ok := routing["light_model"].(string); ok &&
			strings.TrimSpace(lightModel) != "" {
			if _, found := aliases[lightModel]; !found {
				routing["light_model"] = ""
			}
		}
	}
}

func migrateV3AgentModelSelection(
	agent map[string]any,
	aliases map[string]string,
	aliasAccounts map[string]string,
	configuredAccounts map[string]struct{},
) {
	switch model := agent["model"].(type) {
	case string:
		if strings.TrimSpace(model) == "" {
			return
		}
		agent["account_ref"] = migrationAccountRef(
			model,
			aliasAccounts,
			configuredAccounts,
		)
		if _, found := aliases[model]; !found {
			agent["model"] = ""
		}
	case map[string]any:
		primary, _ := model["primary"].(string)
		if strings.TrimSpace(primary) != "" {
			agent["account_ref"] = migrationAccountRef(
				primary,
				aliasAccounts,
				configuredAccounts,
			)
			if _, found := aliases[primary]; !found {
				model["primary"] = ""
			}
		}
		migrationFilterAliasListField(model, "fallbacks", aliases)
	}
}

func migrationAccountRef(
	selector string,
	aliasAccounts map[string]string,
	configuredAccounts map[string]struct{},
) string {
	selector = strings.TrimSpace(selector)
	if resolvedAccount := aliasAccounts[selector]; resolvedAccount != "" {
		if _, enabled := configuredAccounts[resolvedAccount]; enabled {
			return resolvedAccount
		}
		return ""
	}
	if _, ok := configuredAccounts[selector]; ok {
		return selector
	}
	if _, ok := AccountRouterCredentialAccountProvider(selector); ok {
		return selector
	}
	return ""
}

func migrateV3NestedModelPolicy(container map[string]any, aliases map[string]string) {
	switch model := container["model"].(type) {
	case string:
		if _, found := aliases[model]; !found {
			container["model"] = ""
		}
	case map[string]any:
		if primary, ok := model["primary"].(string); ok &&
			strings.TrimSpace(primary) != "" {
			if _, found := aliases[primary]; !found {
				model["primary"] = ""
			}
		}
		migrationFilterAliasListField(model, "fallbacks", aliases)
	}
}

func migrationFilterAliasListField(
	container map[string]any,
	field string,
	aliases map[string]string,
) {
	value, exists := container[field]
	if !exists || value == nil {
		return
	}
	container[field] = migrationFilterAliasList(value, aliases)
}

func migrationFilterAliasList(value any, aliases map[string]string) []any {
	filtered := make([]any, 0)
	switch values := value.(type) {
	case []any:
		for _, item := range values {
			name, ok := item.(string)
			if !ok {
				continue
			}
			if _, found := aliases[name]; found {
				filtered = append(filtered, name)
			}
		}
	case []string:
		for _, name := range values {
			if _, found := aliases[name]; found {
				filtered = append(filtered, name)
			}
		}
	}
	return filtered
}

func loadConfigMap(path string) (map[string]any, error) {
	var m1, m2 map[string]any
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return m1, nil
		}
		return nil, fmt.Errorf("failed to read config: %w", err)
	}
	if err = json.Unmarshal(data, &m1); err != nil {
		return nil, wrapJSONError(data, err, "config.json")
	}
	secPath := securityPath(path)
	data, err = os.ReadFile(secPath)
	if err != nil {
		if os.IsNotExist(err) {
			return m1, nil
		}
		return nil, fmt.Errorf("failed to read security config: %w", err)
	}
	if err = yaml.Unmarshal(data, &m2); err != nil {
		return nil, fmt.Errorf("failed to parse security config: %w", err)
	}
	if m2["web"] != nil || m2["skills"] != nil {
		m3 := make(map[string]any)
		if m2["web"] != nil {
			m3["web"] = m2["web"]
			delete(m2, "web")
		}
		if m2["skills"] != nil {
			m3["skills"] = m2["skills"]
			delete(m2, "skills")
			if m, ok := m3["skills"].(map[string]any); ok {
				if m["clawhub"] != nil {
					m["registries"] = map[string]any{"clawhub": m["clawhub"]}
					delete(m, "clawhub")
				}
				if gh, ok := m["github"].(map[string]any); ok {
					registries, _ := m["registries"].(map[string]any)
					if registries == nil {
						registries = map[string]any{}
					}
					githubRegistry := map[string]any{}
					for k, v := range gh {
						githubRegistry[k] = v
					}
					if token, ok := githubRegistry["token"]; ok {
						githubRegistry["auth_token"] = token
					}
					registries["github"] = githubRegistry
					m["registries"] = registries
				}
			}
		}
		m2["tools"] = m3
	}

	// Handle model_list merging specially: m1 has array format, m2 has map format
	if mainML, hasMainML := m1["model_list"]; hasMainML {
		if secML, hasSecML := m2["model_list"]; hasSecML {
			if secMap, ok := secML.(map[string]any); ok {
				// JSON unmarshals arrays as []any, convert to []map[string]any
				var mainArr []any
				if rawArr, ok := mainML.([]any); ok {
					mainArr = make([]any, 0, len(rawArr))
					for _, item := range rawArr {
						if mVal, ok := item.(map[string]any); ok {
							mainArr = append(mainArr, mVal)
						}
					}
				}
				if len(mainArr) > 0 {
					// Merge array-style with map-style in-place
					err = mergeModelListsWithMap(mainArr, secMap)
					if err != nil {
						logger.Errorf("mergeModelListsWithMap error: %v", err)
						return nil, err
					}
					m1["model_list"] = mainArr
				}
			}
		}
	}
	// Remove model_list from m2 so mergeMap doesn't override the array with map
	delete(m2, "model_list")

	m := mergeMap(m1, m2)
	return m, nil
}

// mergeModelListsWithMap merges array-style model_list with map-style security model_list.
// It generates indexed keys from model_name (like toNameIndex) and uses them
// to look up security entries, falling back to ModelName if the indexed key doesn't exist.
func mergeModelListsWithMap(mainML []any, secML map[string]any) error {
	// Build indexed keys like toNameIndex does
	indexedKeys := make(map[string]int)
	countMap := make(map[string]int)
	for i, m := range mainML {
		if mVal, ok := m.(map[string]any); ok {
			if name, hasName := mVal["model_name"]; hasName {
				nameStr, ok := name.(string)
				if !ok {
					return fmt.Errorf("model_name must be a string, got %T", name)
				}
				index := countMap[nameStr]
				indexedKeys[fmt.Sprintf("%s:%d", nameStr, index)] = i
				if _, ok := indexedKeys[nameStr]; !ok {
					indexedKeys[nameStr] = i
				}
				countMap[nameStr]++
			} else {
				return fmt.Errorf("model_name is required: %#v", mVal)
			}
		}
	}

	for k, v := range secML {
		if i, ok := indexedKeys[k]; ok {
			if vv, ok := v.(map[string]any); ok {
				if mVal, ok := mainML[i].(map[string]any); ok {
					mVal["api_keys"] = vv["api_keys"]
				}
			}
		} else {
			logger.Warnf("model_name not found in main config: %s", k)
		}
		delete(secML, k)
	}

	return nil
}
