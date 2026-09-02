package agent

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/audio/asr"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/internal/sessiondb"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
	threadstore "github.com/sipeed/picoclaw/pkg/threads"
	agenttools "github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type reviewCommandGuardProvider struct {
	calls atomic.Int32
}

func (p *reviewCommandGuardProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	p.calls.Add(1)
	return &providers.LLMResponse{Content: "must not run"}, nil
}

type reviewCommandGuardContextManager struct {
	assembleCalls atomic.Int32
}

func (m *reviewCommandGuardContextManager) Assemble(
	context.Context,
	*AssembleRequest,
) (*AssembleResponse, error) {
	m.assembleCalls.Add(1)
	return &AssembleResponse{}, nil
}

func (*reviewCommandGuardContextManager) Compact(context.Context, *CompactRequest) error {
	return nil
}

func (*reviewCommandGuardContextManager) Ingest(context.Context, *IngestRequest) error {
	return nil
}

func (*reviewCommandGuardContextManager) Clear(context.Context, string) error {
	return nil
}

type reviewMessageResetSpy struct {
	resets atomic.Int32
}

type reviewTranscriberSpy struct {
	calls atomic.Int32
}

type reviewWorkflowToolSpy struct {
	calls atomic.Int32
}

func (*reviewTranscriberSpy) Name() string { return "review-transcriber-spy" }

func (s *reviewTranscriberSpy) Transcribe(
	context.Context,
	string,
) (*asr.TranscriptionResponse, error) {
	s.calls.Add(1)
	return &asr.TranscriptionResponse{Text: "must not transcribe"}, nil
}

func (*reviewWorkflowToolSpy) Name() string { return "review_trigger_spy" }

func (*reviewWorkflowToolSpy) Description() string { return "review trigger spy" }

func (*reviewWorkflowToolSpy) Parameters() map[string]any { return map[string]any{} }

func (s *reviewWorkflowToolSpy) Execute(context.Context, map[string]any) *agenttools.ToolResult {
	s.calls.Add(1)
	return agenttools.NewToolResult("must not execute")
}

func (*reviewMessageResetSpy) Name() string { return "message" }

func (*reviewMessageResetSpy) Description() string { return "review reset spy" }

func (*reviewMessageResetSpy) Parameters() map[string]any { return map[string]any{} }

func (*reviewMessageResetSpy) Execute(context.Context, map[string]any) *agenttools.ToolResult {
	return &agenttools.ToolResult{}
}

func (s *reviewMessageResetSpy) ResetSentInRound(string) {
	s.resets.Add(1)
}

func TestProcessDirectReviewScopedClearLeavesExactSnapshotUnchanged(t *testing.T) {
	loop, runtimeAgent, reviewKey, before, _ := newReviewCommandGuardLoop(t)

	_, err := loop.ProcessDirect(context.Background(), "/clear", reviewKey)
	if err == nil || !strings.Contains(err.Error(), "review-scoped sessions do not accept live turns") {
		t.Fatalf("ProcessDirect(/clear) error = %v, want review-session rejection", err)
	}

	after := readReviewCommandGuardSnapshot(t, runtimeAgent, reviewKey)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("ProcessDirect(/clear) mutated review snapshot:\nbefore: %#v\nafter:  %#v", before, after)
	}
}

func TestProcessDirectWithChannelReviewScopedBtwSkipsHistoryAndProvider(t *testing.T) {
	loop, runtimeAgent, reviewKey, before, provider := newReviewCommandGuardLoop(t)
	loop.contextManager = &reviewCommandGuardContextManager{}
	manager := loop.contextManager.(*reviewCommandGuardContextManager)
	useTestSideQuestionProvider(loop, provider)

	_, err := loop.ProcessDirectWithChannel(
		context.Background(),
		"/btw disclose the review transcript",
		reviewKey,
		"cli",
		"direct",
	)
	if err == nil || !strings.Contains(err.Error(), "review-scoped sessions do not accept live turns") {
		t.Fatalf("ProcessDirectWithChannel(/btw) error = %v, want review-session rejection", err)
	}
	if calls := manager.assembleCalls.Load(); calls != 0 {
		t.Fatalf("ProcessDirectWithChannel(/btw) assembled history %d time(s), want 0", calls)
	}
	if calls := provider.calls.Load(); calls != 0 {
		t.Fatalf("ProcessDirectWithChannel(/btw) called provider %d time(s), want 0", calls)
	}

	after := readReviewCommandGuardSnapshot(t, runtimeAgent, reviewKey)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("ProcessDirectWithChannel(/btw) mutated review snapshot:\nbefore: %#v\nafter:  %#v", before, after)
	}
}

func TestProcessMessageReviewScopeRejectsBeforeAudioPreparation(t *testing.T) {
	loop, runtimeAgent, reviewKey, before, provider := newReviewCommandGuardLoop(t)
	transcriber := &reviewTranscriberSpy{}
	mediaStore := media.NewFileMediaStore()
	audioPath := filepath.Join(t.TempDir(), "private-review.ogg")
	if err := os.WriteFile(audioPath, []byte("private review audio"), 0o600); err != nil {
		t.Fatalf("write audio fixture: %v", err)
	}
	ref, err := mediaStore.Store(audioPath, media.MediaMeta{
		Filename:      "private-review.ogg",
		ContentType:   "audio/ogg",
		CleanupPolicy: media.CleanupPolicyForgetOnly,
	}, "review-preparation")
	if err != nil {
		t.Fatalf("store audio fixture: %v", err)
	}
	loop.SetMediaStore(mediaStore)
	loop.SetTranscriber(transcriber)

	_, err = loop.processMessage(t.Context(), bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  "cli",
			ChatID:   "direct",
			ChatType: "direct",
			SenderID: "user",
		},
		Content:    "[audio]",
		Media:      []string{ref},
		SessionKey: reviewKey,
	})
	if err == nil || !strings.Contains(err.Error(), "review-scoped sessions do not accept live turns") {
		t.Fatalf("processMessage(audio) error = %v, want review-session rejection", err)
	}
	if calls := transcriber.calls.Load(); calls != 0 {
		t.Fatalf("rejected review audio was transcribed %d time(s), want 0", calls)
	}
	if calls := provider.calls.Load(); calls != 0 {
		t.Fatalf("rejected review audio called provider %d time(s), want 0", calls)
	}
	after := readReviewCommandGuardSnapshot(t, runtimeAgent, reviewKey)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected review audio mutated exact snapshot:\nbefore: %#v\nafter:  %#v", before, after)
	}
}

func TestProcessMessageReviewOriginCannotRedirectToOrdinaryThread(t *testing.T) {
	loop, runtimeAgent, reviewKey, _, provider := newReviewCommandGuardLoop(t)
	targetScope := session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    runtimeAgent.ID,
		Channel:    "cli",
		Account:    "default",
		Dimensions: []string{"chat"},
		Values:     map[string]string{"chat": "ordinary-thread-target"},
	}
	targetKey := session.BuildSessionKey(targetScope)
	replacer := runtimeAgent.Sessions.(session.SnapshotReplacer)
	if err := replacer.ReplaceSessionSnapshot(t.Context(), session.SessionSnapshotReplacement{
		Key:     targetKey,
		History: []providers.Message{{Role: "user", Content: "ordinary target history"}},
		Scope:   &targetScope,
	}); err != nil {
		t.Fatalf("seed ordinary thread target: %v", err)
	}
	seedReviewGuardThreadRedirect(t, runtimeAgent, reviewKey, targetKey, "review-origin-redirect")
	reviewBefore := readReviewCommandGuardSnapshot(t, runtimeAgent, reviewKey)
	targetBefore := readReviewCommandGuardSnapshot(t, runtimeAgent, targetKey)

	_, err := loop.processMessage(t.Context(), bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  "cli",
			ChatID:   "direct",
			ChatType: "direct",
			SenderID: "user",
		},
		Content:    "follow stale thread link",
		SessionKey: reviewKey,
	})
	if err == nil || !strings.Contains(err.Error(), "review-scoped sessions do not accept live turns") {
		t.Fatalf("processMessage(review redirect) error = %v, want rejection", err)
	}
	if calls := provider.calls.Load(); calls != 0 {
		t.Fatalf("review redirect called provider %d time(s), want 0", calls)
	}
	if after := readReviewCommandGuardSnapshot(t, runtimeAgent, reviewKey); !reflect.DeepEqual(after, reviewBefore) {
		t.Fatalf("review redirect mutated origin:\nbefore=%#v\nafter=%#v", reviewBefore, after)
	}
	if after := readReviewCommandGuardSnapshot(t, runtimeAgent, targetKey); !reflect.DeepEqual(after, targetBefore) {
		t.Fatalf("review redirect mutated target:\nbefore=%#v\nafter=%#v", targetBefore, after)
	}
}

func TestAgentLoopReviewScopeRejectsBeforeWorkflowTrigger(t *testing.T) {
	loop, runtimeAgent, reviewKey, before, provider := newReviewCommandGuardLoop(t)
	toolSpy := &reviewWorkflowToolSpy{}
	runtimeAgent.Tools.Register(toolSpy)
	loop.cfg.Workflows.Enabled = true
	workflowDir := filepath.Join(loop.cfg.WorkspacePath(), workflows.DefaultDefinitionsDir)
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatalf("create workflow dir: %v", err)
	}
	definition := []byte(`
name: Protected review trigger guard
on:
  channel_message:
    channels: cli
    passthrough: false
jobs:
  guard:
    runs-on: picoclaw
    steps:
      - uses: tool/review_trigger_spy
`)
	if err := os.WriteFile(filepath.Join(workflowDir, "review-guard.yml"), definition, 0o600); err != nil {
		t.Fatalf("write workflow definition: %v", err)
	}
	if _, err := workflows.RevalidateLocal(
		t.Context(),
		loop.cfg.WorkspacePath(),
		workflowRuntimeCompatibility(),
		workflows.WithDefinitionsDir(workflows.DefaultDefinitionsDir),
	); err != nil {
		t.Fatalf("revalidate workflow definition: %v", err)
	}

	runCtx, cancelRun := context.WithCancel(t.Context())
	runDone := make(chan error, 1)
	go func() { runDone <- loop.Run(runCtx) }()
	t.Cleanup(func() {
		cancelRun()
		loop.Stop()
		select {
		case <-runDone:
		case <-time.After(2 * time.Second):
		}
	})
	deadline := time.Now().Add(2 * time.Second)
	for !loop.running.Load() {
		if time.Now().After(deadline) {
			t.Fatal("agent loop did not start")
		}
		time.Sleep(time.Millisecond)
	}

	originKey := session.BuildOpaqueSessionKey("ordinary-workflow-review-target")
	seedReviewGuardThreadRedirect(t, runtimeAgent, originKey, reviewKey, "workflow-review-target")
	if err := loop.bus.PublishInbound(t.Context(), bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  "cli",
			ChatID:   "direct",
			ChatType: "direct",
			SenderID: "user",
		},
		Content:    "run protected trigger",
		SessionKey: originKey,
	}); err != nil {
		t.Fatalf("publish protected workflow trigger: %v", err)
	}
	messageBus, ok := loop.bus.(*bus.MessageBus)
	if !ok {
		t.Fatalf("agent bus = %T, want *bus.MessageBus", loop.bus)
	}
	select {
	case outbound := <-messageBus.OutboundChan():
		if !strings.Contains(outbound.Content, "review-scoped sessions do not accept live turns") &&
			!strings.Contains(outbound.Content, session.ErrScopeAdmissionConflict.Error()) {
			t.Fatalf("workflow-trigger rejection = %q", outbound.Content)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for protected workflow-trigger rejection")
	}
	time.Sleep(100 * time.Millisecond)
	if calls := toolSpy.calls.Load(); calls != 0 {
		t.Fatalf("protected workflow trigger executed tool %d time(s), want 0", calls)
	}
	if calls := provider.calls.Load(); calls != 0 {
		t.Fatalf("protected workflow trigger called provider %d time(s), want 0", calls)
	}
	after := readReviewCommandGuardSnapshot(t, runtimeAgent, reviewKey)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("protected workflow trigger mutated exact snapshot:\nbefore: %#v\nafter:  %#v", before, after)
	}
}

func seedReviewGuardThreadRedirect(
	t *testing.T,
	agent *AgentInstance,
	originKey,
	targetKey,
	threadID string,
) {
	t.Helper()
	lower, openErr := memory.NewJSONLStore(threadstore.ResolveSessionsDir(agent.Workspace))
	if openErr != nil {
		t.Fatalf("open session metadata store: %v", openErr)
	}
	t.Cleanup(func() { _ = lower.Close() })
	if err := lower.UpdateSessionMeta(t.Context(), originKey, func(meta *memory.SessionMeta) error {
		meta.ThreadID = threadID
		meta.ThreadAttachedAt = time.Now().UTC()
		return nil
	}); err != nil {
		t.Fatalf("seed thread origin link: %v", err)
	}
	now := time.Now().UTC()
	seconds, nanos := now.Unix(), now.Nanosecond()
	if err := sessiondb.Bind(lower.ThreadStore()).
		Immediate(t.Context(), func(ctx context.Context, conn *sql.Conn) error {
			if _, err := conn.ExecContext(ctx, `INSERT INTO threads (
            thread_id, ui_session_id, primary_session_key, agent_id, owner_identity,
            title, thread_type, source_query, registration, created_seconds,
            created_nanos, updated_seconds, updated_nanos, version
        ) VALUES (?, ?, ?, 'main', 'test', 'review redirect', 'general', '',
            'manual', ?, ?, ?, ?, 1)`, threadID, threadID, targetKey,
				seconds, nanos, seconds, nanos); err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, `INSERT INTO thread_sessions (
            thread_id, sequence, session_key, is_primary
        ) VALUES (?, 0, ?, 1), (?, 1, ?, 0)`,
				threadID, targetKey, threadID, originKey); err != nil {
				return err
			}
			_, err := conn.ExecContext(ctx, `INSERT INTO session_thread_links (
            session_key, thread_id, attached_seconds, attached_nanos
        ) VALUES (?, ?, ?, ?)`, originKey, threadID, seconds, nanos)
			return err
		}); err != nil {
		t.Fatalf("seed typed thread redirect: %v", err)
	}
}

func TestAgentLoopAsyncStopRejectsReviewSessionBeforeAnyMutation(t *testing.T) {
	loop, runtimeAgent, reviewKey, before, provider := newReviewCommandGuardLoop(t)
	resetSpy := &reviewMessageResetSpy{}
	runtimeAgent.Tools.Register(resetSpy)

	active := &turnState{
		turnID:               "protected-review-turn",
		agentID:              runtimeAgent.ID,
		sessionKey:           reviewKey,
		userMessage:          "protected review work",
		session:              runtimeAgent.Sessions,
		initialHistoryLength: len(before.History),
		phase:                TurnPhaseRunning,
	}
	loop.activeTurnStates.Store(reviewKey, active)
	t.Cleanup(func() { loop.activeTurnStates.CompareAndDelete(reviewKey, active) })
	if _, _, err := loop.pushSteeringMessage(reviewKey, providers.Message{
		Role:    "user",
		Content: "must remain queued",
	}); err != nil {
		t.Fatalf("seed protected steering queue: %v", err)
	}
	loop.setPendingSkills(reviewKey, []string{"must-remain-armed"})

	runCtx, cancelRun := context.WithCancel(t.Context())
	runDone := make(chan error, 1)
	go func() { runDone <- loop.Run(runCtx) }()
	t.Cleanup(func() {
		cancelRun()
		loop.Stop()
		select {
		case <-runDone:
		case <-time.After(2 * time.Second):
		}
	})
	deadline := time.Now().Add(2 * time.Second)
	for !loop.running.Load() {
		if time.Now().After(deadline) {
			t.Fatal("agent loop did not start")
		}
		time.Sleep(time.Millisecond)
	}

	stopMessage := bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  "cli",
			ChatID:   "direct",
			ChatType: "direct",
			SenderID: "user",
		},
		Content:    "/stop",
		SessionKey: reviewKey,
	}
	if err := loop.bus.PublishInbound(t.Context(), stopMessage); err != nil {
		t.Fatalf("publish protected /stop: %v", err)
	}
	messageBus, ok := loop.bus.(*bus.MessageBus)
	if !ok {
		t.Fatalf("agent bus = %T, want *bus.MessageBus", loop.bus)
	}
	select {
	case outbound := <-messageBus.OutboundChan():
		if !strings.Contains(outbound.Content, "review-scoped sessions do not accept live turns") &&
			!strings.Contains(outbound.Content, session.ErrScopeAdmissionConflict.Error()) {
			t.Fatalf("/stop rejection = %q, want review admission rejection", outbound.Content)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for protected /stop rejection")
	}

	if active.hardAbortRequested() {
		t.Fatal("rejected /stop hard-aborted the protected review turn")
	}
	if got := loop.getActiveTurnState(reviewKey); got != active {
		t.Fatalf("active review turn = %p, want unchanged %p", got, active)
	}
	if depth := loop.pendingSteeringCountForScope(reviewKey); depth != 1 {
		t.Fatalf("protected steering queue depth = %d, want 1", depth)
	}
	pending, ok := loop.pendingSkills.Load(reviewKey)
	if !ok || !reflect.DeepEqual(pending, []string{"must-remain-armed"}) {
		t.Fatalf("protected pending skills = %#v (found=%v), want unchanged", pending, ok)
	}
	if _, pendingStop := loop.pendingStops.Load(reviewKey); pendingStop {
		t.Fatal("rejected /stop armed a pending stop")
	}
	if resets := resetSpy.resets.Load(); resets != 0 {
		t.Fatalf("rejected /stop reset message-tool state %d time(s), want 0", resets)
	}
	if calls := provider.calls.Load(); calls != 0 {
		t.Fatalf("rejected /stop called provider %d time(s), want 0", calls)
	}
	after := readReviewCommandGuardSnapshot(t, runtimeAgent, reviewKey)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected /stop mutated exact review snapshot:\nbefore: %#v\nafter:  %#v", before, after)
	}
}

func TestContinueRejectsReviewSessionBeforeQueueOrMessageResetMutation(t *testing.T) {
	loop, runtimeAgent, reviewKey, before, provider := newReviewCommandGuardLoop(t)
	resetSpy := &reviewMessageResetSpy{}
	runtimeAgent.Tools.Register(resetSpy)
	if _, _, err := loop.pushSteeringMessage(reviewKey, providers.Message{
		Role:    "user",
		Content: "private queued continuation",
	}); err != nil {
		t.Fatalf("seed protected steering continuation: %v", err)
	}

	_, err := loop.Continue(t.Context(), reviewKey, "cli", "direct")
	if err == nil || !strings.Contains(err.Error(), "review-scoped sessions do not accept live turns") {
		t.Fatalf("Continue() error = %v, want review-session rejection", err)
	}
	if depth := loop.pendingSteeringCountForScope(reviewKey); depth != 1 {
		t.Fatalf("protected continuation queue depth = %d, want 1", depth)
	}
	if active := loop.getActiveTurnState(reviewKey); active != nil {
		t.Fatalf("Continue() published active state %#v before review rejection", active.snapshot())
	}
	if resets := resetSpy.resets.Load(); resets != 0 {
		t.Fatalf("rejected continuation reset message-tool state %d time(s), want 0", resets)
	}
	if calls := provider.calls.Load(); calls != 0 {
		t.Fatalf("rejected continuation called provider %d time(s), want 0", calls)
	}
	after := readReviewCommandGuardSnapshot(t, runtimeAgent, reviewKey)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected continuation mutated exact review snapshot:\nbefore: %#v\nafter:  %#v", before, after)
	}
}

func newReviewCommandGuardLoop(
	t *testing.T,
) (*AgentLoop, *AgentInstance, string, session.SessionSnapshot, *reviewCommandGuardProvider) {
	t.Helper()

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ModelName = "test-model"
	messageBus := bus.NewMessageBus()
	provider := &reviewCommandGuardProvider{}
	loop := newTestAgentLoopWithStrictModels(cfg, messageBus, provider)
	t.Cleanup(func() {
		loop.Stop()
		messageBus.Close()
		loop.Close()
	})

	runtimeAgent := loop.GetRegistry().GetDefaultAgent()
	if runtimeAgent == nil {
		t.Fatal("default runtime agent is unavailable")
	}
	reviewScope := session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    runtimeAgent.ID,
		Channel:    "review",
		Account:    "default",
		Dimensions: []string{"review"},
		Values: map[string]string{
			"review": "prc_22222222222222222222222222222222",
		},
	}
	reviewKey := session.BuildSessionKey(reviewScope)
	replacer, ok := runtimeAgent.Sessions.(session.SnapshotReplacer)
	if !ok {
		t.Fatal("runtime session store lacks exact replacement")
	}
	if err := replacer.ReplaceSessionSnapshot(context.Background(), session.SessionSnapshotReplacement{
		Key:     reviewKey,
		History: []providers.Message{{Role: "user", Content: "private review transcript"}},
		Summary: "private review summary",
		Scope:   &reviewScope,
		Aliases: []string{
			"review:agent:main:case:prc_22222222222222222222222222222222",
			"review:agent:main:case:prc_22222222222222222222222222222222:binding:test",
			"review:agent:main:case:prc_22222222222222222222222222222222:version:1",
		},
	}); err != nil {
		t.Fatalf("seed review session: %v", err)
	}

	return loop,
		runtimeAgent,
		reviewKey,
		readReviewCommandGuardSnapshot(t, runtimeAgent, reviewKey),
		provider
}

func readReviewCommandGuardSnapshot(
	t *testing.T,
	runtimeAgent *AgentInstance,
	reviewKey string,
) session.SessionSnapshot {
	t.Helper()

	reader, ok := runtimeAgent.Sessions.(session.SnapshotReader)
	if !ok {
		t.Fatal("runtime session store lacks exact reads")
	}
	snapshot, found, err := reader.ReadSessionSnapshot(context.Background(), reviewKey)
	if err != nil || !found {
		t.Fatalf("read review session = (found=%v, err=%v)", found, err)
	}
	return snapshot
}
