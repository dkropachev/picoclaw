package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigrateV3ToV4SeedsUnambiguousAliasesAndAgentSelections(t *testing.T) {
	defaults := map[string]any{
		"model_name":      "router-1",
		"model_fallbacks": []any{"coding", "ambiguous", "raw-model"},
		"image_model":     "raw-image-model",
		"image_model_fallbacks": []any{
			"vision",
			"raw-image-model",
		},
		"routing": map[string]any{
			"light_model": "coding",
		},
	}
	directAgent := map[string]any{
		"id":    "direct",
		"model": "coding",
	}
	routerAgentModel := map[string]any{
		"primary":   "router-1",
		"fallbacks": []any{"vision", "raw-model"},
	}
	routerAgent := map[string]any{
		"id":    "router",
		"model": routerAgentModel,
		"subagents": map[string]any{
			"model": map[string]any{
				"primary":   "raw-model",
				"fallbacks": []any{"coding", "raw-model"},
			},
		},
	}
	m := map[string]any{
		"version": 3,
		"model_list": []any{
			map[string]any{
				"model_name": "coding",
				"model":      "gpt-5.4",
				"provider":   "openai",
				"enabled":    true,
			},
			// Duplicate entries with the same concrete model remain unambiguous.
			map[string]any{
				"model_name": "coding",
				"model":      "gpt-5.4",
				"provider":   "openai",
				"enabled":    true,
			},
			map[string]any{
				"model_name": "vision",
				"model":      "gpt-5.4-vision",
				"provider":   "openai",
				"enabled":    true,
			},
			map[string]any{"model_name": "ambiguous", "model": "model-a", "provider": "openai"},
			map[string]any{"model_name": "ambiguous", "model": "model-b", "provider": "anthropic"},
			map[string]any{
				"model_name": "legacy-router",
				"provider":   AccountRouterProvider,
				"model":      "must-not-become-an-alias",
				"router":     map[string]any{"name": "legacy-router"},
			},
		},
		"account_routers": []any{
			map[string]any{"name": "router-1", "enabled": true},
		},
		"agents": map[string]any{
			"defaults": defaults,
			"list":     []any{directAgent, routerAgent},
		},
	}

	require.NoError(t, migrateV3ToV4(m))
	require.Equal(t, 4, m["version"])
	require.Equal(t, []any{
		map[string]any{"name": "coding", "model": "gpt-5.4"},
		map[string]any{"name": "vision", "model": "gpt-5.4-vision"},
	}, m["model_aliases"])

	require.Equal(t, "router-1", defaults["account_ref"])
	require.Equal(t, "", defaults["model_name"])
	require.Equal(t, []any{"coding"}, defaults["model_fallbacks"])
	require.Equal(t, "", defaults["image_model"])
	require.Equal(t, []any{"vision"}, defaults["image_model_fallbacks"])
	require.Equal(t, "coding", defaults["routing"].(map[string]any)["light_model"])

	require.Equal(t, "coding", directAgent["account_ref"])
	require.Equal(t, "coding", directAgent["model"])
	require.Equal(t, "router-1", routerAgent["account_ref"])
	require.Equal(t, "", routerAgentModel["primary"])
	require.Equal(t, []any{"vision"}, routerAgentModel["fallbacks"])
	subagentModel := routerAgent["subagents"].(map[string]any)["model"].(map[string]any)
	require.Equal(t, "", subagentModel["primary"])
	require.Equal(t, []any{"coding"}, subagentModel["fallbacks"])
}

func TestMigrateV3ToV4MovesCredentialOnlyDefaultWithoutInventingModel(t *testing.T) {
	defaults := map[string]any{
		"model_name": "credential:openai:work",
	}
	m := map[string]any{
		"version":    3,
		"model_list": []any{},
		"agents": map[string]any{
			"defaults": defaults,
		},
	}

	require.NoError(t, migrateV3ToV4(m))
	require.Equal(t, []any{}, m["model_aliases"])
	require.Equal(t, "credential:openai:work", defaults["account_ref"])
	require.Equal(t, "", defaults["model_name"])
}

func TestMigrateV3ToV4KeepsConcreteDefaultAliasAndAccount(t *testing.T) {
	defaults := map[string]any{"model_name": "coding"}
	m := map[string]any{
		"version": 3,
		"model_list": []any{
			map[string]any{"model_name": "coding", "model": "gpt-5.4", "enabled": true},
		},
		"agents": map[string]any{"defaults": defaults},
	}

	require.NoError(t, migrateV3ToV4(m))
	require.Equal(t, "coding", defaults["account_ref"])
	require.Equal(t, "coding", defaults["model_name"])
}

func TestMigrateV3ToV4ClearsSelectionForDisabledAccount(t *testing.T) {
	defaults := map[string]any{"model_name": "coding"}
	m := map[string]any{
		"version": 3,
		"model_list": []any{
			map[string]any{
				"model_name": "coding",
				"model":      "gpt-5.4",
				"enabled":    false,
			},
		},
		"agents": map[string]any{"defaults": defaults},
	}

	require.NoError(t, migrateV3ToV4(m))
	require.Equal(t, "", defaults["account_ref"])
	require.Equal(t, "coding", defaults["model_name"])
}

func TestMigrateV3ToV4MapsLegacyRawModelIDToItsConcreteAccount(t *testing.T) {
	defaults := map[string]any{"model_name": "gpt-4o"}
	agent := map[string]any{"id": "worker", "model": "openai/gpt-4o"}
	m := map[string]any{
		"version": 3,
		"model_list": []any{
			map[string]any{
				"model_name": "openai-work",
				"provider":   "openai",
				"model":      "openai/gpt-4o",
				"enabled":    true,
			},
		},
		"agents": map[string]any{
			"defaults": defaults,
			"list":     []any{agent},
		},
	}

	require.NoError(t, migrateV3ToV4(m))
	require.Equal(t, "openai-work", defaults["account_ref"])
	require.Equal(t, "gpt-4o", defaults["model_name"])
	require.Equal(t, "openai-work", agent["account_ref"])
	require.Equal(t, "openai/gpt-4o", agent["model"])
	require.Equal(t, []any{
		map[string]any{"name": "openai-work", "model": "openai/gpt-4o"},
		map[string]any{"name": "gpt-4o", "model": "openai/gpt-4o"},
		map[string]any{"name": "openai/gpt-4o", "model": "openai/gpt-4o"},
	}, m["model_aliases"])
}

func TestMigrateV3ToV4DoesNotGuessAmbiguousRawModelAccount(t *testing.T) {
	defaults := map[string]any{"model_name": "shared-model"}
	m := map[string]any{
		"version": 3,
		"model_list": []any{
			map[string]any{
				"model_name": "account-a",
				"provider":   "openai",
				"model":      "shared-model",
			},
			map[string]any{
				"model_name": "account-b",
				"provider":   "openai",
				"model":      "shared-model",
			},
		},
		"agents": map[string]any{"defaults": defaults},
	}

	require.NoError(t, migrateV3ToV4(m))
	require.Equal(t, "", defaults["account_ref"])
	require.Equal(t, "", defaults["model_name"])
	for _, value := range m["model_aliases"].([]any) {
		require.NotEqual(t, "shared-model", value.(map[string]any)["name"])
	}
}

func TestMigrateV3ToV4RejectsWrongVersion(t *testing.T) {
	err := migrateV3ToV4(map[string]any{"version": 2})
	require.ErrorContains(t, err, "expected version 3")
}

func TestMigrateV4ToV5RemovesGeneratedAliasesAndNormalizesSearchRole(t *testing.T) {
	defaults := map[string]any{
		"model_name":      "openai-work",
		"model_fallbacks": []any{"custom", "gpt-5.4"},
	}
	gemini := map[string]any{"model_alias": "web-search-gemini"}
	m := map[string]any{
		"version": 4,
		"agents":  map[string]any{"defaults": defaults},
		"model_list": []any{
			map[string]any{
				"model_name": "openai-work",
				"provider":   "openai",
				"model":      "gpt-5.4",
			},
		},
		"model_aliases": []any{
			map[string]any{"name": "openai-work", "model": "gpt-5.4"},
			map[string]any{"name": "gpt-5.4", "model": "gpt-5.4"},
			map[string]any{"name": "custom", "model": "gpt-5.4-mini"},
			map[string]any{"name": "web-search-gemini", "model": "gemini-2.5-flash"},
		},
		"tools": map[string]any{
			"web": map[string]any{"gemini": gemini},
		},
	}

	require.NoError(t, migrateV4ToV5(m))
	require.Equal(t, 5, m["version"])
	require.Equal(t, []any{
		map[string]any{"name": "custom", "model": "gpt-5.4-mini"},
		map[string]any{"name": "investigate", "model": "gemini-2.5-flash"},
	}, m["model_aliases"])
	require.Equal(t, "", defaults["model_name"])
	require.Equal(t, []any{"custom"}, defaults["model_fallbacks"])
	require.Equal(t, "investigate", gemini["model_alias"])
}

func TestMigrateV4ToV5PreservesPredefinedRoleMapping(t *testing.T) {
	m := map[string]any{
		"version": 4,
		"model_list": []any{
			map[string]any{"model_name": "code", "provider": "openai", "model": "gpt-5.4"},
		},
		"model_aliases": []any{
			map[string]any{"name": "code", "model": "gpt-5.4"},
		},
	}

	require.NoError(t, migrateV4ToV5(m))
	require.Equal(t, []any{
		map[string]any{"name": "code", "model": "gpt-5.4"},
	}, m["model_aliases"])
}

func TestMigrateV4ToV5DropsRouterAndSelectionForRemovedGeneratedAlias(t *testing.T) {
	defaults := map[string]any{"model_name": "task-router"}
	m := map[string]any{
		"version": 4,
		"agents":  map[string]any{"defaults": defaults},
		"model_list": []any{
			map[string]any{"model_name": "openai-work", "provider": "openai", "model": "gpt-5.4"},
		},
		"model_aliases": []any{
			map[string]any{"name": "openai-work", "model": "gpt-5.4"},
		},
		"model_routers": []any{
			map[string]any{
				"name": "task-router",
				"blocks": []any{
					map[string]any{"id": "entry", "type": ModelRouterBlockTypeModel, "model": "openai-work"},
				},
			},
		},
	}

	require.NoError(t, migrateV4ToV5(m))
	require.Empty(t, m["model_aliases"])
	require.Empty(t, m["model_routers"])
	require.Equal(t, "", defaults["model_name"])
}

func TestMigrateV4ToV5RejectsWrongVersion(t *testing.T) {
	err := migrateV4ToV5(map[string]any{"version": 3})
	require.ErrorContains(t, err, "expected version 4")
}

func TestMigrateV3ToV4MovesWebSearchModelsBehindAliases(t *testing.T) {
	gemini := map[string]any{
		"enabled": true,
		"model":   "gemini/gemini-2.5-flash",
	}
	perplexity := map[string]any{"enabled": true}
	m := map[string]any{
		"version":    3,
		"model_list": []any{},
		"tools": map[string]any{
			"web": map[string]any{
				"gemini":     gemini,
				"perplexity": perplexity,
			},
		},
	}

	require.NoError(t, migrateV3ToV4(m))
	require.NotContains(t, gemini, "model")
	require.Equal(t, "web-search-gemini", gemini["model_alias"])
	require.Equal(t, "web-search-perplexity", perplexity["model_alias"])
	require.ElementsMatch(t, []any{
		map[string]any{
			"name":  "web-search-gemini",
			"model": "gemini/gemini-2.5-flash",
		},
		map[string]any{
			"name":  "web-search-perplexity",
			"model": "perplexity/sonar",
		},
	}, m["model_aliases"])
}

func TestFailedV3MigrationDoesNotOverwriteOriginalConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := []byte(`{
		"version": 3,
		"tools": {
			"web": {
				"gemini": {
					"enabled": true,
					"model": ""
				}
			}
		}
	}`)
	require.NoError(t, os.WriteFile(path, data, 0o600))

	_, err := LoadConfig(path)
	require.ErrorContains(t, err, "no model configured")

	saved, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(saved, &raw))
	require.Equal(t, float64(3), raw["version"])
}

func TestLoadConfigMigratesV3ModelAliasesEndToEnd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := []byte(`{
		"version": 3,
		"agents": {
			"defaults": {
				"model_name": "account-work"
			}
		},
		"model_list": [{
			"model_name": "account-work",
			"provider": "openai",
			"model": "gpt-5.4",
			"enabled": true
		}],
		"account_routers": [{
			"name": "router-1",
			"enabled": true,
			"entry": "entry",
			"blocks": [{
				"id": "entry",
				"type": "account",
				"account": "credential:openai:work"
			}]
		}],
		"model_routers": [{
			"name": "task-router",
			"enabled": true,
			"entry": "entry",
			"blocks": [{
				"id": "entry",
				"type": "model",
				"model": "account-work"
			}]
		}],
		"events": {
			"logging": {
				"enabled": false
			}
		},
		"gateway": {
			"host": "127.0.0.1",
			"port": 18790
		}
	}`)
	require.NoError(t, os.WriteFile(path, data, 0o600))

	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	require.Equal(t, CurrentVersion, cfg.Version)
	require.Equal(t, "account-work", cfg.Agents.Defaults.AccountRef)
	require.Empty(t, cfg.Agents.Defaults.ModelName)
	_, err = cfg.GetModelAlias("account-work")
	require.ErrorContains(t, err, "not configured")

	saved, err := os.ReadFile(path)
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(saved, &raw))
	require.Equal(t, float64(CurrentVersion), raw["version"])
	require.Empty(t, raw["model_aliases"])

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	hasBackup := false
	for _, entry := range entries {
		matched, matchErr := filepath.Match("config.json.*.bak", entry.Name())
		require.NoError(t, matchErr)
		hasBackup = hasBackup || matched
	}
	require.True(t, hasBackup, "V3 migration must create a backup")
}
