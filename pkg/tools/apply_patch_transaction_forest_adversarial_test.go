package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyPatchTransactionRejectsUndeclaredPreparedForestMember(t *testing.T) {
	workspace := t.TempDir()
	stateRoot := filepath.Join(t.TempDir(), "transaction-state")
	tool := newApplyPatchPreflightTestTool(
		t,
		workspace,
		true,
		true,
		ApplyPatchPreflightPolicy{TransactionStateRoot: stateRoot},
	)
	injected := false
	var alienPath string
	tool.afterPointOfNoReturn = func(*applyPatchPlan) {
		entries, err := os.ReadDir(workspace)
		if err != nil {
			t.Fatalf("read prepared forest anchor: %v", err)
		}
		for _, entry := range entries {
			if !entry.IsDir() || !strings.HasPrefix(
				entry.Name(),
				".picoclaw-apply-patch-forest-stage-",
			) {
				continue
			}
			alienPath = filepath.Join(workspace, entry.Name(), "alien.txt")
			if err := os.WriteFile(alienPath, []byte("undeclared\n"), 0o600); err != nil {
				t.Fatalf("inject undeclared forest member: %v", err)
			}
			injected = true
			return
		}
		t.Fatal("prepared forest stage was not found")
	}

	result := executeApplyPatch(
		t,
		tool,
		context.Background(),
		"*** Begin Patch\n"+
			"*** Add File: nested/deeper/result.txt\n+candidate\n"+
			"*** End Patch",
	)
	if !injected {
		t.Fatal("undeclared forest member was not injected")
	}
	if result == nil || !result.IsError || result.ForUser != "" {
		t.Fatalf("undeclared forest result = %#v", result)
	}
	if _, err := os.Lstat(filepath.Join(workspace, "nested")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("undeclared forest was published: %v", err)
	}
	data, err := os.ReadFile(alienPath)
	if err != nil || string(data) != "undeclared\n" {
		t.Fatalf("undeclared member was clobbered: %q, %v", data, err)
	}
	retry := executeApplyPatch(
		t,
		tool,
		context.Background(),
		"*** Begin Patch\n*** Add File: must-not-run.txt\n+blocked\n*** End Patch",
	)
	if retry == nil || !retry.IsError || retry.ForUser != "" {
		t.Fatalf("forest conflict retry result = %#v", retry)
	}
	if _, err := os.Lstat(filepath.Join(workspace, "must-not-run.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new patch ran through retained forest conflict: %v", err)
	}
}

func TestApplyPatchTransactionRecoveryResumesPartiallyRemovedForest(t *testing.T) {
	fixture := newApplyPatchTxnRecoveryFixture(t, "nested/deeper/result.txt")
	transaction := prepareApplyPatchTxnCrashTransaction(t, fixture)
	initializeApplyPatchTxnCrashEffects(transaction)
	if err := transaction.publishTargets(); err != nil {
		t.Fatal(err)
	}
	markApplyPatchTxnCrashRollingBack(t, transaction)

	forestIntent := transaction.intent.forests[0]
	forest, err := requireApplyPatchTxnJournalForest(
		transaction.journal,
		forestIntent.id,
	)
	if err != nil || forest.StageRoot.Identity == nil ||
		forest.SentinelWitness.Identity == nil {
		t.Fatal(errors.Join(err, errors.New("forest recovery identity unavailable")))
	}
	if err := applyPatchTxnQuarantineExact(
		forestIntent.anchor,
		forestIntent.publicRoot,
		forestIntent.rollbackRoot,
		*forest.StageRoot.Identity,
	); err != nil {
		t.Fatal(err)
	}
	forest.RollbackRoot.Identity = copyApplyPatchTxnIdentity(*forest.StageRoot.Identity)
	transaction.effects.forestPublished[forestIntent.id] = false
	transaction.effects.forestRollbackQuarantined[forestIntent.id] = true
	if err := applyPatchTxnSyncDirectory(forestIntent.anchor); err != nil {
		t.Fatal(err)
	}
	if err := transaction.checkpoint(); err != nil {
		t.Fatal(err)
	}
	if err := applyPatchTxnRemoveExact(
		forestIntent.anchor,
		forest.SentinelWitness.Basename,
		forest.SentinelWitness.RemovalBasename,
		*forest.SentinelWitness.Identity,
		false,
	); err != nil {
		t.Fatal(err)
	}
	if err := applyPatchTxnSyncDirectory(forestIntent.anchor); err != nil {
		t.Fatal(err)
	}
	removeOneApplyPatchTxnCrashForestFile(t, forestIntent, forest)
	fixture.simulateCrash(t, transaction)

	recoverApplyPatchTxnCrashFixture(t, fixture)
	if _, err := os.Lstat(filepath.Join(fixture.workspacePath, "nested")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partially removed forest remained after recovery: %v", err)
	}
	assertNoApplyPatchTxnWorkspaceResidue(t, fixture.workspacePath)
	assertApplyPatchTxnFaultStateReady(t, fixture.workspacePath, fixture.stateRoot)
}

func TestApplyPatchTransactionRecoveryConvergesAfterRollbackJournalRemoval(t *testing.T) {
	fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
	transaction := prepareApplyPatchTxnCrashTransaction(t, fixture)
	initializeApplyPatchTxnCrashEffects(transaction)
	markApplyPatchTxnCrashRollingBack(t, transaction)
	if err := cleanupApplyPatchTxnUnpublishedTarget(
		transaction.intent.operations[0],
		transaction.journal,
		transaction.checkpoint,
	); err != nil {
		t.Fatal(err)
	}
	if err := transaction.store.preparePrivateCleanup(
		transaction.key[:],
		transaction.journal,
	); err != nil {
		t.Fatal(err)
	}
	// Private cleanup authenticates a pointer and quarantines the active
	// directory before deleting its journal. Simulate a crash after journal
	// deletion while that authenticated cleanup shell remains.
	removeAllApplyPatchTxnRecoveryOwnedState(t, transaction.store)
	fixture.simulateCrash(t, transaction)

	recoverApplyPatchTxnCrashFixture(t, fixture)
	if _, err := os.Lstat(filepath.Join(fixture.workspacePath, "result.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rollback cleanup published a target: %v", err)
	}
	assertNoApplyPatchTxnWorkspaceResidue(t, fixture.workspacePath)
	assertApplyPatchTxnFaultStateReady(t, fixture.workspacePath, fixture.stateRoot)
}

func removeOneApplyPatchTxnCrashForestFile(
	t *testing.T,
	intent *applyPatchTxnForestIntent,
	forest *applyPatchTransactionJournalForest,
) {
	t.Helper()
	rollbackPath := filepath.Join(intent.anchorPath, intent.rollbackRoot)
	for index := len(forest.Entries) - 1; index >= 1; index-- {
		entry := &forest.Entries[index]
		if entry.Kind != "file" || entry.Identity == nil {
			continue
		}
		parentPath := rollbackPath
		parentRelative := filepath.Dir(filepath.FromSlash(entry.RelativePath))
		if parentRelative != "." {
			parentPath = filepath.Join(rollbackPath, parentRelative)
		}
		parent, err := openApplyPatchTxnAnchor(parentPath)
		if err != nil {
			t.Fatal(err)
		}
		basename := filepath.Base(filepath.FromSlash(entry.RelativePath))
		removeErr := applyPatchTxnRemoveExact(
			parent,
			basename,
			entry.RemovalBasename,
			*entry.Identity,
			false,
		)
		if removeErr == nil {
			removeErr = applyPatchTxnSyncDirectory(parent)
		}
		closeErr := parent.Close()
		if removeErr != nil || closeErr != nil {
			t.Fatal(errors.Join(removeErr, closeErr))
		}
		return
	}
	t.Fatal("forest regular member was not found")
}
