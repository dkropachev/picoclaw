package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	picoauth "github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
	picomcp "github.com/sipeed/picoclaw/pkg/mcp"
	"github.com/sipeed/picoclaw/pkg/repoeval"
)

func serveCollectionRaw(
	t *testing.T,
	configPath, method, path, body, contentType string,
	headers map[string]string,
) *httptest.ResponseRecorder {
	t.Helper()
	handler := NewHandler(configPath)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func TestModelAliasCollectionQueryPagingDetailAndMixedBulkDelete(t *testing.T) {
	configPath := modelAliasAPIConfig(t)
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ModelAliases = append(cfg.ModelAliases,
		config.ModelAliasConfig{Name: "analysis", Model: "gpt-5.4-mini"},
		config.ModelAliasConfig{Name: "review", Model: "gpt-5.4"},
	)
	cfg.Agents.Defaults.ModelName = "coding"
	if saveErr := config.SaveConfig(configPath, cfg); saveErr != nil {
		t.Fatal(saveErr)
	}

	first := serveModelAliasRequest(
		t, configPath, http.MethodGet,
		"/api/model-aliases?query="+url.QueryEscape(`model ~ "gpt" ORDER BY name ASC`)+"&limit=1", "",
	)
	if first.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	if strings.Contains(first.Body.String(), `"account_overrides":`) ||
		strings.Contains(first.Body.String(), `"disabled_accounts":`) {
		t.Fatalf("alias collection leaked detail configuration: %s", first.Body.String())
	}
	var page struct {
		Aliases        []config.ModelAliasConfig `json:"model_aliases"`
		Total          int                       `json:"total"`
		NextCursor     string                    `json:"next_cursor"`
		CanonicalQuery string                    `json:"canonical_query"`
		QuerySchema    json.RawMessage           `json:"query_schema"`
		Revision       string                    `json:"config_revision"`
	}
	if decodeErr := json.Unmarshal(first.Body.Bytes(), &page); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if len(page.Aliases) != 1 || page.Total != 3 || page.NextCursor == "" ||
		page.CanonicalQuery == "" || len(page.QuerySchema) == 0 || page.Revision == "" {
		t.Fatalf("page=%#v", page)
	}
	second := serveModelAliasRequest(
		t, configPath, http.MethodGet,
		"/api/model-aliases?query="+url.QueryEscape(`model ~ "gpt" ORDER BY name ASC`)+
			"&limit=1&cursor="+url.QueryEscape(page.NextCursor), "",
	)
	if second.Code != http.StatusOK {
		t.Fatalf("second status=%d body=%s", second.Code, second.Body.String())
	}
	mismatch := serveModelAliasRequest(
		t, configPath, http.MethodGet,
		"/api/model-aliases?query="+url.QueryEscape(`name = coding ORDER BY name ASC`)+
			"&limit=1&cursor="+url.QueryEscape(page.NextCursor), "",
	)
	if mismatch.Code != http.StatusBadRequest {
		t.Fatalf("cursor mismatch status=%d body=%s", mismatch.Code, mismatch.Body.String())
	}
	detail := serveModelAliasRequest(t, configPath, http.MethodGet, "/api/model-aliases/analysis", "")
	if detail.Code != http.StatusOK || !json.Valid(detail.Body.Bytes()) {
		t.Fatalf("detail status=%d body=%s", detail.Code, detail.Body.String())
	}

	revision, err := config.ConfigRevision(configPath)
	if err != nil {
		t.Fatal(err)
	}
	bulk := serveModelAliasRequest(
		t, configPath, http.MethodPost, "/api/model-aliases/bulk-delete",
		`{"ids":["analysis","coding","missing","review","review"],"config_revision":"`+revision+`"}`,
	)
	if bulk.Code != http.StatusOK {
		t.Fatalf("bulk status=%d body=%s", bulk.Code, bulk.Body.String())
	}
	var result collectionBulkDeleteResponse
	if decodeErr := json.Unmarshal(bulk.Body.Bytes(), &result); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if len(result.DeletedIDs) != 1 || result.DeletedIDs[0] != "analysis" || len(result.Failures) != 3 {
		t.Fatalf("bulk result=%#v", result)
	}
	loaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if findModelAliasIndexByName(loaded, "analysis") >= 0 ||
		findModelAliasIndexByName(loaded, "coding") < 0 ||
		findModelAliasIndexByName(loaded, "review") < 0 {
		t.Fatalf("aliases after mixed delete=%#v", loaded.ModelAliases)
	}
}

func TestModelRouterCollectionReturnsSummaryRatherThanConfiguration(t *testing.T) {
	configPath := modelAliasAPIConfig(t)
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ModelRouters = []config.ModelRouterConfig{{
		Name: "task-router", Enabled: true, Entry: "entry",
		Blocks: []config.ModelRouterBlock{
			{
				ID:       "entry",
				Type:     config.ModelRouterBlockTypeRules,
				Fallback: "fallback",
				Rules:    []config.ModelRouterRule{{Match: config.ModelRouterRuleHasCode, Target: "code"}},
			},
			{ID: "code", Type: config.ModelRouterBlockTypeModel, Model: "coding"},
			{ID: "fallback", Type: config.ModelRouterBlockTypeModel, Model: "coding"},
		},
	}}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	response := serveModelAliasRequest(t, configPath, http.MethodGet, "/api/model-routers", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), `"blocks":`) ||
		strings.Contains(response.Body.String(), `"rules":`) {
		t.Fatalf("router collection leaked detail configuration: %s", response.Body.String())
	}
	var page struct {
		Routers []modelRouterSummary `json:"model_routers"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Routers) != 1 || page.Routers[0].BlockCount != 3 || page.Routers[0].RuleCount != 1 {
		t.Fatalf("router summaries=%#v", page.Routers)
	}
}

func TestCollectionRequestBoundariesFencesAndResponseHeaders(t *testing.T) {
	configPath := modelAliasAPIConfig(t)
	revision, err := config.ConfigRevision(configPath)
	if err != nil {
		t.Fatal(err)
	}
	validBody := `{"expected_config_revision":"` + revision + `","model_alias":{"name":"new-alias","model":"gpt-5.4"}}`

	missingType := serveCollectionRaw(
		t, configPath, http.MethodPost, "/api/model-aliases", validBody, "", nil,
	)
	if missingType.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("missing content type status=%d body=%s", missingType.Code, missingType.Body.String())
	}
	duplicate := serveCollectionRaw(
		t,
		configPath,
		http.MethodPost,
		"/api/model-aliases",
		`{"expected_config_revision":"`+revision+`","model_alias":{"name":"one","model":"gpt-5.4"},"MODEL_ALIAS":{"name":"two","model":"gpt-5.4"}}`,
		"application/json",
		nil,
	)
	if duplicate.Code != http.StatusBadRequest {
		t.Fatalf("duplicate JSON status=%d body=%s", duplicate.Code, duplicate.Body.String())
	}
	oversized := serveCollectionRaw(
		t, configPath, http.MethodPost, "/api/model-aliases",
		`{"padding":"`+strings.Repeat("x", collectionMutationMaxBytes)+`"}`,
		"application/json", nil,
	)
	if oversized.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status=%d body=%s", oversized.Code, oversized.Body.String())
	}
	conflicting := serveCollectionRaw(
		t, configPath, http.MethodPost, "/api/model-aliases", validBody,
		"application/json", map[string]string{"If-Match": "different"},
	)
	if conflicting.Code != http.StatusBadRequest {
		t.Fatalf("conflicting fence status=%d body=%s", conflicting.Code, conflicting.Body.String())
	}
	stale := serveCollectionRaw(
		t, configPath, http.MethodPost, "/api/model-aliases",
		`{"expected_config_revision":"sha256:stale","model_alias":{"name":"stale-alias","model":"gpt-5.4"}}`,
		"application/json", nil,
	)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale fence status=%d body=%s", stale.Code, stale.Body.String())
	}
	crossOrigin := serveCollectionRaw(
		t, configPath, http.MethodPost, "/api/model-aliases", validBody,
		"application/json", map[string]string{
			"Origin": "https://attacker.invalid", "Sec-Fetch-Site": "cross-site",
		},
	)
	if crossOrigin.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status=%d body=%s", crossOrigin.Code, crossOrigin.Body.String())
	}
	unknownQuery := serveCollectionRaw(
		t, configPath, http.MethodPost, "/api/model-aliases?unexpected=1", validBody,
		"application/json", nil,
	)
	if unknownQuery.Code != http.StatusBadRequest {
		t.Fatalf("unknown mutation query status=%d body=%s", unknownQuery.Code, unknownQuery.Body.String())
	}
	detailQuery := serveCollectionRaw(
		t, configPath, http.MethodGet, "/api/model-aliases/coding?unexpected=1", "", "", nil,
	)
	if detailQuery.Code != http.StatusBadRequest {
		t.Fatalf("detail query status=%d body=%s", detailQuery.Code, detailQuery.Body.String())
	}
	malformedQueryRequest := httptest.NewRequest(http.MethodGet, "/api/model-aliases", nil)
	malformedQueryRequest.URL.RawQuery = "query=%zz"
	handler := NewHandler(configPath)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	malformedQuery := httptest.NewRecorder()
	mux.ServeHTTP(malformedQuery, malformedQueryRequest)
	if malformedQuery.Code != http.StatusBadRequest {
		t.Fatalf("malformed list query status=%d body=%s", malformedQuery.Code, malformedQuery.Body.String())
	}

	created := serveCollectionRaw(
		t, configPath, http.MethodPost, "/api/model-aliases", validBody,
		"application/json; charset=utf-8", map[string]string{"Sec-Fetch-Site": "same-origin"},
	)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	if contentType := created.Header().Get("Content-Type"); contentType != "application/json; charset=utf-8" {
		t.Fatalf("create Content-Type=%q", contentType)
	}
	if created.Header().Get("Cache-Control") != "no-store" ||
		created.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("create safety headers=%v", created.Header())
	}
	if created.Header().Get("Location") != "/api/model-aliases/new-alias" {
		t.Fatalf("create Location=%q", created.Header().Get("Location"))
	}
	loaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if findModelAliasIndexByName(loaded, "new-alias") < 0 || len(loaded.ModelAliases) != 2 {
		t.Fatalf("aliases after boundary tests=%#v", loaded.ModelAliases)
	}
}

func TestAgentBulkDeleteRecomputesSelectionAwareBlockers(t *testing.T) {
	harness := newAgentAPITestHarness(t, func(cfg *config.Config) {
		cfg.Agents.List = []config.AgentConfig{
			{ID: "main", Default: true, Subagents: &config.SubagentsConfig{AllowAgents: []string{"reviewer"}}},
			{ID: "reviewer", Subagents: &config.SubagentsConfig{AllowAgents: []string{"worker"}}},
			{ID: "worker"},
		}
	})
	revision, err := config.ConfigRevision(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}
	blocked := harness.request(t, http.MethodPost, "/api/agents/bulk-delete", map[string]any{
		"ids": []string{"reviewer", "worker"}, "config_revision": revision,
	})
	if blocked.Code != http.StatusOK {
		t.Fatalf("blocked status=%d body=%s", blocked.Code, blocked.Body.String())
	}
	var result agentBulkDeleteResponse
	if decodeErr := json.Unmarshal(blocked.Body.Bytes(), &result); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if len(result.DeletedIDs) != 0 || len(result.Failures) != 2 ||
		result.Failures[0].ID != "reviewer" || result.Failures[1].ID != "worker" {
		t.Fatalf("fixed-point result=%#v", result)
	}

	cfg, err := config.LoadConfig(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agents.List[0].Subagents = nil
	if saveErr := config.SaveConfig(harness.configPath, cfg); saveErr != nil {
		t.Fatal(saveErr)
	}
	revision, err = config.ConfigRevision(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}
	deleted := harness.request(t, http.MethodPost, "/api/agents/bulk-delete", map[string]any{
		"ids": []string{"reviewer", "worker"}, "config_revision": revision,
	})
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	if err := json.Unmarshal(deleted.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.DeletedIDs) != 2 || len(result.Failures) != 0 {
		t.Fatalf("co-delete result=%#v", result)
	}
}

func TestMCPServerCollectionFiltersAndLoadsDetail(t *testing.T) {
	harness := newMCPAPITestHarness(t, func(cfg *config.Config) {
		cfg.Tools.MCP.Enabled = true
		cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
			"local": {
				Enabled: true, Type: "stdio", Command: "/usr/local/bin/tool",
				Args: []string{"secret-argument"}, EnvFile: "/private/mcp.env",
				Env: map[string]string{"TOKEN": "secret"},
			},
			"remote": {Enabled: false, Type: "http", URL: "https://example.test/mcp"},
		}
	})
	list := harness.request(
		t, http.MethodGet,
		"/api/mcp/servers?query="+url.QueryEscape(`enabled = true ORDER BY name ASC`), nil,
	)
	if list.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	var page struct {
		Servers []mcpServerCollectionSummary `json:"servers"`
		Total   int                          `json:"total"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Servers) != 1 || page.Servers[0].Name != "local" ||
		page.Servers[0].Address != "tool" || page.Servers[0].EnvironmentKeyCount != 1 {
		t.Fatalf("page=%#v", page)
	}
	var rawPage struct {
		Servers []map[string]json.RawMessage `json:"servers"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &rawPage); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"url", "command", "args", "env_file", "env_keys", "header_keys"} {
		if _, leaked := rawPage.Servers[0][forbidden]; leaked {
			t.Fatalf("collection row leaked %q: %s", forbidden, list.Body.String())
		}
	}
	if strings.Contains(list.Body.String(), "secret-argument") ||
		strings.Contains(list.Body.String(), "/private/mcp.env") {
		t.Fatalf("collection leaked MCP configuration: %s", list.Body.String())
	}
	detail := harness.request(t, http.MethodGet, "/api/mcp/servers/local", nil)
	if detail.Code != http.StatusOK || !json.Valid(detail.Body.Bytes()) {
		t.Fatalf("detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	var direct struct {
		Server mcpServerSummary `json:"server"`
	}
	if err := json.Unmarshal(detail.Body.Bytes(), &direct); err != nil {
		t.Fatal(err)
	}
	if direct.Server.Command != "/usr/local/bin/tool" ||
		!slices.Equal(direct.Server.Args, []string{"secret-argument"}) {
		t.Fatalf("direct detail=%#v", direct.Server)
	}
}

func TestMCPBulkDeleteCanonicalIDsReferencesAndCredentialCleanup(t *testing.T) {
	harness := newMCPAPITestHarness(t, func(cfg *config.Config) {
		cfg.Tools.MCP.Enabled = true
		cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
			"plain": {Enabled: true, Type: "stdio", Command: "plain-tool"},
			"auth": {
				Enabled: true, Type: "http", URL: "https://example.test/mcp",
				Auth: &config.MCPServerAuthConfig{Type: "bearer", CredentialID: "mcp:auth-explicit"},
			},
			"protected": {Enabled: true, Type: "stdio", Command: "protected-tool"},
		}
	})
	cfg, err := config.LoadConfig(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}
	workspace := cfg.WorkspacePath()
	if mkdirErr := os.MkdirAll(workspace, 0o700); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	if writeErr := os.WriteFile(
		filepath.Join(workspace, agentDefinitionFileCurrent),
		[]byte("---\nmcpServers: [protected]\n---\n"),
		0o600,
	); writeErr != nil {
		t.Fatal(writeErr)
	}
	plainCredentialID, err := picomcp.CredentialID("plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	if credentialErr := picoauth.SetCredential(plainCredentialID, &picoauth.AuthCredential{
		Provider: "mcp", AuthMethod: "bearer", AccessToken: "unreferenced-but-unowned",
	}); credentialErr != nil {
		t.Fatal(credentialErr)
	}
	const authCredentialID = "mcp:auth-explicit"
	if credentialErr := picoauth.SetCredential(authCredentialID, &picoauth.AuthCredential{
		Provider: "mcp", AuthMethod: "bearer", AccessToken: "owned",
	}); credentialErr != nil {
		t.Fatal(credentialErr)
	}
	revision, err := config.ConfigRevision(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}

	duplicate := harness.request(t, http.MethodPost, "/api/mcp/servers/bulk-delete", map[string]any{
		"ids": []string{"plain", "PLAIN"}, "config_revision": revision,
	})
	if duplicate.Code != http.StatusOK {
		t.Fatalf("case duplicate status=%d body=%s", duplicate.Code, duplicate.Body.String())
	}
	var duplicateResult collectionBulkDeleteResponse
	if decodeErr := json.Unmarshal(duplicate.Body.Bytes(), &duplicateResult); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if len(duplicateResult.DeletedIDs) != 0 || len(duplicateResult.Failures) != 1 ||
		duplicateResult.Failures[0].Code != "duplicate_id" {
		t.Fatalf("case duplicate result=%#v", duplicateResult)
	}

	deleted := harness.request(t, http.MethodPost, "/api/mcp/servers/bulk-delete", map[string]any{
		"ids": []string{"plain", "auth", "protected"}, "config_revision": revision,
	})
	if deleted.Code != http.StatusOK {
		t.Fatalf("bulk status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	var result collectionBulkDeleteResponse
	if decodeErr := json.Unmarshal(deleted.Body.Bytes(), &result); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if !slices.Equal(result.DeletedIDs, []string{"auth", "plain"}) ||
		len(result.Failures) != 1 || result.Failures[0].ID != "protected" ||
		result.Failures[0].Code != "referenced" {
		t.Fatalf("bulk result=%#v", result)
	}
	loaded, err := config.LoadConfig(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loaded.Tools.MCP.Servers["protected"]; !ok || len(loaded.Tools.MCP.Servers) != 1 {
		t.Fatalf("servers after delete=%#v", loaded.Tools.MCP.Servers)
	}
	if credential, err := picoauth.GetCredential(authCredentialID); err != nil || credential != nil {
		t.Fatalf("owned credential after delete=%#v err=%v", credential, err)
	}
	if credential, err := picoauth.GetCredential(plainCredentialID); err != nil || credential == nil {
		t.Fatalf("authless server credential was removed: credential=%#v err=%v", credential, err)
	}
}

func TestRepositoryModelEvaluationCollectionBulkAndRequestBoundaries(t *testing.T) {
	handler, mux, _ := newRepositoryModelEvaluationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	deletedDraft := createRepositoryModelEvaluation(t, mux, "owner/deleted")
	staleDraft := createRepositoryModelEvaluation(t, mux, "owner/stale")
	duplicateDraft := createRepositoryModelEvaluation(t, mux, "owner/duplicate")

	bulk := repositoryModelEvaluationMutation(
		t, mux, http.MethodPost, "/api/model-evaluations/bulk-delete", map[string]any{
			"items": []map[string]any{
				{"id": deletedDraft.ID, "version": deletedDraft.Version},
				{"id": staleDraft.ID, "version": staleDraft.Version + 1},
				{"id": duplicateDraft.ID, "version": duplicateDraft.Version},
				{"id": duplicateDraft.ID, "version": duplicateDraft.Version},
				{"id": "invalid", "version": 1},
			},
		},
	)
	if bulk.Code != http.StatusOK {
		t.Fatalf("bulk status=%d body=%s", bulk.Code, bulk.Body.String())
	}
	var result repoeval.BulkDeleteResult
	if err := json.Unmarshal(bulk.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.DeletedIDs) != 1 || result.DeletedIDs[0] != deletedDraft.ID || len(result.Failures) != 3 {
		t.Fatalf("bulk result=%#v", result)
	}

	query := url.QueryEscape(`status = draft ORDER BY updated DESC`)
	list := httptest.NewRecorder()
	mux.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/model-evaluations?query="+query+"&limit=1", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	var page struct {
		Evaluations    []repositoryModelEvaluationSummary `json:"evaluations"`
		Total          int                                `json:"total"`
		NextCursor     string                             `json:"next_cursor"`
		CanonicalQuery string                             `json:"canonical_query"`
		QuerySchema    json.RawMessage                    `json:"query_schema"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Evaluations) != 1 || page.Total != 2 || page.NextCursor == "" ||
		page.CanonicalQuery == "" || !json.Valid(page.QuerySchema) {
		t.Fatalf("list page=%#v", page)
	}

	missingTypeRequest := httptest.NewRequest(
		http.MethodPost, "/api/model-evaluations/bulk-delete",
		strings.NewReader(`{"items":[{"id":"`+staleDraft.ID+`","version":1}]}`),
	)
	missingTypeRequest.Header.Set("Sec-Fetch-Site", "same-origin")
	missingType := httptest.NewRecorder()
	mux.ServeHTTP(missingType, missingTypeRequest)
	if missingType.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("missing content type status=%d body=%s", missingType.Code, missingType.Body.String())
	}

	oversizedRequest := httptest.NewRequest(
		http.MethodPost, "/api/model-evaluations/bulk-delete",
		bytes.NewReader(bytes.Repeat([]byte(" "), repositoryModelEvaluationRequestMaxBytes+1)),
	)
	oversizedRequest.Header.Set("Content-Type", "application/json")
	oversizedRequest.Header.Set("Sec-Fetch-Site", "same-origin")
	oversized := httptest.NewRecorder()
	mux.ServeHTTP(oversized, oversizedRequest)
	if oversized.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status=%d body=%s", oversized.Code, oversized.Body.String())
	}

	duplicateJSONRequest := httptest.NewRequest(
		http.MethodPost, "/api/model-evaluations/bulk-delete",
		strings.NewReader(`{"items":[],"ITEMS":[]}`),
	)
	duplicateJSONRequest.Header.Set("Content-Type", "application/json")
	duplicateJSONRequest.Header.Set("Sec-Fetch-Site", "same-origin")
	duplicateJSON := httptest.NewRecorder()
	mux.ServeHTTP(duplicateJSON, duplicateJSONRequest)
	if duplicateJSON.Code != http.StatusBadRequest {
		t.Fatalf("duplicate JSON status=%d body=%s", duplicateJSON.Code, duplicateJSON.Body.String())
	}
}
