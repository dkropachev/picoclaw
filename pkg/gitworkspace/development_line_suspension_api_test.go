package gitworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPinnedLineSuspensionAPIJSONIsPrivate(t *testing.T) {
	t.Parallel()

	privateValues := []struct {
		name  string
		value any
	}{
		{
			name: "suspend request",
			value: PinnedLineSuspendRequest{
				Pin:             PinnedAcquireRequest{ReservationKey: "private-old"},
				WorkspaceID:     "private-workspace",
				LineID:          "private-line",
				IntentID:        "private-suspend-intent",
				ExpectedVersion: 1,
			},
		},
		{
			name: "commit suspension request",
			value: PinnedLineCommitSuspensionRequest{
				Suspend: PinnedLineSuspendRequest{IntentID: "private-suspend-intent"},
				Commit:  PinnedCommitRequest{Message: "private commit message"},
			},
		},
		{
			name: "resume request",
			value: PinnedLineSuspendedResumeRequest{
				Pin:            PinnedAcquireRequest{ReservationKey: "private-fresh"},
				WorkspaceID:    "private-workspace",
				LineID:         "private-line",
				IntentID:       "private-resume-intent",
				SuspensionHash: strings.Repeat("a", 64),
			},
		},
		{
			name: "suspend result",
			value: PinnedLineSuspendResult{
				WorkspaceID:      "private-workspace",
				SuspensionHash:   strings.Repeat("b", 64),
				PreparedCommit:   strings.Repeat("c", 40),
				AlreadySuspended: true,
			},
		},
		{
			name: "resume result",
			value: PinnedLineSuspendedResumeResult{
				WorkspaceID:    "private-workspace",
				SuspensionHash: strings.Repeat("d", 64),
				RotationHash:   strings.Repeat("e", 64),
				AlreadyResumed: true,
			},
		},
	}

	for _, test := range privateValues {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			data, err := json.Marshal(test.value)
			if err != nil || string(data) != "{}" {
				t.Fatalf("json.Marshal() = %q, %v; want {}", data, err)
			}
		})
	}
}

func TestManagerSuspendAndResumePinnedLineRetainsCandidateAcrossRestart(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/suspension-api-old")
	path := filepath.Join(fixture.workspace.Path, "retained.txt")
	if err := os.WriteFile(path, []byte("retained repair\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	candidate := fixture.snapshot(t)
	headBefore := testGitCommit(t, fixture.workspace.Path, "HEAD")
	indexBefore := testGitObject(t, fixture.workspace.Path, "write-tree")
	statusBefore := suspensionAPITestStatus(t, fixture.workspace.Path)

	suspendRequest := suspensionAPITestSuspendRequest(
		fixture,
		"pdlnsuspend_api_candidate",
	)
	suspended, err := fixture.manager.SuspendPinnedLine(nil, suspendRequest)
	if err != nil {
		t.Fatalf("SuspendPinnedLine() error = %v", err)
	}
	if suspended.WorkspaceID != fixture.workspace.ID ||
		suspended.Version != fixture.lease.Version ||
		suspended.MutationEpoch != fixture.lease.MutationEpoch ||
		suspended.Tip != fixture.lease.Tip || suspended.Tree != fixture.lease.Tree ||
		suspended.CandidateTree != candidate.Tree ||
		suspended.CandidateDigest != candidate.CandidateDigest ||
		suspended.ChangedFileCount != candidate.ChangedFiles ||
		suspended.SuspensionHash == "" || suspended.PreparedCommit != "" ||
		suspended.PreparedTree != "" || suspended.PreparedCommitApplied ||
		suspended.AlreadySuspended {
		t.Fatalf("SuspendPinnedLine() = %#v", suspended)
	}
	suspensionAPITestAssertGitState(
		t,
		fixture.workspace.Path,
		headBefore,
		indexBefore,
		statusBefore,
	)
	if workspace := workspaceRecordForTest(
		t,
		fixture.manager,
		fixture.workspace.ID,
	); workspace.LockedBy != nil {
		t.Fatalf("suspended workspace remains reserved: %#v", workspace.LockedBy)
	}

	restarted, err := NewManager(fixture.manager.opts)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := restarted.SuspendPinnedLine(ctx, suspendRequest)
	if err != nil {
		t.Fatalf("replayed SuspendPinnedLine() error = %v", err)
	}
	if !replayed.AlreadySuspended || replayed.SuspensionHash != suspended.SuspensionHash ||
		replayed.CandidateTree != suspended.CandidateTree ||
		replayed.CandidateDigest != suspended.CandidateDigest {
		t.Fatalf("replayed suspension = %#v, first = %#v", replayed, suspended)
	}
	if _, snapshotErr := restarted.SnapshotPinnedCandidate(ctx, PinnedCandidateRequest{
		Pin:         fixture.pin,
		WorkspaceID: fixture.workspace.ID,
	}); snapshotErr == nil || (!errors.Is(snapshotErr, ErrPinnedLineConflict) &&
		!errors.Is(snapshotErr, ErrPinnedCommitConflict)) {
		t.Fatalf("retired reservation SnapshotPinnedCandidate() error = %v", snapshotErr)
	}

	resumeRequest := suspensionAPITestResumeRequest(
		fixture,
		suspended,
		"pr-development/suspension-api-fresh",
		"pdlnresume_api_candidate",
	)
	resumed, err := restarted.ResumeSuspendedPinnedLine(ctx, resumeRequest)
	if err != nil {
		t.Fatalf("ResumeSuspendedPinnedLine() error = %v", err)
	}
	if resumed.WorkspaceID != suspended.WorkspaceID ||
		resumed.Version != suspended.Version ||
		resumed.MutationEpoch != suspended.MutationEpoch ||
		resumed.Tip != suspended.Tip || resumed.Tree != suspended.Tree ||
		resumed.CandidateTree != suspended.CandidateTree ||
		resumed.CandidateDigest != suspended.CandidateDigest ||
		resumed.ChangedFileCount != suspended.ChangedFileCount ||
		resumed.SuspensionHash != suspended.SuspensionHash ||
		resumed.RotationHash == "" || resumed.AlreadyResumed {
		t.Fatalf("ResumeSuspendedPinnedLine() = %#v", resumed)
	}
	suspensionAPITestAssertGitState(
		t,
		fixture.workspace.Path,
		headBefore,
		indexBefore,
		statusBefore,
	)
	if content, readErr := os.ReadFile(path); readErr != nil ||
		string(content) != "retained repair\n" {
		t.Fatalf("retained content = %q, %v", content, readErr)
	}

	restartedAgain, err := NewManager(fixture.manager.opts)
	if err != nil {
		t.Fatal(err)
	}
	resumeReplay, err := restartedAgain.ResumeSuspendedPinnedLine(ctx, resumeRequest)
	if err != nil {
		t.Fatalf("replayed ResumeSuspendedPinnedLine() error = %v", err)
	}
	if !resumeReplay.AlreadyResumed || resumeReplay.RotationHash != resumed.RotationHash ||
		resumeReplay.SuspensionHash != resumed.SuspensionHash {
		t.Fatalf("replayed resume = %#v, first = %#v", resumeReplay, resumed)
	}
}

func TestManagerSuspendPinnedLineCommitRecoveryBeforeApply(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/suspension-api-commit-before")
	path := filepath.Join(fixture.workspace.Path, "prepared.txt")
	if err := os.WriteFile(path, []byte("prepared but not applied\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	candidate := fixture.snapshot(t)
	commitRequest := fixture.commitRequest(candidate, "Prepare retained repair")
	headBefore := testGitCommit(t, fixture.workspace.Path, "HEAD")
	indexBefore := testGitObject(t, fixture.workspace.Path, "write-tree")
	statusBefore := suspensionAPITestStatus(t, fixture.workspace.Path)

	suspended, err := fixture.manager.SuspendPinnedLineCommitRecovery(
		nil,
		PinnedLineCommitSuspensionRequest{
			Suspend: suspensionAPITestSuspendRequest(
				fixture,
				"pdlnsuspend_api_commit_before",
			),
			Commit: commitRequest,
		},
	)
	if err != nil {
		t.Fatalf("SuspendPinnedLineCommitRecovery() error = %v", err)
	}
	if suspended.PreparedCommit == "" || suspended.PreparedTree != candidate.Tree ||
		suspended.PreparedCommitApplied || suspended.CandidateTree != candidate.Tree ||
		suspended.CandidateDigest != candidate.CandidateDigest ||
		suspended.ChangedFileCount != candidate.ChangedFiles {
		t.Fatalf("unapplied commit suspension = %#v", suspended)
	}
	if commit := testGitCommit(
		t,
		fixture.workspace.Path,
		suspended.PreparedCommit,
	); commit != suspended.PreparedCommit {
		t.Fatalf("prepared commit = %q, want %q", commit, suspended.PreparedCommit)
	}
	suspensionAPITestAssertGitState(
		t,
		fixture.workspace.Path,
		headBefore,
		indexBefore,
		statusBefore,
	)

	resumeRequest := suspensionAPITestResumeRequest(
		fixture,
		suspended,
		"pr-development/suspension-api-commit-before-fresh",
		"pdlnresume_api_commit_before",
	)
	resumed, err := fixture.manager.ResumeSuspendedPinnedLine(ctx, resumeRequest)
	if err != nil {
		t.Fatalf("ResumeSuspendedPinnedLine() error = %v", err)
	}
	if resumed.Version != fixture.lease.Version ||
		resumed.MutationEpoch != fixture.lease.MutationEpoch ||
		resumed.CandidateTree != candidate.Tree || resumed.RotationHash == "" {
		t.Fatalf("unapplied commit resume = %#v", resumed)
	}
	suspensionAPITestAssertGitState(
		t,
		fixture.workspace.Path,
		headBefore,
		indexBefore,
		statusBefore,
	)
}

func TestManagerSuspendPinnedLineCommitRecoveryRejectsUnappliedCandidateDrift(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/suspension-api-commit-drift")
	path := filepath.Join(fixture.workspace.Path, "prepared.txt")
	if err := os.WriteFile(path, []byte("prepared repair\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	preparedCandidate := fixture.snapshot(t)
	commitRequest := fixture.commitRequest(preparedCandidate, "Prepare exact repair")
	if err := os.WriteFile(path, []byte("unfenced later edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	headBefore := testGitCommit(t, fixture.workspace.Path, "HEAD")
	indexBefore := testGitObject(t, fixture.workspace.Path, "write-tree")
	statusBefore := suspensionAPITestStatus(t, fixture.workspace.Path)

	_, err := fixture.manager.SuspendPinnedLineCommitRecovery(
		ctx,
		PinnedLineCommitSuspensionRequest{
			Suspend: suspensionAPITestSuspendRequest(
				fixture,
				"pdlnsuspend_api_commit_drift",
			),
			Commit: commitRequest,
		},
	)
	if err == nil || !errors.Is(err, ErrPinnedLineConflict) {
		t.Fatalf("SuspendPinnedLineCommitRecovery() drift error = %v", err)
	}
	suspensionAPITestAssertGitState(
		t,
		fixture.workspace.Path,
		headBefore,
		indexBefore,
		statusBefore,
	)
	if workspace := workspaceRecordForTest(
		t,
		fixture.manager,
		fixture.workspace.ID,
	); workspace.LockedBy == nil ||
		workspace.LockedBy.SessionKey != fixture.pin.ReservationKey {
		t.Fatalf("failed suspension released reservation: %#v", workspace.LockedBy)
	}
}

func TestManagerResumeAppliedCommitSuspensionAfterReverseCAS(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/suspension-api-commit-applied")
	path := filepath.Join(fixture.workspace.Path, "prepared.txt")
	if err := os.WriteFile(path, []byte("prepared repair\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	preparedCandidate := fixture.snapshot(t)
	commitRequest := fixture.commitRequest(preparedCandidate, "Apply prepared repair")
	applied, err := fixture.manager.CommitPinned(ctx, commitRequest)
	if err != nil {
		t.Fatalf("CommitPinned() error = %v", err)
	}
	if writeErr := os.WriteFile(
		path,
		[]byte("prepared repair plus follow-up\n"),
		0o644,
	); writeErr != nil {
		t.Fatal(writeErr)
	}

	suspended, err := fixture.manager.SuspendPinnedLineCommitRecovery(
		ctx,
		PinnedLineCommitSuspensionRequest{
			Suspend: suspensionAPITestSuspendRequest(
				fixture,
				"pdlnsuspend_api_commit_applied",
			),
			Commit: commitRequest,
		},
	)
	if err != nil {
		t.Fatalf("SuspendPinnedLineCommitRecovery() error = %v", err)
	}
	if !suspended.PreparedCommitApplied || suspended.PreparedCommit != applied.Commit ||
		suspended.PreparedTree != applied.Tree ||
		suspended.CandidateTree == suspended.PreparedTree ||
		suspended.CandidateDigest == preparedCandidate.CandidateDigest {
		t.Fatalf("applied commit suspension = %#v", suspended)
	}
	if head := testGitCommit(t, fixture.workspace.Path, "HEAD"); head != applied.Commit {
		t.Fatalf("suspended applied HEAD = %q, want %q", head, applied.Commit)
	}
	if index := testGitObject(t, fixture.workspace.Path, "write-tree"); index != applied.Tree {
		t.Fatalf("suspended applied index = %q, want %q", index, applied.Tree)
	}

	// Simulate a crash after the reverse HEAD CAS and before the index reset.
	if _, gitErr := runGit(
		ctx,
		fixture.workspace.Path,
		"update-ref",
		"--no-deref",
		"HEAD",
		fixture.lease.Tip,
		applied.Commit,
	); gitErr != nil {
		t.Fatalf("simulate reverse HEAD CAS: %v", gitErr)
	}
	if head := testGitCommit(t, fixture.workspace.Path, "HEAD"); head != fixture.lease.Tip {
		t.Fatalf("reverse-CAS HEAD = %q, want %q", head, fixture.lease.Tip)
	}
	if index := testGitObject(t, fixture.workspace.Path, "write-tree"); index != applied.Tree {
		t.Fatalf("reverse-CAS index = %q, want %q", index, applied.Tree)
	}

	resumeRequest := suspensionAPITestResumeRequest(
		fixture,
		suspended,
		"pr-development/suspension-api-commit-applied-fresh",
		"pdlnresume_api_commit_applied",
	)
	resumed, err := fixture.manager.ResumeSuspendedPinnedLine(ctx, resumeRequest)
	if err != nil {
		t.Fatalf("ResumeSuspendedPinnedLine() reverse-CAS error = %v", err)
	}
	if resumed.CandidateTree != suspended.CandidateTree ||
		resumed.CandidateDigest != suspended.CandidateDigest ||
		resumed.ChangedFileCount != suspended.ChangedFileCount ||
		resumed.Version != fixture.lease.Version ||
		resumed.MutationEpoch != fixture.lease.MutationEpoch {
		t.Fatalf("reverse-CAS resume = %#v", resumed)
	}
	if head := testGitCommit(t, fixture.workspace.Path, "HEAD"); head != fixture.lease.Tip {
		t.Fatalf("resumed HEAD = %q, want %q", head, fixture.lease.Tip)
	}
	if index := testGitObject(t, fixture.workspace.Path, "write-tree"); index != fixture.lease.Tree {
		t.Fatalf("resumed index = %q, want %q", index, fixture.lease.Tree)
	}
	if content, readErr := os.ReadFile(path); readErr != nil ||
		string(content) != "prepared repair plus follow-up\n" {
		t.Fatalf("post-prepare content = %q, %v", content, readErr)
	}
	if status := suspensionAPITestStatus(t, fixture.workspace.Path); status != "?? prepared.txt\n" {
		t.Fatalf("resumed post-prepare status = %q", status)
	}
}

func TestManagerResumeAppliedCommitSuspensionFromPreparedHEAD(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/suspension-api-commit-head")
	path := filepath.Join(fixture.workspace.Path, "prepared.txt")
	if err := os.WriteFile(path, []byte("prepared repair\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	preparedCandidate := fixture.snapshot(t)
	commitRequest := fixture.commitRequest(preparedCandidate, "Apply prepared repair")
	applied, err := fixture.manager.CommitPinned(ctx, commitRequest)
	if err != nil {
		t.Fatalf("CommitPinned() error = %v", err)
	}
	if writeErr := os.WriteFile(
		path,
		[]byte("prepared repair plus review fix\n"),
		0o644,
	); writeErr != nil {
		t.Fatal(writeErr)
	}
	suspended, err := fixture.manager.SuspendPinnedLineCommitRecovery(
		ctx,
		PinnedLineCommitSuspensionRequest{
			Suspend: suspensionAPITestSuspendRequest(
				fixture,
				"pdlnsuspend_api_commit_head",
			),
			Commit: commitRequest,
		},
	)
	if err != nil {
		t.Fatalf("SuspendPinnedLineCommitRecovery() error = %v", err)
	}
	if !suspended.PreparedCommitApplied || suspended.PreparedCommit != applied.Commit ||
		suspended.PreparedTree != applied.Tree ||
		suspended.CandidateTree == suspended.PreparedTree {
		t.Fatalf("applied commit suspension = %#v", suspended)
	}

	resumed, err := fixture.manager.ResumeSuspendedPinnedLine(
		ctx,
		suspensionAPITestResumeRequest(
			fixture,
			suspended,
			"pr-development/suspension-api-commit-head-fresh",
			"pdlnresume_api_commit_head",
		),
	)
	if err != nil {
		t.Fatalf("ResumeSuspendedPinnedLine() prepared HEAD error = %v", err)
	}
	if resumed.CandidateTree != suspended.CandidateTree ||
		resumed.CandidateDigest != suspended.CandidateDigest {
		t.Fatalf("prepared HEAD resume = %#v", resumed)
	}
	if head := testGitCommit(t, fixture.workspace.Path, "HEAD"); head != fixture.lease.Tip {
		t.Fatalf("resumed HEAD = %q, want %q", head, fixture.lease.Tip)
	}
	if index := testGitObject(t, fixture.workspace.Path, "write-tree"); index != fixture.lease.Tree {
		t.Fatalf("resumed index = %q, want %q", index, fixture.lease.Tree)
	}
	if content, readErr := os.ReadFile(path); readErr != nil ||
		string(content) != "prepared repair plus review fix\n" {
		t.Fatalf("post-prepare content = %q, %v", content, readErr)
	}
}

func TestDevelopmentLineSuspensionRejectsCrossedResumeCausality(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/suspension-api-causality-old")
	firstSuspension, err := fixture.manager.SuspendPinnedLine(
		ctx,
		suspensionAPITestSuspendRequest(fixture, "pdlnsuspend_api_causality_one"),
	)
	if err != nil {
		t.Fatal(err)
	}
	firstFresh := "pr-development/suspension-api-causality-fresh-one"
	if _, resumeErr := fixture.manager.ResumeSuspendedPinnedLine(
		ctx,
		suspensionAPITestResumeRequest(
			fixture,
			firstSuspension,
			firstFresh,
			"pdlnresume_api_causality_one",
		),
	); resumeErr != nil {
		t.Fatal(resumeErr)
	}
	secondRequest := suspensionAPITestSuspendRequest(
		fixture,
		"pdlnsuspend_api_causality_two",
	)
	secondRequest.Pin.ReservationKey = firstFresh
	secondSuspension, err := fixture.manager.SuspendPinnedLine(ctx, secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.ResumeSuspendedPinnedLine(
		ctx,
		suspensionAPITestResumeRequest(
			fixture,
			secondSuspension,
			"pr-development/suspension-api-causality-fresh-two",
			"pdlnresume_api_causality_two",
		),
	); err != nil {
		t.Fatal(err)
	}

	state := adversarialCloneInventory(t, fixture.manager)
	line := state.DevelopmentLines[pinnedLineTestID]
	rotations := state.PinnedReservationRotations[fixture.workspace.ID]
	if line == nil || len(line.Suspensions) != 2 || len(rotations) != 2 {
		t.Fatalf("causal suspension inventory = %#v, rotations = %#v", line, rotations)
	}
	// The test fixture intentionally uses a fixed clock. Re-anchor an otherwise
	// valid interleaving so the adversarial rewrite can cross S2 without also
	// crossing R2 or the line's current time.
	base := line.Suspensions[0].SuspendedAt
	line.Suspensions[1].SuspendedAt = base.Add(2 * time.Nanosecond)
	rehashDevelopmentLineSuspensionsForTest(line)
	rotations[0].SuspensionHash = line.Suspensions[0].RecordHash
	rotations[0].RotatedAt = base.Add(time.Nanosecond)
	rotations[0].RecordHash = pinnedReservationRotationRecordDigest(rotations[0])
	rotations[1].SuspensionHash = line.Suspensions[1].RecordHash
	rotations[1].PreviousRecordHash = rotations[0].RecordHash
	rotations[1].RotatedAt = base.Add(4 * time.Nanosecond)
	rotations[1].RecordHash = pinnedReservationRotationRecordDigest(rotations[1])
	line.UpdatedAt = rotations[1].RotatedAt
	workspace := state.Workspaces[fixture.workspace.ID]
	workspace.PinnedReservationRotationTailHash = rotations[1].RecordHash
	workspace.UpdatedAt = rotations[1].RotatedAt
	workspace.LastWorkAt = rotations[1].RotatedAt
	workspace.LockedBy.LockedAt = rotations[1].RotatedAt
	workspace.LockedBy.HeartbeatAt = rotations[1].RotatedAt
	state.PinnedReservationRotations[fixture.workspace.ID] = rotations
	if err := validateDevelopmentLineInventory(state); err != nil {
		t.Fatalf("valid interleaved suspension inventory error = %v", err)
	}

	crossedTime := base.Add(3 * time.Nanosecond)
	rotations[0].RotatedAt = crossedTime
	rotations[0].RecordHash = pinnedReservationRotationRecordDigest(rotations[0])
	rotations[1].PreviousRecordHash = rotations[0].RecordHash
	rotations[1].RecordHash = pinnedReservationRotationRecordDigest(rotations[1])
	state.PinnedReservationRotations[fixture.workspace.ID] = rotations
	workspace.PinnedReservationRotationTailHash = rotations[1].RecordHash
	if err := validateDevelopmentLineInventory(state); err == nil {
		t.Fatal("validateDevelopmentLineInventory() accepted resume crossing later suspension")
	}
}

func suspensionAPITestSuspendRequest(
	fixture pinnedLineTestFixture,
	intent string,
) PinnedLineSuspendRequest {
	return PinnedLineSuspendRequest{
		Pin:                   fixture.pin,
		WorkspaceID:           fixture.workspace.ID,
		LineID:                pinnedLineTestID,
		IntentID:              intent,
		ExpectedVersion:       fixture.lease.Version,
		ExpectedMutationEpoch: fixture.lease.MutationEpoch,
		ExpectedTip:           fixture.lease.Tip,
		ExpectedTree:          fixture.lease.Tree,
	}
}

func suspensionAPITestResumeRequest(
	fixture pinnedLineTestFixture,
	suspended PinnedLineSuspendResult,
	reservation, intent string,
) PinnedLineSuspendedResumeRequest {
	freshPin := fixture.pin
	freshPin.ReservationKey = reservation
	return PinnedLineSuspendedResumeRequest{
		Pin:                   freshPin,
		WorkspaceID:           suspended.WorkspaceID,
		LineID:                pinnedLineTestID,
		IntentID:              intent,
		ExpectedVersion:       suspended.Version,
		ExpectedMutationEpoch: suspended.MutationEpoch,
		ExpectedTip:           suspended.Tip,
		ExpectedTree:          suspended.Tree,
		SuspensionHash:        suspended.SuspensionHash,
		CandidateTree:         suspended.CandidateTree,
		CandidateDigest:       suspended.CandidateDigest,
		ChangedFileCount:      suspended.ChangedFileCount,
	}
}

func suspensionAPITestStatus(t *testing.T, directory string) string {
	t.Helper()
	status, err := runGit(
		context.Background(),
		directory,
		"status",
		"--porcelain=v1",
		"--untracked-files=all",
	)
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func suspensionAPITestAssertGitState(
	t *testing.T,
	directory, wantHead, wantIndex, wantStatus string,
) {
	t.Helper()
	if head := testGitCommit(t, directory, "HEAD"); head != wantHead {
		t.Fatalf("HEAD = %q, want %q", head, wantHead)
	}
	if index := testGitObject(t, directory, "write-tree"); index != wantIndex {
		t.Fatalf("index = %q, want %q", index, wantIndex)
	}
	if status := suspensionAPITestStatus(t, directory); status != wantStatus {
		t.Fatalf("status = %q, want %q", status, wantStatus)
	}
}
