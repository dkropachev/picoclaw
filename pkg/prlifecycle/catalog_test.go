package prlifecycle

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

func TestDecisionPointCatalogIsExactOrderedAndDetached(t *testing.T) {
	want := []string{
		"pr.charter.confirm",
		"pr.charter.reconfirm",
		"pr.review.start",
		"pr.review.complete",
		"pr.finding.classify",
		"pr.implementation.eligibility",
		"pr.implementation.start",
		"pr.implementation.scope",
		"pr.implementation.complete",
		"pr.review.publish",
		"pr.implementation.publish",
		"pr.deferred.publish",
		"pr.correction.promote",
		"pr.publication.reconcile",
	}
	points := DecisionPoints()
	if len(points) != len(want) {
		t.Fatalf("catalog length = %d, want %d", len(points), len(want))
	}
	for index, id := range want {
		wantPurpose := gatetypes.GatePurposeAuthorization
		if id == "pr.finding.classify" {
			wantPurpose = gatetypes.GatePurposeClassification
		}
		if points[index].ID != id || points[index].Ordinal != index+1 ||
			points[index].Purpose != wantPurpose || !IsDecisionPoint(id) {
			t.Fatalf("catalog[%d] = %#v, want %q at ordinal %d", index, points[index], id, index+1)
		}
		if ordinal, exists := DecisionPointOrdinal(id); !exists || ordinal != index+1 {
			t.Fatalf("DecisionPointOrdinal(%q) = %d, %v", id, ordinal, exists)
		}
		if purpose, exists := DecisionPointPurpose(id); !exists || purpose != wantPurpose {
			t.Fatalf("DecisionPointPurpose(%q) = %q, %v, want %q", id, purpose, exists, wantPurpose)
		}
	}
	points[0].ID = "mutated"
	if DecisionPoints()[0].ID != want[0] || IsDecisionPoint("mutated") {
		t.Fatal("DecisionPoints returned shared mutable storage")
	}
	if _, exists := DecisionPointPurpose("pr.unknown"); exists {
		t.Fatal("unknown decision point has a purpose")
	}
}
