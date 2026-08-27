package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestApplyPatchTransactionPersistForwardDecisionStateClassifiesReplacement(t *testing.T) {
	t.Run("prepared reread rolls back", func(t *testing.T) {
		fixture, transaction := newApplyPatchTxnDecisionTestTransaction(t)
		transaction.journal.DecisionAttempted = true
		injected := errors.New("forward journal replacement stopped before rename")
		transaction.fault = func(boundary string) error {
			if boundary == "journal_replace_before_rename" {
				return injected
			}
			return nil
		}
		persistErr := transaction.persistForwardDecisionState()
		if !errors.Is(persistErr, injected) ||
			errors.Is(persistErr, errApplyPatchCommitUncertain) {
			t.Fatalf("prepared forward reread error = %v", persistErr)
		}
		persisted := readApplyPatchTxnDecisionTestJournal(t, transaction)
		if persisted.Phase != applyPatchTransactionPhasePrepared ||
			persisted.DecisionAttempted {
			t.Fatalf("persisted forward journal = phase:%q attempted:%t",
				persisted.Phase, persisted.DecisionAttempted)
		}
		transaction.fault = nil
		rollbackErr := transaction.rollback(persistErr)
		if !errors.Is(rollbackErr, injected) ||
			errors.Is(rollbackErr, errApplyPatchRollbackIncomplete) {
			t.Fatalf("ordinary forward rollback error = %v", rollbackErr)
		}
		fixture.simulateCrash(t, transaction)
		assertNoApplyPatchTxnWorkspaceResidue(t, fixture.workspacePath)
	})

	t.Run("visible prepared marker resyncs", func(t *testing.T) {
		fixture, transaction := newApplyPatchTxnDecisionTestTransaction(t)
		transaction.journal.DecisionAttempted = true
		visibleErr := errors.New("forward journal visible before sync")
		transaction.fault = applyPatchTxnDecisionVisibleFault(visibleErr, nil)
		if err := transaction.persistForwardDecisionState(); err != nil {
			t.Fatalf("visible forward decision error = %v", err)
		}
		persisted := readApplyPatchTxnDecisionTestJournal(t, transaction)
		if persisted.Phase != applyPatchTransactionPhasePrepared ||
			!persisted.DecisionAttempted {
			t.Fatalf("visible forward journal = phase:%q attempted:%t",
				persisted.Phase, persisted.DecisionAttempted)
		}
		fixture.simulateCrash(t, transaction)
	})

	for _, testCase := range []struct {
		name       string
		mismatch   bool
		resyncFail bool
	}{
		{name: "transaction mismatch", mismatch: true},
		{name: "persistent resync failure", resyncFail: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture, transaction := newApplyPatchTxnDecisionTestTransaction(t)
			transaction.journal.DecisionAttempted = true
			visibleErr := errors.New("forward journal visible before sync")
			resyncErr := errors.New("forward journal resync failed")
			transaction.fault = func(boundary string) error {
				switch boundary {
				case "journal_replace_visible_before_sync":
					if testCase.mismatch {
						transaction.journal.TransactionID = "ffffffffffffffffffffffffffffffff"
					}
					return visibleErr
				case "journal_decision_resync":
					if testCase.resyncFail {
						return resyncErr
					}
				}
				return nil
			}
			persistErr := transaction.persistForwardDecisionState()
			if !errors.Is(persistErr, errApplyPatchCommitUncertain) ||
				!errors.Is(persistErr, visibleErr) {
				t.Fatalf("uncertain forward decision error = %v", persistErr)
			}
			if testCase.resyncFail && !errors.Is(persistErr, resyncErr) {
				t.Fatalf("forward resync error = %v", persistErr)
			}
			fixture.simulateCrash(t, transaction)
		})
	}
}

func TestApplyPatchTransactionPersistCommittedDecisionClassifiesReplacement(t *testing.T) {
	t.Run("prepared reread rolls back", func(t *testing.T) {
		fixture, transaction := newApplyPatchTxnCommittedDecisionTestTransaction(t)
		injected := errors.New("committed journal replacement stopped before rename")
		transaction.fault = func(boundary string) error {
			if boundary == "journal_replace_before_rename" {
				return injected
			}
			return nil
		}
		persistErr := transaction.persistCommittedDecision()
		if !errors.Is(persistErr, injected) ||
			errors.Is(persistErr, errApplyPatchCommitUncertain) ||
			transaction.journal.Phase != applyPatchTransactionPhasePrepared {
			t.Fatalf("prepared committed reread error = %v, phase=%q",
				persistErr, transaction.journal.Phase)
		}
		persisted := readApplyPatchTxnDecisionTestJournal(t, transaction)
		if persisted.Phase != applyPatchTransactionPhasePrepared ||
			!persisted.DecisionAttempted {
			t.Fatalf("persisted pre-commit journal = phase:%q attempted:%t",
				persisted.Phase, persisted.DecisionAttempted)
		}
		transaction.fault = nil
		rollbackErr := transaction.rollback(persistErr)
		if !errors.Is(rollbackErr, injected) ||
			errors.Is(rollbackErr, errApplyPatchRollbackIncomplete) {
			t.Fatalf("ordinary committed-marker rollback error = %v", rollbackErr)
		}
		fixture.simulateCrash(t, transaction)
		assertNoApplyPatchTxnWorkspaceResidue(t, fixture.workspacePath)
	})

	t.Run("visible committed marker resyncs", func(t *testing.T) {
		fixture, transaction := newApplyPatchTxnCommittedDecisionTestTransaction(t)
		visibleErr := errors.New("committed journal visible before sync")
		transaction.fault = applyPatchTxnDecisionVisibleFault(visibleErr, nil)
		if err := transaction.persistCommittedDecision(); err != nil {
			t.Fatalf("visible committed decision error = %v", err)
		}
		persisted := readApplyPatchTxnDecisionTestJournal(t, transaction)
		if persisted.Phase != applyPatchTransactionPhaseCommitted ||
			!persisted.DecisionAttempted {
			t.Fatalf("visible committed journal = phase:%q attempted:%t",
				persisted.Phase, persisted.DecisionAttempted)
		}
		fixture.simulateCrash(t, transaction)
	})

	for _, testCase := range []struct {
		name       string
		mismatch   bool
		resyncFail bool
	}{
		{name: "transaction mismatch", mismatch: true},
		{name: "persistent resync failure", resyncFail: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture, transaction := newApplyPatchTxnCommittedDecisionTestTransaction(t)
			visibleErr := errors.New("committed journal visible before sync")
			resyncErr := errors.New("committed journal resync failed")
			transaction.fault = func(boundary string) error {
				switch boundary {
				case "journal_replace_visible_before_sync":
					if testCase.mismatch {
						transaction.journal.TransactionID = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
					}
					return visibleErr
				case "journal_decision_resync":
					if testCase.resyncFail {
						return resyncErr
					}
				}
				return nil
			}
			persistErr := transaction.persistCommittedDecision()
			if !errors.Is(persistErr, errApplyPatchCommitUncertain) ||
				!errors.Is(persistErr, visibleErr) {
				t.Fatalf("uncertain committed decision error = %v", persistErr)
			}
			if testCase.resyncFail && !errors.Is(persistErr, resyncErr) {
				t.Fatalf("committed resync error = %v", persistErr)
			}
			fixture.simulateCrash(t, transaction)
		})
	}
}

func TestApplyPatchTransactionDecisionResyncRejectsNamedStateMutation(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(string, string) error
	}{
		{
			name: "journal bytes replaced",
			mutate: func(_ string, journalPath string) error {
				return os.WriteFile(journalPath, []byte("alien journal"), 0o600)
			},
		},
		{
			name: "active directory moved",
			mutate: func(activePath string, _ string) error {
				if runtime.GOOS == "windows" {
					return errors.ErrUnsupported
				}
				return os.Rename(activePath, activePath+"-moved")
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			transactionFixture, transaction := newApplyPatchTxnCommittedDecisionTestTransaction(t)
			workspaceStatePath, err := transaction.store.workspace.directoryPath()
			if err != nil {
				t.Fatal(err)
			}
			activePath := filepath.Join(workspaceStatePath, transaction.store.activeName)
			journalPath := filepath.Join(activePath, applyPatchTransactionJournalFile)
			visibleErr := errors.New("committed journal visible before sync")
			var mutationErr error
			transaction.fault = func(boundary string) error {
				switch boundary {
				case "journal_replace_visible_before_sync":
					return visibleErr
				case "journal_decision_resync":
					mutationErr = testCase.mutate(activePath, journalPath)
					return nil
				default:
					return nil
				}
			}
			persistErr := transaction.persistCommittedDecision()
			if errors.Is(mutationErr, errors.ErrUnsupported) {
				transactionFixture.simulateCrash(t, transaction)
				t.Skip("moving an open active directory is unsupported on Windows")
			}
			if mutationErr != nil {
				t.Fatalf("state mutation error = %v", mutationErr)
			}
			if !errors.Is(persistErr, errApplyPatchCommitUncertain) ||
				!errors.Is(persistErr, visibleErr) {
				t.Fatalf("mutated decision resync = %v", persistErr)
			}
			transactionFixture.simulateCrash(t, transaction)
		})
	}
}

func newApplyPatchTxnDecisionTestTransaction(
	t *testing.T,
) (*applyPatchTxnRecoveryFixture, *applyPatchPreparedTransaction) {
	t.Helper()
	fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
	transaction := fixture.begin(t)
	if err := transaction.revalidate(context.Background(), fixture.plan); err != nil {
		t.Fatal(err)
	}
	if err := transaction.markPrepared(context.Background()); err != nil {
		t.Fatal(err)
	}
	transaction.effects = applyPatchTxnEffects{
		sourceQuarantined:         make(map[int]bool),
		sourceRestoreRequired:     make(map[int]bool),
		targetPublished:           make(map[int]bool),
		targetRollbackQuarantined: make(map[int]bool),
		forestPublished:           make(map[string]bool),
		forestRollbackQuarantined: make(map[string]bool),
	}
	return fixture, transaction
}

func newApplyPatchTxnCommittedDecisionTestTransaction(
	t *testing.T,
) (*applyPatchTxnRecoveryFixture, *applyPatchPreparedTransaction) {
	t.Helper()
	fixture, transaction := newApplyPatchTxnDecisionTestTransaction(t)
	transaction.journal.DecisionAttempted = true
	if err := transaction.persistForwardDecisionState(); err != nil {
		t.Fatal(err)
	}
	transaction.journal.Phase = applyPatchTransactionPhaseCommitted
	return fixture, transaction
}

func readApplyPatchTxnDecisionTestJournal(
	t *testing.T,
	transaction *applyPatchPreparedTransaction,
) *applyPatchTransactionJournal {
	t.Helper()
	persisted, _, err := transaction.store.readJournal(transaction.key[:])
	if err != nil {
		t.Fatal(err)
	}
	return persisted
}

func applyPatchTxnDecisionVisibleFault(
	visibleErr error,
	resyncErr error,
) func(string) error {
	return func(boundary string) error {
		switch boundary {
		case "journal_replace_visible_before_sync":
			return visibleErr
		case "journal_decision_resync":
			return resyncErr
		default:
			return nil
		}
	}
}
