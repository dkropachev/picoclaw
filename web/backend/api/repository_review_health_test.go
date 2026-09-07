package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

func TestRepositoryReviewCanonicalFindingHealthCoversEveryState(t *testing.T) {
	now := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
	automation := repoaudit.RepositoryReviewAutomation{
		CampaignID: "rrc_health", UpdatedAt: now.Add(-time.Hour),
	}
	finding := func(id string) repoaudit.Finding {
		return repoaudit.Finding{
			ID: id, CampaignID: automation.CampaignID, Status: repoaudit.FindingOpen, UpdatedAt: now,
		}
	}
	associatedNew := finding("rdf_new")
	associatedNew.RepositoryFindingID = "rrf_shared"
	associatedNew.RepositoryMatchState = repoaudit.RepositoryMatchKnown
	associatedExisting := finding("rdf_existing")
	associatedExisting.RepositoryFindingID = "rrf_shared"
	associatedExisting.RepositoryMatchState = repoaudit.RepositoryMatchKnown
	needsReview := finding("rdf_review")
	needsReview.RepositoryFindingID = "rrf_provisional"
	needsReview.RepositoryMatchState = repoaudit.RepositoryMatchProvisional
	pending := finding("rdf_pending")
	processing := finding("rdf_processing")
	failed := finding("rdf_failed")
	unrelated := finding("rdf_other")
	unrelated.CampaignID = "rrc_other"
	state := repoaudit.RepositoryState{
		Findings: []repoaudit.Finding{
			associatedNew, associatedExisting, needsReview, pending, processing, failed, unrelated,
		},
		RepositoryFindings: []repoaudit.RepositoryFinding{
			{
				ID: "rrf_shared", MatchState: repoaudit.RepositoryMatchKnown,
				ReviewFindingIDs: []string{associatedNew.ID, associatedExisting.ID}, UpdatedAt: now.Add(time.Minute),
			},
			{
				ID: "rrf_provisional", MatchState: repoaudit.RepositoryMatchProvisional,
				ReviewFindingIDs: []string{needsReview.ID}, UpdatedAt: now.Add(2 * time.Minute),
			},
			{
				ID: "rrf_attention", MatchState: repoaudit.RepositoryMatchKnown,
				ValidationState: repoaudit.RepositoryValidationFailed,
				Issue:           repoaudit.RepositoryFindingIssueAssociation{Conflict: true},
				UpdatedAt:       now.Add(3 * time.Minute),
			},
		},
		MappingJobs: []repoaudit.RepositoryMappingJob{
			{ReviewFindingID: pending.ID, State: repoaudit.RepositoryMappingPending},
			{ReviewFindingID: processing.ID, State: repoaudit.RepositoryMappingRunning},
			{
				ReviewFindingID: failed.ID, State: repoaudit.RepositoryMappingPending,
				Attempts: repoaudit.RepositoryRunFindingStatusAttemptLimit,
			},
		},
		RawFindings: []repoaudit.RawReviewFinding{
			{State: repoaudit.RawFindingDeduplicationPending, UpdatedAt: now.Add(time.Minute)},
			{State: repoaudit.RawFindingDeduplicationRunning, UpdatedAt: now.Add(2 * time.Minute)},
			{State: repoaudit.RawFindingDeduplicationFailed, UpdatedAt: now.Add(3 * time.Minute)},
			{State: repoaudit.RawFindingDeduplicationCompleted, UpdatedAt: now.Add(5 * time.Minute)},
		},
		UpdatedAt: now.Add(4 * time.Minute),
	}

	health := repositoryReviewFindingHealthFor(automation, state)
	if health.RunFindings != (repositoryReviewRunFindingHealth{
		Total: 6, Pending: 1, Processing: 1, Failed: 1, NeedsReview: 1,
		AssociatedNew: 1, AssociatedExisting: 1, Unrepresented: 3,
	}) {
		t.Fatalf("run finding health=%#v", health.RunFindings)
	}
	if health.RepositoryFindings != (repositoryReviewRepositoryFindingHealth{
		Total: 3, Provisional: 1, ValidationFailed: 1, IssueConflicts: 1,
	}) {
		t.Fatalf("repository finding health=%#v", health.RepositoryFindings)
	}
	if health.FindingsProcessing != (repositoryReviewFindingsProcessingHealth{
		Total: 4, Pending: 1, Processing: 1, Failed: 1, Completed: 1,
	}) {
		t.Fatalf("processing health=%#v", health.FindingsProcessing)
	}
	if !health.UpdatedAt.Equal(now.Add(5 * time.Minute)) {
		t.Fatalf("health update=%s", health.UpdatedAt)
	}
}

func TestRepositoryReviewCanonicalFindingHealthRouteErrors(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewAPIState(t, workspace)
	automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
	for target, want := range map[string]int{
		"/api/repository-reviews/automations/" + automation.ID + "/finding-health": http.StatusOK,
		"/api/repository-reviews/automations/rra_missing/finding-health":           http.StatusNotFound,
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != want {
			t.Fatalf("health %q=%d want=%d body=%s", target, response.Code, want, response.Body.String())
		}
	}
}
