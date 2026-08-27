package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

type repositoryReviewDuplicateRequest struct {
	CandidateID                string `json:"candidate_id"`
	Decision                   string `json:"decision"`
	ExpectedProvisionalVersion int64  `json:"expected_provisional_version"`
	ExpectedCandidateVersion   int64  `json:"expected_candidate_version,omitempty"`
}

type repositoryReviewValidationRequest struct {
	RepositoryFindingIDs []string `json:"repository_finding_ids"`
}

type repositoryReviewFindingLifecycleRequest struct {
	Lifecycle       repoaudit.RepositoryFindingLifecycle `json:"lifecycle"`
	ExpectedVersion int64                                `json:"expected_version"`
}

var loadRepositoryReviewLifecycleConfig = config.LoadConfig

func (h *Handler) handleUpdateRepositoryReviewFindingLifecycle(
	w http.ResponseWriter,
	r *http.Request,
) {
	if err := validateRepositoryReviewMutation(r); err != nil {
		writeRepositoryReviewError(w, err)
		return
	}
	var request repositoryReviewFindingLifecycleRequest
	if err := decodeRepositoryReviewRequest(r, &request); err != nil {
		writeRepositoryReviewError(w, err)
		return
	}
	ledger, err := h.repositoryReviewAutomationLedger(r.Context(), r.PathValue("automation_id"))
	if err != nil || !ledger.Found {
		if err == nil {
			err = errors.New("repository review ledger not found")
		}
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	state, finding, err := ledger.Store.SetRepositoryFindingLifecycle(
		ledger.State.Repository, r.PathValue("repository_finding_id"), request.Lifecycle,
		request.ExpectedVersion,
	)
	if err != nil {
		writeRepositoryReviewError(w, err)
		return
	}
	writeRepositoryReviewJSON(w, http.StatusOK, map[string]any{
		"automation": projectRepositoryReviewAutomation(ledger.Automation),
		"repository": repoaudit.Summarize(state), "repository_finding": finding,
	})
}

func (h *Handler) handleResolveRepositoryReviewPossibleDuplicate(
	w http.ResponseWriter,
	r *http.Request,
) {
	if err := validateRepositoryReviewMutation(r); err != nil {
		writeRepositoryReviewError(w, err)
		return
	}
	var request repositoryReviewDuplicateRequest
	if err := decodeRepositoryReviewRequest(r, &request); err != nil {
		writeRepositoryReviewError(w, err)
		return
	}
	ledger, err := h.repositoryReviewAutomationLedger(r.Context(), r.PathValue("automation_id"))
	if err != nil || !ledger.Found {
		if err == nil {
			err = errors.New("repository review ledger not found")
		}
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	state, finding, err := ledger.Store.ResolvePossibleDuplicate(
		ledger.State.Repository,
		repoaudit.RepositoryDuplicateResolution{
			ProvisionalID: r.PathValue("repository_finding_id"),
			CandidateID:   request.CandidateID, Decision: request.Decision,
			ExpectedProvisionalVersion: request.ExpectedProvisionalVersion,
			ExpectedCandidateVersion:   request.ExpectedCandidateVersion,
		},
	)
	if err != nil {
		writeRepositoryReviewError(w, err)
		return
	}
	writeRepositoryReviewJSON(w, http.StatusOK, map[string]any{
		"automation": projectRepositoryReviewAutomation(ledger.Automation),
		"repository": repoaudit.Summarize(state), "repository_finding": finding,
	})
}

func (h *Handler) handleReserveRepositoryReviewValidations(
	w http.ResponseWriter,
	r *http.Request,
) {
	if err := validateRepositoryReviewMutation(r); err != nil {
		writeRepositoryReviewError(w, err)
		return
	}
	var request repositoryReviewValidationRequest
	if err := decodeRepositoryReviewRequest(r, &request); err != nil {
		writeRepositoryReviewError(w, err)
		return
	}
	ledger, err := h.repositoryReviewAutomationLedger(r.Context(), r.PathValue("automation_id"))
	if err != nil || !ledger.Found {
		if err == nil {
			err = errors.New("repository review ledger not found")
		}
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	for _, findingID := range request.RepositoryFindingIDs {
		finding, found := repositoryReviewRepositoryFindingByID(ledger.State, findingID)
		if !found {
			writeRepositoryReviewError(w, os.ErrNotExist)
			return
		}
		if finding.Issue.URL != "" &&
			!repoaudit.RepositoryFindingIssueSnapshotFresh(finding, time.Now()) {
			writeRepositoryReviewError(w, errors.New(
				"GitHub issue synchronization is required before validation",
			))
			return
		}
	}
	cfg, err := loadRepositoryReviewLifecycleConfig(h.configPath)
	if err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	snapshot, err := repositoryMappingSnapshot(r.Context(), ledger.Store, cfg, ledger.Automation)
	if err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	if snapshot.Model == "" || snapshot.Account == "" {
		writeRepositoryReviewAutomationError(w, errors.New("repository validation model is unavailable"))
		return
	}
	state, jobs, err := ledger.Store.ReserveValidationJobs(
		ledger.State.Repository, request.RepositoryFindingIDs, snapshot,
	)
	if err != nil {
		writeRepositoryReviewError(w, err)
		return
	}
	writeRepositoryReviewJSON(w, http.StatusAccepted, map[string]any{
		"automation": projectRepositoryReviewAutomation(ledger.Automation),
		"repository": repoaudit.Summarize(state), "validation_jobs": jobs,
	})
}

func (h *Handler) handleSyncRepositoryReviewFinding(w http.ResponseWriter, r *http.Request) {
	if err := validateRepositoryReviewMutation(r); err != nil || r.URL == nil ||
		r.URL.RawQuery != "" {
		writeRepositoryReviewError(w, errors.New("invalid repository finding sync request"))
		return
	}
	var request struct{}
	if err := decodeRepositoryReviewRequest(r, &request); err != nil {
		writeRepositoryReviewError(w, err)
		return
	}
	ledger, err := h.repositoryReviewAutomationLedger(r.Context(), r.PathValue("automation_id"))
	if err != nil || !ledger.Found {
		if err == nil {
			err = errors.New("repository review ledger not found")
		}
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	finding, found := repositoryReviewRepositoryFindingByID(
		ledger.State, r.PathValue("repository_finding_id"),
	)
	if !found {
		writeRepositoryReviewAutomationError(w, errors.New("repository finding not found"))
		return
	}
	body, _ := json.Marshal(map[string]any{"expected_version": finding.Version})
	upstream := "/runtime/repository-reviews/automations/" + r.PathValue("automation_id") +
		"/repository-findings/" + r.PathValue("repository_finding_id") + "/sync"
	h.proxyPRWorkspaceGateway(w, r, http.MethodPost, upstream, "", body, time.Minute)
}
