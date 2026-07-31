// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package config

import "strings"

// isProvidersMapEmpty checks if a providers map has any non-empty provider configurations.
func isProvidersMapEmpty(providers map[string]any) bool {
	for _, prov := range providers {
		if provMap, ok := prov.(map[string]any); ok {
			if apiKey, ok := provMap["api_key"]; ok && apiKey != "" {
				return false
			}
			if apiBase, ok := provMap["api_base"]; ok && apiBase != "" {
				return false
			}
			if connectMode, ok := provMap["connect_mode"]; ok && connectMode != "" {
				return false
			}
			if authMethod, ok := provMap["auth_method"]; ok && authMethod != "" {
				return false
			}
		}
	}
	return true
}

// v0ProvidersMapToModelList converts a V0 providers map to a model_list slice.
func v0ProvidersMapToModelList(providers map[string]any, userProvider, userModel string) []any {
	// providerMigration defines migration rules for a provider
	type providerMigration struct {
		jsonKeys  []string
		protocol  string
		extractFn func(prov map[string]any) map[string]any
	}

	migrations := []providerMigration{
		{
			jsonKeys: []string{"openai", "gpt"},
			protocol: "openai",
			extractFn: func(prov map[string]any) map[string]any {
				entry := make(map[string]any)
				if v, ok := prov["api_key"]; ok && v != "" {
					entry["api_key"] = v
				}
				if v, ok := prov["api_base"]; ok && v != "" {
					entry["api_base"] = v
				}
				if v, ok := prov["proxy"]; ok && v != "" {
					entry["proxy"] = v
				}
				if v, ok := prov["request_timeout"]; ok && v != nil {
					entry["request_timeout"] = v
				}
				if v, ok := prov["auth_method"]; ok && v != "" {
					entry["auth_method"] = v
				}
				if v, ok := prov["web_search"]; ok && v != false {
					entry["web_search"] = v
				}
				return entry
			},
		},
		{
			jsonKeys: []string{"anthropic", "claude"},
			protocol: "anthropic",
			extractFn: func(prov map[string]any) map[string]any {
				entry := make(map[string]any)
				if v, ok := prov["api_key"]; ok && v != "" {
					entry["api_key"] = v
				}
				if v, ok := prov["api_base"]; ok && v != "" {
					entry["api_base"] = v
				}
				if v, ok := prov["proxy"]; ok && v != "" {
					entry["proxy"] = v
				}
				if v, ok := prov["request_timeout"]; ok && v != nil {
					entry["request_timeout"] = v
				}
				if v, ok := prov["auth_method"]; ok && v != "" {
					entry["auth_method"] = v
				}
				return entry
			},
		},
		{
			jsonKeys: []string{"litellm"},
			protocol: "litellm",
			extractFn: func(prov map[string]any) map[string]any {
				entry := make(map[string]any)
				if v, ok := prov["api_key"]; ok && v != "" {
					entry["api_key"] = v
				}
				if v, ok := prov["api_base"]; ok && v != "" {
					entry["api_base"] = v
				}
				if v, ok := prov["proxy"]; ok && v != "" {
					entry["proxy"] = v
				}
				if v, ok := prov["request_timeout"]; ok && v != nil {
					entry["request_timeout"] = v
				}
				return entry
			},
		},
		{
			jsonKeys: []string{"openrouter"},
			protocol: "openrouter",
			extractFn: func(prov map[string]any) map[string]any {
				entry := make(map[string]any)
				if v, ok := prov["api_key"]; ok && v != "" {
					entry["api_key"] = v
				}
				if v, ok := prov["api_base"]; ok && v != "" {
					entry["api_base"] = v
				}
				if v, ok := prov["proxy"]; ok && v != "" {
					entry["proxy"] = v
				}
				if v, ok := prov["request_timeout"]; ok && v != nil {
					entry["request_timeout"] = v
				}
				return entry
			},
		},
		{
			jsonKeys: []string{"groq"},
			protocol: "groq",
			extractFn: func(prov map[string]any) map[string]any {
				entry := make(map[string]any)
				if v, ok := prov["api_key"]; ok && v != "" {
					entry["api_key"] = v
				}
				if v, ok := prov["api_base"]; ok && v != "" {
					entry["api_base"] = v
				}
				if v, ok := prov["proxy"]; ok && v != "" {
					entry["proxy"] = v
				}
				if v, ok := prov["request_timeout"]; ok && v != nil {
					entry["request_timeout"] = v
				}
				return entry
			},
		},
		{
			jsonKeys: []string{"zhipu", "glm"},
			protocol: "zhipu",
			extractFn: func(prov map[string]any) map[string]any {
				entry := make(map[string]any)
				if v, ok := prov["api_key"]; ok && v != "" {
					entry["api_key"] = v
				}
				if v, ok := prov["api_base"]; ok && v != "" {
					entry["api_base"] = v
				}
				if v, ok := prov["proxy"]; ok && v != "" {
					entry["proxy"] = v
				}
				if v, ok := prov["request_timeout"]; ok && v != nil {
					entry["request_timeout"] = v
				}
				return entry
			},
		},
		{
			jsonKeys: []string{"vllm"},
			protocol: "vllm",
			extractFn: func(prov map[string]any) map[string]any {
				entry := make(map[string]any)
				if v, ok := prov["api_key"]; ok && v != "" {
					entry["api_key"] = v
				}
				if v, ok := prov["api_base"]; ok && v != "" {
					entry["api_base"] = v
				}
				if v, ok := prov["proxy"]; ok && v != "" {
					entry["proxy"] = v
				}
				if v, ok := prov["request_timeout"]; ok && v != nil {
					entry["request_timeout"] = v
				}
				return entry
			},
		},
		{
			jsonKeys: []string{"gemini", "google"},
			protocol: "gemini",
			extractFn: func(prov map[string]any) map[string]any {
				entry := make(map[string]any)
				if v, ok := prov["api_key"]; ok && v != "" {
					entry["api_key"] = v
				}
				if v, ok := prov["api_base"]; ok && v != "" {
					entry["api_base"] = v
				}
				if v, ok := prov["proxy"]; ok && v != "" {
					entry["proxy"] = v
				}
				if v, ok := prov["request_timeout"]; ok && v != nil {
					entry["request_timeout"] = v
				}
				return entry
			},
		},
		{
			jsonKeys: []string{"nvidia"},
			protocol: "nvidia",
			extractFn: func(prov map[string]any) map[string]any {
				entry := make(map[string]any)
				if v, ok := prov["api_key"]; ok && v != "" {
					entry["api_key"] = v
				}
				if v, ok := prov["api_base"]; ok && v != "" {
					entry["api_base"] = v
				}
				if v, ok := prov["proxy"]; ok && v != "" {
					entry["proxy"] = v
				}
				if v, ok := prov["request_timeout"]; ok && v != nil {
					entry["request_timeout"] = v
				}
				return entry
			},
		},
		{
			jsonKeys: []string{"ollama"},
			protocol: "ollama",
			extractFn: func(prov map[string]any) map[string]any {
				entry := make(map[string]any)
				if v, ok := prov["api_key"]; ok && v != "" {
					entry["api_key"] = v
				}
				if v, ok := prov["api_base"]; ok && v != "" {
					entry["api_base"] = v
				}
				if v, ok := prov["proxy"]; ok && v != "" {
					entry["proxy"] = v
				}
				if v, ok := prov["request_timeout"]; ok && v != nil {
					entry["request_timeout"] = v
				}
				return entry
			},
		},
		{
			jsonKeys: []string{"moonshot", "kimi"},
			protocol: "moonshot",
			extractFn: func(prov map[string]any) map[string]any {
				entry := make(map[string]any)
				if v, ok := prov["api_key"]; ok && v != "" {
					entry["api_key"] = v
				}
				if v, ok := prov["api_base"]; ok && v != "" {
					entry["api_base"] = v
				}
				if v, ok := prov["proxy"]; ok && v != "" {
					entry["proxy"] = v
				}
				if v, ok := prov["request_timeout"]; ok && v != nil {
					entry["request_timeout"] = v
				}
				return entry
			},
		},
		{
			jsonKeys: []string{"shengsuanyun"},
			protocol: "shengsuanyun",
			extractFn: func(prov map[string]any) map[string]any {
				entry := make(map[string]any)
				if v, ok := prov["api_key"]; ok && v != "" {
					entry["api_key"] = v
				}
				if v, ok := prov["api_base"]; ok && v != "" {
					entry["api_base"] = v
				}
				if v, ok := prov["proxy"]; ok && v != "" {
					entry["proxy"] = v
				}
				if v, ok := prov["request_timeout"]; ok && v != nil {
					entry["request_timeout"] = v
				}
				return entry
			},
		},
		{
			jsonKeys: []string{"deepseek"},
			protocol: "deepseek",
			extractFn: func(prov map[string]any) map[string]any {
				entry := make(map[string]any)
				if v, ok := prov["api_key"]; ok && v != "" {
					entry["api_key"] = v
				}
				if v, ok := prov["api_base"]; ok && v != "" {
					entry["api_base"] = v
				}
				if v, ok := prov["proxy"]; ok && v != "" {
					entry["proxy"] = v
				}
				if v, ok := prov["request_timeout"]; ok && v != nil {
					entry["request_timeout"] = v
				}
				return entry
			},
		},
		{
			jsonKeys: []string{"cerebras"},
			protocol: "cerebras",
			extractFn: func(prov map[string]any) map[string]any {
				entry := make(map[string]any)
				if v, ok := prov["api_key"]; ok && v != "" {
					entry["api_key"] = v
				}
				if v, ok := prov["api_base"]; ok && v != "" {
					entry["api_base"] = v
				}
				if v, ok := prov["proxy"]; ok && v != "" {
					entry["proxy"] = v
				}
				if v, ok := prov["request_timeout"]; ok && v != nil {
					entry["request_timeout"] = v
				}
				return entry
			},
		},
		{
			jsonKeys: []string{"vivgrid"},
			protocol: "vivgrid",
			extractFn: func(prov map[string]any) map[string]any {
				entry := make(map[string]any)
				if v, ok := prov["api_key"]; ok && v != "" {
					entry["api_key"] = v
				}
				if v, ok := prov["api_base"]; ok && v != "" {
					entry["api_base"] = v
				}
				if v, ok := prov["proxy"]; ok && v != "" {
					entry["proxy"] = v
				}
				if v, ok := prov["request_timeout"]; ok && v != nil {
					entry["request_timeout"] = v
				}
				return entry
			},
		},
		{
			jsonKeys: []string{"volcengine", "doubao"},
			protocol: "volcengine",
			extractFn: func(prov map[string]any) map[string]any {
				entry := make(map[string]any)
				if v, ok := prov["api_key"]; ok && v != "" {
					entry["api_key"] = v
				}
				if v, ok := prov["api_base"]; ok && v != "" {
					entry["api_base"] = v
				}
				if v, ok := prov["proxy"]; ok && v != "" {
					entry["proxy"] = v
				}
				if v, ok := prov["request_timeout"]; ok && v != nil {
					entry["request_timeout"] = v
				}
				return entry
			},
		},
		{
			jsonKeys: []string{"github_copilot", "copilot"},
			protocol: "github-copilot",
			extractFn: func(prov map[string]any) map[string]any {
				entry := make(map[string]any)
				if v, ok := prov["api_key"]; ok && v != "" {
					entry["api_key"] = v
				}
				if v, ok := prov["api_base"]; ok && v != "" {
					entry["api_base"] = v
				}
				if v, ok := prov["connect_mode"]; ok && v != "" {
					entry["connect_mode"] = v
				}
				return entry
			},
		},
		{
			jsonKeys: []string{"antigravity"},
			protocol: "antigravity",
			extractFn: func(prov map[string]any) map[string]any {
				entry := make(map[string]any)
				if v, ok := prov["api_key"]; ok && v != "" {
					entry["api_key"] = v
				}
				if v, ok := prov["auth_method"]; ok && v != "" {
					entry["auth_method"] = v
				}
				return entry
			},
		},
		{
			jsonKeys: []string{"qwen", "tongyi"},
			protocol: "qwen",
			extractFn: func(prov map[string]any) map[string]any {
				entry := make(map[string]any)
				if v, ok := prov["api_key"]; ok && v != "" {
					entry["api_key"] = v
				}
				if v, ok := prov["api_base"]; ok && v != "" {
					entry["api_base"] = v
				}
				if v, ok := prov["proxy"]; ok && v != "" {
					entry["proxy"] = v
				}
				if v, ok := prov["request_timeout"]; ok && v != nil {
					entry["request_timeout"] = v
				}
				return entry
			},
		},
		{
			jsonKeys: []string{"mistral"},
			protocol: "mistral",
			extractFn: func(prov map[string]any) map[string]any {
				entry := make(map[string]any)
				if v, ok := prov["api_key"]; ok && v != "" {
					entry["api_key"] = v
				}
				if v, ok := prov["api_base"]; ok && v != "" {
					entry["api_base"] = v
				}
				if v, ok := prov["proxy"]; ok && v != "" {
					entry["proxy"] = v
				}
				if v, ok := prov["request_timeout"]; ok && v != nil {
					entry["request_timeout"] = v
				}
				return entry
			},
		},
		{
			jsonKeys: []string{"avian"},
			protocol: "avian",
			extractFn: func(prov map[string]any) map[string]any {
				entry := make(map[string]any)
				if v, ok := prov["api_key"]; ok && v != "" {
					entry["api_key"] = v
				}
				if v, ok := prov["api_base"]; ok && v != "" {
					entry["api_base"] = v
				}
				if v, ok := prov["proxy"]; ok && v != "" {
					entry["proxy"] = v
				}
				if v, ok := prov["request_timeout"]; ok && v != nil {
					entry["request_timeout"] = v
				}
				return entry
			},
		},
		{
			jsonKeys: []string{"minimax"},
			protocol: "minimax",
			extractFn: func(prov map[string]any) map[string]any {
				entry := make(map[string]any)
				if v, ok := prov["api_key"]; ok && v != "" {
					entry["api_key"] = v
				}
				if v, ok := prov["api_base"]; ok && v != "" {
					entry["api_base"] = v
				}
				if v, ok := prov["proxy"]; ok && v != "" {
					entry["proxy"] = v
				}
				if v, ok := prov["request_timeout"]; ok && v != nil {
					entry["request_timeout"] = v
				}
				return entry
			},
		},
		{
			jsonKeys: []string{"longcat"},
			protocol: "longcat",
			extractFn: func(prov map[string]any) map[string]any {
				entry := make(map[string]any)
				if v, ok := prov["api_key"]; ok && v != "" {
					entry["api_key"] = v
				}
				if v, ok := prov["api_base"]; ok && v != "" {
					entry["api_base"] = v
				}
				if v, ok := prov["proxy"]; ok && v != "" {
					entry["proxy"] = v
				}
				if v, ok := prov["request_timeout"]; ok && v != nil {
					entry["request_timeout"] = v
				}
				return entry
			},
		},
		{
			jsonKeys: []string{"modelscope"},
			protocol: "modelscope",
			extractFn: func(prov map[string]any) map[string]any {
				entry := make(map[string]any)
				if v, ok := prov["api_key"]; ok && v != "" {
					entry["api_key"] = v
				}
				if v, ok := prov["api_base"]; ok && v != "" {
					entry["api_base"] = v
				}
				if v, ok := prov["proxy"]; ok && v != "" {
					entry["proxy"] = v
				}
				if v, ok := prov["request_timeout"]; ok && v != nil {
					entry["request_timeout"] = v
				}
				return entry
			},
		},
		{
			jsonKeys: []string{"novita"},
			protocol: "novita",
			extractFn: func(prov map[string]any) map[string]any {
				entry := make(map[string]any)
				if v, ok := prov["api_key"]; ok && v != "" {
					entry["api_key"] = v
				}
				if v, ok := prov["api_base"]; ok && v != "" {
					entry["api_base"] = v
				}
				if v, ok := prov["proxy"]; ok && v != "" {
					entry["proxy"] = v
				}
				if v, ok := prov["request_timeout"]; ok && v != nil {
					entry["request_timeout"] = v
				}
				return entry
			},
		},
	}

	// We need access to agents.defaults for user provider/model, but we only have providers map
	// This function is called with just the providers map, so we can't access agents.defaults
	// The caller (migrateV0ToV1) would need to pass this information if needed
	// For now, we skip the user provider/model matching

	var result []any

	for _, migration := range migrations {
		// Find the provider in the providers map
		var provData map[string]any
		found := false
		for _, key := range migration.jsonKeys {
			if v, ok := providers[key]; ok {
				if provMap, ok := v.(map[string]any); ok {
					provData = provMap
					found = true
					break
				}
			}
		}
		if !found {
			continue
		}

		// Extract fields using the extraction function
		entry := migration.extractFn(provData)
		if len(entry) == 0 {
			continue
		}

		// Preserve this as an account transport. A model is attached only
		// when the legacy config explicitly selected one for this provider.
		entry["model_name"] = migration.jsonKeys[0]
		entry["provider"] = migration.protocol

		modelToUse := ""
		if userProvider != "" && userModel != "" {
			for _, key := range migration.jsonKeys {
				if userProvider == key {
					// Build the model string with protocol prefix if needed
					if !strings.Contains(userModel, "/") {
						modelToUse = migration.protocol + "/" + userModel
					} else {
						modelToUse = userModel
					}
					break
				}
			}
		}
		if modelToUse != "" {
			entry["model"] = modelToUse
		}

		result = append(result, entry)
	}

	return result
}
