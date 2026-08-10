package attention

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

	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	ConversationStatusProcessing       = "processing"
	ConversationStatusWaiting          = "waiting"
	ConversationStatusContinuing       = "continuing"
	ConversationStatusRecoveryRequired = "recovery_required"
	ConversationStatusCompleted        = "completed"
	ConversationStatusFailed           = "failed"

	conversationGateJobID = "gates"
)

var (
	ErrConversationInvalid     = errors.New("invalid attention conversation request")
	ErrConversationConflict    = errors.New("attention conversation conflict")
	ErrConversationUnavailable = errors.New("attention conversation unavailable")

	conversationTaskStepPattern = regexp.MustCompile(
		`^gate_[a-z][a-z0-9_-]{0,63}_attention$`,
	)
)

// ConversationTurnView is the only human-task state projected across a
// product bridge. Private workflow, task, input, and session identities remain
// unrepresentable.
type ConversationTurnView struct {
	Status        string `json:"status"`
	Title         string `json:"title"`
	Questions     any    `json:"questions"`
	Response      string `json:"response,omitempty"`
	ResponseToken string `json:"response_token,omitempty"`
}

type ConversationView struct {
	Status     string                 `json:"status"`
	CanRespond bool                   `json:"can_respond"`
	Turns      []ConversationTurnView `json:"turns"`
}

// ConversationTokenFactory binds a product-owned durable decision identity to
// one exact task generation without exposing that identity in the projection.
type ConversationTokenFactory func(
	task workflows.WorkflowHumanTask,
	waitingRevision uint64,
) (string, error)

type ConversationInput struct {
	CaseVersion int64
	RunID       string
	Token       ConversationTokenFactory
}

type ConversationResponse struct {
	ConversationInput
	ExpectedCaseVersion int64
	ResponseToken       string
	Response            string
}

type ConversationEngineConfig struct {
	RunStore         workflows.RunStore
	Executor         *workflows.Executor
	MaxResponseBytes int
	ResponseIDDomain string
}

type ConversationEngine struct {
	runs             workflows.RunStore
	executor         *workflows.Executor
	maxResponseBytes int
	responseIDDomain string
}

type conversationBinding struct {
	task            workflows.WorkflowHumanTask
	responseToken   string
	waitingRevision uint64
}

type loadedConversation struct {
	view     ConversationView
	bindings []conversationBinding
}

func NewConversationEngine(config ConversationEngineConfig) (*ConversationEngine, error) {
	if config.RunStore == nil || nilInterface(config.RunStore) {
		return nil, errors.New("attention conversation run store is required")
	}
	if config.MaxResponseBytes <= 0 ||
		config.MaxResponseBytes > workflows.MaxHumanTaskPayloadBytes {
		return nil, errors.New("attention conversation response limit is invalid")
	}
	if !validConversationDomain(config.ResponseIDDomain) {
		return nil, errors.New("attention conversation response domain is invalid")
	}
	engine := &ConversationEngine{
		runs:             config.RunStore,
		maxResponseBytes: config.MaxResponseBytes,
		responseIDDomain: config.ResponseIDDomain,
	}
	if config.Executor != nil {
		executor := *config.Executor
		executor.Store = config.RunStore
		engine.executor = &executor
	}
	return engine, nil
}

func (engine *ConversationEngine) Project(
	ctx context.Context,
	input ConversationInput,
) (ConversationView, error) {
	loaded, err := engine.load(ctx, input)
	if err != nil {
		return ConversationView{}, err
	}
	return loaded.view, nil
}

func (engine *ConversationEngine) Respond(
	ctx context.Context,
	request ConversationResponse,
) (ConversationView, error) {
	if !engine.available() || request.ExpectedCaseVersion < 0 ||
		!ValidConversationResponseToken(request.ResponseToken) {
		return ConversationView{}, ErrConversationInvalid
	}
	if request.ExpectedCaseVersion != request.CaseVersion {
		return ConversationView{}, ErrConversationConflict
	}
	response, err := normalizeConversationResponse(
		request.Response,
		engine.maxResponseBytes,
	)
	if err != nil {
		return ConversationView{}, err
	}
	loaded, err := engine.load(ctx, request.ConversationInput)
	if err != nil {
		return ConversationView{}, err
	}
	var target *conversationBinding
	for index := range loaded.bindings {
		if loaded.bindings[index].responseToken != request.ResponseToken {
			continue
		}
		if target != nil {
			return ConversationView{}, ErrConversationUnavailable
		}
		target = &loaded.bindings[index]
	}
	if target == nil || target.task.Status == workflows.HumanTaskStatusCanceled {
		return ConversationView{}, ErrConversationConflict
	}
	responseID := ConversationResponseID(
		engine.responseIDDomain,
		request.ResponseToken,
		response,
	)
	accepted := conversationTaskResponseMatches(target.task, responseID, response)
	if target.task.Status != workflows.HumanTaskStatusWaiting && !accepted {
		return ConversationView{}, ErrConversationConflict
	}
	if accepted && (engine.executor == nil ||
		target.task.Status != workflows.HumanTaskStatusRecoveryRequired) {
		return loaded.view, nil
	}
	if engine.executor == nil {
		return ConversationView{}, ErrConversationUnavailable
	}
	_, resumeErr := engine.executor.ResumeHumanTask(
		conversationContext(ctx),
		target.task.RunID,
		target.task.ID,
		workflows.HumanTaskResumeRequest{
			ExpectedRevision: target.waitingRevision,
			InputHash:        target.task.InputHash,
			ResponseID:       responseID,
			Response:         response,
		},
	)
	after, projectionErr := engine.load(
		context.WithoutCancel(conversationContext(ctx)),
		request.ConversationInput,
	)
	if projectionErr == nil && conversationResponseWasAccepted(
		after.bindings,
		request.ResponseToken,
		responseID,
		response,
	) {
		return after.view, nil
	}
	if resumeErr != nil {
		return ConversationView{}, sanitizeConversationError(ctx, resumeErr)
	}
	if projectionErr != nil {
		return ConversationView{}, sanitizeConversationError(ctx, projectionErr)
	}
	return ConversationView{}, ErrConversationUnavailable
}

func (engine *ConversationEngine) load(
	ctx context.Context,
	input ConversationInput,
) (loadedConversation, error) {
	if !engine.available() || input.CaseVersion < 0 ||
		!validConversationRunID(input.RunID) || input.Token == nil {
		return loadedConversation{}, ErrConversationInvalid
	}
	ctx = conversationContext(ctx)
	run, tasks, err := engine.loadRunAndTasks(ctx, input.RunID)
	if err != nil {
		return loadedConversation{}, err
	}
	bindings, turns, err := engine.projectTasks(input.Token, run, tasks)
	if err != nil {
		return loadedConversation{}, err
	}
	status, canRespond, err := conversationViewState(
		run,
		tasks,
		engine.executor != nil,
	)
	if err != nil {
		return loadedConversation{}, err
	}
	if canRespond {
		if len(turns) == 0 || len(bindings) != len(turns) {
			return loadedConversation{}, ErrConversationUnavailable
		}
		current := len(turns) - 1
		switch turns[current].Status {
		case workflows.HumanTaskStatusWaiting,
			workflows.HumanTaskStatusRecoveryRequired:
			turns[current].ResponseToken = bindings[current].responseToken
		default:
			return loadedConversation{}, ErrConversationUnavailable
		}
	}
	return loadedConversation{
		view: ConversationView{
			Status:     status,
			CanRespond: canRespond,
			Turns:      turns,
		},
		bindings: bindings,
	}, nil
}

// GetRun and ListHumanTasks are separate store operations. A second run read
// fences a concurrent waiting-to-running-to-waiting continuation.
func (engine *ConversationEngine) loadRunAndTasks(
	ctx context.Context,
	runID string,
) (*workflows.Run, []workflows.WorkflowHumanTask, error) {
	const maximumSnapshotAttempts = 8
	tasksReader := &workflows.Executor{Store: engine.runs}
	for attempt := 0; attempt < maximumSnapshotAttempts; attempt++ {
		before, err := engine.runs.GetRun(ctx, runID)
		if err != nil {
			return nil, nil, sanitizeConversationError(ctx, err)
		}
		if !ValidPrivateRun(before, runID) {
			return nil, nil, ErrConversationUnavailable
		}
		tasks, err := tasksReader.ListHumanTasks(ctx, runID)
		if err != nil {
			return nil, nil, sanitizeConversationError(ctx, err)
		}
		after, err := engine.runs.GetRun(ctx, runID)
		if err != nil {
			return nil, nil, sanitizeConversationError(ctx, err)
		}
		if !ValidPrivateRun(after, runID) {
			return nil, nil, ErrConversationUnavailable
		}
		if sameConversationRunSnapshot(before, after) {
			return after, tasks, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
	}
	return nil, nil, ErrConversationUnavailable
}

func sameConversationRunSnapshot(first, second *workflows.Run) bool {
	if first == nil || second == nil {
		return false
	}
	return first.ID == second.ID &&
		first.WorkflowRef == second.WorkflowRef &&
		first.Status == second.Status &&
		first.UpdatedAt.Equal(second.UpdatedAt) &&
		sameConversationTime(first.CompletedAt, second.CompletedAt) &&
		sameConversationTime(first.CancelRequestedAt, second.CancelRequestedAt)
}

func sameConversationTime(first, second *time.Time) bool {
	if first == nil || second == nil {
		return first == nil && second == nil
	}
	return first.Equal(*second)
}

func (engine *ConversationEngine) projectTasks(
	tokenFactory ConversationTokenFactory,
	run *workflows.Run,
	tasks []workflows.WorkflowHumanTask,
) ([]conversationBinding, []ConversationTurnView, error) {
	if run == nil || len(tasks) > workflows.MaxWorkflowGateCount {
		return nil, nil, ErrConversationUnavailable
	}
	bindings := make([]conversationBinding, len(tasks))
	turns := make([]ConversationTurnView, len(tasks))
	seenIDs := make(map[string]struct{}, len(tasks))
	seenSteps := make(map[string]struct{}, len(tasks))
	nonAnswered := 0
	for index, task := range tasks {
		waitingRevision, err := validateConversationTask(run, task)
		if err != nil {
			return nil, nil, err
		}
		if _, duplicate := seenIDs[task.ID]; duplicate {
			return nil, nil, ErrConversationUnavailable
		}
		if _, duplicate := seenSteps[task.StepID]; duplicate {
			return nil, nil, ErrConversationUnavailable
		}
		if index > 0 {
			previous := tasks[index-1]
			if task.CreatedAt.Before(previous.CreatedAt) ||
				(task.CreatedAt.Equal(previous.CreatedAt) && task.ID <= previous.ID) {
				return nil, nil, ErrConversationUnavailable
			}
		}
		seenIDs[task.ID] = struct{}{}
		seenSteps[task.StepID] = struct{}{}
		if task.Status != workflows.HumanTaskStatusAnswered {
			nonAnswered++
			if index != len(tasks)-1 {
				return nil, nil, ErrConversationUnavailable
			}
		}
		token, err := tokenFactory(task, waitingRevision)
		if err != nil || !ValidConversationResponseToken(token) {
			return nil, nil, ErrConversationUnavailable
		}
		bindings[index] = conversationBinding{
			task:            task,
			responseToken:   token,
			waitingRevision: waitingRevision,
		}
		turn := ConversationTurnView{
			Status:    task.Status,
			Title:     task.Title,
			Questions: task.Questions,
		}
		switch task.Status {
		case workflows.HumanTaskStatusAnswered,
			workflows.HumanTaskStatusContinuing,
			workflows.HumanTaskStatusRecoveryRequired:
			response, ok := task.Response.(string)
			if !ok || !validConversationResponse(response, engine.maxResponseBytes) ||
				task.ResponseID != ConversationResponseID(
					engine.responseIDDomain,
					token,
					response,
				) {
				return nil, nil, ErrConversationUnavailable
			}
			turn.Response = response
		}
		turns[index] = turn
	}
	if nonAnswered > 1 {
		return nil, nil, ErrConversationUnavailable
	}
	return bindings, turns, nil
}

func validateConversationTask(
	run *workflows.Run,
	task workflows.WorkflowHumanTask,
) (uint64, error) {
	if run == nil || task.RunID != run.ID || task.WorkflowRef != WorkflowRef ||
		task.JobID != conversationGateJobID ||
		!conversationTaskStepPattern.MatchString(task.StepID) ||
		!validConversationPrefixedHexID(task.ID, "ht_") ||
		task.ID != conversationHumanTaskID(run.ID, task.JobID, task.StepID) ||
		task.InputHash != strings.TrimSpace(task.InputHash) || task.InputHash == "" ||
		!utf8.ValidString(task.InputHash) ||
		len(task.InputHash) > workflows.MaxHumanTaskInputHashBytes ||
		task.Title != strings.TrimSpace(task.Title) || task.Title == "" ||
		!utf8.ValidString(task.Title) || strings.IndexByte(task.Title, 0) >= 0 ||
		len(task.Title) > workflows.MaxHumanTaskTitleBytes || task.Questions == nil ||
		!exactConversationResponseSchema(task.ResponseSchema) ||
		task.CreatedAt.IsZero() || task.UpdatedAt.IsZero() ||
		task.CreatedAt.Before(run.CreatedAt) || task.UpdatedAt.Before(task.CreatedAt) ||
		task.UpdatedAt.After(run.UpdatedAt) {
		return 0, ErrConversationUnavailable
	}
	if !validConversationQuestions(task.Questions) ||
		!conversationTaskInputHashMatches(task) {
		return 0, ErrConversationUnavailable
	}
	step, exists := run.Steps[task.JobID+"/"+task.StepID]
	if !exists {
		return 0, ErrConversationUnavailable
	}
	switch task.Status {
	case workflows.HumanTaskStatusWaiting:
		if task.Revision == 0 || task.ResponseID != "" || task.Response != nil ||
			task.AnsweredAt != nil || task.CanceledAt != nil || task.RetryAt != nil ||
			step.Status != workflows.RunStatusWaiting {
			return 0, ErrConversationUnavailable
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
			return 0, ErrConversationUnavailable
		}
		if task.Status == workflows.HumanTaskStatusAnswered && task.RetryAt != nil ||
			task.Status != workflows.HumanTaskStatusAnswered && task.RetryAt == nil {
			return 0, ErrConversationUnavailable
		}
		return task.Revision - 1, nil
	case workflows.HumanTaskStatusCanceled:
		if task.Revision < 2 || task.ResponseID != "" || task.Response != nil ||
			task.AnsweredAt != nil || task.CanceledAt == nil ||
			task.CanceledAt.Before(task.CreatedAt) ||
			task.CanceledAt.After(task.UpdatedAt) || task.RetryAt != nil ||
			step.Status != workflows.RunStatusWaiting {
			return 0, ErrConversationUnavailable
		}
		return task.Revision - 1, nil
	default:
		return 0, ErrConversationUnavailable
	}
}

func conversationTaskInputHashMatches(task workflows.WorkflowHumanTask) bool {
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

func conversationHumanTaskID(runID, jobID, stepID string) string {
	digest := sha256.Sum256([]byte(runID + "\x00" + jobID + "\x00" + stepID))
	return "ht_" + hex.EncodeToString(digest[:16])
}

func validConversationQuestions(value any) bool {
	nodes := 0
	var visit func(any, int) bool
	visit = func(candidate any, depth int) bool {
		if depth > workflows.MaxWorkflowGateJSONDepth {
			return false
		}
		nodes++
		if nodes > workflows.MaxWorkflowGateJSONNodes {
			return false
		}
		switch typed := candidate.(type) {
		case nil, bool:
			return true
		case string:
			return utf8.ValidString(typed)
		case json.Number:
			if !utf8.ValidString(typed.String()) {
				return false
			}
			_, err := json.Marshal(typed)
			return err == nil
		case float64:
			_, err := json.Marshal(typed)
			return err == nil
		case []any:
			for _, item := range typed {
				if !visit(item, depth+1) {
					return false
				}
			}
			return true
		case map[string]any:
			for key, item := range typed {
				if !utf8.ValidString(key) || !visit(item, depth+1) {
					return false
				}
			}
			return true
		default:
			return false
		}
	}
	return visit(value, 0)
}

func exactConversationResponseSchema(schema map[string]any) bool {
	if len(schema) != 1 {
		return false
	}
	typeValue, ok := schema["type"].(string)
	return ok && typeValue == "string"
}

func conversationViewState(
	run *workflows.Run,
	tasks []workflows.WorkflowHumanTask,
	runtimeEnabled bool,
) (string, bool, error) {
	if run == nil {
		return "", false, ErrConversationUnavailable
	}
	var current *workflows.WorkflowHumanTask
	if len(tasks) > 0 {
		candidate := tasks[len(tasks)-1]
		current = &candidate
	}
	switch run.Status {
	case workflows.RunStatusWaiting:
		if current == nil || current.Status != workflows.HumanTaskStatusWaiting {
			return "", false, ErrConversationUnavailable
		}
		return ConversationStatusWaiting, runtimeEnabled, nil
	case workflows.RunStatusRunning:
		if current == nil {
			return ConversationStatusProcessing, false, nil
		}
		switch current.Status {
		case workflows.HumanTaskStatusContinuing:
			return ConversationStatusContinuing, false, nil
		case workflows.HumanTaskStatusRecoveryRequired:
			return ConversationStatusRecoveryRequired, runtimeEnabled, nil
		default:
			return "", false, ErrConversationUnavailable
		}
	case workflows.RunStatusSucceeded, workflows.RunStatusSkipped:
		if current != nil && current.Status != workflows.HumanTaskStatusAnswered {
			return "", false, ErrConversationUnavailable
		}
		return ConversationStatusCompleted, false, nil
	case workflows.RunStatusFailed, workflows.RunStatusCanceled:
		if current != nil {
			switch current.Status {
			case workflows.HumanTaskStatusAnswered,
				workflows.HumanTaskStatusCanceled:
			default:
				return "", false, ErrConversationUnavailable
			}
		}
		return ConversationStatusFailed, false, nil
	default:
		return "", false, ErrConversationUnavailable
	}
}

// ConversationResponseID binds a normalized answer to the opaque task fence
// in a product-specific domain.
func ConversationResponseID(domain, token, response string) string {
	digest := sha256.New()
	WriteConversationHashField(digest, []byte(domain))
	WriteConversationHashField(digest, []byte(token))
	WriteConversationHashField(digest, []byte(response))
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}

func WriteConversationHashField(digest hash.Hash, value []byte) {
	WriteConversationHashUint64(digest, uint64(len(value)))
	_, _ = digest.Write(value)
}

func WriteConversationHashUint64(digest hash.Hash, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = digest.Write(encoded[:])
}

func ValidConversationResponseToken(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 ||
		!strings.HasPrefix(value, "sha256:") {
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

func normalizeConversationResponse(value string, maximum int) (string, error) {
	normalized := strings.TrimSpace(value)
	if !validConversationResponse(normalized, maximum) {
		return "", ErrConversationInvalid
	}
	return normalized, nil
}

func validConversationResponse(value string, maximum int) bool {
	return value == strings.TrimSpace(value) && value != "" &&
		utf8.ValidString(value) && len(value) <= maximum &&
		strings.IndexByte(value, 0) < 0
}

func conversationResponseWasAccepted(
	bindings []conversationBinding,
	token, responseID, response string,
) bool {
	for _, binding := range bindings {
		if binding.responseToken != token || binding.task.ResponseID != responseID {
			continue
		}
		return conversationTaskResponseMatches(binding.task, responseID, response)
	}
	return false
}

func conversationTaskResponseMatches(
	task workflows.WorkflowHumanTask,
	responseID, response string,
) bool {
	if task.ResponseID != responseID {
		return false
	}
	stored, ok := task.Response.(string)
	return ok && stored == response
}

func (engine *ConversationEngine) available() bool {
	return engine != nil && engine.runs != nil && !nilInterface(engine.runs) &&
		engine.maxResponseBytes > 0 &&
		validConversationDomain(engine.responseIDDomain)
}

func validConversationDomain(value string) bool {
	return value == strings.TrimSpace(value) && value != "" &&
		utf8.ValidString(value) && len(value) <= 256 &&
		strings.IndexByte(value, 0) < 0
}

func validConversationRunID(value string) bool {
	return validConversationPrefixedHexID(value, "wr_")
}

func validConversationPrefixedHexID(value, prefix string) bool {
	if len(value) != len(prefix)+32 || !strings.HasPrefix(value, prefix) {
		return false
	}
	for _, character := range value[len(prefix):] {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func conversationContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func sanitizeConversationError(ctx context.Context, err error) error {
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
	case errors.Is(err, ErrConversationInvalid),
		errors.Is(err, workflows.ErrHumanTaskResponseInvalid):
		return ErrConversationInvalid
	case errors.Is(err, ErrConversationConflict),
		errors.Is(err, workflows.ErrHumanTaskStale),
		errors.Is(err, workflows.ErrHumanTaskConflict),
		errors.Is(err, workflows.ErrRunAdmissionConflict),
		errors.Is(err, workflows.ErrRunConcurrencyLimit):
		return ErrConversationConflict
	default:
		return ErrConversationUnavailable
	}
}
