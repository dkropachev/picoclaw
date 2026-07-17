package workflows

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/adhocore/gronx"
)

type ValidationError struct {
	Path    string
	Message string
}

type ValidationErrors []ValidationError

func (e ValidationErrors) Error() string {
	if len(e) == 0 {
		return ""
	}
	parts := make([]string, 0, len(e))
	for _, item := range e {
		if item.Path == "" {
			parts = append(parts, item.Message)
			continue
		}
		parts = append(parts, item.Path+": "+item.Message)
	}
	return strings.Join(parts, "; ")
}

func Validate(workflow *Workflow) error {
	var errs ValidationErrors
	if workflow == nil {
		return ValidationErrors{{Message: "workflow is required"}}
	}
	if len(workflow.Jobs) == 0 {
		errs = append(errs, ValidationError{Path: "jobs", Message: "at least one job is required"})
	}
	errs = append(errs, validateScheduleTriggers(workflow.On.Schedule)...)
	errs = append(errs, validateWorkflowCall(workflow.On.WorkflowCall)...)
	errs = append(errs, validateChannelTrigger("on.channel_message", workflow.On.ChannelMessage)...)
	errs = append(errs, validateCommandTrigger("on.command", workflow.On.Command)...)
	errs = append(errs, validateRuntimeEventTrigger("on.runtime_event", workflow.On.RuntimeEvent)...)
	errs = append(errs, validateJobs(workflow.Jobs)...)
	if len(errs) > 0 {
		sort.SliceStable(errs, func(i, j int) bool {
			if errs[i].Path != errs[j].Path {
				return errs[i].Path < errs[j].Path
			}
			return errs[i].Message < errs[j].Message
		})
		return errs
	}
	return nil
}

func validateScheduleTriggers(schedules []ScheduleTrigger) ValidationErrors {
	var errs ValidationErrors
	for i, schedule := range schedules {
		path := fmt.Sprintf("on.schedule[%d].cron", i)
		cron := strings.TrimSpace(schedule.Cron)
		if cron == "" {
			errs = append(errs, ValidationError{Path: path, Message: "cron is required"})
			continue
		}
		if !gronx.IsValid(cron) {
			errs = append(errs, ValidationError{Path: path, Message: "invalid cron expression"})
		}
	}
	return errs
}

func validateWorkflowCall(call *WorkflowCall) ValidationErrors {
	if call == nil {
		return nil
	}
	var errs ValidationErrors
	for name, input := range call.Inputs {
		path := "on.workflow_call.inputs." + name
		if strings.TrimSpace(name) == "" {
			errs = append(errs, ValidationError{Path: path, Message: "input name is required"})
		}
		if !validInputType(input.Type) {
			errs = append(errs, ValidationError{Path: path + ".type", Message: "unsupported input type"})
		}
	}
	for name, output := range call.Outputs {
		if strings.TrimSpace(output.Value) == "" {
			errs = append(
				errs,
				ValidationError{
					Path:    "on.workflow_call.outputs." + name + ".value",
					Message: "output value is required",
				},
			)
		}
	}
	return errs
}

func validateChannelTrigger(path string, trigger *ChannelMessageTrigger) ValidationErrors {
	if trigger == nil {
		return nil
	}
	var errs ValidationErrors
	errs = append(errs, validateConversation(path+".conversation", trigger.Conversation)...)
	if strings.TrimSpace(trigger.TextMatches) != "" {
		if _, err := regexp.Compile(trigger.TextMatches); err != nil {
			errs = append(
				errs,
				ValidationError{Path: path + ".text_matches", Message: fmt.Sprintf("invalid regex: %v", err)},
			)
		}
	}
	return errs
}

func validateCommandTrigger(path string, trigger *CommandTrigger) ValidationErrors {
	if trigger == nil {
		return nil
	}
	var errs ValidationErrors
	if strings.TrimSpace(trigger.Name) == "" {
		errs = append(errs, ValidationError{Path: path + ".name", Message: "command name is required"})
	}
	for name, input := range trigger.Args {
		if !validInputType(input.Type) {
			errs = append(
				errs,
				ValidationError{Path: path + ".args." + name + ".type", Message: "unsupported input type"},
			)
		}
	}
	errs = append(errs, validateConversation(path+".conversation", trigger.Conversation)...)
	return errs
}

func validateRuntimeEventTrigger(path string, trigger *RuntimeEventTrigger) ValidationErrors {
	if trigger == nil {
		return nil
	}
	var errs ValidationErrors
	if len(trigger.Kinds) == 0 && len(trigger.Sources) == 0 && len(trigger.Agents) == 0 &&
		len(trigger.Sessions) == 0 && len(trigger.Channels) == 0 && len(trigger.Chats) == 0 {
		errs = append(errs, ValidationError{Path: path, Message: "at least one filter is required"})
	}
	return errs
}

func validateJobs(jobs map[string]Job) ValidationErrors {
	var errs ValidationErrors
	for id, job := range jobs {
		jobPath := "jobs." + id
		if strings.TrimSpace(id) == "" {
			errs = append(errs, ValidationError{Path: "jobs", Message: "job id is required"})
		}
		for _, dep := range job.Needs {
			if _, ok := jobs[dep]; !ok {
				errs = append(
					errs,
					ValidationError{Path: jobPath + ".needs", Message: fmt.Sprintf("unknown dependency %q", dep)},
				)
			}
		}
		if strings.TrimSpace(job.Uses) != "" {
			if _, err := CanonicalLocalRef(job.Uses); err != nil {
				errs = append(errs, ValidationError{Path: jobPath + ".uses", Message: err.Error()})
			}
			if len(job.Steps) > 0 {
				errs = append(
					errs,
					ValidationError{Path: jobPath + ".steps", Message: "reusable workflow jobs cannot define steps"},
				)
			}
			continue
		}
		if strings.TrimSpace(job.RunsOn) == "" {
			errs = append(
				errs,
				ValidationError{Path: jobPath + ".runs-on", Message: "runs-on is required for step jobs"},
			)
		}
		if len(job.Steps) == 0 {
			errs = append(errs, ValidationError{Path: jobPath + ".steps", Message: "at least one step is required"})
		}
		errs = append(errs, validateRunContext(jobPath+".context", job.Context)...)
		errs = append(errs, validateSteps(jobPath+".steps", job.Steps)...)
	}
	errs = append(errs, validateJobCycles(jobs)...)
	return errs
}

func validateSteps(path string, steps []Step) ValidationErrors {
	var errs ValidationErrors
	seenIDs := make(map[string]struct{})
	for i, step := range steps {
		stepPath := fmt.Sprintf("%s[%d]", path, i)
		if strings.TrimSpace(step.ID) != "" {
			if _, exists := seenIDs[step.ID]; exists {
				errs = append(errs, ValidationError{Path: stepPath + ".id", Message: "duplicate step id"})
			}
			seenIDs[step.ID] = struct{}{}
		}
		uses := strings.TrimSpace(step.Uses)
		if uses == "" {
			errs = append(errs, ValidationError{Path: stepPath + ".uses", Message: "uses is required"})
			continue
		}
		if strings.HasPrefix(strings.TrimPrefix(uses, "./"), "workflows/") {
			errs = append(
				errs,
				ValidationError{
					Path:    stepPath + ".uses",
					Message: "reusable workflows are only supported at job level",
				},
			)
			continue
		}
		if !validStepUses(uses) {
			errs = append(errs, ValidationError{Path: stepPath + ".uses", Message: "unsupported uses target"})
		}
		errs = append(errs, validateRunContext(stepPath+".context", step.Context)...)
		if strings.HasPrefix(uses, "agent/") {
			errs = append(errs, validateAgentStepOptions(stepPath+".with", step.With)...)
		}
	}
	return errs
}

func validateJobCycles(jobs map[string]Job) ValidationErrors {
	const (
		unvisited = 0
		visiting  = 1
		done      = 2
	)
	state := make(map[string]int, len(jobs))
	var errs ValidationErrors
	var visit func(string, []string)
	visit = func(id string, stack []string) {
		switch state[id] {
		case visiting:
			errs = append(errs, ValidationError{Path: "jobs." + id + ".needs", Message: "dependency cycle detected"})
			return
		case done:
			return
		}
		state[id] = visiting
		for _, dep := range jobs[id].Needs {
			if _, ok := jobs[dep]; ok {
				visit(dep, append(stack, id))
			}
		}
		state[id] = done
	}
	ids := make([]string, 0, len(jobs))
	for id := range jobs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if state[id] == unvisited {
			visit(id, nil)
		}
	}
	return errs
}

func validInputType(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "string", "number", "boolean", "object", "array":
		return true
	default:
		return false
	}
}

func validateConversation(path string, spec ConversationSpec) ValidationErrors {
	var errs ValidationErrors
	if !validConversationSession(spec.Session) {
		errs = append(errs, ValidationError{Path: path + ".session", Message: "unsupported session mode"})
	}
	if !validConversationDelivery(spec.Delivery) {
		errs = append(errs, ValidationError{Path: path + ".delivery", Message: "unsupported delivery mode"})
	}
	return errs
}

func validateRunContext(path string, ctx RunContext) ValidationErrors {
	var errs ValidationErrors
	if !validRunSession(ctx.Session) {
		errs = append(errs, ValidationError{Path: path + ".session", Message: "unsupported session context"})
	}
	if !validRunDelivery(ctx.Delivery) {
		errs = append(errs, ValidationError{Path: path + ".delivery", Message: "unsupported delivery context"})
	}
	return errs
}

func validConversationSession(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "discussion", "sender", "global":
		return true
	default:
		return false
	}
}

func validConversationDelivery(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "same_discussion", "none":
		return true
	default:
		return false
	}
}

func validRunSession(value string) bool {
	value = strings.TrimSpace(value)
	return value == "" || value == "inherit" || strings.HasPrefix(value, "key:")
}

func validRunDelivery(value string) bool {
	value = strings.TrimSpace(value)
	return value == "" || value == "inherit" || value == "none"
}

func validStepUses(value string) bool {
	for _, prefix := range []string{"agent/", "tool/", "mcp/", "function/"} {
		if strings.HasPrefix(value, prefix) && strings.TrimSpace(strings.TrimPrefix(value, prefix)) != "" {
			return true
		}
	}
	return false
}

func validateAgentStepOptions(path string, with map[string]any) ValidationErrors {
	var errs ValidationErrors
	if with == nil {
		return nil
	}
	if value, ok := stringOption(with, "history"); ok && !validHistoryMode(value) {
		errs = append(errs, ValidationError{Path: path + ".history", Message: "unsupported history mode"})
	}
	if value, ok := stringOption(with, "cache"); ok && !validCacheMode(value) {
		errs = append(errs, ValidationError{Path: path + ".cache", Message: "unsupported cache mode"})
	}
	if value, ok := stringOption(with, "session"); ok && !validRunSession(value) {
		errs = append(errs, ValidationError{Path: path + ".session", Message: "unsupported session context"})
	}
	return errs
}

func stringOption(values map[string]any, key string) (string, bool) {
	value, ok := values[key]
	if !ok || value == nil {
		return "", false
	}
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(text), true
}

func validHistoryMode(value string) bool {
	switch value {
	case "", "read_write", "read_only", "none":
		return true
	default:
		return false
	}
}

func validCacheMode(value string) bool {
	switch {
	case value == "", value == "session", value == "agent", value == "none":
		return true
	case strings.HasPrefix(value, "key:") && strings.TrimSpace(strings.TrimPrefix(value, "key:")) != "":
		return true
	default:
		return false
	}
}
