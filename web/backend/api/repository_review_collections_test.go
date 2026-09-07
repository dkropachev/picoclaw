package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

func TestRepositoryReviewCollectionRoutesAndCompactProjections(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedCanonicalRepositoryReviewGenerationFindings(t, workspace, 4)
	state = completeRepositoryReviewAPIMappingJobs(t, workspace, state)
	automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)

	runQuery := url.QueryEscape("ALL ORDER BY repository ASC")
	runResponse := httptest.NewRecorder()
	mux.ServeHTTP(runResponse, httptest.NewRequest(
		http.MethodGet,
		"/api/repository-reviews/automations/"+automation.ID+"/findings?query="+runQuery+"&limit=1",
		nil,
	))
	if runResponse.Code != http.StatusOK {
		t.Fatalf("run findings status=%d body=%s", runResponse.Code, runResponse.Body.String())
	}
	var runPage struct {
		Findings       []repositoryReviewRunFindingSummary `json:"findings"`
		Total          int                                 `json:"total"`
		NextCursor     string                              `json:"next_cursor"`
		CanonicalQuery string                              `json:"canonical_query"`
		QuerySchema    collectionquery.Schema              `json:"query_schema"`
	}
	if err := json.Unmarshal(runResponse.Body.Bytes(), &runPage); err != nil {
		t.Fatal(err)
	}
	if runPage.Total != 4 || len(runPage.Findings) != 1 || runPage.NextCursor == "" ||
		runPage.CanonicalQuery != "ALL ORDER BY repository ASC" || len(runPage.QuerySchema.Fields) == 0 {
		t.Fatalf("run page=%#v", runPage)
	}
	var runWire struct {
		Findings []map[string]json.RawMessage `json:"findings"`
	}
	if err := json.Unmarshal(runResponse.Body.Bytes(), &runWire); err != nil {
		t.Fatal(err)
	}
	for _, detailOnly := range []string{
		"commit_sha", "file", "message", "models", "observation_count", "target_branch",
		"advertised_default_branch", "version", "issue_draft_id", "evidence", "impact",
		"observations", "validation", "match_hints", "fix_effort",
	} {
		if _, found := runWire.Findings[0][detailOnly]; found {
			t.Fatalf("run summary leaked detail field %q: %s", detailOnly, runResponse.Body.String())
		}
	}

	repositoryResponse := httptest.NewRecorder()
	mux.ServeHTTP(repositoryResponse, httptest.NewRequest(
		http.MethodGet,
		"/api/repository-reviews/automations/"+automation.ID+
			"/repository-findings?query="+runQuery+"&limit=1",
		nil,
	))
	if repositoryResponse.Code != http.StatusOK {
		t.Fatalf("repository findings status=%d body=%s", repositoryResponse.Code, repositoryResponse.Body.String())
	}
	var repositoryPage struct {
		Findings   []repositoryReviewRepositoryFindingCollectionSummary `json:"repository_findings"`
		Total      int                                                  `json:"total"`
		NextCursor string                                               `json:"next_cursor"`
	}
	if err := json.Unmarshal(repositoryResponse.Body.Bytes(), &repositoryPage); err != nil {
		t.Fatal(err)
	}
	if repositoryPage.Total != 4 || len(repositoryPage.Findings) != 1 || repositoryPage.NextCursor == "" ||
		repositoryPage.Findings[0].Path == "" ||
		repositoryPage.Findings[0].OccurrenceCount != 1 {
		t.Fatalf("repository page=%#v", repositoryPage)
	}
	var repositoryWire struct {
		Findings []map[string]json.RawMessage `json:"repository_findings"`
	}
	if err := json.Unmarshal(repositoryResponse.Body.Bytes(), &repositoryWire); err != nil {
		t.Fatal(err)
	}
	for _, detailOnly := range []string{
		"review_finding_ids", "found_commits", "path_symbol_history", "possible_duplicates",
		"resolution_history", "match_hints", "fix_effort", "version",
	} {
		if _, found := repositoryWire.Findings[0][detailOnly]; found {
			t.Fatalf("repository summary leaked detail field %q: %s", detailOnly, repositoryResponse.Body.String())
		}
	}

	detailResponse := httptest.NewRecorder()
	mux.ServeHTTP(detailResponse, httptest.NewRequest(
		http.MethodGet,
		"/api/repository-reviews/automations/"+automation.ID+
			"/repository-findings/"+repositoryPage.Findings[0].ID,
		nil,
	))
	if detailResponse.Code != http.StatusOK ||
		!strings.Contains(detailResponse.Body.String(), `"repository_finding"`) ||
		!strings.Contains(detailResponse.Body.String(), `"evidence"`) {
		t.Fatalf("repository detail status=%d body=%s", detailResponse.Code, detailResponse.Body.String())
	}
	runIDOnRepositoryRoute := httptest.NewRecorder()
	mux.ServeHTTP(runIDOnRepositoryRoute, httptest.NewRequest(
		http.MethodGet,
		"/api/repository-reviews/automations/"+automation.ID+
			"/repository-findings/"+runPage.Findings[0].ID,
		nil,
	))
	if runIDOnRepositoryRoute.Code != http.StatusNotFound {
		t.Fatalf(
			"run ID on repository detail status=%d body=%s",
			runIDOnRepositoryRoute.Code,
			runIDOnRepositoryRoute.Body.String(),
		)
	}

	wrongContext := httptest.NewRecorder()
	mux.ServeHTTP(wrongContext, httptest.NewRequest(
		http.MethodGet,
		"/api/repository-reviews/automations/"+automation.ID+
			"/repository-findings?query="+runQuery+"&cursor="+url.QueryEscape(runPage.NextCursor),
		nil,
	))
	if wrongContext.Code != http.StatusBadRequest ||
		!strings.Contains(wrongContext.Body.String(), `"code":"invalid_cursor"`) {
		t.Fatalf("wrong-context cursor status=%d body=%s", wrongContext.Code, wrongContext.Body.String())
	}
}

func TestRepositoryReviewCollectionSeverityOrderAndStructuredQueryError(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	query, err := collectionquery.Parse("", repositoryReviewRunFindingCollectionSchema)
	if err != nil {
		t.Fatal(err)
	}
	summaries := []repositoryReviewRunFindingSummary{
		repositoryReviewCollectionFindingForTest("low", "low", now),
		repositoryReviewCollectionFindingForTest("critical", "critical", now),
		repositoryReviewCollectionFindingForTest("high", "high", now),
	}
	contextID := repositoryReviewCollectionCursorContext("findings", "rra_test", "current")
	page, err := collectionquery.Paginate(
		summaries, query, "", 50, now, repositoryReviewRunFindingPageOptions(contextID),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{page.Items[0].Severity, page.Items[1].Severity, page.Items[2].Severity}; strings.Join(
		got,
		",",
	) != "critical,high,low" {
		t.Fatalf("severity order=%v", got)
	}

	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewAPIState(t, workspace)
	automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
	malformedQuery := `title = "é" AND`
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/api/repository-reviews/automations/"+automation.ID+"/findings?query="+url.QueryEscape(malformedQuery),
		nil,
	))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("query error status=%d body=%s", response.Code, response.Body.String())
	}
	var queryError struct {
		Code     string `json:"code"`
		Position int    `json:"position"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &queryError); err != nil {
		t.Fatal(err)
	}
	if queryError.Code != "invalid_query" || queryError.Position != len(malformedQuery) {
		t.Fatalf("query error=%#v query bytes=%d body=%s", queryError, len(malformedQuery), response.Body.String())
	}
}

func TestRepositoryReviewCanonicalCollectionSchemasResolveEveryField(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	automation := testRepositoryReviewAutomation()
	automation.ID = "rra_collection_fields"
	automation.Status = repoaudit.RepositoryReviewAutomationRunning
	automation.UpdatedAt = now
	for _, field := range repositoryReviewAutomationCollectionSchema.Fields {
		if _, ok := repositoryReviewAutomationCollectionField(automation, field.Name); !ok {
			t.Fatalf("automation schema field %q is unresolved", field.Name)
		}
	}
	if _, ok := repositoryReviewAutomationCollectionField(automation, "unknown"); ok {
		t.Fatal("unknown automation field resolved")
	}

	runFinding := repositoryReviewRunFindingSummary{
		ID: "rdf_fields", Repository: "owner/repo", Path: "pkg/service.go", Symbol: "Save",
		Severity: "high", Title: "Lost update", Status: repoaudit.FindingOpen,
		RunFindingStatus: repositoryReviewRunFindingAssociatedExisting, Association: "existing",
		Contributors: []string{"reviewer-one"},
		CreatedAt:    now.Add(-time.Hour), UpdatedAt: now,
	}
	for _, field := range repositoryReviewRunFindingCollectionSchema.Fields {
		if _, ok := repositoryReviewRunFindingCollectionField(runFinding, field.Name); !ok {
			t.Fatalf("run finding schema field %q is unresolved", field.Name)
		}
	}
	if _, ok := repositoryReviewRunFindingCollectionField(runFinding, "unknown"); ok {
		t.Fatal("unknown run finding field resolved")
	}
	runQuery, err := collectionquery.Parse(
		`severity IN (high, critical) AND contributors ~ reviewer ORDER BY severity DESC`,
		repositoryReviewRunFindingCollectionSchema,
	)
	if err != nil {
		t.Fatal(err)
	}
	runContext := repositoryReviewCollectionCursorContext("findings", "rra_fields", "current")
	runPage, err := collectionquery.Paginate(
		[]repositoryReviewRunFindingSummary{runFinding}, runQuery, "", 50, now,
		repositoryReviewRunFindingPageOptions(runContext),
	)
	if err != nil || runPage.Total != 1 {
		t.Fatalf("run typed query page=%#v err=%v", runPage, err)
	}

	repositoryFinding := repositoryReviewRepositoryFindingCollectionSummary{
		ID: "rrf_fields", Repository: "owner/repo", CanonicalTitle: "Lost update",
		CanonicalSeverity: "critical", Path: "pkg/service.go", Symbol: "Save",
		MatchState: repoaudit.RepositoryMatchKnown, Lifecycle: repoaudit.RepositoryFindingOpen,
		Issue:           repositoryReviewRepositoryFindingIssueSummary{State: repoaudit.RepositoryFindingIssueOpen},
		ValidationState: repoaudit.RepositoryValidationConfirmed,
		OccurrenceCount: 2, FoundCommitCount: 3,
		CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now,
	}
	for _, field := range repositoryReviewRepositoryFindingCollectionSchema.Fields {
		if _, ok := repositoryReviewRepositoryFindingCollectionField(repositoryFinding, field.Name); !ok {
			t.Fatalf("repository finding schema field %q is unresolved", field.Name)
		}
	}
	if _, ok := repositoryReviewRepositoryFindingCollectionField(repositoryFinding, "unknown"); ok {
		t.Fatal("unknown repository finding field resolved")
	}
	repositoryQuery, err := collectionquery.Parse(
		`match = known AND lifecycle = open AND issue = open AND validation = confirmed AND occurrences >= 2`,
		repositoryReviewRepositoryFindingCollectionSchema,
	)
	if err != nil {
		t.Fatal(err)
	}
	repositoryPage, err := collectionquery.Paginate(
		[]repositoryReviewRepositoryFindingCollectionSummary{repositoryFinding}, repositoryQuery, "", 50, now,
		repositoryReviewRepositoryFindingPageOptions(
			repositoryReviewCollectionCursorContext("repository-findings", "rra_fields"),
		),
	)
	if err != nil || repositoryPage.Total != 1 {
		t.Fatalf("repository typed query page=%#v err=%v", repositoryPage, err)
	}

	issue := repositoryReviewIssueCollectionSummary{
		ID: "rid_fields", Repository: "owner/repo", FindingCount: 2,
		Origin: repoaudit.IssueDraftOriginAIGenerated, GenerationID: "rrig_fields",
		Publishable: true, Title: "Lost update", State: repoaudit.IssueDraftEditing,
		CreatedAt: now.Add(-3 * time.Hour), UpdatedAt: now,
	}
	for _, field := range repositoryReviewIssueCollectionSchema.Fields {
		if _, ok := repositoryReviewIssueCollectionField(issue, field.Name); !ok {
			t.Fatalf("issue schema field %q is unresolved", field.Name)
		}
	}
	if _, ok := repositoryReviewIssueCollectionField(issue, "unknown"); ok {
		t.Fatal("unknown issue field resolved")
	}
	issueQuery, err := collectionquery.Parse(
		`state = editing AND origin = ai_generated AND publishable = true AND findings >= 2`,
		repositoryReviewIssueCollectionSchema,
	)
	if err != nil {
		t.Fatal(err)
	}
	issuePage, err := collectionquery.Paginate(
		[]repositoryReviewIssueCollectionSummary{issue}, issueQuery, "", 50, now,
		repositoryReviewIssuePageOptions(repositoryReviewCollectionCursorContext("issues", "rra_fields")),
	)
	if err != nil || issuePage.Total != 1 {
		t.Fatalf("issue typed query page=%#v err=%v", issuePage, err)
	}
}

func repositoryReviewCollectionFindingForTest(
	id string,
	severity string,
	now time.Time,
) repositoryReviewRunFindingSummary {
	return repositoryReviewRunFindingSummary{
		ID: id, Repository: "owner/repo", Path: id + ".go", Severity: severity,
		Title: id, Status: repoaudit.FindingOpen,
		RunFindingStatus: repositoryReviewRunFindingPending, Association: "unassociated",
		Contributors: []string{}, CreatedAt: now, UpdatedAt: now,
	}
}
