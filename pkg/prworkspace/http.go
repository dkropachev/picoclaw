package prworkspace

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
)

const (
	// RuntimeRoutePrefix is the sole protected runtime subtree for both PR
	// review and implementation. The launcher exposes the same contract under
	// /api/pr-workspaces.
	RuntimeRoutePrefix = "/runtime/eventing/pr-workspaces"
	maxHTTPBodyBytes   = 1 << 20
)

// HTTPConfig freezes the runtime dependencies used by mutating endpoints.
// Missing optional side-effect capabilities leave their routes unavailable;
// they never silently weaken an authorization or validation decision.
type HTTPConfig struct {
	Service               *Service
	Implementation        ImplementationConfig
	IssuePublisher        IssuePublisher
	ReviewPublisher       ReviewPublisher
	BranchPublisher       BranchPublisher
	ReviewNudgePolicy     NudgePolicy
	CompletionNudgePolicy NudgePolicy
	SizePolicy            SizePolicy
}

type DeferredIssueMode string

const (
	DeferredIssuesOff       DeferredIssueMode = "off"
	DeferredIssuesAsk       DeferredIssueMode = "ask"
	DeferredIssuesAutomatic DeferredIssueMode = "automatic"
)

func validDeferredIssueMode(value DeferredIssueMode) bool {
	return value == DeferredIssuesOff || value == DeferredIssuesAsk || value == DeferredIssuesAutomatic
}

// HTTPHandler serves the unified, version-fenced PR workspace contract.
type HTTPHandler struct {
	service               *Service
	implementation        ImplementationConfig
	issuePublisher        IssuePublisher
	reviewPublisher       ReviewPublisher
	branchPublisher       BranchPublisher
	reviewNudgePolicy     NudgePolicy
	completionNudgePolicy NudgePolicy
	sizePolicy            SizePolicy
}

func NewHTTPHandler(config HTTPConfig) (*HTTPHandler, error) {
	if config.Service == nil {
		return nil, errors.New("PR workspace service is required")
	}
	if config.ReviewNudgePolicy == (NudgePolicy{}) {
		config.ReviewNudgePolicy = DefaultNudgePolicy()
	}
	if config.CompletionNudgePolicy == (NudgePolicy{}) {
		config.CompletionNudgePolicy = DefaultNudgePolicy()
	}
	if err := config.ReviewNudgePolicy.Validate(); err != nil {
		return nil, err
	}
	if err := config.CompletionNudgePolicy.Validate(); err != nil {
		return nil, err
	}
	if config.SizePolicy == (SizePolicy{}) {
		config.SizePolicy = DefaultSizePolicy()
	}
	if !config.SizePolicy.Valid() {
		return nil, errors.New("invalid PR workspace size policy")
	}
	return &HTTPHandler{
		service: config.Service, implementation: config.Implementation,
		issuePublisher: config.IssuePublisher, reviewPublisher: config.ReviewPublisher,
		branchPublisher:       config.BranchPublisher,
		reviewNudgePolicy:     config.ReviewNudgePolicy,
		completionNudgePolicy: config.CompletionNudgePolicy,
		sizePolicy:            config.SizePolicy,
	}, nil
}

func (handler *HTTPHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if handler == nil || handler.service == nil || !canonicalHTTPRequest(request) {
		writeHTTPError(response, http.StatusBadRequest, "invalid_request", nil)
		return
	}
	segments, ok := workspaceRouteSegments(request.URL.Path)
	if !ok {
		writeHTTPError(response, http.StatusNotFound, "not_found", nil)
		return
	}
	if len(segments) == 0 {
		handler.serveRoot(response, request)
		return
	}
	workspaceID := segments[0]
	if !validOpaqueID(workspaceID, "prw_") {
		writeHTTPError(response, http.StatusBadRequest, "invalid_workspace_id", nil)
		return
	}
	handler.serveWorkspace(response, request, workspaceID, segments[1:])
}

func (handler *HTTPHandler) serveRoot(response http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		filter, err := listFilterFromQuery(request.URL.Query())
		if err != nil {
			writeHTTPResult(response, Aggregate{}, err)
			return
		}
		page, err := handler.service.List(request.Context(), filter)
		if err != nil {
			writeHTTPResult(response, Aggregate{}, err)
			return
		}
		result := struct {
			Workspaces []Workspace `json:"workspaces"`
			NextCursor string      `json:"next_cursor,omitempty"`
		}{Workspaces: page.Workspaces}
		if page.Next != nil {
			result.NextCursor = encodeWorkspaceCursor(*page.Next)
		}
		writeHTTPJSON(response, http.StatusOK, result)
	case http.MethodPost:
		var body struct {
			RequestID      string `json:"request_id"`
			PullRequestURL string `json:"pull_request_url"`
			ProviderOrigin string `json:"provider_origin"`
			Repository     string `json:"repository"`
			PullNumber     int64  `json:"pull_number"`
		}
		if !decodeHTTPBody(response, request, &body) {
			return
		}
		aggregate, err := handler.service.Create(request.Context(), CreateWorkspaceRequest{
			RequestID: body.RequestID,
			Resolve: ResolveRequest{
				PullRequestURL: body.PullRequestURL, ProviderOrigin: body.ProviderOrigin,
				Repository: body.Repository, PullNumber: body.PullNumber,
			},
		})
		writeHTTPResultStatus(response, aggregate, err, http.StatusCreated)
	default:
		writeHTTPMethod(response, http.MethodGet, http.MethodPost)
	}
}

func (handler *HTTPHandler) serveWorkspace(
	response http.ResponseWriter,
	request *http.Request,
	workspaceID string,
	tail []string,
) {
	if len(tail) == 0 {
		if request.Method != http.MethodGet {
			writeHTTPMethod(response, http.MethodGet)
			return
		}
		aggregate, err := handler.service.Get(request.Context(), workspaceID)
		writeHTTPResult(response, aggregate, err)
		return
	}
	if request.URL.RawQuery != "" {
		writeHTTPError(response, http.StatusBadRequest, "invalid_query", nil)
		return
	}
	switch tail[0] {
	case "refresh":
		handler.serveRefresh(response, request, workspaceID, tail)
	case "charter":
		handler.serveCharter(response, request, workspaceID, tail[1:])
	case "review-runs", "implementation-runs", "completion-audits", "nudge-runs":
		handler.serveRun(response, request, workspaceID, tail)
	case "stage-runs":
		handler.serveStageRun(response, request, workspaceID, tail)
	case "findings":
		handler.serveFinding(response, request, workspaceID, tail)
	case "corrections":
		handler.serveCorrection(response, request, workspaceID, tail)
	case "messages":
		handler.serveMessage(response, request, workspaceID, tail)
	case "deferred-groups":
		handler.serveDeferred(response, request, workspaceID, tail)
	case "gates":
		handler.serveGate(response, request, workspaceID, tail)
	case "publications":
		handler.servePublication(response, request, workspaceID, tail)
	default:
		writeHTTPError(response, http.StatusNotFound, "not_found", nil)
	}
}

type mutationFence struct {
	ExpectedVersion int64  `json:"expected_version"`
	RequestID       string `json:"request_id"`
}

func (handler *HTTPHandler) serveRefresh(
	response http.ResponseWriter,
	request *http.Request,
	workspaceID string,
	tail []string,
) {
	if len(tail) != 1 || request.Method != http.MethodPost {
		writeHTTPMethod(response, http.MethodPost)
		return
	}
	var body mutationFence
	if !decodeHTTPBody(response, request, &body) {
		return
	}
	aggregate, err := handler.service.RefreshProvider(request.Context(), RefreshProviderRequest{
		WorkspaceID: workspaceID, ExpectedVersion: body.ExpectedVersion, RequestID: body.RequestID,
	})
	writeHTTPResult(response, aggregate, err)
}

type charterHTTPBody struct {
	mutationFence
	ExpectedHeadRevision    string   `json:"expected_head_revision"`
	ExpectedCharterRevision int64    `json:"expected_charter_revision"`
	PRType                  PRType   `json:"pr_type"`
	Goal                    string   `json:"goal"`
	AcceptanceCriteria      []string `json:"acceptance_criteria"`
	IncludedAreas           []string `json:"included_areas"`
	Exclusions              []string `json:"exclusions"`
	NonGoals                []string `json:"non_goals"`
}

func (handler *HTTPHandler) serveCharter(
	response http.ResponseWriter,
	request *http.Request,
	workspaceID string,
	tail []string,
) {
	var body charterHTTPBody
	if !decodeHTTPBody(response, request, &body) {
		return
	}
	if len(tail) == 1 && tail[0] == "draft" && request.Method == http.MethodPost {
		if !handler.matchHeadRevision(response, request, workspaceID, body.ExpectedVersion, body.ExpectedHeadRevision) {
			return
		}
		aggregate, err := handler.service.DraftCharter(request.Context(), DraftCharterRequest{
			WorkspaceID: workspaceID, ExpectedVersion: body.ExpectedVersion, RequestID: body.RequestID,
		})
		writeHTTPResult(response, aggregate, err)
		return
	}
	draft := CharterDraftOutput{
		Type: body.PRType, Goal: body.Goal, AcceptanceCriteria: body.AcceptanceCriteria,
		IncludedAreas: body.IncludedAreas, ExcludedAreas: body.Exclusions, NonGoals: body.NonGoals,
	}
	if len(tail) == 0 && request.Method == http.MethodPut {
		if !handler.matchHeadRevision(response, request, workspaceID, body.ExpectedVersion, body.ExpectedHeadRevision) {
			return
		}
		aggregate, err := handler.service.SaveCharter(request.Context(), SaveCharterRequest{
			WorkspaceID: workspaceID, ExpectedVersion: body.ExpectedVersion,
			RequestID: body.RequestID, Draft: draft,
		})
		writeHTTPResult(response, aggregate, err)
		return
	}
	if len(tail) == 1 && tail[0] == "revise" && request.Method == http.MethodPost {
		if !handler.matchHeadRevision(response, request, workspaceID, body.ExpectedVersion, body.ExpectedHeadRevision) {
			return
		}
		aggregate, err := handler.service.ReviseCharter(request.Context(), ReviseCharterRequest{
			SaveCharterRequest: SaveCharterRequest{
				WorkspaceID:     workspaceID,
				ExpectedVersion: body.ExpectedVersion,
				RequestID:       body.RequestID,
				Draft:           draft,
			},
			ExpectedCharterRevision: body.ExpectedCharterRevision,
		})
		writeHTTPResult(response, aggregate, err)
		return
	}
	if len(tail) == 1 && tail[0] == "confirm" && request.Method == http.MethodPost {
		aggregate, err := handler.service.Get(request.Context(), workspaceID)
		if err != nil {
			writeHTTPResult(response, aggregate, err)
			return
		}
		charter, ok := charterAtRevision(aggregate.Charters, body.ExpectedCharterRevision)
		if !ok {
			writeHTTPResult(response, aggregate, ErrConflict)
			return
		}
		aggregate, err = handler.service.ConfirmCharter(request.Context(), ConfirmCharterRequest{
			WorkspaceID: workspaceID, CharterID: charter.ID,
			ExpectedVersion: body.ExpectedVersion, RequestID: body.RequestID,
		})
		writeHTTPResult(response, aggregate, err)
		return
	}
	writeHTTPMethod(response, http.MethodPost, http.MethodPut)
}

type runHTTPBody struct {
	mutationFence
	ExpectedHeadRevision string     `json:"expected_head_revision"`
	FindingIDs           []string   `json:"finding_ids"`
	Instruction          string     `json:"instruction"`
	Stage                NudgeStage `json:"stage"`
}

func (handler *HTTPHandler) serveRun(
	response http.ResponseWriter,
	request *http.Request,
	workspaceID string,
	tail []string,
) {
	if len(tail) != 1 || request.Method != http.MethodPost {
		writeHTTPMethod(response, http.MethodPost)
		return
	}
	var body runHTTPBody
	if !decodeHTTPBody(response, request, &body) {
		return
	}
	if tail[0] != "nudge-runs" &&
		!handler.matchHeadRevision(response, request, workspaceID, body.ExpectedVersion, body.ExpectedHeadRevision) {
		return
	}
	var aggregate Aggregate
	var err error
	switch tail[0] {
	case "review-runs":
		aggregate, err = handler.service.RunReview(request.Context(), RunReviewRequest{
			WorkspaceID: workspaceID, ExpectedVersion: body.ExpectedVersion,
			RequestID: body.RequestID, NudgePolicy: handler.reviewNudgePolicy,
		})
	case "implementation-runs":
		if handler.implementation.Repair == nil || handler.implementation.Validation == nil {
			writeHTTPError(response, http.StatusServiceUnavailable, "implementation_unavailable", nil)
			return
		}
		aggregate, err = handler.service.RunImplementation(
			request.Context(),
			handler.implementation,
			RunImplementationRequest{
				WorkspaceID: workspaceID, ExpectedVersion: body.ExpectedVersion,
				RequestID: body.RequestID, FindingIDs: body.FindingIDs,
				NudgePolicy: handler.completionNudgePolicy, SizePolicy: handler.sizePolicy,
			},
		)
	case "completion-audits":
		aggregate, err = handler.service.RunCompletionAudit(request.Context(), RunCompletionAuditRequest{
			WorkspaceID: workspaceID, ExpectedVersion: body.ExpectedVersion,
			RequestID: body.RequestID, NudgePolicy: handler.completionNudgePolicy,
		})
	case "nudge-runs":
		aggregate, err = handler.service.RunNudge(request.Context(), RunNudgeRequest{
			WorkspaceID: workspaceID, ExpectedVersion: body.ExpectedVersion,
			RequestID: body.RequestID, Stage: body.Stage,
		})
	}
	if err == nil {
		var automatic Aggregate
		automatic, err = handler.applyDeferredIssuePolicy(request, aggregate, body.RequestID)
		if automatic.Workspace.ID != "" {
			aggregate = automatic
		}
	}
	writeHTTPResult(response, aggregate, err)
}

func (handler *HTTPHandler) serveStageRun(
	response http.ResponseWriter,
	request *http.Request,
	workspaceID string,
	tail []string,
) {
	if len(tail) != 3 || tail[2] != "cancel" || request.Method != http.MethodPost {
		writeHTTPMethod(response, http.MethodPost)
		return
	}
	var body mutationFence
	if !decodeHTTPBody(response, request, &body) {
		return
	}
	aggregate, err := handler.service.CancelStage(request.Context(), CancelStageRequest{
		WorkspaceID: workspaceID, StageRunID: tail[1], ExpectedVersion: body.ExpectedVersion, RequestID: body.RequestID,
	})
	writeHTTPResult(response, aggregate, err)
}

type findingHTTPBody struct {
	mutationFence
	Disposition    FindingDisposition `json:"disposition"`
	Reason         string             `json:"reason"`
	Title          string             `json:"title"`
	Message        string             `json:"message"`
	Evidence       string             `json:"evidence"`
	Severity       string             `json:"severity"`
	ScopeDistance  ScopeDistance      `json:"scope_distance"`
	Size           ChangeSize         `json:"size"`
	TypeCompatible bool               `json:"type_compatible"`
}

func (handler *HTTPHandler) serveFinding(
	response http.ResponseWriter,
	request *http.Request,
	workspaceID string,
	tail []string,
) {
	if len(tail) < 2 || len(tail) > 3 {
		writeHTTPError(response, http.StatusNotFound, "not_found", nil)
		return
	}
	var body findingHTTPBody
	if !decodeHTTPBody(response, request, &body) {
		return
	}
	aggregate, err := handler.service.Get(request.Context(), workspaceID)
	if err != nil {
		writeHTTPResult(response, aggregate, err)
		return
	}
	finding, index := findFinding(aggregate.Findings, tail[1])
	if index < 0 {
		writeHTTPError(response, http.StatusNotFound, "finding_not_found", nil)
		return
	}
	if len(tail) == 3 && tail[2] == "disposition" && request.Method == http.MethodPost {
		aggregate, err = handler.service.DecideFinding(request.Context(), FindingDecisionRequest{
			WorkspaceID: workspaceID, FindingID: tail[1], ExpectedVersion: body.ExpectedVersion,
			RequestID: body.RequestID, Disposition: body.Disposition, Scope: finding.Scope, Reason: body.Reason,
		})
		if err == nil {
			var automatic Aggregate
			automatic, err = handler.applyDeferredIssuePolicy(request, aggregate, body.RequestID)
			if automatic.Workspace.ID != "" {
				aggregate = automatic
			}
		}
		writeHTTPResult(response, aggregate, err)
		return
	}
	if len(tail) == 2 && request.Method == http.MethodPatch {
		scope := finding.Scope
		scope.Distance, scope.Size, scope.TypeCompatible = body.ScopeDistance, body.Size, body.TypeCompatible
		aggregate, err = handler.service.UpdateFinding(request.Context(), UpdateFindingRequest{
			WorkspaceID: workspaceID, FindingID: tail[1], ExpectedVersion: body.ExpectedVersion,
			RequestID: body.RequestID, Severity: body.Severity, Title: body.Title,
			Message: body.Message, Evidence: body.Evidence, Scope: scope,
		})
		writeHTTPResult(response, aggregate, err)
		return
	}
	writeHTTPMethod(response, http.MethodPost, http.MethodPatch)
}

type correctionHTTPBody struct {
	mutationFence
	Kind          CorrectionKind          `json:"kind"`
	Applicability CorrectionApplicability `json:"applicability"`
	TargetID      string                  `json:"target_id"`
	OriginalClaim string                  `json:"original_claim"`
	Correction    string                  `json:"correction"`
	Reason        string                  `json:"reason"`
}

func (handler *HTTPHandler) serveCorrection(
	response http.ResponseWriter,
	request *http.Request,
	workspaceID string,
	tail []string,
) {
	var body correctionHTTPBody
	if !decodeHTTPBody(response, request, &body) {
		return
	}
	if len(tail) == 1 && request.Method == http.MethodPost {
		targetType := "workspace"
		targetID := body.TargetID
		if body.TargetID != "" {
			targetType = "finding"
		} else {
			targetID = workspaceID
		}
		aggregate, err := handler.service.AddCorrection(request.Context(), AddCorrectionRequest{
			WorkspaceID: workspaceID, ExpectedVersion: body.ExpectedVersion, RequestID: body.RequestID,
			Correction: Correction{
				Kind: body.Kind, Applicability: body.Applicability,
				TargetType: targetType, TargetID: targetID, OriginalClaim: body.OriginalClaim,
				Correction: body.Correction, Evidence: body.Reason,
			},
		})
		writeHTTPResult(response, aggregate, err)
		return
	}
	if len(tail) == 3 && tail[2] == "promote" && request.Method == http.MethodPost {
		aggregate, err := handler.service.PromoteCorrection(request.Context(), PromoteCorrectionRequest{
			WorkspaceID:     workspaceID,
			CorrectionID:    tail[1],
			ExpectedVersion: body.ExpectedVersion,
			RequestID:       body.RequestID,
		})
		writeHTTPResult(response, aggregate, err)
		return
	}
	writeHTTPMethod(response, http.MethodPost)
}

type messageHTTPBody struct {
	mutationFence
	Content          string                  `json:"content"`
	Stage            string                  `json:"stage"`
	MarkAsCorrection bool                    `json:"mark_as_correction"`
	Applicability    CorrectionApplicability `json:"applicability"`
}

func (handler *HTTPHandler) serveMessage(
	response http.ResponseWriter,
	request *http.Request,
	workspaceID string,
	tail []string,
) {
	if len(tail) != 1 || request.Method != http.MethodPost {
		writeHTTPMethod(response, http.MethodPost)
		return
	}
	var body messageHTTPBody
	if !decodeHTTPBody(response, request, &body) {
		return
	}
	aggregate, err := handler.service.AddMessage(request.Context(), AddMessageRequest{
		WorkspaceID: workspaceID, ExpectedVersion: body.ExpectedVersion,
		RequestID: body.RequestID, Stage: body.Stage, Content: body.Content,
		MarkAsCorrection: body.MarkAsCorrection, Applicability: body.Applicability,
	})
	writeHTTPResult(response, aggregate, err)
}

type deferredHTTPBody struct {
	mutationFence
	Title            string   `json:"title"`
	Body             string   `json:"body"`
	Labels           []string `json:"labels"`
	FindingIDs       []string `json:"finding_ids"`
	GroupIDs         []string `json:"group_ids"`
	ExistingIssueURL string   `json:"existing_issue_url"`
	PublicationID    string   `json:"publication_id"`
}

func (handler *HTTPHandler) serveDeferred(
	response http.ResponseWriter,
	request *http.Request,
	workspaceID string,
	tail []string,
) {
	var body deferredHTTPBody
	if !decodeHTTPBody(response, request, &body) {
		return
	}
	if len(tail) == 2 && tail[1] == "regroup" && request.Method == http.MethodPost {
		aggregate, err := handler.service.RegroupDeferred(request.Context(), RegroupDeferredRequest{
			WorkspaceID: workspaceID, ExpectedVersion: body.ExpectedVersion, RequestID: body.RequestID,
		})
		writeHTTPResult(response, aggregate, err)
		return
	}
	if len(tail) == 2 && tail[1] == "automatic-sync" && request.Method == http.MethodPost {
		if !validMutationEnvelope(workspaceID, body.ExpectedVersion, body.RequestID) {
			writeHTTPResult(response, Aggregate{}, ErrInvalid)
			return
		}
		aggregate, err := handler.service.Get(request.Context(), workspaceID)
		if err == nil && handler.service.deferredMode(aggregate) != DeferredIssuesAutomatic {
			writeHTTPError(response, http.StatusConflict, "automatic_deferred_issues_disabled", nil)
			return
		}
		if err == nil && aggregate.Workspace.Version != body.ExpectedVersion {
			err = ErrConflict
		}
		if err == nil {
			aggregate, err = handler.applyDeferredIssuePolicy(request, aggregate, body.RequestID)
		}
		writeHTTPResult(response, aggregate, err)
		return
	}
	if len(tail) < 2 || len(tail) > 3 {
		writeHTTPError(response, http.StatusNotFound, "not_found", nil)
		return
	}
	groupID := tail[1]
	if len(tail) == 2 && request.Method == http.MethodPatch {
		aggregate, err := handler.service.UpdateDeferred(request.Context(), UpdateDeferredRequest{
			WorkspaceID: workspaceID, GroupID: groupID, ExpectedVersion: body.ExpectedVersion,
			RequestID: body.RequestID, Title: body.Title, Body: body.Body, Labels: body.Labels,
		})
		writeHTTPResult(response, aggregate, err)
		return
	}
	if len(tail) != 3 || request.Method != http.MethodPost {
		writeHTTPMethod(response, http.MethodPost, http.MethodPatch)
		return
	}
	var aggregate Aggregate
	var err error
	switch tail[2] {
	case "split":
		aggregate, err = handler.service.SplitDeferred(request.Context(), SplitDeferredRequest{
			WorkspaceID: workspaceID, GroupID: groupID, ExpectedVersion: body.ExpectedVersion,
			RequestID: body.RequestID, FindingIDs: body.FindingIDs,
		})
	case "merge":
		groupIDs := body.GroupIDs
		if len(groupIDs) == 0 {
			groupIDs = append(groupIDs, groupID)
		}
		aggregate, err = handler.service.MergeDeferred(request.Context(), MergeDeferredRequest{
			WorkspaceID: workspaceID, GroupIDs: groupIDs, ExpectedVersion: body.ExpectedVersion,
			RequestID: body.RequestID, Title: body.Title, Body: body.Body,
		})
	case "link":
		aggregate, err = handler.service.LinkDeferred(request.Context(), LinkDeferredRequest{
			WorkspaceID: workspaceID, GroupID: groupID, ExpectedVersion: body.ExpectedVersion,
			RequestID: body.RequestID, ExistingIssueURL: body.ExistingIssueURL,
		})
	case "publish":
		current, getErr := handler.service.Get(request.Context(), workspaceID)
		if getErr != nil {
			writeHTTPResult(response, current, getErr)
			return
		}
		if handler.service.deferredMode(current) == DeferredIssuesOff {
			writeHTTPError(response, http.StatusConflict, "deferred_issue_publication_disabled", nil)
			return
		}
		if handler.issuePublisher == nil {
			writeHTTPError(response, http.StatusServiceUnavailable, "issue_publisher_unavailable", nil)
			return
		}
		aggregate, err = handler.service.QueueDeferredPublication(request.Context(), QueueDeferredPublicationRequest{
			WorkspaceID:     workspaceID,
			GroupID:         groupID,
			ExpectedVersion: body.ExpectedVersion,
			RequestID:       body.RequestID,
		})
	case "reconcile":
		if handler.issuePublisher == nil {
			writeHTTPError(response, http.StatusServiceUnavailable, "issue_publisher_unavailable", nil)
			return
		}
		publicationID := body.PublicationID
		if publicationID == "" {
			current, getErr := handler.service.Get(request.Context(), workspaceID)
			if getErr != nil {
				writeHTTPResult(response, current, getErr)
				return
			}
			group, ok := findDeferredGroup(current.DeferredGroups, groupID)
			if !ok {
				writeHTTPError(response, http.StatusNotFound, "deferred_group_not_found", nil)
				return
			}
			publicationID = group.PublicationID
		}
		aggregate, err = handler.service.ReconcileIssuePublication(
			request.Context(),
			handler.issuePublisher,
			ReconcileIssuePublicationRequest{
				WorkspaceID: workspaceID, PublicationID: publicationID,
				ExpectedVersion: body.ExpectedVersion, RequestID: body.RequestID,
			},
		)
	default:
		writeHTTPError(response, http.StatusNotFound, "not_found", nil)
		return
	}
	writeHTTPResult(response, aggregate, err)
}

type gateHTTPBody struct {
	mutationFence
	FieldValues map[string]any `json:"field-values"`
}

func (handler *HTTPHandler) serveGate(
	response http.ResponseWriter,
	request *http.Request,
	workspaceID string,
	tail []string,
) {
	if len(tail) == 1 && request.Method == http.MethodGet {
		aggregate, err := handler.service.Get(request.Context(), workspaceID)
		if err != nil {
			writeHTTPResult(response, aggregate, err)
			return
		}
		writeHTTPJSON(response, http.StatusOK, struct {
			Gates            []GateRun `json:"gates"`
			WorkspaceVersion int64     `json:"workspace_version"`
		}{Gates: aggregate.Gates, WorkspaceVersion: aggregate.Workspace.Version})
		return
	}
	if len(tail) != 3 || tail[2] != "respond" || request.Method != http.MethodPost {
		writeHTTPMethod(response, http.MethodGet, http.MethodPost)
		return
	}
	var body gateHTTPBody
	if !decodeHTTPBody(response, request, &body) {
		return
	}
	aggregate, err := handler.service.RespondGate(request.Context(), RespondGateRequest{
		WorkspaceID: workspaceID, GateRunID: tail[1], ExpectedVersion: body.ExpectedVersion,
		RequestID: body.RequestID, FieldValues: body.FieldValues,
	})
	if err == nil && handler.service.deferredMode(aggregate) == DeferredIssuesAutomatic {
		var automatic Aggregate
		automatic, err = handler.applyDeferredIssuePolicy(request, aggregate, body.RequestID)
		if automatic.Workspace.ID != "" {
			aggregate = automatic
		}
	}
	writeHTTPResult(response, aggregate, err)
}

type publicationHTTPBody struct {
	mutationFence
	ExpectedHeadRevision string   `json:"expected_head_revision"`
	FindingIDs           []string `json:"finding_ids"`
}

func (handler *HTTPHandler) servePublication(
	response http.ResponseWriter,
	request *http.Request,
	workspaceID string,
	tail []string,
) {
	if request.Method != http.MethodPost || len(tail) < 2 || len(tail) > 3 {
		writeHTTPMethod(response, http.MethodPost)
		return
	}
	var body publicationHTTPBody
	if !decodeHTTPBody(response, request, &body) {
		return
	}
	if !handler.matchHeadRevision(response, request, workspaceID, body.ExpectedVersion, body.ExpectedHeadRevision) {
		return
	}
	current, err := handler.service.Get(request.Context(), workspaceID)
	if err != nil {
		writeHTTPResult(response, current, err)
		return
	}
	var aggregate Aggregate
	if len(tail) == 2 {
		switch tail[1] {
		case "review":
			if handler.reviewPublisher == nil {
				writeHTTPError(response, http.StatusServiceUnavailable, "review_publisher_unavailable", nil)
				return
			}
			aggregate, err = handler.service.QueueReviewPublication(request.Context(), QueueReviewPublicationRequest{
				WorkspaceID: workspaceID, ExpectedVersion: body.ExpectedVersion,
				RequestID: body.RequestID, ExpectedHeadSHA: current.ProviderSnapshot.HeadSHA,
				FindingIDs: body.FindingIDs,
			})
		case "implementation":
			if handler.branchPublisher == nil {
				writeHTTPError(response, http.StatusServiceUnavailable, "branch_publisher_unavailable", nil)
				return
			}
			aggregate, err = handler.service.QueueBranchPublication(request.Context(), QueueBranchPublicationRequest{
				WorkspaceID: workspaceID, ExpectedVersion: body.ExpectedVersion,
				RequestID: body.RequestID, ExpectedHeadSHA: current.ProviderSnapshot.HeadSHA,
			})
		default:
			writeHTTPError(response, http.StatusNotFound, "not_found", nil)
			return
		}
		writeHTTPResult(response, aggregate, err)
		return
	}
	if tail[2] != "reconcile" {
		writeHTTPError(response, http.StatusNotFound, "not_found", nil)
		return
	}
	aggregate, err = handler.service.ReconcilePhasePublication(
		request.Context(),
		handler.reviewPublisher,
		handler.branchPublisher,
		ReconcilePhasePublicationRequest{
			WorkspaceID: workspaceID, PublicationID: tail[1], ExpectedVersion: body.ExpectedVersion,
			RequestID: body.RequestID,
		},
	)
	writeHTTPResult(response, aggregate, err)
}

func (handler *HTTPHandler) applyDeferredIssuePolicy(
	request *http.Request,
	aggregate Aggregate,
	requestID string,
) (Aggregate, error) {
	if handler.service.deferredMode(aggregate) != DeferredIssuesAutomatic {
		return aggregate, nil
	}
	if hasUngroupedDeferredFindings(aggregate) {
		next, err := handler.service.RegroupDeferred(request.Context(), RegroupDeferredRequest{
			WorkspaceID:     aggregate.Workspace.ID,
			ExpectedVersion: aggregate.Workspace.Version,
			RequestID:       stableID("req_", requestID, "automatic-deferred-regroup"),
		})
		if err != nil {
			return handler.deferredPolicyFailureAggregate(request, aggregate, next), err
		}
		aggregate = next
	}
	if handler.issuePublisher == nil || !aggregate.ProviderSnapshot.CanCreateIssue {
		return aggregate, nil
	}
	for _, group := range aggregate.DeferredGroups {
		if group.PublicationSuppressed || !activeDeferredGroupValid(aggregate, group) {
			continue
		}
		next, err := handler.service.QueueDeferredPublication(
			request.Context(),
			QueueDeferredPublicationRequest{
				WorkspaceID:     aggregate.Workspace.ID,
				GroupID:         group.ID,
				ExpectedVersion: aggregate.Workspace.Version,
				RequestID:       stableID("req_", requestID, "automatic-deferred-queue", group.ID),
			},
		)
		if err != nil {
			return handler.deferredPolicyFailureAggregate(request, aggregate, next), err
		}
		aggregate = next
	}
	return aggregate, nil
}

// deferredPolicyFailureAggregate preserves the newest known workspace state
// across subordinate failures. Some service failures happen before a mutation
// and therefore return an empty aggregate; others happen after an earlier group
// was queued successfully. Reloading here prevents the HTTP error response from
// hiding either the caller's retained state or a partially committed advance.
func (handler *HTTPHandler) deferredPolicyFailureAggregate(
	request *http.Request,
	retained, subordinate Aggregate,
) Aggregate {
	workspaceID := retained.Workspace.ID
	best := retained
	if subordinate.Workspace.ID == workspaceID && subordinate.Workspace.Version >= best.Workspace.Version {
		best = subordinate
	}
	current, err := handler.service.Get(request.Context(), workspaceID)
	if err == nil && current.Workspace.ID == workspaceID && current.Workspace.Version >= best.Workspace.Version {
		best = current
	}
	return best
}

func hasUngroupedDeferredFindings(aggregate Aggregate) bool {
	grouped := make(map[string]struct{})
	for _, group := range aggregate.DeferredGroups {
		for _, findingID := range group.FindingIDs {
			grouped[findingID] = struct{}{}
		}
	}
	for _, finding := range aggregate.Findings {
		if finding.Disposition == FindingDeferred {
			if _, exists := grouped[finding.ID]; !exists {
				return true
			}
		}
	}
	return false
}

func (handler *HTTPHandler) matchHeadRevision(
	response http.ResponseWriter,
	request *http.Request,
	workspaceID string,
	expectedVersion int64,
	expected string,
) bool {
	aggregate, err := handler.service.Get(request.Context(), workspaceID)
	if err != nil {
		writeHTTPResult(response, aggregate, err)
		return false
	}
	current := aggregate.ProviderSnapshot.ProviderRevision
	if current == "" {
		current = aggregate.ProviderSnapshot.HeadSHA
	}
	if aggregate.Workspace.Version != expectedVersion || expected == "" || expected != current {
		writeHTTPResult(response, aggregate, ErrConflict)
		return false
	}
	return true
}

func charterAtRevision(values []Charter, revision int64) (Charter, bool) {
	for _, value := range values {
		if value.Revision == revision {
			return value, true
		}
	}
	return Charter{}, false
}

func canonicalHTTPRequest(request *http.Request) bool {
	return request != nil && request.URL != nil && request.URL.Fragment == "" &&
		request.URL.EscapedPath() == request.URL.Path &&
		!strings.Contains(request.URL.Path, "//") &&
		!strings.Contains(request.URL.Path, "/./") &&
		!strings.Contains(request.URL.Path, "/../")
}

func workspaceRouteSegments(path string) ([]string, bool) {
	if path == RuntimeRoutePrefix {
		return nil, true
	}
	if !strings.HasPrefix(path, RuntimeRoutePrefix+"/") {
		return nil, false
	}
	tail := strings.TrimPrefix(path, RuntimeRoutePrefix+"/")
	if tail == "" || strings.HasSuffix(tail, "/") {
		return nil, false
	}
	segments := strings.Split(tail, "/")
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return nil, false
		}
	}
	return segments, true
}

func listFilterFromQuery(values url.Values) (ListFilter, error) {
	allowed := map[string]bool{
		"repository": true, "phase": true, "state": true, "ownership": true,
		"needs_action": true, "limit": true, "cursor": true,
	}
	for key, entries := range values {
		if !allowed[key] || len(entries) != 1 {
			return ListFilter{}, ErrInvalid
		}
	}
	filter := ListFilter{Repository: values.Get("repository")}
	if raw := values.Get("phase"); raw != "" {
		filter.Phase = Phase(raw)
		if !validPhase(filter.Phase) {
			return ListFilter{}, ErrInvalid
		}
	}
	if raw := values.Get("state"); raw != "" {
		filter.State = ExecutionState(raw)
		if !validExecutionState(filter.State) {
			return ListFilter{}, ErrInvalid
		}
	}
	if raw := values.Get("ownership"); raw != "" {
		owned := raw == "owned"
		if !owned && raw != "external" {
			return ListFilter{}, ErrInvalid
		}
		filter.Owned = &owned
	}
	if raw := values.Get("needs_action"); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return ListFilter{}, ErrInvalid
		}
		filter.NeedsAction = &value
	}
	if raw := values.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			return ListFilter{}, ErrInvalid
		}
		filter.Limit = limit
	}
	if raw := values.Get("cursor"); raw != "" {
		cursor, err := decodeWorkspaceCursor(raw)
		if err != nil {
			return ListFilter{}, ErrInvalid
		}
		filter.AfterUpdated, filter.AfterID = cursor.UpdatedAt, cursor.ID
	}
	return filter, nil
}

func encodeWorkspaceCursor(cursor WorkspaceCursor) string {
	raw, _ := json.Marshal(struct {
		UpdatedAt time.Time `json:"updated_at"`
		ID        string    `json:"id"`
	}{cursor.UpdatedAt, cursor.ID})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeWorkspaceCursor(value string) (WorkspaceCursor, error) {
	if len(value) > 1024 {
		return WorkspaceCursor{}, ErrInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return WorkspaceCursor{}, err
	}
	var decoded struct {
		UpdatedAt time.Time `json:"updated_at"`
		ID        string    `json:"id"`
	}
	if err = json.Unmarshal(
		raw,
		&decoded,
	); err != nil || decoded.UpdatedAt.IsZero() ||
		!validOpaqueID(decoded.ID, "prw_") {
		return WorkspaceCursor{}, ErrInvalid
	}
	return WorkspaceCursor{UpdatedAt: decoded.UpdatedAt, ID: decoded.ID}, nil
}

func decodeHTTPBody(response http.ResponseWriter, request *http.Request, target any) bool {
	if request.Body == nil || request.URL.RawQuery != "" ||
		!strings.HasPrefix(strings.ToLower(request.Header.Get("Content-Type")), "application/json") {
		writeHTTPError(response, http.StatusBadRequest, "invalid_request", nil)
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, maxHTTPBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeHTTPError(response, http.StatusBadRequest, "invalid_json", nil)
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeHTTPError(response, http.StatusBadRequest, "invalid_json", nil)
		return false
	}
	return true
}

func writeHTTPResult(response http.ResponseWriter, aggregate Aggregate, err error) {
	writeHTTPResultStatus(response, aggregate, err, http.StatusOK)
}

func writeHTTPResultStatus(response http.ResponseWriter, aggregate Aggregate, err error, success int) {
	if err == nil {
		writeHTTPJSON(response, success, publicAggregate(aggregate))
		return
	}
	status, code := http.StatusInternalServerError, "internal_error"
	switch {
	case errors.Is(err, ErrInvalid):
		status, code = http.StatusBadRequest, "invalid_request"
	case errors.Is(err, ErrNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, ErrConflict):
		status, code = http.StatusConflict, "version_conflict"
	case errors.Is(err, ErrRequestConflict):
		status, code = http.StatusConflict, "request_id_conflict"
	}
	if status == http.StatusInternalServerError {
		logger.ErrorCF("pr_workspace", "PR workspace request failed", map[string]any{
			"error": err.Error(),
		})
	}
	var current *Aggregate
	// A post-mutation orchestration step (for example automatic deferred-issue
	// queuing) can fail after the primary command committed. Always return the
	// authoritative aggregate when one is available so clients can refresh and
	// retry from the retained version instead of guessing whether work landed.
	if aggregate.Workspace.ID != "" {
		projected := publicAggregate(aggregate)
		current = &projected
	}
	writeHTTPError(response, status, code, current)
}

// publicAggregate makes the HTTP collection contract exact. Domain stores may
// use nil slices internally for an empty relation, but clients must never have
// to guess whether null means "empty" or "unavailable" after a sparse
// mutation. Every aggregate response therefore emits JSON arrays consistently.
func publicAggregate(aggregate Aggregate) Aggregate {
	if aggregate.Charters == nil {
		aggregate.Charters = []Charter{}
	}
	if aggregate.StageRuns == nil {
		aggregate.StageRuns = []StageRun{}
	}
	if aggregate.Findings == nil {
		aggregate.Findings = []Finding{}
	}
	if aggregate.Messages == nil {
		aggregate.Messages = []Message{}
	}
	if aggregate.Corrections == nil {
		aggregate.Corrections = []Correction{}
	}
	if aggregate.RepositoryLessons == nil {
		aggregate.RepositoryLessons = []RepositoryLesson{}
	}
	if aggregate.NudgeRounds == nil {
		aggregate.NudgeRounds = []NudgeRoundRecord{}
	}
	if aggregate.DeferredGroups == nil {
		aggregate.DeferredGroups = []DeferredGroup{}
	}
	if aggregate.RepairAttempts == nil {
		aggregate.RepairAttempts = []RepairAttempt{}
	}
	if aggregate.ValidationRuns == nil {
		aggregate.ValidationRuns = []ValidationRun{}
	}
	if aggregate.Gates == nil {
		aggregate.Gates = []GateRun{}
	}
	if aggregate.Publications == nil {
		aggregate.Publications = []Publication{}
	}
	if aggregate.Activity == nil {
		aggregate.Activity = []Activity{}
	}
	return aggregate
}

func writeHTTPError(response http.ResponseWriter, status int, code string, current *Aggregate) {
	message := strings.ReplaceAll(code, "_", " ")
	value := struct {
		Code    string     `json:"code"`
		Message string     `json:"message"`
		Current *Aggregate `json:"current,omitempty"`
	}{Code: code, Message: message, Current: current}
	writeHTTPJSON(response, status, value)
}

func writeHTTPMethod(response http.ResponseWriter, methods ...string) {
	response.Header().Set("Allow", strings.Join(methods, ", "))
	writeHTTPError(response, http.StatusMethodNotAllowed, "method_not_allowed", nil)
}

func writeHTTPJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func validPhase(value Phase) bool {
	switch value {
	case PhaseIntake, PhaseCharter, PhaseReview, PhaseTriage, PhaseImplementation,
		PhaseValidation, PhaseCompletionAudit, PhasePublication, PhaseComplete:
		return true
	default:
		return false
	}
}

func validExecutionState(value ExecutionState) bool {
	switch value {
	case ExecutionQueued, ExecutionRunning, ExecutionWaitingGate, ExecutionWaitingUser,
		ExecutionSucceeded, ExecutionFailed, ExecutionBlocked, ExecutionCanceled,
		ExecutionStale, ExecutionUnknown:
		return true
	default:
		return false
	}
}

var _ http.Handler = (*HTTPHandler)(nil)
