package prworkspace

import (
	"context"
	"fmt"
)

// authorizePublicationReconciliation implements the deliberately two-action
// recovery contract shared by issue, review, and branch publications:
//
//   - pass: re-observe the provider;
//   - block: assume the external write failed and unlock a fresh publication;
//   - revise/defer: unsupported, because persisting either as a terminal gate
//     would strand the unchanged unknown publication behind a reusable subject.
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
	gate, gateNew, err := service.ensureGate(ctx, aggregate, "pr.publication.reconcile", "authorization", map[string]any{
		"publication": publication, "request": authorizationRequest,
		"provider_revision": aggregate.ProviderSnapshot.ProviderRevision,
	})
	if err != nil {
		return aggregate, false, err
	}
	gate.TargetID = publication.ID

	if gate.State == ExecutionSucceeded {
		switch gate.Outcome {
		case GatePass:
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
		case GateBlock:
			patch, patchErr := service.gateOutcomePatch(aggregate, gate)
			if patchErr != nil {
				return aggregate, false, patchErr
			}
			if gateNew {
				patch.AppendGates = append(patch.AppendGates, gate)
			}
			patch.Activity = append(patch.Activity, Activity{
				Kind: "publication.reconcile_assumed_failed", Actor: "gate", EntityID: publication.ID,
				Summary: "Publication assumed failed; a fresh publication may be requested", CreatedAt: service.now().UTC(),
			})
			resolved, mutateErr := service.store.Mutate(ctx, Mutation{
				WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: expectedVersion,
				RequestID: requestID, Patch: patch,
			})
			return resolved.Aggregate, false, mutateErr
		case GateRevise, GateDefer:
			if gateNew {
				return aggregate, false, unsupportedPublicationReconciliationOutcome(gate.Outcome)
			}
			// Repair an already-persisted terminal outcome from an interrupted or
			// older process. Touching the publication changes the next gate subject,
			// while staling this gate prevents it from being presented as actionable.
			now := service.now().UTC()
			gate.State = ExecutionStale
			publication.UpdatedAt = now
			publication.PublicErrorCode = "reconcile_outcome_rejected_" + string(gate.Outcome)
			state := ExecutionWaitingUser
			repaired, mutateErr := service.store.Mutate(ctx, Mutation{
				WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: expectedVersion,
				RequestID: requestID, Patch: AggregatePatch{
					ExecutionState: &state, ReplaceGates: []GateRun{gate},
					ReplacePublications: []Publication{publication},
					Activity: []Activity{{
						Kind: "publication.reconcile_outcome_rejected", Actor: "system", EntityID: publication.ID,
						Summary: "Unsupported reconciliation outcome discarded; choose re-observe or assume failed", CreatedAt: now,
					}},
				},
			})
			if mutateErr != nil {
				return repaired.Aggregate, false, mutateErr
			}
			return repaired.Aggregate, false, unsupportedPublicationReconciliationOutcome(gate.Outcome)
		default:
			return aggregate, false, fmt.Errorf("%w: publication reconciliation returned no supported outcome", ErrInvalid)
		}
	}

	if gate.Outcome != "" {
		return aggregate, false, fmt.Errorf("%w: unresolved publication reconciliation gate has an outcome", ErrInvalid)
	}
	state := ExecutionWaitingGate
	if !gateNew && aggregate.Workspace.ExecutionState == state {
		return aggregate, false, nil
	}
	patch := AggregatePatch{ExecutionState: &state}
	if gateNew {
		patch.AppendGates = []GateRun{gate}
		patch.Activity = []Activity{{
			Kind: "publication.reconcile_requested", Actor: "system", EntityID: publication.ID,
			Summary: "Publication reconciliation requires re-observe or assume-failed authorization", CreatedAt: service.now().UTC(),
		}}
	}
	waiting, mutateErr := service.store.Mutate(ctx, Mutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: expectedVersion,
		RequestID: requestID, Patch: patch,
	})
	return waiting.Aggregate, false, mutateErr
}

func unsupportedPublicationReconciliationOutcome(outcome GateOutcome) error {
	return fmt.Errorf(
		"%w: publication reconciliation does not support %q; choose pass to re-observe or block to assume failed",
		ErrInvalid,
		outcome,
	)
}
