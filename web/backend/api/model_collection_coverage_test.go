package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/config"
)

type collectionCoverageReadError struct{}

func (collectionCoverageReadError) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (collectionCoverageReadError) Close() error             { return nil }

func modelCollectionCoverageMux(handler *Handler) *http.ServeMux {
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	return mux
}

func modelCollectionCoverageRequest(
	t *testing.T,
	mux *http.ServeMux,
	method, target string,
	body []byte,
	headers map[string]string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func modelCollectionCoverageJSON(
	t *testing.T,
	mux *http.ServeMux,
	method, target string,
	body any,
) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return modelCollectionCoverageRequest(t, mux, method, target, encoded, nil)
}

func requireModelCollectionCoverageCode(
	t *testing.T,
	response *httptest.ResponseRecorder,
	wantStatus int,
	wantCode string,
) {
	t.Helper()
	hasCode := wantCode == "" || strings.Contains(
		response.Body.String(),
		`"code":"`+wantCode+`"`,
	)
	if response.Code != wantStatus || !hasCode {
		t.Fatalf(
			"status=%d body=%s, want status=%d code=%q",
			response.Code,
			response.Body.String(),
			wantStatus,
			wantCode,
		)
	}
}

func modelCollectionCoverageRouter(name string) config.ModelRouterConfig {
	return config.ModelRouterConfig{
		Name: name, Enabled: true, Entry: "entry",
		Blocks: []config.ModelRouterBlock{{
			ID: "entry", Type: config.ModelRouterBlockTypeModel, Model: "coding",
		}},
	}
}

func modelCollectionCoverageRevision(t *testing.T, configPath string) string {
	t.Helper()
	revision, err := config.ConfigRevision(configPath)
	if err != nil {
		t.Fatal(err)
	}
	return revision
}

func modelCollectionCoverageConfig(
	t *testing.T,
	mutate func(*config.Config),
) string {
	t.Helper()
	configPath := modelAliasAPIConfig(t)
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(cfg)
	}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func TestCollectionQueryAndRequestBoundaryCoverage(t *testing.T) {
	t.Run("schema projections", func(t *testing.T) {
		schema := mustCollectionQuerySchema([]collectionquery.FieldSchema{
			{Name: "name", Type: collectionquery.TypeString, Sortable: true},
			{Name: "state", Type: collectionquery.TypeEnum, SuggestedValues: []string{"ready"}},
		}, []collectionquery.SortField{{Field: "name", Direction: collectionquery.Ascending}})
		values := []string{"", "Alpha", "alpha", strings.Repeat("x", collectionquery.MaxSuggestedValueBytes+1)}
		for index := 0; index < collectionquery.MaxSuggestedValues+3; index++ {
			values = append(values, "value-"+strings.Repeat("x", index))
		}
		projected := collectionSchemaWithSuggestions(schema, map[collectionquery.Field][]string{
			"name": values, "state": {"ignored"},
		})
		if len(projected.Fields[0].SuggestedValues) != collectionquery.MaxSuggestedValues ||
			!slices.Equal(projected.Fields[1].SuggestedValues, []string{"ready"}) {
			t.Fatalf("projected schema=%#v", projected.Fields)
		}

		deferredPanic := false
		func() {
			defer func() { deferredPanic = recover() != nil }()
			mustCollectionQuerySchema([]collectionquery.FieldSchema{{Name: "", Type: collectionquery.TypeString}}, nil)
		}()
		if !deferredPanic {
			t.Fatal("invalid schema did not panic")
		}
	})

	t.Run("list query errors", func(t *testing.T) {
		response := httptest.NewRecorder()
		if _, ok := parseCollectionListRequest(response, nil, modelAliasCollectionSchema); ok {
			t.Fatal("nil request was accepted")
		}
		requireModelCollectionCoverageCode(t, response, http.StatusBadRequest, "invalid_collection_request")

		tests := []struct {
			name     string
			rawQuery string
			wantCode string
		}{
			{name: "malformed", rawQuery: "query=%zz", wantCode: "invalid_collection_request"},
			{name: "unsupported", rawQuery: "other=1", wantCode: "invalid_collection_request"},
			{name: "duplicate", rawQuery: "limit=1&limit=2", wantCode: "invalid_collection_request"},
			{name: "invalid limit", rawQuery: "limit=201", wantCode: "invalid_page_limit"},
			{name: "invalid query", rawQuery: "query=" + url.QueryEscape(`name =`), wantCode: "invalid_query"},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				request := httptest.NewRequest(http.MethodGet, "/api/model-aliases", nil)
				request.URL.RawQuery = test.rawQuery
				response := httptest.NewRecorder()
				if _, ok := parseCollectionListRequest(response, request, modelAliasCollectionSchema); ok {
					t.Fatal("invalid list request was accepted")
				}
				requireModelCollectionCoverageCode(t, response, http.StatusBadRequest, test.wantCode)
			})
		}
	})

	t.Run("detail query errors", func(t *testing.T) {
		response := httptest.NewRecorder()
		if validateCollectionQueryParameters(response, nil) {
			t.Fatal("nil request was accepted")
		}
		for _, rawQuery := range []string{"value=%zz", "other=1", "revision=one&revision=two"} {
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.URL.RawQuery = rawQuery
			response := httptest.NewRecorder()
			if validateCollectionQueryParameters(response, request, "revision") {
				t.Fatalf("raw query %q was accepted", rawQuery)
			}
		}
	})

	t.Run("page errors", func(t *testing.T) {
		response := httptest.NewRecorder()
		writeCollectionPageError(response, errors.New("private paging failure"))
		requireModelCollectionCoverageCode(t, response, http.StatusInternalServerError, "collection_page_failed")
	})

	t.Run("JSON decoder boundaries", func(t *testing.T) {
		var target map[string]any
		response := httptest.NewRecorder()
		if decodeCollectionJSON(response, nil, &target) {
			t.Fatal("nil request was decoded")
		}

		tests := []struct {
			name       string
			request    func() *http.Request
			wantStatus int
		}{
			{
				name: "multiple content types", wantStatus: http.StatusUnsupportedMediaType,
				request: func() *http.Request {
					r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
					r.Header["Content-Type"] = []string{"application/json", "application/json"}
					return r
				},
			},
			{
				name: "wrong media type", wantStatus: http.StatusUnsupportedMediaType,
				request: func() *http.Request {
					r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
					r.Header.Set("Content-Type", "text/plain")
					return r
				},
			},
			{
				name: "unsupported parameter", wantStatus: http.StatusUnsupportedMediaType,
				request: func() *http.Request {
					r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
					r.Header.Set("Content-Type", "application/json; profile=test")
					return r
				},
			},
			{
				name: "reader error", wantStatus: http.StatusBadRequest,
				request: func() *http.Request {
					r := httptest.NewRequest(http.MethodPost, "/", nil)
					r.Body = collectionCoverageReadError{}
					r.Header.Set("Content-Type", "application/json")
					return r
				},
			},
			{
				name: "invalid utf8", wantStatus: http.StatusBadRequest,
				request: func() *http.Request {
					body := []byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}
					r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
					r.Header.Set("Content-Type", "application/json")
					return r
				},
			},
			{
				name: "unknown field", wantStatus: http.StatusBadRequest,
				request: func() *http.Request {
					r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"unknown":true}`))
					r.Header.Set("Content-Type", "application/json")
					return r
				},
			},
			{
				name: "trailing object", wantStatus: http.StatusBadRequest,
				request: func() *http.Request {
					r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{} {}`))
					r.Header.Set("Content-Type", "application/json")
					return r
				},
			},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				response := httptest.NewRecorder()
				var requestTarget struct{}
				if decodeCollectionJSON(response, test.request(), &requestTarget) {
					t.Fatal("invalid JSON request was decoded")
				}
				if response.Code != test.wantStatus {
					t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
				}
			})
		}
	})

	t.Run("revision fences and safe errors", func(t *testing.T) {
		response := httptest.NewRecorder()
		if _, ok := resolveCollectionRevision(response, nil, ""); ok {
			t.Fatal("nil revision request was accepted")
		}

		request := httptest.NewRequest(http.MethodPost, "/?revision=one", nil)
		request.Header["If-Match"] = []string{"one", "one"}
		response = httptest.NewRecorder()
		if _, ok := resolveCollectionRevision(response, request, "one"); ok {
			t.Fatal("duplicate If-Match was accepted")
		}

		request = httptest.NewRequest(http.MethodPost, "/", nil)
		request.URL.RawQuery = "revision=%zz"
		response = httptest.NewRecorder()
		if _, ok := resolveCollectionRevision(response, request, ""); ok {
			t.Fatal("malformed revision query was accepted")
		}

		response = httptest.NewRecorder()
		if revision, ok := bulkCollectionRevision(response, collectionBulkDeleteRequest{
			ExpectedConfigRevision: " expected ",
		}); !ok || revision != "expected" {
			t.Fatalf("expected revision=%q ok=%v", revision, ok)
		}
		response = httptest.NewRecorder()
		if _, ok := bulkCollectionRevision(response, collectionBulkDeleteRequest{
			ConfigRevision: "one", ExpectedConfigRevision: "two",
		}); ok {
			t.Fatal("conflicting bulk revisions were accepted")
		}

		for _, test := range []struct {
			expected string
			status   int
			ok       bool
		}{
			{expected: "", status: http.StatusPreconditionRequired},
			{expected: "old", status: http.StatusConflict},
			{expected: "current", status: http.StatusOK, ok: true},
		} {
			response = httptest.NewRecorder()
			if ok := requireCollectionRevision(response, test.expected, "current"); ok != test.ok {
				t.Fatalf("require revision(%q)=%v", test.expected, ok)
			}
			if !test.ok && response.Code != test.status {
				t.Fatalf("revision status=%d body=%s", response.Code, response.Body.String())
			}
		}

		response = httptest.NewRecorder()
		writeCollectionError(response, http.StatusConflict, "blocked", " ", 2, []string{"agent/default"})
		if !strings.Contains(response.Body.String(), "Collection request failed") ||
			!strings.Contains(response.Body.String(), "blockers") {
			t.Fatalf("safe error=%s", response.Body.String())
		}
		response = httptest.NewRecorder()
		writeCollectionError(response, http.StatusBadRequest, "invalid_query", "bad", 7, nil)
		if !strings.Contains(response.Body.String(), `"position":7`) {
			t.Fatalf("query error=%s", response.Body.String())
		}

		if got := boundedCollectionMessage("  short  ", 20); got != "short" {
			t.Fatalf("bounded short=%q", got)
		}
		if got := boundedCollectionMessage("\u00e9long", 1); got != "" {
			t.Fatalf("bounded unicode=%q", got)
		}
		if got := boundedCollectionMessage(strings.Repeat("x", 20), 5); got != "xxxxx" {
			t.Fatalf("bounded long=%q", got)
		}
	})

	t.Run("origin guard", func(t *testing.T) {
		handler := NewHandler(filepath.Join(t.TempDir(), "config.json"))
		called := false
		guarded := handler.requireCollectionMutationOrigin(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(http.StatusNoContent)
		})
		request := httptest.NewRequest(http.MethodPost, "http://example.com/api/model-aliases", nil)
		request.Header.Set("Origin", "://invalid")
		response := httptest.NewRecorder()
		guarded(response, request)
		requireModelCollectionCoverageCode(t, response, http.StatusForbidden, "cross_origin_mutation")
		if called {
			t.Fatal("guard called handler for invalid origin")
		}

		request = httptest.NewRequest(http.MethodPost, "http://example.com/api/model-aliases", nil)
		request.Header.Set("Origin", "http://example.com")
		response = httptest.NewRecorder()
		guarded(response, request)
		if response.Code != http.StatusNoContent || !called {
			t.Fatalf("same-origin status=%d called=%v", response.Code, called)
		}
	})

	t.Run("bulk ID normalization and save errors", func(t *testing.T) {
		ids, failures := normalizeBulkIDs([]string{" b ", "a", "dup", "dup", " "})
		if !slices.Equal(ids, []string{"a", "b"}) || len(failures) != 2 || failures[0].Code != "invalid_id" {
			t.Fatalf("ids=%v failures=%#v", ids, failures)
		}
		failures = []collectionBulkFailure{{ID: "same", Code: "z"}, {ID: "same", Code: "a"}}
		sortCollectionFailures(failures)
		if failures[0].Code != "a" {
			t.Fatalf("same-ID failure sort=%#v", failures)
		}

		response := httptest.NewRecorder()
		writeCollectionConfigSaveError(response, config.ErrConfigRevisionMismatch)
		requireModelCollectionCoverageCode(t, response, http.StatusConflict, "config_revision_mismatch")
		response = httptest.NewRecorder()
		writeCollectionConfigSaveError(response, errors.New("disk secret"))
		requireModelCollectionCoverageCode(t, response, http.StatusInternalServerError, "config_save_failed")
		if strings.Contains(response.Body.String(), "disk secret") {
			t.Fatalf("save error leaked: %s", response.Body.String())
		}
	})
}

func TestModelCollectionListsDetailsAndPagingCoverage(t *testing.T) {
	configPath := modelCollectionCoverageConfig(t, func(cfg *config.Config) {
		cfg.ModelAliases = append(cfg.ModelAliases, config.ModelAliasConfig{
			Name: "analysis", Model: "gpt-5.4-mini",
			AccountOverrides: map[string]string{"openai-work": "gpt-5.4"},
			DisabledAccounts: []string{"credential:openai:paused"},
		})
		taskRouter := config.ModelRouterConfig{
			Name: "task-router", Enabled: true, Entry: "entry",
			Blocks: []config.ModelRouterBlock{
				{
					ID: "entry", Type: config.ModelRouterBlockTypeRules, Fallback: "fallback",
					Rules: []config.ModelRouterRule{{Match: config.ModelRouterRuleHasCode, Target: "code"}},
				},
				{ID: "code", Type: config.ModelRouterBlockTypeModel, Model: "coding"},
				{ID: "fallback", Type: config.ModelRouterBlockTypeModel, Model: "analysis"},
			},
		}
		otherRouter := modelCollectionCoverageRouter("other-router")
		cfg.ModelRouters = []config.ModelRouterConfig{taskRouter, otherRouter}
	})
	mux := modelCollectionCoverageMux(NewHandler(configPath))

	aliasQuery := url.QueryEscape(
		`name = analysis AND model ~ "gpt" AND overrides >= 1 AND disabled_accounts >= 1 ORDER BY overrides DESC`,
	)
	aliases := modelCollectionCoverageRequest(t, mux, http.MethodGet, "/api/model-aliases?query="+aliasQuery, nil, nil)
	var aliasPage struct {
		Aliases []modelAliasSummary `json:"model_aliases"`
		Total   int                 `json:"total"`
	}
	if err := json.Unmarshal(aliases.Body.Bytes(), &aliasPage); err != nil {
		t.Fatal(err)
	}
	if aliases.Code != http.StatusOK || aliasPage.Total != 1 || len(aliasPage.Aliases) != 1 ||
		aliasPage.Aliases[0].Name != "analysis" {
		t.Fatalf("alias list status=%d body=%s", aliases.Code, aliases.Body.String())
	}

	routerQuery := url.QueryEscape(
		`name ~ "router" AND enabled = true AND blocks >= 3 AND rules >= 1 ORDER BY rules DESC`,
	)
	routers := modelCollectionCoverageRequest(t, mux, http.MethodGet, "/api/model-routers?query="+routerQuery, nil, nil)
	var routerPage struct {
		Routers []modelRouterSummary `json:"model_routers"`
		Total   int                  `json:"total"`
	}
	if err := json.Unmarshal(routers.Body.Bytes(), &routerPage); err != nil {
		t.Fatal(err)
	}
	if routers.Code != http.StatusOK || routerPage.Total != 1 || len(routerPage.Routers) != 1 ||
		routerPage.Routers[0].Name != "task-router" {
		t.Fatalf("router list status=%d body=%s", routers.Code, routers.Body.String())
	}

	for _, test := range []struct {
		path       string
		wantStatus int
		wantCode   string
	}{
		{path: "/api/model-aliases/analysis", wantStatus: http.StatusOK},
		{path: "/api/model-aliases/missing", wantStatus: http.StatusNotFound, wantCode: "model_alias_not_found"},
		{path: "/api/model-routers/task-router", wantStatus: http.StatusOK},
		{path: "/api/model-routers/missing", wantStatus: http.StatusNotFound, wantCode: "model_router_not_found"},
		{
			path:       "/api/model-routers/task-router?other=1",
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_collection_request",
		},
	} {
		response := modelCollectionCoverageRequest(t, mux, http.MethodGet, test.path, nil, nil)
		requireModelCollectionCoverageCode(t, response, test.wantStatus, test.wantCode)
	}

	for _, path := range []string{
		"/api/model-aliases?cursor=not-a-cursor",
		"/api/model-routers?cursor=not-a-cursor",
	} {
		invalidCursor := modelCollectionCoverageRequest(t, mux, http.MethodGet, path, nil, nil)
		requireModelCollectionCoverageCode(t, invalidCursor, http.StatusBadRequest, "invalid_cursor")
	}
	invalidRouterList := modelCollectionCoverageRequest(
		t, mux, http.MethodGet, "/api/model-routers?limit=0", nil, nil,
	)
	requireModelCollectionCoverageCode(
		t,
		invalidRouterList,
		http.StatusBadRequest,
		"invalid_page_limit",
	)

	brokenConfig := filepath.Join(t.TempDir(), "config.json")
	if err := config.SaveConfig(brokenConfig, config.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	broken, err := config.LoadConfig(brokenConfig)
	if err != nil {
		t.Fatal(err)
	}
	broken.ModelAliases = []config.ModelAliasConfig{{Name: "bad/name", Model: "gpt-5.4"}}
	broken.ModelRouters = []config.ModelRouterConfig{modelCollectionCoverageRouter("bad/name")}
	if err := config.SaveConfig(brokenConfig, broken); err != nil {
		t.Fatal(err)
	}
	brokenMux := modelCollectionCoverageMux(NewHandler(brokenConfig))
	for _, path := range []string{"/api/model-aliases", "/api/model-routers"} {
		response := modelCollectionCoverageRequest(t, brokenMux, http.MethodGet, path, nil, nil)
		requireModelCollectionCoverageCode(t, response, http.StatusInternalServerError, "config_load_failed")
	}
}

func TestModelCollectionAliasMutationErrorCoverage(t *testing.T) {
	configPath := modelCollectionCoverageConfig(t, func(cfg *config.Config) {
		cfg.Agents.Defaults.ModelName = "coding"
	})
	handler := NewHandler(configPath)
	mux := modelCollectionCoverageMux(handler)
	revision := modelCollectionCoverageRevision(t, configPath)
	validAlias := config.ModelAliasConfig{Name: "analysis", Model: "gpt-5.4-mini"}

	for _, test := range []struct {
		name       string
		method     string
		path       string
		body       any
		wantStatus int
		wantCode   string
	}{
		{
			name: "create missing object", method: http.MethodPost, path: "/api/model-aliases",
			body:       map[string]any{"expected_config_revision": revision},
			wantStatus: http.StatusBadRequest, wantCode: "invalid_model_alias",
		},
		{
			name: "create normalization", method: http.MethodPost, path: "/api/model-aliases",
			body: map[string]any{"expected_config_revision": revision, "model_alias": config.ModelAliasConfig{
				Name: "bad-duplicates", Model: "gpt-5.4", DisabledAccounts: []string{" same ", "same"},
			}},
			wantStatus: http.StatusUnprocessableEntity, wantCode: "invalid_model_alias",
		},
		{
			name: "create invalid identity", method: http.MethodPost, path: "/api/model-aliases",
			body: map[string]any{
				"expected_config_revision": revision,
				"model_alias": config.ModelAliasConfig{
					Name: "bad/name", Model: "gpt-5.4",
				},
			},
			wantStatus: http.StatusUnprocessableEntity, wantCode: "invalid_model_alias",
		},
		{
			name: "create missing fence", method: http.MethodPost, path: "/api/model-aliases",
			body:       map[string]any{"model_alias": validAlias},
			wantStatus: http.StatusPreconditionRequired, wantCode: "config_revision_required",
		},
		{
			name: "create existing", method: http.MethodPost, path: "/api/model-aliases",
			body: map[string]any{
				"expected_config_revision": revision,
				"model_alias": config.ModelAliasConfig{
					Name: "coding", Model: "gpt-5.4",
				},
			},
			wantStatus: http.StatusConflict, wantCode: "model_alias_exists",
		},
		{
			name: "create invalid configuration", method: http.MethodPost, path: "/api/model-aliases",
			body: map[string]any{
				"expected_config_revision": revision,
				"model_alias":              config.ModelAliasConfig{Name: "empty-model"},
			},
			wantStatus: http.StatusUnprocessableEntity, wantCode: "invalid_model_configuration",
		},
		{
			name: "update missing object", method: http.MethodPut, path: "/api/model-aliases/coding",
			body:       map[string]any{"expected_config_revision": revision},
			wantStatus: http.StatusBadRequest, wantCode: "invalid_model_alias",
		},
		{
			name: "update normalization", method: http.MethodPut, path: "/api/model-aliases/coding",
			body: map[string]any{"expected_config_revision": revision, "model_alias": config.ModelAliasConfig{
				Name: "coding", Model: "gpt-5.4", AccountOverrides: map[string]string{"openai-work": "one", " openai-work ": "two"},
			}},
			wantStatus: http.StatusUnprocessableEntity, wantCode: "invalid_model_alias",
		},
		{
			name: "update invalid identity", method: http.MethodPut, path: "/api/model-aliases/coding",
			body: map[string]any{
				"expected_config_revision": revision,
				"model_alias": config.ModelAliasConfig{
					Name: "bad/name", Model: "gpt-5.4",
				},
			},
			wantStatus: http.StatusUnprocessableEntity, wantCode: "invalid_model_alias",
		},
		{
			name: "update immutable name", method: http.MethodPut, path: "/api/model-aliases/coding",
			body:       map[string]any{"expected_config_revision": revision, "model_alias": validAlias},
			wantStatus: http.StatusConflict, wantCode: "model_alias_name_immutable",
		},
		{
			name: "update missing fence", method: http.MethodPut, path: "/api/model-aliases/coding",
			body:       map[string]any{"model_alias": config.ModelAliasConfig{Name: "coding", Model: "gpt-5.4"}},
			wantStatus: http.StatusPreconditionRequired, wantCode: "config_revision_required",
		},
		{
			name: "update not found", method: http.MethodPut, path: "/api/model-aliases/missing",
			body: map[string]any{
				"expected_config_revision": revision,
				"model_alias": config.ModelAliasConfig{
					Name: "missing", Model: "gpt-5.4",
				},
			},
			wantStatus: http.StatusNotFound, wantCode: "model_alias_not_found",
		},
		{
			name: "update invalid configuration", method: http.MethodPut, path: "/api/model-aliases/coding",
			body: map[string]any{
				"expected_config_revision": revision,
				"model_alias":              config.ModelAliasConfig{Name: "coding"},
			},
			wantStatus: http.StatusUnprocessableEntity, wantCode: "invalid_model_configuration",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := modelCollectionCoverageJSON(t, mux, test.method, test.path, test.body)
			requireModelCollectionCoverageCode(t, response, test.wantStatus, test.wantCode)
		})
	}

	deleteMissing := modelCollectionCoverageRequest(
		t, mux, http.MethodDelete, "/api/model-aliases/missing?revision="+url.QueryEscape(revision), nil, nil,
	)
	requireModelCollectionCoverageCode(t, deleteMissing, http.StatusNotFound, "model_alias_not_found")
	deleteWithoutFence := modelCollectionCoverageRequest(
		t, mux, http.MethodDelete, "/api/model-aliases/missing", nil, nil,
	)
	requireModelCollectionCoverageCode(
		t,
		deleteWithoutFence,
		http.StatusPreconditionRequired,
		"config_revision_required",
	)
	deleteReferenced := modelCollectionCoverageRequest(
		t, mux, http.MethodDelete, "/api/model-aliases/coding?revision="+url.QueryEscape(revision), nil, nil,
	)
	requireModelCollectionCoverageCode(t, deleteReferenced, http.StatusConflict, "model_alias_referenced")
	if !strings.Contains(deleteReferenced.Body.String(), "agents.defaults.model_name") {
		t.Fatalf("reference blockers=%s", deleteReferenced.Body.String())
	}

	for _, body := range []any{
		map[string]any{"ids": []string{}, "config_revision": revision},
		map[string]any{"ids": []string{"coding"}, "config_revision": "one", "expected_config_revision": "two"},
		map[string]any{"ids": []string{"coding"}},
	} {
		response := modelCollectionCoverageJSON(t, mux, http.MethodPost, "/api/model-aliases/bulk-delete", body)
		if response.Code < http.StatusBadRequest {
			t.Fatalf("bulk validation status=%d body=%s", response.Code, response.Body.String())
		}
	}
}

func TestModelCollectionRouterMutationAndBulkCoverage(t *testing.T) {
	configPath := modelCollectionCoverageConfig(t, func(cfg *config.Config) {
		cfg.ModelRouters = []config.ModelRouterConfig{
			modelCollectionCoverageRouter("task-router"),
			modelCollectionCoverageRouter("free-router"),
		}
		cfg.Agents.Defaults.ModelName = "task-router"
	})
	handler := NewHandler(configPath)
	mux := modelCollectionCoverageMux(handler)
	revision := modelCollectionCoverageRevision(t, configPath)
	validRouter := modelCollectionCoverageRouter("new-router")

	for _, test := range []struct {
		name       string
		method     string
		path       string
		body       any
		wantStatus int
		wantCode   string
	}{
		{
			name: "create missing object", method: http.MethodPost, path: "/api/model-routers",
			body:       map[string]any{"expected_config_revision": revision},
			wantStatus: http.StatusBadRequest, wantCode: "invalid_model_router",
		},
		{
			name: "create invalid identity", method: http.MethodPost, path: "/api/model-routers",
			body: map[string]any{
				"expected_config_revision": revision,
				"model_router":             modelCollectionCoverageRouter("bad/name"),
			},
			wantStatus: http.StatusUnprocessableEntity, wantCode: "invalid_model_router",
		},
		{
			name: "create missing fence", method: http.MethodPost, path: "/api/model-routers",
			body:       map[string]any{"model_router": validRouter},
			wantStatus: http.StatusPreconditionRequired, wantCode: "config_revision_required",
		},
		{
			name: "create existing", method: http.MethodPost, path: "/api/model-routers",
			body: map[string]any{
				"expected_config_revision": revision,
				"model_router":             modelCollectionCoverageRouter("task-router"),
			},
			wantStatus: http.StatusConflict, wantCode: "model_router_exists",
		},
		{
			name: "create invalid configuration", method: http.MethodPost, path: "/api/model-routers",
			body: map[string]any{
				"expected_config_revision": revision,
				"model_router":             config.ModelRouterConfig{Name: "disabled-router"},
			},
			wantStatus: http.StatusUnprocessableEntity, wantCode: "invalid_model_configuration",
		},
		{
			name: "update missing object", method: http.MethodPut, path: "/api/model-routers/task-router",
			body:       map[string]any{"expected_config_revision": revision},
			wantStatus: http.StatusBadRequest, wantCode: "invalid_model_router",
		},
		{
			name: "update invalid identity", method: http.MethodPut, path: "/api/model-routers/task-router",
			body: map[string]any{
				"expected_config_revision": revision,
				"model_router":             modelCollectionCoverageRouter("bad/name"),
			},
			wantStatus: http.StatusUnprocessableEntity, wantCode: "invalid_model_router",
		},
		{
			name: "update immutable", method: http.MethodPut, path: "/api/model-routers/task-router",
			body:       map[string]any{"expected_config_revision": revision, "model_router": validRouter},
			wantStatus: http.StatusConflict, wantCode: "model_router_name_immutable",
		},
		{
			name: "update missing fence", method: http.MethodPut, path: "/api/model-routers/task-router",
			body:       map[string]any{"model_router": modelCollectionCoverageRouter("task-router")},
			wantStatus: http.StatusPreconditionRequired, wantCode: "config_revision_required",
		},
		{
			name: "update missing router", method: http.MethodPut, path: "/api/model-routers/missing",
			body: map[string]any{
				"expected_config_revision": revision,
				"model_router":             modelCollectionCoverageRouter("missing"),
			},
			wantStatus: http.StatusNotFound, wantCode: "model_router_not_found",
		},
		{
			name: "update invalid configuration", method: http.MethodPut, path: "/api/model-routers/task-router",
			body: map[string]any{
				"expected_config_revision": revision,
				"model_router":             config.ModelRouterConfig{Name: "task-router"},
			},
			wantStatus: http.StatusUnprocessableEntity, wantCode: "invalid_model_configuration",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := modelCollectionCoverageJSON(t, mux, test.method, test.path, test.body)
			requireModelCollectionCoverageCode(t, response, test.wantStatus, test.wantCode)
		})
	}

	missing := modelCollectionCoverageRequest(
		t, mux, http.MethodDelete, "/api/model-routers/missing?revision="+url.QueryEscape(revision), nil, nil,
	)
	requireModelCollectionCoverageCode(t, missing, http.StatusNotFound, "model_router_not_found")
	deleteWithoutFence := modelCollectionCoverageRequest(
		t, mux, http.MethodDelete, "/api/model-routers/missing", nil, nil,
	)
	requireModelCollectionCoverageCode(
		t,
		deleteWithoutFence,
		http.StatusPreconditionRequired,
		"config_revision_required",
	)
	referenced := modelCollectionCoverageRequest(
		t, mux, http.MethodDelete, "/api/model-routers/task-router?revision="+url.QueryEscape(revision), nil, nil,
	)
	requireModelCollectionCoverageCode(t, referenced, http.StatusConflict, "model_router_referenced")

	bulk := modelCollectionCoverageJSON(t, mux, http.MethodPost, "/api/model-routers/bulk-delete", map[string]any{
		"ids":             []string{"free-router", "task-router", "missing", "duplicate", "duplicate", " "},
		"config_revision": revision,
	})
	if bulk.Code != http.StatusOK {
		t.Fatalf("bulk status=%d body=%s", bulk.Code, bulk.Body.String())
	}
	var result collectionBulkDeleteResponse
	if err := json.Unmarshal(bulk.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.DeletedIDs, []string{"free-router"}) || len(result.Failures) != 4 {
		t.Fatalf("bulk result=%#v", result)
	}

	noDelete := modelCollectionCoverageJSON(t, mux, http.MethodPost, "/api/model-routers/bulk-delete", map[string]any{
		"ids": []string{"missing"}, "config_revision": result.ConfigRevision,
	})
	if noDelete.Code != http.StatusOK || !strings.Contains(noDelete.Body.String(), `"deleted_ids":[]`) {
		t.Fatalf("no-delete status=%d body=%s", noDelete.Code, noDelete.Body.String())
	}

	for _, body := range []any{
		map[string]any{"ids": []string{}, "config_revision": result.ConfigRevision},
		map[string]any{"ids": []string{"task-router"}, "config_revision": "one", "expected_config_revision": "two"},
		map[string]any{"ids": []string{"task-router"}},
	} {
		response := modelCollectionCoverageJSON(t, mux, http.MethodPost, "/api/model-routers/bulk-delete", body)
		if response.Code < http.StatusBadRequest {
			t.Fatalf("bulk validation status=%d body=%s", response.Code, response.Body.String())
		}
	}
}

func TestModelCollectionResolversFailClosedWhenSchemaAndResolverDiverge(t *testing.T) {
	configPath := modelCollectionCoverageConfig(t, func(cfg *config.Config) {
		cfg.ModelRouters = []config.ModelRouterConfig{modelCollectionCoverageRouter("task-router")}
	})
	mux := modelCollectionCoverageMux(NewHandler(configPath))

	func() {
		original := modelAliasCollectionSchema
		defer func() { modelAliasCollectionSchema = original }()
		modelAliasCollectionSchema = mustCollectionQuerySchema(
			[]collectionquery.FieldSchema{
				{Name: "name", Type: collectionquery.TypeString, Sortable: true},
				{Name: "unsupported", Type: collectionquery.TypeString, Sortable: true},
			},
			[]collectionquery.SortField{{Field: "name", Direction: collectionquery.Ascending}},
		)
		response := modelCollectionCoverageRequest(
			t,
			mux,
			http.MethodGet,
			"/api/model-aliases?query="+url.QueryEscape(`unsupported = value`),
			nil,
			nil,
		)
		requireModelCollectionCoverageCode(t, response, http.StatusInternalServerError, "collection_page_failed")
	}()

	func() {
		original := modelRouterCollectionSchema
		defer func() { modelRouterCollectionSchema = original }()
		modelRouterCollectionSchema = mustCollectionQuerySchema(
			[]collectionquery.FieldSchema{
				{Name: "name", Type: collectionquery.TypeString, Sortable: true},
				{Name: "unsupported", Type: collectionquery.TypeString, Sortable: true},
			},
			[]collectionquery.SortField{{Field: "name", Direction: collectionquery.Ascending}},
		)
		response := modelCollectionCoverageRequest(
			t,
			mux,
			http.MethodGet,
			"/api/model-routers?query="+url.QueryEscape(`unsupported = value`),
			nil,
			nil,
		)
		requireModelCollectionCoverageCode(t, response, http.StatusInternalServerError, "collection_page_failed")
	}()
}

func TestModelCollectionLoadDecodeQueryAndSaveFailureCoverage(t *testing.T) {
	// A directory at the configured file path forces every snapshot load to fail.
	brokenPath := t.TempDir()
	brokenMux := modelCollectionCoverageMux(NewHandler(brokenPath))
	alias := config.ModelAliasConfig{Name: "analysis", Model: "gpt-5.4"}
	router := modelCollectionCoverageRouter("task-router")
	for _, test := range []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{name: "list aliases", method: http.MethodGet, path: "/api/model-aliases"},
		{name: "get alias", method: http.MethodGet, path: "/api/model-aliases/coding"},
		{
			name: "create alias", method: http.MethodPost, path: "/api/model-aliases",
			body: map[string]any{"expected_config_revision": "x", "model_alias": alias},
		},
		{
			name: "update alias", method: http.MethodPut, path: "/api/model-aliases/analysis",
			body: map[string]any{"expected_config_revision": "x", "model_alias": alias},
		},
		{name: "delete alias", method: http.MethodDelete, path: "/api/model-aliases/analysis?revision=x"},
		{
			name: "bulk aliases", method: http.MethodPost, path: "/api/model-aliases/bulk-delete",
			body: map[string]any{"ids": []string{"analysis"}, "config_revision": "x"},
		},
		{name: "list routers", method: http.MethodGet, path: "/api/model-routers"},
		{name: "get router", method: http.MethodGet, path: "/api/model-routers/task-router"},
		{
			name: "create router", method: http.MethodPost, path: "/api/model-routers",
			body: map[string]any{"expected_config_revision": "x", "model_router": router},
		},
		{
			name: "update router", method: http.MethodPut, path: "/api/model-routers/task-router",
			body: map[string]any{"expected_config_revision": "x", "model_router": router},
		},
		{name: "delete router", method: http.MethodDelete, path: "/api/model-routers/task-router?revision=x"},
		{
			name: "bulk routers", method: http.MethodPost, path: "/api/model-routers/bulk-delete",
			body: map[string]any{"ids": []string{"task-router"}, "config_revision": "x"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var response *httptest.ResponseRecorder
			if test.body == nil {
				response = modelCollectionCoverageRequest(t, brokenMux, test.method, test.path, nil, nil)
			} else {
				response = modelCollectionCoverageJSON(t, brokenMux, test.method, test.path, test.body)
			}
			if response.Code != http.StatusInternalServerError {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}

	configPath := modelCollectionCoverageConfig(t, func(cfg *config.Config) {
		cfg.ModelAliases = append(cfg.ModelAliases, alias)
		cfg.ModelRouters = []config.ModelRouterConfig{router}
	})
	handler := NewHandler(configPath)
	mux := modelCollectionCoverageMux(handler)
	revision := modelCollectionCoverageRevision(t, configPath)

	for _, test := range []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/api/model-aliases"},
		{method: http.MethodPut, path: "/api/model-aliases/analysis"},
		{method: http.MethodPost, path: "/api/model-aliases/bulk-delete"},
		{method: http.MethodPost, path: "/api/model-routers"},
		{method: http.MethodPut, path: "/api/model-routers/task-router"},
		{method: http.MethodPost, path: "/api/model-routers/bulk-delete"},
	} {
		response := modelCollectionCoverageRequest(t, mux, test.method, test.path, []byte(`{`), nil)
		requireModelCollectionCoverageCode(t, response, http.StatusBadRequest, "invalid_collection_request")
	}

	for _, test := range []struct {
		method string
		path   string
		body   any
	}{
		{method: http.MethodPost, path: "/api/model-aliases?other=1", body: map[string]any{}},
		{method: http.MethodPut, path: "/api/model-aliases/analysis?other=1", body: map[string]any{}},
		{method: http.MethodDelete, path: "/api/model-aliases/analysis?other=1"},
		{method: http.MethodPost, path: "/api/model-aliases/bulk-delete?other=1", body: map[string]any{}},
		{method: http.MethodPost, path: "/api/model-routers?other=1", body: map[string]any{}},
		{method: http.MethodPut, path: "/api/model-routers/task-router?other=1", body: map[string]any{}},
		{method: http.MethodDelete, path: "/api/model-routers/task-router?other=1"},
		{method: http.MethodPost, path: "/api/model-routers/bulk-delete?other=1", body: map[string]any{}},
	} {
		response := modelCollectionCoverageJSON(t, mux, test.method, test.path, test.body)
		requireModelCollectionCoverageCode(t, response, http.StatusBadRequest, "invalid_collection_request")
	}

	for _, test := range []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{
			name: "create alias", method: http.MethodPost, path: "/api/model-aliases",
			body: map[string]any{
				"expected_config_revision": revision,
				"model_alias": config.ModelAliasConfig{
					Name: "save-alias", Model: "gpt-5.4",
				},
			},
		},
		{
			name: "update alias", method: http.MethodPut, path: "/api/model-aliases/analysis",
			body: map[string]any{"expected_config_revision": revision, "model_alias": alias},
		},
		{
			name:   "delete alias",
			method: http.MethodDelete,
			path:   "/api/model-aliases/analysis?revision=" + url.QueryEscape(revision),
		},
		{
			name: "bulk alias", method: http.MethodPost, path: "/api/model-aliases/bulk-delete",
			body: map[string]any{"ids": []string{"analysis"}, "config_revision": revision},
		},
		{
			name: "create router", method: http.MethodPost, path: "/api/model-routers",
			body: map[string]any{
				"expected_config_revision": revision,
				"model_router":             modelCollectionCoverageRouter("save-router"),
			},
		},
		{
			name: "update router", method: http.MethodPut, path: "/api/model-routers/task-router",
			body: map[string]any{"expected_config_revision": revision, "model_router": router},
		},
		{
			name:   "delete router",
			method: http.MethodDelete,
			path:   "/api/model-routers/task-router?revision=" + url.QueryEscape(revision),
		},
		{
			name: "bulk router", method: http.MethodPost, path: "/api/model-routers/bulk-delete",
			body: map[string]any{"ids": []string{"task-router"}, "config_revision": revision},
		},
	} {
		t.Run("save "+test.name, func(t *testing.T) {
			failing := NewHandler(configPath)
			failing.saveConfigIfRevision = func(string, *config.Config, string) (string, error) {
				return "", errors.New("disk failure")
			}
			failingMux := modelCollectionCoverageMux(failing)
			var response *httptest.ResponseRecorder
			if test.body == nil {
				response = modelCollectionCoverageRequest(t, failingMux, test.method, test.path, nil, nil)
			} else {
				response = modelCollectionCoverageJSON(t, failingMux, test.method, test.path, test.body)
			}
			requireModelCollectionCoverageCode(t, response, http.StatusInternalServerError, "config_save_failed")
		})
	}
}

func TestRepositoryModelEvaluationCollectionAdditionalCoverage(t *testing.T) {
	handler, mux, _ := newRepositoryModelEvaluationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	created := createRepositoryModelEvaluation(t, mux, "owner/query-coverage")
	queryText := `id = "` + created.ID + `"` +
		` AND repository ~ "owner" AND ref = main` +
		` AND models >= 2 AND progress >= 0 AND version >= 1` +
		` ORDER BY created DESC, updated DESC`
	query := url.QueryEscape(queryText)
	response := modelCollectionCoverageRequest(t, mux, http.MethodGet, "/api/model-evaluations?query="+query, nil, nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), created.ID) {
		t.Fatalf("list status=%d body=%s", response.Code, response.Body.String())
	}

	for _, id := range []string{"", "rme_short", "rme_" + strings.Repeat("g", 32), "rme_" + strings.Repeat("a", 32)} {
		want := id == "rme_"+strings.Repeat("a", 32)
		if got := validRepositoryModelEvaluationCollectionID(id); got != want {
			t.Fatalf("valid evaluation ID %q=%v, want %v", id, got, want)
		}
	}

	brokenConfigPath := t.TempDir()
	brokenHandler := NewHandler(brokenConfigPath)
	brokenMux := modelCollectionCoverageMux(brokenHandler)
	for _, test := range []struct {
		method     string
		path       string
		body       any
		wantStatus int
		wantCode   string
	}{
		{
			method: http.MethodGet, path: "/api/model-evaluations",
			wantStatus: http.StatusInternalServerError, wantCode: "repository_model_evaluation_unavailable",
		},
		{method: http.MethodPost, path: "/api/model-evaluations/bulk-delete", body: map[string]any{
			"items": []map[string]any{{"id": created.ID, "version": created.Version}},
		}, wantStatus: http.StatusBadRequest, wantCode: "invalid_repository_model_evaluation"},
	} {
		var got *httptest.ResponseRecorder
		if test.body == nil {
			got = modelCollectionCoverageRequest(t, brokenMux, test.method, test.path, nil, nil)
		} else {
			got = modelCollectionCoverageJSON(t, brokenMux, test.method, test.path, test.body)
		}
		requireModelCollectionCoverageCode(
			t,
			got,
			test.wantStatus,
			test.wantCode,
		)
	}

	canceledRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/model-evaluations/bulk-delete",
		strings.NewReader(`{"items":[{"id":"`+created.ID+`","version":1}]}`),
	)
	canceledRequest.Header.Set("Content-Type", "application/json")
	canceledRequest.Header.Set("Sec-Fetch-Site", "same-origin")
	canceledContext, cancel := context.WithCancel(t.Context())
	cancel()
	canceledRequest = canceledRequest.WithContext(canceledContext)
	canceledResponse := httptest.NewRecorder()
	mux.ServeHTTP(canceledResponse, canceledRequest)
	requireModelCollectionCoverageCode(
		t,
		canceledResponse,
		http.StatusInternalServerError,
		"repository_model_evaluation_unavailable",
	)
}
