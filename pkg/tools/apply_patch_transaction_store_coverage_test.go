package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyPatchTransactionStoreCoverageGuards(t *testing.T) {
	var nilStore *applyPatchTxnStore
	if err := nilStore.prepareCommittedCleanup(nil, nil); err == nil {
		t.Fatal("nil committed cleanup state succeeded")
	}
	if err := nilStore.preparePrivateCleanup(nil, nil); err == nil {
		t.Fatal("nil private cleanup state succeeded")
	}
	if _, err := applyPatchTxnCleanupJournalDigest(nil, nil); err == nil {
		t.Fatal("nil cleanup journal digest succeeded")
	}
	if err := nilStore.cleanupOwnedStateAuthenticated(nil, nil); err == nil {
		t.Fatal("nil authenticated cleanup succeeded")
	}
	if err := nilStore.finishCommittedStateCleanup(); err != nil {
		t.Fatalf("nil committed-state cleanup = %v", err)
	}
	if store, err := createApplyPatchTxnStore(nil, nil); err == nil || store != nil {
		t.Fatalf("nil store construction = %#v, %v", store, err)
	}
	if err := cleanupApplyPatchTxnJournalLessActiveDirectory(nil, "active", nil); err != nil {
		t.Fatalf("absent journal-less cleanup = %v", err)
	}
	if err := requireApplyPatchTxnWorkspaceReadyForNewTransaction(nil); err == nil {
		t.Fatal("nil workspace readiness succeeded")
	}
	if err := nilStore.Close(); err != nil {
		t.Fatalf("nil store close = %v", err)
	}
	if err := (&applyPatchTxnStore{}).Close(); err != nil {
		t.Fatalf("empty store close = %v", err)
	}
	if err := nilStore.revalidateLocked(); err == nil {
		t.Fatal("nil store revalidation succeeded")
	}
	if _, err := nilStore.inspectActiveRegular("state"); err == nil {
		t.Fatal("nil active regular inspection succeeded")
	}
	if _, err := nilStore.readBackup(nil, nil, 0); err == nil {
		t.Fatal("nil backup read succeeded")
	}
	if err := nilStore.writeBackups(nil, nil, nil, nil, nil); err == nil {
		t.Fatal("nil backup write succeeded")
	}
	if err := nilStore.verifyBackups(nil, nil); err == nil {
		t.Fatal("nil backup verification succeeded")
	}
	if _, _, err := readApplyPatchTransactionPrivateRegularBounded(nil, "state", 1); err == nil {
		t.Fatal("nil private bounded read succeeded")
	}
	if err := syncApplyPatchTxnRootDirectory(nil); err == nil {
		t.Fatal("nil directory sync succeeded")
	}
	if err := removeApplyPatchTxnRootIdentity(nil, "state", applyPatchTxnIdentity{}); err == nil {
		t.Fatal("nil owned-state removal succeeded")
	}
	if _, err := readApplyPatchTxnRootEntries(nil); err == nil {
		t.Fatal("nil root entry read succeeded")
	}
}

func TestApplyPatchTransactionStoreCoverageRootValidation(t *testing.T) {
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := requireApplyPatchTxnWorkspaceReadyForNewTransaction(root); err != nil {
		t.Fatalf("empty workspace readiness = %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "alien"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := requireApplyPatchTxnWorkspaceReadyForNewTransaction(root); err == nil {
		t.Fatal("alien workspace entry was accepted")
	}
	if _, _, err := readApplyPatchTransactionPrivateRegularBounded(root, "alien", -1); err == nil {
		t.Fatal("negative private read limit succeeded")
	}
	if err := os.Mkdir(filepath.Join(directory, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readApplyPatchTransactionPrivateRegularBounded(root, "directory", 16); err == nil {
		t.Fatal("private directory read succeeded")
	}
	if err := os.Chmod(filepath.Join(directory, "alien"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readApplyPatchTransactionPrivateRegularBounded(root, "alien", 16); err == nil {
		t.Fatal("non-private file read succeeded")
	}

	store := &applyPatchTxnStore{
		activeRoot: root,
		owned:      make(map[string]applyPatchTxnIdentity),
	}
	if err := store.writeReplacingFileLocked("", "target", []byte("x"), nil); err == nil {
		t.Fatal("invalid replacing write succeeded")
	}
	if err := os.WriteFile(filepath.Join(directory, "stage"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.writeReplacingFileLocked("stage", "target", []byte("x"), nil); err == nil {
		t.Fatal("existing replacement stage succeeded")
	}

	store.journalBytes = nil
	if err := os.WriteFile(
		filepath.Join(directory, applyPatchTransactionJournalFile),
		[]byte("journal"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.revalidateCurrentJournalLocked(nil); err == nil {
		t.Fatal("unexpected current journal succeeded")
	}
	if err := os.Remove(filepath.Join(directory, applyPatchTransactionJournalFile)); err != nil {
		t.Fatal(err)
	}
	store.journalBytes = []byte("expected")
	if err := store.revalidateCurrentJournalLocked(nil); err == nil {
		t.Fatal("missing tracked journal succeeded")
	}
}

func TestApplyPatchTransactionStoreCoverageBackupValidation(t *testing.T) {
	t.Run("write guards and cancellation", func(t *testing.T) {
		transaction, backup := newApplyPatchTxnStoreCoverageTransaction(t)
		if err := transaction.store.writeBackups(nil, nil, nil, nil, nil); err == nil {
			t.Fatal("invalid backup write state succeeded")
		}
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		if err := transaction.store.writeBackups(
			canceled,
			transaction.key[:],
			transaction.intent,
			transaction.journal,
			func(*applyPatchTransactionJournal) error { return nil },
		); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled backup write = %v", err)
		}
		clone := cloneApplyPatchTxnStoreCoverageJournal(t, transaction)
		cloneBackup := requireApplyPatchTxnStoreCoverageBackup(t, clone)
		cloneBackup.Backup = nil
		if err := transaction.store.writeBackups(
			context.Background(),
			transaction.key[:],
			transaction.intent,
			clone,
			func(*applyPatchTransactionJournal) error { return nil },
		); err == nil {
			t.Fatal("missing backup record write succeeded")
		}
		if backup.StateIdentity == nil {
			t.Fatal("fixture backup identity is unavailable")
		}
	})

	for _, testCase := range []struct {
		name   string
		mutate func(*applyPatchTxnStore, *applyPatchTransactionJournalArtifact)
	}{
		{
			name: "missing backup record",
			mutate: func(_ *applyPatchTxnStore, backup *applyPatchTransactionJournalArtifact) {
				backup.Backup = nil
			},
		},
		{
			name: "uncheckpointed existing backup",
			mutate: func(_ *applyPatchTxnStore, backup *applyPatchTransactionJournalArtifact) {
				backup.StateIdentity = nil
			},
		},
		{
			name: "invalid link metadata",
			mutate: func(_ *applyPatchTxnStore, backup *applyPatchTransactionJournalArtifact) {
				backup.StateLinks = 2
			},
		},
		{
			name: "missing committed-cleanup backup",
			mutate: func(store *applyPatchTxnStore, backup *applyPatchTransactionJournalArtifact) {
				store.committedCleanupAuthenticated = true
				backup.StateName = ".picoclaw-apply-patch-backup-00000000000000000000000000000000"
			},
		},
		{
			name: "identity mismatch",
			mutate: func(_ *applyPatchTxnStore, backup *applyPatchTransactionJournalArtifact) {
				backup.StateIdentity.File++
			},
		},
		{
			name: "authentication mismatch",
			mutate: func(_ *applyPatchTxnStore, backup *applyPatchTransactionJournalArtifact) {
				backup.Backup.HMACSHA256 = strings.Repeat("0", 64)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			transaction, _ := newApplyPatchTxnStoreCoverageTransaction(t)
			clone := cloneApplyPatchTxnStoreCoverageJournal(t, transaction)
			backup := requireApplyPatchTxnStoreCoverageBackup(t, clone)
			testCase.mutate(transaction.store, backup)
			err := transaction.store.verifyBackups(transaction.key[:], clone)
			if testCase.name == "missing committed-cleanup backup" {
				if err != nil {
					t.Fatalf("authenticated missing backup = %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("invalid backup verification succeeded")
			}
		})
	}

	t.Run("link count changed", func(t *testing.T) {
		transaction, backup := newApplyPatchTxnStoreCoverageTransaction(t)
		transaction.store.mu.Lock()
		linkErr := transaction.store.activeRoot.Link(backup.StateName, "backup-alias")
		transaction.store.mu.Unlock()
		if linkErr != nil {
			t.Fatal(linkErr)
		}
		if err := transaction.store.verifyBackups(
			transaction.key[:], transaction.journal,
		); err == nil {
			t.Fatal("hard-linked backup verification succeeded")
		}
	})

	t.Run("read metadata and authentication", func(t *testing.T) {
		transaction, _ := newApplyPatchTxnStoreCoverageTransaction(t)
		if _, err := transaction.store.readBackup(nil, nil, 0); err == nil {
			t.Fatal("nil journal backup read succeeded")
		}
		clone := cloneApplyPatchTxnStoreCoverageJournal(t, transaction)
		backup := requireApplyPatchTxnStoreCoverageBackup(t, clone)
		backup.StateLinks = 2
		if _, err := transaction.store.readBackup(transaction.key[:], clone, 0); err == nil {
			t.Fatal("invalid backup link metadata read succeeded")
		}
		clone = cloneApplyPatchTxnStoreCoverageJournal(t, transaction)
		backup = requireApplyPatchTxnStoreCoverageBackup(t, clone)
		backup.StateIdentity.File++
		if _, err := transaction.store.readBackup(transaction.key[:], clone, 0); err == nil {
			t.Fatal("wrong backup identity read succeeded")
		}
		clone = cloneApplyPatchTxnStoreCoverageJournal(t, transaction)
		backup = requireApplyPatchTxnStoreCoverageBackup(t, clone)
		backup.Backup.HMACSHA256 = strings.Repeat("0", 64)
		if _, err := transaction.store.readBackup(transaction.key[:], clone, 0); err == nil {
			t.Fatal("wrong backup authentication read succeeded")
		}
	})

	t.Run("checkpoint failure", func(t *testing.T) {
		transaction, _ := newApplyPatchTxnStoreCoverageTransaction(t)
		data := []byte("coverage backup")
		name, err := newApplyPatchTxnPrivateName("backup")
		if err != nil {
			t.Fatal(err)
		}
		record, err := newApplyPatchTransactionBackupRecord(
			transaction.key[:], transaction.journal.TransactionID, name, data,
		)
		if err != nil {
			t.Fatal(err)
		}
		artifact := &applyPatchTransactionJournalArtifact{
			OperationIndex: 0,
			Role:           applyPatchTransactionArtifactBackupBlob,
			StateName:      name,
			Backup:         &record,
		}
		injected := errors.New("backup identity checkpoint failed")
		if err := transaction.store.writeOneBackup(
			context.Background(),
			transaction.key[:],
			artifact,
			data,
			transaction.journal,
			func(*applyPatchTransactionJournal) error { return injected },
		); !errors.Is(err, injected) {
			t.Fatalf("backup checkpoint failure = %v", err)
		}
	})
}

func newApplyPatchTxnStoreCoverageTransaction(
	t *testing.T,
) (*applyPatchPreparedTransaction, *applyPatchTransactionJournalArtifact) {
	t.Helper()
	workspace := t.TempDir()
	source := filepath.Join(workspace, "source.txt")
	if err := os.WriteFile(source, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := buildApplyPatchTxnTestPlan(
		t,
		workspace,
		"*** Begin Patch\n*** Update File: source.txt\n@@\n-before\n+after\n*** End Patch",
	)
	state, workspaceState := openApplyPatchTxnTestState(t, plan.workspace)
	transaction, err := beginApplyPatchTransaction(
		context.Background(), state, workspaceState, plan,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transaction.closeHandles() })
	return transaction, requireApplyPatchTxnStoreCoverageBackup(t, transaction.journal)
}

func requireApplyPatchTxnStoreCoverageBackup(
	t *testing.T,
	journal *applyPatchTransactionJournal,
) *applyPatchTransactionJournalArtifact {
	t.Helper()
	backup, err := requireApplyPatchTxnArtifact(
		journal, 0, applyPatchTransactionArtifactBackupBlob,
	)
	if err != nil {
		t.Fatal(err)
	}
	return backup
}

func cloneApplyPatchTxnStoreCoverageJournal(
	t *testing.T,
	transaction *applyPatchPreparedTransaction,
) *applyPatchTransactionJournal {
	t.Helper()
	encoded, err := encodeApplyPatchTransactionJournal(
		transaction.key[:], transaction.journal,
	)
	if err != nil {
		t.Fatal(err)
	}
	clone, err := decodeApplyPatchTransactionJournal(transaction.key[:], encoded)
	if err != nil {
		t.Fatal(err)
	}
	return clone
}
