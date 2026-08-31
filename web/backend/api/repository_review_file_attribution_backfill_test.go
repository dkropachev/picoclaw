package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestRepositoryReviewFileAttributionBackfillRestoresExactAcknowledgements(t *testing.T) {
	inspected := make([]int, 27)
	for index := range inspected {
		inspected[index] = index
	}
	fixture := newRepositoryReviewBackfillFixture(t, 27, repositoryReviewBackfillRunSpec{
		inspected: inspected,
	})
	runID := fixture.automation.RunIDs[0]
	run := fixture.runs[runID]
	review := run.Steps["find_bugs/review"]
	children := review.Outputs["managed_children"].([]map[string]any)
	firstScope := children[0]["scope"].([]map[string]any)
	secondPaths := make([]string, 0, 26)
	for _, file := range firstScope[:26] {
		secondPaths = append(secondPaths, file["path"].(string))
	}
	children[1] = map[string]any{
		"label": "security", "required": true, "valid": true,
		"tasks": []string{workflows.RepositoryBugFinderFocuses()[1].Task},
		"scope": firstScope,
		"structured": map[string]any{
			"summary": "second retained focus", "reviewedFiles": secondPaths,
			"findings": []map[string]any{}, "residualRisks": []string{},
		},
		"text": "validated", "model": map[string]any{"selected": "review-a"},
	}
	for index := range children {
		children[index]["index"] = index + 1
		if valid, _ := children[index]["valid"].(bool); valid {
			children[index]["usage"] = []map[string]any{{
				"model": "provider/review-a", "reviewer": "review-a",
			}}
		}
	}
	review.Outputs["agent_id"] = "main"
	review.Outputs["managed_children"] = children
	run.Steps["find_bugs/review"] = review
	fixture.runs[runID] = run

	prepared, err := prepareRepositoryReviewFileAttributionBackfill(
		t.Context(), fixture.automation, fixture.state,
		repositoryReviewBackfillLoader{runs: fixture.runs, err: map[string]error{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	report := prepared.report
	if report.ConfiguredRuns != 1 || report.RecoveredRuns != 1 ||
		report.AllowedNonLedgerRuns != 0 || report.ChildAttempts != 4 ||
		report.SuccessfulChildren != 2 || report.FailedChildren != 2 ||
		report.AttributionRecords != 2 || report.AcknowledgementOccurrences != 53 ||
		report.UniqueFiles != 27 || report.UniqueFileAssignments != 53 ||
		report.Digest == "" {
		t.Fatalf("attribution backfill report = %#v", report)
	}
	for _, attribution := range prepared.attributions {
		if attribution.Source != repoaudit.RepositoryReviewFileAttributionSourceLegacyManagedChild ||
			attribution.RootAgentID != "main" || attribution.ReviewerIdentity != "review-a" ||
			attribution.Model != "review-a" || attribution.ModelAlias != "" ||
			attribution.Account != "" || attribution.UsageModel != "provider/review-a" {
			t.Fatalf("legacy attribution = %#v", attribution)
		}
	}

	merged, err := fixture.store.MergeRepositoryReviewFileAttributions(
		t.Context(),
		repoaudit.MergeRepositoryReviewFileAttributionsRequest{
			Repository: fixture.state.Repository, ExpectedVersion: fixture.state.Version,
			Attributions: prepared.attributions,
		},
	)
	if err != nil || len(merged.FileAttributions) != 2 {
		t.Fatalf("merged attributions = %#v, %v", merged.FileAttributions, err)
	}
	replayed, err := fixture.store.MergeRepositoryReviewFileAttributions(
		t.Context(),
		repoaudit.MergeRepositoryReviewFileAttributionsRequest{
			Repository: fixture.state.Repository, ExpectedVersion: fixture.state.Version,
			Attributions: prepared.attributions,
		},
	)
	if err != nil || replayed.Version != merged.Version ||
		!reflect.DeepEqual(replayed.FileAttributions, merged.FileAttributions) {
		t.Fatalf("replayed attributions = %#v, %v", replayed.FileAttributions, err)
	}
	reprepared, err := prepareRepositoryReviewFileAttributionBackfill(
		t.Context(), fixture.automation, merged,
		repositoryReviewBackfillLoader{runs: fixture.runs, err: map[string]error{}},
	)
	if err != nil || reprepared.report.ExistingAttributionRecords != 2 ||
		reprepared.report.Digest != prepared.report.Digest ||
		!reflect.DeepEqual(reprepared.attributions, prepared.attributions) {
		t.Fatalf(
			"reprepared legacy attributions=%#v report=%#v err=%v",
			reprepared.attributions,
			reprepared.report,
			err,
		)
	}
}

func TestRepositoryReviewFileAttributionDigestBindsAutomationAndAllRuns(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(t, 2, repositoryReviewBackfillRunSpec{
		inspected: []int{0},
	})
	repositoryReviewPrepareAttributionFixtureRuns(t, &fixture)
	loader := repositoryReviewBackfillLoader{runs: fixture.runs, err: map[string]error{}}
	base, err := prepareRepositoryReviewFileAttributionBackfill(
		t.Context(), fixture.automation, fixture.state, loader,
	)
	if err != nil {
		t.Fatal(err)
	}
	versionChanged := fixture.automation
	versionChanged.Version++
	versioned, err := prepareRepositoryReviewFileAttributionBackfill(
		t.Context(), versionChanged, fixture.state, loader,
	)
	if err != nil || versioned.report.Digest == base.report.Digest {
		t.Fatalf("version-bound digest=%q base=%q err=%v", versioned.report.Digest, base.report.Digest, err)
	}

	nonLedgerID := "wr_attribution_non_ledger"
	completedAt := fixture.automation.StartedAt.Add(time.Minute)
	nonLedger := &workflows.Run{
		ID: nonLedgerID, WorkflowRef: workflows.RepositoryBugFinderWorkflowRef,
		Status: workflows.RunStatusFailed, Error: "failed before planning",
		CreatedAt: fixture.automation.StartedAt, CompletedAt: &completedAt,
		Steps: map[string]workflows.StepExecution{},
	}
	withNonLedger := fixture.automation
	withNonLedger.RunIDs = append(append([]string(nil), withNonLedger.RunIDs...), nonLedgerID)
	runs := make(map[string]*workflows.Run, len(fixture.runs)+1)
	for runID, run := range fixture.runs {
		runs[runID] = run
	}
	runs[nonLedgerID] = nonLedger
	added, err := prepareRepositoryReviewFileAttributionBackfill(
		t.Context(), withNonLedger, fixture.state,
		repositoryReviewBackfillLoader{runs: runs, err: map[string]error{}},
	)
	if err != nil || added.report.AllowedNonLedgerRuns != 1 ||
		added.report.Digest == base.report.Digest {
		t.Fatalf("non-ledger digest report=%#v err=%v", added.report, err)
	}
	mutatedRun := *nonLedger
	mutatedRun.Error = "different pre-planning failure"
	runs[nonLedgerID] = &mutatedRun
	mutated, err := prepareRepositoryReviewFileAttributionBackfill(
		t.Context(), withNonLedger, fixture.state,
		repositoryReviewBackfillLoader{runs: runs, err: map[string]error{}},
	)
	if err != nil || mutated.report.AllowedNonLedgerRuns != 1 ||
		mutated.report.Digest == added.report.Digest {
		t.Fatalf("run-content digest report=%#v err=%v", mutated.report, err)
	}
}

func TestRepositoryReviewFileAttributionBackfillRejectsPartialSuccessfulIdentity(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(t, 2, repositoryReviewBackfillRunSpec{
		inspected: []int{0},
	})
	repositoryReviewPrepareAttributionFixtureRuns(t, &fixture)
	runID := fixture.automation.RunIDs[0]
	run := fixture.runs[runID]
	review := run.Steps["find_bugs/review"]
	children := review.Outputs["managed_children"].([]map[string]any)
	children[0]["tasks"] = []string{"unknown legacy focus"}
	review.Outputs["managed_children"] = children
	run.Steps["find_bugs/review"] = review

	if _, err := prepareRepositoryReviewFileAttributionBackfill(
		t.Context(), fixture.automation, fixture.state,
		repositoryReviewBackfillLoader{runs: fixture.runs, err: map[string]error{}},
	); !errors.Is(err, repoaudit.ErrInvalidPlan) {
		t.Fatalf("partial successful identity error=%v", err)
	}
}

func TestRepositoryReviewFileAttributionBackfillAllowsSuccessfulEmptyAcknowledgement(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(t, 2, repositoryReviewBackfillRunSpec{
		inspected: []int{0},
	})
	repositoryReviewPrepareAttributionFixtureRuns(t, &fixture)
	runID := fixture.automation.RunIDs[0]
	run := fixture.runs[runID]
	review := run.Steps["find_bugs/review"]
	children := review.Outputs["managed_children"].([]map[string]any)
	structured := children[0]["structured"].(map[string]any)
	structured["reviewedFiles"] = []string{}
	children[0]["structured"] = structured
	review.Outputs["managed_children"] = children
	run.Steps["find_bugs/review"] = review

	prepared, err := prepareRepositoryReviewFileAttributionBackfill(
		t.Context(), fixture.automation, fixture.state,
		repositoryReviewBackfillLoader{runs: fixture.runs, err: map[string]error{}},
	)
	if err != nil || prepared.report.SuccessfulChildren != 1 ||
		prepared.report.AttributionRecords != 0 || len(prepared.attributions) != 0 ||
		prepared.report.Digest == "" {
		t.Fatalf(
			"empty acknowledgement report=%#v attributions=%#v err=%v",
			prepared.report,
			prepared.attributions,
			err,
		)
	}
}

func TestRepositoryReviewLegacyChildUsageModelAcceptsDefaultSelectedReviewer(t *testing.T) {
	child := map[string]any{
		"model": map[string]any{"selected": "fallback-reviewer"},
		"usage": []map[string]any{{
			"model": "provider/concrete-reviewer", "reviewer": "fallback-reviewer",
		}},
	}
	model, err := repositoryReviewLegacyChildUsageModel(child, "default")
	if err != nil || model != "provider/concrete-reviewer" {
		t.Fatalf("default reviewer usage model=%q err=%v", model, err)
	}
	child["usage"] = []map[string]any{{
		"model": "provider/concrete-reviewer", "reviewer": "unrelated-reviewer",
	}}
	if _, err := repositoryReviewLegacyChildUsageModel(child, "default"); !errors.Is(err, repoaudit.ErrInvalidPlan) {
		t.Fatalf("unrelated default reviewer error=%v", err)
	}
}

func TestRepositoryReviewFileAttributionBackfillPrefersEquivalentLiveRecord(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(t, 2, repositoryReviewBackfillRunSpec{
		inspected: []int{0},
	})
	repositoryReviewPrepareAttributionFixtureRuns(t, &fixture)
	loader := repositoryReviewBackfillLoader{runs: fixture.runs, err: map[string]error{}}
	initial, err := prepareRepositoryReviewFileAttributionBackfill(
		t.Context(), fixture.automation, fixture.state, loader,
	)
	if err != nil || len(initial.attributions) != 1 {
		t.Fatalf("initial attributions=%#v err=%v", initial.attributions, err)
	}
	legacy := initial.attributions[0]
	live := legacy
	live.ID = ""
	live.Source = repoaudit.RepositoryReviewFileAttributionSourceLiveCheckpoint
	live.AssignmentID = "current-campaign-assignment"
	live.Model = legacy.UsageModel
	live.ModelAlias = legacy.ReviewerIdentity
	live.Account = "resolved-account"
	live.UsageModel = ""
	live.CompletedAt = live.CompletedAt.Add(-time.Second)
	live, err = repoaudit.NewRepositoryReviewFileAttribution(live)
	if err != nil || live.ID != legacy.ID {
		t.Fatalf("live logical identity=%#v legacy=%#v err=%v", live, legacy, err)
	}
	merged, err := fixture.store.MergeRepositoryReviewFileAttributions(
		t.Context(), repoaudit.MergeRepositoryReviewFileAttributionsRequest{
			Repository: fixture.state.Repository, ExpectedVersion: fixture.state.Version,
			Attributions: []repoaudit.RepositoryReviewFileAttribution{live},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareRepositoryReviewFileAttributionBackfill(
		t.Context(), fixture.automation, merged, loader,
	)
	if err != nil || prepared.report.ExistingAttributionRecords != 1 ||
		len(prepared.attributions) != 1 || !reflect.DeepEqual(prepared.attributions[0], live) {
		t.Fatalf("resolved live attribution=%#v report=%#v err=%v", prepared.attributions, prepared.report, err)
	}
}

func TestRepositoryReviewFileAttributionBackfillRequiresOfflineController(t *testing.T) {
	workspace := t.TempDir()
	store := repoaudit.NewStore(workspace)
	unlock, err := store.LockAutomationController()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := BackfillRepositoryReviewFileAttributions(
		t.Context(), workspace, "rra_offline", RepositoryReviewFileAttributionBackfillOptions{},
	); !errors.Is(err, repoaudit.ErrAutomationControllerLocked) {
		t.Fatalf("controller lock error=%v", err)
	}
}

func TestBackfillRepositoryReviewFileAttributionsPublicLifecycle(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(t, 2, repositoryReviewBackfillRunSpec{
		inspected: []int{0},
	})
	repositoryReviewPrepareAttributionFixtureRuns(t, &fixture)
	repositoryReviewPersistAttributionFixtureRuns(t, fixture)

	dry, err := BackfillRepositoryReviewFileAttributions(
		nil, " "+fixture.workspace+" ", " "+fixture.automation.ID+" ",
		RepositoryReviewFileAttributionBackfillOptions{},
	)
	if err != nil || dry.Applied || dry.Digest == "" || dry.AttributionRecords != 1 {
		t.Fatalf("public dry run=%#v err=%v", dry, err)
	}
	if changed, changedErr := BackfillRepositoryReviewFileAttributions(
		t.Context(), fixture.workspace, fixture.automation.ID,
		RepositoryReviewFileAttributionBackfillOptions{ExpectedDigest: "sha256:changed"},
	); !errors.Is(changedErr, repoaudit.ErrConflict) || changed.Digest != dry.Digest {
		t.Fatalf("changed digest report=%#v err=%v", changed, changedErr)
	}
	if _, missingDigestErr := BackfillRepositoryReviewFileAttributions(
		t.Context(), fixture.workspace, fixture.automation.ID,
		RepositoryReviewFileAttributionBackfillOptions{Apply: true},
	); !errors.Is(missingDigestErr, repoaudit.ErrInvalidPlan) {
		t.Fatalf("missing apply digest error=%v", missingDigestErr)
	}
	applied, err := BackfillRepositoryReviewFileAttributions(
		t.Context(), fixture.workspace, fixture.automation.ID,
		RepositoryReviewFileAttributionBackfillOptions{Apply: true, ExpectedDigest: dry.Digest},
	)
	if err != nil || !applied.Applied || applied.StateVersionAfter != dry.StateVersionBefore+1 {
		t.Fatalf("public apply=%#v err=%v", applied, err)
	}
	replayed, err := BackfillRepositoryReviewFileAttributions(
		t.Context(), fixture.workspace, fixture.automation.ID,
		RepositoryReviewFileAttributionBackfillOptions{Apply: true, ExpectedDigest: dry.Digest},
	)
	if err != nil || !replayed.Applied || replayed.Digest != dry.Digest ||
		replayed.StateVersionAfter != applied.StateVersionAfter ||
		replayed.ExistingAttributionRecords != 1 {
		t.Fatalf("public replay=%#v err=%v", replayed, err)
	}
}

func TestBackfillRepositoryReviewFileAttributionsPublicErrorsAndNoop(t *testing.T) {
	for _, test := range []struct {
		name        string
		workspace   string
		automation  string
		options     RepositoryReviewFileAttributionBackfillOptions
		wantInvalid bool
	}{
		{name: "blank workspace", automation: "rra_test", wantInvalid: true},
		{name: "blank automation", workspace: t.TempDir(), wantInvalid: true},
		{name: "apply without digest", workspace: t.TempDir(), automation: "rra_test", options: RepositoryReviewFileAttributionBackfillOptions{Apply: true}, wantInvalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := BackfillRepositoryReviewFileAttributions(
				t.Context(), test.workspace, test.automation, test.options,
			)
			if test.wantInvalid && !errors.Is(err, repoaudit.ErrInvalidPlan) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	t.Run("missing automation", func(t *testing.T) {
		_, err := BackfillRepositoryReviewFileAttributions(
			t.Context(), t.TempDir(), "rra_missing",
			RepositoryReviewFileAttributionBackfillOptions{},
		)
		if err == nil || !strings.Contains(err.Error(), "automation was not found") {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("canceled automation read", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := BackfillRepositoryReviewFileAttributions(
			ctx, t.TempDir(), "rra_missing", RepositoryReviewFileAttributionBackfillOptions{},
		)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("missing ledger", func(t *testing.T) {
		workspace := t.TempDir()
		store := repoaudit.NewStore(workspace)
		input := testRepositoryReviewAutomation()
		input.ID = "rra_missing_ledger"
		created, err := store.CreateAutomation(t.Context(), input)
		if err != nil {
			t.Fatal(err)
		}
		_, err = BackfillRepositoryReviewFileAttributions(
			t.Context(), workspace, created.ID, RepositoryReviewFileAttributionBackfillOptions{},
		)
		if err == nil || !strings.Contains(err.Error(), "ledger was not found") {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("corrupt ledger", func(t *testing.T) {
		fixture := newRepositoryReviewBackfillFixture(t, 1, repositoryReviewBackfillRunSpec{
			inspected: []int{0},
		})
		entries, err := os.ReadDir(filepath.Join(fixture.workspace, "repository_reviews"))
		if err != nil {
			t.Fatal(err)
		}
		corrupted := false
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "repo_") &&
				strings.HasSuffix(entry.Name(), ".json") &&
				!strings.HasSuffix(entry.Name(), ".summary.json") {
				if writeErr := os.WriteFile(
					filepath.Join(fixture.workspace, "repository_reviews", entry.Name()),
					[]byte("{"), 0o600,
				); writeErr != nil {
					t.Fatal(writeErr)
				}
				corrupted = true
				break
			}
		}
		if !corrupted {
			t.Fatal("repository ledger file was not found")
		}
		_, err = BackfillRepositoryReviewFileAttributions(
			t.Context(), fixture.workspace, fixture.automation.ID,
			RepositoryReviewFileAttributionBackfillOptions{},
		)
		if err == nil || errors.Is(err, repoaudit.ErrInvalidPlan) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("preparation failure", func(t *testing.T) {
		fixture := newRepositoryReviewBackfillFixture(t, 1, repositoryReviewBackfillRunSpec{
			inspected: []int{0},
		})
		_, err := BackfillRepositoryReviewFileAttributions(
			t.Context(), fixture.workspace, fixture.automation.ID,
			RepositoryReviewFileAttributionBackfillOptions{},
		)
		if !errors.Is(err, repoaudit.ErrInvalidPlan) {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("successful empty acknowledgement apply", func(t *testing.T) {
		fixture := newRepositoryReviewBackfillFixture(t, 1, repositoryReviewBackfillRunSpec{
			inspected: []int{0},
		})
		repositoryReviewPrepareAttributionFixtureRuns(t, &fixture)
		run := fixture.runs[fixture.automation.RunIDs[0]]
		review := run.Steps["find_bugs/review"]
		children := review.Outputs["managed_children"].([]map[string]any)
		structured := children[0]["structured"].(map[string]any)
		structured["reviewedFiles"] = []string{}
		children[0]["structured"] = structured
		review.Outputs["managed_children"] = children
		run.Steps["find_bugs/review"] = review
		repositoryReviewPersistAttributionFixtureRuns(t, fixture)
		dry, err := BackfillRepositoryReviewFileAttributions(
			t.Context(), fixture.workspace, fixture.automation.ID,
			RepositoryReviewFileAttributionBackfillOptions{},
		)
		if err != nil || dry.AttributionRecords != 0 {
			t.Fatalf("dry=%#v err=%v", dry, err)
		}
		applied, err := BackfillRepositoryReviewFileAttributions(
			t.Context(), fixture.workspace, fixture.automation.ID,
			RepositoryReviewFileAttributionBackfillOptions{Apply: true, ExpectedDigest: dry.Digest},
		)
		if err != nil || !applied.Applied || applied.StateVersionAfter != applied.StateVersionBefore {
			t.Fatalf("noop apply=%#v err=%v", applied, err)
		}
	})
}

func repositoryReviewPrepareAttributionFixtureRuns(
	t *testing.T,
	fixture *repositoryReviewBackfillFixture,
) {
	t.Helper()
	for runID, run := range fixture.runs {
		review := run.Steps["find_bugs/review"]
		children := review.Outputs["managed_children"].([]map[string]any)
		for index := range children {
			children[index]["index"] = index + 1
			if valid, _ := children[index]["valid"].(bool); valid {
				children[index]["usage"] = []map[string]any{{
					"model": "provider/review-a", "reviewer": "review-a",
				}}
			}
		}
		review.Outputs["agent_id"] = "main"
		review.Outputs["managed_children"] = children
		run.Steps["find_bugs/review"] = review
		fixture.runs[runID] = run
	}
}

func repositoryReviewPersistAttributionFixtureRuns(
	t *testing.T,
	fixture repositoryReviewBackfillFixture,
) {
	t.Helper()
	for _, runID := range fixture.automation.RunIDs {
		if run := fixture.runs[runID]; run != nil {
			if err := fixture.runStore.DeleteRun(t.Context(), runID); err != nil {
				t.Fatal(err)
			}
			if err := fixture.runStore.CreateRun(t.Context(), run); err != nil {
				t.Fatal(err)
			}
		}
	}
}
