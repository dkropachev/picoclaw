//go:build unix

//nolint:govet // Independent protocol assertions intentionally reuse err.
package database

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type echoRequest struct {
	Value string `json:"value"`
}

type echoResponse struct {
	Value          string `json:"value"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

func TestControlAndTypedDomainRoundTrip(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	startedAt := time.Now().UTC().Truncate(time.Nanosecond)
	handler := HandlerFunc(func(_ context.Context, request Request) (any, error) {
		switch request.Operation {
		case "echo":
			var input echoRequest
			if err := request.DecodePayload(&input); err != nil {
				return nil, NewError(CodeInvalid, "echo payload is invalid")
			}
			return echoResponse{Value: input.Value, IdempotencyKey: request.IdempotencyKey}, nil
		case "missing":
			return nil, NewError(CodeNotFound, "echo record was not found")
		default:
			return nil, NewError(CodeUnsupported, "echo operation is unsupported")
		}
	})
	server, err := StartServer(context.Background(), ServerOptions{
		Home: home,
		StatusProvider: func(context.Context) ([]StoreStatus, error) {
			return []StoreStatus{
				{
					ID: "workspace/workflows", Readiness: StoreMigrationRequired,
					Error: NewError(CodeMigrationRequired, "offline migration is required"),
				},
				{ID: "global/auth", Readiness: StoreReady},
			}, nil
		},
		Handler: handler,
		AllowsIdempotency: func(domain string, version int, operation string) bool {
			return domain == "test-domain" && version == 1 && operation == "echo"
		},
		Now: func() time.Time { return startedAt },
	})
	if err != nil {
		t.Fatalf("StartServer() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	client, err := Connect(home)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	ping, err := client.Ping(context.Background())
	if err != nil || ping.Epoch != server.Manifest().Epoch || ping.PID != os.Getpid() {
		t.Fatalf("Ping() = %#v, %v", ping, err)
	}
	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.StartedAt.Equal(startedAt) || len(status.Stores) != 2 ||
		status.Stores[0].ID != "global/auth" || status.Stores[1].ID != "workspace/workflows" {
		t.Fatalf("Status() = %#v", status)
	}

	var echo echoResponse
	if err := client.CallWithOptions(
		context.Background(), "test-domain", 1, "echo", echoRequest{Value: "hello"}, &echo,
		CallOptions{Mutation: true, IdempotencyKey: "stable-request-1"},
	); err != nil {
		t.Fatalf("typed CallWithOptions() error = %v", err)
	}
	if echo.Value != "hello" || echo.IdempotencyKey != "stable-request-1" {
		t.Fatalf("echo response = %#v", echo)
	}
	if err := client.Call(
		context.Background(), "test-domain", 1, "missing", EmptyPayload{}, &echo,
	); CodeOf(err) != CodeNotFound {
		t.Fatalf("missing call error = %v, want NotFound", err)
	}
	if err := client.Call(
		context.Background(), "unknown-domain", 1, "read", EmptyPayload{}, &echo,
	); CodeOf(err) != CodeUnsupported {
		t.Fatalf("unknown-domain error = %v, want Unsupported", err)
	}
}

func TestAuthenticationEpochAndDeadlineFailBeforeDispatch(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	var calls atomic.Int64
	server, err := StartServer(context.Background(), ServerOptions{
		Home: home,
		Handler: HandlerFunc(func(context.Context, Request) (any, error) {
			calls.Add(1)
			return echoResponse{Value: "unexpected"}, nil
		}),
	})
	if err != nil {
		t.Fatalf("StartServer() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})

	wrongToken := server.Manifest()
	wrongToken.Token = "00" + wrongToken.Token[2:]
	if wrongToken.Token == server.Manifest().Token {
		wrongToken.Token = "11" + wrongToken.Token[2:]
	}
	client, err := ConnectWithManifest(home, wrongToken)
	if err != nil {
		t.Fatalf("ConnectWithManifest(wrong token) error = %v", err)
	}
	if _, err := client.Ping(context.Background()); CodeOf(err) != CodeUnauthorized {
		t.Fatalf("wrong-token Ping() error = %v, want Unauthorized", err)
	}

	staleEpoch := server.Manifest()
	staleEpoch.Epoch = "00" + staleEpoch.Epoch[2:]
	if staleEpoch.Epoch == server.Manifest().Epoch {
		staleEpoch.Epoch = "11" + staleEpoch.Epoch[2:]
	}
	client, err = ConnectWithManifest(home, staleEpoch)
	if err != nil {
		t.Fatalf("ConnectWithManifest(stale epoch) error = %v", err)
	}
	if _, err := client.Ping(context.Background()); CodeOf(err) != CodeConflict {
		t.Fatalf("stale-epoch Ping() error = %v, want Conflict", err)
	}

	liveClient, err := Connect(home)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	var canceledOutput echoResponse
	if err := liveClient.Call(
		canceled, "test-domain", 1, "read", EmptyPayload{}, &canceledOutput,
	); CodeOf(err) != CodeDeadline {
		t.Fatalf("canceled call error = %v, want Deadline", err)
	}

	response := exchangeEnvelope(t, server.Manifest(), RequestEnvelope{
		Protocol: ProtocolVersion, RequestID: "expired-request", Token: server.Manifest().Token,
		BrokerEpoch: server.Manifest().Epoch, Domain: "test-domain", DomainVersion: 1,
		Operation: "read", DeadlineUnixNs: time.Now().Add(-time.Second).UnixNano(),
		Payload: mustPayload(t, EmptyPayload{}),
	})
	if response.Error == nil || response.Error.Code != CodeDeadline {
		t.Fatalf("expired response = %#v, want Deadline", response)
	}
	if calls.Load() != 0 {
		t.Fatalf("handler called %d times for rejected requests", calls.Load())
	}
}

func TestProtocolMismatchReturnsStructuredUnsupported(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	server, err := StartServer(context.Background(), ServerOptions{Home: home})
	if err != nil {
		t.Fatalf("StartServer() error = %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Close(ctx)
	}()
	response := exchangeEnvelope(t, server.Manifest(), RequestEnvelope{
		Protocol: ProtocolVersion + 1, RequestID: "version-request", Token: server.Manifest().Token,
		BrokerEpoch: server.Manifest().Epoch, Domain: ControlDomain, DomainVersion: ControlVersion,
		Operation: ControlOperationPing, DeadlineUnixNs: time.Now().Add(time.Second).UnixNano(),
		Payload: mustPayload(t, EmptyPayload{}),
	})
	if response.Error == nil || response.Error.Code != CodeUnsupported {
		t.Fatalf("protocol mismatch response = %#v", response)
	}
}

func TestControlledShutdownKeepsFenceThroughCallback(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	callbackStarted := make(chan struct{})
	releaseCallback := make(chan struct{})
	server, err := StartServer(context.Background(), ServerOptions{
		Home: home,
		OnShutdownRequested: func() {
			close(callbackStarted)
			<-releaseCallback
		},
	})
	if err != nil {
		t.Fatalf("StartServer() error = %v", err)
	}
	client, err := Connect(home)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	if err := client.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case <-callbackStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown callback did not start")
	}
	if fence, err := AcquireMigrationFence(home); fence != nil || CodeOf(err) != CodeConflict {
		t.Fatalf("migration fence during shutdown callback = %#v, %v", fence, err)
	}
	close(releaseCallback)
	select {
	case <-server.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("server did not finish controlled shutdown")
	}
	fence, err := AcquireMigrationFence(home)
	if err != nil {
		t.Fatalf("migration fence after shutdown = %v", err)
	}
	_ = fence.Close()
}

func TestStatusRejectsDuplicateCatalogIDs(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	server, err := StartServer(context.Background(), ServerOptions{
		Home: home,
		StatusProvider: func(context.Context) ([]StoreStatus, error) {
			return []StoreStatus{
				{ID: "global/auth", Readiness: StoreReady},
				{ID: "global/auth", Readiness: StoreUnavailable},
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("StartServer() error = %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Close(ctx)
	}()
	client, _ := Connect(home)
	if _, err := client.Status(context.Background()); CodeOf(err) != CodeIntegrity {
		t.Fatalf("duplicate catalog Status() error = %v, want Integrity", err)
	}
}

func TestMutationDisconnectReturnsOutcomeUnknown(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	stateDir, err := prepareStateDirectory(home)
	if err != nil {
		t.Fatalf("prepareStateDirectory() error = %v", err)
	}
	endpoint := endpointForStateDirectory(stateDir)
	if err := prepareEndpoint(endpoint); err != nil {
		t.Fatalf("prepareEndpoint() error = %v", err)
	}
	listener, err := listenLocal(endpoint)
	if err != nil {
		t.Fatalf("listenLocal() error = %v", err)
	}
	defer listener.Close()
	token, _ := randomHex(tokenBytes)
	epoch, _ := randomHex(epochBytes)
	manifest := Manifest{
		PID: os.Getpid(), Protocol: ProtocolVersion, Token: token, Endpoint: endpoint, Epoch: epoch,
	}
	if err := writeManifest(stateDir, manifest); err != nil {
		t.Fatalf("writeManifest() error = %v", err)
	}
	defer os.Remove(filepath.Join(stateDir, manifestFileName))
	accepted := make(chan struct{})
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			var request RequestEnvelope
			_ = readFrameStrict(connection, &request)
			_ = connection.Close()
		}
		close(accepted)
	}()
	client, err := Connect(home)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	var output echoResponse
	err = client.CallWithOptions(
		context.Background(), "test-domain", 1, "mutate", echoRequest{Value: "x"}, &output,
		CallOptions{Mutation: true, IdempotencyKey: "mutation-1"},
	)
	if CodeOf(err) != CodeOutcomeUnknown {
		t.Fatalf("disconnect mutation error = %v, want OutcomeUnknown", err)
	}
	<-accepted
}

func exchangeEnvelope(t *testing.T, manifest Manifest, request RequestEnvelope) ResponseEnvelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	connection, err := dialLocal(ctx, manifest.Endpoint)
	if err != nil {
		t.Fatalf("dialLocal() error = %v", err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
	if err := WriteFrame(connection, request); err != nil {
		t.Fatalf("WriteFrame() error = %v", err)
	}
	var response ResponseEnvelope
	if err := readFrameStrict(connection, &response); err != nil {
		t.Fatalf("read response frame: %v", err)
	}
	return response
}

func mustPayload(t *testing.T, value any) []byte {
	t.Helper()
	payload, err := marshalPayload(value)
	if err != nil {
		t.Fatalf("marshalPayload() error = %v", err)
	}
	return payload
}

func TestConnectWithManifestRejectsForeignEndpoint(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if _, err := PrepareHome(home); err != nil {
		t.Fatalf("PrepareHome() error = %v", err)
	}
	if _, err := prepareStateDirectory(home); err != nil {
		t.Fatalf("prepareStateDirectory() error = %v", err)
	}
	token, _ := randomHex(tokenBytes)
	epoch, _ := randomHex(epochBytes)
	_, err := ConnectWithManifest(home, Manifest{
		PID: 1, Protocol: ProtocolVersion, Token: token,
		Endpoint: filepath.Join(t.TempDir(), "foreign.sock"), Epoch: epoch,
	})
	if CodeOf(err) != CodeIntegrity {
		t.Fatalf("foreign endpoint error = %v, want Integrity", err)
	}
}

func TestParentCancellationClosesServer(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	parent, cancel := context.WithCancel(context.Background())
	server, err := StartServer(parent, ServerOptions{Home: home})
	if err != nil {
		t.Fatalf("StartServer() error = %v", err)
	}
	cancel()
	select {
	case <-server.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("parent cancellation did not close server")
	}
	if _, err := Connect(home); CodeOf(err) != CodeUnavailable {
		t.Fatalf("Connect() after parent cancellation = %v, want Unavailable", err)
	}
}

func TestShutdownInterruptsIdleUnauthenticatedConnection(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	server, err := StartServer(context.Background(), ServerOptions{Home: home})
	if err != nil {
		t.Fatalf("StartServer() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	connection, err := dialLocal(ctx, server.Manifest().Endpoint)
	if err != nil {
		t.Fatalf("dialLocal() error = %v", err)
	}
	defer connection.Close()
	// Let Accept publish the connection worker before initiating shutdown.
	time.Sleep(10 * time.Millisecond)
	if err := server.Close(ctx); err != nil {
		t.Fatalf("Close() with idle connection error = %v", err)
	}
}

func TestCloseHandlerRunsBeforeOnlineFenceRelease(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	closeStarted := make(chan struct{})
	releaseClose := make(chan struct{})
	server, err := StartServer(context.Background(), ServerOptions{
		Home: home,
		CloseHandler: func() error {
			close(closeStarted)
			<-releaseClose
			return NewError(CodeInternal, "test close failure")
		},
	})
	if err != nil {
		t.Fatalf("StartServer() error = %v", err)
	}
	closeResult := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		closeResult <- server.Close(ctx)
	}()
	select {
	case <-closeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("CloseHandler did not start")
	}
	if fence, err := AcquireMigrationFence(home); fence != nil || CodeOf(err) != CodeConflict {
		t.Fatalf("migration fence while CloseHandler runs = %#v, %v", fence, err)
	}
	select {
	case <-server.Done():
		t.Fatal("Server.Done closed before CloseHandler completed")
	default:
	}
	close(releaseClose)
	if err := <-closeResult; CodeOf(err) != CodeInternal {
		t.Fatalf("Close() error = %v, want joined CloseHandler error", err)
	}
	select {
	case <-server.Done():
	default:
		t.Fatal("Server.Done remained open after cleanup")
	}
	fence, err := AcquireMigrationFence(home)
	if err != nil {
		t.Fatalf("migration fence after CloseHandler = %v", err)
	}
	_ = fence.Close()
}
