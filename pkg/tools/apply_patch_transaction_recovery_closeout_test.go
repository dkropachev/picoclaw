package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionRecoveryCloseoutSourceStateMatrix(t *testing.T) {
	valid := []struct {
		phase      applyPatchTransactionPhase
		kind       string
		public     applyPatchTxnRecoveryObjectState
		quarantine bool
	}{
		{applyPatchTransactionPhasePreparing, "delete", applyPatchTxnRecoveryOriginal, false},
		{applyPatchTransactionPhasePrepared, "delete", applyPatchTxnRecoveryOriginal, false},
		{applyPatchTransactionPhasePrepared, "delete", applyPatchTxnRecoveryAbsent, true},
		{applyPatchTransactionPhasePrepared, "delete", applyPatchTxnRecoveryAbsent, false},
		{applyPatchTransactionPhasePrepared, "update", applyPatchTxnRecoveryPostimage, false},
		{applyPatchTransactionPhaseRollingBack, "delete", applyPatchTxnRecoveryOriginal, false},
		{applyPatchTransactionPhaseRollingBack, "delete", applyPatchTxnRecoveryAbsent, true},
		{applyPatchTransactionPhaseRollingBack, "delete", applyPatchTxnRecoveryAbsent, false},
		{applyPatchTransactionPhaseRollingBack, "delete", applyPatchTxnRecoveryRestored, false},
		{applyPatchTransactionPhaseRollingBack, "update", applyPatchTxnRecoveryPostimage, false},
		{applyPatchTransactionPhaseCommitted, "update", applyPatchTxnRecoveryPostimage, false},
		{applyPatchTransactionPhaseCommitted, "delete", applyPatchTxnRecoveryAbsent, false},
	}
	for index, state := range valid {
		if err := validateApplyPatchTxnRecoverySourceState(
			state.phase, state.kind, state.public, state.quarantine,
		); err != nil {
			t.Fatalf("valid source state %d = %v", index, err)
		}
	}
	invalid := []struct {
		phase  applyPatchTransactionPhase
		kind   string
		public applyPatchTxnRecoveryObjectState
	}{
		{"unknown", "delete", applyPatchTxnRecoveryOriginal},
		{applyPatchTransactionPhasePreparing, "delete", applyPatchTxnRecoveryAbsent},
		{applyPatchTransactionPhaseCommitted, "delete", applyPatchTxnRecoveryOriginal},
		{applyPatchTransactionPhaseCommitted, "update", applyPatchTxnRecoveryAbsent},
	}
	for index, state := range invalid {
		if err := validateApplyPatchTxnRecoverySourceState(
			state.phase, state.kind, state.public, false,
		); err == nil {
			t.Fatalf("invalid source state %d accepted", index)
		}
	}
}

func TestApplyPatchTransactionRecoveryCloseoutBindingsAndAuthorization(t *testing.T) {
	fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
	tx := fixture.begin(t)
	defer tx.abortPreparing()
	if err := validateApplyPatchTxnRecoveryBindings(
		fixture.state,
		fixture.workspaceState,
		fixture.workspace,
		tx.key[:],
		tx.journal,
	); err != nil {
		t.Fatalf("valid recovery binding = %v", err)
	}
	mutations := []func(*applyPatchTransactionJournal){
		func(j *applyPatchTransactionJournal) { j.Workspace.CanonicalPath += "-other" },
		func(j *applyPatchTransactionJournal) { j.Workspace.Identity.File++ },
		func(j *applyPatchTransactionJournal) { j.State.CanonicalRoot += "-other" },
		func(j *applyPatchTransactionJournal) { j.State.RootIdentity.File++ },
		func(j *applyPatchTransactionJournal) { j.State.WorkspaceDirectory = "workspaces/other" },
	}
	for index, mutate := range mutations {
		journal := cloneApplyPatchTransactionJournal(t, tx.journal)
		mutate(journal)
		if err := validateApplyPatchTxnRecoveryBindings(
			fixture.state,
			fixture.workspaceState,
			fixture.workspace,
			tx.key[:],
			journal,
		); err == nil {
			t.Fatalf("invalid recovery binding %d accepted", index)
		}
	}

	denied := errors.New("denied recovery path")
	tool := newApplyPatchTxnRecoveryTool(t, fixture.workspacePath, fixture.stateRoot)
	tool.pathGuard = func(string) error { return denied }
	if err := tool.authorizeApplyPatchTxnRecovery(
		context.Background(), fixture.workspace, tx.journal,
	); err == nil {
		t.Fatalf("guard-denied recovery = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	tool.pathGuard = nil
	if err := tool.authorizeApplyPatchTxnRecovery(
		canceled, fixture.workspace, tx.journal,
	); err == nil {
		t.Fatalf("canceled recovery authorization = %v", err)
	}
}

func TestApplyPatchTransactionRecoveryCloseoutReconstructGuards(t *testing.T) {
	if intent, err := reconstructApplyPatchTxnIntent(nil); intent != nil || err == nil {
		t.Fatalf("nil recovery intent = %#v, %v", intent, err)
	}
	fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
	tx := fixture.begin(t)
	defer tx.abortPreparing()
	if intent, err := reconstructApplyPatchTxnIntent(tx.journal); err != nil {
		t.Fatalf("valid recovery intent = %v", err)
	} else if closeErr := intent.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	mutations := []func(*applyPatchTransactionJournal){
		func(j *applyPatchTransactionJournal) {
			stage, _ := requireApplyPatchTxnArtifact(j, 0, applyPatchTransactionArtifactPostimageStage)
			stage.Rooted.AnchorCanonicalPath = filepath.Join(fixture.workspacePath, "missing")
		},
		func(j *applyPatchTransactionJournal) { j.Operations[0].ForestID = "missing" },
		func(j *applyPatchTransactionJournal) {
			stage, _ := requireApplyPatchTxnArtifact(j, 0, applyPatchTransactionArtifactPostimageStage)
			stage.Rooted.AnchorIdentity.File++
		},
	}
	for index, mutate := range mutations {
		journal := cloneApplyPatchTransactionJournal(t, tx.journal)
		mutate(journal)
		if intent, err := reconstructApplyPatchTxnIntent(journal); intent != nil || err == nil {
			if intent != nil {
				_ = intent.Close()
			}
			t.Fatalf("invalid recovery intent %d = %#v, %v", index, intent, err)
		}
	}
	if _, err := resolveApplyPatchTxnTargetLayoutForRecovery(
		filepath.Join(fixture.workspacePath, "result.txt"), "missing", tx.journal,
	); err == nil {
		t.Fatal("missing recovery forest layout succeeded")
	}
}

func TestApplyPatchTransactionRecoveryCloseoutCommittedResyncFaults(t *testing.T) {
	var nilTransaction *applyPatchPreparedTransaction
	if err := nilTransaction.resyncVisibleCommittedDecision(); !errors.Is(err, errApplyPatchCommitUncertain) {
		t.Fatalf("nil committed resync = %v", err)
	}
	for _, boundary := range []string{
		"committed_recovery_visible_before_sync",
		"committed_recovery_journal_sync",
	} {
		t.Run(boundary, func(t *testing.T) {
			fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
			tx := commitApplyPatchTxnRecoveryFixtureToDecision(t, fixture)
			defer tx.closeHandles()
			injected := errors.New("injected committed recovery resync")
			tx.fault = func(observed string) error {
				if observed == boundary {
					return injected
				}
				return nil
			}
			if err := tx.resyncVisibleCommittedDecision(); !errors.Is(err, errApplyPatchCommitUncertain) ||
				!errors.Is(err, injected) {
				t.Fatalf("committed resync fault = %v", err)
			}
		})
	}
}

func TestApplyPatchTransactionRecoveryCloseoutRootedRemovalReconciliation(t *testing.T) {
	fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
	tx := fixture.begin(t)
	defer tx.abortPreparing()
	stage, err := requireApplyPatchTxnArtifact(
		tx.journal, 0, applyPatchTransactionArtifactPostimageStage,
	)
	if err != nil || stage.Rooted.Identity == nil {
		t.Fatal(errors.Join(err, errors.New("stage unavailable")))
	}
	if err := reconcileApplyPatchTxnRootedRemoval(tx, stage.Rooted, "regular"); err != nil {
		t.Fatalf("ordinary rooted reconciliation = %v", err)
	}
	removalPath := filepath.Join(stage.Rooted.AnchorCanonicalPath, stage.Rooted.RemovalBasename)
	if err := os.WriteFile(removalPath, []byte("alien\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := reconcileApplyPatchTxnRootedRemoval(tx, stage.Rooted, "regular"); err == nil {
		t.Fatal("unexpected removal quarantine reconciled")
	}
	if err := os.Remove(removalPath); err != nil {
		t.Fatal(err)
	}
	stage.Rooted.RemovalAttempted = true
	if err := reconcileApplyPatchTxnRootedRemoval(tx, stage.Rooted, "regular"); err != nil {
		t.Fatalf("absent attempted removal reconciliation = %v", err)
	}
}

func TestApplyPatchTransactionRecoveryCloseoutTargetParticipantStates(t *testing.T) {
	t.Run("prepared staged", func(t *testing.T) {
		fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
		tx := prepareApplyPatchTxnCrashTransaction(t, fixture)
		defer tx.closeHandles()
		initializeApplyPatchTxnCrashEffects(tx)
		if err := validateApplyPatchTxnRecoveryTargetParticipants(
			tx, tx.intent.operations[0], false,
		); err != nil {
			t.Fatalf("prepared staged target = %v", err)
		}
	})
	t.Run("published", func(t *testing.T) {
		fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
		tx := prepareApplyPatchTxnCrashTransaction(t, fixture)
		defer tx.closeHandles()
		initializeApplyPatchTxnCrashEffects(tx)
		if err := tx.publishTargets(); err != nil {
			t.Fatal(err)
		}
		if err := validateApplyPatchTxnRecoveryTargetParticipants(
			tx, tx.intent.operations[0], false,
		); err != nil {
			t.Fatalf("published target = %v", err)
		}
	})
	t.Run("missing witness", func(t *testing.T) {
		fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
		tx := prepareApplyPatchTxnCrashTransaction(t, fixture)
		defer tx.closeHandles()
		initializeApplyPatchTxnCrashEffects(tx)
		operation := tx.intent.operations[0]
		witness, err := requireApplyPatchTxnArtifact(
			tx.journal, 0, applyPatchTransactionArtifactPostimageWitness,
		)
		if err != nil || witness.Rooted.Identity == nil {
			t.Fatal(errors.Join(err, errors.New("witness unavailable")))
		}
		if err := os.Remove(filepath.Join(operation.targetAnchor.canonical, witness.Rooted.Basename)); err != nil {
			t.Fatal(err)
		}
		if err := validateApplyPatchTxnRecoveryTargetParticipants(tx, operation, false); err == nil {
			t.Fatal("missing live target witness was accepted")
		}
	})
	t.Run("witness drift", func(t *testing.T) {
		fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
		tx := prepareApplyPatchTxnCrashTransaction(t, fixture)
		defer tx.closeHandles()
		initializeApplyPatchTxnCrashEffects(tx)
		operation := tx.intent.operations[0]
		witness, _ := requireApplyPatchTxnArtifact(
			tx.journal, 0, applyPatchTransactionArtifactPostimageWitness,
		)
		if err := os.WriteFile(
			filepath.Join(operation.targetAnchor.canonical, witness.Rooted.Basename),
			[]byte("drift\n"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		if err := validateApplyPatchTxnRecoveryTargetParticipants(tx, operation, false); err == nil {
			t.Fatal("drifted target witness was accepted")
		}
	})
}

func TestApplyPatchTransactionRecoveryCloseoutSourceParticipantStates(t *testing.T) {
	t.Run("prepared original", func(t *testing.T) {
		fixture := newApplyPatchTxnCrashMoveFixture(t)
		tx := prepareApplyPatchTxnCrashTransaction(t, fixture)
		defer tx.closeHandles()
		initializeApplyPatchTxnCrashEffects(tx)
		if err := validateApplyPatchTxnRecoverySourceParticipants(
			tx, tx.intent.operations[0], false,
		); err != nil {
			t.Fatalf("prepared original source = %v", err)
		}
	})
	t.Run("quarantined", func(t *testing.T) {
		fixture := newApplyPatchTxnCrashMoveFixture(t)
		tx := prepareApplyPatchTxnCrashTransaction(t, fixture)
		defer tx.closeHandles()
		initializeApplyPatchTxnCrashEffects(tx)
		if err := tx.quarantineSources(); err != nil {
			t.Fatal(err)
		}
		if err := validateApplyPatchTxnRecoverySourceParticipants(
			tx, tx.intent.operations[0], false,
		); err != nil {
			t.Fatalf("quarantined source = %v", err)
		}
	})
	t.Run("source drift", func(t *testing.T) {
		fixture := newApplyPatchTxnCrashMoveFixture(t)
		tx := prepareApplyPatchTxnCrashTransaction(t, fixture)
		defer tx.closeHandles()
		initializeApplyPatchTxnCrashEffects(tx)
		operation := tx.intent.operations[0]
		if err := os.WriteFile(operation.planned.sourcePath, []byte("drift\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := validateApplyPatchTxnRecoverySourceParticipants(tx, operation, false); err == nil {
			t.Fatal("drifted original source was accepted")
		}
	})
	t.Run("quarantine without witness", func(t *testing.T) {
		fixture := newApplyPatchTxnCrashMoveFixture(t)
		tx := prepareApplyPatchTxnCrashTransaction(t, fixture)
		defer tx.closeHandles()
		initializeApplyPatchTxnCrashEffects(tx)
		operation := tx.intent.operations[0]
		if err := tx.quarantineSources(); err != nil {
			t.Fatal(err)
		}
		witness, _ := requireApplyPatchTxnArtifact(
			tx.journal, 0, applyPatchTransactionArtifactSourceWitness,
		)
		if err := os.Remove(filepath.Join(operation.source.anchor.canonical, witness.Rooted.Basename)); err != nil {
			t.Fatal(err)
		}
		if err := validateApplyPatchTxnRecoverySourceParticipants(tx, operation, false); err == nil {
			t.Fatal("quarantined source without witness was accepted")
		}
	})
}
