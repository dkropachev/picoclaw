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

func TestRepositoryReviewFileAttributionCollectionAggregatesOwnedRecords(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	store := repoaudit.NewStore(workspace)
	repository := "owner/file-attributions"
	file := repoaudit.FileRef{
		Path: "pkg/store.go", BlobSHA: strings.Repeat("a", 40),
		SizeBytes: 240, Category: "code", Mode: "100644",
	}
	otherFile := repoaudit.FileRef{
		Path: "pkg/worker.go", BlobSHA: strings.Repeat("b", 40),
		SizeBytes: 180, Category: "code", Mode: "100644",
	}
	firstCompleted := time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)
	latestCompleted := firstCompleted.Add(time.Hour)
	automationID := "rra_file_attributions"
	attributions := []repoaudit.RepositoryReviewFileAttribution{
		repositoryReviewFileAttributionForAPI(
			t, automationID, "run-one", repoaudit.RepositoryReviewFocusSecurityTrust,
			"assignment-security", 1, firstCompleted, file,
		),
		repositoryReviewFileAttributionForAPI(
			t, automationID, "run-two", repoaudit.RepositoryReviewFocusSecurityTrust,
			"assignment-security", 2, latestCompleted, file,
		),
		repositoryReviewFileAttributionForAPI(
			t, automationID, "unretained-run", repoaudit.RepositoryReviewFocusConcurrencyRecovery,
			"assignment-concurrency", 3, latestCompleted, otherFile,
		),
		repositoryReviewFileAttributionForAPI(
			t, "rra_other_automation", "run-one", repoaudit.RepositoryReviewFocusSecurityTrust,
			"assignment-security", 4, latestCompleted.Add(time.Hour), file,
		),
	}
	if _, err := store.MergeRepositoryReviewFileAttributions(
		t.Context(),
		repoaudit.MergeRepositoryReviewFileAttributionsRequest{
			Repository: repository, ExpectedVersion: 0, Attributions: attributions,
		},
	); err != nil {
		t.Fatal(err)
	}
	automationInput := testRepositoryReviewAutomation()
	automationInput.ID = automationID
	automationInput.Repository = repository
	automationInput.RunIDs = []string{"run-one"}
	automation, err := store.CreateAutomation(t.Context(), automationInput)
	if err != nil {
		t.Fatal(err)
	}

	query := url.QueryEscape("ALL ORDER BY path ASC, focus ASC")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/api/repository-reviews/automations/"+automation.ID+
			"/file-attributions?query="+query+"&limit=1",
		nil,
	))
	if response.Code != http.StatusOK {
		t.Fatalf("file attribution status=%d body=%s", response.Code, response.Body.String())
	}
	var page struct {
		Attributions []repositoryReviewFileAttributionSummary `json:"file_attributions"`
		Total        int                                      `json:"total"`
		NextCursor   string                                   `json:"next_cursor"`
		Canonical    string                                   `json:"canonical_query"`
		QuerySchema  collectionquery.Schema                   `json:"query_schema"`
		Repository   repoaudit.RepositorySummary              `json:"repository"`
		Automation   repoaudit.RepositoryReviewAutomation     `json:"automation"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Attributions) != 1 || page.NextCursor == "" ||
		page.Canonical != "ALL ORDER BY path ASC, focus ASC" ||
		len(page.QuerySchema.Fields) == 0 || page.Repository.Repository != repository ||
		page.Automation.ID != automation.ID {
		t.Fatalf("file attribution page=%#v", page)
	}
	first := page.Attributions[0]
	if first.ID == "" || first.Path != file.Path || first.CommitSHA != strings.Repeat("c", 40) ||
		first.BlobSHA != file.BlobSHA ||
		first.FocusID != repoaudit.RepositoryReviewFocusSecurityTrust ||
		first.RootAgentID != "main" || first.ReviewerIdentity != "review" ||
		first.Account != "review-account" || first.Model != "openai/gpt-5.6-sol" ||
		first.Attempts != 2 || first.RunCount != 2 ||
		len(first.RunIDs) != 2 || first.RunIDs[0] != "run-one" || first.RunIDs[1] != "run-two" ||
		!first.LatestCompletedAt.Equal(latestCompleted) || first.Source != "legacy" ||
		len(first.Sources) != 1 || first.Sources[0] != "legacy_managed_child" {
		t.Fatalf("aggregated file attribution=%#v", first)
	}

	filtered := httptest.NewRecorder()
	mux.ServeHTTP(filtered, httptest.NewRequest(
		http.MethodGet,
		"/api/repository-reviews/automations/"+automation.ID+
			"/file-attributions?query="+
			url.QueryEscape("focus = concurrency_recovery ORDER BY latest DESC"),
		nil,
	))
	if filtered.Code != http.StatusOK ||
		!strings.Contains(filtered.Body.String(), otherFile.Path) ||
		strings.Contains(filtered.Body.String(), `"path":"`+file.Path+`"`) {
		t.Fatalf("filtered status=%d body=%s", filtered.Code, filtered.Body.String())
	}
	exact := httptest.NewRecorder()
	mux.ServeHTTP(exact, httptest.NewRequest(
		http.MethodGet,
		"/api/repository-reviews/automations/"+automation.ID+
			"/file-attributions?query="+url.QueryEscape(
			`focus = security_trust AND model = "openai/gpt-5.6-sol" AND source = legacy`,
		),
		nil,
	))
	if exact.Code != http.StatusOK || !strings.Contains(exact.Body.String(), file.Path) ||
		strings.Contains(exact.Body.String(), `"path":"`+otherFile.Path+`"`) {
		t.Fatalf("exact filter status=%d body=%s", exact.Code, exact.Body.String())
	}

	invalid := httptest.NewRecorder()
	mux.ServeHTTP(invalid, httptest.NewRequest(
		http.MethodGet,
		"/api/repository-reviews/automations/"+automation.ID+
			"/file-attributions?limit=201",
		nil,
	))
	if invalid.Code != http.StatusBadRequest ||
		!strings.Contains(invalid.Body.String(), `"code":"invalid_page_limit"`) {
		t.Fatalf("invalid status=%d body=%s", invalid.Code, invalid.Body.String())
	}
}

func TestRepositoryReviewFileAttributionSummariesKeepProvenanceDistinct(t *testing.T) {
	completedAt := time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)
	file := repoaudit.FileRef{
		Path: "pkg/store.go", BlobSHA: strings.Repeat("a", 40),
		SizeBytes: 240, Category: "code", Mode: "100644",
	}
	base := repositoryReviewFileAttributionForAPI(
		t, "rra_summary_distinct", "run-one", repoaudit.RepositoryReviewFocusSecurityTrust,
		"assignment-security", 1, completedAt, file,
	)
	inputs := []repoaudit.RepositoryReviewFileAttribution{base}
	for index, mutate := range []func(*repoaudit.RepositoryReviewFileAttribution){
		func(value *repoaudit.RepositoryReviewFileAttribution) {
			value.RootAgentID = "secondary"
		},
		func(value *repoaudit.RepositoryReviewFileAttribution) {
			value.Account = "other-account"
		},
		func(value *repoaudit.RepositoryReviewFileAttribution) {
			value.CommitSHA = strings.Repeat("e", 40)
			value.AcknowledgedFiles[0].BlobSHA = strings.Repeat("f", 40)
		},
	} {
		candidate := base
		candidate.ID = ""
		candidate.AcknowledgedFiles = append([]repoaudit.FileRef(nil), base.AcknowledgedFiles...)
		candidate.ChildIndex = index + 2
		mutate(&candidate)
		normalized, err := repoaudit.NewRepositoryReviewFileAttribution(candidate)
		if err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, normalized)
	}

	summaries := repositoryReviewFileAttributionSummaries(
		repoaudit.RepositoryReviewAutomation{ID: "rra_summary_distinct"}, inputs,
	)
	if len(summaries) != 4 {
		t.Fatalf("provenance-distinct summaries=%#v", summaries)
	}
	seen := make(map[string]struct{}, len(summaries))
	for _, summary := range summaries {
		if _, duplicate := seen[summary.ID]; duplicate {
			t.Fatalf("duplicate summary cursor ID: %#v", summary)
		}
		seen[summary.ID] = struct{}{}
	}
}

func TestRepositoryReviewFileAttributionSummariesCoalesceSourcesAndCapRunSample(t *testing.T) {
	completedAt := time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)
	file := repoaudit.FileRef{
		Path: "pkg/store.go", BlobSHA: strings.Repeat("a", 40),
		SizeBytes: 240, Category: "code", Mode: "100644",
	}
	automationID := "rra_summary_mixed"
	inputs := make([]repoaudit.RepositoryReviewFileAttribution, 0, 21)
	for index := range 21 {
		attribution := repositoryReviewFileAttributionForAPI(
			t, automationID, fmt.Sprintf("run-%02d", index+1),
			repoaudit.RepositoryReviewFocusSecurityTrust,
			"assignment-security", index+1, completedAt.Add(time.Duration(index)*time.Minute), file,
		)
		if index == 20 {
			attribution.ID = ""
			attribution.Source = repoaudit.RepositoryReviewFileAttributionSourceLiveCheckpoint
			attribution.Model = "openai/gpt-5.6-sol"
			attribution.UsageModel = ""
			var err error
			attribution, err = repoaudit.NewRepositoryReviewFileAttribution(attribution)
			if err != nil {
				t.Fatal(err)
			}
		}
		inputs = append(inputs, attribution)
	}

	summaries := repositoryReviewFileAttributionSummaries(
		repoaudit.RepositoryReviewAutomation{ID: automationID}, inputs,
	)
	if len(summaries) != 1 {
		t.Fatalf("mixed-source summaries=%#v", summaries)
	}
	summary := summaries[0]
	if summary.ID == "" || summary.Model != "openai/gpt-5.6-sol" ||
		summary.Source != "mixed" || len(summary.Sources) != 2 ||
		summary.Attempts != 21 || summary.RunCount != 21 ||
		len(summary.RunIDs) != maxRepositoryReviewFileAttributionRunSample {
		t.Fatalf("mixed-source summary=%#v", summary)
	}
	model, ok := repositoryReviewFileAttributionCollectionField(summary, "model")
	if !ok || model.Text != summary.Model {
		t.Fatalf("model field=%#v ok=%v", model, ok)
	}
	source, ok := repositoryReviewFileAttributionCollectionField(summary, "source")
	if !ok || source.Text != "mixed" {
		t.Fatalf("source field=%#v ok=%v", source, ok)
	}
}

func TestRepositoryReviewFileAttributionCollectionPagesBeyondMaximum(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	store := repoaudit.NewStore(workspace)
	repository := "owner/file-attribution-pages"
	automationID := "rra_file_attribution_pages"
	completedAt := time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)
	attributions := make([]repoaudit.RepositoryReviewFileAttribution, 0, 201)
	for index := range 201 {
		file := repoaudit.FileRef{
			Path:    fmt.Sprintf("pkg/file_%03d.go", index),
			BlobSHA: fmt.Sprintf("%040x", index+1), SizeBytes: 100,
			Category: "code", Mode: "100644",
		}
		attributions = append(attributions, repositoryReviewFileAttributionForAPI(
			t, automationID, "run-page", repoaudit.RepositoryReviewFocusSecurityTrust,
			"assignment-security", index+1, completedAt, file,
		))
	}
	if _, err := store.MergeRepositoryReviewFileAttributions(
		t.Context(), repoaudit.MergeRepositoryReviewFileAttributionsRequest{
			Repository: repository, ExpectedVersion: 0, Attributions: attributions,
		},
	); err != nil {
		t.Fatal(err)
	}
	automationInput := testRepositoryReviewAutomation()
	automationInput.ID = automationID
	automationInput.Repository = repository
	automationInput.RunIDs = []string{"run-page"}
	if _, err := store.CreateAutomation(t.Context(), automationInput); err != nil {
		t.Fatal(err)
	}

	firstResponse := httptest.NewRecorder()
	mux.ServeHTTP(firstResponse, httptest.NewRequest(
		http.MethodGet,
		"/api/repository-reviews/automations/"+automationID+"/file-attributions?limit=200",
		nil,
	))
	var firstPage struct {
		Attributions []repositoryReviewFileAttributionSummary `json:"file_attributions"`
		Total        int                                      `json:"total"`
		NextCursor   string                                   `json:"next_cursor"`
	}
	if err := json.Unmarshal(firstResponse.Body.Bytes(), &firstPage); err != nil {
		t.Fatal(err)
	}
	if firstResponse.Code != http.StatusOK || firstPage.Total != 201 ||
		len(firstPage.Attributions) != 200 || firstPage.NextCursor == "" {
		t.Fatalf("first attribution page=%#v body=%s", firstPage, firstResponse.Body.String())
	}
	secondResponse := httptest.NewRecorder()
	mux.ServeHTTP(secondResponse, httptest.NewRequest(
		http.MethodGet,
		"/api/repository-reviews/automations/"+automationID+
			"/file-attributions?limit=200&cursor="+url.QueryEscape(firstPage.NextCursor),
		nil,
	))
	var secondPage struct {
		Attributions []repositoryReviewFileAttributionSummary `json:"file_attributions"`
		NextCursor   string                                   `json:"next_cursor"`
	}
	if err := json.Unmarshal(secondResponse.Body.Bytes(), &secondPage); err != nil {
		t.Fatal(err)
	}
	if secondResponse.Code != http.StatusOK || len(secondPage.Attributions) != 1 ||
		secondPage.NextCursor != "" || secondPage.Attributions[0].ID == "" {
		t.Fatalf("second attribution page=%#v body=%s", secondPage, secondResponse.Body.String())
	}
}

func repositoryReviewFileAttributionForAPI(
	t *testing.T,
	automationID string,
	runID string,
	focusID string,
	assignmentID string,
	childIndex int,
	completedAt time.Time,
	file repoaudit.FileRef,
) repoaudit.RepositoryReviewFileAttribution {
	t.Helper()
	attribution, err := repoaudit.NewRepositoryReviewFileAttribution(
		repoaudit.RepositoryReviewFileAttribution{
			AutomationID: automationID, RunID: runID, CommitSHA: strings.Repeat("c", 40),
			InventoryHash: "inventory", ProfileHash: "profile",
			AssignmentID: assignmentID, FocusID: focusID,
			RootAgentID: "main", ReviewerIdentity: "review",
			Model: "gpt-5.6-sol", ModelAlias: "review", Account: "review-account",
			UsageModel: "openai/gpt-5.6-sol", AcknowledgedFiles: []repoaudit.FileRef{file},
			EvidenceDigest: "sha256:" + strings.Repeat("d", 64),
			Source:         repoaudit.RepositoryReviewFileAttributionSourceLegacyManagedChild,
			ChildIndex:     childIndex, Required: true, CompletedAt: completedAt,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return attribution
}
