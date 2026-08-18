// Package prlifecycle owns the closed vocabulary shared by persisted PR
// lifecycle configuration and its presentation projections.
package prlifecycle

import "github.com/sipeed/picoclaw/pkg/workflows/gatetypes"

// DecisionPoint identifies one configurable lifecycle gate and its stable UI
// ordinal. Ordinals are part of the public lifecycle contract.
type DecisionPoint struct {
	ID      string
	Ordinal int
	Purpose gatetypes.GatePurpose
}

var decisionPoints = []DecisionPoint{
	{ID: "pr.charter.confirm", Ordinal: 1, Purpose: gatetypes.GatePurposeAuthorization},
	{ID: "pr.charter.reconfirm", Ordinal: 2, Purpose: gatetypes.GatePurposeAuthorization},
	{ID: "pr.review.start", Ordinal: 3, Purpose: gatetypes.GatePurposeAuthorization},
	{ID: "pr.review.complete", Ordinal: 4, Purpose: gatetypes.GatePurposeAuthorization},
	{ID: "pr.finding.classify", Ordinal: 5, Purpose: gatetypes.GatePurposeClassification},
	{ID: "pr.implementation.eligibility", Ordinal: 6, Purpose: gatetypes.GatePurposeAuthorization},
	{ID: "pr.implementation.start", Ordinal: 7, Purpose: gatetypes.GatePurposeAuthorization},
	{ID: "pr.implementation.scope", Ordinal: 8, Purpose: gatetypes.GatePurposeAuthorization},
	{ID: "pr.implementation.complete", Ordinal: 9, Purpose: gatetypes.GatePurposeAuthorization},
	{ID: "pr.review.publish", Ordinal: 10, Purpose: gatetypes.GatePurposeAuthorization},
	{ID: "pr.implementation.publish", Ordinal: 11, Purpose: gatetypes.GatePurposeAuthorization},
	{ID: "pr.deferred.publish", Ordinal: 12, Purpose: gatetypes.GatePurposeAuthorization},
	{ID: "pr.correction.promote", Ordinal: 13, Purpose: gatetypes.GatePurposeAuthorization},
	{ID: "pr.publication.reconcile", Ordinal: 14, Purpose: gatetypes.GatePurposeAuthorization},
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

// DecisionPointPurpose returns the one purpose the lifecycle assigns to id.
// Purpose is domain-owned metadata: profile authors may configure how the gate
// reaches a decision, but may not change how that decision is interpreted.
func DecisionPointPurpose(id string) (gatetypes.GatePurpose, bool) {
	point, exists := decisionPointCatalog[id]
	return point.Purpose, exists
}
