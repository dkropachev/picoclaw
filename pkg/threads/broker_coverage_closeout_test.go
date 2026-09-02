//nolint:govet // Independent broker assertions intentionally use narrow error scopes.
package threads

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/session"
)

func TestThreadBrokerCompleteTypedOperationLifecycle(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	cfg := &config.Config{Agents: config.AgentsConfig{
		Defaults: config.AgentDefaults{Workspace: workspace},
	}}
	handler, err := NewBrokerHandler(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handler.Close() })

	selector, err := memory.WorkspaceSelector(ResolveSessionsDir(workspace))
	if err != nil {
		t.Fatal(err)
	}
	resolved := threadBrokerCall[memory.StoreResolutionResponse](
		t,
		handler,
		memory.SessionOperationResolveStore,
		memory.StoreResolutionRequest{WorkspaceSelector: selector},
	)
	if resolved.StoreID != memory.SessionsStoreID {
		t.Fatalf("resolved thread store = %q", resolved.StoreID)
	}
	preflight := threadBrokerCall[threadBrokerResponse](
		t,
		handler,
		BrokerPreflightOperation,
		threadStoreRequest{StoreID: memory.SessionsStoreID},
	)
	if !preflight.OK {
		t.Fatalf("thread preflight = %#v", preflight)
	}
	ping := threadBrokerCall[threadBrokerResponse](
		t,
		handler,
		threadOperationPing,
		threadStoreRequest{StoreID: memory.SessionsStoreID},
	)
	if !ping.OK {
		t.Fatalf("thread ping = %#v", ping)
	}

	for _, request := range []CreateRequest{
		{
			ID: "thread-one", PrimarySessionKey: "session-one", Title: "First",
			Type: TypeCoding, Context: map[string]string{"repo": "one"},
		},
		{
			ID: "thread-two", PrimarySessionKey: "session-two", Title: "Second",
			Type: TypeReviewing, Context: map[string]string{"repo": "two"},
		},
	} {
		created := threadBrokerCall[threadBrokerResponse](
			t,
			handler,
			threadOperationCreate,
			threadCreateRequest{StoreID: memory.SessionsStoreID, Request: request},
		)
		if !created.Found || created.Thread == nil || created.Thread.ID != request.ID {
			t.Fatalf("created thread = %#v", created)
		}
	}
	search := threadBrokerCall[threadBrokerResponse](
		t,
		handler,
		threadOperationSearch,
		threadSearchRequest{
			StoreID: memory.SessionsStoreID,
			Options: SearchOptions{Query: "First", Limit: 10},
		},
	)
	if len(search.Threads) != 1 || search.Threads[0].ID != "thread-one" {
		t.Fatalf("thread search = %#v", search)
	}
	listed := threadBrokerCall[threadBrokerResponse](
		t,
		handler,
		threadOperationList,
		threadListRequest{
			StoreID: memory.SessionsStoreID,
			Options: ListOptions{IncludeDropped: true}, Offset: 0, Limit: 1,
		},
	)
	if len(listed.Threads) != 1 || listed.Next != 1 {
		t.Fatalf("thread list page = %#v", listed)
	}
	got := threadBrokerCall[threadBrokerResponse](
		t,
		handler,
		threadOperationGet,
		threadIDRequest{StoreID: memory.SessionsStoreID, ID: "thread-one"},
	)
	if !got.Found || got.Thread == nil {
		t.Fatalf("thread get = %#v", got)
	}
	meta := threadBrokerCall[threadBrokerResponse](
		t,
		handler,
		threadOperationGetMeta,
		threadIDRequest{StoreID: memory.SessionsStoreID, ID: "thread-one"},
	)
	if !meta.Found || meta.Meta == nil || meta.Meta.ID != "thread-one" {
		t.Fatalf("thread metadata = %#v", meta)
	}
	discoverable := false
	updated := threadBrokerCall[threadBrokerResponse](
		t,
		handler,
		threadOperationUpdate,
		threadUpdateRequest{
			StoreID: memory.SessionsStoreID, ID: "thread-one",
			Request: UpdateRequest{Title: "Updated", Discoverable: &discoverable},
		},
	)
	if !updated.Found || updated.Thread == nil || updated.Thread.Title != "Updated" {
		t.Fatalf("thread update = %#v", updated)
	}

	local := handler.workspaces[memory.SessionsStoreID].adapter.LocalStore()
	if err := local.EnsureSessionHistory(t.Context(), "origin-session"); err != nil {
		t.Fatal(err)
	}
	attached := threadBrokerCall[threadBrokerResponse](
		t,
		handler,
		threadOperationAttach,
		threadAttachRequest{
			StoreID: memory.SessionsStoreID,
			Request: AttachRequest{
				ThreadID: "thread-one", SessionKey: "origin-session", AgentID: "main",
				OwnerIdentity: "owner", OriginSessionID: "origin-ui", Summary: "handoff",
				Scope: &session.SessionScope{AgentID: "main", Channel: "pico"},
			},
		},
	)
	if !attached.Found || attached.Thread == nil || attached.Handoff == nil {
		t.Fatalf("thread attach = %#v", attached)
	}
	returned := threadBrokerCall[threadBrokerResponse](
		t,
		handler,
		threadOperationReturnOrigin,
		threadIDRequest{StoreID: memory.SessionsStoreID, ID: attached.Handoff.ID},
	)
	if !returned.Found || returned.Handoff == nil {
		t.Fatalf("thread return-to-origin = %#v", returned)
	}
	threadBrokerCall[threadBrokerResponse](
		t,
		handler,
		threadOperationDetach,
		threadDetachRequest{StoreID: memory.SessionsStoreID, SessionKey: "origin-session"},
	)

	allocation := PicoAllocation{
		SessionID: "pico-ui", Key: "pico-session", AgentID: "main",
		Scope:   session.SessionScope{AgentID: "main", Channel: "pico"},
		Aliases: []string{"pico-alias"},
	}
	pico := threadBrokerCall[threadBrokerResponse](
		t,
		handler,
		threadOperationCreatePico,
		threadCreatePicoRequest{
			StoreID: memory.SessionsStoreID, Allocation: allocation,
			Request: CreateRequest{ID: "pico-thread", Title: "Pico"},
		},
	)
	if !pico.Found || pico.Thread == nil {
		t.Fatalf("pico thread = %#v", pico)
	}

	if _, err := threadBrokerHandle(
		t.Context(), handler, "thread.unknown",
		threadStoreRequest{StoreID: memory.SessionsStoreID},
	); database.CodeOf(err) != database.CodeUnsupported {
		t.Fatalf("unknown thread operation error = %v", err)
	}
}

func TestThreadBrokerValidationCloseAndMappingBoundaries(t *testing.T) {
	if _, err := (*brokerWorkspace)(nil).ensureStore(); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("nil broker workspace error = %v", err)
	}
	if err := (*BrokerHandler)(nil).Close(); err != nil {
		t.Fatalf("nil broker close = %v", err)
	}
	_, err := (*BrokerHandler)(nil).Handle(t.Context(), database.Request{})
	if database.CodeOf(err) != database.CodeUnsupported {
		t.Fatalf("nil broker Handle error = %v", err)
	}
	for _, value := range []string{"", " spaced ", strings.Repeat("x", 4097), "bad\x00id"} {
		if validThreadIdentity(value) {
			t.Errorf("invalid thread identity accepted: %q", value)
		}
	}
	if !validThreadIdentity("valid") || !validThreadStoreID(memory.SessionsStoreID) {
		t.Fatal("valid thread identity rejected")
	}
	for _, test := range []struct {
		err  error
		code database.ErrorCode
	}{
		{nil, ""},
		{database.NewError(database.CodeConflict, "conflict"), database.CodeConflict},
		{context.Canceled, database.CodeDeadline},
		{errors.New("missing: " + filepath.Base("file")), database.CodeInternal},
		{errSessionMissing, database.CodeNotFound},
		{errReviewScope, database.CodeUnauthorized},
	} {
		if got := database.CodeOf(mapThreadBrokerError(test.err)); got != test.code {
			t.Errorf("mapThreadBrokerError(%v) = %q, want %q", test.err, got, test.code)
		}
	}
	for _, payload := range [][]byte{nil, []byte(`{}`), []byte(`{"store_id":"../bad"}`)} {
		if _, err := requestStoreID(database.Request{Payload: payload}); database.CodeOf(err) != database.CodeInvalid {
			t.Fatalf("requestStoreID(%s) error = %v", payload, err)
		}
	}
}

func TestThreadBrokerRejectsMalformedTypedOperations(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	handler, err := NewBrokerHandler(home, &config.Config{Agents: config.AgentsConfig{
		Defaults: config.AgentDefaults{Workspace: workspace},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handler.Close() })

	for name, test := range map[string]struct {
		operation string
		input     any
		code      database.ErrorCode
	}{
		"resolve empty": {
			memory.SessionOperationResolveStore, memory.StoreResolutionRequest{}, database.CodeInvalid,
		},
		"resolve unknown": {
			memory.SessionOperationResolveStore,
			memory.StoreResolutionRequest{WorkspaceSelector: "unknown"},
			database.CodeUnauthorized,
		},
		"preflight empty": {
			BrokerPreflightOperation, threadStoreRequest{}, database.CodeInvalid,
		},
		"preflight unknown": {
			BrokerPreflightOperation,
			threadStoreRequest{StoreID: "workspace.unknown.sessions"},
			database.CodeUnauthorized,
		},
		"ping": {
			threadOperationPing, threadStoreRequest{StoreID: "wrong"}, database.CodeUnauthorized,
		},
		"search": {
			threadOperationSearch,
			threadSearchRequest{
				StoreID: memory.SessionsStoreID, Options: SearchOptions{Offset: -1},
			},
			database.CodeInvalid,
		},
		"list": {
			threadOperationList,
			threadListRequest{StoreID: memory.SessionsStoreID, Limit: 0},
			database.CodeInvalid,
		},
		"get": {
			threadOperationGet, threadIDRequest{StoreID: memory.SessionsStoreID}, database.CodeInvalid,
		},
		"get meta": {
			threadOperationGetMeta, threadIDRequest{StoreID: memory.SessionsStoreID}, database.CodeInvalid,
		},
		"update": {
			threadOperationUpdate,
			threadUpdateRequest{StoreID: memory.SessionsStoreID},
			database.CodeInvalid,
		},
		"detach": {
			threadOperationDetach,
			threadDetachRequest{StoreID: memory.SessionsStoreID},
			database.CodeInvalid,
		},
		"return": {
			threadOperationReturnOrigin,
			threadIDRequest{StoreID: memory.SessionsStoreID},
			database.CodeInvalid,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := threadBrokerHandle(
				t.Context(), handler, test.operation, test.input,
			); database.CodeOf(err) != test.code {
				t.Fatalf("malformed %s error = %v, want %q", test.operation, err, test.code)
			}
		})
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := threadBrokerHandle(
		canceled,
		handler,
		threadOperationPing,
		threadStoreRequest{StoreID: memory.SessionsStoreID},
	); database.CodeOf(err) != database.CodeDeadline {
		t.Fatalf("canceled thread request error = %v", err)
	}
	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := threadBrokerHandle(
		t.Context(),
		handler,
		threadOperationPing,
		threadStoreRequest{StoreID: memory.SessionsStoreID},
	); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("closed thread request error = %v", err)
	}
}

func TestThreadBrokerClientRejectsMalformedResponses(t *testing.T) {
	home := t.TempDir()
	handler := database.HandlerFunc(func(_ context.Context, request database.Request) (any, error) {
		switch request.Operation {
		case threadOperationSearch:
			return threadBrokerResponse{Threads: []Thread{{ID: "search"}}}, nil
		case threadOperationList:
			return threadBrokerResponse{Next: -1}, nil
		case threadOperationGet, threadOperationGetMeta, threadOperationCreate:
			return threadBrokerResponse{Found: true}, nil
		default:
			return threadBrokerResponse{OK: true}, nil
		}
	})
	server, err := database.StartServer(t.Context(), database.ServerOptions{Home: home, Handler: handler})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	store := Store{brokerClient: client, brokerStoreID: memory.SessionsStoreID}
	if items, err := store.brokerSearch(t.Context(), SearchOptions{}); err != nil ||
		len(items) != 1 || items[0].ID != "search" {
		t.Fatalf("broker search = %#v, %v", items, err)
	}
	if _, err := store.brokerList(t.Context(), ListOptions{}); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("invalid broker page error = %v", err)
	}
	if _, found, err := store.brokerGet(t.Context(), "id"); found ||
		database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("malformed broker get = found:%t err:%v", found, err)
	}
	if _, found, err := store.brokerGetMeta(t.Context(), "id"); found ||
		database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("malformed broker metadata = found:%t err:%v", found, err)
	}
	if _, found, err := store.brokerThreadMutation(
		t.Context(), threadOperationCreate,
		threadCreateRequest{StoreID: memory.SessionsStoreID},
	); found || database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("malformed broker mutation = found:%t err:%v", found, err)
	}

	if _, err := (Store{}).resolvedBrokerStoreID(t.Context()); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("missing broker client resolution error = %v", err)
	}
	want := errors.New("resolution failed")
	if _, err := (Store{brokerResolveErr: want}).resolvedBrokerStoreID(t.Context()); !errors.Is(err, want) {
		t.Fatalf("captured resolution error = %v", err)
	}
	err = (Store{}).callBroker(t.Context(), "read", nil, nil, false)
	if database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("missing broker call error = %v", err)
	}
}

func threadBrokerCall[T any](
	t *testing.T,
	handler *BrokerHandler,
	operation string,
	input any,
) T {
	t.Helper()
	result, err := threadBrokerHandle(t.Context(), handler, operation, input)
	if err != nil {
		t.Fatalf("thread broker %s: %v", operation, err)
	}
	response, ok := result.(T)
	if !ok {
		t.Fatalf("thread broker %s response = %T", operation, result)
	}
	return response
}

func threadBrokerHandle(
	ctx context.Context,
	handler *BrokerHandler,
	operation string,
	input any,
) (any, error) {
	payload, err := database.MarshalCanonical(input)
	if err != nil {
		return nil, err
	}
	return handler.Handle(ctx, database.Request{
		Domain: memory.SessionsBrokerDomain, Version: memory.SessionsBrokerVersion,
		Operation: operation, Payload: payload,
	})
}
