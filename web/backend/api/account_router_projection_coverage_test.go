package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestAccountRouterDefaultProjectionFailureCoverage(t *testing.T) {
	resetGatewayTestState(t)
	harness := newAgentAPITestHarness(t, func(cfg *config.Config) {
		configureAccountRouterMutationFixture(cfg)
		cfg.AccountRouters = append(cfg.AccountRouters, testAccountRouter("projection-router", true))
	})
	revision, err := config.ConfigRevision(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}
	harness.handler.saveConfigIfRevision = func(
		_ string,
		candidate *config.Config,
		_ string,
	) (string, error) {
		index := findAccountRouterIndex(candidate, "projection-router")
		if index < 0 {
			t.Fatal("projection router missing from candidate")
		}
		candidate.AccountRouters[index].Name = strings.Repeat(
			"x",
			collectionResourceIDIdentityMaxBytes+1,
		)
		return revision + "-saved", nil
	}

	response := harness.request(
		t,
		http.MethodPost,
		"/api/account-routers/projection-router/default",
		accountRouterRevisionRequest{ExpectedConfigRevision: revision},
	)
	if response.Code != http.StatusInternalServerError ||
		decodeCollectionErrorCode(t, response.Body.Bytes()) != "account_router_projection_failed" {
		t.Fatalf("projection failure status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAccountRouterListProjectionFailureCoverage(t *testing.T) {
	resetGatewayTestState(t)
	oversizedRouter := testAccountRouter(
		strings.Repeat("x", collectionResourceIDIdentityMaxBytes+1),
		true,
	)
	harness := newAgentAPITestHarness(t, func(cfg *config.Config) {
		cfg.ModelList = []*config.ModelConfig{configuredAccountForRouterTests()}
		cfg.AccountRouters = config.AccountRouterList{oversizedRouter}
	})

	response := harness.request(t, http.MethodGet, "/api/account-routers", nil)
	if response.Code != http.StatusInternalServerError ||
		decodeCollectionErrorCode(t, response.Body.Bytes()) != "account_router_projection_failed" {
		t.Fatalf("list projection failure status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAccountRouterInjectedDependencyFailures(t *testing.T) {
	t.Run("page", func(t *testing.T) {
		harness := newAgentAPITestHarness(t, configureAccountRouterCollectionFixture)
		harness.handler.pageAccountRouters = func(
			[]accountRouterCollectionItem,
			collectionListRequest,
		) (collectionquery.PageResult[accountRouterCollectionItem], error) {
			return collectionquery.PageResult[accountRouterCollectionItem]{}, errors.New("page failure")
		}
		response := harness.request(t, http.MethodGet, "/api/account-routers", nil)
		requireAccountRouterCoverageError(
			t,
			response,
			http.StatusInternalServerError,
			"collection_page_failed",
		)
	})

	for _, operation := range []string{"detail", "create", "update"} {
		t.Run(operation+" projection", func(t *testing.T) {
			harness := newAgentAPITestHarness(t, func(cfg *config.Config) {
				configureAccountRouterMutationFixture(cfg)
				cfg.AccountRouters = append(cfg.AccountRouters, testAccountRouter("router", true))
			})
			harness.handler.projectAccountRouterResource = func(
				*config.Config,
				config.AccountRouterConfig,
				time.Time,
			) (accountRouterResource, error) {
				return accountRouterResource{}, errors.New("projection failure")
			}
			revision, err := config.ConfigRevision(harness.configPath)
			if err != nil {
				t.Fatal(err)
			}
			var responseStatus int
			var responseBody []byte
			switch operation {
			case "detail":
				response := harness.request(t, http.MethodGet, "/api/account-routers/router", nil)
				responseStatus, responseBody = response.Code, response.Body.Bytes()
			case "create":
				response := harness.request(
					t,
					http.MethodPost,
					"/api/account-routers",
					accountRouterMutationRequest{
						ExpectedConfigRevision: revision,
						AccountRouter: ptrAccountRouterCoverage(
							testAccountRouter("created", true),
						),
					},
				)
				responseStatus, responseBody = response.Code, response.Body.Bytes()
			case "update":
				router := testAccountRouter("router", true)
				router.RefreshIntervalSeconds = 30
				response := harness.request(
					t,
					http.MethodPut,
					"/api/account-routers/router",
					accountRouterMutationRequest{
						ExpectedConfigRevision: revision,
						AccountRouter:          &router,
					},
				)
				responseStatus, responseBody = response.Code, response.Body.Bytes()
			}
			if responseStatus != http.StatusInternalServerError ||
				decodeCollectionErrorCode(t, responseBody) != "account_router_projection_failed" {
				t.Fatalf("status=%d body=%s", responseStatus, responseBody)
			}
		})
	}

	for _, operation := range []string{"delete", "bulk"} {
		t.Run(operation+" candidate", func(t *testing.T) {
			harness := newAgentAPITestHarness(t, func(cfg *config.Config) {
				configureAccountRouterMutationFixture(cfg)
				cfg.AccountRouters = append(cfg.AccountRouters, testAccountRouter("router", true))
			})
			harness.handler.validateAccountRouterCandidate = func(*config.Config) error {
				return errors.New("candidate failure")
			}
			revision, err := config.ConfigRevision(harness.configPath)
			if err != nil {
				t.Fatal(err)
			}
			var responseStatus int
			var responseBody []byte
			if operation == "delete" {
				response := harness.request(
					t,
					http.MethodDelete,
					"/api/account-routers/router?revision="+revision,
					nil,
				)
				responseStatus, responseBody = response.Code, response.Body.Bytes()
			} else {
				response := harness.request(
					t,
					http.MethodPost,
					"/api/account-routers/bulk-delete",
					map[string]any{"ids": []string{"router"}, "config_revision": revision},
				)
				responseStatus, responseBody = response.Code, response.Body.Bytes()
			}
			if responseStatus != http.StatusUnprocessableEntity ||
				decodeCollectionErrorCode(t, responseBody) != "invalid_account_router_configuration" {
				t.Fatalf("status=%d body=%s", responseStatus, responseBody)
			}
		})
	}
}
