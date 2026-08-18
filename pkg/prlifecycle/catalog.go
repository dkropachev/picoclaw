// Package prlifecycle owns the closed vocabulary shared by persisted PR
// lifecycle configuration and its presentation projections.
package prlifecycle

// DecisionPoint identifies one configurable lifecycle gate and its stable UI
// ordinal. Ordinals are part of the public lifecycle contract.
type DecisionPoint struct {
	ID      string
	Ordinal int
}

var decisionPoints = []DecisionPoint{
	{ID: "pr.charter.confirm", Ordinal: 1},
	{ID: "pr.charter.reconfirm", Ordinal: 2},
	{ID: "pr.review.start", Ordinal: 3},
	{ID: "pr.review.complete", Ordinal: 4},
	{ID: "pr.finding.classify", Ordinal: 5},
	{ID: "pr.implementation.eligibility", Ordinal: 6},
	{ID: "pr.implementation.start", Ordinal: 7},
	{ID: "pr.implementation.scope", Ordinal: 8},
	{ID: "pr.implementation.complete", Ordinal: 9},
	{ID: "pr.review.publish", Ordinal: 10},
	{ID: "pr.implementation.publish", Ordinal: 11},
	{ID: "pr.deferred.publish", Ordinal: 12},
	{ID: "pr.correction.promote", Ordinal: 13},
	{ID: "pr.publication.reconcile", Ordinal: 14},
	{ID: "pr.implementation.hard-scope", Ordinal: 15},
}

var decisionPointCatalog = func() map[string]DecisionPoint {
	values := make(map[string]DecisionPoint, len(decisionPoints))
	for _, point := range decisionPoints {
		values[point.ID] = point
	}
	return values
}()

// DecisionPoints returns the complete ordered catalog as detached storage.
func DecisionPoints() []DecisionPoint {
	return append([]DecisionPoint(nil), decisionPoints...)
}

// IsDecisionPoint reports whether id belongs to the closed lifecycle catalog.
func IsDecisionPoint(id string) bool {
	_, exists := decisionPointCatalog[id]
	return exists
}

// DecisionPointOrdinal returns the stable gate ordinal for id.
func DecisionPointOrdinal(id string) (int, bool) {
	point, exists := decisionPointCatalog[id]
	return point.Ordinal, exists
}
