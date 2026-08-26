package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/tools"
)

const (
	p007RunInitialToolResponse = "initial response sent by the message tool"
	p007RunFallbackResponse    = "initial fallback that must remain suppressed"
	p007RunResultResponse      = "late tracked result after initial output"
	p007SteeringContent        = "steering committed during tracked continuation"
)

// p007BlockingDismissChannelManager makes the normal Run worker pause after it
// has observed the message-tool sent marker but before its outer tracked-result
// output owner is released. Message-tool output is forwarded to the real bus so
// the test observes one ordered outbound stream.
type p007BlockingDismissChannelManager struct {
	messageBus     *bus.MessageBus
	dismissStarted chan struct{}
	dismissRelease chan struct{}
	dismissOnce    sync.Once
}

func (*p007BlockingDismissChannelManager) GetChannel(string) (channels.Channel, bool) {
	return nil, false
}

func (*p007BlockingDismissChannelManager) GetEnabledChannels() []string { return nil }

func (*p007BlockingDismissChannelManager) InvokeTypingStop(string, string) {}

func (manager *p007BlockingDismissChannelManager) SendMessage(
	ctx context.Context,
	message bus.OutboundMessage,
) error {
	return manager.messageBus.PublishOutbound(ctx, message)
}

func (*p007BlockingDismissChannelManager) SendMedia(
	context.Context,
	bus.OutboundMediaMessage,
) error {
	return nil
}

func (*p007BlockingDismissChannelManager) SendPlaceholder(
	context.Context,
	string,
	string,
) bool {
	return false
}

func (manager *p007BlockingDismissChannelManager) DismissToolFeedback(
	ctx context.Context,
	_, _ string,
	_ *bus.InboundContext,
) {
	manager.dismissOnce.Do(func() { close(manager.dismissStarted) })
	select {
	case <-manager.dismissRelease:
	case <-ctx.Done():
	}
}

// p007MessageMarkerTool delegates to the real message tool from inside the
// provider's deterministic barrier call. This sets the same per-session marker
// production uses to suppress a duplicate final response.
type p007MessageMarkerTool struct {
	messageTool *tools.MessageTool
}

type p007PanickingMessageResetTool struct{}

func (*p007PanickingMessageResetTool) Name() string        { return "message" }
func (*p007PanickingMessageResetTool) Description() string { return "panic reset fixture" }

func (*p007PanickingMessageResetTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
	}
}

func (*p007PanickingMessageResetTool) Execute(
	context.Context,
	map[string]any,
) *tools.ToolResult {
	return tools.SilentResult("unused")
}
func (*p007PanickingMessageResetTool) ResetSentInRound(string) { panic("reset panic") }

func (*p007MessageMarkerTool) Name() string { return "tracked_result_barrier" }

func (*p007MessageMarkerTool) Description() string {
	return "Test-only message-tool marker for the tracked result output barrier"
}

func (*p007MessageMarkerTool) Parameters() map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"additionalProperties": false,
	}
}

func (tool *p007MessageMarkerTool) Execute(
	ctx context.Context,
	_ map[string]any,
) *tools.ToolResult {
	if tool == nil || tool.messageTool == nil {
		return tools.ErrorResult("message tool is unavailable")
	}
	return tool.messageTool.Execute(ctx, map[string]any{
		"content": p007RunInitialToolResponse,
	})
}

func TestTrackedSubagentResultRunOutputBarrierOrdersInitialBeforeLateResult(
	t *testing.T,
) {
	childGate, releaseChild := trackedSpawnPipelineGate()
	provider := &trackedSpawnPipelineProvider{
		includeBarrier:  true,
		childGate:       childGate,
		childStarted:    make(chan struct{}),
		initialResponse: p007RunFallbackResponse,
		resultResponse:  p007RunResultResponse,
	}
	t.Cleanup(releaseChild)
	loop, messageBus, parent := newTrackedSpawnPipelineLoop(t, provider)

	messageRaw, ok := parent.Tools.Get("message")
	if !ok {
		t.Fatal("real parent message tool is unavailable")
	}
	messageTool, ok := messageRaw.(*tools.MessageTool)
	if !ok {
		t.Fatalf("parent message tool type = %T, want *tools.MessageTool", messageRaw)
	}
	loop.RegisterTool(&p007MessageMarkerTool{messageTool: messageTool})

	dismissRelease := make(chan struct{})
	manager := &p007BlockingDismissChannelManager{
		messageBus:     messageBus,
		dismissStarted: make(chan struct{}),
		dismissRelease: dismissRelease,
	}
	loop.channelManager = manager

	runCtx, cancelRun := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- loop.Run(runCtx) }()
	t.Cleanup(func() {
		cancelRun()
		select {
		case err := <-runDone:
			if err != nil {
				t.Errorf("AgentLoop.Run() cleanup error = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("AgentLoop.Run() did not stop during cleanup")
		}
	})

	const sessionKey = "named-run-output-barrier-session"
	inbound := bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  trackedSpawnPipelineChannel,
			ChatID:   trackedSpawnPipelineChatID,
			ChatType: "direct",
			SenderID: "run-output-barrier-user",
		},
		Content:    "spawn a late child and send the initial response",
		SessionKey: sessionKey,
	}
	resolvedSessionKey, _, routable := loop.resolveSteeringTarget(inbound)
	if !routable || resolvedSessionKey == "" {
		t.Fatalf("normal Run inbound did not resolve a session: %q", resolvedSessionKey)
	}
	if err := messageBus.PublishInbound(context.Background(), inbound); err != nil {
		t.Fatalf("publish normal Run inbound: %v", err)
	}

	select {
	case <-provider.childStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("tracked child did not reach its late-result gate")
	}
	select {
	case <-manager.dismissStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("normal Run worker did not reach its initial-output barrier")
	}

	// The initial message-tool response is already accepted. Complete the child
	// while the normal Run worker still owns final-output ordering.
	releaseChild()
	record := waitForP007PendingTrackedResult(t, loop, trackedSpawnPipelineTaskID)
	if record.state != trackedSubagentResultPendingRoot || record.rootEligible {
		t.Fatalf(
			"late result while output barrier held = state %v eligible %v, want pending-root/ineligible",
			record.state,
			record.rootEligible,
		)
	}
	if !messageTool.HasSentTo(
		resolvedSessionKey,
		trackedSpawnPipelineChannel,
		trackedSpawnPipelineChatID,
	) {
		t.Fatal("late tracked continuation reset the message-tool sent marker before initial output completed")
	}
	resultPrompts, resultResponses, acknowledgements, calls := provider.snapshot()
	_ = resultResponses
	_ = acknowledgements
	_ = calls
	if resultPrompts != 0 {
		t.Fatalf("late result provider calls while output barrier held = %d, want 0", resultPrompts)
	}

	close(dismissRelease)
	outbounds := collectTrackedSpawnPipelineOutbounds(
		t,
		messageBus.OutboundChan(),
		p007RunResultResponse,
		5*time.Second,
	)
	assertTrackedSpawnPipelineDelivery(
		t,
		provider,
		messageBus,
		outbounds,
		resolvedSessionKey,
		p007RunResultResponse,
	)
	if len(outbounds) != 2 {
		t.Fatalf("normal Run outbound count = %d, want initial plus late result: %#v", len(outbounds), outbounds)
	}
	if outbounds[0].Content != p007RunInitialToolResponse ||
		outbounds[1].Content != p007RunResultResponse {
		t.Fatalf("normal Run outbound order = %#v", outbounds)
	}
	for _, outbound := range outbounds {
		if outbound.Content == p007RunFallbackResponse {
			t.Fatalf("message-tool marker was reset early; duplicate fallback escaped: %#v", outbounds)
		}
	}
}

func waitForP007PendingTrackedResult(
	t *testing.T,
	loop *AgentLoop,
	taskID string,
) trackedSubagentResultRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		loop.trackedSubagentResults.mu.Lock()
		var snapshot trackedSubagentResultRecord
		found := false
		for _, candidate := range loop.trackedSubagentResults.records {
			if candidate == nil || candidate.completion.TaskID != taskID {
				continue
			}
			snapshot = *candidate
			found = true
			break
		}
		loop.trackedSubagentResults.mu.Unlock()
		if found && (snapshot.state == trackedSubagentResultPendingPreferred ||
			snapshot.state == trackedSubagentResultPendingRoot) {
			return snapshot
		}
		if time.Now().After(deadline) {
			t.Fatalf("tracked result %q did not remain pending; last record = %#v", taskID, snapshot)
		}
		time.Sleep(time.Millisecond)
	}
}

type p007HandoffProviderCall struct {
	index    int
	messages []providers.Message
}

type p007HandoffProvider struct {
	mu sync.Mutex

	firstGate      <-chan struct{}
	firstErr       error
	resultResponse string
	steerResponse  string
	calls          [][]providers.Message
	called         chan p007HandoffProviderCall
}

func newP007HandoffProvider() *p007HandoffProvider {
	return &p007HandoffProvider{
		resultResponse: "tracked result response before steering",
		steerResponse:  "tracked steering response",
		called:         make(chan p007HandoffProviderCall, 8),
	}
}

func (provider *p007HandoffProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	snapshot := session.CloneMessages(messages)
	provider.mu.Lock()
	provider.calls = append(provider.calls, snapshot)
	index := len(provider.calls)
	gate := provider.firstGate
	firstErr := provider.firstErr
	provider.mu.Unlock()
	provider.called <- p007HandoffProviderCall{index: index, messages: snapshot}

	if index == 1 && gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if index == 1 && firstErr != nil {
		return nil, firstErr
	}
	response := provider.resultResponse
	if p007MessagesContain(snapshot, p007SteeringContent) {
		response = provider.steerResponse
	}
	return &providers.LLMResponse{Content: response, FinishReason: "stop"}, nil
}

func (provider *p007HandoffProvider) snapshotCalls() [][]providers.Message {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	result := make([][]providers.Message, len(provider.calls))
	for index, messages := range provider.calls {
		result[index] = session.CloneMessages(messages)
	}
	return result
}

type p007HandoffFixture struct {
	loop        *AgentLoop
	messageBus  *bus.MessageBus
	rootSession string
	route       trackedSubagentResultRoute
	recordID    trackedSubagentResultID
}

func newP007HandoffFixture(
	t *testing.T,
	provider providers.LLMProvider,
) *p007HandoffFixture {
	t.Helper()
	root := t.TempDir()
	mainWorkspace := filepath.Join(root, "main")
	rootWorkspace := filepath.Join(root, "alpha")
	for _, workspace := range []string{mainWorkspace, rootWorkspace} {
		if err := os.MkdirAll(workspace, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := trackedSubagentRuntimeConfig(mainWorkspace, rootWorkspace, true)
	messageBus := bus.NewMessageBus()
	loop := newTestAgentLoopWithStrictModels(cfg, messageBus, provider)
	t.Cleanup(func() {
		loop.Close()
		messageBus.Close()
	})
	rootAgent, ok := loop.GetRegistry().GetAgent("alpha")
	if !ok || rootAgent == nil {
		t.Fatal("alpha agent is unavailable")
	}
	rootScope := &session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    rootAgent.ID,
		Channel:    "test",
		Dimensions: []string{"chat"},
		Values:     map[string]string{"chat": "direct:p007-handoff"},
	}
	rootSession := session.BuildSessionKey(*rootScope)
	if err := admitSessionMetadata(
		context.Background(),
		rootAgent.Sessions,
		rootSession,
		rootScope,
		nil,
		rootAgent.ID,
	); err != nil {
		t.Fatalf("admit handoff root session: %v", err)
	}
	rootAgent.Sessions.AddMessage(rootSession, "user", "handoff fixture history")
	if err := rootAgent.Sessions.Save(rootSession); err != nil {
		t.Fatalf("save handoff root session: %v", err)
	}

	const rootTurnID = "p007-handoff-root-turn"
	const taskID = "subagent-p007-handoff"
	route := trackedSubagentResultRoute{
		SourceTurnID: rootTurnID, SourceAgentID: rootAgent.ID,
		SourceSessionKey:            rootSession,
		RootTurnID:                  rootTurnID,
		RootAgentID:                 rootAgent.ID,
		RootSessionKey:              rootSession,
		RootChannel:                 "test",
		RootChatID:                  "p007-handoff",
		RootPersistent:              true,
		RootLateContinuationAllowed: true,
		RootEnableSummary:           true,
		RootScope:                   session.CloneScope(rootScope),
		RootInbound: bus.InboundContext{
			Channel:  "test",
			ChatID:   "p007-handoff",
			ChatType: "direct",
			SenderID: "p007-handoff-user",
		},
	}
	return &p007HandoffFixture{
		loop:        loop,
		messageBus:  messageBus,
		rootSession: rootSession,
		route:       route,
		recordID: trackedSubagentResultID{
			SourceTurnID: rootTurnID,
			TaskID:       taskID,
		},
	}
}

func (fixture *p007HandoffFixture) publishLateResult(t *testing.T) {
	t.Helper()
	root := &turnState{
		turnID: fixture.route.RootTurnID, agentID: fixture.route.RootAgentID,
		sessionKey: fixture.route.RootSessionKey,
		channel:    fixture.route.RootChannel, chatID: fixture.route.RootChatID,
		terminalStatus: TurnEndStatusCompleted,
	}
	if _, loaded := fixture.loop.activeTurnStates.LoadOrStore(fixture.rootSession, root); loaded {
		t.Fatal("handoff root session already has an active turn")
	}
	fixture.loop.acceptTrackedSubagentResult(
		fixture.route,
		tools.SubagentCompletion{TaskID: fixture.recordID.TaskID, Status: "completed"},
		tools.NewToolResult("p007 handoff mailbox payload"),
	)
	fixture.loop.releaseSessionTurnState(fixture.rootSession, root)
	fixture.loop.markTrackedSubagentResultOutputReady(root.turnID)
}

func TestTrackedSubagentResultContinuationDrainsSteeringEnqueuedWhileGated(
	t *testing.T,
) {
	tests := []struct {
		name     string
		firstErr error
	}{
		{name: "success"},
		{name: "provider error", firstErr: errors.New("gated tracked continuation failed")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			firstGate := make(chan struct{})
			provider := newP007HandoffProvider()
			provider.firstGate = firstGate
			provider.firstErr = test.firstErr
			fixture := newP007HandoffFixture(t, provider)

			fixture.publishLateResult(t)
			firstCall := waitForP007HandoffProviderCall(t, provider)
			if firstCall.index != 1 ||
				!p007MessagesContain(firstCall.messages, "p007 handoff mailbox payload") {
				t.Fatalf("first tracked continuation call = %#v", firstCall)
			}
			if err := fixture.loop.enqueueSteeringMessage(
				fixture.rootSession,
				fixture.route.RootAgentID,
				providers.Message{Role: "user", Content: p007SteeringContent},
			); err != nil {
				t.Fatalf("enqueue steering during tracked continuation: %v", err)
			}
			if depth := fixture.loop.pendingSteeringCountForScope(fixture.rootSession); depth != 1 {
				t.Fatalf("gated continuation steering depth = %d, want 1", depth)
			}
			close(firstGate)

			secondCall := waitForP007HandoffProviderCall(t, provider)
			if secondCall.index != 2 ||
				!p007MessagesContain(secondCall.messages, p007SteeringContent) {
				t.Fatalf("steering continuation call = %#v", secondCall)
			}
			if count := p007MessageContentCount(
				secondCall.messages,
				p007SteeringContent,
			); count != 1 {
				t.Fatalf(
					"steering continuation prompt occurrences = %d, want exactly 1: %#v",
					count,
					secondCall.messages,
				)
			}
			waitForTrackedSubagentRuntimeRecord(
				t,
				fixture.loop,
				fixture.recordID,
				trackedSubagentResultClaimed,
				true,
			)
			if depth := fixture.loop.pendingSteeringCountForScope(fixture.rootSession); depth != 0 {
				t.Fatalf("steering depth after tracked continuation = %d, want 0", depth)
			}
			if active := fixture.loop.getActiveTurnState(fixture.rootSession); active != nil {
				t.Fatalf("tracked continuation retained active ownership: %#v", active)
			}
			calls := provider.snapshotCalls()
			if len(calls) != 2 {
				t.Fatalf("provider calls = %d, want result plus steering: %#v", len(calls), calls)
			}

			select {
			case outbound := <-fixture.messageBus.OutboundChan():
				if outbound.Content != provider.steerResponse ||
					outbound.SessionKey != fixture.rootSession {
					t.Fatalf("steering response outbound = %#v", outbound)
				}
			default:
				t.Fatal("tracked steering response was not published")
			}
			select {
			case duplicate := <-fixture.messageBus.OutboundChan():
				t.Fatalf("tracked continuation published duplicate outbound: %#v", duplicate)
			default:
			}
		})
	}
}

func p007MessageContentCount(messages []providers.Message, content string) int {
	count := 0
	for _, message := range messages {
		if strings.Contains(message.Content, content) {
			count++
		}
	}
	return count
}

func waitForP007HandoffProviderCall(
	t *testing.T,
	provider *p007HandoffProvider,
) p007HandoffProviderCall {
	t.Helper()
	select {
	case call := <-provider.called:
		return call
	case <-time.After(5 * time.Second):
		t.Fatal("tracked handoff provider was not called")
		return p007HandoffProviderCall{}
	}
}

func TestTrackedSubagentResultEligibleMailboxWakesAfterBlockingPlaceholderDeletion(
	t *testing.T,
) {
	tests := []struct {
		name   string
		delete func(*testing.T, *p007HandoffFixture, *turnState)
	}{
		{
			name: "empty Continue",
			delete: func(t *testing.T, fixture *p007HandoffFixture, blocker *turnState) {
				t.Helper()
				fixture.loop.steering.mu.Lock()
				steeringLocked := true
				defer func() {
					if steeringLocked {
						fixture.loop.steering.mu.Unlock()
					}
				}()

				unlock := fixture.loop.lockSessionTurn(fixture.rootSession)
				if !fixture.loop.activeTurnStates.CompareAndDelete(fixture.rootSession, blocker) {
					unlock()
					t.Fatal("failed to remove setup blocker before Continue")
				}
				unlock()

				type continueResult struct {
					response string
					err      error
				}
				continued := make(chan continueResult, 1)
				go func() {
					response, err := fixture.loop.Continue(
						context.Background(),
						fixture.rootSession,
						fixture.route.RootChannel,
						fixture.route.RootChatID,
					)
					continued <- continueResult{response: response, err: err}
				}()

				waitForP007ActiveTurnPrefix(
					t,
					fixture.loop,
					fixture.rootSession,
					"pending-continue-",
				)
				assertP007TrackedResultPumpIdle(t, fixture)

				fixture.loop.steering.mu.Unlock()
				steeringLocked = false
				select {
				case result := <-continued:
					if result.err != nil || result.response != "" {
						t.Fatalf("empty Continue result = (%q, %v)", result.response, result.err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("empty Continue did not delete its placeholder")
				}
			},
		},
		{
			name: "abandoned reservation",
			delete: func(t *testing.T, fixture *p007HandoffFixture, blocker *turnState) {
				t.Helper()
				if transferred := fixture.loop.abandonSessionTurnState(
					context.Background(),
					fixture.rootSession,
					blocker,
				); transferred {
					t.Fatal("empty abandoned placeholder unexpectedly transferred steering")
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := newP007HandoffProvider()
			provider.resultResponse = "eligible mailbox resumed"
			fixture := newP007HandoffFixture(t, provider)
			blocker := armP007EligibleResultBehindPlaceholder(t, fixture)

			test.delete(t, fixture, blocker)
			call := waitForP007HandoffProviderCall(t, provider)
			if !p007MessagesContain(call.messages, "task_id="+fixture.recordID.TaskID) ||
				!p007MessagesContain(call.messages, "p007 handoff mailbox payload") {
				t.Fatalf("woken mailbox provider call = %#v", call)
			}
			waitForTrackedSubagentRuntimeRecord(
				t,
				fixture.loop,
				fixture.recordID,
				trackedSubagentResultClaimed,
				true,
			)
			select {
			case outbound := <-fixture.messageBus.OutboundChan():
				if outbound.Content != provider.resultResponse {
					t.Fatalf("woken mailbox outbound = %#v", outbound)
				}
			default:
				t.Fatal("woken eligible mailbox did not publish its response")
			}
			if active := fixture.loop.getActiveTurnState(fixture.rootSession); active != nil {
				t.Fatalf("placeholder remained after mailbox wake: %#v", active)
			}
		})
	}
}

func TestTrackedSubagentSteeringPanicReleasesExactPlaceholder(t *testing.T) {
	loop := &AgentLoop{steering: newSteeringQueue(SteeringAll)}
	route := p007TrackedRoute(
		"panic-source", "panic-agent", "panic-session",
		"panic-root", "panic-agent", "panic-session",
	)
	if err := loop.steering.pushScope(
		route.RootSessionKey,
		providers.Message{Role: "user", Content: "queued steering"},
	); err != nil {
		t.Fatal(err)
	}
	placeholder, messages, ok := loop.claimTrackedSubagentSteeringContinuation(route)
	if !ok {
		t.Fatal("strict steering placeholder was not claimed")
	}
	agent := &AgentInstance{ID: route.RootAgentID, Tools: tools.NewToolRegistry()}
	agent.Tools.Register(&p007PanickingMessageResetTool{})
	func() {
		defer loop.releaseTrackedSubagentOutputTurn(route.RootSessionKey, placeholder)
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatal("panicking resetter did not panic")
			}
		}()
		_, _ = loop.runTrackedSubagentSteeringContinuation(
			context.Background(),
			agent,
			nil,
			route,
			placeholder,
			messages,
		)
	}()
	if active := loop.getActiveTurnState(route.RootSessionKey); active != nil {
		t.Fatalf("panicking resetter retained placeholder: %#v", active)
	}
}

func assertP007TrackedResultPumpIdle(
	t *testing.T,
	fixture *p007HandoffFixture,
) {
	t.Helper()
	scope := trackedSubagentResultScope{
		AgentID: fixture.route.RootAgentID, SessionKey: fixture.rootSession,
	}
	fixture.loop.trackedSubagentResults.mu.Lock()
	state := fixture.loop.trackedSubagentResults.scopes[scope]
	pumping := state != nil && state.pumping
	fixture.loop.trackedSubagentResults.mu.Unlock()
	if pumping {
		t.Fatal("eligible result bypassed the active session placeholder")
	}
}

func armP007EligibleResultBehindPlaceholder(
	t *testing.T,
	fixture *p007HandoffFixture,
) *turnState {
	t.Helper()
	fixture.loop.watchTrackedSubagentResultRoute(fixture.route)
	root := &turnState{
		turnID: fixture.route.RootTurnID, agentID: fixture.route.RootAgentID,
		sessionKey: fixture.rootSession,
		channel:    fixture.route.RootChannel, chatID: fixture.route.RootChatID,
		terminalStatus: TurnEndStatusCompleted,
	}
	fixture.loop.activeTurnStates.Store(fixture.rootSession, root)
	fixture.loop.releaseSessionTurnState(fixture.rootSession, root)
	fixture.loop.markTrackedSubagentResultOutputReady(root.turnID)

	blocker := &turnState{
		turnID:  "p007-blocking-placeholder-" + strings.ReplaceAll(t.Name(), "/", "-"),
		agentID: fixture.route.RootAgentID, sessionKey: fixture.rootSession,
		channel: fixture.route.RootChannel, chatID: fixture.route.RootChatID,
		phase: TurnPhaseSetup,
	}
	if _, loaded := fixture.loop.activeTurnStates.LoadOrStore(fixture.rootSession, blocker); loaded {
		t.Fatal("failed to install blocking placeholder")
	}
	fixture.loop.acceptTrackedSubagentResult(
		fixture.route,
		tools.SubagentCompletion{TaskID: fixture.recordID.TaskID, Status: "completed"},
		tools.NewToolResult("p007 handoff mailbox payload"),
	)
	record := waitForTrackedSubagentRuntimeRecord(
		t,
		fixture.loop,
		fixture.recordID,
		trackedSubagentResultPendingRoot,
		true,
	)
	if !record.rootEligible {
		t.Fatalf("blocked mailbox record = %#v, want eligible root result", record)
	}
	return blocker
}

func waitForP007ActiveTurnPrefix(
	t *testing.T,
	loop *AgentLoop,
	sessionKey string,
	prefix string,
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		active := loop.getActiveTurnState(sessionKey)
		if active != nil && strings.HasPrefix(active.turnID, prefix) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"active turn for %q did not acquire prefix %q; active=%#v",
				sessionKey,
				prefix,
				active,
			)
		}
		time.Sleep(time.Millisecond)
	}
}
