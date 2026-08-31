package api

import (
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

func TestRepositoryReviewRecoveringOutcomeUsesRecoveredCampaignPaths(t *testing.T) {
	campaignID := repoaudit.NewRepositoryReviewCampaignID()
	startedAt := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	state := repoaudit.RepositoryState{
		Version: 1,
		CurrentCampaign: &repoaudit.RepositoryReviewCampaignCoverage{
			ID: campaignID,
			Paths: map[string]repoaudit.RepositoryReviewCampaignPathCoverage{
				"completed.go":   {Completed: true},
				"unsupported.go": {Unsupported: true},
			},
		},
		Runs: []repoaudit.ReviewRun{{
			ID: "wr_recovered", CampaignID: campaignID,
			UnsupportedPaths: []string{"run-unsupported.go"},
			CompletedAt:      startedAt.Add(time.Minute),
		}},
		Files: map[string]repoaudit.ReviewedFile{
			"legacy-file.go": {RunID: "wr_recovered"},
		},
	}
	automation := repoaudit.RepositoryReviewAutomation{
		CampaignID: campaignID, CampaignRecoveryPending: true,
		RunIDs: []string{"wr_recovered"}, StartedAt: startedAt,
	}

	outcome := loadRepositoryReviewOutcomeFromResolvedState(state, automation)
	if !outcome.found || outcome.coverageAvailable || outcome.reviewedFiles != 1 ||
		outcome.unsupportedFiles != 2 {
		t.Fatalf("recovered outcome=%#v", outcome)
	}
}

func TestRepositoryReviewLegacyOutcomeUsesRunOwnedFiles(t *testing.T) {
	startedAt := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	state := repoaudit.RepositoryState{
		Runs: []repoaudit.ReviewRun{{
			ID: "wr_legacy", UnsupportedPaths: []string{"unsupported.bin"},
			CompletedAt: startedAt.Add(time.Minute),
		}},
		Files: map[string]repoaudit.ReviewedFile{
			"reviewed.go": {RunID: "wr_legacy"},
			"other.go":    {RunID: "wr_other"},
		},
	}
	automation := repoaudit.RepositoryReviewAutomation{
		RunIDs: []string{"wr_legacy"}, StartedAt: startedAt,
	}
	outcome := loadRepositoryReviewOutcomeFromResolvedState(state, automation)
	if !outcome.found || outcome.reviewedFiles != 1 || outcome.unsupportedFiles != 1 {
		t.Fatalf("legacy outcome=%#v", outcome)
	}
}

func TestRepositoryReviewOutcomeLegacySelectionAndModelAttribution(t *testing.T) {
	startedAt := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	contextRecord := repoaudit.FindingContext{
		ID: "ctx_current", RunID: "wr_current", Model: "reviewer",
		Files: []repoaudit.FileRef{{Path: "pkg/current.go"}},
	}
	current := repoaudit.Finding{
		ID: "rdf_current", ContextIDs: []string{contextRecord.ID},
		Observations: []repoaudit.FindingObservation{{
			ContextID: contextRecord.ID, Model: "reviewer",
		}},
	}
	state := repoaudit.RepositoryState{
		Runs: []repoaudit.ReviewRun{
			{ID: "wr_old", FindingIDs: []string{"rdf_old"}, CompletedAt: startedAt.Add(-time.Minute)},
			{ID: "wr_current", FindingIDs: []string{current.ID}, CompletedAt: startedAt.Add(time.Minute)},
			{ID: "wr_foreign", FindingIDs: []string{"rdf_foreign"}, CompletedAt: startedAt.Add(time.Minute)},
		},
		Contexts: []repoaudit.FindingContext{contextRecord},
		Findings: []repoaudit.Finding{current},
		Files: map[string]repoaudit.ReviewedFile{
			"pkg/current.go": {RunID: "wr_current"},
		},
	}
	automation := repoaudit.RepositoryReviewAutomation{
		RunIDs: []string{"wr_old", "wr_current"}, StartedAt: startedAt,
		ReviewerModels: []string{"reviewer", "unused"},
	}
	outcome := loadRepositoryReviewOutcomeFromResolvedState(state, automation)
	if !outcome.found || outcome.reviewedFiles != 1 ||
		outcome.modelFindings["reviewer"] != 1 || outcome.modelFindings["unused"] != 0 ||
		len(outcome.modelPaths["reviewer"]) != 1 ||
		outcome.modelPaths["reviewer"][0] != "pkg/current.go" {
		t.Fatalf("legacy attributed outcome=%#v", outcome)
	}

	empty := loadRepositoryReviewOutcomeFromResolvedState(
		repoaudit.RepositoryState{}, repoaudit.RepositoryReviewAutomation{},
	)
	if empty.found {
		t.Fatalf("empty legacy outcome=%#v", empty)
	}
}

func TestRepositoryReviewCurrentFindingProgressHelpers(t *testing.T) {
	campaignID := repoaudit.NewRepositoryReviewCampaignID()
	state := repoaudit.RepositoryState{
		RawFindings: []repoaudit.RawReviewFinding{
			{ID: "rrw_one", CampaignID: campaignID},
			{ID: "rrw_two", CampaignID: campaignID},
			{ID: "rrw_other", CampaignID: repoaudit.NewRepositoryReviewCampaignID()},
		},
		DeduplicatedFindings: []repoaudit.DeduplicatedReviewFinding{
			{ID: "rdf_one", CampaignID: campaignID, RepositoryFindingID: "rrf_shared"},
			{ID: "rdf_two", CampaignID: campaignID, RepositoryFindingID: "rrf_shared"},
			{ID: "rdf_pending", CampaignID: campaignID},
		},
	}
	automation := repoaudit.RepositoryReviewAutomation{CampaignID: campaignID}

	applyRepositoryReviewCurrentFindingProgress(nil, state)
	applyRepositoryReviewCurrentFindingProgress(&automation, state)
	if automation.Progress.RawFindings != 2 ||
		automation.Progress.DeduplicatedFindings != 3 ||
		automation.Progress.Findings != 3 {
		t.Fatalf("finding progress=%#v", automation.Progress)
	}
	aggregates, pending := repositoryReviewDeduplicatedAssociationCounts(
		state.DeduplicatedFindings,
	)
	if aggregates != 1 || pending != 1 {
		t.Fatalf("association counts=(%d, %d)", aggregates, pending)
	}
}
