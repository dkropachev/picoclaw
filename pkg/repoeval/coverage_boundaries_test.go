package repoeval

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type evaluationCancelAfterFirstContext struct {
	context.Context
	calls int
}

func (ctx *evaluationCancelAfterFirstContext) Err() error {
	ctx.calls++
	if ctx.calls > 1 {
		return context.Canceled
	}
	return nil
}

func TestValidationBoundaryBranches(t *testing.T) {
	if Clone(Evaluation{}).Corpus != nil || cloneCorpus(nil) != nil || cloneTime(nil) != nil ||
		cloneIntMap(nil) != nil || cloneFloatMap(nil) != nil {
		t.Fatal("nil clone helpers changed nil values")
	}
	if values, err := normalizeIntMap(nil); err != nil || len(values) != 0 {
		t.Fatalf("normalizeIntMap(nil) = %#v, %v", values, err)
	}
	if values, err := normalizeFloatMap(nil); err != nil || len(values) != 0 {
		t.Fatalf("normalizeFloatMap(nil) = %#v, %v", values, err)
	}
	if cloneLanguageProgressMap(nil) != nil {
		t.Fatal("cloneLanguageProgressMap(nil) changed nil")
	}
	languageProgress := map[string]LanguageProgress{"Go": {Regions: []string{"pkg"}}}
	progressClone := cloneLanguageProgressMap(languageProgress)
	progressClone["Go"] = LanguageProgress{Regions: []string{"web"}}
	if languageProgress["Go"].Regions[0] != "pkg" {
		t.Fatal("language progress clone was shallow")
	}
	if normalized, err := normalizeLanguageProgressMap(nil); err != nil || len(normalized) != 0 {
		t.Fatalf("normalizeLanguageProgressMap(nil) = %#v, %v", normalized, err)
	}
	if _, err := normalizeLanguageProgressMap(map[string]LanguageProgress{
		"Go": {}, " Go ": {},
	}); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("language progress collision error = %v", err)
	}
	if _, err := normalizeLanguageProgressMap(map[string]LanguageProgress{
		"Go": {Regions: []string{"../bad"}},
	}); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("language progress region error = %v", err)
	}
	if normalized, err := normalizeLanguageProgressMap(map[string]LanguageProgress{
		" Go ": {Regions: []string{" web\\frontend ", "pkg"}},
	}); err != nil || len(normalized["Go"].Regions) != 2 {
		t.Fatalf("normalized language progress = %#v, %v", normalized, err)
	}
	if _, err := normalizeIntMap(map[string]int{"Go": 1, " Go ": 2}); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("normalized int collision error = %v", err)
	}
	if _, err := normalizeFloatMap(
		map[string]float64{"quality": 1, " quality ": 2},
	); !errors.Is(
		err,
		ErrInvalidEvaluation,
	) {
		t.Fatalf("normalized float collision error = %v", err)
	}
	if _, err := normalizeFocus(Focus{CodeTypes: []CodeType{CodeTypeCode, CodeTypeCode}}); err == nil {
		t.Fatal("normalizeFocus accepted duplicate code types")
	}
	if _, err := normalizeFocus(Focus{ExcludeFolders: []string{"../bad"}}); err == nil {
		t.Fatal("normalizeFocus accepted invalid exclude folder")
	}
	if values, err := normalizePaths(
		[]string{"", " pkg ", "pkg"},
		true,
	); err != nil || len(values) != 1 ||
		values[0] != "pkg" {
		t.Fatalf("normalizePaths = %#v, %v", values, err)
	}
	if values := normalizeUniqueText([]string{"", "a", " a ", "b"}); len(values) != 2 {
		t.Fatalf("normalizeUniqueText = %#v", values)
	}

	base := validStoredEvaluationForValidation()
	for name, mutate := range map[string]func(*Evaluation){
		"normalize focus": func(value *Evaluation) {
			value.Focus.CodeTypes = []CodeType{CodeTypeCode, CodeTypeCode}
		},
		"normalize language limits": func(value *Evaluation) {
			value.FilesPerLanguage = map[string]int{"Go": 1, " Go ": 2}
		},
		"normalize progress languages": func(value *Evaluation) {
			value.Progress.Languages = map[string]LanguageProgress{"Go": {}, " Go ": {}}
		},
		"normalize stats": func(value *Evaluation) {
			value.ModelStats = map[string]ModelStats{"model": {}, " model ": {}}
		},
		"normalize comparison concrete models": func(value *Evaluation) {
			value.Comparisons = validComparisons()
			value.Comparisons[0].ConcreteModels = map[string]int{"model": 1, " model ": 1}
		},
		"normalize comparison regions": func(value *Evaluation) {
			value.Comparisons = validComparisons()
			value.Comparisons[0].Regions = []string{"../bad"}
		},
		"normalize comparison scores": func(value *Evaluation) {
			value.Comparisons = validComparisons()
			value.Comparisons[0].Scores = map[string]float64{"score": 1, " score ": 2}
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := Clone(base)
			mutate(&value)
			if _, err := normalizeEvaluation(value); !errors.Is(err, ErrInvalidEvaluation) {
				t.Fatalf("normalize error = %v", err)
			}
		})
	}
	assertInvalidEvaluationMutation(t, base, "candidate text", func(value *Evaluation) {
		value.CandidateModels[0] = strings.Repeat("m", maxAliasBytes+1)
	})
	assertInvalidEvaluationMutation(t, base, "terminal timestamp mismatch", func(value *Evaluation) {
		value.Status = StatusCompleted
		value.Comparisons = validComparisons()
	})
	assertInvalidEvaluationMutation(t, base, "nonterminal finished", func(value *Evaluation) {
		value.FinishedAt = timePointer(evaluationTestNow)
	})
	assertInvalidEvaluationMutation(t, base, "timestamp zone", func(value *Evaluation) {
		local := evaluationTestNow.In(time.FixedZone("local", 3600))
		value.StartedAt = &local
	})
	assertInvalidEvaluationMutation(t, base, "timestamp before create", func(value *Evaluation) {
		value.StartedAt = timePointer(evaluationTestNow.Add(-time.Hour))
	})
	assertInvalidEvaluationMutation(t, base, "finished before start", func(value *Evaluation) {
		value.Status = StatusFailed
		value.Failure = "failed"
		value.StartedAt = timePointer(evaluationTestNow.Add(time.Hour))
		value.FinishedAt = timePointer(evaluationTestNow)
	})
	assertInvalidEvaluationMutation(t, base, "duplicate focus", func(value *Evaluation) {
		value.Focus.CodeTypes = []CodeType{CodeTypeCode, CodeTypeCode}
	})
	assertInvalidEvaluationMutation(t, base, "duplicate path", func(value *Evaluation) {
		value.Focus.IncludeFolders = []string{"pkg", "pkg"}
	})
	assertInvalidEvaluationMutation(t, base, "invalid path", func(value *Evaluation) {
		value.Focus.IncludeFolders = []string{"pkg\\service"}
	})
	assertInvalidEvaluationMutation(t, base, "missing ready corpus", func(value *Evaluation) {
		value.Status = StatusReady
	})
	assertInvalidEvaluationMutation(t, base, "progress timestamp", func(value *Evaluation) {
		value.Progress.UpdatedAt = evaluationTestNow.In(time.FixedZone("local", 1))
	})
	assertInvalidEvaluationMutation(t, base, "too many stats", func(value *Evaluation) {
		for index := 0; index < 17; index++ {
			value.ModelStats[string(rune('a'+index))] = ModelStats{}
		}
	})
	assertInvalidEvaluationMutation(t, base, "stats time", func(value *Evaluation) {
		local := evaluationTestNow.In(time.FixedZone("local", 1))
		value.ModelStats["model-a"] = ModelStats{StartedAt: &local}
	})
	assertInvalidEvaluationMutation(t, base, "stats completed no start", func(value *Evaluation) {
		value.ModelStats["model-a"] = ModelStats{CompletedAt: timePointer(evaluationTestNow)}
	})
	assertInvalidEvaluationMutation(t, base, "too many comparisons", func(value *Evaluation) {
		value.Comparisons = make([]ModelComparison, 3)
	})
	assertInvalidEvaluationMutation(t, base, "duplicate warning", func(value *Evaluation) {
		value.Warnings = []string{"same", "same"}
	})

	tooManyLanguages := make(map[string]int, maxLanguages+1)
	for index := 0; index <= maxLanguages; index++ {
		tooManyLanguages[string(rune(0x100+index))] = 1
	}
	if err := validateLanguageLimits(tooManyLanguages); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("too many languages error = %v", err)
	}
	if err := validateBoundedTexts([]string{"a", "b"}, 1, 2); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("bounded count error = %v", err)
	}
	if err := validateBoundedTexts([]string{"a", "a"}, 2, 2); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("bounded duplicate error = %v", err)
	}
	local := evaluationTestNow.In(time.FixedZone("local", 1))
	if err := validateTimePair(&local, nil); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("time zone pair error = %v", err)
	}
	if err := validateTimePair(nil, timePointer(evaluationTestNow)); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("completed-only time pair error = %v", err)
	}
	if err := validateTimePair(
		timePointer(evaluationTestNow),
		timePointer(evaluationTestNow.Add(-time.Second)),
	); !errors.Is(
		err,
		ErrInvalidEvaluation,
	) {
		t.Fatalf("reversed time pair error = %v", err)
	}
	if validGitObjectID(strings.Repeat("g", 40)) || validEvaluationID("rme_0000000000000000000000000000000g") {
		t.Fatal("hex validation accepted non-hex input")
	}
	if validLowerHex("") || validLowerHex("0g") || !validLowerHex("0123abcdef") {
		t.Fatal("lower hex validation is wrong")
	}
}

func TestRepositoryIdentityAndProgressBoundaries(t *testing.T) {
	for _, repository := range []string{
		"owner/repo", "https://github.com/owner/repo.git", "ssh://git@github.com/owner/repo.git",
		"git@github.com:owner/repo.git", "file:///tmp/repository",
	} {
		if !validRepositoryIdentity(repository) {
			t.Errorf("valid repository identity rejected: %q", repository)
		}
	}
	for _, repository := range []string{
		"bad\nrepo", "https://token@github.com/owner/repo", "https://github.com/owner/repo?token=x",
		"https://github.com/owner/repo#token", "ssh://user@github.com/owner/repo",
		"ssh://git:secret@github.com/owner/repo", "https:///owner/repo", "token@github.com:owner/repo",
	} {
		if validRepositoryIdentity(repository) {
			t.Errorf("unsafe repository identity accepted: %q", repository)
		}
	}

	stages := []ProgressStage{
		ProgressIdle, ProgressResolving, ProgressInventorying, ProgressClassifying, ProgressSelecting,
		ProgressValidating, ProgressCandidateExecution, ProgressJudging, ProgressAnalyzing,
		ProgressCompleted, ProgressCanceling, ProgressCanceled, ProgressFailed,
	}
	for _, stage := range stages {
		if !stage.Valid() {
			t.Errorf("valid progress stage rejected: %q", stage)
		}
	}
	if ProgressStage("unknown").Valid() {
		t.Fatal("unknown progress stage accepted")
	}

	valid := Progress{
		Stage: ProgressSelecting, TotalFiles: 5, SelectedFiles: 2, CompletedFiles: 1,
		TotalTasks: 4, CompletedTasks: 1, Percent: 25, UpdatedAt: evaluationTestNow,
		Languages: map[string]LanguageProgress{
			"Go": {
				AvailableFiles: 5,
				SelectedFiles:  2,
				CompletedFiles: 1,
				SelectedBytes:  100,
				Regions:        []string{"pkg"},
			},
		},
	}
	if err := validateProgress(valid); err != nil {
		t.Fatalf("valid progress error = %v", err)
	}
	tests := map[string]func(*Progress){
		"stage":    func(value *Progress) { value.Stage = "unknown" },
		"language": func(value *Progress) { value.Languages[""] = LanguageProgress{} },
		"available": func(value *Progress) {
			item := value.Languages["Go"]
			item.AvailableFiles = -1
			value.Languages["Go"] = item
		},
		"selected max": func(value *Progress) {
			item := value.Languages["Go"]
			item.AvailableFiles, item.SelectedFiles = 30, 21
			value.Languages["Go"] = item
		},
		"selected available": func(value *Progress) {
			item := value.Languages["Go"]
			item.AvailableFiles, item.SelectedFiles = 1, 2
			value.Languages["Go"] = item
		},
		"completed": func(value *Progress) {
			item := value.Languages["Go"]
			item.CompletedFiles = 3
			value.Languages["Go"] = item
		},
		"bytes": func(value *Progress) {
			item := value.Languages["Go"]
			item.SelectedBytes = -1
			value.Languages["Go"] = item
		},
		"region": func(value *Progress) {
			item := value.Languages["Go"]
			item.Regions = []string{"../bad"}
			value.Languages["Go"] = item
		},
	}
	for name, alter := range tests {
		t.Run(name, func(t *testing.T) {
			value := valid
			value.Languages = cloneLanguageProgressMap(valid.Languages)
			alter(&value)
			if err := validateProgress(value); !errors.Is(err, ErrInvalidEvaluation) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestComparisonCompletionBoundaries(t *testing.T) {
	base := validStoredEvaluationForValidation()
	base.Status = StatusCompleted
	base.Corpus = validManifest()
	base.StartedAt = timePointer(evaluationTestNow)
	base.FinishedAt = timePointer(evaluationTestNow)
	base.Comparisons = validComparisons()
	tests := map[string]func(*Evaluation){
		"completion":        func(value *Evaluation) { value.Comparisons[0].Completion = "unknown" },
		"concrete key":      func(value *Evaluation) { value.Comparisons[0].ConcreteModels[""] = 1 },
		"concrete count":    func(value *Evaluation) { value.Comparisons[0].ConcreteModels["other"] = 0 },
		"failures":          func(value *Evaluation) { value.Comparisons[0].Failures = -1 },
		"completed failure": func(value *Evaluation) { value.Comparisons[0].Failure = "bad" },
		"completed score":   func(value *Evaluation) { value.Comparisons[0].OverallScore = nil },
		"partial failure":   func(value *Evaluation) { value.Comparisons[1].Failure = "" },
		"partial score":     func(value *Evaluation) { value.Comparisons[1].OverallScore = floatPointer(1) },
		"partial rank":      func(value *Evaluation) { value.Comparisons[1].Rank = 2 },
		"partial dimensions": func(value *Evaluation) {
			value.Comparisons[1].Scores = map[string]float64{"quality": 1}
		},
		"failed score": func(value *Evaluation) {
			value.Comparisons[1].Completion = ModelCompletionFailed
			value.Comparisons[1].Rank = 0
			value.Comparisons[1].Scores = map[string]float64{}
			value.Comparisons[1].OverallScore = floatPointer(1)
		},
		"usage": func(value *Evaluation) {
			value.Comparisons[0].Usage.InputTokens = -1
		},
		"pending final": func(value *Evaluation) {
			value.Comparisons[1].Completion = ModelCompletionPending
			value.Comparisons[1].Failure = ""
			value.Comparisons[1].OverallScore = nil
			value.Comparisons[1].Rank = 0
		},
	}
	for name, alter := range tests {
		t.Run(name, func(t *testing.T) {
			value := Clone(base)
			alter(&value)
			if err := validateEvaluation(value); !errors.Is(err, ErrInvalidEvaluation) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	failed := Clone(base)
	failed.Comparisons[1].Completion = ModelCompletionFailed
	failed.Comparisons[1].Failure = "provider unavailable"
	failed.Comparisons[1].OverallScore = nil
	failed.Comparisons[1].Rank = 0
	failed.Comparisons[1].Scores = map[string]float64{}
	failed.Comparisons[0].Rank = 1
	if err := validateEvaluation(failed); err != nil {
		t.Fatalf("honest failed comparison rejected: %v", err)
	}
}

func TestCorpusBoundaryBranches(t *testing.T) {
	base := validStoredEvaluationForValidation()
	base.Status = StatusReady
	base.Corpus = validManifest()
	assertInvalidEvaluationMutation(t, base, "duplicate chunk", func(value *Evaluation) {
		value.Corpus.Files[0].Chunks = append(value.Corpus.Files[0].Chunks,
			CorpusChunk{ID: "chunk-1", StartLine: 21, EndLine: 22, ContentHash: "sha256:two"})
	})
	assertInvalidEvaluationMutation(t, base, "noncanonical region", func(value *Evaluation) {
		value.Corpus.Files[0].Region = "pkg\\sub"
	})
	assertInvalidEvaluationMutation(t, base, "noncanonical module", func(value *Evaluation) {
		value.Corpus.Files[0].Module = "pkg\\sub"
	})
	assertInvalidEvaluationMutation(t, base, "too many chunks", func(value *Evaluation) {
		chunks := make([]CorpusChunk, maxChunksPerFile+1)
		for index := range chunks {
			chunks[index] = CorpusChunk{
				ID:          string(rune(0x1000 + index)),
				StartLine:   index + 1,
				EndLine:     index + 1,
				ContentHash: "sha256:x",
			}
		}
		value.Corpus.Files[0].Chunks = chunks
	})

	manifest := validManifest()
	manifest.Files[0].Chunks[0].EndLine = 0
	if err := validateCorpusFile(manifest.Files[0]); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("reversed chunk error = %v", err)
	}
	manifest = validManifest()
	manifest.Files[0].Chunks = append(manifest.Files[0].Chunks,
		CorpusChunk{ID: "chunk-2", StartLine: 20, EndLine: 21, ContentHash: "sha256:two"})
	if err := validateCorpusFile(manifest.Files[0]); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("touching chunks error = %v", err)
	}
}

func TestCheckpointAndComparisonRemainingBoundaries(t *testing.T) {
	base := validStoredEvaluationForValidation()
	if err := validateCheckpoint(
		Checkpoint{Batches: make([]BatchCheckpoint, maxBatchCheckpoints+1)},
		base,
	); !errors.Is(
		err,
		ErrInvalidEvaluation,
	) {
		t.Fatalf("checkpoint batch bound error = %v", err)
	}
	if err := validateCheckpoint(Checkpoint{ConcreteModels: map[string]map[string]int{
		"model-a": {}, "model-b": {}, "extra": {},
	}}, base); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("checkpoint model bound error = %v", err)
	}
	if err := validateCheckpoint(Checkpoint{ConcreteModels: map[string]map[string]int{
		"model-a": {"concrete": 1},
	}}, base); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("draft checkpoint error = %v", err)
	}
	if err := validateCheckpoint(Checkpoint{ConcreteModels: map[string]map[string]int{
		"model-a": {"": 1},
	}}, func() Evaluation { value := base; value.Status = StatusRunning; return value }()); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("checkpoint concrete model error = %v", err)
	}
	if err := validateCheckpoint(Checkpoint{Batches: []BatchCheckpoint{{
		ID: "batch-a", CandidateIDs: []string{"cand_unknown"}, JudgeJSON: `{}`, MappingJSON: `{}`,
		CompletedAt: evaluationTestNow,
	}}}, func() Evaluation { value := base; value.Status = StatusRunning; return value }()); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("checkpoint unknown candidate error = %v", err)
	}
	runningWithCorpus := Clone(base)
	runningWithCorpus.Status = StatusRunning
	runningWithCorpus.Corpus = validManifest()
	candidateID := runningWithCorpus.Corpus.Files[0].CandidateID
	if err := validateCheckpoint(Checkpoint{Batches: []BatchCheckpoint{
		{
			ID:           "batch-a",
			CandidateIDs: []string{candidateID},
			JudgeJSON:    `{}`,
			MappingJSON:  `{}`,
			CompletedAt:  evaluationTestNow,
		},
		{
			ID:           "batch-b",
			CandidateIDs: []string{candidateID},
			JudgeJSON:    `{}`,
			MappingJSON:  `{}`,
			CompletedAt:  evaluationTestNow,
		},
	}}, runningWithCorpus); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("checkpoint duplicate candidate error = %v", err)
	}
	invalidProgress := base.Progress
	invalidProgress.CurrentPath = "../bad"
	if err := validateProgress(invalidProgress); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("progress current path error = %v", err)
	}

	completed := Clone(base)
	completed.Status = StatusCompleted
	completed.Corpus = validManifest()
	completed.StartedAt = timePointer(evaluationTestNow)
	completed.FinishedAt = timePointer(evaluationTestNow)
	completed.Comparisons = validComparisons()
	for name, mutate := range map[string]func(*Evaluation){
		"limitations":      func(value *Evaluation) { value.Comparisons[0].Limitations = []string{"same", "same"} },
		"languages":        func(value *Evaluation) { value.Comparisons[0].Languages = []string{"same", "same"} },
		"regions":          func(value *Evaluation) { value.Comparisons[0].Regions = []string{"../bad"} },
		"comparison count": func(value *Evaluation) { value.Comparisons = value.Comparisons[:1] },
		"missing rank": func(value *Evaluation) {
			value.Comparisons[0].Rank = 2
			value.Comparisons[1].Completion = ModelCompletionFailed
			value.Comparisons[1].Failure = "failed"
			value.Comparisons[1].Rank = 0
			value.Comparisons[1].OverallScore = nil
			value.Comparisons[1].Scores = map[string]float64{}
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := Clone(completed)
			mutate(&value)
			if err := validateEvaluation(value); !errors.Is(err, ErrInvalidEvaluation) {
				t.Fatalf("validation error = %v", err)
			}
		})
	}
}

func TestCorpusRejectsAggregateChunkOverflow(t *testing.T) {
	evaluation := validStoredEvaluationForValidation()
	manifest := validManifest()
	manifest.Files = make([]CorpusFile, 257)
	manifest.LanguageCounts = make(map[string]int)
	evaluation.FilesPerLanguage = make(map[string]int)
	for fileIndex := range manifest.Files {
		language := fmt.Sprintf("language-%03d", fileIndex/20)
		evaluation.FilesPerLanguage[language] = 20
		manifest.LanguageCounts[language]++
		chunks := make([]CorpusChunk, maxChunksPerFile)
		for chunkIndex := range chunks {
			chunks[chunkIndex] = CorpusChunk{
				ID: fmt.Sprintf("chunk-%03d", chunkIndex), StartLine: chunkIndex + 1,
				EndLine: chunkIndex + 1, ContentHash: "sha256:chunk",
			}
		}
		manifest.Files[fileIndex] = CorpusFile{
			CandidateID: fmt.Sprintf("cand_%064x", fileIndex+1),
			Path:        fmt.Sprintf("pkg/file-%03d.go", fileIndex), BlobSHA: strings.Repeat("a", 40),
			SizeBytes: 1, Language: language, CodeType: CodeTypeCode,
			Module: "pkg", Region: "pkg", Chunks: chunks,
		}
	}
	if err := validateCorpus(manifest, evaluation); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("aggregate chunk overflow error = %v", err)
	}
}

func TestStoreAdditionalErrorAndDefaultBranches(t *testing.T) {
	store := NewStore(t.TempDir())
	if list, err := store.List(context.Background()); err != nil || len(list) != 0 {
		t.Fatalf("empty List = %#v, %v", list, err)
	}
	if store.clock().IsZero() || store.idGenerator() == nil {
		t.Fatal("default clock/ID generator unavailable")
	}
	id, err := randomEvaluationID()
	if err != nil || !validEvaluationID(id) {
		t.Fatalf("random ID = %q, %v", id, err)
	}

	store = newEvaluationTestStore(t, 80)
	created, err := store.Create(context.Background(), validCreateRequest())
	if err != nil {
		t.Fatal(err)
	}
	if _, invalidIDErr := store.Update(
		context.Background(),
		"invalid",
		1,
		func(*Evaluation) error { return nil },
	); !errors.Is(
		invalidIDErr,
		ErrInvalidEvaluation,
	) {
		t.Fatalf("invalid Update ID error = %v", invalidIDErr)
	}
	if _, nilMutationErr := store.Update(
		context.Background(),
		created.ID,
		created.Version,
		nil,
	); !errors.Is(
		nilMutationErr,
		ErrInvalidEvaluation,
	) {
		t.Fatalf("nil Update mutation error = %v", nilMutationErr)
	}
	if _, missingErr := store.Update(
		context.Background(),
		testEvaluationID(999),
		1,
		func(*Evaluation) error { return nil },
	); !errors.Is(
		missingErr,
		os.ErrNotExist,
	) {
		t.Fatalf("missing Update error = %v", missingErr)
	}
	ctx, cancel := context.WithCancel(context.Background())
	_, err = store.Update(ctx, created.ID, created.Version, func(*Evaluation) error {
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-mutation cancel error = %v", err)
	}

	// Equal timestamps exercise List's deterministic ID tie break.
	second, err := store.Create(context.Background(), validCreateRequest())
	if err != nil {
		t.Fatal(err)
	}
	listed, err := store.List(context.Background())
	if err != nil || len(listed) != 2 || listed[0].UpdatedAt != listed[1].UpdatedAt || listed[0].ID > listed[1].ID {
		t.Fatalf("tie List = %#v, %v (second=%s)", listed, err, second.ID)
	}
	if _, err := store.list(context.Background(), 1); err == nil {
		t.Fatal("bounded List accepted two evaluations")
	}

	if err := store.save(created, true); !errors.Is(err, os.ErrExist) {
		t.Fatalf("exclusive save existing error = %v", err)
	}
	missing := Clone(created)
	missing.ID = testEvaluationID(998)
	if err := store.save(missing, false); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("nonexclusive save missing error = %v", err)
	}
	invalid := Clone(created)
	invalid.Status = "bad"
	if err := store.save(invalid, false); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("invalid save error = %v", err)
	}
}

func TestStoreListSurfacesCorruptStateAndSymlinkCount(t *testing.T) {
	t.Skip("legacy per-evaluation JSON fault injection replaced by SQLite schema tests")
	store := newEvaluationTestStore(t, 90)
	created, err := store.Create(context.Background(), validCreateRequest())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.path(created.ID), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(context.Background()); err == nil {
		t.Fatal("List ignored corrupt evaluation state")
	}

	other := newEvaluationTestStore(t, 91)
	if err := os.MkdirAll(other.root, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		target,
		filepath.Join(other.root, stateNamePrefix+testEvaluationID(1)+stateFileSuffix),
	); err != nil {
		t.Skip(err)
	}
	if _, err := other.stateCount(); err == nil {
		t.Fatal("stateCount accepted a symlink")
	}
}

func TestStoreRemainingLockCancellationAndPersistenceErrors(t *testing.T) {
	t.Run("operation lock failures", func(t *testing.T) {
		store := NewStore(t.TempDir())
		if err := os.Mkdir(repositoryEvaluationTestLockPath(t, store.root, "store.lock"), 0o700); err != nil {
			t.Fatal(err)
		}
		calls := []func() error{
			func() error { _, err := store.Create(context.Background(), validCreateRequest()); return err },
			func() error { _, _, err := store.Get(context.Background(), testEvaluationID(1)); return err },
			func() error { _, err := store.List(context.Background()); return err },
			func() error {
				_, err := store.Update(
					context.Background(),
					testEvaluationID(1),
					1,
					func(*Evaluation) error { return nil },
				)
				return err
			},
			func() error { return store.Delete(context.Background(), testEvaluationID(1), 1) },
		}
		for index, call := range calls {
			if err := call(); err == nil {
				t.Fatalf("operation %d ignored lock failure", index)
			}
		}
	})
	t.Run("post-lock cancellation", func(t *testing.T) {
		store := newEvaluationTestStore(t, 600)
		ctx := &evaluationCancelAfterFirstContext{Context: context.Background()}
		if _, err := store.Create(ctx, validCreateRequest()); !errors.Is(err, context.Canceled) {
			t.Fatalf("Create post-lock error = %v", err)
		}
		created, err := store.Create(context.Background(), validCreateRequest())
		if err != nil {
			t.Fatal(err)
		}
		ctx = &evaluationCancelAfterFirstContext{Context: context.Background()}
		if err := store.Delete(ctx, created.ID, created.Version); !errors.Is(err, context.Canceled) {
			t.Fatalf("Delete post-load error = %v", err)
		}
		ctx = &evaluationCancelAfterFirstContext{Context: context.Background()}
		if _, err := store.List(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("List iteration error = %v", err)
		}
	})
	t.Run("create state lstat error", func(t *testing.T) {
		store := newEvaluationTestStore(t, 610)
		if err := os.MkdirAll(store.root, 0o700); err != nil {
			t.Fatal(err)
		}
		store.newID = func() (string, error) {
			if err := os.Chmod(store.root, 0); err != nil {
				t.Fatal(err)
			}
			return testEvaluationID(611), nil
		}
		t.Cleanup(func() { _ = os.Chmod(store.root, 0o700) })
		if _, err := store.Create(context.Background(), validCreateRequest()); err == nil {
			t.Skip("filesystem user bypasses directory permissions")
		}
	})
	t.Run("update persistence error", func(t *testing.T) {
		store := newEvaluationTestStore(t, 620)
		created, err := store.Create(context.Background(), validCreateRequest())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Update(context.Background(), created.ID, created.Version, func(value *Evaluation) error {
			value.Warnings = []string{"changed"}
			if removeErr := os.RemoveAll(store.root); removeErr != nil {
				return removeErr
			}
			return os.WriteFile(store.root, nil, 0o600)
		}); err == nil {
			t.Fatal("Update ignored persistence failure")
		}
	})
}

func TestStoreRemainingFilesystemAndUtilityBranches(t *testing.T) {
	t.Skip("legacy per-evaluation JSON filesystem branches no longer exist")
	t.Run("unreadable catalog", func(t *testing.T) {
		store := NewStore(t.TempDir())
		if err := os.MkdirAll(store.root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(store.root, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(store.root, 0o700) })
		if _, err := store.List(context.Background()); err == nil {
			t.Skip("filesystem user bypasses directory permissions")
		}
		if _, err := store.stateCount(); err == nil {
			t.Skip("filesystem user bypasses directory permissions")
		}
	})
	t.Run("unreadable state", func(t *testing.T) {
		store := newEvaluationTestStore(t, 630)
		created, err := store.Create(context.Background(), validCreateRequest())
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(store.path(created.ID), 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(store.path(created.ID), 0o600) })
		if _, err := store.load(created.ID); err == nil {
			t.Skip("filesystem user bypasses file permissions")
		}
	})
	t.Run("direct helper errors", func(t *testing.T) {
		store := NewStore(t.TempDir())
		if _, err := store.load("invalid"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("invalid load error = %v", err)
		}
		if err := os.WriteFile(store.root, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.stateCount(); err == nil {
			t.Fatal("stateCount accepted file root")
		}
		if _, err := store.load(testEvaluationID(1)); err == nil {
			t.Fatal("load accepted file root")
		}
		if err := store.ensureSafeRoot(); err == nil {
			t.Fatal("ensureSafeRoot accepted file root")
		}
	})
	t.Run("unsearchable root parent", func(t *testing.T) {
		parent := t.TempDir()
		store := NewStore(filepath.Join(parent, "workspace"))
		if err := os.Chmod(parent, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
		if err := store.requireSafeRoot(true); err == nil {
			t.Skip("filesystem user bypasses directory permissions")
		}
	})
	t.Run("uncreatable root", func(t *testing.T) {
		workspace := t.TempDir()
		store := NewStore(workspace)
		if err := os.Chmod(workspace, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(workspace, 0o700) })
		if err := store.ensureSafeRoot(); err == nil {
			t.Skip("filesystem user bypasses directory permissions")
		}
	})
	t.Run("marshal time", func(t *testing.T) {
		store := newEvaluationTestStore(t, 640)
		created, err := store.Create(context.Background(), validCreateRequest())
		if err != nil {
			t.Fatal(err)
		}
		invalidTime := time.Date(10_000, time.January, 1, 0, 0, 0, 0, time.UTC)
		created.CreatedAt = invalidTime
		created.UpdatedAt = invalidTime
		created.Progress.UpdatedAt = invalidTime
		if err := store.save(created, false); err == nil {
			t.Fatal("save accepted an unencodable timestamp")
		}
	})
	t.Run("create invalid and unencodable clock", func(t *testing.T) {
		zeroStore := newEvaluationTestStore(t, 650)
		zeroStore.now = func() time.Time { return time.Time{} }
		if _, err := zeroStore.Create(context.Background(), validCreateRequest()); err == nil {
			t.Fatal("Create accepted zero clock")
		}
		invalidTimeStore := newEvaluationTestStore(t, 651)
		invalidTimeStore.now = func() time.Time {
			return time.Date(10_000, time.January, 1, 0, 0, 0, 0, time.UTC)
		}
		if _, err := invalidTimeStore.Create(context.Background(), validCreateRequest()); err == nil {
			t.Fatal("Create ignored serialization failure")
		}
	})
	t.Run("save target lstat error", func(t *testing.T) {
		store := newEvaluationTestStore(t, 660)
		created, err := store.Create(context.Background(), validCreateRequest())
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(store.path(created.ID)); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(store.root, 0o400); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(store.root, 0o700) })
		if err := store.save(created, true); err == nil {
			t.Skip("filesystem user bypasses directory search permissions")
		}
	})
	t.Run("entropy", func(t *testing.T) {
		original := repositoryEvaluationRandRead
		repositoryEvaluationRandRead = func([]byte) (int, error) {
			return 0, errors.New("entropy unavailable")
		}
		t.Cleanup(func() { repositoryEvaluationRandRead = original })
		if _, err := randomEvaluationID(); err == nil {
			t.Fatal("randomEvaluationID ignored entropy error")
		}
	})
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (Store{}).Update(
		canceled,
		testEvaluationID(1),
		1,
		func(*Evaluation) error { return nil },
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("pre-canceled Update error = %v", err)
	}
	if (Store{}).clock().IsZero() || (Store{}).idGenerator() == nil {
		t.Fatal("nil store utilities did not use safe defaults")
	}
}

func validStoredEvaluationForValidation() Evaluation {
	return Evaluation{
		SchemaVersion:           SchemaVersion,
		ID:                      testEvaluationID(500),
		Version:                 1,
		Status:                  StatusDraft,
		Repository:              "owner/repo",
		Ref:                     "main",
		CandidateModels:         []string{"model-a", "model-b"},
		SelectorModelAlias:      "selector",
		JudgeModelAlias:         "judge",
		Focus:                   Focus{CodeTypes: []CodeType{CodeTypeCode}, IncludeFolders: []string{"pkg"}},
		DefaultFilesPerLanguage: 20,
		FilesPerLanguage:        map[string]int{"Go": 3, "TypeScript": 2},
		Progress: Progress{
			Stage:     ProgressIdle,
			Languages: map[string]LanguageProgress{},
			UpdatedAt: evaluationTestNow,
		},
		ModelStats:  map[string]ModelStats{},
		Comparisons: []ModelComparison{},
		Warnings:    []string{},
		RunIDs:      []string{},
		CreatedAt:   evaluationTestNow,
		UpdatedAt:   evaluationTestNow,
	}
}

func assertInvalidEvaluationMutation(t *testing.T, base Evaluation, name string, alter func(*Evaluation)) {
	t.Helper()
	value := Clone(base)
	alter(&value)
	if err := validateEvaluation(value); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("%s error = %v, want ErrInvalidEvaluation", name, err)
	}
}
