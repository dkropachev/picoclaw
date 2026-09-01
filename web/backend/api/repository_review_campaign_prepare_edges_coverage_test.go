package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type repositoryReviewCancelingLoader struct {
	runs   map[string]*workflows.Run
	err    error
	cancel context.CancelFunc
}

func (loader repositoryReviewCancelingLoader) GetRun(
	_ context.Context,
	runID string,
) (*workflows.Run, error) {
	if loader.cancel != nil {
		loader.cancel()
	}
	if loader.err != nil {
		return nil, loader.err
	}
	return loader.runs[runID], nil
}

func repositoryReviewRecoveryProfileForEdges(
	fixture repositoryReviewBackfillFixture,
) workflows.RepositoryReviewModelProfile {
	return workflows.RepositoryReviewModelProfile{
		Revision: "legacy-automation-profile", AccountRef: fixture.automation.EffectiveAccountRef,
		ReviewerModels:  append([]string(nil), fixture.automation.ReviewerModels...),
		MaxContentBytes: int(fixture.automation.MaxContentBytes),
	}
}

func TestRepositoryReviewLegacyPrepareNonLedgerEdgesCoverage(t *testing.T) {
	base := newRepositoryReviewBackfillFixture(t, 1, repositoryReviewBackfillRunSpec{
		inspected: []int{0}, occurrences: 1,
	})
	profile := repositoryReviewRecoveryProfileForEdges(base)
	completed := base.automation.StartedAt.Add(time.Minute)
	failed := &workflows.Run{
		ID: "wr_extra", WorkflowRef: workflows.RepositoryBugFinderWorkflowRef,
		Status: workflows.RunStatusFailed, CreatedAt: completed.Add(-time.Second),
		CompletedAt: &completed,
	}

	for name, mutate := range map[string]func(
		*repoaudit.RepositoryReviewAutomation,
		*repoaudit.RepositoryState,
		*repositoryReviewBackfillLoader,
	){
		"missing retained run": func(
			automation *repoaudit.RepositoryReviewAutomation,
			_ *repoaudit.RepositoryState,
			loader *repositoryReviewBackfillLoader,
		) {
			automation.RunIDs = append(automation.RunIDs, "wr_missing")
			loader.err["wr_missing"] = os.ErrNotExist
		},
		"old uncommitted run": func(
			automation *repoaudit.RepositoryReviewAutomation,
			_ *repoaudit.RepositoryState,
			loader *repositoryReviewBackfillLoader,
		) {
			candidate := *failed
			candidate.CreatedAt = automation.StartedAt.Add(-time.Minute)
			automation.RunIDs = append(automation.RunIDs, candidate.ID)
			loader.runs[candidate.ID] = &candidate
		},
		"successful record missing ledger": func(
			automation *repoaudit.RepositoryReviewAutomation,
			_ *repoaudit.RepositoryState,
			loader *repositoryReviewBackfillLoader,
		) {
			candidate := cloneRepositoryReviewBackfillRunForCoverage(
				t, loader.runs[automation.RunIDs[0]],
			)
			candidate.ID = "wr_uncommitted_record"
			automation.RunIDs = append(automation.RunIDs, candidate.ID)
			loader.runs[candidate.ID] = candidate
		},
		"harmless pre-scope failure": func(
			automation *repoaudit.RepositoryReviewAutomation,
			_ *repoaudit.RepositoryState,
			loader *repositoryReviewBackfillLoader,
		) {
			automation.RunIDs = append(automation.RunIDs, failed.ID)
			loader.runs[failed.ID] = failed
		},
		"empty current ledger": func(
			automation *repoaudit.RepositoryReviewAutomation,
			state *repoaudit.RepositoryState,
			_ *repositoryReviewBackfillLoader,
		) {
			state.Runs = nil
			automation.RunIDs = []string{"wr_missing"}
		},
		"empty unrelated ledger identity": func(
			_ *repoaudit.RepositoryReviewAutomation,
			state *repoaudit.RepositoryState,
			_ *repositoryReviewBackfillLoader,
		) {
			state.Runs = append(state.Runs, repoaudit.ReviewRun{})
		},
	} {
		t.Run(name, func(t *testing.T) {
			automation := base.automation
			automation.RunIDs = append([]string(nil), base.automation.RunIDs...)
			state := cloneRepositoryReviewBackfillStateForCoverage(t, base.state)
			loader := repositoryReviewBackfillLoaderForCoverage(base)
			mutate(&automation, &state, &loader)
			prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
				t.Context(), automation, state, repoaudit.NewRepositoryReviewCampaignID(),
				loader, profile,
			)
			if err != nil {
				t.Fatalf("prepare error=%v", err)
			}
			if name == "harmless pre-scope failure" || name == "old uncommitted run" ||
				name == "empty unrelated ledger identity" {
				if !prepared.Available || !prepared.Exact {
					t.Fatalf("harmless failure prepare=%#v", prepared)
				}
				return
			}
			if prepared.Available || prepared.Exact {
				t.Fatalf("ambiguous history prepare=%#v", prepared)
			}
		})
	}
}

func TestRepositoryReviewLegacyPrepareCancellationEdgesCoverage(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(t, 1, repositoryReviewBackfillRunSpec{
		inspected: []int{0}, occurrences: 1,
	})
	profile := repositoryReviewRecoveryProfileForEdges(fixture)

	t.Run("ledger load canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		loader := repositoryReviewCancelingLoader{runs: fixture.runs, cancel: cancel}
		_, err := prepareRepositoryReviewLegacyCampaignBackfill(
			ctx, fixture.automation, fixture.state, repoaudit.NewRepositoryReviewCampaignID(),
			loader, profile,
		)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled ledger load error=%v", err)
		}
	})

	t.Run("retained deadline", func(t *testing.T) {
		loader := repositoryReviewBackfillLoaderForCoverage(fixture)
		loader.err[fixture.automation.RunIDs[0]] = context.DeadlineExceeded
		_, err := prepareRepositoryReviewLegacyCampaignBackfill(
			t.Context(), fixture.automation, fixture.state,
			repoaudit.NewRepositoryReviewCampaignID(), loader, profile,
		)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("deadline load error=%v", err)
		}
	})

	t.Run("nonledger cancel after load", func(t *testing.T) {
		completed := fixture.automation.StartedAt.Add(time.Minute)
		extra := &workflows.Run{
			ID: "wr_cancel_extra", WorkflowRef: workflows.RepositoryBugFinderWorkflowRef,
			Status: workflows.RunStatusFailed, CreatedAt: completed, CompletedAt: &completed,
		}
		automation := fixture.automation
		automation.RunIDs = append(append([]string(nil), automation.RunIDs...), extra.ID)
		runs := make(map[string]*workflows.Run, len(fixture.runs)+1)
		for id, run := range fixture.runs {
			runs[id] = run
		}
		runs[extra.ID] = extra
		ctx, cancel := context.WithCancel(t.Context())
		_, err := prepareRepositoryReviewLegacyCampaignBackfill(
			ctx, automation, fixture.state, repoaudit.NewRepositoryReviewCampaignID(),
			repositoryReviewCancelingLoader{runs: runs, cancel: cancel}, profile,
		)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("nonledger cancellation error=%v", err)
		}
	})
}

func TestRepositoryReviewLegacyPrepareLedgerCorruptionEdgesCoverage(t *testing.T) {
	for name, mutate := range map[string]func(
		*repositoryReviewBackfillFixture,
		*repositoryReviewBackfillLoader,
	){
		"nil workflow": func(
			fixture *repositoryReviewBackfillFixture,
			loader *repositoryReviewBackfillLoader,
		) {
			loader.runs[fixture.automation.RunIDs[0]] = nil
		},
		"failed workflow": func(
			fixture *repositoryReviewBackfillFixture,
			loader *repositoryReviewBackfillLoader,
		) {
			run := cloneRepositoryReviewBackfillRunForCoverage(
				t, loader.runs[fixture.automation.RunIDs[0]],
			)
			run.Status = workflows.RunStatusFailed
			loader.runs[run.ID] = run
		},
		"scope evidence missing": func(
			fixture *repositoryReviewBackfillFixture,
			loader *repositoryReviewBackfillLoader,
		) {
			run := cloneRepositoryReviewBackfillRunForCoverage(
				t, loader.runs[fixture.automation.RunIDs[0]],
			)
			step := run.Steps["find_bugs/full_scope_catalog"]
			delete(step.Outputs, "candidates")
			run.Steps["find_bugs/full_scope_catalog"] = step
			loader.runs[run.ID] = run
		},
		"catalog differs": func(
			fixture *repositoryReviewBackfillFixture,
			loader *repositoryReviewBackfillLoader,
		) {
			secondID := fixture.automation.RunIDs[1]
			run := cloneRepositoryReviewBackfillRunForCoverage(t, loader.runs[secondID])
			step := run.Steps["find_bugs/full_scope_catalog"]
			items := step.Outputs["candidates"].([]any)
			items[0].(map[string]any)["coverage_only"] = true
			step.Outputs["candidates"] = items
			run.Steps["find_bugs/full_scope_catalog"] = step
			loader.runs[secondID] = run
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newRepositoryReviewBackfillFixture(t, 2,
				repositoryReviewBackfillRunSpec{inspected: []int{0}},
				repositoryReviewBackfillRunSpec{inspected: []int{1}},
			)
			loader := repositoryReviewBackfillLoaderForCoverage(fixture)
			mutate(&fixture, &loader)
			prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
				t.Context(), fixture.automation, fixture.state,
				repoaudit.NewRepositoryReviewCampaignID(), loader,
				repositoryReviewRecoveryProfileForEdges(fixture),
			)
			if err != nil && name != "catalog differs" {
				t.Fatalf("prepare error=%v", err)
			}
			if prepared.Available || prepared.Exact {
				t.Fatalf("corrupt ledger prepare=%#v", prepared)
			}
		})
	}
}

func TestRepositoryReviewLegacyApplyConflictEdgesCoverage(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(t, 1, repositoryReviewBackfillRunSpec{
		inspected: []int{0}, occurrences: 1,
	})
	prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), fixture.automation, fixture.state,
		repoaudit.NewRepositoryReviewCampaignID(), fixture.runStore,
		repositoryReviewRecoveryProfileForEdges(fixture),
	)
	if err != nil || !prepared.Available {
		t.Fatalf("prepare=%#v err=%v", prepared, err)
	}
	_, installed, err := installRepositoryReviewLegacyCampaignAuthority(
		t.Context(), fixture.store, prepared,
	)
	if err != nil {
		t.Fatal(err)
	}

	wrongReviewVersion := installed
	wrongReviewVersion.Request.ExpectedReviewVersion++
	if _, applyErr := applyRepositoryReviewLegacyCampaignBackfill(
		t.Context(), fixture.store, wrongReviewVersion,
	); !errors.Is(applyErr, repoaudit.ErrConflict) {
		t.Fatalf("stale begin error=%v", applyErr)
	}

	missingMarker := installed
	automation, found, err := fixture.store.GetAutomation(t.Context(), installed.AutomationID)
	if err != nil || !found {
		t.Fatal(err)
	}
	automation, err = fixture.store.UpdateAutomation(
		t.Context(), automation.ID, automation.Version,
		func(value *repoaudit.RepositoryReviewAutomation) error {
			value.CampaignRecoveryPending = false
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	missingMarker.AutomationVersion = automation.Version
	if _, applyErr := applyRepositoryReviewLegacyCampaignBackfill(
		t.Context(), fixture.store, missingMarker,
	); !errors.Is(applyErr, repoaudit.ErrConflict) {
		t.Fatalf("missing marker apply error=%v", applyErr)
	}
}

func TestRepositoryReviewResetCampaignAuthorityCoverage(t *testing.T) {
	automation := testRepositoryReviewAutomation()
	automation.CampaignID = repoaudit.NewRepositoryReviewCampaignID()
	automation.CampaignRecoveryPending = true
	automation.ScopeSelection = &repoaudit.RepositoryReviewScopeSelection{
		IncludePrefixes: []string{"pkg"},
	}
	automation.ScopePlan = repoaudit.RepositoryReviewScopePlan{
		CommitSHA: strings.Repeat("a", 40), PolicyHash: strings.Repeat("b", 64),
		Hash: strings.Repeat("c", 64), Summary: "scope",
	}
	resetRepositoryReviewExecutionCampaign(&automation)
	if automation.CampaignID != "" || automation.CampaignRecoveryPending ||
		automation.ScopeSelection != nil || automation.ScopePlan.Hash != "" {
		t.Fatalf("campaign authority survived reset: %#v", automation)
	}
}

func TestRepositoryReviewLegacyStageAndClearHelpersCoverage(t *testing.T) {
	campaignID := repoaudit.NewRepositoryReviewCampaignID()
	prepared := repositoryReviewLegacyCampaignBackfill{
		AutomationStatus: repoaudit.RepositoryReviewAutomationPaused,
		Request: repoaudit.ReconcileCampaignRequest{
			Coverage: repoaudit.RepositoryReviewCampaignCoverage{
				ID: campaignID, CommitSHA: strings.Repeat("a", 40),
			},
		},
		ScopePlan: repoaudit.RepositoryReviewScopePlan{
			CommitSHA: strings.Repeat("a", 40), Hash: strings.Repeat("b", 64),
		},
	}
	selection := repoaudit.RepositoryReviewScopeSelection{IncludePrefixes: []string{"pkg"}}
	for name, candidate := range map[string]*repoaudit.RepositoryReviewAutomation{
		"nil":             nil,
		"campaign exists": {CampaignID: repoaudit.NewRepositoryReviewCampaignID(), Status: prepared.AutomationStatus},
		"wrong status":    {Status: repoaudit.RepositoryReviewAutomationFailed},
		"active":          {Status: prepared.AutomationStatus, ActiveRunID: "run"},
	} {
		t.Run("stage "+name, func(t *testing.T) {
			if err := stageRepositoryReviewLegacyCampaign(candidate, prepared, selection); !errors.Is(
				err, repoaudit.ErrConflict,
			) {
				t.Fatalf("stage error=%v", err)
			}
		})
	}
	valid := &repoaudit.RepositoryReviewAutomation{Status: prepared.AutomationStatus}
	if err := stageRepositoryReviewLegacyCampaign(valid, prepared, selection); err != nil ||
		valid.CampaignID != campaignID || !valid.CampaignRecoveryPending || valid.ScopeSelection == nil {
		t.Fatalf("valid stage=%#v err=%v", valid, err)
	}
	for name, candidate := range map[string]*repoaudit.RepositoryReviewAutomation{
		"nil":         nil,
		"wrong ID":    {CampaignID: repoaudit.NewRepositoryReviewCampaignID(), CampaignRecoveryPending: true},
		"not pending": {CampaignID: campaignID},
		"active":      {CampaignID: campaignID, CampaignRecoveryPending: true, ActiveRunID: "run"},
	} {
		t.Run("clear "+name, func(t *testing.T) {
			if err := clearRepositoryReviewLegacyCampaign(candidate, campaignID); !errors.Is(
				err, repoaudit.ErrConflict,
			) {
				t.Fatalf("clear error=%v", err)
			}
		})
	}
	if err := clearRepositoryReviewLegacyCampaign(valid, campaignID); err != nil ||
		valid.CampaignRecoveryPending {
		t.Fatalf("valid clear=%#v err=%v", valid, err)
	}
}

func TestRepositoryReviewLegacyProfileWrapperErrorCoverage(t *testing.T) {
	automation := testRepositoryReviewAutomation()
	automation.MaxContentBytes = 1024
	invalid := workflows.RepositoryReviewModelProfile{
		Revision: "graph", ReviewerModels: []string{"review"}, MaxContentBytes: 0,
	}
	if _, err := repositoryReviewLegacyProfileHash(automation, "scope", invalid); err == nil {
		t.Fatal("invalid current profile hash error=nil")
	}
	if _, err := repositoryReviewHistoricalProfileHash(automation, "scope", invalid); err == nil {
		t.Fatal("invalid historical profile hash error=nil")
	}
}

func TestRepositoryReviewLegacyRunEvidenceRemainingErrorsCoverage(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(t, 1, repositoryReviewBackfillRunSpec{
		inspected: []int{0}, occurrences: 1,
	})
	runID := fixture.automation.RunIDs[0]
	for name, mutate := range map[string]func(*workflows.Run, *repoaudit.ReviewRun){
		"reviewer denominator": func(run *workflows.Run, _ *repoaudit.ReviewRun) {
			step := run.Steps["find_bugs/plan"]
			models := make([]string, 33)
			for index := range models {
				models[index] = "reviewer-" + string(rune('a'+index))
			}
			step.Outputs["reviewerModels"] = models
			run.Steps["find_bugs/plan"] = step
		},
		"unsupported outside plan": func(run *workflows.Run, ledger *repoaudit.ReviewRun) {
			ledger.UnsupportedPaths = []string{"outside.bin"}
			step := run.Steps["find_bugs/record"]
			step.Outputs["run"] = *ledger
			run.Steps["find_bugs/record"] = step
		},
		"missing managed children": func(run *workflows.Run, _ *repoaudit.ReviewRun) {
			step := run.Steps["find_bugs/review"]
			delete(step.Outputs, "managed_children")
			run.Steps["find_bugs/review"] = step
		},
	} {
		t.Run(name, func(t *testing.T) {
			run := cloneRepositoryReviewBackfillRunForCoverage(t, fixture.runs[runID])
			ledger := fixture.state.Runs[0]
			mutate(run, &ledger)
			if _, _, valid := repositoryReviewLegacyRunEvidence(
				run, ledger, fixture.state.Repository,
			); valid {
				t.Fatal("invalid run evidence was accepted")
			}
		})
	}
}

func TestRepositoryReviewLegacyNonLedgerRemainingBranchesCoverage(t *testing.T) {
	completed := time.Now().UTC()
	withRecord := &workflows.Run{
		ID: "wr_record", WorkflowRef: workflows.RepositoryBugFinderWorkflowRef,
		Status: workflows.RunStatusSucceeded, CompletedAt: &completed,
		Steps: map[string]workflows.StepExecution{
			"find_bugs/record": {
				Status:  workflows.RunStatusSucceeded,
				Outputs: map[string]any{"run": map[string]any{"remaining_files": 0}},
			},
		},
	}
	if allowed, _ := repositoryReviewLegacyNonLedgerRunAllowed(withRecord); allowed {
		t.Fatal("recorded non-ledger run was accepted")
	}
	invalidPlan := &workflows.Run{
		ID: "wr_bad_plan", WorkflowRef: workflows.RepositoryBugFinderWorkflowRef,
		Status: workflows.RunStatusSucceeded, CompletedAt: &completed,
		Steps: map[string]workflows.StepExecution{
			"find_bugs/plan": {
				Status:  workflows.RunStatusSucceeded,
				Outputs: map[string]any{"plan": "bad", "pendingCount": 0},
			},
			"find_bugs/result": {
				Status:  workflows.RunStatusSucceeded,
				Outputs: map[string]any{"run": map[string]any{"remaining_files": 0}},
			},
		},
	}
	if allowed, _ := repositoryReviewLegacyNonLedgerRunAllowed(invalidPlan); allowed {
		t.Fatal("malformed no-op plan was accepted")
	}
}

func TestRepositoryReviewLegacyAdapterRemainingErrorsCoverage(t *testing.T) {
	newFixture := func(t *testing.T) repositoryReviewBackfillFixture {
		t.Helper()
		return newRepositoryReviewBackfillFixture(t, 1, repositoryReviewBackfillRunSpec{
			inspected: []int{0}, occurrences: 1,
		})
	}

	t.Run("unbound campaign conflict", func(t *testing.T) {
		fixture := newFixture(t)
		automation := fixture.automation
		automation.CampaignID = repoaudit.NewRepositoryReviewCampaignID()
		if _, err := (&repositoryReviewController{}).recoverLegacyRepositoryReviewCampaign(
			t.Context(), fixture.store, fixture.workspace, automation,
			automation.ResolvedCommitSHA, repositoryReviewRecoveryProfileForEdges(fixture),
		); !errors.Is(err, repoaudit.ErrConflict) {
			t.Fatalf("unbound campaign error=%v", err)
		}
	})

	t.Run("invalid resolved profile", func(t *testing.T) {
		fixture := newFixture(t)
		if _, err := (&repositoryReviewController{}).recoverLegacyRepositoryReviewCampaign(
			t.Context(), fixture.store, fixture.workspace, fixture.automation,
			fixture.automation.ResolvedCommitSHA, workflows.RepositoryReviewModelProfile{},
		); err == nil {
			t.Fatal("invalid profile recovery error=nil")
		}
	})

	t.Run("stale automation install", func(t *testing.T) {
		fixture := newFixture(t)
		automation := fixture.automation
		automation.Version++
		if _, err := (&repositoryReviewController{}).recoverLegacyRepositoryReviewCampaign(
			t.Context(), fixture.store, fixture.workspace, automation,
			automation.ResolvedCommitSHA, repositoryReviewRecoveryProfileForEdges(fixture),
		); !errors.Is(err, repoaudit.ErrConflict) {
			t.Fatalf("stale install error=%v", err)
		}
	})

	t.Run("corrupt repository state", func(t *testing.T) {
		fixture := newFixture(t)
		statePath := filepath.Join(
			fixture.workspace, "repository_reviews", "repository-reviews.db",
		)
		if err := os.WriteFile(statePath, []byte("not-sqlite"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := (&repositoryReviewController{}).recoverLegacyRepositoryReviewCampaign(
			t.Context(), fixture.store, fixture.workspace, fixture.automation,
			fixture.automation.ResolvedCommitSHA, repositoryReviewRecoveryProfileForEdges(fixture),
		); err == nil {
			t.Fatal("corrupt repository recovery error=nil")
		}
	})
}

func TestRepositoryReviewLegacyPersistenceFailureCoverage(t *testing.T) {
	prepare := func(t *testing.T) (repositoryReviewBackfillFixture, repositoryReviewLegacyCampaignBackfill) {
		t.Helper()
		fixture := newRepositoryReviewBackfillFixture(t, 1, repositoryReviewBackfillRunSpec{
			inspected: []int{0}, occurrences: 1,
		})
		prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
			t.Context(), fixture.automation, fixture.state,
			repoaudit.NewRepositoryReviewCampaignID(), fixture.runStore,
			repositoryReviewRecoveryProfileForEdges(fixture),
		)
		if err != nil || !prepared.Available {
			t.Fatalf("prepare=%#v err=%v", prepared, err)
		}
		return fixture, prepared
	}
	corruptDatabase := func(t *testing.T, workspace string) {
		t.Helper()
		if err := os.WriteFile(
			filepath.Join(workspace, "repository_reviews", "repository-reviews.db"),
			[]byte("not-sqlite"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("install save", func(t *testing.T) {
		fixture, prepared := prepare(t)
		corruptDatabase(t, fixture.workspace)
		if _, _, err := installRepositoryReviewLegacyCampaignAuthority(
			t.Context(), fixture.store, prepared,
		); err == nil {
			t.Fatal("read-only install error=nil")
		}
	})

	t.Run("adapter apply save", func(t *testing.T) {
		fixture, prepared := prepare(t)
		installed, _, err := installRepositoryReviewLegacyCampaignAuthority(
			t.Context(), fixture.store, prepared,
		)
		if err != nil {
			t.Fatal(err)
		}
		corruptDatabase(t, fixture.workspace)
		if _, err := (&repositoryReviewController{}).recoverLegacyRepositoryReviewCampaign(
			t.Context(), fixture.store, fixture.workspace, installed,
			fixture.automation.ResolvedCommitSHA, repositoryReviewRecoveryProfileForEdges(fixture),
		); err == nil {
			t.Fatal("read-only adapter error=nil")
		}
	})
}

func repositoryReviewNoopWorkflowForEdges(
	t *testing.T,
	fixture repositoryReviewBackfillFixture,
	runID string,
) *workflows.Run {
	t.Helper()
	var firstPlan repoaudit.Plan
	if !repositoryReviewDecodeValue(
		repositoryReviewRunStep(fixture.runs[fixture.automation.RunIDs[0]], "plan").Outputs["plan"],
		&firstPlan,
	) {
		t.Fatal("fixture plan did not decode")
	}
	noopPlan, err := fixture.store.PlanWithProfileLimitAuthoritative(
		t.Context(), firstPlan.Repository, firstPlan.CommitSHA, firstPlan.InventoryHash,
		firstPlan.ProfileHash, firstPlan.PendingFiles, false, 1, true,
	)
	if err != nil || len(noopPlan.PendingFiles) != 0 || len(noopPlan.DeferredFiles) != 0 {
		t.Fatalf("no-op plan=%#v err=%v", noopPlan, err)
	}
	completed := fixture.automation.StartedAt.Add(2 * time.Minute)
	return &workflows.Run{
		ID: runID, WorkflowRef: workflows.RepositoryBugFinderWorkflowRef,
		Status: workflows.RunStatusSucceeded, CreatedAt: completed.Add(-time.Second),
		CompletedAt: &completed,
		Steps: map[string]workflows.StepExecution{
			"find_bugs/plan": {
				Status:  workflows.RunStatusSucceeded,
				Outputs: map[string]any{"plan": noopPlan, "pendingCount": 0},
			},
			"find_bugs/result": {
				Status:  workflows.RunStatusSucceeded,
				Outputs: map[string]any{"run": map[string]any{"remaining_files": 0}},
			},
		},
	}
}

func TestRepositoryReviewLegacyPrepareNoopUnionEdgesCoverage(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(t, 1, repositoryReviewBackfillRunSpec{
		inspected: []int{0}, complete: true,
	})
	profile := repositoryReviewRecoveryProfileForEdges(fixture)
	noop := repositoryReviewNoopWorkflowForEdges(t, fixture, "wr_noop_contained")
	automation := fixture.automation
	automation.RunIDs = append(append([]string(nil), automation.RunIDs...), noop.ID)
	loader := repositoryReviewBackfillLoaderForCoverage(fixture)
	loader.runs[noop.ID] = noop
	prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), automation, fixture.state, repoaudit.NewRepositoryReviewCampaignID(),
		loader, profile,
	)
	if err != nil || !prepared.Available || !prepared.Exact {
		t.Fatalf("contained no-op prepare=%#v err=%v", prepared, err)
	}

	foreign := newRepositoryReviewBackfillFixture(t, 1, repositoryReviewBackfillRunSpec{
		inspected: []int{0}, complete: true,
	})
	foreignNoop := repositoryReviewNoopWorkflowForEdges(t, foreign, "wr_noop_foreign")
	step := foreignNoop.Steps["find_bugs/plan"]
	var foreignPlan repoaudit.Plan
	if !repositoryReviewDecodeValue(step.Outputs["plan"], &foreignPlan) {
		t.Fatal("foreign no-op plan did not decode")
	}
	// A separately digest-valid no-op that is not part of the recovered union
	// must make the history unavailable. Generate it under a distinct repository.
	otherStore := repoaudit.NewStore(t.TempDir())
	file := foreignPlan.UnchangedFiles[0]
	first, err := otherStore.PlanWithProfileLimitAuthoritative(
		t.Context(), "owner/other", foreignPlan.CommitSHA, foreignPlan.InventoryHash,
		foreignPlan.ProfileHash, []repoaudit.FileRef{file}, false, 1, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, recordErr := otherStore.Record(t.Context(), repoaudit.RecordRequest{
		Plan: first, RunID: "wr_seed", CompletedAt: time.Now().UTC(),
		CompletedFiles: first.PendingFiles,
	}); recordErr != nil {
		t.Fatal(recordErr)
	}
	otherNoop, err := otherStore.PlanWithProfileLimitAuthoritative(
		t.Context(), "owner/other", foreignPlan.CommitSHA, foreignPlan.InventoryHash,
		foreignPlan.ProfileHash, []repoaudit.FileRef{file}, false, 1, true,
	)
	if err != nil || len(otherNoop.PendingFiles) != 0 {
		t.Fatalf("foreign no-op=%#v err=%v", otherNoop, err)
	}
	step.Outputs["plan"] = otherNoop
	foreignNoop.Steps["find_bugs/plan"] = step
	automation.RunIDs[len(automation.RunIDs)-1] = foreignNoop.ID
	loader = repositoryReviewBackfillLoaderForCoverage(fixture)
	loader.runs[foreignNoop.ID] = foreignNoop
	prepared, err = prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), automation, fixture.state, repoaudit.NewRepositoryReviewCampaignID(),
		loader, profile,
	)
	if err != nil || prepared.Available || prepared.Exact {
		t.Fatalf("foreign no-op prepare=%#v err=%v", prepared, err)
	}

	// A digest-valid no-op under the same repository and source identity but a
	// different exact file must also fail the recovered-union containment check.
	otherFile := file
	otherFile.Path = "pkg/not-in-union.go"
	otherFile.BlobSHA = strings.Repeat("d", 40)
	sameStore := repoaudit.NewStore(t.TempDir())
	first, err = sameStore.PlanWithProfileLimitAuthoritative(
		t.Context(), fixture.state.Repository, foreignPlan.CommitSHA, foreignPlan.InventoryHash,
		foreignPlan.ProfileHash, []repoaudit.FileRef{otherFile}, false, 1, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, recordErr := sameStore.Record(t.Context(), repoaudit.RecordRequest{
		Plan: first, RunID: "wr_same_seed", CompletedAt: time.Now().UTC(),
		CompletedFiles: first.PendingFiles,
	}); recordErr != nil {
		t.Fatal(recordErr)
	}
	sameNoop, err := sameStore.PlanWithProfileLimitAuthoritative(
		t.Context(), fixture.state.Repository, foreignPlan.CommitSHA, foreignPlan.InventoryHash,
		foreignPlan.ProfileHash, []repoaudit.FileRef{otherFile}, false, 1, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	step.Outputs["plan"] = sameNoop
	foreignNoop.Steps["find_bugs/plan"] = step
	loader = repositoryReviewBackfillLoaderForCoverage(fixture)
	loader.runs[foreignNoop.ID] = foreignNoop
	prepared, err = prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), automation, fixture.state, repoaudit.NewRepositoryReviewCampaignID(),
		loader, profile,
	)
	if err != nil || prepared.Available || prepared.Exact {
		t.Fatalf("outside-union no-op prepare=%#v err=%v", prepared, err)
	}
}

func repositoryReviewAppendDriftRunForEdges(
	t *testing.T,
	fixture *repositoryReviewBackfillFixture,
	commit string,
	inventory string,
	file repoaudit.FileRef,
) repositoryReviewBackfillLoader {
	t.Helper()
	baseID := fixture.automation.RunIDs[0]
	baseWorkflow := cloneRepositoryReviewBackfillRunForCoverage(t, fixture.runs[baseID])
	var basePlan repoaudit.Plan
	if !repositoryReviewDecodeValue(
		repositoryReviewRunStep(baseWorkflow, "plan").Outputs["plan"], &basePlan,
	) {
		t.Fatal("base plan did not decode")
	}
	plan, err := fixture.store.PlanWithProfileLimitAuthoritative(
		t.Context(), basePlan.Repository, commit, inventory, basePlan.ProfileHash,
		[]repoaudit.FileRef{file}, true, 1, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	review := baseWorkflow.Steps["find_bugs/review"]
	children := review.Outputs["managed_children"].([]any)
	for _, rawChild := range children {
		child := rawChild.(map[string]any)
		for _, rawFile := range child["scope"].([]any) {
			item := rawFile.(map[string]any)
			item["fileHash"] = file.BlobSHA
			item["sizeBytes"] = file.SizeBytes
			item["category"] = file.Category
			item["mode"] = file.Mode
		}
	}
	review.Outputs["managed_children"] = children
	baseWorkflow.Steps["find_bugs/review"] = review
	evidence, err := workflows.DecodeRepositoryReviewManagedEvidence(
		children, plan, workflows.RepositoryReviewManagedEvidenceOptions{
			AllowLegacyCoreFindings: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	runID := "wr_drift_" + commit[:4] + "_" + file.BlobSHA[:4]
	completed := fixture.automation.StartedAt.Add(5 * time.Minute)
	recorded, err := fixture.store.Record(t.Context(), repoaudit.RecordRequest{
		Plan: plan, RunID: runID, CompletedAt: completed,
		Observations:     evidence.Observations,
		CompletedFiles:   evidence.CompletedFiles,
		UnsupportedFiles: evidence.UnsupportedFiles,
	})
	if err != nil {
		t.Fatal(err)
	}
	baseWorkflow.ID = runID
	baseWorkflow.CreatedAt = completed.Add(-time.Minute)
	baseWorkflow.UpdatedAt = completed
	baseWorkflow.CompletedAt = &completed
	planStep := baseWorkflow.Steps["find_bugs/plan"]
	planStep.Outputs["plan"] = plan
	baseWorkflow.Steps["find_bugs/plan"] = planStep
	recordStep := baseWorkflow.Steps["find_bugs/record"]
	recordStep.Outputs["run"] = recorded.Run
	baseWorkflow.Steps["find_bugs/record"] = recordStep
	fixture.runs[runID] = baseWorkflow
	fixture.automation.RunIDs = append(fixture.automation.RunIDs, runID)
	state, found, err := fixture.store.Get(basePlan.Repository)
	if err != nil || !found {
		t.Fatalf("drift state found=%v err=%v", found, err)
	}
	fixture.state = state
	return repositoryReviewBackfillLoaderForCoverage(*fixture)
}

func TestRepositoryReviewLegacyPreparePlanAndUnionDriftEdgesCoverage(t *testing.T) {
	for name, mutate := range map[string]func(*repositoryReviewBackfillFixture) repositoryReviewBackfillLoader{
		"commit": func(fixture *repositoryReviewBackfillFixture) repositoryReviewBackfillLoader {
			var plan repoaudit.Plan
			_ = repositoryReviewDecodeValue(
				repositoryReviewRunStep(fixture.runs[fixture.automation.RunIDs[0]], "plan").Outputs["plan"],
				&plan,
			)
			return repositoryReviewAppendDriftRunForEdges(
				t, fixture, strings.Repeat("b", 40), plan.InventoryHash, plan.PendingFiles[0],
			)
		},
		"inventory": func(fixture *repositoryReviewBackfillFixture) repositoryReviewBackfillLoader {
			var plan repoaudit.Plan
			_ = repositoryReviewDecodeValue(
				repositoryReviewRunStep(fixture.runs[fixture.automation.RunIDs[0]], "plan").Outputs["plan"],
				&plan,
			)
			return repositoryReviewAppendDriftRunForEdges(
				t, fixture, plan.CommitSHA, "inventory-other", plan.PendingFiles[0],
			)
		},
		"file identity": func(fixture *repositoryReviewBackfillFixture) repositoryReviewBackfillLoader {
			var plan repoaudit.Plan
			_ = repositoryReviewDecodeValue(
				repositoryReviewRunStep(fixture.runs[fixture.automation.RunIDs[0]], "plan").Outputs["plan"],
				&plan,
			)
			file := plan.PendingFiles[0]
			file.BlobSHA = strings.Repeat("c", 40)
			return repositoryReviewAppendDriftRunForEdges(
				t, fixture, plan.CommitSHA, plan.InventoryHash, file,
			)
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newRepositoryReviewBackfillFixture(t, 1, repositoryReviewBackfillRunSpec{
				inspected: []int{0}, occurrences: 1,
			})
			loader := mutate(&fixture)
			prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
				t.Context(), fixture.automation, fixture.state,
				repoaudit.NewRepositoryReviewCampaignID(), loader,
				repositoryReviewRecoveryProfileForEdges(fixture),
			)
			if err != nil || prepared.Available || prepared.Exact {
				t.Fatalf("%s drift prepare=%#v err=%v", name, prepared, err)
			}
		})
	}
}

func TestRepositoryReviewLegacyPrepareUnchangedProvenanceCoverage(t *testing.T) {
	seed := newRepositoryReviewBackfillFixture(t, 2, repositoryReviewBackfillRunSpec{
		inspected: []int{0},
	})
	seedWorkflow := seed.runs[seed.automation.RunIDs[0]]
	var seedPlan repoaudit.Plan
	if !repositoryReviewDecodeValue(
		repositoryReviewRunStep(seedWorkflow, "plan").Outputs["plan"], &seedPlan,
	) {
		t.Fatal("seed plan did not decode")
	}
	files, err := repositoryReviewLegacyPlanManifest(seedPlan)
	if err != nil || len(files) != 2 {
		t.Fatalf("seed files=%#v err=%v", files, err)
	}
	workspace := t.TempDir()
	store := repoaudit.NewStore(workspace)
	automation := seed.automation
	automation.RunIDs = nil
	automation.StartedAt = time.Now().UTC().Add(-time.Hour)
	automation.Version = 1
	profile := repositoryReviewRecoveryProfileForEdges(seed)
	profileHash, err := repositoryReviewHistoricalProfileHash(
		automation, automation.ScopePlan.Hash, profile,
	)
	if err != nil {
		t.Fatal(err)
	}

	buildRun := func(plan repoaudit.Plan, runID string, complete bool, completed time.Time) *workflows.Run {
		t.Helper()
		scope := repositoryReviewBackfillFileMaps(plan.PendingFiles)
		children := make([]map[string]any, 0, 4)
		for index := range 4 {
			child := map[string]any{
				"label": "coverage", "required": true, "scope": scope,
				"model": map[string]any{"selected": "review-a"},
			}
			if complete || index == 0 {
				child["valid"] = true
				child["structured"] = repositoryReviewBackfillStructured(
					t, plan.PendingFiles, nil,
				)
				child["text"] = "validated"
			} else {
				child["valid"] = false
				child["run_error"] = "failed"
			}
			children = append(children, child)
		}
		evidence, decodeErr := workflows.DecodeRepositoryReviewManagedEvidence(
			children, plan, workflows.RepositoryReviewManagedEvidenceOptions{
				AllowLegacyCoreFindings: true,
			},
		)
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		completedFiles := evidence.CompletedFiles
		if completedFiles == nil {
			completedFiles = []repoaudit.FileRef{}
		}
		recorded, recordErr := store.Record(t.Context(), repoaudit.RecordRequest{
			Plan: plan, RunID: runID, CompletedAt: completed,
			Observations:   evidence.Observations,
			CompletedFiles: completedFiles,
		})
		if recordErr != nil {
			t.Fatal(recordErr)
		}
		selectedPaths := make([]string, 0, len(files))
		for _, file := range files {
			selectedPaths = append(selectedPaths, file.Path)
		}
		return &workflows.Run{
			ID: runID, WorkflowRef: workflows.RepositoryBugFinderWorkflowRef,
			Status: workflows.RunStatusSucceeded, CreatedAt: completed.Add(-time.Minute),
			CompletedAt: &completed,
			Steps: map[string]workflows.StepExecution{
				"find_bugs/plan": {
					Status: workflows.RunStatusSucceeded,
					Outputs: map[string]any{
						"plan": plan, "accountRef": profile.AccountRef,
						"reviewerModels":         profile.ReviewerModels,
						"includeDefaultReviewer": profile.IncludeDefaultReviewer,
						"maxContentBytes":        profile.MaxContentBytes,
					},
				},
				"find_bugs/full_scope_catalog": seedWorkflow.Steps["find_bugs/full_scope_catalog"],
				"find_bugs/scope": {
					Status: workflows.RunStatusSucceeded,
					Outputs: map[string]any{
						"scopePlan": automation.ScopePlan, "selectedPaths": selectedPaths,
					},
				},
				"find_bugs/review": {
					Status:  workflows.RunStatusSucceeded,
					Outputs: map[string]any{"managed_children": children},
				},
				"find_bugs/record": {
					Status:  workflows.RunStatusSucceeded,
					Outputs: map[string]any{"run": recorded.Run},
				},
			},
		}
	}

	firstPlan, err := store.PlanWithProfileLimitAuthoritative(
		t.Context(), seedPlan.Repository, seedPlan.CommitSHA, seedPlan.InventoryHash,
		profileHash, files, false, 1, true,
	)
	if err != nil || len(firstPlan.PendingFiles) != 1 || len(firstPlan.DeferredFiles) != 1 {
		t.Fatalf("first partial plan=%#v err=%v", firstPlan, err)
	}
	firstCompleted := automation.StartedAt.Add(10 * time.Minute)
	firstRun := buildRun(firstPlan, "wr_partial_first", true, firstCompleted)
	secondPlan, err := store.PlanWithProfileLimitAuthoritative(
		t.Context(), seedPlan.Repository, seedPlan.CommitSHA, seedPlan.InventoryHash,
		profileHash, files, false, 1, true,
	)
	if err != nil || len(secondPlan.UnchangedFiles) != 1 || len(secondPlan.PendingFiles) != 1 {
		t.Fatalf("second partial plan=%#v err=%v", secondPlan, err)
	}
	secondRun := buildRun(
		secondPlan, "wr_partial_second", false, firstCompleted.Add(time.Minute),
	)
	automation.RunIDs = []string{firstRun.ID, secondRun.ID}
	state, found, err := store.Get(seedPlan.Repository)
	if err != nil || !found {
		t.Fatalf("partial state found=%v err=%v", found, err)
	}
	loader := repositoryReviewMoreRawLoader{
		runs: map[string]*workflows.Run{firstRun.ID: firstRun, secondRun.ID: secondRun},
		err:  map[string]error{},
	}
	prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), automation, state, repoaudit.NewRepositoryReviewCampaignID(),
		loader, profile,
	)
	if err != nil || !prepared.Available || prepared.CompletedFiles != 1 {
		t.Fatalf("unchanged recovery=%#v err=%v", prepared, err)
	}
	for pathValue := range state.Files {
		delete(state.Files, pathValue)
	}
	prepared, err = prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), automation, state, repoaudit.NewRepositoryReviewCampaignID(),
		loader, profile,
	)
	if err != nil || prepared.Available || prepared.Exact {
		t.Fatalf("missing unchanged provenance=%#v err=%v", prepared, err)
	}
}

func TestRepositoryReviewLegacyPrepareManagedUnsupportedCoverage(t *testing.T) {
	seed := newRepositoryReviewBackfillFixture(t, 2, repositoryReviewBackfillRunSpec{
		inspected: []int{0},
	})
	seedWorkflow := seed.runs[seed.automation.RunIDs[0]]
	var seedPlan repoaudit.Plan
	if !repositoryReviewDecodeValue(
		repositoryReviewRunStep(seedWorkflow, "plan").Outputs["plan"], &seedPlan,
	) {
		t.Fatal("seed plan did not decode")
	}
	files := append([]repoaudit.FileRef(nil), seedPlan.PendingFiles...)
	reviewable := files[0]
	unsupportedFile := files[1]
	profile := repositoryReviewRecoveryProfileForEdges(seed)
	profileHash, err := repositoryReviewHistoricalProfileHash(
		seed.automation, seed.automation.ScopePlan.Hash, profile,
	)
	if err != nil {
		t.Fatal(err)
	}
	store := repoaudit.NewStore(t.TempDir())
	plan, err := store.PlanWithProfileLimitAuthoritative(
		t.Context(), seedPlan.Repository, seedPlan.CommitSHA, seedPlan.InventoryHash,
		profileHash, files, false, 2, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	scope := repositoryReviewBackfillFileMaps(plan.PendingFiles)
	for _, item := range scope {
		if item["path"] == unsupportedFile.Path {
			item["contentComplete"] = false
			item["contentUnavailable"] = "binary"
		} else {
			item["contentComplete"] = true
		}
	}
	children := make([]map[string]any, 0, 4)
	for index := range 4 {
		child := map[string]any{
			"label": "unsupported", "required": true, "valid": false,
			"tasks": []string{workflows.RepositoryBugFinderFocuses()[index].Task},
			"scope": scope, "run_error": "unsupported",
			"model": map[string]any{"selected": "review-a"},
		}
		if index == 0 {
			child["valid"] = true
			delete(child, "run_error")
			child["structured"] = repositoryReviewBackfillStructured(
				t, []repoaudit.FileRef{reviewable}, nil,
			)
			child["text"] = "validated"
		}
		children = append(children, child)
	}
	evidence, err := workflows.DecodeRepositoryReviewManagedEvidence(
		children, plan, workflows.RepositoryReviewManagedEvidenceOptions{
			TerminalUnsupportedFiles: []repoaudit.FileRef{unsupportedFile}, RequiredAssignments: 4,
			AllowLegacyCoreFindings: true,
		},
	)
	if err != nil || len(evidence.UnsupportedFiles) != 1 {
		t.Fatalf("unsupported evidence=%#v err=%v", evidence, err)
	}
	completed := seed.automation.StartedAt.Add(10 * time.Minute)
	recorded, err := store.Record(t.Context(), repoaudit.RecordRequest{
		Plan: plan, RunID: "wr_managed_unsupported", CompletedAt: completed,
		Observations: evidence.Observations, CompletedFiles: []repoaudit.FileRef{},
		UnsupportedFiles: evidence.UnsupportedFiles,
	})
	if err != nil {
		t.Fatal(err)
	}
	selectedPaths := []string{reviewable.Path, unsupportedFile.Path}
	workflowRun := &workflows.Run{
		ID: recorded.Run.ID, WorkflowRef: workflows.RepositoryBugFinderWorkflowRef,
		Status: workflows.RunStatusSucceeded, CreatedAt: completed.Add(-time.Minute),
		CompletedAt: &completed,
		Steps: map[string]workflows.StepExecution{
			"find_bugs/plan": {
				Status: workflows.RunStatusSucceeded,
				Outputs: map[string]any{
					"plan": plan, "accountRef": profile.AccountRef,
					"reviewerModels":         profile.ReviewerModels,
					"includeDefaultReviewer": profile.IncludeDefaultReviewer,
					"maxContentBytes":        profile.MaxContentBytes,
				},
			},
			"find_bugs/full_scope_catalog": seedWorkflow.Steps["find_bugs/full_scope_catalog"],
			"find_bugs/scope": {
				Status: workflows.RunStatusSucceeded,
				Outputs: map[string]any{
					"scopePlan": seed.automation.ScopePlan, "selectedPaths": selectedPaths,
				},
			},
			"find_bugs/review": {
				Status:  workflows.RunStatusSucceeded,
				Outputs: map[string]any{"managed_children": children},
			},
			"find_bugs/record": {
				Status: workflows.RunStatusSucceeded, Outputs: map[string]any{"run": recorded.Run},
			},
		},
	}
	state, found, err := store.Get(seedPlan.Repository)
	if err != nil || !found {
		t.Fatalf("unsupported state found=%v err=%v", found, err)
	}
	automation := seed.automation
	automation.RunIDs = []string{workflowRun.ID}
	prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), automation, state, repoaudit.NewRepositoryReviewCampaignID(),
		repositoryReviewMoreRawLoader{
			runs: map[string]*workflows.Run{workflowRun.ID: workflowRun}, err: map[string]error{},
		},
		profile,
	)
	if err != nil || !prepared.Available || prepared.UnsupportedFiles != 1 ||
		prepared.InspectedFiles != 1 {
		t.Fatalf("managed unsupported recovery=%#v err=%v", prepared, err)
	}

	// Preserve workflow evidence but remove its terminal path from the durable
	// record. This reaches the explicit workflow-vs-ledger unsupported fence.
	mismatchState := cloneRepositoryReviewBackfillStateForCoverage(t, state)
	mismatchRun := mismatchState.Runs[0]
	mismatchRun.UnsupportedCount = 0
	mismatchRun.UnsupportedPaths = nil
	mismatchRun.UnreviewedFiles = len(plan.PendingFiles)
	mismatchRun.UnreviewedPaths = []string{reviewable.Path, unsupportedFile.Path}
	mismatchRun.RemainingFiles = len(plan.PendingFiles)
	sort.Strings(mismatchRun.UnreviewedPaths)
	mismatchState.Runs[0] = mismatchRun
	mismatchWorkflow := cloneRepositoryReviewBackfillRunForCoverage(t, workflowRun)
	recordStep := mismatchWorkflow.Steps["find_bugs/record"]
	recordStep.Outputs["run"] = mismatchRun
	mismatchWorkflow.Steps["find_bugs/record"] = recordStep
	prepared, err = prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), automation, mismatchState, repoaudit.NewRepositoryReviewCampaignID(),
		repositoryReviewMoreRawLoader{
			runs: map[string]*workflows.Run{mismatchWorkflow.ID: mismatchWorkflow},
			err:  map[string]error{},
		},
		profile,
	)
	if err != nil || prepared.Available || prepared.Exact {
		t.Fatalf("unsupported mismatch recovery=%#v err=%v", prepared, err)
	}
}

func TestFinalizeRepositoryReviewLegacyBackfillByteBoundariesCoverage(t *testing.T) {
	files := []repoaudit.FileRef{
		{Path: "a.go", BlobSHA: strings.Repeat("a", 40), SizeBytes: 1},
		{Path: "b.go", BlobSHA: strings.Repeat("b", 40), SizeBytes: 2},
		{Path: "c.bin", BlobSHA: strings.Repeat("c", 40), SizeBytes: 3},
	}
	coverage := map[string]repoaudit.RepositoryReviewCampaignPathCoverage{
		files[0].Path: {Inspected: true},
		files[1].Path: {Inspected: true, Completed: true},
		files[2].Path: {Unsupported: true},
	}
	recovered := []repositoryReviewLegacyRecoveredRun{{
		recovery: repoaudit.RepositoryReviewCampaignRunRecovery{
			ID: "wr_recovered", Plan: repoaudit.Plan{ID: "plan"}, InspectedFiles: 1,
		},
		contextIDs: []string{"context"}, findingIDs: []string{"finding"},
	}}
	base := repositoryReviewLegacyCampaignBackfill{Exact: true}
	state := repoaudit.RepositoryState{Repository: "owner/repo", ReviewVersion: 7}
	call := func(
		result repositoryReviewLegacyCampaignBackfill,
		runs []repositoryReviewLegacyRecoveredRun,
		maximum int,
	) (repositoryReviewLegacyCampaignBackfill, error) {
		return finalizeRepositoryReviewLegacyCampaignBackfill(
			result, state, repoaudit.NewRepositoryReviewCampaignID(), strings.Repeat("d", 40),
			"inventory", "profile", "sha256:"+strings.Repeat("e", 64), 4,
			nil, files, coverage, runs, maximum,
		)
	}
	valid, err := call(base, recovered, 1<<20)
	if err != nil || !valid.Available || valid.InspectedFiles != 2 ||
		valid.CompletedFiles != 1 || valid.UnsupportedFiles != 1 ||
		valid.FindingOccurrences != 1 {
		t.Fatalf("valid finalized recovery=%#v err=%v", valid, err)
	}
	tooSmall, err := call(base, recovered, 1)
	if err != nil || tooSmall.Available || tooSmall.Exact {
		t.Fatalf("base-byte bound recovery=%#v err=%v", tooSmall, err)
	}
	large := recovered
	large[0].contextIDs = []string{strings.Repeat("x", 8<<10)}
	candidateBound, err := call(base, large, 4<<10)
	if err != nil || candidateBound.Available || candidateBound.Exact {
		t.Fatalf("candidate-byte bound recovery=%#v err=%v", candidateBound, err)
	}
	inexact := base
	inexact.Exact = false
	inexactResult, err := call(inexact, recovered, 1<<20)
	if err != nil || inexactResult.Available || inexactResult.Exact {
		t.Fatalf("inexact finalized recovery=%#v err=%v", inexactResult, err)
	}
}
