package gitworkspace

import (
	"context"
	"os"
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

func TestManagerInventoryLockHonorsContext(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	root := filepath.Join(t.TempDir(), "git-workspaces")
	manager := newTestManagerAtRoot(t, root, &now)
	if err := os.Mkdir(filepath.Join(root, inventoryLockDir), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()

	_, err := manager.Stats(ctx)
	if err == nil {
		t.Fatal("Stats() error = nil, want inventory lock timeout")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("Stats() error = %v, want context deadline exceeded", err)
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
	return dir
}
