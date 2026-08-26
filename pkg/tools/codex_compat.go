package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/sipeed/picoclaw/pkg/media"
)

type codexExecBackend interface {
	Execute(ctx context.Context, args map[string]any) *ToolResult
}

type CodexExecCommandTool struct {
	exec codexExecBackend
}

func NewCodexExecCommandTool(exec *ExecTool) *CodexExecCommandTool {
	return &CodexExecCommandTool{exec: exec}
}

func (t *CodexExecCommandTool) Name() string {
	return "exec_command"
}

func (t *CodexExecCommandTool) Description() string {
	return "Run a shell command. Synchronous calls return captured output. Set background=true to return an input-only session for write_stdin; this surface does not expose session output, polling, or termination."
}

func (t *CodexExecCommandTool) Parameters() map[string]any {
	return codexExecCommandParameters()
}

func codexExecCommandParameters() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"cmd": map[string]any{
				"type":        "string",
				"description": "Shell command to execute.",
			},
			"workdir": map[string]any{
				"type":        "string",
				"description": "Working directory for the command. Blank uses the configured backend working directory.",
			},
			"background": map[string]any{
				"type":        "boolean",
				"description": "Run in the background and return an input-only session id for write_stdin.",
			},
			"tty": map[string]any{
				"type":        "boolean",
				"description": "Run the background session in a pseudo-terminal when supported. Requires background=true.",
			},
		},
		"required": []string{"cmd"},
	}
}

func (t *CodexExecCommandTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	if err := validateToolArgs(codexExecCommandParameters(), args); err != nil {
		return ErrorResult(fmt.Sprintf("invalid arguments for tool %q: %s", "exec_command", err))
	}

	command := strings.TrimSpace(args["cmd"].(string))
	if command == "" {
		return ErrorResult("cmd must be nonblank")
	}
	workdir, _ := args["workdir"].(string)
	workdir = strings.TrimSpace(workdir)
	background, _ := args["background"].(bool)
	tty, _ := args["tty"].(bool)
	if tty && !background {
		return ErrorResult("tty=true requires background=true")
	}

	if t == nil || isTypedNil(t.exec) {
		return ErrorResult("exec backend not configured")
	}

	mapped := map[string]any{
		"action":  "run",
		"command": command,
	}
	if workdir != "" {
		mapped["cwd"] = workdir
	}
	if _, present := args["background"]; present {
		mapped["background"] = background
	}
	if _, present := args["tty"]; present {
		mapped["pty"] = tty
	}

	result := t.exec.Execute(ctx, mapped)
	if result == nil {
		return ErrorResult("exec backend returned no result")
	}
	if !background || result.IsError {
		return result
	}
	return projectCodexSessionResult(result, "")
}

type CodexWriteStdinTool struct {
	exec codexExecBackend
}

func NewCodexWriteStdinTool(exec *ExecTool) *CodexWriteStdinTool {
	return &CodexWriteStdinTool{exec: exec}
}

func (t *CodexWriteStdinTool) Name() string {
	return "write_stdin"
}

func (t *CodexWriteStdinTool) Description() string {
	return "Write exact characters to an input-only background exec_command session and return its current status. This tool does not poll or return session output."
}

func (t *CodexWriteStdinTool) Parameters() map[string]any {
	return codexWriteStdinParameters()
}

func codexWriteStdinParameters() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"session_id": map[string]any{
				"type":        "string",
				"description": "Input-only session id returned by background exec_command.",
			},
			"chars": map[string]any{
				"type":        "string",
				"description": "Nonempty characters to write exactly, without trimming.",
			},
		},
		"required": []string{"session_id", "chars"},
	}
}

func (t *CodexWriteStdinTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	if err := validateToolArgs(codexWriteStdinParameters(), args); err != nil {
		return ErrorResult(fmt.Sprintf("invalid arguments for tool %q: %s", "write_stdin", err))
	}

	sessionID := strings.TrimSpace(args["session_id"].(string))
	if sessionID == "" {
		return ErrorResult("session_id must be nonblank")
	}
	chars := args["chars"].(string)
	if chars == "" {
		return ErrorResult("chars must be nonempty")
	}

	if t == nil || isTypedNil(t.exec) {
		return ErrorResult("exec backend not configured")
	}
	result := t.exec.Execute(ctx, map[string]any{
		"action":    "write",
		"sessionId": sessionID,
		"data":      chars,
	})
	if result == nil {
		return ErrorResult("exec backend returned no result")
	}
	if result.IsError {
		return result
	}
	return projectCodexSessionResult(result, sessionID)
}

type codexSessionResponse struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
}

func projectCodexSessionResult(result *ToolResult, expectedSessionID string) *ToolResult {
	var native ExecResponse
	if err := json.Unmarshal([]byte(result.ForLLM), &native); err != nil {
		return ErrorResult(fmt.Sprintf("exec backend returned an invalid session response: %v", err))
	}
	sessionID := strings.TrimSpace(native.SessionID)
	if expectedSessionID != "" {
		if sessionID != expectedSessionID {
			return ErrorResult("exec backend returned a mismatched session id")
		}
		sessionID = expectedSessionID
	}
	status := strings.TrimSpace(native.Status)
	if sessionID == "" || status == "" {
		return ErrorResult("exec backend returned an incomplete session response")
	}
	payload, err := json.Marshal(codexSessionResponse{SessionID: sessionID, Status: status})
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to project exec session response: %v", err))
	}
	projected := *result
	projected.Media = append([]string(nil), result.Media...)
	projected.Messages = append(result.Messages[:0:0], result.Messages...)
	projected.ArtifactTags = append([]string(nil), result.ArtifactTags...)
	projected.ForLLM = string(payload)
	return &projected
}

type CodexViewImageTool struct {
	loader Tool
}

func NewCodexViewImageTool(loader Tool) *CodexViewImageTool {
	return &CodexViewImageTool{loader: loader}
}

func (t *CodexViewImageTool) SetMediaStore(store media.MediaStore) {
	if t == nil {
		return
	}
	if loader, ok := t.loader.(mediaStoreAware); ok {
		loader.SetMediaStore(store)
	}
}

func (t *CodexViewImageTool) Name() string {
	return "view_image"
}

func (t *CodexViewImageTool) Description() string {
	return "Load a local image for visual inspection using a Codex-compatible argument shape."
}

func (t *CodexViewImageTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the local image file.",
			},
			"detail": map[string]any{
				"type":        "string",
				"enum":        []string{"high", "original"},
				"description": "Accepted for Codex compatibility. PicoClaw uses the image detail supported by the active model.",
			},
		},
		"required": []string{"path"},
	}
}

func (t *CodexViewImageTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	if t == nil || t.loader == nil {
		return ErrorResult("image loader backend not configured")
	}
	return t.loader.Execute(ctx, map[string]any{
		"path": compatStringArg(args, "path"),
	})
}

type UpdatePlanTool struct {
	mu    sync.Mutex
	steps []PlanStep
}

type PlanStep struct {
	Step   string
	Status string
}

func NewUpdatePlanTool() *UpdatePlanTool {
	return &UpdatePlanTool{}
}

func NewUpdatePlanToolFactory() ToolFactory {
	prototype := NewUpdatePlanTool()
	descriptor, err := toolDescriptorFromTool(prototype)
	if err != nil {
		panic(fmt.Sprintf("build update_plan descriptor: %v", err))
	}
	factory, err := NewToolFactory(descriptor, ToolTraits{
		Risk:        ToolRiskMutation,
		Parallel:    ToolParallelSerialized,
		Idempotency: ToolIdempotencyIdempotent,
		Sharing:     ToolSharingPerOwner,
	}, func(ToolBuildContext) (Tool, error) {
		return NewUpdatePlanTool(), nil
	})
	if err != nil {
		panic(fmt.Sprintf("build update_plan factory: %v", err))
	}
	return factory
}

func (t *UpdatePlanTool) Name() string {
	return "update_plan"
}

func (t *UpdatePlanTool) Description() string {
	return "Update the current task plan. Use exactly one in_progress item when work is underway."
}

func (t *UpdatePlanTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"explanation": map[string]any{
				"type":        "string",
				"description": "Optional note for why the plan changed.",
			},
			"plan": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"step": map[string]any{
							"type":        "string",
							"description": "Task step.",
						},
						"status": map[string]any{
							"type":        "string",
							"enum":        []string{"pending", "in_progress", "completed"},
							"description": "Step status.",
						},
					},
					"required":             []string{"step", "status"},
					"additionalProperties": false,
				},
			},
		},
		"required": []string{"plan"},
	}
}

func (t *UpdatePlanTool) Execute(_ context.Context, args map[string]any) *ToolResult {
	rawPlan, ok := args["plan"].([]any)
	if !ok {
		return ErrorResult("plan is required")
	}

	steps := make([]PlanStep, 0, len(rawPlan))
	inProgress := 0
	for _, raw := range rawPlan {
		item, ok := raw.(map[string]any)
		if !ok {
			return ErrorResult("plan items must be objects")
		}
		step := strings.TrimSpace(compatStringArg(item, "step"))
		status := strings.TrimSpace(compatStringArg(item, "status"))
		if step == "" {
			return ErrorResult("plan item step is required")
		}
		switch status {
		case "pending", "completed":
		case "in_progress":
			inProgress++
		default:
			return ErrorResult(fmt.Sprintf("invalid plan status %q", status))
		}
		steps = append(steps, PlanStep{Step: step, Status: status})
	}
	if inProgress > 1 {
		return ErrorResult("at most one plan item can be in_progress")
	}

	t.mu.Lock()
	t.steps = steps
	t.mu.Unlock()
	return SilentResult("Plan updated")
}

func compatStringArg(args map[string]any, key string) string {
	switch v := args[key].(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	case int, int64, float64, bool:
		return fmt.Sprint(v)
	default:
		return ""
	}
}
