package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type supervisorGateProvider struct {
	started      chan struct{}
	release      chan struct{}
	ignoreCancel bool
	panicValue   any
	startOnce    sync.Once
}

func newSupervisorGateProvider(ignoreCancel bool) *supervisorGateProvider {
	return &supervisorGateProvider{
		started:      make(chan struct{}),
		release:      make(chan struct{}),
		ignoreCancel: ignoreCancel,
	}
}

func (p *supervisorGateProvider) Chat(
	ctx context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	p.startOnce.Do(func() { close(p.started) })
	if p.panicValue != nil {
		panic(p.panicValue)
	}
	if p.ignoreCancel {
		<-p.release
		return &providers.LLMResponse{Content: "stubborn success", FinishReason: "stop"}, nil
	}
	select {
	case <-p.release:
		return &providers.LLMResponse{Content: "released success", FinishReason: "stop"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type supervisorSessionStore struct {
	*ephemeralSessionStore

	mu             sync.Mutex
	saves          int
	setHistory     int
	setSummary     int
	restoreSaves   int
	restorePending bool
	onSave         func()
	onAddFull      func()
	panicRestore   any
}

func newSupervisorSessionStore(history []providers.Message, summary string) *supervisorSessionStore {
	return &supervisorSessionStore{
		ephemeralSessionStore: &ephemeralSessionStore{
			history: append([]providers.Message(nil), history...),
			summary: summary,
		},
	}
}

func (s *supervisorSessionStore) AddFullMessage(key string, message providers.Message) {
	s.ephemeralSessionStore.AddFullMessage(key, message)
	s.mu.Lock()
	onAddFull := s.onAddFull
	s.mu.Unlock()
	if onAddFull != nil {
		onAddFull()
	}
}

func (s *supervisorSessionStore) SetHistory(key string, history []providers.Message) {
	s.mu.Lock()
	s.setHistory++
	s.restorePending = true
	panicRestore := s.panicRestore
	s.mu.Unlock()
	if panicRestore != nil {
		panic(panicRestore)
	}
	s.ephemeralSessionStore.SetHistory(key, history)
}

func (s *supervisorSessionStore) SetSummary(key, summary string) {
	s.mu.Lock()
	s.setSummary++
	s.restorePending = true
	s.mu.Unlock()
	s.ephemeralSessionStore.SetSummary(key, summary)
}

func (s *supervisorSessionStore) Save(_ string) error {
	s.mu.Lock()
	s.saves++
	if s.restorePending {
		s.restoreSaves++
		s.restorePending = false
	}
	onSave := s.onSave
	s.mu.Unlock()
	if onSave != nil {
		onSave()
	}
	return nil
}

func (s *supervisorSessionStore) counts() (saves, histories, summaries, restoreSaves int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saves, s.setHistory, s.setSummary, s.restoreSaves
}

type supervisorGitWorkspaceManager struct {
	controllerGitWorkspaceManagerFake
	onRelease func(context.Context, gitworkspace.ReleaseRequest)
}

type supervisorPanicEventBus struct {
	runtimeevents.Bus
	panicKind  runtimeevents.Kind
	panicValue any
}

func (b *supervisorPanicEventBus) PublishNonBlocking(event runtimeevents.Event) runtimeevents.PublishResult {
	if event.Kind == b.panicKind {
		panic(b.panicValue)
	}
	return b.Bus.PublishNonBlocking(event)
}

type supervisorCountingProvider struct {
	calls atomic.Int64
}

func (p *supervisorCountingProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	p.calls.Add(1)
	return &providers.LLMResponse{Content: "unexpected", FinishReason: "stop"}, nil
}

type supervisorCancelTool struct{}

func (*supervisorCancelTool) Name() string { return "supervisor_cancel" }
func (*supervisorCancelTool) Description() string {
	return "Requests failure cancellation during a tool boundary"
}

type supervisorSemaphoreProvider struct {
	rootStarted  chan struct{}
	childStarted chan struct{}
	rootOnce     sync.Once
	childRelease chan struct{}
}

func newSupervisorSemaphoreProvider() *supervisorSemaphoreProvider {
	return &supervisorSemaphoreProvider{
		rootStarted:  make(chan struct{}),
		childStarted: make(chan struct{}, 8),
		childRelease: make(chan struct{}),
	}
}

func (p *supervisorSemaphoreProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	isChild := false
	for _, message := range messages {
		if strings.Contains(message.Content, "semaphore child") {
			isChild = true
			break
		}
	}
	if !isChild {
		p.rootOnce.Do(func() { close(p.rootStarted) })
		<-ctx.Done()
		return nil, ctx.Err()
	}
	p.childStarted <- struct{}{}
	select {
	case <-p.childRelease:
		return &providers.LLMResponse{Content: "child complete", FinishReason: "stop"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (*supervisorCancelTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

func (*supervisorCancelTool) Execute(ctx context.Context, _ map[string]any) *tools.ToolResult {
	ts := turnStateFromContext(ctx)
	requestTurnTreeCancellation(ts.al, ts, false)
	return tools.UserResult("tool finished after cancellation")
}

func (m *supervisorGitWorkspaceManager) ReleaseSession(
	ctx context.Context,
	req gitworkspace.ReleaseRequest,
) ([]gitworkspace.WorkspaceInfo, error) {
	if m.onRelease != nil {
		m.onRelease(ctx, req)
	}
	return nil, nil
}

func newSupervisorTurnState(
	agent *AgentInstance,
	sessionKey, turnID string,
) *turnState {
	opts := normalizeProcessOptions(makeTestProcessOpts(sessionKey))
	return newTurnState(agent, opts, turnEventScope{
		turnID:  turnID,
		context: newTurnContext(nil, nil, nil),
	})
}

func supervisorAgentWithProvider(
	base *AgentInstance,
	provider providers.LLMProvider,
) *AgentInstance {
	copyAgent := *base
	copyAgent.Provider = provider
	copyAgent.Sessions = newEphemeralSession(nil)
	copyAgent.Candidates = append([]providers.FallbackCandidate(nil), base.Candidates...)
	copyAgent.CandidateProviders = make(map[string]providers.LLMProvider, len(copyAgent.Candidates))
	for _, candidate := range copyAgent.Candidates {
		bindBootstrapProvider(copyAgent.CandidateProviders, candidate, provider)
	}
	return &copyAgent
}

func waitForTurnEndStatus(
	t *testing.T,
	events <-chan runtimeevents.Event,
	turnID string,
) TurnEndStatus {
	t.Helper()
	event := waitForRuntimeEvent(t, events, 2*time.Second, func(event runtimeevents.Event) bool {
		return event.Kind == runtimeevents.KindAgentTurnEnd && event.Scope.TurnID == turnID
	})
	payload, ok := event.Payload.(TurnEndPayload)
	if !ok {
		t.Fatalf("turn.end payload = %T", event.Payload)
	}
	return payload.Status
}

func runTurnRecovering(
	al *AgentLoop,
	ctx context.Context,
	ts *turnState,
) (result turnResult, err error, recovered any) {
	defer func() { recovered = recover() }()
	result, err = al.runTurn(ctx, ts, NewPipeline(al))
	return result, err, nil
}

func TestRunTurnSupervisorHardAbortOwnsRollbackAndCleanup(t *testing.T) {
	provider := newSupervisorGateProvider(false)
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	al.cfg.Agents.Defaults.MaxParallelTurns = 9
	al.cfg.Agents.Defaults.SubTurn.MaxConcurrent = 2

	initial := []providers.Message{{Role: "user", Content: "stable"}}
	store := newSupervisorSessionStore(initial, "stable summary")
	agent.Sessions = store
	ts := newSupervisorTurnState(agent, "supervisor-hard", "turn-supervisor-hard")

	var ownedCancels atomic.Int64
	ts.cancelFunc = func() { ownedCancels.Add(1) }
	var releaseCalled, releaseLive, releaseBounded, releaseSawOwner, releaseSawRestore atomic.Bool
	releaseEntered := make(chan struct{})
	releaseGate := make(chan struct{})
	al.gitWorkspaces = &supervisorGitWorkspaceManager{onRelease: func(
		ctx context.Context,
		req gitworkspace.ReleaseRequest,
	) {
		releaseCalled.Store(true)
		releaseLive.Store(ctx.Err() == nil)
		deadline, ok := ctx.Deadline()
		releaseBounded.Store(ok && time.Until(deadline) > 0 && time.Until(deadline) <= turnCleanupTimeout)
		releaseSawOwner.Store(al.getActiveTurnState(req.SessionKey) == ts)
		saves, histories, summaries, restoreSaves := store.counts()
		releaseSawRestore.Store(saves == 1 && histories == 1 && summaries == 1 && restoreSaves == 1)
		close(releaseEntered)
		<-releaseGate
		select {
		case <-ts.Finished():
			releaseLive.Store(false)
		default:
		}
	}}

	events, closeEvents := subscribeRuntimeEventsForTest(
		t,
		al,
		16,
		runtimeevents.KindAgentTurnEnd,
	)
	defer closeEvents()

	type outcome struct {
		result turnResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := al.runTurn(context.Background(), ts, NewPipeline(al))
		done <- outcome{result: result, err: err}
	}()
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not start")
	}

	if ts.al != al || ts.pendingResults == nil || cap(ts.concurrencySem) != 2 {
		t.Fatalf(
			"supervisor bindings = al:%p mailbox:%v concurrency:%d",
			ts.al,
			ts.pendingResults != nil,
			cap(ts.concurrencySem),
		)
	}
	if err := al.HardAbort(ts.sessionKey); err != nil {
		t.Fatalf("HardAbort() error = %v", err)
	}
	select {
	case <-releaseEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("Git release was not entered")
	}
	select {
	case <-ts.Finished():
		t.Fatal("Finished closed before Git and owned-resource cleanup")
	default:
	}
	if al.getActiveTurnState(ts.sessionKey) != ts {
		t.Fatal("exact active owner cleared before Git release")
	}
	if _, histories, summaries, restoreSaves := store.counts(); histories != 1 || summaries != 1 || restoreSaves != 1 {
		t.Fatalf("restore incomplete before Git release = %d/%d/%d", histories, summaries, restoreSaves)
	}
	select {
	case event := <-events:
		t.Fatalf("turn event arrived before Git release completed: %#v", event)
	default:
	}
	close(releaseGate)

	var got outcome
	select {
	case got = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("hard-aborted turn did not unwind")
	}
	if got.err != nil || got.result.status != TurnEndStatusAborted {
		t.Fatalf("runTurn() = %#v, %v", got.result, got.err)
	}
	if status := waitForTurnEndStatus(t, events, ts.turnID); status != TurnEndStatusAborted {
		t.Fatalf("turn.end status = %q", status)
	}
	if !releaseCalled.Load() {
		t.Fatal("turn.end was observed before Git release")
	}
	if al.getActiveTurnState(ts.sessionKey) != nil {
		t.Fatal("hard-aborted active owner leaked")
	}
	select {
	case <-ts.Finished():
	default:
		t.Fatal("Finished was not closed")
	}
	if !releaseCalled.Load() || !releaseLive.Load() || !releaseBounded.Load() ||
		!releaseSawOwner.Load() || !releaseSawRestore.Load() {
		t.Fatalf(
			"Git release probe = called:%t live:%t bounded:%t owner:%t restored:%t",
			releaseCalled.Load(),
			releaseLive.Load(),
			releaseBounded.Load(),
			releaseSawOwner.Load(),
			releaseSawRestore.Load(),
		)
	}
	if ownedCancels.Load() != 1 {
		t.Fatalf("owned cancellation count = %d, want 1", ownedCancels.Load())
	}
	if ts.requestHardAbort() {
		t.Fatal("repeated hard abort was accepted")
	}
	if ownedCancels.Load() != 1 {
		t.Fatalf("repeated hard abort invoked cancellation: %d", ownedCancels.Load())
	}

	if history := store.GetHistory(ts.sessionKey); !reflect.DeepEqual(history, initial) {
		t.Fatalf("restored history = %#v, want %#v", history, initial)
	}
	if summary := store.GetSummary(ts.sessionKey); summary != "stable summary" {
		t.Fatalf("restored summary = %q", summary)
	}
	if saves, histories, summaries, restoreSaves := store.counts(); saves != 1 || histories != 1 || summaries != 1 ||
		restoreSaves != 1 {
		t.Fatalf("restore counts = saves:%d history:%d summary:%d restore_saves:%d",
			saves, histories, summaries, restoreSaves)
	}

	// Mailbox remains open after terminal commit; result delivery rejects via
	// state under parent.mu instead of relying on channel closure.
	select {
	case ts.pendingResults <- &tools.ToolResult{ForLLM: "still open"}:
	default:
		t.Fatal("terminal mailbox unexpectedly unavailable")
	}
}

func TestHardAbortSupervisesRunningRootChildAndGrandchild(t *testing.T) {
	rootProvider := newSupervisorGateProvider(false)
	al, rootAgent, cleanup := newTurnCoordTestLoop(t, rootProvider)
	defer cleanup()
	childProvider := newSupervisorGateProvider(false)
	grandchildProvider := newSupervisorGateProvider(false)
	childAgent := supervisorAgentWithProvider(rootAgent, childProvider)
	grandchildAgent := supervisorAgentWithProvider(rootAgent, grandchildProvider)

	root := newSupervisorTurnState(rootAgent, "live-tree-root", "turn-live-tree-root")
	child := newSupervisorTurnState(childAgent, "live-tree-child", "turn-live-tree-child")
	child.parentTurnState = root
	child.parentTurnID = root.turnID
	child.depth = 1
	childInputCtx, childInputCancel := context.WithCancel(context.Background())
	child.ctx, child.cancelFunc = childInputCtx, childInputCancel
	grandchild := newSupervisorTurnState(grandchildAgent, "live-tree-grandchild", "turn-live-tree-grandchild")
	grandchild.parentTurnState = child
	grandchild.parentTurnID = child.turnID
	grandchild.depth = 2
	grandchild.critical = true
	grandchildInputCtx, grandchildInputCancel := context.WithCancel(context.Background())
	grandchild.ctx, grandchild.cancelFunc = grandchildInputCtx, grandchildInputCancel

	events, closeEvents := subscribeRuntimeEventsForTest(t, al, 32, runtimeevents.KindAgentTurnEnd)
	defer closeEvents()
	type outcome struct {
		result turnResult
		err    error
	}
	rootDone := make(chan outcome, 1)
	go func() {
		result, err := al.runTurn(context.Background(), root, NewPipeline(al))
		rootDone <- outcome{result: result, err: err}
	}()
	<-rootProvider.started

	al.prepareTurnState(child)
	if !al.attachChildTurn(root, child) {
		t.Fatal("attach live child")
	}
	childDone := make(chan outcome, 1)
	go func() {
		result, err := al.runTurn(childInputCtx, child, NewPipeline(al))
		childDone <- outcome{result: result, err: err}
	}()
	<-childProvider.started

	al.prepareTurnState(grandchild)
	if !al.attachChildTurn(child, grandchild) {
		t.Fatal("attach live grandchild")
	}
	grandchildDone := make(chan outcome, 1)
	go func() {
		result, err := al.runTurn(grandchildInputCtx, grandchild, NewPipeline(al))
		grandchildDone <- outcome{result: result, err: err}
	}()
	<-grandchildProvider.started

	if err := al.HardAbort(root.sessionKey); err != nil {
		t.Fatal(err)
	}
	for name, done := range map[string]<-chan outcome{
		"root": rootDone, "child": childDone, "grandchild": grandchildDone,
	} {
		select {
		case got := <-done:
			if got.err != nil || got.result.status != TurnEndStatusAborted {
				t.Fatalf("%s outcome = %#v, %v", name, got.result, got.err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s did not stop", name)
		}
	}
	for _, state := range []*turnState{root, child, grandchild} {
		if !state.hardAbortRequested() || !state.isFinished.Load() || al.getActiveTurnState(state.sessionKey) != nil {
			t.Fatalf(
				"state %s = hard:%t finished:%t active:%v",
				state.sessionKey,
				state.hardAbortRequested(),
				state.isFinished.Load(),
				al.getActiveTurnState(state.sessionKey),
			)
		}
	}

	statuses := make(map[string]TurnEndStatus, 3)
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for len(statuses) < 3 {
		select {
		case event := <-events:
			payload, ok := event.Payload.(TurnEndPayload)
			if ok {
				statuses[event.Scope.TurnID] = payload.Status
			}
		case <-deadline.C:
			t.Fatalf("turn.end statuses = %#v", statuses)
		}
	}
	for _, state := range []*turnState{root, child, grandchild} {
		if statuses[state.turnID] != TurnEndStatusAborted {
			t.Fatalf("turn %s status = %q", state.turnID, statuses[state.turnID])
		}
	}
}

func TestRealRootSubTurnSemaphoreSaturatesAndReleasesOnAbort(t *testing.T) {
	provider := newSupervisorSemaphoreProvider()
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	al.cfg.Agents.Defaults.MaxParallelTurns = 7
	al.cfg.Agents.Defaults.SubTurn.MaxConcurrent = 2
	al.cfg.Agents.Defaults.SubTurn.ConcurrencyTimeoutSec = 2
	root := newSupervisorTurnState(agent, "semaphore-root", "turn-semaphore-root")
	type turnOutcome struct {
		result turnResult
		err    error
	}
	rootDone := make(chan turnOutcome, 1)
	go func() {
		result, err := al.runTurn(context.Background(), root, NewPipeline(al))
		rootDone <- turnOutcome{result: result, err: err}
	}()
	<-provider.rootStarted
	root.mu.RLock()
	rootCtx := root.ctx
	root.mu.RUnlock()
	if rootCtx == nil || cap(root.concurrencySem) != 2 {
		t.Fatalf("real root bindings = ctx:%v concurrency:%d", rootCtx != nil, cap(root.concurrencySem))
	}

	type spawnOutcome struct {
		result *tools.ToolResult
		err    error
	}
	spawnDone := make([]chan spawnOutcome, 3)
	startSpawn := func(index int) {
		spawnDone[index] = make(chan spawnOutcome, 1)
		go func() {
			result, err := spawnSubTurnFromTrustedRuntime(rootCtx, al, root, SubTurnConfig{
				Model:        agent.Model,
				SystemPrompt: fmt.Sprintf("semaphore child %d", index),
				Timeout:      5 * time.Second,
			})
			spawnDone[index] <- spawnOutcome{result: result, err: err}
		}()
	}
	for i := 0; i < 2; i++ {
		startSpawn(i)
		select {
		case <-provider.childStarted:
		case <-time.After(2 * time.Second):
			t.Fatalf("child %d did not enter provider", i)
		}
	}
	if len(root.concurrencySem) != 2 {
		t.Fatalf("saturated semaphore length = %d, want 2", len(root.concurrencySem))
	}
	startSpawn(2)
	select {
	case <-provider.childStarted:
		t.Fatal("N+1 child entered before a semaphore slot was released")
	case <-time.After(50 * time.Millisecond):
	}

	provider.childRelease <- struct{}{}
	select {
	case <-provider.childStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("N+1 child did not enter after a slot was released")
	}
	if len(root.concurrencySem) != 2 {
		t.Fatalf("replacement semaphore length = %d, want 2", len(root.concurrencySem))
	}
	if err := al.HardAbort(root.sessionKey); err != nil {
		t.Fatal(err)
	}

	successes, canceled := 0, 0
	for i, done := range spawnDone {
		select {
		case got := <-done:
			switch {
			case got.err == nil && got.result != nil:
				successes++
			case errors.Is(got.err, context.Canceled):
				canceled++
			default:
				t.Fatalf("spawn %d outcome = %#v, %v", i, got.result, got.err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("spawn %d did not stop", i)
		}
	}
	if successes != 1 || canceled != 2 || len(root.concurrencySem) != 0 {
		t.Fatalf("spawn outcomes = success:%d canceled:%d semaphore:%d", successes, canceled, len(root.concurrencySem))
	}
	select {
	case got := <-rootDone:
		if got.err != nil || got.result.status != TurnEndStatusAborted {
			t.Fatalf("root outcome = %#v, %v", got.result, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("root did not stop")
	}
}

func TestRunTurnSupervisorSetupErrorAndPanicTerminalize(t *testing.T) {
	t.Run("setup error", func(t *testing.T) {
		al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
		defer cleanup()
		setupErr := errors.New("invalid turn configuration")
		agent.ConfigurationError = setupErr
		ts := newSupervisorTurnState(agent, "supervisor-setup-error", "turn-supervisor-setup-error")
		events, closeEvents := subscribeRuntimeEventsForTest(t, al, 8, runtimeevents.KindAgentTurnEnd)
		defer closeEvents()

		result, err := al.runTurn(context.Background(), ts, NewPipeline(al))
		if !errors.Is(err, setupErr) || result.status != TurnEndStatusError {
			t.Fatalf("runTurn() = %#v, %v", result, err)
		}
		if status := waitForTurnEndStatus(t, events, ts.turnID); status != TurnEndStatusError {
			t.Fatalf("turn.end status = %q", status)
		}
		if !ts.isFinished.Load() || al.getActiveTurnState(ts.sessionKey) != nil {
			t.Fatalf(
				"terminal state = finished:%t active:%v",
				ts.isFinished.Load(),
				al.getActiveTurnState(ts.sessionKey),
			)
		}
	})

	t.Run("panic restores and re-panics", func(t *testing.T) {
		provider := newSupervisorGateProvider(false)
		provider.panicValue = "provider exploded"
		al, agent, cleanup := newTurnCoordTestLoop(t, provider)
		defer cleanup()
		initial := []providers.Message{{Role: "user", Content: "before panic"}}
		store := newSupervisorSessionStore(initial, "before panic summary")
		agent.Sessions = store
		ts := newSupervisorTurnState(agent, "supervisor-panic", "turn-supervisor-panic")
		events, closeEvents := subscribeRuntimeEventsForTest(t, al, 8, runtimeevents.KindAgentTurnEnd)
		defer closeEvents()

		var recovered any
		func() {
			defer func() { recovered = recover() }()
			_, _ = al.runTurn(context.Background(), ts, NewPipeline(al))
		}()
		if recovered != "provider exploded" {
			t.Fatalf("recovered panic = %#v", recovered)
		}
		if status := waitForTurnEndStatus(t, events, ts.turnID); status != TurnEndStatusError {
			t.Fatalf("turn.end status = %q", status)
		}
		if history := store.GetHistory(ts.sessionKey); !reflect.DeepEqual(history, initial) {
			t.Fatalf("panic-restored history = %#v, want %#v", history, initial)
		}
		if saves, histories, summaries, restoreSaves := store.counts(); saves != 1 || histories != 1 ||
			summaries != 1 ||
			restoreSaves != 1 {
			t.Fatalf("panic restore counts = %d/%d/%d/%d", saves, histories, summaries, restoreSaves)
		}
		if !ts.isFinished.Load() || al.getActiveTurnState(ts.sessionKey) != nil {
			t.Fatal("panic terminal cleanup incomplete")
		}
	})
}

func TestRunTurnSupervisorCleanupPanicsCannotStrandOwner(t *testing.T) {
	t.Run("Git panic is rethrown after event and mandatory cleanup", func(t *testing.T) {
		al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
		defer cleanup()
		ts := newSupervisorTurnState(agent, "cleanup-git-panic", "turn-cleanup-git-panic")
		al.gitWorkspaces = &supervisorGitWorkspaceManager{
			onRelease: func(context.Context, gitworkspace.ReleaseRequest) {
				panic("git cleanup panic")
			},
		}
		events, closeEvents := subscribeRuntimeEventsForTest(t, al, 8, runtimeevents.KindAgentTurnEnd)
		defer closeEvents()

		_, _, recovered := runTurnRecovering(al, context.Background(), ts)
		if recovered != "git cleanup panic" || ts.terminalStatusSnapshot("") != TurnEndStatusCompleted {
			t.Fatalf("terminal status = %q, panic %#v", ts.terminalStatusSnapshot(""), recovered)
		}
		if status := waitForTurnEndStatus(t, events, ts.turnID); status != TurnEndStatusCompleted {
			t.Fatalf("turn.end status = %q", status)
		}
		if !ts.isFinished.Load() || al.getActiveTurnState(ts.sessionKey) != nil || ts.ctx.Err() == nil {
			t.Fatalf(
				"mandatory tail = finished:%t active:%v ctx_err:%v",
				ts.isFinished.Load(),
				al.getActiveTurnState(ts.sessionKey),
				ts.ctx.Err(),
			)
		}
	})

	t.Run("restore panic still attempts Git and turn end", func(t *testing.T) {
		provider := newSupervisorGateProvider(false)
		al, agent, cleanup := newTurnCoordTestLoop(t, provider)
		defer cleanup()
		store := newSupervisorSessionStore([]providers.Message{{Role: "user", Content: "stable"}}, "stable")
		store.panicRestore = "restore cleanup panic"
		agent.Sessions = store
		ts := newSupervisorTurnState(agent, "cleanup-restore-panic", "turn-cleanup-restore-panic")
		var gitAttempted atomic.Bool
		al.gitWorkspaces = &supervisorGitWorkspaceManager{
			onRelease: func(context.Context, gitworkspace.ReleaseRequest) {
				gitAttempted.Store(true)
			},
		}
		events, closeEvents := subscribeRuntimeEventsForTest(t, al, 8, runtimeevents.KindAgentTurnEnd)
		defer closeEvents()
		type panicOutcome struct {
			result    turnResult
			err       error
			recovered any
		}
		done := make(chan panicOutcome, 1)
		go func() {
			result, err, recovered := runTurnRecovering(al, context.Background(), ts)
			done <- panicOutcome{result: result, err: err, recovered: recovered}
		}()
		<-provider.started
		if abortErr := al.HardAbort(ts.sessionKey); abortErr != nil {
			t.Fatal(abortErr)
		}
		got := <-done
		if got.recovered != "restore cleanup panic" || ts.terminalStatusSnapshot("") != TurnEndStatusAborted {
			t.Fatalf("terminal status = %q, panic %#v", ts.terminalStatusSnapshot(""), got.recovered)
		}
		if !gitAttempted.Load() {
			t.Fatal("Git cleanup was skipped after restore panic")
		}
		if status := waitForTurnEndStatus(t, events, ts.turnID); status != TurnEndStatusAborted {
			t.Fatalf("turn.end status = %q", status)
		}
		if al.getActiveTurnState(ts.sessionKey) != nil || ts.ctx.Err() == nil {
			t.Fatal("restore panic stranded active owner or local context")
		}
	})

	t.Run("turn end event panic still clears owner", func(t *testing.T) {
		al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
		defer cleanup()
		al.runtimeEvents = &supervisorPanicEventBus{
			Bus:        al.runtimeEvents,
			panicKind:  runtimeevents.KindAgentTurnEnd,
			panicValue: "turn end event panic",
		}
		ts := newSupervisorTurnState(agent, "cleanup-event-panic", "turn-cleanup-event-panic")
		_, _, recovered := runTurnRecovering(al, context.Background(), ts)
		if recovered != "turn end event panic" || ts.terminalStatusSnapshot("") != TurnEndStatusCompleted {
			t.Fatalf("terminal status = %q, panic %#v", ts.terminalStatusSnapshot(""), recovered)
		}
		if al.getActiveTurnState(ts.sessionKey) != nil || ts.ctx.Err() == nil || !ts.isFinished.Load() {
			t.Fatal("event panic stranded terminal state")
		}
	})

	t.Run("original provider panic wins over cleanup panic", func(t *testing.T) {
		provider := newSupervisorGateProvider(false)
		provider.panicValue = "original provider panic"
		al, agent, cleanup := newTurnCoordTestLoop(t, provider)
		defer cleanup()
		al.gitWorkspaces = &supervisorGitWorkspaceManager{
			onRelease: func(context.Context, gitworkspace.ReleaseRequest) {
				panic("secondary cleanup panic")
			},
		}
		ts := newSupervisorTurnState(agent, "cleanup-panic-precedence", "turn-cleanup-panic-precedence")
		_, _, recovered := runTurnRecovering(al, context.Background(), ts)
		if recovered != "original provider panic" {
			t.Fatalf("panic precedence = %#v", recovered)
		}
		if al.getActiveTurnState(ts.sessionKey) != nil || ts.ctx.Err() == nil || !ts.isFinished.Load() {
			t.Fatal("dual panic stranded terminal state")
		}
	})
}

func TestRunTurnSupervisorStopsAtFailureCancellationBoundaries(t *testing.T) {
	t.Run("before setup", func(t *testing.T) {
		provider := &supervisorCountingProvider{}
		al, agent, cleanup := newTurnCoordTestLoop(t, provider)
		defer cleanup()
		root := newSupervisorTurnState(agent, "pre-setup-root", "turn-pre-setup-root")
		al.prepareTurnState(root)
		childAgent := *agent
		store := newSupervisorSessionStore(nil, "")
		childAgent.Sessions = store
		child := newSupervisorTurnState(&childAgent, "pre-setup-child", "turn-pre-setup-child")
		child.parentTurnState = root
		child.parentTurnID = root.turnID
		childInputCtx, childCancel := context.WithCancel(context.Background())
		child.ctx, child.cancelFunc = childInputCtx, childCancel
		al.prepareTurnState(child)
		if !al.attachChildTurn(root, child) {
			t.Fatal("attach child")
		}
		requestTurnTreeCancellation(al, root, false)
		events, closeEvents := subscribeRuntimeEventsForTest(
			t,
			al,
			8,
			runtimeevents.KindAgentTurnStart,
			runtimeevents.KindAgentTurnEnd,
		)
		defer closeEvents()

		result, err := al.runTurn(childInputCtx, child, NewPipeline(al))
		if !errors.Is(err, context.Canceled) || result.status != TurnEndStatusError {
			t.Fatalf("runTurn() = %#v, %v", result, err)
		}
		if provider.calls.Load() != 0 || len(store.GetHistory(child.sessionKey)) != 0 {
			t.Fatalf("pre-setup work = calls:%d history:%#v", provider.calls.Load(), store.GetHistory(child.sessionKey))
		}
		if status := waitForTurnEndStatus(t, events, child.turnID); status != TurnEndStatusError {
			t.Fatalf("turn.end status = %q", status)
		}
		select {
		case <-child.Finished():
		default:
			t.Fatal("pre-setup canceled child did not finish")
		}
		if al.getActiveTurnState(child.sessionKey) != nil {
			t.Fatal("pre-setup canceled child leaked exact active owner")
		}
		time.Sleep(10 * time.Millisecond)
		for _, event := range collectRuntimeEventStream(events) {
			if event.Kind == runtimeevents.KindAgentTurnStart {
				t.Fatal("turn.start emitted for pre-setup canceled child")
			}
		}
	})

	t.Run("during steering injection", func(t *testing.T) {
		provider := &supervisorCountingProvider{}
		al, agent, cleanup := newTurnCoordTestLoop(t, provider)
		defer cleanup()
		store := newSupervisorSessionStore(nil, "")
		agent.Sessions = store
		opts := makeTestProcessOpts("cancel-during-injection")
		opts.InitialSteeringMessages = []providers.Message{{Role: "user", Content: "cancel here"}}
		ts := newTurnState(agent, normalizeProcessOptions(opts), turnEventScope{
			turnID:  "turn-cancel-during-injection",
			context: newTurnContext(nil, nil, nil),
		})
		store.onAddFull = func() { requestTurnTreeCancellation(al, ts, false) }
		result, err := al.runTurn(context.Background(), ts, NewPipeline(al))
		if !errors.Is(err, context.Canceled) || result.status != TurnEndStatusError {
			t.Fatalf("runTurn() = %#v, %v", result, err)
		}
		if provider.calls.Load() != 0 {
			t.Fatalf("provider calls = %d, want 0", provider.calls.Load())
		}
		if saves, _, _, _ := store.counts(); saves != 0 {
			t.Fatalf("Save count = %d, want 0", saves)
		}
	})

	t.Run("after tool execution", func(t *testing.T) {
		provider := &toolCallRespProvider{
			toolName: "supervisor_cancel",
			toolArgs: map[string]any{},
			response: "unexpected second response",
		}
		al, agent, cleanup := newTurnCoordTestLoop(t, provider)
		defer cleanup()
		store := newSupervisorSessionStore(nil, "")
		agent.Sessions = store
		al.RegisterTool(&supervisorCancelTool{})
		ts := newSupervisorTurnState(agent, "cancel-after-tool", "turn-cancel-after-tool")
		result, err := al.runTurn(context.Background(), ts, NewPipeline(al))
		if !errors.Is(err, context.Canceled) || result.status != TurnEndStatusError {
			t.Fatalf("runTurn() = %#v, %v", result, err)
		}
		provider.mu.Lock()
		calls := provider.callCount
		provider.mu.Unlock()
		if calls != 1 {
			t.Fatalf("provider calls = %d, want 1", calls)
		}
		if saves, _, _, _ := store.counts(); saves != 0 {
			t.Fatalf("Save count = %d, want 0", saves)
		}
	})
}

func TestAttachedChildStartsOnlyWhenParentCompletionPolicyAllows(t *testing.T) {
	provider := &supervisorCountingProvider{}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	parent := newSupervisorTurnState(agent, "completed-parent", "turn-completed-parent")
	al.prepareTurnState(parent)

	newChild := func(sessionKey string, critical bool) (*turnState, context.Context) {
		childAgent := supervisorAgentWithProvider(agent, provider)
		child := newSupervisorTurnState(childAgent, sessionKey, "turn-"+sessionKey)
		child.parentTurnState = parent
		child.parentTurnID = parent.turnID
		child.critical = critical
		inputCtx, cancel := context.WithCancel(context.Background())
		child.ctx, child.cancelFunc = inputCtx, cancel
		al.prepareTurnState(child)
		if !al.attachChildTurn(parent, child) {
			t.Fatalf("attach %s", sessionKey)
		}
		return child, inputCtx
	}
	noncritical, noncriticalCtx := newChild("completed-noncritical", false)
	critical, criticalCtx := newChild("completed-critical", true)
	if status, committed := parent.runTerminal(TurnEndStatusCompleted); !committed || status != TurnEndStatusCompleted {
		t.Fatalf("parent terminal = %q, %t", status, committed)
	}

	result, err := al.runTurn(noncriticalCtx, noncritical, NewPipeline(al))
	if err != nil || result.status != TurnEndStatusCompleted || provider.calls.Load() != 0 {
		t.Fatalf("noncritical run = %#v, %v, provider calls %d", result, err, provider.calls.Load())
	}
	if history := noncritical.agent.Sessions.GetHistory(noncritical.sessionKey); len(history) != 0 {
		t.Fatalf("noncritical setup mutated history: %#v", history)
	}

	result, err = al.runTurn(criticalCtx, critical, NewPipeline(al))
	if err != nil || result.status != TurnEndStatusCompleted || provider.calls.Load() != 1 {
		t.Fatalf("critical run = %#v, %v, provider calls %d", result, err, provider.calls.Load())
	}
}

func TestRunTurnSupervisorRejectsConcurrentRepeatedAndPrefinishedState(t *testing.T) {
	provider := newSupervisorGateProvider(true)
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()
	al.cfg.Agents.Defaults.MaxParallelTurns = 11
	al.cfg.Agents.Defaults.SubTurn.MaxConcurrent = 3
	ts := newSupervisorTurnState(agent, "supervisor-once", "turn-supervisor-once")
	events, closeEvents := subscribeRuntimeEventsForTest(
		t,
		al,
		16,
		runtimeevents.KindAgentTurnStart,
		runtimeevents.KindAgentTurnEnd,
	)
	defer closeEvents()

	type outcome struct {
		result turnResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := al.runTurn(context.Background(), ts, NewPipeline(al))
		done <- outcome{result: result, err: err}
	}()
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not start")
	}

	if _, err := al.runTurn(context.Background(), ts, NewPipeline(al)); err == nil {
		t.Fatal("concurrent same-state run was accepted")
	}
	ts.Finish(false)
	if ts.isFinished.Load() || ts.terminalStatusSnapshot("") != "" {
		t.Fatal("compatibility Finish stole terminal ownership from admitted run")
	}
	close(provider.release)
	got := <-done
	if got.err != nil || got.result.status != TurnEndStatusCompleted {
		t.Fatalf("first run = %#v, %v", got.result, got.err)
	}
	if _, err := al.runTurn(context.Background(), ts, NewPipeline(al)); err == nil {
		t.Fatal("repeated same-state run was accepted")
	}
	if al.getActiveTurnState(ts.sessionKey) != nil {
		t.Fatal("same-state run leaked active owner")
	}
	if cap(ts.concurrencySem) != 3 {
		t.Fatalf("root concurrency = %d, want SubTurn.MaxConcurrent 3", cap(ts.concurrencySem))
	}

	starts, ends := 0, 0
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for ends == 0 {
		select {
		case event := <-events:
			if event.Scope.TurnID != ts.turnID {
				continue
			}
			switch event.Kind {
			case runtimeevents.KindAgentTurnStart:
				starts++
			case runtimeevents.KindAgentTurnEnd:
				ends++
			}
		case <-timer.C:
			t.Fatal("timed out waiting for lifecycle events")
		}
	}
	time.Sleep(10 * time.Millisecond)
	for _, event := range collectRuntimeEventStream(events) {
		if event.Scope.TurnID != ts.turnID {
			continue
		}
		if event.Kind == runtimeevents.KindAgentTurnStart {
			starts++
		}
		if event.Kind == runtimeevents.KindAgentTurnEnd {
			ends++
		}
	}
	if starts != 1 || ends != 1 {
		t.Fatalf("lifecycle counts = start:%d end:%d", starts, ends)
	}

	prefinished := newSupervisorTurnState(agent, "supervisor-prefinished", "turn-supervisor-prefinished")
	prefinished.Finish(false)
	if _, err := al.runTurn(context.Background(), prefinished, NewPipeline(al)); err == nil {
		t.Fatal("prefinished state was accepted")
	}
	if al.getActiveTurnState(prefinished.sessionKey) != nil {
		t.Fatal("prefinished state leaked active owner")
	}

	adhoc := al.newAdHocRootTurnState(context.Background())
	if adhoc.pendingResults == nil || cap(adhoc.concurrencySem) != 3 {
		t.Fatalf(
			"ad-hoc root bindings = mailbox:%v concurrency:%d",
			adhoc.pendingResults != nil,
			cap(adhoc.concurrencySem),
		)
	}
}

func TestRunTurnSupervisorFailureCancellationOverridesStubbornChildSuccess(t *testing.T) {
	provider := newSupervisorGateProvider(true)
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()

	root := newSupervisorTurnState(agent, "failure-root", "turn-failure-root")
	al.prepareTurnState(root)
	al.activeTurnStates.Store(root.sessionKey, root)
	defer al.releaseSessionTurnState(root.sessionKey, root)

	childAgent := *agent
	childStore := newSupervisorSessionStore(nil, "")
	childAgent.Sessions = childStore
	child := newSupervisorTurnState(&childAgent, "failure-child", "turn-failure-child")
	child.parentTurnState = root
	child.parentTurnID = root.turnID
	child.depth = 1
	child.critical = true
	childInputCtx, childInputCancel := context.WithCancel(context.Background())
	var childOwnedCancels atomic.Int64
	child.ctx = childInputCtx
	child.cancelFunc = func() {
		childOwnedCancels.Add(1)
		childInputCancel()
	}
	al.prepareTurnState(child)
	if !al.attachChildTurn(root, child) {
		t.Fatal("failed to attach critical child")
	}

	events, closeEvents := subscribeRuntimeEventsForTest(t, al, 8, runtimeevents.KindAgentTurnEnd)
	defer closeEvents()
	type outcome struct {
		result turnResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := al.runTurn(childInputCtx, child, NewPipeline(al))
		done <- outcome{result: result, err: err}
	}()
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("child provider did not start")
	}

	requestTurnTreeCancellation(al, root, false)
	if !child.cancellationRequested() || child.hardAbortRequested() {
		t.Fatalf(
			"child cancellation flags = failure:%t hard:%t",
			child.cancellationRequested(),
			child.hardAbortRequested(),
		)
	}
	close(provider.release)
	got := <-done
	if got.result.status != TurnEndStatusError || got.err == nil {
		t.Fatalf("stubborn child run = %#v, %v", got.result, got.err)
	}
	if status := waitForTurnEndStatus(t, events, child.turnID); status != TurnEndStatusError {
		t.Fatalf("child turn.end status = %q", status)
	}
	if childOwnedCancels.Load() != 1 {
		t.Fatalf("child owned cancellation count = %d, want 1", childOwnedCancels.Load())
	}
	if saves, _, _, _ := childStore.counts(); saves != 0 {
		t.Fatalf("stubborn child Save count = %d, want 0", saves)
	}
	for _, message := range childStore.GetHistory(child.sessionKey) {
		if message.Role == "assistant" && message.Content == "stubborn success" {
			t.Fatal("stubborn child persisted assistant success after failure cancellation")
		}
	}
}

func TestHardAbortTraversesTerminalIntermediateToCriticalGrandchild(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()

	root := newSupervisorTurnState(agent, "tree-root", "turn-tree-root")
	childAgent := *agent
	childAgent.Sessions = newEphemeralSession(nil)
	child := newSupervisorTurnState(&childAgent, "tree-child", "turn-tree-child")
	child.parentTurnState = root
	child.parentTurnID = root.turnID
	child.depth = 1
	grandchildAgent := *agent
	grandchildAgent.Sessions = newEphemeralSession(nil)
	grandchild := newSupervisorTurnState(&grandchildAgent, "tree-grandchild", "turn-tree-grandchild")
	grandchild.parentTurnState = child
	grandchild.parentTurnID = child.turnID
	grandchild.depth = 2
	grandchild.critical = true
	grandchildCtx, grandchildCancel := context.WithCancel(context.Background())
	grandchild.ctx, grandchild.cancelFunc = grandchildCtx, grandchildCancel

	al.prepareTurnState(root)
	al.prepareTurnState(child)
	al.prepareTurnState(grandchild)
	al.activeTurnStates.Store(root.sessionKey, root)
	defer al.releaseSessionTurnState(root.sessionKey, root)
	if !al.attachChildTurn(root, child) || !al.attachChildTurn(child, grandchild) {
		t.Fatal("failed to build exact three-level graph")
	}
	child.runTerminal(TurnEndStatusCompleted)
	al.releaseSessionTurnState(child.sessionKey, child)
	if al.getActiveTurnState(child.sessionKey) != nil || al.getActiveTurnState(grandchild.sessionKey) != grandchild {
		t.Fatal("test graph active-state precondition failed")
	}

	if !root.requestHardAbort() {
		t.Fatal("root hard abort was not accepted")
	}
	if !grandchild.hardAbortRequested() || !grandchild.cancellationRequested() {
		t.Fatalf(
			"grandchild flags = hard:%t cancel:%t",
			grandchild.hardAbortRequested(),
			grandchild.cancellationRequested(),
		)
	}
	select {
	case <-grandchildCtx.Done():
	default:
		t.Fatal("critical grandchild escaped hard abort through terminal intermediate")
	}
}

func TestTurnTerminalHardAbortAndCompletedLinearizeExactlyOnce(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()

	t.Run("completed wins", func(t *testing.T) {
		root := newSupervisorTurnState(agent, "linear-completed", "turn-linear-completed")
		childCtx, childCancel := context.WithCancel(context.Background())
		child := &turnState{
			turnID:          "turn-linear-completed-child",
			sessionKey:      "linear-completed-child",
			parentTurnID:    root.turnID,
			parentTurnState: root,
			critical:        true,
			ctx:             childCtx,
			cancelFunc:      childCancel,
		}
		al.prepareTurnState(root)
		al.prepareTurnState(child)
		if !al.attachChildTurn(root, child) {
			t.Fatal("attach child")
		}
		defer al.releaseSessionTurnState(child.sessionKey, child)
		status, committed := root.runTerminal(TurnEndStatusCompleted)
		if !committed || status != TurnEndStatusCompleted || root.requestHardAbort() {
			t.Fatalf(
				"completed outcome = %q committed:%t hardAccepted:%t",
				status,
				committed,
				root.hardAbortRequested(),
			)
		}
		select {
		case <-childCtx.Done():
			t.Fatal("late hard abort canceled surviving critical child")
		default:
		}
	})

	t.Run("hard wins", func(t *testing.T) {
		root := newSupervisorTurnState(agent, "linear-hard", "turn-linear-hard")
		al.prepareTurnState(root)
		if !root.requestHardAbort() {
			t.Fatal("hard request not accepted")
		}
		status, committed := root.runTerminal(TurnEndStatusCompleted)
		if !committed || status != TurnEndStatusAborted {
			t.Fatalf("hard outcome = %q committed:%t", status, committed)
		}
		if _, second := root.runTerminal(TurnEndStatusError); second {
			t.Fatal("second terminal transition committed")
		}
	})
}

func TestGracefulInterruptRejectedAfterFailureOrTerminalCommit(t *testing.T) {
	t.Run("failure cancellation", func(t *testing.T) {
		ts := &turnState{}
		requestTurnTreeCancellation(nil, ts, false)
		if ts.requestGracefulInterrupt("too late") {
			t.Fatal("graceful interrupt accepted after failure cancellation")
		}
	})

	t.Run("terminal cleanup", func(t *testing.T) {
		ts := &turnState{}
		ts.runTerminal(TurnEndStatusCompleted)
		if ts.requestGracefulInterrupt("too late") {
			t.Fatal("graceful interrupt accepted after terminal commit")
		}
	})
}

func TestTerminalClaimRejectsGracefulAttachAndResultBeforeCommit(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	parent := newSupervisorTurnState(agent, "terminal-claim-parent", "turn-terminal-claim-parent")
	al.prepareTurnState(parent)
	if !parent.claimDetachedTerminal() {
		t.Fatal("terminal claim failed")
	}
	if parent.isFinished.Load() {
		t.Fatal("test requires claim-before-commit window")
	}
	if parent.requestGracefulInterrupt("too late") {
		t.Fatal("graceful interrupt accepted after terminal claim")
	}

	childAgent := *agent
	childAgent.Sessions = newEphemeralSession(nil)
	child := newSupervisorTurnState(&childAgent, "terminal-claim-child", "turn-terminal-claim-child")
	child.parentTurnState = parent
	child.parentTurnID = parent.turnID
	al.prepareTurnState(child)
	if al.attachChildTurn(parent, child) {
		t.Fatal("child attached after terminal claim")
	}

	events, closeEvents := subscribeRuntimeEventsForTest(t, al, 4, runtimeevents.KindAgentSubTurnOrphan)
	defer closeEvents()
	deliverSubTurnResult(al, parent, child.sessionKey, &tools.ToolResult{ForLLM: "late"})
	event := waitForRuntimeEvent(t, events, 2*time.Second, func(event runtimeevents.Event) bool {
		return event.Kind == runtimeevents.KindAgentSubTurnOrphan
	})
	payload, ok := event.Payload.(SubTurnOrphanPayload)
	if !ok || payload.Reason != "parent_finished" {
		t.Fatalf("orphan payload = %#v", event.Payload)
	}
	if len(parent.pendingResults) != 0 {
		t.Fatal("result entered mailbox after terminal claim")
	}
	status, committed := parent.commitClaimedTerminal(TurnEndStatusCompleted)
	if !committed || status != TurnEndStatusCompleted {
		t.Fatalf("terminal commit = %q, %t", status, committed)
	}
}

func TestPipelineFinalizeAllResponsesHandledHardAbortDoesNotRestore(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	initial := []providers.Message{{Role: "user", Content: "stable"}}
	store := newSupervisorSessionStore(initial, "stable summary")
	agent.Sessions = store
	ts := newSupervisorTurnState(agent, "handled-hard", "turn-handled-hard")
	al.prepareTurnState(ts)
	if !ts.requestHardAbort() {
		t.Fatal("hard request not accepted")
	}
	exec := newTurnExecution(agent, ts.opts, initial, "stable summary", initial)
	exec.allResponsesHandled = true
	result, err := NewPipeline(al).Finalize(
		context.Background(),
		context.Background(),
		ts,
		exec,
		TurnEndStatusCompleted,
		"",
	)
	if err != nil || result.status != TurnEndStatusAborted {
		t.Fatalf("Finalize() = %#v, %v", result, err)
	}
	if saves, histories, summaries, restoreSaves := store.counts(); saves != 0 || histories != 0 || summaries != 0 ||
		restoreSaves != 0 {
		t.Fatalf("Finalize owned rollback = %d/%d/%d/%d", saves, histories, summaries, restoreSaves)
	}
	actual, committed := ts.runTerminal(TurnEndStatusCompleted)
	if !committed || actual != TurnEndStatusAborted {
		t.Fatalf("terminal outcome = %q committed:%t", actual, committed)
	}
	if restoreErr := ts.restoreSession(agent); restoreErr != nil {
		t.Fatal(restoreErr)
	}
	if saves, histories, summaries, restoreSaves := store.counts(); saves != 1 || histories != 1 || summaries != 1 ||
		restoreSaves != 1 {
		t.Fatalf("supervisor restore count = %d/%d/%d/%d", saves, histories, summaries, restoreSaves)
	}
}

func TestDeliverSubTurnResultRaceWithTerminalIsExactlyClassified(t *testing.T) {
	al, agent, cleanup := newTurnCoordTestLoop(t, &simpleConvProvider{})
	defer cleanup()
	parent := newSupervisorTurnState(agent, "delivery-race", "turn-delivery-race")
	al.prepareTurnState(parent)
	events, closeEvents := subscribeRuntimeEventsForTest(
		t,
		al,
		256,
		runtimeevents.KindAgentSubTurnResultDelivered,
		runtimeevents.KindAgentSubTurnOrphan,
	)
	defer closeEvents()

	const attempts = 128
	start := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(attempts + 1)
	for i := range attempts {
		go func(id int) {
			defer workers.Done()
			<-start
			deliverSubTurnResult(al, parent, fmt.Sprintf("child-%d", id), &tools.ToolResult{ForLLM: "result"})
		}(i)
	}
	go func() {
		defer workers.Done()
		<-start
		parent.runTerminal(TurnEndStatusCompleted)
	}()
	close(start)
	workers.Wait()

	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	delivered, orphaned := 0, 0
	for delivered+orphaned < attempts {
		select {
		case event := <-events:
			switch event.Kind {
			case runtimeevents.KindAgentSubTurnResultDelivered:
				delivered++
			case runtimeevents.KindAgentSubTurnOrphan:
				orphaned++
			}
		case <-deadline.C:
			t.Fatalf("classified = delivered:%d orphan:%d, want %d", delivered, orphaned, attempts)
		}
	}
	if delivered > cap(parent.pendingResults) || delivered+orphaned != attempts {
		t.Fatalf("classification = delivered:%d orphan:%d capacity:%d", delivered, orphaned, cap(parent.pendingResults))
	}
	time.Sleep(10 * time.Millisecond)
	for _, event := range collectRuntimeEventStream(events) {
		if event.Kind == runtimeevents.KindAgentSubTurnResultDelivered ||
			event.Kind == runtimeevents.KindAgentSubTurnOrphan {
			t.Fatalf("extra delivery classification event: %#v", event)
		}
	}
}
