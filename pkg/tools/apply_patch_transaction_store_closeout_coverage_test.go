package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionStoreCloseoutPrivateCleanupConflicts(t *testing.T) {
	t.Run("invalid journal", func(t *testing.T) {
		transaction, _ := newApplyPatchTxnStoreCoverageTransaction(t)
		invalid := cloneApplyPatchTxnStoreCoverageJournal(t, transaction)
		invalid.TransactionID = "invalid"
		if err := transaction.store.preparePrivateCleanup(transaction.key[:], invalid); err == nil {
			t.Fatal("invalid cleanup journal succeeded")
		}
	})

	t.Run("pointer conflict", func(t *testing.T) {
		transaction, _ := newApplyPatchTxnStoreCoverageTransaction(t)
		workspacePath, err := transaction.store.workspace.directoryPath()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(workspacePath, applyPatchTransactionPointerFile),
			[]byte("alien pointer"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		if err := transaction.store.preparePrivateCleanup(
			transaction.key[:], transaction.journal,
		); err == nil {
			t.Fatal("alien cleanup pointer succeeded")
		}
	})

	t.Run("quarantine collision", func(t *testing.T) {
		transaction, _ := newApplyPatchTxnStoreCoverageTransaction(t)
		committedName := applyPatchTransactionCommitNamePrefix +
			transaction.journal.TransactionID
		workspacePath, err := transaction.store.workspace.directoryPath()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(workspacePath, committedName), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := transaction.store.quarantineCommittedDirectory(); err == nil {
			t.Fatal("colliding committed directory quarantine succeeded")
		}
	})

	t.Run("already quarantined", func(t *testing.T) {
		transaction, _ := newApplyPatchTxnStoreCoverageTransaction(t)
		transaction.store.activeName = applyPatchTransactionCommitNamePrefix +
			transaction.journal.TransactionID
		if err := transaction.store.quarantineCommittedDirectory(); err == nil {
			t.Fatal("renamed in-memory directory unexpectedly revalidated")
		}
	})
}

func TestApplyPatchTransactionStoreCloseoutFinishCleanupConflicts(t *testing.T) {
	t.Run("alien active entry", func(t *testing.T) {
		transaction, _ := newApplyPatchTxnStoreCoverageTransaction(t)
		transaction.store.mu.Lock()
		file, openErr := transaction.store.activeRoot.OpenFile(
			"alien",
			os.O_CREATE|os.O_EXCL|os.O_RDWR,
			0o600,
		)
		if openErr == nil {
			openErr = file.Close()
		}
		if openErr != nil {
			transaction.store.mu.Unlock()
			t.Fatal(openErr)
		}
		transaction.store.mu.Unlock()
		if err := transaction.store.finishCommittedStateCleanup(); err == nil {
			t.Fatal("alien active entry cleanup succeeded")
		}
	})

	t.Run("owned identity changed", func(t *testing.T) {
		transaction, _ := newApplyPatchTxnStoreCoverageTransaction(t)
		transaction.store.mu.Lock()
		identity := transaction.store.owned[applyPatchTransactionJournalFile]
		identity.File++
		transaction.store.owned[applyPatchTransactionJournalFile] = identity
		transaction.store.mu.Unlock()
		if err := transaction.store.finishCommittedStateCleanup(); err == nil {
			t.Fatal("wrong owned identity cleanup succeeded")
		}
	})

	t.Run("missing pointer identity", func(t *testing.T) {
		transaction, _ := newApplyPatchTxnStoreCoverageTransaction(t)
		transaction.store.pointerInfo = nil
		transaction.store.mu.Lock()
		for name := range transaction.store.owned {
			if name == applyPatchTransactionJournalFile {
				continue
			}
			identity := transaction.store.owned[name]
			if err := removeApplyPatchTxnRootIdentity(transaction.store.activeRoot, name, identity); err != nil {
				transaction.store.mu.Unlock()
				t.Fatal(err)
			}
			delete(transaction.store.owned, name)
		}
		transaction.store.mu.Unlock()
		if err := transaction.store.finishCommittedStateCleanup(); err == nil {
			t.Fatal("missing pointer identity cleanup succeeded")
		}
	})

	t.Run("moved active directory", func(t *testing.T) {
		transaction, _ := newApplyPatchTxnStoreCoverageTransaction(t)
		workspacePath, err := transaction.store.workspace.directoryPath()
		if err != nil {
			t.Fatal(err)
		}
		activePath := filepath.Join(workspacePath, transaction.store.activeName)
		if err := os.Rename(activePath, activePath+"-moved"); err != nil {
			t.Fatal(err)
		}
		if err := transaction.store.finishCommittedStateCleanup(); err == nil {
			t.Fatal("moved active directory cleanup succeeded")
		}
	})
}

func TestApplyPatchTransactionStoreCloseoutConstructionAndJournalCorruption(t *testing.T) {
	t.Run("closed workspace store", func(t *testing.T) {
		transaction, _ := newApplyPatchTxnStoreCoverageTransaction(t)
		workspace := transaction.workspaceState
		if err := workspace.Close(); err != nil {
			t.Fatal(err)
		}
		if store, err := createApplyPatchTxnStore(workspace, transaction.intent); err == nil || store != nil {
			t.Fatalf("closed workspace store = %#v, %v", store, err)
		}
	})

	t.Run("alien workspace state", func(t *testing.T) {
		workspacePath := t.TempDir()
		plan := buildApplyPatchTxnTestPlan(
			t, workspacePath,
			"*** Begin Patch\n*** Add File: result.txt\n+candidate\n*** End Patch",
		)
		state, workspaceState := openApplyPatchTxnTestState(t, plan.workspace)
		intent, err := buildApplyPatchTxnIntent(context.Background(), plan)
		if err != nil {
			t.Fatal(err)
		}
		defer intent.Close()
		if err := workspaceState.withDirectoryAnchor(func(root *os.Root) error {
			file, err := root.OpenFile("alien", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
			if err != nil {
				return err
			}
			return file.Close()
		}); err != nil {
			t.Fatal(err)
		}
		if store, err := createApplyPatchTxnStore(workspaceState, intent); err == nil || store != nil {
			t.Fatalf("alien workspace store = %#v, %v", store, err)
		}
		_ = state
	})

	t.Run("journal bytes and ownership", func(t *testing.T) {
		transaction, _ := newApplyPatchTxnStoreCoverageTransaction(t)
		transaction.store.mu.Lock()
		journalPath := filepath.Join(
			transaction.store.workspace.absoluteDirectory,
			transaction.store.activeName,
			applyPatchTransactionJournalFile,
		)
		transaction.store.mu.Unlock()
		if err := os.WriteFile(journalPath, []byte("alien"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := transaction.store.readJournal(transaction.key[:]); err == nil {
			t.Fatal("alien journal read succeeded")
		}
	})

	t.Run("closed store read", func(t *testing.T) {
		transaction, _ := newApplyPatchTxnStoreCoverageTransaction(t)
		if err := transaction.store.Close(); err != nil {
			t.Fatal(err)
		}
		if _, _, err := transaction.store.readJournal(transaction.key[:]); err == nil {
			t.Fatal("closed store journal read succeeded")
		}
		transaction.store = nil
	})
}

func TestApplyPatchTransactionStoreCloseoutBackupWriteFailures(t *testing.T) {
	t.Run("existing backup name", func(t *testing.T) {
		transaction, backup := newApplyPatchTxnStoreCoverageTransaction(t)
		clone := *backup
		clone.StateIdentity = nil
		clone.StateLinks = 0
		if err := transaction.store.writeOneBackup(
			context.Background(), transaction.key[:], &clone,
			[]byte("before\n"), transaction.journal,
			func(*applyPatchTransactionJournal) error { return nil },
		); err == nil {
			t.Fatal("existing backup write succeeded")
		}
	})

	t.Run("canceled write after identity checkpoint", func(t *testing.T) {
		transaction, _ := newApplyPatchTxnStoreCoverageTransaction(t)
		artifact, data := newApplyPatchTxnStoreCloseoutBackup(t, transaction)
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		if err := transaction.store.writeOneBackup(
			canceled, transaction.key[:], artifact, data, transaction.journal,
			func(*applyPatchTransactionJournal) error { return nil },
		); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled backup write = %v", err)
		}
	})

	t.Run("store closes after identity checkpoint", func(t *testing.T) {
		transaction, _ := newApplyPatchTxnStoreCoverageTransaction(t)
		artifact, data := newApplyPatchTxnStoreCloseoutBackup(t, transaction)
		if err := transaction.store.writeOneBackup(
			context.Background(), transaction.key[:], artifact, data, transaction.journal,
			func(*applyPatchTransactionJournal) error {
				return transaction.store.Close()
			},
		); err == nil {
			t.Fatal("closed-store backup write succeeded")
		}
		transaction.store = nil
	})
}

func TestApplyPatchTransactionStoreCloseoutBoundedPrivateFiles(t *testing.T) {
	directory := t.TempDir()
	root, openErr := os.OpenRoot(directory)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer root.Close()
	if err := os.WriteFile(filepath.Join(directory, "oversize"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readApplyPatchTransactionPrivateRegularBounded(root, "oversize", 4); err == nil {
		t.Fatal("oversize bounded private read succeeded")
	}
	if err := os.Mkdir(filepath.Join(directory, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readApplyPatchTransactionPrivateRegularBounded(root, "directory", 4); err == nil {
		t.Fatal("directory bounded private read succeeded")
	}
	if err := os.WriteFile(filepath.Join(directory, "wrong-mode"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readApplyPatchTransactionPrivateRegularBounded(root, "wrong-mode", 4); err == nil {
		t.Fatal("wrong-mode bounded private read succeeded")
	}
	if err := removeApplyPatchTxnRootIdentity(
		root,
		"missing",
		applyPatchTxnIdentity{Device: 1, File: 1, Kind: "regular"},
	); err == nil {
		t.Fatal("missing owned state removal succeeded")
	}
	if err := os.WriteFile(filepath.Join(directory, "identity"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, statErr := os.Lstat(filepath.Join(directory, "identity"))
	if statErr != nil {
		t.Fatal(statErr)
	}
	identity, identityErr := applyPatchTxnIdentityFromFileInfo(info, "regular")
	if identityErr != nil {
		t.Fatal(identityErr)
	}
	identity.File++
	if err := removeApplyPatchTxnRootIdentity(root, "identity", identity); err == nil {
		t.Fatal("wrong owned state identity removal succeeded")
	}
	for index := 0; index <= applyPatchTransactionMaxEntries; index++ {
		name := filepath.Join(directory, fmt.Sprintf("entry-%04d", index))
		if err := os.WriteFile(name, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := readApplyPatchTxnRootEntries(root); err == nil {
		t.Fatal("oversize root entry set succeeded")
	}
}

func TestApplyPatchTransactionStoreCloseoutReplacingTargetCollision(t *testing.T) {
	transaction, _ := newApplyPatchTxnStoreCoverageTransaction(t)
	transaction.store.mu.Lock()
	file, err := transaction.store.activeRoot.OpenFile(
		"existing-target", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600,
	)
	if err == nil {
		err = file.Close()
	}
	if err != nil {
		transaction.store.mu.Unlock()
		t.Fatal(err)
	}
	err = transaction.store.writeReplacingFileLocked(
		"replacement-stage", "existing-target", []byte("replacement"), nil,
	)
	_, stageErr := transaction.store.activeRoot.Lstat("replacement-stage")
	_, tracked := transaction.store.owned["replacement-stage"]
	transaction.store.mu.Unlock()
	if err != nil {
		t.Fatalf("replacing an existing target = %v", err)
	}
	if !errors.Is(stageErr, os.ErrNotExist) || tracked {
		t.Fatalf("replacement cleanup = stage:%v tracked:%t", stageErr, tracked)
	}
}

func TestApplyPatchTransactionStoreCloseoutJournalLessCleanupConflicts(t *testing.T) {
	workspacePath := t.TempDir()
	plan := buildApplyPatchTxnTestPlan(
		t, workspacePath,
		"*** Begin Patch\n*** Add File: result.txt\n+candidate\n*** End Patch",
	)
	_, workspaceState := openApplyPatchTxnTestState(t, plan.workspace)
	name := "active-closeout0000000000000000000000000000"
	var directoryInfo os.FileInfo
	if err := workspaceState.withDirectoryAnchor(func(root *os.Root) error {
		if err := root.Mkdir(name, 0o700); err != nil {
			return err
		}
		var err error
		directoryInfo, err = root.Lstat(name)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	wrongInfo, err := os.Lstat(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := cleanupApplyPatchTxnJournalLessActiveDirectory(
		workspaceState, name, wrongInfo,
	); err == nil {
		t.Fatal("wrong journal-less directory identity cleanup succeeded")
	}
	if err := os.WriteFile(
		filepath.Join(workspaceState.absoluteDirectory, name, "alien"),
		[]byte("alien"), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := cleanupApplyPatchTxnJournalLessActiveDirectory(
		workspaceState, name, directoryInfo,
	); err == nil {
		t.Fatal("nonempty journal-less directory cleanup succeeded")
	}
}

func newApplyPatchTxnStoreCloseoutBackup(
	t *testing.T,
	transaction *applyPatchPreparedTransaction,
) (*applyPatchTransactionJournalArtifact, []byte) {
	t.Helper()
	data := []byte("closeout backup")
	name := applyPatchTxnCloseoutPrivateName(t, "backup")
	record, err := newApplyPatchTransactionBackupRecord(
		transaction.key[:], transaction.journal.TransactionID, name, data,
	)
	if err != nil {
		t.Fatal(err)
	}
	return &applyPatchTransactionJournalArtifact{
		OperationIndex: 0,
		Role:           applyPatchTransactionArtifactBackupBlob,
		StateName:      name,
		Backup:         &record,
	}, data
}
