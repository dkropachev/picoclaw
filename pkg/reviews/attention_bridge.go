package reviews

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

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
	attentionGateJobID        = "gates"
)

var attentionTaskStepPattern = regexp.MustCompile(
	`^gate_[a-z][a-z0-9_-]{0,63}_attention$`,
)

// AttentionTurnView is the only human-task state deliberately projected into
// the review workbench. Private workflow, task, input, and session identities
// are intentionally unrepresentable.
type AttentionTurnView struct {
	Status        string `json:"status"`
	Title         string `json:"title"`
	Questions     any    `json:"questions"`
	Response      string `json:"response,omitempty"`
	ResponseToken string `json:"response_token,omitempty"`
}

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
	service  *Service
	reader   attentionBridgeReader
	runs     workflows.RunStore
	executor *workflows.Executor
}

type loadedAttention struct {
	view     AttentionView
	bindings []attentionTaskBinding
}

type attentionTaskBinding struct {
	task            workflows.WorkflowHumanTask
	responseToken   string
	waitingRevision uint64
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
	bridge := &AttentionBridge{
		service: config.Service,
		reader:  reader,
		runs:    config.RunStore,
	}
	if config.Executor != nil {
		executor := *config.Executor
		executor.Store = config.RunStore
		bridge.executor = &executor
	}
	return bridge, nil
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
	if bridge == nil || bridge.service == nil || bridge.reader == nil || bridge.runs == nil {
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
	var target *attentionTaskBinding
	for index := range loaded.bindings {
		if loaded.bindings[index].responseToken != request.ResponseToken {
			continue
		}
		if target != nil {
			return AttentionView{}, ErrUnavailable
		}
		target = &loaded.bindings[index]
	}
	if target == nil || target.task.Status == workflows.HumanTaskStatusCanceled {
		return AttentionView{}, eventing.ErrReviewConflict
	}
	responseID := attentionResponseID(request.ResponseToken, response)
	accepted := attentionTaskResponseMatches(target.task, responseID, response)
	if target.task.Status != workflows.HumanTaskStatusWaiting && !accepted {
		return AttentionView{}, eventing.ErrReviewConflict
	}
	if accepted && (bridge.executor == nil ||
		target.task.Status != workflows.HumanTaskStatusRecoveryRequired) {
		return loaded.view, nil
	}
	if bridge.executor == nil {
		return AttentionView{}, ErrUnavailable
	}
	_, resumeErr := bridge.executor.ResumeHumanTask(
		ctx,
		target.task.RunID,
		target.task.ID,
		workflows.HumanTaskResumeRequest{
			ExpectedRevision: target.waitingRevision,
			InputHash:        target.task.InputHash,
			ResponseID:       responseID,
			Response:         response,
		},
	)

	// ClaimHumanTask persists the answer before continuation. Reproject even on
	// an executor error so an accepted response is not reported as an unknown
	// outcome merely because a later private gate failed or the caller left.
	after, projectionErr := bridge.load(context.WithoutCancel(ctx), caseID)
	if projectionErr == nil && attentionResponseWasAccepted(
		after.bindings,
		request.ResponseToken,
		responseID,
		response,
	) {
		return after.view, nil
	}
	if resumeErr != nil {
		return AttentionView{}, sanitizeAttentionBridgeError(ctx, resumeErr)
	}
	if projectionErr != nil {
		return AttentionView{}, sanitizeAttentionBridgeError(ctx, projectionErr)
	}
	return AttentionView{}, ErrUnavailable
}

func (bridge *AttentionBridge) load(
	ctx context.Context,
	caseID string,
) (loadedAttention, error) {
	if bridge == nil || bridge.service == nil || bridge.reader == nil || bridge.runs == nil {
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
	run, tasks, err := bridge.loadRunAndTasks(ctx, runID)
	if err != nil {
		return loadedAttention{}, err
	}
	bindings, turns, err := projectAttentionTasks(trigger, run, tasks)
	if err != nil {
		return loadedAttention{}, ErrUnavailable
	}
	base.Turns = turns
	base.Status, base.CanRespond, err = attentionViewState(run, tasks, bridge.executor != nil)
	if err != nil {
		return loadedAttention{}, ErrUnavailable
	}
	if base.CanRespond {
		if len(base.Turns) == 0 || len(bindings) != len(base.Turns) {
			return loadedAttention{}, ErrUnavailable
		}
		current := len(base.Turns) - 1
		switch base.Turns[current].Status {
		case workflows.HumanTaskStatusWaiting,
			workflows.HumanTaskStatusRecoveryRequired:
			base.Turns[current].ResponseToken = bindings[current].responseToken
		default:
			return loadedAttention{}, ErrUnavailable
		}
	}
	return loadedAttention{view: base, bindings: bindings}, nil
}

// GetRun and ListHumanTasks are intentionally separate RunStore operations.
// Fence them with a second run read so a waiting-to-running-to-waiting
// continuation cannot be mistaken for corruption or leak a transient 503 to
// a competing response. A stable long-running continuation succeeds on its
// first attempt because its status and UpdatedAt remain unchanged.
func (bridge *AttentionBridge) loadRunAndTasks(
	ctx context.Context,
	runID string,
) (*workflows.Run, []workflows.WorkflowHumanTask, error) {
	const maximumSnapshotAttempts = 8
	tasksReader := &workflows.Executor{Store: bridge.runs}
	for attempt := 0; attempt < maximumSnapshotAttempts; attempt++ {
		before, err := bridge.runs.GetRun(ctx, runID)
		if err != nil {
			return nil, nil, sanitizeAttentionBridgeError(ctx, err)
		}
		if !validAttentionRun(before, runID) {
			return nil, nil, ErrUnavailable
		}
		tasks, err := tasksReader.ListHumanTasks(ctx, runID)
		if err != nil {
			return nil, nil, sanitizeAttentionBridgeError(ctx, err)
		}
		after, err := bridge.runs.GetRun(ctx, runID)
		if err != nil {
			return nil, nil, sanitizeAttentionBridgeError(ctx, err)
		}
		if !validAttentionRun(after, runID) {
			return nil, nil, ErrUnavailable
		}
		if sameAttentionRunSnapshot(before, after) {
			return after, tasks, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
	}
	return nil, nil, ErrUnavailable
}

func sameAttentionRunSnapshot(first, second *workflows.Run) bool {
	if first == nil || second == nil {
		return false
	}
	return first.ID == second.ID &&
		first.WorkflowRef == second.WorkflowRef &&
		first.Status == second.Status &&
		first.UpdatedAt.Equal(second.UpdatedAt) &&
		sameAttentionTime(first.CompletedAt, second.CompletedAt) &&
		sameAttentionTime(first.CancelRequestedAt, second.CancelRequestedAt)
}

func sameAttentionTime(first, second *time.Time) bool {
	if first == nil || second == nil {
		return first == nil && second == nil
	}
	return first.Equal(*second)
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

func projectAttentionTasks(
	trigger eventing.ReviewAttentionTrigger,
	run *workflows.Run,
	tasks []workflows.WorkflowHumanTask,
) ([]attentionTaskBinding, []AttentionTurnView, error) {
	if run == nil || len(tasks) > workflows.MaxWorkflowGateCount {
		return nil, nil, ErrUnavailable
	}
	bindings := make([]attentionTaskBinding, len(tasks))
	turns := make([]AttentionTurnView, len(tasks))
	seenIDs := make(map[string]struct{}, len(tasks))
	seenSteps := make(map[string]struct{}, len(tasks))
	nonAnswered := 0
	for index, task := range tasks {
		waitingRevision, err := validateAttentionTask(run, task)
		if err != nil {
			return nil, nil, err
		}
		if _, duplicate := seenIDs[task.ID]; duplicate {
			return nil, nil, ErrUnavailable
		}
		if _, duplicate := seenSteps[task.StepID]; duplicate {
			return nil, nil, ErrUnavailable
		}
		if index > 0 {
			previous := tasks[index-1]
			if task.CreatedAt.Before(previous.CreatedAt) ||
				(task.CreatedAt.Equal(previous.CreatedAt) && task.ID <= previous.ID) {
				return nil, nil, ErrUnavailable
			}
		}
		seenIDs[task.ID] = struct{}{}
		seenSteps[task.StepID] = struct{}{}
		if task.Status != workflows.HumanTaskStatusAnswered {
			nonAnswered++
			if index != len(tasks)-1 {
				return nil, nil, ErrUnavailable
			}
		}
		token := attentionTaskResponseToken(trigger, task, waitingRevision)
		bindings[index] = attentionTaskBinding{
			task:            task,
			responseToken:   token,
			waitingRevision: waitingRevision,
		}
		turn := AttentionTurnView{
			Status:    task.Status,
			Title:     task.Title,
			Questions: task.Questions,
		}
		switch task.Status {
		case workflows.HumanTaskStatusAnswered,
			workflows.HumanTaskStatusContinuing,
			workflows.HumanTaskStatusRecoveryRequired:
			response, ok := task.Response.(string)
			if !ok || !validAttentionResponseText(response) {
				return nil, nil, ErrUnavailable
			}
			if task.ResponseID != attentionResponseID(token, response) {
				return nil, nil, ErrUnavailable
			}
			turn.Response = response
		}
		turns[index] = turn
	}
	if nonAnswered > 1 {
		return nil, nil, ErrUnavailable
	}
	return bindings, turns, nil
}

func validateAttentionTask(
	run *workflows.Run,
	task workflows.WorkflowHumanTask,
) (uint64, error) {
	if run == nil || task.RunID != run.ID || task.WorkflowRef != reviewAttentionWorkflowRef ||
		task.JobID != attentionGateJobID || !attentionTaskStepPattern.MatchString(task.StepID) ||
		!validWorkingContextPrefixedHexID(task.ID, "ht_") ||
		task.ID != attentionHumanTaskID(run.ID, task.JobID, task.StepID) ||
		task.InputHash != strings.TrimSpace(task.InputHash) || task.InputHash == "" ||
		!utf8.ValidString(task.InputHash) ||
		len(task.InputHash) > workflows.MaxHumanTaskInputHashBytes ||
		task.Title != strings.TrimSpace(task.Title) || task.Title == "" ||
		!utf8.ValidString(task.Title) || strings.IndexByte(task.Title, 0) >= 0 ||
		len(task.Title) > workflows.MaxHumanTaskTitleBytes || task.Questions == nil ||
		!exactAttentionResponseSchema(task.ResponseSchema) ||
		task.CreatedAt.IsZero() || task.UpdatedAt.IsZero() ||
		task.CreatedAt.Before(run.CreatedAt) || task.UpdatedAt.Before(task.CreatedAt) ||
		task.UpdatedAt.After(run.UpdatedAt) {
		return 0, ErrUnavailable
	}
	if err := validateAttentionQuestions(task.Questions); err != nil {
		return 0, ErrUnavailable
	}
	if !attentionTaskInputHashMatches(task) {
		return 0, ErrUnavailable
	}
	step, exists := run.Steps[task.JobID+"/"+task.StepID]
	if !exists {
		return 0, ErrUnavailable
	}
	switch task.Status {
	case workflows.HumanTaskStatusWaiting:
		if task.Revision == 0 || task.ResponseID != "" || task.Response != nil ||
			task.AnsweredAt != nil || task.CanceledAt != nil || task.RetryAt != nil ||
			step.Status != workflows.RunStatusWaiting {
			return 0, ErrUnavailable
		}
		return task.Revision, nil
	case workflows.HumanTaskStatusAnswered,
		workflows.HumanTaskStatusContinuing,
		workflows.HumanTaskStatusRecoveryRequired:
		if task.Revision < 2 || task.ResponseID != strings.TrimSpace(task.ResponseID) ||
			task.ResponseID == "" || !utf8.ValidString(task.ResponseID) ||
			len(task.ResponseID) > workflows.MaxHumanTaskResponseIDBytes ||
			task.AnsweredAt == nil || task.AnsweredAt.Before(task.CreatedAt) ||
			task.AnsweredAt.After(task.UpdatedAt) || task.CanceledAt != nil ||
			step.Status != workflows.RunStatusSucceeded {
			return 0, ErrUnavailable
		}
		if task.Status == workflows.HumanTaskStatusAnswered && task.RetryAt != nil ||
			task.Status != workflows.HumanTaskStatusAnswered && task.RetryAt == nil {
			return 0, ErrUnavailable
		}
		return task.Revision - 1, nil
	case workflows.HumanTaskStatusCanceled:
		if task.Revision < 2 || task.ResponseID != "" || task.Response != nil ||
			task.AnsweredAt != nil || task.CanceledAt == nil ||
			task.CanceledAt.Before(task.CreatedAt) || task.CanceledAt.After(task.UpdatedAt) ||
			task.RetryAt != nil || step.Status != workflows.RunStatusWaiting {
			return 0, ErrUnavailable
		}
		return task.Revision - 1, nil
	default:
		return 0, ErrUnavailable
	}
}

func attentionTaskInputHashMatches(task workflows.WorkflowHumanTask) bool {
	payload, err := json.Marshal(map[string]any{
		"title":           task.Title,
		"questions":       task.Questions,
		"response_schema": task.ResponseSchema,
	})
	if err != nil || len(payload) > workflows.MaxHumanTaskPayloadBytes {
		return false
	}
	digest := sha256.Sum256(payload)
	return task.InputHash == "sha256:"+hex.EncodeToString(digest[:])
}

func attentionHumanTaskID(runID, jobID, stepID string) string {
	digest := sha256.Sum256([]byte(runID + "\x00" + jobID + "\x00" + stepID))
	return "ht_" + hex.EncodeToString(digest[:16])
}

func validateAttentionQuestions(value any) error {
	nodes := 0
	var visit func(any, int) error
	visit = func(candidate any, depth int) error {
		if depth > workflows.MaxWorkflowGateJSONDepth {
			return ErrUnavailable
		}
		nodes++
		if nodes > workflows.MaxWorkflowGateJSONNodes {
			return ErrUnavailable
		}
		switch typed := candidate.(type) {
		case nil, bool:
			return nil
		case string:
			if !utf8.ValidString(typed) {
				return ErrUnavailable
			}
			return nil
		case json.Number:
			if !utf8.ValidString(typed.String()) {
				return ErrUnavailable
			}
			if _, err := json.Marshal(typed); err != nil {
				return ErrUnavailable
			}
			return nil
		case float64:
			if _, err := json.Marshal(typed); err != nil {
				return ErrUnavailable
			}
			return nil
		case []any:
			for _, item := range typed {
				if err := visit(item, depth+1); err != nil {
					return err
				}
			}
			return nil
		case map[string]any:
			for key, item := range typed {
				if !utf8.ValidString(key) {
					return ErrUnavailable
				}
				if err := visit(item, depth+1); err != nil {
					return err
				}
			}
			return nil
		default:
			return ErrUnavailable
		}
	}
	return visit(value, 0)
}

func exactAttentionResponseSchema(schema map[string]any) bool {
	if len(schema) != 1 {
		return false
	}
	typeValue, ok := schema["type"].(string)
	return ok && typeValue == "string"
}

func attentionViewState(
	run *workflows.Run,
	tasks []workflows.WorkflowHumanTask,
	runtimeEnabled bool,
) (string, bool, error) {
	if run == nil {
		return "", false, ErrUnavailable
	}
	var current *workflows.WorkflowHumanTask
	if len(tasks) > 0 {
		candidate := tasks[len(tasks)-1]
		current = &candidate
	}
	switch run.Status {
	case workflows.RunStatusWaiting:
		if current == nil || current.Status != workflows.HumanTaskStatusWaiting {
			return "", false, ErrUnavailable
		}
		return AttentionStatusWaiting, runtimeEnabled, nil
	case workflows.RunStatusRunning:
		if current == nil {
			return AttentionStatusProcessing, false, nil
		}
		switch current.Status {
		case workflows.HumanTaskStatusContinuing:
			return AttentionStatusContinuing, false, nil
		case workflows.HumanTaskStatusRecoveryRequired:
			return AttentionStatusRecoveryRequired, runtimeEnabled, nil
		default:
			return "", false, ErrUnavailable
		}
	case workflows.RunStatusSucceeded, workflows.RunStatusSkipped:
		if current != nil && current.Status != workflows.HumanTaskStatusAnswered {
			return "", false, ErrUnavailable
		}
		return AttentionStatusCompleted, false, nil
	case workflows.RunStatusFailed, workflows.RunStatusCanceled:
		if current != nil {
			switch current.Status {
			case workflows.HumanTaskStatusAnswered, workflows.HumanTaskStatusCanceled:
			default:
				return "", false, ErrUnavailable
			}
		}
		return AttentionStatusFailed, false, nil
	default:
		return "", false, ErrUnavailable
	}
}

func attentionTaskResponseToken(
	trigger eventing.ReviewAttentionTrigger,
	task workflows.WorkflowHumanTask,
	waitingRevision uint64,
) string {
	digest := sha256.New()
	writeAttentionHashField(digest, []byte(attentionTaskFenceDomain))
	writeAttentionHashField(digest, []byte(trigger.CaseID))
	writeAttentionHashUint64(digest, uint64(trigger.CaseVersion))
	writeAttentionHashField(digest, []byte(trigger.SubmissionID))
	writeAttentionHashField(digest, []byte(trigger.DecisionPoint))
	writeAttentionHashField(digest, []byte(trigger.PolicyRevision))
	writeAttentionHashField(digest, []byte(trigger.RunID))
	writeAttentionHashField(digest, []byte(task.ID))
	writeAttentionHashUint64(digest, waitingRevision)
	writeAttentionHashField(digest, []byte(task.InputHash))
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}

func attentionResponseID(token, response string) string {
	digest := sha256.New()
	writeAttentionHashField(digest, []byte(attentionResponseIDDomain))
	writeAttentionHashField(digest, []byte(token))
	writeAttentionHashField(digest, []byte(response))
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}

func writeAttentionHashField(digest hash.Hash, value []byte) {
	writeAttentionHashUint64(digest, uint64(len(value)))
	_, _ = digest.Write(value)
}

func writeAttentionHashUint64(digest hash.Hash, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = digest.Write(encoded[:])
}

func validAttentionResponseToken(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func validAttentionResponseText(value string) bool {
	return value == strings.TrimSpace(value) && value != "" && utf8.ValidString(value) &&
		len(value) <= maxReviewChatBytes && strings.IndexByte(value, 0) < 0
}

func attentionResponseWasAccepted(
	bindings []attentionTaskBinding,
	token, responseID, response string,
) bool {
	for _, binding := range bindings {
		if binding.responseToken != token || binding.task.ResponseID != responseID {
			continue
		}
		return attentionTaskResponseMatches(binding.task, responseID, response)
	}
	return false
}

func attentionTaskResponseMatches(
	task workflows.WorkflowHumanTask,
	responseID, response string,
) bool {
	if task.ResponseID != responseID {
		return false
	}
	stored, ok := task.Response.(string)
	return ok && stored == response
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
		errors.Is(err, workflows.ErrHumanTaskResponseInvalid):
		return ErrInvalidRequest
	case errors.Is(err, eventing.ErrReviewConflict),
		errors.Is(err, workflows.ErrHumanTaskStale),
		errors.Is(err, workflows.ErrHumanTaskConflict),
		errors.Is(err, workflows.ErrRunAdmissionConflict),
		errors.Is(err, workflows.ErrRunConcurrencyLimit):
		return eventing.ErrReviewConflict
	default:
		return ErrUnavailable
	}
}
