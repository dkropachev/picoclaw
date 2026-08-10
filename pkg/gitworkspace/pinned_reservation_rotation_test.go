package gitworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPinnedReservationRotationJSONIsPrivate(t *testing.T) {
	request := PinnedReservationRotationRequest{
		Pin: PinnedAcquireRequest{
			Repository:     "private-repository",
			SourceRef:      "private-source",
			ExpectedCommit: strings.Repeat("a", 40),
			ReservationKey: "private-old-bearer",
			AgentID:        "private-agent",
		},
		WorkspaceID:               "private-workspace",
		IntentID:                  "private-intent",
		ReplacementReservationKey: "private-new-bearer",
		LineID:                    "private-line",
		ExpectedVersion:           7,
		ExpectedMutationEpoch:     8,
		ExpectedTip:               strings.Repeat("b", 40),
		ExpectedTree:              strings.Repeat("c", 40),
	}
	requestJSON, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if string(requestJSON) != "{}" {
		t.Fatalf("rotation request JSON exposed private state: %s", requestJSON)
	}
	result := PinnedReservationRotationResult{
		WorkspaceID:    "private-workspace",
		Bound:          true,
		Version:        7,
		MutationEpoch:  8,
		Tip:            strings.Repeat("d", 40),
		Tree:           strings.Repeat("e", 40),
		RotationHash:   strings.Repeat("f", 64),
		AlreadyRotated: true,
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if string(resultJSON) != "{}" {
		t.Fatalf("rotation result JSON exposed private state: %s", resultJSON)
	}
}

func TestManagerRotatePinnedReservationUnboundIsExactAndGitNeutral(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedCommitTestFixture(t, "pr-development/rotation-unbound-old")
	headBefore := testGitCommit(t, fixture.workspace.Path, "HEAD")
	treeBefore := testGitObject(t, fixture.workspace.Path, "write-tree")
	statusBefore, err := runGit(ctx, fixture.workspace.Path, "status", "--porcelain=v1")
	if err != nil {
		t.Fatal(err)
	}
	request := unboundPinnedReservationRotationRequest(
		fixture,
		"pdrr_unbound_first",
		"pr-development/rotation-unbound-new",
	)
	rotated, err := fixture.manager.RotatePinnedReservation(ctx, request)
	if err != nil {
		t.Fatalf("RotatePinnedReservation() error = %v", err)
	}
	if rotated.WorkspaceID != fixture.workspace.ID || rotated.Bound ||
		rotated.Version != 0 || rotated.MutationEpoch != 0 || rotated.Tip != "" ||
		rotated.Tree != "" || !validLowerHex(rotated.RotationHash, 64) ||
		rotated.AlreadyRotated {
		t.Fatalf("RotatePinnedReservation() = %#v", rotated)
	}

	restarted, err := NewManager(fixture.manager.opts)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := restarted.RotatePinnedReservation(ctx, request)
	if err != nil {
		t.Fatalf("replayed RotatePinnedReservation() error = %v", err)
	}
	if !replayed.AlreadyRotated || replayed.RotationHash != rotated.RotationHash ||
		replayed.WorkspaceID != rotated.WorkspaceID {
		t.Fatalf("replayed RotatePinnedReservation() = %#v", replayed)
	}
	if headAfter := testGitCommit(t, fixture.workspace.Path, "HEAD"); headAfter != headBefore {
		t.Fatalf("reservation rotation changed HEAD from %q to %q", headBefore, headAfter)
	}
	if treeAfter := testGitObject(t, fixture.workspace.Path, "write-tree"); treeAfter != treeBefore {
		t.Fatalf("reservation rotation changed the index tree from %q to %q", treeBefore, treeAfter)
	}
	statusAfter, err := runGit(ctx, fixture.workspace.Path, "status", "--porcelain=v1")
	if err != nil {
		t.Fatal(err)
	}
	if statusAfter != statusBefore {
		t.Fatalf("reservation rotation changed worktree status from %q to %q", statusBefore, statusAfter)
	}

	callbackCalled := false
	err = restarted.WithPinnedOperation(ctx, fixture.pin, func(context.Context) error {
		callbackCalled = true
		return nil
	})
	if err == nil || !errors.Is(err, ErrPinnedLineConflict) || callbackCalled {
		t.Fatalf("stale WithPinnedOperation() = called %v, error %v", callbackCalled, err)
	}
	if _, acquireErr := restarted.AcquirePinned(ctx, fixture.pin); acquireErr == nil ||
		!strings.Contains(acquireErr.Error(), "rotation") {
		t.Fatalf("stale AcquirePinned() error = %v", acquireErr)
	}

	newPin := fixture.pin
	newPin.ReservationKey = request.ReplacementReservationKey
	if heartbeat, heartbeatErr := restarted.AcquirePinned(ctx, newPin); heartbeatErr != nil ||
		heartbeat.ID != fixture.workspace.ID {
		t.Fatalf("replacement AcquirePinned() = %#v, %v", heartbeat, heartbeatErr)
	}
	secondRequest := PinnedReservationRotationRequest{
		Pin:                       newPin,
		WorkspaceID:               fixture.workspace.ID,
		IntentID:                  "pdrr_unbound_second",
		ReplacementReservationKey: "pr-development/rotation-unbound-newer",
	}
	second, err := restarted.RotatePinnedReservation(ctx, secondRequest)
	if err != nil {
		t.Fatalf("second RotatePinnedReservation() error = %v", err)
	}
	if second.AlreadyRotated || second.RotationHash == rotated.RotationHash {
		t.Fatalf("second RotatePinnedReservation() = %#v", second)
	}
	if _, err := restarted.RotatePinnedReservation(ctx, request); err == nil ||
		!errors.Is(err, ErrPinnedLineConflict) {
		t.Fatalf("superseded rotation replay error = %v", err)
	}
	adversarialInspectInventory(t, restarted, func(state *storeState) {
		records := state.PinnedReservationRotations[fixture.workspace.ID]
		if len(records) != 2 || records[0].RecordHash != rotated.RotationHash ||
			records[1].PreviousRecordHash != records[0].RecordHash ||
			records[1].PreviousReservationHash != records[0].ReplacementReservationHash ||
			records[1].RecordHash != second.RotationHash {
			t.Fatalf("reservation rotation chain = %#v", records)
		}
	})
}

func TestManagerRotatePinnedReservationRejectsUnboundRequestAfterAdoption(
	t *testing.T,
) {
	ctx := context.Background()
	fixture := newPinnedCommitTestFixture(
		t,
		"pr-development/rotation-adopted-old",
	)
	baseTree := testGitObject(t, fixture.workspace.Path, "rev-parse", "HEAD^{tree}")
	if _, err := fixture.manager.AdoptPinnedLine(ctx, PinnedLineAdoptRequest{
		Pin:          fixture.pin,
		WorkspaceID:  fixture.workspace.ID,
		LineID:       pinnedLineTestID,
		ExpectedTree: baseTree,
	}); err != nil {
		t.Fatalf("AdoptPinnedLine() error = %v", err)
	}
	before, err := os.ReadFile(fixture.manager.statePath())
	if err != nil {
		t.Fatal(err)
	}
	_, rotateErr := fixture.manager.RotatePinnedReservation(
		ctx,
		unboundPinnedReservationRotationRequest(
			fixture,
			"pdrr_adopted_unbound_rejected",
			"pr-development/rotation-adopted-new",
		),
	)
	if rotateErr == nil || !errors.Is(rotateErr, ErrPinnedLineConflict) ||
		!strings.Contains(rotateErr.Error(), "bound to a development line") {
		t.Fatalf("unbound rotation after adoption error = %v", rotateErr)
	}
	after, err := os.ReadFile(fixture.manager.statePath())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("rejected unbound rotation changed the adopted-line inventory")
	}
}

func TestManagerRotatePinnedReservationCapacityFailsWithoutInventoryMutation(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedCommitTestFixture(t, "pr-development/rotation-capacity-old")
	tailReservation := fixture.pin.ReservationKey
	adversarialMutateInventory(t, fixture.manager, func(state *storeState) {
		workspace := state.Workspaces[fixture.workspace.ID]
		rotatedAt := workspace.CreatedAt.Add(time.Second)
		previousRecordHash := emptyPinnedReservationRotationDigest()
		rotations := make([]pinnedReservationRotationRecord, 0, maxPinnedReservationRotations)
		for ordinal := 0; ordinal < maxPinnedReservationRotations; ordinal++ {
			replacement := fmt.Sprintf("pr-development/rotation-capacity-%04x", ordinal)
			record := pinnedReservationRotationRecord{
				IntentID:                   fmt.Sprintf("pdrr_capacity_%04x", ordinal),
				WorkspaceID:                workspace.ID,
				RepoID:                     workspace.RepoID,
				SourceRef:                  workspace.PinnedSourceRef,
				SourceCommit:               workspace.PinnedCommit,
				PreviousReservationHash:    developmentLineReservationHash(tailReservation),
				ReplacementReservationHash: developmentLineReservationHash(replacement),
				AgentID:                    fixture.pin.AgentID,
				PreviousRecordHash:         previousRecordHash,
				RotatedAt:                  rotatedAt,
			}
			record.RecordHash = pinnedReservationRotationRecordDigest(record)
			rotations = append(rotations, record)
			previousRecordHash = record.RecordHash
			tailReservation = replacement
		}
		workspace.LockedBy.SessionKey = tailReservation
		workspace.LockedBy.LockedAt = rotatedAt
		workspace.LockedBy.HeartbeatAt = rotatedAt
		workspace.UpdatedAt = rotatedAt
		state.PinnedReservationRotations[workspace.ID] = rotations
		workspace.PinnedReservationRotationCount = len(rotations)
		workspace.PinnedReservationRotationTailHash = previousRecordHash
	})

	before, err := os.ReadFile(fixture.manager.statePath())
	if err != nil {
		t.Fatal(err)
	}
	request := unboundPinnedReservationRotationRequest(
		fixture,
		"pdrr_capacity_rejected",
		"pr-development/rotation-capacity-overflow",
	)
	request.Pin.ReservationKey = tailReservation
	_, rotateErr := fixture.manager.RotatePinnedReservation(ctx, request)
	if rotateErr == nil || !errors.Is(rotateErr, ErrPinnedLineConflict) ||
		!strings.Contains(rotateErr.Error(), "history is full") {
		t.Fatalf("capacity RotatePinnedReservation() error = %v", rotateErr)
	}
	after, err := os.ReadFile(fixture.manager.statePath())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("capacity rejection changed the inventory")
	}
	adversarialInspectInventory(t, fixture.manager, func(state *storeState) {
		workspace := state.Workspaces[fixture.workspace.ID]
		if len(state.PinnedReservationRotations[workspace.ID]) !=
			maxPinnedReservationRotations || workspace.LockedBy == nil ||
			workspace.LockedBy.SessionKey != tailReservation {
			t.Fatalf("capacity rejection changed state: workspace %#v rotations %d",
				workspace, len(state.PinnedReservationRotations[workspace.ID]))
		}
	})
}

func TestManagerRotatePinnedReservationBoundParksOnlyReplacement(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/rotation-bound-old")
	request := boundPinnedReservationRotationRequest(
		fixture,
		"pdrr_bound_first",
		"pr-development/rotation-bound-new",
	)
	rotated, err := fixture.manager.RotatePinnedReservation(ctx, request)
	if err != nil {
		t.Fatalf("RotatePinnedReservation() error = %v", err)
	}
	if !rotated.Bound || rotated.Version != fixture.lease.Version ||
		rotated.MutationEpoch != fixture.lease.MutationEpoch ||
		rotated.Tip != fixture.lease.Tip || rotated.Tree != fixture.lease.Tree ||
		!validLowerHex(rotated.RotationHash, 64) || rotated.AlreadyRotated {
		t.Fatalf("RotatePinnedReservation() = %#v", rotated)
	}
	middlePin := fixture.pin
	middlePin.ReservationKey = request.ReplacementReservationKey
	if heartbeat, heartbeatErr := fixture.manager.AcquirePinned(ctx, middlePin); heartbeatErr != nil ||
		heartbeat.ID != fixture.workspace.ID {
		t.Fatalf("replacement AcquirePinned() = %#v, %v", heartbeat, heartbeatErr)
	}
	if _, staleErr := fixture.manager.SnapshotPinnedCandidate(ctx, PinnedCandidateRequest{
		Pin:         fixture.pin,
		WorkspaceID: fixture.workspace.ID,
	}); staleErr == nil || !errors.Is(staleErr, ErrPinnedCommitConflict) {
		t.Fatalf("stale SnapshotPinnedCandidate() error = %v", staleErr)
	}
	secondRequest := request
	secondRequest.Pin = middlePin
	secondRequest.IntentID = "pdrr_bound_second"
	secondRequest.ReplacementReservationKey = "pr-development/rotation-bound-newer"
	second, err := fixture.manager.RotatePinnedReservation(ctx, secondRequest)
	if err != nil {
		t.Fatalf("second RotatePinnedReservation() error = %v", err)
	}
	if !second.Bound || second.RotationHash == rotated.RotationHash || second.AlreadyRotated {
		t.Fatalf("second RotatePinnedReservation() = %#v", second)
	}
	newPin := middlePin
	newPin.ReservationKey = secondRequest.ReplacementReservationKey

	parked, err := fixture.manager.ParkPinnedLine(ctx, PinnedLineParkRequest{
		Pin:             newPin,
		WorkspaceID:     fixture.workspace.ID,
		LineID:          pinnedLineTestID,
		IntentID:        "pdlnpark_rotation_bound_no_changes",
		ExpectedVersion: fixture.lease.Version,
		MutationEpoch:   fixture.lease.MutationEpoch,
		PreviousTip:     fixture.lease.Tip,
		Tip:             fixture.lease.Tip,
		Tree:            fixture.lease.Tree,
		NoChanges:       true,
	})
	if err != nil {
		t.Fatalf("ParkPinnedLine() error = %v", err)
	}
	if !parked.NoChanges || parked.Version != 1 {
		t.Fatalf("ParkPinnedLine() = %#v", parked)
	}
	adversarialInspectInventory(t, fixture.manager, func(state *storeState) {
		line := state.DevelopmentLines[pinnedLineTestID]
		if line == nil || len(line.RetiredReservationHashes) != 1 ||
			line.RetiredReservationHashes[0] !=
				developmentLineReservationHash(newPin.ReservationKey) ||
			line.RetiredReservationHashes[0] ==
				developmentLineReservationHash(fixture.pin.ReservationKey) ||
			line.RetiredReservationHashes[0] ==
				developmentLineReservationHash(middlePin.ReservationKey) ||
			len(state.PinnedReservationRotations[fixture.workspace.ID]) != 2 ||
			state.PinnedReservationRotations[fixture.workspace.ID][1].PreviousReservationHash !=
				state.PinnedReservationRotations[fixture.workspace.ID][0].ReplacementReservationHash {
			t.Fatalf("park/rotation histories = line %#v rotations %#v", line,
				state.PinnedReservationRotations[fixture.workspace.ID])
		}
	})
	if _, err := fixture.manager.RotatePinnedReservation(ctx, secondRequest); err == nil ||
		!errors.Is(err, ErrPinnedLineConflict) {
		t.Fatalf("post-park rotation replay error = %v", err)
	}
	parkedRequest := request
	parkedRequest.Pin = newPin
	parkedRequest.IntentID = "pdrr_bound_parked_before_resume"
	parkedRequest.ReplacementReservationKey = "pr-development/rotation-bound-after-park"
	if _, err := fixture.manager.RotatePinnedReservation(ctx, parkedRequest); err == nil ||
		!errors.Is(err, ErrPinnedLineConflict) {
		t.Fatalf("parked pre-resume RotatePinnedReservation() error = %v", err)
	}
}

func TestManagerRotatePinnedReservationRejectsPendingPark(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/rotation-pending-old")
	parkRequest := PinnedLineParkRequest{
		Pin:             fixture.pin,
		WorkspaceID:     fixture.workspace.ID,
		LineID:          pinnedLineTestID,
		IntentID:        "pdlnpark_rotation_pending",
		ExpectedVersion: fixture.lease.Version,
		MutationEpoch:   fixture.lease.MutationEpoch,
		PreviousTip:     fixture.lease.Tip,
		Tip:             fixture.lease.Tip,
		Tree:            fixture.lease.Tree,
		NoChanges:       true,
	}
	adversarialMutateInventory(t, fixture.manager, func(state *storeState) {
		installPendingPinnedLinePark(
			state.DevelopmentLines[pinnedLineTestID],
			parkRequest,
			developmentLineReservationHash(fixture.pin.ReservationKey),
			time.Date(2026, 8, 8, 9, 1, 0, 0, time.UTC),
		)
	})
	request := boundPinnedReservationRotationRequest(
		fixture,
		"pdrr_pending_rejected",
		"pr-development/rotation-pending-new",
	)
	if _, err := fixture.manager.RotatePinnedReservation(ctx, request); err == nil ||
		!errors.Is(err, ErrPinnedLineConflict) {
		t.Fatalf("pending RotatePinnedReservation() error = %v", err)
	}
}

func TestManagerPinnedReservationRotationChainCrossesAdoptionAndRepairEpisodes(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedCommitTestFixture(t, "pr-development/rotation-mixed-old")
	baseTree := testGitObject(t, fixture.workspace.Path, "rev-parse", "HEAD^{tree}")
	unboundRequest := unboundPinnedReservationRotationRequest(
		fixture,
		"pdrr_mixed_unbound",
		"pr-development/rotation-mixed-adopt",
	)
	if _, err := fixture.manager.RotatePinnedReservation(ctx, unboundRequest); err != nil {
		t.Fatal(err)
	}
	adoptPin := fixture.pin
	adoptPin.ReservationKey = unboundRequest.ReplacementReservationKey
	lease, err := fixture.manager.AdoptPinnedLine(ctx, PinnedLineAdoptRequest{
		Pin:          adoptPin,
		WorkspaceID:  fixture.workspace.ID,
		LineID:       pinnedLineTestID,
		ExpectedTree: baseTree,
	})
	if err != nil {
		t.Fatalf("AdoptPinnedLine() error = %v", err)
	}
	parked, err := fixture.manager.ParkPinnedLine(ctx, PinnedLineParkRequest{
		Pin:             adoptPin,
		WorkspaceID:     fixture.workspace.ID,
		LineID:          pinnedLineTestID,
		IntentID:        "pdlnpark_rotation_mixed",
		ExpectedVersion: lease.Version,
		MutationEpoch:   lease.MutationEpoch,
		PreviousTip:     lease.Tip,
		Tip:             lease.Tip,
		Tree:            lease.Tree,
		NoChanges:       true,
	})
	if err != nil {
		t.Fatalf("ParkPinnedLine() error = %v", err)
	}
	resumePin := fixture.pin
	resumePin.ReservationKey = "pr-development/rotation-mixed-resume"
	resumePin.AgentID = "repair-worker-mixed"
	resumed, err := fixture.manager.ResumePinnedLine(ctx, PinnedLineResumeRequest{
		Pin:             resumePin,
		WorkspaceID:     fixture.workspace.ID,
		LineID:          pinnedLineTestID,
		ExpectedVersion: parked.Version,
		ExpectedEpoch:   parked.MutationEpoch,
		ExpectedTip:     parked.Tip,
		ExpectedTree:    parked.Tree,
	})
	if err != nil {
		t.Fatalf("ResumePinnedLine() error = %v", err)
	}
	boundRequest := PinnedReservationRotationRequest{
		Pin:                       resumePin,
		WorkspaceID:               fixture.workspace.ID,
		IntentID:                  "pdrr_mixed_bound",
		ReplacementReservationKey: "pr-development/rotation-mixed-recovered",
		LineID:                    pinnedLineTestID,
		ExpectedVersion:           resumed.Version,
		ExpectedMutationEpoch:     resumed.MutationEpoch,
		ExpectedTip:               resumed.Tip,
		ExpectedTree:              resumed.Tree,
	}
	if _, err := fixture.manager.RotatePinnedReservation(ctx, boundRequest); err != nil {
		t.Fatalf("bound RotatePinnedReservation() after mixed history error = %v", err)
	}
	adversarialInspectInventory(t, fixture.manager, func(state *storeState) {
		records := state.PinnedReservationRotations[fixture.workspace.ID]
		if len(records) != 2 || records[0].LineID != "" ||
			records[1].LineID != pinnedLineTestID || records[1].MutationEpoch != 2 ||
			state.DevelopmentLines[pinnedLineTestID].RetiredReservationHashes[0] !=
				records[0].ReplacementReservationHash {
			t.Fatalf("mixed reservation rotation history = %#v", records)
		}
	})
}

func TestManagerRotatePinnedReservationWaitsForBothOperationLocks(t *testing.T) {
	for _, held := range []string{"old", "replacement"} {
		t.Run(held, func(t *testing.T) {
			ctx := context.Background()
			fixture := newPinnedCommitTestFixture(
				t,
				"pr-development/rotation-lock-"+held+"-old",
			)
			request := unboundPinnedReservationRotationRequest(
				fixture,
				"pdrr_lock_"+held,
				"pr-development/rotation-lock-"+held+"-new",
			)
			heldPin := fixture.pin
			if held == "replacement" {
				heldPin.ReservationKey = request.ReplacementReservationKey
			}
			entered := make(chan struct{})
			release := make(chan struct{})
			holderDone := make(chan error, 1)
			go func() {
				holderDone <- fixture.manager.WithPinnedOperation(
					ctx,
					heldPin,
					func(context.Context) error {
						close(entered)
						<-release
						return nil
					},
				)
			}()
			<-entered
			restarted, err := NewManager(fixture.manager.opts)
			if err != nil {
				t.Fatal(err)
			}
			rotationDone := make(chan error, 1)
			go func() {
				_, rotateErr := restarted.RotatePinnedReservation(ctx, request)
				rotationDone <- rotateErr
			}()
			select {
			case err := <-rotationDone:
				t.Fatalf("rotation did not wait for %s operation lock: %v", held, err)
			case <-time.After(100 * time.Millisecond):
			}
			close(release)
			if err := <-holderDone; err != nil {
				t.Fatalf("held operation error = %v", err)
			}
			select {
			case err := <-rotationDone:
				if err != nil {
					t.Fatalf("rotation after %s lock release error = %v", held, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("rotation deadlocked after %s lock release", held)
			}
		})
	}
}

func TestManagerPinnedReservationRotationInventoryRejectsTampering(t *testing.T) {
	fixture := newPinnedCommitTestFixture(t, "pr-development/rotation-tamper-old")
	firstRequest := unboundPinnedReservationRotationRequest(
		fixture,
		"pdrr_tamper_first",
		"pr-development/rotation-tamper-middle",
	)
	if _, err := fixture.manager.RotatePinnedReservation(context.Background(), firstRequest); err != nil {
		t.Fatal(err)
	}
	middlePin := fixture.pin
	middlePin.ReservationKey = firstRequest.ReplacementReservationKey
	secondRequest := PinnedReservationRotationRequest{
		Pin:                       middlePin,
		WorkspaceID:               fixture.workspace.ID,
		IntentID:                  "pdrr_tamper_second",
		ReplacementReservationKey: "pr-development/rotation-tamper-new",
	}
	if _, err := fixture.manager.RotatePinnedReservation(context.Background(), secondRequest); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*storeState)
	}{
		{
			name: "whole history deletion",
			mutate: func(state *storeState) {
				delete(state.PinnedReservationRotations, fixture.workspace.ID)
			},
		},
		{
			name: "latest history suffix deletion",
			mutate: func(state *storeState) {
				state.PinnedReservationRotations[fixture.workspace.ID] = state.PinnedReservationRotations[fixture.workspace.ID][:1]
			},
		},
		{
			name: "record hash",
			mutate: func(state *storeState) {
				state.PinnedReservationRotations[fixture.workspace.ID][0].RecordHash = strings.Repeat("a", 64)
			},
		},
		{
			name: "causal reservation",
			mutate: func(state *storeState) {
				records := state.PinnedReservationRotations[fixture.workspace.ID]
				records[1].PreviousReservationHash = developmentLineReservationHash(
					"pr-development/rotation-tamper-unrelated",
				)
				records[1].RecordHash = pinnedReservationRotationRecordDigest(records[1])
			},
		},
		{
			name: "global replacement reuse",
			mutate: func(state *storeState) {
				records := state.PinnedReservationRotations[fixture.workspace.ID]
				records[1].ReplacementReservationHash = records[0].ReplacementReservationHash
				records[1].RecordHash = pinnedReservationRotationRecordDigest(records[1])
			},
		},
		{
			name: "active tail",
			mutate: func(state *storeState) {
				state.Workspaces[fixture.workspace.ID].LockedBy.SessionKey = "pr-development/rotation-tamper-different-active"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := adversarialCloneInventory(t, fixture.manager)
			test.mutate(state)
			if err := validateDevelopmentLineInventory(state); err == nil {
				t.Fatal("validateDevelopmentLineInventory() tamper error = nil")
			}
			if err := adversarialSaveInventory(fixture.manager, state); err == nil {
				t.Fatal("saveLocked() tamper error = nil")
			}
		})
	}

	state := adversarialCloneInventory(t, fixture.manager)
	state.PinnedReservationRotations[fixture.workspace.ID][0].RecordHash = strings.Repeat("b", 64)
	writePinnedReservationRotationInventory(t, fixture.manager, state)
	if err := adversarialLoadInventory(fixture.manager); err == nil {
		t.Fatal("loadLocked() persisted tamper error = nil")
	}
}

func TestManagerPinnedReservationRotationMigratesVersionTwoAndRejectsRollback(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedCommitTestFixture(t, "pr-development/rotation-migration-old")
	versionTwo := adversarialCloneInventory(t, fixture.manager)
	versionTwo.Version = 2
	versionTwo.Workspaces[fixture.workspace.ID].PinnedReservationRotationCount = 0
	versionTwo.Workspaces[fixture.workspace.ID].PinnedReservationRotationTailHash = ""
	writePinnedReservationRotationInventory(t, fixture.manager, versionTwo)
	if _, err := fixture.manager.Stats(ctx); err != nil {
		t.Fatalf("load version-2 inventory error = %v", err)
	}
	request := unboundPinnedReservationRotationRequest(
		fixture,
		"pdrr_migration",
		"pr-development/rotation-migration-new",
	)
	if _, err := fixture.manager.RotatePinnedReservation(ctx, request); err != nil {
		t.Fatalf("rotate migrated version-2 inventory error = %v", err)
	}
	data, err := os.ReadFile(fixture.manager.statePath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"version": "4"`) {
		t.Fatalf("migrated inventory lacks version-4 fence: %s", data)
	}

	rollback := adversarialCloneInventory(t, fixture.manager)
	rollback.Version = 2
	writePinnedReservationRotationInventory(t, fixture.manager, rollback)
	if err := adversarialLoadInventory(fixture.manager); err == nil ||
		!strings.Contains(err.Error(), "rollback-fenced reservation rotations") {
		t.Fatalf("mislabeled version-2 rotation inventory error = %v", err)
	}
}

func TestManagerPinnedReservationRotationReplacementCannotBeReusedAfterRelease(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedCommitTestFixture(t, "pr-development/rotation-release-old")
	request := unboundPinnedReservationRotationRequest(
		fixture,
		"pdrr_release",
		"pr-development/rotation-release-new",
	)
	if _, err := fixture.manager.RotatePinnedReservation(ctx, request); err != nil {
		t.Fatal(err)
	}
	newPin := fixture.pin
	newPin.ReservationKey = request.ReplacementReservationKey
	if _, err := fixture.manager.ReleasePinned(ctx, PinnedReleaseRequest{
		ReservationKey: newPin.ReservationKey,
		AgentID:        newPin.AgentID,
	}); err != nil {
		t.Fatalf("ReleasePinned() error = %v", err)
	}
	if _, err := fixture.manager.AcquirePinned(ctx, newPin); err == nil ||
		!strings.Contains(err.Error(), "rotation") {
		t.Fatalf("released replacement AcquirePinned() error = %v", err)
	}
	reuse := request
	reuse.Pin = newPin
	reuse.IntentID = "pdrr_release_reuse"
	reuse.ReplacementReservationKey = fixture.pin.ReservationKey
	if _, err := fixture.manager.RotatePinnedReservation(ctx, reuse); err == nil ||
		!errors.Is(err, ErrPinnedLineConflict) {
		t.Fatalf("historical replacement reuse error = %v", err)
	}
}

func TestManagerReleasedRotationTailRejectsUnrelatedReservationOwners(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedCommitTestFixture(t, "pr-development/rotation-owner-old")
	request := unboundPinnedReservationRotationRequest(
		fixture,
		"pdrr_owner_release",
		"pr-development/rotation-owner-released",
	)
	if _, err := fixture.manager.RotatePinnedReservation(ctx, request); err != nil {
		t.Fatal(err)
	}
	replacementPin := fixture.pin
	replacementPin.ReservationKey = request.ReplacementReservationKey
	if _, err := fixture.manager.ReleasePinned(ctx, PinnedReleaseRequest{
		ReservationKey: replacementPin.ReservationKey,
		AgentID:        replacementPin.AgentID,
	}); err != nil {
		t.Fatal(err)
	}
	genericWorkspace, err := fixture.manager.Acquire(ctx, AcquireRequest{
		Repository: replacementPin.Repository,
		Ref:        replacementPin.SourceRef,
		SessionKey: replacementPin.ReservationKey,
		AgentID:    "generic-namespace-agent",
	})
	if err != nil {
		t.Fatalf("generic acquire with identical reservation bytes error = %v", err)
	}
	if genericWorkspace.ID == fixture.workspace.ID {
		t.Fatalf("generic acquire reused controller workspace %q", genericWorkspace.ID)
	}

	unrelatedPin := fixture.pin
	unrelatedPin.ReservationKey = "pr-development/rotation-owner-unrelated"
	unrelatedWorkspace, err := fixture.manager.AcquirePinned(ctx, unrelatedPin)
	if err != nil {
		t.Fatal(err)
	}
	unrelatedTree := testGitObject(t, unrelatedWorkspace.Path, "rev-parse", "HEAD^{tree}")
	unrelatedLease, err := fixture.manager.AdoptPinnedLine(ctx, PinnedLineAdoptRequest{
		Pin:          unrelatedPin,
		WorkspaceID:  unrelatedWorkspace.ID,
		LineID:       adversarialSecondLineID,
		ExpectedTree: unrelatedTree,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, parkErr := fixture.manager.ParkPinnedLine(ctx, PinnedLineParkRequest{
		Pin:             unrelatedPin,
		WorkspaceID:     unrelatedWorkspace.ID,
		LineID:          adversarialSecondLineID,
		IntentID:        "pdlnpark_rotation_owner_unrelated",
		ExpectedVersion: unrelatedLease.Version,
		MutationEpoch:   unrelatedLease.MutationEpoch,
		PreviousTip:     unrelatedLease.Tip,
		Tip:             unrelatedLease.Tip,
		Tree:            unrelatedLease.Tree,
		NoChanges:       true,
	}); parkErr != nil {
		t.Fatal(parkErr)
	}
	activePin := fixture.pin
	activePin.ReservationKey = "pr-development/rotation-owner-active"
	activeWorkspace, err := fixture.manager.AcquirePinned(ctx, activePin)
	if err != nil {
		t.Fatal(err)
	}
	baseline := adversarialCloneInventory(t, fixture.manager)
	if err := validateDevelopmentLineInventory(baseline); err != nil {
		t.Fatalf("baseline inventory error = %v", err)
	}
	releasedHash := developmentLineReservationHash(replacementPin.ReservationKey)

	t.Run("active on another pinned workspace", func(t *testing.T) {
		state := adversarialCloneState(t, baseline)
		state.Workspaces[activeWorkspace.ID].LockedBy.SessionKey = replacementPin.ReservationKey
		if err := validateDevelopmentLineInventory(state); err == nil ||
			!strings.Contains(err.Error(), "unrelated owner") {
			t.Fatalf("active unrelated replacement owner error = %v", err)
		}
	})

	t.Run("retired by another line", func(t *testing.T) {
		state := adversarialCloneState(t, baseline)
		line := state.DevelopmentLines[adversarialSecondLineID]
		line.RetiredReservationHashes[0] = releasedHash
		line.LastParkReservationHash = releasedHash
		if err := validateDevelopmentLineInventory(state); err == nil ||
			!strings.Contains(err.Error(), "unrelated owner") {
			t.Fatalf("retired unrelated replacement owner error = %v", err)
		}
	})
}

func unboundPinnedReservationRotationRequest(
	fixture pinnedCommitTestFixture,
	intentID, replacementReservation string,
) PinnedReservationRotationRequest {
	return PinnedReservationRotationRequest{
		Pin:                       fixture.pin,
		WorkspaceID:               fixture.workspace.ID,
		IntentID:                  intentID,
		ReplacementReservationKey: replacementReservation,
	}
}

func boundPinnedReservationRotationRequest(
	fixture pinnedLineTestFixture,
	intentID, replacementReservation string,
) PinnedReservationRotationRequest {
	return PinnedReservationRotationRequest{
		Pin:                       fixture.pin,
		WorkspaceID:               fixture.workspace.ID,
		IntentID:                  intentID,
		ReplacementReservationKey: replacementReservation,
		LineID:                    pinnedLineTestID,
		ExpectedVersion:           fixture.lease.Version,
		ExpectedMutationEpoch:     fixture.lease.MutationEpoch,
		ExpectedTip:               fixture.lease.Tip,
		ExpectedTree:              fixture.lease.Tree,
	}
}

func writePinnedReservationRotationInventory(
	t *testing.T,
	manager *Manager,
	state *storeState,
) {
	t.Helper()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager.statePath(), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
