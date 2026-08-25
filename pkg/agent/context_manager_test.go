package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
)

// ---------------------------------------------------------------------------
// Factory registry tests
// ---------------------------------------------------------------------------

func TestRegisterContextManager_Success(t *testing.T) {
	cleanup := resetCMRegistry()
	defer cleanup()

	factory := func(cfg json.RawMessage, al *AgentLoop) (ContextManager, error) {
		return &noopContextManager{}, nil
	}
	if err := RegisterContextManager("test_cm", factory); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	f, ok := lookupContextManager("test_cm")
	if !ok {
		t.Fatal("expected factory to be registered")
	}
	if f == nil {
		t.Fatal("expected non-nil factory")
	}
}

func TestRegisterContextManager_EmptyName(t *testing.T) {
	cleanup := resetCMRegistry()
	defer cleanup()

	err := RegisterContextManager("", func(cfg json.RawMessage, al *AgentLoop) (ContextManager, error) {
		return &noopContextManager{}, nil
	})
	if err == nil {
		t.Fatal("expected error for empty name")
	}
	if !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRegisterContextManager_NilFactory(t *testing.T) {
	cleanup := resetCMRegistry()
	defer cleanup()

	err := RegisterContextManager("nil_factory", nil)
	if err == nil {
		t.Fatal("expected error for nil factory")
	}
	if !strings.Contains(err.Error(), "factory is nil") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRegisterContextManager_Duplicate(t *testing.T) {
	cleanup := resetCMRegistry()
	defer cleanup()

	factory := func(cfg json.RawMessage, al *AgentLoop) (ContextManager, error) {
		return &noopContextManager{}, nil
	}
	if err := RegisterContextManager("dup_cm", factory); err != nil {
		t.Fatalf("first registration failed: %v", err)
	}
	err := RegisterContextManager("dup_cm", factory)
	if err == nil {
		t.Fatal("expected error for duplicate registration")
	}
	if !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLookupContextManager_Unknown(t *testing.T) {
	cleanup := resetCMRegistry()
	defer cleanup()

	_, ok := lookupContextManager("nonexistent")
	if ok {
		t.Fatal("expected lookup to fail for unknown name")
	}
}

// ---------------------------------------------------------------------------
// resolveContextManager tests
// ---------------------------------------------------------------------------

func TestResolveContextManager_Default(t *testing.T) {
	cleanup := resetCMRegistry()
	defer cleanup()

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         t.TempDir(),
				ModelName:         "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
				ContextManager:    "", // default → legacy
			},
		},
	}
	al := newCMTestAgentLoop(cfg)

	cm := al.contextManager
	if cm == nil {
		t.Fatal("expected non-nil context manager")
	}
	if _, ok := cm.(*legacyContextManager); !ok {
		t.Fatalf("expected *legacyContextManager, got %T", cm)
	}
}

func TestResolveContextManager_ExplicitLegacy(t *testing.T) {
	cleanup := resetCMRegistry()
	defer cleanup()

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         t.TempDir(),
				ModelName:         "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
				ContextManager:    "legacy",
			},
		},
	}
	al := newCMTestAgentLoop(cfg)

	if _, ok := al.contextManager.(*legacyContextManager); !ok {
		t.Fatalf("expected *legacyContextManager, got %T", al.contextManager)
	}
}

func TestResolveContextManager_UnknownFallsBackToLegacy(t *testing.T) {
	cleanup := resetCMRegistry()
	defer cleanup()

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         t.TempDir(),
				ModelName:         "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
				ContextManager:    "unknown_cm",
			},
		},
	}
	al := newCMTestAgentLoop(cfg)

	if _, ok := al.contextManager.(*legacyContextManager); !ok {
		t.Fatalf("expected fallback to *legacyContextManager, got %T", al.contextManager)
	}
}

func TestResolveContextManager_RegisteredFactory(t *testing.T) {
	cleanup := resetCMRegistry()
	defer cleanup()

	factory := func(cfg json.RawMessage, al *AgentLoop) (ContextManager, error) {
		return &noopContextManager{}, nil
	}
	if err := RegisterContextManager("custom_cm", factory); err != nil {
		t.Fatalf("register failed: %v", err)
	}

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         t.TempDir(),
				ModelName:         "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
				ContextManager:    "custom_cm",
			},
		},
	}
	al := newCMTestAgentLoop(cfg)

	if _, ok := al.contextManager.(*noopContextManager); !ok {
		t.Fatalf("expected *noopContextManager, got %T", al.contextManager)
	}
}

func TestResolveContextManager_FactoryError(t *testing.T) {
	cleanup := resetCMRegistry()
	defer cleanup()

	factory := func(cfg json.RawMessage, al *AgentLoop) (ContextManager, error) {
		return nil, os.ErrPermission
	}
	if err := RegisterContextManager("broken_cm", factory); err != nil {
		t.Fatalf("register failed: %v", err)
	}

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         t.TempDir(),
				ModelName:         "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
				ContextManager:    "broken_cm",
			},
		},
	}
	al := newCMTestAgentLoop(cfg)

	// Should fall back to legacy when factory returns error
	if _, ok := al.contextManager.(*legacyContextManager); !ok {
		t.Fatalf("expected fallback to *legacyContextManager on factory error, got %T", al.contextManager)
	}
}

// ---------------------------------------------------------------------------
// Legacy Assemble tests
// ---------------------------------------------------------------------------

func TestLegacyAssemble_Passthrough(t *testing.T) {
	cfg := testConfig(t)
	al := newCMTestAgentLoop(cfg)

	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("expected default agent")
	}

	history := []providers.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
	}
	agent.Sessions.SetHistory("test-session", history)

	resp, err := al.contextManager.Assemble(context.Background(), &AssembleRequest{
		SessionKey: "test-session",
		Budget:     8000,
		MaxTokens:  4096,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.History) != len(history) {
		t.Fatalf("expected %d messages, got %d", len(history), len(resp.History))
	}
	for i, msg := range resp.History {
		if msg.Content != history[i].Content || msg.Role != history[i].Role {
			t.Fatalf("message %d mismatch: want %+v, got %+v", i, history[i], msg)
		}
	}
}

func TestLegacyAssemble_EmptyHistory(t *testing.T) {
	cfg := testConfig(t)
	al := newCMTestAgentLoop(cfg)

	resp, err := al.contextManager.Assemble(context.Background(), &AssembleRequest{
		SessionKey: "test-session",
		Budget:     8000,
		MaxTokens:  4096,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.History) != 0 {
		t.Fatalf("expected empty messages, got %d", len(resp.History))
	}
}

func TestLegacyContext_UnownedExplicitSessionFailsClosed(t *testing.T) {
	tests := map[string]string{
		"opaque":               session.BuildOpaqueSessionKey("unowned-legacy-context"),
		"removed legacy agent": "agent:removed:test:direct:unowned",
	}
	for name, sessionKey := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.Agents.Defaults.ContextWindow = 8000
			cfg.Agents.Defaults.SummarizeMessageThreshold = 2
			al := newCMTestAgentLoop(cfg)
			defer al.Close()

			defaultAgent := al.registry.GetDefaultAgent()
			if defaultAgent == nil {
				t.Fatal("expected default agent")
			}
			defaultHistory := legacyContextTestHistory("default-private")
			defaultAgent.Sessions.SetHistory(sessionKey, defaultHistory)
			defaultAgent.Sessions.SetSummary(sessionKey, "default-private-summary")
			defaultHistoryBefore := defaultAgent.Sessions.GetHistory(sessionKey)

			resp, err := al.contextManager.Assemble(t.Context(), &AssembleRequest{
				SessionKey: sessionKey,
				Budget:     8000,
				MaxTokens:  4096,
			})
			if err != nil {
				t.Fatalf("Assemble() error = %v", err)
			}
			if resp == nil || len(resp.History) != 0 || resp.Summary != "" {
				t.Fatalf("Assemble() = %#v, want empty context for unowned explicit session", resp)
			}

			for _, reason := range []ContextCompressReason{
				ContextCompressReasonRetry,
				ContextCompressReasonSummarize,
			} {
				if err := al.contextManager.Compact(t.Context(), &CompactRequest{
					SessionKey: sessionKey,
					Reason:     reason,
				}); err != nil {
					t.Fatalf("Compact(%q) error = %v", reason, err)
				}
			}
			time.Sleep(100 * time.Millisecond)
			if err := al.contextManager.Clear(t.Context(), sessionKey); err == nil {
				t.Fatal("Clear() succeeded for unowned explicit session")
			}
			if got := defaultAgent.Sessions.GetHistory(sessionKey); !reflect.DeepEqual(got, defaultHistoryBefore) {
				t.Fatalf("default history mutated: %#v", got)
			}
			if got := defaultAgent.Sessions.GetSummary(sessionKey); got != "default-private-summary" {
				t.Fatalf("default summary mutated: %q", got)
			}
		})
	}
}

func TestLegacyAssemble_NamedAgentUsesOwningSessionStore(t *testing.T) {
	defaultProvider := &contextCompletionCaptureProvider{response: "default"}
	namedProvider := &contextCompletionCaptureProvider{response: "named"}
	al, defaultAgent, namedAgent := newNamedLegacyContextTestLoop(
		t,
		defaultProvider,
		namedProvider,
	)
	defer al.Close()

	sessionKey := admitLegacyContextTestSession(t, namedAgent, "named-assemble")
	defaultAgent.Sessions.SetHistory(sessionKey, []providers.Message{{
		Role: "user", Content: "default-store-history",
	}})
	defaultAgent.Sessions.SetSummary(sessionKey, "default-store-summary")
	namedHistory := []providers.Message{
		{Role: "user", Content: "named-store-history"},
		{Role: "assistant", Content: "named-store-answer"},
	}
	namedAgent.Sessions.SetHistory(sessionKey, namedHistory)
	namedAgent.Sessions.SetSummary(sessionKey, "named-store-summary")

	resp, err := al.contextManager.Assemble(t.Context(), &AssembleRequest{
		SessionKey: sessionKey,
		Budget:     8000,
		MaxTokens:  4096,
	})
	if err != nil {
		t.Fatalf("Assemble() error = %v", err)
	}
	if !reflect.DeepEqual(resp.History, namedAgent.Sessions.GetHistory(sessionKey)) {
		t.Fatalf("Assemble() history = %#v, want named history %#v", resp.History, namedHistory)
	}
	if len(resp.History) != len(namedHistory) {
		t.Fatalf("Assemble() history length = %d, want %d", len(resp.History), len(namedHistory))
	}
	for i := range namedHistory {
		if resp.History[i].Role != namedHistory[i].Role ||
			resp.History[i].Content != namedHistory[i].Content {
			t.Fatalf("Assemble() history[%d] = %#v, want role/content %#v", i, resp.History[i], namedHistory[i])
		}
	}
	if resp.Summary != "named-store-summary" {
		t.Fatalf("Assemble() summary = %q, want named-store-summary", resp.Summary)
	}
}

// ---------------------------------------------------------------------------
// Legacy Compact overflow tests
// ---------------------------------------------------------------------------

func TestLegacyCompact_Overflow(t *testing.T) {
	cfg := testConfig(t)
	al := newCMTestAgentLoop(cfg)

	defaultAgent := al.registry.GetDefaultAgent()
	if defaultAgent == nil {
		t.Fatal("expected default agent")
	}

	history := []providers.Message{
		{Role: "user", Content: "msg 1"},
		{Role: "assistant", Content: "resp 1"},
		{Role: "user", Content: "msg 2"},
		{Role: "assistant", Content: "resp 2"},
		{Role: "user", Content: "msg 3"},
	}
	defaultAgent.Sessions.SetHistory("session-overflow", history)

	runtimeCh, closeRuntimeEvents := subscribeRuntimeEventsForTest(
		t,
		al,
		16,
		runtimeevents.KindAgentContextCompress,
	)
	defer closeRuntimeEvents()

	err := al.contextManager.Compact(context.Background(), &CompactRequest{
		SessionKey: "session-overflow",
		Reason:     ContextCompressReasonRetry,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// After overflow compression, history should be shorter
	newHistory := defaultAgent.Sessions.GetHistory("session-overflow")
	if len(newHistory) >= len(history) {
		t.Fatalf("expected compressed history, got %d messages (was %d)", len(newHistory), len(history))
	}

	// Summary should contain compression note
	summary := defaultAgent.Sessions.GetSummary("session-overflow")
	if !strings.Contains(summary, "Emergency compression") {
		t.Fatalf("expected compression note in summary, got %q", summary)
	}

	// Event should carry the proactive reason
	events := collectRuntimeEventStream(runtimeCh)
	compressEvt, ok := findRuntimeEvent(events, runtimeevents.KindAgentContextCompress)
	if !ok {
		t.Fatal("expected context compress event")
	}
	payload, ok := compressEvt.Payload.(ContextCompressPayload)
	if !ok {
		t.Fatalf("expected ContextCompressPayload, got %T", compressEvt.Payload)
	}
	if payload.Reason != ContextCompressReasonRetry {
		t.Fatalf("expected retry reason, got %q", payload.Reason)
	}
}

func TestLegacyCompact_Overflow_ProactiveReason(t *testing.T) {
	cfg := testConfig(t)
	al := newCMTestAgentLoop(cfg)

	defaultAgent := al.registry.GetDefaultAgent()
	if defaultAgent == nil {
		t.Fatal("expected default agent")
	}

	history := []providers.Message{
		{Role: "user", Content: "msg 1"},
		{Role: "assistant", Content: "resp 1"},
		{Role: "user", Content: "msg 2"},
		{Role: "assistant", Content: "resp 2"},
		{Role: "user", Content: "msg 3"},
	}
	defaultAgent.Sessions.SetHistory("session-proactive", history)

	runtimeCh, closeRuntimeEvents := subscribeRuntimeEventsForTest(
		t,
		al,
		16,
		runtimeevents.KindAgentContextCompress,
	)
	defer closeRuntimeEvents()

	err := al.contextManager.Compact(context.Background(), &CompactRequest{
		SessionKey: "session-proactive",
		Reason:     ContextCompressReasonProactive,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	events := collectRuntimeEventStream(runtimeCh)
	compressEvt, ok := findRuntimeEvent(events, runtimeevents.KindAgentContextCompress)
	if !ok {
		t.Fatal("expected context compress event")
	}
	payload, ok := compressEvt.Payload.(ContextCompressPayload)
	if !ok {
		t.Fatalf("expected ContextCompressPayload, got %T", compressEvt.Payload)
	}
	if payload.Reason != ContextCompressReasonProactive {
		t.Fatalf("expected proactive reason, got %q", payload.Reason)
	}
}

func TestLegacyCompact_Overflow_TooShortToCompress(t *testing.T) {
	cfg := testConfig(t)
	al := newCMTestAgentLoop(cfg)

	defaultAgent := al.registry.GetDefaultAgent()
	if defaultAgent == nil {
		t.Fatal("expected default agent")
	}

	history := []providers.Message{
		{Role: "user", Content: "only one"},
	}
	defaultAgent.Sessions.SetHistory("session-tiny", history)

	err := al.contextManager.Compact(context.Background(), &CompactRequest{
		SessionKey: "session-tiny",
		Reason:     ContextCompressReasonRetry,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// History should be unchanged (too short to compress)
	newHistory := defaultAgent.Sessions.GetHistory("session-tiny")
	if len(newHistory) != len(history) {
		t.Fatalf("expected history unchanged, got %d messages (was %d)", len(newHistory), len(history))
	}
}

func TestLegacyCompact_Overflow_NamedAgentDoesNotMutateDefaultStore(t *testing.T) {
	defaultProvider := &contextCompletionCaptureProvider{response: "default"}
	namedProvider := &contextCompletionCaptureProvider{response: "named"}
	al, defaultAgent, namedAgent := newNamedLegacyContextTestLoop(
		t,
		defaultProvider,
		namedProvider,
	)
	defer al.Close()

	sessionKey := admitLegacyContextTestSession(t, namedAgent, "named-overflow")
	defaultAgent.Sessions.SetHistory(sessionKey, legacyContextTestHistory("default-only"))
	defaultAgent.Sessions.SetSummary(sessionKey, "default-summary-before")
	namedAgent.Sessions.SetHistory(sessionKey, legacyContextTestHistory("named-only"))
	namedAgent.Sessions.SetSummary(sessionKey, "named-summary-before")
	defaultHistoryBefore := defaultAgent.Sessions.GetHistory(sessionKey)
	runtimeCh, closeRuntimeEvents := subscribeRuntimeEventsForTest(
		t,
		al,
		4,
		runtimeevents.KindAgentContextCompress,
	)
	defer closeRuntimeEvents()

	if err := al.contextManager.Compact(t.Context(), &CompactRequest{
		SessionKey: sessionKey,
		Reason:     ContextCompressReasonRetry,
	}); err != nil {
		t.Fatalf("Compact() error = %v", err)
	}

	if got := defaultAgent.Sessions.GetHistory(sessionKey); !reflect.DeepEqual(got, defaultHistoryBefore) {
		t.Fatalf("default history mutated: got %#v, want %#v", got, defaultHistoryBefore)
	}
	if got := defaultAgent.Sessions.GetSummary(sessionKey); got != "default-summary-before" {
		t.Fatalf("default summary mutated: got %q", got)
	}
	if got := namedAgent.Sessions.GetHistory(sessionKey); len(got) >= len(legacyContextTestHistory("named-only")) {
		t.Fatalf("named history was not compressed: %#v", got)
	}
	if got := namedAgent.Sessions.GetSummary(sessionKey); !strings.Contains(got, "named-summary-before") ||
		!strings.Contains(got, "Emergency compression") {
		t.Fatalf("named summary = %q, want prior summary plus compression note", got)
	}
	compressEvent := waitForRuntimeEvent(t, runtimeCh, time.Second, func(evt runtimeevents.Event) bool {
		return evt.Kind == runtimeevents.KindAgentContextCompress
	})
	if compressEvent.Scope.AgentID != namedAgent.ID {
		t.Fatalf("compression event agent = %q, want %q", compressEvent.Scope.AgentID, namedAgent.ID)
	}
}

// ---------------------------------------------------------------------------
// Legacy Compact post-turn tests
// ---------------------------------------------------------------------------

func TestLegacyCompact_PostTurn_BelowThreshold(t *testing.T) {
	cfg := testConfig(t)
	al := newCMTestAgentLoop(cfg)

	defaultAgent := al.registry.GetDefaultAgent()
	if defaultAgent == nil {
		t.Fatal("expected default agent")
	}

	// Small history, below summarization thresholds
	history := []providers.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
	}
	defaultAgent.Sessions.SetHistory("session-small", history)

	err := al.contextManager.Compact(context.Background(), &CompactRequest{
		SessionKey: "session-small",
		Reason:     ContextCompressReasonSummarize,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// History should remain unchanged
	newHistory := defaultAgent.Sessions.GetHistory("session-small")
	if len(newHistory) != len(history) {
		t.Fatalf("expected unchanged history, got %d messages (was %d)", len(newHistory), len(history))
	}
}

func TestLegacyCompact_PostTurn_ExceedsMessageThreshold(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:                 t.TempDir(),
				ModelName:                 "test-model",
				MaxTokens:                 4096,
				MaxToolIterations:         10,
				ContextWindow:             8000,
				SummarizeMessageThreshold: 2,
				SummarizeTokenPercent:     75,
			},
		},
	}
	msgBus := bus.NewMessageBus()
	al := newTestAgentLoopWithStrictModels(cfg, msgBus, &simpleMockProvider{response: "summary"})

	defaultAgent := al.registry.GetDefaultAgent()
	if defaultAgent == nil {
		t.Fatal("expected default agent")
	}

	// 6 messages > threshold of 2
	history := []providers.Message{
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "q2"},
		{Role: "assistant", Content: "a2"},
		{Role: "user", Content: "q3"},
		{Role: "assistant", Content: "a3"},
	}
	defaultAgent.Sessions.SetHistory("session-threshold", history)

	runtimeCh, closeRuntimeEvents := subscribeRuntimeEventsForTest(
		t,
		al,
		16,
		runtimeevents.KindAgentSessionSummarize,
	)
	defer closeRuntimeEvents()

	err := al.contextManager.Compact(context.Background(), &CompactRequest{
		SessionKey: "session-threshold",
		Reason:     ContextCompressReasonSummarize,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	waitForRuntimeEvent(t, runtimeCh, 5*time.Second, func(evt runtimeevents.Event) bool {
		return evt.Kind == runtimeevents.KindAgentSessionSummarize
	})

	newHistory := defaultAgent.Sessions.GetHistory("session-threshold")
	if len(newHistory) >= len(history) {
		t.Fatalf("expected summarization to reduce history from %d messages, got %d", len(history), len(newHistory))
	}
}

func TestLegacyCompact_PostTurn_ConcurrentAgentsUseOwningProviderAndStore(t *testing.T) {
	defaultProvider := &contextCompletionCaptureProvider{response: "default generated summary"}
	namedProvider := &contextCompletionCaptureProvider{response: "named generated summary"}
	al, defaultAgent, namedAgent := newNamedLegacyContextTestLoop(
		t,
		defaultProvider,
		namedProvider,
	)
	defer al.Close()

	defaultSession := admitLegacyContextTestSession(t, defaultAgent, "default-summarize")
	namedSession := admitLegacyContextTestSession(t, namedAgent, "named-summarize")
	defaultAgent.Sessions.SetHistory(defaultSession, legacyContextTestHistory("default-only"))
	namedAgent.Sessions.SetHistory(namedSession, legacyContextTestHistory("named-only"))

	runtimeCh, closeRuntimeEvents := subscribeRuntimeEventsForTest(
		t,
		al,
		16,
		runtimeevents.KindAgentSessionSummarize,
	)
	defer closeRuntimeEvents()

	var compactWG sync.WaitGroup
	for _, sessionKey := range []string{defaultSession, namedSession} {
		compactWG.Add(1)
		go func() {
			defer compactWG.Done()
			if err := al.contextManager.Compact(context.Background(), &CompactRequest{
				SessionKey: sessionKey,
				Reason:     ContextCompressReasonSummarize,
			}); err != nil {
				t.Errorf("Compact(%q) error = %v", sessionKey, err)
			}
		}()
	}
	compactWG.Wait()
	for range 2 {
		waitForRuntimeEvent(t, runtimeCh, 5*time.Second, func(evt runtimeevents.Event) bool {
			return evt.Kind == runtimeevents.KindAgentSessionSummarize
		})
	}

	if got := defaultAgent.Sessions.GetSummary(defaultSession); got != "default generated summary" {
		t.Fatalf("default summary = %q", got)
	}
	if got := namedAgent.Sessions.GetSummary(namedSession); got != "named generated summary" {
		t.Fatalf("named summary = %q", got)
	}
	if got := defaultProvider.Models(); !reflect.DeepEqual(got, []string{"default-upstream-model"}) {
		t.Fatalf("default provider models = %#v", got)
	}
	if got := namedProvider.Models(); !reflect.DeepEqual(got, []string{"named-upstream-model"}) {
		t.Fatalf("named provider models = %#v", got)
	}
	assertLegacyContextPromptIsolation(t, defaultProvider.Prompts(), "default-only", "named-only")
	assertLegacyContextPromptIsolation(t, namedProvider.Prompts(), "named-only", "default-only")
}

func TestLegacySummarizationUsesConcreteAliasModel(t *testing.T) {
	provider := &contextCompletionCaptureProvider{response: "resolved summary"}
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         t.TempDir(),
				AccountRef:        "summary-account",
				ModelName:         "summary",
				MaxTokens:         4096,
				MaxToolIterations: 10,
				ContextWindow:     8000,
			},
		},
		ModelList: []*config.ModelConfig{{
			ModelName: "summary-account",
			Provider:  "openai",
			APIBase:   "http://example.invalid/v1",
			APIKeys:   config.SimpleSecureStrings("test-key"),
			Enabled:   true,
		}},
		ModelAliases: []config.ModelAliasConfig{{
			Name:  "summary",
			Model: "upstream-summary-model",
		}},
	}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	defer al.Close()

	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("default agent is nil")
	}
	if agent.ConfigurationError != nil {
		t.Fatalf("default agent configuration error = %v", agent.ConfigurationError)
	}
	agent.Sessions.SetHistory("summary-alias", []providers.Message{
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "q2"},
		{Role: "assistant", Content: "a2"},
		{Role: "user", Content: "q3"},
		{Role: "assistant", Content: "a3"},
	})

	manager := &legacyContextManager{al: al}
	manager.summarizeSession(agent, "summary-alias")

	models := provider.Models()
	if len(models) == 0 {
		t.Fatal("summarization provider was not called")
	}
	for _, model := range models {
		if model != "upstream-summary-model" {
			t.Fatalf("summarization model = %q, want concrete upstream model", model)
		}
	}
}

func TestLegacySummarizationResolvesModelRouterTarget(t *testing.T) {
	provider := &contextCompletionCaptureProvider{response: "resolved summary"}
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         t.TempDir(),
				AccountRef:        "summary-account",
				ModelName:         "summary-router",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
		ModelList: []*config.ModelConfig{{
			ModelName: "summary-account",
			Provider:  "openai",
			APIBase:   "http://example.invalid/v1",
			APIKeys:   config.SimpleSecureStrings("test-key"),
			Enabled:   true,
		}},
		ModelAliases: []config.ModelAliasConfig{
			{Name: "fast-summary", Model: "upstream-fast-summary"},
			{Name: "default-summary", Model: "upstream-default-summary"},
		},
		ModelRouters: []config.ModelRouterConfig{{
			Name:    "summary-router",
			Enabled: true,
			Entry:   "rules",
			Blocks: []config.ModelRouterBlock{
				{
					ID:   "rules",
					Type: config.ModelRouterBlockTypeRules,
					Rules: []config.ModelRouterRule{{
						Match:  config.ModelRouterRuleContains,
						Value:  "quick",
						Target: "fast",
					}},
					Fallback: "default",
				},
				{ID: "fast", Type: config.ModelRouterBlockTypeModel, Model: "fast-summary"},
				{ID: "default", Type: config.ModelRouterBlockTypeModel, Model: "default-summary"},
			},
		}},
	}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), provider)
	defer al.Close()

	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("default agent is nil")
	}
	if agent.ConfigurationError != nil {
		t.Fatalf("default agent configuration error = %v", agent.ConfigurationError)
	}
	candidates, err := candidatesForAccountAliases(
		cfg,
		"summary-account",
		"fast-summary",
		nil,
		cfg.Agents.Defaults.Workspace,
		agent.CandidateProviders,
	)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("fast summary candidates = %#v, error = %v", candidates, err)
	}
	bindBootstrapProvider(agent.CandidateProviders, candidates[0], provider)

	manager := &legacyContextManager{al: al}
	if _, err := manager.retryLLMCall(
		context.Background(),
		agent,
		"summary-router-session",
		"make a quick summary",
		1,
	); err != nil {
		t.Fatalf("retryLLMCall: %v", err)
	}
	models := provider.Models()
	if len(models) != 1 || models[0] != "upstream-fast-summary" {
		t.Fatalf("summarization models = %#v, want concrete routed model", models)
	}
}

func TestLegacySummarizationResolvesAccountRouterProviderAndOverride(t *testing.T) {
	bootstrapProvider := &contextCompletionCaptureProvider{response: "wrong provider"}
	selectedProvider := &contextCompletionCaptureProvider{response: "resolved summary"}
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         t.TempDir(),
				AccountRef:        "summary-accounts",
				ModelName:         "summary",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
		ModelList: []*config.ModelConfig{{
			ModelName: "account-b",
			Provider:  "anthropic",
			APIBase:   "http://example.invalid/v1",
			APIKeys:   config.SimpleSecureStrings("test-key"),
			Enabled:   true,
		}},
		ModelAliases: []config.ModelAliasConfig{{
			Name:  "summary",
			Model: "default-summary-model",
			AccountOverrides: map[string]string{
				"account-b": "account-b-summary-model",
			},
		}},
		AccountRouters: []config.AccountRouterConfig{{
			Name:    "summary-accounts",
			Enabled: true,
			Entry:   "selected",
			Blocks: []config.AccountRouterBlock{{
				ID:      "selected",
				Type:    config.AccountRouterBlockTypeAccount,
				Account: "account-b",
			}},
		}},
	}
	al := NewAgentLoop(cfg, bus.NewMessageBus(), bootstrapProvider)
	defer al.Close()

	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("default agent is nil")
	}
	if agent.ConfigurationError != nil {
		t.Fatalf("default agent configuration error = %v", agent.ConfigurationError)
	}
	if agent.AccountRouter == nil {
		t.Fatal("account router was not configured")
	}
	selection := agent.AccountRouter.Select("account-router-summary", "compression")
	if len(selection.Candidates) != 1 {
		t.Fatalf("account router candidates = %#v, want one", selection.Candidates)
	}
	bindBootstrapProvider(
		agent.CandidateProviders,
		selection.Candidates[0],
		selectedProvider,
	)

	manager := &legacyContextManager{al: al}
	if _, err := manager.retryLLMCall(
		context.Background(),
		agent,
		"account-router-summary",
		"summarize this conversation",
		1,
	); err != nil {
		t.Fatalf("retryLLMCall: %v", err)
	}
	if got := selectedProvider.Models(); len(got) != 1 || got[0] != "account-b-summary-model" {
		t.Fatalf("selected provider models = %#v, want account override", got)
	}
	if got := bootstrapProvider.Models(); len(got) != 0 {
		t.Fatalf("bootstrap provider was called with models %#v", got)
	}
}

// ---------------------------------------------------------------------------
// Legacy Ingest tests
// ---------------------------------------------------------------------------

func TestLegacyIngest_NoOp(t *testing.T) {
	cfg := testConfig(t)
	al := newCMTestAgentLoop(cfg)

	err := al.contextManager.Ingest(context.Background(), &IngestRequest{
		SessionKey: "session-ingest",
		Message:    providers.Message{Role: "user", Content: "test"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Mock ContextManager — verifies dispatch through AgentLoop
// ---------------------------------------------------------------------------

func TestAgentLoop_UsesCustomContextManager(t *testing.T) {
	cleanup := resetCMRegistry()
	defer cleanup()

	mock := &trackingContextManager{}
	factory := func(cfg json.RawMessage, al *AgentLoop) (ContextManager, error) {
		return mock, nil
	}
	if err := RegisterContextManager("tracking_cm", factory); err != nil {
		t.Fatalf("register failed: %v", err)
	}

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         t.TempDir(),
				ModelName:         "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
				ContextManager:    "tracking_cm",
			},
		},
	}
	al := newCMTestAgentLoop(cfg)

	// Verify the mock was installed
	if al.contextManager != mock {
		t.Fatalf("expected mock context manager, got %T", al.contextManager)
	}

	// Direct method calls
	_, err := mock.Assemble(context.Background(), &AssembleRequest{
		SessionKey: "s1",
		Budget:     8000,
		MaxTokens:  4096,
	})
	if err != nil {
		t.Fatalf("Assemble error: %v", err)
	}
	if mock.assembleCalls.Load() != 1 {
		t.Fatalf("expected 1 assemble call, got %d", mock.assembleCalls.Load())
	}

	err = mock.Compact(context.Background(), &CompactRequest{
		SessionKey: "s1",
		Reason:     ContextCompressReasonRetry,
	})
	if err != nil {
		t.Fatalf("Compact error: %v", err)
	}
	if mock.compactCalls.Load() != 1 {
		t.Fatalf("expected 1 compact call, got %d", mock.compactCalls.Load())
	}

	err = mock.Ingest(context.Background(), &IngestRequest{
		SessionKey: "s1",
		Message:    providers.Message{Role: "user", Content: "test"},
	})
	if err != nil {
		t.Fatalf("Ingest error: %v", err)
	}
	if mock.ingestCalls.Load() != 1 {
		t.Fatalf("expected 1 ingest call, got %d", mock.ingestCalls.Load())
	}
}

func TestIngestCalledDuringTurn(t *testing.T) {
	cleanup := resetCMRegistry()
	defer cleanup()

	mock := &trackingContextManager{}
	factory := func(cfg json.RawMessage, al *AgentLoop) (ContextManager, error) {
		return mock, nil
	}
	if err := RegisterContextManager("ingest_track_cm", factory); err != nil {
		t.Fatalf("register failed: %v", err)
	}

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         t.TempDir(),
				ModelName:         "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
				ContextManager:    "ingest_track_cm",
			},
		},
	}

	msgBus := bus.NewMessageBus()
	al := newTestAgentLoopWithStrictModels(cfg, msgBus, &simpleMockProvider{response: "done"})
	defaultAgent := al.registry.GetDefaultAgent()
	if defaultAgent == nil {
		t.Fatal("expected default agent")
	}

	// Run a turn — ingestMessage is called for user message and final assistant message
	_, err := al.runAgentLoop(context.Background(), defaultAgent, processOptions{
		SessionKey:      "session-ingest-turn",
		Channel:         "cli",
		ChatID:          "direct",
		UserMessage:     "test ingest",
		DefaultResponse: defaultResponse,
		EnableSummary:   false,
		SendResponse:    false,
	})
	if err != nil {
		t.Fatalf("runAgentLoop failed: %v", err)
	}

	// Should have at least 2 ingest calls: user message + final assistant message
	if mock.ingestCalls.Load() < 2 {
		t.Fatalf("expected >= 2 ingest calls during turn, got %d", mock.ingestCalls.Load())
	}
}

func TestClearCommandRoutedAgentCallsContextManagerClear(t *testing.T) {
	cleanup := resetCMRegistry()
	defer cleanup()

	mock := &trackingContextManager{}
	factory := func(cfg json.RawMessage, al *AgentLoop) (ContextManager, error) {
		return mock, nil
	}
	if err := RegisterContextManager("clear_track_cm", factory); err != nil {
		t.Fatalf("register failed: %v", err)
	}

	workspace := t.TempDir()
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         filepath.Join(workspace, "default"),
				ModelName:         "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
				ContextManager:    "clear_track_cm",
			},
			List: []config.AgentConfig{
				{
					ID:        "main",
					Default:   true,
					Workspace: filepath.Join(workspace, "main"),
				},
				{
					ID:        "support",
					Workspace: filepath.Join(workspace, "support"),
				},
			},
			Dispatch: &config.DispatchConfig{
				Rules: []config.DispatchRule{
					{
						Name:  "support-dingtalk",
						Agent: "support",
						When: config.DispatchSelector{
							Channel: "dingtalk",
						},
					},
				},
			},
		},
		Session: config.SessionConfig{
			Dimensions: []string{"chat"},
		},
	}

	al := newTestAgentLoopWithStrictModels(cfg, bus.NewMessageBus(), &simpleMockProvider{response: "done"})
	if al.contextManager != mock {
		t.Fatalf("expected mock context manager, got %T", al.contextManager)
	}

	msg := testInboundMessage(bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  "dingtalk",
			ChatID:   "chat1",
			ChatType: "direct",
			SenderID: "user1",
		},
		Content: "/clear",
	})
	route, _, err := al.resolveMessageRoute(msg)
	if err != nil {
		t.Fatalf("resolveMessageRoute() error = %v", err)
	}
	sessionKey := al.allocateRouteSession(route, msg).SessionKey

	if _, err := al.processMessage(context.Background(), msg); err != nil {
		t.Fatalf("processMessage() error = %v", err)
	}

	if got := mock.clearCalls.Load(); got != 1 {
		t.Fatalf("Clear calls = %d, want 1", got)
	}
	mock.mu.Lock()
	gotKey := mock.lastClearKey
	mock.mu.Unlock()
	if gotKey != sessionKey {
		t.Fatalf("Clear session key = %q, want %q", gotKey, sessionKey)
	}
}

// ---------------------------------------------------------------------------
// forceCompression edge cases (via legacy Compact)
// ---------------------------------------------------------------------------

func TestLegacyCompact_Overflow_SingleTurnKeepsLastUserMessage(t *testing.T) {
	cfg := testConfig(t)
	al := newCMTestAgentLoop(cfg)

	defaultAgent := al.registry.GetDefaultAgent()
	if defaultAgent == nil {
		t.Fatal("expected default agent")
	}

	// History with only 2 messages — forceCompression should still handle it
	history := []providers.Message{
		{Role: "user", Content: "first question"},
		{Role: "assistant", Content: "first answer"},
	}
	defaultAgent.Sessions.SetHistory("session-2msg", history)

	err := al.contextManager.Compact(context.Background(), &CompactRequest{
		SessionKey: "session-2msg",
		Reason:     ContextCompressReasonRetry,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	newHistory := defaultAgent.Sessions.GetHistory("session-2msg")
	// With 2 messages, forceCompression returns false (len <= 2), so no compression
	if len(newHistory) != len(history) {
		t.Fatalf("expected no compression for 2-message history, got %d", len(newHistory))
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// noopContextManager is a minimal ContextManager that does nothing.
type noopContextManager struct{}

func (m *noopContextManager) Assemble(_ context.Context, req *AssembleRequest) (*AssembleResponse, error) {
	return &AssembleResponse{}, nil
}
func (m *noopContextManager) Compact(_ context.Context, _ *CompactRequest) error { return nil }
func (m *noopContextManager) Ingest(_ context.Context, _ *IngestRequest) error   { return nil }
func (m *noopContextManager) Clear(_ context.Context, _ string) error            { return nil }

// trackingContextManager tracks call counts for each method.
type trackingContextManager struct {
	assembleCalls atomic.Int64
	compactCalls  atomic.Int64
	ingestCalls   atomic.Int64
	clearCalls    atomic.Int64
	mu            sync.Mutex
	lastAssemble  *AssembleRequest
	lastCompact   *CompactRequest
	lastIngest    *IngestRequest
	lastClearKey  string
}

func (m *trackingContextManager) Assemble(_ context.Context, req *AssembleRequest) (*AssembleResponse, error) {
	m.assembleCalls.Add(1)
	m.mu.Lock()
	m.lastAssemble = req
	m.mu.Unlock()
	return &AssembleResponse{}, nil
}

func (m *trackingContextManager) Compact(_ context.Context, req *CompactRequest) error {
	m.compactCalls.Add(1)
	m.mu.Lock()
	m.lastCompact = req
	m.mu.Unlock()
	return nil
}

func (m *trackingContextManager) Ingest(_ context.Context, req *IngestRequest) error {
	m.ingestCalls.Add(1)
	m.mu.Lock()
	m.lastIngest = req
	m.mu.Unlock()
	return nil
}

func (m *trackingContextManager) Clear(_ context.Context, sessionKey string) error {
	m.clearCalls.Add(1)
	m.mu.Lock()
	m.lastClearKey = sessionKey
	m.mu.Unlock()
	return nil
}

// resetCMRegistry clears the global factory registry and returns a cleanup
// function that restores the original state after the test.
func resetCMRegistry() func() {
	cmRegistryMu.Lock()
	original := make(map[string]ContextManagerFactory, len(cmRegistry))
	for k, v := range cmRegistry {
		original[k] = v
	}
	cmRegistry = make(map[string]ContextManagerFactory)
	cmRegistryMu.Unlock()

	return func() {
		cmRegistryMu.Lock()
		cmRegistry = original
		cmRegistryMu.Unlock()
	}
}

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         t.TempDir(),
				ModelName:         "test-model",
				MaxTokens:         4096,
				MaxToolIterations: 10,
			},
		},
	}
}

func newNamedLegacyContextTestLoop(
	t *testing.T,
	defaultProvider *contextCompletionCaptureProvider,
	namedProvider *contextCompletionCaptureProvider,
) (*AgentLoop, *AgentInstance, *AgentInstance) {
	t.Helper()
	defaultWorkspace := t.TempDir()
	namedWorkspace := t.TempDir()
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:                 defaultWorkspace,
				ModelName:                 "default-summary",
				MaxTokens:                 4096,
				MaxToolIterations:         10,
				ContextWindow:             8000,
				SummarizeMessageThreshold: 2,
				SummarizeTokenPercent:     75,
			},
			List: []config.AgentConfig{
				{ID: "main", Default: true, Workspace: defaultWorkspace},
				{
					ID:        "named",
					Workspace: namedWorkspace,
					Model:     &config.AgentModelConfig{Primary: "named-summary"},
				},
			},
		},
		ModelAliases: []config.ModelAliasConfig{
			{Name: "default-summary", Model: "default-upstream-model"},
			{Name: "named-summary", Model: "named-upstream-model"},
		},
	}
	al := newTestAgentLoopWithStrictModels(cfg, bus.NewMessageBus(), defaultProvider)
	defaultAgent := al.registry.GetDefaultAgent()
	if defaultAgent == nil {
		al.Close()
		t.Fatal("default agent is nil")
	}
	namedAgent, ok := al.registry.GetAgent("named")
	if !ok || namedAgent == nil {
		al.Close()
		t.Fatal("named agent is nil")
	}
	if defaultAgent.ConfigurationError != nil {
		al.Close()
		t.Fatalf("default agent configuration error = %v", defaultAgent.ConfigurationError)
	}
	if namedAgent.ConfigurationError != nil {
		al.Close()
		t.Fatalf("named agent configuration error = %v", namedAgent.ConfigurationError)
	}
	if len(namedAgent.Candidates) != 1 {
		al.Close()
		t.Fatalf("named candidates = %#v, want one", namedAgent.Candidates)
	}
	bindBootstrapProvider(namedAgent.CandidateProviders, namedAgent.Candidates[0], namedProvider)
	return al, defaultAgent, namedAgent
}

func admitLegacyContextTestSession(t *testing.T, agent *AgentInstance, identity string) string {
	t.Helper()
	scope := session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    agent.ID,
		Channel:    "test",
		Dimensions: []string{"chat"},
		Values:     map[string]string{"chat": "direct:" + identity},
	}
	sessionKey := session.BuildSessionKey(scope)
	if err := admitSessionMetadata(t.Context(), agent.Sessions, sessionKey, &scope, nil, agent.ID); err != nil {
		t.Fatalf("admit session metadata: %v", err)
	}
	return sessionKey
}

func legacyContextTestHistory(prefix string) []providers.Message {
	return []providers.Message{
		{Role: "user", Content: prefix + " question 1"},
		{Role: "assistant", Content: prefix + " answer 1"},
		{Role: "user", Content: prefix + " question 2"},
		{Role: "assistant", Content: prefix + " answer 2"},
		{Role: "user", Content: prefix + " question 3"},
		{Role: "assistant", Content: prefix + " answer 3"},
	}
}

func assertLegacyContextPromptIsolation(
	t *testing.T,
	prompts []string,
	want string,
	unwanted string,
) {
	t.Helper()
	if len(prompts) != 1 {
		t.Fatalf("provider prompts = %#v, want one", prompts)
	}
	if !strings.Contains(prompts[0], want) {
		t.Fatalf("provider prompt does not contain %q: %q", want, prompts[0])
	}
	if strings.Contains(prompts[0], unwanted) {
		t.Fatalf("provider prompt contains foreign context %q: %q", unwanted, prompts[0])
	}
}

func newCMTestAgentLoop(cfg *config.Config) *AgentLoop {
	msgBus := bus.NewMessageBus()
	return newTestAgentLoopWithStrictModels(cfg, msgBus, &simpleMockProvider{response: "test"})
}

type contextCompletionCaptureProvider struct {
	mu       sync.Mutex
	response string
	models   []string
	prompts  []string
}

func (p *contextCompletionCaptureProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	model string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	var prompt strings.Builder
	for _, message := range messages {
		prompt.WriteString(message.Content)
		prompt.WriteByte('\n')
	}
	p.mu.Lock()
	p.models = append(p.models, model)
	p.prompts = append(p.prompts, prompt.String())
	p.mu.Unlock()
	return &providers.LLMResponse{Content: p.response}, nil
}

func (p *contextCompletionCaptureProvider) Models() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.models...)
}

func (p *contextCompletionCaptureProvider) Prompts() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.prompts...)
}
