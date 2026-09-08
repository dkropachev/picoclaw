package api

import (
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

func TestRepositoryReviewAutomationBatchPublishReportsPartialSelectionFailures(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	state := seedCanonicalRepositoryReviewGenerationFindings(t, workspace, 2)
	state = completeRepositoryReviewAPIMappingJobs(t, workspace, state)
	automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	drafts := make([]repoaudit.IssueDraft, 0, 2)
	for index, finding := range state.Findings {
		findingID := finding.ID
		_, draft, _, reserveErr := store.ReserveIssueGeneration(repoaudit.IssueGenerationRequest{
			Repository: state.Repository, FindingID: findingID,
			GenerationID:         fmt.Sprintf("rrig_publish_%d", index),
			ResolvedInstructions: repositoryReviewDefaultIssueInstructions,
			InstructionsMode:     repoaudit.IssueDraftInstructionsDefault,
			GeneratorModel:       "cheap", GeneratorAccount: "api",
		})
		if reserveErr != nil {
			t.Fatal(reserveErr)
		}
		_, draft, completeErr := store.CompleteIssueGeneration(
			state.Repository, draft.ID, draft.GenerationID,
			fmt.Sprintf("Issue %d", index+1), "Evidence", []string{"bug"}, "",
		)
		if completeErr != nil {
			t.Fatal(completeErr)
		}
		drafts = append(drafts, draft)
	}
	var calls atomic.Int64
	installEventProxyStubs(t, func(_ *http.Request, _ time.Duration) (*http.Response, error) {
		calls.Add(1)
		return eventUpstreamResponse(
			http.StatusOK,
			`{"draft":{"id":"`+drafts[0].ID+`","state":"posted"}}`,
		), nil
	})
	response := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost,
		"/api/repository-reviews/automations/"+automation.ID+"/issues/publish",
		map[string]any{
			"confirmed": true,
			"issues": []map[string]any{
				{"id": drafts[0].ID, "expected_version": drafts[0].Version},
				{"id": drafts[1].ID, "expected_version": drafts[1].Version + 1},
			},
		},
	)
	if response.Code != http.StatusOK || calls.Load() != 1 ||
		!strings.Contains(response.Body.String(), `"outcome":"posted"`) ||
		!strings.Contains(response.Body.String(), `"code":"stale_repository_review"`) {
		t.Fatalf(
			"partial publish status=%d calls=%d body=%s",
			response.Code, calls.Load(), response.Body.String(),
		)
	}
}
