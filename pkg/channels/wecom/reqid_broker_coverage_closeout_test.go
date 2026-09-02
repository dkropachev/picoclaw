package wecom

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestWeComBrokerHandlerOperationAndErrorMatrix(t *testing.T) {
	handler := NewBrokerHandler(t.TempDir())
	t.Cleanup(func() { _ = handler.Close() })
	call := func(operation string, input any) (any, error) {
		t.Helper()
		payload, err := database.MarshalCanonical(input)
		if err != nil {
			t.Fatal(err)
		}
		return handler.Handle(t.Context(), database.Request{
			Domain: wecomBrokerDomain, Version: wecomBrokerVersion,
			Operation: operation, Payload: payload,
		})
	}
	if value, err := call(wecomBrokerOperationPreflight, wecomBrokerTarget{StoreID: WeComStoreID}); err != nil ||
		!value.(wecomBrokerReadyResponse).Ready {
		t.Fatalf("preflight = %#v, %v", value, err)
	}
	if value, err := call(wecomBrokerOperationPut, wecomBrokerPutRequest{
		StoreID: WeComStoreID, ChatID: "chat", RequestID: "request", ChatType: 2,
		TTLNanoSeconds: int64(time.Hour),
	}); err != nil || !value.(wecomBrokerMutationResponse).Updated {
		t.Fatalf("put = %#v, %v", value, err)
	}
	value, err := call(wecomBrokerOperationGet, wecomBrokerChatRequest{StoreID: WeComStoreID, ChatID: "chat"})
	if err != nil || !value.(wecomBrokerGetResponse).Found || value.(wecomBrokerGetResponse).Route.ReqID != "request" {
		t.Fatalf("get = %#v, %v", value, err)
	}
	if value, err := call(wecomBrokerOperationDelete, wecomBrokerChatRequest{
		StoreID: WeComStoreID, ChatID: "chat",
	}); err != nil || !value.(wecomBrokerMutationResponse).Updated {
		t.Fatalf("delete = %#v, %v", value, err)
	}
	if value, err := call(wecomBrokerOperationGet, wecomBrokerChatRequest{
		StoreID: WeComStoreID, ChatID: "chat",
	}); err != nil || value.(wecomBrokerGetResponse).Found {
		t.Fatalf("missing get = %#v, %v", value, err)
	}
	invalid := []struct {
		operation string
		input     any
	}{
		{wecomBrokerOperationPreflight, wecomBrokerTarget{StoreID: "workspace.bad"}},
		{wecomBrokerOperationPut, wecomBrokerPutRequest{StoreID: WeComStoreID, ChatID: "chat"}},
		{wecomBrokerOperationPut, wecomBrokerPutRequest{StoreID: WeComStoreID, ChatID: "bad\x00", RequestID: "r"}},
		{wecomBrokerOperationGet, wecomBrokerChatRequest{StoreID: WeComStoreID, ChatID: "bad\x00"}},
		{wecomBrokerOperationDelete, wecomBrokerChatRequest{StoreID: "workspace.bad", ChatID: "chat"}},
	}
	for _, test := range invalid {
		if _, err := call(test.operation, test.input); database.CodeOf(err) != database.CodeInvalid {
			t.Errorf("%s invalid error = %v", test.operation, err)
		}
	}
	if _, err := call("unknown", wecomBrokerTarget{StoreID: WeComStoreID}); database.CodeOf(
		err,
	) != database.CodeUnsupported {
		t.Fatalf("unknown operation error = %v", err)
	}
	if _, err := (*BrokerHandler)(nil).Handle(t.Context(), database.Request{}); database.CodeOf(
		err,
	) != database.CodeUnsupported {
		t.Fatalf("nil handler error = %v", err)
	}

	cases := []struct {
		err  error
		code database.ErrorCode
	}{
		{database.NewError(database.CodeConflict, "conflict"), database.CodeConflict},
		{context.DeadlineExceeded, database.CodeDeadline},
		{sqlitestore.ErrTooNew, database.CodeUnsupported},
		{sqlitestore.ErrInvalidSchema, database.CodeIntegrity},
		{sqlitestore.ErrIntegrity, database.CodeIntegrity},
		{sql.ErrNoRows, database.CodeNotFound},
		{os.ErrPermission, database.CodeUnavailable},
		{errors.New("boom"), database.CodeInternal},
	}
	if mapWecomBrokerError(nil) != nil {
		t.Fatal("nil WeCom error mapped non-nil")
	}
	for _, test := range cases {
		if got := database.CodeOf(mapWecomBrokerError(test.err)); got != test.code {
			t.Errorf("map(%v) = %s, want %s", test.err, got, test.code)
		}
	}
	if database.CodeOf(RunOfflineDatabaseMigration(t.TempDir())) != database.CodeConflict {
		t.Fatal("unfenced WeCom migration accepted")
	}
	if err := (*BrokerHandler)(nil).Close(); err != nil {
		t.Fatalf("nil close = %v", err)
	}
}
