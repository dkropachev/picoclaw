package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/isolation"
	"github.com/sipeed/picoclaw/pkg/providers"
)

const (
	processHookHelperEnvReportPath = "PICOCLAW_HOOK_ENV_REPORT_PATH"
	processHookHelperEnvReportKeys = "PICOCLAW_HOOK_ENV_REPORT_KEYS"
)

type processHookHelperEnvObservation struct {
	Present bool   `json:"present"`
	Value   string `json:"value"`
}

func TestProcessHook_HelperProcess(t *testing.T) {
	if os.Getenv("PICOCLAW_HOOK_HELPER") != "1" {
		return
	}
	if err := runProcessHookHelper(); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	os.Exit(0)
}

func TestNewProcessHookWithExecutionPolicy_ZeroFailsClosed(t *testing.T) {
	reportPath := filepath.Join(t.TempDir(), "environment.json")
	opts := ProcessHookOptions{
		Command: processHookHelperCommand(),
		Env: append(
			processHookHelperEnv("rewrite", ""),
			processHookEnvironmentReportEnv(reportPath, "P014_ZERO_POLICY_STARTED")...,
		),
		InterceptLLM: true,
	}

	hook, err := NewProcessHookWithExecutionPolicy(
		context.Background(),
		"zero-policy",
		opts,
		isolation.ExecutionPolicy{},
	)
	if hook != nil {
		_ = hook.Close()
		t.Fatal("NewProcessHookWithExecutionPolicy() returned a hook for zero policy")
	}
	if !errors.Is(err, isolation.ErrExecutionPolicyUnavailable) {
		t.Fatalf("NewProcessHookWithExecutionPolicy() error = %v, want execution policy unavailable", err)
	}
	if _, statErr := os.Stat(reportPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("zero-policy hook process started; environment report stat error = %v", statErr)
	}
}

func TestNewProcessHook_UsesDisabledRestrictedCompatibilityPolicy(t *testing.T) {
	const ambientKey = "P014_HOOK_COMPAT_AMBIENT_SECRET"
	t.Cleanup(func() {
		isolation.Configure(config.DefaultConfig())
	})
	t.Setenv("PATH", t.TempDir())
	t.Setenv(ambientKey, "must-not-pass")

	legacyCfg := config.DefaultConfig()
	legacyCfg.Isolation.Enabled = true
	isolation.Configure(legacyCfg)

	command := processHookHelperCommand()
	absoluteCommand, err := filepath.Abs(command[0])
	if err != nil {
		t.Fatalf("filepath.Abs() error = %v", err)
	}
	command[0] = absoluteCommand
	reportPath := filepath.Join(t.TempDir(), "environment.json")
	env := processHookHelperEnv("rewrite", "")
	env = append(env, processHookEnvironmentReportEnv(reportPath, ambientKey)...)
	hook, err := NewProcessHook(context.Background(), "compatibility-policy", ProcessHookOptions{
		Command:      command,
		Env:          env,
		InterceptLLM: true,
	})
	if err != nil {
		t.Fatalf("NewProcessHook() error = %v", err)
	}
	defer hook.Close()

	if got := readProcessHookEnvironmentReport(t, reportPath)[ambientKey]; got.Present {
		t.Fatalf("compatibility policy inherited ambient secret: %+v", got)
	}
}

func TestNewProcessHookWithExecutionPolicy_ExplicitEnvironmentDoesNotInheritAmbient(t *testing.T) {
	const (
		ambientKey  = "P014_HOOK_AMBIENT_SECRET"
		explicitKey = "P014_HOOK_EXPLICIT_VALUE"
		emptyKey    = "P014_HOOK_EXPLICIT_EMPTY"
	)
	t.Setenv(ambientKey, "must-not-pass")
	t.Setenv(explicitKey, "ambient-value")

	reportPath := filepath.Join(t.TempDir(), "environment.json")
	env := processHookHelperEnv("rewrite", "")
	env = append(env, processHookEnvironmentReportEnv(
		reportPath,
		ambientKey,
		explicitKey,
		emptyKey,
	)...)
	env = append(env, explicitKey+"=explicit-value", emptyKey+"=")

	hook, err := NewProcessHookWithExecutionPolicy(
		context.Background(),
		"restricted-environment",
		ProcessHookOptions{
			Command:      processHookHelperCommand(),
			Env:          env,
			InterceptLLM: true,
		},
		isolation.NewExecutionPolicy(config.DefaultConfig().Isolation),
	)
	if err != nil {
		t.Fatalf("NewProcessHookWithExecutionPolicy() error = %v", err)
	}
	defer hook.Close()

	report := readProcessHookEnvironmentReport(t, reportPath)
	if got := report[ambientKey]; got.Present {
		t.Fatalf("ambient secret was inherited: %+v", got)
	}
	if got := report[explicitKey]; !got.Present || got.Value != "explicit-value" {
		t.Fatalf("explicit environment value = %+v, want present explicit-value", got)
	}
	if got := report[emptyKey]; !got.Present || got.Value != "" {
		t.Fatalf("explicit empty environment value = %+v, want present empty", got)
	}
}

func TestNewProcessHookWithExecutionPolicy_UsesExactCapturedPolicy(t *testing.T) {
	const policyKey = "P014_HOOK_POLICY_GENERATION"
	isolationCfg := config.DefaultConfig().Isolation
	isolationCfg.EnvironmentAllowlist = []string{policyKey}

	t.Setenv(policyKey, "generation-a")
	policyA := isolation.NewExecutionPolicy(isolationCfg)
	t.Setenv(policyKey, "generation-b")
	policyB := isolation.NewExecutionPolicy(isolationCfg)
	t.Setenv(policyKey, "live-parent")

	for _, test := range []struct {
		name   string
		policy isolation.ExecutionPolicy
		want   string
	}{
		{name: "generation a", policy: policyA, want: "generation-a"},
		{name: "generation b", policy: policyB, want: "generation-b"},
	} {
		t.Run(test.name, func(t *testing.T) {
			reportPath := filepath.Join(t.TempDir(), "environment.json")
			env := processHookHelperEnv("rewrite", "")
			env = append(env, processHookEnvironmentReportEnv(reportPath, policyKey)...)
			hook, err := NewProcessHookWithExecutionPolicy(
				context.Background(),
				test.name,
				ProcessHookOptions{
					Command:      processHookHelperCommand(),
					Env:          env,
					InterceptLLM: true,
				},
				test.policy,
			)
			if err != nil {
				t.Fatalf("NewProcessHookWithExecutionPolicy() error = %v", err)
			}
			defer hook.Close()

			got := readProcessHookEnvironmentReport(t, reportPath)[policyKey]
			if !got.Present || got.Value != test.want {
				t.Fatalf("policy environment = %+v, want present %q", got, test.want)
			}
		})
	}
}

func TestAgentLoop_MountProcessHook_LLMAndObserver(t *testing.T) {
	provider := &llmHookTestProvider{}
	al, agent, cleanup := newHookTestLoop(t, provider)
	defer cleanup()

	eventLog := filepath.Join(t.TempDir(), "events.log")
	if err := al.MountProcessHook(context.Background(), "ipc-llm", ProcessHookOptions{
		Command:      processHookHelperCommand(),
		Env:          processHookHelperEnv("rewrite", eventLog),
		Trusted:      true,
		Observe:      true,
		InterceptLLM: true,
	}); err != nil {
		t.Fatalf("MountProcessHook failed: %v", err)
	}

	resp, err := al.runAgentLoop(context.Background(), agent, processOptions{
		SessionKey:      "session-1",
		Channel:         "cli",
		ChatID:          "direct",
		UserMessage:     "hello",
		DefaultResponse: defaultResponse,
		EnableSummary:   false,
		SendResponse:    false,
	})
	if err != nil {
		t.Fatalf("runAgentLoop failed: %v", err)
	}
	if resp != "provider content|ipc" {
		t.Fatalf("expected process-hooked llm content, got %q", resp)
	}

	provider.mu.Lock()
	lastModel := provider.lastModel
	provider.mu.Unlock()
	if lastModel != "process-model" {
		t.Fatalf("expected process model, got %q", lastModel)
	}

	waitForFileContains(t, eventLog, "agent.turn.end")
}

func TestAgentLoop_MountProcessHook_DefaultTrustDiscardsRewrite(t *testing.T) {
	provider := &llmHookTestProvider{}
	al, agent, cleanup := newHookTestLoop(t, provider)
	defer cleanup()

	if err := al.MountProcessHook(context.Background(), "ipc-untrusted", ProcessHookOptions{
		Command:      processHookHelperCommand(),
		Env:          processHookHelperEnv("rewrite", ""),
		InterceptLLM: true,
	}); err != nil {
		t.Fatalf("MountProcessHook failed: %v", err)
	}

	resp, err := al.runAgentLoop(context.Background(), agent, processOptions{
		SessionKey:      "session-untrusted",
		Channel:         "cli",
		ChatID:          "direct",
		UserMessage:     "hello",
		DefaultResponse: defaultResponse,
		EnableSummary:   false,
		SendResponse:    false,
	})
	if err != nil {
		t.Fatalf("runAgentLoop failed: %v", err)
	}
	if resp != "provider content" {
		t.Fatalf("response = %q, want unmodified provider content", resp)
	}

	provider.mu.Lock()
	lastModel := provider.lastModel
	provider.mu.Unlock()
	if lastModel != "test-model" {
		t.Fatalf("model = %q, want original test-model", lastModel)
	}
}

func TestAgentLoop_MountProcessHook_ToolRewrite(t *testing.T) {
	provider := &toolHookProvider{}
	al, agent, cleanup := newHookTestLoop(t, provider)
	defer cleanup()

	al.RegisterTool(&echoTextTool{})
	if err := al.MountProcessHook(context.Background(), "ipc-tool", ProcessHookOptions{
		Command:       processHookHelperCommand(),
		Env:           processHookHelperEnv("rewrite", ""),
		Trusted:       true,
		InterceptTool: true,
	}); err != nil {
		t.Fatalf("MountProcessHook failed: %v", err)
	}

	resp, err := al.runAgentLoop(context.Background(), agent, processOptions{
		SessionKey:      "session-1",
		Channel:         "cli",
		ChatID:          "direct",
		UserMessage:     "run tool",
		DefaultResponse: defaultResponse,
		EnableSummary:   false,
		SendResponse:    false,
	})
	if err != nil {
		t.Fatalf("runAgentLoop failed: %v", err)
	}
	if resp != "ipc:ipc" {
		t.Fatalf("expected rewritten process-hook tool result, got %q", resp)
	}
}

type blockedToolProvider struct {
	calls int
}

func (p *blockedToolProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	p.calls++
	if p.calls == 1 {
		return &providers.LLMResponse{
			ToolCalls: []providers.ToolCall{
				{
					ID:        "call-1",
					Name:      "echo_text",
					Arguments: map[string]any{},
				},
			},
		}, nil
	}

	return &providers.LLMResponse{
		Content: messages[len(messages)-1].Content,
	}, nil
}

func TestAgentLoop_MountProcessHook_ApprovalDeny(t *testing.T) {
	provider := &blockedToolProvider{}
	al, agent, cleanup := newHookTestLoop(t, provider)
	defer cleanup()
	al.RegisterTool(&echoTextTool{})

	if err := al.MountProcessHook(context.Background(), "ipc-approval", ProcessHookOptions{
		Command:     processHookHelperCommand(),
		Env:         processHookHelperEnv("deny", ""),
		ApproveTool: true,
	}); err != nil {
		t.Fatalf("MountProcessHook failed: %v", err)
	}

	runtimeCh, closeRuntimeEvents := subscribeRuntimeEventsForTest(
		t,
		al,
		16,
		runtimeevents.KindAgentToolExecSkipped,
	)
	defer closeRuntimeEvents()

	resp, err := al.runAgentLoop(context.Background(), agent, processOptions{
		SessionKey:      "session-1",
		Channel:         "cli",
		ChatID:          "direct",
		UserMessage:     "run blocked tool",
		DefaultResponse: defaultResponse,
		EnableSummary:   false,
		SendResponse:    false,
	})
	if err != nil {
		t.Fatalf("runAgentLoop failed: %v", err)
	}

	expected := toolApprovalDeniedMessage
	if resp != expected {
		t.Fatalf("expected %q, got %q", expected, resp)
	}

	events := collectRuntimeEventStream(runtimeCh)
	skippedEvt, ok := findRuntimeEvent(events, runtimeevents.KindAgentToolExecSkipped)
	if !ok {
		t.Fatal("expected tool skipped event")
	}
	payload, ok := skippedEvt.Payload.(ToolExecSkippedPayload)
	if !ok {
		t.Fatalf("expected ToolExecSkippedPayload, got %T", skippedEvt.Payload)
	}
	if payload.Reason != expected {
		t.Fatalf("expected reason %q, got %q", expected, payload.Reason)
	}
}

func TestAgentLoop_MountProcessHook_IsolationSupportsRelativeDirAndCommand(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux-only isolation path handling")
	}

	root := t.TempDir()
	t.Setenv(config.EnvHome, filepath.Join(root, "picoclaw-home"))
	binDir := filepath.Join(root, "bin")
	hookDir := filepath.Join(root, "hooks")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeBwrap(t, filepath.Join(binDir, "bwrap"))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	linkTestBinary(t, os.Args[0], filepath.Join(hookDir, "hook-helper"))

	cfg := config.DefaultConfig()
	cfg.Isolation.Enabled = true
	policy := isolation.NewExecutionPolicy(cfg.Isolation)

	provider := &llmHookTestProvider{}
	al, agent, cleanup := newHookTestLoop(t, provider, policy)
	defer cleanup()

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relHookDir, err := filepath.Rel(cwd, hookDir)
	if err != nil {
		t.Fatal(err)
	}

	mountErr := al.MountProcessHook(context.Background(), "ipc-relative", ProcessHookOptions{
		Command:      []string{"./hook-helper", "-test.run=TestProcessHook_HelperProcess", "--"},
		Dir:          relHookDir,
		Env:          processHookHelperEnv("rewrite", ""),
		Trusted:      true,
		InterceptLLM: true,
	})
	if mountErr != nil {
		t.Fatalf("MountProcessHook failed with relative dir/command under isolation: %v", mountErr)
	}

	resp, err := al.runAgentLoop(context.Background(), agent, processOptions{
		SessionKey:      "session-relative",
		Channel:         "cli",
		ChatID:          "direct",
		UserMessage:     "hello",
		DefaultResponse: defaultResponse,
		EnableSummary:   false,
		SendResponse:    false,
	})
	if err != nil {
		t.Fatalf("runAgentLoop failed: %v", err)
	}
	if resp != "provider content|ipc" {
		t.Fatalf("expected process-hooked llm content, got %q", resp)
	}
	provider.mu.Lock()
	lastModel := provider.lastModel
	provider.mu.Unlock()
	if lastModel != "process-model" {
		t.Fatalf("expected process model, got %q", lastModel)
	}
}

func processHookHelperCommand() []string {
	return []string{os.Args[0], "-test.run=TestProcessHook_HelperProcess", "--"}
}

func processHookHelperEnv(mode, eventLog string) []string {
	env := []string{
		"PICOCLAW_HOOK_HELPER=1",
		"PICOCLAW_HOOK_MODE=" + mode,
	}
	if coverageDir, ok := os.LookupEnv("GOCOVERDIR"); ok {
		env = append(env, "GOCOVERDIR="+coverageDir)
	}
	if eventLog != "" {
		env = append(env, "PICOCLAW_HOOK_EVENT_LOG="+eventLog)
	}
	return env
}

func processHookHelperEnvMap(mode, eventLog string) map[string]string {
	env := map[string]string{
		"PICOCLAW_HOOK_HELPER": "1",
		"PICOCLAW_HOOK_MODE":   mode,
	}
	if coverageDir, ok := os.LookupEnv("GOCOVERDIR"); ok {
		env["GOCOVERDIR"] = coverageDir
	}
	if eventLog != "" {
		env["PICOCLAW_HOOK_EVENT_LOG"] = eventLog
	}
	return env
}

func processHookEnvironmentReportEnv(path string, keys ...string) []string {
	return []string{
		processHookHelperEnvReportPath + "=" + path,
		processHookHelperEnvReportKeys + "=" + strings.Join(keys, ","),
	}
}

func readProcessHookEnvironmentReport(
	t *testing.T,
	path string,
) map[string]processHookHelperEnvObservation {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read process hook environment report: %v", err)
	}
	var report map[string]processHookHelperEnvObservation
	if err := json.Unmarshal(body, &report); err != nil {
		t.Fatalf("decode process hook environment report: %v", err)
	}
	return report
}

func writeFakeBwrap(t *testing.T, path string) {
	t.Helper()
	script := `#!/bin/sh
set -eu
workdir=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --)
      shift
      break
      ;;
    --chdir)
      workdir="$2"
      shift 2
      ;;
    --bind|--ro-bind)
      shift 3
      ;;
    --proc|--dev)
      shift 2
      ;;
    --die-with-parent|--unshare-ipc)
      shift
      ;;
    *)
      shift
      ;;
  esac
done
if [ -n "$workdir" ]; then
  cd "$workdir"
fi
exec "$@"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bwrap: %v", err)
	}
}

func linkTestBinary(t *testing.T, source, target string) {
	t.Helper()
	if err := os.Symlink(source, target); err == nil {
		return
	}
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read test binary: %v", err)
	}
	if err := os.WriteFile(target, data, 0o755); err != nil {
		t.Fatalf("create hook helper binary: %v", err)
	}
}

func waitForFileContains(t *testing.T, path, substring string) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), substring) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	data, _ := os.ReadFile(path)
	t.Fatalf("timed out waiting for %q in %s; current content: %q", substring, path, string(data))
}

func runProcessHookHelper() error {
	if err := writeProcessHookEnvironmentReport(); err != nil {
		return err
	}
	mode := os.Getenv("PICOCLAW_HOOK_MODE")
	eventLog := os.Getenv("PICOCLAW_HOOK_EVENT_LOG")

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), processHookReadBufferSize)
	encoder := json.NewEncoder(os.Stdout)

	for scanner.Scan() {
		var msg processHookRPCMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			return err
		}

		if msg.ID == 0 {
			if msg.Method == "hook.runtime_event" && eventLog != "" {
				var evt map[string]any
				if err := json.Unmarshal(msg.Params, &evt); err == nil {
					if kind, ok := evt["kind"].(string); ok {
						_ = os.WriteFile(eventLog, []byte(kind+"\n"), 0o644)
					}
				}
			}
			continue
		}

		result, rpcErr := handleProcessHookRequest(mode, msg)
		resp := processHookRPCMessage{
			JSONRPC: processHookJSONRPCVersion,
			ID:      msg.ID,
		}
		if rpcErr != nil {
			resp.Error = rpcErr
		} else if result != nil {
			body, err := json.Marshal(result)
			if err != nil {
				return err
			}
			resp.Result = body
		} else {
			resp.Result = []byte("{}")
		}

		if err := encoder.Encode(resp); err != nil {
			return err
		}
	}

	return scanner.Err()
}

func writeProcessHookEnvironmentReport() error {
	path := os.Getenv(processHookHelperEnvReportPath)
	if path == "" {
		return nil
	}

	report := make(map[string]processHookHelperEnvObservation)
	for _, key := range strings.Split(os.Getenv(processHookHelperEnvReportKeys), ",") {
		if key == "" {
			continue
		}
		value, present := os.LookupEnv(key)
		report[key] = processHookHelperEnvObservation{
			Present: present,
			Value:   value,
		}
	}
	body, err := json.Marshal(report)
	if err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o600)
}

func handleProcessHookRequest(mode string, msg processHookRPCMessage) (any, *processHookRPCError) {
	switch msg.Method {
	case "hook.hello":
		return map[string]any{"ok": true}, nil
	case "hook.before_llm":
		if mode != "rewrite" {
			return map[string]any{"action": HookActionContinue}, nil
		}
		var req map[string]any
		_ = json.Unmarshal(msg.Params, &req)
		req["model"] = "process-model"
		return map[string]any{
			"action":  HookActionModify,
			"request": req,
		}, nil
	case "hook.after_llm":
		if mode != "rewrite" {
			return map[string]any{"action": HookActionContinue}, nil
		}
		var resp map[string]any
		_ = json.Unmarshal(msg.Params, &resp)
		if rawResponse, ok := resp["response"].(map[string]any); ok {
			if content, ok := rawResponse["content"].(string); ok {
				rawResponse["content"] = content + "|ipc"
			}
		}
		return map[string]any{
			"action":   HookActionModify,
			"response": resp,
		}, nil
	case "hook.before_tool":
		if mode != "rewrite" {
			return map[string]any{"action": HookActionContinue}, nil
		}
		var call map[string]any
		_ = json.Unmarshal(msg.Params, &call)
		rawArgs, ok := call["arguments"].(map[string]any)
		if !ok || rawArgs == nil {
			rawArgs = map[string]any{}
		}
		rawArgs["text"] = "ipc"
		call["arguments"] = rawArgs
		return map[string]any{
			"action": HookActionModify,
			"call":   call,
		}, nil
	case "hook.after_tool":
		if mode != "rewrite" {
			return map[string]any{"action": HookActionContinue}, nil
		}
		var result map[string]any
		_ = json.Unmarshal(msg.Params, &result)
		if rawResult, ok := result["result"].(map[string]any); ok {
			if forLLM, ok := rawResult["for_llm"].(string); ok {
				rawResult["for_llm"] = "ipc:" + forLLM
			}
		}
		return map[string]any{
			"action": HookActionModify,
			"result": result,
		}, nil
	case "hook.approve_tool":
		if mode == "deny" {
			return ApprovalDecision{
				Approved: false,
				Reason:   "blocked by ipc hook",
			}, nil
		}
		return ApprovalDecision{Approved: true}, nil
	default:
		return nil, &processHookRPCError{
			Code:    -32601,
			Message: "method not found",
		}
	}
}
