//go:build android || darwin || dragonfly || freebsd || ios || linux || netbsd || openbsd || solaris

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/sipeed/picoclaw/pkg/config"
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
	if strings.Contains(err.Error(), filepath.Base(fifo)) || strings.Contains(err.Error(), archiveRoot) {
		t.Fatalf("checkpoint error disclosed private path: %v", err)
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

func TestCheckpointSnapshotRejectsRootReplacementDuringPinnedEnumeration(t *testing.T) {
	parent := t.TempDir()
	checkpointRoot := filepath.Join(parent, "private-checkpoint-root")
	archiveRoot := filepath.Join(checkpointRoot, "legacy-json")
	if err := os.MkdirAll(checkpointRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkpointRoot, "before.json"), []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	moved := checkpointRoot + "-moved"
	agentCheckpointDuringRootEnumeration = func() {
		agentCheckpointDuringRootEnumeration = nil
		if err := os.Rename(checkpointRoot, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(checkpointRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(checkpointRoot, "before.json"), []byte("replacement"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { agentCheckpointDuringRootEnumeration = nil })
	snapshot, err := agentCheckpointRetainedStateSnapshotBounded(
		checkpointRoot, archiveRoot, 4, 2, 4, 2,
	)
	if err == nil || snapshot != nil {
		t.Fatalf("root-replaced checkpoint snapshot = %#v, %v", snapshot, err)
	}
	if strings.Contains(err.Error(), checkpointRoot) || strings.Contains(err.Error(), "before.json") {
		t.Fatalf("root replacement error disclosed private path: %v", err)
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

func TestDynamicLegacyTreesAreValidatedByBoundedIdentityCatalog(t *testing.T) {
	home := t.TempDir()
	weixinRoot := filepath.Join(home, "channels", "weixin")
	syncRoot := filepath.Join(weixinRoot, "sync")
	if err := os.MkdirAll(syncRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	nonJSON := filepath.Join(syncRoot, "retained-state.bin")
	if err := os.WriteFile(nonJSON, []byte("retained"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfig, filepath.Join(home, "config.json"))
	workspace := t.TempDir()
	roots, err := agentRuntimeFileMutationProtectedRoots("")
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := agentFileMutationIdentityCatalog(workspace, &config.Config{}, roots)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(nonJSON)
	if err != nil {
		t.Fatal(err)
	}
	if protected, protectErr := catalog.ProtectsPath(nonJSON, info); protectErr != nil || !protected {
		t.Fatalf("non-JSON retained state protected=%t err=%v", protected, protectErr)
	}

	workspaceFile := filepath.Join(t.TempDir(), "workspace-file")
	if err := os.WriteFile(workspaceFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if roots, rootErr := agentWorkspaceAccountRouterProtectedRoots(workspaceFile); rootErr != nil || len(roots) == 0 {
		t.Fatalf("lexical regular-file router roots = %#v, %v", roots, rootErr)
	}
	if failed, catalogErr := agentFileMutationIdentityCatalog(
		workspaceFile,
		&config.Config{},
		nil,
	); catalogErr == nil || failed != nil {
		t.Fatalf("regular-file workspace catalog = %#v, %v", failed, catalogErr)
	}

	blockedWorkspace := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(blockedWorkspace, "state"),
		[]byte("blocks archive traversal"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if roots, rootErr := agentWorkspaceAccountRouterProtectedRoots(
		blockedWorkspace,
	); rootErr != nil ||
		len(roots) == 0 {
		t.Fatalf("lexical blocked router roots = %#v, %v", roots, rootErr)
	}
	if failed, catalogErr := agentFileMutationIdentityCatalog(
		blockedWorkspace,
		&config.Config{},
		nil,
	); catalogErr == nil || failed != nil {
		t.Fatalf("blocked router archive catalog = %#v, %v", failed, catalogErr)
	}
}
