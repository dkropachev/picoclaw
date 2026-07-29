package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type agentTurnUXCall struct {
	channel  string
	chatID   string
	turnUXID string
}

type agentTurnUXRebindCall struct {
	channel      string
	chatID       string
	fromTurnUXID string
	toTurnUXID   string
}

type agentTurnUXMessageBus struct {
	inner           *bus.MessageBus
	failOutbound    atomic.Bool
	blockOutbound   atomic.Bool
	outboundStarted chan struct{}
	outboundRelease chan struct{}
	outboundOnce    sync.Once
}

func (messageBus *agentTurnUXMessageBus) PublishInbound(
	ctx context.Context,
	message bus.InboundMessage,
) error {
	return messageBus.inner.PublishInbound(ctx, message)
}

func (messageBus *agentTurnUXMessageBus) PublishOutbound(
	ctx context.Context,
	message bus.OutboundMessage,
) error {
	if messageBus.failOutbound.Load() {
		return errors.New("forced outbound publish failure")
	}
	if messageBus.blockOutbound.Load() {
		messageBus.outboundOnce.Do(func() {
			if messageBus.outboundStarted != nil {
				close(messageBus.outboundStarted)
			}
		})
		if messageBus.outboundRelease != nil {
			select {
			case <-messageBus.outboundRelease:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return messageBus.inner.PublishOutbound(ctx, message)
}

func (messageBus *agentTurnUXMessageBus) PublishOutboundMedia(
	ctx context.Context,
	message bus.OutboundMediaMessage,
) error {
	return messageBus.inner.PublishOutboundMedia(ctx, message)
}

func (messageBus *agentTurnUXMessageBus) GetStreamer(
	ctx context.Context,
	channel, chatID, sessionKey string,
) (bus.Streamer, bool) {
	return messageBus.inner.GetStreamer(
		ctx,
		channel,
		chatID,
		sessionKey,
	)
}

func (messageBus *agentTurnUXMessageBus) GetStreamerForTurn(
	ctx context.Context,
	channel, chatID, sessionKey, turnUXID string,
) (bus.Streamer, bool) {
	return messageBus.inner.GetStreamerForTurn(
		ctx,
		channel,
		chatID,
		sessionKey,
		turnUXID,
	)
}

func (messageBus *agentTurnUXMessageBus) InboundChan() <-chan bus.InboundMessage {
	return messageBus.inner.InboundChan()
}

type agentTurnUXChannelManager struct {
	mu sync.Mutex

	typingCalls  []agentTurnUXCall
	cleanupCalls []agentTurnUXCall
	rebindCalls  []agentTurnUXRebindCall

	typingCalled  chan agentTurnUXCall
	cleanupCalled chan agentTurnUXCall
	rebindCalled  chan agentTurnUXRebindCall
}

func newAgentTurnUXChannelManager() *agentTurnUXChannelManager {
	return &agentTurnUXChannelManager{
		typingCalled:  make(chan agentTurnUXCall, 8),
		cleanupCalled: make(chan agentTurnUXCall, 8),
		rebindCalled:  make(chan agentTurnUXRebindCall, 8),
	}
}

func (m *agentTurnUXChannelManager) GetChannel(string) (channels.Channel, bool) {
	return nil, false
}

func (m *agentTurnUXChannelManager) GetEnabledChannels() []string {
	return nil
}

func (m *agentTurnUXChannelManager) InvokeTypingStop(string, string) {}

func (m *agentTurnUXChannelManager) InvokeTypingStopForMessage(
	channel, chatID, turnUXID string,
) {
	call := agentTurnUXCall{
		channel:  channel,
		chatID:   chatID,
		turnUXID: turnUXID,
	}
	m.mu.Lock()
	m.typingCalls = append(m.typingCalls, call)
	m.mu.Unlock()
	m.typingCalled <- call
}

func (m *agentTurnUXChannelManager) CleanupTurnUXForMessage(
	_ context.Context,
	channel, chatID, turnUXID string,
) {
	call := agentTurnUXCall{
		channel:  channel,
		chatID:   chatID,
		turnUXID: turnUXID,
	}
	m.mu.Lock()
	m.cleanupCalls = append(m.cleanupCalls, call)
	m.mu.Unlock()
	m.cleanupCalled <- call
}

func (m *agentTurnUXChannelManager) RebindTurnUXForMessage(
	channel, chatID, fromTurnUXID, toTurnUXID string,
) {
	call := agentTurnUXRebindCall{
		channel:      channel,
		chatID:       chatID,
		fromTurnUXID: fromTurnUXID,
		toTurnUXID:   toTurnUXID,
	}
	m.mu.Lock()
	m.rebindCalls = append(m.rebindCalls, call)
	m.mu.Unlock()
	m.rebindCalled <- call
}

func (m *agentTurnUXChannelManager) SendMessage(
	context.Context,
	bus.OutboundMessage,
) error {
	return nil
}

func (m *agentTurnUXChannelManager) SendMedia(
	context.Context,
	bus.OutboundMediaMessage,
) error {
	return nil
}

func (m *agentTurnUXChannelManager) SendPlaceholder(
	context.Context,
	string,
	string,
) bool {
	return false
}

func (m *agentTurnUXChannelManager) SendPlaceholderForMessage(
	context.Context,
	string,
	string,
	string,
) bool {
	return false
}

func (m *agentTurnUXChannelManager) DismissToolFeedback(
	context.Context,
	string,
	string,
	*bus.InboundContext,
) {
}

func (m *agentTurnUXChannelManager) snapshotCalls() (
	typing []agentTurnUXCall,
	cleanup []agentTurnUXCall,
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]agentTurnUXCall(nil), m.typingCalls...),
		append([]agentTurnUXCall(nil), m.cleanupCalls...)
}

type agentTurnUXGatedProvider struct {
	started  chan struct{}
	release  chan struct{}
	startOne sync.Once
	calls    atomic.Int32
	response string
	panicOn  bool
}

func newAgentTurnUXGatedProvider(response string) *agentTurnUXGatedProvider {
	return &agentTurnUXGatedProvider{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		response: response,
	}
}

func (p *agentTurnUXGatedProvider) Chat(
	ctx context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	p.calls.Add(1)
	p.startOne.Do(func() {
		close(p.started)
	})
	select {
	case <-p.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if p.panicOn {
		panic("agent turn UX provider panic")
	}
	return &providers.LLMResponse{Content: p.response}, nil
}

func (p *agentTurnUXGatedProvider) GetDefaultModel() string {
	return "agent-turn-ux-test"
}

type agentTurnUXPanicThenContinueProvider struct {
	firstStarted  chan struct{}
	panicFirst    chan struct{}
	secondStarted chan struct{}
	releaseSecond chan struct{}
	calls         atomic.Int32

	mu             sync.Mutex
	secondMessages []providers.Message
}

func newAgentTurnUXPanicThenContinueProvider() *agentTurnUXPanicThenContinueProvider {
	return &agentTurnUXPanicThenContinueProvider{
		firstStarted:  make(chan struct{}),
		panicFirst:    make(chan struct{}),
		secondStarted: make(chan struct{}),
		releaseSecond: make(chan struct{}),
	}
}

func (p *agentTurnUXPanicThenContinueProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	switch p.calls.Add(1) {
	case 1:
		close(p.firstStarted)
		select {
		case <-p.panicFirst:
			panic("initial owner panicked after steering commit")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	case 2:
		p.mu.Lock()
		p.secondMessages = append([]providers.Message(nil), messages...)
		p.mu.Unlock()
		close(p.secondStarted)
		select {
		case <-p.releaseSecond:
			return &providers.LLMResponse{Content: "rescued continuation"}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	default:
		return &providers.LLMResponse{Content: "unexpected extra call"}, nil
	}
}

func (p *agentTurnUXPanicThenContinueProvider) GetDefaultModel() string {
	return "agent-turn-ux-panic-continue-test"
}

func (p *agentTurnUXPanicThenContinueProvider) capturedSecondMessages() []providers.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]providers.Message(nil), p.secondMessages...)
}

func startAgentTurnUXLoop(
	t *testing.T,
	provider providers.LLMProvider,
	manager *agentTurnUXChannelManager,
) (*AgentLoop, *bus.MessageBus, context.CancelFunc) {
	t.Helper()

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.MaxParallelTurns = 1
	return startAgentTurnUXLoopWithConfig(t, cfg, provider, manager)
}

func startAgentTurnUXLoopWithConfig(
	t *testing.T,
	cfg *config.Config,
	provider providers.LLMProvider,
	manager *agentTurnUXChannelManager,
) (*AgentLoop, *bus.MessageBus, context.CancelFunc) {
	t.Helper()

	msgBus := bus.NewMessageBus()
	al := NewAgentLoop(cfg, msgBus, provider)
	al.channelManager = manager

	runCtx, cancelRun := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		runDone <- al.Run(runCtx)
	}()

	t.Cleanup(func() {
		cancelRun()
		al.Stop()
		select {
		case err := <-runDone:
			if err != nil {
				t.Errorf("AgentLoop.Run() error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Errorf("AgentLoop.Run() did not stop")
		}
		waitForAgentTurnUXRuntimeIdle(t, al)
		msgBus.Close()
		al.Close()
	})

	return al, msgBus, cancelRun
}

func waitForAgentTurnUXRuntimeIdle(t *testing.T, al *AgentLoop) {
	t.Helper()

	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		al.runtimeGateMu.Lock()
		active := al.runtimeGateActive
		al.ensureRuntimeGateChangedLocked()
		changed := al.runtimeGateChanged
		al.runtimeGateMu.Unlock()
		if active == 0 {
			return
		}
		select {
		case <-changed:
		case <-timer.C:
			t.Fatalf("runtime users did not drain; active = %d", active)
		}
	}
}

func waitForAgentTurnUXState(
	t *testing.T,
	al *AgentLoop,
	sessionKey string,
	wantPresent bool,
) {
	t.Helper()

	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		present := al.getActiveTurnState(sessionKey) != nil
		if present == wantPresent {
			return
		}
		select {
		case <-timer.C:
			t.Fatalf(
				"active turn presence for %q = %v, want %v",
				sessionKey,
				present,
				wantPresent,
			)
		default:
			runtime.Gosched()
		}
	}
}

func waitForAgentTurnUXWorkerRelease(t *testing.T, al *AgentLoop) {
	t.Helper()

	select {
	case al.workerSem <- struct{}{}:
		<-al.workerSem
	case <-time.After(2 * time.Second):
		t.Fatal("agent worker did not release its semaphore slot")
	}
}

func assertAgentTurnUXCall(
	t *testing.T,
	got agentTurnUXCall,
	channel, chatID, turnUXID string,
) {
	t.Helper()
	want := agentTurnUXCall{
		channel:  channel,
		chatID:   chatID,
		turnUXID: turnUXID,
	}
	if got != want {
		t.Fatalf("turn UX call = %#v, want %#v", got, want)
	}
}

func TestAgentTurnUXSynchronousRouteFailureTransfersOrCleansExactTurn(
	t *testing.T,
) {
	for _, testCase := range []struct {
		name         string
		failOutbound bool
	}{
		{name: "buffered error response"},
		{name: "failed error response", failOutbound: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			manager := newAgentTurnUXChannelManager()
			innerBus := bus.NewMessageBus()
			messageBus := &agentTurnUXMessageBus{inner: innerBus}
			messageBus.failOutbound.Store(testCase.failOutbound)

			cfg := config.DefaultConfig()
			cfg.Agents.Defaults.Workspace = t.TempDir()
			al := NewAgentLoop(
				cfg,
				innerBus,
				newAgentTurnUXGatedProvider("must not run"),
			)
			al.bus = messageBus
			al.channelManager = manager

			// Force resolveSteeringTarget and processMessage down the same
			// non-routable path that an invalid live route would take.
			al.registry.mu.Lock()
			agents := al.registry.agents
			al.registry.agents = make(map[string]*AgentInstance)
			al.registry.mu.Unlock()

			const (
				channel  = "test"
				chatID   = "route-failure-chat"
				turnUXID = "turn-ux-route-failure"
			)
			message := bus.InboundMessage{
				Context: bus.InboundContext{
					Channel:  channel,
					ChatID:   chatID,
					ChatType: "direct",
					SenderID: "user",
					TurnUXID: turnUXID,
				},
				Content: "cannot be routed",
			}
			al.processMessageSync(context.Background(), message)

			al.registry.mu.Lock()
			al.registry.agents = agents
			al.registry.mu.Unlock()

			typing, cleanup := manager.snapshotCalls()
			if testCase.failOutbound {
				if len(typing) != 0 {
					t.Fatalf("typing-stop calls = %#v, want none", typing)
				}
				if len(cleanup) != 1 {
					t.Fatalf("cleanup calls = %#v, want one", cleanup)
				}
				assertAgentTurnUXCall(
					t,
					cleanup[0],
					channel,
					chatID,
					turnUXID,
				)
				select {
				case outbound := <-innerBus.OutboundChan():
					t.Fatalf("unexpected failed outbound: %#v", outbound)
				default:
				}
			} else {
				if len(cleanup) != 0 {
					t.Fatalf("cleanup calls = %#v, want none", cleanup)
				}
				if len(typing) != 1 {
					t.Fatalf("typing-stop calls = %#v, want one", typing)
				}
				assertAgentTurnUXCall(
					t,
					typing[0],
					channel,
					chatID,
					turnUXID,
				)
				select {
				case outbound := <-innerBus.OutboundChan():
					if outbound.Context.TurnUXID != turnUXID {
						t.Fatalf(
							"outbound TurnUXID = %q, want %q",
							outbound.Context.TurnUXID,
							turnUXID,
						)
					}
				default:
					t.Fatal("route failure did not buffer its error response")
				}
			}

			innerBus.Close()
			al.Close()
		})
	}
}

func TestAgentTurnUXSynchronousNoOutputCleansExactTurn(t *testing.T) {
	manager := newAgentTurnUXChannelManager()
	messageBus := bus.NewMessageBus()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	al := NewAgentLoop(
		cfg,
		messageBus,
		newAgentTurnUXGatedProvider("must not run"),
	)
	al.channelManager = manager

	const (
		channel  = "system"
		chatID   = "cli:direct"
		turnUXID = "turn-ux-no-output"
	)
	al.processMessageSync(context.Background(), bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  channel,
			ChatID:   chatID,
			SenderID: "subagent",
			TurnUXID: turnUXID,
		},
		Content: "internal completion",
	})

	typing, cleanup := manager.snapshotCalls()
	if len(typing) != 0 {
		t.Fatalf("typing-stop calls = %#v, want none", typing)
	}
	if len(cleanup) != 1 {
		t.Fatalf("cleanup calls = %#v, want one", cleanup)
	}
	assertAgentTurnUXCall(t, cleanup[0], channel, chatID, turnUXID)
	select {
	case outbound := <-messageBus.OutboundChan():
		t.Fatalf("unexpected no-output outbound: %#v", outbound)
	default:
	}

	messageBus.Close()
	al.Close()
}

func TestAgentTurnUXConsumedWorkflowCleansExactTurn(t *testing.T) {
	manager := newAgentTurnUXChannelManager()
	provider := newAgentTurnUXGatedProvider("must not run")
	cfg := config.DefaultConfig()
	workspace := t.TempDir()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Agents.Defaults.MaxParallelTurns = 1
	cfg.Workflows.Enabled = true

	workflowDir := filepath.Join(workspace, workflows.DefaultDefinitionsDir)
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workflowDir, "consume.yml"), []byte(`
name: Consume turn UX
on:
  channel_message:
    channels: test
    passthrough: false
jobs:
  consume:
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

	al, msgBus, _ := startAgentTurnUXLoopWithConfig(t, cfg, provider, manager)
	blockingTool := &runtimeGateBlockingWorkflowTool{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	al.GetRegistry().GetDefaultAgent().Tools.Register(blockingTool)

	const (
		channel  = "test"
		chatID   = "workflow-consumed-chat"
		turnUXID = "turn-ux-workflow-consumed"
	)
	msg := bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  channel,
			ChatID:   chatID,
			ChatType: "direct",
			SenderID: "user",
			TurnUXID: turnUXID,
		},
		Content: "run the consuming workflow",
	}
	if err := msgBus.PublishInbound(context.Background(), msg); err != nil {
		t.Fatalf("PublishInbound() error = %v", err)
	}

	select {
	case call := <-manager.cleanupCalled:
		assertAgentTurnUXCall(t, call, channel, chatID, turnUXID)
	case <-time.After(2 * time.Second):
		t.Fatal("consumed workflow did not clean transient UX")
	}
	select {
	case <-blockingTool.started:
	case <-time.After(2 * time.Second):
		t.Fatal("consuming workflow did not start")
	}
	close(blockingTool.release)
	waitForAgentTurnUXRuntimeIdle(t, al)

	typing, cleanup := manager.snapshotCalls()
	if len(typing) != 0 {
		t.Fatalf("typing-stop calls = %#v, want none", typing)
	}
	if len(cleanup) != 1 {
		t.Fatalf("cleanup calls = %#v, want exactly one", cleanup)
	}
	if calls := provider.calls.Load(); calls != 0 {
		t.Fatalf("provider calls = %d, want consumed message not to start an agent turn", calls)
	}
}

func TestAgentTurnUXRuntimeAcquireFailureCleansExactTurn(t *testing.T) {
	manager := newAgentTurnUXChannelManager()
	provider := newAgentTurnUXGatedProvider("must not run")
	al, msgBus, _ := startAgentTurnUXLoop(t, provider, manager)

	al.runtimeGateMu.Lock()
	al.runtimeGateStopped = true
	al.signalRuntimeGateChangedLocked()
	al.runtimeGateMu.Unlock()

	const (
		channel  = "test"
		chatID   = "runtime-acquire-failure-chat"
		turnUXID = "turn-ux-runtime-acquire-failure"
	)
	msg := bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  channel,
			ChatID:   chatID,
			ChatType: "direct",
			SenderID: "user",
			TurnUXID: turnUXID,
		},
		Content: "fail runtime admission",
	}
	if err := msgBus.PublishInbound(context.Background(), msg); err != nil {
		t.Fatalf("PublishInbound() error = %v", err)
	}

	select {
	case call := <-manager.cleanupCalled:
		assertAgentTurnUXCall(t, call, channel, chatID, turnUXID)
	case <-time.After(2 * time.Second):
		t.Fatal("runtime acquire failure did not clean transient UX")
	}
	if calls := provider.calls.Load(); calls != 0 {
		t.Fatalf("provider calls = %d, want 0", calls)
	}
	typing, cleanup := manager.snapshotCalls()
	if len(typing) != 0 {
		t.Fatalf("typing-stop calls = %#v, want none", typing)
	}
	if len(cleanup) != 1 {
		t.Fatalf("cleanup calls = %#v, want exactly one", cleanup)
	}
}

func TestAgentTurnUXRuntimeRetainFailurePreservesNewerReservation(t *testing.T) {
	manager := newAgentTurnUXChannelManager()
	msgBus := bus.NewMessageBus()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	al := NewAgentLoop(
		cfg,
		msgBus,
		newAgentTurnUXGatedProvider("must not run"),
	)
	al.channelManager = manager
	t.Cleanup(func() {
		msgBus.Close()
		al.Close()
	})

	const (
		channel       = "test"
		chatID        = "runtime-retain-failure-chat"
		sessionKey    = "agent:main:test:runtime-retain-failure-chat"
		turnUXID      = "turn-ux-runtime-retain-failure"
		newerTurnUXID = "turn-ux-newer-reservation"
	)
	older := &turnState{
		turnID:   makePendingTurnID(sessionKey, 1),
		turnUXID: turnUXID,
		phase:    TurnPhaseSetup,
	}
	newer := &turnState{
		turnID:   makePendingTurnID(sessionKey, 2),
		turnUXID: newerTurnUXID,
		phase:    TurnPhaseSetup,
	}
	al.activeTurnStates.Store(sessionKey, newer)
	t.Cleanup(func() {
		al.activeTurnStates.CompareAndDelete(sessionKey, newer)
	})

	al.runtimeGateMu.Lock()
	al.runtimeGateStopped = true
	al.signalRuntimeGateChangedLocked()
	al.runtimeGateMu.Unlock()

	msg := bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  channel,
			ChatID:   chatID,
			ChatType: "direct",
			SenderID: "user",
			TurnUXID: turnUXID,
		},
		Content: "fail runtime retention",
	}
	_, release, err := al.retainInboundWorkerRuntime(
		context.Background(),
		sessionKey,
		older,
		msg,
	)
	release()
	if !errors.Is(err, errAgentRuntimeStopped) {
		t.Fatalf(
			"retainInboundWorkerRuntime() error = %v, want %v",
			err,
			errAgentRuntimeStopped,
		)
	}

	select {
	case call := <-manager.cleanupCalled:
		assertAgentTurnUXCall(t, call, channel, chatID, turnUXID)
	case <-time.After(2 * time.Second):
		t.Fatal("runtime retain failure did not clean transient UX")
	}
	if got := al.getActiveTurnState(sessionKey); got != newer {
		t.Fatalf("active turn after older retain failure = %p, want newer %p", got, newer)
	}
	typing, cleanup := manager.snapshotCalls()
	if len(typing) != 0 {
		t.Fatalf("typing-stop calls = %#v, want none", typing)
	}
	if len(cleanup) != 1 || cleanup[0].turnUXID == newerTurnUXID {
		t.Fatalf("cleanup calls = %#v, want only older turn", cleanup)
	}
}

func TestAgentTurnUXStopPublishFailureCleansExactTurn(t *testing.T) {
	manager := newAgentTurnUXChannelManager()
	msgBus := bus.NewMessageBus()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	al := NewAgentLoop(
		cfg,
		msgBus,
		newAgentTurnUXGatedProvider("must not run"),
	)
	al.channelManager = manager
	msgBus.Close()
	t.Cleanup(al.Close)

	const (
		channel    = "test"
		chatID     = "stop-publish-failure-chat"
		sessionKey = "agent:main:test:stop-publish-failure-chat"
		turnUXID   = "turn-ux-stop-publish-failure"
	)
	msg := bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  channel,
			ChatID:   chatID,
			ChatType: "direct",
			SenderID: "user",
			TurnUXID: turnUXID,
		},
		Content: "/stop",
	}
	msg = bus.NormalizeInboundMessage(msg)
	if !al.tryHandleStopCommand(context.Background(), msg, sessionKey) {
		t.Fatal("tryHandleStopCommand() handled = false, want true")
	}

	select {
	case call := <-manager.typingCalled:
		assertAgentTurnUXCall(t, call, channel, chatID, turnUXID)
	case <-time.After(2 * time.Second):
		t.Fatal("/stop did not stop exact typing UX")
	}
	select {
	case call := <-manager.cleanupCalled:
		assertAgentTurnUXCall(t, call, channel, chatID, turnUXID)
	case <-time.After(2 * time.Second):
		t.Fatal("/stop publish failure did not clean transient UX")
	}
	typing, cleanup := manager.snapshotCalls()
	if len(typing) != 1 || len(cleanup) != 1 {
		t.Fatalf(
			"turn UX calls = typing:%#v cleanup:%#v, want one exact call each",
			typing,
			cleanup,
		)
	}
}

func TestAgentTurnUXCanceledWhileWaitingForWorkerCleansExactTurn(t *testing.T) {
	manager := newAgentTurnUXChannelManager()
	provider := newAgentTurnUXGatedProvider("must not run")
	al, msgBus, cancelRun := startAgentTurnUXLoop(t, provider, manager)

	al.workerSem <- struct{}{}
	workerSlotHeld := true
	t.Cleanup(func() {
		if workerSlotHeld {
			<-al.workerSem
		}
	})

	const (
		channel  = "test"
		chatID   = "worker-wait-chat"
		turnUXID = "turn-ux-worker-wait"
	)
	msg := bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  channel,
			ChatID:   chatID,
			ChatType: "direct",
			SenderID: "user",
			TurnUXID: turnUXID,
		},
		Content: "cancel before a worker slot is available",
	}
	sessionKey, _, ok := al.resolveSteeringTarget(msg)
	if !ok {
		t.Fatal("message did not resolve to an agent session")
	}
	if err := msgBus.PublishInbound(context.Background(), msg); err != nil {
		t.Fatalf("PublishInbound() error = %v", err)
	}
	waitForAgentTurnUXState(t, al, sessionKey, true)

	cancelRun()

	select {
	case call := <-manager.cleanupCalled:
		assertAgentTurnUXCall(t, call, channel, chatID, turnUXID)
	case <-time.After(2 * time.Second):
		t.Fatal("canceled worker wait did not clean transient UX")
	}
	waitForAgentTurnUXState(t, al, sessionKey, false)
	waitForAgentTurnUXRuntimeIdle(t, al)

	if calls := provider.calls.Load(); calls != 0 {
		t.Fatalf("provider calls = %d, want 0", calls)
	}
	typing, cleanup := manager.snapshotCalls()
	if len(typing) != 0 {
		t.Fatalf("typing-stop calls = %#v, want none", typing)
	}
	if len(cleanup) != 1 {
		t.Fatalf("cleanup calls = %#v, want exactly one", cleanup)
	}

	<-al.workerSem
	workerSlotHeld = false
}

func TestAgentTurnUXBufferedOutboundStopsTypingWithoutFullCleanup(t *testing.T) {
	manager := newAgentTurnUXChannelManager()
	provider := newAgentTurnUXGatedProvider("buffered response")
	al, msgBus, _ := startAgentTurnUXLoop(t, provider, manager)

	const (
		channel  = "test"
		chatID   = "buffered-outbound-chat"
		turnUXID = "turn-ux-buffered-outbound"
	)
	msg := bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  channel,
			ChatID:   chatID,
			ChatType: "direct",
			SenderID: "user",
			TurnUXID: turnUXID,
		},
		Content: "produce a buffered response",
	}
	if err := msgBus.PublishInbound(context.Background(), msg); err != nil {
		t.Fatalf("PublishInbound() error = %v", err)
	}
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not start")
	}
	close(provider.release)

	select {
	case call := <-manager.typingCalled:
		assertAgentTurnUXCall(t, call, channel, chatID, turnUXID)
	case <-time.After(2 * time.Second):
		t.Fatal("successful buffered outbound did not stop exact typing UX")
	}
	waitForAgentTurnUXWorkerRelease(t, al)

	typing, cleanup := manager.snapshotCalls()
	if len(typing) != 1 {
		t.Fatalf("typing-stop calls = %#v, want exactly one", typing)
	}
	if len(cleanup) != 0 {
		t.Fatalf(
			"cleanup calls before buffered outbound delivery = %#v, want none",
			cleanup,
		)
	}

	select {
	case outbound := <-msgBus.OutboundChan():
		if outbound.Content != "buffered response" {
			t.Fatalf("outbound content = %q, want buffered response", outbound.Content)
		}
		if outbound.Context.TurnUXID != turnUXID {
			t.Fatalf(
				"outbound TurnUXID = %q, want %q",
				outbound.Context.TurnUXID,
				turnUXID,
			)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("buffered outbound was not available for delivery")
	}
}

func TestAgentTurnUXProviderPanicCleansExactTurnWithoutOutbound(t *testing.T) {
	manager := newAgentTurnUXChannelManager()
	provider := newAgentTurnUXGatedProvider("")
	provider.panicOn = true
	al, msgBus, _ := startAgentTurnUXLoop(t, provider, manager)

	const (
		channel  = "test"
		chatID   = "provider-panic-chat"
		turnUXID = "turn-ux-provider-panic"
	)
	msg := bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  channel,
			ChatID:   chatID,
			ChatType: "direct",
			SenderID: "user",
			TurnUXID: turnUXID,
		},
		Content: "panic without outbound",
	}
	sessionKey, _, ok := al.resolveSteeringTarget(msg)
	if !ok {
		t.Fatal("message did not resolve to an agent session")
	}
	if err := msgBus.PublishInbound(context.Background(), msg); err != nil {
		t.Fatalf("PublishInbound() error = %v", err)
	}
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not start")
	}
	close(provider.release)

	select {
	case call := <-manager.cleanupCalled:
		assertAgentTurnUXCall(t, call, channel, chatID, turnUXID)
	case <-time.After(2 * time.Second):
		t.Fatal("provider panic did not clean transient UX")
	}
	waitForAgentTurnUXWorkerRelease(t, al)
	waitForAgentTurnUXState(t, al, sessionKey, false)

	typing, cleanup := manager.snapshotCalls()
	if len(typing) != 0 {
		t.Fatalf("typing-stop calls = %#v, want none", typing)
	}
	if len(cleanup) != 1 {
		t.Fatalf("cleanup calls = %#v, want exactly one", cleanup)
	}
	select {
	case outbound := <-msgBus.OutboundChan():
		t.Fatalf("unexpected outbound after provider panic: %#v", outbound)
	default:
	}
}

func TestAgentTurnUXProviderPanicRescuesCommittedSteering(t *testing.T) {
	manager := newAgentTurnUXChannelManager()
	provider := newAgentTurnUXPanicThenContinueProvider()
	al, msgBus, _ := startAgentTurnUXLoop(t, provider, manager)

	const (
		channel       = "test"
		chatID        = "panic-rescue-chat"
		firstTurnUXID = "turn-ux-panic-owner"
		queuedTurnUX  = "turn-ux-queued-steering"
		queuedContent = "rescue this committed steering message"
	)
	first := bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  channel,
			ChatID:   chatID,
			ChatType: "direct",
			SenderID: "user",
			TurnUXID: firstTurnUXID,
		},
		Content: "initial owner will panic",
	}
	sessionKey, _, ok := al.resolveSteeringTarget(first)
	if !ok {
		t.Fatal("initial message did not resolve to an agent session")
	}
	if err := msgBus.PublishInbound(context.Background(), first); err != nil {
		t.Fatalf("PublishInbound(first) error = %v", err)
	}
	select {
	case <-provider.firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("initial provider call did not start")
	}

	second := first
	second.Context.TurnUXID = queuedTurnUX
	second.Content = queuedContent
	if err := msgBus.PublishInbound(context.Background(), second); err != nil {
		t.Fatalf("PublishInbound(second) error = %v", err)
	}
	select {
	case rebind := <-manager.rebindCalled:
		want := agentTurnUXRebindCall{
			channel:      channel,
			chatID:       chatID,
			fromTurnUXID: queuedTurnUX,
			toTurnUXID:   firstTurnUXID,
		}
		if rebind != want {
			t.Fatalf("steering UX rebind = %#v, want %#v", rebind, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second inbound did not commit to the active steering owner")
	}
	if depth := al.pendingSteeringCountForScope(sessionKey); depth != 1 {
		t.Fatalf("steering depth before owner panic = %d, want 1", depth)
	}

	close(provider.panicFirst)

	select {
	case <-provider.secondStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("orphaned steering was not resumed by a fresh owner")
	}
	rescuedTurn := al.getActiveTurnState(sessionKey)
	rescuedTurnUXID := ""
	if rescuedTurn != nil {
		rescuedTurnUXID = rescuedTurn.turnUXID
	}
	if rescuedTurnUXID != firstTurnUXID {
		t.Fatalf(
			"rescued turn UX identity = %q, want original owner %q",
			rescuedTurnUXID,
			firstTurnUXID,
		)
	}
	if depth := al.pendingSteeringCountForScope(sessionKey); depth != 0 {
		t.Fatalf("steering depth during rescued continuation = %d, want 0", depth)
	}
	foundQueuedMessage := false
	for _, message := range provider.capturedSecondMessages() {
		if message.Role == "user" && message.Content == queuedContent {
			foundQueuedMessage = true
			break
		}
	}
	if !foundQueuedMessage {
		t.Fatalf(
			"rescued provider messages = %#v, missing committed steering content %q",
			provider.capturedSecondMessages(),
			queuedContent,
		)
	}
	typingBeforeRelease, cleanupBeforeRelease := manager.snapshotCalls()
	if len(typingBeforeRelease) != 0 || len(cleanupBeforeRelease) != 0 {
		t.Fatalf(
			"old owner touched transferred UX during rescue: typing=%#v cleanup=%#v",
			typingBeforeRelease,
			cleanupBeforeRelease,
		)
	}

	close(provider.releaseSecond)
	select {
	case outbound := <-msgBus.OutboundChan():
		if outbound.Content != "rescued continuation" {
			t.Fatalf(
				"rescued outbound content = %q, want %q",
				outbound.Content,
				"rescued continuation",
			)
		}
		if outbound.Context.TurnUXID != firstTurnUXID {
			t.Fatalf(
				"rescued outbound TurnUXID = %q, want %q",
				outbound.Context.TurnUXID,
				firstTurnUXID,
			)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("rescued continuation did not publish its response")
	}
	select {
	case call := <-manager.typingCalled:
		assertAgentTurnUXCall(t, call, channel, chatID, firstTurnUXID)
	case <-time.After(2 * time.Second):
		t.Fatal("rescued buffered response did not release typing ownership")
	}
	waitForAgentTurnUXState(t, al, sessionKey, false)
	waitForAgentTurnUXRuntimeIdle(t, al)

	if calls := provider.calls.Load(); calls != 2 {
		t.Fatalf("provider calls = %d, want initial owner plus one rescue", calls)
	}
	if depth := al.pendingSteeringCountForScope(sessionKey); depth != 0 {
		t.Fatalf("final steering depth = %d, want 0", depth)
	}
	typing, cleanup := manager.snapshotCalls()
	if len(typing) != 1 {
		t.Fatalf("typing-stop calls = %#v, want exactly one", typing)
	}
	if len(cleanup) != 0 {
		t.Fatalf("cleanup calls = %#v, buffered rescue must retain delivery ownership", cleanup)
	}
}

func TestAgentTurnUXRescuePublishFailureCleansTransferredTurn(t *testing.T) {
	manager := newAgentTurnUXChannelManager()
	provider := newAgentTurnUXGatedProvider("rescued response")
	close(provider.release)

	innerBus := bus.NewMessageBus()
	messageBus := &agentTurnUXMessageBus{inner: innerBus}
	messageBus.failOutbound.Store(true)
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	al := NewAgentLoop(cfg, innerBus, provider)
	al.bus = messageBus
	al.channelManager = manager
	al.running.Store(true)

	const (
		channel  = "test"
		chatID   = "rescue-publish-failure-chat"
		turnUXID = "turn-ux-rescue-publish-failure"
	)
	inboundContext := &bus.InboundContext{
		Channel:  channel,
		ChatID:   chatID,
		ChatType: "direct",
		SenderID: "user",
		TurnUXID: turnUXID,
	}
	sessionKey, _, ok := al.resolveSteeringTarget(bus.InboundMessage{
		Context: *inboundContext,
		Content: "resolve rescue session",
	})
	if !ok {
		t.Fatal("rescue message did not resolve to an agent session")
	}
	if _, _, err := al.pushSteeringMessage(
		sessionKey,
		providers.Message{Role: "user", Content: "committed rescue"},
	); err != nil {
		t.Fatalf("pushSteeringMessage() error = %v", err)
	}
	if transferred := al.rescueOrClearOrphanedSteering(
		context.Background(),
		sessionKey,
		channel,
		chatID,
		inboundContext,
	); !transferred {
		t.Fatal("rescue did not accept committed steering ownership")
	}

	select {
	case call := <-manager.cleanupCalled:
		assertAgentTurnUXCall(t, call, channel, chatID, turnUXID)
	case <-time.After(2 * time.Second):
		t.Fatal("failed rescue publish did not clean transferred UX")
	}
	waitForAgentTurnUXState(t, al, sessionKey, false)
	waitForAgentTurnUXRuntimeIdle(t, al)
	if depth := al.pendingSteeringCountForScope(sessionKey); depth != 0 {
		t.Fatalf("steering depth after failed rescue publish = %d, want 0", depth)
	}
	typing, cleanup := manager.snapshotCalls()
	if len(typing) != 0 {
		t.Fatalf("typing-stop calls = %#v, want none", typing)
	}
	if len(cleanup) != 1 {
		t.Fatalf("cleanup calls = %#v, want exactly one", cleanup)
	}
	select {
	case outbound := <-innerBus.OutboundChan():
		t.Fatalf("unexpected failed rescue outbound: %#v", outbound)
	default:
	}

	al.running.Store(false)
	innerBus.Close()
	al.Close()
}

func TestAgentTurnUXRescueMarkerHandsLateAbandonmentToSuccessor(t *testing.T) {
	manager := newAgentTurnUXChannelManager()
	provider := newAgentTurnUXGatedProvider("rescued response")
	close(provider.release)

	innerBus := bus.NewMessageBus()
	messageBus := &agentTurnUXMessageBus{
		inner:           innerBus,
		outboundStarted: make(chan struct{}),
		outboundRelease: make(chan struct{}),
	}
	messageBus.blockOutbound.Store(true)
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	al := NewAgentLoop(cfg, innerBus, provider)
	al.bus = messageBus
	al.channelManager = manager
	al.running.Store(true)
	t.Cleanup(func() {
		al.running.Store(false)
		innerBus.Close()
		al.Close()
	})

	const (
		channel      = "test"
		chatID       = "successor-rescue-chat"
		firstTurnUX  = "turn-ux-first-rescue"
		secondTurnUX = "turn-ux-second-rescue"
	)
	firstContext := &bus.InboundContext{
		Channel:  channel,
		ChatID:   chatID,
		ChatType: "direct",
		SenderID: "user",
		TurnUXID: firstTurnUX,
	}
	sessionKey, _, ok := al.resolveSteeringTarget(bus.InboundMessage{
		Context: *firstContext,
		Content: "resolve successor rescue session",
	})
	if !ok {
		t.Fatal("rescue message did not resolve to an agent session")
	}

	if _, _, err := al.pushSteeringMessage(
		sessionKey,
		providers.Message{Role: "user", Content: "first committed rescue"},
	); err != nil {
		t.Fatalf("push first steering message: %v", err)
	}
	if transferred := al.rescueOrClearOrphanedSteering(
		context.Background(),
		sessionKey,
		channel,
		chatID,
		firstContext,
	); !transferred {
		t.Fatal("first rescue did not accept committed steering ownership")
	}

	select {
	case <-messageBus.outboundStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first rescue did not reach its final outbound handoff")
	}
	if al.getActiveTurnState(sessionKey) != nil {
		t.Fatal("first rescue still owned the session while outbound was blocked")
	}

	secondContext := cloneInboundContext(firstContext)
	secondContext.TurnUXID = secondTurnUX
	if _, _, err := al.pushSteeringMessage(
		sessionKey,
		providers.Message{Role: "user", Content: "late committed rescue"},
	); err != nil {
		t.Fatalf("push late steering message: %v", err)
	}
	if transferred := al.rescueOrClearOrphanedSteering(
		context.Background(),
		sessionKey,
		channel,
		chatID,
		secondContext,
	); !transferred {
		t.Fatal("late abandonment did not transfer to the existing supervisor")
	}

	close(messageBus.outboundRelease)

	for index, turnUXID := range []string{firstTurnUX, secondTurnUX} {
		select {
		case outbound := <-innerBus.OutboundChan():
			if outbound.Content != "rescued response" {
				t.Fatalf(
					"outbound %d content = %q, want rescued response",
					index,
					outbound.Content,
				)
			}
			if outbound.Context.TurnUXID != turnUXID {
				t.Fatalf(
					"outbound %d TurnUXID = %q, want %q",
					index,
					outbound.Context.TurnUXID,
					turnUXID,
				)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("rescued outbound %d was not published", index)
		}
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		_, rescueActive := al.steeringRescues.Load(sessionKey)
		if !rescueActive &&
			al.getActiveTurnState(sessionKey) == nil &&
			al.pendingSteeringCountForScope(sessionKey) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("successor rescue did not retire its ownership state")
		}
		runtime.Gosched()
	}

	if calls := provider.calls.Load(); calls != 2 {
		t.Fatalf("provider calls = %d, want one call per rescue owner", calls)
	}
	typing, cleanup := manager.snapshotCalls()
	if len(typing) != 2 {
		t.Fatalf("typing-stop calls = %#v, want both exact rescue owners", typing)
	}
	if typing[0].turnUXID != firstTurnUX ||
		typing[1].turnUXID != secondTurnUX {
		t.Fatalf(
			"typing-stop ownership = %#v, want %q then %q",
			typing,
			firstTurnUX,
			secondTurnUX,
		)
	}
	if len(cleanup) != 0 {
		t.Fatalf(
			"full cleanup calls = %#v, buffered successor responses own delivery",
			cleanup,
		)
	}
}

func TestAgentTurnUXRescueTreatsCompetingOwnerAsQueueTransfer(t *testing.T) {
	manager := newAgentTurnUXChannelManager()
	provider := newAgentTurnUXGatedProvider("must not run")
	close(provider.release)

	innerBus := bus.NewMessageBus()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	al := NewAgentLoop(cfg, innerBus, provider)
	al.channelManager = manager
	al.running.Store(true)
	t.Cleanup(func() {
		al.running.Store(false)
		innerBus.Close()
		al.Close()
	})

	const (
		channel  = "test"
		chatID   = "competing-rescue-owner-chat"
		turnUXID = "turn-ux-competing-rescue-owner"
	)
	inboundContext := &bus.InboundContext{
		Channel:  channel,
		ChatID:   chatID,
		ChatType: "direct",
		SenderID: "user",
		TurnUXID: turnUXID,
	}
	sessionKey, _, ok := al.resolveSteeringTarget(bus.InboundMessage{
		Context: *inboundContext,
		Content: "resolve competing rescue session",
	})
	if !ok {
		t.Fatal("rescue message did not resolve to an agent session")
	}

	competingOwner := &turnState{
		turnID:     "competing-live-owner",
		sessionKey: sessionKey,
		channel:    channel,
		chatID:     chatID,
		turnUXID:   "turn-ux-competing-live-owner",
		phase:      TurnPhaseRunning,
	}
	if _, _, err := al.pushSteeringMessage(
		sessionKey,
		providers.Message{
			Role:    "user",
			Content: "owned by the competing live turn",
		},
	); err != nil {
		t.Fatalf("push rescue steering message: %v", err)
	}

	// Deterministically start the supervisor after a competing claimant has
	// taken the idle session. continueWithInboundContext returns
	// errSessionTurnAlreadyOwned; the supervisor must treat that sentinel as a
	// queue handoff, not format and publish it as a processing error.
	state := &steeringRescueState{}
	request := steeringRescueRequest{
		parentContext:  context.Background(),
		channel:        channel,
		chatID:         chatID,
		inboundContext: cloneInboundContext(inboundContext),
	}
	unlock := al.lockSessionTurn(sessionKey)
	al.activeTurnStates.Store(sessionKey, competingOwner)
	al.steeringRescues.Store(sessionKey, state)
	unlock()
	go al.runSteeringRescue(sessionKey, state, request, false)

	deadline := time.Now().Add(2 * time.Second)
	for {
		_, rescueActive := al.steeringRescues.Load(sessionKey)
		if !rescueActive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("rescue marker remained after queue ownership transfer")
		}
		runtime.Gosched()
	}
	select {
	case outbound := <-innerBus.OutboundChan():
		t.Fatalf(
			"ownership transfer was published as a user-facing error: %#v",
			outbound,
		)
	default:
	}
	if actual, loaded := al.activeTurnStates.Load(sessionKey); !loaded ||
		actual != competingOwner {
		t.Fatal("rescue disturbed the competing live owner")
	}
	if depth := al.pendingSteeringCountForScope(sessionKey); depth != 1 {
		t.Fatalf(
			"competing owner's steering depth = %d, want 1",
			depth,
		)
	}
	if calls := provider.calls.Load(); calls != 0 {
		t.Fatalf("provider calls = %d, want competing owner to retain queue", calls)
	}
	typing, cleanup := manager.snapshotCalls()
	if len(typing) != 0 {
		t.Fatalf("typing-stop calls = %#v, want no buffered rescue output", typing)
	}
	if len(cleanup) != 1 || cleanup[0].turnUXID != turnUXID {
		t.Fatalf(
			"cleanup calls = %#v, want exact abandoned rescue owner",
			cleanup,
		)
	}

	unlock = al.lockSessionTurn(sessionKey)
	al.activeTurnStates.CompareAndDelete(sessionKey, competingOwner)
	al.steering.clearScope(sessionKey)
	unlock()
}

func TestAgentTurnUXCrossChatSteeringCleansSecondaryChat(t *testing.T) {
	manager := newAgentTurnUXChannelManager()
	provider := newAgentTurnUXGatedProvider("global-session response")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.MaxParallelTurns = 1
	cfg.Session.Dimensions = nil
	cfg.Session.DmScope = "global"
	al, msgBus, _ := startAgentTurnUXLoopWithConfig(t, cfg, provider, manager)

	first := bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  "test",
			ChatID:   "chat-a",
			ChatType: "direct",
			SenderID: "same-user",
			TurnUXID: "turn-ux-chat-a",
		},
		Content: "start in chat A",
	}
	second := bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  "test",
			ChatID:   "chat-b",
			ChatType: "direct",
			SenderID: "same-user",
			TurnUXID: "turn-ux-chat-b",
		},
		Content: "steer from chat B",
	}
	firstSession, _, firstOK := al.resolveSteeringTarget(first)
	secondSession, _, secondOK := al.resolveSteeringTarget(second)
	if !firstOK || !secondOK || firstSession != secondSession {
		t.Fatalf(
			"global session resolution = first:(%q,%v) second:(%q,%v), want one shared scope",
			firstSession,
			firstOK,
			secondSession,
			secondOK,
		)
	}

	if err := msgBus.PublishInbound(context.Background(), first); err != nil {
		t.Fatalf("PublishInbound(first) error = %v", err)
	}
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first chat did not start the active turn")
	}
	if err := msgBus.PublishInbound(context.Background(), second); err != nil {
		t.Fatalf("PublishInbound(second) error = %v", err)
	}

	select {
	case call := <-manager.cleanupCalled:
		assertAgentTurnUXCall(t, call, "test", "chat-b", "turn-ux-chat-b")
	case <-time.After(2 * time.Second):
		t.Fatal("cross-chat steering did not clean its secondary-chat UX")
	}
	manager.mu.Lock()
	rebindCalls := append([]agentTurnUXRebindCall(nil), manager.rebindCalls...)
	manager.mu.Unlock()
	if len(rebindCalls) != 0 {
		t.Fatalf("cross-chat steering rebind calls = %#v, want none", rebindCalls)
	}
	if depth := al.pendingSteeringCountForScope(firstSession); depth != 1 {
		t.Fatalf("cross-chat steering queue depth = %d, want 1", depth)
	}

	close(provider.release)
	waitForAgentTurnUXState(t, al, firstSession, false)
	waitForAgentTurnUXRuntimeIdle(t, al)
}
