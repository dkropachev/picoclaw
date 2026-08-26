package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// These tests exercise the mailbox's fail-closed edges directly. The larger
// P007 integration tests cover the happy path; keeping these cases local makes
// regressions in malformed routes, terminal races, and strict-session checks
// cheap to diagnose.

func TestTrackedSubagentMailboxWorkerAndOutputOwnerDefenses(t *testing.T) {
	if ctx := (*AgentLoop)(nil).trackedSubagentWorkerContext(); ctx == nil {
		t.Fatal("nil loop returned a nil worker context")
	}
	(*AgentLoop)(nil).cancelTrackedSubagentWorkers()

	loop := &AgentLoop{runtimeGateStopped: true}
	ctx := loop.trackedSubagentWorkerContext()
	select {
	case <-ctx.Done():
	default:
		t.Fatal("stopped runtime did not cancel its mailbox worker context")
	}
	loop.cancelTrackedSubagentWorkers()

	if got := withTrackedSubagentResultOutputOwner(nil, nil); got == nil {
		t.Fatal("nil owner did not normalize the context")
	}
	if got := trackedSubagentResultOutputOwnerFromContext(nil); got != nil {
		t.Fatalf("owner from nil context = %#v", got)
	}

	var nilOwner *trackedSubagentResultOutputOwner
	nilOwner.record(loop, "turn", "session")
	nilOwner.release(loop)
	owner := &trackedSubagentResultOutputOwner{}
	owner.record(nil, "turn", "session")
	owner.record(loop, "", "session")
	owner.record(loop, "turn", "")
	owner.release(nil)

	activeLoop := &AgentLoop{}
	owner.record(activeLoop, "turn-a", "shared-session")
	owner.record(activeLoop, "turn-a", "shared-session")
	owner.record(activeLoop, "turn-b", "shared-session")
	ownerCtx := withTrackedSubagentResultOutputOwner(context.Background(), owner)
	if trackedSubagentResultOutputOwnerFromContext(ownerCtx) != owner {
		t.Fatal("output owner was not retained in context")
	}
	owner.release(activeLoop)
	activeLoop.trackedSubagentResults.mu.Lock()
	holds := activeLoop.trackedSubagentResults.outputHolds["shared-session"]
	activeLoop.trackedSubagentResults.mu.Unlock()
	if holds != 0 {
		t.Fatalf("output holds after release = %d", holds)
	}
}

func TestTrackedSubagentMailboxRouteSnapshotRejectsMalformedTrees(t *testing.T) {
	if _, err := snapshotTrackedSubagentResultRoute(nil); err == nil {
		t.Fatal("nil source route was accepted")
	}

	validRoot := p007ActiveTurn("root", "agent", "session")
	validRoot.opts.Dispatch.InboundContext = &bus.InboundContext{
		Account: " account ", Channel: "ignored", ChatID: "ignored-chat",
	}
	validRoot.opts.Dispatch.SessionScope = &session.SessionScope{
		Version: 1, AgentID: "agent", Channel: "telegram",
	}
	if route, err := snapshotTrackedSubagentResultRoute(validRoot); err != nil ||
		route.RootInbound.Account != "account" {
		t.Fatalf("valid route snapshot = (%#v, %v)", route, err)
	}

	tests := []struct {
		name string
		turn *turnState
	}{
		{name: "incomplete identity", turn: &turnState{
			turnID: "turn", agentID: "agent", channel: "telegram", chatID: "chat",
		}},
		{name: "partial channel route", turn: &turnState{
			turnID: "turn", agentID: "agent", sessionKey: "session", channel: "telegram",
		}},
		{name: "missing root route", turn: &turnState{
			turnID: "turn", agentID: "agent", sessionKey: "session",
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := snapshotTrackedSubagentResultRoute(test.turn); err == nil {
				t.Fatalf("malformed turn was accepted: %#v", test.turn)
			}
		})
	}

	cycleA := p007ActiveTurn("cycle-a", "agent", "cycle-a-session")
	cycleB := p007ActiveTurn("cycle-b", "agent", "cycle-b-session")
	cycleA.parentTurnState, cycleA.parentTurnID = cycleB, cycleB.turnID
	cycleB.parentTurnState, cycleB.parentTurnID = cycleA, cycleA.turnID
	if _, err := snapshotTrackedSubagentResultRoute(cycleA); err == nil ||
		!strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle error = %v", err)
	}

	badEdgeRoot := p007ActiveTurn("edge-root", "agent", "edge-root-session")
	badEdgeChild := p007ActiveTurn("edge-child", "agent", "edge-child-session")
	badEdgeChild.parentTurnState, badEdgeChild.parentTurnID = badEdgeRoot, "wrong-parent"
	if _, err := snapshotTrackedSubagentResultRoute(badEdgeChild); err == nil ||
		!strings.Contains(err.Error(), "parent edge") {
		t.Fatalf("parent-edge error = %v", err)
	}

	deep := make([]*turnState, 129)
	for index := range deep {
		deep[index] = p007ActiveTurn(
			"deep-turn-"+strings.Repeat("x", index%3),
			"agent",
			"deep-session-"+strings.Repeat("y", index%3),
		)
		// Identity only needs to be non-empty; make each value unique below.
		deep[index].turnID += time.Duration(index).String()
		deep[index].sessionKey += time.Duration(index).String()
	}
	for index := 0; index < len(deep)-1; index++ {
		deep[index].parentTurnState = deep[index+1]
		deep[index].parentTurnID = deep[index+1].turnID
	}
	if _, err := snapshotTrackedSubagentResultRoute(deep[0]); err == nil ||
		!strings.Contains(err.Error(), "depth") {
		t.Fatalf("depth error = %v", err)
	}

	inbound := sanitizeTrackedSubagentInbound(
		&bus.InboundContext{Channel: " cli ", ChatID: " chat ", ChatType: ""},
		"", "", nil,
	)
	if inbound.Channel != "cli" || inbound.ChatID != "chat" {
		t.Fatalf("inbound route fallback = %#v", inbound)
	}
}

func TestTrackedSubagentMailboxAdmissionRejectsInvalidAndUnavailableResults(t *testing.T) {
	validRoute := p007TrackedRoute(
		"source-turn", "source-agent", "source-session",
		"root-turn", "root-agent", "root-session",
	)
	validCompletion := tools.SubagentCompletion{TaskID: "task", Status: "completed"}

	validation := []struct {
		name       string
		route      trackedSubagentResultRoute
		completion tools.SubagentCompletion
		result     *tools.ToolResult
		want       string
	}{
		{
			name:       "invalid identity",
			route:      trackedSubagentResultRoute{},
			completion: validCompletion,
			result:     tools.NewToolResult("ok"),
			want:       "invalid_parent_route",
		},
		{
			name:       "invalid target",
			route:      func() trackedSubagentResultRoute { r := validRoute; r.RootChatID = ""; return r }(),
			completion: validCompletion,
			result:     tools.NewToolResult("ok"),
			want:       "invalid_parent_route",
		},
		{
			name:       "missing task",
			route:      validRoute,
			completion: tools.SubagentCompletion{Status: "completed"},
			result:     tools.NewToolResult("ok"),
			want:       "missing_task_identity",
		},
		{
			name:       "invalid status",
			route:      validRoute,
			completion: tools.SubagentCompletion{TaskID: "task", Status: "running"},
			result:     tools.NewToolResult("ok"),
			want:       "invalid_task_status",
		},
		{name: "nil result", route: validRoute, completion: validCompletion, want: "nil_result"},
	}
	for _, test := range validation {
		t.Run(test.name, func(t *testing.T) {
			if got := validateTrackedSubagentResult(test.route, test.completion, test.result); got != test.want {
				t.Fatalf("validation reason = %q, want %q", got, test.want)
			}
		})
	}

	(*AgentLoop)(nil).acceptTrackedSubagentResult(validRoute, validCompletion, tools.NewToolResult("ok"))
	admission := []struct {
		name       string
		configure  func(*AgentLoop, *trackedSubagentResultRoute)
		result     *tools.ToolResult
		wantReason string
	}{
		{name: "empty result", result: tools.NewToolResult(" \n "), wantReason: "empty_result"},
		{name: "failed root", configure: func(loop *AgentLoop, route *trackedSubagentResultRoute) {
			loop.trackedSubagentResults.mu.Lock()
			loop.trackedSubagentResults.initLocked()
			loop.trackedSubagentResults.released[route.RootTurnID] = trackedSubagentTurnRelease{
				status: TurnEndStatusError,
			}
			loop.trackedSubagentResults.mu.Unlock()
		}, result: tools.NewToolResult("ok"), wantReason: "root_failed"},
		{name: "ephemeral root", configure: func(_ *AgentLoop, route *trackedSubagentResultRoute) {
			route.RootPersistent = false
		}, result: tools.NewToolResult("ok"), wantReason: "root_not_persistent"},
		{name: "non detachable released root", configure: func(loop *AgentLoop, route *trackedSubagentResultRoute) {
			route.RootLateContinuationAllowed = false
			loop.trackedSubagentResults.mu.Lock()
			loop.trackedSubagentResults.initLocked()
			loop.trackedSubagentResults.released[route.RootTurnID] = trackedSubagentTurnRelease{
				status: TurnEndStatusCompleted, released: true,
			}
			loop.trackedSubagentResults.mu.Unlock()
		}, result: tools.NewToolResult("ok"), wantReason: "root_continuation_policy"},
	}
	for _, test := range admission {
		t.Run(test.name, func(t *testing.T) {
			loop := &AgentLoop{}
			route := validRoute
			route.SourceTurnID += "-" + test.name
			if test.configure != nil {
				test.configure(loop, &route)
			}
			loop.acceptTrackedSubagentResult(route, validCompletion, test.result)
			id := trackedSubagentResultID{SourceTurnID: route.SourceTurnID, TaskID: "task"}
			loop.trackedSubagentResults.mu.Lock()
			record := loop.trackedSubagentResults.records[id]
			if record == nil || record.state != trackedSubagentResultOrphaned ||
				record.orphanReason != test.wantReason {
				loop.trackedSubagentResults.mu.Unlock()
				t.Fatalf("orphan record = %#v, want %q", record, test.wantReason)
			}
			loop.trackedSubagentResults.mu.Unlock()
		})
	}
}

func TestTrackedSubagentMailboxQueueAndClaimDefenses(t *testing.T) {
	mailbox := &trackedSubagentResultMailbox{}
	mailbox.mu.Lock()
	mailbox.initLocked()
	mailbox.indexPendingLocked(nil)
	mailbox.unindexPendingLocked(nil)
	missingScope := trackedSubagentResultScope{AgentID: "agent", SessionKey: "missing"}
	mailbox.removeFromScopeLocked(missingScope, trackedSubagentResultID{})
	state := mailbox.scopeLocked(missingScope)
	state.queue = []trackedSubagentResultID{{SourceTurnID: "other", TaskID: "other"}}
	mailbox.removeFromScopeLocked(missingScope, trackedSubagentResultID{SourceTurnID: "wanted", TaskID: "wanted"})
	state.queue = append(state.queue, trackedSubagentResultID{SourceTurnID: "absent", TaskID: "absent"})
	if _, ok := mailbox.claimForTurnLocked(missingScope, "turn", "channel", "chat", nil); ok {
		mailbox.mu.Unlock()
		t.Fatal("queue entry without a record was claimable")
	}
	mailbox.mu.Unlock()

	if activeTurnMatchesTrackedResult("not-a-turn", "turn", "agent") {
		t.Fatal("non-turn active owner matched")
	}
	if activeTurnMatchesTrackedResult((*turnState)(nil), "turn", "agent") {
		t.Fatal("nil active turn matched")
	}
	if trackedSubagentResultRouteMatchesTurn(
		trackedSubagentResultRoute{RootChannel: "channel", RootChatID: "chat"},
		"channel", "chat", nil,
	) != true {
		t.Fatal("nil frozen scope should permit exact channel/chat")
	}

	loop := &AgentLoop{}
	loop.watchTrackedSubagentResultRoute(trackedSubagentResultRoute{})
	if _, ok := loop.dequeueTrackedSubagentResult(nil); ok {
		t.Fatal("nil turn dequeued a result")
	}
	invalidTurn := &turnState{turnID: "turn"}
	if _, ok := loop.dequeueTrackedSubagentResult(invalidTurn); ok {
		t.Fatal("incomplete turn dequeued a result")
	}
	loop.activeTurnStates.Store("foreign", "not-a-turn")
	foreign := p007ActiveTurn("foreign-turn", "agent", "foreign")
	if _, ok := loop.dequeueTrackedSubagentResult(foreign); ok {
		t.Fatal("turn without exact active ownership dequeued a result")
	}
}

func TestTrackedSubagentMailboxReleasePoliciesAndTerminalDefenses(t *testing.T) {
	(*AgentLoop)(nil).handleTrackedSubagentResultTurnReleased(nil)
	loop := &AgentLoop{}
	loop.handleTrackedSubagentResultTurnReleased(nil)
	loop.handleTrackedSubagentResultTurnReleased(&turnState{})
	loop.handleTrackedSubagentResultTurnReleased(&turnState{turnID: "untracked"})
	loop.trackedSubagentResults.trackedTurns.Store("default-error", struct{}{})
	loop.handleTrackedSubagentResultTurnReleased(&turnState{turnID: "default-error"})
	loop.trackedSubagentResults.mu.Lock()
	if got := loop.trackedSubagentResults.released["default-error"].status; got != TurnEndStatusError {
		loop.trackedSubagentResults.mu.Unlock()
		t.Fatalf("default terminal status = %q", got)
	}
	loop.trackedSubagentResults.mu.Unlock()

	tests := []struct {
		name        string
		persistent  bool
		lateAllowed bool
		rootStatus  TurnEndStatus
		wantReason  string
		wantRehome  bool
	}{
		{
			name:        "ephemeral",
			persistent:  false,
			lateAllowed: true,
			rootStatus:  TurnEndStatusCompleted,
			wantReason:  "root_not_persistent",
		},
		{
			name:        "policy",
			persistent:  true,
			lateAllowed: false,
			rootStatus:  TurnEndStatusCompleted,
			wantReason:  "root_continuation_policy",
		},
		{
			name:        "failed",
			persistent:  true,
			lateAllowed: true,
			rootStatus:  TurnEndStatusError,
			wantReason:  "root_failed",
		},
		{name: "rehome", persistent: true, lateAllowed: true, rootStatus: TurnEndStatusCompleted, wantRehome: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			caseLoop := &AgentLoop{}
			route := p007TrackedRoute(
				"source-"+test.name, "worker", "source-session-"+test.name,
				"root-"+test.name, "root", "root-session-"+test.name,
			)
			route.RootPersistent = test.persistent
			route.RootLateContinuationAllowed = test.lateAllowed
			record := p007CoverageQueueRecord(caseLoop, route, trackedSubagentResultPendingPreferred)
			caseLoop.trackedSubagentResults.mu.Lock()
			caseLoop.trackedSubagentResults.released[route.RootTurnID] = trackedSubagentTurnRelease{
				status: test.rootStatus, released: true, outputReady: true,
			}
			// Keep the rehome pump inert while still exercising scheduling.
			caseLoop.trackedSubagentResults.outputHolds[route.RootSessionKey] = 1
			caseLoop.trackedSubagentResults.mu.Unlock()
			caseLoop.handleTrackedSubagentResultTurnReleased(&turnState{
				turnID: route.SourceTurnID, terminalStatus: TurnEndStatusCompleted,
			})
			caseLoop.trackedSubagentResults.mu.Lock()
			defer caseLoop.trackedSubagentResults.mu.Unlock()
			if test.wantRehome {
				rootScope := trackedSubagentResultScope{AgentID: route.RootAgentID, SessionKey: route.RootSessionKey}
				if record.state != trackedSubagentResultPendingRoot || !record.rootEligible ||
					record.currentScope != rootScope {
					t.Fatalf("re-homed record = %#v", record)
				}
				return
			}
			if record.state != trackedSubagentResultOrphaned || record.orphanReason != test.wantReason {
				t.Fatalf("released record = %#v, want orphan %q", record, test.wantReason)
			}
		})
	}

	rootPolicyLoop := &AgentLoop{}
	rootPolicyRoute := p007TrackedRoute("same-root", "root", "same-session", "same-root", "root", "same-session")
	rootPolicyRoute.RootPersistent = false
	rootPolicyRecord := p007CoverageQueueRecord(rootPolicyLoop, rootPolicyRoute, trackedSubagentResultPendingRoot)
	rootPolicyLoop.handleTrackedSubagentResultTurnReleased(&turnState{
		turnID: "same-root", terminalStatus: TurnEndStatusCompleted,
	})
	if rootPolicyRecord.orphanReason != "root_not_persistent" {
		t.Fatalf("completed ephemeral root reason = %q", rootPolicyRecord.orphanReason)
	}

	if got := (*AgentLoop)(nil).noteTrackedSubagentResultTurnTerminalLocked("turn", TurnEndStatusError); got != nil {
		t.Fatalf("nil-loop terminal note = %#v", got)
	}
	if got := loop.noteTrackedSubagentResultTurnTerminalLocked("", TurnEndStatusError); got != nil {
		t.Fatalf("empty terminal note = %#v", got)
	}
	if got := loop.noteTrackedSubagentResultTurnTerminalLocked("untracked-note", TurnEndStatusError); got != nil {
		t.Fatalf("untracked terminal note = %#v", got)
	}
	nilRecordLoop := &AgentLoop{}
	nilRecordLoop.trackedSubagentResults.trackedTurns.Store("root", struct{}{})
	nilRecordLoop.trackedSubagentResults.mu.Lock()
	nilRecordLoop.trackedSubagentResults.initLocked()
	nilRecordLoop.trackedSubagentResults.pendingByRoot["root"] = map[trackedSubagentResultID]struct{}{
		{TaskID: "missing"}: {},
	}
	nilRecordLoop.trackedSubagentResults.mu.Unlock()
	if got := nilRecordLoop.noteTrackedSubagentResultTurnTerminalSafely("root", TurnEndStatusError); len(got) != 0 {
		t.Fatalf("nil record produced orphans: %#v", got)
	}

	mailbox := &trackedSubagentResultMailbox{}
	if mailbox.orphanLocked(nil, "reason") {
		t.Fatal("nil record was orphaned")
	}
	if mailbox.orphanLocked(&trackedSubagentResultRecord{state: trackedSubagentResultClaimed}, "reason") {
		t.Fatal("claimed record was re-orphaned")
	}
	compactTrackedSubagentTerminalRecord(nil)
}

func TestTrackedSubagentMailboxPumpAndReservationDefenses(t *testing.T) {
	(*AgentLoop)(nil).maybeStartTrackedSubagentResultPump(trackedSubagentResultScope{})
	loop := &AgentLoop{}
	loop.maybeStartTrackedSubagentResultPump(trackedSubagentResultScope{})
	if got := recordRouteRootTurnID(nil); got != "" {
		t.Fatalf("nil record root turn = %q", got)
	}
	(*AgentLoop)(nil).markTrackedSubagentResultOutputReady("root")
	loop.markTrackedSubagentResultOutputReady("")
	loop.markTrackedSubagentResultOutputReady("unknown")
	loop.trackedSubagentResults.mu.Lock()
	loop.trackedSubagentResults.initLocked()
	loop.trackedSubagentResults.released["root"] = trackedSubagentTurnRelease{
		status: TurnEndStatusCompleted, released: true,
	}
	loop.trackedSubagentResults.pendingByRoot["root"] = map[trackedSubagentResultID]struct{}{{TaskID: "missing"}: {}}
	loop.trackedSubagentResults.mu.Unlock()
	loop.markTrackedSubagentResultOutputReady("root")

	if loop.releaseTrackedSubagentOutputTurn("session", nil) {
		t.Fatal("nil reservation was released")
	}
	reservation := &turnState{turnID: "reservation", sessionKey: "session"}
	if loop.releaseTrackedSubagentOutputTurn("session", reservation) {
		t.Fatal("unowned reservation was released")
	}
	loop.activeTurnStates.Store("session", reservation)
	if !loop.releaseTrackedSubagentOutputTurn("session", reservation) {
		t.Fatal("exact reservation was not released")
	}
	outer := &turnState{
		turnID: "outer", sessionKey: "session", terminalStatus: TurnEndStatusCompleted,
		opts: processOptions{turnReservation: reservation},
	}
	loop.trackedSubagentResults.trackedTurns.Store(outer.turnID, struct{}{})
	loop.activeTurnStates.Store("session", outer)
	if !loop.releaseTrackedSubagentOutputTurn("session", reservation) {
		t.Fatal("turn backed by exact reservation was not released")
	}

	resetTrackedSubagentMessageTool(nil, "session")
	resetTrackedSubagentMessageTool(&AgentInstance{}, "session")

	loop.steering = nil
	loop.maybeStartTrackedSubagentSteeringRescue(trackedSubagentResultRoute{})
	loop.steering = newSteeringQueue(SteeringAll)
	loop.maybeStartTrackedSubagentSteeringRescue(trackedSubagentResultRoute{})

	scope := trackedSubagentResultScope{AgentID: "root", SessionKey: "session"}
	for attempt := 1; attempt <= 3; attempt++ {
		delay, mode := loop.nextTrackedSubagentSteeringRescueRetry(scope)
		if delay <= 0 || mode != "delayed" {
			t.Fatalf("retry %d = (%s, %q)", attempt, delay, mode)
		}
	}
	if delay, mode := loop.nextTrackedSubagentSteeringRescueRetry(scope); delay != 0 || mode != "none" {
		t.Fatalf("exhausted retry = (%s, %q)", delay, mode)
	}
	_ = loop.steering.pushScope(scope.SessionKey, providers.Message{Role: "user", Content: "clear me"})
	loop.clearTrackedSubagentSteeringRescue(scope, scope.SessionKey)
	if loop.steering.lenScope(scope.SessionKey) != 0 {
		t.Fatal("steering rescue clear left messages queued")
	}

	if placeholder, messages, ok := (&AgentLoop{}).claimTrackedSubagentSteeringContinuation(
		p007TrackedRoute("source", "source", "source-session", "root", "root", "session"),
	); ok || placeholder != nil || messages != nil {
		t.Fatal("nil steering queue claimed a continuation")
	}
	activeLoop := &AgentLoop{steering: newSteeringQueue(SteeringAll)}
	route := p007TrackedRoute("source", "source", "source-session", "root", "root", "session")
	_ = activeLoop.steering.pushScope(route.RootSessionKey, providers.Message{Role: "user", Content: "queued"})
	activeLoop.activeTurnStates.Store(route.RootSessionKey, &turnState{})
	if _, _, ok := activeLoop.claimTrackedSubagentSteeringContinuation(route); ok {
		t.Fatal("active session admitted a steering rescue")
	}
	activeLoop.activeTurnStates.Delete(route.RootSessionKey)
	activeLoop.steering.dequeueScope(route.RootSessionKey)
	if _, _, ok := activeLoop.claimTrackedSubagentSteeringContinuation(route); ok {
		t.Fatal("empty steering queue admitted a rescue")
	}

	heldLoop := &AgentLoop{steering: newSteeringQueue(SteeringAll), runtimeGateStopped: true}
	heldRoute := p007TrackedRoute(
		"held-source", "source", "held-source-session",
		"held-root", "root", "held-root-session",
	)
	heldScope := trackedSubagentResultScope{
		AgentID: heldRoute.RootAgentID, SessionKey: heldRoute.RootSessionKey,
	}
	_ = heldLoop.steering.pushScope(
		heldRoute.RootSessionKey,
		providers.Message{Role: "user", Content: "wake after output hold"},
	)
	heldLoop.trackedSubagentResults.mu.Lock()
	heldLoop.trackedSubagentResults.initLocked()
	heldLoop.trackedSubagentResults.outputHolds[heldRoute.RootSessionKey] = 1
	heldLoop.trackedSubagentResults.scopeLocked(heldScope)
	heldLoop.trackedSubagentResults.mu.Unlock()
	heldLoop.maybeStartTrackedSubagentSteeringRescue(heldRoute)
	heldLoop.trackedSubagentResults.mu.Lock()
	delete(heldLoop.trackedSubagentResults.outputHolds, heldRoute.RootSessionKey)
	heldLoop.trackedSubagentResults.mu.Unlock()
	deadline := time.Now().Add(time.Second)
	for {
		heldLoop.trackedSubagentResults.mu.Lock()
		heldState := heldLoop.trackedSubagentResults.scopes[heldScope]
		settled := heldState != nil && !heldState.steeringWakeScheduled && !heldState.rescuingSteering
		heldLoop.trackedSubagentResults.mu.Unlock()
		if settled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("held steering wake did not settle")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type p007CoverageOpaqueStore struct{ session.SessionStore }

type p007CoverageSnapshotStore struct {
	session.SessionStore
	snapshot session.SessionSnapshot
	found    bool
	err      error
	metadata *session.SessionScope
}

type p007CoverageSequencedSnapshotStore struct {
	session.SessionStore
	responses []struct {
		snapshot session.SessionSnapshot
		found    bool
		err      error
	}
	calls int
}

type p007CoveragePanicSnapshotStore struct{ session.SessionStore }

func (*p007CoveragePanicSnapshotStore) ReadSessionSnapshot(
	context.Context,
	string,
) (session.SessionSnapshot, bool, error) {
	panic("coverage snapshot panic")
}

func (store *p007CoverageSequencedSnapshotStore) ReadSessionSnapshot(
	context.Context,
	string,
) (session.SessionSnapshot, bool, error) {
	index := store.calls
	store.calls++
	if index >= len(store.responses) {
		index = len(store.responses) - 1
	}
	response := store.responses[index]
	return response.snapshot, response.found, response.err
}

func (store *p007CoverageSnapshotStore) ReadSessionSnapshot(
	context.Context,
	string,
) (session.SessionSnapshot, bool, error) {
	return store.snapshot, store.found, store.err
}

func (*p007CoverageSnapshotStore) EnsureSessionMetadata(string, *session.SessionScope, []string) {}
func (*p007CoverageSnapshotStore) ResolveSessionKey(key string) string                           { return key }
func (store *p007CoverageSnapshotStore) GetSessionScope(string) *session.SessionScope {
	return session.CloneScope(store.metadata)
}

func TestTrackedSubagentMailboxStrictSessionValidationBranches(t *testing.T) {
	ctx := context.Background()
	expected := &session.SessionScope{Version: 1, AgentID: "root", Channel: "telegram"}

	tests := []struct {
		name  string
		agent *AgentInstance
		want  string
	}{
		{name: "nil agent", want: "unavailable"},
		{name: "nil sessions", agent: &AgentInstance{ID: "root"}, want: "unavailable"},
		{
			name:  "opaque store",
			agent: &AgentInstance{ID: "root", Sessions: &p007CoverageOpaqueStore{}},
			want:  "strict snapshots",
		},
		{
			name:  "read error",
			agent: &AgentInstance{ID: "root", Sessions: &p007CoverageSnapshotStore{err: errors.New("read failed")}},
			want:  "read failed",
		},
		{
			name:  "missing",
			agent: &AgentInstance{ID: "root", Sessions: &p007CoverageSnapshotStore{found: false}},
			want:  "canonically",
		},
		{
			name: "wrong key",
			agent: &AgentInstance{
				ID:       "root",
				Sessions: &p007CoverageSnapshotStore{found: true, snapshot: session.SessionSnapshot{Key: "alias"}},
			},
			want: "canonically",
		},
		{
			name: "wrong owner",
			agent: &AgentInstance{
				ID: "root",
				Sessions: &p007CoverageSnapshotStore{
					found:    true,
					snapshot: session.SessionSnapshot{Key: "session", Scope: &session.SessionScope{AgentID: "other"}},
				},
			},
			want: "owner",
		},
		{
			name: "review scope",
			agent: &AgentInstance{
				ID: "root",
				Sessions: &p007CoverageSnapshotStore{
					found: true,
					snapshot: session.SessionSnapshot{
						Key:   "session",
						Scope: &session.SessionScope{AgentID: "root", Channel: "review"},
					},
				},
			},
			want: "owner",
		},
		{
			name: "metadata wrong owner",
			agent: &AgentInstance{
				ID: "root",
				Sessions: &p007CoverageSnapshotStore{
					found:    true,
					snapshot: session.SessionSnapshot{Key: "session"},
					metadata: &session.SessionScope{AgentID: "other"},
				},
			},
			want: "owner",
		},
		{
			name: "scope changed",
			agent: &AgentInstance{
				ID: "root",
				Sessions: &p007CoverageSnapshotStore{
					found: true,
					snapshot: session.SessionSnapshot{
						Key:   "session",
						Scope: &session.SessionScope{AgentID: "root", Channel: "cli"},
					},
				},
			},
			want: "scope changed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateTrackedSubagentExistingSession(ctx, test.agent, "session", expected)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v, want substring %q", err, test.want)
			}
		})
	}
	metadataStore := &p007CoverageSnapshotStore{
		found: true, snapshot: session.SessionSnapshot{Key: "session"},
		metadata: session.CloneScope(expected),
	}
	if err := validateTrackedSubagentExistingSession(
		ctx, &AgentInstance{ID: "root", Sessions: metadataStore}, "session", expected,
	); err != nil {
		t.Fatalf("metadata fallback validation: %v", err)
	}
}

func TestTrackedSubagentMailboxPreflightAndPublishDefenses(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.MCP.Enabled = false
	route := p007TrackedRoute("source", "source", "source-session", "root", "root", "session")
	expected := &session.SessionScope{Version: 1, AgentID: "root", Channel: "telegram"}
	route.RootScope = expected

	loop := &AgentLoop{cfg: cfg}
	if _, _, reason, err := loop.preflightTrackedSubagentResultContinuation(
		context.Background(),
		route,
	); err == nil ||
		reason != "invalid_named_session" {
		t.Fatalf("nil-registry preflight = (%q, %v)", reason, err)
	}
	loop.registry = &AgentRegistry{agents: map[string]*AgentInstance{}}
	if _, _, reason, err := loop.preflightTrackedSubagentResultContinuation(
		context.Background(),
		route,
	); err == nil ||
		reason != "invalid_named_session" {
		t.Fatalf("missing-agent preflight = (%q, %v)", reason, err)
	}
	loop.registry.agents["root"] = &AgentInstance{ID: "root", Sessions: &p007CoverageOpaqueStore{}}
	if _, _, reason, err := loop.preflightTrackedSubagentResultContinuation(
		context.Background(),
		route,
	); err == nil ||
		reason != "invalid_named_session" {
		t.Fatalf("opaque-store preflight = (%q, %v)", reason, err)
	}

	store := &p007CoverageSnapshotStore{
		found:    true,
		snapshot: session.SessionSnapshot{Key: route.RootSessionKey, Scope: session.CloneScope(expected)},
	}
	loop.registry.agents["root"] = &AgentInstance{ID: "root", Sessions: store}
	if agent, snapshot, reason, err := loop.preflightTrackedSubagentResultContinuation(
		context.Background(),
		route,
	); err != nil || reason != "" || agent == nil ||
		snapshot.Key != route.RootSessionKey {
		t.Fatalf("valid preflight = (%#v, %#v, %q, %v)", agent, snapshot, reason, err)
	}
	store.err = errors.New("snapshot transient")
	if _, _, reason, err := loop.preflightTrackedSubagentResultContinuation(
		context.Background(),
		route,
	); err == nil ||
		reason != "session_snapshot_failed" {
		t.Fatalf("snapshot-error preflight = (%q, %v)", reason, err)
	}
	store.err = nil
	store.found = false
	if _, _, reason, err := loop.preflightTrackedSubagentResultContinuation(
		context.Background(),
		route,
	); err == nil ||
		reason != "invalid_named_session" {
		t.Fatalf("missing-snapshot preflight = (%q, %v)", reason, err)
	}
	store.found = true
	store.snapshot = session.SessionSnapshot{Key: route.RootSessionKey}
	store.metadata = &session.SessionScope{Version: 1, AgentID: "other", Channel: "telegram"}
	if _, _, reason, err := loop.preflightTrackedSubagentResultContinuation(
		context.Background(),
		route,
	); err == nil ||
		reason != "invalid_named_session" {
		t.Fatalf("metadata-owner preflight = (%q, %v)", reason, err)
	}

	if loop.publishTrackedSubagentResultResponse(context.Background(), nil, nil, route, "") {
		t.Fatal("empty response was published")
	}
	if loop.publishTrackedSubagentResultResponse(context.Background(), nil, nil, route, "response") {
		t.Fatal("response without a bus was published")
	}
	if trackedSubagentMessageToolSentTo(nil, "session", "channel", "chat") {
		t.Fatal("nil agent reported a message-tool send")
	}

	messageTool := tools.NewMessageTool()
	messageTool.SetSendCallback(
		func(context.Context, string, string, string, string, []bus.MediaPart) error { return nil },
	)
	toolCtx := tools.WithToolContext(context.Background(), route.RootChannel, route.RootChatID)
	toolCtx = tools.WithToolSessionContext(toolCtx, route.RootAgentID, route.RootSessionKey, route.RootScope)
	if result := messageTool.Execute(toolCtx, map[string]any{"content": "sent"}); result.IsError {
		t.Fatalf("message marker execute: %#v", result)
	}
	registry := tools.NewToolRegistry()
	registry.Register(messageTool)
	agent := &AgentInstance{ID: "root", Tools: registry}
	if !loop.publishTrackedSubagentResultResponse(context.Background(), agent, route.RootScope, route, "suppressed") {
		t.Fatal("matching message-tool send did not suppress duplicate final output")
	}

	id := trackedSubagentResultID{SourceTurnID: "source", TaskID: "task"}
	scope := trackedSubagentResultScope{AgentID: "root", SessionKey: "session"}
	if loop.retryTrackedSubagentResultPreflight(id, scope) {
		t.Fatal("missing result record was retried")
	}

	eventBus := runtimeevents.NewBus()
	loop.runtimeEvents = &supervisorPanicEventBus{
		Bus: eventBus, panicKind: runtimeevents.KindAgentFollowUpQueued,
		panicValue: "coverage event panic",
	}
	loop.emitTrackedSubagentEventSafely(
		runtimeevents.KindAgentFollowUpQueued,
		HookMeta{AgentID: "root"},
		FollowUpQueuedPayload{},
	)
	_ = eventBus.Close()
}

func TestTrackedSubagentMailboxPumpAndSteeringFailureBranches(t *testing.T) {
	route := p007TrackedRoute(
		"source", "source", "source-session",
		"root", "root", "session",
	)
	scope := trackedSubagentResultScope{AgentID: route.RootAgentID, SessionKey: route.RootSessionKey}
	id := trackedSubagentResultID{SourceTurnID: route.SourceTurnID, TaskID: "task"}

	stopped := &AgentLoop{runtimeGateStopped: true}
	stopped.runTrackedSubagentResultPump(scope, id, route)
	stopped.runTrackedSubagentSteeringRescue(scope, route)

	cfg := config.DefaultConfig()
	cfg.Tools.MCP.Enabled = false
	preflightReject := &AgentLoop{
		cfg: cfg, registry: &AgentRegistry{agents: map[string]*AgentInstance{}},
		steering: newSteeringQueue(SteeringAll),
	}
	preflightReject.runTrackedSubagentResultPump(scope, id, route)
	preflightReject.runTrackedSubagentSteeringRescue(scope, route)

	transientStore := &p007CoverageSnapshotStore{err: errors.New("transient snapshot")}
	transient := &AgentLoop{
		cfg: cfg,
		registry: &AgentRegistry{agents: map[string]*AgentInstance{
			"root": {ID: "root", Sessions: transientStore},
		}},
		steering: newSteeringQueue(SteeringAll),
	}
	transient.trackedSubagentResults.mu.Lock()
	transient.trackedSubagentResults.scopeLocked(scope).steeringRescueAttempts = 3
	transient.trackedSubagentResults.mu.Unlock()
	_ = transient.steering.pushScope(
		route.RootSessionKey,
		providers.Message{Role: "user", Content: "cleared after retries"},
	)
	transient.runTrackedSubagentSteeringRescue(scope, route)
	if transient.steering.lenScope(route.RootSessionKey) != 0 {
		t.Fatal("exhausted transient rescue retained steering")
	}

	validSnapshot := session.SessionSnapshot{Key: route.RootSessionKey}
	recheckStore := &p007CoverageSequencedSnapshotStore{responses: []struct {
		snapshot session.SessionSnapshot
		found    bool
		err      error
	}{
		{snapshot: validSnapshot, found: true},
		{err: errors.New("strict recheck failed")},
	}}
	recheck := &AgentLoop{
		cfg: cfg,
		registry: &AgentRegistry{agents: map[string]*AgentInstance{
			"root": {ID: "root", Sessions: recheckStore},
		}},
		steering: newSteeringQueue(SteeringAll),
	}
	recheck.trackedSubagentResults.mu.Lock()
	recheck.trackedSubagentResults.scopeLocked(scope).steeringRescueAttempts = 3
	recheck.trackedSubagentResults.mu.Unlock()
	_ = recheck.steering.pushScope(
		route.RootSessionKey,
		providers.Message{Role: "user", Content: "strict recheck"},
	)
	recheck.runTrackedSubagentSteeringRescue(scope, route)
	if recheckStore.calls != 2 || recheck.steering.lenScope(route.RootSessionKey) != 0 {
		t.Fatalf(
			"strict recheck calls/queue = %d/%d",
			recheckStore.calls,
			recheck.steering.lenScope(route.RootSessionKey),
		)
	}

	validStore := &p007CoverageSnapshotStore{
		found: true, snapshot: validSnapshot,
	}
	claimMiss := &AgentLoop{
		cfg: cfg,
		registry: &AgentRegistry{agents: map[string]*AgentInstance{
			"root": {ID: "root", Sessions: validStore},
		}},
		steering: newSteeringQueue(SteeringAll),
	}
	claimMiss.runTrackedSubagentResultPump(scope, id, route)
	claimMiss.runTrackedSubagentSteeringRescue(scope, route)

	claimLoop := &AgentLoop{}
	claimLoop.activeTurnStates.Store(route.RootSessionKey, &turnState{})
	if _, _, claimed := claimLoop.claimTrackedSubagentResultForContinuation(scope, id, route); claimed {
		t.Fatal("active root session admitted a result continuation")
	}
	claimLoop.activeTurnStates.Delete(route.RootSessionKey)
	if _, _, claimed := claimLoop.claimTrackedSubagentResultForContinuation(scope, id, route); claimed {
		t.Fatal("missing mailbox record admitted a result continuation")
	}

	panicLoop := &AgentLoop{
		cfg: cfg,
		registry: &AgentRegistry{agents: map[string]*AgentInstance{
			"root": {ID: "root", Sessions: &p007CoveragePanicSnapshotStore{}},
		}},
		steering: newSteeringQueue(SteeringAll),
	}
	if err := panicLoop.steering.pushScope(
		route.RootSessionKey,
		providers.Message{Role: "user", Content: "retry panic deterministically"},
	); err != nil {
		t.Fatalf("queue panic retry steering: %v", err)
	}
	panicLoop.runTrackedSubagentResultPump(scope, id, route)
	panicLoop.runTrackedSubagentSteeringRescue(scope, route)
	// The recovered steering panic schedules a bounded delayed retry. Observe the
	// retry attempt itself instead of sleeping for an assumed scheduler interval.
	deadline := time.Now().Add(2 * time.Second)
	for {
		panicLoop.trackedSubagentResults.mu.Lock()
		panicState := panicLoop.trackedSubagentResults.scopes[scope]
		retried := panicState != nil && panicState.steeringRescueAttempts >= 2
		panicLoop.trackedSubagentResults.mu.Unlock()
		if retried {
			panicLoop.clearSteeringMessagesForScope(route.RootSessionKey)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("recovered steering panic did not execute its delayed retry")
		}
		time.Sleep(time.Millisecond)
	}

	panicExhausted := &AgentLoop{
		cfg: cfg,
		registry: &AgentRegistry{agents: map[string]*AgentInstance{
			"root": {ID: "root", Sessions: &p007CoveragePanicSnapshotStore{}},
		}},
	}
	panicExhausted.trackedSubagentResults.mu.Lock()
	panicExhausted.trackedSubagentResults.scopeLocked(scope).steeringRescueAttempts = 3
	panicExhausted.trackedSubagentResults.mu.Unlock()
	panicExhausted.runTrackedSubagentSteeringRescue(scope, route)

	// A stale index is legal during defensive cleanup and must simply be skipped.
	nilRecordRelease := &AgentLoop{}
	nilRecordRelease.trackedSubagentResults.trackedTurns.Store("stale-turn", struct{}{})
	nilRecordRelease.trackedSubagentResults.mu.Lock()
	nilRecordRelease.trackedSubagentResults.initLocked()
	nilRecordRelease.trackedSubagentResults.pendingBySource["stale-turn"] = map[trackedSubagentResultID]struct{}{
		{SourceTurnID: "stale-turn", TaskID: "missing"}: {},
	}
	nilRecordRelease.trackedSubagentResults.mu.Unlock()
	nilRecordRelease.handleTrackedSubagentResultTurnReleased(&turnState{
		turnID: "stale-turn", terminalStatus: TurnEndStatusCompleted,
	})
}

func TestTrackedSubagentMailboxDeterministicFailureEdges(t *testing.T) {
	route := p007TrackedRoute(
		"failure-source", "source", "failure-source-session",
		"failure-root", "root", "failure-root-session",
	)

	innerBus := bus.NewMessageBus()
	failingBus := &agentTurnUXMessageBus{inner: innerBus}
	failingBus.failOutbound.Store(true)
	publishLoop := &AgentLoop{bus: failingBus}
	if publishLoop.publishTrackedSubagentResultResponse(
		context.Background(), nil, nil, route, "must fail",
	) {
		t.Fatal("failed outbound publish was reported as accepted")
	}
	innerBus.Close()

	hookCfg := config.DefaultConfig()
	hookCfg.Tools.MCP.Enabled = false
	hookCfg.Hooks.Enabled = true
	hookCfg.Hooks.Builtins = map[string]config.BuiltinHookConfig{
		"p007-unknown-hook": {Enabled: true},
	}
	hookManager := NewHookManager(nil)
	hookLoop := &AgentLoop{cfg: hookCfg, hooks: hookManager}
	if _, _, reason, err := hookLoop.preflightTrackedSubagentResultContinuation(
		context.Background(), route,
	); err == nil || reason != "runtime_setup_failed" {
		t.Fatalf("hook setup preflight = (%q, %v)", reason, err)
	}
	hookManager.Close()

	mcpLoop := &AgentLoop{cfg: config.DefaultConfig()}
	if _, _, reason, err := mcpLoop.preflightTrackedSubagentResultContinuation(
		context.Background(), route,
	); err == nil || reason != "runtime_setup_failed" {
		t.Fatalf("MCP setup preflight = (%q, %v)", reason, err)
	}

	errorProvider := newTrackedSubagentRuntimeProvider("steering-error")
	errorProvider.err = errors.New("deterministic steering provider failure")
	fixture := newTrackedSubagentRuntimeFixture(t, errorProvider)
	if err := fixture.loop.steering.pushScope(
		fixture.route.RootSessionKey,
		providers.Message{Role: "user", Content: "rescue provider failure"},
	); err != nil {
		t.Fatalf("queue steering failure fixture: %v", err)
	}
	fixture.loop.runTrackedSubagentSteeringRescue(
		trackedSubagentResultScope{
			AgentID: fixture.route.RootAgentID, SessionKey: fixture.route.RootSessionKey,
		},
		fixture.route,
	)
	if got := errorProvider.calls.Load(); got != 1 {
		t.Fatalf("steering failure provider calls = %d, want 1", got)
	}
}

func TestTrackedSubagentMailboxPreservesGenericSystemDeliveryBranches(t *testing.T) {
	ctx := context.Background()
	if target, err := (&AgentLoop{}).buildContinuationTarget(bus.InboundMessage{
		Context: bus.InboundContext{Channel: "system"}, Channel: "system",
	}); err != nil || target != nil {
		t.Fatalf("system continuation target = (%#v, %v)", target, err)
	}
	if _, err := (&AgentLoop{}).processSystemMessage(ctx, bus.InboundMessage{
		Context: bus.InboundContext{Channel: "telegram"}, Channel: "telegram",
	}); err == nil || !strings.Contains(err.Error(), "non-system") {
		t.Fatalf("non-system dispatch error = %v", err)
	}
	if response, err := (&AgentLoop{}).processSystemMessage(ctx, bus.InboundMessage{
		Context: bus.InboundContext{
			Channel: "system", ChatID: "no-origin-prefix", SenderID: "async:test",
		},
		Channel: "system", ChatID: "no-origin-prefix", SenderID: "async:test",
		Content: "Task completed.\n\nResult:\ninternal result",
	}); err != nil || response != "" {
		t.Fatalf("internal system result = (%q, %v)", response, err)
	}

	withoutDefault := &AgentLoop{registry: &AgentRegistry{agents: map[string]*AgentInstance{}}}
	if _, err := withoutDefault.processSystemMessage(ctx, bus.InboundMessage{
		Context: bus.InboundContext{
			Channel: "system", ChatID: "telegram:missing", SenderID: "async:test",
		},
		Channel: "system", ChatID: "telegram:missing", SenderID: "async:test",
		Content: "generic result",
	}); err == nil || !strings.Contains(err.Error(), "no default agent") {
		t.Fatalf("missing default system error = %v", err)
	}

	defaultProvider := &p007RecordingProvider{response: "generic system follow-up"}
	namedProvider := &p007RecordingProvider{response: "unused named response"}
	loop, messageBus := p007NamedAgentLoop(t, defaultProvider, namedProvider)
	response, err := loop.processSystemMessage(ctx, bus.InboundMessage{
		Context: bus.InboundContext{
			Channel: "system", ChatID: "telegram:generic-chat", SenderID: "async:generic",
		},
		Channel: "system", ChatID: "telegram:generic-chat", SenderID: "async:generic",
		Content: "generic async completion",
	})
	if err != nil || response != "generic system follow-up" {
		t.Fatalf("generic system follow-up = (%q, %v)", response, err)
	}
	select {
	case outbound := <-messageBus.OutboundChan():
		if outbound.Channel != "telegram" || outbound.ChatID != "generic-chat" ||
			outbound.Content != response {
			t.Fatalf("generic system outbound = %#v", outbound)
		}
	case <-time.After(time.Second):
		t.Fatal("generic system result did not publish its response")
	}
}

func p007CoverageQueueRecord(
	loop *AgentLoop,
	route trackedSubagentResultRoute,
	state trackedSubagentResultState,
) *trackedSubagentResultRecord {
	id := trackedSubagentResultID{SourceTurnID: route.SourceTurnID, TaskID: "coverage-task"}
	scope := trackedSubagentResultScope{AgentID: route.SourceAgentID, SessionKey: route.SourceSessionKey}
	if state == trackedSubagentResultPendingRoot {
		scope = trackedSubagentResultScope{AgentID: route.RootAgentID, SessionKey: route.RootSessionKey}
	}
	record := &trackedSubagentResultRecord{
		id: id, route: cloneTrackedSubagentResultRoute(route),
		completion: tools.SubagentCompletion{TaskID: id.TaskID, Status: "completed"},
		content:    "coverage result", state: state, currentScope: scope,
	}
	loop.trackedSubagentResults.trackedTurns.Store(route.SourceTurnID, struct{}{})
	loop.trackedSubagentResults.trackedTurns.Store(route.RootTurnID, struct{}{})
	loop.trackedSubagentResults.mu.Lock()
	loop.trackedSubagentResults.initLocked()
	loop.trackedSubagentResults.records[id] = record
	loop.trackedSubagentResults.enqueueLocked(scope, id)
	loop.trackedSubagentResults.indexPendingLocked(record)
	rootScope := trackedSubagentResultScope{AgentID: route.RootAgentID, SessionKey: route.RootSessionKey}
	loop.trackedSubagentResults.scopeLocked(rootScope).pending++
	loop.trackedSubagentResults.mu.Unlock()
	return record
}
