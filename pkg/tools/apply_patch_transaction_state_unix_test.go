//go:build unix

package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newApplyPatchTransactionStateFixture(
	t *testing.T,
) (string, applyPatchTransactionStateRoot) {
	t.Helper()
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	canonicalWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareApplyPatchTransactionStateRoot(
		canonicalWorkspace,
		filepath.Join(parent, "state"),
		nil,
	)
	if err != nil {
		t.Fatalf("prepare state: %v", err)
	}
	return canonicalWorkspace, prepared
}

func TestApplyPatchTransactionStateInitializationAndReopen(t *testing.T) {
	workspace, prepared := newApplyPatchTransactionStateFixture(t)
	state, err := openApplyPatchTransactionState(context.Background(), prepared)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	key, err := state.authenticationKey()
	if err != nil {
		t.Fatalf("authentication key: %v", err)
	}
	keyID, err := state.authenticationKeyID()
	if err != nil || len(keyID) != 64 {
		t.Fatalf("key ID = %q, %v", keyID, err)
	}
	rootPath, err := state.rootPath()
	if err != nil || rootPath != prepared.path {
		t.Fatalf("root path = %q, %v", rootPath, err)
	}
	identity, err := state.rootIdentity()
	if err == nil && !identity.valid("directory") {
		t.Fatalf("root identity = %#v, %v", identity, err)
	}
	if err != nil && !errors.Is(err, errApplyPatchTransactionUnsupported) {
		t.Fatalf("root identity = %#v, %v", identity, err)
	}
	if err = state.withRootAnchor(func(root *os.Root) error {
		_, statErr := root.Lstat(applyPatchTransactionAuthenticationFile)
		return statErr
	}); err != nil {
		t.Fatalf("rooted key stat: %v", err)
	}
	if err = state.Close(); err != nil {
		t.Fatalf("close state: %v", err)
	}
	if state.authentication != [applyPatchTransactionAuthenticationBytes]byte{} {
		t.Fatal("state close did not clear authentication key")
	}
	if _, err = state.authenticationKey(); err == nil {
		t.Fatal("closed state returned authentication key")
	}
	if err = state.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}

	reopened, err := openApplyPatchTransactionState(context.Background(), prepared)
	if err != nil {
		t.Fatalf("reopen state: %v", err)
	}
	defer reopened.Close()
	reopenedKey, err := reopened.authenticationKey()
	if err != nil || reopenedKey != key {
		t.Fatalf("reopened key changed: %v", err)
	}
	if got, statErr := os.Stat(prepared.path); statErr != nil || got.Mode().Perm() != 0o700 {
		t.Fatalf("root mode = %v, %v", got, statErr)
	}
	for _, name := range []string{
		applyPatchTransactionInitLockFile,
		applyPatchTransactionAuthenticationFile,
	} {
		info, statErr := os.Stat(filepath.Join(prepared.path, name))
		if statErr != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v, %v", name, info, statErr)
		}
	}
	locked, err := reopened.lockWorkspace(context.Background(), workspace)
	if err != nil {
		t.Fatalf("reopened workspace lock: %v", err)
	}
	if err = locked.Close(); err != nil {
		t.Fatalf("close reopened workspace lock: %v", err)
	}
}

func TestApplyPatchTransactionConcurrentInitializationWinnerReread(t *testing.T) {
	_, prepared := newApplyPatchTransactionStateFixture(t)
	const workers = 12
	keys := make(chan [applyPatchTransactionAuthenticationBytes]byte, workers)
	errorsChannel := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			state, err := openApplyPatchTransactionState(context.Background(), prepared)
			if err != nil {
				errorsChannel <- err
				return
			}
			key, keyErr := state.authenticationKey()
			closeErr := state.Close()
			if keyErr != nil || closeErr != nil {
				errorsChannel <- errors.Join(keyErr, closeErr)
				return
			}
			keys <- key
		}()
	}
	group.Wait()
	close(keys)
	close(errorsChannel)
	for err := range errorsChannel {
		t.Errorf("initialize state: %v", err)
	}
	var winner [applyPatchTransactionAuthenticationBytes]byte
	for key := range keys {
		if winner == [applyPatchTransactionAuthenticationBytes]byte{} {
			winner = key
		} else if key != winner {
			t.Fatal("concurrent initializer did not reread one winner")
		}
	}
}

func TestApplyPatchTransactionPreCanceledInitializationHasNoEffect(t *testing.T) {
	_, prepared := newApplyPatchTransactionStateFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := openApplyPatchTransactionState(ctx, prepared); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled initialization error = %v", err)
	}
	if _, err := os.Lstat(prepared.path); !os.IsNotExist(err) {
		t.Fatalf("pre-canceled initialization created state: %v", err)
	}
}

func TestApplyPatchTransactionStateRejectsUnsafeObjectsAndDrift(t *testing.T) {
	workspace, prepared := newApplyPatchTransactionStateFixture(t)
	if err := os.Symlink(t.TempDir(), prepared.path); err == nil {
		if _, prepareErr := prepareApplyPatchTransactionStateRoot(workspace, prepared.path, nil); prepareErr == nil {
			t.Fatal("symlink state root was accepted")
		}
		if err = os.Remove(prepared.path); err != nil {
			t.Fatal(err)
		}
	}
	state, err := openApplyPatchTransactionState(context.Background(), prepared)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	if err = state.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(filepath.Join(prepared.path, applyPatchTransactionAuthenticationFile), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err = openApplyPatchTransactionState(context.Background(), prepared); err == nil {
		t.Fatal("world-readable authentication key was accepted")
	}
	if err = os.Chmod(filepath.Join(prepared.path, applyPatchTransactionAuthenticationFile), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err = openApplyPatchTransactionState(context.Background(), prepared)
	if err != nil {
		t.Fatalf("reopen state: %v", err)
	}
	renamed := prepared.path + "-renamed"
	if err = os.Rename(prepared.path, renamed); err != nil {
		t.Fatal(err)
	}
	if _, err = state.rootPath(); err == nil {
		t.Fatal("renamed state root retained named authority")
	}
	if err = state.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyPatchTransactionWorkspaceLockContentionAndIsolation(t *testing.T) {
	workspace, prepared := newApplyPatchTransactionStateFixture(t)
	stateA, err := openApplyPatchTransactionState(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	defer stateA.Close()
	stateB, err := openApplyPatchTransactionState(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	defer stateB.Close()

	lockA, err := stateA.lockWorkspace(context.Background(), workspace)
	if err != nil {
		t.Fatalf("lock A: %v", err)
	}
	if err = stateA.Close(); err == nil {
		t.Fatal("state closed while its workspace lock was active")
	}
	workspaceInfo, err := os.Stat(workspace)
	if err != nil {
		t.Fatal(err)
	}
	workspaceIdentity, err := applyPatchTxnIdentityFromFileInfo(workspaceInfo, "directory")
	if err != nil {
		t.Fatal(err)
	}
	physicalDigest, err := applyPatchTxnWorkspaceIdentityDigest(workspaceIdentity)
	if err != nil {
		t.Fatal(err)
	}
	wantRelative := filepath.ToSlash(filepath.Join(
		applyPatchTransactionWorkspacesDirectory,
		physicalDigest,
	))
	if got, pathErr := lockA.directoryRelative(); pathErr != nil || got != wantRelative {
		t.Fatalf("relative directory = %q, %v; want %q", got, pathErr, wantRelative)
	}
	if err = lockA.withDirectoryAnchor(func(root *os.Root) error {
		_, statErr := root.Lstat(applyPatchTransactionWorkspaceBindingFile)
		return statErr
	}); err != nil {
		t.Fatalf("workspace rooted binding stat: %v", err)
	}

	waitForApplyPatchTransactionLockCancellation(t, func(ctx context.Context) error {
		_, lockErr := stateB.lockWorkspace(ctx, workspace)
		return lockErr
	})
	workspaceAlias := filepath.Join(filepath.Dir(workspace), "workspace-alias")
	if err = os.Symlink(workspace, workspaceAlias); err == nil {
		waitForApplyPatchTransactionLockCancellation(t, func(ctx context.Context) error {
			_, lockErr := stateB.lockWorkspace(ctx, workspaceAlias)
			return lockErr
		})
	}

	unrelatedWorkspace := filepath.Join(filepath.Dir(workspace), "unrelated")
	if err = os.Mkdir(unrelatedWorkspace, 0o700); err != nil {
		t.Fatal(err)
	}
	lockB, err := stateB.lockWorkspace(context.Background(), unrelatedWorkspace)
	if err != nil {
		t.Fatalf("unrelated lock: %v", err)
	}
	if err = lockB.Close(); err != nil {
		t.Fatalf("close unrelated lock: %v", err)
	}
	waitForApplyPatchTransactionLockCancellation(t, func(ctx context.Context) error {
		_, lockErr := stateB.lockWorkspace(ctx, workspace)
		return lockErr
	})
	if err = lockA.Close(); err != nil {
		t.Fatalf("close lock A: %v", err)
	}
	lockAfterRelease, err := stateB.lockWorkspace(context.Background(), workspace)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	if err = lockAfterRelease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyPatchTransactionWorkspaceRenameKeepsPhysicalLockIdentity(t *testing.T) {
	workspace, prepared := newApplyPatchTransactionStateFixture(t)
	stateA, err := openApplyPatchTransactionState(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	defer stateA.Close()
	stateB, err := openApplyPatchTransactionState(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	defer stateB.Close()
	lockA, err := stateA.lockWorkspace(context.Background(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	renamed := workspace + "-renamed"
	if err := os.Rename(workspace, renamed); err != nil {
		_ = lockA.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if lockB, lockErr := stateB.lockWorkspace(ctx, renamed); lockB != nil ||
		!errors.Is(lockErr, context.DeadlineExceeded) {
		if lockB != nil {
			_ = lockB.Close()
		}
		_ = lockA.Close()
		t.Fatalf("renamed physical workspace bypassed live lock: lock=%v err=%v", lockB, lockErr)
	}
	if err := lockA.Close(); err != nil {
		t.Fatal(err)
	}
	lockB, lockErr := stateB.lockWorkspace(context.Background(), renamed)
	if lockB != nil {
		_ = lockB.Close()
	}
	if lockErr == nil {
		t.Fatalf("renamed workspace did not fail closed on old recovery binding: %v", lockErr)
	}
}

func TestApplyPatchTransactionWorkspaceBindingTamperFailsClosed(t *testing.T) {
	workspace, prepared := newApplyPatchTransactionStateFixture(t)
	state, err := openApplyPatchTransactionState(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	locked, err := state.lockWorkspace(context.Background(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := locked.directoryPath()
	if err != nil {
		t.Fatal(err)
	}
	if err = locked.Close(); err != nil {
		t.Fatal(err)
	}
	if err = state.Close(); err != nil {
		t.Fatal(err)
	}
	bindingPath := filepath.Join(directory, applyPatchTransactionWorkspaceBindingFile)
	binding, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	binding[len(binding)-1] ^= 1
	if err = os.WriteFile(bindingPath, binding, 0o600); err != nil {
		t.Fatal(err)
	}
	state, err = openApplyPatchTransactionState(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if _, err = state.lockWorkspace(context.Background(), workspace); err == nil ||
		!strings.Contains(err.Error(), "binding") {
		t.Fatalf("tampered binding error = %v", err)
	}
}

func TestApplyPatchTransactionWorkspaceAdmissionAndLockABA(t *testing.T) {
	workspace, prepared := newApplyPatchTransactionStateFixture(t)
	state, err := openApplyPatchTransactionState(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if _, err = state.lockWorkspace(
		context.Background(),
		filepath.Join(workspace, "missing"),
	); err == nil {
		t.Fatal("missing workspace was accepted")
	}
	insideState := filepath.Join(prepared.path, "inside-workspace")
	if err = os.Mkdir(insideState, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err = state.lockWorkspace(context.Background(), insideState); err == nil {
		t.Fatal("workspace overlapping transaction state was accepted")
	}

	locked, err := state.lockWorkspace(context.Background(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := locked.directoryPath()
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(directory, applyPatchTransactionWorkspaceLockFile)
	displacedPath := lockPath + ".displaced"
	if err = os.Rename(lockPath, displacedPath); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = locked.directoryPath(); err == nil {
		t.Fatal("replaced persistent lock file retained authority")
	}
	if err = locked.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyPatchTransactionInitLockCancellation(t *testing.T) {
	_, prepared := newApplyPatchTransactionStateFixture(t)
	state, err := openApplyPatchTransactionState(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	if err = state.Close(); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireApplyPatchTransactionFileLock(
		context.Background(),
		filepath.Join(prepared.path, applyPatchTransactionInitLockFile),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	waitForApplyPatchTransactionLockCancellation(t, func(ctx context.Context) error {
		_, openErr := openApplyPatchTransactionState(ctx, prepared)
		return openErr
	})
}
