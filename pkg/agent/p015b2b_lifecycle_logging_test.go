package agent

import "testing"

func TestP015B2BLifecycleLoggingASTManifest(t *testing.T) {
	p015B2BValidateLoggingPartition(t, "lifecycle", map[string]p015B2BFileManifest{
		"context_seahorse.go": {sinks: []p015B2BSinkCount{
			{"WarnSafeCF", "ComponentAgent", 1},
			{"WarnSafeCF", "ComponentSeahorse", 1},
		}},
		"evolution_bridge.go": {sinks: []p015B2BSinkCount{{"WarnSafeCF", "ComponentAgent", 4}}},
		"git_workspace.go":    {sinks: []p015B2BSinkCount{{"WarnSafeCF", "ComponentGitWorkspace", 3}}},
		"legacy_events.go":    {sinks: []p015B2BSinkCount{{"WarnSafeCF", "ComponentAgent", 1}}},
	}, 10)
}
