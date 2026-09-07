package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

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
	contextID := repositoryReviewCollectionCursorContext("findings", automation.ID)
	if _, err := repositoryReviewCollectionCursorItemID("bad", "rdf"); err == nil {
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
