package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

func TestRepositoryReviewRemainingGatewayPublicationBranches(t *testing.T) {
	seedPublishable := func(t *testing.T) (*Handler, *http.ServeMux, string, repoaudit.RepositoryState, repoaudit.RepositoryReviewAutomation, repoaudit.IssueDraft) {
		t.Helper()
		handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
		state := seedRepositoryReviewAPIState(t, workspace)
		state = completeRepositoryReviewAPIMappingJobs(t, workspace, state)
		automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
		store := repoaudit.NewStore(workspace)
		_, draft, _, err := store.ReserveIssueGeneration(repoaudit.IssueGenerationRequest{
			Repository: state.Repository, FindingID: state.Findings[0].ID,
			GenerationID: "rrig_gateway_remaining", ResolvedInstructions: repositoryReviewDefaultIssueInstructions,
			InstructionsMode: repoaudit.IssueDraftInstructionsDefault,
			GeneratorModel:   "cheap", GeneratorAccount: "api",
		})
		if err != nil {
			t.Fatal(err)
		}
		state, draft, err = store.CompleteIssueGeneration(
			state.Repository, draft.ID, draft.AttemptGenerationID,
			"Preview", "Evidence", []string{"bug"}, "",
		)
		if err != nil {
			t.Fatal(err)
		}
		return handler, mux, workspace, state, automation, draft
	}

	t.Run("cross-site single and batch", func(t *testing.T) {
		_, mux, _, state, automation, draft := seedPublishable(t)
		for _, target := range []string{
			baseAutomationIssuePath(automation.ID, draft.ID) + "/publish",
			"/api/repository-reviews/automations/" + automation.ID + "/issues/publish",
		} {
			request := httptest.NewRequest(
				http.MethodPost, "http://launcher.local"+target,
				strings.NewReader(`{"expected_version":1,"confirmed":true}`),
			)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Sec-Fetch-Site", "cross-site")
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("cross publish %q=%d %s state=%s", target, response.Code, response.Body.String(), state.ID)
			}
		}
	})

	t.Run("unknown outcome", func(t *testing.T) {
		_, mux, _, _, automation, draft := seedPublishable(t)
		installEventProxyStubs(t, func(_ *http.Request, _ time.Duration) (*http.Response, error) {
			return eventUpstreamResponse(
				http.StatusAccepted,
				`{"outcome":"unknown","draft":{"id":"`+draft.ID+`","state":"unknown"}}`,
			), nil
		})
		response := repositoryReviewAutomationMutation(
			t, mux, http.MethodPost,
			baseAutomationIssuePath(automation.ID, draft.ID)+"/publish",
			map[string]any{"expected_version": draft.Version, "confirmed": true},
		)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"outcome":"unknown"`) {
			t.Fatalf("unknown single publish=%d %s", response.Code, response.Body.String())
		}
	})

	t.Run("draft disappears after gateway", func(t *testing.T) {
		_, mux, workspace, state, automation, draft := seedPublishable(t)
		store := repoaudit.NewStore(workspace)
		installEventProxyStubs(t, func(_ *http.Request, _ time.Duration) (*http.Response, error) {
			_, _ = store.DeleteIssueDraft(state.Repository, draft.ID, draft.Version)
			return eventUpstreamResponse(http.StatusOK, `{"draft":{"id":"`+draft.ID+`","state":"posted"}}`), nil
		})
		response := repositoryReviewAutomationMutation(
			t, mux, http.MethodPost,
			baseAutomationIssuePath(automation.ID, draft.ID)+"/publish",
			map[string]any{"expected_version": draft.Version, "confirmed": true},
		)
		if response.Code != http.StatusNotFound {
			t.Fatalf("disappeared single publish=%d %s", response.Code, response.Body.String())
		}
	})

	t.Run("ledger disappears after gateway", func(t *testing.T) {
		_, mux, workspace, _, automation, draft := seedPublishable(t)
		installEventProxyStubs(t, func(_ *http.Request, _ time.Duration) (*http.Response, error) {
			databasePath := filepath.Join(
				workspace, "repository_reviews", "repository-reviews.db",
			)
			for _, suffix := range []string{"", "-wal", "-shm"} {
				_ = os.Remove(databasePath + suffix)
			}
			return eventUpstreamResponse(http.StatusOK, `{"draft":{"id":"`+draft.ID+`","state":"posted"}}`), nil
		})
		response := repositoryReviewAutomationMutation(
			t, mux, http.MethodPost,
			baseAutomationIssuePath(automation.ID, draft.ID)+"/publish",
			map[string]any{"expected_version": draft.Version, "confirmed": true},
		)
		if response.Code != http.StatusNotFound {
			t.Fatalf("missing ledger single publish=%d %s", response.Code, response.Body.String())
		}
	})

	t.Run("ledger corrupts after gateway", func(t *testing.T) {
		_, mux, workspace, _, automation, draft := seedPublishable(t)
		installEventProxyStubs(t, func(_ *http.Request, _ time.Duration) (*http.Response, error) {
			root := filepath.Join(workspace, "repository_reviews")
			_ = os.RemoveAll(root)
			_ = os.WriteFile(root, []byte("not a directory"), 0o600)
			return eventUpstreamResponse(http.StatusOK, `{"draft":{"id":"`+draft.ID+`","state":"posted"}}`), nil
		})
		response := repositoryReviewAutomationMutation(
			t, mux, http.MethodPost,
			baseAutomationIssuePath(automation.ID, draft.ID)+"/publish",
			map[string]any{"expected_version": draft.Version, "confirmed": true},
		)
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("corrupt ledger single publish=%d %s", response.Code, response.Body.String())
		}
	})
}
