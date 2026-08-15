package prlifecycle

import "testing"

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
		if points[index].ID != id || points[index].Ordinal != index+1 || !IsDecisionPoint(id) {
			t.Fatalf("catalog[%d] = %#v, want %q at ordinal %d", index, points[index], id, index+1)
		}
		if ordinal, exists := DecisionPointOrdinal(id); !exists || ordinal != index+1 {
			t.Fatalf("DecisionPointOrdinal(%q) = %d, %v", id, ordinal, exists)
		}
	}
	points[0].ID = "mutated"
	if DecisionPoints()[0].ID != want[0] || IsDecisionPoint("mutated") {
		t.Fatal("DecisionPoints returned shared mutable storage")
	}
}
