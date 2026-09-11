//go:build unix

package database_test

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	integrationStoreID = database.StoreID("workspace/repository-reviews")
	integrationDomain  = "repository-reviews"
)

type scopedEchoRequest struct {
	Value string `json:"value"`
}

type scopedEchoResponse struct {
	Value string `json:"value"`
}

type scopedIdempotencyResponse struct {
	Count int64  `json:"count"`
	Value string `json:"value"`
}

func TestScopedStoreRoundTripRediscoveryAndAdmission(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	bindings := []database.StoreBinding{{ID: integrationStoreID, Domain: integrationDomain}}
	statuses := func(context.Context) ([]database.StoreStatus, error) {
		return []database.StoreStatus{{ID: integrationStoreID, Readiness: database.StoreReady}}, nil
	}

	var firstCalls atomic.Int64
	first := startScopedIntegrationServer(t, home, bindings, statuses, &firstCalls, nil)
	t.Cleanup(func() { closeScopedIntegrationServer(t, first) })
	client, err := database.Connect(home)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	assertScopedEcho(t, client, "first")
	if firstCalls.Load() != 1 {
		t.Fatalf("first handler calls = %d, want 1", firstCalls.Load())
	}
	closeScopedIntegrationServer(t, first)

	var secondCalls atomic.Int64
	var idempotencyChecks atomic.Int64
	second := startScopedIntegrationServer(
		t,
		home,
		bindings,
		statuses,
		&secondCalls,
		func(string, int, string) bool {
			idempotencyChecks.Add(1)
			return true
		},
	)
	t.Cleanup(func() { closeScopedIntegrationServer(t, second) })

	// The already-connected discovery client must retain the exact StoreID when
	// it observes the replacement broker epoch and performs the read there.
	assertScopedEcho(t, client, "replacement")
	if secondCalls.Load() != 1 {
		t.Fatalf("replacement handler calls = %d, want 1", secondCalls.Load())
	}
	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if len(status.RequiredStores) != 1 || status.RequiredStores[0] != integrationStoreID ||
		len(status.Stores) != 1 || status.Stores[0].ID != integrationStoreID {
		t.Fatalf("scoped Status() = %#v", status)
	}

	rejected := []struct {
		name    string
		key     string
		storeID database.StoreID
		domain  string
		code    database.ErrorCode
	}{
		{name: "missing", key: "missing", domain: integrationDomain, code: database.CodeInvalid},
		{
			name: "unknown", key: "unknown", storeID: "workspace/repository-evaluations",
			domain: integrationDomain, code: database.CodeUnsupported,
		},
		{
			name: "wrong domain", key: "wrong-domain", storeID: integrationStoreID,
			domain: "repository-evaluations", code: database.CodeUnsupported,
		},
	}
	for _, test := range rejected {
		t.Run(test.name, func(t *testing.T) {
			var output scopedEchoResponse
			err := client.CallWithOptions(
				context.Background(),
				test.domain,
				1,
				"mutate",
				scopedEchoRequest{Value: "must-not-dispatch"},
				&output,
				database.CallOptions{
					StoreID: test.storeID, Mutation: true,
					IdempotencyKey: "rejected-" + test.key,
				},
			)
			if database.CodeOf(err) != test.code {
				t.Fatalf("rejected call error = %v, want %s", err, test.code)
			}
		})
	}
	if secondCalls.Load() != 1 {
		t.Fatalf("handler calls after rejected requests = %d, want 1", secondCalls.Load())
	}
	if idempotencyChecks.Load() != 0 {
		t.Fatalf("idempotency policy reached by rejected requests %d times", idempotencyChecks.Load())
	}
}

func TestCallStoreConflictRetryRetainsExactTarget(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	storeIDs := make(chan database.StoreID, 2)
	var calls atomic.Int64
	server, err := database.StartServer(context.Background(), database.ServerOptions{
		Home: home,
		ServedStores: []database.StoreBinding{{
			ID: integrationStoreID, Domain: integrationDomain,
		}},
		Handler: database.HandlerFunc(func(_ context.Context, request database.Request) (any, error) {
			storeIDs <- request.StoreID
			if calls.Add(1) == 1 {
				return nil, database.NewError(database.CodeConflict, "retry scoped read")
			}
			return scopedEchoResponse{Value: "retried"}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeScopedIntegrationServer(t, server) })
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	var response scopedEchoResponse
	if err := client.CallStore(
		context.Background(), integrationStoreID, integrationDomain, 1, "read",
		scopedEchoRequest{Value: "request"}, &response,
	); err != nil {
		t.Fatal(err)
	}
	if response.Value != "retried" || calls.Load() != 2 {
		t.Fatalf("retried response/calls = %#v/%d", response, calls.Load())
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if storeID := <-storeIDs; storeID != integrationStoreID {
			t.Fatalf("attempt %d StoreID = %q, want %q", attempt, storeID, integrationStoreID)
		}
	}
}

func TestCallStoreWithOptionsReplaysFreshClientRequests(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	var calls atomic.Int64
	server, err := database.StartServer(context.Background(), database.ServerOptions{
		Home: home,
		ServedStores: []database.StoreBinding{{
			ID: integrationStoreID, Domain: integrationDomain,
		}},
		Handler: database.HandlerFunc(func(_ context.Context, request database.Request) (any, error) {
			var input scopedEchoRequest
			if err := request.DecodePayload(&input); err != nil {
				return nil, database.NewError(database.CodeInvalid, "scoped mutation payload is invalid")
			}
			return scopedIdempotencyResponse{Count: calls.Add(1), Value: input.Value}, nil
		}),
		AllowsIdempotency: func(domain string, version int, operation string) bool {
			return domain == integrationDomain && version == 1 && operation == "mutate"
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeScopedIntegrationServer(t, server) })
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	call := func(output *scopedIdempotencyResponse) error {
		return client.CallStoreWithOptions(
			context.Background(), integrationStoreID, integrationDomain, 1, "mutate",
			scopedEchoRequest{Value: "semantic-mutation"}, output,
			database.CallOptions{Mutation: true, IdempotencyKey: "stable-client-call"},
		)
	}
	var first, second scopedIdempotencyResponse
	if err := call(&first); err != nil {
		t.Fatalf("first CallStoreWithOptions() error = %v", err)
	}
	if err := call(&second); err != nil {
		t.Fatalf("fresh CallStoreWithOptions() replay error = %v", err)
	}
	if first != (scopedIdempotencyResponse{Count: 1, Value: "semantic-mutation"}) ||
		second != first || calls.Load() != 1 {
		t.Fatalf("fresh client results/calls = %#v, %#v/%d", first, second, calls.Load())
	}
}

func startScopedIntegrationServer(
	t *testing.T,
	home string,
	bindings []database.StoreBinding,
	statuses database.StatusProvider,
	calls *atomic.Int64,
	allowsIdempotency func(string, int, string) bool,
) *database.Server {
	t.Helper()
	handler := database.HandlerFunc(func(_ context.Context, request database.Request) (any, error) {
		calls.Add(1)
		if request.StoreID != integrationStoreID || request.Domain != integrationDomain ||
			request.Version != 1 || request.Operation != "read" {
			return nil, database.NewError(database.CodeInvalid, "scoped echo request is invalid")
		}
		var input scopedEchoRequest
		if err := request.DecodePayload(&input); err != nil {
			return nil, database.NewError(database.CodeInvalid, "scoped echo payload is invalid")
		}
		return scopedEchoResponse{Value: input.Value}, nil
	})
	server, err := database.StartServer(context.Background(), database.ServerOptions{
		Home: home, ServedStores: bindings, RequiredStores: []database.StoreID{integrationStoreID},
		StatusProvider: statuses, Handler: handler, AllowsIdempotency: allowsIdempotency,
	})
	if err != nil {
		t.Fatalf("StartServer() error = %v", err)
	}
	return server
}

func assertScopedEcho(t *testing.T, client *database.Client, value string) {
	t.Helper()
	var response scopedEchoResponse
	if err := client.CallStore(
		context.Background(), integrationStoreID, integrationDomain, 1, "read",
		scopedEchoRequest{Value: value}, &response,
	); err != nil {
		t.Fatalf("CallStore() error = %v", err)
	}
	if response.Value != value {
		t.Fatalf("CallStore() response = %#v, want value %q", response, value)
	}
}

func closeScopedIntegrationServer(t *testing.T, server *database.Server) {
	t.Helper()
	if server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Close(ctx); err != nil {
		t.Fatalf("Server.Close() error = %v", err)
	}
}
