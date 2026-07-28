package tools

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
)

type CodexExecCommandTool struct {
	exec *ExecTool
}

func NewCodexExecCommandTool(exec *ExecTool) *CodexExecCommandTool {
	return &CodexExecCommandTool{exec: exec}
}

func (t *CodexExecCommandTool) Name() string {
	return "exec_command"
}

func (t *CodexExecCommandTool) Description() string {
	return "Run a shell command using a Codex-compatible argument shape. Returns command output, or a session id when background=true."
}

func (t *CodexExecCommandTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"cmd": map[string]any{
				"type":        "string",
				"description": "Shell command to execute.",
			},
			"workdir": map[string]any{
				"type":        "string",
				"description": "Working directory for the command. Relative paths are resolved by the shell tool backend.",
			},
			"yield_time_ms": map[string]any{
				"type":        "integer",
				"description": "Approximate foreground timeout in milliseconds.",
			},
			"timeout": map[string]any{
				"type":        "integer",
				"description": "Foreground timeout in seconds. Overrides yield_time_ms when provided.",
			},
			"background": map[string]any{
				"type":        "boolean",
				"description": "Run in the background and return a reusable session id.",
			},
			"tty": map[string]any{
				"type":        "boolean",
				"description": "Run in a pseudo-terminal when supported.",
			},
			"login": map[string]any{
				"type":        "boolean",
				"description": "Accepted for Codex compatibility. PicoClaw's exec backend controls shell login behavior.",
			},
			"shell": map[string]any{
				"type":        "string",
				"description": "Accepted for Codex compatibility. PicoClaw executes through its configured shell backend.",
			},
			"max_output_tokens": map[string]any{
				"type":        "integer",
				"description": "Accepted for Codex compatibility. PicoClaw applies its backend output limits.",
			},
		},
		"required": []string{"cmd"},
	}
}

func (t *CodexExecCommandTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	if t == nil || t.exec == nil {
		return ErrorResult("exec backend not configured")
	}
	command := strings.TrimSpace(compatStringArg(args, "cmd"))
	if command == "" {
		return ErrorResult("cmd is required")
	}

	mapped := map[string]any{
		"action":  "run",
		"command": command,
	}
	if workdir := strings.TrimSpace(compatStringArg(args, "workdir")); workdir != "" {
		mapped["cwd"] = workdir
	}
	if background, ok := compatBoolArg(args, "background"); ok {
		mapped["background"] = background
	}
	if tty, ok := compatBoolArg(args, "tty"); ok {
		mapped["pty"] = tty
	}
	if timeout, ok := compatIntArg(args, "timeout"); ok && timeout > 0 {
		mapped["timeout"] = timeout
	} else if yieldMS, ok := compatIntArg(args, "yield_time_ms"); ok && yieldMS > 0 {
		mapped["timeout"] = int(math.Ceil(float64(yieldMS) / 1000.0))
	}

	return t.exec.Execute(ctx, mapped)
}

type CodexWriteStdinTool struct {
	exec *ExecTool
}

func NewCodexWriteStdinTool(exec *ExecTool) *CodexWriteStdinTool {
	return &CodexWriteStdinTool{exec: exec}
}

func (t *CodexWriteStdinTool) Name() string {
	return "write_stdin"
}

func (t *CodexWriteStdinTool) Description() string {
	return "Write characters to an existing exec_command session."
}

func (t *CodexWriteStdinTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"session_id": map[string]any{
				"type":        "string",
				"description": "Session id returned by exec_command.",
			},
			"chars": map[string]any{
				"type":        "string",
				"description": "Characters to write to stdin.",
			},
			"yield_time_ms": map[string]any{
				"type":        "integer",
				"description": "Accepted for Codex compatibility.",
			},
			"max_output_tokens": map[string]any{
				"type":        "integer",
				"description": "Accepted for Codex compatibility.",
			},
		},
		"required": []string{"session_id"},
	}
}

func (t *CodexWriteStdinTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	if t == nil || t.exec == nil {
		return ErrorResult("exec backend not configured")
	}
	sessionID := strings.TrimSpace(compatStringArg(args, "session_id"))
	if sessionID == "" {
		return ErrorResult("session_id is required")
	}
	return t.exec.Execute(ctx, map[string]any{
		"action":    "write",
		"sessionId": sessionID,
		"data":      compatStringArg(args, "chars"),
	})
}

type CodexViewImageTool struct {
	loader Tool
}

func NewCodexViewImageTool(loader Tool) *CodexViewImageTool {
	return &CodexViewImageTool{loader: loader}
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

func compatBoolArg(args map[string]any, key string) (bool, bool) {
	switch v := args[key].(type) {
	case bool:
		return v, true
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(v))
		return parsed, err == nil
	default:
		return false, false
	}
}

func compatIntArg(args map[string]any, key string) (int, bool) {
	switch v := args[key].(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(v))
		return parsed, err == nil
	default:
		return 0, false
	}
}
