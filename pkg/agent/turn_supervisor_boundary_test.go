package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/sipeed/picoclaw/pkg/tools"
)

func TestTurnSupervisorBindingAndAttachmentBoundaries(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()

	var nilLoop *AgentLoop
	nilLoop.prepareTurnState(&turnState{})
	al.prepareTurnState(nil)

	existingMailbox := make(chan *tools.ToolResult, 1)
	existingSemaphore := make(chan struct{}, 2)
	existingFinished := make(chan struct{})
	state := &turnState{
		pendingResults: existingMailbox,
		concurrencySem: existingSemaphore,
		finishedChan:   existingFinished,
	}
	al.prepareTurnState(state)
	if state.al != al || state.pendingResults != existingMailbox ||
		state.concurrencySem != existingSemaphore || state.finishedChan != existingFinished {
		t.Fatalf("idempotent bindings changed existing state: %#v", state)
	}

	adhoc := al.newAdHocRootTurnState(nil)
	if adhoc.ctx == nil || adhoc.al != al || adhoc.pendingResults == nil || adhoc.concurrencySem == nil {
		t.Fatalf("nil-context ad-hoc root was not fully bound: %#v", adhoc)
	}
	if (*turnState)(nil).acceptsChildren() {
		t.Fatal("nil turn state accepted children")
	}

	parent := newSupervisorTurnState(agent, "attach-parent", "turn-attach-parent")
	validChild := func(sessionKey, turnID string) *turnState {
		return &turnState{
			sessionKey:      sessionKey,
			turnID:          turnID,
			parentTurnID:    parent.turnID,
			parentTurnState: parent,
		}
	}
	if nilLoop.attachChildTurn(parent, validChild("nil-loop", "turn-nil-loop")) ||
		al.attachChildTurn(nil, validChild("nil-parent", "turn-nil-parent")) ||
		al.attachChildTurn(parent, nil) {
		t.Fatal("invalid attachment receiver or operand was accepted")
	}
	for name, child := range map[string]*turnState{
		"missing session": validChild("", "turn-missing-session"),
		"missing turn":    validChild("missing-turn", ""),
		"wrong parent": {
			sessionKey:      "wrong-parent",
			turnID:          "turn-wrong-parent",
			parentTurnID:    parent.turnID,
			parentTurnState: &turnState{},
		},
		"wrong parent ID": {
			sessionKey:      "wrong-parent-id",
			turnID:          "turn-wrong-parent-id",
			parentTurnID:    "different-parent",
			parentTurnState: parent,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if al.attachChildTurn(parent, child) {
				t.Fatal("invalid child attachment was accepted")
			}
		})
	}

	duplicate := validChild("duplicate-child", "turn-duplicate-child")
	al.activeTurnStates.Store(duplicate.sessionKey, &turnState{})
	if al.attachChildTurn(parent, duplicate) {
		t.Fatal("duplicate active child key was accepted")
	}
	al.activeTurnStates.Delete(duplicate.sessionKey)

	claimedParent := newSupervisorTurnState(agent, "claimed-parent", "turn-claimed-parent")
	if !claimedParent.claimDetachedTerminal() {
		t.Fatal("failed to establish terminal claim")
	}
	claimedChild := &turnState{
		sessionKey:      "claimed-child",
		turnID:          "turn-claimed-child",
		parentTurnID:    claimedParent.turnID,
		parentTurnState: claimedParent,
	}
	if claimedParent.acceptsChildren() || al.attachChildTurn(claimedParent, claimedChild) {
		t.Fatal("terminal-claimed parent accepted a child")
	}
}

func TestTurnSupervisorRunAdmissionAndTerminalBoundaries(t *testing.T) {
	if (*turnState)(nil).admitRun() {
		t.Fatal("nil turn state admitted a run")
	}
	(*turnState)(nil).releaseRunAdmission()

	state := &turnState{}
	if !state.admitRun() || state.admitRun() {
		t.Fatal("run admission was not exactly once")
	}
	state.releaseRunAdmission()
	if !state.admitRun() {
		t.Fatal("unclaimed failed admission was not reusable")
	}
	status, committed := state.claimRunTerminal(TurnEndStatusCompleted)
	if !committed || status != TurnEndStatusCompleted {
		t.Fatalf("claimed run terminal = %q, %t", status, committed)
	}
	state.releaseRunAdmission()
	if state.admitRun() {
		t.Fatal("terminal run admission was reset")
	}
	if status, committed = state.claimRunTerminal(TurnEndStatusError); committed || status != TurnEndStatusCompleted {
		t.Fatalf("repeated run terminal = %q, %t", status, committed)
	}
	if status, committed = state.commitClaimedTerminal(
		TurnEndStatusError,
	); committed ||
		status != TurnEndStatusCompleted {
		t.Fatalf("repeated direct terminal commit = %q, %t", status, committed)
	}

	unadmitted := &turnState{}
	if status, committed = unadmitted.claimRunTerminal(TurnEndStatusError); committed || status != "" {
		t.Fatalf("unadmitted terminal claim = %q, %t", status, committed)
	}
	unadmitted.Finish(false)
	if !unadmitted.isFinished.Load() || unadmitted.admitRun() {
		t.Fatal("detached finish did not fence later run admission")
	}
	unadmitted.Finish(true)

	if got := (&turnState{terminalStatus: TurnEndStatusError}).terminalStatusSnapshot(
		TurnEndStatusCompleted,
	); got != TurnEndStatusError {
		t.Fatalf("stored terminal status snapshot = %q", got)
	}
	if got := (&turnState{hardAbort: true}).terminalStatusSnapshot(
		TurnEndStatusCompleted,
	); got != TurnEndStatusAborted {
		t.Fatalf("hard terminal status snapshot = %q", got)
	}
	if got := (&turnState{cancelRequested: true}).terminalStatusSnapshot(
		TurnEndStatusCompleted,
	); got != TurnEndStatusError {
		t.Fatalf("canceled terminal status snapshot = %q", got)
	}
	if got := (&turnState{}).terminalStatusSnapshot(TurnEndStatusCompleted); got != TurnEndStatusCompleted {
		t.Fatalf("fallback terminal status snapshot = %q", got)
	}
	if (&turnState{}).IsParentEnded() {
		t.Fatal("root turn reported a parent end")
	}
}

func TestTurnSupervisorCancellationTreeDefensiveBoundaries(t *testing.T) {
	if requestTurnTreeCancellation(nil, nil, true) {
		t.Fatal("nil cancellation root changed")
	}

	al, _, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	root := &turnState{turnID: "cancel-root", sessionKey: "cancel-root"}
	legacy := &turnState{
		turnID:          "turn-legacy-child",
		sessionKey:      "legacy-child",
		parentTurnID:    root.turnID,
		parentTurnState: root,
	}
	mismatch := &turnState{
		turnID:          "turn-mismatch-child",
		sessionKey:      "mismatch-child",
		parentTurnID:    "other-parent",
		parentTurnState: root,
	}
	root.childTurnIDs = []string{"missing-child", "wrong-type", mismatch.sessionKey, legacy.sessionKey}
	al.activeTurnStates.Store("wrong-type", "not-a-turn")
	al.activeTurnStates.Store(mismatch.sessionKey, mismatch)
	al.activeTurnStates.Store(legacy.sessionKey, legacy)
	defer al.activeTurnStates.Delete("wrong-type")
	defer al.activeTurnStates.Delete(mismatch.sessionKey)
	defer al.activeTurnStates.Delete(legacy.sessionKey)

	var providerCanceled, turnCanceled, ownedCanceled atomic.Int64
	legacy.providerCancel = func() { providerCanceled.Add(1) }
	legacy.turnCancel = func() { turnCanceled.Add(1) }
	legacy.cancelFunc = func() { ownedCanceled.Add(1) }
	// A corrupt back-edge proves visited-set termination without widening the
	// exact parent checks used for ordinary child traversal.
	legacy.childTurns = map[string]*turnState{root.sessionKey: root}
	root.parentTurnState = legacy
	root.parentTurnID = legacy.turnID

	if !requestTurnTreeCancellation(al, root, true) {
		t.Fatal("hard cancellation tree request was not accepted")
	}
	if providerCanceled.Load() != 1 || turnCanceled.Load() != 1 || ownedCanceled.Load() != 1 {
		t.Fatalf("legacy child cancellations = provider:%d turn:%d owned:%d",
			providerCanceled.Load(), turnCanceled.Load(), ownedCanceled.Load())
	}
	if requestTurnTreeCancellation(al, root, true) {
		t.Fatal("repeated hard cancellation tree request was accepted")
	}

	nonHardRoot := &turnState{turnID: "nonhard-root", sessionKey: "nonhard-root"}
	alreadyDispatched := &turnState{
		turnID:           "already-dispatched",
		sessionKey:       "already-dispatched",
		parentTurnID:     nonHardRoot.turnID,
		parentTurnState:  nonHardRoot,
		cancelDispatched: true,
	}
	nonHardRoot.childTurns = map[string]*turnState{alreadyDispatched.sessionKey: alreadyDispatched}
	requestTurnTreeCancellation(al, nonHardRoot, false)
	if !nonHardRoot.cancelRequested || !nonHardRoot.cancelDispatched {
		t.Fatal("non-hard root cancellation was not recorded")
	}
	requestTurnTreeCancellation(al, nonHardRoot, false)

	finished := &turnState{hardAbort: false}
	finished.isFinished.Store(true)
	if requestTurnTreeCancellation(nil, finished, true) {
		t.Fatal("finished root accepted hard cancellation")
	}
	claimed := &turnState{terminalClaimed: true}
	if requestTurnTreeCancellation(nil, claimed, true) {
		t.Fatal("terminal-claimed root accepted hard cancellation")
	}
}

func TestTurnSupervisorCancellationAndMailboxHelperBoundaries(t *testing.T) {
	runTurnTerminalStep(nil, nil)
	var cleanupPanic any
	runTurnTerminalStep(&cleanupPanic, func() { panic("boundary panic") })
	if cleanupPanic != "boundary panic" {
		t.Fatalf("terminal step panic = %#v", cleanupPanic)
	}

	if err := turnCancellationError(nil, nil, nil); err != nil {
		t.Fatalf("nil cancellation state error = %v", err)
	}
	if err := turnCancellationError(
		context.Background(),
		context.Background(),
		&turnState{hardAbort: true},
	); err != nil {
		t.Fatalf("hard abort cancellation error = %v", err)
	}
	turnCtx, cancelTurn := context.WithCancel(context.Background())
	cancelTurn()
	if err := turnCancellationError(context.Background(), turnCtx, &turnState{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("turn context cancellation error = %v", err)
	}
	inputCtx, cancelInput := context.WithCancel(context.Background())
	cancelInput()
	if err := turnCancellationError(inputCtx, context.Background(), &turnState{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("input context cancellation error = %v", err)
	}
	if err := turnCancellationError(
		context.Background(),
		context.Background(),
		&turnState{cancelRequested: true},
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("supervisor cancellation marker error = %v", err)
	}

	deliverSubTurnResult(nil, nil, "child", &tools.ToolResult{ForLLM: "ignored"})
	parent := &turnState{}
	deliverSubTurnResult(nil, parent, "nil-result", nil)
	if parent.pendingResults != nil {
		t.Fatal("nil result created a mailbox")
	}
	deliverSubTurnResult(nil, parent, "missing-mailbox", &tools.ToolResult{ForLLM: "ignored"})
	parent.pendingResults = make(chan *tools.ToolResult, 1)
	parent.pendingResults <- &tools.ToolResult{ForLLM: "full"}
	deliverSubTurnResult(nil, parent, "full-mailbox", &tools.ToolResult{ForLLM: "ignored"})
	if len(parent.pendingResults) != 1 {
		t.Fatal("full mailbox was mutated")
	}
}
