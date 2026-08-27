package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionEngineForestCloseoutDriftMatrix(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *applyPatchPreparedTransaction, *applyPatchTxnForestIntent, *applyPatchTransactionJournalForest)
	}{
		{
			"public root",
			func(t *testing.T, _ *applyPatchPreparedTransaction, intent *applyPatchTxnForestIntent, _ *applyPatchTransactionJournalForest) {
				if err := os.Mkdir(filepath.Join(intent.anchorPath, intent.publicRoot), 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			"rollback root",
			func(t *testing.T, _ *applyPatchPreparedTransaction, intent *applyPatchTxnForestIntent, _ *applyPatchTransactionJournalForest) {
				if err := os.Mkdir(filepath.Join(intent.anchorPath, intent.rollbackRoot), 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			"missing root identity",
			func(_ *testing.T, _ *applyPatchPreparedTransaction, _ *applyPatchTxnForestIntent, forest *applyPatchTransactionJournalForest) {
				forest.StageRoot.Identity = nil
			},
		},
		{
			"wrong root identity",
			func(_ *testing.T, _ *applyPatchPreparedTransaction, _ *applyPatchTxnForestIntent, forest *applyPatchTransactionJournalForest) {
				forest.StageRoot.Identity.File++
			},
		},
		{
			"wrong root mode",
			func(_ *testing.T, _ *applyPatchPreparedTransaction, _ *applyPatchTxnForestIntent, forest *applyPatchTransactionJournalForest) {
				forest.Entries[0].Mode ^= 1
			},
		},
		{
			"uncheckpointed entry",
			func(_ *testing.T, _ *applyPatchPreparedTransaction, _ *applyPatchTxnForestIntent, forest *applyPatchTransactionJournalForest) {
				forest.Entries[len(forest.Entries)-1].Identity = nil
			},
		},
		{
			"file content",
			func(t *testing.T, _ *applyPatchPreparedTransaction, intent *applyPatchTxnForestIntent, forest *applyPatchTransactionJournalForest) {
				entry := forest.Entries[len(forest.Entries)-1]
				path := filepath.Join(intent.anchorPath, intent.stageRoot, filepath.FromSlash(entry.RelativePath))
				if err := os.WriteFile(path, []byte("drift\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			"missing sentinel",
			func(_ *testing.T, _ *applyPatchPreparedTransaction, _ *applyPatchTxnForestIntent, forest *applyPatchTransactionJournalForest) {
				forest.SentinelRelativePath = "missing"
			},
		},
		{
			"witness content",
			func(t *testing.T, _ *applyPatchPreparedTransaction, intent *applyPatchTxnForestIntent, _ *applyPatchTransactionJournalForest) {
				path := filepath.Join(intent.anchorPath, intent.sentinelWitnessName)
				if err := os.WriteFile(path, []byte("drift\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			"alien manifest",
			func(t *testing.T, _ *applyPatchPreparedTransaction, intent *applyPatchTxnForestIntent, _ *applyPatchTransactionJournalForest) {
				path := filepath.Join(intent.anchorPath, intent.stageRoot, "alien")
				if err := os.WriteFile(path, []byte("alien\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newApplyPatchTxnRecoveryFixture(t, "nested/deeper/result.txt")
			tx := fixture.begin(t)
			defer tx.abortPreparing()
			intent := tx.intent.forests[0]
			forest := &tx.journal.Forests[0]
			test.mutate(t, tx, intent, forest)
			if err := verifyApplyPatchTxnStagedForest(intent, forest); err == nil {
				t.Fatal("drifted staged forest was accepted")
			}
		})
	}
}

func TestApplyPatchTransactionEngineForestCloseoutHelperBranches(t *testing.T) {
	forest := &applyPatchTransactionJournalForest{
		SentinelRelativePath: "file",
		Entries: []applyPatchTransactionJournalForestEntry{
			{RelativePath: ".", Kind: "directory"},
			{RelativePath: "dir", Kind: "directory"},
			{RelativePath: "dir/file", Kind: "file"},
		},
	}
	children := expectedApplyPatchTxnForestChildren(forest)
	if len(children) != 2 || len(children["."]) != 1 || len(children["dir"]) != 1 {
		t.Fatalf("forest children = %#v", children)
	}
	if sentinel := findApplyPatchTxnForestSentinel(forest); sentinel != nil {
		t.Fatalf("unexpected sentinel = %#v", sentinel)
	}
	if err := verifyApplyPatchTxnForestManifestAt(nil, forest, "root"); err == nil {
		t.Fatal("nil forest intent manifest succeeded")
	}
}
