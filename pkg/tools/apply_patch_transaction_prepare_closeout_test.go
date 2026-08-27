package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionPrepareCloseoutPlanAndOperationFailures(t *testing.T) {
	var nilIntent *applyPatchTxnIntentPlan
	if err := nilIntent.Close(); err != nil {
		t.Fatalf("nil intent close = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if intent, err := buildApplyPatchTxnIntent(
		canceled,
		&applyPatchPlan{ops: []plannedApplyPatchOp{{}}},
	); intent != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled intent = %#v, %v", intent, err)
	}
	for _, plan := range []*applyPatchPlan{
		nil,
		{},
		{ops: make([]plannedApplyPatchOp, applyPatchCandidateMaxOperations+1)},
	} {
		if intent, err := buildApplyPatchTxnIntent(context.Background(), plan); intent != nil || err == nil {
			t.Fatalf("invalid intent plan = %#v, %v", intent, err)
		}
	}

	workspace := t.TempDir()
	writeApplyPatchFixture(t, workspace, "source.txt", "before\n", 0o640)
	plan := buildApplyPatchTxnTestPlan(
		t,
		workspace,
		"*** Begin Patch\n*** Delete File: source.txt\n*** End Patch",
	)
	if err := os.WriteFile(filepath.Join(workspace, "source.txt"), []byte("drift\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if operation, err := buildApplyPatchTxnOperationIntent(
		context.Background(), 0, plan.ops[0],
	); operation != nil || err == nil {
		t.Fatalf("drifted operation = %#v, %v", operation, err)
	}

	target := filepath.Join(workspace, "existing.txt")
	if err := os.WriteFile(target, []byte("occupied\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if operation, err := buildApplyPatchTxnOperationIntent(
		context.Background(),
		0,
		plannedApplyPatchOp{
			kind: "add", targetLabel: "existing.txt", targetPath: target,
			after: []byte("new\n"), mode: 0o644,
		},
	); operation != nil || err == nil {
		t.Fatalf("occupied target operation = %#v, %v", operation, err)
	}
	if operation, err := buildApplyPatchTxnOperationIntent(
		context.Background(),
		0,
		plannedApplyPatchOp{kind: "add", targetPath: "relative"},
	); operation != nil || err == nil {
		t.Fatalf("relative target operation = %#v, %v", operation, err)
	}
}

func TestApplyPatchTransactionPrepareCloseoutForestAndLayoutFailures(t *testing.T) {
	if forest, err := buildApplyPatchTxnForestIntent(applyPatchTxnTargetLayout{}); forest != nil || err == nil {
		t.Fatalf("empty forest = %#v, %v", forest, err)
	}
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "occupied"), []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if forest, err := buildApplyPatchTxnForestIntent(applyPatchTxnTargetLayout{
		anchorPath: workspace,
		components: []string{"occupied", "child"},
	}); forest != nil || err == nil {
		t.Fatalf("occupied forest = %#v, %v", forest, err)
	}
	if forest, err := buildApplyPatchTxnForestIntent(applyPatchTxnTargetLayout{
		anchorPath: filepath.Join(workspace, "missing-anchor"),
		components: []string{"root", "child"},
	}); forest != nil || err == nil {
		t.Fatalf("missing-anchor forest = %#v, %v", forest, err)
	}

	for _, target := range []string{"", "relative", filepath.Join(workspace, "bad\x00target")} {
		if layout, err := resolveApplyPatchTxnTargetLayout(target); layout.anchorPath != "" || err == nil {
			t.Fatalf("invalid layout %q = %#v, %v", target, layout, err)
		}
	}
	fileAncestor := filepath.Join(workspace, "file-parent")
	if err := os.WriteFile(fileAncestor, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveApplyPatchTxnTargetLayout(filepath.Join(fileAncestor, "child")); err == nil {
		t.Fatal("file ancestor layout succeeded")
	}
	symlinkAncestor := filepath.Join(workspace, "link-parent")
	if err := os.Symlink(workspace, symlinkAncestor); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveApplyPatchTxnTargetLayout(filepath.Join(symlinkAncestor, "child")); err == nil {
		t.Fatal("symlink ancestor layout succeeded")
	}
	for _, layout := range []applyPatchTxnTargetLayout{
		{},
		{anchorPath: workspace},
		{anchorPath: workspace, components: []string{"bad/name"}},
	} {
		if _, err := applyPatchTxnLayoutGroupKey(layout); err == nil {
			t.Fatalf("invalid group layout succeeded: %#v", layout)
		}
	}
	if endpoint, err := openApplyPatchTxnExistingRegular("relative", nil); endpoint != nil || err == nil {
		t.Fatalf("relative endpoint = %#v, %v", endpoint, err)
	}
	if endpoint, err := openApplyPatchTxnExistingRegular(
		filepath.Join(workspace, "missing"), nil,
	); endpoint != nil || err == nil {
		t.Fatalf("nil-info endpoint = %#v, %v", endpoint, err)
	}
	if name, err := newApplyPatchTxnPrivateName(""); name != "" || err == nil {
		t.Fatalf("blank private name = %q, %v", name, err)
	}
	if name, err := newApplyPatchTxnPrivateName("bad/name"); name != "" || err == nil {
		t.Fatalf("invalid private name = %q, %v", name, err)
	}
}

func TestApplyPatchTransactionPrepareCloseoutMappingGuards(t *testing.T) {
	key := make([]byte, applyPatchTransactionKeyBytes)
	if journal, err := newApplyPatchTxnPreparingJournal(
		key,
		applyPatchTransactionWorkspaceBinding{},
		applyPatchTransactionStateBinding{},
		nil,
	); journal != nil || err == nil {
		t.Fatalf("nil mapped journal = %#v, %v", journal, err)
	}
	if _, err := newApplyPatchTxnStateBinding("", applyPatchTxnIdentity{}, key, "", nil); err == nil {
		t.Fatal("nil intent state binding succeeded")
	}
	if _, err := mapApplyPatchTxnJournalOperation(&applyPatchTxnIntent{
		planned: plannedApplyPatchOp{kind: "update", source: &applyPatchFileSnapshot{}},
	}); err == nil {
		t.Fatal("missing source intent mapped")
	}
	if _, err := mapApplyPatchTxnJournalForest(nil, nil); err == nil {
		t.Fatal("nil forest intent mapped")
	}
}
