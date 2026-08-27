package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyPatchTransactionRecoveryStoreExactCloseoutControlEntryTypes(t *testing.T) {
	for _, name := range []string{
		applyPatchTransactionPointerFile,
		applyPatchTransactionPointerStageFile,
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
			if err := fixture.workspaceState.withDirectoryAnchor(func(root *os.Root) error {
				return root.Mkdir(name, 0o700)
			}); err != nil {
				t.Fatal(err)
			}
			key, err := fixture.state.authenticationKey()
			if err != nil {
				t.Fatal(err)
			}
			store, journal, err := openApplyPatchTxnRecoveryStore(
				fixture.workspaceState, key[:],
			)
			if store != nil {
				_ = store.Close()
			}
			if journal != nil || err == nil {
				t.Fatalf("directory control entry = %#v, %#v, %v", store, journal, err)
			}
		})
	}
}

func TestApplyPatchTransactionRecoveryStoreExactCloseoutMalformedInitialStage(t *testing.T) {
	fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
	intent, err := buildApplyPatchTxnIntent(context.Background(), fixture.plan)
	if err != nil {
		t.Fatal(err)
	}
	store, storeErr := createApplyPatchTxnStore(fixture.workspaceState, intent)
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	file, openErr := store.activeRoot.OpenFile(
		applyPatchTransactionJournalStageFile,
		os.O_CREATE|os.O_EXCL|os.O_RDWR,
		0o600,
	)
	if openErr != nil {
		t.Fatal(openErr)
	}
	if _, err := file.WriteString("invalid"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := intent.Close(); err != nil {
		t.Fatal(err)
	}
	key, keyErr := fixture.state.authenticationKey()
	if keyErr != nil {
		t.Fatal(keyErr)
	}
	recovered, journal, recoveryErr := openApplyPatchTxnRecoveryStore(
		fixture.workspaceState,
		key[:],
	)
	if recovered != nil {
		_ = recovered.Close()
	}
	if journal != nil || recoveryErr == nil {
		t.Fatalf("malformed initial stage = %#v, %#v, %v", recovered, journal, recoveryErr)
	}
}

func TestApplyPatchTransactionRecoveryStoreExactCloseoutJournalConflicts(t *testing.T) {
	for _, state := range []string{"malformed journal", "active binding", "alien state entry"} {
		t.Run(state, func(t *testing.T) {
			fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
			tx := fixture.begin(t)
			activeName := tx.store.activeName
			workspaceStatePath, pathErr := fixture.workspaceState.directoryPath()
			if pathErr != nil {
				t.Fatal(pathErr)
			}
			activePath := filepath.Join(workspaceStatePath, activeName)
			if err := tx.closeHandles(); err != nil {
				t.Fatal(err)
			}
			switch state {
			case "malformed journal":
				if err := os.WriteFile(
					filepath.Join(activePath, applyPatchTransactionJournalFile),
					[]byte("invalid"),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			case "active binding":
				replacement := "active-" + strings.Repeat("e", applyPatchTransactionIDHexBytes)
				if err := os.Rename(activePath, filepath.Join(workspaceStatePath, replacement)); err != nil {
					t.Fatal(err)
				}
			case "alien state entry":
				if err := os.WriteFile(filepath.Join(activePath, "alien"), []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			key, keyErr := fixture.state.authenticationKey()
			if keyErr != nil {
				t.Fatal(keyErr)
			}
			store, journal, err := openApplyPatchTxnRecoveryStore(
				fixture.workspaceState, key[:],
			)
			if store != nil {
				_ = store.Close()
			}
			if journal != nil || err == nil {
				t.Fatalf("journal conflict %q = %#v, %#v, %v", state, store, journal, err)
			}
		})
	}
}
