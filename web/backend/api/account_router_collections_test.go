package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/config"
)

func configuredAccountForRouterTests() *config.ModelConfig {
	return &config.ModelConfig{
		ModelName: "account-a",
		Provider:  "openai",
		Model:     "gpt-4o",
		APIKeys:   config.SimpleSecureStrings("sk-router-secret"),
		Enabled:   true,
	}
}

func testAccountRouter(name string, enabled bool) config.AccountRouterConfig {
	return config.AccountRouterConfig{
		Name:    name,
		Enabled: enabled,
		Entry:   "entry",
		Blocks: []config.AccountRouterBlock{{
			ID:      "entry",
			Type:    config.AccountRouterBlockTypeAccount,
			Account: "account-a",
		}},
	}
}

func configureAccountRouterCollectionFixture(cfg *config.Config) {
	cfg.ModelList = []*config.ModelConfig{configuredAccountForRouterTests()}
	cfg.AccountRouters = config.AccountRouterList{
		testAccountRouter("referenced-router", true),
		testAccountRouter("disabled-router", false),
		testAccountRouter("free-router", true),
		testAccountRouter("default-router", true),
	}
	cfg.Agents.Defaults.AccountRef = "default-router"
	cfg.Agents.List = []config.AgentConfig{{
		ID: "worker", Default: true, AccountRef: "referenced-router",
	}}
}

func configureAccountRouterMutationFixture(cfg *config.Config) {
	cfg.ModelList = []*config.ModelConfig{configuredAccountForRouterTests()}
	cfg.ModelAliases = []config.ModelAliasConfig{{Name: "coding", Model: "gpt-4o-mini"}}
	cfg.Agents.Defaults.AccountRef = "account-a"
	cfg.Agents.Defaults.ModelName = "coding"
}

func decodeAccountRouterMutationResponse(
	t *testing.T,
	recorderBody []byte,
) (accountRouterResource, string) {
	t.Helper()
	var response struct {
		AccountRouter  accountRouterResource `json:"account_router"`
		ConfigRevision string                `json:"config_revision"`
	}
	if err := json.Unmarshal(recorderBody, &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v; body=%s", err, recorderBody)
	}
	return response.AccountRouter, response.ConfigRevision
}

func decodeCollectionErrorCode(t *testing.T, body []byte) string {
	t.Helper()
	var response struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("json.Unmarshal(error) error = %v; body=%s", err, body)
	}
	return response.Code
}

func TestAccountRouterCollectionQueryPagingDetailAndUTF8Errors(t *testing.T) {
	resetGatewayTestState(t)
	harness := newAgentAPITestHarness(t, configureAccountRouterCollectionFixture)
	query := `enabled = true ORDER BY name ASC`
	first := harness.request(
		t,
		http.MethodGet,
		"/api/account-routers?query="+url.QueryEscape(query)+"&limit=2",
		nil,
	)
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, body=%s", first.Code, first.Body.String())
	}
	if strings.Contains(first.Body.String(), "sk-router-secret") ||
		strings.Contains(first.Body.String(), `"account":"account-a"`) {
		t.Fatalf("summary response leaked detail or credentials: %s", first.Body.String())
	}
	var page struct {
		AccountRouters []accountRouterSummary `json:"account_routers"`
		Total          int                    `json:"total"`
		NextCursor     string                 `json:"next_cursor"`
		CanonicalQuery string                 `json:"canonical_query"`
		QuerySchema    json.RawMessage        `json:"query_schema"`
		ConfigRevision string                 `json:"config_revision"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatalf("json.Unmarshal(first) error = %v", err)
	}
	if len(page.AccountRouters) != 2 || page.Total != 3 || page.NextCursor == "" ||
		page.CanonicalQuery == "" || len(page.QuerySchema) == 0 || page.ConfigRevision == "" {
		t.Fatalf("first page = %#v", page)
	}
	for _, router := range page.AccountRouters {
		if router.ID != router.Name || router.Status != accountRouterStatusAvailable ||
			router.Accounts != 1 || router.Blocks != 1 {
			t.Fatalf("summary = %#v", router)
		}
	}

	second := harness.request(
		t,
		http.MethodGet,
		"/api/account-routers?query="+url.QueryEscape(query)+"&limit=2&cursor="+
			url.QueryEscape(page.NextCursor),
		nil,
	)
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d, body=%s", second.Code, second.Body.String())
	}
	mismatch := harness.request(
		t,
		http.MethodGet,
		"/api/account-routers?query="+url.QueryEscape(`enabled = false ORDER BY name ASC`)+
			"&limit=2&cursor="+url.QueryEscape(page.NextCursor),
		nil,
	)
	if mismatch.Code != http.StatusBadRequest ||
		decodeCollectionErrorCode(t, mismatch.Body.Bytes()) != "invalid_cursor" {
		t.Fatalf("cursor mismatch status = %d, body=%s", mismatch.Code, mismatch.Body.String())
	}

	detail := harness.request(t, http.MethodGet, "/api/account-routers/free-router", nil)
	if detail.Code != http.StatusOK {
		t.Fatalf("detail status = %d, body=%s", detail.Code, detail.Body.String())
	}
	resource, detailRevision := decodeAccountRouterMutationResponse(t, detail.Body.Bytes())
	if resource.ID != "free-router" || resource.Name != resource.ID ||
		resource.Status != accountRouterStatusAvailable || resource.IsDefault ||
		len(resource.Accounts) != 1 || resource.Accounts[0] != "account-a" ||
		len(resource.Blocks) != 1 || detailRevision != page.ConfigRevision {
		t.Fatalf("detail resource = %#v, revision=%q", resource, detailRevision)
	}
	if strings.Contains(detail.Body.String(), "sk-router-secret") {
		t.Fatalf("detail leaked credential: %s", detail.Body.String())
	}

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		invalidPath := harness.request(
			t,
			method,
			"/api/account-routers/%20free-router%20",
			nil,
		)
		if invalidPath.Code != http.StatusBadRequest ||
			decodeCollectionErrorCode(t, invalidPath.Body.Bytes()) != "invalid_account_router_id" {
			t.Fatalf(
				"%s invalid path status = %d, body=%s",
				method,
				invalidPath.Code,
				invalidPath.Body.String(),
			)
		}
	}

	invalidQuery := `name = "é" AND unknown = value`
	invalid := harness.request(
		t,
		http.MethodGet,
		"/api/account-routers?query="+url.QueryEscape(invalidQuery),
		nil,
	)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid query status = %d, body=%s", invalid.Code, invalid.Body.String())
	}
	var queryError struct {
		Code     string `json:"code"`
		Message  string `json:"message"`
		Position int    `json:"position"`
	}
	if err := json.Unmarshal(invalid.Body.Bytes(), &queryError); err != nil {
		t.Fatalf("json.Unmarshal(query error) error = %v", err)
	}
	if queryError.Code != "invalid_query" ||
		queryError.Position != strings.Index(invalidQuery, "unknown") ||
		len(queryError.Message) == 0 || len(queryError.Message) > 512 {
		t.Fatalf("query error = %#v", queryError)
	}
}

func TestAccountRouterResourceIDsPreserveUnsafeAndReservedNames(t *testing.T) {
	resetGatewayTestState(t)
	reservedShapeName := accountRouterOpaqueIDPrefix +
		strings.Repeat("A", collectionResourceIDEncodedBytes)
	if !accountRouterOpaqueID(reservedShapeName) {
		t.Fatalf("test reserved-shape name %q is not recognized", reservedShapeName)
	}
	names := []string{
		"safe-router",
		"slash/router",
		"space router",
		"query?router",
		"fragment#router",
		"percent%router",
		`backslash\router`,
		"unicode-路由器",
		".",
		"..",
		"new",
		reservedShapeName,
	}
	harness := newAgentAPITestHarness(t, func(cfg *config.Config) {
		cfg.ModelList = []*config.ModelConfig{configuredAccountForRouterTests()}
		for _, name := range names {
			cfg.AccountRouters = append(cfg.AccountRouters, testAccountRouter(name, true))
		}
	})
	beforeReads, err := os.ReadFile(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}

	listRecorder := harness.request(t, http.MethodGet, "/api/account-routers", nil)
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", listRecorder.Code, listRecorder.Body.String())
	}
	var list struct {
		AccountRouters []accountRouterSummary `json:"account_routers"`
		ConfigRevision string                 `json:"config_revision"`
	}
	if decodeErr := json.Unmarshal(listRecorder.Body.Bytes(), &list); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if len(list.AccountRouters) != len(names) || list.ConfigRevision == "" {
		t.Fatalf("router list = %#v", list)
	}
	byName := make(map[string]accountRouterSummary, len(list.AccountRouters))
	seenIDs := make(map[string]bool, len(list.AccountRouters))
	for _, summary := range list.AccountRouters {
		if seenIDs[summary.ID] {
			t.Fatalf("duplicate resource ID %q in %#v", summary.ID, list.AccountRouters)
		}
		seenIDs[summary.ID] = true
		byName[summary.Name] = summary
	}
	pagedRecorder := harness.request(t, http.MethodGet, "/api/account-routers?limit=1", nil)
	if pagedRecorder.Code != http.StatusOK {
		t.Fatalf("paged list status = %d, body=%s", pagedRecorder.Code, pagedRecorder.Body.String())
	}
	var paged struct {
		NextCursor string `json:"next_cursor"`
	}
	if err = json.Unmarshal(pagedRecorder.Body.Bytes(), &paged); err != nil {
		t.Fatal(err)
	}
	defaultQuery, err := collectionquery.Parse("", accountRouterCollectionSchema)
	if err != nil {
		t.Fatal(err)
	}
	decodedCursor, err := collectionquery.DecodeCursor(
		paged.NextCursor,
		defaultQuery,
		validAccountRouterResourceID,
	)
	if err != nil || decodedCursor.ID != byName["."].ID ||
		!accountRouterOpaqueID(decodedCursor.ID) {
		t.Fatalf("unsafe-name cursor = %#v, err=%v", decodedCursor, err)
	}
	for _, name := range names {
		summary, found := byName[name]
		if !found {
			t.Fatalf("list omitted router name %q: %#v", name, list.AccountRouters)
		}
		wantID, idErr := accountRouterResourceID(name)
		if idErr != nil {
			t.Fatal(idErr)
		}
		if summary.ID != wantID || !validAccountRouterResourceID(summary.ID) {
			t.Fatalf("router %q ID = %q, want %q", name, summary.ID, wantID)
		}
		if name == "safe-router" {
			if summary.ID != name {
				t.Fatalf("safe router ID = %q, want direct name", summary.ID)
			}
		} else if summary.ID == name || !accountRouterOpaqueID(summary.ID) {
			t.Fatalf("unsafe/reserved router %q got non-opaque ID %q", name, summary.ID)
		}

		detail := harness.request(
			t,
			http.MethodGet,
			"/api/account-routers/"+url.PathEscape(summary.ID),
			nil,
		)
		if detail.Code != http.StatusOK {
			t.Fatalf("detail %q status = %d, body=%s", name, detail.Code, detail.Body.String())
		}
		resource, _ := decodeAccountRouterMutationResponse(t, detail.Body.Bytes())
		if resource.ID != summary.ID || resource.Name != name {
			t.Fatalf("detail %q resource = %#v", name, resource)
		}
	}

	directReserved := harness.request(t, http.MethodGet, "/api/account-routers/new", nil)
	if directReserved.Code != http.StatusBadRequest ||
		decodeCollectionErrorCode(t, directReserved.Body.Bytes()) != "invalid_account_router_id" {
		t.Fatalf("direct reserved ID status = %d, body=%s", directReserved.Code, directReserved.Body.String())
	}
	directShape := harness.request(
		t,
		http.MethodGet,
		"/api/account-routers/"+url.PathEscape(reservedShapeName),
		nil,
	)
	if directShape.Code != http.StatusNotFound ||
		decodeCollectionErrorCode(t, directShape.Body.Bytes()) != "account_router_not_found" {
		t.Fatalf("direct natural opaque shape status = %d, body=%s", directShape.Code, directShape.Body.String())
	}
	afterReads, err := os.ReadFile(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeReads, afterReads) {
		t.Fatal("resource-ID list/detail reads migrated persisted router names")
	}

	newID := byName["new"].ID
	madeDefault := harness.request(
		t,
		http.MethodPost,
		"/api/account-routers/"+url.PathEscape(newID)+"/default",
		accountRouterRevisionRequest{ExpectedConfigRevision: list.ConfigRevision},
	)
	if madeDefault.Code != http.StatusOK {
		t.Fatalf("opaque default status = %d, body=%s", madeDefault.Code, madeDefault.Body.String())
	}
	defaultResource, defaultRevision := decodeAccountRouterMutationResponse(t, madeDefault.Body.Bytes())
	if defaultResource.ID != newID || defaultResource.Name != "new" || !defaultResource.IsDefault {
		t.Fatalf("opaque default resource = %#v", defaultResource)
	}

	slashID := byName["slash/router"].ID
	slashRouter := testAccountRouter("slash/router", true)
	slashRouter.RefreshIntervalSeconds = 90
	updated := harness.request(
		t,
		http.MethodPut,
		"/api/account-routers/"+url.PathEscape(slashID),
		accountRouterMutationRequest{
			ExpectedConfigRevision: defaultRevision,
			AccountRouter:          &slashRouter,
		},
	)
	if updated.Code != http.StatusOK {
		t.Fatalf("opaque update status = %d, body=%s", updated.Code, updated.Body.String())
	}
	updatedResource, updatedRevision := decodeAccountRouterMutationResponse(t, updated.Body.Bytes())
	if updatedResource.ID != slashID || updatedResource.Name != "slash/router" ||
		updatedResource.RefreshIntervalSeconds != 90 {
		t.Fatalf("opaque update resource = %#v", updatedResource)
	}

	missingDigest, err := encodeCollectionResourceID(accountRouterCollectionIDNamespace, "missing/router")
	if err != nil {
		t.Fatal(err)
	}
	missingID := accountRouterOpaqueIDPrefix + missingDigest
	dotID := byName["."].ID
	bulk := harness.request(t, http.MethodPost, "/api/account-routers/bulk-delete", map[string]any{
		"ids": []string{dotID, newID, missingID}, "config_revision": updatedRevision,
	})
	if bulk.Code != http.StatusOK {
		t.Fatalf("opaque bulk status = %d, body=%s", bulk.Code, bulk.Body.String())
	}
	var bulkResult collectionBulkDeleteResponse
	if err = json.Unmarshal(bulk.Body.Bytes(), &bulkResult); err != nil {
		t.Fatal(err)
	}
	failureCodes := make(map[string]string, len(bulkResult.Failures))
	for _, failure := range bulkResult.Failures {
		failureCodes[failure.ID] = failure.Code
	}
	if len(bulkResult.DeletedIDs) != 1 || bulkResult.DeletedIDs[0] != dotID ||
		len(bulkResult.Failures) != 2 || failureCodes[missingID] != "not_found" ||
		failureCodes[newID] != "default" {
		t.Fatalf("opaque bulk result = %#v", bulkResult)
	}

	dotDotID := byName[".."].ID
	deleted := harness.request(
		t,
		http.MethodDelete,
		"/api/account-routers/"+url.PathEscape(dotDotID)+
			"?revision="+url.QueryEscape(bulkResult.ConfigRevision),
		nil,
	)
	if deleted.Code != http.StatusOK {
		t.Fatalf("opaque delete status = %d, body=%s", deleted.Code, deleted.Body.String())
	}
	var deleteResult collectionBulkDeleteResponse
	if err = json.Unmarshal(deleted.Body.Bytes(), &deleteResult); err != nil {
		t.Fatal(err)
	}
	if len(deleteResult.DeletedIDs) != 1 || deleteResult.DeletedIDs[0] != dotDotID {
		t.Fatalf("opaque delete result = %#v", deleteResult)
	}

	createdRouter := testAccountRouter("created/unsafe", true)
	created := harness.request(t, http.MethodPost, "/api/account-routers", accountRouterMutationRequest{
		ExpectedConfigRevision: deleteResult.ConfigRevision,
		AccountRouter:          &createdRouter,
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("unsafe create status = %d, body=%s", created.Code, created.Body.String())
	}
	createdResource, _ := decodeAccountRouterMutationResponse(t, created.Body.Bytes())
	if createdResource.Name != "created/unsafe" || !accountRouterOpaqueID(createdResource.ID) ||
		created.Header().Get("Location") != "/api/account-routers/"+createdResource.ID {
		t.Fatalf("unsafe create resource = %#v, Location=%q", createdResource, created.Header().Get("Location"))
	}
}

func TestAccountRouterCursorAnchorsCredentialExpiryStatus(t *testing.T) {
	resetGatewayTestState(t)
	credentialRouter := func(name, accountRef string) config.AccountRouterConfig {
		return config.AccountRouterConfig{
			Name: name, Enabled: true, Entry: "entry",
			Blocks: []config.AccountRouterBlock{{
				ID: "entry", Type: config.AccountRouterBlockTypeAccount, Account: accountRef,
			}},
		}
	}
	harness := newAgentAPITestHarness(t, func(cfg *config.Config) {
		cfg.ModelList = nil
		cfg.AccountRouters = config.AccountRouterList{
			credentialRouter("alpha-router", "credential:openai:alpha"),
			credentialRouter("beta-router", "credential:openai:beta"),
		}
	})
	expiresAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	evaluatedAt := expiresAt.Add(-time.Minute)
	store := &auth.AuthStore{Credentials: map[string]*auth.AuthCredential{
		"openai:alpha": {
			Provider: "openai", AuthMethod: "oauth", AccessToken: "private-alpha",
			ExpiresAt: expiresAt,
		},
		"openai:beta": {
			Provider: "openai", AuthMethod: "oauth", AccessToken: "private-beta",
			ExpiresAt: expiresAt,
		},
	}}
	originalLoadStore := oauthLoadStore
	oauthLoadStore = func() (*auth.AuthStore, error) { return store, nil }
	t.Cleanup(func() { oauthLoadStore = originalLoadStore })
	if !credentialAccountAvailableAt("credential:openai:alpha", evaluatedAt) ||
		credentialAccountAvailableAt("credential:openai:alpha", expiresAt.Add(time.Second)) {
		t.Fatal("credential availability did not honor the supplied evaluation time")
	}

	cfg, _, err := config.LoadConfigSnapshot(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}
	queryText := `status = available ORDER BY status ASC, name ASC`
	query, err := collectionquery.Parse(queryText, accountRouterCollectionSchema)
	if err != nil {
		t.Fatal(err)
	}
	items, err := projectAccountRouterItems(cfg, evaluatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Summary.Status != accountRouterStatusAvailable ||
		items[1].Summary.Status != accountRouterStatusAvailable {
		t.Fatalf("anchored router items = %#v", items)
	}
	cursor, err := collectionquery.CursorFor(
		query,
		items[0],
		evaluatedAt,
		accountRouterPageOptions(),
	)
	if err != nil {
		t.Fatal(err)
	}
	response := harness.request(
		t,
		http.MethodGet,
		"/api/account-routers?query="+url.QueryEscape(queryText)+
			"&cursor="+url.QueryEscape(cursor),
		nil,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("anchored page status = %d, body=%s", response.Code, response.Body.String())
	}
	var page struct {
		AccountRouters []accountRouterSummary `json:"account_routers"`
		Total          int                    `json:"total"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.AccountRouters) != 1 ||
		page.AccountRouters[0].Name != "beta-router" ||
		page.AccountRouters[0].Status != accountRouterStatusAvailable {
		t.Fatalf("cursor-anchored router page = %#v", page)
	}
}

func TestAccountRouterCollectionCRUDDefaultAndMutationBoundaries(t *testing.T) {
	resetGatewayTestState(t)
	harness := newAgentAPITestHarness(t, configureAccountRouterMutationFixture)
	initialRevision, err := config.ConfigRevision(harness.configPath)
	if err != nil {
		t.Fatalf("ConfigRevision() error = %v", err)
	}
	invalidName := testAccountRouter("bad\nname", true)
	invalidCreate := harness.request(t, http.MethodPost, "/api/account-routers", accountRouterMutationRequest{
		ExpectedConfigRevision: initialRevision,
		AccountRouter:          &invalidName,
	})
	if invalidCreate.Code != http.StatusUnprocessableEntity ||
		decodeCollectionErrorCode(t, invalidCreate.Body.Bytes()) != "invalid_account_router" {
		t.Fatalf("invalid name status = %d, body=%s", invalidCreate.Code, invalidCreate.Body.String())
	}
	router := testAccountRouter("created-router", false)
	createRequest := accountRouterMutationRequest{
		ExpectedConfigRevision: initialRevision,
		AccountRouter:          &router,
	}
	conflictingBody, err := json.Marshal(createRequest)
	if err != nil {
		t.Fatal(err)
	}
	conflictingFence := serveCollectionRaw(
		t,
		harness.configPath,
		http.MethodPost,
		"/api/account-routers",
		string(conflictingBody),
		"application/json",
		map[string]string{"If-Match": "different"},
	)
	if conflictingFence.Code != http.StatusBadRequest ||
		decodeCollectionErrorCode(t, conflictingFence.Body.Bytes()) != "conflicting_config_revision" {
		t.Fatalf(
			"conflicting fence status = %d, body=%s",
			conflictingFence.Code,
			conflictingFence.Body.String(),
		)
	}
	headerCreateBody, err := json.Marshal(accountRouterMutationRequest{AccountRouter: &router})
	if err != nil {
		t.Fatal(err)
	}
	created := serveCollectionRaw(
		t,
		harness.configPath,
		http.MethodPost,
		"/api/account-routers",
		string(headerCreateBody),
		"application/json",
		map[string]string{"If-Match": `"` + initialRevision + `"`},
	)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", created.Code, created.Body.String())
	}
	createdResource, createdRevision := decodeAccountRouterMutationResponse(t, created.Body.Bytes())
	if createdResource.ID != "created-router" ||
		createdResource.Status != accountRouterStatusDisabled ||
		created.Header().Get("Location") != "/api/account-routers/created-router" ||
		createdRevision == "" || createdRevision == initialRevision {
		t.Fatalf("create resource = %#v, revision=%q, headers=%v", createdResource, createdRevision, created.Header())
	}

	disabledDefault := harness.request(
		t,
		http.MethodPost,
		"/api/account-routers/created-router/default",
		accountRouterRevisionRequest{ExpectedConfigRevision: createdRevision},
	)
	if disabledDefault.Code != http.StatusConflict ||
		decodeCollectionErrorCode(t, disabledDefault.Body.Bytes()) != "account_router_disabled" {
		t.Fatalf("disabled default status = %d, body=%s", disabledDefault.Code, disabledDefault.Body.String())
	}

	router.Enabled = true
	stale := harness.request(t, http.MethodPut, "/api/account-routers/created-router", accountRouterMutationRequest{
		ExpectedConfigRevision: initialRevision,
		AccountRouter:          &router,
	})
	if stale.Code != http.StatusConflict ||
		decodeCollectionErrorCode(t, stale.Body.Bytes()) != "config_revision_mismatch" {
		t.Fatalf("stale update status = %d, body=%s", stale.Code, stale.Body.String())
	}
	updated := harness.request(t, http.MethodPut, "/api/account-routers/created-router", accountRouterMutationRequest{
		ExpectedConfigRevision: createdRevision,
		AccountRouter:          &router,
	})
	if updated.Code != http.StatusOK {
		t.Fatalf("update status = %d, body=%s", updated.Code, updated.Body.String())
	}
	updatedResource, updatedRevision := decodeAccountRouterMutationResponse(t, updated.Body.Bytes())
	if !updatedResource.Enabled || updatedResource.Status != accountRouterStatusAvailable ||
		updatedRevision == "" || updatedRevision == createdRevision {
		t.Fatalf("update resource = %#v, revision=%q", updatedResource, updatedRevision)
	}

	rename := router
	rename.Name = "renamed-router"
	renameResponse := harness.request(
		t,
		http.MethodPut,
		"/api/account-routers/created-router",
		accountRouterMutationRequest{
			ExpectedConfigRevision: updatedRevision,
			AccountRouter:          &rename,
		},
	)
	if renameResponse.Code != http.StatusConflict ||
		decodeCollectionErrorCode(t, renameResponse.Body.Bytes()) != "account_router_name_immutable" {
		t.Fatalf("rename status = %d, body=%s", renameResponse.Code, renameResponse.Body.String())
	}

	invalidRouter := testAccountRouter("invalid-router", true)
	invalidRouter.Blocks[0].Account = "missing-account"
	revisionBeforeInvalid, err := config.ConfigRevision(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}
	invalidCandidate := harness.request(t, http.MethodPost, "/api/account-routers", accountRouterMutationRequest{
		ExpectedConfigRevision: revisionBeforeInvalid,
		AccountRouter:          &invalidRouter,
	})
	if invalidCandidate.Code != http.StatusUnprocessableEntity ||
		decodeCollectionErrorCode(t, invalidCandidate.Body.Bytes()) != "invalid_account_router_configuration" {
		t.Fatalf("invalid candidate status = %d, body=%s", invalidCandidate.Code, invalidCandidate.Body.String())
	}
	revisionAfterInvalid, err := config.ConfigRevision(harness.configPath)
	if err != nil || revisionAfterInvalid != revisionBeforeInvalid {
		t.Fatalf("rejected candidate revision = %q, want %q; err=%v", revisionAfterInvalid, revisionBeforeInvalid, err)
	}

	duplicateJSON := `{"expected_config_revision":"` + updatedRevision +
		`","account_router":{"name":"one"},"ACCOUNT_ROUTER":{"name":"two"}}`
	duplicate := serveCollectionRaw(
		t,
		harness.configPath,
		http.MethodPost,
		"/api/account-routers",
		duplicateJSON,
		"application/json",
		nil,
	)
	if duplicate.Code != http.StatusBadRequest ||
		decodeCollectionErrorCode(t, duplicate.Body.Bytes()) != "invalid_collection_request" {
		t.Fatalf("duplicate JSON status = %d, body=%s", duplicate.Code, duplicate.Body.String())
	}

	encodedCreate, err := json.Marshal(accountRouterMutationRequest{
		ExpectedConfigRevision: updatedRevision,
		AccountRouter:          &invalidRouter,
	})
	if err != nil {
		t.Fatal(err)
	}
	crossOrigin := serveCollectionRaw(
		t,
		harness.configPath,
		http.MethodPost,
		"/api/account-routers",
		string(encodedCreate),
		"application/json",
		map[string]string{"Origin": "https://attacker.invalid", "Sec-Fetch-Site": "cross-site"},
	)
	if crossOrigin.Code != http.StatusForbidden ||
		decodeCollectionErrorCode(t, crossOrigin.Body.Bytes()) != "cross_origin_mutation" {
		t.Fatalf("cross-origin status = %d, body=%s", crossOrigin.Code, crossOrigin.Body.String())
	}

	deleted := harness.request(
		t,
		http.MethodDelete,
		"/api/account-routers/created-router?revision="+url.QueryEscape(updatedRevision),
		nil,
	)
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body=%s", deleted.Code, deleted.Body.String())
	}
	var deleteResponse collectionBulkDeleteResponse
	if decodeErr := json.Unmarshal(deleted.Body.Bytes(), &deleteResponse); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if len(deleteResponse.DeletedIDs) != 1 || deleteResponse.DeletedIDs[0] != "created-router" ||
		deleteResponse.ConfigRevision == "" {
		t.Fatalf("delete response = %#v", deleteResponse)
	}

	router.Enabled = true
	recreated := harness.request(t, http.MethodPost, "/api/account-routers", accountRouterMutationRequest{
		ExpectedConfigRevision: deleteResponse.ConfigRevision,
		AccountRouter:          &router,
	})
	if recreated.Code != http.StatusCreated {
		t.Fatalf("recreate status = %d, body=%s", recreated.Code, recreated.Body.String())
	}
	_, recreatedRevision := decodeAccountRouterMutationResponse(t, recreated.Body.Bytes())
	staleDefault := harness.request(
		t,
		http.MethodPost,
		"/api/account-routers/created-router/default",
		accountRouterRevisionRequest{ExpectedConfigRevision: deleteResponse.ConfigRevision},
	)
	if staleDefault.Code != http.StatusConflict ||
		decodeCollectionErrorCode(t, staleDefault.Body.Bytes()) != "config_revision_mismatch" {
		t.Fatalf("stale default status = %d, body=%s", staleDefault.Code, staleDefault.Body.String())
	}
	madeDefault := harness.request(
		t,
		http.MethodPost,
		"/api/account-routers/created-router/default",
		accountRouterRevisionRequest{ExpectedConfigRevision: recreatedRevision},
	)
	if madeDefault.Code != http.StatusOK {
		t.Fatalf("make default status = %d, body=%s", madeDefault.Code, madeDefault.Body.String())
	}
	defaultResource, defaultRevision := decodeAccountRouterMutationResponse(t, madeDefault.Body.Bytes())
	if !defaultResource.IsDefault || defaultRevision == "" || defaultRevision == recreatedRevision {
		t.Fatalf("default resource = %#v, revision=%q", defaultResource, defaultRevision)
	}
	loaded, err := config.LoadConfig(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Agents.Defaults.AccountRef != "created-router" ||
		loaded.Agents.Defaults.ModelName != "coding" {
		t.Fatalf("defaults = account %q model %q", loaded.Agents.Defaults.AccountRef, loaded.Agents.Defaults.ModelName)
	}
	disabledDefaultRouter := router
	disabledDefaultRouter.Enabled = false
	disableDefault := harness.request(
		t,
		http.MethodPut,
		"/api/account-routers/created-router",
		accountRouterMutationRequest{
			ExpectedConfigRevision: defaultRevision,
			AccountRouter:          &disabledDefaultRouter,
		},
	)
	if disableDefault.Code != http.StatusUnprocessableEntity ||
		decodeCollectionErrorCode(t, disableDefault.Body.Bytes()) != "invalid_account_router_configuration" {
		t.Fatalf("disable default status = %d, body=%s", disableDefault.Code, disableDefault.Body.String())
	}
	blockedDelete := harness.request(
		t,
		http.MethodDelete,
		"/api/account-routers/created-router?revision="+url.QueryEscape(defaultRevision),
		nil,
	)
	if blockedDelete.Code != http.StatusConflict ||
		decodeCollectionErrorCode(t, blockedDelete.Body.Bytes()) != "default" {
		t.Fatalf("default delete status = %d, body=%s", blockedDelete.Code, blockedDelete.Body.String())
	}
}

func TestAccountRouterBulkDeletePartialFailuresAndBounds(t *testing.T) {
	resetGatewayTestState(t)
	harness := newAgentAPITestHarness(t, configureAccountRouterCollectionFixture)
	revision, err := config.ConfigRevision(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}
	response := harness.request(t, http.MethodPost, "/api/account-routers/bulk-delete", map[string]any{
		"ids": []string{
			"default-router",
			"referenced-router",
			"free-router",
			"missing-router",
		},
		"config_revision": revision,
	})
	if response.Code != http.StatusOK {
		t.Fatalf("bulk status = %d, body=%s", response.Code, response.Body.String())
	}
	var result collectionBulkDeleteResponse
	if decodeErr := json.Unmarshal(response.Body.Bytes(), &result); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if len(result.DeletedIDs) != 1 || result.DeletedIDs[0] != "free-router" ||
		len(result.Failures) != 3 || result.ConfigRevision == "" ||
		result.ConfigRevision == revision {
		t.Fatalf("bulk response = %#v", result)
	}
	failureCodes := make(map[string]string, len(result.Failures))
	failureBlockers := make(map[string][]string, len(result.Failures))
	for _, failure := range result.Failures {
		failureCodes[failure.ID] = failure.Code
		failureBlockers[failure.ID] = failure.Blockers
	}
	if failureCodes["default-router"] != "default" ||
		failureCodes["referenced-router"] != "referenced" ||
		failureCodes["missing-router"] != "not_found" ||
		!containsString(failureBlockers["default-router"], "agents.defaults.account_ref") ||
		!containsString(failureBlockers["referenced-router"], "agents.list[0].account_ref") {
		t.Fatalf("bulk failures = %#v", result.Failures)
	}
	loaded, err := config.LoadConfig(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if findAccountRouterIndex(loaded, "free-router") >= 0 ||
		findAccountRouterIndex(loaded, "default-router") < 0 ||
		findAccountRouterIndex(loaded, "referenced-router") < 0 {
		t.Fatalf("routers after bulk = %#v", loaded.AccountRouters)
	}

	tooMany := make([]string, 201)
	for index := range tooMany {
		tooMany[index] = fmt.Sprintf("router-%03d", index)
	}
	oversized := harness.request(t, http.MethodPost, "/api/account-routers/bulk-delete", map[string]any{
		"ids": tooMany, "config_revision": result.ConfigRevision,
	})
	if oversized.Code != http.StatusBadRequest ||
		decodeCollectionErrorCode(t, oversized.Body.Bytes()) != "invalid_bulk_delete" {
		t.Fatalf("oversized bulk status = %d, body=%s", oversized.Code, oversized.Body.String())
	}
	missingRevision := harness.request(t, http.MethodPost, "/api/account-routers/bulk-delete", map[string]any{
		"ids": []string{"disabled-router"},
	})
	if missingRevision.Code != http.StatusPreconditionRequired ||
		decodeCollectionErrorCode(t, missingRevision.Body.Bytes()) != "config_revision_required" {
		t.Fatalf("missing revision status = %d, body=%s", missingRevision.Code, missingRevision.Body.String())
	}
}

func TestAccountRouterCollectionPureFunctionCoverage(t *testing.T) {
	value := 42.0
	leftValue := 3.0
	rightValue := 7.0
	router := config.AccountRouterConfig{
		Name: " nested-router ", Enabled: true, Entry: "branch",
		Blocks: []config.AccountRouterBlock{
			{
				ID: "branch", Type: config.AccountRouterBlockTypeBranch,
				Condition: &config.AccountRouterCondition{
					Left: config.AccountRouterExpression{
						Op:   config.AccountRouterMathAdd,
						Left: &config.AccountRouterExpression{Value: &leftValue},
						Right: &config.AccountRouterExpression{
							Op:    config.AccountRouterMathMultiply,
							Left:  &config.AccountRouterExpression{Value: &rightValue},
							Right: &config.AccountRouterExpression{Value: &value},
						},
					},
					Operator: config.AccountRouterBranchOpGT,
					Right:    config.AccountRouterExpression{Value: &value},
				},
				Then: "primary", Else: "fallback",
			},
			{ID: "primary", Type: config.AccountRouterBlockTypeAccount, Account: "available"},
			{
				ID: "fallback", Type: config.AccountRouterBlockTypeLoadBalance,
				Accounts: []string{"unreachable", "unconfigured"},
			},
		},
	}
	clone := normalizeAccountRouterForMutation(router)
	if clone.Name != "nested-router" || clone.Blocks[0].Condition == router.Blocks[0].Condition ||
		clone.Blocks[0].Condition.Left.Left == router.Blocks[0].Condition.Left.Left ||
		clone.Blocks[0].Condition.Left.Right == router.Blocks[0].Condition.Left.Right ||
		clone.Blocks[0].Condition.Right.Value == router.Blocks[0].Condition.Right.Value {
		t.Fatalf("router was not deeply cloned: %#v", clone)
	}
	*clone.Blocks[0].Condition.Right.Value = 100
	clone.Blocks[2].Accounts[0] = "changed"
	if *router.Blocks[0].Condition.Right.Value != 42 || router.Blocks[2].Accounts[0] != "unreachable" {
		t.Fatal("clone mutation changed source router")
	}

	tooLong := strings.Repeat("x", collectionResourceIDIdentityMaxBytes+1)
	if _, err := accountRouterResourceID(tooLong); err == nil {
		t.Fatal("oversized router name received an ID")
	}
	oversizedRouter := testAccountRouter(tooLong, true)
	if _, err := accountRouterSummaryForConfig(&config.Config{}, oversizedRouter, time.Now()); err == nil {
		t.Fatal("oversized router summary succeeded")
	}
	if _, err := accountRouterResourceForConfig(&config.Config{}, oversizedRouter, time.Now()); err == nil {
		t.Fatal("oversized router resource succeeded")
	}
	if _, err := projectAccountRouterItems(
		&config.Config{AccountRouters: config.AccountRouterList{oversizedRouter}},
		time.Now(),
	); err == nil {
		t.Fatal("oversized router collection projection succeeded")
	}
	if index, name := findAccountRouterByResourceID(nil, "router"); index != -1 || name != "" {
		t.Fatalf("nil config lookup = (%d, %q)", index, name)
	}
	brokenConfig := &config.Config{AccountRouters: config.AccountRouterList{{Name: tooLong}}}
	if index, name := findAccountRouterByResourceID(brokenConfig, "missing"); index != -1 || name != "" {
		t.Fatalf("broken config lookup = (%d, %q)", index, name)
	}

	response := httptest.NewRecorder()
	if id, ok := accountRouterIDFromPath(response, nil); ok || id != "" ||
		response.Code != http.StatusBadRequest {
		t.Fatalf("nil request path result = (%q, %t), response=%d", id, ok, response.Code)
	}

	item := accountRouterCollectionItem{Summary: accountRouterSummary{
		ID: "router", Name: "router", Enabled: true, IsDefault: true,
		Status: accountRouterStatusAvailable, Entry: "entry", Accounts: 2, Blocks: 3,
	}}
	options := accountRouterPageOptions()
	for _, field := range []collectionquery.Field{
		"name", "enabled", "is_default", "status", "entry", "accounts", "blocks",
	} {
		if _, ok := options.Resolve(item, field, time.Now()); !ok {
			t.Fatalf("field %q did not resolve", field)
		}
	}
	if _, ok := options.Resolve(item, "unknown", time.Now()); ok {
		t.Fatal("unknown router field resolved")
	}
}

func TestAccountRouterStatusCoverage(t *testing.T) {
	resetGatewayTestState(t)
	resetModelProbeHooks(t)
	probeCommandAvailableFunc = func(string) bool { return false }
	now := time.Now().UTC()
	if credentialAccountAvailableAt("credential:openai:missing", time.Time{}) {
		t.Fatal("credential was available at zero evaluation time")
	}
	if credentialProviderMatches(nil, "openai") {
		t.Fatal("nil credential matched provider")
	}
	if credentialProviderMatches(&auth.AuthCredential{Provider: "openai"}, "\x00") {
		t.Fatal("credential matched invalid provider")
	}
	originalLoadStore := oauthLoadStore
	oauthLoadStore = func() (*auth.AuthStore, error) { return nil, errors.New("store unavailable") }
	t.Cleanup(func() { oauthLoadStore = originalLoadStore })
	if credentialAccountAvailableAt("credential:openai:missing", now) {
		t.Fatal("credential was available while store failed")
	}
	oauthLoadStore = originalLoadStore
	valid := testAccountRouter("router", true)
	if got := accountRouterStatus(nil, &valid, now); got != accountRouterStatusInvalid {
		t.Fatalf("nil config status = %q", got)
	}
	if got := accountRouterStatus(&config.Config{}, nil, now); got != accountRouterStatusInvalid {
		t.Fatalf("nil router status = %q", got)
	}
	if got := accountRouterStatus(&config.Config{}, &valid, time.Time{}); got != accountRouterStatusInvalid {
		t.Fatalf("zero-time status = %q", got)
	}
	invalid := config.AccountRouterConfig{Enabled: true}
	if got := accountRouterStatus(&config.Config{}, &invalid, now); got != accountRouterStatusInvalid {
		t.Fatalf("invalid router status = %q", got)
	}

	routerFor := func(accounts ...string) config.AccountRouterConfig {
		return config.AccountRouterConfig{
			Name: "status-router", Enabled: true, Entry: "entry",
			Blocks: []config.AccountRouterBlock{{
				ID: "entry", Type: config.AccountRouterBlockTypeLoadBalance,
				Accounts: accounts,
			}},
		}
	}
	conditionValue := 1.0
	accountless := config.AccountRouterConfig{
		Name: "accountless-router", Enabled: true, Entry: "branch",
		Blocks: []config.AccountRouterBlock{{
			ID: "branch", Type: config.AccountRouterBlockTypeBranch,
			Condition: &config.AccountRouterCondition{
				Left:     config.AccountRouterExpression{Value: &conditionValue},
				Operator: config.AccountRouterBranchOpEQ,
				Right:    config.AccountRouterExpression{Value: &conditionValue},
			},
			Then: "branch", Else: "branch",
		}},
	}
	if got := accountRouterStatus(&config.Config{}, &accountless, now); got != accountRouterStatusInvalid {
		t.Fatalf("accountless router status = %q", got)
	}
	cfg := &config.Config{ModelList: []*config.ModelConfig{
		{ModelName: "unconfigured", Provider: "openai", Model: "gpt-4o", Enabled: true},
		{
			ModelName: "unreachable", Provider: "claude-cli", Model: "claude-cli",
			Enabled: true,
		},
	}}
	for _, test := range []struct {
		name     string
		router   config.AccountRouterConfig
		expected string
	}{
		{name: "unsupported credential", router: routerFor("credential:unsupported:item"), expected: accountRouterStatusInvalid},
		{name: "missing credential", router: routerFor("credential:openai:missing"), expected: accountRouterStatusUnconfigured},
		{name: "missing model", router: routerFor("missing-model"), expected: accountRouterStatusInvalid},
		{name: "unconfigured model", router: routerFor("unconfigured"), expected: accountRouterStatusUnconfigured},
		{name: "unreachable model", router: routerFor("unreachable"), expected: accountRouterStatusUnreachable},
		{
			name:   "unreachable wins over unconfigured",
			router: routerFor("unconfigured", "unreachable"), expected: accountRouterStatusUnreachable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := accountRouterStatus(cfg, &test.router, now); got != test.expected {
				t.Fatalf("status = %q, want %q", got, test.expected)
			}
		})
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
