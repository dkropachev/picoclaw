package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

func TestRepositoryReviewV6RawProcessingHealthAndRetry(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewAPIStateWithProcessing(t, workspace, false)
	automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
	base := "/api/repository-reviews/automations/" + automation.ID
	rawID := state.RawFindings[0].ID

	for _, path := range []string{
		base + "/raw-findings?query=ALL&limit=1",
		base + "/raw-findings/" + rawID,
		base + "/findings-processing?query=ALL&limit=1",
		base + "/findings-processing/sources/" + rawID,
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}

	store := repoaudit.NewSQLiteStore(workspace)
	jobID := state.DeduplicationJobs[0].ID
	for attempt := 0; attempt < repoaudit.DeduplicationAttemptLimit; attempt++ {
		_, claim, claimed, err := store.ClaimDeduplicationJob(state.Repository, jobID, time.Minute)
		if err != nil || !claimed {
			t.Fatalf("claim attempt %d claimed=%v err=%v", attempt, claimed, err)
		}
		if _, _, _, err = store.FailDeduplicationJob(
			state.Repository, jobID, claim.Job.LeaseID, errors.New("test failure"),
		); err != nil {
			t.Fatal(err)
		}
	}

	health := httptest.NewRecorder()
	mux.ServeHTTP(health, httptest.NewRequest(http.MethodGet, base+"/finding-health", nil))
	var healthBody struct {
		Processing struct {
			Total  int `json:"total"`
			Failed int `json:"failed"`
		} `json:"findings_processing"`
	}
	if health.Code != http.StatusOK || json.Unmarshal(health.Body.Bytes(), &healthBody) != nil ||
		healthBody.Processing.Total != 1 || healthBody.Processing.Failed != 1 {
		t.Fatalf("health status=%d body=%s", health.Code, health.Body.String())
	}

	retryRequest := httptest.NewRequest(
		http.MethodPost, base+"/findings-processing/sources/"+rawID+"/retry", strings.NewReader(`{}`),
	)
	setRepositoryReviewMutationHeaders(retryRequest)
	retry := httptest.NewRecorder()
	mux.ServeHTTP(retry, retryRequest)
	if retry.Code != http.StatusAccepted || !strings.Contains(retry.Body.String(), `"deduplication_state":"pending"`) {
		t.Fatalf("retry status=%d body=%s", retry.Code, retry.Body.String())
	}
}

func TestRepositoryReviewV6LegacyRouteTombstones(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewAPIState(t, workspace)
	automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
	base := "/api/repository-reviews/automations/" + automation.ID
	checks := []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, base + "/run-findings", http.StatusNotFound},
		{http.MethodGet, base + "/report", http.StatusNotFound},
		{http.MethodGet, base + "/campaigns/" + automation.CampaignID + "/findings-processing", http.StatusNotFound},
		{http.MethodGet, base + "/historical-deduplication", http.StatusNotFound},
		{http.MethodPatch, base + "/findings/" + state.Findings[0].ID, http.StatusMethodNotAllowed},
	}
	for _, check := range checks {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(check.method, check.path, nil))
		if response.Code != check.want {
			t.Fatalf("%s %s status=%d body=%s", check.method, check.path, response.Code, response.Body.String())
		}
	}
}
