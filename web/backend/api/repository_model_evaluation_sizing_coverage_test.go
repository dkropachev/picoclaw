package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/repoeval"
	"github.com/sipeed/picoclaw/pkg/reposcope"
)

func TestRepositoryModelEvaluationRepresentativeCorpusCoverage(t *testing.T) {
	selected := []reposcope.Candidate{
		{ID: "go-1", Language: reposcope.Language("Go")},
		{ID: "go-2", Language: reposcope.Language("Go")},
		{ID: "go-3", Language: reposcope.Language("Go")},
		{ID: "ts-1", Language: reposcope.Language("TypeScript")},
		{ID: "ts-2", Language: reposcope.Language("TypeScript")},
		{ID: "rust-1", Language: reposcope.Language("Rust")},
	}
	capped, changed := repositoryModelEvaluationCapRepresentativeCorpus(selected, 5)
	if !changed || len(capped) != 5 {
		t.Fatalf("capped corpus=%#v changed=%v", capped, changed)
	}
	ids := make([]string, len(capped))
	for index := range capped {
		ids[index] = capped[index].ID
	}
	if !reflect.DeepEqual(ids, []string{"go-1", "rust-1", "ts-1", "go-2", "ts-2"}) {
		t.Fatalf("round-robin IDs=%v", ids)
	}
}

func TestRepositoryModelEvaluationSizingHelperErrorCoverage(t *testing.T) {
	evaluation := repoeval.Evaluation{
		ID:              "rme_" + strings.Repeat("a", 32),
		CandidateModels: []string{"model-a"},
		Corpus: &repoeval.CorpusManifest{InventoryHash: "inventory", Files: []repoeval.CorpusFile{
			{CandidateID: "one", Path: "one.go", SizeBytes: 4},
			{CandidateID: "two", Path: "two.go", SizeBytes: 4},
		}},
		WorkSizingPlan: []repoeval.WorkSizingPoint{{
			ID: "small", Axis: repoeval.WorkSizingAxisFilesPerBatch,
			FilesPerBatch: 10, ContentBytesPerBatch: 5,
		}},
	}
	batches := repositoryModelEvaluationBatches(evaluation)
	if len(batches) != 2 || len(batches[0].files) != 1 {
		t.Fatalf("content-bounded batches=%#v", batches)
	}

	zeroLimit := evaluation
	zeroLimit.WorkSizingPlan = []repoeval.WorkSizingPoint{{
		ID: "invalid", Axis: repoeval.WorkSizingAxisFilesPerBatch,
		FilesPerBatch: 0, ContentBytesPerBatch: 5,
	}}
	if got := repositoryModelEvaluationBatches(zeroLimit); len(got) != 2 || len(got[0].files) != 1 {
		t.Fatalf("defensive zero-limit batches=%#v", got)
	}

	if repositoryModelEvaluationCandidateIDsOverlap([]string{"one"}, []string{"two"}) {
		t.Fatal("disjoint candidate IDs overlapped")
	}
	baseBatch := repositoryModelEvaluationBatch{ids: []string{"one", "two"}, models: []string{"model-a"}}
	if repositoryModelEvaluationCheckpointCompletesBatch(
		repoeval.BatchCheckpoint{CandidateIDs: []string{"one"}}, baseBatch,
	) {
		t.Fatal("short checkpoint completed a batch")
	}
	if !repositoryModelEvaluationCheckpointCompletesBatch(
		repoeval.BatchCheckpoint{CandidateIDs: []string{"one", "two"}}, baseBatch,
	) {
		t.Fatal("legacy complete checkpoint was rejected")
	}
	if repositoryModelEvaluationCheckpointCompletesBatch(repoeval.BatchCheckpoint{
		CandidateIDs: []string{"one", "two"},
		Candidates: map[string]repoeval.BatchCandidateCheckpoint{
			"model-a": {CompletedCandidateIDs: []string{"one", "missing"}},
		},
	}, baseBatch) {
		t.Fatal("checkpoint missing a candidate completed a batch")
	}

	evaluation.Checkpoint.Batches = []repoeval.BatchCheckpoint{
		{
			ID: "partial", WorkSizingPointID: "small", CandidateIDs: []string{"one"},
			Candidates: map[string]repoeval.BatchCandidateCheckpoint{
				"model-a": {Failures: 1},
			},
		},
		{ID: "unrelated", WorkSizingPointID: "other", CandidateIDs: []string{"two"}},
	}
	cleaned := repositoryModelEvaluationDiscardPartialJudgedBatches(evaluation)
	if len(cleaned.Checkpoint.Batches) != 1 || cleaned.Checkpoint.Batches[0].ID != "unrelated" {
		t.Fatalf("cleaned checkpoints=%#v", cleaned.Checkpoint.Batches)
	}

	if got := repositoryModelEvaluationConfiguredPointID(repoeval.Evaluation{WorkSizingPlan: []repoeval.WorkSizingPoint{
		{ID: "first", Axis: repoeval.WorkSizingAxisFilesPerBatch},
		{ID: "last", Axis: repoeval.WorkSizingAxisContentBytesPerBatch},
	}}); got != "last" {
		t.Fatalf("configured fallback point=%q", got)
	}
	if got := repositoryModelEvaluationReviewFocus(repoeval.Evaluation{
		Profile: &repoeval.ProfileSnapshot{ReviewFocus: "Profile focus"},
		Focus:   repoeval.Focus{FreeText: "Extra focus"},
	}); got != "Profile focus\n\nAdditional scope guidance: Extra focus" {
		t.Fatalf("combined review focus=%q", got)
	}
}

func TestRepositoryModelEvaluationBatchOutcomeObservationCoverage(t *testing.T) {
	batch := repositoryModelEvaluationBatch{
		files:  []repoeval.CorpusFile{{CandidateID: "one", Path: "one.go"}},
		models: []string{"model-a"},
	}
	outcomes := repositoryModelEvaluationBatchOutcomes(batch, []map[string]any{
		{
			"model": map[string]any{"requested": "model-a"}, "valid": true,
			"scope": []map[string]any{{"path": "one.go", "contentPromptBytes": 9}},
		},
		{
			"model": map[string]any{"requested": "model-a"}, "valid": true,
			"scope": []map[string]any{{"path": "one.go", "contentPromptBytes": 12}},
		},
	})
	if got := outcomes["model-a"]; got.ObservedContentBytesMax != 12 || got.Successes != 2 {
		t.Fatalf("batch outcomes=%#v", got)
	}
}

func TestRepositoryModelEvaluationWorkSizingSparseCoverage(t *testing.T) {
	point := repoeval.WorkSizingPoint{
		ID: "configured", Axis: repoeval.WorkSizingAxisConfigured,
		FilesPerBatch: 1, ContentBytesPerBatch: 1024,
	}
	evaluation := repoeval.Evaluation{
		CandidateModels: []string{"model-a"}, WorkSizingPlan: []repoeval.WorkSizingPoint{point},
		Corpus: &repoeval.CorpusManifest{
			Files: []repoeval.CorpusFile{{CandidateID: "one", SizeBytes: 10}},
		},
		WorkSizingUsage:          map[string]map[string]repoeval.Usage{point.ID: {"model-a": {Requests: 1}}},
		WorkSizingConcreteModels: map[string]map[string]map[string]int{point.ID: {"model-a": {"gpt": 1}}},
		Checkpoint: repoeval.Checkpoint{Batches: []repoeval.BatchCheckpoint{
			{WorkSizingPointID: "other", Candidates: map[string]repoeval.BatchCandidateCheckpoint{"model-a": {}}},
			{WorkSizingPointID: point.ID, Candidates: map[string]repoeval.BatchCandidateCheckpoint{}},
			{
				WorkSizingPointID: point.ID,
				Candidates: map[string]repoeval.BatchCandidateCheckpoint{"model-a": {
					Attempts: 1, Successes: 1, CompletedCandidateIDs: []string{"missing"},
				}},
				MappingJSON: `[]`, JudgeJSON: `{"evaluations":[]}`,
			},
		}},
	}
	result := repositoryModelEvaluationWorkSizingResults(evaluation)[0]
	if result.Completion != repoeval.ModelCompletionFailed || result.Attempts != 1 {
		t.Fatalf("sparse sizing result=%#v", result)
	}

	evaluation.Checkpoint.Batches[2].Candidates["model-a"] = repoeval.BatchCandidateCheckpoint{
		Attempts: 1, Successes: 1, CompletedCandidateIDs: []string{"one"},
	}
	result = repositoryModelEvaluationWorkSizingResults(evaluation)[0]
	if result.Completion != repoeval.ModelCompletionPartial || result.FilesAnalyzed != 1 || result.BatchSamples != 0 {
		t.Fatalf("partial sizing result=%#v", result)
	}

	if _, _, _, ok := repositoryModelEvaluationBatchScores(
		repoeval.BatchCheckpoint{MappingJSON: `{`, JudgeJSON: `{}`}, "model-a",
	); ok {
		t.Fatal("invalid score JSON was accepted")
	}
	if _, _, _, ok := repositoryModelEvaluationBatchScores(
		repoeval.BatchCheckpoint{MappingJSON: `[]`, JudgeJSON: `{"evaluations":[]}`}, "model-a",
	); ok {
		t.Fatal("missing score row was accepted")
	}
	if stats := repositoryModelEvaluationScoreStats(nil); stats.Samples != 0 {
		t.Fatalf("empty score stats=%#v", stats)
	}
}

func TestRepositoryModelEvaluationMaterializationStoreErrors(t *testing.T) {
	handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
	cfg := mustLoadRepositoryModelEvaluationConfig(t, handler.configPath)
	payload := repositoryModelEvaluationCreateAPIRequest{
		Repository: "owner/repo", ProfileID: "rrpf_kttutlpoaklekkcrod5fqpz3qw",
		CandidateModels: []string{"model-a", "model-b"},
	}
	badConfig := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(badConfig, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewHandler(badConfig).materializeRepositoryModelEvaluationCreateRequest(
		t.Context(), cfg, payload,
	); err == nil {
		t.Fatal("materialization loaded an invalid review-store config")
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := handler.materializeRepositoryModelEvaluationCreateRequest(canceled, cfg, payload); !errors.Is(
		err, context.Canceled,
	) {
		t.Fatalf("canceled profile load error=%v", err)
	}
}

func TestRepositoryModelEvaluationRunHandlerControllerErrors(t *testing.T) {
	t.Run("ensure controller", func(t *testing.T) {
		handler, mux, _ := newRepositoryModelEvaluationTestHandler(t)
		t.Cleanup(handler.Shutdown)
		stopped := newRepositoryModelEvaluationController(handler)
		stopped.Stop()
		handler.repositoryModelEvaluationController = stopped
		response := repositoryModelEvaluationMutation(
			t, mux, http.MethodPost, "/api/model-evaluations/run",
			repositoryModelEvaluationCreateBody("owner/ensure-error"),
		)
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("ensure error status=%d body=%s", response.Code, response.Body.String())
		}
	})

	t.Run("run create", func(t *testing.T) {
		handler, mux, _ := newRepositoryModelEvaluationTestHandler(t)
		t.Cleanup(handler.Shutdown)
		controller := newRepositoryModelEvaluationController(handler)
		if err := controller.Start(); err != nil {
			t.Fatal(err)
		}
		base := controller.store.(repoeval.Store)
		controller.store = &repositoryModelEvaluationFaultStore{
			base: base, createErr: errors.New("injected create failure"),
		}
		handler.repositoryModelEvaluationController = controller
		response := repositoryModelEvaluationMutation(
			t, mux, http.MethodPost, "/api/model-evaluations/run",
			repositoryModelEvaluationCreateBody("owner/run-error"),
		)
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("run error status=%d body=%s", response.Code, response.Body.String())
		}
	})
}

func TestRepositoryModelEvaluationPatchProfileErrors(t *testing.T) {
	handler, mux, _ := newRepositoryModelEvaluationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	created := createRepositoryModelEvaluation(t, mux, "owner/patch-errors")
	profileID := "rrpf_kttutlpoaklekkcrod5fqpz3qw"
	custom := repositoryModelEvaluationMutation(t, mux, http.MethodPatch,
		"/api/model-evaluations/"+created.ID, map[string]any{
			"expected_version": created.Version, "profile_id": profileID,
			"selector_model_alias": "model-a",
		})
	if custom.Code != http.StatusBadRequest {
		t.Fatalf("custom patch status=%d body=%s", custom.Code, custom.Body.String())
	}
	missing := repositoryModelEvaluationMutation(t, mux, http.MethodPatch,
		"/api/model-evaluations/"+created.ID, map[string]any{
			"expected_version": created.Version, "profile_id": "rrpf_missing",
		})
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing profile patch status=%d body=%s", missing.Code, missing.Body.String())
	}
}

func TestRepositoryModelEvaluationOptionsProfileFilters(t *testing.T) {
	handler, mux, workspace := newRepositoryModelEvaluationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	profiles := repoaudit.NewStore(workspace)
	create := func(profile repoaudit.RepositoryReviewProfile) {
		t.Helper()
		if _, err := profiles.CreateProfile(t.Context(), profile); err != nil {
			t.Fatal(err)
		}
	}
	base := repoaudit.RepositoryReviewProfile{
		Name: "API test profile", ReviewFocus: "Find bugs.", ReviewerModel: "model-a",
		MaxFilesPerRun: 8, MaxContentBytes: 1024, MaxParallelChildren: 1,
		ScopePolicy: repoaudit.RepositoryReviewScopePolicy{CodeTypes: []repoaudit.RepositoryReviewCodeType{
			repoaudit.RepositoryReviewCodeTypeCode,
		}},
	}
	create(base)
	badMaximum := base
	badMaximum.Name = "bad maximum"
	badMaximum.MaxFilesPerRun = 129
	create(badMaximum)
	unavailable := base
	unavailable.Name = "unavailable"
	unavailable.ReviewerModel = "missing"
	create(unavailable)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/model-evaluations/options", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("options status=%d body=%s", response.Code, response.Body.String())
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	request := httptest.NewRequest(http.MethodGet, "/api/model-evaluations/options", nil).WithContext(canceled)
	canceledResponse := httptest.NewRecorder()
	mux.ServeHTTP(canceledResponse, request)
	if canceledResponse.Code != http.StatusInternalServerError {
		t.Fatalf("canceled options status=%d body=%s", canceledResponse.Code, canceledResponse.Body.String())
	}
}

func TestRepositoryModelEvaluationOptionsRequireComparisonModel(t *testing.T) {
	workspace := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Agents.Defaults.AccountRef = "api"
	cfg.Agents.Defaults.ModelName = "model-a"
	cfg.ModelAliases = []config.ModelAliasConfig{{Name: "model-a", Model: "openai/gpt-a"}}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "api", Provider: "openai", Model: "openai/gpt-a", Enabled: true,
	}}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := repoaudit.NewStore(workspace).CreateProfile(t.Context(), repoaudit.RepositoryReviewProfile{
		Name: "single", ReviewFocus: "Find bugs.", ReviewerModel: "model-a",
		MaxFilesPerRun: 1, MaxContentBytes: 1, MaxParallelChildren: 1,
		ScopePolicy: repoaudit.RepositoryReviewScopePolicy{CodeTypes: []repoaudit.RepositoryReviewCodeType{
			repoaudit.RepositoryReviewCodeTypeCode,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(configPath)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	t.Cleanup(handler.Shutdown)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/model-evaluations/options", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"profiles":[]`) {
		t.Fatalf("single-model options status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRepositoryModelEvaluationExecutionAliasDuplicateCoverage(t *testing.T) {
	handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
	cfg := mustLoadRepositoryModelEvaluationConfig(t, handler.configPath)
	err := validateRepositoryModelEvaluationExecutionAliases(
		cfg, []string{"model-a", "model-a"}, "model-a", "judge",
		&repoeval.ProfileSnapshot{AccountRef: "api"},
	)
	if !errors.Is(err, repoeval.ErrInvalidEvaluation) {
		t.Fatalf("duplicate frozen candidate error=%v", err)
	}
}

func TestRepositoryModelEvaluationControllerRecoveryErrorCoverage(t *testing.T) {
	handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
	evaluation := repoeval.Evaluation{
		ID: "rme_" + strings.Repeat("c", 32), Version: 1,
		CandidateModels:    []string{"model-a", "model-b"},
		SelectorModelAlias: "model-a", JudgeModelAlias: "judge",
	}

	t.Run("ready alias admission", func(t *testing.T) {
		controller := newRepositoryModelEvaluationController(handler)
		invalid := evaluation
		invalid.CandidateModels = []string{"model-a", "missing"}
		controller.store = &repositoryModelEvaluationFaultStore{getValue: &invalid}
		if _, err := controller.startReadyEvaluationActive(
			t.Context(), invalid.ID, "token",
		); !errors.Is(err, errRepositoryModelEvaluationUnavailableModel) {
			t.Fatalf("ready alias error=%v", err)
		}
	})

	t.Run("duplicate reservations", func(t *testing.T) {
		controller := newRepositoryModelEvaluationController(handler)
		controller.store = &repositoryModelEvaluationFaultStore{getValue: &evaluation}
		token, _, _, err := controller.reserveActive(evaluation.ID)
		if err != nil {
			t.Fatal(err)
		}
		controller.recoverPreflight(evaluation.ID)
		controller.recoverEvaluation(evaluation.ID)
		controller.recoverReadyEvaluation(evaluation.ID)
		controller.releaseActive(evaluation.ID, token)
	})

	t.Run("recovery updates", func(t *testing.T) {
		controller := newRepositoryModelEvaluationController(handler)
		controller.store = &repositoryModelEvaluationFaultStore{
			getValue: &evaluation, updateErr: errors.New("injected recovery update failure"),
		}
		controller.recoverEvaluation(evaluation.ID)
		controller.recoverReadyEvaluation(evaluation.ID)
		if len(controller.active) != 0 {
			t.Fatalf("failed recovery leaked reservations=%#v", controller.active)
		}
	})

	t.Run("ready execution", func(t *testing.T) {
		controller := newRepositoryModelEvaluationController(handler)
		controller.store = &repositoryModelEvaluationFaultStore{getErr: errors.New("injected ready load failure")}
		token, runCtx, _, err := controller.reserveActive(evaluation.ID)
		if err != nil {
			t.Fatal(err)
		}
		controller.wg.Add(1)
		controller.executeReadyEvaluation(runCtx, evaluation.ID, token)
		if len(controller.active) != 0 {
			t.Fatalf("failed ready execution leaked reservations=%#v", controller.active)
		}
	})
}

func TestRepositoryModelEvaluationPreflightCorpusCapWarning(t *testing.T) {
	handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
	controller := newRepositoryModelEvaluationController(handler)
	selected := make([]reposcope.Candidate, repositoryModelEvaluationMaxCorpusFiles+1)
	for index := range selected {
		selected[index] = reposcope.Candidate{
			ID: fmt.Sprintf("cand_%064x", index+1), CommitID: strings.Repeat("a", 40),
			InventoryID: "inventory", Path: fmt.Sprintf("pkg/file-%03d.go", index),
			BlobID: strings.Repeat("b", 40), Size: 1, Language: reposcope.Language("Go"),
			CodeType: reposcope.CodeType("code"), Region: "pkg", Module: "pkg",
		}
	}
	_, _, warnings, err := controller.preflightManifest(repoeval.Evaluation{
		CandidateModels: []string{"model-a", "model-b"}, DefaultFilesPerLanguage: 20,
	}, "wr_capped", map[string]any{
		"commit": "", "inventoryHash": "",
		"selection": map[string]any{"selected": selected},
		"catalog": map[string]any{"counts": map[string]any{
			"availableByLanguage": map[string]int{"Go": len(selected)},
			"eligibleFiles":       len(selected),
		}},
		"selector": map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, warning := range warnings {
		if strings.Contains(warning, "capped the representative corpus") {
			found = true
		}
	}
	if !found {
		t.Fatalf("cap warning missing from %v", warnings)
	}
}

func TestRepositoryModelEvaluationMissingAnalyzerUsesConfiguredSizing(t *testing.T) {
	point := repoeval.WorkSizingPoint{
		ID: "configured", Axis: repoeval.WorkSizingAxisConfigured,
		FilesPerBatch: 1, ContentBytesPerBatch: 1024,
	}
	evaluation := repoeval.Evaluation{
		CandidateModels: []string{"model-a"}, WorkSizingPlan: []repoeval.WorkSizingPoint{point},
		Corpus: &repoeval.CorpusManifest{Files: []repoeval.CorpusFile{{CandidateID: "one", SizeBytes: 10}}},
		WorkSizingUsage: map[string]map[string]repoeval.Usage{
			point.ID: {"model-a": {Requests: 2}},
		},
		WorkSizingConcreteModels: map[string]map[string]map[string]int{
			point.ID: {"model-a": {"openai/gpt-a": 2}},
		},
	}
	rows, _, err := repositoryModelEvaluationComparisons(evaluation, map[string]any{
		"comparisons": []any{},
	})
	if err != nil || len(rows) != 1 || rows[0].Usage.Requests != 2 ||
		rows[0].ConcreteModels["openai/gpt-a"] != 2 {
		t.Fatalf("missing analyzer rows=%#v err=%v", rows, err)
	}
}

func TestRepositoryModelEvaluationRuntimeSetupCancellationCoverage(t *testing.T) {
	handler, _, workspace := newRepositoryModelEvaluationTestHandler(t)
	controller := newRepositoryModelEvaluationController(handler)
	if err := os.WriteFile(filepath.Join(workspace, "workflow_runs"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.runWorkflowRuntime(
		t.Context(),
		"name: canceled\non: {workflow_call: {}}\njobs: {}\n",
		"workflows/canceled.yml",
		"wr_canceled",
		nil,
		nil,
	); err == nil {
		t.Fatal("invalid workflow run store initialized")
	}
}
