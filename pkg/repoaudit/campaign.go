package repoaudit

import "time"

// CurrentCampaignFindings selects immutable review occurrences belonging to an
// automation campaign. Run FindingIDs are authoritative, while context
// membership preserves findings recorded by legacy checkpoints that omitted
// the run-level ID projection.
func CurrentCampaignFindings(
	state RepositoryState,
	runIDs []string,
	startedAt time.Time,
) []Finding {
	wantedRuns := make(map[string]struct{}, len(runIDs))
	for _, runID := range runIDs {
		if runID != "" {
			wantedRuns[runID] = struct{}{}
		}
	}
	selected := make(map[string]struct{})
	for _, run := range state.Runs {
		if _, ok := wantedRuns[run.ID]; !ok ||
			!startedAt.IsZero() && run.CompletedAt.Before(startedAt) {
			continue
		}
		for _, findingID := range run.FindingIDs {
			selected[findingID] = struct{}{}
		}
	}
	currentContexts := make(map[string]struct{})
	for _, contextRecord := range state.Contexts {
		if _, ok := wantedRuns[contextRecord.RunID]; !ok ||
			!startedAt.IsZero() && contextRecord.CreatedAt.Before(startedAt) {
			continue
		}
		currentContexts[contextRecord.ID] = struct{}{}
	}
	for _, finding := range state.Findings {
		for _, contextID := range finding.ContextIDs {
			if _, ok := currentContexts[contextID]; ok {
				selected[finding.ID] = struct{}{}
				break
			}
		}
	}
	out := make([]Finding, 0, len(selected))
	for _, finding := range state.Findings {
		if _, ok := selected[finding.ID]; ok {
			out = append(out, finding)
		}
	}
	return out
}
