package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

func overwriteRepositoryReviewPayloadForCoverage(
	t *testing.T,
	workspace string,
	query string,
	id string,
) {
	setRepositoryReviewPayloadForCoverage(t, workspace, query, id, []byte(`{}`))
}

func setRepositoryReviewPayloadForCoverage(
	t *testing.T,
	workspace string,
	query string,
	id string,
	payload []byte,
) {
	t.Helper()
	database, err := sql.Open(
		"sqlite", filepath.Join(workspace, "repository_reviews", "repository-reviews.db"),
	)
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	if _, err := database.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	result, execErr := database.Exec(query, payload, id)
	closeErr := database.Close()
	if execErr != nil || closeErr != nil {
		t.Fatalf("overwrite repository-review payload: exec=%v close=%v", execErr, closeErr)
	}
	if changed, rowsErr := result.RowsAffected(); rowsErr != nil || changed != 1 {
		t.Fatalf("overwritten repository-review rows=%d err=%v", changed, rowsErr)
	}
}

func repositoryReviewStatePayloadForCoverage(
	t *testing.T,
	workspace string,
	stateID string,
) []byte {
	t.Helper()
	database, err := sql.Open(
		"sqlite", filepath.Join(workspace, "repository_reviews", "repository-reviews.db"),
	)
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	if _, err := database.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	var payload []byte
	queryErr := database.QueryRow(
		`SELECT payload_json FROM repository_review_states WHERE state_id = ?`,
		stateID,
	).Scan(&payload)
	closeErr := database.Close()
	if queryErr != nil || closeErr != nil {
		t.Fatalf("read repository-review payload: query=%v close=%v", queryErr, closeErr)
	}
	return payload
}

func overwriteRepositoryReviewStatePayloadForCoverage(
	t *testing.T,
	workspace string,
	stateID string,
) {
	t.Helper()
	overwriteRepositoryReviewPayloadForCoverage(
		t,
		workspace,
		`UPDATE repository_review_states SET payload_json = ? WHERE state_id = ?`,
		stateID,
	)
}

func overwriteRepositoryReviewAutomationPayloadForCoverage(
	t *testing.T,
	workspace string,
	automationID string,
) {
	t.Helper()
	overwriteRepositoryReviewPayloadForCoverage(
		t,
		workspace,
		`UPDATE repository_review_automations SET payload_json = ? WHERE automation_id = ?`,
		automationID,
	)
}

func TestRepositoryReviewSQLiteSemanticFailuresReachLateReadBoundaries(t *testing.T) {
	t.Run("automation collection state", func(t *testing.T) {
		handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
		t.Cleanup(handler.Shutdown)
		state := seedRepositoryReviewAPIState(t, workspace)
		_ = seedRepositoryReviewDetailAutomation(
			t, handler, state.Repository, state.Runs[0].ID,
		)
		overwriteRepositoryReviewStatePayloadForCoverage(t, workspace, state.ID)

		response := httptest.NewRecorder()
		mux.ServeHTTP(
			response,
			httptest.NewRequest(http.MethodGet, "/api/repository-reviews/automations", nil),
		)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("semantic collection state=%d %s", response.Code, response.Body.String())
		}
	})

	t.Run("controller automation", func(t *testing.T) {
		handler, _, workspace := newRepositoryReviewAutomationTestHandler(t)
		t.Cleanup(handler.Shutdown)
		store, err := handler.repositoryReviewStore()
		if err != nil {
			t.Fatal(err)
		}
		automation, err := store.CreateAutomation(t.Context(), testRepositoryReviewAutomation())
		if err != nil {
			t.Fatal(err)
		}
		controller := handler.repositoryReviewControllerInstance()
		overwriteRepositoryReviewAutomationPayloadForCoverage(t, workspace, automation.ID)

		if _, _, _, err = controller.repositoryReviewCommitOptions(
			t.Context(), automation.ID,
		); err == nil {
			t.Fatal("semantic automation commit options error=nil")
		}
		if _, err = controller.pauseAutomationForRun(
			t.Context(), automation.ID, automation.Version, "",
		); err == nil {
			t.Fatal("semantic automation pause error=nil")
		}
		if _, err = loadSettledRepositoryReviewPause(
			t.Context(), store, automation.ID,
		); err == nil {
			t.Fatal("semantic settled pause error=nil")
		}
	})

	t.Run("repository state consumers", func(t *testing.T) {
		handler, _, workspace := newRepositoryReviewAutomationTestHandler(t)
		t.Cleanup(handler.Shutdown)
		store, err := handler.repositoryReviewStore()
		if err != nil {
			t.Fatal(err)
		}
		state := seedRepositoryReviewAPIState(t, workspace)
		automation := seedRepositoryReviewDetailAutomation(
			t, handler, state.Repository, state.Runs[0].ID,
		)
		cfg, err := config.LoadConfig(handler.configPath)
		if err != nil {
			t.Fatal(err)
		}
		overwriteRepositoryReviewStatePayloadForCoverage(t, workspace, state.ID)

		if _, err = handler.repositoryReviewControllerInstance().ensureRepositoryReviewCampaign(
			t.Context(), store, cfg, automation, strings.Repeat("a", 40), "start",
		); err == nil {
			t.Fatal("semantic campaign state error=nil")
		}
		if _, err = BackfillRepositoryReviewFileAttributions(
			t.Context(), workspace, automation.ID,
			RepositoryReviewFileAttributionBackfillOptions{},
		); err == nil {
			t.Fatal("semantic attribution state error=nil")
		}
	})

	t.Run("historical profile", func(t *testing.T) {
		handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
		t.Cleanup(handler.Shutdown)
		store, err := handler.repositoryReviewStore()
		if err != nil {
			t.Fatal(err)
		}
		profile := createRepositoryReviewProfileForTest(t, mux, "SQLite profile", "cheap")
		cfg, err := config.LoadConfig(handler.configPath)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(
			filepath.Join(workspace, "repository_reviews", "repository-reviews.db"),
			[]byte("not-sqlite"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		automation := testRepositoryReviewAutomation()
		automation.ProfileID = profile.ID
		if _, _, err = repositoryReviewHistoricalProfileSnapshot(
			t.Context(), handler.repositoryReviewControllerInstance(), store, cfg, automation,
		); err == nil {
			t.Fatal("corrupt historical profile store error=nil")
		}
	})
}

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

func TestRepositoryReviewLegacyInstallRejectsInvalidStagedCommit(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(t, 1, repositoryReviewBackfillRunSpec{
		inspected: []int{0}, occurrences: 1,
	})
	prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
		context.Background(),
		fixture.automation,
		fixture.state,
		repoaudit.NewRepositoryReviewCampaignID(),
		fixture.runStore,
	)
	if err != nil || !prepared.Available {
		t.Fatalf("prepared legacy campaign=%#v err=%v", prepared, err)
	}
	prepared.Request.Coverage.CommitSHA = "invalid"
	if _, _, err = installRepositoryReviewLegacyCampaignAuthority(
		t.Context(), fixture.store, prepared,
	); err == nil {
		t.Fatal("invalid staged commit install error=nil")
	}
}
