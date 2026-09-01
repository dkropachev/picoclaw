package api

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func cloneRepositoryReviewBackfillRunForCoverage(t *testing.T, run *workflows.Run) *workflows.Run {
	t.Helper()
	data, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	var clone workflows.Run
	if err := json.Unmarshal(data, &clone); err != nil {
		t.Fatal(err)
	}
	return &clone
}

func repositoryReviewBackfillLoaderForCoverage(
	fixture repositoryReviewBackfillFixture,
) repositoryReviewBackfillLoader {
	runs := make(map[string]*workflows.Run, len(fixture.runs))
	for id, run := range fixture.runs {
		runs[id] = run
	}
	return repositoryReviewBackfillLoader{runs: runs, err: make(map[string]error)}
}

func cloneRepositoryReviewBackfillStateForCoverage(
	t *testing.T,
	state repoaudit.RepositoryState,
) repoaudit.RepositoryState {
	t.Helper()
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var clone repoaudit.RepositoryState
	if err := json.Unmarshal(data, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func TestRepositoryReviewLegacyBackfillPureHelperCoverage(t *testing.T) {
	first := repoaudit.FileRef{
		Path: "a.go", BlobSHA: strings.Repeat("a", 40), SizeBytes: 1,
		Category: "code", Mode: "100644",
	}
	second := repoaudit.FileRef{
		Path: "b.go", BlobSHA: strings.Repeat("b", 40), SizeBytes: 2,
		Category: "code", Mode: "100644",
	}

	digest, digestErr := repositoryReviewLegacyCatalogDigest(map[string]any{"files": []string{"a.go"}})
	if digestErr != nil || digest == "" {
		t.Fatalf("catalog digest=%q err=%v", digest, digestErr)
	}
	if _, err := repositoryReviewLegacyCatalogDigest(func() {}); err == nil {
		t.Fatal("unmarshalable catalog was accepted")
	}
	if _, err := repositoryReviewLegacyCatalogDigest(strings.Repeat(
		"x", repositoryReviewLegacyBackfillMaxRunBytes+1,
	)); !errors.Is(err, repoaudit.ErrInvalidPlan) {
		t.Fatalf("oversized catalog error=%v", err)
	}

	automation := testRepositoryReviewAutomation()
	automation.EffectiveAccountRef = "account"
	automation.MaxContentBytes = 2048
	resolved := workflows.RepositoryReviewModelProfile{
		Revision: "graph", AccountRef: "account", ReviewerModels: []string{"reviewer"},
		MaxContentBytes: 1024,
	}
	input, profileErr := repositoryReviewLegacyProfileHashInput(automation, "scope", resolved)
	if profileErr != nil || input.MaxContentBytes != 1024 || input.ModelGraphRevision != "graph" {
		t.Fatalf("profile input=%#v err=%v", input, profileErr)
	}
	currentHash, currentHashErr := repositoryReviewLegacyProfileHash(automation, "scope", resolved)
	if currentHashErr != nil || currentHash == "" {
		t.Fatalf("current hash=%q err=%v", currentHash, currentHashErr)
	}
	legacyHash, legacyHashErr := repositoryReviewHistoricalProfileHash(automation, "scope", resolved)
	if legacyHashErr != nil || legacyHash == "" || legacyHash == currentHash {
		t.Fatalf("legacy hash=%q current=%q err=%v", legacyHash, currentHash, legacyHashErr)
	}
	resolved.MaxContentBytes = 0
	if _, err := repositoryReviewLegacyProfileHashInput(automation, "scope", resolved); err == nil {
		t.Fatal("invalid resolved content bound was accepted")
	}

	for _, models := range [][]string{
		{""}, {" model"}, {strings.Repeat("m", 257)}, {"same", "same"},
	} {
		if repositoryReviewLegacyModelsCanonical(models) {
			t.Fatalf("noncanonical models accepted: %#v", models)
		}
	}
	if !repositoryReviewLegacyModelsCanonical([]string{"a", "b"}) {
		t.Fatal("canonical models were rejected")
	}
	for _, value := range []any{func() {}, "1", 1.5, 0, int64((4 << 20) + 1)} {
		if _, ok := repositoryReviewLegacyPositiveContentBytes(value); ok {
			t.Fatalf("invalid positive content bytes accepted: %#v", value)
		}
	}
	for _, value := range []any{1, int64(1024), json.Number("2048")} {
		if parsed, ok := repositoryReviewLegacyPositiveContentBytes(value); !ok || parsed < 1 {
			t.Fatalf("content bytes %#v = (%d,%v)", value, parsed, ok)
		}
	}

	manifest, err := repositoryReviewLegacyPlanManifest(repoaudit.Plan{
		PendingFiles: []repoaudit.FileRef{second}, DeferredFiles: []repoaudit.FileRef{first},
		UnsupportedFiles: []repoaudit.UnsupportedFile{{FileRef: repoaudit.FileRef{
			Path: "c.bin", BlobSHA: strings.Repeat("c", 40), SizeBytes: 3,
		}}},
	})
	if err != nil || len(manifest) != 3 || manifest[0].Path != "a.go" {
		t.Fatalf("manifest=%#v err=%v", manifest, err)
	}
	if _, err := repositoryReviewLegacyPlanManifest(repoaudit.Plan{
		PendingFiles: []repoaudit.FileRef{first}, DeferredFiles: []repoaudit.FileRef{first},
	}); err == nil {
		t.Fatal("duplicate plan manifest was accepted")
	}

	selected := map[string]repoaudit.FileRef{first.Path: first}
	coverage := make(map[string]repoaudit.RepositoryReviewCampaignPathCoverage)
	exact := true
	repositoryReviewMergeLegacyCoverage(
		coverage, selected, second,
		repoaudit.RepositoryReviewCampaignPathCoverage{Inspected: true}, &exact,
	)
	if exact {
		t.Fatal("out-of-scope coverage update remained exact")
	}
	exact = true
	repositoryReviewMergeLegacyCoverage(
		coverage, selected, first,
		repoaudit.RepositoryReviewCampaignPathCoverage{Inspected: true, Completed: true}, &exact,
	)
	if !exact || !coverage[first.Path].Inspected || !coverage[first.Path].Completed {
		t.Fatalf("merged coverage=%#v exact=%v", coverage, exact)
	}
	repositoryReviewMergeLegacyCoverage(
		coverage, selected, first,
		repoaudit.RepositoryReviewCampaignPathCoverage{Unsupported: true}, &exact,
	)
	if exact || coverage[first.Path] != (repoaudit.RepositoryReviewCampaignPathCoverage{}) {
		t.Fatalf("conflicting terminal coverage=%#v exact=%v", coverage, exact)
	}
	coverage[first.Path] = repoaudit.RepositoryReviewCampaignPathCoverage{Unsupported: true}
	exact = true
	repositoryReviewMergeLegacyCoverage(
		coverage, selected, first,
		repoaudit.RepositoryReviewCampaignPathCoverage{Completed: true}, &exact,
	)
	if exact || coverage[first.Path] != (repoaudit.RepositoryReviewCampaignPathCoverage{}) {
		t.Fatalf("conflicting reviewed coverage=%#v exact=%v", coverage, exact)
	}
}

func TestRepositoryReviewLegacyBackfillProjectionHelperCoverage(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(t, 1, repositoryReviewBackfillRunSpec{
		inspected: []int{0}, occurrences: 1,
	})
	runID := fixture.automation.RunIDs[0]
	workflowRun := fixture.runs[runID]
	ledgerRun := fixture.state.Runs[0]
	var plan repoaudit.Plan
	if !repositoryReviewDecodeValue(
		workflowRun.Steps["find_bugs/plan"].Outputs["plan"], &plan,
	) {
		t.Fatal("fixture plan did not decode")
	}
	manifest, err := repositoryReviewLegacyPlanManifest(plan)
	if err != nil {
		t.Fatal(err)
	}
	catalog, scopeHash, ok := repositoryReviewLegacyScopeEvidence(workflowRun, manifest)
	if !ok || catalog == nil || scopeHash == "" {
		t.Fatalf("scope evidence=(%#v,%q,%v)", catalog, scopeHash, ok)
	}
	for name, mutate := range map[string]func(*workflows.Run){
		"catalog failed": func(run *workflows.Run) {
			step := run.Steps["find_bugs/full_scope_catalog"]
			step.Status = workflows.RunStatusFailed
			run.Steps["find_bugs/full_scope_catalog"] = step
		},
		"scope failed": func(run *workflows.Run) {
			step := run.Steps["find_bugs/scope"]
			step.Error = "failed"
			run.Steps["find_bugs/scope"] = step
		},
		"missing plan": func(run *workflows.Run) {
			step := run.Steps["find_bugs/scope"]
			delete(step.Outputs, "scopePlan")
			run.Steps["find_bugs/scope"] = step
		},
		"malformed paths": func(run *workflows.Run) {
			step := run.Steps["find_bugs/scope"]
			step.Outputs["selectedPaths"] = "bad"
			run.Steps["find_bugs/scope"] = step
		},
		"path mismatch": func(run *workflows.Run) {
			step := run.Steps["find_bugs/scope"]
			step.Outputs["selectedPaths"] = []string{"other.go"}
			run.Steps["find_bugs/scope"] = step
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := cloneRepositoryReviewBackfillRunForCoverage(t, workflowRun)
			mutate(candidate)
			if _, _, valid := repositoryReviewLegacyScopeEvidence(candidate, manifest); valid {
				t.Fatal("invalid scope evidence was accepted")
			}
		})
	}

	decodedPlan, evidence, valid := repositoryReviewLegacyRunEvidence(
		workflowRun, ledgerRun, fixture.state.Repository,
	)
	if !valid || decodedPlan.ID != plan.ID || len(evidence.Children) == 0 {
		t.Fatalf("run evidence plan=%#v evidence=%#v valid=%v", decodedPlan, evidence, valid)
	}
	for name, mutate := range map[string]func(*workflows.Run, *repoaudit.ReviewRun){
		"nil run":      func(_ *workflows.Run, _ *repoaudit.ReviewRun) {},
		"wrong run id": func(run *workflows.Run, _ *repoaudit.ReviewRun) { run.ID = "other" },
		"plan failed": func(run *workflows.Run, _ *repoaudit.ReviewRun) {
			step := run.Steps["find_bugs/plan"]
			step.Status = workflows.RunStatusFailed
			run.Steps["find_bugs/plan"] = step
		},
		"malformed plan": func(run *workflows.Run, _ *repoaudit.ReviewRun) {
			step := run.Steps["find_bugs/plan"]
			step.Outputs["plan"] = "bad"
			run.Steps["find_bugs/plan"] = step
		},
		"noncanonical account": func(run *workflows.Run, _ *repoaudit.ReviewRun) {
			step := run.Steps["find_bugs/plan"]
			step.Outputs["accountRef"] = " account "
			run.Steps["find_bugs/plan"] = step
		},
		"duplicate reviewers": func(run *workflows.Run, _ *repoaudit.ReviewRun) {
			step := run.Steps["find_bugs/plan"]
			step.Outputs["reviewerModels"] = []string{"same", "same"}
			run.Steps["find_bugs/plan"] = step
		},
		"invalid content bytes": func(run *workflows.Run, _ *repoaudit.ReviewRun) {
			step := run.Steps["find_bugs/plan"]
			step.Outputs["maxContentBytes"] = 0
			run.Steps["find_bugs/plan"] = step
		},
		"record mismatch": func(run *workflows.Run, _ *repoaudit.ReviewRun) {
			step := run.Steps["find_bugs/record"]
			var recorded repoaudit.ReviewRun
			_ = repositoryReviewDecodeValue(step.Outputs["run"], &recorded)
			recorded.RemainingFiles++
			step.Outputs["run"] = recorded
			run.Steps["find_bugs/record"] = step
		},
		"unsupported outside pending": func(_ *workflows.Run, ledger *repoaudit.ReviewRun) {
			ledger.UnsupportedPaths = []string{"outside.bin"}
		},
		"missing managed children": func(run *workflows.Run, _ *repoaudit.ReviewRun) {
			step := run.Steps["find_bugs/review"]
			delete(step.Outputs, "managed_children")
			run.Steps["find_bugs/review"] = step
		},
		"skipped nonterminal review": func(run *workflows.Run, _ *repoaudit.ReviewRun) {
			step := run.Steps["find_bugs/review"]
			step.Status = workflows.RunStatusSkipped
			run.Steps["find_bugs/review"] = step
		},
		"malformed managed evidence": func(run *workflows.Run, _ *repoaudit.ReviewRun) {
			step := run.Steps["find_bugs/review"]
			step.Outputs["managed_children"] = "bad"
			run.Steps["find_bugs/review"] = step
		},
	} {
		t.Run(name, func(t *testing.T) {
			if name == "nil run" {
				if _, _, valid := repositoryReviewLegacyRunEvidence(nil, ledgerRun, fixture.state.Repository); valid {
					t.Fatal("nil run evidence was accepted")
				}
				return
			}
			candidate := cloneRepositoryReviewBackfillRunForCoverage(t, workflowRun)
			candidateLedger := ledgerRun
			mutate(candidate, &candidateLedger)
			if _, _, valid := repositoryReviewLegacyRunEvidence(
				candidate, candidateLedger, fixture.state.Repository,
			); valid {
				t.Fatal("invalid run evidence was accepted")
			}
		})
	}

	contextRecord := fixture.state.Contexts[0]
	finding := fixture.state.Findings[0]
	if !repositoryReviewLegacyContextMatchesScope(
		contextRecord, finding.File, map[string]repoaudit.FileRef{finding.File.Path: finding.File},
	) {
		t.Fatal("valid context scope was rejected")
	}
	if repositoryReviewLegacyContextMatchesScope(repoaudit.FindingContext{}, finding.File, nil) {
		t.Fatal("empty context scope was accepted")
	}
	outsideContext := contextRecord
	outsideContext.Files = []repoaudit.FileRef{{Path: "outside.go"}}
	if repositoryReviewLegacyContextMatchesScope(
		outsideContext, finding.File, map[string]repoaudit.FileRef{finding.File.Path: finding.File},
	) {
		t.Fatal("outside context scope was accepted")
	}
	wrongPrimary := contextRecord
	if repositoryReviewLegacyContextMatchesScope(
		wrongPrimary, repoaudit.FileRef{Path: "other.go"},
		map[string]repoaudit.FileRef{finding.File.Path: finding.File},
	) {
		t.Fatal("context without primary was accepted")
	}

	if !repositoryReviewLegacyContextHasEvidence(contextRecord, ledgerRun, evidence) {
		t.Fatal("valid context evidence was rejected")
	}
	wrongTime := contextRecord
	wrongTime.CreatedAt = wrongTime.CreatedAt.Add(time.Second)
	if repositoryReviewLegacyContextHasEvidence(wrongTime, ledgerRun, evidence) {
		t.Fatal("wrong-time context evidence was accepted")
	}
	badFiles := contextRecord
	badFiles.Files = append(badFiles.Files, badFiles.Files[0])
	if repositoryReviewLegacyContextHasEvidence(badFiles, ledgerRun, evidence) {
		t.Fatal("noncanonical context evidence was accepted")
	}
	if _, found := repositoryReviewLegacyFindingEvidence(finding, contextRecord, evidence); !found {
		t.Fatal("valid finding evidence was rejected")
	}
	if _, found := repositoryReviewLegacyFindingEvidence(finding, badFiles, evidence); found {
		t.Fatal("noncanonical finding evidence was accepted")
	}
	wrongEvidence := evidence
	wrongEvidence.Observations = append([]repoaudit.Observation(nil), evidence.Observations...)
	wrongEvidence.Observations[0].Model = "other"
	if _, found := repositoryReviewLegacyFindingEvidence(finding, contextRecord, wrongEvidence); found {
		t.Fatal("wrong-model finding evidence was accepted")
	}
}

func TestRepositoryReviewLegacyBackfillPreparationBoundaryCoverage(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(t, 2, repositoryReviewBackfillRunSpec{
		inspected: []int{0}, occurrences: 1,
	})
	campaignID := repoaudit.NewRepositoryReviewCampaignID()
	loader := repositoryReviewBackfillLoaderForCoverage(fixture)
	resolved := workflows.RepositoryReviewModelProfile{
		Revision: "legacy-automation-profile", AccountRef: fixture.automation.EffectiveAccountRef,
		ReviewerModels:  append([]string(nil), fixture.automation.ReviewerModels...),
		MaxContentBytes: int(fixture.automation.MaxContentBytes),
	}
	valid, err := prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), fixture.automation, fixture.state, campaignID, loader, resolved,
	)
	if err != nil || !valid.Available || !valid.Exact {
		t.Fatalf("valid prepared backfill=%#v err=%v", valid, err)
	}

	for name, run := range map[string]func() (repositoryReviewLegacyCampaignBackfill, error){
		"nil run store": func() (repositoryReviewLegacyCampaignBackfill, error) {
			return prepareRepositoryReviewLegacyCampaignBackfill(
				t.Context(), fixture.automation, fixture.state, campaignID, nil, resolved)
		},
		"campaign conflict": func() (repositoryReviewLegacyCampaignBackfill, error) {
			automation := fixture.automation
			automation.CampaignID = repoaudit.NewRepositoryReviewCampaignID()
			return prepareRepositoryReviewLegacyCampaignBackfill(
				t.Context(), automation, fixture.state, campaignID, loader, resolved)
		},
		"ledger campaign conflict": func() (repositoryReviewLegacyCampaignBackfill, error) {
			state := fixture.state
			state.CurrentCampaign = &repoaudit.RepositoryReviewCampaignCoverage{ID: repoaudit.NewRepositoryReviewCampaignID()}
			return prepareRepositoryReviewLegacyCampaignBackfill(
				t.Context(), fixture.automation, state, campaignID, loader, resolved)
		},
		"multiple profiles": func() (repositoryReviewLegacyCampaignBackfill, error) {
			return prepareRepositoryReviewLegacyCampaignBackfill(
				t.Context(), fixture.automation, fixture.state, campaignID, loader, resolved, resolved)
		},
		"empty resolved profile": func() (repositoryReviewLegacyCampaignBackfill, error) {
			return prepareRepositoryReviewLegacyCampaignBackfill(
				t.Context(), fixture.automation, fixture.state, campaignID, loader,
				workflows.RepositoryReviewModelProfile{})
		},
		"oversized reviewer cohort": func() (repositoryReviewLegacyCampaignBackfill, error) {
			profile := resolved
			profile.ReviewerModels = make([]string, 33)
			for index := range profile.ReviewerModels {
				profile.ReviewerModels[index] = string(rune('a' + index))
			}
			return prepareRepositoryReviewLegacyCampaignBackfill(
				t.Context(), fixture.automation, fixture.state, campaignID, loader, profile)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := run(); err == nil {
				t.Fatal("invalid preparation was accepted")
			}
		})
	}

	for name, mutate := range map[string]func(*repoaudit.RepositoryReviewAutomation, *repoaudit.RepositoryState, *repositoryReviewBackfillLoader){
		"missing start": func(automation *repoaudit.RepositoryReviewAutomation, _ *repoaudit.RepositoryState, _ *repositoryReviewBackfillLoader) {
			automation.StartedAt = time.Time{}
		},
		"blank configured run": func(automation *repoaudit.RepositoryReviewAutomation, _ *repoaudit.RepositoryState, _ *repositoryReviewBackfillLoader) {
			automation.RunIDs = append(automation.RunIDs, "")
		},
		"duplicate configured run": func(automation *repoaudit.RepositoryReviewAutomation, _ *repoaudit.RepositoryState, _ *repositoryReviewBackfillLoader) {
			automation.RunIDs = append(automation.RunIDs, automation.RunIDs[0])
		},
		"duplicate ledger run": func(_ *repoaudit.RepositoryReviewAutomation, state *repoaudit.RepositoryState, _ *repositoryReviewBackfillLoader) {
			state.Runs = append(state.Runs, state.Runs[0])
		},
		"foreign tagged run": func(_ *repoaudit.RepositoryReviewAutomation, state *repoaudit.RepositoryState, _ *repositoryReviewBackfillLoader) {
			state.Runs[0].CampaignID = repoaudit.NewRepositoryReviewCampaignID()
		},
		"missing workflow": func(_ *repoaudit.RepositoryReviewAutomation, _ *repoaudit.RepositoryState, loader *repositoryReviewBackfillLoader) {
			loader.err[fixture.automation.RunIDs[0]] = os.ErrNotExist
		},
		"duplicate context": func(_ *repoaudit.RepositoryReviewAutomation, state *repoaudit.RepositoryState, _ *repositoryReviewBackfillLoader) {
			state.Contexts = append(state.Contexts, state.Contexts[0])
		},
		"duplicate finding": func(_ *repoaudit.RepositoryReviewAutomation, state *repoaudit.RepositoryState, _ *repositoryReviewBackfillLoader) {
			state.Findings = append(state.Findings, state.Findings[0])
		},
	} {
		t.Run(name, func(t *testing.T) {
			automation := fixture.automation
			automation.RunIDs = append([]string(nil), fixture.automation.RunIDs...)
			state := fixture.state
			state.Runs = append([]repoaudit.ReviewRun(nil), fixture.state.Runs...)
			state.Contexts = append([]repoaudit.FindingContext(nil), fixture.state.Contexts...)
			state.Findings = append([]repoaudit.Finding(nil), fixture.state.Findings...)
			candidateLoader := repositoryReviewBackfillLoaderForCoverage(fixture)
			mutate(&automation, &state, &candidateLoader)
			prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
				t.Context(), automation, state, campaignID, candidateLoader, resolved,
			)
			if err != nil {
				t.Fatalf("inexact preparation error=%v", err)
			}
			if prepared.Available || prepared.Exact {
				t.Fatalf("inexact preparation=%#v", prepared)
			}
		})
	}
}

func TestRepositoryReviewLegacyBackfillWorkflowCorruptionCoverage(t *testing.T) {
	for name, mutate := range map[string]func(
		*repositoryReviewBackfillFixture,
		*repositoryReviewBackfillLoader,
		*workflows.RepositoryReviewModelProfile,
	){
		"workflow marshal error": func(
			fixture *repositoryReviewBackfillFixture,
			loader *repositoryReviewBackfillLoader,
			_ *workflows.RepositoryReviewModelProfile,
		) {
			run := cloneRepositoryReviewBackfillRunForCoverage(t, loader.runs[fixture.automation.RunIDs[0]])
			run.Inputs = map[string]any{"invalid": func() {}}
			loader.runs[run.ID] = run
		},
		"workflow size bound": func(
			fixture *repositoryReviewBackfillFixture,
			loader *repositoryReviewBackfillLoader,
			_ *workflows.RepositoryReviewModelProfile,
		) {
			run := cloneRepositoryReviewBackfillRunForCoverage(t, loader.runs[fixture.automation.RunIDs[0]])
			run.Inputs = map[string]any{"oversized": strings.Repeat("x", repositoryReviewLegacyBackfillMaxRunBytes)}
			loader.runs[run.ID] = run
		},
		"invalid frozen scope": func(
			fixture *repositoryReviewBackfillFixture,
			loader *repositoryReviewBackfillLoader,
			_ *workflows.RepositoryReviewModelProfile,
		) {
			run := cloneRepositoryReviewBackfillRunForCoverage(t, loader.runs[fixture.automation.RunIDs[0]])
			step := run.Steps["find_bugs/scope"]
			step.Status = workflows.RunStatusFailed
			run.Steps["find_bugs/scope"] = step
			loader.runs[run.ID] = run
		},
		"catalog marshal error": func(
			fixture *repositoryReviewBackfillFixture,
			loader *repositoryReviewBackfillLoader,
			_ *workflows.RepositoryReviewModelProfile,
		) {
			run := cloneRepositoryReviewBackfillRunForCoverage(t, loader.runs[fixture.automation.RunIDs[0]])
			step := run.Steps["find_bugs/full_scope_catalog"]
			step.Outputs["candidates"] = func() {}
			run.Steps["find_bugs/full_scope_catalog"] = step
			loader.runs[run.ID] = run
		},
		"catalog cannot recover scope": func(
			fixture *repositoryReviewBackfillFixture,
			loader *repositoryReviewBackfillLoader,
			_ *workflows.RepositoryReviewModelProfile,
		) {
			run := cloneRepositoryReviewBackfillRunForCoverage(t, loader.runs[fixture.automation.RunIDs[0]])
			step := run.Steps["find_bugs/full_scope_catalog"]
			step.Outputs["candidates"] = "invalid-catalog"
			run.Steps["find_bugs/full_scope_catalog"] = step
			loader.runs[run.ID] = run
		},
		"invalid resolved graph hash": func(
			_ *repositoryReviewBackfillFixture,
			_ *repositoryReviewBackfillLoader,
			profile *workflows.RepositoryReviewModelProfile,
		) {
			profile.Revision = ""
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newRepositoryReviewBackfillFixture(t, 2, repositoryReviewBackfillRunSpec{
				inspected: []int{0}, occurrences: 1,
			})
			loader := repositoryReviewBackfillLoaderForCoverage(fixture)
			profile := workflows.RepositoryReviewModelProfile{
				Revision: "legacy-automation-profile", AccountRef: fixture.automation.EffectiveAccountRef,
				ReviewerModels:  fixture.automation.ReviewerModels,
				MaxContentBytes: int(fixture.automation.MaxContentBytes),
			}
			mutate(&fixture, &loader, &profile)
			prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
				t.Context(), fixture.automation, fixture.state,
				repoaudit.NewRepositoryReviewCampaignID(), loader, profile,
			)
			if name == "catalog cannot recover scope" || name == "invalid resolved graph hash" {
				if err == nil {
					t.Fatalf("%s error=nil prepared=%#v", name, prepared)
				}
				return
			}
			if err != nil || prepared.Available || prepared.Exact {
				t.Fatalf("%s prepared=%#v err=%v", name, prepared, err)
			}
		})
	}

	fixture := newRepositoryReviewBackfillFixture(t, 2,
		repositoryReviewBackfillRunSpec{inspected: []int{0}},
		repositoryReviewBackfillRunSpec{inspected: []int{1}},
	)
	state := cloneRepositoryReviewBackfillStateForCoverage(t, fixture.state)
	state.Runs[1].CompletedAt = state.Runs[0].CompletedAt
	loader := repositoryReviewBackfillLoaderForCoverage(fixture)
	second := cloneRepositoryReviewBackfillRunForCoverage(t, loader.runs[fixture.automation.RunIDs[1]])
	second.UpdatedAt = loader.runs[fixture.automation.RunIDs[0]].UpdatedAt
	step := second.Steps["find_bugs/full_scope_catalog"]
	items := step.Outputs["candidates"].([]any)
	for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
		items[left], items[right] = items[right], items[left]
	}
	step.Outputs["candidates"] = items
	second.Steps["find_bugs/full_scope_catalog"] = step
	loader.runs[second.ID] = second
	prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), fixture.automation, state,
		repoaudit.NewRepositoryReviewCampaignID(), loader,
	)
	if err != nil || prepared.Available || prepared.Exact {
		t.Fatalf("catalog drift prepared=%#v err=%v", prepared, err)
	}

	state = cloneRepositoryReviewBackfillStateForCoverage(t, fixture.state)
	state.Contexts = append(state.Contexts, repoaudit.FindingContext{})
	state.Findings = append(state.Findings, repoaudit.Finding{})
	prepared, err = prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), fixture.automation, state,
		repoaudit.NewRepositoryReviewCampaignID(), repositoryReviewBackfillLoaderForCoverage(fixture),
	)
	if err != nil || !prepared.Available || !prepared.Exact {
		t.Fatalf("empty legacy records should be ignored: %#v err=%v", prepared, err)
	}
}

func TestRepositoryReviewLegacyGlobalTagBoundaryCoverage(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(t, 1, repositoryReviewBackfillRunSpec{
		inspected: []int{0}, occurrences: 1,
	})
	run := fixture.state.Runs[0]
	workflowRun := fixture.runs[run.ID]
	plan, evidence, valid := repositoryReviewLegacyRunEvidence(
		workflowRun, run, fixture.state.Repository,
	)
	if !valid {
		t.Fatal("fixture run evidence is invalid")
	}
	manifest, err := repositoryReviewLegacyPlanManifest(plan)
	if err != nil {
		t.Fatal(err)
	}
	manifestIndex := make(map[string]repoaudit.FileRef, len(manifest))
	selected := make(map[string]repoaudit.FileRef, len(manifest))
	for _, file := range manifest {
		manifestIndex[file.Path] = file
		selected[file.Path] = file
	}
	contextRecord := fixture.state.Contexts[0]
	finding := fixture.state.Findings[0]
	coverage := map[string]repoaudit.RepositoryReviewCampaignPathCoverage{
		finding.File.Path: {Inspected: true},
	}
	campaignID := repoaudit.NewRepositoryReviewCampaignID()
	basePlans := map[string]repoaudit.Plan{run.ID: plan}
	baseManifests := map[string]map[string]repoaudit.FileRef{run.ID: manifestIndex}
	baseEvidence := map[string]workflows.RepositoryReviewManagedEvidence{run.ID: evidence}
	baseContextsByID := map[string]repoaudit.FindingContext{contextRecord.ID: contextRecord}
	baseContextsByRun := map[string][]repoaudit.FindingContext{run.ID: {contextRecord}}
	baseFindings := map[string]repoaudit.Finding{finding.ID: finding}
	contexts, findings, unrecovered, exact := repositoryReviewLegacyGlobalTags(
		campaignID, fixture.state.Repository, run.CommitSHA, run.InventoryHash,
		[]repoaudit.ReviewRun{run}, basePlans, baseManifests, selected, coverage,
		baseEvidence, baseContextsByID, baseContextsByRun, baseFindings,
	)
	if !exact || len(contexts) != 1 || len(findings) != 1 || len(unrecovered) != 0 {
		t.Fatalf("valid global tags contexts=%#v findings=%#v unrecovered=%#v exact=%v",
			contexts, findings, unrecovered, exact)
	}

	t.Run("unrecovered run and duplicate projections", func(t *testing.T) {
		candidateRun := run
		candidateRun.FindingIDs = append(candidateRun.FindingIDs, "", finding.ID)
		_, _, _, exact := repositoryReviewLegacyGlobalTags(
			campaignID, fixture.state.Repository, run.CommitSHA, run.InventoryHash,
			[]repoaudit.ReviewRun{candidateRun}, map[string]repoaudit.Plan{},
			baseManifests, selected, coverage, baseEvidence,
			baseContextsByID, baseContextsByRun, baseFindings,
		)
		if exact {
			t.Fatal("unrecovered duplicate run projections remained exact")
		}
	})

	for name, mutate := range map[string]func(
		*repoaudit.ReviewRun,
		map[string]repoaudit.Plan,
		map[string]map[string]repoaudit.FileRef,
		map[string]repoaudit.FileRef,
		map[string]repoaudit.RepositoryReviewCampaignPathCoverage,
		map[string]workflows.RepositoryReviewManagedEvidence,
		map[string]repoaudit.FindingContext,
		map[string][]repoaudit.FindingContext,
		map[string]repoaudit.Finding,
	){
		"missing finding": func(_ *repoaudit.ReviewRun, _ map[string]repoaudit.Plan, _ map[string]map[string]repoaudit.FileRef, _ map[string]repoaudit.FileRef, _ map[string]repoaudit.RepositoryReviewCampaignPathCoverage, _ map[string]workflows.RepositoryReviewManagedEvidence, _ map[string]repoaudit.FindingContext, _ map[string][]repoaudit.FindingContext, findings map[string]repoaudit.Finding) {
			delete(findings, finding.ID)
		},
		"finding repository": func(_ *repoaudit.ReviewRun, _ map[string]repoaudit.Plan, _ map[string]map[string]repoaudit.FileRef, _ map[string]repoaudit.FileRef, _ map[string]repoaudit.RepositoryReviewCampaignPathCoverage, _ map[string]workflows.RepositoryReviewManagedEvidence, _ map[string]repoaudit.FindingContext, _ map[string][]repoaudit.FindingContext, findings map[string]repoaudit.Finding) {
			value := findings[finding.ID]
			value.Repository = "owner/other"
			findings[finding.ID] = value
		},
		"missing context": func(_ *repoaudit.ReviewRun, _ map[string]repoaudit.Plan, _ map[string]map[string]repoaudit.FileRef, _ map[string]repoaudit.FileRef, _ map[string]repoaudit.RepositoryReviewCampaignPathCoverage, _ map[string]workflows.RepositoryReviewManagedEvidence, contexts map[string]repoaudit.FindingContext, _ map[string][]repoaudit.FindingContext, _ map[string]repoaudit.Finding) {
			delete(contexts, contextRecord.ID)
		},
		"context profile": func(_ *repoaudit.ReviewRun, _ map[string]repoaudit.Plan, _ map[string]map[string]repoaudit.FileRef, _ map[string]repoaudit.FileRef, _ map[string]repoaudit.RepositoryReviewCampaignPathCoverage, _ map[string]workflows.RepositoryReviewManagedEvidence, contexts map[string]repoaudit.FindingContext, _ map[string][]repoaudit.FindingContext, _ map[string]repoaudit.Finding) {
			value := contexts[contextRecord.ID]
			value.ProfileHash = "other"
			contexts[contextRecord.ID] = value
		},
		"finding evidence": func(_ *repoaudit.ReviewRun, _ map[string]repoaudit.Plan, _ map[string]map[string]repoaudit.FileRef, _ map[string]repoaudit.FileRef, _ map[string]repoaudit.RepositoryReviewCampaignPathCoverage, evidenceByRun map[string]workflows.RepositoryReviewManagedEvidence, _ map[string]repoaudit.FindingContext, _ map[string][]repoaudit.FindingContext, _ map[string]repoaudit.Finding) {
			value := evidenceByRun[run.ID]
			value.Observations = nil
			evidenceByRun[run.ID] = value
		},
		"unselected retained context": func(_ *repoaudit.ReviewRun, _ map[string]repoaudit.Plan, _ map[string]map[string]repoaudit.FileRef, _ map[string]repoaudit.FileRef, _ map[string]repoaudit.RepositoryReviewCampaignPathCoverage, _ map[string]workflows.RepositoryReviewManagedEvidence, _ map[string]repoaudit.FindingContext, contextsByRun map[string][]repoaudit.FindingContext, _ map[string]repoaudit.Finding) {
			contextsByRun[run.ID] = append(contextsByRun[run.ID], repoaudit.FindingContext{ID: "extra"})
		},
		"unreferenced finding context": func(_ *repoaudit.ReviewRun, _ map[string]repoaudit.Plan, _ map[string]map[string]repoaudit.FileRef, _ map[string]repoaudit.FileRef, _ map[string]repoaudit.RepositoryReviewCampaignPathCoverage, _ map[string]workflows.RepositoryReviewManagedEvidence, contexts map[string]repoaudit.FindingContext, _ map[string][]repoaudit.FindingContext, findings map[string]repoaudit.Finding) {
			contexts["extra"] = contextRecord
			contexts["extra"] = func() repoaudit.FindingContext { value := contexts["extra"]; value.ID = "extra"; return value }()
			findings["extra"] = repoaudit.Finding{ID: "extra", ContextIDs: []string{"extra"}}
		},
		"missing inspection coverage": func(_ *repoaudit.ReviewRun, _ map[string]repoaudit.Plan, _ map[string]map[string]repoaudit.FileRef, _ map[string]repoaudit.FileRef, coverage map[string]repoaudit.RepositoryReviewCampaignPathCoverage, _ map[string]workflows.RepositoryReviewManagedEvidence, _ map[string]repoaudit.FindingContext, _ map[string][]repoaudit.FindingContext, _ map[string]repoaudit.Finding) {
			delete(coverage, finding.File.Path)
		},
		"finding severity projection": func(_ *repoaudit.ReviewRun, _ map[string]repoaudit.Plan, _ map[string]map[string]repoaudit.FileRef, _ map[string]repoaudit.FileRef, _ map[string]repoaudit.RepositoryReviewCampaignPathCoverage, _ map[string]workflows.RepositoryReviewManagedEvidence, _ map[string]repoaudit.FindingContext, _ map[string][]repoaudit.FindingContext, findings map[string]repoaudit.Finding) {
			value := findings[finding.ID]
			value.Severity = "low"
			findings[finding.ID] = value
		},
		"finding immutable title": func(_ *repoaudit.ReviewRun, _ map[string]repoaudit.Plan, _ map[string]map[string]repoaudit.FileRef, _ map[string]repoaudit.FileRef, _ map[string]repoaudit.RepositoryReviewCampaignPathCoverage, _ map[string]workflows.RepositoryReviewManagedEvidence, _ map[string]repoaudit.FindingContext, _ map[string][]repoaudit.FindingContext, findings map[string]repoaudit.Finding) {
			value := findings[finding.ID]
			value.Title += " tampered"
			findings[finding.ID] = value
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidateRun := run
			candidateRun.FindingIDs = append([]string(nil), run.FindingIDs...)
			plans := make(map[string]repoaudit.Plan, len(basePlans))
			for key, value := range basePlans {
				plans[key] = value
			}
			manifests := make(map[string]map[string]repoaudit.FileRef, len(baseManifests))
			for key, values := range baseManifests {
				copied := make(map[string]repoaudit.FileRef, len(values))
				for pathValue, file := range values {
					copied[pathValue] = file
				}
				manifests[key] = copied
			}
			selectedCopy := make(map[string]repoaudit.FileRef, len(selected))
			for key, value := range selected {
				selectedCopy[key] = value
			}
			coverageCopy := make(map[string]repoaudit.RepositoryReviewCampaignPathCoverage, len(coverage))
			for key, value := range coverage {
				coverageCopy[key] = value
			}
			evidenceCopy := make(map[string]workflows.RepositoryReviewManagedEvidence, len(baseEvidence))
			for key, value := range baseEvidence {
				value.Observations = append([]repoaudit.Observation(nil), value.Observations...)
				evidenceCopy[key] = value
			}
			contextsByID := make(map[string]repoaudit.FindingContext, len(baseContextsByID))
			for key, value := range baseContextsByID {
				contextsByID[key] = value
			}
			contextsByRun := make(map[string][]repoaudit.FindingContext, len(baseContextsByRun))
			for key, values := range baseContextsByRun {
				contextsByRun[key] = append([]repoaudit.FindingContext(nil), values...)
			}
			findingsByID := make(map[string]repoaudit.Finding, len(baseFindings))
			for key, value := range baseFindings {
				value.ContextIDs = append([]string(nil), value.ContextIDs...)
				value.Observations = append([]repoaudit.FindingObservation(nil), value.Observations...)
				findingsByID[key] = value
			}
			mutate(
				&candidateRun, plans, manifests, selectedCopy, coverageCopy, evidenceCopy,
				contextsByID, contextsByRun, findingsByID,
			)
			_, _, _, exact := repositoryReviewLegacyGlobalTags(
				campaignID, fixture.state.Repository, run.CommitSHA, run.InventoryHash,
				[]repoaudit.ReviewRun{candidateRun}, plans, manifests, selectedCopy, coverageCopy,
				evidenceCopy, contextsByID, contextsByRun, findingsByID,
			)
			if exact {
				t.Fatal("corrupt global tags remained exact")
			}
		})
	}
}

func TestRepositoryReviewLegacyProjectionRemainingBoundaryCoverage(t *testing.T) {
	base := repoaudit.Finding{
		ContextIDs: []string{"context"}, Models: []string{"review"}, ObservationCount: 1,
		Observations: []repoaudit.FindingObservation{{ContextID: "context", Model: "review"}},
	}
	for name, mutate := range map[string]func(*repoaudit.Finding){
		"empty context ID":            func(value *repoaudit.Finding) { value.ContextIDs[0] = "" },
		"empty observation model":     func(value *repoaudit.Finding) { value.Observations[0].Model = "" },
		"missing observation context": func(value *repoaudit.Finding) { value.Observations = nil; value.ObservationCount = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := base
			candidate.ContextIDs = append([]string(nil), base.ContextIDs...)
			candidate.Observations = append([]repoaudit.FindingObservation(nil), base.Observations...)
			mutate(&candidate)
			if repositoryReviewLegacyFindingProjectionValid(candidate) {
				t.Fatal("invalid finding projection was accepted")
			}
		})
	}
}

func TestRepositoryReviewLegacyInstallAndApplyReadErrorCoverage(t *testing.T) {
	prepare := func(t *testing.T) (repositoryReviewBackfillFixture, repositoryReviewLegacyCampaignBackfill) {
		t.Helper()
		fixture := newRepositoryReviewBackfillFixture(t, 1, repositoryReviewBackfillRunSpec{
			inspected: []int{0}, occurrences: 1,
		})
		prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
			t.Context(), fixture.automation, fixture.state,
			repoaudit.NewRepositoryReviewCampaignID(), fixture.runStore,
		)
		if err != nil || !prepared.Available {
			t.Fatalf("prepared=%#v err=%v", prepared, err)
		}
		return fixture, prepared
	}
	t.Run("install read error", func(t *testing.T) {
		t.Skip("per-automation JSON corruption was replaced by SQLite integrity coverage")
		fixture, prepared := prepare(t)
		path := filepath.Join(
			fixture.workspace, "repository_reviews", "automation_"+fixture.automation.ID+".json",
		)
		if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := installRepositoryReviewLegacyCampaignAuthority(
			t.Context(), fixture.store, prepared,
		); err == nil {
			t.Fatal("corrupt automation install error=nil")
		}
	})
	t.Run("apply read error", func(t *testing.T) {
		t.Skip("per-automation JSON corruption was replaced by SQLite integrity coverage")
		fixture, prepared := prepare(t)
		_, installedPrepared, err := installRepositoryReviewLegacyCampaignAuthority(
			t.Context(), fixture.store, prepared,
		)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(
			fixture.workspace, "repository_reviews", "automation_"+fixture.automation.ID+".json",
		)
		if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := applyRepositoryReviewLegacyCampaignBackfill(
			t.Context(), fixture.store, installedPrepared,
		); err == nil {
			t.Fatal("corrupt automation apply error=nil")
		}
	})
}

func TestRepositoryReviewLegacyBackfillInstallApplyAndAdapterBoundaryCoverage(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(t, 1, repositoryReviewBackfillRunSpec{
		inspected: []int{0}, occurrences: 1,
	})
	campaignID := repoaudit.NewRepositoryReviewCampaignID()
	resolved := workflows.RepositoryReviewModelProfile{
		Revision: "legacy-automation-profile", AccountRef: fixture.automation.EffectiveAccountRef,
		ReviewerModels:  fixture.automation.ReviewerModels,
		MaxContentBytes: int(fixture.automation.MaxContentBytes),
	}
	prepared, prepareErr := prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), fixture.automation, fixture.state, campaignID, fixture.runStore, resolved,
	)
	if prepareErr != nil || !prepared.Available {
		t.Fatalf("prepared=%#v err=%v", prepared, prepareErr)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := installRepositoryReviewLegacyCampaignAuthority(
		canceled, fixture.store, prepared,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled install error=%v", err)
	}
	if _, _, err := installRepositoryReviewLegacyCampaignAuthority(
		t.Context(), fixture.store, repositoryReviewLegacyCampaignBackfill{},
	); !errors.Is(err, repoaudit.ErrInvalidPlan) {
		t.Fatalf("invalid install error=%v", err)
	}
	missing := prepared
	missing.AutomationID = "rra_missing"
	if _, _, err := installRepositoryReviewLegacyCampaignAuthority(
		t.Context(), fixture.store, missing,
	); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing install error=%v", err)
	}
	stale := prepared
	stale.AutomationVersion++
	if _, _, err := installRepositoryReviewLegacyCampaignAuthority(
		t.Context(), fixture.store, stale,
	); !errors.Is(err, repoaudit.ErrConflict) {
		t.Fatalf("stale install error=%v", err)
	}
	installed, installedPrepared, err := installRepositoryReviewLegacyCampaignAuthority(
		t.Context(), fixture.store, prepared,
	)
	if err != nil || !installed.CampaignRecoveryPending {
		t.Fatalf("installed=%#v err=%v", installed, err)
	}
	tooStale := installedPrepared
	tooStale.AutomationVersion += 2
	if _, _, err := installRepositoryReviewLegacyCampaignAuthority(
		t.Context(), fixture.store, tooStale,
	); !errors.Is(err, repoaudit.ErrConflict) {
		t.Fatalf("too-stale replay error=%v", err)
	}
	if _, err := applyRepositoryReviewLegacyCampaignBackfill(
		canceled, fixture.store, installedPrepared,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled apply error=%v", err)
	}
	if _, err := applyRepositoryReviewLegacyCampaignBackfill(
		t.Context(), fixture.store, repositoryReviewLegacyCampaignBackfill{},
	); !errors.Is(err, repoaudit.ErrInvalidPlan) {
		t.Fatalf("invalid apply error=%v", err)
	}
	missingApply := installedPrepared
	missingApply.AutomationID = "rra_missing"
	if _, err := applyRepositoryReviewLegacyCampaignBackfill(
		t.Context(), fixture.store, missingApply,
	); !errors.Is(err, repoaudit.ErrConflict) {
		t.Fatalf("missing apply error=%v", err)
	}

	controller := &repositoryReviewController{}
	if _, err := controller.recoverLegacyRepositoryReviewCampaign(
		canceled, fixture.store, fixture.workspace, fixture.automation,
		fixture.automation.ResolvedCommitSHA, resolved,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled adapter error=%v", err)
	}
	if _, err := controller.recoverLegacyRepositoryReviewCampaign(
		t.Context(), fixture.store, "", fixture.automation,
		fixture.automation.ResolvedCommitSHA, resolved,
	); !errors.Is(err, repoaudit.ErrInvalidAutomation) {
		t.Fatalf("blank-workspace adapter error=%v", err)
	}
	if _, err := controller.recoverLegacyRepositoryReviewCampaign(
		t.Context(), fixture.store, fixture.workspace, fixture.automation, "bad", resolved,
	); !errors.Is(err, repoaudit.ErrInvalidAutomation) {
		t.Fatalf("bad-commit adapter error=%v", err)
	}
	missingAutomation := fixture.automation
	missingAutomation.Repository = "owner/missing"
	missingAutomation.RunIDs = nil
	if _, err := controller.recoverLegacyRepositoryReviewCampaign(
		t.Context(), fixture.store, fixture.workspace, missingAutomation,
		fixture.automation.ResolvedCommitSHA, resolved,
	); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing-state adapter error=%v", err)
	}
	wrongCommit := strings.Repeat("f", 40)
	if _, err := controller.recoverLegacyRepositoryReviewCampaign(
		t.Context(), fixture.store, fixture.workspace, fixture.automation, wrongCommit, resolved,
	); !errors.Is(err, repoaudit.ErrConflict) {
		t.Fatalf("wrong-commit adapter error=%v", err)
	}
}
