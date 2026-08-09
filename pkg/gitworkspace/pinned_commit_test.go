package gitworkspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const pinnedCommitTestIntent = "pdcmt_0123456789abcdef0123456789abcdef"

type pinnedCommitTestFixture struct {
	manager   *Manager
	pin       PinnedAcquireRequest
	workspace WorkspaceInfo
}

func newPinnedCommitTestFixture(t *testing.T, reservation string) pinnedCommitTestFixture {
	t.Helper()
	ctx := context.Background()
	source := initSourceRepo(t)
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &now)
	pin := PinnedAcquireRequest{
		Repository:     source,
		SourceRef:      "main",
		ExpectedCommit: testGitCommit(t, source, "HEAD"),
		ReservationKey: reservation,
		AgentID:        "main",
	}
	workspace, err := manager.AcquirePinned(ctx, pin)
	if err != nil {
		t.Fatalf("AcquirePinned() error = %v", err)
	}
	return pinnedCommitTestFixture{
		manager:   manager,
		pin:       pin,
		workspace: workspace,
	}
}

func (fixture pinnedCommitTestFixture) snapshot(t *testing.T) PinnedCandidate {
	t.Helper()
	candidate, err := fixture.manager.SnapshotPinnedCandidate(
		context.Background(),
		PinnedCandidateRequest{
			Pin:         fixture.pin,
			WorkspaceID: fixture.workspace.ID,
		},
	)
	if err != nil {
		t.Fatalf("SnapshotPinnedCandidate() error = %v", err)
	}
	return candidate
}

func (fixture pinnedCommitTestFixture) commitRequest(
	candidate PinnedCandidate,
	message string,
) PinnedCommitRequest {
	return PinnedCommitRequest{
		Pin:                     fixture.pin,
		WorkspaceID:             fixture.workspace.ID,
		IntentID:                pinnedCommitTestIntent,
		ExpectedParent:          candidate.ParentCommit,
		ExpectedTree:            candidate.Tree,
		ExpectedCandidateDigest: candidate.CandidateDigest,
		Message:                 message,
		AuthoredAt:              pinnedCommitTestTime(),
	}
}

func TestManagerSnapshotAndCommitPinnedCandidate(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedCommitTestFixture(t, "pr-development/commit-normal")
	if err := os.WriteFile(
		filepath.Join(fixture.workspace.Path, "README.md"),
		[]byte("# repaired repo\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(fixture.workspace.Path, "repair.txt"),
		[]byte("targeted repair\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	ignoredDirectory := filepath.Join(fixture.workspace.Path, "ignored")
	if err := os.MkdirAll(ignoredDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	ignoredPath := filepath.Join(ignoredDirectory, "cache.bin")
	if err := os.WriteFile(ignoredPath, []byte("local cache\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	indexBefore := testGitObject(t, fixture.workspace.Path, "write-tree")

	candidate := fixture.snapshot(t)
	if candidate.WorkspaceID != fixture.workspace.ID ||
		candidate.ParentCommit != fixture.pin.ExpectedCommit ||
		candidate.Tree == "" || candidate.CandidateDigest == "" ||
		candidate.ChangedFiles != 2 {
		t.Fatalf("SnapshotPinnedCandidate() = %#v", candidate)
	}
	if indexAfter := testGitObject(t, fixture.workspace.Path, "write-tree"); indexAfter != indexBefore {
		t.Fatalf("snapshot changed real index from %q to %q", indexBefore, indexAfter)
	}
	if head := testGitCommit(t, fixture.workspace.Path, "HEAD"); head != candidate.ParentCommit {
		t.Fatalf("snapshot changed HEAD to %q, want %q", head, candidate.ParentCommit)
	}

	request := fixture.commitRequest(candidate, "Apply targeted repair")
	result, err := fixture.manager.CommitPinned(ctx, request)
	if err != nil {
		t.Fatalf("CommitPinned() error = %v", err)
	}
	if result.WorkspaceID != fixture.workspace.ID || result.IntentID != request.IntentID ||
		result.ParentCommit != candidate.ParentCommit || result.Tree != candidate.Tree ||
		result.CandidateDigest != candidate.CandidateDigest || result.Commit == "" ||
		result.ChangedFiles != candidate.ChangedFiles || result.AlreadyApplied ||
		!result.WorkspaceClean {
		t.Fatalf("CommitPinned() = %#v", result)
	}
	if head := testGitCommit(t, fixture.workspace.Path, "HEAD"); head != result.Commit {
		t.Fatalf("pinned HEAD = %q, want %q", head, result.Commit)
	}
	if tree := testGitObject(t, fixture.workspace.Path, "rev-parse", "HEAD^{tree}"); tree != candidate.Tree {
		t.Fatalf("pinned commit tree = %q, want %q", tree, candidate.Tree)
	}
	if branch, branchErr := runGit(ctx, fixture.workspace.Path, "symbolic-ref", "-q", "HEAD"); branchErr == nil {
		t.Fatalf("pinned HEAD became attached to %q", strings.TrimSpace(branch))
	}
	status, err := runGit(ctx, fixture.workspace.Path, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil || strings.TrimSpace(status) != "" {
		t.Fatalf("pinned status = %q, %v; want clean", status, err)
	}
	if content, readErr := os.ReadFile(ignoredPath); readErr != nil || string(content) != "local cache\n" {
		t.Fatalf("ignored cache = %q, %v", content, readErr)
	}
	if _, showErr := runGit(ctx, fixture.workspace.Path, "show", result.Commit+":ignored/cache.bin"); showErr == nil {
		t.Fatal("ignored cache was included in pinned commit")
	}
	metadata, err := runGit(
		ctx,
		fixture.workspace.Path,
		"show",
		"-s",
		"--format=%an%x00%ae%x00%at%x00%ct%x00%B",
		result.Commit,
	)
	if err != nil {
		t.Fatal(err)
	}
	wantMetadata := "PicoClaw\x00picoclaw@localhost\x00" +
		"1786198516\x001786198516\x00" +
		pinnedCommitObjectMessage("Apply targeted repair", request.IntentID) + "\n"
	if metadata != wantMetadata {
		t.Fatalf("pinned commit metadata = %q, want %q", metadata, wantMetadata)
	}
	if sourceHead := testGitCommit(t, fixture.pin.Repository, "main"); sourceHead != fixture.pin.ExpectedCommit {
		t.Fatalf("source branch moved to %q, want %q", sourceHead, fixture.pin.ExpectedCommit)
	}
}

func TestManagerCommitPinnedRetryIsDeterministicAndRecoversIndex(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedCommitTestFixture(t, "pr-development/commit-retry")
	if err := os.WriteFile(
		filepath.Join(fixture.workspace.Path, "retry.txt"),
		[]byte("repair once\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	candidate := fixture.snapshot(t)
	request := fixture.commitRequest(candidate, "Record retry-safe repair")
	first, err := fixture.manager.CommitPinned(ctx, request)
	if err != nil {
		t.Fatalf("first CommitPinned() error = %v", err)
	}

	if _, readErr := runGit(
		ctx,
		fixture.workspace.Path,
		"read-tree",
		candidate.ParentCommit,
	); readErr != nil {
		t.Fatal(readErr)
	}
	if index := testGitObject(t, fixture.workspace.Path, "write-tree"); index == candidate.Tree {
		t.Fatalf("test did not move real index away from candidate tree %q", candidate.Tree)
	}
	second, err := fixture.manager.CommitPinned(ctx, request)
	if err != nil {
		t.Fatalf("retry CommitPinned() error = %v", err)
	}
	if second.Commit != first.Commit || !second.AlreadyApplied || !second.WorkspaceClean {
		t.Fatalf("retry CommitPinned() = %#v, first = %#v", second, first)
	}
	if index := testGitObject(t, fixture.workspace.Path, "write-tree"); index != candidate.Tree {
		t.Fatalf("recovered index tree = %q, want %q", index, candidate.Tree)
	}
	third, err := fixture.manager.CommitPinned(ctx, request)
	if err != nil {
		t.Fatalf("second retry CommitPinned() error = %v", err)
	}
	if third.Commit != first.Commit || !third.AlreadyApplied || !third.WorkspaceClean {
		t.Fatalf("second retry CommitPinned() = %#v, first = %#v", third, first)
	}
}

func TestManagerCommitPinnedLeavesStaleGitLocksForExplicitRecovery(t *testing.T) {
	t.Run("HEAD lock before application", func(t *testing.T) {
		ctx := context.Background()
		fixture := newPinnedCommitTestFixture(t, "pr-development/commit-stale-head-lock")
		path := filepath.Join(fixture.workspace.Path, "head-lock.txt")
		if err := os.WriteFile(path, []byte("validated repair\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		candidate := fixture.snapshot(t)
		lockPath := filepath.Join(fixture.workspace.Path, ".git", "HEAD.lock")
		if writeErr := os.WriteFile(lockPath, []byte("stale\n"), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
		_, snapshotErr := fixture.manager.SnapshotPinnedCandidate(ctx, PinnedCandidateRequest{
			Pin:         fixture.pin,
			WorkspaceID: fixture.workspace.ID,
		})
		assertPinnedCommitConflict(t, snapshotErr, "HEAD.lock")

		result, err := fixture.manager.CommitPinned(
			ctx,
			fixture.commitRequest(candidate, "Do not delete a stale HEAD lock"),
		)
		if err == nil {
			t.Fatal("CommitPinned() error = nil, want stale HEAD lock failure")
		}
		if result != (PinnedCommitResult{}) {
			t.Fatalf("CommitPinned() = %#v, want zero pre-application result", result)
		}
		if head := testGitCommit(t, fixture.workspace.Path, "HEAD"); head != candidate.ParentCommit {
			t.Fatalf("stale HEAD lock moved HEAD to %q, want %q", head, candidate.ParentCommit)
		}
		if content, readErr := os.ReadFile(lockPath); readErr != nil || string(content) != "stale\n" {
			t.Fatalf("stale HEAD lock = %q, %v; want preserved", content, readErr)
		}
	})

	t.Run("index lock after application", func(t *testing.T) {
		ctx := context.Background()
		fixture := newPinnedCommitTestFixture(t, "pr-development/commit-stale-index-lock")
		path := filepath.Join(fixture.workspace.Path, "index-lock.txt")
		validated := []byte("validated repair\n")
		if err := os.WriteFile(path, validated, 0o644); err != nil {
			t.Fatal(err)
		}
		candidate := fixture.snapshot(t)
		request := fixture.commitRequest(candidate, "Preserve applied evidence with a stale index lock")
		applied, err := fixture.manager.CommitPinned(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		if _, readErr := runGit(
			ctx,
			fixture.workspace.Path,
			"read-tree",
			candidate.ParentCommit,
		); readErr != nil {
			t.Fatal(readErr)
		}
		indexPath := filepath.Join(fixture.workspace.Path, ".git", "index")
		indexBefore, readErr := os.ReadFile(indexPath)
		if readErr != nil {
			t.Fatal(readErr)
		}
		lockPath := filepath.Join(fixture.workspace.Path, ".git", "index.lock")
		if writeErr := os.WriteFile(lockPath, []byte("stale\n"), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}

		result, err := fixture.manager.CommitPinned(ctx, request)
		if !errors.Is(err, ErrPinnedCommitWorkspaceDrift) {
			t.Fatalf("CommitPinned() error = %v, want ErrPinnedCommitWorkspaceDrift", err)
		}
		if result.Commit != applied.Commit || !result.AlreadyApplied || result.WorkspaceClean {
			t.Fatalf("CommitPinned() = %#v, want proven applied recovery result", result)
		}
		if head := testGitCommit(t, fixture.workspace.Path, "HEAD"); head != applied.Commit {
			t.Fatalf("stale index lock moved HEAD to %q, want %q", head, applied.Commit)
		}
		indexAfter, readErr := os.ReadFile(indexPath)
		if readErr != nil || !bytes.Equal(indexAfter, indexBefore) {
			t.Fatalf(
				"real index changed under stale lock: equal=%v error=%v",
				bytes.Equal(indexAfter, indexBefore),
				readErr,
			)
		}
		if content, readErr := os.ReadFile(lockPath); readErr != nil || string(content) != "stale\n" {
			t.Fatalf("stale index lock = %q, %v; want preserved", content, readErr)
		}
		if content, readErr := os.ReadFile(path); readErr != nil || !bytes.Equal(content, validated) {
			t.Fatalf("worktree changed under stale lock: content=%q error=%v", content, readErr)
		}
	})

	t.Run("HEAD lock after application", func(t *testing.T) {
		ctx := context.Background()
		fixture := newPinnedCommitTestFixture(t, "pr-development/commit-applied-head-lock")
		path := filepath.Join(fixture.workspace.Path, "applied-head-lock.txt")
		if err := os.WriteFile(path, []byte("validated repair\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		candidate := fixture.snapshot(t)
		request := fixture.commitRequest(candidate, "Preserve applied evidence with a HEAD lock")
		applied, err := fixture.manager.CommitPinned(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		lockPath := filepath.Join(fixture.workspace.Path, ".git", "HEAD.lock")
		if writeErr := os.WriteFile(lockPath, []byte("stale\n"), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}

		result, err := fixture.manager.CommitPinned(ctx, request)
		if !errors.Is(err, ErrPinnedCommitWorkspaceDrift) ||
			!errors.Is(err, ErrPinnedCommitConflict) {
			t.Fatalf("CommitPinned() error = %v, want applied workspace recovery conflict", err)
		}
		if result.Commit != applied.Commit || !result.AlreadyApplied || result.WorkspaceClean {
			t.Fatalf("CommitPinned() = %#v, want proven applied recovery result", result)
		}
		if head := testGitCommit(t, fixture.workspace.Path, "HEAD"); head != applied.Commit {
			t.Fatalf("applied HEAD lock moved HEAD to %q, want %q", head, applied.Commit)
		}
		if content, readErr := os.ReadFile(lockPath); readErr != nil || string(content) != "stale\n" {
			t.Fatalf("applied HEAD lock = %q, %v; want preserved", content, readErr)
		}
	})
}

func TestManagerCommitPinnedBindsAppliedCommitToIntentAndPayload(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedCommitTestFixture(t, "pr-development/commit-intent-binding")
	if err := os.WriteFile(
		filepath.Join(fixture.workspace.Path, "intent.txt"),
		[]byte("bound repair\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	candidate := fixture.snapshot(t)
	request := fixture.commitRequest(candidate, "Bind this repair")
	applied, err := fixture.manager.CommitPinned(ctx, request)
	if err != nil {
		t.Fatalf("first CommitPinned() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*PinnedCommitRequest)
	}{
		{
			name: "different intent",
			mutate: func(changed *PinnedCommitRequest) {
				changed.IntentID = "pdcmt_fedcba9876543210fedcba9876543210"
			},
		},
		{
			name: "same intent changed payload",
			mutate: func(changed *PinnedCommitRequest) {
				changed.Message = "Different repair payload"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := request
			test.mutate(&changed)
			result, commitErr := fixture.manager.CommitPinned(ctx, changed)
			assertPinnedCommitConflict(t, commitErr, "expected parent")
			if result != (PinnedCommitResult{}) {
				t.Fatalf("conflicting CommitPinned() result = %#v, want zero value", result)
			}
			if head := testGitCommit(t, fixture.workspace.Path, "HEAD"); head != applied.Commit {
				t.Fatalf("conflicting retry moved HEAD to %q, want %q", head, applied.Commit)
			}
		})
	}
}

func TestManagerCommitPinnedReportsAppliedWorkspaceDriftWithoutRewriting(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedCommitTestFixture(t, "pr-development/commit-applied-drift")
	path := filepath.Join(fixture.workspace.Path, "applied.txt")
	if err := os.WriteFile(path, []byte("validated repair\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	candidate := fixture.snapshot(t)
	request := fixture.commitRequest(candidate, "Apply before later drift")
	applied, err := fixture.manager.CommitPinned(ctx, request)
	if err != nil {
		t.Fatalf("first CommitPinned() error = %v", err)
	}
	if !applied.WorkspaceClean || applied.AlreadyApplied {
		t.Fatalf("first CommitPinned() = %#v", applied)
	}

	drifted := []byte("changed after the commit was applied\n")
	if writeErr := os.WriteFile(path, drifted, 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}
	result, err := fixture.manager.CommitPinned(ctx, request)
	if !errors.Is(err, ErrPinnedCommitWorkspaceDrift) {
		t.Fatalf("retry CommitPinned() error = %v, want ErrPinnedCommitWorkspaceDrift", err)
	}
	if result.Commit != applied.Commit || result.ParentCommit != candidate.ParentCommit ||
		result.Tree != candidate.Tree || result.CandidateDigest != candidate.CandidateDigest ||
		result.IntentID != request.IntentID || !result.AlreadyApplied || result.WorkspaceClean {
		t.Fatalf("drifted CommitPinned() = %#v, applied = %#v", result, applied)
	}
	if head := testGitCommit(t, fixture.workspace.Path, "HEAD"); head != applied.Commit {
		t.Fatalf("drifted pinned HEAD = %q, want applied commit %q", head, applied.Commit)
	}
	content, readErr := os.ReadFile(path)
	if readErr != nil || string(content) != string(drifted) {
		t.Fatalf("drifted content = %q, %v; want preserved %q", content, readErr, drifted)
	}
}

func TestManagerCommitPinnedPreservesUnexpectedStagedIndexAfterApply(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedCommitTestFixture(t, "pr-development/commit-staged-drift")
	path := filepath.Join(fixture.workspace.Path, "staged.txt")
	validated := []byte("validated repair\n")
	if err := os.WriteFile(path, validated, 0o644); err != nil {
		t.Fatal(err)
	}
	candidate := fixture.snapshot(t)
	request := fixture.commitRequest(candidate, "Apply before staged drift")
	applied, err := fixture.manager.CommitPinned(ctx, request)
	if err != nil {
		t.Fatalf("first CommitPinned() error = %v", err)
	}
	parentTree := testGitObject(
		t,
		fixture.workspace.Path,
		"rev-parse",
		candidate.ParentCommit+"^{tree}",
	)

	if writeErr := os.WriteFile(path, []byte("unrelated staged content\n"), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}
	if _, addErr := runGit(ctx, fixture.workspace.Path, "add", "--", "staged.txt"); addErr != nil {
		t.Fatal(addErr)
	}
	if writeErr := os.WriteFile(path, validated, 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}
	stagedTree := testGitObject(t, fixture.workspace.Path, "write-tree")
	if stagedTree == parentTree || stagedTree == candidate.Tree {
		t.Fatalf(
			"test staged tree = %q, want third tree distinct from parent %q and candidate %q",
			stagedTree,
			parentTree,
			candidate.Tree,
		)
	}

	result, err := fixture.manager.CommitPinned(ctx, request)
	if !errors.Is(err, ErrPinnedCommitWorkspaceDrift) {
		t.Fatalf("retry CommitPinned() error = %v, want ErrPinnedCommitWorkspaceDrift", err)
	}
	if result.Commit != applied.Commit || result.ParentCommit != candidate.ParentCommit ||
		result.Tree != candidate.Tree || result.CandidateDigest != candidate.CandidateDigest ||
		result.IntentID != request.IntentID || !result.AlreadyApplied || result.WorkspaceClean {
		t.Fatalf("staged-drift CommitPinned() = %#v, applied = %#v", result, applied)
	}
	if index := testGitObject(t, fixture.workspace.Path, "write-tree"); index != stagedTree {
		t.Fatalf("retry rewrote staged index to %q, want preserved %q", index, stagedTree)
	}
	if content, readErr := os.ReadFile(path); readErr != nil || string(content) != string(validated) {
		t.Fatalf("retry changed worktree content to %q, %v", content, readErr)
	}
	if head := testGitCommit(t, fixture.workspace.Path, "HEAD"); head != applied.Commit {
		t.Fatalf("retry moved HEAD to %q, want %q", head, applied.Commit)
	}
}

func TestManagerCommitPinnedRejectsMalformedRequestBeforeMutation(t *testing.T) {
	fixture := newPinnedCommitTestFixture(t, "pr-development/commit-invalid")
	if err := os.WriteFile(
		filepath.Join(fixture.workspace.Path, "invalid.txt"),
		[]byte("candidate remains local\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	candidate := fixture.snapshot(t)
	base := fixture.commitRequest(candidate, "Validate exact request")
	tests := []struct {
		name   string
		mutate func(*PinnedCommitRequest)
	}{
		{
			name: "workspace",
			mutate: func(request *PinnedCommitRequest) {
				request.WorkspaceID = ""
			},
		},
		{
			name: "intent",
			mutate: func(request *PinnedCommitRequest) {
				request.IntentID = "pdcmt_not-hex"
			},
		},
		{
			name: "parent",
			mutate: func(request *PinnedCommitRequest) {
				request.ExpectedParent = "not-a-commit"
			},
		},
		{
			name: "tree",
			mutate: func(request *PinnedCommitRequest) {
				request.ExpectedTree = strings.Repeat("a", len(request.ExpectedTree)-1)
			},
		},
		{
			name: "digest",
			mutate: func(request *PinnedCommitRequest) {
				request.ExpectedCandidateDigest = strings.Repeat("A", 64)
			},
		},
		{
			name: "message",
			mutate: func(request *PinnedCommitRequest) {
				request.Message = "invalid\nmessage"
			},
		},
		{
			name: "time",
			mutate: func(request *PinnedCommitRequest) {
				request.AuthoredAt = time.Time{}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := base
			test.mutate(&request)
			result, err := fixture.manager.CommitPinned(context.Background(), request)
			if !errors.Is(err, ErrPinnedCommitInvalid) {
				t.Fatalf("CommitPinned() error = %v, want ErrPinnedCommitInvalid", err)
			}
			if result != (PinnedCommitResult{}) {
				t.Fatalf("invalid CommitPinned() result = %#v, want zero value", result)
			}
			if head := testGitCommit(t, fixture.workspace.Path, "HEAD"); head != candidate.ParentCommit {
				t.Fatalf("invalid CommitPinned() moved HEAD to %q, want %q", head, candidate.ParentCommit)
			}
		})
	}
}

func TestManagerSnapshotAndCommitPinnedSHA256Candidate(t *testing.T) {
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "sha256-source")
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
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &now)
	pin := PinnedAcquireRequest{
		Repository:     source,
		SourceRef:      "main",
		ExpectedCommit: expected,
		ReservationKey: "pr-development/commit-sha256",
		AgentID:        "main",
	}
	workspace, err := manager.AcquirePinned(ctx, pin)
	if err != nil {
		t.Fatalf("AcquirePinned() SHA-256 error = %v", err)
	}
	if writeErr := os.WriteFile(
		filepath.Join(workspace.Path, "sha256.txt"),
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
	if len(candidate.ParentCommit) != 64 || len(candidate.Tree) != 64 ||
		len(candidate.CandidateDigest) != 64 || candidate.ChangedFiles != 1 {
		t.Fatalf("SHA-256 candidate = %#v", candidate)
	}
	request := PinnedCommitRequest{
		Pin:                     pin,
		WorkspaceID:             workspace.ID,
		IntentID:                pinnedCommitTestIntent,
		ExpectedParent:          candidate.ParentCommit,
		ExpectedTree:            candidate.Tree,
		ExpectedCandidateDigest: candidate.CandidateDigest,
		Message:                 "Commit SHA-256 repair",
		AuthoredAt:              pinnedCommitTestTime(),
	}
	result, err := manager.CommitPinned(ctx, request)
	if err != nil {
		t.Fatalf("CommitPinned() SHA-256 error = %v", err)
	}
	if len(result.Commit) != 64 || result.Commit != testGitCommit(t, workspace.Path, "HEAD") ||
		result.Tree != candidate.Tree || result.ParentCommit != expected ||
		!result.WorkspaceClean || result.AlreadyApplied {
		t.Fatalf("SHA-256 CommitPinned() = %#v", result)
	}
}

func TestManagerCommitPinnedRejectsCandidateDrift(t *testing.T) {
	t.Run("evidence", func(t *testing.T) {
		fixture := newPinnedCommitTestFixture(t, "pr-development/commit-drift-evidence")
		path := filepath.Join(fixture.workspace.Path, "evidence.txt")
		if err := os.WriteFile(path, []byte("validated\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		candidate := fixture.snapshot(t)
		request := fixture.commitRequest(candidate, "Evidence must match")
		request.ExpectedCandidateDigest = strings.Repeat("0", 64)
		_, err := fixture.manager.CommitPinned(context.Background(), request)
		assertPinnedCommitConflict(t, err, "candidate digest")
		if head := testGitCommit(t, fixture.workspace.Path, "HEAD"); head != candidate.ParentCommit {
			t.Fatalf("evidence rejection moved HEAD to %q", head)
		}
	})

	t.Run("parent", func(t *testing.T) {
		ctx := context.Background()
		fixture := newPinnedCommitTestFixture(t, "pr-development/commit-drift-parent")
		path := filepath.Join(fixture.workspace.Path, "parent.txt")
		if err := os.WriteFile(path, []byte("validated\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		candidate := fixture.snapshot(t)
		if _, err := runGit(ctx, fixture.workspace.Path, "add", "--all"); err != nil {
			t.Fatal(err)
		}
		if _, err := runGit(ctx, fixture.workspace.Path, "commit", "-m", "concurrent local commit"); err != nil {
			t.Fatal(err)
		}
		concurrent := testGitCommit(t, fixture.workspace.Path, "HEAD")
		request := fixture.commitRequest(candidate, "Original validated parent")
		_, err := fixture.manager.CommitPinned(ctx, request)
		assertPinnedCommitConflict(t, err, "expected parent")
		if head := testGitCommit(t, fixture.workspace.Path, "HEAD"); head != concurrent {
			t.Fatalf("parent rejection moved HEAD to %q, want %q", head, concurrent)
		}
	})

	t.Run("worktree", func(t *testing.T) {
		fixture := newPinnedCommitTestFixture(t, "pr-development/commit-drift-worktree")
		path := filepath.Join(fixture.workspace.Path, "worktree.txt")
		if err := os.WriteFile(path, []byte("validated\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		candidate := fixture.snapshot(t)
		if err := os.WriteFile(path, []byte("changed after validation\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		request := fixture.commitRequest(candidate, "Reject worktree drift")
		_, err := fixture.manager.CommitPinned(context.Background(), request)
		assertPinnedCommitConflict(t, err, "worktree changed")
		if head := testGitCommit(t, fixture.workspace.Path, "HEAD"); head != candidate.ParentCommit {
			t.Fatalf("worktree rejection moved HEAD to %q", head)
		}
	})
}

func TestManagerPinnedCommitRejectsNoChanges(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedCommitTestFixture(t, "pr-development/commit-no-change")
	_, err := fixture.manager.SnapshotPinnedCandidate(ctx, PinnedCandidateRequest{
		Pin:         fixture.pin,
		WorkspaceID: fixture.workspace.ID,
	})
	assertPinnedCommitConflict(t, err, "no ordinary changes")

	parentTree := testGitObject(t, fixture.workspace.Path, "rev-parse", "HEAD^{tree}")
	_, err = fixture.manager.CommitPinned(ctx, PinnedCommitRequest{
		Pin:                     fixture.pin,
		WorkspaceID:             fixture.workspace.ID,
		IntentID:                pinnedCommitTestIntent,
		ExpectedParent:          fixture.pin.ExpectedCommit,
		ExpectedTree:            parentTree,
		ExpectedCandidateDigest: strings.Repeat("0", 64),
		Message:                 "No empty checkpoints",
		AuthoredAt:              pinnedCommitTestTime(),
	})
	assertPinnedCommitConflict(t, err, "no ordinary changes")
}

func TestManagerSnapshotPinnedValidationCandidateAllowsExactNoChanges(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedCommitTestFixture(t, "pr-development/validation-no-change")
	request := PinnedCandidateRequest{
		Pin:         fixture.pin,
		WorkspaceID: fixture.workspace.ID,
	}
	parentTree := testGitObject(t, fixture.workspace.Path, "rev-parse", "HEAD^{tree}")

	candidate, err := fixture.manager.SnapshotPinnedValidationCandidate(ctx, request)
	if err != nil {
		t.Fatalf("SnapshotPinnedValidationCandidate() error = %v", err)
	}
	if candidate.WorkspaceID != fixture.workspace.ID ||
		candidate.ParentCommit != fixture.pin.ExpectedCommit ||
		candidate.Tree != parentTree || candidate.ChangedFiles != 0 ||
		!validLowerHex(candidate.CandidateDigest, sha256.Size*2) {
		t.Fatalf("SnapshotPinnedValidationCandidate() = %#v", candidate)
	}
	if _, err := fixture.manager.SnapshotPinnedCandidate(ctx, request); err == nil ||
		!errors.Is(err, ErrPinnedCommitConflict) {
		t.Fatalf("SnapshotPinnedCandidate(clean) error = %v", err)
	}

	if writeErr := os.WriteFile(
		filepath.Join(fixture.workspace.Path, "changed.txt"),
		[]byte("changed\n"),
		0o644,
	); writeErr != nil {
		t.Fatal(writeErr)
	}
	strict, err := fixture.manager.SnapshotPinnedCandidate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	validation, err := fixture.manager.SnapshotPinnedValidationCandidate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if strict != validation {
		t.Fatalf("changed candidate evidence differs: strict %#v, validation %#v", strict, validation)
	}
}

func TestManagerCommitPinnedIgnoresHooksSigningAndEditorPoisoning(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedCommitTestFixture(t, "pr-development/commit-poisoning")
	sentinel := filepath.Join(t.TempDir(), "poison-ran")
	poison := filepath.Join(t.TempDir(), "poison.sh")
	if err := os.WriteFile(
		poison,
		[]byte("#!/bin/sh\nprintf poison > \""+sentinel+"\"\nexit 91\n"),
		0o755,
	); err != nil {
		t.Fatal(err)
	}
	for _, hook := range []string{"commit-msg", "post-commit", "pre-commit", "reference-transaction"} {
		if err := os.WriteFile(
			filepath.Join(fixture.workspace.Path, ".git", "hooks", hook),
			[]byte("#!/bin/sh\nexec \""+poison+"\" \"$@\"\n"),
			0o755,
		); err != nil {
			t.Fatal(err)
		}
	}
	for key, value := range map[string]string{
		"commit.gpgSign":      "true",
		"core.editor":         poison,
		"gpg.program":         poison,
		"i18n.commitEncoding": "ISO-8859-1",
		"sequence.editor":     poison,
	} {
		if _, err := runGit(ctx, fixture.workspace.Path, "config", "--local", key, value); err != nil {
			t.Fatal(err)
		}
	}
	globalConfig := filepath.Join(t.TempDir(), "global.gitconfig")
	if err := os.WriteFile(
		globalConfig,
		[]byte("[commit]\n\tgpgSign = true\n[core]\n\thooksPath = "+poison+"\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", poison)
	t.Setenv("VISUAL", poison)
	t.Setenv("GIT_EDITOR", poison)
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
	t.Setenv("GIT_CONFIG_COUNT", "2")
	t.Setenv("GIT_CONFIG_KEY_0", "commit.gpgSign")
	t.Setenv("GIT_CONFIG_VALUE_0", "true")
	t.Setenv("GIT_CONFIG_KEY_1", "core.hooksPath")
	t.Setenv("GIT_CONFIG_VALUE_1", poison)

	if err := os.WriteFile(
		filepath.Join(fixture.workspace.Path, "safe.txt"),
		[]byte("safe plumbing\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	candidate := fixture.snapshot(t)
	result, err := fixture.manager.CommitPinned(
		ctx,
		fixture.commitRequest(candidate, "Use safe commit plumbing"),
	)
	if err != nil {
		t.Fatalf("CommitPinned() under poisoned environment error = %v", err)
	}
	if !result.WorkspaceClean || result.Commit == "" {
		t.Fatalf("CommitPinned() = %#v", result)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("poison sentinel stat error = %v, want not exist", err)
	}
}

func TestManagerPinnedCommitRejectsUnsafeOperationState(t *testing.T) {
	t.Run("attached HEAD", func(t *testing.T) {
		ctx := context.Background()
		fixture := newPinnedCommitTestFixture(t, "pr-development/commit-attached")
		if _, err := runGit(ctx, fixture.workspace.Path, "switch", "-c", "attached"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(fixture.workspace.Path, "attached.txt"),
			[]byte("reject\n"),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
		_, err := fixture.manager.SnapshotPinnedCandidate(ctx, PinnedCandidateRequest{
			Pin:         fixture.pin,
			WorkspaceID: fixture.workspace.ID,
		})
		assertPinnedCommitConflict(t, err, "HEAD is attached")
	})

	t.Run("in-progress operation before snapshot", func(t *testing.T) {
		fixture := newPinnedCommitTestFixture(t, "pr-development/commit-operation-snapshot")
		if err := os.WriteFile(
			filepath.Join(fixture.workspace.Path, "operation.txt"),
			[]byte("reject\n"),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(fixture.workspace.Path, ".git", "MERGE_HEAD"),
			[]byte(fixture.pin.ExpectedCommit+"\n"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		_, err := fixture.manager.SnapshotPinnedCandidate(
			context.Background(),
			PinnedCandidateRequest{Pin: fixture.pin, WorkspaceID: fixture.workspace.ID},
		)
		assertPinnedCommitConflict(t, err, "in-progress Git operation")
	})

	t.Run("in-progress operation before commit", func(t *testing.T) {
		fixture := newPinnedCommitTestFixture(t, "pr-development/commit-operation-commit")
		if err := os.WriteFile(
			filepath.Join(fixture.workspace.Path, "operation.txt"),
			[]byte("reject\n"),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
		candidate := fixture.snapshot(t)
		if err := os.WriteFile(
			filepath.Join(fixture.workspace.Path, ".git", "CHERRY_PICK_HEAD"),
			[]byte(fixture.pin.ExpectedCommit+"\n"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		_, err := fixture.manager.CommitPinned(
			context.Background(),
			fixture.commitRequest(candidate, "Reject operation state"),
		)
		assertPinnedCommitConflict(t, err, "in-progress Git operation")
	})
}

func TestManagerSnapshotPinnedCandidateRejectsRefStorageDrift(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedCommitTestFixture(t, "pr-development/commit-ref-storage")
	if err := os.WriteFile(
		filepath.Join(fixture.workspace.Path, "ref-storage.txt"),
		[]byte("must not be snapshotted\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	indexBefore := testGitObject(t, fixture.workspace.Path, "write-tree")
	if _, err := runGit(
		ctx,
		fixture.workspace.Path,
		"config",
		"--local",
		"core.repositoryFormatVersion",
		"1",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(
		ctx,
		fixture.workspace.Path,
		"config",
		"--local",
		"extensions.refStorage",
		"files",
	); err != nil {
		t.Fatal(err)
	}

	_, err := fixture.manager.SnapshotPinnedCandidate(ctx, PinnedCandidateRequest{
		Pin:         fixture.pin,
		WorkspaceID: fixture.workspace.ID,
	})
	assertPinnedCommitConflict(t, err, "refstorage")
	if head := testGitCommit(t, fixture.workspace.Path, "HEAD"); head != fixture.pin.ExpectedCommit {
		t.Fatalf("ref-storage rejection moved HEAD to %q", head)
	}
	if index := testGitObject(t, fixture.workspace.Path, "write-tree"); index != indexBefore {
		t.Fatalf("ref-storage rejection changed index to %q, want %q", index, indexBefore)
	}
}

func TestManagerReleasePinnedRejectsPreferSymlinkRefsBeforeDirtyPreservation(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedCommitTestFixture(t, "pr-development/release-symlink-head-config")
	if _, err := runGit(
		ctx,
		fixture.workspace.Path,
		"config",
		"--local",
		"core.preferSymlinkRefs",
		"true",
	); err != nil {
		t.Fatal(err)
	}
	dirtyPath := filepath.Join(fixture.workspace.Path, "dirty.txt")
	if err := os.WriteFile(dirtyPath, []byte("preserve me\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	released, err := fixture.manager.ReleasePinned(ctx, PinnedReleaseRequest{
		ReservationKey: fixture.pin.ReservationKey,
		AgentID:        fixture.pin.AgentID,
	})
	if err == nil || !strings.Contains(err.Error(), "core.prefersymlinkrefs") {
		t.Fatalf("ReleasePinned() error = %v, want unsafe preferSymlinkRefs rejection", err)
	}
	if len(released) != 0 {
		t.Fatalf("ReleasePinned() = %#v, want no released workspace", released)
	}
	headInfo, statErr := os.Lstat(filepath.Join(fixture.workspace.Path, ".git", "HEAD"))
	if statErr != nil || headInfo.Mode()&os.ModeSymlink != 0 || !headInfo.Mode().IsRegular() {
		t.Fatalf("HEAD metadata after rejection = %#v, %v; want regular file", headInfo, statErr)
	}
	if head := testGitCommit(t, fixture.workspace.Path, "HEAD"); head != fixture.pin.ExpectedCommit {
		t.Fatalf("ReleasePinned() moved HEAD to %q, want %q", head, fixture.pin.ExpectedCommit)
	}
	content, readErr := os.ReadFile(dirtyPath)
	if readErr != nil || string(content) != "preserve me\n" {
		t.Fatalf("dirty content after rejection = %q, %v", content, readErr)
	}
	if _, unsetErr := runGit(
		ctx,
		fixture.workspace.Path,
		"config",
		"--local",
		"--unset",
		"core.preferSymlinkRefs",
	); unsetErr != nil {
		t.Fatal(unsetErr)
	}
	retained := workspaceRecordForTest(t, fixture.manager, fixture.workspace.ID)
	if retained.LockedBy == nil || retained.LockedBy.SessionKey != fixture.pin.ReservationKey {
		t.Fatalf("workspace lock after rejected release = %#v", retained.LockedBy)
	}
}

func TestManagerSnapshotPinnedCandidateRejectsChangedGitLink(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedCommitTestFixture(t, "pr-development/commit-gitlink")
	if _, err := runGit(
		ctx,
		fixture.workspace.Path,
		"clone",
		"--no-local",
		"--",
		fixture.pin.Repository,
		"nested",
	); err != nil {
		t.Fatal(err)
	}
	_, err := fixture.manager.SnapshotPinnedCandidate(ctx, PinnedCandidateRequest{
		Pin:         fixture.pin,
		WorkspaceID: fixture.workspace.ID,
	})
	assertPinnedCommitConflict(t, err, "changed Git links")
}

func TestManagerRejectsPinnedGitMetadataSymlinkEscapes(t *testing.T) {
	t.Run("non-regular pack entry", func(t *testing.T) {
		fixture := newPinnedCommitTestFixture(t, "pr-development/commit-pack-layout")
		if err := os.WriteFile(
			filepath.Join(fixture.workspace.Path, "pack-layout.txt"),
			[]byte("must not be snapshotted\n"),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
		entryPath := filepath.Join(
			fixture.workspace.Path,
			".git",
			"objects",
			"pack",
			"non-regular-entry",
		)
		if err := os.Mkdir(entryPath, 0o700); err != nil {
			t.Fatal(err)
		}

		_, err := fixture.manager.SnapshotPinnedCandidate(
			context.Background(),
			PinnedCandidateRequest{Pin: fixture.pin, WorkspaceID: fixture.workspace.ID},
		)
		assertPinnedCommitConflict(t, err, "pack entry non-regular-entry is not a real file")
		if head := testGitCommit(t, fixture.workspace.Path, "HEAD"); head != fixture.pin.ExpectedCommit {
			t.Fatalf("pack-layout rejection moved HEAD to %q", head)
		}
	})

	t.Run("object fanout", func(t *testing.T) {
		fixture := newPinnedCommitTestFixture(t, "pr-development/commit-object-fanout")
		if err := os.WriteFile(
			filepath.Join(fixture.workspace.Path, "fanout.txt"),
			[]byte("must remain local\n"),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
		outside := t.TempDir()
		sentinel := filepath.Join(outside, "sentinel")
		if err := os.WriteFile(sentinel, []byte("unchanged\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		objectsPath := filepath.Join(fixture.workspace.Path, ".git", "objects")
		fanout := ""
		const hexDigits = "0123456789abcdef"
		for _, first := range hexDigits {
			for _, second := range hexDigits {
				name := string([]rune{first, second})
				if _, err := os.Lstat(filepath.Join(objectsPath, name)); os.IsNotExist(err) {
					fanout = name
					break
				}
			}
			if fanout != "" {
				break
			}
		}
		if fanout == "" {
			t.Fatal("all Git object fanout directories unexpectedly exist")
		}
		if err := os.Symlink(outside, filepath.Join(objectsPath, fanout)); err != nil {
			t.Skipf("directory symlinks are unavailable: %v", err)
		}

		_, err := fixture.manager.SnapshotPinnedCandidate(
			context.Background(),
			PinnedCandidateRequest{Pin: fixture.pin, WorkspaceID: fixture.workspace.ID},
		)
		assertPinnedCommitConflict(t, err, "object fanout")
		assertPinnedOutsideDirectoryUnchanged(t, outside, sentinel)
	})

	t.Run("logs", func(t *testing.T) {
		ctx := context.Background()
		fixture := newPinnedCommitTestFixture(t, "pr-development/commit-logs-symlink")
		if err := os.WriteFile(
			filepath.Join(fixture.workspace.Path, "logs.txt"),
			[]byte("validated before metadata drift\n"),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
		candidate := fixture.snapshot(t)
		outside := t.TempDir()
		sentinel := filepath.Join(outside, "sentinel")
		if err := os.WriteFile(sentinel, []byte("unchanged\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		logsPath := filepath.Join(fixture.workspace.Path, ".git", "logs")
		if err := os.Rename(logsPath, logsPath+".original"); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, logsPath); err != nil {
			t.Skipf("directory symlinks are unavailable: %v", err)
		}

		result, err := fixture.manager.CommitPinned(
			ctx,
			fixture.commitRequest(candidate, "Reject escaped Git logs"),
		)
		assertPinnedCommitConflict(t, err, "logs path")
		if result != (PinnedCommitResult{}) {
			t.Fatalf("escaped-logs CommitPinned() result = %#v, want zero value", result)
		}
		if head := testGitCommit(t, fixture.workspace.Path, "HEAD"); head != candidate.ParentCommit {
			t.Fatalf("escaped-logs rejection moved HEAD to %q", head)
		}
		assertPinnedOutsideDirectoryUnchanged(t, outside, sentinel)
	})
}

func TestManagerCommitPinnedRejectsHardLinkedHEADLog(t *testing.T) {
	ctx := context.Background()
	fixture := newPinnedCommitTestFixture(t, "pr-development/commit-hard-link-head-log")
	if err := os.WriteFile(
		filepath.Join(fixture.workspace.Path, "hardlink.txt"),
		[]byte("validated repair\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	headLogPath := filepath.Join(fixture.workspace.Path, ".git", "logs", "HEAD")
	original, readErr := os.ReadFile(headLogPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	outsidePath := filepath.Join(t.TempDir(), "outside-head-log")
	if writeErr := os.WriteFile(outsidePath, original, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if removeErr := os.Remove(headLogPath); removeErr != nil {
		t.Fatal(removeErr)
	}
	if linkErr := os.Link(outsidePath, headLogPath); linkErr != nil {
		t.Skipf("hard links are unavailable: %v", linkErr)
	}

	_, err := fixture.manager.SnapshotPinnedCandidate(ctx, PinnedCandidateRequest{
		Pin:         fixture.pin,
		WorkspaceID: fixture.workspace.ID,
	})
	assertPinnedCommitConflict(t, err, "HEAD log")
	outside, readErr := os.ReadFile(outsidePath)
	if readErr != nil || !bytes.Equal(outside, original) {
		t.Fatalf("outside HEAD log changed: content=%q error=%v", outside, readErr)
	}
	if head := testGitCommit(t, fixture.workspace.Path, "HEAD"); head != fixture.pin.ExpectedCommit {
		t.Fatalf("hard-link rejection moved HEAD to %q, want %q", head, fixture.pin.ExpectedCommit)
	}
}

func TestManagerReleasePinnedRejectsPreservationRefSymlinkEscapes(t *testing.T) {
	newAppliedFixture := func(t *testing.T, reservation string) (pinnedCommitTestFixture, string) {
		t.Helper()
		fixture := newPinnedCommitTestFixture(t, reservation)
		if err := os.WriteFile(
			filepath.Join(fixture.workspace.Path, "preserve.txt"),
			[]byte("committed local repair\n"),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
		candidate := fixture.snapshot(t)
		result, err := fixture.manager.CommitPinned(
			context.Background(),
			fixture.commitRequest(candidate, "Commit before preservation"),
		)
		if err != nil {
			t.Fatalf("CommitPinned() error = %v", err)
		}
		return fixture, result.Commit
	}

	tests := []struct {
		name        string
		reservation string
		escapePath  func(string) string
	}{
		{
			name:        "branch refs",
			reservation: "pr-development/release-ref-escape",
			escapePath: func(workspace string) string {
				return filepath.Join(workspace, ".git", "refs", "heads", "picoclaw")
			},
		},
		{
			name:        "branch logs",
			reservation: "pr-development/release-log-ref-escape",
			escapePath: func(workspace string) string {
				return filepath.Join(
					workspace,
					".git",
					"logs",
					"refs",
					"heads",
					"picoclaw",
				)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, applied := newAppliedFixture(t, test.reservation)
			outside := t.TempDir()
			sentinel := filepath.Join(outside, "sentinel")
			if err := os.WriteFile(sentinel, []byte("unchanged\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			escapePath := test.escapePath(fixture.workspace.Path)
			if err := os.MkdirAll(filepath.Dir(escapePath), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, escapePath); err != nil {
				t.Skipf("directory symlinks are unavailable: %v", err)
			}

			released, err := fixture.manager.ReleasePinned(
				context.Background(),
				PinnedReleaseRequest{
					ReservationKey: fixture.pin.ReservationKey,
					AgentID:        fixture.pin.AgentID,
				},
			)
			if err == nil {
				t.Fatalf("ReleasePinned() = %#v, nil; want metadata-layout rejection", released)
			}
			if len(released) != 0 {
				t.Fatalf("ReleasePinned() after ref escape = %#v, want no released workspace", released)
			}
			if head := testGitCommit(t, fixture.workspace.Path, "HEAD"); head != applied {
				t.Fatalf("ReleasePinned() moved HEAD to %q, want %q", head, applied)
			}
			assertPinnedOutsideDirectoryUnchanged(t, outside, sentinel)
		})
	}
}

func TestManagerWithPinnedOperationScopesAndSerializesSnapshots(t *testing.T) {
	ctx := context.Background()
	source := initSourceRepo(t)
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	root := filepath.Join(t.TempDir(), "git-workspaces")
	first := newTestManagerAtRoot(t, root, &now)
	second := newTestManagerAtRoot(t, root, &now)
	pin := PinnedAcquireRequest{
		Repository:     source,
		SourceRef:      "main",
		ExpectedCommit: testGitCommit(t, source, "HEAD"),
		ReservationKey: "pr-development/commit-serialized",
		AgentID:        "main",
	}
	workspace, err := first.AcquirePinned(ctx, pin)
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(
		filepath.Join(workspace.Path, "serialized.txt"),
		[]byte("one operation at a time\n"),
		0o644,
	); writeErr != nil {
		t.Fatal(writeErr)
	}
	type snapshotResult struct {
		candidate PinnedCandidate
		err       error
	}
	secondDone := make(chan snapshotResult, 1)
	secondStarted := make(chan struct{})
	secondContext, cancelSecond := context.WithTimeout(ctx, 2*time.Second)
	defer cancelSecond()
	leakedContextChannel := make(chan context.Context, 1)
	err = first.WithPinnedOperation(ctx, pin, func(operationContext context.Context) error {
		leakedContextChannel <- operationContext
		nested, nestedErr := first.SnapshotPinnedCandidate(
			operationContext,
			PinnedCandidateRequest{Pin: pin, WorkspaceID: workspace.ID},
		)
		if nestedErr != nil {
			t.Fatalf("nested SnapshotPinnedCandidate() error = %v", nestedErr)
		}
		if nested.ChangedFiles != 1 {
			t.Fatalf("nested SnapshotPinnedCandidate() = %#v", nested)
		}
		go func() {
			close(secondStarted)
			candidate, snapshotErr := second.SnapshotPinnedCandidate(
				secondContext,
				PinnedCandidateRequest{Pin: pin, WorkspaceID: workspace.ID},
			)
			secondDone <- snapshotResult{candidate: candidate, err: snapshotErr}
		}()
		<-secondStarted
		select {
		case result := <-secondDone:
			t.Fatalf("second Manager completed inside callback: %#v, %v", result.candidate, result.err)
		case <-time.After(150 * time.Millisecond):
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithPinnedOperation() error = %v", err)
	}
	leakedContext := <-leakedContextChannel
	result := <-secondDone
	if result.err != nil || result.candidate.ChangedFiles != 1 {
		t.Fatalf("second Manager after callback = %#v, %v", result.candidate, result.err)
	}
	if leakedContext == nil {
		t.Fatal("WithPinnedOperation() did not provide a derived context")
	}

	err = second.WithPinnedOperation(ctx, pin, func(context.Context) error {
		blockedContext, cancel := context.WithTimeout(leakedContext, 150*time.Millisecond)
		defer cancel()
		_, snapshotErr := first.SnapshotPinnedCandidate(
			blockedContext,
			PinnedCandidateRequest{Pin: pin, WorkspaceID: workspace.ID},
		)
		if !errors.Is(snapshotErr, context.DeadlineExceeded) {
			t.Fatalf(
				"SnapshotPinnedCandidate() with leaked context error = %v, want deadline exceeded",
				snapshotErr,
			)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("second WithPinnedOperation() error = %v", err)
	}
	final, err := first.SnapshotPinnedCandidate(ctx, PinnedCandidateRequest{
		Pin:         pin,
		WorkspaceID: workspace.ID,
	})
	if err != nil || final.ChangedFiles != 1 {
		t.Fatalf("final SnapshotPinnedCandidate() = %#v, %v", final, err)
	}
}

func assertPinnedCommitConflict(t *testing.T, err error, contains string) {
	t.Helper()
	if !errors.Is(err, ErrPinnedCommitConflict) ||
		(contains != "" && !strings.Contains(err.Error(), contains)) {
		t.Fatalf("error = %v, want ErrPinnedCommitConflict containing %q", err, contains)
	}
}

func testGitObject(t *testing.T, directory string, args ...string) string {
	t.Helper()
	output, err := runGit(context.Background(), directory, args...)
	if err != nil {
		t.Fatalf("git %s error = %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(output)
}

func assertPinnedOutsideDirectoryUnchanged(t *testing.T, directory, sentinel string) {
	t.Helper()
	content, err := os.ReadFile(sentinel)
	if err != nil || string(content) != "unchanged\n" {
		t.Fatalf("outside sentinel = %q, %v", content, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(sentinel) {
		t.Fatalf("outside directory entries = %#v, want only sentinel", entries)
	}
}

func pinnedCommitTestTime() time.Time {
	return time.Date(2026, 8, 8, 14, 15, 16, 0, time.UTC)
}
