//go:build unix

package database

import (
	"os"
	"sync/atomic"
	"testing"
)

func TestShutdownReplacementAcceptsRealPostShutdownPingFailure(t *testing.T) {
	home := t.TempDir()
	stateDir, err := prepareStateDirectory(home)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := endpointForStateDirectory(stateDir)
	if prepareErr := prepareEndpoint(endpoint); prepareErr != nil {
		t.Fatal(prepareErr)
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
	if writeErr := writeManifest(stateDir, manifest); writeErr != nil {
		_ = listener.Close()
		t.Fatal(writeErr)
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
