package repoeval

import (
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestEvaluationCheckpointPersistsAndClonesWithoutAliasing(t *testing.T) {
	store := NewStore(t.TempDir())
	created, err := store.Create(t.Context(), validCreateRequest())
	if err != nil {
		t.Fatal(err)
	}
	preflighting, err := store.Update(t.Context(), created.ID, created.Version, func(candidate *Evaluation) error {
		candidate.Status = StatusPreflighting
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := store.Update(t.Context(), created.ID, preflighting.Version, func(candidate *Evaluation) error {
		candidate.Status = StatusReady
		candidate.Corpus = validManifest()
		candidate.Progress = checkpointProgress(ProgressValidating)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	running, err := store.Update(t.Context(), created.ID, ready.Version, func(candidate *Evaluation) error {
		candidate.Status = StatusRunning
		candidate.Checkpoint = Checkpoint{
			Batches: []BatchCheckpoint{{
				ID: strings.Repeat("a", 64), CandidateIDs: []string{candidate.Corpus.Files[0].CandidateID},
				Candidates: map[string]BatchCandidateCheckpoint{
					"model-a": {
						CompletedCandidateIDs: []string{candidate.Corpus.Files[0].CandidateID},
						Attempts:              1, Successes: 1,
					},
				},
				JudgeJSON: `{"evaluations":[]}`, MappingJSON: `[]`, CompletedAt: time.Now().UTC(),
			}},
			ConcreteModels: map[string]map[string]int{"model-a": {"gpt-a": 1}},
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	clone := Clone(running)
	clone.Checkpoint.Batches[0].CandidateIDs[0] = "changed"
	completed := clone.Checkpoint.Batches[0].Candidates["model-a"]
	completed.CompletedCandidateIDs[0] = "changed"
	clone.Checkpoint.Batches[0].Candidates["model-a"] = completed
	clone.Checkpoint.ConcreteModels["model-a"]["gpt-a"] = 9
	if running.Checkpoint.Batches[0].CandidateIDs[0] == "changed" ||
		running.Checkpoint.Batches[0].Candidates["model-a"].CompletedCandidateIDs[0] == "changed" ||
		running.Checkpoint.ConcreteModels["model-a"]["gpt-a"] != 1 {
		t.Fatalf("checkpoint clone aliased original: %#v", running.Checkpoint)
	}
	loaded, found, err := store.Get(t.Context(), created.ID)
	if err != nil || !found || len(loaded.Checkpoint.Batches) != 1 {
		t.Fatalf("loaded=%#v found=%v err=%v", loaded, found, err)
	}
}

func TestEvaluationCheckpointRejectsUnknownDuplicateAndUnboundedEvidence(t *testing.T) {
	evaluation := validEvaluationForCheckpoint(t)
	tests := []Checkpoint{
		{
			Batches: []BatchCheckpoint{
				{
					ID:           "batch",
					CandidateIDs: []string{"cand_" + strings.Repeat("9", 64)},
					JudgeJSON:    `{}`,
					MappingJSON:  `[]`,
					CompletedAt:  time.Now().UTC(),
				},
			},
		},
		{Batches: []BatchCheckpoint{{
			ID: "batch", CandidateIDs: []string{evaluation.Corpus.Files[0].CandidateID},
			Candidates: map[string]BatchCandidateCheckpoint{
				"unknown": {Attempts: 1, Failures: 1},
			},
			JudgeJSON: `{}`, MappingJSON: `[]`, CompletedAt: time.Now().UTC(),
		}}},
		{Batches: []BatchCheckpoint{{
			ID: "batch", CandidateIDs: []string{evaluation.Corpus.Files[0].CandidateID},
			Candidates: map[string]BatchCandidateCheckpoint{
				"model-a": {Attempts: 1, Successes: 1, Failures: 1},
			},
			JudgeJSON: `{}`, MappingJSON: `[]`, CompletedAt: time.Now().UTC(),
		}}},
		{Batches: []BatchCheckpoint{{
			ID: "batch", CandidateIDs: []string{evaluation.Corpus.Files[0].CandidateID},
			Candidates: map[string]BatchCandidateCheckpoint{
				"model-a": {CompletedCandidateIDs: []string{evaluation.Corpus.Files[0].CandidateID}},
			},
			JudgeJSON: `{}`, MappingJSON: `[]`, CompletedAt: time.Now().UTC(),
		}}},
		{Batches: []BatchCheckpoint{
			{
				ID:           "same",
				CandidateIDs: []string{evaluation.Corpus.Files[0].CandidateID},
				JudgeJSON:    `{}`,
				MappingJSON:  `[]`,
				CompletedAt:  time.Now().UTC(),
			},
			{
				ID:           "same",
				CandidateIDs: []string{evaluation.Corpus.Files[0].CandidateID},
				JudgeJSON:    `{}`,
				MappingJSON:  `[]`,
				CompletedAt:  time.Now().UTC(),
			},
		}},
		{
			Batches: []BatchCheckpoint{
				{
					ID:           "batch",
					CandidateIDs: []string{evaluation.Corpus.Files[0].CandidateID},
					JudgeJSON:    strings.Repeat("x", maxBatchEvidenceBytes+1),
					MappingJSON:  `[]`,
					CompletedAt:  time.Now().UTC(),
				},
			},
		},
		{ConcreteModels: map[string]map[string]int{"unknown": {"gpt": 1}}},
	}
	for index, checkpoint := range tests {
		candidate := Clone(evaluation)
		candidate.Checkpoint = checkpoint
		if err := validateEvaluation(candidate); !errors.Is(err, ErrInvalidEvaluation) {
			t.Fatalf("case %d error=%v", index, err)
		}
	}
}

func TestEvaluationCheckpointRejectsAmbiguousCompletedCandidatePairs(t *testing.T) {
	evaluation := validEvaluationForCheckpoint(t)
	firstID := evaluation.Corpus.Files[0].CandidateID
	secondID := evaluation.Corpus.Files[1].CandidateID
	completedAt := time.Now().UTC()
	batch := func(id string, candidateIDs, completedIDs []string) BatchCheckpoint {
		return BatchCheckpoint{
			ID: id, CandidateIDs: candidateIDs,
			Candidates: map[string]BatchCandidateCheckpoint{
				"model-a": {
					CompletedCandidateIDs: completedIDs,
					Attempts:              1,
					Successes:             1,
				},
			},
			JudgeJSON: `{}`, MappingJSON: `[]`, CompletedAt: completedAt,
		}
	}
	tests := map[string]Checkpoint{
		"duplicate candidate in batch": {
			Batches: []BatchCheckpoint{batch("batch-a", []string{firstID, firstID}, []string{firstID})},
		},
		"completed candidate outside batch": {
			Batches: []BatchCheckpoint{batch("batch-a", []string{firstID}, []string{secondID})},
		},
		"duplicate completed candidate in outcome": {
			Batches: []BatchCheckpoint{batch(
				"batch-a",
				[]string{firstID, secondID},
				[]string{firstID, firstID},
			)},
		},
		"completed alias file pair repeated across batches": {
			Batches: []BatchCheckpoint{
				batch("batch-a", []string{firstID}, []string{firstID}),
				batch("batch-b", []string{firstID}, []string{firstID}),
			},
		},
	}
	for name, checkpoint := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateCheckpoint(checkpoint, evaluation); !errors.Is(err, ErrInvalidEvaluation) {
				t.Fatalf("validateCheckpoint() error = %v, want %v", err, ErrInvalidEvaluation)
			}
		})
	}
}

func TestEvaluationNormalizationDefaultsRefAndRejectsDuplicateRanks(t *testing.T) {
	evaluation := validStoredEvaluationForValidation()
	evaluation.Ref = " \t "
	normalized, err := normalizeEvaluation(evaluation)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Ref != "HEAD" {
		t.Fatalf("normalized empty ref = %q, want HEAD", normalized.Ref)
	}

	comparisons := validComparisons()
	comparisons[1].Completion = ModelCompletionCompleted
	comparisons[1].Failure = ""
	comparisons[1].Rank = comparisons[0].Rank
	comparisons[1].OverallScore = floatPointer(80)
	if err := validateComparisons(
		comparisons,
		[]string{"model-a", "model-b"},
		StatusCompleted,
	); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("duplicate completed rank error = %v", err)
	}
}

func TestEvaluationControllerLeaseIsExclusive(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" && runtime.GOOS != "darwin" && runtime.GOOS != "freebsd" {
		t.Skip("controller lease is a no-op on this platform")
	}
	store := NewStore(t.TempDir())
	release, err := store.LockController()
	if err != nil {
		t.Fatal(err)
	}
	if _, secondErr := store.LockController(); !errors.Is(secondErr, ErrControllerLocked) {
		t.Fatalf("second lease error=%v", secondErr)
	}
	release()
	releaseAgain, err := store.LockController()
	if err != nil {
		t.Fatalf("lease after release error=%v", err)
	}
	releaseAgain()
}

func TestStoreAllowsOnlyGuardedCanceledAndFailedResume(t *testing.T) {
	store := NewStore(t.TempDir())
	draft, err := store.Create(t.Context(), validCreateRequest())
	if err != nil {
		t.Fatal(err)
	}
	canceled, err := store.Update(t.Context(), draft.ID, draft.Version, func(candidate *Evaluation) error {
		candidate.Status = StatusCanceled
		candidate.Progress.Stage = ProgressCanceled
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, invalidResumeErr := store.Update(
		t.Context(),
		canceled.ID,
		canceled.Version,
		func(candidate *Evaluation) error {
			candidate.Status = StatusRunning
			return nil
		},
	); !errors.Is(invalidResumeErr, ErrConflict) {
		t.Fatalf("canceled state resumed without corpus: %v", invalidResumeErr)
	}
	failedDraft, err := store.Create(t.Context(), validCreateRequest())
	if err != nil {
		t.Fatal(err)
	}
	preflighting, err := store.Update(
		t.Context(),
		failedDraft.ID,
		failedDraft.Version,
		func(candidate *Evaluation) error {
			candidate.Status = StatusPreflighting
			candidate.Progress.Stage = ProgressResolving
			return nil
		},
	)
	if err != nil || preflighting.FinishedAt != nil || preflighting.Status != StatusPreflighting {
		t.Fatalf("preflight resume=%#v err=%v", preflighting, err)
	}
	ready, err := store.Update(t.Context(), preflighting.ID, preflighting.Version, func(candidate *Evaluation) error {
		candidate.Status = StatusReady
		candidate.Corpus = validManifest()
		candidate.Progress = checkpointProgress(ProgressValidating)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	running, err := store.Update(t.Context(), ready.ID, ready.Version, func(candidate *Evaluation) error {
		candidate.Status = StatusRunning
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := store.Update(t.Context(), running.ID, running.Version, func(candidate *Evaluation) error {
		candidate.Status = StatusFailed
		candidate.Failure = "provider unavailable"
		candidate.Progress.Stage = ProgressFailed
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := store.Update(t.Context(), failed.ID, failed.Version, func(candidate *Evaluation) error {
		candidate.Status = StatusRunning
		candidate.Failure = ""
		candidate.Progress.Stage = ProgressCandidateExecution
		return nil
	})
	if err != nil || resumed.FinishedAt != nil || resumed.Failure != "" {
		t.Fatalf("execution resume=%#v err=%v", resumed, err)
	}
	if StatusCanceled.CanTransitionTo(StatusPreflighting) || !StatusFailed.CanTransitionTo(StatusRunning) ||
		StatusCompleted.CanTransitionTo(StatusRunning) {
		t.Fatal("terminal resume transition matrix is wrong")
	}
}

func validEvaluationForCheckpoint(t *testing.T) Evaluation {
	t.Helper()
	store := NewStore(t.TempDir())
	created, err := store.Create(t.Context(), validCreateRequest())
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := store.Update(t.Context(), created.ID, created.Version, func(candidate *Evaluation) error {
		candidate.Status = StatusPreflighting
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := store.Update(t.Context(), created.ID, preflight.Version, func(candidate *Evaluation) error {
		candidate.Status = StatusReady
		candidate.Corpus = validManifest()
		candidate.Progress = checkpointProgress(ProgressValidating)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	running, err := store.Update(t.Context(), created.ID, ready.Version, func(candidate *Evaluation) error {
		candidate.Status = StatusRunning
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return running
}

func checkpointProgress(stage ProgressStage) Progress {
	return Progress{
		Stage: stage, TotalFiles: 2, SelectedFiles: 2,
		Languages: map[string]LanguageProgress{
			"Go": {
				AvailableFiles: 1, SelectedFiles: 1, SelectedBytes: 120,
				Regions: []string{"pkg"},
			},
			"TypeScript": {
				AvailableFiles: 1, SelectedFiles: 1, SelectedBytes: 90,
				Regions: []string{"web/frontend"},
			},
		},
		UpdatedAt: evaluationTestNow,
	}
}
