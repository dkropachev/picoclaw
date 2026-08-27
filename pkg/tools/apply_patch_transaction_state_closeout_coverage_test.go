package tools

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyPatchTransactionStateCloseoutPathsAndBindings(t *testing.T) {
	workspace := t.TempDir()
	for _, root := range []string{" bad ", "bad\x00root"} {
		prepared, prepareErr := prepareApplyPatchTransactionStateRoot(workspace, root, nil)
		if prepareErr == nil || prepared.path != "" {
			t.Fatalf("invalid prepared root %q = %#v, %v", root, prepared, prepareErr)
		}
	}
	root := filepath.Join(t.TempDir(), "state")
	if _, err := prepareApplyPatchTransactionStateRoot(
		workspace, root, []string{"relative-allow-root"},
	); err == nil {
		t.Fatal("relative write-allow root succeeded")
	}
	if _, err := validateApplyPatchTransactionAllowRoot("relative"); err == nil {
		t.Fatal("relative allow root validation succeeded")
	}
	symlinkRoot := filepath.Join(t.TempDir(), "state-link")
	if err := os.Symlink(workspace, symlinkRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := captureApplyPatchTransactionStateFences(symlinkRoot); err == nil {
		t.Fatal("symlink state fence capture succeeded")
	}
	if err := revalidateApplyPatchTransactionStateFences(
		applyPatchTransactionStateRoot{},
	); err == nil {
		t.Fatal("empty state fences revalidated")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	prepared, err := prepareApplyPatchTransactionStateRoot(workspace, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	state, stateErr := openApplyPatchTransactionState(canceled, prepared)
	if !errors.Is(stateErr, context.Canceled) || state != nil {
		t.Fatalf("canceled state open = %#v, %v", state, stateErr)
	}

	var key [applyPatchTransactionAuthenticationBytes]byte
	valid, err := encodeApplyPatchTransactionWorkspaceBinding(workspace, key)
	if err != nil {
		t.Fatal(err)
	}
	for name, candidate := range map[string][]byte{
		"short":   nil,
		"bad mac": append(append([]byte(nil), valid[:len(valid)-1]...), valid[len(valid)-1]^0xff),
		"bad magic": resealApplyPatchTxnCloseoutBinding(
			t, valid, key, func(payload []byte) { payload[0] ^= 1 },
		),
		"bad path length": resealApplyPatchTxnCloseoutBinding(t, valid, key, func(payload []byte) {
			binary.BigEndian.PutUint32(payload[len(applyPatchTransactionWorkspaceBindingMagic):], 0)
		}),
		"relative path": resealApplyPatchTxnCloseoutBinding(t, valid, key, func(payload []byte) {
			pathStart := len(applyPatchTransactionWorkspaceBindingMagic) + 4
			for index := pathStart; index < len(payload); index++ {
				payload[index] = 'x'
			}
		}),
	} {
		t.Run(name, func(t *testing.T) {
			decoded, decodeErr := decodeApplyPatchTransactionWorkspaceBinding(candidate, key)
			if decodeErr == nil || decoded != "" {
				t.Fatalf("invalid binding decode = %q, %v", decoded, decodeErr)
			}
		})
	}
	if _, err := encodeApplyPatchTransactionWorkspaceBinding("relative", key); err == nil {
		t.Fatal("relative workspace binding encode succeeded")
	}
	if _, err := encodeApplyPatchTransactionWorkspaceBinding(
		string(os.PathSeparator)+strings.Repeat("x", applyPatchTransactionWorkspacePathLimit+1), key,
	); err == nil {
		t.Fatal("oversize workspace binding encode succeeded")
	}
}

func TestApplyPatchTransactionStateCloseoutPrivateFiles(t *testing.T) {
	directory := t.TempDir()
	root, rootOpenErr := os.OpenRoot(directory)
	if rootOpenErr != nil {
		t.Fatal(rootOpenErr)
	}
	defer root.Close()
	if err := publishApplyPatchTransactionPrivateRegular(
		nil, directory, "state", []byte("x"),
	); err == nil {
		t.Fatal("nil-root private publication succeeded")
	}
	if err := writeApplyPatchTransactionSyncedFile(nil, []byte("x")); err == nil {
		t.Fatal("nil private stage write succeeded")
	}
	closed, tempErr := os.CreateTemp(directory, "closed-")
	if tempErr != nil {
		t.Fatal(tempErr)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := writeApplyPatchTransactionSyncedFile(closed, []byte("x")); err == nil {
		t.Fatal("closed private stage write succeeded")
	}
	if err := cleanupApplyPatchTransactionPrivateStage(
		root, directory, "missing", []byte("x"), false,
	); err != nil {
		t.Fatalf("missing private stage cleanup = %v", err)
	}
	stageName := ".state.stage"
	if err := os.Mkdir(filepath.Join(directory, stageName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := cleanupApplyPatchTransactionPrivateStage(
		root, directory, "state", []byte("x"), false,
	); err == nil {
		t.Fatal("directory private stage cleanup succeeded")
	}
	if err := os.Remove(filepath.Join(directory, stageName)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, stageName), []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cleanupApplyPatchTransactionPrivateStage(
		root, directory, "state", []byte("x"), false,
	); err == nil {
		t.Fatal("different private stage cleanup succeeded")
	}
	if err := cleanupApplyPatchTransactionPrivateStage(
		root, directory, "state", []byte("x"), true,
	); err != nil {
		t.Fatalf("allowed-different private stage cleanup = %v", err)
	}

	if err := os.Mkdir(filepath.Join(directory, ".conflict.stage"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := publishApplyPatchTransactionPrivateRegular(
		root, directory, "conflict", []byte("x"),
	); err == nil {
		t.Fatal("directory publication stage succeeded")
	}
	if err := os.Remove(filepath.Join(directory, ".conflict.stage")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(directory, ".conflict.stage"), []byte("y"), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := publishApplyPatchTransactionPrivateRegular(
		root, directory, "conflict", []byte("x"),
	); err == nil {
		t.Fatal("different-content publication stage succeeded")
	}
	if err := os.Remove(filepath.Join(directory, ".conflict.stage")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(directory, ".auth.key.stage"), []byte("short"), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := publishApplyPatchTransactionPrivateRegular(
		root, directory, applyPatchTransactionAuthenticationFile,
		make([]byte, applyPatchTransactionAuthenticationBytes),
	); err != nil {
		t.Fatalf("stale authentication stage replacement = %v", err)
	}
	if err := publishApplyPatchTransactionPrivateRegular(
		root, directory, "published", []byte("data"),
	); err != nil {
		t.Fatal(err)
	}
	if err := publishApplyPatchTransactionPrivateRegular(
		root, directory, "published", []byte("data"),
	); err != nil {
		t.Fatalf("create-only loser cleanup = %v", err)
	}
	if _, _, err := readApplyPatchTransactionPrivateRegular(root, "published", 3); err == nil {
		t.Fatal("wrong private expected length succeeded")
	}
	info, err := root.Lstat("published")
	if err != nil {
		t.Fatal(err)
	}
	if err := removeApplyPatchTransactionExactRootEntry(nil, "published", info); err == nil {
		t.Fatal("nil exact-root removal succeeded")
	}
	if err := removeApplyPatchTransactionExactRootEntry(root, "published", nil); err == nil {
		t.Fatal("nil exact-root identity removal succeeded")
	}
	otherPath := filepath.Join(directory, "other")
	if err := os.WriteFile(otherPath, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	otherInfo, _ := os.Lstat(otherPath)
	if err := removeApplyPatchTransactionExactRootEntry(root, "published", otherInfo); err == nil {
		t.Fatal("wrong exact-root identity removal succeeded")
	}
}

func TestApplyPatchTransactionStateCloseoutDirectoriesAndBindingConflict(t *testing.T) {
	directory := t.TempDir()
	root, rootOpenErr := os.OpenRoot(directory)
	if rootOpenErr != nil {
		t.Fatal(rootOpenErr)
	}
	defer root.Close()
	invalidChild, invalidInfo, invalidErr := ensureApplyPatchTransactionPrivateDirectory(
		nil, directory, "child",
	)
	if invalidErr == nil || invalidChild != nil || invalidInfo != nil {
		t.Fatalf("nil private directory = %#v, %#v, %v", invalidChild, invalidInfo, invalidErr)
	}
	if err := os.WriteFile(filepath.Join(directory, "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	invalidChild, invalidInfo, invalidErr = ensureApplyPatchTransactionPrivateDirectory(
		root, directory, "file",
	)
	if invalidErr == nil || invalidChild != nil || invalidInfo != nil {
		t.Fatalf("regular private directory = %#v, %#v, %v", invalidChild, invalidInfo, invalidErr)
	}
	if err := os.Mkdir(filepath.Join(directory, "wrong-mode"), 0o755); err != nil {
		t.Fatal(err)
	}
	invalidChild, invalidInfo, invalidErr = ensureApplyPatchTransactionPrivateDirectory(
		root, directory, "wrong-mode",
	)
	if invalidErr == nil || invalidChild != nil || invalidInfo != nil {
		t.Fatalf("wrong-mode private directory = %#v, %#v, %v", invalidChild, invalidInfo, invalidErr)
	}
	child, childInfo, childErr := ensureApplyPatchTransactionPrivateDirectory(root, directory, "child")
	if childErr != nil {
		t.Fatal(childErr)
	}
	defer child.Close()
	if err := revalidateApplyPatchTransactionDirectory(nil, "child", childInfo); err == nil {
		t.Fatal("nil parent directory revalidation succeeded")
	}
	if err := os.Chmod(filepath.Join(directory, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := revalidateApplyPatchTransactionDirectory(root, "child", childInfo); err == nil {
		t.Fatal("changed private directory revalidated")
	}

	workspace := t.TempDir()
	var authentication [applyPatchTransactionAuthenticationBytes]byte
	wrongBinding, err := encodeApplyPatchTransactionWorkspaceBinding(t.TempDir(), authentication)
	if err != nil {
		t.Fatal(err)
	}
	if err := publishApplyPatchTransactionPrivateRegular(
		child,
		filepath.Join(directory, "child"),
		applyPatchTransactionWorkspaceBindingFile,
		wrongBinding,
	); err != nil {
		t.Fatal(err)
	}
	if err := ensureApplyPatchTransactionWorkspaceBinding(
		child,
		filepath.Join(directory, "child"),
		workspace,
		authentication,
	); err == nil {
		t.Fatal("conflicting workspace binding succeeded")
	}
}

func TestApplyPatchTransactionStateCloseoutCorruptionAndLifecycle(t *testing.T) {
	t.Run("authentication changed", func(t *testing.T) {
		state, _, _ := newApplyPatchTxnStateCloseout(t)
		authPath := filepath.Join(state.prepared.path, applyPatchTransactionAuthenticationFile)
		if err := os.WriteFile(
			authPath, make([]byte, applyPatchTransactionAuthenticationBytes), 0o600,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := state.authenticationKey(); err == nil {
			t.Fatal("changed authentication key revalidated")
		}
	})

	t.Run("init lock changed", func(t *testing.T) {
		state, _, _ := newApplyPatchTxnStateCloseout(t)
		lockPath := filepath.Join(state.prepared.path, applyPatchTransactionInitLockFile)
		if err := os.Chmod(lockPath, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := state.authenticationKeyID(); err == nil {
			t.Fatal("changed init lock revalidated")
		}
	})

	t.Run("named root moved", func(t *testing.T) {
		state, _, _ := newApplyPatchTxnStateCloseout(t)
		if err := os.Rename(state.prepared.path, state.prepared.path+"-moved"); err != nil {
			t.Fatal(err)
		}
		if _, err := state.rootIdentity(); err == nil {
			t.Fatal("moved state root revalidated")
		}
	})

	t.Run("closed state accessors", func(t *testing.T) {
		state, _, _ := newApplyPatchTxnStateCloseout(t)
		if err := state.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := state.authenticationKeyID(); err == nil {
			t.Fatal("closed key ID succeeded")
		}
		if _, err := state.rootIdentity(); err == nil {
			t.Fatal("closed root identity succeeded")
		}
		if err := state.withRootAnchor(func(*os.Root) error { return nil }); err == nil {
			t.Fatal("closed rooted operation succeeded")
		}
		if err := state.Close(); err != nil {
			t.Fatalf("duplicate state close = %v", err)
		}
	})

	t.Run("workspace overlap and invalid inputs", func(t *testing.T) {
		state, workspace, _ := newApplyPatchTxnStateCloseout(t)
		locked, lockErr := (*applyPatchTransactionState)(nil).lockWorkspace(
			context.Background(), workspace,
		)
		if lockErr == nil || locked != nil {
			t.Fatalf("nil state workspace lock = %#v, %v", locked, lockErr)
		}
		for _, invalid := range []string{"relative", filepath.Join(workspace, "missing")} {
			locked, lockErr = state.lockWorkspace(context.Background(), invalid)
			if lockErr == nil || locked != nil {
				t.Fatalf("invalid workspace %q = %#v, %v", invalid, locked, lockErr)
			}
		}
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		locked, lockErr = state.lockWorkspace(canceled, workspace)
		if !errors.Is(lockErr, context.Canceled) || locked != nil {
			t.Fatalf("canceled workspace lock = %#v, %v", locked, lockErr)
		}
		locked, lockErr = state.lockWorkspace(context.Background(), state.prepared.path)
		if lockErr == nil || locked != nil {
			t.Fatalf("overlapping workspace lock = %#v, %v", locked, lockErr)
		}
	})

	t.Run("active workspace lifecycle and corruption", func(t *testing.T) {
		state, workspacePath, _ := newApplyPatchTxnStateCloseout(t)
		workspace, err := state.lockWorkspace(context.Background(), workspacePath)
		if err != nil {
			t.Fatal(err)
		}
		if err := state.Close(); err == nil {
			t.Fatal("state closed with active workspace")
		}
		if err := os.Chmod(
			filepath.Join(workspace.absoluteDirectory, applyPatchTransactionWorkspaceLockFile),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := workspace.directoryRelative(); err == nil {
			t.Fatal("changed workspace lock revalidated")
		}
		if err := workspace.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := workspace.directoryPath(); err == nil {
			t.Fatal("closed workspace path succeeded")
		}
		if err := workspace.withDirectoryAnchor(func(*os.Root) error { return nil }); err == nil {
			t.Fatal("closed workspace rooted operation succeeded")
		}
		if err := workspace.Close(); err != nil {
			t.Fatalf("duplicate workspace close = %v", err)
		}
	})
}

func newApplyPatchTxnStateCloseout(
	t *testing.T,
) (*applyPatchTransactionState, string, applyPatchTransactionStateRoot) {
	t.Helper()
	workspace := t.TempDir()
	stateRoot := filepath.Join(t.TempDir(), "state")
	prepared, err := prepareApplyPatchTransactionStateRoot(workspace, stateRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	state, err := openApplyPatchTransactionState(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	return state, workspace, prepared
}

func resealApplyPatchTxnCloseoutBinding(
	t *testing.T,
	binding []byte,
	authentication [applyPatchTransactionAuthenticationBytes]byte,
	mutate func([]byte),
) []byte {
	t.Helper()
	payload := append([]byte(nil), binding[:len(binding)-sha256.Size]...)
	mutate(payload)
	mac := hmac.New(sha256.New, authentication[:])
	_, _ = mac.Write(payload)
	return mac.Sum(payload)
}
