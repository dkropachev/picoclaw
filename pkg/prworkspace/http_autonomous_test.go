package prworkspace

import (
	"context"
	"testing"
)

func TestAdvanceDevelopmentWorkspaceRunsQueuedImplementationPhase(t *testing.T) {
	service, aggregate := readyImplementationService(t)
	previous := StageRun{
		ID: "psr_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Stage: "implementation",
		State: ExecutionFailed, CharterID: aggregate.Workspace.ActiveCharterID,
		HeadSHA: aggregate.ProviderSnapshot.HeadSHA, Attempt: 1,
		StartedAt: aggregate.Workspace.UpdatedAt, FinishedAt: &aggregate.Workspace.UpdatedAt,
	}
	phase, queued := PhaseImplementation, ExecutionQueued
	mutated, err := service.store.Mutate(context.Background(), Mutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-autonomous-implementation-01",
		Patch: AggregatePatch{
			Phase: &phase, ExecutionState: &queued, AppendStageRuns: []StageRun{previous},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	repair := &implementationRepair{}
	handler, err := NewHTTPHandler(HTTPConfig{
		Service: service,
		Implementation: ImplementationConfig{
			Repair: repair, Validation: implementationValidation{}, MaxCycles: 1,
		},
		CompletionNudgePolicy: ConfiguredNudgePolicy(0, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !handler.AutonomousDevelopmentWorkspaceReady(mutated.Aggregate) ||
		!handler.AutonomousDevelopmentWorkspaceClaimRequired(mutated.Aggregate) {
		t.Fatal("queued implementation phase was not admitted for autonomous execution")
	}

	claimed, err := service.ClaimAutonomousWork(context.Background(), ClaimAutonomousWorkRequest{
		WorkspaceID: mutated.Aggregate.Workspace.ID, ExpectedVersion: mutated.Aggregate.Workspace.Version,
		RequestID: "request-autonomous-implementation-claim",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := handler.AdvanceDevelopmentWorkspace(
		context.Background(), claimed, "request-autonomous-implementation-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	if repair.calls != 1 {
		t.Fatalf("repair calls = %d, want 1", repair.calls)
	}
	if result.Workspace.Version <= claimed.Workspace.Version {
		t.Fatalf(
			"implementation did not persist: before=%d after=%d",
			claimed.Workspace.Version,
			result.Workspace.Version,
		)
	}
}
