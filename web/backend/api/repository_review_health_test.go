package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

func TestRepositoryReviewFindingHealthUsesExactScopesAndDirectStatuses(t *testing.T) {
	now := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
	automation := repoaudit.RepositoryReviewAutomation{
		CampaignID: "rrc_selected", UpdatedAt: now.Add(-time.Hour),
	}
	selectedFinding := func(id string) repoaudit.Finding {
		return repoaudit.Finding{
			ID: id, CampaignID: automation.CampaignID, Status: repoaudit.FindingOpen,
			UpdatedAt: now,
		}
	}
	associatedNew := selectedFinding("rdf_new")
	associatedNew.RepositoryFindingID = "rrf_shared"
	associatedNew.RepositoryMatchState = repoaudit.RepositoryMatchKnown
	associatedExisting := selectedFinding("rdf_existing")
	associatedExisting.RepositoryFindingID = "rrf_shared"
	associatedExisting.RepositoryMatchState = repoaudit.RepositoryMatchKnown
	needsReview := selectedFinding("rdf_review")
	needsReview.RepositoryFindingID = "rrf_provisional"
	needsReview.RepositoryMatchState = repoaudit.RepositoryMatchProvisional
	pending := selectedFinding("rdf_pending")
	processing := selectedFinding("rdf_processing")
	failed := selectedFinding("rdf_failed")
	unrelated := selectedFinding("rdf_other_campaign")
	unrelated.CampaignID = "rrc_other"
	compatibilityPending := selectedFinding("rfn_compatibility_pending")
	compatibilityPending.DeduplicationPending = true
	deduplicated := func(finding repoaudit.Finding) repoaudit.DeduplicatedReviewFinding {
		return repoaudit.DeduplicatedReviewFinding{
			ID: finding.ID, CampaignID: finding.CampaignID,
			RepositoryFindingID:  finding.RepositoryFindingID,
			RepositoryMatchState: finding.RepositoryMatchState,
		}
	}

	state := repoaudit.RepositoryState{
		Findings: []repoaudit.Finding{
			associatedNew, associatedExisting, needsReview, pending, processing, failed,
			unrelated, compatibilityPending,
		},
		DeduplicatedFindings: []repoaudit.DeduplicatedReviewFinding{
			deduplicated(associatedNew), deduplicated(associatedExisting),
			deduplicated(needsReview), deduplicated(pending), deduplicated(processing),
			deduplicated(failed), deduplicated(unrelated),
		},
		RepositoryFindings: []repoaudit.RepositoryFinding{
			{
				ID: "rrf_shared", MatchState: repoaudit.RepositoryMatchKnown,
				ReviewFindingIDs: []string{associatedNew.ID, associatedExisting.ID},
				UpdatedAt:        now.Add(time.Minute),
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
			{State: repoaudit.RawFindingDeduplicationCompleted, UpdatedAt: now.Add(4 * time.Minute)},
			{State: repoaudit.RawFindingDeduplicationCompleted, UpdatedAt: now.Add(5 * time.Minute)},
		},
		HistoricalDeduplication: repoaudit.HistoricalDeduplicationReplay{
			Status: repoaudit.HistoricalDeduplicationCompleted, UpdatedAt: now.Add(4 * time.Minute),
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
		Total: 5, Pending: 1, Processing: 1, Failed: 1, Completed: 2,
	}) {
		t.Fatalf("processing health=%#v", health.FindingsProcessing)
	}
	if health.RunFindings.Unrepresented != health.RunFindings.Pending+
		health.RunFindings.Processing+health.RunFindings.Failed {
		t.Fatalf("unrepresented was not a direct status sum: %#v", health.RunFindings)
	}
	if health.HistoricalConsolidation.Status != "completed" ||
		health.HistoricalConsolidation.Required || health.HistoricalConsolidation.Retryable {
		t.Fatalf("historical health=%#v", health.HistoricalConsolidation)
	}
	if want := now.Add(5 * time.Minute); !health.UpdatedAt.Equal(want) {
		t.Fatalf("updated_at=%s want=%s", health.UpdatedAt, want)
	}
}

func TestRepositoryReviewHistoricalConsolidationHealthNormalization(t *testing.T) {
	tests := []struct {
		name      string
		replay    repoaudit.HistoricalDeduplicationReplay
		status    string
		retryable bool
	}{
		{name: "inactive", status: "not_required"},
		{
			name: "inactive pending", replay: repoaudit.HistoricalDeduplicationReplay{
				Status: repoaudit.HistoricalDeduplicationPending,
			}, status: "not_required",
		},
		{
			name: "completed remains visible", replay: repoaudit.HistoricalDeduplicationReplay{
				Status: repoaudit.HistoricalDeduplicationCompleted,
			}, status: "completed",
		},
		{
			name: "legacy required", replay: repoaudit.HistoricalDeduplicationReplay{
				Required: true,
			}, status: "pending",
		},
		{
			name: "pending", replay: repoaudit.HistoricalDeduplicationReplay{
				Required: true, Status: repoaudit.HistoricalDeduplicationPending,
			}, status: "pending",
		},
		{
			name: "replaying", replay: repoaudit.HistoricalDeduplicationReplay{
				Required: true, Status: repoaudit.HistoricalDeduplicationReplaying,
			}, status: "replaying",
		},
		{
			name: "merging", replay: repoaudit.HistoricalDeduplicationReplay{
				Required: true, Status: repoaudit.HistoricalDeduplicationMerging,
			}, status: "merging",
		},
		{
			name: "failed", replay: repoaudit.HistoricalDeduplicationReplay{
				Required: true, Status: repoaudit.HistoricalDeduplicationFailed,
			}, status: "failed", retryable: true,
		},
		{
			name: "required completed", replay: repoaudit.HistoricalDeduplicationReplay{
				Required: true, Status: repoaudit.HistoricalDeduplicationCompleted,
			}, status: "completed",
		},
		{
			name: "future state is normalized", replay: repoaudit.HistoricalDeduplicationReplay{
				Required: true, Status: repoaudit.HistoricalDeduplicationReplayStatus("future"),
			}, status: "failed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := repositoryReviewHistoricalConsolidationHealthFor(test.replay)
			if got.Required != test.replay.Required || got.Status != test.status ||
				got.Retryable != test.retryable {
				t.Fatalf("historical health=%#v", got)
			}
		})
	}
}

func TestRepositoryReviewFindingHealthRoute(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewAPIState(t, workspace)
	state = seedRepositoryReviewDeduplicationAPIState(
		t, workspace, state, "rrc_health_route",
	)
	state = completeRepositoryReviewAPIMappingJobs(t, workspace, state)
	automation := seedRepositoryReviewDetailAutomation(
		t, handler, state.Repository, state.Runs[0].ID,
	)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/api/repository-reviews/automations/"+automation.ID+"/finding-health",
		nil,
	))
	var health repositoryReviewFindingHealth
	if err := json.Unmarshal(response.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || health.RunFindings.Total != 1 ||
		health.RunFindings.AssociatedNew != 1 || health.RunFindings.Unrepresented != 0 ||
		health.HistoricalConsolidation.Status != "not_required" || health.UpdatedAt.IsZero() {
		t.Fatalf("health status=%d payload=%#v body=%s", response.Code, health, response.Body.String())
	}

	missing := httptest.NewRecorder()
	mux.ServeHTTP(missing, httptest.NewRequest(
		http.MethodGet,
		"/api/repository-reviews/automations/rra_missing/finding-health",
		nil,
	))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing health status=%d body=%s", missing.Code, missing.Body.String())
	}
}
