package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

func TestRepositoryReviewCanonicalReadRoutesAndSourcePaging(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewAPIState(t, workspace)
	state = completeRepositoryReviewAPIMappingJobs(t, workspace, state)
	automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
	base := "/api/repository-reviews/automations/" + automation.ID
	findingID := state.Findings[0].ID
	rawID := state.RawFindings[0].ID
	repositoryFindingID := state.RepositoryFindings[0].ID

	for _, target := range []string{
		base,
		base + "/findings?query=ALL&limit=1",
		base + "/findings/" + findingID,
		base + "/findings/" + findingID + "/sources?offset=0&limit=1",
		base + "/findings/" + findingID + "/sources/" + rawID,
		base + "/raw-findings?query=ALL&limit=1",
		base + "/raw-findings/" + rawID,
		base + "/findings-processing?query=ALL&limit=1",
		base + "/findings-processing/sources/" + rawID,
		base + "/repository-findings?query=ALL&limit=1",
		base + "/repository-findings/" + repositoryFindingID,
		base + "/file-attributions?query=ALL&limit=1",
		base + "/issues?query=ALL&limit=1",
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s=%d %s", target, response.Code, response.Body.String())
		}
	}

	for _, target := range []string{
		base + "/findings/" + findingID + "/sources?unknown=1",
		base + "/findings/" + findingID + "/sources?offset=-1",
		base + "/findings/" + findingID + "/sources?limit=201",
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid source page %s=%d %s", target, response.Code, response.Body.String())
		}
	}
	for _, target := range []string{
		base + "/findings/rdf_missing/sources",
		base + "/findings/" + findingID + "/sources/rrw_missing",
		base + "/raw-findings/rrw_missing",
		base + "/findings-processing/sources/rrw_missing",
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("missing source %s=%d %s", target, response.Code, response.Body.String())
		}
	}
}

func TestRepositoryReviewCanonicalRetryRouteBoundaries(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewAPIState(t, workspace)
	automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
	base := "/api/repository-reviews/automations/" + automation.ID
	rawID := state.RawFindings[0].ID

	for _, target := range []string{
		base + "/raw-findings/" + rawID + "/retry",
		base + "/findings-processing/sources/" + rawID + "/retry",
	} {
		response := repositoryReviewAutomationMutation(t, mux, http.MethodPost, target, map[string]any{})
		if response.Code != http.StatusConflict {
			t.Fatalf("completed retry %s=%d %s", target, response.Code, response.Body.String())
		}
		withQuery := repositoryReviewAutomationMutation(t, mux, http.MethodPost, target+"?x=1", map[string]any{})
		if withQuery.Code != http.StatusBadRequest {
			t.Fatalf("query retry %s=%d %s", target, withQuery.Code, withQuery.Body.String())
		}
		malformed := httptest.NewRequest(http.MethodPost, target, strings.NewReader(`{`))
		setRepositoryReviewMutationHeaders(malformed)
		malformedResponse := httptest.NewRecorder()
		mux.ServeHTTP(malformedResponse, malformed)
		if malformedResponse.Code != http.StatusBadRequest {
			t.Fatalf("malformed retry %s=%d %s", target, malformedResponse.Code, malformedResponse.Body.String())
		}
	}
	missingRaw := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, base+"/raw-findings/rrw_missing/retry", map[string]any{},
	)
	if missingRaw.Code != http.StatusNotFound {
		t.Fatalf("missing raw retry=%d %s", missingRaw.Code, missingRaw.Body.String())
	}

	bulkPath := base + "/findings-processing/retry"
	bulk := repositoryReviewAutomationMutation(t, mux, http.MethodPost, bulkPath, map[string]any{
		"source_ids": []string{rawID, "rrw_missing"},
	})
	if bulk.Code != http.StatusAccepted || !strings.Contains(bulk.Body.String(), `"failures"`) {
		t.Fatalf("bulk retry=%d %s", bulk.Code, bulk.Body.String())
	}
	for _, body := range []string{
		`{`, `{"source_ids":[]}`, `{"source_ids":["same"," same "]}`,
		`{"source_ids":["one"],"extra":true}`,
	} {
		request := httptest.NewRequest(http.MethodPost, bulkPath, strings.NewReader(body))
		setRepositoryReviewMutationHeaders(request)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid bulk %q=%d %s", body, response.Code, response.Body.String())
		}
	}
	missing := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost,
		"/api/repository-reviews/automations/rra_missing/findings-processing/retry",
		map[string]any{"source_ids": []string{rawID}},
	)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing bulk automation=%d %s", missing.Code, missing.Body.String())
	}
}

func TestRepositoryReviewCanonicalRawAndBulkRetrySuccess(t *testing.T) {
	t.Run("raw source", func(t *testing.T) {
		handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
		t.Cleanup(handler.Shutdown)
		state := seedRepositoryReviewAPIStateWithProcessing(t, workspace, false)
		store := repoaudit.NewSQLiteStore(workspace)
		state = failCanonicalRepositoryReviewRaw(t, store, state)
		automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
		response := repositoryReviewAutomationMutation(
			t, mux, http.MethodPost,
			"/api/repository-reviews/automations/"+automation.ID+
				"/raw-findings/"+state.RawFindings[0].ID+"/retry",
			map[string]any{},
		)
		if response.Code != http.StatusAccepted ||
			!strings.Contains(response.Body.String(), `"deduplication_state":"pending"`) {
			t.Fatalf("raw retry=%d %s", response.Code, response.Body.String())
		}
	})

	t.Run("bulk processing", func(t *testing.T) {
		handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
		t.Cleanup(handler.Shutdown)
		state := seedRepositoryReviewAPIStateWithProcessing(t, workspace, false)
		store := repoaudit.NewSQLiteStore(workspace)
		state = failCanonicalRepositoryReviewRaw(t, store, state)
		automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
		response := repositoryReviewAutomationMutation(
			t, mux, http.MethodPost,
			"/api/repository-reviews/automations/"+automation.ID+"/findings-processing/retry",
			map[string]any{"source_ids": []string{state.RawFindings[0].ID}},
		)
		if response.Code != http.StatusAccepted ||
			!strings.Contains(response.Body.String(), `"retried_ids":["`+state.RawFindings[0].ID+`"]`) {
			t.Fatalf("bulk retry=%d %s", response.Code, response.Body.String())
		}
	})
}

func failCanonicalRepositoryReviewRaw(
	t *testing.T,
	store repoaudit.Store,
	state repoaudit.RepositoryState,
) repoaudit.RepositoryState {
	t.Helper()
	jobID := state.DeduplicationJobs[0].ID
	for attempt := 0; attempt < repoaudit.DeduplicationAttemptLimit; attempt++ {
		_, claim, claimed, err := store.ClaimDeduplicationJob(state.Repository, jobID, time.Minute)
		if err != nil || !claimed {
			t.Fatalf("claim %d: claimed=%v err=%v", attempt, claimed, err)
		}
		if _, _, _, err := store.FailDeduplicationJob(
			state.Repository, jobID, claim.Job.LeaseID, errors.New("canonical test failure"),
		); err != nil {
			t.Fatal(err)
		}
	}
	updated, found, err := store.Get(state.Repository)
	if err != nil || !found || updated.RawFindings[0].State != repoaudit.RawFindingDeduplicationFailed {
		t.Fatalf("failed raw state=%#v found=%v err=%v", updated, found, err)
	}
	return updated
}

func TestRepositoryReviewCanonicalRawCollectionHelperCoverage(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	raw := repoaudit.RawReviewFinding{
		ID: "rrw_fields", CampaignID: "rrc_fields",
		File: repoaudit.FileRef{Path: "pkg/service.go"}, Severity: "high",
		Title: "Lost update", Symbol: "Save", Model: "provider/model", Reviewer: "reviewer",
		State: repoaudit.RawFindingDeduplicationFailed, Disposition: repoaudit.RawFindingDispositionUndecided,
		Failure:   &repoaudit.DeduplicationFailure{Code: "failed", Message: "Safe failure.", At: now},
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	}
	rawSummary := projectRepositoryReviewRawFindingSummary(raw)
	contextID := repositoryReviewCollectionCursorContext("raw", "rra_fields")
	for _, options := range []collectionquery.PageOptions[repositoryReviewRawFindingSummary]{
		repositoryReviewRawFindingPageOptions(contextID),
		repositoryReviewFindingsProcessingPageOptions(contextID),
	} {
		cursor, err := options.ID(rawSummary)
		if err != nil || !options.ValidateID(cursor) {
			t.Fatalf("raw cursor=%q err=%v", cursor, err)
		}
		for _, field := range append(
			append([]collectionquery.Field{}, repositoryReviewRawFindingCollectionSchemaFieldNames()...),
			repositoryReviewFindingsProcessingCollectionSchemaFieldNames()...,
		) {
			_, _ = options.Resolve(rawSummary, field, now)
		}
		if _, ok := options.Resolve(rawSummary, "unknown", now); ok {
			t.Fatal("unknown raw field resolved")
		}
	}

	deduplicated := repositoryReviewDeduplicatedFindingSummary{
		ID: "rdf_fields", Repository: "owner/repo", Path: "pkg/service.go", Severity: "high",
		Title: "Lost update", Symbol: "Save", Status: repoaudit.FindingOpen,
		RunFindingStatus: repositoryReviewRunFindingPending, Association: "unassociated",
		Contributors: []string{"reviewer"}, RawSourceCount: 2, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	}
	deduplicatedOptions := repositoryReviewDeduplicatedFindingPageOptions(contextID)
	for _, field := range repositoryReviewDeduplicatedFindingCollectionSchema.Fields {
		if _, ok := deduplicatedOptions.Resolve(deduplicated, field.Name, now); !ok {
			t.Fatalf("deduplicated field %q unresolved", field.Name)
		}
	}
	if _, ok := deduplicatedOptions.Resolve(deduplicated, "unknown", now); ok {
		t.Fatal("unknown deduplicated field resolved")
	}

	for target, wantError := range map[string]bool{
		"/":                    false,
		"/?offset=2&limit=10":  false,
		"/?offset=-1":          true,
		"/?limit=201":          true,
		"/?unknown=1":          true,
		"/?offset=1&offset=2":  true,
		"/?limit=not-a-number": true,
	} {
		_, _, err := repositoryReviewRawPage(httptest.NewRequest(http.MethodGet, target, nil))
		if (err != nil) != wantError {
			t.Fatalf("raw page %q err=%v want_error=%v", target, err, wantError)
		}
	}
	if _, _, err := repositoryReviewRawPage(nil); err == nil {
		t.Fatal("nil raw page request accepted")
	}
	if _, err := repositoryReviewPageInteger("7", 0, 10); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"-1", "0", "11", "bad"} {
		if _, err := repositoryReviewPageInteger(value, 0, 10); err == nil {
			t.Fatalf("page integer %q accepted", value)
		}
	}
	if !containsRepositoryReviewSourceID([]string{"one", "two"}, "two") ||
		containsRepositoryReviewSourceID([]string{"one"}, "missing") {
		t.Fatal("source membership mismatch")
	}

	counters := repositoryReviewFindingsProcessingCounters([]repoaudit.RawReviewFinding{
		{State: repoaudit.RawFindingDeduplicationPending, UpdatedAt: now},
		{State: repoaudit.RawFindingDeduplicationRunning, UpdatedAt: now.Add(time.Minute)},
		{State: repoaudit.RawFindingDeduplicationFailed, UpdatedAt: now.Add(2 * time.Minute)},
		{
			State:       repoaudit.RawFindingDeduplicationCompleted,
			Disposition: repoaudit.RawFindingDispositionNew, UpdatedAt: now.Add(3 * time.Minute),
		},
		{
			State:       repoaudit.RawFindingDeduplicationCompleted,
			Disposition: repoaudit.RawFindingDispositionDuplicate, UpdatedAt: now.Add(4 * time.Minute),
		},
	})
	if counters.RawTotal != 5 || counters.Pending != 1 || counters.Processing != 1 ||
		counters.Failed != 1 || counters.Completed != 2 || counters.New != 1 || counters.Duplicates != 1 {
		t.Fatalf("processing counters=%#v", counters)
	}

	request := httptest.NewRequest(http.MethodGet, "/?generation_id=g&offset=1&limit=2", nil)
	generationID, offset, limit, err := repositoryReviewIssuePage(request)
	if err != nil || generationID != "g" || offset != 1 || limit != 2 {
		t.Fatalf("issue page=%q/%d/%d err=%v", generationID, offset, limit, err)
	}
	for _, target := range []string{"/?bad=1", "/?generation_id=" + url.QueryEscape(strings.Repeat("x", 257))} {
		if _, _, _, err := repositoryReviewIssuePage(httptest.NewRequest(http.MethodGet, target, nil)); err == nil {
			t.Fatalf("invalid issue page %q accepted", target)
		}
	}
	if _, _, _, err := repositoryReviewIssuePage(nil); err == nil {
		t.Fatal("nil issue page accepted")
	}
}

func repositoryReviewRawFindingCollectionSchemaFieldNames() []collectionquery.Field {
	fields := make([]collectionquery.Field, 0, len(repositoryReviewRawFindingCollectionSchema.Fields))
	for _, field := range repositoryReviewRawFindingCollectionSchema.Fields {
		fields = append(fields, field.Name)
	}
	return fields
}

func repositoryReviewFindingsProcessingCollectionSchemaFieldNames() []collectionquery.Field {
	fields := make([]collectionquery.Field, 0, len(repositoryReviewFindingsProcessingCollectionSchema.Fields))
	for _, field := range repositoryReviewFindingsProcessingCollectionSchema.Fields {
		fields = append(fields, field.Name)
	}
	return fields
}
