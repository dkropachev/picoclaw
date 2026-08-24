package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestRepositoryReviewCoverageDetailAndDraftUpdateHandlers(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewAPIState(t, workspace)

	detail := httptest.NewRecorder()
	mux.ServeHTTP(detail, httptest.NewRequest(
		http.MethodGet,
		"/api/repository-reviews/"+state.ID+"?offset=0&limit=1&draft_offset=0&draft_limit=1",
		nil,
	))
	if detail.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	var projected repositoryReviewDetailResponse
	if err := json.Unmarshal(detail.Body.Bytes(), &projected); err != nil {
		t.Fatal(err)
	}
	if projected.ID != state.ID || projected.FindingTotal != 1 || len(projected.Findings) != 1 ||
		len(projected.Contexts) != 1 || len(projected.Files) != 0 {
		t.Fatalf("detail projection=%#v", projected)
	}

	invalidPage := httptest.NewRecorder()
	mux.ServeHTTP(invalidPage, httptest.NewRequest(
		http.MethodGet,
		"/api/repository-reviews/"+state.ID+"?limit=0",
		nil,
	))
	if invalidPage.Code != http.StatusBadRequest {
		t.Fatalf("invalid page status=%d body=%s", invalidPage.Code, invalidPage.Body.String())
	}

	missingID := "rrp_" + strings.Repeat("f", 64)
	missing := httptest.NewRecorder()
	mux.ServeHTTP(missing, httptest.NewRequest(
		http.MethodGet,
		"/api/repository-reviews/"+missingID,
		nil,
	))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing detail status=%d body=%s", missing.Code, missing.Body.String())
	}

	prepared := repositoryReviewCoverageMutation(
		t,
		mux,
		http.MethodPost,
		"/api/repository-reviews/"+state.ID+"/issue-drafts",
		map[string]any{
			"finding_ids": []string{state.Findings[0].ID},
			"title":       "Initial issue", "body": "Initial body", "labels": []string{"bug"},
			"expected_version": state.Version,
		},
	)
	if prepared.Code != http.StatusCreated {
		t.Fatalf("prepare status=%d body=%s", prepared.Code, prepared.Body.String())
	}
	var preparedResult struct {
		Repository repoaudit.RepositorySummary `json:"repository"`
		Draft      repoaudit.IssueDraft        `json:"draft"`
	}
	if err := json.Unmarshal(prepared.Body.Bytes(), &preparedResult); err != nil {
		t.Fatal(err)
	}

	updated := repositoryReviewCoverageMutation(
		t,
		mux,
		http.MethodPatch,
		"/api/repository-reviews/"+state.ID+"/issue-drafts/"+preparedResult.Draft.ID,
		map[string]any{
			"title": "Updated issue", "body": "Updated body", "labels": []string{"bug", "reviewed"},
			"expected_version": preparedResult.Draft.Version,
		},
	)
	if updated.Code != http.StatusOK {
		t.Fatalf("update draft status=%d body=%s", updated.Code, updated.Body.String())
	}
	var updatedResult struct {
		Repository repoaudit.RepositorySummary `json:"repository"`
		Draft      repoaudit.IssueDraft        `json:"draft"`
	}
	if err := json.Unmarshal(updated.Body.Bytes(), &updatedResult); err != nil {
		t.Fatal(err)
	}
	if updatedResult.Draft.Title != "Updated issue" || updatedResult.Draft.Body != "Updated body" ||
		len(updatedResult.Draft.Labels) != 2 || updatedResult.Draft.Version <= preparedResult.Draft.Version {
		t.Fatalf("updated draft=%#v", updatedResult.Draft)
	}

	missingMutation := repositoryReviewCoverageMutation(
		t,
		mux,
		http.MethodPatch,
		"/api/repository-reviews/"+missingID+"/issue-drafts/"+preparedResult.Draft.ID,
		map[string]any{
			"title": "No repository", "body": "No repository", "expected_version": 1,
		},
	)
	if missingMutation.Code != http.StatusNotFound {
		t.Fatalf("missing mutation status=%d body=%s", missingMutation.Code, missingMutation.Body.String())
	}
}

func TestRepositoryReviewCoverageAutomationOptionsAndAccountProjection(t *testing.T) {
	withPicoclawAuthHome(t)
	handler, mux, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/api/repository-reviews/automation-options",
		nil,
	))
	if response.Code != http.StatusOK {
		t.Fatalf("options status=%d body=%s", response.Code, response.Body.String())
	}
	var options repositoryReviewAutomationOptionsCoverageResponse
	if err := json.Unmarshal(response.Body.Bytes(), &options); err != nil {
		t.Fatal(err)
	}
	if len(options.Models) != 2 || len(options.Accounts) != 0 {
		t.Fatalf("options=%#v", options)
	}

	used := 25
	accounts := repositoryReviewAccountOptions(codexAccountLimitsResponse{Accounts: []codexAccountLimitAccount{
		{
			ID: "openai:work", Provider: "openai", Email: "work@example.test",
			LimitsStatus: "available", Entries: []codexAccountLimitEntry{{
				Name: "Codex", Status: "available", Window: "weekly", UsedPercent: &used,
				RefreshesAt: "2026-08-27T12:00:00Z",
			}},
		},
		{ID: "github:backup", Provider: "github-copilot", AccountID: "backup-id", CredentialStatus: "missing"},
	}})
	if len(accounts) != 2 || accounts[0].Label != "work@example.test" || len(accounts[0].Entries) != 1 ||
		accounts[0].Entries[0].RemainingPercent == nil || *accounts[0].Entries[0].RemainingPercent != 75 ||
		accounts[1].Label != "backup-id" || accounts[1].Status != "missing" {
		t.Fatalf("account options=%#v", accounts)
	}

	missingConfigHandler := NewHandler(t.TempDir())
	missingConfigMux := http.NewServeMux()
	missingConfigHandler.RegisterRoutes(missingConfigMux)
	missingConfigResponse := httptest.NewRecorder()
	missingConfigMux.ServeHTTP(missingConfigResponse, httptest.NewRequest(
		http.MethodGet,
		"/api/repository-reviews/automation-options",
		nil,
	))
	if missingConfigResponse.Code != http.StatusInternalServerError {
		t.Fatalf(
			"missing config options status=%d body=%s",
			missingConfigResponse.Code,
			missingConfigResponse.Body.String(),
		)
	}
}

type repositoryReviewAutomationOptionsCoverageResponse struct {
	Models   []repositoryReviewModelOption   `json:"models"`
	Accounts []repositoryReviewAccountOption `json:"accounts"`
}

func TestRepositoryReviewCoverageAutomationMutationBranches(t *testing.T) {
	handler, mux, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	store, storeErr := handler.repositoryReviewStore()
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	profile := createRepositoryReviewProfileForTest(t, mux, "Coverage", "cheap")

	createdResponse := repositoryReviewAutomationMutation(t, mux, http.MethodPost,
		"/api/repository-reviews/automations", map[string]any{
			"repository": "owner/coverage",
			"branch":     "main",
			"profile_id": profile.ID,
		})
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create default status=%d body=%s", createdResponse.Code, createdResponse.Body.String())
	}
	var created struct {
		Automation repoaudit.RepositoryReviewAutomation `json:"automation"`
	}
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Automation.ProfileID != profile.ID ||
		len(created.Automation.ReviewerModels) != 1 ||
		created.Automation.ReviewerModels[0] != "cheap" ||
		!created.Automation.AutoContinue {
		t.Fatalf("defaulted automation=%#v", created.Automation)
	}

	paused, updateErr := store.UpdateAutomation(t.Context(), created.Automation.ID, created.Automation.Version,
		func(candidate *repoaudit.RepositoryReviewAutomation) error {
			candidate.Status = repoaudit.RepositoryReviewAutomationPaused
			candidate.PauseReason = repoaudit.RepositoryReviewPauseManual
			candidate.PauseDetail = "paused before reconfiguration"
			candidate.Progress = repoaudit.RepositoryReviewProgress{TotalBatches: 2, CompletedBatches: 1}
			candidate.Usage = repoaudit.RepositoryReviewTokenUsage{
				PromptTokens:     8,
				CompletionTokens: 2,
				TotalTokens:      10,
			}
			candidate.EstimatedCostUSD = 0.25
			candidate.StartedAt = time.Now().UTC().Add(-time.Minute)
			return nil
		})
	if updateErr != nil {
		t.Fatal(updateErr)
	}
	updateBody := map[string]any{
		"repository":       paused.Repository,
		"branch":           "release/v2",
		"profile_id":       profile.ID,
		"expected_version": paused.Version,
	}
	updatedResponse := repositoryReviewAutomationMutation(t, mux, http.MethodPatch,
		"/api/repository-reviews/automations/"+paused.ID, updateBody)
	if updatedResponse.Code != http.StatusOK {
		t.Fatalf("reconfigure status=%d body=%s", updatedResponse.Code, updatedResponse.Body.String())
	}
	var updated struct {
		Automation repoaudit.RepositoryReviewAutomation `json:"automation"`
	}
	if err := json.Unmarshal(updatedResponse.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Automation.Status != repoaudit.RepositoryReviewAutomationIdle ||
		updated.Automation.Usage.TotalTokens != 0 || updated.Automation.Progress.CompletedBatches != 0 ||
		!updated.Automation.StartedAt.IsZero() {
		t.Fatalf("reconfigured automation=%#v", updated.Automation)
	}

	runningInput := testRepositoryReviewAutomation()
	runningInput.Status = repoaudit.RepositoryReviewAutomationRunning
	runningInput.ActiveRunID = "wr_pause_coverage"
	runningInput.RunIDs = []string{"wr_pause_coverage"}
	runningInput.Progress.TotalBatches = 1
	running, err := store.CreateAutomation(t.Context(), runningInput)
	if err != nil {
		t.Fatal(err)
	}
	controller := handler.repositoryReviewControllerInstance()
	controller.mu.Lock()
	controller.active[running.ID] = &repositoryReviewActiveRun{runID: running.ActiveRunID, store: store}
	controller.mu.Unlock()
	handler.StartRepositoryReviewController()

	deleteActive := repositoryReviewAutomationMutation(t, mux, http.MethodDelete,
		"/api/repository-reviews/automations/"+running.ID,
		map[string]any{"expected_version": running.Version})
	if deleteActive.Code != http.StatusConflict {
		t.Fatalf("delete active status=%d body=%s", deleteActive.Code, deleteActive.Body.String())
	}

	pausedResponse := repositoryReviewAutomationMutation(t, mux, http.MethodPost,
		"/api/repository-reviews/automations/"+running.ID+"/pause",
		map[string]any{"expected_version": running.Version})
	if pausedResponse.Code != http.StatusAccepted {
		t.Fatalf("pause status=%d body=%s", pausedResponse.Code, pausedResponse.Body.String())
	}
	var pauseResult struct {
		Automation repoaudit.RepositoryReviewAutomation `json:"automation"`
	}
	if err := json.Unmarshal(pausedResponse.Body.Bytes(), &pauseResult); err != nil {
		t.Fatal(err)
	}
	if pauseResult.Automation.Status != repoaudit.RepositoryReviewAutomationStopping ||
		pauseResult.Automation.RequestedPauseReason != repoaudit.RepositoryReviewPauseManual {
		t.Fatalf("pause result=%#v", pauseResult.Automation)
	}

	missingDelete := repositoryReviewAutomationMutation(t, mux, http.MethodDelete,
		"/api/repository-reviews/automations/rra_missing",
		map[string]any{"expected_version": 1})
	if missingDelete.Code != http.StatusNotFound {
		t.Fatalf("missing delete status=%d body=%s", missingDelete.Code, missingDelete.Body.String())
	}

	badHandler := NewHandler(t.TempDir())
	badMux := http.NewServeMux()
	badHandler.RegisterRoutes(badMux)
	badList := httptest.NewRecorder()
	badMux.ServeHTTP(badList, httptest.NewRequest(http.MethodGet, "/api/repository-reviews/automations", nil))
	if badList.Code != http.StatusInternalServerError {
		t.Fatalf("bad list status=%d body=%s", badList.Code, badList.Body.String())
	}
}

func TestRepositoryReviewSplitCoverageOffsets(t *testing.T) {
	for _, repository := range []string{
		"http://example.com:81/owner/repository",
		"git://example.com:9418/owner/repository",
	} {
		if normalized, err := normalizeRepositoryReviewAutomationRepository(repository); err == nil {
			t.Fatalf("unsafe repository %q normalized to %q", repository, normalized)
		}
	}
	if normalized, err := normalizeRepositoryReviewAutomationRepository(
		"https://[2001:db8::1]/owner/repository",
	); err != nil || normalized != "https://[2001:db8::1]/owner/repository.git" {
		t.Fatalf("IPv6 repository normalization = (%q, %v)", normalized, err)
	}

	validScope := repoaudit.RepositoryReviewScopePolicy{
		CodeTypes:      []repoaudit.RepositoryReviewCodeType{repoaudit.RepositoryReviewCodeTypeCode},
		IncludeFolders: []string{"pkg"},
		ExcludeFolders: []string{"pkg/generated"},
	}
	invalidScope := validScope
	invalidScope.CodeTypes = []repoaudit.RepositoryReviewCodeType{"invalid"}
	if repositoryReviewScopePoliciesEqual(invalidScope, validScope) {
		t.Fatal("invalid scope policies compared equal")
	}
	if slicesEqualRepositoryReviewCodeTypes(
		validScope.CodeTypes,
		append(validScope.CodeTypes, repoaudit.RepositoryReviewCodeTypeTest),
	) {
		t.Fatal("different-length code type slices compared equal")
	}
	if slicesEqualRepositoryReviewCodeTypes(
		validScope.CodeTypes,
		[]repoaudit.RepositoryReviewCodeType{repoaudit.RepositoryReviewCodeTypeTest},
	) {
		t.Fatal("different code type slices compared equal")
	}

	controller := &repositoryReviewController{}
	if _, err := controller.normalizeRepositoryReviewAutomationAdmission(
		t.Context(),
		repoaudit.Store{},
		repoaudit.RepositoryReviewAutomation{Repository: "relative/path/extra"},
	); err == nil {
		t.Fatal("admission accepted an invalid repository")
	}

	automation := repoaudit.RepositoryReviewAutomation{}
	applyRepositoryReviewRunProgress(&automation, &workflows.RunResult{Outputs: map[string]any{
		"scopePlan": map[string]any{"commit_sha": strings.Repeat("a", 40)},
	}})
	if automation.ScopePlan.CommitSHA != strings.Repeat("a", 40) {
		t.Fatalf("scope plan was not projected: %#v", automation.ScopePlan)
	}
}

func TestRepositoryReviewCoverageControllerHelpersAndOutcome(t *testing.T) {
	handler, _, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewAPIState(t, workspace)
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}

	automation := repoaudit.RepositoryReviewAutomation{
		Repository: "owner/repo", ReviewerModels: []string{"review-model"}, RunIDs: []string{"api-run"},
		ModelStats: make(map[string]repoaudit.RepositoryReviewModelStats),
	}
	outcome := loadRepositoryReviewOutcome(store, automation)
	if !outcome.found || outcome.reviewedFiles != 1 || outcome.findings != 1 ||
		outcome.modelFindings["review-model"] != 1 || len(outcome.modelPaths["review-model"]) != 1 {
		t.Fatalf("loaded outcome=%#v state=%#v", outcome, state)
	}
	applyRepositoryReviewOutcome(&automation, outcome)
	if automation.Progress.ReviewedFiles != 1 || automation.Progress.Findings != 1 ||
		automation.ModelStats["review-model"].Findings != 1 ||
		automation.ModelStats["review-model"].ReviewedFiles < 1 {
		t.Fatalf("applied automation=%#v", automation)
	}

	if got := mapStringValues(
		map[string]string{"b": "second", "a": "first"},
	); len(got) != 2 || got[0] != "first" ||
		got[1] != "second" {
		t.Fatalf("map values=%#v", got)
	}
	models := repoaudit.RepositoryReviewAutomation{ReviewerModels: []string{"cheap", "quality"}}
	if got := repositoryReviewExecutionModels(models); len(got) != 1 || got[0] != "cheap" {
		t.Fatalf("single execution models=%#v", got)
	}
	models.CompareModels = true
	if got := repositoryReviewExecutionModels(models); len(got) != 2 {
		t.Fatalf("comparison execution models=%#v", got)
	}

	cfg := config.DefaultConfig()
	cfg.ModelAliases = []config.ModelAliasConfig{{
		Name: "cheap", Model: "openai/gpt-cheap",
		AccountOverrides: map[string]string{"work": "anthropic/claude-cheap"},
	}}
	priced := repoaudit.RepositoryReviewAutomation{
		ReviewerModels: []string{"cheap"},
		ModelPrices: map[string]repoaudit.RepositoryReviewModelPrice{
			"cheap": {InputPricePer1M: 1, OutputPricePer1M: 2},
		},
	}
	index := repositoryReviewAccountingIndex(cfg, priced)
	if index["cheap"].alias != "cheap" || index["gpt-cheap"].alias != "cheap" ||
		index["anthropic/claude-cheap"].alias != "cheap" || !index["*"].known {
		t.Fatalf("accounting index=%#v", index)
	}

	controller := newRepositoryReviewController(handler)
	controller.active["rra_latch"] = &repositoryReviewActiveRun{runID: "wr_latch", store: store}
	latchErr := controller.latchAccountingFailure("rra_latch", "wr_latch", errors.New("disk full"))
	if !errors.Is(latchErr, errRepositoryReviewSafeStop) ||
		controller.active["rra_latch"].pauseReason != repoaudit.RepositoryReviewPauseRunFailed ||
		!strings.Contains(controller.active["rra_latch"].pauseDetail, "disk full") {
		t.Fatalf("latch error=%v active=%#v", latchErr, controller.active["rra_latch"])
	}

	admissionController := newRepositoryReviewController(handler)
	admissionController.active["rra_admit"] = &repositoryReviewActiveRun{runID: "wr_admit"}
	if err := admissionController.admitProviderCall("rra_admit", "wr_admit"); err != nil {
		t.Fatalf("admitted call error=%v", err)
	}
	admissionController.active["rra_admit"].pauseReason = repoaudit.RepositoryReviewPauseManual
	admissionController.active["rra_admit"].pauseDetail = "manual stop"
	if err := admissionController.admitProviderCall(
		"rra_admit",
		"wr_admit",
	); !errors.Is(
		err,
		errRepositoryReviewSafeStop,
	) {
		t.Fatalf("paused admission error=%v", err)
	}
	delete(admissionController.active, "rra_admit")
	if err := admissionController.admitProviderCall(
		"rra_admit",
		"wr_admit",
	); !errors.Is(
		err,
		errRepositoryReviewSafeStop,
	) {
		t.Fatalf("missing admission error=%v", err)
	}
	admissionController.cancel()
	if err := admissionController.admitProviderCall(
		"rra_admit",
		"wr_admit",
	); !errors.Is(
		err,
		errRepositoryReviewSafeStop,
	) {
		t.Fatalf("canceled admission error=%v", err)
	}
	var nilController *repositoryReviewController
	if err := nilController.admitProviderCall("rra_admit", "wr_admit"); !errors.Is(err, errRepositoryReviewSafeStop) {
		t.Fatalf("nil admission error=%v", err)
	}

	costGuard := repoaudit.RepositoryReviewAutomation{
		EstimatedCostUSD: 2, BudgetPolicy: repoaudit.RepositoryReviewBudgetPolicy{MaxEstimatedCostUSD: 1},
	}
	if reason, _ := repositoryReviewBudgetGuard(costGuard); reason != repoaudit.RepositoryReviewPauseCostBudget {
		t.Fatalf("cost guard reason=%q", reason)
	}
	if got := repositoryReviewAnySlice([]map[string]any{{"id": 1}}); len(got) != 1 {
		t.Fatalf("map slice=%#v", got)
	}
	if got := repositoryReviewAnySlice("not-a-slice"); got != nil {
		t.Fatalf("invalid slice=%#v", got)
	}

	for name, value := range map[string]any{
		"int": 1, "int64": int64(2), "float64": float64(3), "float32": float32(4), "string": "5",
	} {
		if got := repositoryReviewInt(value); got < 1 || got > 5 {
			t.Fatalf("repositoryReviewInt(%s)=%d", name, got)
		}
	}
	if got := repositoryReviewInt(true); got != 0 {
		t.Fatalf("repositoryReviewInt(bool)=%d", got)
	}
	if got := repositoryReviewRunError(errors.New("provider failed"), nil); got != "provider failed" {
		t.Fatalf("run error=%q", got)
	}
	if got := repositoryReviewRunError(nil, &workflows.RunResult{Error: "result failed"}); got != "result failed" {
		t.Fatalf("result error=%q", got)
	}
	if got := repositoryReviewRunError(nil, nil); got == "" {
		t.Fatal("default run error is empty")
	}
	bounded := repositoryReviewBoundedDetail(strings.Repeat("é", 3000))
	if len(bounded) > 4096 || !utf8.ValidString(bounded) || !strings.HasSuffix(bounded, "...") {
		t.Fatalf("bounded detail bytes=%d valid=%v", len(bounded), utf8.ValidString(bounded))
	}
	if normalizeRepositoryReviewWindow("7d") != "weekly" ||
		normalizeRepositoryReviewWindow("24h") != "daily" ||
		normalizeRepositoryReviewWindow("") != "unknown" ||
		normalizeRepositoryReviewWindow("monthly") != "monthly" {
		t.Fatal("window normalization mismatch")
	}
	if reset, ok := parseRepositoryReviewReset("2026-08-27T12:00:00Z"); !ok || reset.IsZero() {
		t.Fatalf("RFC3339 reset=%s ok=%v", reset, ok)
	}
	if reset, ok := parseRepositoryReviewReset("2026-08-27 12:00:00 UTC"); !ok || reset.IsZero() {
		t.Fatalf("display reset=%s ok=%v", reset, ok)
	}
	if _, ok := parseRepositoryReviewReset("-"); ok {
		t.Fatal("dash reset unexpectedly parsed")
	}
}

func TestRepositoryReviewCoverageFinishAutomationBranches(t *testing.T) {
	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	controller := handler.repositoryReviewControllerInstance()

	createRunning := func(runID string, autoContinue bool) repoaudit.RepositoryReviewAutomation {
		t.Helper()
		candidate := testRepositoryReviewAutomation()
		candidate.AutoContinue = autoContinue
		candidate.Status = repoaudit.RepositoryReviewAutomationRunning
		candidate.ActiveRunID = runID
		candidate.RunIDs = []string{runID}
		candidate.Progress.TotalBatches = 1
		created, createErr := store.CreateAutomation(t.Context(), candidate)
		if createErr != nil {
			t.Fatal(createErr)
		}
		controller.mu.Lock()
		controller.active[created.ID] = &repositoryReviewActiveRun{runID: runID, store: store}
		controller.mu.Unlock()
		return created
	}

	checkpointed := createRunning("wr_finish_checkpoint", false)
	controller.finishAutomationRun(checkpointed.ID, checkpointed.ActiveRunID, &workflows.RunResult{
		RunID: checkpointed.ActiveRunID, Status: workflows.RunStatusSucceeded,
		Outputs: map[string]any{"remainingFiles": 2, "reviewedFiles": 1},
	}, nil, true)
	checkpointed, _, err = store.GetAutomation(t.Context(), checkpointed.ID)
	if err != nil || checkpointed.Status != repoaudit.RepositoryReviewAutomationPaused ||
		checkpointed.PauseReason != repoaudit.RepositoryReviewPauseManual ||
		checkpointed.Progress.CompletedBatches != 1 || checkpointed.Progress.RemainingFiles != 2 {
		t.Fatalf("checkpointed finish=%#v err=%v", checkpointed, err)
	}

	failed := createRunning("wr_finish_failed", false)
	controller.finishAutomationRun(
		failed.ID,
		failed.ActiveRunID,
		&workflows.RunResult{RunID: failed.ActiveRunID, Status: workflows.RunStatusFailed, Error: "workflow failed"},
		errors.New("provider failed"),
		false,
	)
	failed, _, err = store.GetAutomation(t.Context(), failed.ID)
	if err != nil || failed.Status != repoaudit.RepositoryReviewAutomationFailed ||
		failed.PauseReason != repoaudit.RepositoryReviewPauseRunFailed ||
		failed.Progress.CompletedBatches != 0 || !strings.Contains(failed.PauseDetail, "provider failed") {
		t.Fatalf("failed finish=%#v err=%v", failed, err)
	}

	missingCheckpoint := createRunning("wr_finish_missing", false)
	controller.finishAutomationRun(missingCheckpoint.ID, missingCheckpoint.ActiveRunID, &workflows.RunResult{
		RunID: missingCheckpoint.ActiveRunID, Status: workflows.RunStatusSucceeded,
		Outputs: map[string]any{"remainingFiles": 0},
	}, nil, false)
	missingCheckpoint, _, err = store.GetAutomation(t.Context(), missingCheckpoint.ID)
	if err != nil || missingCheckpoint.Status != repoaudit.RepositoryReviewAutomationFailed ||
		!strings.Contains(missingCheckpoint.PauseDetail, "without a verified durable") {
		t.Fatalf("missing checkpoint finish=%#v err=%v", missingCheckpoint, err)
	}
}

func TestRepositoryReviewCoverageProgressMonitor(t *testing.T) {
	handler, _, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	runID := "wr_progress_coverage"
	input := testRepositoryReviewAutomation()
	input.Status = repoaudit.RepositoryReviewAutomationRunning
	input.ActiveRunID = runID
	input.RunIDs = []string{runID}
	input.Progress.TotalBatches = 1
	automation, err := store.CreateAutomation(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	workflowStore := workflows.NewFileRunStore(workspace)
	if err := workflowStore.CreateRun(t.Context(), &workflows.Run{
		ID: runID, WorkflowRef: workflows.RepositoryBugFinderWorkflowRef, Status: workflows.RunStatusRunning,
		Steps: map[string]workflows.StepExecution{
			"find_bugs/review": {ID: "review", Status: workflows.RunStatusRunning},
		},
	}); err != nil {
		t.Fatal(err)
	}

	monitorCtx, cancelMonitor := context.WithCancel(t.Context())
	done := make(chan struct{})
	controller := newRepositoryReviewController(handler)
	go func() {
		controller.monitorWorkflowProgress(monitorCtx, store, workflowStore, automation.ID, runID)
		close(done)
	}()
	deadline := time.Now().Add(2500 * time.Millisecond)
	for time.Now().Before(deadline) {
		current, found, getErr := store.GetAutomation(t.Context(), automation.ID)
		if getErr != nil {
			cancelMonitor()
			t.Fatal(getErr)
		}
		if found && current.Progress.Stage == "Reviewing bounded file batch" {
			cancelMonitor()
			<-done
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	cancelMonitor()
	<-done
	t.Fatal("progress monitor did not project the running review step")
}

func repositoryReviewCoverageMutation(
	t *testing.T,
	mux *http.ServeMux,
	method string,
	path string,
	body map[string]any,
) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, path, strings.NewReader(string(data)))
	setRepositoryReviewMutationHeaders(request)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func repositoryReviewCoverageRawRequest(
	t *testing.T,
	mux *http.ServeMux,
	method string,
	path string,
	body string,
	validMutationHeaders bool,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if validMutationHeaders {
		setRepositoryReviewMutationHeaders(request)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func TestRepositoryReviewCoverageHandlerRequestFailures(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewAPIState(t, workspace)

	mutations := []struct {
		method          string
		path            string
		malformedStatus int
	}{
		{method: http.MethodPost, path: "/api/repository-reviews/automations", malformedStatus: http.StatusBadRequest},
		{
			method:          http.MethodPatch,
			path:            "/api/repository-reviews/automations/rra_missing",
			malformedStatus: http.StatusBadRequest,
		},
		{
			method:          http.MethodDelete,
			path:            "/api/repository-reviews/automations/rra_missing",
			malformedStatus: http.StatusBadRequest,
		},
		{
			method:          http.MethodPost,
			path:            "/api/repository-reviews/automations/rra_missing/start",
			malformedStatus: http.StatusBadRequest,
		},
		{
			method:          http.MethodPost,
			path:            "/api/repository-reviews/automations/rra_missing/pause",
			malformedStatus: http.StatusBadRequest,
		},
		{
			method:          http.MethodPatch,
			path:            "/api/repository-reviews/" + state.ID + "/findings/missing",
			malformedStatus: http.StatusInternalServerError,
		},
		{
			method:          http.MethodPost,
			path:            "/api/repository-reviews/" + state.ID + "/issue-drafts",
			malformedStatus: http.StatusInternalServerError,
		},
		{
			method:          http.MethodPatch,
			path:            "/api/repository-reviews/" + state.ID + "/issue-drafts/missing",
			malformedStatus: http.StatusInternalServerError,
		},
	}
	for _, mutation := range mutations {
		withoutHeaders := repositoryReviewCoverageRawRequest(
			t, mux, mutation.method, mutation.path, `{}`, false,
		)
		if withoutHeaders.Code != http.StatusBadRequest {
			t.Fatalf(
				"%s %s without headers = %d %s",
				mutation.method,
				mutation.path,
				withoutHeaders.Code,
				withoutHeaders.Body.String(),
			)
		}
		malformed := repositoryReviewCoverageRawRequest(
			t, mux, mutation.method, mutation.path, `{`, true,
		)
		if malformed.Code != mutation.malformedStatus {
			t.Fatalf("%s %s malformed = %d %s", mutation.method, mutation.path, malformed.Code, malformed.Body.String())
		}
	}

	invalidCreate := repositoryReviewCoverageMutation(
		t,
		mux,
		http.MethodPost,
		"/api/repository-reviews/automations",
		map[string]any{"repository": "", "reviewer_models": []string{}},
	)
	if invalidCreate.Code != http.StatusBadRequest {
		t.Fatalf("invalid create = %d %s", invalidCreate.Code, invalidCreate.Body.String())
	}

	missingFinding := repositoryReviewCoverageMutation(
		t,
		mux,
		http.MethodPatch,
		"/api/repository-reviews/"+state.ID+"/findings/missing",
		map[string]any{"status": "dismissed", "expected_version": state.Version},
	)
	if missingFinding.Code != http.StatusNotFound {
		t.Fatalf("missing finding = %d %s", missingFinding.Code, missingFinding.Body.String())
	}
	missingIssueFinding := repositoryReviewCoverageMutation(
		t,
		mux,
		http.MethodPost,
		"/api/repository-reviews/"+state.ID+"/issue-drafts",
		map[string]any{"finding_ids": []string{"missing"}, "expected_version": state.Version},
	)
	if missingIssueFinding.Code != http.StatusNotFound {
		t.Fatalf("missing issue finding = %d %s", missingIssueFinding.Code, missingIssueFinding.Body.String())
	}
	missingDraft := repositoryReviewCoverageMutation(
		t,
		mux,
		http.MethodPatch,
		"/api/repository-reviews/"+state.ID+"/issue-drafts/missing",
		map[string]any{"title": "title", "body": "body", "expected_version": 1},
	)
	if missingDraft.Code != http.StatusNotFound {
		t.Fatalf("missing draft = %d %s", missingDraft.Code, missingDraft.Body.String())
	}
}

func TestRepositoryReviewCoverageMissingConfigurationHandlers(t *testing.T) {
	handler := NewHandler(t.TempDir())
	t.Cleanup(handler.Shutdown)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	automation := testRepositoryReviewAutomation()
	configBody := automationConfigBody(automation)
	configBody["profile_id"] = "rrpf_missing"
	configBody["expected_version"] = 1

	requests := []struct {
		method string
		path   string
		body   map[string]any
	}{
		{method: http.MethodPost, path: "/api/repository-reviews/automations", body: configBody},
		{method: http.MethodPatch, path: "/api/repository-reviews/automations/rra_missing", body: configBody},
		{
			method: http.MethodDelete,
			path:   "/api/repository-reviews/automations/rra_missing",
			body:   map[string]any{"expected_version": 1},
		},
		{
			method: http.MethodPost,
			path:   "/api/repository-reviews/automations/rra_missing/start",
			body:   map[string]any{"expected_version": 1},
		},
		{
			method: http.MethodPost,
			path:   "/api/repository-reviews/automations/rra_missing/pause",
			body:   map[string]any{"expected_version": 1},
		},
		{
			method: http.MethodPatch,
			path:   "/api/repository-reviews/rrp_missing/findings/missing",
			body:   map[string]any{"status": "dismissed", "expected_version": 1},
		},
		{
			method: http.MethodPost,
			path:   "/api/repository-reviews/rrp_missing/issue-drafts",
			body:   map[string]any{"finding_ids": []string{"missing"}, "expected_version": 1},
		},
		{
			method: http.MethodPatch,
			path:   "/api/repository-reviews/rrp_missing/issue-drafts/missing",
			body:   map[string]any{"title": "title", "body": "body", "expected_version": 1},
		},
	}
	for _, request := range requests {
		response := repositoryReviewCoverageMutation(t, mux, request.method, request.path, request.body)
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("%s %s = %d %s", request.method, request.path, response.Code, response.Body.String())
		}
	}

	for _, path := range []string{
		"/api/repository-reviews",
		"/api/repository-reviews/rrp_" + strings.Repeat("a", 64),
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("GET %s = %d %s", path, response.Code, response.Body.String())
		}
	}
}

func TestRepositoryReviewCoveragePagingProjectionAndDecodeBoundaries(t *testing.T) {
	if _, pageErr := repositoryReviewPage(nil); pageErr == nil {
		t.Fatal("nil page request was accepted")
	}
	for _, target := range []string{
		"/api/repository-reviews/id?offset=1&limit=2&draft_offset=3&draft_limit=4&extra=5",
		"/api/repository-reviews/id?unknown=1",
		"/api/repository-reviews/id?offset=1&offset=2",
		"/api/repository-reviews/id?offset=nope",
		"/api/repository-reviews/id?limit=201",
		"/api/repository-reviews/id?draft_offset=-1",
		"/api/repository-reviews/id?draft_limit=21",
	} {
		if _, pageErr := repositoryReviewPage(httptest.NewRequest(http.MethodGet, target, nil)); pageErr == nil {
			t.Fatalf("invalid page %q was accepted", target)
		}
	}
	if value, integerErr := repositoryReviewPageInteger("", 7, 10); integerErr != nil || value != 7 {
		t.Fatalf("page fallback=%d err=%v", value, integerErr)
	}

	state := repoaudit.RepositoryState{
		Findings: []repoaudit.Finding{
			{ID: "one", ContextIDs: []string{"ctx-one"}},
			{ID: "two", ContextIDs: []string{"ctx-two"}},
			{ID: "three", ContextIDs: []string{"ctx-three"}},
		},
		Contexts:    []repoaudit.FindingContext{{ID: "ctx-one"}, {ID: "ctx-two"}, {ID: "ctx-three"}},
		Files:       map[string]repoaudit.ReviewedFile{"private": {}},
		Unsupported: make(map[string]repoaudit.UnsupportedFile),
		Runs:        make([]repoaudit.ReviewRun, 51),
		IssueDrafts: []repoaudit.IssueDraft{{ID: "old"}, {ID: "middle"}, {ID: "new"}},
	}
	for index := 0; index < 205; index++ {
		path := "path-" + strconv.Itoa(index)
		state.Unsupported[path] = repoaudit.UnsupportedFile{FileRef: repoaudit.FileRef{Path: path}}
	}
	projected := projectRepositoryReviewDetail(state, repositoryReviewPageRequest{
		FindingOffset: 0, FindingLimit: 1, DraftOffset: 0, DraftLimit: 1,
	})
	if len(projected.Findings) != 1 || len(projected.Contexts) != 1 || len(projected.Files) != 0 ||
		len(projected.Unsupported) != 200 || len(projected.Runs) != 50 || len(projected.IssueDrafts) != 1 ||
		projected.NextFindingOffset == nil || projected.NextDraftOffset == nil {
		t.Fatalf("projected detail=%#v", projected)
	}
	clamped := projectRepositoryReviewDetail(state, repositoryReviewPageRequest{
		FindingOffset: 99, FindingLimit: 1, DraftOffset: 99, DraftLimit: 1,
	})
	if clamped.FindingOffset != len(state.Findings) || len(clamped.Findings) != 0 || len(clamped.IssueDrafts) != 0 {
		t.Fatalf("clamped detail=%#v", clamped)
	}

	if decodeErr := decodeRepositoryReviewRequest(nil, &map[string]any{}); decodeErr == nil {
		t.Fatal("nil decode request was accepted")
	}
	trailing := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{} {}`))
	if decodeErr := decodeRepositoryReviewRequest(trailing, &map[string]any{}); decodeErr == nil {
		t.Fatal("trailing JSON was accepted")
	}
	if mutationErr := validateRepositoryReviewMutation(nil); mutationErr == nil {
		t.Fatal("nil mutation was accepted")
	}
}

func TestRepositoryReviewCoverageErrorProjection(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
	}{
		{err: os.ErrNotExist, status: http.StatusNotFound},
		{err: repoaudit.ErrConflict, status: http.StatusConflict},
		{err: repoaudit.ErrInvalidPlan, status: http.StatusBadRequest},
		{err: errors.New("duplicate input"), status: http.StatusBadRequest},
		{err: errors.New("disk failed"), status: http.StatusInternalServerError},
	} {
		response := httptest.NewRecorder()
		writeRepositoryReviewError(response, test.err)
		if response.Code != test.status {
			t.Fatalf("review error %v = %d", test.err, response.Code)
		}
	}
	for _, test := range []struct {
		err    error
		status int
	}{
		{err: os.ErrNotExist, status: http.StatusNotFound},
		{err: errRepositoryReviewAutomationBusy, status: http.StatusConflict},
		{err: repoaudit.ErrInvalidAutomation, status: http.StatusBadRequest},
		{err: io.ErrUnexpectedEOF, status: http.StatusBadRequest},
		{err: &json.UnmarshalTypeError{Value: "string", Type: reflect.TypeOf(1)}, status: http.StatusBadRequest},
		{err: errors.New("disk failed"), status: http.StatusInternalServerError},
	} {
		response := httptest.NewRecorder()
		writeRepositoryReviewAutomationError(response, test.err)
		if response.Code != test.status {
			t.Fatalf("automation error %v = %d", test.err, response.Code)
		}
	}
}

func TestRepositoryReviewCoverageAutomationTransitionsAndUtilities(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	store, storeErr := handler.repositoryReviewStore()
	if storeErr != nil {
		t.Fatal(storeErr)
	}

	runningInput := testRepositoryReviewAutomation()
	runningInput.Status = repoaudit.RepositoryReviewAutomationRunning
	runningInput.ActiveRunID = "run-busy-update"
	runningInput.RunIDs = []string{runningInput.ActiveRunID}
	running, createErr := store.CreateAutomation(t.Context(), runningInput)
	if createErr != nil {
		t.Fatal(createErr)
	}
	busyBody := automationConfigBody(running)
	busyBody["expected_version"] = running.Version
	busy := repositoryReviewCoverageMutation(
		t, mux, http.MethodPatch, "/api/repository-reviews/automations/"+running.ID, busyBody,
	)
	if busy.Code != http.StatusConflict {
		t.Fatalf("busy update = %d %s", busy.Code, busy.Body.String())
	}

	idleInput := testRepositoryReviewAutomation()
	idleInput.AccountLimitSnapshots = []repoaudit.RepositoryReviewAccountLimitSnapshot{{
		AccountID: "account-a", Window: "weekly", CheckedAt: time.Now().UTC(),
	}}
	idleInput.NextCheckAt = time.Now().UTC().Add(time.Minute)
	idle, createErr := store.CreateAutomation(t.Context(), idleInput)
	if createErr != nil {
		t.Fatal(createErr)
	}
	quotaBody := automationConfigBody(idle)
	quotaBody["expected_version"] = idle.Version
	budget := quotaBody["budget"].(repoaudit.RepositoryReviewBudgetPolicy)
	budget.MinRemainingPercent = budget.MinRemainingPercent + 1
	quotaBody["budget"] = budget
	quotaUpdate := repositoryReviewCoverageMutation(
		t, mux, http.MethodPatch, "/api/repository-reviews/automations/"+idle.ID, quotaBody,
	)
	if quotaUpdate.Code != http.StatusOK {
		t.Fatalf("quota-only update = %d %s", quotaUpdate.Code, quotaUpdate.Body.String())
	}
	var quotaResult struct {
		Automation repoaudit.RepositoryReviewAutomation `json:"automation"`
	}
	if decodeErr := json.Unmarshal(quotaUpdate.Body.Bytes(), &quotaResult); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if len(quotaResult.Automation.AccountLimitSnapshots) != 0 || !quotaResult.Automation.NextCheckAt.IsZero() {
		t.Fatalf("quota-only automation=%#v", quotaResult.Automation)
	}

	staleDelete := repositoryReviewCoverageMutation(
		t,
		mux,
		http.MethodDelete,
		"/api/repository-reviews/automations/"+idle.ID,
		map[string]any{"expected_version": idle.Version + 10},
	)
	if staleDelete.Code != http.StatusConflict {
		t.Fatalf("stale delete = %d %s", staleDelete.Code, staleDelete.Body.String())
	}
	for _, action := range []string{"start", "resume", "restart", "pause"} {
		missing := repositoryReviewCoverageMutation(
			t,
			mux,
			http.MethodPost,
			"/api/repository-reviews/automations/rra_missing/"+action,
			map[string]any{"expected_version": 1},
		)
		if missing.Code != http.StatusNotFound {
			t.Fatalf("missing %s = %d %s", action, missing.Code, missing.Body.String())
		}
	}

	var nilHandler *Handler
	for _, action := range []string{"start", "pause"} {
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"expected_version":1}`))
		request.SetPathValue("automation_id", "rra_missing")
		setRepositoryReviewMutationHeaders(request)
		response := httptest.NewRecorder()
		if action == "start" {
			nilHandler.handleRepositoryReviewAutomationStartAction(response, request, false, false)
		} else {
			nilHandler.handlePauseRepositoryReviewAutomation(response, request)
		}
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("nil handler %s = %d %s", action, response.Code, response.Body.String())
		}
	}

	applyRepositoryReviewAutomationRequest(nil, repositoryReviewAutomationConfigRequest{})
	if slicesEqual([]string{"a"}, []string{"b"}) || slicesEqual([]string{"a"}, nil) ||
		!slicesEqual([]string{"a"}, []string{"a"}) {
		t.Fatal("slice equality mismatch")
	}
	if len(repositoryReviewModelOptions(nil)) != 0 ||
		repositoryReviewAliasAvailableForRuntime(nil, config.ModelAliasConfig{Name: "alias"}) ||
		repositoryReviewAliasAvailableForRuntime(config.DefaultConfig(), config.ModelAliasConfig{}) {
		t.Fatal("nil model option boundary mismatch")
	}
	if repositoryReviewRuntimeAccountRefs(nil) != nil {
		t.Fatal("nil runtime account refs were non-nil")
	}
	if _, ok := repositoryReviewAliasPrice(nil, "alias", map[string]bool{}); ok {
		t.Fatal("nil alias pricing succeeded")
	}
	if _, ok := repositoryReviewAliasPrice(config.DefaultConfig(), "", map[string]bool{}); ok {
		t.Fatal("empty alias pricing succeeded")
	}
	if _, ok := repositoryReviewAliasPrice(config.DefaultConfig(), "alias", map[string]bool{"alias": true}); ok {
		t.Fatal("cyclic alias pricing succeeded")
	}

	routerConfig := config.DefaultConfig()
	routerConfig.Agents.Defaults.AccountRef = "review-router"
	routerConfig.AccountRouters = []config.AccountRouterConfig{{
		Name: "review-router", Enabled: true, Entry: "entry",
		Blocks: []config.AccountRouterBlock{
			{
				ID: "entry", Type: config.AccountRouterBlockTypeAccount,
				Account: " account-a ", Fallback: "pool",
			},
			{
				ID: "pool", Type: config.AccountRouterBlockTypeLoadBalance,
				Accounts: []string{"account-b", "account-a", ""},
			},
		},
	}}
	if refs := repositoryReviewRuntimeAccountRefs(
		routerConfig,
	); !reflect.DeepEqual(
		refs,
		[]string{"account-a", "account-b"},
	) {
		t.Fatalf("router refs=%#v", refs)
	}

	usedAbove := 125
	usedBelow := -10
	accounts := repositoryReviewAccountOptions(codexAccountLimitsResponse{Accounts: []codexAccountLimitAccount{
		{ID: "fallback-id", Entries: []codexAccountLimitEntry{{UsedPercent: &usedAbove}}},
		{ID: "account-id", AccountID: "account-label", Entries: []codexAccountLimitEntry{{UsedPercent: &usedBelow}}},
	}})
	if accounts[0].Label != "fallback-id" || *accounts[0].Entries[0].RemainingPercent != 0 ||
		accounts[1].Label != "account-label" || *accounts[1].Entries[0].RemainingPercent != 100 {
		t.Fatalf("fallback accounts=%#v", accounts)
	}

	root := filepath.Join(workspace, "repository_reviews")
	if removeErr := os.RemoveAll(root); removeErr != nil {
		t.Fatal(removeErr)
	}
	if writeErr := os.WriteFile(root, []byte("not a directory"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	listFailure := httptest.NewRecorder()
	mux.ServeHTTP(listFailure, httptest.NewRequest(http.MethodGet, "/api/repository-reviews/automations", nil))
	if listFailure.Code != http.StatusInternalServerError {
		t.Fatalf("corrupt automation list = %d %s", listFailure.Code, listFailure.Body.String())
	}
}

func TestRepositoryReviewCoveragePublishAndCorruptStoreFailures(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewAPIState(t, workspace)

	invalid := httptest.NewRecorder()
	handler.handlePublishRepositoryReviewIssue(invalid, nil)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("nil publish = %d %s", invalid.Code, invalid.Body.String())
	}
	emptyRequest := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	emptyRequest.SetPathValue("repository_id", state.ID)
	emptyRequest.SetPathValue("draft_id", "draft")
	setRepositoryReviewMutationHeaders(emptyRequest)
	empty := httptest.NewRecorder()
	handler.handlePublishRepositoryReviewIssue(empty, emptyRequest)
	if empty.Code != http.StatusBadRequest {
		t.Fatalf("empty publish = %d %s", empty.Code, empty.Body.String())
	}
	proxyRequest := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"expected_version":1}`))
	proxyRequest.SetPathValue("repository_id", state.ID)
	proxyRequest.SetPathValue("draft_id", "draft")
	setRepositoryReviewMutationHeaders(proxyRequest)
	proxy := httptest.NewRecorder()
	handler.handlePublishRepositoryReviewIssue(proxy, proxyRequest)
	if proxy.Code < http.StatusBadRequest {
		t.Fatalf("unconfigured publish proxy = %d %s", proxy.Code, proxy.Body.String())
	}

	root := filepath.Join(workspace, "repository_reviews")
	if removeErr := os.RemoveAll(root); removeErr != nil {
		t.Fatal(removeErr)
	}
	if writeErr := os.WriteFile(root, []byte("not a directory"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	for _, path := range []string{
		"/api/repository-reviews",
		"/api/repository-reviews/" + state.ID,
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("corrupt GET %s = %d %s", path, response.Code, response.Body.String())
		}
	}
	mutation := repositoryReviewCoverageMutation(
		t,
		mux,
		http.MethodPatch,
		"/api/repository-reviews/"+state.ID+"/findings/"+state.Findings[0].ID,
		map[string]any{"status": "dismissed", "expected_version": state.Version},
	)
	if mutation.Code != http.StatusInternalServerError {
		t.Fatalf("corrupt mutation = %d %s", mutation.Code, mutation.Body.String())
	}
}

func repositoryReviewCoverageRunningAutomation(
	t *testing.T,
	store repoaudit.Store,
	runID string,
	autoContinue bool,
) repoaudit.RepositoryReviewAutomation {
	t.Helper()
	input := testRepositoryReviewAutomation()
	input.Status = repoaudit.RepositoryReviewAutomationRunning
	input.ActiveRunID = runID
	input.RunIDs = []string{runID}
	input.AutoContinue = autoContinue
	input.Progress.TotalBatches = 1
	created, createErr := store.CreateAutomation(t.Context(), input)
	if createErr != nil {
		t.Fatal(createErr)
	}
	return created
}

func TestRepositoryReviewCoverageExecuteAndFinishBoundaries(t *testing.T) {
	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	store, storeErr := handler.repositoryReviewStore()
	if storeErr != nil {
		t.Fatal(storeErr)
	}

	t.Run("missing active run", func(t *testing.T) {
		controller := newRepositoryReviewController(handler)
		controller.wg.Add(1)
		controller.executeAutomation("rra_missing", "run-missing")
	})
	t.Run("missing automation", func(t *testing.T) {
		controller := newRepositoryReviewController(handler)
		controller.active["rra_missing"] = &repositoryReviewActiveRun{runID: "run-missing", store: store}
		controller.wg.Add(1)
		controller.executeAutomation("rra_missing", "run-missing")
		if _, exists := controller.active["rra_missing"]; exists {
			t.Fatal("missing automation active run survived")
		}
	})
	t.Run("runtime config failure", func(t *testing.T) {
		automation := repositoryReviewCoverageRunningAutomation(t, store, "run-runtime-config", false)
		controller := newRepositoryReviewController(handler)
		controller.active[automation.ID] = &repositoryReviewActiveRun{
			runID: automation.ActiveRunID, store: store, config: nil,
		}
		controller.wg.Add(1)
		controller.executeAutomation(automation.ID, automation.ActiveRunID)
		updated, found, getErr := store.GetAutomation(t.Context(), automation.ID)
		if getErr != nil || !found || updated.Status != repoaudit.RepositoryReviewAutomationFailed {
			t.Fatalf("runtime failure automation=%#v found=%v err=%v", updated, found, getErr)
		}
	})
	t.Run("real workflow runtime failure", func(t *testing.T) {
		automation := repositoryReviewCoverageRunningAutomation(t, store, "run-real-runtime", false)
		cfg, loadErr := config.LoadConfig(handler.configPath)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		controller := newRepositoryReviewController(handler)
		runCtx, cancelRun := context.WithTimeout(context.Background(), 3*time.Second)
		controller.ctx = runCtx
		controller.cancel = cancelRun
		controller.active[automation.ID] = &repositoryReviewActiveRun{
			runID: automation.ActiveRunID, store: store, config: cfg,
		}
		controller.wg.Add(1)
		controller.executeAutomation(automation.ID, automation.ActiveRunID)
		cancelRun()
		updated, found, getErr := store.GetAutomation(t.Context(), automation.ID)
		if getErr != nil || !found || updated.Status == repoaudit.RepositoryReviewAutomationRunning {
			t.Fatalf("real runtime automation=%#v found=%v err=%v", updated, found, getErr)
		}
	})

	finish := func(
		t *testing.T,
		runID string,
		status repoaudit.RepositoryReviewAutomationStatus,
		autoContinue bool,
		result *workflows.RunResult,
		runErr error,
		checkpointed bool,
		configureActive func(*repositoryReviewActiveRun),
	) repoaudit.RepositoryReviewAutomation {
		t.Helper()
		automation := repositoryReviewCoverageRunningAutomation(t, store, runID, autoContinue)
		if status == repoaudit.RepositoryReviewAutomationStopping {
			var updateErr error
			automation, updateErr = store.UpdateAutomation(
				t.Context(),
				automation.ID,
				automation.Version,
				func(candidate *repoaudit.RepositoryReviewAutomation) error {
					candidate.Status = repoaudit.RepositoryReviewAutomationStopping
					candidate.RequestedPauseReason = repoaudit.RepositoryReviewPauseManual
					return nil
				},
			)
			if updateErr != nil {
				t.Fatal(updateErr)
			}
		}
		controller := newRepositoryReviewController(handler)
		active := &repositoryReviewActiveRun{runID: runID, store: store}
		if configureActive != nil {
			configureActive(active)
		}
		controller.active[automation.ID] = active
		if autoContinue {
			controller.stopped = true
		}
		controller.finishAutomationRun(automation.ID, runID, result, runErr, checkpointed)
		updated, found, getErr := store.GetAutomation(t.Context(), automation.ID)
		if getErr != nil || !found {
			t.Fatalf("finished automation found=%v err=%v", found, getErr)
		}
		return updated
	}

	canceled := finish(
		t,
		"run-canceled",
		repoaudit.RepositoryReviewAutomationRunning,
		false,
		nil,
		context.Canceled,
		false,
		nil,
	)
	if canceled.Status != repoaudit.RepositoryReviewAutomationPaused ||
		canceled.PauseReason != repoaudit.RepositoryReviewPauseServiceRestart {
		t.Fatalf("canceled finish=%#v", canceled)
	}
	stopping := finish(
		t,
		"run-stopping",
		repoaudit.RepositoryReviewAutomationStopping,
		false,
		&workflows.RunResult{Status: workflows.RunStatusSucceeded},
		nil,
		true,
		nil,
	)
	if stopping.Status != repoaudit.RepositoryReviewAutomationPaused {
		t.Fatalf("stopping finish=%#v", stopping)
	}
	failed := finish(
		t,
		"run-nil-result",
		repoaudit.RepositoryReviewAutomationRunning,
		false,
		nil,
		nil,
		false,
		nil,
	)
	if failed.Status != repoaudit.RepositoryReviewAutomationFailed {
		t.Fatalf("nil-result finish=%#v", failed)
	}
	completed := finish(
		t,
		"run-complete",
		repoaudit.RepositoryReviewAutomationRunning,
		false,
		&workflows.RunResult{
			Status:  workflows.RunStatusSucceeded,
			Outputs: map[string]any{"remainingFiles": 0, "reviewedFiles": 1},
		},
		nil,
		true,
		nil,
	)
	if completed.Status != repoaudit.RepositoryReviewAutomationCompleted || completed.CompletedAt.IsZero() {
		t.Fatalf("completed finish=%#v", completed)
	}
	continued := finish(
		t,
		"run-continue",
		repoaudit.RepositoryReviewAutomationRunning,
		true,
		&workflows.RunResult{
			Status:  workflows.RunStatusSucceeded,
			Outputs: map[string]any{"remainingFiles": 2, "reviewedFiles": 1},
		},
		nil,
		true,
		nil,
	)
	if continued.Status != repoaudit.RepositoryReviewAutomationIdle {
		t.Fatalf("continued finish=%#v", continued)
	}
}

func TestRepositoryReviewCoverageRunAndProgressHelpers(t *testing.T) {
	if repositoryReviewWorkflowStage(nil) != "" || repositoryReviewRunStep(nil, "review").ID != "" {
		t.Fatal("nil workflow helpers returned state")
	}
	queued := &workflows.Run{Steps: map[string]workflows.StepExecution{}}
	if repositoryReviewWorkflowStage(queued) != "queued" {
		t.Fatalf("queued stage=%q", repositoryReviewWorkflowStage(queued))
	}
	running := &workflows.Run{Steps: map[string]workflows.StepExecution{
		"review": {ID: "review", Status: workflows.RunStatusRunning},
	}}
	if repositoryReviewWorkflowStage(running) != "Reviewing bounded file batch" ||
		repositoryReviewRunStep(running, "review").ID != "review" {
		t.Fatalf(
			"running stage=%q step=%#v",
			repositoryReviewWorkflowStage(running),
			repositoryReviewRunStep(running, "review"),
		)
	}
	succeeded := &workflows.Run{Steps: map[string]workflows.StepExecution{
		"find_bugs/record": {
			ID: "record", Status: workflows.RunStatusSucceeded,
			Outputs: map[string]any{"run": map[string]any{"id": "saved"}},
		},
	}}
	result := &workflows.RunResult{Status: workflows.RunStatusSucceeded}
	if !repositoryReviewRunCheckpointed(succeeded, result) ||
		repositoryReviewRunCheckpointed(nil, result) || repositoryReviewRunCheckpointed(succeeded, nil) {
		t.Fatal("record checkpoint detection mismatch")
	}
	noPending := &workflows.Run{Steps: map[string]workflows.StepExecution{
		"plan":   {Status: workflows.RunStatusSucceeded, Outputs: map[string]any{"pending_count": 0}},
		"result": {Status: workflows.RunStatusSucceeded},
	}}
	result.Outputs = map[string]any{"remainingFiles": 0}
	if !repositoryReviewRunCheckpointed(noPending, result) {
		t.Fatal("no-op checkpoint was not recognized")
	}

	automation := repoaudit.RepositoryReviewAutomation{MaxFilesPerRun: 2}
	applyRepositoryReviewRunProgress(nil, result)
	applyRepositoryReviewRunProgress(&automation, nil)
	applyRepositoryReviewRunProgress(&automation, &workflows.RunResult{Outputs: map[string]any{
		"remaining_files": 3, "reviewed_files": 2,
	}})
	if automation.Progress.RemainingFiles != 3 || automation.Progress.ReviewedFiles != 2 ||
		automation.Progress.TotalBatches != 2 {
		t.Fatalf("snake-case progress=%#v", automation.Progress)
	}

	if outcome := loadRepositoryReviewOutcome(
		repoaudit.NewStore(t.TempDir()),
		repoaudit.RepositoryReviewAutomation{Repository: "owner/missing"},
	); outcome.found {
		t.Fatalf("missing outcome=%#v", outcome)
	}
	applyRepositoryReviewOutcome(nil, repositoryReviewOutcome{found: true})
	applyRepositoryReviewOutcome(&automation, repositoryReviewOutcome{})
	addRepositoryReviewModelPaths(nil, "alias", []string{"path"})
	addRepositoryReviewModelPaths(&automation, "", []string{"path"})
	addRepositoryReviewModelPaths(&automation, "alias", nil)

	controller := newRepositoryReviewController(nil)
	if _, updateErr := controller.updateLatest(
		t.Context(),
		repoaudit.NewStore(t.TempDir()),
		"rra_missing",
		func(*repoaudit.RepositoryReviewAutomation) error {
			return nil
		},
	); updateErr == nil {
		t.Fatal("missing updateLatest automation was accepted")
	}
	var nilController *repositoryReviewController
	if nilController.clock().IsZero() {
		t.Fatal("nil controller clock returned zero")
	}
}

func TestRepositoryReviewCoverageQuotaRemainingBranches(t *testing.T) {
	now := time.Now().UTC()
	base := testRepositoryReviewAutomation()
	base.BudgetPolicy.AccountIDs = []string{"work"}
	base.BudgetPolicy.PauseOnUnknown = true
	base.BudgetPolicy.CheckIntervalSeconds = 30

	tests := []struct {
		name       string
		automation repoaudit.RepositoryReviewAutomation
		response   codexAccountLimitsResponse
		wantPause  bool
	}{
		{
			name: "unselected account", automation: base,
			response:  codexAccountLimitsResponse{Accounts: []codexAccountLimitAccount{{ID: "backup"}}},
			wantPause: true,
		},
		{
			name: "unknown status", automation: base,
			response: codexAccountLimitsResponse{Accounts: []codexAccountLimitAccount{{
				ID: "work", Entries: []codexAccountLimitEntry{{Status: "mystery", Window: "weekly"}},
			}}},
			wantPause: true,
		},
		{
			name: "nil remaining", automation: base,
			response: codexAccountLimitsResponse{Accounts: []codexAccountLimitAccount{{
				ID: "work", Entries: []codexAccountLimitEntry{{Status: "available", Window: "weekly"}},
			}}},
			wantPause: true,
		},
		{
			name: "valid reset and required window",
			automation: func() repoaudit.RepositoryReviewAutomation {
				value := base
				value.BudgetPolicy.MinRemainingPercentByWindow = map[string]float64{"weekly": 10}
				return value
			}(),
			response: codexAccountLimitsResponse{Accounts: []codexAccountLimitAccount{{
				ID: "work", Entries: []codexAccountLimitEntry{{
					Status: "available", Window: "weekly", UsedPercent: ptrInt(0),
					RefreshesAt: now.Add(time.Hour).Format(time.RFC3339),
				}},
			}}},
		},
		{
			name: "response error after valid account", automation: base,
			response: codexAccountLimitsResponse{
				Error: "partial telemetry",
				Accounts: []codexAccountLimitAccount{
					{
						ID: "work",
						Entries: []codexAccountLimitEntry{
							{Status: "available", Window: "weekly", UsedPercent: ptrInt(0)},
						},
					},
				},
			},
			wantPause: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshots, _, reason, _, quotaErr := evaluateRepositoryReviewQuota(test.automation, test.response, now)
			if quotaErr != nil || (reason != "") != test.wantPause ||
				len(snapshots) == 0 && len(test.response.Accounts) > 0 {
				t.Fatalf("snapshots=%#v reason=%q err=%v", snapshots, reason, quotaErr)
			}
		})
	}
}

func TestRepositoryReviewCoverageManagedChildOutcomeBranches(t *testing.T) {
	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	store, storeErr := handler.repositoryReviewStore()
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	controller := newRepositoryReviewController(handler)
	controller.recordManagedChildOutcomes("missing", "run", nil, nil)
	controller.recordManagedChildOutcomes(
		"missing",
		"run",
		&workflows.Run{Steps: map[string]workflows.StepExecution{"review": {Outputs: map[string]any{}}}},
		nil,
	)

	automation := repositoryReviewCoverageRunningAutomation(t, store, "run-managed-branches", false)
	controller.active[automation.ID] = &repositoryReviewActiveRun{runID: automation.ActiveRunID, store: store}
	index := map[string]repositoryReviewAccountingModel{
		"selected-model": {alias: "cheap", known: true},
		"fallback":       {alias: "cheap", known: true},
		"":               {alias: "cheap", known: true},
	}
	children := []any{
		"not-a-map",
		map[string]any{"model": map[string]any{"selected": "missing"}, "admitted": true, "valid": true},
		map[string]any{"model": map[string]any{"selected": "selected-model"}, "admitted": false},
		map[string]any{
			"model":    map[string]any{"requested": nil, "selected": "selected-model"},
			"admitted": true, "valid": false,
		},
		map[string]any{
			"model":    map[string]any{"requested": "selected-model"},
			"admitted": true, "valid": true, "run_error": errors.New("child failed"),
		},
		map[string]any{
			"model":    map[string]any{"requested": "selected-model"},
			"admitted": true, "valid": true,
			"scope":      []any{map[string]any{"path": "a.go"}, map[string]any{"path": ""}, "bad-scope"},
			"structured": map[string]any{"reviewed_files": []any{"a.go", "outside.go"}},
		},
	}
	run := &workflows.Run{Steps: map[string]workflows.StepExecution{
		"review": {Outputs: map[string]any{"managed_children": children}},
	}}
	controller.recordManagedChildOutcomes(automation.ID, automation.ActiveRunID, run, index)
	updated, found, getErr := store.GetAutomation(t.Context(), automation.ID)
	if getErr != nil || !found {
		t.Fatalf("managed automation found=%v err=%v", found, getErr)
	}
	stats := updated.ModelStats["cheap"]
	if stats.Failures != 2 || stats.Requests < stats.Failures || stats.ReviewedFiles < 1 {
		t.Fatalf("managed child stats=%#v automation=%#v", stats, updated)
	}

	delete(controller.active, automation.ID)
	controller.recordManagedChildOutcomes(automation.ID, automation.ActiveRunID, run, index)
	controller.requestSafeStop("missing", "run", repoaudit.RepositoryReviewPauseManual, "stop")
}

func TestRepositoryReviewCoverageReconcileBranches(t *testing.T) {
	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	store, storeErr := handler.repositoryReviewStore()
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	cfg, loadErr := config.LoadConfig(handler.configPath)
	if loadErr != nil {
		t.Fatal(loadErr)
	}

	nilConfigController := newRepositoryReviewController(handler)
	nilConfigController.reconcile()

	stale := repositoryReviewCoverageRunningAutomation(t, store, "run-stale-reconcile", false)
	controller := newRepositoryReviewController(handler)
	controller.leasedStore = store
	controller.leasedConfig = cfg
	controller.now = func() time.Time { return time.Now().UTC() }
	controller.reconcile()
	stale, found, getErr := store.GetAutomation(t.Context(), stale.ID)
	if getErr != nil || !found || stale.Status != repoaudit.RepositoryReviewAutomationPaused ||
		stale.PauseReason != repoaudit.RepositoryReviewPauseServiceRestart {
		t.Fatalf("stale reconcile=%#v found=%v err=%v", stale, found, getErr)
	}

	stoppingInput := testRepositoryReviewAutomation()
	stoppingInput.Status = repoaudit.RepositoryReviewAutomationStopping
	stoppingInput.ActiveRunID = "run-stopping-reconcile"
	stoppingInput.RunIDs = []string{stoppingInput.ActiveRunID}
	stoppingInput.RequestedPauseReason = repoaudit.RepositoryReviewPauseManual
	stoppingInput.RequestedPauseDetail = "requested manual pause"
	stopping, createErr := store.CreateAutomation(t.Context(), stoppingInput)
	if createErr != nil {
		t.Fatal(createErr)
	}
	controller.reconcile()
	stopping, found, getErr = store.GetAutomation(t.Context(), stopping.ID)
	if getErr != nil || !found || stopping.Status != repoaudit.RepositoryReviewAutomationPaused ||
		stopping.PauseReason != repoaudit.RepositoryReviewPauseManual {
		t.Fatalf("stopping reconcile=%#v found=%v err=%v", stopping, found, getErr)
	}

	quotaInput := testRepositoryReviewAutomation()
	quotaInput.Status = repoaudit.RepositoryReviewAutomationRunning
	quotaInput.ActiveRunID = "run-quota-reconcile"
	quotaInput.RunIDs = []string{quotaInput.ActiveRunID}
	quotaInput.BudgetPolicy.AccountIDs = []string{"work"}
	quotaInput.BudgetPolicy.MinRemainingPercent = 10
	quota, createErr := store.CreateAutomation(t.Context(), quotaInput)
	if createErr != nil {
		t.Fatal(createErr)
	}
	controller.active[quota.ID] = &repositoryReviewActiveRun{runID: quota.ActiveRunID, store: store}
	controller.probe = func(context.Context) (codexAccountLimitsResponse, error) {
		return codexAccountLimitsResponse{Accounts: []codexAccountLimitAccount{{
			ID: "work", Entries: []codexAccountLimitEntry{{
				Status: "available", Window: "weekly", UsedPercent: ptrInt(100),
			}},
		}}}, nil
	}
	controller.reconcile()
	quota, found, getErr = store.GetAutomation(t.Context(), quota.ID)
	if getErr != nil || !found || quota.Status != repoaudit.RepositoryReviewAutomationStopping ||
		quota.RequestedPauseReason != repoaudit.RepositoryReviewPauseAccountLimit {
		t.Fatalf("quota reconcile=%#v found=%v err=%v", quota, found, getErr)
	}

	badWorkspace := t.TempDir()
	if writeErr := os.WriteFile(
		filepath.Join(badWorkspace, "repository_reviews"),
		[]byte("not a directory"),
		0o600,
	); writeErr != nil {
		t.Fatal(writeErr)
	}
	badController := newRepositoryReviewController(handler)
	badController.leasedConfig = cfg
	badController.leasedStore = repoaudit.NewStore(badWorkspace)
	badController.reconcile()
}

func TestRepositoryReviewCoverageControllerLifecycleBoundaries(t *testing.T) {
	var nilHandler *Handler
	nilHandler.StartRepositoryReviewController()
	nilHandler.stopRepositoryReviewController()
	if nilHandler.repositoryReviewControllerInstance() != nil {
		t.Fatal("nil handler created a controller")
	}

	var nilController *repositoryReviewController
	if startErr := nilController.Start(); startErr == nil {
		t.Fatal("nil controller started")
	}
	nilController.Stop()
	if _, _, storeErr := nilController.store(); storeErr == nil {
		t.Fatal("nil controller returned a store")
	}

	badHandler := NewHandler(t.TempDir())
	badHandler.StartRepositoryReviewController()
	badHandler.stopRepositoryReviewController()

	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	controller := newRepositoryReviewController(handler)
	controller.stopped = true
	if startErr := controller.Start(); !errors.Is(startErr, context.Canceled) {
		t.Fatalf("stopped controller start error=%v", startErr)
	}
	if _, configErr := controller.currentLeasedConfiguration(); configErr == nil {
		t.Fatal("controller without lease returned config")
	}

	leased, loadErr := config.LoadConfig(handler.configPath)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	changed := *leased
	changed.Agents.Defaults.Workspace = t.TempDir()
	controller = newRepositoryReviewController(handler)
	controller.leasedConfig = &changed
	if _, configErr := controller.currentLeasedConfiguration(); configErr == nil ||
		!strings.Contains(configErr.Error(), "workspace changed") {
		t.Fatalf("changed workspace config error=%v", configErr)
	}

	canceledController := newRepositoryReviewController(handler)
	canceledController.cancel()
	canceledController.wg.Add(1)
	canceledController.monitor()
}

func TestRepositoryReviewCoverageFinishRunFailedPause(t *testing.T) {
	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	store, storeErr := handler.repositoryReviewStore()
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	automation := repositoryReviewCoverageRunningAutomation(t, store, "run-accounting-failed", false)
	controller := newRepositoryReviewController(handler)
	controller.active[automation.ID] = &repositoryReviewActiveRun{
		runID: automation.ActiveRunID, store: store,
		pauseReason: repoaudit.RepositoryReviewPauseRunFailed,
		pauseDetail: "usage accounting failed",
	}
	controller.finishAutomationRun(
		automation.ID,
		automation.ActiveRunID,
		&workflows.RunResult{Status: workflows.RunStatusSucceeded},
		nil,
		true,
	)
	updated, found, getErr := store.GetAutomation(t.Context(), automation.ID)
	if getErr != nil || !found || updated.Status != repoaudit.RepositoryReviewAutomationFailed ||
		!strings.Contains(updated.PauseDetail, "accounting") {
		t.Fatalf("accounting-failed finish=%#v found=%v err=%v", updated, found, getErr)
	}
}

func repositoryReviewCoverageLeasedController(
	t *testing.T,
	handler *Handler,
	store repoaudit.Store,
) *repositoryReviewController {
	t.Helper()
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}
	controller := newRepositoryReviewController(handler)
	controller.startOnce.Do(func() {})
	controller.leasedStore = store
	controller.leasedConfig = cfg
	return controller
}

func TestRepositoryReviewCoverageModelOptionEdgeBranches(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.AccountRef = "review-router"
	cfg.AccountRouters = []config.AccountRouterConfig{{
		Name: "review-router", Enabled: true, Entry: "pool",
		Blocks: []config.AccountRouterBlock{{
			ID:       "pool",
			Type:     config.AccountRouterBlockTypeLoadBalance,
			Accounts: []string{"api", "missing", "dynamic"},
		}},
	}}
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "api", Provider: "openai", Model: "openai/base", Enabled: true,
			InputPricePerMTok: 1.5, OutputPricePerMTok: 6,
		},
		{ModelName: "dynamic", Provider: config.ModelRouterProvider, Enabled: true},
	}
	cfg.ModelAliases = []config.ModelAliasConfig{
		{Name: "plain", Model: "plain-model"},
		{Name: "disabled", Model: "openai/disabled", DisabledAccounts: []string{"api"}},
	}
	options := repositoryReviewModelOptions(cfg)
	byAlias := make(map[string]repositoryReviewModelOption, len(options))
	for _, option := range options {
		byAlias[option.Alias] = option
	}
	if plain := byAlias["plain"]; !plain.Available || !plain.PriceKnown || plain.Provider != "openai" {
		t.Fatalf("plain option=%#v", plain)
	}
	if disabled := byAlias["disabled"]; disabled.Available || disabled.BlockedReason == "" {
		t.Fatalf("disabled option=%#v", disabled)
	}

	embedded := config.DefaultConfig()
	embedded.Agents.Defaults.AccountRef = "embedded-router"
	embedded.ModelList = []*config.ModelConfig{{
		ModelName: "embedded-router", Enabled: true,
		Router: &config.AccountRouterConfig{Entry: "entry", Blocks: []config.AccountRouterBlock{{
			ID: "entry", Type: config.AccountRouterBlockTypeAccount, Account: "direct",
		}}},
	}}
	if refs := repositoryReviewRuntimeAccountRefs(embedded); !reflect.DeepEqual(refs, []string{"direct"}) {
		t.Fatalf("embedded router refs=%#v", refs)
	}

	blankIndex := repositoryReviewAccountingIndex(
		&config.Config{ModelAliases: []config.ModelAliasConfig{{Name: "blank", Model: "  "}}},
		repoaudit.RepositoryReviewAutomation{ReviewerModels: []string{"blank"}},
	)
	if _, exists := blankIndex["blank"]; !exists {
		t.Fatalf("blank accounting index=%#v", blankIndex)
	}
}

func TestRepositoryReviewCoverageOptionsExposeCredentialLoadFailure(t *testing.T) {
	authHome := t.TempDir()
	t.Setenv("PICOCLAW_HOME", authHome)
	if err := os.WriteFile(filepath.Join(authHome, "auth.json"), []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	handler, mux, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/api/repository-reviews/automation-options",
		nil,
	))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "limits_error") {
		t.Fatalf("options credential failure=%d %s", response.Code, response.Body.String())
	}
}

func TestRepositoryReviewCoverageControllerTransitionEdges(t *testing.T) {
	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	createIdle := func(t *testing.T) repoaudit.RepositoryReviewAutomation {
		t.Helper()
		created, createErr := store.CreateAutomation(t.Context(), testRepositoryReviewAutomation())
		if createErr != nil {
			t.Fatal(createErr)
		}
		return created
	}

	controller := repositoryReviewCoverageLeasedController(t, handler, store)
	idle := createIdle(t)
	directCanceled := repositoryReviewCoverageLeasedController(t, handler, store)
	directCanceled.cancel()
	if _, startErr := directCanceled.startAutomation(
		t.Context(), idle.ID, idle.Version, false, "start",
	); !errors.Is(startErr, context.Canceled) {
		t.Fatalf("direct canceled start error=%v", startErr)
	}
	if _, startErr := controller.startAutomation(
		t.Context(),
		idle.ID,
		idle.Version+1,
		false,
		"start",
	); !errors.Is(
		startErr,
		repoaudit.ErrConflict,
	) {
		t.Fatalf("stale start error=%v", startErr)
	}
	if _, startErr := controller.startAutomation(
		t.Context(),
		idle.ID,
		idle.Version,
		false,
		"invalid",
	); !errors.Is(
		startErr,
		errRepositoryReviewInvalidTransition,
	) {
		t.Fatalf("invalid action error=%v", startErr)
	}
	controller.active[idle.ID] = &repositoryReviewActiveRun{runID: "already-active", store: store}
	if _, startErr := controller.startAutomation(
		t.Context(),
		idle.ID,
		idle.Version,
		false,
		"start",
	); !errors.Is(
		startErr,
		errRepositoryReviewAutomationBusy,
	) {
		t.Fatalf("locally active start error=%v", startErr)
	}
	delete(controller.active, idle.ID)

	resetFailure := repositoryReviewCoverageLeasedController(t, handler, store)
	resetFailure.update = func(
		context.Context,
		repoaudit.Store,
		string,
		int64,
		func(*repoaudit.RepositoryReviewAutomation) error,
	) (repoaudit.RepositoryReviewAutomation, error) {
		return repoaudit.RepositoryReviewAutomation{}, errors.New("reset persistence failed")
	}
	if _, startErr := resetFailure.startAutomation(
		t.Context(), idle.ID, idle.Version, true, "start",
	); startErr == nil || !strings.Contains(startErr.Error(), "reset persistence") {
		t.Fatalf("reset persistence error=%v", startErr)
	}

	transitionFailure := repositoryReviewCoverageLeasedController(t, handler, store)
	transitionFailure.update = func(
		context.Context,
		repoaudit.Store,
		string,
		int64,
		func(*repoaudit.RepositoryReviewAutomation) error,
	) (repoaudit.RepositoryReviewAutomation, error) {
		return repoaudit.RepositoryReviewAutomation{}, errors.New("transition persistence failed")
	}
	if _, startErr := transitionFailure.startAutomation(
		t.Context(), idle.ID, idle.Version, false, "start",
	); startErr == nil || !strings.Contains(startErr.Error(), "transition persistence") {
		t.Fatalf("transition persistence error=%v", startErr)
	}

	canceled := createIdle(t)
	canceled.BudgetPolicy.AccountIDs = []string{"work"}
	canceled, err = store.UpdateAutomation(
		t.Context(),
		canceled.ID,
		canceled.Version,
		func(candidate *repoaudit.RepositoryReviewAutomation) error {
			candidate.BudgetPolicy.AccountIDs = []string{"work"}
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	cancelController := repositoryReviewCoverageLeasedController(t, handler, store)
	cancelController.probe = func(context.Context) (codexAccountLimitsResponse, error) {
		cancelController.cancel()
		return codexAccountLimitsResponse{Accounts: []codexAccountLimitAccount{{ID: "work"}}}, nil
	}
	if _, startErr := cancelController.startAutomation(
		t.Context(), canceled.ID, canceled.Version, false, "start",
	); !errors.Is(startErr, context.Canceled) {
		t.Fatalf("canceled admission error=%v", startErr)
	}

	raced := createIdle(t)
	raced, err = store.UpdateAutomation(
		t.Context(),
		raced.ID,
		raced.Version,
		func(candidate *repoaudit.RepositoryReviewAutomation) error {
			candidate.BudgetPolicy.AccountIDs = []string{"work"}
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	raceController := repositoryReviewCoverageLeasedController(t, handler, store)
	raceController.probe = func(context.Context) (codexAccountLimitsResponse, error) {
		raceController.mu.Lock()
		raceController.active[raced.ID] = &repositoryReviewActiveRun{runID: "won-race", store: store}
		raceController.mu.Unlock()
		return codexAccountLimitsResponse{Accounts: []codexAccountLimitAccount{{ID: "work"}}}, nil
	}
	if _, startErr := raceController.startAutomation(
		t.Context(),
		raced.ID,
		raced.Version,
		false,
		"start",
	); !errors.Is(
		startErr,
		errRepositoryReviewAutomationBusy,
	) {
		t.Fatalf("raced admission error=%v", startErr)
	}

	stoppingInput := testRepositoryReviewAutomation()
	stoppingInput.Status = repoaudit.RepositoryReviewAutomationStopping
	stoppingInput.ActiveRunID = "run-already-stopping"
	stoppingInput.RunIDs = []string{stoppingInput.ActiveRunID}
	stoppingInput.RequestedPauseReason = repoaudit.RepositoryReviewPauseManual
	stopping, err := store.CreateAutomation(t.Context(), stoppingInput)
	if err != nil {
		t.Fatal(err)
	}
	pauseController := repositoryReviewCoverageLeasedController(t, handler, store)
	paused, pauseErr := pauseController.pauseAutomation(t.Context(), stopping.ID, stopping.Version)
	if pauseErr != nil || paused.Status != repoaudit.RepositoryReviewAutomationStopping {
		t.Fatalf("repeat pause=%#v err=%v", paused, pauseErr)
	}

	canceledPause := repositoryReviewCoverageLeasedController(t, handler, store)
	canceledPause.cancel()
	if _, pauseErr := canceledPause.pauseAutomation(
		t.Context(),
		stopping.ID,
		stopping.Version,
	); !errors.Is(
		pauseErr,
		context.Canceled,
	) {
		t.Fatalf("canceled pause error=%v", pauseErr)
	}

	staleGuard := idle
	staleGuard.Version++
	if _, guardErr := controller.pauseAtGuard(
		t.Context(), store, staleGuard, repoaudit.RepositoryReviewPauseTokenBudget,
		"stale guard", nil, time.Time{},
	); !errors.Is(guardErr, repoaudit.ErrConflict) {
		t.Fatalf("stale guard error=%v", guardErr)
	}
}

func TestRepositoryReviewCoverageConfigurationAndStoreErrors(t *testing.T) {
	handler, _, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	controller := repositoryReviewCoverageLeasedController(t, handler, store)
	valid, createErr := store.CreateAutomation(t.Context(), testRepositoryReviewAutomation())
	if createErr != nil {
		t.Fatal(createErr)
	}
	if _, startErr := controller.startAutomation(t.Context(), "invalid", 1, false, "start"); startErr == nil {
		t.Fatal("invalid automation ID started")
	}

	if err := os.WriteFile(handler.configPath, []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, configErr := controller.currentLeasedConfiguration(); configErr == nil {
		t.Fatal("corrupt leased configuration loaded")
	}
	if _, startErr := controller.startAutomation(
		t.Context(), valid.ID, valid.Version, false, "start",
	); startErr == nil {
		t.Fatal("automation started with corrupt current configuration")
	}

	badWorkspace := t.TempDir()
	badStore := repoaudit.NewStore(badWorkspace)
	bad := repositoryReviewCoverageRunningAutomation(t, badStore, "run-corrupt-usage", false)
	badPath := filepath.Join(
		badWorkspace,
		"repository_reviews",
		"automation_"+bad.ID+".json",
	)
	if err := os.WriteFile(badPath, []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	accountingController := newRepositoryReviewController(nil)
	accountingController.active[bad.ID] = &repositoryReviewActiveRun{
		runID: bad.ActiveRunID, store: badStore,
	}
	usageErr := accountingController.recordUsage(
		bad.ID,
		bad.ActiveRunID,
		workflows.AgentUsage{PromptTokens: 1, TotalTokens: 1},
		nil,
	)
	if !errors.Is(usageErr, errRepositoryReviewSafeStop) ||
		accountingController.active[bad.ID].pauseReason != repoaudit.RepositoryReviewPauseRunFailed {
		t.Fatalf("corrupt accounting error=%v active=%#v", usageErr, accountingController.active[bad.ID])
	}

	_ = workspace
}

func TestRepositoryReviewCoverageAccountingAndRetryEdges(t *testing.T) {
	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	controller := newRepositoryReviewController(handler)
	if usageErr := controller.recordUsage(
		"rra_missing", "run-missing", workflows.AgentUsage{TotalTokens: 1}, nil,
	); !errors.Is(usageErr, errRepositoryReviewSafeStop) {
		t.Fatalf("missing active accounting error=%v", usageErr)
	}

	mismatch := repositoryReviewCoverageRunningAutomation(t, store, "run-persisted", false)
	controller.active[mismatch.ID] = &repositoryReviewActiveRun{runID: "run-observer", store: store}
	if usageErr := controller.recordUsage(
		mismatch.ID,
		"run-observer",
		workflows.AgentUsage{PromptTokens: 4, TotalTokens: 4},
		map[string]repositoryReviewAccountingModel{"": {alias: "cheap", known: true}},
	); usageErr != nil {
		t.Fatalf("mismatched run accounting error=%v", usageErr)
	}
	unchanged, found, getErr := store.GetAutomation(t.Context(), mismatch.ID)
	if getErr != nil || !found || unchanged.Usage.TotalTokens != 0 {
		t.Fatalf("mismatched run persisted=%#v found=%v err=%v", unchanged, found, getErr)
	}

	conflicted, createErr := store.CreateAutomation(t.Context(), testRepositoryReviewAutomation())
	if createErr != nil {
		t.Fatal(createErr)
	}
	attempts := 0
	if _, retryErr := controller.updateLatest(
		t.Context(),
		store,
		conflicted.ID,
		func(*repoaudit.RepositoryReviewAutomation) error {
			attempts++
			return repoaudit.ErrConflict
		},
	); !errors.Is(retryErr, repoaudit.ErrConflict) ||
		attempts != 12 {
		t.Fatalf("conflict retries=%d err=%v", attempts, retryErr)
	}

	invalidOnly := &workflows.Run{Steps: map[string]workflows.StepExecution{
		"review": {Outputs: map[string]any{"managed_children": []any{"invalid-child"}}},
	}}
	controller.recordManagedChildOutcomes("missing", "run", invalidOnly, nil)
	missingAccounting := &workflows.Run{Steps: map[string]workflows.StepExecution{
		"review": {Outputs: map[string]any{"managed_children": []any{map[string]any{
			"model": map[string]any{"selected": "unpriced"}, "admitted": true, "valid": true,
		}}}},
	}}
	controller.recordManagedChildOutcomes("missing", "run", missingAccounting, nil)

	childMismatch := repositoryReviewCoverageRunningAutomation(t, store, "run-child-persisted", false)
	controller.active[childMismatch.ID] = &repositoryReviewActiveRun{runID: "run-child-observer", store: store}
	validChild := &workflows.Run{Steps: map[string]workflows.StepExecution{
		"review": {Outputs: map[string]any{"managed_children": []any{map[string]any{
			"model": map[string]any{"selected": "selected"}, "admitted": true, "valid": false,
		}}}},
	}}
	controller.recordManagedChildOutcomes(
		childMismatch.ID,
		"run-child-observer",
		validChild,
		map[string]repositoryReviewAccountingModel{"selected": {alias: "cheap", known: true}},
	)
	childUnchanged, found, getErr := store.GetAutomation(t.Context(), childMismatch.ID)
	if getErr != nil || !found || childUnchanged.ModelStats["cheap"].Failures != 0 {
		t.Fatalf("mismatched child outcome=%#v found=%v err=%v", childUnchanged, found, getErr)
	}
}

func TestRepositoryReviewCoverageOutcomeSelectionEdges(t *testing.T) {
	handler, _, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	seed := seedRepositoryReviewAPIState(t, workspace)
	store := repoaudit.NewStore(workspace)

	future := loadRepositoryReviewOutcome(store, repoaudit.RepositoryReviewAutomation{
		Repository: seed.Repository,
		RunIDs:     []string{"api-run"},
		StartedAt:  time.Now().UTC().Add(time.Hour),
	})
	if future.found {
		t.Fatalf("future campaign outcome=%#v", future)
	}

	code := repoaudit.FileRef{
		Path: "pkg/edge.go", BlobSHA: strings.Repeat("b", 40), SizeBytes: 80,
		Category: "code", Mode: "100644",
	}
	binary := repoaudit.FileRef{
		Path: "assets/edge.bin", BlobSHA: strings.Repeat("c", 40), SizeBytes: 16,
		Category: "binary", Mode: "100644",
	}
	plan, planErr := store.Plan(
		t.Context(), seed.Repository, "commit-edge", "inventory-edge", []repoaudit.FileRef{code, binary}, false,
	)
	if planErr != nil {
		t.Fatal(planErr)
	}
	line := 7
	recorded, recordErr := store.Record(t.Context(), repoaudit.RecordRequest{
		Plan: plan, RunID: "edge-run",
		UnsupportedFiles: []repoaudit.UnsupportedFile{{FileRef: binary, Reason: "binary fixture"}},
		Observations: []repoaudit.Observation{{
			Model: "edge-model", Reviewer: "edge-reviewer", ScopeFiles: []repoaudit.FileRef{code},
			Findings: []repoaudit.FindingCandidate{{
				Severity: "medium", Title: "Edge finding", File: code.Path, Line: &line,
				Evidence: "edge evidence", Impact: "edge impact", Recommendation: "edge recommendation",
				Validation: repoaudit.Validation{Status: "confirmed", Summary: "confirmed"},
			}},
		}},
	})
	if recordErr != nil {
		t.Fatal(recordErr)
	}
	outcome := loadRepositoryReviewOutcome(store, repoaudit.RepositoryReviewAutomation{
		Repository: seed.Repository, RunIDs: []string{"edge-run"}, ReviewerModels: []string{"other-model"},
	})
	if !outcome.found || outcome.unsupportedFiles != 1 || outcome.findings != 1 ||
		outcome.modelFindings["other-model"] != 0 {
		t.Fatalf("selected outcome=%#v state=%#v", outcome, recorded.State)
	}

	automation := repoaudit.RepositoryReviewAutomation{
		ModelStats:            make(map[string]repoaudit.RepositoryReviewModelStats),
		ModelCoverageSketches: make(map[string]string),
	}
	addRepositoryReviewModelPaths(&automation, "edge", []string{" ", "pkg/edge.go"})
	if automation.ModelStats["edge"].ReviewedFiles != 1 {
		t.Fatalf("blank-path sketch stats=%#v", automation.ModelStats["edge"])
	}
}

func TestRepositoryReviewCoverageQuotaProbeAndReconcileErrors(t *testing.T) {
	withPicoclawAuthHome(t)
	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	controller := repositoryReviewCoverageLeasedController(t, handler, store)
	controller.probe = nil
	quotaInput := testRepositoryReviewAutomation()
	quotaInput.BudgetPolicy.AccountIDs = []string{"work"}
	if snapshots, _, _, _, quotaErr := controller.checkQuota(
		t.Context(),
		quotaInput,
	); quotaErr != nil ||
		len(snapshots) != 0 {
		t.Fatalf("default empty quota snapshots=%#v err=%v", snapshots, quotaErr)
	}

	runningInput := testRepositoryReviewAutomation()
	runningInput.Status = repoaudit.RepositoryReviewAutomationRunning
	runningInput.ActiveRunID = "run-probe-error"
	runningInput.RunIDs = []string{runningInput.ActiveRunID}
	runningInput.BudgetPolicy.AccountIDs = []string{"work"}
	runningInput.BudgetPolicy.PauseOnUnknown = true
	running, createErr := store.CreateAutomation(t.Context(), runningInput)
	if createErr != nil {
		t.Fatal(createErr)
	}
	controller.active[running.ID] = &repositoryReviewActiveRun{runID: running.ActiveRunID, store: store}
	controller.probe = func(context.Context) (codexAccountLimitsResponse, error) {
		return codexAccountLimitsResponse{}, errors.New("telemetry offline")
	}
	controller.reconcile()
	updated, found, getErr := store.GetAutomation(t.Context(), running.ID)
	if getErr != nil || !found || updated.Status != repoaudit.RepositoryReviewAutomationStopping ||
		updated.RequestedPauseReason != repoaudit.RepositoryReviewPauseAccountLimit {
		t.Fatalf("probe-error reconcile=%#v found=%v err=%v", updated, found, getErr)
	}

	racedInput := testRepositoryReviewAutomation()
	racedInput.Status = repoaudit.RepositoryReviewAutomationRunning
	racedInput.ActiveRunID = "run-quota-original"
	racedInput.RunIDs = []string{racedInput.ActiveRunID}
	racedInput.BudgetPolicy.AccountIDs = []string{"work"}
	raced, createErr := store.CreateAutomation(t.Context(), racedInput)
	if createErr != nil {
		t.Fatal(createErr)
	}
	raceController := repositoryReviewCoverageLeasedController(t, handler, store)
	raceController.active[raced.ID] = &repositoryReviewActiveRun{runID: raced.ActiveRunID, store: store}
	raceController.probe = func(ctx context.Context) (codexAccountLimitsResponse, error) {
		var updateErr error
		raced, updateErr = store.UpdateAutomation(
			ctx,
			raced.ID,
			raced.Version,
			func(candidate *repoaudit.RepositoryReviewAutomation) error {
				candidate.ActiveRunID = "run-quota-replaced"
				candidate.RunIDs = append(candidate.RunIDs, candidate.ActiveRunID)
				return nil
			},
		)
		if updateErr != nil {
			return codexAccountLimitsResponse{}, updateErr
		}
		return codexAccountLimitsResponse{Accounts: []codexAccountLimitAccount{{ID: "work"}}}, nil
	}
	raceController.reconcile()
	raced, found, getErr = store.GetAutomation(t.Context(), raced.ID)
	if getErr != nil || !found || raced.ActiveRunID != "run-quota-replaced" || len(raced.AccountLimitSnapshots) != 0 {
		t.Fatalf("raced quota reconcile=%#v found=%v err=%v", raced, found, getErr)
	}

	canceledInput, createErr := store.CreateAutomation(t.Context(), testRepositoryReviewAutomation())
	if createErr != nil {
		t.Fatal(createErr)
	}
	canceledReconcile := repositoryReviewCoverageLeasedController(t, handler, store)
	canceledReconcile.now = func() time.Time {
		canceledReconcile.cancel()
		return time.Now().UTC()
	}
	canceledReconcile.reconcile()
	canceledInput, found, getErr = store.GetAutomation(context.Background(), canceledInput.ID)
	if getErr != nil || !found || canceledInput.Status != repoaudit.RepositoryReviewAutomationIdle {
		t.Fatalf("canceled reconcile automation=%#v found=%v err=%v", canceledInput, found, getErr)
	}
}

func TestRepositoryReviewCoverageWorkflowObservers(t *testing.T) {
	if limitsError := repositoryReviewLimitsError(
		codexAccountLimitsResponse{Error: "partial telemetry"}, nil,
	); limitsError != "partial telemetry" {
		t.Fatalf("projected limits error=%q", limitsError)
	}
	if limitsError := repositoryReviewLimitsError(codexAccountLimitsResponse{}, nil); limitsError != "" {
		t.Fatalf("empty limits error=%q", limitsError)
	}
	if reason, detail, pause := repositoryReviewFinalPause(
		"", "", repoaudit.RepositoryReviewAutomationStopping,
	); !pause || reason != repoaudit.RepositoryReviewPauseManual || detail == "" {
		t.Fatalf("stopping fallback reason=%q detail=%q pause=%v", reason, detail, pause)
	}
	if _, _, pause := repositoryReviewFinalPause(
		"", "", repoaudit.RepositoryReviewAutomationRunning,
	); pause {
		t.Fatal("running automation received a final pause")
	}

	usageCalls := 0
	wantUsageErr := errors.New("usage stopped")
	usageObserver := repositoryReviewAgentUsageObserver("run-target", func(usage workflows.AgentUsage) error {
		usageCalls++
		if usage.TotalTokens != 7 {
			t.Fatalf("usage=%#v", usage)
		}
		return wantUsageErr
	})
	if observeErr := usageObserver(workflows.AgentUsageEvent{
		RunID: "run-other", Usage: workflows.AgentUsage{TotalTokens: 3},
	}); observeErr != nil || usageCalls != 0 {
		t.Fatalf("unrelated usage calls=%d err=%v", usageCalls, observeErr)
	}
	if observeErr := usageObserver(workflows.AgentUsageEvent{
		RunID: "run-target", Usage: workflows.AgentUsage{TotalTokens: 7},
	}); !errors.Is(observeErr, wantUsageErr) || usageCalls != 1 {
		t.Fatalf("target usage calls=%d err=%v", usageCalls, observeErr)
	}

	admissionCalls := 0
	wantAdmissionErr := errors.New("admission stopped")
	admissionObserver := repositoryReviewAgentCallAdmissionObserver("run-target", func() error {
		admissionCalls++
		return wantAdmissionErr
	})
	if admitErr := admissionObserver(
		workflows.AgentCallAdmissionEvent{RunID: "run-other"},
	); admitErr != nil ||
		admissionCalls != 0 {
		t.Fatalf("unrelated admission calls=%d err=%v", admissionCalls, admitErr)
	}
	if admitErr := admissionObserver(
		workflows.AgentCallAdmissionEvent{RunID: "run-target"},
	); !errors.Is(admitErr, wantAdmissionErr) ||
		admissionCalls != 1 {
		t.Fatalf("target admission calls=%d err=%v", admissionCalls, admitErr)
	}
}

func TestRepositoryReviewCoverageProgressMonitorMissingAndDuplicateStage(t *testing.T) {
	handler, _, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	automation := repositoryReviewCoverageRunningAutomation(t, store, "run-progress-edges", false)
	workflowStore := workflows.NewFileRunStore(workspace)
	controller := newRepositoryReviewController(handler)
	controller.progressEvery = 2 * time.Millisecond
	monitorCtx, cancelMonitor := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		controller.monitorWorkflowProgress(
			monitorCtx, store, workflowStore, automation.ID, automation.ActiveRunID,
		)
		close(done)
	}()
	time.Sleep(10 * time.Millisecond)
	if createErr := workflowStore.CreateRun(t.Context(), &workflows.Run{
		ID: automation.ActiveRunID, WorkflowRef: workflows.RepositoryBugFinderWorkflowRef,
		Status: workflows.RunStatusRunning,
		Steps: map[string]workflows.StepExecution{
			"review": {ID: "review", Status: workflows.RunStatusSucceeded},
		},
	}); createErr != nil {
		cancelMonitor()
		<-done
		t.Fatal(createErr)
	}
	deadline := time.Now().Add(time.Second)
	for {
		current, found, getErr := store.GetAutomation(t.Context(), automation.ID)
		if getErr != nil {
			cancelMonitor()
			<-done
			t.Fatal(getErr)
		}
		if found && current.Progress.Stage == "Reviewing bounded file batch" {
			break
		}
		if time.Now().After(deadline) {
			cancelMonitor()
			<-done
			t.Fatalf("progress stage never updated: %#v", current.Progress)
		}
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(10 * time.Millisecond)
	cancelMonitor()
	<-done

	succeeded := &workflows.Run{Steps: map[string]workflows.StepExecution{
		"record": {Status: workflows.RunStatusSucceeded},
	}}
	if stage := repositoryReviewWorkflowStage(succeeded); stage != "Checkpointing findings" {
		t.Fatalf("succeeded stage=%q", stage)
	}
}

func TestRepositoryReviewCoverageFinishMismatchedRunAndControllerTiming(t *testing.T) {
	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	mismatch := repositoryReviewCoverageRunningAutomation(t, store, "run-finish-persisted", false)
	controller := newRepositoryReviewController(handler)
	controller.active[mismatch.ID] = &repositoryReviewActiveRun{runID: "run-finish-observer", store: store}
	controller.finishAutomationRun(
		mismatch.ID,
		"run-finish-observer",
		&workflows.RunResult{Status: workflows.RunStatusSucceeded},
		nil,
		true,
	)
	mismatch, found, getErr := store.GetAutomation(t.Context(), mismatch.ID)
	if getErr != nil || !found || mismatch.ActiveRunID != "run-finish-persisted" {
		t.Fatalf("mismatched finish=%#v found=%v err=%v", mismatch, found, getErr)
	}

	timedStop := newRepositoryReviewController(nil)
	timedStop.stopTimeout = time.Millisecond
	timedStop.wg.Add(1)
	timedStop.Stop()
	timedStop.wg.Done()

	monitor := repositoryReviewCoverageLeasedController(t, handler, store)
	monitor.monitorEvery = 2 * time.Millisecond
	monitor.wg.Add(1)
	monitorDone := make(chan struct{})
	go func() {
		monitor.monitor()
		close(monitorDone)
	}()
	time.Sleep(10 * time.Millisecond)
	monitor.cancel()
	<-monitorDone
}
