package repoaudit

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryReviewAssignmentCampaignBindingAndMergeCoverage(t *testing.T) {
	profileHash := "sha256:" + strings.Repeat("a", 64)
	catalog := repositoryReviewAssignmentCatalogForTest(t, profileHash)
	campaignID := NewRepositoryReviewCampaignID()
	commit := strings.Repeat("b", 40)
	scopeDigest := "sha256:" + strings.Repeat("c", 64)
	state := RepositoryState{CurrentCampaign: &RepositoryReviewCampaignCoverage{
		ID: campaignID, CommitSHA: commit, InventoryHash: "inventory",
		ProfileHash: profileHash, ScopeDigest: scopeDigest,
		RequiredAssignments: 4, SelectedFiles: 3,
		Paths: map[string]RepositoryReviewCampaignPathCoverage{
			"complete.go":  {Completed: true},
			"inspected.go": {Inspected: true},
			"binary.bin":   {Unsupported: true},
		},
	}}
	changed, err := bindRepositoryReviewCampaignAssignmentCatalog(
		&state, campaignID, commit, "inventory", profileHash, scopeDigest, catalog, 3,
	)
	if err != nil || !changed || !state.CurrentCampaign.Paths["complete.go"].Completed ||
		state.CurrentCampaign.Paths["complete.go"].AssignmentBits == "" ||
		state.CurrentCampaign.Paths["inspected.go"] != (RepositoryReviewCampaignPathCoverage{}) ||
		!state.CurrentCampaign.Paths["binary.bin"].Unsupported {
		t.Fatalf("catalog binding = %#v changed=%v err=%v", state.CurrentCampaign, changed, err)
	}
	if _, exists := state.CurrentCampaign.Paths["inspected.go"]; exists {
		t.Fatal("ambiguous inspection-only path survived assignment binding")
	}
	changed, err = bindRepositoryReviewCampaignAssignmentCatalog(
		&state, campaignID, commit, "inventory", profileHash, scopeDigest, catalog, 3,
	)
	if err != nil || changed {
		t.Fatalf("catalog replay changed=%v err=%v", changed, err)
	}
	otherCatalog := append([]RepositoryReviewAssignment(nil), catalog...)
	otherCatalog[0], err = NewRepositoryReviewAssignment(
		otherCatalog[0].FocusID, "review-b", otherCatalog[0].PromptRevision, profileHash, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bindRepositoryReviewCampaignAssignmentCatalog(
		&state, campaignID, commit, "inventory", profileHash, scopeDigest, otherCatalog, 3,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("catalog drift error = %v", err)
	}
	wrongProfile := append([]RepositoryReviewAssignment(nil), catalog...)
	wrongProfile[0].ProfileHash = "sha256:" + strings.Repeat("d", 64)
	if _, err := bindRepositoryReviewCampaignAssignmentCatalog(
		&state, campaignID, commit, "inventory", profileHash, scopeDigest, wrongProfile, 3,
	); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("catalog profile error = %v", err)
	}

	coverage := state.CurrentCampaign
	if _, err := mergeRepositoryReviewCampaignPath(nil, "a.go", RepositoryReviewCampaignPathCoverage{
		Completed: true,
	}); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("nil merge error = %v", err)
	}
	changed, err = mergeRepositoryReviewCampaignPath(
		coverage, "new.go", RepositoryReviewCampaignPathCoverage{Completed: true},
	)
	if err != nil || !changed || !coverage.Paths["new.go"].Completed {
		t.Fatalf("required completion merge changed=%v path=%#v err=%v", changed, coverage.Paths["new.go"], err)
	}
	changed, err = mergeRepositoryReviewCampaignPath(
		coverage, "new.go", RepositoryReviewCampaignPathCoverage{Completed: true},
	)
	if err != nil || changed {
		t.Fatalf("completion replay changed=%v err=%v", changed, err)
	}
	if _, err := mergeRepositoryReviewCampaignPath(
		coverage, "new.go", RepositoryReviewCampaignPathCoverage{Unsupported: true},
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("unsupported-after-credit error = %v", err)
	}
	changed, err = mergeRepositoryReviewCampaignPath(
		coverage, "terminal.bin", RepositoryReviewCampaignPathCoverage{Unsupported: true},
	)
	if err != nil || !changed {
		t.Fatalf("unsupported merge changed=%v err=%v", changed, err)
	}
	changed, err = mergeRepositoryReviewCampaignPath(
		coverage, "terminal.bin", RepositoryReviewCampaignPathCoverage{Unsupported: true},
	)
	if err != nil || changed {
		t.Fatalf("unsupported replay changed=%v err=%v", changed, err)
	}
	coverage.Paths["malformed.go"] = RepositoryReviewCampaignPathCoverage{AssignmentBits: "%%%"}
	if _, err := mergeRepositoryReviewCampaignPath(
		coverage, "malformed.go", RepositoryReviewCampaignPathCoverage{Completed: true},
	); err == nil {
		t.Fatal("malformed current assignment bits merged")
	}
	delete(coverage.Paths, "malformed.go")
	if _, err := mergeRepositoryReviewCampaignPath(
		coverage, "bad-update.go", RepositoryReviewCampaignPathCoverage{AssignmentBits: "%%%"},
	); err == nil {
		t.Fatal("malformed update assignment bits merged")
	}

	legacy := &RepositoryReviewCampaignCoverage{
		ID: campaignID, CommitSHA: commit, InventoryHash: "inventory",
		ProfileHash: profileHash, ScopeDigest: scopeDigest,
		RequiredAssignments: 1, SelectedFiles: 1,
		Paths: map[string]RepositoryReviewCampaignPathCoverage{},
	}
	changed, err = mergeRepositoryReviewCampaignPath(
		legacy, "legacy.go", RepositoryReviewCampaignPathCoverage{Inspected: true},
	)
	if err != nil || !changed {
		t.Fatalf("legacy merge changed=%v err=%v", changed, err)
	}
	if _, err := mergeRepositoryReviewCampaignPath(
		legacy, "legacy.go", RepositoryReviewCampaignPathCoverage{Unsupported: true},
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("legacy terminal conflict error = %v", err)
	}
}

func TestRepositoryReviewAssignmentPlanningAndStateValidationCoverage(t *testing.T) {
	if _, err := NewStore(t.TempDir()).PlanAssignmentsForCampaign(
		context.Background(), "owner/repo", strings.Repeat("a", 40), "inventory", "profile",
		NewRepositoryReviewCampaignID(), nil, nil, false, 1, true,
	); err == nil {
		t.Fatal("assignment planner accepted an empty catalog")
	}

	fixture := newAssignmentCoverageFixture(t, 1, 1)
	state, _, err := fixture.store.Get(fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	state.CurrentCampaign.Paths[fixture.files[0].Path] = RepositoryReviewCampaignPathCoverage{
		Unsupported: true,
	}
	delete(state.Unsupported, fixture.files[0].Path)
	state.Version++
	state.ReviewVersion++
	state.UpdatedAt = repositoryAuditTestNow
	if err := fixture.store.save(&state); err != nil {
		t.Fatal(err)
	}
	terminal, err := fixture.store.PlanAssignmentsForCampaign(
		context.Background(), fixture.repository, fixture.plan.CommitSHA, fixture.plan.InventoryHash,
		fixture.plan.ProfileHash, fixture.campaignID, fixture.catalog, fixture.files, true, 1, true,
	)
	if err != nil || len(terminal.UnsupportedFiles) != 1 ||
		terminal.UnsupportedFiles[0].Reason != "campaign_terminal" {
		t.Fatalf("campaign terminal plan = %#v err=%v", terminal, err)
	}

	malformed := state
	malformed.CurrentCampaign = cloneRepositoryReviewCampaignCoverageForTest(state.CurrentCampaign)
	malformed.CurrentCampaign.Paths[fixture.files[0].Path] = RepositoryReviewCampaignPathCoverage{
		AssignmentBits: "%%%", Inspected: true,
	}
	unsafe := fixture.store
	unsafe.loadForTest = func(string) (RepositoryState, error) { return malformed, nil }
	if _, err := unsafe.PlanAssignmentsForCampaign(
		context.Background(), fixture.repository, fixture.plan.CommitSHA, fixture.plan.InventoryHash,
		fixture.plan.ProfileHash, fixture.campaignID, fixture.catalog, fixture.files, false, 1, true,
	); err == nil {
		t.Fatal("planner accepted malformed durable assignment bits")
	}

	defaultAssignment, err := NewRepositoryReviewAssignment(
		RepositoryReviewFocusCorrectnessState, "default", "prompt", fixture.plan.ProfileHash, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	defaultRepository := "owner/default-reviewer-plan"
	defaultCampaign := NewRepositoryReviewCampaignID()
	defaultStore := NewStore(t.TempDir())
	if _, err := defaultStore.BeginCampaign(context.Background(), BeginCampaignRequest{
		Repository: defaultRepository, CampaignID: defaultCampaign, CommitSHA: fixture.plan.CommitSHA,
	}); err != nil {
		t.Fatal(err)
	}
	defaultPlan, err := defaultStore.PlanAssignmentsForCampaign(
		context.Background(), defaultRepository, fixture.plan.CommitSHA, "inventory",
		fixture.plan.ProfileHash, defaultCampaign, []RepositoryReviewAssignment{defaultAssignment},
		fixture.files, false, 1, true,
	)
	if err != nil || len(defaultPlan.AssignmentPlans) != 1 || defaultPlan.AssignmentPlans[0].Reviewer != "" {
		t.Fatalf("default reviewer plan = %#v err=%v", defaultPlan.AssignmentPlans, err)
	}

	invalidCatalogPlan := fixture.plan
	invalidCatalogPlan.RequiredAssignments++
	invalidCatalogPlan.ID = planDigest(invalidCatalogPlan)
	if _, err := validateRepositoryReviewCampaignPlan(invalidCatalogPlan); err == nil {
		t.Fatal("plan with inconsistent assignment count validated")
	}
	invalidAssignmentPlan := fixture.plan
	invalidAssignmentPlan.AssignmentPlans[0].Files = nil
	invalidAssignmentPlan.ID = planDigest(invalidAssignmentPlan)
	if _, err := validateRepositoryReviewCampaignPlan(invalidAssignmentPlan); err == nil {
		t.Fatal("plan with invalid assignment scope validated")
	}
	legacyPlan := fixture.plan
	legacyPlan.AssignmentCatalog = nil
	legacyPlan.ID = planDigest(legacyPlan)
	if _, err := validateRepositoryReviewCampaignPlan(legacyPlan); err == nil {
		t.Fatal("plan with assignment plans but no catalog validated")
	}

	invalidState := state
	invalidState.ActiveReviewRun = &RepositoryReviewActiveRun{ID: "bad"}
	if err := validateState(invalidState); err == nil {
		t.Fatal("state with invalid active run validated")
	}
	invalidState = state
	invalidState.CampaignHistory = maps.Clone(state.CampaignHistory)
	delete(invalidState.CampaignHistory, invalidState.CurrentCampaign.ID)
	if err := validateState(invalidState); err == nil {
		t.Fatal("state without current campaign history validated")
	}
	invalidState = state
	invalidState.Runs = append(invalidState.Runs, ReviewRun{
		ID: "checkpoint-run", CampaignID: fixture.campaignID,
		CommitSHA: fixture.plan.CommitSHA, ProfileHash: fixture.plan.ProfileHash,
		ScopeDigest:       state.CurrentCampaign.ScopeDigest,
		CheckpointDigests: map[string]string{"a": "sha256:" + strings.Repeat("a", 64)},
	})
	if err := validateState(invalidState); err == nil {
		t.Fatal("run with missing checkpoint scope validated")
	}
}

func TestRepositoryReviewAssignmentStoreFailureCoverage(t *testing.T) {
	fixture := newAssignmentCoverageFixture(t, 1, 1)
	sentinel := errors.New("injected assignment load failure")
	failing := fixture.store
	failing.loadForTest = func(string) (RepositoryState, error) {
		return RepositoryState{}, sentinel
	}
	begin := BeginRepositoryReviewRunRequest{
		Plan: fixture.plan, RunID: "failure-run", ReviewableFiles: fixture.files,
	}
	if _, err := failing.BeginRepositoryReviewRun(context.Background(), begin); !errors.Is(err, sentinel) {
		t.Fatalf("begin load error = %v", err)
	}
	assignmentPlan := fixture.plan.AssignmentPlans[0]
	verify := VerifyRepositoryReviewAssignmentRequest{
		Repository: fixture.repository, RunID: "failure-run",
		AssignmentID: assignmentPlan.AssignmentID, Files: assignmentPlan.Files,
	}
	if err := failing.VerifyRepositoryReviewAssignment(context.Background(), verify); !errors.Is(err, sentinel) {
		t.Fatalf("verify load error = %v", err)
	}
	checkpoint := assignmentCoverageCheckpoint(fixture, "failure-run", 0, fixture.files)
	if _, err := failing.CheckpointRepositoryReviewAssignment(
		context.Background(), checkpoint,
	); !errors.Is(err, sentinel) {
		t.Fatalf("checkpoint load error = %v", err)
	}
	if _, err := failing.FinalizeRepositoryReviewRun(
		context.Background(), FinalizeRepositoryReviewRunRequest{
			Plan: fixture.plan, RunID: "failure-run",
		},
	); !errors.Is(err, sentinel) {
		t.Fatalf("finalize load error = %v", err)
	}
	if _, err := failing.InterruptRepositoryReviewRun(
		context.Background(), fixture.repository, "failure-run",
	); !errors.Is(err, sentinel) {
		t.Fatalf("interrupt load error = %v", err)
	}
	if _, _, err := failing.InterruptAbandonedRepositoryReviewRun(
		context.Background(), fixture.repository,
	); !errors.Is(err, sentinel) {
		t.Fatalf("abandoned load error = %v", err)
	}

	state, _, err := fixture.store.Get(fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	missingCampaign := fixture.store
	missingCampaign.loadForTest = func(string) (RepositoryState, error) {
		candidate := state
		candidate.CurrentCampaign = nil
		return candidate, nil
	}
	if _, err := missingCampaign.BeginRepositoryReviewRun(
		context.Background(), begin,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing campaign begin error = %v", err)
	}
	outside := begin
	outside.ReviewableFiles = []FileRef{repositoryAuditTestFile("outside.go", "f", 10)}
	if _, err := fixture.store.BeginRepositoryReviewRun(
		context.Background(), outside,
	); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("outside begin error = %v", err)
	}

	active, err := fixture.store.BeginRepositoryReviewRun(context.Background(), begin)
	if err != nil {
		t.Fatal(err)
	}
	active.CurrentCampaign.Paths[fixture.files[0].Path] = RepositoryReviewCampaignPathCoverage{
		AssignmentBits: "%%%", Inspected: true,
	}
	malformedState := active
	malformed := fixture.store
	malformed.loadForTest = func(string) (RepositoryState, error) { return malformedState, nil }
	if err := malformed.VerifyRepositoryReviewAssignment(context.Background(), verify); err == nil {
		t.Fatal("verify accepted malformed assignment bits")
	}
	malformedState.ActiveReviewRun = nil
	if _, err := malformed.BeginRepositoryReviewRun(context.Background(), begin); err == nil {
		t.Fatal("begin accepted malformed assignment bits")
	}
	malformedState.ActiveReviewRun = active.ActiveReviewRun
	if _, err := malformed.CheckpointRepositoryReviewAssignment(
		context.Background(), checkpoint,
	); err == nil {
		t.Fatal("checkpoint accepted malformed assignment bits")
	}
	if run, err := repositoryReviewActiveRunFromPlan(
		fixture.plan, "nil-reviewable", nil, repositoryAuditTestNow,
	); err != nil || len(run.Reservations) != len(fixture.plan.AssignmentPlans) {
		t.Fatalf("nil reviewable run = %#v err=%v", run, err)
	}

	rootFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(rootFile, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	blocked := fixture.store
	blocked.root = rootFile
	if _, err := blocked.BeginRepositoryReviewRun(context.Background(), begin); err == nil {
		t.Fatal("begin ignored lock-root failure")
	}
	if err := blocked.VerifyRepositoryReviewAssignment(context.Background(), verify); err == nil {
		t.Fatal("verify ignored lock-root failure")
	}
	if _, err := blocked.CheckpointRepositoryReviewAssignment(
		context.Background(), checkpoint,
	); err == nil {
		t.Fatal("checkpoint ignored lock-root failure")
	}
	if _, err := blocked.FinalizeRepositoryReviewRun(
		context.Background(), FinalizeRepositoryReviewRunRequest{
			Plan: fixture.plan, RunID: "failure-run",
		},
	); err == nil {
		t.Fatal("finalize ignored lock-root failure")
	}
	if _, err := blocked.InterruptRepositoryReviewRun(
		context.Background(), fixture.repository, "failure-run",
	); err == nil {
		t.Fatal("interrupt ignored lock-root failure")
	}
	if _, _, err := blocked.InterruptAbandonedRepositoryReviewRun(
		context.Background(), fixture.repository,
	); err == nil {
		t.Fatal("abandoned interrupt ignored lock-root failure")
	}

	invalidPlan := fixture.plan
	invalidPlan.RequiredAssignments++
	invalidPlan.ID = planDigest(invalidPlan)
	if _, err := fixture.store.BeginRepositoryReviewRun(
		context.Background(), BeginRepositoryReviewRunRequest{
			Plan: invalidPlan, RunID: "invalid-plan", ReviewableFiles: fixture.files,
		},
	); err == nil {
		t.Fatal("begin accepted structurally invalid campaign plan")
	}
}
