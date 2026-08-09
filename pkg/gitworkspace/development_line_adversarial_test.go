package gitworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const adversarialSecondLineID = "pdln_fedcba9876543210fedcba9876543210"

func TestManagerPinnedDevelopmentLinePendingParkSealsMutationAndRecoversRefAhead(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/adversarial-pending-1")
	firstCommit, firstPark := adversarialParkOneChange(
		t,
		&fixture,
		"first.txt",
		"first repair\n",
		"pdcmt_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"pdlnpark_adversarial_pending_first",
	)

	secondPin := fixture.pin
	secondPin.ReservationKey = "pr-development/adversarial-pending-2"
	lease, err := fixture.manager.ResumePinnedLine(ctx, PinnedLineResumeRequest{
		Pin:             secondPin,
		WorkspaceID:     fixture.workspace.ID,
		LineID:          pinnedLineTestID,
		ExpectedVersion: firstPark.Version,
		ExpectedEpoch:   firstPark.MutationEpoch,
		ExpectedTip:     firstPark.Tip,
		ExpectedTree:    firstPark.Tree,
	})
	if err != nil {
		t.Fatalf("ResumePinnedLine() error = %v", err)
	}
	fixture.pin = secondPin
	if writeErr := os.WriteFile(
		filepath.Join(fixture.workspace.Path, "second.txt"),
		[]byte("second repair\n"),
		0o644,
	); writeErr != nil {
		t.Fatal(writeErr)
	}
	candidate := fixture.snapshot(t)
	commitRequest := fixture.commitRequest(candidate, "Apply second repair")
	commitRequest.IntentID = "pdcmt_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	commitRequest.AuthoredAt = time.Date(2026, 8, 9, 15, 0, 0, 0, time.UTC)
	secondCommit, err := fixture.manager.CommitPinned(ctx, commitRequest)
	if err != nil {
		t.Fatalf("CommitPinned() error = %v", err)
	}
	parkRequest := PinnedLineParkRequest{
		Pin:             secondPin,
		WorkspaceID:     fixture.workspace.ID,
		LineID:          pinnedLineTestID,
		IntentID:        "pdlnpark_adversarial_pending_second",
		ExpectedVersion: firstPark.Version,
		MutationEpoch:   lease.MutationEpoch,
		PreviousTip:     firstCommit.Commit,
		Tip:             secondCommit.Commit,
		Tree:            secondCommit.Tree,
	}

	adversarialMutateInventory(t, fixture.manager, func(state *storeState) {
		line := state.DevelopmentLines[pinnedLineTestID]
		if line == nil {
			t.Fatal("development line is missing")
		}
		installPendingPinnedLinePark(
			line,
			parkRequest,
			developmentLineReservationHash(secondPin.ReservationKey),
			time.Date(2026, 8, 9, 15, 1, 0, 0, time.UTC),
		)
	})
	if _, updateErr := runGit(
		ctx,
		fixture.workspace.Path,
		"-c",
		"core.logAllRefUpdates=false",
		"update-ref",
		"refs/heads/"+developmentLineBranch(pinnedLineTestID),
		secondCommit.Commit,
		firstCommit.Commit,
	); updateErr != nil {
		t.Fatalf("advance retained ref for crash state: %v", updateErr)
	}

	if _, acquireErr := fixture.manager.AcquirePinned(ctx, secondPin); acquireErr == nil ||
		!strings.Contains(acquireErr.Error(), "pending") {
		t.Fatalf("AcquirePinned() pending error = %v", acquireErr)
	}
	callbackCalled := false
	err = fixture.manager.WithPinnedOperation(ctx, secondPin, func(context.Context) error {
		callbackCalled = true
		return nil
	})
	if err == nil || !errors.Is(err, ErrPinnedLineConflict) || callbackCalled {
		t.Fatalf("WithPinnedOperation() pending result = called %v, error %v", callbackCalled, err)
	}
	thirdPin := secondPin
	thirdPin.ReservationKey = "pr-development/adversarial-pending-3"
	_, err = fixture.manager.ResumePinnedLine(ctx, PinnedLineResumeRequest{
		Pin:             thirdPin,
		WorkspaceID:     fixture.workspace.ID,
		LineID:          pinnedLineTestID,
		ExpectedVersion: firstPark.Version,
		ExpectedEpoch:   firstPark.MutationEpoch,
		ExpectedTip:     firstPark.Tip,
		ExpectedTree:    firstPark.Tree,
	})
	if err == nil || !errors.Is(err, ErrPinnedLineConflict) ||
		!strings.Contains(err.Error(), "pending") {
		t.Fatalf("ResumePinnedLine() pending error = %v", err)
	}
	_, err = fixture.manager.SnapshotPinnedCandidate(ctx, PinnedCandidateRequest{
		Pin:         secondPin,
		WorkspaceID: fixture.workspace.ID,
	})
	if err == nil || !errors.Is(err, ErrPinnedLineConflict) ||
		!strings.Contains(err.Error(), "pending") {
		t.Fatalf("SnapshotPinnedCandidate() pending error = %v", err)
	}
	_, err = fixture.manager.CommitPinned(ctx, commitRequest)
	if err == nil || !errors.Is(err, ErrPinnedLineConflict) ||
		!strings.Contains(err.Error(), "pending") {
		t.Fatalf("CommitPinned() pending error = %v", err)
	}

	changedIntent := parkRequest
	changedIntent.IntentID = "pdlnpark_adversarial_pending_changed"
	if _, changedErr := fixture.manager.ParkPinnedLine(ctx, changedIntent); changedErr == nil ||
		!errors.Is(changedErr, ErrPinnedLineConflict) ||
		!strings.Contains(changedErr.Error(), "pending park intent changed") {
		t.Fatalf("changed ParkPinnedLine() error = %v", changedErr)
	}
	recovered, err := fixture.manager.ParkPinnedLine(ctx, parkRequest)
	if err != nil {
		t.Fatalf("exact ParkPinnedLine() recovery error = %v", err)
	}
	if recovered.Version != firstPark.Version+1 || recovered.Tip != secondCommit.Commit ||
		recovered.AlreadyParked || !recovered.WorkspaceClean {
		t.Fatalf("exact ParkPinnedLine() recovery = %#v", recovered)
	}
	adversarialInspectInventory(t, fixture.manager, func(state *storeState) {
		line := state.DevelopmentLines[pinnedLineTestID]
		if line == nil || line.PendingParkSet || line.State != developmentLineParked ||
			line.Tip != secondCommit.Commit {
			t.Fatalf("recovered development line = %#v", line)
		}
	})
}

func TestManagerPinnedDevelopmentLineResumeRejectsReservationOwnedByPinnedCheckout(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/adversarial-collision-1")
	_, parked := adversarialParkOneChange(
		t,
		&fixture,
		"repair.txt",
		"repair\n",
		"pdcmt_cccccccccccccccccccccccccccccccc",
		"pdlnpark_adversarial_collision",
	)

	collidingPin := fixture.pin
	collidingPin.ReservationKey = "pr-development/adversarial-collision-2"
	unrelated, err := fixture.manager.AcquirePinned(ctx, collidingPin)
	if err != nil {
		t.Fatalf("unrelated AcquirePinned() error = %v", err)
	}
	if unrelated.ID == fixture.workspace.ID {
		t.Fatalf("unrelated pinned checkout reused line workspace %q", unrelated.ID)
	}
	_, err = fixture.manager.ResumePinnedLine(ctx, PinnedLineResumeRequest{
		Pin:             collidingPin,
		WorkspaceID:     fixture.workspace.ID,
		LineID:          pinnedLineTestID,
		ExpectedVersion: parked.Version,
		ExpectedEpoch:   parked.MutationEpoch,
		ExpectedTip:     parked.Tip,
		ExpectedTree:    parked.Tree,
	})
	if err == nil || !errors.Is(err, ErrPinnedLineConflict) ||
		!strings.Contains(err.Error(), "already owns a pinned workspace") {
		t.Fatalf("ResumePinnedLine() collision error = %v", err)
	}
	adversarialInspectInventory(t, fixture.manager, func(state *storeState) {
		line := state.DevelopmentLines[pinnedLineTestID]
		if line == nil || line.State != developmentLineParked || line.PendingParkSet {
			t.Fatalf("development line after rejected resume = %#v", line)
		}
		workspace := state.Workspaces[unrelated.ID]
		if workspace == nil || workspace.LockedBy == nil ||
			workspace.LockedBy.SessionKey != collidingPin.ReservationKey {
			t.Fatalf("unrelated pinned workspace after rejected resume = %#v", workspace)
		}
	})
}

func TestInspectDevelopmentLineRefIgnoresPackedNestedOnlyRef(t *testing.T) {
	ctx := context.Background()
	repository := initSourceRepo(t)
	branch := developmentLineBranch(pinnedLineTestID)
	nestedRef := "refs/heads/" + branch + "/nested"
	if _, err := runGit(ctx, repository, "update-ref", nestedRef, "HEAD"); err != nil {
		t.Fatalf("create nested retained ref: %v", err)
	}
	if _, err := runGit(ctx, repository, "pack-refs", "--all", "--prune"); err != nil {
		t.Fatalf("pack nested retained ref: %v", err)
	}
	packed, err := os.ReadFile(filepath.Join(repository, ".git", "packed-refs"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(packed), nestedRef) {
		t.Fatalf("packed-refs does not contain nested ref %q", nestedRef)
	}
	commit, found, err := inspectDevelopmentLineRef(ctx, repository, branch, nil)
	if err != nil {
		t.Fatalf("inspectDevelopmentLineRef() error = %v", err)
	}
	if found || commit != "" {
		t.Fatalf("inspectDevelopmentLineRef() = %q, %v; want exact ref absent", commit, found)
	}
}

func TestAdvanceDevelopmentLineRefDoesNotAccumulateReflog(t *testing.T) {
	ctx := context.Background()
	repository := initSourceRepo(t)
	now := time.Date(2026, 8, 9, 18, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &now)
	environment, cleanup, err := manager.newPinnedGitEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	workspace := &WorkspaceRecord{Path: repository}
	branch := developmentLineBranch(pinnedLineTestID)
	previous := testGitCommit(t, repository, "HEAD")
	if _, initialErr := manager.advanceDevelopmentLineRef(
		ctx,
		workspace,
		branch,
		previous,
		previous,
		true,
		environment,
	); initialErr != nil {
		t.Fatalf("create retained ref: %v", initialErr)
	}
	tree := testGitObject(t, repository, "rev-parse", previous+"^{tree}")
	for index := 0; index < 64; index++ {
		tip, commitErr := runGit(
			ctx,
			repository,
			"commit-tree",
			tree,
			"-p",
			previous,
			"-m",
			fmt.Sprintf("retained line step %d", index),
		)
		if commitErr != nil {
			t.Fatal(commitErr)
		}
		tip = strings.TrimSpace(tip)
		if _, advanceErr := manager.advanceDevelopmentLineRef(
			ctx,
			workspace,
			branch,
			previous,
			tip,
			false,
			environment,
		); advanceErr != nil {
			t.Fatalf("advance retained ref %d: %v", index, advanceErr)
		}
		previous = tip
	}
	reflog := adversarialDevelopmentLineMetadataLeaf(repository, branch, true)
	if _, statErr := os.Lstat(reflog); !os.IsNotExist(statErr) {
		t.Fatalf("retained ref reflog stat error = %v, want absent", statErr)
	}
	current, found, err := inspectDevelopmentLineRef(ctx, repository, branch, environment)
	if err != nil || !found || current != previous {
		t.Fatalf("retained ref = %q, %v, %v; want %q", current, found, err, previous)
	}
}

func TestDevelopmentLineInventoryRejectsCorruptRelationsAndReservationReuse(t *testing.T) {
	fixture := newPinnedLineTestFixture(t, "pr-development/adversarial-inventory-1")
	_, _ = adversarialParkOneChange(
		t,
		&fixture,
		"repair.txt",
		"repair\n",
		"pdcmt_dddddddddddddddddddddddddddddddd",
		"pdlnpark_adversarial_inventory",
	)
	valid := adversarialCloneInventory(t, fixture.manager)

	tests := []struct {
		name   string
		mutate func(*storeState)
	}{
		{
			name: "parked epoch differs from version",
			mutate: func(state *storeState) {
				state.DevelopmentLines[pinnedLineTestID].MutationEpoch++
			},
		},
		{
			name: "partial pinned workspace identity",
			mutate: func(state *storeState) {
				line := state.DevelopmentLines[pinnedLineTestID]
				state.Workspaces[line.WorkspaceID].PinnedCommit = ""
			},
		},
		{
			name: "workspace map key differs from record",
			mutate: func(state *storeState) {
				line := state.DevelopmentLines[pinnedLineTestID]
				workspace := state.Workspaces[line.WorkspaceID]
				delete(state.Workspaces, line.WorkspaceID)
				state.Workspaces[line.WorkspaceID+"-tampered"] = workspace
			},
		},
		{
			name: "version zero has expected-version residue",
			mutate: func(state *storeState) {
				line := state.DevelopmentLines[pinnedLineTestID]
				line.Version = 0
				line.MutationEpoch = 1
				line.State = developmentLineMutating
				line.LastParkIntentID = ""
				line.LastParkReservationHash = ""
				line.LastParkAgentID = ""
				line.LastParkEpoch = 0
				line.LastParkExpectedVersion = 1
				line.LastParkPreviousTip = ""
				line.LastParkTip = ""
				line.LastParkTree = ""
				line.RetiredReservationHashes = nil
				reservation := "pr-development/adversarial-max-residue"
				line.MutationReservationHash = developmentLineReservationHash(reservation)
				line.MutationAgentID = "main"
				workspace := state.Workspaces[line.WorkspaceID]
				workspace.LockedBy = &LockInfo{
					SessionKey:  reservation,
					AgentID:     "main",
					LockedAt:    line.UpdatedAt,
					HeartbeatAt: line.UpdatedAt,
				}
			},
		},
		{
			name: "mutating maximum version cannot park",
			mutate: func(state *storeState) {
				line := state.DevelopmentLines[pinnedLineTestID]
				line.Version = maxDevelopmentLineReservations
				line.MutationEpoch = maxDevelopmentLineReservations + 1
				line.State = developmentLineMutating
				reservation := "pr-development/adversarial-max-version"
				line.MutationReservationHash = developmentLineReservationHash(reservation)
				line.MutationAgentID = "main"
				line.RetiredReservationHashes = make(
					[]string,
					maxDevelopmentLineReservations,
				)
				for index := range line.RetiredReservationHashes {
					line.RetiredReservationHashes[index] = adversarialReservationHash(index)
				}
				line.LastParkEpoch = maxDevelopmentLineReservations
				line.LastParkExpectedVersion = maxDevelopmentLineReservations - 1
				line.LastParkReservationHash = line.RetiredReservationHashes[len(line.RetiredReservationHashes)-1]
				workspace := state.Workspaces[line.WorkspaceID]
				workspace.LockedBy = &LockInfo{
					SessionKey:  reservation,
					AgentID:     "main",
					LockedAt:    line.UpdatedAt,
					HeartbeatAt: line.UpdatedAt,
				}
			},
		},
		{
			name: "retired reservation repeated",
			mutate: func(state *storeState) {
				line := state.DevelopmentLines[pinnedLineTestID]
				line.Version = 2
				line.MutationEpoch = 2
				line.LastParkEpoch = 2
				line.LastParkExpectedVersion = 1
				line.RetiredReservationHashes = append(
					line.RetiredReservationHashes,
					line.RetiredReservationHashes[0],
				)
			},
		},
		{
			name: "current reservation was retired",
			mutate: func(state *storeState) {
				line := state.DevelopmentLines[pinnedLineTestID]
				workspace := state.Workspaces[line.WorkspaceID]
				line.State = developmentLineMutating
				line.MutationEpoch = line.Version + 1
				line.MutationReservationHash = line.RetiredReservationHashes[0]
				line.MutationAgentID = "main"
				workspace.LockedBy = &LockInfo{
					SessionKey:  "pr-development/adversarial-inventory-1",
					AgentID:     "main",
					LockedAt:    line.UpdatedAt,
					HeartbeatAt: line.UpdatedAt,
				}
			},
		},
		{
			name: "retired reservation belongs to two lines",
			mutate: func(state *storeState) {
				original := state.DevelopmentLines[pinnedLineTestID]
				originalWorkspace := state.Workspaces[original.WorkspaceID]
				other := *original
				other.ID = adversarialSecondLineID
				other.WorkspaceID = original.WorkspaceID + "-second"
				other.Branch = developmentLineBranch(other.ID)
				otherWorkspace := *originalWorkspace
				otherWorkspace.ID = other.WorkspaceID
				otherWorkspace.DevelopmentLineID = other.ID
				otherWorkspace.LockedBy = nil
				state.DevelopmentLines[other.ID] = &other
				state.Workspaces[otherWorkspace.ID] = &otherWorkspace
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			corrupt := adversarialCloneState(t, valid)
			test.mutate(corrupt)
			if err := validateDevelopmentLineInventory(corrupt); err == nil {
				t.Fatal("validateDevelopmentLineInventory() error = nil")
			}
			if err := adversarialSaveInventory(fixture.manager, corrupt); err == nil {
				t.Fatal("saveLocked() corrupt inventory error = nil")
			}
		})
	}

	corruptOnDisk := adversarialCloneState(t, valid)
	corruptOnDisk.DevelopmentLines[pinnedLineTestID].MutationEpoch++
	data, err := json.MarshalIndent(corruptOnDisk, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.manager.statePath(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := adversarialLoadInventory(fixture.manager); err == nil {
		t.Fatal("loadLocked() corrupt inventory error = nil")
	}
}

func TestManagerPinnedDevelopmentLineRejectsRegularSymbolicRetainedRef(t *testing.T) {
	t.Run("park", func(t *testing.T) {
		ctx := context.Background()
		fixture := newPinnedLineTestFixture(
			t,
			"pr-development/adversarial-symbolic-ref-park",
		)
		commit := fixture.commitChange(
			t,
			"symbolic.txt",
			"must review\n",
			"pdcmt_abababababababababababababababab",
			"Add symbolic-ref park subject",
			time.Date(2026, 8, 10, 15, 0, 0, 0, time.UTC),
		)
		attackerRef := "refs/heads/attacker-park"
		if _, updateErr := runGit(
			ctx,
			fixture.workspace.Path,
			"update-ref",
			attackerRef,
			fixture.pin.ExpectedCommit,
		); updateErr != nil {
			t.Fatal(updateErr)
		}
		retainedRef := "refs/heads/" + developmentLineBranch(pinnedLineTestID)
		if _, symbolicErr := runGit(
			ctx,
			fixture.workspace.Path,
			"symbolic-ref",
			retainedRef,
			attackerRef,
		); symbolicErr != nil {
			t.Fatal(symbolicErr)
		}
		_, parkErr := fixture.manager.ParkPinnedLine(ctx, PinnedLineParkRequest{
			Pin:             fixture.pin,
			WorkspaceID:     fixture.workspace.ID,
			LineID:          pinnedLineTestID,
			IntentID:        "pdlnpark_adversarial_symbolic_ref",
			ExpectedVersion: 0,
			MutationEpoch:   1,
			PreviousTip:     fixture.pin.ExpectedCommit,
			Tip:             commit.Commit,
			Tree:            commit.Tree,
		})
		if parkErr == nil || !errors.Is(parkErr, ErrPinnedLineConflict) {
			t.Fatalf("ParkPinnedLine() symbolic ref error = %v", parkErr)
		}
		attackerTip := testGitCommit(t, fixture.workspace.Path, attackerRef)
		if attackerTip != fixture.pin.ExpectedCommit {
			t.Fatalf("attacker ref moved to %s, want %s", attackerTip, fixture.pin.ExpectedCommit)
		}
	})

	t.Run("review", func(t *testing.T) {
		ctx := context.Background()
		fixture := newPinnedLineTestFixture(
			t,
			"pr-development/adversarial-symbolic-ref-review",
		)
		_, parked := adversarialParkOneChange(
			t,
			&fixture,
			"review.txt",
			"review me\n",
			"pdcmt_cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd",
			"pdlnpark_adversarial_symbolic_review",
		)
		attackerRef := "refs/heads/attacker-review"
		if _, updateErr := runGit(
			ctx,
			fixture.workspace.Path,
			"update-ref",
			attackerRef,
			parked.Tip,
		); updateErr != nil {
			t.Fatal(updateErr)
		}
		if _, symbolicErr := runGit(
			ctx,
			fixture.workspace.Path,
			"symbolic-ref",
			"refs/heads/"+developmentLineBranch(pinnedLineTestID),
			attackerRef,
		); symbolicErr != nil {
			t.Fatal(symbolicErr)
		}
		_, reviewErr := fixture.manager.SnapshotPinnedLineReview(
			ctx,
			PinnedLineReviewRequest{
				LineID:          pinnedLineTestID,
				ExpectedVersion: parked.Version,
				ExpectedBase:    parked.PreviousTip,
				ExpectedTip:     parked.Tip,
				ExpectedTree:    parked.Tree,
			},
		)
		if reviewErr == nil || !errors.Is(reviewErr, ErrPinnedLineConflict) {
			t.Fatalf("SnapshotPinnedLineReview() symbolic ref error = %v", reviewErr)
		}
	})
}

func TestManagerLegacyPinnedWorkspaceUpgradeFailsClosedOrPurgesDroppedState(t *testing.T) {
	t.Run("live requires old binary cleanup", func(t *testing.T) {
		fixture := newPinnedCommitTestFixture(
			t,
			"pr-development/adversarial-legacy-live",
		)
		state := adversarialCloneInventory(t, fixture.manager)
		oldID := adversarialMakePinnedWorkspaceLegacy(t, state, fixture.workspace.ID)
		adversarialWriteLegacyInventory(t, fixture.manager, state)
		_, loadErr := fixture.manager.Stats(context.Background())
		if !errors.Is(loadErr, errLegacyPinnedWorkspaceMigration) ||
			loadErr.Error() != errLegacyPinnedWorkspaceMigration.Error() {
			t.Fatalf("Stats() legacy live error = %v", loadErr)
		}
		if strings.Contains(loadErr.Error(), oldID) ||
			strings.Contains(loadErr.Error(), fixture.workspace.Path) {
			t.Fatalf("legacy migration error exposed private state: %q", loadErr)
		}
	})

	t.Run("dropped tombstone is purged", func(t *testing.T) {
		ctx := context.Background()
		fixture := newPinnedCommitTestFixture(
			t,
			"pr-development/adversarial-legacy-dropped",
		)
		state := adversarialCloneInventory(t, fixture.manager)
		oldID := adversarialMakePinnedWorkspaceLegacy(t, state, fixture.workspace.ID)
		workspace := state.Workspaces[oldID]
		droppedAt := time.Date(2026, 8, 10, 17, 0, 0, 0, time.UTC)
		workspace.DroppedAt = &droppedAt
		workspace.LockedBy = nil
		state.History = append(state.History, HistoryEntry{
			ID:          "legacy-private-history",
			Time:        droppedAt,
			Action:      "preserve_failed",
			RepoID:      workspace.RepoID,
			WorkspaceID: oldID,
			Detail:      "private",
		})
		if removeErr := os.RemoveAll(workspace.Path); removeErr != nil {
			t.Fatal(removeErr)
		}
		adversarialWriteLegacyInventory(t, fixture.manager, state)
		result, reconcileErr := fixture.manager.Reconcile(ctx)
		if reconcileErr != nil {
			t.Fatalf("Reconcile() dropped legacy state error = %v", reconcileErr)
		}
		if result.Stats.WorkspaceCount != 0 || len(result.Stats.History) != 0 {
			t.Fatalf("Reconcile() exposed dropped legacy state: %#v", result.Stats)
		}
		adversarialInspectInventory(t, fixture.manager, func(migrated *storeState) {
			if migrated.Version != stateVersion || len(migrated.Workspaces) != 0 ||
				len(migrated.History) != 0 || len(migrated.DevelopmentLineHistory) == 0 {
				t.Fatalf("migrated dropped legacy state = %#v", migrated)
			}
		})
	})

	t.Run("corrupt dropped state is not laundered", func(t *testing.T) {
		fixture := newPinnedCommitTestFixture(
			t,
			"pr-development/adversarial-legacy-corrupt",
		)
		state := adversarialCloneInventory(t, fixture.manager)
		oldID := adversarialMakePinnedWorkspaceLegacy(t, state, fixture.workspace.ID)
		workspace := state.Workspaces[oldID]
		droppedAt := time.Date(2026, 8, 10, 18, 0, 0, 0, time.UTC)
		workspace.DroppedAt = &droppedAt
		workspace.LockedBy = nil
		if removeErr := os.RemoveAll(workspace.Path); removeErr != nil {
			t.Fatal(removeErr)
		}
		workspace.PinnedCommit = ""
		workspace.Path = filepath.Join(t.TempDir(), "unrelated-private-path")
		adversarialWriteLegacyInventory(t, fixture.manager, state)
		_, loadErr := fixture.manager.Stats(context.Background())
		if !errors.Is(loadErr, errLegacyPinnedWorkspaceMigration) ||
			loadErr.Error() != errLegacyPinnedWorkspaceMigration.Error() {
			t.Fatalf("Stats() corrupt legacy error = %v", loadErr)
		}
	})
}

func TestManagerInventoryRejectsDroppedControllerPinnedWorkspace(t *testing.T) {
	fixture := newPinnedCommitTestFixture(
		t,
		"pr-development/adversarial-dropped-pinned",
	)
	state := adversarialCloneInventory(t, fixture.manager)
	workspace := state.Workspaces[fixture.workspace.ID]
	droppedAt := time.Date(2026, 8, 10, 19, 0, 0, 0, time.UTC)
	workspace.DroppedAt = &droppedAt
	if err := validateDevelopmentLineInventory(state); err == nil {
		t.Fatal("validateDevelopmentLineInventory() dropped pin error = nil")
	}
	if err := adversarialSaveInventory(fixture.manager, state); err == nil {
		t.Fatal("saveLocked() dropped pin error = nil")
	}
}

func TestManagerPinnedDevelopmentLineRejectsUnsafeRetainedRefLayout(t *testing.T) {
	tests := []struct {
		name    string
		logLeaf bool
		symlink bool
	}{
		{name: "ref lock"},
		{name: "reflog lock", logLeaf: true},
		{name: "ref symlink", symlink: true},
		{name: "reflog symlink", logLeaf: true, symlink: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPinnedLineTestFixture(
				t,
				"pr-development/adversarial-layout-"+strings.ReplaceAll(test.name, " ", "-"),
			)
			_, parked := adversarialParkOneChange(
				t,
				&fixture,
				"repair.txt",
				"repair\n",
				"pdcmt_eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
				"pdlnpark_adversarial_layout_"+strings.ReplaceAll(test.name, " ", "_"),
			)
			leaf := adversarialDevelopmentLineMetadataLeaf(
				fixture.workspace.Path,
				developmentLineBranch(pinnedLineTestID),
				test.logLeaf,
			)
			if test.logLeaf {
				if err := os.MkdirAll(filepath.Dir(leaf), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if test.symlink {
				realLeaf := leaf + ".real"
				if test.logLeaf {
					if err := os.WriteFile(realLeaf, []byte("unexpected reflog\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Rename(leaf, realLeaf); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Base(realLeaf), leaf); err != nil {
					if os.IsPermission(err) {
						t.Skipf("symlinks are unavailable: %v", err)
					}
					t.Fatal(err)
				}
			} else if err := os.WriteFile(leaf+".lock", []byte("stale\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			_, err := fixture.manager.SnapshotPinnedLineReview(
				context.Background(),
				PinnedLineReviewRequest{
					LineID:          pinnedLineTestID,
					ExpectedVersion: parked.Version,
					ExpectedBase:    parked.PreviousTip,
					ExpectedTip:     parked.Tip,
					ExpectedTree:    parked.Tree,
				},
			)
			if err == nil || !errors.Is(err, ErrPinnedLineConflict) {
				t.Fatalf("SnapshotPinnedLineReview() unsafe layout error = %v", err)
			}
		})
	}
}

func adversarialParkOneChange(
	t *testing.T,
	fixture *pinnedLineTestFixture,
	path, content, commitIntent, parkIntent string,
) (PinnedCommitResult, PinnedLineParkResult) {
	t.Helper()
	commit := fixture.commitChange(
		t,
		path,
		content,
		commitIntent,
		"Apply adversarial repair",
		time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC),
	)
	parked, err := fixture.manager.ParkPinnedLine(
		context.Background(),
		PinnedLineParkRequest{
			Pin:             fixture.pin,
			WorkspaceID:     fixture.workspace.ID,
			LineID:          pinnedLineTestID,
			IntentID:        parkIntent,
			ExpectedVersion: fixture.lease.Version,
			MutationEpoch:   fixture.lease.MutationEpoch,
			PreviousTip:     fixture.lease.Tip,
			Tip:             commit.Commit,
			Tree:            commit.Tree,
		},
	)
	if err != nil {
		t.Fatalf("ParkPinnedLine() error = %v", err)
	}
	return commit, parked
}

func adversarialMutateInventory(
	t *testing.T,
	manager *Manager,
	mutate func(*storeState),
) {
	t.Helper()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	unlock, err := manager.lockInventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	state, err := manager.loadLocked()
	if err != nil {
		t.Fatal(err)
	}
	mutate(state)
	if err := manager.saveLocked(state); err != nil {
		t.Fatal(err)
	}
}

func adversarialInspectInventory(
	t *testing.T,
	manager *Manager,
	inspect func(*storeState),
) {
	t.Helper()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	unlock, err := manager.lockInventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	state, err := manager.loadLocked()
	if err != nil {
		t.Fatal(err)
	}
	inspect(state)
}

func adversarialCloneInventory(t *testing.T, manager *Manager) *storeState {
	t.Helper()
	var cloned *storeState
	adversarialInspectInventory(t, manager, func(state *storeState) {
		cloned = adversarialCloneState(t, state)
	})
	return cloned
}

func adversarialCloneState(t *testing.T, state *storeState) *storeState {
	t.Helper()
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var cloned storeState
	if err := json.Unmarshal(data, &cloned); err != nil {
		t.Fatal(err)
	}
	return &cloned
}

func adversarialSaveInventory(manager *Manager, state *storeState) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	unlock, err := manager.lockInventory(context.Background())
	if err != nil {
		return err
	}
	defer unlock()
	return manager.saveLocked(state)
}

func adversarialLoadInventory(manager *Manager) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	unlock, err := manager.lockInventory(context.Background())
	if err != nil {
		return err
	}
	defer unlock()
	_, err = manager.loadLocked()
	return err
}

func adversarialMakePinnedWorkspaceLegacy(
	t *testing.T,
	state *storeState,
	workspaceID string,
) string {
	t.Helper()
	workspace := state.Workspaces[workspaceID]
	if workspace == nil {
		t.Fatalf("workspace %q is missing", workspaceID)
	}
	oldID := workspace.RepoID
	if _, exists := state.Workspaces[oldID]; exists && oldID != workspaceID {
		t.Fatalf("legacy workspace ID %q is occupied", oldID)
	}
	delete(state.Workspaces, workspaceID)
	oldPath := filepath.Join(
		filepath.Dir(workspace.Path),
		safePathName(workspace.RemoteURL)+"-"+oldID,
	)
	if renameErr := os.Rename(workspace.Path, oldPath); renameErr != nil {
		t.Fatal(renameErr)
	}
	workspace.ID = oldID
	workspace.Path = oldPath
	workspace.PinnedReservationRotationCount = 0
	workspace.PinnedReservationRotationTailHash = ""
	state.Workspaces[oldID] = workspace
	if repository := state.Repositories[workspace.RepoID]; repository != nil {
		for index, candidate := range repository.WorkspaceIDs {
			if candidate == workspaceID {
				repository.WorkspaceIDs[index] = oldID
			}
		}
	}
	state.History = append(state.History, state.DevelopmentLineHistory...)
	state.DevelopmentLineHistory = nil
	for index := range state.History {
		if state.History[index].WorkspaceID == workspaceID {
			state.History[index].WorkspaceID = oldID
		}
	}
	return oldID
}

func adversarialWriteLegacyInventory(t *testing.T, manager *Manager, state *storeState) {
	t.Helper()
	type legacyStoreState storeState
	legacy := legacyStoreState(*state)
	legacy.Version = 1
	data, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager.statePath(), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func adversarialDevelopmentLineMetadataLeaf(
	workspacePath, branch string,
	logLeaf bool,
) string {
	root := filepath.Join(workspacePath, ".git", "refs", "heads")
	if logLeaf {
		root = filepath.Join(workspacePath, ".git", "logs", "refs", "heads")
	}
	return filepath.Join(root, filepath.FromSlash(branch))
}

func adversarialReservationHash(index int) string {
	return developmentLineReservationHash(
		fmt.Sprintf("pr-development/adversarial-retired-%d", index),
	)
}
