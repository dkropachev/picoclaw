package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/media"
)

type codexExecBackendFunc func(context.Context, map[string]any) *ToolResult

func (fn codexExecBackendFunc) Execute(ctx context.Context, args map[string]any) *ToolResult {
	return fn(ctx, args)
}

func sortedSchemaPropertyNames(t *testing.T, schema map[string]any) []string {
	t.Helper()
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties = %#v, want map", schema["properties"])
	}
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func TestCodexCompatibilitySchemasAreClosedAndExact(t *testing.T) {
	tests := []struct {
		name           string
		tool           Tool
		wantProperties []string
		wantRequired   []string
	}{
		{
			name:           "exec_command",
			tool:           NewCodexExecCommandTool(nil),
			wantProperties: []string{"background", "cmd", "tty", "workdir"},
			wantRequired:   []string{"cmd"},
		},
		{
			name:           "write_stdin",
			tool:           NewCodexWriteStdinTool(nil),
			wantProperties: []string{"chars", "session_id"},
			wantRequired:   []string{"session_id", "chars"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			schema := test.tool.Parameters()
			if schema["type"] != "object" || schema["additionalProperties"] != false {
				t.Fatalf("schema root = %#v, want closed object", schema)
			}
			if got := sortedSchemaPropertyNames(t, schema); !reflect.DeepEqual(got, test.wantProperties) {
				t.Fatalf("properties = %v, want %v", got, test.wantProperties)
			}
			if got, ok := schema["required"].([]string); !ok || !reflect.DeepEqual(got, test.wantRequired) {
				t.Fatalf("required = %#v, want %#v", schema["required"], test.wantRequired)
			}
		})
	}

	if description := NewCodexExecCommandTool(nil).Description(); !strings.Contains(description, "input-only") ||
		!strings.Contains(description, "does not expose session output") {
		t.Fatalf("exec_command description is not explicit about input-only sessions: %q", description)
	}
	if description := NewCodexWriteStdinTool(nil).Description(); !strings.Contains(description, "input-only") ||
		!strings.Contains(description, "does not poll") {
		t.Fatalf("write_stdin description is not explicit about input-only sessions: %q", description)
	}
}

func TestCodexExecCommandTool_DirectValidationPrecedesBackendAndNeverDispatches(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
	}{
		{name: "missing cmd", args: map[string]any{}},
		{name: "wrong cmd type", args: map[string]any{"cmd": 1}},
		{name: "wrong workdir type", args: map[string]any{"cmd": "true", "workdir": true}},
		{name: "wrong background type", args: map[string]any{"cmd": "true", "background": "true"}},
		{name: "wrong tty type", args: map[string]any{"cmd": "true", "tty": "true"}},
		{name: "blank cmd", args: map[string]any{"cmd": " \t\n"}},
		{name: "tty without background", args: map[string]any{"cmd": "true", "tty": true}},
		{name: "yield removed", args: map[string]any{"cmd": "true", "yield_time_ms": 1}},
		{name: "timeout removed", args: map[string]any{"cmd": "true", "timeout": 1}},
		{name: "login removed", args: map[string]any{"cmd": "true", "login": true}},
		{name: "shell removed", args: map[string]any{"cmd": "true", "shell": "sh"}},
		{name: "output budget removed", args: map[string]any{"cmd": "true", "max_output_tokens": 1}},
		{name: "escalation absent", args: map[string]any{"cmd": "true", "sandbox_permissions": "require_escalated"}},
		{name: "prefix rule absent", args: map[string]any{"cmd": "true", "prefix_rule": []any{"true"}}},
		{name: "justification absent", args: map[string]any{"cmd": "true", "justification": "probe"}},
		{name: "arbitrary unknown", args: map[string]any{"cmd": "true", "unknown": true}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls int
			backend := codexExecBackendFunc(func(context.Context, map[string]any) *ToolResult {
				calls++
				return SilentResult(`{"sessionId":"should-not-run","status":"running"}`)
			})
			result := (&CodexExecCommandTool{exec: backend}).Execute(context.Background(), test.args)
			if result == nil || !result.IsError {
				t.Fatalf("invalid args returned %#v, want error", result)
			}
			if calls != 0 {
				t.Fatalf("backend calls = %d, want zero", calls)
			}

			var nilTool *CodexExecCommandTool
			precedence := nilTool.Execute(context.Background(), test.args)
			if precedence == nil || !precedence.IsError ||
				strings.Contains(precedence.ForLLM, "backend not configured") {
				t.Fatalf("validation did not outrank backend availability: %#v", precedence)
			}
		})
	}

	valid := NewCodexExecCommandTool(nil).Execute(context.Background(), map[string]any{"cmd": "true"})
	if valid == nil || !valid.IsError || !strings.Contains(valid.ForLLM, "backend not configured") {
		t.Fatalf("valid nil-backend call = %#v, want backend availability error", valid)
	}
}

func TestCodexWriteStdinTool_DirectValidationPrecedesBackendAndNeverDispatches(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
	}{
		{name: "missing session", args: map[string]any{"chars": "x"}},
		{name: "missing chars", args: map[string]any{"session_id": "session"}},
		{name: "wrong session type", args: map[string]any{"session_id": 1, "chars": "x"}},
		{name: "wrong chars type", args: map[string]any{"session_id": "session", "chars": 1}},
		{name: "blank session", args: map[string]any{"session_id": " \t\n", "chars": "x"}},
		{name: "empty chars", args: map[string]any{"session_id": "session", "chars": ""}},
		{name: "yield removed", args: map[string]any{"session_id": "session", "chars": "x", "yield_time_ms": 1}},
		{
			name: "output budget removed",
			args: map[string]any{"session_id": "session", "chars": "x", "max_output_tokens": 1},
		},
		{name: "arbitrary unknown", args: map[string]any{"session_id": "session", "chars": "x", "unknown": true}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls int
			backend := codexExecBackendFunc(func(context.Context, map[string]any) *ToolResult {
				calls++
				return SilentResult(`{"sessionId":"session","status":"running"}`)
			})
			result := (&CodexWriteStdinTool{exec: backend}).Execute(context.Background(), test.args)
			if result == nil || !result.IsError {
				t.Fatalf("invalid args returned %#v, want error", result)
			}
			if calls != 0 {
				t.Fatalf("backend calls = %d, want zero", calls)
			}

			var nilTool *CodexWriteStdinTool
			precedence := nilTool.Execute(context.Background(), test.args)
			if precedence == nil || !precedence.IsError ||
				strings.Contains(precedence.ForLLM, "backend not configured") {
				t.Fatalf("validation did not outrank backend availability: %#v", precedence)
			}
		})
	}

	valid := NewCodexWriteStdinTool(nil).Execute(context.Background(), map[string]any{
		"session_id": "session",
		"chars":      " ",
	})
	if valid == nil || !valid.IsError || !strings.Contains(valid.ForLLM, "backend not configured") {
		t.Fatalf("valid nil-backend call = %#v, want backend availability error", valid)
	}
}

func TestCodexExecCommandTool_MapsCommandToExecBackend(t *testing.T) {
	execTool, err := NewExecToolWithConfig(t.TempDir(), false, &config.Config{
		Tools: config.ToolsConfig{
			Exec: config.ExecConfig{
				ToolConfig:     config.ToolConfig{Enabled: true},
				AllowRemote:    true,
				TimeoutSeconds: 5,
			},
		},
	})
	if err != nil {
		t.Fatalf("NewExecToolWithConfig() error = %v", err)
	}

	result := NewCodexExecCommandTool(execTool).Execute(context.Background(), map[string]any{
		"cmd": "printf codex-compatible",
	})
	if result.IsError {
		t.Fatalf("exec_command returned error: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "codex-compatible") {
		t.Fatalf("ForLLM = %q, want command output", result.ForLLM)
	}
}

func TestCodexExecCommandTool_TrimsCommandAndWorkdir(t *testing.T) {
	workdir := t.TempDir()
	execTool, err := NewExecToolWithConfig(t.TempDir(), false, &config.Config{
		Tools: config.ToolsConfig{
			Exec: config.ExecConfig{
				ToolConfig:  config.ToolConfig{Enabled: true},
				AllowRemote: true,
			},
		},
	})
	if err != nil {
		t.Fatalf("NewExecToolWithConfig() error = %v", err)
	}
	command := "pwd"
	if runtime.GOOS == "windows" {
		command = "(Get-Location).Path"
	}

	result := NewCodexExecCommandTool(execTool).Execute(context.Background(), map[string]any{
		"cmd":     "  " + command + "  ",
		"workdir": "  " + workdir + "  ",
	})
	if result.IsError {
		t.Fatalf("exec_command returned error: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, filepath.Clean(workdir)) {
		t.Fatalf("ForLLM = %q, want working directory %q", result.ForLLM, workdir)
	}
}

func TestCodexExecCommandTool_BackgroundProjectionIsCrossPlatformAndDetached(t *testing.T) {
	backendResult := &ToolResult{
		ForLLM:       `{"sessionId":"session-cross-platform","status":"running"}`,
		ForUser:      "native user result",
		Silent:       true,
		Media:        []string{"media-canary"},
		ArtifactTags: []string{"artifact-canary"},
	}
	var captured map[string]any
	backend := codexExecBackendFunc(func(_ context.Context, args map[string]any) *ToolResult {
		captured = make(map[string]any, len(args))
		for key, value := range args {
			captured[key] = value
		}
		return backendResult
	})

	result := (&CodexExecCommandTool{exec: backend}).Execute(context.Background(), map[string]any{
		"cmd":        "true",
		"background": true,
	})
	if result.IsError {
		t.Fatalf("background projection returned error: %#v", result)
	}
	const want = `{"session_id":"session-cross-platform","status":"running"}`
	if result.ForLLM != want {
		t.Fatalf("projected response = %q, want %q", result.ForLLM, want)
	}
	if captured["action"] != "run" || captured["command"] != "true" ||
		captured["background"] != true {
		t.Fatalf("captured backend args = %#v", captured)
	}
	if result == backendResult || backendResult.ForLLM == result.ForLLM ||
		result.ForUser != backendResult.ForUser || result.Silent != backendResult.Silent {
		t.Fatalf("projection alias/preservation = projected %#v, backend %#v", result, backendResult)
	}
	result.Media[0] = "projected-media"
	result.ArtifactTags[0] = "projected-artifact"
	if backendResult.Media[0] != "media-canary" ||
		backendResult.ArtifactTags[0] != "artifact-canary" {
		t.Fatalf("projection retained backend slice aliases: %#v", backendResult)
	}
}

func TestCodexCompatibilityBackendAndProjectionDefenses(t *testing.T) {
	var typedNil *ExecTool
	if result := (&CodexExecCommandTool{exec: typedNil}).Execute(
		context.Background(),
		map[string]any{"cmd": "true"},
	); !result.IsError || !strings.Contains(result.ForLLM, "backend not configured") {
		t.Fatalf("typed-nil exec backend result = %#v", result)
	}
	if result := (&CodexWriteStdinTool{exec: typedNil}).Execute(
		context.Background(),
		map[string]any{"session_id": "session", "chars": "x"},
	); !result.IsError || !strings.Contains(result.ForLLM, "backend not configured") {
		t.Fatalf("typed-nil stdin backend result = %#v", result)
	}

	nilBackend := codexExecBackendFunc(func(context.Context, map[string]any) *ToolResult { return nil })
	if result := (&CodexExecCommandTool{exec: nilBackend}).Execute(
		context.Background(),
		map[string]any{"cmd": "true"},
	); !result.IsError || !strings.Contains(result.ForLLM, "returned no result") {
		t.Fatalf("nil exec result = %#v", result)
	}
	if result := (&CodexWriteStdinTool{exec: nilBackend}).Execute(
		context.Background(),
		map[string]any{"session_id": "session", "chars": "x"},
	); !result.IsError || !strings.Contains(result.ForLLM, "returned no result") {
		t.Fatalf("nil stdin result = %#v", result)
	}

	sentinel := errors.New("backend sentinel")
	backendError := &ToolResult{ForLLM: "backend failure", IsError: true, Err: sentinel}
	errorBackend := codexExecBackendFunc(func(context.Context, map[string]any) *ToolResult {
		return backendError
	})
	if result := (&CodexExecCommandTool{exec: errorBackend}).Execute(
		context.Background(),
		map[string]any{"cmd": "true", "background": true},
	); result != backendError || !errors.Is(result.Err, sentinel) {
		t.Fatalf("backend error was not preserved: %#v", result)
	}
}

func TestProjectCodexSessionResultRejectsInvalidBackendResponses(t *testing.T) {
	tests := []struct {
		name              string
		forLLM            string
		expectedSessionID string
		want              string
	}{
		{name: "malformed", forLLM: "not-json", want: "invalid session response"},
		{name: "missing identity", forLLM: `{"status":"running"}`, want: "incomplete"},
		{name: "missing status", forLLM: `{"sessionId":"session"}`, want: "incomplete"},
		{
			name:              "mismatched identity",
			forLLM:            `{"sessionId":"other","status":"running"}`,
			expectedSessionID: "session",
			want:              "mismatched session id",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := projectCodexSessionResult(
				&ToolResult{ForLLM: test.forLLM},
				test.expectedSessionID,
			)
			if !result.IsError || !strings.Contains(result.ForLLM, test.want) {
				t.Fatalf("projection result = %#v, want error containing %q", result, test.want)
			}
		})
	}
}

func TestCodexExecCommandTool_BackgroundPTYProjectsSnakeCaseAndChains(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PTY is not supported on Windows")
	}
	execTool, err := NewExecTool(t.TempDir(), false)
	if err != nil {
		t.Fatalf("NewExecTool() error = %v", err)
	}
	manager := NewSessionManager()
	manager.Stop()
	execTool.sessionManager = manager
	ctx := context.Background()
	t.Cleanup(func() {
		for _, info := range manager.List() {
			session, cleanupErr := manager.Get(info.ID)
			if cleanupErr != nil {
				t.Errorf("cleanup Get(%q): %v", info.ID, cleanupErr)
				continue
			}
			if !session.IsDone() {
				if cleanupErr = session.Kill(); cleanupErr != nil {
					t.Errorf("cleanup Kill(%q): %v", info.ID, cleanupErr)
				}
			}
			if session.ptyMaster != nil {
				if cleanupErr = session.ptyMaster.Close(); cleanupErr != nil &&
					!errors.Is(cleanupErr, os.ErrClosed) {
					t.Errorf("cleanup PTY close(%q): %v", info.ID, cleanupErr)
				}
			}
			manager.Remove(info.ID)
		}
	})

	background := NewCodexExecCommandTool(execTool).Execute(ctx, map[string]any{
		"cmd":        "cat",
		"background": true,
		"tty":        true,
	})
	if background.IsError {
		t.Fatalf("background exec_command returned error: %s", background.ForLLM)
	}
	var started codexSessionResponse
	if err = json.Unmarshal([]byte(background.ForLLM), &started); err != nil {
		t.Fatalf("background response is not JSON: %v; %s", err, background.ForLLM)
	}
	if started.SessionID == "" {
		t.Fatalf("background response = %#v, want session id", started)
	}
	if started.Status != "running" {
		t.Fatalf("background response = %#v, want running status", started)
	}
	wantBackground := fmt.Sprintf(`{"session_id":%q,"status":"running"}`, started.SessionID)
	if background.ForLLM != wantBackground || strings.Contains(background.ForLLM, "sessionId") {
		t.Fatalf("background response = %q, want exact %q", background.ForLLM, wantBackground)
	}
	session, err := manager.Get(started.SessionID)
	if err != nil {
		t.Fatalf("Get(%q) error = %v", started.SessionID, err)
	}
	if !session.PTY {
		t.Fatal("tty=true did not create a PTY-backed background session")
	}
	written := NewCodexWriteStdinTool(execTool).Execute(ctx, map[string]any{
		"session_id": started.SessionID,
		"chars":      "chained input\n",
	})
	if written.IsError {
		t.Fatalf("write_stdin returned error: %s", written.ForLLM)
	}
	wantWritten := fmt.Sprintf(`{"session_id":%q,"status":"running"}`, started.SessionID)
	if written.ForLLM != wantWritten || strings.Contains(written.ForLLM, "sessionId") {
		t.Fatalf("write response = %q, want exact %q", written.ForLLM, wantWritten)
	}
}

func TestCodexWriteStdinTool_ForwardsExactCharactersWithoutConsumingOutput(t *testing.T) {
	manager := NewSessionManager()
	manager.Stop()
	var stdin bytes.Buffer
	bufferedOutput := bytes.NewBufferString("buffered-output-canary")
	manager.Add(&ProcessSession{
		ID:           "memory-session",
		Status:       "running",
		stdinWriter:  &stdin,
		outputBuffer: bufferedOutput,
	})
	execTool := &ExecTool{sessionManager: manager}
	chars := " \n\x00\tcontrol\r"

	result := NewCodexWriteStdinTool(execTool).Execute(context.Background(), map[string]any{
		"session_id": "  memory-session  ",
		"chars":      chars,
	})
	if result.IsError {
		t.Fatalf("write_stdin returned error: %s", result.ForLLM)
	}
	if got := stdin.String(); got != chars {
		t.Fatalf("forwarded chars = %q, want exact %q", got, chars)
	}
	if got := bufferedOutput.String(); got != "buffered-output-canary" {
		t.Fatalf("buffered output changed to %q", got)
	}
	const want = `{"session_id":"memory-session","status":"running"}`
	if result.ForLLM != want || strings.Contains(result.ForLLM, "buffered-output-canary") {
		t.Fatalf("write response = %q, want exact status-only %q", result.ForLLM, want)
	}
}

func TestCodexWriteStdinTool_PropagatesSessionLifecycleErrors(t *testing.T) {
	manager := &SessionManager{sessions: map[string]*ProcessSession{
		"exited-session": {
			ID:          "exited-session",
			Status:      "done",
			ExitCode:    23,
			stdinWriter: &bytes.Buffer{},
		},
		"no-stdin-session": {
			ID:     "no-stdin-session",
			Status: "running",
		},
	}}
	tool := NewCodexWriteStdinTool(&ExecTool{sessionManager: manager})
	tests := []struct {
		name      string
		sessionID string
		want      string
	}{
		{name: "unknown session", sessionID: "missing-session", want: "session not found"},
		{name: "completed session", sessionID: "exited-session", want: "already exited with code 23"},
		{name: "session without stdin", sessionID: "no-stdin-session", want: "no stdin available"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := tool.Execute(context.Background(), map[string]any{
				"session_id": test.sessionID,
				"chars":      "exact input",
			})
			if result == nil || !result.IsError || !strings.Contains(result.ForLLM, test.want) {
				t.Fatalf("write result = %#v, want error containing %q", result, test.want)
			}
		})
	}
}

func TestProcessSessionStateAndInputGuards(t *testing.T) {
	session := &ProcessSession{ID: "state-session", Status: "running"}

	session.SetPtyKeyMode(PtyKeyModeSS3)
	if got := session.GetPtyKeyMode(); got != PtyKeyModeSS3 {
		t.Fatalf("PTY key mode = %v, want SS3", got)
	}
	session.SetExitCode(37)
	if got := session.GetExitCode(); got != 37 {
		t.Fatalf("exit code = %d, want 37", got)
	}
	session.SetStatus("exited")
	if got := session.GetStatus(); got != "exited" || !session.IsDone() {
		t.Fatalf("status = %q done=%t, want exited/done", got, session.IsDone())
	}
	if err := session.Write("ignored"); !errors.Is(err, ErrSessionDone) {
		t.Fatalf("Write on exited session error = %v, want ErrSessionDone", err)
	}
	if err := session.Kill(); !errors.Is(err, ErrSessionDone) {
		t.Fatalf("Kill on exited session error = %v, want ErrSessionDone", err)
	}

	session.SetStatus("running")
	if err := session.Write("ignored"); !errors.Is(err, ErrNoStdin) {
		t.Fatalf("Write without stdin error = %v, want ErrNoStdin", err)
	}
	if err := session.Kill(); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Kill without PID error = %v, want ErrSessionNotFound", err)
	}
}

func TestSessionManagerCleanupRemovesOnlyOldCompletedSessions(t *testing.T) {
	now := time.Now()
	old := now.Add(-31 * time.Minute).Unix()
	recent := now.Add(-29 * time.Minute).Unix()
	manager := &SessionManager{sessions: map[string]*ProcessSession{
		"old-done":    {ID: "old-done", Status: "done", StartTime: old},
		"old-exited":  {ID: "old-exited", Status: "exited", StartTime: old},
		"old-running": {ID: "old-running", Status: "running", StartTime: old},
		"recent-done": {ID: "recent-done", Status: "done", StartTime: recent},
	}}

	manager.cleanupOldSessions()
	for _, removed := range []string{"old-done", "old-exited"} {
		if _, err := manager.Get(removed); !errors.Is(err, ErrSessionNotFound) {
			t.Fatalf("old completed session %q was retained: %v", removed, err)
		}
	}
	for _, retained := range []string{"old-running", "recent-done"} {
		if session, err := manager.Get(retained); err != nil || session.ID != retained {
			t.Fatalf("eligible session %q was removed: session=%#v err=%v", retained, session, err)
		}
	}
}

func TestCodexCompatibilityRegistryValidationNeverDispatchesInvalidCalls(t *testing.T) {
	var calls int
	backend := codexExecBackendFunc(func(context.Context, map[string]any) *ToolResult {
		calls++
		return SilentResult(`{"sessionId":"unexpected","status":"running"}`)
	})
	registry := NewToolRegistry()
	registry.Register(&CodexExecCommandTool{exec: backend})
	registry.Register(&CodexWriteStdinTool{exec: backend})

	tests := []struct {
		name string
		tool string
		args map[string]any
	}{
		{
			name: "exec schema rejection",
			tool: "exec_command",
			args: map[string]any{"cmd": "true", "yield_time_ms": 1},
		},
		{
			name: "exec semantic rejection",
			tool: "exec_command",
			args: map[string]any{"cmd": "true", "tty": true},
		},
		{
			name: "stdin schema rejection",
			tool: "write_stdin",
			args: map[string]any{"session_id": "session", "chars": "x", "max_output_tokens": 1},
		},
		{
			name: "stdin semantic rejection",
			tool: "write_stdin",
			args: map[string]any{"session_id": "session", "chars": ""},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := registry.Execute(context.Background(), test.tool, test.args)
			if result == nil || !result.IsError {
				t.Fatalf("invalid registry call returned %#v, want error", result)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("backend calls = %d, want zero", calls)
	}
}

func TestUpdatePlanTool_RejectsMultipleInProgressItems(t *testing.T) {
	result := NewUpdatePlanTool().Execute(context.Background(), map[string]any{
		"plan": []any{
			map[string]any{"step": "first", "status": "in_progress"},
			map[string]any{"step": "second", "status": "in_progress"},
		},
	})
	if !result.IsError {
		t.Fatalf("expected multiple in_progress items to fail, got: %s", result.ForLLM)
	}
}

func TestCodexViewImageTool_ForwardsPathToLoader(t *testing.T) {
	loader := &mockRegistryTool{
		name:   "load_image",
		desc:   "load",
		params: map[string]any{"type": "object"},
		result: SilentResult("loaded"),
	}

	result := NewCodexViewImageTool(loader).Execute(context.Background(), map[string]any{
		"path":   "image.png",
		"detail": "high",
	})
	if result.IsError {
		t.Fatalf("view_image returned error: %s", result.ForLLM)
	}
	if result.ForLLM != "loaded" {
		t.Fatalf("ForLLM = %q, want loaded", result.ForLLM)
	}
}

func TestCodexViewImageTool_ForwardsMediaStoreToPrivateLoader(t *testing.T) {
	loader := &mockMediaStoreAwareTool{
		mockRegistryTool: *newMockTool("load_image", "load"),
	}
	store := media.NewFileMediaStore()
	NewCodexViewImageTool(loader).SetMediaStore(store)
	if loader.store != store {
		t.Fatal("view_image did not forward media-store injection to its loader")
	}

	NewCodexViewImageTool(newMockTool("load_image", "load")).SetMediaStore(store)
	var nilView *CodexViewImageTool
	nilView.SetMediaStore(store)
}
