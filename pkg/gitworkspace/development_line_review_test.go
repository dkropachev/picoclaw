package gitworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestManagerPinnedDevelopmentLineReviewCanonicalizesCRLF(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/line-review-crlf")
	commit := fixture.commitChange(
		t,
		"crlf.txt",
		"first\r\nsecond\r\n",
		"pdcmt_44444444444444444444444444444444",
		"Add CRLF review subject",
		time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC),
	)
	parked, err := fixture.manager.ParkPinnedLine(ctx, PinnedLineParkRequest{
		Pin:             fixture.pin,
		WorkspaceID:     fixture.workspace.ID,
		LineID:          pinnedLineTestID,
		IntentID:        "pdlnpark_review_crlf",
		ExpectedVersion: 0,
		MutationEpoch:   1,
		PreviousTip:     fixture.pin.ExpectedCommit,
		Tip:             commit.Commit,
		Tree:            commit.Tree,
	})
	if err != nil {
		t.Fatal(err)
	}
	review, err := fixture.manager.SnapshotPinnedLineReview(ctx, PinnedLineReviewRequest{
		LineID:          pinnedLineTestID,
		ExpectedVersion: parked.Version,
		ExpectedBase:    parked.PreviousTip,
		ExpectedTip:     parked.Tip,
		ExpectedTree:    parked.Tree,
	})
	if err != nil {
		t.Fatalf("SnapshotPinnedLineReview() error = %v", err)
	}
	if strings.ContainsRune(review.UnifiedDiff, '\r') ||
		!strings.Contains(review.UnifiedDiff, "+first\n+second\n") {
		t.Fatalf("canonical review diff = %q", review.UnifiedDiff)
	}
	if review.Version != parked.Version || review.MutationEpoch != parked.MutationEpoch ||
		review.ParkIntentID != "pdlnpark_review_crlf" ||
		!validLowerHex(review.ReviewDigest, 64) {
		t.Fatalf("review provenance = %#v", review)
	}
	replayed, err := fixture.manager.SnapshotPinnedLineReview(ctx, PinnedLineReviewRequest{
		LineID:          pinnedLineTestID,
		ExpectedVersion: parked.Version,
		ExpectedBase:    parked.PreviousTip,
		ExpectedTip:     parked.Tip,
		ExpectedTree:    parked.Tree,
	})
	if err != nil || replayed.ReviewDigest != review.ReviewDigest {
		t.Fatalf("replayed review digest = %q, %v; want %q", replayed.ReviewDigest, err, review.ReviewDigest)
	}
}

func TestManagerPreviewPinnedLineReviewMatchesParkedSnapshot(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/line-review-preview")
	commit := fixture.commitChange(
		t,
		"preview.txt",
		"previewed repair\n",
		"pdcmt_99999999999999999999999999999999",
		"Preview repair subject",
		time.Date(2026, 8, 9, 19, 0, 0, 0, time.UTC),
	)
	request := PinnedLineParkRequest{
		Pin:             fixture.pin,
		WorkspaceID:     fixture.workspace.ID,
		LineID:          pinnedLineTestID,
		IntentID:        "pdlnpark_review_preview",
		ExpectedVersion: fixture.lease.Version,
		MutationEpoch:   fixture.lease.MutationEpoch,
		PreviousTip:     fixture.lease.Tip,
		Tip:             commit.Commit,
		Tree:            commit.Tree,
	}
	stateBefore := developmentLineStateForTest(t, fixture.manager)
	branch := stateBefore.DevelopmentLines[pinnedLineTestID].Branch
	refBefore := testGitCommit(t, fixture.workspace.Path, "refs/heads/"+branch)

	var preview PinnedLineReviewSnapshot
	if err := fixture.manager.WithPinnedOperation(
		ctx,
		fixture.pin,
		func(operationCtx context.Context) error {
			var previewErr error
			preview, previewErr = fixture.manager.PreviewPinnedLineReview(
				operationCtx,
				request,
			)
			if previewErr != nil {
				return previewErr
			}
			workspace := workspaceRecordForTest(t, fixture.manager, fixture.workspace.ID)
			if workspace.LockedBy == nil ||
				workspace.LockedBy.SessionKey != fixture.pin.ReservationKey {
				t.Fatalf("preview released reservation: %#v", workspace)
			}
			return nil
		},
	); err != nil {
		t.Fatalf("PreviewPinnedLineReview() error = %v", err)
	}
	stateAfter := developmentLineStateForTest(t, fixture.manager)
	lineAfter := stateAfter.DevelopmentLines[pinnedLineTestID]
	if lineAfter.State != developmentLineMutating || lineAfter.Version != fixture.lease.Version ||
		lineAfter.PendingParkSet || lineAfter.LastParkIntentID != "" ||
		stateAfter.Workspaces[fixture.workspace.ID].LockedBy == nil {
		t.Fatalf("preview mutated line inventory: %#v", lineAfter)
	}
	if refAfter := testGitCommit(t, fixture.workspace.Path, "refs/heads/"+branch); refAfter != refBefore {
		t.Fatalf("preview advanced retained ref from %q to %q", refBefore, refAfter)
	}

	parked, err := fixture.manager.ParkPinnedLine(ctx, request)
	if err != nil {
		t.Fatalf("ParkPinnedLine() error = %v", err)
	}
	snapshot, err := fixture.manager.SnapshotPinnedLineReview(ctx, PinnedLineReviewRequest{
		LineID:          pinnedLineTestID,
		ExpectedVersion: parked.Version,
		ExpectedBase:    parked.PreviousTip,
		ExpectedTip:     parked.Tip,
		ExpectedTree:    parked.Tree,
	})
	if err != nil {
		t.Fatalf("SnapshotPinnedLineReview() error = %v", err)
	}
	if !reflect.DeepEqual(preview, snapshot) {
		t.Fatalf("preview = %#v; parked snapshot = %#v", preview, snapshot)
	}
}

func TestManagerPreviewPinnedLineReviewMatchesNoChangePark(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/line-review-preview-no-change")
	request := PinnedLineParkRequest{
		Pin:             fixture.pin,
		WorkspaceID:     fixture.workspace.ID,
		LineID:          pinnedLineTestID,
		IntentID:        "pdlnpark_review_preview_no_change",
		ExpectedVersion: fixture.lease.Version,
		MutationEpoch:   fixture.lease.MutationEpoch,
		PreviousTip:     fixture.lease.Tip,
		Tip:             fixture.lease.Tip,
		Tree:            fixture.lease.Tree,
		NoChanges:       true,
	}
	preview, err := fixture.manager.PreviewPinnedLineReview(ctx, request)
	if err != nil {
		t.Fatalf("PreviewPinnedLineReview(no-change) error = %v", err)
	}
	if len(preview.ChangedPaths) != 0 || preview.UnifiedDiff != "" {
		t.Fatalf("no-change preview = %#v", preview)
	}
	parked, err := fixture.manager.ParkPinnedLine(ctx, request)
	if err != nil {
		t.Fatalf("ParkPinnedLine(no-change) error = %v", err)
	}
	snapshot, err := fixture.manager.SnapshotPinnedLineReview(ctx, PinnedLineReviewRequest{
		LineID:          pinnedLineTestID,
		ExpectedVersion: parked.Version,
		ExpectedBase:    parked.PreviousTip,
		ExpectedTip:     parked.Tip,
		ExpectedTree:    parked.Tree,
	})
	if err != nil {
		t.Fatalf("SnapshotPinnedLineReview(no-change) error = %v", err)
	}
	if !reflect.DeepEqual(preview, snapshot) {
		t.Fatalf("no-change preview = %#v; parked snapshot = %#v", preview, snapshot)
	}
}

func TestManagerPreviewPinnedLineReviewRejectsOversizedDiffBeforePark(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/line-review-preview-oversized")
	commit := fixture.commitChange(
		t,
		"oversized.txt",
		strings.Repeat("review overflow line\n", 32_000),
		"pdcmt_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"Create oversized review subject",
		time.Date(2026, 8, 9, 20, 0, 0, 0, time.UTC),
	)
	request := PinnedLineParkRequest{
		Pin:             fixture.pin,
		WorkspaceID:     fixture.workspace.ID,
		LineID:          pinnedLineTestID,
		IntentID:        "pdlnpark_review_preview_oversized",
		ExpectedVersion: fixture.lease.Version,
		MutationEpoch:   fixture.lease.MutationEpoch,
		PreviousTip:     fixture.lease.Tip,
		Tip:             commit.Commit,
		Tree:            commit.Tree,
	}
	if _, err := fixture.manager.PreviewPinnedLineReview(ctx, request); err == nil ||
		!strings.Contains(err.Error(), "output exceeded") {
		t.Fatalf("PreviewPinnedLineReview(oversized) error = %v", err)
	}
	state := developmentLineStateForTest(t, fixture.manager)
	line := state.DevelopmentLines[pinnedLineTestID]
	workspace := state.Workspaces[fixture.workspace.ID]
	if line == nil || line.State != developmentLineMutating || line.PendingParkSet ||
		line.Version != fixture.lease.Version || workspace == nil || workspace.LockedBy == nil {
		t.Fatalf("oversized preview changed park state: line %#v, workspace %#v", line, workspace)
	}
	if retained := testGitCommit(
		t,
		fixture.workspace.Path,
		"refs/heads/"+line.Branch,
	); retained != request.PreviousTip {
		t.Fatalf("oversized preview advanced retained ref to %q", retained)
	}
}

func TestManagerPinnedDevelopmentLineReviewRejectsUnsafeText(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "bare carriage return", content: "before\rafter\n"},
		{name: "escape", content: "before\x1bafter\n"},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newPinnedLineTestFixture(
				t,
				"pr-development/line-review-invalid-"+test.name,
			)
			intentDigit := string(rune('5' + index))
			commit := fixture.commitChange(
				t,
				"unsafe.txt",
				test.content,
				"pdcmt_"+strings.Repeat(intentDigit, 32),
				"Add unsafe review subject",
				time.Date(2026, 8, 9, 15+index, 0, 0, 0, time.UTC),
			)
			parked, err := fixture.manager.ParkPinnedLine(ctx, PinnedLineParkRequest{
				Pin:             fixture.pin,
				WorkspaceID:     fixture.workspace.ID,
				LineID:          pinnedLineTestID,
				IntentID:        "pdlnpark_review_invalid_" + intentDigit,
				ExpectedVersion: 0,
				MutationEpoch:   1,
				PreviousTip:     fixture.pin.ExpectedCommit,
				Tip:             commit.Commit,
				Tree:            commit.Tree,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = fixture.manager.SnapshotPinnedLineReview(
				ctx,
				PinnedLineReviewRequest{
					LineID:          pinnedLineTestID,
					ExpectedVersion: parked.Version,
					ExpectedBase:    parked.PreviousTip,
					ExpectedTip:     parked.Tip,
					ExpectedTree:    parked.Tree,
				},
			)
			if err == nil || !errors.Is(err, ErrPinnedLineConflict) {
				t.Fatalf("SnapshotPinnedLineReview() error = %v", err)
			}
		})
	}
}

func TestManagerPinnedDevelopmentLineReviewCannotHideChangesWithAttributes(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/line-review-attributes")
	if err := os.WriteFile(
		filepath.Join(fixture.workspace.Path, ".gitattributes"),
		[]byte("*.go -diff\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	commit := fixture.commitChange(
		t,
		"security.go",
		"package security\n\nconst reviewed = true\n",
		"pdcmt_88888888888888888888888888888888",
		"Add attributed review subject",
		time.Date(2026, 8, 9, 18, 0, 0, 0, time.UTC),
	)
	parked, err := fixture.manager.ParkPinnedLine(ctx, PinnedLineParkRequest{
		Pin:             fixture.pin,
		WorkspaceID:     fixture.workspace.ID,
		LineID:          pinnedLineTestID,
		IntentID:        "pdlnpark_review_attributes",
		ExpectedVersion: 0,
		MutationEpoch:   1,
		PreviousTip:     fixture.pin.ExpectedCommit,
		Tip:             commit.Commit,
		Tree:            commit.Tree,
	})
	if err != nil {
		t.Fatal(err)
	}
	review, err := fixture.manager.SnapshotPinnedLineReview(ctx, PinnedLineReviewRequest{
		LineID:          pinnedLineTestID,
		ExpectedVersion: parked.Version,
		ExpectedBase:    parked.PreviousTip,
		ExpectedTip:     parked.Tip,
		ExpectedTree:    parked.Tree,
	})
	if err != nil {
		t.Fatalf("SnapshotPinnedLineReview() error = %v", err)
	}
	if !strings.Contains(review.UnifiedDiff, "+const reviewed = true") ||
		strings.Contains(review.UnifiedDiff, "Binary files") {
		t.Fatalf("attributed review diff hid text: %q", review.UnifiedDiff)
	}
}

func TestManagerPinnedDevelopmentLineReviewRejectsLocalDiffConfiguration(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/line-review-config")
	commit := fixture.commitChange(
		t,
		"configured.txt",
		"review subject\n",
		"pdcmt_77777777777777777777777777777777",
		"Add configured review subject",
		time.Date(2026, 8, 9, 17, 0, 0, 0, time.UTC),
	)
	parked, err := fixture.manager.ParkPinnedLine(ctx, PinnedLineParkRequest{
		Pin:             fixture.pin,
		WorkspaceID:     fixture.workspace.ID,
		LineID:          pinnedLineTestID,
		IntentID:        "pdlnpark_review_config",
		ExpectedVersion: 0,
		MutationEpoch:   1,
		PreviousTip:     fixture.pin.ExpectedCommit,
		Tip:             commit.Commit,
		Tree:            commit.Tree,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := PinnedLineReviewRequest{
		LineID:          pinnedLineTestID,
		ExpectedVersion: parked.Version,
		ExpectedBase:    parked.PreviousTip,
		ExpectedTip:     parked.Tip,
		ExpectedTree:    parked.Tree,
	}
	if _, err := runGit(
		ctx,
		fixture.workspace.Path,
		"config",
		"--local",
		"diff.untrusted.binary",
		"true",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.SnapshotPinnedLineReview(ctx, request); err == nil ||
		!errors.Is(err, ErrPinnedLineConflict) {
		t.Fatalf("SnapshotPinnedLineReview() unsafe config error = %v", err)
	}
	if _, err := runGit(
		ctx,
		fixture.workspace.Path,
		"config",
		"--local",
		"--unset-all",
		"diff.untrusted.binary",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.SnapshotPinnedLineReview(ctx, request); err != nil {
		t.Fatalf("SnapshotPinnedLineReview() after config recovery = %v", err)
	}
}

func TestManagerPinnedDevelopmentLineReviewRejectsOtherOutputConfiguration(t *testing.T) {
	tests := []struct {
		key   string
		value string
	}{
		{key: "core.bigFileThreshold", value: "1"},
		{key: "core.quotePath", value: "false"},
	}
	for _, test := range tests {
		t.Run(test.key, func(t *testing.T) {
			ctx := context.Background()
			fixture := newPinnedLineTestFixture(
				t,
				"pr-development/line-review-output-config-"+test.key,
			)
			commit := fixture.commitChange(
				t,
				"configured.txt",
				"review subject\n",
				"pdcmt_99999999999999999999999999999999",
				"Add output-config review subject",
				time.Date(2026, 8, 9, 19, 0, 0, 0, time.UTC),
			)
			parked, err := fixture.manager.ParkPinnedLine(ctx, PinnedLineParkRequest{
				Pin:             fixture.pin,
				WorkspaceID:     fixture.workspace.ID,
				LineID:          pinnedLineTestID,
				IntentID:        "pdlnpark_review_output_config",
				ExpectedVersion: 0,
				MutationEpoch:   1,
				PreviousTip:     fixture.pin.ExpectedCommit,
				Tip:             commit.Commit,
				Tree:            commit.Tree,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, configErr := runGit(
				ctx,
				fixture.workspace.Path,
				"config",
				"--local",
				test.key,
				test.value,
			); configErr != nil {
				t.Fatal(configErr)
			}
			_, err = fixture.manager.SnapshotPinnedLineReview(
				ctx,
				PinnedLineReviewRequest{
					LineID:          pinnedLineTestID,
					ExpectedVersion: parked.Version,
					ExpectedBase:    parked.PreviousTip,
					ExpectedTip:     parked.Tip,
					ExpectedTree:    parked.Tree,
				},
			)
			if err == nil || !errors.Is(err, ErrPinnedLineConflict) {
				t.Fatalf("SnapshotPinnedLineReview() unsafe config error = %v", err)
			}
		})
	}
}

func TestManagerPinnedDevelopmentLineParkRejectsLocalFsyncConfiguration(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/line-fsync-config")
	if _, err := runGit(
		ctx,
		fixture.workspace.Path,
		"config",
		"--local",
		"core.fsync",
		"none",
	); err != nil {
		t.Fatal(err)
	}
	request := PinnedLineParkRequest{
		Pin:             fixture.pin,
		WorkspaceID:     fixture.workspace.ID,
		LineID:          pinnedLineTestID,
		IntentID:        "pdlnpark_fsync_config",
		ExpectedVersion: 0,
		MutationEpoch:   1,
		PreviousTip:     fixture.pin.ExpectedCommit,
		Tip:             fixture.pin.ExpectedCommit,
		Tree:            fixture.baseTree,
		NoChanges:       true,
	}
	if _, err := fixture.manager.ParkPinnedLine(ctx, request); err == nil ||
		!errors.Is(err, ErrPinnedLineConflict) {
		t.Fatalf("ParkPinnedLine() unsafe fsync error = %v", err)
	}
	state := developmentLineStateForTest(t, fixture.manager)
	line := state.DevelopmentLines[pinnedLineTestID]
	if line == nil || line.PendingParkSet || line.State != developmentLineMutating {
		t.Fatalf("unsafe fsync config changed line: %#v", line)
	}
	if _, err := runGit(
		ctx,
		fixture.workspace.Path,
		"config",
		"--local",
		"--unset-all",
		"core.fsync",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.ParkPinnedLine(ctx, request); err != nil {
		t.Fatalf("ParkPinnedLine() after fsync recovery = %v", err)
	}
}

func TestManagerPinnedDevelopmentLineRejectsOversizedAgentBeforeEffects(t *testing.T) {
	ctx := context.Background()
	t.Run("adopt", func(t *testing.T) {
		fixture := newPinnedCommitTestFixture(t, "pr-development/line-agent-adopt")
		requestPin := fixture.pin
		requestPin.AgentID = strings.Repeat("a", 257)
		_, err := fixture.manager.AdoptPinnedLine(ctx, PinnedLineAdoptRequest{
			Pin:          requestPin,
			WorkspaceID:  fixture.workspace.ID,
			LineID:       pinnedLineTestID,
			ExpectedTree: testGitObject(t, fixture.workspace.Path, "rev-parse", "HEAD^{tree}"),
		})
		if err == nil || !errors.Is(err, ErrPinnedLineInvalid) {
			t.Fatalf("AdoptPinnedLine() error = %v", err)
		}
		state := developmentLineStateForTest(t, fixture.manager)
		if len(state.DevelopmentLines) != 0 ||
			state.Workspaces[fixture.workspace.ID].DevelopmentLineID != "" {
			t.Fatalf("invalid adoption changed inventory: %#v", state)
		}
		if _, err := runGit(
			ctx,
			fixture.workspace.Path,
			"show-ref",
			"--verify",
			"refs/heads/"+developmentLineBranch(pinnedLineTestID),
		); err == nil {
			t.Fatal("invalid adoption created its retained ref")
		}
	})

	t.Run("resume", func(t *testing.T) {
		fixture := newPinnedLineTestFixture(t, "pr-development/line-agent-resume-first")
		parked, err := fixture.manager.ParkPinnedLine(ctx, PinnedLineParkRequest{
			Pin:             fixture.pin,
			WorkspaceID:     fixture.workspace.ID,
			LineID:          pinnedLineTestID,
			IntentID:        "pdlnpark_agent_resume",
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
		requestPin := fixture.pin
		requestPin.ReservationKey = "pr-development/line-agent-resume-second"
		requestPin.AgentID = strings.Repeat("a", 257)
		_, err = fixture.manager.ResumePinnedLine(ctx, PinnedLineResumeRequest{
			Pin:             requestPin,
			WorkspaceID:     fixture.workspace.ID,
			LineID:          pinnedLineTestID,
			ExpectedVersion: parked.Version,
			ExpectedEpoch:   parked.MutationEpoch,
			ExpectedTip:     parked.Tip,
			ExpectedTree:    parked.Tree,
		})
		if err == nil || !errors.Is(err, ErrPinnedLineInvalid) {
			t.Fatalf("ResumePinnedLine() error = %v", err)
		}
		state := developmentLineStateForTest(t, fixture.manager)
		line := state.DevelopmentLines[pinnedLineTestID]
		if line == nil || line.State != developmentLineParked ||
			state.Workspaces[fixture.workspace.ID].LockedBy != nil {
			t.Fatalf("invalid resume changed inventory: %#v", state)
		}
	})
}

func TestManagerPinnedWorkspaceRejectsOversizedReservationBeforeEffects(t *testing.T) {
	ctx := context.Background()
	source := initSourceRepo(t)
	now := time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &now)
	_, err := manager.AcquirePinned(ctx, PinnedAcquireRequest{
		Repository:     source,
		SourceRef:      "main",
		ExpectedCommit: testGitCommit(t, source, "HEAD"),
		ReservationKey: strings.Repeat("r", 257),
		AgentID:        "main",
	})
	if err == nil || !strings.Contains(err.Error(), "bounded identity") {
		t.Fatalf("AcquirePinned() oversized reservation error = %v", err)
	}
	state := developmentLineStateForTest(t, manager)
	if len(state.Repositories) != 0 || len(state.Workspaces) != 0 ||
		len(state.History) != 0 || len(state.DevelopmentLineHistory) != 0 {
		t.Fatalf("oversized reservation changed inventory: %#v", state)
	}
}

func TestManagerPinnedDevelopmentLineParkIsTerminalOutsideOuterOperation(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/line-terminal-park")
	request := PinnedLineParkRequest{
		Pin:             fixture.pin,
		WorkspaceID:     fixture.workspace.ID,
		LineID:          pinnedLineTestID,
		IntentID:        "pdlnpark_terminal",
		ExpectedVersion: 0,
		MutationEpoch:   1,
		PreviousTip:     fixture.pin.ExpectedCommit,
		Tip:             fixture.pin.ExpectedCommit,
		Tree:            fixture.baseTree,
		NoChanges:       true,
	}
	err := fixture.manager.WithPinnedOperation(ctx, fixture.pin, func(operationContext context.Context) error {
		_, parkErr := fixture.manager.ParkPinnedLine(operationContext, request)
		if parkErr == nil || !errors.Is(parkErr, ErrPinnedLineConflict) {
			t.Fatalf("inherited ParkPinnedLine() error = %v", parkErr)
		}
		workspace := workspaceRecordForTest(t, fixture.manager, fixture.workspace.ID)
		if workspace.LockedBy == nil ||
			workspace.LockedBy.SessionKey != fixture.pin.ReservationKey {
			t.Fatalf("outer mutation reservation was released: %#v", workspace)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.ParkPinnedLine(ctx, request); err != nil {
		t.Fatalf("terminal ParkPinnedLine() error = %v", err)
	}
}

func TestManagerInventoryVersionFourFencesOlderDecoders(t *testing.T) {
	fixture := newPinnedCommitTestFixture(t, "pr-development/version-fence")
	if _, err := os.Stat(filepath.Join(fixture.manager.RootDir(), "inventory.json")); !os.IsNotExist(err) {
		t.Fatalf("mutable inventory JSON remains after SQLite open: %v", err)
	}
	database, err := fixture.manager.openInventoryDatabase(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var schemaVersion int
	if err := database.QueryRow(`PRAGMA user_version`).Scan(&schemaVersion); err != nil ||
		schemaVersion != inventorySchemaVersion {
		t.Fatalf("inventory schema version = %d, %v", schemaVersion, err)
	}
	state := adversarialCloneInventory(t, fixture.manager)
	if state.Version != stateVersion {
		t.Fatalf("domain inventory version = %d", state.Version)
	}
	if _, err := fixture.manager.Stats(context.Background()); err != nil {
		t.Fatalf("version-4 manager failed to reload tagged inventory: %v", err)
	}
}

func TestManagerPinnedWorkspaceIsPrivateBeforeLineAdoption(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedCommitTestFixture(t, "pr-development/pre-adoption-private")
	stats, err := fixture.manager.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.RepositoryCount != 0 || stats.WorkspaceCount != 0 ||
		len(stats.Repositories) != 0 || len(stats.Workspaces) != 0 ||
		len(stats.History) != 0 || stats.TotalSizeBytes != 0 || stats.IgnoredBytes != 0 {
		t.Fatalf("pre-adoption Stats() exposed pinned workspace: %#v", stats)
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
	generic, err := fixture.manager.Acquire(ctx, AcquireRequest{
		Repository: fixture.pin.Repository,
		Ref:        fixture.pin.SourceRef,
		SessionKey: fixture.pin.ReservationKey,
		AgentID:    "generic-agent",
	})
	if err != nil {
		t.Fatalf("Acquire() with controller session collision error = %v", err)
	}
	if generic.ID != fixture.workspace.RepoID || generic.ID == fixture.workspace.ID {
		t.Fatalf("generic/private workspace IDs = %q/%q", generic.ID, fixture.workspace.ID)
	}
	released, err := fixture.manager.ReleaseSession(ctx, ReleaseRequest{
		SessionKey: fixture.pin.ReservationKey,
		AgentID:    "generic-agent",
	})
	if err != nil || len(released) != 1 || released[0].ID != generic.ID {
		t.Fatalf("ReleaseSession() = %#v, %v", released, err)
	}
	private := workspaceRecordForTest(t, fixture.manager, fixture.workspace.ID)
	if private.LockedBy == nil || private.LockedBy.SessionKey != fixture.pin.ReservationKey {
		t.Fatalf("generic release changed pinned controller lock: %#v", private.LockedBy)
	}
}

func TestManagerPinnedDevelopmentLineActivityDoesNotChangeGenericRepositoryTimes(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/line-private-times-first")
	clock := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	fixture.manager.now = func() time.Time { return clock }
	generic, err := fixture.manager.Acquire(ctx, AcquireRequest{
		Repository: fixture.pin.Repository,
		Ref:        fixture.pin.SourceRef,
		SessionKey: "generic/shared-repository",
		AgentID:    "generic-agent",
	})
	if err != nil {
		t.Fatalf("Acquire() generic workspace error = %v", err)
	}
	if generic.ID != fixture.workspace.RepoID || generic.ID == fixture.workspace.ID {
		t.Fatalf(
			"generic/private workspace IDs = %q/%q, want %q/distinct",
			generic.ID,
			fixture.workspace.ID,
			fixture.workspace.RepoID,
		)
	}
	before, err := fixture.manager.Stats(ctx)
	if err != nil || len(before.Repositories) != 1 || before.WorkspaceCount != 1 {
		t.Fatalf("generic Stats() before line activity = %#v, %v", before, err)
	}
	beforeRepository := before.Repositories[0]

	clock = clock.Add(time.Hour)
	parked, err := fixture.manager.ParkPinnedLine(ctx, PinnedLineParkRequest{
		Pin:             fixture.pin,
		WorkspaceID:     fixture.workspace.ID,
		LineID:          pinnedLineTestID,
		IntentID:        "pdlnpark_private_times_first",
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
	clock = clock.Add(time.Hour)
	secondPin := fixture.pin
	secondPin.ReservationKey = "pr-development/line-private-times-second"
	lease, err := fixture.manager.ResumePinnedLine(ctx, PinnedLineResumeRequest{
		Pin:             secondPin,
		WorkspaceID:     fixture.workspace.ID,
		LineID:          pinnedLineTestID,
		ExpectedVersion: parked.Version,
		ExpectedEpoch:   parked.MutationEpoch,
		ExpectedTip:     parked.Tip,
		ExpectedTree:    parked.Tree,
	})
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Hour)
	if _, parkErr := fixture.manager.ParkPinnedLine(ctx, PinnedLineParkRequest{
		Pin:             secondPin,
		WorkspaceID:     fixture.workspace.ID,
		LineID:          pinnedLineTestID,
		IntentID:        "pdlnpark_private_times_second",
		ExpectedVersion: parked.Version,
		MutationEpoch:   lease.MutationEpoch,
		PreviousTip:     parked.Tip,
		Tip:             parked.Tip,
		Tree:            parked.Tree,
		NoChanges:       true,
	}); parkErr != nil {
		t.Fatal(parkErr)
	}
	after, err := fixture.manager.Stats(ctx)
	if err != nil || len(after.Repositories) != 1 || after.WorkspaceCount != 1 {
		t.Fatalf("generic Stats() after line activity = %#v, %v", after, err)
	}
	afterRepository := after.Repositories[0]
	if !afterRepository.FirstSeenAt.Equal(beforeRepository.FirstSeenAt) ||
		!afterRepository.LastSeenAt.Equal(beforeRepository.LastSeenAt) ||
		!afterRepository.LastWorkAt.Equal(beforeRepository.LastWorkAt) {
		t.Fatalf(
			"generic repository times changed from %#v to %#v",
			beforeRepository,
			afterRepository,
		)
	}
}

func TestManagerPinnedDevelopmentLineHistoryHasIndependentRetention(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/line-private-history")
	generic, err := fixture.manager.Acquire(ctx, AcquireRequest{
		Repository: fixture.pin.Repository,
		Ref:        fixture.pin.SourceRef,
		SessionKey: "generic/private-history",
		AgentID:    "generic-agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	baseTime := time.Date(2026, 8, 10, 14, 0, 0, 0, time.UTC)
	adversarialMutateInventory(t, fixture.manager, func(state *storeState) {
		for index := 0; index < historyLimit+25; index++ {
			fixture.manager.addHistoryLocked(
				state,
				baseTime.Add(time.Duration(index)*time.Nanosecond),
				"development_line_retention_test",
				fixture.workspace.RepoID,
				fixture.workspace.ID,
				"",
				"",
				"hidden",
			)
		}
	})
	adversarialInspectInventory(t, fixture.manager, func(state *storeState) {
		if len(state.DevelopmentLineHistory) != historyLimit {
			t.Fatalf(
				"private history length = %d, want %d",
				len(state.DevelopmentLineHistory),
				historyLimit,
			)
		}
		if len(state.History) == 0 {
			t.Fatal("generic history was evicted by private line events")
		}
		for _, entry := range state.History {
			if entry.WorkspaceID != generic.ID {
				t.Fatalf("public history contains private entry: %#v", entry)
			}
		}
	})
	stats, err := fixture.manager.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.History) == 0 {
		t.Fatal("Stats() generic history was evicted by private line events")
	}
	for _, entry := range stats.History {
		if entry.WorkspaceID != generic.ID ||
			strings.HasPrefix(entry.Action, "development_line_") {
			t.Fatalf("Stats() exposed private history: %#v", entry)
		}
	}
}

func TestManagerPinnedDevelopmentLineParkErasesReservationBearer(t *testing.T) {
	ctx := context.Background()
	reservation := "pr-development/raw-bearer-must-disappear"
	fixture := newPinnedLineTestFixture(t, reservation)
	if _, err := fixture.manager.ParkPinnedLine(ctx, PinnedLineParkRequest{
		Pin:             fixture.pin,
		WorkspaceID:     fixture.workspace.ID,
		LineID:          pinnedLineTestID,
		IntentID:        "pdlnpark_erase_reservation_bearer",
		ExpectedVersion: fixture.lease.Version,
		MutationEpoch:   fixture.lease.MutationEpoch,
		PreviousTip:     fixture.lease.Tip,
		Tip:             fixture.lease.Tip,
		Tree:            fixture.lease.Tree,
		NoChanges:       true,
	}); err != nil {
		t.Fatal(err)
	}
	state := developmentLineStateForTest(t, fixture.manager)
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), reservation) {
		t.Fatal("parked inventory retained the raw mutation reservation")
	}
	for _, entry := range state.DevelopmentLineHistory {
		if entry.SessionKey != "" || entry.AgentID != "" {
			t.Fatalf("private history retained controller identity: %#v", entry)
		}
	}
}

func TestManagerReleasePinnedPreservesWithoutReservationBearer(t *testing.T) {
	ctx := context.Background()
	reservation := "pr-development/readable-reservation-bearer"
	fixture := newPinnedCommitTestFixture(t, reservation)
	if err := os.WriteFile(
		filepath.Join(fixture.workspace.Path, "preserve.txt"),
		[]byte("preserve privately\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	released, err := fixture.manager.ReleasePinned(ctx, PinnedReleaseRequest{
		ReservationKey: reservation,
		AgentID:        fixture.pin.AgentID,
	})
	if err != nil || len(released) != 1 {
		t.Fatalf("ReleasePinned() = %#v, %v", released, err)
	}
	readableSegment := safeBranchSegment(reservation)
	if !strings.Contains(released[0].PreservedBranch, "/pinned-") ||
		strings.Contains(released[0].PreservedBranch, reservation) ||
		strings.Contains(released[0].PreservedBranch, readableSegment) {
		t.Fatalf("pinned preservation branch exposed reservation: %q", released[0].PreservedBranch)
	}
	commitMessage, messageErr := runGit(
		ctx,
		fixture.workspace.Path,
		"show",
		"-s",
		"--format=%B",
		released[0].PreservedBranch,
	)
	if messageErr != nil {
		t.Fatal(messageErr)
	}
	if strings.Contains(commitMessage, reservation) ||
		strings.Contains(commitMessage, readableSegment) {
		t.Fatalf("pinned preservation commit exposed reservation: %q", commitMessage)
	}
	state := developmentLineStateForTest(t, fixture.manager)
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), reservation) ||
		strings.Contains(string(encoded), readableSegment) {
		t.Fatal("released pinned inventory retained a reservation bearer")
	}
	for _, entry := range state.DevelopmentLineHistory {
		if entry.SessionKey != "" || entry.AgentID != "" {
			t.Fatalf("private pinned history retained controller identity: %#v", entry)
		}
	}
}

func TestManagerPinnedDevelopmentLineAdoptionCannotEvictGenericHistory(t *testing.T) {
	ctx := context.Background()
	source := initSourceRepo(t)
	now := time.Date(2026, 8, 10, 16, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &now)
	publicHistory := make([]HistoryEntry, historyLimit)
	for index := range publicHistory {
		publicHistory[index] = HistoryEntry{
			ID:          fmt.Sprintf("public-%04d", index),
			Time:        now.Add(time.Duration(index) * time.Nanosecond),
			Action:      "generic_retention_test",
			WorkspaceID: "generic-visible",
		}
	}
	adversarialMutateInventory(t, manager, func(state *storeState) {
		state.History = append([]HistoryEntry(nil), publicHistory...)
	})
	pin := PinnedAcquireRequest{
		Repository:     source,
		SourceRef:      "main",
		ExpectedCommit: testGitCommit(t, source, "HEAD"),
		ReservationKey: "pr-development/private-history-adoption",
		AgentID:        "main",
	}
	workspace, err := manager.AcquirePinned(ctx, pin)
	if err != nil {
		t.Fatal(err)
	}
	tree := testGitObject(t, workspace.Path, "rev-parse", "HEAD^{tree}")
	lease, err := manager.AdoptPinnedLine(ctx, PinnedLineAdoptRequest{
		Pin:          pin,
		WorkspaceID:  workspace.ID,
		LineID:       pinnedLineTestID,
		ExpectedTree: tree,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, parkErr := manager.ParkPinnedLine(ctx, PinnedLineParkRequest{
		Pin:             pin,
		WorkspaceID:     workspace.ID,
		LineID:          pinnedLineTestID,
		IntentID:        "pdlnpark_private_history_adoption",
		ExpectedVersion: lease.Version,
		MutationEpoch:   lease.MutationEpoch,
		PreviousTip:     lease.Tip,
		Tip:             lease.Tip,
		Tree:            lease.Tree,
		NoChanges:       true,
	}); parkErr != nil {
		t.Fatal(parkErr)
	}
	adversarialInspectInventory(t, manager, func(state *storeState) {
		if len(state.History) != len(publicHistory) {
			t.Fatalf(
				"generic history length = %d, want %d",
				len(state.History),
				len(publicHistory),
			)
		}
		for index := range publicHistory {
			if state.History[index] != publicHistory[index] {
				t.Fatalf("generic history entry %d changed", index)
			}
		}
	})
	stats, err := manager.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.History) != historyLimit {
		t.Fatalf("Stats() history length = %d, want %d", len(stats.History), historyLimit)
	}
}

func developmentLineStateForTest(t *testing.T, manager *Manager) *storeState {
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
	return state
}
