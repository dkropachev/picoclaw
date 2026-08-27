package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionIntentPredeclaresNamesWithoutMutation(t *testing.T) {
	workspace := t.TempDir()
	sourcePath := filepath.Join(workspace, "source.txt")
	if err := os.WriteFile(sourcePath, []byte("before\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	sourceInfo, err := os.Lstat(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	plan := &applyPatchPlan{ops: []plannedApplyPatchOp{
		{
			kind: "update", sourceLabel: "source.txt", targetLabel: "source.txt",
			sourcePath: sourcePath, targetPath: sourcePath,
			source: &applyPatchFileSnapshot{
				path: sourcePath, info: sourceInfo, mode: 0o640,
				data: []byte("before\n"), linkCount: 1,
			},
			before: []byte("before\n"), after: []byte("after\n"), mode: 0o640,
		},
		{
			kind: "add", targetLabel: "nested/one.txt",
			targetPath: filepath.Join(workspace, "nested", "one.txt"),
			after:      []byte("one\n"), mode: 0o644,
		},
		{
			kind: "add", targetLabel: "nested/deeper/two.txt",
			targetPath: filepath.Join(workspace, "nested", "deeper", "two.txt"),
			after:      []byte("two\n"), mode: 0o644,
		},
	}}
	intent, err := buildApplyPatchTxnIntent(context.Background(), plan)
	if err != nil {
		t.Fatalf("buildApplyPatchTxnIntent() error = %v", err)
	}
	defer intent.Close()
	if len(intent.operations) != 3 || len(intent.forests) != 1 {
		t.Fatalf("intent operations/forests = %d/%d", len(intent.operations), len(intent.forests))
	}
	forest := intent.forests[0]
	if len(forest.operations) != 2 || forest.publicRoot != "nested" ||
		forest.sentinelRelativePath != "one.txt" || forest.stageRoot == "" ||
		forest.rollbackRoot == "" || forest.sentinelWitnessName == "" {
		t.Fatalf("forest intent = %+v", forest)
	}
	update := intent.operations[0]
	if update.source == nil || update.targetAnchor == nil || update.stageName == "" ||
		update.postWitnessName == "" || update.sourceWitnessName == "" ||
		update.sourceProbeWitness == "" ||
		update.sourceQuarantine == "" || update.sourceRestoreStage == "" ||
		update.backupName == "" {
		t.Fatalf("update intent = %+v", update)
	}
	entries, err := os.ReadDir(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "source.txt" {
		t.Fatalf("intent construction mutated workspace: %#v", entries)
	}
}

func TestApplyPatchTransactionIntentRejectsSourceABA(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "source.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	held, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	removeErr := os.Remove(path)
	if removeErr != nil {
		t.Fatal(removeErr)
	}
	rewriteErr := os.WriteFile(path, []byte("before\n"), 0o600)
	if rewriteErr != nil {
		t.Fatal(rewriteErr)
	}
	plan := &applyPatchPlan{ops: []plannedApplyPatchOp{{
		kind: "delete", sourceLabel: "source.txt", sourcePath: path,
		source: &applyPatchFileSnapshot{
			path: path, info: info, mode: 0o600, data: []byte("before\n"), linkCount: 1,
		},
		before: []byte("before\n"), mode: 0o600,
	}}}
	intent, err := buildApplyPatchTxnIntent(context.Background(), plan)
	if intent != nil {
		_ = intent.Close()
	}
	if err == nil {
		t.Fatal("identical-content source ABA was accepted")
	}
}
