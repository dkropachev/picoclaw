package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

func TestRepositoryReviewFileAttributionHandlerErrorsAndEmptyLedger(t *testing.T) {
	handler, mux, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	automationInput := testRepositoryReviewAutomation()
	automationInput.ID = "rra_file_attribution_empty"
	automationInput.Repository = "owner/file-attribution-empty"
	automation, err := store.CreateAutomation(t.Context(), automationInput)
	if err != nil {
		t.Fatal(err)
	}
	basePath := "/api/repository-reviews/automations/" + automation.ID + "/file-attributions"

	invalidQuery := httptest.NewRecorder()
	mux.ServeHTTP(invalidQuery, httptest.NewRequest(
		http.MethodGet, basePath+"?query="+url.QueryEscape("unknown = value"), nil,
	))
	if invalidQuery.Code != http.StatusBadRequest ||
		!strings.Contains(invalidQuery.Body.String(), `"code":"invalid_query"`) {
		t.Fatalf("invalid query status=%d body=%s", invalidQuery.Code, invalidQuery.Body.String())
	}

	missing := httptest.NewRecorder()
	mux.ServeHTTP(missing, httptest.NewRequest(
		http.MethodGet,
		"/api/repository-reviews/automations/rra_missing/file-attributions",
		nil,
	))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing status=%d body=%s", missing.Code, missing.Body.String())
	}

	invalidCursor := httptest.NewRecorder()
	mux.ServeHTTP(invalidCursor, httptest.NewRequest(
		http.MethodGet, basePath+"?cursor=not-a-cursor", nil,
	))
	if invalidCursor.Code != http.StatusBadRequest ||
		!strings.Contains(invalidCursor.Body.String(), `"code":"invalid_cursor"`) {
		t.Fatalf("invalid cursor status=%d body=%s", invalidCursor.Code, invalidCursor.Body.String())
	}

	empty := httptest.NewRecorder()
	mux.ServeHTTP(empty, httptest.NewRequest(http.MethodGet, basePath, nil))
	var emptyBody map[string]json.RawMessage
	if err := json.Unmarshal(empty.Body.Bytes(), &emptyBody); err != nil {
		t.Fatal(err)
	}
	_, projectedRepository := emptyBody["repository"]
	if empty.Code != http.StatusOK ||
		string(emptyBody["file_attributions"]) != "[]" || projectedRepository {
		t.Fatalf("empty status=%d body=%s", empty.Code, empty.Body.String())
	}
}

func TestRepositoryReviewFileAttributionSuggestionAndFieldHelpers(t *testing.T) {
	values := []string{}
	appendRepositoryReviewAttributionSuggestion(&values, "   ")
	appendRepositoryReviewAttributionSuggestion(&values, "Alpha")
	appendRepositoryReviewAttributionSuggestion(&values, " alpha ")
	for index := 1; index < collectionquery.MaxSuggestedValues; index++ {
		appendRepositoryReviewAttributionSuggestion(&values, fmt.Sprintf("value-%03d", index))
	}
	appendRepositoryReviewAttributionSuggestion(&values, "overflow")
	if len(values) != collectionquery.MaxSuggestedValues || values[0] != "Alpha" {
		t.Fatalf("suggestions=%#v", values)
	}

	completedAt := time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)
	summary := repositoryReviewFileAttributionSummary{
		ID: "summary", Path: "pkg/store.go", CommitSHA: strings.Repeat("a", 40),
		BlobSHA: strings.Repeat("b", 40), FocusID: repoaudit.RepositoryReviewFocusSecurityTrust,
		RootAgentID: "main", ReviewerIdentity: "review", Account: "account",
		Model: "model", Source: "mixed", Attempts: 3, RunCount: 2,
		LatestCompletedAt: completedAt,
	}
	fields := []collectionquery.Field{
		"path", "commit", "blob", "focus", "agent", "reviewer", "account",
		"model", "source", "attempts", "runs", "latest",
	}
	for _, field := range fields {
		if _, ok := repositoryReviewFileAttributionCollectionField(summary, field); !ok {
			t.Fatalf("field %q was not resolved", field)
		}
	}
	if _, ok := repositoryReviewFileAttributionCollectionField(summary, "unknown"); ok {
		t.Fatal("unknown field was resolved")
	}
}

func TestRepositoryReviewCanonicalFileAttributionAggregationCoverage(t *testing.T) {
	automation := repoaudit.RepositoryReviewAutomation{ID: "rra_attribution_coverage"}
	completedAt := time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)
	file := repoaudit.FileRef{
		Path: "pkg/store.go", BlobSHA: strings.Repeat("a", 40), SizeBytes: 240,
		Category: "code", Mode: "100644",
	}
	records := make(
		[]repoaudit.RepositoryReviewFileAttribution,
		0,
		maxRepositoryReviewFileAttributionRunSample+4,
	)
	records = append(records,
		repoaudit.RepositoryReviewFileAttribution{
			AutomationID: "rra_other", RunID: "foreign", Model: "ignored",
			AcknowledgedFiles: []repoaudit.FileRef{file},
		},
		repoaudit.RepositoryReviewFileAttribution{
			AutomationID: automation.ID, RunID: "blank", Model: "ignored",
			AcknowledgedFiles: []repoaudit.FileRef{{Path: "   "}},
		},
	)
	for index := range maxRepositoryReviewFileAttributionRunSample + 1 {
		records = append(records, repoaudit.RepositoryReviewFileAttribution{
			ID:           fmt.Sprintf("rfa_%03d", index),
			AutomationID: automation.ID,
			RunID:        fmt.Sprintf("run-%03d", index),
			CommitSHA:    strings.Repeat("b", 40),
			FocusID:      repoaudit.RepositoryReviewFocusSecurityTrust, RootAgentID: "main",
			ReviewerIdentity: "review", Model: "provider-model", UsageModel: "resolved-model",
			Account: "account", Source: repoaudit.RepositoryReviewFileAttributionSourceLiveCheckpoint,
			AcknowledgedFiles: []repoaudit.FileRef{file, file},
			CompletedAt:       completedAt.Add(time.Duration(index) * time.Minute),
		})
	}
	records = append(records, repoaudit.RepositoryReviewFileAttribution{
		ID: "rfa_fallback", AutomationID: automation.ID, RunID: "fallback-run",
		CommitSHA: strings.Repeat("b", 40), FocusID: repoaudit.RepositoryReviewFocusSecurityTrust,
		RootAgentID: "main", ReviewerIdentity: "review", Model: "fallback-model", Account: "account",
		AcknowledgedFiles: []repoaudit.FileRef{file}, CompletedAt: completedAt,
	})

	summaries := repositoryReviewFileAttributionSummaries(automation, records)
	if len(summaries) != 2 {
		t.Fatalf("attribution summaries=%#v", summaries)
	}
	var resolved repositoryReviewFileAttributionSummary
	for _, summary := range summaries {
		if summary.Model == "resolved-model" {
			resolved = summary
		}
	}
	if resolved.ID == "" || resolved.Attempts != maxRepositoryReviewFileAttributionRunSample+1 ||
		resolved.RunCount != maxRepositoryReviewFileAttributionRunSample+1 ||
		len(resolved.RunIDs) != maxRepositoryReviewFileAttributionRunSample ||
		resolved.Source != "live" || len(resolved.Sources) != 1 {
		t.Fatalf("resolved attribution summary=%#v", resolved)
	}
	if repositoryReviewFileAttributionGroupKey(file, records[2], "resolved-model") == "" ||
		repositoryReviewFileAttributionEffectiveModel(records[len(records)-1]) != "fallback-model" ||
		repositoryReviewFileAttributionSourceClass(nil) != "live" ||
		len(sortedRepositoryReviewAttributionSet(map[string]struct{}{"b": {}, "a": {}})) != 2 {
		t.Fatal("attribution helper projection mismatch")
	}
	contextID := repositoryReviewCollectionCursorContext("file-attributions", automation.ID)
	pageOptions := repositoryReviewFileAttributionPageOptions(contextID)
	cursor, err := pageOptions.ID(resolved)
	if err != nil || !pageOptions.ValidateID(cursor) {
		t.Fatalf("attribution cursor=%q err=%v", cursor, err)
	}
}
