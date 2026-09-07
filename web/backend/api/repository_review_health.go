package api

import (
	"net/http"
	"time"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

type repositoryReviewRunFindingHealth struct {
	Total              int `json:"total"`
	Pending            int `json:"pending"`
	Processing         int `json:"processing"`
	Failed             int `json:"failed"`
	NeedsReview        int `json:"needs_review"`
	AssociatedNew      int `json:"associated_new"`
	AssociatedExisting int `json:"associated_existing"`
	Unrepresented      int `json:"unrepresented"`
}

type repositoryReviewRepositoryFindingHealth struct {
	Total            int `json:"total"`
	Provisional      int `json:"provisional"`
	ValidationFailed int `json:"validation_failed"`
	IssueConflicts   int `json:"issue_conflicts"`
}

type repositoryReviewFindingsProcessingHealth struct {
	Total      int `json:"total"`
	Pending    int `json:"pending"`
	Processing int `json:"processing"`
	Failed     int `json:"failed"`
	Completed  int `json:"completed"`
}

type repositoryReviewFindingHealth struct {
	RunFindings        repositoryReviewRunFindingHealth         `json:"run_findings"`
	RepositoryFindings repositoryReviewRepositoryFindingHealth  `json:"repository_findings"`
	FindingsProcessing repositoryReviewFindingsProcessingHealth `json:"findings_processing"`
	UpdatedAt          time.Time                                `json:"updated_at"`
}

func (h *Handler) handleGetRepositoryReviewFindingHealth(
	w http.ResponseWriter,
	r *http.Request,
) {
	ledger, err := h.repositoryReviewAutomationLedger(r.Context(), r.PathValue("automation_id"))
	if err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	writeRepositoryReviewJSON(
		w,
		http.StatusOK,
		repositoryReviewFindingHealthFor(ledger.Automation, ledger.State),
	)
}

func repositoryReviewFindingHealthFor(
	automation repoaudit.RepositoryReviewAutomation,
	state repoaudit.RepositoryState,
) repositoryReviewFindingHealth {
	result := repositoryReviewFindingHealth{
		UpdatedAt: latestRepositoryReviewHealthUpdate(automation, state),
	}
	statusIndex := newRepositoryReviewRunFindingStatusIndex(state)
	for _, finding := range repositoryReviewCurrentDeduplicatedFindings(automation, state) {
		result.RunFindings.Total++
		switch statusIndex.status(finding) {
		case repositoryReviewRunFindingPending:
			result.RunFindings.Pending++
		case repositoryReviewRunFindingProcessing:
			result.RunFindings.Processing++
		case repositoryReviewRunFindingFailed:
			result.RunFindings.Failed++
		case repositoryReviewRunFindingNeedsReview:
			result.RunFindings.NeedsReview++
		case repositoryReviewRunFindingAssociatedNew:
			result.RunFindings.AssociatedNew++
		case repositoryReviewRunFindingAssociatedExisting:
			result.RunFindings.AssociatedExisting++
		}
	}
	// Keep this as a direct sum of the independently projected states. An
	// aggregate-count subtraction loses many-to-one occurrences.
	result.RunFindings.Unrepresented = result.RunFindings.Pending +
		result.RunFindings.Processing + result.RunFindings.Failed

	result.RepositoryFindings.Total = len(state.RepositoryFindings)
	for _, finding := range state.RepositoryFindings {
		if finding.MatchState == repoaudit.RepositoryMatchProvisional {
			result.RepositoryFindings.Provisional++
		}
		if finding.ValidationState == repoaudit.RepositoryValidationFailed {
			result.RepositoryFindings.ValidationFailed++
		}
		if finding.Issue.Conflict {
			result.RepositoryFindings.IssueConflicts++
		}
	}

	result.FindingsProcessing = repositoryReviewFindingsProcessingHealthFor(state.RawFindings)
	return result
}

func repositoryReviewFindingsProcessingHealthFor(
	findings []repoaudit.RawReviewFinding,
) repositoryReviewFindingsProcessingHealth {
	result := repositoryReviewFindingsProcessingHealth{Total: len(findings)}
	for _, raw := range findings {
		switch raw.State {
		case repoaudit.RawFindingDeduplicationPending:
			result.Pending++
		case repoaudit.RawFindingDeduplicationRunning:
			result.Processing++
		case repoaudit.RawFindingDeduplicationFailed:
			result.Failed++
		case repoaudit.RawFindingDeduplicationCompleted:
			result.Completed++
		}
	}
	return result
}

func latestRepositoryReviewHealthUpdate(
	automation repoaudit.RepositoryReviewAutomation,
	state repoaudit.RepositoryState,
) time.Time {
	latest := automation.UpdatedAt
	updates := make(
		[]time.Time,
		0,
		2+len(state.Findings)+len(state.RepositoryFindings)+len(state.RawFindings),
	)
	updates = append(updates, state.UpdatedAt)
	for _, finding := range state.Findings {
		updates = append(updates, finding.UpdatedAt)
	}
	for _, finding := range state.RepositoryFindings {
		updates = append(updates, finding.UpdatedAt)
	}
	for _, raw := range state.RawFindings {
		updates = append(updates, raw.UpdatedAt)
	}
	for _, updatedAt := range updates {
		if updatedAt.After(latest) {
			latest = updatedAt
		}
	}
	return latest
}
