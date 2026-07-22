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
)

type WorkflowDevelopmentSession struct {
	ID                    string                         `json:"id"`
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
	TargetWorkflowRef string    `json:"target_workflow_ref"`
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
	data, err := os.ReadFile(activeDevelopmentPath(workspace))
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
) (*WorkflowDevelopmentSession, error) {
	session, err := requireActiveDevelopment(workspace)
	if err != nil {
		return nil, err
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
		targetRef, err := CanonicalLocalRef(req.TargetRef)
		if err != nil {
			return nil, err
		}
		session.TargetWorkflowRef = targetRef
	}
	if req.Regenerate {
		session.YAML = GenerateWorkflowDraftYAML(session.Prompt)
	} else if req.YAML != nil {
		session.YAML = strings.TrimRight(*req.YAML, " \t\r\n") + "\n"
	}
	draftChanged := session.TargetWorkflowRef != previousTargetRef ||
		normalizeDevelopmentYAMLForKey(session.YAML) != normalizeDevelopmentYAMLForKey(previousYAML)
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

func ValidateWorkflowDevelopment(workspace string) (*WorkflowDevelopmentSession, error) {
	session, err := requireActiveDevelopment(workspace)
	if err != nil {
		return nil, err
	}
	if err := ensureNoCurrentRunningDevelopmentTest(session); err != nil {
		return nil, err
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
	session, err := ValidateWorkflowDevelopment(workspace)
	if err != nil {
		return nil, err
	}
	if session.Validation == nil || !session.Validation.Valid {
		return nil, fmt.Errorf("workflow draft is not valid")
	}
	if testErr := requireCurrentSuccessfulDevelopmentTest(session); testErr != nil {
		return nil, testErr
	}
	local := collectLocalOptions(opts...)
	resolved, err := local.resolver(workspace).ResolveLocal(session.TargetWorkflowRef)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(resolved.Path), 0o755); err != nil {
		return nil, err
	}
	tmp := resolved.Path + ".tmp"
	if err := os.WriteFile(tmp, []byte(session.YAML), 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, resolved.Path); err != nil {
		return nil, err
	}
	if _, err := RevalidateLocal(ctx, workspace, runtime, opts...); err != nil {
		return nil, err
	}
	if err := archiveDevelopmentSession(workspace, session, "published"); err != nil {
		return nil, err
	}
	if err := os.Remove(activeDevelopmentPath(workspace)); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return &WorkflowDevelopmentPublishResult{WorkflowRef: session.TargetWorkflowRef, Session: session}, nil
}

func RecordWorkflowDevelopmentTest(
	workspace string,
	result *RunResult,
	testErr error,
) (*WorkflowDevelopmentSession, error) {
	session, err := requireActiveDevelopment(workspace)
	if err != nil {
		return nil, err
	}
	recordWorkflowDevelopmentTest(session, result, testErr)
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
	session, err := requireActiveDevelopment(workspace)
	if err != nil {
		return nil, false, err
	}
	if session.ID != sessionID ||
		WorkflowDevelopmentDraftKey(session.TargetWorkflowRef, session.YAML) != draftKey {
		return session, false, nil
	}
	recordWorkflowDevelopmentTest(session, result, testErr)
	if err := writeActiveDevelopment(workspace, session); err != nil {
		return nil, false, err
	}
	return session, true, nil
}

func recordWorkflowDevelopmentTest(
	session *WorkflowDevelopmentSession,
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
		TargetWorkflowRef: session.TargetWorkflowRef,
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
	if session.LastTest.DraftKey != WorkflowDevelopmentDraftKey(session.TargetWorkflowRef, session.YAML) {
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
	if session.LastTest.DraftKey != WorkflowDevelopmentDraftKey(session.TargetWorkflowRef, session.YAML) {
		return nil
	}
	return ErrDevelopmentBusy
}

func hasCurrentSuccessfulDevelopmentTest(session *WorkflowDevelopmentSession) bool {
	return session != nil &&
		session.LastTest != nil &&
		session.LastTest.Status == RunStatusSucceeded &&
		session.LastTest.DraftKey == WorkflowDevelopmentDraftKey(session.TargetWorkflowRef, session.YAML)
}

func DiscardWorkflowDevelopment(workspace string) (*WorkflowDevelopmentSession, error) {
	session, err := requireActiveDevelopment(workspace)
	if err != nil {
		return nil, err
	}
	if err := archiveDevelopmentSession(workspace, session, "discarded"); err != nil {
		return nil, err
	}
	if err := os.Remove(activeDevelopmentPath(workspace)); err != nil && !os.IsNotExist(err) {
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
	session, err := GetWorkflowDevelopmentSession(workspace)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, ErrNoActiveDevelopment
	}
	return session, nil
}

func writeNewActiveDevelopment(workspace string, session *WorkflowDevelopmentSession) error {
	if err := os.MkdirAll(filepath.Dir(activeDevelopmentPath(workspace)), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(activeDevelopmentPath(workspace), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return ErrActiveDevelopmentExists
		}
		return err
	}
	defer file.Close()
	_, err = file.Write(data)
	return err
}

func writeActiveDevelopment(workspace string, session *WorkflowDevelopmentSession) error {
	if err := os.MkdirAll(filepath.Dir(activeDevelopmentPath(workspace)), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return err
	}
	tmp := activeDevelopmentPath(workspace) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, activeDevelopmentPath(workspace))
}

func archiveDevelopmentSession(workspace string, session *WorkflowDevelopmentSession, state string) error {
	archiveDir := filepath.Join(workspace, workflowDevelopmentDir, "archive")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		return err
	}
	copySession := *session
	copySession.Status = strings.TrimSpace(state)
	copySession.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(copySession, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(archiveDir, safeID(session.ID)+".json"), data, 0o600)
}

func activeDevelopmentPath(workspace string) string {
	return filepath.Join(workspace, workflowDevelopmentDir, workflowDevelopmentActive)
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
