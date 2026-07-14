package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type toolDiscoveryPromptContributor struct {
	useBM25  bool
	useRegex bool
}

func (c toolDiscoveryPromptContributor) PromptSource() PromptSourceDescriptor {
	return PromptSourceDescriptor{
		ID:              PromptSourceToolDiscovery,
		Owner:           "tools",
		Description:     "Tool discovery instructions",
		Allowed:         []PromptPlacement{{Layer: PromptLayerCapability, Slot: PromptSlotTooling}},
		StableByDefault: true,
	}
}

func (c toolDiscoveryPromptContributor) ContributePrompt(
	_ context.Context,
	req PromptBuildRequest,
) ([]PromptPart, error) {
	if req.SuppressToolUseRule {
		return nil, nil
	}
	useBM25 := c.useBM25 && promptAllowsTool(req, tools.BM25SearchToolName)
	useRegex := c.useRegex && promptAllowsTool(req, tools.RegexSearchToolName)
	if !useBM25 && !useRegex {
		return nil, nil
	}
	content := formatToolDiscoveryRule(useBM25, useRegex)
	if strings.TrimSpace(content) == "" {
		return nil, nil
	}

	return []PromptPart{
		{
			ID:      "capability.tool_discovery",
			Layer:   PromptLayerCapability,
			Slot:    PromptSlotTooling,
			Source:  PromptSource{ID: PromptSourceToolDiscovery, Name: "tool_registry:discovery"},
			Title:   "tool discovery",
			Content: content,
			Stable:  true,
			Cache:   PromptCacheEphemeral,
		},
	}, nil
}

type threadPolicyPromptContributor struct {
	cfg *config.Config
}

func (c threadPolicyPromptContributor) PromptSource() PromptSourceDescriptor {
	return PromptSourceDescriptor{
		ID:              PromptSourceThreadPolicy,
		Owner:           "threads",
		Description:     "Thread routing policy",
		Allowed:         []PromptPlacement{{Layer: PromptLayerInstruction, Slot: PromptSlotWorkspace}},
		StableByDefault: true,
	}
}

func (c threadPolicyPromptContributor) ContributePrompt(
	_ context.Context,
	req PromptBuildRequest,
) ([]PromptPart, error) {
	if req.SuppressToolUseRule || c.cfg == nil || !c.cfg.Tools.IsToolEnabled("threads") {
		return nil, nil
	}
	if !promptAllowsTool(req, tools.ThreadsToolName) {
		return nil, nil
	}
	content := formatThreadPolicyPrompt(c.cfg.Tools.Threads.Policy)
	if strings.TrimSpace(content) == "" {
		return nil, nil
	}

	return []PromptPart{
		{
			ID:      "instruction.thread_policy",
			Layer:   PromptLayerInstruction,
			Slot:    PromptSlotWorkspace,
			Source:  PromptSource{ID: PromptSourceThreadPolicy, Name: "thread:policy"},
			Title:   "thread routing policy",
			Content: content,
			Stable:  true,
			Cache:   PromptCacheEphemeral,
		},
	}, nil
}

func formatThreadPolicyPrompt(policy config.ThreadPolicyConfig) string {
	if !policy.Enabled || policy.EffectiveMode() == config.ThreadPolicyModeOff {
		return ""
	}

	rules := config.NormalizeThreadPolicyRules(policy.Rules)
	instructions := strings.TrimSpace(policy.Instructions)
	if len(rules) == 0 && instructions == "" {
		return ""
	}

	var lines []string
	lines = append(lines, "## Thread Routing Policy")
	lines = append(lines, "")
	lines = append(
		lines,
		"Use the `threads` tool to keep substantial work in a separate full-window PicoClaw UI thread when the policy below matches.",
	)
	switch policy.EffectiveMode() {
	case config.ThreadPolicyModeSuggest:
		lines = append(
			lines,
			"Mode: suggest. Search or propose a matching thread, but do not create or switch threads unless the user asks.",
		)
	case config.ThreadPolicyModeTool:
		lines = append(
			lines,
			"Mode: tool. When a rule matches, use `threads` to find an existing thread and either `register_current` or `attach_current` when the current session should become part of that thread.",
		)
	default:
		lines = append(
			lines,
			"Mode: auto. When a rule matches, call `threads` before doing the work with `action=\"switch\"`, the matching `type`, `query` set to the user's request, a concise `title`, and `create_if_missing=true`; then continue the work in the selected thread.",
		)
	}
	lines = append(
		lines,
		"The user can ask you to inspect or change this policy from chat; use `threads` with `action=\"get_policy\"` or `action=\"set_policy\"`.",
	)
	if len(rules) > 0 {
		lines = append(lines, "")
		lines = append(lines, "Rules:")
		for _, rule := range rules {
			lines = append(lines, fmt.Sprintf("- %s: %s", rule.Type, rule.Description))
		}
	}
	if instructions != "" {
		lines = append(lines, "")
		lines = append(lines, "Additional instructions:")
		lines = append(lines, instructions)
	}
	return strings.Join(lines, "\n")
}

type mcpServerPromptContributor struct {
	serverName string
	toolCount  int
	deferred   bool
}

func (c mcpServerPromptContributor) PromptSource() PromptSourceDescriptor {
	return PromptSourceDescriptor{
		ID:              mcpPromptSourceID(c.serverName),
		Owner:           "mcp",
		Description:     fmt.Sprintf("MCP server %q capability prompt", c.serverName),
		Allowed:         []PromptPlacement{{Layer: PromptLayerCapability, Slot: PromptSlotMCP}},
		StableByDefault: true,
	}
}

func (c mcpServerPromptContributor) ContributePrompt(
	_ context.Context,
	req PromptBuildRequest,
) ([]PromptPart, error) {
	if req.SuppressToolUseRule {
		return nil, nil
	}
	serverName := strings.TrimSpace(c.serverName)
	if serverName == "" || c.toolCount <= 0 {
		return nil, nil
	}
	if len(req.AllowedTools) > 0 &&
		!promptAllowsToolPrefix(req, "mcp_"+promptSourceComponent(serverName)+"_") {
		return nil, nil
	}

	availability := "available as native tools"
	if c.deferred {
		availability = "hidden behind tool discovery until unlocked"
	}

	return []PromptPart{
		{
			ID:     "capability.mcp." + promptSourceComponent(serverName),
			Layer:  PromptLayerCapability,
			Slot:   PromptSlotMCP,
			Source: PromptSource{ID: mcpPromptSourceID(serverName), Name: "mcp:" + serverName},
			Title:  "MCP server capability",
			Content: fmt.Sprintf(
				"MCP server `%s` is connected. It contributes %d tool(s), currently %s.",
				serverName,
				c.toolCount,
				availability,
			),
			Stable: true,
			Cache:  PromptCacheEphemeral,
		},
	}, nil
}

type agentDiscoveryPromptContributor struct {
	agentID  string
	discover func(agentID string) []AgentDescriptor
}

func (c agentDiscoveryPromptContributor) PromptSource() PromptSourceDescriptor {
	return PromptSourceDescriptor{
		ID:              PromptSourceAgentDiscovery,
		Owner:           "agent",
		Description:     "Structured multi-agent discovery registry",
		Allowed:         []PromptPlacement{{Layer: PromptLayerCapability, Slot: PromptSlotTooling}},
		StableByDefault: false,
	}
}

func (c agentDiscoveryPromptContributor) ContributePrompt(
	_ context.Context,
	req PromptBuildRequest,
) ([]PromptPart, error) {
	if req.SuppressToolUseRule {
		return nil, nil
	}
	if !promptAllowsTool(req, "spawn") {
		return nil, nil
	}
	if c.discover == nil {
		return nil, nil
	}
	content := formatAgentDiscoverySection(c.discover(c.agentID))
	if strings.TrimSpace(content) == "" {
		return nil, nil
	}

	return []PromptPart{
		{
			ID:      "capability.agent_discovery",
			Layer:   PromptLayerCapability,
			Slot:    PromptSlotTooling,
			Source:  PromptSource{ID: PromptSourceAgentDiscovery, Name: "agent:discovery"},
			Title:   "agent discovery",
			Content: content,
			Stable:  false,
			Cache:   PromptCacheNone,
		},
	}, nil
}

func mcpPromptSourceID(serverName string) PromptSourceID {
	return PromptSourceID("mcp:" + promptSourceComponent(serverName))
}

func promptSourceComponent(value string) string {
	const maxLen = 64

	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "unnamed"
	}

	var b strings.Builder
	lastWasSep := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			lastWasSep = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			lastWasSep = false
		case r == '-' || r == '_':
			if !lastWasSep && b.Len() > 0 {
				b.WriteRune(r)
				lastWasSep = true
			}
		default:
			if !lastWasSep && b.Len() > 0 {
				b.WriteRune('_')
				lastWasSep = true
			}
		}
	}

	result := strings.Trim(b.String(), "_")
	if result == "" {
		return "unnamed"
	}
	if len(result) > maxLen {
		return result[:maxLen]
	}
	return result
}

func promptAllowsTool(req PromptBuildRequest, name string) bool {
	if len(req.AllowedTools) == 0 {
		return true
	}
	allowed := cleanAllowedSet(req.AllowedTools)
	_, ok := allowed[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

func promptAllowsToolPrefix(req PromptBuildRequest, prefix string) bool {
	if len(req.AllowedTools) == 0 {
		return true
	}
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	if prefix == "" {
		return false
	}
	for _, name := range req.AllowedTools {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(name)), prefix) {
			return true
		}
	}
	return false
}
