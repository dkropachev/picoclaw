package database

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type idempotencyMutationRequest struct {
	Value string `json:"value"`
}

type idempotencyMutationResponse struct {
	Count int64  `json:"count"`
	Value string `json:"value"`
}

func TestServerReplaysStableIdempotentMutationAcrossRequestIDs(t *testing.T) {
	t.Parallel()
	var mutations atomic.Int64
	server := newIdempotencyTestServer(HandlerFunc(func(
		_ context.Context,
		request Request,
	) (any, error) {
		var input idempotencyMutationRequest
		if err := request.DecodePayload(&input); err != nil {
			return nil, err
		}
		return idempotencyMutationResponse{
			Count: mutations.Add(1),
			Value: input.Value,
		}, nil
	}))
	firstRequest := idempotencyTestEnvelope(t, "stable-1", "request-1", "first")
	secondRequest := firstRequest
	secondRequest.RequestID = "request-2"
	first, _ := server.dispatch(firstRequest)
	second, _ := server.dispatch(secondRequest)
	if first.Error != nil || second.Error != nil {
		t.Fatalf("dispatch errors = %v, %v", first.Error, second.Error)
	}
	if first.RequestID != firstRequest.RequestID || second.RequestID != secondRequest.RequestID ||
		first.BrokerEpoch != firstRequest.BrokerEpoch || second.BrokerEpoch != secondRequest.BrokerEpoch {
		t.Fatalf("response identities = %#v, %#v", first, second)
	}
	if err := validResponseEnvelope(first, firstRequest.RequestID, firstRequest.BrokerEpoch); err != nil {
		t.Fatalf("first response is invalid: %v", err)
	}
	if err := validResponseEnvelope(second, secondRequest.RequestID, secondRequest.BrokerEpoch); err != nil {
		t.Fatalf("replayed response is invalid: %v", err)
	}
	if string(first.Payload) != string(second.Payload) {
		t.Fatalf("replayed payload = %s, want %s", second.Payload, first.Payload)
	}
	if got := mutations.Load(); got != 1 {
		t.Fatalf("domain mutation count = %d, want 1", got)
	}
}

func TestServerRejectsIdempotencyKeyReuseForDifferentPayload(t *testing.T) {
	t.Parallel()
	var mutations atomic.Int64
	server := newIdempotencyTestServer(HandlerFunc(func(context.Context, Request) (any, error) {
		return idempotencyMutationResponse{Count: mutations.Add(1)}, nil
	}))
	first := idempotencyTestEnvelope(t, "stable-1", "request-1", "first")
	second := idempotencyTestEnvelope(t, "stable-1", "request-2", "second")
	firstResponse, _ := server.dispatch(first)
	secondResponse, _ := server.dispatch(second)
	if firstResponse.Error != nil {
		t.Fatalf("first dispatch error = %v", firstResponse.Error)
	}
	if CodeOf(secondResponse.Error) != CodeConflict {
		t.Fatalf("second dispatch error = %v, want Conflict", secondResponse.Error)
	}
	if got := mutations.Load(); got != 1 {
		t.Fatalf("domain mutation count = %d, want 1", got)
	}
}

func TestServerCoalescesConcurrentIdempotentMutation(t *testing.T) {
	t.Parallel()
	var mutations atomic.Int64
	started := make(chan struct{})
	release := make(chan struct{})
	server := newIdempotencyTestServer(HandlerFunc(func(context.Context, Request) (any, error) {
		if mutations.Add(1) == 1 {
			close(started)
		}
		<-release
		return idempotencyMutationResponse{Count: 1, Value: "coalesced"}, nil
	}))
	firstRequest := idempotencyTestEnvelope(t, "stable-concurrent", "request-concurrent-1", "value")
	secondRequest := firstRequest
	secondRequest.RequestID = "request-concurrent-2"
	type result struct {
		request  RequestEnvelope
		response ResponseEnvelope
	}
	responses := make(chan result, 2)
	go func() {
		response, _ := server.dispatch(firstRequest)
		responses <- result{request: firstRequest, response: response}
	}()
	<-started
	go func() {
		response, _ := server.dispatch(secondRequest)
		responses <- result{request: secondRequest, response: response}
	}()
	close(release)
	first, second := <-responses, <-responses
	if first.response.Error != nil || second.response.Error != nil ||
		string(first.response.Payload) != string(second.response.Payload) {
		t.Fatalf("concurrent responses = %#v, %#v", first, second)
	}
	for _, completed := range []result{first, second} {
		if err := validResponseEnvelope(
			completed.response,
			completed.request.RequestID,
			completed.request.BrokerEpoch,
		); err != nil {
			t.Fatalf("concurrent response for %q is invalid: %v", completed.request.RequestID, err)
		}
	}
	if got := mutations.Load(); got != 1 {
		t.Fatalf("domain mutation count = %d, want 1", got)
	}
}

func TestServerRejectsUndeclaredIdempotencyBeforeDispatch(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	server := newIdempotencyTestServer(HandlerFunc(func(context.Context, Request) (any, error) {
		calls.Add(1)
		return idempotencyMutationResponse{}, nil
	}))
	server.allowsIdempotency = func(string, int, string) bool { return false }
	response, _ := server.dispatch(idempotencyTestEnvelope(t, "undeclared", "request", "value"))
	if CodeOf(response.Error) != CodeUnsupported || calls.Load() != 0 {
		t.Fatalf("undeclared response = %#v, dispatches=%d", response, calls.Load())
	}
}

func newIdempotencyTestServer(handler Handler) *Server {
	return &Server{
		manifest: Manifest{Token: "test-token", Epoch: "test-epoch"},
		handler:  handler, now: time.Now, ctx: context.Background(),
		idempotency:       newIdempotencyRegistry(),
		allowsIdempotency: func(string, int, string) bool { return true },
	}
}

func idempotencyTestEnvelope(
	t *testing.T,
	key,
	requestID,
	value string,
) RequestEnvelope {
	t.Helper()
	payload, err := marshalPayload(idempotencyMutationRequest{Value: value})
	if err != nil {
		t.Fatal(err)
	}
	return RequestEnvelope{
		Protocol: ProtocolVersion, RequestID: requestID, Token: "test-token",
		BrokerEpoch: "test-epoch", Domain: "mutation", DomainVersion: 1,
		Operation: "apply", DeadlineUnixNs: time.Now().Add(time.Minute).UnixNano(),
		IdempotencyKey: key, Payload: payload,
	}
}
