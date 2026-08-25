package repoeval

import (
	"errors"
	"math"
	"testing"
	"time"
)

func TestProfileBackedWorkSizingStateRoundTripsAndClones(t *testing.T) {
	request := validCreateRequest()
	request.Profile = validProfileSnapshotForWorkSizing(request.Focus)
	request.SelectorModelAlias = request.Profile.ReviewerModel
	request.DefaultFilesPerLanguage = DefaultFilesPerLanguage
	request.FilesPerLanguage = map[string]int{}
	request.WorkSizingPlan = validWorkSizingPlan()
	store := newEvaluationTestStore(t, 90)
	created, err := store.Create(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.Profile.Focus.CodeTypes[0] = CodeTypeBenchTest
	request.WorkSizingPlan[0].FilesPerBatch = 1
	if created.Profile.Focus.CodeTypes[0] == CodeTypeBenchTest ||
		created.WorkSizingPlan[0].FilesPerBatch != 8 {
		t.Fatal("created evaluation aliases its profile-backed request")
	}

	efficiency := 84.0
	updated, err := store.Update(t.Context(), created.ID, created.Version, func(candidate *Evaluation) error {
		candidate.WorkSizingUsage = map[string]map[string]Usage{
			"configured": {
				"model-a": {Requests: 1, InputTokens: 100, CachedInputTokens: 40, OutputTokens: 20},
			},
		}
		candidate.WorkSizingConcreteModels = map[string]map[string]map[string]int{
			"configured": {"model-a": {"openai/gpt-a": 1}},
		}
		candidate.WorkSizingResults = []WorkSizingModelResult{{
			PointID: "configured", Axis: WorkSizingAxisConfigured, ModelAlias: "model-a",
			Completion: ModelCompletionCompleted, FilesPerBatch: 8, ContentBytesPerBatch: 64 << 10,
			BatchSamples: 1, FilesAnalyzed: 2, BytesAnalyzed: 1024,
			Attempts: 1, Successes: 1,
			ObservedMinFilesPerBatch: 2, ObservedMaxFilesPerBatch: 2, ObservedMeanFilesPerBatch: 2,
			ObservedMinContentBytesPerBatch: 1024, ObservedMaxContentBytesPerBatch: 1024,
			ObservedMeanContentBytesPerBatch: 1024,
			Scores: map[string]WorkSizingScoreStats{
				"overall": {Samples: 1, WeightedMean: 90, Minimum: 90, Maximum: 90},
			},
			ConfirmedFindings: 1,
			Usage:             Usage{Requests: 1, InputTokens: 100, CachedInputTokens: 40, OutputTokens: 20},
			ConcreteModels:    map[string]int{"openai/gpt-a": 1},
			EffectiveTokens:   84, EffectiveTokensPerKiB: &efficiency,
		}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded, found, err := store.Get(t.Context(), updated.ID)
	if err != nil || !found || loaded.WorkSizingResults[0].EffectiveTokens != 84 ||
		*loaded.WorkSizingResults[0].EffectiveTokensPerKiB != 84 {
		t.Fatalf("profile-backed state = (%#v, %t, %v)", loaded, found, err)
	}
	loaded.Profile.Name = "mutated"
	loaded.WorkSizingUsage["configured"]["model-a"] = Usage{}
	loaded.WorkSizingConcreteModels["configured"]["model-a"]["openai/gpt-a"] = 99
	loaded.WorkSizingResults[0].ConcreteModels["openai/gpt-a"] = 99
	loaded.WorkSizingResults[0].Scores["overall"] = WorkSizingScoreStats{}
	again, _, err := store.Get(t.Context(), updated.ID)
	if err != nil || again.Profile.Name == "mutated" ||
		again.WorkSizingUsage["configured"]["model-a"].InputTokens != 100 ||
		again.WorkSizingConcreteModels["configured"]["model-a"]["openai/gpt-a"] != 1 ||
		again.WorkSizingResults[0].ConcreteModels["openai/gpt-a"] != 1 ||
		again.WorkSizingResults[0].Scores["overall"].Samples != 1 {
		t.Fatalf("profile-backed state leaked mutable data: %#v err=%v", again, err)
	}

	reset, err := store.Update(t.Context(), updated.ID, updated.Version, func(candidate *Evaluation) error {
		candidate.Profile.Name = "renamed profile"
		return nil
	})
	if err != nil || len(reset.WorkSizingUsage) != 0 || len(reset.WorkSizingResults) != 0 {
		t.Fatalf("profile configuration reset = (%#v, %v)", reset, err)
	}
}

func TestProfileSnapshotNilAndEmptySlicesDoNotResetLifecycle(t *testing.T) {
	request := validCreateRequest()
	request.Focus.IncludeFolders = nil
	request.Focus.ExcludeFolders = nil
	request.Profile = validProfileSnapshotForWorkSizing(request.Focus)
	request.SelectorModelAlias = request.Profile.ReviewerModel
	request.DefaultFilesPerLanguage = DefaultFilesPerLanguage
	request.FilesPerLanguage = map[string]int{}
	request.WorkSizingPlan = validWorkSizingPlan()
	store := newEvaluationTestStore(t, 91)
	created, err := store.Create(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	loaded, _, err := store.Get(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := store.Update(t.Context(), loaded.ID, loaded.Version, func(candidate *Evaluation) error {
		candidate.Status = StatusPreflighting
		return nil
	})
	if err != nil || updated.Status != StatusPreflighting {
		t.Fatalf("profile lifecycle update reset to %q: %v", updated.Status, err)
	}
}

func TestWorkSizingCheckpointCompletionIsScopedByPoint(t *testing.T) {
	evaluation := validEvaluationForCheckpoint(t)
	evaluation.Profile = validProfileSnapshotForWorkSizing(evaluation.Focus)
	evaluation.SelectorModelAlias = evaluation.Profile.ReviewerModel
	evaluation.DefaultFilesPerLanguage = DefaultFilesPerLanguage
	evaluation.FilesPerLanguage = map[string]int{}
	evaluation.WorkSizingPlan = validWorkSizingPlan()
	candidateID := evaluation.Corpus.Files[0].CandidateID
	completedAt := time.Now().UTC()
	checkpoint := func(id, pointID string) BatchCheckpoint {
		return BatchCheckpoint{
			ID: id, WorkSizingPointID: pointID, CandidateIDs: []string{candidateID},
			Candidates: map[string]BatchCandidateCheckpoint{
				"model-a": {
					CompletedCandidateIDs: []string{candidateID}, Attempts: 1, Successes: 1,
					ObservedFilesTotal: 1, ObservedFilesMin: 1, ObservedFilesMax: 1,
					ObservedContentBytesTotal: 120, ObservedContentBytesMin: 120,
					ObservedContentBytesMax: 120,
				},
			},
			JudgeJSON: `{}`, MappingJSON: `[]`, CompletedAt: completedAt,
		}
	}
	evaluation.Checkpoint.Batches = []BatchCheckpoint{
		checkpoint("configured-batch", "configured"),
		checkpoint("files-batch", "files-4"),
	}
	if err := validateEvaluation(evaluation); err != nil {
		t.Fatalf("same candidate at distinct sizing points was rejected: %v", err)
	}
	evaluation.Checkpoint.Batches[1].WorkSizingPointID = "configured"
	if err := validateEvaluation(evaluation); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("duplicate completion inside one sizing point error=%v", err)
	}
	evaluation.Checkpoint.Batches[1].WorkSizingPointID = "missing"
	if err := validateEvaluation(evaluation); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("unknown sizing point error=%v", err)
	}
}

func TestWorkSizingValidationBoundaries(t *testing.T) {
	request := validCreateRequest()
	request.Profile = validProfileSnapshotForWorkSizing(request.Focus)
	request.SelectorModelAlias = request.Profile.ReviewerModel
	request.DefaultFilesPerLanguage = DefaultFilesPerLanguage
	request.FilesPerLanguage = map[string]int{}
	request.WorkSizingPlan = validWorkSizingPlan()
	invalid := []func(*CreateRequest){
		func(value *CreateRequest) { value.Profile.MaxParallelChildren = 65 },
		func(value *CreateRequest) { value.WorkSizingPlan[1].ID = value.WorkSizingPlan[0].ID },
		func(value *CreateRequest) { value.WorkSizingPlan[1].Axis = "unknown" },
		func(value *CreateRequest) { value.WorkSizingPlan[1].FilesPerBatch = 9 },
		func(value *CreateRequest) { value.WorkSizingPlan[0].ContentBytesPerBatch-- },
	}
	for index, mutate := range invalid {
		candidate := request
		candidate.Profile = cloneProfileSnapshot(request.Profile)
		candidate.WorkSizingPlan = append([]WorkSizingPoint(nil), request.WorkSizingPlan...)
		mutate(&candidate)
		if _, err := newEvaluationTestStore(t, 100+index).Create(t.Context(), candidate); !errors.Is(
			err,
			ErrInvalidEvaluation,
		) {
			t.Fatalf("invalid work sizing case %d error=%v", index, err)
		}
	}

	evaluation := validStoredEvaluationForValidation()
	evaluation.Usage = Usage{InputTokens: 1, CachedInputTokens: 2}
	if err := validateEvaluation(evaluation); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("cached input exceeding input tokens error=%v", err)
	}
	evaluation.Usage = Usage{}
	evaluation.Profile = validProfileSnapshotForWorkSizing(evaluation.Focus)
	evaluation.SelectorModelAlias = evaluation.Profile.ReviewerModel
	evaluation.DefaultFilesPerLanguage = DefaultFilesPerLanguage
	evaluation.FilesPerLanguage = map[string]int{}
	evaluation.WorkSizingPlan = validWorkSizingPlan()
	evaluation.WorkSizingResults = []WorkSizingModelResult{{
		PointID: "configured", Axis: WorkSizingAxisConfigured, ModelAlias: "model-a",
		Completion: ModelCompletionCompleted, FilesPerBatch: 8, ContentBytesPerBatch: 64 << 10,
		Scores: map[string]WorkSizingScoreStats{
			"overall": {Samples: 1, WeightedMean: math.NaN(), Minimum: 0, Maximum: 100},
		},
	}}
	if err := validateEvaluation(evaluation); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("non-finite work sizing score error=%v", err)
	}
}

func validProfileSnapshotForWorkSizing(focus Focus) *ProfileSnapshot {
	return &ProfileSnapshot{
		ID: "rrpf_work_sizing", Version: 1, Name: "Work sizing", ReviewerModel: "model-a",
		ReviewFocus: "Find concrete bugs.", Focus: focus,
		MaxFilesPerBatch: 8, MaxContentBytesPerBatch: 64 << 10, MaxParallelChildren: 3,
	}
}

func validWorkSizingPlan() []WorkSizingPoint {
	return []WorkSizingPoint{
		{ID: "configured", Axis: WorkSizingAxisConfigured, FilesPerBatch: 8, ContentBytesPerBatch: 64 << 10},
		{ID: "files-4", Axis: WorkSizingAxisFilesPerBatch, FilesPerBatch: 4, ContentBytesPerBatch: 64 << 10},
		{ID: "bytes-32768", Axis: WorkSizingAxisContentBytesPerBatch, FilesPerBatch: 8, ContentBytesPerBatch: 32 << 10},
	}
}
