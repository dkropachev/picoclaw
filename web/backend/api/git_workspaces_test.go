package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
)

const (
	gitWorkspaceOneID   = "gw-aaaaaaaaaaaa"
	gitWorkspaceTwoID   = "gw-bbbbbbbbbbbb"
	gitWorkspaceThreeID = "gw-cccccccccccc"
)

type fakeGitWorkspaceStatsResult struct {
	stats gitworkspace.Stats
	err   error
}

type fakeGitWorkspaceManager struct {
	statsResults   []fakeGitWorkspaceStatsResult
	statsCalls     int
	reconcile      gitworkspace.ReconcileResult
	reconcileErr   error
	cleanup        gitworkspace.CleanupResult
	cleanupErr     error
	drop           gitworkspace.WorkspaceInfo
	dropErr        error
	reconcileCalls int
	cleanupCalls   int
	dropCalls      int
}

type failingGitWorkspaceResponseWriter struct {
	header http.Header
}

func (writer *failingGitWorkspaceResponseWriter) Header() http.Header {
	if writer.header == nil {
		writer.header = make(http.Header)
	}
	return writer.header
}

func (*failingGitWorkspaceResponseWriter) WriteHeader(int) {}

func (*failingGitWorkspaceResponseWriter) Write([]byte) (int, error) {
	return 0, errors.New("injected response write failure")
}

func (manager *fakeGitWorkspaceManager) Stats(context.Context) (gitworkspace.Stats, error) {
	manager.statsCalls++
	if len(manager.statsResults) == 0 {
		return gitworkspace.Stats{}, nil
	}
	index := manager.statsCalls - 1
	if index >= len(manager.statsResults) {
		index = len(manager.statsResults) - 1
	}
	return manager.statsResults[index].stats, manager.statsResults[index].err
}

func (manager *fakeGitWorkspaceManager) Reconcile(context.Context) (gitworkspace.ReconcileResult, error) {
	manager.reconcileCalls++
	return manager.reconcile, manager.reconcileErr
}

func (manager *fakeGitWorkspaceManager) CleanupIgnored(context.Context, string) (gitworkspace.CleanupResult, error) {
	manager.cleanupCalls++
	return manager.cleanup, manager.cleanupErr
}

func (manager *fakeGitWorkspaceManager) Drop(context.Context, string) (gitworkspace.WorkspaceInfo, error) {
	manager.dropCalls++
	return manager.drop, manager.dropErr
}

func TestGitWorkspaceCollectionSchemasQueriesPagingAndSuggestions(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)
	items := []gitWorkspaceSummary{
		{
			ID: gitWorkspaceOneID, Repository: "git@github.com:acme/alpha.git",
			Branch: "main", Status: "available", Size: 100, Ignored: 10,
			Updated: base,
		},
		{
			ID: gitWorkspaceTwoID, Repository: "git@github.com:acme/beta.git",
			Branch: "feature", Status: "locked", Locked: true, Dirty: true,
			Size: 200, Ignored: 20, Updated: base.Add(time.Hour),
		},
		{
			ID: gitWorkspaceThreeID, Repository: "local/gamma",
			Branch: "preserved", Status: "dropped", Dirty: true,
			Size: 0, Ignored: 0, Updated: base.Add(2 * time.Hour),
		},
	}
	tests := []struct {
		query string
		want  string
	}{
		{`id = "` + gitWorkspaceOneID + `"`, gitWorkspaceOneID},
		{`repository ~ "beta"`, gitWorkspaceTwoID},
		{`branch = "preserved"`, gitWorkspaceThreeID},
		{`status = "locked"`, gitWorkspaceTwoID},
		{`locked = true`, gitWorkspaceTwoID},
		{`dirty = false`, gitWorkspaceOneID},
		{`size >= 200`, gitWorkspaceTwoID},
		{`ignored = 10`, gitWorkspaceOneID},
		{`updated > "2026-08-27T13:30:00Z"`, gitWorkspaceThreeID},
	}
	for _, test := range tests {
		query, err := collectionquery.Parse(test.query, gitWorkspaceCollectionSchema)
		if err != nil {
			t.Fatalf("Parse(%q) error = %v", test.query, err)
		}
		page, err := pageGitWorkspaces(items, collectionListRequest{
			Query: query, Limit: 200, Now: base,
		})
		if err != nil || page.Total != 1 || len(page.Items) != 1 ||
			page.Items[0].ID != test.want {
			t.Fatalf("query %q page = %#v, %v", test.query, page, err)
		}
	}
	ordered, err := collectionquery.Parse(
		"ORDER BY dirty DESC, size DESC, id ASC",
		gitWorkspaceCollectionSchema,
	)
	if err != nil {
		t.Fatal(err)
	}
	page, err := pageGitWorkspaces(items, collectionListRequest{
		Query: ordered, Limit: 1, Now: base,
	})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != gitWorkspaceTwoID ||
		page.NextCursor == "" {
		t.Fatalf("ordered first page = %#v, %v", page, err)
	}
	next, err := pageGitWorkspaces(items, collectionListRequest{
		Query: ordered, Cursor: page.NextCursor, Limit: 1, Now: base,
	})
	if err != nil || len(next.Items) != 1 || next.Items[0].ID != gitWorkspaceThreeID {
		t.Fatalf("ordered second page = %#v, %v", next, err)
	}
	different, err := collectionquery.Parse(
		"dirty = false ORDER BY updated DESC",
		gitWorkspaceCollectionSchema,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pageGitWorkspaces(items, collectionListRequest{
		Query: different, Cursor: page.NextCursor, Limit: 1, Now: base,
	}); !errors.Is(err, collectionquery.ErrInvalidCursor) {
		t.Fatalf("cursor/query mismatch error = %v", err)
	}
	if _, ok := resolveGitWorkspaceCollectionField(items[0], "unknown", base); ok {
		t.Fatal("workspace resolver accepted unknown field")
	}

	history := []gitWorkspaceHistorySummary{
		{
			ID: "111111111111", Action: "allocated", Workspace: gitWorkspaceOneID,
			Repository: "git@github.com:acme/alpha.git", Agent: "main", Time: base,
		},
		{
			ID: "222222222222", Action: "cleaned_ignored", Workspace: gitWorkspaceTwoID,
			Repository: "git@github.com:acme/beta.git", Agent: "worker",
			Time: base.Add(time.Hour),
		},
	}
	for _, test := range []struct {
		query string
		want  string
	}{
		{`action = "allocated"`, "111111111111"},
		{`workspace = "` + gitWorkspaceTwoID + `"`, "222222222222"},
		{`repository ~ "alpha"`, "111111111111"},
		{`agent = "worker"`, "222222222222"},
		{`time > "2026-08-27T12:30:00Z"`, "222222222222"},
	} {
		query, err := collectionquery.Parse(test.query, gitWorkspaceHistoryCollectionSchema)
		if err != nil {
			t.Fatal(err)
		}
		page, err := pageGitWorkspaceHistory(history, collectionListRequest{
			Query: query, Limit: 200, Now: base,
		})
		if err != nil || page.Total != 1 || page.Items[0].ID != test.want {
			t.Fatalf("history query %q = %#v, %v", test.query, page, err)
		}
	}
	defaultHistory, err := collectionquery.Parse("", gitWorkspaceHistoryCollectionSchema)
	if err != nil {
		t.Fatal(err)
	}
	historyPage, err := pageGitWorkspaceHistory(history, collectionListRequest{
		Query: defaultHistory, Limit: 1, Now: base,
	})
	if err != nil || historyPage.Items[0].ID != "222222222222" ||
		historyPage.NextCursor == "" {
		t.Fatalf("history default page = %#v, %v", historyPage, err)
	}
	if _, ok := resolveGitWorkspaceHistoryCollectionField(history[0], "unknown", base); ok {
		t.Fatal("history resolver accepted unknown field")
	}

	many := make([]gitWorkspaceSummary, 150)
	manyHistory := make([]gitWorkspaceHistorySummary, 150)
	for index := range many {
		many[index] = gitWorkspaceSummary{
			ID:         "gw-" + strings.Repeat(string(rune('a'+index%26)), 12),
			Repository: "repository-" + string(rune('a'+index%26)),
			Branch:     "branch-" + string(rune('a'+index%26)),
		}
		manyHistory[index] = gitWorkspaceHistorySummary{
			Action:    "action-" + string(rune('a'+index%26)),
			Workspace: many[index].ID, Repository: many[index].Repository,
			Agent: "agent-" + string(rune('a'+index%26)),
		}
	}
	for _, schema := range []collectionquery.Schema{
		registerGitWorkspaceCollectionSchemaSuggestions(many),
		gitWorkspaceHistorySchemaWithSuggestions(manyHistory),
	} {
		for _, field := range schema.Fields {
			if len(field.SuggestedValues) > collectionquery.MaxSuggestedValues {
				t.Fatalf("field %s suggestions = %d", field.Name, len(field.SuggestedValues))
			}
		}
	}
}

func TestGitWorkspaceCollectionHTTPListDetailAndHistoryAreSafe(t *testing.T) {
	stats := gitWorkspaceAPIStatsFixture()
	manager := &fakeGitWorkspaceManager{
		statsResults: []fakeGitWorkspaceStatsResult{{stats: stats}},
	}
	handler := NewHandler("")
	handler.loadGitWorkspaceManager = func() (gitWorkspaceManagerAPI, error) {
		return manager, nil
	}
	mux := http.NewServeMux()
	handler.registerGitWorkspaceRoutes(mux)

	list := serveGitWorkspaceRequest(
		t, mux, http.MethodGet,
		"/api/git-workspaces?query="+url.QueryEscape(`status != "dropped" ORDER BY updated DESC`)+"&limit=1",
		"", nil,
	)
	if list.Code != http.StatusOK {
		t.Fatalf("list = %d %s", list.Code, list.Body.String())
	}
	var listBody struct {
		Workspaces     []gitWorkspaceSummary  `json:"workspaces"`
		Total          int                    `json:"total"`
		NextCursor     string                 `json:"next_cursor"`
		CanonicalQuery string                 `json:"canonical_query"`
		QuerySchema    collectionquery.Schema `json:"query_schema"`
		WorkspaceCount int                    `json:"workspace_count"`
		TotalSizeBytes int64                  `json:"total_size_bytes"`
		MaxTotalSize   int64                  `json:"max_total_size_bytes"`
	}
	decodeGitWorkspaceJSON(t, list, &listBody)
	if listBody.Total != 2 || len(listBody.Workspaces) != 1 ||
		listBody.Workspaces[0].ID != gitWorkspaceTwoID || listBody.NextCursor == "" ||
		listBody.CanonicalQuery != `status != "dropped" ORDER BY updated DESC` ||
		listBody.WorkspaceCount != 2 || listBody.TotalSizeBytes != 300 ||
		listBody.MaxTotalSize != 1_000_000 {
		t.Fatalf("list body = %#v", listBody)
	}
	assertGitWorkspaceSchemaField(t, listBody.QuerySchema, "locked", collectionquery.TypeBoolean)
	for _, canary := range []string{
		"/private/git-root", "SESSION-SECRET", `"session_key"`, `"locked_by"`,
		`"path"`, `"history"`, `"repositories"`,
	} {
		if strings.Contains(list.Body.String(), canary) {
			t.Fatalf("list leaked %q: %s", canary, list.Body.String())
		}
	}

	second := serveGitWorkspaceRequest(
		t, mux, http.MethodGet,
		"/api/git-workspaces?query="+url.QueryEscape(`status != "dropped" ORDER BY updated DESC`)+
			"&limit=1&cursor="+url.QueryEscape(listBody.NextCursor),
		"", nil,
	)
	var secondBody struct {
		Workspaces []gitWorkspaceSummary `json:"workspaces"`
	}
	decodeGitWorkspaceJSON(t, second, &secondBody)
	if len(secondBody.Workspaces) != 1 || secondBody.Workspaces[0].ID != gitWorkspaceOneID {
		t.Fatalf("second page = %#v", secondBody)
	}

	detail := serveGitWorkspaceRequest(
		t, mux, http.MethodGet, "/api/git-workspaces/"+gitWorkspaceTwoID, "", nil,
	)
	if detail.Code != http.StatusOK {
		t.Fatalf("detail = %d %s", detail.Code, detail.Body.String())
	}
	var detailBody struct {
		Workspace gitWorkspaceDetail `json:"workspace"`
	}
	decodeGitWorkspaceJSON(t, detail, &detailBody)
	if detailBody.Workspace.ID != gitWorkspaceTwoID ||
		detailBody.Workspace.Path != "/private/checkouts/beta" ||
		detailBody.Workspace.RemoteURL != "git@github.com:acme/beta.git" ||
		detailBody.Workspace.LockedBy == nil ||
		detailBody.Workspace.LockedBy.AgentID != "worker" {
		t.Fatalf("detail body = %#v", detailBody.Workspace)
	}
	if strings.Contains(detail.Body.String(), "SESSION-SECRET") ||
		strings.Contains(detail.Body.String(), "session_key") {
		t.Fatalf("detail leaked session identity: %s", detail.Body.String())
	}

	history := serveGitWorkspaceRequest(
		t, mux, http.MethodGet,
		"/api/git-workspaces/history?query="+url.QueryEscape(`action ~ "ed" ORDER BY time DESC`),
		"", nil,
	)
	if history.Code != http.StatusOK {
		t.Fatalf("history = %d %s", history.Code, history.Body.String())
	}
	var historyBody gitWorkspaceHistoryResponse
	decodeGitWorkspaceJSON(t, history, &historyBody)
	if historyBody.Total != 2 || len(historyBody.History) != 2 ||
		historyBody.History[0].ID != "222222222222" ||
		historyBody.History[0].Repository != "git@github.com:acme/beta.git" ||
		historyBody.History[1].Repository != "git@github.com:acme/alpha.git" {
		t.Fatalf("history body = %#v", historyBody)
	}
	for _, canary := range []string{
		"SESSION-SECRET", "DETAIL-SECRET", "/private/checkouts", "/private/repos",
		`"session_key"`, `"detail"`, `"repo_id"`, `"workspace_id"`,
	} {
		if strings.Contains(history.Body.String(), canary) {
			t.Fatalf("history leaked %q: %s", canary, history.Body.String())
		}
	}
	assertGitWorkspaceSchemaField(t, historyBody.QuerySchema, "time", collectionquery.TypeTimestamp)

	for _, test := range []struct {
		target string
		status int
		code   string
	}{
		{"/api/git-workspaces/not-valid", http.StatusBadRequest, "invalid_git_workspace_id"},
		{"/api/git-workspaces/gw-dddddddddddd", http.StatusNotFound, "git_workspace_not_found"},
		{"/api/git-workspaces/" + gitWorkspaceOneID + "?unexpected=1", http.StatusBadRequest, "invalid_collection_request"},
	} {
		response := serveGitWorkspaceRequest(t, mux, http.MethodGet, test.target, "", nil)
		requireGitWorkspaceError(t, response, test.status, test.code)
	}

	badQuery := serveGitWorkspaceRequest(
		t, mux, http.MethodGet,
		"/api/git-workspaces?query="+url.QueryEscape(`repository = "é" AND`),
		"", nil,
	)
	requireGitWorkspaceError(t, badQuery, http.StatusBadRequest, "invalid_query")
	if !strings.Contains(badQuery.Body.String(), `"position":`) {
		t.Fatalf("UTF-8 query error omitted byte position: %s", badQuery.Body.String())
	}

	manager.statsResults = []fakeGitWorkspaceStatsResult{{err: errors.New("PRIVATE-STATS-ERROR")}}
	manager.statsCalls = 0
	unavailable := serveGitWorkspaceRequest(
		t, mux, http.MethodGet, "/api/git-workspaces", "", nil,
	)
	requireGitWorkspaceError(t, unavailable, http.StatusInternalServerError, "git_workspaces_unavailable")
	if strings.Contains(unavailable.Body.String(), "PRIVATE-STATS-ERROR") {
		t.Fatalf("stats error leaked: %s", unavailable.Body.String())
	}
}

func TestGitWorkspaceMutationsAreSameOriginStrictAndStructured(t *testing.T) {
	stats := gitWorkspaceAPIStatsFixture()
	available, _ := findPublicGitWorkspace(stats, gitWorkspaceOneID)
	manager := &fakeGitWorkspaceManager{
		statsResults: []fakeGitWorkspaceStatsResult{{stats: stats}},
		cleanup: gitworkspace.CleanupResult{
			Workspace: available, Before: 64, After: 0,
		},
		drop: available,
		reconcile: gitworkspace.ReconcileResult{
			Cleaned: []gitworkspace.WorkspaceInfo{available}, Stats: stats,
		},
	}
	handler := NewHandler("")
	handler.loadGitWorkspaceManager = func() (gitWorkspaceManagerAPI, error) {
		return manager, nil
	}
	mux := http.NewServeMux()
	handler.registerGitWorkspaceRoutes(mux)

	for _, test := range []struct {
		method string
		target string
		body   string
	}{
		{http.MethodPost, "/api/git-workspaces/reconcile", ""},
		{http.MethodPost, "/api/git-workspaces/cleanup", `{"workspace_id":"` + gitWorkspaceOneID + `"}`},
		{http.MethodDelete, "/api/git-workspaces/" + gitWorkspaceOneID, ""},
	} {
		response := serveGitWorkspaceRequest(t, mux, test.method, test.target, test.body, map[string]string{
			"Content-Type": "application/json", "Sec-Fetch-Site": "cross-site",
		})
		requireGitWorkspaceError(t, response, http.StatusForbidden, "cross_origin_mutation")
	}
	if manager.reconcileCalls != 0 || manager.cleanupCalls != 0 || manager.dropCalls != 0 {
		t.Fatalf("cross-origin mutation reached manager: %#v", manager)
	}

	cleanup := serveGitWorkspaceRequest(
		t, mux, http.MethodPost, "/api/git-workspaces/cleanup",
		`{"workspace_id":"`+gitWorkspaceOneID+`"}`,
		map[string]string{"Content-Type": "application/json"},
	)
	if cleanup.Code != http.StatusOK || strings.Contains(cleanup.Body.String(), "SESSION-SECRET") {
		t.Fatalf("cleanup = %d %s", cleanup.Code, cleanup.Body.String())
	}
	var cleanupBody struct {
		Before    int64              `json:"before_ignored_bytes"`
		After     int64              `json:"after_ignored_bytes"`
		Workspace gitWorkspaceDetail `json:"workspace"`
	}
	decodeGitWorkspaceJSON(t, cleanup, &cleanupBody)
	if cleanupBody.Before != 64 || cleanupBody.After != 0 ||
		cleanupBody.Workspace.ID != gitWorkspaceOneID {
		t.Fatalf("cleanup body = %#v", cleanupBody)
	}

	drop := serveGitWorkspaceRequest(
		t, mux, http.MethodDelete, "/api/git-workspaces/"+gitWorkspaceOneID, "", nil,
	)
	if drop.Code != http.StatusOK || strings.Contains(drop.Body.String(), "SESSION-SECRET") {
		t.Fatalf("drop = %d %s", drop.Code, drop.Body.String())
	}

	reconcile := serveGitWorkspaceRequest(
		t, mux, http.MethodPost, "/api/git-workspaces/reconcile", "", nil,
	)
	if reconcile.Code != http.StatusOK || strings.Contains(reconcile.Body.String(), "/private/") ||
		strings.Contains(reconcile.Body.String(), "SESSION-SECRET") {
		t.Fatalf("reconcile = %d %s", reconcile.Code, reconcile.Body.String())
	}

	jsonHeaders := map[string]string{"Content-Type": "application/json"}
	strictCases := []struct {
		name    string
		body    string
		headers map[string]string
		status  int
		code    string
	}{
		{
			"content type", `{"workspace_id":"` + gitWorkspaceOneID + `"}`,
			nil, http.StatusUnsupportedMediaType, "json_content_type_required",
		},
		{
			"unknown field",
			`{"workspace_id":"` + gitWorkspaceOneID + `","extra":true}`,
			jsonHeaders, http.StatusBadRequest, "invalid_collection_request",
		},
		{
			"duplicate",
			`{"workspace_id":"` + gitWorkspaceOneID + `","workspace_id":"` +
				gitWorkspaceTwoID + `"}`,
			jsonHeaders, http.StatusBadRequest, "invalid_collection_request",
		},
		{
			"multiple", `{"workspace_id":"` + gitWorkspaceOneID + `"} {}`,
			jsonHeaders, http.StatusBadRequest, "invalid_collection_request",
		},
		{
			"invalid id", `{"workspace_id":"bad"}`, jsonHeaders,
			http.StatusBadRequest, "invalid_git_workspace_id",
		},
	}
	for _, test := range strictCases {
		t.Run(test.name, func(t *testing.T) {
			response := serveGitWorkspaceRequest(
				t, mux, http.MethodPost, "/api/git-workspaces/cleanup",
				test.body, test.headers,
			)
			requireGitWorkspaceError(t, response, test.status, test.code)
		})
	}
	oversized := serveGitWorkspaceRequest(
		t, mux, http.MethodPost, "/api/git-workspaces/cleanup",
		`{"workspace_id":"`+strings.Repeat("x", collectionMutationMaxBytes)+`"}`,
		map[string]string{"Content-Type": "application/json"},
	)
	requireGitWorkspaceError(t, oversized, http.StatusRequestEntityTooLarge, "collection_request_too_large")

	invalidUTF8 := httptest.NewRequest(
		http.MethodPost, "/api/git-workspaces/cleanup",
		bytes.NewReader([]byte{
			'{', '"', 'w', 'o', 'r', 'k', 's', 'p', 'a', 'c', 'e', '_', 'i', 'd',
			'"', ':', '"', 0xff, '"', '}',
		}),
	)
	invalidUTF8.Header.Set("Content-Type", "application/json")
	invalidUTF8Response := httptest.NewRecorder()
	mux.ServeHTTP(invalidUTF8Response, invalidUTF8)
	requireGitWorkspaceError(t, invalidUTF8Response, http.StatusBadRequest, "invalid_collection_request")
}

func TestGitWorkspaceMutationStatesAndRacesUseSafeCodes(t *testing.T) {
	base := gitWorkspaceAPIStatsFixture()
	available, _ := findPublicGitWorkspace(base, gitWorkspaceOneID)
	locked, _ := findPublicGitWorkspace(base, gitWorkspaceTwoID)
	dropped, _ := findPublicGitWorkspace(base, gitWorkspaceThreeID)
	missing := base
	missing.Workspaces = []gitworkspace.WorkspaceInfo{locked, dropped}

	for _, test := range []struct {
		name   string
		id     string
		stats  gitworkspace.Stats
		status int
		code   string
	}{
		{"missing", "gw-dddddddddddd", base, http.StatusNotFound, "git_workspace_not_found"},
		{"locked", gitWorkspaceTwoID, base, http.StatusConflict, "git_workspace_locked"},
		{"dropped", gitWorkspaceThreeID, base, http.StatusConflict, "git_workspace_dropped"},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := &fakeGitWorkspaceManager{
				statsResults: []fakeGitWorkspaceStatsResult{{stats: test.stats}},
			}
			handler := NewHandler("")
			handler.loadGitWorkspaceManager = func() (gitWorkspaceManagerAPI, error) {
				return manager, nil
			}
			request := httptest.NewRequest(
				http.MethodPost, "/api/git-workspaces/cleanup",
				strings.NewReader(`{"workspace_id":"`+test.id+`"}`),
			)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.handleCleanupGitWorkspace(response, request)
			requireGitWorkspaceError(t, response, test.status, test.code)
			if manager.cleanupCalls != 0 {
				t.Fatal("preflight failure reached cleanup")
			}
		})
	}

	for _, test := range []struct {
		name       string
		after      fakeGitWorkspaceStatsResult
		wantCode   string
		wantStatus int
	}{
		{"became locked", fakeGitWorkspaceStatsResult{stats: base}, "git_workspace_locked", http.StatusConflict},
		{"became dropped", fakeGitWorkspaceStatsResult{stats: base}, "git_workspace_dropped", http.StatusConflict},
		{"became missing", fakeGitWorkspaceStatsResult{stats: missing}, "git_workspace_not_found", http.StatusNotFound},
		{
			"unclassified", fakeGitWorkspaceStatsResult{err: errors.New("PRIVATE-RACE")},
			"git_workspace_cleanup_failed", http.StatusConflict,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			after := test.after
			if test.name == "became locked" {
				after.stats.Workspaces = []gitworkspace.WorkspaceInfo{available, locked, dropped}
				for index := range after.stats.Workspaces {
					if after.stats.Workspaces[index].ID == gitWorkspaceOneID {
						after.stats.Workspaces[index].Status = "locked"
						after.stats.Workspaces[index].LockedBy = &gitworkspace.LockInfo{
							SessionKey: "RACE-SESSION-SECRET",
						}
					}
				}
			}
			if test.name == "became dropped" {
				after.stats.Workspaces = append([]gitworkspace.WorkspaceInfo(nil), base.Workspaces...)
				for index := range after.stats.Workspaces {
					if after.stats.Workspaces[index].ID == gitWorkspaceOneID {
						after.stats.Workspaces[index].Status = "dropped"
						now := time.Now().UTC()
						after.stats.Workspaces[index].DroppedAt = &now
					}
				}
			}
			manager := &fakeGitWorkspaceManager{
				statsResults: []fakeGitWorkspaceStatsResult{{stats: base}, after},
				cleanupErr:   errors.New("PRIVATE-OPERATION-ERROR SESSION-SECRET"),
			}
			handler := NewHandler("")
			handler.loadGitWorkspaceManager = func() (gitWorkspaceManagerAPI, error) {
				return manager, nil
			}
			request := httptest.NewRequest(
				http.MethodPost, "/api/git-workspaces/cleanup",
				strings.NewReader(`{"workspace_id":"`+gitWorkspaceOneID+`"}`),
			)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.handleCleanupGitWorkspace(response, request)
			requireGitWorkspaceError(t, response, test.wantStatus, test.wantCode)
			for _, canary := range []string{"PRIVATE-OPERATION-ERROR", "SESSION-SECRET", "RACE-SESSION-SECRET"} {
				if strings.Contains(response.Body.String(), canary) {
					t.Fatalf("race error leaked %q: %s", canary, response.Body.String())
				}
			}
		})
	}

	manager := &fakeGitWorkspaceManager{
		statsResults: []fakeGitWorkspaceStatsResult{{stats: base}, {err: errors.New("PRIVATE")}},
		dropErr:      errors.New("PRIVATE DROP"),
	}
	handler := NewHandler("")
	handler.loadGitWorkspaceManager = func() (gitWorkspaceManagerAPI, error) { return manager, nil }
	dropRequest := httptest.NewRequest(
		http.MethodDelete, "/api/git-workspaces/"+gitWorkspaceOneID, nil,
	)
	dropRequest.SetPathValue("id", gitWorkspaceOneID)
	dropResponse := httptest.NewRecorder()
	handler.handleDropGitWorkspace(dropResponse, dropRequest)
	requireGitWorkspaceError(t, dropResponse, http.StatusConflict, "git_workspace_drop_failed")

	manager = &fakeGitWorkspaceManager{reconcileErr: errors.New("PRIVATE RECONCILE")}
	handler.loadGitWorkspaceManager = func() (gitWorkspaceManagerAPI, error) { return manager, nil }
	reconcileResponse := httptest.NewRecorder()
	handler.handleReconcileGitWorkspaces(
		reconcileResponse,
		httptest.NewRequest(http.MethodPost, "/api/git-workspaces/reconcile", nil),
	)
	requireGitWorkspaceError(t, reconcileResponse, http.StatusInternalServerError, "git_workspace_reconcile_failed")
}

func TestGitWorkspaceSettingsAreScopedFencedAndRestartAware(t *testing.T) {
	resetGatewayTestState(t)
	configPath := gitWorkspaceAPIConfig(t, t.TempDir())
	cfg, revision, err := config.LoadConfigForUpdateSnapshot(configPath)
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
	gateway.bootConfigSignature = computeGatewayRuntimeSignature(cfg)
	gateway.cmd = process
	setGatewayRuntimeStatusLocked("running")
	gateway.mu.Unlock()

	handler := NewHandler(configPath)
	mux := http.NewServeMux()
	handler.registerGitWorkspaceRoutes(mux)
	get := serveGitWorkspaceRequest(
		t, mux, http.MethodGet, "/api/git-workspaces/settings", "", nil,
	)
	if get.Code != http.StatusOK {
		t.Fatalf("settings GET = %d %s", get.Code, get.Body.String())
	}
	var initial gitWorkspaceSettingsResponse
	decodeGitWorkspaceJSON(t, get, &initial)
	if initial.ConfigRevision != revision ||
		initial.Configured.MaxTotalSizeBytes != cfg.GitWorkspaces.MaxTotalSizeBytes ||
		initial.Effective.MaxTotalSizeBytes != cfg.GitWorkspaces.EffectiveMaxTotalSizeBytes() ||
		initial.Effects.GatewayEffect != "applied" {
		t.Fatalf("initial settings = %#v", initial)
	}

	nextSettings := gitWorkspaceSettings{
		MaxTotalSizeBytes:          9_000_000,
		IgnoredCleanupDelaySeconds: 1800,
		DropDelaySeconds:           7200,
	}
	body, err := json.Marshal(gitWorkspaceSettingsRequest{
		ExpectedConfigRevision: revision,
		Settings:               &nextSettings,
	})
	if err != nil {
		t.Fatal(err)
	}
	put := serveGitWorkspaceRequest(
		t, mux, http.MethodPut, "/api/git-workspaces/settings",
		string(body), map[string]string{"Content-Type": "application/json"},
	)
	if put.Code != http.StatusOK {
		t.Fatalf("settings PUT = %d %s", put.Code, put.Body.String())
	}
	var updated gitWorkspaceSettingsResponse
	decodeGitWorkspaceJSON(t, put, &updated)
	if updated.Configured != nextSettings || updated.Effective != nextSettings ||
		updated.ConfigRevision == revision ||
		updated.Effects.GatewayEffect != "restart_required" ||
		updated.Effects.LauncherEffect != "applied" ||
		updated.Effects.CatalogEffect != "applied" {
		t.Fatalf("updated settings = %#v", updated)
	}
	persisted, err := config.LoadConfig(configPath)
	if err != nil || persisted.GitWorkspaces.MaxTotalSizeBytes != 9_000_000 ||
		persisted.GitWorkspaces.IgnoredCleanupDelaySeconds != 1800 ||
		persisted.GitWorkspaces.DropDelaySeconds != 7200 {
		t.Fatalf("persisted settings = %#v, %v", persisted, err)
	}

	stale := serveGitWorkspaceRequest(
		t, mux, http.MethodPut, "/api/git-workspaces/settings",
		string(body), map[string]string{"Content-Type": "application/json"},
	)
	requireGitWorkspaceError(t, stale, http.StatusConflict, "config_revision_mismatch")

	for _, test := range []struct {
		name     string
		settings *gitWorkspaceSettings
	}{
		{"missing", nil},
		{"negative bytes", &gitWorkspaceSettings{MaxTotalSizeBytes: -1}},
		{"oversized bytes", &gitWorkspaceSettings{MaxTotalSizeBytes: gitWorkspaceMaximumSettingsBytes + 1}},
		{"negative cleanup", &gitWorkspaceSettings{IgnoredCleanupDelaySeconds: -1}},
		{"oversized cleanup", &gitWorkspaceSettings{IgnoredCleanupDelaySeconds: gitWorkspaceMaximumDelaySeconds + 1}},
		{"negative drop", &gitWorkspaceSettings{DropDelaySeconds: -1}},
		{"oversized drop", &gitWorkspaceSettings{DropDelaySeconds: gitWorkspaceMaximumDelaySeconds + 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(gitWorkspaceSettingsRequest{
				ExpectedConfigRevision: updated.ConfigRevision, Settings: test.settings,
			})
			if err != nil {
				t.Fatal(err)
			}
			response := serveGitWorkspaceRequest(
				t, mux, http.MethodPut, "/api/git-workspaces/settings",
				string(encoded), map[string]string{"Content-Type": "application/json"},
			)
			requireGitWorkspaceError(t, response, http.StatusUnprocessableEntity, "invalid_git_workspace_settings")
		})
	}

	crossOrigin := serveGitWorkspaceRequest(
		t, mux, http.MethodPut, "/api/git-workspaces/settings", string(body),
		map[string]string{
			"Content-Type": "application/json", "Sec-Fetch-Site": "cross-site",
		},
	)
	requireGitWorkspaceError(t, crossOrigin, http.StatusForbidden, "cross_origin_mutation")

	badQuery := serveGitWorkspaceRequest(
		t, mux, http.MethodGet, "/api/git-workspaces/settings?unexpected=1", "", nil,
	)
	requireGitWorkspaceError(t, badQuery, http.StatusBadRequest, "invalid_collection_request")
}

func TestGitWorkspaceSettingsFailureBoundariesStayStructured(t *testing.T) {
	malformedPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(malformedPath, []byte(`{"broken":`), 0o600); err != nil {
		t.Fatal(err)
	}
	missingHandler := NewHandler(malformedPath)
	get := httptest.NewRecorder()
	missingHandler.handleGetGitWorkspaceSettings(
		get, httptest.NewRequest(http.MethodGet, "/api/git-workspaces/settings", nil),
	)
	requireGitWorkspaceError(t, get, http.StatusInternalServerError, "git_workspace_settings_unavailable")

	configPath := gitWorkspaceAPIConfig(t, t.TempDir())
	_, revision, err := config.LoadConfigForUpdateSnapshot(configPath)
	if err != nil {
		t.Fatal(err)
	}
	settings := gitWorkspaceSettings{MaxTotalSizeBytes: 1234}
	body, err := json.Marshal(gitWorkspaceSettingsRequest{
		ExpectedConfigRevision: revision, Settings: &settings,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(configPath)
	handler.saveConfigIfRevision = func(string, *config.Config, string) (string, error) {
		return "", errors.New("PRIVATE SAVE FAILURE")
	}
	request := httptest.NewRequest(
		http.MethodPut, "/api/git-workspaces/settings", strings.NewReader(string(body)),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.handlePutGitWorkspaceSettings(response, request)
	requireGitWorkspaceError(t, response, http.StatusInternalServerError, "config_save_failed")
	if strings.Contains(response.Body.String(), "PRIVATE SAVE FAILURE") {
		t.Fatalf("save failure leaked: %s", response.Body.String())
	}

	handler.saveConfigIfRevision = func(string, *config.Config, string) (string, error) {
		return "", config.ErrConfigRevisionMismatch
	}
	request = httptest.NewRequest(
		http.MethodPut, "/api/git-workspaces/settings", strings.NewReader(string(body)),
	)
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.handlePutGitWorkspaceSettings(response, request)
	requireGitWorkspaceError(t, response, http.StatusConflict, "config_revision_mismatch")

	conflicting := httptest.NewRequest(
		http.MethodPut, "/api/git-workspaces/settings?revision=other",
		strings.NewReader(string(body)),
	)
	conflicting.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.handlePutGitWorkspaceSettings(response, conflicting)
	requireGitWorkspaceError(t, response, http.StatusBadRequest, "conflicting_config_revision")
}

func TestGitWorkspaceRoutesUseRealManagerAndHidePinnedInventory(t *testing.T) {
	ctx := context.Background()
	source := initAPISourceRepo(t)
	rootDir := filepath.Join(t.TempDir(), "git-workspaces")
	configPath := gitWorkspaceAPIConfig(t, rootDir)
	manager, err := gitworkspace.NewManager(gitworkspace.Options{
		RootDir: rootDir, MaxTotalSizeBytes: 1 << 30,
		IgnoredCleanupDelay: time.Hour, DropDelay: 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	public, err := manager.Acquire(ctx, gitworkspace.AcquireRequest{
		Repository: source, SessionKey: "api-session", AgentID: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.TrimSpace(runAPIGitOutput(t, source, "rev-parse", "HEAD"))
	pinned, err := manager.AcquirePinned(ctx, gitworkspace.PinnedAcquireRequest{
		Repository: source, SourceRef: "main", ExpectedCommit: commit,
		ReservationKey: "pr-development/PRIVATE-RESERVATION", AgentID: "controller",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ReleaseSession(ctx, gitworkspace.ReleaseRequest{SessionKey: "api-session"}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(public.Path, "ignored"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(public.Path, "ignored", "cache.bin"), []byte("cache"), 0o600); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	NewHandler(configPath).RegisterRoutes(mux)
	list := serveGitWorkspaceRequest(t, mux, http.MethodGet, "/api/git-workspaces", "", nil)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), public.ID) ||
		strings.Contains(list.Body.String(), pinned.ID) ||
		strings.Contains(list.Body.String(), "PRIVATE-RESERVATION") {
		t.Fatalf("real list = %d %s", list.Code, list.Body.String())
	}
	privateDetail := serveGitWorkspaceRequest(
		t, mux, http.MethodGet, "/api/git-workspaces/"+pinned.ID, "", nil,
	)
	requireGitWorkspaceError(t, privateDetail, http.StatusNotFound, "git_workspace_not_found")

	locked := manager
	_ = locked
	cleanup := serveGitWorkspaceRequest(
		t, mux, http.MethodPost, "/api/git-workspaces/cleanup",
		`{"workspace_id":"`+public.ID+`"}`,
		map[string]string{"Content-Type": "application/json"},
	)
	if cleanup.Code != http.StatusOK {
		t.Fatalf("real cleanup = %d %s", cleanup.Code, cleanup.Body.String())
	}
	var cleanupBody struct {
		Before int64 `json:"before_ignored_bytes"`
		After  int64 `json:"after_ignored_bytes"`
	}
	decodeGitWorkspaceJSON(t, cleanup, &cleanupBody)
	if cleanupBody.Before == 0 || cleanupBody.After != 0 {
		t.Fatalf("cleanup bytes = %#v", cleanupBody)
	}
	drop := serveGitWorkspaceRequest(
		t, mux, http.MethodDelete, "/api/git-workspaces/"+public.ID, "", nil,
	)
	if drop.Code != http.StatusOK {
		t.Fatalf("real drop = %d %s", drop.Code, drop.Body.String())
	}
	if _, err := os.Stat(public.Path); !os.IsNotExist(err) {
		t.Fatalf("dropped path stat = %v", err)
	}
	secondDrop := serveGitWorkspaceRequest(
		t, mux, http.MethodDelete, "/api/git-workspaces/"+public.ID, "", nil,
	)
	requireGitWorkspaceError(t, secondDrop, http.StatusConflict, "git_workspace_dropped")

	history := serveGitWorkspaceRequest(
		t, mux, http.MethodGet, "/api/git-workspaces/history", "", nil,
	)
	if history.Code != http.StatusOK || strings.Contains(history.Body.String(), public.Path) ||
		strings.Contains(history.Body.String(), source) ||
		strings.Contains(history.Body.String(), "api-session") ||
		strings.Contains(history.Body.String(), pinned.ID) ||
		strings.Contains(history.Body.String(), "PRIVATE-RESERVATION") {
		t.Fatalf("real history = %d %s", history.Code, history.Body.String())
	}
}

func TestGitWorkspaceHelpersValidateAndProjectBoundaries(t *testing.T) {
	t.Parallel()
	for _, id := range []string{
		gitWorkspaceOneID, "gw-aaaaaaaaaaaa-2", "gw-aaaaaaaaaaaa-9",
		"gw-aaaaaaaaaaaa-10", "gw-aaaaaaaaaaaa-100",
	} {
		if !validGitWorkspaceID(id) {
			t.Fatalf("validGitWorkspaceID(%q) = false", id)
		}
	}
	for _, id := range []string{
		"", "gw-", "GW-aaaaaaaaaaaa", "gw-bad/value", "gw-a",
		"gw-aaaaaaaaaaaa-0", "gw-aaaaaaaaaaaa-1", "gw-aaaaaaaaaaaa-02",
		"gw-aaaaaaaaaaaa-pinned", "gw-" + strings.Repeat("a", 129),
		"gw-" + string([]byte{0xff}),
	} {
		if validGitWorkspaceID(id) {
			t.Fatalf("validGitWorkspaceID(%q) = true", id)
		}
	}
	if !validGitWorkspaceHistoryID("abcdef123456") ||
		validGitWorkspaceHistoryID("ABCDEF123456") ||
		validGitWorkspaceHistoryID("abcdef12345") ||
		validGitWorkspaceHistoryID(string([]byte{0xff})) {
		t.Fatal("history ID validation boundary failed")
	}

	longRepository := strings.Repeat("é", collectionquery.MaxSuggestedValueBytes)
	label := safeGitWorkspaceRepositoryLabel(longRepository)
	if !utf8.ValidString(label) || len(label) > collectionquery.MaxSuggestedValueBytes {
		t.Fatalf("bounded repository label bytes/UTF-8 = %d/%v", len(label), utf8.ValidString(label))
	}
	if got := safeGitWorkspaceRepositoryLabel("/private/repos/alpha"); got != "local/alpha" {
		t.Fatalf("local repository label = %q", got)
	}
	if got := safeGitWorkspaceRepositoryLabel(""); got != "" {
		t.Fatalf("empty repository label = %q", got)
	}
	if got := safeGitWorkspaceHistoryValue(
		"  "+strings.Repeat("é", 200)+"  ",
		21,
	); !utf8.ValidString(got) || len(got) > 21 {
		t.Fatalf("history value = %q (%d)", got, len(got))
	}

	workspace := gitworkspace.WorkspaceInfo{
		ID: gitWorkspaceOneID, RepoID: "repo", RemoteURL: "/private/repo",
		CurrentBranch: "", PreservedBranch: "preserved", Ref: "main",
		Path: "/private/checkout", CreatedAt: time.Now().UTC(),
		LastWorkAt: time.Now().UTC(), LastCleanedAt: time.Now().UTC(),
		LockedBy: &gitworkspace.LockInfo{
			SessionKey: "SESSION-SECRET", AgentID: "main",
			LockedAt: time.Now().UTC(), HeartbeatAt: time.Now().UTC(),
		},
		Status: "locked",
	}
	if gitWorkspaceBranch(workspace) != "preserved" {
		t.Fatalf("branch fallback = %q", gitWorkspaceBranch(workspace))
	}
	workspace.PreservedBranch = ""
	workspace.Ref = ""
	if gitWorkspaceBranch(workspace) != "detached" {
		t.Fatalf("detached branch = %q", gitWorkspaceBranch(workspace))
	}
	detail := gitWorkspaceDetailFromInfo(workspace)
	encoded, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	if detail.LastWork == nil || detail.LastCleaned == nil || detail.LockedBy == nil ||
		strings.Contains(string(encoded), "SESSION-SECRET") ||
		strings.Contains(string(encoded), "session_key") {
		t.Fatalf("detail projection = %#v JSON=%s", detail, encoded)
	}
	workspace.LastWorkAt = time.Time{}
	workspace.LastCleanedAt = time.Time{}
	detail = gitWorkspaceDetailFromInfo(workspace)
	if detail.LastWork != nil || detail.LastCleaned != nil {
		t.Fatalf("zero detail times projected = %#v", detail)
	}

	stats := gitWorkspaceAPIStatsFixture()
	if _, found := findPublicGitWorkspace(stats, "gw-missing"); found {
		t.Fatal("missing public workspace found")
	}
	if !gitWorkspaceDropped(stats.Workspaces[2]) || gitWorkspaceDropped(stats.Workspaces[0]) {
		t.Fatal("dropped classification failed")
	}
	public := projectGitWorkspaceSummaries(stats.Workspaces)
	selected := gitWorkspaceSummariesForInfos(public, []gitworkspace.WorkspaceInfo{
		stats.Workspaces[0], {ID: "gw-missing"},
	})
	if len(selected) != 1 || selected[0].ID != gitWorkspaceOneID {
		t.Fatalf("selected summaries = %#v", selected)
	}
	if aggregate := gitWorkspaceAggregateFromStats(stats); aggregate.WorkspaceCount != 2 ||
		aggregate.MaxTotalSizeBytes != 1_000_000 {
		t.Fatalf("aggregate = %#v", aggregate)
	}

	if !validGitWorkspaceSettings(gitWorkspaceSettings{}) ||
		!validGitWorkspaceSettings(gitWorkspaceSettings{
			MaxTotalSizeBytes:          gitWorkspaceMaximumSettingsBytes,
			IgnoredCleanupDelaySeconds: gitWorkspaceMaximumDelaySeconds,
			DropDelaySeconds:           gitWorkspaceMaximumDelaySeconds,
		}) {
		t.Fatal("valid settings rejected")
	}
	response := gitWorkspaceSettingsResponseForConfig(
		config.DefaultConfig(), "revision", agentEffects{},
	)
	if response.ConfigRevision != "revision" || response.Effective.MaxTotalSizeBytes <= 0 {
		t.Fatalf("settings response = %#v", response)
	}
}

func TestGitWorkspaceHandlerAndProjectionCoverageBoundaries(t *testing.T) {
	stats := gitWorkspaceAPIStatsFixture()
	manager := &fakeGitWorkspaceManager{
		statsResults: []fakeGitWorkspaceStatsResult{{stats: stats}},
	}
	handler := NewHandler("")
	handler.loadGitWorkspaceManager = func() (gitWorkspaceManagerAPI, error) {
		return manager, nil
	}
	mux := http.NewServeMux()
	handler.registerGitWorkspaceRoutes(mux)

	first := serveGitWorkspaceRequest(
		t, mux, http.MethodGet, "/api/git-workspaces?limit=1", "", nil,
	)
	var firstPage struct {
		NextCursor string `json:"next_cursor"`
	}
	decodeGitWorkspaceJSON(t, first, &firstPage)
	if firstPage.NextCursor == "" {
		t.Fatal("workspace cursor is empty")
	}
	badCursor := serveGitWorkspaceRequest(
		t, mux, http.MethodGet,
		"/api/git-workspaces?query="+url.QueryEscape(`dirty = true ORDER BY updated DESC`)+
			"&limit=1&cursor="+url.QueryEscape(firstPage.NextCursor),
		"", nil,
	)
	requireGitWorkspaceError(t, badCursor, http.StatusBadRequest, "invalid_cursor")

	firstHistory := serveGitWorkspaceRequest(
		t, mux, http.MethodGet, "/api/git-workspaces/history?limit=1", "", nil,
	)
	var firstHistoryPage struct {
		NextCursor string `json:"next_cursor"`
	}
	decodeGitWorkspaceJSON(t, firstHistory, &firstHistoryPage)
	if firstHistoryPage.NextCursor == "" {
		t.Fatal("history cursor is empty")
	}
	badHistoryCursor := serveGitWorkspaceRequest(
		t, mux, http.MethodGet,
		"/api/git-workspaces/history?query="+url.QueryEscape(`action = "allocated" ORDER BY time DESC`)+
			"&limit=1&cursor="+url.QueryEscape(firstHistoryPage.NextCursor),
		"", nil,
	)
	requireGitWorkspaceError(t, badHistoryCursor, http.StatusBadRequest, "invalid_cursor")
	requireGitWorkspaceError(
		t,
		serveGitWorkspaceRequest(
			t, mux, http.MethodGet, "/api/git-workspaces/history?unexpected=1", "", nil,
		),
		http.StatusBadRequest,
		"invalid_collection_request",
	)
	requireGitWorkspaceError(
		t,
		serveGitWorkspaceRequest(
			t, mux, http.MethodPost, "/api/git-workspaces/reconcile?unexpected=1", "", nil,
		),
		http.StatusBadRequest,
		"invalid_collection_request",
	)
	requireGitWorkspaceError(
		t,
		serveGitWorkspaceRequest(
			t, mux, http.MethodPost, "/api/git-workspaces/cleanup?unexpected=1",
			`{"workspace_id":"`+gitWorkspaceOneID+`"}`,
			map[string]string{"Content-Type": "application/json"},
		),
		http.StatusBadRequest,
		"invalid_collection_request",
	)
	requireGitWorkspaceError(
		t,
		serveGitWorkspaceRequest(
			t, mux, http.MethodDelete, "/api/git-workspaces/"+gitWorkspaceOneID+"?unexpected=1", "", nil,
		),
		http.StatusBadRequest,
		"invalid_collection_request",
	)
	requireGitWorkspaceError(
		t,
		serveGitWorkspaceRequest(
			t, mux, http.MethodDelete, "/api/git-workspaces/bad", "", nil,
		),
		http.StatusBadRequest,
		"invalid_git_workspace_id",
	)

	manager.statsResults = []fakeGitWorkspaceStatsResult{{err: errors.New("private stats")}}
	manager.statsCalls = 0
	for _, target := range []string{
		"/api/git-workspaces/" + gitWorkspaceOneID,
		"/api/git-workspaces/history",
	} {
		requireGitWorkspaceError(
			t,
			serveGitWorkspaceRequest(t, mux, http.MethodGet, target, "", nil),
			http.StatusInternalServerError,
			"git_workspaces_unavailable",
		)
	}

	loaderErrorHandler := NewHandler("")
	loaderErrorHandler.loadGitWorkspaceManager = func() (gitWorkspaceManagerAPI, error) {
		return nil, errors.New("PRIVATE LOADER ERROR")
	}
	loaderMux := http.NewServeMux()
	loaderErrorHandler.registerGitWorkspaceRoutes(loaderMux)
	for _, test := range []struct {
		method  string
		target  string
		body    string
		headers map[string]string
	}{
		{http.MethodGet, "/api/git-workspaces", "", nil},
		{http.MethodPost, "/api/git-workspaces/reconcile", "", nil},
		{
			http.MethodPost, "/api/git-workspaces/cleanup",
			`{"workspace_id":"` + gitWorkspaceOneID + `"}`,
			map[string]string{"Content-Type": "application/json"},
		},
	} {
		response := serveGitWorkspaceRequest(
			t, loaderMux, test.method, test.target, test.body, test.headers,
		)
		requireGitWorkspaceError(
			t, response, http.StatusInternalServerError, "git_workspaces_unavailable",
		)
		if strings.Contains(response.Body.String(), "PRIVATE LOADER ERROR") {
			t.Fatalf("loader error leaked: %s", response.Body.String())
		}
	}

	statsErrorManager := &fakeGitWorkspaceManager{
		statsResults: []fakeGitWorkspaceStatsResult{{err: errors.New("private stats")}},
	}
	statsErrorHandler := NewHandler("")
	statsErrorHandler.loadGitWorkspaceManager = func() (gitWorkspaceManagerAPI, error) {
		return statsErrorManager, nil
	}
	request := httptest.NewRequest(
		http.MethodPost, "/api/git-workspaces/cleanup",
		strings.NewReader(`{"workspace_id":"`+gitWorkspaceOneID+`"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	statsErrorHandler.handleCleanupGitWorkspace(response, request)
	requireGitWorkspaceError(
		t, response, http.StatusInternalServerError, "git_workspaces_unavailable",
	)

	lockedManager := &fakeGitWorkspaceManager{
		statsResults: []fakeGitWorkspaceStatsResult{{stats: gitWorkspaceAPIStatsFixture()}},
	}
	lockedHandler := NewHandler("")
	lockedHandler.loadGitWorkspaceManager = func() (gitWorkspaceManagerAPI, error) {
		return lockedManager, nil
	}
	dropLockedRequest := httptest.NewRequest(
		http.MethodDelete, "/api/git-workspaces/"+gitWorkspaceTwoID, nil,
	)
	dropLockedRequest.SetPathValue("id", gitWorkspaceTwoID)
	dropLockedResponse := httptest.NewRecorder()
	lockedHandler.handleDropGitWorkspace(dropLockedResponse, dropLockedRequest)
	requireGitWorkspaceError(
		t, dropLockedResponse, http.StatusConflict, "git_workspace_locked",
	)

	malformedConfig := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(malformedConfig, []byte(`{"broken":`), 0o600); err != nil {
		t.Fatal(err)
	}
	malformedHandler := NewHandler(malformedConfig)
	if _, err := malformedHandler.gitWorkspaceManager(); err == nil {
		t.Fatal("malformed config manager error = nil")
	}
	validSettings := gitWorkspaceSettings{MaxTotalSizeBytes: 1}
	settingsBody, err := json.Marshal(gitWorkspaceSettingsRequest{
		ExpectedConfigRevision: "revision", Settings: &validSettings,
	})
	if err != nil {
		t.Fatal(err)
	}
	settingsRequest := httptest.NewRequest(
		http.MethodPut, "/api/git-workspaces/settings",
		strings.NewReader(string(settingsBody)),
	)
	settingsRequest.Header.Set("Content-Type", "application/json")
	settingsResponse := httptest.NewRecorder()
	malformedHandler.handlePutGitWorkspaceSettings(settingsResponse, settingsRequest)
	requireGitWorkspaceError(
		t, settingsResponse, http.StatusInternalServerError,
		"git_workspace_settings_unavailable",
	)

	validConfigHandler := NewHandler(gitWorkspaceAPIConfig(t, t.TempDir()))
	for _, test := range []struct {
		target  string
		body    string
		headers map[string]string
	}{
		{
			"/api/git-workspaces/settings?unexpected=1", string(settingsBody),
			map[string]string{"Content-Type": "application/json"},
		},
		{
			"/api/git-workspaces/settings", `{"broken":true}`,
			map[string]string{"Content-Type": "application/json"},
		},
	} {
		request := httptest.NewRequest(http.MethodPut, test.target, strings.NewReader(test.body))
		for key, value := range test.headers {
			request.Header.Set(key, value)
		}
		response := httptest.NewRecorder()
		validConfigHandler.handlePutGitWorkspaceSettings(response, request)
		requireGitWorkspaceError(
			t, response, http.StatusBadRequest, "invalid_collection_request",
		)
	}

	projectionStats := gitworkspace.Stats{
		Workspaces: []gitworkspace.WorkspaceInfo{{
			ID: gitWorkspaceOneID, RepoID: "repo-one", RemoteURL: "/private/one",
		}},
		Repositories: []gitworkspace.RepositoryInfo{
			{ID: "repo-one", RemoteURL: "/private/one"},
			{ID: "repo-orphan", RemoteURL: "git@github.com:acme/orphan.git"},
		},
		History: []gitworkspace.HistoryEntry{
			{
				ID: "444444444444", Action: "repository_seen",
				RepoID: "repo-orphan", Time: time.Now().UTC(),
			},
			{
				ID: "555555555555", Action: "workspace_seen",
				WorkspaceID: gitWorkspaceOneID, Time: time.Now().UTC(),
			},
		},
	}
	projectedHistory := projectGitWorkspaceHistory(projectionStats)
	if len(projectedHistory) != 2 ||
		projectedHistory[0].Repository != "git@github.com:acme/orphan.git" ||
		projectedHistory[1].Repository != "local/one" {
		t.Fatalf("projection coverage history = %#v", projectedHistory)
	}
	if got := safeGitWorkspaceRepositoryLabel(string(filepath.Separator)); got != "local repository" {
		t.Fatalf("root repository label = %q", got)
	}
	oddUTF8 := strings.Repeat("a", collectionquery.MaxSuggestedValueBytes-1) + "é"
	if got := safeGitWorkspaceRepositoryLabel(oddUTF8); !utf8.ValidString(got) ||
		len(got) != collectionquery.MaxSuggestedValueBytes-1 {
		t.Fatalf("odd UTF-8 label = %q (%d)", got, len(got))
	}
	for _, value := range []int64{-1, 0, gitWorkspaceMaximumSettingsBytes + 1} {
		got := safeGitWorkspaceByteCount(value)
		if got < 0 || got > gitWorkspaceMaximumSettingsBytes {
			t.Fatalf("safe byte count(%d) = %d", value, got)
		}
	}

	successWriter := httptest.NewRecorder()
	writeJSON(successWriter, map[string]string{"status": "ok"})
	if !strings.Contains(successWriter.Body.String(), `"status":"ok"`) {
		t.Fatalf("writeJSON success = %s", successWriter.Body.String())
	}
	failureWriter := &failingGitWorkspaceResponseWriter{}
	writeJSON(failureWriter, map[string]string{"status": "fail"})
}

func TestConfigSignatureTracksEffectiveGitWorkspaceRuntime(t *testing.T) {
	t.Parallel()
	newConfig := func() *config.Config {
		cfg := config.DefaultConfig()
		cfg.Agents.Defaults.Workspace = "/stable/workspace"
		return cfg
	}
	baseline := computeConfigSignature(newConfig())
	for _, test := range []struct {
		name   string
		mutate func(*config.Config)
	}{
		{"root", func(cfg *config.Config) { cfg.GitWorkspaces.RootDir = "/other/git-root" }},
		{"max size", func(cfg *config.Config) { cfg.GitWorkspaces.MaxTotalSizeBytes++ }},
		{"cleanup delay", func(cfg *config.Config) { cfg.GitWorkspaces.IgnoredCleanupDelaySeconds++ }},
		{"drop delay", func(cfg *config.Config) { cfg.GitWorkspaces.DropDelaySeconds++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := newConfig()
			test.mutate(cfg)
			if got := computeConfigSignature(cfg); got == baseline {
				t.Fatalf("%s did not change config signature", test.name)
			}
		})
	}
	explicit := newConfig()
	explicit.GitWorkspaces.MaxTotalSizeBytes = config.DefaultGitWorkspaceMaxTotalSizeBytes
	explicit.GitWorkspaces.IgnoredCleanupDelaySeconds = config.DefaultGitWorkspaceIgnoredCleanupDelaySecs
	explicit.GitWorkspaces.DropDelaySeconds = config.DefaultGitWorkspaceDropDelaySecs
	if got := computeConfigSignature(explicit); got != baseline {
		t.Fatalf("explicit effective defaults signature = %q, want %q", got, baseline)
	}
	if computeGitWorkspaceRuntimeSignature(nil) != "" {
		t.Fatal("nil git workspace signature was nonempty")
	}
	if repeated := computeConfigSignature(newConfig()); repeated != baseline {
		t.Fatalf("signature nondeterministic: %q != %q", repeated, baseline)
	}
}

func gitWorkspaceAPIStatsFixture() gitworkspace.Stats {
	base := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)
	droppedAt := base.Add(3 * time.Hour)
	workspaces := []gitworkspace.WorkspaceInfo{
		{
			ID: gitWorkspaceOneID, RepoID: "repo-alpha", RemoteURL: "/private/repos/alpha",
			UpstreamURL: "git@github.com:acme/alpha.git", Ref: "main",
			Path: "/private/checkouts/alpha", CurrentBranch: "main",
			SizeBytes: 100, IgnoredBytes: 10, CreatedAt: base,
			UpdatedAt: base.Add(time.Hour), Status: "available",
		},
		{
			ID: gitWorkspaceTwoID, RepoID: "repo-beta",
			RemoteURL: "git@github.com:acme/beta.git", Ref: "feature",
			Path: "/private/checkouts/beta", CurrentBranch: "feature",
			Dirty: true, SizeBytes: 200, IgnoredBytes: 20, CreatedAt: base,
			UpdatedAt: base.Add(2 * time.Hour), Status: "locked",
			LockedBy: &gitworkspace.LockInfo{
				SessionKey: "SESSION-SECRET", AgentID: "worker",
				LockedAt: base, HeartbeatAt: base.Add(time.Hour),
			},
		},
		{
			ID: gitWorkspaceThreeID, RepoID: "repo-gamma",
			RemoteURL: "/private/repos/gamma", Ref: "main",
			Path: "/private/checkouts/gamma", PreservedBranch: "preserved",
			CreatedAt: base, UpdatedAt: base.Add(3 * time.Hour),
			DroppedAt: &droppedAt, Status: "dropped",
		},
		{
			ID: "invalid-private-controller", RepoID: "private-repo",
			RemoteURL: "/private/controller", Path: "/private/controller-checkout",
			Status: "locked", UpdatedAt: base.Add(4 * time.Hour),
		},
	}
	return gitworkspace.Stats{
		RootDir: "/private/git-root", MaxTotalSizeBytes: 1_000_000,
		IgnoredCleanupDelaySeconds: 3600, DropDelaySeconds: 7200,
		TotalSizeBytes: 300, IgnoredBytes: 30, RepositoryCount: 3,
		WorkspaceCount: 2, LockedWorkspaceCount: 1, Workspaces: workspaces,
		Repositories: []gitworkspace.RepositoryInfo{
			{ID: "repo-alpha", RemoteURL: "/private/repos/alpha"},
			{ID: "repo-beta", RemoteURL: "git@github.com:acme/beta.git"},
			{ID: "repo-gamma", RemoteURL: "/private/repos/gamma"},
		},
		History: []gitworkspace.HistoryEntry{
			{
				ID: "111111111111", Time: base, Action: "allocated",
				RepoID: "repo-alpha", WorkspaceID: gitWorkspaceOneID,
				SessionKey: "SESSION-SECRET", AgentID: "main",
				Detail: "/private/checkouts/alpha DETAIL-SECRET",
			},
			{
				ID: "222222222222", Time: base.Add(time.Hour), Action: "cleaned_ignored",
				RepoID: "repo-beta", WorkspaceID: gitWorkspaceTwoID,
				SessionKey: "SESSION-SECRET", AgentID: "worker",
				Detail: "/private/checkouts/beta DETAIL-SECRET",
			},
			{ID: "INVALID", Time: base.Add(2 * time.Hour), Action: "invalid"},
			{
				ID: "333333333333", Time: base.Add(3 * time.Hour), Action: "private",
				RepoID: "private-repo", WorkspaceID: "invalid-private-controller",
				SessionKey: "PRIVATE-SESSION", Detail: "/private/controller-checkout",
			},
		},
	}
}

func gitWorkspaceAPIConfig(t *testing.T, rootDir string) string {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.GitWorkspaces.RootDir = rootDir
	cfg.GitWorkspaces.MaxTotalSizeBytes = 1 << 30
	cfg.GitWorkspaces.IgnoredCleanupDelaySeconds = 3600
	cfg.GitWorkspaces.DropDelaySeconds = 86400
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	return configPath
}

func serveGitWorkspaceRequest(
	t *testing.T,
	handler http.Handler,
	method string,
	target string,
	body string,
	headers map[string]string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeGitWorkspaceJSON(
	t *testing.T,
	response *httptest.ResponseRecorder,
	target any,
) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, response.Body.String())
	}
}

func requireGitWorkspaceError(
	t *testing.T,
	response *httptest.ResponseRecorder,
	status int,
	code string,
) {
	t.Helper()
	var body struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	decodeGitWorkspaceJSON(t, response, &body)
	if response.Code != status || body.Code != code || body.Message == "" ||
		len(response.Body.Bytes()) > 1024 {
		t.Fatalf("error = %d %s, want %d/%s", response.Code, response.Body.String(), status, code)
	}
}

func assertGitWorkspaceSchemaField(
	t *testing.T,
	schema collectionquery.Schema,
	name collectionquery.Field,
	fieldType collectionquery.FieldType,
) {
	t.Helper()
	for _, field := range schema.Fields {
		if field.Name == name {
			if field.Type != fieldType {
				t.Fatalf("schema %s type = %s, want %s", name, field.Type, fieldType)
			}
			return
		}
	}
	t.Fatalf("schema missing field %s", name)
}

func initAPISourceRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runAPIGit(t, dir, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignored/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runAPIGit(t, dir, "add", ".")
	runAPIGit(t, dir, "commit", "-m", "initial")
	return dir
}

func runAPIGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	_ = runAPIGitOutput(t, dir, args...)
}

func runAPIGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=PicoClaw",
		"GIT_AUTHOR_EMAIL=picoclaw@localhost",
		"GIT_COMMITTER_NAME=PicoClaw",
		"GIT_COMMITTER_EMAIL=picoclaw@localhost",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(output))
	}
	return string(output)
}
