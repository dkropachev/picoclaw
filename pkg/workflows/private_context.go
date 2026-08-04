package workflows

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/session"
)

const (
	WorkflowContextVisibilityPrivate = "private"

	MaxWorkflowPrivateRootValuesBytes = MaxWorkflowGateInputsBytes
	MaxWorkflowPrivateRootBytes       = 8 << 20
	maxPrivateSessionKeyBytes         = 4 << 10
	maxPrivateSessionRevisionBytes    = 256
	maxPrivateHistoryRevisionBytes    = 256
)

var (
	ErrPrivateWorkflowContext = errors.New("private workflow context is invalid")
	ErrPrivateWorkflowFailed  = errors.New("private workflow failed")
)

// frozenWorkflowRootContext is persisted manually by FileRunStore and omitted
// from every ordinary Run JSON projection. Revision authenticates the exact
// JSON-normalized values and captured read-only session used by execution.
type frozenWorkflowRootContext struct {
	Values          map[string]any         `json:"values,omitempty"`
	ReadOnlySession *FrozenReadOnlySession `json:"read_only_session,omitempty"`
	Revision        string                 `json:"revision"`
	RunBinding      string                 `json:"run_binding"`
}

type frozenWorkflowRootPayload struct {
	Values          map[string]any         `json:"values,omitempty"`
	ReadOnlySession *FrozenReadOnlySession `json:"read_only_session,omitempty"`
}

// persistedRunJSON bypasses Run.MarshalJSON only inside the trusted local
// store. Every ordinary JSON encoding of a private Run is redacted by default;
// the executor still retains its exact continuation state in run.json.
type persistedRunJSON Run

func (run Run) MarshalJSON() ([]byte, error) {
	if !IsPrivateWorkflowRun(&run) {
		return json.Marshal(persistedRunJSON(run))
	}
	projected := run
	projected.Origin = nil
	projected.ParentRunID = ""
	projected.ChildRunIDs = nil
	projected.CallerJobID = ""
	projected.Session = ""
	projected.Delivery = Delivery{}
	projected.Event = nil
	projected.Inputs = nil
	projected.Outputs = nil
	projected.Error = ""
	projected.CancelReason = ""
	projected.Jobs = cloneMaplessJobExecutions(run.Jobs)
	projected.Steps = cloneMaplessStepExecutions(run.Steps)
	projected.privateRoot = nil
	return json.Marshal(persistedRunJSON(projected))
}

func cloneMaplessJobExecutions(values map[string]JobExecution) map[string]JobExecution {
	if values == nil {
		return nil
	}
	out := make(map[string]JobExecution, len(values))
	for key, value := range values {
		value.Outputs = nil
		value.Error = ""
		out[key] = value
	}
	return out
}

func cloneMaplessStepExecutions(values map[string]StepExecution) map[string]StepExecution {
	if values == nil {
		return nil
	}
	out := make(map[string]StepExecution, len(values))
	for key, value := range values {
		value.Outputs = nil
		value.Error = ""
		out[key] = value
	}
	return out
}

func freezeWorkflowPrivateRoot(
	ctx context.Context,
	agents AgentRunner,
	request *PrivateRootRequest,
) (*frozenWorkflowRootContext, error) {
	if request == nil {
		return nil, nil
	}
	values := request.Values
	if values == nil {
		values = map[string]any{}
	}
	normalized, err := normalizeWorkflowGateValue(
		"private workflow root values",
		values,
		MaxWorkflowPrivateRootValuesBytes,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: values", ErrPrivateWorkflowContext)
	}
	normalizedValues, ok := normalized.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: values must be an object", ErrPrivateWorkflowContext)
	}
	encodedValues, err := marshalPrivateWorkflowJSON(normalizedValues)
	if err != nil || request.privateValuesRevision == "" ||
		workflowHashBytes(encodedValues) != request.privateValuesRevision {
		return nil, fmt.Errorf(
			"%w: compiler-private values changed before admission",
			ErrPrivateWorkflowContext,
		)
	}

	var frozenSession *FrozenReadOnlySession
	if request.ReadOnlySession != nil {
		ref, refErr := normalizePrivateReadOnlySessionRef(*request.ReadOnlySession)
		if refErr != nil {
			return nil, refErr
		}
		capturer, ok := agents.(ReadOnlySessionCapturer)
		if !ok || capturer == nil {
			return nil, fmt.Errorf(
				"%w: read-only session capture is unavailable",
				ErrPrivateWorkflowContext,
			)
		}
		captured, captureErr := capturer.CaptureReadOnlySession(ctx, ref)
		if captureErr != nil {
			return nil, fmt.Errorf(
				"%w: read-only session capture failed",
				ErrPrivateWorkflowContext,
			)
		}
		// Capturers are a capability boundary, not an authority to weaken the
		// caller's CAS fence. Check the store revision before validating or
		// persisting any captured content. Empty remains the documented legacy
		// unfenced mode.
		if ref.ExpectedRevision != "" &&
			(captured == nil || captured.Snapshot.Revision != ref.ExpectedRevision) {
			return nil, fmt.Errorf(
				"%w: read-only session changed before capture",
				ErrPrivateWorkflowContext,
			)
		}
		if validateErr := validateFrozenReadOnlySessionWithContext(
			ctx,
			captured,
			ref.AgentID,
		); validateErr != nil {
			return nil, validateErr
		}
		frozenSession = cloneFrozenReadOnlySession(captured)
		// The store CAS token and aliases are unnecessary after the exact
		// content snapshot has been captured. Keeping them would enlarge the
		// local capability surface without improving continuation safety.
		frozenSession.Snapshot.Revision = ""
		frozenSession.Snapshot.Aliases = nil
	}

	payload := frozenWorkflowRootPayload{
		Values:          normalizedValues,
		ReadOnlySession: frozenSession,
	}
	encoded, err := marshalPrivateWorkflowJSON(payload)
	if err != nil || len(encoded) > MaxWorkflowPrivateRootBytes {
		return nil, fmt.Errorf("%w: encoded root exceeds its bound", ErrPrivateWorkflowContext)
	}
	// Execute the same typed JSON snapshot that can be restored after a
	// process restart; caller-owned maps and non-persisted message metadata can
	// no longer change or distinguish the first segment from continuation.
	var detached frozenWorkflowRootPayload
	if err := decodeJSONWithNumbers(encoded, &detached); err != nil {
		return nil, fmt.Errorf("%w: root is not durable JSON", ErrPrivateWorkflowContext)
	}
	revision := privateWorkflowRootRevision(encoded)
	return &frozenWorkflowRootContext{
		Values:          detached.Values,
		ReadOnlySession: detached.ReadOnlySession,
		Revision:        revision,
	}, nil
}

func normalizePrivateReadOnlySessionRef(ref ReadOnlySessionRef) (ReadOnlySessionRef, error) {
	if ref.AgentID != strings.TrimSpace(ref.AgentID) ||
		!routing.IsCanonicalAgentID(ref.AgentID) {
		return ReadOnlySessionRef{}, fmt.Errorf(
			"%w: read-only session agent is invalid",
			ErrPrivateWorkflowContext,
		)
	}
	if ref.Session != strings.TrimSpace(ref.Session) || ref.Session == "" ||
		!utf8.ValidString(ref.Session) || len(ref.Session) > maxPrivateSessionKeyBytes {
		return ReadOnlySessionRef{}, fmt.Errorf(
			"%w: read-only session key is invalid",
			ErrPrivateWorkflowContext,
		)
	}
	if ref.ExpectedRevision != "" &&
		(ref.ExpectedRevision != strings.TrimSpace(ref.ExpectedRevision) ||
			!utf8.ValidString(ref.ExpectedRevision) ||
			len(ref.ExpectedRevision) > maxPrivateSessionRevisionBytes) {
		return ReadOnlySessionRef{}, fmt.Errorf(
			"%w: read-only session revision is invalid",
			ErrPrivateWorkflowContext,
		)
	}
	return ref, nil
}

func validateFrozenReadOnlySession(
	frozen *FrozenReadOnlySession,
	wantAgentID string,
) error {
	return validateFrozenReadOnlySessionWithContext(
		context.Background(),
		frozen,
		wantAgentID,
	)
}

func validateFrozenReadOnlySessionWithContext(
	ctx context.Context,
	frozen *FrozenReadOnlySession,
	wantAgentID string,
) error {
	if ctx == nil {
		return fmt.Errorf("%w: captured session validation context is invalid", ErrPrivateWorkflowContext)
	}
	if frozen == nil || frozen.AgentID != wantAgentID ||
		frozen.AgentID != strings.TrimSpace(frozen.AgentID) ||
		!routing.IsCanonicalAgentID(frozen.AgentID) {
		return fmt.Errorf("%w: captured session agent is invalid", ErrPrivateWorkflowContext)
	}
	key := frozen.Snapshot.Key
	if key != strings.TrimSpace(key) || key == "" || !utf8.ValidString(key) ||
		len(key) > maxPrivateSessionKeyBytes {
		return fmt.Errorf("%w: captured session key is invalid", ErrPrivateWorkflowContext)
	}
	owner := ""
	if frozen.Snapshot.Scope != nil {
		owner = frozen.Snapshot.Scope.AgentID
	} else if legacy := session.ParseLegacyAgentSessionKey(key); legacy != nil {
		owner = legacy.AgentID
	}
	if owner != frozen.AgentID || !routing.IsCanonicalAgentID(owner) {
		return fmt.Errorf("%w: captured session owner does not match", ErrPrivateWorkflowContext)
	}
	revision := frozen.HistoryRevision
	if revision != strings.TrimSpace(revision) || revision == "" ||
		!utf8.ValidString(revision) || len(revision) > maxPrivateHistoryRevisionBytes {
		return fmt.Errorf("%w: captured history revision is invalid", ErrPrivateWorkflowContext)
	}
	// Validate the exact snapshot-to-set closure and authoritative attachment
	// metadata before this capability can become durable. The materialized copy
	// is deliberately discarded: HistoryRevision continues to bind the captured
	// snapshot containing immutable frozen references.
	if _, err := session.MaterializeSessionSnapshotMedia(
		ctx,
		frozen.Snapshot,
		frozen.FrozenMedia,
	); err != nil {
		return fmt.Errorf("%w: captured session media is invalid", ErrPrivateWorkflowContext)
	}
	return nil
}

func cloneFrozenReadOnlySession(value *FrozenReadOnlySession) *FrozenReadOnlySession {
	if value == nil {
		return nil
	}
	out := *value
	out.Snapshot.History = session.CloneMessages(value.Snapshot.History)
	out.Snapshot.Scope = session.CloneScope(value.Snapshot.Scope)
	out.Snapshot.Aliases = append([]string(nil), value.Snapshot.Aliases...)
	out.FrozenMedia = value.FrozenMedia.Clone()
	return &out
}

func cloneFrozenWorkflowRootContext(
	root *frozenWorkflowRootContext,
) *frozenWorkflowRootContext {
	if root == nil {
		return nil
	}
	return &frozenWorkflowRootContext{
		Values:          cloneMap(root.Values),
		ReadOnlySession: cloneFrozenReadOnlySession(root.ReadOnlySession),
		Revision:        root.Revision,
		RunBinding:      root.RunBinding,
	}
}

func validateFrozenWorkflowRootContext(root *frozenWorkflowRootContext) error {
	if root == nil || root.Values == nil || root.Revision == "" {
		return ErrPrivateWorkflowContext
	}
	if root.ReadOnlySession != nil {
		if err := validateFrozenReadOnlySession(
			root.ReadOnlySession,
			root.ReadOnlySession.AgentID,
		); err != nil {
			return err
		}
	}
	payload := frozenWorkflowRootPayload{
		Values:          root.Values,
		ReadOnlySession: root.ReadOnlySession,
	}
	encoded, err := marshalPrivateWorkflowJSON(payload)
	if err != nil || len(encoded) > MaxWorkflowPrivateRootBytes ||
		privateWorkflowRootRevision(encoded) != root.Revision {
		return ErrPrivateWorkflowContext
	}
	return nil
}

func privateWorkflowRootRevision(encoded []byte) string {
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}

type privateWorkflowRunBindingPayload struct {
	Schema              string `json:"schema"`
	ID                  string `json:"id"`
	WorkflowRef         string `json:"workflow_ref"`
	RetryOfRunID        string `json:"retry_of_run_id,omitempty"`
	RootRevision        string `json:"root_revision"`
	FrozenMediaRevision string `json:"frozen_media_revision,omitempty"`
	WorkflowRevision    string `json:"workflow_revision"`
}

func bindPrivateWorkflowRun(run *Run) error {
	if run == nil || run.privateRoot == nil {
		return nil
	}
	binding, err := privateWorkflowRunBinding(run)
	if err != nil {
		return err
	}
	run.privateRoot.RunBinding = binding
	return nil
}

func privateWorkflowRunBinding(run *Run) (string, error) {
	if run == nil || run.privateRoot == nil || run.execution == nil ||
		run.execution.WorkflowRevision == "" {
		return "", ErrPrivateWorkflowContext
	}
	frozenMediaRevision, err := privateWorkflowFrozenMediaRevision(
		run.privateRoot.ReadOnlySession,
	)
	if err != nil {
		return "", ErrPrivateWorkflowContext
	}
	encoded, err := marshalPrivateWorkflowJSON(privateWorkflowRunBindingPayload{
		Schema:              "picoclaw.private-workflow-run-binding.v1",
		ID:                  run.ID,
		WorkflowRef:         run.WorkflowRef,
		RetryOfRunID:        run.RetryOfRunID,
		RootRevision:        run.privateRoot.Revision,
		FrozenMediaRevision: frozenMediaRevision,
		WorkflowRevision:    run.execution.WorkflowRevision,
	})
	if err != nil {
		return "", ErrPrivateWorkflowContext
	}
	return workflowHashBytes(encoded), nil
}

func privateWorkflowFrozenMediaRevision(
	frozen *FrozenReadOnlySession,
) (string, error) {
	if frozen == nil {
		return "", nil
	}
	encoded, err := marshalPrivateWorkflowJSON(frozen.FrozenMedia)
	if err != nil {
		return "", err
	}
	return workflowHashBytes(encoded), nil
}

func marshalPrivateWorkflowJSON(value any) (encoded []byte, err error) {
	defer func() {
		if recover() != nil {
			encoded = nil
			err = ErrPrivateWorkflowContext
		}
	}()
	return json.Marshal(value)
}

func cloneRunRequestForExecution(request RunRequest) (RunRequest, error) {
	out := request
	out.Inputs = cloneMap(request.Inputs)
	out.Secrets = cloneStringMap(request.Secrets)
	out.Event = cloneMap(request.Event)
	out.Origin = cloneRunOrigin(request.Origin)
	out.Delivery = cloneDelivery(request.Delivery)
	if request.PrivateRoot != nil {
		privateRoot := *request.PrivateRoot
		values := request.PrivateRoot.Values
		if values == nil {
			values = map[string]any{}
		}
		normalized, err := normalizeWorkflowGateValue(
			"private workflow root values",
			values,
			MaxWorkflowPrivateRootValuesBytes,
		)
		if err != nil {
			return RunRequest{}, ErrPrivateWorkflowContext
		}
		normalizedValues, ok := normalized.(map[string]any)
		if !ok {
			return RunRequest{}, ErrPrivateWorkflowContext
		}
		privateRoot.Values = normalizedValues
		if request.PrivateRoot.ReadOnlySession != nil {
			ref := *request.PrivateRoot.ReadOnlySession
			privateRoot.ReadOnlySession = &ref
		}
		out.PrivateRoot = &privateRoot
	}
	out.frozenPrivateRoot = cloneFrozenWorkflowRootContext(request.frozenPrivateRoot)
	return out, nil
}

func cloneDelivery(value Delivery) Delivery {
	value.ReplyHandles = cloneStringMap(value.ReplyHandles)
	return value
}

func validatePrivateWorkflowInvocationEnvelope(request RunRequest) error {
	initial := request.PrivateRoot != nil
	retry := request.frozenPrivateRoot != nil
	if !initial && !retry {
		return nil
	}
	if initial == retry {
		return ErrPrivateWorkflowContext
	}
	if initial && request.RetryOfRunID != "" {
		return fmt.Errorf(
			"%w: an initial private root cannot claim retry provenance",
			ErrPrivateWorkflowContext,
		)
	}
	if retry && (request.RetryOfRunID == "" ||
		request.RetryOfRunID != strings.TrimSpace(request.RetryOfRunID)) {
		return fmt.Errorf("%w: frozen root is retry-only", ErrPrivateWorkflowContext)
	}
	if len(request.Inputs) != 0 || len(request.Event) != 0 || len(request.Secrets) != 0 ||
		request.Origin != nil || strings.TrimSpace(request.Session) != "" ||
		!deliveryIsEmpty(request.Delivery) || request.ParentRunID != "" ||
		request.CallerJobID != "" || request.CallDepth != 0 {
		return fmt.Errorf(
			"%w: private gates cannot mix public invocation context",
			ErrPrivateWorkflowContext,
		)
	}
	return nil
}

func validatePrivateWorkflowAdmission(workflow *Workflow, request RunRequest) error {
	if err := validatePrivateWorkflowInvocationEnvelope(request); err != nil {
		return err
	}
	if request.PrivateRoot == nil && request.frozenPrivateRoot == nil {
		return nil
	}
	initial := request.PrivateRoot != nil
	if initial && (workflow == nil || workflow.privateRootRevision == "") {
		return ErrPrivateWorkflowContext
	}
	if workflow == nil || len(workflow.Jobs) != 1 ||
		len(workflow.On.Schedule) != 0 || workflow.On.ChannelMessage != nil ||
		workflow.On.Command != nil || workflow.On.RuntimeEvent != nil ||
		workflow.On.Event != nil || workflow.On.WorkflowCall != nil {
		return fmt.Errorf("%w: compiled gate shape changed", ErrPrivateWorkflowContext)
	}
	job, ok := workflow.Jobs[workflowGateJobID]
	if !ok || strings.TrimSpace(job.Uses) != "" || job.Secrets != nil ||
		len(job.Needs) != 0 || len(job.With) != 0 || len(job.Outputs) != 0 ||
		len(job.Steps) == 0 ||
		strings.TrimSpace(job.If) != "" || job.ContinueOnError ||
		strings.TrimSpace(job.Context.Session) != "" ||
		strings.TrimSpace(job.Context.Delivery) != "" {
		return fmt.Errorf("%w: compiled gate shape changed", ErrPrivateWorkflowContext)
	}
	for _, step := range job.Steps {
		uses := strings.TrimSpace(step.Uses)
		if step.ContinueOnError || strings.TrimSpace(step.Context.Session) != "" {
			return fmt.Errorf("%w: compiled gate target is unsafe", ErrPrivateWorkflowContext)
		}
		if uses == "human/task" {
			if strings.TrimSpace(step.Context.Delivery) != "" {
				return fmt.Errorf("%w: compiled gate target is unsafe", ErrPrivateWorkflowContext)
			}
			continue
		}
		if !strings.HasPrefix(uses, "agent/") ||
			!routing.IsCanonicalAgentID(strings.TrimPrefix(uses, "agent/")) ||
			strings.TrimSpace(stringFromMap(step.With, "tools")) != AgentToolsNone ||
			strings.TrimSpace(step.Context.Delivery) != "none" {
			return fmt.Errorf("%w: compiled gate target is unsafe", ErrPrivateWorkflowContext)
		}
		switch strings.TrimSpace(stringFromMap(step.With, "history")) {
		case "read_only":
			if strings.TrimSpace(stringFromMap(step.With, "session")) != "inherit" ||
				strings.TrimSpace(stringFromMap(step.With, "cache")) != "session" {
				return fmt.Errorf("%w: compiled gate target is unsafe", ErrPrivateWorkflowContext)
			}
		case "none":
			if strings.TrimSpace(stringFromMap(step.With, "session")) != AgentSessionEphemeral ||
				strings.TrimSpace(stringFromMap(step.With, "cache")) != "none" {
				return fmt.Errorf("%w: compiled gate target is unsafe", ErrPrivateWorkflowContext)
			}
		default:
			return fmt.Errorf("%w: compiled gate target is unsafe", ErrPrivateWorkflowContext)
		}
	}
	return nil
}

// captureInitialPrivateWorkflow turns the caller-owned compiled workflow into
// the one immutable JSON snapshot used by all later admission and execution.
// The compiler stamp is checked against exactly the bytes decoded here.
func captureInitialPrivateWorkflow(workflow *Workflow) (*Workflow, error) {
	if workflow == nil || workflow.privateRootRevision == "" {
		return nil, fmt.Errorf(
			"%w: only a compiled gate workflow accepts a private root",
			ErrPrivateWorkflowContext,
		)
	}
	if err := preflightPrivateWorkflowDynamicJSON(workflow); err != nil {
		return nil, ErrPrivateWorkflowContext
	}
	encoded, err := marshalPrivateWorkflowJSON(workflow)
	if err != nil || workflowHashBytes(encoded) != workflow.privateRootRevision {
		return nil, fmt.Errorf(
			"%w: compiled gate workflow changed before admission",
			ErrPrivateWorkflowContext,
		)
	}
	var snapshot Workflow
	if err := decodeJSONWithNumbers(encoded, &snapshot); err != nil {
		return nil, ErrPrivateWorkflowContext
	}
	snapshot.privateRootRevision = workflow.privateRootRevision
	return &snapshot, nil
}

// preflightPrivateWorkflowDynamicJSON rejects stateful JSON/Text marshalers
// without invoking them. Compiled workflows contain dynamic JSON only in the
// fields below; all other fields are closed workflow structs and scalars.
func preflightPrivateWorkflowDynamicJSON(workflow *Workflow) error {
	check := func(label string, value any) error {
		return preflightWorkflowGateJSON(label, value, MaxWorkflowPrivateRootBytes)
	}
	if err := check("private workflow manual trigger", workflow.On.Manual); err != nil {
		return err
	}
	if workflow.On.Command != nil {
		for name, input := range workflow.On.Command.Args {
			if err := check("private workflow command input "+name, input.Default); err != nil {
				return err
			}
		}
	}
	if workflow.On.WorkflowCall != nil {
		for name, input := range workflow.On.WorkflowCall.Inputs {
			if err := check("private workflow call input "+name, input.Default); err != nil {
				return err
			}
		}
	}
	for jobID, job := range workflow.Jobs {
		if err := check("private workflow job "+jobID+" with", job.With); err != nil {
			return err
		}
		if err := check("private workflow job "+jobID+" secrets", job.Secrets); err != nil {
			return err
		}
		for index, step := range job.Steps {
			if err := check(
				fmt.Sprintf("private workflow job %s step %d with", jobID, index),
				step.With,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func privateWorkflowReadOnlyAgentID(workflow *Workflow) (string, error) {
	if workflow == nil {
		return "", ErrPrivateWorkflowContext
	}
	agentID := ""
	for _, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if !strings.HasPrefix(strings.TrimSpace(step.Uses), "agent/") ||
				strings.TrimSpace(stringFromMap(step.With, "history")) != "read_only" {
				continue
			}
			candidate := strings.TrimPrefix(strings.TrimSpace(step.Uses), "agent/")
			if !routing.IsCanonicalAgentID(candidate) || (agentID != "" && agentID != candidate) {
				return "", fmt.Errorf(
					"%w: private read-only agent is inconsistent",
					ErrPrivateWorkflowContext,
				)
			}
			agentID = candidate
		}
	}
	return agentID, nil
}

func validatePrivateRootForWorkflow(
	workflow *Workflow,
	root *frozenWorkflowRootContext,
) error {
	if err := validateFrozenWorkflowRootContext(root); err != nil {
		return err
	}
	wantAgentID, err := privateWorkflowReadOnlyAgentID(workflow)
	if err != nil {
		return err
	}
	if wantAgentID == "" {
		if root.ReadOnlySession != nil {
			return fmt.Errorf(
				"%w: unused read-only session",
				ErrPrivateWorkflowContext,
			)
		}
		return nil
	}
	if root.ReadOnlySession == nil || root.ReadOnlySession.AgentID != wantAgentID {
		return fmt.Errorf(
			"%w: required read-only session is unavailable",
			ErrPrivateWorkflowContext,
		)
	}
	return nil
}

func deliveryIsEmpty(value Delivery) bool {
	return value.Channel == "" && value.ChatID == "" && value.TopicID == "" &&
		value.ThreadTS == "" && value.MessageID == "" &&
		value.ReplyToMessageID == "" && len(value.ReplyHandles) == 0
}

func IsPrivateWorkflowRun(run *Run) bool {
	return run != nil && (run.privateRoot != nil ||
		run.ContextVisibility == WorkflowContextVisibilityPrivate)
}

func validateRunPrivateContext(run *Run) error {
	if run == nil {
		return nil
	}
	if run.privateRoot == nil {
		if run.ContextVisibility == WorkflowContextVisibilityPrivate {
			return ErrPrivateWorkflowContext
		}
		return nil
	}
	if run.ContextVisibility != WorkflowContextVisibilityPrivate {
		return ErrPrivateWorkflowContext
	}
	if err := validatePrivateRunInvocationEnvelope(run); err != nil {
		return err
	}
	if err := validateFrozenWorkflowRootContext(run.privateRoot); err != nil {
		return err
	}
	binding, err := privateWorkflowRunBinding(run)
	if err != nil || run.privateRoot.RunBinding == "" ||
		run.privateRoot.RunBinding != binding {
		return ErrPrivateWorkflowContext
	}
	if run.execution == nil || run.execution.Workflow == nil ||
		validatePersistedWorkflowDefinition(run.execution) != nil {
		return ErrPrivateWorkflowContext
	}
	return validatePrivateRootForWorkflow(run.execution.Workflow, run.privateRoot)
}

func validatePrivateRunInvocationEnvelope(run *Run) error {
	if run == nil {
		return nil
	}
	if len(run.Inputs) != 0 || len(run.Event) != 0 || run.Origin != nil ||
		run.Session != "" || !deliveryIsEmpty(run.Delivery) ||
		run.ParentRunID != "" || run.CallerJobID != "" ||
		len(run.ChildRunIDs) != 0 ||
		run.RetryOfRunID != strings.TrimSpace(run.RetryOfRunID) {
		return ErrPrivateWorkflowContext
	}
	return nil
}

func preserveFrozenRunPrivateContext(existing, incoming *Run) error {
	if existing == nil || incoming == nil {
		return nil
	}
	if existing.privateRoot == nil {
		if incoming.privateRoot != nil ||
			incoming.ContextVisibility == WorkflowContextVisibilityPrivate {
			return ErrPrivateWorkflowContext
		}
		return nil
	}
	if err := validateRunPrivateContext(existing); err != nil {
		return err
	}
	if incoming.privateRoot == nil {
		return ErrPrivateWorkflowContext
	}
	if incoming.ID != existing.ID ||
		incoming.WorkflowRef != existing.WorkflowRef ||
		incoming.RetryOfRunID != existing.RetryOfRunID ||
		incoming.ContextVisibility != WorkflowContextVisibilityPrivate ||
		incoming.privateRoot.Revision != existing.privateRoot.Revision {
		return ErrPrivateWorkflowContext
	}
	return validateFrozenWorkflowRootContext(incoming.privateRoot)
}

func sanitizePrivateWorkflowEvent(event RunEvent) RunEvent {
	event.Message = ""
	event.Payload = nil
	return event
}

func sanitizePrivateRunResult(run *Run, result *RunResult) *RunResult {
	if result == nil || !IsPrivateWorkflowRun(run) {
		return result
	}
	projected := *result
	projected.Outputs = nil
	projected.Error = ""
	return &projected
}

func sanitizePrivateRunOutcome(
	run *Run,
	result *RunResult,
	err error,
) (*RunResult, error) {
	result = sanitizePrivateRunResult(run, result)
	if err == nil || !IsPrivateWorkflowRun(run) {
		return result, err
	}
	switch {
	case errors.Is(err, ErrHumanTaskConflict):
		return result, ErrHumanTaskConflict
	case errors.Is(err, ErrPrivateWorkflowContext):
		return result, ErrPrivateWorkflowContext
	case errors.Is(err, context.Canceled):
		return result, context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return result, context.DeadlineExceeded
	case errors.Is(err, ErrRunCanceled):
		return result, ErrRunCanceled
	case errors.Is(err, ErrRunAdmissionConflict):
		return result, ErrRunAdmissionConflict
	case errors.Is(err, ErrRunAdmissionUnavailable):
		return result, ErrRunAdmissionUnavailable
	default:
		return result, ErrPrivateWorkflowFailed
	}
}
