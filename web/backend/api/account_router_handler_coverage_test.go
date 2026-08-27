package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func accountRouterCoverageMux(handler *Handler) *http.ServeMux {
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	return mux
}

func accountRouterCoverageRawRequest(
	t *testing.T,
	mux *http.ServeMux,
	method string,
	target string,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func requireAccountRouterCoverageError(
	t *testing.T,
	response *httptest.ResponseRecorder,
	status int,
	code string,
) {
	t.Helper()
	if response.Code != status || decodeCollectionErrorCode(t, response.Body.Bytes()) != code {
		t.Fatalf("status=%d body=%s, want status=%d code=%q", response.Code, response.Body.String(), status, code)
	}
}

func TestAccountRouterCollectionConfigLoadFailures(t *testing.T) {
	broken := NewHandler(t.TempDir())
	mux := accountRouterCoverageMux(broken)
	routerBody, err := json.Marshal(accountRouterMutationRequest{
		ExpectedConfigRevision: "revision",
		AccountRouter:          ptrAccountRouterCoverage(testAccountRouter("router", true)),
	})
	if err != nil {
		t.Fatal(err)
	}
	revisionBody := `{"expected_config_revision":"revision"}`
	bulkBody := `{"ids":["router"],"config_revision":"revision"}`
	for _, test := range []struct {
		name   string
		method string
		target string
		body   string
	}{
		{name: "list", method: http.MethodGet, target: "/api/account-routers"},
		{name: "detail", method: http.MethodGet, target: "/api/account-routers/router"},
		{name: "create", method: http.MethodPost, target: "/api/account-routers", body: string(routerBody)},
		{name: "update", method: http.MethodPut, target: "/api/account-routers/router", body: string(routerBody)},
		{name: "default", method: http.MethodPost, target: "/api/account-routers/router/default", body: revisionBody},
		{name: "delete", method: http.MethodDelete, target: "/api/account-routers/router?revision=revision"},
		{name: "bulk", method: http.MethodPost, target: "/api/account-routers/bulk-delete", body: bulkBody},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := accountRouterCoverageRawRequest(t, mux, test.method, test.target, test.body)
			requireAccountRouterCoverageError(t, response, http.StatusInternalServerError, "config_load_failed")
		})
	}
}

func ptrAccountRouterCoverage(router config.AccountRouterConfig) *config.AccountRouterConfig {
	return &router
}

func TestAccountRouterCollectionSaveFailures(t *testing.T) {
	for _, operation := range []string{"create", "update", "default", "delete", "bulk"} {
		t.Run(operation, func(t *testing.T) {
			harness := newAgentAPITestHarness(t, func(cfg *config.Config) {
				configureAccountRouterMutationFixture(cfg)
				if operation != "create" {
					cfg.AccountRouters = append(cfg.AccountRouters, testAccountRouter("router", true))
				}
			})
			revision, err := config.ConfigRevision(harness.configPath)
			if err != nil {
				t.Fatal(err)
			}
			harness.handler.saveConfigIfRevision = func(string, *config.Config, string) (string, error) {
				return "", errors.New("injected save failure")
			}
			var response *httptest.ResponseRecorder
			switch operation {
			case "create":
				response = harness.request(
					t,
					http.MethodPost,
					"/api/account-routers",
					accountRouterMutationRequest{
						ExpectedConfigRevision: revision,
						AccountRouter: ptrAccountRouterCoverage(
							testAccountRouter("router", true),
						),
					},
				)
			case "update":
				router := testAccountRouter("router", true)
				router.RefreshIntervalSeconds = 30
				response = harness.request(
					t,
					http.MethodPut,
					"/api/account-routers/router",
					accountRouterMutationRequest{
						ExpectedConfigRevision: revision,
						AccountRouter:          &router,
					},
				)
			case "default":
				response = harness.request(
					t,
					http.MethodPost,
					"/api/account-routers/router/default",
					accountRouterRevisionRequest{ExpectedConfigRevision: revision},
				)
			case "delete":
				response = harness.request(
					t,
					http.MethodDelete,
					"/api/account-routers/router?revision="+url.QueryEscape(revision),
					nil,
				)
			case "bulk":
				response = harness.request(t, http.MethodPost, "/api/account-routers/bulk-delete", map[string]any{
					"ids": []string{"router"}, "config_revision": revision,
				})
			}
			requireAccountRouterCoverageError(t, response, http.StatusInternalServerError, "config_save_failed")
		})
	}
}

func TestAccountRouterCollectionRequestAndLookupFailures(t *testing.T) {
	harness := newAgentAPITestHarness(t, func(cfg *config.Config) {
		configureAccountRouterMutationFixture(cfg)
		cfg.AccountRouters = append(cfg.AccountRouters, testAccountRouter("router", true))
	})
	revision, err := config.ConfigRevision(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name   string
		method string
		target string
		body   string
		status int
		code   string
	}{
		{
			name: "detail query", method: http.MethodGet,
			target: "/api/account-routers/router?query=ALL",
			status: http.StatusBadRequest, code: "invalid_collection_request",
		},
		{
			name: "create query", method: http.MethodPost,
			target: "/api/account-routers?other=x", body: `{}`,
			status: http.StatusBadRequest, code: "invalid_collection_request",
		},
		{
			name: "update query", method: http.MethodPut,
			target: "/api/account-routers/router?other=x", body: `{}`,
			status: http.StatusBadRequest, code: "invalid_collection_request",
		},
		{
			name: "default query", method: http.MethodPost,
			target: "/api/account-routers/router/default?other=x", body: `{}`,
			status: http.StatusBadRequest, code: "invalid_collection_request",
		},
		{
			name: "delete query", method: http.MethodDelete,
			target: "/api/account-routers/router?other=x",
			status: http.StatusBadRequest, code: "invalid_collection_request",
		},
		{
			name: "bulk query", method: http.MethodPost,
			target: "/api/account-routers/bulk-delete?other=x", body: `{}`,
			status: http.StatusBadRequest, code: "invalid_collection_request",
		},
		{
			name: "create malformed", method: http.MethodPost,
			target: "/api/account-routers", body: `{`,
			status: http.StatusBadRequest, code: "invalid_collection_request",
		},
		{
			name: "update malformed", method: http.MethodPut,
			target: "/api/account-routers/router", body: `{`,
			status: http.StatusBadRequest, code: "invalid_collection_request",
		},
		{
			name: "default malformed", method: http.MethodPost,
			target: "/api/account-routers/router/default", body: `{`,
			status: http.StatusBadRequest, code: "invalid_collection_request",
		},
		{
			name: "bulk malformed", method: http.MethodPost,
			target: "/api/account-routers/bulk-delete", body: `{`,
			status: http.StatusBadRequest, code: "invalid_collection_request",
		},
		{
			name: "default invalid id", method: http.MethodPost,
			target: "/api/account-routers/%20bad%20/default", body: `{}`,
			status: http.StatusBadRequest, code: "invalid_account_router_id",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := accountRouterCoverageRawRequest(t, harness.mux, test.method, test.target, test.body)
			requireAccountRouterCoverageError(t, response, test.status, test.code)
		})
	}

	nilRouter := harness.request(t, http.MethodPost, "/api/account-routers", accountRouterMutationRequest{
		ExpectedConfigRevision: revision,
	})
	requireAccountRouterCoverageError(t, nilRouter, http.StatusBadRequest, "invalid_account_router")
	structurallyInvalid := config.AccountRouterConfig{Name: "invalid", Enabled: true}
	invalidUpdate := harness.request(t, http.MethodPut, "/api/account-routers/router", accountRouterMutationRequest{
		ExpectedConfigRevision: revision, AccountRouter: &structurallyInvalid,
	})
	requireAccountRouterCoverageError(t, invalidUpdate, http.StatusUnprocessableEntity, "invalid_account_router")

	duplicate := harness.request(t, http.MethodPost, "/api/account-routers", accountRouterMutationRequest{
		ExpectedConfigRevision: revision,
		AccountRouter:          ptrAccountRouterCoverage(testAccountRouter("router", true)),
	})
	requireAccountRouterCoverageError(t, duplicate, http.StatusConflict, "account_router_exists")
	missingRouter := testAccountRouter("missing", true)
	missingUpdate := harness.request(t, http.MethodPut, "/api/account-routers/missing", accountRouterMutationRequest{
		ExpectedConfigRevision: revision, AccountRouter: &missingRouter,
	})
	requireAccountRouterCoverageError(t, missingUpdate, http.StatusNotFound, "account_router_not_found")
	missingDefault := harness.request(
		t,
		http.MethodPost,
		"/api/account-routers/missing/default",
		accountRouterRevisionRequest{ExpectedConfigRevision: revision},
	)
	requireAccountRouterCoverageError(t, missingDefault, http.StatusNotFound, "account_router_not_found")
	missingDelete := harness.request(
		t,
		http.MethodDelete,
		"/api/account-routers/missing?revision="+url.QueryEscape(revision),
		nil,
	)
	requireAccountRouterCoverageError(t, missingDelete, http.StatusNotFound, "account_router_not_found")
}

func TestAccountRouterCollectionDefaultAndBulkBoundaries(t *testing.T) {
	harness := newAgentAPITestHarness(t, func(cfg *config.Config) {
		configureAccountRouterMutationFixture(cfg)
		unavailable := testAccountRouter("unavailable", true)
		unavailable.Blocks[0].Account = "credential:openai:missing"
		cfg.AccountRouters = append(cfg.AccountRouters, unavailable)
	})
	revision, err := config.ConfigRevision(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}
	unavailable := harness.request(
		t,
		http.MethodPost,
		"/api/account-routers/unavailable/default",
		accountRouterRevisionRequest{ExpectedConfigRevision: revision},
	)
	requireAccountRouterCoverageError(t, unavailable, http.StatusConflict, "account_router_unavailable")

	invalidOnly := harness.request(t, http.MethodPost, "/api/account-routers/bulk-delete", map[string]any{
		"ids": []string{"bad/id", "missing"}, "config_revision": revision,
	})
	if invalidOnly.Code != http.StatusOK || !strings.Contains(invalidOnly.Body.String(), `"invalid_id"`) ||
		!strings.Contains(invalidOnly.Body.String(), `"not_found"`) {
		t.Fatalf("invalid-only bulk status=%d body=%s", invalidOnly.Code, invalidOnly.Body.String())
	}
	conflictingRevision := accountRouterCoverageRawRequest(
		t,
		harness.mux,
		http.MethodPost,
		"/api/account-routers/bulk-delete",
		`{"ids":["missing"],"config_revision":"one","expected_config_revision":"two"}`,
	)
	requireAccountRouterCoverageError(t, conflictingRevision, http.StatusBadRequest, "conflicting_config_revision")
	missingDeleteRevision := harness.request(t, http.MethodDelete, "/api/account-routers/unavailable", nil)
	requireAccountRouterCoverageError(
		t,
		missingDeleteRevision,
		http.StatusPreconditionRequired,
		"config_revision_required",
	)
}

func TestAccountRouterCollectionRejectsInvalidMutationCandidates(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		harness := newAgentAPITestHarness(t, func(cfg *config.Config) {
			configureAccountRouterMutationFixture(cfg)
			cfg.ModelList = append(cfg.ModelList, &config.ModelConfig{
				ModelName: "anthropic-account", Provider: "anthropic", Model: "claude-sonnet-4",
				APIKeys: config.SimpleSecureStrings("private"), Enabled: true,
			})
			cfg.ModelAliases[0].DisabledAccounts = []string{"anthropic-account"}
			router := testAccountRouter("router", true)
			router.Blocks[0].Account = "anthropic-account"
			cfg.AccountRouters = append(cfg.AccountRouters, router)
		})
		revision, err := config.ConfigRevision(harness.configPath)
		if err != nil {
			t.Fatal(err)
		}
		response := harness.request(
			t,
			http.MethodPost,
			"/api/account-routers/router/default",
			accountRouterRevisionRequest{ExpectedConfigRevision: revision},
		)
		requireAccountRouterCoverageError(
			t,
			response,
			http.StatusUnprocessableEntity,
			"invalid_account_router_configuration",
		)
	})
}
