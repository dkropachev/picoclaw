package agent

import (
	"context"
	"errors"
	"strings"

	"github.com/sipeed/picoclaw/pkg/mcp"
	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

// ResolveWorkflowDependency reports the effective runtime availability of one
// declared workflow dependency. It intentionally returns only fixed readiness
// codes: provider, configuration, connection, and filesystem errors must not
// cross the browser-facing readiness boundary.
func (al *AgentLoop) ResolveWorkflowDependency(
	ctx context.Context,
	dependency workflows.WorkflowDependencyOccurrence,
) workflows.WorkflowDependencyReadinessCode {
	if al == nil || al.cfg == nil || al.registry == nil {
		return workflows.WorkflowDependencyReadinessUnavailable
	}
	if err := ctx.Err(); err != nil {
		return workflows.WorkflowDependencyReadinessUnavailable
	}

	switch dependency.Kind {
	case workflows.WorkflowDependencyKindReusable:
		return workflows.WorkflowDependencyReadinessReady
	case workflows.WorkflowDependencyKindHuman:
		if strings.TrimSpace(dependency.Name) == "task" {
			return workflows.WorkflowDependencyReadinessReady
		}
		return workflows.WorkflowDependencyReadinessNotFound
	case workflows.WorkflowDependencyKindFunction:
		if workflows.IsNativeFunction(dependency.Name) {
			return workflows.WorkflowDependencyReadinessReady
		}
		return workflows.WorkflowDependencyReadinessNotFound
	case workflows.WorkflowDependencyKindAgent:
		return al.resolveWorkflowAgentDependency(dependency.Name)
	case workflows.WorkflowDependencyKindTool:
		return al.resolveWorkflowToolDependency(ctx, dependency.Name)
	case workflows.WorkflowDependencyKindMCP:
		return al.resolveWorkflowMCPDependency(ctx, dependency.Name)
	default:
		return workflows.WorkflowDependencyReadinessUnavailable
	}
}

func (al *AgentLoop) resolveWorkflowAgentDependency(
	name string,
) workflows.WorkflowDependencyReadinessCode {
	normalized := routing.NormalizeAgentID(name)
	matches := 0
	for _, candidate := range al.cfg.Agents.List {
		if routing.NormalizeAgentID(candidate.ID) == normalized {
			matches++
		}
	}
	if matches > 1 {
		return workflows.WorkflowDependencyReadinessNameCollision
	}
	if len(al.cfg.Agents.List) == 0 && normalized != routing.DefaultAgentID {
		return workflows.WorkflowDependencyReadinessNotFound
	}
	if _, ok := al.registry.GetAgent(name); !ok {
		return workflows.WorkflowDependencyReadinessNotFound
	}
	return workflows.WorkflowDependencyReadinessReady
}

func (al *AgentLoop) resolveWorkflowToolDependency(
	ctx context.Context,
	name string,
) workflows.WorkflowDependencyReadinessCode {
	name = strings.TrimSpace(name)
	if name == "" {
		return workflows.WorkflowDependencyReadinessNotConfigured
	}
	if name == "workflow" {
		return workflows.WorkflowDependencyReadinessNotAllowed
	}
	agent := al.registry.GetDefaultAgent()
	if agent == nil || agent.Tools == nil {
		return workflows.WorkflowDependencyReadinessUnavailable
	}
	if _, ok := agent.Tools.Get(name); ok {
		return workflows.WorkflowDependencyReadinessReady
	}

	// A tool/<name> target may intentionally address an eager MCP wrapper.
	// Initialize MCP only after the base registry misses so an unrelated MCP
	// failure cannot block an otherwise-ready built-in tool.
	if al.cfg.Tools.MCP.Enabled {
		_ = al.ensureMCPInitialized(ctx)
		if _, ok := agent.Tools.Get(name); ok {
			return workflows.WorkflowDependencyReadinessReady
		}
		if agent.Tools.HasRegistered(name) {
			return workflows.WorkflowDependencyReadinessDisabled
		}
	}
	return workflows.WorkflowDependencyReadinessNotFound
}

func (al *AgentLoop) resolveWorkflowMCPDependency(
	ctx context.Context,
	name string,
) workflows.WorkflowDependencyReadinessCode {
	requestedServer, requestedTool, ok := strings.Cut(strings.TrimSpace(name), "/")
	requestedServer = strings.TrimSpace(requestedServer)
	requestedTool = strings.TrimSpace(requestedTool)
	if !ok || requestedServer == "" || requestedTool == "" {
		return workflows.WorkflowDependencyReadinessNotConfigured
	}
	if !al.cfg.Tools.MCP.Enabled {
		return workflows.WorkflowDependencyReadinessDisabled
	}
	defaultAgent := al.registry.GetDefaultAgent()
	if defaultAgent == nil || defaultAgent.Tools == nil {
		return workflows.WorkflowDependencyReadinessUnavailable
	}

	serverCfg, configured := al.cfg.Tools.MCP.Servers[requestedServer]
	if !configured {
		return workflows.WorkflowDependencyReadinessNotConfigured
	}
	switch {
	case !serverCfg.Enabled:
		return workflows.WorkflowDependencyReadinessDisabled
	case !defaultAgent.AllowsMCPServer(requestedServer):
		return workflows.WorkflowDependencyReadinessNotAllowed
	case serverIsDeferred(al.cfg.Tools.MCP.Discovery.Enabled, serverCfg):
		// Explicit workflow MCP steps must never rely on transient discovery
		// TTL promotion.
		return workflows.WorkflowDependencyReadinessDisabled
	}

	if err := al.ensureMCPInitialized(ctx); err != nil {
		if errors.Is(err, mcp.ErrCanonicalToolNameCollision) {
			return workflows.WorkflowDependencyReadinessNameCollision
		}
		return workflows.WorkflowDependencyReadinessNotConnected
	}
	manager := al.mcp.getManager()
	if manager == nil {
		return workflows.WorkflowDependencyReadinessNotConnected
	}
	if err := validateCanonicalMCPToolNames(manager.GetServers()); err != nil {
		return workflows.WorkflowDependencyReadinessNameCollision
	}
	connection, connected := manager.GetServer(requestedServer)
	if !connected || connection == nil {
		return workflows.WorkflowDependencyReadinessNotConnected
	}
	found := false
	for _, tool := range connection.Tools {
		if tool != nil && tool.Name == requestedTool {
			found = true
			break
		}
	}
	if !found {
		return workflows.WorkflowDependencyReadinessNotFound
	}

	canonicalName := mcp.CanonicalToolName(requestedServer, requestedTool)
	if registeredTool, registered := defaultAgent.Tools.Get(canonicalName); registered {
		if workflowMCPToolMatches(registeredTool, requestedServer, requestedTool) {
			return workflows.WorkflowDependencyReadinessReady
		}
		return workflows.WorkflowDependencyReadinessNameCollision
	}
	if defaultAgent.Tools.HasRegistered(canonicalName) {
		return workflows.WorkflowDependencyReadinessDisabled
	}
	return workflows.WorkflowDependencyReadinessNotAllowed
}
