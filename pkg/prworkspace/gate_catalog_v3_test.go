package prworkspace

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/prlifecycle"
	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

func TestPRLifecycleGateCatalogExactlyCoversDecisionPoints(t *testing.T) {
	catalog, catalogErr := PRLifecycleGateCatalog()
	if catalogErr != nil {
		t.Fatal(catalogErr)
	}
	points := prlifecycle.DecisionPoints()
	if len(catalog) != len(points) {
		t.Fatalf("catalog length = %d, want %d", len(catalog), len(points))
	}
	for index, entry := range catalog {
		if entry.DecisionPoint != points[index].ID || entry.WorkflowRef != PRLifecycleWorkflowRef ||
			entry.WorkflowRevision == "" || entry.GateRef == "" || entry.Gate.Prompt == "" ||
			entry.Gate.DefaultAction == nil {
			t.Fatalf("catalog[%d] = %#v", index, entry)
		}
		if err := gatetypes.ValidateGateAction(*entry.Gate.DefaultAction); err != nil {
			t.Fatalf("catalog[%d] default action: %v", index, err)
		}
		if entry.SourceAISupported != (entry.DecisionPoint == "pr.finding.classify") {
			t.Fatalf("catalog[%d] source AI support = %t", index, entry.SourceAISupported)
		}
	}

	// Returned definitions must not expose mutable catalog storage.
	catalog[0].Gate.Fields[0].Options[0].Label = "mutated"
	fresh, freshErr := PRLifecycleGateCatalog()
	if freshErr != nil {
		t.Fatal(freshErr)
	}
	if fresh[0].Gate.Fields[0].Options[0].Label == "mutated" {
		t.Fatal("PRLifecycleGateCatalog returned shared mutable storage")
	}
}

func TestPRLifecycleGateCatalogRejectsUnknownDecisionPoint(t *testing.T) {
	if _, err := prLifecycleGateCatalogEntry("pr.unknown"); err == nil {
		t.Fatal("unknown decision point error = nil")
	}
}
