package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionCleanupCloseoutGuards(t *testing.T) {
	if err := cleanupApplyPatchTxnPrePONR(nil, nil, nil, nil); err == nil {
		t.Fatal("nil pre-PONR cleanup succeeded")
	}
	if err := cleanupApplyPatchTxnPublicStages(
		&applyPatchTxnIntentPlan{},
		&applyPatchTransactionJournal{},
		func() error { return nil },
	); err != nil {
		t.Fatalf("empty public-stage cleanup = %v", err)
	}
	if err := cleanupApplyPatchTxnForestStage(nil, nil, nil); err == nil {
		t.Fatal("nil forest-stage cleanup succeeded")
	}
	if err := cleanupApplyPatchTxnForestStage(
		&applyPatchTxnForestIntent{anchor: &applyPatchTxnAnchor{}},
		&applyPatchTransactionJournalForest{},
		func() error { return nil },
	); err != nil {
		t.Fatalf("uncheckpointed forest-stage cleanup = %v", err)
	}
}

func TestApplyPatchTransactionCleanupCloseoutPublicStages(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		patch string
		seed  func(string) error
	}{
		{
			name:  "source and target artifacts",
			patch: "*** Begin Patch\n*** Update File: source.txt\n@@\n-before\n+after\n*** End Patch",
			seed: func(workspace string) error {
				return os.WriteFile(filepath.Join(workspace, "source.txt"), []byte("before\n"), 0o600)
			},
		},
		{
			name:  "nested forest",
			patch: "*** Begin Patch\n*** Add File: nested/deeper/result.txt\n+candidate\n*** End Patch",
			seed:  func(string) error { return nil },
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			transaction, workspace := newApplyPatchTxnCleanupCloseoutTransaction(t, testCase.patch, testCase.seed)
			if err := cleanupApplyPatchTxnPublicStages(
				transaction.intent,
				transaction.journal,
				transaction.checkpoint,
			); err != nil {
				t.Fatalf("public stage cleanup = %v", err)
			}
			assertNoApplyPatchTxnWorkspaceResidue(t, workspace)
		})
	}
}

func TestApplyPatchTransactionCleanupCloseoutFailures(t *testing.T) {
	t.Run("checkpoint interruption", func(t *testing.T) {
		transaction, _ := newApplyPatchTxnCleanupCloseoutTransaction(
			t,
			"*** Begin Patch\n*** Add File: result.txt\n+candidate\n*** End Patch",
			func(string) error { return nil },
		)
		injected := errors.New("cleanup checkpoint interrupted")
		if err := cleanupApplyPatchTxnPublicStages(
			transaction.intent,
			transaction.journal,
			func() error { return injected },
		); !errors.Is(err, injected) {
			t.Fatalf("cleanup checkpoint error = %v", err)
		}
	})

	t.Run("missing source artifact", func(t *testing.T) {
		transaction, _ := newApplyPatchTxnCleanupCloseoutTransaction(
			t,
			"*** Begin Patch\n*** Update File: source.txt\n@@\n-before\n+after\n*** End Patch",
			func(workspace string) error {
				return os.WriteFile(filepath.Join(workspace, "source.txt"), []byte("before\n"), 0o600)
			},
		)
		clone := cloneApplyPatchTxnStoreCoverageJournal(t, transaction)
		for index := range clone.Artifacts {
			if clone.Artifacts[index].Role == applyPatchTransactionArtifactSourceProbeWitness {
				clone.Artifacts = append(clone.Artifacts[:index], clone.Artifacts[index+1:]...)
				break
			}
		}
		if err := cleanupApplyPatchTxnPublicStages(
			transaction.intent, clone, func() error { return nil },
		); err == nil {
			t.Fatal("missing source artifact cleanup succeeded")
		}
	})

	t.Run("forest identity mismatch", func(t *testing.T) {
		transaction, _ := newApplyPatchTxnCleanupCloseoutTransaction(
			t,
			"*** Begin Patch\n*** Add File: nested/deeper/result.txt\n+candidate\n*** End Patch",
			func(string) error { return nil },
		)
		forest, err := requireApplyPatchTxnJournalForest(
			transaction.journal, transaction.intent.forests[0].id,
		)
		if err != nil || forest.StageRoot.Identity == nil {
			t.Fatal(errors.Join(err, errors.New("forest stage unavailable")))
		}
		forest.StageRoot.Identity.File++
		if err := cleanupApplyPatchTxnForestStage(
			transaction.intent.forests[0], forest, transaction.checkpoint,
		); err == nil {
			t.Fatal("wrong forest identity cleanup succeeded")
		}
	})

	t.Run("forest stage already absent", func(t *testing.T) {
		transaction, _ := newApplyPatchTxnCleanupCloseoutTransaction(
			t,
			"*** Begin Patch\n*** Add File: nested/result.txt\n+candidate\n*** End Patch",
			func(string) error { return nil },
		)
		forestIntent := transaction.intent.forests[0]
		forest, err := requireApplyPatchTxnJournalForest(transaction.journal, forestIntent.id)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Join(forestIntent.anchorPath, forestIntent.stageRoot)); err != nil {
			t.Fatal(err)
		}
		forest.SentinelWitness.Identity = nil
		if err := cleanupApplyPatchTxnForestStage(
			forestIntent, forest, func() error { return nil },
		); err != nil {
			t.Fatalf("absent forest stage cleanup = %v", err)
		}
	})

	t.Run("missing forest journal", func(t *testing.T) {
		transaction, _ := newApplyPatchTxnCleanupCloseoutTransaction(
			t,
			"*** Begin Patch\n*** Add File: nested/result.txt\n+candidate\n*** End Patch",
			func(string) error { return nil },
		)
		transaction.journal.Forests = nil
		if err := cleanupApplyPatchTxnPublicStages(
			transaction.intent, transaction.journal, transaction.checkpoint,
		); err == nil {
			t.Fatal("missing forest journal cleanup succeeded")
		}
	})

	for _, role := range []applyPatchTransactionArtifactRole{
		applyPatchTransactionArtifactPostimageWitness,
		applyPatchTransactionArtifactPostimageStage,
	} {
		t.Run("missing target "+string(role), func(t *testing.T) {
			transaction, _ := newApplyPatchTxnCleanupCloseoutTransaction(
				t,
				"*** Begin Patch\n*** Add File: result.txt\n+candidate\n*** End Patch",
				func(string) error { return nil },
			)
			removeApplyPatchTxnCloseoutArtifact(t, transaction.journal, 0, role)
			if err := cleanupApplyPatchTxnPublicStages(
				transaction.intent, transaction.journal, transaction.checkpoint,
			); err == nil {
				t.Fatal("missing target artifact cleanup succeeded")
			}
		})
	}

	for _, role := range []applyPatchTransactionArtifactRole{
		applyPatchTransactionArtifactPostimageWitness,
		applyPatchTransactionArtifactPostimageStage,
	} {
		t.Run("changed target "+string(role), func(t *testing.T) {
			transaction, _ := newApplyPatchTxnCleanupCloseoutTransaction(
				t,
				"*** Begin Patch\n*** Add File: result.txt\n+candidate\n*** End Patch",
				func(string) error { return nil },
			)
			artifact, err := requireApplyPatchTxnArtifact(transaction.journal, 0, role)
			if err != nil || artifact.Rooted.Identity == nil {
				t.Fatal(errors.Join(err, errors.New("target artifact unavailable")))
			}
			artifact.Rooted.Identity.File++
			if err := cleanupApplyPatchTxnPublicStages(
				transaction.intent, transaction.journal, transaction.checkpoint,
			); err == nil {
				t.Fatal("changed target artifact cleanup succeeded")
			}
		})
	}

	t.Run("changed forest sentinel", func(t *testing.T) {
		transaction, _ := newApplyPatchTxnCleanupCloseoutTransaction(
			t,
			"*** Begin Patch\n*** Add File: nested/result.txt\n+candidate\n*** End Patch",
			func(string) error { return nil },
		)
		forest, err := requireApplyPatchTxnJournalForest(
			transaction.journal, transaction.intent.forests[0].id,
		)
		if err != nil || forest.SentinelWitness.Identity == nil {
			t.Fatal(errors.Join(err, errors.New("forest witness unavailable")))
		}
		forest.SentinelWitness.Identity.File++
		if err := cleanupApplyPatchTxnForestStage(
			transaction.intent.forests[0], forest, transaction.checkpoint,
		); err == nil {
			t.Fatal("changed forest sentinel cleanup succeeded")
		}
	})

	t.Run("changed source restore identity", func(t *testing.T) {
		transaction, _ := newApplyPatchTxnCleanupCloseoutTransaction(
			t,
			"*** Begin Patch\n*** Update File: source.txt\n@@\n-before\n+after\n*** End Patch",
			func(workspace string) error {
				return os.WriteFile(filepath.Join(workspace, "source.txt"), []byte("before\n"), 0o600)
			},
		)
		operation := transaction.intent.operations[0]
		restore, err := requireApplyPatchTxnArtifact(
			transaction.journal, 0, applyPatchTransactionArtifactSourceRestoreStage,
		)
		if err != nil {
			t.Fatal(err)
		}
		file, identity, err := applyPatchTxnCreateRegular(
			operation.source.anchor, restore.Rooted.Basename, 0o600,
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		identity.File++
		restore.Rooted.Identity = &identity
		if err := cleanupApplyPatchTxnPublicStages(
			transaction.intent, transaction.journal, transaction.checkpoint,
		); err == nil {
			t.Fatal("changed source restore cleanup succeeded")
		}
	})

	t.Run("forest stage replaced by regular file", func(t *testing.T) {
		transaction, _ := newApplyPatchTxnCleanupCloseoutTransaction(
			t,
			"*** Begin Patch\n*** Add File: nested/result.txt\n+candidate\n*** End Patch",
			func(string) error { return nil },
		)
		intent := transaction.intent.forests[0]
		forest, err := requireApplyPatchTxnJournalForest(transaction.journal, intent.id)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Join(intent.anchorPath, intent.stageRoot)); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(intent.anchorPath, intent.stageRoot), []byte("file"), 0o600,
		); err != nil {
			t.Fatal(err)
		}
		forest.SentinelWitness.Identity = nil
		if err := cleanupApplyPatchTxnForestStage(
			intent, forest, transaction.checkpoint,
		); err == nil {
			t.Fatal("regular-file forest stage cleanup succeeded")
		}
	})
}

func TestApplyPatchTransactionCleanupCloseoutPrePONR(t *testing.T) {
	transaction, workspace := newApplyPatchTxnCleanupCloseoutTransaction(
		t,
		"*** Begin Patch\n*** Add File: nested/result.txt\n+candidate\n*** End Patch",
		func(string) error { return nil },
	)
	if err := cleanupApplyPatchTxnPrePONR(
		transaction.intent,
		transaction.journal,
		transaction.store,
		transaction.key[:],
	); err != nil {
		t.Fatalf("pre-PONR cleanup = %v", err)
	}
	transaction.store = nil
	assertNoApplyPatchTxnWorkspaceResidue(t, workspace)
}

func TestApplyPatchTransactionCleanupCloseoutPrePONRFinishFailure(t *testing.T) {
	transaction, _ := newApplyPatchTxnCleanupCloseoutTransaction(
		t,
		"*** Begin Patch\n*** Add File: result.txt\n+candidate\n*** End Patch",
		func(string) error { return nil },
	)
	transaction.store.mu.Lock()
	file, err := transaction.store.activeRoot.OpenFile(
		"alien", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600,
	)
	if err == nil {
		err = file.Close()
	}
	transaction.store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := cleanupApplyPatchTxnPrePONR(
		transaction.intent, transaction.journal, transaction.store, transaction.key[:],
	); err == nil {
		t.Fatal("alien-state pre-PONR cleanup succeeded")
	}
}

func newApplyPatchTxnCleanupCloseoutTransaction(
	t *testing.T,
	patch string,
	seed func(string) error,
) (*applyPatchPreparedTransaction, string) {
	t.Helper()
	workspace := t.TempDir()
	if seed != nil {
		if err := seed(workspace); err != nil {
			t.Fatal(err)
		}
	}
	plan := buildApplyPatchTxnTestPlan(t, workspace, patch)
	state, workspaceState := openApplyPatchTxnTestState(t, plan.workspace)
	transaction, err := beginApplyPatchTransaction(
		context.Background(), state, workspaceState, plan,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transaction.closeHandles() })
	return transaction, workspace
}
