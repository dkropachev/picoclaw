//nolint:govet // Independent broker assertions intentionally reuse err.
package api

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestModelCatalogBrokerHandlerOperationMatrix(t *testing.T) {
	handler := NewModelCatalogBrokerHandler(t.TempDir())
	t.Cleanup(func() { _ = handler.Close() })
	call := func(operation string, input any) (any, error) {
		t.Helper()
		payload, err := database.MarshalCanonical(input)
		if err != nil {
			t.Fatal(err)
		}
		return handler.Handle(t.Context(), database.Request{
			Domain: modelCatalogBrokerDomain, Version: modelCatalogBrokerVersion,
			Operation: operation, Payload: payload,
		})
	}
	if value, err := call(modelCatalogOperationPreflight, struct {
		StoreID database.StoreID `json:"store_id"`
	}{ModelCatalogStoreID}); err != nil || !value.(modelCatalogMutationResponse).Updated {
		t.Fatalf("preflight = %#v, %v", value, err)
	}
	models := []CatalogModel{{ID: "model-a", OwnedBy: "owner"}, {ID: "model-b"}}
	if value, err := call(modelCatalogOperationSave, modelCatalogSaveRequest{
		StoreID: ModelCatalogStoreID, Provider: "openai", APIBase: "https://example.invalid/v1",
		APIKey: "secret-value", Models: models,
	}); err != nil || !value.(modelCatalogMutationResponse).Updated {
		t.Fatalf("save = %#v, %v", value, err)
	}
	value, err := call(modelCatalogOperationLoadPage, modelCatalogPageRequest{StoreID: ModelCatalogStoreID})
	if err != nil {
		t.Fatal(err)
	}
	page := value.(modelCatalogPageResponse)
	if !page.Done || len(page.Entries) != 1 || len(page.Entries[0].Models) != 2 || page.Revision == "" {
		t.Fatalf("page = %#v", page)
	}
	store := &CatalogStore{Entries: map[string]*CatalogEntry{page.Entries[0].ID: page.Entries[0]}}
	if value, err := call(modelCatalogOperationSaveAll, modelCatalogSaveAllRequest{
		StoreID: ModelCatalogStoreID, Store: store,
	}); err != nil || !value.(modelCatalogMutationResponse).Updated {
		t.Fatalf("save-all = %#v, %v", value, err)
	}
	if _, err := call(modelCatalogOperationLoadPage, modelCatalogPageRequest{
		StoreID: ModelCatalogStoreID, Revision: strings.Repeat("0", 64),
	}); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("stale revision error = %v", err)
	}
	if value, err := call(modelCatalogOperationDelete, modelCatalogDeleteRequest{
		StoreID: ModelCatalogStoreID, ID: page.Entries[0].ID,
	}); err != nil || !value.(modelCatalogDeleteResponse).Deleted {
		t.Fatalf("delete = %#v, %v", value, err)
	}
	if value, err := call(modelCatalogOperationDelete, modelCatalogDeleteRequest{
		StoreID: ModelCatalogStoreID, ID: page.Entries[0].ID,
	}); err != nil || value.(modelCatalogDeleteResponse).Deleted {
		t.Fatalf("second delete = %#v, %v", value, err)
	}

	invalid := []struct {
		operation string
		input     any
	}{
		{modelCatalogOperationPreflight, modelCatalogDeleteRequest{StoreID: "workspace.bad"}},
		{modelCatalogOperationLoadPage, modelCatalogPageRequest{StoreID: ModelCatalogStoreID, CatalogCursor: -1}},
		{modelCatalogOperationLoadPage, modelCatalogPageRequest{StoreID: ModelCatalogStoreID, Revision: "BAD"}},
		{modelCatalogOperationSaveAll, modelCatalogSaveAllRequest{StoreID: ModelCatalogStoreID}},
		{modelCatalogOperationSave, modelCatalogSaveRequest{StoreID: "workspace.bad"}},
		{modelCatalogOperationDelete, modelCatalogDeleteRequest{StoreID: ModelCatalogStoreID}},
	}
	for _, test := range invalid {
		if _, err := call(test.operation, test.input); database.CodeOf(err) != database.CodeInvalid {
			t.Errorf("%s invalid error = %v", test.operation, err)
		}
	}
	if _, err := call("unknown", modelCatalogDeleteRequest{StoreID: ModelCatalogStoreID, ID: "x"}); database.CodeOf(
		err,
	) != database.CodeUnsupported {
		t.Fatalf("unknown operation error = %v", err)
	}
	if _, err := (*ModelCatalogBrokerHandler)(nil).Handle(t.Context(), database.Request{}); database.CodeOf(
		err,
	) != database.CodeUnsupported {
		t.Fatalf("nil handler error = %v", err)
	}
}

func TestModelCatalogBrokerPaginationCodecAndErrorMatrix(t *testing.T) {
	entry := &CatalogEntry{
		ID: "catalog", Provider: "openai", APIBase: "https://example.invalid",
		FetchedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Models:    []CatalogModel{{ID: "one"}, {ID: "two"}},
	}
	store := &CatalogStore{Entries: map[string]*CatalogEntry{"catalog": entry}}
	revision, err := modelCatalogRevision(store)
	if err != nil || len(revision) != 64 {
		t.Fatalf("revision = %q, %v", revision, err)
	}
	entry.Models[0].ID = "changed"
	changed, err := modelCatalogRevision(store)
	if err != nil || changed == revision {
		t.Fatalf("changed revision = %q, %v", changed, err)
	}
	if _, err := modelCatalogRevision(nil); err == nil {
		t.Fatal("nil store revision succeeded")
	}
	if _, err := modelCatalogRevision(&CatalogStore{Entries: map[string]*CatalogEntry{"bad": nil}}); err == nil {
		t.Fatal("nil entry revision succeeded")
	}
	if _, err := modelCatalogRevision(&CatalogStore{Entries: map[string]*CatalogEntry{
		"key": {ID: "other"},
	}}); err == nil {
		t.Fatal("mismatched entry revision succeeded")
	}
	for _, value := range []string{"", revision} {
		if !validModelCatalogRevision(value) {
			t.Errorf("valid revision rejected: %q", value)
		}
	}
	for _, value := range []string{"BAD", strings.ToUpper(revision), strings.Repeat("z", 64)} {
		if validModelCatalogRevision(value) {
			t.Errorf("invalid revision accepted: %q", value)
		}
	}

	if _, err := pageModelCatalogStore(nil, modelCatalogPageRequest{}); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("nil page source error = %v", err)
	}
	if _, err := pageModelCatalogStore(store, modelCatalogPageRequest{CatalogCursor: 2}); database.CodeOf(
		err,
	) != database.CodeInvalid {
		t.Fatalf("catalog cursor error = %v", err)
	}
	done, err := pageModelCatalogStore(store, modelCatalogPageRequest{CatalogCursor: 1})
	if err != nil || !done.Done || len(done.Entries) != 0 {
		t.Fatalf("terminal page = %#v, %v", done, err)
	}
	_, terminalCursorErr := pageModelCatalogStore(
		store, modelCatalogPageRequest{CatalogCursor: 1, ModelCursor: 1},
	)
	if database.CodeOf(terminalCursorErr) != database.CodeInvalid {
		t.Fatalf("terminal model cursor error = %v", terminalCursorErr)
	}
	if _, err := pageModelCatalogStore(store, modelCatalogPageRequest{ModelCursor: 3}); database.CodeOf(
		err,
	) != database.CodeIntegrity {
		t.Fatalf("model cursor error = %v", err)
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
		{os.ErrPermission, database.CodeUnavailable},
		{errors.New("invalid input"), database.CodeInvalid},
		{errors.New("exceeds limit"), database.CodeInvalid},
		{errors.New("bad timestamp"), database.CodeInvalid},
		{errors.New("boom"), database.CodeInternal},
	}
	if mapModelCatalogBrokerError(nil) != nil {
		t.Fatal("nil catalog error mapped non-nil")
	}
	for _, test := range cases {
		if got := database.CodeOf(mapModelCatalogBrokerError(test.err)); got != test.code {
			t.Errorf("map(%v) = %s, want %s", test.err, got, test.code)
		}
	}
	if database.CodeOf(RunOfflineModelCatalogMigration(t.Context(), t.TempDir())) != database.CodeConflict {
		t.Fatal("unfenced catalog migration accepted")
	}
	if err := (*ModelCatalogBrokerHandler)(nil).Close(); err != nil {
		t.Fatalf("nil close error = %v", err)
	}
}
