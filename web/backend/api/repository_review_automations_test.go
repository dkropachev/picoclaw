package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestRepositoryReviewAutomationRoutesCreateUpdateListAndDelete(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	profile := createRepositoryReviewProfileForTest(t, mux, "Core pre-review", "cheap")

	create := repositoryReviewAutomationMutation(t, mux, http.MethodPost,
		"/api/repository-reviews/automations", map[string]any{
			"repository": "https://github.com/acme/core.git",
			"branch":     "main",
			"profile_id": profile.ID,
		})
	if create.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", create.Code, create.Body.String())
	}
	var created struct {
		Automation repoaudit.RepositoryReviewAutomation `json:"automation"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Automation.ID == "" || created.Automation.Status != repoaudit.RepositoryReviewAutomationIdle ||
		created.Automation.ProfileID != profile.ID ||
		created.Automation.ProfileVersion != profile.Version ||
		created.Automation.Ref != "main" || created.Automation.Target != "all" ||
		len(created.Automation.ReviewerModels) != 1 ||
		created.Automation.ReviewerModels[0] != profile.ReviewerModel ||
		created.Automation.CompareModels {
		t.Fatalf("created automation=%#v", created.Automation)
	}
	statePath := filepath.Join(workspace, "repository_reviews", "automation_"+created.Automation.ID+".json")
	if info, err := os.Stat(statePath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("automation file info=%v err=%v", info, err)
	}

	list := httptest.NewRecorder()
	mux.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/repository-reviews/automations", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), created.Automation.ID) {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}

	updateBody := map[string]any{
		"repository":       created.Automation.Repository,
		"branch":           "release/v2",
		"profile_id":       profile.ID,
		"expected_version": created.Automation.Version,
	}
	update := repositoryReviewAutomationMutation(t, mux, http.MethodPatch,
		"/api/repository-reviews/automations/"+created.Automation.ID, updateBody)
	if update.Code != http.StatusOK || !strings.Contains(update.Body.String(), "release/v2") {
		t.Fatalf("update status=%d body=%s", update.Code, update.Body.String())
	}
	var changed struct {
		Automation repoaudit.RepositoryReviewAutomation `json:"automation"`
	}
	if err := json.Unmarshal(update.Body.Bytes(), &changed); err != nil {
		t.Fatal(err)
	}

	stale := repositoryReviewAutomationMutation(t, mux, http.MethodPatch,
		"/api/repository-reviews/automations/"+created.Automation.ID, updateBody)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale update status=%d body=%s", stale.Code, stale.Body.String())
	}
	deleted := repositoryReviewAutomationMutation(t, mux, http.MethodDelete,
		"/api/repository-reviews/automations/"+created.Automation.ID,
		map[string]any{"expected_version": changed.Automation.Version})
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
}

func TestRepositoryReviewAutomationRoutesRejectInvalidStateTransitionsAndBodies(t *testing.T) {
	handler, mux, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	store, storeErr := handler.repositoryReviewStore()
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	idle, err := store.CreateAutomation(t.Context(), testRepositoryReviewAutomation())
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"pause", "resume", "restart"} {
		response := repositoryReviewAutomationMutation(t, mux, http.MethodPost,
			"/api/repository-reviews/automations/"+idle.ID+"/"+action,
			map[string]any{"expected_version": idle.Version})
		if response.Code != http.StatusConflict {
			t.Fatalf("idle %s status=%d body=%s", action, response.Code, response.Body.String())
		}
	}
	pausedInput := testRepositoryReviewAutomation()
	pausedInput.Status = repoaudit.RepositoryReviewAutomationPaused
	pausedInput.PauseReason = repoaudit.RepositoryReviewPauseManual
	pausedInput.PauseDetail = "paused"
	paused, err := store.CreateAutomation(t.Context(), pausedInput)
	if err != nil {
		t.Fatal(err)
	}
	response := repositoryReviewAutomationMutation(t, mux, http.MethodPost,
		"/api/repository-reviews/automations/"+paused.ID+"/start",
		map[string]any{"expected_version": paused.Version})
	if response.Code != http.StatusConflict {
		t.Fatalf("paused start status=%d body=%s", response.Code, response.Body.String())
	}

	for _, body := range []string{"", `{`, `{"expected_version":"wrong"}`, `{} {}`} {
		request := httptest.NewRequest(
			http.MethodPost,
			"/api/repository-reviews/automations/"+idle.ID+"/start",
			strings.NewReader(body),
		)
		setRepositoryReviewMutationHeaders(request)
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body %q status=%d response=%s", body, recorder.Code, recorder.Body.String())
		}
	}
	invalidScope := automationConfigBody(testRepositoryReviewAutomation())
	invalidScope["scope_policy"] = map[string]any{
		"code_types": []string{"code"}, "include_folders": []string{"../outside"},
	}
	response = repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, "/api/repository-reviews/automations", invalidScope,
	)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unsafe scope status=%d body=%s", response.Code, response.Body.String())
	}
	forgedPlan := automationConfigBody(testRepositoryReviewAutomation())
	forgedPlan["scope_plan"] = map[string]any{"summary": "client supplied"}
	response = repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, "/api/repository-reviews/automations", forgedPlan,
	)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("client scope plan status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRepositoryReviewAutomationScopeChangeClearsCommitPlan(t *testing.T) {
	handler, mux, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	automation, err := store.CreateAutomation(t.Context(), testRepositoryReviewAutomation())
	if err != nil {
		t.Fatal(err)
	}
	automation, err = store.UpdateAutomation(
		t.Context(), automation.ID, automation.Version,
		func(candidate *repoaudit.RepositoryReviewAutomation) error {
			candidate.ScopePlan = repoaudit.RepositoryReviewScopePlan{
				CommitSHA: strings.Repeat("a", 40), PolicyHash: strings.Repeat("b", 64),
				Hash: strings.Repeat("c", 64), Summary: "production files selected",
			}
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	equivalentBody := automationConfigBody(automation)
	equivalentBody["scope_policy"] = map[string]any{
		"code_types":      []string{" CODE ", "hotpath-code"},
		"include_folders": []string{}, "exclude_folders": []string{},
	}
	equivalentBody["expected_version"] = automation.Version
	equivalentResponse := repositoryReviewAutomationMutation(
		t, mux, http.MethodPatch, "/api/repository-reviews/automations/"+automation.ID,
		equivalentBody,
	)
	if equivalentResponse.Code != http.StatusOK {
		t.Fatalf("equivalent scope update status=%d body=%s", equivalentResponse.Code, equivalentResponse.Body.String())
	}
	var equivalent struct {
		Automation repoaudit.RepositoryReviewAutomation `json:"automation"`
	}
	if err := json.Unmarshal(equivalentResponse.Body.Bytes(), &equivalent); err != nil {
		t.Fatal(err)
	}
	if equivalent.Automation.ScopePlan.Hash == "" {
		t.Fatalf("equivalent normalized scope cleared plan: %#v", equivalent.Automation.ScopePlan)
	}
	automation = equivalent.Automation
	body := automationConfigBody(automation)
	policy := automation.ScopePolicy
	policy.FreeText = "Focus on storage boundaries."
	body["scope_policy"] = policy
	body["expected_version"] = automation.Version
	response := repositoryReviewAutomationMutation(
		t, mux, http.MethodPatch, "/api/repository-reviews/automations/"+automation.ID, body,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("scope update status=%d body=%s", response.Code, response.Body.String())
	}
	var result struct {
		Automation repoaudit.RepositoryReviewAutomation `json:"automation"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Automation.ScopePolicy.FreeText != "Focus on storage boundaries." ||
		result.Automation.ScopePlan.CommitSHA != "" || result.Automation.ScopePlan.Hash != "" ||
		len(result.Automation.ScopePlan.Warnings) != 0 {
		t.Fatalf("scope update = %#v", result.Automation)
	}
}

func TestRepositoryReviewAutomationStartPersistsTokenBudgetPause(t *testing.T) {
	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	controller := handler.repositoryReviewControllerInstance()
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	automation := testRepositoryReviewAutomation()
	automation.Status = repoaudit.RepositoryReviewAutomationRunning
	automation.ActiveRunID = "wr_guard"
	automation.RunIDs = []string{"wr_guard"}
	automation.BudgetPolicy.GuardExpression = "spent.tokens.total < 100"
	automation.Usage = repoaudit.RepositoryReviewTokenUsage{
		PromptTokens: 80, CompletionTokens: 20, TotalTokens: 100,
	}
	automation, err = store.CreateAutomation(t.Context(), automation)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}
	controller.mu.Lock()
	controller.active[automation.ID] = &repositoryReviewActiveRun{
		runID: "wr_guard", store: store, config: cfg,
		reservations: make(map[int]repositoryReviewTaskReservation),
	}
	controller.mu.Unlock()
	guardErr := controller.observeRepositoryReviewTask(
		automation.ID, "wr_guard", workflows.ManagedChildActivity{
			Phase: workflows.ManagedChildStarted, Index: 1, EstimatedPromptTokens: 1,
		},
	)
	if !errors.Is(guardErr, errRepositoryReviewSafeStop) {
		t.Fatalf("guard error=%v", guardErr)
	}
	updated, _, err := store.GetAutomation(t.Context(), automation.ID)
	if err != nil || updated.Status != repoaudit.RepositoryReviewAutomationStopping ||
		updated.RequestedPauseReason != repoaudit.RepositoryReviewPauseGuardExpression {
		t.Fatalf("guarded automation=%#v err=%v", updated, err)
	}
}

func TestRepositoryReviewAutomationUsageTriggersSafeCheckpointStopAndComparison(t *testing.T) {
	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	controller := handler.repositoryReviewControllerInstance()
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	automation := testRepositoryReviewAutomation()
	automation.BudgetPolicy.GuardExpression = "spent.tokens.total < 100"
	automation.ModelPrices = map[string]repoaudit.RepositoryReviewModelPrice{
		"cheap": {InputPricePer1M: 1, OutputPricePer1M: 4},
	}
	automation.Status = repoaudit.RepositoryReviewAutomationRunning
	automation.ActiveRunID = "wr_usage"
	automation.RunIDs = []string{"wr_usage"}
	automation.Progress.TotalBatches = 1
	automation, createErr := store.CreateAutomation(t.Context(), automation)
	if createErr != nil {
		t.Fatal(createErr)
	}
	controller.mu.Lock()
	controller.active[automation.ID] = &repositoryReviewActiveRun{runID: "wr_usage", store: store}
	controller.mu.Unlock()
	stopErr := controller.recordUsage(automation.ID, "wr_usage", workflows.AgentUsage{
		Model: "cheap", PromptTokens: 80, CompletionTokens: 25, TotalTokens: 105, CachedTokens: 10,
	}, repositoryReviewAccountingIndex(nil, automation))
	if stopErr != nil {
		t.Fatalf("recordUsage error=%v", stopErr)
	}

	updated, found, err := store.GetAutomation(t.Context(), automation.ID)
	if err != nil || !found {
		t.Fatalf("GetAutomation found=%v err=%v", found, err)
	}
	stats := updated.ModelStats["cheap"]
	if updated.Status != repoaudit.RepositoryReviewAutomationRunning ||
		updated.Usage.TotalTokens != 105 || stats.Requests != 1 || stats.Tokens.CachedTokens != 10 ||
		math.Abs(stats.EstimatedCostUSD-0.00018) > 0.0000001 {
		t.Fatalf("usage automation=%#v stats=%#v", updated, stats)
	}
	controller.mu.Lock()
	active := controller.active[automation.ID]
	controller.mu.Unlock()
	if active == nil || active.pauseReason != "" {
		t.Fatalf("active stop=%#v", active)
	}
	if err := controller.observeRepositoryReviewTask(
		automation.ID, "wr_usage", workflows.ManagedChildActivity{
			Phase: workflows.ManagedChildStarted, Index: 1, EstimatedPromptTokens: 1,
		},
	); !errors.Is(err, errRepositoryReviewSafeStop) {
		t.Fatalf("next task guard error=%v", err)
	}
}

func TestRepositoryReviewTaskAdmissionReservesConcurrentWorkerUsage(t *testing.T) {
	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	controller := handler.repositoryReviewControllerInstance()
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	automation := testRepositoryReviewAutomation()
	automation.Status = repoaudit.RepositoryReviewAutomationRunning
	automation.ActiveRunID = "wr_reservations"
	automation.RunIDs = []string{automation.ActiveRunID}
	automation.BudgetPolicy.GuardExpression = "spent.tokens.total < 2500"
	automation, err = store.CreateAutomation(t.Context(), automation)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}
	controller.mu.Lock()
	controller.active[automation.ID] = &repositoryReviewActiveRun{
		runID: automation.ActiveRunID, store: store, config: cfg,
		reservations: make(map[int]repositoryReviewTaskReservation),
	}
	controller.mu.Unlock()
	activity := workflows.ManagedChildActivity{
		Phase: workflows.ManagedChildStarted, EstimatedPromptTokens: 1_000,
		EstimatedOutputTokens: 500, PriceKnown: true,
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for index := 1; index <= 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			candidate := activity
			candidate.Index = index
			results <- controller.observeRepositoryReviewTask(
				automation.ID, automation.ActiveRunID, candidate,
			)
		}(index)
	}
	close(start)
	wait.Wait()
	close(results)
	admitted, denied := 0, 0
	for result := range results {
		if result == nil {
			admitted++
		} else if errors.Is(result, errRepositoryReviewSafeStop) {
			denied++
		} else {
			t.Fatalf("unexpected task admission error=%v", result)
		}
	}
	if admitted != 1 || denied != 1 {
		t.Fatalf("concurrent admissions admitted=%d denied=%d", admitted, denied)
	}
	controller.mu.Lock()
	reservations := len(controller.active[automation.ID].reservations)
	admittedIndex := 0
	for index := range controller.active[automation.ID].reservations {
		admittedIndex = index
	}
	controller.mu.Unlock()
	if reservations != 1 {
		t.Fatalf("in-flight reservations=%d, want one admitted task", reservations)
	}
	if err := controller.admitProviderCall(automation.ID, automation.ActiveRunID); err != nil {
		t.Fatalf("guard pause interrupted an already admitted task: %v", err)
	}
	if err := controller.observeRepositoryReviewTask(
		automation.ID, automation.ActiveRunID,
		workflows.ManagedChildActivity{Phase: workflows.ManagedChildCompleted, Index: admittedIndex, Success: true},
	); err != nil {
		t.Fatal(err)
	}
	controller.mu.Lock()
	reservations = len(controller.active[automation.ID].reservations)
	controller.mu.Unlock()
	if reservations != 0 {
		t.Fatalf("completed task retained %d reservations", reservations)
	}
}

func TestRepositoryReviewUnmappedModelStillConsumesGlobalTokenBudget(t *testing.T) {
	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	controller := handler.repositoryReviewControllerInstance()
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	automation := testRepositoryReviewAutomation()
	automation.Status = repoaudit.RepositoryReviewAutomationRunning
	automation.ActiveRunID = "wr_unmapped"
	automation.RunIDs = []string{"wr_unmapped"}
	automation.Progress.TotalBatches = 1
	automation.BudgetPolicy.GuardExpression = "spent.tokens.total < 10"
	automation, err = store.CreateAutomation(t.Context(), automation)
	if err != nil {
		t.Fatal(err)
	}
	controller.mu.Lock()
	controller.active[automation.ID] = &repositoryReviewActiveRun{runID: "wr_unmapped", store: store}
	controller.mu.Unlock()
	stopErr := controller.recordUsage(automation.ID, "wr_unmapped", workflows.AgentUsage{
		Model: "unexpected-fallback", PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10,
	}, map[string]repositoryReviewAccountingModel{})
	if stopErr != nil {
		t.Fatalf("recordUsage error=%v", stopErr)
	}
	updated, _, err := store.GetAutomation(t.Context(), automation.ID)
	if err != nil || updated.Usage.TotalTokens != 10 ||
		updated.Status != repoaudit.RepositoryReviewAutomationRunning {
		t.Fatalf("unmapped usage=%#v err=%v", updated, err)
	}
}

func TestRepositoryReviewGuardReservationUsesConservativeAutomationPrice(t *testing.T) {
	automation := testRepositoryReviewAutomation()
	automation.ModelPrices = map[string]repoaudit.RepositoryReviewModelPrice{
		"cheap": {InputPricePer1M: 10, OutputPricePer1M: 20},
	}
	reservation := repositoryReviewGuardReservation(
		automation,
		workflows.ManagedChildActivity{
			ModelAlias: "cheap", EstimatedPromptTokens: 1_000, EstimatedOutputTokens: 500,
			EstimatedCostUSD: 0.000001, PriceKnown: true,
		},
	)
	if !reservation.CostKnown || math.Abs(reservation.CostUSD-0.02) > 0.0000001 {
		t.Fatalf("reservation=%#v, want conservative snapshot cost", reservation)
	}
}

func TestRepositoryReviewModelOutcomeUsesRequestedReviewerAndAcknowledgedZeroFindingCoverage(t *testing.T) {
	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	controller := handler.repositoryReviewControllerInstance()
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	automation := testRepositoryReviewAutomation()
	automation.Status = repoaudit.RepositoryReviewAutomationRunning
	automation.ActiveRunID = "wr_outcome"
	automation.RunIDs = []string{"wr_outcome"}
	automation.Progress.TotalBatches = 1
	automation, err = store.CreateAutomation(t.Context(), automation)
	if err != nil {
		t.Fatal(err)
	}
	controller.mu.Lock()
	controller.active[automation.ID] = &repositoryReviewActiveRun{runID: "wr_outcome", store: store}
	controller.mu.Unlock()
	run := &workflows.Run{Steps: map[string]workflows.StepExecution{
		"find_bugs/review": {
			ID: "review", Status: workflows.RunStatusSucceeded,
			Outputs: map[string]any{
				"managed_children": []map[string]any{{
					"admitted": true, "valid": true,
					"model": map[string]any{"requested": "cheap", "selected": "fallback-model"},
					"scope": []any{map[string]any{"path": "a.go"}},
					"structured": map[string]any{
						"reviewedFiles": []any{"a.go"}, "findings": []any{},
					},
				}},
			},
		},
	}}
	controller.recordManagedChildOutcomes(
		automation.ID, "wr_outcome", run, repositoryReviewAccountingIndex(nil, automation),
	)
	controller.recordManagedChildOutcomes(
		automation.ID, "wr_outcome", run, repositoryReviewAccountingIndex(nil, automation),
	)
	updated, _, err := store.GetAutomation(t.Context(), automation.ID)
	if err != nil {
		t.Fatal(err)
	}
	stats := updated.ModelStats["cheap"]
	if stats.ReviewedFiles != 1 || stats.Findings != 0 || stats.Failures != 0 {
		t.Fatalf("requested reviewer stats=%#v", stats)
	}
	if updated.ModelCoverageSketches["cheap"] == "" {
		t.Fatal("durable model coverage sketch was not stored")
	}
	if projected := projectRepositoryReviewAutomation(updated); projected.ModelCoverageSketches != nil {
		t.Fatalf("API projection exposed internal coverage sketch: %#v", projected.ModelCoverageSketches)
	}
}

func TestRepositoryReviewAutomationStartAutoContinuesBoundedBatches(t *testing.T) {
	handler, mux, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	controller := handler.repositoryReviewControllerInstance()
	controller.probe = func(context.Context) (codexAccountLimitsResponse, error) {
		return codexAccountLimitsResponse{}, nil
	}
	var calls atomic.Int32
	controller.runBatch = func(
		_ context.Context,
		automation repoaudit.RepositoryReviewAutomation,
		runID string,
		observe workflows.AgentUsageObserver,
	) (*workflows.RunResult, error) {
		call := calls.Add(1)
		if automation.ID == "" || runID == "" {
			t.Fatal("fake batch received empty automation or run identity")
		}
		if err := observe(workflows.AgentUsage{
			Model: "cheap", PromptTokens: 40, CompletionTokens: 10, TotalTokens: 50,
		}); err != nil {
			return nil, err
		}
		remaining := 2
		reviewed := 1
		if call == 1 {
			cfg, err := config.LoadConfig(handler.configPath)
			if err != nil {
				return nil, err
			}
			cfg.ModelList[0].InputPricePerMTok = 9
			cfg.ModelList[0].OutputPricePerMTok = 13
			if err := config.SaveConfig(handler.configPath, cfg); err != nil {
				return nil, err
			}
		}
		if call == 2 {
			remaining = 0
			reviewed = 2
		}
		return &workflows.RunResult{
			RunID: runID, Status: workflows.RunStatusSucceeded,
			Outputs: map[string]any{"remainingFiles": remaining, "reviewedFiles": reviewed},
		}, nil
	}

	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	automation := testRepositoryReviewAutomation()
	automation.ModelPrices = map[string]repoaudit.RepositoryReviewModelPrice{
		"cheap": {InputPricePer1M: 7, OutputPricePer1M: 11},
	}
	automation, err = store.CreateAutomation(t.Context(), automation)
	if err != nil {
		t.Fatal(err)
	}
	response := repositoryReviewAutomationMutation(t, mux, http.MethodPost,
		"/api/repository-reviews/automations/"+automation.ID+"/start",
		map[string]any{"expected_version": automation.Version})
	if response.Code != http.StatusAccepted {
		t.Fatalf("start status=%d body=%s", response.Code, response.Body.String())
	}

	deadline := time.Now().Add(3 * time.Second)
	var completed repoaudit.RepositoryReviewAutomation
	for time.Now().Before(deadline) {
		current, found, getErr := store.GetAutomation(t.Context(), automation.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if found && current.Status == repoaudit.RepositoryReviewAutomationCompleted {
			completed = current
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if completed.ID == "" {
		t.Fatalf("automation did not complete; batches=%d", calls.Load())
	}
	stats := completed.ModelStats["cheap"]
	price := completed.ModelPrices["cheap"]
	if calls.Load() != 2 || len(completed.RunIDs) != 2 ||
		completed.Progress.CompletedBatches != 2 || completed.Progress.RemainingFiles != 0 ||
		completed.EffectiveAccountRef != "api" ||
		completed.Usage.TotalTokens != 100 || stats.Requests != 2 ||
		math.Abs(completed.EstimatedCostUSD-0.00055) > 0.0000001 ||
		price.InputPricePer1M != 9 || price.OutputPricePer1M != 13 {
		t.Fatalf("completed=%#v stats=%#v calls=%d", completed, stats, calls.Load())
	}
}

func TestRepositoryReviewOrdinaryResumeRetainsLegacyAccountingSnapshot(t *testing.T) {
	handler, mux, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	controller := handler.repositoryReviewControllerInstance()
	seenPrice := make(chan repoaudit.RepositoryReviewModelPrice, 1)
	controller.runBatch = func(
		_ context.Context,
		automation repoaudit.RepositoryReviewAutomation,
		runID string,
		observe workflows.AgentUsageObserver,
	) (*workflows.RunResult, error) {
		seenPrice <- automation.ModelPrices["cheap"]
		if err := observe(workflows.AgentUsage{
			Model: "cheap", Reviewer: "cheap",
			PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12,
		}); err != nil {
			return nil, err
		}
		return &workflows.RunResult{
			RunID: runID, Status: workflows.RunStatusSucceeded,
			Outputs: map[string]any{"remainingFiles": 0},
		}, nil
	}
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	automation := testRepositoryReviewAutomation()
	automation.Status = repoaudit.RepositoryReviewAutomationPaused
	automation.PauseReason = repoaudit.RepositoryReviewPauseManual
	automation.PauseDetail = "legacy campaign paused"
	automation.ModelPrices = map[string]repoaudit.RepositoryReviewModelPrice{
		"cheap": {InputPricePer1M: 7, OutputPricePer1M: 11},
	}
	automation, err = store.CreateAutomation(t.Context(), automation)
	if err != nil {
		t.Fatal(err)
	}
	resumed := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost,
		"/api/repository-reviews/automations/"+automation.ID+"/resume",
		map[string]any{"expected_version": automation.Version},
	)
	if resumed.Code != http.StatusAccepted {
		t.Fatalf("resume status=%d body=%s", resumed.Code, resumed.Body.String())
	}
	select {
	case price := <-seenPrice:
		if price.InputPricePer1M != 1 || price.OutputPricePer1M != 2 {
			t.Fatalf("ordinary resume did not refresh central pricing: %#v", price)
		}
	case <-time.After(time.Second):
		t.Fatal("resumed batch did not start")
	}
	completed := waitForRepositoryReviewAutomationStatus(
		t, store, automation.ID, repoaudit.RepositoryReviewAutomationCompleted,
	)
	if math.Abs(completed.EstimatedCostUSD-0.000014) > 0.0000001 {
		t.Fatalf("refreshed snapshot cost=%v", completed.EstimatedCostUSD)
	}
}

func TestRepositoryReviewGuardPauseRequiresExplicitResume(t *testing.T) {
	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	controller := handler.repositoryReviewControllerInstance()
	var calls atomic.Int32
	controller.runBatch = func(
		_ context.Context,
		_ repoaudit.RepositoryReviewAutomation,
		runID string,
		_ workflows.AgentUsageObserver,
	) (*workflows.RunResult, error) {
		calls.Add(1)
		return &workflows.RunResult{
			RunID: runID, Status: workflows.RunStatusSucceeded,
			Outputs: map[string]any{"remainingFiles": 0},
		}, nil
	}
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	automation := testRepositoryReviewAutomation()
	automation.Status = repoaudit.RepositoryReviewAutomationPaused
	automation.PauseReason = repoaudit.RepositoryReviewPauseGuardExpression
	automation.PauseDetail = "task admission guard was false"
	automation.BudgetPolicy.GuardExpression = "account.limits.weekly.remaining_percent >= 50"
	automation, err = store.CreateAutomation(t.Context(), automation)
	if err != nil {
		t.Fatal(err)
	}

	if startErr := controller.Start(); startErr != nil {
		t.Fatal(startErr)
	}
	controller.reconcile()
	time.Sleep(30 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatalf("guard-paused review auto-resumed %d times", calls.Load())
	}
	current, _, err := store.GetAutomation(t.Context(), automation.ID)
	if err != nil || current.Status != repoaudit.RepositoryReviewAutomationPaused {
		t.Fatalf("guard pause changed without explicit resume: %#v err=%v", current, err)
	}
}

func TestRepositoryReviewRestartReconciliationPreservesManualStopIntent(t *testing.T) {
	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	controller := handler.repositoryReviewControllerInstance()
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}
	automation := testRepositoryReviewAutomation()
	automation.Status = repoaudit.RepositoryReviewAutomationStopping
	automation.ActiveRunID = "wr_manual_stop"
	automation.RunIDs = []string{"wr_manual_stop"}
	automation.RequestedPauseReason = repoaudit.RepositoryReviewPauseManual
	automation.RequestedPauseDetail = "operator requested a safe stop"
	automation, err = store.CreateAutomation(t.Context(), automation)
	if err != nil {
		t.Fatal(err)
	}
	controller.leasedStore = store
	controller.leasedConfig = cfg
	controller.reconcile()
	updated, found, err := store.GetAutomation(t.Context(), automation.ID)
	if err != nil || !found {
		t.Fatalf("GetAutomation found=%v err=%v", found, err)
	}
	if updated.Status != repoaudit.RepositoryReviewAutomationPaused ||
		updated.PauseReason != repoaudit.RepositoryReviewPauseManual ||
		updated.PauseDetail != "operator requested a safe stop" ||
		updated.ActiveRunID != "" || len(updated.RunIDs) != 1 {
		t.Fatalf("reconciled manual stop=%#v", updated)
	}
}

func TestRepositoryReviewBudgetResetKeepsLifetimeComparisonWithoutResurrectingGuardCost(t *testing.T) {
	handler, mux, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	automation := testRepositoryReviewAutomation()
	automation.Status = repoaudit.RepositoryReviewAutomationPaused
	automation.PauseReason = repoaudit.RepositoryReviewPauseGuardExpression
	automation.PauseDetail = "guard is false"
	automation, err = store.CreateAutomation(t.Context(), automation)
	if err != nil {
		t.Fatal(err)
	}
	response := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost,
		"/api/repository-reviews/automations/"+automation.ID+"/resume",
		map[string]any{"expected_version": automation.Version, "reset_budget": true},
	)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "unknown field") {
		t.Fatalf("legacy reset status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRepositoryReviewAutomationManualPauseResumeAndRestart(t *testing.T) {
	handler, mux, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	controller := handler.repositoryReviewControllerInstance()
	controller.runBatch = func(
		_ context.Context,
		_ repoaudit.RepositoryReviewAutomation,
		runID string,
		observe workflows.AgentUsageObserver,
	) (*workflows.RunResult, error) {
		if err := observe(workflows.AgentUsage{
			Model: "cheap", PromptTokens: 8, CompletionTokens: 2, TotalTokens: 10,
		}); err != nil {
			return nil, err
		}
		return &workflows.RunResult{
			RunID: runID, Status: workflows.RunStatusSucceeded,
			Outputs: map[string]any{"remainingFiles": 0, "reviewedFiles": 1},
		}, nil
	}
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	initial := testRepositoryReviewAutomation()
	initial.Status = repoaudit.RepositoryReviewAutomationPaused
	initial.PauseReason = repoaudit.RepositoryReviewPauseManual
	initial.PauseDetail = "Paused manually."
	automation, err := store.CreateAutomation(t.Context(), initial)
	if err != nil {
		t.Fatal(err)
	}

	resumed := repositoryReviewAutomationMutation(t, mux, http.MethodPost,
		"/api/repository-reviews/automations/"+automation.ID+"/resume",
		map[string]any{"expected_version": automation.Version})
	if resumed.Code != http.StatusAccepted {
		t.Fatalf("resume status=%d body=%s", resumed.Code, resumed.Body.String())
	}
	completed := waitForRepositoryReviewAutomationStatus(
		t, store, automation.ID, repoaudit.RepositoryReviewAutomationCompleted,
	)
	if completed.Usage.TotalTokens != 10 || completed.Progress.CompletedBatches != 1 {
		t.Fatalf("resumed completion=%#v", completed)
	}

	restarted := repositoryReviewAutomationMutation(t, mux, http.MethodPost,
		"/api/repository-reviews/automations/"+automation.ID+"/restart",
		map[string]any{"expected_version": completed.Version})
	if restarted.Code != http.StatusAccepted {
		t.Fatalf("restart status=%d body=%s", restarted.Code, restarted.Body.String())
	}
	recompleted := waitForRepositoryReviewAutomationStatus(
		t, store, automation.ID, repoaudit.RepositoryReviewAutomationCompleted,
	)
	if len(recompleted.RunIDs) != 2 || recompleted.Usage.TotalTokens != 10 ||
		recompleted.Progress.CompletedBatches != 1 {
		t.Fatalf("restarted completion=%#v", recompleted)
	}
}

func TestEvaluateRepositoryReviewQuotaAcrossAccountsAndWindows(t *testing.T) {
	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	controller := handler.repositoryReviewControllerInstance()
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}
	usedWeekly, usedDaily := 80, 5
	controller.probe = func(context.Context) (codexAccountLimitsResponse, error) {
		return codexAccountLimitsResponse{Accounts: []codexAccountLimitAccount{{
			ID: "api", Entries: []codexAccountLimitEntry{
				{Name: "Codex", Status: "available", Window: "weekly", UsedPercent: &usedWeekly},
				{Name: "Codex", Status: "available", Window: "daily", UsedPercent: &usedDaily},
			},
		}}}, nil
	}
	snapshots, known, err := controller.repositoryReviewGuardAccountLimits(
		t.Context(), cfg, testRepositoryReviewAutomation(),
	)
	if err != nil || !known || len(snapshots) != 2 {
		t.Fatalf("guard snapshots=%#v known=%v err=%v", snapshots, known, err)
	}
	allowed, err := repoaudit.EvaluateRepositoryReviewGuardExpression(
		"account.limits.weekly.remaining_percent >= 25",
		repoaudit.RepositoryReviewGuardEnvironment{
			AccountLimitsKnown: true, AccountLimitSnapshots: snapshots,
		},
	)
	if err != nil || allowed {
		t.Fatalf("weekly guard allowed=%v err=%v", allowed, err)
	}
}

func TestRepositoryReviewModelOptionsExposePriceAndBlockAgenticCLI(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.ModelName = "cheap"
	cfg.Agents.Defaults.AccountRef = "api"
	cfg.ModelAliases = []config.ModelAliasConfig{
		{Name: "cheap", Model: "openai/gpt-cheap"},
		{Name: "unsafe", Model: "codex-cli/codex"},
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "api", Provider: "openai", Model: "openai/gpt-cheap", Enabled: true,
		InputPricePerMTok: 0.2, OutputPricePerMTok: 0.8,
	}}
	options := repositoryReviewModelOptions(cfg)
	if len(options) != 2 || options[0].Alias != "cheap" || !options[0].Default ||
		!options[0].PriceKnown || options[0].InputPricePer1M != 0.2 ||
		options[1].Alias != "unsafe" || options[1].Available || options[1].BlockedReason == "" {
		t.Fatalf("options=%#v", options)
	}
}

func TestRepositoryReviewModelOptionsRejectPartiallyPricedAccountRoute(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.AccountRef = "review-router"
	cfg.ModelAliases = []config.ModelAliasConfig{{
		Name: "review", Model: "openai/review",
	}}
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "priced", Provider: "openai", Model: "openai/review", Enabled: true,
			InputPricePerMTok: 1, OutputPricePerMTok: 4,
		},
		{ModelName: "unpriced", Provider: "openai", Model: "openai/review", Enabled: true},
	}
	cfg.AccountRouters = []config.AccountRouterConfig{{
		Name: "review-router", Enabled: true, Entry: "accounts",
		Blocks: []config.AccountRouterBlock{{
			ID: "accounts", Type: config.AccountRouterBlockTypeLoadBalance,
			Accounts: []string{"priced", "unpriced"},
		}},
	}}

	options := repositoryReviewModelOptions(cfg)
	if len(options) != 1 || !options[0].Available || options[0].PriceKnown {
		t.Fatalf("partially priced option=%#v", options)
	}
	automation := testRepositoryReviewAutomation()
	automation.ReviewerModels = []string{"review"}
	automation.BudgetPolicy.GuardExpression = "spend.total.usd < 10"
	if err := repositoryReviewRefreshAccountingSnapshot(cfg, &automation); err == nil {
		t.Fatal("partially priced route admitted a USD budget")
	}
}

func TestRepositoryReviewPricingIgnoresUnreachableAccountRouterBlocks(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.AccountRef = "review-router"
	cfg.ModelAliases = []config.ModelAliasConfig{{Name: "review", Model: "openai/review"}}
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "priced", Provider: "openai", Model: "openai/review", Enabled: true,
			InputPricePerMTok: 1, OutputPricePerMTok: 4,
		},
		{ModelName: "orphan-unpriced", Provider: "openai", Model: "openai/review", Enabled: true},
	}
	cfg.AccountRouters = []config.AccountRouterConfig{{
		Name: "review-router", Enabled: true, Entry: "entry",
		Blocks: []config.AccountRouterBlock{
			{ID: "entry", Type: config.AccountRouterBlockTypeAccount, Account: "priced"},
			{ID: "orphan", Type: config.AccountRouterBlockTypeAccount, Account: "orphan-unpriced"},
		},
	}}

	if refs := repositoryReviewRuntimeAccountRefs(cfg); !reflect.DeepEqual(refs, []string{"priced"}) {
		t.Fatalf("reachable account refs=%#v", refs)
	}
	options := repositoryReviewModelOptions(cfg)
	if len(options) != 1 || !options[0].PriceKnown || options[0].InputPricePer1M != 1 {
		t.Fatalf("orphan block affected pricing=%#v", options)
	}
}

func TestRepositoryReviewCentralPricingHelperBoundaries(t *testing.T) {
	t.Run("configuration and snapshot errors", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "invalid.json")
		if err := os.WriteFile(configPath, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		handler := &Handler{configPath: configPath}
		automation := testRepositoryReviewAutomation()
		if err := handler.refreshRepositoryReviewAccountingSnapshot(&automation); err == nil {
			t.Fatal("invalid central configuration produced an accounting snapshot")
		}
		if err := handler.validateRepositoryReviewProfileSelection(
			"", "cheap",
			repoaudit.RepositoryReviewBudgetPolicy{GuardExpression: "spend.total.usd < 1"},
		); err == nil {
			t.Fatal("invalid central configuration admitted a profile cost budget")
		}
		if err := repositoryReviewRefreshAccountingSnapshot(nil, nil); !errors.Is(
			err,
			repoaudit.ErrInvalidAutomation,
		) {
			t.Fatalf("nil automation pricing error=%v", err)
		}
		unknown := testRepositoryReviewAutomation()
		unknown.ReviewerModels = []string{"missing"}
		unknown.ModelPrices = map[string]repoaudit.RepositoryReviewModelPrice{
			"missing": {InputPricePer1M: 99, OutputPricePer1M: 99},
		}
		if err := repositoryReviewRefreshAccountingSnapshot(nil, &unknown); err != nil ||
			len(unknown.ModelPrices) != 0 {
			t.Fatalf("unknown central pricing snapshot=%#v error=%v", unknown.ModelPrices, err)
		}
	})

	t.Run("reachable router graph", func(t *testing.T) {
		if refs := repositoryReviewReachableAccountRouterRefs(nil); refs != nil {
			t.Fatalf("nil router refs=%#v", refs)
		}
		router := &config.AccountRouterConfig{
			Entry: " branch ",
			Blocks: []config.AccountRouterBlock{
				{ID: "", Type: config.AccountRouterBlockTypeAccount, Account: "ignored"},
				{
					ID: "branch", Type: config.AccountRouterBlockTypeBranch,
					Then: "direct", Else: "missing", Fallback: "branch",
				},
				{
					ID: "direct", Type: config.AccountRouterBlockTypeAccount,
					Account: " account-a ", Fallback: "pool",
				},
				{
					ID: "pool", Type: config.AccountRouterBlockTypeLoadBalance,
					Accounts: []string{"", "account-a", "account-b"},
				},
			},
		}
		if refs := repositoryReviewReachableAccountRouterRefs(router); !reflect.DeepEqual(
			refs,
			[]string{"account-a", "account-b"},
		) {
			t.Fatalf("reachable router refs=%#v", refs)
		}
	})

	t.Run("equivalent alias recursion", func(t *testing.T) {
		if price, found := repositoryReviewEquivalentAliasPrice(nil, "root", nil); price != nil || found {
			t.Fatalf("nil equivalent pricing=(%#v,%v)", price, found)
		}
		cfg := config.DefaultConfig()
		cfg.ModelAliases = []config.ModelAliasConfig{
			{Name: "root", Model: "openai/root"},
			{Name: "middle", Model: "openai/middle"},
			{Name: "leaf", Model: "openai/leaf"},
		}
		cfg.ModelList = []*config.ModelConfig{
			nil,
			{ModelName: "disabled", Provider: "openai", Model: "openai/disabled"},
			{
				ModelName: "account-router", Enabled: true,
				Router: &config.AccountRouterConfig{Name: "account-router"},
			},
			{
				ModelName: "model-router", Enabled: true,
				ModelRouter: &config.ModelRouterConfig{Name: "model-router"},
			},
			{
				ModelName: "subscription-middle", Provider: "openai", Model: "openai/root",
				Enabled: true, Subscription: true, SubscriptionEquivalentModel: "middle",
			},
			{
				ModelName: "subscription-leaf", Provider: "openai", Model: "openai/middle",
				Enabled: true, Subscription: true, SubscriptionEquivalentModel: "leaf",
			},
			{
				ModelName: "priced", Provider: "openai", Model: "openai/leaf", Enabled: true,
				InputPricePerMTok: 1.5, OutputPricePerMTok: 6,
			},
		}
		price, found := repositoryReviewEquivalentAliasPrice(
			cfg,
			"root",
			make(map[string]bool),
		)
		if !found || price.InputPricePerMTok != 1.5 || price.OutputPricePerMTok != 6 {
			t.Fatalf("recursive equivalent pricing=(%#v,%v)", price, found)
		}
		missingPrice, missingFound := repositoryReviewEquivalentAliasPrice(
			cfg,
			"missing",
			make(map[string]bool),
		)
		if missingPrice == nil || missingFound {
			t.Fatalf("missing equivalent alias pricing=(%#v,%v)", missingPrice, missingFound)
		}
		if price, found := repositoryReviewEquivalentAliasPrice(
			cfg,
			"root",
			map[string]bool{"root": true},
		); price != nil || found {
			t.Fatalf("recursive guard pricing=(%#v,%v)", price, found)
		}
	})
}

func TestRepositoryReviewModelOptionsInheritSubscriptionPriceAndRejectUnsafeOverride(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.AccountRef = "review-router"
	cfg.ModelAliases = []config.ModelAliasConfig{
		{Name: "subscription-review", Model: "openai/subscription", AccountOverrides: map[string]string{
			"unsafe": "codex-cli/codex",
		}},
		{Name: "metered-review", Model: "openai/metered"},
	}
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "subscription", Provider: "openai", Model: "openai/subscription", Enabled: true,
			Subscription: true, SubscriptionEquivalentModel: "metered-review",
		},
		{
			ModelName: "metered", Provider: "openai", Model: "openai/metered", Enabled: true,
			InputPricePerMTok: 1.25, OutputPricePerMTok: 5,
		},
		{ModelName: "unsafe", Provider: "codex-cli", Model: "codex-cli/codex", Enabled: true},
	}
	cfg.AccountRouters = []config.AccountRouterConfig{{
		Name: "review-router", Enabled: true, Entry: "accounts",
		Blocks: []config.AccountRouterBlock{{
			ID: "accounts", Type: config.AccountRouterBlockTypeLoadBalance,
			Accounts: []string{"subscription", "metered", "unsafe"},
		}},
	}}
	options := repositoryReviewModelOptions(cfg)
	byAlias := make(map[string]repositoryReviewModelOption, len(options))
	for _, option := range options {
		byAlias[option.Alias] = option
	}
	subscription := byAlias["subscription-review"]
	if subscription.Available || subscription.BlockedReason == "" || subscription.PriceKnown {
		t.Fatalf("subscription option=%#v", subscription)
	}
	cfg.ModelAliases[0].AccountOverrides = nil
	cfg.AccountRouters[0].Blocks[0].Accounts = []string{"subscription", "metered"}
	options = repositoryReviewModelOptions(cfg)
	byAlias = make(map[string]repositoryReviewModelOption, len(options))
	for _, option := range options {
		byAlias[option.Alias] = option
	}
	subscription = byAlias["subscription-review"]
	if !subscription.Available || !subscription.PriceKnown ||
		subscription.InputPricePer1M != 1.25 || subscription.OutputPricePer1M != 5 ||
		!subscription.Subscription || subscription.EquivalentModel != "metered-review" {
		t.Fatalf("safe subscription option=%#v", subscription)
	}
}

func TestRepositoryReviewWorkflowProjectionUsesQualifiedStepIDs(t *testing.T) {
	run := &workflows.Run{Steps: map[string]workflows.StepExecution{
		"find_bugs/plan":   {ID: "plan", Status: workflows.RunStatusSucceeded},
		"find_bugs/review": {ID: "review", Status: workflows.RunStatusRunning},
	}}
	if got := repositoryReviewWorkflowStage(run); got != "Reviewing bounded file batch" {
		t.Fatalf("stage=%q", got)
	}
	if step := repositoryReviewRunStep(run, "review"); step.ID != "review" {
		t.Fatalf("qualified step=%#v", step)
	}
}

func TestRepositoryReviewCheckpointRequiresDurableRecordOrVerifiedNoop(t *testing.T) {
	result := &workflows.RunResult{
		Status: workflows.RunStatusSucceeded, Outputs: map[string]any{"remainingFiles": 0},
	}
	recordFailure := &workflows.Run{Steps: map[string]workflows.StepExecution{
		"find_bugs/plan":   {Status: workflows.RunStatusSucceeded, Outputs: map[string]any{"pendingCount": 1}},
		"find_bugs/record": {Status: workflows.RunStatusSucceeded, Error: "disk write failed"},
		"find_bugs/result": {Status: workflows.RunStatusSucceeded},
	}}
	if repositoryReviewRunCheckpointed(recordFailure, result) {
		t.Fatal("continued record failure counted as a durable checkpoint")
	}
	recordSuccess := &workflows.Run{Steps: map[string]workflows.StepExecution{
		"find_bugs/record": {
			Status:  workflows.RunStatusSucceeded,
			Outputs: map[string]any{"run": map[string]any{"id": "wr_checkpoint"}},
		},
	}}
	if !repositoryReviewRunCheckpointed(recordSuccess, result) {
		t.Fatal("durable record was not counted as a checkpoint")
	}
	noop := &workflows.Run{Steps: map[string]workflows.StepExecution{
		"find_bugs/plan":   {Status: workflows.RunStatusSucceeded, Outputs: map[string]any{"pendingCount": 0}},
		"find_bugs/result": {Status: workflows.RunStatusSucceeded},
	}}
	if !repositoryReviewRunCheckpointed(noop, result) {
		t.Fatal("verified no-op result was not counted as a checkpoint")
	}
}

func TestRepositoryReviewAutomationControllerLeaseIsWorkspaceSingleton(t *testing.T) {
	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	first := handler.repositoryReviewControllerInstance()
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(first.Stop)
	secondHandler := NewHandler(handler.configPath)
	second := secondHandler.repositoryReviewControllerInstance()
	if err := second.Start(); !errors.Is(err, repoaudit.ErrAutomationControllerLocked) {
		t.Fatalf("second controller Start() error=%v", err)
	}
}

func TestRepositoryReviewAutomationStopCancelsBlockedQuotaAdmission(t *testing.T) {
	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	controller := handler.repositoryReviewControllerInstance()
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	automation := testRepositoryReviewAutomation()
	automation.Status = repoaudit.RepositoryReviewAutomationRunning
	automation.ActiveRunID = "wr_guard_probe"
	automation.RunIDs = []string{"wr_guard_probe"}
	automation.BudgetPolicy.GuardExpression = "account.limits.known"
	automation, err = store.CreateAutomation(t.Context(), automation)
	if err != nil {
		t.Fatal(err)
	}
	probeStarted := make(chan struct{})
	controller.probe = func(ctx context.Context) (codexAccountLimitsResponse, error) {
		close(probeStarted)
		<-ctx.Done()
		return codexAccountLimitsResponse{}, ctx.Err()
	}
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}
	controller.mu.Lock()
	controller.active[automation.ID] = &repositoryReviewActiveRun{
		runID: automation.ActiveRunID, store: store, config: cfg,
		reservations: make(map[int]repositoryReviewTaskReservation),
	}
	controller.mu.Unlock()
	admissionDone := make(chan error, 1)
	go func() {
		admissionDone <- controller.observeRepositoryReviewTask(
			automation.ID, automation.ActiveRunID, workflows.ManagedChildActivity{
				Phase: workflows.ManagedChildStarted, Index: 1,
			},
		)
	}()
	select {
	case <-probeStarted:
	case <-time.After(time.Second):
		t.Fatal("quota probe did not start")
	}
	stoppedAt := time.Now()
	controller.Stop()
	if time.Since(stoppedAt) > time.Second {
		t.Fatalf("controller Stop took %s", time.Since(stoppedAt))
	}
	select {
	case admissionErr := <-admissionDone:
		if !errors.Is(admissionErr, errRepositoryReviewSafeStop) {
			t.Fatalf("task admission error=%v", admissionErr)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked admission did not exit")
	}
	current, _, getErr := store.GetAutomation(t.Context(), automation.ID)
	if getErr != nil || current.Status != repoaudit.RepositoryReviewAutomationStopping ||
		current.RequestedPauseReason != repoaudit.RepositoryReviewPauseGuardExpression {
		t.Fatalf("post-stop automation=%#v err=%v", current, getErr)
	}
}

func newRepositoryReviewAutomationTestHandler(t *testing.T) (*Handler, *http.ServeMux, string) {
	t.Helper()
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Agents.Defaults.ModelName = "cheap"
	cfg.Agents.Defaults.AccountRef = "api"
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "api", Provider: "openai", Model: "openai/test", Enabled: true,
		InputPricePerMTok: 1, OutputPricePerMTok: 2,
	}}
	cfg.ModelAliases = []config.ModelAliasConfig{
		{Name: "cheap", Model: "gpt-cheap"},
		{Name: "quality", Model: "gpt-quality"},
	}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(configPath)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	return handler, mux, workspace
}

func testRepositoryReviewAutomation() repoaudit.RepositoryReviewAutomation {
	return repoaudit.RepositoryReviewAutomation{
		Name: "Test review", Repository: "https://github.com/acme/core.git",
		Ref: "main", Target: "all", ReviewFocus: "Find correctness bugs.",
		ReviewerModels: []string{"cheap"}, AutoContinue: true,
		MaxFilesPerRun: 4, MaxContentBytes: 65536, MaxParallelChildren: 1,
		EstimatedOutputTokens: 900,
		BudgetPolicy:          repoaudit.RepositoryReviewBudgetPolicy{},
		Status:                repoaudit.RepositoryReviewAutomationIdle,
	}
}

func repositoryReviewAutomationMutation(
	t *testing.T,
	mux *http.ServeMux,
	method string,
	path string,
	body map[string]any,
) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	setRepositoryReviewMutationHeaders(request)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func automationConfigBody(automation repoaudit.RepositoryReviewAutomation) map[string]any {
	autoContinue := automation.AutoContinue
	return map[string]any{
		"name": automation.Name, "repository": automation.Repository, "ref": automation.Ref,
		"target": automation.Target, "review_focus": automation.ReviewFocus,
		"scope_policy":    automation.ScopePolicy,
		"reviewer_models": automation.ReviewerModels, "compare_models": automation.CompareModels,
		"force":         automation.Force,
		"auto_continue": autoContinue, "max_files_per_run": automation.MaxFilesPerRun,
		"max_content_bytes":     automation.MaxContentBytes,
		"max_parallel_children": automation.MaxParallelChildren,
		"budget":                automation.BudgetPolicy,
	}
}

func waitForRepositoryReviewAutomationStatus(
	t *testing.T,
	store repoaudit.Store,
	id string,
	status repoaudit.RepositoryReviewAutomationStatus,
) repoaudit.RepositoryReviewAutomation {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		automation, found, err := store.GetAutomation(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if found && automation.Status == status {
			return automation
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("automation %s did not reach %s", id, status)
	return repoaudit.RepositoryReviewAutomation{}
}

func ptrInt(value int) *int { return &value }
