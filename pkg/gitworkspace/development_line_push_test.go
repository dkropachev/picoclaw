package gitworkspace

import (
	"bytes"
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

type pinnedLinePushTestFixture struct {
	pinnedLineTestFixture
	parked  PinnedLineParkResult
	request PinnedLinePushRequest
}

func newPinnedLinePushTestFixture(
	t *testing.T,
	reservation string,
) pinnedLinePushTestFixture {
	t.Helper()
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, reservation)
	fixture.manager.pinnedLinePushTransport = func(repository string) (string, error) {
		if repository != fixture.pin.Repository {
			return "", errors.New("unexpected test push repository")
		}
		return repository, nil
	}
	if _, configErr := runGit(
		ctx,
		fixture.pin.Repository,
		"config",
		"receive.denyCurrentBranch",
		"ignore",
	); configErr != nil {
		t.Fatalf("configure source push target: %v", configErr)
	}
	commit := fixture.commitChange(
		t,
		"push.txt",
		"retained line push\n",
		"pdcmt_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"Prepare retained line push",
		time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC),
	)
	parkIntent := "pdlnpark_push_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	parked, parkErr := fixture.manager.ParkPinnedLine(ctx, PinnedLineParkRequest{
		Pin:             fixture.pin,
		WorkspaceID:     fixture.workspace.ID,
		LineID:          pinnedLineTestID,
		IntentID:        parkIntent,
		ExpectedVersion: fixture.lease.Version,
		MutationEpoch:   fixture.lease.MutationEpoch,
		PreviousTip:     fixture.lease.Tip,
		Tip:             commit.Commit,
		Tree:            commit.Tree,
	})
	if parkErr != nil {
		t.Fatalf("ParkPinnedLine() error = %v", parkErr)
	}
	return pinnedLinePushTestFixture{
		pinnedLineTestFixture: fixture,
		parked:                parked,
		request: PinnedLinePushRequest{
			Repository:            fixture.pin.Repository,
			SourceRef:             fixture.pin.SourceRef,
			ExpectedSourceCommit:  fixture.pin.ExpectedCommit,
			WorkspaceID:           fixture.workspace.ID,
			LineID:                pinnedLineTestID,
			ExpectedVersion:       parked.Version,
			ExpectedMutationEpoch: parked.MutationEpoch,
			ExpectedParkIntentID:  parkIntent,
			ExpectedBase:          parked.PreviousTip,
			ExpectedTip:           parked.Tip,
			ExpectedTree:          parked.Tree,
			ExpectedRemoteTip:     fixture.pin.ExpectedCommit,
		},
	}
}

func TestManagerPushPinnedLineCASReplayAndNoInventoryMutation(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedLinePushTestFixture(
		t,
		"pr-development/push-success",
	)
	inventoryBefore, readErr := os.ReadFile(fixture.manager.statePath())
	if readErr != nil {
		t.Fatal(readErr)
	}
	result, pushErr := fixture.manager.PushPinnedLine(ctx, fixture.request)
	if pushErr != nil {
		t.Fatalf("PushPinnedLine() error = %v", pushErr)
	}
	assertPinnedLinePushResult(
		t,
		result,
		fixture,
		PinnedLinePushApplied,
	)
	if remoteTip := testGitCommit(
		t,
		fixture.pin.Repository,
		"refs/heads/"+fixture.pin.SourceRef,
	); remoteTip != fixture.parked.Tip {
		t.Fatalf("remote tip = %q, want %q", remoteTip, fixture.parked.Tip)
	}

	replayMarker := filepath.Join(t.TempDir(), "replay-push-ran")
	installPinnedLinePushHook(
		t,
		fixture.pin.Repository,
		"pre-receive",
		fmt.Sprintf("#!/bin/sh\n: > %q\nexit 1\n", replayMarker),
	)
	replayed, replayErr := fixture.manager.PushPinnedLine(ctx, fixture.request)
	if replayErr != nil {
		t.Fatalf("replayed PushPinnedLine() error = %v", replayErr)
	}
	assertPinnedLinePushResult(
		t,
		replayed,
		fixture,
		PinnedLinePushAlreadyCurrent,
	)
	if _, markerErr := os.Stat(replayMarker); !os.IsNotExist(markerErr) {
		t.Fatalf("exact replay invoked a second push: %v", markerErr)
	}
	inventoryAfter, readErr := os.ReadFile(fixture.manager.statePath())
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(inventoryBefore, inventoryAfter) {
		t.Fatal("PushPinnedLine() mutated manager inventory")
	}
	state := developmentLineStateForTest(t, fixture.manager)
	line := state.DevelopmentLines[pinnedLineTestID]
	workspace := state.Workspaces[fixture.workspace.ID]
	if line == nil || line.State != developmentLineParked ||
		line.Version != fixture.parked.Version || line.PendingParkSet ||
		workspace == nil || workspace.LockedBy != nil {
		t.Fatalf("pushed retained line changed ownership: %#v, %#v", line, workspace)
	}
}

func TestPinnedLinePushTypesDoNotMarshal(t *testing.T) {
	tests := map[string]any{
		"request": PinnedLinePushRequest{
			Repository:            "git@example.com:owner/repository.git",
			SourceRef:             "feature",
			ExpectedSourceCommit:  strings.Repeat("a", 40),
			WorkspaceID:           "workspace",
			LineID:                "line",
			ExpectedVersion:       1,
			ExpectedMutationEpoch: 1,
			ExpectedParkIntentID:  "park",
			ExpectedBase:          strings.Repeat("b", 40),
			ExpectedTip:           strings.Repeat("c", 40),
			ExpectedTree:          strings.Repeat("d", 40),
			ExpectedRemoteTip:     strings.Repeat("e", 40),
		},
		"result": PinnedLinePushResult{
			WorkspaceID:       "workspace",
			Version:           1,
			MutationEpoch:     1,
			ParkIntentID:      "park",
			BaseCommit:        strings.Repeat("a", 40),
			Tip:               strings.Repeat("b", 40),
			Tree:              strings.Repeat("c", 40),
			RemoteRef:         "refs/heads/feature",
			ExpectedRemoteTip: strings.Repeat("a", 40),
			RemoteTip:         strings.Repeat("b", 40),
			Disposition:       PinnedLinePushApplied,
			WorkspaceClean:    true,
		},
	}
	for name, value := range tests {
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatalf("marshal %s: %v", name, marshalErr)
		}
		if string(encoded) != "{}" {
			t.Fatalf("marshal %s = %s, want {}", name, encoded)
		}
	}
}

func TestManagerPushPinnedLineRejectsProductionLocalTransport(t *testing.T) {
	fixture := newPinnedLinePushTestFixture(
		t,
		"pr-development/push-local-transport",
	)
	fixture.manager.pinnedLinePushTransport = nil
	result, pushErr := fixture.manager.PushPinnedLine(
		context.Background(),
		fixture.request,
	)
	if result != (PinnedLinePushResult{}) ||
		!errors.Is(pushErr, ErrPinnedLineInvalid) {
		t.Fatalf("PushPinnedLine() = %#v, %v; want invalid transport", result, pushErr)
	}
	if remoteTip := testGitCommit(
		t,
		fixture.pin.Repository,
		"refs/heads/"+fixture.pin.SourceRef,
	); remoteTip != fixture.pin.ExpectedCommit {
		t.Fatalf("invalid local transport changed remote to %q", remoteTip)
	}
}

func TestManagerPushPinnedLineRejectsStaleRemoteFence(t *testing.T) {
	fixture := newPinnedLinePushTestFixture(
		t,
		"pr-development/push-conflict",
	)
	request := fixture.request
	request.ExpectedRemoteTip = request.ExpectedTip
	_, pushErr := fixture.manager.PushPinnedLine(context.Background(), request)
	if !errors.Is(pushErr, ErrPinnedLineConflict) ||
		errors.Is(pushErr, ErrPinnedLinePushOutcomeUnknown) {
		t.Fatalf("PushPinnedLine() error = %v, want pre-effect conflict", pushErr)
	}
	if remoteTip := testGitCommit(
		t,
		fixture.pin.Repository,
		"refs/heads/"+fixture.pin.SourceRef,
	); remoteTip != fixture.pin.ExpectedCommit {
		t.Fatalf("conflicting push changed remote to %q", remoteTip)
	}
}

func TestManagerPushPinnedLineRejectsRepositoryLocalPushConfiguration(t *testing.T) {
	fixture := newPinnedLinePushTestFixture(
		t,
		"pr-development/push-config",
	)
	if _, configErr := runGit(
		context.Background(),
		fixture.workspace.Path,
		"config",
		"push.followTags",
		"true",
	); configErr != nil {
		t.Fatal(configErr)
	}
	_, pushErr := fixture.manager.PushPinnedLine(
		context.Background(),
		fixture.request,
	)
	if !errors.Is(pushErr, ErrPinnedLineConflict) ||
		!strings.Contains(pushErr.Error(), "push.followtags") {
		t.Fatalf("PushPinnedLine() error = %v, want unsafe config conflict", pushErr)
	}
	if strings.Contains(pushErr.Error(), fixture.pin.Repository) {
		t.Fatalf("PushPinnedLine() exposed repository path in error: %v", pushErr)
	}
	if remoteTip := testGitCommit(
		t,
		fixture.pin.Repository,
		"refs/heads/"+fixture.pin.SourceRef,
	); remoteTip != fixture.pin.ExpectedCommit {
		t.Fatalf("configuration-conflicting push changed remote to %q", remoteTip)
	}
}

func TestManagerPushPinnedLineReturnsUnknownAfterStartedRejectedPush(t *testing.T) {
	fixture := newPinnedLinePushTestFixture(
		t,
		"pr-development/push-unknown",
	)
	installPinnedLinePushHook(
		t,
		fixture.pin.Repository,
		"pre-receive",
		"#!/bin/sh\necho private-remote-diagnostic >&2\nexit 1\n",
	)
	result, pushErr := fixture.manager.PushPinnedLine(
		context.Background(),
		fixture.request,
	)
	if result != (PinnedLinePushResult{}) ||
		!errors.Is(pushErr, ErrPinnedLinePushOutcomeUnknown) ||
		errors.Is(pushErr, ErrPinnedLineConflict) {
		t.Fatalf("PushPinnedLine() = %#v, %v; want unknown", result, pushErr)
	}
	if strings.Contains(pushErr.Error(), "private-remote-diagnostic") ||
		strings.Contains(pushErr.Error(), fixture.pin.Repository) {
		t.Fatalf("PushPinnedLine() exposed remote diagnostics: %v", pushErr)
	}
	if remoteTip := testGitCommit(
		t,
		fixture.pin.Repository,
		"refs/heads/"+fixture.pin.SourceRef,
	); remoteTip != fixture.pin.ExpectedCommit {
		t.Fatalf("rejected push changed remote to %q", remoteTip)
	}
}

func TestManagerPushPinnedLineReconcilesAppliedPushAfterCancellation(t *testing.T) {
	fixture := newPinnedLinePushTestFixture(
		t,
		"pr-development/push-reconciled",
	)
	coordination := t.TempDir()
	started := filepath.Join(coordination, "started")
	release := filepath.Join(coordination, "release")
	defer func() { _ = os.WriteFile(release, []byte("release\n"), 0o600) }()
	installPinnedLinePushHook(
		t,
		fixture.pin.Repository,
		"post-receive",
		blockingPinnedLinePushHook(started, release),
	)

	type pushOutcome struct {
		result PinnedLinePushResult
		err    error
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan pushOutcome, 1)
	go func() {
		result, err := fixture.manager.PushPinnedLine(ctx, fixture.request)
		done <- pushOutcome{result: result, err: err}
	}()
	waitForPinnedLinePushPath(t, started)
	cancel()
	time.Sleep(50 * time.Millisecond)
	if writeErr := os.WriteFile(release, []byte("release\n"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	select {
	case outcome := <-done:
		if outcome.err != nil ||
			outcome.result.Disposition != PinnedLinePushReconciled ||
			outcome.result.RemoteTip != fixture.parked.Tip ||
			!outcome.result.WorkspaceClean {
			t.Fatalf("PushPinnedLine() = %#v, %v; want reconciled", outcome.result, outcome.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PushPinnedLine() did not reconcile after cancellation")
	}
}

func TestManagerPushPinnedLineSerializesResumeAcrossManagers(t *testing.T) {
	fixture := newPinnedLinePushTestFixture(
		t,
		"pr-development/push-lock",
	)
	coordination := t.TempDir()
	started := filepath.Join(coordination, "started")
	release := filepath.Join(coordination, "release")
	defer func() { _ = os.WriteFile(release, []byte("release\n"), 0o600) }()
	installPinnedLinePushHook(
		t,
		fixture.pin.Repository,
		"pre-receive",
		blockingPinnedLinePushHook(started, release),
	)

	type pushOutcome struct {
		result PinnedLinePushResult
		err    error
	}
	pushDone := make(chan pushOutcome, 1)
	go func() {
		result, err := fixture.manager.PushPinnedLine(
			context.Background(),
			fixture.request,
		)
		pushDone <- pushOutcome{result: result, err: err}
	}()
	waitForPinnedLinePushPath(t, started)

	now := time.Date(2026, 8, 10, 13, 0, 0, 0, time.UTC)
	restarted := newTestManagerAtRoot(t, fixture.manager.RootDir(), &now)
	resumePin := fixture.pin
	resumePin.ReservationKey = "pr-development/push-lock-resume"
	type resumeOutcome struct {
		result PinnedLineLease
		err    error
	}
	resumeDone := make(chan resumeOutcome, 1)
	go func() {
		result, err := restarted.ResumePinnedLine(
			context.Background(),
			PinnedLineResumeRequest{
				Pin:             resumePin,
				WorkspaceID:     fixture.workspace.ID,
				LineID:          pinnedLineTestID,
				ExpectedVersion: fixture.parked.Version,
				ExpectedEpoch:   fixture.parked.MutationEpoch,
				ExpectedTip:     fixture.parked.Tip,
				ExpectedTree:    fixture.parked.Tree,
			},
		)
		resumeDone <- resumeOutcome{result: result, err: err}
	}()
	select {
	case outcome := <-resumeDone:
		t.Fatalf("ResumePinnedLine() overtook push: %#v, %v", outcome.result, outcome.err)
	case <-time.After(150 * time.Millisecond):
	}
	if writeErr := os.WriteFile(release, []byte("release\n"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	select {
	case outcome := <-pushDone:
		if outcome.err != nil || outcome.result.Disposition != PinnedLinePushApplied {
			t.Fatalf("PushPinnedLine() = %#v, %v", outcome.result, outcome.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PushPinnedLine() did not finish after hook release")
	}
	select {
	case outcome := <-resumeDone:
		if outcome.err != nil || outcome.result.MutationEpoch != fixture.parked.Version+1 {
			t.Fatalf("ResumePinnedLine() = %#v, %v", outcome.result, outcome.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ResumePinnedLine() did not continue after push")
	}
}

func TestManagerPushPinnedLineReturnsProvenResultWithLocalDrift(t *testing.T) {
	fixture := newPinnedLinePushTestFixture(
		t,
		"pr-development/push-drift",
	)
	coordination := t.TempDir()
	started := filepath.Join(coordination, "started")
	release := filepath.Join(coordination, "release")
	defer func() { _ = os.WriteFile(release, []byte("release\n"), 0o600) }()
	installPinnedLinePushHook(
		t,
		fixture.pin.Repository,
		"post-receive",
		blockingPinnedLinePushHook(started, release),
	)
	type pushOutcome struct {
		result PinnedLinePushResult
		err    error
	}
	done := make(chan pushOutcome, 1)
	go func() {
		result, err := fixture.manager.PushPinnedLine(
			context.Background(),
			fixture.request,
		)
		done <- pushOutcome{result: result, err: err}
	}()
	waitForPinnedLinePushPath(t, started)
	if writeErr := os.WriteFile(
		filepath.Join(fixture.workspace.Path, "drift.txt"),
		[]byte("external drift\n"),
		0o600,
	); writeErr != nil {
		t.Fatal(writeErr)
	}
	if writeErr := os.WriteFile(release, []byte("release\n"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	select {
	case outcome := <-done:
		if !errors.Is(outcome.err, ErrPinnedLinePushWorkspaceDrift) ||
			errors.Is(outcome.err, ErrPinnedLinePushOutcomeUnknown) ||
			outcome.result.Disposition != PinnedLinePushApplied ||
			outcome.result.RemoteTip != fixture.parked.Tip ||
			outcome.result.WorkspaceClean {
			t.Fatalf("PushPinnedLine() = %#v, %v; want proven drift", outcome.result, outcome.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PushPinnedLine() did not finish after drift hook release")
	}
}

func TestManagerPushPinnedLineReturnsProvenResultWithConfigurationDrift(t *testing.T) {
	fixture := newPinnedLinePushTestFixture(
		t,
		"pr-development/push-config-drift",
	)
	coordination := t.TempDir()
	started := filepath.Join(coordination, "started")
	release := filepath.Join(coordination, "release")
	defer func() { _ = os.WriteFile(release, []byte("release\n"), 0o600) }()
	installPinnedLinePushHook(
		t,
		fixture.pin.Repository,
		"post-receive",
		blockingPinnedLinePushHook(started, release),
	)
	type pushOutcome struct {
		result PinnedLinePushResult
		err    error
	}
	done := make(chan pushOutcome, 1)
	go func() {
		result, err := fixture.manager.PushPinnedLine(
			context.Background(),
			fixture.request,
		)
		done <- pushOutcome{result: result, err: err}
	}()
	waitForPinnedLinePushPath(t, started)
	if _, configErr := runGit(
		context.Background(),
		fixture.workspace.Path,
		"config",
		"push.followTags",
		"true",
	); configErr != nil {
		t.Fatal(configErr)
	}
	if writeErr := os.WriteFile(release, []byte("release\n"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	select {
	case outcome := <-done:
		if !errors.Is(outcome.err, ErrPinnedLinePushWorkspaceDrift) ||
			errors.Is(outcome.err, ErrPinnedLinePushOutcomeUnknown) ||
			outcome.result.Disposition != PinnedLinePushApplied ||
			outcome.result.RemoteTip != fixture.parked.Tip ||
			outcome.result.WorkspaceClean {
			t.Fatalf("PushPinnedLine() = %#v, %v; want proven configuration drift", outcome.result, outcome.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PushPinnedLine() did not finish after configuration drift")
	}
}

func assertPinnedLinePushResult(
	t *testing.T,
	result PinnedLinePushResult,
	fixture pinnedLinePushTestFixture,
	disposition PinnedLinePushDisposition,
) {
	t.Helper()
	if result.WorkspaceID != fixture.workspace.ID ||
		result.Version != fixture.parked.Version ||
		result.MutationEpoch != fixture.parked.MutationEpoch ||
		result.ParkIntentID != fixture.request.ExpectedParkIntentID ||
		result.BaseCommit != fixture.parked.PreviousTip ||
		result.Tip != fixture.parked.Tip || result.Tree != fixture.parked.Tree ||
		result.RemoteRef != "refs/heads/"+fixture.pin.SourceRef ||
		result.ExpectedRemoteTip != fixture.request.ExpectedRemoteTip ||
		result.RemoteTip != fixture.parked.Tip ||
		result.Disposition != disposition || !result.WorkspaceClean {
		t.Fatalf("PushPinnedLine() = %#v", result)
	}
}

func installPinnedLinePushHook(
	t *testing.T,
	repository, name, script string,
) {
	t.Helper()
	hook := filepath.Join(repository, ".git", "hooks", name)
	if writeErr := os.WriteFile(hook, []byte(script), 0o700); writeErr != nil {
		t.Fatalf("install %s hook: %v", name, writeErr)
	}
}

func blockingPinnedLinePushHook(started, release string) string {
	return fmt.Sprintf(
		"#!/bin/sh\n: > %q\nwhile [ ! -f %q ]; do sleep 0.02; done\n",
		started,
		release,
	)
}

func waitForPinnedLinePushPath(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, statErr := os.Stat(path); statErr == nil {
			return
		} else if !os.IsNotExist(statErr) {
			t.Fatalf("inspect push coordination path: %v", statErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for push coordination path %s", filepath.Base(path))
}
