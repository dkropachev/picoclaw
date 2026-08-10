package prdevelopment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"strings"
	"unicode/utf8"

	sharedattention "github.com/sipeed/picoclaw/pkg/attention"
	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	AttentionStatusNone             = "none"
	AttentionStatusQueued           = "queued"
	AttentionStatusChecking         = "checking"
	AttentionStatusWaiting          = "waiting"
	AttentionStatusContinuing       = "continuing"
	AttentionStatusRecoveryRequired = "recovery_required"
	AttentionStatusCompleted        = "completed"
	AttentionStatusNotRequired      = "not_required"
	AttentionStatusFailed           = "failed"

	prDevelopmentAttentionTaskFenceDomain  = "picoclaw.pr-development-attention.task-fence.v1"
	prDevelopmentAttentionResponseIDDomain = "picoclaw.pr-development-attention.response-id.v1"
)

// AttentionTurnView is the bounded, case-owned projection of one private gate
// task. The alias deliberately keeps workflow and task identities absent.
type AttentionTurnView = sharedattention.ConversationTurnView

// AttentionView is the complete browser projection for the current automatic
// PR-development attention occurrence.
type AttentionView struct {
	CaseVersion int64               `json:"case_version"`
	Status      string              `json:"status"`
	CanRespond  bool                `json:"can_respond"`
	Turns       []AttentionTurnView `json:"turns"`
}

// AttentionResponseRequest answers one exact projected turn without accepting
// private workflow, run, task, policy, or subject identity from the caller.
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
	eventing.PRDevelopmentAttentionTriggerCaseReader
	GetPRDevelopmentAttentionDecisionRun(
		ctx context.Context,
		key eventing.PRDevelopmentAttentionDecisionKey,
	) (eventing.PRDevelopmentAttentionDecisionRunLink, error)
}

// AttentionBridge projects and resumes only a private run already linked to
// the authoritative current local-review occurrence. It has no Git, provider,
// repair, publication, policy-source, or workflow-admission authority.
type AttentionBridge struct {
	service      *Service
	reader       attentionBridgeReader
	conversation *sharedattention.ConversationEngine
}

type resolvedAttentionConversation struct {
	base  AttentionView
	input *sharedattention.ConversationInput
}

func NewAttentionBridge(config AttentionBridgeConfig) (*AttentionBridge, error) {
	if config.Service == nil || config.Service.store == nil ||
		isNilServiceValue(config.Service.store) || config.Service.caseLocks == nil {
		return nil, errors.New("pull request development attention bridge service is required")
	}
	reader, ok := config.Service.store.(attentionBridgeReader)
	if !ok || isNilServiceValue(reader) {
		return nil, errors.New("pull request development store does not support attention projection")
	}
	conversation, err := sharedattention.NewConversationEngine(
		sharedattention.ConversationEngineConfig{
			RunStore:         config.RunStore,
			Executor:         config.Executor,
			MaxResponseBytes: maximumHumanChatBytes,
			ResponseIDDomain: prDevelopmentAttentionResponseIDDomain,
		},
	)
	if err != nil {
		return nil, err
	}
	return &AttentionBridge{
		service:      config.Service,
		reader:       reader,
		conversation: conversation,
	}, nil
}

func (bridge *AttentionBridge) Project(
	ctx context.Context,
	caseID string,
) (AttentionView, error) {
	resolved, err := bridge.resolve(ctx, caseID)
	if err != nil {
		return AttentionView{}, err
	}
	return bridge.projectResolved(ctx, resolved)
}

func (bridge *AttentionBridge) Respond(
	ctx context.Context,
	request AttentionResponseRequest,
) (AttentionView, error) {
	if bridge == nil || bridge.service == nil || bridge.service.caseLocks == nil ||
		bridge.reader == nil || bridge.conversation == nil {
		return AttentionView{}, ErrUnavailable
	}
	ctx = developmentAttentionContext(ctx)
	caseID := strings.TrimSpace(request.CaseID)
	response := strings.TrimSpace(request.Response)
	if caseID != request.CaseID || !validCaseID(caseID) ||
		request.ExpectedCaseVersion < 0 ||
		request.ExpectedCaseVersion > int64(MaximumConversationVersion) ||
		!sharedattention.ValidConversationResponseToken(request.ResponseToken) ||
		response == "" || response != request.Response || !utf8.ValidString(response) ||
		strings.IndexByte(response, 0) >= 0 || len(response) > maximumHumanChatBytes {
		return AttentionView{}, ErrInvalidRequest
	}
	release, err := bridge.service.caseLocks.acquire(ctx, caseID)
	if err != nil {
		return AttentionView{}, err
	}
	defer release()

	resolved, err := bridge.resolve(ctx, caseID)
	if err != nil {
		return AttentionView{}, err
	}
	if resolved.base.CaseVersion != request.ExpectedCaseVersion {
		return AttentionView{}, eventing.ErrPRDevelopmentConversationConflict
	}
	if resolved.input == nil {
		return AttentionView{}, eventing.ErrPRDevelopmentConversationConflict
	}
	_, err = bridge.conversation.Respond(
		ctx,
		sharedattention.ConversationResponse{
			ConversationInput:   *resolved.input,
			ExpectedCaseVersion: request.ExpectedCaseVersion,
			ResponseToken:       request.ResponseToken,
			Response:            response,
		},
	)
	if err != nil {
		return AttentionView{}, sanitizeDevelopmentAttentionBridgeError(ctx, err)
	}
	// The task response may commit before private continuation finishes. Re-read
	// the atomic case/trigger ownership after continuation so the returned view
	// cannot outlive a concurrently superseded local-review occurrence.
	after, err := bridge.resolve(context.WithoutCancel(ctx), caseID)
	if err != nil {
		return AttentionView{}, sanitizeDevelopmentAttentionBridgeError(ctx, err)
	}
	return bridge.projectResolved(context.WithoutCancel(ctx), after)
}

func (bridge *AttentionBridge) projectResolved(
	ctx context.Context,
	resolved resolvedAttentionConversation,
) (AttentionView, error) {
	if resolved.input == nil {
		return resolved.base, nil
	}
	conversation, err := bridge.conversation.Project(ctx, *resolved.input)
	if err != nil {
		return AttentionView{}, sanitizeDevelopmentAttentionBridgeError(ctx, err)
	}
	return projectDevelopmentAttentionConversation(resolved.base, conversation)
}

func (bridge *AttentionBridge) resolve(
	ctx context.Context,
	caseID string,
) (resolvedAttentionConversation, error) {
	if bridge == nil || bridge.service == nil || bridge.reader == nil ||
		bridge.conversation == nil {
		return resolvedAttentionConversation{}, ErrUnavailable
	}
	ctx = developmentAttentionContext(ctx)
	normalizedCaseID := strings.TrimSpace(caseID)
	if normalizedCaseID != caseID || !validCaseID(normalizedCaseID) {
		return resolvedAttentionConversation{}, ErrInvalidRequest
	}
	caseID = normalizedCaseID
	snapshot, err := bridge.reader.GetCurrentPRDevelopmentAttentionTriggerForCase(ctx, caseID)
	if err != nil {
		return resolvedAttentionConversation{}, sanitizeDevelopmentAttentionBridgeReadError(ctx, err)
	}
	if !validDevelopmentAttentionCaseSnapshot(snapshot, caseID) {
		return resolvedAttentionConversation{}, ErrUnavailable
	}
	base := AttentionView{
		CaseVersion: snapshot.ConversationVersion,
		Status:      AttentionStatusNone,
		Turns:       []AttentionTurnView{},
	}
	if snapshot.Trigger == nil {
		return resolvedAttentionConversation{base: base}, nil
	}
	trigger := *snapshot.Trigger
	policy, pinned, valid := validDevelopmentAttentionBridgeTrigger(trigger, snapshot)
	if !valid {
		return resolvedAttentionConversation{}, ErrUnavailable
	}
	if !snapshot.TriggerCurrent {
		if snapshot.AttentionRequired {
			return resolvedAttentionConversation{}, ErrUnavailable
		}
		return resolvedAttentionConversation{base: base}, nil
	}
	switch trigger.Status {
	case eventing.PRDevelopmentAttentionTriggerPending:
		base.Status = AttentionStatusQueued
		return resolvedAttentionConversation{base: base}, nil
	case eventing.PRDevelopmentAttentionTriggerClaimed:
		base.Status = AttentionStatusChecking
		return resolvedAttentionConversation{base: base}, nil
	case eventing.PRDevelopmentAttentionTriggerNoop:
		base.Status = AttentionStatusNotRequired
		return resolvedAttentionConversation{base: base}, nil
	case eventing.PRDevelopmentAttentionTriggerRecoveryRequired:
		base.Status = AttentionStatusRecoveryRequired
		return resolvedAttentionConversation{base: base}, nil
	case eventing.PRDevelopmentAttentionTriggerFailed:
		base.Status = AttentionStatusFailed
		return resolvedAttentionConversation{base: base}, nil
	case eventing.PRDevelopmentAttentionTriggerDelivered:
		if !pinned || policy.IsNoop() {
			return resolvedAttentionConversation{}, ErrUnavailable
		}
	case eventing.PRDevelopmentAttentionTriggerSuperseded:
		return resolvedAttentionConversation{}, ErrUnavailable
	default:
		return resolvedAttentionConversation{}, ErrUnavailable
	}
	key := attentionDecisionKeyForTrigger(trigger)
	canonicalKey, err := canonicalPRDevelopmentAttentionDecisionKey(key)
	if err != nil {
		return resolvedAttentionConversation{}, ErrUnavailable
	}
	runID, err := sharedattention.RunIDForDecisionKey(canonicalKey)
	if err != nil || runID != trigger.RunID {
		return resolvedAttentionConversation{}, ErrUnavailable
	}
	link, err := bridge.reader.GetPRDevelopmentAttentionDecisionRun(ctx, key)
	if err != nil {
		return resolvedAttentionConversation{}, sanitizeDevelopmentAttentionBridgeReadError(ctx, err)
	}
	if link.Key != key || link.RunID != runID || link.CreatedAt.IsZero() {
		return resolvedAttentionConversation{}, ErrUnavailable
	}
	input := sharedattention.ConversationInput{
		CaseVersion: snapshot.ConversationVersion,
		RunID:       runID,
		Token:       developmentAttentionResponseTokenFactory(trigger),
	}
	return resolvedAttentionConversation{base: base, input: &input}, nil
}

func validDevelopmentAttentionCaseSnapshot(
	snapshot eventing.PRDevelopmentAttentionTriggerCaseSnapshot,
	caseID string,
) bool {
	if snapshot.CaseID != caseID || snapshot.ConversationVersion < 0 ||
		snapshot.ConversationVersion > int64(MaximumConversationVersion) ||
		snapshot.TriggerCurrent && snapshot.Trigger == nil {
		return false
	}
	if snapshot.CurrentReviewEntryID == "" {
		return snapshot.CurrentReviewEntryHash == "" &&
			snapshot.CurrentReviewOutcome == "" && !snapshot.AttentionRequired &&
			!snapshot.TriggerCurrent
	}
	if !validDevelopmentID(snapshot.CurrentReviewEntryID, "pdle_") ||
		!validControllerSHA256(snapshot.CurrentReviewEntryHash) {
		return false
	}
	switch snapshot.CurrentReviewOutcome {
	case eventing.PRDevelopmentLedgerReviewPassed,
		eventing.PRDevelopmentLedgerReviewChangesRequired:
		return !snapshot.AttentionRequired && !snapshot.TriggerCurrent
	case eventing.PRDevelopmentLedgerReviewAttentionRequired:
		return snapshot.AttentionRequired
	default:
		return false
	}
}

func validDevelopmentAttentionBridgeTrigger(
	trigger eventing.PRDevelopmentAttentionTrigger,
	snapshot eventing.PRDevelopmentAttentionTriggerCaseSnapshot,
) (sharedattention.PreparedPolicy, bool, bool) {
	if validateAttentionTriggerIdentity(trigger) != nil ||
		trigger.CaseID != snapshot.CaseID ||
		trigger.ConversationVersion > snapshot.ConversationVersion ||
		trigger.DecisionPoint != eventing.PRDevelopmentAttentionDecisionReviewRequired ||
		trigger.Attempts < 0 || trigger.AvailableAt.IsZero() ||
		trigger.CreatedAt.IsZero() || trigger.UpdatedAt.IsZero() ||
		trigger.UpdatedAt.Before(trigger.CreatedAt) ||
		(trigger.CompletedAt != nil &&
			(trigger.CompletedAt.Before(trigger.CreatedAt) ||
				trigger.CompletedAt.After(trigger.UpdatedAt))) {
		return sharedattention.PreparedPolicy{}, false, false
	}
	_, policy, pinned, err := pinnedAttentionTriggerPolicy(trigger)
	if err != nil {
		return sharedattention.PreparedPolicy{}, false, false
	}
	if snapshot.TriggerCurrent &&
		(trigger.ReviewEntryID != snapshot.CurrentReviewEntryID ||
			trigger.ReviewEntryHash != snapshot.CurrentReviewEntryHash ||
			!snapshot.AttentionRequired) {
		return sharedattention.PreparedPolicy{}, false, false
	}
	terminal := trigger.CompletedAt != nil
	switch trigger.Status {
	case eventing.PRDevelopmentAttentionTriggerPending:
		return policy, pinned, !terminal && trigger.LeaseToken == "" &&
			trigger.LeaseUntil == nil && trigger.RunID == ""
	case eventing.PRDevelopmentAttentionTriggerClaimed:
		return policy, pinned, !terminal && trigger.Attempts > 0 &&
			strings.TrimSpace(trigger.LeaseToken) != "" && trigger.LeaseUntil != nil &&
			trigger.RunID == ""
	case eventing.PRDevelopmentAttentionTriggerNoop:
		return policy, pinned, terminal && trigger.LeaseToken == "" &&
			trigger.LeaseUntil == nil && trigger.RunID == "" &&
			trigger.SubjectRevision == "" && pinned && policy.IsNoop()
	case eventing.PRDevelopmentAttentionTriggerDelivered:
		return policy, pinned, terminal && trigger.LeaseToken == "" &&
			trigger.LeaseUntil == nil && validDevelopmentID(trigger.RunID, "wr_") &&
			validAttentionRevision(trigger.SubjectRevision) && pinned && !policy.IsNoop()
	case eventing.PRDevelopmentAttentionTriggerRecoveryRequired:
		return policy, pinned, terminal && trigger.LeaseToken == "" &&
			trigger.LeaseUntil == nil && trigger.RunID == "" &&
			validAttentionRevision(trigger.SubjectRevision) && pinned && !policy.IsNoop()
	case eventing.PRDevelopmentAttentionTriggerSuperseded,
		eventing.PRDevelopmentAttentionTriggerFailed:
		return policy, pinned, terminal && trigger.LeaseToken == "" &&
			trigger.LeaseUntil == nil && trigger.RunID == ""
	default:
		return sharedattention.PreparedPolicy{}, false, false
	}
}

func developmentAttentionResponseTokenFactory(
	trigger eventing.PRDevelopmentAttentionTrigger,
) sharedattention.ConversationTokenFactory {
	return func(task workflows.WorkflowHumanTask, waitingRevision uint64) (string, error) {
		if task.RunID != trigger.RunID || task.ID == "" ||
			task.InputHash == "" || waitingRevision == 0 {
			return "", sharedattention.ErrConversationUnavailable
		}
		digest := sha256.New()
		sharedattention.WriteConversationHashField(
			digest,
			[]byte(prDevelopmentAttentionTaskFenceDomain),
		)
		writeDevelopmentAttentionTokenField(digest, trigger.CaseID)
		writeDevelopmentAttentionTokenField(digest, trigger.ReviewEntryID)
		writeDevelopmentAttentionTokenField(digest, trigger.ReviewEntryHash)
		sharedattention.WriteConversationHashUint64(
			digest,
			uint64(trigger.ConversationVersion),
		)
		writeDevelopmentAttentionTokenField(digest, trigger.TranscriptDigest)
		writeDevelopmentAttentionTokenField(digest, trigger.DecisionPoint)
		writeDevelopmentAttentionTokenField(digest, trigger.PolicyRevision)
		writeDevelopmentAttentionTokenField(digest, trigger.SubjectRevision)
		writeDevelopmentAttentionTokenField(digest, trigger.RunID)
		writeDevelopmentAttentionTokenField(digest, task.ID)
		sharedattention.WriteConversationHashUint64(digest, waitingRevision)
		writeDevelopmentAttentionTokenField(digest, task.InputHash)
		return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
	}
}

func writeDevelopmentAttentionTokenField(digest hash.Hash, value string) {
	sharedattention.WriteConversationHashField(digest, []byte(value))
}

func projectDevelopmentAttentionConversation(
	base AttentionView,
	conversation sharedattention.ConversationView,
) (AttentionView, error) {
	switch conversation.Status {
	case sharedattention.ConversationStatusProcessing:
		base.Status = AttentionStatusChecking
	case sharedattention.ConversationStatusWaiting:
		base.Status = AttentionStatusWaiting
	case sharedattention.ConversationStatusContinuing:
		base.Status = AttentionStatusContinuing
	case sharedattention.ConversationStatusRecoveryRequired:
		base.Status = AttentionStatusRecoveryRequired
	case sharedattention.ConversationStatusCompleted:
		base.Status = AttentionStatusCompleted
	case sharedattention.ConversationStatusFailed:
		base.Status = AttentionStatusFailed
	default:
		return AttentionView{}, ErrUnavailable
	}
	base.CanRespond = conversation.CanRespond
	base.Turns = append([]AttentionTurnView(nil), conversation.Turns...)
	return base, nil
}

func sanitizeDevelopmentAttentionBridgeReadError(ctx context.Context, err error) error {
	if ctxErr := developmentAttentionContext(ctx).Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, eventing.ErrNotFound) {
		return eventing.ErrNotFound
	}
	return sanitizeDevelopmentAttentionBridgeError(ctx, err)
}

func sanitizeDevelopmentAttentionBridgeError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctxErr := developmentAttentionContext(ctx).Err(); ctxErr != nil {
		return ctxErr
	}
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, ErrInvalidRequest),
		errors.Is(err, sharedattention.ErrConversationInvalid):
		return ErrInvalidRequest
	case errors.Is(err, sharedattention.ErrConversationConflict),
		errors.Is(err, workflows.ErrHumanTaskStale),
		errors.Is(err, workflows.ErrHumanTaskConflict),
		errors.Is(err, workflows.ErrRunAdmissionConflict),
		errors.Is(err, workflows.ErrRunConcurrencyLimit),
		errors.Is(err, eventing.ErrPRDevelopmentAttentionConflict):
		return eventing.ErrPRDevelopmentConversationConflict
	default:
		return ErrUnavailable
	}
}

func developmentAttentionContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
