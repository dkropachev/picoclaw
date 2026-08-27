package tools

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionRecoveryCoverageTopologyComparators(t *testing.T) {
	_, base := validApplyPatchTransactionJournal(t)
	populateApplyPatchTxnRemovalNames(base)
	cloneTopology := func(source *applyPatchTransactionJournal) *applyPatchTransactionJournal {
		clone := cloneApplyPatchTransactionJournal(t, source)
		populateApplyPatchTxnRemovalNames(clone)
		return clone
	}
	if !sameApplyPatchTxnJournalTopology(base, cloneTopology(base)) {
		t.Fatal("identical journal topology differed")
	}
	if sameApplyPatchTxnJournalTopology(nil, base) || sameApplyPatchTxnJournalTopology(base, nil) {
		t.Fatal("nil journal topology matched")
	}
	mutations := []func(*applyPatchTransactionJournal){
		func(j *applyPatchTransactionJournal) { j.Version++ },
		func(j *applyPatchTransactionJournal) { j.Workspace.PathSHA256 = "changed" },
		func(j *applyPatchTransactionJournal) { j.State.ActiveDirectory = "changed" },
		func(j *applyPatchTransactionJournal) { j.TransactionID = "changed" },
		func(j *applyPatchTransactionJournal) { j.OperationCount++ },
		func(j *applyPatchTransactionJournal) { j.Operations = nil },
		func(j *applyPatchTransactionJournal) { j.Artifacts = nil },
		func(j *applyPatchTransactionJournal) {
			j.Forests = append(j.Forests, applyPatchTransactionJournalForest{})
		},
		func(j *applyPatchTransactionJournal) { j.Operations[0].Index++ },
		func(j *applyPatchTransactionJournal) { j.Operations[0].Kind = "delete" },
		func(j *applyPatchTransactionJournal) { j.Operations[0].Before.Mode ^= 1 },
		func(j *applyPatchTransactionJournal) { j.Operations[0].After.Mode ^= 1 },
		func(j *applyPatchTransactionJournal) { j.Operations[0].ForestID = "changed" },
		func(j *applyPatchTransactionJournal) { j.Operations[0].Source.Label = "changed" },
		func(j *applyPatchTransactionJournal) { j.Operations[0].Target = nil },
		func(j *applyPatchTransactionJournal) { j.Artifacts[0].OperationIndex++ },
		func(j *applyPatchTransactionJournal) { j.Artifacts[0].Role = "changed" },
		func(j *applyPatchTransactionJournal) { j.Artifacts[0].StateName = "changed" },
		func(j *applyPatchTransactionJournal) { j.Artifacts[0].Expected.Mode ^= 1 },
		func(j *applyPatchTransactionJournal) { j.Artifacts[0].Backup.HMACSHA256 = "changed" },
		func(j *applyPatchTransactionJournal) { j.Artifacts[1].Rooted.Basename = "changed" },
	}
	for index, mutate := range mutations {
		candidate := cloneTopology(base)
		mutate(candidate)
		if sameApplyPatchTxnJournalTopology(base, candidate) {
			t.Fatalf("journal topology mutation %d matched", index)
		}
	}

	_, forestBase := validApplyPatchTransactionForestJournal(t)
	populateApplyPatchTxnRemovalNames(forestBase)
	forestMutations := []func(*applyPatchTransactionJournal){
		func(j *applyPatchTransactionJournal) { j.Forests[0].ID = "changed" },
		func(j *applyPatchTransactionJournal) { j.Forests[0].OperationIndexes[0]++ },
		func(j *applyPatchTransactionJournal) { j.Forests[0].PublicRoot = "changed" },
		func(j *applyPatchTransactionJournal) { j.Forests[0].SentinelRelativePath = "changed" },
		func(j *applyPatchTransactionJournal) { j.Forests[0].StageRoot.Basename = "changed" },
		func(j *applyPatchTransactionJournal) { j.Forests[0].RollbackRoot.Basename = "changed" },
		func(j *applyPatchTransactionJournal) { j.Forests[0].SentinelWitness.Basename = "changed" },
		func(j *applyPatchTransactionJournal) { j.Forests[0].Entries = nil },
		func(j *applyPatchTransactionJournal) { j.Forests[0].Entries[0].RelativePath = "changed" },
		func(j *applyPatchTransactionJournal) { j.Forests[0].Entries[0].CanonicalPath = "changed" },
		func(j *applyPatchTransactionJournal) { j.Forests[0].Entries[0].Kind = "changed" },
		func(j *applyPatchTransactionJournal) { j.Forests[0].Entries[0].Mode ^= 1 },
		func(j *applyPatchTransactionJournal) { j.Forests[0].Entries[0].Length++ },
		func(j *applyPatchTransactionJournal) { j.Forests[0].Entries[0].SHA256 = "changed" },
		func(j *applyPatchTransactionJournal) { j.Forests[0].Entries[0].RemovalBasename = "changed" },
	}
	for index, mutate := range forestMutations {
		candidate := cloneTopology(forestBase)
		mutate(candidate)
		if sameApplyPatchTxnJournalTopology(forestBase, candidate) {
			t.Fatalf("forest topology mutation %d matched", index)
		}
	}

	identity := &applyPatchTxnIdentity{Device: 1, File: 2, Kind: "regular"}
	otherIdentity := &applyPatchTxnIdentity{Device: 1, File: 3, Kind: "regular"}
	integer, otherInteger := 1, 2
	if !sameApplyPatchTxnJournalEndpoint(nil, nil) ||
		sameApplyPatchTxnJournalEndpoint(nil, &applyPatchTransactionJournalEndpoint{}) ||
		!sameApplyPatchTxnJournalRootedTopology(nil, nil) ||
		sameApplyPatchTxnJournalRootedTopology(nil, &applyPatchTransactionJournalRootedLocation{}) ||
		!sameApplyPatchTxnBackupRecord(nil, nil) ||
		sameApplyPatchTxnBackupRecord(nil, &applyPatchTransactionBackupRecord{}) ||
		!sameApplyPatchTxnOptionalIdentity(nil, nil) ||
		sameApplyPatchTxnOptionalIdentity(identity, otherIdentity) ||
		!sameApplyPatchTxnOptionalInt(nil, nil) ||
		sameApplyPatchTxnOptionalInt(&integer, &otherInteger) {
		t.Fatal("topology helper boundary mismatch")
	}
}

func TestApplyPatchTransactionRecoveryCoveragePointerStagePromotion(t *testing.T) {
	fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
	transaction := fixture.begin(t)
	t.Cleanup(func() {
		_ = transaction.abortPreparing()
		if fixture.workspaceState != nil {
			_ = fixture.workspaceState.Close()
		}
		if fixture.state != nil {
			_ = fixture.state.Close()
		}
	})
	pointer, buildErr := buildApplyPatchTxnPointer(transaction.key[:], transaction.journal)
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	pointerBytes, encodeErr := encodeApplyPatchTransactionPointer(transaction.key[:], pointer)
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	if data, info, resolveErr := resolveApplyPatchTxnRecoveryPointerStage(
		fixture.workspaceState, transaction.key[:], pointerBytes, nil, nil, nil,
	); resolveErr != nil || !bytes.Equal(data, pointerBytes) || info != nil {
		t.Fatalf("absent pointer stage = %q, %#v, %v", data, info, resolveErr)
	}
	if _, _, conflictErr := resolveApplyPatchTxnRecoveryPointerStage(
		fixture.workspaceState,
		transaction.key[:],
		[]byte("invalid pointer"),
		nil,
		pointerBytes,
		nil,
	); conflictErr == nil {
		t.Fatal("invalid incumbent pointer matched staged pointer")
	}

	directoryPath, pathErr := fixture.workspaceState.directoryPath()
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	var stageData []byte
	var stageInfo os.FileInfo
	anchorErr := fixture.workspaceState.withDirectoryAnchor(func(root *os.Root) error {
		if publishErr := publishApplyPatchTransactionPrivateRegular(
			root,
			directoryPath,
			applyPatchTransactionPointerStageFile,
			pointerBytes,
		); publishErr != nil {
			return publishErr
		}
		var readErr error
		stageData, stageInfo, readErr = readApplyPatchTransactionPrivateRegularBounded(
			root,
			applyPatchTransactionPointerStageFile,
			applyPatchTransactionPointerMaxBytes,
		)
		return readErr
	})
	if anchorErr != nil {
		t.Fatal(anchorErr)
	}
	resolved, pointerInfo, promoteErr := resolveApplyPatchTxnRecoveryPointerStage(
		fixture.workspaceState,
		transaction.key[:],
		nil,
		nil,
		stageData,
		stageInfo,
	)
	if promoteErr != nil || !bytes.Equal(resolved, pointerBytes) || pointerInfo == nil {
		t.Fatalf("promoted pointer = %q, %#v, %v", resolved, pointerInfo, promoteErr)
	}
	if removeErr := removeApplyPatchTxnRecoveryPointer(
		fixture.workspaceState,
		pointerInfo,
	); removeErr != nil {
		t.Fatal(removeErr)
	}
}

func TestApplyPatchTransactionRecoveryCoverageUncheckpointedArtifacts(t *testing.T) {
	directory := t.TempDir()
	anchor, openErr := openApplyPatchTxnAnchor(directory)
	if openErr != nil {
		t.Fatal(openErr)
	}
	identity := anchor.identity
	if err := anchor.Close(); err != nil {
		t.Fatal(err)
	}
	location := applyPatchTransactionJournalRootedLocation{
		AnchorCanonicalPath: directory,
		AnchorIdentity:      identity,
		Basename:            ".picoclaw-apply-patch-source-probe-witness-00000000000000000000000000000000",
		RemovalBasename:     ".picoclaw-apply-patch-remove-00000000000000000000000000000000",
	}
	transaction := &applyPatchPreparedTransaction{journal: &applyPatchTransactionJournal{
		Phase: applyPatchTransactionPhasePreparing,
		Artifacts: []applyPatchTransactionJournalArtifact{{
			Role:   applyPatchTransactionArtifactSourceProbeWitness,
			Rooted: &location,
		}},
	}}
	if err := validateApplyPatchTxnUncheckpointedRootedArtifacts(transaction); err != nil {
		t.Fatalf("absent uncheckpointed artifact = %v", err)
	}
	path := filepath.Join(directory, location.Basename)
	if err := os.WriteFile(path, []byte("present\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateApplyPatchTxnUncheckpointedRootedArtifacts(transaction); err == nil {
		t.Fatal("present uncheckpointed artifact was accepted")
	}
	transaction.journal.Phase = applyPatchTransactionPhasePrepared
	transaction.journal.Artifacts[0].Role = applyPatchTransactionArtifactSourceQuarantine
	if err := validateApplyPatchTxnUncheckpointedRootedArtifacts(transaction); err != nil {
		t.Fatalf("deferred source quarantine = %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	transaction.journal.Artifacts[0].Rooted.AnchorCanonicalPath = filepath.Join(directory, "missing")
	if err := validateApplyPatchTxnUncheckpointedRootedArtifacts(transaction); err == nil {
		t.Fatal("missing artifact anchor was accepted")
	}

	transaction.journal.Artifacts = nil
	transaction.journal.Forests = []applyPatchTransactionJournalForest{{
		StageRoot: location,
		SentinelWitness: applyPatchTransactionJournalRootedLocation{
			Identity: &applyPatchTxnIdentity{Device: 1, File: 1, Kind: "regular"},
		},
	}}
	transaction.journal.Forests[0].StageRoot.AnchorCanonicalPath = directory
	if err := validateApplyPatchTxnUncheckpointedRootedArtifacts(transaction); err != nil {
		t.Fatalf("absent uncheckpointed forest = %v", err)
	}
	if err := os.WriteFile(path, []byte("forest\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateApplyPatchTxnUncheckpointedRootedArtifacts(transaction); err == nil {
		t.Fatal("present uncheckpointed forest was accepted")
	}
}

func TestApplyPatchTransactionRecoveryCoverageObjectClassification(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "object.txt")
	if err := os.WriteFile(path, []byte("object\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	anchor, openErr := openApplyPatchTxnAnchor(directory)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer anchor.Close()
	identity, _, identityErr := applyPatchTxnIdentityAt(anchor, "object.txt")
	if identityErr != nil {
		t.Fatal(identityErr)
	}
	state, classifyErr := inspectApplyPatchTxnRecoveryObject(
		anchor,
		"object.txt",
		map[applyPatchTxnRecoveryObjectState]applyPatchTxnIdentity{
			applyPatchTxnRecoveryOriginal: identity,
		},
	)
	if classifyErr != nil || state != applyPatchTxnRecoveryOriginal {
		t.Fatalf("classified object = %q, %v", state, classifyErr)
	}
	if _, err := inspectApplyPatchTxnRecoveryObject(
		anchor,
		"object.txt",
		map[applyPatchTxnRecoveryObjectState]applyPatchTxnIdentity{
			applyPatchTxnRecoveryOriginal: {Device: identity.Device, File: identity.File + 1, Kind: "regular"},
		},
	); err == nil {
		t.Fatal("wrong object identity was classified")
	}
	state, classifyErr = inspectApplyPatchTxnRecoveryObject(anchor, "absent.txt", nil)
	if classifyErr != nil || state != applyPatchTxnRecoveryAbsent {
		t.Fatalf("classified absent object = %q, %v", state, classifyErr)
	}
	if present, err := applyPatchTxnRecoveryIdentityPresent(
		anchor,
		"object.txt",
		identity,
	); err != nil || !present {
		t.Fatalf("identity present = %v, %v", present, err)
	}
	if present, err := applyPatchTxnRecoveryIdentityPresent(
		anchor,
		"absent.txt",
		identity,
	); err != nil || present {
		t.Fatalf("absent identity present = %v, %v", present, err)
	}
}

func TestApplyPatchTransactionRecoveryCoverageDefensiveBoundaries(t *testing.T) {
	tool := &ApplyPatchTool{}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := tool.recoverApplyPatchTransaction(
		canceled,
		nil,
		nil,
		applyPatchWorkspace{},
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled recovery = %v", err)
	}
	if err := tool.recoverApplyPatchTransaction(
		context.Background(),
		nil,
		nil,
		applyPatchWorkspace{},
	); err == nil {
		t.Fatal("nil-state recovery succeeded")
	}
	if err := preflightApplyPatchTxnRecoveryMutation(nil); err == nil {
		t.Fatal("nil recovery preflight succeeded")
	}
	var store *applyPatchTxnStore
	if err := store.finishRecoveryJournalStage(); err != nil {
		t.Fatalf("nil recovery journal stage = %v", err)
	}
	if pointer, err := buildApplyPatchTxnPointer(nil, nil); pointer != nil || err == nil {
		t.Fatalf("nil cleanup pointer = %#v, %v", pointer, err)
	}
	if err := removeApplyPatchTxnRecoveryPointer(nil, nil); err == nil {
		t.Fatal("nil recovery pointer removal succeeded")
	}
}

func TestApplyPatchTransactionRecoveryCoverageVirtualRemovalViews(t *testing.T) {
	directory := t.TempDir()
	anchor, openErr := openApplyPatchTxnAnchor(directory)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer anchor.Close()
	location := &applyPatchTransactionJournalRootedLocation{
		AnchorCanonicalPath: directory,
		AnchorIdentity:      anchor.identity,
		Basename:            "source.txt",
		RemovalBasename:     ".picoclaw-apply-patch-remove-11111111111111111111111111111111",
	}
	if _, _, err := inspectApplyPatchTxnVirtualRootedRemoval(nil, "regular"); err == nil {
		t.Fatal("nil virtual rooted removal was accepted")
	}
	badAnchor := *location
	badAnchor.AnchorCanonicalPath = filepath.Join(directory, "missing")
	if _, _, err := inspectApplyPatchTxnVirtualRootedRemoval(&badAnchor, "regular"); err == nil {
		t.Fatal("missing virtual removal anchor was accepted")
	}
	location.RemovalAttempted = true
	if _, _, err := inspectApplyPatchTxnVirtualRemovalAt(anchor, location, "regular"); err == nil {
		t.Fatal("uncheckpointed removal attempt was accepted")
	}
	location.RemovalAttempted = false
	if name, present, err := inspectApplyPatchTxnVirtualRemovalAt(
		anchor, location, "regular",
	); err != nil || name != location.Basename || present {
		t.Fatalf("absent virtual removal = %q, %v, %v", name, present, err)
	}
	if err := os.WriteFile(
		filepath.Join(directory, location.RemovalBasename),
		[]byte("alien\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, _, err := inspectApplyPatchTxnVirtualRemovalAt(anchor, location, "regular"); err == nil {
		t.Fatal("unexpected uncheckpointed removal quarantine was accepted")
	}
	if err := os.Remove(filepath.Join(directory, location.RemovalBasename)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, location.Basename), []byte("owned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, _, identityErr := applyPatchTxnIdentityAt(anchor, location.Basename)
	if identityErr != nil {
		t.Fatal(identityErr)
	}
	location.Identity = &identity
	if name, present, err := inspectApplyPatchTxnVirtualRootedRemoval(
		location, "regular",
	); err != nil || name != location.Basename || !present {
		t.Fatalf("present virtual source = %q, %v, %v", name, present, err)
	}
	if err := os.Link(
		filepath.Join(directory, location.Basename),
		filepath.Join(directory, location.RemovalBasename),
	); err != nil {
		t.Fatal(err)
	}
	if _, _, err := inspectApplyPatchTxnVirtualRemovalAt(anchor, location, "regular"); err == nil {
		t.Fatal("unexpected checkpointed removal quarantine was accepted")
	}
	location.RemovalAttempted = true
	if _, _, err := inspectApplyPatchTxnVirtualRemovalAt(anchor, location, "regular"); err == nil {
		t.Fatal("ambiguous attempted removal was accepted")
	}
	if err := os.Remove(filepath.Join(directory, location.Basename)); err != nil {
		t.Fatal(err)
	}
	if name, present, err := inspectApplyPatchTxnVirtualRemovalAt(
		anchor, location, "regular",
	); err != nil || name != location.RemovalBasename || !present {
		t.Fatalf("virtual removal quarantine = %q, %v, %v", name, present, err)
	}
	if err := os.Remove(filepath.Join(directory, location.RemovalBasename)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, location.Basename), []byte("alien\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := inspectApplyPatchTxnVirtualRemovalAt(anchor, location, "regular"); err == nil {
		t.Fatal("wrong virtual removal identity was accepted")
	}
}

func TestApplyPatchTransactionRecoveryCoverageVirtualForestRootSelection(t *testing.T) {
	directory := t.TempDir()
	anchor, openErr := openApplyPatchTxnAnchor(directory)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer anchor.Close()
	intent := &applyPatchTxnForestIntent{
		anchor:       anchor,
		stageRoot:    "stage",
		publicRoot:   "public",
		rollbackRoot: "rollback",
	}
	forest := &applyPatchTransactionJournalForest{}
	if root, err := selectApplyPatchTxnVirtualForestRoot(intent, forest); err != nil || root != "" {
		t.Fatalf("empty virtual forest root = %q, %v", root, err)
	}
	identity, mkdirErr := applyPatchTxnMkdir(anchor, intent.stageRoot, 0o700)
	if mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	forest.StageRoot.Identity = &identity
	if root, err := selectApplyPatchTxnVirtualForestRoot(intent, forest); err != nil || root != intent.stageRoot {
		t.Fatalf("selected virtual forest root = %q, %v", root, err)
	}
	wrong := identity
	wrong.File++
	forest.StageRoot.Identity = &wrong
	if _, err := selectApplyPatchTxnVirtualForestRoot(intent, forest); err == nil {
		t.Fatal("wrong virtual forest identity was accepted")
	}
}
