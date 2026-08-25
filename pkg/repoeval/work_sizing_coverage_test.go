package repoeval

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestWorkSizingNormalizationErrorCoverage(t *testing.T) {
	if cloneWorkSizingScoreMap(nil) != nil {
		t.Fatal("nil score map clone changed nil")
	}

	badProfile := validProfileSnapshotForWorkSizing(Focus{
		CodeTypes: []CodeType{CodeTypeCode, CodeTypeCode},
	})
	if _, err := normalizeProfileSnapshot(badProfile); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("profile focus normalization error = %v", err)
	}
	request := validCreateRequest()
	request.Profile = badProfile
	if _, err := normalizeCreate(request); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("create profile normalization error = %v", err)
	}

	usagePointCollision := map[string]map[string]Usage{"point": {}, " point ": {}}
	usageAliasCollision := map[string]map[string]Usage{
		"point": {"model": {}, " model ": {}},
	}
	concretePointCollision := map[string]map[string]map[string]int{"point": {}, " point ": {}}
	concreteAliasCollision := map[string]map[string]map[string]int{
		"point": {"model": {}, " model ": {}},
	}
	concreteModelCollision := map[string]map[string]map[string]int{
		"point": {"model": {"concrete": 1, " concrete ": 1}},
	}
	resultConcreteCollision := []WorkSizingModelResult{{
		ConcreteModels: map[string]int{"concrete": 1, " concrete ": 1},
	}}
	resultScoreCollision := []WorkSizingModelResult{{
		Scores: map[string]WorkSizingScoreStats{"score": {}, " score ": {}},
	}}

	for name, check := range map[string]func() error{
		"usage point": func() error {
			_, err := normalizeWorkSizingUsage(usagePointCollision)
			return err
		},
		"usage alias": func() error {
			_, err := normalizeWorkSizingUsage(usageAliasCollision)
			return err
		},
		"concrete point": func() error {
			_, err := normalizeWorkSizingConcreteModels(concretePointCollision)
			return err
		},
		"concrete alias": func() error {
			_, err := normalizeWorkSizingConcreteModels(concreteAliasCollision)
			return err
		},
		"concrete model": func() error {
			_, err := normalizeWorkSizingConcreteModels(concreteModelCollision)
			return err
		},
		"result concrete": func() error {
			_, err := normalizeWorkSizingResults(resultConcreteCollision)
			return err
		},
		"result score": func() error {
			_, err := normalizeWorkSizingResults(resultScoreCollision)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := check(); !errors.Is(err, ErrInvalidEvaluation) {
				t.Fatalf("normalization error = %v", err)
			}
		})
	}

	base := validStoredEvaluationForValidation()
	base.Profile = validProfileSnapshotForWorkSizing(base.Focus)
	base.SelectorModelAlias = base.Profile.ReviewerModel
	base.DefaultFilesPerLanguage = DefaultFilesPerLanguage
	base.FilesPerLanguage = map[string]int{}
	base.WorkSizingPlan = validWorkSizingPlan()
	for name, mutate := range map[string]func(*Evaluation){
		"profile": func(value *Evaluation) { value.Profile = badProfile },
		"plan": func(value *Evaluation) {
			value.WorkSizingPlan = []WorkSizingPoint{{ID: "same"}, {ID: " same "}}
		},
		"usage":    func(value *Evaluation) { value.WorkSizingUsage = usagePointCollision },
		"concrete": func(value *Evaluation) { value.WorkSizingConcreteModels = concretePointCollision },
		"results":  func(value *Evaluation) { value.WorkSizingResults = resultScoreCollision },
	} {
		t.Run("evaluation "+name, func(t *testing.T) {
			value := Clone(base)
			mutate(&value)
			if _, err := normalizeEvaluation(value); !errors.Is(err, ErrInvalidEvaluation) {
				t.Fatalf("normalizeEvaluation error = %v", err)
			}
		})
	}
}

func TestWorkSizingValidationErrorCoverage(t *testing.T) {
	profile := validProfileSnapshotForWorkSizing(Focus{CodeTypes: []CodeType{CodeTypeCode}})
	if err := validateWorkSizingPlan(make([]WorkSizingPoint, maxWorkSizingPoints+1), profile); !errors.Is(
		err, ErrInvalidEvaluation,
	) {
		t.Fatalf("oversized plan error = %v", err)
	}
	if err := validateWorkSizingPlan([]WorkSizingPoint{{
		ID: "point", Axis: WorkSizingAxisFilesPerBatch, FilesPerBatch: 1, ContentBytesPerBatch: 1,
	}}, nil); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("profile-less plan error = %v", err)
	}
	duplicatePlan := validWorkSizingPlan()
	duplicatePlan[1].ID = duplicatePlan[0].ID
	if err := validateWorkSizingPlan(duplicatePlan, profile); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("duplicate point error = %v", err)
	}
	if err := validateWorkSizingPlan([]WorkSizingPoint{{
		ID: "point", Axis: WorkSizingAxisFilesPerBatch,
		FilesPerBatch: 1, ContentBytesPerBatch: 1,
	}}, profile); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("missing configured point error = %v", err)
	}

	request := validCreateRequest()
	request.Focus = profile.Focus
	request.Profile = profile
	request.SelectorModelAlias = profile.ReviewerModel
	request.WorkSizingPlan = validWorkSizingPlan()
	request.DefaultFilesPerLanguage = DefaultFilesPerLanguage
	request.FilesPerLanguage = map[string]int{}
	request, err := normalizeCreate(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCreate(request); err != nil {
		t.Fatalf("valid profile create baseline = %v", err)
	}
	request.FilesPerLanguage = map[string]int{"Go": 1}
	if err := validateCreate(request); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("profile custom quota error = %v", err)
	}

	if err := validateWorkSizingData(Evaluation{WorkSizingUsage: map[string]map[string]Usage{
		"orphan": {},
	}}); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("data without plan error = %v", err)
	}

	valid := validWorkSizingDataEvaluationForCoverage()
	invalid := map[string]func(*Evaluation){
		"too many usage points": func(value *Evaluation) {
			value.WorkSizingUsage["second"] = map[string]Usage{}
		},
		"unknown usage point": func(value *Evaluation) {
			value.WorkSizingUsage = map[string]map[string]Usage{"unknown": {}}
		},
		"unknown usage alias": func(value *Evaluation) {
			value.WorkSizingUsage["configured"]["unknown"] = Usage{}
		},
		"too many concrete points": func(value *Evaluation) {
			value.WorkSizingConcreteModels["second"] = map[string]map[string]int{}
		},
		"unknown concrete point": func(value *Evaluation) {
			value.WorkSizingConcreteModels = map[string]map[string]map[string]int{"unknown": {}}
		},
		"unknown concrete alias": func(value *Evaluation) {
			value.WorkSizingConcreteModels["configured"]["unknown"] = map[string]int{}
		},
		"too many concrete models": func(value *Evaluation) {
			models := make(map[string]int, 129)
			for index := 0; index < 129; index++ {
				models[fmt.Sprintf("model-%d", index)] = 1
			}
			value.WorkSizingConcreteModels["configured"]["model-a"] = models
		},
		"invalid concrete aggregate": func(value *Evaluation) {
			value.WorkSizingConcreteModels["configured"]["model-a"] = map[string]int{"": 1}
		},
		"too many results": func(value *Evaluation) {
			value.WorkSizingResults = append(value.WorkSizingResults, value.WorkSizingResults[0])
			value.WorkSizingResults = append(value.WorkSizingResults, value.WorkSizingResults[0])
		},
		"invalid result shape": func(value *Evaluation) {
			value.WorkSizingResults[0].Completion = "unknown"
		},
		"duplicate result": func(value *Evaluation) {
			value.WorkSizingResults = append(value.WorkSizingResults, value.WorkSizingResults[0])
		},
		"result aggregate mismatch": func(value *Evaluation) {
			value.WorkSizingResults[0].Usage.Requests = 1
		},
		"invalid result concrete": func(value *Evaluation) {
			value.WorkSizingResults[0].ConcreteModels = map[string]int{"": 1}
			value.WorkSizingConcreteModels["configured"]["model-a"] = map[string]int{"": 1}
		},
		"invalid result score": func(value *Evaluation) {
			value.WorkSizingResults[0].Scores = map[string]WorkSizingScoreStats{
				"overall": {Samples: 1, Minimum: 80, WeightedMean: 70, Maximum: 90},
			}
		},
	}
	for name, mutate := range invalid {
		t.Run(name, func(t *testing.T) {
			value := Clone(valid)
			mutate(&value)
			if err := validateWorkSizingData(value); !errors.Is(err, ErrInvalidEvaluation) {
				t.Fatalf("validation error = %v", err)
			}
		})
	}

	if validObservedWorkSizing(WorkSizingModelResult{ObservedMinFilesPerBatch: -1}) {
		t.Fatal("negative observed sizing was accepted")
	}
	if validObservedWorkSizing(WorkSizingModelResult{Attempts: 0, ObservedMinFilesPerBatch: 1}) {
		t.Fatal("zero-attempt observations were accepted")
	}
	if validWorkSizingEfficiency(WorkSizingModelResult{EffectiveTokens: 1}) {
		t.Fatal("incorrect effective tokens were accepted")
	}
	if validWorkSizingEfficiency(WorkSizingModelResult{BytesAnalyzed: 1}) {
		t.Fatal("missing efficiency ratio was accepted")
	}
	ratio := math.NaN()
	if validWorkSizingEfficiency(WorkSizingModelResult{
		BytesAnalyzed: 1, EffectiveTokensPerKiB: &ratio,
	}) {
		t.Fatal("non-finite efficiency ratio was accepted")
	}

	if validBatchCandidateObservations(BatchCandidateCheckpoint{ObservedFilesTotal: -1}, 1) {
		t.Fatal("negative checkpoint observations were accepted")
	}
	if validBatchCandidateObservations(BatchCandidateCheckpoint{ObservedFilesTotal: 1}, 1) {
		t.Fatal("zero-attempt checkpoint observations were accepted")
	}
}

func TestCheckpointRejectsSizingPointWithoutPlan(t *testing.T) {
	evaluation := validEvaluationForCheckpoint(t)
	evaluation.WorkSizingPlan = nil
	evaluation.Profile = nil
	evaluation.SelectorModelAlias = "selector"
	evaluation.DefaultFilesPerLanguage = DefaultFilesPerLanguage
	evaluation.FilesPerLanguage = map[string]int{"Go": 3}
	evaluation.Checkpoint.Batches = []BatchCheckpoint{{
		ID: "batch", WorkSizingPointID: "orphan",
		CandidateIDs: []string{evaluation.Corpus.Files[0].CandidateID},
		JudgeJSON:    `{}`, MappingJSON: `[]`, CompletedAt: evaluationTestNow,
	}}
	if err := validateCheckpoint(evaluation.Checkpoint, evaluation); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("orphan checkpoint sizing point error = %v", err)
	}
}

func validWorkSizingDataEvaluationForCoverage() Evaluation {
	point := WorkSizingPoint{
		ID: "configured", Axis: WorkSizingAxisConfigured,
		FilesPerBatch: 8, ContentBytesPerBatch: 64 << 10,
	}
	return Evaluation{
		CandidateModels: []string{"model-a", "model-b"},
		WorkSizingPlan:  []WorkSizingPoint{point},
		WorkSizingUsage: map[string]map[string]Usage{
			point.ID: {"model-a": {}},
		},
		WorkSizingConcreteModels: map[string]map[string]map[string]int{
			point.ID: {"model-a": {}},
		},
		WorkSizingResults: []WorkSizingModelResult{{
			PointID: point.ID, Axis: point.Axis, ModelAlias: "model-a",
			Completion: ModelCompletionCompleted, FilesPerBatch: point.FilesPerBatch,
			ContentBytesPerBatch: point.ContentBytesPerBatch,
		}},
	}
}

func TestStoreRejectsOversizedSerializedEvaluation(t *testing.T) {
	store := newEvaluationTestStore(t, 991)
	evaluation := validStoredEvaluationForValidation()
	files := make([]CorpusFile, 256)
	languageCounts := make(map[string]int)
	for fileIndex := range files {
		language := fmt.Sprintf("Language-%02d", fileIndex/20)
		languageCounts[language]++
		chunks := make([]CorpusChunk, maxChunksPerFile)
		for chunkIndex := range chunks {
			chunks[chunkIndex] = CorpusChunk{
				ID: fmt.Sprintf(
					"chunk-%03d-%s",
					chunkIndex,
					strings.Repeat("i", maxAliasBytes-len("chunk-000-")),
				),
				StartLine:   chunkIndex*2 + 1,
				EndLine:     chunkIndex*2 + 2,
				ContentHash: strings.Repeat("h", maxHashBytes),
			}
		}
		files[fileIndex] = CorpusFile{
			CandidateID: fmt.Sprintf("cand_%064x", fileIndex+1),
			Path:        fmt.Sprintf("language-%02d/file-%03d.go", fileIndex/20, fileIndex),
			BlobSHA:     strings.Repeat("a", 40),
			Language:    language,
			CodeType:    CodeTypeCode,
			Module:      fmt.Sprintf("language-%02d", fileIndex/20),
			Region:      fmt.Sprintf("language-%02d", fileIndex/20),
			Chunks:      chunks,
		}
	}
	evaluation.Corpus = &CorpusManifest{
		CommitSHA: strings.Repeat("b", 40), InventoryHash: "inventory", PolicyHash: "policy",
		RubricHash: "rubric", SelectorRunID: "selector", Files: files,
		LanguageCounts: languageCounts, GeneratedAt: evaluationTestNow,
	}
	if err := store.save(evaluation, true); err == nil {
		t.Fatal("oversized serialized evaluation was accepted")
	}
}
