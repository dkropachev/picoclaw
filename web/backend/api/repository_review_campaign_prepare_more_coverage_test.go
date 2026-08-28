package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type repositoryReviewMoreRawLoader struct {
	runs map[string]*workflows.Run
	err  map[string]error
}

func (l repositoryReviewMoreRawLoader) GetRun(_ context.Context, id string) (*workflows.Run, error) {
	if err := l.err[id]; err != nil {
		return nil, err
	}
	return l.runs[id], nil
}

type repositoryReviewMoreErrContext struct {
	context.Context
	failAt int
	calls  int
}

func (c *repositoryReviewMoreErrContext) Err() error {
	c.calls++
	if c.calls >= c.failAt {
		return context.Canceled
	}
	return nil
}

func repositoryReviewMoreCloneRun(t *testing.T, run *workflows.Run) *workflows.Run {
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

func TestRepositoryReviewLegacyPrepareContextFencesMoreCoverage(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(t, 1, repositoryReviewBackfillRunSpec{
		inspected: []int{0}, occurrences: 1,
	})
	campaignID := repoaudit.NewRepositoryReviewCampaignID()
	raw := repositoryReviewMoreRawLoader{runs: fixture.runs, err: map[string]error{}}

	for _, failAt := range []int{2, 3, 4} {
		t.Run(string(rune('0'+failAt)), func(t *testing.T) {
			ctx := &repositoryReviewMoreErrContext{Context: t.Context(), failAt: failAt}
			_, err := prepareRepositoryReviewLegacyCampaignBackfill(
				ctx, fixture.automation, fixture.state, campaignID, raw,
			)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("failAt=%d error=%v calls=%d", failAt, err, ctx.calls)
			}
		})
	}

	nonLedger := cloneRepositoryReviewBackfillStateForCoverage(t, fixture.state)
	nonLedger.Runs = nil
	for _, failAt := range []int{2, 3} {
		t.Run("non-ledger-"+string(rune('0'+failAt)), func(t *testing.T) {
			ctx := &repositoryReviewMoreErrContext{Context: t.Context(), failAt: failAt}
			_, err := prepareRepositoryReviewLegacyCampaignBackfill(
				ctx, fixture.automation, nonLedger, campaignID, raw,
			)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("failAt=%d error=%v calls=%d", failAt, err, ctx.calls)
			}
		})
	}

	deadline := repositoryReviewMoreRawLoader{
		runs: fixture.runs,
		err:  map[string]error{fixture.automation.RunIDs[0]: context.DeadlineExceeded},
	}
	if _, err := prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), fixture.automation, nonLedger, campaignID, deadline,
	); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("non-ledger deadline error=%v", err)
	}
}

func TestRepositoryReviewLegacyPrepareRawWorkflowBoundariesMoreCoverage(t *testing.T) {
	for _, nonLedger := range []bool{false, true} {
		for name, mutate := range map[string]func(*workflows.Run){
			"marshal": func(run *workflows.Run) {
				run.Inputs = map[string]any{"bad": func() {}}
			},
			"size": func(run *workflows.Run) {
				run.Inputs = map[string]any{"large": strings.Repeat("x", repositoryReviewLegacyBackfillMaxRunBytes)}
			},
		} {
			t.Run(name+string(rune('0'+map[bool]int{false: 0, true: 1}[nonLedger])), func(t *testing.T) {
				fixture := newRepositoryReviewBackfillFixture(
					t, 1, repositoryReviewBackfillRunSpec{inspected: []int{0}},
				)
				runID := fixture.automation.RunIDs[0]
				run := repositoryReviewMoreCloneRun(t, fixture.runs[runID])
				mutate(run)
				state := cloneRepositoryReviewBackfillStateForCoverage(t, fixture.state)
				if nonLedger {
					state.Runs = nil
				}
				prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
					t.Context(), fixture.automation, state,
					repoaudit.NewRepositoryReviewCampaignID(),
					repositoryReviewMoreRawLoader{runs: map[string]*workflows.Run{runID: run}, err: map[string]error{}},
				)
				if name == "marshal" {
					if err == nil {
						t.Fatalf("marshal corruption accepted: %#v", prepared)
					}
					return
				}
				if err != nil || prepared.Exact || prepared.Available {
					t.Fatalf("size prepared=%#v err=%v", prepared, err)
				}
			})
		}
	}

	fixture := newRepositoryReviewBackfillFixture(t, 1, repositoryReviewBackfillRunSpec{inspected: []int{0}})
	runID := fixture.automation.RunIDs[0]
	state := cloneRepositoryReviewBackfillStateForCoverage(t, fixture.state)
	state.Runs = nil
	older := repositoryReviewMoreCloneRun(t, fixture.runs[runID])
	older.CreatedAt = fixture.automation.StartedAt.Add(-time.Minute)
	prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), fixture.automation, state, repoaudit.NewRepositoryReviewCampaignID(),
		repositoryReviewMoreRawLoader{runs: map[string]*workflows.Run{runID: older}, err: map[string]error{}},
	)
	if err != nil || prepared.Exact || prepared.Available {
		t.Fatalf("old non-ledger prepared=%#v err=%v", prepared, err)
	}

	disallowed := repositoryReviewMoreCloneRun(t, fixture.runs[runID])
	disallowed.Steps["find_bugs/record"] = workflows.StepExecution{Status: workflows.RunStatusFailed}
	disallowed.Status = workflows.RunStatusRunning
	prepared, err = prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), fixture.automation, state, repoaudit.NewRepositoryReviewCampaignID(),
		repositoryReviewMoreRawLoader{runs: map[string]*workflows.Run{runID: disallowed}, err: map[string]error{}},
	)
	if err != nil || prepared.Exact || prepared.Available {
		t.Fatalf("disallowed non-ledger prepared=%#v err=%v", prepared, err)
	}
}

func TestRepositoryReviewLegacyPrepareCrossRunDriftMoreCoverage(t *testing.T) {
	for name, mutate := range map[string]func(*testing.T, *repositoryReviewBackfillFixture, *workflows.Run){
		"catalog marshal": func(_ *testing.T, _ *repositoryReviewBackfillFixture, run *workflows.Run) {
			step := run.Steps["find_bugs/full_scope_catalog"]
			step.Outputs["candidates"] = func() {}
			run.Steps["find_bugs/full_scope_catalog"] = step
		},
		"catalog drift": func(_ *testing.T, _ *repositoryReviewBackfillFixture, run *workflows.Run) {
			step := run.Steps["find_bugs/full_scope_catalog"]
			step.Outputs["candidates"] = map[string]any{"different": true}
			run.Steps["find_bugs/full_scope_catalog"] = step
		},
		"commit drift": func(_ *testing.T, fixture *repositoryReviewBackfillFixture, run *workflows.Run) {
			planStep := run.Steps["find_bugs/plan"]
			var plan repoaudit.Plan
			if !repositoryReviewDecodeValue(planStep.Outputs["plan"], &plan) {
				return
			}
			plan.CommitSHA = strings.Repeat("b", 40)
			planStep.Outputs["plan"] = plan
			run.Steps["find_bugs/plan"] = planStep
			fixture.state.Runs[1].CommitSHA = plan.CommitSHA
			record := run.Steps["find_bugs/record"]
			record.Outputs["run"] = fixture.state.Runs[1]
			run.Steps["find_bugs/record"] = record
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newRepositoryReviewBackfillFixture(t, 2,
				repositoryReviewBackfillRunSpec{inspected: []int{0}},
				repositoryReviewBackfillRunSpec{inspected: []int{1}},
			)
			fixture.state = cloneRepositoryReviewBackfillStateForCoverage(t, fixture.state)
			secondID := fixture.automation.RunIDs[1]
			second := repositoryReviewMoreCloneRun(t, fixture.runs[secondID])
			mutate(t, &fixture, second)
			runs := map[string]*workflows.Run{
				fixture.automation.RunIDs[0]: fixture.runs[fixture.automation.RunIDs[0]],
				secondID:                     second,
			}
			prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
				t.Context(), fixture.automation, fixture.state,
				repoaudit.NewRepositoryReviewCampaignID(),
				repositoryReviewMoreRawLoader{runs: runs, err: map[string]error{}},
			)
			if name == "catalog marshal" {
				if err == nil || prepared.Available {
					t.Fatalf("catalog marshal prepared=%#v err=%v", prepared, err)
				}
				return
			}
			if err != nil || prepared.Exact || prepared.Available {
				t.Fatalf("drift prepared=%#v err=%v", prepared, err)
			}
		})
	}
}

func TestRepositoryReviewLegacyGlobalTagsMoreCoverage(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(t, 1, repositoryReviewBackfillRunSpec{
		inspected: []int{0}, occurrences: 1,
	})
	run := fixture.state.Runs[0]
	plan, evidence, valid := repositoryReviewLegacyRunEvidence(
		fixture.runs[run.ID], run, fixture.state.Repository,
	)
	if !valid {
		t.Fatal("invalid fixture evidence")
	}
	manifest, err := repositoryReviewLegacyPlanManifest(plan)
	if err != nil {
		t.Fatal(err)
	}
	manifestIndex := map[string]repoaudit.FileRef{}
	selected := map[string]repoaudit.FileRef{}
	for _, file := range manifest {
		manifestIndex[file.Path] = file
		selected[file.Path] = file
	}
	contextRecord := fixture.state.Contexts[0]
	finding := fixture.state.Findings[0]
	coverage := map[string]repoaudit.RepositoryReviewCampaignPathCoverage{
		finding.File.Path: {Inspected: true},
	}
	call := func(runValue repoaudit.ReviewRun, evidenceValue workflows.RepositoryReviewManagedEvidence, findingValue repoaudit.Finding) ([]string, []string, []string, bool) {
		return repositoryReviewLegacyGlobalTags(
			repoaudit.NewRepositoryReviewCampaignID(), fixture.state.Repository,
			run.CommitSHA, run.InventoryHash, []repoaudit.ReviewRun{runValue},
			map[string]repoaudit.Plan{run.ID: plan},
			map[string]map[string]repoaudit.FileRef{run.ID: manifestIndex},
			selected, coverage,
			map[string]workflows.RepositoryReviewManagedEvidence{run.ID: evidenceValue},
			map[string]repoaudit.FindingContext{contextRecord.ID: contextRecord},
			map[string][]repoaudit.FindingContext{run.ID: {contextRecord}},
			map[string]repoaudit.Finding{findingValue.ID: findingValue},
		)
	}

	duplicate := run
	duplicate.FindingIDs = append(duplicate.FindingIDs, duplicate.FindingIDs[0], "")
	if _, _, _, exact := call(duplicate, evidence, finding); exact {
		t.Fatal("duplicate/blank finding projection remained exact")
	}

	withoutCandidate := evidence
	withoutCandidate.Observations = append([]repoaudit.Observation(nil), evidence.Observations...)
	withoutCandidate.Observations[0].Findings = nil
	if _, _, unrecovered, exact := call(run, withoutCandidate, finding); exact || len(unrecovered) != 1 {
		t.Fatalf("missing candidate exact=%v unrecovered=%#v", exact, unrecovered)
	}

	blankContext := finding
	blankContext.ContextIDs = []string{""}
	if repositoryReviewLegacyFindingProjectionValid(blankContext) {
		t.Fatal("blank context identity accepted")
	}
}
