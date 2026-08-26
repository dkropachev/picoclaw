package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func processTestOwner(label string) ProcessSessionOwner {
	return ProcessSessionOwner{
		AgentID:    "process-test-agent-" + label,
		SessionKey: "process-test-session-" + label,
	}
}

func processTestContext(owner ProcessSessionOwner) context.Context {
	ctx := WithToolContext(context.Background(), "cli", "process-test")
	return WithToolSessionContext(ctx, owner.AgentID, owner.SessionKey, nil)
}

func closedShellProcessTestWaitDone() chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}

func processTestSleepCommand(seconds int) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("Start-Sleep -Seconds %d", seconds)
	}
	return fmt.Sprintf("sleep %d", seconds)
}

func processTestOutputThenSleepCommand(output string, seconds int) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("Write-Output %q; Start-Sleep -Seconds %d", output, seconds)
	}
	return fmt.Sprintf("printf '%s\\n'; sleep %d", output, seconds)
}

func processTestCreateMarkerCommand(path string) string {
	if runtime.GOOS == "windows" {
		path = strings.ReplaceAll(path, "'", "''")
		return fmt.Sprintf(
			"New-Item -ItemType File -Force -LiteralPath '%s' | Out-Null",
			path,
		)
	}
	return fmt.Sprintf("touch %q", path)
}

type processTestHandle struct {
	closeCalls atomic.Int64
	onClose    func()
}

func (handle *processTestHandle) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (handle *processTestHandle) Write(data []byte) (int, error) {
	return len(data), nil
}

func (handle *processTestHandle) Close() error {
	handle.closeCalls.Add(1)
	if handle.onClose != nil {
		handle.onClose()
	}
	return nil
}

func installProcessTestManager(
	t *testing.T,
	tool *ExecTool,
	owners ...ProcessSessionOwner,
) *SessionManager {
	t.Helper()
	manager := NewSessionManager()
	tool.sessionManager = manager
	t.Cleanup(func() {
		cleanupProcessTestManager(t, manager, owners...)
		manager.Stop()
	})
	return manager
}

func cleanupProcessTestManager(
	t *testing.T,
	manager *SessionManager,
	owners ...ProcessSessionOwner,
) {
	t.Helper()
	if manager == nil {
		return
	}
	for _, owner := range owners {
		infos, err := manager.List(owner)
		if err != nil {
			t.Errorf("cleanup List(%#v): %v", owner, err)
			continue
		}
		for _, info := range infos {
			session, getErr := manager.Get(owner, info.ID)
			if getErr != nil {
				if !errors.Is(getErr, ErrSessionNotFound) {
					t.Errorf("cleanup Get(%q): %v", info.ID, getErr)
				}
				continue
			}
			if !session.IsDone() {
				if killErr := session.Kill(); killErr != nil && !errors.Is(killErr, ErrSessionDone) {
					t.Errorf("cleanup Kill(%q): %v", info.ID, killErr)
				}
			}
			awaitProcessTestWait(t, session, info.ID)
			if closer, ok := session.stdinWriter.(io.Closer); ok {
				_ = closer.Close()
			}
			if session.ptyMaster != nil {
				if closeErr := session.ptyMaster.Close(); closeErr != nil &&
					!errors.Is(closeErr, os.ErrClosed) {
					t.Errorf("cleanup PTY close(%q): %v", info.ID, closeErr)
				}
			}
			if removeErr := manager.Remove(owner, info.ID, session); removeErr != nil &&
				!errors.Is(removeErr, ErrSessionNotFound) {
				t.Errorf("cleanup Remove(%q): %v", info.ID, removeErr)
			}
		}
	}
}

func awaitProcessTestWait(t *testing.T, session *ProcessSession, sessionID string) {
	t.Helper()
	if session == nil || session.waitDone == nil {
		return
	}
	select {
	case <-session.waitDone:
	case <-time.After(5 * time.Second):
		t.Errorf("timed out waiting for process session %q to be reaped", sessionID)
	}
}

func startOwnedBackgroundProcess(
	t *testing.T,
	tool *ExecTool,
	ctx context.Context,
	command string,
	pty bool,
) ExecResponse {
	t.Helper()
	result := tool.Execute(ctx, map[string]any{
		"action":     "run",
		"command":    command,
		"background": true,
		"pty":        pty,
	})
	if result == nil || result.IsError {
		t.Fatalf("background run failed: %#v", result)
	}
	var response ExecResponse
	if err := json.Unmarshal([]byte(result.ForLLM), &response); err != nil {
		t.Fatalf("decode background response: %v; %q", err, result.ForLLM)
	}
	if response.SessionID == "" || response.Status != "running" {
		t.Fatalf("background response = %#v", response)
	}
	return response
}

func assertProcessAccessMatchesAbsent(
	t *testing.T,
	foreignTool *ExecTool,
	foreignCtx context.Context,
	absentTool *ExecTool,
	absentCtx context.Context,
	args map[string]any,
) {
	t.Helper()
	foreign := foreignTool.Execute(foreignCtx, cloneProcessTestArgs(args))
	absent := absentTool.Execute(absentCtx, cloneProcessTestArgs(args))
	if foreign == nil || absent == nil || !foreign.IsError || !absent.IsError ||
		foreign.ForLLM != absent.ForLLM || foreign.ForUser != absent.ForUser {
		t.Fatalf("foreign/absent mismatch: foreign=%#v absent=%#v args=%#v", foreign, absent, args)
	}
}

func cloneProcessTestArgs(args map[string]any) map[string]any {
	cloned := make(map[string]any, len(args))
	for key, value := range args {
		cloned[key] = value
	}
	return cloned
}

func TestProcessSessionOwnerRequiredOnlyForManagedActions(t *testing.T) {
	tool, err := NewExecTool(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	cleanupOwner := processTestOwner("missing-owner-cleanup")
	manager := installProcessTestManager(t, tool, cleanupOwner)
	tool.sessionIDGenerator = func() string { return "owner-required" }
	marker := filepath.Join(t.TempDir(), "must-not-exist")

	contexts := map[string]context.Context{
		"missing": context.Background(),
		"agent only": WithToolSessionContext(
			context.Background(), "agent-only", "", nil,
		),
		"session only": WithToolSessionContext(
			context.Background(), "", "session-only", nil,
		),
		"padded agent": WithToolSessionContext(
			context.Background(), " padded", "session", nil,
		),
	}
	for name, ctx := range contexts {
		t.Run(name, func(t *testing.T) {
			result := tool.Execute(ctx, map[string]any{
				"action":     "run",
				"command":    processTestCreateMarkerCommand(marker),
				"background": true,
			})
			if result == nil || !result.IsError ||
				!strings.Contains(result.ForLLM, ErrProcessSessionOwnerInvalid.Error()) {
				t.Fatalf("ownerless background result = %#v", result)
			}
		})
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ownerless background command ran: %v", err)
	}
	if sessions, err := manager.List(cleanupOwner); err != nil || len(sessions) != 0 {
		t.Fatalf("ownerless admission published sessions=%#v err=%v", sessions, err)
	}
	manager.mu.RLock()
	reservations := len(manager.reservations)
	manager.mu.RUnlock()
	if reservations != 0 {
		t.Fatalf("ownerless admission leaked %d reservations", reservations)
	}

	for _, args := range []map[string]any{
		{"action": "list"},
		{"action": "poll", "sessionId": "missing"},
		{"action": "read", "sessionId": "missing"},
		{"action": "write", "sessionId": "missing", "data": "x"},
		{"action": "kill", "sessionId": "missing"},
		{"action": "send-keys", "sessionId": "missing", "keys": "up"},
	} {
		result := tool.Execute(context.Background(), args)
		if result == nil || !result.IsError ||
			!strings.Contains(result.ForLLM, ErrProcessSessionOwnerInvalid.Error()) {
			t.Fatalf("ownerless managed action %#v = %#v", args, result)
		}
	}

	nativeSync := tool.Execute(context.Background(), map[string]any{
		"action": "run", "command": "echo ownerless-sync",
	})
	if nativeSync.IsError || !strings.Contains(nativeSync.ForLLM, "ownerless-sync") {
		t.Fatalf("ownerless native sync = %#v", nativeSync)
	}
	codexSync := NewCodexExecCommandTool(tool).Execute(context.Background(), map[string]any{
		"cmd": "echo ownerless-codex-sync",
	})
	if codexSync.IsError || !strings.Contains(codexSync.ForLLM, "ownerless-codex-sync") {
		t.Fatalf("ownerless Codex sync = %#v", codexSync)
	}
}

func TestProcessSessionNativeNonPTYOwnerIsolation(t *testing.T) {
	tool, err := NewExecTool(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	ownerA := processTestOwner("native-a")
	ownerB := processTestOwner("native-b")
	manager := installProcessTestManager(t, tool, ownerA, ownerB)
	ctxA, ctxB := processTestContext(ownerA), processTestContext(ownerB)

	absentTool, err := NewExecTool(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	installProcessTestManager(t, absentTool, ownerB)
	absentCtx := processTestContext(ownerB)

	started := startOwnedBackgroundProcess(
		t, tool, ctxA, processTestOutputThenSleepCommand("owner-output", 30), false,
	)
	if write := tool.Execute(ctxA, map[string]any{
		"action": "write", "sessionId": started.SessionID, "data": "owner-input\n",
	}); write.IsError {
		t.Fatalf("owner write = %#v", write)
	}
	time.Sleep(100 * time.Millisecond)

	for _, args := range []map[string]any{
		{"action": "poll", "sessionId": started.SessionID},
		{"action": "read", "sessionId": started.SessionID},
		{"action": "write", "sessionId": started.SessionID, "data": "foreign-input\n"},
		{"action": "send-keys", "sessionId": started.SessionID, "keys": "up"},
		{"action": "kill", "sessionId": started.SessionID},
	} {
		assertProcessAccessMatchesAbsent(t, tool, ctxB, absentTool, absentCtx, args)
	}
	listB := tool.Execute(ctxB, map[string]any{"action": "list"})
	if listB.IsError || strings.Contains(listB.ForLLM, started.SessionID) {
		t.Fatalf("foreign list exposed owner A: %#v", listB)
	}
	if sessions, listErr := manager.List(ownerA); listErr != nil || len(sessions) != 1 {
		t.Fatalf("owner A list = %#v err=%v", sessions, listErr)
	}

	read := tool.Execute(ctxA, map[string]any{"action": "read", "sessionId": started.SessionID})
	if read.IsError || !strings.Contains(read.ForLLM, "owner-output") ||
		strings.Contains(read.ForLLM, "foreign-input") {
		t.Fatalf("owner output after foreign probes = %#v", read)
	}
	poll := tool.Execute(ctxA, map[string]any{"action": "poll", "sessionId": started.SessionID})
	if poll.IsError || !strings.Contains(poll.ForLLM, `"status":"running"`) {
		t.Fatalf("foreign kill affected owner process: %#v", poll)
	}
	session, err := manager.Get(ownerA, started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	killed := tool.Execute(ctxA, map[string]any{"action": "kill", "sessionId": started.SessionID})
	if killed.IsError {
		t.Fatalf("owner kill = %#v", killed)
	}
	select {
	case <-session.waitDone:
	default:
		t.Fatal("kill returned before the sole Wait owner completed")
	}
}

func TestProcessSessionPTYOwnerIsolationAndKeyMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PTY is not supported on Windows")
	}
	tool, err := NewExecTool(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	ownerA := processTestOwner("pty-a")
	ownerB := processTestOwner("pty-b")
	manager := installProcessTestManager(t, tool, ownerA, ownerB)
	ctxA, ctxB := processTestContext(ownerA), processTestContext(ownerB)
	absentTool, err := NewExecTool(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	installProcessTestManager(t, absentTool, ownerB)
	absentCtx := processTestContext(ownerB)

	started := startOwnedBackgroundProcess(t, tool, ctxA, "cat", true)
	session, err := manager.Get(ownerA, started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	session.SetPtyKeyMode(PtyKeyModeSS3)
	for _, args := range []map[string]any{
		{"action": "poll", "sessionId": started.SessionID},
		{"action": "read", "sessionId": started.SessionID},
		{"action": "write", "sessionId": started.SessionID, "data": "foreign-pty\n"},
		{"action": "send-keys", "sessionId": started.SessionID, "keys": "up"},
		{"action": "kill", "sessionId": started.SessionID},
	} {
		assertProcessAccessMatchesAbsent(t, tool, ctxB, absentTool, absentCtx, args)
	}
	if session.GetPtyKeyMode() != PtyKeyModeSS3 {
		t.Fatal("foreign send-keys observed or changed PTY key mode")
	}
	if write := tool.Execute(ctxA, map[string]any{
		"action": "write", "sessionId": started.SessionID, "data": "owner-pty\n",
	}); write.IsError {
		t.Fatalf("owner PTY write = %#v", write)
	}
	time.Sleep(100 * time.Millisecond)
	read := tool.Execute(ctxA, map[string]any{"action": "read", "sessionId": started.SessionID})
	if read.IsError || !strings.Contains(read.ForLLM, "owner-pty") ||
		strings.Contains(read.ForLLM, "foreign-pty") {
		t.Fatalf("owner PTY output after foreign probes = %#v", read)
	}
	if killed := tool.Execute(ctxA, map[string]any{
		"action": "kill", "sessionId": started.SessionID,
	}); killed.IsError {
		t.Fatalf("owner PTY kill = %#v", killed)
	}
}

func TestCodexProcessSessionOwnerIsolation(t *testing.T) {
	execTool, err := NewExecTool(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	ownerA := processTestOwner("codex-a")
	ownerB := processTestOwner("codex-b")
	installProcessTestManager(t, execTool, ownerA, ownerB)
	ctxA, ctxB := processTestContext(ownerA), processTestContext(ownerB)

	absentExec, err := NewExecTool(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	installProcessTestManager(t, absentExec, ownerB)

	background := NewCodexExecCommandTool(execTool).Execute(ctxA, map[string]any{
		"cmd": processTestSleepCommand(30), "background": true,
	})
	if background.IsError {
		t.Fatalf("Codex background = %#v", background)
	}
	var started codexSessionResponse
	if err := json.Unmarshal([]byte(background.ForLLM), &started); err != nil {
		t.Fatal(err)
	}
	foreign := NewCodexWriteStdinTool(execTool).Execute(ctxB, map[string]any{
		"session_id": started.SessionID, "chars": "foreign\n",
	})
	absent := NewCodexWriteStdinTool(absentExec).Execute(ctxB, map[string]any{
		"session_id": started.SessionID, "chars": "foreign\n",
	})
	if !foreign.IsError || !absent.IsError || foreign.ForLLM != absent.ForLLM {
		t.Fatalf("Codex foreign/absent mismatch: %#v / %#v", foreign, absent)
	}
	written := NewCodexWriteStdinTool(execTool).Execute(ctxA, map[string]any{
		"session_id": started.SessionID, "chars": "owner\n",
	})
	if written.IsError || !strings.Contains(written.ForLLM, `"session_id"`) {
		t.Fatalf("Codex owner write = %#v", written)
	}
	if killed := execTool.Execute(ctxA, map[string]any{
		"action": "kill", "sessionId": started.SessionID,
	}); killed.IsError {
		t.Fatalf("Codex session owner kill = %#v", killed)
	}
}

func TestProcessSessionSharedExecToolsUseExactOwner(t *testing.T) {
	first, err := NewExecTool(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewExecTool(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if first.sessionManager == nil || first.sessionManager != second.sessionManager ||
		first.sessionManager != getSessionManager() {
		t.Fatal("new exec tools did not borrow the same production session manager")
	}
	owner := processTestOwner("shared-owner")
	foreign := processTestOwner("shared-foreign")
	manager := installProcessTestManager(t, first, owner, foreign)
	second.sessionManager = manager
	started := startOwnedBackgroundProcess(
		t, first, processTestContext(owner), processTestSleepCommand(30), false,
	)
	if write := second.Execute(processTestContext(owner), map[string]any{
		"action": "write", "sessionId": started.SessionID, "data": "shared\n",
	}); write.IsError {
		t.Fatalf("second tool owner write = %#v", write)
	}
	if poll := second.Execute(processTestContext(foreign), map[string]any{
		"action": "poll", "sessionId": started.SessionID,
	}); !poll.IsError || !strings.Contains(poll.ForLLM, "session not found") {
		t.Fatalf("second tool foreign poll = %#v", poll)
	}
	if killed := second.Execute(processTestContext(owner), map[string]any{
		"action": "kill", "sessionId": started.SessionID,
	}); killed.IsError {
		t.Fatalf("second tool owner kill = %#v", killed)
	}
}

func TestProcessSessionSetupFailuresCloseAcquiredHandles(t *testing.T) {
	type setupFailureCase struct {
		name           string
		configure      func(*backgroundProcessOperations, *processTestHandle, *processTestHandle, *processTestHandle)
		wantCloseCalls [3]int64
		wantStartCalls int64
	}
	tests := []setupFailureCase{
		{
			name: "stderr-pipe",
			configure: func(
				operations *backgroundProcessOperations,
				stdout, _, _ *processTestHandle,
			) {
				operations.stdoutPipe = func(*exec.Cmd) (io.ReadCloser, error) {
					return stdout, nil
				}
				operations.stderrPipe = func(*exec.Cmd) (io.ReadCloser, error) {
					return nil, errors.New("injected stderr pipe failure")
				}
			},
			wantCloseCalls: [3]int64{1, 0, 0},
		},
		{
			name: "stdin-pipe",
			configure: func(
				operations *backgroundProcessOperations,
				stdout, stderr, _ *processTestHandle,
			) {
				operations.stdoutPipe = func(*exec.Cmd) (io.ReadCloser, error) {
					return stdout, nil
				}
				operations.stderrPipe = func(*exec.Cmd) (io.ReadCloser, error) {
					return stderr, nil
				}
				operations.stdinPipe = func(*exec.Cmd) (io.WriteCloser, error) {
					return nil, errors.New("injected stdin pipe failure")
				}
			},
			wantCloseCalls: [3]int64{1, 1, 0},
		},
		{
			name: "process-start",
			configure: func(
				operations *backgroundProcessOperations,
				stdout, stderr, stdin *processTestHandle,
			) {
				operations.stdoutPipe = func(*exec.Cmd) (io.ReadCloser, error) {
					return stdout, nil
				}
				operations.stderrPipe = func(*exec.Cmd) (io.ReadCloser, error) {
					return stderr, nil
				}
				operations.stdinPipe = func(*exec.Cmd) (io.WriteCloser, error) {
					return stdin, nil
				}
			},
			wantCloseCalls: [3]int64{1, 1, 1},
			wantStartCalls: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tool, err := NewExecTool(t.TempDir(), false)
			if err != nil {
				t.Fatal(err)
			}
			owner := processTestOwner("setup-" + test.name)
			manager := installProcessTestManager(t, tool, owner)
			tool.sessionIDGenerator = func() string { return "setup-" + test.name }
			stdout := &processTestHandle{}
			stderr := &processTestHandle{}
			stdin := &processTestHandle{}
			var startCalls atomic.Int64
			operations := backgroundProcessOperations{
				start: func(*exec.Cmd) error {
					startCalls.Add(1)
					return errors.New("injected process start failure")
				},
			}
			test.configure(&operations, stdout, stderr, stdin)
			tool.backgroundOps = &operations

			result := tool.Execute(processTestContext(owner), map[string]any{
				"action": "run", "command": "echo setup", "background": true,
			})
			if result == nil || !result.IsError {
				t.Fatalf("injected setup failure result = %#v", result)
			}
			gotCloseCalls := [3]int64{
				stdout.closeCalls.Load(),
				stderr.closeCalls.Load(),
				stdin.closeCalls.Load(),
			}
			if gotCloseCalls != test.wantCloseCalls || startCalls.Load() != test.wantStartCalls {
				t.Fatalf(
					"cleanup calls close=%v start=%d; want close=%v start=%d",
					gotCloseCalls,
					startCalls.Load(),
					test.wantCloseCalls,
					test.wantStartCalls,
				)
			}
			if sessions, listErr := manager.List(owner); listErr != nil || len(sessions) != 0 {
				t.Fatalf("setup failure sessions=%#v err=%v", sessions, listErr)
			}
			manager.mu.RLock()
			reservations := len(manager.reservations)
			manager.mu.RUnlock()
			if reservations != 0 {
				t.Fatalf("setup failure leaked %d reservations", reservations)
			}
		})
	}

	t.Run("pty-process-start", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("PTY is not supported on Windows")
		}
		tool, err := NewExecTool(t.TempDir(), false)
		if err != nil {
			t.Fatal(err)
		}
		owner := processTestOwner("setup-pty-start")
		manager := installProcessTestManager(t, tool, owner)
		ptyMaster, ptySlave, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		tool.backgroundOps = &backgroundProcessOperations{
			openPTY: func() (*os.File, *os.File, error) {
				return ptyMaster, ptySlave, nil
			},
			start: func(*exec.Cmd) error {
				return errors.New("injected PTY start failure")
			},
		}
		result := tool.Execute(processTestContext(owner), map[string]any{
			"action": "run", "command": "echo setup", "background": true, "pty": true,
		})
		if result == nil || !result.IsError {
			t.Fatalf("injected PTY setup failure result = %#v", result)
		}
		if _, statErr := ptyMaster.Stat(); statErr == nil {
			t.Fatal("PTY master remained open after start failure")
		}
		if _, statErr := ptySlave.Stat(); statErr == nil {
			t.Fatal("PTY slave remained open after start failure")
		}
		if sessions, listErr := manager.List(owner); listErr != nil || len(sessions) != 0 {
			t.Fatalf("PTY setup failure sessions=%#v err=%v", sessions, listErr)
		}
	})
}

func TestProcessSessionPromotionFailureClosesHandlesAndWaitsOnce(t *testing.T) {
	tool, err := NewExecTool(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	owner := processTestOwner("promotion-lifecycle")
	manager := installProcessTestManager(t, tool, owner)
	tool.sessionIDGenerator = func() string { return "promotion-lifecycle" }

	events := make([]string, 0, 5)
	stdout := &processTestHandle{onClose: func() { events = append(events, "stdout") }}
	stderr := &processTestHandle{onClose: func() { events = append(events, "stderr") }}
	stdin := &processTestHandle{onClose: func() { events = append(events, "stdin") }}
	var startCalls atomic.Int64
	var terminateCalls atomic.Int64
	var waitCalls atomic.Int64
	tool.backgroundOps = &backgroundProcessOperations{
		stdoutPipe: func(*exec.Cmd) (io.ReadCloser, error) { return stdout, nil },
		stderrPipe: func(*exec.Cmd) (io.ReadCloser, error) { return stderr, nil },
		stdinPipe:  func(*exec.Cmd) (io.WriteCloser, error) { return stdin, nil },
		start: func(cmd *exec.Cmd) error {
			startCalls.Add(1)
			cmd.Process = &os.Process{Pid: 424242}
			return nil
		},
		terminate: func(*exec.Cmd) error {
			terminateCalls.Add(1)
			events = append(events, "terminate")
			return nil
		},
		wait: func(*exec.Cmd) error {
			waitCalls.Add(1)
			events = append(events, "wait")
			return nil
		},
	}
	tool.beforePromotion = func(token *processSessionReservation) {
		if !manager.releaseReservation(token) {
			t.Error("failed to invalidate lifecycle-test reservation")
		}
	}

	result := tool.Execute(processTestContext(owner), map[string]any{
		"action": "run", "command": "echo promotion", "background": true,
	})
	if result == nil || !result.IsError ||
		!strings.Contains(result.ForLLM, ErrSessionReservationInvalid.Error()) {
		t.Fatalf("promotion lifecycle failure result = %#v", result)
	}
	if startCalls.Load() != 1 || terminateCalls.Load() != 1 || waitCalls.Load() != 1 {
		t.Fatalf(
			"promotion lifecycle calls start=%d terminate=%d wait=%d",
			startCalls.Load(),
			terminateCalls.Load(),
			waitCalls.Load(),
		)
	}
	for name, handle := range map[string]*processTestHandle{
		"stdout": stdout,
		"stderr": stderr,
		"stdin":  stdin,
	} {
		if calls := handle.closeCalls.Load(); calls != 1 {
			t.Fatalf("%s close calls = %d, want 1", name, calls)
		}
	}
	wantEvents := []string{"terminate", "stdin", "stdout", "stderr", "wait"}
	if fmt.Sprint(events) != fmt.Sprint(wantEvents) {
		t.Fatalf("promotion cleanup order = %v, want %v", events, wantEvents)
	}
	if sessions, listErr := manager.List(owner); listErr != nil || len(sessions) != 0 {
		t.Fatalf("promotion lifecycle sessions=%#v err=%v", sessions, listErr)
	}
	manager.mu.RLock()
	reservations := len(manager.reservations)
	manager.mu.RUnlock()
	if reservations != 0 {
		t.Fatalf("promotion lifecycle leaked %d reservations", reservations)
	}
}

func TestProcessSessionPTYWaitPanicFinalizesBeforeCompletion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PTY is not supported on Windows")
	}
	tool, err := NewExecTool(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	owner := processTestOwner("pty-wait-panic")
	manager := installProcessTestManager(t, tool, owner)
	tool.sessionIDGenerator = func() string { return "pty-wait-panic" }
	ptyMaster, ptySlave, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = ptyMaster.Close()
		_ = ptySlave.Close()
	})
	var waitCalls atomic.Int64
	var terminateCalls atomic.Int64
	tool.backgroundOps = &backgroundProcessOperations{
		openPTY: func() (*os.File, *os.File, error) {
			return ptyMaster, ptySlave, nil
		},
		start: func(cmd *exec.Cmd) error {
			cmd.Process = &os.Process{Pid: 424242}
			_, writeErr := ptySlave.WriteString("wait-panic-output\n")
			return writeErr
		},
		terminate: func(*exec.Cmd) error {
			terminateCalls.Add(1)
			return nil
		},
		wait: func(*exec.Cmd) error {
			waitCalls.Add(1)
			panic("injected PTY Wait panic")
		},
	}

	started := startOwnedBackgroundProcess(
		t,
		tool,
		processTestContext(owner),
		"echo ignored-by-injected-process-operations",
		true,
	)
	session, err := manager.Get(owner, started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	awaitProcessTestWait(t, session, started.SessionID)
	if waitCalls.Load() != 1 || terminateCalls.Load() != 1 {
		t.Fatalf(
			"panic lifecycle calls wait=%d terminate=%d, want 1 each",
			waitCalls.Load(),
			terminateCalls.Load(),
		)
	}
	if !session.IsDone() || session.GetStatus() != "done" || session.GetExitCode() != -1 {
		t.Fatalf(
			"panic lifecycle state done=%v status=%q exit=%d",
			session.IsDone(),
			session.GetStatus(),
			session.GetExitCode(),
		)
	}
	if _, statErr := session.ptyMaster.Stat(); statErr == nil {
		t.Fatal("waitDone closed before the PTY master was closed")
	}
	if output := session.Read(); !strings.Contains(output, "wait-panic-output") {
		t.Fatalf("panic lifecycle lost buffered output: %q", output)
	}
	time.Sleep(20 * time.Millisecond)
	if output := session.Read(); output != "" {
		t.Fatalf("PTY reader appended output after waitDone: %q", output)
	}
}

func TestProcessSessionPTYDrainTimeoutBoundsDetachedSlave(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PTY is not supported on Windows")
	}
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid is required for the detached PTY regression")
	}
	tool, err := NewExecTool(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	owner := processTestOwner("pty-detached-slave")
	manager := installProcessTestManager(t, tool, owner)
	tool.sessionIDGenerator = func() string { return "pty-detached-slave" }
	tool.ptyDrainTimeout = 250 * time.Millisecond
	realWait := tool.resolveBackgroundProcessOperations().wait
	waitReturned := make(chan struct{})
	tool.backgroundOps = &backgroundProcessOperations{
		wait: func(cmd *exec.Cmd) error {
			defer close(waitReturned)
			return realWait(cmd)
		},
	}
	pidFile := filepath.Join(t.TempDir(), "detached.pid")
	t.Cleanup(func() {
		data, readErr := os.ReadFile(pidFile)
		if readErr != nil {
			return
		}
		pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
		if parseErr != nil {
			return
		}
		process, findErr := os.FindProcess(pid)
		if findErr == nil {
			_ = process.Kill()
		}
	})

	startedAt := time.Now()
	started := startOwnedBackgroundProcess(
		t,
		tool,
		processTestContext(owner),
		fmt.Sprintf("setsid sleep 5 & echo $! > %q", pidFile),
		true,
	)
	session, err := manager.Get(owner, started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-waitReturned:
	case <-time.After(time.Second):
		t.Fatal("tracked command Wait did not return before detached slave drain")
	}
	processExitDeadline := time.Now().Add(100 * time.Millisecond)
	for !session.IsDone() && time.Now().Before(processExitDeadline) {
		time.Sleep(time.Millisecond)
	}
	if !session.IsDone() || session.GetStatus() != "running" {
		t.Fatalf(
			"tracked exit was not fenced during drain: done=%v status=%q",
			session.IsDone(),
			session.GetStatus(),
		)
	}
	if writeErr := session.Write("must-not-reach-detached-slave"); !errors.Is(writeErr, ErrSessionDone) {
		t.Fatalf("write to reaped PTY session error = %v, want ErrSessionDone", writeErr)
	}
	killResult := tool.Execute(processTestContext(owner), map[string]any{
		"action": "kill", "sessionId": started.SessionID,
	})
	if killResult == nil || !killResult.IsError ||
		!strings.Contains(killResult.ForLLM, "process already exited") {
		t.Fatalf("kill targeted a reaped PID during PTY drain: %#v", killResult)
	}
	select {
	case <-session.waitDone:
	case <-time.After(2 * time.Second):
		waitState := "pending"
		select {
		case <-waitReturned:
			waitState = "returned"
		default:
		}
		t.Fatalf(
			"tracked PTY completion waited for a detached slave holder; cmd.Wait=%s status=%q",
			waitState,
			session.GetStatus(),
		)
	}
	if elapsed := time.Since(startedAt); elapsed >= 2*time.Second {
		t.Fatalf("bounded PTY drain took %v", elapsed)
	}
	if !session.IsDone() || session.GetStatus() != "done" {
		t.Fatalf("bounded PTY drain status = %q", session.GetStatus())
	}
	if _, statErr := session.ptyMaster.Stat(); statErr == nil {
		t.Fatal("bounded PTY drain left the master open")
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read detached PID: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse detached PID: %v", err)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("find detached process: %v", err)
	}
	if err := process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("detached slave holder did not outlive tracked command: %v", err)
	}
}

func TestProcessSessionIDReservationCollisionAndExhaustion(t *testing.T) {
	t.Run("collision then success", func(t *testing.T) {
		tool, err := NewExecTool(t.TempDir(), false)
		if err != nil {
			t.Fatal(err)
		}
		owner := processTestOwner("collision")
		manager := installProcessTestManager(t, tool, owner)
		collision, err := manager.reserveID(owner, "collision-id")
		if err != nil {
			t.Fatal(err)
		}
		defer manager.releaseReservation(collision)
		var calls atomic.Int64
		tool.sessionIDGenerator = func() string {
			if calls.Add(1) == 1 {
				return "collision-id"
			}
			return "unique-id"
		}
		started := startOwnedBackgroundProcess(
			t, tool, processTestContext(owner), processTestSleepCommand(30), false,
		)
		if started.SessionID != "unique-id" || calls.Load() != 2 {
			t.Fatalf("collision result=%#v generator calls=%d", started, calls.Load())
		}
		if killed := tool.Execute(processTestContext(owner), map[string]any{
			"action": "kill", "sessionId": started.SessionID,
		}); killed.IsError {
			t.Fatalf("kill unique session = %#v", killed)
		}
	})

	t.Run("fixed exhaustion before start", func(t *testing.T) {
		tool, err := NewExecTool(t.TempDir(), false)
		if err != nil {
			t.Fatal(err)
		}
		owner := processTestOwner("exhaustion")
		manager := installProcessTestManager(t, tool, owner)
		collision, err := manager.reserveID(owner, "always-collides")
		if err != nil {
			t.Fatal(err)
		}
		defer manager.releaseReservation(collision)
		var calls atomic.Int64
		tool.sessionIDGenerator = func() string {
			calls.Add(1)
			return "always-collides"
		}
		marker := filepath.Join(t.TempDir(), "not-started")
		result := tool.Execute(processTestContext(owner), map[string]any{
			"action": "run", "command": processTestCreateMarkerCommand(marker), "background": true,
		})
		if result == nil || !result.IsError || calls.Load() != processSessionIDReservationAttempts {
			t.Fatalf("exhaustion result=%#v calls=%d", result, calls.Load())
		}
		if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("collision exhaustion started process: %v", err)
		}
		if sessions, err := manager.List(owner); err != nil || len(sessions) != 0 {
			t.Fatalf("exhaustion sessions=%#v err=%v", sessions, err)
		}
	})
}

func TestProcessSessionPromotionFailureCleansStartedProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell cleanup canary")
	}
	tool, err := NewExecTool(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	owner := processTestOwner("promotion-failure")
	manager := installProcessTestManager(t, tool, owner)
	tool.sessionIDGenerator = func() string { return "promotion-failure" }
	tool.beforePromotion = func(token *processSessionReservation) {
		if !manager.releaseReservation(token) {
			t.Error("failed to invalidate reservation before promotion")
		}
	}
	marker := filepath.Join(t.TempDir(), "leaked-process")
	result := tool.Execute(processTestContext(owner), map[string]any{
		"action":     "run",
		"command":    fmt.Sprintf("sleep 1; echo leaked > %q", marker),
		"background": true,
	})
	if result == nil || !result.IsError ||
		!strings.Contains(result.ForLLM, ErrSessionReservationInvalid.Error()) {
		t.Fatalf("promotion failure result = %#v", result)
	}
	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("promotion failure leaked process: %v", err)
	}
	if sessions, err := manager.List(owner); err != nil || len(sessions) != 0 {
		t.Fatalf("promotion failure sessions=%#v err=%v", sessions, err)
	}
	manager.mu.RLock()
	reservations := len(manager.reservations)
	manager.mu.RUnlock()
	if reservations != 0 {
		t.Fatalf("promotion failure leaked %d reservations", reservations)
	}
}

func TestProcessSessionKillABACannotRemoveReplacement(t *testing.T) {
	tool, err := NewExecTool(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	owner := processTestOwner("kill-aba")
	manager := installProcessTestManager(t, tool, owner)
	tool.sessionIDGenerator = func() string { return "kill-aba-id" }
	started := startOwnedBackgroundProcess(
		t, tool, processTestContext(owner), processTestSleepCommand(30), false,
	)
	oldSession, err := manager.Get(owner, started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	replacement := &ProcessSession{
		ID: started.SessionID, PID: 1, Command: "replacement", Background: true,
		StartTime: time.Now().Unix(), Status: "done", outputBuffer: &bytes.Buffer{},
		waitDone: closedShellProcessTestWaitDone(),
	}
	var hookErr error
	tool.beforeSessionRemove = func(gotOwner ProcessSessionOwner, id string, got *ProcessSession) {
		if gotOwner != owner || id != started.SessionID || got != oldSession {
			hookErr = fmt.Errorf("unexpected pre-remove identity")
			return
		}
		if removeErr := manager.Remove(owner, id, oldSession); removeErr != nil {
			hookErr = removeErr
			return
		}
		hookErr = manager.Add(owner, replacement)
	}
	result := tool.Execute(processTestContext(owner), map[string]any{
		"action": "kill", "sessionId": started.SessionID,
	})
	if hookErr != nil {
		t.Fatalf("ABA hook error = %v", hookErr)
	}
	if result == nil || !result.IsError || !strings.Contains(result.ForLLM, ErrSessionStale.Error()) {
		t.Fatalf("ABA kill result = %#v", result)
	}
	got, err := manager.Get(owner, started.SessionID)
	if err != nil || got != replacement {
		t.Fatalf("replacement deleted by stale remove: got=%p want=%p err=%v", got, replacement, err)
	}
	select {
	case <-oldSession.waitDone:
	default:
		t.Fatal("ABA hook ran before old process Wait completed")
	}
}

func TestProcessSessionKillWaitTimeoutRetainsExactRecord(t *testing.T) {
	manager := NewSessionManager()
	t.Cleanup(manager.Stop)
	owner := processTestOwner("kill-wait-timeout")
	session := newManagedTestSession(
		"kill-wait-timeout",
		424242,
		"running",
		time.Now().Unix(),
	)
	var killCalls atomic.Int64
	session.killProcessFn = func(pid int) error {
		if pid != session.PID {
			return fmt.Errorf("kill pid = %d, want %d", pid, session.PID)
		}
		killCalls.Add(1)
		return nil
	}
	if err := manager.Add(owner, session); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		session.signalWaitDone()
		removeErr := manager.Remove(owner, session.ID, session)
		if removeErr != nil && !errors.Is(removeErr, ErrSessionNotFound) {
			t.Errorf("cleanup timeout session: %v", removeErr)
		}
	})
	tool := &ExecTool{
		sessionManager:     manager,
		sessionWaitTimeout: 25 * time.Millisecond,
	}

	result := tool.Execute(processTestContext(owner), map[string]any{
		"action": "kill", "sessionId": session.ID,
	})
	if result == nil || !result.IsError ||
		!strings.Contains(result.ForLLM, context.DeadlineExceeded.Error()) {
		t.Fatalf("kill wait timeout result = %#v", result)
	}
	if killCalls.Load() != 1 {
		t.Fatalf("kill calls = %d, want 1", killCalls.Load())
	}
	retained, err := manager.Get(owner, session.ID)
	if err != nil || retained != session {
		t.Fatalf("timed-out kill removed record: got=%p want=%p err=%v", retained, session, err)
	}
}

func TestProcessSessionConcurrentForeignOperations(t *testing.T) {
	tool, err := NewExecTool(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	owner := processTestOwner("race-owner")
	foreign := processTestOwner("race-foreign")
	installProcessTestManager(t, tool, owner, foreign)
	started := startOwnedBackgroundProcess(
		t, tool, processTestContext(owner), processTestSleepCommand(30), false,
	)
	ctxOwner, ctxForeign := processTestContext(owner), processTestContext(foreign)

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for iteration := 0; iteration < 25; iteration++ {
				for _, args := range []map[string]any{
					{"action": "poll", "sessionId": started.SessionID},
					{"action": "read", "sessionId": started.SessionID},
					{"action": "write", "sessionId": started.SessionID, "data": "foreign"},
					{"action": "send-keys", "sessionId": started.SessionID, "keys": "up"},
					{"action": "kill", "sessionId": started.SessionID},
				} {
					if result := tool.Execute(ctxForeign, args); result == nil || !result.IsError {
						errs <- fmt.Errorf("worker %d foreign action succeeded: %#v", worker, args)
						return
					}
				}
				if result := tool.Execute(ctxOwner, map[string]any{"action": "list"}); result == nil || result.IsError {
					errs <- fmt.Errorf("worker %d owner list failed: %#v", worker, result)
					return
				}
			}
		}(worker)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if result := tool.Execute(ctxOwner, map[string]any{
		"action": "kill", "sessionId": started.SessionID,
	}); result.IsError {
		t.Fatalf("owner kill after foreign race = %#v", result)
	}
}
