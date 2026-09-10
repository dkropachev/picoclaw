package integration

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRunIntegrationTestsScriptExecutesSuiteCommand(t *testing.T) {
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}

	repoRoot := repoRootFromTestFile(t)
	suitesRoot := filepath.Join(repoRoot, "integration", "suites")
	suiteDir, err := os.MkdirTemp(suitesRoot, "runner-script-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(suiteDir)
	})

	suiteName := filepath.Base(suiteDir)
	err = os.WriteFile(
		filepath.Join(suiteDir, "suite.env"),
		[]byte("TEST_COMMAND='printf runner-ok'\n"),
		0o644,
	)
	if err != nil {
		t.Fatalf("WriteFile(suite.env) error = %v", err)
	}
	err = os.WriteFile(
		filepath.Join(suiteDir, "docker-compose.yml"),
		[]byte("services:\n  fake-dependency:\n    image: busybox\n"),
		0o644,
	)
	if err != nil {
		t.Fatalf("WriteFile(docker-compose.yml) error = %v", err)
	}

	stubDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "docker.log")
	err = os.WriteFile(filepath.Join(stubDir, "docker"), []byte(`#!/bin/sh
set -eu

log_file="${DOCKER_LOG:?}"
{
  printf '%s\n' '---'
  for arg in "$@"; do
    printf '%s\n' "$arg"
  done
} >>"$log_file"

subcommand=""
for arg in "$@"; do
  case "$arg" in
    config|up|run|down)
      subcommand="$arg"
      ;;
  esac
done

case "$subcommand" in
  config)
    printf '%s\n' integration-runner fake-dependency
    ;;
  up)
    ;;
  run)
    printf '%s\n' runner-ok
    ;;
  down)
    ;;
  *)
    printf 'unexpected docker invocation: %s\n' "$*" >&2
    exit 1
    ;;
esac
`), 0o755)
	if err != nil {
		t.Fatalf("WriteFile(docker stub) error = %v", err)
	}

	cmd := exec.Command(bashPath, filepath.Join(repoRoot, "scripts", "run-integration-tests.sh"), suiteName)
	cmd.Dir = repoRoot
	cacheRoot := t.TempDir()
	cmd.Env = replaceIntegrationTestEnvironment(os.Environ(), map[string]string{
		"PATH":                   stubDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"DOCKER_LOG":             logPath,
		"INTEGRATION_GOCACHE":    filepath.Join(cacheRoot, "build"),
		"INTEGRATION_GOMODCACHE": filepath.Join(cacheRoot, "modules"),
		"INTEGRATION_RUNNER_UID": "12345",
		"INTEGRATION_RUNNER_GID": "23456",
	})

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run-integration-tests.sh error = %v\noutput:\n%s", err, output)
	}
	if !strings.Contains(string(output), "runner-ok") {
		t.Fatalf("script output did not include runner output:\n%s", output)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(logPath) error = %v", err)
	}
	assertLoggedComposeProject(t, string(logData), "picoclaw-int-"+suiteName)
	defaultDownArgs := findLoggedDockerInvocation(t, string(logData), "down")
	if containsArg(defaultDownArgs, "--rmi") {
		t.Fatalf("default integration cleanup unexpectedly removes images:\n%v", defaultDownArgs)
	}

	runArgs := findLoggedDockerInvocation(t, string(logData), "run")
	if strings.Contains(strings.Join(runArgs, "\n"), "\nsh\n-c\n") {
		t.Fatalf("docker compose run unexpectedly wrapped TEST_COMMAND with sh -c:\n%v", runArgs)
	}

	if !containsArg(runArgs, "integration-runner") {
		t.Fatalf("docker compose run args missing runner service:\n%v", runArgs)
	}
	if !containsArg(runArgs, "-T") {
		t.Fatalf("docker compose run did not disable pseudo-TTY allocation:\n%v", runArgs)
	}
	if !containsArg(runArgs, "printf runner-ok") {
		t.Fatalf("docker compose run args missing suite command as a single argument:\n%v", runArgs)
	}
	if !strings.Contains(strings.Join(runArgs, "\n"), "-count=1") {
		t.Fatalf("docker compose run does not disable cached Go test results:\n%v", runArgs)
	}
}

func TestRunIntegrationTestsScriptInjectsCoverageForGoTest(t *testing.T) {
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}

	repoRoot := repoRootFromTestFile(t)
	suitesRoot := filepath.Join(repoRoot, "integration", "suites")
	suiteDir, err := os.MkdirTemp(suitesRoot, "runner-coverage-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(suiteDir)
		_ = os.RemoveAll(filepath.Join(repoRoot, ".coverage", "runner-test"))
	})

	suiteName := filepath.Base(suiteDir)
	err = os.WriteFile(
		filepath.Join(suiteDir, "suite.env"),
		[]byte("TEST_COMMAND='go test ./pkg/mcp -run TestIntegration -v'\n"+
			"INTEGRATION_COMPOSE_PROJECT_NAMESPACE=manifest-override\n"+
			"INTEGRATION_GOMAXPROCS=99\n"),
		0o644,
	)
	if err != nil {
		t.Fatalf("WriteFile(suite.env) error = %v", err)
	}
	err = os.WriteFile(
		filepath.Join(suiteDir, "docker-compose.yml"),
		[]byte("services:\n  fake-dependency:\n    image: busybox\n"),
		0o644,
	)
	if err != nil {
		t.Fatalf("WriteFile(docker-compose.yml) error = %v", err)
	}

	stubDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "docker.log")
	err = os.WriteFile(filepath.Join(stubDir, "docker"), []byte(`#!/bin/sh
set -eu

log_file="${DOCKER_LOG:?}"
{
  printf '%s\n' '---'
  for arg in "$@"; do
    printf '%s\n' "$arg"
  done
} >>"$log_file"

subcommand=""
for arg in "$@"; do
  case "$arg" in
    config|up|run|down)
      subcommand="$arg"
      ;;
  esac
done

case "$subcommand" in
  config)
    printf '%s\n' integration-runner fake-dependency
    ;;
  up|down)
    ;;
  run)
    printf '%s\n' runner-ok
    ;;
  *)
    printf 'unexpected docker invocation: %s\n' "$*" >&2
    exit 1
    ;;
esac
`), 0o755)
	if err != nil {
		t.Fatalf("WriteFile(docker stub) error = %v", err)
	}

	cmd := exec.Command(bashPath, filepath.Join(repoRoot, "scripts", "run-integration-tests.sh"), suiteName)
	cmd.Dir = repoRoot
	cacheRoot := t.TempDir()
	cmd.Env = replaceIntegrationTestEnvironment(os.Environ(), map[string]string{
		"PATH":                                  stubDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"DOCKER_LOG":                            logPath,
		"INTEGRATION_COVERPKG":                  "github.com/sipeed/picoclaw/pkg/mcp",
		"INTEGRATION_COVERPROFILE_DIR":          "/workspace/.coverage/runner-test",
		"INTEGRATION_COMPOSE_PROJECT_NAMESPACE": "pc-test-base",
		"INTEGRATION_GOMAXPROCS":                "2",
		"INTEGRATION_GOCACHE":                   filepath.Join(cacheRoot, "build"),
		"INTEGRATION_GOMODCACHE":                filepath.Join(cacheRoot, "modules"),
		"INTEGRATION_RUNNER_UID":                "12345",
		"INTEGRATION_RUNNER_GID":                "23456",
	})

	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run-integration-tests.sh error = %v\noutput:\n%s", err, output)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(logPath) error = %v", err)
	}
	assertLoggedComposeProject(t, string(logData), "picoclaw-int-pc-test-base-"+suiteName)
	namespacedDownArgs := findLoggedDockerInvocation(t, string(logData), "down")
	for _, wanted := range []string{"--rmi", "local"} {
		if !containsArg(namespacedDownArgs, wanted) {
			t.Errorf("namespaced cleanup is missing %q:\n%v", wanted, namespacedDownArgs)
		}
	}
	runArgs := findLoggedDockerInvocation(t, string(logData), "run")
	joinedArgs := strings.Join(runArgs, "\n")
	for _, want := range []string{
		"INTEGRATION_COVERPKG=github.com/sipeed/picoclaw/pkg/mcp",
		"INTEGRATION_COVERPROFILE=/workspace/.coverage/runner-test/" + suiteName + ".cover.out",
		"GOMAXPROCS=2",
		"-count=1",
		"-coverprofile=\"$INTEGRATION_COVERPROFILE\"",
		"go test ./pkg/mcp -run TestIntegration -v",
	} {
		if !strings.Contains(joinedArgs, want) {
			t.Fatalf("docker run args missing %q:\n%s", want, joinedArgs)
		}
	}
	if _, err := os.Stat(filepath.Join(repoRoot, ".coverage", "runner-test")); err != nil {
		t.Fatalf("coverage dir was not created: %v", err)
	}
}

func TestRunIntegrationTestsScriptExecutesHostSuiteWithoutDocker(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("host integration runner is exercised by POSIX CI")
	}
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}

	repoRoot := repoRootFromTestFile(t)
	suiteDir, err := os.MkdirTemp(filepath.Join(repoRoot, "integration", "suites"), "runner-host-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(suiteDir) })
	if err = os.WriteFile(
		filepath.Join(suiteDir, "suite.env"),
		[]byte("RUNNER_MODE=host\nTEST_COMMAND='go test ./pkg/sample -v'\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	stubDir := t.TempDir()
	dockerMarker := filepath.Join(t.TempDir(), "docker-called")
	goLog := filepath.Join(t.TempDir(), "go.log")
	writeExecutable(t, filepath.Join(stubDir, "docker"), `#!/bin/sh
set -eu
: >"${DOCKER_MARKER:?}"
exit 97
`)
	writeExecutable(t, filepath.Join(stubDir, "go"), `#!/bin/sh
set -eu
printf 'pwd=%s\n' "$PWD" >"${GO_LOG:?}"
printf 'args=%s\n' "$*" >>"${GO_LOG:?}"
printf 'gocache=%s\n' "${GOCACHE:?}" >>"${GO_LOG:?}"
printf 'gomodcache=%s\n' "${GOMODCACHE:?}" >>"${GO_LOG:?}"
printf 'gotoolchain=%s\n' "${GOTOOLCHAIN:?}" >>"${GO_LOG:?}"
printf 'goflags=%s\n' "${GOFLAGS:?}" >>"${GO_LOG:?}"
printf 'cgo=%s\n' "${CGO_ENABLED:?}" >>"${GO_LOG:?}"
`)
	cacheRoot := t.TempDir()
	buildCache := filepath.Join(cacheRoot, "build")
	moduleCache := filepath.Join(cacheRoot, "modules")
	command := exec.Command(
		bashPath,
		filepath.Join(repoRoot, "scripts", "run-integration-tests.sh"),
		filepath.Base(suiteDir),
	)
	command.Dir = repoRoot
	command.Env = replaceIntegrationTestEnvironment(os.Environ(), map[string]string{
		"PATH":                   stubDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"DOCKER_MARKER":          dockerMarker,
		"GO_LOG":                 goLog,
		"INTEGRATION_GOCACHE":    buildCache,
		"INTEGRATION_GOMODCACHE": moduleCache,
	})
	if output, runErr := command.CombinedOutput(); runErr != nil {
		t.Fatalf("host runner error = %v\n%s", runErr, output)
	}
	if _, statErr := os.Stat(dockerMarker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("host suite invoked Docker: %v", statErr)
	}
	logData, err := os.ReadFile(goLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"pwd=" + repoRoot,
		"args=test -buildvcs=false -count=1 ./pkg/sample -v",
		"gocache=" + buildCache,
		"gomodcache=" + moduleCache,
		"gotoolchain=auto",
		"goflags=-tags=goolm,stdjson,integration",
		"cgo=0",
	} {
		if !strings.Contains(string(logData), want+"\n") {
			t.Fatalf("host Go invocation is missing %q:\n%s", want, logData)
		}
	}
}

func TestRunIntegrationTestsScriptHostCoverageUsesTranslatedPath(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("host integration runner is exercised by POSIX CI")
	}
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	repoRoot := repoRootFromTestFile(t)
	suiteDir, err := os.MkdirTemp(filepath.Join(repoRoot, "integration", "suites"), "runner-host-cover-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(suiteDir) })
	if err = os.WriteFile(
		filepath.Join(suiteDir, "suite.env"),
		[]byte("RUNNER_MODE=host\nTEST_COMMAND='go test ./pkg/sample'\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	stubDir := t.TempDir()
	goLog := filepath.Join(t.TempDir(), "go.log")
	writeExecutable(t, filepath.Join(stubDir, "go"), `#!/bin/sh
set -eu
printf '%s\n' "$*" >"${GO_LOG:?}"
case "$*" in
  *"-coverprofile="*) ;;
  *) exit 98 ;;
esac
`)
	cacheRoot := t.TempDir()
	command := exec.Command(
		bashPath,
		filepath.Join(repoRoot, "scripts", "run-integration-tests.sh"),
		filepath.Base(suiteDir),
	)
	command.Dir = repoRoot
	command.Env = replaceIntegrationTestEnvironment(os.Environ(), map[string]string{
		"PATH":                         stubDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"GO_LOG":                       goLog,
		"INTEGRATION_GOCACHE":          filepath.Join(cacheRoot, "build"),
		"INTEGRATION_GOMODCACHE":       filepath.Join(cacheRoot, "modules"),
		"INTEGRATION_COVERPKG":         "example.com/sample",
		"INTEGRATION_COVERPROFILE_DIR": "/workspace/.coverage/runner-test",
		"INTEGRATION_GOMAXPROCS":       "2",
	})
	if output, runErr := command.CombinedOutput(); runErr != nil {
		t.Fatalf("host coverage runner error = %v\n%s", runErr, output)
	}
	logData, err := os.ReadFile(goLog)
	if err != nil {
		t.Fatal(err)
	}
	wantProfile := filepath.Join(repoRoot, ".coverage", "runner-test", filepath.Base(suiteDir)+".cover.out")
	for _, want := range []string{
		"-count=1",
		"-covermode=atomic",
		"-coverpkg=example.com/sample",
		"-coverprofile=" + wantProfile,
	} {
		if !strings.Contains(string(logData), want) {
			t.Fatalf("host coverage invocation is missing %q: %s", want, logData)
		}
	}
	if info, statErr := os.Stat(filepath.Dir(wantProfile)); statErr != nil || !info.IsDir() {
		t.Fatalf("host coverage directory = %v, error = %v", info, statErr)
	}
}

func TestRunIntegrationTestsScriptRejectsInvalidHostManifest(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("host integration runner is exercised by POSIX CI")
	}
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	repoRoot := repoRootFromTestFile(t)

	for _, manifest := range []string{
		"RUNNER_MODE=invalid\nTEST_COMMAND='exit 99'\n",
		"RUNNER_MODE=host\nRUNNER_SERVICE=custom\nTEST_COMMAND='exit 99'\n",
	} {
		suiteDir, makeErr := os.MkdirTemp(filepath.Join(repoRoot, "integration", "suites"), "runner-invalid-")
		if makeErr != nil {
			t.Fatal(makeErr)
		}
		t.Cleanup(func() { _ = os.RemoveAll(suiteDir) })
		if writeErr := os.WriteFile(filepath.Join(suiteDir, "suite.env"), []byte(manifest), 0o644); writeErr != nil {
			t.Fatal(writeErr)
		}
		stubDir := t.TempDir()
		dockerMarker := filepath.Join(t.TempDir(), "docker-called")
		writeExecutable(t, filepath.Join(stubDir, "docker"), `#!/bin/sh
set -eu
: >"${DOCKER_MARKER:?}"
exit 97
`)
		cacheRoot := t.TempDir()
		command := exec.Command(
			bashPath,
			filepath.Join(repoRoot, "scripts", "run-integration-tests.sh"),
			filepath.Base(suiteDir),
		)
		command.Dir = repoRoot
		command.Env = replaceIntegrationTestEnvironment(os.Environ(), map[string]string{
			"PATH":                   stubDir + string(os.PathListSeparator) + os.Getenv("PATH"),
			"DOCKER_MARKER":          dockerMarker,
			"INTEGRATION_GOCACHE":    filepath.Join(cacheRoot, "build"),
			"INTEGRATION_GOMODCACHE": filepath.Join(cacheRoot, "modules"),
		})
		if output, runErr := command.CombinedOutput(); runErr == nil ||
			!strings.Contains(string(output), "suite ") {
			t.Fatalf("invalid manifest error = %v, output=%q", runErr, output)
		}
		if _, statErr := os.Stat(dockerMarker); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("invalid host manifest invoked Docker: %v", statErr)
		}
	}
}

func TestCurrentIntegrationSuitesRunOnHostWithoutComposeFiles(t *testing.T) {
	repoRoot := repoRootFromTestFile(t)
	for suite, snippets := range map[string][]string{
		"mcp-streamable": {
			"RUNNER_MODE=host",
			"TEST_COMMAND='bash ./integration/suites/mcp-streamable/run.sh'",
		},
		"storage-json": {
			"RUNNER_MODE=host",
			"PICOCLAW_STORAGE_JSON_ALLOWLIST_SUITE=1",
			"TestIntegrationRuntimeOwnedJSON",
		},
	} {
		manifestPath := filepath.Join(repoRoot, "integration", "suites", suite, "suite.env")
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		for _, snippet := range snippets {
			if !strings.Contains(string(data), snippet) {
				t.Errorf("%s is missing %q", manifestPath, snippet)
			}
		}
		composeFiles, err := filepath.Glob(filepath.Join(filepath.Dir(manifestPath), "docker-compose*.yml"))
		if err != nil {
			t.Fatal(err)
		}
		if len(composeFiles) != 0 {
			t.Errorf("host suite %s still has Compose files: %v", suite, composeFiles)
		}
	}
	for _, obsolete := range []string{
		filepath.Join(repoRoot, "integration", "fixtures", "mcp-streamable-server", "Dockerfile"),
	} {
		if _, err := os.Stat(obsolete); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("obsolete Docker fixture remains: %s", obsolete)
		}
	}
}

func TestRunIntegrationTestsScriptExportsReusableHostCaches(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("integration runner script is exercised by POSIX CI")
	}
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}

	repoRoot := repoRootFromTestFile(t)
	suitesRoot := filepath.Join(repoRoot, "integration", "suites")
	suiteDir, err := os.MkdirTemp(suitesRoot, "runner-cache-")
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
	dockerLog := filepath.Join(t.TempDir(), "docker.log")
	environmentLog := filepath.Join(t.TempDir(), "environment.log")
	writeExecutable(t, filepath.Join(stubDir, "id"), `#!/bin/sh
set -eu
case "${1:-}" in
  -u) printf 12345 ;;
  -g) printf 23456 ;;
  *) exit 2 ;;
esac
`)
	writeExecutable(t, filepath.Join(stubDir, "docker"), `#!/bin/sh
set -eu
case "$*" in
  "info --format {{json .SecurityOptions}} {{.OperatingSystem}} {{.Name}}")
    printf '%s\n' "${DOCKER_ENGINE_DETAILS:-[] linux test-engine}"
    exit 0
    ;;
esac
printf '%s|%s|%s|%s\n' \
  "${INTEGRATION_GOCACHE:?}" "${INTEGRATION_GOMODCACHE:?}" \
  "${INTEGRATION_RUNNER_UID:?}" "${INTEGRATION_RUNNER_GID:?}" \
  >>"${DOCKER_ENVIRONMENT_LOG:?}"
printf '%s\n' "$*" >>"${DOCKER_LOG:?}"
case "$*" in
  *" config --services") printf '%s\n' integration-runner ;;
  *" run --rm "*) printf '%s\n' runner-ok ;;
  *" down -v --remove-orphans"*) ;;
  *) printf 'unexpected docker invocation: %s\n' "$*" >&2; exit 1 ;;
esac
`)

	cacheRoot := filepath.Join(t.TempDir(), "cache root")
	buildCache := filepath.Join(cacheRoot, "build")
	moduleCache := filepath.Join(cacheRoot, "modules")
	command := exec.Command(
		bashPath,
		filepath.Join(repoRoot, "scripts", "run-integration-tests.sh"),
		filepath.Base(suiteDir),
	)
	command.Dir = repoRoot
	command.Env = replaceIntegrationTestEnvironment(os.Environ(), map[string]string{
		"PATH":                   stubDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"DOCKER_LOG":             dockerLog,
		"DOCKER_ENVIRONMENT_LOG": environmentLog,
		"GOCACHE":                filepath.Join(t.TempDir(), "ambient-build-cache"),
		"GOMODCACHE":             filepath.Join(t.TempDir(), "ambient-module-cache"),
		"INTEGRATION_GOCACHE":    buildCache,
		"INTEGRATION_GOMODCACHE": moduleCache,
		"INTEGRATION_RUNNER_UID": "",
		"INTEGRATION_RUNNER_GID": "",
	})
	if output, runErr := command.CombinedOutput(); runErr != nil {
		t.Fatalf("runner error = %v\n%s", runErr, output)
	}
	for _, path := range []string{buildCache, moduleCache} {
		if info, statErr := os.Stat(path); statErr != nil || !info.IsDir() {
			t.Fatalf("cache directory %q was not created: info=%v error=%v", path, info, statErr)
		}
	}
	environmentData, err := os.ReadFile(environmentLog)
	if err != nil {
		t.Fatal(err)
	}
	wantLine := strings.Join([]string{buildCache, moduleCache, "12345", "23456"}, "|")
	assertIntegrationDockerEnvironments(t, environmentData, wantLine)

	if err = os.WriteFile(environmentLog, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	defaultCommand := exec.Command(
		bashPath,
		filepath.Join(repoRoot, "scripts", "run-integration-tests.sh"),
		filepath.Base(suiteDir),
	)
	defaultCommand.Dir = repoRoot
	defaultCommand.Env = replaceIntegrationTestEnvironment(os.Environ(), map[string]string{
		"PATH":                   stubDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"DOCKER_LOG":             dockerLog,
		"DOCKER_ENVIRONMENT_LOG": environmentLog,
		"GOCACHE":                filepath.Join(t.TempDir(), "ambient-build-cache"),
		"GOMODCACHE":             filepath.Join(t.TempDir(), "ambient-module-cache"),
		"INTEGRATION_GOCACHE":    "",
		"INTEGRATION_GOMODCACHE": "",
		"INTEGRATION_RUNNER_UID": "12345",
		"INTEGRATION_RUNNER_GID": "23456",
	})
	if output, runErr := defaultCommand.CombinedOutput(); runErr != nil {
		t.Fatalf("runner with default caches error = %v\n%s", runErr, output)
	}
	defaultData, err := os.ReadFile(environmentLog)
	if err != nil {
		t.Fatal(err)
	}
	wantDefaultLine := strings.Join([]string{
		filepath.Join(repoRoot, ".cache", "go-build"),
		filepath.Join(repoRoot, ".cache", "go-mod"),
		"12345",
		"23456",
	}, "|")
	assertIntegrationDockerEnvironments(t, defaultData, wantDefaultLine)

	for _, engine := range []struct {
		name    string
		details string
	}{
		{name: "rootless", details: `["name=rootless"] linux test-engine`},
		{name: "Docker Desktop", details: `[] Docker Desktop docker-desktop`},
	} {
		t.Run(engine.name, func(t *testing.T) {
			if writeErr := os.WriteFile(environmentLog, nil, 0o600); writeErr != nil {
				t.Fatal(writeErr)
			}
			mappedCommand := exec.Command(
				bashPath,
				filepath.Join(repoRoot, "scripts", "run-integration-tests.sh"),
				filepath.Base(suiteDir),
			)
			mappedCommand.Dir = repoRoot
			mappedCommand.Env = replaceIntegrationTestEnvironment(os.Environ(), map[string]string{
				"PATH":                   stubDir + string(os.PathListSeparator) + os.Getenv("PATH"),
				"DOCKER_LOG":             dockerLog,
				"DOCKER_ENVIRONMENT_LOG": environmentLog,
				"DOCKER_ENGINE_DETAILS":  engine.details,
				"INTEGRATION_GOCACHE":    buildCache,
				"INTEGRATION_GOMODCACHE": moduleCache,
				"INTEGRATION_RUNNER_UID": "",
				"INTEGRATION_RUNNER_GID": "",
			})
			if output, runErr := mappedCommand.CombinedOutput(); runErr != nil {
				t.Fatalf("runner with %s engine error = %v\n%s", engine.name, runErr, output)
			}
			mappedData, readErr := os.ReadFile(environmentLog)
			if readErr != nil {
				t.Fatal(readErr)
			}
			wantMappedLine := strings.Join([]string{buildCache, moduleCache, "0", "0"}, "|")
			assertIntegrationDockerEnvironments(t, mappedData, wantMappedLine)
		})
	}
}

func assertIntegrationDockerEnvironments(t *testing.T, data []byte, want string) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 {
		t.Fatalf("Docker environment captures = %d, want config/run/down:\n%s", len(lines), data)
	}
	for _, line := range lines {
		if line != want {
			t.Fatalf("Docker cache environment = %q, want %q", line, want)
		}
	}
}

func TestRunIntegrationTestsScriptRejectsUnsafeCachePathsBeforeDocker(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("integration runner script is exercised by POSIX CI")
	}
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	repoRoot := repoRootFromTestFile(t)
	regularFile := filepath.Join(t.TempDir(), "cache-file")
	if err = os.WriteFile(regularFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	overlapRoot := t.TempDir()

	for _, test := range []struct {
		name        string
		buildCache  string
		moduleCache string
		want        string
	}{
		{name: "relative build cache", buildCache: "relative", moduleCache: t.TempDir(), want: "INTEGRATION_GOCACHE must be an absolute path"},
		{name: "disabled build cache", buildCache: "off", moduleCache: t.TempDir(), want: "INTEGRATION_GOCACHE must be an absolute path"},
		{name: "relative module cache", buildCache: t.TempDir(), moduleCache: "relative", want: "INTEGRATION_GOMODCACHE must be an absolute path"},
		{name: "cache regular file", buildCache: regularFile, moduleCache: t.TempDir(), want: "INTEGRATION_GOCACHE is not a directory"},
		{name: "identical caches", buildCache: overlapRoot, moduleCache: overlapRoot, want: "caches must not overlap"},
		{name: "nested caches", buildCache: overlapRoot, moduleCache: filepath.Join(overlapRoot, "modules"), want: "caches must not overlap"},
	} {
		t.Run(test.name, func(t *testing.T) {
			stubDir := t.TempDir()
			dockerMarker := filepath.Join(t.TempDir(), "docker-called")
			writeExecutable(t, filepath.Join(stubDir, "docker"), `#!/bin/sh
set -eu
: >"${DOCKER_MARKER:?}"
exit 99
`)
			command := exec.Command(
				bashPath,
				filepath.Join(repoRoot, "scripts", "run-integration-tests.sh"),
				"storage-json",
			)
			command.Dir = repoRoot
			command.Env = replaceIntegrationTestEnvironment(os.Environ(), map[string]string{
				"PATH":                   stubDir + string(os.PathListSeparator) + os.Getenv("PATH"),
				"DOCKER_MARKER":          dockerMarker,
				"INTEGRATION_GOCACHE":    test.buildCache,
				"INTEGRATION_GOMODCACHE": test.moduleCache,
			})
			output, runErr := command.CombinedOutput()
			if runErr == nil || !strings.Contains(string(output), test.want) {
				t.Fatalf("runner error = %v, output=%q; want %q", runErr, output, test.want)
			}
			if _, statErr := os.Stat(dockerMarker); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("Docker ran before cache validation: %v", statErr)
			}
		})
	}
}

func TestRunIntegrationTestsScriptRejectsSuiteCacheOverride(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("integration runner script is exercised by POSIX CI")
	}
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	repoRoot := repoRootFromTestFile(t)
	suiteDir, err := os.MkdirTemp(filepath.Join(repoRoot, "integration", "suites"), "runner-override-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(suiteDir) })
	if err = os.WriteFile(
		filepath.Join(suiteDir, "suite.env"),
		[]byte("TEST_COMMAND='printf runner-ok'\nINTEGRATION_GOCACHE=/tmp/suite-override\n"),
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
	dockerMarker := filepath.Join(t.TempDir(), "docker-called")
	writeExecutable(t, filepath.Join(stubDir, "docker"), `#!/bin/sh
set -eu
: >"${DOCKER_MARKER:?}"
exit 99
`)
	cacheRoot := t.TempDir()
	command := exec.Command(
		bashPath,
		filepath.Join(repoRoot, "scripts", "run-integration-tests.sh"),
		filepath.Base(suiteDir),
	)
	command.Dir = repoRoot
	command.Env = replaceIntegrationTestEnvironment(os.Environ(), map[string]string{
		"PATH":                   stubDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"DOCKER_MARKER":          dockerMarker,
		"INTEGRATION_GOCACHE":    filepath.Join(cacheRoot, "build"),
		"INTEGRATION_GOMODCACHE": filepath.Join(cacheRoot, "modules"),
		"INTEGRATION_RUNNER_UID": "12345",
		"INTEGRATION_RUNNER_GID": "23456",
	})
	output, runErr := command.CombinedOutput()
	if runErr == nil || !strings.Contains(string(output), "INTEGRATION_GOCACHE: readonly variable") {
		t.Fatalf("suite cache override error = %v, output=%q", runErr, output)
	}
	if _, statErr := os.Stat(dockerMarker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Docker ran after suite cache override: %v", statErr)
	}
}

func TestRunIntegrationTestsScriptRejectsCachedTestCountOverrides(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("integration runner script is exercised by POSIX CI")
	}
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	repoRoot := repoRootFromTestFile(t)

	for _, arguments := range []string{
		"-count=2",
		"--count=2",
		"-test.count=2",
		"--test.count=2",
		"-count 2",
		"-test.count 2",
	} {
		t.Run(strings.ReplaceAll(arguments, " ", "_"), func(t *testing.T) {
			suiteDir, makeErr := os.MkdirTemp(
				filepath.Join(repoRoot, "integration", "suites"),
				"runner-count-",
			)
			if makeErr != nil {
				t.Fatal(makeErr)
			}
			t.Cleanup(func() { _ = os.RemoveAll(suiteDir) })
			if writeErr := os.WriteFile(
				filepath.Join(suiteDir, "suite.env"),
				[]byte("TEST_COMMAND='go test ./pkg/mcp "+arguments+"'\n"),
				0o644,
			); writeErr != nil {
				t.Fatal(writeErr)
			}
			if writeErr := os.WriteFile(
				filepath.Join(suiteDir, "docker-compose.yml"),
				[]byte("services:\n  integration-runner: {}\n"),
				0o644,
			); writeErr != nil {
				t.Fatal(writeErr)
			}

			stubDir := t.TempDir()
			goMarker := filepath.Join(t.TempDir(), "go-called")
			writeExecutable(t, filepath.Join(stubDir, "go"), `#!/bin/sh
set -eu
: >"${GO_MARKER:?}"
`)
			writeExecutable(t, filepath.Join(stubDir, "docker"), `#!/bin/sh
set -eu
subcommand=""
last_argument=""
for argument in "$@"; do
  last_argument="$argument"
  case "$argument" in
    config|up|run|down) subcommand="$argument" ;;
  esac
done
case "$subcommand" in
  config) printf '%s\n' integration-runner ;;
  run) bash -c "$last_argument" ;;
  down) ;;
  *) exit 99 ;;
esac
`)
			cacheRoot := t.TempDir()
			command := exec.Command(
				bashPath,
				filepath.Join(repoRoot, "scripts", "run-integration-tests.sh"),
				filepath.Base(suiteDir),
			)
			command.Dir = repoRoot
			command.Env = replaceIntegrationTestEnvironment(os.Environ(), map[string]string{
				"PATH":                   stubDir + string(os.PathListSeparator) + os.Getenv("PATH"),
				"GO_MARKER":              goMarker,
				"INTEGRATION_GOCACHE":    filepath.Join(cacheRoot, "build"),
				"INTEGRATION_GOMODCACHE": filepath.Join(cacheRoot, "modules"),
				"INTEGRATION_RUNNER_UID": "12345",
				"INTEGRATION_RUNNER_GID": "23456",
			})
			output, runErr := command.CombinedOutput()
			if runErr == nil || !strings.Contains(
				string(output),
				"integration suites cannot override go test -count=1",
			) {
				t.Fatalf("count override error = %v, output=%q", runErr, output)
			}
			if _, statErr := os.Stat(goMarker); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("go ran despite count override: %v", statErr)
			}
		})
	}
}

func TestIntegrationRunnerComposeUsesHostCacheBindMounts(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRootFromTestFile(t), "integration", "docker-compose.runner.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Services map[string]struct {
			Image      string `yaml:"image"`
			User       string `yaml:"user"`
			WorkingDir string `yaml:"working_dir"`
			Volumes    []struct {
				Type   string `yaml:"type"`
				Source string `yaml:"source"`
				Target string `yaml:"target"`
				Bind   struct {
					CreateHostPath *bool `yaml:"create_host_path"`
				} `yaml:"bind"`
			} `yaml:"volumes"`
		} `yaml:"services"`
		Volumes map[string]any `yaml:"volumes"`
	}
	if err = yaml.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	runner, ok := document.Services["integration-runner"]
	if !ok {
		t.Fatal("integration-runner service is missing")
	}
	if runner.User != "${INTEGRATION_RUNNER_UID}:${INTEGRATION_RUNNER_GID}" {
		t.Fatalf("runner user = %q", runner.User)
	}
	goMod, err := os.ReadFile(filepath.Join(repoRootFromTestFile(t), "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	goVersion := ""
	for _, line := range strings.Split(string(goMod), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "go" {
			goVersion = fields[1]
			break
		}
	}
	if wantImage := "golang:" + goVersion + "-bookworm"; goVersion == "" || runner.Image != wantImage {
		t.Fatalf("runner image = %q, want %q from go.mod", runner.Image, wantImage)
	}
	if runner.WorkingDir != "${INTEGRATION_REPO_ROOT}" {
		t.Fatalf("runner working directory = %q", runner.WorkingDir)
	}
	wantMounts := map[string]string{
		"${INTEGRATION_REPO_ROOT}":  "${INTEGRATION_REPO_ROOT}",
		"/workspace":                "${INTEGRATION_REPO_ROOT}",
		"${INTEGRATION_GOCACHE}":    "${INTEGRATION_GOCACHE}",
		"${INTEGRATION_GOMODCACHE}": "${INTEGRATION_GOMODCACHE}",
	}
	for _, mount := range runner.Volumes {
		if wantSource, wanted := wantMounts[mount.Target]; wanted {
			if mount.Type != "bind" || mount.Source != wantSource ||
				mount.Bind.CreateHostPath == nil || *mount.Bind.CreateHostPath {
				t.Fatalf("cache mount %q = %#v, want bind source %q", mount.Target, mount, wantSource)
			}
			delete(wantMounts, mount.Target)
		}
	}
	if len(wantMounts) != 0 {
		t.Fatalf("missing cache mounts: %v", wantMounts)
	}
	if len(document.Volumes) != 0 || strings.Contains(string(raw), "picoclaw-integration-gocache") ||
		strings.Contains(string(raw), "picoclaw-integration-gomodcache") {
		t.Fatal("integration runner still declares project-local cache volumes")
	}
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
}

func replaceIntegrationTestEnvironment(base []string, replacements map[string]string) []string {
	result := make([]string, 0, len(base)+len(replacements))
	for _, entry := range base {
		name, _, ok := strings.Cut(entry, "=")
		if _, replaced := replacements[name]; ok && replaced {
			continue
		}
		result = append(result, entry)
	}
	for name, value := range replacements {
		result = append(result, name+"="+value)
	}
	return result
}

func assertLoggedComposeProject(t *testing.T, logData, wanted string) {
	t.Helper()
	invocations := 0
	for _, block := range strings.Split(logData, "---\n") {
		args := strings.Split(strings.TrimSpace(block), "\n")
		if len(args) == 1 && args[0] == "" {
			continue
		}
		invocations++
		found := false
		for index := 0; index+1 < len(args); index++ {
			if args[index] == "-p" && args[index+1] == wanted {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Docker invocation does not use project %q:\n%v", wanted, args)
		}
	}
	if invocations == 0 {
		t.Fatal("no Docker invocations logged")
	}
}

func repoRootFromTestFile(t *testing.T) string {
	t.Helper()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	return filepath.Dir(wd)
}

func findLoggedDockerInvocation(t *testing.T, logData, subcommand string) []string {
	t.Helper()

	for _, block := range strings.Split(logData, "---\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		args := strings.Split(block, "\n")
		if containsArg(args, subcommand) {
			return args
		}
	}

	t.Fatalf("did not find docker %q invocation in log:\n%s", subcommand, logData)
	return nil
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
