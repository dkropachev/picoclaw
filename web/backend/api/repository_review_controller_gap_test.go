package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestRepositoryReviewCanonicalRuntimeProfileComparison(t *testing.T) {
	handler, _, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}
	store := repoaudit.NewSQLiteStore(workspace)
	resolved := workflows.RepositoryReviewModelProfile{
		Revision: "sha256:runtime-profile", AccountRef: "api",
		ReviewerModels: []string{"cheap"}, MaxContentBytes: 64 << 10,
	}
	automation := testRepositoryReviewAutomation()
	automation.Repository = "owner/runtime-profile"
	automation.CampaignID = repoaudit.NewRepositoryReviewCampaignID()
	automation.ScopePlan.Hash = strings.Repeat("a", 64)
	profileHash, err := repositoryReviewProfileHash(automation, automation.ScopePlan.Hash, resolved)
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("b", 40)
	if _, err = store.BeginCampaign(t.Context(), repoaudit.BeginCampaignRequest{
		Repository: automation.Repository, CampaignID: automation.CampaignID,
		CommitSHA: commit, Exact: true,
		DeduplicationSnapshot: &repoaudit.RepositoryReviewDeduplicationSnapshot{
			ReviewerModel: "cheap", DeduplicationModel: "cheap",
			SimilarityThreshold: repoaudit.DeduplicationDefaultThreshold,
			CandidateLimit:      repoaudit.DeduplicationDefaultCandidateLimit,
		},
	}); err != nil {
		t.Fatal(err)
	}
	catalog := make([]repoaudit.RepositoryReviewAssignment, 0, 4)
	for _, focus := range repoaudit.RepositoryReviewFocusIDs() {
		assignment, assignmentErr := repoaudit.NewRepositoryReviewAssignment(
			focus, "cheap", "prompt-v1", profileHash, true,
		)
		if assignmentErr != nil {
			t.Fatal(assignmentErr)
		}
		catalog = append(catalog, assignment)
	}
	file := repoaudit.FileRef{
		Path: "pkg/runtime.go", BlobSHA: strings.Repeat("c", 40),
		SizeBytes: 100, Category: "code", Mode: "100644",
	}
	if _, err = store.PlanAssignmentsForCampaign(
		t.Context(), automation.Repository, commit, "inventory-runtime", profileHash,
		automation.CampaignID, catalog, []repoaudit.FileRef{file}, false, 1, true,
	); err != nil {
		t.Fatal(err)
	}

	previousRunners := newWorkflowRuntimeRunners
	t.Cleanup(func() { newWorkflowRuntimeRunners = previousRunners })
	runner := &repositoryReviewCanonicalProfileRunner{profile: resolved}
	newWorkflowRuntimeRunners = func(string) workflowRuntimeRunners {
		return workflowRuntimeRunners{Agents: runner}
	}
	controller := handler.repositoryReviewControllerInstance()
	changed, err := controller.repositoryReviewRuntimeProfileChanged(t.Context(), store, cfg, automation)
	if err != nil || changed {
		t.Fatalf("matching runtime profile changed=%v err=%v", changed, err)
	}
	runner.profile.Revision = "sha256:changed-runtime-profile"
	changed, err = controller.repositoryReviewRuntimeProfileChanged(t.Context(), store, cfg, automation)
	if err != nil || !changed {
		t.Fatalf("changed runtime profile changed=%v err=%v", changed, err)
	}
	runner.err = os.ErrPermission
	if _, err = controller.repositoryReviewRuntimeProfileChanged(t.Context(), store, cfg, automation); err == nil {
		t.Fatal("runtime profile resolution error was ignored")
	}
	runner.err = nil
	runner.profile = resolved
	runner.profile.MaxContentBytes = 0
	if _, err = controller.repositoryReviewRuntimeProfileChanged(t.Context(), store, cfg, automation); err == nil {
		t.Fatal("invalid runtime profile hash was accepted")
	}

	blockedRoot := filepath.Join(t.TempDir(), "blocked-store")
	if err = os.WriteFile(blockedRoot, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = controller.repositoryReviewRuntimeProfileChanged(
		t.Context(), repoaudit.NewSQLiteStore(blockedRoot), cfg, automation,
	); err == nil {
		t.Fatal("runtime profile store error was ignored")
	}
}

func TestRepositoryReviewCanonicalCampaignOutcomeModelProjection(t *testing.T) {
	const campaignID = "rrc_canonical_outcome"
	state := repoaudit.RepositoryState{
		Repository: "owner/repo",
		RawFindings: []repoaudit.RawReviewFinding{
			{ID: "rrw_one", CampaignID: campaignID, ModelAlias: "model-a"},
			{ID: "rrw_two", CampaignID: campaignID, ModelAlias: "model-b"},
		},
		Findings: []repoaudit.Finding{
			{ID: "rdf_one", CampaignID: campaignID, RawSourceIDs: []string{"rrw_one"}},
			{ID: "rdf_two", CampaignID: campaignID, RawSourceIDs: []string{"rrw_two"}},
		},
		Contexts: []repoaudit.FindingContext{
			{
				ID: "context-a", CampaignID: campaignID, ModelAlias: "model-a",
				Files: []repoaudit.FileRef{{Path: "pkg/a.go"}, {Path: "pkg/b.go"}},
			},
			{ID: "context-b", CampaignID: campaignID, ModelAlias: "model-b"},
		},
	}
	automation := repoaudit.RepositoryReviewAutomation{
		CampaignID: campaignID, ReviewerModels: []string{"model-a", "model-b"},
	}
	outcome := loadRepositoryReviewCampaignOutcome(state, automation)
	if !outcome.found || outcome.rawFindings != 2 || outcome.deduplicatedFindings != 2 ||
		outcome.modelFindings["model-a"] != 1 || outcome.modelFindings["model-b"] != 1 ||
		len(outcome.modelPaths["model-a"]) != 2 {
		t.Fatalf("canonical outcome=%#v", outcome)
	}
	fromState := loadRepositoryReviewOutcomeFromResolvedState(state, automation)
	if !fromState.found {
		t.Fatal("resolved canonical state did not produce an outcome")
	}

	applyRepositoryReviewLiveMetrics(nil, state)
	applyRepositoryReviewCurrentFindingProgress(nil, state)
	if got := repositoryReviewCurrentFindings(repoaudit.RepositoryReviewAutomation{}, state); len(got) != 0 {
		t.Fatalf("campaignless findings=%#v", got)
	}
}

func TestRepositoryReviewCanonicalControllerResidualBranches(t *testing.T) {
	if got := normalizeRepositoryReviewWindow(""); got != "unknown" {
		t.Fatalf("blank accounting window=%q", got)
	}
	reset, ok := parseRepositoryReviewReset("2026-09-08 12:34:56 UTC")
	if !ok || reset.IsZero() {
		t.Fatalf("fallback accounting reset=%v ok=%v", reset, ok)
	}

	automation := repoaudit.RepositoryReviewAutomation{
		ModelCoverageSketches: map[string]string{"cheap": "not-base64"},
		ModelStats:            make(map[string]repoaudit.RepositoryReviewModelStats),
	}
	addRepositoryReviewModelPaths(&automation, "cheap", []string{"", "pkg/a.go"})
	encoded := automation.ModelCoverageSketches["cheap"]
	if encoded == "" || encoded == "not-base64" {
		t.Fatalf("coverage sketch was not rebuilt: %q", encoded)
	}
	automation.ModelCoverageSketches["cheap"] = encoded
	addRepositoryReviewModelPaths(&automation, "cheap", []string{"pkg/b.go"})
	if automation.ModelStats["cheap"].ReviewedFiles < 2 {
		t.Fatalf("coverage estimate=%#v", automation.ModelStats)
	}
	empty := repoaudit.RepositoryReviewAutomation{
		ModelStats: make(map[string]repoaudit.RepositoryReviewModelStats),
	}
	addRepositoryReviewModelPaths(&empty, "cheap", []string{"pkg/c.go"})
	if empty.ModelCoverageSketches["cheap"] == "" {
		t.Fatal("nil coverage sketch map was not initialized")
	}

	oversized := strings.Repeat("x", 4092) + "é" + strings.Repeat("x", 8)
	bounded := repositoryReviewBoundedDetail(oversized)
	if len(bounded) > 4096 || !strings.HasSuffix(bounded, "...") {
		t.Fatalf("bounded UTF-8 detail length=%d suffix=%q", len(bounded), bounded[len(bounded)-3:])
	}
	if !repositoryReviewFileProgressMade(
		repoaudit.RepositoryReviewProgress{}, repoaudit.RepositoryReviewProgress{}, nil,
		repositoryReviewOutcome{found: true, coverageExact: true, unsupportedFiles: 1}, false,
	) {
		t.Fatal("canonical exact outcome did not count durable unsupported progress")
	}

	cfg := &config.Config{ModelAliases: []config.ModelAliasConfig{{
		Name: "cheap", Model: "provider/concrete-model",
	}}}
	priceAutomation := repoaudit.RepositoryReviewAutomation{
		ReviewerModels: []string{"cheap"},
		ModelPrices: map[string]repoaudit.RepositoryReviewModelPrice{
			"cheap": {InputPricePer1M: 1, OutputPricePer1M: 2},
		},
	}
	index := repositoryReviewAccountingIndex(cfg, priceAutomation)
	if entry, found := index["concrete-model"]; !found || entry.alias != "cheap" {
		t.Fatalf("concrete accounting alias=%#v found=%v", entry, found)
	}

	ctx, cancel := context.WithCancel(t.Context())
	controller := &repositoryReviewController{
		ctx: ctx, cancel: cancel, monitorEvery: time.Millisecond,
	}
	controller.wg = sync.WaitGroup{}
	controller.wg.Add(1)
	go controller.monitor()
	time.Sleep(3 * time.Millisecond)
	cancel()
	controller.wg.Wait()
}
