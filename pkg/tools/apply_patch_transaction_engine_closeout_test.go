package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionEngineCloseoutPreparingDriftMatrix(t *testing.T) {
	tests := []struct {
		name   string
		patch  string
		mutate func(*testing.T, *applyPatchPreparedTransaction)
	}{
		{
			"source content",
			"*** Begin Patch\n*** Delete File: source.txt\n*** End Patch",
			func(t *testing.T, tx *applyPatchPreparedTransaction) {
				sourcePath := tx.intent.operations[0].planned.sourcePath
				if err := os.WriteFile(sourcePath, []byte("drift\n"), 0o640); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			"source private name",
			"*** Begin Patch\n*** Delete File: source.txt\n*** End Patch",
			func(t *testing.T, tx *applyPatchPreparedTransaction) {
				artifact, err := requireApplyPatchTxnArtifact(
					tx.journal, 0, applyPatchTransactionArtifactSourceWitness,
				)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(artifact.Rooted.AnchorCanonicalPath, artifact.Rooted.Basename)
				if err := os.WriteFile(path, []byte("alien\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			"stage content",
			"*** Begin Patch\n*** Add File: result.txt\n+result\n*** End Patch",
			func(t *testing.T, tx *applyPatchPreparedTransaction) {
				artifact, err := requireApplyPatchTxnArtifact(
					tx.journal, 0, applyPatchTransactionArtifactPostimageStage,
				)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(artifact.Rooted.AnchorCanonicalPath, artifact.Rooted.Basename)
				if err := os.WriteFile(path, []byte("drift\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			"witness removed",
			"*** Begin Patch\n*** Add File: result.txt\n+result\n*** End Patch",
			func(t *testing.T, tx *applyPatchPreparedTransaction) {
				artifact, err := requireApplyPatchTxnArtifact(
					tx.journal, 0, applyPatchTransactionArtifactPostimageWitness,
				)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(artifact.Rooted.AnchorCanonicalPath, artifact.Rooted.Basename)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			"rollback name appeared",
			"*** Begin Patch\n*** Add File: result.txt\n+result\n*** End Patch",
			func(t *testing.T, tx *applyPatchPreparedTransaction) {
				artifact, err := requireApplyPatchTxnArtifact(
					tx.journal, 0, applyPatchTransactionArtifactTargetRollbackQuarantine,
				)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(artifact.Rooted.AnchorCanonicalPath, artifact.Rooted.Basename)
				if err := os.WriteFile(path, []byte("alien\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			"public target appeared",
			"*** Begin Patch\n*** Add File: result.txt\n+result\n*** End Patch",
			func(t *testing.T, tx *applyPatchPreparedTransaction) {
				targetPath := tx.intent.operations[0].planned.targetPath
				if err := os.WriteFile(targetPath, []byte("alien\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tx, plan := newApplyPatchTxnEngineCloseoutTransaction(t, test.patch)
			defer tx.abortPreparing()
			test.mutate(t, tx)
			if err := verifyApplyPatchTxnPreparingPublicState(tx.intent, tx.journal); err == nil {
				t.Fatal("preparing drift was accepted")
			}
			if err := tx.revalidate(context.Background(), plan); err == nil {
				t.Fatal("transaction drift revalidated")
			}
		})
	}
}

func TestApplyPatchTransactionEngineCloseoutCapabilityProbeErrors(t *testing.T) {
	if err := probeApplyPatchTxnNoReplaceCapabilities(
		&applyPatchTxnIntentPlan{},
		&applyPatchTransactionJournal{},
	); err != nil {
		t.Fatalf("empty capability plan = %v", err)
	}
	tx, _ := newApplyPatchTxnEngineCloseoutTransaction(
		t,
		"*** Begin Patch\n*** Add File: result.txt\n+result\n*** End Patch",
	)
	defer tx.abortPreparing()
	stage, err := requireApplyPatchTxnArtifact(
		tx.journal, 0, applyPatchTransactionArtifactPostimageStage,
	)
	if err != nil {
		t.Fatal(err)
	}
	identity := stage.Rooted.Identity
	stage.Rooted.Identity = nil
	if err := probeApplyPatchTxnNoReplaceCapabilities(tx.intent, tx.journal); err == nil {
		t.Fatal("missing stage capability identity was accepted")
	}
	stage.Rooted.Identity = identity
	tx.intent.operations[0].targetAnchor.closed = true
	if err := probeApplyPatchTxnNoReplaceCapabilities(tx.intent, tx.journal); err == nil {
		t.Fatal("closed target capability anchor was accepted")
	}
	if err := probeApplyPatchTxnStateNoReplaceCapability(tx.workspaceState, "", nil); err == nil {
		t.Fatal("nil state capability identity was accepted")
	}
}

func newApplyPatchTxnEngineCloseoutTransaction(
	t *testing.T,
	patch string,
) (*applyPatchPreparedTransaction, *applyPatchPlan) {
	t.Helper()
	workspace := t.TempDir()
	if patch == "*** Begin Patch\n*** Delete File: source.txt\n*** End Patch" {
		writeApplyPatchFixture(t, workspace, "source.txt", "before\n", 0o640)
	}
	plan := buildApplyPatchTxnTestPlan(t, workspace, patch)
	state, workspaceState := openApplyPatchTxnTestState(t, plan.workspace)
	tx, err := beginApplyPatchTransaction(
		context.Background(), state, workspaceState, plan,
	)
	if err != nil {
		t.Fatal(err)
	}
	return tx, plan
}
