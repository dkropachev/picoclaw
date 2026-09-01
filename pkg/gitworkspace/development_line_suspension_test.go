package gitworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDevelopmentLineSuspensionInventoryAcceptsAnchoredModes(t *testing.T) {
	for _, test := range []struct {
		name     string
		mode     string
		prepared bool
		applied  bool
	}{
		{name: "candidate", mode: developmentLineSuspensionCandidate},
		{
			name:     "commit recovery before apply",
			mode:     developmentLineSuspensionCommitRecovery,
			prepared: true,
		},
		{
			name:     "commit recovery after apply",
			mode:     developmentLineSuspensionCommitRecovery,
			prepared: true,
			applied:  true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPinnedLineTestFixture(
				t,
				"pr-development/suspension-mode-"+strings.ReplaceAll(test.name, " ", "-"),
			)
			state := adversarialCloneInventory(t, fixture.manager)
			installDevelopmentLineSuspensionForTest(
				t,
				state,
				pinnedLineTestID,
				fixture.pin.ReservationKey,
				test.mode,
				test.prepared,
				test.applied,
				"a",
			)
			if err := validateDevelopmentLineInventory(state); err != nil {
				t.Fatalf("validateDevelopmentLineInventory() error = %v", err)
			}
			line := state.DevelopmentLines[pinnedLineTestID]
			if line.State != developmentLineSuspended || line.SuspensionCount != 1 ||
				line.SuspensionTailHash != line.Suspensions[0].RecordHash {
				t.Fatalf("suspended line = %#v", line)
			}
		})
	}
}

func TestDevelopmentLineSuspensionInventoryRejectsCorruption(t *testing.T) {
	fixture := newPinnedLineTestFixture(t, "pr-development/suspension-corruption")
	valid := adversarialCloneInventory(t, fixture.manager)
	installDevelopmentLineSuspensionForTest(
		t,
		valid,
		pinnedLineTestID,
		fixture.pin.ReservationKey,
		developmentLineSuspensionCandidate,
		false,
		false,
		"a",
	)
	if err := validateDevelopmentLineInventory(valid); err != nil {
		t.Fatalf("valid suspension error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*storeState)
	}{
		{
			name: "anchor count",
			mutate: func(state *storeState) {
				state.DevelopmentLines[pinnedLineTestID].SuspensionCount++
			},
		},
		{
			name: "anchor tail",
			mutate: func(state *storeState) {
				state.DevelopmentLines[pinnedLineTestID].SuspensionTailHash = strings.Repeat("f", 64)
			},
		},
		{
			name: "truncated records",
			mutate: func(state *storeState) {
				state.DevelopmentLines[pinnedLineTestID].Suspensions = nil
			},
		},
		{
			name: "record hash",
			mutate: func(state *storeState) {
				state.DevelopmentLines[pinnedLineTestID].Suspensions[0].CandidateDigest = strings.Repeat("e", 64)
			},
		},
		{
			name: "unknown mode",
			mutate: func(state *storeState) {
				line := state.DevelopmentLines[pinnedLineTestID]
				line.Suspensions[0].Mode = "unknown"
				rehashDevelopmentLineSuspensionsForTest(line)
			},
		},
		{
			name: "candidate has prepared commit",
			mutate: func(state *storeState) {
				line := state.DevelopmentLines[pinnedLineTestID]
				line.Suspensions[0].PreparedCommit = strings.Repeat("c", len(line.SourceCommit))
				rehashDevelopmentLineSuspensionsForTest(line)
			},
		},
		{
			name: "commit recovery lacks prepared commit",
			mutate: func(state *storeState) {
				line := state.DevelopmentLines[pinnedLineTestID]
				line.Suspensions[0].Mode = developmentLineSuspensionCommitRecovery
				rehashDevelopmentLineSuspensionsForTest(line)
			},
		},
		{
			name: "prepared commit equals retained tip",
			mutate: func(state *storeState) {
				line := state.DevelopmentLines[pinnedLineTestID]
				line.Suspensions[0].Mode = developmentLineSuspensionCommitRecovery
				line.Suspensions[0].PreparedCommit = line.Tip
				line.Suspensions[0].PreparedTree = strings.Repeat("e", len(line.Tree))
				rehashDevelopmentLineSuspensionsForTest(line)
			},
		},
		{
			name: "prepared tree equals retained tree",
			mutate: func(state *storeState) {
				line := state.DevelopmentLines[pinnedLineTestID]
				line.Suspensions[0].Mode = developmentLineSuspensionCommitRecovery
				line.Suspensions[0].PreparedCommit = strings.Repeat("c", len(line.Tip))
				line.Suspensions[0].PreparedTree = line.Tree
				rehashDevelopmentLineSuspensionsForTest(line)
			},
		},
		{
			name: "applied without prepared commit",
			mutate: func(state *storeState) {
				line := state.DevelopmentLines[pinnedLineTestID]
				line.Suspensions[0].PreparedCommitApplied = true
				rehashDevelopmentLineSuspensionsForTest(line)
			},
		},
		{
			name: "oversized candidate",
			mutate: func(state *storeState) {
				line := state.DevelopmentLines[pinnedLineTestID]
				line.Suspensions[0].ChangedFileCount = maxPinnedCandidateChangedFiles + 1
				rehashDevelopmentLineSuspensionsForTest(line)
			},
		},
		{
			name: "source identity",
			mutate: func(state *storeState) {
				line := state.DevelopmentLines[pinnedLineTestID]
				line.Suspensions[0].SourceRef += "-changed"
				rehashDevelopmentLineSuspensionsForTest(line)
			},
		},
		{
			name: "current retained fence",
			mutate: func(state *storeState) {
				line := state.DevelopmentLines[pinnedLineTestID]
				line.Suspensions[0].Tip = strings.Repeat("d", len(line.Tip))
				rehashDevelopmentLineSuspensionsForTest(line)
			},
		},
		{
			name: "suspension time differs from current state",
			mutate: func(state *storeState) {
				line := state.DevelopmentLines[pinnedLineTestID]
				line.UpdatedAt = line.UpdatedAt.Add(time.Second)
			},
		},
		{
			name: "suspended workspace remains locked",
			mutate: func(state *storeState) {
				line := state.DevelopmentLines[pinnedLineTestID]
				state.Workspaces[line.WorkspaceID].LockedBy = &LockInfo{
					SessionKey:  "pr-development/suspension-corrupt-active",
					AgentID:     line.Suspensions[0].AgentID,
					LockedAt:    line.UpdatedAt,
					HeartbeatAt: line.UpdatedAt,
				}
			},
		},
		{
			name: "duplicate request",
			mutate: func(state *storeState) {
				line := state.DevelopmentLines[pinnedLineTestID]
				second := line.Suspensions[0]
				second.IntentID += "-second"
				second.RetiredReservationHash = developmentLineReservationHash(
					"pr-development/suspension-corrupt-second",
				)
				second.SuspendedAt = second.SuspendedAt.Add(time.Second)
				line.UpdatedAt = second.SuspendedAt
				line.Suspensions = append(line.Suspensions, second)
				rehashDevelopmentLineSuspensionsForTest(line)
			},
		},
		{
			name: "duplicate retired reservation",
			mutate: func(state *storeState) {
				line := state.DevelopmentLines[pinnedLineTestID]
				second := line.Suspensions[0]
				second.IntentID += "-second"
				second.RequestHash = strings.Repeat("b", 64)
				second.SuspendedAt = second.SuspendedAt.Add(time.Second)
				line.UpdatedAt = second.SuspendedAt
				line.Suspensions = append(line.Suspensions, second)
				rehashDevelopmentLineSuspensionsForTest(line)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			corrupt := adversarialCloneState(t, valid)
			test.mutate(corrupt)
			if err := validateDevelopmentLineInventory(corrupt); err == nil {
				t.Fatal("validateDevelopmentLineInventory() corruption error = nil")
			}
			if err := adversarialSaveInventory(fixture.manager, corrupt); err == nil {
				t.Fatal("saveLocked() corruption error = nil")
			}
		})
	}
}

func TestDevelopmentLineSuspensionReservationIsPermanentlyRetired(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/suspension-freshness")
	state := adversarialCloneInventory(t, fixture.manager)
	installDevelopmentLineSuspensionForTest(
		t,
		state,
		pinnedLineTestID,
		fixture.pin.ReservationKey,
		developmentLineSuspensionCandidate,
		false,
		false,
		"a",
	)
	line := state.DevelopmentLines[pinnedLineTestID]
	retiredHash := developmentLineReservationHash(fixture.pin.ReservationKey)
	if !developmentLineReservationRetired(line, retiredHash) {
		t.Fatal("suspended reservation is not retired")
	}
	if err := requireFreshPinnedLineReservation(
		state,
		line.ID,
		fixture.pin.ReservationKey,
	); err == nil || !errors.Is(err, ErrPinnedLineConflict) {
		t.Fatalf("requireFreshPinnedLineReservation() error = %v", err)
	}
	if err := requireFreshPinnedReservationRotation(state, retiredHash); err == nil ||
		!errors.Is(err, ErrPinnedLineConflict) {
		t.Fatalf("requireFreshPinnedReservationRotation() error = %v", err)
	}
	if err := adversarialSaveInventory(fixture.manager, state); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.AcquirePinned(ctx, fixture.pin); err == nil {
		t.Fatal("AcquirePinned() reused suspended reservation")
	}

	// A later owner causally consumes the suspension without erasing the retired
	// reservation hash.
	freshReservation := "pr-development/suspension-fresh-owner"
	resumedAt := line.UpdatedAt.Add(time.Second)
	rotations := state.PinnedReservationRotations[fixture.workspace.ID]
	previousRotationHash := emptyPinnedReservationRotationDigest()
	if len(rotations) > 0 {
		previousRotationHash = rotations[len(rotations)-1].RecordHash
	}
	rotation := pinnedReservationRotationRecord{
		IntentID:                   "pdrr_suspension_foundation_resume",
		WorkspaceID:                fixture.workspace.ID,
		LineID:                     line.ID,
		RepoID:                     line.RepoID,
		SourceRef:                  line.SourceRef,
		SourceCommit:               line.SourceCommit,
		Version:                    line.Version,
		MutationEpoch:              line.MutationEpoch,
		Tip:                        line.Tip,
		Tree:                       line.Tree,
		SuspensionHash:             line.SuspensionTailHash,
		PreviousReservationHash:    retiredHash,
		ReplacementReservationHash: developmentLineReservationHash(freshReservation),
		AgentID:                    fixture.pin.AgentID,
		PreviousRecordHash:         previousRotationHash,
		RotatedAt:                  resumedAt,
	}
	rotation.RecordHash = pinnedReservationRotationRecordDigest(rotation)
	rotations = append(rotations, rotation)
	state.PinnedReservationRotations[fixture.workspace.ID] = rotations
	line.State = developmentLineMutating
	line.MutationReservationHash = rotation.ReplacementReservationHash
	line.MutationAgentID = fixture.pin.AgentID
	line.UpdatedAt = resumedAt
	workspace := state.Workspaces[line.WorkspaceID]
	workspace.PinnedReservationRotationCount = len(rotations)
	workspace.PinnedReservationRotationTailHash = rotation.RecordHash
	workspace.LockedBy = &LockInfo{
		SessionKey:  freshReservation,
		AgentID:     fixture.pin.AgentID,
		LockedAt:    resumedAt,
		HeartbeatAt: resumedAt,
	}
	workspace.UpdatedAt = resumedAt
	workspace.LastWorkAt = resumedAt
	if err := validateDevelopmentLineInventory(state); err != nil {
		t.Fatalf("validate resumed inventory error = %v", err)
	}
	if !developmentLineReservationRetired(line, retiredHash) {
		t.Fatal("later owner erased suspended reservation retirement")
	}

	// A rotation cannot causally consume suspension evidence from its future,
	// even when every content hash and owner fence is recomputed consistently.
	rotationIndex := len(rotations) - 1
	rotations[rotationIndex].RotatedAt = line.Suspensions[0].SuspendedAt.Add(-time.Nanosecond)
	rotations[rotationIndex].RecordHash = pinnedReservationRotationRecordDigest(
		rotations[rotationIndex],
	)
	state.PinnedReservationRotations[fixture.workspace.ID] = rotations
	workspace.PinnedReservationRotationTailHash = rotations[rotationIndex].RecordHash
	if err := validateDevelopmentLineInventory(state); err == nil {
		t.Fatal("validateDevelopmentLineInventory() accepted pre-suspension resume")
	}
}

func TestDevelopmentLineSuspensionAcceptsRotationReplacementRetirement(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/suspension-rotation")
	replacement := "pr-development/suspension-rotation-replacement"
	_, err := fixture.manager.RotatePinnedReservation(ctx, PinnedReservationRotationRequest{
		Pin:                       fixture.pin,
		WorkspaceID:               fixture.workspace.ID,
		IntentID:                  "pdrr_suspension_foundation",
		ReplacementReservationKey: replacement,
		LineID:                    pinnedLineTestID,
		ExpectedVersion:           fixture.lease.Version,
		ExpectedMutationEpoch:     fixture.lease.MutationEpoch,
		ExpectedTip:               fixture.lease.Tip,
		ExpectedTree:              fixture.lease.Tree,
	})
	if err != nil {
		t.Fatal(err)
	}
	state := adversarialCloneInventory(t, fixture.manager)
	installDevelopmentLineSuspensionForTest(
		t,
		state,
		pinnedLineTestID,
		replacement,
		developmentLineSuspensionCandidate,
		false,
		false,
		"a",
	)
	if err := validateDevelopmentLineInventory(state); err != nil {
		t.Fatalf("rotation replacement suspension error = %v", err)
	}
}

func TestManagerDevelopmentLineSuspensionMigratesVersionThreeAndRejectsRollback(t *testing.T) {
	fixture := newPinnedLineTestFixture(t, "pr-development/suspension-migration")
	versionThree := adversarialCloneInventory(t, fixture.manager)
	versionThree.Version = 3
	line := versionThree.DevelopmentLines[pinnedLineTestID]
	line.SuspensionCount = 0
	line.SuspensionTailHash = ""
	line.Suspensions = nil
	legacy, err := json.Marshal(versionThree)
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := fixture.manager.decodeLegacyInventory(legacy)
	if err != nil {
		t.Fatalf("load version-3 inventory error = %v", err)
	}
	migratedLine := migrated.DevelopmentLines[pinnedLineTestID]
	if migrated.Version != stateVersion || migratedLine.SuspensionCount != 0 ||
		migratedLine.SuspensionTailHash != emptyDevelopmentLineSuspensionDigest() ||
		len(migratedLine.Suspensions) != 0 {
		t.Fatalf("migrated suspension anchor = %#v", migratedLine)
	}
	if _, statErr := os.Stat(fixture.manager.statePath()); !os.IsNotExist(statErr) {
		t.Fatalf("SQLite migration retained mutable inventory JSON: %v", statErr)
	}

	for _, test := range []struct {
		name   string
		mutate func(*developmentLineRecord)
	}{
		{
			name: "state",
			mutate: func(line *developmentLineRecord) {
				line.State = developmentLineSuspended
			},
		},
		{
			name: "anchor",
			mutate: func(line *developmentLineRecord) {
				line.SuspensionTailHash = emptyDevelopmentLineSuspensionDigest()
			},
		},
		{
			name: "record",
			mutate: func(line *developmentLineRecord) {
				line.Suspensions = []developmentLineSuspensionRecord{{Mode: "candidate"}}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			rollback := adversarialCloneState(t, versionThree)
			test.mutate(rollback.DevelopmentLines[pinnedLineTestID])
			legacy, marshalErr := json.Marshal(rollback)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if _, err := fixture.manager.decodeLegacyInventory(legacy); err == nil ||
				!strings.Contains(err.Error(), "pre-version-4") {
				t.Fatalf("mislabeled version-3 suspension error = %v", err)
			}
		})
	}
}

func installDevelopmentLineSuspensionForTest(
	t *testing.T,
	state *storeState,
	lineID, reservation, mode string,
	prepared, applied bool,
	hashDigit string,
) {
	t.Helper()
	if state.PinnedReservationRotations == nil {
		state.PinnedReservationRotations = make(
			map[string][]pinnedReservationRotationRecord,
		)
	}
	line := state.DevelopmentLines[lineID]
	if line == nil {
		t.Fatalf("line %q is missing", lineID)
	}
	workspace := state.Workspaces[line.WorkspaceID]
	if workspace == nil || workspace.LockedBy == nil ||
		workspace.LockedBy.SessionKey != reservation {
		t.Fatalf("line workspace is not owned by %q", reservation)
	}
	now := line.UpdatedAt.Add(time.Second)
	record := developmentLineSuspensionRecord{
		Mode:                   mode,
		IntentID:               "pdlnsuspend_foundation_" + hashDigit,
		RequestHash:            strings.Repeat(hashDigit, 64),
		WorkspaceID:            line.WorkspaceID,
		LineID:                 line.ID,
		RepoID:                 line.RepoID,
		SourceRef:              line.SourceRef,
		SourceCommit:           line.SourceCommit,
		Version:                line.Version,
		MutationEpoch:          line.MutationEpoch,
		Tip:                    line.Tip,
		Tree:                   line.Tree,
		RetiredReservationHash: developmentLineReservationHash(reservation),
		AgentID:                workspace.LockedBy.AgentID,
		CandidateTree:          line.Tree,
		CandidateDigest:        strings.Repeat("d", 64),
		ChangedFileCount:       0,
		PreparedCommitApplied:  applied,
		SuspendedAt:            now,
	}
	if prepared {
		record.PreparedCommit = strings.Repeat("c", len(line.SourceCommit))
		record.PreparedTree = strings.Repeat("e", len(line.SourceCommit))
	}
	line.Suspensions = append(line.Suspensions, record)
	line.State = developmentLineSuspended
	line.MutationReservationHash = ""
	line.MutationAgentID = ""
	line.UpdatedAt = now
	workspace.LockedBy = nil
	workspace.UpdatedAt = now
	rehashDevelopmentLineSuspensionsForTest(line)
}

func rehashDevelopmentLineSuspensionsForTest(line *developmentLineRecord) {
	previous := emptyDevelopmentLineSuspensionDigest()
	for index := range line.Suspensions {
		line.Suspensions[index].PreviousRecordHash = previous
		line.Suspensions[index].RecordHash = ""
		line.Suspensions[index].RecordHash = developmentLineSuspensionRecordDigest(
			line.Suspensions[index],
		)
		previous = line.Suspensions[index].RecordHash
	}
	line.SuspensionCount = len(line.Suspensions)
	line.SuspensionTailHash = previous
}
