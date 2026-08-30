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
	state := seedRepositoryReviewGenerationFindings(t, workspace, 4)
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

func TestRepositoryReviewRunFindingsSurviveFailedHistoricalDeduplication(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewAPIState(t, workspace)
	want := state.Findings[0]
	state.RawFindings = nil
	state.DeduplicatedFindings = nil
	state.DeduplicationJobs = nil
	state.FindingsProcessing = repoaudit.FindingsProcessingCounters{}
	state.HistoricalDeduplication = repoaudit.HistoricalDeduplicationReplay{
		Required:  true,
		Status:    repoaudit.HistoricalDeduplicationFailed,
		Attempts:  3,
		Error:     "Historical deduplication failed.",
		UpdatedAt: time.Now().UTC(),
	}
	persistRepositoryReviewAdditionalCoverageState(t, workspace, state)
	automation := seedRepositoryReviewDetailAutomation(
		t, handler, state.Repository, state.Runs[0].ID,
	)

	query := url.QueryEscape("ALL ORDER BY severity DESC, updated DESC")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/api/repository-reviews/automations/"+automation.ID+"/run-findings?query="+query,
		nil,
	))
	if response.Code != http.StatusOK {
		t.Fatalf("run findings status=%d body=%s", response.Code, response.Body.String())
	}
	var page struct {
		Findings       []repositoryReviewRunFindingSummary `json:"findings"`
		Total          int                                 `json:"total"`
		CanonicalQuery string                              `json:"canonical_query"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Findings) != 1 || page.Findings[0].ID != want.ID ||
		page.Findings[0].Title != want.Title || page.Findings[0].Severity != want.Severity ||
		page.CanonicalQuery != "ALL ORDER BY severity DESC, updated DESC" {
		t.Fatalf("run findings page=%#v body=%s", page, response.Body.String())
	}

	detailResponse := httptest.NewRecorder()
	mux.ServeHTTP(detailResponse, httptest.NewRequest(
		http.MethodGet,
		"/api/repository-reviews/automations/"+automation.ID+"/run-findings/"+want.ID,
		nil,
	))
	if detailResponse.Code != http.StatusOK {
		t.Fatalf(
			"run finding detail status=%d body=%s",
			detailResponse.Code,
			detailResponse.Body.String(),
		)
	}
	var detail struct {
		Finding repositoryReviewRunFindingProjection `json:"finding"`
	}
	if err := json.Unmarshal(detailResponse.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Finding.ID != want.ID {
		t.Fatalf("run finding detail=%#v body=%s", detail, detailResponse.Body.String())
	}
}

func TestRepositoryReviewIssueCollectionGenerationCursorAndLegacyFirstPage(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewGenerationFindings(t, workspace, 4)
	state = completeRepositoryReviewAPIMappingJobs(t, workspace, state)
	automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
	store := repoaudit.NewStore(workspace)
	generations := []string{"rrig_first", "rrig_first", "rrig_second", "rrig_second"}
	for index, findingID := range state.Runs[0].FindingIDs {
		_, draft, reserved, err := store.ReserveIssueGeneration(repoaudit.IssueGenerationRequest{
			Repository: state.Repository, FindingID: findingID, GenerationID: generations[index],
			ResolvedInstructions: repositoryReviewDefaultIssueInstructions,
			InstructionsMode:     repoaudit.IssueDraftInstructionsDefault,
			GeneratorModel:       "cheap", GeneratorAccount: "api",
		})
		if err != nil || !reserved {
			t.Fatalf("reserve issue %d: reserved=%v err=%v", index, reserved, err)
		}
		if _, _, err = store.CompleteIssueGeneration(
			state.Repository, draft.ID, draft.GenerationID,
			"Issue preview", "private issue body", []string{"bug"}, "",
		); err != nil {
			t.Fatal(err)
		}
	}

	base := "/api/repository-reviews/automations/" + automation.ID + "/issues"
	legacy := httptest.NewRecorder()
	mux.ServeHTTP(legacy, httptest.NewRequest(
		http.MethodGet, base+"?generation_id=rrig_first&limit=1", nil,
	))
	if legacy.Code != http.StatusOK || !strings.Contains(legacy.Body.String(), `"offset":0`) ||
		!strings.Contains(legacy.Body.String(), `"next_offset":1`) ||
		!strings.Contains(legacy.Body.String(), `"body":"private issue body"`) ||
		strings.Contains(legacy.Body.String(), `"canonical_query"`) {
		t.Fatalf("legacy first page status=%d body=%s", legacy.Code, legacy.Body.String())
	}

	query := url.QueryEscape("ALL ORDER BY updated DESC")
	first := httptest.NewRecorder()
	mux.ServeHTTP(first, httptest.NewRequest(
		http.MethodGet, base+"?query="+query+"&generation_id=rrig_first&limit=1", nil,
	))
	if first.Code != http.StatusOK {
		t.Fatalf("issue collection status=%d body=%s", first.Code, first.Body.String())
	}
	var firstPage struct {
		Issues         []repositoryReviewIssueCollectionSummary `json:"issues"`
		Total          int                                      `json:"total"`
		NextCursor     string                                   `json:"next_cursor"`
		CanonicalQuery string                                   `json:"canonical_query"`
		GenerationID   string                                   `json:"generation_id"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstPage); err != nil {
		t.Fatal(err)
	}
	for _, detailOnly := range []string{
		"finding_ids", "generation_error", "labels", "external_id", "external_url",
		"external_state", "body", "resolved_instructions",
	} {
		if strings.Contains(first.Body.String(), `"`+detailOnly+`"`) {
			t.Fatalf("issue summary leaked detail field %q: %s", detailOnly, first.Body.String())
		}
	}
	if firstPage.Total != 2 || len(firstPage.Issues) != 1 || firstPage.NextCursor == "" ||
		firstPage.GenerationID != "rrig_first" || firstPage.CanonicalQuery != "ALL ORDER BY updated DESC" ||
		firstPage.Issues[0].FindingCount != 1 || strings.Contains(first.Body.String(), `"offset"`) {
		t.Fatalf("issue collection page=%#v body=%s", firstPage, first.Body.String())
	}

	wrongGeneration := httptest.NewRecorder()
	mux.ServeHTTP(wrongGeneration, httptest.NewRequest(
		http.MethodGet,
		base+"?query="+query+"&generation_id=rrig_second&cursor="+url.QueryEscape(firstPage.NextCursor),
		nil,
	))
	if wrongGeneration.Code != http.StatusBadRequest ||
		!strings.Contains(wrongGeneration.Body.String(), `"code":"invalid_cursor"`) {
		t.Fatalf("wrong-generation cursor status=%d body=%s", wrongGeneration.Code, wrongGeneration.Body.String())
	}

	for _, malformedGeneration := range []string{"%FF", "%00"} {
		malformed := httptest.NewRecorder()
		mux.ServeHTTP(malformed, httptest.NewRequest(
			http.MethodGet,
			base+"?query="+query+"&generation_id="+malformedGeneration,
			nil,
		))
		if malformed.Code != http.StatusBadRequest ||
			!strings.Contains(malformed.Body.String(), `"code":"invalid_generation_id"`) {
			t.Fatalf(
				"malformed generation %q status=%d body=%s",
				malformedGeneration,
				malformed.Code,
				malformed.Body.String(),
			)
		}
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
	contextID := repositoryReviewCollectionCursorContext("run-findings", "rra_test", "current")
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

func TestRepositoryReviewCollectionSchemasResolveEveryFieldAndTypedPredicates(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	runFinding := repositoryReviewRunFindingSummary{
		ID: "rfn_fields", Repository: "owner/repo", Path: "pkg/service.go", Symbol: "Save",
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
	runQuery, err := collectionquery.Parse(
		`severity IN (high, critical) AND contributors ~ reviewer AND updated >= 2026-08-28T00:00:00Z ORDER BY severity DESC`,
		repositoryReviewRunFindingCollectionSchema,
	)
	if err != nil {
		t.Fatal(err)
	}
	runContext := repositoryReviewCollectionCursorContext("run-findings", "rra_fields", "current")
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
	repositoryQuery, err := collectionquery.Parse(
		`match = known AND lifecycle IN (open, regressed) AND issue = open AND validation = confirmed AND occurrences >= 2 ORDER BY commits DESC`,
		repositoryReviewRepositoryFindingCollectionSchema,
	)
	if err != nil {
		t.Fatal(err)
	}
	repositoryContext := repositoryReviewCollectionCursorContext("repository-findings", "rra_fields")
	repositoryPage, err := collectionquery.Paginate(
		[]repositoryReviewRepositoryFindingCollectionSummary{repositoryFinding},
		repositoryQuery, "", 50, now,
		repositoryReviewRepositoryFindingPageOptions(repositoryContext),
	)
	if err != nil || repositoryPage.Total != 1 {
		t.Fatalf("repository typed query page=%#v err=%v", repositoryPage, err)
	}

	issue := repositoryReviewIssueCollectionSummary{
		ID: "rid_fields", Repository: "owner/repo",
		FindingCount: 2, Origin: repoaudit.IssueDraftOriginAIGenerated, GenerationID: "rrig_fields",
		Canonical: true, Publishable: true, Title: "Lost update", State: repoaudit.IssueDraftEditing,
		CreatedAt: now.Add(-3 * time.Hour), UpdatedAt: now,
	}
	for _, field := range repositoryReviewIssueCollectionSchema.Fields {
		if _, ok := repositoryReviewIssueCollectionField(issue, field.Name); !ok {
			t.Fatalf("issue schema field %q is unresolved", field.Name)
		}
	}
	issueQuery, err := collectionquery.Parse(
		`state = editing AND origin = ai_generated AND canonical = true AND publishable = true AND findings >= 2 ORDER BY updated DESC`,
		repositoryReviewIssueCollectionSchema,
	)
	if err != nil {
		t.Fatal(err)
	}
	issueContext := repositoryReviewCollectionCursorContext("issues", "rra_fields", "rrig_fields")
	issuePage, err := collectionquery.Paginate(
		[]repositoryReviewIssueCollectionSummary{issue}, issueQuery, "", 50, now,
		repositoryReviewIssuePageOptions(issueContext),
	)
	if err != nil || issuePage.Total != 1 {
		t.Fatalf("issue typed query page=%#v err=%v", issuePage, err)
	}
}

func TestRepositoryReviewCollectionBoundaryCoverage(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewAPIState(t, workspace)
	automation := seedRepositoryReviewDetailAutomation(
		t,
		handler,
		state.Repository,
		state.Runs[0].ID,
	)
	base := "/api/repository-reviews/automations/"

	for _, path := range []string{
		base + "rra_missing/findings?query=ALL",
		base + "rra_missing/repository-findings?query=ALL",
		base + "rra_missing/issues?query=ALL",
		base + "rra_missing/repository-findings/rrf_missing",
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("missing collection path %s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}

	for _, test := range []struct {
		path string
		code string
	}{
		{
			path: base + automation.ID + "/findings?query=ALL&cursor=bad",
			code: "invalid_cursor",
		},
		{
			path: base + automation.ID + "/repository-findings?query=unknown%20%3D%20x",
			code: "invalid_query",
		},
		{
			path: base + automation.ID + "/issues?query=ALL&query=ALL",
			code: "invalid_collection_request",
		},
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if response.Code != http.StatusBadRequest ||
			!strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
			t.Fatalf("boundary path %s status=%d body=%s", test.path, response.Code, response.Body.String())
		}
	}

	repositorySummary := projectRepositoryReviewRepositoryFindingCollectionSummary(
		repoaudit.RepositoryFinding{
			ID: "rrf_empty", Repository: "owner/repo", CanonicalTitle: "Empty history",
			CanonicalSeverity: "low", MatchState: repoaudit.RepositoryMatchKnown,
			Lifecycle: repoaudit.RepositoryFindingOpen,
		},
	)
	if repositorySummary.Path != "" ||
		repositorySummary.Issue.State != repoaudit.RepositoryFindingIssueNone {
		t.Fatalf("empty repository summary=%#v", repositorySummary)
	}
	issueSummary := projectRepositoryReviewIssueCollectionSummary(
		repoaudit.RepositoryState{},
		repoaudit.IssueDraft{ID: "rid_legacy", Repository: "owner/repo"},
	)
	if issueSummary.Origin != repoaudit.IssueDraftOriginLegacy {
		t.Fatalf("legacy issue summary=%#v", issueSummary)
	}

	if _, ok := repositoryReviewRunFindingCollectionField(
		repositoryReviewRunFindingSummary{},
		"unknown",
	); ok {
		t.Fatal("unknown run-finding field resolved")
	}
	if _, ok := repositoryReviewRepositoryFindingCollectionField(
		repositoryReviewRepositoryFindingCollectionSummary{},
		"unknown",
	); ok {
		t.Fatal("unknown repository-finding field resolved")
	}
	if _, ok := repositoryReviewIssueCollectionField(
		repositoryReviewIssueCollectionSummary{},
		"unknown",
	); ok {
		t.Fatal("unknown issue field resolved")
	}
	if repositoryReviewSeverityRank("medium") != 2 ||
		repositoryReviewSeverityRank("unknown") != 0 {
		t.Fatal("severity ranks were not complete")
	}
	for status, expected := range map[repositoryReviewRunFindingStatus]string{
		repositoryReviewRunFindingAssociatedNew:      "new",
		repositoryReviewRunFindingAssociatedExisting: "existing",
		repositoryReviewRunFindingNeedsReview:        "needs_review",
		repositoryReviewRunFindingPending:            "unassociated",
	} {
		if got := repositoryReviewRunFindingAssociation(status); got != expected {
			t.Fatalf("association %s=%s, want %s", status, got, expected)
		}
	}
	contributors := repositoryReviewFindingContributors(repoaudit.Finding{
		Models: []string{"model-b", "", "REVIEWER-A"},
		Observations: []repoaudit.FindingObservation{
			{Reviewer: " reviewer-a ", Model: "ignored"},
			{Model: "model-c"},
			{Reviewer: " ", Model: ""},
		},
	})
	if strings.Join(contributors, ",") != "model-b,model-c,reviewer-a" {
		t.Fatalf("contributors=%v", contributors)
	}

	contextID := repositoryReviewCollectionCursorContext("run-findings", automation.ID)
	if _, err := repositoryReviewCollectionCursorItemID("bad", "rfn"); err == nil {
		t.Fatal("invalid cursor context was accepted")
	}
	if _, err := repositoryReviewCollectionCursorItemID(
		contextID,
		strings.Repeat("x", (16<<10)+1),
	); err == nil {
		t.Fatal("oversized cursor item identity was accepted")
	}

	response := httptest.NewRecorder()
	if _, _, ok := parseRepositoryReviewIssueCollectionRequest(response, nil); ok ||
		response.Code != http.StatusBadRequest {
		t.Fatalf("nil issue request status=%d", response.Code)
	}
	malformed := httptest.NewRequest(http.MethodGet, base+automation.ID+"/issues", nil)
	malformed.URL.RawQuery = "%zz"
	response = httptest.NewRecorder()
	if _, _, ok := parseRepositoryReviewIssueCollectionRequest(response, malformed); ok ||
		response.Code != http.StatusBadRequest {
		t.Fatalf("malformed issue query status=%d", response.Code)
	}
	if repositoryReviewUsesIssueCollectionRequest(nil) {
		t.Fatal("nil issue request selected collection mode")
	}
	if !repositoryReviewUsesIssueCollectionRequest(malformed) {
		t.Fatal("malformed issue request did not fail into collection validation")
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
