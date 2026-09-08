package api

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

func TestRepositoryReviewValidationMapsLateProfileStoreFailure(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	profile := createRepositoryReviewProfileForTest(t, mux, "Validation profile", "cheap")
	state := seedRepositoryReviewAPIState(t, workspace)
	state = completeRepositoryReviewAPIMappingJobs(t, workspace, state)
	automation := seedRepositoryReviewDetailAutomation(
		t, handler, state.Repository, state.Runs[0].ID,
	)
	materialized, err := repoaudit.MaterializeRepositoryReviewAutomation(profile, automation)
	if err != nil {
		t.Fatal(err)
	}
	automation, err = store.UpdateAutomation(
		t.Context(), automation.ID, automation.Version,
		func(candidate *repoaudit.RepositoryReviewAutomation) error {
			*candidate = materialized
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}
	previousLoad := loadRepositoryReviewLifecycleConfig
	t.Cleanup(func() { loadRepositoryReviewLifecycleConfig = previousLoad })
	loadRepositoryReviewLifecycleConfig = func(string) (*config.Config, error) {
		writeErr := os.WriteFile(
			filepath.Join(workspace, "repository_reviews", "repository-reviews.db"),
			[]byte("not-sqlite"),
			0o600,
		)
		return cfg, writeErr
	}

	response := repositoryReviewAutomationMutation(
		t,
		mux,
		http.MethodPost,
		"/api/repository-reviews/automations/"+automation.ID+
			"/repository-findings/validations",
		map[string]any{"repository_finding_ids": []string{state.RepositoryFindings[0].ID}},
	)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("late validation profile failure=%d %s", response.Code, response.Body.String())
	}
}
