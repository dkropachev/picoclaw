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

func TestPinnedLineReservationRecoveryJSONIsPrivate(t *testing.T) {
	adoptJSON, err := json.Marshal(PinnedLineAdoptRecoveryRequest{
		IntentID:                  "private-adopt-intent",
		ReplacementReservationKey: "private-adopt-replacement",
		RequireSuspensionCapacity: true,
	})
	if err != nil || string(adoptJSON) != "{}" {
		t.Fatalf("adopt recovery JSON = %q, %v", adoptJSON, err)
	}
	resumeJSON, err := json.Marshal(PinnedLineResumeRecoveryRequest{
		IntentID:                  "private-resume-intent",
		ReplacementReservationKey: "private-resume-replacement",
		RequireSuspensionCapacity: true,
	})
	if err != nil || string(resumeJSON) != "{}" {
		t.Fatalf("resume recovery JSON = %q, %v", resumeJSON, err)
	}
	resultJSON, err := json.Marshal(PinnedLineReservationRecoveryResult{
		WorkspaceID:    "private-workspace",
		Version:        1,
		MutationEpoch:  2,
		Tip:            strings.Repeat("a", 40),
		Tree:           strings.Repeat("b", 40),
		RotationHash:   strings.Repeat("c", 64),
		AlreadyRotated: true,
	})
	if err != nil || string(resultJSON) != "{}" {
		t.Fatalf("line recovery result JSON = %q, %v", resultJSON, err)
	}
}

func TestManagerRecoverPinnedLineAdoptReservationPreEffectAndReplay(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedCommitTestFixture(t, "pr-development/adopt-recovery-old")
	baseTree := testGitObject(t, fixture.workspace.Path, "rev-parse", "HEAD^{tree}")
	request := PinnedLineAdoptRecoveryRequest{
		Adopt: PinnedLineAdoptRequest{
			Pin:          fixture.pin,
			WorkspaceID:  fixture.workspace.ID,
			LineID:       pinnedLineTestID,
			ExpectedTree: baseTree,
		},
		IntentID:                  "pdlr_adopt_pre_effect",
		ReplacementReservationKey: "pr-development/adopt-recovery-fresh",
	}
	headBefore := testGitCommit(t, fixture.workspace.Path, "HEAD")
	indexBefore := testGitObject(t, fixture.workspace.Path, "write-tree")
	statusBefore, err := runGit(ctx, fixture.workspace.Path, "status", "--porcelain=v1")
	if err != nil {
		t.Fatal(err)
	}

	recovered, err := fixture.manager.RecoverPinnedLineAdoptReservation(ctx, request)
	if err != nil {
		t.Fatalf("RecoverPinnedLineAdoptReservation() error = %v", err)
	}
	assertPinnedLineRecoveryResult(
		t,
		recovered,
		fixture.workspace.ID,
		0,
		1,
		fixture.pin.ExpectedCommit,
		baseTree,
		false,
	)
	if headAfter := testGitCommit(t, fixture.workspace.Path, "HEAD"); headAfter != headBefore {
		t.Fatalf("adoption recovery changed HEAD from %q to %q", headBefore, headAfter)
	}
	if indexAfter := testGitObject(t, fixture.workspace.Path, "write-tree"); indexAfter != indexBefore {
		t.Fatalf("adoption recovery changed index from %q to %q", indexBefore, indexAfter)
	}
	statusAfter, err := runGit(ctx, fixture.workspace.Path, "status", "--porcelain=v1")
	if err != nil || statusAfter != statusBefore {
		t.Fatalf("adoption recovery status = %q, %v; want %q", statusAfter, err, statusBefore)
	}
	if retained := testGitCommit(
		t,
		fixture.workspace.Path,
		"refs/heads/"+developmentLineBranch(pinnedLineTestID),
	); retained != fixture.pin.ExpectedCommit {
		t.Fatalf("recovered retained ref = %q", retained)
	}
	adversarialInspectInventory(t, fixture.manager, func(state *storeState) {
		line := state.DevelopmentLines[pinnedLineTestID]
		records := state.PinnedReservationRotations[fixture.workspace.ID]
		if line == nil || line.State != developmentLineMutating ||
			line.MutationReservationHash != developmentLineReservationHash(
				request.ReplacementReservationKey,
			) || len(records) != 1 || records[0].LineID != "" ||
			records[0].RecordHash != recovered.RotationHash {
			t.Fatalf("recovered adoption inventory = line %#v, records %#v", line, records)
		}
	})
	assertPinnedRecoveryOldReservationRevoked(
		t,
		fixture.manager,
		fixture.pin,
		fixture.workspace.ID,
	)

	restarted, err := NewManager(fixture.manager.opts)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := restarted.RecoverPinnedLineAdoptReservation(ctx, request)
	if err != nil {
		t.Fatalf("replayed adoption recovery error = %v", err)
	}
	if !replayed.AlreadyRotated || replayed.RotationHash != recovered.RotationHash ||
		replayed.WorkspaceID != recovered.WorkspaceID ||
		replayed.MutationEpoch != recovered.MutationEpoch {
		t.Fatalf("replayed adoption recovery = %#v, first %#v", replayed, recovered)
	}
}

func TestManagerRecoverPinnedLineAdoptReservationFinishesDurableUnboundRotation(
	t *testing.T,
) {
	ctx := context.Background()
	fixture := newPinnedCommitTestFixture(t, "pr-development/adopt-staged-old")
	baseTree := testGitObject(t, fixture.workspace.Path, "rev-parse", "HEAD^{tree}")
	request := PinnedLineAdoptRecoveryRequest{
		Adopt: PinnedLineAdoptRequest{
			Pin:          fixture.pin,
			WorkspaceID:  fixture.workspace.ID,
			LineID:       pinnedLineTestID,
			ExpectedTree: baseTree,
		},
		IntentID:                  "pdlr_adopt_staged_rotation",
		ReplacementReservationKey: "pr-development/adopt-staged-fresh",
	}
	rotation, err := fixture.manager.RotatePinnedReservation(
		ctx,
		pinnedLineAdoptRecoveryRotationRequest(request, false),
	)
	if err != nil {
		t.Fatalf("stage unbound rotation: %v", err)
	}
	if rotation.Bound {
		t.Fatalf("staged rotation = %#v, want unbound", rotation)
	}

	recovered, err := fixture.manager.RecoverPinnedLineAdoptReservation(ctx, request)
	if err != nil {
		t.Fatalf("finish staged adoption recovery: %v", err)
	}
	assertPinnedLineRecoveryResult(
		t,
		recovered,
		fixture.workspace.ID,
		0,
		1,
		fixture.pin.ExpectedCommit,
		baseTree,
		true,
	)
	if recovered.RotationHash != rotation.RotationHash {
		t.Fatalf(
			"finished adoption rotation hash = %q, want %q",
			recovered.RotationHash,
			rotation.RotationHash,
		)
	}
	replayed, err := fixture.manager.RecoverPinnedLineAdoptReservation(ctx, request)
	if err != nil || !replayed.AlreadyRotated ||
		replayed.RotationHash != rotation.RotationHash {
		t.Fatalf("post-adoption unbound replay = %#v, %v", replayed, err)
	}
}

func TestManagerRecoverPinnedLineAdoptReservationPostEffect(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/adopt-post-old")
	request := PinnedLineAdoptRecoveryRequest{
		Adopt: PinnedLineAdoptRequest{
			Pin:          fixture.pin,
			WorkspaceID:  fixture.workspace.ID,
			LineID:       pinnedLineTestID,
			ExpectedTree: fixture.baseTree,
		},
		IntentID:                  "pdlr_adopt_post_effect",
		ReplacementReservationKey: "pr-development/adopt-post-fresh",
	}
	recovered, err := fixture.manager.RecoverPinnedLineAdoptReservation(ctx, request)
	if err != nil {
		t.Fatalf("post-effect adoption recovery: %v", err)
	}
	assertPinnedLineRecoveryResult(
		t,
		recovered,
		fixture.workspace.ID,
		0,
		1,
		fixture.pin.ExpectedCommit,
		fixture.baseTree,
		false,
	)
	adversarialInspectInventory(t, fixture.manager, func(state *storeState) {
		records := state.PinnedReservationRotations[fixture.workspace.ID]
		if len(records) != 1 || records[0].LineID != pinnedLineTestID ||
			records[0].RecordHash != recovered.RotationHash {
			t.Fatalf("post-effect adoption records = %#v", records)
		}
	})
}

func TestManagerRecoverPinnedLineAdoptReservationPostEffectRejectsGitDrift(
	t *testing.T,
) {
	for _, testCase := range pinnedLineRecoveryGitDriftCases() {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newPinnedLineTestFixture(
				t,
				"pr-development/adopt-post-drift-"+testCase.name,
			)
			request := PinnedLineAdoptRecoveryRequest{
				Adopt: PinnedLineAdoptRequest{
					Pin:          fixture.pin,
					WorkspaceID:  fixture.workspace.ID,
					LineID:       pinnedLineTestID,
					ExpectedTree: fixture.baseTree,
				},
				IntentID: "pdlr_adopt_post_drift_" + strings.ReplaceAll(
					testCase.name,
					"-",
					"_",
				),
				ReplacementReservationKey: "pr-development/adopt-post-drift-fresh-" +
					testCase.name,
			}
			testCase.mutate(t, fixture)

			if _, err := fixture.manager.RecoverPinnedLineAdoptReservation(
				ctx,
				request,
			); err == nil || !errors.Is(err, ErrPinnedLineConflict) {
				t.Fatalf("post-effect adoption recovery drift error = %v", err)
			}
			assertPinnedLineRecoveryDidNotRotate(
				t,
				fixture,
				fixture.pin,
				fixture.lease.Version,
				fixture.lease.MutationEpoch,
			)
		})
	}
}

func TestManagerRecoverPinnedLineResumeReservationPreEffectAndReplay(t *testing.T) {
	ctx := context.Background()
	fixture, request := newPinnedLineResumeRecoveryFixture(
		t,
		"pr-development/resume-recovery-pre",
	)
	before := snapshotPinnedRecoveryGitState(t, fixture)
	recovered, err := fixture.manager.RecoverPinnedLineResumeReservation(ctx, request)
	if err != nil {
		t.Fatalf("RecoverPinnedLineResumeReservation() error = %v", err)
	}
	assertPinnedLineRecoveryResult(
		t,
		recovered,
		fixture.workspace.ID,
		request.Resume.ExpectedVersion,
		request.Resume.ExpectedEpoch+1,
		request.Resume.ExpectedTip,
		request.Resume.ExpectedTree,
		false,
	)
	after := snapshotPinnedRecoveryGitState(t, fixture)
	if after != before {
		t.Fatalf("pre-effect resume changed Git state: before %#v, after %#v", before, after)
	}
	adversarialInspectInventory(t, fixture.manager, func(state *storeState) {
		line := state.DevelopmentLines[pinnedLineTestID]
		records := state.PinnedReservationRotations[fixture.workspace.ID]
		if line == nil || line.State != developmentLineMutating ||
			line.MutationEpoch != request.Resume.ExpectedEpoch+1 ||
			line.MutationReservationHash != developmentLineReservationHash(
				request.ReplacementReservationKey,
			) || len(records) != 1 || records[0].LineID != pinnedLineTestID ||
			records[0].RecordHash != recovered.RotationHash {
			t.Fatalf("pre-effect resume inventory = line %#v, records %#v", line, records)
		}
	})
	assertPinnedRecoveryOldReservationRevoked(
		t,
		fixture.manager,
		request.Resume.Pin,
		fixture.workspace.ID,
	)

	restarted, err := NewManager(fixture.manager.opts)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := restarted.RecoverPinnedLineResumeReservation(ctx, request)
	if err != nil || !replayed.AlreadyRotated ||
		replayed.RotationHash != recovered.RotationHash {
		t.Fatalf("replayed resume recovery = %#v, %v", replayed, err)
	}
	freshPin := request.Resume.Pin
	freshPin.ReservationKey = request.ReplacementReservationKey
	parked, err := restarted.ParkPinnedLine(ctx, PinnedLineParkRequest{
		Pin:             freshPin,
		WorkspaceID:     fixture.workspace.ID,
		LineID:          pinnedLineTestID,
		IntentID:        "pdlnpark_recovered_resume",
		ExpectedVersion: recovered.Version,
		MutationEpoch:   recovered.MutationEpoch,
		PreviousTip:     recovered.Tip,
		Tip:             recovered.Tip,
		Tree:            recovered.Tree,
		NoChanges:       true,
	})
	if err != nil || parked.Version != recovered.Version+1 {
		t.Fatalf("park recovered resume = %#v, %v", parked, err)
	}
}

func TestManagerRecoverPinnedLineResumeReservationPostEffect(t *testing.T) {
	ctx := context.Background()
	fixture, request := newPinnedLineResumeRecoveryFixture(
		t,
		"pr-development/resume-recovery-post",
	)
	resumed, err := fixture.manager.ResumePinnedLine(ctx, request.Resume)
	if err != nil {
		t.Fatalf("stage post-effect ResumePinnedLine(): %v", err)
	}
	before := snapshotPinnedRecoveryGitState(t, fixture)
	recovered, err := fixture.manager.RecoverPinnedLineResumeReservation(ctx, request)
	if err != nil {
		t.Fatalf("post-effect resume recovery: %v", err)
	}
	assertPinnedLineRecoveryResult(
		t,
		recovered,
		fixture.workspace.ID,
		resumed.Version,
		resumed.MutationEpoch,
		resumed.Tip,
		resumed.Tree,
		false,
	)
	after := snapshotPinnedRecoveryGitState(t, fixture)
	if after != before {
		t.Fatalf("post-effect resume changed Git state: before %#v, after %#v", before, after)
	}
	resumes := 0
	adversarialInspectInventory(t, fixture.manager, func(state *storeState) {
		for _, entry := range state.DevelopmentLineHistory {
			if entry.WorkspaceID == fixture.workspace.ID &&
				entry.Action == "development_line_resumed" {
				resumes++
			}
		}
	})
	if resumes != 1 {
		t.Fatalf("post-effect recovery recorded %d resume events, want 1", resumes)
	}
}

func TestManagerRecoverPinnedLineResumeReservationPostEffectRejectsGitDrift(
	t *testing.T,
) {
	for _, testCase := range pinnedLineRecoveryGitDriftCases() {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			fixture, request := newPinnedLineResumeRecoveryFixture(
				t,
				"pr-development/resume-post-drift-"+testCase.name,
			)
			resumed, err := fixture.manager.ResumePinnedLine(ctx, request.Resume)
			if err != nil {
				t.Fatalf("stage post-effect ResumePinnedLine(): %v", err)
			}
			testCase.mutate(t, fixture)

			if _, err := fixture.manager.RecoverPinnedLineResumeReservation(
				ctx,
				request,
			); err == nil || !errors.Is(err, ErrPinnedLineConflict) {
				t.Fatalf("post-effect resume recovery drift error = %v", err)
			}
			assertPinnedLineRecoveryDidNotRotate(
				t,
				fixture,
				request.Resume.Pin,
				resumed.Version,
				resumed.MutationEpoch,
			)
		})
	}
}

func TestManagerRecoverPinnedLineResumeReservationWaitsAndFencesStaleResume(
	t *testing.T,
) {
	ctx := context.Background()
	fixture, request := newPinnedLineResumeRecoveryFixture(
		t,
		"pr-development/resume-recovery-race",
	)
	entered := make(chan struct{})
	release := make(chan struct{})
	staleDone := make(chan error, 1)
	go func() {
		staleDone <- fixture.manager.WithPinnedOperation(
			ctx,
			request.Resume.Pin,
			func(operationContext context.Context) error {
				close(entered)
				<-release
				_, resumeErr := fixture.manager.ResumePinnedLine(
					operationContext,
					request.Resume,
				)
				return resumeErr
			},
		)
	}()
	<-entered
	restarted, err := NewManager(fixture.manager.opts)
	if err != nil {
		t.Fatal(err)
	}
	type recoveryResult struct {
		result PinnedLineReservationRecoveryResult
		err    error
	}
	recoveryDone := make(chan recoveryResult, 1)
	go func() {
		result, recoveryErr := restarted.RecoverPinnedLineResumeReservation(ctx, request)
		recoveryDone <- recoveryResult{result: result, err: recoveryErr}
	}()
	select {
	case completed := <-recoveryDone:
		t.Fatalf("recovery did not wait for stale operation: %#v, %v", completed.result,
			completed.err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-staleDone; err != nil {
		t.Fatalf("stale exact resume error = %v", err)
	}
	select {
	case completed := <-recoveryDone:
		if completed.err != nil || completed.result.MutationEpoch !=
			request.Resume.ExpectedEpoch+1 {
			t.Fatalf("recovery after stale resume = %#v, %v", completed.result, completed.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resume recovery deadlocked after stale operation returned")
	}
	assertPinnedRecoveryOldReservationRevoked(
		t,
		restarted,
		request.Resume.Pin,
		fixture.workspace.ID,
	)
}

func TestManagerPinnedLineReservationRecoveryRejectsChangedReplay(t *testing.T) {
	ctx := context.Background()
	fixture, request := newPinnedLineResumeRecoveryFixture(
		t,
		"pr-development/resume-recovery-conflict",
	)
	recovered, err := fixture.manager.RecoverPinnedLineResumeReservation(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	changed := request
	changed.ReplacementReservationKey = "pr-development/resume-recovery-other-fresh"
	if _, changedErr := fixture.manager.RecoverPinnedLineResumeReservation(
		ctx,
		changed,
	); changedErr == nil || !errors.Is(changedErr, ErrPinnedLineConflict) {
		t.Fatalf("changed replacement replay error = %v", changedErr)
	}
	changed = request
	changed.Resume.ExpectedTree = strings.Repeat("a", len(request.Resume.ExpectedTree))
	if changed.Resume.ExpectedTree == request.Resume.ExpectedTree {
		changed.Resume.ExpectedTree = strings.Repeat("b", len(request.Resume.ExpectedTree))
	}
	if _, changedErr := fixture.manager.RecoverPinnedLineResumeReservation(
		ctx,
		changed,
	); changedErr == nil || !errors.Is(changedErr, ErrPinnedLineConflict) {
		t.Fatalf("changed fence replay error = %v", changedErr)
	}
	changed = request
	changed.IntentID = "pdlr_resume_new_intent_after_recovery"
	if _, changedErr := fixture.manager.RecoverPinnedLineResumeReservation(
		ctx,
		changed,
	); changedErr == nil || !errors.Is(changedErr, ErrPinnedLineConflict) {
		t.Fatalf("reused old bearer error = %v", changedErr)
	}
	replayed, err := fixture.manager.RecoverPinnedLineResumeReservation(ctx, request)
	if err != nil || !replayed.AlreadyRotated ||
		replayed.RotationHash != recovered.RotationHash {
		t.Fatalf("exact replay after conflicts = %#v, %v", replayed, err)
	}
}

type pinnedRecoveryGitState struct {
	head   string
	index  string
	status string
	ref    string
}

type pinnedLineRecoveryGitDriftCase struct {
	name   string
	mutate func(*testing.T, pinnedLineTestFixture)
}

func pinnedLineRecoveryGitDriftCases() []pinnedLineRecoveryGitDriftCase {
	return []pinnedLineRecoveryGitDriftCase{
		{
			name: "dirty-worktree",
			mutate: func(t *testing.T, fixture pinnedLineTestFixture) {
				t.Helper()
				if err := os.WriteFile(
					filepath.Join(fixture.workspace.Path, "README.md"),
					[]byte("# recovery drift\n"),
					0o644,
				); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "attached-head",
			mutate: func(t *testing.T, fixture pinnedLineTestFixture) {
				t.Helper()
				if _, err := runGit(
					context.Background(),
					fixture.workspace.Path,
					"switch",
					"-c",
					"recovery-drift",
				); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "changed-retained-ref",
			mutate: func(t *testing.T, fixture pinnedLineTestFixture) {
				t.Helper()
				tree := testGitObject(t, fixture.workspace.Path, "rev-parse", "HEAD^{tree}")
				driftCommit, err := runGit(
					context.Background(),
					fixture.workspace.Path,
					"commit-tree",
					tree,
					"-p",
					"HEAD",
					"-m",
					"retained ref drift",
				)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := runGit(
					context.Background(),
					fixture.workspace.Path,
					"update-ref",
					"refs/heads/"+developmentLineBranch(pinnedLineTestID),
					strings.TrimSpace(driftCommit),
				); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "git-control-plane",
			mutate: func(t *testing.T, fixture pinnedLineTestFixture) {
				t.Helper()
				if err := os.WriteFile(
					filepath.Join(fixture.workspace.Path, ".git", "info", "attributes"),
					[]byte("* -text\n"),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
}

func assertPinnedLineRecoveryDidNotRotate(
	t *testing.T,
	fixture pinnedLineTestFixture,
	oldPin PinnedAcquireRequest,
	expectedVersion, expectedEpoch int64,
) {
	t.Helper()
	adversarialInspectInventory(t, fixture.manager, func(state *storeState) {
		line := state.DevelopmentLines[pinnedLineTestID]
		rotations := state.PinnedReservationRotations[fixture.workspace.ID]
		workspace := state.Workspaces[fixture.workspace.ID]
		if line == nil || line.Version != expectedVersion ||
			line.MutationEpoch != expectedEpoch ||
			line.MutationReservationHash != developmentLineReservationHash(
				oldPin.ReservationKey,
			) || len(rotations) != 0 || workspace == nil || workspace.LockedBy == nil ||
			workspace.LockedBy.SessionKey != oldPin.ReservationKey {
			t.Fatalf(
				"failed recovery mutated inventory: line %#v, rotations %#v, workspace %#v",
				line,
				rotations,
				workspace,
			)
		}
	})
}

func newPinnedLineResumeRecoveryFixture(
	t *testing.T,
	prefix string,
) (pinnedLineTestFixture, PinnedLineResumeRecoveryRequest) {
	t.Helper()
	fixture := newPinnedLineTestFixture(t, prefix+"-initial")
	parked, err := fixture.manager.ParkPinnedLine(
		context.Background(),
		PinnedLineParkRequest{
			Pin:             fixture.pin,
			WorkspaceID:     fixture.workspace.ID,
			LineID:          pinnedLineTestID,
			IntentID:        "pdlnpark_" + strings.ReplaceAll(prefix, "/", "_"),
			ExpectedVersion: fixture.lease.Version,
			MutationEpoch:   fixture.lease.MutationEpoch,
			PreviousTip:     fixture.lease.Tip,
			Tip:             fixture.lease.Tip,
			Tree:            fixture.lease.Tree,
			NoChanges:       true,
		},
	)
	if err != nil {
		t.Fatalf("park resume recovery fixture: %v", err)
	}
	oldPin := fixture.pin
	oldPin.ReservationKey = prefix + "-old"
	return fixture, PinnedLineResumeRecoveryRequest{
		Resume: PinnedLineResumeRequest{
			Pin:             oldPin,
			WorkspaceID:     fixture.workspace.ID,
			LineID:          pinnedLineTestID,
			ExpectedVersion: parked.Version,
			ExpectedEpoch:   parked.MutationEpoch,
			ExpectedTip:     parked.Tip,
			ExpectedTree:    parked.Tree,
		},
		IntentID:                  "pdlr_" + strings.ReplaceAll(prefix, "/", "_"),
		ReplacementReservationKey: prefix + "-fresh",
	}
}

func snapshotPinnedRecoveryGitState(
	t *testing.T,
	fixture pinnedLineTestFixture,
) pinnedRecoveryGitState {
	t.Helper()
	status, err := runGit(
		context.Background(),
		fixture.workspace.Path,
		"status",
		"--porcelain=v1",
	)
	if err != nil {
		t.Fatal(err)
	}
	return pinnedRecoveryGitState{
		head:   testGitCommit(t, fixture.workspace.Path, "HEAD"),
		index:  testGitObject(t, fixture.workspace.Path, "write-tree"),
		status: status,
		ref: testGitCommit(
			t,
			fixture.workspace.Path,
			"refs/heads/"+developmentLineBranch(pinnedLineTestID),
		),
	}
}

func assertPinnedLineRecoveryResult(
	t *testing.T,
	result PinnedLineReservationRecoveryResult,
	workspaceID string,
	version, epoch int64,
	tip, tree string,
	replay bool,
) {
	t.Helper()
	if result.WorkspaceID != workspaceID || result.Version != version ||
		result.MutationEpoch != epoch || result.Tip != tip || result.Tree != tree ||
		!validLowerHex(result.RotationHash, 64) || result.AlreadyRotated != replay {
		t.Fatalf("pinned line recovery result = %#v", result)
	}
}

func assertPinnedRecoveryOldReservationRevoked(
	t *testing.T,
	manager *Manager,
	oldPin PinnedAcquireRequest,
	workspaceID string,
) {
	t.Helper()
	called := false
	err := manager.WithPinnedOperation(
		context.Background(),
		oldPin,
		func(context.Context) error {
			called = true
			return nil
		},
	)
	if err == nil || !errors.Is(err, ErrPinnedLineConflict) || called {
		t.Fatalf("stale WithPinnedOperation() = called %v, error %v", called, err)
	}
	if _, acquireErr := manager.AcquirePinned(context.Background(), oldPin); acquireErr == nil {
		t.Fatalf("stale AcquirePinned() for workspace %q error = nil", workspaceID)
	}
}
