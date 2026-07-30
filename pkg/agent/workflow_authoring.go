package agent

import (
	"container/heap"
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/sipeed/picoclaw/pkg/mcp"
	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/tools"
	integrationtools "github.com/sipeed/picoclaw/pkg/tools/integration"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

var marshalWorkflowAuthoringCapabilities = workflows.MarshalWorkflowAuthoringCapabilities

// WorkflowAuthoringCapabilities projects the action identities available to
// workflow authors from this live runtime generation. The runtime lease spans
// the complete projection so reload cannot mix identities from different
// generations.
func (al *AgentLoop) WorkflowAuthoringCapabilities(
	ctx context.Context,
) (workflows.WorkflowAuthoringCapabilities, error) {
	catalog, _, err := al.workflowAuthoringCapabilities(ctx)
	return catalog, err
}

// WorkflowAuthoringCapabilitiesJSON returns the fully validated and bounded
// catalog encoding produced while the live runtime lease is still held.
func (al *AgentLoop) WorkflowAuthoringCapabilitiesJSON(
	ctx context.Context,
) ([]byte, error) {
	_, encoded, err := al.workflowAuthoringCapabilities(ctx)
	return encoded, err
}

func (al *AgentLoop) workflowAuthoringCapabilities(
	ctx context.Context,
) (workflows.WorkflowAuthoringCapabilities, []byte, error) {
	if al == nil {
		return workflows.WorkflowAuthoringCapabilities{}, nil, fmt.Errorf("agent loop not configured")
	}
	leaseCtx, release, err := al.acquireRuntimeUse(ctx)
	if err != nil {
		return workflows.WorkflowAuthoringCapabilities{}, nil, err
	}
	defer release()

	cfg := al.GetConfig()
	registry := al.GetRegistry()
	if cfg == nil || registry == nil {
		return workflows.WorkflowAuthoringCapabilities{}, nil, fmt.Errorf("agent runtime unavailable")
	}

	catalog := workflows.WorkflowAuthoringCapabilities{
		MCPStatus: workflows.WorkflowAuthoringMCPDisabled,
		Agents:    []workflows.WorkflowAuthoringAgentCapability{},
		Tools:     []workflows.WorkflowAuthoringToolCapability{},
		MCPTools:  []workflows.WorkflowAuthoringMCPToolCapability{},
		Functions: []workflows.WorkflowAuthoringFunctionCapability{},
		Limits:    []workflows.WorkflowAuthoringLimitCode{},
	}
	if cfg.Tools.IsToolEnabled("mcp") {
		manager := al.mcp.getManager()
		if manager == nil {
			catalog.MCPStatus = workflows.WorkflowAuthoringMCPUnavailable
		} else {
			catalog.MCPStatus = workflows.WorkflowAuthoringMCPReady
		}
	}

	limits := make([]workflows.WorkflowAuthoringLimitCode, 0, 4)
	var projectErr error
	catalog.Agents, limits, projectErr = al.projectWorkflowAuthoringAgents(
		leaseCtx,
		registry,
		limits,
	)
	if projectErr != nil {
		return workflows.WorkflowAuthoringCapabilities{}, nil, projectErr
	}

	defaultAgent := registry.GetDefaultAgent()
	if defaultAgent == nil || defaultAgent.Tools == nil {
		return workflows.WorkflowAuthoringCapabilities{}, nil, fmt.Errorf("default agent tools unavailable")
	}
	shapeSanitizer := &workflows.WorkflowAuthoringShapeSanitizer{}
	catalog.Tools, catalog.MCPTools, limits, projectErr = al.projectWorkflowAuthoringTools(
		leaseCtx,
		defaultAgent,
		catalog.MCPStatus,
		shapeSanitizer,
		limits,
	)
	if projectErr != nil {
		return workflows.WorkflowAuthoringCapabilities{}, nil, projectErr
	}
	catalog.Functions, limits = projectWorkflowAuthoringFunctions(limits)

	catalog.Limits = workflows.NormalizeWorkflowAuthoringLimits(limits)
	catalog.Complete = catalog.MCPStatus != workflows.WorkflowAuthoringMCPUnavailable &&
		len(catalog.Limits) == 0
	encoded, ok := marshalWorkflowAuthoringCapabilities(catalog)
	if !ok {
		return workflows.WorkflowAuthoringCapabilities{}, nil, fmt.Errorf(
			"workflow authoring capabilities unavailable",
		)
	}
	return catalog, encoded, nil
}

func (al *AgentLoop) projectWorkflowAuthoringAgents(
	ctx context.Context,
	registry *AgentRegistry,
	limits []workflows.WorkflowAuthoringLimitCode,
) (
	[]workflows.WorkflowAuthoringAgentCapability,
	[]workflows.WorkflowAuthoringLimitCode,
	error,
) {
	if registry == nil {
		return nil, limits, fmt.Errorf("agent registry unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	registry.mu.RLock()
	defaultID := registry.defaultAgentIDLocked()
	defaultInstance := registry.agents[defaultID]
	defaultCandidate := workflowAuthoringAgentCandidate{}
	hasSafeDefault := defaultInstance != nil &&
		workflows.SafeWorkflowAuthoringAgentID(defaultID)
	if hasSafeDefault {
		defaultCandidate = workflowAuthoringAgentCandidate{
			id:        defaultID,
			isDefault: true,
		}
	}
	capacity := workflows.MaxWorkflowAuthoringAgents
	if hasSafeDefault {
		capacity--
	}
	candidates := make(workflowAuthoringAgentMaxHeap, 0, capacity)
	eligible := 0
	unsafe := false
	for id, instance := range registry.agents {
		if err := ctx.Err(); err != nil {
			registry.mu.RUnlock()
			return nil, limits, err
		}
		if instance == nil || !workflows.SafeWorkflowAuthoringAgentID(id) {
			unsafe = true
			continue
		}
		eligible++
		if hasSafeDefault && id == defaultID && instance == defaultInstance {
			continue
		}
		retainWorkflowAuthoringAgentCandidate(
			&candidates,
			workflowAuthoringAgentCandidate{id: id},
			capacity,
		)
	}
	registry.mu.RUnlock()

	selected := make([]workflowAuthoringAgentCandidate, 0, len(candidates)+1)
	selected = append(selected, candidates...)
	if hasSafeDefault {
		selected = append(selected, defaultCandidate)
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].id < selected[j].id })

	selectedCounts := make(map[string]int, len(selected))
	for _, candidate := range selected {
		selectedCounts[candidate.id] = 0
	}
	if al.cfg != nil && len(al.cfg.Agents.List) > 0 {
		for _, configured := range al.cfg.Agents.List {
			if err := ctx.Err(); err != nil {
				return nil, limits, err
			}
			id := routing.NormalizeAgentID(configured.ID)
			if _, retained := selectedCounts[id]; retained {
				selectedCounts[id]++
			}
		}
	}

	out := make([]workflows.WorkflowAuthoringAgentCapability, 0, len(selected))
	for _, candidate := range selected {
		readiness := workflows.WorkflowDependencyReadinessReady
		switch {
		case al.cfg == nil:
			readiness = workflows.WorkflowDependencyReadinessUnavailable
		case len(al.cfg.Agents.List) == 0 && candidate.id != routing.DefaultAgentID:
			readiness = workflows.WorkflowDependencyReadinessNotFound
		case selectedCounts[candidate.id] > 1:
			readiness = workflows.WorkflowDependencyReadinessNameCollision
		}
		out = append(out, workflows.WorkflowAuthoringAgentCapability{
			ID:        candidate.id,
			Target:    "agent/" + candidate.id,
			IsDefault: candidate.isDefault,
			Readiness: readiness,
		})
	}
	if eligible > workflows.MaxWorkflowAuthoringAgents {
		limits = append(limits, workflows.WorkflowAuthoringAgentsTruncated)
	}
	if unsafe {
		limits = append(limits, workflows.WorkflowAuthoringUnsafeFieldsOmitted)
	}
	return out, limits, nil
}

type workflowAuthoringAgentCandidate struct {
	id        string
	isDefault bool
}

type workflowAuthoringAgentMaxHeap []workflowAuthoringAgentCandidate

func (values *workflowAuthoringAgentMaxHeap) Len() int {
	return len(*values)
}

func (values *workflowAuthoringAgentMaxHeap) Less(i, j int) bool {
	return (*values)[i].id > (*values)[j].id
}

func (values *workflowAuthoringAgentMaxHeap) Swap(i, j int) {
	(*values)[i], (*values)[j] = (*values)[j], (*values)[i]
}

func (values *workflowAuthoringAgentMaxHeap) Push(value any) {
	*values = append(*values, value.(workflowAuthoringAgentCandidate))
}

func (values *workflowAuthoringAgentMaxHeap) Pop() any {
	old := *values
	last := old[len(old)-1]
	*values = old[:len(old)-1]
	return last
}

func retainWorkflowAuthoringAgentCandidate(
	values *workflowAuthoringAgentMaxHeap,
	candidate workflowAuthoringAgentCandidate,
	maximum int,
) {
	if maximum <= 0 {
		return
	}
	if values.Len() < maximum {
		heap.Push(values, candidate)
		return
	}
	if candidate.id >= (*values)[0].id {
		return
	}
	heap.Pop(values)
	heap.Push(values, candidate)
}

func (al *AgentLoop) projectWorkflowAuthoringTools(
	ctx context.Context,
	defaultAgent *AgentInstance,
	mcpStatus workflows.WorkflowAuthoringMCPStatus,
	shapeSanitizer *workflows.WorkflowAuthoringShapeSanitizer,
	limits []workflows.WorkflowAuthoringLimitCode,
) (
	[]workflows.WorkflowAuthoringToolCapability,
	[]workflows.WorkflowAuthoringMCPToolCapability,
	[]workflows.WorkflowAuthoringLimitCode,
	error,
) {
	if defaultAgent == nil || defaultAgent.Tools == nil {
		return nil, nil, limits, fmt.Errorf("default agent tools unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	toolCandidates := make(workflowAuthoringToolMaxHeap, 0, workflows.MaxWorkflowAuthoringTools)
	toolEligible := 0
	unsafe := false
	err := defaultAgent.Tools.VisitCoreTools(ctx, func(entry tools.CoreToolSnapshotEntry) bool {
		if wrapped, isMCP := entry.Tool.(*integrationtools.MCPTool); isMCP {
			if mcpStatus != workflows.WorkflowAuthoringMCPReady {
				return true
			}
			server, toolName, ok := safeWorkflowMCPIdentity(wrapped)
			if !ok ||
				!workflows.SafeWorkflowAuthoringIdentity(server) ||
				!workflows.SafeWorkflowAuthoringIdentity(toolName) ||
				strings.Contains(server, "/") ||
				strings.Contains(toolName, "/") {
				unsafe = true
				return true
			}
			if entry.Name != mcp.CanonicalToolName(server, toolName) {
				unsafe = true
			}
			return true
		}

		if !workflows.SafeWorkflowAuthoringIdentity(entry.Name) {
			unsafe = true
			return true
		}
		if strings.EqualFold(entry.Name, "workflow") {
			return true
		}
		toolEligible++
		retainWorkflowAuthoringToolCandidate(
			&toolCandidates,
			workflowAuthoringToolCandidate{
				entry:   entry,
				primary: entry.Name,
			},
			workflows.MaxWorkflowAuthoringTools,
		)
		return true
	})
	if err != nil {
		return nil, nil, limits, err
	}
	mcpCandidates := workflowAuthoringToolMaxHeap{}
	mcpEligible := 0
	if mcpStatus == workflows.WorkflowAuthoringMCPReady {
		var mcpUnsafe bool
		mcpCandidates,
			mcpEligible,
			mcpUnsafe,
			err = al.selectReadyWorkflowAuthoringMCPTools(ctx, defaultAgent)
		if err != nil {
			return nil, nil, limits, err
		}
		unsafe = unsafe || mcpUnsafe
	}
	if toolEligible > workflows.MaxWorkflowAuthoringTools {
		limits = append(limits, workflows.WorkflowAuthoringToolsTruncated)
	}
	if mcpEligible > workflows.MaxWorkflowAuthoringMCPTools {
		limits = append(limits, workflows.WorkflowAuthoringMCPToolsTruncated)
	}
	if unsafe {
		limits = append(limits, workflows.WorkflowAuthoringUnsafeFieldsOmitted)
	}

	sort.Slice(toolCandidates, func(i, j int) bool {
		return workflowAuthoringToolCandidateLess(toolCandidates[i], toolCandidates[j])
	})
	toolCapabilities := make(
		[]workflows.WorkflowAuthoringToolCapability,
		0,
		len(toolCandidates),
	)
	for _, candidate := range toolCandidates {
		if err := ctx.Err(); err != nil {
			return nil, nil, limits, err
		}
		target := "tool/" + candidate.primary
		shape, projected := projectWorkflowToolParameters(candidate.entry.Tool, shapeSanitizer)
		if !projected {
			limits = append(limits, workflows.WorkflowAuthoringParameterShapesOmitted)
		}
		toolCapabilities = append(toolCapabilities, workflows.WorkflowAuthoringToolCapability{
			Name:                    candidate.primary,
			Target:                  target,
			Readiness:               workflows.WorkflowDependencyReadinessReady,
			ParameterShapeProjected: projected,
			ParameterShape:          shape,
		})
	}

	sort.Slice(mcpCandidates, func(i, j int) bool {
		return workflowAuthoringToolCandidateLess(mcpCandidates[i], mcpCandidates[j])
	})
	mcpCapabilities := make(
		[]workflows.WorkflowAuthoringMCPToolCapability,
		0,
		len(mcpCandidates),
	)
	for _, candidate := range mcpCandidates {
		if err := ctx.Err(); err != nil {
			return nil, nil, limits, err
		}
		target := "mcp/" + candidate.primary + "/" + candidate.secondary
		shape, projected := projectWorkflowToolParameters(
			candidate.entry.Tool,
			shapeSanitizer,
		)
		if !projected {
			limits = append(limits, workflows.WorkflowAuthoringParameterShapesOmitted)
		}
		mcpCapabilities = append(mcpCapabilities, workflows.WorkflowAuthoringMCPToolCapability{
			Server:                  candidate.primary,
			Tool:                    candidate.secondary,
			Target:                  target,
			Readiness:               workflows.WorkflowDependencyReadinessReady,
			ParameterShapeProjected: projected,
			ParameterShape:          shape,
		})
	}
	return toolCapabilities, mcpCapabilities, limits, nil
}

type workflowAuthoringToolCandidate struct {
	entry     tools.CoreToolSnapshotEntry
	primary   string
	secondary string
}

type workflowAuthoringToolMaxHeap []workflowAuthoringToolCandidate

func (values *workflowAuthoringToolMaxHeap) Len() int {
	return len(*values)
}

func (values *workflowAuthoringToolMaxHeap) Less(i, j int) bool {
	return workflowAuthoringToolCandidateLess((*values)[j], (*values)[i])
}

func (values *workflowAuthoringToolMaxHeap) Swap(i, j int) {
	(*values)[i], (*values)[j] = (*values)[j], (*values)[i]
}

func (values *workflowAuthoringToolMaxHeap) Push(value any) {
	*values = append(*values, value.(workflowAuthoringToolCandidate))
}

func (values *workflowAuthoringToolMaxHeap) Pop() any {
	old := *values
	last := old[len(old)-1]
	*values = old[:len(old)-1]
	return last
}

func workflowAuthoringToolCandidateLess(
	left, right workflowAuthoringToolCandidate,
) bool {
	if left.primary == right.primary {
		return left.secondary < right.secondary
	}
	return left.primary < right.primary
}

func retainWorkflowAuthoringToolCandidate(
	values *workflowAuthoringToolMaxHeap,
	candidate workflowAuthoringToolCandidate,
	maximum int,
) (duplicate bool) {
	if containsWorkflowAuthoringToolCandidate(*values, candidate) {
		return true
	}
	if maximum <= 0 {
		return false
	}
	if values.Len() < maximum {
		heap.Push(values, candidate)
		return false
	}
	if !workflowAuthoringToolCandidateLess(candidate, (*values)[0]) {
		return false
	}
	heap.Pop(values)
	heap.Push(values, candidate)
	return false
}

func containsWorkflowAuthoringToolCandidate(
	values workflowAuthoringToolMaxHeap,
	candidate workflowAuthoringToolCandidate,
) bool {
	for _, retained := range values {
		if retained.primary == candidate.primary &&
			retained.secondary == candidate.secondary {
			return true
		}
	}
	return false
}

func (al *AgentLoop) selectReadyWorkflowAuthoringMCPTools(
	ctx context.Context,
	defaultAgent *AgentInstance,
) (workflowAuthoringToolMaxHeap, int, bool, error) {
	candidates := make(
		workflowAuthoringToolMaxHeap,
		0,
		workflows.MaxWorkflowAuthoringMCPTools,
	)
	manager := al.mcp.getManager()
	if al.cfg == nil || manager == nil || defaultAgent == nil || defaultAgent.Tools == nil {
		return candidates, 0, false, nil
	}

	truncated := false
	unsafe := false
	identitySafe, err := manager.VisitWorkflowAuthoringServers(
		ctx,
		func(serverName string, connection *mcp.ServerConnection) bool {
			if connection == nil {
				return true
			}
			for _, tool := range connection.Tools {
				if ctx.Err() != nil {
					return false
				}
				if tool == nil {
					continue
				}
				if !workflows.SafeWorkflowAuthoringIdentity(serverName) ||
					!workflows.SafeWorkflowAuthoringIdentity(tool.Name) ||
					strings.Contains(serverName, "/") ||
					strings.Contains(tool.Name, "/") {
					unsafe = true
					continue
				}
				serverConfig, configured := al.cfg.Tools.MCP.Servers[serverName]
				if !configured ||
					!serverConfig.Enabled ||
					!defaultAgent.AllowsMCPServer(serverName) ||
					serverIsDeferred(al.cfg.Tools.MCP.Discovery.Enabled, serverConfig) {
					continue
				}
				canonical := mcp.CanonicalToolName(serverName, tool.Name)
				registered, core := defaultAgent.Tools.GetCoreTool(canonical)
				if !core {
					continue
				}
				if !workflowMCPToolMatches(registered, serverName, tool.Name) {
					unsafe = true
					continue
				}
				candidate := workflowAuthoringToolCandidate{
					entry: tools.CoreToolSnapshotEntry{
						Name: canonical,
						Tool: registered,
					},
					primary:   serverName,
					secondary: tool.Name,
				}
				if containsWorkflowAuthoringToolCandidate(candidates, candidate) {
					continue
				}
				if candidates.Len() >= workflows.MaxWorkflowAuthoringMCPTools {
					truncated = true
				}
				retainWorkflowAuthoringToolCandidate(
					&candidates,
					candidate,
					workflows.MaxWorkflowAuthoringMCPTools,
				)
			}
			return true
		},
	)
	if err != nil {
		return nil, 0, unsafe, err
	}
	if !identitySafe {
		return candidates[:0], 0, true, nil
	}
	eligible := len(candidates)
	if truncated {
		eligible = workflows.MaxWorkflowAuthoringMCPTools + 1
	}
	return candidates, eligible, unsafe, nil
}

func projectWorkflowAuthoringFunctions(
	limits []workflows.WorkflowAuthoringLimitCode,
) (
	[]workflows.WorkflowAuthoringFunctionCapability,
	[]workflows.WorkflowAuthoringLimitCode,
) {
	names := workflows.NativeFunctionNames()
	out := make([]workflows.WorkflowAuthoringFunctionCapability, 0, min(
		len(names),
		workflows.MaxWorkflowAuthoringFunctions,
	))
	for _, name := range names {
		if !workflows.SafeWorkflowAuthoringIdentity(name) {
			limits = append(limits, workflows.WorkflowAuthoringUnsafeFieldsOmitted)
			continue
		}
		target := "function/" + name
		if !workflows.SafeWorkflowAuthoringTarget(target) {
			limits = append(limits, workflows.WorkflowAuthoringUnsafeFieldsOmitted)
			continue
		}
		if len(out) >= workflows.MaxWorkflowAuthoringFunctions {
			limits = append(limits, workflows.WorkflowAuthoringFunctionsTruncated)
			continue
		}
		out = append(out, workflows.WorkflowAuthoringFunctionCapability{
			Name:      name,
			Target:    target,
			Readiness: workflows.WorkflowDependencyReadinessReady,
		})
	}
	return out, limits
}

func safeWorkflowMCPIdentity(
	tool *integrationtools.MCPTool,
) (server string, name string, ok bool) {
	if tool == nil {
		return "", "", false
	}
	defer func() {
		if recover() != nil {
			server = ""
			name = ""
			ok = false
		}
	}()
	server, name = tool.MCPIdentity()
	return server, name, true
}

func projectWorkflowToolParameters(
	tool tools.Tool,
	sanitizer *workflows.WorkflowAuthoringShapeSanitizer,
) (shape *workflows.WorkflowAuthoringParameterShape, projected bool) {
	if tool == nil || sanitizer == nil {
		return nil, false
	}
	var parameters map[string]any
	ok := true
	func() {
		defer func() {
			if recover() != nil {
				ok = false
			}
		}()
		parameters = tool.Parameters()
	}()
	if !ok {
		return nil, false
	}
	return sanitizer.Project(parameters)
}
