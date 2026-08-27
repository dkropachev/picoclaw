package api

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	ppid "github.com/sipeed/picoclaw/pkg/pid"
)

const (
	apiTestRuntimeProbeOutputEnv = "_PICOCLAW_API_TEST_RUNTIME_PROBE_OUTPUT"
	apiTestRuntimeCanaryReadyEnv = "_PICOCLAW_API_TEST_RUNTIME_CANARY_READY"
)

type apiTestRuntimeProbe struct {
	HOME             string            `json:"home"`
	PicoHome         string            `json:"pico_home"`
	Config           string            `json:"config"`
	Binary           string            `json:"binary"`
	Workspace        string            `json:"workspace"`
	EventDB          string            `json:"event_db"`
	GatewayHost      string            `json:"gateway_host"`
	GatewayPort      int               `json:"gateway_port"`
	LogFile          string            `json:"log_file"`
	GatewayRun       string            `json:"gateway_status"`
	AmbientAuthority map[string]string `json:"ambient_authority"`
}

func TestAPITestRuntimeProbeProcess(t *testing.T) {
	outputPath := os.Getenv(apiTestRuntimeProbeOutputEnv)
	if outputPath == "" {
		t.Skip("API test runtime probe helper")
	}
	outputPath = requireAPITestOwnedHelperPath(t, outputPath)
	cfg, err := config.LoadConfig(os.Getenv(config.EnvConfig))
	if err != nil {
		t.Fatalf("load probe config: %v", err)
	}
	status, _ := NewHandler(os.Getenv(config.EnvConfig)).gatewayStatusData()["gateway_status"].(string)
	authority := make(map[string]string)
	for _, name := range []string{
		"ANTHROPIC_API_KEY",
		"AWS_ACCESS_KEY_ID",
		"AWS_EC2_METADATA_DISABLED",
		"AWS_PROFILE",
		"AWS_SECRET_ACCESS_KEY",
		"AWS_SHARED_CREDENTIALS_FILE",
		"AZURE_CLIENT_SECRET",
		"DOCKER_HOST",
		"GCM_INTERACTIVE",
		"GH_PAT",
		"GH_TOKEN",
		"GITHUB_PAT",
		"GITHUB_TOKEN",
		"GIT_ASKPASS",
		"GIT_TERMINAL_PROMPT",
		"GOAUTH",
		"GOENV",
		"GOOGLE_APPLICATION_CREDENTIALS",
		"KUBECONFIG",
		"NETRC",
		"NOTIFY_SOCKET",
		"LISTEN_FDS",
		"OPENAI_API_KEY",
		"SERVICE_API_KEY",
		"SSH_AUTH_SOCK",
	} {
		authority[name] = os.Getenv(name)
	}
	probe := apiTestRuntimeProbe{
		HOME:             os.Getenv("HOME"),
		PicoHome:         os.Getenv(config.EnvHome),
		Config:           os.Getenv(config.EnvConfig),
		Binary:           os.Getenv(config.EnvBinary),
		Workspace:        cfg.WorkspacePath(),
		EventDB:          cfg.Events.Ingress.DatabasePath,
		GatewayHost:      cfg.Gateway.Host,
		GatewayPort:      cfg.Gateway.Port,
		LogFile:          os.Getenv("PICOCLAW_LOG_FILE"),
		GatewayRun:       status,
		AmbientAuthority: authority,
	}
	raw, err := json.Marshal(probe)
	if err != nil {
		t.Fatalf("encode runtime probe: %v", err)
	}
	if err = os.WriteFile(outputPath, raw, 0o600); err != nil {
		t.Fatalf("write runtime probe: %v", err)
	}
}

func TestAPITestRuntimeCanaryProcess(t *testing.T) {
	readyPath := os.Getenv(apiTestRuntimeCanaryReadyEnv)
	if readyPath == "" {
		t.Skip("API test runtime canary helper")
	}
	readyPath = requireAPITestOwnedHelperPath(t, readyPath)
	if err := os.WriteFile(readyPath, []byte("ready"), 0o600); err != nil {
		t.Fatalf("write canary readiness: %v", err)
	}
	time.Sleep(30 * time.Second)
}

func requireAPITestOwnedHelperPath(t *testing.T, path string) string {
	t.Helper()
	if apiSuiteTestRuntime == nil {
		t.Fatal("API suite test runtime is not initialized")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("resolve API test helper path: %v", err)
	}
	absolute = filepath.Clean(absolute)
	inside, err := pathWithinAPITestRoot(apiSuiteTestRuntime.Root, absolute)
	if err != nil || !inside {
		t.Fatalf("API test helper path escapes suite runtime: %q", absolute)
	}
	return absolute
}

func TestAPITestRuntimeOverridesHostileInheritedState(t *testing.T) {
	if apiSuiteTestRuntime == nil {
		t.Fatal("API suite test runtime is not initialized")
	}
	hostileRoot := t.TempDir()
	hostileHome := filepath.Join(hostileRoot, "home")
	hostilePicoHome := filepath.Join(hostileRoot, "picoclaw")
	if err := os.MkdirAll(hostilePicoHome, 0o700); err != nil {
		t.Fatalf("create hostile PicoClaw home: %v", err)
	}

	readyPath := filepath.Join(hostileRoot, "canary-ready")
	canary := exec.Command(
		apiSuiteTestRuntime.FixtureBinary,
		"-test.run=^TestAPITestRuntimeCanaryProcess$",
	)
	canary.Env = replaceAPITestEnvironment(os.Environ(), map[string]string{
		apiTestRuntimeCanaryReadyEnv: readyPath,
	})
	if err := canary.Start(); err != nil {
		t.Fatalf("start runtime canary: %v", err)
	}
	canaryDone := make(chan error, 1)
	go func() { canaryDone <- canary.Wait() }()
	t.Cleanup(func() {
		_ = canary.Process.Kill()
		select {
		case <-canaryDone:
		case <-time.After(time.Second):
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("runtime canary did not become ready")
		}
		time.Sleep(20 * time.Millisecond)
	}

	pidRaw, err := json.Marshal(ppid.PidFileData{PID: canary.Process.Pid, Token: "hostile-token"})
	if err != nil {
		t.Fatalf("encode hostile PID file: %v", err)
	}
	hostilePIDPath := filepath.Join(hostilePicoHome, ".picoclaw.pid")
	if err = os.WriteFile(hostilePIDPath, pidRaw, 0o600); err != nil {
		t.Fatalf("write hostile PID file: %v", err)
	}
	hostileConfig := filepath.Join(hostileRoot, "config.json")
	if err = os.WriteFile(hostileConfig, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write hostile config: %v", err)
	}
	hostileBinary := filepath.Join(hostileRoot, "hostile-picoclaw-bin")
	if err = os.WriteFile(hostileBinary, []byte("hostile"), 0o700); err != nil {
		t.Fatalf("write hostile binary: %v", err)
	}

	probePath := filepath.Join(hostileRoot, "probe.json")
	probeCommand := exec.Command(
		apiSuiteTestRuntime.FixtureBinary,
		"-test.run=^TestAPITestRuntimeProbeProcess$",
	)
	probeCommand.Env = replaceAPITestEnvironment(os.Environ(), map[string]string{
		"ANTHROPIC_API_KEY":                     "operator-anthropic-key",
		"AWS_ACCESS_KEY_ID":                     "operator-access-key",
		"AWS_EC2_METADATA_DISABLED":             "false",
		"AWS_PROFILE":                           "operator-profile",
		"AWS_SECRET_ACCESS_KEY":                 "operator-secret-key",
		"AWS_SHARED_CREDENTIALS_FILE":           filepath.Join(hostileRoot, "aws-credentials"),
		"AZURE_CLIENT_SECRET":                   "operator-azure-secret",
		"DOCKER_HOST":                           "unix:///operator/docker.sock",
		"GH_TOKEN":                              "operator-gh-token",
		"GH_PAT":                                "operator-gh-pat",
		"GITHUB_TOKEN":                          "operator-github-token",
		"GITHUB_PAT":                            "operator-github-pat",
		"GCM_INTERACTIVE":                       "always",
		"GIT_ASKPASS":                           filepath.Join(hostileRoot, "askpass"),
		"GIT_TERMINAL_PROMPT":                   "1",
		"GOOGLE_APPLICATION_CREDENTIALS":        filepath.Join(hostileRoot, "google.json"),
		"GOAUTH":                                filepath.Join(hostileRoot, "goauth-helper"),
		"GOENV":                                 filepath.Join(hostileRoot, "goenv"),
		"HOME":                                  hostileHome,
		"KUBECONFIG":                            filepath.Join(hostileRoot, "kubeconfig"),
		"NETRC":                                 filepath.Join(hostileRoot, "netrc"),
		"NOTIFY_SOCKET":                         filepath.Join(hostileRoot, "notify.sock"),
		"LISTEN_FDS":                            "3",
		"OPENAI_API_KEY":                        "operator-openai-key",
		"SERVICE_API_KEY":                       "operator-generic-key",
		"SSH_AUTH_SOCK":                         filepath.Join(hostileRoot, "ssh-agent.sock"),
		"USERPROFILE":                           hostileHome,
		"XDG_CONFIG_HOME":                       filepath.Join(hostileHome, "config"),
		config.EnvHome:                          hostilePicoHome,
		config.EnvConfig:                        hostileConfig,
		config.EnvBinary:                        hostileBinary,
		"PICOCLAW_AGENTS_DEFAULTS_WORKSPACE":    filepath.Join(hostileRoot, "hostile-workspace"),
		"PICOCLAW_EVENTS_INGRESS_DATABASE_PATH": filepath.Join(hostileRoot, "hostile-events.db"),
		"PICOCLAW_GATEWAY_HOST":                 "0.0.0.0",
		"PICOCLAW_GATEWAY_PORT":                 "65531",
		"PICOCLAW_LOG_FILE":                     filepath.Join(hostileRoot, "hostile.log"),
		apiTestRuntimeProbeOutputEnv:            probePath,
	})
	if output, err := probeCommand.CombinedOutput(); err != nil {
		t.Fatalf("runtime probe failed: %v\n%s", err, output)
	}
	rawProbe, err := os.ReadFile(probePath)
	if err != nil {
		t.Fatalf("read runtime probe: %v", err)
	}
	var observed apiTestRuntimeProbe
	if err = json.Unmarshal(rawProbe, &observed); err != nil {
		t.Fatalf("decode runtime probe: %v", err)
	}
	if observed.HOME != apiSuiteTestRuntime.OSHome ||
		observed.PicoHome != apiSuiteTestRuntime.PicoHome ||
		observed.Config != apiSuiteTestRuntime.ConfigPath ||
		observed.Binary != apiSuiteTestRuntime.FixtureBinary ||
		observed.Workspace != apiSuiteTestRuntime.Workspace ||
		observed.EventDB != apiSuiteTestRuntime.EventDB {
		t.Fatalf("runtime probe escaped suite manifest: %#v", observed)
	}
	loadedSuiteConfig, err := config.LoadConfig(apiSuiteTestRuntime.ConfigPath)
	if err != nil {
		t.Fatalf("load suite config: %v", err)
	}
	if observed.GatewayHost != loadedSuiteConfig.Gateway.Host ||
		observed.GatewayPort != loadedSuiteConfig.Gateway.Port {
		t.Fatalf("runtime probe inherited hostile gateway endpoint: %#v", observed)
	}
	if observed.LogFile != "" {
		t.Fatalf("runtime probe inherited hostile log path: %#v", observed)
	}
	for _, name := range []string{
		"ANTHROPIC_API_KEY",
		"AWS_ACCESS_KEY_ID",
		"AWS_PROFILE",
		"AWS_SECRET_ACCESS_KEY",
		"AWS_SHARED_CREDENTIALS_FILE",
		"AZURE_CLIENT_SECRET",
		"DOCKER_HOST",
		"GH_TOKEN",
		"GH_PAT",
		"GITHUB_TOKEN",
		"GITHUB_PAT",
		"GIT_ASKPASS",
		"GOOGLE_APPLICATION_CREDENTIALS",
		"KUBECONFIG",
		"NETRC",
		"NOTIFY_SOCKET",
		"LISTEN_FDS",
		"OPENAI_API_KEY",
		"SERVICE_API_KEY",
		"SSH_AUTH_SOCK",
	} {
		if value := observed.AmbientAuthority[name]; value != "" {
			t.Fatalf("runtime probe inherited %s=%q", name, value)
		}
	}
	for name, want := range map[string]string{
		"AWS_EC2_METADATA_DISABLED": "true",
		"GCM_INTERACTIVE":           "never",
		"GIT_TERMINAL_PROMPT":       "0",
		"GOAUTH":                    "off",
		"GOENV":                     "off",
	} {
		if value := observed.AmbientAuthority[name]; value != want {
			t.Fatalf("runtime probe %s=%q, want %q", name, value, want)
		}
	}
	for _, hostilePath := range []string{
		filepath.Join(hostileRoot, "hostile-events.db"),
		filepath.Join(hostileRoot, "hostile.log"),
	} {
		if _, statErr := os.Stat(hostilePath); !os.IsNotExist(statErr) {
			t.Fatalf("runtime probe created hostile path %q: %v", hostilePath, statErr)
		}
	}
	if observed.GatewayRun == "running" {
		t.Fatalf("probe attached hostile gateway PID: %#v", observed)
	}
	if _, err = os.Stat(hostilePIDPath); err != nil {
		t.Fatalf("probe mutated hostile PID file: %v", err)
	}
	select {
	case err = <-canaryDone:
		t.Fatalf("probe stopped hostile canary: %v", err)
	default:
	}
}

func TestAPITestFixtureBinaryRefusesGatewayLaunch(t *testing.T) {
	command := exec.Command(apiSuiteTestRuntime.FixtureBinary, "gateway", "-E")
	command.Env = os.Environ()
	output, err := command.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != apiTestFixtureExitCode {
		t.Fatalf("fixture gateway exit = %v, want code %d\n%s", err, apiTestFixtureExitCode, output)
	}
}

func TestAPITestFixtureBinaryAnswersVersionWithoutRunningSuite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, apiSuiteTestRuntime.FixtureBinary, "version")
	command.Env = os.Environ()
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture version command failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "picoclaw test-fixture") {
		t.Fatalf("fixture version output = %q", output)
	}
}
