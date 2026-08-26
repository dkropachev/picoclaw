package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type runtimeGateProvider struct {
	name   string
	closed chan struct{}
	called chan struct{}
}

func (p *runtimeGateProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	if p.called != nil {
		select {
		case p.called <- struct{}{}:
		default:
		}
	}
	return &providers.LLMResponse{Content: p.name}, nil
}

func (p *runtimeGateProvider) Close() {
	select {
	case <-p.closed:
	default:
		close(p.closed)
	}
}

func TestAgentLoopStopBeforeRunPreventsLateStartup(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	msgBus := bus.NewMessageBus()
	provider := &runtimeGateProvider{name: "provider", closed: make(chan struct{})}
	al := newTestAgentLoopWithStrictModels(cfg, msgBus, provider)

	// Model the gateway shutdown winning immediately after spawning Run but
	// before the goroutine is scheduled. Stop is terminal and must be
	// remembered across that registration gap.
	al.Stop()
	msgBus.Close()
	al.Close()
	if err := al.Run(context.Background()); err != nil {
		t.Fatalf("late AgentLoop.Run() error = %v", err)
	}
	if al.running.Load() {
		t.Fatal("late AgentLoop.Run() started after terminal Stop")
	}
	al.runLifecycleMu.Lock()
	done := al.runDone
	al.runLifecycleMu.Unlock()
	if done != nil {
		t.Fatal("late AgentLoop.Run() registered after terminal Stop")
	}
}

func TestAgentLoopStopRejectsNewRootRuntimeButAllowsRetainedChildren(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	al := newTestAgentLoopWithStrictModels(
		cfg,
		bus.NewMessageBus(),
		&runtimeGateProvider{name: "provider", closed: make(chan struct{})},
	)
	defer al.Close()

	rootCtx, releaseRoot, err := al.acquireRuntimeUse(context.Background())
	if err != nil {
		t.Fatalf("acquireRuntimeUse() error = %v", err)
	}
	al.Stop()

	_, releaseChild, err := al.retainRuntimeUse(rootCtx)
	if err != nil {
		releaseRoot()
		t.Fatalf("retainRuntimeUse() for admitted parent error = %v", err)
	}
	if _, _, err = al.acquireRuntimeUse(context.Background()); !errors.Is(err, errAgentRuntimeStopped) {
		releaseChild()
		releaseRoot()
		t.Fatalf("fresh acquireRuntimeUse() error = %v, want %v", err, errAgentRuntimeStopped)
	}
	releaseChild()
	releaseRoot()
}

func TestAgentLoopStopWakesPausedRootRuntimeAdmission(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	al := newTestAgentLoopWithStrictModels(
		cfg,
		bus.NewMessageBus(),
		&runtimeGateProvider{name: "provider", closed: make(chan struct{})},
	)
	defer al.Close()

	resume, err := al.PauseRuntimeForReload(context.Background())
	if err != nil {
		t.Fatalf("PauseRuntimeForReload() error = %v", err)
	}
	defer resume()

	admissionDone := make(chan error, 1)
	go func() {
		_, _, acquireErr := al.acquireRuntimeUse(context.Background())
		admissionDone <- acquireErr
	}()

	al.Stop()
	select {
	case acquireErr := <-admissionDone:
		if !errors.Is(acquireErr, errAgentRuntimeStopped) {
			t.Fatalf("paused acquireRuntimeUse() error = %v, want %v", acquireErr, errAgentRuntimeStopped)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not wake paused root runtime admission")
	}
}

func TestReloadDrainsRuntimeGenerationBeforeReturningRetainedProvider(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	providerA := &runtimeGateProvider{name: "provider-a", closed: make(chan struct{})}
	providerB := &runtimeGateProvider{name: "provider-b", closed: make(chan struct{})}
	al := newTestAgentLoopWithStrictModels(cfg, bus.NewMessageBus(), providerA)
	defer al.Close()
	factory := reloadToolRegistryLeaseFactory(t)
	liveA := &reloadToolRegistryLeaseProbe{marker: 10}
	agentA := al.GetRegistry().GetDefaultAgent()
	if agentA == nil || agentA.Tools == nil {
		t.Fatal("generation A tool registry is unavailable")
	}
	if registerErr := agentA.Tools.RegisterFactoryBacked(liveA, factory); registerErr != nil {
		t.Fatal(registerErr)
	}
	competitorA := tools.NewToolRegistry()
	if registerErr := competitorA.RegisterFactoryBacked(liveA, factory); registerErr == nil {
		t.Fatal("generation A compatibility lease was not retained")
	}

	previous, err := al.ReloadProviderAndConfigRetainingPrevious(
		context.Background(),
		providerB,
		cfg,
	)
	if err != nil {
		t.Fatalf("reload A -> B error = %v", err)
	}
	if previous != providerA {
		t.Fatalf("reload A -> B retained %T, want provider A", previous)
	}
	if registerErr := competitorA.RegisterFactoryBacked(liveA, factory); registerErr != nil {
		t.Fatalf("reload A -> B did not release generation A tool lease: %v", registerErr)
	}
	if closeErr := competitorA.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	select {
	case <-providerA.closed:
		t.Fatal("retained provider A was closed with its agent registry")
	default:
	}
	if response, chatErr := providerA.Chat(
		context.Background(),
		nil,
		nil,
		"",
		nil,
	); chatErr != nil || response == nil ||
		response.Content != "provider-a" {
		t.Fatalf("retained provider A is unusable: %#v, %v", response, chatErr)
	}

	_, releaseRuntime, err := al.acquireRuntimeUse(context.Background())
	if err != nil {
		t.Fatalf("acquireRuntimeUse() error = %v", err)
	}
	captured := al.GetRegistry().GetDefaultAgent()
	if captured == nil || captured.Provider != providerB {
		releaseRuntime()
		t.Fatalf("captured agent provider = %v, want provider B", captured)
	}
	liveB := &reloadToolRegistryLeaseProbe{marker: 11}
	if registerErr := captured.Tools.RegisterFactoryBacked(liveB, factory); registerErr != nil {
		releaseRuntime()
		t.Fatal(registerErr)
	}
	competitorB := tools.NewToolRegistry()
	if registerErr := competitorB.RegisterFactoryBacked(liveB, factory); registerErr == nil {
		releaseRuntime()
		t.Fatal("generation B compatibility lease was not retained")
	}

	rollbackDone := make(chan struct {
		provider providers.LLMProvider
		err      error
	}, 1)
	go func() {
		retained, reloadErr := al.ReloadProviderAndConfigRetainingPrevious(
			context.Background(),
			providerA,
			cfg,
		)
		rollbackDone <- struct {
			provider providers.LLMProvider
			err      error
		}{provider: retained, err: reloadErr}
	}()

	select {
	case result := <-rollbackDone:
		releaseRuntime()
		t.Fatalf("rollback returned before captured B runtime drained: %v", result.err)
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case <-providerB.closed:
		releaseRuntime()
		t.Fatal("provider B closed while its runtime generation was active")
	default:
	}

	releaseRuntime()
	var result struct {
		provider providers.LLMProvider
		err      error
	}
	select {
	case result = <-rollbackDone:
	case <-time.After(2 * time.Second):
		t.Fatal("rollback did not finish after runtime generation drained")
	}
	if result.err != nil {
		t.Fatalf("rollback B -> A error = %v", result.err)
	}
	if result.provider != providerB {
		t.Fatalf("rollback retained %T, want provider B", result.provider)
	}
	if registerErr := competitorB.RegisterFactoryBacked(liveB, factory); registerErr != nil {
		t.Fatalf("rollback did not release generation B tool lease: %v", registerErr)
	}
	if closeErr := competitorB.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	select {
	case <-providerB.closed:
		t.Fatal("retained provider B closed before explicit disposal")
	default:
	}

	al.CloseRetainedProvider(context.Background(), result.provider)
	select {
	case <-providerB.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("provider B was not closed after rollback and drain")
	}
}

type runtimeGateBlockingWorkflowTool struct {
	started chan struct{}
	release chan struct{}
}

func (t *runtimeGateBlockingWorkflowTool) Name() string {
	return "runtime_gate_block"
}

func (t *runtimeGateBlockingWorkflowTool) Description() string {
	return "blocks a workflow until released"
}

func (t *runtimeGateBlockingWorkflowTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (t *runtimeGateBlockingWorkflowTool) Execute(
	ctx context.Context,
	_ map[string]any,
) *tools.ToolResult {
	select {
	case <-t.started:
	default:
		close(t.started)
	}
	select {
	case <-t.release:
		return tools.NewToolResult("released")
	case <-ctx.Done():
		return tools.ErrorResult(ctx.Err().Error()).WithError(ctx.Err())
	}
}

func TestChannelWorkflowRetainsRuntimeAfterTriggerReturns(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	workspace := t.TempDir()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Workflows.Enabled = true
	providerA := &runtimeGateProvider{name: "provider-a", closed: make(chan struct{})}
	providerB := &runtimeGateProvider{name: "provider-b", closed: make(chan struct{})}
	al := newTestAgentLoopWithStrictModels(cfg, bus.NewMessageBus(), providerA)
	defer al.Close()

	blockingTool := &runtimeGateBlockingWorkflowTool{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	al.GetRegistry().GetDefaultAgent().Tools.Register(blockingTool)
	workflowDir := filepath.Join(workspace, workflows.DefaultDefinitionsDir)
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workflowDir, "gate.yml"), []byte(`
name: Runtime gate
on:
  channel_message:
    channels: test
    passthrough: false
jobs:
  gate:
    runs-on: picoclaw
    steps:
      - uses: tool/runtime_gate_block
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := workflows.RevalidateLocal(
		context.Background(),
		workspace,
		workflowRuntimeCompatibility(),
	); err != nil {
		t.Fatalf("RevalidateLocal() error = %v", err)
	}

	consumed := al.handleWorkflowTriggers(context.Background(), bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  "test",
			ChatID:   "gate-chat",
			ChatType: "direct",
			SenderID: "user",
		},
		Content: "run gate workflow",
	})
	if !consumed {
		t.Fatal("handleWorkflowTriggers() consumed = false, want true")
	}
	select {
	case <-blockingTool.started:
	case <-time.After(2 * time.Second):
		t.Fatal("workflow tool did not start")
	}

	reloadDone := make(chan error, 1)
	go func() {
		reloadDone <- al.ReloadProviderAndConfig(context.Background(), providerB, cfg)
	}()
	select {
	case err := <-reloadDone:
		t.Fatalf("reload returned while async workflow was active: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case <-providerA.closed:
		t.Fatal("provider A closed while async workflow retained its runtime")
	default:
	}

	close(blockingTool.release)
	select {
	case err := <-reloadDone:
		if err != nil {
			t.Fatalf("ReloadProviderAndConfig() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reload did not finish after workflow release")
	}
	select {
	case <-providerA.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("provider A was not closed after workflow drained")
	}
}

func TestInboundMessageRetainsRoutingGenerationWhileWaitingForWorker(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	newConfig := func(agentID string) *config.Config {
		cfg := config.DefaultConfig()
		cfg.Agents.Defaults.Workspace = workspace
		cfg.Agents.Defaults.MaxParallelTurns = 1
		cfg.Agents.List = []config.AgentConfig{
			{ID: "main", Default: true},
			{ID: "alpha"},
			{ID: "beta"},
		}
		cfg.Agents.Dispatch = &config.DispatchConfig{
			Rules: []config.DispatchRule{{
				Name:  "generation-route",
				Agent: agentID,
				When:  config.DispatchSelector{Channel: "test"},
			}},
		}
		// Exercise the workflow-trigger decision immediately before normal
		// routing without adding a matching workflow that would itself retain
		// the generation.
		cfg.Workflows.Enabled = true
		return cfg
	}
	cfgA := newConfig("alpha")
	cfgB := newConfig("beta")
	providerA := &runtimeGateProvider{
		name:   "provider-a",
		closed: make(chan struct{}),
		called: make(chan struct{}, 1),
	}
	providerB := &runtimeGateProvider{
		name:   "provider-b",
		closed: make(chan struct{}),
		called: make(chan struct{}, 1),
	}
	msgBus := bus.NewMessageBus()
	al := newTestAgentLoopWithStrictModels(cfgA, msgBus, providerA)
	defer al.Close()

	// Saturate the only worker slot so the admitted message must wait after
	// its trigger decision, route resolution, and placeholder claim.
	al.workerSem <- struct{}{}
	workerSlotHeld := true
	defer func() {
		if workerSlotHeld {
			<-al.workerSem
		}
	}()

	msg := bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  "test",
			ChatID:   "generation-chat",
			ChatType: "direct",
			SenderID: "user",
		},
		Content: "keep one generation",
	}
	sessionKeyA, agentIDA, ok := al.resolveSteeringTarget(msg)
	if !ok || agentIDA != "alpha" {
		t.Fatalf("generation A route = %q, %v, want alpha", agentIDA, ok)
	}
	agentA, ok := al.GetRegistry().GetAgent("alpha")
	if !ok {
		t.Fatal("generation A alpha agent not found")
	}

	runCtx, cancelRun := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		runDone <- al.Run(runCtx)
	}()
	defer func() {
		cancelRun()
		al.Stop()
		select {
		case runErr := <-runDone:
			if runErr != nil {
				t.Errorf("AgentLoop.Run() error = %v", runErr)
			}
		case <-time.After(2 * time.Second):
			t.Errorf("AgentLoop.Run() did not stop")
		}
	}()
	if err := msgBus.PublishInbound(context.Background(), msg); err != nil {
		t.Fatalf("PublishInbound() error = %v", err)
	}

	placeholderDeadline := time.After(2 * time.Second)
	for al.getActiveTurnState(sessionKeyA) == nil {
		select {
		case <-placeholderDeadline:
			t.Fatal("message did not claim its generation A session")
		case <-time.After(time.Millisecond):
		}
	}

	reloadDone := make(chan error, 1)
	go func() {
		reloadDone <- al.ReloadProviderAndConfig(context.Background(), providerB, cfgB)
	}()
	select {
	case reloadErr := <-reloadDone:
		t.Fatalf("reload crossed queued generation A message: %v", reloadErr)
	case <-time.After(100 * time.Millisecond):
	}

	<-al.workerSem
	workerSlotHeld = false
	select {
	case <-providerA.called:
	case <-time.After(2 * time.Second):
		t.Fatal("queued message did not execute with generation A provider")
	}
	select {
	case reloadErr := <-reloadDone:
		if reloadErr != nil {
			t.Fatalf("ReloadProviderAndConfig() error = %v", reloadErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reload did not finish after queued generation A message")
	}

	select {
	case <-providerB.called:
		t.Fatal("queued generation A message executed with generation B provider")
	default:
	}
	if history := agentA.Sessions.GetHistory(sessionKeyA); len(history) == 0 {
		t.Fatal("generation A routed session has no message history")
	}
	sessionKeyB, agentIDB, ok := al.resolveSteeringTarget(msg)
	if !ok || agentIDB != "beta" {
		t.Fatalf("generation B route = %q, %v, want beta", agentIDB, ok)
	}
	agentB, ok := al.GetRegistry().GetAgent("beta")
	if !ok {
		t.Fatal("generation B beta agent not found")
	}
	if history := agentB.Sessions.GetHistory(sessionKeyB); len(history) != 0 {
		t.Fatalf("generation B session history = %#v, want empty", history)
	}
	if state := al.getActiveTurnState(sessionKeyA); state != nil {
		t.Fatalf("generation A placeholder leaked after execution: %#v", state)
	}
}

func TestLegacySummarizerRejectsReloadedWorkspaceGeneration(t *testing.T) {
	t.Parallel()

	cfgA := config.DefaultConfig()
	cfgA.Agents.Defaults.Workspace = t.TempDir()
	cfgA.Agents.Defaults.SummarizeMessageThreshold = 1
	cfgB := config.DefaultConfig()
	cfgB.Agents.Defaults.Workspace = t.TempDir()
	cfgB.Agents.Defaults.SummarizeMessageThreshold = 1
	providerA := &runtimeGateProvider{
		name:   "provider-a",
		closed: make(chan struct{}),
		called: make(chan struct{}, 1),
	}
	providerB := &runtimeGateProvider{
		name:   "provider-b",
		closed: make(chan struct{}),
		called: make(chan struct{}, 1),
	}
	al := newTestAgentLoopWithStrictModels(cfgA, bus.NewMessageBus(), providerA)
	defer al.Close()
	manager := &legacyContextManager{al: al}
	const sessionKey = "summarizer-generation"
	history := []providers.Message{
		{Role: "user", Content: "old one"},
		{Role: "assistant", Content: "old two"},
		{Role: "user", Content: "old three"},
		{Role: "assistant", Content: "old four"},
		{Role: "user", Content: "old five"},
		{Role: "assistant", Content: "old six"},
	}
	agentA := al.GetRegistry().GetDefaultAgent()
	agentA.Sessions.SetHistory(sessionKey, history)
	if err := agentA.Sessions.Save(sessionKey); err != nil {
		t.Fatalf("save generation A history: %v", err)
	}

	resumeRuntime, err := al.PauseRuntimeForReload(context.Background())
	if err != nil {
		t.Fatalf("PauseRuntimeForReload() error = %v", err)
	}
	resumed := false
	defer func() {
		if !resumed {
			resumeRuntime()
		}
	}()
	manager.maybeSummarize(sessionKey)
	summarizeKey := agentA.ID + ":" + sessionKey
	if _, scheduled := manager.summarizing.Load(summarizeKey); !scheduled {
		t.Fatal("generation A summarization was not scheduled")
	}

	if err := al.ReloadProviderAndConfig(context.Background(), providerB, cfgB); err != nil {
		t.Fatalf("ReloadProviderAndConfig() error = %v", err)
	}
	agentB := al.GetRegistry().GetDefaultAgent()
	agentB.Sessions.SetHistory(sessionKey, history)
	if err := agentB.Sessions.Save(sessionKey); err != nil {
		t.Fatalf("save generation B history: %v", err)
	}

	resumeRuntime()
	resumed = true
	deadline := time.After(2 * time.Second)
	for {
		if _, scheduled := manager.summarizing.Load(summarizeKey); !scheduled {
			break
		}
		select {
		case <-deadline:
			t.Fatal("stale summarization did not leave the admission queue")
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case <-providerB.called:
		t.Fatal("generation A summarization called generation B provider")
	default:
	}
	got := agentB.Sessions.GetHistory(sessionKey)
	if len(got) != len(history) {
		t.Fatalf("generation B history length = %d, want %d", len(got), len(history))
	}
	for i := range history {
		if got[i].Role != history[i].Role || got[i].Content != history[i].Content {
			t.Fatalf("generation B history[%d] = %#v, want %#v", i, got[i], history[i])
		}
	}
	if summary := agentB.Sessions.GetSummary(sessionKey); summary != "" {
		t.Fatalf("generation B summary = %q, want empty", summary)
	}
}

type delayedRuntimeGateSpawner struct {
	delegate *AgentLoopSpawner
	entered  chan struct{}
	admit    chan struct{}
}

func (s *delayedRuntimeGateSpawner) PrepareAsyncSubTurn(
	ctx context.Context,
) (context.Context, func(), error) {
	return s.delegate.PrepareAsyncSubTurn(ctx)
}

func (s *delayedRuntimeGateSpawner) SpawnSubTurn(
	ctx context.Context,
	cfg tools.SubTurnConfig,
) (*tools.ToolResult, error) {
	close(s.entered)
	<-s.admit
	return s.delegate.SpawnSubTurn(ctx, cfg)
}

func TestSpawnToolRetainsRuntimeBeforeLaunchingBackgroundSubturn(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	providerA := &runtimeGateProvider{name: "provider-a", closed: make(chan struct{})}
	providerB := &runtimeGateProvider{name: "provider-b", closed: make(chan struct{})}
	al := newTestAgentLoopWithStrictModels(cfg, bus.NewMessageBus(), providerA)
	defer al.Close()

	rootCtx, releaseRoot, err := al.acquireRuntimeUse(context.Background())
	if err != nil {
		t.Fatalf("acquireRuntimeUse() error = %v", err)
	}
	parentAgent := al.GetRegistry().GetDefaultAgent()
	parent := &turnState{
		ctx:            rootCtx,
		turnID:         "runtime-gate-parent",
		agent:          parentAgent,
		session:        newEphemeralSession(nil),
		pendingResults: make(chan *tools.ToolResult, 4),
		concurrencySem: make(chan struct{}, 2),
		opts: processOptions{
			Dispatch: DispatchRequest{SessionKey: "runtime-gate-parent"},
		},
	}
	rootCtx = withTurnState(rootCtx, parent)
	rootCtx = WithAgentLoop(rootCtx, al)
	parent.ctx = rootCtx

	delayed := &delayedRuntimeGateSpawner{
		delegate: NewSubTurnSpawner(al),
		entered:  make(chan struct{}),
		admit:    make(chan struct{}),
	}
	spawnTool := tools.NewSpawnTool(tools.NewSubagentManager(
		providerA,
		parentAgent.Model,
		parentAgent.Workspace,
	))
	spawnTool.SetSpawner(delayed)
	callbackEntered := make(chan struct{})
	releaseCallback := make(chan struct{})
	callbackDone := make(chan struct{})
	result := spawnTool.ExecuteAsync(
		rootCtx,
		map[string]any{"task": "finish", "agent_id": parentAgent.ID},
		func(context.Context, *tools.ToolResult) {
			close(callbackEntered)
			<-releaseCallback
			close(callbackDone)
		},
	)
	if result == nil || result.IsError {
		releaseRoot()
		t.Fatalf("SpawnTool result = %#v, want async acknowledgement", result)
	}
	select {
	case <-delayed.entered:
	case <-time.After(2 * time.Second):
		releaseRoot()
		t.Fatal("background spawn did not reach delayed admission")
	}
	releaseRoot()

	reloadDone := make(chan error, 1)
	go func() {
		reloadDone <- al.ReloadProviderAndConfig(context.Background(), providerB, cfg)
	}()
	select {
	case err := <-reloadDone:
		t.Fatalf("reload returned before background subturn admission: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(delayed.admit)
	select {
	case <-callbackEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("background subturn callback did not start")
	}
	select {
	case err := <-reloadDone:
		t.Fatalf("reload returned before tracked callback completed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseCallback)
	select {
	case <-callbackDone:
	case <-time.After(2 * time.Second):
		t.Fatal("background subturn did not complete while reload was paused")
	}
	select {
	case err := <-reloadDone:
		if err != nil {
			t.Fatalf("ReloadProviderAndConfig() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reload did not finish after background subturn")
	}
	select {
	case <-providerA.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("provider A was not closed after background subturn drained")
	}
}
