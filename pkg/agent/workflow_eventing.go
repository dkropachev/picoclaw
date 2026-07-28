package agent

import (
	"context"
	"fmt"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

// NewEventWorkflowExecutor builds the current gateway-owned executor used by
// the durable external-event dispatcher. Callers rebuild it after config or
// provider reload so it never captures stale registry/config state. Runtime
// hooks and MCP tools are initialized synchronously before the executor can be
// handed to background workers.
func NewEventWorkflowExecutor(ctx context.Context, al *AgentLoop) (*workflows.Executor, error) {
	if al == nil {
		return nil, fmt.Errorf("agent loop is required for event workflows")
	}
	cfg := al.GetConfig()
	if cfg == nil {
		return nil, fmt.Errorf("agent config is required for event workflows")
	}
	registry := al.GetRegistry()
	if registry == nil {
		return nil, fmt.Errorf("agent registry is required for event workflows")
	}
	defaultAgent := registry.GetDefaultAgent()
	if defaultAgent == nil {
		return nil, fmt.Errorf("default agent is required for event workflows")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := al.ensureHooksInitialized(ctx); err != nil {
		return nil, fmt.Errorf("initialize event workflow hooks: %w", err)
	}
	if err := al.ensureMCPInitialized(ctx); err != nil {
		return nil, fmt.Errorf("initialize event workflow MCP tools: %w", err)
	}
	return al.newWorkflowExecutor(cfg.WorkspacePath(), defaultAgent), nil
}
