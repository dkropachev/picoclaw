package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	state := seedRepositoryReviewAPIStateWithProcessing(t, workspace, false)
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
	if _, processErr := originalProcessor(
		store, t.Context(), "missing/repository", repoaudit.DeduplicationProcessOptions{},
	); processErr == nil {
		t.Fatal("default deduplication processor accepted a missing repository")
	}

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
	controller.wakeRepositoryFindingDeduplication()
	controller.wg.Wait()
	processorCalls = 0
	if processErr := controller.processRepositoryFindingDeduplications(t.Context()); processErr == nil ||
		!strings.Contains(processErr.Error(), "injected processor failure") || processorCalls != 1 {
		t.Fatalf("deduplication processing calls=%d err=%v", processorCalls, processErr)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if processErr := controller.processRepositoryFindingDeduplications(canceled); !errors.Is(
		processErr,
		context.Canceled,
	) {
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
	badConfigHandler := NewHandler(t.TempDir())
	if _, modelErr := runRepositoryReviewDeduplicationModel(
		t.Context(), badConfigHandler, snapshot, "score", map[string]any{}, "system", map[string]any{},
	); modelErr == nil {
		t.Fatal("directory-backed deduplication configuration succeeded")
	}
	missingModel := snapshot
	missingModel.DeduplicationModel = "missing-model"
	if _, modelErr := runRepositoryReviewDeduplicationModel(
		t.Context(), modelHandler, missingModel, "score", map[string]any{}, "system", map[string]any{},
	); modelErr == nil {
		t.Fatal("missing deduplication model alias resolved")
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
	if _, judgeErr := runRepositoryReviewDeduplicationJudgment(
		t.Context(), modelHandler, snapshot, "judge", repoaudit.DeduplicationJudgeRequest{},
	); judgeErr == nil || !strings.Contains(judgeErr.Error(), "agent failure") {
		t.Fatalf("deduplication judge agent failure err=%v", judgeErr)
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
	if _, judgeErr := runRepositoryReviewDeduplicationJudgment(
		t.Context(), modelHandler, snapshot, "judge", repoaudit.DeduplicationJudgeRequest{},
	); judgeErr == nil {
		t.Fatal("unencodable structured judgment succeeded")
	}
}

func TestRepositoryReviewDedupCanonicalResidualBranches(t *testing.T) {
	if _, found := repositoryReviewContextByID(repoaudit.RepositoryState{}, "missing"); found {
		t.Fatal("missing finding context resolved")
	}
	rawByID := map[string]repoaudit.RawReviewFinding{
		"rrw_fallback": {ID: "rrw_fallback", Model: "provider/model", Reviewer: "reviewer"},
	}
	summary := projectRepositoryReviewDeduplicatedFindingSummary(
		repoaudit.Finding{ID: "rdf_summary", RawSourceIDs: []string{"rrw_missing", "rrw_fallback", "rrw_fallback"}},
		newRepositoryReviewRunFindingStatusIndex(repoaudit.RepositoryState{}), rawByID,
	)
	if len(summary.Contributors) != 2 || summary.Contributors[0] != "provider/model" {
		t.Fatalf("deduplicated contributors=%#v", summary.Contributors)
	}
	values := appendUniqueRepositoryReviewContributor([]string{"same"}, "   ")
	values = appendUniqueRepositoryReviewContributor(values, "same")
	if len(values) != 1 {
		t.Fatalf("empty or duplicate contributor appended: %#v", values)
	}

	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewAPIState(t, workspace)
	automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	wrongCampaign := testRepositoryReviewAutomation()
	wrongCampaign.ID = "rra_canonical_wrong_campaign"
	wrongCampaign.Repository = state.Repository
	wrongCampaign.CampaignID = repoaudit.NewRepositoryReviewCampaignID()
	wrongCampaign.RunIDs = []string{state.Runs[0].ID}
	if _, err = store.CreateAutomation(t.Context(), wrongCampaign); err != nil {
		t.Fatal(err)
	}

	base := "/api/repository-reviews/automations/"
	for _, target := range []string{
		base + wrongCampaign.ID + "/findings/" + state.Findings[0].ID,
		base + wrongCampaign.ID + "/raw-findings/" + state.RawFindings[0].ID,
		base + automation.ID + "/findings/rdf_missing/sources/" + state.RawFindings[0].ID,
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("canonical identity fence %s=%d %s", target, response.Code, response.Body.String())
		}
	}

	now := time.Now().UTC()
	query, err := collectionquery.Parse("", repositoryReviewRawFindingCollectionSchema)
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := collectionquery.CursorFor(
		query,
		projectRepositoryReviewRawFindingSummary(state.RawFindings[0]),
		now,
		repositoryReviewRawFindingPageOptions(repositoryReviewCollectionCursorContext("wrong")),
	)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		base+automation.ID+"/raw-findings?cursor="+url.QueryEscape(cursor),
		nil,
	))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("wrong-context raw cursor=%d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, base+automation.ID+"/raw-findings?query=unknown%20%3D%20x", nil,
	))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid raw query=%d %s", response.Code, response.Body.String())
	}
}
