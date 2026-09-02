package artifacts

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestCatalogProjectsProviderArtifactsWithoutCallerReconstruction(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "workspace")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	catalog, err := New(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	workflow := filepath.Join(workspace, "state", "workflows.db")
	roots := catalog.ProtectedRoots()
	for _, required := range []string{
		workflow, workflow + "-wal", workflow + "-shm", workflow + "-journal",
	} {
		if !slices.Contains(roots, required) {
			t.Fatalf("provider artifacts omit %q", required)
		}
	}
	if len(roots) == 0 {
		t.Fatal("provider artifact catalog is empty")
	}
	account := filepath.Join(workspace, "state", "account-router.db")
	accountRoots := catalog.ProtectedRootsForDomains("account-routing")
	for _, required := range []string{
		account, account + "-wal", account + "-shm", account + "-journal",
	} {
		if !slices.Contains(accountRoots, required) {
			t.Fatalf("account-routing artifacts omit %q: %#v", required, accountRoots)
		}
	}
	accountRoots[0] = "changed"
	if slices.Contains(catalog.ProtectedRootsForDomains("account-routing"), "changed") {
		t.Fatal("domain artifact roots retained caller mutation")
	}
	mutated := catalog.ProtectedRoots()
	mutated[0] = "changed"
	if slices.Equal(mutated, catalog.ProtectedRoots()) {
		t.Fatal("provider artifact roots were not detached")
	}
	gitRoot := filepath.Join(workspace, ".git-workspaces")
	for domain, databasePath := range map[string]string{
		"git-workspace-inventory": filepath.Join(gitRoot, "inventory.db"),
		"pr-workspace-checkpoints": filepath.Join(
			gitRoot, ".pr-workspace-implementation", "active", "checkpoints.db",
		),
	} {
		roots := catalog.ProtectedRootsForDomains(domain)
		for _, required := range []string{
			databasePath,
			databasePath + "-wal",
			databasePath + "-shm",
			databasePath + "-journal",
			databasePath + ".locks",
		} {
			if !slices.Contains(roots, required) {
				t.Fatalf("%s artifacts omit %q: %#v", domain, required, roots)
			}
		}
	}
}

func TestCatalogDoesNotInspectLiveGenerationMembers(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	workflow := filepath.Join(workspace, "state", "workflows.db")
	if err := os.Mkdir(workflow+"-wal", 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	catalog, err := New(home, cfg)
	if err != nil {
		t.Fatalf("protected-artifact projection inspected a live sidecar: %v", err)
	}
	if !slices.Contains(catalog.ProtectedRoots(), workflow+"-wal") {
		t.Fatal("protected-artifact projection omitted the lexical WAL member")
	}
}
