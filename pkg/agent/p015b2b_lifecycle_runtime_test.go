package agent

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/evolution"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
	"github.com/sipeed/picoclaw/pkg/seahorse"
	"github.com/sipeed/picoclaw/pkg/session"
)

const p015B2BLifecycleErrorCanary = "P015B2B_LIFECYCLE_ERROR_05c83e97"

type p015B2BLifecycleHostileError struct {
	calls *atomic.Int64
}

func (err *p015B2BLifecycleHostileError) Error() string {
	err.calls.Add(1)
	return p015B2BLifecycleErrorCanary
}

type p015B2BLifecycleUnexpectedEntryError struct {
	calls *atomic.Int64
}

func (value *p015B2BLifecycleUnexpectedEntryError) Error() string {
	value.calls.Add(1)
	return p015B2BLifecycleErrorCanary
}

func (value *p015B2BLifecycleUnexpectedEntryError) String() string {
	value.calls.Add(1)
	return p015B2BLifecycleErrorCanary
}

func TestP015B2BLifecycleBudgetFallbackPreservesExactEffectiveBudget(t *testing.T) {
	const (
		sessionCanary = "P015B2B_BUDGET_SESSION_790b2ea4"
		contentCanary = "P015B2B_BUDGET_CONTENT_bed01892"
	)
	engine, err := seahorse.NewEngine(seahorse.Config{DBPath: t.TempDir() + "/budget.db"}, nil)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	defer engine.Close()
	manager := &seahorseContextManager{engine: engine}
	for index := 0; index < 24; index++ {
		role := "user"
		if index%2 != 0 {
			role = "assistant"
		}
		if ingestErr := manager.Ingest(context.Background(), &IngestRequest{
			SessionKey: sessionCanary,
			Message: protocoltypes.Message{
				Role:    role,
				Content: contentCanary + " alpha beta gamma delta epsilon zeta eta theta",
			},
		}); ingestErr != nil {
			t.Fatalf("Ingest(%d) error = %v", index, ingestErr)
		}
	}

	want, err := engine.Assemble(
		context.Background(),
		sessionCanary,
		seahorse.AssembleInput{Budget: 140},
	)
	if err != nil {
		t.Fatalf("direct fallback baseline error = %v", err)
	}
	alternate, err := engine.Assemble(
		context.Background(),
		sessionCanary,
		seahorse.AssembleInput{Budget: 280},
	)
	if err != nil {
		t.Fatalf("direct non-fallback baseline error = %v", err)
	}
	if reflect.DeepEqual(want.Messages, alternate.Messages) {
		t.Fatal("budget fixture does not distinguish effective budgets 140 and 280")
	}

	var got *AssembleResponse
	records, raw := captureP015HookRecords(t, func() {
		got, err = manager.Assemble(context.Background(), &AssembleRequest{
			SessionKey: sessionCanary,
			Budget:     280,
			MaxTokens:  280,
		})
	})
	if err != nil {
		t.Fatalf("Assemble() error = %v", err)
	}
	if got == nil || !reflect.DeepEqual(got.History, seahorseToProviderMessages(want)) {
		t.Fatalf("fallback history = %#v, want exact direct budget-140 result %#v", got, want.Messages)
	}
	record := p015B2BLifecycleRequireRecord(
		t,
		records,
		"MaxTokens >= budget, using 50% fallback",
	)
	if record["context_window"] != float64(280) || record["max_tokens"] != float64(280) {
		t.Fatalf("fallback numeric fields = %#v, want context_window/max_tokens 280", record)
	}
	if record["fallback"] != true {
		t.Fatalf("fallback marker = %#v, want true; record=%#v", record["fallback"], record)
	}
	assertP015CanariesAbsent(t, raw, sessionCanary, contentCanary)
}

type p015B2BLifecycleSnapshotStore struct {
	session.SessionStore
	err   error
	reads int
}

func (store *p015B2BLifecycleSnapshotStore) ReadSessionSnapshot(
	context.Context,
	string,
) (session.SessionSnapshot, bool, error) {
	store.reads++
	return session.SessionSnapshot{}, false, store.err
}

func TestP015B2BLifecycleSeahorseBootstrapFailureIsBestEffortAndSealed(t *testing.T) {
	const sessionCanary = "P015B2B_BOOTSTRAP_SESSION_288cad11"
	var methodCalls atomic.Int64
	hostile := &p015B2BLifecycleHostileError{calls: &methodCalls}
	engine, err := seahorse.NewEngine(seahorse.Config{DBPath: t.TempDir() + "/bootstrap.db"}, nil)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	defer engine.Close()
	store := &p015B2BLifecycleSnapshotStore{err: hostile}
	agent := &AgentInstance{Sessions: store}
	manager := &seahorseContextManager{}

	var bootstrapErr error
	records, raw := captureP015HookRecords(t, func() {
		bootstrapErr = manager.bootstrapAgentSession(
			context.Background(),
			agent,
			engine,
			sessionCanary,
		)
	})
	if bootstrapErr != nil {
		t.Fatalf("bootstrapAgentSession() error = %v, want best-effort nil", bootstrapErr)
	}
	if store.reads != 1 {
		t.Fatalf("snapshot reads = %d, want exactly 1", store.reads)
	}
	if methodCalls.Load() != 0 {
		t.Fatalf("bootstrap diagnostics invoked %d hostile error methods", methodCalls.Load())
	}
	record := p015B2BLifecycleRequireRecord(t, records, "bootstrap snapshot")
	p015B2BLifecycleRequireObservation(
		t,
		record,
		"identity_session_digest",
		logger.ObserveIdentity(logger.ObservationDomainIdentitySession, sessionCanary).Digest,
	)
	assertP015CanariesAbsent(t, raw, sessionCanary, p015B2BLifecycleErrorCanary)
}

type p015B2BLifecycleWorkspaceManager struct {
	gitWorkspaceManager
	releaseErr     error
	reconcileErr   error
	released       []gitworkspace.WorkspaceInfo
	releaseCalls   int
	reconcileCalls int
	request        gitworkspace.ReleaseRequest
}

func (manager *p015B2BLifecycleWorkspaceManager) ReleaseSession(
	_ context.Context,
	request gitworkspace.ReleaseRequest,
) ([]gitworkspace.WorkspaceInfo, error) {
	manager.releaseCalls++
	manager.request = request
	return manager.released, manager.releaseErr
}

func (manager *p015B2BLifecycleWorkspaceManager) Reconcile(
	context.Context,
) (gitworkspace.ReconcileResult, error) {
	manager.reconcileCalls++
	return gitworkspace.ReconcileResult{}, manager.reconcileErr
}

func TestP015B2BLifecycleWorkspaceCleanupPreservesControlFlowAndSealsValues(t *testing.T) {
	const (
		sessionCanary = "P015B2B_WORKSPACE_SESSION_b5faed98"
		agentCanary   = "P015B2B_WORKSPACE_AGENT_7f055774"
	)
	var methodCalls atomic.Int64
	hostile := &p015B2BLifecycleHostileError{calls: &methodCalls}
	releaseFailure := &p015B2BLifecycleWorkspaceManager{releaseErr: hostile}
	reconcileFailure := &p015B2BLifecycleWorkspaceManager{
		released:     []gitworkspace.WorkspaceInfo{{}},
		reconcileErr: hostile,
	}
	state := &turnState{sessionKey: sessionCanary, agentID: agentCanary}

	records, raw := captureP015HookRecords(t, func() {
		(&AgentLoop{gitWorkspaces: releaseFailure}).releaseGitWorkspacesForTurn(
			context.Background(), state,
		)
		(&AgentLoop{gitWorkspaces: reconcileFailure}).releaseGitWorkspacesForTurn(
			context.Background(), state,
		)
	})
	if releaseFailure.releaseCalls != 1 || releaseFailure.reconcileCalls != 0 {
		t.Fatalf(
			"release-error calls = %d release/%d reconcile, want 1/0",
			releaseFailure.releaseCalls,
			releaseFailure.reconcileCalls,
		)
	}
	if reconcileFailure.releaseCalls != 1 || reconcileFailure.reconcileCalls != 1 {
		t.Fatalf(
			"reconcile-error calls = %d release/%d reconcile, want 1/1",
			reconcileFailure.releaseCalls,
			reconcileFailure.reconcileCalls,
		)
	}
	wantRequest := gitworkspace.ReleaseRequest{SessionKey: sessionCanary, AgentID: agentCanary}
	if releaseFailure.request != wantRequest || reconcileFailure.request != wantRequest {
		t.Fatalf(
			"release requests = %#v / %#v, want %#v",
			releaseFailure.request,
			reconcileFailure.request,
			wantRequest,
		)
	}
	if methodCalls.Load() != 0 {
		t.Fatalf("workspace diagnostics invoked %d hostile error methods", methodCalls.Load())
	}
	p015B2BLifecycleRequireRecord(t, records, "Failed to release git workspace locks")
	p015B2BLifecycleRequireRecord(t, records, "Failed to reconcile git workspace retention")
	assertP015CanariesAbsent(
		t,
		raw,
		sessionCanary,
		agentCanary,
		p015B2BLifecycleErrorCanary,
	)
}

func TestP015B2BLifecycleWorkspaceManagerFailureUsesInternalClass(t *testing.T) {
	const rootCanary = "P015B2B_WORKSPACE_ROOT_ERROR_90c8a7e2"
	cfg := &config.Config{GitWorkspaces: config.GitWorkspacesConfig{
		RootDir: rootCanary + "\x00invalid",
	}}
	var manager gitWorkspaceManager
	records, raw := captureP015HookRecords(t, func() {
		manager = newGitWorkspaceManagerFromConfig(cfg)
	})
	if manager != nil {
		t.Fatal("invalid workspace root unexpectedly constructed a manager")
	}
	record := p015B2BLifecycleRequireRecord(
		t,
		records,
		"Failed to initialize git workspace manager",
	)
	if record["error_class"] != "internal" || record["error_digest"] == nil {
		t.Fatalf("workspace-manager diagnostic = %#v", record)
	}
	assertP015CanariesAbsent(t, raw, rootCanary)
}

type p015B2BLifecycleEvolutionRuntime struct {
	err       error
	coldErr   error
	input     evolution.TurnCaseInput
	calls     int
	coldCalls int
}

func (runtime *p015B2BLifecycleEvolutionRuntime) FinalizeTurn(
	_ context.Context,
	input evolution.TurnCaseInput,
) error {
	runtime.calls++
	runtime.input = input
	return runtime.err
}

func (runtime *p015B2BLifecycleEvolutionRuntime) RunColdPathOnce(context.Context, string) error {
	runtime.coldCalls++
	return runtime.coldErr
}

type p015B2BLifecycleSubscription struct {
	err  error
	done chan struct{}
}

func (*p015B2BLifecycleSubscription) ID() uint64 { return 1 }
func (*p015B2BLifecycleSubscription) Name() string {
	return "p015b2b-lifecycle"
}
func (subscription *p015B2BLifecycleSubscription) Close() error { return subscription.err }
func (subscription *p015B2BLifecycleSubscription) Done() <-chan struct{} {
	return subscription.done
}

func (*p015B2BLifecycleSubscription) Stats() runtimeevents.SubscriberStats {
	return runtimeevents.SubscriberStats{}
}

func TestP015B2BLifecycleEvolutionFailurePathsPreserveInputsAndSealValues(t *testing.T) {
	const (
		turnCanary      = "P015B2B_EVOLUTION_TURN_1dfcd348"
		workspaceCanary = "/private/P015B2B_EVOLUTION_WORKSPACE_5b4ea551"
		scheduleCanary  = "P015B2B_EVOLUTION_SCHEDULE_79e78665"
	)
	var methodCalls atomic.Int64
	hostile := &p015B2BLifecycleHostileError{calls: &methodCalls}
	runtime := &p015B2BLifecycleEvolutionRuntime{err: hostile}
	coldRuntime := &p015B2BLifecycleEvolutionRuntime{coldErr: hostile}
	bridge := &evolutionBridge{runtime: runtime, bgCtx: context.Background()}
	coldCfg := &config.Config{
		Agents: config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: workspaceCanary}},
		Evolution: config.EvolutionConfig{
			Enabled: true,
			Mode:    "draft",
		},
	}
	coldBridge, err := newEvolutionBridge(nil, coldCfg, nil)
	if err != nil {
		t.Fatalf("newEvolutionBridge() error = %v", err)
	}
	if coldBridge == nil || coldBridge.coldPathRunner == nil {
		t.Fatal("draft evolution bridge has no cold-path runner")
	}
	coldBridge.runtime = coldRuntime
	t.Cleanup(func() { _ = coldBridge.Close() })
	done := make(chan struct{})
	close(done)
	closeBridge := &evolutionBridge{
		runtimeSub: &p015B2BLifecycleSubscription{err: hostile, done: done},
		bgCtx:      context.Background(),
	}
	scheduleBridge := &evolutionBridge{
		cfg:            config.EvolutionConfig{},
		coldPathRunner: &evolution.ColdPathRunner{},
		bgCtx:          context.Background(),
	}

	var admitted bool
	var closeErr error
	records, raw := captureP015HookRecords(t, func() {
		admitted = bridge.handleTurnEndAsync(
			EventMeta{TurnID: turnCanary, SessionKey: "session"},
			TurnEndPayload{
				Status:      TurnEndStatusCompleted,
				Workspace:   workspaceCanary,
				UserMessage: "functional input",
			},
		)
		bridge.wg.Wait()
		closeErr = closeBridge.Close()
		scheduleBridge.startScheduledColdPath(workspaceCanary, []string{scheduleCanary})
		if !coldBridge.coldPathRunner.Trigger(workspaceCanary) {
			t.Error("cold-path trigger was rejected")
		}
		if err := coldBridge.coldPathRunner.Close(); err != nil {
			t.Errorf("close cold-path runner: %v", err)
		}
	})
	if !admitted || runtime.calls != 1 {
		t.Fatalf("finalize admission/calls = %v/%d, want true/1", admitted, runtime.calls)
	}
	if runtime.input.TurnID != turnCanary || runtime.input.Workspace != workspaceCanary ||
		runtime.input.UserMessage != "functional input" {
		t.Fatalf("FinalizeTurn input lost functional values: %#v", runtime.input)
	}
	if closeErr != nil {
		t.Fatalf("Close() error = %v, want cleanup to remain best-effort", closeErr)
	}
	if scheduleBridge.scheduleActive {
		t.Fatal("invalid schedule unexpectedly activated the scheduler")
	}
	if coldRuntime.coldCalls != 1 {
		t.Fatalf("cold-path calls = %d, want 1", coldRuntime.coldCalls)
	}
	if methodCalls.Load() != 0 {
		t.Fatalf("evolution diagnostics invoked %d hostile error methods", methodCalls.Load())
	}
	finalizeRecord := p015B2BLifecycleRequireRecord(t, records, "Evolution finalize turn failed")
	p015B2BLifecycleRequireObservation(
		t,
		finalizeRecord,
		"identity_turn_digest",
		logger.ObserveIdentity(logger.ObservationDomainIdentityTurn, turnCanary).Digest,
	)
	p015B2BLifecycleRequireObservation(
		t,
		finalizeRecord,
		"identity_workspace_digest",
		logger.ObserveIdentity(logger.ObservationDomainIdentityWorkspace, workspaceCanary).Digest,
	)
	p015B2BLifecycleRequireRecord(t, records, "Failed to close evolution runtime subscription")
	p015B2BLifecycleRequireRecord(t, records, "Cold path run failed")
	scheduleRecord := p015B2BLifecycleRequireRecord(
		t,
		records,
		"No valid evolution cold path schedule times configured",
	)
	if scheduleRecord["count"] != float64(1) {
		t.Fatalf("invalid schedule count = %#v, want 1", scheduleRecord["count"])
	}
	assertP015CanariesAbsent(
		t,
		raw,
		turnCanary,
		workspaceCanary,
		scheduleCanary,
		p015B2BLifecycleErrorCanary,
	)
}

func TestP015B2BLifecycleLegacyUnexpectedTypeIsRemovedWithoutIntrospection(t *testing.T) {
	var methodCalls atomic.Int64
	id := legacyEventSubSeq.Add(1)
	legacyEventSubLock.Store(id, &p015B2BLifecycleUnexpectedEntryError{calls: &methodCalls})
	t.Cleanup(func() { legacyEventSubLock.Delete(id) })

	loop := &AgentLoop{}
	records, raw := captureP015HookRecords(t, func() {
		loop.UnsubscribeEvents(id)
	})
	if _, exists := legacyEventSubLock.Load(id); exists {
		t.Fatal("unexpected legacy subscription value was not removed")
	}
	if methodCalls.Load() != 0 {
		t.Fatalf("legacy diagnostics invoked %d hostile methods", methodCalls.Load())
	}
	p015B2BLifecycleRequireRecord(
		t,
		records,
		"UnsubscribeEvents: unexpected type in subscription map",
	)
	assertP015CanariesAbsent(t, raw, p015B2BLifecycleErrorCanary)
}

func p015B2BLifecycleRequireRecord(
	t *testing.T,
	records []map[string]any,
	message string,
) map[string]any {
	t.Helper()
	var matches []map[string]any
	for _, record := range records {
		if record["message"] == message {
			matches = append(matches, record)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("records for %q = %d, want exactly 1; records=%#v", message, len(matches), records)
	}
	if _, invalid := matches[0]["safe_fields_state"]; invalid {
		t.Fatalf("record for %q rejected safe fields: %#v", message, matches[0])
	}
	return matches[0]
}

func p015B2BLifecycleRequireObservation(
	t *testing.T,
	record map[string]any,
	key string,
	want string,
) {
	t.Helper()
	if want == "" || record[key] != want {
		t.Fatalf("record field %s = %#v, want exact nonempty digest %q", key, record[key], want)
	}
}

var (
	_ error                      = (*p015B2BLifecycleHostileError)(nil)
	_ error                      = (*p015B2BLifecycleUnexpectedEntryError)(nil)
	_ runtimeevents.Subscription = (*p015B2BLifecycleSubscription)(nil)
	_ gitWorkspaceManager        = (*p015B2BLifecycleWorkspaceManager)(nil)
	_ session.SnapshotReader     = (*p015B2BLifecycleSnapshotStore)(nil)
)
