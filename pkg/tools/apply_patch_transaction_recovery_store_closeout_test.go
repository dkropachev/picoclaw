package tools

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestApplyPatchTransactionRecoveryStoreCloseoutWorkspaceConflicts(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *applyPatchTxnRecoveryFixture)
	}{
		{
			"alien entry",
			func(t *testing.T, fixture *applyPatchTxnRecoveryFixture) {
				writeApplyPatchTxnRecoveryWorkspaceFile(t, fixture, "alien", []byte("x"))
			},
		},
		{
			"invalid active name",
			func(t *testing.T, fixture *applyPatchTxnRecoveryFixture) {
				mkdirApplyPatchTxnRecoveryWorkspace(t, fixture, "active-bad")
			},
		},
		{
			"active entry is regular",
			func(t *testing.T, fixture *applyPatchTxnRecoveryFixture) {
				writeApplyPatchTxnRecoveryWorkspaceFile(
					t,
					fixture,
					"active-"+strings.Repeat("c", applyPatchTransactionIDHexBytes),
					[]byte("not a directory"),
				)
			},
		},
		{
			"journal-less shell has multiple entries",
			func(t *testing.T, fixture *applyPatchTxnRecoveryFixture) {
				name := "active-" + strings.Repeat("d", applyPatchTransactionIDHexBytes)
				mkdirApplyPatchTxnRecoveryWorkspace(t, fixture, name)
				if err := fixture.workspaceState.withDirectoryAnchor(func(root *os.Root) error {
					active, err := root.OpenRoot(name)
					if err != nil {
						return err
					}
					defer active.Close()
					for _, child := range []string{"one", "two"} {
						file, err := active.OpenFile(child, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
						if err != nil {
							return err
						}
						if err := file.Close(); err != nil {
							return err
						}
					}
					return syncApplyPatchTxnRootDirectory(active)
				}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			"committed without pointer",
			func(t *testing.T, fixture *applyPatchTxnRecoveryFixture) {
				mkdirApplyPatchTxnRecoveryWorkspace(
					t,
					fixture,
					"committed-"+strings.Repeat("a", applyPatchTransactionIDHexBytes),
				)
			},
		},
		{
			"multiple active directories",
			func(t *testing.T, fixture *applyPatchTxnRecoveryFixture) {
				mkdirApplyPatchTxnRecoveryWorkspace(
					t,
					fixture,
					"active-"+strings.Repeat("a", applyPatchTransactionIDHexBytes),
				)
				mkdirApplyPatchTxnRecoveryWorkspace(
					t,
					fixture,
					"active-"+strings.Repeat("b", applyPatchTransactionIDHexBytes),
				)
			},
		},
		{
			"invalid pointer",
			func(t *testing.T, fixture *applyPatchTxnRecoveryFixture) {
				writeApplyPatchTxnRecoveryWorkspaceFile(
					t,
					fixture,
					applyPatchTransactionPointerFile,
					[]byte("invalid"),
				)
			},
		},
		{
			"invalid pointer stage",
			func(t *testing.T, fixture *applyPatchTxnRecoveryFixture) {
				writeApplyPatchTxnRecoveryWorkspaceFile(
					t,
					fixture,
					applyPatchTransactionPointerStageFile,
					[]byte("invalid"),
				)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
			test.setup(t, fixture)
			key, err := fixture.state.authenticationKey()
			if err != nil {
				t.Fatal(err)
			}
			store, journal, openErr := openApplyPatchTxnRecoveryStore(
				fixture.workspaceState,
				key[:],
			)
			clear(key[:])
			if store != nil {
				_ = store.Close()
			}
			if journal != nil || openErr == nil {
				t.Fatalf("conflicting recovery store = %#v, %#v, %v", store, journal, openErr)
			}
		})
	}
}

func TestApplyPatchTransactionRecoveryStoreCloseoutPointerRemovalGuards(t *testing.T) {
	fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
	if err := removeApplyPatchTxnRecoveryPointer(fixture.workspaceState, nil); err == nil {
		t.Fatal("nil pointer identity removed")
	}
	if err := removeApplyPatchTxnRecoveryCommittedShell(
		fixture.workspaceState, "", nil, nil,
	); err == nil {
		t.Fatal("nil committed shell identities removed")
	}
	if store, journal, err := openApplyPatchTxnRecoveryStore(
		fixture.workspaceState,
		nil,
	); store != nil || journal != nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty recovery workspace = %#v, %#v, %v", store, journal, err)
	}
}

func writeApplyPatchTxnRecoveryWorkspaceFile(
	t *testing.T,
	fixture *applyPatchTxnRecoveryFixture,
	name string,
	data []byte,
) {
	t.Helper()
	directory, err := fixture.workspaceState.directoryPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.workspaceState.withDirectoryAnchor(func(root *os.Root) error {
		return publishApplyPatchTransactionPrivateRegular(root, directory, name, data)
	}); err != nil {
		t.Fatal(err)
	}
}

func mkdirApplyPatchTxnRecoveryWorkspace(
	t *testing.T,
	fixture *applyPatchTxnRecoveryFixture,
	name string,
) {
	t.Helper()
	if err := fixture.workspaceState.withDirectoryAnchor(func(root *os.Root) error {
		if err := root.Mkdir(name, 0o700); err != nil {
			return err
		}
		return syncApplyPatchTxnRootDirectory(root)
	}); err != nil {
		t.Fatal(err)
	}
}
