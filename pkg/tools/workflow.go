package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

const WorkflowToolName = "workflow"

type WorkflowTool struct {
	executor       *workflows.Executor
	workspace      string
	runtime        workflows.RuntimeCompatibility
	definitionsDir string
}

func NewWorkflowTool(
	executor *workflows.Executor,
	workspace string,
	runtime ...workflows.RuntimeCompatibility,
) *WorkflowTool {
	var compatibility workflows.RuntimeCompatibility
	if len(runtime) > 0 {
		compatibility = runtime[0]
	}
	compatibility = workflows.NormalizeRuntimeCompatibility(compatibility)
	tool := &WorkflowTool{
		executor:       executor,
		workspace:      strings.TrimSpace(workspace),
		runtime:        compatibility,
		definitionsDir: workflows.DefaultDefinitionsDir,
	}
	if executor != nil && len(runtime) > 0 {
		executor.RuntimeCompatibility = compatibility
	}
	if executor != nil && strings.TrimSpace(executor.DefinitionsDir) != "" {
		tool.definitionsDir = executor.DefinitionsDir
	}
	return tool
}

func (t *WorkflowTool) Name() string {
	return WorkflowToolName
}

func (t *WorkflowTool) Description() string {
	return "List, validate, run, inspect, and develop reusable PicoClaw workflows from the workspace."
}

func (t *WorkflowTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"description": "Workflow action to perform.",
				"enum": []string{
					"list",
					"compatibility",
					"revalidate",
					"validate",
					"reload",
					"run",
					"status",
					"events",
					"graph",
					"cancel",
					"retry",
					"dev_status",
					"dev_start",
					"dev_revise",
					"dev_validate",
					"dev_test",
					"dev_publish",
					"dev_discard",
				},
				"default": "list",
			},
			"ref": map[string]any{
				"type":        "string",
				"description": "Canonical workflow ref such as workflows/summarize-text.yml.",
			},
			"run_id": map[string]any{
				"type":        "string",
				"description": "Workflow run ID for status, events, graph, cancel, or retry.",
			},
			"inputs": map[string]any{
				"type":                 "object",
				"description":          "Workflow inputs for run.",
				"additionalProperties": true,
			},
			"secrets": map[string]any{
				"type":                 "object",
				"description":          "Workflow secrets for run or retry.",
				"additionalProperties": map[string]any{"type": "string"},
			},
			"reason": map[string]any{
				"type":        "string",
				"description": "Optional cancellation reason or workflow development reason.",
			},
			"prompt": map[string]any{
				"type":        "string",
				"description": "Workflow development brief or revision instruction.",
			},
			"target_ref": map[string]any{
				"type":        "string",
				"description": "Target workflow ref for workflow development.",
			},
			"yaml": map[string]any{
				"type":        "string",
				"description": "Complete draft workflow YAML for development revise or test actions.",
			},
			"regenerate": map[string]any{
				"type":        "boolean",
				"description": "Regenerate a development draft from the current prompt.",
			},
		},
	}
}

func (t *WorkflowTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	if t == nil || t.executor == nil {
		return ErrorResult("workflow executor not configured")
	}
	action, _ := args["action"].(string)
	action = strings.ToLower(strings.TrimSpace(action))
	if action == "" {
		action = "list"
	}
	switch action {
	case "list":
		return t.list(ctx)
	case "compatibility":
		return t.compatibility(ctx)
	case "revalidate":
		return t.revalidate(ctx)
	case "validate":
		return t.validate(ctx, workflowStringArg(args, "ref"))
	case "reload":
		return t.reload(ctx)
	case "run":
		return t.run(ctx, args)
	case "status":
		return t.status(ctx, workflowStringArg(args, "run_id"))
	case "events":
		return t.events(ctx, workflowStringArg(args, "run_id"))
	case "graph":
		return t.graph(ctx, workflowStringArg(args, "run_id"))
	case "cancel":
		return t.cancel(ctx, workflowStringArg(args, "run_id"), workflowStringArg(args, "reason"))
	case "retry":
		return t.retry(ctx, args)
	case "dev_status":
		return t.devStatus()
	case "dev_start":
		return t.devStart(ctx, args)
	case "dev_revise":
		return t.devRevise(args)
	case "dev_validate":
		return t.devValidate()
	case "dev_test":
		return t.devTest(ctx, args)
	case "dev_publish":
		return t.devPublish(ctx)
	case "dev_discard":
		return t.devDiscard()
	default:
		return ErrorResult(fmt.Sprintf("unsupported workflow action %q", action))
	}
}

func (t *WorkflowTool) compatibility(ctx context.Context) *ToolResult {
	summary, err := workflows.LoadCompatibilitySummary(ctx, t.workspace, t.runtime, t.localOptions()...)
	if err != nil {
		return ErrorResult(err.Error()).WithError(err)
	}
	return jsonToolResult(summary)
}

func (t *WorkflowTool) revalidate(ctx context.Context) *ToolResult {
	if _, err := workflows.RevalidateLocal(ctx, t.workspace, t.runtime, t.localOptions()...); err != nil {
		return ErrorResult(err.Error()).WithError(err)
	}
	return t.compatibility(ctx)
}

func (t *WorkflowTool) reload(ctx context.Context) *ToolResult {
	result, err := workflows.ReloadLocal(ctx, t.workspace, t.localOptions()...)
	if err != nil {
		return ErrorResult(err.Error()).WithError(err)
	}
	return jsonToolResult(result)
}

func (t *WorkflowTool) cancel(ctx context.Context, runID, reason string) *ToolResult {
	if runID == "" {
		return ErrorResult("run_id is required")
	}
	run, err := t.executor.CancelRun(ctx, runID, reason)
	if err != nil {
		return ErrorResult(err.Error()).WithError(err)
	}
	return jsonToolResult(run)
}

func (t *WorkflowTool) retry(ctx context.Context, args map[string]any) *ToolResult {
	runID := workflowStringArg(args, "run_id")
	if runID == "" {
		return ErrorResult("run_id is required")
	}
	secrets, err := workflowStringMapArg(args["secrets"])
	if err != nil {
		return ErrorResult(err.Error()).WithError(err)
	}
	previousRun, err := t.executorStore().GetRun(ctx, runID)
	if err != nil {
		return ErrorResult(err.Error()).WithError(err)
	}
	if runnableErr := workflows.EnsureWorkflowRunnable(
		ctx,
		t.workspace,
		previousRun.WorkflowRef,
		t.runtime,
		t.localOptions()...,
	); runnableErr != nil {
		return ErrorResult(runnableErr.Error()).WithError(runnableErr)
	}
	result, err := t.executor.Retry(ctx, runID, secrets)
	if err != nil {
		return jsonErrorToolResult(result, err)
	}
	return jsonToolResult(result)
}

func (t *WorkflowTool) list(ctx context.Context) *ToolResult {
	defs, err := workflows.ListLocal(ctx, t.workspace, t.localOptions()...)
	if err != nil {
		return ErrorResult(err.Error()).WithError(err)
	}
	return jsonToolResult(map[string]any{"workflows": defs})
}

func (t *WorkflowTool) validate(ctx context.Context, ref string) *ToolResult {
	if ref == "" {
		return ErrorResult("ref is required")
	}
	workflow, err := workflows.LoadLocal(ctx, t.workspace, ref, t.localOptions()...)
	if err != nil {
		return ErrorResult(err.Error()).WithError(err)
	}
	if err := workflows.Validate(workflow); err != nil {
		return ErrorResult(err.Error()).WithError(err)
	}
	return jsonToolResult(map[string]any{"ref": ref, "valid": true})
}

func (t *WorkflowTool) run(ctx context.Context, args map[string]any) *ToolResult {
	ref := workflowStringArg(args, "ref")
	if ref == "" {
		return ErrorResult("ref is required")
	}
	inputs, _ := args["inputs"].(map[string]any)
	secrets, err := workflowStringMapArg(args["secrets"])
	if err != nil {
		return ErrorResult(err.Error()).WithError(err)
	}
	if runnableErr := workflows.EnsureWorkflowRunnable(
		ctx,
		t.workspace,
		ref,
		t.runtime,
		t.localOptions()...,
	); runnableErr != nil {
		return ErrorResult(runnableErr.Error()).WithError(runnableErr)
	}
	result, err := t.executor.Run(ctx, workflows.RunRequest{
		Ref:      ref,
		Inputs:   inputs,
		Secrets:  secrets,
		Session:  ToolSessionKey(ctx),
		Delivery: deliveryFromToolContext(ctx),
	})
	if err != nil {
		return jsonErrorToolResult(result, err)
	}
	return jsonToolResult(result)
}

func (t *WorkflowTool) status(ctx context.Context, runID string) *ToolResult {
	if runID == "" {
		return ErrorResult("run_id is required")
	}
	run, err := t.executorStore().GetRun(ctx, runID)
	if err != nil {
		return ErrorResult(err.Error()).WithError(err)
	}
	return jsonToolResult(run)
}

func (t *WorkflowTool) events(ctx context.Context, runID string) *ToolResult {
	if runID == "" {
		return ErrorResult("run_id is required")
	}
	events, err := t.executorStore().Events(ctx, runID)
	if err != nil {
		return ErrorResult(err.Error()).WithError(err)
	}
	return jsonToolResult(map[string]any{"run_id": runID, "events": events})
}

func (t *WorkflowTool) graph(ctx context.Context, runID string) *ToolResult {
	if runID == "" {
		return ErrorResult("run_id is required")
	}
	graph, err := workflows.BuildRunGraph(ctx, t.executorStore(), runID)
	if err != nil {
		return ErrorResult(err.Error()).WithError(err)
	}
	return jsonToolResult(graph)
}

func (t *WorkflowTool) devStatus() *ToolResult {
	session, err := workflows.GetWorkflowDevelopmentSession(t.workspace)
	if err != nil {
		return ErrorResult(err.Error()).WithError(err)
	}
	return jsonToolResult(map[string]any{"session": session})
}

func (t *WorkflowTool) devStart(ctx context.Context, args map[string]any) *ToolResult {
	session, err := workflows.StartWorkflowDevelopment(
		ctx,
		t.workspace,
		t.runtime,
		workflows.WorkflowDevelopmentStartRequest{
			Reason:    workflowStringArg(args, "reason"),
			Prompt:    workflowStringArg(args, "prompt"),
			Ref:       workflowStringArg(args, "ref"),
			TargetRef: workflowStringArg(args, "target_ref"),
		},
		t.localOptions()...,
	)
	if err != nil {
		active, activeErr := workflows.GetWorkflowDevelopmentSession(t.workspace)
		if activeErr == nil && active != nil {
			return jsonErrorToolResult(map[string]any{"session": active, "error": err.Error()}, err)
		}
		return ErrorResult(err.Error()).WithError(err)
	}
	return jsonToolResult(map[string]any{"session": session})
}

func (t *WorkflowTool) devRevise(args map[string]any) *ToolResult {
	req := workflows.WorkflowDevelopmentReviseRequest{
		Prompt:     workflowStringArg(args, "prompt"),
		TargetRef:  workflowStringArg(args, "target_ref"),
		YAML:       workflowOptionalStringArg(args, "yaml"),
		Regenerate: workflowBoolArg(args, "regenerate"),
	}
	session, err := workflows.ReviseWorkflowDevelopment(t.workspace, req)
	if err != nil {
		return ErrorResult(err.Error()).WithError(err)
	}
	return jsonToolResult(map[string]any{"session": session})
}

func (t *WorkflowTool) devValidate() *ToolResult {
	session, err := workflows.ValidateWorkflowDevelopment(t.workspace)
	if err != nil {
		return ErrorResult(err.Error()).WithError(err)
	}
	return jsonToolResult(map[string]any{"session": session})
}

func (t *WorkflowTool) devTest(ctx context.Context, args map[string]any) *ToolResult {
	req := workflows.WorkflowDevelopmentReviseRequest{
		Prompt:     workflowStringArg(args, "prompt"),
		TargetRef:  workflowStringArg(args, "target_ref"),
		YAML:       workflowOptionalStringArg(args, "yaml"),
		Regenerate: workflowBoolArg(args, "regenerate"),
	}
	if req.Prompt != "" || req.TargetRef != "" || req.YAML != nil || req.Regenerate {
		if _, err := workflows.ReviseWorkflowDevelopment(t.workspace, req); err != nil {
			return ErrorResult(err.Error()).WithError(err)
		}
	}
	session, err := workflows.ValidateWorkflowDevelopment(t.workspace)
	if err != nil {
		return ErrorResult(err.Error()).WithError(err)
	}
	if session.Validation == nil || !session.Validation.Valid {
		recorded, recordErr := workflows.RecordWorkflowDevelopmentTest(
			t.workspace,
			nil,
			fmt.Errorf("workflow draft is not valid"),
		)
		if recordErr != nil {
			return ErrorResult(recordErr.Error()).WithError(recordErr)
		}
		return jsonErrorToolResult(
			map[string]any{"session": recorded, "error": "workflow draft is not valid"},
			fmt.Errorf("workflow draft is not valid"),
		)
	}
	workflow, err := workflows.Parse([]byte(session.YAML))
	if err != nil {
		recorded, recordErr := workflows.RecordWorkflowDevelopmentTest(t.workspace, nil, err)
		if recordErr != nil {
			return ErrorResult(recordErr.Error()).WithError(recordErr)
		}
		return jsonErrorToolResult(map[string]any{"session": recorded, "error": err.Error()}, err)
	}
	inputs, _ := args["inputs"].(map[string]any)
	secrets, err := workflowStringMapArg(args["secrets"])
	if err != nil {
		return ErrorResult(err.Error()).WithError(err)
	}
	result, runErr := t.executor.Run(ctx, workflows.RunRequest{
		Workflow:    workflow,
		WorkflowRef: "draft:" + session.TargetWorkflowRef,
		Inputs:      inputs,
		Secrets:     secrets,
		Session:     ToolSessionKey(ctx),
		Delivery:    deliveryFromToolContext(ctx),
	})
	recorded, recordErr := workflows.RecordWorkflowDevelopmentTest(t.workspace, result, runErr)
	if recordErr != nil {
		return ErrorResult(recordErr.Error()).WithError(recordErr)
	}
	payload := map[string]any{"session": recorded, "result": result}
	if runErr != nil {
		payload["error"] = runErr.Error()
		return jsonErrorToolResult(payload, runErr)
	}
	return jsonToolResult(payload)
}

func (t *WorkflowTool) devPublish(ctx context.Context) *ToolResult {
	result, err := workflows.PublishWorkflowDevelopment(ctx, t.workspace, t.runtime, t.localOptions()...)
	if err != nil {
		return ErrorResult(err.Error()).WithError(err)
	}
	return jsonToolResult(result)
}

func (t *WorkflowTool) devDiscard() *ToolResult {
	session, err := workflows.DiscardWorkflowDevelopment(t.workspace)
	if err != nil {
		return ErrorResult(err.Error()).WithError(err)
	}
	return jsonToolResult(map[string]any{"session": session})
}

func (t *WorkflowTool) executorStore() workflows.RunStore {
	if t.executor.Store != nil {
		return t.executor.Store
	}
	return workflows.NewFileRunStore(t.workspace)
}

func (t *WorkflowTool) localOptions() []workflows.LocalOption {
	if t == nil || strings.TrimSpace(t.definitionsDir) == "" {
		return nil
	}
	return []workflows.LocalOption{workflows.WithDefinitionsDir(t.definitionsDir)}
}

func deliveryFromToolContext(ctx context.Context) workflows.Delivery {
	return workflows.Delivery{
		Channel:          ToolChannel(ctx),
		ChatID:           ToolChatID(ctx),
		TopicID:          ToolTopicID(ctx),
		MessageID:        ToolMessageID(ctx),
		ReplyToMessageID: ToolReplyToMessageID(ctx),
	}
}

func jsonToolResult(value any) *ToolResult {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return ErrorResult(err.Error()).WithError(err)
	}
	return NewToolResult(string(data))
}

func jsonErrorToolResult(value any, err error) *ToolResult {
	data, marshalErr := json.MarshalIndent(value, "", "  ")
	if marshalErr != nil || len(data) == 0 || string(data) == "null" {
		return ErrorResult(err.Error()).WithError(err)
	}
	return ErrorResult(string(data)).WithError(err)
}

func workflowStringArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}

func workflowOptionalStringArg(args map[string]any, key string) *string {
	value, ok := args[key]
	if !ok || value == nil {
		return nil
	}
	text, ok := value.(string)
	if !ok {
		return nil
	}
	return &text
}

func workflowBoolArg(args map[string]any, key string) bool {
	value, _ := args[key].(bool)
	return value
}

func workflowStringMapArg(value any) (map[string]string, error) {
	if value == nil {
		return nil, nil
	}
	raw, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("secrets must be an object")
	}
	out := make(map[string]string, len(raw))
	for key, value := range raw {
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("secret %q must be a string", key)
		}
		out[key] = text
	}
	return out, nil
}
