package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestRepositoryReviewDedupAdditionalProjectionAndPageCoverage(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	line := 17
	summary := repositoryReviewDeduplicatedFindingSummary{
		ID: "rdf_coverage", Repository: "owner/repo", Path: "pkg/core.go", Line: &line,
		Severity: "high", Title: "Lost update", Symbol: "Save", Status: repoaudit.FindingOpen,
		RunFindingStatus: repositoryReviewRunFindingAssociatedExisting, Association: "existing",
		RepositoryFindingID: "rrf_coverage", Contributors: []string{"model-a", "reviewer-a"},
		RawSourceCount: 2, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	}
	options := repositoryReviewDeduplicatedFindingPageOptions(
		repositoryReviewCollectionCursorContext("deduplicated-findings", "rra_coverage", "rrc_coverage"),
	)
	id, err := options.ID(summary)
	if err != nil || id == "" || !options.ValidateID(id) {
		t.Fatalf("deduplicated page id=%q err=%v", id, err)
	}
	for _, field := range repositoryReviewDeduplicatedFindingCollectionSchema.Fields {
		if _, ok := options.Resolve(summary, field.Name, now); !ok {
			t.Fatalf("deduplicated field %q was unresolved", field.Name)
		}
	}
	if _, ok := options.Resolve(summary, collectionquery.Field("unknown"), now); ok {
		t.Fatal("unknown deduplicated field resolved")
	}

	for _, request := range []*http.Request{
		nil,
		{URL: nil},
		httptest.NewRequest(http.MethodGet, "/?other=1", nil),
		httptest.NewRequest(http.MethodGet, "/?offset=1&offset=2", nil),
		httptest.NewRequest(http.MethodGet, "/?offset=-1", nil),
		httptest.NewRequest(http.MethodGet, "/?limit=201", nil),
	} {
		if _, _, pageErr := repositoryReviewRawPage(request); pageErr == nil {
			t.Fatalf("raw page accepted request %#v", request)
		}
	}
	offset, limit, err := repositoryReviewRawPage(httptest.NewRequest(http.MethodGet, "/?offset=2&limit=3", nil))
	if err != nil || offset != 2 || limit != 3 {
		t.Fatalf("raw page offset=%d limit=%d err=%v", offset, limit, err)
	}

	invalidProcessing := []string{
		"/?other=1", "/?state=unknown", "/?offset=-1", "/?limit=201", "/?state=pending&state=failed",
	}
	if _, _, _, pageErr := repositoryReviewFindingsProcessingPage(nil); pageErr == nil {
		t.Fatal("nil findings-processing request was accepted")
	}
	for _, target := range invalidProcessing {
		if _, _, _, pageErr := repositoryReviewFindingsProcessingPage(
			httptest.NewRequest(http.MethodGet, target, nil),
		); pageErr == nil {
			t.Fatalf("findings-processing page accepted %q", target)
		}
	}
	for _, state := range []repoaudit.RawFindingDeduplicationState{
		repoaudit.RawFindingDeduplicationPending,
		repoaudit.RawFindingDeduplicationRunning,
		repoaudit.RawFindingDeduplicationFailed,
		repoaudit.RawFindingDeduplicationCompleted,
	} {
		_, _, filter, pageErr := repositoryReviewFindingsProcessingPage(httptest.NewRequest(
			http.MethodGet, "/?state="+string(state), nil,
		))
		if pageErr != nil || filter != string(state) {
			t.Fatalf("findings-processing state=%q filter=%q err=%v", state, filter, pageErr)
		}
	}

	findings := []repoaudit.RawReviewFinding{
		{State: repoaudit.RawFindingDeduplicationPending, UpdatedAt: now.Add(-4 * time.Minute)},
		{State: repoaudit.RawFindingDeduplicationRunning, UpdatedAt: now.Add(-3 * time.Minute)},
		{State: repoaudit.RawFindingDeduplicationFailed, UpdatedAt: now.Add(-2 * time.Minute)},
		{
			State:       repoaudit.RawFindingDeduplicationCompleted,
			Disposition: repoaudit.RawFindingDispositionNew, UpdatedAt: now.Add(-time.Minute),
		},
		{
			State:       repoaudit.RawFindingDeduplicationCompleted,
			Disposition: repoaudit.RawFindingDispositionDuplicate, UpdatedAt: now,
		},
	}
	counters := repositoryReviewFindingsProcessingCounters(findings)
	if counters.RawTotal != 5 || counters.Pending != 1 || counters.Processing != 1 ||
		counters.Failed != 1 || counters.Completed != 2 || counters.New != 1 ||
		counters.Duplicates != 1 || !counters.UpdatedAt.Equal(now) {
		t.Fatalf("findings-processing counters=%#v", counters)
	}
	if !containsRepositoryReviewSourceID([]string{"one", "two"}, "two") ||
		containsRepositoryReviewSourceID([]string{"one"}, "missing") {
		t.Fatal("source membership coverage mismatch")
	}
	contributors := appendUniqueRepositoryReviewContributor(nil, " model-a ")
	contributors = appendUniqueRepositoryReviewContributor(contributors, "model-a")
	contributors = appendUniqueRepositoryReviewContributor(contributors, " ")
	if len(contributors) != 1 || contributors[0] != "model-a" {
		t.Fatalf("contributors=%#v", contributors)
	}
}

func TestRepositoryReviewDedupAdditionalRouteCoverage(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewAPIState(t, workspace)
	campaignID := "rrc_additional_coverage"
	state = seedRepositoryReviewDeduplicationAPIState(t, workspace, state, campaignID)
	automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
	base := "/api/repository-reviews/automations/" + automation.ID

	requests := []struct {
		path string
		want int
	}{
		{base + "/findings?query=(", http.StatusBadRequest},
		{base + "/findings/rdf_missing", http.StatusNotFound},
		{base + "/findings/" + state.DeduplicatedFindings[0].ID + "/sources?other=1", http.StatusBadRequest},
		{base + "/findings/rdf_missing/sources", http.StatusNotFound},
		{base + "/findings/" + state.DeduplicatedFindings[0].ID + "/sources/rrw_missing", http.StatusNotFound},
		{base + "/campaigns/wrong/findings-processing/sources/" + state.RawFindings[0].ID, http.StatusNotFound},
		{base + "/campaigns/" + campaignID + "/findings-processing?state=unknown", http.StatusBadRequest},
		{base + "/findings-processing", http.StatusBadRequest},
	}
	for _, test := range requests {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if response.Code != test.want {
			t.Fatalf("GET %s status=%d want=%d body=%s", test.path, response.Code, test.want, response.Body.String())
		}
	}

	detail := httptest.NewRecorder()
	mux.ServeHTTP(detail, httptest.NewRequest(
		http.MethodGet,
		base+"/findings/"+state.DeduplicatedFindings[0].ID+"/sources/"+state.RawFindings[0].ID,
		nil,
	))
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"context"`) ||
		!strings.Contains(detail.Body.String(), `"finding"`) {
		t.Fatalf("raw source detail status=%d body=%s", detail.Code, detail.Body.String())
	}

	filtered := httptest.NewRecorder()
	mux.ServeHTTP(filtered, httptest.NewRequest(
		http.MethodGet,
		base+"/campaigns/"+campaignID+"/findings-processing?state=pending&offset=0&limit=1",
		nil,
	))
	if filtered.Code != http.StatusOK || !strings.Contains(filtered.Body.String(), state.RawFindings[1].ID) {
		t.Fatalf("filtered processing status=%d body=%s", filtered.Code, filtered.Body.String())
	}

	for _, target := range []string{
		base + "/campaigns/" + campaignID + "/findings-processing/sources/" + state.RawFindings[1].ID + "/retry?query=1",
		base + "/campaigns/" + campaignID + "/findings-processing/sources/" + state.RawFindings[1].ID + "/retry",
	} {
		request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(`{}`))
		if !strings.Contains(target, "query=1") {
			setRepositoryReviewMutationHeaders(request)
		}
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest && response.Code != http.StatusConflict {
			t.Fatalf("retry %s status=%d body=%s", target, response.Code, response.Body.String())
		}
	}
}

func TestRepositoryReviewHistoricalDedupAdditionalRouteCoverage(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewAPIState(t, workspace)
	automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
	now := time.Now().UTC()
	state.RawFindings = nil
	state.DeduplicationJobs = nil
	state.DeduplicatedFindings = nil
	state.FindingsProcessing = repoaudit.FindingsProcessingCounters{}
	for index := range state.Findings {
		state.Findings[index].DeduplicationPending = false
		state.Findings[index].CommitSHA = strings.Repeat("a", 40)
	}
	state.HistoricalDeduplication = repoaudit.HistoricalDeduplicationReplay{
		Required: true, Status: repoaudit.HistoricalDeduplicationPending, UpdatedAt: now,
	}
	persistRepositoryReviewAdditionalCoverageState(t, workspace, state)
	store := repoaudit.NewStore(workspace)
	snapshot := repoaudit.RepositoryReviewDeduplicationSnapshot{
		ReviewerModel: "cheap", DeduplicationModel: "cheap", AccountRef: "api",
		SimilarityThreshold: 90, CandidateLimit: 4,
	}
	state, _, err := store.FreezeHistoricalDeduplicationReplay(state.Repository, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	state, admission, err := store.AdmitNextHistoricalDeduplicationBatch(state.Repository)
	if err != nil || admission.Admitted == 0 {
		t.Fatalf("historical admission=%#v err=%v", admission, err)
	}
	state, _, err = store.FailHistoricalDeduplicationReplay(state.Repository, "")
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/repository-reviews/automations/" + automation.ID + "/historical-deduplication"

	invalid := httptest.NewRecorder()
	mux.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, base+"?other=1", nil))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("historical invalid page status=%d body=%s", invalid.Code, invalid.Body.String())
	}
	page := httptest.NewRecorder()
	mux.ServeHTTP(page, httptest.NewRequest(http.MethodGet, base+"?offset=0&limit=1", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), admission.RawFindings[0].ID) {
		t.Fatalf("historical page status=%d body=%s", page.Code, page.Body.String())
	}

	badRetry := httptest.NewRecorder()
	mux.ServeHTTP(badRetry, httptest.NewRequest(http.MethodPost, base+"/retry?query=1", strings.NewReader(`{}`)))
	if badRetry.Code != http.StatusBadRequest {
		t.Fatalf("historical invalid retry status=%d body=%s", badRetry.Code, badRetry.Body.String())
	}
	retryRequest := httptest.NewRequest(http.MethodPost, base+"/retry", strings.NewReader(`{}`))
	setRepositoryReviewMutationHeaders(retryRequest)
	retried := httptest.NewRecorder()
	mux.ServeHTTP(retried, retryRequest)
	if retried.Code != http.StatusAccepted ||
		!strings.Contains(retried.Body.String(), `"status":"pending"`) {
		t.Fatalf("historical retry status=%d body=%s", retried.Code, retried.Body.String())
	}
}

func TestRepositoryReviewDedupAdditionalControllerAndModelCoverage(t *testing.T) {
	if err := (*repositoryReviewController)(nil).processRepositoryFindingDeduplications(t.Context()); err == nil {
		t.Fatal("nil deduplication controller succeeded")
	}
	if repositoryStateHasPendingDeduplication(repoaudit.RepositoryState{}) {
		t.Fatal("empty repository reported pending deduplication")
	}
	if !repositoryStateHasPendingDeduplication(repoaudit.RepositoryState{
		DeduplicationJobs: []repoaudit.DeduplicationJob{{State: repoaudit.DeduplicationJobPending}},
	}) {
		t.Fatal("pending repository was not detected")
	}

	handler, _, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewAPIState(t, workspace)
	state = seedRepositoryReviewDeduplicationAPIState(t, workspace, state, "rrc_controller_coverage")
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agents.Defaults.ContextWindow = 2
	controller := newRepositoryReviewController(handler)
	controller.startOnce.Do(func() {})
	controller.leasedStore = store
	controller.leasedConfig = cfg
	t.Cleanup(controller.Stop)

	originalProcessor := processRepositoryDeduplicationJobs
	t.Cleanup(func() { processRepositoryDeduplicationJobs = originalProcessor })
	processorCalls := 0
	processRepositoryDeduplicationJobs = func(
		_ repoaudit.Store,
		_ context.Context,
		repository string,
		options repoaudit.DeduplicationProcessOptions,
	) (repoaudit.DeduplicationProcessResult, error) {
		processorCalls++
		if repository != state.Repository || options.ModelInputCeiling != 1 || options.LeaseDuration != time.Hour {
			t.Fatalf("deduplication options repository=%q options=%#v", repository, options)
		}
		return repoaudit.DeduplicationProcessResult{}, errors.New("injected processor failure")
	}
	if processErr := controller.processRepositoryFindingDeduplications(t.Context()); processErr == nil ||
		!strings.Contains(processErr.Error(), "injected processor failure") || processorCalls != 1 {
		t.Fatalf("deduplication processing calls=%d err=%v", processorCalls, processErr)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if processErr := controller.processRepositoryFindingDeduplications(canceled); !errors.Is(processErr, context.Canceled) {
		t.Fatalf("canceled deduplication processing err=%v", processErr)
	}

	blockedRoot := filepath.Join(t.TempDir(), "blocked")
	if writeErr := os.WriteFile(blockedRoot, nil, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	blocked := newRepositoryReviewController(handler)
	blocked.leasedConfig = cfg
	blocked.leasedStore = repoaudit.NewStore(blockedRoot)
	if processErr := blocked.processRepositoryFindingDeduplications(t.Context()); processErr == nil {
		t.Fatal("deduplication processing accepted an unreadable store")
	}

	snapshot := repoaudit.RepositoryReviewDeduplicationSnapshot{
		ReviewerModel: "cheap", DeduplicationModel: "cheap", AccountRef: "api",
		SimilarityThreshold: 90, CandidateLimit: 4,
	}
	modelHandler := newRepositoryReviewAIAdjudicationHandler(t, http.StatusOK, `{}`)
	if _, modelErr := runRepositoryReviewDeduplicationModel(
		t.Context(), nil, snapshot, "score", map[string]any{}, "system", map[string]any{},
	); modelErr == nil {
		t.Fatal("nil deduplication handler succeeded")
	}
	if _, modelErr := runRepositoryReviewDeduplicationModel(
		t.Context(), modelHandler, snapshot, "score",
		strings.Repeat("x", repoaudit.DeduplicationMaximumInputBytes+1), "system", map[string]any{},
	); modelErr == nil || !strings.Contains(modelErr.Error(), "exceeds") {
		t.Fatalf("oversized deduplication input err=%v", modelErr)
	}

	originalAgent := runRepositoryDeduplicationAgent
	t.Cleanup(func() { runRepositoryDeduplicationAgent = originalAgent })
	runRepositoryDeduplicationAgent = func(
		context.Context, *webWorkflowRuntimeRunner, workflows.AgentRequest,
	) (map[string]any, error) {
		return nil, errors.New("injected agent failure")
	}
	if _, modelErr := runRepositoryReviewDeduplicationModel(
		t.Context(), modelHandler, snapshot, "score", map[string]any{}, "system", map[string]any{},
	); modelErr == nil || !strings.Contains(modelErr.Error(), "agent failure") {
		t.Fatalf("deduplication agent failure err=%v", modelErr)
	}
	runRepositoryDeduplicationAgent = func(
		context.Context, *webWorkflowRuntimeRunner, workflows.AgentRequest,
	) (map[string]any, error) {
		return map[string]any{"structured_valid": false}, nil
	}
	if _, modelErr := runRepositoryReviewDeduplicationModel(
		t.Context(), modelHandler, snapshot, "score", map[string]any{}, "system", map[string]any{},
	); modelErr == nil || !strings.Contains(modelErr.Error(), "invalid structured") {
		t.Fatalf("invalid structured deduplication output err=%v", modelErr)
	}
	runRepositoryDeduplicationAgent = func(
		context.Context, *webWorkflowRuntimeRunner, workflows.AgentRequest,
	) (map[string]any, error) {
		return map[string]any{"structured_valid": true, "structured": func() {}}, nil
	}
	if _, scoreErr := runRepositoryReviewDeduplicationScoring(
		t.Context(), modelHandler, snapshot, "score", repoaudit.DeduplicationScoringRequest{},
	); scoreErr == nil {
		t.Fatal("unencodable structured score succeeded")
	}
}

func TestRepositoryReviewHistoricalDedupAdditionalControllerCoverage(t *testing.T) {
	if err := (*repositoryReviewController)(nil).processHistoricalFindingDeduplications(
		t.Context(), nil,
	); err == nil {
		t.Fatal("nil historical controller succeeded")
	}
	handler, _, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewAPIState(t, workspace)
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}
	controller := newRepositoryReviewController(handler)
	controller.startOnce.Do(func() {})
	controller.leasedStore = store
	controller.leasedConfig = cfg
	t.Cleanup(controller.Stop)
	legacy := state
	legacy.RawFindings = nil
	legacy.DeduplicationJobs = nil
	legacy.DeduplicatedFindings = nil
	legacy.FindingsProcessing = repoaudit.FindingsProcessingCounters{}
	for index := range legacy.Findings {
		legacy.Findings[index].DeduplicationPending = false
		legacy.Findings[index].CommitSHA = strings.Repeat("b", 40)
	}
	legacy.HistoricalDeduplication = repoaudit.HistoricalDeduplicationReplay{
		Required: true, Status: repoaudit.HistoricalDeduplicationPending,
		UpdatedAt: time.Now().UTC(),
	}
	persistRepositoryReviewAdditionalCoverageState(t, workspace, legacy)
	replayAutomation := testRepositoryReviewAutomation()
	replayAutomation.ID = "rra_historical_replay_coverage"
	replayAutomation.Repository = legacy.Repository
	replayAutomation.RunIDs = []string{legacy.Runs[0].ID}
	if advanceErr := controller.advanceHistoricalFindingDeduplication(
		t.Context(), legacy, []repoaudit.RepositoryReviewAutomation{replayAutomation},
	); advanceErr != nil {
		t.Fatalf("pending replay advance err=%v", advanceErr)
	}
	failed := state
	failed.HistoricalDeduplication = repoaudit.HistoricalDeduplicationReplay{
		Required: true, Status: repoaudit.HistoricalDeduplicationFailed,
	}
	if advanceErr := controller.advanceHistoricalFindingDeduplication(t.Context(), failed, nil); advanceErr != nil {
		t.Fatalf("failed replay advance err=%v", advanceErr)
	}
	completed := state
	completed.HistoricalDeduplication = repoaudit.HistoricalDeduplicationReplay{
		Status: repoaudit.HistoricalDeduplicationCompleted,
	}
	if advanceErr := controller.advanceHistoricalFindingDeduplication(t.Context(), completed, nil); advanceErr != nil {
		t.Fatalf("completed replay advance err=%v", advanceErr)
	}
	pending := state
	pending.HistoricalDeduplication = repoaudit.HistoricalDeduplicationReplay{
		Required: true, Status: repoaudit.HistoricalDeduplicationPending,
	}
	automation := testRepositoryReviewAutomation()
	automation.ID = "rra_historical_missing_profile"
	automation.Repository = state.Repository
	automation.ProfileID = "rrpf_missing"
	if advanceErr := controller.advanceHistoricalFindingDeduplication(
		t.Context(), pending, []repoaudit.RepositoryReviewAutomation{automation},
	); advanceErr == nil || !strings.Contains(advanceErr.Error(), "profile was not found") {
		t.Fatalf("missing historical profile err=%v", advanceErr)
	}

	blockedRoot := filepath.Join(t.TempDir(), "blocked")
	if writeErr := os.WriteFile(blockedRoot, nil, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	blocked := newRepositoryReviewController(handler)
	blocked.leasedConfig = cfg
	blocked.leasedStore = repoaudit.NewStore(blockedRoot)
	if processErr := blocked.processHistoricalFindingDeduplications(t.Context(), nil); processErr == nil {
		t.Fatal("historical processing accepted an unreadable store")
	}
}

func persistRepositoryReviewAdditionalCoverageState(
	t *testing.T,
	workspace string,
	state repoaudit.RepositoryState,
) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(workspace, "repository_reviews", "repo_*.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if strings.HasSuffix(path, ".summary.json") {
			continue
		}
		encoded, encodeErr := json.Marshal(state)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		if writeErr := os.WriteFile(path, encoded, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
		return
	}
	t.Fatal("repository review state path is missing")
}
