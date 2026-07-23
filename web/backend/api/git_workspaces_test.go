package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
)

func TestGitWorkspaceRoutesListCleanupAndDrop(t *testing.T) {
	ctx := context.Background()
	source := initAPISourceRepo(t)
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	rootDir := filepath.Join(t.TempDir(), "git-workspaces")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.GitWorkspaces.RootDir = rootDir
	cfg.GitWorkspaces.MaxTotalSizeBytes = 1 << 30
	cfg.GitWorkspaces.IgnoredCleanupDelaySeconds = 3600
	cfg.GitWorkspaces.DropDelaySeconds = 86400
	if err := config.SaveConfig(cfgPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	manager, err := gitworkspace.NewManager(gitworkspace.Options{
		RootDir:             rootDir,
		MaxTotalSizeBytes:   cfg.GitWorkspaces.MaxTotalSizeBytes,
		IgnoredCleanupDelay: time.Hour,
		DropDelay:           24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	acquired, err := manager.Acquire(ctx, gitworkspace.AcquireRequest{
		Repository: source,
		SessionKey: "api-session",
	})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if _, err := manager.ReleaseSession(ctx, gitworkspace.ReleaseRequest{SessionKey: "api-session"}); err != nil {
		t.Fatalf("ReleaseSession() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(acquired.Path, "ignored"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(acquired.Path, "ignored", "cache.bin"), []byte("cache"), 0o644); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	NewHandler(cfgPath).RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/git-workspaces", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var stats gitworkspace.Stats
	if err := json.Unmarshal(rec.Body.Bytes(), &stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if stats.WorkspaceCount != 1 || stats.IgnoredBytes == 0 {
		t.Fatalf("stats workspace/ignored = %d/%d, want 1/>0", stats.WorkspaceCount, stats.IgnoredBytes)
	}

	rec = httptest.NewRecorder()
	body := bytes.NewBufferString(`{"workspace_id":"` + acquired.ID + `"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/git-workspaces/cleanup", body)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("cleanup status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var cleanup gitworkspace.CleanupResult
	if err := json.Unmarshal(rec.Body.Bytes(), &cleanup); err != nil {
		t.Fatalf("decode cleanup: %v", err)
	}
	if cleanup.Before == 0 || cleanup.After != 0 {
		t.Fatalf("cleanup before/after = %d/%d, want >0/0", cleanup.Before, cleanup.After)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/git-workspaces/"+acquired.ID, nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("drop status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if _, err := os.Stat(acquired.Path); !os.IsNotExist(err) {
		t.Fatalf("dropped workspace path stat error = %v, want not exist", err)
	}
}

func initAPISourceRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runAPIGit(t, dir, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignored/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runAPIGit(t, dir, "add", ".")
	runAPIGit(t, dir, "commit", "-m", "initial")
	return dir
}

func runAPIGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=PicoClaw",
		"GIT_AUTHOR_EMAIL=picoclaw@localhost",
		"GIT_COMMITTER_NAME=PicoClaw",
		"GIT_COMMITTER_EMAIL=picoclaw@localhost",
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(output))
	}
}
