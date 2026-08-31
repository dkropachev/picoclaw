//go:build unix

package repoaudit

import (
	"errors"
	"strings"
	"testing"
)

func TestPreexistingCoverageDebtProfileSize(t *testing.T) {
	t.Run("encoded profile size", func(t *testing.T) {
		profile := profileCoverageFixture("rrpf_encoded_size")
		profile.ReviewFocus = strings.Repeat("\x01", maxFindingTextBytes)
		profile.IssuePrompt = strings.Repeat("\x01", maxRepositoryReviewIssuePromptBytes)
		profile.ScopePolicy.FreeText = strings.Repeat("\x01", maxRepositoryReviewScopeTextBytes)
		for index := range maxRepositoryReviewScopeFolders {
			prefix := strings.Repeat("\x01", maxRepositoryReviewScopePrefixBytes-4) +
				string(rune('a'+index/26)) + string(rune('a'+index%26))
			profile.ScopePolicy.IncludeFolders = append(profile.ScopePolicy.IncludeFolders, prefix+"i")
			profile.ScopePolicy.ExcludeFolders = append(profile.ScopePolicy.ExcludeFolders, prefix+"e")
		}
		if err := NewStore(t.TempDir()).saveProfile(profile); err == nil ||
			!strings.Contains(err.Error(), "exceeds its size limit") {
			t.Fatalf("oversized encoded profile error = %v", err)
		}
	})
}

func TestPreexistingCoverageDebtStoreValidationAndPlanning(t *testing.T) {
	fixture := newAssignmentCoverageFixture(t, 1, 1)
	state, _, err := fixture.store.Get(fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := state
	corrupt.CurrentCampaign = func() *RepositoryReviewCampaignCoverage {
		coverage := cloneRepositoryReviewCampaignCoverage(*state.CurrentCampaign)
		coverage.Paths[fixture.files[0].Path] = RepositoryReviewCampaignPathCoverage{AssignmentBits: "!"}
		return &coverage
	}()
	store := fixture.store
	store.loadForTest = func(string) (RepositoryState, error) { return corrupt, nil }
	if _, err := store.PlanAssignmentsForCampaign(
		t.Context(), fixture.repository, fixture.plan.CommitSHA, fixture.plan.InventoryHash,
		fixture.plan.ProfileHash, fixture.campaignID, fixture.catalog, fixture.files, false, 1, true,
	); err == nil {
		t.Fatal("planner accepted malformed retained assignment bits")
	}

	invalidAttribution := state
	invalidAttribution.FileAttributions = []RepositoryReviewFileAttribution{{}}
	if err := validateState(invalidAttribution); err == nil ||
		!strings.Contains(err.Error(), "file attribution") {
		t.Fatalf("invalid attribution state error = %v", err)
	}
}

func TestPreexistingCoverageDebtFinalizeFallbackAndMergeError(t *testing.T) {
	fixture := newAssignmentCoverageFixture(t, 1, 1)
	if _, err := fixture.store.BeginRepositoryReviewRun(t.Context(), BeginRepositoryReviewRunRequest{
		Plan: fixture.plan, RunID: "legacy-context-run", ReviewableFiles: fixture.files,
	}); err != nil {
		t.Fatal(err)
	}
	state, _, err := fixture.store.Get(fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	state.Contexts = append(state.Contexts, FindingContext{
		ID: "legacy-context", CampaignID: fixture.campaignID,
		Repository: fixture.repository, CommitSHA: fixture.plan.CommitSHA,
		InventoryHash: fixture.plan.InventoryHash, ProfileHash: fixture.plan.ProfileHash,
		RunID: "legacy-context-run", Model: "provider/legacy", Reviewer: "legacy",
		Files: fixture.files, CreatedAt: repositoryAuditTestNow,
	})
	if saveErr := fixture.store.save(&state); saveErr != nil {
		t.Fatal(saveErr)
	}
	result, err := fixture.store.FinalizeRepositoryReviewRun(t.Context(), FinalizeRepositoryReviewRunRequest{
		Plan: fixture.plan, RunID: "legacy-context-run", CompletedAt: repositoryAuditTestNow,
	})
	if err != nil || len(result.Run.Models) != 1 || result.Run.Models[0] != "provider/legacy" {
		t.Fatalf("legacy finalize models=%v err=%v", result.Run.Models, err)
	}

	mergeFixture := newAssignmentCoverageFixture(t, 1, 1)
	if _, beginErr := mergeFixture.store.BeginRepositoryReviewRun(t.Context(), BeginRepositoryReviewRunRequest{
		Plan: mergeFixture.plan, RunID: "merge-error-run", ReviewableFiles: mergeFixture.files,
	}); beginErr != nil {
		t.Fatal(beginErr)
	}
	mergeState, _, err := mergeFixture.store.Get(mergeFixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	mergeState.CurrentCampaign.InventoryHash = ""
	mergeState.CurrentCampaign.ProfileHash = ""
	mergeState.CurrentCampaign.ScopeDigest = ""
	mergeState.CurrentCampaign.RequiredAssignments = 0
	mergeState.CurrentCampaign.SelectedFiles = 0
	mergeState.CurrentCampaign.AssignmentCatalog = nil
	broken := mergeFixture.store
	broken.loadForTest = func(string) (RepositoryState, error) { return mergeState, nil }
	_, err = broken.FinalizeRepositoryReviewRun(t.Context(), FinalizeRepositoryReviewRunRequest{
		Plan: mergeFixture.plan, RunID: "merge-error-run",
		UnsupportedFiles: []UnsupportedFile{{
			FileRef: mergeFixture.files[0], Reason: "file_too_large",
		}},
	})
	if !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("unsupported merge error = %v", err)
	}
}
