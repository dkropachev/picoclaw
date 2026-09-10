//go:build !windows

package integration

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestRunIntegrationTestsScriptWaitsForDockerCleanupOnSignal(t *testing.T) {
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	repoRoot := repoRootFromTestFile(t)
	suiteDir, err := os.MkdirTemp(
		filepath.Join(repoRoot, "integration", "suites"),
		"runner-signal-docker-",
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(suiteDir) })
	if err = os.WriteFile(
		filepath.Join(suiteDir, "suite.env"),
		[]byte("TEST_COMMAND='printf runner-ok'\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(
		filepath.Join(suiteDir, "docker-compose.yml"),
		[]byte("services:\n  integration-runner: {}\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	stubDir := t.TempDir()
	runPIDPath := filepath.Join(t.TempDir(), "docker-run.pid")
	cleanupDonePath := filepath.Join(t.TempDir(), "docker-cleanup.done")
	writeExecutable(t, filepath.Join(stubDir, "docker"), `#!/bin/sh
set -eu
case "$*" in
  *" config --services")
    printf '%s\n' integration-runner
    ;;
  *" run --rm -T "*)
    printf '%s\n' "$$" >"${DOCKER_RUN_PID_FILE:?}"
    trap 'exit 0' TERM INT
    while :; do sleep 0.05; done
    ;;
  *" down -v --remove-orphans"*)
    sleep 1.2
    printf done >"${DOCKER_CLEANUP_DONE_FILE:?}"
    ;;
  *)
    printf 'unexpected docker invocation: %s\n' "$*" >&2
    exit 99
    ;;
esac
`)

	command := exec.Command(
		bashPath,
		filepath.Join(repoRoot, "scripts", "run-integration-tests.sh"),
		filepath.Base(suiteDir),
	)
	command.Dir = repoRoot
	cacheRoot := t.TempDir()
	command.Env = replaceIntegrationTestEnvironment(os.Environ(), map[string]string{
		"PATH":                     stubDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"DOCKER_RUN_PID_FILE":      runPIDPath,
		"DOCKER_CLEANUP_DONE_FILE": cleanupDonePath,
		"INTEGRATION_GOCACHE":      filepath.Join(cacheRoot, "build"),
		"INTEGRATION_GOMODCACHE":   filepath.Join(cacheRoot, "modules"),
		"INTEGRATION_RUNNER_UID":   "12345",
		"INTEGRATION_RUNNER_GID":   "23456",
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
		case <-time.After(3 * time.Second):
			_ = command.Process.Kill()
		}
	})

	waitForIntegrationTestPath(t, runPIDPath, 3*time.Second)
	if err = command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal Docker integration runner: %v", err)
	}
	select {
	case waitErr := <-done:
		finished = true
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) || exitErr.ExitCode() != 143 {
			t.Fatalf("Docker runner signal exit = %v, want 143", waitErr)
		}
	case <-time.After(5 * time.Second):
		_ = command.Process.Kill()
		t.Fatal("Docker runner did not finish bounded cleanup")
	}
	waitForIntegrationTestPath(t, cleanupDonePath, time.Second)
	assertIntegrationTestPIDStopped(t, runPIDPath)
}
