package prworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/sipeed/picoclaw/pkg/logger"
)

const (
	// RuntimeRoutePrefix is the sole protected runtime subtree for both PR
	// review and implementation. The launcher exposes the same contract under
	// /api/development-workspaces.
	RuntimeRoutePrefix = "/runtime/eventing/development-workspaces"
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
	switch workspaceID {
	case "notifications", "notification-views", "notification-settings", "push-subscriptions":
		handler.serveNotificationAPI(response, request, workspaceID, segments[1:])
		return
	}
	if workspaceID == "repositories" {
		if len(segments) == 1 && request.Method == http.MethodGet && request.URL.RawQuery == "" {
			repositories, err := handler.service.ListConfiguredRepositories(request.Context())
			if err != nil {
				writeHTTPError(response, http.StatusServiceUnavailable, "repositories_unavailable", nil)
				return
			}
			writeHTTPJSON(response, http.StatusOK, map[string]any{"repositories": repositories})
			return
		}
		if len(segments) == 2 && segments[1] == "resolve" && request.Method == http.MethodPost {
			var body struct {
				RepositoryURL string `json:"repository_url"`
			}
			if !decodeHTTPBody(response, request, &body) {
				return
			}
			repository, err := handler.service.VerifyConfiguredRepository(request.Context(), body.RepositoryURL)
			if err != nil {
				writeHTTPError(response, http.StatusBadRequest, "repository_unavailable", nil)
				return
			}
			writeHTTPJSON(response, http.StatusOK, repository)
			return
		}
		writeHTTPMethod(response, http.MethodGet, http.MethodPost)
		return
	}
	if !validOpaqueID(workspaceID, "devw_") {
		writeHTTPError(response, http.StatusBadRequest, "invalid_workspace_id", nil)
		return
	}
	handler.serveWorkspace(response, request, workspaceID, segments[1:])
}

func (handler *HTTPHandler) serveRoot(response http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		handler.serveWorkspaceCollection(response, request)
	case http.MethodPost:
		var body struct {
			Intent DevelopmentIntent `json:"intent"`
			Source *struct {
				Kind               SourceKind `json:"kind"`
				IssueURL           string     `json:"issue_url"`
				RepositoryIdentity string     `json:"repository_identity"`
				Content            string     `json:"content"`
			} `json:"source"`
			PullRequestURL string `json:"pull_request_url"`
			RequestID      string `json:"request_id"`
		}
		if !decodeHTTPBody(response, request, &body) {
			return
		}
		create := CreateWorkspaceRequest{
			RequestID: body.RequestID, Intent: body.Intent, PullRequestURL: body.PullRequestURL,
		}
		if body.Source != nil {
			create.SourceKind = body.Source.Kind
			create.IssueURL = body.Source.IssueURL
			create.RepositoryIdentity = body.Source.RepositoryIdentity
			create.Brief = body.Source.Content
		} else if body.Intent == IntentPickupPR {
			create.SourceKind = SourcePullRequest
		}
		aggregate, err := handler.service.Create(request.Context(), create)
		writeHTTPResultStatus(response, aggregate, err, http.StatusCreated)
	default:
		writeHTTPMethod(response, http.MethodGet, http.MethodPost)
	}
}

func (handler *HTTPHandler) advanceDevelopmentWorkspace(
	ctx context.Context, aggregate Aggregate, requestID string,
) (Aggregate, error) {
	if aggregate.Workspace.Phase == PhaseCharter && len(aggregate.Charters) == 0 {
		var err error
		aggregate, err = handler.service.DraftCharter(ctx, DraftCharterRequest{
			WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
			RequestID: requestID + ":charter",
		})
		if err != nil {
			return aggregate, err
		}
	}
	if aggregate.Workspace.Phase == PhaseCharter && aggregate.Workspace.ActiveCharterID == "" &&
		len(aggregate.Charters) > 0 {
		charter := aggregate.Charters[len(aggregate.Charters)-1]
		var err error
		aggregate, err = handler.service.ConfirmCharterAutomatically(ctx, ConfirmCharterRequest{
			WorkspaceID: aggregate.Workspace.ID, CharterID: charter.ID,
			ExpectedVersion: aggregate.Workspace.Version, RequestID: requestID + ":confirm",
		})
		if err != nil {
			return aggregate, err
		}
	}
	if aggregate.Workspace.Phase == PhasePlanning {
		var err error
		aggregate, err = handler.service.RunFeaturePlanning(ctx, RunFeaturePlanningRequest{
			WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
			RequestID: requestID + ":planning",
		})
		if err != nil {
			return aggregate, err
		}
	} else if aggregate.Workspace.Phase == PhaseReview {
		var err error
		aggregate, err = handler.service.RunReview(ctx, RunReviewRequest{
			WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
			RequestID: requestID + ":review", NudgePolicy: handler.reviewNudgePolicy,
		})
		if err != nil {
			return aggregate, err
		}
	}
	if (aggregate.Workspace.Phase == PhaseTriage || aggregate.Workspace.Phase == PhaseImplementation) &&
		!aggregateHasOpenFindings(aggregate) && handler.implementation.Repair != nil &&
		handler.implementation.Validation != nil {
		implemented, err := handler.service.RunImplementation(ctx, handler.implementation, RunImplementationRequest{
			WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
			RequestID:   requestID + ":implementation",
			NudgePolicy: handler.completionNudgePolicy, SizePolicy: handler.sizePolicy,
		})
		if err != nil {
			return implemented, err
		}
		return handler.maybeQueueBranchPublication(ctx, implemented, requestID+":publication")
	}
	return handler.maybeQueueBranchPublication(ctx, aggregate, requestID+":publication")
}

// AdvanceDevelopmentWorkspace resumes one durable autonomous workspace from
// its persisted phase. Gateway workers call it under an exact runtime lease;
// browser requests never need to remain connected while AI or Git work runs.
func (handler *HTTPHandler) AdvanceDevelopmentWorkspace(
	ctx context.Context, aggregate Aggregate, requestID string,
) (Aggregate, error) {
	return handler.advanceDevelopmentWorkspace(ctx, aggregate, requestID)
}

// AutonomousDevelopmentWorkspaceReady prevents the durable worker from
// repeatedly selecting lifecycle phases whose configured runtime cannot make
// progress. It intentionally says nothing about user-triggered operations.
func (handler *HTTPHandler) AutonomousDevelopmentWorkspaceReady(aggregate Aggregate) bool {
	if handler == nil {
		return false
	}
	switch aggregate.Workspace.Phase {
	case PhaseCharter:
		return len(aggregate.Charters) > 0 ||
			(handler.service != nil && handler.service.ai.Runner != nil)
	case PhasePlanning:
		return handler.service != nil && handler.service.ai.Runner != nil &&
			handler.service.planningEvidence != nil
	case PhaseReview:
		return handler.service != nil && handler.service.ai.Runner != nil &&
			handler.service.reviewEvidence != nil && handler.service.reviewWorkflow != nil
	case PhaseTriage, PhaseImplementation:
		return handler.implementation.Repair != nil && handler.implementation.Validation != nil
	case PhasePublication:
		return handler.branchPublisher != nil
	default:
		return true
	}
}

// AutonomousDevelopmentWorkspaceClaimRequired identifies operations that may
// invoke AI or edit a candidate. The worker durably claims these before the
// runtime call; pure lifecycle transitions remain single-CAS mutations.
func (handler *HTTPHandler) AutonomousDevelopmentWorkspaceClaimRequired(aggregate Aggregate) bool {
	if !handler.AutonomousDevelopmentWorkspaceReady(aggregate) {
		return false
	}
	switch aggregate.Workspace.Phase {
	case PhaseCharter:
		return len(aggregate.Charters) == 0
	case PhasePlanning, PhaseReview, PhaseTriage, PhaseImplementation:
		return true
	default:
		return false
	}
}

func aggregateHasOpenFindings(aggregate Aggregate) bool {
	for _, finding := range aggregate.Findings {
		if finding.Disposition == FindingOpen {
			return true
		}
	}
	return false
}

func (handler *HTTPHandler) serveWorkspace(
	response http.ResponseWriter,
	request *http.Request,
	workspaceID string,
	tail []string,
) {
	if len(tail) == 0 && request.URL.RawQuery != "" {
		writeHTTPError(response, http.StatusBadRequest, "invalid_query", nil)
		return
	}
	if len(tail) == 0 {
		if request.Method != http.MethodGet {
			writeHTTPMethod(response, http.MethodGet)
			return
		}
		aggregate, err := handler.service.Get(request.Context(), workspaceID)
		writeHTTPResult(response, aggregate, err)
		return
	}
	if request.URL.RawQuery != "" && tail[0] != "code" {
		writeHTTPError(response, http.StatusBadRequest, "invalid_query", nil)
		return
	}
	switch tail[0] {
	case "refresh":
		handler.serveRefresh(response, request, workspaceID, tail)
	case "charter":
		handler.serveCharter(response, request, workspaceID, tail[1:])
	case "planning-runs", "review-runs", "implementation-runs", "completion-audits", "nudge-runs":
		handler.serveRun(response, request, workspaceID, tail)
	case "stage-runs":
		handler.serveStageRun(response, request, workspaceID, tail)
	case "findings":
		handler.serveFinding(response, request, workspaceID, tail)
	case "corrections":
		handler.serveCorrection(response, request, workspaceID, tail)
	case "messages":
		handler.serveMessage(response, request, workspaceID, tail)
	case "conversation":
		handler.serveConversation(response, request, workspaceID, tail)
	case "code":
		handler.serveCode(response, request, workspaceID, tail)
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

func (handler *HTTPHandler) serveCode(
	response http.ResponseWriter,
	request *http.Request,
	workspaceID string,
	tail []string,
) {
	if request.Method != http.MethodGet || len(tail) != 2 {
		writeHTTPMethod(response, http.MethodGet)
		return
	}
	query := request.URL.Query()
	for key := range query {
		if key != "revision" && key != "candidate_revision" && key != "path" && key != "cursor" {
			writeHTTPError(response, http.StatusBadRequest, "invalid_query", nil)
			return
		}
	}
	var value any
	var err error
	switch tail[1] {
	case "tree":
		value, err = handler.service.ListCodeTree(
			request.Context(), workspaceID, query.Get("revision"), query.Get("path"), query.Get("cursor"),
		)
	case "blob":
		value, err = handler.service.ReadCodeBlob(
			request.Context(), workspaceID, query.Get("revision"), query.Get("candidate_revision"), query.Get("path"),
		)
	case "diff":
		value, err = handler.service.ReadCodeDiff(
			request.Context(),
			workspaceID,
			query.Get("revision"),
			query.Get("path"),
		)
	default:
		writeHTTPError(response, http.StatusNotFound, "not_found", nil)
		return
	}
	if err != nil {
		writeHTTPError(response, http.StatusConflict, "code_unavailable", nil)
		return
	}
	writeHTTPJSON(response, http.StatusOK, value)
}

func (handler *HTTPHandler) serveConversation(
	response http.ResponseWriter,
	request *http.Request,
	workspaceID string,
	tail []string,
) {
	if len(tail) != 2 || tail[1] != "messages" {
		writeHTTPError(response, http.StatusNotFound, "not_found", nil)
		return
	}
	switch request.Method {
	case http.MethodGet:
		page, err := handler.service.Conversation(request.Context(), workspaceID)
		if err != nil {
			writeHTTPResult(response, Aggregate{}, err)
			return
		}
		writeHTTPJSON(response, http.StatusOK, page)
	case http.MethodPost:
		var body struct {
			Mode              string `json:"mode"`
			Content           string `json:"content"`
			ExpectedRevision  int64  `json:"expected_revision"`
			RequestID         string `json:"request_id"`
			CandidateRevision string `json:"candidate_revision"`
		}
		if !decodeHTTPBody(response, request, &body) {
			return
		}
		page, err := handler.service.SendConversationMessage(request.Context(), ConversationMessageRequest{
			WorkspaceID: workspaceID, ExpectedRevision: body.ExpectedRevision,
			RequestID: body.RequestID, Mode: body.Mode, Content: body.Content,
			CandidateRevision: body.CandidateRevision,
		})
		if err != nil {
			writeHTTPResult(response, Aggregate{}, err)
			return
		}
		if body.Mode == "steer" {
			if aggregate, getErr := handler.service.Get(request.Context(), workspaceID); getErr == nil {
				_, _ = handler.maybeRunImplementation(request.Context(), aggregate, body.RequestID+":implementation")
			}
		}
		writeHTTPJSON(response, http.StatusCreated, page)
	default:
		writeHTTPMethod(response, http.MethodGet, http.MethodPost)
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
	case "planning-runs":
		aggregate, err = handler.service.RunFeaturePlanning(request.Context(), RunFeaturePlanningRequest{
			WorkspaceID: workspaceID, ExpectedVersion: body.ExpectedVersion, RequestID: body.RequestID,
		})
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
	if err == nil {
		aggregate, err = handler.maybeRunImplementation(
			request.Context(), aggregate, body.RequestID+":implementation",
		)
	}
	if err == nil {
		aggregate, err = handler.maybeQueueBranchPublication(
			request.Context(), aggregate, body.RequestID+":publication",
		)
	}
	writeHTTPResult(response, aggregate, err)
}

func (handler *HTTPHandler) maybeRunImplementation(
	ctx context.Context, aggregate Aggregate, requestID string,
) (Aggregate, error) {
	if handler.implementation.Repair == nil || handler.implementation.Validation == nil ||
		aggregateHasOpenFindings(aggregate) {
		return aggregate, nil
	}
	if aggregate.Workspace.Phase != PhaseTriage &&
		!(aggregate.Workspace.Phase == PhaseImplementation &&
			aggregate.Workspace.ExecutionState == ExecutionQueued) {
		return aggregate, nil
	}
	implemented, err := handler.service.RunImplementation(ctx, handler.implementation, RunImplementationRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: requestID, NudgePolicy: handler.completionNudgePolicy, SizePolicy: handler.sizePolicy,
	})
	if err != nil {
		return implemented, err
	}
	return handler.maybeQueueBranchPublication(ctx, implemented, requestID+":publication")
}

func (handler *HTTPHandler) maybeQueueBranchPublication(
	ctx context.Context, aggregate Aggregate, requestID string,
) (Aggregate, error) {
	if handler.branchPublisher == nil || aggregate.Workspace.Phase != PhasePublication {
		return aggregate, nil
	}
	for _, publication := range aggregate.Publications {
		if publication.Kind == PublicationBranchPush && publication.State != ExecutionStale &&
			publication.State != ExecutionFailed && publication.State != ExecutionCanceled {
			return aggregate, nil
		}
	}
	if _, found := latestPublishableRepair(aggregate, aggregate.ProviderSnapshot.HeadSHA); !found {
		return aggregate, nil
	}
	return handler.service.QueueBranchPublication(ctx, QueueBranchPublicationRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: requestID, ExpectedHeadSHA: aggregate.ProviderSnapshot.HeadSHA,
	})
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
		!request.URL.ForceQuery &&
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

func decodeHTTPBody(response http.ResponseWriter, request *http.Request, target any) bool {
	mediaType, _, mediaErr := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if request.Body == nil || request.URL.RawQuery != "" || mediaErr != nil ||
		!strings.EqualFold(mediaType, "application/json") {
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
	case PhaseIntake, PhaseCharter, PhasePlanning, PhaseReview, PhaseTriage, PhaseImplementation,
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
