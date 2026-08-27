package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionRecoveryPreflightExactFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *applyPatchPreparedTransaction)
	}{
		{
			name: "journal encoding",
			mutate: func(_ *testing.T, tx *applyPatchPreparedTransaction) {
				tx.journal.TransactionID = "invalid"
			},
		},
		{
			name: "artifact removal view",
			mutate: func(t *testing.T, tx *applyPatchPreparedTransaction) {
				stage, _ := requireApplyPatchTxnArtifact(
					tx.journal, 0, applyPatchTransactionArtifactPostimageStage,
				)
				if err := os.WriteFile(
					filepath.Join(stage.Rooted.AnchorCanonicalPath, stage.Rooted.RemovalBasename),
					[]byte("alien\n"), 0o600,
				); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "ambiguous target classification",
			mutate: func(t *testing.T, tx *applyPatchPreparedTransaction) {
				operation := tx.intent.operations[0]
				stage, _ := requireApplyPatchTxnArtifact(
					tx.journal, 0, applyPatchTransactionArtifactPostimageStage,
				)
				if err := os.Link(
					filepath.Join(stage.Rooted.AnchorCanonicalPath, stage.Rooted.Basename),
					operation.planned.targetPath,
				); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "rooted alias link count",
			mutate: func(t *testing.T, tx *applyPatchPreparedTransaction) {
				stage, _ := requireApplyPatchTxnArtifact(
					tx.journal, 0, applyPatchTransactionArtifactPostimageStage,
				)
				if err := os.Link(
					filepath.Join(stage.Rooted.AnchorCanonicalPath, stage.Rooted.Basename),
					filepath.Join(stage.Rooted.AnchorCanonicalPath, "undeclared-link"),
				); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
			tx := fixture.begin(t)
			defer tx.abortPreparing()
			test.mutate(t, tx)
			if err := preflightApplyPatchTxnRecoveryMutation(tx); err == nil {
				t.Fatal("invalid recovery preflight succeeded")
			}
		})
	}
}
