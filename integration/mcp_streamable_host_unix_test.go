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
	parentTestPIDPath := filepath.Join(t.TempDir(), "parent-test.pid")
	serverStoppedPath := filepath.Join(t.TempDir(), "server.stopped")
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
      printf '%s\n' 'trap '\''printf stopped >"${SERVER_STOPPED_FILE:?}"; exit 0'\'' TERM INT'
      printf '%s\n' 'printf "%s\n" "$$" >"${SERVER_PID_FILE:?}"'
      printf '%s\n' 'printf "127.0.0.1:12345\n" >"${STREAMABLE_READY_FILE:?}.tmp"'
      printf '%s\n' 'mv "${STREAMABLE_READY_FILE}.tmp" "$STREAMABLE_READY_FILE"'
      printf '%s\n' 'while :; do sleep 0.05; done'
    } >"$output"
    chmod 755 "$output"
    ;;
  test)
    trap '' TERM INT
    printf '%s\n' "$$" >"${TEST_PID_FILE:?}"
    while :; do sleep 0.05; done
    ;;
  *)
    exit 99
    ;;
esac
`)

	command := exec.Command(
		bashPath,
		filepath.Join(repoRoot, "scripts", "run-integration-tests.sh"),
		"mcp-streamable",
	)
	command.Dir = repoRoot
	cacheRoot := t.TempDir()
	command.Env = replaceIntegrationTestEnvironment(os.Environ(), map[string]string{
		"PATH":                                stubDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"TMPDIR":                              runtimeParent,
		"SERVER_PID_FILE":                     serverPIDPath,
		"TEST_PID_FILE":                       testPIDPath,
		"MCP_STREAMABLE_TEST_PARENT_PID_FILE": parentTestPIDPath,
		"SERVER_STOPPED_FILE":                 serverStoppedPath,
		"INTEGRATION_GOCACHE":                 filepath.Join(cacheRoot, "build"),
		"INTEGRATION_GOMODCACHE":              filepath.Join(cacheRoot, "modules"),
	})
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	finished := false
	t.Cleanup(func() {
		if finished || command.Process == nil {
			return
		}
		_ = command.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = command.Process.Kill()
		}
	})

	waitForIntegrationTestPID(t, serverPIDPath, 3*time.Second)
	waitForIntegrationTestPID(t, testPIDPath, 3*time.Second)
	waitForIntegrationTestPID(t, parentTestPIDPath, 3*time.Second)
	if err = command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal runner: %v", err)
	}
	select {
	case waitErr := <-done:
		finished = true
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) || exitErr.ExitCode() != 143 {
			t.Fatalf("runner signal exit = %v, want 143", waitErr)
		}
	case <-time.After(5 * time.Second):
		_ = command.Process.Kill()
		t.Fatal("runner did not exit after SIGTERM")
	}
	waitForIntegrationTestPath(t, serverStoppedPath, time.Second)
	for _, path := range []string{serverPIDPath, testPIDPath} {
		assertIntegrationTestPIDStopped(t, path)
	}
	runtimeDirs, err := filepath.Glob(filepath.Join(runtimeParent, "picoclaw-mcp-streamable.*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(runtimeDirs) != 0 {
		t.Fatalf("fixture runtime directories remain: %v", runtimeDirs)
	}
}

func TestMCPStreamableHostSuiteReapsBuildOnTopLevelSignal(t *testing.T) {
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	for _, test := range []struct {
		name     string
		signal   os.Signal
		exitCode int
	}{
		{name: "interrupt", signal: os.Interrupt, exitCode: 130},
		{name: "terminate", signal: syscall.SIGTERM, exitCode: 143},
	} {
		t.Run(test.name, func(t *testing.T) {
			testMCPStreamableHostBuildSignal(t, bashPath, test.signal, test.exitCode)
		})
	}
}

func testMCPStreamableHostBuildSignal(
	t *testing.T,
	bashPath string,
	signal os.Signal,
	wantExit int,
) {
	t.Helper()
	repoRoot := repoRootFromTestFile(t)
	stubDir := t.TempDir()
	runtimeParent := t.TempDir()
	buildPIDPath := filepath.Join(t.TempDir(), "build.pid")
	buildStoppedPath := filepath.Join(t.TempDir(), "build.stopped")
	writeExecutable(t, filepath.Join(stubDir, "curl"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(stubDir, "go"), `#!/bin/sh
set -eu
[ "${1:-}" = build ]
trap 'printf stopped >"${BUILD_STOPPED_FILE:?}"; exit 0' TERM INT
printf '%s\n' "$$" >"${BUILD_PID_FILE:?}"
while :; do sleep 0.05; done
`)

	command := exec.Command(
		bashPath,
		filepath.Join(repoRoot, "scripts", "run-integration-tests.sh"),
		"mcp-streamable",
	)
	command.Dir = repoRoot
	cacheRoot := t.TempDir()
	command.Env = replaceIntegrationTestEnvironment(os.Environ(), map[string]string{
		"PATH":                   stubDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"TMPDIR":                 runtimeParent,
		"BUILD_PID_FILE":         buildPIDPath,
		"BUILD_STOPPED_FILE":     buildStoppedPath,
		"INTEGRATION_GOCACHE":    filepath.Join(cacheRoot, "build"),
		"INTEGRATION_GOMODCACHE": filepath.Join(cacheRoot, "modules"),
	})
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	finished := false
	t.Cleanup(func() {
		if finished || command.Process == nil {
			return
		}
		_ = command.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = command.Process.Kill()
		}
	})

	waitForIntegrationTestPID(t, buildPIDPath, 3*time.Second)
	if err := command.Process.Signal(signal); err != nil {
		t.Fatalf("signal top-level runner during build: %v", err)
	}
	select {
	case waitErr := <-done:
		finished = true
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) || exitErr.ExitCode() != wantExit {
			t.Fatalf("top-level build signal exit = %v, want %d", waitErr, wantExit)
		}
	case <-time.After(5 * time.Second):
		_ = command.Process.Kill()
		t.Fatal("top-level runner did not exit after build signal")
	}
	waitForIntegrationTestPath(t, buildStoppedPath, time.Second)
	assertIntegrationTestPIDStopped(t, buildPIDPath)
	runtimeDirs, globErr := filepath.Glob(filepath.Join(runtimeParent, "picoclaw-mcp-streamable.*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(runtimeDirs) != 0 {
		t.Fatalf("build-interrupted runtime directories remain: %v", runtimeDirs)
	}
}

func assertIntegrationTestPIDStopped(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("child PID %d remains after runner exit: %v", pid, err)
	}
}

func waitForIntegrationTestPID(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		raw, err := os.ReadFile(path)
		if err == nil {
			value := string(raw)
			pid, parseErr := strconv.Atoi(strings.TrimSpace(value))
			if strings.HasSuffix(value, "\n") && parseErr == nil && pid > 0 {
				return
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read %s: %v", path, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for complete PID in %s", path)
		}
		time.Sleep(5 * time.Millisecond)
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
