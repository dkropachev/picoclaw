package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type repositoryReviewThirdErrorContext struct {
	calls atomic.Int32
}

func (ctx *repositoryReviewThirdErrorContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (ctx *repositoryReviewThirdErrorContext) Done() <-chan struct{}       { return nil }
func (ctx *repositoryReviewThirdErrorContext) Value(any) any               { return nil }
func (ctx *repositoryReviewThirdErrorContext) Err() error {
	if ctx.calls.Add(1) >= 3 {
		return context.Canceled
	}
	return nil
}

//nolint:govet // Boundary assertions intentionally reuse err in short scopes.
func TestRepositoryReviewAssignmentControllerCampaignBoundaries(t *testing.T) {
	store := repoaudit.NewStore(t.TempDir())
	automation := testRepositoryReviewAutomation()
	automation.ID = "rra_assignment_campaign_coverage"
	automation.Repository = "owner/assignment-coverage"
	automation.ModelStats = map[string]repoaudit.RepositoryReviewModelStats{
		"cheap": {Findings: 3, ReviewedFiles: 5},
	}
	created, err := store.CreateAutomation(t.Context(), automation)
	if err != nil {
		t.Fatal(err)
	}
	controller := &repositoryReviewController{}
	if _, err := controller.ensureRepositoryReviewCampaign(
		t.Context(), store, &config.Config{}, created, "bad", "start",
	); !errors.Is(err, repoaudit.ErrInvalidAutomation) {
		t.Fatalf("invalid commit error = %v", err)
	}
	commit := strings.Repeat("a", 40)
	first, err := controller.ensureRepositoryReviewCampaign(
		t.Context(), store, &config.Config{}, created, commit, "start",
	)
	if err != nil || first.CampaignID == "" {
		t.Fatalf("first campaign = %#v, %v", first, err)
	}
	replayed, err := controller.ensureRepositoryReviewCampaign(
		t.Context(), store, &config.Config{}, first, commit, "resume",
	)
	if err != nil || replayed.CampaignID != first.CampaignID {
		t.Fatalf("campaign replay = %#v, %v", replayed, err)
	}
	conflicting := replayed
	conflicting.CampaignID = repoaudit.NewRepositoryReviewCampaignID()
	if _, err := controller.ensureRepositoryReviewCampaign(
		t.Context(), store, &config.Config{}, conflicting, commit, "resume",
	); !errors.Is(err, repoaudit.ErrConflict) {
		t.Fatalf("campaign conflict error = %v", err)
	}
	secondCommit := strings.Repeat("b", 40)
	second, err := controller.ensureRepositoryReviewCampaign(
		t.Context(), store, &config.Config{}, replayed, secondCommit, "resume",
	)
	if err != nil || second.CampaignID == first.CampaignID ||
		second.ResolvedCommitSHA != secondCommit || second.Progress != (repoaudit.RepositoryReviewProgress{}) ||
		second.ModelStats["cheap"].Findings != 0 || second.ModelStats["cheap"].ReviewedFiles != 0 {
		t.Fatalf("second campaign = %#v, %v", second, err)
	}
	pendingWithoutState := created
	pendingWithoutState.ID = "rra_missing_pending"
	pendingWithoutState.Repository = "owner/missing-pending"
	pendingWithoutState.CampaignRecoveryPending = true
	if _, err := controller.ensureRepositoryReviewCampaign(
		t.Context(), store, &config.Config{}, pendingWithoutState, commit, "resume",
	); !errors.Is(err, repoaudit.ErrConflict) {
		t.Fatalf("missing pending recovery error = %v", err)
	}
	if _, err := (*repositoryReviewController)(nil).resolveRepositoryReviewCampaignProfile(
		context.Background(), &config.Config{}, created,
	); err == nil {
		t.Fatal("nil controller resolved a review profile")
	}
	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	runtimeConfig, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}
	runtimeConfig.ModelList[0].APIBase = "http://127.0.0.1:1/v1"
	if err := config.SaveConfig(handler.configPath, runtimeConfig); err != nil {
		t.Fatal(err)
	}
	resolved, err := handler.repositoryReviewControllerInstance().resolveRepositoryReviewCampaignProfile(
		t.Context(), runtimeConfig, created,
	)
	if err != nil || resolved.Revision == "" || len(resolved.ReviewerModels) == 0 {
		t.Fatalf("resolved campaign profile = %#v, %v", resolved, err)
	}
}

//nolint:govet // Boundary assertions intentionally reuse err in short scopes.
func TestRepositoryReviewAssignmentCampaignRecoveryExecutionBranches(t *testing.T) {
	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("d", 40)
	input := testRepositoryReviewAutomation()
	input.Repository = "owner/assignment-recovery-execution"
	input.CampaignID = repoaudit.NewRepositoryReviewCampaignID()
	input.CampaignRecoveryPending = true
	input.ResolvedCommitSHA = commit
	input.ScopeSelection = &repoaudit.RepositoryReviewScopeSelection{IncludePrefixes: []string{"pkg"}}
	input.ScopePlan = repoaudit.RepositoryReviewScopePlan{
		CommitSHA: commit, PolicyHash: strings.Repeat("a", 64), Hash: strings.Repeat("b", 64),
		Summary: "Recovered scope",
	}
	input.RunIDs = []string{"legacy-run"}
	input.StartedAt = time.Now().Add(-time.Hour)
	input.Status = repoaudit.RepositoryReviewAutomationPaused
	input.PauseReason = repoaudit.RepositoryReviewPauseServiceRestart
	input.PauseDetail = "resume legacy assignment recovery"
	automation, err := store.CreateAutomation(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginCampaign(t.Context(), repoaudit.BeginCampaignRequest{
		Repository: repoaudit.CanonicalRepositoryIdentity(input.Repository),
		CampaignID: input.CampaignID, CommitSHA: commit,
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}
	previousRunners := newWorkflowRuntimeRunners
	t.Cleanup(func() { newWorkflowRuntimeRunners = previousRunners })
	newWorkflowRuntimeRunners = func(string) workflowRuntimeRunners {
		return workflowRuntimeRunners{Agents: &repositoryReviewRecoveryProfileRunner{
			profile: workflows.RepositoryReviewModelProfile{
				Revision: "sha256:assignment-recovery", ReviewerModels: []string{"cheap"},
				MaxContentBytes: 65536,
			},
		}}
	}
	controller := &repositoryReviewController{handler: handler}
	controller.recoverCampaign = func(
		context.Context,
		repoaudit.Store,
		string,
		repoaudit.RepositoryReviewAutomation,
		string,
		workflows.RepositoryReviewModelProfile,
	) (repoaudit.RepositoryReviewAutomation, error) {
		recovered := automation
		recovered.CampaignRecoveryPending = false
		return recovered, nil
	}
	recovered, err := controller.ensureRepositoryReviewCampaign(
		t.Context(), store, cfg, automation, commit, "resume",
	)
	if err != nil || recovered.CampaignRecoveryPending {
		t.Fatalf("recovered campaign = %#v err=%v", recovered, err)
	}

	sentinel := errors.New("injected assignment recovery failure")
	controller.recoverCampaign = func(
		context.Context,
		repoaudit.Store,
		string,
		repoaudit.RepositoryReviewAutomation,
		string,
		workflows.RepositoryReviewModelProfile,
	) (repoaudit.RepositoryReviewAutomation, error) {
		return repoaudit.RepositoryReviewAutomation{}, sentinel
	}
	if _, err := controller.ensureRepositoryReviewCampaign(
		t.Context(), store, cfg, automation, commit, "resume",
	); !errors.Is(err, sentinel) {
		t.Fatalf("recovery failure = %v", err)
	}
	controller.recoverCampaign = nil
	if _, err := controller.ensureRepositoryReviewCampaign(
		t.Context(), store, cfg, automation, commit, "resume",
	); err == nil || !strings.Contains(err.Error(), "recovery is unavailable") {
		t.Fatalf("missing recovery adapter error = %v", err)
	}

	newWorkflowRuntimeRunners = func(string) workflowRuntimeRunners {
		return workflowRuntimeRunners{Agents: fakeWorkflowRuntimeRunner{}}
	}
	if _, err := controller.resolveRepositoryReviewCampaignProfile(t.Context(), cfg, automation); err == nil {
		t.Fatal("non-profile runtime resolved an assignment campaign profile")
	}
}

func TestRepositoryReviewAssignmentCampaignResumeWithoutLegacyState(t *testing.T) {
	store := repoaudit.NewStore(t.TempDir())
	automation := testRepositoryReviewAutomation()
	automation.ID = "rra_assignment_resume_without_state"
	automation.Repository = "owner/assignment-resume-without-state"
	automation.RunIDs = []string{"unknown-run"}
	created, err := store.CreateAutomation(t.Context(), automation)
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("e", 40)
	updated, err := (&repositoryReviewController{}).ensureRepositoryReviewCampaign(
		t.Context(), store, &config.Config{}, created, commit, "resume",
	)
	if err != nil || updated.CampaignID == "" || updated.ResolvedCommitSHA != commit {
		t.Fatalf("resume without state = %#v err=%v", updated, err)
	}
}

func TestRepositoryReviewAssignmentAdmissionFailureBranches(t *testing.T) {
	type admissionFixture struct {
		handler    *Handler
		store      repoaudit.Store
		controller *repositoryReviewController
		automation repoaudit.RepositoryReviewAutomation
		workspace  string
		commit     string
	}
	prepare := func(t *testing.T, suffix string) admissionFixture {
		t.Helper()
		handler, _, workspace := newRepositoryReviewAutomationTestHandler(t)
		t.Cleanup(handler.Shutdown)
		store, err := handler.repositoryReviewStore()
		if err != nil {
			t.Fatal(err)
		}
		input := testRepositoryReviewAutomation()
		input.Repository = "https://github.com/acme/assignment-" + suffix + ".git"
		automation, err := store.CreateAutomation(t.Context(), input)
		if err != nil {
			t.Fatal(err)
		}
		commit := strings.Repeat("a", 40)
		controller := handler.repositoryReviewControllerInstance()
		controller.resolveCommit = func(
			context.Context,
			*config.Config,
			repoaudit.RepositoryReviewAutomation,
			string,
		) (string, error) {
			return commit, nil
		}
		return admissionFixture{
			handler: handler, store: store, controller: controller,
			automation: automation, workspace: workspace, commit: commit,
		}
	}

	t.Run("budget persistence", func(t *testing.T) {
		fixture := prepare(t, "budget-failure")
		sentinel := errors.New("injected budget reset failure")
		calls := 0
		fixture.controller.update = func(
			ctx context.Context,
			store repoaudit.Store,
			id string,
			version int64,
			mutate func(*repoaudit.RepositoryReviewAutomation) error,
		) (repoaudit.RepositoryReviewAutomation, error) {
			calls++
			if calls == 2 {
				return repoaudit.RepositoryReviewAutomation{}, sentinel
			}
			return updateRepositoryReviewAutomation(ctx, store, id, version, mutate)
		}
		if _, err := fixture.controller.startAutomation(
			t.Context(), fixture.automation.ID, fixture.automation.Version, true, "start",
		); !errors.Is(err, sentinel) {
			t.Fatalf("budget persistence error = %v", err)
		}
	})

	t.Run("abandoned reservation interrupt", func(t *testing.T) {
		fixture := prepare(t, "interrupt-failure")
		calls := 0
		lockPath := filepath.Join(fixture.workspace, "repository_reviews.lock")
		fixture.controller.update = func(
			ctx context.Context,
			store repoaudit.Store,
			id string,
			version int64,
			mutate func(*repoaudit.RepositoryReviewAutomation) error,
		) (repoaudit.RepositoryReviewAutomation, error) {
			calls++
			updated, err := updateRepositoryReviewAutomation(ctx, store, id, version, mutate)
			if err == nil && calls == 1 {
				if removeErr := os.Remove(lockPath); removeErr != nil && !os.IsNotExist(removeErr) {
					t.Fatal(removeErr)
				}
				if mkdirErr := os.Mkdir(lockPath, 0o700); mkdirErr != nil {
					t.Fatal(mkdirErr)
				}
			}
			return updated, err
		}
		if _, err := fixture.controller.startAutomation(
			t.Context(), fixture.automation.ID, fixture.automation.Version, false, "start",
		); err == nil {
			t.Fatal("irregular assignment lock was accepted")
		}
		if err := os.RemoveAll(lockPath); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("ledger read", func(t *testing.T) {
		fixture := prepare(t, "ledger-failure")
		calls := 0
		fixture.controller.update = func(
			ctx context.Context,
			store repoaudit.Store,
			id string,
			version int64,
			mutate func(*repoaudit.RepositoryReviewAutomation) error,
		) (repoaudit.RepositoryReviewAutomation, error) {
			calls++
			updated, err := updateRepositoryReviewAutomation(ctx, store, id, version, mutate)
			if err == nil && calls == 2 {
				identity := repoaudit.CanonicalRepositoryIdentity(updated.Repository)
				filename := strings.Replace(repoaudit.RepositoryID(identity), "rrp_", "repo_", 1) + ".json"
				if writeErr := os.WriteFile(
					filepath.Join(fixture.workspace, "repository_reviews", filename), []byte("{"), 0o600,
				); writeErr != nil {
					t.Fatal(writeErr)
				}
			}
			return updated, err
		}
		if _, err := fixture.controller.startAutomation(
			t.Context(), fixture.automation.ID, fixture.automation.Version, false, "start",
		); err == nil {
			t.Fatal("corrupt assignment ledger was accepted")
		}
	})

	t.Run("campaign authorization", func(t *testing.T) {
		fixture := prepare(t, "authorization-failure")
		calls := 0
		fixture.controller.update = func(
			ctx context.Context,
			store repoaudit.Store,
			id string,
			version int64,
			mutate func(*repoaudit.RepositoryReviewAutomation) error,
		) (repoaudit.RepositoryReviewAutomation, error) {
			calls++
			updated, err := updateRepositoryReviewAutomation(ctx, store, id, version, mutate)
			if err == nil && calls == 2 {
				identity := repoaudit.CanonicalRepositoryIdentity(updated.Repository)
				_, err = store.BeginCampaign(ctx, repoaudit.BeginCampaignRequest{
					Repository: identity, CampaignID: updated.CampaignID,
					CommitSHA: strings.Repeat("b", 40), Exact: true,
				})
			}
			return updated, err
		}
		if _, err := fixture.controller.startAutomation(
			t.Context(), fixture.automation.ID, fixture.automation.Version, false, "start",
		); !errors.Is(err, repoaudit.ErrConflict) {
			t.Fatalf("campaign authorization error = %v", err)
		}
	})

	t.Run("late controller shutdown", func(t *testing.T) {
		fixture := prepare(t, "late-shutdown")
		calls := 0
		fixture.controller.update = func(
			ctx context.Context,
			store repoaudit.Store,
			id string,
			version int64,
			mutate func(*repoaudit.RepositoryReviewAutomation) error,
		) (repoaudit.RepositoryReviewAutomation, error) {
			calls++
			updated, err := updateRepositoryReviewAutomation(ctx, store, id, version, mutate)
			if err == nil && calls == 2 {
				fixture.controller.stopped = true
			}
			return updated, err
		}
		if _, err := fixture.controller.startAutomation(
			t.Context(), fixture.automation.ID, fixture.automation.Version, false, "start",
		); !errors.Is(err, context.Canceled) {
			t.Fatalf("late controller shutdown error = %v", err)
		}
	})
}

func TestLoadRepositoryReviewOutcomeUsesAssignmentCampaign(t *testing.T) {
	ctx := t.Context()
	store := repoaudit.NewStore(t.TempDir())
	repository := "owner/assignment-outcome"
	commit := strings.Repeat("c", 40)
	profileHash := "sha256:" + strings.Repeat("d", 64)
	campaignID := repoaudit.NewRepositoryReviewCampaignID()
	code := repoaudit.FileRef{
		Path: "code.go", BlobSHA: strings.Repeat("e", 40), SizeBytes: 10,
		Category: "code", Mode: "100644",
	}
	binary := repoaudit.FileRef{
		Path: "asset.bin", BlobSHA: strings.Repeat("f", 40), SizeBytes: 20,
		Category: "binary", Mode: "100644",
	}
	if _, err := store.BeginCampaign(ctx, repoaudit.BeginCampaignRequest{
		Repository: repository, CampaignID: campaignID, CommitSHA: commit,
	}); err != nil {
		t.Fatal(err)
	}
	catalog := make([]repoaudit.RepositoryReviewAssignment, 0, 4)
	for _, focusID := range repoaudit.RepositoryReviewFocusIDs() {
		assignment, err := repoaudit.NewRepositoryReviewAssignment(
			focusID, "review-a", "prompt-v1", profileHash, true,
		)
		if err != nil {
			t.Fatal(err)
		}
		catalog = append(catalog, assignment)
	}
	plan, err := store.PlanAssignmentsForCampaign(
		ctx, repository, commit, "inventory", profileHash, campaignID,
		catalog, []repoaudit.FileRef{code, binary}, false, 2, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginRepositoryReviewRun(ctx, repoaudit.BeginRepositoryReviewRunRequest{
		Plan: plan, RunID: "assignment-outcome-run", ReviewableFiles: []repoaudit.FileRef{code},
	}); err != nil {
		t.Fatal(err)
	}
	for index, assignmentPlan := range plan.AssignmentPlans {
		findings := []repoaudit.FindingCandidate(nil)
		if index == 0 {
			findings = []repoaudit.FindingCandidate{{
				Severity: "high", Title: "campaign finding", Symbol: "Run", File: code.Path,
				Evidence: "validated branch", Impact: "observable failure",
				Validation: repoaudit.Validation{Status: "confirmed", Summary: "confirmed"},
			}}
		}
		if _, err := store.CheckpointRepositoryReviewAssignment(
			ctx, repoaudit.CheckpointRepositoryReviewAssignmentRequest{
				Plan: plan, RunID: "assignment-outcome-run", AssignmentID: assignmentPlan.AssignmentID,
				Digest:            "sha256:" + strings.Repeat(string(rune('1'+index)), 64),
				AcknowledgedFiles: []repoaudit.FileRef{code},
				Observation: repoaudit.Observation{
					Model: "review-a", Reviewer: assignmentPlan.FocusID,
					ScopeFiles: []repoaudit.FileRef{code}, Findings: findings,
					RawDigest: "sha256:" + strings.Repeat("9", 64),
				},
			},
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.FinalizeRepositoryReviewRun(
		ctx, repoaudit.FinalizeRepositoryReviewRunRequest{
			Plan: plan, RunID: "assignment-outcome-run",
			UnsupportedFiles: []repoaudit.UnsupportedFile{{
				FileRef: binary, Reason: "binary",
			}},
		},
	); err != nil {
		t.Fatal(err)
	}
	outcome := loadRepositoryReviewOutcome(store, repoaudit.RepositoryReviewAutomation{
		Repository: repository, CampaignID: campaignID,
		RunIDs:         []string{"old-run", "assignment-outcome-run"},
		ReviewerModels: []string{"review-a"},
	})
	if !outcome.found || outcome.reviewedFiles != 1 || outcome.unsupportedFiles != 1 ||
		outcome.findings != 0 || outcome.modelFindings["review-a"] != 1 ||
		len(outcome.modelPaths["review-a"]) != 1 {
		t.Fatalf("assignment campaign outcome = %#v", outcome)
	}
}

func TestLoadRepositoryReviewOutcomeSkipsForeignLegacyRunForPendingCampaign(t *testing.T) {
	store := repoaudit.NewStore(t.TempDir())
	repository := "owner/assignment-foreign-legacy-run"
	commit := strings.Repeat("a", 40)
	file := repoaudit.FileRef{
		Path: "legacy.go", BlobSHA: strings.Repeat("b", 40), SizeBytes: 10,
		Category: "code", Mode: "100644",
	}
	plan, err := store.PlanWithProfileLimitAuthoritative(
		t.Context(), repository, commit, "inventory", "legacy-profile",
		[]repoaudit.FileRef{file}, false, 1, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Record(t.Context(), repoaudit.RecordRequest{
		Plan: plan, RunID: "legacy-run", CompletedFiles: []repoaudit.FileRef{file},
	}); err != nil {
		t.Fatal(err)
	}
	outcome := loadRepositoryReviewOutcome(store, repoaudit.RepositoryReviewAutomation{
		Repository: repository, CampaignID: repoaudit.NewRepositoryReviewCampaignID(),
		CampaignRecoveryPending: true, RunIDs: []string{"legacy-run"},
	})
	if !outcome.found || outcome.reviewedFiles != 0 {
		t.Fatalf("foreign legacy outcome = %#v", outcome)
	}
}

func TestRepositoryReviewAssignmentSmallBoundaryHelpers(t *testing.T) {
	if !repositoryReviewLegacyReviewerIdentityMatches("default", "default") ||
		repositoryReviewLegacyReviewerIdentityMatches("default", "review-a") ||
		repositoryReviewLegacyReviewerIdentityMatches("", "") ||
		!repositoryReviewLegacyReviewerIdentityMatches("review-a", "review-a") {
		t.Fatal("legacy reviewer identity boundary mismatch")
	}
	if got := repositoryReviewEffectiveWorkflowTimeoutForAssignment(0, 0); got != 65*time.Minute {
		t.Fatalf("default assignment envelope = %s", got)
	}
}

func TestRepositoryReviewAssignmentInstalledRecoveryRejectsActiveRun(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(t, 1, repositoryReviewBackfillRunSpec{
		inspected: []int{0}, occurrences: 0,
	})
	resolved := workflows.RepositoryReviewModelProfile{
		Revision: "assignment-active-recovery", AccountRef: fixture.automation.EffectiveAccountRef,
		ReviewerModels: fixture.automation.ReviewerModels, MaxContentBytes: int(fixture.automation.MaxContentBytes),
	}
	prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), fixture.automation, fixture.state,
		repoaudit.NewRepositoryReviewCampaignID(), fixture.runStore, resolved,
	)
	if err != nil || !prepared.Available {
		t.Fatalf("prepared recovery = %#v err=%v", prepared, err)
	}
	installed, prepared, err := installRepositoryReviewLegacyCampaignAuthority(
		t.Context(), fixture.store, prepared,
	)
	if err != nil {
		t.Fatal(err)
	}
	active, err := fixture.store.UpdateAutomation(
		t.Context(), installed.ID, installed.Version,
		func(candidate *repoaudit.RepositoryReviewAutomation) error {
			candidate.CampaignRecoveryPending = false
			candidate.Status = repoaudit.RepositoryReviewAutomationRunning
			candidate.ActiveRunID = "active-assignment-run"
			candidate.RunIDs = append(candidate.RunIDs, candidate.ActiveRunID)
			if candidate.StartedAt.IsZero() {
				candidate.StartedAt = time.Now().Add(-time.Minute)
			}
			candidate.PauseReason = ""
			candidate.PauseDetail = ""
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	prepared.AutomationVersion = active.Version
	if _, _, err := installRepositoryReviewLegacyCampaignAuthority(
		t.Context(), fixture.store, prepared,
	); !errors.Is(err, repoaudit.ErrConflict) {
		t.Fatalf("active installed recovery error = %v", err)
	}
}

func TestRepositoryReviewAssignmentRecoveryRejectsOversizedCatalog(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(t, 1, repositoryReviewBackfillRunSpec{
		inspected: []int{0}, occurrences: 0,
	})
	models := make([]string, 33)
	for index := range models {
		models[index] = fmt.Sprintf("review-%02d", index)
	}
	resolved := workflows.RepositoryReviewModelProfile{
		Revision: "assignment-oversized-catalog", AccountRef: fixture.automation.EffectiveAccountRef,
		ReviewerModels: models, MaxContentBytes: int(fixture.automation.MaxContentBytes),
	}
	prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), fixture.automation, fixture.state,
		repoaudit.NewRepositoryReviewCampaignID(), fixture.runStore, resolved,
	)
	if err == nil || prepared.Exact {
		t.Fatalf("oversized recovery catalog = %#v err=%v", prepared, err)
	}
}

func TestRepositoryReviewAssignmentTimeoutMutationBoundaries(t *testing.T) {
	handler, mux, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	profile := createRepositoryReviewProfileForTest(t, mux, "Timeout profile", "cheap")
	invalidCreate := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, "/api/repository-reviews/automations", map[string]any{
			"repository": "owner/timeout-create", "profile_id": profile.ID,
			"assignment_timeout_seconds": 0,
		},
	)
	if invalidCreate.Code != http.StatusBadRequest {
		t.Fatalf("invalid automation timeout create = %d %s", invalidCreate.Code, invalidCreate.Body.String())
	}
	createdResponse := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, "/api/repository-reviews/automations", map[string]any{
			"repository": "owner/timeout-update", "profile_id": profile.ID,
		},
	)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("automation create = %d %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created struct {
		Automation repoaudit.RepositoryReviewAutomation `json:"automation"`
	}
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	invalidUpdate := repositoryReviewAutomationMutation(
		t, mux, http.MethodPatch,
		"/api/repository-reviews/automations/"+created.Automation.ID,
		map[string]any{
			"repository": created.Automation.Repository, "profile_id": profile.ID,
			"expected_version": created.Automation.Version, "assignment_timeout_seconds": 61,
		},
	)
	if invalidUpdate.Code != http.StatusBadRequest {
		t.Fatalf("invalid automation timeout update = %d %s", invalidUpdate.Code, invalidUpdate.Body.String())
	}
	profileBody := repositoryReviewProfileBody(profile)
	profileBody["expected_version"] = profile.Version
	profileBody["assignment_timeout_seconds"] = 61
	invalidProfileUpdate := repositoryReviewAutomationMutation(
		t, mux, http.MethodPatch, "/api/repository-reviews/profiles/"+profile.ID, profileBody,
	)
	if invalidProfileUpdate.Code != http.StatusBadRequest {
		t.Fatalf(
			"invalid profile timeout update = %d %s",
			invalidProfileUpdate.Code,
			invalidProfileUpdate.Body.String(),
		)
	}
}

func TestRepositoryReviewAssignmentReconcileStopsAfterCatalogRead(t *testing.T) {
	store := repoaudit.NewStore(t.TempDir())
	automation := testRepositoryReviewAutomation()
	automation.ID = "rra_assignment_reconcile_cancel"
	if _, err := store.CreateAutomation(t.Context(), automation); err != nil {
		t.Fatal(err)
	}
	ctx := &repositoryReviewThirdErrorContext{}
	controller := &repositoryReviewController{
		ctx: ctx, leasedStore: store, leasedConfig: &config.Config{},
		active: make(map[string]*repositoryReviewActiveRun),
	}
	controller.reconcile()
	if ctx.calls.Load() < 3 {
		t.Fatalf("reconcile context checks = %d", ctx.calls.Load())
	}
}

//nolint:govet // Boundary assertions intentionally reuse err in short scopes.
func TestRepositoryReviewAssignmentReconcileRestoresCampaignAfterRestart(t *testing.T) {
	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("f", 40)
	input := testRepositoryReviewAutomation()
	input.ID = "rra_assignment_reconcile_restart"
	input.Repository = "owner/assignment-reconcile-restart"
	input.Status = repoaudit.RepositoryReviewAutomationRunning
	input.ActiveRunID = "assignment-reconcile-run"
	input.RunIDs = []string{input.ActiveRunID}
	input.StartedAt = time.Now().Add(-time.Minute)
	input.ResolvedCommitSHA = commit
	input.CampaignID = repoaudit.NewRepositoryReviewCampaignID()
	controller := handler.repositoryReviewControllerInstance()
	if err := controller.Start(); err != nil {
		t.Fatal(err)
	}
	automation, err := store.CreateAutomation(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginCampaign(t.Context(), repoaudit.BeginCampaignRequest{
		Repository: repoaudit.CanonicalRepositoryIdentity(input.Repository),
		CampaignID: input.CampaignID, CommitSHA: commit,
	}); err != nil {
		t.Fatal(err)
	}
	controller.reconcile()
	paused, found, err := store.GetAutomation(t.Context(), automation.ID)
	if err != nil || !found || paused.Status != repoaudit.RepositoryReviewAutomationPaused ||
		paused.ActiveRunID != "" || paused.CampaignID != input.CampaignID {
		t.Fatalf("reconciled assignment campaign = %#v found=%v err=%v", paused, found, err)
	}
}
