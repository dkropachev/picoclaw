package gitworkspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type pinnedLineBareRemotePushFixture struct {
	pinnedLinePushTestFixture
	remote          string
	ambientSentinel string
}

func newPinnedLineBareRemotePushFixture(
	t *testing.T,
	reservation string,
) pinnedLineBareRemotePushFixture {
	t.Helper()
	fixture := newPinnedLinePushTestFixture(t, reservation)
	ambientSentinel := poisonPinnedLineBareRemoteAmbientGit(t)
	remoteParent := t.TempDir()
	remote := filepath.Join(remoteParent, "remote.git")
	if _, cloneErr := runPinnedLineBareRemoteGit(
		t,
		remoteParent,
		"clone",
		"--quiet",
		"--bare",
		fixture.pin.Repository,
		remote,
	); cloneErr != nil {
		t.Fatalf("clone bare push target: %v", cloneErr)
	}
	if _, updateErr := runPinnedLineBareRemoteGit(
		t,
		remote,
		"update-ref",
		"refs/heads/base-protected",
		fixture.request.ExpectedRemoteTip,
	); updateErr != nil {
		t.Fatalf("create protected base ref: %v", updateErr)
	}
	fixture.manager.pinnedLinePushTransport = func(repository string) (string, error) {
		if repository != fixture.pin.Repository {
			return "", errors.New("unexpected test push repository")
		}
		return remote, nil
	}
	return pinnedLineBareRemotePushFixture{
		pinnedLinePushTestFixture: fixture,
		remote:                    remote,
		ambientSentinel:           ambientSentinel,
	}
}

func TestManagerPushPinnedLineBareRemoteBoundary(t *testing.T) {
	t.Run("exact tip and replay", func(t *testing.T) {
		fixture := newPinnedLineBareRemotePushFixture(
			t,
			"pr-development/bare-push-success",
		)
		assertPinnedLineBareRemoteTip(
			t,
			fixture,
			fixture.request.ExpectedRemoteTip,
		)
		receipts := filepath.Join(t.TempDir(), "receives")
		installPinnedLineBareRemoteHook(
			t,
			fixture.remote,
			"pre-receive",
			fmt.Sprintf("#!/bin/sh\ncat >> %q\n", receipts),
		)

		result, pushErr := fixture.manager.PushPinnedLine(
			context.Background(),
			fixture.request,
		)
		if pushErr != nil {
			t.Fatalf("PushPinnedLine() error = %v", pushErr)
		}
		assertPinnedLinePushResult(
			t,
			result,
			fixture.pinnedLinePushTestFixture,
			PinnedLinePushApplied,
		)
		assertPinnedLineBareRemoteTip(t, fixture, fixture.parked.Tip)

		replayed, replayErr := fixture.manager.PushPinnedLine(
			context.Background(),
			fixture.request,
		)
		if replayErr != nil {
			t.Fatalf("replayed PushPinnedLine() error = %v", replayErr)
		}
		assertPinnedLinePushResult(
			t,
			replayed,
			fixture.pinnedLinePushTestFixture,
			PinnedLinePushAlreadyCurrent,
		)
		wantReceive := fmt.Sprintf(
			"%s %s refs/heads/%s\n",
			fixture.request.ExpectedRemoteTip,
			fixture.request.ExpectedTip,
			fixture.pin.SourceRef,
		)
		if received, readErr := os.ReadFile(receipts); readErr != nil ||
			string(received) != wantReceive {
			t.Fatalf(
				"bare remote receive effects = %q, %v; want exactly %q",
				received,
				readErr,
				wantReceive,
			)
		}
		assertPinnedLineBareRemoteTip(t, fixture, fixture.parked.Tip)
		wantRefs := fmt.Sprintf(
			"refs/heads/base-protected=%s\nrefs/heads/%s=%s",
			fixture.request.ExpectedRemoteTip,
			fixture.pin.SourceRef,
			fixture.request.ExpectedTip,
		)
		if refs := pinnedLineBareRemoteRefs(t, fixture.remote); refs != wantRefs {
			t.Fatalf("bare remote refs = %q, want %q", refs, wantRefs)
		}
		assertPinnedLineBareRemoteRetained(t, fixture)
	})

	t.Run("stale CAS is pre-effect", func(t *testing.T) {
		fixture := newPinnedLineBareRemotePushFixture(
			t,
			"pr-development/bare-push-cas",
		)
		initialTip := fixture.request.ExpectedRemoteTip
		receipt := filepath.Join(t.TempDir(), "receive")
		installPinnedLineBareRemoteHook(
			t,
			fixture.remote,
			"pre-receive",
			fmt.Sprintf("#!/bin/sh\n: > %q\n", receipt),
		)
		request := fixture.request
		request.ExpectedRemoteTip = request.ExpectedTip

		result, pushErr := fixture.manager.PushPinnedLine(
			context.Background(),
			request,
		)
		if result != (PinnedLinePushResult{}) ||
			!errors.Is(pushErr, ErrPinnedLineConflict) ||
			errors.Is(pushErr, ErrPinnedLinePushOutcomeUnknown) {
			t.Fatalf("PushPinnedLine() = %#v, %v; want pre-effect CAS conflict", result, pushErr)
		}
		assertPinnedLineBareRemoteTip(t, fixture, initialTip)
		assertPinnedLineBareRemoteNoReceive(t, receipt)
		assertPinnedLineBareRemoteRetained(t, fixture)
	})

	t.Run("local drift is pre-effect", func(t *testing.T) {
		fixture := newPinnedLineBareRemotePushFixture(
			t,
			"pr-development/bare-push-drift",
		)
		initialTip := fixture.request.ExpectedRemoteTip
		receipt := filepath.Join(t.TempDir(), "receive")
		installPinnedLineBareRemoteHook(
			t,
			fixture.remote,
			"pre-receive",
			fmt.Sprintf("#!/bin/sh\n: > %q\n", receipt),
		)
		if writeErr := os.WriteFile(
			filepath.Join(fixture.workspace.Path, "pre-push-drift.txt"),
			[]byte("external drift\n"),
			0o600,
		); writeErr != nil {
			t.Fatal(writeErr)
		}

		result, pushErr := fixture.manager.PushPinnedLine(
			context.Background(),
			fixture.request,
		)
		if result != (PinnedLinePushResult{}) ||
			!errors.Is(pushErr, ErrPinnedLineConflict) ||
			errors.Is(pushErr, ErrPinnedLinePushOutcomeUnknown) {
			t.Fatalf("PushPinnedLine() = %#v, %v; want pre-effect local conflict", result, pushErr)
		}
		assertPinnedLineBareRemoteTip(t, fixture, initialTip)
		assertPinnedLineBareRemoteNoReceive(t, receipt)
		assertPinnedLineBareRemoteRetained(t, fixture)
	})

	t.Run("divergent candidate is pre-effect", func(t *testing.T) {
		fixture := newPinnedLineBareRemotePushFixture(
			t,
			"pr-development/bare-push-divergent",
		)
		divergentTip := createPinnedLineBareRemoteDivergence(t, fixture)
		refsBefore := pinnedLineBareRemoteRefs(t, fixture.remote)
		receipt := filepath.Join(t.TempDir(), "receive")
		installPinnedLineBareRemoteHook(
			t,
			fixture.remote,
			"pre-receive",
			fmt.Sprintf("#!/bin/sh\n: > %q\n", receipt),
		)
		request := fixture.request
		request.ExpectedRemoteTip = divergentTip

		result, pushErr := fixture.manager.PushPinnedLine(
			context.Background(),
			request,
		)
		if result != (PinnedLinePushResult{}) ||
			!errors.Is(pushErr, ErrPinnedLineConflict) ||
			errors.Is(pushErr, ErrPinnedLinePushOutcomeUnknown) {
			t.Fatalf(
				"PushPinnedLine() = %#v, %v; want pre-effect non-forward conflict",
				result,
				pushErr,
			)
		}
		assertPinnedLineBareRemoteNoReceive(t, receipt)
		if refsAfter := pinnedLineBareRemoteRefs(t, fixture.remote); refsAfter != refsBefore {
			t.Fatalf(
				"bare remote refs changed after non-forward rejection: before %q, after %q",
				refsBefore,
				refsAfter,
			)
		}
		assertPinnedLineBareRemoteTip(t, fixture, divergentTip)
		assertPinnedLineBareRemoteRetained(t, fixture)
	})
}

func poisonPinnedLineBareRemoteAmbientGit(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	hooks := filepath.Join(root, "hooks")
	if err := os.Mkdir(hooks, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(root, "ambient-git-hook-ran")
	for _, name := range []string{"pre-push", "reference-transaction"} {
		if err := os.WriteFile(
			filepath.Join(hooks, name),
			[]byte(fmt.Sprintf("#!/bin/sh\n: > %q\nexit 97\n", sentinel)),
			0o700,
		); err != nil {
			t.Fatalf("install hostile ambient Git hook %s: %v", name, err)
		}
	}
	globalConfig := filepath.Join(root, "global.gitconfig")
	if err := os.WriteFile(
		globalConfig,
		[]byte("[protocol \"file\"]\n\tallow = never\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
	t.Setenv("GIT_CONFIG_COUNT", "2")
	t.Setenv("GIT_CONFIG_KEY_0", "protocol.file.allow")
	t.Setenv("GIT_CONFIG_VALUE_0", "never")
	t.Setenv("GIT_CONFIG_KEY_1", "core.hooksPath")
	t.Setenv("GIT_CONFIG_VALUE_1", hooks)
	t.Setenv("GIT_DIR", filepath.Join(root, "hostile.git"))
	t.Setenv("GIT_WORK_TREE", filepath.Join(root, "hostile-worktree"))
	return sentinel
}

func runPinnedLineBareRemoteGit(
	t *testing.T,
	directory string,
	args ...string,
) (string, error) {
	t.Helper()
	output, err := runPinnedGit(context.Background(), directory, nil, args...)
	return strings.TrimSpace(output), err
}

func createPinnedLineBareRemoteDivergence(
	t *testing.T,
	fixture pinnedLineBareRemotePushFixture,
) string {
	t.Helper()
	tree, err := runPinnedLineBareRemoteGit(
		t,
		fixture.workspace.Path,
		"rev-parse",
		fixture.request.ExpectedRemoteTip+"^{tree}",
	)
	if err != nil {
		t.Fatalf("resolve divergent remote tree: %v", err)
	}
	divergent, err := runPinnedLineBareRemoteGit(
		t,
		fixture.workspace.Path,
		"commit-tree",
		tree,
		"-p",
		fixture.request.ExpectedRemoteTip,
		"-m",
		"Advance source branch on a divergent line",
	)
	if err != nil {
		t.Fatalf("create divergent remote commit: %v", err)
	}
	if divergent == fixture.request.ExpectedTip {
		t.Fatal("divergent remote commit unexpectedly equals the parked tip")
	}
	if _, err = runPinnedLineBareRemoteGit(
		t,
		fixture.workspace.Path,
		"push",
		"--quiet",
		fixture.remote,
		divergent+":refs/heads/"+fixture.pin.SourceRef,
	); err != nil {
		t.Fatalf("advance bare remote to divergent commit: %v", err)
	}
	assertPinnedLineBareRemoteTip(t, fixture, divergent)
	return divergent
}

func pinnedLineBareRemoteRefs(t *testing.T, remote string) string {
	t.Helper()
	refs, err := runPinnedLineBareRemoteGit(
		t,
		remote,
		"for-each-ref",
		"--format=%(refname)=%(objectname)",
		"refs/heads",
	)
	if err != nil {
		t.Fatalf("inspect bare remote refs: %v", err)
	}
	return refs
}

func installPinnedLineBareRemoteHook(
	t *testing.T,
	remote, name, script string,
) {
	t.Helper()
	hook := filepath.Join(remote, "hooks", name)
	if writeErr := os.WriteFile(hook, []byte(script), 0o700); writeErr != nil {
		t.Fatalf("install bare remote %s hook: %v", name, writeErr)
	}
}

func assertPinnedLineBareRemoteTip(
	t *testing.T,
	fixture pinnedLineBareRemotePushFixture,
	want string,
) {
	t.Helper()
	tip, err := runPinnedLineBareRemoteGit(
		t,
		fixture.remote,
		"rev-parse",
		"--verify",
		"refs/heads/"+fixture.pin.SourceRef+"^{commit}",
	)
	if err != nil {
		t.Fatalf("resolve bare remote tip: %v", err)
	}
	if tip != want {
		t.Fatalf("bare remote tip = %q, want %q", tip, want)
	}
}

func assertPinnedLineBareRemoteNoReceive(t *testing.T, receipt string) {
	t.Helper()
	if _, statErr := os.Stat(receipt); !os.IsNotExist(statErr) {
		t.Fatalf("pre-effect rejection invoked bare remote receive: %v", statErr)
	}
}

func assertPinnedLineBareRemoteRetained(
	t *testing.T,
	fixture pinnedLineBareRemotePushFixture,
) {
	t.Helper()
	state := developmentLineStateForTest(t, fixture.manager)
	line := state.DevelopmentLines[pinnedLineTestID]
	workspace := state.Workspaces[fixture.workspace.ID]
	if line == nil || workspace == nil {
		t.Fatalf("retained line ownership is missing: %#v, %#v", line, workspace)
	}
	if line.State != developmentLineParked || line.PendingParkSet ||
		line.Version != fixture.parked.Version ||
		line.MutationEpoch != fixture.parked.MutationEpoch ||
		line.Tip != fixture.parked.Tip || line.Tree != fixture.parked.Tree ||
		line.MutationReservationHash != "" || line.MutationAgentID != "" ||
		workspace.LockedBy != nil || workspace.DevelopmentLineID != line.ID {
		t.Fatalf("retained line is not parked and reservation-free: %#v, %#v", line, workspace)
	}
	tip, err := runPinnedLineBareRemoteGit(
		t,
		fixture.workspace.Path,
		"rev-parse",
		"--verify",
		"refs/heads/"+line.Branch+"^{commit}",
	)
	if err != nil {
		t.Fatalf("resolve retained line tip: %v", err)
	}
	if tip != fixture.parked.Tip {
		t.Fatalf("retained line tip = %q, want parked tip %q", tip, fixture.parked.Tip)
	}
	baseTip, err := runPinnedLineBareRemoteGit(
		t,
		fixture.remote,
		"rev-parse",
		"--verify",
		"refs/heads/base-protected^{commit}",
	)
	if err != nil {
		t.Fatalf("resolve protected base ref: %v", err)
	}
	if baseTip != fixture.request.ExpectedRemoteTip {
		t.Fatalf(
			"protected base ref = %q, want unchanged %q",
			baseTip,
			fixture.request.ExpectedRemoteTip,
		)
	}
	if _, statErr := os.Stat(fixture.ambientSentinel); !os.IsNotExist(statErr) {
		t.Fatalf("hostile ambient Git configuration affected fixture operations: %v", statErr)
	}
}
