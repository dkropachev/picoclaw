package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sipeed/picoclaw/pkg/repoeval"
	"github.com/sipeed/picoclaw/pkg/reposcope"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestRepositoryModelEvaluationProgressTracksActualWorkflowPhases(t *testing.T) {
	handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
	store, _, err := handler.repositoryModelEvaluationStore()
	if err != nil {
		t.Fatal(err)
	}
	controller := newRepositoryModelEvaluationController(handler)
	controller.store = store

	draft, err := store.Create(t.Context(), repositoryModelEvaluationCreateRequest("owner/progress-preflight"))
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := store.Update(t.Context(), draft.ID, draft.Version, func(value *repoeval.Evaluation) error {
		value.Status = repoeval.StatusPreflighting
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	token, _, cancel, err := controller.reserveActive(preflight.ID)
	if err != nil {
		t.Fatal(err)
	}
	observer := controller.preflightStepObserver(preflight.ID, token)
	preflightStages := []struct {
		step    string
		stage   repoeval.ProgressStage
		model   string
		minimum float64
	}{
		{step: "checkout", stage: repoeval.ProgressResolving, minimum: 3},
		{step: "inventory", stage: repoeval.ProgressInventorying, minimum: 15},
		{step: "catalog", stage: repoeval.ProgressClassifying, minimum: 45},
		{step: "selector", stage: repoeval.ProgressSelecting, model: "selector", minimum: 72},
		{step: "select", stage: repoeval.ProgressValidating, minimum: 94},
	}
	for _, expected := range preflightStages {
		if observerErr := observer(
			workflows.StepActivityEvent{RunID: "wr-preflight", StepID: expected.step},
		); observerErr != nil {
			t.Fatal(observerErr)
		}
		current, found, getErr := store.Get(t.Context(), preflight.ID)
		if getErr != nil || !found {
			t.Fatalf("preflight progress found=%v err=%v", found, getErr)
		}
		if current.Progress.Stage != expected.stage || current.Progress.CurrentModel != expected.model ||
			current.Progress.Percent < expected.minimum || current.Progress.Message == "" {
			t.Fatalf("preflight step %q progress = %#v", expected.step, current.Progress)
		}
	}
	cancel()
	controller.releaseActive(preflight.ID, token)

	ready := seedReadyRepositoryModelEvaluation(t, controller, store, "owner/progress-evaluation")
	running, err := store.Update(t.Context(), ready.ID, ready.Version, func(value *repoeval.Evaluation) error {
		value.Status = repoeval.StatusRunning
		value.Progress.Stage = repoeval.ProgressCandidateExecution
		value.Progress.Percent = 0
		value.Progress.TotalTasks = 32
		value.Checkpoint.ConcreteModels = make(map[string]map[string]int)
		value.ModelStats = map[string]repoeval.ModelStats{
			"model-a": {FilesSelected: len(value.Corpus.Files)},
			"model-b": {FilesSelected: len(value.Corpus.Files)},
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	token, _, cancel, err = controller.reserveActive(running.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	defer controller.releaseActive(running.ID, token)
	batch := repositoryModelEvaluationBatches(running)[0]
	batchObserver := controller.batchStepObserver(
		running.ID,
		token,
		batch,
		running.JudgeModelAlias,
	)
	if observerErr := batchObserver(workflows.StepActivityEvent{StepID: "validate"}); observerErr != nil {
		t.Fatal(observerErr)
	}
	assertRepositoryModelEvaluationProgress(t, store, running.ID, repoeval.ProgressValidating, "")
	if observerErr := batchObserver(workflows.StepActivityEvent{StepID: "candidates"}); observerErr != nil {
		t.Fatal(observerErr)
	}
	assertRepositoryModelEvaluationProgress(t, store, running.ID, repoeval.ProgressCandidateExecution, "")
	if recordErr := controller.recordUsage(running.ID, token, workflows.AgentUsage{
		Reviewer: "model-a", Model: "concrete-a", PromptTokens: 1,
	}, true); recordErr != nil {
		t.Fatal(recordErr)
	}
	assertRepositoryModelEvaluationProgress(t, store, running.ID, repoeval.ProgressCandidateExecution, "")
	if observerErr := batchObserver(workflows.StepActivityEvent{StepID: "judge"}); observerErr != nil {
		t.Fatal(observerErr)
	}
	assertRepositoryModelEvaluationProgress(t, store, running.ID, repoeval.ProgressJudging, "judge")

	judging, err := controller.updateLatest(t.Context(), running.ID, func(value *repoeval.Evaluation) error {
		value.Status = repoeval.StatusJudging
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if judging.Status != repoeval.StatusJudging {
		t.Fatalf("judging status = %q", judging.Status)
	}
	if err := controller.analysisStepObserver(running.ID, token, "judge")(
		workflows.StepActivityEvent{StepID: "analyze"},
	); err != nil {
		t.Fatal(err)
	}
	assertRepositoryModelEvaluationProgress(t, store, running.ID, repoeval.ProgressJudging, "judge")
}

func TestRepositoryModelEvaluationBatchInputUsesExactPersistedCandidates(t *testing.T) {
	handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
	store, _, err := handler.repositoryModelEvaluationStore()
	if err != nil {
		t.Fatal(err)
	}
	controller := newRepositoryModelEvaluationController(handler)
	controller.store = store
	ready := seedReadyRepositoryModelEvaluation(t, controller, store, "owner/batch-input")
	batch := repositoryModelEvaluationBatches(ready)[0]
	var captured map[string]any
	controller.runWorkflow = func(
		_ context.Context,
		_ string,
		_ string,
		_ string,
		inputs map[string]any,
		_ workflows.AgentUsageEventObserver,
	) (*workflows.RunResult, error) {
		captured = inputs
		return repositoryModelEvaluationBatchResult(), nil
	}
	if _, err := controller.runEvaluationBatch(
		t.Context(), ready, batch, "wr-batch-input", "inactive-token",
	); err != nil {
		t.Fatal(err)
	}
	if _, legacy := captured["selected_candidate_ids"]; legacy {
		t.Fatalf("batch input retained ID-only validation: %#v", captured)
	}
	var candidates []reposcope.Candidate
	if err := json.Unmarshal([]byte(captured["selected_candidates"].(string)), &candidates); err != nil {
		t.Fatal(err)
	}
	if len(candidates) != len(batch.files) {
		t.Fatalf("batch candidates = %#v", candidates)
	}
	for index, candidate := range candidates {
		file := batch.files[index]
		if candidate.ID != file.CandidateID || candidate.CommitID != ready.Corpus.CommitSHA ||
			candidate.InventoryID != ready.Corpus.InventoryHash || candidate.Path != file.Path ||
			candidate.BlobID != file.BlobSHA || candidate.Size != file.SizeBytes ||
			string(candidate.Language) != file.Language || string(candidate.CodeType) != string(file.CodeType) ||
			candidate.Module != file.Module || candidate.Region != file.Region {
			t.Fatalf("persisted candidate %d = %#v, file = %#v", index, candidate, file)
		}
	}
}

func assertRepositoryModelEvaluationProgress(
	t *testing.T,
	store repoeval.Store,
	id string,
	stage repoeval.ProgressStage,
	model string,
) {
	t.Helper()
	current, found, err := store.Get(t.Context(), id)
	if err != nil || !found {
		t.Fatalf("evaluation progress found=%v err=%v", found, err)
	}
	if current.Progress.Stage != stage || current.Progress.CurrentModel != model || current.Progress.Message == "" {
		t.Fatalf("evaluation progress = %#v, want stage=%q model=%q", current.Progress, stage, model)
	}
}

func TestRepositoryModelEvaluationRuntimeStepObserverFiltersRunIdentity(t *testing.T) {
	var observed []workflows.StepActivityEvent
	observer := repositoryModelEvaluationRuntimeStepObserver(
		"wr-target",
		func(event workflows.StepActivityEvent) error {
			observed = append(observed, event)
			return nil
		},
	)
	if err := observer(workflows.StepActivityEvent{RunID: "wr-other", StepID: "inventory"}); err != nil {
		t.Fatal(err)
	}
	if err := observer(workflows.StepActivityEvent{RunID: "wr-target", StepID: "catalog"}); err != nil {
		t.Fatal(err)
	}
	if len(observed) != 1 || observed[0].StepID != "catalog" {
		t.Fatalf("filtered step activity = %#v", observed)
	}
}

func TestRepositoryModelEvaluationProgressObserverBoundaries(t *testing.T) {
	ctx := context.Background()
	if repositoryModelEvaluationWithStepObserver(ctx, nil) != ctx {
		t.Fatal("nil step observer changed context")
	}
	if repositoryModelEvaluationStepObserver(ctx) != nil {
		t.Fatal("empty context exposed a step observer")
	}

	handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
	store, _, err := handler.repositoryModelEvaluationStore()
	if err != nil {
		t.Fatal(err)
	}
	controller := newRepositoryModelEvaluationController(handler)
	controller.store = store
	ready := seedReadyRepositoryModelEvaluation(t, controller, store, "owner/progress-boundaries")
	running, err := store.Update(t.Context(), ready.ID, ready.Version, func(value *repoeval.Evaluation) error {
		value.Status = repoeval.StatusRunning
		value.Progress.Stage = repoeval.ProgressCandidateExecution
		value.Progress.TotalTasks = 10
		value.ModelStats = map[string]repoeval.ModelStats{"model-a": {}, "model-b": {}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	token, _, cancel, err := controller.reserveActive(running.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	defer controller.releaseActive(running.ID, token)

	preflight := controller.preflightStepObserver(running.ID, token)
	if observerErr := preflight(workflows.StepActivityEvent{StepID: "unknown"}); observerErr != nil {
		t.Fatal(observerErr)
	}
	batch := repositoryModelEvaluationBatches(running)[0]
	batchObserver := controller.batchStepObserver(running.ID, token, batch, running.JudgeModelAlias)
	if observerErr := batchObserver(workflows.StepActivityEvent{StepID: "unknown"}); observerErr != nil {
		t.Fatal(observerErr)
	}
	if observerErr := controller.batchStepObserver(
		running.ID,
		token,
		repositoryModelEvaluationBatch{},
		running.JudgeModelAlias,
	)(workflows.StepActivityEvent{StepID: "judge"}); observerErr == nil {
		t.Fatal("empty batch progress observer succeeded")
	}
	if observerErr := controller.analysisStepObserver(running.ID, token, running.JudgeModelAlias)(
		workflows.StepActivityEvent{StepID: "unknown"},
	); observerErr != nil {
		t.Fatal(observerErr)
	}
	analyzing, err := controller.updateLatest(t.Context(), running.ID, func(value *repoeval.Evaluation) error {
		value.Status = repoeval.StatusJudging
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	analyzing, err = controller.updateLatest(t.Context(), analyzing.ID, func(value *repoeval.Evaluation) error {
		value.Status = repoeval.StatusAnalyzing
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.analysisStepObserver(analyzing.ID, token, analyzing.JudgeModelAlias)(
		workflows.StepActivityEvent{StepID: "analyze"},
	); err != nil {
		t.Fatal(err)
	}
	assertRepositoryModelEvaluationProgress(
		t,
		store,
		analyzing.ID,
		repoeval.ProgressAnalyzing,
		analyzing.JudgeModelAlias,
	)
}
