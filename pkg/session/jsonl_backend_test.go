package session_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/internal/sessiondb"
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
	_ session.ScopeAdmitter    = (*session.JSONLBackend)(nil)
)

type recordingSnapshotStore struct {
	memory.Store
	called      int
	replacement memory.SessionSnapshotReplacement
	returnErr   error
	mutateInput bool
}

type pausingAdmissionStore struct {
	*memory.JSONLStore
	admitted chan struct{}
	release  chan struct{}
	once     sync.Once
}

type preAdmissionBarrierStore struct {
	*memory.JSONLStore
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *preAdmissionBarrierStore) AdmitSessionMeta(
	ctx context.Context,
	key string,
	admit memory.SessionMetaAdmission,
) (bool, error) {
	s.once.Do(func() { close(s.reached) })
	select {
	case <-s.release:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	return s.JSONLStore.AdmitSessionMeta(ctx, key, admit)
}

func (s *pausingAdmissionStore) AdmitSessionMeta(
	ctx context.Context,
	key string,
	admit memory.SessionMetaAdmission,
) (bool, error) {
	updated, err := s.JSONLStore.AdmitSessionMeta(ctx, key, admit)
	s.once.Do(func() { close(s.admitted) })
	<-s.release
	return updated, err
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

func ordinaryAdmissionScope(chatID string) *session.SessionScope {
	return &session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    "main",
		Channel:    "github",
		Account:    "default",
		Dimensions: []string{"chat"},
		Values: map[string]string{
			"chat": chatID,
		},
	}
}

func reviewAdmissionScope(caseID string) *session.SessionScope {
	return &session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    "main",
		Channel:    "review",
		Account:    "default",
		Dimensions: []string{"review"},
		Values: map[string]string{
			"review": caseID,
		},
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

func TestJSONLBackendAdmitSessionScope_RejectsSpoofedLiveReviewScope(t *testing.T) {
	b := newBackend(t)
	scope := reviewAdmissionScope("prc_spoofed_live")
	key := session.BuildSessionKey(*scope)

	updated, err := b.AdmitSessionScope(context.Background(), session.SessionScopeAdmission{
		Key:            key,
		Scope:          scope,
		InitialAliases: []string{"review:case:prc_spoofed_live"},
		Mode:           session.ScopeAdmissionLive,
	})
	if updated || !errors.Is(err, session.ErrScopeAdmissionConflict) {
		t.Fatalf("AdmitSessionScope() = (updated=%v, err=%v), want live/review conflict", updated, err)
	}
	if sessions := b.ListSessions(); len(sessions) != 0 {
		t.Fatalf("rejected live review admission created sessions: %v", sessions)
	}
	if _, found, readErr := b.ReadSessionSnapshot(context.Background(), key); readErr != nil || found {
		t.Fatalf("rejected admission snapshot = (found=%v, err=%v), want absent", found, readErr)
	}
}

func TestJSONLBackendAdmitSessionScope_ReviewReservationIsEmptyAndIdempotent(t *testing.T) {
	b := newBackend(t)
	scope := reviewAdmissionScope("prc_reservation")
	key := session.BuildSessionKey(*scope)
	aliases := []string{
		"review:agent:main:case:prc_reservation",
		"review:agent:main:case:prc_reservation:binding:one",
	}
	admission := session.SessionScopeAdmission{
		Key:            key,
		Scope:          scope,
		InitialAliases: aliases,
		Mode:           session.ScopeAdmissionReview,
	}

	updated, err := b.AdmitSessionScope(context.Background(), admission)
	if err != nil || !updated {
		t.Fatalf("first AdmitSessionScope() = (updated=%v, err=%v), want reservation", updated, err)
	}
	before, found, err := b.ReadSessionSnapshot(context.Background(), aliases[0])
	if err != nil || !found {
		t.Fatalf("ReadSessionSnapshot(alias) = (found=%v, err=%v)", found, err)
	}
	if before.Key != key || before.Revision == "" || len(before.History) != 0 || before.Summary != "" ||
		!reflect.DeepEqual(before.Scope, scope) || !reflect.DeepEqual(before.Aliases, aliases) {
		t.Fatalf("reserved snapshot = %+v", before)
	}
	if resolved := b.ResolveSessionKey(aliases[1]); resolved != key {
		t.Fatalf("ResolveSessionKey(second alias) = %q, want %q", resolved, key)
	}

	updated, err = b.AdmitSessionScope(context.Background(), admission)
	if err != nil || updated {
		t.Fatalf("idempotent AdmitSessionScope() = (updated=%v, err=%v), want unchanged", updated, err)
	}
	after, found, err := b.ReadSessionSnapshot(context.Background(), key)
	if err != nil || !found {
		t.Fatalf("ReadSessionSnapshot(key) = (found=%v, err=%v)", found, err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("idempotent admission changed snapshot:\n before=%+v\n after=%+v", before, after)
	}
}

func TestJSONLBackendAdmitSessionScope_LiveUpdatesExistingOrdinaryMetadata(t *testing.T) {
	b := newBackend(t)
	desiredScope := ordinaryAdmissionScope("pull:org/repo:17")
	key := session.BuildSessionKey(*desiredScope)
	oldAlias := "agent:main:github:legacy-review-17"
	newAlias := "agent:main:github:pull:org/repo:17"
	legacyScope := session.CloneScope(desiredScope)
	legacyScope.Account = ""

	b.AddMessage(key, "user", "preserve this history")
	b.SetSummary(key, "preserve this summary")
	b.EnsureSessionMetadata(key, legacyScope, []string{oldAlias})

	updated, err := b.AdmitSessionScope(context.Background(), session.SessionScopeAdmission{
		Key:            key,
		Scope:          desiredScope,
		InitialAliases: []string{newAlias},
		Mode:           session.ScopeAdmissionLive,
	})
	if err != nil || !updated {
		t.Fatalf("AdmitSessionScope() = (updated=%v, err=%v), want compatible update", updated, err)
	}
	snapshot, found, err := b.ReadSessionSnapshot(context.Background(), newAlias)
	if err != nil || !found {
		t.Fatalf("ReadSessionSnapshot(new alias) = (found=%v, err=%v)", found, err)
	}
	if snapshot.Key != key || !reflect.DeepEqual(snapshot.Scope, desiredScope) ||
		!reflect.DeepEqual(snapshot.Aliases, []string{newAlias}) ||
		len(snapshot.History) != 1 || snapshot.History[0].Content != "preserve this history" ||
		snapshot.Summary != "preserve this summary" {
		t.Fatalf("updated ordinary snapshot = %+v", snapshot)
	}
	if resolved := b.ResolveSessionKey(oldAlias); resolved != oldAlias {
		t.Fatalf("removed alias still resolves to %q", resolved)
	}
}

func TestJSONLBackendAdmitSessionScope_NilLiveAliasesPreserveLockedOwner(t *testing.T) {
	for _, lookup := range []string{"canonical", "alias"} {
		t.Run(lookup, func(t *testing.T) {
			b := newBackend(t)
			scope := ordinaryAdmissionScope("pull:org/repo:preserve-aliases-" + lookup)
			key := session.BuildSessionKey(*scope)
			aliases := []string{
				"agent:main:github:legacy-preserve-aliases-" + lookup,
				"agent:main:github:pull:org/repo:preserve-aliases-" + lookup,
			}
			b.EnsureSessionMetadata(key, scope, aliases)
			b.AddMessage(key, "user", "keep alias-bound history")
			before, found, err := b.ReadSessionSnapshot(context.Background(), key)
			if err != nil || !found {
				t.Fatalf("ReadSessionSnapshot(before) = (found=%v, err=%v)", found, err)
			}
			requestedKey := key
			if lookup == "alias" {
				requestedKey = aliases[0]
			}
			fallback := ordinaryAdmissionScope("different-fallback-" + lookup)
			updated, err := b.AdmitSessionScope(context.Background(), session.SessionScopeAdmission{
				Key:                   requestedKey,
				Scope:                 fallback,
				Mode:                  session.ScopeAdmissionLive,
				PreserveExistingScope: true,
			})
			if err != nil || !updated {
				t.Fatalf("AdmitSessionScope(preserve owner) = (updated=%v, err=%v)", updated, err)
			}
			after, found, err := b.ReadSessionSnapshot(context.Background(), aliases[0])
			if err != nil || !found {
				t.Fatalf("ReadSessionSnapshot(alias) = (found=%v, err=%v)", found, err)
			}
			if !reflect.DeepEqual(after.Scope, before.Scope) ||
				!reflect.DeepEqual(after.Aliases, before.Aliases) ||
				!reflect.DeepEqual(after.History, before.History) ||
				after.Summary != before.Summary {
				t.Fatalf("preserving admission changed owner tuple:\nbefore=%+v\nafter=%+v", before, after)
			}
		})
	}
}

func TestJSONLBackendAdmitSessionScope_PreserveExistingScopeUsesFallbackWhenAbsent(t *testing.T) {
	b := newBackend(t)
	fallback := ordinaryAdmissionScope("pull:org/repo:absent-fallback")
	key := session.BuildSessionKey(*fallback)
	updated, err := b.AdmitSessionScope(context.Background(), session.SessionScopeAdmission{
		Key:                   key,
		Scope:                 fallback,
		Mode:                  session.ScopeAdmissionLive,
		PreserveExistingScope: true,
	})
	if err != nil || !updated {
		t.Fatalf("AdmitSessionScope(absent fallback) = (updated=%v, err=%v)", updated, err)
	}
	snapshot, found, err := b.ReadSessionSnapshot(context.Background(), key)
	if err != nil || !found || !reflect.DeepEqual(snapshot.Scope, fallback) {
		t.Fatalf("fallback snapshot = (found=%v, snapshot=%+v, err=%v)", found, snapshot, err)
	}
}

func TestJSONLBackendAdmitSessionScope_SnapshotsInitialAliasesBeforeLowerWait(t *testing.T) {
	lower, err := memory.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lower.Close() })
	barrier := &preAdmissionBarrierStore{
		JSONLStore: lower,
		reached:    make(chan struct{}),
		release:    make(chan struct{}),
	}
	backend := session.NewJSONLBackend(barrier)
	scope := ordinaryAdmissionScope("pull:org/repo:alias-snapshot")
	key := session.BuildSessionKey(*scope)
	aliases := []string{"agent:main:github:alias-snapshot"}
	done := make(chan error, 1)
	go func() {
		_, admitErr := backend.AdmitSessionScope(context.Background(), session.SessionScopeAdmission{
			Key:            key,
			Scope:          scope,
			InitialAliases: aliases,
			Mode:           session.ScopeAdmissionLive,
		})
		done <- admitErr
	}()
	<-barrier.reached
	aliases[0] = "agent:main:github:mutated-after-invocation"
	close(barrier.release)
	if waitErr := <-done; waitErr != nil {
		t.Fatalf("AdmitSessionScope() error = %v", waitErr)
	}
	snapshot, found, err := backend.ReadSessionSnapshot(
		context.Background(),
		"agent:main:github:alias-snapshot",
	)
	if err != nil || !found ||
		!reflect.DeepEqual(snapshot.Aliases, []string{"agent:main:github:alias-snapshot"}) {
		t.Fatalf("snapshotted aliases = (found=%v, aliases=%v, err=%v)",
			found, snapshot.Aliases, err)
	}
}

func TestJSONLBackendAdmitSessionScope_LiveAliasAdmissionPromotesOnceAndKeepsContinuity(
	t *testing.T,
) {
	dir := t.TempDir()
	lower, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lower.Close() })
	backend := session.NewJSONLBackend(lower)
	desiredScope := ordinaryAdmissionScope("pull:org/repo:alias-continuity")
	canonicalKey := session.BuildSessionKey(*desiredScope)
	requestedAlias := "agent:main:github:legacy-alias-continuity"
	allocationAlias := "agent:main:github:pull:org/repo:alias-continuity"

	if addErr := lower.AddMessage(
		context.Background(),
		requestedAlias,
		"user",
		"legacy first",
	); addErr != nil {
		t.Fatal(addErr)
	}
	if summaryErr := lower.SetSummary(
		context.Background(),
		requestedAlias,
		"legacy summary",
	); summaryErr != nil {
		t.Fatal(summaryErr)
	}
	legacyScope := session.CloneScope(desiredScope)
	legacyScope.Account = ""
	rawLegacyScope, err := json.Marshal(legacyScope)
	if err != nil {
		t.Fatal(err)
	}
	if upsertErr := lower.UpsertSessionMeta(
		context.Background(),
		canonicalKey,
		rawLegacyScope,
		[]string{requestedAlias},
	); upsertErr != nil {
		t.Fatal(upsertErr)
	}

	admission := session.SessionScopeAdmission{
		Key:            requestedAlias,
		Scope:          desiredScope,
		InitialAliases: []string{allocationAlias},
		Mode:           session.ScopeAdmissionLive,
	}
	updated, err := backend.AdmitSessionScope(context.Background(), admission)
	if err != nil || !updated {
		t.Fatalf("first alias admission = (updated=%v, err=%v)", updated, err)
	}
	first, found, err := backend.ReadSessionSnapshot(context.Background(), canonicalKey)
	if err != nil || !found {
		t.Fatalf("first canonical snapshot = (found=%v, err=%v)", found, err)
	}
	wantAliases := []string{allocationAlias, requestedAlias}
	if len(first.History) != 1 || first.History[0].Content != "legacy first" ||
		first.Summary != "legacy summary" || !reflect.DeepEqual(first.Scope, desiredScope) ||
		!reflect.DeepEqual(first.Aliases, wantAliases) {
		t.Fatalf("first promoted snapshot = %+v", first)
	}
	if resolved := backend.ResolveSessionKey(requestedAlias); resolved != canonicalKey {
		t.Fatalf("requested alias resolves to %q, want %q", resolved, canonicalKey)
	}

	backend.AddMessage(requestedAlias, "assistant", "canonical second")
	updated, err = backend.AdmitSessionScope(context.Background(), admission)
	if err != nil || !updated {
		t.Fatalf("second alias admission = (updated=%v, err=%v)", updated, err)
	}
	second, found, err := backend.ReadSessionSnapshot(context.Background(), canonicalKey)
	if err != nil || !found {
		t.Fatalf("second canonical snapshot = (found=%v, err=%v)", found, err)
	}
	if len(second.History) != 2 || second.History[0].Content != "legacy first" ||
		second.History[1].Content != "canonical second" ||
		second.Summary != "legacy summary" ||
		!reflect.DeepEqual(second.Aliases, wantAliases) {
		t.Fatalf("continuous canonical snapshot = %+v", second)
	}
}

func TestJSONLBackendAdmitSessionScope_LiveAliasOwnershipIsClosedBeforeReturn(
	t *testing.T,
) {
	dir := t.TempDir()
	liveLower, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = liveLower.Close() })
	reviewLower, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reviewLower.Close() })

	desiredScope := ordinaryAdmissionScope("pull:org/repo:forced-race")
	canonicalKey := session.BuildSessionKey(*desiredScope)
	requestedAlias := "agent:main:github:legacy-forced-race"
	allocationAlias := "agent:main:github:pull:org/repo:forced-race"
	if addErr := liveLower.AddMessage(
		context.Background(),
		requestedAlias,
		"user",
		"legacy protected by live admission",
	); addErr != nil {
		t.Fatal(addErr)
	}
	rawScope, err := json.Marshal(desiredScope)
	if err != nil {
		t.Fatal(err)
	}
	if err := liveLower.UpsertSessionMeta(
		context.Background(),
		canonicalKey,
		rawScope,
		[]string{requestedAlias},
	); err != nil {
		t.Fatal(err)
	}

	paused := &pausingAdmissionStore{
		JSONLStore: liveLower,
		admitted:   make(chan struct{}),
		release:    make(chan struct{}),
	}
	liveBackend := session.NewJSONLBackend(paused)
	liveDone := make(chan error, 1)
	go func() {
		updated, admitErr := liveBackend.AdmitSessionScope(
			context.Background(),
			session.SessionScopeAdmission{
				Key:            requestedAlias,
				Scope:          desiredScope,
				InitialAliases: []string{allocationAlias},
				Mode:           session.ScopeAdmissionLive,
			},
		)
		if admitErr == nil && !updated {
			admitErr = errors.New("live admission did not update metadata")
		}
		liveDone <- admitErr
	}()
	select {
	case <-paused.admitted:
	case <-time.After(2 * time.Second):
		t.Fatal("live admission did not reach its post-transaction boundary")
	}

	reviewScope := reviewAdmissionScope("prc_forced_alias_race")
	reviewKey := session.BuildSessionKey(*reviewScope)
	reviewBackend := session.NewJSONLBackend(reviewLower)
	claimed, claimErr := reviewBackend.AdmitSessionScope(
		context.Background(),
		session.SessionScopeAdmission{
			Key:            reviewKey,
			Scope:          reviewScope,
			InitialAliases: []string{requestedAlias},
			Mode:           session.ScopeAdmissionReview,
		},
	)
	if claimed || claimErr == nil {
		t.Fatalf("review claim during live return gap = (updated=%v, err=%v)", claimed, claimErr)
	}
	intermediate, found, readErr := reviewBackend.ReadSessionSnapshot(
		context.Background(),
		canonicalKey,
	)
	if readErr != nil || !found || len(intermediate.History) != 1 ||
		intermediate.History[0].Content != "legacy protected by live admission" ||
		!reflect.DeepEqual(intermediate.Aliases, []string{allocationAlias, requestedAlias}) {
		t.Fatalf("atomic live tuple = (found=%v, snapshot=%+v, err=%v)", found, intermediate, readErr)
	}
	if _, found, readErr := reviewBackend.ReadSessionSnapshot(
		context.Background(),
		reviewKey,
	); readErr != nil || found {
		t.Fatalf("losing review session = (found=%v, err=%v)", found, readErr)
	}

	close(paused.release)
	if err := <-liveDone; err != nil {
		t.Fatalf("live admission error = %v", err)
	}
}

func TestJSONLBackendAdmitSessionScope_ReviewRequiresCanonicalExactV1Binding(t *testing.T) {
	tests := map[string]func(*session.SessionScope, *string){
		"wrong version": func(scope *session.SessionScope, _ *string) {
			scope.Version = session.ScopeVersionV1 + 1
		},
		"noncanonical owner": func(scope *session.SessionScope, _ *string) {
			scope.AgentID = "Main"
		},
		"noncanonical channel": func(scope *session.SessionScope, _ *string) {
			scope.Channel = "Review"
		},
		"wrong channel": func(scope *session.SessionScope, _ *string) {
			scope.Channel = "github"
		},
		"noncanonical account": func(scope *session.SessionScope, _ *string) {
			scope.Account = " DEFAULT "
		},
		"noncanonical dimension": func(scope *session.SessionScope, _ *string) {
			scope.Dimensions[0] = "Review"
		},
		"noncanonical value": func(scope *session.SessionScope, _ *string) {
			scope.Values["review"] = "PRC_NONCANONICAL"
		},
		"inexact values": func(scope *session.SessionScope, _ *string) {
			scope.Values["extra"] = "value"
		},
		"mismatched key": func(_ *session.SessionScope, key *string) {
			other := reviewAdmissionScope("prc_other_exact_key")
			*key = session.BuildSessionKey(*other)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			backend := newBackend(t)
			scope := reviewAdmissionScope("prc_exact_binding")
			key := session.BuildSessionKey(*scope)
			mutate(scope, &key)

			updated, err := backend.AdmitSessionScope(
				context.Background(),
				session.SessionScopeAdmission{
					Key:   key,
					Scope: scope,
					Mode:  session.ScopeAdmissionReview,
				},
			)
			if updated || err == nil {
				t.Fatalf("malformed review admission = (updated=%v, err=%v)", updated, err)
			}
			if sessions := backend.ListSessions(); len(sessions) != 0 {
				t.Fatalf("malformed review admission created sessions: %v", sessions)
			}
		})
	}
}

func TestJSONLBackendAdmitSessionScope_ExistingReviewRejectsLiveWithoutMutation(t *testing.T) {
	b := newBackend(t)
	reviewScope := reviewAdmissionScope("prc_protected")
	key := session.BuildSessionKey(*reviewScope)
	alias := "review:agent:main:case:prc_protected"
	if updated, err := b.AdmitSessionScope(context.Background(), session.SessionScopeAdmission{
		Key:            key,
		Scope:          reviewScope,
		InitialAliases: []string{alias},
		Mode:           session.ScopeAdmissionReview,
	}); err != nil || !updated {
		t.Fatalf("review reservation = (updated=%v, err=%v)", updated, err)
	}
	b.AddMessage(key, "user", "protected transcript")
	b.SetSummary(key, "protected summary")
	before, found, err := b.ReadSessionSnapshot(context.Background(), key)
	if err != nil || !found {
		t.Fatalf("ReadSessionSnapshot(before) = (found=%v, err=%v)", found, err)
	}

	for _, requestedKey := range []string{key, alias} {
		updated, admitErr := b.AdmitSessionScope(context.Background(), session.SessionScopeAdmission{
			Key:   requestedKey,
			Scope: ordinaryAdmissionScope("attacker-selected-review-key"),
			Mode:  session.ScopeAdmissionLive,
		})
		if updated || !errors.Is(admitErr, session.ErrScopeAdmissionConflict) {
			t.Fatalf("live admission through %q = (updated=%v, err=%v), want conflict", requestedKey, updated, admitErr)
		}
		after, afterFound, readErr := b.ReadSessionSnapshot(context.Background(), alias)
		if readErr != nil || !afterFound {
			t.Fatalf("ReadSessionSnapshot(after %q) = (found=%v, err=%v)", requestedKey, afterFound, readErr)
		}
		if !reflect.DeepEqual(after, before) {
			t.Fatalf(
				"rejected live admission through %q changed protected snapshot:\n before=%+v\n after=%+v",
				requestedKey,
				before,
				after,
			)
		}
	}
}

func TestJSONLBackendAdmitSessionScope_FailsClosedWhenUnsupportedOrCanceled(t *testing.T) {
	t.Run("unsupported", func(t *testing.T) {
		lower, err := memory.NewJSONLStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = lower.Close() })
		b := session.NewJSONLBackend(struct{ memory.Store }{Store: lower})
		scope := reviewAdmissionScope("prc_unsupported")

		updated, admitErr := b.AdmitSessionScope(context.Background(), session.SessionScopeAdmission{
			Key:   session.BuildSessionKey(*scope),
			Scope: scope,
			Mode:  session.ScopeAdmissionReview,
		})
		if updated || !errors.Is(admitErr, session.ErrScopeAdmissionUnsupported) {
			t.Fatalf("AdmitSessionScope() = (updated=%v, err=%v), want unsupported", updated, admitErr)
		}
		if sessions := lower.ListSessions(); len(sessions) != 0 {
			t.Fatalf("unsupported admission mutated lower store: %v", sessions)
		}
	})

	t.Run("canceled", func(t *testing.T) {
		b := newBackend(t)
		scope := reviewAdmissionScope("prc_canceled")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		updated, err := b.AdmitSessionScope(ctx, session.SessionScopeAdmission{
			Key:   session.BuildSessionKey(*scope),
			Scope: scope,
			Mode:  session.ScopeAdmissionReview,
		})
		if updated || !errors.Is(err, context.Canceled) {
			t.Fatalf("AdmitSessionScope() = (updated=%v, err=%v), want cancellation", updated, err)
		}
		if sessions := b.ListSessions(); len(sessions) != 0 {
			t.Fatalf("canceled admission created sessions: %v", sessions)
		}
	})
}

func TestJSONLBackendAdmitSessionScope_TwoStoresSerializeLiveAndReviewClaims(t *testing.T) {
	dir := t.TempDir()
	storeA, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storeA.Close() })
	storeB, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storeB.Close() })
	backendA := session.NewJSONLBackend(storeA)
	backendB := session.NewJSONLBackend(storeB)

	type result struct {
		mode    session.ScopeAdmissionMode
		updated bool
		err     error
	}
	for iteration := 0; iteration < 20; iteration++ {
		caseID := fmt.Sprintf("prc_two_store_%02d", iteration)
		reviewScope := reviewAdmissionScope(caseID)
		ordinaryScope := ordinaryAdmissionScope("attacker:" + caseID)
		key := session.BuildSessionKey(*reviewScope)
		alias := "review:agent:main:case:" + caseID
		start := make(chan struct{})
		results := make(chan result, 2)

		go func() {
			<-start
			updated, admitErr := backendA.AdmitSessionScope(context.Background(), session.SessionScopeAdmission{
				Key:   key,
				Scope: ordinaryScope,
				Mode:  session.ScopeAdmissionLive,
			})
			results <- result{mode: session.ScopeAdmissionLive, updated: updated, err: admitErr}
		}()
		go func() {
			<-start
			updated, admitErr := backendB.AdmitSessionScope(context.Background(), session.SessionScopeAdmission{
				Key:            key,
				Scope:          reviewScope,
				InitialAliases: []string{alias},
				Mode:           session.ScopeAdmissionReview,
			})
			results <- result{mode: session.ScopeAdmissionReview, updated: updated, err: admitErr}
		}()
		close(start)

		first := <-results
		second := <-results
		winner := first
		loser := second
		if first.err != nil || !first.updated {
			winner, loser = second, first
		}
		if winner.err != nil || !winner.updated {
			t.Fatalf("iteration %d has no winner: first=%+v second=%+v", iteration, first, second)
		}
		if loser.updated || !errors.Is(loser.err, session.ErrScopeAdmissionConflict) {
			t.Fatalf("iteration %d loser = %+v, want scope conflict", iteration, loser)
		}

		snapshot, found, readErr := backendA.ReadSessionSnapshot(context.Background(), key)
		if readErr != nil || !found {
			t.Fatalf("iteration %d snapshot = (found=%v, err=%v)", iteration, found, readErr)
		}
		wantScope := ordinaryScope
		if winner.mode == session.ScopeAdmissionReview {
			wantScope = reviewScope
		}
		if snapshot.Key != key || snapshot.Revision == "" || !reflect.DeepEqual(snapshot.Scope, wantScope) {
			t.Fatalf("iteration %d winner=%d snapshot=%+v", iteration, winner.mode, snapshot)
		}
		if winner.mode == session.ScopeAdmissionReview {
			if !reflect.DeepEqual(snapshot.Aliases, []string{alias}) || backendA.ResolveSessionKey(alias) != key {
				t.Fatalf("iteration %d review aliases = %v", iteration, snapshot.Aliases)
			}
		} else if len(snapshot.Aliases) != 0 {
			t.Fatalf("iteration %d live winner aliases = %v, want none", iteration, snapshot.Aliases)
		}
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
	t.Run("schema", func(t *testing.T) {
		dir := t.TempDir()
		store, err := memory.NewJSONLStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		b := session.NewJSONLBackend(store)
		b.AddMessage("corrupt-meta", "user", "hello")
		if _, err := sessiondb.Bind(store.ThreadStore()).Database().Exec(`CREATE INDEX unexpected_session_index
            ON sessions(summary)`); err != nil {
			t.Fatal(err)
		}
		if err := b.Close(); err != nil {
			t.Fatal(err)
		}
		if reopened, err := memory.NewStore(dir); err == nil || reopened != nil {
			t.Fatalf("corrupt schema reopened as %#v, %v", reopened, err)
		}
	})

	t.Run("nested payload", func(t *testing.T) {
		dir := t.TempDir()
		store, err := memory.NewJSONLStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		b := session.NewJSONLBackend(store)
		b.AddMessage("corrupt-history", "user", "hello")
		conn, err := sessiondb.Bind(store.ThreadStore()).Database().Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(context.Background(), `PRAGMA ignore_check_constraints = ON`); err != nil {
			_ = conn.Close()
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(context.Background(), `UPDATE session_messages
            SET nested_payload = x'7b' WHERE session_key = 'corrupt-history'`); err != nil {
			_ = conn.Close()
			t.Fatal(err)
		}
		_ = conn.Close()
		if err := b.Close(); err != nil {
			t.Fatal(err)
		}
		if reopened, err := memory.NewStore(dir); err == nil || reopened != nil {
			t.Fatalf("corrupt payload reopened as %#v, %v", reopened, err)
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
