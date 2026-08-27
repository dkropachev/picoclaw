package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type applyPatchTxnCloseoutHeldLock struct {
	info os.FileInfo
	err  error
}

func (lock *applyPatchTxnCloseoutHeldLock) Close() error { return nil }

func (lock *applyPatchTxnCloseoutHeldLock) fileInfo() (os.FileInfo, error) {
	return lock.info, lock.err
}

func TestApplyPatchTransactionStateOpenCloseoutRootCorruptions(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		setup func(string) error
	}{
		{
			name: "root is regular file",
			setup: func(path string) error {
				return os.WriteFile(path, []byte("file"), 0o600)
			},
		},
		{
			name: "root has public mode",
			setup: func(path string) error {
				return os.Mkdir(path, 0o755)
			},
		},
		{
			name: "init lock is directory",
			setup: func(path string) error {
				if err := os.Mkdir(path, 0o700); err != nil {
					return err
				}
				return os.Mkdir(filepath.Join(path, applyPatchTransactionInitLockFile), 0o700)
			},
		},
		{
			name: "authentication key has wrong size",
			setup: func(path string) error {
				if err := os.Mkdir(path, 0o700); err != nil {
					return err
				}
				return os.WriteFile(
					filepath.Join(path, applyPatchTransactionAuthenticationFile),
					[]byte("short"), 0o600,
				)
			},
		},
		{
			name: "authentication key has wrong mode",
			setup: func(path string) error {
				if err := os.Mkdir(path, 0o700); err != nil {
					return err
				}
				return os.WriteFile(
					filepath.Join(path, applyPatchTransactionAuthenticationFile),
					make([]byte, applyPatchTransactionAuthenticationBytes), 0o644,
				)
			},
		},
		{
			name: "authentication stage is directory",
			setup: func(path string) error {
				if err := os.Mkdir(path, 0o700); err != nil {
					return err
				}
				return os.Mkdir(
					filepath.Join(path, "."+applyPatchTransactionAuthenticationFile+".stage"),
					0o700,
				)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			workspace := t.TempDir()
			root := filepath.Join(t.TempDir(), "state")
			if err := testCase.setup(root); err != nil {
				t.Fatal(err)
			}
			prepared, err := prepareApplyPatchTransactionStateRoot(workspace, root, nil)
			if err != nil {
				t.Fatal(err)
			}
			state, err := openApplyPatchTransactionState(context.Background(), prepared)
			if state != nil {
				_ = state.Close()
			}
			if err == nil {
				t.Fatal("corrupt state root opened")
			}
		})
	}
}

func TestApplyPatchTransactionStateOpenCloseoutFenceAndLockedFile(t *testing.T) {
	workspace := t.TempDir()
	parent := t.TempDir()
	rootPath := filepath.Join(parent, "state")
	prepared, prepareErr := prepareApplyPatchTransactionStateRoot(workspace, rootPath, nil)
	if prepareErr != nil {
		t.Fatal(prepareErr)
	}
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := revalidateApplyPatchTransactionStateFences(prepared); err == nil {
		t.Fatal("changed state fence revalidated")
	}

	directory := t.TempDir()
	root, rootOpenErr := os.OpenRoot(directory)
	if rootOpenErr != nil {
		t.Fatal(rootOpenErr)
	}
	defer root.Close()
	if _, err := validateApplyPatchTransactionLockedFile(
		root, "lock", &applyPatchTxnCloseoutHeldLock{err: errors.New("handle failed")},
	); err == nil {
		t.Fatal("failed lock handle validated")
	}
	if err := os.WriteFile(filepath.Join(directory, "lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	otherPath := filepath.Join(directory, "other")
	if err := os.WriteFile(otherPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	otherInfo, otherStatErr := os.Lstat(otherPath)
	if otherStatErr != nil {
		t.Fatal(otherStatErr)
	}
	if _, err := validateApplyPatchTransactionLockedFile(
		root, "lock", &applyPatchTxnCloseoutHeldLock{info: otherInfo},
	); err == nil {
		t.Fatal("mismatched lock handle validated")
	}
	lockInfo, lockStatErr := os.Lstat(filepath.Join(directory, "lock"))
	if lockStatErr != nil {
		t.Fatal(lockStatErr)
	}
	if err := os.Chmod(filepath.Join(directory, "lock"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := validateApplyPatchTransactionLockedFile(
		root, "lock", &applyPatchTxnCloseoutHeldLock{info: lockInfo},
	); err == nil {
		t.Fatal("wrong-mode lock validated")
	}
}

func TestApplyPatchTransactionStateOpenCloseoutWorkspaceCorruptions(t *testing.T) {
	for _, corruption := range []string{"directory moved", "root closed", "lock handle failed"} {
		t.Run(corruption, func(t *testing.T) {
			state, workspacePath, _ := newApplyPatchTxnStateCloseout(t)
			workspace, err := state.lockWorkspace(context.Background(), workspacePath)
			if err != nil {
				t.Fatal(err)
			}
			defer workspace.Close()
			switch corruption {
			case "directory moved":
				if err := os.Rename(
					workspace.absoluteDirectory,
					workspace.absoluteDirectory+"-moved",
				); err != nil {
					t.Fatal(err)
				}
			case "root closed":
				if err := workspace.root.Close(); err != nil {
					t.Fatal(err)
				}
			case "lock handle failed":
				realLock := workspace.lock
				workspace.lock = &applyPatchTxnCloseoutHeldLock{err: errors.New("lock failed")}
				defer realLock.Close()
			}
			if err := workspace.revalidateLocked(); err == nil {
				t.Fatal("corrupt workspace state revalidated")
			}
		})
	}
}

func TestApplyPatchTransactionStateOpenCloseoutWorkspaceDirectoryConflicts(t *testing.T) {
	t.Run("workspaces root is regular", func(t *testing.T) {
		state, workspacePath, _ := newApplyPatchTxnStateCloseout(t)
		if err := os.WriteFile(
			filepath.Join(state.prepared.path, applyPatchTransactionWorkspacesDirectory),
			[]byte("alien"), 0o600,
		); err != nil {
			t.Fatal(err)
		}
		if workspace, err := state.lockWorkspace(context.Background(), workspacePath); err == nil || workspace != nil {
			t.Fatalf("regular workspaces root = %#v, %v", workspace, err)
		}
	})

	t.Run("binding conflict", func(t *testing.T) {
		state, workspacePath, _ := newApplyPatchTxnStateCloseout(t)
		workspaceInfo, workspaceStatErr := os.Stat(workspacePath)
		if workspaceStatErr != nil {
			t.Fatal(workspaceStatErr)
		}
		identity, identityErr := applyPatchTxnIdentityFromFileInfo(workspaceInfo, "directory")
		if identityErr != nil {
			t.Fatal(identityErr)
		}
		digest, digestErr := applyPatchTxnWorkspaceIdentityDigest(identity)
		if digestErr != nil {
			t.Fatal(digestErr)
		}
		directory := filepath.Join(
			state.prepared.path, applyPatchTransactionWorkspacesDirectory, digest,
		)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(
			filepath.Join(state.prepared.path, applyPatchTransactionWorkspacesDirectory), 0o700,
		); err != nil {
			t.Fatal(err)
		}
		wrongBinding, bindingErr := encodeApplyPatchTransactionWorkspaceBinding(
			t.TempDir(), state.authentication,
		)
		if bindingErr != nil {
			t.Fatal(bindingErr)
		}
		if err := os.WriteFile(
			filepath.Join(directory, applyPatchTransactionWorkspaceBindingFile),
			wrongBinding, 0o600,
		); err != nil {
			t.Fatal(err)
		}
		if workspace, err := state.lockWorkspace(context.Background(), workspacePath); err == nil || workspace != nil {
			t.Fatalf("conflicting workspace binding = %#v, %v", workspace, err)
		}
	})
}
