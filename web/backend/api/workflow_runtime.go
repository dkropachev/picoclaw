package api

import (
	"context"
	"fmt"
	"strings"
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

type workflowDependencyRuntime interface {
	workflows.WorkflowDependencyRuntimeResolver
	Close() error
}

var newWorkflowDependencyRuntime = func(
	configPath string,
	cfg *config.Config,
) workflowDependencyRuntime {
	return &webWorkflowRuntimeRunner{configPath: configPath, config: cfg}
}

var newWorkflowRuntimeRunners = func(configPath string) workflowRuntimeRunners {
	runner := &webWorkflowRuntimeRunner{configPath: configPath}
	return workflowRuntimeRunners{
		Tools:         runner,
		Agents:        runner,
		RuntimeEvents: runner,
	}
}

func workflowRuntimeRunnersForConfig(
	configPath string,
	cfg *config.Config,
) workflowRuntimeRunners {
	runners := newWorkflowRuntimeRunners(configPath)
	bindWorkflowRuntimeRunnerConfig(runners.Tools, cfg)
	bindWorkflowRuntimeRunnerConfig(runners.Agents, cfg)
	bindWorkflowRuntimeRunnerConfig(runners.RuntimeEvents, cfg)
	return runners
}

func bindWorkflowRuntimeRunnerConfig(runner any, cfg *config.Config) {
	if concrete, ok := runner.(*webWorkflowRuntimeRunner); ok {
		concrete.config = cfg
	}
}

type webWorkflowRuntimeRunner struct {
	configPath    string
	config        *config.Config
	mu            sync.Mutex
	msgBus        *bus.MessageBus
	loop          *agentloop.AgentLoop
	initializeMCP func(context.Context, *agentloop.AgentLoop) error
}

var (
	_ workflows.ReadOnlySessionCapturer         = (*webWorkflowRuntimeRunner)(nil)
	_ workflows.RepositoryReviewProfileResolver = (*webWorkflowRuntimeRunner)(nil)
)

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

func (r *webWorkflowRuntimeRunner) ResolveRepositoryReviewProfile(
	ctx context.Context,
	agentID string,
	requestedAccountRef string,
	requestedReviewerModels []string,
) (workflows.RepositoryReviewModelProfile, error) {
	if r == nil {
		return workflows.RepositoryReviewModelProfile{}, fmt.Errorf("agent runner not configured")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureLoopLocked(); err != nil {
		return workflows.RepositoryReviewModelProfile{}, err
	}
	resolver, ok := agentloop.NewWorkflowAgentRunner(r.loop).(workflows.RepositoryReviewProfileResolver)
	if !ok {
		return workflows.RepositoryReviewModelProfile{}, fmt.Errorf(
			"agent runner does not support repository review profiles",
		)
	}
	return resolver.ResolveRepositoryReviewProfile(
		ctx,
		agentID,
		requestedAccountRef,
		requestedReviewerModels,
	)
}

func (r *webWorkflowRuntimeRunner) CaptureReadOnlySession(
	ctx context.Context,
	ref workflows.ReadOnlySessionRef,
) (*workflows.FrozenReadOnlySession, error) {
	if r == nil {
		return nil, fmt.Errorf("agent runner not configured")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureLoopLocked(); err != nil {
		return nil, err
	}
	runner := agentloop.NewWorkflowAgentRunner(r.loop)
	capturer, ok := runner.(workflows.ReadOnlySessionCapturer)
	if !ok {
		return nil, fmt.Errorf("agent runner does not support read-only session capture")
	}
	return capturer.CaptureReadOnlySession(ctx, ref)
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
	if req.MCP {
		initializeMCP := r.initializeMCP
		if initializeMCP == nil {
			initializeMCP = func(ctx context.Context, loop *agentloop.AgentLoop) error {
				return loop.EnsureMCPInitialized(ctx)
			}
		}
		if err := initializeMCP(ctx, r.loop); err != nil {
			return nil, fmt.Errorf("failed to initialize MCP for workflow tool step: %w", err)
		}
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

func (r *webWorkflowRuntimeRunner) ResolveWorkflowDependency(
	ctx context.Context,
	dependency workflows.WorkflowDependencyOccurrence,
) workflows.WorkflowDependencyReadinessCode {
	if r == nil {
		return workflows.WorkflowDependencyReadinessUnavailable
	}
	if dependency.Kind == workflows.WorkflowDependencyKindReusable {
		return workflows.WorkflowDependencyReadinessReady
	}
	if dependency.Kind == workflows.WorkflowDependencyKindHuman {
		if strings.TrimSpace(dependency.Name) == "task" {
			return workflows.WorkflowDependencyReadinessReady
		}
		return workflows.WorkflowDependencyReadinessNotFound
	}
	if dependency.Kind == workflows.WorkflowDependencyKindFunction &&
		workflows.IsNativeFunction(dependency.Name) {
		return workflows.WorkflowDependencyReadinessReady
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureLoopLocked(); err != nil {
		return workflows.WorkflowDependencyReadinessInvalidConfiguration
	}
	return r.loop.ResolveWorkflowDependency(ctx, dependency)
}

func (r *webWorkflowRuntimeRunner) ensureLoopLocked() error {
	if r.loop != nil {
		return nil
	}
	cfg := r.config
	if cfg == nil {
		var err error
		cfg, err = config.LoadConfig(r.configPath)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
	}
	provider, _, err := providers.CreateProvider(cfg)
	if err != nil {
		return fmt.Errorf("failed to create provider: %w", err)
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
