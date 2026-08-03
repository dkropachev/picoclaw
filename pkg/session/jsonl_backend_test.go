package session_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/session"
)

// Compile-time interface satisfaction checks.
var (
	_ session.SessionStore     = (*session.SessionManager)(nil)
	_ session.SessionStore     = (*session.JSONLBackend)(nil)
	_ session.SnapshotReader   = (*session.SessionManager)(nil)
	_ session.SnapshotReader   = (*session.JSONLBackend)(nil)
	_ session.SnapshotReplacer = (*session.JSONLBackend)(nil)
)

type recordingSnapshotStore struct {
	memory.Store
	called      int
	replacement memory.SessionSnapshotReplacement
	returnErr   error
	mutateInput bool
}

func (s *recordingSnapshotStore) ReplaceSessionSnapshot(
	_ context.Context,
	replacement memory.SessionSnapshotReplacement,
) error {
	s.called++
	s.replacement = replacement
	s.replacement.History = session.CloneMessages(replacement.History)
	s.replacement.Scope = append(json.RawMessage(nil), replacement.Scope...)
	s.replacement.Aliases = append([]string(nil), replacement.Aliases...)

	if s.mutateInput {
		replacement.History[0].Content = "mutated by lower store"
		replacement.History[0].ToolCalls[0].Function.Name = "mutated"
		replacement.History[0].ToolCalls[0].Function.Arguments = `{"mutated":true}`
		replacement.Scope[0] = 'x'
		replacement.Aliases[0] = "mutated"
	}
	return s.returnErr
}

func validSnapshotReplacement() session.SessionSnapshotReplacement {
	scope := &session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    "main",
		Channel:    "review",
		Account:    "default",
		Dimensions: []string{"review", "review_version"},
		Values: map[string]string{
			"review":         "prc_0123456789abcdef0123456789abcdef",
			"review_version": "1",
		},
	}
	return session.SessionSnapshotReplacement{
		Key: session.BuildSessionKey(*scope),
		History: []providers.Message{{
			Role:    "user",
			Content: "review this",
			ToolCalls: []providers.ToolCall{{
				ID:       "call-1",
				Function: &providers.FunctionCall{Name: "inspect", Arguments: `{"path":"main.go"}`},
			}},
		}},
		Summary: "review summary",
		Scope:   scope,
		Aliases: []string{"review:case:prc_0123456789abcdef0123456789abcdef"},
	}
}

func newBackend(t *testing.T) *session.JSONLBackend {
	t.Helper()
	store, err := memory.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return session.NewJSONLBackend(store)
}

func TestJSONLBackend_AddAndGetHistory(t *testing.T) {
	b := newBackend(t)

	b.AddMessage("s1", "user", "hello")
	b.AddMessage("s1", "assistant", "hi")

	history := b.GetHistory("s1")
	if len(history) != 2 {
		t.Fatalf("got %d messages, want 2", len(history))
	}
	if history[0].Role != "user" || history[0].Content != "hello" {
		t.Errorf("msg[0] = %+v", history[0])
	}
	if history[1].Role != "assistant" || history[1].Content != "hi" {
		t.Errorf("msg[1] = %+v", history[1])
	}
}

func TestJSONLBackend_AddFullMessage(t *testing.T) {
	b := newBackend(t)

	msg := providers.Message{
		Role:    "assistant",
		Content: "done",
		ToolCalls: []providers.ToolCall{
			{ID: "tc1", Function: &providers.FunctionCall{Name: "read_file", Arguments: `{"path":"x"}`}},
		},
	}
	b.AddFullMessage("s1", msg)

	history := b.GetHistory("s1")
	if len(history) != 1 {
		t.Fatalf("got %d, want 1", len(history))
	}
	if len(history[0].ToolCalls) != 1 || history[0].ToolCalls[0].ID != "tc1" {
		t.Errorf("tool calls = %+v", history[0].ToolCalls)
	}
}

func TestJSONLBackend_AddFullMessage_PreservesModelName(t *testing.T) {
	b := newBackend(t)

	msg := providers.Message{
		Role:      "assistant",
		Content:   "done",
		ModelName: "gpt-5.4-mini",
	}
	b.AddFullMessage("s1", msg)

	history := b.GetHistory("s1")
	if len(history) != 1 {
		t.Fatalf("got %d, want 1", len(history))
	}
	if history[0].ModelName != "gpt-5.4-mini" {
		t.Fatalf("ModelName = %q, want %q", history[0].ModelName, "gpt-5.4-mini")
	}
}

func TestJSONLBackend_Summary(t *testing.T) {
	b := newBackend(t)

	if got := b.GetSummary("s1"); got != "" {
		t.Errorf("got %q, want empty", got)
	}

	b.SetSummary("s1", "test summary")
	if got := b.GetSummary("s1"); got != "test summary" {
		t.Errorf("got %q, want %q", got, "test summary")
	}
}

func TestJSONLBackend_TruncateAndSave(t *testing.T) {
	b := newBackend(t)

	for i := 0; i < 10; i++ {
		b.AddMessage("s1", "user", fmt.Sprintf("msg %d", i))
	}
	b.TruncateHistory("s1", 3)

	history := b.GetHistory("s1")
	if len(history) != 3 {
		t.Fatalf("got %d, want 3", len(history))
	}
	if history[0].Content != "msg 7" {
		t.Errorf("got %q, want %q", history[0].Content, "msg 7")
	}

	// Save triggers compaction.
	if err := b.Save("s1"); err != nil {
		t.Fatal(err)
	}

	// Messages still accessible after compaction.
	history = b.GetHistory("s1")
	if len(history) != 3 {
		t.Fatalf("after save: got %d, want 3", len(history))
	}
}

func TestJSONLBackend_SetHistory(t *testing.T) {
	b := newBackend(t)
	b.AddMessage("s1", "user", "old")

	b.SetHistory("s1", []providers.Message{
		{Role: "user", Content: "new1"},
		{Role: "assistant", Content: "new2"},
	})

	history := b.GetHistory("s1")
	if len(history) != 2 {
		t.Fatalf("got %d, want 2", len(history))
	}
	if history[0].Content != "new1" {
		t.Errorf("got %q, want %q", history[0].Content, "new1")
	}
}

func TestJSONLBackend_EmptySession(t *testing.T) {
	b := newBackend(t)

	history := b.GetHistory("nonexistent")
	if history == nil {
		t.Fatal("got nil, want empty slice")
	}
	if len(history) != 0 {
		t.Errorf("got %d, want 0", len(history))
	}
}

func TestJSONLBackend_SessionIsolation(t *testing.T) {
	b := newBackend(t)
	b.AddMessage("s1", "user", "session1")
	b.AddMessage("s2", "user", "session2")

	h1 := b.GetHistory("s1")
	h2 := b.GetHistory("s2")

	if len(h1) != 1 || h1[0].Content != "session1" {
		t.Errorf("s1: %+v", h1)
	}
	if len(h2) != 1 || h2[0].Content != "session2" {
		t.Errorf("s2: %+v", h2)
	}
}

func TestJSONLBackend_SummarizeFlow(t *testing.T) {
	// Simulates the real summarization flow in the agent loop:
	// SetSummary → TruncateHistory → Save
	b := newBackend(t)

	for i := 0; i < 20; i++ {
		b.AddMessage("s1", "user", fmt.Sprintf("msg %d", i))
	}

	b.SetSummary("s1", "conversation about testing")
	b.TruncateHistory("s1", 4)
	if err := b.Save("s1"); err != nil {
		t.Fatal(err)
	}

	if got := b.GetSummary("s1"); got != "conversation about testing" {
		t.Errorf("summary = %q", got)
	}
	history := b.GetHistory("s1")
	if len(history) != 4 {
		t.Fatalf("got %d messages, want 4", len(history))
	}
	if history[0].Content != "msg 16" {
		t.Errorf("first message = %q, want %q", history[0].Content, "msg 16")
	}
}

func TestJSONLBackend_ResolveAliasAndPersistMetadata(t *testing.T) {
	b := newBackend(t)

	scope := &session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    "main",
		Channel:    "telegram",
		Account:    "default",
		Dimensions: []string{"chat"},
		Values: map[string]string{
			"chat": "group:c1",
		},
	}
	b.EnsureSessionMetadata("canonical", scope, []string{"legacy"})

	if got := b.ResolveSessionKey("legacy"); got != "canonical" {
		t.Fatalf("ResolveSessionKey() = %q, want %q", got, "canonical")
	}

	b.AddMessage("legacy", "user", "hello through alias")
	history := b.GetHistory("canonical")
	if len(history) != 1 {
		t.Fatalf("len(history) = %d, want 1", len(history))
	}
	if history[0].Content != "hello through alias" {
		t.Fatalf("history[0].Content = %q, want %q", history[0].Content, "hello through alias")
	}

	resolvedScope := b.GetSessionScope("legacy")
	if resolvedScope == nil {
		t.Fatal("GetSessionScope() returned nil")
	}
	if resolvedScope.AgentID != scope.AgentID || resolvedScope.Values["chat"] != scope.Values["chat"] {
		t.Fatalf("GetSessionScope() = %+v, want %+v", resolvedScope, scope)
	}
}

func TestJSONLBackend_EnsureSessionMetadata_PromotesLegacyAliasHistory(t *testing.T) {
	b := newBackend(t)

	legacyKey := "agent:main:direct:legacy-user"
	b.AddMessage(legacyKey, "user", "legacy history")
	b.SetSummary(legacyKey, "legacy summary")

	canonicalKey := session.BuildOpaqueSessionKey(legacyKey)
	b.EnsureSessionMetadata(canonicalKey, &session.SessionScope{
		Version: session.ScopeVersionV1,
		AgentID: "main",
	}, []string{legacyKey})

	if got := b.ResolveSessionKey(legacyKey); got != canonicalKey {
		t.Fatalf("ResolveSessionKey() = %q, want %q", got, canonicalKey)
	}
	history := b.GetHistory(canonicalKey)
	if len(history) != 1 || history[0].Content != "legacy history" {
		t.Fatalf("promoted history = %+v", history)
	}
	if summary := b.GetSummary(canonicalKey); summary != "legacy summary" {
		t.Fatalf("promoted summary = %q, want %q", summary, "legacy summary")
	}
}

func TestJSONLBackend_EnsureSessionMetadata_AllowsSharedLegacyFallbacksAcrossAccounts(t *testing.T) {
	b := newBackend(t)
	allocations := make([]session.Allocation, 0, 2)
	for _, account := range []string{"work", "personal"} {
		allocation := session.AllocateRouteSession(session.AllocationInput{
			AgentID: "main",
			Context: bus.InboundContext{
				Channel:  "telegram",
				Account:  account,
				ChatID:   "dm-42",
				ChatType: "direct",
				SenderID: "user-42",
			},
			SessionPolicy: routing.SessionPolicy{Dimensions: []string{"sender"}},
		})
		allocations = append(allocations, allocation)
		b.EnsureSessionMetadata(
			allocation.SessionKey,
			&allocation.Scope,
			allocation.SessionAliases,
		)
	}
	if allocations[0].SessionKey == allocations[1].SessionKey {
		t.Fatal("account-specific allocations unexpectedly share a canonical key")
	}
	for index, allocation := range allocations {
		scope := b.GetSessionScope(allocation.SessionKey)
		if scope == nil || scope.Account != allocation.Scope.Account {
			t.Fatalf("scope %d = %+v, want account %q", index, scope, allocation.Scope.Account)
		}
	}
}

func TestJSONLBackend_EnsureSessionMetadata_PromotesLegacyPicoDirectAliasHistory(t *testing.T) {
	b := newBackend(t)

	legacyKey := "agent:main:pico:direct:pico:session-123"
	b.AddMessage(legacyKey, "user", "legacy pico history")

	scope := &session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    "main",
		Channel:    "pico",
		Account:    "default",
		Dimensions: []string{"sender"},
		Values: map[string]string{
			"sender": "pico-user",
		},
	}
	allocation := session.AllocateRouteSession(session.AllocationInput{
		AgentID: "main",
		Context: bus.InboundContext{
			Channel:  "pico",
			Account:  "default",
			ChatID:   "pico:session-123",
			ChatType: "direct",
			SenderID: "pico-user",
		},
		SessionPolicy: routing.SessionPolicy{
			Dimensions: []string{"sender"},
		},
	})

	b.EnsureSessionMetadata(allocation.SessionKey, scope, allocation.SessionAliases)

	if got := b.ResolveSessionKey(legacyKey); got != allocation.SessionKey {
		t.Fatalf("ResolveSessionKey() = %q, want %q", got, allocation.SessionKey)
	}
	history := b.GetHistory(allocation.SessionKey)
	if len(history) != 1 || history[0].Content != "legacy pico history" {
		t.Fatalf("promoted history = %+v", history)
	}
}

func TestJSONLBackend_EnsureSessionMetadata_DoesNotOverwriteNonEmptyCanonicalHistory(t *testing.T) {
	b := newBackend(t)

	canonicalKey := session.BuildOpaqueSessionKey("agent:main:direct:current-user")
	legacyKey := "agent:main:direct:legacy-user"

	b.AddMessage(canonicalKey, "user", "current canonical history")
	b.AddMessage(legacyKey, "user", "legacy history")

	b.EnsureSessionMetadata(canonicalKey, &session.SessionScope{
		Version: session.ScopeVersionV1,
		AgentID: "main",
	}, []string{legacyKey})

	history := b.GetHistory(canonicalKey)
	if len(history) != 1 || history[0].Content != "current canonical history" {
		t.Fatalf("canonical history overwritten: %+v", history)
	}
}

func TestJSONLBackendReadSessionSnapshot_ResolvesAliasAndClones(t *testing.T) {
	b := newBackend(t)
	canonicalKey := "canonical"
	alias := "agent:main:direct:legacy"
	b.EnsureSessionMetadata(canonicalKey, &session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    "main",
		Channel:    "github",
		Dimensions: []string{"repo"},
		Values:     map[string]string{"repo": "org/repo"},
	}, []string{alias})
	b.AddFullMessage(alias, providers.Message{
		Role:        "user",
		Content:     "review this",
		Attachments: []providers.Attachment{{Filename: "finding.json"}},
		SystemParts: []providers.ContentBlock{{
			Type:         "text",
			Text:         "policy",
			CacheControl: &providers.CacheControl{Type: "ephemeral"},
		}},
		ToolCalls: []providers.ToolCall{{
			ID:       "call-1",
			Function: &providers.FunctionCall{Name: "inspect", Arguments: `{}`},
		}},
	})
	b.SetSummary(alias, "existing work")

	snapshot, found, err := b.ReadSessionSnapshot(context.Background(), alias)
	if err != nil || !found {
		t.Fatalf("ReadSessionSnapshot() = (found=%v, err=%v)", found, err)
	}
	if snapshot.Key != canonicalKey || snapshot.Summary != "existing work" {
		t.Fatalf("snapshot identity/summary = (%q, %q)", snapshot.Key, snapshot.Summary)
	}
	if snapshot.Scope == nil || snapshot.Scope.Values["repo"] != "org/repo" {
		t.Fatalf("snapshot scope = %+v", snapshot.Scope)
	}
	if len(snapshot.Aliases) != 1 || snapshot.Aliases[0] != alias {
		t.Fatalf("snapshot aliases = %v, want [%q]", snapshot.Aliases, alias)
	}
	if snapshot.Revision == "" {
		t.Fatal("snapshot revision is empty")
	}
	if len(snapshot.History) != 1 || snapshot.History[0].Content != "review this" {
		t.Fatalf("snapshot history = %+v", snapshot.History)
	}

	snapshot.Scope.Dimensions[0] = "mutated"
	snapshot.Scope.Values["repo"] = "mutated"
	snapshot.Aliases[0] = "mutated"
	snapshot.History[0].Attachments[0].Filename = "mutated"
	snapshot.History[0].SystemParts[0].CacheControl.Type = "mutated"
	snapshot.History[0].ToolCalls[0].Function.Name = "mutated"

	again, found, err := b.ReadSessionSnapshot(context.Background(), alias)
	if err != nil || !found {
		t.Fatalf("second ReadSessionSnapshot() = (found=%v, err=%v)", found, err)
	}
	if again.Scope.Dimensions[0] != "repo" || again.Scope.Values["repo"] != "org/repo" {
		t.Fatalf("stored scope changed through snapshot: %+v", again.Scope)
	}
	if len(again.Aliases) != 1 || again.Aliases[0] != alias {
		t.Fatalf("stored aliases changed through snapshot: %v", again.Aliases)
	}
	message := again.History[0]
	if message.Attachments[0].Filename != "finding.json" ||
		message.SystemParts[0].CacheControl.Type != "ephemeral" ||
		message.ToolCalls[0].Function.Name != "inspect" {
		t.Fatalf("stored history changed through snapshot: %+v", message)
	}
}

func TestJSONLBackendReadSessionSnapshot_MissingBlankCanceledNoCreate(t *testing.T) {
	b := newBackend(t)
	for _, key := range []string{"", "  ", "missing"} {
		if _, found, err := b.ReadSessionSnapshot(context.Background(), key); err != nil || found {
			t.Fatalf("ReadSessionSnapshot(%q) = (found=%v, err=%v), want miss", key, found, err)
		}
	}
	if sessions := b.ListSessions(); len(sessions) != 0 {
		t.Fatalf("strict reads created sessions: %v", sessions)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, found, err := b.ReadSessionSnapshot(ctx, "missing"); err != context.Canceled || found {
		t.Fatalf("canceled read = (found=%v, err=%v), want context.Canceled", found, err)
	}
}

func TestJSONLBackendReadSessionSnapshot_PropagatesCorruption(t *testing.T) {
	t.Run("metadata", func(t *testing.T) {
		dir := t.TempDir()
		store, err := memory.NewJSONLStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		b := session.NewJSONLBackend(store)
		b.AddMessage("corrupt-meta", "user", "hello")
		writeErr := os.WriteFile(filepath.Join(dir, "corrupt-meta.meta.json"), []byte(`{"key":`), 0o644)
		if writeErr != nil {
			t.Fatal(writeErr)
		}

		_, found, err := b.ReadSessionSnapshot(context.Background(), "corrupt-meta")
		if err == nil || found || !strings.Contains(err.Error(), "decode") ||
			!strings.Contains(err.Error(), "meta") {
			t.Fatalf("corrupt metadata read = (found=%v, err=%v)", found, err)
		}
	})

	t.Run("history", func(t *testing.T) {
		dir := t.TempDir()
		store, err := memory.NewJSONLStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		b := session.NewJSONLBackend(store)
		b.AddMessage("corrupt-history", "user", "hello")
		path := filepath.Join(dir, "corrupt-history.jsonl")
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := file.WriteString("not-json\n")
		if writeErr != nil {
			file.Close()
			t.Fatal(writeErr)
		}
		closeErr := file.Close()
		if closeErr != nil {
			t.Fatal(closeErr)
		}

		_, found, err := b.ReadSessionSnapshot(context.Background(), "corrupt-history")
		if err == nil || found || !strings.Contains(err.Error(), "decode jsonl line") {
			t.Fatalf("corrupt history read = (found=%v, err=%v)", found, err)
		}
	})
}

func TestJSONLBackendReplaceSessionSnapshot_RoundTripAndCAS(t *testing.T) {
	b := newBackend(t)
	replacement := validSnapshotReplacement()
	if err := b.ReplaceSessionSnapshot(context.Background(), replacement); err != nil {
		t.Fatalf("ReplaceSessionSnapshot() first create: %v", err)
	}

	// Mutation after return must not affect the committed tuple.
	replacement.History[0].Content = "mutated"
	replacement.Scope.Values["review"] = "mutated"
	replacement.Aliases[0] = "mutated"

	snapshot, found, err := b.ReadSessionSnapshot(
		context.Background(),
		"review:case:prc_0123456789abcdef0123456789abcdef",
	)
	if err != nil || !found {
		t.Fatalf("ReadSessionSnapshot() = (found=%v, err=%v)", found, err)
	}
	if snapshot.Key == "" || snapshot.History[0].Content != "review this" ||
		snapshot.Summary != "review summary" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.Scope == nil ||
		snapshot.Scope.Values["review"] != "prc_0123456789abcdef0123456789abcdef" {
		t.Fatalf("snapshot scope = %+v", snapshot.Scope)
	}
	if len(snapshot.Aliases) != 1 ||
		snapshot.Aliases[0] != "review:case:prc_0123456789abcdef0123456789abcdef" {
		t.Fatalf("snapshot aliases = %v", snapshot.Aliases)
	}
	if snapshot.Revision == "" {
		t.Fatal("snapshot revision is empty")
	}

	previousRevision := snapshot.Revision
	next := session.SessionSnapshotReplacement{
		Key:              snapshot.Key,
		History:          []providers.Message{{Role: "assistant", Content: "fixed"}},
		Summary:          "updated",
		Scope:            snapshot.Scope,
		Aliases:          snapshot.Aliases,
		ExpectedRevision: previousRevision,
	}
	if replaceErr := b.ReplaceSessionSnapshot(context.Background(), next); replaceErr != nil {
		t.Fatalf("ReplaceSessionSnapshot() CAS: %v", replaceErr)
	}

	updated, found, err := b.ReadSessionSnapshot(context.Background(), snapshot.Key)
	if err != nil || !found {
		t.Fatalf("updated ReadSessionSnapshot() = (found=%v, err=%v)", found, err)
	}
	if updated.Revision == "" || updated.Revision == previousRevision ||
		len(updated.History) != 1 || updated.History[0].Content != "fixed" ||
		updated.Summary != "updated" {
		t.Fatalf("updated snapshot = %+v", updated)
	}

	next.ExpectedRevision = previousRevision
	if replaceErr := b.ReplaceSessionSnapshot(context.Background(), next); !errors.Is(
		replaceErr,
		session.ErrSnapshotConflict,
	) {
		t.Fatalf("stale replacement error = %v, want ErrSnapshotConflict", replaceErr)
	}
}

func TestJSONLBackendReplaceSessionSnapshot_ClonesLowerStoreInput(t *testing.T) {
	recording := &recordingSnapshotStore{mutateInput: true}
	b := session.NewJSONLBackend(recording)
	replacement := validSnapshotReplacement()

	if err := b.ReplaceSessionSnapshot(context.Background(), replacement); err != nil {
		t.Fatalf("ReplaceSessionSnapshot() error = %v", err)
	}
	if recording.called != 1 {
		t.Fatalf("lower replacement calls = %d, want 1", recording.called)
	}
	if recording.replacement.Key != replacement.Key ||
		recording.replacement.ExpectedRevision != replacement.ExpectedRevision ||
		recording.replacement.History[0].Content != "review this" ||
		recording.replacement.Aliases[0] != replacement.Aliases[0] {
		t.Fatalf("forwarded replacement = %+v", recording.replacement)
	}
	var decodedScope session.SessionScope
	if err := json.Unmarshal(recording.replacement.Scope, &decodedScope); err != nil {
		t.Fatalf("forwarded scope is invalid: %v", err)
	}
	if decodedScope.Values["review"] != replacement.Scope.Values["review"] {
		t.Fatalf("forwarded scope = %+v", decodedScope)
	}

	if replacement.History[0].Content != "review this" ||
		replacement.History[0].ToolCalls[0].Function.Name != "inspect" ||
		replacement.History[0].ToolCalls[0].Function.Arguments != `{"path":"main.go"}` ||
		replacement.Scope.Values["review"] != "prc_0123456789abcdef0123456789abcdef" ||
		replacement.Aliases[0] != "review:case:prc_0123456789abcdef0123456789abcdef" {
		t.Fatalf("lower store mutated caller-owned replacement: %+v", replacement)
	}
}

func TestJSONLBackendReplaceSessionSnapshot_FailsClosedWhenUnsupported(t *testing.T) {
	lower, err := memory.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lower.Close() })

	// Embedding only the stable Store interface intentionally hides the
	// concrete JSONL store's optional snapshot replacement method.
	b := session.NewJSONLBackend(struct{ memory.Store }{Store: lower})
	err = b.ReplaceSessionSnapshot(context.Background(), validSnapshotReplacement())
	if !errors.Is(err, session.ErrSnapshotUnsupported) {
		t.Fatalf("ReplaceSessionSnapshot() error = %v, want ErrSnapshotUnsupported", err)
	}
	if sessions := lower.ListSessions(); len(sessions) != 0 {
		t.Fatalf("unsupported replacement used legacy write fallback: %v", sessions)
	}
}

func TestJSONLBackendReplaceSessionSnapshot_TranslatesConflictAndCancellation(t *testing.T) {
	t.Run("conflict", func(t *testing.T) {
		recording := &recordingSnapshotStore{returnErr: memory.ErrSnapshotConflict}
		b := session.NewJSONLBackend(recording)
		err := b.ReplaceSessionSnapshot(context.Background(), validSnapshotReplacement())
		if !errors.Is(err, session.ErrSnapshotConflict) {
			t.Fatalf("ReplaceSessionSnapshot() error = %v, want ErrSnapshotConflict", err)
		}
	})

	t.Run("canceled before lower call", func(t *testing.T) {
		recording := &recordingSnapshotStore{}
		b := session.NewJSONLBackend(recording)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := b.ReplaceSessionSnapshot(ctx, validSnapshotReplacement())
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ReplaceSessionSnapshot() error = %v, want context.Canceled", err)
		}
		if recording.called != 0 {
			t.Fatalf("lower replacement calls = %d, want 0", recording.called)
		}
	})
}

func TestJSONLBackendReplaceSessionSnapshot_ValidatesExactBinding(t *testing.T) {
	tests := map[string]func(*session.SessionSnapshotReplacement){
		"blank key": func(replacement *session.SessionSnapshotReplacement) {
			replacement.Key = ""
		},
		"non-exact opaque key": func(replacement *session.SessionSnapshotReplacement) {
			replacement.Key = strings.ToUpper(replacement.Key)
		},
		"missing scope": func(replacement *session.SessionSnapshotReplacement) {
			replacement.Scope = nil
		},
		"unsupported scope version": func(replacement *session.SessionSnapshotReplacement) {
			replacement.Scope.Version++
		},
		"non-canonical owner": func(replacement *session.SessionSnapshotReplacement) {
			replacement.Scope.AgentID = "Main"
		},
		"non-canonical channel": func(replacement *session.SessionSnapshotReplacement) {
			replacement.Scope.Channel = "Review"
		},
		"non-canonical account": func(replacement *session.SessionSnapshotReplacement) {
			replacement.Scope.Account = "Default"
		},
		"duplicate dimension": func(replacement *session.SessionSnapshotReplacement) {
			replacement.Scope.Dimensions = append(replacement.Scope.Dimensions, "review")
		},
		"missing dimension value": func(replacement *session.SessionSnapshotReplacement) {
			delete(replacement.Scope.Values, "review")
		},
		"unlisted semantic value": func(replacement *session.SessionSnapshotReplacement) {
			replacement.Scope.Values["sender"] = "spoofed-owner"
		},
		"key and scope mismatch": func(replacement *session.SessionSnapshotReplacement) {
			replacement.Scope.Values["review"] = "another-review"
		},
		"blank alias": func(replacement *session.SessionSnapshotReplacement) {
			replacement.Aliases = []string{""}
		},
		"padded alias": func(replacement *session.SessionSnapshotReplacement) {
			replacement.Aliases = []string{" padded"}
		},
		"canonical key alias": func(replacement *session.SessionSnapshotReplacement) {
			replacement.Aliases = []string{replacement.Key}
		},
		"duplicate alias": func(replacement *session.SessionSnapshotReplacement) {
			replacement.Aliases = []string{"review:case:one", "review:case:one"}
		},
		"transient thought": func(replacement *session.SessionSnapshotReplacement) {
			replacement.History = []providers.Message{{
				Role:             "assistant",
				ReasoningContent: "runtime thought",
			}}
		},
		"runtime tool arguments": func(replacement *session.SessionSnapshotReplacement) {
			replacement.History[0].ToolCalls[0].Arguments = map[string]any{"path": "main.go"}
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			recording := &recordingSnapshotStore{}
			b := session.NewJSONLBackend(recording)
			replacement := validSnapshotReplacement()
			mutate(&replacement)
			if err := b.ReplaceSessionSnapshot(context.Background(), replacement); err == nil {
				t.Fatal("ReplaceSessionSnapshot() error = nil, want validation error")
			}
			if recording.called != 0 {
				t.Fatalf("lower replacement calls = %d, want 0", recording.called)
			}
		})
	}
}
