package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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
	ErrActiveDevelopmentExists          = errors.New("a workflow development session is already active")
	ErrNoActiveDevelopment              = errors.New("no active workflow development session")
	ErrDevelopmentBusy                  = errors.New("a workflow development operation is already in progress")
	ErrWorkflowDevelopmentFenceMismatch = errors.New(
		"workflow development test draft fence mismatch",
	)
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

// WorkflowDevelopmentTestDraftFence identifies one exact active draft
// snapshot. All fields are required and opaque to callers.
type WorkflowDevelopmentTestDraftFence struct {
	SessionID               string `json:"session_id"`
	ExpectedSessionRevision string `json:"expected_session_revision"`
	ExpectedDraftRevision   string `json:"expected_draft_revision"`
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
	return reviseWorkflowDevelopmentLocked(workspace, session, req, opts...)
}

// ReviseWorkflowDevelopmentFenced applies a revision only when the active
// development session still matches the exact tested draft snapshot. Fence
// comparison and revision share the workspace's process and advisory lock.
func ReviseWorkflowDevelopmentFenced(
	workspace string,
	fence WorkflowDevelopmentTestDraftFence,
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
	if !workflowDevelopmentTestDraftFenceMatches(session, fence) {
		return nil, ErrWorkflowDevelopmentFenceMismatch
	}
	return reviseWorkflowDevelopmentLocked(workspace, session, req, opts...)
}

func workflowDevelopmentTestDraftFenceMatches(
	session *WorkflowDevelopmentSession,
	fence WorkflowDevelopmentTestDraftFence,
) bool {
	return session != nil &&
		fence.SessionID != "" &&
		fence.SessionID == session.ID &&
		fence.ExpectedSessionRevision != "" &&
		fence.ExpectedSessionRevision == session.SessionRevision &&
		fence.ExpectedDraftRevision != "" &&
		fence.ExpectedDraftRevision == session.DraftRevision
}

func reviseWorkflowDevelopmentLocked(
	workspace string,
	session *WorkflowDevelopmentSession,
	req WorkflowDevelopmentReviseRequest,
	opts ...LocalOption,
) (*WorkflowDevelopmentSession, error) {
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

// RecordWorkflowDevelopmentTestIfCurrent applies a terminal result only while
// the exact session, draft, and running test identity still own LastTest.
func RecordWorkflowDevelopmentTestIfCurrent(
	workspace string,
	sessionID string,
	draftKey string,
	expectedRunID string,
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
	if !currentWorkflowDevelopmentTestMatchesRun(
		session,
		draftKey,
		"",
		expectedRunID,
		result,
	) {
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
	expectedRunID string,
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
	if !currentWorkflowDevelopmentTestMatchesRun(
		session,
		draftKey,
		trimmedEventID,
		expectedRunID,
		result,
	) {
		return session, false, nil
	}
	result, testErr = SanitizeEventBackedDraftTestOutcome(result, testErr)
	recordWorkflowDevelopmentTest(session, trimmedEventID, result, testErr)
	if err := writeActiveDevelopment(workspace, session); err != nil {
		return nil, false, err
	}
	return session, true, nil
}

func currentWorkflowDevelopmentTestMatchesRun(
	session *WorkflowDevelopmentSession,
	draftKey string,
	eventID string,
	expectedRunID string,
	result *RunResult,
) bool {
	normalizedRunID := strings.TrimSpace(expectedRunID)
	if session == nil ||
		session.LastTest == nil ||
		normalizedRunID == "" ||
		normalizedRunID != expectedRunID ||
		result == nil ||
		result.RunID != expectedRunID {
		return false
	}
	current := session.LastTest
	return current.DraftKey == draftKey &&
		current.EventID == eventID &&
		current.RunID == expectedRunID &&
		workflowDevelopmentTestIsActive(current.Status)
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
	if run == nil {
		return false
	}
	if origin, trusted := trustedRunOrigin(run); trusted &&
		origin.Kind == RunOriginExternalEventDraftTest {
		return true
	}
	if !strings.HasPrefix(strings.TrimSpace(run.WorkflowRef), "draft:") {
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
	if store != nil {
		if origin, trusted := trustedRunOriginWithStore(ctx, store, run); trusted {
			return origin.Kind == RunOriginExternalEventDraftTest
		}
	}
	var lookup runOriginLookup
	if store != nil {
		lookup = store.GetRun
	}
	return eventBackedDraftRunFamilyWithLookup(ctx, run, lookup)
}

// ProjectEventBackedDraftRunForBrowser masks diagnostic fields without
// changing status, identifiers, structured redacted event context, or outputs.
func ProjectEventBackedDraftRunForBrowser(run *Run) *Run {
	return ProjectWorkflowRunForBrowser(run, IsEventBackedDraftRun(run))
}

// ProjectWorkflowRunForBrowser applies an ancestry decision already resolved
// by the API or a batched run listing.
func ProjectWorkflowRunForBrowser(run *Run, eventBackedDraft bool) *Run {
	if IsPrivateWorkflowRun(run) {
		return projectPrivateWorkflowRunForBrowser(run)
	}
	origin, _ := trustedRunOrigin(run)
	return projectWorkflowRunForBrowser(run, eventBackedDraft, origin)
}

// ProjectWorkflowRunForBrowserWithStore additionally verifies inherited
// provenance against every available parent and retry ancestor before
// projecting authoritative event, dispatch, and root-run identifiers.
func ProjectWorkflowRunForBrowserWithStore(
	ctx context.Context,
	store RunStore,
	run *Run,
	eventBackedDraft bool,
) *Run {
	if IsPrivateWorkflowRun(run) {
		return projectPrivateWorkflowRunForBrowser(run)
	}
	origin, _ := trustedRunOriginWithStore(ctx, store, run)
	return projectWorkflowRunForBrowser(run, eventBackedDraft, origin)
}

func projectWorkflowRunForBrowser(
	run *Run,
	eventBackedDraft bool,
	origin *RunOrigin,
) *Run {
	if run == nil {
		return nil
	}
	if IsPrivateWorkflowRun(run) {
		return projectPrivateWorkflowRunForBrowser(run)
	}
	projected := cloneRun(run)
	projected.Origin = cloneRunOrigin(origin)
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

func projectPrivateWorkflowRunForBrowser(run *Run) *Run {
	if run == nil {
		return nil
	}
	projected := *run
	projected.Origin = nil
	projected.ParentRunID = ""
	projected.ChildRunIDs = nil
	projected.CallerJobID = ""
	projected.Session = ""
	projected.Delivery = Delivery{}
	projected.Event = nil
	projected.Inputs = nil
	projected.Outputs = nil
	projected.Jobs = cloneMaplessJobExecutions(run.Jobs)
	projected.Steps = cloneMaplessStepExecutions(run.Steps)
	projected.Error = ""
	projected.CancelReason = ""
	projected.execution = nil
	projected.humanTasks = nil
	projected.privateRoot = nil
	if run.CompletedAt != nil {
		completedAt := *run.CompletedAt
		projected.CompletedAt = &completedAt
	}
	if run.CancelRequestedAt != nil {
		cancelRequestedAt := *run.CancelRequestedAt
		projected.CancelRequestedAt = &cancelRequestedAt
	}
	return &projected
}

// ProjectEventBackedDraftRunsForBrowser applies the same projection to a run
// listing while leaving production and manual run diagnostics unchanged.
func ProjectEventBackedDraftRunsForBrowser(runs []Run) []Run {
	return projectEventBackedDraftRunsForBrowser(
		context.Background(),
		nil,
		runs,
	)
}

// ProjectEventBackedDraftRunsForBrowserWithStore applies the run-list
// projection while resolving retained provenance ancestry through the store.
// Only an actual not-found result is treated as an independent-retention
// boundary; an unreadable or corrupt retained ancestor suppresses origin.
func ProjectEventBackedDraftRunsForBrowserWithStore(
	ctx context.Context,
	store RunStore,
	runs []Run,
) []Run {
	return projectEventBackedDraftRunsForBrowser(ctx, store, runs)
}

func projectEventBackedDraftRunsForBrowser(
	ctx context.Context,
	store RunStore,
	runs []Run,
) []Run {
	byID := make(map[string]*Run, len(runs))
	for index := range runs {
		byID[runs[index].ID] = &runs[index]
	}
	projected := make([]Run, len(runs))
	type lookupResult struct {
		run *Run
		err error
	}
	lookups := make(map[string]lookupResult)
	lookup := func(ctx context.Context, runID string) (*Run, error) {
		run, exists := byID[runID]
		if exists && run != nil {
			return run, nil
		}
		if result, cached := lookups[runID]; cached {
			return result.run, result.err
		}
		if store != nil {
			run, err := store.GetRun(ctx, runID)
			lookups[runID] = lookupResult{run: run, err: err}
			return run, err
		}
		err := fmt.Errorf(
			"workflow run %q is unavailable: %w",
			runID,
			fs.ErrNotExist,
		)
		lookups[runID] = lookupResult{err: err}
		return nil, err
	}
	runPointers := make([]*Run, 0, len(runs))
	for index := range runs {
		if !IsPrivateWorkflowRun(&runs[index]) {
			runPointers = append(runPointers, &runs[index])
		}
	}
	origins := trustedRunOriginsWithLookup(ctx, runPointers, lookup)
	for index := range runs {
		if IsPrivateWorkflowRun(&runs[index]) {
			projected[index] = *projectPrivateWorkflowRunForBrowser(&runs[index])
			continue
		}
		origin := origins[runs[index].ID]
		masked := origin != nil &&
			origin.Kind == RunOriginExternalEventDraftTest
		if origin == nil {
			masked = eventBackedDraftRunFamilyWithLookup(
				ctx,
				&runs[index],
				lookup,
			)
		}
		projected[index] = *projectWorkflowRunForBrowser(
			&runs[index],
			masked,
			origin,
		)
	}
	return projected
}

func eventBackedDraftRunFamilyWithLookup(
	ctx context.Context,
	run *Run,
	lookup runOriginLookup,
) bool {
	if run == nil {
		return false
	}

	const (
		fallbackLineageVisiting = iota + 1
		fallbackLineageValidated
	)
	type lineageFrame struct {
		run         *Run
		ancestorIDs []string
		next        int
		depth       int
	}

	if ctx == nil {
		ctx = context.Background()
	}
	carriesEvent := false
	unsafeAncestry := false
	states := make(map[string]int, eventBackedDraftAncestryMaximumDepth)
	newFrame := func(current *Run, depth int) (lineageFrame, bool) {
		if current == nil {
			unsafeAncestry = true
			return lineageFrame{}, false
		}
		carriesEvent = carriesEvent || len(current.Event) != 0
		if IsEventBackedDraftRun(current) {
			return lineageFrame{}, true
		}
		if !validRunOriginRunID(current.ID) {
			unsafeAncestry = true
			return lineageFrame{}, false
		}
		ancestorIDs, valid := eventBackedDraftFallbackAncestorIDs(current)
		if !valid {
			unsafeAncestry = true
		}
		states[current.ID] = fallbackLineageVisiting
		return lineageFrame{
			run:         current,
			ancestorIDs: ancestorIDs,
			depth:       depth,
		}, false
	}

	frame, draft := newFrame(run, 0)
	if draft {
		return true
	}
	stack := make([]lineageFrame, 0, eventBackedDraftAncestryMaximumDepth)
	if frame.run != nil {
		stack = append(stack, frame)
	}
	for len(stack) != 0 {
		current := &stack[len(stack)-1]
		if current.next >= len(current.ancestorIDs) {
			states[current.run.ID] = fallbackLineageValidated
			stack = stack[:len(stack)-1]
			continue
		}

		ancestorID := current.ancestorIDs[current.next]
		current.next++
		if current.depth+1 >= eventBackedDraftAncestryMaximumDepth {
			unsafeAncestry = true
			continue
		}
		switch states[ancestorID] {
		case fallbackLineageVisiting:
			unsafeAncestry = true
			continue
		case fallbackLineageValidated:
			continue
		}
		if lookup == nil || ctx.Err() != nil {
			unsafeAncestry = true
			continue
		}
		ancestor, err := lookup(ctx, ancestorID)
		if err != nil ||
			ancestor == nil ||
			ancestor.ID != ancestorID {
			unsafeAncestry = true
			continue
		}
		ancestorFrame, ancestorDraft := newFrame(
			ancestor,
			current.depth+1,
		)
		if ancestorDraft {
			return true
		}
		if ancestorFrame.run != nil {
			stack = append(stack, ancestorFrame)
		}
	}
	return carriesEvent && unsafeAncestry
}

func eventBackedDraftFallbackAncestorIDs(run *Run) ([]string, bool) {
	if run == nil {
		return nil, false
	}
	ancestorIDs := make([]string, 0, 2)
	valid := true
	for _, runID := range []string{run.ParentRunID, run.RetryOfRunID} {
		if runID == "" {
			continue
		}
		if !validRunOriginRunID(runID) || runID == run.ID {
			valid = false
			continue
		}
		if len(ancestorIDs) == 0 || ancestorIDs[0] != runID {
			ancestorIDs = append(ancestorIDs, runID)
		}
	}
	return ancestorIDs, valid
}

// ProjectEventBackedDraftEventsForBrowser masks both free-form lifecycle
// messages and payloads. Kind, time, run/job/step IDs remain visible.
func ProjectEventBackedDraftEventsForBrowser(
	run *Run,
	events []RunEvent,
) []RunEvent {
	return ProjectWorkflowRunEventsForBrowser(
		events,
		IsEventBackedDraftRun(run),
		IsPrivateWorkflowRun(run),
	)
}

// ProjectWorkflowRunEventsForBrowser applies a resolved ancestry decision to
// lifecycle events.
func ProjectWorkflowRunEventsForBrowser(
	events []RunEvent,
	eventBackedDraft bool,
	privateRun ...bool,
) []RunEvent {
	private := len(privateRun) != 0 && privateRun[0]
	projected := make([]RunEvent, len(events))
	for index, event := range events {
		if private {
			event.Message = ""
			event.Payload = nil
		} else {
			event.Payload = cloneMap(event.Payload)
		}
		if !private && eventBackedDraft {
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
	case RunStatusRunning, RunStatusWaiting:
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
	if !workflowDevelopmentTestIsActive(session.LastTest.Status) {
		return nil
	}
	if !workflowDevelopmentTestMatchesDraft(session) {
		return nil
	}
	return ErrDevelopmentBusy
}

func workflowDevelopmentTestIsActive(status string) bool {
	return status == RunStatusRunning || status == RunStatusWaiting
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
