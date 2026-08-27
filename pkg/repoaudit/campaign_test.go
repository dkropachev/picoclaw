package repoaudit

import (
	"fmt"
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
