package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionStageCloseoutCheckpointFaultMatrix(t *testing.T) {
	patches := []struct {
		name  string
		patch string
		max   int
	}{
		{
			"regular add",
			"*** Begin Patch\n*** Add File: result.txt\n+result\n*** End Patch",
			4,
		},
		{
			"nested forest",
			"*** Begin Patch\n" +
				"*** Add File: nested/deeper/one.txt\n+one\n" +
				"*** Add File: nested/two.txt\n+two\n" +
				"*** End Patch",
			9,
		},
	}
	injected := errors.New("injected staging checkpoint failure")
	for _, patch := range patches {
		for failAt := 1; failAt <= patch.max; failAt++ {
			t.Run(patch.name+" checkpoint "+string(rune('a'+failAt)), func(t *testing.T) {
				intent, journal, _ := newApplyPatchTxnStageCloseoutFixture(t, patch.patch)
				defer intent.Close()
				calls := 0
				err := stageApplyPatchTxnPostimages(
					context.Background(),
					intent,
					journal,
					func(*applyPatchTransactionJournal) error {
						calls++
						if calls == failAt {
							return injected
						}
						return nil
					},
				)
				if calls < failAt {
					return
				}
				if !errors.Is(err, injected) {
					t.Fatalf("checkpoint %d error = %v", failAt, err)
				}
			})
		}
	}
}

func TestApplyPatchTransactionStageCloseoutDefensiveAndDrift(t *testing.T) {
	if err := validateApplyPatchTxnIntentNamesAbsent(nil); err == nil {
		t.Fatal("nil intent names validated")
	}
	if err := stageApplyPatchTxnPostimages(nil, nil, nil, nil); err == nil {
		t.Fatal("nil staging state succeeded")
	}
	intent, journal, _ := newApplyPatchTxnStageCloseoutFixture(
		t,
		"*** Begin Patch\n*** Add File: result.txt\n+result\n*** End Patch",
	)
	defer intent.Close()
	if err := stageApplyPatchTxnPostimages(
		context.Background(), intent, journal, nil,
	); err == nil {
		t.Fatal("nil staging checkpoint succeeded")
	}
	journal.Phase = applyPatchTransactionPhasePrepared
	if err := stageApplyPatchTxnPostimages(
		context.Background(), intent, journal, func(*applyPatchTransactionJournal) error { return nil },
	); err == nil {
		t.Fatal("prepared journal was staged")
	}
	journal.Phase = applyPatchTransactionPhasePreparing
	journal.Operations = nil
	if err := stageApplyPatchTxnPostimages(
		context.Background(), intent, journal, func(*applyPatchTransactionJournal) error { return nil },
	); err == nil {
		t.Fatal("mismatched operations were staged")
	}

	directory := t.TempDir()
	path := filepath.Join(directory, "file.txt")
	if err := os.WriteFile(path, []byte("content\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	anchor, err := openApplyPatchTxnAnchor(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer anchor.Close()
	expected := mapApplyPatchTxnFileState(true, []byte("content\n"), 0o640)
	if _, err := verifyApplyPatchTxnRegular(anchor, "missing", expected, 1); err == nil {
		t.Fatal("missing regular verified")
	}
	wrongMode := expected
	wrongMode.Mode = 0o600
	if _, err := verifyApplyPatchTxnRegular(anchor, "file.txt", wrongMode, 1); err == nil {
		t.Fatal("wrong-mode regular verified")
	}
	wrongDigest := expected
	wrongDigest.SHA256 = string(make([]byte, 64))
	if _, err := verifyApplyPatchTxnRegular(anchor, "file.txt", wrongDigest, 1); err == nil {
		t.Fatal("wrong-content regular verified")
	}
	if _, err := requireApplyPatchTxnArtifact(&applyPatchTransactionJournal{}, 0, "missing"); err == nil {
		t.Fatal("missing artifact returned")
	}
	if _, err := requireApplyPatchTxnJournalForest(&applyPatchTransactionJournal{}, "missing"); err == nil {
		t.Fatal("missing forest returned")
	}
}

func TestApplyPatchTransactionStageCloseoutDeclaredNameFailures(t *testing.T) {
	intent, _, _ := newApplyPatchTxnStageCloseoutFixture(
		t,
		"*** Begin Patch\n*** Add File: result.txt\n+result\n*** End Patch",
	)
	defer intent.Close()
	op := intent.operations[0]
	original := op.stageName
	op.stageName = ""
	if err := validateApplyPatchTxnIntentNamesAbsent(intent); err == nil {
		t.Fatal("blank declared name was accepted")
	}
	op.stageName = original
	op.postWitnessName = original
	if err := validateApplyPatchTxnIntentNamesAbsent(intent); err == nil {
		t.Fatal("duplicate declared name was accepted")
	}
	op.postWitnessName = ".picoclaw-apply-patch-post-witness-11111111111111111111111111111111"
	occupiedPath := filepath.Join(op.targetAnchor.canonical, op.stageName)
	if err := os.WriteFile(occupiedPath, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateApplyPatchTxnIntentNamesAbsent(intent); err == nil {
		t.Fatal("occupied declared name was accepted")
	}
	_ = os.Remove(filepath.Join(op.targetAnchor.canonical, op.stageName))
	deleteIntent, _ := newApplyPatchTxnSourceProbeCloseoutFixture(t)
	defer deleteIntent.Close()
	deleteIntent.operations[0].source.anchor = nil
	if err := validateApplyPatchTxnIntentNamesAbsent(deleteIntent); err == nil {
		t.Fatal("nil declared anchor was accepted")
	}
}

func newApplyPatchTxnStageCloseoutFixture(
	t *testing.T,
	patch string,
) (*applyPatchTxnIntentPlan, *applyPatchTransactionJournal, []byte) {
	t.Helper()
	workspace := t.TempDir()
	plan := buildApplyPatchTxnTestPlan(t, workspace, patch)
	intent, err := buildApplyPatchTxnIntent(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	stateRoot := t.TempDir()
	key, workspaceBinding, stateBinding := applyPatchTxnTestBindings(
		t, plan.workspace, stateRoot, intent,
	)
	journal, err := newApplyPatchTxnPreparingJournal(
		key, workspaceBinding, stateBinding, intent,
	)
	if err != nil {
		_ = intent.Close()
		t.Fatal(err)
	}
	return intent, journal, key
}
