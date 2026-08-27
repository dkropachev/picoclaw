package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionStagesCheckpointedPostimagesWithoutPublishing(t *testing.T) {
	workspacePath := filepath.Join(t.TempDir(), "workspace")
	stateRoot := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(workspacePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(workspacePath, "source.txt")
	if err := os.WriteFile(sourcePath, []byte("before\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	workspaceInfo, err := os.Lstat(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	sourceInfo, err := os.Lstat(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	plan := &applyPatchPlan{
		workspace: applyPatchWorkspace{canonical: workspacePath, info: workspaceInfo},
		ops: []plannedApplyPatchOp{
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
				kind: "add", targetLabel: "root-add.txt",
				targetPath: filepath.Join(workspacePath, "root-add.txt"),
				after:      []byte("root add\n"), mode: 0o644,
			},
			{
				kind: "add", targetLabel: "nested/deeper/add.txt",
				targetPath: filepath.Join(workspacePath, "nested", "deeper", "add.txt"),
				after:      []byte("forest add\n"), mode: 0o644,
			},
		},
	}
	intent, err := buildApplyPatchTxnIntent(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	defer intent.Close()
	key, workspaceBinding, stateBinding := applyPatchTxnTestBindings(
		t,
		plan.workspace,
		stateRoot,
		intent,
	)
	journal, err := newApplyPatchTxnPreparingJournal(
		key,
		workspaceBinding,
		stateBinding,
		intent,
	)
	if err != nil {
		t.Fatal(err)
	}
	nameValidationErr := validateApplyPatchTxnIntentNamesAbsent(intent)
	if nameValidationErr != nil {
		t.Fatal(nameValidationErr)
	}
	checkpoints := 0
	checkpoint := func(current *applyPatchTransactionJournal) error {
		checkpoints++
		encoded, encodeErr := encodeApplyPatchTransactionJournal(key, current)
		if encodeErr != nil {
			return encodeErr
		}
		_, decodeErr := decodeApplyPatchTransactionJournal(key, encoded)
		return decodeErr
	}
	stageErr := stageApplyPatchTxnPostimages(
		context.Background(),
		intent,
		journal,
		checkpoint,
	)
	if stageErr != nil {
		t.Fatalf("stageApplyPatchTxnPostimages() error = %v", stageErr)
	}
	if checkpoints < 10 {
		t.Fatalf("journal checkpoints = %d, want at least 10", checkpoints)
	}
	for _, publicPath := range []string{
		filepath.Join(workspacePath, "root-add.txt"),
		filepath.Join(workspacePath, "nested"),
	} {
		_, statErr := os.Lstat(publicPath)
		if !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("postimage was published before prepared: %q, %v", publicPath, statErr)
		}
	}
	data, err := os.ReadFile(sourcePath)
	if err != nil || string(data) != "before\n" {
		t.Fatalf("source changed during staging: %q, %v", data, err)
	}
	for _, operationIndex := range []int{0, 1} {
		stage, artifactErr := requireApplyPatchTxnArtifact(
			journal,
			operationIndex,
			applyPatchTransactionArtifactPostimageStage,
		)
		if artifactErr != nil || stage.Rooted.Identity == nil {
			t.Fatalf("operation %d stage = %+v, %v", operationIndex, stage, artifactErr)
		}
		witness, artifactErr := requireApplyPatchTxnArtifact(
			journal,
			operationIndex,
			applyPatchTransactionArtifactPostimageWitness,
		)
		if artifactErr != nil || witness.Rooted.Identity == nil ||
			!witness.Rooted.Identity.equal(*stage.Rooted.Identity) {
			t.Fatalf("operation %d witness = %+v, %v", operationIndex, witness, artifactErr)
		}
	}
	if journal.Operations[0].After.Mode != 0o640 ||
		journal.Operations[1].After.Mode&^uint32(0o644) != 0 ||
		journal.Operations[2].After.Mode&^uint32(0o644) != 0 {
		t.Fatalf(
			"staged modes = %#o/%#o/%#o",
			journal.Operations[0].After.Mode,
			journal.Operations[1].After.Mode,
			journal.Operations[2].After.Mode,
		)
	}
	forest := &journal.Forests[0]
	if forest.StageRoot.Identity == nil || forest.SentinelWitness.Identity == nil {
		t.Fatalf("checkpointed forest = %+v", forest)
	}
	for _, entry := range forest.Entries {
		if entry.Identity == nil {
			t.Fatalf("forest entry was not checkpointed: %+v", entry)
		}
	}
	_, finalEncodeErr := encodeApplyPatchTransactionJournal(key, journal)
	if finalEncodeErr != nil {
		t.Fatalf("final preparing journal is invalid: %v", finalEncodeErr)
	}
}

func TestApplyPatchTransactionStagingHonorsPreEffectCancellation(t *testing.T) {
	workspace := t.TempDir()
	workspaceInfo, err := os.Lstat(workspace)
	if err != nil {
		t.Fatal(err)
	}
	plan := &applyPatchPlan{
		workspace: applyPatchWorkspace{canonical: workspace, info: workspaceInfo},
		ops: []plannedApplyPatchOp{{
			kind: "add", targetLabel: "canceled.txt",
			targetPath: filepath.Join(workspace, "canceled.txt"),
			after:      []byte("canceled\n"), mode: 0o644,
		}},
	}
	intent, err := buildApplyPatchTxnIntent(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	defer intent.Close()
	stateRoot := t.TempDir()
	key, workspaceBinding, stateBinding := applyPatchTxnTestBindings(
		t,
		plan.workspace,
		stateRoot,
		intent,
	)
	journal, err := newApplyPatchTxnPreparingJournal(
		key,
		workspaceBinding,
		stateBinding,
		intent,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	checkpointCalls := 0
	err = stageApplyPatchTxnPostimages(
		ctx,
		intent,
		journal,
		func(*applyPatchTransactionJournal) error {
			checkpointCalls++
			return nil
		},
	)
	if !errors.Is(err, context.Canceled) || checkpointCalls != 0 {
		t.Fatalf("canceled staging = %v, checkpoints=%d", err, checkpointCalls)
	}
	entries, err := os.ReadDir(workspace)
	if err != nil || len(entries) != 0 {
		t.Fatalf("canceled staging residue = %#v, %v", entries, err)
	}
}

func applyPatchTxnTestBindings(
	t *testing.T,
	workspace applyPatchWorkspace,
	stateRoot string,
	intent *applyPatchTxnIntentPlan,
) ([]byte, applyPatchTransactionWorkspaceBinding, applyPatchTransactionStateBinding) {
	t.Helper()
	workspaceBinding, err := newApplyPatchTxnWorkspaceBinding(workspace)
	if err != nil {
		t.Fatal(err)
	}
	stateInfo, err := os.Lstat(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	stateIdentity, err := applyPatchTxnIdentityFromFileInfo(stateInfo, "directory")
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, applyPatchTransactionKeyBytes)
	for index := range key {
		key[index] = byte(index + 1)
	}
	stateBinding, err := newApplyPatchTxnStateBinding(
		stateRoot,
		stateIdentity,
		key,
		applyPatchTxnTestWorkspaceDirectory(t, workspaceBinding.Identity),
		intent,
	)
	if err != nil {
		t.Fatal(err)
	}
	return key, workspaceBinding, stateBinding
}

func applyPatchTxnTestWorkspaceDirectory(
	t *testing.T,
	identity applyPatchTxnIdentity,
) string {
	t.Helper()
	digest, err := applyPatchTxnWorkspaceIdentityDigest(identity)
	if err != nil {
		t.Fatal(err)
	}
	return "workspaces/" + digest
}
