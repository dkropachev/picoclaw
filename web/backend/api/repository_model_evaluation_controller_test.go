package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoeval"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestRepositoryModelEvaluationUsagePricesConcreteModelsAndSeparatesJudge(t *testing.T) {
	handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
	cfg := mustLoadRepositoryModelEvaluationConfig(t, handler.configPath)
	cfg.ModelList[0].InputPricePerMTok = 2
	cfg.ModelList[0].OutputPricePerMTok = 4
	if err := config.SaveConfig(handler.configPath, cfg); err != nil {
		t.Fatal(err)
	}
	store, _, err := handler.repositoryModelEvaluationStore()
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(t.Context(), repositoryModelEvaluationCreateRequest("owner/repo"))
	if err != nil {
		t.Fatal(err)
	}
	preflighting, err := store.Update(
		t.Context(),
		created.ID,
		created.Version,
		func(candidate *repoeval.Evaluation) error {
			candidate.Status = repoeval.StatusPreflighting
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	controller := newRepositoryModelEvaluationController(handler)
	manifest, progress, _, err := controller.preflightManifest(
		preflighting,
		"wr_selector",
		repositoryModelEvaluationPreflightResult().Outputs,
	)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := store.Update(
		t.Context(),
		created.ID,
		preflighting.Version,
		func(candidate *repoeval.Evaluation) error {
			candidate.Status = repoeval.StatusReady
			candidate.Corpus = manifest
			candidate.Progress = progress
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	running, err := store.Update(t.Context(), created.ID, ready.Version, func(candidate *repoeval.Evaluation) error {
		candidate.Status = repoeval.StatusRunning
		candidate.Progress.Stage = repoeval.ProgressCandidateExecution
		candidate.Progress.TotalTasks = 4
		candidate.Checkpoint.ConcreteModels = make(map[string]map[string]int)
		candidate.ModelStats = map[string]repoeval.ModelStats{
			"model-a": {FilesSelected: 2, StartedAt: &now},
			"model-b": {FilesSelected: 2, StartedAt: &now},
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	controller.store = store
	token, _, cancel, err := controller.reserveActive(running.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	defer controller.releaseActive(running.ID, token)
	observe := controller.usageObserver(
		running.ID,
		token,
		workflows.RepositoryModelEvaluationBatchWorkflowRef,
	)
	if observeErr := observe(workflows.AgentUsageEvent{StepID: "candidates", Usage: workflows.AgentUsage{
		Model: "gpt-a", Reviewer: "model-a", PromptTokens: 100, CompletionTokens: 20,
		CachedTokens: 10, ReasoningTokens: 7, LatencyMillis: 9,
	}}); observeErr != nil {
		t.Fatal(observeErr)
	}
	priced, found, err := store.Get(t.Context(), running.ID)
	if err != nil || !found {
		t.Fatalf("priced evaluation found=%v err=%v", found, err)
	}
	wantCandidateCost := (100.0*2 + 20.0*4) / 1_000_000
	stats := priced.ModelStats["model-a"]
	if stats.Usage.Requests != 1 || stats.Usage.ReasoningTokens != 7 ||
		stats.Usage.EstimatedCostUSD == nil || *stats.Usage.EstimatedCostUSD != wantCandidateCost ||
		priced.Checkpoint.ConcreteModels["model-a"]["gpt-a"] != 1 {
		t.Fatalf("candidate usage=%#v concrete=%#v", stats.Usage, priced.Checkpoint.ConcreteModels)
	}
	if observeErr := observe(workflows.AgentUsageEvent{StepID: "judge", Usage: workflows.AgentUsage{
		Model: "gpt-judge", Reviewer: "model-a", PromptTokens: 50, CompletionTokens: 10,
		ReasoningTokens: 3,
	}}); observeErr != nil {
		t.Fatal(observeErr)
	}
	separated, _, err := store.Get(t.Context(), running.ID)
	if err != nil {
		t.Fatal(err)
	}
	if separated.Usage.Requests != 2 || separated.Usage.ReasoningTokens != 10 ||
		separated.Usage.EstimatedCostUSD == nil || separated.ModelStats["model-a"].Usage.Requests != 1 ||
		*separated.ModelStats["model-a"].Usage.EstimatedCostUSD != wantCandidateCost {
		t.Fatalf("global=%#v candidate=%#v", separated.Usage, separated.ModelStats["model-a"].Usage)
	}

	unknown := workflows.AgentUsage{Model: "unpriced-concrete", Reviewer: "model-a", PromptTokens: 1}
	if recordErr := controller.recordUsage(running.ID, token, unknown, true); recordErr != nil {
		t.Fatal(recordErr)
	}
	latched, _, err := store.Get(t.Context(), running.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latched.Usage.EstimatedCostUSD != nil || latched.ModelStats["model-a"].Usage.EstimatedCostUSD != nil {
		t.Fatalf(
			"unknown price was represented as known: global=%#v candidate=%#v",
			latched.Usage,
			latched.ModelStats["model-a"].Usage,
		)
	}
	if _, known := repositoryModelEvaluationUsagePrice(nil, workflows.AgentUsage{}); known {
		t.Fatal("nil config produced a known price")
	}
	if price, known := repositoryModelEvaluationUsagePrice(cfg, workflows.AgentUsage{Reviewer: "model-a"}); !known ||
		price.inputPerMillion != 2 ||
		price.outputPerMillion != 4 {
		t.Fatalf("reviewer fallback price=%#v known=%v", price, known)
	}
	qualifiedPrice, qualifiedKnown := repositoryModelEvaluationUsagePrice(
		cfg,
		workflows.AgentUsage{Model: "provider/gpt-a"},
	)
	if !qualifiedKnown ||
		qualifiedPrice.inputPerMillion != 2 ||
		qualifiedPrice.outputPerMillion != 4 {
		t.Fatalf("provider-qualified price=%#v known=%v", qualifiedPrice, qualifiedKnown)
	}
	if len(repositoryModelEvaluationPriceKeys("")) != 0 ||
		!slices.Equal(repositoryModelEvaluationPriceKeys("provider/gpt-a"), []string{"provider/gpt-a", "gpt-a"}) {
		t.Fatal("price identity keys are incorrect")
	}
	subscriptionConfig := config.DefaultConfig()
	subscriptionConfig.Agents.Defaults.AccountRef = "evaluation-router"
	subscriptionConfig.ModelAliases = []config.ModelAliasConfig{
		{Name: "subscription-alias", Model: "openai/subscription"},
		{Name: "metered-alias", Model: "openai/metered"},
	}
	subscriptionConfig.ModelList = []*config.ModelConfig{
		{
			ModelName: "subscription", Provider: "openai", Model: "openai/subscription", Enabled: true,
			Subscription: true, SubscriptionEquivalentModel: "metered-alias",
		},
		{
			ModelName: "metered", Provider: "openai", Model: "openai/metered", Enabled: true,
			InputPricePerMTok: 1.25, OutputPricePerMTok: 5,
		},
	}
	subscriptionConfig.AccountRouters = []config.AccountRouterConfig{{
		Name: "evaluation-router", Enabled: true, Entry: "accounts",
		Blocks: []config.AccountRouterBlock{{
			ID: "accounts", Type: config.AccountRouterBlockTypeLoadBalance,
			Accounts: []string{"subscription", "metered"},
		}},
	}}
	subscriptionPrice, subscriptionKnown := repositoryModelEvaluationUsagePrice(
		subscriptionConfig,
		workflows.AgentUsage{Model: "openai/subscription"},
	)
	if !subscriptionKnown || subscriptionPrice.inputPerMillion != 1.25 ||
		subscriptionPrice.outputPerMillion != 5 {
		t.Fatalf("subscription-equivalent price=%#v known=%v", subscriptionPrice, subscriptionKnown)
	}
	repositoryModelEvaluationAddUsage(nil, workflows.AgentUsage{}, repositoryModelEvaluationPrice{}, false)
	unknownThenKnown := repoeval.Usage{}
	repositoryModelEvaluationAddUsage(
		&unknownThenKnown,
		workflows.AgentUsage{PromptTokens: 1},
		repositoryModelEvaluationPrice{},
		false,
	)
	repositoryModelEvaluationAddUsage(
		&unknownThenKnown,
		workflows.AgentUsage{PromptTokens: 1},
		repositoryModelEvaluationPrice{inputPerMillion: 2},
		true,
	)
	if unknownThenKnown.EstimatedCostUSD != nil {
		t.Fatal("known request erased an earlier unknown-price latch")
	}
}

func TestRepositoryModelEvaluationPendingBatchesResumeOnlyMissingAliasFilePairs(t *testing.T) {
	evaluation := repositoryModelEvaluationMetricsFixture()
	evaluation.Checkpoint.Batches = []repoeval.BatchCheckpoint{{
		ID: "attempt-one", CandidateIDs: []string{"candidate-go", "candidate-ts"},
		Candidates: map[string]repoeval.BatchCandidateCheckpoint{
			"model-a": {
				CompletedCandidateIDs: []string{"candidate-go"}, Attempts: 1, Successes: 1,
			},
			"model-b": {
				CompletedCandidateIDs: []string{"candidate-go", "candidate-ts"}, Attempts: 1, Successes: 1,
			},
		},
		JudgeJSON: `{}`, MappingJSON: `[]`, CompletedAt: time.Now().UTC(),
	}}
	pending := repositoryModelEvaluationPendingBatches(evaluation)
	if len(pending) != 1 || !slices.Equal(pending[0].models, []string{"model-a"}) ||
		!slices.Equal(pending[0].ids, []string{"candidate-ts"}) {
		t.Fatalf("pending resume batches=%#v", pending)
	}
	evaluation.Checkpoint.Batches = append(evaluation.Checkpoint.Batches, repoeval.BatchCheckpoint{
		ID: "attempt-two", CandidateIDs: []string{"candidate-ts"},
		Candidates: map[string]repoeval.BatchCandidateCheckpoint{
			"model-a": {
				CompletedCandidateIDs: []string{"candidate-ts"}, Attempts: 1, Successes: 1,
			},
		},
		JudgeJSON: `{}`, MappingJSON: `[]`, CompletedAt: time.Now().UTC(),
	})
	if pending = repositoryModelEvaluationPendingBatches(evaluation); len(pending) != 0 {
		t.Fatalf("successful alias/file tasks were scheduled again: %#v", pending)
	}
}

func TestRepositoryModelEvaluationJudgeEvidenceRejectsAbsentDuplicateOrOmittedIDs(t *testing.T) {
	mapping := `[{"candidateId":"candidate-001","modelAlias":"model-a"},` +
		`{"candidateId":"candidate-002","modelAlias":"model-b"}]`
	validJudge := `{"evaluations":[{"candidateId":"candidate-001"},{"candidateId":"candidate-002"}]}`
	if err := repositoryModelEvaluationValidateJudgeEvidence(
		validJudge,
		mapping,
		[]string{"model-a", "model-b"},
	); err != nil {
		t.Fatal(err)
	}
	invalid := []struct {
		judge   string
		mapping string
		models  []string
	}{
		{judge: `{`, mapping: mapping, models: []string{"model-a", "model-b"}},
		{judge: validJudge, mapping: `{`, models: []string{"model-a", "model-b"}},
		{judge: validJudge, mapping: `[]`, models: []string{"model-a", "model-b"}},
		{judge: `{"evaluations":[]}`, mapping: mapping, models: []string{"model-a", "model-b"}},
		{
			judge:   `{"evaluations":[{"candidateId":"candidate-001"},{"candidateId":"candidate-001"}]}`,
			mapping: mapping,
			models:  []string{"model-a", "model-b"},
		},
		{
			judge:   `{"evaluations":[{"candidateId":"candidate-001"},{"candidateId":"candidate-003"}]}`,
			mapping: mapping,
			models:  []string{"model-a", "model-b"},
		},
		{judge: validJudge, mapping: mapping, models: []string{"model-b"}},
		{
			judge: validJudge,
			mapping: `[{"candidateId":"candidate-001","modelAlias":"model-a"},` +
				`{"candidateId":"candidate-001","modelAlias":"model-b"}]`,
			models: []string{"model-a", "model-b"},
		},
		{
			judge: validJudge,
			mapping: `[{"candidateId":"candidate-001","modelAlias":"model-a"},` +
				`{"candidateId":"candidate-002","modelAlias":"model-a"}]`,
			models: []string{"model-a", "model-b"},
		},
		{
			judge:   `{"evaluations":[{"candidateId":""}]}`,
			mapping: `[{"candidateId":"","modelAlias":"model-a"}]`,
			models:  []string{"model-a"},
		},
	}
	for index, test := range invalid {
		if err := repositoryModelEvaluationValidateJudgeEvidence(
			test.judge,
			test.mapping,
			test.models,
		); err == nil {
			t.Fatalf("invalid judge evidence case %d succeeded", index)
		}
	}
	confirmed, unsupported := repositoryModelEvaluationJudgedClaimCounts([]repoeval.BatchCheckpoint{
		{MappingJSON: `{`, JudgeJSON: `{}`},
		{
			MappingJSON: `[{"candidateId":"candidate-001","modelAlias":"model-a"}]`,
			JudgeJSON: `{"evaluations":[` +
				`{"candidateId":"missing","confirmedClaims":9,"unsupportedClaims":9},` +
				`{"candidateId":"candidate-001","confirmedClaims":-1,"unsupportedClaims":-1}]}`,
		},
	})
	if confirmed["model-a"] != 0 || unsupported["model-a"] != 0 || len(confirmed) != 1 || len(unsupported) != 1 {
		t.Fatalf("bounded judged claim counts=%v/%v", confirmed, unsupported)
	}
}

func TestRepositoryModelEvaluationAnalysisReceivesPerAliasFileWeights(t *testing.T) {
	evaluation := repositoryModelEvaluationMetricsFixture()
	evaluation.Checkpoint.Batches = []repoeval.BatchCheckpoint{{
		ID: "weighted", CandidateIDs: []string{"candidate-go", "candidate-ts"},
		Candidates: map[string]repoeval.BatchCandidateCheckpoint{
			"model-a": {
				CompletedCandidateIDs: []string{"candidate-go"}, Attempts: 1, Successes: 1,
			},
			"model-b": {
				CompletedCandidateIDs: []string{"candidate-go", "candidate-ts"}, Attempts: 1, Successes: 1,
			},
		},
		JudgeJSON: `{"evaluations":[]}`, MappingJSON: `[]`, CompletedAt: time.Now().UTC(),
	}}
	var captured map[string]any
	controller := &repositoryModelEvaluationController{}
	controller.runWorkflow = func(
		_ context.Context,
		_ string,
		_ string,
		_ string,
		inputs map[string]any,
		_ workflows.AgentUsageEventObserver,
	) (*workflows.RunResult, error) {
		captured = inputs
		return &workflows.RunResult{Status: workflows.RunStatusSucceeded}, nil
	}
	if _, err := controller.runEvaluationAnalysis(
		t.Context(),
		evaluation,
		"wr-weighted",
		"token",
	); err != nil {
		t.Fatal(err)
	}
	var batches []struct {
		CandidateIDs []string                                     `json:"candidateIds"`
		Outcomes     map[string]repoeval.BatchCandidateCheckpoint `json:"candidateOutcomes"`
	}
	if err := json.Unmarshal([]byte(captured["judged_batches"].(string)), &batches); err != nil {
		t.Fatal(err)
	}
	if len(batches) != 1 || !slices.Equal(batches[0].CandidateIDs, []string{"candidate-go", "candidate-ts"}) ||
		len(batches[0].Outcomes["model-a"].CompletedCandidateIDs) != 1 ||
		len(batches[0].Outcomes["model-b"].CompletedCandidateIDs) != 2 {
		t.Fatalf("weighted analysis evidence=%#v", batches)
	}
}

func TestRepositoryModelEvaluationBatchOutcomesDoNotClaimUnreadFiles(t *testing.T) {
	evaluation := repositoryModelEvaluationMetricsFixture()
	base := repositoryModelEvaluationBatches(evaluation)[0]
	base.models = []string{"model-a"}
	outcomes := repositoryModelEvaluationBatchOutcomes(base, []map[string]any{{
		"model": map[string]any{"requested": "model-a", "selected": "gpt-a"},
		"valid": true,
		"scope": []map[string]any{
			{"path": "pkg/core.go", "contentComplete": true},
			{"path": "web/app.ts", "contentComplete": false, "contentUnavailable": "bounded"},
		},
	}})
	outcome := outcomes["model-a"]
	if outcome.Attempts != 1 || outcome.Successes != 1 || outcome.Failures != 0 ||
		!slices.Equal(outcome.CompletedCandidateIDs, []string{"candidate-go"}) {
		t.Fatalf("unread file outcome=%#v", outcome)
	}
}

func TestRepositoryModelEvaluationBatchOutcomesNeverFabricateProviderAttempts(t *testing.T) {
	evaluation := repositoryModelEvaluationMetricsFixture()
	base := repositoryModelEvaluationBatches(evaluation)[0]
	base.models = []string{"model-a"}
	outcome := repositoryModelEvaluationBatchOutcomes(base, []map[string]any{{
		"model": map[string]any{"requested": "model-a", "selected": "gpt-a"},
		"valid": true,
		"scope": []map[string]any{{"path": "pkg/core.go", "contentComplete": true}},
	}})["model-a"]
	if outcome.Attempts != 1 || outcome.Successes != 1 || outcome.Failures != 0 ||
		!slices.Equal(outcome.CompletedCandidateIDs, []string{"candidate-go"}) {
		t.Fatalf("actual child outcome gained phantom attempts: %#v", outcome)
	}
}

func TestRepositoryModelEvaluationBatchOutcomeAndAttemptEdges(t *testing.T) {
	evaluation := repositoryModelEvaluationMetricsFixture()
	base := repositoryModelEvaluationBatches(evaluation)[0]
	base.models = []string{"model-a"}
	children := []map[string]any{
		{"model": map[string]any{"requested": "outside"}, "valid": true},
		{
			"model": map[string]any{"selected": "model-a"},
			"valid": false,
			"error": "invalid structured output",
		},
		{
			"model": map[string]any{"requested": "model-a"},
			"valid": true,
			"scope": "not structured scope",
		},
	}
	outcome := repositoryModelEvaluationBatchOutcomes(base, children)["model-a"]
	if outcome.Attempts != 2 || outcome.Successes != 1 || outcome.Failures != 1 ||
		len(outcome.CompletedCandidateIDs) != 0 {
		t.Fatalf("edge child outcomes=%#v", outcome)
	}
	if outcomes := repositoryModelEvaluationBatchOutcomes(base, make(chan int)); outcomes["model-a"].Attempts != 0 {
		t.Fatalf("malformed managed children produced attempts: %#v", outcomes)
	}
	if attempt := repositoryModelEvaluationBatchAttempt(
		[]repoeval.BatchCheckpoint{{
			CandidateIDs: []string{"candidate-go"},
			Candidates:   map[string]repoeval.BatchCandidateCheckpoint{"different": {}},
		}},
		[]string{"model-a"},
		[]string{"candidate-go"},
	); attempt != 0 {
		t.Fatalf("different checkpoint counted as attempt: %d", attempt)
	}
	if attempt := repositoryModelEvaluationBatchAttempt(
		[]repoeval.BatchCheckpoint{{
			CandidateIDs: []string{"candidate-go"},
			Candidates:   map[string]repoeval.BatchCandidateCheckpoint{"model-a": {}},
		}},
		[]string{"model-a"},
		[]string{"candidate-go"},
	); attempt != 1 {
		t.Fatalf("matching checkpoint attempt=%d", attempt)
	}
	unavailable := repositoryModelEvaluationBatchOutcomes(base, []map[string]any{{
		"model": map[string]any{"requested": "model-a"},
		"valid": true,
		"scope": []map[string]any{{
			"path": "pkg/core.go", "contentUnavailable": "aggregate_limit",
		}},
	}})["model-a"]
	if unavailable.Successes != 1 || len(unavailable.CompletedCandidateIDs) != 0 {
		t.Fatalf("unavailable content counted as analyzed: %#v", unavailable)
	}
	legacy := evaluation
	legacy.Checkpoint.Batches = []repoeval.BatchCheckpoint{{
		CandidateIDs: []string{"candidate-go", "candidate-ts"},
		JudgeJSON:    `{}`, MappingJSON: `[]`, CompletedAt: time.Now().UTC(),
	}}
	legacy.ModelStats = nil
	legacy.Progress.TotalTasks = 10
	repositoryModelEvaluationApplyCheckpointMetrics(&legacy)
	if legacy.Progress.CompletedFiles != 2 || legacy.Progress.CompletedTasks != 3 ||
		legacy.ModelStats["model-a"].FilesCompleted != 2 || legacy.ModelStats["model-b"].FilesCompleted != 2 {
		t.Fatalf("legacy checkpoint metrics=%#v/%#v", legacy.Progress, legacy.ModelStats)
	}
	repositoryModelEvaluationApplyCheckpointMetrics(nil)
	repositoryModelEvaluationApplyCheckpointMetrics(&repoeval.Evaluation{})
}

func TestRepositoryModelEvaluationComparisonsUseCheckpointCoverageAndFailures(t *testing.T) {
	evaluation := repositoryModelEvaluationMetricsFixture()
	evaluation.CandidateModels = append(evaluation.CandidateModels, "model-c")
	evaluation.ModelStats = map[string]repoeval.ModelStats{
		"model-a": {Usage: repoeval.Usage{Requests: 1}},
		"model-b": {Usage: repoeval.Usage{Requests: 1}},
		"model-c": {Usage: repoeval.Usage{Requests: 1}},
	}
	evaluation.Checkpoint.Batches = []repoeval.BatchCheckpoint{{
		ID: "partial", CandidateIDs: []string{"candidate-go", "candidate-ts"},
		Candidates: map[string]repoeval.BatchCandidateCheckpoint{
			"model-a": {
				CompletedCandidateIDs: []string{"candidate-go"}, Attempts: 2, Successes: 1, Failures: 1,
			},
			"model-b": {
				CompletedCandidateIDs: []string{"candidate-go", "candidate-ts"},
				Attempts:              2, Successes: 2,
			},
			"model-c": {Attempts: 2, Failures: 2},
		},
		JudgeJSON: `{}`, MappingJSON: `[]`, CompletedAt: time.Now().UTC(),
	}}
	score := 70.0
	rows, _, err := repositoryModelEvaluationComparisons(evaluation, map[string]any{
		"comparisons": []map[string]any{
			{
				"modelAlias": "model-a", "rank": 1, "completion": "completed",
				"scores": map[string]float64{}, "overallScore": score, "verdict": "partial",
			},
			{
				"modelAlias": "model-b", "rank": 2, "completion": "failed",
				"scores": map[string]float64{"correctness": 70}, "overallScore": score, "verdict": "complete",
			},
			{
				"modelAlias": "model-c", "rank": 3, "completion": "completed",
				"scores": map[string]float64{}, "overallScore": score, "verdict": "failed",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].Completion != repoeval.ModelCompletionPartial ||
		rows[0].OverallScore != nil || rows[0].Rank != 0 || len(rows[0].Scores) != 0 ||
		rows[0].FilesAnalyzed != 1 || rows[0].BytesAnalyzed != 120 ||
		!slices.Equal(rows[0].Languages, []string{"go"}) || !slices.Equal(rows[0].Regions, []string{"pkg"}) ||
		rows[0].Failures != 1 || rows[1].Completion != repoeval.ModelCompletionCompleted ||
		rows[1].FilesAnalyzed != 2 || rows[1].BytesAnalyzed != 210 || rows[1].OverallScore == nil ||
		rows[2].Completion != repoeval.ModelCompletionFailed || rows[2].FilesAnalyzed != 0 ||
		rows[2].BytesAnalyzed != 0 || len(rows[2].Languages) != 0 || len(rows[2].Regions) != 0 ||
		rows[2].Failures != 2 || rows[2].OverallScore != nil || rows[2].Rank != 0 {
		t.Fatalf("checkpoint-derived comparison rows=%#v", rows)
	}
}

func TestRepositoryModelEvaluationFinalProgressPreservesPartialCoverage(t *testing.T) {
	handler, mux, _ := newRepositoryModelEvaluationTestHandler(t)
	controller := newRepositoryModelEvaluationController(handler)
	controller.runWorkflow = func(
		_ context.Context,
		_ string,
		ref string,
		_ string,
		_ map[string]any,
		_ workflows.AgentUsageEventObserver,
	) (*workflows.RunResult, error) {
		switch ref {
		case workflows.RepositoryModelEvaluationPreflightWorkflowRef:
			return repositoryModelEvaluationPreflightResult(), nil
		case workflows.RepositoryModelEvaluationBatchWorkflowRef:
			result := repositoryModelEvaluationBatchResult()
			result.Outputs["candidates"] = []map[string]any{
				{
					"model": map[string]any{"requested": "model-a", "selected": "gpt-a"},
					"valid": true,
					"scope": []map[string]any{{"path": "pkg/core.go"}, {"path": "web/app.ts"}},
				},
				{
					"model": map[string]any{"requested": "model-b", "selected": "gpt-b"},
					"valid": false, "run_error": "candidate failed",
					"scope": []map[string]any{{"path": "pkg/core.go"}, {"path": "web/app.ts"}},
				},
			}
			return result, nil
		case workflows.RepositoryModelEvaluationAnalysisWorkflowRef:
			return repositoryModelEvaluationAnalysisResult(), nil
		default:
			return nil, errors.New("unexpected workflow")
		}
	}
	handler.repositoryModelEvaluationController = controller
	t.Cleanup(handler.Shutdown)
	created := createRepositoryModelEvaluation(t, mux, "owner/partial-progress")
	repositoryModelEvaluationMutation(
		t,
		mux,
		http.MethodPost,
		"/api/model-evaluations/"+created.ID+"/preflight",
		map[string]any{"expected_version": created.Version},
	)
	ready := waitRepositoryModelEvaluationStatus(t, handler, created.ID, repoeval.StatusReady)
	repositoryModelEvaluationMutation(
		t,
		mux,
		http.MethodPost,
		"/api/model-evaluations/"+created.ID+"/start",
		map[string]any{"expected_version": ready.Version},
	)
	completed := waitRepositoryModelEvaluationStatus(t, handler, created.ID, repoeval.StatusCompleted)
	if completed.Progress.CompletedFiles != 0 || completed.Progress.CompletedTasks != completed.Progress.TotalTasks ||
		completed.Progress.Languages["go"].CompletedFiles != 0 ||
		completed.Progress.Languages["typescript"].CompletedFiles != 0 || len(completed.Comparisons) != 2 ||
		completed.Comparisons[0].Completion != repoeval.ModelCompletionCompleted ||
		completed.Comparisons[1].Completion != repoeval.ModelCompletionFailed ||
		completed.Comparisons[1].OverallScore != nil || completed.Comparisons[1].Failures != 1 {
		t.Fatalf("partial terminal evaluation=%#v", completed)
	}
}

func TestRepositoryModelEvaluationInvalidJudgeEvidenceFailsBeforeCheckpoint(t *testing.T) {
	handler, mux, _ := newRepositoryModelEvaluationTestHandler(t)
	controller := newRepositoryModelEvaluationController(handler)
	controller.runWorkflow = func(
		_ context.Context,
		_ string,
		ref string,
		_ string,
		_ map[string]any,
		_ workflows.AgentUsageEventObserver,
	) (*workflows.RunResult, error) {
		switch ref {
		case workflows.RepositoryModelEvaluationPreflightWorkflowRef:
			return repositoryModelEvaluationPreflightResult(), nil
		case workflows.RepositoryModelEvaluationBatchWorkflowRef:
			result := repositoryModelEvaluationBatchResult()
			result.Outputs["judge"] = map[string]any{"evaluations": []map[string]any{}}
			return result, nil
		default:
			return nil, errors.New("analysis must not run after invalid judge evidence")
		}
	}
	handler.repositoryModelEvaluationController = controller
	t.Cleanup(handler.Shutdown)
	created := createRepositoryModelEvaluation(t, mux, "owner/invalid-judge-evidence")
	repositoryModelEvaluationMutation(
		t,
		mux,
		http.MethodPost,
		"/api/model-evaluations/"+created.ID+"/preflight",
		map[string]any{"expected_version": created.Version},
	)
	ready := waitRepositoryModelEvaluationStatus(t, handler, created.ID, repoeval.StatusReady)
	repositoryModelEvaluationMutation(
		t,
		mux,
		http.MethodPost,
		"/api/model-evaluations/"+created.ID+"/start",
		map[string]any{"expected_version": ready.Version},
	)
	failed := waitRepositoryModelEvaluationStatus(t, handler, created.ID, repoeval.StatusFailed)
	if !strings.Contains(failed.Failure, "judge omitted") || len(failed.Checkpoint.Batches) != 0 {
		t.Fatalf("invalid judge evidence persisted=%#v", failed)
	}
}

func repositoryModelEvaluationMetricsFixture() repoeval.Evaluation {
	return repoeval.Evaluation{
		ID: "rme_metrics", CandidateModels: []string{"model-a", "model-b"},
		Corpus: &repoeval.CorpusManifest{
			InventoryHash:  "inventory",
			LanguageCounts: map[string]int{"go": 1, "typescript": 1},
			Files: []repoeval.CorpusFile{
				{
					CandidateID: "candidate-go", Path: "pkg/core.go", SizeBytes: 120,
					Language: "go", Region: "pkg",
				},
				{
					CandidateID: "candidate-ts", Path: "web/app.ts", SizeBytes: 90,
					Language: "typescript", Region: "web",
				},
			},
		},
		Progress: repoeval.Progress{
			TotalFiles: 2, SelectedFiles: 2, TotalTasks: 4,
			Languages: map[string]repoeval.LanguageProgress{
				"go":         {SelectedFiles: 1},
				"typescript": {SelectedFiles: 1},
			},
		},
		ModelStats: map[string]repoeval.ModelStats{"model-a": {}, "model-b": {}},
	}
}

func TestRepositoryModelEvaluationBatchUsesCanonicalGitWorkspaceRepository(t *testing.T) {
	evaluation := repositoryModelEvaluationMetricsFixture()
	evaluation.Repository = "scylladb/seastar"
	evaluation.JudgeModelAlias = "judge"
	evaluation.Corpus.CommitSHA = strings.Repeat("a", 40)
	var captured map[string]any
	controller := &repositoryModelEvaluationController{}
	controller.runWorkflow = func(
		_ context.Context,
		_ string,
		_ string,
		_ string,
		inputs map[string]any,
		_ workflows.AgentUsageEventObserver,
	) (*workflows.RunResult, error) {
		captured = inputs
		return &workflows.RunResult{Status: workflows.RunStatusSucceeded}, nil
	}
	batches := repositoryModelEvaluationBatches(evaluation)
	if len(batches) != 1 {
		t.Fatalf("batches=%#v", batches)
	}
	if _, err := controller.runEvaluationBatch(
		t.Context(),
		evaluation,
		batches[0],
		"wr_workspace_repository",
		"token",
	); err != nil {
		t.Fatal(err)
	}
	if captured["repository"] != "https://github.com/scylladb/seastar.git" {
		t.Fatalf("batch repository=%#v", captured["repository"])
	}
}

func TestRepositoryModelEvaluationControllerRecoversPreflight(t *testing.T) {
	handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
	store, _, err := handler.repositoryModelEvaluationStore()
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(t.Context(), repositoryModelEvaluationCreateRequest("owner/repo"))
	if err != nil {
		t.Fatal(err)
	}
	preflighting, err := store.Update(
		t.Context(),
		created.ID,
		created.Version,
		func(candidate *repoeval.Evaluation) error {
			candidate.Status = repoeval.StatusPreflighting
			candidate.RunIDs = append(candidate.RunIDs, "wr_orphaned_preflight")
			candidate.Progress.Stage = repoeval.ProgressInventorying
			return nil
		},
	)
	if err != nil || preflighting.RestartDirective() != repoeval.RecoveryResume {
		t.Fatalf("preflight=%#v err=%v", preflighting, err)
	}
	controller := newRepositoryModelEvaluationController(handler)
	controller.runWorkflow = func(_ context.Context, _ string, ref string, _ string, inputs map[string]any, _ workflows.AgentUsageEventObserver) (*workflows.RunResult, error) {
		if ref != workflows.RepositoryModelEvaluationPreflightWorkflowRef {
			t.Fatalf("recovery workflow ref=%q", ref)
		}
		if inputs["repository"] != "https://github.com/owner/repo.git" {
			t.Fatalf("recovery repository=%#v", inputs["repository"])
		}
		return repositoryModelEvaluationPreflightResult(), nil
	}
	handler.repositoryModelEvaluationController = controller
	t.Cleanup(handler.Shutdown)
	if err := controller.Start(); err != nil {
		t.Fatal(err)
	}
	ready := waitRepositoryModelEvaluationStatus(t, handler, created.ID, repoeval.StatusReady)
	if len(ready.RunIDs) != 2 || ready.RunIDs[0] != "wr_orphaned_preflight" {
		t.Fatalf("recovered run IDs=%v", ready.RunIDs)
	}
}

func TestRepositoryModelEvaluationControllerRecoversMissingBatches(t *testing.T) {
	handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
	store, _, err := handler.repositoryModelEvaluationStore()
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(t.Context(), repositoryModelEvaluationCreateRequest("owner/repo"))
	if err != nil {
		t.Fatal(err)
	}
	preflighting, err := store.Update(
		t.Context(),
		created.ID,
		created.Version,
		func(candidate *repoeval.Evaluation) error {
			candidate.Status = repoeval.StatusPreflighting
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	seed := newRepositoryModelEvaluationController(handler)
	manifest, progress, warnings, err := seed.preflightManifest(
		preflighting,
		"wr_selector",
		repositoryModelEvaluationPreflightResult().Outputs,
	)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := store.Update(
		t.Context(),
		created.ID,
		preflighting.Version,
		func(candidate *repoeval.Evaluation) error {
			candidate.Status = repoeval.StatusReady
			candidate.Corpus = manifest
			candidate.Progress = progress
			candidate.Warnings = warnings
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	_, err = store.Update(t.Context(), created.ID, ready.Version, func(candidate *repoeval.Evaluation) error {
		candidate.Status = repoeval.StatusRunning
		candidate.Progress.Stage = repoeval.ProgressCandidateExecution
		candidate.Progress.TotalTasks = 6
		candidate.Checkpoint = repoeval.Checkpoint{ConcreteModels: map[string]map[string]int{}}
		candidate.ModelStats = map[string]repoeval.ModelStats{
			"model-a": {FilesSelected: 2, StartedAt: &now},
			"model-b": {FilesSelected: 2, StartedAt: &now},
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	controller := newRepositoryModelEvaluationController(handler)
	var batchRuns int
	controller.runWorkflow = func(_ context.Context, _ string, ref string, _ string, _ map[string]any, _ workflows.AgentUsageEventObserver) (*workflows.RunResult, error) {
		switch ref {
		case workflows.RepositoryModelEvaluationBatchWorkflowRef:
			batchRuns++
			return repositoryModelEvaluationBatchResult(), nil
		case workflows.RepositoryModelEvaluationAnalysisWorkflowRef:
			return repositoryModelEvaluationAnalysisResult(), nil
		default:
			return nil, errors.New("unexpected recovery workflow")
		}
	}
	handler.repositoryModelEvaluationController = controller
	t.Cleanup(handler.Shutdown)
	if err := controller.Start(); err != nil {
		t.Fatal(err)
	}
	completed := waitRepositoryModelEvaluationStatus(t, handler, created.ID, repoeval.StatusCompleted)
	if batchRuns != 1 || len(completed.Checkpoint.Batches) != 1 {
		t.Fatalf("batchRuns=%d checkpoint=%#v", batchRuns, completed.Checkpoint)
	}

	// A second process sees terminal state and does not repeat durable batches.
	controller.Stop()
	second := newRepositoryModelEvaluationController(handler)
	second.runWorkflow = func(context.Context, string, string, string, map[string]any, workflows.AgentUsageEventObserver) (*workflows.RunResult, error) {
		t.Fatal("terminal evaluation was unexpectedly recovered")
		return nil, nil
	}
	handler.repositoryModelEvaluationController = second
	if err := second.Start(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryModelEvaluationControllerFailureAndShutdownRecoveryBoundary(t *testing.T) {
	t.Run("failure", func(t *testing.T) {
		handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
		controller := newRepositoryModelEvaluationController(handler)
		controller.runWorkflow = func(context.Context, string, string, string, map[string]any, workflows.AgentUsageEventObserver) (*workflows.RunResult, error) {
			return &workflows.RunResult{Status: workflows.RunStatusFailed, Error: "selector rejected output"}, nil
		}
		handler.repositoryModelEvaluationController = controller
		t.Cleanup(handler.Shutdown)
		store, _, _ := handler.repositoryModelEvaluationStore()
		created, err := store.Create(t.Context(), repositoryModelEvaluationCreateRequest("owner/repo"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := controller.Preflight(t.Context(), created.ID, created.Version); err != nil {
			t.Fatal(err)
		}
		failed := waitRepositoryModelEvaluationStatus(t, handler, created.ID, repoeval.StatusFailed)
		if !strings.Contains(failed.Failure, "selector rejected") || failed.Progress.Stage != repoeval.ProgressFailed {
			t.Fatalf("failed=%#v", failed)
		}
	})

	t.Run("shutdown leaves recovery state", func(t *testing.T) {
		handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
		controller := newRepositoryModelEvaluationController(handler)
		entered := make(chan struct{})
		controller.runWorkflow = func(ctx context.Context, _ string, _ string, _ string, _ map[string]any, _ workflows.AgentUsageEventObserver) (*workflows.RunResult, error) {
			close(entered)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		handler.repositoryModelEvaluationController = controller
		store, _, _ := handler.repositoryModelEvaluationStore()
		created, err := store.Create(t.Context(), repositoryModelEvaluationCreateRequest("owner/repo"))
		if err != nil {
			t.Fatal(err)
		}
		if _, resumeErr := controller.RunExisting(t.Context(), created.ID, created.Version); resumeErr != nil {
			t.Fatal(resumeErr)
		}
		<-entered
		controller.Stop()
		current, found, err := store.Get(t.Context(), created.ID)
		if err != nil || !found || current.Status != repoeval.StatusPreflighting {
			t.Fatalf("shutdown state=%#v found=%v err=%v", current, found, err)
		}
	})
}

func TestRepositoryModelEvaluationControllerBatchFailureAndRunningCancellation(t *testing.T) {
	t.Run("batch failure", func(t *testing.T) {
		handler, mux, _ := newRepositoryModelEvaluationTestHandler(t)
		controller := newRepositoryModelEvaluationController(handler)
		controller.runWorkflow = func(
			_ context.Context,
			_ string,
			ref string,
			_ string,
			_ map[string]any,
			_ workflows.AgentUsageEventObserver,
		) (*workflows.RunResult, error) {
			if ref == workflows.RepositoryModelEvaluationPreflightWorkflowRef {
				return repositoryModelEvaluationPreflightResult(), nil
			}
			return nil, errors.New("candidate provider unavailable")
		}
		handler.repositoryModelEvaluationController = controller
		t.Cleanup(handler.Shutdown)
		created := createRepositoryModelEvaluation(t, mux, "owner/repo")
		if response := repositoryModelEvaluationMutation(
			t,
			mux,
			http.MethodPost,
			"/api/model-evaluations/"+created.ID+"/preflight",
			map[string]any{"expected_version": created.Version},
		); response.Code != http.StatusAccepted {
			t.Fatalf("preflight status=%d body=%s", response.Code, response.Body.String())
		}
		ready := waitRepositoryModelEvaluationStatus(t, handler, created.ID, repoeval.StatusReady)
		if response := repositoryModelEvaluationMutation(
			t,
			mux,
			http.MethodPost,
			"/api/model-evaluations/"+created.ID+"/start",
			map[string]any{"expected_version": ready.Version},
		); response.Code != http.StatusAccepted {
			t.Fatalf("start status=%d body=%s", response.Code, response.Body.String())
		}
		failed := waitRepositoryModelEvaluationStatus(t, handler, created.ID, repoeval.StatusFailed)
		if !strings.Contains(failed.Failure, "provider unavailable") {
			t.Fatalf("failed=%#v", failed)
		}
		controller.runWorkflow = func(
			_ context.Context,
			_ string,
			ref string,
			_ string,
			_ map[string]any,
			_ workflows.AgentUsageEventObserver,
		) (*workflows.RunResult, error) {
			if ref == workflows.RepositoryModelEvaluationBatchWorkflowRef {
				return repositoryModelEvaluationBatchResult(), nil
			}
			if ref == workflows.RepositoryModelEvaluationAnalysisWorkflowRef {
				return repositoryModelEvaluationAnalysisResult(), nil
			}
			return nil, errors.New("unexpected resumed workflow")
		}
		resume := repositoryModelEvaluationMutation(
			t,
			mux,
			http.MethodPost,
			"/api/model-evaluations/"+created.ID+"/resume",
			map[string]any{"expected_version": failed.Version},
		)
		if resume.Code != http.StatusAccepted {
			t.Fatalf("resume failed status=%d body=%s", resume.Code, resume.Body.String())
		}
		completed := waitRepositoryModelEvaluationStatus(t, handler, created.ID, repoeval.StatusCompleted)
		if completed.ID != failed.ID || len(completed.Checkpoint.Batches) != 1 {
			t.Fatalf("resumed completion=%#v", completed)
		}
	})

	t.Run("running cancellation", func(t *testing.T) {
		handler, mux, _ := newRepositoryModelEvaluationTestHandler(t)
		controller := newRepositoryModelEvaluationController(handler)
		batchEntered := make(chan struct{})
		controller.runWorkflow = func(
			ctx context.Context,
			_ string,
			ref string,
			_ string,
			_ map[string]any,
			_ workflows.AgentUsageEventObserver,
		) (*workflows.RunResult, error) {
			if ref == workflows.RepositoryModelEvaluationPreflightWorkflowRef {
				return repositoryModelEvaluationPreflightResult(), nil
			}
			close(batchEntered)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		handler.repositoryModelEvaluationController = controller
		t.Cleanup(handler.Shutdown)
		created := createRepositoryModelEvaluation(t, mux, "owner/repo")
		repositoryModelEvaluationMutation(
			t,
			mux,
			http.MethodPost,
			"/api/model-evaluations/"+created.ID+"/preflight",
			map[string]any{"expected_version": created.Version},
		)
		ready := waitRepositoryModelEvaluationStatus(t, handler, created.ID, repoeval.StatusReady)
		start := repositoryModelEvaluationMutation(
			t,
			mux,
			http.MethodPost,
			"/api/model-evaluations/"+created.ID+"/start",
			map[string]any{"expected_version": ready.Version},
		)
		if start.Code != http.StatusAccepted {
			t.Fatalf("start ready status=%d body=%s", start.Code, start.Body.String())
		}
		<-batchEntered
		active, _, _ := handler.getRepositoryModelEvaluation(t.Context(), created.ID)
		cancel := repositoryModelEvaluationMutation(
			t,
			mux,
			http.MethodPost,
			"/api/model-evaluations/"+created.ID+"/cancel",
			map[string]any{"expected_version": active.Version},
		)
		if cancel.Code != http.StatusAccepted {
			t.Fatalf("cancel status=%d body=%s", cancel.Code, cancel.Body.String())
		}
		waitRepositoryModelEvaluationStatus(t, handler, created.ID, repoeval.StatusCanceled)
	})
}

func TestRepositoryModelEvaluationResumeSkipsDurableJudgedBatch(t *testing.T) {
	handler, mux, _ := newRepositoryModelEvaluationTestHandler(t)
	controller := newRepositoryModelEvaluationController(handler)
	batchRuns, analysisRuns := 0, 0
	analysisFails := true
	controller.runWorkflow = func(
		_ context.Context,
		_ string,
		ref string,
		_ string,
		_ map[string]any,
		_ workflows.AgentUsageEventObserver,
	) (*workflows.RunResult, error) {
		switch ref {
		case workflows.RepositoryModelEvaluationPreflightWorkflowRef:
			return repositoryModelEvaluationPreflightResult(), nil
		case workflows.RepositoryModelEvaluationBatchWorkflowRef:
			batchRuns++
			return repositoryModelEvaluationBatchResult(), nil
		case workflows.RepositoryModelEvaluationAnalysisWorkflowRef:
			analysisRuns++
			if analysisFails {
				return &workflows.RunResult{Status: workflows.RunStatusFailed, Error: "analysis interrupted"}, nil
			}
			return repositoryModelEvaluationAnalysisResult(), nil
		default:
			return nil, errors.New("unexpected workflow")
		}
	}
	handler.repositoryModelEvaluationController = controller
	t.Cleanup(handler.Shutdown)
	created := createRepositoryModelEvaluation(t, mux, "owner/repo")
	repositoryModelEvaluationMutation(
		t,
		mux,
		http.MethodPost,
		"/api/model-evaluations/"+created.ID+"/preflight",
		map[string]any{"expected_version": created.Version},
	)
	ready := waitRepositoryModelEvaluationStatus(t, handler, created.ID, repoeval.StatusReady)
	repositoryModelEvaluationMutation(
		t,
		mux,
		http.MethodPost,
		"/api/model-evaluations/"+created.ID+"/start",
		map[string]any{"expected_version": ready.Version},
	)
	failed := waitRepositoryModelEvaluationStatus(t, handler, created.ID, repoeval.StatusFailed)
	if len(failed.Checkpoint.Batches) != 1 || batchRuns != 1 || analysisRuns != 1 {
		t.Fatalf("first attempt batches=%d analyses=%d checkpoint=%#v", batchRuns, analysisRuns, failed.Checkpoint)
	}
	analysisFails = false
	resume := repositoryModelEvaluationMutation(
		t,
		mux,
		http.MethodPost,
		"/api/model-evaluations/"+created.ID+"/resume",
		map[string]any{"expected_version": failed.Version},
	)
	if resume.Code != http.StatusAccepted {
		t.Fatalf("resume status=%d body=%s", resume.Code, resume.Body.String())
	}
	waitRepositoryModelEvaluationStatus(t, handler, created.ID, repoeval.StatusCompleted)
	if batchRuns != 1 || analysisRuns != 2 {
		t.Fatalf("resume reran durable work: batches=%d analyses=%d", batchRuns, analysisRuns)
	}
}

func TestRepositoryModelEvaluationControllerLeaseStartupAndCancelRecoveryBranches(t *testing.T) {
	handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
	store, _, err := handler.repositoryModelEvaluationStore()
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(t.Context(), repositoryModelEvaluationCreateRequest("owner/repo"))
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := store.Update(
		t.Context(),
		created.ID,
		created.Version,
		func(candidate *repoeval.Evaluation) error {
			candidate.Status = repoeval.StatusPreflighting
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Update(t.Context(), created.ID, preflight.Version, func(candidate *repoeval.Evaluation) error {
		candidate.Status = repoeval.StatusCanceling
		candidate.Progress.Stage = repoeval.ProgressCanceling
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	controller := newRepositoryModelEvaluationController(handler)
	handler.repositoryModelEvaluationController = controller
	t.Cleanup(handler.Shutdown)
	handler.StartRepositoryModelEvaluationController()
	waitRepositoryModelEvaluationStatus(t, handler, created.ID, repoeval.StatusCanceled)
	if err := controller.Start(); err != nil {
		t.Fatalf("idempotent Start error=%v", err)
	}

	secondHandler := NewHandler(handler.configPath)
	second := newRepositoryModelEvaluationController(secondHandler)
	if err := second.Start(); !errors.Is(err, repoeval.ErrControllerLocked) {
		t.Fatalf("second controller Start error=%v", err)
	}
	second.Stop()

	stopped := newRepositoryModelEvaluationController(handler)
	stopped.Stop()
	if err := stopped.Start(); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped controller Start error=%v", err)
	}
	if _, _, _, err := stopped.reserveActive("id"); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped reserve error=%v", err)
	}
}

func TestRepositoryModelEvaluationControllerHelpersAndInvalidTransitions(t *testing.T) {
	var nilHandler *Handler
	if _, err := nilHandler.ensureRepositoryModelEvaluationController(); err == nil {
		t.Fatal("nil handler created an evaluation controller")
	}
	nilHandler.StartRepositoryModelEvaluationController()
	nilHandler.stopRepositoryModelEvaluationController()
	handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
	controller := newRepositoryModelEvaluationController(handler)
	controller.runWorkflow = func(
		ctx context.Context,
		_ string,
		_ string,
		_ string,
		_ map[string]any,
		_ workflows.AgentUsageEventObserver,
	) (*workflows.RunResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	handler.repositoryModelEvaluationController = controller
	t.Cleanup(handler.Shutdown)
	store, _, _ := handler.repositoryModelEvaluationStore()
	created, err := store.Create(t.Context(), repositoryModelEvaluationCreateRequest("owner/repo"))
	if err != nil {
		t.Fatal(err)
	}
	if _, startErr := controller.StartEvaluation(
		t.Context(),
		created.ID,
		created.Version,
	); !errors.Is(
		startErr,
		repoeval.ErrInvalidTransition,
	) {
		t.Fatalf("start draft error=%v", startErr)
	}
	if _, cancelErr := controller.Cancel(t.Context(), created.ID, created.Version+1); !errors.Is(
		cancelErr,
		repoeval.ErrConflict,
	) {
		t.Fatalf("stale cancel error=%v", cancelErr)
	}
	canceled, err := controller.Cancel(t.Context(), created.ID, created.Version)
	if err != nil || canceled.Status != repoeval.StatusCanceled {
		t.Fatalf("cancel=%#v err=%v", canceled, err)
	}
	_, err = controller.Resume(
		t.Context(),
		created.ID,
		canceled.Version,
	)
	if !errors.Is(err, repoeval.ErrInvalidTransition) {
		t.Fatalf("resume canceled error=%v", err)
	}

	if got := repositoryModelEvaluationRunError(errors.New("boom"), nil); got != "boom" {
		t.Fatalf("run error=%q", got)
	}
	if got := repositoryModelEvaluationRunError(
		nil,
		&workflows.RunResult{Error: "result failed"},
	); got != "result failed" {
		t.Fatalf("result error=%q", got)
	}
	if got := repositoryModelEvaluationRunError(nil, nil); !strings.Contains(got, "workflow failed") {
		t.Fatalf("fallback error=%q", got)
	}
	if len(boundedRepositoryModelEvaluationDetail(strings.Repeat("ø", 40<<10))) > 64<<10 {
		t.Fatal("bounded detail exceeded limit")
	}
	if value, err := compactJSON(` { "a": 1 } `); err != nil || value != `{"a":1}` {
		t.Fatalf("compact=%q err=%v", value, err)
	}
	if _, err := compactJSON("not json"); err == nil {
		t.Fatal("invalid compact JSON accepted")
	}
	hugeJSON, _ := json.Marshal(strings.Repeat("x", 256<<10))
	if _, err := compactJSON(string(hugeJSON)); err == nil {
		t.Fatal("oversized compact JSON accepted")
	}
	if _, err := compactJSON(make(chan int)); err == nil {
		t.Fatal("unmarshalable structured evidence accepted")
	}
	if _, err := compactJSON(make([]byte, 256<<10)); err == nil {
		t.Fatal("oversized non-string structured evidence accepted")
	}
	if _, err := stableJSONHash(make(chan int)); err == nil {
		t.Fatal("unhashable policy accepted")
	}
	var decoded []int
	if err := decodeAny(`[1,2]`, &decoded); err != nil || !slices.Equal(decoded, []int{1, 2}) {
		t.Fatalf("decoded=%v err=%v", decoded, err)
	}
	if got := anyMap(map[string]any{"ok": true}); !boolValue(got["ok"]) {
		t.Fatalf("anyMap=%v", got)
	}
	if got := anyMap(make(chan int)); len(got) != 0 {
		t.Fatalf("unmappable=%v", got)
	}
	if got := anyMap("null"); got == nil || len(got) != 0 {
		t.Fatalf("null map=%v", got)
	}
	if anyString(nil) != "" || anyString(json.Number("2")) != "2" || anyString(true) != "true" || intValue(3) != 3 ||
		intValue(int64(3)) != 3 || intValue(float64(3)) != 3 || intValue(json.Number("4")) != 4 {
		t.Fatal("coercion helper mismatch")
	}
	if intValue("bad") != 0 || boolValue("true") {
		t.Fatal("invalid coercion accepted")
	}
	if len(intMap("bad")) != 0 || stringSlice("bad") != nil {
		t.Fatal("invalid collection coercion accepted")
	}
	if boundedRepositoryModelEvaluationDetail(" ") != "The repository model evaluation failed." {
		t.Fatal("empty failure detail did not use safe fallback")
	}
	if sanitized := sanitizeRepositoryModelEvaluationRuntimeText(
		"checkout /private/repository failed",
		"/private/repository",
	); strings.Contains(sanitized, "/private/repository") || sanitizeRepositoryModelEvaluationRuntimeText("", "/tmp") != "" {
		t.Fatalf("runtime path sanitization=%q", sanitized)
	}
	if sanitized := sanitizeRepositoryModelEvaluationRuntimeText(
		"git clone -- /home/operator/missing/repo /tmp/workspace failed",
	); strings.Contains(sanitized, "/home/operator") || strings.Contains(sanitized, "/tmp/workspace") {
		t.Fatalf("inferred runtime path sanitization=%q", sanitized)
	}
	for _, input := range []string{
		"/opt/private failed",
		"failure [/home/operator/private]",
		"failure `/tmp/private`",
		"failure path=/var/lib/private",
	} {
		if sanitized := sanitizeRepositoryModelEvaluationRuntimeText(input); strings.Contains(sanitized, "/private") {
			t.Fatalf("bounded runtime path sanitization=%q", sanitized)
		}
	}
	if sanitized := sanitizeRepositoryModelEvaluationRuntimeText(
		"fetch https://github.com/owner/repository.git failed",
	); !strings.Contains(sanitized, "https://github.com/owner/repository.git") {
		t.Fatalf("remote URL was redacted=%q", sanitized)
	}
	clockController := &repositoryModelEvaluationController{}
	if clockController.clock().IsZero() {
		t.Fatal("default controller clock is zero")
	}
	if repositoryModelEvaluationBatchTaskCount(12, 2) != 25 || repositoryModelEvaluationBatchTaskCount(0, 2) != 0 {
		t.Fatal("managed task count mismatch")
	}
	models := []string{"a", "b", "c"}
	for _, test := range []struct {
		index int
		want  []string
	}{
		{index: 0, want: []string{"a", "b", "c"}},
		{index: 1, want: []string{"b", "c", "a"}},
		{index: 4, want: []string{"b", "c", "a"}},
		{index: -1, want: []string{"c", "a", "b"}},
	} {
		rotated := rotateRepositoryModelEvaluationCandidates(models, test.index)
		if !slices.Equal(rotated, test.want) {
			t.Fatalf("rotation %d = %v, want %v", test.index, rotated, test.want)
		}
		rotated[0] = "changed"
		if models[0] != "a" {
			t.Fatal("rotation aliased caller models")
		}
	}
	if got := rotateRepositoryModelEvaluationCandidates([]string{"only"}, 99); !slices.Equal(
		got,
		[]string{"only"},
	) {
		t.Fatalf("single model rotation=%v", got)
	}
	values := uniqueBoundedStrings([]string{" a ", "a", "", strings.Repeat("x", 9)}, 2, 8)
	if len(values) != 1 || values[0] != "a" {
		t.Fatalf("bounded strings=%v", values)
	}
	if values := uniqueBoundedStrings([]string{"a", "b", "c"}, 2, 8); !slices.Equal(values, []string{"a", "b"}) {
		t.Fatalf("count-bounded strings=%v", values)
	}
	runtimeController := newRepositoryModelEvaluationController(handler)
	if _, err := runtimeController.runWorkflowRuntime(
		t.Context(),
		"not a workflow",
		"workflows/invalid.yml",
		workflows.NewRunID(),
		nil,
		nil,
	); err == nil {
		t.Fatal("invalid workflow unexpectedly ran")
	}
	badRuntimeConfig := filepath.Join(t.TempDir(), "invalid.json")
	if err := os.WriteFile(badRuntimeConfig, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	badRuntime := newRepositoryModelEvaluationController(NewHandler(badRuntimeConfig))
	if _, err := badRuntime.runWorkflowRuntime(
		t.Context(),
		"not a workflow",
		"workflows/invalid.yml",
		workflows.NewRunID(),
		nil,
		nil,
	); err == nil {
		t.Fatal("missing runtime config unexpectedly succeeded")
	}
	usageFailure := errors.New("usage observer failed")
	usageCalls := 0
	observer := repositoryModelEvaluationRuntimeUsageObserver(
		"wr_target",
		func(workflows.AgentUsageEvent) error {
			usageCalls++
			return usageFailure
		},
	)
	if err := observer(workflows.AgentUsageEvent{RunID: "wr_other"}); err != nil || usageCalls != 0 {
		t.Fatalf("foreign usage observation err=%v calls=%d", err, usageCalls)
	}
	if err := observer(
		workflows.AgentUsageEvent{RunID: "wr_target"},
	); !errors.Is(err, usageFailure) ||
		usageCalls != 1 {
		t.Fatalf("target usage observation err=%v calls=%d", err, usageCalls)
	}
	if err := repositoryModelEvaluationRuntimeUsageObserver("wr_target", nil)(
		workflows.AgentUsageEvent{RunID: "wr_target"},
	); err != nil {
		t.Fatalf("nil usage observer error=%v", err)
	}
	if err := controller.recordUsage("missing", "wrong-token", workflows.AgentUsage{}, false); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("inactive usage error=%v", err)
	}
	controller.cancel()
	controller.handleExecutionCancellation("missing")
	controller.fail("missing", "wrong-token", "ignored")
	if err := repositoryModelEvaluationApplyFailure(nil, "failed"); !errors.Is(
		err,
		repoeval.ErrInvalidEvaluation,
	) {
		t.Fatalf("nil failure target error=%v", err)
	}
	failureTarget := &repoeval.Evaluation{Repository: "/private/repo"}
	if err := repositoryModelEvaluationApplyFailure(failureTarget, "", "/private/repo"); err != nil ||
		failureTarget.Status != repoeval.StatusFailed || failureTarget.Failure == "" {
		t.Fatalf("failure target=%#v err=%v", failureTarget, err)
	}
}

func TestRepositoryModelEvaluationOneShotReadyRecoveryAndTimeoutBudget(t *testing.T) {
	t.Run("legacy ready recovery", func(t *testing.T) {
		handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
		store, _, err := handler.repositoryModelEvaluationStore()
		if err != nil {
			t.Fatal(err)
		}
		seed := newRepositoryModelEvaluationController(handler)
		ready := seedReadyRepositoryModelEvaluation(t, seed, store, "owner/ready-recovery")
		controller := newRepositoryModelEvaluationController(handler)
		controller.runWorkflow = successfulRepositoryModelEvaluationWorkflow
		handler.repositoryModelEvaluationController = controller
		t.Cleanup(handler.Shutdown)
		if err := controller.Start(); err != nil {
			t.Fatal(err)
		}
		completed := waitRepositoryModelEvaluationStatus(t, handler, ready.ID, repoeval.StatusCompleted)
		if !completed.OneShot || len(completed.Comparisons) != len(completed.CandidateModels) {
			t.Fatalf("completed recovery=%#v", completed)
		}
	})

	t.Run("ready alias disappearance fails durably", func(t *testing.T) {
		handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
		store, _, err := handler.repositoryModelEvaluationStore()
		if err != nil {
			t.Fatal(err)
		}
		request := repositoryModelEvaluationCreateRequest("owner/ready-missing-alias")
		request.CandidateModels = []string{"removed-model", "model-b"}
		seed := newRepositoryModelEvaluationController(handler)
		ready := seedReadyRepositoryModelEvaluationFromRequest(t, seed, store, request)
		controller := newRepositoryModelEvaluationController(handler)
		controller.runWorkflow = successfulRepositoryModelEvaluationWorkflow
		handler.repositoryModelEvaluationController = controller
		t.Cleanup(handler.Shutdown)
		if err := controller.Start(); err != nil {
			t.Fatal(err)
		}
		failed := waitRepositoryModelEvaluationStatus(t, handler, ready.ID, repoeval.StatusFailed)
		if !strings.Contains(failed.Failure, "removed-model") {
			t.Fatalf("ready recovery failure=%#v", failed)
		}
	})

	t.Run("incident batch topology", func(t *testing.T) {
		selected := make([]map[string]any, 12)
		encoded, err := json.Marshal(selected)
		if err != nil {
			t.Fatal(err)
		}
		budget := repositoryModelEvaluationEffectiveWorkflowTimeout(
			5*time.Minute,
			workflows.RepositoryModelEvaluationBatchWorkflowRef,
			map[string]any{
				"selected_candidates": string(encoded),
				"candidate_models":    "model-a,model-b,model-c",
			},
		)
		if budget != 79*time.Minute {
			t.Fatalf("incident topology timeout=%s want=79m", budget)
		}
		if preserved := repositoryModelEvaluationEffectiveWorkflowTimeout(
			6*time.Hour,
			workflows.RepositoryModelEvaluationBatchWorkflowRef,
			map[string]any{},
		); preserved != 6*time.Hour {
			t.Fatalf("configured timeout=%s", preserved)
		}
		if preflight := repositoryModelEvaluationEffectiveWorkflowTimeout(
			5*time.Minute,
			workflows.RepositoryModelEvaluationPreflightWorkflowRef,
			nil,
		); preflight < 15*time.Minute {
			t.Fatalf("preflight timeout=%s", preflight)
		}
		huge := make([]map[string]any, 256)
		hugeJSON, err := json.Marshal(huge)
		if err != nil {
			t.Fatal(err)
		}
		if capped := repositoryModelEvaluationEffectiveWorkflowTimeout(
			5*time.Minute,
			workflows.RepositoryModelEvaluationBatchWorkflowRef,
			map[string]any{
				"selected_candidates": string(hugeJSON),
				"candidate_models":    strings.Repeat("model,", 32),
			},
		); capped != repositoryModelEvaluationMaxTimeout {
			t.Fatalf("capped timeout=%s", capped)
		}
	})
}

func TestRepositoryModelEvaluationResidualOneShotBranches(t *testing.T) {
	handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
	controller := newRepositoryModelEvaluationController(handler)
	handler.repositoryModelEvaluationController = controller
	t.Cleanup(handler.Shutdown)
	if err := controller.Start(); err != nil {
		t.Fatal(err)
	}
	store := controller.store.(repoeval.Store)
	failedPreflight := func(repository string) repoeval.Evaluation {
		draft, err := store.Create(t.Context(), repositoryModelEvaluationCreateRequest(repository))
		if err != nil {
			t.Fatal(err)
		}
		preflighting, err := store.Update(
			t.Context(),
			draft.ID,
			draft.Version,
			func(value *repoeval.Evaluation) error {
				value.Status = repoeval.StatusPreflighting
				value.Progress.Stage = repoeval.ProgressResolving
				return nil
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		failed, err := store.Update(
			t.Context(),
			preflighting.ID,
			preflighting.Version,
			func(value *repoeval.Evaluation) error {
				value.Status = repoeval.StatusFailed
				value.Progress.Stage = repoeval.ProgressFailed
				value.Failure = "preflight failed"
				return nil
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		return failed
	}

	busyFailed := failedPreflight("owner/busy-failed")
	busyToken, _, busyCancel, err := controller.reserveActive(busyFailed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, restartErr := controller.Resume(
		t.Context(),
		busyFailed.ID,
		busyFailed.Version,
	); !errors.Is(restartErr, errRepositoryModelEvaluationBusy) {
		t.Fatalf("busy failed restart error=%v", restartErr)
	}
	busyCancel()
	controller.releaseActive(busyFailed.ID, busyToken)

	missingPath := filepath.Join(t.TempDir(), "missing")
	missingFailed := failedPreflight(missingPath)
	if _, restartErr := controller.Restart(
		t.Context(),
		missingFailed.ID,
		missingFailed.Version,
	); !errors.Is(restartErr, repoeval.ErrInvalidEvaluation) {
		t.Fatalf("missing repository start-over error=%v", restartErr)
	}

	invalidDraft, err := store.Create(
		t.Context(),
		repositoryModelEvaluationCreateRequest(missingPath),
	)
	if err != nil {
		t.Fatal(err)
	}
	invalidPreflight, err := store.Update(
		t.Context(),
		invalidDraft.ID,
		invalidDraft.Version,
		func(value *repoeval.Evaluation) error {
			value.OneShot = true
			value.Status = repoeval.StatusPreflighting
			value.Progress.Stage = repoeval.ProgressResolving
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	invalidToken, invalidCtx, _, err := controller.reserveActive(invalidPreflight.ID)
	if err != nil {
		t.Fatal(err)
	}
	controller.wg.Add(1)
	controller.executePreflight(invalidCtx, invalidPreflight.ID, invalidToken, "wr_invalid_repository")
	invalidFailed, found, err := store.Get(t.Context(), invalidPreflight.ID)
	if err != nil || !found || invalidFailed.Status != repoeval.StatusFailed {
		t.Fatalf("invalid repository preflight=%#v found=%v err=%v", invalidFailed, found, err)
	}

	usageDraft, err := store.Create(
		t.Context(),
		repositoryModelEvaluationCreateRequest("owner/fenced-usage"),
	)
	if err != nil {
		t.Fatal(err)
	}
	usageToken, _, usageCancel, err := controller.reserveActive(usageDraft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if usageErr := controller.recordUsage(
		usageDraft.ID,
		usageToken,
		workflows.AgentUsage{},
		false,
	); !errors.Is(usageErr, context.Canceled) {
		t.Fatalf("fenced usage error=%v", usageErr)
	}
	usageCancel()
	controller.releaseActive(usageDraft.ID, usageToken)
}

func repositoryModelEvaluationCreateRequest(repository string) repoeval.CreateRequest {
	return repoeval.CreateRequest{
		Repository:         repository,
		Ref:                "main",
		CandidateModels:    []string{"model-a", "model-b"},
		SelectorModelAlias: "selector",
		JudgeModelAlias:    "judge",
		Focus: repoeval.Focus{
			CodeTypes: []repoeval.CodeType{repoeval.CodeTypeCode, repoeval.CodeTypeTest},
		},
		DefaultFilesPerLanguage: 20,
		FilesPerLanguage:        map[string]int{"go": 20},
	}
}
