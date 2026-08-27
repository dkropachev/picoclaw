//go:build linux

package tools

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	applyPatchKilledHelperModeEnvironment      = "PICOCLAW_APPLY_PATCH_KILLED_HELPER_MODE"
	applyPatchKilledHelperWorkspaceEnvironment = "PICOCLAW_APPLY_PATCH_KILLED_HELPER_WORKSPACE"
	applyPatchKilledHelperStateEnvironment     = "PICOCLAW_APPLY_PATCH_KILLED_HELPER_STATE"
	applyPatchKilledHelperReadyEnvironment     = "PICOCLAW_APPLY_PATCH_KILLED_HELPER_READY"

	applyPatchKilledHelperLockMode     = "lock"
	applyPatchKilledHelperPreparedMode = "prepared"
)

const applyPatchKilledHelperPatch = "*** Begin Patch\n" +
	"*** Update File: source.txt\n" +
	"@@\n" +
	"-before\n" +
	"+after\n" +
	"*** End Patch"

type applyPatchKilledHelperProcess struct {
	command *exec.Cmd
	done    chan struct{}
	waitErr error
	output  bytes.Buffer
}

func TestApplyPatchTransactionWorkspaceLockReleasedAfterKilledProcess(t *testing.T) {
	workspace := t.TempDir()
	stateRoot := filepath.Join(t.TempDir(), "transaction-state")
	prepared, err := prepareApplyPatchTransactionStateRoot(workspace, stateRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	readyPath := filepath.Join(t.TempDir(), "ready")
	helper := startApplyPatchKilledHelper(
		t,
		applyPatchKilledHelperLockMode,
		workspace,
		stateRoot,
		readyPath,
	)
	helper.waitReady(t, readyPath)

	state, err := openApplyPatchTransactionState(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	started := time.Now()
	contended, contentionErr := state.lockWorkspace(ctx, workspace)
	cancel()
	if contended != nil {
		_ = contended.Close()
	}
	if !errors.Is(contentionErr, context.DeadlineExceeded) {
		t.Fatalf("cross-process contention error = %v, want deadline", contentionErr)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("cross-process lock cancellation took %v", elapsed)
	}

	helper.kill(t)
	reacquireCtx, cancelReacquire := context.WithTimeout(context.Background(), 5*time.Second)
	reacquired, reacquireErr := state.lockWorkspace(reacquireCtx, workspace)
	cancelReacquire()
	if reacquireErr != nil {
		t.Fatalf("workspace lock after killed holder = %v", reacquireErr)
	}
	if err := reacquired.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyPatchTransactionRecoversPreparedStateAfterKilledProcess(t *testing.T) {
	workspace := t.TempDir()
	writeApplyPatchFixture(t, workspace, "source.txt", "before\n", 0o640)
	before := applyPatchSnapshotTree(t, workspace)
	stateRoot := filepath.Join(t.TempDir(), "transaction-state")
	prepared, err := prepareApplyPatchTransactionStateRoot(workspace, stateRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	readyPath := filepath.Join(t.TempDir(), "ready")
	helper := startApplyPatchKilledHelper(
		t,
		applyPatchKilledHelperPreparedMode,
		workspace,
		stateRoot,
		readyPath,
	)
	helper.waitReady(t, readyPath)
	if _, sourceErr := os.Lstat(filepath.Join(workspace, "source.txt")); !errors.Is(sourceErr, os.ErrNotExist) {
		t.Fatalf("killed helper did not quarantine its source: %v", sourceErr)
	}
	helper.kill(t)

	workspaceSnapshot, err := snapshotApplyPatchWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	tool, err := NewApplyPatchToolWithPermissionsAndPolicy(
		workspace,
		true,
		true,
		true,
		ApplyPatchPreflightPolicy{TransactionStateRoot: stateRoot},
	)
	if err != nil {
		t.Fatal(err)
	}
	state, err := openApplyPatchTransactionState(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	lockCtx, cancelLock := context.WithTimeout(context.Background(), 5*time.Second)
	workspaceState, err := state.lockWorkspace(lockCtx, workspaceSnapshot.canonical)
	cancelLock()
	if err != nil {
		t.Fatalf("lock workspace after prepared holder kill: %v", err)
	}
	defer workspaceState.Close()

	key, err := state.authenticationKey()
	if err != nil {
		t.Fatal(err)
	}
	store, journal, err := openApplyPatchTxnRecoveryStore(workspaceState, key[:])
	clear(key[:])
	if err != nil {
		t.Fatalf("open killed helper recovery state: %v", err)
	}
	if journal.Phase != applyPatchTransactionPhasePrepared || journal.DecisionAttempted {
		_ = store.Close()
		t.Fatalf(
			"killed helper journal = phase:%q attempted:%t, want prepared:false",
			journal.Phase,
			journal.DecisionAttempted,
		)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	recoveryCtx, cancelRecovery := context.WithTimeout(context.Background(), 5*time.Second)
	recoveryErr := tool.recoverApplyPatchTransaction(
		recoveryCtx,
		state,
		workspaceState,
		workspaceSnapshot,
	)
	cancelRecovery()
	if recoveryErr != nil {
		t.Fatalf("recover killed prepared transaction: %v", recoveryErr)
	}
	if err := workspaceState.withDirectoryAnchor(func(root *os.Root) error {
		return requireApplyPatchTxnWorkspaceReadyForNewTransaction(root)
	}); err != nil {
		t.Fatalf("workspace state after killed transaction recovery: %v", err)
	}
	assertApplyPatchTreeEqual(t, workspace, before)
	assertNoApplyPatchTxnWorkspaceResidue(t, workspace)
}

func TestApplyPatchTransactionKilledProcessHelper(t *testing.T) {
	mode := os.Getenv(applyPatchKilledHelperModeEnvironment)
	if mode == "" {
		return
	}
	workspace := os.Getenv(applyPatchKilledHelperWorkspaceEnvironment)
	stateRoot := os.Getenv(applyPatchKilledHelperStateEnvironment)
	readyPath := os.Getenv(applyPatchKilledHelperReadyEnvironment)
	if workspace == "" || stateRoot == "" || readyPath == "" {
		t.Fatal("killed apply-patch helper environment is incomplete")
	}

	switch mode {
	case applyPatchKilledHelperLockMode:
		prepared, err := prepareApplyPatchTransactionStateRoot(workspace, stateRoot, nil)
		if err != nil {
			t.Fatal(err)
		}
		state, err := openApplyPatchTransactionState(context.Background(), prepared)
		if err != nil {
			t.Fatal(err)
		}
		defer state.Close()
		locked, err := state.lockWorkspace(context.Background(), workspace)
		if err != nil {
			t.Fatal(err)
		}
		defer locked.Close()
		signalApplyPatchKilledHelperReady(t, readyPath)
		blockApplyPatchKilledHelper()
	case applyPatchKilledHelperPreparedMode:
		tool, err := NewApplyPatchToolWithPermissionsAndPolicy(
			workspace,
			true,
			true,
			true,
			ApplyPatchPreflightPolicy{TransactionStateRoot: stateRoot},
		)
		if err != nil {
			t.Fatal(err)
		}
		tool.transactionFault = func(boundary string) error {
			if boundary == "source_quarantine:0" {
				signalApplyPatchKilledHelperReady(t, readyPath)
				blockApplyPatchKilledHelper()
			}
			return nil
		}
		result := tool.Execute(
			context.Background(),
			map[string]any{"patch": applyPatchKilledHelperPatch},
		)
		t.Fatalf("prepared killed helper returned unexpectedly: %#v", result)
	default:
		t.Fatalf("unknown killed apply-patch helper mode %q", mode)
	}
}

func startApplyPatchKilledHelper(
	t *testing.T,
	mode string,
	workspace string,
	stateRoot string,
	readyPath string,
) *applyPatchKilledHelperProcess {
	t.Helper()
	helper := &applyPatchKilledHelperProcess{
		command: exec.Command(
			os.Args[0],
			"-test.run=^TestApplyPatchTransactionKilledProcessHelper$",
			"-test.count=1",
		),
		done: make(chan struct{}),
	}
	helper.command.Env = applyPatchKilledHelperEnvironment(
		mode,
		workspace,
		stateRoot,
		readyPath,
	)
	helper.command.Stdout = &helper.output
	helper.command.Stderr = &helper.output
	if err := helper.command.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		helper.waitErr = helper.command.Wait()
		close(helper.done)
	}()
	t.Cleanup(func() { helper.stop() })
	return helper
}

func (helper *applyPatchKilledHelperProcess) waitReady(t *testing.T, readyPath string) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		if _, err := os.Stat(readyPath); err == nil {
			select {
			case <-helper.done:
				t.Fatalf(
					"killed helper exited after readiness: %v\n%s",
					helper.waitErr,
					helper.output.String(),
				)
			default:
				return
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case <-helper.done:
			t.Fatalf(
				"killed helper exited before readiness: %v\n%s",
				helper.waitErr,
				helper.output.String(),
			)
		case <-ticker.C:
		case <-timer.C:
			helper.stop()
			t.Fatalf("timed out waiting for killed helper\n%s", helper.output.String())
		}
	}
}

func (helper *applyPatchKilledHelperProcess) kill(t *testing.T) {
	t.Helper()
	select {
	case <-helper.done:
		t.Fatalf(
			"killed helper exited before forced termination: %v\n%s",
			helper.waitErr,
			helper.output.String(),
		)
	default:
	}
	if err := helper.command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-helper.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for killed helper exit")
	}
	if helper.waitErr == nil {
		t.Fatal("forcibly killed helper exited successfully")
	}
}

func (helper *applyPatchKilledHelperProcess) stop() {
	if helper == nil || helper.command == nil || helper.command.Process == nil {
		return
	}
	select {
	case <-helper.done:
		return
	default:
	}
	_ = helper.command.Process.Kill()
	select {
	case <-helper.done:
	case <-time.After(5 * time.Second):
	}
}

func applyPatchKilledHelperEnvironment(
	mode string,
	workspace string,
	stateRoot string,
	readyPath string,
) []string {
	names := []string{
		applyPatchKilledHelperModeEnvironment,
		applyPatchKilledHelperWorkspaceEnvironment,
		applyPatchKilledHelperStateEnvironment,
		applyPatchKilledHelperReadyEnvironment,
	}
	environment := make([]string, 0, len(os.Environ())+len(names))
	for _, item := range os.Environ() {
		keep := true
		for _, name := range names {
			if strings.HasPrefix(item, name+"=") {
				keep = false
				break
			}
		}
		if keep {
			environment = append(environment, item)
		}
	}
	return append(
		environment,
		applyPatchKilledHelperModeEnvironment+"="+mode,
		applyPatchKilledHelperWorkspaceEnvironment+"="+workspace,
		applyPatchKilledHelperStateEnvironment+"="+stateRoot,
		applyPatchKilledHelperReadyEnvironment+"="+readyPath,
	)
}

func signalApplyPatchKilledHelperReady(t *testing.T, readyPath string) {
	t.Helper()
	file, err := os.OpenFile(readyPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.WriteString("ready\n")
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		t.Fatal(errors.Join(writeErr, syncErr, closeErr))
	}
}

func blockApplyPatchKilledHelper() {
	for {
		time.Sleep(time.Second)
	}
}
