package workflows

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
)

var (
	// ErrWorkflowTriggerSimulationInvalidInput identifies a malformed selector,
	// workflow identity, or source outside the fixed inspection bounds.
	ErrWorkflowTriggerSimulationInvalidInput = errors.New("workflow trigger simulation input is invalid")
	// ErrWorkflowTriggerSimulationScenario identifies a scenario union that is
	// absent, ambiguous, or incompatible with the selected trigger family.
	ErrWorkflowTriggerSimulationScenario = errors.New("workflow trigger simulation scenario is invalid")
)

// WorkflowTriggerSelector selects one exact trigger family. ScheduleIndex is
// required only for schedule and forbidden for every other family.
type WorkflowTriggerSelector struct {
	Kind          WorkflowTriggerKind
	ScheduleIndex *int
}

// WorkflowTriggerInvocation supplies caller-controlled execution context for
// manual and workflow_call simulation. It must never be serialized by a
// review surface because it can contain input and secret values.
type WorkflowTriggerInvocation struct {
	Inputs   map[string]any    `json:"-"`
	Secrets  map[string]string `json:"-"`
	Session  string            `json:"-"`
	Delivery Delivery          `json:"-"`
}

// WorkflowTriggerSimulationScenario is a strict type-bound union:
// manual/workflow_call use Invocation, schedule uses ScheduledAt,
// channel_message/command use Message, runtime_event uses RuntimeEvent, and
// event uses an already-authorized durable Event envelope.
type WorkflowTriggerSimulationScenario struct {
	Invocation   *WorkflowTriggerInvocation `json:"-"`
	ScheduledAt  *time.Time                 `json:"-"`
	Message      *ChannelMessageEvent       `json:"-"`
	RuntimeEvent *runtimeevents.Event       `json:"-"`
	Event        *eventing.Envelope         `json:"-"`
}

// WorkflowTriggerSimulationInput is the complete pure simulation input. YAML
// and Scenario are explicitly non-serializing because they may contain
// executable values or secrets.
type WorkflowTriggerSimulationInput struct {
	YAML        string                            `json:"-"`
	WorkflowRef string                            `json:"-"`
	Trigger     WorkflowTriggerSelector           `json:"-"`
	Scenario    WorkflowTriggerSimulationScenario `json:"-"`
}

// WorkflowTriggerSimulationReason is a fixed, non-sensitive public result.
type WorkflowTriggerSimulationReason string

const (
	WorkflowTriggerSimulationMatched                   WorkflowTriggerSimulationReason = "matched"
	WorkflowTriggerSimulationInvalidWorkflow           WorkflowTriggerSimulationReason = "invalid_workflow"
	WorkflowTriggerSimulationTriggerAbsent             WorkflowTriggerSimulationReason = "trigger_absent"
	WorkflowTriggerSimulationScheduleIndexRequired     WorkflowTriggerSimulationReason = "schedule_index_required"
	WorkflowTriggerSimulationScheduleIndexOutOfRange   WorkflowTriggerSimulationReason = "schedule_index_out_of_range"
	WorkflowTriggerSimulationInvalidScenario           WorkflowTriggerSimulationReason = "invalid_scenario"
	WorkflowTriggerSimulationNotMatched                WorkflowTriggerSimulationReason = "not_matched"
	WorkflowTriggerSimulationShadowedByCommand         WorkflowTriggerSimulationReason = "shadowed_by_command"
	WorkflowTriggerSimulationRuntimeFeedbackSuppressed WorkflowTriggerSimulationReason = "runtime_feedback_suppressed"
	WorkflowTriggerSimulationEvaluationFailed          WorkflowTriggerSimulationReason = "trigger_evaluation_failed"
	WorkflowTriggerSimulationReviewIncomplete          WorkflowTriggerSimulationReason = "review_incomplete"
)

// WorkflowTriggerRunContextSummary proves which context categories were
// derived without exposing their values.
type WorkflowTriggerRunContextSummary struct {
	InputCount  int  `json:"input_count"`
	SecretCount int  `json:"secret_count"`
	HasEvent    bool `json:"has_event"`
	HasSession  bool `json:"has_session"`
	HasDelivery bool `json:"has_delivery"`
}

// WorkflowTriggerSimulationStatus is the safe matcher result. Reason is always
// present and is "matched" for an executable successful simulation.
type WorkflowTriggerSimulationStatus struct {
	SelectedKind   WorkflowTriggerKind              `json:"selected_kind"`
	EffectiveKind  WorkflowTriggerKind              `json:"effective_kind,omitempty"`
	ScheduleIndex  *int                             `json:"schedule_index,omitempty"`
	Present        bool                             `json:"present"`
	Matched        bool                             `json:"matched"`
	Executable     bool                             `json:"executable"`
	Reason         WorkflowTriggerSimulationReason  `json:"reason"`
	Passthrough    *bool                            `json:"passthrough,omitempty"`
	ContextSummary WorkflowTriggerRunContextSummary `json:"context_summary"`
}

// WorkflowTriggerEffectReview is a bounded review built by the existing safe
// workflow-definition inspector. It excludes prompts, arbitrary arguments,
// input values, secret values, conditions, paths, and parser messages.
type WorkflowTriggerEffectReview struct {
	JobCount   int                                     `json:"job_count"`
	StepCount  int                                     `json:"step_count"`
	Targets    []string                                `json:"targets"`
	Effects    []WorkflowDefinitionEffectInspection    `json:"effects"`
	Complete   bool                                    `json:"complete"`
	Validation WorkflowDefinitionInspectionValidation  `json:"validation"`
	Limits     []WorkflowDefinitionInspectionLimitCode `json:"limits"`
}

// WorkflowTriggerSimulation is safe to serialize: only Simulation and Review
// are exported. The executable request and exact YAML remain private.
type WorkflowTriggerSimulation struct {
	Simulation WorkflowTriggerSimulationStatus `json:"simulation"`
	Review     WorkflowTriggerEffectReview     `json:"review"`

	runRequest   RunRequest
	workflowYAML string
}

// RunRequest returns a detached executable context only after a complete,
// valid, matched review. Re-parsing the retained exact YAML gives each caller
// an independently owned workflow snapshot.
func (simulation WorkflowTriggerSimulation) RunRequest() (RunRequest, bool) {
	if !simulation.Simulation.Executable {
		return RunRequest{}, false
	}
	workflow, err := Parse([]byte(simulation.workflowYAML))
	if err != nil || Validate(workflow) != nil {
		return RunRequest{}, false
	}
	request := simulation.runRequest
	request.Inputs = cloneWorkflowTriggerMap(request.Inputs)
	request.Secrets = cloneWorkflowTriggerStringMap(request.Secrets)
	request.Event = cloneWorkflowTriggerMap(request.Event)
	request.Origin = cloneRunOrigin(request.Origin)
	request.Delivery = cloneWorkflowTriggerDelivery(request.Delivery)
	request.Workflow = workflow
	return request, true
}

// SimulateWorkflowTrigger evaluates one exact trigger and derives the same
// context helpers used by production. It performs no config, filesystem,
// runtime, session, run-store, or network mutation.
func SimulateWorkflowTrigger(
	input WorkflowTriggerSimulationInput,
) (WorkflowTriggerSimulation, error) {
	if err := validateWorkflowTriggerSimulationInput(input); err != nil {
		return WorkflowTriggerSimulation{}, err
	}
	inspection, err := InspectWorkflowDefinitionBytes(
		WorkflowDefinitionInspectionSource{
			Kind: WorkflowDefinitionInspectionSourcePublished,
			Ref:  input.WorkflowRef,
		},
		[]byte(input.YAML),
	)
	if err != nil {
		return WorkflowTriggerSimulation{}, fmt.Errorf(
			"%w: %v",
			ErrWorkflowTriggerSimulationInvalidInput,
			err,
		)
	}
	simulation := WorkflowTriggerSimulation{
		Simulation: WorkflowTriggerSimulationStatus{
			SelectedKind:  input.Trigger.Kind,
			ScheduleIndex: cloneWorkflowTriggerIndex(input.Trigger.ScheduleIndex),
			Reason:        WorkflowTriggerSimulationNotMatched,
		},
		Review:       workflowTriggerEffectReview(inspection),
		workflowYAML: input.YAML,
	}
	projected := inspection.Triggers[input.Trigger.Kind]
	simulation.Simulation.Present = projected.Present
	if !simulation.Simulation.Present {
		simulation.Simulation.Reason = WorkflowTriggerSimulationTriggerAbsent
		return simulation, nil
	}
	if !inspection.Validation.Valid {
		simulation.Simulation.Reason = WorkflowTriggerSimulationInvalidWorkflow
		return simulation, nil
	}
	workflow, parseErr := Parse([]byte(input.YAML))
	if parseErr != nil || Validate(workflow) != nil {
		simulation.Simulation.Reason = WorkflowTriggerSimulationInvalidWorkflow
		return simulation, nil
	}
	if input.Trigger.Kind == WorkflowTriggerSchedule {
		index := *input.Trigger.ScheduleIndex
		if index < 0 || index >= len(workflow.On.Schedule) {
			simulation.Simulation.Present = false
			simulation.Simulation.Reason = WorkflowTriggerSimulationScheduleIndexOutOfRange
			return simulation, nil
		}
	}

	request, effectiveKind, passthrough, matched, reason := simulateWorkflowTriggerContext(
		workflow,
		input,
	)
	simulation.Simulation.EffectiveKind = effectiveKind
	simulation.Simulation.Matched = matched
	simulation.Simulation.Reason = reason
	simulation.Simulation.Passthrough = passthrough
	if !matched {
		return simulation, nil
	}
	resolvedInputs, resolveErr := ResolveWorkflowCallInvocation(
		workflow.On.WorkflowCall,
		request.Inputs,
		request.Secrets,
	)
	if resolveErr != nil {
		simulation.Simulation.Matched = false
		simulation.Simulation.Reason = WorkflowTriggerSimulationInvalidScenario
		simulation.Simulation.Passthrough = nil
		return simulation, nil
	}
	request.Inputs = resolvedInputs
	request.Ref = input.WorkflowRef
	request.WorkflowRef = input.WorkflowRef
	request.Workflow = nil
	simulation.runRequest = cloneWorkflowTriggerRunRequest(request)
	simulation.Simulation.ContextSummary = workflowTriggerRunContextSummary(request)
	if !inspection.Complete {
		simulation.Simulation.Reason = WorkflowTriggerSimulationReviewIncomplete
		return simulation, nil
	}
	simulation.Simulation.Executable = true
	simulation.Simulation.Reason = WorkflowTriggerSimulationMatched
	return simulation, nil
}

func validateWorkflowTriggerSimulationInput(
	input WorkflowTriggerSimulationInput,
) error {
	if !input.Trigger.Kind.Valid() {
		return ErrWorkflowTriggerSimulationInvalidInput
	}
	canonical, err := CanonicalLocalRef(input.WorkflowRef)
	if err != nil || canonical != input.WorkflowRef {
		return ErrWorkflowTriggerSimulationInvalidInput
	}
	if input.Trigger.Kind == WorkflowTriggerSchedule {
		if input.Trigger.ScheduleIndex == nil {
			return fmt.Errorf(
				"%w: %s",
				ErrWorkflowTriggerSimulationInvalidInput,
				WorkflowTriggerSimulationScheduleIndexRequired,
			)
		}
	} else if input.Trigger.ScheduleIndex != nil {
		return ErrWorkflowTriggerSimulationInvalidInput
	}
	count := 0
	if input.Scenario.Invocation != nil {
		count++
	}
	if input.Scenario.ScheduledAt != nil {
		count++
	}
	if input.Scenario.Message != nil {
		count++
	}
	if input.Scenario.RuntimeEvent != nil {
		count++
	}
	if input.Scenario.Event != nil {
		count++
	}
	if count != 1 {
		return ErrWorkflowTriggerSimulationScenario
	}
	validScenario := false
	switch input.Trigger.Kind {
	case WorkflowTriggerManual, WorkflowTriggerWorkflowCall:
		validScenario = input.Scenario.Invocation != nil
	case WorkflowTriggerSchedule:
		validScenario = input.Scenario.ScheduledAt != nil &&
			!input.Scenario.ScheduledAt.IsZero()
	case WorkflowTriggerChannelMessage, WorkflowTriggerCommand:
		validScenario = input.Scenario.Message != nil
	case WorkflowTriggerRuntimeEvent:
		validScenario = input.Scenario.RuntimeEvent != nil
	case WorkflowTriggerEvent:
		validScenario = input.Scenario.Event != nil
	}
	if !validScenario {
		return ErrWorkflowTriggerSimulationScenario
	}
	return nil
}

func simulateWorkflowTriggerContext(
	workflow *Workflow,
	input WorkflowTriggerSimulationInput,
) (
	RunRequest,
	WorkflowTriggerKind,
	*bool,
	bool,
	WorkflowTriggerSimulationReason,
) {
	switch input.Trigger.Kind {
	case WorkflowTriggerManual, WorkflowTriggerWorkflowCall:
		invocation := input.Scenario.Invocation
		return RunRequest{
				Inputs:   cloneWorkflowTriggerMap(invocation.Inputs),
				Secrets:  cloneWorkflowTriggerStringMap(invocation.Secrets),
				Session:  strings.TrimSpace(invocation.Session),
				Delivery: cloneWorkflowTriggerDelivery(invocation.Delivery),
			},
			input.Trigger.Kind,
			nil,
			true,
			WorkflowTriggerSimulationMatched
	case WorkflowTriggerSchedule:
		context, err := BuildWorkflowScheduleRunContext(
			workflow,
			input.WorkflowRef,
			*input.Trigger.ScheduleIndex,
			*input.Scenario.ScheduledAt,
		)
		if err != nil {
			return RunRequest{}, "", nil, false, WorkflowTriggerSimulationInvalidScenario
		}
		return RunRequest{
				Inputs:   context.Inputs,
				Event:    context.Event,
				Session:  context.Session,
				Delivery: context.Delivery,
			},
			WorkflowTriggerSchedule,
			nil,
			true,
			WorkflowTriggerSimulationMatched
	case WorkflowTriggerChannelMessage, WorkflowTriggerCommand:
		matched, ok, err := MatchInboundMessageTrigger(
			workflow,
			input.WorkflowRef,
			*input.Scenario.Message,
		)
		if err != nil {
			return RunRequest{}, "", nil, false, WorkflowTriggerSimulationEvaluationFailed
		}
		if !ok || matched == nil || matched.Match == nil {
			return RunRequest{}, "", nil, false, WorkflowTriggerSimulationNotMatched
		}
		passthrough := matched.Match.Passthrough
		if matched.Kind != input.Trigger.Kind {
			reason := WorkflowTriggerSimulationNotMatched
			if input.Trigger.Kind == WorkflowTriggerChannelMessage &&
				matched.Kind == WorkflowTriggerCommand {
				reason = WorkflowTriggerSimulationShadowedByCommand
			}
			return RunRequest{}, matched.Kind, &passthrough, false, reason
		}
		return RunRequest{
				Inputs:   matched.Match.Inputs,
				Event:    matched.Match.Event,
				Session:  matched.Match.Session,
				Delivery: matched.Match.Delivery,
			},
			matched.Kind,
			&passthrough,
			true,
			WorkflowTriggerSimulationMatched
	case WorkflowTriggerRuntimeEvent:
		event := *input.Scenario.RuntimeEvent
		match, ok, err := MatchRuntimeEvent(workflow, input.WorkflowRef, event)
		if err != nil {
			return RunRequest{}, "", nil, false, WorkflowTriggerSimulationEvaluationFailed
		}
		if !ok {
			reason := WorkflowTriggerSimulationNotMatched
			if isWorkflowTriggerFeedbackEvent(input.WorkflowRef, event) {
				reason = WorkflowTriggerSimulationRuntimeFeedbackSuppressed
			}
			return RunRequest{}, "", nil, false, reason
		}
		return RunRequest{
				Inputs:   match.Inputs,
				Event:    match.Event,
				Session:  match.Session,
				Delivery: match.Delivery,
			},
			WorkflowTriggerRuntimeEvent,
			nil,
			true,
			WorkflowTriggerSimulationMatched
	case WorkflowTriggerEvent:
		result, err := EvaluateEventTrigger(workflow.On.Event, *input.Scenario.Event)
		if err != nil {
			return RunRequest{}, "", nil, false, WorkflowTriggerSimulationEvaluationFailed
		}
		if !result.Matched {
			return RunRequest{}, "", nil, false, WorkflowTriggerSimulationNotMatched
		}
		context, err := EventWorkflowRunContextFromEnvelope(
			input.WorkflowRef,
			"",
			*input.Scenario.Event,
		)
		if err != nil {
			return RunRequest{}, "", nil, false, WorkflowTriggerSimulationInvalidScenario
		}
		return RunRequest{
				Inputs:   context.Inputs,
				Event:    context.Event,
				Origin:   context.Origin,
				Session:  context.Session,
				Delivery: context.Delivery,
			},
			WorkflowTriggerEvent,
			nil,
			true,
			WorkflowTriggerSimulationMatched
	default:
		return RunRequest{}, "", nil, false, WorkflowTriggerSimulationEvaluationFailed
	}
}

func workflowTriggerEffectReview(
	inspection *WorkflowDefinitionInspection,
) WorkflowTriggerEffectReview {
	review := WorkflowTriggerEffectReview{
		Targets: []string{},
		Effects: []WorkflowDefinitionEffectInspection{},
		Limits:  []WorkflowDefinitionInspectionLimitCode{},
	}
	if inspection == nil {
		return review
	}
	review.Complete = inspection.Complete
	review.Validation = cloneWorkflowTriggerInspectionValidation(inspection.Validation)
	review.Limits = append(review.Limits, inspection.Limits...)
	review.Effects = append(review.Effects, inspection.Effects...)
	targets := make(map[string]struct{})
	for _, job := range inspection.Jobs {
		review.JobCount++
		if job.ReusableTarget != "" {
			targets[job.ReusableTarget] = struct{}{}
		}
		for _, step := range job.Steps {
			review.StepCount++
			if step.Target != "" {
				targets[step.Target] = struct{}{}
			}
		}
	}
	for target := range targets {
		review.Targets = append(review.Targets, target)
	}
	sort.Strings(review.Targets)
	return review
}

func cloneWorkflowTriggerInspectionValidation(
	validation WorkflowDefinitionInspectionValidation,
) WorkflowDefinitionInspectionValidation {
	validation.Issues = append(
		[]WorkflowDefinitionValidationIssue(nil),
		validation.Issues...,
	)
	return validation
}

func workflowTriggerRunContextSummary(
	request RunRequest,
) WorkflowTriggerRunContextSummary {
	return WorkflowTriggerRunContextSummary{
		InputCount:  len(request.Inputs),
		SecretCount: len(request.Secrets),
		HasEvent:    len(request.Event) > 0,
		HasSession:  strings.TrimSpace(request.Session) != "",
		HasDelivery: workflowTriggerDeliveryPresent(request.Delivery),
	}
}

func workflowTriggerDeliveryPresent(delivery Delivery) bool {
	return strings.TrimSpace(delivery.Channel) != "" ||
		strings.TrimSpace(delivery.ChatID) != "" ||
		strings.TrimSpace(delivery.TopicID) != "" ||
		strings.TrimSpace(delivery.ThreadTS) != "" ||
		strings.TrimSpace(delivery.MessageID) != "" ||
		strings.TrimSpace(delivery.ReplyToMessageID) != "" ||
		len(delivery.ReplyHandles) > 0
}

func cloneWorkflowTriggerDelivery(delivery Delivery) Delivery {
	if delivery.ReplyHandles != nil {
		delivery.ReplyHandles = cloneStringMap(delivery.ReplyHandles)
	}
	return delivery
}

func cloneWorkflowTriggerRunRequest(request RunRequest) RunRequest {
	request.Inputs = cloneWorkflowTriggerMap(request.Inputs)
	request.Secrets = cloneWorkflowTriggerStringMap(request.Secrets)
	request.Event = cloneWorkflowTriggerMap(request.Event)
	request.Origin = cloneRunOrigin(request.Origin)
	request.Delivery = cloneWorkflowTriggerDelivery(request.Delivery)
	request.Workflow = nil
	request.OnRunPersisted = nil
	request.OnRunCreated = nil
	return request
}

func cloneWorkflowTriggerMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = cloneWorkflowTriggerValue(value)
	}
	return out
}

func cloneWorkflowTriggerStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func cloneWorkflowTriggerValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneWorkflowTriggerMap(typed)
	case map[string]string:
		return cloneWorkflowTriggerStringMap(typed)
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = cloneWorkflowTriggerValue(item)
		}
		return out
	case []string:
		return append([]string(nil), typed...)
	case json.RawMessage:
		return append(json.RawMessage(nil), typed...)
	case []byte:
		return append([]byte(nil), typed...)
	default:
		return value
	}
}

func cloneWorkflowTriggerIndex(index *int) *int {
	if index == nil {
		return nil
	}
	copyIndex := *index
	return &copyIndex
}
