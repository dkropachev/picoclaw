package workflows

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/adhocore/gronx"
	"github.com/sipeed/picoclaw/pkg/routing"
)

type ValidationError struct {
	Path    string
	Message string
}

type ValidationErrors []ValidationError

var (
	expressionReferencePattern = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_-]*(?:\.[A-Za-z0-9_-]+)*`)
	expressionPathPattern      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*(?:\.[A-Za-z0-9_-]+)*$`)
)

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
	errs = append(errs, validateWorkflowCall(workflow.On.WorkflowCall, workflow.Jobs)...)
	errs = append(errs, validateChannelTrigger("on.channel_message", workflow.On.ChannelMessage)...)
	errs = append(errs, validateCommandTrigger("on.command", workflow.On.Command)...)
	errs = append(errs, validateRuntimeEventTrigger("on.runtime_event", workflow.On.RuntimeEvent)...)
	errs = append(errs, validateEventTrigger("on.event", workflow.On.Event)...)
	errs = append(errs, validateJobs(workflow.Jobs)...)
	errs = append(errs, validateHumanTaskWorkflow(workflow)...)
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

func validateWorkflowCall(call *WorkflowCall, jobs map[string]Job) ValidationErrors {
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
		if input.Default != nil {
			if err := validateWorkflowInputValue(name, input.Type, input.Default); err != nil {
				errs = append(errs, ValidationError{Path: path + ".default", Message: err.Error()})
			}
		}
	}
	for name, output := range call.Outputs {
		path := "on.workflow_call.outputs." + name + ".value"
		if strings.TrimSpace(output.Value) == "" {
			errs = append(
				errs,
				ValidationError{
					Path:    path,
					Message: "output value is required",
				},
			)
			continue
		}
		errs = append(errs, validateWorkflowOutputExpression(path, output.Value, jobs)...)
	}
	return errs
}

func validateWorkflowOutputExpression(path string, value string, jobs map[string]Job) ValidationErrors {
	var errs ValidationErrors
	for _, expr := range outputExpressionBodies(value) {
		if err := validateExpressionSyntax(expr); err != nil {
			errs = append(errs, ValidationError{Path: path, Message: err.Error()})
			continue
		}
		for _, ref := range expressionReferenceTokens(expr) {
			parts := strings.Split(ref, ".")
			if len(parts) == 0 {
				continue
			}
			switch parts[0] {
			case "inputs", "secrets", "event", "delivery", "session":
				continue
			case "jobs":
				if len(parts) < 2 || strings.TrimSpace(parts[1]) == "" {
					errs = append(errs, ValidationError{Path: path, Message: "job id is required"})
					continue
				}
				job, ok := jobs[parts[1]]
				if !ok {
					errs = append(
						errs,
						ValidationError{Path: path, Message: fmt.Sprintf("unknown job %q", parts[1])},
					)
					continue
				}
				if len(parts) < 3 {
					continue
				}
				switch parts[2] {
				case "outputs":
					if len(parts) >= 4 && strings.TrimSpace(job.Uses) == "" {
						if _, ok := job.Outputs[parts[3]]; !ok {
							errs = append(
								errs,
								ValidationError{
									Path:    path,
									Message: fmt.Sprintf("unknown job output %q on job %q", parts[3], parts[1]),
								},
							)
						}
					}
				case "status", "error":
				default:
					errs = append(
						errs,
						ValidationError{
							Path:    path,
							Message: fmt.Sprintf("unsupported job field %q", parts[2]),
						},
					)
				}
			case "steps", "needs":
				errs = append(
					errs,
					ValidationError{
						Path:    path,
						Message: fmt.Sprintf("workflow outputs cannot reference %q", parts[0]),
					},
				)
			default:
				errs = append(
					errs,
					ValidationError{
						Path:    path,
						Message: fmt.Sprintf("unknown expression root %q", parts[0]),
					},
				)
			}
		}
	}
	return errs
}

func validateExpressionSyntax(expr string) error {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return fmt.Errorf("expression is empty")
	}
	for _, op := range []string{" == ", " != ", " >= ", " <= ", " > ", " < "} {
		if idx := strings.Index(expr, op); idx >= 0 {
			if err := validateExpressionSyntax(expr[:idx]); err != nil {
				return err
			}
			return validateExpressionSyntax(expr[idx+len(op):])
		}
	}
	if strings.HasPrefix(expr, "not ") {
		return validateExpressionSyntax(strings.TrimSpace(strings.TrimPrefix(expr, "not ")))
	}
	if isQuotedExpressionLiteral(expr) {
		return nil
	}
	switch expr {
	case "true", "false", "null":
		return nil
	}
	if _, err := strconv.ParseFloat(expr, 64); err == nil {
		return nil
	}
	if expressionPathPattern.MatchString(expr) {
		return nil
	}
	return fmt.Errorf("unsupported expression syntax %q", expr)
}

func isQuotedExpressionLiteral(expr string) bool {
	if len(expr) < 2 {
		return false
	}
	return (strings.HasPrefix(expr, "'") && strings.HasSuffix(expr, "'")) ||
		(strings.HasPrefix(expr, `"`) && strings.HasSuffix(expr, `"`))
}

func outputExpressionBodies(value string) []string {
	matches := expressionPattern.FindAllStringSubmatch(value, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) > 1 {
			out = append(out, strings.TrimSpace(match[1]))
		}
	}
	return out
}

func expressionReferenceTokens(expr string) []string {
	expr = stripExpressionStringLiterals(expr)
	matches := expressionReferencePattern.FindAllString(expr, -1)
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		switch match {
		case "true", "false", "null", "not":
			continue
		}
		out = append(out, match)
	}
	return out
}

func stripExpressionStringLiterals(expr string) string {
	var b strings.Builder
	var quote rune
	escaped := false
	for _, r := range expr {
		if quote != 0 {
			if escaped {
				escaped = false
				b.WriteRune(' ')
				continue
			}
			if r == '\\' {
				escaped = true
				b.WriteRune(' ')
				continue
			}
			if r == quote {
				quote = 0
			}
			b.WriteRune(' ')
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
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
		errs = append(errs, validateRunContext(jobPath+".context", job.Context)...)
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
		stepID := strings.TrimSpace(step.ID)
		if stepID == "" {
			stepID = fmt.Sprintf("step_%d", i+1)
		}
		if _, exists := seenIDs[stepID]; exists {
			errs = append(errs, ValidationError{Path: stepPath + ".id", Message: "duplicate step id"})
		}
		seenIDs[stepID] = struct{}{}
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
			errs = append(errs, validateAgentStepOptions(
				stepPath+".with",
				strings.TrimPrefix(uses, "agent/"),
				step.With,
			)...)
		}
		if uses == "human/task" {
			errs = append(errs, validateHumanTaskStep(stepPath, step)...)
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
	if value == "human/task" {
		return true
	}
	for _, prefix := range []string{"agent/", "tool/", "mcp/", "function/"} {
		if strings.HasPrefix(value, prefix) && strings.TrimSpace(strings.TrimPrefix(value, prefix)) != "" {
			return true
		}
	}
	return false
}

func validateHumanTaskWorkflow(workflow *Workflow) ValidationErrors {
	if workflow == nil || !workflowContainsHumanTask(workflow) {
		return nil
	}
	var errs ValidationErrors
	if workflow.On.Event != nil {
		errs = append(errs, ValidationError{
			Path:    "on.event",
			Message: "human/task is not supported for durable event workflows",
		})
	}
	if workflow.On.WorkflowCall != nil {
		errs = append(errs, ValidationError{
			Path:    "on.workflow_call",
			Message: "human/task is not supported in reusable workflow definitions",
		})
	}
	for jobID, job := range workflow.Jobs {
		if strings.TrimSpace(job.Uses) != "" {
			errs = append(errs, ValidationError{
				Path:    "jobs." + jobID + ".uses",
				Message: "workflows containing human/task cannot call reusable workflows",
			})
		}
	}
	return errs
}

func workflowContainsHumanTask(workflow *Workflow) bool {
	if workflow == nil {
		return false
	}
	for _, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if strings.TrimSpace(step.Uses) == "human/task" {
				return true
			}
		}
	}
	return false
}

func validateHumanTaskStep(path string, step Step) ValidationErrors {
	var errs ValidationErrors
	if step.ContinueOnError {
		errs = append(errs, ValidationError{
			Path:    path + ".continue-on-error",
			Message: "human/task cannot continue on error",
		})
	}
	allowed := map[string]bool{
		"title": true, "questions": true, "response_schema": true, "input_hash": true,
	}
	for key, value := range step.With {
		if !allowed[key] {
			errs = append(errs, ValidationError{Path: path + ".with." + key, Message: "unsupported human/task option"})
		}
		if workflowValueReferencesSecrets(value) {
			errs = append(errs, ValidationError{
				Path:    path + ".with." + key,
				Message: "human/task values cannot reference secrets",
			})
		}
	}
	title, titleExists := step.With["title"]
	if !titleExists {
		errs = append(errs, ValidationError{Path: path + ".with.title", Message: "title is required"})
	} else if value, ok := title.(string); !ok {
		errs = append(errs, ValidationError{Path: path + ".with.title", Message: "title must be a string"})
	} else if strings.TrimSpace(value) == "" || !utf8.ValidString(value) || len(value) > MaxHumanTaskTitleBytes {
		errs = append(errs, ValidationError{
			Path: path + ".with.title",
			Message: fmt.Sprintf(
				"title must be nonblank valid UTF-8 and at most %d bytes",
				MaxHumanTaskTitleBytes,
			),
		})
	}
	if questions, exists := step.With["questions"]; !exists || questions == nil {
		errs = append(errs, ValidationError{Path: path + ".with.questions", Message: "questions are required"})
	}
	if raw, exists := step.With["response_schema"]; exists && raw != nil {
		schema, err := normalizeSchemaMap(raw)
		if err != nil {
			errs = append(errs, ValidationError{
				Path: path + ".with.response_schema", Message: "response_schema must be an object",
			})
		} else if err := validateHumanTaskResponseSchema(schema, 0); err != nil {
			errs = append(errs, ValidationError{Path: path + ".with.response_schema", Message: err.Error()})
		}
	}
	if raw, exists := step.With["input_hash"]; exists {
		if value, ok := raw.(string); !ok {
			errs = append(errs, ValidationError{
				Path: path + ".with.input_hash", Message: "input_hash must be a string",
			})
		} else if !utf8.ValidString(value) || len(strings.TrimSpace(value)) > MaxHumanTaskInputHashBytes {
			errs = append(errs, ValidationError{
				Path: path + ".with.input_hash",
				Message: fmt.Sprintf(
					"input_hash must be valid UTF-8 and at most %d bytes",
					MaxHumanTaskInputHashBytes,
				),
			})
		}
	}
	if encoded, err := json.Marshal(step.With); err != nil || len(encoded) > MaxHumanTaskPayloadBytes {
		errs = append(errs, ValidationError{Path: path + ".with", Message: "human/task payload is too large"})
	}
	return errs
}

func validateHumanTaskResponseSchema(schema map[string]any, depth int) error {
	if depth > 32 {
		return errors.New("response_schema exceeds maximum nesting depth")
	}
	allowed := map[string]bool{
		"type": true, "enum": true, "required": true, "properties": true,
		"additionalProperties": true, "items": true,
	}
	for keyword := range schema {
		if !allowed[keyword] {
			return fmt.Errorf("unsupported response_schema keyword %q", keyword)
		}
	}
	typeName := ""
	if rawType, exists := schema["type"]; exists {
		value, ok := rawType.(string)
		if !ok {
			return errors.New("response_schema type must be a string")
		}
		typeName = strings.ToLower(strings.TrimSpace(value))
		switch typeName {
		case "object", "array", "string", "integer", "number", "boolean", "null":
		default:
			return fmt.Errorf("unsupported response_schema type %q", value)
		}
	}
	if rawEnum, exists := schema["enum"]; exists {
		length := 0
		switch values := rawEnum.(type) {
		case []any:
			length = len(values)
		case []string:
			length = len(values)
		}
		if length == 0 {
			return errors.New("response_schema enum must be a nonempty array")
		}
		if _, err := json.Marshal(rawEnum); err != nil {
			return errors.New("response_schema enum is invalid")
		}
	}
	if rawRequired, exists := schema["required"]; exists {
		if typeName != "object" {
			return errors.New("response_schema required needs type object")
		}
		var values []string
		switch typed := rawRequired.(type) {
		case []any:
			values = make([]string, 0, len(typed))
			for _, item := range typed {
				name, ok := item.(string)
				if !ok {
					return errors.New("response_schema required must be an array of unique nonblank strings")
				}
				values = append(values, name)
			}
		case []string:
			values = append([]string(nil), typed...)
		default:
			return errors.New("response_schema required must be an array of unique nonblank strings")
		}
		seen := make(map[string]bool, len(values))
		for _, item := range values {
			name := item
			name = strings.TrimSpace(name)
			if name == "" || seen[name] {
				return errors.New("response_schema required must be an array of unique nonblank strings")
			}
			seen[name] = true
		}
	}
	if rawProperties, exists := schema["properties"]; exists {
		if typeName != "object" {
			return errors.New("response_schema properties needs type object")
		}
		properties, err := normalizeSchemaMap(rawProperties)
		if err != nil {
			return errors.New("response_schema properties must be an object")
		}
		for name, rawChild := range properties {
			if strings.TrimSpace(name) == "" {
				return errors.New("response_schema property names must be nonblank")
			}
			child, err := normalizeSchemaMap(rawChild)
			if err != nil {
				return fmt.Errorf("response_schema property %q must be an object", name)
			}
			if err := validateHumanTaskResponseSchema(child, depth+1); err != nil {
				return err
			}
		}
	}
	if rawAdditional, exists := schema["additionalProperties"]; exists {
		if typeName != "object" {
			return errors.New("response_schema additionalProperties needs type object")
		}
		if _, ok := rawAdditional.(bool); !ok {
			child, err := normalizeSchemaMap(rawAdditional)
			if err != nil {
				return errors.New("response_schema additionalProperties must be a boolean or object")
			}
			if err := validateHumanTaskResponseSchema(child, depth+1); err != nil {
				return err
			}
		}
	}
	if rawItems, exists := schema["items"]; exists {
		if typeName != "array" {
			return errors.New("response_schema items needs type array")
		}
		child, err := normalizeSchemaMap(rawItems)
		if err != nil {
			return errors.New("response_schema items must be an object")
		}
		if err := validateHumanTaskResponseSchema(child, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func workflowValueReferencesSecrets(value any) bool {
	switch typed := value.(type) {
	case string:
		for _, match := range expressionPattern.FindAllStringSubmatch(typed, -1) {
			if len(match) <= 1 {
				continue
			}
			for _, ref := range expressionReferenceTokens(match[1]) {
				if ref == "secrets" || strings.HasPrefix(ref, "secrets.") {
					return true
				}
			}
		}
	case map[string]any:
		for _, item := range typed {
			if workflowValueReferencesSecrets(item) {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if workflowValueReferencesSecrets(item) {
				return true
			}
		}
	}
	return false
}

func validateAgentStepOptions(path, agentID string, with map[string]any) ValidationErrors {
	var errs ValidationErrors
	if with == nil {
		return nil
	}
	history, historySet, historyString := agentStringOption(with, "history")
	if historySet && !historyString {
		errs = append(errs, ValidationError{Path: path + ".history", Message: "history mode must be a string"})
	} else if historyString && !validHistoryMode(history) {
		errs = append(errs, ValidationError{Path: path + ".history", Message: "unsupported history mode"})
	}
	cache, cacheSet, cacheString := agentStringOption(with, "cache")
	if cacheSet && !cacheString {
		errs = append(errs, ValidationError{Path: path + ".cache", Message: "cache mode must be a string"})
	} else if cacheString && !validCacheMode(cache) {
		errs = append(errs, ValidationError{Path: path + ".cache", Message: "unsupported cache mode"})
	}
	sessionMode, sessionSet, sessionString := agentStringOption(with, "session")
	if sessionSet && !sessionString {
		errs = append(errs, ValidationError{Path: path + ".session", Message: "session context must be a string"})
	} else if sessionString && !validRunSession(sessionMode) {
		errs = append(errs, ValidationError{Path: path + ".session", Message: "unsupported session context"})
	}
	toolsMode := AgentToolsInherit
	if raw, exists := with["tools"]; exists {
		value, ok := raw.(string)
		toolsMode = strings.TrimSpace(value)
		if !ok || !validAgentToolsMode(toolsMode) {
			errs = append(errs, ValidationError{Path: path + ".tools", Message: "unsupported tools mode"})
		}
	}
	if history == "read_only" && toolsMode != AgentToolsNone {
		errs = append(errs, ValidationError{
			Path:    path + ".tools",
			Message: "read_only history requires tools: none",
		})
	}
	if history == "read_only" && !routing.IsCanonicalAgentID(strings.TrimSpace(agentID)) {
		errs = append(errs, ValidationError{
			Path:    strings.TrimSuffix(path, ".with") + ".uses",
			Message: "read_only history requires an exact canonical agent ID",
		})
	}
	return errs
}

func agentStringOption(values map[string]any, key string) (string, bool, bool) {
	value, exists := values[key]
	if !exists {
		return "", false, true
	}
	text, ok := value.(string)
	if !ok {
		return "", true, false
	}
	return strings.TrimSpace(text), true, true
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

func validAgentToolsMode(value string) bool {
	return value == AgentToolsInherit || value == AgentToolsNone
}
