package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
	ppid "github.com/sipeed/picoclaw/pkg/pid"
	"github.com/sipeed/picoclaw/web/backend/utils"
)

func startLongRunningProcess(t *testing.T) *exec.Cmd {
	t.Helper()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("powershell", "-NoProfile", "-Command", "Start-Sleep -Seconds 30")
	} else {
		cmd = exec.Command("sleep", "30")
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	return cmd
}

func startGatewayLikeProcess(t *testing.T) *exec.Cmd {
	t.Helper()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		t.Skip("gateway-like process commandline check is not deterministic on Windows tests")
	}
	cmd = exec.Command("sh", "-c", "sleep 30 # picoclaw gateway")

	if err := cmd.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	return cmd
}

func writeTestPidFile(t *testing.T, data ppid.PidFileData) string {
	t.Helper()

	path := filepath.Join(globalConfigDir(), ".picoclaw.pid")
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatalf("marshal pid file: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write pid file: %v", err)
	}
	return path
}

func mockGatewayHealthResponse(statusCode, pid int) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Body: io.NopCloser(strings.NewReader(
			`{"status":"ok","uptime":"1s","pid":` + strconv.Itoa(pid) + `}`,
		)),
	}
}

func startIgnoringTermProcess(t *testing.T) *exec.Cmd {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("TERM handling differs on Windows")
	}

	cmd := exec.Command("sh", "-c", "trap '' TERM; sleep 30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	return cmd
}

func resetGatewayTestState(t *testing.T) {
	t.Helper()

	originalHealthGet := gatewayHealthGet
	originalProcessMatcher := gatewayProcessMatcher
	originalExecCommand := gatewayExecCommand
	originalBeforeCommandStart := gatewayBeforeCommandStart
	originalRestartGracePeriod := gatewayRestartGracePeriod
	originalRestartForceKillWindow := gatewayRestartForceKillWindow
	originalRestartPollInterval := gatewayRestartPollInterval
	t.Setenv("PICOCLAW_HOME", t.TempDir())
	t.Cleanup(func() {
		gatewayHealthGet = originalHealthGet
		gatewayProcessMatcher = originalProcessMatcher
		gatewayExecCommand = originalExecCommand
		gatewayBeforeCommandStart = originalBeforeCommandStart
		gatewayRestartGracePeriod = originalRestartGracePeriod
		gatewayRestartForceKillWindow = originalRestartForceKillWindow
		gatewayRestartPollInterval = originalRestartPollInterval

		gateway.mu.Lock()
		gateway.cmd = nil
		gateway.pidData = nil
		gateway.owned = false
		gateway.bootDefaultModel = ""
		gateway.bootConfigSignature = ""
		setGatewayRuntimeStatusLocked("stopped")
		gateway.mu.Unlock()
	})
}

func TestPicoGatewayProtocol(t *testing.T) {
	resetGatewayTestState(t)

	gateway.mu.Lock()
	gateway.picoToken = "ui-token"
	gateway.mu.Unlock()

	if got := picoGatewayProtocol(); got != tokenPrefix+"ui-token" {
		t.Fatalf("picoGatewayProtocol() = %q, want %q", got, tokenPrefix+"ui-token")
	}
}

type gatewayStartEnvSnapshot struct {
	GatewayHost    string `json:"gateway_host"`
	GatewayHostSet bool   `json:"gateway_host_set"`
	ConfigPath     string `json:"config_path"`
}

func TestGatewayStartHelperProcess(t *testing.T) {
	var envPath string
	for i, arg := range os.Args {
		if arg == "--" && i+2 < len(os.Args) && os.Args[i+1] == "gateway-env-helper" {
			envPath = os.Args[i+2]
			break
		}
	}
	if envPath == "" {
		t.Skip("helper process")
	}

	host, ok := os.LookupEnv(config.EnvGatewayHost)
	raw, err := json.Marshal(gatewayStartEnvSnapshot{
		GatewayHost:    host,
		GatewayHostSet: ok,
		ConfigPath:     os.Getenv(config.EnvConfig),
	})
	if err != nil {
		_, _ = io.WriteString(os.Stderr, err.Error())
		os.Exit(2)
	}
	if err := os.WriteFile(envPath, raw, 0o600); err != nil {
		_, _ = io.WriteString(os.Stderr, err.Error())
		os.Exit(2)
	}
	os.Exit(0)
}

func unsetGatewayStartEnvForTest(t *testing.T, key string) {
	t.Helper()

	prev, hadPrev := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("Unsetenv(%q) error = %v", key, err)
	}
	t.Cleanup(func() {
		if hadPrev {
			_ = os.Setenv(key, prev)
			return
		}
		_ = os.Unsetenv(key)
	})
}

func newGatewayStartTestHandler(t *testing.T) *Handler {
	t.Helper()
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	h.SetServerOptions(18800, false, false, nil)
	return h
}

func addGatewayTestModel(cfg *config.Config) *config.ModelConfig {
	model := &config.ModelConfig{
		ModelName: "test-model",
		Model:     "openai/gpt-4.1",
		Provider:  "openai",
	}
	cfg.ModelList = append(cfg.ModelList, model)
	return model
}

func startGatewayAndCaptureEnv(t *testing.T, h *Handler) gatewayStartEnvSnapshot {
	t.Helper()

	unsetGatewayStartEnvForTest(t, config.EnvGatewayHost)

	envPath := filepath.Join(t.TempDir(), "gateway-child-env.json")
	gatewayExecCommand = func(_ string, _ ...string) *exec.Cmd {
		return exec.Command(
			os.Args[0],
			"-test.run=TestGatewayStartHelperProcess",
			"--",
			"gateway-env-helper",
			envPath,
		)
	}

	pid, err := h.startGatewayLocked("starting", 0)
	if err != nil {
		t.Fatalf("startGatewayLocked() error = %v", err)
	}
	if pid <= 0 {
		t.Fatalf("startGatewayLocked() pid = %d, want > 0", pid)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		raw, err := os.ReadFile(envPath)
		if err == nil {
			var snapshot gatewayStartEnvSnapshot
			err = json.Unmarshal(raw, &snapshot)
			if err != nil {
				t.Fatalf("Unmarshal(child env) error = %v", err)
			}
			return snapshot
		}
		if !os.IsNotExist(err) {
			t.Fatalf("ReadFile(%q) error = %v", envPath, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for gateway child env snapshot %q", envPath)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStartGatewayLocked_ForwardsLauncherHostOverrideToGatewayEnv(t *testing.T) {
	h := newGatewayStartTestHandler(t)
	h.SetServerBindHost("127.0.0.1,::1", true)

	snapshot := startGatewayAndCaptureEnv(t, h)
	if !snapshot.GatewayHostSet {
		t.Fatal("gateway host env was not set")
	}
	if snapshot.GatewayHost != "127.0.0.1,::1" {
		t.Fatalf("gateway host env = %q, want %q", snapshot.GatewayHost, "127.0.0.1,::1")
	}
	if snapshot.ConfigPath != h.configPath {
		t.Fatalf("config env = %q, want %q", snapshot.ConfigPath, h.configPath)
	}
}

func TestStartGatewayLocked_ForwardsLauncherHostFromEnvironmentToGatewayEnv(t *testing.T) {
	h := newGatewayStartTestHandler(t)
	h.SetServerBindHost("::", true)

	snapshot := startGatewayAndCaptureEnv(t, h)
	if !snapshot.GatewayHostSet {
		t.Fatal("gateway host env was not set")
	}
	if snapshot.GatewayHost != "::" {
		t.Fatalf("gateway host env = %q, want %q", snapshot.GatewayHost, "::")
	}
}

func TestStartGatewayLocked_ForwardsWildcardHostForPublicLauncher(t *testing.T) {
	h := newGatewayStartTestHandler(t)
	h.SetServerOptions(18800, true, true, nil)

	snapshot := startGatewayAndCaptureEnv(t, h)
	if !snapshot.GatewayHostSet {
		t.Fatal("gateway host env was not set")
	}
	if snapshot.GatewayHost != "*" {
		t.Fatalf("gateway host env = %q, want %q", snapshot.GatewayHost, "*")
	}
}

func TestStartGatewayLocked_UsesReloadedConfigForBootSignature(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep command differs on Windows")
	}

	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	delete(cfg.Channels, "pico")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	h.SetServerOptions(18800, false, false, nil)
	gatewayExecCommand = func(_ string, _ ...string) *exec.Cmd {
		return exec.Command("sleep", "30")
	}

	originalSignature := computeConfigSignature(cfg)
	pid, err := h.startGatewayLocked("starting", 0)
	if err != nil {
		t.Fatalf("startGatewayLocked() error = %v", err)
	}
	if pid <= 0 {
		t.Fatalf("startGatewayLocked() pid = %d, want > 0", pid)
	}

	gateway.mu.Lock()
	cmd := gateway.cmd
	bootSignature := gateway.bootConfigSignature
	gateway.mu.Unlock()
	t.Cleanup(func() {
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		if cmd != nil {
			_ = cmd.Wait()
		}
	})

	updatedCfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	expectedSignature := computeConfigSignature(updatedCfg)
	if expectedSignature == originalSignature {
		t.Fatal("expected EnsurePicoChannel() to change the config signature during gateway start")
	}
	if bootSignature != expectedSignature {
		t.Fatalf("bootConfigSignature = %q, want %q", bootSignature, expectedSignature)
	}
}

func TestStartGatewayLockedCapturesRuntimeSignatureBeforeChildStart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep command differs on Windows")
	}

	resetGatewayTestState(t)
	configPath := filepath.Join(t.TempDir(), "config.json")
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("MkdirAll(workspace) error = %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	definitionPath := filepath.Join(workspace, agentDefinitionFileCurrent)
	before := []byte("---\ntools: [exec]\n---\nbody\n")
	after := []byte("---\ntools: [write_file]\n---\nbody\n")
	if err := os.WriteFile(definitionPath, before, 0o600); err != nil {
		t.Fatalf("WriteFile(before AGENT.md) error = %v", err)
	}

	h := NewHandler(configPath)
	h.SetServerOptions(18800, false, false, nil)
	gatewayExecCommand = func(_ string, _ ...string) *exec.Cmd {
		return exec.Command("sleep", "30")
	}
	gatewayBeforeCommandStart = func() {
		if err := os.WriteFile(definitionPath, after, 0o600); err != nil {
			t.Fatalf("WriteFile(after AGENT.md) error = %v", err)
		}
	}

	pid, err := h.startGatewayLocked("starting", 0)
	if err != nil {
		t.Fatalf("startGatewayLocked() error = %v", err)
	}
	if pid <= 0 {
		t.Fatalf("startGatewayLocked() pid = %d, want > 0", pid)
	}
	gateway.mu.Lock()
	cmd := gateway.cmd
	bootSignature := gateway.bootConfigSignature
	gateway.mu.Unlock()
	t.Cleanup(func() {
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		if cmd != nil {
			_ = cmd.Wait()
		}
	})

	updatedCfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if err = os.WriteFile(definitionPath, before, 0o600); err != nil {
		t.Fatalf("restore before AGENT.md error = %v", err)
	}
	expectedBootSignature := computeGatewayRuntimeSignature(updatedCfg)
	if err = os.WriteFile(definitionPath, after, 0o600); err != nil {
		t.Fatalf("restore after AGENT.md error = %v", err)
	}
	currentSignature := computeGatewayRuntimeSignature(updatedCfg)
	if bootSignature != expectedBootSignature {
		t.Fatalf(
			"bootConfigSignature = %q, want pre-start %q",
			bootSignature,
			expectedBootSignature,
		)
	}
	if currentSignature == bootSignature ||
		!gatewayRestartRequiredBySignature(
			bootSignature,
			currentSignature,
			"running",
		) {
		t.Fatal("post-snapshot capability change was not conservatively detected")
	}
}

func TestAttachToGatewayUsesUnknownRuntimeSignatureBaseline(t *testing.T) {
	resetGatewayTestState(t)
	cfg := config.DefaultConfig()

	gateway.mu.Lock()
	err := attachToGatewayProcessLocked(os.Getpid(), cfg)
	bootSignature := gateway.bootConfigSignature
	gateway.mu.Unlock()
	if err != nil {
		t.Fatalf("attachToGatewayProcessLocked() error = %v", err)
	}
	if bootSignature != gatewayUnknownBootConfigSignature {
		t.Fatalf("bootConfigSignature = %q, want unknown", bootSignature)
	}
	if !gatewayRestartRequiredBySignature(
		bootSignature,
		computeGatewayRuntimeSignature(cfg),
		"running",
	) {
		t.Fatal("unknown attached-process baseline did not require restart")
	}
	if !gatewayRestartRequiredBySignature(
		gatewayUnknownBootConfigSignature,
		gatewayUnknownBootConfigSignature,
		"running",
	) {
		t.Fatal("unknown current and boot signatures must remain conservative")
	}
}

func TestGatewayStartReady_NoDefaultModel(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)

	ready, reason, err := h.gatewayStartReady()
	if err != nil {
		t.Fatalf("gatewayStartReady() error = %v", err)
	}
	if ready {
		t.Fatalf("gatewayStartReady() ready = true, want false")
	}
	if reason != "no default model configured" {
		t.Fatalf("gatewayStartReady() reason = %q, want %q", reason, "no default model configured")
	}
}

func TestGatewayStartReady_RejectsASROnlyDefaultModel(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "elevenlabs-asr",
		Provider:  "elevenlabs",
		Model:     "scribe_v1",
		APIKeys:   config.SimpleSecureStrings("sk_elevenlabs_test"),
	}}
	cfg.Agents.Defaults.ModelName = "elevenlabs-asr"

	err = config.SaveConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	ready, reason, err := h.gatewayStartReady()
	if err != nil {
		t.Fatalf("gatewayStartReady() error = %v", err)
	}
	if ready {
		t.Fatal("gatewayStartReady() ready = true, want false")
	}
	if reason != `default model "elevenlabs-asr" is not usable for chat` {
		t.Fatalf(
			"gatewayStartReady() reason = %q, want %q",
			reason,
			`default model "elevenlabs-asr" is not usable for chat`,
		)
	}
}

func TestLooksLikeGatewayCommandLine(t *testing.T) {
	cases := []struct {
		name    string
		cmdline string
		want    bool
	}{
		{
			name:    "default picoclaw gateway",
			cmdline: "/usr/local/bin/picoclaw gateway -E",
			want:    true,
		},
		{
			name:    "renamed binary with gateway subcommand",
			cmdline: "/opt/bin/custom-claw gateway -E -d",
			want:    true,
		},
		{
			name:    "standalone gateway binary path",
			cmdline: "/opt/bin/gateway -E",
			want:    true,
		},
		{
			name:    "non gateway process",
			cmdline: "/bin/sleep 30",
			want:    false,
		},
		{
			name:    "gateway substring only",
			cmdline: "/opt/bin/gatewayd --serve",
			want:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := looksLikeGatewayCommandLine(tc.cmdline)
			if got != tc.want {
				t.Fatalf("looksLikeGatewayCommandLine(%q) = %v, want %v", tc.cmdline, got, tc.want)
			}
		})
	}
}

func TestValidateGatewayPidDataAcceptsHealthWhenMatcherInconclusive(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)

	const testPID = 34567
	pidData := &ppid.PidFileData{
		PID:  testPID,
		Host: "127.0.0.1",
		Port: 18790,
	}

	gatewayProcessMatcher = func(int) (bool, bool) { return false, false }
	gatewayHealthGet = func(string, time.Duration) (*http.Response, error) {
		return mockGatewayHealthResponse(http.StatusOK, testPID), nil
	}

	ok, decisive, reason := h.validateGatewayPidData(pidData, nil)
	if !ok {
		t.Fatalf("validateGatewayPidData() ok = false, want true (reason=%q)", reason)
	}
	if !decisive {
		t.Fatalf("validateGatewayPidData() decisive = false, want true")
	}
}

func TestValidateGatewayPidDataRejectsHealthPidMismatchWhenMatcherInconclusive(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)

	pidData := &ppid.PidFileData{
		PID:  34567,
		Host: "127.0.0.1",
		Port: 18790,
	}

	gatewayProcessMatcher = func(int) (bool, bool) { return false, false }
	gatewayHealthGet = func(string, time.Duration) (*http.Response, error) {
		return mockGatewayHealthResponse(http.StatusOK, 99999), nil
	}

	ok, decisive, reason := h.validateGatewayPidData(pidData, nil)
	if ok {
		t.Fatalf("validateGatewayPidData() ok = true, want false")
	}
	if !decisive {
		t.Fatalf("validateGatewayPidData() decisive = false, want true")
	}
	if !strings.Contains(reason, "health pid mismatch") {
		t.Fatalf("validateGatewayPidData() reason = %q, want contains %q", reason, "health pid mismatch")
	}
}

func TestGatewayStartReady_InvalidDefaultModel(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = "missing-model"
	err := config.SaveConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	ready, reason, err := h.gatewayStartReady()
	if err != nil {
		t.Fatalf("gatewayStartReady() error = %v", err)
	}
	if ready {
		t.Fatalf("gatewayStartReady() ready = true, want false")
	}
	if reason == "" {
		t.Fatalf("gatewayStartReady() reason is empty")
	}
}

func TestGatewayStartReady_ValidDefaultModel(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	model := addGatewayTestModel(cfg)
	cfg.Agents.Defaults.ModelName = model.ModelName
	model.SetAPIKey("test-key")
	err := config.SaveConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	ready, reason, err := h.gatewayStartReady()
	if err != nil {
		t.Fatalf("gatewayStartReady() error = %v", err)
	}
	if !ready {
		t.Fatalf("gatewayStartReady() ready = false, want true (reason=%q)", reason)
	}
}

func TestGatewayStartReady_DefaultModelWithoutCredential(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	model := addGatewayTestModel(cfg)
	cfg.Agents.Defaults.ModelName = model.ModelName
	model.SetAPIKey("")
	model.AuthMethod = ""
	err := config.SaveConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	ready, reason, err := h.gatewayStartReady()
	if err != nil {
		t.Fatalf("gatewayStartReady() error = %v", err)
	}
	if ready {
		t.Fatalf("gatewayStartReady() ready = true, want false")
	}
	if !strings.Contains(reason, "no credentials configured") {
		t.Fatalf("gatewayStartReady() reason = %q, want contains %q", reason, "no credentials configured")
	}
}

func TestGatewayCommandArgsIncludesDebugFlagWhenEnabled(t *testing.T) {
	h := NewHandler(filepath.Join(t.TempDir(), "config.json"))
	h.SetDebug(true)

	args := h.gatewayCommandArgs()
	want := []string{"gateway", "-E", "-d"}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Fatalf("gatewayCommandArgs() = %v, want %v", args, want)
	}
}

func TestGatewayStartReady_LocalModelWithoutAPIKey(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetModelProbeHooks(t)

	probeOpenAICompatibleModelFunc = func(apiBase, modelID, apiKey string) bool {
		return false
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "local-vllm",
		Model:     "vllm/custom-model",
		APIBase:   "http://localhost:8000/v1",
	}}
	cfg.Agents.Defaults.ModelName = "local-vllm"
	err = config.SaveConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	ready, reason, err := h.gatewayStartReady()
	if err != nil {
		t.Fatalf("gatewayStartReady() error = %v", err)
	}
	if ready {
		t.Fatalf("gatewayStartReady() ready = true, want false without a running local service")
	}
	if !strings.Contains(reason, "not reachable") {
		t.Fatalf("gatewayStartReady() reason = %q, want contains %q", reason, "not reachable")
	}
}

func TestGatewayStartReady_LocalModelWithRunningService(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetModelProbeHooks(t)

	probeOpenAICompatibleModelFunc = func(apiBase, modelID, apiKey string) bool {
		return apiBase == "http://127.0.0.1:8000/v1" && modelID == "custom-model" && apiKey == ""
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "local-vllm",
		Model:     "vllm/custom-model",
		APIBase:   "http://127.0.0.1:8000/v1",
	}}
	cfg.Agents.Defaults.ModelName = "local-vllm"
	err = config.SaveConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	ready, reason, err := h.gatewayStartReady()
	if err != nil {
		t.Fatalf("gatewayStartReady() error = %v", err)
	}
	if !ready {
		t.Fatalf("gatewayStartReady() ready = false, want true with a running local service (reason=%q)", reason)
	}
}

func TestGatewayStartReady_RemoteVLLMWithAPIKeyDoesNotProbe(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetModelProbeHooks(t)

	probeOpenAICompatibleModelFunc = func(apiBase, modelID, apiKey string) bool {
		t.Fatalf("unexpected OpenAI-compatible probe for %q (%q)", apiBase, modelID)
		return false
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "remote-vllm",
		Model:     "vllm/custom-model",
		APIBase:   "https://models.example.com/v1",
	}}
	cfg.ModelList[0o0].SetAPIKey("remote-key")
	cfg.Agents.Defaults.ModelName = "remote-vllm"
	err = config.SaveConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	ready, reason, err := h.gatewayStartReady()
	if err != nil {
		t.Fatalf("gatewayStartReady() error = %v", err)
	}
	if !ready {
		t.Fatalf("gatewayStartReady() ready = false, want true for remote vllm with api key (reason=%q)", reason)
	}
}

func TestGatewayStartReady_LocalOllamaUsesDefaultProbeBase(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetModelProbeHooks(t)

	probeOllamaModelFunc = func(apiBase, modelID string) bool {
		return apiBase == "http://localhost:11434/v1" && modelID == "llama3"
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "local-ollama",
		Model:     "ollama/llama3",
	}}
	cfg.Agents.Defaults.ModelName = "local-ollama"
	err = config.SaveConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	ready, reason, err := h.gatewayStartReady()
	if err != nil {
		t.Fatalf("gatewayStartReady() error = %v", err)
	}
	if !ready {
		t.Fatalf("gatewayStartReady() ready = false, want true with default Ollama probe base (reason=%q)", reason)
	}
}

func TestGatewayStartReady_OAuthModelRequiresStoredCredential(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName:  "openai-oauth",
		Model:      "openai/gpt-5.4",
		AuthMethod: "oauth",
	}}
	cfg.Agents.Defaults.ModelName = "openai-oauth"
	err = config.SaveConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	ready, reason, err := h.gatewayStartReady()
	if err != nil {
		t.Fatalf("gatewayStartReady() error = %v", err)
	}
	if ready {
		t.Fatalf("gatewayStartReady() ready = true, want false without stored credential")
	}
	if !strings.Contains(reason, "no credentials configured") {
		t.Fatalf("gatewayStartReady() reason = %q, want contains %q", reason, "no credentials configured")
	}

	err = auth.SetCredential(oauthProviderOpenAI, &auth.AuthCredential{
		AccessToken: "openai-token",
		Provider:    oauthProviderOpenAI,
		AuthMethod:  "oauth",
	})
	if err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}

	ready, reason, err = h.gatewayStartReady()
	if err != nil {
		t.Fatalf("gatewayStartReady() error = %v", err)
	}
	if !ready {
		t.Fatalf("gatewayStartReady() ready = false, want true with stored credential (reason=%q)", reason)
	}
}

func TestGatewayStatusIncludesStartConditionWhenNotReady(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	allowed, ok := body["gateway_start_allowed"].(bool)
	if !ok {
		t.Fatalf("gateway_start_allowed missing or not bool: %#v", body["gateway_start_allowed"])
	}
	if allowed {
		t.Fatalf("gateway_start_allowed = true, want false")
	}
	if _, ok := body["gateway_start_reason"].(string); !ok {
		t.Fatalf("gateway_start_reason missing or not string: %#v", body["gateway_start_reason"])
	}
}

func TestGatewayStatusKeepsRunningWhenHealthProbeFailsAfterRunning(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	cmd := startLongRunningProcess(t)
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	gateway.mu.Lock()
	gateway.cmd = cmd
	gateway.bootDefaultModel = "existing-model"
	// Simulate a process that has already reached the running state.
	setGatewayRuntimeStatusLocked("running")
	gateway.mu.Unlock()

	gatewayHealthGet = func(string, time.Duration) (*http.Response, error) {
		return nil, errors.New("probe failed")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := body["gateway_status"]; got != "running" {
		t.Fatalf("gateway_status = %#v, want %q", got, "running")
	}
}

func TestGatewayStatusKeepsPidDataWhileTrackedProcessAliveWhenPidFileUnavailable(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	cmd := startLongRunningProcess(t)
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	gateway.mu.Lock()
	gateway.cmd = cmd
	gateway.pidData = &ppid.PidFileData{
		PID:   cmd.Process.Pid,
		Token: "existing-token",
	}
	setGatewayRuntimeStatusLocked("running")
	gateway.mu.Unlock()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if gateway.pidData == nil {
		t.Fatal("gateway.pidData was cleared while runtime status remained running")
	}
}

func TestGatewayStatusDowngradesRunningWhenTrackedProcessExitedAndPidFileMissing(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	cmd := startLongRunningProcess(t)
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()

	gateway.mu.Lock()
	gateway.cmd = cmd
	gateway.pidData = &ppid.PidFileData{
		PID:   cmd.Process.Pid,
		Token: "stale-token",
	}
	setGatewayRuntimeStatusLocked("running")
	gateway.mu.Unlock()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got := body["gateway_status"]; got != "stopped" {
		t.Fatalf("gateway_status = %#v, want %q", got, "stopped")
	}

	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if gateway.pidData != nil {
		t.Fatal("gateway.pidData should be cleared when tracked process has exited")
	}
}

func TestGatewayStatusIgnoresAndRemovesPidFileForNonGatewayProcess(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	cmd := startLongRunningProcess(t)
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	pidPath := writeTestPidFile(t, ppid.PidFileData{
		PID:   cmd.Process.Pid,
		Token: "stale-token",
		Host:  "127.0.0.1",
		Port:  18790,
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got := body["gateway_status"]; got != "stopped" {
		t.Fatalf("gateway_status = %#v, want %q", got, "stopped")
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatal("stale pid file should be removed for non-gateway process")
	}
}

func TestGatewayStopRefusesNonGatewayAttachedProcess(t *testing.T) {
	resetGatewayTestState(t)
	if runtime.GOOS == "windows" {
		t.Skip("commandline-based process type check is best-effort on Windows")
	}

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	cmd := startLongRunningProcess(t)
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	gateway.mu.Lock()
	gateway.cmd = cmd
	gateway.owned = false
	setGatewayRuntimeStatusLocked("running")
	gateway.mu.Unlock()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/gateway/stop", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if !isCmdProcessAliveLocked(cmd) {
		t.Fatal("non-gateway process should not be terminated by /api/gateway/stop")
	}
}

func TestGatewayStatusReportsRunningFromPidProbe(t *testing.T) {
	resetGatewayTestState(t)
	gatewayProcessMatcher = func(int) (bool, bool) { return true, true }

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	cmd := startGatewayLikeProcess(t)
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	gateway.mu.Lock()
	setGatewayRuntimeStatusLocked("stopped")
	gateway.mu.Unlock()

	gatewayHealthGet = func(string, time.Duration) (*http.Response, error) {
		return mockGatewayHealthResponse(http.StatusOK, cmd.Process.Pid), nil
	}

	writeTestPidFile(t, ppid.PidFileData{
		PID:   cmd.Process.Pid,
		Token: "test-token",
		Host:  "127.0.0.1",
		Port:  18790,
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := body["gateway_status"]; got != "running" {
		t.Fatalf("gateway_status = %#v, want %q", got, "running")
	}
	if got := body["gateway_restart_required"]; got != true {
		t.Fatalf(
			"gateway_restart_required = %#v, want conservative true for attached process",
			got,
		)
	}
}

func TestGatewayStatusRequiresRestartAfterDefaultModelChange(t *testing.T) {
	resetGatewayTestState(t)
	gatewayProcessMatcher = func(int) (bool, bool) { return true, true }

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	model := addGatewayTestModel(cfg)
	cfg.Agents.Defaults.ModelName = model.ModelName
	model.SetAPIKey("test-key")
	cfg.ModelList = append(cfg.ModelList, &config.ModelConfig{
		ModelName: "second-model",
		Model:     "openai/gpt-4.1",
	})
	cfg.ModelList[len(cfg.ModelList)-1].SetAPIKey("second-key")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	cmd := startGatewayLikeProcess(t)
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})
	writeTestPidFile(t, ppid.PidFileData{
		PID:   cmd.Process.Pid,
		Token: "test-token",
		Host:  "127.0.0.1",
		Port:  18790,
	})

	bootSignature := computeConfigSignature(cfg)
	gateway.mu.Lock()
	gateway.cmd = cmd
	gateway.bootDefaultModel = model.ModelName
	gateway.bootConfigSignature = bootSignature
	setGatewayRuntimeStatusLocked("running")
	gateway.mu.Unlock()

	updatedCfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	updatedCfg.Agents.Defaults.ModelName = "second-model"
	if err := config.SaveConfig(configPath, updatedCfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	gatewayHealthGet = func(string, time.Duration) (*http.Response, error) {
		return mockGatewayHealthResponse(http.StatusOK, os.Getpid()), nil
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := body["gateway_status"]; got != "running" {
		t.Fatalf("gateway_status = %#v, want %q", got, "running")
	}
	if got := body["boot_default_model"]; got != model.ModelName {
		t.Fatalf("boot_default_model = %#v, want %q", got, model.ModelName)
	}
	if got := body["config_default_model"]; got != "second-model" {
		t.Fatalf("config_default_model = %#v, want %q", got, "second-model")
	}
	if got := body["gateway_restart_required"]; got != true {
		t.Fatalf("gateway_restart_required = %#v, want true", got)
	}
}

func TestGatewayStatusRequiresRestartAfterToolChange(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	model := addGatewayTestModel(cfg)
	cfg.Agents.Defaults.ModelName = model.ModelName
	model.SetAPIKey("test-key")
	cfg.Tools.WriteFile.Enabled = true
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess() error = %v", err)
	}

	bootSignature := computeConfigSignature(cfg)
	gateway.mu.Lock()
	gateway.cmd = &exec.Cmd{Process: process}
	gateway.bootDefaultModel = model.ModelName
	gateway.bootConfigSignature = bootSignature
	setGatewayRuntimeStatusLocked("running")
	gateway.mu.Unlock()

	updatedCfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	updatedCfg.Tools.WriteFile.Enabled = false
	if err := config.SaveConfig(configPath, updatedCfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	gatewayHealthGet = func(string, time.Duration) (*http.Response, error) {
		return mockGatewayHealthResponse(http.StatusOK, os.Getpid()), nil
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := body["gateway_status"]; got != "running" {
		t.Fatalf("gateway_status = %#v, want %q", got, "running")
	}
	if got := body["gateway_restart_required"]; got != true {
		t.Fatalf("gateway_restart_required = %#v, want true", got)
	}
}

func TestGatewayStatusRequiresRestartAfterChannelChange(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	model := addGatewayTestModel(cfg)
	cfg.Agents.Defaults.ModelName = model.ModelName
	model.SetAPIKey("test-key")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess() error = %v", err)
	}

	bootSignature := computeConfigSignature(cfg)
	gateway.mu.Lock()
	gateway.cmd = &exec.Cmd{Process: process}
	gateway.bootDefaultModel = model.ModelName
	gateway.bootConfigSignature = bootSignature
	setGatewayRuntimeStatusLocked("running")
	gateway.mu.Unlock()

	updatedCfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	telegram := updatedCfg.Channels.Get("telegram")
	if telegram == nil {
		t.Fatalf("expected default telegram channel config")
	}
	telegram.Enabled = !telegram.Enabled
	if err := config.SaveConfig(configPath, updatedCfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	gatewayHealthGet = func(string, time.Duration) (*http.Response, error) {
		return mockGatewayHealthResponse(http.StatusOK, os.Getpid()), nil
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := body["gateway_status"]; got != "running" {
		t.Fatalf("gateway_status = %#v, want %q", got, "running")
	}
	if got := body["gateway_restart_required"]; got != true {
		t.Fatalf("gateway_restart_required = %#v, want true", got)
	}
}

func TestConfigSignatureHashesDeltaChatPassword(t *testing.T) {
	cfg := config.DefaultConfig()
	delta := cfg.Channels.Get(config.ChannelDeltaChat)
	if delta == nil {
		t.Fatal("expected default Delta Chat channel config")
	}
	decoded, err := delta.GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded() error = %v", err)
	}
	settings, ok := decoded.(*config.DeltaChatSettings)
	if !ok {
		t.Fatalf(
			"Delta Chat settings type = %T, want *config.DeltaChatSettings",
			decoded,
		)
	}

	const oldPassword = "delta-password-before"
	const newPassword = "delta-password-after"
	settings.Password.Set(oldPassword)
	before := computeConfigSignature(cfg)
	settings.Password.Set(newPassword)
	after := computeConfigSignature(cfg)

	if after == before {
		t.Fatal("rotating the Delta Chat password should change the config signature")
	}
	for _, signature := range []string{before, after} {
		if strings.Contains(signature, oldPassword) ||
			strings.Contains(signature, newPassword) {
			t.Fatal("config signature must not contain a plaintext Delta Chat password")
		}
	}
}

func TestConfigSignatureTracksFullOrderedAgentConfiguration(t *testing.T) {
	newConfig := func() *config.Config {
		cfg := config.DefaultConfig()
		cfg.Agents.List = []config.AgentConfig{
			{
				ID:      "main",
				Default: true,
				Model: &config.AgentModelConfig{
					Primary:   "primary-model",
					Fallbacks: nil,
				},
				Skills: nil,
				Subagents: &config.SubagentsConfig{
					AllowAgents: []string{"worker"},
				},
			},
			{ID: "worker", Name: "Worker"},
		}
		cfg.Agents.Dispatch = &config.DispatchConfig{
			Rules: []config.DispatchRule{
				{
					Name:  "github",
					Agent: "worker",
					When:  config.DispatchSelector{Channel: "github"},
				},
			},
		}
		return cfg
	}

	baseline := computeConfigSignature(newConfig())
	tests := []struct {
		name   string
		mutate func(*config.Config)
	}{
		{
			name: "configured order",
			mutate: func(cfg *config.Config) {
				cfg.Agents.List[0], cfg.Agents.List[1] = cfg.Agents.List[1], cfg.Agents.List[0]
			},
		},
		{
			name: "agent fields",
			mutate: func(cfg *config.Config) {
				cfg.Agents.List[1].Name = "Renamed worker"
			},
		},
		{
			name: "agent defaults",
			mutate: func(cfg *config.Config) {
				cfg.Agents.Defaults.MaxTokens++
			},
		},
		{
			name: "dispatch",
			mutate: func(cfg *config.Config) {
				cfg.Agents.Dispatch.Rules[0].Agent = "main"
			},
		},
		{
			name: "nil versus empty model fallbacks",
			mutate: func(cfg *config.Config) {
				cfg.Agents.List[0].Model.Fallbacks = []string{}
			},
		},
		{
			name: "nil versus empty skills",
			mutate: func(cfg *config.Config) {
				cfg.Agents.List[0].Skills = []string{}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := newConfig()
			tt.mutate(cfg)
			if got := computeConfigSignature(cfg); got == baseline {
				t.Fatalf("%s must change the config signature", tt.name)
			}
		})
	}
}

func TestSignatureCanonicalizerKeepsLegacyNilCollectionSemanticsOutsideAgents(
	t *testing.T,
) {
	type payload struct {
		Items  []string          `json:"items"`
		Labels map[string]string `json:"labels"`
	}

	nilCollections, err := json.Marshal(canonicalizeSignatureValue(
		reflect.ValueOf(payload{}),
	))
	if err != nil {
		t.Fatalf("marshal nil collections: %v", err)
	}
	emptyCollections, err := json.Marshal(canonicalizeSignatureValue(
		reflect.ValueOf(payload{
			Items:  []string{},
			Labels: map[string]string{},
		}),
	))
	if err != nil {
		t.Fatalf("marshal empty collections: %v", err)
	}
	if string(nilCollections) != string(emptyCollections) {
		t.Fatalf(
			"non-agent nil collections = %s, empty collections = %s; want legacy-equivalent signatures",
			nilCollections,
			emptyCollections,
		)
	}

	nilAgentCollections, err := json.Marshal(canonicalizeAgentSignatureValue(
		reflect.ValueOf(payload{}),
	))
	if err != nil {
		t.Fatalf("marshal nil agent collections: %v", err)
	}
	emptyAgentCollections, err := json.Marshal(canonicalizeAgentSignatureValue(
		reflect.ValueOf(payload{
			Items:  []string{},
			Labels: map[string]string{},
		}),
	))
	if err != nil {
		t.Fatalf("marshal empty agent collections: %v", err)
	}
	if string(nilAgentCollections) == string(emptyAgentCollections) {
		t.Fatalf(
			"agent nil and empty collections must remain distinct: %s",
			nilAgentCollections,
		)
	}
}

func TestConfigSignatureKeepsNilAndEmptyEquivalentOutsideAgents(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Web.Enabled = true
	cfg.Tools.Web.PrivateHostWhitelist = nil
	cfg.Tools.MCP.Enabled = true
	cfg.Tools.MCP.Servers = nil
	nilCollections := computeConfigSignature(cfg)

	cfg.Tools.Web.PrivateHostWhitelist = config.FlexibleStringSlice{}
	cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{}
	emptyCollections := computeConfigSignature(cfg)

	if nilCollections != emptyCollections {
		t.Fatal("non-agent nil and empty collections must retain legacy-equivalent signatures")
	}
}

func TestConfigSignatureTracksOnlyActiveEventIngressRuntime(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()

	disabledSignature := computeConfigSignature(cfg)
	cfg.Events.Ingress.DatabasePath = "eventing/alternate.db"
	cfg.Events.Ingress.RetentionDays = 14
	cfg.Events.Ingress.Webhooks = map[string]config.GenericWebhookConfig{
		"inactive": {
			Enabled: false,
			Format:  config.EventWebhookFormatGitHub,
			Secret:  *config.NewSecureString("01234567890123456789012345678901"),
		},
	}
	if got := computeConfigSignature(cfg); got != disabledSignature {
		t.Fatal("disabled event ingress configuration should not require a restart")
	}

	cfg.Events.Ingress.Enabled = true
	enabledSignature := computeConfigSignature(cfg)
	if enabledSignature == disabledSignature {
		t.Fatal("enabling event ingress should change the config signature")
	}

	cfg.Events.Ingress.RetentionDays = 21
	retentionSignature := computeConfigSignature(cfg)
	if retentionSignature == enabledSignature {
		t.Fatal("active event retention policy should change the config signature")
	}

	inactive := cfg.Events.Ingress.Webhooks["inactive"]
	inactive.Format = config.EventWebhookFormatStandard
	cfg.Events.Ingress.Webhooks["inactive"] = inactive
	if got := computeConfigSignature(cfg); got != retentionSignature {
		t.Fatal("inactive webhook routing metadata should not change the active runtime signature")
	}

	inactive.Secret = *config.NewSecureString("inactive-secret-change")
	cfg.Events.Ingress.Webhooks["inactive"] = inactive
	inactiveRedactionSignature := computeConfigSignature(cfg)
	if inactiveRedactionSignature == retentionSignature {
		t.Fatal("rotating an inactive webhook secret should change the active store redactor signature")
	}

	active := inactive
	active.Enabled = true
	active.Format = config.EventWebhookFormatGitHub
	active.Secret = *config.NewSecureString("01234567890123456789012345678901")
	cfg.Events.Ingress.Webhooks["primary"] = active
	webhookSignature := computeConfigSignature(cfg)
	if webhookSignature == inactiveRedactionSignature {
		t.Fatal("an active webhook should change the config signature")
	}

	active.Secret = *config.NewSecureString("abcdefghijklmnopqrstuvwxyzABCDEF")
	cfg.Events.Ingress.Webhooks["primary"] = active
	if got := computeConfigSignature(cfg); got == webhookSignature {
		t.Fatal("rotating an active webhook secret should change the config signature")
	}
}

func TestConfigSignatureCanonicalizesEventRedactFieldsLikeRedactor(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Events.Ingress.Enabled = true
	cfg.Events.Ingress.RedactFields = []string{
		"Tenant_Secret",
		"Custom-Field",
		"tenant_secret",
		" *** ",
	}

	before := computeConfigSignature(cfg)
	cfg.Events.Ingress.RedactFields = []string{
		"custom field",
		"TENANT-SECRET",
		"CustomField",
		"***",
	}
	after := computeConfigSignature(cfg)

	if after != before {
		t.Fatal("semantically equivalent redact fields should not change the runtime signature")
	}

	cfg.Events.Ingress.RedactFields = []string{
		"custom field",
		"TENANT-SECRET",
		"CustomField",
		"!!!",
	}
	if got := computeConfigSignature(cfg); got == after {
		t.Fatal("changing an exact punctuation-only redact field should change the runtime signature")
	}
}

func TestConfigSignatureTracksWorkflowRuntimeWithoutEventIngress(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Events.Ingress.Enabled = false
	cfg.Workflows.Enabled = true

	before := computeConfigSignature(cfg)
	cfg.Workflows.DefinitionsDir = "automation/workflows"
	afterDefinitions := computeConfigSignature(cfg)
	if afterDefinitions == before {
		t.Fatal("workflow definitions change should require restart without event ingress")
	}

	cfg.Workflows.MaxConcurrentRuns = cfg.Workflows.EffectiveMaxConcurrentRuns() + 1
	afterLimit := computeConfigSignature(cfg)
	if afterLimit == afterDefinitions {
		t.Fatal("workflow executor limit should require restart without event ingress")
	}

	cfg.Tools.Workflow.Enabled = !cfg.Tools.Workflow.Enabled
	afterTool := computeConfigSignature(cfg)
	if afterTool == afterLimit {
		t.Fatal("workflow tool toggle should require restart without event ingress")
	}

	cfg.Workflows.Enabled = false
	disabled := computeConfigSignature(cfg)
	if disabled == afterTool {
		t.Fatal("disabling workflows should require restart without event ingress")
	}
	cfg.Workflows.MaxConcurrentRuns++
	if got := computeConfigSignature(cfg); got != disabled {
		t.Fatal("executor limit should be inert while workflows are disabled")
	}
}

func TestConfigSignatureTracksEventWorkflowDispatchToggleBothDirections(t *testing.T) {
	tests := []struct {
		name   string
		before bool
		after  bool
	}{
		{name: "enable workers", before: false, after: true},
		{name: "disable workers", before: true, after: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Agents.Defaults.Workspace = t.TempDir()
			cfg.Events.Ingress.Enabled = true
			cfg.Workflows.Enabled = tt.before

			before := computeConfigSignature(cfg)
			cfg.Workflows.Enabled = tt.after
			after := computeConfigSignature(cfg)
			if after == before {
				t.Fatalf(
					"changing event workflow dispatch from %t to %t should change the runtime signature",
					tt.before,
					tt.after,
				)
			}
		})
	}
}

func TestConfigSignatureTracksActiveEventWorkflowExecutorSettings(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Events.Ingress.Enabled = true
	cfg.Workflows.Enabled = true

	before := computeConfigSignature(cfg)
	cfg.Workflows.MaxConcurrentRuns = cfg.Workflows.EffectiveMaxConcurrentRuns() + 1
	after := computeConfigSignature(cfg)
	if after == before {
		t.Fatal("changing an active event workflow executor setting should change the runtime signature")
	}

	cfg.Workflows.Enabled = false
	disabledBefore := computeConfigSignature(cfg)
	cfg.Workflows.MaxConcurrentRuns++
	disabledAfter := computeConfigSignature(cfg)
	if disabledAfter != disabledBefore {
		t.Fatal("inactive event workflow executor settings should not change the runtime signature")
	}
}

func TestConfigSignatureTracksActiveEventWorkflowWorkspace(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Events.Ingress.Enabled = true
	cfg.Events.Ingress.DatabasePath = filepath.Join(
		t.TempDir(),
		"absolute-events.db",
	)
	cfg.Workflows.Enabled = true
	cfg.Agents.Defaults.Workspace = filepath.Join(t.TempDir(), "workspace-one")

	before := computeConfigSignature(cfg)
	cfg.Agents.Defaults.Workspace = filepath.Join(t.TempDir(), "workspace-two")
	after := computeConfigSignature(cfg)
	if after == before {
		t.Fatal("changing the active event workflow workspace should change the config signature")
	}

	cfg.Workflows.Enabled = false
	disabledBefore := computeConfigSignature(cfg)
	cfg.Agents.Defaults.Workspace = filepath.Join(t.TempDir(), "workspace-three")
	if got := computeConfigSignature(cfg); got == disabledBefore {
		t.Fatal("agent workspace changes should require restart when event dispatch is disabled")
	}
}

func TestConfigSignatureTracksEventRedactionCredentialDigests(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Events.Ingress.Enabled = true
	cfg.Tools.Web.Enabled = false

	model := addGatewayTestModel(cfg)
	cfg.Agents.Defaults.ModelName = model.ModelName
	const oldModelSecret = "model-redaction-secret-old"
	const newModelSecret = "model-redaction-secret-new"
	model.SetAPIKey(oldModelSecret)

	beforeModelRotation := computeConfigSignature(cfg)
	model.SetAPIKey(newModelSecret)
	afterModelRotation := computeConfigSignature(cfg)
	if afterModelRotation == beforeModelRotation {
		t.Fatal("rotating a model credential should change the event redaction runtime signature")
	}
	for _, signature := range []string{beforeModelRotation, afterModelRotation} {
		if strings.Contains(signature, oldModelSecret) ||
			strings.Contains(signature, newModelSecret) {
			t.Fatal("event redaction runtime signature must not contain plaintext model credentials")
		}
	}

	const oldToolSecret = "tool-redaction-secret-old"
	const newToolSecret = "tool-redaction-secret-new"
	cfg.Tools.Web.Brave.SetAPIKey(oldToolSecret)
	beforeToolRotation := computeConfigSignature(cfg)
	cfg.Tools.Web.Brave.SetAPIKey(newToolSecret)
	afterToolRotation := computeConfigSignature(cfg)
	if afterToolRotation == beforeToolRotation {
		t.Fatal("rotating a tool credential should change the event redaction runtime signature")
	}
	for _, signature := range []string{beforeToolRotation, afterToolRotation} {
		if strings.Contains(signature, oldToolSecret) ||
			strings.Contains(signature, newToolSecret) {
			t.Fatal("event redaction runtime signature must not contain plaintext tool credentials")
		}
	}
}

func TestConfigSignatureTracksActiveEventChannelAdapter(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Events.Ingress.Enabled = true

	delta := cfg.Channels.Get("deltachat")
	if delta == nil {
		t.Fatal("expected default Delta Chat channel config")
	}
	delta.Enabled = true
	cfg.Events.Ingress.Channels = map[string]config.ChannelEventIngressConfig{
		"deltachat": {
			Enabled: true,
			Source:  config.EventChannelSourceEmail,
			Mode:    config.EventChannelModeMirror,
		},
	}

	before := computeConfigSignature(cfg)
	adapter := cfg.Events.Ingress.Channels["deltachat"]
	adapter.Mode = config.EventChannelModeEventOnly
	cfg.Events.Ingress.Channels["deltachat"] = adapter
	after := computeConfigSignature(cfg)
	if after == before {
		t.Fatal("changing an active event channel adapter should change the config signature")
	}
}

func TestGatewayStatusRequiresRestartAfterDefaultModelStreamingChange(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	model := addGatewayTestModel(cfg)
	cfg.Agents.Defaults.ModelName = model.ModelName
	model.SetAPIKey("test-key")
	model.Streaming = config.ModelStreamingConfig{Enabled: false}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess() error = %v", err)
	}

	bootSignature := computeConfigSignature(cfg)
	gateway.mu.Lock()
	gateway.cmd = &exec.Cmd{Process: process}
	gateway.bootDefaultModel = model.ModelName
	gateway.bootConfigSignature = bootSignature
	setGatewayRuntimeStatusLocked("running")
	gateway.mu.Unlock()

	updatedCfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	updatedCfg.ModelList[0].Streaming = config.ModelStreamingConfig{Enabled: true}
	if err := config.SaveConfig(configPath, updatedCfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	gatewayHealthGet = func(string, time.Duration) (*http.Response, error) {
		return mockGatewayHealthResponse(http.StatusOK, os.Getpid()), nil
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := body["gateway_status"]; got != "running" {
		t.Fatalf("gateway_status = %#v, want %q", got, "running")
	}
	if got := body["gateway_restart_required"]; got != true {
		t.Fatalf("gateway_restart_required = %#v, want true", got)
	}
}

func TestConfigSignatureIncludesModelStreamingForDefaultModelRef(t *testing.T) {
	cfg := config.DefaultConfig()
	model := addGatewayTestModel(cfg)
	model.ModelName = "friendly-alias"
	model.Provider = ""
	model.Model = "openai/gpt-4o-ref"
	cfg.Agents.Defaults.ModelName = "openai/gpt-4o-ref"
	model.Streaming = config.ModelStreamingConfig{Enabled: false}

	before := computeConfigSignature(cfg)

	model.Streaming = config.ModelStreamingConfig{Enabled: true}
	after := computeConfigSignature(cfg)

	if before == after {
		t.Fatal("config signature should change when streaming changes for a default model referenced by model ref")
	}
}

func TestConfigSignatureMCPConfigIsDeterministicAcrossMapOrder(t *testing.T) {
	first := config.DefaultConfig()
	first.Tools.MCP.Servers = map[string]config.MCPServerConfig{
		"zeta": {
			Enabled: true,
			Type:    "http",
			URL:     "https://zeta.example.test/mcp",
			Headers: map[string]string{
				"X-Zeta":   "zeta",
				"X-Shared": "shared",
			},
		},
		"alpha": {
			Enabled: true,
			Type:    "stdio",
			Command: "npx",
			Args:    []string{"-y", "alpha-server"},
			Env: map[string]string{
				"ZETA":  "zeta",
				"ALPHA": "alpha",
			},
		},
	}

	second := config.DefaultConfig()
	second.Tools.MCP.Servers = make(map[string]config.MCPServerConfig)
	second.Tools.MCP.Servers["alpha"] = config.MCPServerConfig{
		Enabled: true,
		Type:    "stdio",
		Command: "npx",
		Args:    []string{"-y", "alpha-server"},
		Env: map[string]string{
			"ALPHA": "alpha",
			"ZETA":  "zeta",
		},
	}
	second.Tools.MCP.Servers["zeta"] = config.MCPServerConfig{
		Enabled: true,
		Type:    "http",
		URL:     "https://zeta.example.test/mcp",
		Headers: map[string]string{
			"X-Shared": "shared",
			"X-Zeta":   "zeta",
		},
	}

	firstSignature := computeConfigSignature(first)
	secondSignature := computeConfigSignature(second)
	if firstSignature != secondSignature {
		t.Fatalf(
			"equivalent MCP configs produced different signatures:\nfirst:  %s\nsecond: %s",
			firstSignature,
			secondSignature,
		)
	}
	if repeated := computeConfigSignature(first); repeated != firstSignature {
		t.Fatalf("repeated signature = %q, want deterministic %q", repeated, firstSignature)
	}
}

func TestConfigSignatureChangesForMCPRuntimeConfig(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*config.Config)
	}{
		{
			name: "server URL",
			mutate: func(cfg *config.Config) {
				server := cfg.Tools.MCP.Servers["remote"]
				server.URL = "https://changed.example.test/mcp"
				cfg.Tools.MCP.Servers["remote"] = server
			},
		},
		{
			name: "server header",
			mutate: func(cfg *config.Config) {
				server := cfg.Tools.MCP.Servers["remote"]
				server.Headers["X-API-Key"] = "replacement"
				cfg.Tools.MCP.Servers["remote"] = server
			},
		},
		{
			name: "server command",
			mutate: func(cfg *config.Config) {
				server := cfg.Tools.MCP.Servers["local"]
				server.Command = "bunx"
				cfg.Tools.MCP.Servers["local"] = server
			},
		},
		{
			name: "server arguments",
			mutate: func(cfg *config.Config) {
				server := cfg.Tools.MCP.Servers["local"]
				server.Args = append(server.Args, "--verbose")
				cfg.Tools.MCP.Servers["local"] = server
			},
		},
		{
			name: "server environment",
			mutate: func(cfg *config.Config) {
				server := cfg.Tools.MCP.Servers["local"]
				server.Env["TOKEN"] = "replacement"
				cfg.Tools.MCP.Servers["local"] = server
			},
		},
		{
			name: "credential revision",
			mutate: func(cfg *config.Config) {
				server := cfg.Tools.MCP.Servers["remote"]
				server.Auth.Revision++
				cfg.Tools.MCP.Servers["remote"] = server
			},
		},
		{
			name: "discovery TTL",
			mutate: func(cfg *config.Config) {
				cfg.Tools.MCP.Discovery.TTL++
			},
		},
		{
			name: "inline text limit",
			mutate: func(cfg *config.Config) {
				cfg.Tools.MCP.MaxInlineTextChars++
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Tools.MCP.Enabled = true
			cfg.Tools.MCP.Discovery = config.ToolDiscoveryConfig{
				Enabled:          true,
				TTL:              5,
				MaxSearchResults: 5,
				UseBM25:          true,
			}
			cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
				"remote": {
					Enabled: true,
					Type:    "http",
					URL:     "https://remote.example.test/mcp",
					Headers: map[string]string{"X-API-Key": "original"},
					Auth: &config.MCPServerAuthConfig{
						Type:         "bearer",
						CredentialID: "mcp:remote",
						Revision:     1,
					},
				},
				"local": {
					Enabled: true,
					Type:    "stdio",
					Command: "npx",
					Args:    []string{"-y", "local-server"},
					Env:     map[string]string{"TOKEN": "original"},
				},
			}

			before := computeConfigSignature(cfg)
			tt.mutate(cfg)
			after := computeConfigSignature(cfg)
			if before == after {
				t.Fatalf("config signature did not change after MCP %s changed", tt.name)
			}
		})
	}
}

func TestConfigSignatureIgnoresMCPDetailsWhileDisabled(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.MCP.Enabled = false
	cfg.Tools.MCP.Discovery.Enabled = true
	cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
		"remote": {
			Enabled: true,
			Type:    "http",
			URL:     "https://remote.example.test/mcp",
		},
	}
	before := computeConfigSignature(cfg)

	cfg.Tools.MCP.Discovery.TTL++
	server := cfg.Tools.MCP.Servers["remote"]
	server.URL = "https://changed.example.test/mcp"
	cfg.Tools.MCP.Servers["remote"] = server

	if after := computeConfigSignature(cfg); before != after {
		t.Fatalf("disabled MCP details changed gateway signature:\nbefore: %s\nafter:  %s", before, after)
	}
}

func TestConfigSignatureIncludesModelStreamingForLoadBalancedAliasEntries(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "lb-alias",
			Model:     "openai/gpt-4o-primary",
			Streaming: config.ModelStreamingConfig{Enabled: false},
		},
		{
			ModelName: "lb-alias",
			Model:     "openai/gpt-4o-secondary",
			Streaming: config.ModelStreamingConfig{Enabled: false},
		},
	}
	cfg.Agents.Defaults.ModelName = "lb-alias"

	before := computeConfigSignature(cfg)

	cfg.ModelList[1].Streaming = config.ModelStreamingConfig{Enabled: true}
	after := computeConfigSignature(cfg)

	if before == after {
		t.Fatal("config signature should change when streaming changes for a load-balanced alias entry")
	}
}

func TestConfigSignatureIncludesSlashModelIDForDefaultProvider(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "nvidia-model",
			Provider:  "nvidia",
			Model:     "z-ai/glm-5.1",
			Streaming: config.ModelStreamingConfig{Enabled: false},
		},
	}
	cfg.Agents.Defaults.Provider = "nvidia"
	cfg.Agents.Defaults.ModelName = "z-ai/glm-5.1"

	before := computeConfigSignature(cfg)

	cfg.ModelList[0].Streaming = config.ModelStreamingConfig{Enabled: true}
	after := computeConfigSignature(cfg)

	if before == after {
		t.Fatal(
			"config signature should change when streaming changes for a slash-containing model id on the default provider",
		)
	}
}

func TestConfigSignatureIncludesSupportedPrefixSlashModelIDForDefaultProvider(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "openrouter-openai",
			Provider:  "openrouter",
			Model:     "openai/gpt-4o",
			Streaming: config.ModelStreamingConfig{Enabled: false},
		},
	}
	cfg.Agents.Defaults.Provider = "openrouter"
	cfg.Agents.Defaults.ModelName = "openai/gpt-4o"

	before := computeConfigSignature(cfg)

	cfg.ModelList[0].Streaming = config.ModelStreamingConfig{Enabled: true}
	after := computeConfigSignature(cfg)

	if before == after {
		t.Fatal(
			"config signature should change when streaming changes for a supported-prefix slash model id on the default provider",
		)
	}
}

func TestConfigSignatureIncludesLegacyDefaultProviderPrefixedSlashModelID(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "legacy-openrouter-openai",
			Model:     "openrouter/openai/gpt-4o",
			Streaming: config.ModelStreamingConfig{Enabled: false},
		},
	}
	cfg.Agents.Defaults.Provider = "openrouter"
	cfg.Agents.Defaults.ModelName = "openai/gpt-4o"

	before := computeConfigSignature(cfg)

	cfg.ModelList[0].Streaming = config.ModelStreamingConfig{Enabled: true}
	after := computeConfigSignature(cfg)

	if before == after {
		t.Fatal(
			"config signature should change when streaming changes for a legacy default-provider prefixed slash model id",
		)
	}
}

func TestConfigSignatureIncludesSlashModelIDWithoutProviderFieldForDefaultProvider(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "nvidia-model",
			Model:     "z-ai/glm-5.1",
			Streaming: config.ModelStreamingConfig{Enabled: false},
		},
	}
	cfg.Agents.Defaults.Provider = "nvidia"
	cfg.Agents.Defaults.ModelName = "z-ai/glm-5.1"

	before := computeConfigSignature(cfg)

	cfg.ModelList[0].Streaming = config.ModelStreamingConfig{Enabled: true}
	after := computeConfigSignature(cfg)

	if before == after {
		t.Fatal(
			"config signature should change when streaming changes for a default-provider slash model id without provider field",
		)
	}
}

func TestConfigSignatureIncludesUnknownSlashPrefixModelIDWithoutProviderFieldForDefaultProvider(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "nvidia-meta",
			Model:     "meta/llama-3.1-8b",
			Streaming: config.ModelStreamingConfig{Enabled: false},
		},
	}
	cfg.Agents.Defaults.Provider = "nvidia"
	cfg.Agents.Defaults.ModelName = "meta/llama-3.1-8b"

	before := computeConfigSignature(cfg)

	cfg.ModelList[0].Streaming = config.ModelStreamingConfig{Enabled: true}
	after := computeConfigSignature(cfg)

	if before == after {
		t.Fatal(
			"config signature should change when streaming changes for unknown-prefix default-provider slash model id",
		)
	}
}

func TestConfigSignatureDashAliasSlashModelIDMatchesProviderAlias(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "zai-model",
			Provider:  "zai",
			Model:     "glm-5.1",
			Streaming: config.ModelStreamingConfig{Enabled: false},
		},
	}
	cfg.Agents.Defaults.Provider = "nvidia"
	cfg.Agents.Defaults.ModelName = "z-ai/glm-5.1"

	before := computeConfigSignature(cfg)

	cfg.ModelList[0].Streaming = config.ModelStreamingConfig{Enabled: true}
	after := computeConfigSignature(cfg)

	if before == after {
		t.Fatal("config signature should change when a dash-alias slash ref matches a provider alias")
	}
}

func TestConfigSignatureDashAliasSlashModelIDMatchesProviderAliasWithOpenAIDefault(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "zai-model",
			Provider:  "zai",
			Model:     "glm-5.1",
			Streaming: config.ModelStreamingConfig{Enabled: false},
		},
	}
	cfg.Agents.Defaults.Provider = "openai"
	cfg.Agents.Defaults.ModelName = "z-ai/glm-5.1"

	before := computeConfigSignature(cfg)

	cfg.ModelList[0].Streaming = config.ModelStreamingConfig{Enabled: true}
	after := computeConfigSignature(cfg)

	if before == after {
		t.Fatal(
			"config signature should change when a dash-alias slash ref matches a provider alias with OpenAI default",
		)
	}
}

func TestConfigSignatureProviderAliasRefIgnoresDefaultProvider(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "openai-gpt",
			Provider:  "openai",
			Model:     "gpt-4o",
			Streaming: config.ModelStreamingConfig{Enabled: false},
		},
	}
	cfg.Agents.Defaults.Provider = "nvidia"
	cfg.Agents.Defaults.ModelName = "gpt/gpt-4o"

	before := computeConfigSignature(cfg)

	cfg.ModelList[0].Streaming = config.ModelStreamingConfig{Enabled: true}
	after := computeConfigSignature(cfg)

	if before == after {
		t.Fatal("config signature should change for a provider alias ref even when default provider differs")
	}
}

func TestConfigSignatureExplicitProviderRefIgnoresDefaultProvider(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "openai-gpt",
			Provider:  "openai",
			Model:     "gpt-4o",
			Streaming: config.ModelStreamingConfig{Enabled: false},
		},
	}
	cfg.Agents.Defaults.Provider = "nvidia"
	cfg.Agents.Defaults.ModelName = "openai/gpt-4o"

	before := computeConfigSignature(cfg)

	cfg.ModelList[0].Streaming = config.ModelStreamingConfig{Enabled: true}
	after := computeConfigSignature(cfg)

	if before == after {
		t.Fatal("config signature should change for an explicit provider ref even when default provider differs")
	}
}

func TestConfigSignatureExactModelNameTakesPrecedenceOverResolvedRefs(t *testing.T) {
	tests := []struct {
		name                  string
		defaultProvider       string
		defaultModelName      string
		models                []*config.ModelConfig
		shadowedEntryIndex    int
		exactModelNameIndex   int
		shadowedChangeMessage string
		exactChangeMessage    string
	}{
		{
			name:             "slash model name shadows explicit provider ref",
			defaultProvider:  "nvidia",
			defaultModelName: "openai/gpt-4o",
			models: []*config.ModelConfig{
				{
					ModelName: "openai/gpt-4o",
					Provider:  "nvidia",
					Model:     "openai/gpt-4o",
					Streaming: config.ModelStreamingConfig{Enabled: false},
				},
				{
					ModelName: "openai-gpt",
					Provider:  "openai",
					Model:     "gpt-4o",
					Streaming: config.ModelStreamingConfig{Enabled: false},
				},
			},
			shadowedEntryIndex:    1,
			exactModelNameIndex:   0,
			shadowedChangeMessage: "config signature should not change when an exact slash model_name shadows an explicit provider ref",
			exactChangeMessage:    "config signature should change when the exact slash model_name entry changes",
		},
		{
			name:             "bare model name shadows default provider model id",
			defaultProvider:  "openai",
			defaultModelName: "gpt-4o",
			models: []*config.ModelConfig{
				{
					ModelName: "gpt-4o",
					Provider:  "anthropic",
					Model:     "claude-sonnet",
					Streaming: config.ModelStreamingConfig{Enabled: false},
				},
				{
					ModelName: "openai-gpt",
					Provider:  "openai",
					Model:     "gpt-4o",
					Streaming: config.ModelStreamingConfig{Enabled: false},
				},
			},
			shadowedEntryIndex:    1,
			exactModelNameIndex:   0,
			shadowedChangeMessage: "config signature should not change when an exact bare model_name shadows a default-provider model id",
			exactChangeMessage:    "config signature should change when the exact bare model_name entry changes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.ModelList = tt.models
			cfg.Agents.Defaults.Provider = tt.defaultProvider
			cfg.Agents.Defaults.ModelName = tt.defaultModelName

			before := computeConfigSignature(cfg)

			cfg.ModelList[tt.shadowedEntryIndex].Streaming = config.ModelStreamingConfig{Enabled: true}
			afterShadowedChange := computeConfigSignature(cfg)

			if before != afterShadowedChange {
				t.Fatal(tt.shadowedChangeMessage)
			}

			cfg.ModelList[tt.exactModelNameIndex].Streaming = config.ModelStreamingConfig{Enabled: true}
			afterExactModelNameChange := computeConfigSignature(cfg)

			if before == afterExactModelNameChange {
				t.Fatal(tt.exactChangeMessage)
			}
		})
	}
}

func TestConfigSignatureIncludesLoadBalancedDuplicateEntryIndex(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "lb-alias",
			Provider:  "openai",
			Model:     "gpt-4o",
			Streaming: config.ModelStreamingConfig{Enabled: false},
		},
		{
			ModelName: "lb-alias",
			Provider:  "openai",
			Model:     "gpt-4o",
			Streaming: config.ModelStreamingConfig{Enabled: true},
		},
	}
	cfg.Agents.Defaults.ModelName = "lb-alias"

	before := computeConfigSignature(cfg)

	cfg.ModelList[0].Streaming.Enabled = true
	cfg.ModelList[1].Streaming.Enabled = false
	after := computeConfigSignature(cfg)

	if before == after {
		t.Fatal("config signature should change when duplicate load-balanced entries swap streaming state")
	}
}

func TestConfigSignatureProviderDotAliasRefIgnoresDefaultProvider(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "zai-model",
			Provider:  "zai",
			Model:     "glm-5.1",
			Streaming: config.ModelStreamingConfig{Enabled: false},
		},
	}
	cfg.Agents.Defaults.Provider = "nvidia"
	cfg.Agents.Defaults.ModelName = "z.ai/glm-5.1"

	before := computeConfigSignature(cfg)

	cfg.ModelList[0].Streaming = config.ModelStreamingConfig{Enabled: true}
	after := computeConfigSignature(cfg)

	if before == after {
		t.Fatal(
			"config signature should change for an explicit dot-alias provider ref even when default provider differs",
		)
	}
}

func TestConfigSignatureIncludesDefaultProviderPrefixedRefWithSplitConfig(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "openai-split",
			Provider:  "openai",
			Model:     "gpt-4o",
			Streaming: config.ModelStreamingConfig{Enabled: false},
		},
	}
	cfg.Agents.Defaults.Provider = "openai"
	cfg.Agents.Defaults.ModelName = "openai/gpt-4o"

	before := computeConfigSignature(cfg)

	cfg.ModelList[0].Streaming = config.ModelStreamingConfig{Enabled: true}
	after := computeConfigSignature(cfg)

	if before == after {
		t.Fatal(
			"config signature should change when streaming changes for default-provider prefixed ref with split config",
		)
	}
}

func TestConfigSignatureBareModelRefUsesExactModelBeforeDefaultProviderModelID(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "azure-alias",
			Provider:  "azure",
			Model:     "gpt-4o",
			Streaming: config.ModelStreamingConfig{Enabled: false},
		},
		{
			ModelName: "openai-alias",
			Model:     "openai/gpt-4o",
			Streaming: config.ModelStreamingConfig{Enabled: false},
		},
	}
	cfg.Agents.Defaults.Provider = "openai"
	cfg.Agents.Defaults.ModelName = "gpt-4o"

	before := computeConfigSignature(cfg)

	cfg.ModelList[0].Streaming = config.ModelStreamingConfig{Enabled: true}
	afterExactModelChange := computeConfigSignature(cfg)

	if before == afterExactModelChange {
		t.Fatal("config signature should change when the exact bare model entry changes streaming")
	}

	cfg.ModelList[1].Streaming = config.ModelStreamingConfig{Enabled: true}
	afterDefaultProviderModelChange := computeConfigSignature(cfg)

	if afterExactModelChange != afterDefaultProviderModelChange {
		t.Fatal("config signature should not change when a shadowed default-provider model id changes streaming")
	}
}

func TestGatewayStatusRequiresRestartAfterWebSearchConfigChange(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	model := addGatewayTestModel(cfg)
	cfg.Agents.Defaults.ModelName = model.ModelName
	model.SetAPIKey("test-key")
	cfg.Tools.Web.Enabled = true
	cfg.Tools.Web.Provider = "sogou"
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess() error = %v", err)
	}

	bootSignature := computeConfigSignature(cfg)
	gateway.mu.Lock()
	gateway.cmd = &exec.Cmd{Process: process}
	gateway.bootDefaultModel = model.ModelName
	gateway.bootConfigSignature = bootSignature
	setGatewayRuntimeStatusLocked("running")
	gateway.mu.Unlock()

	updatedCfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	updatedCfg.Tools.Web.Provider = "duckduckgo"
	if err := config.SaveConfig(configPath, updatedCfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	gatewayHealthGet = func(string, time.Duration) (*http.Response, error) {
		return mockGatewayHealthResponse(http.StatusOK, os.Getpid()), nil
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := body["gateway_status"]; got != "running" {
		t.Fatalf("gateway_status = %#v, want %q", got, "running")
	}
	if got := body["gateway_restart_required"]; got != true {
		t.Fatalf("gateway_restart_required = %#v, want true", got)
	}
}

func TestGatewayStatusRequiresRestartAfterAgentRuntimeChange(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	model := addGatewayTestModel(cfg)
	cfg.Agents.Defaults.ModelName = model.ModelName
	model.SetAPIKey("test-key")
	cfg.Agents.Defaults.MaxTokens = 1000
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess() error = %v", err)
	}

	bootSignature := computeConfigSignature(cfg)
	gateway.mu.Lock()
	gateway.cmd = &exec.Cmd{Process: process}
	gateway.bootDefaultModel = model.ModelName
	gateway.bootConfigSignature = bootSignature
	setGatewayRuntimeStatusLocked("running")
	gateway.mu.Unlock()

	updatedCfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	updatedCfg.Agents.Defaults.MaxTokens = 2000
	if err := config.SaveConfig(configPath, updatedCfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	gatewayHealthGet = func(string, time.Duration) (*http.Response, error) {
		return mockGatewayHealthResponse(http.StatusOK, os.Getpid()), nil
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := body["gateway_status"]; got != "running" {
		t.Fatalf("gateway_status = %#v, want %q", got, "running")
	}
	if got := body["gateway_restart_required"]; got != true {
		t.Fatalf("gateway_restart_required = %#v, want true", got)
	}
}

func TestGatewayStatusNoRestartRequiredWhenNotRunning(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	model := addGatewayTestModel(cfg)
	cfg.Agents.Defaults.ModelName = model.ModelName
	model.SetAPIKey("test-key")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	gateway.mu.Lock()
	gateway.cmd = nil
	gateway.bootDefaultModel = ""
	gateway.bootConfigSignature = ""
	setGatewayRuntimeStatusLocked("stopped")
	gateway.mu.Unlock()

	updatedCfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	updatedCfg.Agents.Defaults.ModelName = "different-model"
	if err := config.SaveConfig(configPath, updatedCfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	gatewayHealthGet = func(string, time.Duration) (*http.Response, error) {
		return nil, errors.New("no gateway running")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := body["gateway_status"]; got != "stopped" {
		t.Fatalf("gateway_status = %#v, want %q", got, "stopped")
	}
	if got := body["gateway_restart_required"]; got != false {
		t.Fatalf("gateway_restart_required = %#v, want false", got)
	}
}

func TestGatewayStatusReturnsErrorAfterStartupWindowExpires(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	cmd := startLongRunningProcess(t)
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	gateway.mu.Lock()
	gateway.cmd = cmd
	gateway.bootDefaultModel = "existing-model"
	setGatewayRuntimeStatusLocked("starting")
	gateway.startupDeadline = time.Now().Add(-time.Second)
	gateway.mu.Unlock()

	gatewayHealthGet = func(string, time.Duration) (*http.Response, error) {
		return nil, errors.New("probe failed")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := body["gateway_status"]; got != "error" {
		t.Fatalf("gateway_status = %#v, want %q", got, "error")
	}
}

func TestGatewayStatusReturnsRestartingDuringRestartGap(t *testing.T) {
	resetGatewayTestState(t)

	// Mock health check to return error, so it won't override our "restarting" status
	gatewayHealthGet = func(url string, timeout time.Duration) (*http.Response, error) {
		return nil, errors.New("mock health check error")
	}

	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	gateway.mu.Lock()
	setGatewayRuntimeStatusLocked("restarting")
	gateway.mu.Unlock()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := body["gateway_status"]; got != "restarting" {
		t.Fatalf("gateway_status = %#v, want %q", got, "restarting")
	}
}

func TestGatewayRestartKeepsRunningProcessWhenPreconditionsFail(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	model := addGatewayTestModel(cfg)
	cfg.Agents.Defaults.ModelName = model.ModelName
	model.SetAPIKey("")
	model.AuthMethod = ""
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	cmd := startLongRunningProcess(t)
	t.Cleanup(func() {
		gateway.mu.Lock()
		if gateway.cmd == cmd {
			gateway.cmd = nil
			gateway.bootDefaultModel = ""
		}
		gateway.mu.Unlock()

		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	gateway.mu.Lock()
	gateway.cmd = cmd
	gateway.bootDefaultModel = "existing-model"
	gateway.mu.Unlock()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/gateway/restart", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	gateway.mu.Lock()
	stillRunning := gateway.cmd == cmd && isCmdProcessAliveLocked(cmd)
	gateway.mu.Unlock()

	if !stillRunning {
		t.Fatalf("gateway process was stopped when restart preconditions failed")
	}
}

func TestGatewayRestartKeepsOldProcessWhenItDoesNotExitInTime(t *testing.T) {
	resetGatewayTestState(t)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	model := addGatewayTestModel(cfg)
	cfg.Agents.Defaults.ModelName = model.ModelName
	model.SetAPIKey("test-key")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	cmd := startIgnoringTermProcess(t)
	t.Cleanup(func() {
		gateway.mu.Lock()
		if gateway.cmd == cmd {
			gateway.cmd = nil
			gateway.bootDefaultModel = ""
		}
		gateway.mu.Unlock()

		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	gatewayRestartGracePeriod = 150 * time.Millisecond
	gatewayRestartForceKillWindow = 150 * time.Millisecond
	gatewayRestartPollInterval = 10 * time.Millisecond

	gateway.mu.Lock()
	gateway.cmd = cmd
	gateway.bootDefaultModel = "existing-model"
	setGatewayRuntimeStatusLocked("running")
	gateway.mu.Unlock()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/gateway/restart", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}

	gateway.mu.Lock()
	stillRunning := gateway.cmd == cmd && isCmdProcessAliveLocked(cmd)
	status := gateway.runtimeStatus
	gateway.mu.Unlock()

	if !stillRunning {
		t.Fatalf("gateway process was replaced before the old process exited")
	}
	if status != "running" {
		t.Fatalf("runtimeStatus = %q, want %q", status, "running")
	}
}

func TestGatewayRestartReturnsErrorStatusWhenReplacementFailsToStart(t *testing.T) {
	resetGatewayTestState(t)

	// Mock health check to return error, so it won't override our "error" status
	gatewayHealthGet = func(url string, timeout time.Duration) (*http.Response, error) {
		return nil, errors.New("mock health check error")
	}

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	model := addGatewayTestModel(cfg)
	cfg.Agents.Defaults.ModelName = model.ModelName
	model.SetAPIKey("test-key")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	invalidBinaryPath := filepath.Join(t.TempDir(), "fake-picoclaw")
	if err := os.WriteFile(invalidBinaryPath, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("PICOCLAW_BINARY", invalidBinaryPath)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/gateway/restart", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("restart status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}

	statusRec := httptest.NewRecorder()
	statusReq := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(statusRec, statusReq)

	if statusRec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", statusRec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(statusRec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := body["gateway_status"]; got != "error" {
		t.Fatalf("gateway_status = %#v, want %q", got, "error")
	}
}

func TestGatewayStatusExcludesLogsFields(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/gateway/status", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if _, ok := body["logs"]; ok {
		t.Fatalf("logs unexpectedly present in status response: %#v", body["logs"])
	}
	if _, ok := body["log_total"]; ok {
		t.Fatalf("log_total unexpectedly present in status response: %#v", body["log_total"])
	}
	if _, ok := body["log_run_id"]; ok {
		t.Fatalf("log_run_id unexpectedly present in status response: %#v", body["log_run_id"])
	}
}

func TestGatewayLogsReturnsIncrementalHistory(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	gateway.logs.Clear()
	gateway.logs.Append("first line")
	gateway.logs.Append("second line")
	runID := gateway.logs.RunID()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/gateway/logs?log_offset=1&log_run_id="+strconv.Itoa(runID),
		nil,
	)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("logs status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal logs response: %v", err)
	}

	logs, ok := body["logs"].([]any)
	if !ok {
		t.Fatalf("logs missing or not array: %#v", body["logs"])
	}
	if len(logs) != 1 || logs[0] != "second line" {
		t.Fatalf("logs = %#v, want [\"second line\"]", logs)
	}
	if got := body["log_total"]; got != float64(2) {
		t.Fatalf("log_total = %#v, want 2", got)
	}
	if got := body["log_run_id"]; got != float64(runID) {
		t.Fatalf("log_run_id = %#v, want %d", got, runID)
	}
}

func TestGatewayClearLogsResetsBufferedHistory(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	gateway.logs.Clear()
	gateway.logs.Append("first line")
	gateway.logs.Append("second line")
	previousRunID := gateway.logs.RunID()

	clearRec := httptest.NewRecorder()
	clearReq := httptest.NewRequest(http.MethodPost, "/api/gateway/logs/clear", nil)
	mux.ServeHTTP(clearRec, clearReq)

	if clearRec.Code != http.StatusOK {
		t.Fatalf("clear status = %d, want %d", clearRec.Code, http.StatusOK)
	}

	var clearBody map[string]any
	if err := json.Unmarshal(clearRec.Body.Bytes(), &clearBody); err != nil {
		t.Fatalf("unmarshal clear response: %v", err)
	}

	if got := clearBody["status"]; got != "cleared" {
		t.Fatalf("clear status body = %#v, want %q", got, "cleared")
	}

	clearRunID, ok := clearBody["log_run_id"].(float64)
	if !ok {
		t.Fatalf("log_run_id missing or not number: %#v", clearBody["log_run_id"])
	}
	if int(clearRunID) <= previousRunID {
		t.Fatalf("log_run_id = %d, want > %d", int(clearRunID), previousRunID)
	}

	logsRec := httptest.NewRecorder()
	logsReq := httptest.NewRequest(
		http.MethodGet,
		"/api/gateway/logs?log_offset=0&log_run_id="+strconv.Itoa(previousRunID),
		nil,
	)
	mux.ServeHTTP(logsRec, logsReq)

	if logsRec.Code != http.StatusOK {
		t.Fatalf("logs code = %d, want %d", logsRec.Code, http.StatusOK)
	}

	var logsBody map[string]any
	if err := json.Unmarshal(logsRec.Body.Bytes(), &logsBody); err != nil {
		t.Fatalf("unmarshal logs response: %v", err)
	}

	logs, ok := logsBody["logs"].([]any)
	if !ok {
		t.Fatalf("logs missing or not array: %#v", logsBody["logs"])
	}
	if len(logs) != 0 {
		t.Fatalf("logs len = %d, want 0", len(logs))
	}
	if got := logsBody["log_total"]; got != float64(0) {
		t.Fatalf("log_total = %#v, want 0", got)
	}
	if got := logsBody["log_run_id"]; got != clearBody["log_run_id"] {
		t.Fatalf("log_run_id = %#v, want %#v", got, clearBody["log_run_id"])
	}
}

func TestFindPicoclawBinary_EnvOverride(t *testing.T) {
	// Create a temporary file to act as the mock binary
	tmpDir := t.TempDir()
	mockBinary := filepath.Join(tmpDir, "picoclaw-mock")
	if err := os.WriteFile(mockBinary, []byte("mock"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	t.Setenv("PICOCLAW_BINARY", mockBinary)

	got := utils.FindPicoclawBinary()
	if got != mockBinary {
		t.Errorf("FindPicoclawBinary() = %q, want %q", got, mockBinary)
	}
}

func TestFindPicoclawBinary_EnvOverride_InvalidPath(t *testing.T) {
	// When PICOCLAW_BINARY points to a non-existent path, fall through to next strategy
	t.Setenv("PICOCLAW_BINARY", "/nonexistent/picoclaw-binary")

	got := utils.FindPicoclawBinary()
	// Should not return the invalid path; falls back to "picoclaw" or another found path
	if got == "/nonexistent/picoclaw-binary" {
		t.Errorf("FindPicoclawBinary() returned invalid env path %q, expected fallback", got)
	}
}
