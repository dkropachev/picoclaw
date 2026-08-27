package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionEngineCoverageDefensiveBoundaries(t *testing.T) {
	if transaction, err := beginApplyPatchTransaction(nil, nil, nil, nil); transaction != nil || err == nil {
		t.Fatalf("nil begin = %#v, %v", transaction, err)
	}
	var unavailable *applyPatchPreparedTransaction
	if err := unavailable.abortPreparing(); err != nil {
		t.Fatalf("nil abort = %v", err)
	}
	if err := unavailable.closeHandles(); err != nil {
		t.Fatalf("nil close = %v", err)
	}
	if err := unavailable.revalidate(nil, nil); err == nil {
		t.Fatal("nil transaction revalidated")
	}
	if err := unavailable.markPrepared(nil); err == nil {
		t.Fatal("nil transaction marked prepared")
	}
	if err := probeApplyPatchTxnStateNoReplaceCapability(nil, "", nil); err == nil {
		t.Fatal("nil workspace capability probe succeeded")
	}
	if got := expectedApplyPatchTxnForestChildren(nil); len(got) != 1 {
		t.Fatalf("nil forest children = %#v", got)
	}
	if sentinel := findApplyPatchTxnForestSentinel(nil); sentinel != nil {
		t.Fatalf("nil forest sentinel = %#v", sentinel)
	}
	if err := verifyApplyPatchTxnForestManifestAt(nil, nil, ""); err == nil {
		t.Fatal("nil forest manifest verified")
	}
	if err := validateApplyPatchTxnRemovalNamesAbsent(
		&applyPatchTransactionJournal{},
	); err != nil {
		t.Fatalf("empty removal set = %v", err)
	}
}

func TestApplyPatchTransactionEngineCoverageBeginAndRevalidateErrors(t *testing.T) {
	workspacePath := t.TempDir()
	workspace, err := snapshotApplyPatchWorkspace(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	state, workspaceState := openApplyPatchTxnTestState(t, workspace)
	if transaction, beginErr := beginApplyPatchTransaction(
		context.Background(),
		state,
		workspaceState,
		&applyPatchPlan{workspace: workspace},
	); transaction != nil || beginErr == nil {
		t.Fatalf("invalid-plan begin = %#v, %v", transaction, beginErr)
	}

	writeApplyPatchFixture(t, workspacePath, "source.txt", "before\n", 0o640)
	plan := buildApplyPatchTxnTestPlan(
		t,
		workspacePath,
		"*** Begin Patch\n*** Delete File: source.txt\n*** End Patch",
	)
	transaction, err := beginApplyPatchTransaction(
		context.Background(),
		state,
		workspaceState,
		plan,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transaction.abortPreparing() })

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := transaction.revalidate(canceled, plan); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled revalidate = %v", err)
	}
	if err := transaction.markPrepared(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled mark prepared = %v", err)
	}

	transaction.key[0] ^= 0xff
	if err := transaction.revalidate(context.Background(), plan); err == nil {
		t.Fatal("authentication-key drift revalidated")
	}
	transaction.key[0] ^= 0xff

	originalID := transaction.journal.TransactionID
	transaction.journal.TransactionID = "invalid"
	if err := transaction.revalidate(context.Background(), plan); err == nil {
		t.Fatal("invalid in-memory journal revalidated")
	}
	transaction.journal.TransactionID = originalID

	transaction.store.mu.Lock()
	transaction.store.closed = true
	transaction.store.mu.Unlock()
	if err := transaction.markPrepared(context.Background()); err == nil {
		t.Fatal("closed store marked transaction prepared")
	}
	if transaction.journal.Phase != applyPatchTransactionPhasePreparing {
		t.Fatalf("failed mark phase = %q", transaction.journal.Phase)
	}
	transaction.store.mu.Lock()
	transaction.store.closed = false
	transaction.store.mu.Unlock()
}

func TestApplyPatchTransactionEngineCoverageRemovalNameConflicts(t *testing.T) {
	workspace := t.TempDir()
	writeApplyPatchFixture(t, workspace, "source.txt", "before\n", 0o640)
	plan := buildApplyPatchTxnTestPlan(
		t,
		workspace,
		"*** Begin Patch\n*** Delete File: source.txt\n*** End Patch",
	)
	state, workspaceState := openApplyPatchTxnTestState(t, plan.workspace)
	transaction, err := beginApplyPatchTransaction(
		context.Background(), state, workspaceState, plan,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transaction.abortPreparing() })
	artifact, err := requireApplyPatchTxnArtifact(
		transaction.journal,
		0,
		applyPatchTransactionArtifactSourceProbeWitness,
	)
	if err != nil {
		t.Fatal(err)
	}
	removalPath := filepath.Join(
		artifact.Rooted.AnchorCanonicalPath,
		artifact.Rooted.RemovalBasename,
	)
	if err := os.WriteFile(removalPath, []byte("conflict\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateApplyPatchTxnRemovalNamesAbsent(transaction.journal); err == nil {
		t.Fatal("present removal name was accepted")
	}
	if err := os.Remove(removalPath); err != nil {
		t.Fatal(err)
	}

	originalPath := artifact.Rooted.AnchorCanonicalPath
	originalIdentity := artifact.Rooted.AnchorIdentity
	artifact.Rooted.AnchorCanonicalPath = filepath.Join(workspace, "missing-anchor")
	if err := validateApplyPatchTxnRemovalNamesAbsent(transaction.journal); err == nil {
		t.Fatal("missing removal anchor was accepted")
	}
	artifact.Rooted.AnchorCanonicalPath = originalPath
	artifact.Rooted.AnchorIdentity = applyPatchTxnIdentity{
		Device: originalIdentity.Device,
		File:   originalIdentity.File + 1,
		Kind:   originalIdentity.Kind,
	}
	if err := validateApplyPatchTxnRemovalNamesAbsent(transaction.journal); err == nil {
		t.Fatal("changed removal anchor identity was accepted")
	}
	artifact.Rooted.AnchorIdentity = originalIdentity
}

func TestApplyPatchTransactionEngineCoveragePreparedMarkerVisibleFallback(t *testing.T) {
	workspace := t.TempDir()
	plan := buildApplyPatchTxnTestPlan(
		t,
		workspace,
		"*** Begin Patch\n*** Add File: result.txt\n+result\n*** End Patch",
	)
	state, workspaceState := openApplyPatchTxnTestState(t, plan.workspace)
	transaction, err := beginApplyPatchTransaction(
		context.Background(), state, workspaceState, plan,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transaction.abortPreparing() })
	transaction.journal.Phase = applyPatchTransactionPhasePrepared
	injected := errors.New("visible prepared marker before sync")
	writeErr := transaction.store.writeJournal(
		transaction.key[:],
		transaction.journal,
		func(boundary string) error {
			if boundary == "journal_replace_visible_before_sync" {
				return injected
			}
			return nil
		},
	)
	if !errors.Is(writeErr, injected) {
		t.Fatalf("injected prepared write = %v", writeErr)
	}
	transaction.journal.Phase = applyPatchTransactionPhasePreparing
	if err := transaction.markPrepared(context.Background()); err != nil {
		t.Fatalf("visible prepared marker fallback = %v", err)
	}
	if transaction.journal.Phase != applyPatchTransactionPhasePrepared {
		t.Fatalf("fallback phase = %q", transaction.journal.Phase)
	}
}
