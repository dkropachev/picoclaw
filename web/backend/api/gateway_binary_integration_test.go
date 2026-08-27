package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	ppid "github.com/sipeed/picoclaw/pkg/pid"
)

var apiTestCoreBinary = struct {
	sync.Once
	path   string
	output string
	err    error
}{}

type synchronizedGatewayOutput struct {
	mu sync.Mutex
	bytes.Buffer
}

func (o *synchronizedGatewayOutput) Write(data []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.Buffer.Write(data)
}

func (o *synchronizedGatewayOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.Buffer.String()
}

func builtAPITestCoreBinary(t *testing.T) string {
	t.Helper()
	if apiSuiteTestRuntime == nil {
		t.Fatal("API suite test runtime is not initialized")
	}

	apiTestCoreBinary.Do(func() {
		repositoryRoot, err := findAPITestRepositoryRoot()
		if err != nil {
			apiTestCoreBinary.err = err
			return
		}
		binaryName := "picoclaw"
		if runtime.GOOS == "windows" {
			binaryName += ".exe"
		}
		apiTestCoreBinary.path = filepath.Join(apiSuiteTestRuntime.Root, "bin", binaryName)
		command := exec.Command(
			"go",
			"build",
			"-buildvcs=false",
			"-tags=goolm,stdjson",
			"-o",
			apiTestCoreBinary.path,
			"./cmd/picoclaw",
		)
		command.Dir = repositoryRoot
		command.Env = replaceAPITestEnvironment(os.Environ(), map[string]string{
			"CGO_ENABLED": "0",
			"GOCACHE":     filepath.Join(repositoryRoot, ".cache", "go-build"),
			"GOMODCACHE":  filepath.Join(repositoryRoot, ".cache", "go-mod"),
			"GOTOOLCHAIN": "auto",
		})
		output, buildErr := command.CombinedOutput()
		apiTestCoreBinary.output = string(output)
		if buildErr != nil {
			apiTestCoreBinary.err = fmt.Errorf("build test-owned picoclaw binary: %w", buildErr)
		}
	})
	if apiTestCoreBinary.err != nil {
		t.Fatalf("%v\n%s", apiTestCoreBinary.err, apiTestCoreBinary.output)
	}
	return apiTestCoreBinary.path
}

func findAPITestRepositoryRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if info, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil && !info.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("cannot find repository root containing go.mod")
		}
		dir = parent
	}
}

func replaceAPITestEnvironment(base []string, replacements map[string]string) []string {
	values := make(map[string]string, len(base)+len(replacements))
	for _, entry := range base {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	for key, value := range replacements {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	environment := make([]string, 0, len(keys))
	for _, key := range keys {
		environment = append(environment, key+"="+values[key])
	}
	return environment
}

func isolatedAPITestCommandEnvironment(runtimeRoot *gatewayRuntimeHarness) []string {
	values := map[string]string{
		"HOME":                  runtimeRoot.OSHome,
		"USERPROFILE":           runtimeRoot.OSHome,
		"XDG_CONFIG_HOME":       filepath.Join(runtimeRoot.OSHome, ".config"),
		"XDG_DATA_HOME":         filepath.Join(runtimeRoot.OSHome, ".local", "share"),
		"XDG_CACHE_HOME":        filepath.Join(runtimeRoot.OSHome, ".cache"),
		"XDG_STATE_HOME":        filepath.Join(runtimeRoot.OSHome, ".local", "state"),
		"XDG_RUNTIME_DIR":       filepath.Join(runtimeRoot.OSHome, ".run"),
		"CODEX_HOME":            filepath.Join(runtimeRoot.OSHome, ".codex"),
		"CLAUDE_CONFIG_DIR":     filepath.Join(runtimeRoot.OSHome, ".claude"),
		"OPENCLAW_HOME":         filepath.Join(runtimeRoot.OSHome, ".openclaw"),
		"GNUPGHOME":             filepath.Join(runtimeRoot.OSHome, ".gnupg"),
		"GIT_CONFIG_GLOBAL":     filepath.Join(runtimeRoot.OSHome, ".config", "git", "config"),
		"GIT_CONFIG_NOSYSTEM":   "1",
		"APPDATA":               filepath.Join(runtimeRoot.OSHome, "AppData", "Roaming"),
		"LOCALAPPDATA":          filepath.Join(runtimeRoot.OSHome, "AppData", "Local"),
		"TMPDIR":                filepath.Join(runtimeRoot.Root, "tmp"),
		"TEMP":                  filepath.Join(runtimeRoot.Root, "tmp"),
		"TMP":                   filepath.Join(runtimeRoot.Root, "tmp"),
		"NO_COLOR":              "1",
		config.EnvHome:          runtimeRoot.PicoHome,
		config.EnvConfig:        runtimeRoot.Config,
		config.EnvBinary:        runtimeRoot.Binary,
		config.EnvBuiltinSkills: filepath.Join(runtimeRoot.Root, "builtin-skills"),
		config.EnvGatewayHost:   "127.0.0.1",
	}
	for _, key := range []string{
		"PATH",
		"PATHEXT",
		"SYSTEMDRIVE",
		"SYSTEMROOT",
		"WINDIR",
		"COMSPEC",
	} {
		if value, ok := os.LookupEnv(key); ok {
			values[key] = value
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	environment := make([]string, 0, len(keys))
	for _, key := range keys {
		environment = append(environment, key+"="+values[key])
	}
	return environment
}

func reserveAPITestLoopbackPort(t *testing.T) int {
	t.Helper()
	port, err := allocateAPITestLoopbackPort()
	if err != nil {
		t.Fatalf("reserve gateway test port: %v", err)
	}
	return port
}

func TestGatewayDirectBinaryUsesOnlyTestRuntime(t *testing.T) {
	if testing.Short() {
		t.Skip("direct gateway binary integration test")
	}

	port := reserveAPITestLoopbackPort(t)
	harness := newGatewayRuntimeHarness(t, func(cfg *config.Config) {
		cfg.Gateway.Host = "127.0.0.1"
		cfg.Gateway.Port = port
		cfg.Workflows.Enabled = false
		cfg.Events.Ingress.Enabled = true
	})
	harness.Binary = builtAPITestCoreBinary(t)
	harness.assertOwnedPath(t, harness.Config)
	harness.assertOwnedPath(t, harness.Workspace)
	harness.assertOwnedPath(t, harness.EventDB)
	if err := os.MkdirAll(filepath.Join(harness.Root, "tmp"), 0o700); err != nil {
		t.Fatalf("create gateway test temp directory: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(harness.Root, "builtin-skills"), 0o700); err != nil {
		t.Fatalf("create gateway test built-in skills directory: %v", err)
	}

	command := exec.Command(harness.Binary, "gateway", "-E")
	command.Dir = harness.Root
	command.Env = isolatedAPITestCommandEnvironment(harness)
	var output synchronizedGatewayOutput
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatalf("start test-owned gateway binary: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
	}()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		if runtime.GOOS == "windows" {
			_ = command.Process.Kill()
		} else {
			_ = command.Process.Signal(os.Interrupt)
		}
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = command.Process.Kill()
			<-done
		}
	}
	t.Cleanup(stop)

	healthURL := "http://127.0.0.1:" + strconv.Itoa(port) + "/health"
	client := &http.Client{
		Timeout: 500 * time.Millisecond,
		Transport: &http.Transport{
			Proxy: nil,
		},
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		select {
		case waitErr := <-done:
			stopped = true
			t.Fatalf("test-owned gateway exited before health check: %v\n%s", waitErr, output.String())
		default:
		}
		response, requestErr := client.Get(healthURL)
		if requestErr == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("test-owned gateway did not become healthy\n%s", output.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	pidPath := filepath.Join(harness.PicoHome, ".picoclaw.pid")
	rawPID, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("read test-owned gateway PID file: %v\n%s", err, output.String())
	}
	var pidData ppid.PidFileData
	if err = json.Unmarshal(rawPID, &pidData); err != nil {
		t.Fatalf("decode test-owned gateway PID file: %v", err)
	}
	if pidData.PID != command.Process.Pid || pidData.Port != port {
		t.Fatalf(
			"gateway PID data = (pid=%d, port=%d), want (pid=%d, port=%d)",
			pidData.PID,
			pidData.Port,
			command.Process.Pid,
			port,
		)
	}
	harness.assertOwnedPath(t, pidPath)

	dbInfo, err := os.Stat(harness.EventDB)
	if err != nil {
		t.Fatalf("stat test-owned event database: %v", err)
	}
	if dbInfo.Size() == 0 {
		t.Fatal("gateway did not initialize test-owned event database")
	}

	stop()
	if runtime.GOOS != "windows" {
		if _, err = os.Stat(pidPath); err == nil {
			var remaining ppid.PidFileData
			remainingRaw, readErr := os.ReadFile(pidPath)
			decodeErr := json.Unmarshal(remainingRaw, &remaining)
			if readErr != nil || decodeErr != nil || remaining.PID != command.Process.Pid {
				t.Fatalf(
					"gateway left unexpected PID authority: pid=%d read=%v decode=%v",
					remaining.PID,
					readErr,
					decodeErr,
				)
			}
			if !ppid.RemovePidFileIfPID(harness.PicoHome, command.Process.Pid) {
				t.Fatalf("remove test-owned gateway PID file after forced shutdown")
			}
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat gateway PID file after stop: %v", err)
		}
		if _, err = os.Stat(pidPath); !os.IsNotExist(err) {
			t.Fatalf("test-owned gateway PID file remains after cleanup: %v", err)
		}
	}
}
