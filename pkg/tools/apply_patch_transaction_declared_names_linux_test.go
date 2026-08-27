//go:build linux

package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionFinalPreparingRejectsLateSourceProbeName(t *testing.T) {
	workspace := t.TempDir()
	writeApplyPatchFixture(t, workspace, "delete.txt", "before\n", 0o640)
	plan := buildApplyPatchTxnTestPlan(
		t,
		workspace,
		"*** Begin Patch\n*** Delete File: delete.txt\n*** End Patch",
	)
	state, workspaceState := openApplyPatchTxnTestState(t, plan.workspace)
	transaction, err := beginApplyPatchTransaction(
		context.Background(),
		state,
		workspaceState,
		plan,
	)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := requireApplyPatchTxnArtifact(
		transaction.journal,
		0,
		applyPatchTransactionArtifactSourceProbeWitness,
	)
	if err != nil || probe.Rooted.Identity != nil {
		t.Fatal(errors.Join(err, errors.New("inactive source probe unavailable")))
	}
	alien := filepath.Join(probe.Rooted.AnchorCanonicalPath, probe.Rooted.Basename)
	if err := os.WriteFile(alien, []byte("late\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := transaction.revalidate(context.Background(), plan); err == nil {
		t.Fatal("final preparing validation accepted a late source probe name")
	}
	if err := transaction.abortPreparing(); err == nil {
		t.Fatal("late source probe cleanup discarded an unowned object")
	}
	assertApplyPatchTxnTestFile(
		t,
		filepath.Join(workspace, "delete.txt"),
		"before\n",
		0o640,
	)
	assertApplyPatchTxnTestFile(t, alien, "late\n", 0o600)
}

func TestApplyPatchTransactionCommitEntryRejectsLateDeclaredNames(t *testing.T) {
	tests := []struct {
		name       string
		role       applyPatchTransactionArtifactRole
		useRemoval bool
	}{
		{"source probe basename", applyPatchTransactionArtifactSourceProbeWitness, false},
		{"source restore removal", applyPatchTransactionArtifactSourceRestoreStage, true},
		{"target rollback basename", applyPatchTransactionArtifactTargetRollbackQuarantine, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			writeApplyPatchFixture(t, workspace, "update.txt", "before\n", 0o640)
			stateRoot := filepath.Join(t.TempDir(), "transaction-state")
			tool := newApplyPatchPreflightTestTool(
				t,
				workspace,
				true,
				true,
				ApplyPatchPreflightPolicy{TransactionStateRoot: stateRoot},
			)
			var alien string
			tool.beforeTransactionCommit = func(transaction *applyPatchPreparedTransaction) {
				artifact, err := requireApplyPatchTxnArtifact(
					transaction.journal,
					0,
					test.role,
				)
				if err != nil {
					t.Fatal(err)
				}
				name := artifact.Rooted.Basename
				if test.useRemoval {
					name = artifact.Rooted.RemovalBasename
				}
				alien = filepath.Join(artifact.Rooted.AnchorCanonicalPath, name)
				if err := os.WriteFile(alien, []byte("late\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			result := executeApplyPatch(
				t,
				tool,
				context.Background(),
				"*** Begin Patch\n"+
					"*** Update File: update.txt\n@@\n-before\n+after\n"+
					"*** End Patch",
			)
			if result == nil || !result.IsError || result.ForUser != "" {
				t.Fatalf("late declared-name result = %#v", result)
			}
			assertApplyPatchTxnTestFile(
				t,
				filepath.Join(workspace, "update.txt"),
				"before\n",
				0o640,
			)
			assertApplyPatchTxnTestFile(t, alien, "late\n", 0o600)
		})
	}
}

func TestApplyPatchTransactionPreparedRecoveryRejectsInactiveProbeNames(t *testing.T) {
	tests := []struct {
		name       string
		role       applyPatchTransactionArtifactRole
		useRemoval bool
	}{
		{"probe basename", applyPatchTransactionArtifactSourceProbeWitness, false},
		{"restore removal", applyPatchTransactionArtifactSourceRestoreStage, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			writeApplyPatchFixture(t, workspace, "source.txt", "before\n", 0o640)
			fixture := newApplyPatchTxnRecoveryFixtureForPatch(
				t,
				workspace,
				"*** Begin Patch\n"+
					"*** Update File: source.txt\n@@\n-before\n+after\n"+
					"*** End Patch",
			)
			transaction := prepareApplyPatchTxnCrashTransaction(t, fixture)
			artifact, err := requireApplyPatchTxnArtifact(
				transaction.journal,
				0,
				test.role,
			)
			if err != nil || artifact.Rooted.Identity != nil {
				t.Fatal(errors.Join(err, errors.New("inactive source artifact unavailable")))
			}
			name := artifact.Rooted.Basename
			if test.useRemoval {
				name = artifact.Rooted.RemovalBasename
			}
			alien := filepath.Join(artifact.Rooted.AnchorCanonicalPath, name)
			if err := os.WriteFile(alien, []byte("late\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			fixture.simulateCrash(t, transaction)

			tool := newApplyPatchTxnRecoveryTool(
				t,
				fixture.workspacePath,
				fixture.stateRoot,
			)
			result := executeApplyPatch(
				t,
				tool,
				context.Background(),
				"*** Begin Patch\n*** Add File: must-not-run.txt\n+blocked\n*** End Patch",
			)
			if result == nil || !result.IsError || result.ForUser != "" {
				t.Fatalf("inactive source recovery result = %#v", result)
			}
			assertApplyPatchTxnTestFile(
				t,
				filepath.Join(workspace, "source.txt"),
				"before\n",
				0o640,
			)
			assertApplyPatchTxnTestFile(t, alien, "late\n", 0o600)
			if _, err := os.Lstat(filepath.Join(workspace, "must-not-run.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("new patch ran after inactive probe conflict: %v", err)
			}
		})
	}
}

func TestApplyPatchTransactionCommitEntryRejectsLateForestEntryRemoval(t *testing.T) {
	workspace := t.TempDir()
	tool := newApplyPatchPreflightTestTool(
		t,
		workspace,
		true,
		true,
		ApplyPatchPreflightPolicy{
			TransactionStateRoot: filepath.Join(t.TempDir(), "transaction-state"),
		},
	)
	var alien string
	tool.beforeTransactionCommit = func(transaction *applyPatchPreparedTransaction) {
		forestIntent := transaction.intent.forests[0]
		forest, err := requireApplyPatchTxnJournalForest(
			transaction.journal,
			forestIntent.id,
		)
		if err != nil {
			t.Fatal(err)
		}
		entry := &forest.Entries[len(forest.Entries)-1]
		parent := filepath.Join(
			forestIntent.anchorPath,
			forestIntent.stageRoot,
			filepath.Dir(filepath.FromSlash(entry.RelativePath)),
		)
		alien = filepath.Join(parent, entry.RemovalBasename)
		if err := os.WriteFile(alien, []byte("late\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result := executeApplyPatch(
		t,
		tool,
		context.Background(),
		"*** Begin Patch\n"+
			"*** Add File: nested/deeper/result.txt\n+candidate\n"+
			"*** End Patch",
	)
	if result == nil || !result.IsError || result.ForUser != "" {
		t.Fatalf("late forest removal result = %#v", result)
	}
	if _, err := os.Lstat(filepath.Join(workspace, "nested")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("late forest removal published authored tree: %v", err)
	}
	assertApplyPatchTxnTestFile(t, alien, "late\n", 0o600)
}
