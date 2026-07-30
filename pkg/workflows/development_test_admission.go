package workflows

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrWorkflowDevelopmentTestAdmissionInvalid = errors.New(
	"workflow development test run admission is invalid",
)

// WorkflowDevelopmentTestRunAdmission binds one reviewed execution to the
// active session revision and carries the exact candidate that will become the
// tested draft. RunID is required. EventID is optional.
type WorkflowDevelopmentTestRunAdmission struct {
	SessionID               string `json:"session_id"`
	ExpectedSessionRevision string `json:"expected_session_revision"`
	ExpectedDraftRevision   string `json:"expected_draft_revision"`
	Prompt                  string `json:"prompt,omitempty"`
	TargetWorkflowRef       string `json:"target_workflow_ref"`
	YAML                    string `json:"yaml"`
	EventID                 string `json:"event_id,omitempty"`
	RunID                   string `json:"run_id"`
}

// AdmitWorkflowDevelopmentTestRun atomically prepares and validates a
// candidate draft, starts its durable run, and claims that run as the active
// draft test. The start callback runs while the workflow mutation lock is held
// and must return only after durable run creation.
//
// A fence, validation, busy-state, or start failure leaves the active session
// untouched. If the run starts but the final active-session write fails,
// started and a detached copy of the still-persisted original session are
// returned with recorded=false so the caller can release the run and report
// degraded reconciliation without presenting an unpersisted candidate.
func AdmitWorkflowDevelopmentTestRun[T any](
	workspace string,
	admission WorkflowDevelopmentTestRunAdmission,
	start func() (T, error),
	opts ...LocalOption,
) (
	session *WorkflowDevelopmentSession,
	recorded bool,
	started T,
	err error,
) {
	var zero T
	if start == nil {
		return nil, false, zero, fmt.Errorf(
			"%w: start callback is required",
			ErrWorkflowDevelopmentTestAdmissionInvalid,
		)
	}
	if !workflowDevelopmentTestRunAdmissionIdentityValid(admission) {
		return nil, false, zero, ErrWorkflowDevelopmentTestAdmissionInvalid
	}

	unlock, lockErr := lockWorkflowMutation(workspace)
	if lockErr != nil {
		return nil, false, zero, lockErr
	}
	defer unlock()

	current, currentErr := requireActiveDevelopment(workspace)
	if currentErr != nil {
		return nil, false, zero, currentErr
	}
	fence := WorkflowDevelopmentTestDraftFence{
		SessionID:               admission.SessionID,
		ExpectedSessionRevision: admission.ExpectedSessionRevision,
		ExpectedDraftRevision:   admission.ExpectedDraftRevision,
	}
	if !workflowDevelopmentTestDraftFenceMatches(current, fence) {
		return cloneWorkflowDevelopmentSession(current),
			false,
			zero,
			ErrWorkflowDevelopmentFenceMismatch
	}
	if current.LastTest != nil &&
		current.LastTest.Status == RunStatusRunning {
		return cloneWorkflowDevelopmentSession(current),
			false,
			zero,
			ErrDevelopmentBusy
	}

	canonicalTarget, canonicalErr := CanonicalLocalRef(
		admission.TargetWorkflowRef,
	)
	if canonicalErr != nil ||
		canonicalTarget != admission.TargetWorkflowRef {
		if canonicalErr == nil {
			canonicalErr = fmt.Errorf("workflow ref must be canonical")
		}
		return cloneWorkflowDevelopmentSession(current), false, zero, fmt.Errorf(
			"%w: %v",
			ErrWorkflowDevelopmentTestAdmissionInvalid,
			canonicalErr,
		)
	}

	candidate := *current
	previousTargetRef := candidate.TargetWorkflowRef
	previousYAML := candidate.YAML
	if prompt := strings.TrimSpace(admission.Prompt); prompt != "" {
		candidate.Prompt = prompt
	}
	candidate.TargetWorkflowRef = canonicalTarget
	candidate.YAML = admission.YAML
	if candidate.TargetWorkflowRef != previousTargetRef ||
		candidate.BaseTargetRevision == "" ||
		candidate.BaseTargetRevision == WorkflowTargetRevisionUnknown {
		baseTargetRevision, revisionErr := captureWorkflowDevelopmentTargetRevision(
			workspace,
			candidate.TargetWorkflowRef,
			opts...,
		)
		if revisionErr != nil {
			return cloneWorkflowDevelopmentSession(current),
				false,
				zero,
				revisionErr
		}
		candidate.BaseTargetRevision = baseTargetRevision
	}
	if candidate.TargetWorkflowRef != previousTargetRef ||
		candidate.YAML != previousYAML {
		candidate.LastTest = nil
	}
	candidate.DraftRevision = WorkflowDevelopmentDraftRevision(
		candidate.TargetWorkflowRef,
		candidate.YAML,
	)
	candidate.Validation = validateDevelopmentYAML(candidate.YAML)
	candidate.Status = WorkflowDevelopmentStatusEditing
	candidate.UpdatedAt = time.Now().UTC()
	if candidate.Validation == nil || !candidate.Validation.Valid {
		return &candidate, false, zero, ErrWorkflowDevelopmentDraftNotReady
	}

	started, startErr := start()
	if startErr != nil {
		return cloneWorkflowDevelopmentSession(current),
			false,
			started,
			startErr
	}

	runningResult := &RunResult{
		RunID:  admission.RunID,
		Status: RunStatusRunning,
	}
	eventID := strings.TrimSpace(admission.EventID)
	if eventID != "" {
		runningResult, _ = SanitizeEventBackedDraftTestOutcome(
			runningResult,
			nil,
		)
	}
	recordWorkflowDevelopmentTest(
		&candidate,
		eventID,
		runningResult,
		nil,
	)
	if writeErr := writeActiveDevelopment(workspace, &candidate); writeErr != nil {
		return cloneWorkflowDevelopmentSession(current), false, started, writeErr
	}
	return &candidate, true, started, nil
}

func workflowDevelopmentTestRunAdmissionIdentityValid(
	admission WorkflowDevelopmentTestRunAdmission,
) bool {
	return admission.SessionID != "" &&
		admission.ExpectedSessionRevision != "" &&
		admission.ExpectedDraftRevision != "" &&
		admission.TargetWorkflowRef != "" &&
		admission.RunID != "" &&
		strings.TrimSpace(admission.RunID) == admission.RunID &&
		(admission.EventID == "" ||
			strings.TrimSpace(admission.EventID) == admission.EventID)
}

func cloneWorkflowDevelopmentSession(
	session *WorkflowDevelopmentSession,
) *WorkflowDevelopmentSession {
	if session == nil {
		return nil
	}
	cloned := *session
	if session.Validation != nil {
		validation := *session.Validation
		validation.Errors = append(
			[]WorkflowValidationIssue(nil),
			session.Validation.Errors...,
		)
		validation.Warnings = append(
			[]WorkflowValidationIssue(nil),
			session.Validation.Warnings...,
		)
		cloned.Validation = &validation
	}
	if session.LastTest != nil {
		lastTest := *session.LastTest
		cloned.LastTest = &lastTest
	}
	return &cloned
}
