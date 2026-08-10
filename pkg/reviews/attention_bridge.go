package reviews

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	sharedattention "github.com/sipeed/picoclaw/pkg/attention"
	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	AttentionStatusNone             = "none"
	AttentionStatusQueued           = "queued"
	AttentionStatusProcessing       = "processing"
	AttentionStatusWaiting          = "waiting"
	AttentionStatusContinuing       = "continuing"
	AttentionStatusRecoveryRequired = "recovery_required"
	AttentionStatusCompleted        = "completed"
	AttentionStatusNotRequired      = "not_required"
	AttentionStatusFailed           = "failed"

	attentionTaskFenceDomain  = "picoclaw.review-attention.task-fence.v1"
	attentionResponseIDDomain = "picoclaw.review-attention.response-id.v1"
)

// AttentionTurnView is the only human-task state deliberately projected into
// the review workbench. Private workflow, task, input, and session identities
// are intentionally unrepresentable.
type AttentionTurnView = sharedattention.ConversationTurnView

// AttentionView is the case-owned browser projection for one submitted-review
// attention occurrence.
type AttentionView struct {
	CaseVersion int64               `json:"case_version"`
	Status      string              `json:"status"`
	CanRespond  bool                `json:"can_respond"`
	Turns       []AttentionTurnView `json:"turns"`
}

// AttentionResponseRequest answers one exact projected attention turn without
// accepting any private workflow or task identity from the caller.
type AttentionResponseRequest struct {
	CaseID              string `json:"-"`
	ExpectedCaseVersion int64  `json:"expected_case_version"`
	ResponseToken       string `json:"response_token"`
	Response            string `json:"response"`
}

type AttentionBridgeConfig struct {
	Service  *Service
	Executor *workflows.Executor
	RunStore workflows.RunStore
}

type attentionBridgeReader interface {
	GetReviewAttentionTrigger(
		ctx context.Context,
		submissionID string,
	) (eventing.ReviewAttentionTrigger, error)
	GetReviewDecisionRun(
		ctx context.Context,
		key eventing.ReviewDecisionKey,
	) (eventing.ReviewDecisionRunLink, error)
}

// AttentionBridge projects and resumes only private attention runs already
// linked to an authoritative submitted review case.
type AttentionBridge struct {
	service      *Service
	reader       attentionBridgeReader
	conversation *sharedattention.ConversationEngine
}

type loadedAttention struct {
	view  AttentionView
	input *sharedattention.ConversationInput
}

func NewAttentionBridge(config AttentionBridgeConfig) (*AttentionBridge, error) {
	if config.Service == nil || config.Service.store == nil ||
		isNilWorkingContextValue(config.Service.store) {
		return nil, errors.New("review attention bridge service is required")
	}
	reader, ok := config.Service.store.(attentionBridgeReader)
	if !ok || isNilWorkingContextValue(reader) {
		return nil, errors.New("review store does not support attention projection")
	}
	if config.RunStore == nil || isNilWorkingContextValue(config.RunStore) {
		return nil, errors.New("review attention run store is required")
	}
	conversation, err := sharedattention.NewConversationEngine(
		sharedattention.ConversationEngineConfig{
			RunStore:         config.RunStore,
			Executor:         config.Executor,
			MaxResponseBytes: maxReviewChatBytes,
			ResponseIDDomain: attentionResponseIDDomain,
		},
	)
	if err != nil {
		return nil, errors.New("review attention conversation is unavailable")
	}
	return &AttentionBridge{
		service:      config.Service,
		reader:       reader,
		conversation: conversation,
	}, nil
}

// IsAttentionWorkflowRun reserves the exact internal review-attention
// workflow reference. Browser-generic workflow surfaces use this predicate to
// hide both valid runs and malformed impostors before observation or mutation.
func IsAttentionWorkflowRun(run *workflows.Run) bool {
	return run != nil && run.WorkflowRef == reviewAttentionWorkflowRef
}

func (bridge *AttentionBridge) Project(
	ctx context.Context,
	caseID string,
) (AttentionView, error) {
	normalizedCaseID := strings.TrimSpace(caseID)
	if caseID != normalizedCaseID {
		return AttentionView{}, ErrInvalidRequest
	}
	loaded, err := bridge.load(ctx, normalizedCaseID)
	if err != nil {
		return AttentionView{}, err
	}
	return loaded.view, nil
}

func (bridge *AttentionBridge) Respond(
	ctx context.Context,
	request AttentionResponseRequest,
) (AttentionView, error) {
	if bridge == nil || bridge.service == nil || bridge.reader == nil ||
		bridge.conversation == nil {
		return AttentionView{}, ErrUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	caseID := strings.TrimSpace(request.CaseID)
	if !validWorkingContextPrefixedHexID(caseID, "prc_") ||
		request.CaseID != caseID || request.ExpectedCaseVersion <= 0 ||
		!validAttentionResponseToken(request.ResponseToken) {
		return AttentionView{}, ErrInvalidRequest
	}
	response, err := normalizeHumanText(
		"attention response",
		request.Response,
		maxReviewChatBytes,
	)
	if err != nil {
		return AttentionView{}, err
	}
	loaded, err := bridge.load(ctx, caseID)
	if err != nil {
		return AttentionView{}, err
	}
	if loaded.view.CaseVersion != request.ExpectedCaseVersion {
		return AttentionView{}, eventing.ErrReviewConflict
	}
	if loaded.input == nil {
		return AttentionView{}, eventing.ErrReviewConflict
	}
	_, err = bridge.conversation.Respond(
		ctx,
		sharedattention.ConversationResponse{
			ConversationInput:   *loaded.input,
			ExpectedCaseVersion: request.ExpectedCaseVersion,
			ResponseToken:       request.ResponseToken,
			Response:            response,
		},
	)
	if err != nil {
		return AttentionView{}, sanitizeAttentionBridgeError(ctx, err)
	}
	// Re-read the product-owned link after continuation so the returned view is
	// still rooted in the authoritative submitted-review occurrence.
	after, err := bridge.load(context.WithoutCancel(ctx), caseID)
	if err != nil {
		return AttentionView{}, sanitizeAttentionBridgeError(ctx, err)
	}
	return after.view, nil
}

func (bridge *AttentionBridge) load(
	ctx context.Context,
	caseID string,
) (loadedAttention, error) {
	if bridge == nil || bridge.service == nil || bridge.reader == nil ||
		bridge.conversation == nil {
		return loadedAttention{}, ErrUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	caseID = strings.TrimSpace(caseID)
	if !validWorkingContextPrefixedHexID(caseID, "prc_") {
		return loadedAttention{}, ErrInvalidRequest
	}
	detail, err := bridge.service.store.GetReviewCase(ctx, caseID)
	if err != nil {
		return loadedAttention{}, sanitizeAttentionBridgeReadError(ctx, err)
	}
	if err = validateWorkingContextDetail(caseID, detail); err != nil {
		return loadedAttention{}, ErrUnavailable
	}
	base := AttentionView{
		CaseVersion: detail.Case.Version,
		Status:      AttentionStatusNone,
		Turns:       []AttentionTurnView{},
	}
	if detail.Case.Status != eventing.ReviewCaseSubmitted {
		return loadedAttention{view: base}, nil
	}
	if !validSubmittedAttentionCase(detail) {
		return loadedAttention{}, ErrUnavailable
	}
	trigger, err := bridge.reader.GetReviewAttentionTrigger(ctx, detail.Submission.ID)
	if errors.Is(err, eventing.ErrNotFound) {
		// Stores migrated from schema v4 deliberately have no synthetic
		// occurrence for a submission that was already terminal.
		return loadedAttention{view: base}, nil
	}
	if err != nil {
		return loadedAttention{}, sanitizeAttentionBridgeReadError(ctx, err)
	}
	if !validAttentionTriggerForCase(trigger, detail) {
		return loadedAttention{}, ErrUnavailable
	}
	switch trigger.Status {
	case eventing.ReviewAttentionPending:
		base.Status = AttentionStatusQueued
		return loadedAttention{view: base}, nil
	case eventing.ReviewAttentionClaimed:
		base.Status = AttentionStatusProcessing
		return loadedAttention{view: base}, nil
	case eventing.ReviewAttentionNoop:
		base.Status = AttentionStatusNotRequired
		return loadedAttention{view: base}, nil
	case eventing.ReviewAttentionDelivered:
	default:
		return loadedAttention{}, ErrUnavailable
	}

	key := eventing.ReviewDecisionKey{
		CaseID:         trigger.CaseID,
		CaseVersion:    trigger.CaseVersion,
		DecisionPoint:  trigger.DecisionPoint,
		PolicyRevision: trigger.PolicyRevision,
	}
	runID, err := attentionRunID(key)
	if err != nil || runID != trigger.RunID {
		return loadedAttention{}, ErrUnavailable
	}
	link, err := bridge.reader.GetReviewDecisionRun(ctx, key)
	if err != nil {
		return loadedAttention{}, sanitizeAttentionBridgeError(ctx, err)
	}
	if link.Key != key || link.RunID != runID {
		return loadedAttention{}, ErrUnavailable
	}
	input := sharedattention.ConversationInput{
		CaseVersion: detail.Case.Version,
		RunID:       runID,
		Token: func(
			task workflows.WorkflowHumanTask,
			waitingRevision uint64,
		) (string, error) {
			return attentionTaskResponseToken(trigger, task, waitingRevision), nil
		},
	}
	conversation, err := bridge.conversation.Project(ctx, input)
	if err != nil {
		return loadedAttention{}, sanitizeAttentionBridgeProjectionError(ctx, err)
	}
	base.Status = conversation.Status
	base.CanRespond = conversation.CanRespond
	base.Turns = conversation.Turns
	return loadedAttention{view: base, input: &input}, nil
}

func validSubmittedAttentionCase(detail eventing.ReviewCaseDetail) bool {
	submission := detail.Submission
	return submission != nil &&
		validWorkingContextPrefixedHexID(submission.ID, "prs_") &&
		submission.CaseID == detail.Case.ID &&
		submission.DraftVersion > 0 &&
		submission.DraftVersion < detail.Case.Version &&
		submission.Status == eventing.ReviewSubmissionSubmitted &&
		submission.SubmittedAt != nil &&
		!submission.CreatedAt.IsZero() && !submission.UpdatedAt.IsZero() &&
		!submission.UpdatedAt.Before(submission.CreatedAt) &&
		!submission.SubmittedAt.Before(submission.CreatedAt) &&
		!submission.SubmittedAt.After(submission.UpdatedAt)
}

func validAttentionTriggerForCase(
	trigger eventing.ReviewAttentionTrigger,
	detail eventing.ReviewCaseDetail,
) bool {
	if detail.Submission == nil || trigger.SubmissionID != detail.Submission.ID ||
		trigger.CaseID != detail.Case.ID || trigger.CaseVersion != detail.Case.Version ||
		trigger.DecisionPoint != eventing.ReviewAttentionDecisionSubmitted ||
		trigger.CreatedAt.IsZero() || trigger.UpdatedAt.IsZero() ||
		trigger.UpdatedAt.Before(trigger.CreatedAt) {
		return false
	}
	policy, pinned, validPin := decodeAttentionTriggerPin(trigger)
	if !validPin {
		return false
	}
	switch trigger.Status {
	case eventing.ReviewAttentionPending, eventing.ReviewAttentionClaimed:
		return trigger.RunID == "" && trigger.CompletedAt == nil
	case eventing.ReviewAttentionNoop:
		return trigger.RunID == "" && pinned &&
			attentionPolicyIsNoop(policy.resolution.Effective) && trigger.CompletedAt != nil
	case eventing.ReviewAttentionDelivered:
		return validWorkingContextPrefixedHexID(trigger.RunID, "wr_") && pinned &&
			!attentionPolicyIsNoop(policy.resolution.Effective) && trigger.CompletedAt != nil
	default:
		return false
	}
}

func decodeAttentionTriggerPin(
	trigger eventing.ReviewAttentionTrigger,
) (resolvedAttentionPolicy, bool, bool) {
	hasRevision := trigger.PolicyRevision != ""
	hasPolicy := len(trigger.PinnedPolicy) != 0
	if hasRevision != hasPolicy {
		return resolvedAttentionPolicy{}, false, false
	}
	if !hasRevision {
		return resolvedAttentionPolicy{}, false, true
	}
	policy, err := decodePreparedAttentionPolicy(trigger.PinnedPolicy)
	if err != nil || policy.decisionRevision != trigger.PolicyRevision {
		return resolvedAttentionPolicy{}, false, false
	}
	return policy, true, true
}

func attentionTaskResponseToken(
	trigger eventing.ReviewAttentionTrigger,
	task workflows.WorkflowHumanTask,
	waitingRevision uint64,
) string {
	digest := sha256.New()
	sharedattention.WriteConversationHashField(digest, []byte(attentionTaskFenceDomain))
	sharedattention.WriteConversationHashField(digest, []byte(trigger.CaseID))
	sharedattention.WriteConversationHashUint64(digest, uint64(trigger.CaseVersion))
	sharedattention.WriteConversationHashField(digest, []byte(trigger.SubmissionID))
	sharedattention.WriteConversationHashField(digest, []byte(trigger.DecisionPoint))
	sharedattention.WriteConversationHashField(digest, []byte(trigger.PolicyRevision))
	sharedattention.WriteConversationHashField(digest, []byte(trigger.RunID))
	sharedattention.WriteConversationHashField(digest, []byte(task.ID))
	sharedattention.WriteConversationHashUint64(digest, waitingRevision)
	sharedattention.WriteConversationHashField(digest, []byte(task.InputHash))
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}

func attentionResponseID(token, response string) string {
	return sharedattention.ConversationResponseID(
		attentionResponseIDDomain,
		token,
		response,
	)
}

func validAttentionResponseToken(value string) bool {
	return sharedattention.ValidConversationResponseToken(value)
}

func sanitizeAttentionBridgeReadError(ctx context.Context, err error) error {
	if ctx != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
	}
	if errors.Is(err, eventing.ErrNotFound) {
		return eventing.ErrNotFound
	}
	return sanitizeAttentionBridgeError(ctx, err)
}

func sanitizeAttentionBridgeProjectionError(ctx context.Context, err error) error {
	if ctx != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
	}
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	default:
		return ErrUnavailable
	}
}

func sanitizeAttentionBridgeError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
	}
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, ErrInvalidRequest),
		errors.Is(err, sharedattention.ErrConversationInvalid),
		errors.Is(err, workflows.ErrHumanTaskResponseInvalid):
		return ErrInvalidRequest
	case errors.Is(err, eventing.ErrReviewConflict),
		errors.Is(err, sharedattention.ErrConversationConflict),
		errors.Is(err, workflows.ErrHumanTaskStale),
		errors.Is(err, workflows.ErrHumanTaskConflict),
		errors.Is(err, workflows.ErrRunAdmissionConflict),
		errors.Is(err, workflows.ErrRunConcurrencyLimit):
		return eventing.ErrReviewConflict
	default:
		return ErrUnavailable
	}
}
