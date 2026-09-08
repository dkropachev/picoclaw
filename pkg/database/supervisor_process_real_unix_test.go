//go:build unix

//nolint:govet // Independent supervisor boundary assertions intentionally use narrow errors.
package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestMain(m *testing.M) {
	previousUmask := unix.Umask(0o022)
	exitCode := m.Run()
	unix.Umask(previousUmask)
	os.Exit(exitCode)
}

func TestStartSupervisorProcessUsesPathAndCleansFailedBootstrap(t *testing.T) {
	executableDir := t.TempDir()
	executableName := "database-supervisor-real-helper"
	executable := filepath.Join(executableDir, executableName)
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	t.Run("reject current test executable when none is configured", func(t *testing.T) {
		home := t.TempDir()
		if err := startSupervisorProcess(EnsureOptions{}, home); CodeOf(err) != CodeInvalid {
			t.Fatalf("current test executable error = %v", err)
		}
	})

	t.Run("resolve executable from PATH", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("PATH", executableDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		if err := startSupervisorProcess(EnsureOptions{Executable: executableName}, home); err != nil {
			t.Fatal(err)
		}
		if info, err := os.Stat(filepath.Join(home, "logs", "database-supervisor.log")); err != nil ||
			!info.Mode().IsRegular() {
			t.Fatalf("supervisor log = (%v, %v)", info, err)
		}
		removeTestBootstrapFiles(t, home)
	})

	t.Run("log path prevents process configuration", func(t *testing.T) {
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, "logs"), []byte("occupied"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := startSupervisorProcess(EnsureOptions{Executable: executable}, home)
		if err == nil || !strings.Contains(err.Error(), "supervisor log directory") {
			t.Fatalf("configure supervisor error = %v", err)
		}
		stateDir, stateErr := StateDirectory(home)
		if stateErr != nil {
			t.Fatal(stateErr)
		}
		entries, readErr := os.ReadDir(stateDir)
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".bootstrap-") {
				t.Fatalf("failed start retained bootstrap %q", entry.Name())
			}
		}
	})

	t.Run("state boundary prevents bootstrap creation", func(t *testing.T) {
		home := t.TempDir()
		if err := os.WriteFile(
			filepath.Join(home, StateDirectoryName),
			[]byte("not a directory"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		if err := startSupervisorProcess(EnsureOptions{Executable: executable}, home); CodeOf(err) != CodeIntegrity {
			t.Fatalf("unsafe state boundary start error = %v", err)
		}
	})
}

func TestReadyBrokerHelpersPropagateLiveProtocolFailures(t *testing.T) {
	home := t.TempDir()
	server, err := StartServer(context.Background(), ServerOptions{
		Home: home,
		StatusProvider: func(context.Context) ([]StoreStatus, error) {
			return []StoreStatus{{ID: "invalid store id", Readiness: StoreReady}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeServer(t, server)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if client, err := connectReadyBroker(canceled, home); err == nil || client != nil {
		t.Fatalf("canceled ready broker = (%p, %v)", client, err)
	}
	if client, status, err := connectReadyBrokerStatus(t.Context(), home); err == nil ||
		client != nil || status.Epoch != "" {
		t.Fatalf("invalid broker status = (%p, %#v, %v)", client, status, err)
	}
}

func TestShutdownBrokerForReplacementHandlesStaleAndCanceledClients(t *testing.T) {
	t.Run("stale broker is already replaceable", func(t *testing.T) {
		home := t.TempDir()
		server, err := StartServer(context.Background(), ServerOptions{Home: home})
		if err != nil {
			t.Fatal(err)
		}
		client, err := Connect(home)
		if err != nil {
			t.Fatal(err)
		}
		closeServer(t, server)
		if err := shutdownBrokerForReplacement(t.Context(), client); err != nil {
			t.Fatalf("stale replacement shutdown = %v", err)
		}
	})

	t.Run("canceled shutdown does not dispatch", func(t *testing.T) {
		home := t.TempDir()
		server, err := StartServer(context.Background(), ServerOptions{Home: home})
		if err != nil {
			t.Fatal(err)
		}
		defer closeServer(t, server)
		client, err := Connect(home)
		if err != nil {
			t.Fatal(err)
		}
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		if err := shutdownBrokerForReplacement(canceled, client); CodeOf(err) != CodeDeadline {
			t.Fatalf("canceled replacement shutdown = %v", err)
		}
		if _, err := client.Ping(t.Context()); err != nil {
			t.Fatalf("canceled shutdown reached broker: %v", err)
		}
	})
}

func TestShutdownBrokerForReplacementTimesOutWhileEpochRemainsHealthy(t *testing.T) {
	home := t.TempDir()
	stateDir, err := prepareStateDirectory(home)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := endpointForStateDirectory(stateDir)
	if err := prepareEndpoint(endpoint); err != nil {
		t.Fatal(err)
	}
	listener, err := listenLocal(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	token, err := randomHex(tokenBytes)
	if err != nil {
		t.Fatal(err)
	}
	epoch, err := randomHex(epochBytes)
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		PID: os.Getpid(), Protocol: ProtocolVersion, Token: token,
		Endpoint: endpoint, Epoch: epoch,
	}
	if err := writeManifest(stateDir, manifest); err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			var request RequestEnvelope
			if readFrameStrict(connection, &request) == nil {
				var response ResponseEnvelope
				switch request.Operation {
				case ControlOperationShutdown:
					response = coverageResponse(request, ShutdownResponse{Accepted: true})
				case ControlOperationPing:
					response = coverageResponse(request, PingResponse{
						Protocol: ProtocolVersion, PID: manifest.PID, Epoch: manifest.Epoch,
					})
				case ControlOperationStatus:
					response = coverageResponse(request, BrokerStatus{
						Protocol: ProtocolVersion, PID: manifest.PID, Epoch: manifest.Epoch,
						StartedAt:          time.Now().UTC(),
						CatalogFingerprint: "sha256:" + strings.Repeat("d", 64),
					})
				default:
					response = coverageResponse(request, EmptyPayload{})
				}
				_ = WriteFrame(connection, response)
			}
			_ = connection.Close()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("persistent test broker did not stop")
		}
		_ = removeManifestForEpoch(home, epoch)
		_ = cleanupEndpoint(endpoint)
	})
	client, err := Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	if err := shutdownBrokerForReplacement(ctx, client); CodeOf(err) != CodeDeadline {
		t.Fatalf("healthy retained epoch replacement error = %v", err)
	}

	configPath := filepath.Join(t.TempDir(), "config.json")
	configuration := config.DefaultConfig()
	configuration.Agents.Defaults.Workspace = filepath.Join(home, "workspace")
	if err := config.SaveConfig(configPath, configuration); err != nil {
		t.Fatal(err)
	}
	if client, err := EnsureSupervisor(context.Background(), EnsureOptions{
		Home:       home,
		Executable: filepath.Join(t.TempDir(), "missing-supervisor"),
		ConfigPath: configPath,
		Timeout:    120 * time.Millisecond,
	}); client != nil || CodeOf(err) != CodeUnavailable {
		t.Fatalf("stubborn mismatched supervisor replacement = (%p, %v)", client, err)
	}
}

func TestConsumeSupervisorBootstrapRejectsUnsearchableStateDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can unlink files from a read-only directory")
	}
	home := t.TempDir()
	token := strings.Repeat("c", tokenBytes*2)
	path, err := prepareSupervisorBootstrap(home, token)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Dir(path)
	if err := os.Chmod(stateDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(stateDir, 0o700)
		_ = os.Remove(path)
	})
	t.Setenv(supervisorBootstrapEnvironment, token)
	if ConsumeSupervisorBootstrap(home) {
		t.Fatal("bootstrap beneath an unsearchable state directory was consumed")
	}
}

func TestMonitorSupervisorNilContextReturnsValidationFailure(t *testing.T) {
	if err := MonitorSupervisor(nil, EnsureOptions{Home: "bad\x00home"}); CodeOf(err) != CodeInvalid {
		t.Fatalf("nil-context monitor validation error = %v", err)
	}
}

func TestMonitorSupervisorRecoversAfterExecutableAppears(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	configuration := config.DefaultConfig()
	configuration.Agents.Defaults.Workspace = filepath.Join(home, "workspace")
	if err := config.SaveConfig(configPath, configuration); err != nil {
		t.Fatal(err)
	}
	_, fingerprint, err := LoadCatalogConfiguration(configPath)
	if err != nil {
		t.Fatal(err)
	}
	initialStatus := make(chan struct{})
	var statusOnce sync.Once
	initial, err := StartServer(context.Background(), ServerOptions{
		Home: home, CatalogFingerprint: fingerprint,
		StatusProvider: func(context.Context) ([]StoreStatus, error) {
			statusOnce.Do(func() { close(initialStatus) })
			return []StoreStatus{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	initialEpoch := initial.Manifest().Epoch

	helper := filepath.Join(t.TempDir(), "delayed-supervisor-helper")
	t.Setenv(supervisorProcessHelperEnvironment, "1")
	monitorContext, cancelMonitor := context.WithCancel(context.Background())
	monitorDone := make(chan error, 1)
	go func() {
		monitorDone <- MonitorSupervisor(monitorContext, EnsureOptions{
			Home: home, Executable: helper, ConfigPath: configPath, Timeout: 120 * time.Millisecond,
		})
	}()
	select {
	case <-initialStatus:
	case err := <-monitorDone:
		t.Fatalf("monitor exited before initial attachment: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("monitor did not attach to initial supervisor")
	}
	// Let the status response reach MonitorSupervisor before stopping the broker.
	time.Sleep(50 * time.Millisecond)
	closeServer(t, initial)

	helperWrite := time.AfterFunc(2300*time.Millisecond, func() {
		script := fmt.Sprintf(
			"#!/bin/sh\nexec %q -test.run=^TestSupervisorProcessHelper$ -- \"$@\"\n",
			os.Args[0],
		)
		_ = os.WriteFile(helper, []byte(script), 0o700)
	})
	defer helperWrite.Stop()

	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	var replacement *Client
	for replacement == nil {
		select {
		case err := <-monitorDone:
			t.Fatalf("monitor exited before recovery: %v", err)
		case <-deadline.C:
			logData, _ := os.ReadFile(filepath.Join(home, "logs", "database-supervisor.log"))
			t.Fatalf("monitor did not recover supervisor; log:\n%s", logData)
		case <-ticker.C:
			candidate, connectErr := Connect(home)
			if connectErr != nil || candidate.Epoch() == initialEpoch {
				continue
			}
			pingContext, cancel := context.WithTimeout(context.Background(), time.Second)
			_, pingErr := candidate.Ping(pingContext)
			cancel()
			if pingErr == nil {
				replacement = candidate
			}
		}
	}
	// Allow the successful inner EnsureSupervisor call to settle back into the
	// monitor loop before cancellation.
	time.Sleep(50 * time.Millisecond)
	cancelMonitor()
	select {
	case err := <-monitorDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("monitor recovery cancellation = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("recovered monitor did not stop")
	}
	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelShutdown()
	if err := replacement.Shutdown(shutdownContext); err != nil && CodeOf(err) != CodeOutcomeUnknown {
		t.Fatalf("shutdown replacement supervisor: %v", err)
	}
}

func TestMonitorSupervisorStopsDuringRetryBackoff(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	configuration := config.DefaultConfig()
	configuration.Agents.Defaults.Workspace = filepath.Join(home, "workspace")
	if err := config.SaveConfig(configPath, configuration); err != nil {
		t.Fatal(err)
	}
	_, fingerprint, err := LoadCatalogConfiguration(configPath)
	if err != nil {
		t.Fatal(err)
	}
	initialStatus := make(chan struct{})
	var statusOnce sync.Once
	initial, err := StartServer(context.Background(), ServerOptions{
		Home: home, CatalogFingerprint: fingerprint,
		StatusProvider: func(context.Context) ([]StoreStatus, error) {
			statusOnce.Do(func() { close(initialStatus) })
			return []StoreStatus{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	monitorContext, cancelMonitor := context.WithCancel(context.Background())
	monitorDone := make(chan error, 1)
	missingExecutable := filepath.Join(t.TempDir(), "missing-supervisor")
	go func() {
		monitorDone <- MonitorSupervisor(monitorContext, EnsureOptions{
			Home:       home,
			Executable: missingExecutable,
			ConfigPath: configPath,
			Timeout:    80 * time.Millisecond,
		})
	}()
	select {
	case <-initialStatus:
	case err := <-monitorDone:
		t.Fatalf("monitor exited before initial attachment: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("monitor did not attach to initial supervisor")
	}
	time.Sleep(50 * time.Millisecond)
	closeServer(t, initial)
	// The fixed two-second probe plus two bounded failed Ensure calls reaches
	// the retry timer without relying on an injected process-start failure.
	time.Sleep(2300 * time.Millisecond)
	cancelMonitor()
	select {
	case err := <-monitorDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("retrying monitor cancellation = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("retrying monitor did not stop")
	}
}

func removeTestBootstrapFiles(t *testing.T, home string) {
	t.Helper()
	stateDir, err := StateDirectory(home)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".bootstrap-") {
			continue
		}
		if err := os.Remove(filepath.Join(stateDir, entry.Name())); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	// Give the short-lived helper a chance to exit before its test directory is
	// reclaimed. The process is detached by production code, so readiness is the
	// durable bootstrap/log state rather than a waitable child handle.
	time.Sleep(20 * time.Millisecond)
}
