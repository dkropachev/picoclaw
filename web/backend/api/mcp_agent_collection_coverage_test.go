package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	picoauth "github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestAgentCollectionQueryFieldsPagingAndDetailBoundaries(t *testing.T) {
	harness := newAgentAPITestHarness(t, func(cfg *config.Config) {
		cfg.ModelList = []*config.ModelConfig{
			{ModelName: "acct-alpha", Provider: "openai", Model: "openai/model-a", Enabled: true},
			{ModelName: "acct-beta", Provider: "openai", Model: "openai/model-b", Enabled: true},
		}
		cfg.ModelAliases = []config.ModelAliasConfig{
			{Name: "model-a", Model: "provider/model-a"},
			{Name: "model-b", Model: "provider/model-b"},
		}
		cfg.Agents.List = []config.AgentConfig{
			{
				ID: "main", Name: "Alpha", Default: true,
				Workspace: "/workspace/alpha", AccountRef: "acct-alpha",
				Model:  &config.AgentModelConfig{Primary: "model-a", Fallbacks: []string{"model-b"}},
				Skills: []string{"skill-a"},
				Subagents: &config.SubagentsConfig{
					AllowAgents: []string{"worker"},
				},
			},
			{
				ID: "worker", Name: "Beta", Workspace: "/workspace/beta",
				AccountRef: "acct-beta",
			},
		}
	})
	if _, _, err := config.LoadCurrentConfigSnapshot(harness.configPath); err != nil {
		t.Fatalf("fixture does not load as a current config: %v", err)
	}

	queries := []struct {
		query string
		total int
		id    string
	}{
		{query: `id = "main"`, total: 1, id: "main"},
		{query: `name = "Alpha"`, total: 1, id: "main"},
		{query: `workspace = "/workspace/alpha"`, total: 1, id: "main"},
		{query: `account = "acct-alpha"`, total: 1, id: "main"},
		{query: `model = "model-a"`, total: 1, id: "main"},
		{query: `default = true`, total: 1, id: "main"},
		{query: `implicit = false`, total: 2, id: "main"},
		{query: `position = 1`, total: 1, id: "worker"},
	}
	for _, test := range queries {
		t.Run(test.query, func(t *testing.T) {
			response := harness.request(
				t,
				http.MethodGet,
				"/api/agents?query="+url.QueryEscape(test.query),
				nil,
			)
			page := decodeAgentCollection(t, response)
			if page.Total != test.total || len(page.Agents) == 0 || page.Agents[0].ID != test.id ||
				page.CanonicalQuery == "" || page.QuerySchema == nil {
				t.Fatalf("query %q page=%#v", test.query, page)
			}
		})
	}

	first := decodeAgentCollection(t, harness.request(
		t,
		http.MethodGet,
		"/api/agents?query="+url.QueryEscape(
			`position >= 0 ORDER BY workspace ASC, account ASC, model ASC`,
		)+"&limit=1",
		nil,
	))
	if first.Total != 2 || len(first.Agents) != 1 || first.NextCursor == "" {
		t.Fatalf("first page=%#v", first)
	}
	second := decodeAgentCollection(t, harness.request(
		t,
		http.MethodGet,
		"/api/agents?query="+url.QueryEscape(
			`position >= 0 ORDER BY workspace ASC, account ASC, model ASC`,
		)+"&limit=1&cursor="+url.QueryEscape(first.NextCursor),
		nil,
	))
	if len(second.Agents) != 1 || second.Agents[0].ID == first.Agents[0].ID {
		t.Fatalf("second page=%#v after first=%#v", second, first)
	}
	mismatch := harness.request(
		t,
		http.MethodGet,
		"/api/agents?query="+url.QueryEscape(`id = "main"`)+
			"&limit=1&cursor="+url.QueryEscape(first.NextCursor),
		nil,
	)
	if mismatch.Code != http.StatusBadRequest {
		t.Fatalf("cursor mismatch status=%d body=%s", mismatch.Code, mismatch.Body.String())
	}
	invalidQuery := harness.request(t, http.MethodGet, "/api/agents?query=unknown%20%3D%201", nil)
	if invalidQuery.Code != http.StatusBadRequest {
		t.Fatalf("invalid query status=%d body=%s", invalidQuery.Code, invalidQuery.Body.String())
	}
	detailQuery := harness.request(t, http.MethodGet, "/api/agents/main?unexpected=1", nil)
	if detailQuery.Code != http.StatusBadRequest {
		t.Fatalf("detail query status=%d body=%s", detailQuery.Code, detailQuery.Body.String())
	}
	missing := harness.request(t, http.MethodGet, "/api/agents/missing", nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing detail status=%d body=%s", missing.Code, missing.Body.String())
	}

	resources := []agentResource{
		{
			ID: "main", Model: &agentModelPolicy{Primary: "model-a", Fallbacks: &[]string{"model-b"}},
			Skills: []string{"skill-a"}, Subagents: &agentSubagentsPolicy{AllowAgents: []string{"worker"}},
		},
	}
	query, err := collectionquery.Parse("", agentCollectionSchema)
	if err != nil {
		t.Fatal(err)
	}
	cloned, err := pageAgentResources(resources, collectionListRequest{Query: query, Now: time.Now().UTC()})
	if err != nil || len(cloned.Items) != 1 {
		t.Fatalf("pageAgentResources()=%#v err=%v", cloned, err)
	}
	cloned.Items[0].Model.Primary = "mutated"
	(*cloned.Items[0].Model.Fallbacks)[0] = "mutated"
	cloned.Items[0].Skills[0] = "mutated"
	cloned.Items[0].Subagents.AllowAgents[0] = "mutated"
	if resources[0].Model.Primary != "model-a" || (*resources[0].Model.Fallbacks)[0] != "model-b" ||
		resources[0].Skills[0] != "skill-a" || resources[0].Subagents.AllowAgents[0] != "worker" {
		t.Fatalf("paged clone mutated source=%#v", resources[0])
	}
}

func TestAgentBulkDeleteBoundariesNoopPromotionAndSaveFailure(t *testing.T) {
	newHarness := func(t *testing.T) *agentAPITestHarness {
		t.Helper()
		return newAgentAPITestHarness(t, func(cfg *config.Config) {
			cfg.Agents.List = []config.AgentConfig{
				{ID: "main", Default: true},
				{ID: "worker"},
				{ID: "reviewer"},
			}
		})
	}

	t.Run("request boundaries and no-op failures", func(t *testing.T) {
		harness := newHarness(t)
		revision, err := config.ConfigRevision(harness.configPath)
		if err != nil {
			t.Fatal(err)
		}
		tooMany := make([]string, 201)
		for index := range tooMany {
			tooMany[index] = "agent-" + strings.Repeat("x", index%3+1)
		}
		tests := []struct {
			name   string
			path   string
			body   map[string]any
			status int
		}{
			{
				name: "unknown query", path: "/api/agents/bulk-delete?unexpected=1",
				body: map[string]any{
					"ids": []string{"worker"}, "config_revision": revision,
				},
				status: http.StatusBadRequest,
			},
			{
				name: "empty selection", path: "/api/agents/bulk-delete",
				body: map[string]any{
					"ids": []string{}, "config_revision": revision,
				},
				status: http.StatusBadRequest,
			},
			{
				name: "oversized selection", path: "/api/agents/bulk-delete",
				body: map[string]any{
					"ids": tooMany, "config_revision": revision,
				},
				status: http.StatusBadRequest,
			},
			{
				name: "conflicting revisions", path: "/api/agents/bulk-delete",
				body: map[string]any{
					"ids": []string{"worker"}, "config_revision": revision,
					"expected_config_revision": "other",
				},
				status: http.StatusBadRequest,
			},
			{
				name: "missing revision", path: "/api/agents/bulk-delete",
				body: map[string]any{"ids": []string{"worker"}}, status: http.StatusBadRequest,
			},
			{
				name: "stale revision", path: "/api/agents/bulk-delete",
				body: map[string]any{
					"ids": []string{"worker"}, "config_revision": "stale",
				},
				status: http.StatusConflict,
			},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				response := harness.request(t, http.MethodPost, test.path, test.body)
				if response.Code != test.status {
					t.Fatalf("status=%d want=%d body=%s", response.Code, test.status, response.Body.String())
				}
			})
		}
		malformedRequest := httptest.NewRequest(
			http.MethodPost,
			"/api/agents/bulk-delete",
			strings.NewReader("{"),
		)
		malformedRequest.Header.Set("Content-Type", "application/json")
		malformed := httptest.NewRecorder()
		harness.mux.ServeHTTP(malformed, malformedRequest)
		if malformed.Code != http.StatusBadRequest {
			t.Fatalf("malformed body status=%d body=%s", malformed.Code, malformed.Body.String())
		}

		before, err := os.ReadFile(harness.configPath)
		if err != nil {
			t.Fatal(err)
		}
		noOp := harness.request(t, http.MethodPost, "/api/agents/bulk-delete", map[string]any{
			"ids": []string{"", "dup", "dup", "Bad_ID", "missing"}, "config_revision": revision,
		})
		if noOp.Code != http.StatusOK {
			t.Fatalf("no-op status=%d body=%s", noOp.Code, noOp.Body.String())
		}
		var result agentBulkDeleteResponse
		if decodeErr := json.Unmarshal(noOp.Body.Bytes(), &result); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		codes := make(map[string]string, len(result.Failures))
		for _, failure := range result.Failures {
			codes[failure.ID] = failure.Code
		}
		if len(result.DeletedIDs) != 0 || len(result.Failures) != 4 ||
			codes[""] != "invalid_id" || codes["dup"] != "duplicate_id" ||
			codes["Bad_ID"] != "invalid_agent_id" || codes["missing"] != "agent_not_found" ||
			result.ConfigRevision != revision {
			t.Fatalf("no-op result=%#v", result)
		}
		after, err := os.ReadFile(harness.configPath)
		if err != nil || !slices.Equal(before, after) {
			t.Fatalf("no-op changed config: equal=%v err=%v", slices.Equal(before, after), err)
		}
	})

	t.Run("configuration load failure", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(configPath, []byte("{not-json"), 0o600); err != nil {
			t.Fatal(err)
		}
		handler := NewHandler(configPath)
		mux := http.NewServeMux()
		handler.RegisterRoutes(mux)
		request := httptest.NewRequest(
			http.MethodPost,
			"/api/agents/bulk-delete",
			strings.NewReader(`{"ids":["worker"],"config_revision":"revision"}`),
		)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusInternalServerError ||
			!strings.Contains(response.Body.String(), "agents_unavailable") {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})

	t.Run("implicit main is retained", func(t *testing.T) {
		harness := newAgentAPITestHarness(t, nil)
		revision, err := config.ConfigRevision(harness.configPath)
		if err != nil {
			t.Fatal(err)
		}
		response := harness.request(t, http.MethodPost, "/api/agents/bulk-delete", map[string]any{
			"ids": []string{"main"}, "expected_config_revision": revision,
		})
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		var result agentBulkDeleteResponse
		if decodeErr := json.Unmarshal(response.Body.Bytes(), &result); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if len(result.DeletedIDs) != 0 || len(result.Failures) != 1 ||
			result.Failures[0].Code != "implicit_agent_required" {
			t.Fatalf("implicit result=%#v", result)
		}
	})

	t.Run("deleting the default promotes the first survivor", func(t *testing.T) {
		harness := newHarness(t)
		revision, err := config.ConfigRevision(harness.configPath)
		if err != nil {
			t.Fatal(err)
		}
		response := harness.request(t, http.MethodPost, "/api/agents/bulk-delete", map[string]any{
			"ids": []string{"main"}, "config_revision": revision,
		})
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		var result agentBulkDeleteResponse
		if decodeErr := json.Unmarshal(response.Body.Bytes(), &result); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		loaded, err := config.LoadConfig(harness.configPath)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(result.DeletedIDs, []string{"main"}) || len(loaded.Agents.List) != 2 ||
			loaded.Agents.List[0].ID != "worker" || !loaded.Agents.List[0].Default ||
			loaded.Agents.List[1].Default {
			t.Fatalf("result=%#v agents=%#v", result, loaded.Agents.List)
		}
	})

	t.Run("save failure retains every selected agent", func(t *testing.T) {
		harness := newHarness(t)
		revision, err := config.ConfigRevision(harness.configPath)
		if err != nil {
			t.Fatal(err)
		}
		harness.handler.saveConfigIfRevision = func(string, *config.Config, string) (string, error) {
			return "", errors.New("injected save failure")
		}
		response := harness.request(t, http.MethodPost, "/api/agents/bulk-delete", map[string]any{
			"ids": []string{"worker"}, "config_revision": revision,
		})
		if response.Code != http.StatusInternalServerError ||
			!strings.Contains(response.Body.String(), "agent_save_failed") {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		loaded, err := config.LoadConfig(harness.configPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, exists := findConfiguredAgent(loaded, "worker"); !exists {
			t.Fatal("worker was removed despite failed save")
		}
	})

	failures := []agentBulkDeleteFailure{
		{ID: "same", Code: "z-code"},
		{ID: "same", Code: "a-code"},
	}
	sortAgentBulkFailures(failures)
	if failures[0].Code != "a-code" {
		t.Fatalf("same-ID failures were not sorted by code: %#v", failures)
	}
}

func TestMCPCollectionSummaryQueryPagingAndDetailBoundaries(t *testing.T) {
	deferred := true
	longCommand := strings.Repeat("a", 255) + "é-tail"
	longSummary := projectMCPServerCollectionSummary(mcpServerSummary{
		Name: "long", Type: "stdio", Command: longCommand, Deferred: &deferred,
	})
	if len(longSummary.Address) != 255 || !utf8.ValidString(longSummary.Address) ||
		longSummary.Deferred == nil || !*longSummary.Deferred {
		t.Fatalf("long summary=%#v", longSummary)
	}
	deferred = false
	if !*longSummary.Deferred {
		t.Fatal("summary did not clone deferred flag")
	}
	remoteSummary := projectMCPServerCollectionSummary(mcpServerSummary{
		Name: "remote", Type: "http", URL: "https://example.test/private/path?token=secret",
	})
	if remoteSummary.Address != "https://example.test" {
		t.Fatalf("remote summary address=%q", remoteSummary.Address)
	}
	invalidSummary := projectMCPServerCollectionSummary(mcpServerSummary{
		Name: "invalid", Type: "http", URL: "/relative/private/path",
	})
	if invalidSummary.Address != "" {
		t.Fatalf("invalid URL address=%q", invalidSummary.Address)
	}
	windowsSummary := projectMCPServerCollectionSummary(mcpServerSummary{
		Name: "windows", Type: "stdio", Command: `C:\tools\mcp.exe`,
	})
	if windowsSummary.Address != "mcp.exe" {
		t.Fatalf("windows command address=%q", windowsSummary.Address)
	}

	harness := newMCPAPITestHarness(t, func(cfg *config.Config) {
		cfg.Tools.MCP.Enabled = true
		cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
			"local": {
				Enabled: true, Deferred: boolPointer(true), Type: "stdio", Command: "/usr/local/bin/local",
				Env: map[string]string{"TOKEN": "secret"},
			},
			"remote": {
				Enabled: false, Type: "http", URL: "https://example.test/private",
				Headers: map[string]string{"X-Key": "secret"},
			},
			"oauth": {
				Enabled: true, Type: "sse", URL: "https://oauth.example.test/events",
				Auth: &config.MCPServerAuthConfig{Type: "oauth"},
			},
		}
	})
	queries := []struct {
		query string
		name  string
	}{
		{query: `name = "local"`, name: "local"},
		{query: `enabled = false`, name: "remote"},
		{query: `deferred = true`, name: "local"},
		{query: `type = http`, name: "remote"},
		{query: `auth = custom`, name: "remote"},
		{query: `auth = oauth`, name: "oauth"},
	}
	for _, test := range queries {
		t.Run(test.query, func(t *testing.T) {
			response := harness.request(
				t,
				http.MethodGet,
				"/api/mcp/servers?query="+url.QueryEscape(test.query),
				nil,
			)
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var page struct {
				Servers        []mcpServerCollectionSummary `json:"servers"`
				Total          int                          `json:"total"`
				CanonicalQuery string                       `json:"canonical_query"`
				QuerySchema    collectionquery.Schema       `json:"query_schema"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
			if page.Total != 1 || len(page.Servers) != 1 || page.Servers[0].Name != test.name ||
				page.CanonicalQuery == "" || len(page.QuerySchema.Fields) == 0 {
				t.Fatalf("query %q page=%#v", test.query, page)
			}
		})
	}

	first := harness.request(
		t,
		http.MethodGet,
		"/api/mcp/servers?query="+url.QueryEscape(
			`type IN (stdio, http, sse) ORDER BY auth ASC, name DESC`,
		)+"&limit=1",
		nil,
	)
	if first.Code != http.StatusOK {
		t.Fatalf("first page status=%d body=%s", first.Code, first.Body.String())
	}
	var firstPage struct {
		Servers    []mcpServerCollectionSummary `json:"servers"`
		Total      int                          `json:"total"`
		NextCursor string                       `json:"next_cursor"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstPage); err != nil {
		t.Fatal(err)
	}
	if firstPage.Total != 3 || len(firstPage.Servers) != 1 || firstPage.NextCursor == "" {
		t.Fatalf("first page=%#v", firstPage)
	}
	second := harness.request(
		t,
		http.MethodGet,
		"/api/mcp/servers?query="+url.QueryEscape(
			`type IN (stdio, http, sse) ORDER BY auth ASC, name DESC`,
		)+"&limit=1&cursor="+url.QueryEscape(firstPage.NextCursor),
		nil,
	)
	if second.Code != http.StatusOK {
		t.Fatalf("second page status=%d body=%s", second.Code, second.Body.String())
	}
	mismatch := harness.request(
		t,
		http.MethodGet,
		"/api/mcp/servers?query="+url.QueryEscape(`name = "local"`)+
			"&limit=1&cursor="+url.QueryEscape(firstPage.NextCursor),
		nil,
	)
	if mismatch.Code != http.StatusBadRequest {
		t.Fatalf("cursor mismatch status=%d body=%s", mismatch.Code, mismatch.Body.String())
	}
	invalidQuery := harness.request(t, http.MethodGet, "/api/mcp/servers?query=unknown%20%3D%201", nil)
	if invalidQuery.Code != http.StatusBadRequest {
		t.Fatalf("invalid query status=%d body=%s", invalidQuery.Code, invalidQuery.Body.String())
	}

	detail := harness.request(t, http.MethodGet, "/api/mcp/servers/REMOTE", nil)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"name":"remote"`) {
		t.Fatalf("case-insensitive detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	invalidDetailQuery := harness.request(t, http.MethodGet, "/api/mcp/servers/remote?unexpected=1", nil)
	if invalidDetailQuery.Code != http.StatusBadRequest {
		t.Fatalf("detail query status=%d body=%s", invalidDetailQuery.Code, invalidDetailQuery.Body.String())
	}
	missing := harness.request(t, http.MethodGet, "/api/mcp/servers/missing", nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing detail status=%d body=%s", missing.Code, missing.Body.String())
	}
}

func TestMCPCollectionLoadCredentialAndBulkFailureBoundaries(t *testing.T) {
	t.Run("configuration load failures", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(configPath, []byte("{not-json"), 0o600); err != nil {
			t.Fatal(err)
		}
		handler := NewHandler(configPath)
		mux := http.NewServeMux()
		handler.RegisterRoutes(mux)
		for _, test := range []struct {
			method string
			path   string
			body   string
		}{
			{method: http.MethodGet, path: "/api/mcp/servers"},
			{method: http.MethodGet, path: "/api/mcp/servers/missing"},
			{method: http.MethodPost, path: "/api/mcp/servers/bulk-delete", body: `{"ids":["missing"],"config_revision":"revision"}`},
		} {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			if test.body != "" {
				request.Header.Set("Content-Type", "application/json")
			}
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != http.StatusInternalServerError ||
				!strings.Contains(response.Body.String(), "config_load_failed") {
				t.Fatalf("%s %s status=%d body=%s", test.method, test.path, response.Code, response.Body.String())
			}
		}
	})

	for _, route := range []string{"/api/mcp/servers", "/api/mcp/servers/bearer"} {
		t.Run("credential load failure "+route, func(t *testing.T) {
			harness := newMCPAPITestHarness(t, func(cfg *config.Config) {
				cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
					"bearer": {
						Enabled: true, Type: "http", URL: "https://example.test/mcp",
						Auth: &config.MCPServerAuthConfig{Type: "bearer"},
					},
				}
			})
			credentialPath := filepath.Join(config.GetHome(), "auth.json")
			if writeErr := os.WriteFile(credentialPath, []byte("{not-json"), 0o600); writeErr != nil {
				t.Fatal(writeErr)
			}
			response := harness.request(t, http.MethodGet, route, nil)
			if response.Code != http.StatusInternalServerError ||
				!strings.Contains(response.Body.String(), "mcp_credentials_failed") {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}

	t.Run("bulk request boundaries and no-op", func(t *testing.T) {
		harness := newMCPAPITestHarness(t, func(cfg *config.Config) {
			cfg.Tools.MCP.Enabled = true
			cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
				"kept": {Enabled: true, Type: "stdio", Command: "kept"},
			}
		})
		revision, err := config.ConfigRevision(harness.configPath)
		if err != nil {
			t.Fatal(err)
		}
		tooMany := make([]string, 201)
		for index := range tooMany {
			tooMany[index] = "server-" + strings.Repeat("x", index%3+1)
		}
		requireStatus := func(name, path string, body map[string]any, want int) {
			t.Helper()
			t.Run(name, func(t *testing.T) {
				response := harness.request(t, http.MethodPost, path, body)
				if response.Code != want {
					t.Fatalf("status=%d want=%d body=%s", response.Code, want, response.Body.String())
				}
			})
		}
		requireStatus(
			"unknown query",
			"/api/mcp/servers/bulk-delete?unexpected=1",
			map[string]any{"ids": []string{"kept"}, "config_revision": revision},
			http.StatusBadRequest,
		)
		requireStatus(
			"empty selection",
			"/api/mcp/servers/bulk-delete",
			map[string]any{"ids": []string{}, "config_revision": revision},
			http.StatusBadRequest,
		)
		requireStatus(
			"oversized selection",
			"/api/mcp/servers/bulk-delete",
			map[string]any{"ids": tooMany, "config_revision": revision},
			http.StatusBadRequest,
		)
		requireStatus(
			"conflicting body revisions",
			"/api/mcp/servers/bulk-delete",
			map[string]any{
				"ids": []string{"kept"}, "config_revision": revision,
				"expected_config_revision": "other",
			},
			http.StatusBadRequest,
		)
		requireStatus(
			"missing revision",
			"/api/mcp/servers/bulk-delete",
			map[string]any{"ids": []string{"kept"}},
			http.StatusPreconditionRequired,
		)
		requireStatus(
			"stale revision",
			"/api/mcp/servers/bulk-delete",
			map[string]any{"ids": []string{"kept"}, "config_revision": "stale"},
			http.StatusConflict,
		)
		malformedRequest := httptest.NewRequest(
			http.MethodPost,
			"/api/mcp/servers/bulk-delete",
			strings.NewReader("{"),
		)
		malformedRequest.Header.Set("Content-Type", "application/json")
		malformed := httptest.NewRecorder()
		harness.mux.ServeHTTP(malformed, malformedRequest)
		if malformed.Code != http.StatusBadRequest {
			t.Fatalf("malformed body status=%d body=%s", malformed.Code, malformed.Body.String())
		}

		before, err := os.ReadFile(harness.configPath)
		if err != nil {
			t.Fatal(err)
		}
		noOp := harness.request(t, http.MethodPost, "/api/mcp/servers/bulk-delete", map[string]any{
			"ids": []string{"", "missing"}, "config_revision": revision,
		})
		if noOp.Code != http.StatusOK {
			t.Fatalf("no-op status=%d body=%s", noOp.Code, noOp.Body.String())
		}
		var result collectionBulkDeleteResponse
		if decodeErr := json.Unmarshal(noOp.Body.Bytes(), &result); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if len(result.DeletedIDs) != 0 || len(result.Failures) != 2 || result.ConfigRevision != revision ||
			result.Failures[0].Code != "invalid_id" || result.Failures[1].Code != "not_found" {
			t.Fatalf("no-op result=%#v", result)
		}
		after, err := os.ReadFile(harness.configPath)
		if err != nil || !slices.Equal(before, after) {
			t.Fatalf("no-op changed config: equal=%v err=%v", slices.Equal(before, after), err)
		}
	})

	if mcpServerReferences(nil, "server") != nil ||
		mcpServerReferences(&config.Config{}, " ") != nil {
		t.Fatal("empty MCP reference inputs produced blockers")
	}

	t.Run("shared credential remains referenced", func(t *testing.T) {
		const credentialID = "mcp:shared-bulk-coverage"
		harness := newMCPAPITestHarness(t, func(cfg *config.Config) {
			cfg.Tools.MCP.Enabled = true
			cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
				"one": {
					Enabled: true, Type: "http", URL: "https://one.example.test/mcp",
					Auth: &config.MCPServerAuthConfig{Type: "bearer", CredentialID: credentialID},
				},
				"two": {
					Enabled: true, Type: "http", URL: "https://two.example.test/mcp",
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
		response := harness.request(t, http.MethodPost, "/api/mcp/servers/bulk-delete", map[string]any{
			"ids": []string{"one"}, "config_revision": revision,
		})
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		var result collectionBulkDeleteResponse
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		credential, getErr := picoauth.GetCredential(credentialID)
		if !slices.Equal(result.DeletedIDs, []string{"one"}) || len(result.CleanupFailures) != 0 ||
			credential == nil || getErr != nil {
			t.Fatalf("result=%#v credential=%#v err=%v", result, credential, getErr)
		}
	})

	t.Run("unavailable agent definition blocks deletion", func(t *testing.T) {
		workspace := filepath.Join(t.TempDir(), "worker-workspace")
		if err := os.MkdirAll(filepath.Join(workspace, agentDefinitionFileCurrent), 0o700); err != nil {
			t.Fatal(err)
		}
		harness := newMCPAPITestHarness(t, func(cfg *config.Config) {
			cfg.Tools.MCP.Enabled = true
			cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
				"protected": {Enabled: true, Type: "stdio", Command: "protected"},
			}
			cfg.Agents.List = []config.AgentConfig{{ID: "worker", Default: true, Workspace: workspace}}
		})
		revision, err := config.ConfigRevision(harness.configPath)
		if err != nil {
			t.Fatal(err)
		}
		response := harness.request(t, http.MethodPost, "/api/mcp/servers/bulk-delete", map[string]any{
			"ids": []string{"protected"}, "config_revision": revision,
		})
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		var result collectionBulkDeleteResponse
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.DeletedIDs) != 0 || len(result.Failures) != 1 ||
			result.Failures[0].Code != "referenced" ||
			!slices.Equal(result.Failures[0].Blockers, []string{"agent:worker:definition_unavailable"}) {
			t.Fatalf("result=%#v", result)
		}
	})

	t.Run("save failure retains server", func(t *testing.T) {
		harness := newMCPAPITestHarness(t, func(cfg *config.Config) {
			cfg.Tools.MCP.Enabled = true
			cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
				"kept": {Enabled: true, Type: "stdio", Command: "kept"},
			}
		})
		revision, err := config.ConfigRevision(harness.configPath)
		if err != nil {
			t.Fatal(err)
		}
		originalSave := mcpSaveConfigIfRevision
		mcpSaveConfigIfRevision = func(string, *config.Config, string) (string, error) {
			return "", errors.New("injected save failure")
		}
		t.Cleanup(func() { mcpSaveConfigIfRevision = originalSave })
		response := harness.request(t, http.MethodPost, "/api/mcp/servers/bulk-delete", map[string]any{
			"ids": []string{"kept"}, "config_revision": revision,
		})
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		loaded, err := config.LoadConfig(harness.configPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, exists := loaded.Tools.MCP.Servers["kept"]; !exists {
			t.Fatal("server was removed despite failed save")
		}
	})
}

func TestMCPMutationRevisionAndReferenceBoundaries(t *testing.T) {
	harness := newMCPAPITestHarness(t, func(cfg *config.Config) {
		cfg.Tools.MCP.Enabled = true
		cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
			"protected": {Enabled: true, Type: "stdio", Command: "protected"},
		}
	})
	cfg, err := config.LoadConfig(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if mkdirErr := os.MkdirAll(cfg.WorkspacePath(), 0o700); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	if writeErr := os.WriteFile(
		filepath.Join(cfg.WorkspacePath(), agentDefinitionFileCurrent),
		[]byte("---\nmcpServers: [protected]\n---\n"),
		0o600,
	); writeErr != nil {
		t.Fatal(writeErr)
	}
	revision, err := config.ConfigRevision(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}
	server := map[string]any{"name": "added", "type": "stdio", "command": "added", "enabled": true}
	tests := []struct {
		name   string
		method string
		path   string
		body   map[string]any
		status int
	}{
		{
			name: "add unknown query", method: http.MethodPost,
			path: "/api/mcp/servers?unexpected=1", body: server, status: http.StatusBadRequest,
		},
		{
			name: "add conflicting fences", method: http.MethodPost,
			path: "/api/mcp/servers?revision=" + url.QueryEscape(revision),
			body: map[string]any{
				"name": "added", "type": "stdio", "command": "added",
				"expected_config_revision": "other",
			},
			status: http.StatusBadRequest,
		},
		{
			name: "add stale fence", method: http.MethodPost, path: "/api/mcp/servers",
			body: map[string]any{
				"name": "added", "type": "stdio", "command": "added",
				"expected_config_revision": "stale",
			},
			status: http.StatusConflict,
		},
		{
			name: "update unknown query", method: http.MethodPut,
			path: "/api/mcp/servers/protected?unexpected=1",
			body: map[string]any{
				"name": "protected", "type": "stdio", "command": "updated",
			},
			status: http.StatusBadRequest,
		},
		{
			name: "update stale fence", method: http.MethodPut, path: "/api/mcp/servers/protected",
			body: map[string]any{
				"name": "protected", "type": "stdio", "command": "updated",
				"expected_config_revision": "stale",
			},
			status: http.StatusConflict,
		},
		{
			name: "delete unknown query", method: http.MethodDelete,
			path: "/api/mcp/servers/protected?unexpected=1", status: http.StatusBadRequest,
		},
		{
			name: "delete stale fence", method: http.MethodDelete,
			path: "/api/mcp/servers/protected?revision=stale", status: http.StatusConflict,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := harness.request(t, test.method, test.path, test.body)
			if response.Code != test.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.status, response.Body.String())
			}
		})
	}
	blocked := harness.request(
		t,
		http.MethodDelete,
		"/api/mcp/servers/protected?revision="+url.QueryEscape(revision),
		nil,
	)
	if blocked.Code != http.StatusConflict ||
		!strings.Contains(blocked.Body.String(), "mcp_server_referenced") {
		t.Fatalf("blocked status=%d body=%s", blocked.Code, blocked.Body.String())
	}
	loaded, err := config.LoadConfig(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := loaded.Tools.MCP.Servers["protected"]; !exists {
		t.Fatal("referenced MCP server was deleted")
	}
}

func boolPointer(value bool) *bool {
	return &value
}
