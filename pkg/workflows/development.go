package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

var (
	ErrActiveDevelopmentExists = errors.New("a workflow development session is already active")
	ErrNoActiveDevelopment     = errors.New("no active workflow development session")
	ErrDevelopmentBusy         = errors.New("a workflow development operation is already in progress")
)

const (
	WorkflowDevelopmentReasonNew                 = "new"
	WorkflowDevelopmentReasonEdit                = "edit"
	WorkflowDevelopmentReasonVersionRevalidation = "version_revalidation"

	WorkflowDevelopmentStatusPlanning       = "planning"
	WorkflowDevelopmentStatusEditing        = "editing"
	WorkflowDevelopmentStatusValidating     = "validating"
	WorkflowDevelopmentStatusTesting        = "testing"
	WorkflowDevelopmentStatusReadyToPublish = "ready_to_publish"

	workflowDevelopmentDir    = "workflow_dev"
	workflowDevelopmentActive = "active.json"

	EventBackedDraftTestFailureDiagnostic  = "event-backed draft test failed; diagnostic details withheld"
	EventBackedDraftTestCanceledDiagnostic = "event-backed draft test canceled; diagnostic details withheld"
	EventBackedDraftRunErrorDiagnostic     = "event-backed draft run failed; diagnostic details withheld"
	EventBackedDraftCancelReasonDiagnostic = "event-backed draft run canceled; diagnostic details withheld"
	EventBackedDraftJobErrorDiagnostic     = "event-backed draft job failed; diagnostic details withheld"
	EventBackedDraftStepErrorDiagnostic    = "event-backed draft step failed; diagnostic details withheld"
	EventBackedDraftEventMessageDiagnostic = "event-backed draft lifecycle message withheld"
	EventBackedDraftEventPayloadDiagnostic = "event-backed draft lifecycle payload withheld"
)

type WorkflowDevelopmentSession struct {
	ID                    string                         `json:"id"`
	SessionRevision       string                         `json:"session_revision"`
	DraftRevision         string                         `json:"draft_revision"`
	BaseTargetRevision    string                         `json:"base_target_revision"`
	Reason                string                         `json:"reason"`
	Status                string                         `json:"status"`
	Prompt                string                         `json:"prompt,omitempty"`
	SourceWorkflowRef     string                         `json:"source_workflow_ref,omitempty"`
	TargetWorkflowRef     string                         `json:"target_workflow_ref"`
	TargetPicoclawVersion string                         `json:"target_picoclaw_version,omitempty"`
	TargetGitCommit       string                         `json:"target_git_commit,omitempty"`
	YAML                  string                         `json:"yaml"`
	Validation            *WorkflowDevelopmentValidation `json:"validation,omitempty"`
	LastTest              *WorkflowDevelopmentTest       `json:"last_test,omitempty"`
	CreatedAt             time.Time                      `json:"created_at"`
	UpdatedAt             time.Time                      `json:"updated_at"`
}

type WorkflowDevelopmentValidation struct {
	Valid       bool                      `json:"valid"`
	Errors      []WorkflowValidationIssue `json:"errors,omitempty"`
	Warnings    []WorkflowValidationIssue `json:"warnings,omitempty"`
	ValidatedAt time.Time                 `json:"validated_at"`
}

type WorkflowDevelopmentTest struct {
	DraftKey          string    `json:"draft_key"`
	DraftRevision     string    `json:"draft_revision,omitempty"`
	TargetWorkflowRef string    `json:"target_workflow_ref"`
	EventID           string    `json:"event_id,omitempty"`
	RunID             string    `json:"run_id,omitempty"`
	Status            string    `json:"status"`
	Error             string    `json:"error,omitempty"`
	TestedAt          time.Time `json:"tested_at"`
}

type WorkflowDevelopmentStartRequest struct {
	Reason    string `json:"reason,omitempty"`
	Prompt    string `json:"prompt,omitempty"`
	Ref       string `json:"ref,omitempty"`
	TargetRef string `json:"target_ref,omitempty"`
}

type WorkflowDevelopmentReviseRequest struct {
	Prompt     string  `json:"prompt,omitempty"`
	TargetRef  string  `json:"target_ref,omitempty"`
	YAML       *string `json:"yaml,omitempty"`
	Regenerate bool    `json:"regenerate,omitempty"`
}

type WorkflowDevelopmentPublishResult struct {
	WorkflowRef string                      `json:"workflow_ref"`
	Session     *WorkflowDevelopmentSession `json:"session"`
}

func GetWorkflowDevelopmentSession(workspace string) (*WorkflowDevelopmentSession, error) {
	unlock, err := lockWorkflowMutation(workspace)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return getWorkflowDevelopmentSessionLocked(workspace)
}

// getWorkflowDevelopmentSessionLocked reads the active development snapshot
// after the caller has acquired the workspace mutation lock. Keeping the
// unlocked read private ensures public status requests first finish or reject
// any prepared template/publish transaction without making already-locked
// mutation paths recursively acquire the non-reentrant lock.
func getWorkflowDevelopmentSessionLocked(
	workspace string,
) (*WorkflowDevelopmentSession, error) {
	activePath, err := checkedActiveDevelopmentPath(workspace)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(activePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var session WorkflowDevelopmentSession
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, err
	}
	if session.BaseTargetRevision == "" {
		session.BaseTargetRevision = WorkflowTargetRevisionUnknown
	}
	if session.DraftRevision == "" || session.SessionRevision == "" {
		if err := refreshWorkflowDevelopmentRevisions(&session); err != nil {
			return nil, err
		}
	}
	return &session, nil
}

func StartWorkflowDevelopment(
	ctx context.Context,
	workspace string,
	runtime RuntimeCompatibility,
	req WorkflowDevelopmentStartRequest,
	opts ...LocalOption,
) (*WorkflowDevelopmentSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	unlock, lockErr := lockWorkflowMutation(workspace)
	if lockErr != nil {
		return nil, lockErr
	}
	defer unlock()
	runtime = NormalizeRuntimeCompatibility(runtime)
	reason := normalizeDevelopmentReason(req.Reason)
	prompt := strings.TrimSpace(req.Prompt)
	sourceRef := strings.TrimSpace(req.Ref)
	targetRef := strings.TrimSpace(req.TargetRef)
	var draftYAML string
	if reason == WorkflowDevelopmentReasonNew {
		if targetRef == "" {
			targetRef = WorkflowRefFromPrompt(prompt)
		}
		draftYAML = GenerateWorkflowDraftYAML(prompt)
	} else {
		if sourceRef == "" {
			sourceRef = targetRef
		}
		canonicalSource, err := CanonicalLocalRef(sourceRef)
		if err != nil {
			return nil, err
		}
		sourceRef = canonicalSource
		local := collectLocalOptions(opts...)
		resolved, err := local.resolver(workspace).ResolveLocal(sourceRef)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(resolved.Path)
		if err != nil {
			return nil, err
		}
		draftYAML = string(data)
		if targetRef == "" {
			targetRef = sourceRef
		}
	}
	canonicalTarget, err := CanonicalLocalRef(targetRef)
	if err != nil {
		return nil, err
	}
	baseTargetRevision, err := captureWorkflowDevelopmentTargetRevision(
		workspace,
		canonicalTarget,
		opts...,
	)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	session := &WorkflowDevelopmentSession{
		ID:                    fmt.Sprintf("dev_%d", now.UnixNano()),
		Reason:                reason,
		Status:                WorkflowDevelopmentStatusEditing,
		Prompt:                prompt,
		SourceWorkflowRef:     sourceRef,
		TargetWorkflowRef:     canonicalTarget,
		TargetPicoclawVersion: runtime.PicoclawVersion,
		TargetGitCommit:       runtime.GitCommit,
		YAML:                  draftYAML,
		BaseTargetRevision:    baseTargetRevision,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	session.Validation = validateDevelopmentYAML(session.YAML)
	if err := writeNewActiveDevelopment(workspace, session); err != nil {
		return nil, err
	}
	return session, nil
}

func ReviseWorkflowDevelopment(
	workspace string,
	req WorkflowDevelopmentReviseRequest,
	opts ...LocalOption,
) (*WorkflowDevelopmentSession, error) {
	unlock, lockErr := lockWorkflowMutation(workspace)
	if lockErr != nil {
		return nil, lockErr
	}
	defer unlock()
	session, sessionErr := requireActiveDevelopment(workspace)
	if sessionErr != nil {
		return nil, sessionErr
	}
	if err := ensureNoCurrentRunningDevelopmentTest(session); err != nil {
		return nil, err
	}
	previousTargetRef := session.TargetWorkflowRef
	previousYAML := session.YAML
	if strings.TrimSpace(req.Prompt) != "" {
		session.Prompt = strings.TrimSpace(req.Prompt)
	}
	if strings.TrimSpace(req.TargetRef) != "" {
		targetRef, canonicalErr := CanonicalLocalRef(req.TargetRef)
		if canonicalErr != nil {
			return nil, canonicalErr
		}
		session.TargetWorkflowRef = targetRef
	}
	if session.TargetWorkflowRef != previousTargetRef ||
		session.BaseTargetRevision == "" ||
		session.BaseTargetRevision == WorkflowTargetRevisionUnknown {
		baseTargetRevision, revisionErr := captureWorkflowDevelopmentTargetRevision(
			workspace,
			session.TargetWorkflowRef,
			opts...,
		)
		if revisionErr != nil {
			return nil, revisionErr
		}
		session.BaseTargetRevision = baseTargetRevision
	}
	if req.Regenerate {
		session.YAML = GenerateWorkflowDraftYAML(session.Prompt)
	} else if req.YAML != nil {
		session.YAML = *req.YAML
	}
	draftChanged := session.TargetWorkflowRef != previousTargetRef ||
		session.YAML != previousYAML
	if draftChanged {
		session.Status = WorkflowDevelopmentStatusEditing
		session.Validation = nil
		session.LastTest = nil
	}
	session.UpdatedAt = time.Now().UTC()
	if err := writeActiveDevelopment(workspace, session); err != nil {
		return nil, err
	}
	return session, nil
}

func ValidateWorkflowDevelopment(
	workspace string,
	opts ...LocalOption,
) (*WorkflowDevelopmentSession, error) {
	unlock, lockErr := lockWorkflowMutation(workspace)
	if lockErr != nil {
		return nil, lockErr
	}
	defer unlock()
	session, sessionErr := requireActiveDevelopment(workspace)
	if sessionErr != nil {
		return nil, sessionErr
	}
	if err := ensureNoCurrentRunningDevelopmentTest(session); err != nil {
		return nil, err
	}
	if session.BaseTargetRevision == "" ||
		session.BaseTargetRevision == WorkflowTargetRevisionUnknown {
		baseTargetRevision, revisionErr := captureWorkflowDevelopmentTargetRevision(
			workspace,
			session.TargetWorkflowRef,
			opts...,
		)
		if revisionErr != nil {
			return nil, revisionErr
		}
		session.BaseTargetRevision = baseTargetRevision
	}
	session.Status = WorkflowDevelopmentStatusValidating
	session.Validation = validateDevelopmentYAML(session.YAML)
	if session.Validation.Valid && hasCurrentSuccessfulDevelopmentTest(session) {
		session.Status = WorkflowDevelopmentStatusReadyToPublish
	} else {
		session.Status = WorkflowDevelopmentStatusEditing
	}
	session.UpdatedAt = time.Now().UTC()
	if err := writeActiveDevelopment(workspace, session); err != nil {
		return nil, err
	}
	return session, nil
}

func PublishWorkflowDevelopment(
	ctx context.Context,
	workspace string,
	runtime RuntimeCompatibility,
	opts ...LocalOption,
) (*WorkflowDevelopmentPublishResult, error) {
	return publishWorkflowDevelopmentTransaction(
		ctx,
		workspace,
		nil,
		runtime,
		nil,
		nil,
		opts...,
	)
}

func RecordWorkflowDevelopmentTest(
	workspace string,
	result *RunResult,
	testErr error,
) (*WorkflowDevelopmentSession, error) {
	unlock, lockErr := lockWorkflowMutation(workspace)
	if lockErr != nil {
		return nil, lockErr
	}
	defer unlock()
	session, err := requireActiveDevelopment(workspace)
	if err != nil {
		return nil, err
	}
	recordWorkflowDevelopmentTest(session, "", result, testErr)
	if err := writeActiveDevelopment(workspace, session); err != nil {
		return nil, err
	}
	return session, nil
}

// RecordWorkflowDevelopmentEventTest persists a draft-test result together
// with the durable event selected for its server-owned preview context.
func RecordWorkflowDevelopmentEventTest(
	workspace string,
	eventID string,
	result *RunResult,
	testErr error,
) (*WorkflowDevelopmentSession, error) {
	unlock, lockErr := lockWorkflowMutation(workspace)
	if lockErr != nil {
		return nil, lockErr
	}
	defer unlock()
	session, err := requireActiveDevelopment(workspace)
	if err != nil {
		return nil, err
	}
	result, testErr = SanitizeEventBackedDraftTestOutcome(result, testErr)
	recordWorkflowDevelopmentTest(session, eventID, result, testErr)
	if err := writeActiveDevelopment(workspace, session); err != nil {
		return nil, err
	}
	return session, nil
}

func RecordWorkflowDevelopmentTestIfCurrent(
	workspace string,
	sessionID string,
	draftKey string,
	result *RunResult,
	testErr error,
) (*WorkflowDevelopmentSession, bool, error) {
	unlock, lockErr := lockWorkflowMutation(workspace)
	if lockErr != nil {
		return nil, false, lockErr
	}
	defer unlock()
	session, err := requireActiveDevelopment(workspace)
	if err != nil {
		return nil, false, err
	}
	if session.ID != sessionID ||
		WorkflowDevelopmentDraftKey(session.TargetWorkflowRef, session.YAML) != draftKey {
		return session, false, nil
	}
	recordWorkflowDevelopmentTest(session, "", result, testErr)
	if err := writeActiveDevelopment(workspace, session); err != nil {
		return nil, false, err
	}
	return session, true, nil
}

// RecordWorkflowDevelopmentEventTestIfCurrent applies async completion only
// to the exact active draft and retains the event identity that launched it.
func RecordWorkflowDevelopmentEventTestIfCurrent(
	workspace string,
	sessionID string,
	draftKey string,
	eventID string,
	result *RunResult,
	testErr error,
) (*WorkflowDevelopmentSession, bool, error) {
	unlock, lockErr := lockWorkflowMutation(workspace)
	if lockErr != nil {
		return nil, false, lockErr
	}
	defer unlock()
	session, err := requireActiveDevelopment(workspace)
	if err != nil {
		return nil, false, err
	}
	if session.ID != sessionID ||
		WorkflowDevelopmentDraftKey(session.TargetWorkflowRef, session.YAML) != draftKey {
		return session, false, nil
	}
	trimmedEventID := strings.TrimSpace(eventID)
	if current := session.LastTest; current != nil && current.DraftKey == draftKey {
		if current.EventID != trimmedEventID {
			return session, false, nil
		}
		if result != nil &&
			current.RunID != "" &&
			result.RunID != "" &&
			current.RunID != result.RunID {
			return session, false, nil
		}
	}
	result, testErr = SanitizeEventBackedDraftTestOutcome(result, testErr)
	recordWorkflowDevelopmentTest(session, trimmedEventID, result, testErr)
	if err := writeActiveDevelopment(workspace, session); err != nil {
		return nil, false, err
	}
	return session, true, nil
}

// SanitizeEventBackedDraftTestOutcome returns the browser-safe snapshot used
// by event-backed draft-test responses and development-session persistence.
// Workflow outputs remain available, but provider/tool error text never does.
func SanitizeEventBackedDraftTestOutcome(
	result *RunResult,
	testErr error,
) (*RunResult, error) {
	diagnostic := EventBackedDraftTestFailureDiagnostic
	if result != nil && result.Status == RunStatusCanceled {
		diagnostic = EventBackedDraftTestCanceledDiagnostic
	}
	var projected *RunResult
	if result != nil {
		cloned := *result
		cloned.Outputs = cloneMap(result.Outputs)
		if cloned.Error != "" ||
			cloned.Status == RunStatusFailed ||
			cloned.Status == RunStatusCanceled {
			cloned.Error = diagnostic
		}
		projected = &cloned
	}
	if testErr != nil {
		return projected, errors.New(diagnostic)
	}
	return projected, nil
}

// IsEventBackedDraftRun identifies the narrow browser projection boundary:
// draft workflow identity plus a persisted structured event context.
func IsEventBackedDraftRun(run *Run) bool {
	if run == nil || !strings.HasPrefix(strings.TrimSpace(run.WorkflowRef), "draft:") {
		return false
	}
	return len(run.Event) != 0
}

const eventBackedDraftAncestryMaximumDepth = 64

// IsEventBackedDraftRunFamily resolves reusable children back to their root.
// Missing/cyclic/depth-exhausted ancestry fails closed to masking when the run
// carries inherited event context.
func IsEventBackedDraftRunFamily(
	ctx context.Context,
	store RunStore,
	run *Run,
) bool {
	if run == nil {
		return false
	}
	current := run
	carriesEvent := len(run.Event) != 0
	seen := make(map[string]struct{}, eventBackedDraftAncestryMaximumDepth)
	for depth := 0; depth < eventBackedDraftAncestryMaximumDepth; depth++ {
		carriesEvent = carriesEvent || len(current.Event) != 0
		if IsEventBackedDraftRun(current) {
			return true
		}
		parentID := strings.TrimSpace(current.ParentRunID)
		if parentID == "" {
			return false
		}
		if _, exists := seen[parentID]; exists || store == nil {
			return carriesEvent
		}
		seen[parentID] = struct{}{}
		parent, err := store.GetRun(ctx, parentID)
		if err != nil || parent == nil {
			return carriesEvent
		}
		current = parent
	}
	return carriesEvent
}

// ProjectEventBackedDraftRunForBrowser masks diagnostic fields without
// changing status, identifiers, structured redacted event context, or outputs.
func ProjectEventBackedDraftRunForBrowser(run *Run) *Run {
	return ProjectWorkflowRunForBrowser(run, IsEventBackedDraftRun(run))
}

// ProjectWorkflowRunForBrowser applies an ancestry decision already resolved
// by the API or a batched run listing.
func ProjectWorkflowRunForBrowser(run *Run, eventBackedDraft bool) *Run {
	if run == nil {
		return nil
	}
	projected := cloneRun(run)
	if !eventBackedDraft {
		return projected
	}
	if projected.Error != "" {
		projected.Error = EventBackedDraftRunErrorDiagnostic
	}
	if projected.CancelReason != "" {
		projected.CancelReason = EventBackedDraftCancelReasonDiagnostic
	}
	for key, job := range projected.Jobs {
		if job.Error != "" {
			job.Error = EventBackedDraftJobErrorDiagnostic
			projected.Jobs[key] = job
		}
	}
	for key, step := range projected.Steps {
		if step.Error != "" {
			step.Error = EventBackedDraftStepErrorDiagnostic
			projected.Steps[key] = step
		}
	}
	return projected
}

// ProjectEventBackedDraftRunsForBrowser applies the same projection to a run
// listing while leaving production and manual run diagnostics unchanged.
func ProjectEventBackedDraftRunsForBrowser(runs []Run) []Run {
	byID := make(map[string]*Run, len(runs))
	for index := range runs {
		byID[runs[index].ID] = &runs[index]
	}
	memo := make(map[string]bool, len(runs))
	projected := make([]Run, len(runs))
	for index := range runs {
		masked := eventBackedDraftRunFamilyInMap(&runs[index], byID, memo)
		projected[index] = *ProjectWorkflowRunForBrowser(&runs[index], masked)
	}
	return projected
}

func eventBackedDraftRunFamilyInMap(
	run *Run,
	byID map[string]*Run,
	memo map[string]bool,
) bool {
	if run == nil {
		return false
	}
	current := run
	path := make([]string, 0, eventBackedDraftAncestryMaximumDepth)
	seen := make(map[string]struct{}, eventBackedDraftAncestryMaximumDepth)
	masked := false
	resolved := false
	carriesEvent := len(run.Event) != 0
	for depth := 0; depth < eventBackedDraftAncestryMaximumDepth; depth++ {
		carriesEvent = carriesEvent || len(current.Event) != 0
		// Only a positive classification is safe to memoize. A false result
		// can come from an event-free path whose parent was absent from this
		// listing; an event-bearing child that reaches that same path must
		// still fail closed rather than inherit the earlier false result.
		if value := memo[current.ID]; value {
			masked = true
			resolved = true
			break
		}
		if IsEventBackedDraftRun(current) {
			masked = true
			resolved = true
			break
		}
		if _, exists := seen[current.ID]; exists {
			break
		}
		seen[current.ID] = struct{}{}
		path = append(path, current.ID)
		parentID := strings.TrimSpace(current.ParentRunID)
		if parentID == "" {
			resolved = true
			break
		}
		parent, exists := byID[parentID]
		if !exists || parent == nil {
			break
		}
		current = parent
	}
	if !resolved {
		masked = carriesEvent
	}
	if masked {
		for _, runID := range path {
			memo[runID] = true
		}
	}
	return masked
}

// ProjectEventBackedDraftEventsForBrowser masks both free-form lifecycle
// messages and payloads. Kind, time, run/job/step IDs remain visible.
func ProjectEventBackedDraftEventsForBrowser(
	run *Run,
	events []RunEvent,
) []RunEvent {
	return ProjectWorkflowRunEventsForBrowser(events, IsEventBackedDraftRun(run))
}

// ProjectWorkflowRunEventsForBrowser applies a resolved ancestry decision to
// lifecycle events.
func ProjectWorkflowRunEventsForBrowser(
	events []RunEvent,
	eventBackedDraft bool,
) []RunEvent {
	projected := make([]RunEvent, len(events))
	for index, event := range events {
		event.Payload = cloneMap(event.Payload)
		if eventBackedDraft {
			if event.Message != "" {
				event.Message = EventBackedDraftEventMessageDiagnostic
			}
			if len(event.Payload) != 0 {
				event.Payload = map[string]any{
					"diagnostic": EventBackedDraftEventPayloadDiagnostic,
				}
			}
		}
		projected[index] = event
	}
	return projected
}

func recordWorkflowDevelopmentTest(
	session *WorkflowDevelopmentSession,
	eventID string,
	result *RunResult,
	testErr error,
) {
	status := "validation_failed"
	runID := ""
	errorMessage := ""
	if result != nil {
		status = result.Status
		runID = result.RunID
		errorMessage = result.Error
	}
	if testErr != nil {
		errorMessage = testErr.Error()
		if result == nil {
			status = "validation_failed"
		}
	}
	if strings.TrimSpace(status) == "" {
		if errorMessage == "" {
			status = RunStatusSucceeded
		} else {
			status = RunStatusFailed
		}
	}
	now := time.Now().UTC()
	session.LastTest = &WorkflowDevelopmentTest{
		DraftKey:          WorkflowDevelopmentDraftKey(session.TargetWorkflowRef, session.YAML),
		DraftRevision:     session.DraftRevision,
		TargetWorkflowRef: session.TargetWorkflowRef,
		EventID:           strings.TrimSpace(eventID),
		RunID:             runID,
		Status:            status,
		Error:             errorMessage,
		TestedAt:          now,
	}
	switch status {
	case RunStatusRunning:
		session.Status = WorkflowDevelopmentStatusTesting
	case RunStatusSucceeded:
		if session.Validation != nil && session.Validation.Valid {
			session.Status = WorkflowDevelopmentStatusReadyToPublish
		} else {
			session.Status = WorkflowDevelopmentStatusEditing
		}
	default:
		session.Status = WorkflowDevelopmentStatusEditing
	}
	session.UpdatedAt = now
}

func WorkflowDevelopmentDraftKey(targetRef string, yaml string) string {
	return strings.TrimSpace(targetRef) + "\x00" + normalizeDevelopmentYAMLForKey(yaml)
}

func requireCurrentSuccessfulDevelopmentTest(session *WorkflowDevelopmentSession) error {
	if session == nil {
		return ErrNoActiveDevelopment
	}
	if session.LastTest == nil {
		return fmt.Errorf("workflow draft must pass a current test run before publish")
	}
	if !workflowDevelopmentTestMatchesDraft(session) {
		return fmt.Errorf("workflow draft test is stale; run the draft again before publish")
	}
	if session.LastTest.Status != RunStatusSucceeded {
		return fmt.Errorf("workflow draft test must succeed before publish")
	}
	return nil
}

func ensureNoCurrentRunningDevelopmentTest(session *WorkflowDevelopmentSession) error {
	if session == nil || session.LastTest == nil {
		return nil
	}
	if session.LastTest.Status != RunStatusRunning {
		return nil
	}
	if !workflowDevelopmentTestMatchesDraft(session) {
		return nil
	}
	return ErrDevelopmentBusy
}

func hasCurrentSuccessfulDevelopmentTest(session *WorkflowDevelopmentSession) bool {
	return session != nil &&
		session.LastTest != nil &&
		session.LastTest.Status == RunStatusSucceeded &&
		workflowDevelopmentTestMatchesDraft(session)
}

func workflowDevelopmentTestMatchesDraft(session *WorkflowDevelopmentSession) bool {
	if session == nil || session.LastTest == nil {
		return false
	}
	if session.LastTest.DraftRevision != "" {
		return session.LastTest.DraftRevision == session.DraftRevision
	}
	return session.LastTest.DraftKey ==
		WorkflowDevelopmentDraftKey(session.TargetWorkflowRef, session.YAML)
}

func DiscardWorkflowDevelopment(workspace string) (*WorkflowDevelopmentSession, error) {
	unlock, lockErr := lockWorkflowMutation(workspace)
	if lockErr != nil {
		return nil, lockErr
	}
	defer unlock()
	session, err := requireActiveDevelopment(workspace)
	if err != nil {
		return nil, err
	}
	if archiveErr := archiveDevelopmentSession(workspace, session, "discarded"); archiveErr != nil {
		return nil, archiveErr
	}
	activePath, err := checkedActiveDevelopmentPath(workspace)
	if err != nil {
		return nil, err
	}
	if err := fileutil.RemoveDurable(activePath); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return session, nil
}

func WorkflowRefFromPrompt(prompt string) string {
	slug := slugFromText(prompt)
	if slug == "" {
		slug = "workflow"
	}
	return "workflows/" + slug + ".yml"
}

func GenerateWorkflowDraftYAML(prompt string) string {
	title := titleFromPrompt(prompt)
	message := strings.TrimSpace(prompt)
	if message == "" {
		message = "Describe the task this workflow should complete."
	}
	if shouldGenerateRepositoryReviewWorkflow(message) {
		return generateRepositoryReviewDraftYAML(title, message)
	}
	workflow := Workflow{
		Name: title,
		On: WorkflowTriggers{
			Manual: map[string]any{},
		},
		Jobs: map[string]Job{
			"develop": {
				Name:   "Run AI workflow",
				RunsOn: "picoclaw",
				Steps: []Step{
					{
						ID:   "run_agent",
						Name: "Ask agent",
						Uses: "agent/main",
						With: map[string]any{
							"prompt":  message,
							"history": "none",
							"cache":   "session",
						},
					},
				},
			},
		},
	}
	data, err := yaml.Marshal(workflow)
	if err != nil {
		return fallbackWorkflowDraftYAML(title, message)
	}
	return string(data)
}

func shouldGenerateRepositoryReviewWorkflow(prompt string) bool {
	normalized := strings.ToLower(strings.TrimSpace(prompt))
	if normalized == "" {
		return false
	}
	if !promptContainsAnyWord(normalized, "review", "audit", "inspect", "analyze") {
		return false
	}
	if !strings.Contains(normalized, "whole repo") &&
		!strings.Contains(normalized, "entire repo") &&
		!strings.Contains(normalized, "full repo") &&
		!strings.Contains(normalized, "repo-wide") &&
		!strings.Contains(normalized, "whole repository") &&
		!strings.Contains(normalized, "entire repository") &&
		!strings.Contains(normalized, "full repository") &&
		!strings.Contains(normalized, "repository-wide") &&
		!strings.Contains(normalized, "whole codebase") &&
		!strings.Contains(normalized, "entire codebase") &&
		!strings.Contains(normalized, "full codebase") &&
		!strings.Contains(normalized, "codebase-wide") &&
		!strings.Contains(normalized, "whole project") &&
		!strings.Contains(normalized, "entire project") &&
		!strings.Contains(normalized, "full project") &&
		!strings.Contains(normalized, "all files") &&
		!promptContainsWord(normalized, "everything") {
		return false
	}
	if strings.Contains(normalized, "pull request") ||
		promptContainsWord(normalized, "pr") ||
		strings.Contains(normalized, "diff") ||
		strings.Contains(normalized, "changed files") {
		return false
	}
	return true
}

func generateRepositoryReviewDraftYAML(title, message string) string {
	workflow := Workflow{
		Name: title,
		On: WorkflowTriggers{
			Manual: map[string]any{},
		},
		Jobs: map[string]Job{
			"review": {
				Name:   "Review repository",
				RunsOn: "picoclaw",
				Outputs: map[string]string{
					"summary": "${{ steps.review.outputs.structured.summary }}",
					"review":  "${{ steps.review.outputs.structured }}",
					"managed": "${{ steps.review.outputs.managed }}",
				},
				Steps: []Step{
					{
						ID:   "inventory",
						Name: "Inventory repository content",
						Uses: "function/git.inventory",
						With: map[string]any{
							"working_directory": ".",
							"commit":            repositoryReviewCommitFromPrompt(message),
							"target":            "all",
							"include_content":   true,
							"max_content_bytes": 65536,
						},
					},
					{
						ID:   "review",
						Name: "Review repository with managed scope split",
						Uses: "agent/main",
						With: map[string]any{
							"managed": map[string]any{
								"mode":                  "auto",
								"strategy":              "scope_split",
								"max_items_per_chunk":   4,
								"max_parallel_children": 3,
							},
							"session": "key:workflow-repository-review",
							"history": "none",
							"cache":   "session",
							"prompt":  repositoryReviewPrompt(message),
							"scope":   "${{ steps.inventory.outputs.selectedFiles }}",
							"output":  repositoryReviewOutputContract(),
						},
					},
				},
			},
		},
	}
	data, err := yaml.Marshal(workflow)
	if err != nil {
		return fallbackRepositoryReviewDraftYAML(title, message)
	}
	return string(data)
}

func repositoryReviewCommitFromPrompt(prompt string) string {
	normalized := strings.ToLower(prompt)
	if strings.Contains(normalized, "current branch") ||
		strings.Contains(normalized, "current checkout") ||
		strings.Contains(normalized, "current ref") ||
		strings.Contains(normalized, "head branch") ||
		promptContainsWord(normalized, "head") {
		return "HEAD"
	}
	if strings.Contains(normalized, "origin/main") {
		return "origin/main"
	}
	if strings.Contains(normalized, "origin-master") ||
		strings.Contains(normalized, "origin/master") {
		return "origin/master"
	}
	if promptContainsWord(normalized, "master") {
		return "master"
	}
	return "main"
}

func promptContainsAnyWord(prompt string, words ...string) bool {
	for _, word := range words {
		if promptContainsWord(prompt, word) {
			return true
		}
	}
	return false
}

func promptContainsWord(prompt, word string) bool {
	fields := strings.FieldsFunc(prompt, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	for _, field := range fields {
		if field == word {
			return true
		}
	}
	return false
}

func repositoryReviewPrompt(message string) string {
	return strings.TrimSpace(`You are executing a Codex-style repository code review.

User request:
` + message + `

Review only files from the assigned scope. The scope is the normalized repository inventory for the requested commit and includes capped file content. Prioritize actionable bugs, security issues, reliability risks, data loss, concurrency problems, behavioral regressions, and missing tests. Ignore pure style preferences and broad refactors unless they hide a concrete bug. Return findings first in priority order by severity. If no actionable issues are found, return an empty findings array and explain residual risk.`)
}

func repositoryReviewOutputContract() map[string]any {
	return map[string]any{
		"format":          "json",
		"repair_attempts": 1,
		"schema": map[string]any{
			"type":     "object",
			"required": []string{"summary", "findings", "tests", "residualRisks"},
			"properties": map[string]any{
				"summary": map[string]any{
					"type": "string",
				},
				"findings": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type":     "object",
						"required": []string{"severity", "title", "file", "evidence", "impact", "recommendation"},
						"properties": map[string]any{
							"severity": map[string]any{
								"type": "string",
								"enum": []string{"critical", "high", "medium", "low"},
							},
							"title": map[string]any{
								"type": "string",
							},
							"file": map[string]any{
								"type": "string",
							},
							"line": map[string]any{
								"type": "integer",
							},
							"evidence": map[string]any{
								"type": "string",
							},
							"impact": map[string]any{
								"type": "string",
							},
							"recommendation": map[string]any{
								"type": "string",
							},
						},
					},
				},
				"tests": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "string",
					},
				},
				"residualRisks": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "string",
					},
				},
			},
		},
	}
}

func validateDevelopmentYAML(raw string) *WorkflowDevelopmentValidation {
	validation := &WorkflowDevelopmentValidation{ValidatedAt: time.Now().UTC()}
	workflow, err := Parse([]byte(raw))
	if err != nil {
		validation.Errors = []WorkflowValidationIssue{{Message: err.Error()}}
		return validation
	}
	if err := Validate(workflow); err != nil {
		validation.Errors = ValidationIssues(err)
		return validation
	}
	validation.Valid = true
	return validation
}

func normalizeDevelopmentReason(reason string) string {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case WorkflowDevelopmentReasonEdit, "edit_existing":
		return WorkflowDevelopmentReasonEdit
	case WorkflowDevelopmentReasonVersionRevalidation, "revalidation", "repair":
		return WorkflowDevelopmentReasonVersionRevalidation
	default:
		return WorkflowDevelopmentReasonNew
	}
}

func normalizeDevelopmentYAMLForKey(value string) string {
	trimmed := strings.TrimRightFunc(value, unicode.IsSpace)
	if trimmed == "" {
		return ""
	}
	return trimmed + "\n"
}

func requireActiveDevelopment(workspace string) (*WorkflowDevelopmentSession, error) {
	session, err := getWorkflowDevelopmentSessionLocked(workspace)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, ErrNoActiveDevelopment
	}
	return session, nil
}

func writeNewActiveDevelopment(workspace string, session *WorkflowDevelopmentSession) error {
	path, err := checkedActiveDevelopmentPath(workspace)
	if err != nil {
		return err
	}
	if mkdirErr := fileutil.MkdirAllDurable(filepath.Dir(path), 0o755); mkdirErr != nil {
		return mkdirErr
	}
	if revisionErr := refreshWorkflowDevelopmentRevisions(session); revisionErr != nil {
		return revisionErr
	}
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return ErrActiveDevelopmentExists
		}
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
	}()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		closed = true
		_ = fileutil.RemoveDurable(path)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		closed = true
		_ = fileutil.RemoveDurable(path)
		return err
	}
	if err := file.Close(); err != nil {
		closed = true
		_ = fileutil.RemoveDurable(path)
		return err
	}
	closed = true
	return syncWorkflowRunDirectory(filepath.Dir(path))
}

func writeActiveDevelopment(workspace string, session *WorkflowDevelopmentSession) error {
	path, err := checkedActiveDevelopmentPath(workspace)
	if err != nil {
		return err
	}
	if mkdirErr := fileutil.MkdirAllDurable(filepath.Dir(path), 0o755); mkdirErr != nil {
		return mkdirErr
	}
	if revisionErr := refreshWorkflowDevelopmentRevisions(session); revisionErr != nil {
		return revisionErr
	}
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return err
	}
	return writeWorkflowTemplateAtomic(path, data, 0o600)
}

func archiveDevelopmentSession(workspace string, session *WorkflowDevelopmentSession, state string) error {
	archivePath, err := checkedWorkflowDevelopmentArchivePath(workspace, session.ID)
	if err != nil {
		return err
	}
	if mkdirErr := fileutil.MkdirAllDurable(filepath.Dir(archivePath), 0o755); mkdirErr != nil {
		return mkdirErr
	}
	data, err := marshalWorkflowDevelopmentArchive(session, state)
	if err != nil {
		return err
	}
	return writeWorkflowTemplateAtomic(archivePath, data, 0o600)
}

func activeDevelopmentPath(workspace string) string {
	return filepath.Join(workspace, workflowDevelopmentDir, workflowDevelopmentActive)
}

func checkedActiveDevelopmentPath(workspace string) (string, error) {
	return resolveWorkflowInternalPath(
		workspace,
		workflowDevelopmentDir,
		workflowDevelopmentActive,
	)
}

func checkedWorkflowDevelopmentArchivePath(
	workspace string,
	sessionID string,
) (string, error) {
	return resolveWorkflowInternalPath(
		workspace,
		workflowDevelopmentDir,
		"archive",
		safeID(sessionID)+".json",
	)
}

var slugTokenPattern = regexp.MustCompile(`[^a-z0-9]+`)

func slugFromText(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = slugTokenPattern.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	if value == "" {
		return ""
	}
	parts := strings.Split(value, "-")
	if len(parts) > 5 {
		parts = parts[:5]
	}
	return strings.Trim(path.Clean(strings.Join(parts, "-")), ".")
}

func titleFromPrompt(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "AI workflow"
	}
	fields := strings.Fields(prompt)
	if len(fields) > 8 {
		fields = fields[:8]
	}
	title := strings.Join(fields, " ")
	title = strings.Trim(title, " \t\r\n.,:;!?")
	if title == "" {
		return "AI workflow"
	}
	runes := []rune(title)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

func fallbackWorkflowDraftYAML(title, message string) string {
	return fmt.Sprintf(
		"name: %q\non:\n  manual: {}\njobs:\n  develop:\n    name: Run AI workflow\n    runs-on: picoclaw\n    steps:\n      - id: run_agent\n        name: Ask agent\n        uses: agent/main\n        with:\n          prompt: %q\n          history: none\n          cache: session\n",
		title,
		message,
	)
}

func fallbackRepositoryReviewDraftYAML(title, message string) string {
	return fmt.Sprintf(
		"name: %q\non:\n  manual: {}\njobs:\n  review:\n    name: Review repository\n    runs-on: picoclaw\n    steps:\n      - id: inventory\n        name: Inventory repository content\n        uses: function/git.inventory\n        with:\n          working_directory: .\n          commit: %q\n          target: all\n          include_content: true\n          max_content_bytes: 65536\n      - id: review\n        name: Review repository with managed scope split\n        uses: agent/main\n        with:\n          managed:\n            mode: auto\n            strategy: scope_split\n            max_items_per_chunk: 4\n            max_parallel_children: 3\n          session: key:workflow-repository-review\n          history: none\n          cache: session\n          prompt: %q\n          scope: ${{ steps.inventory.outputs.selectedFiles }}\n",
		title,
		repositoryReviewCommitFromPrompt(message),
		repositoryReviewPrompt(message),
	)
}
