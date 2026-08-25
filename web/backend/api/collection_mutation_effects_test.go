package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"

	picoauth "github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
)

func requireCollectionMutationEffects(
	t *testing.T,
	response *httptest.ResponseRecorder,
) agentEffects {
	t.Helper()
	var envelope struct {
		Effects agentEffects `json:"effects"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("json.Unmarshal(effects) error = %v; body=%s", err, response.Body.String())
	}
	if envelope.Effects.LauncherEffect != "applied" ||
		envelope.Effects.CatalogEffect != "applied" ||
		(envelope.Effects.GatewayEffect != "applied" &&
			envelope.Effects.GatewayEffect != "restart_required") {
		t.Fatalf("effects = %#v; body=%s", envelope.Effects, response.Body.String())
	}
	return envelope.Effects
}

func TestNameAddressedModelMutationsReportRestartEffects(t *testing.T) {
	configPath := modelAliasAPIConfig(t)
	revision, err := config.ConfigRevision(configPath)
	if err != nil {
		t.Fatal(err)
	}

	alias := config.ModelAliasConfig{Name: "effects-alias", Model: "gpt-5.4"}
	createdAlias := serveCollectionMutationJSON(t, configPath, http.MethodPost, "/api/model-aliases", map[string]any{
		"expected_config_revision": revision,
		"model_alias":              alias,
	})
	requireCollectionStatusAndEffects(t, createdAlias, http.StatusCreated)
	revision = collectionMutationRevision(t, createdAlias)

	alias.Model = "gpt-5.4-mini"
	updatedAlias := serveCollectionMutationJSON(
		t,
		configPath,
		http.MethodPut,
		"/api/model-aliases/effects-alias",
		map[string]any{
			"expected_config_revision": revision,
			"model_alias":              alias,
		},
	)
	requireCollectionStatusAndEffects(t, updatedAlias, http.StatusOK)
	revision = collectionMutationRevision(t, updatedAlias)

	deletedAlias := serveCollectionRaw(
		t, configPath, http.MethodDelete,
		"/api/model-aliases/effects-alias?revision="+url.QueryEscape(revision),
		"", "", nil,
	)
	requireCollectionStatusAndEffects(t, deletedAlias, http.StatusOK)
	revision = collectionMutationRevision(t, deletedAlias)

	createdAlias = serveCollectionMutationJSON(t, configPath, http.MethodPost, "/api/model-aliases", map[string]any{
		"expected_config_revision": revision,
		"model_alias":              alias,
	})
	requireCollectionStatusAndEffects(t, createdAlias, http.StatusCreated)
	revision = collectionMutationRevision(t, createdAlias)
	bulkDeletedAlias := serveCollectionMutationJSON(
		t, configPath, http.MethodPost, "/api/model-aliases/bulk-delete", map[string]any{
			"ids": []string{"effects-alias"}, "config_revision": revision,
		},
	)
	requireCollectionStatusAndEffects(t, bulkDeletedAlias, http.StatusOK)
	revision = collectionMutationRevision(t, bulkDeletedAlias)

	router := config.ModelRouterConfig{
		Name: "effects-router", Enabled: true, Entry: "entry",
		Blocks: []config.ModelRouterBlock{
			{
				ID: "entry", Type: config.ModelRouterBlockTypeRules, Fallback: "fallback",
				Rules: []config.ModelRouterRule{{Match: config.ModelRouterRuleHasCode, Target: "code"}},
			},
			{ID: "code", Type: config.ModelRouterBlockTypeModel, Model: "coding"},
			{ID: "fallback", Type: config.ModelRouterBlockTypeModel, Model: "coding"},
		},
	}
	createdRouter := serveCollectionMutationJSON(t, configPath, http.MethodPost, "/api/model-routers", map[string]any{
		"expected_config_revision": revision,
		"model_router":             router,
	})
	requireCollectionStatusAndEffects(t, createdRouter, http.StatusCreated)
	revision = collectionMutationRevision(t, createdRouter)

	router.Blocks[0].Rules = append(router.Blocks[0].Rules, config.ModelRouterRule{
		Match: config.ModelRouterRuleHasMedia, Target: "code",
	})
	updatedRouter := serveCollectionMutationJSON(
		t,
		configPath,
		http.MethodPut,
		"/api/model-routers/effects-router",
		map[string]any{
			"expected_config_revision": revision,
			"model_router":             router,
		},
	)
	requireCollectionStatusAndEffects(t, updatedRouter, http.StatusOK)
	revision = collectionMutationRevision(t, updatedRouter)

	deletedRouter := serveCollectionRaw(
		t, configPath, http.MethodDelete,
		"/api/model-routers/effects-router?revision="+url.QueryEscape(revision),
		"", "", nil,
	)
	requireCollectionStatusAndEffects(t, deletedRouter, http.StatusOK)
	revision = collectionMutationRevision(t, deletedRouter)

	createdRouter = serveCollectionMutationJSON(t, configPath, http.MethodPost, "/api/model-routers", map[string]any{
		"expected_config_revision": revision,
		"model_router":             router,
	})
	requireCollectionStatusAndEffects(t, createdRouter, http.StatusCreated)
	revision = collectionMutationRevision(t, createdRouter)

	bulkDeletedRouter := serveCollectionMutationJSON(
		t, configPath, http.MethodPost, "/api/model-routers/bulk-delete", map[string]any{
			"ids": []string{"effects-router"}, "config_revision": revision,
		},
	)
	requireCollectionStatusAndEffects(t, bulkDeletedRouter, http.StatusOK)
}

func TestModelRouterMutationRequiresRestartForRunningGeneration(t *testing.T) {
	resetGatewayTestState(t)
	configPath := modelAliasAPIConfig(t)
	cfg, revision, err := config.LoadCurrentConfigSnapshot(configPath)
	if err != nil {
		t.Fatal(err)
	}
	process := startLongRunningProcess(t)
	t.Cleanup(func() {
		if process.ProcessState == nil {
			_ = process.Process.Kill()
			_, _ = process.Process.Wait()
		}
	})
	gateway.mu.Lock()
	gateway.cmd = process
	gateway.bootConfigSignature = computeGatewayRuntimeSignature(cfg)
	setGatewayRuntimeStatusLocked("running")
	gateway.mu.Unlock()

	router := config.ModelRouterConfig{
		Name: "effects-router", Enabled: true, Entry: "entry",
		Blocks: []config.ModelRouterBlock{
			{
				ID: "entry", Type: config.ModelRouterBlockTypeRules, Fallback: "fallback",
				Rules: []config.ModelRouterRule{{Match: config.ModelRouterRuleHasCode, Target: "code"}},
			},
			{ID: "code", Type: config.ModelRouterBlockTypeModel, Model: "coding"},
			{ID: "fallback", Type: config.ModelRouterBlockTypeModel, Model: "coding"},
		},
	}
	created := serveCollectionMutationJSON(t, configPath, http.MethodPost, "/api/model-routers", map[string]any{
		"expected_config_revision": revision,
		"model_router":             router,
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	if effect := requireCollectionMutationEffects(t, created); effect.GatewayEffect != "restart_required" {
		t.Fatalf("router create gateway effect=%q, want restart_required", effect.GatewayEffect)
	}
}

func TestMCPServerCRUDAndBulkDeleteReportRestartEffects(t *testing.T) {
	harness := newMCPAPITestHarness(t, func(cfg *config.Config) {
		cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{}
	})

	created := harness.request(t, http.MethodPost, "/api/mcp/servers", map[string]any{
		"name": "effects-local", "type": "stdio", "command": "first", "enabled": true,
	})
	requireCollectionStatusAndEffects(t, created, http.StatusOK)
	revision, err := config.ConfigRevision(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}

	updated := harness.request(t, http.MethodPut, "/api/mcp/servers/effects-local", map[string]any{
		"name": "effects-local", "type": "stdio", "command": "second", "enabled": true,
		"expected_config_revision": revision,
	})
	requireCollectionStatusAndEffects(t, updated, http.StatusOK)

	deleted := harness.request(t, http.MethodDelete, "/api/mcp/servers/effects-local", nil)
	requireCollectionStatusAndEffects(t, deleted, http.StatusOK)

	created = harness.request(t, http.MethodPost, "/api/mcp/servers", map[string]any{
		"name": "effects-local", "type": "stdio", "command": "third", "enabled": true,
	})
	requireCollectionStatusAndEffects(t, created, http.StatusOK)
	revision, err = config.ConfigRevision(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}
	bulkDeleted := harness.request(t, http.MethodPost, "/api/mcp/servers/bulk-delete", map[string]any{
		"ids": []string{"effects-local"}, "config_revision": revision,
	})
	requireCollectionStatusAndEffects(t, bulkDeleted, http.StatusOK)
}

func TestMCPBulkDeleteRemovesFinalUnreferencedCredential(t *testing.T) {
	const credentialID = "mcp:cleanup-success"
	harness := newMCPAPITestHarness(t, func(cfg *config.Config) {
		cfg.Tools.MCP.Enabled = true
		cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
			"cleanup-success": {
				Enabled: true, Type: "http", URL: "https://example.test/mcp",
				Auth: &config.MCPServerAuthConfig{Type: "bearer", CredentialID: credentialID},
			},
		}
	})
	if err := picoauth.SetCredential(credentialID, &picoauth.AuthCredential{
		Provider: "mcp", AuthMethod: "bearer", AccessToken: "secret",
	}); err != nil {
		t.Fatal(err)
	}
	revision, err := config.ConfigRevision(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}
	deleted := harness.request(t, http.MethodPost, "/api/mcp/servers/bulk-delete", map[string]any{
		"ids": []string{"cleanup-success"}, "config_revision": revision,
	})
	requireCollectionStatusAndEffects(t, deleted, http.StatusOK)
	var result collectionBulkDeleteResponse
	if decodeErr := json.Unmarshal(deleted.Body.Bytes(), &result); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if !slices.Equal(result.DeletedIDs, []string{"cleanup-success"}) ||
		len(result.Failures) != 0 || len(result.CleanupFailures) != 0 {
		t.Fatalf("bulk result = %#v", result)
	}
	if credential, getErr := picoauth.GetCredential(credentialID); getErr != nil || credential != nil {
		t.Fatalf("credential after cleanup = %#v, err=%v", credential, getErr)
	}
}

func TestMCPMutationsReportEffectsAndBulkCleanupFailures(t *testing.T) {
	const credentialID = "mcp:cleanup-effect"
	harness := newMCPAPITestHarness(t, func(cfg *config.Config) {
		cfg.Tools.MCP.Enabled = true
		cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
			"cleanup-effect": {
				Enabled: true, Type: "http", URL: "https://example.test/mcp",
				Auth: &config.MCPServerAuthConfig{Type: "bearer", CredentialID: credentialID},
			},
		}
	})
	if err := picoauth.SetCredential(credentialID, &picoauth.AuthCredential{
		Provider: "mcp", AuthMethod: "bearer", AccessToken: "secret",
	}); err != nil {
		t.Fatal(err)
	}

	revision, err := config.ConfigRevision(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}
	originalDeleteCredential := mcpDeleteCredential
	mcpDeleteCredential = func(id string) error {
		if id == credentialID {
			return errors.New("injected credential-store failure")
		}
		return originalDeleteCredential(id)
	}
	t.Cleanup(func() { mcpDeleteCredential = originalDeleteCredential })

	deleted := harness.request(t, http.MethodPost, "/api/mcp/servers/bulk-delete", map[string]any{
		"ids": []string{"cleanup-effect"}, "config_revision": revision,
	})
	requireCollectionStatusAndEffects(t, deleted, http.StatusOK)
	var result collectionBulkDeleteResponse
	if decodeErr := json.Unmarshal(deleted.Body.Bytes(), &result); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if !slices.Equal(result.DeletedIDs, []string{"cleanup-effect"}) ||
		len(result.Failures) != 0 || len(result.CleanupFailures) != 1 ||
		result.CleanupFailures[0].ID != "cleanup-effect" ||
		result.CleanupFailures[0].Code != "credential_cleanup_failed" {
		t.Fatalf("bulk result = %#v", result)
	}
	if credential, getErr := picoauth.GetCredential(credentialID); getErr != nil || credential == nil {
		t.Fatalf("credential after injected cleanup failure = %#v, err=%v", credential, getErr)
	}
	loaded, err := config.LoadConfig(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := loaded.Tools.MCP.Servers["cleanup-effect"]; exists {
		t.Fatal("server remained after response reported it deleted")
	}
}

func serveCollectionMutationJSON(
	t *testing.T,
	configPath, method, path string,
	payload any,
) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return serveCollectionRaw(t, configPath, method, path, string(body), "application/json", nil)
}

func requireCollectionStatusAndEffects(
	t *testing.T,
	response *httptest.ResponseRecorder,
	wantStatus int,
) {
	t.Helper()
	if response.Code != wantStatus {
		t.Fatalf("status=%d, want=%d; body=%s", response.Code, wantStatus, response.Body.String())
	}
	requireCollectionMutationEffects(t, response)
}

func collectionMutationRevision(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var envelope struct {
		ConfigRevision string `json:"config_revision"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.ConfigRevision == "" {
		t.Fatalf("missing config_revision; body=%s", response.Body.String())
	}
	return envelope.ConfigRevision
}
