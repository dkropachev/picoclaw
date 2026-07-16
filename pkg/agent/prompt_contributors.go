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
		"Start the main chat as a normal chat. Do not create, register, attach, or switch a thread just because a rule matches.",
		"A chat becomes eligible to become or join a PicoClaw thread only after the matching rule's thresholds are crossed.",
		"Count only visible user and assistant chat messages for message thresholds, and estimate text thresholds from combined visible user and assistant text. Ignore system and tool messages.",
	)
	switch policy.EffectiveMode() {
	case config.ThreadPolicyModeSuggest:
		lines = append(
			lines,
			"Mode: suggest. After a rule matches and its thresholds are satisfied, search or propose a matching thread, but do not create, register, attach, or switch threads unless the user asks.",
		)
	case config.ThreadPolicyModeTool:
		lines = append(
			lines,
			"Mode: tool. After a rule matches and its thresholds are satisfied, search for an existing thread first. Use `register_current` when the current chat itself should become a discoverable thread, or `attach_current` when an existing thread should absorb the current context.",
		)
	default:
		lines = append(
			lines,
			"Mode: auto. After a rule matches and its thresholds are satisfied, call `threads` before doing the work with `action=\"switch\"`, the matching `type`, `query` set to the user's request, a concise `title`, and `create_if_missing=true`; then continue the work in the selected thread. If the current chat should become the thread, use `register_current` instead.",
		)
	}
	lines = append(
		lines,
		"The user can ask you to inspect or change this policy from chat; use `threads` with `action=\"get_policy\"` or `action=\"set_policy\"`.",
		"Requests to find, search, show, list, open, switch to, or continue an existing thread are thread navigation, not new work. Use `find`, `search`, `propose_switch`, or `switch` without `create_if_missing`; do not create, register, or attach a new thread unless the user explicitly asks to create one.",
		"After a successful switch for a thread navigation request, the navigation request is complete. Continue in that selected thread for later user work instead of creating another thread from the navigation text.",
	)
	if len(rules) > 0 {
		lines = append(lines, "")
		lines = append(lines, "Rules:")
		for _, rule := range rules {
			line := fmt.Sprintf("- %s: %s", rule.Type, rule.Description)
			if thresholds := formatThreadRuleThresholds(rule); thresholds != "" {
				line += " " + thresholds
			}
			lines = append(lines, line)
		}
	}
	if instructions != "" {
		lines = append(lines, "")
		lines = append(lines, "Additional instructions:")
		lines = append(lines, instructions)
	}
	return strings.Join(lines, "\n")
}

func formatThreadRuleThresholds(rule config.ThreadPolicyRule) string {
	var thresholds []string
	if rule.MinMessages > 0 {
		thresholds = append(thresholds, fmt.Sprintf("%d visible user/assistant messages", rule.MinMessages))
	}
	if rule.MinTextChars > 0 {
		thresholds = append(thresholds, fmt.Sprintf("%d visible user/assistant text characters", rule.MinTextChars))
	}
	if len(thresholds) == 0 {
		return "Threshold: none; eligible immediately when the rule matches."
	}
	joiner := " or "
	if config.NormalizeThreadPolicyThresholdLogic(rule.ThresholdLogic) == config.ThreadPolicyThresholdAll {
		joiner = " and "
	}
	return fmt.Sprintf("Threshold: wait until the current chat has at least %s.", strings.Join(thresholds, joiner))
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
