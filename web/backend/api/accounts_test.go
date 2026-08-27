package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/collectionquery"
)

const (
	accountAccessTokenSentinel = "access-token-must-stay-private-47"
	accountRefreshSentinel     = "refresh-token-must-stay-private-93"
	accountClientSecret        = "oauth-client-secret-must-stay-private-61"
	accountEmailSentinel       = "private-email-29@example.test"
	accountRemoteIDSentinel    = "remote-account-id-must-stay-private-83"
	accountProjectSentinel     = "project-id-must-stay-private-17"
)

func setupAccountCollectionTest(t *testing.T) (*http.ServeMux, time.Time) {
	t.Helper()
	configPath, cleanup := setupOAuthTestEnv(t)
	t.Cleanup(cleanup)
	resetOAuthHooks(t)

	future := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	soon := time.Now().UTC().Add(2 * time.Minute).Truncate(time.Second)
	past := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	credentials := map[string]*auth.AuthCredential{
		"anthropic": {
			Provider: "anthropic", AuthMethod: "token", AccessToken: "anthropic-private-token",
		},
		"github-copilot:work": {
			Provider: "github-copilot", AuthMethod: "token", AccessToken: "copilot-private-token",
			ExpiresAt: soon,
		},
		"openai:alpha": {
			Provider: "openai", AuthMethod: "oauth", AccessToken: accountAccessTokenSentinel,
			RefreshToken: accountRefreshSentinel, OAuthClientSecret: accountClientSecret,
			Email: accountEmailSentinel, AccountID: accountRemoteIDSentinel,
			ProjectID: accountProjectSentinel, ExpiresAt: future,
		},
		"openai:beta": {
			Provider: "openai", AuthMethod: "token", AccessToken: "beta-private-token",
		},
		"openai:expired": {
			Provider: "openai", AuthMethod: "oauth", AccessToken: "expired-private-token",
			ExpiresAt: past,
		},
		// MCP credentials share the auth store but are not provider accounts.
		"mcp:private-tool": {
			Provider: "mcp", AuthMethod: "bearer", AccessToken: "mcp-private-token",
		},
	}
	for credentialID, credential := range credentials {
		if err := auth.SetCredential(credentialID, credential); err != nil {
			t.Fatalf("SetCredential(%q): %v", credentialID, err)
		}
	}

	handler := NewHandler(configPath)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	return mux, future
}

func requestAccountCollection(
	t *testing.T,
	mux *http.ServeMux,
	target string,
) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	return recorder
}

func decodeAccountsCollectionResponse(
	t *testing.T,
	recorder *httptest.ResponseRecorder,
) accountsCollectionResponse {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response accountsCollectionResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode account collection response: %v", err)
	}
	return response
}

func TestAccountCollectionQueryOrderingPagingAndCursorBinding(t *testing.T) {
	mux, future := setupAccountCollectionTest(t)

	firstRecorder := requestAccountCollection(t, mux, "/api/accounts?limit=2")
	first := decodeAccountsCollectionResponse(t, firstRecorder)
	if first.Total != 5 || len(first.Accounts) != 2 || first.NextCursor == "" {
		t.Fatalf("first page=%#v, want 2 of 5 with cursor", first)
	}
	if first.CanonicalQuery != "ALL ORDER BY provider ASC, id ASC" {
		t.Fatalf("canonical query=%q", first.CanonicalQuery)
	}
	if firstRecorder.Header().Get("Cache-Control") != "no-store" ||
		firstRecorder.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("safe response headers=%v", firstRecorder.Header())
	}

	all := append([]accountCollectionResource(nil), first.Accounts...)
	cursor := first.NextCursor
	for cursor != "" {
		next := decodeAccountsCollectionResponse(t, requestAccountCollection(
			t,
			mux,
			"/api/accounts?limit=2&cursor="+url.QueryEscape(cursor),
		))
		if next.Total != 5 {
			t.Fatalf("paged total=%d, want 5", next.Total)
		}
		all = append(all, next.Accounts...)
		cursor = next.NextCursor
	}
	if len(all) != 5 {
		t.Fatalf("paged account count=%d, want 5: %#v", len(all), all)
	}
	seen := make(map[string]bool, len(all))
	for index, account := range all {
		if !validCollectionResourceID(account.ID) || seen[account.ID] {
			t.Fatalf("invalid or duplicate account ID %q", account.ID)
		}
		seen[account.ID] = true
		if index == 0 {
			continue
		}
		previous := all[index-1]
		if previous.Provider > account.Provider ||
			(previous.Provider == account.Provider && previous.ID > account.ID) {
			t.Fatalf("default ordering is not provider/id ascending: %#v", all)
		}
	}

	query := `provider = openai AND status = connected ORDER BY account DESC`
	filtered := decodeAccountsCollectionResponse(t, requestAccountCollection(
		t,
		mux,
		"/api/accounts?query="+url.QueryEscape(query),
	))
	if filtered.Total != 2 || len(filtered.Accounts) != 2 ||
		filtered.Accounts[0].Account != "openai:beta" ||
		filtered.Accounts[1].Account != "openai:alpha" {
		t.Fatalf("filtered accounts=%#v", filtered.Accounts)
	}
	if filtered.Accounts[0].ExpiresAt != "" ||
		filtered.Accounts[1].ExpiresAt != future.Format(time.RFC3339) {
		t.Fatalf("expiry projection=%#v", filtered.Accounts)
	}

	nonExpiring := decodeAccountsCollectionResponse(t, requestAccountCollection(
		t,
		mux,
		"/api/accounts?query="+url.QueryEscape(`provider = anthropic AND expires_at !~ T`),
	))
	if nonExpiring.Total != 1 || nonExpiring.Accounts[0].Account != "anthropic" ||
		nonExpiring.Accounts[0].ExpiresAt != "" {
		t.Fatalf("non-expiring account query=%#v", nonExpiring.Accounts)
	}

	boundCursor := first.NextCursor
	mismatch := requestAccountCollection(
		t,
		mux,
		"/api/accounts?limit=2&query="+url.QueryEscape("ORDER BY account ASC")+
			"&cursor="+url.QueryEscape(boundCursor),
	)
	requireAccountCollectionError(t, mismatch, http.StatusBadRequest, "invalid_cursor")
	invalid := requestAccountCollection(t, mux, "/api/accounts?cursor=not-a-cursor")
	requireAccountCollectionError(t, invalid, http.StatusBadRequest, "invalid_cursor")

	fieldTypes := make(map[collectionquery.Field]collectionquery.FieldType)
	for _, field := range first.QuerySchema.Fields {
		fieldTypes[field.Name] = field.Type
	}
	for _, name := range []collectionquery.Field{
		"id", "provider", "account", "status", "auth_method", "expires_at",
	} {
		if _, ok := fieldTypes[name]; !ok {
			t.Fatalf("query schema omitted %q: %#v", name, first.QuerySchema)
		}
	}
	if fieldTypes["status"] != collectionquery.TypeEnum ||
		fieldTypes["expires_at"] != collectionquery.TypeString {
		t.Fatalf("query schema field types=%#v", fieldTypes)
	}
}

func TestAccountCollectionCursorAnchorsLifecycleStatus(t *testing.T) {
	mux, _ := setupAccountCollectionTest(t)
	evaluatedAt := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	expiredNow := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	store := &auth.AuthStore{Credentials: map[string]*auth.AuthCredential{
		"openai:alpha": {
			Provider: "openai", AuthMethod: "oauth", AccessToken: "private-alpha",
			ExpiresAt: expiredNow,
		},
		"openai:beta": {
			Provider: "openai", AuthMethod: "oauth", AccessToken: "private-beta",
			ExpiresAt: expiredNow,
		},
	}}
	originalLoadStore := oauthLoadStore
	oauthLoadStore = func() (*auth.AuthStore, error) { return store, nil }
	t.Cleanup(func() { oauthLoadStore = originalLoadStore })

	queryText := `status = connected ORDER BY account ASC`
	query, err := collectionquery.Parse(queryText, accountCollectionSchema)
	if err != nil {
		t.Fatal(err)
	}
	resources, err := projectAccountCollectionResources(store, evaluatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 2 {
		t.Fatalf("resources=%#v", resources)
	}
	sort.Slice(resources, func(left, right int) bool {
		return resources[left].Account < resources[right].Account
	})
	cursor, err := collectionquery.CursorFor(
		query,
		resources[0],
		evaluatedAt,
		accountCollectionPageOptions(),
	)
	if err != nil {
		t.Fatal(err)
	}

	response := decodeAccountsCollectionResponse(t, requestAccountCollection(
		t,
		mux,
		"/api/accounts?query="+url.QueryEscape(queryText)+"&cursor="+url.QueryEscape(cursor),
	))
	if response.Total != 2 || len(response.Accounts) != 1 ||
		response.Accounts[0].Account != "openai:beta" ||
		response.Accounts[0].Status != "connected" {
		t.Fatalf("cursor-anchored lifecycle page=%#v", response)
	}
}

func TestAccountCollectionDetailUsesOpaqueIDAndMasksCredentialMaterial(t *testing.T) {
	mux, _ := setupAccountCollectionTest(t)
	query := url.QueryEscape(`account = "openai:alpha"`)
	listRecorder := requestAccountCollection(t, mux, "/api/accounts?query="+query)
	list := decodeAccountsCollectionResponse(t, listRecorder)
	if list.Total != 1 || len(list.Accounts) != 1 {
		t.Fatalf("alpha list=%#v", list)
	}
	resource := list.Accounts[0]
	wantID, err := encodeCollectionResourceID(accountCollectionIDNamespace, "openai:alpha")
	if err != nil {
		t.Fatal(err)
	}
	if resource.ID != wantID || resource.Account != "openai:alpha" ||
		resource.Provider != "openai" || resource.Status != "connected" ||
		resource.AuthMethod != "oauth" {
		t.Fatalf("safe account projection=%#v", resource)
	}
	assertAccountResponseMasksSecrets(t, listRecorder.Body.String())

	detailRecorder := requestAccountCollection(t, mux, "/api/accounts/"+resource.ID)
	if detailRecorder.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", detailRecorder.Code, detailRecorder.Body.String())
	}
	assertAccountResponseMasksSecrets(t, detailRecorder.Body.String())
	var raw map[string]map[string]json.RawMessage
	if decodeErr := json.Unmarshal(detailRecorder.Body.Bytes(), &raw); decodeErr != nil {
		t.Fatalf("decode detail shape: %v", decodeErr)
	}
	if len(raw) != 1 || raw["account"] == nil {
		t.Fatalf("detail envelope=%#v", raw)
	}
	keys := make([]string, 0, len(raw["account"]))
	for key := range raw["account"] {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	wantKeys := []string{"account", "auth_method", "expires_at", "id", "provider", "status"}
	if strings.Join(keys, ",") != strings.Join(wantKeys, ",") {
		t.Fatalf("detail account keys=%v, want %v", keys, wantKeys)
	}
	var detail accountItemResponse
	if decodeErr := json.Unmarshal(detailRecorder.Body.Bytes(), &detail); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if detail.Account != resource {
		t.Fatalf("detail account=%#v, want %#v", detail.Account, resource)
	}

	invalid := requestAccountCollection(t, mux, "/api/accounts/not-an-opaque-id")
	requireAccountCollectionError(t, invalid, http.StatusBadRequest, "invalid_account_id")
	missingID, err := encodeCollectionResourceID(accountCollectionIDNamespace, "openai:missing")
	if err != nil {
		t.Fatal(err)
	}
	missing := requestAccountCollection(t, mux, "/api/accounts/"+missingID)
	requireAccountCollectionError(t, missing, http.StatusNotFound, "account_not_found")
	detailQuery := requestAccountCollection(t, mux, "/api/accounts/"+resource.ID+"?query=ALL")
	requireAccountCollectionError(t, detailQuery, http.StatusBadRequest, "invalid_collection_request")
}

func TestAccountCollectionStructuredErrorsAndUTF8BytePositions(t *testing.T) {
	mux, _ := setupAccountCollectionTest(t)

	unicodeQuery := "account = café AND nope = value"
	recorder := requestAccountCollection(
		t,
		mux,
		"/api/accounts?query="+url.QueryEscape(unicodeQuery),
	)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unicode query status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var queryError struct {
		Code     string `json:"code"`
		Message  string `json:"message"`
		Position int    `json:"position"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &queryError); err != nil {
		t.Fatal(err)
	}
	if queryError.Code != "invalid_query" ||
		queryError.Position != strings.Index(unicodeQuery, "nope") ||
		queryError.Message == "" || len(queryError.Message) > collectionquery.MaxQueryErrorMessageLen {
		t.Fatalf("structured query error=%#v", queryError)
	}

	for _, test := range []struct {
		target string
		code   string
	}{
		{target: "/api/accounts?limit=201", code: "invalid_page_limit"},
		{target: "/api/accounts?other=value", code: "invalid_collection_request"},
		{target: "/api/accounts?query=" + url.QueryEscape("status = unknown"), code: "invalid_query"},
	} {
		requireAccountCollectionError(
			t,
			requestAccountCollection(t, mux, test.target),
			http.StatusBadRequest,
			test.code,
		)
	}

	originalLoadStore := oauthLoadStore
	oauthLoadStore = func() (*auth.AuthStore, error) {
		return nil, errors.New("private store path must not escape")
	}
	t.Cleanup(func() { oauthLoadStore = originalLoadStore })
	storeFailure := requestAccountCollection(t, mux, "/api/accounts")
	requireAccountCollectionError(t, storeFailure, http.StatusInternalServerError, "account_store_failed")
	if strings.Contains(storeFailure.Body.String(), "private store path") {
		t.Fatalf("store error leaked internal detail: %s", storeFailure.Body.String())
	}

	oauthLoadStore = func() (*auth.AuthStore, error) {
		credentialID := "openai:" + strings.Repeat("a", collectionResourceIDIdentityMaxBytes)
		return &auth.AuthStore{Credentials: map[string]*auth.AuthCredential{
			credentialID: {Provider: "openai", AuthMethod: "token", AccessToken: "private"},
		}}, nil
	}
	projectionFailure := requestAccountCollection(t, mux, "/api/accounts")
	requireAccountCollectionError(
		t,
		projectionFailure,
		http.StatusInternalServerError,
		"account_projection_failed",
	)
}

func TestAccountCollectionProjectionAndFieldCoverage(t *testing.T) {
	if _, err := projectAccountCollectionResources(nil, time.Now().UTC()); err == nil {
		t.Fatal("nil store projection succeeded")
	}
	if _, err := projectAccountCollectionResources(&auth.AuthStore{}, time.Time{}); err == nil {
		t.Fatal("zero evaluation time projection succeeded")
	}

	evaluatedAt := time.Now().UTC().Truncate(time.Second)
	store := &auth.AuthStore{Credentials: map[string]*auth.AuthCredential{
		"openai:nil": nil,
		" OPENAI:MIXED ": {
			Provider: "openai", AuthMethod: "token", AccessToken: "private",
		},
		"openai:canonical": {
			Provider: "openai", AuthMethod: " TOKEN ", AccessToken: "private",
		},
		"mcp:ignored": {Provider: "mcp", AccessToken: "private"},
	}}
	resources, err := projectAccountCollectionResources(store, evaluatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 1 || resources[0].Account != "openai:canonical" ||
		resources[0].AuthMethod != "token" {
		t.Fatalf("normalized resources = %#v", resources)
	}

	resource := resources[0]
	fields := []collectionquery.Field{"id", "provider", "account", "status", "auth_method", "expires_at"}
	for _, field := range fields {
		if _, ok := accountCollectionField(resource, field); !ok {
			t.Fatalf("field %q did not resolve", field)
		}
	}
	if _, ok := accountCollectionField(resource, "unknown"); ok {
		t.Fatal("unknown field resolved")
	}

	status := oauthProviderStatus{}
	status.applyCredentialAt("openai", "openai:missing", nil, evaluatedAt)
	if status.DisplayName == "" || status.Methods == nil || status.Status != "not_logged_in" {
		t.Fatalf("defaulted provider status = %#v", status)
	}
}

func TestAccountCollectionDetailStoreFailure(t *testing.T) {
	mux, _ := setupAccountCollectionTest(t)
	id, err := encodeCollectionResourceID(accountCollectionIDNamespace, "openai:alpha")
	if err != nil {
		t.Fatal(err)
	}
	originalLoadStore := oauthLoadStore
	oauthLoadStore = func() (*auth.AuthStore, error) { return nil, errors.New("store unavailable") }
	t.Cleanup(func() { oauthLoadStore = originalLoadStore })

	response := requestAccountCollection(t, mux, "/api/accounts/"+id)
	requireAccountCollectionError(t, response, http.StatusInternalServerError, "account_store_failed")
}

func requireAccountCollectionError(
	t *testing.T,
	recorder *httptest.ResponseRecorder,
	wantStatus int,
	wantCode string,
) {
	t.Helper()
	var response struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response status=%d body=%s: %v", recorder.Code, recorder.Body.String(), err)
	}
	if recorder.Code != wantStatus || response.Code != wantCode || response.Message == "" {
		t.Fatalf(
			"status=%d response=%#v, want status=%d code=%q",
			recorder.Code,
			response,
			wantStatus,
			wantCode,
		)
	}
}

func assertAccountResponseMasksSecrets(t *testing.T, body string) {
	t.Helper()
	for _, secret := range []string{
		accountAccessTokenSentinel,
		accountRefreshSentinel,
		accountClientSecret,
		accountEmailSentinel,
		accountRemoteIDSentinel,
		accountProjectSentinel,
	} {
		if strings.Contains(body, secret) {
			t.Fatalf("account response leaked %q: %s", secret, body)
		}
	}
}
