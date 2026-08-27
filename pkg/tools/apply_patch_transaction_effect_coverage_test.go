package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionEffectCoverageCommitClassifications(t *testing.T) {
	if err := (*applyPatchPreparedTransaction)(nil).commit(); err == nil {
		t.Fatal("nil transaction commit succeeded")
	}
	if err := committedApplyPatchCleanupDeferred(errors.New("deferred")); err != nil {
		t.Fatalf("committed cleanup deferral = %v", err)
	}

	t.Run("forward ordinary rollback", func(t *testing.T) {
		transaction, workspace := newApplyPatchTxnEffectCoverageTransaction(t)
		injected := errors.New("forward marker stayed prepared")
		transaction.fault = func(boundary string) error {
			if boundary == "journal_replace_before_rename" &&
				transaction.journal.Phase == applyPatchTransactionPhasePrepared &&
				transaction.journal.DecisionAttempted {
				return injected
			}
			return nil
		}
		commitErr := transaction.commit()
		if !errors.Is(commitErr, injected) ||
			errors.Is(commitErr, errApplyPatchCommitUncertain) ||
			errors.Is(commitErr, errApplyPatchRollbackIncomplete) {
			t.Fatalf("forward ordinary rollback = %v", commitErr)
		}
		assertNoApplyPatchTxnWorkspaceResidue(t, workspace)
	})

	t.Run("committed marker ordinary rollback", func(t *testing.T) {
		transaction, workspace := newApplyPatchTxnEffectCoverageTransaction(t)
		injected := errors.New("commit marker stayed prepared")
		transaction.fault = func(boundary string) error {
			if boundary == "journal_replace_before_rename" &&
				transaction.journal.Phase == applyPatchTransactionPhaseCommitted {
				return injected
			}
			return nil
		}
		commitErr := transaction.commit()
		if !errors.Is(commitErr, injected) ||
			errors.Is(commitErr, errApplyPatchCommitUncertain) ||
			errors.Is(commitErr, errApplyPatchRollbackIncomplete) {
			t.Fatalf("committed-marker ordinary rollback = %v", commitErr)
		}
		assertNoApplyPatchTxnWorkspaceResidue(t, workspace)
	})

	for _, phase := range []applyPatchTransactionPhase{
		applyPatchTransactionPhasePrepared,
		applyPatchTransactionPhaseCommitted,
	} {
		t.Run("uncertain "+string(phase), func(t *testing.T) {
			transaction, _ := newApplyPatchTxnEffectCoverageTransaction(t)
			visibleErr := errors.New("decision marker visible before sync")
			resyncErr := errors.New("decision marker resync failed")
			transaction.fault = func(boundary string) error {
				if transaction.journal.DecisionAttempted &&
					transaction.journal.Phase == phase {
					switch boundary {
					case "journal_replace_visible_before_sync":
						return visibleErr
					case "journal_decision_resync":
						return resyncErr
					}
				}
				return nil
			}
			commitErr := transaction.commit()
			if !errors.Is(commitErr, errApplyPatchCommitUncertain) ||
				!errors.Is(commitErr, visibleErr) || !errors.Is(commitErr, resyncErr) {
				t.Fatalf("%s uncertain commit = %v", phase, commitErr)
			}
		})
	}

	t.Run("committed cleanup deferred", func(t *testing.T) {
		transaction, _ := newApplyPatchTxnEffectCoverageTransaction(t)
		faultHit := false
		transaction.fault = func(boundary string) error {
			if boundary == "journal_replace_before_rename" &&
				transaction.journal.Phase == applyPatchTransactionPhaseCommitted &&
				transaction.store.committedCleanupAuthenticated {
				faultHit = true
				return errors.New("committed cleanup checkpoint failed")
			}
			return nil
		}
		if err := transaction.commit(); err != nil {
			t.Fatalf("deferred committed cleanup = %v", err)
		}
		if !faultHit {
			t.Fatal("committed cleanup fault was not reached")
		}
	})
}

func TestApplyPatchTransactionEffectCoverageGuards(t *testing.T) {
	operation := &applyPatchTxnIntent{index: 0}
	transaction := &applyPatchPreparedTransaction{
		journal: &applyPatchTransactionJournal{},
		intent:  &applyPatchTxnIntentPlan{},
	}
	if err := requireApplyPatchTxnRootedAbsent(nil, nil); err == nil {
		t.Fatal("nil rooted absence state succeeded")
	}
	if err := transaction.finishBackupRestoredSourceCleanup(operation, nil); err == nil {
		t.Fatal("missing backup restore cleanup metadata succeeded")
	}
	if err := transaction.rollbackPublishedTarget(operation); err == nil {
		t.Fatal("missing published target metadata succeeded")
	}
	if err := transaction.finishRollbackQuarantinedTarget(operation); err == nil {
		t.Fatal("missing rollback quarantine metadata succeeded")
	}
	if err := cleanupApplyPatchTxnUnpublishedTarget(
		operation,
		transaction.journal,
		func() error { return nil },
	); err == nil {
		t.Fatal("missing unpublished target metadata succeeded")
	}
	if err := transaction.rollbackPublishedForest(
		&applyPatchTxnForestIntent{},
		&applyPatchTransactionJournalForest{},
	); err == nil {
		t.Fatal("missing published forest identity succeeded")
	}
	if err := removeApplyPatchTxnForestTree(
		&applyPatchTxnForestIntent{anchorPath: t.TempDir()},
		&applyPatchTransactionJournalForest{Entries: []applyPatchTransactionJournalForestEntry{
			{RelativePath: "."},
			{RelativePath: "child"},
		}},
		"stage",
		func() error { return nil },
	); err == nil {
		t.Fatal("uncheckpointed forest entry removal succeeded")
	}
	if err := removeApplyPatchTxnRootedWithCheckpoint(nil, nil, false, nil); err == nil {
		t.Fatal("nil rooted removal state succeeded")
	}
	if err := removeApplyPatchTxnForestEntryWithCheckpoint(nil, "entry", nil, nil); err == nil {
		t.Fatal("nil forest entry removal state succeeded")
	}
	if err := verifyApplyPatchTxnRetainedOriginal(nil, nil); err == nil {
		t.Fatal("nil retained original succeeded")
	}
	if _, err := committedApplyPatchTxnPostimageLinks(
		operation,
		transaction.journal,
		false,
	); err == nil {
		t.Fatal("missing committed witness metadata succeeded")
	}
	if err := verifyApplyPatchTxnForestTreeAt(
		&applyPatchTxnForestIntent{},
		&applyPatchTransactionJournalForest{},
		"root",
		false,
	); err == nil {
		t.Fatal("missing published forest identity succeeded")
	}
	if err := verifyApplyPatchTxnRollingForestTreeAt(nil, nil, "root"); err == nil {
		t.Fatal("nil rolling forest state succeeded")
	}

	targetOperation := &applyPatchTxnIntent{index: 0, targetAnchor: &applyPatchTxnAnchor{}}
	if err := verifyApplyPatchTxnCommittedPublicState(
		&applyPatchTxnIntentPlan{operations: []*applyPatchTxnIntent{targetOperation}},
		&applyPatchTransactionJournal{Operations: []applyPatchTransactionJournalOperation{{Index: 0}}},
		applyPatchTxnEffects{targetPublished: map[int]bool{}},
	); err == nil {
		t.Fatal("unpublished committed target succeeded")
	}

	missingAnchor := filepath.Join(t.TempDir(), "missing-anchor")
	if err := verifyApplyPatchTxnRollbackPrivateResidueAbsent(
		&applyPatchTxnIntentPlan{},
		&applyPatchTransactionJournal{Artifacts: []applyPatchTransactionJournalArtifact{{
			Rooted: &applyPatchTransactionJournalRootedLocation{
				AnchorCanonicalPath: missingAnchor,
			},
		}}},
	); err == nil {
		t.Fatal("missing rollback artifact anchor succeeded")
	}
}

func TestApplyPatchTransactionEffectCoverageRemovalClassification(t *testing.T) {
	directory := t.TempDir()
	anchor, err := openApplyPatchTxnAnchor(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer anchor.Close()

	if removed, classifyErr := preclassifyApplyPatchTxnRemoval(
		anchor,
		"absent",
		"absent-removal",
		applyPatchTxnIdentity{},
	); classifyErr != nil || !removed {
		t.Fatalf("both absent = removed:%t err:%v", removed, classifyErr)
	}
	file, identity, err := applyPatchTxnCreateRegular(anchor, "source", 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if closeErr := file.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if removed, classifyErr := preclassifyApplyPatchTxnRemoval(
		anchor,
		"source",
		"source-removal",
		identity,
	); classifyErr != nil || removed {
		t.Fatalf("owned source = removed:%t err:%v", removed, classifyErr)
	}
	wrong := identity
	wrong.File++
	if _, classifyErr := preclassifyApplyPatchTxnRemoval(
		anchor,
		"source",
		"source-removal",
		wrong,
	); classifyErr == nil {
		t.Fatal("alien source identity succeeded")
	}
	removal, _, err := applyPatchTxnCreateRegular(anchor, "source-removal", 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := removal.Close(); err != nil {
		t.Fatal(err)
	}
	if _, classifyErr := preclassifyApplyPatchTxnRemoval(
		anchor,
		"source",
		"source-removal",
		identity,
	); classifyErr == nil {
		t.Fatal("unexpected removal quarantine succeeded")
	}
	if _, classifyErr := preclassifyApplyPatchTxnRemoval(
		anchor,
		"../invalid-source",
		"../invalid-removal",
		identity,
	); classifyErr == nil {
		t.Fatal("invalid removal names succeeded")
	}

	checkpointErr := errors.New("removal checkpoint failed")
	location := &applyPatchTransactionJournalRootedLocation{
		Basename:        "source",
		RemovalBasename: "unused-removal",
		Identity:        copyApplyPatchTxnIdentity(identity),
	}
	if err := removeApplyPatchTxnRootedWithCheckpoint(
		anchor,
		location,
		false,
		func() error { return checkpointErr },
	); !errors.Is(err, checkpointErr) {
		t.Fatalf("rooted checkpoint error = %v", err)
	}
}

func TestApplyPatchTransactionEffectCoverageCommittedDefaultUncertain(t *testing.T) {
	transaction, _ := newApplyPatchTxnEffectCoverageTransaction(t)
	transaction.journal.Phase = applyPatchTransactionPhaseCommitted
	transaction.journal.DecisionAttempted = true
	injected := errors.New("commit marker was not published")
	transaction.fault = func(boundary string) error {
		if boundary == "journal_replace_before_rename" {
			return injected
		}
		return nil
	}
	persistErr := transaction.persistCommittedDecision()
	if !errors.Is(persistErr, errApplyPatchCommitUncertain) ||
		!errors.Is(persistErr, injected) {
		t.Fatalf("default committed uncertainty = %v", persistErr)
	}
}

func newApplyPatchTxnEffectCoverageTransaction(
	t *testing.T,
) (*applyPatchPreparedTransaction, string) {
	t.Helper()
	workspace := t.TempDir()
	plan := buildApplyPatchTxnTestPlan(
		t,
		workspace,
		"*** Begin Patch\n*** Add File: result.txt\n+candidate\n*** End Patch",
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
	t.Cleanup(func() { _ = transaction.closeHandles() })
	if err := transaction.revalidate(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if err := transaction.markPrepared(context.Background()); err != nil {
		t.Fatal(err)
	}
	return transaction, workspace
}

func TestApplyPatchTransactionEffectCoverageRootedAbsentConflict(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "present"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	anchor, err := openApplyPatchTxnAnchor(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer anchor.Close()
	if err := requireApplyPatchTxnRootedAbsent(
		anchor,
		&applyPatchTransactionJournalRootedLocation{
			Basename: "present", RemovalBasename: "absent-removal",
		},
	); err == nil {
		t.Fatal("present rooted artifact passed absence check")
	}
}
