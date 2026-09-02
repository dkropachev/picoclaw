package tools

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestApplyPatchRecoveryGlobalMarginNamespaceFailures(t *testing.T) {
	if store, journal, err := openApplyPatchTxnRecoveryStore(nil, nil); store != nil || journal != nil || err == nil {
		t.Fatalf("nil recovery workspace = %#v, %#v, %v", store, journal, err)
	}

	t.Run("invalid pointer closes opened transaction", func(t *testing.T) {
		fixture := newApplyPatchTxnGlobalMarginFixture(t)
		mkdirApplyPatchTxnRecoveryWorkspace(t, fixture, applyPatchTxnGlobalMarginActiveName("a"))
		writeApplyPatchTxnRecoveryWorkspaceFile(
			t,
			fixture,
			applyPatchTransactionPointerFile,
			[]byte("invalid authenticated pointer"),
		)
		assertApplyPatchTxnGlobalMarginRecoveryRejected(t, fixture)
	})

	t.Run("invalid pointer stage closes opened transaction", func(t *testing.T) {
		fixture := newApplyPatchTxnGlobalMarginFixture(t)
		mkdirApplyPatchTxnRecoveryWorkspace(t, fixture, applyPatchTxnGlobalMarginActiveName("b"))
		writeApplyPatchTxnRecoveryWorkspaceFile(
			t,
			fixture,
			applyPatchTransactionPointerStageFile,
			[]byte("invalid authenticated pointer stage"),
		)
		assertApplyPatchTxnGlobalMarginRecoveryRejected(t, fixture)
	})

	t.Run("insecure active directory is rejected", func(t *testing.T) {
		fixture := newApplyPatchTxnGlobalMarginFixture(t)
		name := applyPatchTxnGlobalMarginActiveName("c")
		mkdirApplyPatchTxnRecoveryWorkspace(t, fixture, name)
		if err := fixture.workspaceState.withDirectoryAnchor(func(root *os.Root) error {
			return root.Chmod(name, 0o755)
		}); err != nil {
			t.Fatal(err)
		}
		assertApplyPatchTxnGlobalMarginRecoveryRejected(t, fixture)
	})

	t.Run("oversized journal-less directory is rejected", func(t *testing.T) {
		fixture := newApplyPatchTxnGlobalMarginFixture(t)
		name := applyPatchTxnGlobalMarginActiveName("d")
		mkdirApplyPatchTxnRecoveryWorkspace(t, fixture, name)
		if err := fixture.workspaceState.withDirectoryAnchor(func(root *os.Root) error {
			active, err := root.OpenRoot(name)
			if err != nil {
				return err
			}
			defer active.Close()
			for index := 0; index <= applyPatchTransactionMaxEntries; index++ {
				file, createErr := active.OpenFile(
					fmt.Sprintf("entry-%04d", index),
					os.O_CREATE|os.O_EXCL|os.O_RDWR,
					0o600,
				)
				if createErr != nil {
					return createErr
				}
				if closeErr := file.Close(); closeErr != nil {
					return closeErr
				}
			}
			return syncApplyPatchTxnRootDirectory(active)
		}); err != nil {
			t.Fatal(err)
		}
		assertApplyPatchTxnGlobalMarginRecoveryRejected(t, fixture)
	})

	t.Run("unsafe journal permissions close opened transaction", func(t *testing.T) {
		fixture := newApplyPatchTxnGlobalMarginFixture(t)
		name := applyPatchTxnGlobalMarginActiveName("e")
		mkdirApplyPatchTxnRecoveryWorkspace(t, fixture, name)
		if err := fixture.workspaceState.withDirectoryAnchor(func(root *os.Root) error {
			active, err := root.OpenRoot(name)
			if err != nil {
				return err
			}
			defer active.Close()
			file, err := active.OpenFile(
				applyPatchTransactionJournalFile,
				os.O_CREATE|os.O_EXCL|os.O_RDWR,
				0o600,
			)
			if err != nil {
				return err
			}
			if err = file.Close(); err != nil {
				return err
			}
			return active.Chmod(applyPatchTransactionJournalFile, 0o644)
		}); err != nil {
			t.Fatal(err)
		}
		assertApplyPatchTxnGlobalMarginRecoveryRejected(t, fixture)
	})
}

func TestApplyPatchRecoveryGlobalMarginStageRenameCollision(t *testing.T) {
	transaction, _ := newApplyPatchTxnEffectCoverageTransaction(t)
	const targetName = "collision-target"
	const stageName = "collision-stage"
	if err := transaction.store.activeRoot.Mkdir(targetName, 0o700); err != nil {
		t.Fatal(err)
	}

	transaction.store.mu.Lock()
	err := transaction.store.writeReplacingFileLocked(
		stageName,
		targetName,
		[]byte("candidate"),
		nil,
	)
	transaction.store.mu.Unlock()
	if err == nil {
		t.Fatal("transaction stage replaced a directory")
	}
	if _, statErr := transaction.store.activeRoot.Lstat(stageName); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed stage cleanup = %v", statErr)
	}
}

func newApplyPatchTxnGlobalMarginFixture(t *testing.T) *applyPatchTxnRecoveryFixture {
	t.Helper()
	fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
	t.Cleanup(func() {
		var closeErr error
		if fixture.workspaceState != nil {
			closeErr = errors.Join(closeErr, fixture.workspaceState.Close())
		}
		if fixture.state != nil {
			closeErr = errors.Join(closeErr, fixture.state.Close())
		}
		if closeErr != nil {
			t.Errorf("close recovery fixture: %v", closeErr)
		}
	})
	return fixture
}

func applyPatchTxnGlobalMarginActiveName(fill string) string {
	return applyPatchTransactionActiveNamePrefix + strings.Repeat(fill, applyPatchTransactionIDHexBytes)
}

func assertApplyPatchTxnGlobalMarginRecoveryRejected(
	t *testing.T,
	fixture *applyPatchTxnRecoveryFixture,
) {
	t.Helper()
	key, err := fixture.state.authenticationKey()
	if err != nil {
		t.Fatal(err)
	}
	store, journal, openErr := openApplyPatchTxnRecoveryStore(fixture.workspaceState, key[:])
	clear(key[:])
	if store != nil {
		_ = store.Close()
	}
	if journal != nil || openErr == nil {
		t.Fatalf("unsafe recovery namespace = %#v, %#v, %v", store, journal, openErr)
	}
}
