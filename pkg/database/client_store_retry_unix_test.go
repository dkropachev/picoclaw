//go:build unix

package database

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestStoreBoundMutationRetryReusesExactEnvelope(t *testing.T) {
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
	t.Cleanup(func() { _ = listener.Close() })
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
	requests := make(chan RequestEnvelope, 2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for attempt := 0; attempt < 2; attempt++ {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			var request RequestEnvelope
			if readFrameStrict(connection, &request) == nil {
				requests <- request
				if attempt == 1 {
					payload, _ := marshalPayload(EmptyPayload{})
					_ = WriteFrame(connection, ResponseEnvelope{
						Protocol: ProtocolVersion, RequestID: request.RequestID,
						BrokerEpoch: request.BrokerEpoch, Payload: payload,
					})
				}
			}
			_ = connection.Close()
		}
	}()
	client, err := ConnectWithManifest(home, manifest)
	if err != nil {
		t.Fatal(err)
	}
	storeID := StoreID("workspace/repository-reviews")
	if err := client.CallStoreWithOptions(
		context.Background(), storeID, "repository-reviews", 1, "save", EmptyPayload{}, nil,
		CallOptions{Mutation: true, IdempotencyKey: "store-bound-retry"},
	); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("store-bound retry server did not finish")
	}
	captured := make([]RequestEnvelope, 0, 2)
	for len(captured) < 2 {
		select {
		case request := <-requests:
			captured = append(captured, request)
		case <-time.After(5 * time.Second):
			t.Fatalf("captured retry envelopes = %d, want 2", len(captured))
		}
	}
	first, second := captured[0], captured[1]
	if first.StoreID != storeID || !reflect.DeepEqual(first, second) {
		t.Fatalf("mutation retry envelopes differ: first=%#v second=%#v", first, second)
	}
}
