package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionEffectCloseoutQuarantineFailures(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(*testing.T, *applyPatchPreparedTransaction, *applyPatchTxnIntent)
	}{
		{
			name: "missing witness artifact",
			mutate: func(t *testing.T, transaction *applyPatchPreparedTransaction, operation *applyPatchTxnIntent) {
				removeApplyPatchTxnCloseoutArtifact(
					t, transaction.journal, operation.index,
					applyPatchTransactionArtifactSourceWitness,
				)
			},
		},
		{
			name: "witness collision",
			mutate: func(t *testing.T, _ *applyPatchPreparedTransaction, operation *applyPatchTxnIntent) {
				if err := os.WriteFile(
					filepath.Join(operation.source.anchor.canonical, operation.sourceWitnessName),
					[]byte("alien"), 0o600,
				); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "source metadata mismatch",
			mutate: func(_ *testing.T, transaction *applyPatchPreparedTransaction, operation *applyPatchTxnIntent) {
				transaction.journal.Operations[operation.index].Before.SHA256 = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
			},
		},
		{
			name: "witness checkpoint interruption",
			mutate: func(_ *testing.T, transaction *applyPatchPreparedTransaction, _ *applyPatchTxnIntent) {
				transaction.fault = func(boundary string) error {
					if boundary == "journal_replace_before_rename" {
						return errors.New("witness checkpoint interrupted")
					}
					return nil
				}
			},
		},
		{
			name: "missing quarantine artifact",
			mutate: func(t *testing.T, transaction *applyPatchPreparedTransaction, operation *applyPatchTxnIntent) {
				removeApplyPatchTxnCloseoutArtifact(
					t, transaction.journal, operation.index,
					applyPatchTransactionArtifactSourceQuarantine,
				)
			},
		},
		{
			name: "quarantine collision",
			mutate: func(t *testing.T, _ *applyPatchPreparedTransaction, operation *applyPatchTxnIntent) {
				if err := os.WriteFile(
					filepath.Join(operation.source.anchor.canonical, operation.sourceQuarantine),
					[]byte("alien"), 0o600,
				); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "quarantine checkpoint interruption",
			mutate: func(_ *testing.T, transaction *applyPatchPreparedTransaction, _ *applyPatchTxnIntent) {
				transaction.fault = func(boundary string) error {
					if boundary == "journal_replace_before_rename" &&
						transaction.effects.sourceQuarantined[0] {
						return errors.New("quarantine checkpoint interrupted")
					}
					return nil
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			transaction := newApplyPatchTxnEffectCloseoutUpdate(t)
			operation := transaction.intent.operations[0]
			testCase.mutate(t, transaction, operation)
			if err := transaction.quarantineSources(); err == nil {
				t.Fatal("invalid source quarantine succeeded")
			}
		})
	}
}

func TestApplyPatchTransactionEffectCloseoutPublishFailures(t *testing.T) {
	t.Run("shared forest publishes once", func(t *testing.T) {
		transaction := newApplyPatchTxnEffectCloseoutTransaction(
			t,
			"*** Begin Patch\n"+
				"*** Add File: nested/one.txt\n+one\n"+
				"*** Add File: nested/two.txt\n+two\n"+
				"*** End Patch",
			nil,
		)
		if err := transaction.publishTargets(); err != nil {
			t.Fatal(err)
		}
		if len(transaction.effects.forestPublished) != 1 {
			t.Fatalf("published forests = %v", transaction.effects.forestPublished)
		}
	})

	for _, testCase := range []struct {
		name   string
		forest bool
		mutate func(*testing.T, *applyPatchPreparedTransaction)
	}{
		{
			name:   "missing forest journal",
			forest: true,
			mutate: func(_ *testing.T, transaction *applyPatchPreparedTransaction) {
				transaction.journal.Forests = nil
			},
		},
		{
			name:   "changed staged forest",
			forest: true,
			mutate: func(t *testing.T, transaction *applyPatchPreparedTransaction) {
				forest, err := requireApplyPatchTxnJournalForest(
					transaction.journal, transaction.intent.forests[0].id,
				)
				if err != nil {
					t.Fatal(err)
				}
				forest.StageRoot.Identity.File++
			},
		},
		{
			name:   "forest target collision",
			forest: true,
			mutate: func(t *testing.T, transaction *applyPatchPreparedTransaction) {
				forest := transaction.intent.forests[0]
				if err := os.Mkdir(filepath.Join(forest.anchorPath, forest.publicRoot), 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:   "forest checkpoint interruption",
			forest: true,
			mutate: func(_ *testing.T, transaction *applyPatchPreparedTransaction) {
				transaction.fault = func(boundary string) error {
					if boundary == "journal_replace_before_rename" &&
						len(transaction.effects.forestPublished) != 0 {
						return errors.New("forest checkpoint interrupted")
					}
					return nil
				}
			},
		},
		{
			name: "target participant mismatch",
			mutate: func(_ *testing.T, transaction *applyPatchPreparedTransaction) {
				stage, _ := requireApplyPatchTxnArtifact(
					transaction.journal, 0, applyPatchTransactionArtifactPostimageStage,
				)
				stage.Rooted.Identity.File++
			},
		},
		{
			name: "target collision",
			mutate: func(t *testing.T, transaction *applyPatchPreparedTransaction) {
				operation := transaction.intent.operations[0]
				if err := os.WriteFile(operation.planned.targetPath, []byte("alien"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "target checkpoint interruption",
			mutate: func(_ *testing.T, transaction *applyPatchPreparedTransaction) {
				transaction.fault = func(boundary string) error {
					if boundary == "journal_replace_before_rename" &&
						transaction.effects.targetPublished[0] {
						return errors.New("target checkpoint interrupted")
					}
					return nil
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			patch := "*** Begin Patch\n*** Add File: result.txt\n+candidate\n*** End Patch"
			if testCase.forest {
				patch = "*** Begin Patch\n*** Add File: nested/result.txt\n+candidate\n*** End Patch"
			}
			transaction := newApplyPatchTxnEffectCloseoutTransaction(t, patch, nil)
			testCase.mutate(t, transaction)
			if err := transaction.publishTargets(); err == nil {
				t.Fatal("invalid target publication succeeded")
			}
		})
	}
}

func TestApplyPatchTransactionEffectCloseoutCommittedVerification(t *testing.T) {
	t.Run("delete source remains", func(t *testing.T) {
		transaction := newApplyPatchTxnEffectCloseoutTransaction(
			t,
			"*** Begin Patch\n*** Delete File: source.txt\n*** End Patch",
			func(workspace string) error {
				return os.WriteFile(filepath.Join(workspace, "source.txt"), []byte("before\n"), 0o600)
			},
		)
		if err := verifyApplyPatchTxnCommittedPublicState(
			transaction.intent, transaction.journal, transaction.effects,
		); err == nil {
			t.Fatal("unquarantined delete source verified committed")
		}
	})

	t.Run("published target corruptions", func(t *testing.T) {
		for _, corruption := range []string{"missing effect", "changed bytes", "stage residue"} {
			t.Run(corruption, func(t *testing.T) {
				transaction := newApplyPatchTxnEffectCloseoutTransaction(
					t,
					"*** Begin Patch\n*** Add File: result.txt\n+candidate\n*** End Patch",
					nil,
				)
				if err := transaction.publishTargets(); err != nil {
					t.Fatal(err)
				}
				switch corruption {
				case "missing effect":
					transaction.effects.targetPublished[0] = false
				case "changed bytes":
					if err := os.WriteFile(
						transaction.intent.operations[0].planned.targetPath,
						[]byte("changed\n"), 0o600,
					); err != nil {
						t.Fatal(err)
					}
				case "stage residue":
					operation := transaction.intent.operations[0]
					if err := os.WriteFile(
						filepath.Join(operation.targetAnchor.canonical, operation.stageName),
						[]byte("residue"), 0o600,
					); err != nil {
						t.Fatal(err)
					}
				}
				if err := verifyApplyPatchTxnCommittedPublicState(
					transaction.intent, transaction.journal, transaction.effects,
				); err == nil {
					t.Fatal("corrupt committed target verified")
				}
			})
		}
	})

	t.Run("forest missing effect", func(t *testing.T) {
		transaction := newApplyPatchTxnEffectCloseoutTransaction(
			t,
			"*** Begin Patch\n*** Add File: nested/result.txt\n+candidate\n*** End Patch",
			nil,
		)
		if err := transaction.publishTargets(); err != nil {
			t.Fatal(err)
		}
		transaction.effects.forestPublished = map[string]bool{}
		if err := verifyApplyPatchTxnCommittedPublicState(
			transaction.intent, transaction.journal, transaction.effects,
		); err == nil {
			t.Fatal("untracked published forest verified")
		}
	})
}

func TestApplyPatchTransactionEffectCloseoutForestVerification(t *testing.T) {
	for _, corruption := range []string{
		"stage residue",
		"missing identities",
		"missing witness",
		"changed file",
		"alien member",
	} {
		t.Run(corruption, func(t *testing.T) {
			transaction := newApplyPatchTxnEffectCloseoutTransaction(
				t,
				"*** Begin Patch\n*** Add File: nested/deeper/result.txt\n+candidate\n*** End Patch",
				nil,
			)
			if err := transaction.publishTargets(); err != nil {
				t.Fatal(err)
			}
			intent := transaction.intent.forests[0]
			forest, err := requireApplyPatchTxnJournalForest(transaction.journal, intent.id)
			if err != nil {
				t.Fatal(err)
			}
			switch corruption {
			case "stage residue":
				if err := os.Mkdir(filepath.Join(intent.anchorPath, intent.stageRoot), 0o700); err != nil {
					t.Fatal(err)
				}
			case "missing identities":
				forest.StageRoot.Identity = nil
			case "missing witness":
				if err := os.Remove(filepath.Join(intent.anchorPath, forest.SentinelWitness.Basename)); err != nil {
					t.Fatal(err)
				}
			case "changed file":
				if err := os.WriteFile(
					filepath.Join(intent.anchorPath, intent.publicRoot, "deeper", "result.txt"),
					[]byte("changed\n"), 0o600,
				); err != nil {
					t.Fatal(err)
				}
			case "alien member":
				if err := os.WriteFile(
					filepath.Join(intent.anchorPath, intent.publicRoot, "alien"),
					[]byte("alien"), 0o600,
				); err != nil {
					t.Fatal(err)
				}
			}
			if err := verifyApplyPatchTxnPublishedForest(intent, forest, false); err == nil {
				t.Fatal("corrupt published forest verified")
			}
		})
	}
}

func TestApplyPatchTransactionEffectCloseoutCommittedCleanupMetadata(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		patch  string
		seed   func(string) error
		mutate func(*testing.T, *applyPatchPreparedTransaction)
	}{
		{
			name:  "missing source artifact",
			patch: "*** Begin Patch\n*** Update File: source.txt\n@@\n-before\n+after\n*** End Patch",
			seed: func(workspace string) error {
				return os.WriteFile(filepath.Join(workspace, "source.txt"), []byte("before\n"), 0o600)
			},
			mutate: func(t *testing.T, transaction *applyPatchPreparedTransaction) {
				removeApplyPatchTxnCloseoutArtifact(
					t, transaction.journal, 0,
					applyPatchTransactionArtifactSourceWitness,
				)
			},
		},
		{
			name:  "missing target witness",
			patch: "*** Begin Patch\n*** Add File: result.txt\n+candidate\n*** End Patch",
			mutate: func(t *testing.T, transaction *applyPatchPreparedTransaction) {
				removeApplyPatchTxnCloseoutArtifact(
					t, transaction.journal, 0,
					applyPatchTransactionArtifactPostimageWitness,
				)
			},
		},
		{
			name:  "missing forest witness",
			patch: "*** Begin Patch\n*** Add File: nested/result.txt\n+candidate\n*** End Patch",
			mutate: func(t *testing.T, transaction *applyPatchPreparedTransaction) {
				forest, err := requireApplyPatchTxnJournalForest(
					transaction.journal, transaction.intent.forests[0].id,
				)
				if err != nil {
					t.Fatal(err)
				}
				forest.SentinelWitness.Identity = nil
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			transaction := newApplyPatchTxnEffectCloseoutTransaction(
				t, testCase.patch, testCase.seed,
			)
			testCase.mutate(t, transaction)
			if err := transaction.cleanupCommittedPublicArtifacts(); err == nil {
				t.Fatal("missing committed cleanup metadata succeeded")
			}
		})
	}
}

func TestApplyPatchTransactionEffectCloseoutCommitVerificationWindows(t *testing.T) {
	for _, boundary := range []string{"target_publish:0", "before_decision"} {
		t.Run(boundary, func(t *testing.T) {
			transaction := newApplyPatchTxnEffectCloseoutTransaction(
				t,
				"*** Begin Patch\n*** Add File: result.txt\n+candidate\n*** End Patch",
				nil,
			)
			mutated := false
			transaction.fault = func(observed string) error {
				if observed == boundary && !mutated {
					mutated = true
					return os.WriteFile(
						transaction.intent.operations[0].planned.targetPath,
						[]byte("alien\n"),
						0o600,
					)
				}
				return nil
			}
			commitErr := transaction.commit()
			if commitErr == nil || !mutated {
				t.Fatalf("mutated %s commit = %v", boundary, commitErr)
			}
		})
	}
}

func TestApplyPatchTransactionEffectCloseoutDecisionPrevalidation(t *testing.T) {
	for _, corruption := range []string{"active directory moved", "journal changed"} {
		t.Run(corruption, func(t *testing.T) {
			fixture, transaction := newApplyPatchTxnCommittedDecisionTestTransaction(t)
			workspacePath, err := transaction.store.workspace.directoryPath()
			if err != nil {
				t.Fatal(err)
			}
			activePath := filepath.Join(workspacePath, transaction.store.activeName)
			switch corruption {
			case "active directory moved":
				if err := os.Rename(activePath, activePath+"-moved"); err != nil {
					t.Fatal(err)
				}
			case "journal changed":
				if err := os.WriteFile(
					filepath.Join(activePath, applyPatchTransactionJournalFile),
					[]byte("alien"),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			}
			if err := transaction.syncJournalDirectoryOrUncertain(
				errors.New("visible write failed"),
			); !errors.Is(err, errApplyPatchCommitUncertain) {
				t.Fatalf("decision prevalidation = %v", err)
			}
			fixture.simulateCrash(t, transaction)
		})
	}
}

func newApplyPatchTxnEffectCloseoutUpdate(t *testing.T) *applyPatchPreparedTransaction {
	t.Helper()
	return newApplyPatchTxnEffectCloseoutTransaction(
		t,
		"*** Begin Patch\n*** Update File: source.txt\n@@\n-before\n+after\n*** End Patch",
		func(workspace string) error {
			return os.WriteFile(filepath.Join(workspace, "source.txt"), []byte("before\n"), 0o600)
		},
	)
}

func newApplyPatchTxnEffectCloseoutTransaction(
	t *testing.T,
	patch string,
	seed func(string) error,
) *applyPatchPreparedTransaction {
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
	if err := transaction.revalidate(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if err := transaction.markPrepared(context.Background()); err != nil {
		t.Fatal(err)
	}
	transaction.effects = applyPatchTxnCloseoutEffects()
	return transaction
}

func applyPatchTxnCloseoutEffects() applyPatchTxnEffects {
	return applyPatchTxnEffects{
		sourceQuarantined:         make(map[int]bool),
		sourceRestoreRequired:     make(map[int]bool),
		targetPublished:           make(map[int]bool),
		targetRollbackQuarantined: make(map[int]bool),
		forestPublished:           make(map[string]bool),
		forestRollbackQuarantined: make(map[string]bool),
	}
}

func removeApplyPatchTxnCloseoutArtifact(
	t *testing.T,
	journal *applyPatchTransactionJournal,
	operationIndex int,
	role applyPatchTransactionArtifactRole,
) {
	t.Helper()
	for index := range journal.Artifacts {
		artifact := journal.Artifacts[index]
		if artifact.OperationIndex == operationIndex && artifact.Role == role {
			journal.Artifacts = append(journal.Artifacts[:index], journal.Artifacts[index+1:]...)
			return
		}
	}
	t.Fatalf("artifact %s for operation %d is unavailable", role, operationIndex)
}
