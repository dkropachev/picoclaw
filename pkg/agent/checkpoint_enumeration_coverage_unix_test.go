//go:build android || darwin || dragonfly || freebsd || ios || linux || netbsd || openbsd || solaris

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCheckpointArchiveRejectsSpecialFilesAndClosedEnumeration(t *testing.T) {
	checkpointRoot := filepath.Join(t.TempDir(), "active")
	archiveRoot := filepath.Join(checkpointRoot, "legacy-json")
	if err := os.MkdirAll(archiveRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(archiveRoot, "special")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("FIFO creation unavailable: %v", err)
	}
	files, err := agentCheckpointRetainedStateFilesBounded(
		checkpointRoot, archiveRoot, 4, 2, 4, 2,
	)
	if err == nil || files != nil || !strings.Contains(err.Error(), "unsafe file") {
		t.Fatalf("special checkpoint archive = %#v, %v", files, err)
	}

	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
	if err := forEachCheckpointDirectoryEntry(directory, func(os.DirEntry) error { return nil }); err == nil {
		t.Fatal("closed checkpoint directory enumeration succeeded")
	}
}

func TestSQLiteMutationPolicyFailsClosedWhenRootsCannotBeResolved(t *testing.T) {
	originalWorkingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	unavailableWorkingDirectory := t.TempDir()
	if err := os.Chdir(unavailableWorkingDirectory); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if restoreErr := os.Chdir(originalWorkingDirectory); restoreErr != nil {
			t.Errorf("restore working directory: %v", restoreErr)
		}
	}()
	if err := os.Remove(unavailableWorkingDirectory); err != nil {
		t.Fatal(err)
	}

	cfg := agentFileMutationTestConfig("relative-workspace")
	cfg.GitWorkspaces.RootDir = "relative-git-workspaces"
	cfg.Events.Ingress.Enabled = true
	cfg.Events.Ingress.DatabasePath = filepath.Join("relative-events", "events.db")
	checks := map[string]func() ([]string, error){
		"workflow": func() ([]string, error) {
			return agentWorkflowRuntimeFileMutationProtectedRoots("relative-workspace")
		},
		"workspace": func() ([]string, error) {
			return agentWorkspaceFileMutationProtectedRoots("relative-workspace")
		},
		"evolution": func() ([]string, error) {
			return agentEvolutionFileMutationProtectedRoots("", "relative-evolution")
		},
		"Git workspace": func() ([]string, error) {
			return agentGitWorkspaceFileMutationProtectedRoots(cfg)
		},
		"local CI": func() ([]string, error) {
			return agentLocalCIEvidenceFileMutationProtectedRoots(cfg)
		},
		"account router": func() ([]string, error) {
			return agentWorkspaceAccountRouterProtectedRoots("relative-workspace")
		},
		"workspace SQLite": func() ([]string, error) {
			return appendAgentWorkspaceSQLiteProtectedRoots(nil, cfg)
		},
	}
	for name, check := range checks {
		if roots, checkErr := check(); checkErr == nil || roots != nil {
			t.Fatalf("%s unresolved roots = %#v, %v", name, roots, checkErr)
		}
	}

	for name, check := range map[string]func(){
		"workspace": func() {
			_ = mustAgentWorkspaceFileMutationProtectedRoots("relative-workspace")
		},
		"local CI": func() {
			_ = mustAgentLocalCIEvidenceFileMutationProtectedRoots(cfg)
		},
		"account router": func() {
			_ = mustAgentWorkspaceAccountRouterProtectedRoots("relative-workspace")
		},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("must-build %s roots did not panic", name)
				}
			}()
			check()
		}()
	}
}

func TestWeixinRetainedStateEnumerationPropagatesUnsafePathErrors(t *testing.T) {
	invalidPath := "invalid\x00path"
	if files, err := agentWeixinRetainedStateFiles(invalidPath, t.TempDir()); err == nil || files != nil {
		t.Fatalf("invalid Weixin source root = %#v, %v", files, err)
	}
	if files, err := agentWeixinRetainedStateFiles(t.TempDir(), invalidPath); err == nil || files != nil {
		t.Fatalf("invalid Weixin archive root = %#v, %v", files, err)
	}
}

func TestMutationPolicyFiltersNonJSONAndPropagatesRouterEnumerationErrors(t *testing.T) {
	weixinRoot := t.TempDir()
	syncRoot := filepath.Join(weixinRoot, "sync")
	if err := os.Mkdir(syncRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(syncRoot, "ignored.txt"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	if files, err := agentWeixinRetainedStateFiles(
		weixinRoot, filepath.Join(weixinRoot, "missing-archive"),
	); err != nil || len(files) != 0 {
		t.Fatalf("filtered Weixin state = %#v, %v", files, err)
	}

	workspaceFile := filepath.Join(t.TempDir(), "workspace-file")
	if err := os.WriteFile(workspaceFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if roots, err := agentWorkspaceAccountRouterProtectedRoots(workspaceFile); err == nil || roots != nil {
		t.Fatalf("regular-file router workspace = %#v, %v", roots, err)
	}

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "state"), []byte("blocks archive traversal"), 0o600); err != nil {
		t.Fatal(err)
	}
	if roots, err := agentWorkspaceAccountRouterProtectedRoots(workspace); err == nil || roots != nil {
		t.Fatalf("blocked router archive traversal = %#v, %v", roots, err)
	}
}
