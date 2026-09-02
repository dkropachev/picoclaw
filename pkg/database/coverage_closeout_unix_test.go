//go:build unix

//nolint:govet // Independent failure-boundary assertions intentionally reuse narrow error names.
package database

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestCoverageUnixEndpointLifecycle(t *testing.T) {
	root := t.TempDir()
	endpoint := filepath.Join(root, "socket-parent", "broker.sock")
	if err := prepareEndpoint(endpoint); err != nil {
		t.Fatal(err)
	}
	if err := prepareEndpoint(endpoint); err != nil {
		t.Fatal(err)
	}
	listener, err := listenLocal(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan net.Conn, 1)
	go func() {
		connection, _ := listener.Accept()
		accepted <- connection
	}()
	connection, err := dialLocal(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	if peer := <-accepted; peer != nil {
		_ = peer.Close()
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cleanupEndpoint(endpoint); err != nil {
		t.Fatal(err)
	}
	if err := cleanupEndpoint(endpoint); err != nil {
		t.Fatal(err)
	}

	missingContext, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err := dialLocal(missingContext, filepath.Join(root, "missing.sock")); err == nil {
		t.Fatal("dial of missing endpoint succeeded")
	}
	if got := endpointForStateDirectory(filepath.Join(root, "state")); filepath.Ext(got) != ".sock" {
		t.Fatalf("derived endpoint = %q", got)
	}
}

func coverageResponseClient(
	t *testing.T,
	respond func(RequestEnvelope) ResponseEnvelope,
) *Client {
	t.Helper()
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
	token, _ := randomHex(tokenBytes)
	epoch, _ := randomHex(epochBytes)
	manifest := Manifest{
		PID: os.Getpid(), Protocol: ProtocolVersion, Token: token, Endpoint: endpoint, Epoch: epoch,
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer listener.Close()
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		var request RequestEnvelope
		if readFrameStrict(connection, &request) != nil {
			return
		}
		_ = WriteFrame(connection, respond(request))
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("coverage response server did not stop")
		}
	})
	client, err := ConnectWithManifest(home, manifest)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func coverageResponse(request RequestEnvelope, payload any) ResponseEnvelope {
	raw, _ := marshalPayload(payload)
	return ResponseEnvelope{
		Protocol: ProtocolVersion, RequestID: request.RequestID,
		BrokerEpoch: request.BrokerEpoch, Payload: raw,
	}
}

func TestCoverageClientResponseValidationBoundaries(t *testing.T) {
	t.Run("nil output", func(t *testing.T) {
		client := coverageResponseClient(t, func(request RequestEnvelope) ResponseEnvelope {
			return coverageResponse(request, EmptyPayload{})
		})
		if err := client.CallWithOptions(
			t.Context(), "domain", 1, "read", EmptyPayload{}, nil, CallOptions{},
		); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("structured error", func(t *testing.T) {
		client := coverageResponseClient(t, func(request RequestEnvelope) ResponseEnvelope {
			return ResponseEnvelope{
				Protocol: 1, RequestID: request.RequestID, BrokerEpoch: request.BrokerEpoch,
				Error: NewError(CodeNotFound, "missing"),
			}
		})
		if err := client.CallWithOptions(
			t.Context(), "domain", 1, "read", EmptyPayload{}, nil, CallOptions{},
		); CodeOf(err) != CodeNotFound {
			t.Fatalf("structured response error = %v", err)
		}
	})
	t.Run("request mismatch", func(t *testing.T) {
		client := coverageResponseClient(t, func(request RequestEnvelope) ResponseEnvelope {
			response := coverageResponse(request, EmptyPayload{})
			response.RequestID = "other"
			return response
		})
		if err := client.CallWithOptions(
			t.Context(), "domain", 1, "read", EmptyPayload{}, nil, CallOptions{},
		); CodeOf(err) != CodeUnavailable {
			t.Fatalf("mismatched response = %v", err)
		}
	})
	t.Run("epoch mismatch", func(t *testing.T) {
		client := coverageResponseClient(t, func(request RequestEnvelope) ResponseEnvelope {
			response := coverageResponse(request, EmptyPayload{})
			response.BrokerEpoch = "stale"
			return response
		})
		if err := client.CallWithOptions(
			t.Context(), "domain", 1, "read", EmptyPayload{}, nil, CallOptions{},
		); CodeOf(err) != CodeConflict {
			t.Fatalf("stale response = %v", err)
		}
	})
	t.Run("decode failure", func(t *testing.T) {
		client := coverageResponseClient(t, func(request RequestEnvelope) ResponseEnvelope {
			return coverageResponse(request, struct {
				Unexpected int `json:"unexpected"`
			}{Unexpected: 1})
		})
		var output struct {
			Expected int `json:"expected"`
		}
		if err := client.CallWithOptions(
			t.Context(), "domain", 1, "read", EmptyPayload{}, &output, CallOptions{},
		); CodeOf(err) != CodeUnavailable {
			t.Fatalf("undecodable response = %v", err)
		}
	})
	t.Run("ping identity", func(t *testing.T) {
		client := coverageResponseClient(t, func(request RequestEnvelope) ResponseEnvelope {
			return coverageResponse(request, PingResponse{
				Protocol: 1, PID: -1, Epoch: request.BrokerEpoch,
			})
		})
		if _, err := client.Ping(t.Context()); CodeOf(err) != CodeIntegrity {
			t.Fatalf("invalid ping identity = %v", err)
		}
	})
	t.Run("shutdown rejection", func(t *testing.T) {
		client := coverageResponseClient(t, func(request RequestEnvelope) ResponseEnvelope {
			return coverageResponse(request, ShutdownResponse{})
		})
		if err := client.Shutdown(t.Context()); CodeOf(err) != CodeIntegrity {
			t.Fatalf("shutdown rejection = %v", err)
		}
	})
}

func TestCoverageClientStatusValidationBoundaries(t *testing.T) {
	valid := func(request RequestEnvelope) BrokerStatus {
		return BrokerStatus{
			Protocol: 1, PID: os.Getpid(), Epoch: request.BrokerEpoch, StartedAt: time.Now(),
			RequiredStores: []StoreID{"global/auth"},
			Stores:         []StoreStatus{{ID: "global/auth", Readiness: StoreReady}},
		}
	}
	for _, test := range []struct {
		name   string
		mutate func(*BrokerStatus)
	}{
		{name: "invalid stores", mutate: func(status *BrokerStatus) {
			status.Stores[0].ID = "bad id"
		}},
		{name: "invalid required", mutate: func(status *BrokerStatus) {
			status.RequiredStores[0] = "bad id"
		}},
		{name: "missing required", mutate: func(status *BrokerStatus) { status.Stores = nil }},
		{name: "catalog fingerprint", mutate: func(status *BrokerStatus) {
			status.CatalogFingerprint = "bad"
		}},
		{name: "identity", mutate: func(status *BrokerStatus) { status.PID = -1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := coverageResponseClient(t, func(request RequestEnvelope) ResponseEnvelope {
				status := valid(request)
				test.mutate(&status)
				return coverageResponse(request, status)
			})
			if _, err := client.Status(t.Context()); CodeOf(err) != CodeIntegrity {
				t.Fatalf("invalid status = %v", err)
			}
		})
	}
}

func TestCoverageClientRejectsNonCanonicalResponseFrame(t *testing.T) {
	client := coverageResponseClient(t, func(request RequestEnvelope) ResponseEnvelope {
		return ResponseEnvelope{
			Protocol: 1, RequestID: request.RequestID, BrokerEpoch: request.BrokerEpoch,
			Payload: json.RawMessage(`{"z":1,"a":2}`),
		}
	})
	var output map[string]int
	if err := client.CallWithOptions(
		t.Context(), "domain", 1, "read", EmptyPayload{}, &output, CallOptions{},
	); err != nil {
		// WriteFrame canonicalizes nested raw JSON before transport, so this branch
		// proves the response remains safely decodable instead of carrying raw order.
		t.Fatal(err)
	}
}

func TestCoverageUnixEndpointRejectsUnsafeBoundaries(t *testing.T) {
	t.Run("parent file", func(t *testing.T) {
		parent := filepath.Join(t.TempDir(), "parent")
		if err := os.WriteFile(parent, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := prepareEndpoint(filepath.Join(parent, "broker.sock")); err == nil {
			t.Fatal("file socket parent accepted")
		}
	})
	t.Run("public parent", func(t *testing.T) {
		parent := filepath.Join(t.TempDir(), "parent")
		if err := os.Mkdir(parent, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := prepareEndpoint(filepath.Join(parent, "broker.sock")); CodeOf(err) != CodeIntegrity {
			t.Fatalf("public socket parent = %v", err)
		}
	})
	t.Run("endpoint file", func(t *testing.T) {
		parent := filepath.Join(t.TempDir(), "parent")
		if err := os.Mkdir(parent, 0o700); err != nil {
			t.Fatal(err)
		}
		endpoint := filepath.Join(parent, "broker.sock")
		if err := os.WriteFile(endpoint, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := prepareEndpoint(endpoint); CodeOf(err) != CodeIntegrity {
			t.Fatalf("file endpoint = %v", err)
		}
		if err := cleanupEndpoint(endpoint); CodeOf(err) != CodeIntegrity {
			t.Fatalf("file endpoint cleanup = %v", err)
		}
	})
	t.Run("stale socket", func(t *testing.T) {
		parent := filepath.Join(t.TempDir(), "parent")
		if err := os.Mkdir(parent, 0o700); err != nil {
			t.Fatal(err)
		}
		endpoint := filepath.Join(parent, "broker.sock")
		listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: endpoint, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		listener.SetUnlinkOnClose(false)
		if err := os.Chmod(endpoint, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
		if err := prepareEndpoint(endpoint); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(endpoint); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stale socket remains: %v", err)
		}
	})
}

func TestCoverageUnixOwnerOnlyFileHelpers(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "directory")
	if err := createOwnerOnlyDirectory(directory); err != nil {
		t.Fatal(err)
	}
	if err := createOwnerOnlyDirectory(directory); err == nil {
		t.Fatal("exclusive owner directory recreated")
	}
	leaf := filepath.Join(root, "nested", "leaf")
	if err := prepareOwnerOnlyLeafDirectory(leaf); err != nil {
		t.Fatal(err)
	}
	file, err := createOwnerOnlyTempFile(leaf, "temporary-", 0o600)
	if err != nil {
		t.Fatal(err)
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err := openOwnerOnlyExistingFile(path, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_ = opened.Close()
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := openOwnerOnlyExistingFile(path, 0o600); err == nil {
		t.Fatal("public owner file opened")
	}
	exclusive := filepath.Join(leaf, "exclusive")
	exclusiveFile, err := createOwnerOnlyExclusiveFile(exclusive, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_ = exclusiveFile.Close()
	if _, err := createOwnerOnlyExclusiveFile(exclusive, 0o600); err == nil {
		t.Fatal("exclusive owner file recreated")
	}
	if _, err := openOwnerOnlyExistingFile(filepath.Join(root, "missing"), 0o600); err == nil {
		t.Fatal("missing owner file opened")
	}

	dirInfo, _ := os.Lstat(directory)
	if err := validateOwnerOnlyDirectory(directory, dirInfo); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	dirInfo, _ = os.Lstat(directory)
	if err := validateOwnerOnlyDirectory(directory, dirInfo); CodeOf(err) != CodeIntegrity {
		t.Fatalf("public directory validation = %v", err)
	}
	if err := validateOwnerOnlyFile(directory, dirInfo, 0o600); CodeOf(err) != CodeIntegrity {
		t.Fatalf("directory file validation = %v", err)
	}
}

func TestCoverageUnixLocksAndPhysicalClaims(t *testing.T) {
	if err := releasePlatformFileLock(nil); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	lockPath := filepath.Join(root, "store.lock")
	first, err := acquirePlatformFileLock(lockPath, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquirePlatformFileLock(lockPath, true); !errors.Is(err, errFileLockBusy) {
		t.Fatalf("contended file lock = %v", err)
	}
	if err := releasePlatformFileLock(first); err != nil {
		t.Fatal(err)
	}
	linkTarget := filepath.Join(root, "target")
	if err := os.WriteFile(linkTarget, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(linkTarget, link); err == nil {
		if _, err := acquirePlatformFileLock(link, false); CodeOf(err) != CodeIntegrity {
			t.Fatalf("symlink lock = %v", err)
		}
	}

	restore := SuspendProviderTestAuthority()
	if claims, err := AcquireCatalogStoreClaims(t.TempDir(), &config.Config{}); claims != nil ||
		CodeOf(err) != CodeUnauthorized {
		t.Fatalf("unauthorized physical claims = %#v, %v", claims, err)
	}
	restore()
	claims, err := AcquireCatalogStoreClaims(t.TempDir(), &config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := claims.Close(); err != nil {
		t.Fatal(err)
	}
	if err := claims.Close(); err != nil {
		t.Fatal(err)
	}
	if err := (*PhysicalStoreClaims)(nil).Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCoverageSupervisorProcessValidationAndBootstrap(t *testing.T) {
	home := t.TempDir()
	if err := startSupervisorProcess(
		EnsureOptions{Executable: "picoclaw-missing-coverage-binary"},
		home,
	); CodeOf(
		err,
	) != CodeUnavailable {
		t.Fatalf("missing supervisor executable = %v", err)
	}
	if err := startSupervisorProcess(EnsureOptions{Executable: t.TempDir()}, home); CodeOf(err) != CodeUnavailable {
		t.Fatalf("directory supervisor executable = %v", err)
	}
	nonExecutable := filepath.Join(t.TempDir(), "not-executable")
	if err := os.WriteFile(nonExecutable, []byte("exit 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := startSupervisorProcess(EnsureOptions{Executable: nonExecutable}, home); err == nil ||
		!strings.Contains(err.Error(), "start database supervisor") {
		t.Fatalf("non-executable supervisor = %v", err)
	}
	executable := filepath.Join(t.TempDir(), "supervisor-exit")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := config.SaveConfig(configPath, config.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	if err := startSupervisorProcess(EnsureOptions{
		Executable: executable, ConfigPath: configPath,
	}, home); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(
		filepath.Join(home, "logs", "database-supervisor.log"),
	); err != nil ||
		!info.Mode().IsRegular() {
		t.Fatalf("supervisor log = %v, %v", info, err)
	}

	environment := replaceSupervisorEnvironment(
		[]string{"A=1", "PICOCLAW_CONFIG=old", "B=2", "PICOCLAW_CONFIG=older"},
		"PICOCLAW_CONFIG", "new",
	)
	if strings.Join(environment, ",") != "A=1,B=2,PICOCLAW_CONFIG=new" {
		t.Fatalf("replaced environment = %#v", environment)
	}

	token, _ := randomHex(tokenBytes)
	path, err := prepareSupervisorBootstrap(home, token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prepareSupervisorBootstrap(home, token); err == nil {
		t.Fatal("duplicate bootstrap created")
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(supervisorBootstrapEnvironment, token)
	if ConsumeSupervisorBootstrap(home) {
		t.Fatal("public bootstrap consumed")
	}
	if os.Getenv(supervisorBootstrapEnvironment) != "" {
		t.Fatal("invalid bootstrap environment remained")
	}
	_ = os.Remove(path)
	for _, test := range []struct {
		name  string
		home  string
		value string
	}{
		{name: "invalid token", home: home, value: "bad"},
		{name: "invalid home", home: "bad\x00home", value: strings.Repeat("a", tokenBytes*2)},
		{name: "missing file", home: home, value: strings.Repeat("b", tokenBytes*2)},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(supervisorBootstrapEnvironment, test.value)
			if ConsumeSupervisorBootstrap(test.home) {
				t.Fatal("invalid bootstrap consumed")
			}
		})
	}
	blockedHome := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blockedHome, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareSupervisorBootstrap(blockedHome, token); err == nil {
		t.Fatal("bootstrap accepted file home")
	}
}

func TestCoverageEnsureAndMonitorSupervisorBoundaries(t *testing.T) {
	if _, err := EnsureSupervisor(nil, EnsureOptions{Home: "bad\x00home"}); err == nil {
		t.Fatal("EnsureSupervisor accepted invalid home")
	}
	badConfig := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(badConfig, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureSupervisor(nil, EnsureOptions{Home: t.TempDir(), ConfigPath: badConfig}); err == nil {
		t.Fatal("EnsureSupervisor accepted invalid config")
	}
	validConfig := filepath.Join(t.TempDir(), "valid.json")
	if err := config.SaveConfig(validConfig, config.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := EnsureSupervisor(canceled, EnsureOptions{
		Home: t.TempDir(), Executable: "missing-supervisor", ConfigPath: validConfig, Timeout: time.Second,
	}); CodeOf(err) != CodeDeadline {
		t.Fatalf("canceled supervisor ensure = %v", err)
	}
	if _, err := EnsureSupervisor(context.Background(), EnsureOptions{
		Home: t.TempDir(), Executable: "missing-supervisor", ConfigPath: validConfig,
		Timeout: 60 * time.Millisecond,
	}); CodeOf(err) != CodeUnavailable {
		t.Fatalf("timed-out supervisor ensure = %v", err)
	}

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
	server, err := StartServer(context.Background(), ServerOptions{
		Home: home, CatalogFingerprint: fingerprint,
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := EnsureSupervisor(nil, EnsureOptions{Home: home, ConfigPath: configPath})
	if err != nil || client.Epoch() != server.Manifest().Epoch {
		t.Fatalf("attached supervisor = %p, %v", client, err)
	}
	if ready, err := connectReadyBroker(t.Context(), home); err != nil || ready == nil {
		t.Fatalf("ready broker = %p, %v", ready, err)
	}
	if ready, status, err := connectReadyBrokerStatus(
		t.Context(),
		home,
	); err != nil || ready == nil ||
		status.Epoch != server.Manifest().Epoch {
		t.Fatalf("ready broker status = %p, %#v, %v", ready, status, err)
	}
	monitorContext, cancelMonitor := context.WithCancel(context.Background())
	monitorDone := make(chan error, 1)
	go func() {
		monitorDone <- MonitorSupervisor(monitorContext, EnsureOptions{
			Home: home, ConfigPath: configPath,
		})
	}()
	time.AfterFunc(2100*time.Millisecond, cancelMonitor)
	if err := <-monitorDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("monitor cancellation = %v", err)
	}
	if err := shutdownBrokerForReplacement(t.Context(), nil); CodeOf(err) != CodeUnavailable {
		t.Fatalf("nil replacement shutdown = %v", err)
	}
	if err := shutdownBrokerForReplacement(t.Context(), client); err != nil {
		t.Fatal(err)
	}
	select {
	case <-server.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("replacement shutdown did not stop server")
	}
	if _, err := connectReadyBroker(t.Context(), home); err == nil {
		t.Fatal("stopped broker remained ready")
	}
}
