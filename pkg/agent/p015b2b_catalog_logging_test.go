package agent

import "testing"

func TestP015B2BCatalogLoggingASTManifest(t *testing.T) {
	p015B2BValidateLoggingPartition(t, "catalog/init", map[string]p015B2BFileManifest{
		"account_alias_resolution.go": {sinks: []p015B2BSinkCount{{"WarnSafeCF", "ComponentAgent", 2}}},
		"agent_command.go":            {sinks: []p015B2BSinkCount{{"WarnSafeCF", "ComponentAgent", 2}}},
		"agent_init.go": {sinks: []p015B2BSinkCount{
			{"ErrorSafeCF", "ComponentAgent", 3},
			{"WarnSafeCF", "ComponentAgent", 5},
			{"WarnSafeCF", "ComponentVoiceTTS", 1},
		}},
		"agent_mcp.go": {sinks: []p015B2BSinkCount{
			{"ErrorSafeCF", "ComponentAgent", 3},
			{"InfoSafeCF", "ComponentAgent", 2},
			{"WarnSafeCF", "ComponentAgent", 4},
		}},
		"definition.go": {sinks: []p015B2BSinkCount{{"WarnSafeCF", "ComponentAgent", 1}}},
		"instance.go": {sinks: []p015B2BSinkCount{
			{"DebugSafeCF", "ComponentAgent", 1},
			{"ErrorSafeCF", "ComponentAgent", 1},
			{"InfoSafeCF", "ComponentAgent", 1},
			{"WarnSafeCF", "ComponentAgent", 5},
		}},
		"prompt.go": {sinks: []p015B2BSinkCount{{"WarnSafeCF", "ComponentAgent", 2}}},
		"recursion_tool_factory_catalog.go": {
			sinks: []p015B2BSinkCount{{"ErrorSafeCF", "ComponentAgent", 1}},
		},
		"registry.go": {sinks: []p015B2BSinkCount{
			{"InfoSafeCF", "ComponentAgent", 2},
			{"WarnSafeCF", "ComponentAgent", 1},
		}},
		"thinking.go":       {sinks: []p015B2BSinkCount{{"WarnSafeCF", "ComponentAgent", 1}}},
		"tool_allowlist.go": {sinks: []p015B2BSinkCount{{"WarnSafeCF", "ComponentAgent", 2}}},
	}, 40)
}
