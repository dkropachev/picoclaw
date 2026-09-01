package gitworkspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDevelopmentLineSuspensionRejectsCrossLineEvidenceReuse(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(
		t,
		"pr-development/suspension-adversarial-first",
	)
	firstRequest := suspensionAPITestSuspendRequest(
		fixture,
		"pdlnsuspend_adversarial_cross_line_first",
	)
	if _, err := fixture.manager.SuspendPinnedLine(ctx, firstRequest); err != nil {
		t.Fatalf("first SuspendPinnedLine() error = %v", err)
	}

	secondPin := fixture.pin
	secondPin.ReservationKey = "pr-development/suspension-adversarial-second"
	secondWorkspace, err := fixture.manager.AcquirePinned(ctx, secondPin)
	if err != nil {
		t.Fatalf("second AcquirePinned() error = %v", err)
	}
	secondTree := testGitObject(t, secondWorkspace.Path, "rev-parse", "HEAD^{tree}")
	secondLease, err := fixture.manager.AdoptPinnedLine(ctx, PinnedLineAdoptRequest{
		Pin:          secondPin,
		WorkspaceID:  secondWorkspace.ID,
		LineID:       adversarialSecondLineID,
		ExpectedTree: secondTree,
	})
	if err != nil {
		t.Fatalf("second AdoptPinnedLine() error = %v", err)
	}
	secondRequest := PinnedLineSuspendRequest{
		Pin:                   secondPin,
		WorkspaceID:           secondWorkspace.ID,
		LineID:                adversarialSecondLineID,
		IntentID:              firstRequest.IntentID,
		ExpectedVersion:       secondLease.Version,
		ExpectedMutationEpoch: secondLease.MutationEpoch,
		ExpectedTip:           secondLease.Tip,
		ExpectedTree:          secondLease.Tree,
	}
	beforeDuplicate := adversarialInventorySnapshot(t, fixture.manager)
	if _, duplicateErr := fixture.manager.SuspendPinnedLine(ctx, secondRequest); duplicateErr == nil ||
		!errors.Is(duplicateErr, ErrPinnedLineConflict) ||
		!strings.Contains(duplicateErr.Error(), "intent was already used") {
		t.Fatalf("cross-line duplicate intent error = %v", duplicateErr)
	}
	afterDuplicate := adversarialInventorySnapshot(t, fixture.manager)
	if string(afterDuplicate) != string(beforeDuplicate) {
		t.Fatal("rejected cross-line duplicate intent changed the inventory")
	}

	secondRequest.IntentID = "pdlnsuspend_adversarial_cross_line_second"
	if _, err := fixture.manager.SuspendPinnedLine(ctx, secondRequest); err != nil {
		t.Fatalf("second SuspendPinnedLine() error = %v", err)
	}
	valid := adversarialCloneInventory(t, fixture.manager)
	if valid.PinnedReservationRotations == nil {
		valid.PinnedReservationRotations = make(
			map[string][]pinnedReservationRotationRecord,
		)
	}
	if err := validateDevelopmentLineInventory(valid); err != nil {
		t.Fatalf("two-line suspension baseline error = %v", err)
	}
	first := valid.DevelopmentLines[pinnedLineTestID].Suspensions[0]

	for _, test := range []struct {
		name      string
		wantError string
		mutate    func(*storeState)
		validate  func(*storeState) error
	}{
		{
			name:      "intent",
			wantError: "suspension intent was reused",
			mutate: func(state *storeState) {
				line := state.DevelopmentLines[adversarialSecondLineID]
				line.Suspensions[0].IntentID = first.IntentID
				rehashDevelopmentLineSuspensionsForTest(line)
			},
			validate: validateDevelopmentLineInventory,
		},
		{
			name:      "request",
			wantError: "suspension request was reused",
			mutate: func(state *storeState) {
				line := state.DevelopmentLines[adversarialSecondLineID]
				line.Suspensions[0].RequestHash = first.RequestHash
				rehashDevelopmentLineSuspensionsForTest(line)
			},
			validate: validateDevelopmentLineInventory,
		},
		{
			name:      "record hash",
			wantError: "suspension hash was reused",
			mutate: func(state *storeState) {
				line := state.DevelopmentLines[adversarialSecondLineID]
				line.Suspensions[0].RecordHash = first.RecordHash
				line.SuspensionTailHash = first.RecordHash
			},
			// The global rotation validator owns cross-line record-hash
			// uniqueness. Calling it directly isolates that invariant from
			// each line's earlier self-hash validation.
			validate: validatePinnedReservationRotationInventory,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			corrupt := adversarialCloneState(t, valid)
			if corrupt.PinnedReservationRotations == nil {
				corrupt.PinnedReservationRotations = make(
					map[string][]pinnedReservationRotationRecord,
				)
			}
			test.mutate(corrupt)
			err := test.validate(corrupt)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("cross-line %s reuse error = %v", test.name, err)
			}
		})
	}
}

func TestDevelopmentLineSuspensionRejectsForgedRotationLinks(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(
		t,
		"pr-development/suspension-adversarial-link-old",
	)
	suspended, err := fixture.manager.SuspendPinnedLine(
		ctx,
		suspensionAPITestSuspendRequest(
			fixture,
			"pdlnsuspend_adversarial_link",
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	freshReservation := "pr-development/suspension-adversarial-link-fresh"
	if _, err := fixture.manager.ResumeSuspendedPinnedLine(
		ctx,
		suspensionAPITestResumeRequest(
			fixture,
			suspended,
			freshReservation,
			"pdlnresume_adversarial_link",
		),
	); err != nil {
		t.Fatal(err)
	}
	valid := adversarialCloneInventory(t, fixture.manager)
	if err := validateDevelopmentLineInventory(valid); err != nil {
		t.Fatalf("linked rotation baseline error = %v", err)
	}

	for _, test := range []struct {
		name      string
		wantError string
		mutate    func(*pinnedReservationRotationRecord)
	}{
		{
			name:      "agent fence",
			wantError: "suspension fence changed",
			mutate: func(record *pinnedReservationRotationRecord) {
				record.AgentID = "forged-resume-agent"
			},
		},
		{
			name:      "missing suspension fence",
			wantError: "missing its fence",
			mutate: func(record *pinnedReservationRotationRecord) {
				record.SuspensionHash = ""
			},
		},
		{
			name:      "unknown suspension fence",
			wantError: "rotation suspension is missing",
			mutate: func(record *pinnedReservationRotationRecord) {
				record.SuspensionHash = strings.Repeat("f", 64)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			corrupt := adversarialCloneState(t, valid)
			workspace := corrupt.Workspaces[fixture.workspace.ID]
			rotations := corrupt.PinnedReservationRotations[workspace.ID]
			test.mutate(&rotations[0])
			rotations[0].RecordHash = pinnedReservationRotationRecordDigest(rotations[0])
			corrupt.PinnedReservationRotations[workspace.ID] = rotations
			workspace.PinnedReservationRotationTailHash = rotations[0].RecordHash
			err := validateDevelopmentLineInventory(corrupt)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("forged %s error = %v", test.name, err)
			}
		})
	}

	t.Run("duplicate consumption", func(t *testing.T) {
		corrupt := adversarialCloneState(t, valid)
		line := corrupt.DevelopmentLines[pinnedLineTestID]
		workspace := corrupt.Workspaces[fixture.workspace.ID]
		rotations := corrupt.PinnedReservationRotations[workspace.ID]
		first := rotations[0]
		secondReservation := "pr-development/suspension-adversarial-link-newer"
		second := first
		second.IntentID = "pdrr_adversarial_duplicate_suspension_consumer"
		// A second consumer must repeat the suspension's retired bearer.
		// Global stale-bearer uniqueness rejects that duplicate before the
		// redundant suspension-consumer map needs to report it again.
		second.PreviousReservationHash = first.PreviousReservationHash
		second.ReplacementReservationHash = developmentLineReservationHash(secondReservation)
		second.PreviousRecordHash = first.RecordHash
		second.RecordHash = pinnedReservationRotationRecordDigest(second)
		rotations = append(rotations, second)
		corrupt.PinnedReservationRotations[workspace.ID] = rotations
		workspace.PinnedReservationRotationCount = len(rotations)
		workspace.PinnedReservationRotationTailHash = second.RecordHash
		workspace.LockedBy.SessionKey = secondReservation
		line.MutationReservationHash = second.ReplacementReservationHash
		err := validateDevelopmentLineInventory(corrupt)
		if err == nil || !strings.Contains(err.Error(), "stale reservation was rotated more than once") {
			t.Fatalf("duplicate suspension consumption error = %v", err)
		}
	})
}

func TestManagerSuspendPinnedLineCommitRecoveryReplayIsPayloadExact(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(
		t,
		"pr-development/suspension-adversarial-commit",
	)
	path := filepath.Join(fixture.workspace.Path, "repair.txt")
	if err := os.WriteFile(path, []byte("review repair\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	candidate := fixture.snapshot(t)
	request := PinnedLineCommitSuspensionRequest{
		Suspend: suspensionAPITestSuspendRequest(
			fixture,
			"pdlnsuspend_adversarial_commit_replay",
		),
		Commit: fixture.commitRequest(candidate, "Prepare review repair"),
	}
	headBefore := testGitCommit(t, fixture.workspace.Path, "HEAD")
	indexBefore := testGitObject(t, fixture.workspace.Path, "write-tree")
	statusBefore := suspensionAPITestStatus(t, fixture.workspace.Path)
	first, err := fixture.manager.SuspendPinnedLineCommitRecovery(ctx, request)
	if err != nil {
		t.Fatalf("SuspendPinnedLineCommitRecovery() error = %v", err)
	}
	if first.PreparedCommit == "" || first.PreparedTree != candidate.Tree ||
		first.PreparedCommitApplied || first.AlreadySuspended {
		t.Fatalf("first commit suspension = %#v", first)
	}

	restarted, err := NewManager(fixture.manager.opts)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := restarted.SuspendPinnedLineCommitRecovery(ctx, request)
	if err != nil {
		t.Fatalf("exact commit suspension replay error = %v", err)
	}
	if !replayed.AlreadySuspended || replayed.SuspensionHash != first.SuspensionHash ||
		replayed.PreparedCommit != first.PreparedCommit ||
		replayed.PreparedTree != first.PreparedTree {
		t.Fatalf("commit suspension replay = %#v, first = %#v", replayed, first)
	}

	beforeMismatch := adversarialInventorySnapshot(t, restarted)
	mismatched := request
	mismatched.Commit.Message = "Prepare a different review repair"
	result, mismatchErr := restarted.SuspendPinnedLineCommitRecovery(ctx, mismatched)
	if mismatchErr == nil || !errors.Is(mismatchErr, ErrPinnedLineConflict) ||
		result != (PinnedLineSuspendResult{}) {
		t.Fatalf("mismatched commit suspension replay = %#v, %v", result, mismatchErr)
	}
	afterMismatch := adversarialInventorySnapshot(t, restarted)
	if string(afterMismatch) != string(beforeMismatch) {
		t.Fatal("mismatched commit suspension replay changed the inventory")
	}
	suspensionAPITestAssertGitState(
		t,
		fixture.workspace.Path,
		headBefore,
		indexBefore,
		statusBefore,
	)
}

func TestManagerSuspendPinnedLineReservesResumeRotationCapacity(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedCommitTestFixture(
		t,
		"pr-development/suspension-adversarial-capacity-old",
	)
	tailReservation := fixture.pin.ReservationKey
	adversarialMutateInventory(t, fixture.manager, func(state *storeState) {
		tailReservation = installSuspensionAdversarialRotationCapacity(
			state,
			fixture.workspace.ID,
			tailReservation,
			fixture.pin.AgentID,
		)
	})

	tailPin := fixture.pin
	tailPin.ReservationKey = tailReservation
	baseTree := testGitObject(t, fixture.workspace.Path, "rev-parse", "HEAD^{tree}")
	lease, err := fixture.manager.AdoptPinnedLine(ctx, PinnedLineAdoptRequest{
		Pin:          tailPin,
		WorkspaceID:  fixture.workspace.ID,
		LineID:       pinnedLineTestID,
		ExpectedTree: baseTree,
	})
	if err != nil {
		t.Fatalf("AdoptPinnedLine() at rotation capacity error = %v", err)
	}
	before := adversarialInventorySnapshot(t, fixture.manager)
	_, err = fixture.manager.SuspendPinnedLine(ctx, PinnedLineSuspendRequest{
		Pin:                   tailPin,
		WorkspaceID:           fixture.workspace.ID,
		LineID:                pinnedLineTestID,
		IntentID:              "pdlnsuspend_adversarial_capacity_rejected",
		ExpectedVersion:       lease.Version,
		ExpectedMutationEpoch: lease.MutationEpoch,
		ExpectedTip:           lease.Tip,
		ExpectedTree:          lease.Tree,
	})
	if err == nil || !errors.Is(err, ErrPinnedLineConflict) ||
		!strings.Contains(err.Error(), "has no resume capacity") {
		t.Fatalf("SuspendPinnedLine() at rotation capacity error = %v", err)
	}
	after := adversarialInventorySnapshot(t, fixture.manager)
	if string(after) != string(before) {
		t.Fatal("capacity-rejected suspension changed the inventory")
	}
}

func TestManagerSuspendAndResumePinnedLineSHA256(t *testing.T) {
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "sha256-suspension-source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(ctx, source, "init", "--object-format=sha256", "-b", "main"); err != nil {
		t.Skipf("Git does not support SHA-256 repositories: %v", err)
	}
	seedSourceRepo(t, source)
	expected := testGitCommit(t, source, "HEAD")
	if len(expected) != 64 {
		t.Fatalf("SHA-256 source commit length = %d, want 64", len(expected))
	}
	now := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &now)
	pin := PinnedAcquireRequest{
		Repository:     source,
		SourceRef:      "main",
		ExpectedCommit: expected,
		ReservationKey: "pr-development/suspension-adversarial-sha256-old",
		AgentID:        "sha256-repair-worker",
	}
	workspace, err := manager.AcquirePinned(ctx, pin)
	if err != nil {
		t.Fatalf("AcquirePinned() SHA-256 error = %v", err)
	}
	baseTree := testGitObject(t, workspace.Path, "rev-parse", "HEAD^{tree}")
	lease, err := manager.AdoptPinnedLine(ctx, PinnedLineAdoptRequest{
		Pin:          pin,
		WorkspaceID:  workspace.ID,
		LineID:       pinnedLineTestID,
		ExpectedTree: baseTree,
	})
	if err != nil {
		t.Fatalf("AdoptPinnedLine() SHA-256 error = %v", err)
	}
	if writeErr := os.WriteFile(
		filepath.Join(workspace.Path, "sha256-repair.txt"),
		[]byte("content-addressed repair\n"),
		0o644,
	); writeErr != nil {
		t.Fatal(writeErr)
	}
	candidate, err := manager.SnapshotPinnedCandidate(ctx, PinnedCandidateRequest{
		Pin:         pin,
		WorkspaceID: workspace.ID,
	})
	if err != nil {
		t.Fatalf("SnapshotPinnedCandidate() SHA-256 error = %v", err)
	}
	suspended, err := manager.SuspendPinnedLine(ctx, PinnedLineSuspendRequest{
		Pin:                   pin,
		WorkspaceID:           workspace.ID,
		LineID:                pinnedLineTestID,
		IntentID:              "pdlnsuspend_adversarial_sha256",
		ExpectedVersion:       lease.Version,
		ExpectedMutationEpoch: lease.MutationEpoch,
		ExpectedTip:           lease.Tip,
		ExpectedTree:          lease.Tree,
	})
	if err != nil {
		t.Fatalf("SuspendPinnedLine() SHA-256 error = %v", err)
	}
	if len(suspended.Tip) != 64 || len(suspended.Tree) != 64 ||
		len(suspended.CandidateTree) != 64 ||
		suspended.CandidateTree != candidate.Tree ||
		suspended.CandidateDigest != candidate.CandidateDigest {
		t.Fatalf("SHA-256 suspension = %#v", suspended)
	}
	freshPin := pin
	freshPin.ReservationKey = "pr-development/suspension-adversarial-sha256-fresh"
	resumed, err := manager.ResumeSuspendedPinnedLine(ctx, PinnedLineSuspendedResumeRequest{
		Pin:                   freshPin,
		WorkspaceID:           workspace.ID,
		LineID:                pinnedLineTestID,
		IntentID:              "pdlnresume_adversarial_sha256",
		ExpectedVersion:       suspended.Version,
		ExpectedMutationEpoch: suspended.MutationEpoch,
		ExpectedTip:           suspended.Tip,
		ExpectedTree:          suspended.Tree,
		SuspensionHash:        suspended.SuspensionHash,
		CandidateTree:         suspended.CandidateTree,
		CandidateDigest:       suspended.CandidateDigest,
		ChangedFileCount:      suspended.ChangedFileCount,
	})
	if err != nil {
		t.Fatalf("ResumeSuspendedPinnedLine() SHA-256 error = %v", err)
	}
	if resumed.CandidateTree != candidate.Tree || resumed.RotationHash == "" ||
		len(resumed.Tip) != 64 || len(resumed.Tree) != 64 {
		t.Fatalf("SHA-256 suspension resume = %#v", resumed)
	}
}

func installSuspensionAdversarialRotationCapacity(
	state *storeState,
	workspaceID, initialReservation, agentID string,
) string {
	workspace := state.Workspaces[workspaceID]
	effectiveAt := workspace.CreatedAt.Add(time.Second)
	previousBearer := initialReservation
	previousRecord := emptyPinnedReservationRotationDigest()
	records := make([]pinnedReservationRotationRecord, maxPinnedReservationRotations)
	for ordinal := range records {
		nextBearer := fmt.Sprintf(
			"pr-development/suspension-adversarial-capacity-%04x",
			ordinal,
		)
		record := &records[ordinal]
		record.IntentID = fmt.Sprintf(
			"pdrr_suspension_adversarial_capacity_%04x",
			ordinal,
		)
		record.WorkspaceID = workspace.ID
		record.RepoID = workspace.RepoID
		record.SourceRef = workspace.PinnedSourceRef
		record.SourceCommit = workspace.PinnedCommit
		record.PreviousReservationHash = developmentLineReservationHash(previousBearer)
		record.ReplacementReservationHash = developmentLineReservationHash(nextBearer)
		record.AgentID = agentID
		record.PreviousRecordHash = previousRecord
		record.RotatedAt = effectiveAt
		record.RecordHash = pinnedReservationRotationRecordDigest(*record)
		previousBearer = nextBearer
		previousRecord = record.RecordHash
	}

	workspace.LockedBy = &LockInfo{
		SessionKey:  previousBearer,
		AgentID:     agentID,
		LockedAt:    effectiveAt,
		HeartbeatAt: effectiveAt,
	}
	workspace.UpdatedAt = effectiveAt
	state.PinnedReservationRotations[workspaceID] = records
	workspace.PinnedReservationRotationCount = len(records)
	workspace.PinnedReservationRotationTailHash = previousRecord
	return previousBearer
}
