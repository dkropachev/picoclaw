package auth

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/database"
)

func TestAuthBrokerHandlerOperationAndPaginationBoundaries(t *testing.T) {
	handler := NewBrokerHandler(t.TempDir())
	t.Cleanup(func() { _ = handler.Close() })
	request := func(operation string, input any) (any, error) {
		t.Helper()
		payload, err := database.MarshalCanonical(input)
		if err != nil {
			t.Fatal(err)
		}
		return handler.Handle(t.Context(), database.Request{
			Domain: authBrokerDomain, Version: authBrokerVersion,
			Operation: operation, Payload: payload,
		})
	}

	first := &AuthCredential{Provider: "openai", AuthMethod: "oauth", AccessToken: "one"}
	replacement := &AuthCredential{Provider: "openai", AuthMethod: "oauth", AccessToken: "two"}
	if result, err := request("save", authStoreRequest{
		StoreID: GlobalAuthStoreID,
		Store:   &AuthStore{Credentials: map[string]*AuthCredential{"openai:work": first}},
	}); err != nil || !result.(authMutationResponse).Updated {
		t.Fatalf("save = %#v, %v", result, err)
	}
	if result, err := request("get", authCredentialRequest{
		StoreID: GlobalAuthStoreID, CredentialID: "openai:work",
	}); err != nil || result.(authCredentialResponse).Credential.AccessToken != "one" {
		t.Fatalf("get = %#v, %v", result, err)
	}
	if result, err := request("compare-and-set", authCASRequest{
		StoreID: GlobalAuthStoreID, CredentialID: "openai:work",
		Source: first, Replacement: replacement,
	}); err != nil || !result.(authCredentialResponse).Committed {
		t.Fatalf("compare-and-set = %#v, %v", result, err)
	}
	if result, err := request("compare-and-set", authCASRequest{
		StoreID: GlobalAuthStoreID, CredentialID: "openai:work",
		Source: first, Replacement: first,
	}); err != nil || result.(authCredentialResponse).Committed ||
		result.(authCredentialResponse).Credential.AccessToken != "two" {
		t.Fatalf("stale compare-and-set = %#v, %v", result, err)
	}
	third := &AuthCredential{Provider: "openai", AuthMethod: "oauth", AccessToken: "three"}
	if result, err := request("update", authCASRequest{
		StoreID: GlobalAuthStoreID, CredentialID: "openai:new", Replacement: third,
	}); err != nil || !result.(authCredentialResponse).Committed {
		t.Fatalf("update create = %#v, %v", result, err)
	}
	if result, err := request("set", authCredentialRequest{
		StoreID: GlobalAuthStoreID, CredentialID: "openai:set", Credential: first,
	}); err != nil || !result.(authMutationResponse).Updated {
		t.Fatalf("set = %#v, %v", result, err)
	}

	pageValue, err := request("load-page", authLoadPageRequest{StoreID: GlobalAuthStoreID})
	if err != nil {
		t.Fatal(err)
	}
	page := pageValue.(authLoadPageResponse)
	if !page.Done || len(page.Items) != 3 || page.Revision == "" {
		t.Fatalf("page = %#v", page)
	}
	if _, err := request("load-page", authLoadPageRequest{
		StoreID: GlobalAuthStoreID, Revision: strings.Repeat("0", 64),
	}); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("stale revision error = %v", err)
	}
	if _, err := request("load-page", authLoadPageRequest{
		StoreID: GlobalAuthStoreID, Cursor: "missing", Revision: page.Revision,
	}); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("stale cursor error = %v", err)
	}
	if result, err := request("delete", authCredentialRequest{
		StoreID: GlobalAuthStoreID, CredentialID: "openai:set",
	}); err != nil || !result.(authMutationResponse).Updated {
		t.Fatalf("delete = %#v, %v", result, err)
	}
	if result, err := request("delete-all", authEmptyRequest{StoreID: GlobalAuthStoreID}); err != nil ||
		!result.(authMutationResponse).Updated {
		t.Fatalf("delete-all = %#v, %v", result, err)
	}

	invalid := []struct {
		operation string
		input     any
	}{
		{"load-page", authLoadPageRequest{StoreID: GlobalAuthStoreID, Revision: "BAD"}},
		{"save", authStoreRequest{StoreID: GlobalAuthStoreID}},
		{"get", authCredentialRequest{StoreID: GlobalAuthStoreID}},
		{"set", authCredentialRequest{StoreID: GlobalAuthStoreID, CredentialID: "x"}},
		{"compare-and-set", authCASRequest{StoreID: GlobalAuthStoreID, CredentialID: "x"}},
		{"update", authCASRequest{StoreID: GlobalAuthStoreID, CredentialID: "x"}},
		{"delete", authCredentialRequest{StoreID: GlobalAuthStoreID}},
		{"delete-all", authEmptyRequest{StoreID: "workspace.auth"}},
	}
	for _, test := range invalid {
		if _, err := request(test.operation, test.input); database.CodeOf(err) != database.CodeInvalid {
			t.Errorf("%s invalid error = %v", test.operation, err)
		}
	}
	if _, err := request("unknown", authEmptyRequest{StoreID: GlobalAuthStoreID}); database.CodeOf(
		err,
	) != database.CodeUnsupported {
		t.Fatalf("unknown operation error = %v", err)
	}
	if _, err := (*authBrokerHandler)(nil).Handle(t.Context(), database.Request{}); database.CodeOf(
		err,
	) != database.CodeUnsupported {
		t.Fatalf("nil handler error = %v", err)
	}
	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.Handle(t.Context(), database.Request{
		Domain: authBrokerDomain, Version: authBrokerVersion,
	}); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("closed handler error = %v", err)
	}
}

func TestAuthBrokerPureBoundaries(t *testing.T) {
	if _, err := pageAuthStore(nil, authLoadPageRequest{}); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("nil store error = %v", err)
	}
	store := &AuthStore{Credentials: map[string]*AuthCredential{
		"openai:huge": {
			Provider:    "openai",
			AuthMethod:  "oauth",
			AccessToken: strings.Repeat("x", authBrokerPageBytes+1),
		},
	}}
	if _, err := pageAuthStore(store, authLoadPageRequest{}); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("oversize page error = %v", err)
	}
	for _, value := range []string{"", strings.Repeat("0", 64)} {
		if !validAuthRevision(value) {
			t.Errorf("validAuthRevision(%q) = false", value)
		}
	}
	for _, value := range []string{"ABC", strings.Repeat("A", 64), strings.Repeat("z", 64)} {
		if validAuthRevision(value) {
			t.Errorf("validAuthRevision(%q) = true", value)
		}
	}
	if mapAuthBrokerError(nil) != nil {
		t.Fatal("nil error mapped non-nil")
	}
	deadline := mapAuthBrokerError(context.DeadlineExceeded)
	if database.CodeOf(deadline) != database.CodeDeadline {
		t.Fatalf("deadline code = %s", database.CodeOf(deadline))
	}
	structured := database.NewError(database.CodeConflict, "conflict")
	if mapAuthBrokerError(structured) != structured {
		t.Fatal("structured auth error was replaced")
	}
	if database.CodeOf(mapAuthBrokerError(context.Canceled)) != database.CodeDeadline ||
		database.CodeOf(mapAuthBrokerError(errors.New("boom"))) != database.CodeInternal {
		t.Fatal("auth error mapping mismatch")
	}
	if database.CodeOf(RunOfflineDatabaseMigration(t.Context(), t.TempDir())) != database.CodeConflict {
		t.Fatal("unfenced auth migration accepted")
	}
}
