package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

const (
	trackedSpawnPipelineChildResult = "tracked pipeline child result"
	trackedSpawnPipelineTaskID      = "subagent-1"
	trackedSpawnPipelineAgentID     = "pipeline-parent"
	trackedSpawnPipelineChildID     = "pipeline-child"
	trackedSpawnPipelineParentModel = "pipeline-parent-model"
	trackedSpawnPipelineChildModel  = "pipeline-child-model"
	trackedSpawnPipelineChannel     = "cli"
	trackedSpawnPipelineChatID      = "named-pipeline-chat"
)

type trackedSpawnPipelineProviderCall struct {
	model    string
	messages []providers.Message
	tools    []providers.ToolDefinition
}

type trackedSpawnPipelineProvider struct {
	mu sync.Mutex

	includeBarrier bool
	childGate      <-chan struct{}
	childStarted   chan struct{}
	childStartOnce sync.Once

	calls                 []trackedSpawnPipelineProviderCall
	rootInitialCalls      int
	resultPromptCalls     int
	resultResponseCalls   int
	spawnAcknowledgements []string
	initialResponse       string
	resultResponse        string
}

func (provider *trackedSpawnPipelineProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	definitions []providers.ToolDefinition,
	model string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	call := trackedSpawnPipelineProviderCall{
		model:    model,
		messages: append([]providers.Message(nil), messages...),
		tools:    append([]providers.ToolDefinition(nil), definitions...),
	}
	child := model == trackedSpawnPipelineChildModel ||
		trackedSpawnPipelineIsChildPrompt(messages)
	resultPrompt := trackedSpawnPipelineHasResultPrompt(messages)
	acknowledgements := trackedSpawnPipelineAcknowledgements(messages)

	provider.mu.Lock()
	provider.calls = append(provider.calls, call)
	provider.spawnAcknowledgements = append(
		provider.spawnAcknowledgements,
		acknowledgements...,
	)
	if resultPrompt {
		provider.resultPromptCalls++
	}
	provider.mu.Unlock()

	if child {
		provider.childStartOnce.Do(func() { close(provider.childStarted) })
		select {
		case <-provider.childGate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &providers.LLMResponse{
			Content:      trackedSpawnPipelineChildResult,
			FinishReason: "stop",
		}, nil
	}

	if resultPrompt {
		provider.mu.Lock()
		provider.resultResponseCalls++
		provider.mu.Unlock()
		return &providers.LLMResponse{
			Content:      provider.resultResponse,
			FinishReason: "stop",
		}, nil
	}

	provider.mu.Lock()
	provider.rootInitialCalls++
	rootCall := provider.rootInitialCalls
	provider.mu.Unlock()
	if rootCall == 1 {
		toolCalls := []providers.ToolCall{{
			ID:   "tracked-spawn-call",
			Type: "function",
			Name: "spawn",
			Arguments: map[string]any{
				"task":     "return the tracked pipeline fixture result",
				"label":    "pipeline-fixture",
				"agent_id": trackedSpawnPipelineChildID,
			},
		}}
		if provider.includeBarrier {
			toolCalls = append(toolCalls, providers.ToolCall{
				ID:        "tracked-result-barrier-call",
				Type:      "function",
				Name:      "tracked_result_barrier",
				Arguments: map[string]any{},
			})
		}
		return &providers.LLMResponse{
			ToolCalls:    toolCalls,
			FinishReason: "tool_calls",
		}, nil
	}

	return &providers.LLMResponse{
		Content:      provider.initialResponse,
		FinishReason: "stop",
	}, nil
}

func (provider *trackedSpawnPipelineProvider) snapshot() (
	resultPrompts int,
	resultResponses int,
	acknowledgements []string,
	calls []trackedSpawnPipelineProviderCall,
) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.resultPromptCalls,
		provider.resultResponseCalls,
		append([]string(nil), provider.spawnAcknowledgements...),
		append([]trackedSpawnPipelineProviderCall(nil), provider.calls...)
}

func trackedSpawnPipelineIsChildPrompt(messages []providers.Message) bool {
	for _, message := range messages {
		if message.Role == "system" &&
			strings.Contains(message.Content, "You are a spawned subagent") {
			return true
		}
	}
	return false
}

func trackedSpawnPipelineHasResultPrompt(messages []providers.Message) bool {
	for _, message := range messages {
		if strings.Contains(message.Content, "[Subagent Result task_id="+trackedSpawnPipelineTaskID) &&
			strings.Contains(message.Content, "status=completed") &&
			strings.Contains(message.Content, "source_turn_id=") &&
			strings.Contains(message.Content, trackedSpawnPipelineChildResult) {
			return true
		}
	}
	return false
}

func trackedSpawnPipelineAcknowledgements(messages []providers.Message) []string {
	var result []string
	for _, message := range messages {
		if message.Role == "tool" &&
			strings.Contains(message.Content, "task_id="+trackedSpawnPipelineTaskID) {
			result = append(result, message.Content)
		}
	}
	return result
}

type trackedSpawnPipelineBarrierTool struct {
	loop         *AgentLoop
	releaseChild func()
}

type p007SequentialResultTool struct{ loop *AgentLoop }

func (*p007SequentialResultTool) Name() string        { return "queue_second_result" }
func (*p007SequentialResultTool) Description() string { return "queue a second tracked result" }

func (*p007SequentialResultTool) Parameters() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

func (tool *p007SequentialResultTool) Execute(
	ctx context.Context,
	_ map[string]any,
) *tools.ToolResult {
	turn := turnStateFromContext(ctx)
	route, err := snapshotTrackedSubagentResultRoute(turn)
	if err != nil {
		return tools.ErrorResult(err.Error()).WithError(err)
	}
	tool.loop.acceptTrackedSubagentResult(
		route,
		tools.SubagentCompletion{TaskID: "subagent-second", Status: "completed"},
		tools.NewToolResult("SECOND_RESULT_CANARY"),
	)
	return tools.SilentResult("second result queued")
}

type p007SequentialResultProvider struct {
	mu    sync.Mutex
	calls [][]providers.Message
}

func (provider *p007SequentialResultProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	provider.mu.Lock()
	provider.calls = append(provider.calls, append([]providers.Message(nil), messages...))
	call := len(provider.calls)
	provider.mu.Unlock()
	if call == 1 {
		return &providers.LLMResponse{
			ToolCalls: []providers.ToolCall{{
				ID: "queue-second", Name: "queue_second_result",
				Arguments: map[string]any{},
			}},
			FinishReason: "tool_calls",
		}, nil
	}
	return &providers.LLMResponse{Content: "sequential results integrated", FinishReason: "stop"}, nil
}

func TestTrackedSubagentSequentialResultPromptsInjectOnceEach(t *testing.T) {
	provider := &p007SequentialResultProvider{}
	loop, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	loop.RegisterTool(&p007SequentialResultTool{loop: loop})
	first := trackedSubagentResultPromptMessage(trackedSubagentResultClaim{
		id:         trackedSubagentResultID{SourceTurnID: "first-source", TaskID: "subagent-first"},
		completion: tools.SubagentCompletion{TaskID: "subagent-first", Status: "completed"},
		content:    "FIRST_RESULT_CANARY",
	})
	opts := makeTestProcessOpts("sequential-result-session")
	opts.InitialSteeringMessages = []providers.Message{first}
	response, err := loop.runAgentLoop(context.Background(), agent, opts)
	if err != nil || response != "sequential results integrated" {
		t.Fatalf("sequential result run = %q, %v", response, err)
	}
	provider.mu.Lock()
	calls := append([][]providers.Message(nil), provider.calls...)
	provider.mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(calls))
	}
	for _, canary := range []string{"FIRST_RESULT_CANARY", "SECOND_RESULT_CANARY"} {
		count := 0
		for _, message := range calls[1] {
			if strings.Contains(message.Content, canary) {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("%s occurrences in second prompt = %d, want 1: %#v", canary, count, calls[1])
		}
	}
}

func (*trackedSpawnPipelineBarrierTool) Name() string { return "tracked_result_barrier" }

func (*trackedSpawnPipelineBarrierTool) Description() string {
	return "Test-only barrier that waits for a tracked subagent result to enter its mailbox"
}

func (*trackedSpawnPipelineBarrierTool) Parameters() map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"additionalProperties": false,
	}
}

func (tool *trackedSpawnPipelineBarrierTool) Execute(
	ctx context.Context,
	_ map[string]any,
) *tools.ToolResult {
	tool.releaseChild()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(3 * time.Second)
	defer timeout.Stop()
	for {
		if tool.trackedResultQueued() {
			return tools.SilentResult("tracked result is queued")
		}
		select {
		case <-ctx.Done():
			return tools.ErrorResult(ctx.Err().Error()).WithError(ctx.Err())
		case <-timeout.C:
			err := fmt.Errorf("timed out waiting for tracked result mailbox")
			return tools.ErrorResult(err.Error()).WithError(err)
		case <-ticker.C:
		}
	}
}

func (tool *trackedSpawnPipelineBarrierTool) trackedResultQueued() bool {
	if tool == nil || tool.loop == nil {
		return false
	}
	tool.loop.trackedSubagentResults.mu.Lock()
	defer tool.loop.trackedSubagentResults.mu.Unlock()
	for _, record := range tool.loop.trackedSubagentResults.records {
		if record != nil &&
			record.completion.TaskID == trackedSpawnPipelineTaskID &&
			record.state == trackedSubagentResultPendingPreferred {
			return true
		}
	}
	return false
}

func TestTrackedSpawnPipelineFastResultUsesSingleParentEnvelope(t *testing.T) {
	childGate, releaseChild := trackedSpawnPipelineGate()
	t.Cleanup(releaseChild)
	provider := &trackedSpawnPipelineProvider{
		includeBarrier:  true,
		childGate:       childGate,
		childStarted:    make(chan struct{}),
		initialResponse: "unexpected fast-path response without result",
		resultResponse:  "fast tracked result integrated",
	}
	loop, messageBus, parent := newTrackedSpawnPipelineLoop(t, provider)
	// The tracked result arrives after the only configured tool iteration. Its
	// pending result prompt must keep the coordinator alive for one follow-up
	// LLM call rather than being irreversibly claimed into a finalized turn.
	parent.MaxIterations = 1
	loop.RegisterTool(&trackedSpawnPipelineBarrierTool{
		loop: loop, releaseChild: releaseChild,
	})

	sessionKey := "named-fast-tracked-spawn-session"
	response, err := loop.runAgentLoop(
		context.Background(),
		parent,
		trackedSpawnPipelineOptions(sessionKey, "run the fast tracked spawn"),
	)
	if err != nil {
		t.Fatalf("runAgentLoop() error = %v", err)
	}
	if response != provider.resultResponse {
		t.Fatalf("runAgentLoop() response = %q, want %q", response, provider.resultResponse)
	}

	outbounds := collectTrackedSpawnPipelineOutbounds(
		t, messageBus.OutboundChan(), provider.resultResponse, 3*time.Second,
	)
	assertTrackedSpawnPipelineDelivery(
		t, provider, messageBus, outbounds, sessionKey, provider.resultResponse,
	)
	if len(outbounds) != 1 {
		t.Fatalf("fast-path outbound count = %d, want exactly one: %#v", len(outbounds), outbounds)
	}
	resultPrompts, resultResponses, acknowledgements, calls := provider.snapshot()
	_ = resultPrompts
	_ = resultResponses
	_ = acknowledgements
	for _, call := range calls {
		if trackedSpawnPipelineHasResultPrompt(call.messages) && len(call.tools) != 0 {
			t.Fatalf("over-limit result follow-up exposed tools: %#v", call.tools)
		}
	}
}

func TestTrackedSpawnPipelineLateResultContinuesExactNamedSessionOnce(t *testing.T) {
	childGate, releaseChild := trackedSpawnPipelineGate()
	provider := &trackedSpawnPipelineProvider{
		childGate:       childGate,
		childStarted:    make(chan struct{}),
		initialResponse: "tracked spawn accepted",
		resultResponse:  "late tracked result integrated",
	}
	t.Cleanup(releaseChild)
	loop, messageBus, parent := newTrackedSpawnPipelineLoop(t, provider)

	sessionKey := "named-late-tracked-spawn-session"
	response, err := loop.runAgentLoop(
		context.Background(),
		parent,
		trackedSpawnPipelineOptions(sessionKey, "run the late tracked spawn"),
	)
	if err != nil {
		t.Fatalf("runAgentLoop() error = %v", err)
	}
	if response != provider.initialResponse {
		t.Fatalf("initial runAgentLoop() response = %q, want %q", response, provider.initialResponse)
	}

	select {
	case <-provider.childStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("tracked child did not reach its late-result gate")
	}
	// The child cannot finish until after runAgentLoop has returned and released
	// the exact named session. Its completion must therefore use the continuation
	// path rather than an active-turn checkpoint.
	releaseChild()

	outbounds := collectTrackedSpawnPipelineOutbounds(
		t, messageBus.OutboundChan(), provider.resultResponse, 3*time.Second,
	)
	assertTrackedSpawnPipelineDelivery(
		t, provider, messageBus, outbounds, sessionKey, provider.resultResponse,
	)
	if len(outbounds) != 2 {
		t.Fatalf("late-path outbound count = %d, want initial plus one result: %#v", len(outbounds), outbounds)
	}
	if outbounds[0].Content != provider.initialResponse {
		t.Fatalf("late-path initial outbound = %#v", outbounds[0])
	}
}

func TestTrackedSpawnPipelineRejectedAdmissionLeavesNoMailboxMetadata(t *testing.T) {
	childGate, releaseChild := trackedSpawnPipelineGate()
	defer releaseChild()
	provider := &trackedSpawnPipelineProvider{
		childGate:       childGate,
		childStarted:    make(chan struct{}),
		initialResponse: "spawn rejected",
		resultResponse:  "unexpected result",
	}
	loop, _, parent := newTrackedSpawnPipelineLoop(t, provider)
	parent.Subagents = &config.SubagentsConfig{}
	_, _ = loop.runAgentLoop(
		context.Background(),
		parent,
		trackedSpawnPipelineOptions("rejected-spawn-session", "reject spawn"),
	)
	trackedTurns := 0
	loop.trackedSubagentResults.trackedTurns.Range(func(_, _ any) bool {
		trackedTurns++
		return true
	})
	loop.trackedSubagentResults.mu.Lock()
	defer loop.trackedSubagentResults.mu.Unlock()
	if trackedTurns != 0 || len(loop.trackedSubagentResults.records) != 0 ||
		len(loop.trackedSubagentResults.released) != 0 ||
		len(loop.trackedSubagentResults.rootsBySession) != 0 {
		t.Fatalf(
			"rejected spawn retained mailbox metadata: turns=%d records=%d released=%d roots=%d",
			trackedTurns,
			len(loop.trackedSubagentResults.records),
			len(loop.trackedSubagentResults.released),
			len(loop.trackedSubagentResults.rootsBySession),
		)
	}
}

func trackedSpawnPipelineGate() (<-chan struct{}, func()) {
	gate := make(chan struct{})
	var once sync.Once
	return gate, func() { once.Do(func() { close(gate) }) }
}

func newTrackedSpawnPipelineLoop(
	t *testing.T,
	provider *trackedSpawnPipelineProvider,
) (*AgentLoop, *bus.MessageBus, *AgentInstance) {
	t.Helper()
	root := t.TempDir()
	parentWorkspace := filepath.Join(root, "parent")
	childWorkspace := filepath.Join(root, "child")
	for _, workspace := range []string{parentWorkspace, childWorkspace} {
		if err := os.MkdirAll(workspace, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = parentWorkspace
	cfg.Agents.Defaults.ModelName = trackedSpawnPipelineParentModel
	cfg.Agents.Defaults.MaxToolIterations = 6
	cfg.Agents.Defaults.MaxParallelTurns = 2
	cfg.Agents.Defaults.ToolFeedback.Enabled = false
	cfg.Agents.List = []config.AgentConfig{
		{
			ID:        trackedSpawnPipelineAgentID,
			Default:   true,
			Workspace: parentWorkspace,
			Model: &config.AgentModelConfig{
				Primary: trackedSpawnPipelineParentModel,
			},
			Subagents: &config.SubagentsConfig{
				AllowAgents: []string{trackedSpawnPipelineChildID},
			},
		},
		{
			ID:        trackedSpawnPipelineChildID,
			Workspace: childWorkspace,
			Model: &config.AgentModelConfig{
				Primary: trackedSpawnPipelineChildModel,
			},
		},
	}
	cfg.Tools.Spawn.Enabled = true
	cfg.Tools.Subagent.Enabled = true
	cfg.Tools.SpawnStatus.Enabled = true

	ensureStrictTestModelSelection(cfg, provider)
	messageBus := bus.NewMessageBus()
	loop := NewAgentLoop(cfg, messageBus, provider)
	bindLegacyTestProviderToAliases(loop, cfg, provider)
	t.Cleanup(func() {
		loop.Close()
		messageBus.Close()
	})
	parent, ok := loop.GetRegistry().GetAgent(trackedSpawnPipelineAgentID)
	if !ok || parent == nil {
		t.Fatal("named tracked-spawn parent agent is unavailable")
	}
	for _, name := range []string{"spawn", "spawn_status", "subagent"} {
		if !parent.Tools.HasRegistered(name) {
			t.Fatalf("real recursion catalog did not register %q", name)
		}
	}
	return loop, messageBus, parent
}

func trackedSpawnPipelineOptions(sessionKey, userMessage string) processOptions {
	inbound := &bus.InboundContext{
		Channel:  trackedSpawnPipelineChannel,
		ChatID:   trackedSpawnPipelineChatID,
		ChatType: "direct",
		SenderID: "pipeline-test-user",
	}
	return processOptions{
		SessionKey:      sessionKey,
		Channel:         trackedSpawnPipelineChannel,
		ChatID:          trackedSpawnPipelineChatID,
		UserMessage:     userMessage,
		DefaultResponse: defaultResponse,
		EnableSummary:   false,
		SendResponse:    true,
		NoHistory:       false,
		InboundContext:  inbound,
	}
}

func collectTrackedSpawnPipelineOutbounds(
	t *testing.T,
	outbound <-chan bus.OutboundMessage,
	resultResponse string,
	timeout time.Duration,
) []bus.OutboundMessage {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	var messages []bus.OutboundMessage
	for {
		select {
		case message, ok := <-outbound:
			if !ok {
				t.Fatal("outbound stream closed before tracked result response")
			}
			messages = append(messages, message)
			if message.Content == resultResponse {
				quiet := time.NewTimer(100 * time.Millisecond)
				defer quiet.Stop()
				for {
					select {
					case extra, open := <-outbound:
						if !open {
							return messages
						}
						messages = append(messages, extra)
					case <-quiet.C:
						return messages
					}
				}
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for result-bearing outbound; got %#v", messages)
		}
	}
}

func assertTrackedSpawnPipelineDelivery(
	t *testing.T,
	provider *trackedSpawnPipelineProvider,
	messageBus *bus.MessageBus,
	outbounds []bus.OutboundMessage,
	sessionKey string,
	resultResponse string,
) {
	t.Helper()
	resultPrompts, resultResponses, acknowledgements, calls := provider.snapshot()
	if resultPrompts != 1 || resultResponses != 1 {
		t.Fatalf(
			"result-bearing provider calls = prompts:%d responses:%d, want 1/1; calls=%#v",
			resultPrompts, resultResponses, calls,
		)
	}
	if len(acknowledgements) == 0 {
		t.Fatalf("manager spawn acknowledgement was absent from provider prompts: %#v", calls)
	}
	for _, acknowledgement := range acknowledgements {
		if !strings.Contains(acknowledgement, "Spawned subagent 'pipeline-fixture'") ||
			!strings.Contains(acknowledgement, "task_id="+trackedSpawnPipelineTaskID) {
			t.Fatalf("spawn acknowledgement = %q", acknowledgement)
		}
	}
	childCalls := 0
	for _, call := range calls {
		if call.model == trackedSpawnPipelineChildModel {
			childCalls++
		}
		if trackedSpawnPipelineHasResultPrompt(call.messages) &&
			call.model != trackedSpawnPipelineParentModel {
			t.Fatalf(
				"result prompt used model %q, want named parent model %q",
				call.model,
				trackedSpawnPipelineParentModel,
			)
		}
	}
	if childCalls != 1 {
		t.Fatalf("child provider call count = %d, want 1; calls=%#v", childCalls, calls)
	}

	resultOutboundCount := 0
	for _, outbound := range outbounds {
		if strings.Contains(outbound.Content, trackedSpawnPipelineChildResult) {
			t.Fatalf("raw child ForUser escaped as outbound: %#v", outbound)
		}
		if outbound.Content != resultResponse {
			continue
		}
		resultOutboundCount++
		if outbound.AgentID != trackedSpawnPipelineAgentID ||
			outbound.SessionKey != sessionKey ||
			outbound.Channel != trackedSpawnPipelineChannel ||
			outbound.ChatID != trackedSpawnPipelineChatID {
			t.Fatalf("result outbound lost named route: %#v", outbound)
		}
	}
	if resultOutboundCount != 1 {
		t.Fatalf("result-bearing outbound count = %d, want 1: %#v", resultOutboundCount, outbounds)
	}

	select {
	case inbound := <-messageBus.InboundChan():
		t.Fatalf("tracked spawn published a managed system inbound: %#v", inbound)
	default:
	}
}
