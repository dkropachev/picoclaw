package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/isolation"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/tools"
)

func TestAgentReloadRetainsOldWorkspaceAndCustomRuntimeIdentities(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	oldWorkspace := filepath.Join(root, "old-workspace")
	newWorkspace := filepath.Join(root, "new-workspace")
	oldEvolution := filepath.Join(root, "old-evolution")
	newEvolution := filepath.Join(root, "new-evolution")
	oldEventDatabase := filepath.Join(root, "old-eventing", "events.db")
	newEventDatabase := filepath.Join(root, "new-eventing", "events.db")
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfig, filepath.Join(home, "config.json"))

	oldEvidence := filepath.Join(
		filepath.Dir(oldEventDatabase),
		"pr-workspace-local-ci",
		"evidence",
	)
	fixtures := []string{
		filepath.Join(oldWorkspace, "workflow_runs", "wr_old", "run.json"),
		filepath.Join(oldWorkspace, agentRepositoryReviewStateDir, "old-review.json"),
		filepath.Join(oldWorkspace, agentRepositoryEvalStateDir, "old-evaluation.json"),
		filepath.Join(oldEvolution, "profiles", "old-profile.json"),
		filepath.Join(oldEvidence, "cache", "aa", "old-cache.json"),
	}
	for _, source := range fixtures {
		if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(source, []byte("old-runtime-state"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	oldConfig := agentFileMutationTestConfig(oldWorkspace)
	oldConfig.Evolution.StateDir = oldEvolution
	oldConfig.Events.Ingress.Enabled = true
	oldConfig.Events.Ingress.DatabasePath = oldEventDatabase
	loop := newAgentLoop(
		oldConfig,
		nil,
		&mockProvider{},
		isolation.NewExecutionPolicy(oldConfig.Isolation),
		logger.DiagnosticPolicy{},
	)
	t.Cleanup(loop.Close)

	newConfig := agentFileMutationTestConfig(newWorkspace)
	newConfig.Evolution.StateDir = newEvolution
	newConfig.Events.Ingress.Enabled = true
	newConfig.Events.Ingress.DatabasePath = newEventDatabase
	if err := loop.ReloadProviderAndConfig(
		context.Background(),
		&mockProvider{},
		newConfig,
	); err != nil {
		t.Fatal(err)
	}
	agent := loop.registry.GetDefaultAgent()
	if agent == nil || agent.preparedFileMutationPolicy == nil ||
		agent.fileMutationIdentityCatalog == nil {
		t.Fatal("reloaded agent has no complete mutation generation")
	}
	for _, want := range []string{
		filepath.Join(oldWorkspace, "workflow_runs"),
		filepath.Join(oldWorkspace, agentRepositoryReviewStateDir),
		filepath.Join(oldWorkspace, agentRepositoryEvalStateDir),
		filepath.Join(oldEvolution, "profiles"),
		oldEvidence,
	} {
		if !slices.Contains(agent.fileMutationProtectedRoots, want) {
			t.Fatalf("reloaded generation omitted old runtime root %q", want)
		}
	}
	aliasRoot := filepath.Join(newWorkspace, "old-runtime-aliases")
	if err := os.MkdirAll(aliasRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	for index, source := range fixtures {
		alias := filepath.Join(aliasRoot, string(rune('a'+index))+".alias")
		if err := os.Link(source, alias); err != nil {
			t.Skipf("hardlinks unavailable: %v", err)
		}
		info, err := os.Stat(alias)
		if err != nil {
			t.Fatal(err)
		}
		identityProtected, err := agent.fileMutationIdentityCatalog.ProtectsPath(alias, info)
		if err != nil || !identityProtected {
			t.Fatalf("old runtime alias %d identity protected=%t err=%v", index, identityProtected, err)
		}
		preparedProtected, err := agent.preparedFileMutationPolicy.ProtectsPath(alias)
		if err != nil || !preparedProtected {
			t.Fatalf("old runtime alias %d prepared protected=%t err=%v", index, preparedProtected, err)
		}
	}
}

func TestLocalRepairPreparedPoliciesArePairedAndRetained(t *testing.T) {
	workspace := t.TempDir()
	protected := filepath.Join(t.TempDir(), "runtime.db")
	prepared, err := tools.NewPreparedFileMutationPolicy(
		workspace,
		tools.FileMutationPolicy{ProtectedRoots: []string{protected}},
	)
	if err != nil {
		t.Fatal(err)
	}
	volatile, err := tools.NewPreparedApplyPatchVolatileRoots(
		workspace,
		[]string{protected},
	)
	if err != nil {
		t.Fatal(err)
	}
	base := LocalRepairRunnerConfig{
		Workspaces: &localRepairTestAcquirer{},
		Provider:   &localRepairTestProvider{},
		Model:      "repair-model",
	}
	mutationOnly := base
	mutationOnly.PreparedMutationPolicy = prepared
	if runner, runnerErr := NewLocalRepairRunner(mutationOnly); runnerErr == nil || runner != nil {
		t.Fatalf("unpaired mutation policy runner=%#v err=%v", runner, runnerErr)
	}
	volatileOnly := base
	volatileOnly.PreparedApplyPatchRoots = volatile
	if runner, runnerErr := NewLocalRepairRunner(volatileOnly); runnerErr == nil || runner != nil {
		t.Fatalf("unpaired apply_patch policy runner=%#v err=%v", runner, runnerErr)
	}
	paired := base
	paired.PreparedMutationPolicy = prepared
	paired.PreparedApplyPatchRoots = volatile
	runner, err := NewLocalRepairRunner(paired)
	if err != nil {
		t.Fatal(err)
	}
	if runner.preparedMutationPolicy != prepared ||
		runner.preparedApplyPatchRoots != volatile {
		t.Fatal("local repair detached paired prepared policies")
	}
}
