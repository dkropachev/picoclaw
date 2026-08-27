package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionRecoveryVirtualZeroMarginArtifacts(t *testing.T) {
	t.Run("preparing exclusive stage", func(t *testing.T) {
		fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
		transaction := fixture.begin(t)
		defer transaction.closeHandles()
		if err := validateApplyPatchTxnVirtualRootedArtifacts(transaction); err != nil {
			t.Fatalf("exclusive preparing stage = %v", err)
		}
	})

	t.Run("changed anchor", func(t *testing.T) {
		fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
		transaction := fixture.begin(t)
		defer transaction.closeHandles()
		stage, err := requireApplyPatchTxnArtifact(
			transaction.journal,
			0,
			applyPatchTransactionArtifactPostimageStage,
		)
		if err != nil {
			t.Fatal(err)
		}
		stage.Rooted.AnchorIdentity.File++
		if err := validateApplyPatchTxnVirtualRootedArtifacts(transaction); err == nil {
			t.Fatal("virtual artifact accepted a changed anchor")
		}
	})

	t.Run("restore backup unavailable", func(t *testing.T) {
		_, transaction, operation := newApplyPatchTxnZeroMarginAdd(t)
		stage, err := requireApplyPatchTxnArtifact(
			transaction.journal,
			operation.index,
			applyPatchTransactionArtifactPostimageStage,
		)
		if err != nil {
			t.Fatal(err)
		}
		invalid := *stage
		invalid.OperationIndex = len(transaction.journal.Operations) + 1
		if err := validateApplyPatchTxnVirtualRestoreStage(
			transaction,
			operation.targetAnchor,
			&invalid,
			1,
		); err == nil {
			t.Fatal("restore validation accepted an unavailable backup")
		}
	})
}

func TestApplyPatchTransactionRecoveryVirtualZeroMarginAliases(t *testing.T) {
	directory := t.TempDir()
	anchor, err := openApplyPatchTxnAnchor(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = anchor.Close() })

	t.Run("source inspection error", func(t *testing.T) {
		intent := &applyPatchTxnIntentPlan{
			operations: []*applyPatchTxnIntent{{
				source: &applyPatchTxnEndpoint{
					anchor:   anchor,
					basename: "invalid/name",
				},
				planned: plannedApplyPatchOp{
					sourcePath: filepath.Join(directory, "source"),
				},
			}},
		}
		if _, err := collectApplyPatchTxnVirtualRegularAliases(
			intent,
			&applyPatchTransactionJournal{},
		); err == nil {
			t.Fatal("alias collection accepted an invalid source name")
		}
	})

	t.Run("target inspection error", func(t *testing.T) {
		intent := &applyPatchTxnIntentPlan{
			operations: []*applyPatchTxnIntent{{
				targetAnchor: anchor,
				targetLayout: applyPatchTxnTargetLayout{
					components: []string{"invalid/name"},
				},
				planned: plannedApplyPatchOp{
					targetPath: filepath.Join(directory, "target"),
				},
			}},
		}
		if _, err := collectApplyPatchTxnVirtualRegularAliases(
			intent,
			&applyPatchTransactionJournal{},
		); err == nil {
			t.Fatal("alias collection accepted an invalid target name")
		}
	})

	t.Run("rooted removal anchor changed", func(t *testing.T) {
		location := &applyPatchTransactionJournalRootedLocation{
			AnchorCanonicalPath: directory,
			AnchorIdentity:      anchor.identity,
			Basename:            "participant",
		}
		location.AnchorIdentity.File++
		if _, _, err := inspectApplyPatchTxnVirtualRootedRemoval(
			location,
			"regular",
		); err == nil {
			t.Fatal("rooted removal accepted a changed anchor")
		}
	})
}

func TestApplyPatchTransactionRecoveryVirtualZeroMarginEntryNames(t *testing.T) {
	t.Run("forest root identity conflict", func(t *testing.T) {
		fixture := newApplyPatchTxnRecoveryFixture(t, "nested/deeper/result.txt")
		transaction := fixture.begin(t)
		defer transaction.closeHandles()
		transaction.journal.Forests[0].StageRoot.Identity.File++
		if err := applyApplyPatchTxnVirtualForestEntryNames(
			transaction.intent,
			transaction.journal,
		); err == nil {
			t.Fatal("virtual entry naming accepted a changed forest root")
		}
	})

	t.Run("removal name invalid", func(t *testing.T) {
		fixture := newApplyPatchTxnRecoveryFixture(t, "nested/deeper/result.txt")
		transaction := fixture.begin(t)
		defer transaction.closeHandles()
		forest := &transaction.journal.Forests[0]
		entry := &forest.Entries[len(forest.Entries)-1]
		entry.RemovalBasename = "invalid/name"
		if err := applyApplyPatchTxnVirtualForestEntryNames(
			transaction.intent,
			transaction.journal,
		); err == nil {
			t.Fatal("virtual entry naming accepted an invalid removal name")
		}
	})
}

func TestApplyPatchTransactionRecoveryVirtualZeroMarginPreparing(t *testing.T) {
	for _, state := range []string{
		"journal missing",
		"checkpoint missing",
		"root identity conflict",
		"root absent",
		"entry parent absent",
		"entry identity conflict",
		"witness checkpoint missing",
	} {
		t.Run(state, func(t *testing.T) {
			fixture := newApplyPatchTxnRecoveryFixture(t, "nested/deeper/result.txt")
			transaction := fixture.begin(t)
			defer transaction.closeHandles()
			forestIntent := transaction.intent.forests[0]
			forest := &transaction.journal.Forests[0]
			switch state {
			case "journal missing":
				transaction.journal.Forests = nil
			case "checkpoint missing":
				forest.StageRoot.Identity = nil
			case "root identity conflict":
				forest.StageRoot.Identity.File++
			case "root absent":
				if err := os.RemoveAll(filepath.Join(
					forestIntent.anchorPath,
					forestIntent.stageRoot,
				)); err != nil {
					t.Fatal(err)
				}
			case "entry parent absent":
				entry := forest.Entries[len(forest.Entries)-1]
				parent := filepath.Join(
					forestIntent.anchorPath,
					forestIntent.stageRoot,
					filepath.Dir(filepath.FromSlash(entry.RelativePath)),
				)
				if err := os.RemoveAll(parent); err != nil {
					t.Fatal(err)
				}
			case "entry identity conflict":
				entry := &forest.Entries[len(forest.Entries)-1]
				entry.Identity.File++
			case "witness checkpoint missing":
				forest.SentinelWitness.Identity = nil
			}
			err := validateApplyPatchTxnPreparingVirtualForests(transaction)
			allowedPartial := state == "checkpoint missing" || state == "root absent"
			if allowedPartial && err != nil {
				t.Fatalf("valid partial preparing forest = %v", err)
			}
			if !allowedPartial && err == nil {
				t.Fatal("invalid partial preparing forest was accepted")
			}
		})
	}
}

func TestApplyPatchTransactionRecoveryVirtualZeroMarginRootSelection(t *testing.T) {
	fixture := newApplyPatchTxnRecoveryFixture(t, "nested/deeper/result.txt")
	transaction := fixture.begin(t)
	defer transaction.closeHandles()
	forestIntent := transaction.intent.forests[0]
	forestIntent.publicRoot = forestIntent.stageRoot
	if _, err := selectApplyPatchTxnVirtualForestRoot(
		forestIntent,
		&transaction.journal.Forests[0],
	); err == nil {
		t.Fatal("ambiguous virtual forest root was accepted")
	}
}
