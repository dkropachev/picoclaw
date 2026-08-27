package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestHermeticGoTestRunnerBuildsAndRunsInsideDisposableProductState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Bash runner is exercised by Unix CI")
	}
	_, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is unavailable")
	}

	repoRoot := repoRootForTest(t)
	captureDir := t.TempDir()
	callerDir := t.TempDir()
	operatorHome := t.TempDir()
	operatorSentinel := filepath.Join(operatorHome, "operator-state")
	if err = os.WriteFile(operatorSentinel, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	stubGo := writeHermeticRunnerGoStub(t, captureDir)
	probe := filepath.Join(t.TempDir(), "probe")
	if err = os.WriteFile(probe, []byte(`#!/usr/bin/env bash
set -euo pipefail
test "$#" -eq 2
test -x "$PICOCLAW_BINARY"
test -f "$PICOCLAW_TEST_CONFIG"
test -f "$PICOCLAW_TEST_EVENT_DB"
{
  printf 'TEST_ROOT=%s\n' "$PICOCLAW_TEST_ROOT"
  printf 'HOME=%s\n' "$HOME"
  printf 'USERPROFILE=%s\n' "$USERPROFILE"
  printf 'XDG_CONFIG_HOME=%s\n' "$XDG_CONFIG_HOME"
  printf 'XDG_RUNTIME_DIR=%s\n' "$XDG_RUNTIME_DIR"
  printf 'PICOCLAW_HOME=%s\n' "$PICOCLAW_HOME"
  printf 'PICOCLAW_BINARY=%s\n' "$PICOCLAW_BINARY"
  printf 'PICOCLAW_TEST_CONFIG=%s\n' "$PICOCLAW_TEST_CONFIG"
  printf 'PICOCLAW_TEST_EVENT_DB=%s\n' "$PICOCLAW_TEST_EVENT_DB"
  printf 'PICOCLAW_CONFIG=%s\n' "${PICOCLAW_CONFIG-}"
  printf 'CODEX_HOME=%s\n' "$CODEX_HOME"
  printf 'CLAUDE_CONFIG_DIR=%s\n' "$CLAUDE_CONFIG_DIR"
  printf 'OPENCLAW_HOME=%s\n' "$OPENCLAW_HOME"
  printf 'AWS_ACCESS_KEY_ID=%s\n' "${AWS_ACCESS_KEY_ID-}"
  printf 'AWS_SECRET_ACCESS_KEY=%s\n' "${AWS_SECRET_ACCESS_KEY-}"
  printf 'AWS_PROFILE=%s\n' "${AWS_PROFILE-}"
  printf 'AWS_SHARED_CREDENTIALS_FILE=%s\n' "${AWS_SHARED_CREDENTIALS_FILE-}"
  printf 'AWS_EC2_METADATA_DISABLED=%s\n' "$AWS_EC2_METADATA_DISABLED"
  printf 'OPENAI_API_KEY=%s\n' "${OPENAI_API_KEY-}"
  printf 'ANTHROPIC_API_KEY=%s\n' "${ANTHROPIC_API_KEY-}"
  printf 'GITHUB_TOKEN=%s\n' "${GITHUB_TOKEN-}"
  printf 'GITHUB_PAT=%s\n' "${GITHUB_PAT-}"
  printf 'GH_TOKEN=%s\n' "${GH_TOKEN-}"
  printf 'GH_PAT=%s\n' "${GH_PAT-}"
  printf 'SSH_AUTH_SOCK=%s\n' "${SSH_AUTH_SOCK-}"
  printf 'GOOGLE_APPLICATION_CREDENTIALS=%s\n' "${GOOGLE_APPLICATION_CREDENTIALS-}"
  printf 'AZURE_CLIENT_SECRET=%s\n' "${AZURE_CLIENT_SECRET-}"
  printf 'KUBECONFIG=%s\n' "${KUBECONFIG-}"
  printf 'DOCKER_HOST=%s\n' "${DOCKER_HOST-}"
  printf 'GIT_ASKPASS=%s\n' "${GIT_ASKPASS-}"
  printf 'NETRC=%s\n' "${NETRC-}"
  printf 'SERVICE_API_KEY=%s\n' "${SERVICE_API_KEY-}"
  printf 'NOTIFY_SOCKET=%s\n' "${NOTIFY_SOCKET-}"
  printf 'LISTEN_FDS=%s\n' "${LISTEN_FDS-}"
  printf 'GOAUTH=%s\n' "$GOAUTH"
  printf 'GOENV=%s\n' "$GOENV"
  printf 'GIT_TERMINAL_PROMPT=%s\n' "$GIT_TERMINAL_PROMPT"
  printf 'GCM_INTERACTIVE=%s\n' "$GCM_INTERACTIVE"
  printf 'PWD=%s\n' "$PWD"
} >"$CAPTURE_DIR/environment"
printf '%s\n' "$1" >"$CAPTURE_DIR/arg-1"
printf '%s\n' "$2" >"$CAPTURE_DIR/arg-2"
cp "$PICOCLAW_TEST_CONFIG" "$CAPTURE_DIR/config.json"
`), 0o755); err != nil {
		t.Fatal(err)
	}

	runner := buildHermeticRunnerExecutable(t)
	command := exec.Command(runner, probe, "argument with spaces", "*")
	command.Dir = callerDir
	command.Env = hermeticRunnerTestEnvironment(t, map[string]string{
		"CAPTURE_DIR":                           captureDir,
		"ANTHROPIC_API_KEY":                     "operator-anthropic-key",
		"AWS_ACCESS_KEY_ID":                     "operator-access-key",
		"AWS_PROFILE":                           "operator-profile",
		"AWS_SECRET_ACCESS_KEY":                 "operator-secret-key",
		"AWS_SHARED_CREDENTIALS_FILE":           filepath.Join(operatorHome, "aws-credentials"),
		"AZURE_CLIENT_SECRET":                   "operator-azure-secret",
		"DOCKER_HOST":                           "unix:///run/user/operator/docker.sock",
		"GOCACHE":                               filepath.Join(t.TempDir(), "go-build"),
		"GH_TOKEN":                              "operator-gh-token",
		"GH_PAT":                                "operator-gh-pat",
		"GITHUB_TOKEN":                          "operator-github-token",
		"GITHUB_PAT":                            "operator-github-pat",
		"GIT_ASKPASS":                           filepath.Join(operatorHome, "askpass"),
		"GIT_TERMINAL_PROMPT":                   "1",
		"GCM_INTERACTIVE":                       "always",
		"GOOGLE_APPLICATION_CREDENTIALS":        filepath.Join(operatorHome, "google.json"),
		"GOAUTH":                                filepath.Join(operatorHome, "goauth-helper"),
		"GOENV":                                 filepath.Join(operatorHome, "goenv"),
		"GOMODCACHE":                            filepath.Join(t.TempDir(), "go-mod"),
		"HOME":                                  operatorHome,
		"PICOCLAW_BINARY":                       filepath.Join(operatorHome, "bin", "picoclaw"),
		"PICOCLAW_CONFIG":                       filepath.Join(operatorHome, "config.json"),
		"PICOCLAW_EVENTS_INGRESS_DATABASE_PATH": filepath.Join(operatorHome, "events.db"),
		"PICOCLAW_HOME":                         filepath.Join(operatorHome, ".picoclaw"),
		"PICOCLAW_TEST_GO_BINARY":               stubGo,
		"PICOCLAW_UNRELATED_SENTINEL":           "must-not-survive",
		"KUBECONFIG":                            filepath.Join(operatorHome, "kubeconfig"),
		"NETRC":                                 filepath.Join(operatorHome, "netrc"),
		"NOTIFY_SOCKET":                         filepath.Join(operatorHome, "notify.sock"),
		"LISTEN_FDS":                            "3",
		"OPENAI_API_KEY":                        "operator-openai-key",
		"SERVICE_API_KEY":                       "operator-generic-key",
		"SSH_AUTH_SOCK":                         filepath.Join(operatorHome, "ssh-agent.sock"),
		"TMPDIR":                                t.TempDir(),
	})
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("runner failed: %v\n%s", err, output)
	}

	environment := readHermeticRunnerEnvironment(t, filepath.Join(captureDir, "environment"))
	testRoot := environment["TEST_ROOT"]
	if testRoot == "" {
		t.Fatal("runner did not expose test root")
	}
	if _, err = os.Stat(testRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("test root survived command: %q, err=%v", testRoot, err)
	}
	sentinelData, readErr := os.ReadFile(operatorSentinel)
	if readErr != nil || string(sentinelData) != "untouched" {
		t.Fatalf("operator state changed: data=%q err=%v", sentinelData, readErr)
	}

	wantValues := map[string]string{
		"HOME":                      filepath.Join(testRoot, "home"),
		"USERPROFILE":               filepath.Join(testRoot, "home"),
		"XDG_CONFIG_HOME":           filepath.Join(testRoot, "xdg", "config"),
		"XDG_RUNTIME_DIR":           filepath.Join(testRoot, "xdg", "runtime"),
		"PICOCLAW_HOME":             filepath.Join(testRoot, "picoclaw"),
		"PICOCLAW_BINARY":           filepath.Join(testRoot, "bin", "picoclaw"),
		"PICOCLAW_TEST_CONFIG":      filepath.Join(testRoot, "picoclaw", "config.json"),
		"PICOCLAW_TEST_EVENT_DB":    filepath.Join(testRoot, "picoclaw", "workspace", "eventing", "events.db"),
		"PICOCLAW_CONFIG":           filepath.Join(testRoot, "picoclaw", "config.json"),
		"CODEX_HOME":                filepath.Join(testRoot, "codex"),
		"CLAUDE_CONFIG_DIR":         filepath.Join(testRoot, "claude"),
		"OPENCLAW_HOME":             filepath.Join(testRoot, "openclaw"),
		"AWS_EC2_METADATA_DISABLED": "true",
		"GIT_TERMINAL_PROMPT":       "0",
		"GCM_INTERACTIVE":           "never",
		"GOAUTH":                    "off",
		"GOENV":                     "off",
		"PWD":                       callerDir,
	}
	for _, key := range []string{
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
		if got := environment[key]; got != "" {
			t.Errorf("%s = %q, want stripped", key, got)
		}
	}
	for key, want := range wantValues {
		if got := environment[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	assertHermeticRunnerFile(t, filepath.Join(captureDir, "arg-1"), "argument with spaces\n")
	assertHermeticRunnerFile(t, filepath.Join(captureDir, "arg-2"), "*\n")
	assertHermeticRunnerFile(t, filepath.Join(captureDir, "build-cwd"), repoRoot+"\n")

	buildArgsRaw := readHermeticRunnerFile(t, filepath.Join(captureDir, "build-args"))
	buildArgs := strings.Split(strings.TrimSpace(buildArgsRaw), "\n")
	if len(buildArgs) != 6 || buildArgs[0] != "build" || buildArgs[1] != "-tags" ||
		buildArgs[2] != "goolm,stdjson" || buildArgs[3] != "-o" ||
		buildArgs[4] != filepath.Join(testRoot, "bin", "picoclaw") || buildArgs[5] != "./cmd/picoclaw" {
		t.Fatalf("build args = %#v", buildArgs)
	}

	var configFile struct {
		Agents struct {
			Defaults struct {
				Workspace string `json:"workspace"`
			} `json:"defaults"`
		} `json:"agents"`
		Events struct {
			Ingress struct {
				DatabasePath string `json:"database_path"`
			} `json:"ingress"`
		} `json:"events"`
		Gateway struct {
			Host string `json:"host"`
			Port int    `json:"port"`
		} `json:"gateway"`
	}
	configData := []byte(readHermeticRunnerFile(t, filepath.Join(captureDir, "config.json")))
	if err = json.Unmarshal(configData, &configFile); err != nil {
		t.Fatalf("decode generated config: %v\n%s", err, configData)
	}
	wantWorkspace := filepath.Join(testRoot, "picoclaw", "workspace")
	wantEventDB := filepath.Join(wantWorkspace, "eventing", "events.db")
	if configFile.Agents.Defaults.Workspace != wantWorkspace ||
		configFile.Events.Ingress.DatabasePath != wantEventDB {
		t.Fatalf("generated config escaped sandbox: %#v", configFile)
	}
	if configFile.Gateway.Host != "127.0.0.1" || configFile.Gateway.Port <= 0 ||
		configFile.Gateway.Port == 18790 {
		t.Fatalf("generated gateway endpoint is not ephemeral loopback: %#v", configFile.Gateway)
	}
}

func TestHermeticGoTestRunnerPropagatesCommandFailureAndCleansUp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Bash runner is exercised by Unix CI")
	}
	_, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is unavailable")
	}
	captureDir := t.TempDir()
	stubGo := writeHermeticRunnerGoStub(t, captureDir)
	probe := filepath.Join(t.TempDir(), "failing-probe")
	probeScript := `#!/usr/bin/env bash
printf '%s' "$PICOCLAW_TEST_ROOT" >"$CAPTURE_DIR/test-root"
exit 23
`
	if err = os.WriteFile(probe, []byte(probeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(buildHermeticRunnerExecutable(t), probe)
	command.Env = hermeticRunnerTestEnvironment(t, map[string]string{
		"CAPTURE_DIR":             captureDir,
		"GOCACHE":                 filepath.Join(t.TempDir(), "go-build"),
		"GOMODCACHE":              filepath.Join(t.TempDir(), "go-mod"),
		"PICOCLAW_TEST_GO_BINARY": stubGo,
		"TMPDIR":                  t.TempDir(),
	})
	err = command.Run()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 23 {
		t.Fatalf("runner error = %v, want exit 23", err)
	}
	testRoot := readHermeticRunnerFile(t, filepath.Join(captureDir, "test-root"))
	if _, statErr := os.Stat(testRoot); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed command left test root %q: %v", testRoot, statErr)
	}
}

func TestHermeticGoTestRunnerPropagatesCleanupFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix directory permissions provide the cleanup failure fixture")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is unavailable")
	}
	captureDir := t.TempDir()
	stubGo := writeHermeticRunnerGoStub(t, captureDir)
	probe := filepath.Join(t.TempDir(), "cleanup-failure-probe")
	if err := os.WriteFile(probe, []byte(`#!/usr/bin/env bash
set -euo pipefail
printf '%s' "$PICOCLAW_TEST_ROOT" >"$CAPTURE_DIR/test-root"
chmod 000 "$PICOCLAW_TEST_ROOT"
`), 0o755); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(buildHermeticRunnerExecutable(t), probe)
	command.Env = hermeticRunnerTestEnvironment(t, map[string]string{
		"CAPTURE_DIR":             captureDir,
		"GOCACHE":                 filepath.Join(t.TempDir(), "go-build"),
		"GOMODCACHE":              filepath.Join(t.TempDir(), "go-mod"),
		"PICOCLAW_TEST_GO_BINARY": stubGo,
		"TMPDIR":                  t.TempDir(),
	})
	err := command.Run()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 1 {
		t.Fatalf("runner cleanup error = %v, want exit 1", err)
	}
	testRoot := readHermeticRunnerFile(t, filepath.Join(captureDir, "test-root"))
	if chmodErr := os.Chmod(testRoot, 0o700); chmodErr != nil {
		t.Fatalf("restore failed test root permissions: %v", chmodErr)
	}
	if removeErr := os.RemoveAll(testRoot); removeErr != nil {
		t.Fatalf("remove failed test root fixture: %v", removeErr)
	}
}

func TestOfficialGoTestEntryPointsUseHermeticRunner(t *testing.T) {
	checks := map[string]string{
		"Makefile":                 "$(GO) run ./scripts/hermetic-go-test -- $(GO) test",
		"web/Makefile":             "${WEB_GO} run ../../scripts/hermetic-go-test -- $(GO) test",
		".github/workflows/pr.yml": "go run ./scripts/hermetic-go-test -- go test -tags goolm,stdjson ./...",
	}
	for path, snippet := range checks {
		if content := readRepoFile(t, path); !strings.Contains(content, snippet) {
			t.Errorf("%s does not use official hermetic runner", path)
		}
	}
}

func buildHermeticRunnerExecutable(t *testing.T) string {
	t.Helper()
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go executable is unavailable")
	}
	output := filepath.Join(t.TempDir(), "hermetic-go-test")
	if runtime.GOOS == "windows" {
		output += ".exe"
	}
	command := exec.Command(goBinary, "build", "-o", output, "./scripts/hermetic-go-test")
	command.Dir = repoRootForTest(t)
	if combined, buildErr := command.CombinedOutput(); buildErr != nil {
		t.Fatalf("build hermetic runner: %v\n%s", buildErr, combined)
	}
	return output
}

func writeHermeticRunnerGoStub(t *testing.T, captureDir string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "go")
	script := `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$PWD" >"$CAPTURE_DIR/build-cwd"
printf '%s\n' "$@" >"$CAPTURE_DIR/build-args"
test "${1-}" = build
shift
output=""
while [[ "$#" -gt 0 ]]; do
  if [[ "$1" = -o ]]; then
    output="$2"
    shift 2
    continue
  fi
  shift
done
test -n "$output"
mkdir -p "$(dirname "$output")"
printf '#!/bin/sh\nexit 0\n' >"$output"
chmod 0755 "$output"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func hermeticRunnerTestEnvironment(t *testing.T, overrides map[string]string) []string {
	t.Helper()
	environment := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if _, replaced := overrides[name]; ok && replaced {
			continue
		}
		environment = append(environment, entry)
	}
	for name, value := range overrides {
		environment = append(environment, name+"="+value)
	}
	return environment
}

func readHermeticRunnerEnvironment(t *testing.T, path string) map[string]string {
	t.Helper()
	result := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(readHermeticRunnerFile(t, path)), "\n") {
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("invalid captured environment line %q", line)
		}
		result[name] = value
	}
	return result
}

func assertHermeticRunnerFile(t *testing.T, path, want string) {
	t.Helper()
	if got := readHermeticRunnerFile(t, path); got != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func readHermeticRunnerFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
