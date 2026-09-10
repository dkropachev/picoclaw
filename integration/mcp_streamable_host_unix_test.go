//go:build !windows

package integration

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMCPStreamableHostSuiteReapsFixtureAndTestOnSignal(t *testing.T) {
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}

	repoRoot := repoRootFromTestFile(t)
	stubDir := t.TempDir()
	runtimeParent := t.TempDir()
	serverPIDPath := filepath.Join(t.TempDir(), "server.pid")
	testPIDPath := filepath.Join(t.TempDir(), "test.pid")
	serverStoppedPath := filepath.Join(t.TempDir(), "server.stopped")
	testStoppedPath := filepath.Join(t.TempDir(), "test.stopped")
	writeExecutable(t, filepath.Join(stubDir, "curl"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(stubDir, "go"), `#!/bin/sh
set -eu
case "${1:-}" in
  build)
    output=""
    shift
    while [ "$#" -gt 0 ]; do
      if [ "$1" = "-o" ] && [ "$#" -gt 1 ]; then
        output="$2"
        break
      fi
      shift
    done
    [ -n "$output" ]
    {
      printf '%s\n' '#!/bin/sh'
      printf '%s\n' 'set -eu'
      printf '%s\n' 'printf "%s\n" "$$" >"${SERVER_PID_FILE:?}"'
      printf '%s\n' 'trap '\''printf stopped >"${SERVER_STOPPED_FILE:?}"; exit 0'\'' TERM INT'
      printf '%s\n' 'printf "127.0.0.1:12345\n" >"${STREAMABLE_READY_FILE:?}.tmp"'
      printf '%s\n' 'mv "${STREAMABLE_READY_FILE}.tmp" "$STREAMABLE_READY_FILE"'
      printf '%s\n' 'while :; do sleep 0.05; done'
    } >"$output"
    chmod 755 "$output"
    ;;
  test)
    printf '%s\n' "$$" >"${TEST_PID_FILE:?}"
    trap 'printf stopped >"${TEST_STOPPED_FILE:?}"; exit 0' TERM INT
    while :; do sleep 0.05; done
    ;;
  *)
    exit 99
    ;;
esac
`)

	command := exec.Command(bashPath, filepath.Join(repoRoot, "integration", "suites", "mcp-streamable", "run.sh"))
	command.Dir = repoRoot
	command.Env = replaceIntegrationTestEnvironment(os.Environ(), map[string]string{
		"PATH":                stubDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"TMPDIR":              runtimeParent,
		"SERVER_PID_FILE":     serverPIDPath,
		"TEST_PID_FILE":       testPIDPath,
		"SERVER_STOPPED_FILE": serverStoppedPath,
		"TEST_STOPPED_FILE":   testStoppedPath,
	})
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	t.Cleanup(func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		select {
		case <-done:
		default:
		}
	})

	waitForIntegrationTestPath(t, testPIDPath, 3*time.Second)
	if err = command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal runner: %v", err)
	}
	select {
	case waitErr := <-done:
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) || exitErr.ExitCode() != 143 {
			t.Fatalf("runner signal exit = %v, want 143", waitErr)
		}
	case <-time.After(5 * time.Second):
		_ = command.Process.Kill()
		t.Fatal("runner did not exit after SIGTERM")
	}
	for _, marker := range []string{serverStoppedPath, testStoppedPath} {
		waitForIntegrationTestPath(t, marker, time.Second)
	}
	for _, path := range []string{serverPIDPath, testPIDPath} {
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		pid, parseErr := strconv.Atoi(strings.TrimSpace(string(raw)))
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		if signalErr := syscall.Kill(pid, 0); !errors.Is(signalErr, syscall.ESRCH) {
			t.Errorf("child PID %d remains after runner exit: %v", pid, signalErr)
		}
	}
	runtimeDirs, err := filepath.Glob(filepath.Join(runtimeParent, "picoclaw-mcp-streamable.*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(runtimeDirs) != 0 {
		t.Fatalf("fixture runtime directories remain: %v", runtimeDirs)
	}
}

func waitForIntegrationTestPath(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("inspect %s: %v", path, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
