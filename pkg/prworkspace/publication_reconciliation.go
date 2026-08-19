package prworkspace

import (
	"context"
	"fmt"
)

// authorizePublicationReconciliation implements the deliberately two-action
// recovery contract shared by issue, review, and branch publications:
//
//   - recheck-provider: re-observe the provider;
//   - assume-failed: unlock a fresh publication.
//
// The returned bool is true only when the caller may perform provider reads.
func (service *Service) authorizePublicationReconciliation(
	ctx context.Context,
	aggregate Aggregate,
	publication Publication,
	authorizationRequest any,
	expectedVersion int64,
	requestID string,
	activitySummary string,
) (Aggregate, bool, error) {
	gate, gateNew, err := service.ensureGate(ctx, aggregate, "pr.publication.reconcile", map[string]any{
		"publication": publication, "request": authorizationRequest,
		"provider_revision": aggregate.ProviderSnapshot.ProviderRevision,
	})
	if err != nil {
		return aggregate, false, err
	}
	gate.TargetID = publication.ID

	if gate.State == ExecutionSucceeded {
		switch gateAction(gate) {
		case "recheck-provider":
			if !gateNew {
				return aggregate, true, nil
			}
			authorized, mutateErr := service.store.Mutate(ctx, Mutation{
				WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: expectedVersion,
				RequestID: requestID + ":gate", Patch: AggregatePatch{
					AppendGates: []GateRun{gate},
					Activity: []Activity{{
						Kind: "publication.reconcile_authorized", Actor: "gate", EntityID: publication.ID,
						Summary: activitySummary, CreatedAt: service.now().UTC(),
					}},
				},
			})
			return authorized.Aggregate, mutateErr == nil, mutateErr
		case "assume-failed":
			patch, patchErr := service.gateActionPatch(aggregate, gate)
			if patchErr != nil {
				return aggregate, false, patchErr
			}
			if gateNew {
				patch.AppendGates = append(patch.AppendGates, gate)
			}
			patch.Activity = append(patch.Activity, Activity{
				Kind:      "publication.reconcile_assumed_failed",
				Actor:     "gate",
				EntityID:  publication.ID,
				Summary:   "Publication assumed failed; a fresh publication may be requested",
				CreatedAt: service.now().UTC(),
			})
			resolved, mutateErr := service.store.Mutate(ctx, Mutation{
				WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: expectedVersion,
				RequestID: requestID, Patch: patch,
			})
			return resolved.Aggregate, false, mutateErr
		default:
			return aggregate, false, fmt.Errorf(
				"%w: publication reconciliation returned no supported action",
				ErrInvalid,
			)
		}
	}

	if gateAction(gate) != "" {
		return aggregate, false, fmt.Errorf(
			"%w: unresolved publication reconciliation gate has field-values",
			ErrInvalid,
		)
	}
	state := ExecutionWaitingGate
	if !gateNew && aggregate.Workspace.ExecutionState == state {
		return aggregate, false, nil
	}
	patch := AggregatePatch{ExecutionState: &state}
	if gateNew {
		patch.AppendGates = []GateRun{gate}
		patch.Activity = []Activity{
			{
				Kind:      "publication.reconcile_requested",
				Actor:     "system",
				EntityID:  publication.ID,
				Summary:   "Publication reconciliation requires re-observe or assume-failed authorization",
				CreatedAt: service.now().UTC(),
			},
		}
	}
	waiting, mutateErr := service.store.Mutate(ctx, Mutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: expectedVersion,
		RequestID: requestID, Patch: patch,
	})
	return waiting.Aggregate, false, mutateErr
}
