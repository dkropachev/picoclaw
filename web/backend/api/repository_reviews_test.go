package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

func TestRepositoryReviewRoutesSelectDiscussAndIssueData(t *testing.T) {
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	state := seedRepositoryReviewAPIState(t, workspace)
	mux := http.NewServeMux()
	NewHandler(configPath).RegisterRoutes(mux)

	list := httptest.NewRecorder()
	mux.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/repository-reviews", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), state.ID) ||
		strings.Contains(list.Body.String(), state.Findings[0].Evidence) {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}

	statusBody, _ := json.Marshal(map[string]any{
		"status": "dismissed", "expected_version": state.Version,
	})
	statusResponse := httptest.NewRecorder()
	statusRequest := httptest.NewRequest(
		http.MethodPatch,
		"/api/repository-reviews/"+state.ID+"/findings/"+state.Findings[0].ID,
		bytes.NewReader(statusBody),
	)
	setRepositoryReviewMutationHeaders(statusRequest)
	mux.ServeHTTP(statusResponse, statusRequest)
	if statusResponse.Code != http.StatusOK {
		t.Fatalf("status mutation=%d body=%s", statusResponse.Code, statusResponse.Body.String())
	}
	var dismissed struct {
		Repository repoaudit.RepositorySummary `json:"repository"`
		Finding    repoaudit.Finding           `json:"finding"`
	}
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &dismissed); err != nil {
		t.Fatal(err)
	}
	if dismissed.Finding.Status != repoaudit.FindingDismissed || dismissed.Repository.OpenFindingCount != 0 {
		t.Fatalf("status response=%#v", dismissed)
	}

	draftBody, _ := json.Marshal(map[string]any{
		"finding_ids": []string{state.Findings[0].ID}, "expected_version": dismissed.Repository.Version,
	})
	draftResponse := httptest.NewRecorder()
	draftRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/repository-reviews/"+state.ID+"/issue-drafts",
		bytes.NewReader(draftBody),
	)
	setRepositoryReviewMutationHeaders(draftRequest)
	mux.ServeHTTP(draftResponse, draftRequest)
	if draftResponse.Code != http.StatusCreated ||
		!strings.Contains(draftResponse.Body.String(), "blob `") ||
		!strings.Contains(draftResponse.Body.String(), state.LastCommitSHA) {
		t.Fatalf("draft status=%d body=%s", draftResponse.Code, draftResponse.Body.String())
	}
}

func TestRepositoryReviewDetailPaginationProjectsOnlyReferencedContext(t *testing.T) {
	firstContext := repoaudit.FindingContext{ID: "context-first"}
	secondContext := repoaudit.FindingContext{ID: "context-second"}
	drafts := make([]repoaudit.IssueDraft, 12)
	for index := range drafts {
		drafts[index].ID = fmt.Sprintf("draft-%02d", index)
	}
	state := repoaudit.RepositoryState{
		Files:                   map[string]repoaudit.ReviewedFile{"first.go": {}},
		ReviewAttempts:          map[string]int{"retry.go": 2},
		ReviewAttemptIdentities: map[string]string{"retry.go": "rat_internal"},
		Unsupported: map[string]repoaudit.UnsupportedFile{
			"large.go": {FileRef: repoaudit.FileRef{Path: "large.go"}, Reason: "file_too_large"},
		},
		Findings: []repoaudit.Finding{
			{ID: "finding-first", ContextIDs: []string{firstContext.ID}},
			{ID: "finding-second", ContextIDs: []string{secondContext.ID}},
		},
		Contexts:    []repoaudit.FindingContext{firstContext, secondContext},
		Runs:        make([]repoaudit.ReviewRun, 51),
		IssueDrafts: drafts,
	}

	projected := projectRepositoryReviewDetail(state, repositoryReviewPageRequest{
		FindingOffset: 1, FindingLimit: 1, DraftLimit: 10,
	})
	if projected.FindingOffset != 1 || projected.FindingTotal != 2 ||
		projected.NextFindingOffset != nil || len(projected.Findings) != 1 ||
		projected.Findings[0].ID != "finding-second" || len(projected.Contexts) != 1 ||
		projected.Contexts[0].ID != secondContext.ID || len(projected.Files) != 0 ||
		projected.ReviewAttempts != nil || projected.ReviewAttemptIdentities != nil ||
		len(projected.Runs) != 50 || projected.DraftTotal != 12 ||
		projected.NextDraftOffset == nil || *projected.NextDraftOffset != 10 ||
		len(projected.IssueDrafts) != 10 || projected.IssueDrafts[0].ID != "draft-02" ||
		projected.IssueDrafts[9].ID != "draft-11" {
		t.Fatalf("projected detail=%#v", projected)
	}
}

func TestRepositoryReviewDetailRejectsAmbiguousOrOversizedPage(t *testing.T) {
	for _, target := range []string{
		"/api/repository-reviews/id?offset=1&offset=2",
		"/api/repository-reviews/id?limit=201",
		"/api/repository-reviews/id?unexpected=true",
	} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		if _, err := repositoryReviewPage(request); err == nil {
			t.Fatalf("repositoryReviewPage(%q) unexpectedly succeeded", target)
		}
	}
}

func TestRepositoryReviewMutationRejectsStaleVersionAndUnknownFields(t *testing.T) {
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	state := seedRepositoryReviewAPIState(t, workspace)
	mux := http.NewServeMux()
	NewHandler(configPath).RegisterRoutes(mux)

	for name, test := range map[string]struct {
		body string
		want int
	}{
		"stale":   {body: `{"status":"dismissed","expected_version":999}`, want: http.StatusConflict},
		"unknown": {body: `{"status":"dismissed","expected_version":1,"secret":"x"}`, want: http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(
				http.MethodPatch,
				"/api/repository-reviews/"+state.ID+"/findings/"+state.Findings[0].ID,
				strings.NewReader(test.body),
			)
			setRepositoryReviewMutationHeaders(request)
			mux.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestRepositoryReviewMutationRejectsCrossSiteAndNonJSONRequests(t *testing.T) {
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	state := seedRepositoryReviewAPIState(t, workspace)
	mux := http.NewServeMux()
	NewHandler(configPath).RegisterRoutes(mux)
	for name, test := range map[string][2]string{
		"cross-site": {"application/json", "cross-site"},
		"text":       {"text/plain", "same-origin"},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(
				http.MethodPatch,
				"http://launcher.local/api/repository-reviews/"+state.ID+"/findings/"+state.Findings[0].ID,
				strings.NewReader(fmt.Sprintf(`{"status":"dismissed","expected_version":%d}`, state.Version)),
			)
			request.Header.Set("Content-Type", test[0])
			request.Header.Set("Sec-Fetch-Site", test[1])
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestRepositoryReviewPublishProxiesOnlyExactDraftPayloadToProtectedGateway(t *testing.T) {
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	var captured *http.Request
	var capturedBody string
	installEventProxyStubs(t, func(request *http.Request, _ time.Duration) (*http.Response, error) {
		captured = request
		body, _ := io.ReadAll(request.Body)
		capturedBody = string(body)
		return eventUpstreamResponse(http.StatusOK, `{"repository":{"id":"rrp_test"},"draft":{"id":"rid_test"}}`), nil
	})
	mux := http.NewServeMux()
	NewHandler(configPath).RegisterRoutes(mux)
	request := httptest.NewRequest(
		http.MethodPost,
		"http://launcher.local/api/repository-reviews/rrp_test/issue-drafts/rid_test/publish",
		strings.NewReader(`{"expected_version":3}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("publish status=%d body=%s", response.Code, response.Body.String())
	}
	if captured == nil || captured.Method != http.MethodPost ||
		captured.URL.Path != "/runtime/repository-reviews/rrp_test/issue-drafts/rid_test/publish" ||
		captured.Header.Get("Authorization") != "Bearer gateway-pid-token" ||
		capturedBody != `{"expected_version":3}` {
		t.Fatalf("gateway request=%#v body=%q", captured, capturedBody)
	}
}

func seedRepositoryReviewAPIState(t *testing.T, workspace string) repoaudit.RepositoryState {
	t.Helper()
	store := repoaudit.NewStore(workspace)
	file := repoaudit.FileRef{
		Path: "pkg/service.go", BlobSHA: strings.Repeat("a", 40), SizeBytes: 120,
		Category: "code", Mode: "100644",
	}
	plan, err := store.Plan(
		context.Background(),
		"owner/repo",
		"commit-a",
		"inventory-a",
		[]repoaudit.FileRef{file},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	line := 12
	result, err := store.Record(context.Background(), repoaudit.RecordRequest{
		Plan: plan, RunID: "api-run",
		Observations: []repoaudit.Observation{{
			Model: "review-model", ScopeFiles: []repoaudit.FileRef{file},
			Findings: []repoaudit.FindingCandidate{
				{
					Severity: "high",
					Title:    "Lost update",
					File:     file.Path,
					Line:     &line,
					Message:  "The update is not fenced.",
					Evidence: "Two writers overwrite each other.",
					Impact:   "Data is lost.",
					Validation: repoaudit.Validation{
						Status:  "confirmed",
						Summary: "Reproduced",
						Checks:  []string{"race test"},
					},
				},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return result.State
}

func setRepositoryReviewMutationHeaders(request *http.Request) {
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
}
