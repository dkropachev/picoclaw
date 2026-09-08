package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestRepositoryReviewProfileDeduplicationRequestPreservesExplicitZero(t *testing.T) {
	request := repositoryReviewProfileConfigRequest{
		DeduplicationThreshold:  repositoryReviewOptionalInt{Present: true, Value: 0},
		DeduplicationCandidates: repositoryReviewOptionalInt{Present: true, Value: 0},
	}
	profile := repositoryReviewProfileFromRequest(request)
	if profile.DeduplicationSimilarityThreshold != 0 ||
		profile.DeduplicationCandidateLimit != 0 ||
		!profile.DeduplicationSettingsSpecified ||
		!validRepositoryReviewDeduplicationRequest(request) {
		t.Fatalf("explicit zero deduplication settings = %#v", profile)
	}
	defaults := repositoryReviewProfileFromRequest(repositoryReviewProfileConfigRequest{})
	if defaults.DeduplicationSimilarityThreshold != repoaudit.DeduplicationDefaultThreshold ||
		defaults.DeduplicationCandidateLimit != repoaudit.DeduplicationDefaultCandidateLimit {
		t.Fatalf("default deduplication settings = %#v", defaults)
	}
}

func TestRepositoryReviewDeduplicationModelCallIsIsolatedAndStrict(t *testing.T) {
	handler := newRepositoryReviewAIAdjudicationHandler(t, http.StatusOK, `{}`)
	snapshot := repoaudit.RepositoryReviewDeduplicationSnapshot{
		ReviewerModel: "cheap", DeduplicationModel: "cheap", AccountRef: "api",
		SimilarityThreshold: 90, CandidateLimit: 4,
	}
	request := repoaudit.DeduplicationScoringRequest{
		Finding: repoaudit.DeduplicationDiagnosis{Title: "raw"},
		Candidates: []repoaudit.DeduplicationScoringCandidate{{
			ID: "candidate-000001", Diagnosis: repoaudit.DeduplicationDiagnosis{Title: "candidate"},
		}},
	}
	original := runRepositoryDeduplicationAgent
	t.Cleanup(func() { runRepositoryDeduplicationAgent = original })
	var captured workflows.AgentRequest
	runRepositoryDeduplicationAgent = func(
		_ context.Context,
		_ *webWorkflowRuntimeRunner,
		agentRequest workflows.AgentRequest,
	) (map[string]any, error) {
		captured = agentRequest
		return map[string]any{
			"structured_valid": true,
			"structured": map[string]any{"scores": []any{map[string]any{
				"candidate_id": "candidate-000001", "score": 90,
				"explanation": "Same mechanism.",
			}}},
		}, nil
	}
	response, err := runRepositoryReviewDeduplicationScoring(
		t.Context(), handler, snapshot, repoaudit.DeduplicationScoringInstructions, request,
	)
	if err != nil || repoaudit.ValidateDeduplicationScoringResponse(response, request) != nil {
		t.Fatalf("scoring response=%#v err=%v", response, err)
	}
	if captured.Tools != workflows.AgentToolsNone || captured.History != "none" ||
		captured.Cache != "none" || !captured.EphemeralSession || !captured.PrivateContext ||
		captured.AccountRef != snapshot.AccountRef || captured.Model != snapshot.DeduplicationModel {
		t.Fatalf("deduplication request was not isolated: %#v", captured)
	}
	drifted := snapshot
	drifted.AccountModelRevision = "stale-revision"
	if _, err := runRepositoryReviewDeduplicationScoring(
		t.Context(), handler, drifted, repoaudit.DeduplicationScoringInstructions, request,
	); err == nil || !strings.Contains(err.Error(), "revision changed") {
		t.Fatalf("stale account/model revision error = %v", err)
	}
	runRepositoryDeduplicationAgent = func(
		context.Context, *webWorkflowRuntimeRunner, workflows.AgentRequest,
	) (map[string]any, error) {
		return map[string]any{
			"structured_valid": true,
			"structured":       map[string]any{"decision": "new", "unexpected": true},
		}, nil
	}
	if _, err := runRepositoryReviewDeduplicationJudgment(
		t.Context(), handler, snapshot, repoaudit.DeduplicationJudgeInstructions,
		repoaudit.DeduplicationJudgeRequest{},
	); err == nil {
		t.Fatal("deduplication judgment accepted an unknown field")
	}
}

func TestRepositoryReviewCanonicalDeduplicationControllerCoverage(t *testing.T) {
	handler, _, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewAPIStateWithProcessing(t, workspace, false)
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agents.Defaults.ContextWindow = 128
	controller := newRepositoryReviewController(handler)
	controller.leasedStore = repoaudit.NewSQLiteStore(workspace)
	controller.leasedConfig = cfg

	originalProcess := processRepositoryDeduplicationJobs
	t.Cleanup(func() { processRepositoryDeduplicationJobs = originalProcess })
	called := 0
	processRepositoryDeduplicationJobs = func(
		_ repoaudit.Store,
		_ context.Context,
		repository string,
		options repoaudit.DeduplicationProcessOptions,
	) (repoaudit.DeduplicationProcessResult, error) {
		called++
		if repository != state.Repository || options.ModelInputCeiling != 64 ||
			options.LeaseDuration != time.Hour || options.Score == nil || options.Judge == nil {
			t.Errorf("deduplication process repository=%q options=%#v", repository, options)
		}
		return repoaudit.DeduplicationProcessResult{}, errors.New("injected processor failure")
	}
	if err := controller.processRepositoryFindingDeduplications(t.Context()); err == nil || called != 1 {
		t.Fatalf("deduplication controller calls=%d err=%v", called, err)
	}
	if !repositoryStateHasPendingDeduplication(state) ||
		repositoryStateHasPendingDeduplication(repoaudit.RepositoryState{}) {
		t.Fatal("pending deduplication detection mismatch")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := controller.processRepositoryFindingDeduplications(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled deduplication controller err=%v", err)
	}
	if err := (*repositoryReviewController)(nil).processRepositoryFindingDeduplications(
		t.Context(),
	); err == nil {
		t.Fatal("nil deduplication controller was accepted")
	}
}
