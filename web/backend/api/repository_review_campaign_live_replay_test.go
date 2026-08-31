package api

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestRepositoryReviewCampaignLiveCopyReplay(t *testing.T) {
	source := os.Getenv("REPOSITORY_REVIEW_REPLAY_WORKSPACE")
	if source == "" {
		t.Skip("set REPOSITORY_REVIEW_REPLAY_WORKSPACE to a disposable copied store")
	}
	source, err := filepath.Abs(source)
	if err != nil {
		t.Fatal(err)
	}
	// The supplied source is read-only. All mutation happens after copying it to
	// this test-owned temporary workspace.
	workspace := t.TempDir()
	if filepath.Clean(source) == filepath.Clean(workspace) {
		t.Fatal("replay source must differ from the test-owned destination")
	}
	if copyErr := os.CopyFS(workspace, os.DirFS(source)); copyErr != nil {
		t.Fatal(copyErr)
	}
	store := repoaudit.NewStore(workspace)
	automation, found, err := store.GetAutomation(t.Context(), "rra_5cbi33vsmrt7a3yqtdqtb4kamh")
	if err != nil || !found {
		t.Fatalf("automation found=%v err=%v", found, err)
	}
	state, found, err := store.ResolveRepositoryState(automation.Repository, automation.RunIDs)
	if err != nil || !found {
		t.Fatalf("state found=%v err=%v", found, err)
	}
	resolved := workflows.RepositoryReviewModelProfile{
		Revision: "live-copy", AccountRef: "router-1",
		ReviewerModels: []string{"review"}, MaxContentBytes: 282624,
	}
	campaignID := automation.CampaignID
	if campaignID == "" {
		campaignID = repoaudit.NewRepositoryReviewCampaignID()
	}
	prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), automation, state, campaignID,
		workflows.NewFileRunStore(workspace), resolved,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.Available || !prepared.Exact ||
		prepared.Request.Coverage.SelectedFiles != 367 || prepared.InspectedFiles != 0 ||
		prepared.CompletedFiles != 0 || prepared.FindingOccurrences != 87 ||
		len(prepared.Request.Runs) != 19 || len(prepared.Request.ContextIDs) != 21 ||
		len(prepared.Request.FindingIDs) != 87 || len(prepared.ScopeSelection.CandidateIDs) != 367 {
		if len(prepared.UnrecoveredFindingIDs) > 0 {
			missingID := prepared.UnrecoveredFindingIDs[0]
			for _, finding := range state.Findings {
				if finding.ID != missingID || len(finding.Observations) == 0 {
					continue
				}
				originID := finding.Observations[0].ContextID
				for _, contextRecord := range state.Contexts {
					if contextRecord.ID != originID {
						continue
					}
					var ledgerRun repoaudit.ReviewRun
					for _, run := range state.Runs {
						if run.ID == contextRecord.RunID {
							ledgerRun = run
						}
					}
					workflowRun, _ := workflows.NewFileRunStore(workspace).GetRun(t.Context(), ledgerRun.ID)
					var plan repoaudit.Plan
					_ = repositoryReviewDecodeValue(
						repositoryReviewRunStep(workflowRun, "plan").Outputs["plan"], &plan,
					)
					evidence, _ := workflows.DecodeRepositoryReviewManagedEvidence(
						repositoryReviewRunStep(workflowRun, "review").Outputs["managed_children"], plan,
						workflows.RepositoryReviewManagedEvidenceOptions{AllowLegacyCoreFindings: true},
					)
					candidate, candidateFound := repositoryReviewLegacyFindingEvidence(
						finding, contextRecord, evidence,
					)
					t.Logf("missing identity context=%v candidate=%v finding=%v severity=%s candidateSeverity=%s",
						repoaudit.ValidateRepositoryReviewLegacyContextIdentity(contextRecord), candidateFound,
						repoaudit.ValidateRepositoryReviewLegacyFindingIdentity(finding, contextRecord, candidate),
						finding.Severity, candidate.Severity)
				}
			}
		}
		t.Fatalf(
			"live-copy prepare available=%v exact=%v selected=%d inspected=%d completed=%d "+
				"findings=%d runs=%d/%d contexts=%d IDs=%d selection=%d recovered=%d/%d/%d missing=%v",
			prepared.Available, prepared.Exact, prepared.Request.Coverage.SelectedFiles,
			prepared.InspectedFiles, prepared.CompletedFiles, prepared.FindingOccurrences,
			len(prepared.Request.Runs), prepared.ExpectedRuns, len(prepared.Request.ContextIDs),
			len(prepared.Request.FindingIDs), len(prepared.ScopeSelection.CandidateIDs),
			prepared.RecoveredRuns, prepared.RecoveredContexts, prepared.RecoveredFindings,
			prepared.UnrecoveredFindingIDs,
		)
	}
	_, prepared, err = installRepositoryReviewLegacyCampaignAuthority(t.Context(), store, prepared)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := applyRepositoryReviewLegacyCampaignBackfill(t.Context(), store, prepared)
	if err != nil {
		t.Fatal(err)
	}
	metrics := repoaudit.CurrentCampaignMetrics(applied, campaignID, nil, automation.StartedAt)
	assignmentProgress := repoaudit.CurrentCampaignAssignmentProgress(applied, campaignID)
	taggedFindings := repoaudit.CurrentCampaignFindingsByID(
		applied, campaignID, automation.RunIDs, automation.StartedAt,
	)
	rawSources := repoaudit.CurrentCampaignRawFindings(
		applied, campaignID, automation.RunIDs, automation.StartedAt,
	)
	if !metrics.CoverageExact || metrics.SelectedFiles != 367 || metrics.InspectedFiles != 0 ||
		metrics.CompletedFiles != 0 || assignmentProgress.Completed != 0 ||
		len(taggedFindings) != 87 || len(rawSources) != 87 {
		t.Fatalf("live-copy metrics = %#v assignments=%#v tagged=%d raw=%d",
			metrics, assignmentProgress, len(taggedFindings), len(rawSources))
	}
	rescanned, err := prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), func() repoaudit.RepositoryReviewAutomation {
			value, _, _ := store.GetAutomation(t.Context(), automation.ID)
			return value
		}(), applied, campaignID, workflows.NewFileRunStore(workspace), resolved,
	)
	if err != nil || !rescanned.Available || !rescanned.Exact {
		t.Fatalf("live-copy rescan = %#v err=%v", rescanned, err)
	}
}
