package agent

import "testing"

func TestP015B2BWorkflowLoggingASTManifest(t *testing.T) {
	p015B2BValidateLoggingPartition(t, "workflow", map[string]p015B2BFileManifest{
		"workflow_automations.go": {sinks: []p015B2BSinkCount{{"WarnSafeCF", "ComponentWorkflow", 17}}},
		"workflow_runtime.go":     {sinks: []p015B2BSinkCount{{"WarnSafeCF", "ComponentWorkflow", 1}}},
		"workflow_triggers.go":    {sinks: []p015B2BSinkCount{{"WarnSafeCF", "ComponentWorkflow", 6}}},
	}, 24)
}
