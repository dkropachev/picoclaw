package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionRecoveryZeroMarginParticipants(t *testing.T) {
	t.Run("source witness identity", func(t *testing.T) {
		_, transaction, operation := newApplyPatchTxnZeroMarginMove(t)
		if err := transaction.quarantineSources(); err != nil {
			t.Fatal(err)
		}
		witness, err := requireApplyPatchTxnArtifact(
			transaction.journal,
			operation.index,
			applyPatchTransactionArtifactSourceWitness,
		)
		if err != nil {
			t.Fatal(err)
		}
		if witness.Rooted == nil || witness.Rooted.Identity == nil {
			t.Fatal("source witness identity was not checkpointed")
		}
		witness.Rooted.Identity.File++
		if err := validateApplyPatchTxnRecoverySourceParticipants(
			transaction,
			operation,
			false,
		); err == nil {
			t.Fatal("changed source witness identity was accepted")
		}
	})

	t.Run("source quarantine identity", func(t *testing.T) {
		_, transaction, operation := newApplyPatchTxnZeroMarginMove(t)
		if err := transaction.quarantineSources(); err != nil {
			t.Fatal(err)
		}
		quarantine, err := requireApplyPatchTxnArtifact(
			transaction.journal,
			operation.index,
			applyPatchTransactionArtifactSourceQuarantine,
		)
		if err != nil {
			t.Fatal(err)
		}
		if quarantine.Rooted == nil || quarantine.Rooted.Identity == nil {
			t.Fatal("source quarantine identity was not checkpointed")
		}
		quarantine.Rooted.Identity.File++
		if err := validateApplyPatchTxnRecoverySourceParticipants(
			transaction,
			operation,
			false,
		); err == nil {
			t.Fatal("changed source quarantine identity was accepted")
		}
	})

	t.Run("source invalid public name", func(t *testing.T) {
		_, transaction, operation := newApplyPatchTxnZeroMarginMove(t)
		operation.source.basename = "invalid/name"
		if err := validateApplyPatchTxnRecoverySourceParticipants(
			transaction,
			operation,
			false,
		); err == nil {
			t.Fatal("invalid public source name was accepted")
		}
	})

	t.Run("source restore artifact missing", func(t *testing.T) {
		_, transaction, operation := newApplyPatchTxnZeroMarginMove(t)
		if err := os.Remove(operation.planned.sourcePath); err != nil {
			t.Fatal(err)
		}
		removeApplyPatchTxnExactArtifact(
			transaction.journal,
			applyPatchTransactionArtifactSourceRestoreStage,
		)
		if err := validateApplyPatchTxnRecoverySourceParticipants(
			transaction,
			operation,
			false,
		); err == nil {
			t.Fatal("missing source restore artifact was accepted")
		}
	})

	for _, phase := range []applyPatchTransactionPhase{
		applyPatchTransactionPhasePrepared,
		applyPatchTransactionPhaseRollingBack,
	} {
		t.Run("missing source "+string(phase), func(t *testing.T) {
			workspacePath := t.TempDir()
			writeApplyPatchFixture(t, workspacePath, "source.txt", "delete me\n", 0o600)
			fixture := newApplyPatchTxnRecoveryFixtureForPatch(
				t,
				workspacePath,
				"*** Begin Patch\n*** Delete File: source.txt\n*** End Patch",
			)
			transaction := prepareApplyPatchTxnCrashTransaction(t, fixture)
			defer transaction.closeHandles()
			initializeApplyPatchTxnZeroMarginEffects(transaction)
			operation := transaction.intent.operations[0]
			if err := os.Remove(operation.planned.sourcePath); err != nil {
				t.Fatal(err)
			}
			transaction.journal.Phase = phase
			err := validateApplyPatchTxnRecoverySourceParticipants(
				transaction,
				operation,
				false,
			)
			if phase == applyPatchTransactionPhaseRollingBack && err != nil {
				t.Fatalf("rollback accepted missing source without witness: %v", err)
			}
			if phase == applyPatchTransactionPhasePrepared && err == nil {
				t.Fatal("prepared recovery accepted missing source without witness")
			}
		})
	}

	t.Run("target witness unavailable", func(t *testing.T) {
		_, transaction, operation := newApplyPatchTxnZeroMarginAdd(t)
		witness, err := requireApplyPatchTxnArtifact(
			transaction.journal,
			operation.index,
			applyPatchTransactionArtifactPostimageWitness,
		)
		if err != nil {
			t.Fatal(err)
		}
		witness.Rooted.Identity = nil
		if err := validateApplyPatchTxnRecoveryTargetParticipants(
			transaction,
			operation,
			false,
		); err == nil {
			t.Fatal("prepared target without witness checkpoint was accepted")
		}
	})

	t.Run("target stage invalid name", func(t *testing.T) {
		_, transaction, operation := newApplyPatchTxnZeroMarginAdd(t)
		stage, err := requireApplyPatchTxnArtifact(
			transaction.journal,
			operation.index,
			applyPatchTransactionArtifactPostimageStage,
		)
		if err != nil {
			t.Fatal(err)
		}
		stage.Rooted.Basename = "invalid/name"
		if err := validateApplyPatchTxnRecoveryTargetParticipants(
			transaction,
			operation,
			false,
		); err == nil {
			t.Fatal("invalid staged target name was accepted")
		}
	})

	t.Run("target participant missing", func(t *testing.T) {
		_, transaction, operation := newApplyPatchTxnZeroMarginAdd(t)
		stage, err := requireApplyPatchTxnArtifact(
			transaction.journal,
			operation.index,
			applyPatchTransactionArtifactPostimageStage,
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(operation.targetAnchor.canonical, stage.Rooted.Basename)); err != nil {
			t.Fatal(err)
		}
		if err := validateApplyPatchTxnRecoveryTargetParticipants(
			transaction,
			operation,
			false,
		); err == nil {
			t.Fatal("missing staged target was accepted")
		}
	})

	t.Run("partial target link conflict", func(t *testing.T) {
		fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
		transaction := fixture.begin(t)
		defer transaction.closeHandles()
		initializeApplyPatchTxnZeroMarginEffects(transaction)
		operation := transaction.intent.operations[0]
		stage, err := requireApplyPatchTxnArtifact(
			transaction.journal,
			operation.index,
			applyPatchTransactionArtifactPostimageStage,
		)
		if err != nil {
			t.Fatal(err)
		}
		stagePath := filepath.Join(operation.targetAnchor.canonical, stage.Rooted.Basename)
		if err := os.Link(stagePath, filepath.Join(operation.targetAnchor.canonical, "extra-stage-link")); err != nil {
			t.Fatal(err)
		}
		if err := validateApplyPatchTxnRecoveryTargetParticipants(
			transaction,
			operation,
			false,
		); err == nil {
			t.Fatal("partial target with an extra link was accepted")
		}
	})

	t.Run("published target identity conflict", func(t *testing.T) {
		_, transaction, operation := newApplyPatchTxnZeroMarginAdd(t)
		if err := os.WriteFile(operation.planned.targetPath, []byte("alien\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		transaction.effects.targetPublished[operation.index] = true
		if err := validateApplyPatchTxnRecoveryTargetParticipants(
			transaction,
			operation,
			false,
		); err == nil {
			t.Fatal("alien published target was accepted")
		}
	})
}

func TestApplyPatchTransactionRecoveryZeroMarginClassification(t *testing.T) {
	t.Run("missing operation artifacts", func(t *testing.T) {
		for _, role := range []applyPatchTransactionArtifactRole{
			applyPatchTransactionArtifactPostimageStage,
			applyPatchTransactionArtifactSourceRestoreStage,
			applyPatchTransactionArtifactSourceQuarantine,
			applyPatchTransactionArtifactTargetRollbackQuarantine,
		} {
			t.Run(string(role), func(t *testing.T) {
				fixture := newApplyPatchTxnCrashMoveFixture(t)
				transaction := prepareApplyPatchTxnCrashTransaction(t, fixture)
				defer transaction.closeHandles()
				removeApplyPatchTxnExactArtifact(transaction.journal, role)
				if err := classifyApplyPatchTxnRecovery(transaction); err == nil {
					t.Fatal("recovery classification accepted a missing artifact")
				}
			})
		}
	})

	t.Run("checkpointed quarantine identity conflict", func(t *testing.T) {
		_, transaction, operation := newApplyPatchTxnZeroMarginMove(t)
		quarantine, err := requireApplyPatchTxnArtifact(
			transaction.journal,
			operation.index,
			applyPatchTransactionArtifactSourceQuarantine,
		)
		if err != nil {
			t.Fatal(err)
		}
		quarantine.Rooted.Identity = &applyPatchTxnIdentity{Device: 1, File: 1, Kind: "regular"}
		if err := os.WriteFile(
			filepath.Join(operation.source.anchor.canonical, quarantine.Rooted.Basename),
			[]byte("alien quarantine\n"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		if err := classifyApplyPatchTxnRecovery(transaction); err == nil {
			t.Fatal("recovery classification accepted an alien quarantine")
		}
	})

	t.Run("source state contradiction", func(t *testing.T) {
		_, transaction, _ := newApplyPatchTxnZeroMarginMove(t)
		transaction.journal.Phase = applyPatchTransactionPhaseCommitted
		if err := classifyApplyPatchTxnRecovery(transaction); err == nil {
			t.Fatal("committed recovery accepted an original public source")
		}
	})

	t.Run("missing forest journal", func(t *testing.T) {
		_, transaction, _ := newApplyPatchTxnZeroMarginForest(t)
		transaction.journal.Forests = nil
		if err := classifyApplyPatchTxnRecovery(transaction); err == nil {
			t.Fatal("recovery classification accepted a missing forest journal")
		}
	})

	for _, state := range []string{
		"uncheckpointed unavailable",
		"uncheckpointed absent",
		"uncheckpointed cleanup conflict",
		"public identity conflict",
		"stage identity conflict",
		"rollback identity conflict",
		"rollback ownership conflict",
		"rollback content conflict",
		"rollback invalid name",
		"ambiguous committed stage",
	} {
		t.Run(state, func(t *testing.T) {
			fixture := newApplyPatchTxnRecoveryFixture(t, "nested/deeper/result.txt")
			var transaction *applyPatchPreparedTransaction
			if state == "uncheckpointed absent" || state == "uncheckpointed cleanup conflict" {
				transaction = fixture.begin(t)
			} else {
				transaction = prepareApplyPatchTxnCrashTransaction(t, fixture)
			}
			defer transaction.closeHandles()
			forestIntent := transaction.intent.forests[0]
			forest := &transaction.journal.Forests[0]
			stagePath := filepath.Join(forestIntent.anchorPath, forestIntent.stageRoot)
			publicPath := filepath.Join(forestIntent.anchorPath, forestIntent.publicRoot)
			rollbackPath := filepath.Join(forestIntent.anchorPath, forestIntent.rollbackRoot)
			sentinelPath := filepath.Join(forestIntent.anchorPath, forestIntent.sentinelWitnessName)
			switch state {
			case "uncheckpointed unavailable", "uncheckpointed absent", "uncheckpointed cleanup conflict":
				if err := os.RemoveAll(stagePath); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(sentinelPath); err != nil && !errors.Is(err, os.ErrNotExist) {
					t.Fatal(err)
				}
				forest.StageRoot.Identity = nil
				forest.SentinelWitness.Identity = nil
				if state == "uncheckpointed unavailable" {
					transaction.journal.Phase = applyPatchTransactionPhasePrepared
				}
				if state == "uncheckpointed cleanup conflict" {
					if err := os.Mkdir(publicPath, 0o700); err != nil {
						t.Fatal(err)
					}
				}
			case "public identity conflict":
				if err := os.Rename(stagePath, publicPath); err != nil {
					t.Fatal(err)
				}
				forest.StageRoot.Identity.File++
			case "stage identity conflict":
				forest.StageRoot.Identity.File++
			case "rollback identity conflict":
				if err := os.Rename(stagePath, rollbackPath); err != nil {
					t.Fatal(err)
				}
				wrong := *forest.StageRoot.Identity
				wrong.File++
				forest.RollbackRoot.Identity = &wrong
			case "rollback ownership conflict":
				if err := os.Rename(stagePath, rollbackPath); err != nil {
					t.Fatal(err)
				}
			case "rollback content conflict":
				if err := os.Rename(stagePath, rollbackPath); err != nil {
					t.Fatal(err)
				}
				transaction.journal.Phase = applyPatchTransactionPhaseRollingBack
				entry := forest.Entries[len(forest.Entries)-1]
				if err := os.WriteFile(
					filepath.Join(rollbackPath, filepath.FromSlash(entry.RelativePath)),
					[]byte("drift\n"),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			case "rollback invalid name":
				forestIntent.rollbackRoot = "invalid/name"
			case "ambiguous committed stage":
				transaction.journal.Phase = applyPatchTransactionPhaseCommitted
			}
			err := classifyApplyPatchTxnRecovery(transaction)
			if state == "uncheckpointed absent" {
				if err != nil {
					t.Fatalf("absent uncheckpointed forest = %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("invalid forest recovery state was accepted")
			}
		})
	}
}

func TestApplyPatchTransactionRecoveryZeroMarginLowLevelErrors(t *testing.T) {
	directory := t.TempDir()
	anchor, err := openApplyPatchTxnAnchor(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = anchor.Close() })

	location := applyPatchTransactionJournalRootedLocation{
		AnchorCanonicalPath: directory,
		AnchorIdentity:      anchor.identity,
		Basename:            "invalid/name",
	}
	transaction := &applyPatchPreparedTransaction{
		journal: &applyPatchTransactionJournal{
			Phase: applyPatchTransactionPhasePreparing,
			Artifacts: []applyPatchTransactionJournalArtifact{{
				Role:   applyPatchTransactionArtifactSourceProbeWitness,
				Rooted: &location,
			}},
		},
	}
	if err := validateApplyPatchTxnUncheckpointedRootedArtifacts(transaction); err == nil {
		t.Fatal("invalid uncheckpointed artifact name was accepted")
	}

	transaction.journal.Artifacts[0].Rooted.AnchorCanonicalPath = filepath.Join(directory, "missing")
	transaction.journal.Artifacts[0].Rooted.Basename = "artifact"
	if err := validateApplyPatchTxnUncheckpointedRootedArtifacts(transaction); err == nil {
		t.Fatal("missing uncheckpointed artifact anchor was accepted")
	}
	transaction.journal.Artifacts[0].Rooted.AnchorCanonicalPath = directory
	transaction.journal.Artifacts[0].Rooted.AnchorIdentity = anchor.identity
	transaction.journal.Artifacts[0].Rooted.AnchorIdentity.File++
	if err := validateApplyPatchTxnUncheckpointedRootedArtifacts(transaction); err == nil {
		t.Fatal("changed uncheckpointed artifact anchor was accepted")
	}

	transaction.journal.Artifacts = nil
	transaction.journal.Forests = []applyPatchTransactionJournalForest{{
		StageRoot: applyPatchTransactionJournalRootedLocation{
			AnchorCanonicalPath: directory,
			AnchorIdentity:      anchor.identity,
			Basename:            "invalid/name",
		},
		SentinelWitness: applyPatchTransactionJournalRootedLocation{
			Identity: &applyPatchTxnIdentity{Device: 1, File: 1, Kind: "regular"},
		},
	}}
	if err := validateApplyPatchTxnUncheckpointedRootedArtifacts(transaction); err == nil {
		t.Fatal("invalid uncheckpointed forest name was accepted")
	}
	transaction.journal.Forests[0].StageRoot.AnchorCanonicalPath = filepath.Join(directory, "missing")
	transaction.journal.Forests[0].StageRoot.Basename = "stage"
	if err := validateApplyPatchTxnUncheckpointedRootedArtifacts(transaction); err == nil {
		t.Fatal("missing uncheckpointed forest anchor was accepted")
	}
	transaction.journal.Forests[0].StageRoot.AnchorCanonicalPath = directory
	transaction.journal.Forests[0].StageRoot.AnchorIdentity = anchor.identity
	transaction.journal.Forests[0].StageRoot.AnchorIdentity.File++
	if err := validateApplyPatchTxnUncheckpointedRootedArtifacts(transaction); err == nil {
		t.Fatal("changed uncheckpointed forest anchor was accepted")
	}

	if _, err := inspectApplyPatchTxnRecoveryObject(anchor, "invalid/name", nil); err == nil {
		t.Fatal("invalid recovery object name was accepted")
	}
	if present, err := applyPatchTxnRecoveryIdentityPresent(
		anchor,
		"invalid/name",
		applyPatchTxnIdentity{},
	); present || err == nil {
		t.Fatal("invalid recovery identity name was accepted")
	}

	objectPath := filepath.Join(directory, "quarantine")
	if err := os.WriteFile(objectPath, []byte("quarantine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	operation := &applyPatchTxnIntent{
		index:  0,
		source: &applyPatchTxnEndpoint{anchor: anchor},
	}
	quarantine := &applyPatchTransactionJournalArtifact{
		Rooted: &applyPatchTransactionJournalRootedLocation{Basename: "invalid/name"},
	}
	if _, err := inspectApplyPatchTxnUncheckpointedQuarantine(
		operation,
		quarantine,
		&applyPatchTransactionJournal{},
	); err == nil {
		t.Fatal("invalid uncheckpointed quarantine name was accepted")
	}
	quarantine.Rooted.Basename = "quarantine"
	if _, err := inspectApplyPatchTxnUncheckpointedQuarantine(
		operation,
		quarantine,
		&applyPatchTransactionJournal{},
	); err == nil {
		t.Fatal("uncheckpointed quarantine without a witness was accepted")
	}
}

func TestApplyPatchTransactionRecoveryZeroMarginTargetClassification(t *testing.T) {
	for _, state := range []string{
		"missing stage",
		"stage identity conflict",
		"rollback identity conflict",
		"rollback invalid name",
	} {
		t.Run(state, func(t *testing.T) {
			_, transaction, operation := newApplyPatchTxnZeroMarginAdd(t)
			stage, err := requireApplyPatchTxnArtifact(
				transaction.journal,
				operation.index,
				applyPatchTransactionArtifactPostimageStage,
			)
			if err != nil {
				t.Fatal(err)
			}
			if state == "missing stage" {
				stage = nil
			} else if state == "stage identity conflict" {
				stagePath := filepath.Join(operation.targetAnchor.canonical, stage.Rooted.Basename)
				if err := os.Remove(stagePath); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(stagePath, []byte("alien stage\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				rollback, err := requireApplyPatchTxnArtifact(
					transaction.journal,
					operation.index,
					applyPatchTransactionArtifactTargetRollbackQuarantine,
				)
				if err != nil {
					t.Fatal(err)
				}
				if state == "rollback invalid name" {
					rollback.Rooted.Basename = "invalid/name"
				} else {
					rollback.Rooted.Identity = &applyPatchTxnIdentity{
						Device: 1,
						File:   1,
						Kind:   "regular",
					}
					if err := os.WriteFile(
						filepath.Join(operation.targetAnchor.canonical, rollback.Rooted.Basename),
						[]byte("alien rollback\n"),
						0o600,
					); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := classifyApplyPatchTxnRecoveryTarget(
				transaction,
				operation,
				stage,
				applyPatchTxnRecoveryAbsent,
			); err == nil {
				t.Fatal("invalid recovery target state was accepted")
			}
		})
	}
}

func newApplyPatchTxnZeroMarginMove(
	t *testing.T,
) (*applyPatchTxnRecoveryFixture, *applyPatchPreparedTransaction, *applyPatchTxnIntent) {
	t.Helper()
	fixture := newApplyPatchTxnCrashMoveFixture(t)
	transaction := prepareApplyPatchTxnCrashTransaction(t, fixture)
	t.Cleanup(func() { _ = transaction.closeHandles() })
	initializeApplyPatchTxnZeroMarginEffects(transaction)
	return fixture, transaction, transaction.intent.operations[0]
}

func newApplyPatchTxnZeroMarginAdd(
	t *testing.T,
) (*applyPatchTxnRecoveryFixture, *applyPatchPreparedTransaction, *applyPatchTxnIntent) {
	t.Helper()
	fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
	transaction := prepareApplyPatchTxnCrashTransaction(t, fixture)
	t.Cleanup(func() { _ = transaction.closeHandles() })
	initializeApplyPatchTxnZeroMarginEffects(transaction)
	return fixture, transaction, transaction.intent.operations[0]
}

func newApplyPatchTxnZeroMarginForest(
	t *testing.T,
) (*applyPatchTxnRecoveryFixture, *applyPatchPreparedTransaction, *applyPatchTxnForestIntent) {
	t.Helper()
	fixture := newApplyPatchTxnRecoveryFixture(t, "nested/deeper/result.txt")
	transaction := prepareApplyPatchTxnCrashTransaction(t, fixture)
	t.Cleanup(func() { _ = transaction.closeHandles() })
	initializeApplyPatchTxnZeroMarginEffects(transaction)
	return fixture, transaction, transaction.intent.forests[0]
}

func initializeApplyPatchTxnZeroMarginEffects(transaction *applyPatchPreparedTransaction) {
	transaction.effects = applyPatchTxnEffects{
		sourceQuarantined:         make(map[int]bool),
		sourceRestoreRequired:     make(map[int]bool),
		targetPublished:           make(map[int]bool),
		targetRollbackQuarantined: make(map[int]bool),
		forestPublished:           make(map[string]bool),
		forestRollbackQuarantined: make(map[string]bool),
	}
}

func TestApplyPatchTransactionRecoveryZeroMarginCanceledClassification(t *testing.T) {
	fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	if err := tool.recoverApplyPatchTransaction(
		canceled,
		fixture.state,
		fixture.workspaceState,
		fixture.workspace,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled recovery = %v", err)
	}
}
