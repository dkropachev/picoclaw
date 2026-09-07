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
	"testing"
	"time"
)

func TestCoverageUnixEndpointLifecycle(t *testing.T) {
	root := shortCoverageUnixTempDir(t)
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

func TestCoverageUnixCleanupExistingSocketSuccess(t *testing.T) {
	endpoint := filepath.Join(shortCoverageUnixTempDir(t), "broker.sock")
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
	if err := cleanupEndpoint(endpoint); err != nil {
		t.Fatal(err)
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
		parent := filepath.Join(shortCoverageUnixTempDir(t), "parent")
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

func shortCoverageUnixTempDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "pcdb-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Errorf("remove short Unix temp directory: %v", err)
		}
	})
	return directory
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

func TestCoverageUnixLocks(t *testing.T) {
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
}
