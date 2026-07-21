package api

import (
	"context"
	"fmt"
	"sync"

	agentloop "github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type workflowRuntimeRunners struct {
	Tools         workflows.ToolRunner
	Agents        workflows.AgentRunner
	RuntimeEvents workflows.RuntimeEventPublisher
}

var newWorkflowRuntimeRunners = func(configPath string) workflowRuntimeRunners {
	runner := &webWorkflowRuntimeRunner{configPath: configPath}
	return workflowRuntimeRunners{
		Tools:         runner,
		Agents:        runner,
		RuntimeEvents: runner,
	}
}

type webWorkflowRuntimeRunner struct {
	configPath string
	mu         sync.Mutex
	msgBus     *bus.MessageBus
	loop       *agentloop.AgentLoop
}

func (r *webWorkflowRuntimeRunner) RunAgent(ctx context.Context, req workflows.AgentRequest) (map[string]any, error) {
	if r == nil {
		return nil, fmt.Errorf("agent runner not configured")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureLoopLocked(); err != nil {
		return nil, err
	}
	return agentloop.NewWorkflowAgentRunner(r.loop).RunAgent(ctx, req)
}

func (r *webWorkflowRuntimeRunner) RunTool(ctx context.Context, req workflows.ToolRequest) (map[string]any, error) {
	if r == nil {
		return nil, fmt.Errorf("tool runner not configured")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureLoopLocked(); err != nil {
		return nil, err
	}
	runner, err := agentloop.NewWorkflowToolRunner(r.loop, req.AgentID)
	if err != nil {
		return nil, err
	}
	return runner.RunTool(ctx, req)
}

func (r *webWorkflowRuntimeRunner) PublishNonBlocking(evt runtimeevents.Event) runtimeevents.PublishResult {
	if r == nil {
		return runtimeevents.PublishResult{Closed: true}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureLoopLocked(); err != nil {
		return runtimeevents.PublishResult{Closed: true}
	}
	bus := r.loop.RuntimeEventBus()
	if bus == nil {
		return runtimeevents.PublishResult{Closed: true}
	}
	return bus.PublishNonBlocking(evt)
}

func (r *webWorkflowRuntimeRunner) ensureLoopLocked() error {
	if r.loop != nil {
		return nil
	}
	cfg, err := config.LoadConfig(r.configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	provider, modelID, err := providers.CreateProvider(cfg)
	if err != nil {
		return fmt.Errorf("failed to create provider: %w", err)
	}
	if modelID != "" {
		cfg.Agents.Defaults.ModelName = modelID
	}
	r.msgBus = bus.NewMessageBus()
	r.loop = agentloop.NewAgentLoop(
		cfg,
		r.msgBus,
		provider,
		agentloop.WithConfigPath(r.configPath),
	)
	return nil
}

func (r *webWorkflowRuntimeRunner) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loop != nil {
		r.loop.Close()
		r.loop = nil
	}
	if r.msgBus != nil {
		r.msgBus.Close()
		r.msgBus = nil
	}
	return nil
}
