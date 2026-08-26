package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

type trackedSubagentRuntimeCall struct {
	messages []providers.Message
}

type trackedSubagentRuntimeProvider struct {
	name    string
	err     error
	release <-chan struct{}
	calls   atomic.Int64
	called  chan trackedSubagentRuntimeCall
	closed  chan struct{}
	once    sync.Once
}

func newTrackedSubagentRuntimeProvider(name string) *trackedSubagentRuntimeProvider {
	return &trackedSubagentRuntimeProvider{
		name: name, called: make(chan trackedSubagentRuntimeCall, 8),
		closed: make(chan struct{}),
	}
}

func (provider *trackedSubagentRuntimeProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	provider.calls.Add(1)
	provider.called <- trackedSubagentRuntimeCall{
		messages: session.CloneMessages(messages),
	}
	if provider.release != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-provider.release:
		}
	}
	if provider.err != nil {
		return nil, provider.err
	}
	return &providers.LLMResponse{
		Content: provider.name, FinishReason: "stop",
	}, nil
}

func (provider *trackedSubagentRuntimeProvider) Close() {
	provider.once.Do(func() { close(provider.closed) })
}

type trackedSubagentRuntimeSnapshotMode uint8

const (
	trackedSubagentRuntimeSnapshotNormal trackedSubagentRuntimeSnapshotMode = iota
	trackedSubagentRuntimeSnapshotMissing
	trackedSubagentRuntimeSnapshotWrongOwner
	trackedSubagentRuntimeSnapshotChangedScope
	trackedSubagentRuntimeSnapshotTransient
	trackedSubagentRuntimeSnapshotDisappearAfterPreflight
)

// trackedSubagentRuntimeGuardStore keeps the candidate's real session store but
// lets these tests inject strict-snapshot outcomes and prove that rejection did
// not fall through to any legacy create-on-write API.
type trackedSubagentRuntimeGuardStore struct {
	session.SessionStore
	mode   trackedSubagentRuntimeSnapshotMode
	reads  atomic.Int64
	writes atomic.Int64
}

func (store *trackedSubagentRuntimeGuardStore) ReadSessionSnapshot(
	ctx context.Context,
	key string,
) (session.SessionSnapshot, bool, error) {
	store.reads.Add(1)
	if store.mode == trackedSubagentRuntimeSnapshotTransient && store.reads.Load() < 3 {
		return session.SessionSnapshot{}, false, errors.New("transient snapshot read")
	}
	if store.mode == trackedSubagentRuntimeSnapshotDisappearAfterPreflight &&
		store.reads.Load() >= 2 {
		return session.SessionSnapshot{}, false, nil
	}
	if store.mode == trackedSubagentRuntimeSnapshotMissing {
		return session.SessionSnapshot{}, false, nil
	}
	reader, ok := store.SessionStore.(session.SnapshotReader)
	if !ok {
		return session.SessionSnapshot{}, false, session.ErrSnapshotUnsupported
	}
	snapshot, found, err := reader.ReadSessionSnapshot(ctx, key)
	if err != nil || !found || (store.mode != trackedSubagentRuntimeSnapshotWrongOwner &&
		store.mode != trackedSubagentRuntimeSnapshotChangedScope) {
		return snapshot, found, err
	}
	snapshot.Scope = session.CloneScope(snapshot.Scope)
	if snapshot.Scope == nil {
		snapshot.Scope = &session.SessionScope{Version: session.ScopeVersionV1}
	}
	if store.mode == trackedSubagentRuntimeSnapshotWrongOwner {
		snapshot.Scope.AgentID = "main"
	} else {
		if snapshot.Scope.Values == nil {
			snapshot.Scope.Values = make(map[string]string)
		}
		snapshot.Scope.Values["chat"] = "direct:changed-chat"
	}
	return snapshot, true, nil
}

func (store *trackedSubagentRuntimeGuardStore) AddMessage(key, role, content string) {
	store.writes.Add(1)
	store.SessionStore.AddMessage(key, role, content)
}

func (store *trackedSubagentRuntimeGuardStore) AddFullMessage(
	key string,
	message providers.Message,
) {
	store.writes.Add(1)
	store.SessionStore.AddFullMessage(key, message)
}

func (store *trackedSubagentRuntimeGuardStore) SetSummary(key, summary string) {
	store.writes.Add(1)
	store.SessionStore.SetSummary(key, summary)
}

func (store *trackedSubagentRuntimeGuardStore) SetHistory(
	key string,
	history []providers.Message,
) {
	store.writes.Add(1)
	store.SessionStore.SetHistory(key, history)
}

func (store *trackedSubagentRuntimeGuardStore) TruncateHistory(key string, keepLast int) {
	store.writes.Add(1)
	store.SessionStore.TruncateHistory(key, keepLast)
}

func (store *trackedSubagentRuntimeGuardStore) Save(key string) error {
	store.writes.Add(1)
	return store.SessionStore.Save(key)
}

type trackedSubagentRuntimeFixture struct {
	loop          *AgentLoop
	messageBus    *bus.MessageBus
	mainWorkspace string
	rootWorkspace string
	rootSession   string
	route         trackedSubagentResultRoute
	recordID      trackedSubagentResultID
}

func newTrackedSubagentRuntimeFixture(
	t *testing.T,
	providerA *trackedSubagentRuntimeProvider,
) *trackedSubagentRuntimeFixture {
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
	loop := newTestAgentLoopWithStrictModels(cfg, messageBus, providerA)
	t.Cleanup(func() {
		loop.Close()
		messageBus.Close()
	})
	rootAgent, ok := loop.GetRegistry().GetAgent("alpha")
	if !ok || rootAgent == nil {
		t.Fatal("generation A alpha agent is unavailable")
	}
	rootScope := &session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    "alpha",
		Channel:    "test",
		Dimensions: []string{"chat"},
		Values:     map[string]string{"chat": "direct:tracked-result"},
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
		t.Fatalf("admit generation A root session: %v", err)
	}
	rootAgent.Sessions.AddMessage(rootSession, "user", "original named session")
	if err := rootAgent.Sessions.Save(rootSession); err != nil {
		t.Fatalf("save generation A root session: %v", err)
	}

	const rootTurnID = "tracked-runtime-root"
	const taskID = "subagent-runtime-1"
	route := trackedSubagentResultRoute{
		SourceTurnID: rootTurnID, SourceAgentID: rootAgent.ID,
		SourceSessionKey:            rootSession,
		RootTurnID:                  rootTurnID,
		RootAgentID:                 rootAgent.ID,
		RootSessionKey:              rootSession,
		RootChannel:                 "test",
		RootChatID:                  "tracked-result",
		RootPersistent:              true,
		RootLateContinuationAllowed: true,
		RootEnableSummary:           true,
		RootScope:                   session.CloneScope(rootScope),
		RootInbound: bus.InboundContext{
			Channel: "test", ChatID: "tracked-result", ChatType: "direct",
			SenderID: "runtime-user",
		},
	}
	return &trackedSubagentRuntimeFixture{
		loop: loop, messageBus: messageBus,
		mainWorkspace: mainWorkspace, rootWorkspace: rootWorkspace,
		rootSession: rootSession, route: route,
		recordID: trackedSubagentResultID{SourceTurnID: rootTurnID, TaskID: taskID},
	}
}

func trackedSubagentRuntimeConfig(
	mainWorkspace string,
	rootWorkspace string,
	includeRoot bool,
) *config.Config {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = mainWorkspace
	cfg.Agents.List = []config.AgentConfig{{
		ID: "main", Default: true, Workspace: mainWorkspace,
	}}
	if includeRoot {
		cfg.Agents.List = append(cfg.Agents.List, config.AgentConfig{
			ID: "alpha", Workspace: rootWorkspace,
		})
	}
	return cfg
}

func (fixture *trackedSubagentRuntimeFixture) publishPendingResult(t *testing.T) {
	t.Helper()
	root := &turnState{
		turnID: fixture.route.RootTurnID, agentID: fixture.route.RootAgentID,
		sessionKey: fixture.route.RootSessionKey,
		channel:    fixture.route.RootChannel, chatID: fixture.route.RootChatID,
		terminalStatus: TurnEndStatusCompleted,
	}
	if _, loaded := fixture.loop.activeTurnStates.LoadOrStore(fixture.rootSession, root); loaded {
		t.Fatal("root session already has an active turn")
	}
	fixture.loop.acceptTrackedSubagentResult(
		fixture.route,
		tools.SubagentCompletion{TaskID: fixture.recordID.TaskID, Status: "completed"},
		tools.NewToolResult("runtime mailbox payload"),
	)
	fixture.loop.releaseSessionTurnState(fixture.rootSession, root)
	fixture.loop.markTrackedSubagentResultOutputReady(root.turnID)
}

func (fixture *trackedSubagentRuntimeFixture) newConfigB(
	rootWorkspace string,
	includeRoot bool,
) *config.Config {
	return trackedSubagentRuntimeConfig(
		fixture.mainWorkspace,
		rootWorkspace,
		includeRoot,
	)
}

func waitForTrackedSubagentRuntimeCall(
	t *testing.T,
	provider *trackedSubagentRuntimeProvider,
) trackedSubagentRuntimeCall {
	t.Helper()
	select {
	case call := <-provider.called:
		return call
	case <-time.After(5 * time.Second):
		t.Fatalf("provider %q was not called", provider.name)
		return trackedSubagentRuntimeCall{}
	}
}

func waitForTrackedSubagentRuntimeRecord(
	t *testing.T,
	loop *AgentLoop,
	id trackedSubagentResultID,
	want trackedSubagentResultState,
	wantPumpIdle bool,
) trackedSubagentResultRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		loop.trackedSubagentResults.mu.Lock()
		record := loop.trackedSubagentResults.records[id]
		var snapshot trackedSubagentResultRecord
		pumpIdle := true
		if record != nil {
			snapshot = *record
			for _, state := range loop.trackedSubagentResults.scopes {
				if state != nil && (state.pumping || state.rescuingSteering) {
					pumpIdle = false
					break
				}
			}
		}
		loop.trackedSubagentResults.mu.Unlock()
		if record != nil && snapshot.state == want && (!wantPumpIdle || pumpIdle) {
			return snapshot
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"tracked result state = %v, pump idle %v; want state %v, idle %v",
				snapshot.state,
				pumpIdle,
				want,
				wantPumpIdle,
			)
		}
		time.Sleep(time.Millisecond)
	}
}

func trackedSubagentRuntimeMessagesContain(
	messages []providers.Message,
	parts ...string,
) bool {
	var content strings.Builder
	for _, message := range messages {
		content.WriteString(message.Content)
		content.WriteByte('\n')
	}
	joined := content.String()
	for _, part := range parts {
		if !strings.Contains(joined, part) {
			return false
		}
	}
	return true
}

func waitForTrackedSubagentRuntimeReload(
	t *testing.T,
	reloadDone <-chan error,
) {
	t.Helper()
	select {
	case err := <-reloadDone:
		if err != nil {
			t.Fatalf("reload failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("reload did not finish")
	}
}

func TestTrackedSubagentResultPumpReloadPauseWinnerUsesCoherentGenerationB(
	t *testing.T,
) {
	providerA := newTrackedSubagentRuntimeProvider("generation-a")
	fixture := newTrackedSubagentRuntimeFixture(t, providerA)
	providerB := newTrackedSubagentRuntimeProvider("generation-b")
	cfgB := fixture.newConfigB(fixture.rootWorkspace, true)
	ensureStrictTestModelSelection(cfgB, providerB)

	_, releaseCallback, err := fixture.loop.acquireRuntimeUse(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	callbackReleased := false
	defer func() {
		if !callbackReleased {
			releaseCallback()
		}
	}()
	reloadDone := make(chan error, 1)
	go func() {
		reloadDone <- fixture.loop.ReloadProviderAndConfig(
			context.Background(), providerB, cfgB,
		)
	}()
	waitForRecursionReloadPause(t, fixture.loop, reloadDone)

	// This models the retained tracked callback committing its detached
	// envelope while reload owns fresh admission. The pending mailbox itself
	// must not add a runtime lease after the callback returns.
	fixture.publishPendingResult(t)
	fixture.loop.runtimeGateMu.Lock()
	activeBeforeCallbackReturn := fixture.loop.runtimeGateActive
	fixture.loop.runtimeGateMu.Unlock()
	if activeBeforeCallbackReturn != 1 {
		t.Fatalf(
			"runtime leases after detached envelope commit = %d, want callback lease only",
			activeBeforeCallbackReturn,
		)
	}
	releaseCallback()
	callbackReleased = true
	waitForTrackedSubagentRuntimeReload(t, reloadDone)

	call := waitForTrackedSubagentRuntimeCall(t, providerB)
	if !trackedSubagentRuntimeMessagesContain(
		call.messages,
		"task_id="+fixture.recordID.TaskID,
		"status=completed",
		"runtime mailbox payload",
	) {
		t.Fatalf("generation B prompt omitted tracked envelope: %#v", call.messages)
	}
	waitForTrackedSubagentRuntimeRecord(
		t, fixture.loop, fixture.recordID, trackedSubagentResultClaimed, true,
	)
	if got := providerA.calls.Load(); got != 0 {
		t.Fatalf("generation A provider calls = %d, want 0", got)
	}
	if got := providerB.calls.Load(); got != 1 {
		t.Fatalf("generation B provider calls = %d, want 1", got)
	}
}

func TestTrackedSubagentResultPumpAdmissionWinnerRetainsCoherentGenerationA(
	t *testing.T,
) {
	releaseProviderA := make(chan struct{})
	providerA := newTrackedSubagentRuntimeProvider("generation-a")
	providerA.release = releaseProviderA
	fixture := newTrackedSubagentRuntimeFixture(t, providerA)
	providerB := newTrackedSubagentRuntimeProvider("generation-b")
	cfgB := fixture.newConfigB(fixture.rootWorkspace, true)
	ensureStrictTestModelSelection(cfgB, providerB)

	fixture.publishPendingResult(t)
	call := waitForTrackedSubagentRuntimeCall(t, providerA)
	if !trackedSubagentRuntimeMessagesContain(
		call.messages,
		"task_id="+fixture.recordID.TaskID,
		"runtime mailbox payload",
	) {
		t.Fatalf("generation A prompt omitted tracked envelope: %#v", call.messages)
	}

	reloadDone := make(chan error, 1)
	go func() {
		reloadDone <- fixture.loop.ReloadProviderAndConfig(
			context.Background(), providerB, cfgB,
		)
	}()
	waitForRecursionReloadPause(t, fixture.loop, reloadDone)
	if got := providerB.calls.Load(); got != 0 {
		t.Fatalf("generation B provider ran across generation A lease: %d calls", got)
	}
	close(releaseProviderA)
	waitForTrackedSubagentRuntimeReload(t, reloadDone)
	waitForTrackedSubagentRuntimeRecord(
		t, fixture.loop, fixture.recordID, trackedSubagentResultClaimed, true,
	)
	if got := providerA.calls.Load(); got != 1 {
		t.Fatalf("generation A provider calls = %d, want 1", got)
	}
	if got := providerB.calls.Load(); got != 0 {
		t.Fatalf("generation B provider calls = %d, want 0", got)
	}
}

func TestTrackedSubagentResultPumpRejectsChangedNamedGenerationWithoutFallback(
	t *testing.T,
) {
	tests := []struct {
		name           string
		includeRoot    bool
		rootWorkspace  func(*trackedSubagentRuntimeFixture, *testing.T) string
		snapshotMode   trackedSubagentRuntimeSnapshotMode
		wantStrictRead bool
	}{
		{
			name: "named agent removed", includeRoot: false,
			rootWorkspace: func(fixture *trackedSubagentRuntimeFixture, _ *testing.T) string {
				return fixture.rootWorkspace
			},
		},
		{
			name: "canonical session missing", includeRoot: true,
			rootWorkspace: func(_ *trackedSubagentRuntimeFixture, t *testing.T) string {
				return filepath.Join(t.TempDir(), "replacement-alpha")
			},
			snapshotMode: trackedSubagentRuntimeSnapshotMissing, wantStrictRead: true,
		},
		{
			name: "canonical session owner changed", includeRoot: true,
			rootWorkspace: func(fixture *trackedSubagentRuntimeFixture, _ *testing.T) string {
				return fixture.rootWorkspace
			},
			snapshotMode: trackedSubagentRuntimeSnapshotWrongOwner, wantStrictRead: true,
		},
		{
			name: "canonical session scope changed", includeRoot: true,
			rootWorkspace: func(fixture *trackedSubagentRuntimeFixture, _ *testing.T) string {
				return fixture.rootWorkspace
			},
			snapshotMode: trackedSubagentRuntimeSnapshotChangedScope, wantStrictRead: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			providerA := newTrackedSubagentRuntimeProvider("generation-a")
			fixture := newTrackedSubagentRuntimeFixture(t, providerA)
			providerB := newTrackedSubagentRuntimeProvider("generation-b")
			rootWorkspace := test.rootWorkspace(fixture, t)
			if test.includeRoot {
				if err := os.MkdirAll(rootWorkspace, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			cfgB := fixture.newConfigB(rootWorkspace, test.includeRoot)
			ensureStrictTestModelSelection(cfgB, providerB)

			var (
				candidateRegistry *AgentRegistry
				guard             *trackedSubagentRuntimeGuardStore
			)
			fixture.loop.registryFactory = func(
				candidateConfig *config.Config,
				candidateProvider providers.LLMProvider,
			) *AgentRegistry {
				candidateRegistry = NewAgentRegistry(candidateConfig, candidateProvider)
				if alpha, ok := candidateRegistry.GetAgent("alpha"); ok &&
					test.snapshotMode != trackedSubagentRuntimeSnapshotNormal {
					guard = &trackedSubagentRuntimeGuardStore{
						SessionStore: alpha.Sessions, mode: test.snapshotMode,
					}
					alpha.Sessions = guard
				}
				return candidateRegistry
			}

			_, releaseCallback, err := fixture.loop.acquireRuntimeUse(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			callbackReleased := false
			defer func() {
				if !callbackReleased {
					releaseCallback()
				}
			}()
			reloadDone := make(chan error, 1)
			go func() {
				reloadDone <- fixture.loop.ReloadProviderAndConfig(
					context.Background(), providerB, cfgB,
				)
			}()
			waitForRecursionReloadPause(t, fixture.loop, reloadDone)
			fixture.publishPendingResult(t)
			releaseCallback()
			callbackReleased = true
			waitForTrackedSubagentRuntimeReload(t, reloadDone)

			record := waitForTrackedSubagentRuntimeRecord(
				t,
				fixture.loop,
				fixture.recordID,
				trackedSubagentResultOrphaned,
				true,
			)
			if record.orphanReason != "invalid_named_session" {
				t.Fatalf("orphan reason = %q, want invalid_named_session", record.orphanReason)
			}
			if candidateRegistry == nil {
				t.Fatal("generation B registry was not constructed")
			}
			if test.wantStrictRead && (guard == nil || guard.reads.Load() != 1) {
				t.Fatalf("strict snapshot reads = %v, want 1", func() int64 {
					if guard == nil {
						return 0
					}
					return guard.reads.Load()
				}())
			}
			if guard != nil && guard.writes.Load() != 0 {
				t.Fatalf("rejected named session writes = %d, want 0", guard.writes.Load())
			}
			if got := providerA.calls.Load(); got != 0 {
				t.Fatalf("generation A provider calls = %d, want 0", got)
			}
			if got := providerB.calls.Load(); got != 0 {
				t.Fatalf("generation B/default provider calls = %d, want 0", got)
			}
			defaultAgent := candidateRegistry.GetDefaultAgent()
			if defaultAgent == nil {
				t.Fatal("generation B default agent is unavailable")
			}
			for _, key := range defaultAgent.Sessions.ListSessions() {
				if key == fixture.rootSession {
					t.Fatalf("default agent created/fell back to named session %q", key)
				}
			}
			if active := fixture.loop.getActiveTurnState(fixture.rootSession); active != nil {
				t.Fatalf("rejected named session retained a placeholder: %#v", active)
			}
			select {
			case outbound := <-fixture.messageBus.OutboundChan():
				t.Fatalf("rejected named continuation published outbound: %#v", outbound)
			default:
			}
		})
	}
}

func TestTrackedSubagentResultProviderFailureAfterClaimIsIrreversible(
	t *testing.T,
) {
	provider := newTrackedSubagentRuntimeProvider("permanent-failure")
	provider.err = errors.New("provider observed permanent failure")
	fixture := newTrackedSubagentRuntimeFixture(t, provider)

	fixture.publishPendingResult(t)
	call := waitForTrackedSubagentRuntimeCall(t, provider)
	if !trackedSubagentRuntimeMessagesContain(
		call.messages,
		"task_id="+fixture.recordID.TaskID,
		"runtime mailbox payload",
	) {
		t.Fatalf("claimed prompt omitted tracked envelope: %#v", call.messages)
	}
	record := waitForTrackedSubagentRuntimeRecord(
		t,
		fixture.loop,
		fixture.recordID,
		trackedSubagentResultClaimed,
		true,
	)
	if record.orphanReason != "" {
		t.Fatalf("claimed record was rewritten as orphan %q", record.orphanReason)
	}
	if active := fixture.loop.getActiveTurnState(fixture.rootSession); active != nil {
		t.Fatalf("failed continuation retained a placeholder: %#v", active)
	}

	// A duplicate callback and explicit pump kicks must remain tombstoned after
	// the provider has observed the claim. Requeueing here risks a duplicate
	// model-visible call even though the first continuation returned an error.
	fixture.loop.acceptTrackedSubagentResult(
		fixture.route,
		tools.SubagentCompletion{TaskID: fixture.recordID.TaskID, Status: "completed"},
		tools.NewToolResult("runtime mailbox payload"),
	)
	scope := trackedSubagentResultScope{
		AgentID: fixture.route.RootAgentID, SessionKey: fixture.route.RootSessionKey,
	}
	for range 3 {
		fixture.loop.maybeStartTrackedSubagentResultPump(scope)
	}
	fixture.loop.trackedSubagentResults.mu.Lock()
	scopeState := fixture.loop.trackedSubagentResults.scopes[scope]
	queued := 0
	pending := 0
	pumping := false
	if scopeState != nil {
		queued = len(scopeState.queue)
		pending = scopeState.pending
		pumping = scopeState.pumping
	}
	fixture.loop.trackedSubagentResults.mu.Unlock()
	if queued != 0 || pending != 0 || pumping {
		t.Fatalf(
			"post-claim mailbox = queued %d, pending %d, pumping %v; want terminal tombstone",
			queued,
			pending,
			pumping,
		)
	}
	if got := provider.calls.Load(); got != 1 {
		t.Fatalf("provider calls after duplicate/re-pump = %d, want 1", got)
	}
}

func TestTrackedSubagentResultRetriesTransientStrictSnapshotBeforeClaim(t *testing.T) {
	provider := newTrackedSubagentRuntimeProvider("transient-recovered")
	fixture := newTrackedSubagentRuntimeFixture(t, provider)
	agent, ok := fixture.loop.GetRegistry().GetAgent(fixture.route.RootAgentID)
	if !ok || agent == nil {
		t.Fatal("named agent is unavailable")
	}
	guard := &trackedSubagentRuntimeGuardStore{
		SessionStore: agent.Sessions,
		mode:         trackedSubagentRuntimeSnapshotTransient,
	}
	agent.Sessions = guard

	fixture.publishPendingResult(t)
	call := waitForTrackedSubagentRuntimeCall(t, provider)
	if !trackedSubagentRuntimeMessagesContain(call.messages, "runtime mailbox payload") {
		t.Fatalf("recovered prompt = %#v", call.messages)
	}
	if got := guard.reads.Load(); got != 4 {
		t.Fatalf("strict snapshot attempts = %d, want 4 (including run-boundary recheck)", got)
	}
	waitForTrackedSubagentRuntimeRecord(
		t,
		fixture.loop,
		fixture.recordID,
		trackedSubagentResultClaimed,
		true,
	)
}

func TestTrackedSubagentResultStopCancelsAdmittedDetachedPump(t *testing.T) {
	release := make(chan struct{})
	provider := newTrackedSubagentRuntimeProvider("blocked-until-stop")
	provider.release = release
	fixture := newTrackedSubagentRuntimeFixture(t, provider)
	fixture.publishPendingResult(t)
	_ = waitForTrackedSubagentRuntimeCall(t, provider)

	fixture.loop.Stop()
	deadline := time.Now().Add(3 * time.Second)
	for {
		fixture.loop.runtimeGateMu.Lock()
		active := fixture.loop.runtimeGateActive
		fixture.loop.runtimeGateMu.Unlock()
		if active == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("detached result pump retained %d runtime lease(s) after Stop", active)
		}
		time.Sleep(time.Millisecond)
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
		t.Fatalf("stopped detached pump published outbound: %#v", outbound)
	default:
	}
}

func TestTrackedSubagentResultRunBoundaryDoesNotReadmitDisappearedSession(t *testing.T) {
	provider := newTrackedSubagentRuntimeProvider("must-not-run")
	fixture := newTrackedSubagentRuntimeFixture(t, provider)
	agent, ok := fixture.loop.GetRegistry().GetAgent(fixture.route.RootAgentID)
	if !ok || agent == nil {
		t.Fatal("named agent is unavailable")
	}
	guard := &trackedSubagentRuntimeGuardStore{
		SessionStore: agent.Sessions,
		mode:         trackedSubagentRuntimeSnapshotDisappearAfterPreflight,
	}
	agent.Sessions = guard

	fixture.publishPendingResult(t)
	waitForTrackedSubagentRuntimeRecord(
		t,
		fixture.loop,
		fixture.recordID,
		trackedSubagentResultClaimed,
		true,
	)
	if got := guard.reads.Load(); got != 2 {
		t.Fatalf("strict reads = %d, want preflight plus run-boundary recheck", got)
	}
	if got := guard.writes.Load(); got != 0 {
		t.Fatalf("disappeared session received %d write(s)", got)
	}
	if got := provider.calls.Load(); got != 0 {
		t.Fatalf("provider calls after disappeared session = %d", got)
	}
	select {
	case outbound := <-fixture.messageBus.OutboundChan():
		t.Fatalf("disappeared session published outbound: %#v", outbound)
	default:
	}
}

func TestTrackedSubagentResultWaitsForLaterSessionOutputOwner(t *testing.T) {
	provider := newTrackedSubagentRuntimeProvider("after-later-output")
	fixture := newTrackedSubagentRuntimeFixture(t, provider)
	root := &turnState{
		turnID: fixture.route.RootTurnID, agentID: fixture.route.RootAgentID,
		sessionKey: fixture.route.RootSessionKey,
		channel:    fixture.route.RootChannel, chatID: fixture.route.RootChatID,
		terminalStatus: TurnEndStatusCompleted,
	}
	fixture.loop.activeTurnStates.Store(fixture.rootSession, root)
	fixture.loop.acceptTrackedSubagentResult(
		fixture.route,
		tools.SubagentCompletion{TaskID: fixture.recordID.TaskID, Status: "completed"},
		tools.NewToolResult("held by later output owner"),
	)
	fixture.loop.releaseSessionTurnState(fixture.rootSession, root)

	owner := &trackedSubagentResultOutputOwner{}
	const laterTurnID = "later-session-turn"
	owner.record(fixture.loop, laterTurnID, fixture.rootSession)
	later := &turnState{
		turnID: laterTurnID, agentID: fixture.route.RootAgentID,
		sessionKey: fixture.rootSession,
		channel:    fixture.route.RootChannel, chatID: fixture.route.RootChatID,
		terminalStatus: TurnEndStatusCompleted,
	}
	fixture.loop.activeTurnStates.Store(fixture.rootSession, later)
	fixture.loop.markTrackedSubagentResultOutputReady(root.turnID)
	fixture.loop.releaseSessionTurnState(fixture.rootSession, later)
	mailboxScope := trackedSubagentResultScope{
		AgentID: fixture.route.RootAgentID, SessionKey: fixture.rootSession,
	}
	fixture.loop.trackedSubagentResults.mu.Lock()
	state := fixture.loop.trackedSubagentResults.scopes[mailboxScope]
	pumping := state != nil && state.pumping
	holds := fixture.loop.trackedSubagentResults.outputHolds[fixture.rootSession]
	fixture.loop.trackedSubagentResults.mu.Unlock()
	if pumping || holds != 1 {
		t.Fatalf("later output barrier = pumping:%v holds:%d, want false/1", pumping, holds)
	}
	if got := provider.calls.Load(); got != 0 {
		t.Fatalf("result provider ran before later output owner released: %d", got)
	}
	owner.release(fixture.loop)
	_ = waitForTrackedSubagentRuntimeCall(t, provider)
	waitForTrackedSubagentRuntimeRecord(
		t,
		fixture.loop,
		fixture.recordID,
		trackedSubagentResultClaimed,
		true,
	)
}
