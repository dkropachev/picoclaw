package gitworkspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManagerAcquireReleasePreservesChangesAndCleansIgnored(t *testing.T) {
	ctx := context.Background()
	source := initSourceRepo(t)
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &now)

	acquired, err := manager.Acquire(ctx, AcquireRequest{
		Repository: source,
		SessionKey: "session/main",
		AgentID:    "main",
	})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if acquired.LockedBy == nil || acquired.LockedBy.SessionKey != "session/main" {
		t.Fatalf("workspace lock = %+v, want session/main", acquired.LockedBy)
	}

	if writeErr := os.WriteFile(filepath.Join(acquired.Path, "change.txt"), []byte("work\n"), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}
	if mkdirErr := os.MkdirAll(filepath.Join(acquired.Path, "ignored"), 0o755); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	if writeErr := os.WriteFile(
		filepath.Join(acquired.Path, "ignored", "blob.bin"),
		[]byte(strings.Repeat("x", 4096)),
		0o644,
	); writeErr != nil {
		t.Fatal(writeErr)
	}

	stats, err := manager.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	if stats.IgnoredBytes == 0 {
		t.Fatal("Stats().IgnoredBytes = 0, want ignored files counted")
	}

	released, err := manager.ReleaseSession(ctx, ReleaseRequest{
		SessionKey: "session/main",
		AgentID:    "main",
	})
	if err != nil {
		t.Fatalf("ReleaseSession() error = %v", err)
	}
	if len(released) != 1 {
		t.Fatalf("released count = %d, want 1", len(released))
	}
	if released[0].LockedBy != nil {
		t.Fatalf("released lock = %+v, want nil", released[0].LockedBy)
	}
	if released[0].PreservedBranch == "" {
		t.Fatal("released preserved branch is empty")
	}
	log, err := runGit(ctx, acquired.Path, "log", "-1", "--pretty=%s")
	if err != nil {
		t.Fatalf("git log error = %v", err)
	}
	if strings.TrimSpace(log) != "Preserve PicoClaw workspace changes" {
		t.Fatalf("last commit subject = %q", strings.TrimSpace(log))
	}

	cleaned, err := manager.CleanupIgnored(ctx, acquired.ID)
	if err != nil {
		t.Fatalf("CleanupIgnored() error = %v", err)
	}
	if cleaned.Before == 0 || cleaned.After != 0 {
		t.Fatalf(
			"cleanup ignored bytes before/after = %d/%d, want >0/0",
			cleaned.Before,
			cleaned.After,
		)
	}
	if _, err := os.Stat(filepath.Join(acquired.Path, "ignored", "blob.bin")); !os.IsNotExist(err) {
		t.Fatalf("ignored file stat error = %v, want not exist", err)
	}
}

func TestManagerAllocatesSeparateWorkspaceWhenRepoLocked(t *testing.T) {
	ctx := context.Background()
	source := initSourceRepo(t)
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &now)

	first, err := manager.Acquire(ctx, AcquireRequest{Repository: source, SessionKey: "s1"})
	if err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}
	second, err := manager.Acquire(ctx, AcquireRequest{Repository: source, SessionKey: "s2"})
	if err != nil {
		t.Fatalf("second Acquire() error = %v", err)
	}
	if first.ID == second.ID {
		t.Fatalf("workspace IDs both %q, want separate locked checkouts", first.ID)
	}
}

func TestManagerFreshAcquireObservesAdvancedRemoteInsteadOfReusingCachedHead(t *testing.T) {
	ctx := context.Background()
	source := initSourceRepo(t)
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &now)
	first, err := manager.Acquire(ctx, AcquireRequest{
		Repository: source, Ref: "main", Fresh: true, SessionKey: "review-run-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, releaseErr := manager.ReleaseSession(ctx, ReleaseRequest{SessionKey: "review-run-1"}); releaseErr != nil {
		t.Fatal(releaseErr)
	}
	if writeErr := os.WriteFile(filepath.Join(source, "advanced.txt"), []byte("advanced\n"), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}
	if _, gitErr := runGit(ctx, source, "add", "advanced.txt"); gitErr != nil {
		t.Fatal(gitErr)
	}
	if _, gitErr := runGit(ctx, source, "commit", "-m", "advance remote"); gitErr != nil {
		t.Fatal(gitErr)
	}
	want := testGitCommit(t, source, "HEAD")
	second, err := manager.Acquire(ctx, AcquireRequest{
		Repository: source, Ref: "main", Fresh: true, SessionKey: "review-run-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	stats, statsErr := manager.Stats(ctx)
	if first.ID != second.ID || testGitCommit(t, second.Path, "HEAD") != want ||
		statsErr != nil || stats.WorkspaceCount != 1 {
		t.Fatalf("fresh workspaces first=%#v second=%#v want HEAD=%s", first, second, want)
	}
}

func TestManagerFreshAcquireSkipsExternallyDirtiedUnlockedSnapshot(t *testing.T) {
	ctx := context.Background()
	source := initSourceRepo(t)
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &now)
	first, err := manager.Acquire(ctx, AcquireRequest{
		Repository: source, Ref: "main", Fresh: true, SessionKey: "fresh-dirty-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, releaseErr := manager.ReleaseSession(ctx, ReleaseRequest{SessionKey: "fresh-dirty-1"}); releaseErr != nil {
		t.Fatal(releaseErr)
	}
	dirtyPath := filepath.Join(first.Path, "external-edit.txt")
	if writeErr := os.WriteFile(dirtyPath, []byte("preserve me\n"), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}
	second, err := manager.Acquire(ctx, AcquireRequest{
		Repository: source, Ref: "main", Fresh: true, SessionKey: "fresh-dirty-2",
	})
	if err != nil || second.ID == first.ID {
		t.Fatalf("fresh acquire after external edit first=%#v second=%#v err=%v", first, second, err)
	}
	if content, err := os.ReadFile(dirtyPath); err != nil || string(content) != "preserve me\n" {
		t.Fatalf("external edit content=%q err=%v", content, err)
	}
}

func TestManagerFreshAcquireDoesNotFallBackToDeletedCachedBranch(t *testing.T) {
	ctx := context.Background()
	source := initSourceRepo(t)
	if _, err := runGit(ctx, source, "branch", "review-topic"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &now)
	if _, err := manager.Acquire(ctx, AcquireRequest{
		Repository: source, Ref: "review-topic", Fresh: true, SessionKey: "deleted-ref-1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ReleaseSession(ctx, ReleaseRequest{SessionKey: "deleted-ref-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(ctx, source, "branch", "-D", "review-topic"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Acquire(ctx, AcquireRequest{
		Repository: source, Ref: "review-topic", Fresh: true, SessionKey: "deleted-ref-2",
	}); err == nil || !strings.Contains(err.Error(), "ref is unavailable") {
		t.Fatalf("deleted branch reused cached ref: %v", err)
	}
}

func TestManagerPreservesNetworkOriginIdentityForLocalSourceCheckout(t *testing.T) {
	source := initSourceRepo(t)
	if _, err := runGit(
		context.Background(),
		source,
		"remote",
		"add",
		"origin",
		"git@github.com:Owner/Repo.git",
	); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &now)
	workspace, err := manager.Acquire(context.Background(), AcquireRequest{
		Repository: source, Fresh: true, SessionKey: "local-upstream-review",
	})
	if err != nil || workspace.UpstreamURL != "git@github.com:Owner/Repo.git" {
		t.Fatalf("local-source workspace=%#v err=%v", workspace, err)
	}
}

func TestManagerRejectsDifferentRefForSameSessionReservation(t *testing.T) {
	ctx := context.Background()
	source := initSourceRepo(t)
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &now)
	if _, err := manager.Acquire(ctx, AcquireRequest{
		Repository: source, Ref: "HEAD", SessionKey: "workflow-run:one",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Acquire(ctx, AcquireRequest{
		Repository: source, Ref: "main", SessionKey: "workflow-run:one",
	}); err == nil || !strings.Contains(err.Error(), "different ref") {
		t.Fatalf("different-ref reacquire error=%v", err)
	}
}

func TestManagerRejectsOptionLikeGenericRef(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &now)
	if _, err := manager.Acquire(context.Background(), AcquireRequest{
		Repository: initSourceRepo(t), Ref: "--detach", SessionKey: "unsafe-ref",
	}); err == nil || !strings.Contains(err.Error(), "ref is invalid") {
		t.Fatalf("option-like ref error=%v", err)
	}
}

func TestManagerRejectsCredentialedRepositoryURL(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &now)
	if _, err := manager.Acquire(context.Background(), AcquireRequest{
		Repository: "https://x-access-token:secret@github.com/owner/repo.git",
		SessionKey: "credentialed-url",
	}); err == nil || !strings.Contains(err.Error(), "credentials outside the URL") {
		t.Fatalf("credentialed URL error=%v", err)
	}
}

func TestValidateDevelopmentLineInventoryRejectsUnsafeFreshSnapshotIdentity(t *testing.T) {
	remote := filepath.Clean(t.TempDir())
	repositoryID := repoID(remote)
	valid := func() *storeState {
		return &storeState{
			Repositories: map[string]*RepositoryRecord{
				repositoryID: {ID: repositoryID, RemoteURL: remote},
			},
			Workspaces: map[string]*WorkspaceRecord{
				"fresh": {ID: "fresh", RepoID: repositoryID, RemoteURL: remote, FreshSnapshot: true},
			},
			DevelopmentLines: make(map[string]*developmentLineRecord),
		}
	}
	state := valid()
	state.Workspaces["fresh"].UpstreamURL = "https://token@github.com/owner/repo.git"
	if err := validateDevelopmentLineInventory(
		state,
	); err == nil ||
		!strings.Contains(err.Error(), "upstream identity") {
		t.Fatalf("unsafe upstream error = %v", err)
	}
	state = valid()
	state.Workspaces["fresh"].PreservedBranch = "preserved/branch"
	if err := validateDevelopmentLineInventory(
		state,
	); err == nil ||
		!strings.Contains(err.Error(), "fresh git workspace snapshot") {
		t.Fatalf("fresh/preserved identity error = %v", err)
	}
}

func TestFindFreshReusableWorkspaceSkipsUnsafeCachedSnapshots(t *testing.T) {
	repositoryID := "repo"
	locked := &LockInfo{SessionKey: "active"}
	droppedAt := time.Now()
	state := &storeState{Workspaces: map[string]*WorkspaceRecord{
		"ordinary":  {ID: "ordinary", RepoID: repositoryID},
		"locked":    {ID: "locked", RepoID: repositoryID, FreshSnapshot: true, LockedBy: locked},
		"dropped":   {ID: "dropped", RepoID: repositoryID, FreshSnapshot: true, DroppedAt: &droppedAt},
		"preserved": {ID: "preserved", RepoID: repositoryID, FreshSnapshot: true, PreservedBranch: "saved"},
		"private":   {ID: "private", RepoID: repositoryID, FreshSnapshot: true, PinnedSourceRef: "main"},
		"usable":    {ID: "usable", RepoID: repositoryID, FreshSnapshot: true},
	}}
	if got := (&Manager{}).findFreshReusableWorkspaceLocked(state, repositoryID); got == nil || got.ID != "usable" {
		t.Fatalf("fresh reusable workspace = %#v, want usable", got)
	}
	state.Workspaces["usable"].LockedBy = locked
	if got := (&Manager{}).findFreshReusableWorkspaceLocked(state, repositoryID); got != nil {
		t.Fatalf("unsafe fresh cache was selected: %#v", got)
	}
}

func TestRecloneFreshWorkspaceRejectsUnsafeAndChangedOrigins(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &now)
	repository := &RepositoryRecord{ID: "repo"}
	state := &storeState{Workspaces: make(map[string]*WorkspaceRecord)}
	if err := manager.recloneFreshWorkspaceLocked(
		ctx, state, &WorkspaceRecord{}, repository, "remote", "main", now,
	); err == nil || !strings.Contains(err.Error(), "reuse is not safe") {
		t.Fatalf("unsafe fresh reuse error = %v", err)
	}

	newWorkspace := func(t *testing.T) (string, *WorkspaceRecord, string) {
		t.Helper()
		source := initSourceRepo(t)
		clone := filepath.Join(t.TempDir(), "clone")
		if _, err := runGit(ctx, "", "clone", "--", source, clone); err != nil {
			t.Fatal(err)
		}
		return source, &WorkspaceRecord{
			ID:            "fresh",
			RepoID:        "repo",
			RemoteURL:     source,
			Path:          clone,
			FreshSnapshot: true,
		}, clone
	}

	t.Run("missing origin", func(t *testing.T) {
		source, workspace, clone := newWorkspace(t)
		if _, err := runGit(ctx, clone, "remote", "remove", "origin"); err != nil {
			t.Fatal(err)
		}
		if err := manager.recloneFreshWorkspaceLocked(
			ctx,
			state,
			workspace,
			repository,
			source,
			"main",
			now,
		); err == nil {
			t.Fatal("fresh workspace without origin was reused")
		}
	})

	t.Run("multiple origins", func(t *testing.T) {
		source, workspace, clone := newWorkspace(t)
		if _, err := runGit(ctx, clone, "remote", "set-url", "--add", "origin", source+"-other"); err != nil {
			t.Fatal(err)
		}
		if err := manager.recloneFreshWorkspaceLocked(
			ctx,
			state,
			workspace,
			repository,
			source,
			"main",
			now,
		); err == nil ||
			!strings.Contains(err.Error(), "origin is invalid") {
			t.Fatalf("multiple-origin error = %v", err)
		}
	})

	t.Run("changed origin", func(t *testing.T) {
		source, workspace, clone := newWorkspace(t)
		other := initSourceRepo(t)
		if _, err := runGit(ctx, clone, "remote", "set-url", "origin", other); err != nil {
			t.Fatal(err)
		}
		if err := manager.recloneFreshWorkspaceLocked(
			ctx,
			state,
			workspace,
			repository,
			source,
			"main",
			now,
		); err == nil ||
			!strings.Contains(err.Error(), "origin changed") {
			t.Fatalf("changed-origin error = %v", err)
		}
	})

	t.Run("unavailable remote", func(t *testing.T) {
		source, workspace, _ := newWorkspace(t)
		if err := os.RemoveAll(source); err != nil {
			t.Fatal(err)
		}
		if err := manager.recloneFreshWorkspaceLocked(
			ctx,
			state,
			workspace,
			repository,
			source,
			"main",
			now,
		); err == nil {
			t.Fatal("unavailable fresh remote was reused")
		}
	})
}

func TestConfigureFreshWorkspaceUpstreamAddsUpdatesAndRemovesRemote(t *testing.T) {
	ctx := context.Background()
	source := initSourceRepo(t)
	clone := filepath.Join(t.TempDir(), "clone")
	if _, err := runGit(ctx, "", "clone", "--", source, clone); err != nil {
		t.Fatal(err)
	}
	first := "git@github.com:owner/one.git"
	second := "git@github.com:owner/two.git"
	if err := configureFreshWorkspaceUpstream(ctx, clone, first); err != nil {
		t.Fatal(err)
	}
	if err := configureFreshWorkspaceUpstream(ctx, clone, second); err != nil {
		t.Fatal(err)
	}
	if got, err := runGit(ctx, clone, "remote", "get-url", "picoclaw-upstream"); err != nil ||
		strings.TrimSpace(got) != second {
		t.Fatalf("updated upstream = %q, %v", got, err)
	}
	if err := configureFreshWorkspaceUpstream(ctx, clone, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(ctx, clone, "remote", "get-url", "picoclaw-upstream"); err == nil {
		t.Fatal("empty upstream did not remove preserved remote")
	}
}

func TestLocalRepositoryRemoteOriginRequiresOneSafeNetworkOrigin(t *testing.T) {
	ctx := context.Background()
	if got := localRepositoryRemoteOrigin(ctx, "relative/repo"); got != "" {
		t.Fatalf("relative repository origin = %q", got)
	}
	repo := initSourceRepo(t)
	if got := localRepositoryRemoteOrigin(ctx, repo); got != "" {
		t.Fatalf("repository without origin = %q", got)
	}
	if _, err := runGit(ctx, repo, "remote", "add", "origin", "http://Example.com/Owner/Repo.git"); err != nil {
		t.Fatal(err)
	}
	if got := localRepositoryRemoteOrigin(ctx, repo); got != "http://example.com/Owner/Repo.git" {
		t.Fatalf("safe network origin = %q", got)
	}
	if _, err := runGit(
		ctx,
		repo,
		"remote",
		"set-url",
		"--add",
		"origin",
		"https://github.com/other/repo.git",
	); err != nil {
		t.Fatal(err)
	}
	if got := localRepositoryRemoteOrigin(ctx, repo); got != "" {
		t.Fatalf("multiple network origins = %q", got)
	}
	if _, err := runGit(
		ctx,
		repo,
		"remote",
		"set-url",
		"--delete",
		"origin",
		"https://github.com/other/repo.git",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(
		ctx,
		repo,
		"remote",
		"set-url",
		"origin",
		"https://token@github.com/owner/repo.git",
	); err != nil {
		t.Fatal(err)
	}
	if got := localRepositoryRemoteOrigin(ctx, repo); got != "" {
		t.Fatalf("credentialed network origin = %q", got)
	}
}

func TestManagerAcquirePinnedChecksOutExactDetachedCommit(t *testing.T) {
	ctx := context.Background()
	source := initSourceRepo(t)
	expected := testGitCommit(t, source, "HEAD")
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &now)

	acquired, err := manager.AcquirePinned(ctx, PinnedAcquireRequest{
		Repository:     source,
		SourceRef:      "main",
		ExpectedCommit: expected,
		ReservationKey: "pr-development/case-1",
		AgentID:        "main",
	})
	if err != nil {
		t.Fatalf("AcquirePinned() error = %v", err)
	}
	if acquired.Ref != "main" || acquired.LockedBy == nil ||
		acquired.LockedBy.SessionKey != "pr-development/case-1" ||
		acquired.LockedBy.AgentID != "main" {
		t.Fatalf("AcquirePinned() = %#v", acquired)
	}
	if head := testGitCommit(t, acquired.Path, "HEAD"); head != expected {
		t.Fatalf("pinned HEAD = %q, want %q", head, expected)
	}
	if _, branchErr := runGit(ctx, acquired.Path, "symbolic-ref", "-q", "HEAD"); branchErr == nil {
		t.Fatal("pinned checkout is attached to a branch")
	}
	status, err := runGit(ctx, acquired.Path, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		t.Fatalf("git status error = %v", err)
	}
	if strings.TrimSpace(status) != "" || acquired.Dirty {
		t.Fatalf("pinned workspace status = %q, Dirty = %v", status, acquired.Dirty)
	}
}

func TestManagerAcquirePinnedRejectsMovedSourceRefWithoutPersistingLock(t *testing.T) {
	ctx := context.Background()
	source := initSourceRepo(t)
	stale := testGitCommit(t, source, "HEAD")
	if err := os.WriteFile(filepath.Join(source, "moved.txt"), []byte("new tip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(ctx, source, "add", "moved.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(ctx, source, "commit", "-m", "move branch"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &now)

	_, err := manager.AcquirePinned(ctx, PinnedAcquireRequest{
		Repository:     source,
		SourceRef:      "main",
		ExpectedCommit: stale,
		ReservationKey: "pr-development/stale",
		AgentID:        "main",
	})
	if err == nil || !strings.Contains(err.Error(), "resolved to") {
		t.Fatalf("AcquirePinned() error = %v, want moved-ref rejection", err)
	}
	stats, statsErr := manager.Stats(ctx)
	if statsErr != nil {
		t.Fatalf("Stats() error = %v", statsErr)
	}
	if stats.LockedWorkspaceCount != 0 || stats.WorkspaceCount != 0 {
		t.Fatalf("Stats() after rejected pin = %#v", stats)
	}
}

func TestManagerAcquirePinnedDoesNotResetDirtyUnlockedWorkspace(t *testing.T) {
	ctx := context.Background()
	source := initSourceRepo(t)
	expected := testGitCommit(t, source, "HEAD")
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &now)

	reusable, acquireErr := manager.Acquire(ctx, AcquireRequest{
		Repository: source,
		SessionKey: "ordinary",
	})
	if acquireErr != nil {
		t.Fatalf("Acquire() error = %v", acquireErr)
	}
	if _, err := manager.ReleaseSession(ctx, ReleaseRequest{SessionKey: "ordinary"}); err != nil {
		t.Fatalf("ReleaseSession() error = %v", err)
	}
	dirtyPath := filepath.Join(reusable.Path, "unowned-work.txt")
	if err := os.WriteFile(dirtyPath, []byte("preserve me\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	pinned, pinErr := manager.AcquirePinned(ctx, PinnedAcquireRequest{
		Repository:     source,
		SourceRef:      "main",
		ExpectedCommit: expected,
		ReservationKey: "pr-development/case-dirty",
		AgentID:        "main",
	})
	if pinErr != nil {
		t.Fatalf("AcquirePinned() error = %v", pinErr)
	}
	if pinned.ID == reusable.ID {
		t.Fatalf("AcquirePinned() reused dirty workspace %q", pinned.ID)
	}
	content, readErr := os.ReadFile(dirtyPath)
	if readErr != nil || string(content) != "preserve me\n" {
		t.Fatalf("dirty workspace content = %q, %v", content, readErr)
	}
}

func TestManagerAcquirePinnedHeartbeatRetainsDescendantAndDirtyWork(t *testing.T) {
	ctx := context.Background()
	source := initSourceRepo(t)
	expected := testGitCommit(t, source, "HEAD")
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &now)
	request := PinnedAcquireRequest{
		Repository:     source,
		SourceRef:      "main",
		ExpectedCommit: expected,
		ReservationKey: "pr-development/case-heartbeat",
		AgentID:        "main",
	}

	first, acquireErr := manager.AcquirePinned(ctx, request)
	if acquireErr != nil {
		t.Fatalf("first AcquirePinned() error = %v", acquireErr)
	}
	if err := os.WriteFile(filepath.Join(first.Path, "committed.txt"), []byte("commit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(ctx, first.Path, "add", "committed.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(ctx, first.Path, "commit", "-m", "case work"); err != nil {
		t.Fatal(err)
	}
	descendant := testGitCommit(t, first.Path, "HEAD")
	if descendant == expected {
		t.Fatal("case work did not create a descendant commit")
	}
	dirtyPath := filepath.Join(first.Path, "dirty.txt")
	if err := os.WriteFile(dirtyPath, []byte("still working\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)

	second, heartbeatErr := manager.AcquirePinned(ctx, request)
	if heartbeatErr != nil {
		t.Fatalf("heartbeat AcquirePinned() error = %v", heartbeatErr)
	}
	if second.ID != first.ID || testGitCommit(t, second.Path, "HEAD") != descendant {
		t.Fatalf("heartbeat reset or replaced workspace: first=%#v second=%#v", first, second)
	}
	if content, readErr := os.ReadFile(dirtyPath); readErr != nil || string(content) != "still working\n" {
		t.Fatalf("heartbeat dirty content = %q, %v", content, readErr)
	}
	if second.LockedBy == nil || !second.LockedBy.HeartbeatAt.Equal(now) ||
		!second.LockedBy.LockedAt.Equal(first.LockedBy.LockedAt) {
		t.Fatalf("heartbeat lock = %#v, first = %#v", second.LockedBy, first.LockedBy)
	}
	if !second.Dirty {
		t.Fatal("heartbeat projection did not retain dirty state")
	}
}

func TestManagerAcquirePinnedHeartbeatSurvivesManagerRestart(t *testing.T) {
	ctx := context.Background()
	source := initSourceRepo(t)
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	root := filepath.Join(t.TempDir(), "git-workspaces")
	request := PinnedAcquireRequest{
		Repository:     source,
		SourceRef:      "main",
		ExpectedCommit: testGitCommit(t, source, "HEAD"),
		ReservationKey: "pr-development/restart",
		AgentID:        "main",
	}
	firstManager := newTestManagerAtRoot(t, root, &now)
	first, acquireErr := firstManager.AcquirePinned(ctx, request)
	if acquireErr != nil {
		t.Fatal(acquireErr)
	}
	dirtyPath := filepath.Join(first.Path, "restart-work.txt")
	if err := os.WriteFile(dirtyPath, []byte("retain me\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	now = now.Add(time.Minute)
	restartedManager := newTestManagerAtRoot(t, root, &now)
	second, heartbeatErr := restartedManager.AcquirePinned(ctx, request)
	if heartbeatErr != nil {
		t.Fatalf("AcquirePinned() after manager restart error = %v", heartbeatErr)
	}
	if second.ID != first.ID || second.Path != first.Path || second.LockedBy == nil ||
		!second.LockedBy.HeartbeatAt.Equal(now) {
		t.Fatalf("restarted heartbeat replaced reservation: first=%#v second=%#v", first, second)
	}
	content, readErr := os.ReadFile(dirtyPath)
	if readErr != nil || string(content) != "retain me\n" {
		t.Fatalf("restarted heartbeat content = %q, %v", content, readErr)
	}
}

func TestManagerReleasePinnedPreservesDescendantAndDirtyWork(t *testing.T) {
	ctx := context.Background()
	source := initSourceRepo(t)
	expected := testGitCommit(t, source, "HEAD")
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &now)
	request := PinnedAcquireRequest{
		Repository:     source,
		SourceRef:      "main",
		ExpectedCommit: expected,
		ReservationKey: "pr-development/preserve",
		AgentID:        "main",
	}
	workspace, acquireErr := manager.AcquirePinned(ctx, request)
	if acquireErr != nil {
		t.Fatal(acquireErr)
	}
	if err := os.WriteFile(filepath.Join(workspace.Path, "committed.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(ctx, workspace.Path, "add", "committed.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(ctx, workspace.Path, "commit", "-m", "descendant"); err != nil {
		t.Fatal(err)
	}
	descendant := testGitCommit(t, workspace.Path, "HEAD")
	if err := os.WriteFile(
		filepath.Join(workspace.Path, ".git", "hooks", "reference-transaction"),
		[]byte("#!/bin/sh\nexit 1\n"),
		0o755,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ReleasePinned(ctx, PinnedReleaseRequest{
		ReservationKey: request.ReservationKey,
		AgentID:        "other",
	}); err == nil || !strings.Contains(err.Error(), "agent") {
		t.Fatalf("mismatched pinned release error = %v", err)
	}

	t.Run("clean descendant", func(t *testing.T) {
		released, releaseErr := manager.ReleasePinned(ctx, PinnedReleaseRequest{
			ReservationKey: request.ReservationKey,
			AgentID:        request.AgentID,
		})
		if releaseErr != nil {
			t.Fatal(releaseErr)
		}
		if len(released) != 1 || released[0].PreservedBranch == "" {
			t.Fatalf("ReleaseSession() = %#v", released)
		}
		branchHead := testGitCommit(t, workspace.Path, released[0].PreservedBranch)
		if branchHead != descendant {
			t.Fatalf("preserved branch head = %q, want descendant %q", branchHead, descendant)
		}
	})

	request.ReservationKey = "pr-development/preserve-dirty"
	dirtyWorkspace, dirtyAcquireErr := manager.AcquirePinned(ctx, request)
	if dirtyAcquireErr != nil {
		t.Fatal(dirtyAcquireErr)
	}
	if err := os.WriteFile(filepath.Join(dirtyWorkspace.Path, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	released, releaseErr := manager.ReleasePinned(ctx, PinnedReleaseRequest{
		ReservationKey: request.ReservationKey,
		AgentID:        request.AgentID,
	})
	if releaseErr != nil {
		t.Fatal(releaseErr)
	}
	if len(released) != 1 || released[0].PreservedBranch == "" || released[0].LockedBy != nil {
		t.Fatalf("dirty pinned release = %#v", released)
	}
	show, showErr := runGit(ctx, dirtyWorkspace.Path, "show", released[0].PreservedBranch+":dirty.txt")
	if showErr != nil || show != "dirty\n" {
		t.Fatalf("preserved dirty content = %q, %v", show, showErr)
	}
}

func TestManagerReleasePinnedKeepsLockForUnpreservableSubmoduleWork(t *testing.T) {
	ctx := context.Background()
	submodule := initSourceRepo(t)
	source := initSourceRepo(t)
	if _, err := runGit(
		ctx,
		source,
		"-c",
		"protocol.file.allow=always",
		"submodule",
		"add",
		"--",
		submodule,
		"nested",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(ctx, source, "commit", "-am", "add submodule"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &now)
	request := PinnedAcquireRequest{
		Repository:     source,
		SourceRef:      "main",
		ExpectedCommit: testGitCommit(t, source, "HEAD"),
		ReservationKey: "pr-development/submodule",
		AgentID:        "main",
	}
	workspace, acquireErr := manager.AcquirePinned(ctx, request)
	if acquireErr != nil {
		t.Fatal(acquireErr)
	}
	if _, err := runGit(
		ctx,
		workspace.Path,
		"clone",
		"--no-local",
		"--",
		submodule,
		"nested",
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(workspace.Path, "nested", "README.md"),
		[]byte("dirty submodule\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ReleasePinned(ctx, PinnedReleaseRequest{
		ReservationKey: request.ReservationKey,
		AgentID:        request.AgentID,
	}); err == nil || !strings.Contains(err.Error(), "no preservable staged changes") {
		t.Fatalf("unpreservable submodule ReleasePinned() error = %v", err)
	}
	manager.mu.Lock()
	unlock, lockErr := manager.lockInventory(ctx)
	if lockErr != nil {
		manager.mu.Unlock()
		t.Fatal(lockErr)
	}
	state, loadErr := manager.loadLocked()
	unlock()
	manager.mu.Unlock()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if state.Workspaces[workspace.ID].LockedBy == nil {
		t.Fatal("unpreservable submodule release cleared pinned lock")
	}
}

func TestManagerGenericAcquireNeverAdoptsReleasedPinnedWorkspace(t *testing.T) {
	ctx := context.Background()
	source := initSourceRepo(t)
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &now)
	pinned, pinErr := manager.AcquirePinned(ctx, PinnedAcquireRequest{
		Repository:     source,
		SourceRef:      "main",
		ExpectedCommit: testGitCommit(t, source, "HEAD"),
		ReservationKey: "pr-development/controller-only",
		AgentID:        "main",
	})
	if pinErr != nil {
		t.Fatal(pinErr)
	}
	turnReleased, err := manager.ReleaseSession(ctx, ReleaseRequest{
		SessionKey: "pr-development/controller-only",
		AgentID:    "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(turnReleased) != 0 {
		t.Fatalf("generic release unlocked pinned reservation: %#v", turnReleased)
	}
	if _, err := manager.ReleasePinned(ctx, PinnedReleaseRequest{
		ReservationKey: "pr-development/controller-only",
		AgentID:        "main",
	}); err != nil {
		t.Fatal(err)
	}
	generic, acquireErr := manager.Acquire(ctx, AcquireRequest{
		Repository: source,
		SessionKey: "agent-facing",
	})
	if acquireErr != nil {
		t.Fatal(acquireErr)
	}
	if generic.ID == pinned.ID || generic.Path == pinned.Path {
		t.Fatalf("generic acquisition adopted pinned checkout: pinned=%#v generic=%#v", pinned, generic)
	}
}

func TestPinnedReservationIgnoresGenericSessionKeyCollision(t *testing.T) {
	ctx := context.Background()
	pinnedSource := initSourceRepo(t)
	genericSource := initSourceRepo(t)
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &now)
	request := PinnedAcquireRequest{
		Repository:     pinnedSource,
		SourceRef:      "main",
		ExpectedCommit: testGitCommit(t, pinnedSource, "HEAD"),
		ReservationKey: "opaque-controller-reservation",
		AgentID:        "developer-agent",
	}
	pinned, pinErr := manager.AcquirePinned(ctx, request)
	if pinErr != nil {
		t.Fatal(pinErr)
	}
	generic, acquireErr := manager.Acquire(ctx, AcquireRequest{
		Repository: genericSource,
		SessionKey: request.ReservationKey,
		AgentID:    "model-agent",
	})
	if acquireErr != nil {
		t.Fatal(acquireErr)
	}
	if _, heartbeatErr := manager.AcquirePinned(ctx, request); heartbeatErr != nil {
		t.Fatalf("generic session-key collision blocked pinned heartbeat: %v", heartbeatErr)
	}
	released, releaseErr := manager.ReleaseSession(ctx, ReleaseRequest{
		SessionKey: request.ReservationKey,
		AgentID:    "model-agent",
	})
	if releaseErr != nil {
		t.Fatal(releaseErr)
	}
	if len(released) != 1 || released[0].ID != generic.ID {
		t.Fatalf("generic release affected wrong reservations: %#v", released)
	}
	if heartbeat, heartbeatErr := manager.AcquirePinned(ctx, request); heartbeatErr != nil ||
		heartbeat.ID != pinned.ID || heartbeat.LockedBy == nil {
		t.Fatalf("pinned reservation after generic release = %#v, %v", heartbeat, heartbeatErr)
	}
}

func TestManagerAcquirePinnedReservationAndWorkspaceIntegrity(t *testing.T) {
	ctx := context.Background()

	t.Run("request identity is immutable", func(t *testing.T) {
		source := initSourceRepo(t)
		other := initSourceRepo(t)
		expected := testGitCommit(t, source, "HEAD")
		now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
		manager := newTestManager(t, &now)
		request := PinnedAcquireRequest{
			Repository:     source,
			SourceRef:      "main",
			ExpectedCommit: expected,
			ReservationKey: "pr-development/identity",
			AgentID:        "main",
		}
		workspace, err := manager.AcquirePinned(ctx, request)
		if err != nil {
			t.Fatalf("AcquirePinned() error = %v", err)
		}

		changedAgent := request
		changedAgent.AgentID = "other"
		if _, err := manager.AcquirePinned(ctx, changedAgent); err == nil {
			t.Fatal("changed agent AcquirePinned() error = nil")
		}
		changedRepository := request
		changedRepository.Repository = other
		if _, err := manager.AcquirePinned(ctx, changedRepository); err == nil {
			t.Fatal("changed repository AcquirePinned() error = nil")
		}
		changedCommit := request
		changedCommit.ExpectedCommit = strings.Repeat("a", 40)
		if _, err := manager.AcquirePinned(ctx, changedCommit); err == nil {
			t.Fatal("changed commit AcquirePinned() error = nil")
		}
		if _, err := runGit(ctx, source, "branch", "same-commit"); err != nil {
			t.Fatal(err)
		}
		changedSourceRef := request
		changedSourceRef.SourceRef = "same-commit"
		if _, err := manager.AcquirePinned(ctx, changedSourceRef); err == nil {
			t.Fatal("changed source ref AcquirePinned() error = nil")
		}
		if head := testGitCommit(t, workspace.Path, "HEAD"); head != expected {
			t.Fatalf("identity mismatch changed HEAD to %q", head)
		}
	})

	t.Run("multiple origin URLs fail heartbeat", func(t *testing.T) {
		source := initSourceRepo(t)
		other := initSourceRepo(t)
		now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
		manager := newTestManager(t, &now)
		request := PinnedAcquireRequest{
			Repository:     source,
			SourceRef:      "main",
			ExpectedCommit: testGitCommit(t, source, "HEAD"),
			ReservationKey: "pr-development/multiple-origin",
			AgentID:        "main",
		}
		workspace, acquireErr := manager.AcquirePinned(ctx, request)
		if acquireErr != nil {
			t.Fatal(acquireErr)
		}
		if _, err := runGit(
			ctx,
			workspace.Path,
			"config",
			"--add",
			"remote.origin.url",
			other,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.AcquirePinned(ctx, request); err == nil ||
			!strings.Contains(err.Error(), "origin") {
			t.Fatalf("multiple origin AcquirePinned() error = %v", err)
		}
	})

	t.Run("tampered origin fails heartbeat", func(t *testing.T) {
		source := initSourceRepo(t)
		other := initSourceRepo(t)
		now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
		manager := newTestManager(t, &now)
		request := PinnedAcquireRequest{
			Repository:     source,
			SourceRef:      "main",
			ExpectedCommit: testGitCommit(t, source, "HEAD"),
			ReservationKey: "pr-development/origin",
			AgentID:        "main",
		}
		workspace, err := manager.AcquirePinned(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runGit(ctx, workspace.Path, "remote", "set-url", "origin", other); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.AcquirePinned(ctx, request); err == nil ||
			!strings.Contains(err.Error(), "origin") {
			t.Fatalf("tampered origin AcquirePinned() error = %v", err)
		}
	})

	t.Run("unrelated head fails heartbeat", func(t *testing.T) {
		source := initSourceRepo(t)
		now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
		manager := newTestManager(t, &now)
		request := PinnedAcquireRequest{
			Repository:     source,
			SourceRef:      "main",
			ExpectedCommit: testGitCommit(t, source, "HEAD"),
			ReservationKey: "pr-development/unrelated",
			AgentID:        "main",
		}
		workspace, err := manager.AcquirePinned(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		// The orphan commit is deliberate workspace tampering for this test.
		// Disable Git's unrelated background maintenance actor before creating it
		// so the heartbeat observes only the intended ancestry violation.
		if _, err := runGit(ctx, workspace.Path, "config", "maintenance.auto", "false"); err != nil {
			t.Fatal(err)
		}
		if _, err := runGit(ctx, workspace.Path, "config", "gc.auto", "0"); err != nil {
			t.Fatal(err)
		}
		if _, err := runGit(ctx, workspace.Path, "checkout", "--orphan", "unrelated"); err != nil {
			t.Fatal(err)
		}
		if _, err := runGit(ctx, workspace.Path, "rm", "-rf", "."); err != nil {
			t.Fatal(err)
		}
		if _, err := runGit(ctx, workspace.Path, "commit", "--allow-empty", "-m", "unrelated"); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.AcquirePinned(ctx, request); err == nil ||
			!strings.Contains(err.Error(), "not an ancestor") {
			t.Fatalf("unrelated HEAD AcquirePinned() error = %v", err)
		}
		if _, err := manager.ReleasePinned(ctx, PinnedReleaseRequest{
			ReservationKey: request.ReservationKey,
			AgentID:        request.AgentID,
		}); err == nil || !strings.Contains(err.Error(), "ancestry") {
			t.Fatalf("unrelated HEAD ReleaseSession() error = %v", err)
		}
		manager.mu.Lock()
		unlock, lockErr := manager.lockInventory(ctx)
		if lockErr != nil {
			manager.mu.Unlock()
			t.Fatal(lockErr)
		}
		state, loadErr := manager.loadLocked()
		unlock()
		manager.mu.Unlock()
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if state.Workspaces[workspace.ID].LockedBy == nil {
			t.Fatal("rejected pinned release cleared the durable lock")
		}
	})
}

func TestManagerAcquirePinnedRejectsGitControlPlaneTampering(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string, string)
		want   string
	}{
		{
			name: "replacement ref",
			mutate: func(t *testing.T, path, commit string) {
				t.Helper()
				if _, err := runGit(
					context.Background(),
					path,
					"update-ref",
					"refs/replace/"+commit,
					commit,
				); err != nil {
					t.Fatal(err)
				}
			},
			want: "replacement refs",
		},
		{
			name: "assume unchanged",
			mutate: func(t *testing.T, path, _ string) {
				t.Helper()
				if _, err := runGit(
					context.Background(),
					path,
					"update-index",
					"--assume-unchanged",
					"README.md",
				); err != nil {
					t.Fatal(err)
				}
			},
			want: "assume-unchanged",
		},
		{
			name: "skip worktree",
			mutate: func(t *testing.T, path, _ string) {
				t.Helper()
				if _, err := runGit(
					context.Background(),
					path,
					"update-index",
					"--skip-worktree",
					"README.md",
				); err != nil {
					t.Fatal(err)
				}
			},
			want: "skip-worktree",
		},
		{
			name: "sparse configuration",
			mutate: func(t *testing.T, path, _ string) {
				t.Helper()
				if _, err := runGit(
					context.Background(),
					path,
					"config",
					"core.sparseCheckout",
					"true",
				); err != nil {
					t.Fatal(err)
				}
			},
			want: "unsafe Git configuration",
		},
		{
			name: "push URL redirection",
			mutate: func(t *testing.T, path, _ string) {
				t.Helper()
				if _, err := runGit(
					context.Background(),
					path,
					"config",
					"--add",
					"remote.origin.pushurl",
					"ssh://attacker.invalid/repository.git",
				); err != nil {
					t.Fatal(err)
				}
			},
			want: "unsafe Git configuration",
		},
		{
			name: "graft file",
			mutate: func(t *testing.T, path, commit string) {
				t.Helper()
				if err := os.WriteFile(
					filepath.Join(path, ".git", "info", "grafts"),
					[]byte(commit+"\n"),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			},
			want: "control file",
		},
		{
			name: "commit graph",
			mutate: func(t *testing.T, path, _ string) {
				t.Helper()
				if err := os.WriteFile(
					filepath.Join(path, ".git", "objects", "info", "commit-graph"),
					[]byte("untrusted commit graph\n"),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			},
			want: "control file",
		},
		{
			name: "local exclude pattern",
			mutate: func(t *testing.T, path, _ string) {
				t.Helper()
				if err := os.WriteFile(
					filepath.Join(path, ".git", "info", "exclude"),
					[]byte("hidden-by-agent\n"),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			},
			want: "exclude file",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			source := initSourceRepo(t)
			expected := testGitCommit(t, source, "HEAD")
			now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
			manager := newTestManager(t, &now)
			request := PinnedAcquireRequest{
				Repository:     source,
				SourceRef:      "main",
				ExpectedCommit: expected,
				ReservationKey: "pr-development/control-" + strings.ReplaceAll(test.name, " ", "-"),
				AgentID:        "main",
			}
			workspace, err := manager.AcquirePinned(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, workspace.Path, expected)
			if _, err := manager.AcquirePinned(ctx, request); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("tampered AcquirePinned() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestManagerAcquirePinnedAllocatesSeparateLockedCheckouts(t *testing.T) {
	ctx := context.Background()
	source := initSourceRepo(t)
	expected := testGitCommit(t, source, "HEAD")
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &now)
	request := PinnedAcquireRequest{
		Repository:     source,
		SourceRef:      "main",
		ExpectedCommit: expected,
		ReservationKey: "pr-development/first",
		AgentID:        "main",
	}
	first, err := manager.AcquirePinned(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	request.ReservationKey = "pr-development/second"
	second, err := manager.AcquirePinned(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || first.Path == second.Path {
		t.Fatalf("separate reservations shared checkout: first=%#v second=%#v", first, second)
	}
}

func TestManagerAcquirePinnedValidatesAuthorityInputsBeforeMutation(t *testing.T) {
	source := initSourceRepo(t)
	expected := testGitCommit(t, source, "HEAD")
	base := PinnedAcquireRequest{
		Repository:     source,
		SourceRef:      "main",
		ExpectedCommit: expected,
		ReservationKey: "pr-development/validation",
		AgentID:        "main",
	}
	tests := []struct {
		name   string
		mutate func(*PinnedAcquireRequest)
	}{
		{name: "blank repository", mutate: func(req *PinnedAcquireRequest) { req.Repository = "" }},
		{name: "repository whitespace", mutate: func(req *PinnedAcquireRequest) { req.Repository += " " }},
		{name: "repository newline", mutate: func(req *PinnedAcquireRequest) { req.Repository += "\nother" }},
		{name: "blank source ref", mutate: func(req *PinnedAcquireRequest) { req.SourceRef = "" }},
		{name: "source ref whitespace", mutate: func(req *PinnedAcquireRequest) { req.SourceRef = " main" }},
		{name: "source ref option", mutate: func(req *PinnedAcquireRequest) { req.SourceRef = "--upload-pack=evil" }},
		{name: "source ref traversal", mutate: func(req *PinnedAcquireRequest) { req.SourceRef = "main..other" }},
		{name: "source ref nul", mutate: func(req *PinnedAcquireRequest) { req.SourceRef = "main\x00other" }},
		{name: "commit whitespace", mutate: func(req *PinnedAcquireRequest) { req.ExpectedCommit += " " }},
		{
			name: "commit uppercase",
			mutate: func(req *PinnedAcquireRequest) {
				req.ExpectedCommit = strings.ToUpper(req.ExpectedCommit)
			},
		},
		{
			name: "commit short",
			mutate: func(req *PinnedAcquireRequest) {
				req.ExpectedCommit = strings.Repeat("a", 39)
			},
		},
		{
			name: "commit nonhex",
			mutate: func(req *PinnedAcquireRequest) {
				req.ExpectedCommit = strings.Repeat("g", 40)
			},
		},
		{name: "blank reservation", mutate: func(req *PinnedAcquireRequest) { req.ReservationKey = "" }},
		{name: "reservation whitespace", mutate: func(req *PinnedAcquireRequest) { req.ReservationKey += " " }},
		{name: "reservation newline", mutate: func(req *PinnedAcquireRequest) { req.ReservationKey += "\nother" }},
		{name: "blank agent", mutate: func(req *PinnedAcquireRequest) { req.AgentID = "" }},
		{name: "agent whitespace", mutate: func(req *PinnedAcquireRequest) { req.AgentID = " main" }},
		{name: "agent newline", mutate: func(req *PinnedAcquireRequest) { req.AgentID += "\nother" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
			manager := newTestManager(t, &now)
			request := base
			test.mutate(&request)
			if _, err := manager.AcquirePinned(context.Background(), request); err == nil {
				t.Fatal("AcquirePinned() error = nil")
			}
			stats, err := manager.Stats(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if stats.WorkspaceCount != 0 || stats.LockedWorkspaceCount != 0 {
				t.Fatalf("invalid request mutated inventory: %#v", stats)
			}
		})
	}
}

func TestManagerAcquirePinnedUsesSanitizedGitEnvironment(t *testing.T) {
	ctx := context.Background()
	source := initSourceRepo(t)
	other := initSourceRepo(t)
	expected := testGitCommit(t, source, "HEAD")
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &now)
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
	t.Setenv("GIT_CONFIG_VALUE_0", filepath.Join(other, "hooks"))

	workspace, err := manager.AcquirePinned(ctx, PinnedAcquireRequest{
		Repository:     source,
		SourceRef:      "main",
		ExpectedCommit: expected,
		ReservationKey: "pr-development/sanitized-environment",
		AgentID:        "main",
	})
	if err != nil {
		t.Fatalf("AcquirePinned() with poisoned Git environment error = %v", err)
	}
	if workspace.CurrentBranch != "" || workspace.Dirty {
		t.Fatalf("poisoned environment changed pinned projection: %#v", workspace)
	}
	environment, cleanup, err := manager.newPinnedGitEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	head, err := resolvePinnedGitCommit(ctx, workspace.Path, "HEAD", environment)
	if err != nil || head != expected {
		t.Fatalf("sanitized pinned HEAD = %q, %v; want %q", head, err, expected)
	}
}

func TestManagerAcquirePinnedSupportsSHA256Repositories(t *testing.T) {
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
		t.Fatalf("SHA-256 commit length = %d, want 64", len(expected))
	}
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &now)
	request := PinnedAcquireRequest{
		Repository:     source,
		SourceRef:      "main",
		ExpectedCommit: expected,
		ReservationKey: "pr-development/sha256",
		AgentID:        "main",
	}
	workspace, err := manager.AcquirePinned(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if head := testGitCommit(t, workspace.Path, "HEAD"); head != expected {
		t.Fatalf("SHA-256 pinned HEAD = %q, want %q", head, expected)
	}
	if _, err := manager.AcquirePinned(ctx, request); err != nil {
		t.Fatalf("SHA-256 heartbeat error = %v", err)
	}
}

func TestManagerAcquirePinnedAcceptsExactLocalOriginContainingSpaces(t *testing.T) {
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "source repository with spaces")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(ctx, source, "init", "-b", "main"); err != nil {
		t.Fatal(err)
	}
	seedSourceRepo(t, source)
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &now)
	if _, err := manager.AcquirePinned(ctx, PinnedAcquireRequest{
		Repository:     source,
		SourceRef:      "main",
		ExpectedCommit: testGitCommit(t, source, "HEAD"),
		ReservationKey: "pr-development/spaced-origin",
		AgentID:        "main",
	}); err != nil {
		t.Fatalf("AcquirePinned() with spaced origin error = %v", err)
	}
}

func TestManagerAcquirePinnedRejectsPathTampering(t *testing.T) {
	ctx := context.Background()

	t.Run("inventory path", func(t *testing.T) {
		source := initSourceRepo(t)
		now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
		manager := newTestManager(t, &now)
		request := PinnedAcquireRequest{
			Repository:     source,
			SourceRef:      "main",
			ExpectedCommit: testGitCommit(t, source, "HEAD"),
			ReservationKey: "pr-development/path-record",
			AgentID:        "main",
		}
		workspace, err := manager.AcquirePinned(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		manager.mu.Lock()
		unlock, err := manager.lockInventory(ctx)
		if err != nil {
			manager.mu.Unlock()
			t.Fatal(err)
		}
		state, err := manager.loadLocked()
		if err == nil {
			state.Workspaces[workspace.ID].Path = source
			err = manager.saveLocked(state)
		}
		unlock()
		manager.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manager.AcquirePinned(ctx, request); err == nil ||
			!strings.Contains(err.Error(), "inventory identity") {
			t.Fatalf("tampered inventory path AcquirePinned() error = %v", err)
		}
	})

	t.Run("symlink substitution", func(t *testing.T) {
		source := initSourceRepo(t)
		now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
		manager := newTestManager(t, &now)
		request := PinnedAcquireRequest{
			Repository:     source,
			SourceRef:      "main",
			ExpectedCommit: testGitCommit(t, source, "HEAD"),
			ReservationKey: "pr-development/path-symlink",
			AgentID:        "main",
		}
		workspace, err := manager.AcquirePinned(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		backup := workspace.Path + "-moved"
		if err := os.Rename(workspace.Path, backup); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(backup, workspace.Path); err != nil {
			t.Skipf("symlink creation unavailable: %v", err)
		}
		if _, err := manager.AcquirePinned(ctx, request); err == nil ||
			!strings.Contains(err.Error(), "real directory") {
			t.Fatalf("symlink-substituted AcquirePinned() error = %v", err)
		}
	})

	t.Run("symlinked Git index", func(t *testing.T) {
		source := initSourceRepo(t)
		now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
		manager := newTestManager(t, &now)
		request := PinnedAcquireRequest{
			Repository:     source,
			SourceRef:      "main",
			ExpectedCommit: testGitCommit(t, source, "HEAD"),
			ReservationKey: "pr-development/index-symlink",
			AgentID:        "main",
		}
		workspace, err := manager.AcquirePinned(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		indexPath := filepath.Join(workspace.Path, ".git", "index")
		backup := indexPath + ".original"
		if err := os.Rename(indexPath, backup); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(backup, indexPath); err != nil {
			t.Skipf("symlink creation unavailable: %v", err)
		}
		if _, err := manager.AcquirePinned(ctx, request); err == nil ||
			!strings.Contains(err.Error(), "metadata file index") {
			t.Fatalf("symlinked index AcquirePinned() error = %v", err)
		}
	})

	t.Run("checkout root substitution", func(t *testing.T) {
		source := initSourceRepo(t)
		now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
		manager := newTestManager(t, &now)
		originalCheckoutRoot := manager.checkoutRoot
		movedCheckoutRoot := originalCheckoutRoot + "-moved"
		if err := os.Rename(originalCheckoutRoot, movedCheckoutRoot); err != nil {
			t.Fatal(err)
		}
		externalCheckoutRoot := t.TempDir()
		if err := os.Symlink(externalCheckoutRoot, originalCheckoutRoot); err != nil {
			t.Skipf("symlink creation unavailable: %v", err)
		}
		if _, err := manager.AcquirePinned(ctx, PinnedAcquireRequest{
			Repository:     source,
			SourceRef:      "main",
			ExpectedCommit: testGitCommit(t, source, "HEAD"),
			ReservationKey: "pr-development/checkout-root-symlink",
			AgentID:        "main",
		}); err == nil || !strings.Contains(err.Error(), "checkout root") {
			t.Fatalf("substituted checkout root AcquirePinned() error = %v", err)
		}
	})

	t.Run("preexisting checkout root symlink", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "git-workspaces")
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(t.TempDir(), filepath.Join(root, "checkouts")); err != nil {
			t.Skipf("symlink creation unavailable: %v", err)
		}
		if _, err := NewManager(Options{RootDir: root}); err == nil ||
			!strings.Contains(err.Error(), "checkout root") {
			t.Fatalf("NewManager() with checkout symlink error = %v", err)
		}
	})

	t.Run("manager root substitution", func(t *testing.T) {
		now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
		parent := t.TempDir()
		root := filepath.Join(parent, "git-workspaces")
		manager := newTestManagerAtRoot(t, root, &now)
		movedRoot := root + "-moved"
		if err := os.Rename(root, movedRoot); err != nil {
			t.Fatal(err)
		}
		externalRoot := t.TempDir()
		if err := os.Symlink(externalRoot, root); err != nil {
			t.Skipf("symlink creation unavailable: %v", err)
		}
		if _, err := manager.Stats(ctx); err == nil ||
			!strings.Contains(err.Error(), "root") {
			t.Fatalf("substituted manager root Stats() error = %v", err)
		}
	})
}

func TestManagerPreservationBranchesAreCreateOnlyAtSameTimestamp(t *testing.T) {
	ctx := context.Background()
	source := initSourceRepo(t)
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &now)
	request := AcquireRequest{Repository: source, SessionKey: "same-time"}
	first, acquireErr := manager.Acquire(ctx, request)
	if acquireErr != nil {
		t.Fatal(acquireErr)
	}
	if err := os.WriteFile(filepath.Join(first.Path, "cycle.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	firstRelease, firstReleaseErr := manager.ReleaseSession(ctx, ReleaseRequest{SessionKey: request.SessionKey})
	if firstReleaseErr != nil {
		t.Fatal(firstReleaseErr)
	}
	if len(firstRelease) != 1 || firstRelease[0].PreservedBranch == "" {
		t.Fatalf("first release = %#v", firstRelease)
	}
	second, secondAcquireErr := manager.Acquire(ctx, request)
	if secondAcquireErr != nil {
		t.Fatal(secondAcquireErr)
	}
	if second.ID != first.ID {
		t.Fatalf("second acquisition ID = %q, want %q", second.ID, first.ID)
	}
	if err := os.WriteFile(filepath.Join(second.Path, "cycle.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	secondRelease, secondReleaseErr := manager.ReleaseSession(ctx, ReleaseRequest{SessionKey: request.SessionKey})
	if secondReleaseErr != nil {
		t.Fatal(secondReleaseErr)
	}
	if len(secondRelease) != 1 || secondRelease[0].PreservedBranch == "" ||
		secondRelease[0].PreservedBranch == firstRelease[0].PreservedBranch ||
		!strings.HasSuffix(secondRelease[0].PreservedBranch, "-2") {
		t.Fatalf("preservation branches first=%#v second=%#v", firstRelease, secondRelease)
	}
	firstHead := testGitCommit(t, second.Path, firstRelease[0].PreservedBranch)
	secondHead := testGitCommit(t, second.Path, secondRelease[0].PreservedBranch)
	if firstHead == secondHead {
		t.Fatalf("create-only preservation refs both point to %q", firstHead)
	}
}

func TestManagerAcquirePreservesSafeHTTPSGitHubTransport(t *testing.T) {
	ctx := context.Background()
	source := initSourceRepo(t)
	canonical := "https://github.com/scylladb/alternator-client-java.git"
	configPath := filepath.Join(t.TempDir(), "gitconfig")
	sourceURL := "file://" + filepath.ToSlash(source)
	if _, err := runGit(
		ctx,
		"",
		"config",
		"--file",
		configPath,
		"url."+sourceURL+".insteadOf",
		canonical,
	); err != nil {
		t.Fatalf("git config insteadOf error = %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", configPath)

	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &now)
	acquired, err := manager.Acquire(ctx, AcquireRequest{
		Repository: canonical,
		SessionKey: "s1",
	})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if acquired.RemoteURL != canonical {
		t.Fatalf("RemoteURL = %q, want %q", acquired.RemoteURL, canonical)
	}

	if _, releaseErr := manager.ReleaseSession(ctx, ReleaseRequest{SessionKey: "s1"}); releaseErr != nil {
		t.Fatalf("ReleaseSession() error = %v", releaseErr)
	}
	reacquired, err := manager.Acquire(ctx, AcquireRequest{
		Repository: canonical,
		SessionKey: "s2",
	})
	if err != nil {
		t.Fatalf("reacquire with HTTPS remote error = %v", err)
	}
	if reacquired.ID != acquired.ID {
		t.Fatalf("reacquired workspace ID = %q, want %q", reacquired.ID, acquired.ID)
	}

	stats, err := manager.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	if stats.RepositoryCount != 1 {
		t.Fatalf("RepositoryCount = %d, want 1", stats.RepositoryCount)
	}
	if got := stats.Repositories[0].RemoteURL; got != canonical {
		t.Fatalf("registered remote URL = %q, want %q", got, canonical)
	}
}

func TestNormalizeRepositoryPrefersSSHRemoteWhenSafe(t *testing.T) {
	tests := []struct {
		name    string
		repo    string
		want    string
		wantErr bool
	}{
		{
			name: "github https",
			repo: "https://github.com/scylladb/alternator-client-java.git",
			want: "https://github.com/scylladb/alternator-client-java.git",
		},
		{
			name: "github https without suffix",
			repo: "https://github.com/scylladb/alternator-client-java",
			want: "https://github.com/scylladb/alternator-client-java.git",
		},
		{
			name: "github git protocol",
			repo: "git://github.com/scylladb/alternator-client-java.git",
			want: "git://github.com/scylladb/alternator-client-java.git",
		},
		{
			name: "github ssh url",
			repo: "ssh://git@github.com/scylladb/alternator-client-java.git",
			want: "git@github.com:scylladb/alternator-client-java.git",
		},
		{
			name: "github ssh default port",
			repo: "ssh://git@github.com:22/scylladb/alternator-client-java.git",
			want: "git@github.com:scylladb/alternator-client-java.git",
		},
		{
			name: "scp remote without suffix",
			repo: "git@github.com:scylladb/alternator-client-java",
			want: "git@github.com:scylladb/alternator-client-java.git",
		},
		{
			name: "gitlab nested group",
			repo: "https://gitlab.com/group/subgroup/repo.git",
			want: "https://gitlab.com/group/subgroup/repo.git",
		},
		{
			name:    "web page path remains original",
			repo:    "https://github.com/scylladb/alternator-client-java/tree/main",
			wantErr: true,
		},
		{
			name:    "credentials are rejected",
			repo:    "https://token@github.com/scylladb/alternator-client-java.git",
			wantErr: true,
		},
		{
			name:    "custom port remains original",
			repo:    "https://github.com:8443/scylladb/alternator-client-java.git",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeRepository(tt.repo)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("normalizeRepository() = %q, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeRepository() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("normalizeRepository() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestManagerReconcileDropsOldUnlockedWorkspace(t *testing.T) {
	ctx := context.Background()
	source := initSourceRepo(t)
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &now)

	acquired, err := manager.Acquire(ctx, AcquireRequest{Repository: source, SessionKey: "s1"})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if _, releaseErr := manager.ReleaseSession(ctx, ReleaseRequest{SessionKey: "s1"}); releaseErr != nil {
		t.Fatalf("ReleaseSession() error = %v", releaseErr)
	}
	now = now.Add(49 * time.Hour)

	result, err := manager.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(result.Dropped) != 1 {
		t.Fatalf("dropped count = %d, want 1", len(result.Dropped))
	}
	if result.Dropped[0].ID != acquired.ID {
		t.Fatalf("dropped workspace = %q, want %q", result.Dropped[0].ID, acquired.ID)
	}
	if _, err := os.Stat(acquired.Path); !os.IsNotExist(err) {
		t.Fatalf("dropped path stat error = %v, want not exist", err)
	}
}

func TestManagerCoordinatesInventoryAcrossInstances(t *testing.T) {
	ctx := context.Background()
	source := initSourceRepo(t)
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	root := filepath.Join(t.TempDir(), "git-workspaces")
	first := newTestManagerAtRoot(t, root, &now)
	second := newTestManagerAtRoot(t, root, &now)

	acquired, err := first.Acquire(ctx, AcquireRequest{Repository: source, SessionKey: "s1"})
	if err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}
	if _, cleanupErr := second.CleanupIgnored(ctx, acquired.ID); cleanupErr == nil {
		t.Fatal("second CleanupIgnored() error = nil, want locked workspace error")
	}
	separate, err := second.Acquire(ctx, AcquireRequest{Repository: source, SessionKey: "s2"})
	if err != nil {
		t.Fatalf("second Acquire() error = %v", err)
	}
	if separate.ID == acquired.ID {
		t.Fatalf("second Acquire() reused locked workspace %q", separate.ID)
	}
}

func TestManagerLoadsGenericInventoryThroughCanonicalRootAlias(t *testing.T) {
	ctx := context.Background()
	source := initSourceRepo(t)
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	parent := t.TempDir()
	realRoot := filepath.Join(parent, "real-git-workspaces")
	first := newTestManagerAtRoot(t, realRoot, &now)
	workspace, acquireErr := first.Acquire(ctx, AcquireRequest{
		Repository: source,
		SessionKey: "before-alias",
	})
	if acquireErr != nil {
		t.Fatal(acquireErr)
	}
	if _, err := first.ReleaseSession(ctx, ReleaseRequest{SessionKey: "before-alias"}); err != nil {
		t.Fatal(err)
	}
	aliasRoot := filepath.Join(parent, "workspace-alias")
	if err := os.Symlink(realRoot, aliasRoot); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	aliased := newTestManagerAtRoot(t, aliasRoot, &now)
	if aliased.RootDir() != filepath.Clean(realRoot) {
		t.Fatalf("canonical RootDir() = %q, want %q", aliased.RootDir(), realRoot)
	}
	reused, err := aliased.Acquire(ctx, AcquireRequest{
		Repository: source,
		SessionKey: "after-alias",
	})
	if err != nil {
		t.Fatal(err)
	}
	if reused.ID != workspace.ID || reused.Path != workspace.Path {
		t.Fatalf("aliased manager did not reuse inventory: before=%#v after=%#v", workspace, reused)
	}
	if _, err := aliased.ReleaseSession(ctx, ReleaseRequest{SessionKey: "after-alias"}); err != nil {
		t.Fatal(err)
	}
	if _, err := aliased.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestManagerInventoryLockHonorsContext(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	root := filepath.Join(t.TempDir(), "git-workspaces")
	manager := newTestManagerAtRoot(t, root, &now)
	unlock, err := lockInventoryFile(
		context.Background(),
		filepath.Join(root, inventoryLockFile),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err = manager.Stats(ctx)
	if err == nil {
		t.Fatal("Stats() error = nil, want inventory lock timeout")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("Stats() error = %v, want context deadline exceeded", err)
	}
	unlock()
	if _, err := manager.Stats(context.Background()); err != nil {
		t.Fatalf("Stats() after advisory lock release error = %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, inventoryLockFile), 0o700); !os.IsExist(err) {
		t.Fatalf("legacy lock-directory acquisition error = %v, want existing-file fence", err)
	}
}

func TestManagerInventoryLockRejectsActiveLegacyDirectory(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	root := filepath.Join(t.TempDir(), "git-workspaces")
	manager := newTestManagerAtRoot(t, root, &now)
	if err := os.Mkdir(filepath.Join(root, inventoryLockFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Stats(context.Background()); err == nil {
		t.Fatal("Stats() with legacy inventory lock directory error = nil")
	}
}

func TestManagerInventoryLockRecoversAfterProcessCrash(t *testing.T) {
	root := filepath.Join(t.TempDir(), "git-workspaces")
	readyPath := filepath.Join(t.TempDir(), "ready")
	command := exec.Command(os.Args[0], "-test.run=^TestInventoryLockCrashHelper$")
	command.Env = append(
		os.Environ(),
		"PICOCLAW_LOCK_CRASH_ROOT="+root,
		"PICOCLAW_LOCK_CRASH_READY="+readyPath,
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for child inventory lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("killed lock helper exited successfully")
	}

	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	manager := newTestManagerAtRoot(t, root, &now)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := manager.Stats(ctx); err != nil {
		t.Fatalf("Stats() after lock-holder crash error = %v", err)
	}
}

func TestInventoryLockCrashHelper(t *testing.T) {
	root := os.Getenv("PICOCLAW_LOCK_CRASH_ROOT")
	readyPath := os.Getenv("PICOCLAW_LOCK_CRASH_READY")
	if root == "" || readyPath == "" {
		return
	}
	unlock, err := lockInventoryFile(
		context.Background(),
		filepath.Join(root, inventoryLockFile),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := os.WriteFile(readyPath, []byte("ready\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Second)
	}
}

func newTestManager(t *testing.T, now *time.Time) *Manager {
	t.Helper()
	return newTestManagerAtRoot(t, filepath.Join(t.TempDir(), "git-workspaces"), now)
}

func newTestManagerAtRoot(t *testing.T, root string, now *time.Time) *Manager {
	t.Helper()
	manager, err := NewManager(Options{
		RootDir:             root,
		MaxTotalSizeBytes:   1 << 30,
		IgnoredCleanupDelay: time.Hour,
		DropDelay:           48 * time.Hour,
		Now: func() time.Time {
			return *now
		},
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	return manager
}

func initSourceRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := runGit(context.Background(), dir, "init", "-b", "main"); err != nil {
		t.Fatalf("git init error = %v", err)
	}
	// New Git versions may launch automatic maintenance after a test commit,
	// briefly creating objects/maintenance.lock while pinned-layout assertions
	// inspect the repository. These fixtures test PicoClaw maintenance directly;
	// ambient Git auto-maintenance would add an unrelated concurrent actor.
	if _, err := runGit(context.Background(), dir, "config", "maintenance.auto", "false"); err != nil {
		t.Fatalf("disable test repository maintenance: %v", err)
	}
	if _, err := runGit(context.Background(), dir, "config", "gc.auto", "0"); err != nil {
		t.Fatalf("disable test repository auto-gc: %v", err)
	}
	seedSourceRepo(t, dir)
	return dir
}

func seedSourceRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignored/\n*.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(context.Background(), dir, "add", "."); err != nil {
		t.Fatalf("git add error = %v", err)
	}
	if _, err := runGit(context.Background(), dir, "commit", "-m", "initial"); err != nil {
		t.Fatalf("git commit error = %v", err)
	}
}

func testGitCommit(t *testing.T, directory, revision string) string {
	t.Helper()
	commit, err := runGit(
		context.Background(),
		directory,
		"rev-parse",
		"--verify",
		revision+"^{commit}",
	)
	if err != nil {
		t.Fatalf("resolve git commit %q: %v", revision, err)
	}
	return strings.TrimSpace(commit)
}
