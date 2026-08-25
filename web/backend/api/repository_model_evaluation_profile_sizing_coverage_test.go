package api

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/repoeval"
	"github.com/sipeed/picoclaw/pkg/reposcope"
)

func TestRepositoryModelEvaluationProfileMaterializationErrorCoverage(t *testing.T) {
	handler, _, workspace := newRepositoryModelEvaluationTestHandler(t)
	cfg := mustLoadRepositoryModelEvaluationConfig(t, handler.configPath)
	base := repositoryModelEvaluationCreateAPIRequest{
		Repository: "https://github.com/owner/repo.git", ProfileID: "rrpf_kttutlpoaklekkcrod5fqpz3qw",
		CandidateModels: []string{"model-a", "model-b"},
	}
	for name, mutate := range map[string]func(*repositoryModelEvaluationCreateAPIRequest){
		"missing profile": func(value *repositoryModelEvaluationCreateAPIRequest) { value.ProfileID = "" },
		"custom focus":    func(value *repositoryModelEvaluationCreateAPIRequest) { value.Focus = &repoeval.Focus{} },
		"custom default quota": func(value *repositoryModelEvaluationCreateAPIRequest) {
			quota := 1
			value.DefaultFilesPerLanguage = &quota
		},
		"custom language quota": func(value *repositoryModelEvaluationCreateAPIRequest) {
			quota := map[string]int{}
			value.FilesPerLanguage = &quota
		},
		"custom selector": func(value *repositoryModelEvaluationCreateAPIRequest) {
			selector := ""
			value.SelectorModelAlias = &selector
		},
		"custom judge": func(value *repositoryModelEvaluationCreateAPIRequest) {
			judge := ""
			value.JudgeModelAlias = &judge
		},
		"missing stored profile": func(value *repositoryModelEvaluationCreateAPIRequest) {
			value.ProfileID = "rrpf_aaaaaaaaaaaaaaaaaaaaaaaaaa"
		},
		"one candidate": func(value *repositoryModelEvaluationCreateAPIRequest) {
			value.CandidateModels = []string{"model-a"}
		},
		"reviewer omitted": func(value *repositoryModelEvaluationCreateAPIRequest) {
			value.CandidateModels = []string{"model-b", "judge"}
		},
		"candidate unavailable": func(value *repositoryModelEvaluationCreateAPIRequest) {
			value.CandidateModels = []string{"model-a", "missing"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			payload := base
			payload.CandidateModels = append([]string(nil), base.CandidateModels...)
			mutate(&payload)
			if _, err := handler.materializeRepositoryModelEvaluationCreateRequest(
				t.Context(), cfg, payload,
			); err == nil {
				t.Fatal("invalid profile materialization succeeded")
			}
		})
	}

	oversized, err := repoaudit.NewStore(workspace).CreateProfile(t.Context(), repoaudit.RepositoryReviewProfile{
		Name: "oversized", ReviewFocus: "Find bugs.", ReviewerModel: "model-a", AutoContinue: true,
		MaxFilesPerRun: 129, MaxContentBytes: 1, MaxParallelChildren: 1,
		ScopePolicy: repoaudit.RepositoryReviewScopePolicy{CodeTypes: []repoaudit.RepositoryReviewCodeType{
			repoaudit.RepositoryReviewCodeTypeCode,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := base
	payload.ProfileID = oversized.ID
	if _, err := handler.materializeRepositoryModelEvaluationCreateRequest(
		t.Context(), cfg, payload,
	); !errors.Is(err, repoeval.ErrInvalidEvaluation) {
		t.Fatalf("oversized profile error = %v", err)
	}

	broken := NewHandler(t.TempDir() + "/missing/config.json")
	if _, err := broken.materializeRepositoryModelEvaluationCreateRequest(
		context.Background(), cfg, base,
	); err == nil {
		t.Fatal("broken review store succeeded")
	}
}

func TestRepositoryModelEvaluationProfileHelperCoverage(t *testing.T) {
	if got := normalizeRepositoryModelEvaluationAliases([]string{" a ", "", "a", "b"}); !slices.Equal(
		got,
		[]string{"a", "b"},
	) {
		t.Fatalf("normalized aliases = %#v", got)
	}
	profile := repoaudit.RepositoryReviewProfile{ScopePolicy: repoaudit.RepositoryReviewScopePolicy{
		CodeTypes:      []repoaudit.RepositoryReviewCodeType{repoaudit.RepositoryReviewCodeTypeCode},
		IncludeFolders: []string{"pkg"}, ExcludeFolders: []string{"vendor"}, FreeText: "focus",
	}}
	focus := repositoryModelEvaluationFocusFromReviewProfile(profile)
	profile.ScopePolicy.IncludeFolders[0] = "changed"
	if !slices.Equal(focus.CodeTypes, []repoeval.CodeType{repoeval.CodeTypeCode}) ||
		focus.IncludeFolders[0] != "pkg" || focus.ExcludeFolders[0] != "vendor" || focus.FreeText != "focus" {
		t.Fatalf("profile focus = %#v", focus)
	}

	if repositoryModelEvaluationSizingLadder(0) != nil ||
		!slices.Equal(repositoryModelEvaluationSizingLadder(1), []int64{1}) ||
		!slices.Equal(repositoryModelEvaluationSizingLadder(2), []int64{1, 2}) {
		t.Fatal("sizing ladder edge mismatch")
	}
	plan := repositoryModelEvaluationWorkSizingPlan(8, 64)
	if len(plan) != 5 || plan[len(plan)-1].Axis != repoeval.WorkSizingAxisConfigured {
		t.Fatalf("sizing plan = %#v", plan)
	}

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.AccountRef = "api"
	cfg.Agents.Defaults.ModelName = "default-judge"
	cfg.ModelAliases = []config.ModelAliasConfig{
		{Name: "model-a", Model: "openai/a"},
		{Name: "model-b", Model: "openai/b"},
		{Name: "default-judge", Model: "openai/j"},
		{Name: "other", Model: "openai/o"},
	}
	cfg.ModelList = []*config.ModelConfig{{ModelName: "api", Provider: "openai", Model: "openai/a", Enabled: true}}
	if judge := repositoryModelEvaluationAutomaticJudge(
		cfg, "api", []string{"model-a", "model-b"}, "model-a",
	); judge != "default-judge" {
		t.Fatalf("default judge = %q", judge)
	}
	if judge := repositoryModelEvaluationAutomaticJudge(
		cfg, "api", []string{"model-a", "model-b", "default-judge"}, "model-a",
	); judge != "other" {
		t.Fatalf("independent judge = %q", judge)
	}
	if judge := repositoryModelEvaluationAutomaticJudge(
		cfg, "api", []string{"model-a", "model-b", "default-judge", "other"}, "model-a",
	); judge != "model-a" {
		t.Fatalf("fallback judge = %q", judge)
	}
	if judge := repositoryModelEvaluationAutomaticJudge(
		&config.Config{}, "missing", []string{"model-a"}, "model-a",
	); judge != "" {
		t.Fatalf("unavailable judge = %q", judge)
	}
	if available := repositoryModelEvaluationAvailableAliasesForAccount(nil, "api"); len(available) != 0 {
		t.Fatalf("nil config aliases = %#v", available)
	}
}

func TestRepositoryModelEvaluationCorpusCapAndSizingHelperCoverage(t *testing.T) {
	input := make([]reposcope.Candidate, 0, 9)
	for index := 0; index < 9; index++ {
		language := reposcope.Language("go")
		if index%3 == 1 {
			language = "rust"
		} else if index%3 == 2 {
			language = "typescript"
		}
		input = append(input, reposcope.Candidate{ID: fmt.Sprintf("id-%d", index), Language: language})
	}
	if got, capped := repositoryModelEvaluationCapRepresentativeCorpus(input, 0); capped || len(got) != len(input) {
		t.Fatalf("zero cap = %d, %t", len(got), capped)
	}
	if got, capped := repositoryModelEvaluationCapRepresentativeCorpus(input[:2], 3); capped || len(got) != 2 {
		t.Fatalf("short cap = %d, %t", len(got), capped)
	}
	got, capped := repositoryModelEvaluationCapRepresentativeCorpus(input, 5)
	if !capped || len(got) != 5 || got[0].Language != "go" || got[1].Language != "rust" ||
		got[2].Language != "typescript" {
		t.Fatalf("round-robin cap = %#v, %t", got, capped)
	}

	legacy := repoeval.Evaluation{}
	if repositoryModelEvaluationAccountRef(legacy) != "" ||
		repositoryModelEvaluationReviewFocus(legacy) != "" ||
		repositoryModelEvaluationBatchFilesLimit(legacy, repositoryModelEvaluationBatch{}) != 3 ||
		repositoryModelEvaluationBatchContentLimit(legacy, repositoryModelEvaluationBatch{}) != 524288 ||
		repositoryModelEvaluationParallelChildren(legacy) != 3 ||
		repositoryModelEvaluationConfiguredPointID(legacy) != "" {
		t.Fatal("legacy sizing defaults mismatch")
	}
	profileSnapshot := &repoeval.ProfileSnapshot{
		AccountRef: "account", ReviewFocus: "review", MaxParallelChildren: 7,
	}
	profiled := repoeval.Evaluation{
		Profile: profileSnapshot, Focus: repoeval.Focus{FreeText: "scope"},
		WorkSizingPlan: []repoeval.WorkSizingPoint{{
			ID: "files", Axis: repoeval.WorkSizingAxisFilesPerBatch,
			FilesPerBatch: 4, ContentBytesPerBatch: 100,
		}},
	}
	batch := repositoryModelEvaluationBatch{point: profiled.WorkSizingPlan[0]}
	if repositoryModelEvaluationAccountRef(profiled) != "account" ||
		repositoryModelEvaluationReviewFocus(profiled) != "review\n\nAdditional scope guidance: scope" ||
		repositoryModelEvaluationBatchFilesLimit(profiled, batch) != 4 ||
		repositoryModelEvaluationBatchContentLimit(profiled, batch) != 100 ||
		repositoryModelEvaluationParallelChildren(profiled) != 7 ||
		repositoryModelEvaluationConfiguredPointID(profiled) != "files" {
		t.Fatal("profile sizing helpers mismatch")
	}
	profiled.Focus.FreeText = ""
	if repositoryModelEvaluationReviewFocus(profiled) != "review" {
		t.Fatal("profile review focus without scope mismatch")
	}
}

func TestRepositoryModelEvaluationRecoveryHelperCoverage(t *testing.T) {
	if repositoryModelEvaluationCandidateIDsOverlap([]string{"a"}, []string{"b"}) ||
		!repositoryModelEvaluationCandidateIDsOverlap([]string{"a"}, []string{"a"}) {
		t.Fatal("candidate overlap mismatch")
	}
	batch := repositoryModelEvaluationBatch{ids: []string{"a", "b"}, models: []string{"m1", "m2"}}
	valid := repoeval.BatchCheckpoint{
		CandidateIDs: []string{"a", "b"},
		Candidates: map[string]repoeval.BatchCandidateCheckpoint{
			"m1": {CompletedCandidateIDs: []string{"a", "b"}, Attempts: 1, Successes: 1},
			"m2": {CompletedCandidateIDs: []string{"a", "b"}, Attempts: 1, Successes: 1},
		},
	}
	if !repositoryModelEvaluationCheckpointCompletesBatch(valid, batch) ||
		!repositoryModelEvaluationCheckpointCompletesBatch(
			repoeval.BatchCheckpoint{CandidateIDs: []string{"a", "b"}}, batch,
		) {
		t.Fatal("complete checkpoint rejected")
	}
	for name, mutate := range map[string]func(*repoeval.BatchCheckpoint){
		"wrong count":   func(value *repoeval.BatchCheckpoint) { value.CandidateIDs = []string{"a"} },
		"missing alias": func(value *repoeval.BatchCheckpoint) { delete(value.Candidates, "m2") },
		"failure": func(value *repoeval.BatchCheckpoint) {
			outcome := value.Candidates["m1"]
			outcome.Failures = 1
			value.Candidates["m1"] = outcome
		},
		"missing id": func(value *repoeval.BatchCheckpoint) {
			outcome := value.Candidates["m1"]
			outcome.CompletedCandidateIDs = []string{"a", "x"}
			value.Candidates["m1"] = outcome
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := valid
			value.CandidateIDs = append([]string(nil), valid.CandidateIDs...)
			value.Candidates = map[string]repoeval.BatchCandidateCheckpoint{}
			for alias, outcome := range valid.Candidates {
				outcome.CompletedCandidateIDs = append([]string(nil), outcome.CompletedCandidateIDs...)
				value.Candidates[alias] = outcome
			}
			mutate(&value)
			if repositoryModelEvaluationCheckpointCompletesBatch(value, batch) {
				t.Fatal("incomplete checkpoint accepted")
			}
		})
	}

	if got := repositoryModelEvaluationDiscardPartialJudgedBatches(repoeval.Evaluation{}); got.Corpus != nil {
		t.Fatal("empty discard changed evaluation")
	}
	if stats := repositoryModelEvaluationScoreStats(nil); stats.Samples != 0 {
		t.Fatalf("empty score stats = %#v", stats)
	}
	if _, _, _, ok := repositoryModelEvaluationBatchScores(
		repoeval.BatchCheckpoint{MappingJSON: `{`, JudgeJSON: `{`}, "m1",
	); ok {
		t.Fatal("invalid batch scores accepted")
	}
	if _, _, _, ok := repositoryModelEvaluationBatchScores(
		repoeval.BatchCheckpoint{MappingJSON: `[]`, JudgeJSON: `{"evaluations":[]}`}, "m1",
	); ok {
		t.Fatal("missing batch score accepted")
	}
}

func TestRepositoryModelEvaluationPatchHelperCoverage(t *testing.T) {
	evaluation := &repoeval.Evaluation{}
	repository, ref := "repository", "ref"
	candidates := []string{"a", "b"}
	selector, judge := "selector", "judge"
	focus := repoeval.Focus{FreeText: "focus"}
	defaultFiles := 3
	languageFiles := map[string]int{"go": 2}
	applyRepositoryModelEvaluationPatch(evaluation, repositoryModelEvaluationPatchRequest{
		Repository: &repository, Ref: &ref, CandidateModels: &candidates,
		SelectorModelAlias: &selector, JudgeModelAlias: &judge, Focus: &focus,
		DefaultFilesPerLanguage: &defaultFiles, FilesPerLanguage: &languageFiles,
	})
	languageFiles["go"] = 99
	if evaluation.Repository != repository || evaluation.Ref != ref ||
		!slices.Equal(evaluation.CandidateModels, candidates) || evaluation.SelectorModelAlias != selector ||
		evaluation.JudgeModelAlias != judge || evaluation.Focus.FreeText != "focus" ||
		evaluation.DefaultFilesPerLanguage != 3 || evaluation.FilesPerLanguage["go"] != 2 {
		t.Fatalf("patch projection = %#v", evaluation)
	}
	applyRepositoryModelEvaluationMaterialized(nil, repoeval.CreateRequest{})
	request := repoeval.CreateRequest{
		Repository: "materialized", Ref: "main", CandidateModels: []string{"m1", "m2"},
		SelectorModelAlias: "m1", JudgeModelAlias: "judge", Focus: repoeval.Focus{FreeText: "f"},
		Profile: &repoeval.ProfileSnapshot{ID: "p"}, DefaultFilesPerLanguage: 20,
		FilesPerLanguage: map[string]int{"go": 1}, WorkSizingPlan: []repoeval.WorkSizingPoint{{ID: "point"}},
	}
	applyRepositoryModelEvaluationMaterialized(evaluation, request)
	request.FilesPerLanguage["go"] = 5
	if evaluation.Repository != "materialized" || evaluation.Profile.ID != "p" ||
		evaluation.FilesPerLanguage["go"] != 1 || len(evaluation.WorkSizingPlan) != 1 {
		t.Fatalf("materialized projection = %#v", evaluation)
	}

	key := repositoryModelEvaluationCompletionKey(" point ", "alias", "candidate")
	if key != "point\x00alias\x00candidate" {
		t.Fatalf("completion key = %q", key)
	}
	if repositoryModelEvaluationBatchAttempt(nil, "", nil, nil) != 0 {
		t.Fatal("empty attempt count changed")
	}
	if got, _ := repositoryModelEvaluationCapRepresentativeCorpus(nil, 1); len(got) != 0 {
		t.Fatalf("nil corpus cap = %#v", got)
	}
	_ = time.Now() // keep this test file's direct time import stable on all build tags
}
