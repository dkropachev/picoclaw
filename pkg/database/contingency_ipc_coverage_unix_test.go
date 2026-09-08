//go:build unix

package database

import (
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestCleanupEndpointPropagatesRealParentPermissionFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permissions")
	}
	parent := shortCoverageUnixTempDir(t)
	endpoint := filepath.Join(parent, "broker.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: endpoint, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if err := os.Chmod(endpoint, 0o600); err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(parent, 0o700)
		_ = os.Remove(endpoint)
	})
	if err := cleanupEndpoint(endpoint); err == nil {
		t.Fatal("socket cleanup succeeded without parent write permission")
	}
}

func TestShutdownReplacementAcceptsRealPostShutdownPingFailure(t *testing.T) {
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
		_ = listener.Close()
		t.Fatal(err)
	}
	epoch, err := randomHex(epochBytes)
	if err != nil {
		_ = listener.Close()
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
	var shutdown atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			var request RequestEnvelope
			if ReadFrame(connection, &request) == nil {
				response := contingencyResponse(request, EmptyPayload{})
				switch request.Operation {
				case ControlOperationShutdown:
					shutdown.Store(true)
					response = contingencyResponse(request, ShutdownResponse{Accepted: true})
				case ControlOperationPing:
					if shutdown.Load() {
						response = ResponseEnvelope{
							Protocol: ProtocolVersion, RequestID: request.RequestID,
							BrokerEpoch: request.BrokerEpoch,
							Error:       NewError(CodeUnavailable, "broker is draining"),
						}
					} else {
						response = contingencyResponse(request, PingResponse{
							Protocol: ProtocolVersion, PID: manifest.PID, Epoch: manifest.Epoch,
						})
					}
				}
				_ = WriteFrame(connection, response)
			}
			_ = connection.Close()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
		_ = removeManifestForEpoch(home, epoch)
		_ = cleanupEndpoint(endpoint)
	})
	client, err := Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := shutdownBrokerForReplacement(t.Context(), client); err != nil {
		t.Fatalf("post-shutdown ping failure = %v", err)
	}
}

func contingencyResponse(request RequestEnvelope, payload any) ResponseEnvelope {
	raw, _ := MarshalCanonical(payload)
	return ResponseEnvelope{
		Protocol: ProtocolVersion, RequestID: request.RequestID,
		BrokerEpoch: request.BrokerEpoch, Payload: raw,
	}
}
