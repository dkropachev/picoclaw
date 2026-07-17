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
	executor  *workflows.Executor
	workspace string
}

func NewWorkflowTool(executor *workflows.Executor, workspace string) *WorkflowTool {
	return &WorkflowTool{
		executor:  executor,
		workspace: strings.TrimSpace(workspace),
	}
}

func (t *WorkflowTool) Name() string {
	return WorkflowToolName
}

func (t *WorkflowTool) Description() string {
	return "List, validate, reload, run, cancel, retry, graph, and inspect reusable PicoClaw workflows from the workspace."
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
					"validate",
					"reload",
					"run",
					"status",
					"events",
					"graph",
					"cancel",
					"retry",
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
				"description": "Optional cancellation reason.",
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
	default:
		return ErrorResult(fmt.Sprintf("unsupported workflow action %q", action))
	}
}

func (t *WorkflowTool) reload(ctx context.Context) *ToolResult {
	result, err := workflows.ReloadLocal(ctx, t.workspace)
	if err != nil {
		return ErrorResult(err.Error()).WithError(err)
	}
	return jsonToolResult(result)
}

func (t *WorkflowTool) cancel(ctx context.Context, runID, reason string) *ToolResult {
	if runID == "" {
		return ErrorResult("run_id is required")
	}
	run, err := t.executorStore().CancelRun(ctx, runID, reason)
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
	result, err := t.executor.Retry(ctx, runID, secrets)
	if err != nil {
		return jsonErrorToolResult(result, err)
	}
	return jsonToolResult(result)
}

func (t *WorkflowTool) list(ctx context.Context) *ToolResult {
	defs, err := workflows.ListLocal(ctx, t.workspace)
	if err != nil {
		return ErrorResult(err.Error()).WithError(err)
	}
	return jsonToolResult(map[string]any{"workflows": defs})
}

func (t *WorkflowTool) validate(ctx context.Context, ref string) *ToolResult {
	if ref == "" {
		return ErrorResult("ref is required")
	}
	workflow, err := workflows.LoadLocal(ctx, t.workspace, ref)
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

func (t *WorkflowTool) executorStore() workflows.RunStore {
	if t.executor.Store != nil {
		return t.executor.Store
	}
	return workflows.NewFileRunStore(t.workspace)
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
