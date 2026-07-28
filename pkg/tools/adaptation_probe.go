package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type ToolAdaptationProbeResult struct {
	Profile            ToolAdaptationProfile `json:"profile"`
	VisibleToolSurface string                `json:"visible_tool_surface"`
	ToolName           string                `json:"tool_name"`
	Success            bool                  `json:"success"`
	Error              string                `json:"error,omitempty"`
	DurationMS         int64                 `json:"duration_ms"`
	RanAt              time.Time             `json:"ran_at"`
}

func RunToolAdaptationProbe(
	ctx context.Context,
	provider providers.LLMProvider,
	profile ToolAdaptationProfile,
	visibleToolSurface string,
	model string,
) ToolAdaptationProbeResult {
	start := time.Now()
	result := ToolAdaptationProbeResult{
		Profile:            normalizeToolAdaptationProfile(profile),
		VisibleToolSurface: config.NormalizeToolSurface(visibleToolSurface),
		RanAt:              start.UTC(),
	}
	toolDef, expectedArgs := probeToolDefinition(result.VisibleToolSurface)
	result.ToolName = toolDef.Function.Name

	if provider == nil {
		result.Error = "provider is nil"
		result.DurationMS = time.Since(start).Milliseconds()
		return result
	}

	resp, err := provider.Chat(
		ctx,
		[]providers.Message{{
			Role: "user",
			Content: fmt.Sprintf(
				"Call the tool named %s exactly once with these exact arguments: %s. Do not answer in text.",
				toolDef.Function.Name,
				formatProbeExpectedArgs(expectedArgs),
			),
		}},
		[]providers.ToolDefinition{toolDef},
		model,
		map[string]any{"max_tokens": 128, "temperature": 0},
	)
	result.DurationMS = time.Since(start).Milliseconds()
	if err != nil {
		result.Error = err.Error()
		ObserveToolAdaptationToolOutcome(
			result.Profile,
			result.VisibleToolSurface,
			result.ToolName,
			false,
			result.Error,
			time.Duration(result.DurationMS)*time.Millisecond,
		)
		return result
	}
	if resp == nil || len(resp.ToolCalls) == 0 {
		result.Error = "model did not call a tool"
		ObserveToolAdaptationToolOutcome(
			result.Profile,
			result.VisibleToolSurface,
			result.ToolName,
			false,
			result.Error,
			time.Duration(result.DurationMS)*time.Millisecond,
		)
		return result
	}

	call := providers.NormalizeToolCall(resp.ToolCalls[0])
	if call.Name != toolDef.Function.Name {
		result.Error = fmt.Sprintf("model called %q, want %q", call.Name, toolDef.Function.Name)
	} else if !probeArgsMatch(call.Arguments, expectedArgs) {
		result.Error = fmt.Sprintf("model called %q with unexpected arguments", call.Name)
	} else {
		result.Success = true
	}

	ObserveToolAdaptationToolOutcome(
		result.Profile,
		result.VisibleToolSurface,
		result.ToolName,
		result.Success,
		result.Error,
		time.Duration(result.DurationMS)*time.Millisecond,
	)
	return result
}

func formatProbeExpectedArgs(expected map[string]string) string {
	if expected["plan.0.step"] != "" || expected["plan.0.status"] != "" {
		return `{"plan":[{"step":"probe","status":"completed"}]}`
	}
	if expected["value"] != "" {
		return `{"value":"probe-ok"}`
	}
	return "{}"
}

func probeToolDefinition(surface string) (providers.ToolDefinition, map[string]string) {
	if config.NormalizeToolSurface(surface) == config.ToolSurfaceCodex {
		return providers.ToolDefinition{
				Type: "function",
				Function: providers.ToolFunctionDefinition{
					Name:        "update_plan",
					Description: "Update a short task plan. Probe only; the call is validated but not executed.",
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"explanation": map[string]any{"type": "string"},
							"plan": map[string]any{
								"type": "array",
								"items": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"step": map[string]any{"type": "string"},
										"status": map[string]any{
											"type": "string",
											"enum": []string{"pending", "in_progress", "completed"},
										},
									},
									"required": []string{"step", "status"},
								},
							},
						},
						"required": []string{"plan"},
					},
				},
			},
			map[string]string{"plan.0.step": "probe", "plan.0.status": "completed"}
	}

	return providers.ToolDefinition{
			Type: "function",
			Function: providers.ToolFunctionDefinition{
				Name:        "adaptation_probe_echo",
				Description: "No-op probe tool. The call is validated but not executed.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"value": map[string]any{
							"type":        "string",
							"description": "Must be exactly probe-ok.",
						},
					},
					"required": []string{"value"},
				},
			},
		},
		map[string]string{"value": "probe-ok"}
}

func probeArgsMatch(args map[string]any, expected map[string]string) bool {
	for path, want := range expected {
		got, ok := nestedProbeString(args, strings.Split(path, "."))
		if !ok || got != want {
			return false
		}
	}
	return true
}

func nestedProbeString(value any, path []string) (string, bool) {
	if len(path) == 0 {
		got, ok := value.(string)
		return got, ok
	}
	switch current := value.(type) {
	case map[string]any:
		next, ok := current[path[0]]
		if !ok {
			return "", false
		}
		return nestedProbeString(next, path[1:])
	case []any:
		if path[0] != "0" || len(current) == 0 {
			return "", false
		}
		return nestedProbeString(current[0], path[1:])
	default:
		return "", false
	}
}
