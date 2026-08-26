package agent

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/tools"
)

const p007GenericManagerResult = "generic manager-backed async result"

type p007GenericManagerSpawnObservation struct {
	completion tools.SubagentCompletion
	metadataOK bool
	tracked    bool
}

// p007GenericManagerSpawnTool deliberately has the public name "spawn" while
// remaining a third-party AsyncExecutor. Its callback is produced by the
// public SubagentManager.Spawn API, so it carries authentic task metadata but
// must not acquire the private first-party SpawnTool delivery semantics.
type p007GenericManagerSpawnTool struct {
	manager     *tools.SubagentManager
	observation chan p007GenericManagerSpawnObservation
}

func (*p007GenericManagerSpawnTool) Name() string { return "spawn" }

func (*p007GenericManagerSpawnTool) Description() string {
	return "test-only generic async spawn backed by the public subagent manager"
}

func (*p007GenericManagerSpawnTool) Parameters() map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"additionalProperties": false,
	}
}

func (*p007GenericManagerSpawnTool) Execute(
	context.Context,
	map[string]any,
) *tools.ToolResult {
	return tools.AsyncResult("generic spawn scheduled")
}

func (tool *p007GenericManagerSpawnTool) ExecuteAsync(
	ctx context.Context,
	_ map[string]any,
	callback tools.AsyncCallback,
) *tools.ToolResult {
	ack, err := tool.manager.Spawn(
		ctx,
		"generic manager task",
		"generic-manager",
		"",
		tools.ToolChannel(ctx),
		tools.ToolChatID(ctx),
		func(callbackCtx context.Context, result *tools.ToolResult) {
			completion, metadataOK := tools.SubagentCompletionFromContext(callbackCtx)
			_, tracked := tools.TrackedSpawnCompletionFromContext(callbackCtx)
			callback(callbackCtx, result)
			tool.observation <- p007GenericManagerSpawnObservation{
				completion: completion,
				metadataOK: metadataOK,
				tracked:    tracked,
			}
		},
	)
	if err != nil {
		return tools.ErrorResult(err.Error()).WithError(err)
	}
	return tools.AsyncResult(ack)
}

var _ tools.AsyncExecutor = (*p007GenericManagerSpawnTool)(nil)

type p007GenericManagerSpawnProvider struct {
	calls atomic.Int64
}

func (provider *p007GenericManagerSpawnProvider) Chat(
	_ context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	if provider.calls.Add(1) == 1 {
		return &providers.LLMResponse{
			ToolCalls: []providers.ToolCall{{
				ID: "generic-manager-spawn-call", Name: "spawn",
				Arguments: map[string]any{},
			}},
			FinishReason: "tool_calls",
		}, nil
	}
	return &providers.LLMResponse{
		Content: "generic manager parent completed", FinishReason: "stop",
	}, nil
}

func TestGenericManagerBackedAsyncSpawnKeepsSystemInboundDelivery(t *testing.T) {
	workspace := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Agents.Defaults.ModelName = "generic-manager-model"
	cfg.Agents.Defaults.MaxToolIterations = 4
	cfg.Agents.List = []config.AgentConfig{{
		ID: "generic-parent", Default: true, Workspace: workspace,
		Model: &config.AgentModelConfig{Primary: "generic-manager-model"},
	}}
	// Keep the first-party recursion catalog out of this fixture. The tool with
	// the public name "spawn" below is intentionally an ordinary registration.
	cfg.Tools.Spawn.Enabled = false
	cfg.Tools.SpawnStatus.Enabled = false
	cfg.Tools.Subagent.Enabled = false
	// The generic callback intentionally races the parent tool pipeline. Warm
	// the config's unrelated lazy redaction cache before launching that race.
	_ = cfg.FilterSensitiveData("initialize generic async fixture")

	provider := &p007GenericManagerSpawnProvider{}
	messageBus := bus.NewMessageBus()
	loop := newTestAgentLoopWithStrictModels(cfg, messageBus, provider)
	t.Cleanup(func() {
		loop.Close()
		messageBus.Close()
	})
	parent := loop.GetRegistry().GetDefaultAgent()
	if parent == nil {
		t.Fatal("generic parent agent is unavailable")
	}

	manager := tools.NewSubagentManager(provider, "generic-manager-model", workspace)
	manager.SetSpawner(func(
		context.Context,
		string,
		string,
		string,
		*tools.ToolRegistry,
		int,
		float64,
		bool,
		bool,
	) (*tools.ToolResult, error) {
		return &tools.ToolResult{ForLLM: p007GenericManagerResult}, nil
	})
	genericSpawn := &p007GenericManagerSpawnTool{
		manager:     manager,
		observation: make(chan p007GenericManagerSpawnObservation, 1),
	}
	parent.Tools.Register(genericSpawn)
	registered, ok := parent.Tools.GetRegistered("spawn")
	if !ok || registered != genericSpawn {
		t.Fatalf("registered spawn = %T, want generic manager-backed tool", registered)
	}

	response, err := loop.runAgentLoop(context.Background(), parent, processOptions{
		SessionKey:      "generic-manager-named-session",
		Channel:         "cli",
		ChatID:          "generic-manager-chat",
		UserMessage:     "run the generic manager-backed async tool",
		DefaultResponse: defaultResponse,
		EnableSummary:   false,
		SendResponse:    false,
	})
	if err != nil {
		t.Fatalf("runAgentLoop() error = %v", err)
	}
	if response != "generic manager parent completed" {
		t.Fatalf("runAgentLoop() response = %q", response)
	}

	select {
	case observation := <-genericSpawn.observation:
		if !observation.metadataOK || observation.completion.TaskID != "subagent-1" ||
			observation.completion.Status != "completed" {
			t.Fatalf("public manager callback metadata = %#v", observation)
		}
		if observation.tracked {
			t.Fatal("public manager callback acquired private first-party spawn marker")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("generic manager callback did not complete")
	}

	select {
	case inbound := <-messageBus.InboundChan():
		if inbound.Content != p007GenericManagerResult ||
			inbound.Context.Channel != "system" ||
			inbound.Context.ChatID != "cli:generic-manager-chat" ||
			inbound.Context.SenderID != "async:spawn" {
			t.Fatalf("generic async system inbound = %#v", inbound)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("generic manager-backed spawn did not use generic system inbound delivery")
	}

	loop.trackedSubagentResults.mu.Lock()
	trackedRecords := len(loop.trackedSubagentResults.records)
	loop.trackedSubagentResults.mu.Unlock()
	if trackedRecords != 0 {
		t.Fatalf("generic manager callback entered tracked mailbox: %d record(s)", trackedRecords)
	}
}

type p007PolicyCanaryTool struct {
	name       string
	executions atomic.Int64
}

func (tool *p007PolicyCanaryTool) Name() string { return tool.name }

func (tool *p007PolicyCanaryTool) Description() string {
	return "test-only late-continuation policy canary"
}

func (*p007PolicyCanaryTool) Parameters() map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"additionalProperties": false,
	}
}

func (tool *p007PolicyCanaryTool) Execute(
	context.Context,
	map[string]any,
) *tools.ToolResult {
	tool.executions.Add(1)
	return tools.NewToolResult("POLICY_CANARY_EXECUTED")
}

type p007LatePolicyProviderCall struct {
	messages []providers.Message
	tools    []providers.ToolDefinition
}

type p007LatePolicyProvider struct {
	mu    sync.Mutex
	calls []p007LatePolicyProviderCall
	done  chan struct{}
	once  sync.Once
}

func (provider *p007LatePolicyProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	definitions []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	provider.mu.Lock()
	provider.calls = append(provider.calls, p007LatePolicyProviderCall{
		messages: session.CloneMessages(messages),
		tools:    append([]providers.ToolDefinition(nil), definitions...),
	})
	call := len(provider.calls)
	provider.mu.Unlock()
	if call == 1 {
		return &providers.LLMResponse{
			Content: "attempting a disallowed canary",
			ToolCalls: []providers.ToolCall{{
				ID: "late-policy-canary-call", Name: "late_policy_canary",
				Arguments: map[string]any{},
			}},
			FinishReason: "tool_calls",
		}, nil
	}
	provider.once.Do(func() { close(provider.done) })
	return &providers.LLMResponse{
		Content: "late policy result integrated", FinishReason: "stop",
	}, nil
}

func (provider *p007LatePolicyProvider) snapshot() []p007LatePolicyProviderCall {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	result := make([]p007LatePolicyProviderCall, len(provider.calls))
	copy(result, provider.calls)
	return result
}

func TestTrackedSubagentLateContinuationPreservesParentCapabilityCaps(t *testing.T) {
	t.Setenv("PICOCLAW_BUILTIN_SKILLS", t.TempDir())
	workspace := t.TempDir()
	writeTurnProfileSkill(
		t,
		workspace,
		"late-policy-skill",
		"---\ndescription: P007_SKILL_CATALOG_CANARY\n---\n# P007_SKILL_BODY_CANARY",
	)

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Agents.Defaults.ModelName = "late-policy-model"
	cfg.Agents.Defaults.MaxToolIterations = 4
	cfg.Agents.List = []config.AgentConfig{{
		ID: "late-policy-parent", Default: true, Workspace: workspace,
		Model: &config.AgentModelConfig{Primary: "late-policy-model"},
	}}
	provider := &p007LatePolicyProvider{done: make(chan struct{})}
	messageBus := bus.NewMessageBus()
	loop := newTestAgentLoopWithStrictModels(cfg, messageBus, provider)
	t.Cleanup(func() {
		loop.Close()
		messageBus.Close()
	})
	parent := loop.GetRegistry().GetDefaultAgent()
	if parent == nil {
		t.Fatal("late-policy parent agent is unavailable")
	}
	allowedTool := &p007PolicyCanaryTool{name: "late_policy_allowed"}
	disallowedCanary := &p007PolicyCanaryTool{name: "late_policy_canary"}
	parent.Tools.Register(allowedTool)
	parent.Tools.Register(disallowedCanary)

	rootScope := &session.SessionScope{
		Version: session.ScopeVersionV1, AgentID: parent.ID, Channel: "test",
		Dimensions: []string{"chat"}, Values: map[string]string{"chat": "late-policy-chat"},
	}
	rootSession := session.BuildSessionKey(*rootScope)
	if err := admitSessionMetadata(
		context.Background(), parent.Sessions, rootSession, rootScope, nil, parent.ID,
	); err != nil {
		t.Fatalf("admit late-policy session: %v", err)
	}
	parent.Sessions.AddMessage(rootSession, "user", "persistent parent history")
	if err := parent.Sessions.Save(rootSession); err != nil {
		t.Fatalf("save late-policy session: %v", err)
	}

	parentProfile := config.EffectiveTurnProfile{
		Enabled:          true,
		HistoryMode:      config.TurnProfileModeDefault,
		SystemPromptMode: config.TurnProfileModeOff,
		SkillsMode:       config.TurnProfileModeOff,
		ToolsMode:        config.TurnProfileModeCustom,
		AllowedTools:     []string{allowedTool.Name()},
	}
	rootInbound := &bus.InboundContext{
		Channel: "test", ChatID: "late-policy-chat", ChatType: "direct",
		SenderID: "late-policy-user",
	}
	root := &turnState{
		turnID: "late-policy-root-turn", agentID: parent.ID, sessionKey: rootSession,
		channel: "test", chatID: "late-policy-chat",
		opts: processOptions{Dispatch: DispatchRequest{
			SessionKey: rootSession, SessionScope: session.CloneScope(rootScope),
			InboundContext: rootInbound,
		}},
		profile:        parentProfile,
		terminalStatus: TurnEndStatusCompleted,
	}
	loop.activeTurnStates.Store(rootSession, root)
	route, err := snapshotTrackedSubagentResultRoute(root)
	if err != nil {
		t.Fatalf("snapshot late-policy parent route: %v", err)
	}
	if !reflect.DeepEqual(route.RootProfile, parentProfile) {
		t.Fatalf("captured late-policy profile = %#v, want %#v", route.RootProfile, parentProfile)
	}
	completion := tools.SubagentCompletion{TaskID: "subagent-policy-1", Status: "completed"}
	loop.acceptTrackedSubagentResult(route, completion, tools.NewToolResult("policy child payload"))
	loop.releaseSessionTurnState(rootSession, root)
	loop.markTrackedSubagentResultOutputReady(root.turnID)

	select {
	case <-provider.done:
	case <-time.After(5 * time.Second):
		t.Fatal("late policy continuation did not finish")
	}
	select {
	case outbound := <-messageBus.OutboundChan():
		if outbound.Content != "late policy result integrated" ||
			outbound.AgentID != parent.ID || outbound.SessionKey != rootSession {
			t.Fatalf("late policy outbound = %#v", outbound)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("late policy continuation did not publish its final response")
	}

	calls := provider.snapshot()
	if len(calls) != 2 {
		t.Fatalf("late policy provider calls = %d, want 2", len(calls))
	}
	for index, call := range calls {
		if len(call.tools) != 1 || call.tools[0].Function.Name != allowedTool.Name() {
			t.Fatalf("call %d tool definitions = %#v, want only %q", index+1, call.tools, allowedTool.Name())
		}
		for _, message := range call.messages {
			if strings.Contains(message.Content, "P007_SKILL_CATALOG_CANARY") ||
				strings.Contains(message.Content, "P007_SKILL_BODY_CANARY") {
				t.Fatalf("call %d regained parent-disabled skill context: %#v", index+1, call.messages)
			}
		}
	}
	if len(calls[0].messages) == 0 || calls[0].messages[0].Role != "system" ||
		strings.TrimSpace(calls[0].messages[0].Content) != toolUseSystemPromptRule() {
		t.Fatalf("late continuation system cap was not preserved: %#v", calls[0].messages)
	}
	if !p007MessagesContain(
		calls[1].messages,
		"not allowed by the active turn profile",
	) {
		t.Fatalf("disallowed canary call was not rejected by preserved profile: %#v", calls[1].messages)
	}
	if got := disallowedCanary.executions.Load(); got != 0 {
		t.Fatalf("disallowed late-continuation canary executed %d time(s)", got)
	}
}

func TestTrackedSubagentNamedPumpDoesNotConsumeManualSteeringScope(t *testing.T) {
	provider := newTrackedSubagentRuntimeProvider("manual-scope-safe")
	fixture := newTrackedSubagentRuntimeFixture(t, provider)
	manualCanary := providers.Message{
		Role: "user", Content: "P007_MANUAL_STEERING_SCOPE_CANARY",
	}
	if err := fixture.loop.steering.push(manualCanary); err != nil {
		t.Fatalf("seed manual steering scope: %v", err)
	}

	fixture.publishPendingResult(t)
	call := waitForTrackedSubagentRuntimeCall(t, provider)
	if trackedSubagentRuntimeMessagesContain(call.messages, manualCanary.Content) {
		t.Fatalf("named result pump consumed manual steering canary: %#v", call.messages)
	}
	_ = waitForTrackedSubagentRuntimeRecord(
		t,
		fixture.loop,
		fixture.recordID,
		trackedSubagentResultClaimed,
		true,
	)

	fixture.loop.steering.mu.Lock()
	manualQueue := append(
		[]providers.Message(nil),
		fixture.loop.steering.queues[manualSteeringScope]...,
	)
	fixture.loop.steering.mu.Unlock()
	if len(manualQueue) != 1 || manualQueue[0].Content != manualCanary.Content {
		t.Fatalf("manual steering scope after named pump = %#v", manualQueue)
	}
}

func TestOrdinaryTurnTerminalReleaseDoesNotGrowTrackedResultIndexes(t *testing.T) {
	loop := &AgentLoop{}
	const ordinaryTurns = 1024
	for index := 0; index < ordinaryTurns; index++ {
		turn := &turnState{
			al:         loop,
			turnID:     fmt.Sprintf("ordinary-turn-%d", index),
			agentID:    "ordinary-agent",
			sessionKey: fmt.Sprintf("ordinary-session-%d", index),
			channel:    "test", chatID: "ordinary-chat",
		}
		loop.activeTurnStates.Store(turn.sessionKey, turn)
		status, committed := turn.commitClaimedTerminal(TurnEndStatusCompleted)
		if !committed || status != TurnEndStatusCompleted {
			t.Fatalf("ordinary turn %d terminal commit = (%q, %v)", index, status, committed)
		}
		loop.releaseSessionTurnState(turn.sessionKey, turn)
	}

	trackedTurnCount := 0
	loop.trackedSubagentResults.trackedTurns.Range(func(_, _ any) bool {
		trackedTurnCount++
		return true
	})
	loop.trackedSubagentResults.mu.Lock()
	mailboxSizes := []int{
		len(loop.trackedSubagentResults.records),
		len(loop.trackedSubagentResults.scopes),
		len(loop.trackedSubagentResults.released),
		len(loop.trackedSubagentResults.pendingBySource),
		len(loop.trackedSubagentResults.pendingByRoot),
		len(loop.trackedSubagentResults.rootsBySession),
		len(loop.trackedSubagentResults.outputHolds),
	}
	loop.trackedSubagentResults.mu.Unlock()
	if trackedTurnCount != 0 || !reflect.DeepEqual(mailboxSizes, []int{0, 0, 0, 0, 0, 0, 0}) {
		t.Fatalf(
			"ordinary terminal/release grew tracked state: turns=%d maps=%v",
			trackedTurnCount,
			mailboxSizes,
		)
	}
	activeCount := 0
	loop.activeTurnStates.Range(func(_, _ any) bool {
		activeCount++
		return true
	})
	loop.sessionTurnLocksMu.Lock()
	handoffLocks := len(loop.sessionTurnLocks)
	loop.sessionTurnLocksMu.Unlock()
	if activeCount != 0 || handoffLocks != 0 {
		t.Fatalf("ordinary release cleanup = active:%d locks:%d", activeCount, handoffLocks)
	}
}

func TestTrackedSubagentLateClaimCompactsPayloadAndPendingIndexes(t *testing.T) {
	provider := newTrackedSubagentRuntimeProvider("compaction-complete")
	releaseProvider := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseProvider) }) }
	provider.release = releaseProvider
	t.Cleanup(release)
	fixture := newTrackedSubagentRuntimeFixture(t, provider)
	fixture.route.RootProfile = config.EffectiveTurnProfile{
		Enabled:          true,
		HistoryMode:      config.TurnProfileModeDefault,
		SystemPromptMode: config.TurnProfileModeDefault,
		SkillsMode:       config.TurnProfileModeCustom,
		ToolsMode:        config.TurnProfileModeCustom,
		AllowedSkills:    []string{"retained-skill-payload"},
		AllowedTools:     []string{"retained-tool-payload"},
	}

	fixture.publishPendingResult(t)
	_ = waitForTrackedSubagentRuntimeCall(t, provider)

	fixture.loop.trackedSubagentResults.mu.Lock()
	record := fixture.loop.trackedSubagentResults.records[fixture.recordID]
	if record == nil {
		fixture.loop.trackedSubagentResults.mu.Unlock()
		t.Fatal("late-claim tombstone is missing")
	}
	recordSnapshot := *record
	_, sourceIndexed := fixture.loop.trackedSubagentResults.pendingBySource[fixture.route.SourceTurnID]
	_, rootIndexed := fixture.loop.trackedSubagentResults.pendingByRoot[fixture.route.RootTurnID]
	scope := trackedSubagentResultScope{
		AgentID: fixture.route.RootAgentID, SessionKey: fixture.route.RootSessionKey,
	}
	scopeState := fixture.loop.trackedSubagentResults.scopes[scope]
	queued, pending := 0, 0
	if scopeState != nil {
		queued = len(scopeState.queue)
		pending = scopeState.pending
	}
	fixture.loop.trackedSubagentResults.mu.Unlock()

	if recordSnapshot.state != trackedSubagentResultClaimed ||
		recordSnapshot.content != "" ||
		!reflect.DeepEqual(recordSnapshot.route, trackedSubagentResultRoute{}) ||
		recordSnapshot.currentScope != (trackedSubagentResultScope{}) ||
		recordSnapshot.rootEligible || recordSnapshot.preflightAttempts != 0 {
		t.Fatalf("claimed record retained bulky delivery payload: %#v", recordSnapshot)
	}
	if sourceIndexed || rootIndexed || queued != 0 || pending != 0 {
		t.Fatalf(
			"claimed record retained pending indexes: source=%v root=%v queued=%d pending=%d",
			sourceIndexed,
			rootIndexed,
			queued,
			pending,
		)
	}
	if recordSnapshot.completion.TaskID != fixture.recordID.TaskID ||
		recordSnapshot.completion.Status != "completed" {
		t.Fatalf("compaction removed dedupe tombstone identity: %#v", recordSnapshot.completion)
	}

	release()
	_ = waitForTrackedSubagentRuntimeRecord(
		t,
		fixture.loop,
		fixture.recordID,
		trackedSubagentResultClaimed,
		true,
	)
}
