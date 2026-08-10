package gitworkspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestManagerRecoverPinnedLineAdoptReservationSuspensionCapacity(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedCommitTestFixture(t, "pr-development/adopt-capacity-old")
	installPinnedRecoveryRotationHistory(
		t,
		fixture.manager,
		fixture.workspace.ID,
		"",
		0,
		0,
		"",
		"",
		fixture.pin.ReservationKey,
		fixture.pin.AgentID,
		maxPinnedReservationRotations-2,
		true,
	)
	request := PinnedLineAdoptRecoveryRequest{
		Adopt: PinnedLineAdoptRequest{
			Pin:          fixture.pin,
			WorkspaceID:  fixture.workspace.ID,
			LineID:       pinnedLineTestID,
			ExpectedTree: testGitObject(t, fixture.workspace.Path, "rev-parse", "HEAD^{tree}"),
		},
		IntentID:                  "pdlr_adopt_suspension_capacity",
		ReplacementReservationKey: "pr-development/adopt-capacity-fresh",
		RequireSuspensionCapacity: true,
	}

	recovered, err := fixture.manager.RecoverPinnedLineAdoptReservation(ctx, request)
	if err != nil {
		t.Fatalf("guarded adoption recovery error = %v", err)
	}
	replayed, err := fixture.manager.RecoverPinnedLineAdoptReservation(ctx, request)
	if err != nil || !replayed.AlreadyRotated ||
		replayed.RotationHash != recovered.RotationHash {
		t.Fatalf("guarded adoption replay = %#v, %v", replayed, err)
	}

	before := readPinnedRecoveryCapacityInventory(t, fixture.manager)
	second := request
	second.Adopt.Pin.ReservationKey = request.ReplacementReservationKey
	second.IntentID = "pdlr_adopt_suspension_capacity_rejected"
	second.ReplacementReservationKey = "pr-development/adopt-capacity-overflow"
	if _, err := fixture.manager.RecoverPinnedLineAdoptReservation(ctx, second); err == nil ||
		!errors.Is(err, ErrPinnedLineConflict) ||
		!strings.Contains(err.Error(), "suspended-resume rotation capacity") {
		t.Fatalf("guarded adoption capacity error = %v", err)
	}
	assertPinnedRecoveryCapacityInventoryUnchanged(t, fixture.manager, before)
}

func TestManagerRecoverPinnedLineResumeReservationSuspensionCapacity(t *testing.T) {
	ctx := context.Background()
	fixture, request := newPinnedLineResumeRecoveryFixture(
		t,
		"pr-development/resume-suspension-capacity",
	)
	installPinnedRecoveryRotationHistory(
		t,
		fixture.manager,
		fixture.workspace.ID,
		pinnedLineTestID,
		0,
		1,
		request.Resume.ExpectedTip,
		request.Resume.ExpectedTree,
		fixture.pin.ReservationKey,
		fixture.pin.AgentID,
		maxPinnedReservationRotations-2,
		false,
	)
	request.RequireSuspensionCapacity = true

	recovered, err := fixture.manager.RecoverPinnedLineResumeReservation(ctx, request)
	if err != nil {
		t.Fatalf("guarded resume recovery error = %v", err)
	}
	replayed, err := fixture.manager.RecoverPinnedLineResumeReservation(ctx, request)
	if err != nil || !replayed.AlreadyRotated ||
		replayed.RotationHash != recovered.RotationHash {
		t.Fatalf("guarded resume replay = %#v, %v", replayed, err)
	}

	freshPin := request.Resume.Pin
	freshPin.ReservationKey = request.ReplacementReservationKey
	parked, err := fixture.manager.ParkPinnedLine(ctx, PinnedLineParkRequest{
		Pin:             freshPin,
		WorkspaceID:     fixture.workspace.ID,
		LineID:          pinnedLineTestID,
		IntentID:        "pdlnpark_resume_suspension_capacity",
		ExpectedVersion: recovered.Version,
		MutationEpoch:   recovered.MutationEpoch,
		PreviousTip:     recovered.Tip,
		Tip:             recovered.Tip,
		Tree:            recovered.Tree,
		NoChanges:       true,
	})
	if err != nil {
		t.Fatalf("park guarded recovery = %v", err)
	}
	before := readPinnedRecoveryCapacityInventory(t, fixture.manager)
	secondPin := request.Resume.Pin
	secondPin.ReservationKey = "pr-development/resume-capacity-second-old"
	second := PinnedLineResumeRecoveryRequest{
		Resume: PinnedLineResumeRequest{
			Pin:             secondPin,
			WorkspaceID:     fixture.workspace.ID,
			LineID:          pinnedLineTestID,
			ExpectedVersion: parked.Version,
			ExpectedEpoch:   parked.MutationEpoch,
			ExpectedTip:     parked.Tip,
			ExpectedTree:    parked.Tree,
		},
		IntentID:                  "pdlr_resume_suspension_capacity_rejected",
		ReplacementReservationKey: "pr-development/resume-capacity-overflow",
		RequireSuspensionCapacity: true,
	}
	if _, err := fixture.manager.RecoverPinnedLineResumeReservation(ctx, second); err == nil ||
		!errors.Is(err, ErrPinnedLineConflict) ||
		!strings.Contains(err.Error(), "suspended-resume rotation capacity") {
		t.Fatalf("guarded resume capacity error = %v", err)
	}
	assertPinnedRecoveryCapacityInventoryUnchanged(t, fixture.manager, before)
}

func TestManagerRotatePinnedReservationSuspensionCapacity(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/rotation-suspension-capacity")
	installPinnedRecoveryRotationHistory(
		t,
		fixture.manager,
		fixture.workspace.ID,
		pinnedLineTestID,
		fixture.lease.Version,
		fixture.lease.MutationEpoch,
		fixture.lease.Tip,
		fixture.lease.Tree,
		fixture.pin.ReservationKey,
		fixture.pin.AgentID,
		maxPinnedReservationRotations-2,
		true,
	)
	request := boundPinnedReservationRotationRequest(
		fixture,
		"pdrr_suspension_capacity",
		"pr-development/rotation-capacity-fresh",
	)
	request.RequireSuspensionCapacity = true

	rotated, err := fixture.manager.RotatePinnedReservation(ctx, request)
	if err != nil {
		t.Fatalf("guarded bound rotation error = %v", err)
	}
	compatibleReplay := request
	compatibleReplay.RequireSuspensionCapacity = false
	compatible, err := fixture.manager.RotatePinnedReservation(ctx, compatibleReplay)
	if err != nil || !compatible.AlreadyRotated ||
		compatible.RotationHash != rotated.RotationHash {
		t.Fatalf("default-compatible bound rotation replay = %#v, %v", compatible, err)
	}
	replayed, err := fixture.manager.RotatePinnedReservation(ctx, request)
	if err != nil || !replayed.AlreadyRotated || replayed.RotationHash != rotated.RotationHash {
		t.Fatalf("guarded bound rotation replay = %#v, %v", replayed, err)
	}

	before := readPinnedRecoveryCapacityInventory(t, fixture.manager)
	second := request
	second.Pin.ReservationKey = request.ReplacementReservationKey
	second.IntentID = "pdrr_suspension_capacity_rejected"
	second.ReplacementReservationKey = "pr-development/rotation-capacity-overflow"
	if _, err := fixture.manager.RotatePinnedReservation(ctx, second); err == nil ||
		!errors.Is(err, ErrPinnedLineConflict) ||
		!strings.Contains(err.Error(), "suspended-resume rotation capacity") {
		t.Fatalf("guarded bound rotation capacity error = %v", err)
	}
	assertPinnedRecoveryCapacityInventoryUnchanged(t, fixture.manager, before)
}

func TestManagerResumeSuspendedPinnedLineReservesRecoveryCapacity(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/suspended-resume-capacity-old")
	installPinnedRecoveryRotationHistory(
		t,
		fixture.manager,
		fixture.workspace.ID,
		pinnedLineTestID,
		fixture.lease.Version,
		fixture.lease.MutationEpoch,
		fixture.lease.Tip,
		fixture.lease.Tree,
		fixture.pin.ReservationKey,
		fixture.pin.AgentID,
		maxPinnedReservationRotations-1,
		true,
	)
	suspended, err := fixture.manager.SuspendPinnedLine(
		ctx,
		suspensionAPITestSuspendRequest(
			fixture,
			"pdlnsuspend_resume_capacity",
		),
	)
	if err != nil {
		t.Fatalf("SuspendPinnedLine() error = %v", err)
	}
	before := readPinnedRecoveryCapacityInventory(t, fixture.manager)
	request := suspensionAPITestResumeRequest(
		fixture,
		suspended,
		"pr-development/suspended-resume-capacity-fresh",
		"pdlnresume_capacity_rejected",
	)
	if _, err = fixture.manager.ResumeSuspendedPinnedLine(ctx, request); err == nil ||
		!errors.Is(err, ErrPinnedLineConflict) ||
		!strings.Contains(err.Error(), "suspended-resume rotation capacity") {
		t.Fatalf("ResumeSuspendedPinnedLine() capacity error = %v", err)
	}
	assertPinnedRecoveryCapacityInventoryUnchanged(t, fixture.manager, before)
}

func TestPinnedRecoverySuspensionCapacityRequiresBoundAvailableLine(t *testing.T) {
	fixture := newPinnedCommitTestFixture(t, "pr-development/rotation-capacity-unbound")
	request := unboundPinnedReservationRotationRequest(
		fixture,
		"pdrr_capacity_unbound",
		"pr-development/rotation-capacity-unbound-fresh",
	)
	request.RequireSuspensionCapacity = true
	before := readPinnedRecoveryCapacityInventory(t, fixture.manager)
	if _, err := fixture.manager.RotatePinnedReservation(context.Background(), request); err == nil ||
		!errors.Is(err, ErrPinnedLineInvalid) {
		t.Fatalf("unbound suspension-capacity rotation error = %v", err)
	}
	assertPinnedRecoveryCapacityInventoryUnchanged(t, fixture.manager, before)

	state := &storeState{
		DevelopmentLines: map[string]*developmentLineRecord{
			pinnedLineTestID: {SuspensionCount: maxDevelopmentLineReservations},
		},
		PinnedReservationRotations: map[string][]pinnedReservationRotationRecord{},
	}
	if err := requirePinnedRecoverySuspensionCapacity(
		state,
		"capacity-workspace",
		pinnedLineTestID,
		false,
		false,
	); err == nil || !strings.Contains(err.Error(), "suspension capacity") {
		t.Fatalf("full line suspension capacity error = %v", err)
	}
}

func installPinnedRecoveryRotationHistory(
	t *testing.T,
	manager *Manager,
	workspaceID, lineID string,
	version, mutationEpoch int64,
	tip, tree, finalReservation, agentID string,
	count int,
	finalActive bool,
) {
	t.Helper()
	adversarialMutateInventory(t, manager, func(state *storeState) {
		workspace := state.Workspaces[workspaceID]
		if workspace == nil || count <= 0 {
			t.Fatalf("capacity history owner = %#v, count %d", workspace, count)
		}
		previousRecordHash := emptyPinnedReservationRotationDigest()
		previousReservation := "pr-development/capacity-history-seed"
		rotations := make([]pinnedReservationRotationRecord, 0, count)
		for ordinal := 0; ordinal < count; ordinal++ {
			replacement := fmt.Sprintf(
				"pr-development/capacity-history-%04x",
				ordinal,
			)
			if ordinal == count-1 {
				replacement = finalReservation
			}
			record := pinnedReservationRotationRecord{
				IntentID:                   fmt.Sprintf("pdrr_capacity_guard_%04x", ordinal),
				WorkspaceID:                workspace.ID,
				LineID:                     lineID,
				RepoID:                     workspace.RepoID,
				SourceRef:                  workspace.PinnedSourceRef,
				SourceCommit:               workspace.PinnedCommit,
				Version:                    version,
				MutationEpoch:              mutationEpoch,
				Tip:                        tip,
				Tree:                       tree,
				PreviousReservationHash:    developmentLineReservationHash(previousReservation),
				ReplacementReservationHash: developmentLineReservationHash(replacement),
				AgentID:                    agentID,
				PreviousRecordHash:         previousRecordHash,
				RotatedAt:                  workspace.CreatedAt,
			}
			record.RecordHash = pinnedReservationRotationRecordDigest(record)
			rotations = append(rotations, record)
			previousReservation = replacement
			previousRecordHash = record.RecordHash
		}
		state.PinnedReservationRotations[workspace.ID] = rotations
		workspace.PinnedReservationRotationCount = len(rotations)
		workspace.PinnedReservationRotationTailHash = previousRecordHash
		if finalActive {
			workspace.LockedBy = &LockInfo{
				SessionKey:  finalReservation,
				AgentID:     agentID,
				LockedAt:    workspace.CreatedAt,
				HeartbeatAt: workspace.CreatedAt,
			}
			if lineID != "" {
				line := state.DevelopmentLines[lineID]
				if line == nil {
					t.Fatalf("capacity history line %q is missing", lineID)
				}
				line.MutationReservationHash = developmentLineReservationHash(finalReservation)
				line.MutationAgentID = agentID
			}
		}
	})
}

func readPinnedRecoveryCapacityInventory(t *testing.T, manager *Manager) string {
	t.Helper()
	data, err := os.ReadFile(manager.statePath())
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func assertPinnedRecoveryCapacityInventoryUnchanged(
	t *testing.T,
	manager *Manager,
	before string,
) {
	t.Helper()
	if after := readPinnedRecoveryCapacityInventory(t, manager); after != before {
		t.Fatal("rejected suspension-capacity operation changed inventory")
	}
}
