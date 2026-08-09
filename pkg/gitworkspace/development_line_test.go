package gitworkspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const pinnedLineTestID = "pdln_0123456789abcdef0123456789abcdef"

type pinnedLineTestFixture struct {
	pinnedCommitTestFixture
	baseTree string
	lease    PinnedLineLease
}

func newPinnedLineTestFixture(t *testing.T, reservation string) pinnedLineTestFixture {
	t.Helper()
	fixture := newPinnedCommitTestFixture(t, reservation)
	baseTree := testGitObject(t, fixture.workspace.Path, "rev-parse", "HEAD^{tree}")
	lease, err := fixture.manager.AdoptPinnedLine(
		context.Background(),
		PinnedLineAdoptRequest{
			Pin:          fixture.pin,
			WorkspaceID:  fixture.workspace.ID,
			LineID:       pinnedLineTestID,
			ExpectedTree: baseTree,
		},
	)
	if err != nil {
		t.Fatalf("AdoptPinnedLine() error = %v", err)
	}
	return pinnedLineTestFixture{
		pinnedCommitTestFixture: fixture,
		baseTree:                baseTree,
		lease:                   lease,
	}
}

func (fixture pinnedLineTestFixture) commitChange(
	t *testing.T,
	path, content, intent, message string,
	authoredAt time.Time,
) PinnedCommitResult {
	t.Helper()
	if err := os.WriteFile(
		filepath.Join(fixture.workspace.Path, path),
		[]byte(content),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	candidate := fixture.snapshot(t)
	result, err := fixture.manager.CommitPinned(
		context.Background(),
		PinnedCommitRequest{
			Pin:                     fixture.pin,
			WorkspaceID:             fixture.workspace.ID,
			IntentID:                intent,
			ExpectedParent:          candidate.ParentCommit,
			ExpectedTree:            candidate.Tree,
			ExpectedCandidateDigest: candidate.CandidateDigest,
			Message:                 message,
			AuthoredAt:              authoredAt,
		},
	)
	if err != nil {
		t.Fatalf("CommitPinned() error = %v", err)
	}
	return result
}

func TestManagerPinnedDevelopmentLineParkReviewResumeAcrossRestart(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/line-attempt-1")
	firstPin := fixture.pin
	if fixture.lease.Version != 0 || fixture.lease.MutationEpoch != 1 ||
		fixture.lease.Tip != fixture.pin.ExpectedCommit ||
		fixture.lease.Tree != fixture.baseTree || fixture.lease.AlreadyOwned {
		t.Fatalf("AdoptPinnedLine() = %#v", fixture.lease)
	}
	replayedAdoption, err := fixture.manager.AdoptPinnedLine(ctx, PinnedLineAdoptRequest{
		Pin:          fixture.pin,
		WorkspaceID:  fixture.workspace.ID,
		LineID:       pinnedLineTestID,
		ExpectedTree: fixture.baseTree,
	})
	if err != nil || !replayedAdoption.AlreadyOwned {
		t.Fatalf("replayed AdoptPinnedLine() = %#v, %v", replayedAdoption, err)
	}

	first := fixture.commitChange(
		t,
		"first.txt",
		"first repair\n",
		"pdcmt_11111111111111111111111111111111",
		"Apply first repair",
		time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC),
	)
	firstParkRequest := PinnedLineParkRequest{
		Pin:             fixture.pin,
		WorkspaceID:     fixture.workspace.ID,
		LineID:          pinnedLineTestID,
		IntentID:        "pdlnpark_11111111111111111111111111111111",
		ExpectedVersion: 0,
		MutationEpoch:   1,
		PreviousTip:     fixture.pin.ExpectedCommit,
		Tip:             first.Commit,
		Tree:            first.Tree,
	}
	parked, err := fixture.manager.ParkPinnedLine(ctx, firstParkRequest)
	if err != nil {
		t.Fatalf("ParkPinnedLine() error = %v", err)
	}
	if parked.Version != 1 || parked.MutationEpoch != 1 ||
		parked.PreviousTip != fixture.pin.ExpectedCommit || parked.Tip != first.Commit ||
		parked.Tree != first.Tree || parked.NoChanges || parked.AlreadyParked ||
		!parked.WorkspaceClean {
		t.Fatalf("ParkPinnedLine() = %#v", parked)
	}
	info := workspaceRecordForTest(t, fixture.manager, fixture.workspace.ID)
	if info.LockedBy != nil || info.DevelopmentLineID != pinnedLineTestID {
		t.Fatalf("parked workspace = %#v", info)
	}
	if _, releaseErr := fixture.manager.ReleasePinned(ctx, PinnedReleaseRequest{
		ReservationKey: fixture.pin.ReservationKey,
		AgentID:        fixture.pin.AgentID,
	}); releaseErr == nil || !strings.Contains(releaseErr.Error(), "development line") {
		t.Fatalf("ReleasePinned() error = %v, want line rejection", releaseErr)
	}
	if _, acquireErr := fixture.manager.AcquirePinned(ctx, fixture.pin); acquireErr == nil ||
		!strings.Contains(acquireErr.Error(), "released from a development line") {
		t.Fatalf("stale AcquirePinned() error = %v", acquireErr)
	}

	review, err := fixture.manager.SnapshotPinnedLineReview(ctx, PinnedLineReviewRequest{
		LineID:          pinnedLineTestID,
		ExpectedVersion: 1,
		ExpectedBase:    fixture.pin.ExpectedCommit,
		ExpectedTip:     first.Commit,
		ExpectedTree:    first.Tree,
	})
	if err != nil {
		t.Fatalf("SnapshotPinnedLineReview() error = %v", err)
	}
	if review.BaseCommit != fixture.pin.ExpectedCommit || review.Commit != first.Commit ||
		review.Tree != first.Tree || len(review.ChangedPaths) != 1 ||
		review.ChangedPaths[0] != "first.txt" ||
		!strings.Contains(review.UnifiedDiff, "+first repair") ||
		strings.Contains(review.UnifiedDiff, fixture.workspace.Path) {
		t.Fatalf("SnapshotPinnedLineReview() = %#v", review)
	}
	replayedPark, err := fixture.manager.ParkPinnedLine(ctx, firstParkRequest)
	if err != nil || !replayedPark.AlreadyParked || replayedPark.Tip != first.Commit {
		t.Fatalf("replayed ParkPinnedLine() = %#v, %v", replayedPark, err)
	}

	root := fixture.manager.RootDir()
	now := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
	restarted := newTestManagerAtRoot(t, root, &now)
	secondPin := fixture.pin
	secondPin.ReservationKey = "pr-development/line-attempt-2"
	resumed, err := restarted.ResumePinnedLine(ctx, PinnedLineResumeRequest{
		Pin:             secondPin,
		WorkspaceID:     fixture.workspace.ID,
		LineID:          pinnedLineTestID,
		ExpectedVersion: 1,
		ExpectedEpoch:   1,
		ExpectedTip:     first.Commit,
		ExpectedTree:    first.Tree,
	})
	if err != nil {
		t.Fatalf("ResumePinnedLine() error = %v", err)
	}
	if resumed.Version != 1 || resumed.MutationEpoch != 2 || resumed.Tip != first.Commit ||
		resumed.Tree != first.Tree || resumed.AlreadyOwned {
		t.Fatalf("ResumePinnedLine() = %#v", resumed)
	}
	replayedResume, err := restarted.ResumePinnedLine(ctx, PinnedLineResumeRequest{
		Pin:             secondPin,
		WorkspaceID:     fixture.workspace.ID,
		LineID:          pinnedLineTestID,
		ExpectedVersion: 1,
		ExpectedEpoch:   1,
		ExpectedTip:     first.Commit,
		ExpectedTree:    first.Tree,
	})
	if err != nil || !replayedResume.AlreadyOwned {
		t.Fatalf("replayed ResumePinnedLine() = %#v, %v", replayedResume, err)
	}

	fixture.manager = restarted
	fixture.pin = secondPin
	second := fixture.commitChange(
		t,
		"second.txt",
		"second repair\n",
		"pdcmt_22222222222222222222222222222222",
		"Apply second repair",
		time.Date(2026, 8, 9, 11, 0, 0, 0, time.UTC),
	)
	secondPark, err := restarted.ParkPinnedLine(ctx, PinnedLineParkRequest{
		Pin:             secondPin,
		WorkspaceID:     fixture.workspace.ID,
		LineID:          pinnedLineTestID,
		IntentID:        "pdlnpark_22222222222222222222222222222222",
		ExpectedVersion: 1,
		MutationEpoch:   2,
		PreviousTip:     first.Commit,
		Tip:             second.Commit,
		Tree:            second.Tree,
	})
	if err != nil {
		t.Fatalf("second ParkPinnedLine() error = %v", err)
	}
	if secondPark.Version != 2 || secondPark.Tip != second.Commit {
		t.Fatalf("second ParkPinnedLine() = %#v", secondPark)
	}
	if _, acquireErr := restarted.AcquirePinned(ctx, firstPin); acquireErr == nil ||
		!strings.Contains(acquireErr.Error(), "released from a development line") {
		t.Fatalf("first retired reservation AcquirePinned() error = %v", acquireErr)
	}
	refs, err := runGit(
		ctx,
		fixture.workspace.Path,
		"for-each-ref",
		"--format=%(objectname) %(refname)",
		"refs/heads/picoclaw/development",
	)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(strings.TrimSpace(refs))
	if len(lines) != 2 || lines[0] != second.Commit ||
		lines[1] != "refs/heads/"+developmentLineBranch(pinnedLineTestID) {
		t.Fatalf("retained refs = %q", refs)
	}
}

func TestManagerPinnedDevelopmentLineRejectsDirtyParkAndProtectsCheckout(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/line-dirty")
	if err := os.WriteFile(
		filepath.Join(fixture.workspace.Path, "dirty.txt"),
		[]byte("uncommitted\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	_, err := fixture.manager.ParkPinnedLine(ctx, PinnedLineParkRequest{
		Pin:             fixture.pin,
		WorkspaceID:     fixture.workspace.ID,
		LineID:          pinnedLineTestID,
		IntentID:        "pdlnpark_dirty",
		ExpectedVersion: 0,
		MutationEpoch:   1,
		PreviousTip:     fixture.pin.ExpectedCommit,
		Tip:             fixture.pin.ExpectedCommit,
		Tree:            fixture.baseTree,
		NoChanges:       true,
	})
	if err == nil || !errors.Is(err, ErrPinnedLineConflict) {
		t.Fatalf("dirty ParkPinnedLine() error = %v", err)
	}
	heartbeat, heartbeatErr := fixture.manager.AcquirePinned(ctx, fixture.pin)
	if heartbeatErr != nil || heartbeat.ID != fixture.workspace.ID || heartbeat.LockedBy == nil {
		t.Fatalf("AcquirePinned() after dirty park = %#v, %v", heartbeat, heartbeatErr)
	}
	wantNotFound := fmt.Sprintf("git workspace %q not found", fixture.workspace.ID)
	if _, cleanupErr := fixture.manager.CleanupIgnored(
		ctx,
		fixture.workspace.ID,
	); cleanupErr == nil || cleanupErr.Error() != wantNotFound {
		t.Fatalf("CleanupIgnored() error = %v, want %q", cleanupErr, wantNotFound)
	}
	if _, dropErr := fixture.manager.Drop(ctx, fixture.workspace.ID); dropErr == nil ||
		dropErr.Error() != wantNotFound {
		t.Fatalf("Drop() error = %v, want %q", dropErr, wantNotFound)
	}
}

func TestManagerPinnedDevelopmentLineSurvivesAutomaticReconcileAndIsPrivate(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/line-retained")
	parked, err := fixture.manager.ParkPinnedLine(ctx, PinnedLineParkRequest{
		Pin:             fixture.pin,
		WorkspaceID:     fixture.workspace.ID,
		LineID:          pinnedLineTestID,
		IntentID:        "pdlnpark_no_changes",
		ExpectedVersion: 0,
		MutationEpoch:   1,
		PreviousTip:     fixture.pin.ExpectedCommit,
		Tip:             fixture.pin.ExpectedCommit,
		Tree:            fixture.baseTree,
		NoChanges:       true,
	})
	if err != nil || !parked.NoChanges {
		t.Fatalf("no-change ParkPinnedLine() = %#v, %v", parked, err)
	}
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	manager, err := NewManager(Options{
		RootDir:             fixture.manager.RootDir(),
		MaxTotalSizeBytes:   1,
		IgnoredCleanupDelay: time.Nanosecond,
		DropDelay:           time.Nanosecond,
		Now:                 func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(result.Dropped) != 0 || len(result.Cleaned) != 0 ||
		result.Stats.WorkspaceCount != 0 || len(result.Stats.Workspaces) != 0 ||
		result.Stats.LockedWorkspaceCount != 0 {
		t.Fatalf("Reconcile() = %#v", result)
	}
	if _, statErr := os.Stat(fixture.workspace.Path); statErr != nil {
		t.Fatalf("retained workspace stat error = %v", statErr)
	}
	for _, history := range result.Stats.History {
		if history.WorkspaceID == fixture.workspace.ID ||
			strings.Contains(history.Action, "development_line") ||
			strings.Contains(history.SessionKey, fixture.pin.ReservationKey) {
			t.Fatalf("private line history leaked: %#v", history)
		}
	}
}

func TestManagerPinnedDevelopmentLineIsInvisibleToGenericSessionCollision(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/line-generic-collision")
	request := AcquireRequest{
		Repository: fixture.pin.Repository,
		Ref:        fixture.pin.SourceRef,
		SessionKey: fixture.pin.ReservationKey,
		AgentID:    "generic-agent",
	}
	generic, err := fixture.manager.Acquire(ctx, request)
	if err != nil {
		t.Fatalf("Acquire() colliding generic session error = %v", err)
	}
	if generic.ID != fixture.workspace.RepoID || generic.ID == fixture.workspace.ID {
		t.Fatalf("colliding generic workspace = %#v; private = %#v", generic, fixture.workspace)
	}
	replayed, err := fixture.manager.Acquire(ctx, request)
	if err != nil || replayed.ID != generic.ID || replayed.Path != generic.Path {
		t.Fatalf("replayed generic Acquire() = %#v, %v", replayed, err)
	}
	released, err := fixture.manager.ReleaseSession(ctx, ReleaseRequest{
		SessionKey: fixture.pin.ReservationKey,
		AgentID:    request.AgentID,
	})
	if err != nil || len(released) != 1 || released[0].ID != generic.ID {
		t.Fatalf("ReleaseSession() = %#v, %v", released, err)
	}
	lineWorkspace := workspaceRecordForTest(t, fixture.manager, fixture.workspace.ID)
	if lineWorkspace.LockedBy == nil ||
		lineWorkspace.LockedBy.SessionKey != fixture.pin.ReservationKey {
		t.Fatalf("generic release changed private line lock: %#v", lineWorkspace.LockedBy)
	}
}

func TestManagerPinnedDevelopmentLineConcurrentResumeHasOneWinner(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/line-concurrent-first")
	parked, err := fixture.manager.ParkPinnedLine(ctx, PinnedLineParkRequest{
		Pin:             fixture.pin,
		WorkspaceID:     fixture.workspace.ID,
		LineID:          pinnedLineTestID,
		IntentID:        "pdlnpark_concurrent",
		ExpectedVersion: 0,
		MutationEpoch:   1,
		PreviousTip:     fixture.pin.ExpectedCommit,
		Tip:             fixture.pin.ExpectedCommit,
		Tree:            fixture.baseTree,
		NoChanges:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	managers := []*Manager{
		newTestManagerAtRoot(t, fixture.manager.RootDir(), &now),
		newTestManagerAtRoot(t, fixture.manager.RootDir(), &now),
	}
	errorsByWorker := make([]error, 2)
	var wait sync.WaitGroup
	for index := range managers {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			pin := fixture.pin
			pin.ReservationKey = "pr-development/line-concurrent-" + string(rune('a'+index))
			_, errorsByWorker[index] = managers[index].ResumePinnedLine(
				ctx,
				PinnedLineResumeRequest{
					Pin:             pin,
					WorkspaceID:     fixture.workspace.ID,
					LineID:          pinnedLineTestID,
					ExpectedVersion: parked.Version,
					ExpectedEpoch:   parked.MutationEpoch,
					ExpectedTip:     parked.Tip,
					ExpectedTree:    parked.Tree,
				},
			)
		}(index)
	}
	wait.Wait()
	successes := 0
	conflicts := 0
	for _, err := range errorsByWorker {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrPinnedLineConflict):
			conflicts++
		default:
			t.Fatalf("concurrent ResumePinnedLine() error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent resume successes/conflicts = %d/%d", successes, conflicts)
	}
}

func TestManagerPinnedDevelopmentLineDetectsRetainedRefDrift(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/line-ref-drift")
	first := fixture.commitChange(
		t,
		"drift.txt",
		"review me\n",
		"pdcmt_33333333333333333333333333333333",
		"Create review subject",
		time.Date(2026, 8, 9, 13, 0, 0, 0, time.UTC),
	)
	parked, err := fixture.manager.ParkPinnedLine(ctx, PinnedLineParkRequest{
		Pin:             fixture.pin,
		WorkspaceID:     fixture.workspace.ID,
		LineID:          pinnedLineTestID,
		IntentID:        "pdlnpark_ref_drift",
		ExpectedVersion: 0,
		MutationEpoch:   1,
		PreviousTip:     fixture.pin.ExpectedCommit,
		Tip:             first.Commit,
		Tree:            first.Tree,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, updateErr := runGit(
		ctx,
		fixture.workspace.Path,
		"update-ref",
		"refs/heads/"+developmentLineBranch(pinnedLineTestID),
		fixture.pin.ExpectedCommit,
		first.Commit,
	); updateErr != nil {
		t.Fatal(updateErr)
	}
	_, err = fixture.manager.SnapshotPinnedLineReview(ctx, PinnedLineReviewRequest{
		LineID:          pinnedLineTestID,
		ExpectedVersion: parked.Version,
		ExpectedBase:    parked.PreviousTip,
		ExpectedTip:     parked.Tip,
		ExpectedTree:    parked.Tree,
	})
	if err == nil || !errors.Is(err, ErrPinnedLineConflict) {
		t.Fatalf("SnapshotPinnedLineReview() drift error = %v", err)
	}
}

func workspaceRecordForTest(t *testing.T, manager *Manager, workspaceID string) *WorkspaceRecord {
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
	workspace := state.Workspaces[workspaceID]
	if workspace == nil {
		t.Fatalf("workspace %q is missing", workspaceID)
	}
	copyWorkspace := *workspace
	copyWorkspace.LockedBy = cloneLock(workspace.LockedBy)
	return &copyWorkspace
}
