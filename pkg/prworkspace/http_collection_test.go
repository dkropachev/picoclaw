package prworkspace

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
)

func TestDevelopmentWorkspaceCollectionExhaustsInternalPagesAndPagesAtTwoHundred(t *testing.T) {
	handler, _ := seededDevelopmentWorkspaceCollectionHandler(t, 205)
	first := requestDevelopmentWorkspaceCollection(t, handler, url.Values{"limit": {"200"}})
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	page := decodeDevelopmentWorkspaceCollection(t, first)
	assert.Equal(t, 205, page.Total)
	require.Len(t, page.Workspaces, 200)
	assert.NotEmpty(t, page.NextCursor)
	assert.Equal(t, developmentWorkspaceCollectionID(205), page.Workspaces[0].ID)
	assert.Equal(t, developmentWorkspaceCollectionID(6), page.Workspaces[199].ID)
	assert.Equal(t, "ALL ORDER BY updated DESC", page.CanonicalQuery)

	second := requestDevelopmentWorkspaceCollection(t, handler, url.Values{
		"limit": {"200"}, "cursor": {page.NextCursor},
	})
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	next := decodeDevelopmentWorkspaceCollection(t, second)
	assert.Equal(t, 205, next.Total)
	require.Len(t, next.Workspaces, 5)
	assert.Empty(t, next.NextCursor)
	assert.Equal(t, developmentWorkspaceCollectionID(5), next.Workspaces[0].ID)
	assert.Equal(t, developmentWorkspaceCollectionID(1), next.Workspaces[4].ID)

	seen := make(map[string]struct{}, 205)
	for _, workspace := range append(page.Workspaces, next.Workspaces...) {
		_, duplicate := seen[workspace.ID]
		assert.False(t, duplicate, workspace.ID)
		seen[workspace.ID] = struct{}{}
	}
	assert.Len(t, seen, 205)
}

func TestDevelopmentWorkspaceCollectionSafeProjectionAndDirectRead(t *testing.T) {
	handler, _ := seededDevelopmentWorkspaceCollectionHandler(t, 1)
	response := requestDevelopmentWorkspaceCollection(t, handler, nil)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())

	var raw struct {
		Workspaces []map[string]json.RawMessage `json:"workspaces"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &raw))
	require.Len(t, raw.Workspaces, 1)
	assert.ElementsMatch(t, []string{
		"id", "intent", "source", "repository", "title", "phase", "execution_state", "created", "updated",
	}, mapKeys(raw.Workspaces[0]))
	for _, forbidden := range []string{
		"source_id", "source_kind", "source_number", "provider", "provider_origin", "repository_id",
		"pull_request_id", "pull_number", "provider_head_sha", "version", "provider_snapshot",
		"PRIVATE_SOURCE_BODY", "PRIVATE_HEAD_SHA", "PRIVATE_AUTHOR_ID",
	} {
		assert.NotContains(t, response.Body.String(), forbidden)
	}

	id := developmentWorkspaceCollectionID(1)
	direct := developmentHTTPRequest(t, handler, http.MethodGet, RuntimeRoutePrefix+"/"+id, "")
	require.Equal(t, http.StatusOK, direct.Code, direct.Body.String())
	assert.Contains(t, direct.Body.String(), `"provider_snapshot"`)
	assert.Contains(t, direct.Body.String(), `"version":1`)
	queriedDirect := developmentHTTPRequest(
		t,
		handler,
		http.MethodGet,
		RuntimeRoutePrefix+"/"+id+"?anything=value",
		"",
	)
	assertDevelopmentWorkspaceCollectionError(t, queriedDirect, http.StatusBadRequest, "invalid_query")

	uppercase := developmentHTTPRequest(
		t,
		handler,
		http.MethodGet,
		RuntimeRoutePrefix+"/devw_"+strings.Repeat("A", 32),
		"",
	)
	assertDevelopmentWorkspaceCollectionError(t, uppercase, http.StatusBadRequest, "invalid_workspace_id")
	missing := developmentHTTPRequest(
		t,
		handler,
		http.MethodGet,
		RuntimeRoutePrefix+"/devw_"+strings.Repeat("f", 32),
		"",
	)
	assertDevelopmentWorkspaceCollectionError(t, missing, http.StatusNotFound, "not_found")
}

func TestDevelopmentWorkspaceCollectionSchemaQueriesAndSuggestions(t *testing.T) {
	handler, _ := seededDevelopmentWorkspaceCollectionHandler(t, 18)
	page := decodeDevelopmentWorkspaceCollection(
		t,
		requestDevelopmentWorkspaceCollection(t, handler, nil),
	)
	require.Len(t, page.QuerySchema.Fields, 9)
	fields := make(map[collectionquery.Field]collectionquery.FieldSchema, len(page.QuerySchema.Fields))
	for _, field := range page.QuerySchema.Fields {
		fields[field.Name] = field
	}
	for _, field := range []collectionquery.Field{
		"id", "intent", "source", "repository", "title", "phase", "execution_state", "created", "updated",
	} {
		declaration, ok := fields[field]
		assert.True(t, ok, field)
		assert.True(t, declaration.Sortable, field)
	}
	assert.Equal(t, collectionquery.TypeEnum, fields["intent"].Type)
	assert.Equal(t, collectionquery.TypeEnum, fields["source"].Type)
	assert.Equal(t, collectionquery.TypeTimestamp, fields["created"].Type)
	assert.Equal(t, []collectionquery.SortField{{
		Field: "updated", Direction: collectionquery.Descending,
	}}, page.QuerySchema.DefaultOrder)
	assert.Contains(t, fields["repository"].SuggestedValues, "acme/repo-00")
	assert.Contains(t, fields["title"].SuggestedValues, "Feature brief")
	assert.Contains(t, fields["id"].SuggestedValues, developmentWorkspaceCollectionID(1))

	createdFloor := developmentWorkspaceCollectionTime(10).Format(time.RFC3339)
	queries := []string{
		`id ~ "0001"`,
		`intent = implement_feature`,
		`source = issue`,
		`repository ~ "repo-01"`,
		`title ~ "issue"`,
		`phase = charter`,
		`execution_state = waiting_user`,
		`created >= "` + createdFloor + `"`,
		`updated <= "` + developmentWorkspaceCollectionTime(12).Format(time.RFC3339) + `"`,
	}
	for _, query := range queries {
		t.Run(query, func(t *testing.T) {
			filtered := decodeDevelopmentWorkspaceCollection(t, requestDevelopmentWorkspaceCollection(
				t,
				handler,
				url.Values{"query": {query}, "limit": {"200"}},
			))
			assert.NotZero(t, filtered.Total)
			assert.Len(t, filtered.Workspaces, filtered.Total)
		})
	}

	orderedQuery := `intent = implement_feature ORDER BY repository ASC, title DESC`
	ordered := decodeDevelopmentWorkspaceCollection(t, requestDevelopmentWorkspaceCollection(
		t,
		handler,
		url.Values{"query": {orderedQuery}, "limit": {"200"}},
	))
	for index := 1; index < len(ordered.Workspaces); index++ {
		previous, current := ordered.Workspaces[index-1], ordered.Workspaces[index]
		if previous.Repository == current.Repository {
			assert.GreaterOrEqual(t, previous.Title, current.Title)
		} else {
			assert.LessOrEqual(t, previous.Repository, current.Repository)
		}
	}
}

func TestDevelopmentWorkspaceCollectionCursorBindingAndBoundedErrors(t *testing.T) {
	handler, _ := seededDevelopmentWorkspaceCollectionHandler(t, 12)
	query := `source = issue ORDER BY created ASC`
	first := decodeDevelopmentWorkspaceCollection(t, requestDevelopmentWorkspaceCollection(
		t,
		handler,
		url.Values{"query": {query}, "limit": {"2"}},
	))
	require.NotEmpty(t, first.NextCursor)

	mismatch := requestDevelopmentWorkspaceCollection(t, handler, url.Values{
		"query": {`source = brief ORDER BY created ASC`}, "cursor": {first.NextCursor},
	})
	assertDevelopmentWorkspaceCollectionError(t, mismatch, http.StatusBadRequest, "invalid_cursor")
	invalid := requestDevelopmentWorkspaceCollection(t, handler, url.Values{"cursor": {"not-a-cursor"}})
	assertDevelopmentWorkspaceCollectionError(t, invalid, http.StatusBadRequest, "invalid_cursor")

	utf8Query := `title = "é" AND unknown = value`
	invalidQuery := requestDevelopmentWorkspaceCollection(t, handler, url.Values{"query": {utf8Query}})
	var queryError struct {
		Code     string `json:"code"`
		Message  string `json:"message"`
		Position int    `json:"position"`
	}
	require.NoError(t, json.Unmarshal(invalidQuery.Body.Bytes(), &queryError))
	assert.Equal(t, http.StatusBadRequest, invalidQuery.Code)
	assert.Equal(t, "invalid_query", queryError.Code)
	assert.Equal(t, strings.Index(utf8Query, "unknown"), queryError.Position)
	assert.LessOrEqual(t, len(queryError.Message), collectionquery.MaxQueryErrorMessageLen)
	assert.True(t, utf8.ValidString(queryError.Message))

	oversized := requestDevelopmentWorkspaceCollection(t, handler, url.Values{
		"query": {strings.Repeat("x", collectionquery.MaxQueryBytes+1)},
	})
	require.NoError(t, json.Unmarshal(oversized.Body.Bytes(), &queryError))
	assert.Equal(t, "invalid_query", queryError.Code)
	assert.Equal(t, collectionquery.MaxQueryBytes, queryError.Position)
	assert.LessOrEqual(t, len(queryError.Message), collectionquery.MaxQueryErrorMessageLen)

	invalidUTF8 := developmentHTTPRequest(
		t,
		handler,
		http.MethodGet,
		RuntimeRoutePrefix+"?query=%FF",
		"",
	)
	require.NoError(t, json.Unmarshal(invalidUTF8.Body.Bytes(), &queryError))
	assert.Equal(t, "invalid_query", queryError.Code)
	assert.Equal(t, 0, queryError.Position)
	assert.True(t, utf8.ValidString(queryError.Message))
}

func TestDevelopmentWorkspaceCollectionHardCutParametersAndLimits(t *testing.T) {
	handler, _ := seededDevelopmentWorkspaceCollectionHandler(t, 1)
	tests := []struct {
		name, rawQuery, code string
	}{
		{"legacy repository", "repository=acme%2Frepo", "invalid_collection_request"},
		{"legacy phase", "phase=charter", "invalid_collection_request"},
		{"unknown", "extra=value", "invalid_collection_request"},
		{"duplicate query", "query=ALL&query=ALL", "invalid_collection_request"},
		{"malformed encoding", "query=%zz", "invalid_collection_request"},
		{
			"transport over limit",
			"query=" + strings.Repeat("x", developmentWorkspaceMaxRawQueryBytes),
			"invalid_collection_request",
		},
		{"zero limit", "limit=0", "invalid_page_limit"},
		{"over limit", "limit=201", "invalid_page_limit"},
		{"nonnumeric limit", "limit=many", "invalid_page_limit"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := developmentHTTPRequest(
				t,
				handler,
				http.MethodGet,
				RuntimeRoutePrefix+"?"+test.rawQuery,
				"",
			)
			assertDevelopmentWorkspaceCollectionError(t, response, http.StatusBadRequest, test.code)
		})
	}
	accepted := requestDevelopmentWorkspaceCollection(t, handler, url.Values{"limit": {"200"}})
	assert.Equal(t, http.StatusOK, accepted.Code, accepted.Body.String())
}

func TestDevelopmentWorkspaceCollectionRejectsDuplicateIDsAndNonProgress(t *testing.T) {
	firstAggregate := developmentWorkspaceCollectionAggregate(1)
	secondAggregate := developmentWorkspaceCollectionAggregate(2)
	thirdAggregate := developmentWorkspaceCollectionAggregate(3)
	backing := NewMemoryStore()
	for _, aggregate := range []Aggregate{firstAggregate, secondAggregate, thirdAggregate} {
		backing.aggregates[aggregate.Workspace.ID] = aggregate
	}
	store := &developmentWorkspaceCollectionPagingStore{
		MemoryStore: backing,
		pages: []Page{
			{
				Workspaces: []Workspace{thirdAggregate.Workspace, secondAggregate.Workspace},
				Next: &WorkspaceCursor{
					UpdatedAt: secondAggregate.Workspace.UpdatedAt,
					ID:        secondAggregate.Workspace.ID,
				},
			},
			{Workspaces: []Workspace{secondAggregate.Workspace, firstAggregate.Workspace}},
		},
	}
	handler := developmentWorkspaceCollectionHandlerForStore(t, store)
	response := requestDevelopmentWorkspaceCollection(t, handler, nil)
	assertDevelopmentWorkspaceCollectionError(
		t,
		response,
		http.StatusServiceUnavailable,
		"development_workspaces_unavailable",
	)
	assert.Equal(t, 2, store.listCalls)

	stalled := &developmentWorkspaceCollectionPagingStore{
		MemoryStore: backing,
		pages: []Page{
			{
				Workspaces: []Workspace{thirdAggregate.Workspace, secondAggregate.Workspace},
				Next: &WorkspaceCursor{
					UpdatedAt: secondAggregate.Workspace.UpdatedAt,
					ID:        secondAggregate.Workspace.ID,
				},
			},
			{
				Workspaces: []Workspace{secondAggregate.Workspace},
				Next: &WorkspaceCursor{
					UpdatedAt: secondAggregate.Workspace.UpdatedAt,
					ID:        secondAggregate.Workspace.ID,
				},
			},
		},
	}
	response = requestDevelopmentWorkspaceCollection(
		t,
		developmentWorkspaceCollectionHandlerForStore(t, stalled),
		nil,
	)
	assertDevelopmentWorkspaceCollectionError(
		t,
		response,
		http.StatusServiceUnavailable,
		"development_workspaces_unavailable",
	)
}

func TestDevelopmentWorkspaceCollectionScanCeilings(t *testing.T) {
	fullPages := developmentWorkspaceMaxCollectionItems / developmentWorkspaceInternalPageSize
	acceptedStore := &developmentWorkspaceCollectionCeilingStore{
		MemoryStore:  NewMemoryStore(),
		itemsPerPage: developmentWorkspaceInternalPageSize,
		totalPages:   fullPages,
	}
	acceptedHandler := developmentWorkspaceCollectionHandlerForStore(t, acceptedStore)
	items, err := acceptedHandler.loadDevelopmentWorkspaceCollection(t.Context())
	require.NoError(t, err)
	assert.Len(t, items, developmentWorkspaceMaxCollectionItems)
	assert.Equal(t, fullPages, acceptedStore.listCalls)

	itemCeilingStore := &developmentWorkspaceCollectionCeilingStore{
		MemoryStore:  NewMemoryStore(),
		itemsPerPage: developmentWorkspaceInternalPageSize,
		totalPages:   fullPages + 1,
	}
	itemCeilingHandler := developmentWorkspaceCollectionHandlerForStore(t, itemCeilingStore)
	_, err = itemCeilingHandler.loadDevelopmentWorkspaceCollection(t.Context())
	assert.ErrorIs(t, err, errDevelopmentWorkspaceCollection)
	assert.Equal(t, fullPages, itemCeilingStore.listCalls)

	pageCeilingStore := &developmentWorkspaceCollectionCeilingStore{
		MemoryStore:  NewMemoryStore(),
		itemsPerPage: 1,
		totalPages:   developmentWorkspaceMaxInternalPages + 1,
	}
	pageCeilingHandler := developmentWorkspaceCollectionHandlerForStore(t, pageCeilingStore)
	_, err = pageCeilingHandler.loadDevelopmentWorkspaceCollection(t.Context())
	assert.ErrorIs(t, err, errDevelopmentWorkspaceCollection)
	assert.Equal(t, developmentWorkspaceMaxInternalPages, pageCeilingStore.listCalls)
}

func TestDevelopmentWorkspaceCollectionHelperBoundaries(t *testing.T) {
	assert.Panics(t, func() {
		mustDevelopmentWorkspaceCollectionSchema(nil, nil)
	})
	recorder := httptest.NewRecorder()
	_, ok := parseDevelopmentWorkspaceCollectionRequest(recorder, nil)
	assert.False(t, ok)
	assertDevelopmentWorkspaceCollectionError(
		t,
		recorder,
		http.StatusBadRequest,
		"invalid_collection_request",
	)

	_, err := (*HTTPHandler)(nil).loadDevelopmentWorkspaceCollection(t.Context())
	assert.ErrorIs(t, err, errDevelopmentWorkspaceCollection)
	for name, pages := range map[string][]Page{
		"list error":      nil,
		"oversized page":  {{Workspaces: make([]Workspace, developmentWorkspaceInternalPageSize+1)}},
		"invalid ID":      {{Workspaces: []Workspace{{ID: "DEVW_" + strings.Repeat("1", 32)}}}},
		"invalid summary": {{Workspaces: []Workspace{{ID: developmentWorkspaceCollectionID(1)}}}},
	} {
		t.Run(name, func(t *testing.T) {
			store := &developmentWorkspaceCollectionPagingStore{MemoryStore: NewMemoryStore(), pages: pages}
			handler := developmentWorkspaceCollectionHandlerForStore(t, store)
			_, loadErr := handler.loadDevelopmentWorkspaceCollection(t.Context())
			assert.ErrorIs(t, loadErr, errDevelopmentWorkspaceCollection)
		})
	}

	second := developmentWorkspaceCollectionAggregate(2).Workspace
	third := developmentWorkspaceCollectionAggregate(3).Workspace
	assert.False(t, validDevelopmentWorkspaceStoreCursor(nil, nil, WorkspaceCursor{}))
	assert.False(t, validDevelopmentWorkspaceStoreCursor(
		[]Workspace{third},
		nil,
		WorkspaceCursor{UpdatedAt: third.UpdatedAt, ID: second.ID},
	))
	previous := WorkspaceCursor{UpdatedAt: third.UpdatedAt, ID: third.ID}
	assert.True(t, validDevelopmentWorkspaceStoreCursor(
		[]Workspace{second},
		&previous,
		WorkspaceCursor{UpdatedAt: second.UpdatedAt, ID: second.ID},
	))

	invalid := developmentWorkspaceCollectionAggregate(1).Workspace
	invalid.Intent = IntentPickupPR
	_, err = projectDevelopmentWorkspaceCollectionSummary(invalid)
	assert.ErrorIs(t, err, errDevelopmentWorkspaceCollection)
	assert.Equal(t, "fallback", developmentWorkspaceCollectionText("", "fallback"))
	assert.Equal(t, "Issue #7", developmentWorkspaceFallbackTitle(Workspace{
		SourceKind: SourceIssue, SourceNumber: 7,
	}, "repo"))
	assert.Equal(t, "repo", developmentWorkspaceFallbackTitle(Workspace{SourceKind: SourceIssue}, "repo"))
	assert.Equal(t, "Pull request #8", developmentWorkspaceFallbackTitle(Workspace{
		SourceKind: SourcePullRequest, SourceNumber: 8,
	}, "repo"))
	assert.Equal(t, "repo", developmentWorkspaceFallbackTitle(Workspace{}, "repo"))
	_, resolved := resolveDevelopmentWorkspaceCollectionField(
		developmentWorkspaceCollectionSummary{},
		"unknown",
	)
	assert.False(t, resolved)

	suggestions := boundedDevelopmentWorkspaceSuggestions([]string{
		"", strings.Repeat("x", collectionquery.MaxSuggestedValueBytes+1), "bad\nvalue", "\xff", " Good ", "good",
	})
	assert.Equal(t, []string{"Good"}, suggestions)
	recorder = httptest.NewRecorder()
	writeDevelopmentWorkspaceCollectionError(
		recorder,
		http.StatusBadRequest,
		"invalid_collection_request",
		"bad\nmessage",
		4,
	)
	assert.NotContains(t, recorder.Body.String(), "\\n")
	assert.NotContains(t, recorder.Body.String(), `"position"`)
}

type developmentWorkspaceCollectionPagingStore struct {
	*MemoryStore
	pages     []Page
	listCalls int
}

type developmentWorkspaceCollectionCeilingStore struct {
	*MemoryStore
	itemsPerPage int
	totalPages   int
	listCalls    int
}

func (store *developmentWorkspaceCollectionCeilingStore) List(
	ctx context.Context,
	_ ListFilter,
) (Page, error) {
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	if store.listCalls >= store.totalPages {
		return Page{}, ErrInvalid
	}
	pageIndex := store.listCalls
	store.listCalls++
	workspaces := make([]Workspace, store.itemsPerPage)
	for index := range workspaces {
		ordinal := pageIndex*store.itemsPerPage + index + 1
		updated := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC).
			Add(-time.Duration(ordinal) * time.Second)
		workspaces[index] = Workspace{
			ID:             developmentWorkspaceCollectionID(ordinal),
			Intent:         IntentImplementFeature,
			SourceKind:     SourceBrief,
			Repository:     "acme/repository",
			Phase:          PhaseCharter,
			ExecutionState: ExecutionWaitingUser,
			CreatedAt:      updated.Add(-time.Minute),
			UpdatedAt:      updated,
		}
	}
	page := Page{Workspaces: workspaces}
	if store.listCalls < store.totalPages {
		last := workspaces[len(workspaces)-1]
		page.Next = &WorkspaceCursor{UpdatedAt: last.UpdatedAt, ID: last.ID}
	}
	return page, nil
}

func (store *developmentWorkspaceCollectionPagingStore) List(
	ctx context.Context,
	_ ListFilter,
) (Page, error) {
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	if store.listCalls >= len(store.pages) {
		return Page{}, ErrInvalid
	}
	page := store.pages[store.listCalls]
	store.listCalls++
	return page, nil
}

func seededDevelopmentWorkspaceCollectionHandler(
	t *testing.T,
	count int,
) (*HTTPHandler, *MemoryStore) {
	t.Helper()
	store := NewMemoryStore()
	for index := 1; index <= count; index++ {
		aggregate := developmentWorkspaceCollectionAggregate(index)
		store.aggregates[aggregate.Workspace.ID] = aggregate
	}
	return developmentWorkspaceCollectionHandlerForStore(t, store), store
}

func developmentWorkspaceCollectionHandlerForStore(t *testing.T, store Store) *HTTPHandler {
	t.Helper()
	service, err := NewService(ServiceConfig{Store: store, Provider: developmentIntakeResolver{}})
	require.NoError(t, err)
	handler, err := NewHTTPHandler(HTTPConfig{Service: service})
	require.NoError(t, err)
	return handler
}

func developmentWorkspaceCollectionAggregate(index int) Aggregate {
	intent := IntentImplementFeature
	source := SourceBrief
	sourceNumber := int64(0)
	if index%3 == 0 {
		source, sourceNumber = SourceIssue, int64(index)
	} else if index%3 == 2 {
		intent, source, sourceNumber = IntentPickupPR, SourcePullRequest, int64(index)
	}
	repository := fmt.Sprintf("acme/repo-%02d", index%5)
	id := developmentWorkspaceCollectionID(index)
	created := developmentWorkspaceCollectionTime(index)
	workspace := Workspace{
		ID:              id,
		Intent:          intent,
		SourceKind:      source,
		SourceID:        fmt.Sprintf("PRIVATE_SOURCE_ID_%03d", index),
		SourceNumber:    sourceNumber,
		Provider:        "github",
		ProviderOrigin:  "https://private-provider.example",
		RepositoryID:    fmt.Sprintf("PRIVATE_REPOSITORY_ID_%03d", index),
		Repository:      repository,
		Phase:           PhaseCharter,
		ExecutionState:  ExecutionWaitingUser,
		ProviderHeadSHA: "PRIVATE_HEAD_SHA",
		Version:         1,
		CreatedAt:       created,
		UpdatedAt:       created.Add(time.Second),
	}
	provider := ProviderSnapshot{
		Intent:         intent,
		SourceKind:     source,
		SourceID:       workspace.SourceID,
		SourceNumber:   sourceNumber,
		SourceURL:      "https://private-provider.example/source",
		Provider:       "github",
		ProviderOrigin: workspace.ProviderOrigin,
		RepositoryID:   workspace.RepositoryID,
		Repository:     repository,
		Title:          fmt.Sprintf("Workspace title %03d", index),
		Body:           "PRIVATE_SOURCE_BODY",
		AuthorID:       "PRIVATE_AUTHOR_ID",
		ObservedAt:     created,
	}
	if source == SourcePullRequest {
		workspace.PullRequestID = fmt.Sprintf("PRIVATE_PULL_ID_%03d", index)
		workspace.PullNumber = int64(index)
		provider.PullRequestID = workspace.PullRequestID
		provider.PullNumber = workspace.PullNumber
	}
	return Aggregate{Workspace: workspace, ProviderSnapshot: provider}
}

func developmentWorkspaceCollectionID(index int) string {
	return fmt.Sprintf("devw_%032x", index)
}

func developmentWorkspaceCollectionTime(index int) time.Time {
	return time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC).Add(time.Duration(index) * time.Minute)
}

func requestDevelopmentWorkspaceCollection(
	t *testing.T,
	handler http.Handler,
	query url.Values,
) *httptest.ResponseRecorder {
	t.Helper()
	path := RuntimeRoutePrefix
	if query != nil {
		path += "?" + query.Encode()
	}
	return developmentHTTPRequest(t, handler, http.MethodGet, path, "")
}

func decodeDevelopmentWorkspaceCollection(
	t *testing.T,
	response *httptest.ResponseRecorder,
) developmentWorkspaceCollectionResponse {
	t.Helper()
	var page developmentWorkspaceCollectionResponse
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page), response.Body.String())
	return page
}

func assertDevelopmentWorkspaceCollectionError(
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
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body), response.Body.String())
	assert.Equal(t, status, response.Code, response.Body.String())
	assert.Equal(t, code, body.Code)
	assert.NotEmpty(t, body.Message)
	assert.LessOrEqual(t, len(body.Message), collectionquery.MaxQueryErrorMessageLen)
}

func mapKeys(values map[string]json.RawMessage) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	return result
}
