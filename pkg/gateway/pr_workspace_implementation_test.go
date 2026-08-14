package gateway

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/gitworkspace"
)

func TestPRWorkspaceRepairBaselineAdoptsCleanFreshCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	ctx := context.Background()
	source := initPRWorkspaceRepairTestRepository(t)
	head := runPRWorkspaceRepairGit(t, source, "rev-parse", "HEAD")
	manager, err := gitworkspace.NewManager(gitworkspace.Options{RootDir: t.TempDir()})
	if err != nil {
		t.Fatalf("gitworkspace.NewManager() error = %v", err)
	}
	pin := gitworkspace.PinnedAcquireRequest{
		Repository: source, SourceRef: "main", ExpectedCommit: head,
		ReservationKey: "pr-workspace:prw_11111111111111111111111111111111",
		AgentID:        "repair-test",
	}
	workspace, err := manager.AcquirePinned(ctx, pin)
	if err != nil {
		t.Fatalf("AcquirePinned() error = %v", err)
	}
	request := gitworkspace.PinnedCandidateRequest{Pin: pin, WorkspaceID: workspace.ID}

	baseline, err := snapshotPRWorkspaceRepairBaseline(ctx, manager, request)
	if err != nil {
		t.Fatalf("snapshotPRWorkspaceRepairBaseline(clean) error = %v", err)
	}
	if baseline.WorkspaceID != workspace.ID || baseline.ParentCommit != head ||
		baseline.Tree == "" || baseline.CandidateDigest == "" || baseline.ChangedFiles != 0 {
		t.Fatalf("clean repair baseline = %#v", baseline)
	}
	if _, strictErr := manager.SnapshotPinnedCandidate(ctx, request); strictErr == nil {
		t.Fatal("strict SnapshotPinnedCandidate unexpectedly accepted the clean checkout")
	}

	lease, err := manager.AdoptPinnedLine(ctx, gitworkspace.PinnedLineAdoptRequest{
		Pin: pin, WorkspaceID: workspace.ID,
		LineID: "pdln_11111111111111111111111111111111", ExpectedTree: baseline.Tree,
	})
	if err != nil {
		t.Fatalf("AdoptPinnedLine(clean baseline) error = %v", err)
	}
	if lease.WorkspaceID != workspace.ID || lease.Version != 0 || lease.MutationEpoch <= 0 ||
		lease.Tip != head || lease.Tree != baseline.Tree {
		t.Fatalf("adopted clean repair line = %#v", lease)
	}
}

func TestPRWorkspaceCandidateLookupIsScopedByPRWorkspaceAndTree(t *testing.T) {
	const tree = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	firstWorkspace := "prw_11111111111111111111111111111111"
	secondWorkspace := "prw_22222222222222222222222222222222"
	first := prWorkspaceCandidate{candidate: gitworkspace.PinnedCandidate{WorkspaceID: "gw-first", Tree: tree}}
	second := prWorkspaceCandidate{candidate: gitworkspace.PinnedCandidate{WorkspaceID: "gw-second", Tree: tree}}
	runtime := &prWorkspaceImplementationRuntime{candidates: map[prWorkspaceCandidateKey]prWorkspaceCandidate{
		{workspaceID: firstWorkspace, tree: tree}:  first,
		{workspaceID: secondWorkspace, tree: tree}: second,
	}}
	if got, ok := runtime.lookup(firstWorkspace, tree); !ok || got.candidate.WorkspaceID != "gw-first" {
		t.Fatalf("first workspace lookup = %#v, %v", got, ok)
	}
	if got, ok := runtime.lookup(secondWorkspace, tree); !ok || got.candidate.WorkspaceID != "gw-second" {
		t.Fatalf("second workspace lookup = %#v, %v", got, ok)
	}
	if _, ok := runtime.lookup("prw_33333333333333333333333333333333", tree); ok {
		t.Fatal("same-tree candidate leaked across PR workspaces")
	}
}

func initPRWorkspaceRepairTestRepository(t *testing.T) string {
	t.Helper()
	repository := t.TempDir()
	runPRWorkspaceRepairGit(t, repository, "init")
	runPRWorkspaceRepairGit(t, repository, "checkout", "-b", "main")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("# repair fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runPRWorkspaceRepairGit(t, repository, "add", "README.md")
	runPRWorkspaceRepairGit(
		t, repository,
		"-c", "user.name=PicoClaw Tests", "-c", "user.email=picoclaw@example.invalid",
		"commit", "-m", "initial fixture",
	)
	return repository
}

func runPRWorkspaceRepairGit(t *testing.T, repository string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repository}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v: %s", strings.Join(arguments, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output))
}
