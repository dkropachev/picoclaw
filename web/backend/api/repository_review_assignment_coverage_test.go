package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

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
		outcome.findings != 1 || outcome.modelFindings["review-a"] != 1 ||
		len(outcome.modelPaths["review-a"]) != 1 {
		t.Fatalf("assignment campaign outcome = %#v", outcome)
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
