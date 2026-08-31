package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

type repositoryReviewConfirmedPublishRequest struct {
	ExpectedVersion int64 `json:"expected_version"`
	Confirmed       bool  `json:"confirmed"`
}

type repositoryReviewGatewayPublishRequest struct {
	ExpectedVersion int64 `json:"expected_version"`
}

type repositoryReviewDirectPostRequest struct {
	ExpectedVersion int64  `json:"expected_version"`
	Instructions    string `json:"instructions,omitempty"`
}

type repositoryReviewBatchPublishRequest struct {
	Issues []struct {
		ID              string `json:"id"`
		ExpectedVersion int64  `json:"expected_version"`
	} `json:"issues"`
	Confirmed bool `json:"confirmed"`
}

func (h *Handler) handleRepositoryReviewIssueLinkCandidates(w http.ResponseWriter, r *http.Request) {
	h.proxyRepositoryReviewAutomationGateway(w, r, "candidates")
}

func (h *Handler) handleRepositoryReviewIssueLink(w http.ResponseWriter, r *http.Request) {
	h.proxyRepositoryReviewAutomationGateway(w, r, "link")
}

// handlePostRepositoryReviewAutomationFinding is the explicit no-preview Post
// action. The click authorizes publication: the server resolves the current
// assigned profile, durably generates one draft, and immediately publishes the
// exact saved content through the existing marker/reconciliation boundary.
func (h *Handler) handlePostRepositoryReviewAutomationFinding(w http.ResponseWriter, r *http.Request) {
	if err := validateRepositoryReviewMutation(r); err != nil {
		writeRepositoryReviewError(w, err)
		return
	}
	var request repositoryReviewDirectPostRequest
	if err := decodeRepositoryReviewRequest(r, &request); err != nil || request.ExpectedVersion < 1 {
		writeRepositoryReviewError(w, errors.New("invalid repository review direct post request"))
		return
	}
	request.Instructions = strings.TrimSpace(request.Instructions)
	if !repositoryReviewValidGenerationText(request.Instructions, 16<<10, true) {
		writeRepositoryReviewError(w, errors.New("invalid repository review presentation instructions"))
		return
	}
	ledger, finding, err := h.repositoryReviewAutomationFinding(
		r.Context(), r.PathValue("automation_id"), r.PathValue("finding_id"),
	)
	if err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	if finding.Version != request.ExpectedVersion || finding.Status != repoaudit.FindingOpen ||
		finding.IssueDraftID != "" {
		writeRepositoryReviewError(w, repoaudit.ErrConflict)
		return
	}
	profile, err := h.repositoryReviewCurrentIssueProfile(r.Context(), ledger)
	if err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	generationID, err := newRepositoryReviewIssueGenerationID()
	if err != nil {
		writeRepositoryReviewError(w, err)
		return
	}
	mode := repoaudit.IssueDraftInstructionsDefault
	if request.Instructions != "" {
		mode = repoaudit.IssueDraftInstructionsCustom
	}
	resolved := repositoryReviewResolvedIssueInstructions(repositoryReviewGenerationRequest{
		InstructionsMode: mode,
		Instructions:     request.Instructions,
	}, profile.Prompt)
	draft, result := h.generateRepositoryReviewIssue(
		r.Context(), ledger, finding.ID, generationID, mode, resolved, profile.Account, profile,
	)
	if draft.ID == "" || draft.State != repoaudit.IssueDraftEditing {
		writeRepositoryReviewJSON(w, http.StatusBadGateway, map[string]any{
			"automation": projectRepositoryReviewAutomation(ledger.Automation),
			"finding":    projectRepositoryReviewRunFinding(ledger.State, finding), "result": result,
		})
		return
	}
	if current, found, getErr := ledger.Store.Get(ledger.State.Repository); getErr == nil && found {
		ledger.State = current
	}
	publication := h.publishRepositoryReviewAutomationDraft(r, ledger, draft.ID, draft.Version)
	current, found, err := ledger.Store.Get(ledger.State.Repository)
	if err != nil || !found {
		if err == nil {
			err = os.ErrNotExist
		}
		writeRepositoryReviewError(w, err)
		return
	}
	ledger.State = current
	posted, _ := repositoryReviewIssueByID(current, draft.ID)
	status := http.StatusOK
	if success, _ := publication["success"].(bool); !success && publication["outcome"] != "unknown" {
		status = http.StatusBadGateway
	}
	writeRepositoryReviewJSON(w, status, map[string]any{
		"automation": projectRepositoryReviewAutomation(ledger.Automation),
		"repository": repoaudit.Summarize(current), "issue": posted,
		"result": publication,
	})
}

func (h *Handler) proxyRepositoryReviewAutomationGateway(
	w http.ResponseWriter,
	r *http.Request,
	action string,
) {
	if err := validateRepositoryReviewMutation(r); err != nil || r == nil || r.URL == nil ||
		r.URL.RawQuery != "" || r.Body == nil || r.ContentLength > 32<<10 {
		writeRepositoryReviewError(w, errors.New("invalid repository review request"))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, (32<<10)+1))
	if err != nil || len(body) == 0 || len(body) > 32<<10 || !json.Valid(body) {
		writeRepositoryReviewError(w, errors.New("invalid repository review request"))
		return
	}
	upstream := "/runtime/repository-reviews/automations/" + r.PathValue("automation_id") +
		"/findings/" + r.PathValue("finding_id") + "/issue-link"
	if action == "candidates" {
		upstream += "/candidates"
	}
	h.proxyPRWorkspaceGateway(w, r, r.Method, upstream, "", body, 3*time.Minute)
}

func (h *Handler) handlePublishRepositoryReviewAutomationIssue(w http.ResponseWriter, r *http.Request) {
	if err := validateRepositoryReviewMutation(r); err != nil {
		writeRepositoryReviewError(w, err)
		return
	}
	var request repositoryReviewConfirmedPublishRequest
	if err := decodeRepositoryReviewRequest(r, &request); err != nil || !request.Confirmed {
		if err == nil {
			err = errors.New("issue publication confirmation is required")
		}
		writeRepositoryReviewError(w, err)
		return
	}
	ledger, draft, err := h.repositoryReviewAutomationIssue(
		r.Context(), r.PathValue("automation_id"), r.PathValue("draft_id"),
	)
	if err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	if request.ExpectedVersion < 1 || draft.Version != request.ExpectedVersion {
		writeRepositoryReviewError(w, repoaudit.ErrConflict)
		return
	}
	if draft.State != repoaudit.IssueDraftPosted {
		eligibility := repoaudit.EvaluateIssuePublication(ledger.State, draft)
		if !eligibility.CanPublish {
			writeRepositoryReviewPublicationEligibilityAPIError(w, eligibility)
			return
		}
	}
	body, _ := json.Marshal(repositoryReviewGatewayPublishRequest{ExpectedVersion: request.ExpectedVersion})
	upstream := "/runtime/repository-reviews/" + ledger.State.ID +
		"/issue-drafts/" + draft.ID + "/publish"
	recorder := newRepositoryReviewResponseRecorder()
	h.proxyPRWorkspaceGateway(recorder, r, http.MethodPost, upstream, "", body, time.Minute)
	if recorder.status < 200 || recorder.status >= 300 {
		for name, values := range recorder.header {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		w.WriteHeader(recorder.status)
		_, _ = w.Write(recorder.body)
		return
	}
	updated, found, err := ledger.Store.Get(ledger.State.Repository)
	if err != nil || !found {
		if err == nil {
			err = os.ErrNotExist
		}
		writeRepositoryReviewError(w, err)
		return
	}
	updatedDraft, found := repositoryReviewIssueByID(updated, draft.ID)
	if !found {
		writeRepositoryReviewError(w, os.ErrNotExist)
		return
	}
	ledger.State = updated
	response := repositoryReviewIssueDetail(ledger, updatedDraft)
	var gatewayPayload struct {
		Outcome string `json:"outcome"`
	}
	if json.Unmarshal(recorder.body, &gatewayPayload) == nil && gatewayPayload.Outcome != "" {
		response["outcome"] = gatewayPayload.Outcome
	}
	writeRepositoryReviewJSON(w, http.StatusOK, response)
}

func (h *Handler) handlePublishRepositoryReviewAutomationIssues(w http.ResponseWriter, r *http.Request) {
	if err := validateRepositoryReviewMutation(r); err != nil {
		writeRepositoryReviewError(w, err)
		return
	}
	var request repositoryReviewBatchPublishRequest
	if err := decodeRepositoryReviewRequest(r, &request); err != nil || !request.Confirmed ||
		len(request.Issues) == 0 || len(request.Issues) > 200 {
		if err == nil {
			err = errors.New("one to 200 confirmed issue publications are required")
		}
		writeRepositoryReviewError(w, err)
		return
	}
	ledger, err := h.repositoryReviewAutomationLedger(r.Context(), r.PathValue("automation_id"))
	if err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	if !ledger.Found {
		writeRepositoryReviewAutomationError(w, os.ErrNotExist)
		return
	}
	seen := make(map[string]struct{}, len(request.Issues))
	for index := range request.Issues {
		request.Issues[index].ID = strings.TrimSpace(request.Issues[index].ID)
		item := request.Issues[index]
		if item.ID == "" || item.ExpectedVersion < 1 {
			writeRepositoryReviewError(w, errors.New("invalid repository review issue publication selection"))
			return
		}
		if _, duplicate := seen[item.ID]; duplicate {
			writeRepositoryReviewError(w, errors.New("duplicate issue draft"))
			return
		}
		seen[item.ID] = struct{}{}
	}
	results := make([]map[string]any, 0, len(request.Issues))
	for _, item := range request.Issues {
		draft, found := repositoryReviewIssueByID(ledger.State, item.ID)
		eligibility := repoaudit.IssuePublicationEligibility{CanPublish: true}
		if found && draft.State != repoaudit.IssueDraftPosted {
			eligibility = repoaudit.EvaluateIssuePublication(ledger.State, draft)
		}
		var result map[string]any
		switch {
		case !found:
			result = repositoryReviewPublicationSelectionFailure(item.ID, "not_found")
		case draft.Version != item.ExpectedVersion:
			result = repositoryReviewPublicationSelectionFailure(item.ID, "stale_repository_review")
		case !eligibility.CanPublish:
			result = repositoryReviewPublicationEligibilityResult(item.ID, eligibility)
		default:
			result = h.publishRepositoryReviewAutomationDraft(
				r, ledger, item.ID, item.ExpectedVersion,
			)
		}
		results = append(results, result)
		if state, found, getErr := ledger.Store.Get(ledger.State.Repository); getErr == nil && found {
			ledger.State = state
		}
	}
	writeRepositoryReviewJSON(w, http.StatusOK, map[string]any{
		"automation": projectRepositoryReviewAutomation(ledger.Automation),
		"repository": repoaudit.Summarize(ledger.State), "results": results,
	})
}

func repositoryReviewPublicationSelectionFailure(draftID, code string) map[string]any {
	return map[string]any{
		"id": draftID, "draft_id": draftID, "success": false,
		"outcome": "failed", "code": code,
		"message": strings.ReplaceAll(code, "_", " "),
	}
}

func (h *Handler) publishRepositoryReviewAutomationDraft(
	r *http.Request,
	ledger repositoryReviewAutomationLedger,
	draftID string,
	expectedVersion int64,
) map[string]any {
	body, _ := json.Marshal(repositoryReviewGatewayPublishRequest{ExpectedVersion: expectedVersion})
	upstream := "/runtime/repository-reviews/" + ledger.State.ID +
		"/issue-drafts/" + draftID + "/publish"
	recorder := newRepositoryReviewResponseRecorder()
	h.proxyPRWorkspaceGateway(recorder, r, http.MethodPost, upstream, "", body, time.Minute)
	result := map[string]any{"id": draftID, "draft_id": draftID, "success": false}
	var payload struct {
		Draft           repoaudit.IssueDraft                `json:"draft"`
		Outcome         string                              `json:"outcome"`
		Code            string                              `json:"code"`
		Message         string                              `json:"message"`
		PublishBlockers []repoaudit.IssuePublicationBlocker `json:"publish_blockers"`
	}
	_ = json.Unmarshal(recorder.body, &payload)
	if payload.Draft.ID != "" {
		result["state"] = payload.Draft.State
		result["external_url"] = payload.Draft.ExternalURL
	}
	switch {
	case recorder.status >= 200 && recorder.status < 300 && payload.Outcome == "unknown":
		result["outcome"] = "unknown"
		result["code"] = "publication_unknown"
	case recorder.status >= 200 && recorder.status < 300:
		result["outcome"] = "posted"
		result["success"] = true
	default:
		result["outcome"] = "failed"
		result["code"] = payload.Code
		result["message"] = payload.Message
		result["publish_blockers"] = payload.PublishBlockers
	}
	return result
}

func repositoryReviewPublicationEligibilityResult(
	draftID string,
	eligibility repoaudit.IssuePublicationEligibility,
) map[string]any {
	result := repositoryReviewPublicationSelectionFailure(draftID, "publication_not_allowed")
	if len(eligibility.PublishBlockers) == 0 {
		return result
	}
	first := eligibility.PublishBlockers[0]
	result["code"] = first.Code
	result["message"] = first.Message
	result["publish_blockers"] = eligibility.PublishBlockers
	return result
}

func writeRepositoryReviewPublicationEligibilityAPIError(
	w http.ResponseWriter,
	eligibility repoaudit.IssuePublicationEligibility,
) {
	result := repositoryReviewPublicationEligibilityResult("", eligibility)
	delete(result, "id")
	delete(result, "draft_id")
	delete(result, "outcome")
	delete(result, "success")
	status := http.StatusConflict
	if len(eligibility.PublishBlockers) > 0 &&
		eligibility.PublishBlockers[0].Code == repoaudit.IssuePublicationRepositoryNotGitHub {
		status = http.StatusBadRequest
	}
	writeRepositoryReviewJSON(w, status, result)
}

type repositoryReviewResponseRecorder struct {
	header http.Header
	status int
	body   []byte
}

func newRepositoryReviewResponseRecorder() *repositoryReviewResponseRecorder {
	return &repositoryReviewResponseRecorder{header: make(http.Header), status: http.StatusOK}
}

func (recorder *repositoryReviewResponseRecorder) Header() http.Header { return recorder.header }

func (recorder *repositoryReviewResponseRecorder) WriteHeader(status int) { recorder.status = status }

func (recorder *repositoryReviewResponseRecorder) Write(body []byte) (int, error) {
	recorder.body = append(recorder.body, body...)
	return len(body), nil
}
