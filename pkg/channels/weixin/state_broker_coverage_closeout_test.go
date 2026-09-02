package weixin

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestWeixinBrokerInvalidRequestAndErrorMatrix(t *testing.T) {
	handler := NewBrokerHandler(t.TempDir())
	t.Cleanup(func() { _ = handler.Close() })
	call := func(operation string, input any) error {
		t.Helper()
		payload, err := database.MarshalCanonical(input)
		if err != nil {
			t.Fatal(err)
		}
		_, err = handler.Handle(t.Context(), database.Request{
			Domain: weixinBrokerDomain, Version: weixinBrokerVersion,
			Operation: operation, Payload: payload,
		})
		return err
	}
	invalid := []struct {
		operation string
		input     any
	}{
		{weixinBrokerOperationPreflight, weixinBrokerTarget{StoreID: "workspace.bad"}},
		{weixinBrokerOperationLoadCursor, weixinBrokerAccountRequest{StoreID: WeixinStoreID, AccountKey: "bad/key"}},
		{weixinBrokerOperationSaveCursor, weixinBrokerCursorRequest{StoreID: WeixinStoreID, AccountKey: "bad/key"}},
		{
			weixinBrokerOperationLoadTokens,
			weixinBrokerTokenPageRequest{StoreID: WeixinStoreID, AccountKey: brokerTestAccount},
		},
		{
			weixinBrokerOperationLoadTokens,
			weixinBrokerTokenPageRequest{
				StoreID:    WeixinStoreID,
				AccountKey: brokerTestAccount,
				Limit:      weixinBrokerTokenPageSize + 1,
			},
		},
		{
			weixinBrokerOperationReplaceTokens,
			weixinBrokerTokensRequest{
				StoreID:    WeixinStoreID,
				AccountKey: brokerTestAccount,
				Tokens:     map[string]string{"bad\x00": "x"},
			},
		},
		{
			weixinBrokerOperationSaveToken,
			weixinBrokerTokenRequest{StoreID: WeixinStoreID, AccountKey: brokerTestAccount, UserID: ""},
		},
	}
	for _, test := range invalid {
		if err := call(test.operation, test.input); database.CodeOf(err) != database.CodeInvalid {
			t.Errorf("%s invalid error = %v", test.operation, err)
		}
	}
	if err := call("unknown", weixinBrokerTarget{StoreID: WeixinStoreID}); database.CodeOf(
		err,
	) != database.CodeUnsupported {
		t.Fatalf("unknown operation error = %v", err)
	}
	if _, err := (*BrokerHandler)(nil).Handle(t.Context(), database.Request{}); database.CodeOf(
		err,
	) != database.CodeUnsupported {
		t.Fatalf("nil handler error = %v", err)
	}
	if got := (*weixinStateStore)(nil).forAccount("account"); got != nil {
		t.Fatalf("nil forAccount = %#v", got)
	}
	if !validWeixinBrokerAccount(weixinBrokerAccountRequest{StoreID: WeixinStoreID, AccountKey: brokerTestAccount}) ||
		validWeixinBrokerAccount(weixinBrokerAccountRequest{StoreID: WeixinStoreID, AccountKey: "bad/key"}) {
		t.Fatal("account validation mismatch")
	}
	if validateWeixinMutationResponse(weixinBrokerMutationResponse{}, nil) == nil ||
		validateWeixinMutationResponse(weixinBrokerMutationResponse{Updated: true}, nil) != nil ||
		!errors.Is(validateWeixinMutationResponse(weixinBrokerMutationResponse{}, context.Canceled), context.Canceled) {
		t.Fatal("mutation response validation mismatch")
	}

	cases := []struct {
		err  error
		code database.ErrorCode
	}{
		{database.NewError(database.CodeConflict, "conflict"), database.CodeConflict},
		{context.Canceled, database.CodeDeadline},
		{sqlitestore.ErrTooNew, database.CodeUnsupported},
		{sqlitestore.ErrInvalidSchema, database.CodeIntegrity},
		{sqlitestore.ErrIntegrity, database.CodeIntegrity},
		{os.ErrPermission, database.CodeUnavailable},
		{errors.New("boom"), database.CodeInternal},
	}
	if mapWeixinBrokerError(nil) != nil {
		t.Fatal("nil Weixin error mapped non-nil")
	}
	for _, test := range cases {
		if got := database.CodeOf(mapWeixinBrokerError(test.err)); got != test.code {
			t.Errorf("map(%v) = %s, want %s", test.err, got, test.code)
		}
	}
	if database.CodeOf(RunOfflineDatabaseMigration(t.TempDir())) != database.CodeConflict {
		t.Fatal("unfenced Weixin migration accepted")
	}
	if err := (*BrokerHandler)(nil).Close(); err != nil {
		t.Fatalf("nil close = %v", err)
	}
}
