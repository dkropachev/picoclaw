package repoaudit

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestCurrentCampaignFindingsUsesRunAndLegacyContextMembership(t *testing.T) {
	started := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	state := RepositoryState{}
	runFindingIDs := make([]string, 0, 36)
	for index := range 36 {
		id := fmt.Sprintf("rfn_%02d", index)
		runFindingIDs = append(runFindingIDs, id)
		state.Findings = append(state.Findings, Finding{ID: id})
	}
	state.Runs = []ReviewRun{{
		ID: "run-current", FindingIDs: runFindingIDs, CompletedAt: started.Add(time.Minute),
	}}
	state.Contexts = []FindingContext{{
		ID: "rctx_legacy", RunID: "run-current", CreatedAt: started.Add(2 * time.Minute),
	}}
	state.Findings = append(state.Findings,
		Finding{ID: "rfn_legacy_37", ContextIDs: []string{"rctx_legacy"}},
		Finding{ID: "rfn_other", ContextIDs: []string{"rctx_other"}},
	)
	selected := CurrentCampaignFindings(state, []string{"run-current"}, started)
	if len(selected) != 37 {
		t.Fatalf("current campaign finding count = %d, want 37", len(selected))
	}
}

func TestCurrentCampaignRawFindingsKeepsLegacyTotalStableDuringReplay(t *testing.T) {
	started := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	file := FileRef{Path: "pkg/cache.go", BlobSHA: strings.Repeat("a", 40), SizeBytes: 10}
	legacy := Finding{
		ID: "rfn_legacy", Repository: "owner/repo", CommitSHA: strings.Repeat("b", 40),
		File: file, Severity: "high", Title: "stale cache", Evidence: "stale value",
		Impact: "wrong result", Validation: Validation{Status: "confirmed", Summary: "traced"},
		ContextIDs: []string{"ctx-current"}, Models: []string{"reviewer"},
		Status: FindingOpen, CreatedAt: started.Add(time.Minute), UpdatedAt: started.Add(time.Minute),
	}
	state := RepositoryState{
		Repository: "owner/repo", UpdatedAt: started.Add(2 * time.Minute),
		Findings: []Finding{legacy, {
			ID: "rfn_foreign", ContextIDs: []string{"ctx-foreign"},
		}},
		Contexts: []FindingContext{
			{
				ID: "ctx-current", RunID: "wr_current", Model: "reviewer", Reviewer: "correctness",
				CreatedAt: started.Add(time.Minute),
			},
			{ID: "ctx-foreign", RunID: "wr_foreign", CreatedAt: started.Add(time.Minute)},
		},
		Runs: []ReviewRun{
			{ID: "wr_current", FindingIDs: []string{legacy.ID}, CompletedAt: started.Add(time.Minute)},
			{ID: "wr_foreign", FindingIDs: []string{"rfn_foreign"}, CompletedAt: started.Add(time.Minute)},
		},
	}
	before := CurrentCampaignRawFindings(state, "", []string{"wr_current"}, started)
	if len(before) != 1 || before[0].LegacyFindingID != legacy.ID ||
		!strings.HasPrefix(before[0].ID, "rrw_") || before[0].State != RawFindingDeduplicationPending {
		t.Fatalf("pre-admission raw findings = %#v", before)
	}

	admitted := before[0]
	admitted.State = RawFindingDeduplicationRunning
	state.RawFindings = []RawReviewFinding{admitted, {
		ID: "rrw_foreign", RunID: "wr_foreign", CampaignID: "rrc_foreign",
	}}
	during := CurrentCampaignRawFindings(state, "", []string{"wr_current"}, started)
	if len(during) != 1 || during[0].ID != before[0].ID ||
		during[0].State != RawFindingDeduplicationRunning {
		t.Fatalf("in-flight raw findings = %#v", during)
	}

	admitted.State = RawFindingDeduplicationCompleted
	admitted.Disposition = RawFindingDispositionNew
	admitted.DeduplicatedFindingID = "rdf_current"
	state.RawFindings[0] = admitted
	state.DeduplicatedFindings = []DeduplicatedReviewFinding{{
		ID: "rdf_current", CampaignID: admitted.CampaignID, RawSourceIDs: []string{admitted.ID},
	}}
	state.Findings = append(state.Findings, Finding{
		ID: "rdf_current", ContextIDs: []string{"ctx-current"},
	})
	after := CurrentCampaignRawFindings(state, "", []string{"wr_current"}, started)
	deduplicated := CurrentCampaignDeduplicatedFindings(state, "", []string{"wr_current"}, started)
	if len(after) != 1 || after[0].ID != before[0].ID || len(deduplicated) != 1 ||
		deduplicated[0].ID != "rdf_current" {
		t.Fatalf("completed raw=%#v deduplicated=%#v", after, deduplicated)
	}
}
