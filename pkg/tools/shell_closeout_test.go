package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestCloseoutShellConstructorAndDescriptorBranches(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Exec.EnableDenyPatterns = false
	cfg.Tools.Exec.AllowRemote = false
	cfg.Tools.Exec.TimeoutSeconds = 3
	cfg.Tools.Exec.CustomAllowPatterns = []string{`^safe\b`}
	tool, err := NewExecToolWithConfig(t.TempDir(), true, cfg, []*regexp.Regexp{
		regexp.MustCompile(`^/approved`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if tool.Name() != "exec" || tool.Description() == "" || tool.Parameters()["type"] != "object" {
		t.Fatalf("unexpected exec descriptor: %q %q %#v", tool.Name(), tool.Description(), tool.Parameters())
	}
	if tool.timeout != 3*time.Second || tool.allowRemote || len(tool.customAllowPatterns) != 1 ||
		len(tool.allowedPathPatterns) != 1 {
		t.Fatalf("constructor projection = %#v", tool)
	}

	badDeny := config.DefaultConfig()
	badDeny.Tools.Exec.EnableDenyPatterns = true
	badDeny.Tools.Exec.CustomDenyPatterns = []string{"["}
	if _, err := NewExecToolWithConfig("", false, badDeny); err == nil {
		t.Fatal("invalid custom deny pattern was accepted")
	}
	badAllow := config.DefaultConfig()
	badAllow.Tools.Exec.CustomAllowPatterns = []string{"["}
	if _, err := NewExecToolWithConfig("", false, badAllow); err == nil {
		t.Fatal("invalid custom allow pattern was accepted")
	}
}

func TestCloseoutShellKeyEncodingBranches(t *testing.T) {
	tests := []struct {
		name string
		key  string
		mode PtyKeyMode
		want string
		err  bool
	}{
		{name: "empty", key: "  ", want: ""},
		{name: "short control", key: "C-A", want: "\x01"},
		{name: "long control", key: " ctrl-z ", want: "\x1a"},
		{name: "bad short control", key: "c-1", err: true},
		{name: "bad long control", key: "ctrl-?", err: true},
		{name: "short alt", key: "m-x", want: "\x1bx"},
		{name: "long alt", key: "alt-Y", want: "\x1by"},
		{name: "bad alt", key: "alt-xy", err: true},
		{name: "short shift", key: "s-space", want: " "},
		{name: "long shift", key: "shift-up", want: "\x1b[A"},
		{name: "bad shift", key: "shift-missing", err: true},
		{name: "ss3", key: "up", mode: PtyKeyModeSS3, want: "\x1bOA"},
		{name: "csi", key: "up", mode: PtyKeyModeCSI, want: "\x1b[A"},
		{name: "unknown", key: "definitely-missing", err: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := encodeKeyToken(test.key, test.mode)
			if (err != nil) != test.err || got != test.want {
				t.Fatalf("encodeKeyToken(%q) = %q, %v; want %q, err=%t", test.key, got, err, test.want, test.err)
			}
		})
	}
	if got, err := encodeKeySequence([]string{"ctrl-a", "alt-x", "enter"}, PtyKeyModeCSI); err != nil ||
		got != "\x01\x1bx\r" {
		t.Fatalf("encodeKeySequence success = %q, %v", got, err)
	}
	if _, err := encodeKeySequence([]string{"enter", "missing"}, PtyKeyModeCSI); err == nil {
		t.Fatal("encodeKeySequence accepted an unknown key")
	}
}

func TestCloseoutShellEnvironmentAllowlistAndPathHelpers(t *testing.T) {
	values := map[string]string{"PICOCLAW_CLOSEOUT_PS": "expanded"}
	got, _ := expandPowerShellEnvVarsWithLookup(
		`$env:PICOCLAW_CLOSEOUT_PS $Env:PICOCLAW_CLOSEOUT_PS ${ENV:PICOCLAW_CLOSEOUT_PS} %PICOCLAW_CLOSEOUT_PS% `+
			`$env:PICOCLAW_CLOSEOUT_MISSING %PICOCLAW_CLOSEOUT_MISSING%`,
		func(name string) (string, bool) {
			value, ok := values[name]
			return value, ok
		},
	)
	if got != "expanded expanded expanded expanded $env:PICOCLAW_CLOSEOUT_MISSING %PICOCLAW_CLOSEOUT_MISSING%" {
		t.Fatalf("expanded environment = %q", got)
	}

	tool := &ExecTool{
		allowPatterns:       []*regexp.Regexp{regexp.MustCompile(`^builtin`)},
		customAllowPatterns: []*regexp.Regexp{regexp.MustCompile(`custom$`)},
	}
	if !tool.commandMatchesAllowPattern("builtin command") ||
		!tool.commandMatchesAllowPattern("a custom") ||
		tool.commandMatchesAllowPattern("denied") {
		t.Fatal("command allow-pattern matching was inconsistent")
	}
	if err := tool.SetAllowPatterns([]string{`^echo\b`, `safe$`}); err != nil {
		t.Fatal(err)
	}
	if !tool.commandMatchesAllowPattern("echo ok") {
		t.Fatal("SetAllowPatterns did not install compiled patterns")
	}
	if err := tool.SetAllowPatterns([]string{"["}); err == nil {
		t.Fatal("SetAllowPatterns accepted invalid regexp")
	}

	for input, want := range map[string]bool{
		"a": false, "localhost": false, ".hidden.example": false,
		"script.py": false, "example.com": true, "9.example": true,
	} {
		if got := looksLikeDomain(input); got != want {
			t.Errorf("looksLikeDomain(%q) = %t, want %t", input, got, want)
		}
	}
	if !commonFileExtension("go") || commonFileExtension("example") {
		t.Fatal("common file extension classification was inconsistent")
	}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "present"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !localPathExists(directory, "present") || localPathExists(directory, "missing") {
		t.Fatal("localPathExists classification was inconsistent")
	}

	command := `tool --config=relative/path/file.txt -I/outside /absolute`
	start := strings.Index(command, "/path/")
	_, end := shellTokenBounds(command, start)
	if got := commandPathTextFromMatch(command, start, end); got != "relative/path/file.txt" {
		t.Fatalf("option value path = %q", got)
	}
	attached := strings.Index(command, "/outside")
	_, attachedEnd := shellTokenBounds(command, attached)
	if got := commandPathTextFromMatch(command, attached, attachedEnd); got != "/outside" {
		t.Fatalf("attached option path = %q", got)
	}
	absolute := strings.LastIndex(command, "/absolute")
	if !isUnixAbsolutePathMatchStart(command, absolute) || !isUnixAbsolutePathMatchStart("/root", 0) {
		t.Fatal("absolute path starts were not recognized")
	}
	if _, err := commandPathAbs("child", directory); err != nil {
		t.Fatal(err)
	}
}

func TestCloseoutShellGuardAndSendKeysValidation(t *testing.T) {
	workspace := t.TempDir()
	allowedOutside := filepath.Join(t.TempDir(), "allowed.txt")
	tool := &ExecTool{
		workingDir:          workspace,
		restrictToWorkspace: true,
		denyPatterns:        []*regexp.Regexp{regexp.MustCompile(`danger`)},
		allowPatterns:       []*regexp.Regexp{regexp.MustCompile(`^(echo|cat|curl)`)},
		allowedPathPatterns: []*regexp.Regexp{regexp.MustCompile("^" + regexp.QuoteMeta(allowedOutside) + "$")},
		customAllowPatterns: []*regexp.Regexp{regexp.MustCompile(`^custom`)},
	}
	checks := []struct {
		command string
		blocked bool
	}{
		{command: "echo danger", blocked: true},
		{command: "unknown command", blocked: true},
		{command: "echo ../../outside", blocked: true},
		{command: "cat /definitely/outside", blocked: true},
		{command: "cat /dev/null"},
		{command: "cat " + allowedOutside},
		{command: "curl https://example.com/path"},
		{command: "curl example.com/path"},
	}
	for _, check := range checks {
		blocked := tool.guardCommand(check.command, workspace) != ""
		if blocked != check.blocked {
			t.Errorf("guardCommand(%q) blocked=%t, want %t", check.command, blocked, check.blocked)
		}
	}

	validationTool := &ExecTool{}
	for name, args := range map[string]map[string]any{
		"missing session": {"keys": "enter"},
		"wrong keys type": {"sessionId": "s", "keys": 1},
		"empty keys":      {"sessionId": "s", "keys": ""},
		"commas only":     {"sessionId": "s", "keys": " , , "},
		"no manager":      {"sessionId": "s", "keys": "enter"},
	} {
		t.Run(name, func(t *testing.T) {
			result := validationTool.executeSendKeys(context.Background(), args)
			if result == nil || !result.IsError {
				t.Fatalf("executeSendKeys(%v) = %#v", args, result)
			}
		})
	}
}

func TestCloseoutShellActionValidationAndProcessBounds(t *testing.T) {
	tool := &ExecTool{allowRemote: true}
	for name, args := range map[string]map[string]any{
		"missing action":  {},
		"unknown action":  {"action": "unknown"},
		"run no command":  {"action": "run"},
		"poll no id":      {"action": "poll"},
		"read no id":      {"action": "read"},
		"write no id":     {"action": "write"},
		"write no data":   {"action": "write", "sessionId": "missing"},
		"kill no id":      {"action": "kill"},
		"send keys no id": {"action": "send-keys", "keys": "enter"},
	} {
		t.Run(name, func(t *testing.T) {
			if result := tool.Execute(context.Background(), args); result == nil || !result.IsError {
				t.Fatalf("Execute(%v) = %#v", args, result)
			}
		})
	}
	for _, action := range []string{"list", "poll", "read", "write", "kill", "send-keys"} {
		args := map[string]any{
			"action":    action,
			"sessionId": "missing",
			"data":      "input",
			"keys":      "enter",
		}
		if result := tool.Execute(context.Background(), args); result == nil || !result.IsError ||
			!strings.Contains(result.ForLLM, "manager not configured") {
			t.Errorf("nil-manager %s = %#v", action, result)
		}
	}

	manager := NewSessionManager()
	t.Cleanup(manager.Stop)
	managed := &ExecTool{allowRemote: true, sessionManager: manager}
	owner := ProcessSessionOwner{AgentID: "closeout", SessionKey: "closeout-session"}
	ctx := processTestContext(owner)
	for _, action := range []string{"poll", "read", "write", "kill", "send-keys"} {
		args := map[string]any{
			"action":    action,
			"sessionId": "missing",
			"data":      "input",
			"keys":      "enter",
		}
		if result := managed.Execute(ctx, args); result == nil || !result.IsError ||
			!strings.Contains(result.ForLLM, "session not found") {
			t.Errorf("missing-session %s = %#v", action, result)
		}
	}
	if result := processSessionAccessError(
		"session",
		errors.New("generic access failure"),
	); result == nil || !result.IsError ||
		!strings.Contains(result.ForLLM, "generic access failure") {
		t.Fatalf("generic access error = %#v", result)
	}
	if emptyOwner := processSessionOwnerFromContext(nil); emptyOwner != (ProcessSessionOwner{}) {
		t.Fatalf("nil-context owner = %#v", emptyOwner)
	}
	if got := (*ExecTool)(nil).processSessionWaitBound(); got != processSessionWaitTimeout {
		t.Fatalf("default wait bound = %v", got)
	}
	if got := (&ExecTool{sessionWaitTimeout: 7 * time.Millisecond}).processSessionWaitBound(); got != 7*time.Millisecond {
		t.Fatalf("custom wait bound = %v", got)
	}
	if got := (*ExecTool)(nil).processSessionPTYDrainBound(); got != processSessionPTYDrainTimeout {
		t.Fatalf("default drain bound = %v", got)
	}
	if got := (&ExecTool{ptyDrainTimeout: 9 * time.Millisecond}).processSessionPTYDrainBound(); got != 9*time.Millisecond {
		t.Fatalf("custom drain bound = %v", got)
	}

	ready := make(chan struct{})
	close(ready)
	awaitProcessSessionPTYDrain(ready, time.Second, func() {
		t.Fatal("closed reader invoked timeout cleanup")
	})
	timed := make(chan struct{})
	closed := false
	awaitProcessSessionPTYDrain(timed, time.Millisecond, func() {
		closed = true
		close(timed)
	})
	if !closed {
		t.Fatal("PTY drain timeout did not close the master")
	}

	if _, err := (*ExecTool)(nil).reserveProcessSessionID(owner); err == nil {
		t.Fatal("nil tool reserved a process session")
	}
	reservationTool := &ExecTool{sessionManager: manager}
	reservation, reservationErr := reservationTool.reserveProcessSessionID(owner)
	if reservationErr != nil || reservation == nil || !manager.releaseReservation(reservation) {
		t.Fatalf("default reservation = %#v, %v", reservation, reservationErr)
	}
	invalidGenerator := &ExecTool{
		sessionManager:     manager,
		sessionIDGenerator: func() string { return "" },
	}
	if _, invalidIDErr := invalidGenerator.reserveProcessSessionID(owner); !errors.Is(
		invalidIDErr,
		ErrSessionReservationInvalid,
	) {
		t.Fatalf("invalid generated id error = %v", invalidIDErr)
	}
	held, err := manager.reserveID(owner, "collision")
	if err != nil {
		t.Fatal(err)
	}
	collisionTool := &ExecTool{
		sessionManager:     manager,
		sessionIDGenerator: func() string { return "collision" },
	}
	if _, err := collisionTool.reserveProcessSessionID(owner); !errors.Is(
		err,
		ErrSessionAlreadyExists,
	) {
		t.Fatalf("reservation exhaustion error = %v", err)
	}
	if !manager.releaseReservation(held) {
		t.Fatal("held collision reservation was not released")
	}
}

func TestCloseoutShellSynchronousAndSendKeysSuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell and PTY success commands are Unix-specific")
	}
	tool, err := NewExecTool("", false)
	if err != nil {
		t.Fatal(err)
	}
	tool.allowRemote = true
	if result := tool.Execute(context.Background(), map[string]any{
		"action":  "run",
		"command": ":",
	}); result == nil || result.IsError || result.ForLLM != "(no output)" {
		t.Fatalf("empty synchronous command = %#v", result)
	}

	cfg := config.DefaultConfig()
	cfg.Tools.Exec.EnableDenyPatterns = false
	cfg.Tools.Exec.AllowRemote = false
	internal, err := NewExecToolWithConfig("", false, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result := internal.Execute(context.Background(), map[string]any{
		"action":    "run",
		"command":   "printf closeout",
		"__channel": "cli",
	}); result == nil || result.IsError || !strings.Contains(result.ForLLM, "closeout") {
		t.Fatalf("argument-channel synchronous command = %#v", result)
	}

	ptyTool, err := NewExecTool("", false)
	if err != nil {
		t.Fatal(err)
	}
	owner := processTestOwner("closeout-send-keys")
	installProcessTestManager(t, ptyTool, owner)
	ctx := processTestContext(owner)
	started := ptyTool.Execute(ctx, map[string]any{
		"action":     "run",
		"command":    "cat",
		"pty":        true,
		"background": true,
	})
	if started == nil || started.IsError {
		t.Fatalf("start PTY session = %#v", started)
	}
	var response ExecResponse
	if err := json.Unmarshal([]byte(started.ForLLM), &response); err != nil || response.SessionID == "" {
		t.Fatalf("decode PTY session = %#v, %v", response, err)
	}
	if result := ptyTool.Execute(ctx, map[string]any{
		"action":    "send-keys",
		"sessionId": response.SessionID,
		"keys":      "space, enter",
	}); result == nil || result.IsError {
		t.Fatalf("send keys = %#v", result)
	}
	if result := ptyTool.Execute(ctx, map[string]any{
		"action":    "send-keys",
		"sessionId": response.SessionID,
		"keys":      "missing-key",
	}); result == nil || !result.IsError || !strings.Contains(result.ForLLM, "invalid key") {
		t.Fatalf("send invalid keys = %#v", result)
	}
	if result := ptyTool.Execute(ctx, map[string]any{
		"action":    "kill",
		"sessionId": response.SessionID,
	}); result == nil || result.IsError {
		t.Fatalf("kill PTY session = %#v", result)
	}
}

func TestCloseoutShellBackgroundFailureSeams(t *testing.T) {
	owner := ProcessSessionOwner{AgentID: "closeout", SessionKey: "background-failures"}
	ctx := processTestContext(owner)
	for name, configure := range map[string]func(*ExecTool){
		"open PTY": func(tool *ExecTool) {
			tool.backgroundOps = &backgroundProcessOperations{
				openPTY: func() (*os.File, *os.File, error) {
					return nil, nil, errors.New("injected PTY open failure")
				},
			}
		},
		"prepare PTY": func(tool *ExecTool) {
			closed, err := os.CreateTemp(t.TempDir(), "closed-pty")
			if err != nil {
				t.Fatal(err)
			}
			if err := closed.Close(); err != nil {
				t.Fatal(err)
			}
			tool.backgroundOps = &backgroundProcessOperations{
				openPTY: func() (*os.File, *os.File, error) {
					return closed, nil, nil
				},
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			manager := NewSessionManager()
			t.Cleanup(manager.Stop)
			tool := &ExecTool{
				allowRemote:        true,
				sessionManager:     manager,
				sessionIDGenerator: func() string { return "failure-session" },
			}
			configure(tool)
			result := tool.runBackground(ctx, owner, "true", "", true)
			if result == nil || !result.IsError {
				t.Fatalf("background failure result = %#v", result)
			}
		})
	}

	manager := NewSessionManager()
	t.Cleanup(manager.Stop)
	pipeTool := &ExecTool{
		allowRemote:        true,
		sessionManager:     manager,
		sessionIDGenerator: func() string { return "pipe-failure" },
		backgroundOps: &backgroundProcessOperations{
			stdoutPipe: func(*exec.Cmd) (io.ReadCloser, error) {
				return nil, errors.New("injected stdout pipe failure")
			},
		},
	}
	if result := pipeTool.runBackground(ctx, owner, "true", "", false); result == nil ||
		!result.IsError || !strings.Contains(result.ForLLM, "stdout pipe") {
		t.Fatalf("stdout pipe failure = %#v", result)
	}
}

func TestCloseoutShellBackgroundTruncationAndKeyMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("background stream fixtures use a Unix shell")
	}
	tool, err := NewExecTool("", false)
	if err != nil {
		t.Fatal(err)
	}
	owner := processTestOwner("closeout-truncation")
	manager := installProcessTestManager(t, tool, owner)
	ctx := processTestContext(owner)
	tests := []struct {
		name    string
		command string
		pty     bool
		mode    PtyKeyMode
	}{
		{
			name:    "stdout",
			command: "head -c 1100000 /dev/zero",
		},
		{
			name:    "stderr",
			command: "exec 1>&-; head -c 1100000 /dev/zero >&2",
		},
		{
			name:    "pty and key mode",
			command: "printf '\\033[?1h'; head -c 1100000 /dev/zero",
			pty:     true,
			mode:    PtyKeyModeSS3,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			started := startOwnedBackgroundProcess(t, tool, ctx, test.command, test.pty)
			session, getErr := manager.Get(owner, started.SessionID)
			if getErr != nil {
				t.Fatal(getErr)
			}
			awaitProcessTestWait(t, session, started.SessionID)
			session.mu.Lock()
			truncated := session.outputTruncated
			mode := session.ptyKeyMode
			session.mu.Unlock()
			if !truncated {
				t.Fatal("large background output was not truncated")
			}
			if test.mode != PtyKeyModeNotFound && mode != test.mode {
				t.Fatalf("PTY key mode = %v, want %v", mode, test.mode)
			}
			if err := manager.Remove(owner, started.SessionID, session); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCloseoutShellSessionWriteErrorsAndCompletedKeys(t *testing.T) {
	manager := NewSessionManager()
	t.Cleanup(manager.Stop)
	owner := ProcessSessionOwner{AgentID: "closeout", SessionKey: "write-errors"}
	ctx := processTestContext(owner)
	tool := &ExecTool{allowRemote: true, sessionManager: manager}
	for _, test := range []struct {
		id  string
		err error
	}{
		{id: "writer-done", err: ErrSessionDone},
		{id: "writer-error", err: errors.New("injected writer failure")},
	} {
		reservation, reserveErr := manager.reserveID(owner, test.id)
		if reserveErr != nil {
			t.Fatal(reserveErr)
		}
		session := &ProcessSession{
			ID:           test.id,
			PID:          os.Getpid(),
			Command:      "synthetic writer",
			Background:   true,
			StartTime:    time.Now().Unix(),
			Status:       "running",
			stdinWriter:  closeoutShellErrorWriter{err: test.err},
			outputBuffer: &bytes.Buffer{},
			waitDone:     make(chan struct{}),
		}
		if err := manager.promoteReservation(reservation, session); err != nil {
			t.Fatal(err)
		}
		result := tool.Execute(ctx, map[string]any{
			"action":    "send-keys",
			"sessionId": test.id,
			"keys":      "enter",
		})
		if result == nil || !result.IsError {
			t.Fatalf("send keys writer error = %#v", result)
		}
		if err := manager.Remove(owner, test.id, session); err != nil {
			t.Fatal(err)
		}
	}
	writeReservation, writeReserveErr := manager.reserveID(owner, "write-race-done")
	if writeReserveErr != nil {
		t.Fatal(writeReserveErr)
	}
	writeSession := &ProcessSession{
		ID:           "write-race-done",
		PID:          os.Getpid(),
		Command:      "synthetic write",
		Background:   true,
		StartTime:    time.Now().Unix(),
		Status:       "running",
		stdinWriter:  closeoutShellErrorWriter{err: ErrSessionDone},
		outputBuffer: &bytes.Buffer{},
		waitDone:     make(chan struct{}),
	}
	if promoteErr := manager.promoteReservation(writeReservation, writeSession); promoteErr != nil {
		t.Fatal(promoteErr)
	}
	if result := tool.Execute(ctx, map[string]any{
		"action":    "write",
		"sessionId": writeSession.ID,
		"data":      "input",
	}); result == nil || !result.IsError || !strings.Contains(result.ForLLM, "already exited") {
		t.Fatalf("write raced completed session = %#v", result)
	}
	if removeErr := manager.Remove(owner, writeSession.ID, writeSession); removeErr != nil {
		t.Fatal(removeErr)
	}

	killReservation, killReserveErr := manager.reserveID(owner, "kill-error")
	if killReserveErr != nil {
		t.Fatal(killReserveErr)
	}
	killSession := &ProcessSession{
		ID:           "kill-error",
		PID:          os.Getpid(),
		Command:      "synthetic kill",
		Background:   true,
		StartTime:    time.Now().Unix(),
		Status:       "running",
		outputBuffer: &bytes.Buffer{},
		waitDone:     make(chan struct{}),
		killProcessFn: func(int) error {
			return errors.New("injected kill failure")
		},
	}
	if promoteErr := manager.promoteReservation(killReservation, killSession); promoteErr != nil {
		t.Fatal(promoteErr)
	}
	if result := tool.Execute(ctx, map[string]any{
		"action":    "kill",
		"sessionId": killSession.ID,
	}); result == nil || !result.IsError || !strings.Contains(result.ForLLM, "injected kill failure") {
		t.Fatalf("kill session failure = %#v", result)
	}
	if removeErr := manager.Remove(owner, killSession.ID, killSession); removeErr != nil {
		t.Fatal(removeErr)
	}

	if runtime.GOOS == "windows" {
		return
	}
	realTool, toolErr := NewExecTool("", false)
	if toolErr != nil {
		t.Fatal(toolErr)
	}
	realOwner := processTestOwner("closeout-completed-keys")
	realManager := installProcessTestManager(t, realTool, realOwner)
	realCtx := processTestContext(realOwner)
	started := startOwnedBackgroundProcess(t, realTool, realCtx, "true", false)
	session, getErr := realManager.Get(realOwner, started.SessionID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	awaitProcessTestWait(t, session, started.SessionID)
	if result := realTool.Execute(realCtx, map[string]any{
		"action":    "send-keys",
		"sessionId": started.SessionID,
		"keys":      "enter",
	}); result == nil || !result.IsError || !strings.Contains(result.ForLLM, "already exited") {
		t.Fatalf("send keys to completed session = %#v", result)
	}
	if removeErr := realManager.Remove(realOwner, started.SessionID, session); removeErr != nil {
		t.Fatal(removeErr)
	}
}

func TestCloseoutShellSynchronousExceptionalResults(t *testing.T) {
	t.Run("missing shell", func(t *testing.T) {
		t.Setenv("PATH", "")
		tool, err := NewExecTool("", false)
		if err != nil {
			t.Fatal(err)
		}
		result := tool.runSync(context.Background(), ":", "")
		if result == nil || !result.IsError || !strings.Contains(result.ForLLM, "failed to start") {
			t.Fatalf("missing shell result = %#v", result)
		}
	})
	if runtime.GOOS == "windows" {
		return
	}
	tool, err := NewExecTool("", false)
	if err != nil {
		t.Fatal(err)
	}
	result := tool.runSync(context.Background(), "kill -9 $$", "")
	if result == nil || !result.IsError || !strings.Contains(result.ForLLM, "killed by signal") {
		t.Fatalf("signal exit result = %#v", result)
	}
}

func TestCloseoutShellRestrictedWorkingDirectoryRevalidation(t *testing.T) {
	workspace := t.TempDir()
	inside := filepath.Join(workspace, "inside")
	outside := t.TempDir()
	if err := os.Mkdir(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	tool, err := NewExecTool(
		workspace,
		true,
		[]*regexp.Regexp{regexp.MustCompile("^" + regexp.QuoteMeta(outside) + "$")},
	)
	if err != nil {
		t.Fatal(err)
	}
	for name, cwd := range map[string]string{
		"inside":          inside,
		"allowed outside": outside,
	} {
		t.Run(name, func(t *testing.T) {
			if result := tool.Execute(context.Background(), map[string]any{
				"action":  "run",
				"command": ":",
				"cwd":     cwd,
			}); result == nil || result.IsError {
				t.Fatalf("restricted cwd %q = %#v", cwd, result)
			}
		})
	}
	if result := tool.Execute(context.Background(), map[string]any{
		"action":  "run",
		"command": ":",
		"cwd":     filepath.Join(workspace, "missing"),
	}); result == nil || !result.IsError {
		t.Fatalf("missing restricted cwd = %#v", result)
	}
}

func TestCloseoutShellBackgroundReaderPanicRecovery(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("background panic fixtures use Unix process behavior")
	}
	t.Run("pipe reader", func(t *testing.T) {
		manager := NewSessionManager()
		t.Cleanup(manager.Stop)
		owner := ProcessSessionOwner{AgentID: "closeout", SessionKey: "pipe-panic"}
		var startedCommand *exec.Cmd
		tool := &ExecTool{
			allowRemote:        true,
			sessionManager:     manager,
			sessionIDGenerator: func() string { return "pipe-panic" },
			backgroundOps: &backgroundProcessOperations{
				stdoutPipe: func(*exec.Cmd) (io.ReadCloser, error) {
					return closeoutShellPanicReadCloser{}, nil
				},
				stderrPipe: func(*exec.Cmd) (io.ReadCloser, error) {
					return io.NopCloser(strings.NewReader("")), nil
				},
				stdinPipe: func(*exec.Cmd) (io.WriteCloser, error) {
					return closeoutShellNopWriteCloser{}, nil
				},
				start: func(command *exec.Cmd) error {
					startedCommand = command
					return command.Start()
				},
			},
		}
		ctx := processTestContext(owner)
		result := tool.runBackground(ctx, owner, "true", "", false)
		if result == nil || result.IsError {
			t.Fatalf("start pipe panic fixture = %#v", result)
		}
		session, getErr := manager.Get(owner, "pipe-panic")
		if getErr != nil {
			t.Fatal(getErr)
		}
		awaitProcessTestWait(t, session, session.ID)
		if startedCommand == nil {
			t.Fatal("pipe panic fixture did not start its command")
		}
		_ = startedCommand.Wait()
		session.mu.Lock()
		session.Status = "done"
		session.processExited = true
		session.mu.Unlock()
		if removeErr := manager.Remove(owner, session.ID, session); removeErr != nil {
			t.Fatal(removeErr)
		}
	})
}

type closeoutShellErrorWriter struct{ err error }

func (writer closeoutShellErrorWriter) Write([]byte) (int, error) {
	return 0, writer.err
}

type closeoutShellPanicReadCloser struct{}

func (closeoutShellPanicReadCloser) Read([]byte) (int, error) {
	panic("injected pipe reader panic")
}

func (closeoutShellPanicReadCloser) Close() error { return nil }

type closeoutShellNopWriteCloser struct{}

func (closeoutShellNopWriteCloser) Write(data []byte) (int, error) { return len(data), nil }

func (closeoutShellNopWriteCloser) Close() error { return nil }
