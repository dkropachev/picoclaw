package workflows

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxWorkflowInspectionSourceBytes bounds one definition inspection.
	MaxWorkflowInspectionSourceBytes int64 = 1 << 20
	// MaxWorkflowInspectionSourceRefBytes bounds the lexical published-source
	// identity accepted before any workflow mutation lock is acquired.
	MaxWorkflowInspectionSourceRefBytes = 16 << 10
	// MaxWorkflowInspectionTriggerSchedules bounds one schedule projection.
	MaxWorkflowInspectionTriggerSchedules = 256
	// MaxWorkflowInspectionTriggerEntries bounds aggregate list and declaration
	// entries across all projected trigger families.
	MaxWorkflowInspectionTriggerEntries = 4096
	// MaxWorkflowInspectionTriggerTextBytes bounds one projected trigger value.
	MaxWorkflowInspectionTriggerTextBytes = 4096
	// MaxWorkflowInspectionTriggerNameBytes bounds one projected declaration or
	// attribute-map name.
	MaxWorkflowInspectionTriggerNameBytes = 256
	// MaxWorkflowInspectionTriggerTypeBytes bounds one projected input type.
	MaxWorkflowInspectionTriggerTypeBytes = 64
	// MaxWorkflowInspectionJobs bounds job topology returned by one inspection.
	MaxWorkflowInspectionJobs = 256
	// MaxWorkflowInspectionIDBytes bounds projected job and step IDs.
	MaxWorkflowInspectionIDBytes = 256
	// MaxWorkflowInspectionStepTargetBytes bounds a full projected step target.
	MaxWorkflowInspectionStepTargetBytes = 512
	// MaxWorkflowInspectionDependencyTargetBytes bounds reusable, dependency,
	// and conservative effect targets.
	MaxWorkflowInspectionDependencyTargetBytes = 1024
	// MaxWorkflowInspectionEntries independently bounds projected steps,
	// dependency occurrences, and conservative effect occurrences.
	MaxWorkflowInspectionEntries = 4096
	// MaxWorkflowInspectionValidationIssues bounds public validation details.
	MaxWorkflowInspectionValidationIssues = 128
)

var (
	// ErrWorkflowInspectionSourceInvalid identifies an invalid source union or
	// published definition reference without echoing attacker-controlled data.
	ErrWorkflowInspectionSourceInvalid = errors.New("workflow inspection source is invalid")
	// ErrWorkflowInspectionNotFound identifies a missing published definition.
	ErrWorkflowInspectionNotFound = errors.New("workflow inspection source was not found")
	// ErrWorkflowInspectionSourceTooLarge identifies a definition outside the
	// fixed inspection byte budget.
	ErrWorkflowInspectionSourceTooLarge = errors.New("workflow inspection source is too large")
	// ErrWorkflowInspectionUnavailable identifies a source that cannot be
	// safely resolved or read without retaining filesystem details.
	ErrWorkflowInspectionUnavailable = errors.New("workflow inspection is unavailable")
)

// WorkflowDefinitionInspectionSourceKind identifies the public origin of an
// inspected immutable byte snapshot.
type WorkflowDefinitionInspectionSourceKind string

const (
	WorkflowDefinitionInspectionSourcePublished WorkflowDefinitionInspectionSourceKind = "published"
	WorkflowDefinitionInspectionSourceTemplate  WorkflowDefinitionInspectionSourceKind = "template"
)

// WorkflowDefinitionInspectionSource is a strict union. Published sources use
// Ref; template sources use TemplateName.
type WorkflowDefinitionInspectionSource struct {
	Kind         WorkflowDefinitionInspectionSourceKind `json:"kind"`
	Ref          string                                 `json:"ref,omitempty"`
	TemplateName string                                 `json:"template_name,omitempty"`
}

// WorkflowDefinitionInspection is the bounded, path-free public projection of
// one exact workflow definition. It deliberately excludes source YAML and
// arbitrary executable values.
type WorkflowDefinitionInspection struct {
	Source       WorkflowDefinitionInspectionSource                          `json:"source"`
	Revision     string                                                      `json:"revision"`
	Complete     bool                                                        `json:"complete"`
	Validation   WorkflowDefinitionInspectionValidation                      `json:"validation"`
	Triggers     map[WorkflowTriggerKind]WorkflowDefinitionTriggerInspection `json:"triggers"`
	Jobs         []WorkflowDefinitionJobInspection                           `json:"jobs"`
	Dependencies []WorkflowDefinitionDependencyInspection                    `json:"dependencies"`
	Effects      []WorkflowDefinitionEffectInspection                        `json:"effects"`
	Limits       []WorkflowDefinitionInspectionLimitCode                     `json:"limits"`
}

// WorkflowDefinitionInspectionValidation contains only fixed codes and broad
// scopes. Parser and validator messages never cross this boundary.
type WorkflowDefinitionInspectionValidation struct {
	Valid      bool                                `json:"valid"`
	IssueCount int                                 `json:"issue_count"`
	Issues     []WorkflowDefinitionValidationIssue `json:"issues"`
	Truncated  bool                                `json:"truncated"`
}

// WorkflowDefinitionValidationIssue is one safe validation result.
type WorkflowDefinitionValidationIssue struct {
	Code  WorkflowDefinitionValidationCode  `json:"code"`
	Scope WorkflowDefinitionValidationScope `json:"scope"`
}

// WorkflowDefinitionValidationCode is a fixed public validation reason.
type WorkflowDefinitionValidationCode string

const (
	WorkflowDefinitionValidationInvalidYAML               WorkflowDefinitionValidationCode = "invalid_yaml"
	WorkflowDefinitionValidationJobsRequired              WorkflowDefinitionValidationCode = "jobs_required"
	WorkflowDefinitionValidationScheduleCronRequired      WorkflowDefinitionValidationCode = "schedule_cron_required"
	WorkflowDefinitionValidationScheduleCronInvalid       WorkflowDefinitionValidationCode = "schedule_cron_invalid"
	WorkflowDefinitionValidationInputNameRequired         WorkflowDefinitionValidationCode = "input_name_required"
	WorkflowDefinitionValidationInputTypeUnsupported      WorkflowDefinitionValidationCode = "input_type_unsupported"
	WorkflowDefinitionValidationInputDefaultInvalid       WorkflowDefinitionValidationCode = "input_default_invalid"
	WorkflowDefinitionValidationOutputRequired            WorkflowDefinitionValidationCode = "output_required"
	WorkflowDefinitionValidationOutputExpressionInvalid   WorkflowDefinitionValidationCode = "output_expression_invalid"
	WorkflowDefinitionValidationConversationSession       WorkflowDefinitionValidationCode = "conversation_session_unsupported"
	WorkflowDefinitionValidationConversationDelivery      WorkflowDefinitionValidationCode = "conversation_delivery_unsupported"
	WorkflowDefinitionValidationChannelPatternInvalid     WorkflowDefinitionValidationCode = "channel_pattern_invalid"
	WorkflowDefinitionValidationCommandNameRequired       WorkflowDefinitionValidationCode = "command_name_required"
	WorkflowDefinitionValidationRuntimeFilterRequired     WorkflowDefinitionValidationCode = "runtime_filter_required"
	WorkflowDefinitionValidationEventFilterRequired       WorkflowDefinitionValidationCode = "event_filter_required"
	WorkflowDefinitionValidationEventEntityFilterRequired WorkflowDefinitionValidationCode = "event_entity_filter_required"
	WorkflowDefinitionValidationEventPatternRequired      WorkflowDefinitionValidationCode = "event_pattern_required"
	WorkflowDefinitionValidationEventAttributeRequired    WorkflowDefinitionValidationCode = "event_attribute_required"
	WorkflowDefinitionValidationJobIDRequired             WorkflowDefinitionValidationCode = "job_id_required"
	WorkflowDefinitionValidationJobDependencyUnknown      WorkflowDefinitionValidationCode = "job_dependency_unknown"
	WorkflowDefinitionValidationJobDependencyCycle        WorkflowDefinitionValidationCode = "job_dependency_cycle"
	WorkflowDefinitionValidationReusableTargetInvalid     WorkflowDefinitionValidationCode = "reusable_target_invalid"
	WorkflowDefinitionValidationReusableStepsUnsupported  WorkflowDefinitionValidationCode = "reusable_steps_unsupported"
	WorkflowDefinitionValidationJobRunnerRequired         WorkflowDefinitionValidationCode = "job_runner_required"
	WorkflowDefinitionValidationJobStepsRequired          WorkflowDefinitionValidationCode = "job_steps_required"
	WorkflowDefinitionValidationStepIDDuplicate           WorkflowDefinitionValidationCode = "step_id_duplicate"
	WorkflowDefinitionValidationStepTargetRequired        WorkflowDefinitionValidationCode = "step_target_required"
	WorkflowDefinitionValidationReusableStepUnsupported   WorkflowDefinitionValidationCode = "reusable_step_unsupported"
	WorkflowDefinitionValidationStepTargetUnsupported     WorkflowDefinitionValidationCode = "step_target_unsupported"
	WorkflowDefinitionValidationRunSessionUnsupported     WorkflowDefinitionValidationCode = "run_session_unsupported"
	WorkflowDefinitionValidationRunDeliveryUnsupported    WorkflowDefinitionValidationCode = "run_delivery_unsupported"
	WorkflowDefinitionValidationAgentHistoryUnsupported   WorkflowDefinitionValidationCode = "agent_history_unsupported"
	WorkflowDefinitionValidationAgentCacheUnsupported     WorkflowDefinitionValidationCode = "agent_cache_unsupported"
	WorkflowDefinitionValidationAgentToolsUnsupported     WorkflowDefinitionValidationCode = "agent_tools_unsupported"
	WorkflowDefinitionValidationDefinitionInvalid         WorkflowDefinitionValidationCode = "definition_invalid"
)

// WorkflowDefinitionValidationScope is intentionally coarser than validator
// paths so IDs and arbitrary mapping keys are never copied into the response.
type WorkflowDefinitionValidationScope string

const (
	WorkflowDefinitionValidationScopeWorkflow       WorkflowDefinitionValidationScope = "workflow"
	WorkflowDefinitionValidationScopeJobs           WorkflowDefinitionValidationScope = "jobs"
	WorkflowDefinitionValidationScopeManual         WorkflowDefinitionValidationScope = "trigger.manual"
	WorkflowDefinitionValidationScopeSchedule       WorkflowDefinitionValidationScope = "trigger.schedule"
	WorkflowDefinitionValidationScopeChannelMessage WorkflowDefinitionValidationScope = "trigger.channel_message"
	WorkflowDefinitionValidationScopeCommand        WorkflowDefinitionValidationScope = "trigger.command"
	WorkflowDefinitionValidationScopeRuntimeEvent   WorkflowDefinitionValidationScope = "trigger.runtime_event"
	WorkflowDefinitionValidationScopeEvent          WorkflowDefinitionValidationScope = "trigger.event"
	WorkflowDefinitionValidationScopeWorkflowCall   WorkflowDefinitionValidationScope = "trigger.workflow_call"
)

// WorkflowDefinitionTriggerInspection reports AST presence separately from a
// safe typed projection.
type WorkflowDefinitionTriggerInspection struct {
	Present   bool `json:"present"`
	Projected bool `json:"projected"`
	Value     any  `json:"value,omitempty"`
}

// WorkflowDefinitionScheduleTriggerInspection always emits cron, including an
// empty invalid declaration, so invalid definitions remain inspectable.
type WorkflowDefinitionScheduleTriggerInspection struct {
	Cron string `json:"cron"`
}

// WorkflowDefinitionInputInspection omits input defaults by design.
type WorkflowDefinitionInputInspection struct {
	Type       string `json:"type,omitempty"`
	Required   bool   `json:"required"`
	HasDefault bool   `json:"has_default"`
}

// WorkflowDefinitionChannelTriggerInspection omits conversation session and
// delivery configuration.
type WorkflowDefinitionChannelTriggerInspection struct {
	Channels           StringList `json:"channels,omitempty"`
	Chats              StringList `json:"chats,omitempty"`
	Senders            StringList `json:"senders,omitempty"`
	Mentioned          *bool      `json:"mentioned,omitempty"`
	Command            string     `json:"command,omitempty"`
	TextMatches        string     `json:"text_matches,omitempty"`
	Passthrough        *bool      `json:"passthrough,omitempty"`
	SessionConfigured  bool       `json:"session_configured"`
	DeliveryConfigured bool       `json:"delivery_configured"`
}

// WorkflowDefinitionCommandTriggerInspection omits argument defaults and
// conversation session/delivery configuration.
type WorkflowDefinitionCommandTriggerInspection struct {
	Name               string                                       `json:"name,omitempty"`
	Channels           StringList                                   `json:"channels,omitempty"`
	Chats              StringList                                   `json:"chats,omitempty"`
	Senders            StringList                                   `json:"senders,omitempty"`
	Args               map[string]WorkflowDefinitionInputInspection `json:"args,omitempty"`
	Passthrough        *bool                                        `json:"passthrough,omitempty"`
	SessionConfigured  bool                                         `json:"session_configured"`
	DeliveryConfigured bool                                         `json:"delivery_configured"`
}

// WorkflowDefinitionRuntimeEventTriggerInspection omits session filter values.
type WorkflowDefinitionRuntimeEventTriggerInspection struct {
	Kinds                StringList `json:"kinds,omitempty"`
	Sources              StringList `json:"sources,omitempty"`
	Agents               StringList `json:"agents,omitempty"`
	SessionFilterPresent bool       `json:"session_filter_present"`
	SessionFilterCount   int        `json:"session_filter_count"`
	Channels             StringList `json:"channels,omitempty"`
	Chats                StringList `json:"chats,omitempty"`
}

// WorkflowDefinitionSecretInspection exposes only declaration requirements.
type WorkflowDefinitionSecretInspection struct {
	Required bool `json:"required"`
}

// WorkflowDefinitionWorkflowCallInspection omits input defaults, secret
// values/mappings, and output expressions. Declaration names remain
// inspectable.
type WorkflowDefinitionWorkflowCallInspection struct {
	Inputs  map[string]WorkflowDefinitionInputInspection  `json:"inputs,omitempty"`
	Secrets map[string]WorkflowDefinitionSecretInspection `json:"secrets,omitempty"`
	Outputs []string                                      `json:"outputs,omitempty"`
}

// WorkflowDefinitionJobKind classifies topology without exposing job names,
// runner configuration, conditions, arguments, secrets, outputs, or context.
type WorkflowDefinitionJobKind string

const (
	WorkflowDefinitionJobSteps    WorkflowDefinitionJobKind = "steps"
	WorkflowDefinitionJobReusable WorkflowDefinitionJobKind = "reusable"
)

// WorkflowDefinitionJobInspection is one deterministic job summary.
type WorkflowDefinitionJobInspection struct {
	ID             string                             `json:"id"`
	Kind           WorkflowDefinitionJobKind          `json:"kind"`
	ReusableTarget string                             `json:"reusable_target,omitempty"`
	Steps          []WorkflowDefinitionStepInspection `json:"steps"`
}

// WorkflowDefinitionStepKind classifies a declared step target.
type WorkflowDefinitionStepKind string

const (
	WorkflowDefinitionStepAgent    WorkflowDefinitionStepKind = "agent"
	WorkflowDefinitionStepTool     WorkflowDefinitionStepKind = "tool"
	WorkflowDefinitionStepMCP      WorkflowDefinitionStepKind = "mcp"
	WorkflowDefinitionStepFunction WorkflowDefinitionStepKind = "function"
	WorkflowDefinitionStepUnknown  WorkflowDefinitionStepKind = "unknown"
)

// WorkflowDefinitionStepInspection excludes prompts, with values, conditions,
// names, context, and output expressions.
type WorkflowDefinitionStepInspection struct {
	Index  int                        `json:"index"`
	ID     string                     `json:"id,omitempty"`
	Kind   WorkflowDefinitionStepKind `json:"kind"`
	Target string                     `json:"target,omitempty"`
}

// WorkflowDefinitionDependencyInspection groups direct declarations without
// retaining source paths.
type WorkflowDefinitionDependencyInspection struct {
	Kind        WorkflowDependencyKind `json:"kind"`
	Target      string                 `json:"target"`
	Occurrences int                    `json:"occurrences"`
}

// WorkflowDefinitionEffectKind is a conservative fixed public claim.
type WorkflowDefinitionEffectKind string

const (
	WorkflowDefinitionEffectModelOrDelegatedAction WorkflowDefinitionEffectKind = "model_or_delegated_action_possible"
	WorkflowDefinitionEffectStateChange            WorkflowDefinitionEffectKind = "state_change_possible"
	WorkflowDefinitionEffectExternalStateChange    WorkflowDefinitionEffectKind = "external_state_change_possible"
	WorkflowDefinitionEffectTransitiveUnknown      WorkflowDefinitionEffectKind = "transitive_effects_unknown"
	WorkflowDefinitionEffectUnclassifiedAction     WorkflowDefinitionEffectKind = "unclassified_action"
)

// WorkflowDefinitionEffectInspection groups conservative action claims.
type WorkflowDefinitionEffectInspection struct {
	Kind        WorkflowDefinitionEffectKind `json:"kind"`
	Target      string                       `json:"target,omitempty"`
	Occurrences int                          `json:"occurrences"`
}

// WorkflowDefinitionInspectionLimitCode identifies an intentionally truncated
// projection section.
type WorkflowDefinitionInspectionLimitCode string

const (
	WorkflowDefinitionInspectionLimitJobs             WorkflowDefinitionInspectionLimitCode = "jobs_truncated"
	WorkflowDefinitionInspectionLimitSteps            WorkflowDefinitionInspectionLimitCode = "steps_truncated"
	WorkflowDefinitionInspectionLimitDependencies     WorkflowDefinitionInspectionLimitCode = "dependencies_truncated"
	WorkflowDefinitionInspectionLimitEffects          WorkflowDefinitionInspectionLimitCode = "effects_truncated"
	WorkflowDefinitionInspectionLimitTriggers         WorkflowDefinitionInspectionLimitCode = "triggers_truncated"
	WorkflowDefinitionInspectionLimitUnsafeFields     WorkflowDefinitionInspectionLimitCode = "unsafe_fields_omitted"
	WorkflowDefinitionInspectionLimitValidationIssues WorkflowDefinitionInspectionLimitCode = "validation_issues_truncated"
)

// InspectWorkflowDefinitionBytes inspects an exact byte snapshot. source must
// be a valid published/template union; data is never normalized.
func InspectWorkflowDefinitionBytes(
	source WorkflowDefinitionInspectionSource,
	data []byte,
) (*WorkflowDefinitionInspection, error) {
	normalizedSource, err := normalizeWorkflowInspectionSource(source)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > MaxWorkflowInspectionSourceBytes {
		return nil, ErrWorkflowInspectionSourceTooLarge
	}

	inspection := &WorkflowDefinitionInspection{
		Source:       normalizedSource,
		Revision:     workflowContentRevision(data),
		Complete:     true,
		Triggers:     make(map[WorkflowTriggerKind]WorkflowDefinitionTriggerInspection, len(workflowTriggerKinds)),
		Jobs:         make([]WorkflowDefinitionJobInspection, 0),
		Dependencies: make([]WorkflowDefinitionDependencyInspection, 0),
		Effects:      make([]WorkflowDefinitionEffectInspection, 0),
		Limits:       make([]WorkflowDefinitionInspectionLimitCode, 0),
	}
	for _, kind := range workflowTriggerKinds {
		inspection.Triggers[kind] = WorkflowDefinitionTriggerInspection{}
	}

	raw := string(data)
	triggerInspection := InspectWorkflowTriggers(raw)
	triggerEntriesRemaining := MaxWorkflowInspectionTriggerEntries
	for _, kind := range workflowTriggerKinds {
		projected := triggerInspection.Triggers[kind]
		public := WorkflowDefinitionTriggerInspection{
			Present:   projected.Present,
			Projected: projected.Editable,
		}
		if projected.Present && !projected.Editable {
			// The authoring projection deliberately declines advanced, aliased,
			// or otherwise lossless-only trigger shapes. Inspection must not
			// silently treat that omission as a complete matcher review.
			inspection.addLimit(WorkflowDefinitionInspectionLimitTriggers)
		}
		if projected.Present && projected.Editable {
			var limited bool
			public.Value, limited = publicWorkflowTriggerValue(
				kind,
				projected.Value,
				&triggerEntriesRemaining,
			)
			if limited {
				inspection.addLimit(WorkflowDefinitionInspectionLimitTriggers)
			}
			if public.Value == nil {
				public.Projected = false
			}
		}
		inspection.Triggers[kind] = public
	}

	workflow, parseErr := Parse(data)
	inspection.Validation = inspectWorkflowValidation(workflow, parseErr)
	if inspection.Validation.Truncated {
		inspection.addLimit(WorkflowDefinitionInspectionLimitValidationIssues)
	}

	var jobs map[string]Job
	if workflow != nil {
		// Parse is the runtime authority. Valid YAML aliases and merges must
		// not make executable topology disappear merely because the lossless
		// structured editor declines to rewrite them.
		jobs = workflow.Jobs
	} else {
		// A malformed sibling trigger can still leave an independently safe
		// jobs mapping available for conservative invalid-definition review.
		jobs = inspectWorkflowJobsFromBytes(raw)
	}
	inspection.projectJobs(jobs)
	return inspection, nil
}

// InspectLocalWorkflowDefinition resolves and reads one published definition
// exactly once while holding the shared workflow mutation lock.
func InspectLocalWorkflowDefinition(
	ctx context.Context,
	workspace string,
	ref string,
	opts ...LocalOption,
) (*WorkflowDefinitionInspection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	canonical, err := CanonicalLocalRef(ref)
	if err != nil ||
		canonical != ref ||
		!safeWorkflowInspectionText(canonical, MaxWorkflowInspectionSourceRefBytes) {
		return nil, ErrWorkflowInspectionSourceInvalid
	}
	unlock, err := lockWorkflowMutation(workspace)
	if err != nil {
		return nil, ErrWorkflowInspectionUnavailable
	}
	if contextErr := ctx.Err(); contextErr != nil {
		unlock()
		return nil, contextErr
	}

	local := collectLocalOptions(opts...)
	definitionsDir, err := cleanDefinitionsDir(local.DefinitionsDir)
	if err != nil {
		unlock()
		return nil, ErrWorkflowInspectionUnavailable
	}
	data, err := readWorkflowInspectionSource(
		workspace,
		definitionsDir,
		filepath.FromSlash(strings.TrimPrefix(canonical, DefaultDefinitionsDir+"/")),
	)
	unlock()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return InspectWorkflowDefinitionBytes(
		WorkflowDefinitionInspectionSource{
			Kind: WorkflowDefinitionInspectionSourcePublished,
			Ref:  canonical,
		},
		data,
	)
}

// InspectBuiltInWorkflowTemplate inspects immutable built-in registry bytes.
// Blank names are invalid and never select the historical default template.
func InspectBuiltInWorkflowTemplate(name string) (*WorkflowDefinitionInspection, error) {
	canonicalName := strings.ToLower(strings.TrimSpace(name))
	if canonicalName == "" ||
		canonicalName != name ||
		!safeWorkflowInspectionTemplateName(canonicalName) {
		return nil, ErrWorkflowInspectionSourceInvalid
	}
	template, ok := findBuiltInWorkflowTemplate(canonicalName)
	if !ok {
		return nil, ErrWorkflowTemplateUnknown
	}
	return InspectWorkflowDefinitionBytes(
		WorkflowDefinitionInspectionSource{
			Kind:         WorkflowDefinitionInspectionSourceTemplate,
			TemplateName: template.name,
		},
		[]byte(template.raw),
	)
}

func normalizeWorkflowInspectionSource(
	source WorkflowDefinitionInspectionSource,
) (WorkflowDefinitionInspectionSource, error) {
	switch source.Kind {
	case WorkflowDefinitionInspectionSourcePublished:
		if strings.TrimSpace(source.TemplateName) != "" {
			return WorkflowDefinitionInspectionSource{}, ErrWorkflowInspectionSourceInvalid
		}
		canonical, err := CanonicalLocalRef(source.Ref)
		if err != nil ||
			canonical != source.Ref ||
			!safeWorkflowInspectionText(canonical, MaxWorkflowInspectionSourceRefBytes) {
			return WorkflowDefinitionInspectionSource{}, ErrWorkflowInspectionSourceInvalid
		}
		return WorkflowDefinitionInspectionSource{
			Kind: WorkflowDefinitionInspectionSourcePublished,
			Ref:  canonical,
		}, nil
	case WorkflowDefinitionInspectionSourceTemplate:
		if strings.TrimSpace(source.Ref) != "" {
			return WorkflowDefinitionInspectionSource{}, ErrWorkflowInspectionSourceInvalid
		}
		name := strings.ToLower(strings.TrimSpace(source.TemplateName))
		if name != source.TemplateName ||
			!safeWorkflowInspectionTemplateName(name) {
			return WorkflowDefinitionInspectionSource{}, ErrWorkflowInspectionSourceInvalid
		}
		return WorkflowDefinitionInspectionSource{
			Kind:         WorkflowDefinitionInspectionSourceTemplate,
			TemplateName: name,
		}, nil
	default:
		return WorkflowDefinitionInspectionSource{}, ErrWorkflowInspectionSourceInvalid
	}
}

func readWorkflowInspectionSource(
	workspace string,
	definitionsDir string,
	relative string,
) ([]byte, error) {
	if strings.TrimSpace(workspace) == "" {
		workspace = "."
	}
	workspaceRoot, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, ErrWorkflowInspectionUnavailable
	}
	defer workspaceRoot.Close()
	definitionsRoot, err := workspaceRoot.OpenRoot(definitionsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrWorkflowInspectionNotFound
		}
		return nil, ErrWorkflowInspectionUnavailable
	}
	defer definitionsRoot.Close()
	file, err := openWorkflowInspectionDefinition(definitionsRoot, relative)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrWorkflowInspectionNotFound
		}
		return nil, ErrWorkflowInspectionUnavailable
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrWorkflowInspectionUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxWorkflowInspectionSourceBytes+1))
	if err != nil {
		return nil, ErrWorkflowInspectionUnavailable
	}
	if int64(len(data)) > MaxWorkflowInspectionSourceBytes {
		return nil, ErrWorkflowInspectionSourceTooLarge
	}
	return data, nil
}

type workflowInspectionTriggerMeasure struct {
	entries int
	limited bool
}

func (measure *workflowInspectionTriggerMeasure) addEntries(count int) bool {
	if count < 0 ||
		count > MaxWorkflowInspectionTriggerEntries-measure.entries {
		measure.limited = true
		return false
	}
	measure.entries += count
	return true
}

func (measure *workflowInspectionTriggerMeasure) text(
	value string,
	maxBytes int,
) bool {
	if len(value) > maxBytes || !utf8.ValidString(value) {
		measure.limited = true
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			measure.limited = true
			return false
		}
	}
	return true
}

func (measure *workflowInspectionTriggerMeasure) stringList(
	values StringList,
) bool {
	if !measure.addEntries(len(values)) {
		return false
	}
	for _, value := range values {
		if !measure.text(value, MaxWorkflowInspectionTriggerTextBytes) {
			return false
		}
	}
	return true
}

func (measure *workflowInspectionTriggerMeasure) inputs(
	inputs map[string]Input,
) bool {
	if !measure.addEntries(len(inputs)) {
		return false
	}
	for name, input := range inputs {
		if !measure.text(name, MaxWorkflowInspectionTriggerNameBytes) ||
			!measure.text(input.Type, MaxWorkflowInspectionTriggerTypeBytes) {
			return false
		}
	}
	return true
}

func (measure *workflowInspectionTriggerMeasure) secrets(
	secrets map[string]Secret,
) bool {
	if !measure.addEntries(len(secrets)) {
		return false
	}
	for name := range secrets {
		if !measure.text(name, MaxWorkflowInspectionTriggerNameBytes) {
			return false
		}
	}
	return true
}

func (measure *workflowInspectionTriggerMeasure) outputNames(
	outputs map[string]Output,
) bool {
	if !measure.addEntries(len(outputs)) {
		return false
	}
	for name := range outputs {
		if !measure.text(name, MaxWorkflowInspectionTriggerNameBytes) {
			return false
		}
	}
	return true
}

func (measure *workflowInspectionTriggerMeasure) stringListMap(
	values map[string]StringList,
) bool {
	if !measure.addEntries(len(values)) {
		return false
	}
	for name, items := range values {
		if !measure.text(name, MaxWorkflowInspectionTriggerNameBytes) ||
			!measure.stringList(items) {
			return false
		}
	}
	return true
}

func (measure *workflowInspectionTriggerMeasure) eventEntity(
	entity *EventEntityTrigger,
) bool {
	return entity == nil ||
		(measure.stringList(entity.IDs) &&
			measure.stringList(entity.Types) &&
			measure.stringListMap(entity.Attributes))
}

func measureWorkflowTriggerValue(
	kind WorkflowTriggerKind,
	value any,
) (workflowInspectionTriggerMeasure, bool) {
	var measure workflowInspectionTriggerMeasure
	switch kind {
	case WorkflowTriggerManual:
		manual, ok := value.(map[string]any)
		return measure, ok && len(manual) == 0
	case WorkflowTriggerSchedule:
		schedules, ok := value.([]ScheduleTrigger)
		if !ok {
			return measure, false
		}
		if len(schedules) > MaxWorkflowInspectionTriggerSchedules ||
			!measure.addEntries(len(schedules)) {
			measure.limited = true
			return measure, true
		}
		for _, schedule := range schedules {
			if !measure.text(
				schedule.Cron,
				MaxWorkflowInspectionTriggerTextBytes,
			) {
				break
			}
		}
	case WorkflowTriggerChannelMessage:
		trigger, ok := value.(*ChannelMessageTrigger)
		if !ok || trigger == nil {
			return measure, false
		}
		measure.stringList(trigger.Channels)
		measure.stringList(trigger.Chats)
		measure.stringList(trigger.Senders)
		measure.text(trigger.Command, MaxWorkflowInspectionTriggerTextBytes)
		measure.text(trigger.TextMatches, MaxWorkflowInspectionTriggerTextBytes)
	case WorkflowTriggerCommand:
		trigger, ok := value.(*CommandTrigger)
		if !ok || trigger == nil {
			return measure, false
		}
		measure.text(trigger.Name, MaxWorkflowInspectionTriggerTextBytes)
		measure.stringList(trigger.Channels)
		measure.stringList(trigger.Chats)
		measure.stringList(trigger.Senders)
		measure.inputs(trigger.Args)
	case WorkflowTriggerRuntimeEvent:
		trigger, ok := value.(*RuntimeEventTrigger)
		if !ok || trigger == nil {
			return measure, false
		}
		measure.stringList(trigger.Kinds)
		measure.stringList(trigger.Sources)
		measure.stringList(trigger.Agents)
		measure.stringList(trigger.Channels)
		measure.stringList(trigger.Chats)
	case WorkflowTriggerEvent:
		trigger, ok := value.(*EventTrigger)
		if !ok || trigger == nil {
			return measure, false
		}
		measure.stringList(trigger.Sources)
		measure.stringList(trigger.Connectors)
		measure.stringList(trigger.Types)
		measure.eventEntity(trigger.Actor)
		measure.eventEntity(trigger.Subject)
		measure.stringListMap(trigger.Attributes)
	case WorkflowTriggerWorkflowCall:
		trigger, ok := value.(*WorkflowCall)
		if !ok || trigger == nil {
			return measure, false
		}
		measure.inputs(trigger.Inputs)
		measure.secrets(trigger.Secrets)
		measure.outputNames(trigger.Outputs)
	default:
		return measure, false
	}
	return measure, true
}

func publicWorkflowTriggerValue(
	kind WorkflowTriggerKind,
	value any,
	entriesRemaining *int,
) (any, bool) {
	measure, ok := measureWorkflowTriggerValue(kind, value)
	if !ok {
		return nil, false
	}
	if measure.limited || measure.entries > *entriesRemaining {
		return nil, true
	}
	public := clonePublicWorkflowTriggerValue(kind, value)
	if public == nil {
		return nil, false
	}
	*entriesRemaining -= measure.entries
	return public, false
}

func clonePublicWorkflowTriggerValue(kind WorkflowTriggerKind, value any) any {
	switch kind {
	case WorkflowTriggerManual:
		manual, ok := value.(map[string]any)
		if !ok || len(manual) != 0 {
			return nil
		}
		return map[string]any{}
	case WorkflowTriggerSchedule:
		schedules, ok := value.([]ScheduleTrigger)
		if !ok {
			return nil
		}
		out := make([]WorkflowDefinitionScheduleTriggerInspection, len(schedules))
		for index, schedule := range schedules {
			out[index] = WorkflowDefinitionScheduleTriggerInspection{
				Cron: schedule.Cron,
			}
		}
		return out
	case WorkflowTriggerChannelMessage:
		trigger, ok := value.(*ChannelMessageTrigger)
		if !ok || trigger == nil {
			return nil
		}
		return WorkflowDefinitionChannelTriggerInspection{
			Channels:           cloneInspectionStringList(trigger.Channels),
			Chats:              cloneInspectionStringList(trigger.Chats),
			Senders:            cloneInspectionStringList(trigger.Senders),
			Mentioned:          cloneInspectionBool(trigger.Mentioned),
			Command:            trigger.Command,
			TextMatches:        trigger.TextMatches,
			Passthrough:        cloneInspectionBool(trigger.Passthrough),
			SessionConfigured:  strings.TrimSpace(trigger.Conversation.Session) != "",
			DeliveryConfigured: strings.TrimSpace(trigger.Conversation.Delivery) != "",
		}
	case WorkflowTriggerCommand:
		trigger, ok := value.(*CommandTrigger)
		if !ok || trigger == nil {
			return nil
		}
		return WorkflowDefinitionCommandTriggerInspection{
			Name:               trigger.Name,
			Channels:           cloneInspectionStringList(trigger.Channels),
			Chats:              cloneInspectionStringList(trigger.Chats),
			Senders:            cloneInspectionStringList(trigger.Senders),
			Args:               inspectWorkflowInputs(trigger.Args),
			Passthrough:        cloneInspectionBool(trigger.Passthrough),
			SessionConfigured:  strings.TrimSpace(trigger.Conversation.Session) != "",
			DeliveryConfigured: strings.TrimSpace(trigger.Conversation.Delivery) != "",
		}
	case WorkflowTriggerRuntimeEvent:
		trigger, ok := value.(*RuntimeEventTrigger)
		if !ok || trigger == nil {
			return nil
		}
		return WorkflowDefinitionRuntimeEventTriggerInspection{
			Kinds:                cloneInspectionStringList(trigger.Kinds),
			Sources:              cloneInspectionStringList(trigger.Sources),
			Agents:               cloneInspectionStringList(trigger.Agents),
			SessionFilterPresent: trigger.Sessions != nil,
			SessionFilterCount:   len(trigger.Sessions),
			Channels:             cloneInspectionStringList(trigger.Channels),
			Chats:                cloneInspectionStringList(trigger.Chats),
		}
	case WorkflowTriggerEvent:
		trigger, ok := value.(*EventTrigger)
		if !ok || trigger == nil {
			return nil
		}
		return inspectEventTrigger(trigger)
	case WorkflowTriggerWorkflowCall:
		trigger, ok := value.(*WorkflowCall)
		if !ok || trigger == nil {
			return nil
		}
		outputs := make([]string, 0, len(trigger.Outputs))
		for name := range trigger.Outputs {
			outputs = append(outputs, name)
		}
		sort.Strings(outputs)
		return WorkflowDefinitionWorkflowCallInspection{
			Inputs:  inspectWorkflowInputs(trigger.Inputs),
			Secrets: inspectWorkflowSecrets(trigger.Secrets),
			Outputs: outputs,
		}
	default:
		return nil
	}
}

func inspectWorkflowInputs(
	inputs map[string]Input,
) map[string]WorkflowDefinitionInputInspection {
	if inputs == nil {
		return nil
	}
	out := make(map[string]WorkflowDefinitionInputInspection, len(inputs))
	for name, input := range inputs {
		out[name] = WorkflowDefinitionInputInspection{
			Type:       input.Type,
			Required:   input.Required,
			HasDefault: input.Default != nil,
		}
	}
	return out
}

func inspectWorkflowSecrets(
	secrets map[string]Secret,
) map[string]WorkflowDefinitionSecretInspection {
	if secrets == nil {
		return nil
	}
	out := make(map[string]WorkflowDefinitionSecretInspection, len(secrets))
	for name, secret := range secrets {
		out[name] = WorkflowDefinitionSecretInspection{Required: secret.Required}
	}
	return out
}

func inspectEventTrigger(trigger *EventTrigger) EventTrigger {
	out := EventTrigger{
		Sources:    cloneInspectionStringList(trigger.Sources),
		Connectors: cloneInspectionStringList(trigger.Connectors),
		Types:      cloneInspectionStringList(trigger.Types),
		Attributes: cloneInspectionStringListMap(trigger.Attributes),
	}
	if trigger.Actor != nil {
		out.Actor = &EventEntityTrigger{
			IDs:        cloneInspectionStringList(trigger.Actor.IDs),
			Types:      cloneInspectionStringList(trigger.Actor.Types),
			Attributes: cloneInspectionStringListMap(trigger.Actor.Attributes),
		}
	}
	if trigger.Subject != nil {
		out.Subject = &EventEntityTrigger{
			IDs:        cloneInspectionStringList(trigger.Subject.IDs),
			Types:      cloneInspectionStringList(trigger.Subject.Types),
			Attributes: cloneInspectionStringListMap(trigger.Subject.Attributes),
		}
	}
	return out
}

func cloneInspectionStringList(values StringList) StringList {
	return append(StringList(nil), values...)
}

func cloneInspectionStringListMap(
	values map[string]StringList,
) map[string]StringList {
	if values == nil {
		return nil
	}
	out := make(map[string]StringList, len(values))
	for key, items := range values {
		out[key] = cloneInspectionStringList(items)
	}
	return out
}

func cloneInspectionBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func inspectWorkflowValidation(
	workflow *Workflow,
	parseErr error,
) WorkflowDefinitionInspectionValidation {
	result := WorkflowDefinitionInspectionValidation{
		Issues: make([]WorkflowDefinitionValidationIssue, 0),
	}
	if parseErr != nil || workflow == nil {
		result.IssueCount = 1
		result.Issues = append(result.Issues, WorkflowDefinitionValidationIssue{
			Code:  WorkflowDefinitionValidationInvalidYAML,
			Scope: WorkflowDefinitionValidationScopeWorkflow,
		})
		return result
	}
	validationErr := Validate(workflow)
	if validationErr == nil {
		result.Valid = true
		return result
	}
	var validationErrors ValidationErrors
	if !errors.As(validationErr, &validationErrors) {
		result.IssueCount = 1
		result.Issues = append(result.Issues, WorkflowDefinitionValidationIssue{
			Code:  WorkflowDefinitionValidationDefinitionInvalid,
			Scope: WorkflowDefinitionValidationScopeWorkflow,
		})
		return result
	}
	result.IssueCount = len(validationErrors)
	limit := len(validationErrors)
	if limit > MaxWorkflowInspectionValidationIssues {
		limit = MaxWorkflowInspectionValidationIssues
		result.Truncated = true
	}
	result.Issues = make([]WorkflowDefinitionValidationIssue, 0, limit)
	for _, issue := range validationErrors[:limit] {
		result.Issues = append(result.Issues, WorkflowDefinitionValidationIssue{
			Code:  workflowInspectionValidationCode(issue),
			Scope: workflowInspectionValidationScope(issue.Path),
		})
	}
	return result
}

func workflowInspectionValidationScope(path string) WorkflowDefinitionValidationScope {
	switch {
	case strings.HasPrefix(path, "on.manual"):
		return WorkflowDefinitionValidationScopeManual
	case strings.HasPrefix(path, "on.schedule"):
		return WorkflowDefinitionValidationScopeSchedule
	case strings.HasPrefix(path, "on.channel_message"):
		return WorkflowDefinitionValidationScopeChannelMessage
	case strings.HasPrefix(path, "on.command"):
		return WorkflowDefinitionValidationScopeCommand
	case strings.HasPrefix(path, "on.runtime_event"):
		return WorkflowDefinitionValidationScopeRuntimeEvent
	case strings.HasPrefix(path, "on.event"):
		return WorkflowDefinitionValidationScopeEvent
	case strings.HasPrefix(path, "on.workflow_call"):
		return WorkflowDefinitionValidationScopeWorkflowCall
	case strings.HasPrefix(path, "jobs"):
		return WorkflowDefinitionValidationScopeJobs
	default:
		return WorkflowDefinitionValidationScopeWorkflow
	}
}

func workflowInspectionValidationCode(
	issue ValidationError,
) WorkflowDefinitionValidationCode {
	path := issue.Path
	message := issue.Message
	switch {
	case path == "jobs" && message == "at least one job is required":
		return WorkflowDefinitionValidationJobsRequired
	case strings.HasPrefix(path, "on.schedule") && message == "cron is required":
		return WorkflowDefinitionValidationScheduleCronRequired
	case strings.HasPrefix(path, "on.schedule") && message == "invalid cron expression":
		return WorkflowDefinitionValidationScheduleCronInvalid
	case strings.HasPrefix(path, "on.workflow_call.inputs") && message == "input name is required":
		return WorkflowDefinitionValidationInputNameRequired
	case (strings.HasPrefix(path, "on.workflow_call.inputs") ||
		strings.HasPrefix(path, "on.command.args")) &&
		message == "unsupported input type":
		return WorkflowDefinitionValidationInputTypeUnsupported
	case strings.HasPrefix(path, "on.workflow_call.inputs") &&
		strings.HasSuffix(path, ".default"):
		return WorkflowDefinitionValidationInputDefaultInvalid
	case strings.HasPrefix(path, "on.workflow_call.outputs") &&
		message == "output value is required":
		return WorkflowDefinitionValidationOutputRequired
	case strings.HasPrefix(path, "on.workflow_call.outputs"):
		return WorkflowDefinitionValidationOutputExpressionInvalid
	case strings.HasPrefix(path, "on.channel_message") &&
		strings.HasSuffix(path, ".text_matches"):
		return WorkflowDefinitionValidationChannelPatternInvalid
	case strings.HasPrefix(path, "on.command") && strings.HasSuffix(path, ".name"):
		return WorkflowDefinitionValidationCommandNameRequired
	case strings.HasPrefix(path, "on.runtime_event") &&
		message == "at least one filter is required":
		return WorkflowDefinitionValidationRuntimeFilterRequired
	case strings.HasPrefix(path, "on.event") &&
		message == "at least one filter is required":
		return WorkflowDefinitionValidationEventFilterRequired
	case strings.HasPrefix(path, "on.event") &&
		message == "at least one entity filter is required":
		return WorkflowDefinitionValidationEventEntityFilterRequired
	case strings.HasPrefix(path, "on.event") &&
		(message == "at least one pattern is required" || message == "pattern is required"):
		return WorkflowDefinitionValidationEventPatternRequired
	case strings.HasPrefix(path, "on.event") &&
		(message == "at least one attribute filter is required" ||
			message == "attribute name is required"):
		return WorkflowDefinitionValidationEventAttributeRequired
	case strings.HasPrefix(path, "jobs") && message == "job id is required":
		return WorkflowDefinitionValidationJobIDRequired
	case strings.HasPrefix(path, "jobs") && strings.HasPrefix(message, "unknown dependency "):
		return WorkflowDefinitionValidationJobDependencyUnknown
	case strings.HasPrefix(path, "jobs") && message == "dependency cycle detected":
		return WorkflowDefinitionValidationJobDependencyCycle
	case strings.HasPrefix(path, "jobs") &&
		!strings.Contains(path, ".steps[") &&
		strings.HasSuffix(path, ".uses"):
		return WorkflowDefinitionValidationReusableTargetInvalid
	case strings.HasPrefix(path, "jobs") &&
		message == "reusable workflow jobs cannot define steps":
		return WorkflowDefinitionValidationReusableStepsUnsupported
	case strings.HasPrefix(path, "jobs") &&
		message == "runs-on is required for step jobs":
		return WorkflowDefinitionValidationJobRunnerRequired
	case strings.HasPrefix(path, "jobs") &&
		message == "at least one step is required":
		return WorkflowDefinitionValidationJobStepsRequired
	case strings.HasPrefix(path, "jobs") && message == "duplicate step id":
		return WorkflowDefinitionValidationStepIDDuplicate
	case strings.HasPrefix(path, "jobs") && message == "uses is required":
		return WorkflowDefinitionValidationStepTargetRequired
	case strings.HasPrefix(path, "jobs") &&
		message == "reusable workflows are only supported at job level":
		return WorkflowDefinitionValidationReusableStepUnsupported
	case strings.HasPrefix(path, "jobs") && message == "unsupported uses target":
		return WorkflowDefinitionValidationStepTargetUnsupported
	case strings.HasSuffix(path, ".conversation.session"):
		return WorkflowDefinitionValidationConversationSession
	case strings.HasSuffix(path, ".conversation.delivery"):
		return WorkflowDefinitionValidationConversationDelivery
	case strings.HasSuffix(path, ".context.session") ||
		strings.HasSuffix(path, ".with.session"):
		return WorkflowDefinitionValidationRunSessionUnsupported
	case strings.HasSuffix(path, ".context.delivery"):
		return WorkflowDefinitionValidationRunDeliveryUnsupported
	case strings.HasSuffix(path, ".with.history"):
		return WorkflowDefinitionValidationAgentHistoryUnsupported
	case strings.HasSuffix(path, ".with.cache"):
		return WorkflowDefinitionValidationAgentCacheUnsupported
	case strings.HasSuffix(path, ".with.tools"):
		return WorkflowDefinitionValidationAgentToolsUnsupported
	default:
		return WorkflowDefinitionValidationDefinitionInvalid
	}
}

func inspectWorkflowJobsFromBytes(raw string) map[string]Job {
	document, err := decodeWorkflowEditorDocument(raw)
	if err != nil {
		return nil
	}
	root, reason := editableWorkflowRoot(document)
	if reason != "" || unsafeWorkflowEditorNodeReason(root) != "" {
		return nil
	}
	jobs, err := decodeWorkflowEditorJobs(root)
	if err != nil {
		return nil
	}
	return jobs
}

type workflowInspectionCountKey struct {
	kind   string
	target string
}

func (inspection *WorkflowDefinitionInspection) projectJobs(jobs map[string]Job) {
	jobIDs := make([]string, 0, len(jobs))
	for id := range jobs {
		jobIDs = append(jobIDs, id)
	}
	sort.Strings(jobIDs)
	if len(jobIDs) > MaxWorkflowInspectionJobs {
		inspection.addLimit(WorkflowDefinitionInspectionLimitJobs)
	}

	dependencyCounts := make(map[workflowInspectionCountKey]int)
	effectCounts := make(map[workflowInspectionCountKey]int)
	stepCount := 0
	dependencyCount := 0
	effectCount := 0
	for jobIndex, id := range jobIDs {
		job := jobs[id]
		includeJob := jobIndex < MaxWorkflowInspectionJobs
		var publicJob WorkflowDefinitionJobInspection
		if includeJob {
			publicID, safeID := safeWorkflowInspectionID(id)
			if !safeID {
				inspection.addLimit(WorkflowDefinitionInspectionLimitUnsafeFields)
			}
			publicJob = WorkflowDefinitionJobInspection{
				ID:    publicID,
				Kind:  WorkflowDefinitionJobSteps,
				Steps: make([]WorkflowDefinitionStepInspection, 0),
			}
		}
		if uses := strings.TrimSpace(job.Uses); uses != "" {
			target := ""
			if canonical, err := CanonicalLocalRef(uses); err == nil {
				if safeWorkflowInspectionText(
					canonical,
					MaxWorkflowInspectionDependencyTargetBytes,
				) {
					target = canonical
				} else {
					inspection.addLimit(WorkflowDefinitionInspectionLimitUnsafeFields)
				}
			} else {
				inspection.addLimit(WorkflowDefinitionInspectionLimitUnsafeFields)
			}
			if includeJob {
				publicJob.Kind = WorkflowDefinitionJobReusable
				publicJob.ReusableTarget = target
			}
			if dependencyCount < MaxWorkflowInspectionEntries && target != "" {
				dependencyCounts[workflowInspectionCountKey{
					kind:   string(WorkflowDependencyKindReusable),
					target: target,
				}]++
				dependencyCount++
			} else if target != "" {
				inspection.addLimit(WorkflowDefinitionInspectionLimitDependencies)
			}
			if effectCount < MaxWorkflowInspectionEntries {
				effectCounts[workflowInspectionCountKey{
					kind:   string(WorkflowDefinitionEffectTransitiveUnknown),
					target: target,
				}]++
				effectCount++
			} else {
				inspection.addLimit(WorkflowDefinitionInspectionLimitEffects)
			}
		}

		for index, step := range job.Steps {
			kind, target, dependencyKind, safeTarget := inspectWorkflowStepTarget(step.Uses)
			if !safeTarget {
				inspection.addLimit(WorkflowDefinitionInspectionLimitUnsafeFields)
			}
			if includeJob && stepCount < MaxWorkflowInspectionEntries {
				stepCount++
				rawStepID := strings.TrimSpace(step.ID)
				publicStepID := safeOptionalWorkflowInspectionText(
					rawStepID,
					MaxWorkflowInspectionIDBytes,
				)
				if rawStepID != "" && publicStepID == "" {
					inspection.addLimit(WorkflowDefinitionInspectionLimitUnsafeFields)
				}
				publicJob.Steps = append(
					publicJob.Steps,
					WorkflowDefinitionStepInspection{
						Index:  index,
						ID:     publicStepID,
						Kind:   kind,
						Target: workflowInspectionPublicStepTarget(kind, target),
					},
				)
			} else if includeJob {
				inspection.addLimit(WorkflowDefinitionInspectionLimitSteps)
			}

			if dependencyKind != "" && target != "" {
				if dependencyCount < MaxWorkflowInspectionEntries {
					dependencyCounts[workflowInspectionCountKey{
						kind:   string(dependencyKind),
						target: target,
					}]++
					dependencyCount++
				} else {
					inspection.addLimit(WorkflowDefinitionInspectionLimitDependencies)
				}
			}
			effectKind := WorkflowDefinitionEffectKind("")
			if strings.TrimSpace(step.Uses) != "" {
				effectKind = workflowInspectionEffectKind(kind)
			}
			if effectKind != "" {
				if effectCount < MaxWorkflowInspectionEntries {
					effectCounts[workflowInspectionCountKey{
						kind:   string(effectKind),
						target: target,
					}]++
					effectCount++
				} else {
					inspection.addLimit(WorkflowDefinitionInspectionLimitEffects)
				}
			}
		}
		if includeJob {
			inspection.Jobs = append(inspection.Jobs, publicJob)
		}
	}
	inspection.Dependencies = workflowInspectionDependencies(dependencyCounts)
	inspection.Effects = workflowInspectionEffects(effectCounts)
}

func inspectWorkflowStepTarget(
	raw string,
) (WorkflowDefinitionStepKind, string, WorkflowDependencyKind, bool) {
	uses := strings.TrimSpace(raw)
	if uses == "" {
		return WorkflowDefinitionStepUnknown, "", "", true
	}
	kind, target, ok := workflowStepDependency(uses)
	if !ok {
		if !safeWorkflowInspectionText(
			uses,
			MaxWorkflowInspectionStepTargetBytes,
		) {
			return WorkflowDefinitionStepUnknown, "", "", false
		}
		return WorkflowDefinitionStepUnknown, uses, "", true
	}
	var stepKind WorkflowDefinitionStepKind
	switch kind {
	case WorkflowDependencyKindAgent:
		stepKind = WorkflowDefinitionStepAgent
	case WorkflowDependencyKindTool:
		stepKind = WorkflowDefinitionStepTool
	case WorkflowDependencyKindMCP:
		stepKind = WorkflowDefinitionStepMCP
	case WorkflowDependencyKindFunction:
		stepKind = WorkflowDefinitionStepFunction
	default:
		return WorkflowDefinitionStepUnknown, "", "", true
	}
	if !safeWorkflowInspectionText(
		target,
		MaxWorkflowInspectionStepTargetBytes-len("function/"),
	) {
		return stepKind, "", kind, false
	}
	return stepKind, target, kind, true
}

func workflowInspectionPublicStepTarget(
	kind WorkflowDefinitionStepKind,
	target string,
) string {
	if target == "" {
		return ""
	}
	if kind == WorkflowDefinitionStepUnknown {
		return target
	}
	return string(kind) + "/" + target
}

func workflowInspectionEffectKind(
	kind WorkflowDefinitionStepKind,
) WorkflowDefinitionEffectKind {
	switch kind {
	case WorkflowDefinitionStepAgent:
		return WorkflowDefinitionEffectModelOrDelegatedAction
	case WorkflowDefinitionStepTool, WorkflowDefinitionStepFunction:
		return WorkflowDefinitionEffectStateChange
	case WorkflowDefinitionStepMCP:
		return WorkflowDefinitionEffectExternalStateChange
	case WorkflowDefinitionStepUnknown:
		return WorkflowDefinitionEffectUnclassifiedAction
	default:
		return ""
	}
}

func workflowInspectionDependencies(
	counts map[workflowInspectionCountKey]int,
) []WorkflowDefinitionDependencyInspection {
	keys := sortedWorkflowInspectionCountKeys(counts)
	out := make([]WorkflowDefinitionDependencyInspection, 0, len(keys))
	for _, key := range keys {
		out = append(out, WorkflowDefinitionDependencyInspection{
			Kind:        WorkflowDependencyKind(key.kind),
			Target:      key.target,
			Occurrences: counts[key],
		})
	}
	return out
}

func workflowInspectionEffects(
	counts map[workflowInspectionCountKey]int,
) []WorkflowDefinitionEffectInspection {
	keys := sortedWorkflowInspectionCountKeys(counts)
	out := make([]WorkflowDefinitionEffectInspection, 0, len(keys))
	for _, key := range keys {
		out = append(out, WorkflowDefinitionEffectInspection{
			Kind:        WorkflowDefinitionEffectKind(key.kind),
			Target:      key.target,
			Occurrences: counts[key],
		})
	}
	return out
}

func sortedWorkflowInspectionCountKeys(
	counts map[workflowInspectionCountKey]int,
) []workflowInspectionCountKey {
	keys := make([]workflowInspectionCountKey, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].kind != keys[j].kind {
			return keys[i].kind < keys[j].kind
		}
		return keys[i].target < keys[j].target
	})
	return keys
}

func (inspection *WorkflowDefinitionInspection) addLimit(
	code WorkflowDefinitionInspectionLimitCode,
) {
	for _, existing := range inspection.Limits {
		if existing == code {
			return
		}
	}
	inspection.Complete = false
	inspection.Limits = append(inspection.Limits, code)
	sort.Slice(inspection.Limits, func(i, j int) bool {
		return inspection.Limits[i] < inspection.Limits[j]
	})
}

func safeWorkflowInspectionID(value string) (string, bool) {
	if safeWorkflowInspectionText(value, MaxWorkflowInspectionIDBytes) {
		return value, true
	}
	return "unavailable", false
}

func safeOptionalWorkflowInspectionText(value string, maxBytes int) string {
	if value == "" || !safeWorkflowInspectionText(value, maxBytes) {
		return ""
	}
	return value
}

func safeWorkflowInspectionTemplateName(value string) bool {
	if !safeWorkflowInspectionText(value, 256) {
		return false
	}
	for index, r := range value {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if index > 0 && ((r >= '0' && r <= '9') || r == '-' || r == '_') {
			continue
		}
		return false
	}
	return true
}

func safeWorkflowInspectionText(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}
