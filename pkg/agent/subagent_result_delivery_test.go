package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestTrackedSubagentResultCompositeIdentityDeduplicatesAndConflicts(t *testing.T) {
	loop := &AgentLoop{}
	firstTurn := p007ActiveTurn("source-turn-a", "worker", "source-session-a")
	secondTurn := p007ActiveTurn("source-turn-b", "worker", "source-session-b")
	loop.activeTurnStates.Store(firstTurn.sessionKey, firstTurn)
	loop.activeTurnStates.Store(secondTurn.sessionKey, secondTurn)
	t.Cleanup(func() {
		loop.activeTurnStates.Delete(firstTurn.sessionKey)
		loop.activeTurnStates.Delete(secondTurn.sessionKey)
	})

	firstRoute := p007TrackedRoute(
		firstTurn.turnID, firstTurn.agentID, firstTurn.sessionKey,
		"root-turn", "root", "root-session",
	)
	secondRoute := p007TrackedRoute(
		secondTurn.turnID, secondTurn.agentID, secondTurn.sessionKey,
		"root-turn", "root", "root-session",
	)
	completion := tools.SubagentCompletion{TaskID: "subagent-1", Status: "completed"}

	loop.acceptTrackedSubagentResult(firstRoute, completion, tools.NewToolResult("first result"))
	loop.acceptTrackedSubagentResult(firstRoute, completion, tools.NewToolResult("first result"))
	loop.acceptTrackedSubagentResult(firstRoute, completion, tools.NewToolResult("conflicting replay"))
	loop.acceptTrackedSubagentResult(secondRoute, completion, tools.NewToolResult("second result"))

	firstID := trackedSubagentResultID{SourceTurnID: firstTurn.turnID, TaskID: completion.TaskID}
	secondID := trackedSubagentResultID{SourceTurnID: secondTurn.turnID, TaskID: completion.TaskID}
	loop.trackedSubagentResults.mu.Lock()
	if got := len(loop.trackedSubagentResults.records); got != 2 {
		loop.trackedSubagentResults.mu.Unlock()
		t.Fatalf("record count = %d, want 2 composite identities", got)
	}
	firstRecord := loop.trackedSubagentResults.records[firstID]
	secondRecord := loop.trackedSubagentResults.records[secondID]
	if firstRecord == nil || secondRecord == nil {
		loop.trackedSubagentResults.mu.Unlock()
		t.Fatalf("composite records = (%#v, %#v), want both source-turn identities", firstRecord, secondRecord)
	}
	if !firstRecord.conflictSeen || firstRecord.content != "first result" {
		loop.trackedSubagentResults.mu.Unlock()
		t.Fatalf("first record after conflict = %#v, want original content and conflict tombstone", firstRecord)
	}
	loop.trackedSubagentResults.mu.Unlock()

	firstMessage, ok := loop.dequeueTrackedSubagentResult(firstTurn)
	if !ok || !strings.Contains(firstMessage.Content, "first result") {
		t.Fatalf("first claim = (%#v, %v), want original result", firstMessage, ok)
	}
	if _, claimedAgain := loop.dequeueTrackedSubagentResult(firstTurn); claimedAgain {
		t.Fatal("first composite identity was claimable more than once")
	}
	secondMessage, ok := loop.dequeueTrackedSubagentResult(secondTurn)
	if !ok || !strings.Contains(secondMessage.Content, "second result") {
		t.Fatalf("second claim = (%#v, %v), want independently accepted subagent-1", secondMessage, ok)
	}
}

func TestTrackedSubagentResultConcurrentIdenticalReplayClaimsOnce(t *testing.T) {
	loop := &AgentLoop{}
	turn := p007ActiveTurn("concurrent-source", "worker", "concurrent-session")
	loop.activeTurnStates.Store(turn.sessionKey, turn)
	defer loop.activeTurnStates.Delete(turn.sessionKey)
	route := p007TrackedRoute(
		turn.turnID,
		turn.agentID,
		turn.sessionKey,
		"concurrent-root",
		"root",
		"concurrent-root-session",
	)
	completion := tools.SubagentCompletion{TaskID: "subagent-1", Status: "completed"}
	start := make(chan struct{})
	const callers = 64
	var group sync.WaitGroup
	group.Add(callers)
	for range callers {
		go func() {
			defer group.Done()
			<-start
			loop.acceptTrackedSubagentResult(
				route,
				completion,
				tools.NewToolResult("identical concurrent result"),
			)
		}()
	}
	close(start)
	group.Wait()
	loop.trackedSubagentResults.mu.Lock()
	if got := len(loop.trackedSubagentResults.records); got != 1 {
		loop.trackedSubagentResults.mu.Unlock()
		t.Fatalf("concurrent replay records = %d, want 1", got)
	}
	loop.trackedSubagentResults.mu.Unlock()
	message, claimed := loop.dequeueTrackedSubagentResult(turn)
	if !claimed || !strings.Contains(message.Content, "identical concurrent result") {
		t.Fatalf("concurrent replay claim = (%#v, %v)", message, claimed)
	}
	if _, claimedAgain := loop.dequeueTrackedSubagentResult(turn); claimedAgain {
		t.Fatal("concurrent identical replay was claimable twice")
	}
}

func TestTrackedSubagentResultAdmissionCapacityAndDetachedSnapshot(t *testing.T) {
	loop := &AgentLoop{}
	rootScope := &session.SessionScope{
		Version: 1, AgentID: "root", Channel: "telegram",
		Dimensions: []string{"chat"}, Values: map[string]string{"chat": "root-chat"},
	}
	firstRoute := p007TrackedRoute(
		"source-turn-0", "worker", "source-session-0",
		"root-turn", "root", "root-session",
	)
	firstRoute.RootScope = rootScope
	firstResult := tools.NewToolResult("detached content")
	completion := tools.SubagentCompletion{TaskID: "subagent-0", Status: "completed"}
	loop.acceptTrackedSubagentResult(firstRoute, completion, firstResult)

	rootScope.Values["chat"] = "mutated-chat"
	rootScope.Dimensions[0] = "mutated-dimension"
	firstResult.ForLLM = "mutated content"

	for index := 1; index < maxTrackedSubagentResultsPerScope; index++ {
		route := p007TrackedRoute(
			fmt.Sprintf("source-turn-%d", index), "worker", fmt.Sprintf("source-session-%d", index),
			"root-turn", "root", "root-session",
		)
		loop.acceptTrackedSubagentResult(
			route,
			tools.SubagentCompletion{TaskID: fmt.Sprintf("subagent-%d", index), Status: "completed"},
			tools.NewToolResult(fmt.Sprintf("result-%d", index)),
		)
	}
	overflowRoute := p007TrackedRoute(
		"source-turn-overflow", "worker", "source-session-overflow",
		"root-turn", "root", "root-session",
	)
	loop.acceptTrackedSubagentResult(
		overflowRoute,
		tools.SubagentCompletion{TaskID: "subagent-overflow", Status: "completed"},
		tools.NewToolResult("overflow"),
	)

	firstID := trackedSubagentResultID{SourceTurnID: firstRoute.SourceTurnID, TaskID: completion.TaskID}
	overflowID := trackedSubagentResultID{SourceTurnID: overflowRoute.SourceTurnID, TaskID: "subagent-overflow"}
	rootMailboxScope := trackedSubagentResultScope{AgentID: "root", SessionKey: "root-session"}
	loop.trackedSubagentResults.mu.Lock()
	defer loop.trackedSubagentResults.mu.Unlock()
	firstRecord := loop.trackedSubagentResults.records[firstID]
	if firstRecord == nil {
		t.Fatal("detached record is missing")
	}
	if firstRecord.content != "detached content" || firstRecord.route.RootScope == rootScope ||
		firstRecord.route.RootScope.Values["chat"] != "root-chat" ||
		firstRecord.route.RootScope.Dimensions[0] != "chat" {
		t.Fatalf("retained record aliases caller data: %#v", firstRecord)
	}
	state := loop.trackedSubagentResults.scopes[rootMailboxScope]
	if state == nil || state.pending != maxTrackedSubagentResultsPerScope {
		t.Fatalf("root pending capacity = %#v, want %d", state, maxTrackedSubagentResultsPerScope)
	}
	overflow := loop.trackedSubagentResults.records[overflowID]
	if overflow == nil || overflow.state != trackedSubagentResultOrphaned ||
		overflow.orphanReason != "mailbox_full" {
		t.Fatalf("overflow record = %#v, want mailbox_full orphan tombstone", overflow)
	}
}

func TestTrackedSubagentResultContentBoundIsUTF8Safe(t *testing.T) {
	loop := &AgentLoop{}
	turn := p007ActiveTurn("bounded-turn", "worker", "bounded-session")
	loop.activeTurnStates.Store(turn.sessionKey, turn)
	t.Cleanup(func() { loop.activeTurnStates.Delete(turn.sessionKey) })
	route := p007TrackedRoute(
		turn.turnID, turn.agentID, turn.sessionKey,
		"bounded-root", "root", "bounded-root-session",
	)
	content := strings.Repeat("x", maxTrackedSubagentResultBytes-1) +
		"\xff" + strings.Repeat("y", maxTrackedSubagentResultBytes)
	loop.acceptTrackedSubagentResult(
		route,
		tools.SubagentCompletion{TaskID: "subagent-bounded", Status: "completed"},
		tools.NewToolResult(content),
	)
	id := trackedSubagentResultID{SourceTurnID: turn.turnID, TaskID: "subagent-bounded"}
	loop.trackedSubagentResults.mu.Lock()
	record := loop.trackedSubagentResults.records[id]
	if record == nil || len(record.content) > maxTrackedSubagentResultBytes ||
		!utf8.ValidString(record.content) {
		loop.trackedSubagentResults.mu.Unlock()
		t.Fatalf("bounded record = %#v", record)
	}
	loop.trackedSubagentResults.mu.Unlock()
}

func TestTrackedSubagentResultPreferredTurnClaimsExactlyOnce(t *testing.T) {
	loop := &AgentLoop{}
	turn := p007ActiveTurn("preferred-turn", "worker", "preferred-session")
	loop.activeTurnStates.Store(turn.sessionKey, turn)
	t.Cleanup(func() { loop.activeTurnStates.Delete(turn.sessionKey) })
	route := p007TrackedRoute(
		turn.turnID, turn.agentID, turn.sessionKey,
		"root-turn", "root", "root-session",
	)
	completion := tools.SubagentCompletion{TaskID: "subagent-7", Status: "failed"}
	loop.acceptTrackedSubagentResult(route, completion, tools.NewToolResult("terminal child output"))

	message, ok := loop.dequeueTrackedSubagentResult(turn)
	if !ok || !strings.Contains(message.Content, "task_id=subagent-7 status=failed") ||
		!strings.Contains(message.Content, "terminal child output") {
		t.Fatalf("preferred claim = (%#v, %v)", message, ok)
	}
	if _, ok := loop.dequeueTrackedSubagentResult(turn); ok {
		t.Fatal("claimed preferred result replayed")
	}

	id := trackedSubagentResultID{SourceTurnID: turn.turnID, TaskID: completion.TaskID}
	turn.terminalStatus = TurnEndStatusError
	loop.releaseSessionTurnState(turn.sessionKey, turn)
	loop.acceptTrackedSubagentResult(route, completion, tools.NewToolResult("terminal child output"))
	loop.trackedSubagentResults.mu.Lock()
	record := loop.trackedSubagentResults.records[id]
	if record == nil || record.state != trackedSubagentResultClaimed {
		loop.trackedSubagentResults.mu.Unlock()
		t.Fatalf("claimed identity after failure/replay = %#v, want irreversible claim", record)
	}
	loop.trackedSubagentResults.mu.Unlock()
}

func TestTrackedSubagentResultChildReleaseRehomesToActiveRoot(t *testing.T) {
	loop := &AgentLoop{}
	root, child := p007RootAndChildTurns("rehome")
	loop.activeTurnStates.Store(root.sessionKey, root)
	loop.activeTurnStates.Store(child.sessionKey, child)
	t.Cleanup(func() {
		loop.activeTurnStates.Delete(root.sessionKey)
		loop.activeTurnStates.Delete(child.sessionKey)
	})
	route, err := snapshotTrackedSubagentResultRoute(child)
	if err != nil {
		t.Fatalf("snapshot route: %v", err)
	}
	completion := tools.SubagentCompletion{TaskID: "subagent-2", Status: "completed"}
	loop.acceptTrackedSubagentResult(route, completion, tools.NewToolResult("nested result"))

	child.terminalStatus = TurnEndStatusCompleted
	loop.releaseSessionTurnState(child.sessionKey, child)
	if _, ok := loop.dequeueTrackedSubagentResult(child); ok {
		t.Fatal("released child still claimed its result")
	}
	message, ok := loop.dequeueTrackedSubagentResult(root)
	if !ok || !strings.Contains(message.Content, "nested result") {
		t.Fatalf("root claim after child release = (%#v, %v)", message, ok)
	}
	if _, ok := loop.dequeueTrackedSubagentResult(root); ok {
		t.Fatal("re-homed result was claimable twice")
	}
}

func TestTrackedSubagentResultCappedRootStillClaimsWhileActive(t *testing.T) {
	loop := &AgentLoop{}
	root, child := p007RootAndChildTurns("capped-active-root")
	root.opts.callAdmission = func() error { return nil }
	loop.activeTurnStates.Store(root.sessionKey, root)
	loop.activeTurnStates.Store(child.sessionKey, child)
	defer loop.activeTurnStates.Delete(root.sessionKey)
	route, err := snapshotTrackedSubagentResultRoute(child)
	if err != nil {
		t.Fatal(err)
	}
	if !route.RootPersistent || route.RootLateContinuationAllowed {
		t.Fatalf(
			"capped route persistence = persistent:%v late:%v",
			route.RootPersistent,
			route.RootLateContinuationAllowed,
		)
	}
	loop.acceptTrackedSubagentResult(
		route,
		tools.SubagentCompletion{TaskID: "subagent-capped", Status: "completed"},
		tools.NewToolResult("capped nested result"),
	)
	child.terminalStatus = TurnEndStatusCompleted
	loop.releaseSessionTurnState(child.sessionKey, child)
	message, claimed := loop.dequeueTrackedSubagentResult(root)
	if !claimed || !strings.Contains(message.Content, "capped nested result") {
		t.Fatalf("active capped root claim = (%#v, %v)", message, claimed)
	}
}

func TestTrackedSubagentResultNoHistoryRootStillClaimsWhileActive(t *testing.T) {
	loop := &AgentLoop{}
	root, child := p007RootAndChildTurns("no-history-active-root")
	root.opts.NoHistory = true
	loop.activeTurnStates.Store(root.sessionKey, root)
	loop.activeTurnStates.Store(child.sessionKey, child)
	defer loop.activeTurnStates.Delete(root.sessionKey)
	route, err := snapshotTrackedSubagentResultRoute(child)
	if err != nil {
		t.Fatal(err)
	}
	if route.RootPersistent || !route.RootLateContinuationAllowed {
		t.Fatalf("no-history route = persistent:%v late:%v", route.RootPersistent, route.RootLateContinuationAllowed)
	}
	loop.acceptTrackedSubagentResult(
		route,
		tools.SubagentCompletion{TaskID: "subagent-no-history", Status: "completed"},
		tools.NewToolResult("no-history nested result"),
	)
	child.terminalStatus = TurnEndStatusCompleted
	loop.releaseSessionTurnState(child.sessionKey, child)
	message, claimed := loop.dequeueTrackedSubagentResult(root)
	if !claimed || !strings.Contains(message.Content, "no-history nested result") {
		t.Fatalf("active no-history root claim = (%#v, %v)", message, claimed)
	}
}

func TestTrackedSubagentResultNestedPreferredSurvivesRootCompletion(t *testing.T) {
	loop := &AgentLoop{}
	root, child := p007RootAndChildTurns("root-completes-first")
	root.opts.NoHistory = true
	loop.activeTurnStates.Store(root.sessionKey, root)
	loop.activeTurnStates.Store(child.sessionKey, child)
	defer loop.activeTurnStates.Delete(child.sessionKey)
	route, err := snapshotTrackedSubagentResultRoute(child)
	if err != nil {
		t.Fatal(err)
	}
	loop.acceptTrackedSubagentResult(
		route,
		tools.SubagentCompletion{TaskID: "subagent-preferred", Status: "completed"},
		tools.NewToolResult("preferred survives root completion"),
	)
	root.terminalStatus = TurnEndStatusCompleted
	loop.releaseSessionTurnState(root.sessionKey, root)
	message, claimed := loop.dequeueTrackedSubagentResult(child)
	if !claimed || !strings.Contains(message.Content, "preferred survives root completion") {
		t.Fatalf("nested preferred claim = (%#v, %v)", message, claimed)
	}
}

func TestTrackedSubagentResultRootFailureSuppressesPendingChild(t *testing.T) {
	loop := &AgentLoop{}
	root, child := p007RootAndChildTurns("root-failure")
	loop.activeTurnStates.Store(root.sessionKey, root)
	loop.activeTurnStates.Store(child.sessionKey, child)
	t.Cleanup(func() {
		loop.activeTurnStates.Delete(root.sessionKey)
		loop.activeTurnStates.Delete(child.sessionKey)
	})
	route, err := snapshotTrackedSubagentResultRoute(child)
	if err != nil {
		t.Fatalf("snapshot route: %v", err)
	}
	completion := tools.SubagentCompletion{TaskID: "subagent-3", Status: "canceled"}
	loop.acceptTrackedSubagentResult(route, completion, tools.NewToolResult("must not surface"))

	root.terminalStatus = TurnEndStatusError
	loop.releaseSessionTurnState(root.sessionKey, root)
	if _, ok := loop.dequeueTrackedSubagentResult(child); ok {
		t.Fatal("child claimed a result after its persistent root failed")
	}
	id := trackedSubagentResultID{SourceTurnID: child.turnID, TaskID: completion.TaskID}
	loop.trackedSubagentResults.mu.Lock()
	record := loop.trackedSubagentResults.records[id]
	if record == nil || record.state != trackedSubagentResultOrphaned ||
		record.orphanReason != "root_failed" {
		loop.trackedSubagentResults.mu.Unlock()
		t.Fatalf("record after root failure = %#v, want root_failed orphan", record)
	}
	loop.trackedSubagentResults.mu.Unlock()
}

func TestTrackedSubagentResultRootFailureCommitSuppressesBeforeActiveRelease(t *testing.T) {
	loop := &AgentLoop{}
	root, child := p007RootAndChildTurns("root-terminal-commit")
	root.al = loop
	child.al = loop
	loop.activeTurnStates.Store(root.sessionKey, root)
	loop.activeTurnStates.Store(child.sessionKey, child)
	t.Cleanup(func() {
		loop.activeTurnStates.Delete(root.sessionKey)
		loop.activeTurnStates.Delete(child.sessionKey)
	})
	route, err := snapshotTrackedSubagentResultRoute(child)
	if err != nil {
		t.Fatalf("snapshot route: %v", err)
	}
	completion := tools.SubagentCompletion{TaskID: "subagent-terminal", Status: "canceled"}
	loop.acceptTrackedSubagentResult(route, completion, tools.NewToolResult("must not surface"))

	if status, committed := root.commitClaimedTerminal(TurnEndStatusError); !committed || status != TurnEndStatusError {
		t.Fatalf("root terminal commit = (%q, %v)", status, committed)
	}
	if _, ok := loop.dequeueTrackedSubagentResult(child); ok {
		t.Fatal("child claimed a result after root failure committed but before active release")
	}
	id := trackedSubagentResultID{SourceTurnID: child.turnID, TaskID: completion.TaskID}
	loop.trackedSubagentResults.mu.Lock()
	record := loop.trackedSubagentResults.records[id]
	if record == nil || record.state != trackedSubagentResultOrphaned ||
		record.orphanReason != "root_failed" {
		loop.trackedSubagentResults.mu.Unlock()
		t.Fatalf("record after root terminal commit = %#v, want root_failed orphan", record)
	}
	loop.trackedSubagentResults.mu.Unlock()
}

func TestTrackedSubagentResultRootEligibleRejectsDifferentChatOrScope(t *testing.T) {
	tests := []struct {
		name        string
		channel     string
		chatID      string
		scopeSuffix string
	}{
		{name: "different chat", channel: "telegram", chatID: "other-chat"},
		{name: "different scope", channel: "telegram", chatID: "root-chat", scopeSuffix: "-other"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			loop := &AgentLoop{}
			root, _ := p007RootAndChildTurns("route-guard-" + test.name)
			route, err := snapshotTrackedSubagentResultRoute(root)
			if err != nil {
				t.Fatalf("snapshot route: %v", err)
			}
			loop.watchTrackedSubagentResultRoute(route)
			loop.activeTurnStates.Store(root.sessionKey, root)
			completion := tools.SubagentCompletion{TaskID: "subagent-route", Status: "completed"}
			loop.acceptTrackedSubagentResult(route, completion, tools.NewToolResult("guarded result"))
			root.terminalStatus = TurnEndStatusCompleted
			loop.releaseSessionTurnState(root.sessionKey, root)

			replacementScope := session.CloneScope(route.RootScope)
			if replacementScope != nil && test.scopeSuffix != "" {
				replacementScope.Values["chat"] += test.scopeSuffix
			}
			replacement := p007ActiveTurn(
				"replacement-route-turn",
				route.RootAgentID,
				route.RootSessionKey,
			)
			replacement.channel = test.channel
			replacement.chatID = test.chatID
			replacement.opts.Dispatch.SessionScope = replacementScope
			loop.activeTurnStates.Store(route.RootSessionKey, replacement)
			loop.markTrackedSubagentResultOutputReady(route.RootTurnID)
			if message, claimed := loop.dequeueTrackedSubagentResult(replacement); claimed {
				t.Fatalf("different route claimed result: %#v", message)
			}

			replacement.channel = route.RootChannel
			replacement.chatID = route.RootChatID
			replacement.opts.Dispatch.SessionScope = session.CloneScope(route.RootScope)
			message, claimed := loop.dequeueTrackedSubagentResult(replacement)
			if !claimed || !strings.Contains(message.Content, "guarded result") {
				t.Fatalf("exact replacement claim = (%#v, %v)", message, claimed)
			}
			loop.activeTurnStates.Delete(route.RootSessionKey)
		})
	}
}

func TestTrackedSubagentResultLateFallbackRejectsNonDetachableRootPolicy(t *testing.T) {
	tests := []struct {
		name  string
		apply func(*processOptions)
	}{
		{name: "call admission", apply: func(opts *processOptions) {
			opts.callAdmission = func() error { return nil }
		}},
		{name: "usage observer", apply: func(opts *processOptions) {
			opts.usageObserver = func(workflows.AgentUsage) error { return nil }
		}},
		{name: "result usage", apply: func(opts *processOptions) {
			usage := []workflows.AgentUsage{}
			opts.resultUsage = &usage
		}},
		{name: "result model", apply: func(opts *processOptions) {
			model := ""
			opts.resultModelName = &model
		}},
		{name: "result actual model", apply: func(opts *processOptions) {
			model := ""
			opts.resultActualModel = &model
		}},
		{name: "result account", apply: func(opts *processOptions) {
			account := ""
			opts.resultAccountRef = &account
		}},
		{name: "forced skills", apply: func(opts *processOptions) {
			opts.ForcedSkills = []string{"request-only-skill"}
		}},
		{name: "system prompt override", apply: func(opts *processOptions) {
			opts.SystemPromptOverride = "private governing overlay"
		}},
		{name: "prompt cache override", apply: func(opts *processOptions) {
			opts.PromptCacheKey = "private-cache-partition"
		}},
		{name: "model override", apply: func(opts *processOptions) {
			opts.ModelNameOverride = "special-model"
		}},
		{name: "fallback override", apply: func(opts *processOptions) {
			opts.ModelFallbacksOverride = []string{}
		}},
		{name: "account override", apply: func(opts *processOptions) {
			opts.AccountRefOverride = "special-account"
		}},
		{name: "reasoning override", apply: func(opts *processOptions) {
			opts.ReasoningEffortOverride = "high"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, _ := p007RootAndChildTurns("non-detachable-" + test.name)
			test.apply(&root.opts)
			route, err := snapshotTrackedSubagentResultRoute(root)
			if err != nil {
				t.Fatalf("snapshot route: %v", err)
			}
			if !route.RootPersistent || route.RootLateContinuationAllowed {
				t.Fatalf("non-detachable route allowed late continuation: %#v", route)
			}
		})
	}
}

func TestTrackedSubagentResultLateContinuationUsesExactNamedAgentAndSession(t *testing.T) {
	defaultProvider := &p007RecordingProvider{response: "default provider response"}
	namedProvider := &p007RecordingProvider{response: "named continuation response"}
	loop, messageBus := p007NamedAgentLoop(t, defaultProvider, namedProvider)

	defaultAgent, ok := loop.registry.GetAgent("alpha")
	if !ok {
		t.Fatal("default alpha agent is missing")
	}
	namedAgent, ok := loop.registry.GetAgent("beta")
	if !ok {
		t.Fatal("named beta agent is missing")
	}
	defaultSession := "alpha-default-session"
	namedSession := "beta-named-parent-session"
	defaultAgent.Sessions.SetHistory(defaultSession, []providers.Message{{
		Role: "user", Content: "DEFAULT-HISTORY-CANARY",
	}})
	namedAgent.Sessions.SetHistory(namedSession, []providers.Message{{
		Role: "user", Content: "NAMED-HISTORY-CANARY",
	}})
	namedScope := &session.SessionScope{
		Version: 1, AgentID: "beta", Channel: "telegram",
		Dimensions: []string{"chat"}, Values: map[string]string{"chat": "named-chat"},
	}
	if metadata, ok := namedAgent.Sessions.(session.MetadataAwareSessionStore); ok {
		metadata.EnsureSessionMetadata(namedSession, namedScope, nil)
	}

	root := &turnState{
		turnID: "beta-finished-root", agentID: "beta", sessionKey: namedSession,
		channel: "telegram", chatID: "named-chat", terminalStatus: TurnEndStatusCompleted,
	}
	route := p007TrackedRoute(root.turnID, "beta", namedSession, root.turnID, "beta", namedSession)
	route.RootChannel = "telegram"
	route.RootChatID = "named-chat"
	route.RootScope = namedScope
	route.RootInbound = bus.InboundContext{
		Channel: "telegram", ChatID: "named-chat", ChatType: "direct", SenderID: "named-user",
	}
	loop.watchTrackedSubagentResultRoute(route)
	loop.activeTurnStates.Store(namedSession, root)
	loop.releaseSessionTurnState(namedSession, root)
	loop.markTrackedSubagentResultOutputReady(root.turnID)
	completion := tools.SubagentCompletion{TaskID: "subagent-9", Status: "completed"}
	result := tools.NewToolResult("LATE-RESULT-CANARY")
	loop.acceptTrackedSubagentResult(route, completion, result)

	var outbound bus.OutboundMessage
	select {
	case outbound = <-messageBus.OutboundChan():
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for named-session continuation output")
	}
	if outbound.AgentID != "beta" || outbound.SessionKey != namedSession ||
		outbound.Channel != "telegram" || outbound.ChatID != "named-chat" ||
		outbound.Content != "named continuation response" {
		t.Fatalf("late continuation outbound = %#v", outbound)
	}

	namedCalls := namedProvider.snapshotCalls()
	if len(namedCalls) != 1 {
		t.Fatalf("named provider calls = %d, want exactly 1", len(namedCalls))
	}
	if !p007MessagesContain(namedCalls[0], "NAMED-HISTORY-CANARY") ||
		!p007MessagesContain(namedCalls[0], "LATE-RESULT-CANARY") ||
		p007MessagesContain(namedCalls[0], "DEFAULT-HISTORY-CANARY") {
		t.Fatalf("named provider prompt crossed session boundary: %#v", namedCalls[0])
	}
	if got := len(defaultProvider.snapshotCalls()); got != 0 {
		t.Fatalf("default provider calls = %d, want 0", got)
	}
	if history := defaultAgent.Sessions.GetHistory(defaultSession); len(history) != 1 ||
		history[0].Content != "DEFAULT-HISTORY-CANARY" {
		t.Fatalf("default history mutated by named continuation: %#v", history)
	}
	if history := namedAgent.Sessions.GetHistory(namedSession); !p007MessagesContain(history, "NAMED-HISTORY-CANARY") ||
		!p007MessagesContain(history, "LATE-RESULT-CANARY") {
		t.Fatalf("named history missed exact continuation: %#v", history)
	}

	loop.acceptTrackedSubagentResult(route, completion, tools.NewToolResult("LATE-RESULT-CANARY"))
	time.Sleep(150 * time.Millisecond)
	if got := len(namedProvider.snapshotCalls()); got != 1 {
		t.Fatalf("duplicate replay caused %d named provider calls, want 1", got)
	}
	select {
	case duplicate := <-messageBus.OutboundChan():
		t.Fatalf("duplicate replay published another outbound: %#v", duplicate)
	default:
	}
}

func TestTrackedSubagentResultAcceptReleaseRaceNeverStrands(t *testing.T) {
	loop := &AgentLoop{}
	const iterations = 100
	for index := 0; index < iterations; index++ {
		root, child := p007RootAndChildTurns(fmt.Sprintf("race-%d", index))
		loop.activeTurnStates.Store(root.sessionKey, root)
		loop.activeTurnStates.Store(child.sessionKey, child)
		route, err := snapshotTrackedSubagentResultRoute(child)
		if err != nil {
			t.Fatalf("iteration %d snapshot route: %v", index, err)
		}
		completion := tools.SubagentCompletion{
			TaskID: fmt.Sprintf("subagent-%d", index), Status: "completed",
		}
		child.terminalStatus = TurnEndStatusCompleted

		start := make(chan struct{})
		var racers sync.WaitGroup
		racers.Add(2)
		go func() {
			defer racers.Done()
			<-start
			loop.acceptTrackedSubagentResult(route, completion, tools.NewToolResult("race result"))
		}()
		go func() {
			defer racers.Done()
			<-start
			loop.releaseSessionTurnState(child.sessionKey, child)
		}()
		close(start)
		racers.Wait()

		message, ok := loop.dequeueTrackedSubagentResult(root)
		if !ok || !strings.Contains(message.Content, completion.TaskID) {
			t.Fatalf("iteration %d root claim = (%#v, %v), result stranded", index, message, ok)
		}
		if _, ok := loop.dequeueTrackedSubagentResult(root); ok {
			t.Fatalf("iteration %d result claimed more than once", index)
		}
		loop.activeTurnStates.Delete(root.sessionKey)
		loop.activeTurnStates.Delete(child.sessionKey)
	}
}

func p007TrackedRoute(
	sourceTurnID, sourceAgentID, sourceSessionKey string,
	rootTurnID, rootAgentID, rootSessionKey string,
) trackedSubagentResultRoute {
	return trackedSubagentResultRoute{
		SourceTurnID: sourceTurnID, SourceAgentID: sourceAgentID,
		SourceSessionKey: sourceSessionKey,
		RootTurnID:       rootTurnID, RootAgentID: rootAgentID,
		RootSessionKey: rootSessionKey, RootChannel: "telegram", RootChatID: "root-chat",
		RootPersistent: true, RootLateContinuationAllowed: true, RootEnableSummary: true,
		RootInbound: bus.InboundContext{
			Channel: "telegram", ChatID: "root-chat", ChatType: "direct",
		},
	}
}

func p007ActiveTurn(turnID, agentID, sessionKey string) *turnState {
	return &turnState{
		turnID: turnID, agentID: agentID, sessionKey: sessionKey,
		channel: "telegram", chatID: "root-chat",
	}
}

func p007RootAndChildTurns(suffix string) (*turnState, *turnState) {
	rootSession := "root-session-" + suffix
	rootScope := &session.SessionScope{
		Version: 1, AgentID: "root", Channel: "telegram",
		Dimensions: []string{"chat"}, Values: map[string]string{"chat": "root-chat"},
	}
	rootInbound := &bus.InboundContext{
		Channel: "telegram", ChatID: "root-chat", ChatType: "direct", SenderID: "root-user",
	}
	root := &turnState{
		turnID: "root-turn-" + suffix, agentID: "root", sessionKey: rootSession,
		channel: "telegram", chatID: "root-chat",
		opts: processOptions{Dispatch: DispatchRequest{
			SessionKey: rootSession, SessionScope: rootScope, InboundContext: rootInbound,
		}},
	}
	child := &turnState{
		turnID: "child-turn-" + suffix, agentID: "worker",
		sessionKey:   "child-session-" + suffix,
		parentTurnID: root.turnID, parentTurnState: root,
		opts: processOptions{NoHistory: true, Dispatch: DispatchRequest{
			SessionKey: "child-session-" + suffix,
		}},
	}
	return root, child
}

type p007RecordingProvider struct {
	mu       sync.Mutex
	response string
	calls    [][]providers.Message
}

func (provider *p007RecordingProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	provider.mu.Lock()
	provider.calls = append(provider.calls, session.CloneMessages(messages))
	provider.mu.Unlock()
	return &providers.LLMResponse{Content: provider.response}, nil
}

func (provider *p007RecordingProvider) snapshotCalls() [][]providers.Message {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	result := make([][]providers.Message, len(provider.calls))
	for index := range provider.calls {
		result[index] = session.CloneMessages(provider.calls[index])
	}
	return result
}

func p007NamedAgentLoop(
	t *testing.T,
	defaultProvider, namedProvider providers.LLMProvider,
) (*AgentLoop, *bus.MessageBus) {
	t.Helper()
	workspace := t.TempDir()
	cfg := &config.Config{Agents: config.AgentsConfig{
		Defaults: config.AgentDefaults{
			Workspace: workspace, ModelName: "default-model",
			MaxTokens: 4096, MaxToolIterations: 4,
		},
		List: []config.AgentConfig{
			{ID: "alpha", Default: true, Workspace: workspace, Model: &config.AgentModelConfig{Primary: "model-alpha"}},
			{ID: "beta", Workspace: workspace, Model: &config.AgentModelConfig{Primary: "model-beta"}},
		},
	}}
	messageBus := bus.NewMessageBus()
	loop := newTestAgentLoopWithStrictModels(cfg, messageBus, defaultProvider)
	namedAgent, ok := loop.registry.GetAgent("beta")
	if !ok {
		t.Fatal("named beta agent is missing")
	}
	namedAgent.Provider = namedProvider
	for _, candidate := range namedAgent.Candidates {
		bindBootstrapProvider(namedAgent.CandidateProviders, candidate, namedProvider)
	}
	t.Cleanup(func() {
		loop.registry.Close()
		messageBus.Close()
	})
	return loop, messageBus
}

func p007MessagesContain(messages []providers.Message, value string) bool {
	for _, message := range messages {
		if strings.Contains(message.Content, value) {
			return true
		}
	}
	return false
}
