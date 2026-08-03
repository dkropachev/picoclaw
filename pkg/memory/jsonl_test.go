package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
)

var errInjectedSnapshotWrite = errors.New("injected snapshot write failure")

func newTestStore(t *testing.T) *JSONLStore {
	t.Helper()
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONLStore: %v", err)
	}
	return store
}

func TestNewJSONLStore_CreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "sessions")
	store, err := NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore: %v", err)
	}
	defer store.Close()

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("expected directory, got file")
	}
}

func TestAddMessage_BasicRoundtrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	err := store.AddMessage(ctx, "s1", "user", "hello")
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	err = store.AddMessage(ctx, "s1", "assistant", "hi there")
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	history, err := store.GetHistory(ctx, "s1")
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(history))
	}
	if history[0].Role != "user" || history[0].Content != "hello" {
		t.Errorf("msg[0] = %+v", history[0])
	}
	if history[1].Role != "assistant" || history[1].Content != "hi there" {
		t.Errorf("msg[1] = %+v", history[1])
	}
}

func TestAddMessage_AutoCreatesSession(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Adding a message to a non-existent session should work.
	err := store.AddMessage(ctx, "new-session", "user", "first message")
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	history, err := store.GetHistory(ctx, "new-session")
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("expected 1 message, got %d", len(history))
	}
}

func TestAddMessage_RejectsInvalidCommittedStateWithoutMutation(t *testing.T) {
	tests := map[string]struct {
		corrupt func(*testing.T, *JSONLStore, string, SessionMeta)
		wantErr string
	}{
		"missing metadata key": {
			corrupt: func(t *testing.T, store *JSONLStore, key string, meta SessionMeta) {
				t.Helper()
				meta.Key = ""
				writeRawMetaForTest(t, store, key, meta)
			},
			wantErr: "metadata key is missing",
		},
		"mismatched metadata key": {
			corrupt: func(t *testing.T, store *JSONLStore, key string, meta SessionMeta) {
				t.Helper()
				meta.Key = "different-session"
				writeRawMetaForTest(t, store, key, meta)
			},
			wantErr: "does not match canonical key",
		},
		"negative skip": {
			corrupt: func(t *testing.T, store *JSONLStore, key string, meta SessionMeta) {
				t.Helper()
				meta.Skip = -1
				writeRawMetaForTest(t, store, key, meta)
			},
			wantErr: "skip is negative",
		},
		"negative count": {
			corrupt: func(t *testing.T, store *JSONLStore, key string, meta SessionMeta) {
				t.Helper()
				meta.Count = -1
				writeRawMetaForTest(t, store, key, meta)
			},
			wantErr: "count is negative",
		},
		"skip exceeds count": {
			corrupt: func(t *testing.T, store *JSONLStore, key string, meta SessionMeta) {
				t.Helper()
				meta.Skip = meta.Count + 1
				writeRawMetaForTest(t, store, key, meta)
			},
			wantErr: "exceeds count",
		},
		"invalid selected slot": {
			corrupt: func(t *testing.T, store *JSONLStore, key string, meta SessionMeta) {
				t.Helper()
				meta.HistorySlot = "../outside"
				writeRawMetaForTest(t, store, key, meta)
			},
			wantErr: "invalid history slot",
		},
		"missing selected slot": {
			corrupt: func(t *testing.T, store *JSONLStore, key string, meta SessionMeta) {
				t.Helper()
				meta.HistorySlot = "a"
				writeRawMetaForTest(t, store, key, meta)
			},
			wantErr: "active history slot \"a\" is missing",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			store := newTestStore(t)
			const key = "invalid-append-state"
			if err := store.AddMessage(context.Background(), key, "user", "old"); err != nil {
				t.Fatal(err)
			}
			meta, err := store.GetSessionMeta(context.Background(), key)
			if err != nil {
				t.Fatal(err)
			}
			test.corrupt(t, store, key, meta)
			before := directoryFileBytes(t, store.dir)

			err = store.AddMessage(context.Background(), key, "assistant", "new")
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("AddMessage() error = %v, want %q", err, test.wantErr)
			}
			if after := directoryFileBytes(t, store.dir); !reflect.DeepEqual(after, before) {
				t.Fatalf("rejected append mutated disk\nbefore=%v\nafter=%v", before, after)
			}
		})
	}
}

func TestAddMessage_RepairsValidLegacyHistoryWithoutMetadata(t *testing.T) {
	store := newTestStore(t)
	const key = "legacy-history-without-metadata"
	legacy := providers.Message{Role: "user", Content: "before restart"}
	line, err := encodeJSONLMessage(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(store.jsonlPath(key), append(line, '\n'), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}

	restarted, err := NewJSONLStore(store.dir)
	if err != nil {
		t.Fatal(err)
	}
	if addErr := restarted.AddMessage(
		context.Background(),
		key,
		"assistant",
		"after restart",
	); addErr != nil {
		t.Fatalf("AddMessage() error = %v", addErr)
	}
	history, err := restarted.GetHistory(context.Background(), key)
	if err != nil || len(history) != 2 || history[0].Content != "before restart" ||
		history[1].Content != "after restart" {
		t.Fatalf("repaired history = %+v, err=%v", history, err)
	}
	meta, err := restarted.GetSessionMeta(context.Background(), key)
	if err != nil || meta.Count != 2 || meta.Key != key {
		t.Fatalf("repaired metadata = %+v, err=%v", meta, err)
	}
}

func TestAddFullMessage_WithToolCalls(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	msg := providers.Message{
		Role:    "assistant",
		Content: "Let me search that.",
		ToolCalls: []providers.ToolCall{
			{
				ID:   "call_abc",
				Type: "function",
				Function: &providers.FunctionCall{
					Name:      "web_search",
					Arguments: `{"q":"golang jsonl"}`,
				},
			},
		},
	}

	err := store.AddFullMessage(ctx, "tc", msg)
	if err != nil {
		t.Fatalf("AddFullMessage: %v", err)
	}

	history, err := store.GetHistory(ctx, "tc")
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("expected 1, got %d", len(history))
	}
	if len(history[0].ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(history[0].ToolCalls))
	}
	tc := history[0].ToolCalls[0]
	if tc.ID != "call_abc" {
		t.Errorf("tool call ID = %q", tc.ID)
	}
	if tc.Function == nil || tc.Function.Name != "web_search" {
		t.Errorf("tool call function = %+v", tc.Function)
	}
}

func TestAddFullMessage_PreservesModelName(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	msg := providers.Message{
		Role:      "assistant",
		Content:   "done",
		ModelName: "gpt-5.4-mini",
	}

	if err := store.AddFullMessage(ctx, "model-name", msg); err != nil {
		t.Fatalf("AddFullMessage: %v", err)
	}

	history, err := store.GetHistory(ctx, "model-name")
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("expected 1, got %d", len(history))
	}
	if history[0].ModelName != "gpt-5.4-mini" {
		t.Fatalf("ModelName = %q, want %q", history[0].ModelName, "gpt-5.4-mini")
	}
}

func TestAddFullMessage_ToolCallID(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	msg := providers.Message{
		Role:       "tool",
		Content:    "search results here",
		ToolCallID: "call_abc",
	}

	err := store.AddFullMessage(ctx, "tr", msg)
	if err != nil {
		t.Fatalf("AddFullMessage: %v", err)
	}

	history, err := store.GetHistory(ctx, "tr")
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("expected 1, got %d", len(history))
	}
	if history[0].ToolCallID != "call_abc" {
		t.Errorf("ToolCallID = %q", history[0].ToolCallID)
	}
}

func TestAddFullMessage_DropsTransientAssistantThought(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	err := store.AddFullMessage(ctx, "transient-thought", providers.Message{
		Role:             "assistant",
		ReasoningContent: "internal chain of thought",
	})
	if err != nil {
		t.Fatalf("AddFullMessage: %v", err)
	}

	history, err := store.GetHistory(ctx, "transient-thought")
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history) != 0 {
		t.Fatalf("expected transient thought to be discarded, got %d messages", len(history))
	}
}

func TestGetHistory_EmptySession(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	history, err := store.GetHistory(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if history == nil {
		t.Fatal("expected non-nil empty slice")
	}
	if len(history) != 0 {
		t.Errorf("expected 0 messages, got %d", len(history))
	}
}

func TestGetHistory_Ordering(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		err := store.AddMessage(
			ctx, "order",
			"user",
			string(rune('a'+i)),
		)
		if err != nil {
			t.Fatalf("AddMessage(%d): %v", i, err)
		}
	}

	history, err := store.GetHistory(ctx, "order")
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history) != 5 {
		t.Fatalf("expected 5, got %d", len(history))
	}
	for i := 0; i < 5; i++ {
		expected := string(rune('a' + i))
		if history[i].Content != expected {
			t.Errorf("msg[%d].Content = %q, want %q", i, history[i].Content, expected)
		}
	}
}

func TestSetSummary_GetSummary(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// No summary yet.
	summary, err := store.GetSummary(ctx, "s1")
	if err != nil {
		t.Fatalf("GetSummary: %v", err)
	}
	if summary != "" {
		t.Errorf("expected empty, got %q", summary)
	}

	// Set a summary.
	err = store.SetSummary(ctx, "s1", "talked about Go")
	if err != nil {
		t.Fatalf("SetSummary: %v", err)
	}

	summary, err = store.GetSummary(ctx, "s1")
	if err != nil {
		t.Fatalf("GetSummary: %v", err)
	}
	if summary != "talked about Go" {
		t.Errorf("summary = %q", summary)
	}

	// Update summary.
	err = store.SetSummary(ctx, "s1", "updated summary")
	if err != nil {
		t.Fatalf("SetSummary: %v", err)
	}

	summary, err = store.GetSummary(ctx, "s1")
	if err != nil {
		t.Fatalf("GetSummary: %v", err)
	}
	if summary != "updated summary" {
		t.Errorf("summary = %q", summary)
	}
}

func TestSetHistory_DropsTransientAssistantThought(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	newHistory := []providers.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", ReasoningContent: "internal chain of thought"},
		{Role: "assistant", Content: "visible answer", ReasoningContent: "visible thought"},
	}

	err := store.SetHistory(ctx, "replace", newHistory)
	if err != nil {
		t.Fatalf("SetHistory: %v", err)
	}

	history, err := store.GetHistory(ctx, "replace")
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected transient thought to be removed, got %d messages", len(history))
	}
	if history[0].Role != "user" || history[0].Content != "hello" {
		t.Fatalf("history[0] = %+v, want user/hello", history[0])
	}
	if history[1].Role != "assistant" || history[1].Content != "visible answer" ||
		history[1].ReasoningContent != "visible thought" {
		t.Fatalf("history[1] = %+v, want assistant visible answer with reasoning", history[1])
	}

	meta, err := store.readMeta("replace")
	if err != nil {
		t.Fatalf("readMeta: %v", err)
	}
	activePath, err := store.committedHistoryPath("replace", meta)
	if err != nil {
		t.Fatalf("committedHistoryPath: %v", err)
	}
	data, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatalf("ReadFile(jsonl): %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("jsonl line count = %d, want 2", len(lines))
	}
}

func TestSessionMetaScopeAndAliasesPersist(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	scope := json.RawMessage(`{"version":1,"channel":"telegram","values":{"chat":"group:c1"}}`)
	aliases := []string{"legacy:one", "legacy:one", "canonical"}
	if err := store.UpsertSessionMeta(ctx, "canonical", scope, aliases); err != nil {
		t.Fatalf("UpsertSessionMeta() error = %v", err)
	}

	meta, err := store.GetSessionMeta(ctx, "canonical")
	if err != nil {
		t.Fatalf("GetSessionMeta() error = %v", err)
	}
	var gotScope map[string]any
	if err := json.Unmarshal(meta.Scope, &gotScope); err != nil {
		t.Fatalf("Unmarshal(meta.Scope) error = %v", err)
	}
	var wantScope map[string]any
	if err := json.Unmarshal(scope, &wantScope); err != nil {
		t.Fatalf("Unmarshal(scope) error = %v", err)
	}
	if !reflect.DeepEqual(gotScope, wantScope) {
		t.Fatalf("meta.Scope = %#v, want %#v", gotScope, wantScope)
	}
	if len(meta.Aliases) != 1 || meta.Aliases[0] != "legacy:one" {
		t.Fatalf("meta.Aliases = %#v, want [legacy:one]", meta.Aliases)
	}
}

func TestSessionMetaMutationsRejectNewDuplicateAliasOwnership(t *testing.T) {
	for _, mutation := range []string{"upsert", "update"} {
		t.Run(mutation, func(t *testing.T) {
			store := newTestStore(t)
			ctx := context.Background()
			const (
				alias     = "review:case:owned"
				candidate = "candidate-owner"
			)
			if err := store.UpsertSessionMeta(ctx, "existing-owner", nil, []string{alias}); err != nil {
				t.Fatal(err)
			}
			if err := store.UpsertSessionMeta(ctx, candidate, json.RawMessage(`{"old":true}`), nil); err != nil {
				t.Fatal(err)
			}
			before := directoryFileBytes(t, store.dir)

			var err error
			switch mutation {
			case "upsert":
				err = store.UpsertSessionMeta(
					ctx,
					candidate,
					json.RawMessage(`{"new":true}`),
					[]string{alias},
				)
			case "update":
				err = store.UpdateSessionMeta(ctx, candidate, func(meta *SessionMeta) error {
					meta.Aliases = []string{alias}
					meta.ThreadTitle = "must not persist"
					return nil
				})
			}
			if err == nil || !strings.Contains(err.Error(), "multiple canonical keys") {
				t.Fatalf("%s duplicate alias error = %v", mutation, err)
			}
			if after := directoryFileBytes(t, store.dir); !reflect.DeepEqual(after, before) {
				t.Fatalf("rejected %s mutated disk\nbefore=%v\nafter=%v", mutation, before, after)
			}
		})
	}
}

func TestSessionMetaMutationsRejectOpaqueCanonicalKeyAsAlias(t *testing.T) {
	for _, mutation := range []string{"upsert", "update"} {
		t.Run(mutation, func(t *testing.T) {
			store := newTestStore(t)
			ctx := context.Background()
			canonical := "sk_v1_" + strings.Repeat("a", 64)
			const candidate = "candidate-owner"
			if err := store.UpsertSessionMeta(ctx, canonical, nil, nil); err != nil {
				t.Fatal(err)
			}
			if err := store.UpsertSessionMeta(ctx, candidate, nil, nil); err != nil {
				t.Fatal(err)
			}
			before := directoryFileBytes(t, store.dir)

			var err error
			switch mutation {
			case "upsert":
				err = store.UpsertSessionMeta(ctx, candidate, nil, []string{canonical})
			case "update":
				err = store.UpdateSessionMeta(ctx, candidate, func(meta *SessionMeta) error {
					meta.Aliases = []string{canonical}
					return nil
				})
			}
			if err == nil || !strings.Contains(err.Error(), "canonical session key") {
				t.Fatalf("%s canonical alias error = %v", mutation, err)
			}
			if after := directoryFileBytes(t, store.dir); !reflect.DeepEqual(after, before) {
				t.Fatalf("rejected %s mutated disk\nbefore=%v\nafter=%v", mutation, before, after)
			}
		})
	}
}

func TestSessionMetaMutationsPreservePreexistingSharedAlias(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	const (
		alias = "agent:main:direct:shared-legacy"
		keyA  = "shared-owner-a"
		keyB  = "shared-owner-b"
	)
	for _, key := range []string{keyA, keyB} {
		writeRawMetaForTest(t, store, key, SessionMeta{
			Key:     key,
			Aliases: []string{alias},
		})
	}

	if err := store.UpsertSessionMeta(
		ctx,
		keyA,
		json.RawMessage(`{"updated":true}`),
		[]string{alias},
	); err != nil {
		t.Fatalf("UpsertSessionMeta() preserving shared alias error = %v", err)
	}
	if err := store.UpdateSessionMeta(ctx, keyA, func(meta *SessionMeta) error {
		meta.ThreadTitle = "updated"
		return nil
	}); err != nil {
		t.Fatalf("UpdateSessionMeta() preserving shared alias error = %v", err)
	}
	meta, err := store.GetSessionMeta(ctx, keyA)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(meta.Aliases, []string{alias}) || meta.ThreadTitle != "updated" {
		t.Fatalf("preserved shared alias metadata = %+v", meta)
	}
}

func TestUpdateSessionMetaPreservesUntouchedLegacyAliasesWithoutCatalogScan(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	const key = "legacy-alias-shape-owner"
	wantAliases := []string{" padded-legacy ", key, "duplicate", "duplicate"}
	writeRawMetaForTest(t, store, key, SessionMeta{Key: key, Aliases: wantAliases})
	if err := os.WriteFile(
		store.metaPath("unrelated-corrupt"),
		[]byte("not-json"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	if err := store.UpdateSessionMeta(ctx, key, func(meta *SessionMeta) error {
		meta.ThreadTitle = "thread-owned update"
		return nil
	}); err != nil {
		t.Fatalf("UpdateSessionMeta() error = %v", err)
	}
	meta, err := store.readMeta(key)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(meta.Aliases, wantAliases) || meta.ThreadTitle != "thread-owned update" {
		t.Fatalf("updated metadata = %+v, want aliases %q", meta, wantAliases)
	}
}

func TestResolveSessionKeyByAlias(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if err := store.AddMessage(ctx, "canonical", "user", "hello"); err != nil {
		t.Fatalf("AddMessage() error = %v", err)
	}
	if err := store.UpsertSessionMeta(ctx, "canonical", nil, []string{"legacy:key"}); err != nil {
		t.Fatalf("UpsertSessionMeta() error = %v", err)
	}

	resolved, found, err := store.ResolveSessionKey(ctx, "legacy:key")
	if err != nil {
		t.Fatalf("ResolveSessionKey() error = %v", err)
	}
	if !found {
		t.Fatal("ResolveSessionKey() did not find alias")
	}
	if resolved != "canonical" {
		t.Fatalf("resolved = %q, want %q", resolved, "canonical")
	}
}

func TestResolveSessionKeyByAlias_PrefersMetadataOverLegacyFile(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if err := store.AddMessage(ctx, "legacy:key", "user", "legacy"); err != nil {
		t.Fatalf("AddMessage(legacy) error = %v", err)
	}
	if err := store.AddMessage(ctx, "canonical", "user", "canonical"); err != nil {
		t.Fatalf("AddMessage(canonical) error = %v", err)
	}
	if err := store.UpsertSessionMeta(ctx, "canonical", nil, []string{"legacy:key"}); err != nil {
		t.Fatalf("UpsertSessionMeta() error = %v", err)
	}

	resolved, found, err := store.ResolveSessionKey(ctx, "legacy:key")
	if err != nil {
		t.Fatalf("ResolveSessionKey() error = %v", err)
	}
	if !found {
		t.Fatal("ResolveSessionKey() did not find alias")
	}
	if resolved != "canonical" {
		t.Fatalf("resolved = %q, want %q", resolved, "canonical")
	}
}

func TestResolveSessionKey_DirectHitSkipsCorruptMetadata(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if err := store.AddMessage(ctx, "canonical", "user", "hello"); err != nil {
		t.Fatalf("AddMessage() error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(store.dir, "broken.meta.json"),
		[]byte("{not-json"),
		0o644,
	); err != nil {
		t.Fatalf("WriteFile(broken.meta.json) error = %v", err)
	}

	resolved, found, err := store.ResolveSessionKey(ctx, "canonical")
	if err != nil {
		t.Fatalf("ResolveSessionKey() error = %v", err)
	}
	if !found {
		t.Fatal("ResolveSessionKey() did not find direct session")
	}
	if resolved != "canonical" {
		t.Fatalf("resolved = %q, want %q", resolved, "canonical")
	}
}

func TestResolveSessionKey_SkipsCorruptMetadataDuringAliasScan(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if err := store.AddMessage(ctx, "canonical", "user", "hello"); err != nil {
		t.Fatalf("AddMessage() error = %v", err)
	}
	if err := store.UpsertSessionMeta(ctx, "canonical", nil, []string{"legacy:key"}); err != nil {
		t.Fatalf("UpsertSessionMeta() error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(store.dir, "broken.meta.json"),
		[]byte("{not-json"),
		0o644,
	); err != nil {
		t.Fatalf("WriteFile(broken.meta.json) error = %v", err)
	}

	resolved, found, err := store.ResolveSessionKey(ctx, "legacy:key")
	if err != nil {
		t.Fatalf("ResolveSessionKey() error = %v", err)
	}
	if !found {
		t.Fatal("ResolveSessionKey() did not find alias")
	}
	if resolved != "canonical" {
		t.Fatalf("resolved = %q, want %q", resolved, "canonical")
	}
}

func TestTruncateHistory_KeepLast(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		err := store.AddMessage(
			ctx, "trunc",
			"user",
			string(rune('a'+i)),
		)
		if err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
	}

	err := store.TruncateHistory(ctx, "trunc", 4)
	if err != nil {
		t.Fatalf("TruncateHistory: %v", err)
	}

	history, err := store.GetHistory(ctx, "trunc")
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history) != 4 {
		t.Fatalf("expected 4, got %d", len(history))
	}
	// Should be the last 4: g, h, i, j
	if history[0].Content != "g" {
		t.Errorf("first kept = %q, want 'g'", history[0].Content)
	}
	if history[3].Content != "j" {
		t.Errorf("last kept = %q, want 'j'", history[3].Content)
	}
}

func TestTruncateHistory_KeepZero(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		err := store.AddMessage(ctx, "empty", "user", "msg")
		if err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
	}

	err := store.TruncateHistory(ctx, "empty", 0)
	if err != nil {
		t.Fatalf("TruncateHistory: %v", err)
	}

	history, err := store.GetHistory(ctx, "empty")
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history) != 0 {
		t.Errorf("expected 0, got %d", len(history))
	}
}

func TestTruncateHistory_KeepMoreThanExists(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		err := store.AddMessage(ctx, "few", "user", "msg")
		if err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
	}

	// Keep 100, but only 3 exist — should keep all.
	err := store.TruncateHistory(ctx, "few", 100)
	if err != nil {
		t.Fatalf("TruncateHistory: %v", err)
	}

	history, err := store.GetHistory(ctx, "few")
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history) != 3 {
		t.Errorf("expected 3, got %d", len(history))
	}
}

func TestSetHistory_ReplacesAll(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Add some initial messages.
	for i := 0; i < 5; i++ {
		err := store.AddMessage(ctx, "replace", "user", "old")
		if err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
	}

	// Replace with new history.
	newHistory := []providers.Message{
		{Role: "user", Content: "new1"},
		{Role: "assistant", Content: "new2"},
	}
	err := store.SetHistory(ctx, "replace", newHistory)
	if err != nil {
		t.Fatalf("SetHistory: %v", err)
	}

	history, err := store.GetHistory(ctx, "replace")
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2, got %d", len(history))
	}
	if history[0].Content != "new1" || history[1].Content != "new2" {
		t.Errorf("history = %+v", history)
	}
}

func TestSetHistory_ResetsSkip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Add messages and truncate.
	for i := 0; i < 10; i++ {
		err := store.AddMessage(ctx, "skip-reset", "user", "old")
		if err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
	}
	err := store.TruncateHistory(ctx, "skip-reset", 3)
	if err != nil {
		t.Fatalf("TruncateHistory: %v", err)
	}

	// SetHistory should reset skip to 0.
	newHistory := []providers.Message{
		{Role: "user", Content: "fresh"},
	}
	err = store.SetHistory(ctx, "skip-reset", newHistory)
	if err != nil {
		t.Fatalf("SetHistory: %v", err)
	}

	history, err := store.GetHistory(ctx, "skip-reset")
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("expected 1, got %d", len(history))
	}
	if history[0].Content != "fresh" {
		t.Errorf("content = %q", history[0].Content)
	}
}

func TestColonInKey(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	err := store.AddMessage(ctx, "telegram:123", "user", "hi")
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	history, err := store.GetHistory(ctx, "telegram:123")
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("expected 1, got %d", len(history))
	}

	// Verify the file is named with underscore.
	jsonlFile := filepath.Join(store.dir, "telegram_123.jsonl")
	if _, statErr := os.Stat(jsonlFile); statErr != nil {
		t.Errorf("expected file %s to exist: %v", jsonlFile, statErr)
	}
}

func TestCompact_RemovesSkippedMessages(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Write 10 messages, then truncate to keep last 3.
	for i := 0; i < 10; i++ {
		err := store.AddMessage(ctx, "compact", "user", string(rune('a'+i)))
		if err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
	}
	err := store.TruncateHistory(ctx, "compact", 3)
	if err != nil {
		t.Fatalf("TruncateHistory: %v", err)
	}

	// Before compact: file still has 10 lines.
	allOnDisk, err := readMessages(store.jsonlPath("compact"), 0)
	if err != nil {
		t.Fatalf("readMessages: %v", err)
	}
	if len(allOnDisk) != 10 {
		t.Fatalf("before compact: expected 10 on disk, got %d", len(allOnDisk))
	}

	// Compact.
	err = store.Compact(ctx, "compact")
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// After compact: file should have only 3 lines.
	meta, err := store.readMeta("compact")
	if err != nil {
		t.Fatalf("readMeta: %v", err)
	}
	activePath, err := store.committedHistoryPath("compact", meta)
	if err != nil {
		t.Fatalf("committedHistoryPath: %v", err)
	}
	allOnDisk, err = readMessages(activePath, 0)
	if err != nil {
		t.Fatalf("readMessages: %v", err)
	}
	if len(allOnDisk) != 3 {
		t.Fatalf("after compact: expected 3 on disk, got %d", len(allOnDisk))
	}

	// GetHistory should still return the same 3 messages.
	history, err := store.GetHistory(ctx, "compact")
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("expected 3, got %d", len(history))
	}
	if history[0].Content != "h" || history[2].Content != "j" {
		t.Errorf("wrong content: %+v", history)
	}
}

func TestCompact_NoOpWhenNoSkip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		err := store.AddMessage(ctx, "noop", "user", "msg")
		if err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
	}

	// Compact without prior truncation — should be a no-op.
	err := store.Compact(ctx, "noop")
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}

	history, err := store.GetHistory(ctx, "noop")
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history) != 5 {
		t.Errorf("expected 5, got %d", len(history))
	}
}

func TestCompact_NoOpValidatesSelectedHistory(t *testing.T) {
	for name, slot := range map[string]string{
		"invalid selector": "../outside",
		"missing slot":     "a",
	} {
		t.Run(name, func(t *testing.T) {
			store := newTestStore(t)
			const key = "compact-invalid-selection"
			writeRawMetaForTest(t, store, key, SessionMeta{
				Key:         key,
				HistorySlot: slot,
			})
			before := directoryFileBytes(t, store.dir)

			if err := store.Compact(context.Background(), key); err == nil {
				t.Fatal("Compact() error = nil")
			}
			if after := directoryFileBytes(t, store.dir); !reflect.DeepEqual(after, before) {
				t.Fatalf("rejected compact mutated disk\nbefore=%v\nafter=%v", before, after)
			}
		})
	}
}

func TestCompact_ThenAppend(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 8; i++ {
		err := store.AddMessage(ctx, "cap", "user", string(rune('a'+i)))
		if err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
	}

	err := store.TruncateHistory(ctx, "cap", 2)
	if err != nil {
		t.Fatalf("TruncateHistory: %v", err)
	}
	err = store.Compact(ctx, "cap")
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// Append after compaction should work correctly.
	err = store.AddMessage(ctx, "cap", "user", "new")
	if err != nil {
		t.Fatalf("AddMessage after compact: %v", err)
	}

	history, err := store.GetHistory(ctx, "cap")
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("expected 3, got %d", len(history))
	}
	// g, h (kept from truncation), new (appended after compaction).
	if history[0].Content != "g" {
		t.Errorf("first = %q, want 'g'", history[0].Content)
	}
	if history[2].Content != "new" {
		t.Errorf("last = %q, want 'new'", history[2].Content)
	}
}

func TestTruncateHistory_StaleMetaCount(t *testing.T) {
	// Simulates a crash between JSONL append and meta update in addMsg:
	// file has N+1 lines but meta.Count is still N. TruncateHistory must
	// reconcile with the real line count so that keepLast is accurate.
	store := newTestStore(t)
	ctx := context.Background()

	// Write 10 messages normally (meta.Count = 10).
	for i := 0; i < 10; i++ {
		err := store.AddMessage(ctx, "stale", "user", string(rune('a'+i)))
		if err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
	}

	// Simulate crash: append a line to JSONL but do NOT update meta.
	// This leaves meta.Count = 10 while the file has 11 lines.
	jsonlPath := store.jsonlPath("stale")
	f, err := os.OpenFile(jsonlPath, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	_, err = f.WriteString(`{"role":"user","content":"orphan"}` + "\n")
	if err != nil {
		t.Fatalf("write orphan: %v", err)
	}
	f.Close()

	// TruncateHistory(keepLast=4) should keep the last 4 of 11 lines,
	// not the last 4 of 10.
	err = store.TruncateHistory(ctx, "stale", 4)
	if err != nil {
		t.Fatalf("TruncateHistory: %v", err)
	}

	history, err := store.GetHistory(ctx, "stale")
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history) != 4 {
		t.Fatalf("expected 4, got %d", len(history))
	}
	// Last 4 of [a,b,c,d,e,f,g,h,i,j,orphan] = [h,i,j,orphan]
	if history[0].Content != "h" {
		t.Errorf("first kept = %q, want 'h'", history[0].Content)
	}
	if history[3].Content != "orphan" {
		t.Errorf("last kept = %q, want 'orphan'", history[3].Content)
	}
}

func TestTruncateHistory_IgnoresTransientThoughtForKeepLast(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	sessionKey := "transient-keep-last"
	now := time.Now()

	rawJSONL := strings.Join([]string{
		`{"role":"user","content":"a"}`,
		`{"role":"assistant","content":"b"}`,
		`{"role":"assistant","content":"","reasoning_content":"dangling thought"}`,
		`{"role":"user","content":"c"}`,
		`{"role":"assistant","content":"d"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(store.jsonlPath(sessionKey), []byte(rawJSONL), 0o644); err != nil {
		t.Fatalf("WriteFile(jsonl): %v", err)
	}
	if err := store.writeMeta(sessionKey, SessionMeta{
		Key:       sessionKey,
		Count:     5,
		Skip:      0,
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("writeMeta: %v", err)
	}

	if err := store.TruncateHistory(ctx, sessionKey, 2); err != nil {
		t.Fatalf("TruncateHistory: %v", err)
	}

	history, err := store.GetHistory(ctx, sessionKey)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 retained messages, got %d", len(history))
	}
	if history[0].Content != "c" || history[1].Content != "d" {
		t.Fatalf("kept history = %+v, want c,d", history)
	}

	meta, err := store.readMeta(sessionKey)
	if err != nil {
		t.Fatalf("readMeta: %v", err)
	}
	if meta.Skip != 2 {
		t.Fatalf("meta.Skip = %d, want 2 raw lines skipped", meta.Skip)
	}
}

func TestCrashRecovery_PartialLine(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Write a valid message first.
	err := store.AddMessage(ctx, "crash", "user", "valid")
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	// Simulate a crash by appending a partial JSON line directly.
	jsonlPath := store.jsonlPath("crash")
	f, err := os.OpenFile(jsonlPath, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	_, err = f.WriteString(`{"role":"user","content":"incomple`)
	if err != nil {
		t.Fatalf("write partial: %v", err)
	}
	f.Close()

	// GetHistory should return only the valid message.
	history, err := store.GetHistory(ctx, "crash")
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("expected 1 valid message, got %d", len(history))
	}
	if history[0].Content != "valid" {
		t.Errorf("content = %q", history[0].Content)
	}
}

func TestPersistence_AcrossInstances(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Write with first instance.
	store1, err := NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore: %v", err)
	}
	err = store1.AddMessage(ctx, "persist", "user", "remember me")
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	err = store1.SetSummary(ctx, "persist", "a test session")
	if err != nil {
		t.Fatalf("SetSummary: %v", err)
	}
	store1.Close()

	// Read with second instance.
	store2, err := NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore: %v", err)
	}
	defer store2.Close()

	history, err := store2.GetHistory(ctx, "persist")
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history) != 1 || history[0].Content != "remember me" {
		t.Errorf("history = %+v", history)
	}

	summary, err := store2.GetSummary(ctx, "persist")
	if err != nil {
		t.Fatalf("GetSummary: %v", err)
	}
	if summary != "a test session" {
		t.Errorf("summary = %q", summary)
	}
}

func TestConcurrent_AddAndRead(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	var wg sync.WaitGroup
	const goroutines = 10
	const msgsPerGoroutine = 20

	// Concurrent writes.
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < msgsPerGoroutine; i++ {
				_ = store.AddMessage(ctx, "concurrent", "user", "msg")
			}
		}()
	}
	wg.Wait()

	history, err := store.GetHistory(ctx, "concurrent")
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	expected := goroutines * msgsPerGoroutine
	if len(history) != expected {
		t.Errorf("expected %d messages, got %d", expected, len(history))
	}
}

func TestConcurrent_SummarizeRace(t *testing.T) {
	// Simulates the #704 race: one goroutine adds messages while
	// another truncates + sets summary — like summarizeSession().
	store := newTestStore(t)
	ctx := context.Background()

	// Seed with some messages.
	for i := 0; i < 20; i++ {
		err := store.AddMessage(ctx, "race", "user", "seed")
		if err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
	}

	var wg sync.WaitGroup

	// Writer goroutine (main agent loop).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_ = store.AddMessage(ctx, "race", "user", "new")
		}
	}()

	// Summarizer goroutine (background task).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			_ = store.SetSummary(ctx, "race", "summary")
			_ = store.TruncateHistory(ctx, "race", 5)
		}
	}()

	wg.Wait()

	// Verify the store is still in a consistent state.
	_, err := store.GetHistory(ctx, "race")
	if err != nil {
		t.Fatalf("GetHistory after race: %v", err)
	}
	_, err = store.GetSummary(ctx, "race")
	if err != nil {
		t.Fatalf("GetSummary after race: %v", err)
	}
}

func TestMultipleSessions_Isolation(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	err := store.AddMessage(ctx, "s1", "user", "msg for s1")
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	err = store.AddMessage(ctx, "s2", "user", "msg for s2")
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	h1, err := store.GetHistory(ctx, "s1")
	if err != nil {
		t.Fatalf("GetHistory s1: %v", err)
	}
	h2, err := store.GetHistory(ctx, "s2")
	if err != nil {
		t.Fatalf("GetHistory s2: %v", err)
	}

	if len(h1) != 1 || h1[0].Content != "msg for s1" {
		t.Errorf("s1 history = %+v", h1)
	}
	if len(h2) != 1 || h2[0].Content != "msg for s2" {
		t.Errorf("s2 history = %+v", h2)
	}
}

func TestStore_SetsCreatedAtWhenNil(t *testing.T) {
	type writeOp struct {
		name string
		fn   func(store *JSONLStore, key string) (expectedCount int)
	}

	ops := []writeOp{
		{
			name: "AddMessage",
			fn: func(store *JSONLStore, key string) int {
				if err := store.AddMessage(context.Background(), key, "user", "hello"); err != nil {
					t.Fatalf("AddMessage: %v", err)
				}
				return 1
			},
		},
		{
			name: "AddFullMessage",
			fn: func(store *JSONLStore, key string) int {
				if err := store.AddFullMessage(context.Background(), key, providers.Message{
					Role:    "user",
					Content: "hello from full",
				}); err != nil {
					t.Fatalf("AddFullMessage: %v", err)
				}
				return 1
			},
		},
		{
			name: "SetHistory",
			fn: func(store *JSONLStore, key string) int {
				if err := store.SetHistory(context.Background(), key, []providers.Message{
					{Role: "user", Content: "msg1"},
					{Role: "assistant", Content: "msg2"},
				}); err != nil {
					t.Fatalf("SetHistory: %v", err)
				}
				return 2
			},
		},
	}

	for _, op := range ops {
		t.Run(op.name, func(t *testing.T) {
			store := newTestStore(t)
			key := "s1"

			before := time.Now().Add(-time.Second)
			expectedCount := op.fn(store, key)
			after := time.Now().Add(time.Second)

			history, err := store.GetHistory(context.Background(), key)
			if err != nil {
				t.Fatalf("GetHistory: %v", err)
			}
			if len(history) != expectedCount {
				t.Fatalf("expected %d messages, got %d", expectedCount, len(history))
			}
			for i := range history {
				if history[i].CreatedAt == nil || history[i].CreatedAt.IsZero() {
					t.Errorf("message %d CreatedAt is zero — not set by %s", i, op.name)
				}
				if history[i].CreatedAt.Before(before) || history[i].CreatedAt.After(after) {
					t.Errorf(
						"message %d CreatedAt %v outside expected window [%v, %v]",
						i, history[i].CreatedAt, before, after,
					)
				}
			}
		})
	}
}

func TestStore_PreservesExistingCreatedAt(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC)

	type writeOp struct {
		name      string
		fn        func(store *JSONLStore, key string)
		wantTimes []time.Time
	}

	ops := []writeOp{
		{
			name: "AddFullMessage",
			fn: func(store *JSONLStore, key string) {
				if err := store.AddFullMessage(context.Background(), key, providers.Message{
					Role:      "user",
					Content:   "custom time",
					CreatedAt: &t1,
				}); err != nil {
					t.Fatalf("AddFullMessage: %v", err)
				}
			},
			wantTimes: []time.Time{t1},
		},
		{
			name: "SetHistory",
			fn: func(store *JSONLStore, key string) {
				if err := store.SetHistory(context.Background(), key, []providers.Message{
					{Role: "user", Content: "msg1", CreatedAt: &t1},
					{Role: "assistant", Content: "msg2", CreatedAt: &t2},
				}); err != nil {
					t.Fatalf("SetHistory: %v", err)
				}
			},
			wantTimes: []time.Time{t1, t2},
		},
	}

	for _, op := range ops {
		t.Run(op.name, func(t *testing.T) {
			store := newTestStore(t)
			key := "s1"

			op.fn(store, key)

			history, err := store.GetHistory(context.Background(), key)
			if err != nil {
				t.Fatalf("GetHistory: %v", err)
			}
			if len(history) != len(op.wantTimes) {
				t.Fatalf("expected %d messages, got %d", len(op.wantTimes), len(history))
			}
			for i, want := range op.wantTimes {
				if history[i].CreatedAt == nil || !history[i].CreatedAt.Equal(want) {
					t.Errorf(
						"message %d CreatedAt = %v, want %v (should preserve caller-provided time)",
						i, history[i].CreatedAt, want,
					)
				}
			}
		})
	}
}

func BenchmarkAddMessage(b *testing.B) {
	dir := b.TempDir()
	store, err := NewJSONLStore(dir)
	if err != nil {
		b.Fatalf("NewJSONLStore: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = store.AddMessage(ctx, "bench", "user", "benchmark message content")
	}
}

func BenchmarkGetHistory_100(b *testing.B) {
	dir := b.TempDir()
	store, err := NewJSONLStore(dir)
	if err != nil {
		b.Fatalf("NewJSONLStore: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	for i := 0; i < 100; i++ {
		_ = store.AddMessage(ctx, "bench", "user", "message content")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = store.GetHistory(ctx, "bench")
	}
}

func BenchmarkGetHistory_1000(b *testing.B) {
	dir := b.TempDir()
	store, err := NewJSONLStore(dir)
	if err != nil {
		b.Fatalf("NewJSONLStore: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	for i := 0; i < 1000; i++ {
		_ = store.AddMessage(ctx, "bench", "user", "message content")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = store.GetHistory(ctx, "bench")
	}
}

func TestReadSessionSnapshot_AliasExactAndNoCreate(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	canonicalKey := "canonical"
	alias := "agent:main:direct:legacy"
	scope := json.RawMessage(`{"version":1,"agent_id":"main"}`)
	if err := store.UpsertSessionMeta(ctx, canonicalKey, scope, []string{alias}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddMessage(ctx, canonicalKey, "user", "review context"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSummary(ctx, canonicalKey, "summary"); err != nil {
		t.Fatal(err)
	}

	key, history, meta, found, err := store.ReadSessionSnapshot(ctx, alias)
	if err != nil || !found {
		t.Fatalf("ReadSessionSnapshot() = (found=%v, err=%v)", found, err)
	}
	if key != canonicalKey || meta.Key != canonicalKey || meta.Summary != "summary" {
		t.Fatalf("identity/meta = (%q, %+v)", key, meta)
	}
	if len(history) != 1 || history[0].Content != "review context" {
		t.Fatalf("history = %+v", history)
	}
	storedScope := string(meta.Scope)
	meta.Scope[0] = 'x'
	meta.Aliases[0] = "mutated"
	storedMeta, err := store.GetSessionMeta(ctx, canonicalKey)
	if err != nil {
		t.Fatal(err)
	}
	if string(storedMeta.Scope) != storedScope || storedMeta.Aliases[0] != alias {
		t.Fatalf("stored metadata changed through snapshot: %+v", storedMeta)
	}

	before, err := os.ReadDir(store.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, missingKey := range []string{"", "   ", "missing"} {
		snapshotKey, snapshotHistory, snapshotMeta, found, snapshotErr := store.ReadSessionSnapshot(ctx, missingKey)
		if snapshotErr != nil || found {
			t.Fatalf(
				"ReadSessionSnapshot(%q) = (key=%q, history=%v, meta=%+v, found=%v, err=%v), want miss",
				missingKey,
				snapshotKey,
				snapshotHistory,
				snapshotMeta,
				found,
				snapshotErr,
			)
		}
	}
	after, err := os.ReadDir(store.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("missing snapshot reads created files: before=%d after=%d", len(before), len(after))
	}
}

func TestReadSessionSnapshot_HistorySummaryMetadataAreCoherent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	const key = "coherent"
	if err := store.AddMessage(ctx, key, "user", "0"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSummary(ctx, key, "0"); err != nil {
		t.Fatal(err)
	}
	lock := store.sessionLock(key)
	lock.Lock()
	meta, err := store.readMeta(key)
	if err == nil {
		meta.ThreadTitle = "0"
		err = store.writeMeta(key, meta)
	}
	lock.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	writerDone := make(chan error, 1)
	go func() {
		for version := 1; version <= 50; version++ {
			value := fmt.Sprintf("%d", version)
			lock := store.sessionLock(key)
			lock.Lock()
			meta, err := store.readMeta(key)
			if err == nil {
				meta.Summary = value
				meta.ThreadTitle = value
				meta.Count = 1
				meta.Skip = 0
				err = store.rewriteJSONL(key, []providers.Message{{Role: "user", Content: value}})
			}
			if err == nil {
				err = store.writeMeta(key, meta)
			}
			lock.Unlock()
			if err != nil {
				writerDone <- err
				return
			}
		}
		writerDone <- nil
	}()

	for {
		_, history, meta, found, err := store.ReadSessionSnapshot(ctx, key)
		if err != nil || !found {
			t.Fatalf("ReadSessionSnapshot() = (found=%v, err=%v)", found, err)
		}
		if len(history) != 1 || history[0].Content != meta.Summary || meta.ThreadTitle != meta.Summary {
			t.Fatalf("torn snapshot: history=%+v meta=%+v", history, meta)
		}

		select {
		case err := <-writerDone:
			if err != nil {
				t.Fatal(err)
			}
			return
		default:
		}
	}
}

func TestReadSessionSnapshot_PropagatesStrictAliasMetadataErrors(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.UpsertSessionMeta(ctx, "canonical", nil, []string{"agent:main:direct:alias"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.dir, "zzz-broken.meta.json"), []byte(`{"key":`), 0o644); err != nil {
		t.Fatal(err)
	}

	key, history, meta, found, err := store.ReadSessionSnapshot(ctx, "agent:main:direct:alias")
	if err == nil || found || !strings.Contains(err.Error(), "decode session metadata") {
		t.Fatalf(
			"strict alias lookup = (key=%q, history=%v, meta=%+v, found=%v, err=%v)",
			key,
			history,
			meta,
			found,
			err,
		)
	}
}

func TestReadSessionSnapshot_RejectsAmbiguousAlias(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	const alias = "agent:main:direct:ambiguous"
	for _, key := range []string{"canonical-a", "canonical-b"} {
		writeRawMetaForTest(t, store, key, SessionMeta{
			Key:     key,
			Aliases: []string{alias},
		})
	}

	key, history, meta, found, err := store.ReadSessionSnapshot(ctx, alias)
	if err == nil || found || !strings.Contains(err.Error(), "multiple canonical keys") {
		t.Fatalf(
			"ambiguous alias lookup = (key=%q, history=%v, meta=%+v, found=%v, err=%v)",
			key,
			history,
			meta,
			found,
			err,
		)
	}
}

func TestReadSessionSnapshot_RejectsAliasChangedAfterResolution(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	const (
		canonicalKey = "canonical"
		alias        = "agent:main:direct:moving"
	)
	if err := store.UpsertSessionMeta(ctx, canonicalKey, nil, []string{alias}); err != nil {
		t.Fatal(err)
	}
	resolved, found, err := store.resolveSessionKeyStrict(ctx, alias)
	if err != nil || !found || resolved != canonicalKey {
		t.Fatalf("resolveSessionKeyStrict() = (%q, %v, %v)", resolved, found, err)
	}
	if updateErr := store.UpsertSessionMeta(ctx, canonicalKey, nil, nil); updateErr != nil {
		t.Fatal(updateErr)
	}

	key, history, meta, found, err := store.readResolvedSessionSnapshot(ctx, alias, resolved)
	if err == nil || found || !strings.Contains(err.Error(), "changed during snapshot lookup") {
		t.Fatalf(
			"stale resolved alias lookup = (key=%q, history=%v, meta=%+v, found=%v, err=%v)",
			key,
			history,
			meta,
			found,
			err,
		)
	}
}

func TestReadSessionSnapshot_RejectsOrphanHistoryFilenameCollision(t *testing.T) {
	store := newTestStore(t)
	const (
		orphanKey  = "agent:main:direct:user/foo"
		requestKey = "agent:main:direct:user_foo"
	)
	if store.jsonlPath(orphanKey) != store.jsonlPath(requestKey) {
		t.Fatal("test keys must collide after filename sanitization")
	}
	encoded, err := json.Marshal(providers.Message{Role: "user", Content: "orphaned history"})
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(store.jsonlPath(orphanKey), append(encoded, '\n'), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}

	key, history, meta, found, err := store.ReadSessionSnapshot(context.Background(), requestKey)
	if err == nil || found || !strings.Contains(err.Error(), "metadata is missing") {
		t.Fatalf(
			"orphan collision lookup = (key=%q, history=%v, meta=%+v, found=%v, err=%v)",
			key,
			history,
			meta,
			found,
			err,
		)
	}
}

func TestReadSessionSnapshot_Canceled(t *testing.T) {
	store := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	key, history, meta, found, err := store.ReadSessionSnapshot(ctx, "missing")
	if err != context.Canceled || found {
		t.Fatalf(
			"canceled snapshot = (key=%q, history=%v, meta=%+v, found=%v, err=%v), want context.Canceled",
			key,
			history,
			meta,
			found,
			err,
		)
	}
}

func snapshotReplacementFor(
	t *testing.T,
	key string,
	alias string,
	marker string,
	expectedRevision string,
) SessionSnapshotReplacement {
	t.Helper()
	scope, err := json.Marshal(map[string]string{"marker": marker})
	if err != nil {
		t.Fatal(err)
	}
	aliases := []string(nil)
	if alias != "" {
		aliases = []string{alias}
	}
	return SessionSnapshotReplacement{
		Key:              key,
		History:          []providers.Message{{Role: "user", Content: marker}},
		Summary:          marker,
		Scope:            scope,
		Aliases:          aliases,
		ExpectedRevision: expectedRevision,
	}
}

func readSnapshotForTest(
	t *testing.T,
	store *JSONLStore,
	key string,
) ([]providers.Message, SessionMeta) {
	t.Helper()
	_, history, meta, found, err := store.ReadSessionSnapshot(context.Background(), key)
	if err != nil || !found {
		t.Fatalf("ReadSessionSnapshot(%q) = (found=%v, err=%v)", key, found, err)
	}
	return history, meta
}

func directoryFileBytes(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := make(map[string]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files[entry.Name()] = string(data)
	}
	return files
}

func writeRawMetaForTest(t *testing.T, store *JSONLStore, key string, meta SessionMeta) {
	t.Helper()
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.metaPath(key), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceSessionSnapshot_LegacyUpgradeRotationAndRestart(t *testing.T) {
	dir := t.TempDir()
	store, storeErr := NewJSONLStore(dir)
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	ctx := context.Background()
	const (
		key   = "snapshot-rotation"
		alias = "review:case:rotation"
	)
	if addErr := store.AddMessage(ctx, key, "user", "legacy"); addErr != nil {
		t.Fatal(addErr)
	}
	if summaryErr := store.SetSummary(ctx, key, "legacy"); summaryErr != nil {
		t.Fatal(summaryErr)
	}
	_, legacyMeta := readSnapshotForTest(t, store, key)
	if legacyMeta.Revision == "" || legacyMeta.HistorySlot != "" {
		t.Fatalf("legacy metadata = %+v", legacyMeta)
	}

	first := snapshotReplacementFor(t, key, alias, "one", legacyMeta.Revision)
	if replaceErr := store.ReplaceSessionSnapshot(ctx, first); replaceErr != nil {
		t.Fatal(replaceErr)
	}
	firstHistory, firstMeta := readSnapshotForTest(t, store, alias)
	if firstMeta.HistorySlot != "a" || firstMeta.Revision == legacyMeta.Revision ||
		len(firstHistory) != 1 || firstHistory[0].Content != "one" {
		t.Fatalf("first snapshot = history=%+v meta=%+v", firstHistory, firstMeta)
	}

	second := snapshotReplacementFor(t, key, alias, "two", firstMeta.Revision)
	if replaceErr := store.ReplaceSessionSnapshot(ctx, second); replaceErr != nil {
		t.Fatal(replaceErr)
	}
	_, secondMeta := readSnapshotForTest(t, store, alias)
	if secondMeta.HistorySlot != "b" {
		t.Fatalf("second history slot = %q, want b", secondMeta.HistorySlot)
	}
	third := snapshotReplacementFor(t, key, alias, "three", secondMeta.Revision)
	if replaceErr := store.ReplaceSessionSnapshot(ctx, third); replaceErr != nil {
		t.Fatal(replaceErr)
	}
	_, thirdMeta := readSnapshotForTest(t, store, alias)
	if thirdMeta.HistorySlot != "a" {
		t.Fatalf("third history slot = %q, want a", thirdMeta.HistorySlot)
	}

	matches, globErr := filepath.Glob(filepath.Join(dir, sanitizeKey(key)+".history-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(matches) != 2 {
		t.Fatalf("history slot files = %v, want exactly two", matches)
	}
	restarted, restartErr := NewJSONLStore(dir)
	if restartErr != nil {
		t.Fatal(restartErr)
	}
	history, restartedMeta := readSnapshotForTest(t, restarted, alias)
	if len(history) != 1 || history[0].Content != "three" ||
		restartedMeta.Summary != "three" || restartedMeta.Revision != thirdMeta.Revision {
		t.Fatalf("restarted snapshot = history=%+v meta=%+v", history, restartedMeta)
	}
}

func TestReplaceSessionSnapshot_MetadataOnlyRepair(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	const key = "metadata-only"
	if err := store.UpsertSessionMeta(ctx, key, json.RawMessage(`{"old":true}`), nil); err != nil {
		t.Fatal(err)
	}
	history, meta := readSnapshotForTest(t, store, key)
	if len(history) != 0 || meta.Revision == "" {
		t.Fatalf("metadata-only snapshot = history=%+v meta=%+v", history, meta)
	}
	if err := store.ReplaceSessionSnapshot(
		ctx,
		snapshotReplacementFor(t, key, "", "repaired", meta.Revision),
	); err != nil {
		t.Fatal(err)
	}
	history, meta = readSnapshotForTest(t, store, key)
	if len(history) != 1 || history[0].Content != "repaired" || meta.HistorySlot != "a" {
		t.Fatalf("repaired snapshot = history=%+v meta=%+v", history, meta)
	}
}

func TestStrictSnapshotRejectsCorruptOffsetsWithoutMutation(t *testing.T) {
	tests := map[string]func(*SessionMeta){
		"negative skip": func(meta *SessionMeta) {
			meta.Skip = -1
		},
		"negative count": func(meta *SessionMeta) {
			meta.Count = -1
		},
		"skip exceeds count": func(meta *SessionMeta) {
			meta.Skip = 2
			meta.Count = 1
		},
		"count exceeds history": func(meta *SessionMeta) {
			meta.Count = 2
		},
	}
	for name, corrupt := range tests {
		t.Run(name, func(t *testing.T) {
			store := newTestStore(t)
			const key = "snapshot-corrupt-offset"
			if createErr := store.ReplaceSessionSnapshot(
				context.Background(),
				snapshotReplacementFor(t, key, "", "old", ""),
			); createErr != nil {
				t.Fatal(createErr)
			}
			_, validMeta := readSnapshotForTest(t, store, key)
			corrupt(&validMeta)
			validMeta.Revision = ""
			writeRawMetaForTest(t, store, key, validMeta)
			before := directoryFileBytes(t, store.dir)

			if _, _, _, found, err := store.ReadSessionSnapshot(
				context.Background(),
				key,
			); err == nil || found {
				t.Fatalf("strict corrupt snapshot = (found=%v, err=%v)", found, err)
			}
			replacement := snapshotReplacementFor(t, key, "", "new", "ssr_v1_pre_corruption")
			if err := store.ReplaceSessionSnapshot(
				context.Background(),
				replacement,
			); err == nil {
				t.Fatal("replacement over corrupt metadata error = nil")
			}
			if after := directoryFileBytes(t, store.dir); !reflect.DeepEqual(after, before) {
				t.Fatalf("rejected corrupt replacement mutated disk\nbefore=%v\nafter=%v", before, after)
			}
		})
	}
}

func TestStrictSnapshotRejectsMissingNonemptyLegacyHistory(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	const key = "snapshot-missing-legacy-history"
	if err := store.UpsertSessionMeta(ctx, key, json.RawMessage(`{"old":true}`), nil); err != nil {
		t.Fatal(err)
	}
	meta, err := store.GetSessionMeta(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	meta.Count = 1
	writeRawMetaForTest(t, store, key, meta)
	before := directoryFileBytes(t, store.dir)

	if _, _, _, found, readErr := store.ReadSessionSnapshot(ctx, key); readErr == nil || found {
		t.Fatalf("strict missing history snapshot = (found=%v, err=%v)", found, readErr)
	}
	replacement := snapshotReplacementFor(t, key, "", "new", "ssr_v1_pre_corruption")
	if replaceErr := store.ReplaceSessionSnapshot(ctx, replacement); replaceErr == nil {
		t.Fatal("replacement over missing committed history error = nil")
	}
	if after := directoryFileBytes(t, store.dir); !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected missing-history replacement mutated disk\nbefore=%v\nafter=%v", before, after)
	}
}

func TestReplaceSessionSnapshot_StaleCASDoesNotMutateDisk(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	const key = "snapshot-stale"
	if err := store.ReplaceSessionSnapshot(ctx, snapshotReplacementFor(t, key, "", "one", "")); err != nil {
		t.Fatal(err)
	}
	_, meta := readSnapshotForTest(t, store, key)
	before := directoryFileBytes(t, store.dir)
	stale := snapshotReplacementFor(t, key, "", "stale", "ssr_v1_stale")
	if err := store.ReplaceSessionSnapshot(ctx, stale); !errors.Is(err, ErrSnapshotConflict) {
		t.Fatalf("stale replacement error = %v, want ErrSnapshotConflict", err)
	}
	if after := directoryFileBytes(t, store.dir); !reflect.DeepEqual(after, before) {
		t.Fatalf("stale replacement mutated disk\nbefore=%v\nafter=%v", before, after)
	}
	_, afterMeta := readSnapshotForTest(t, store, key)
	if afterMeta.Revision != meta.Revision {
		t.Fatalf("revision changed after stale CAS: %q -> %q", meta.Revision, afterMeta.Revision)
	}
}

func TestReplaceSessionSnapshot_ConcurrentFirstWriterWins(t *testing.T) {
	dir := t.TempDir()
	left, err := NewJSONLStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	right, err := NewJSONLStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	const key = "snapshot-first-writer"
	start := make(chan struct{})
	results := make(chan error, 2)
	replacements := map[*JSONLStore]SessionSnapshotReplacement{
		left:  snapshotReplacementFor(t, key, "", "left", ""),
		right: snapshotReplacementFor(t, key, "", "right", ""),
	}
	for store, replacement := range replacements {
		go func() {
			<-start
			results <- store.ReplaceSessionSnapshot(context.Background(), replacement)
		}()
	}
	close(start)
	successes := 0
	conflicts := 0
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrSnapshotConflict):
			conflicts++
		default:
			t.Fatalf("concurrent create error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent results = successes %d, conflicts %d", successes, conflicts)
	}
	history, meta := readSnapshotForTest(t, left, key)
	if len(history) != 1 || history[0].Content != meta.Summary ||
		(history[0].Content != "left" && history[0].Content != "right") {
		t.Fatalf("winning snapshot is torn: history=%+v meta=%+v", history, meta)
	}
}

func TestSessionLockPairOrdersSharedShardsAcrossDirectories(t *testing.T) {
	left := &JSONLStore{dir: "session-lock-order-left"}
	keyA, keyB := "a", "b"
	leftA := left.sessionLockShard(keyA)
	leftB := left.sessionLockShard(keyB)
	if leftA == leftB {
		t.Fatalf("test keys unexpectedly share shard %d", leftA)
	}

	var right *JSONLStore
	for candidate := 0; candidate < 100_000; candidate++ {
		store := &JSONLStore{dir: fmt.Sprintf("session-lock-order-right-%d", candidate)}
		if store.sessionLockShard(keyA) == leftB &&
			store.sessionLockShard(keyB) == leftA &&
			store.directoryLock() != left.directoryLock() {
			right = store
			break
		}
	}
	if right == nil {
		t.Fatal("could not construct inverted shared-shard mapping")
	}

	wantFirst, wantSecond := leftA, leftB
	if wantFirst > wantSecond {
		wantFirst, wantSecond = wantSecond, wantFirst
	}
	for name, store := range map[string]*JSONLStore{"left": left, "right": right} {
		first, second := store.orderedSessionLockShards(keyA, keyB)
		if first != wantFirst || second != wantSecond {
			t.Fatalf(
				"%s ordered shards = (%d, %d), want (%d, %d)",
				name,
				first,
				second,
				wantFirst,
				wantSecond,
			)
		}
		unlock := store.lockSessionPair(keyA, keyB)
		unlock()
	}
}

func TestReplaceSessionSnapshot_FailuresPreserveCoherentCommit(t *testing.T) {
	tests := map[string]func(*testing.T, *JSONLStore, context.Context, context.CancelFunc){
		"history write": func(t *testing.T, store *JSONLStore, _ context.Context, _ context.CancelFunc) {
			store.hooks.writeHistory = func(string, []byte, os.FileMode) error {
				return errInjectedSnapshotWrite
			}
		},
		"metadata before commit": func(t *testing.T, store *JSONLStore, _ context.Context, _ context.CancelFunc) {
			store.hooks.writeMeta = func(string, []byte, os.FileMode) error {
				return errInjectedSnapshotWrite
			}
		},
		"canceled after history": func(t *testing.T, store *JSONLStore, _ context.Context, cancel context.CancelFunc) {
			writeHistory := store.hooks.writeHistory
			store.hooks.writeHistory = func(path string, data []byte, mode os.FileMode) error {
				if writeErr := writeHistory(path, data, mode); writeErr != nil {
					return writeErr
				}
				cancel()
				return nil
			}
		},
	}
	for name, inject := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			store, storeErr := NewJSONLStore(dir)
			if storeErr != nil {
				t.Fatal(storeErr)
			}
			if createErr := store.ReplaceSessionSnapshot(
				context.Background(),
				snapshotReplacementFor(t, "snapshot-failure", "", "old", ""),
			); createErr != nil {
				t.Fatal(createErr)
			}
			_, oldMeta := readSnapshotForTest(t, store, "snapshot-failure")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			inject(t, store, ctx, cancel)
			replaceErr := store.ReplaceSessionSnapshot(
				ctx,
				snapshotReplacementFor(t, "snapshot-failure", "", "new", oldMeta.Revision),
			)
			if replaceErr == nil {
				t.Fatal("replacement error = nil, want injected failure")
			}

			restarted, restartErr := NewJSONLStore(dir)
			if restartErr != nil {
				t.Fatal(restartErr)
			}
			history, meta := readSnapshotForTest(t, restarted, "snapshot-failure")
			if len(history) != 1 || history[0].Content != "old" || meta.Summary != "old" ||
				meta.Revision != oldMeta.Revision {
				t.Fatalf("failed replacement became visible: history=%+v meta=%+v", history, meta)
			}
		})
	}
}

func TestReplaceSessionSnapshot_PostCommitErrorIsCoherentAndUncertain(t *testing.T) {
	dir := t.TempDir()
	store, storeErr := NewJSONLStore(dir)
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	const key = "snapshot-post-commit"
	if createErr := store.ReplaceSessionSnapshot(
		context.Background(),
		snapshotReplacementFor(t, key, "", "old", ""),
	); createErr != nil {
		t.Fatal(createErr)
	}
	_, oldMeta := readSnapshotForTest(t, store, key)
	writeMeta := store.hooks.writeMeta
	store.hooks.writeMeta = func(path string, data []byte, mode os.FileMode) error {
		if writeErr := writeMeta(path, data, mode); writeErr != nil {
			return writeErr
		}
		return errInjectedSnapshotWrite
	}
	replaceErr := store.ReplaceSessionSnapshot(
		context.Background(),
		snapshotReplacementFor(t, key, "", "new", oldMeta.Revision),
	)
	if !errors.Is(replaceErr, errInjectedSnapshotWrite) {
		t.Fatalf("post-commit error = %v", replaceErr)
	}
	restarted, restartErr := NewJSONLStore(dir)
	if restartErr != nil {
		t.Fatal(restartErr)
	}
	history, newMeta := readSnapshotForTest(t, restarted, key)
	if len(history) != 1 || history[0].Content != "new" || newMeta.Summary != "new" ||
		newMeta.Revision == oldMeta.Revision {
		t.Fatalf("post-commit tuple is incoherent: history=%+v meta=%+v", history, newMeta)
	}
	if err := restarted.ReplaceSessionSnapshot(
		context.Background(),
		snapshotReplacementFor(t, key, "", "retry", oldMeta.Revision),
	); !errors.Is(err, ErrSnapshotConflict) {
		t.Fatalf("uncertain stale retry error = %v, want conflict", err)
	}
}

func TestReplaceSessionSnapshot_PostCommitCancellationWithAliasIsUncertain(t *testing.T) {
	store := newTestStore(t)
	const (
		key   = "snapshot-post-commit-cancel"
		alias = "review:case:post-commit-cancel"
	)
	if err := store.ReplaceSessionSnapshot(
		context.Background(),
		snapshotReplacementFor(t, key, alias, "old", ""),
	); err != nil {
		t.Fatal(err)
	}
	_, oldMeta := readSnapshotForTest(t, store, key)
	ctx, cancel := context.WithCancel(context.Background())
	writeMeta := store.hooks.writeMeta
	store.hooks.writeMeta = func(path string, data []byte, mode os.FileMode) error {
		if err := writeMeta(path, data, mode); err != nil {
			return err
		}
		cancel()
		return nil
	}
	err := store.ReplaceSessionSnapshot(
		ctx,
		snapshotReplacementFor(t, key, alias, "new", oldMeta.Revision),
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("post-commit cancellation error = %v", err)
	}
	history, newMeta := readSnapshotForTest(t, store, key)
	if len(history) != 1 || history[0].Content != "new" ||
		newMeta.Summary != "new" || newMeta.Revision == oldMeta.Revision {
		t.Fatalf("post-cancel tuple is incoherent: history=%+v meta=%+v", history, newMeta)
	}
	if retryErr := store.ReplaceSessionSnapshot(
		context.Background(),
		snapshotReplacementFor(t, key, alias, "retry", oldMeta.Revision),
	); !errors.Is(retryErr, ErrSnapshotConflict) {
		t.Fatalf("post-cancel stale retry error = %v, want conflict", retryErr)
	}
}

func TestCommitHistoryFailuresPreserveSelectedTupleAfterRestart(t *testing.T) {
	for _, failurePoint := range []string{"history", "metadata"} {
		t.Run("SetHistory "+failurePoint, func(t *testing.T) {
			dir := t.TempDir()
			store, err := NewJSONLStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			const key = "set-history-commit-failure"
			if createErr := store.ReplaceSessionSnapshot(
				context.Background(),
				snapshotReplacementFor(t, key, "", "old", ""),
			); createErr != nil {
				t.Fatal(createErr)
			}
			_, before := readSnapshotForTest(t, store, key)
			if failurePoint == "history" {
				store.hooks.writeHistory = func(string, []byte, os.FileMode) error {
					return errInjectedSnapshotWrite
				}
			} else {
				store.hooks.writeMeta = func(string, []byte, os.FileMode) error {
					return errInjectedSnapshotWrite
				}
			}
			if setErr := store.SetHistory(
				context.Background(),
				key,
				[]providers.Message{{Role: "user", Content: "new"}},
			); setErr == nil {
				t.Fatal("SetHistory() error = nil")
			}
			restarted, err := NewJSONLStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			history, after := readSnapshotForTest(t, restarted, key)
			if len(history) != 1 || history[0].Content != "old" ||
				after.Revision != before.Revision {
				t.Fatalf("failed SetHistory changed tuple: history=%+v meta=%+v", history, after)
			}
		})
	}

	t.Run("Compact metadata", func(t *testing.T) {
		dir := t.TempDir()
		store, err := NewJSONLStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		const key = "compact-commit-failure"
		replacement := snapshotReplacementFor(t, key, "", "summary", "")
		replacement.History = []providers.Message{
			{Role: "user", Content: "discarded"},
			{Role: "user", Content: "retained"},
		}
		if replaceErr := store.ReplaceSessionSnapshot(context.Background(), replacement); replaceErr != nil {
			t.Fatal(replaceErr)
		}
		if truncateErr := store.TruncateHistory(context.Background(), key, 1); truncateErr != nil {
			t.Fatal(truncateErr)
		}
		_, before := readSnapshotForTest(t, store, key)
		store.hooks.writeMeta = func(string, []byte, os.FileMode) error {
			return errInjectedSnapshotWrite
		}
		if compactErr := store.Compact(context.Background(), key); compactErr == nil {
			t.Fatal("Compact() error = nil")
		}
		restarted, err := NewJSONLStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		history, after := readSnapshotForTest(t, restarted, key)
		if len(history) != 1 || history[0].Content != "retained" ||
			after.Revision != before.Revision {
			t.Fatalf("failed Compact changed tuple: history=%+v meta=%+v", history, after)
		}
	})
}

func TestReplaceSessionSnapshot_SlotAwareLegacyOperations(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	const key = "snapshot-ordinary-ops"
	if err := store.ReplaceSessionSnapshot(ctx, snapshotReplacementFor(t, key, "", "initial", "")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.jsonlPath(key), []byte("poison legacy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.historySlotPath(key, "b"), []byte("poison inactive\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.AddMessage(ctx, key, "assistant", "appended"); err != nil {
		t.Fatal(err)
	}
	history, err := store.GetHistory(ctx, key)
	if err != nil || len(history) != 2 || history[0].Content != "initial" || history[1].Content != "appended" {
		t.Fatalf("history after append = %+v, err=%v", history, err)
	}
	if truncateErr := store.TruncateHistory(ctx, key, 1); truncateErr != nil {
		t.Fatal(truncateErr)
	}
	if compactErr := store.Compact(ctx, key); compactErr != nil {
		t.Fatal(compactErr)
	}
	history, err = store.GetHistory(ctx, key)
	if err != nil || len(history) != 1 || history[0].Content != "appended" {
		t.Fatalf("history after compact = %+v, err=%v", history, err)
	}
	if setErr := store.SetHistory(ctx, key, []providers.Message{{Role: "user", Content: "replaced"}}); setErr != nil {
		t.Fatal(setErr)
	}
	history, err = store.GetHistory(ctx, key)
	if err != nil || len(history) != 1 || history[0].Content != "replaced" {
		t.Fatalf("history after SetHistory = %+v, err=%v", history, err)
	}
}

func TestUpdateSessionMeta_CannotChangeHistoryOwnedFields(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	const key = "snapshot-metadata-guard"
	if err := store.ReplaceSessionSnapshot(ctx, snapshotReplacementFor(t, key, "", "old", "")); err != nil {
		t.Fatal(err)
	}
	_, before := readSnapshotForTest(t, store, key)
	err := store.UpdateSessionMeta(ctx, key, func(meta *SessionMeta) error {
		meta.HistorySlot = "b"
		meta.Skip = 99
		meta.Count = 99
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "history-owned") {
		t.Fatalf("UpdateSessionMeta() error = %v", err)
	}
	history, after := readSnapshotForTest(t, store, key)
	if len(history) != 1 || history[0].Content != "old" || after.Revision != before.Revision {
		t.Fatalf("rejected metadata update changed tuple: history=%+v meta=%+v", history, after)
	}
}

func TestReplaceSessionSnapshot_SlottedAliasPromotion(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	const (
		canonical = "snapshot-promoted"
		alias     = "legacy-slotted"
	)
	if err := store.ReplaceSessionSnapshot(ctx, snapshotReplacementFor(t, alias, "", "from-slot", "")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.jsonlPath(alias), []byte("poison legacy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	promoted, err := store.PromoteAliasHistory(ctx, canonical, nil, []string{alias})
	if err != nil || !promoted {
		t.Fatalf("PromoteAliasHistory() = (%v, %v)", promoted, err)
	}
	history, err := store.GetHistory(ctx, canonical)
	if err != nil || len(history) != 1 || history[0].Content != "from-slot" {
		t.Fatalf("promoted history = %+v, err=%v", history, err)
	}
}

func TestReplaceSessionSnapshot_PreservesPromotedLegacyAliasShadow(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	const (
		canonical = "snapshot-promoted-legacy"
		alias     = "agent:main:direct:promoted-legacy"
	)
	if err := store.AddMessage(ctx, alias, "user", "legacy history"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSummary(ctx, alias, "legacy summary"); err != nil {
		t.Fatal(err)
	}
	scope := json.RawMessage(`{"agent":"main","kind":"review"}`)
	if err := store.UpsertSessionMeta(ctx, canonical, scope, []string{alias}); err != nil {
		t.Fatal(err)
	}
	promoted, err := store.PromoteAliasHistory(ctx, canonical, scope, []string{alias})
	if err != nil || !promoted {
		t.Fatalf("PromoteAliasHistory() = (%v, %v)", promoted, err)
	}
	_, before := readSnapshotForTest(t, store, canonical)
	replacement := snapshotReplacementFor(t, canonical, alias, "new", before.Revision)
	replacement.Scope = scope
	if err := store.ReplaceSessionSnapshot(ctx, replacement); err != nil {
		t.Fatalf("ReplaceSessionSnapshot() error = %v", err)
	}
	history, after := readSnapshotForTest(t, store, canonical)
	if len(history) != 1 || history[0].Content != "new" ||
		after.Summary != "new" || !slices.Equal(after.Aliases, []string{alias}) {
		t.Fatalf("replacement after legacy promotion = history=%+v meta=%+v", history, after)
	}
}

func TestSlottedSessionMissingOrInvalidSelectorFailsClosed(t *testing.T) {
	for name, slot := range map[string]string{"missing": "a", "invalid": "../../escape"} {
		t.Run(name, func(t *testing.T) {
			store := newTestStore(t)
			meta := SessionMeta{Key: "broken-slot", HistorySlot: slot}
			data, err := json.Marshal(meta)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(store.metaPath(meta.Key), data, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := store.GetHistory(context.Background(), meta.Key); err == nil {
				t.Fatal("GetHistory() error = nil")
			}
			if _, _, _, found, err := store.ReadSessionSnapshot(context.Background(), meta.Key); err == nil || found {
				t.Fatalf("ReadSessionSnapshot() = (found=%v, err=%v)", found, err)
			}
			if err := store.EnsureSessionHistory(context.Background(), meta.Key); err == nil {
				t.Fatal("EnsureSessionHistory() error = nil")
			}
		})
	}
}

func TestEnsureSessionHistoryRejectsMissingNonemptyLegacyHistory(t *testing.T) {
	store := newTestStore(t)
	const key = "missing-nonempty-legacy-history"
	writeRawMetaForTest(t, store, key, SessionMeta{Key: key, Count: 1})

	err := store.EnsureSessionHistory(context.Background(), key)
	if err == nil || !strings.Contains(err.Error(), "missing legacy history") {
		t.Fatalf("EnsureSessionHistory() error = %v", err)
	}
	if _, statErr := os.Stat(store.jsonlPath(key)); !os.IsNotExist(statErr) {
		t.Fatalf("EnsureSessionHistory() created missing nonempty history: %v", statErr)
	}
}

func TestReplaceSessionSnapshot_RejectsNonPersistableMessages(t *testing.T) {
	store := newTestStore(t)
	replacement := snapshotReplacementFor(t, "snapshot-invalid-message", "", "marker", "")
	replacement.History = []providers.Message{{
		Role:             "assistant",
		ReasoningContent: "transient",
	}}
	if err := store.ReplaceSessionSnapshot(context.Background(), replacement); err == nil {
		t.Fatal("transient replacement error = nil")
	}
	replacement = snapshotReplacementFor(t, "snapshot-invalid-message", "", "marker", "")
	replacement.History[0].ToolCalls = []providers.ToolCall{{
		Name:      "runtime-only",
		Arguments: map[string]any{"path": "main.go"},
	}}
	if err := store.ReplaceSessionSnapshot(context.Background(), replacement); err == nil {
		t.Fatal("runtime tool-call replacement error = nil")
	}
	if sessions := store.ListSessions(); len(sessions) != 0 {
		t.Fatalf("invalid replacements created sessions: %v", sessions)
	}
}

func TestReplaceSessionSnapshot_RejectsUnreadableJSONLLine(t *testing.T) {
	store := newTestStore(t)
	replacement := snapshotReplacementFor(t, "snapshot-oversized-line", "", "marker", "")
	replacement.History[0].Content = strings.Repeat("x", maxLineSize)

	err := store.ReplaceSessionSnapshot(context.Background(), replacement)
	if err == nil || !strings.Contains(err.Error(), "maximum JSONL line size") {
		t.Fatalf("oversized replacement error = %v", err)
	}
	if sessions := store.ListSessions(); len(sessions) != 0 {
		t.Fatalf("oversized replacement created sessions: %v", sessions)
	}
}

func TestAddFullMessage_RejectsUnreadableJSONLLineWithoutMutation(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	const key = "snapshot-oversized-append"
	if err := store.ReplaceSessionSnapshot(
		ctx,
		snapshotReplacementFor(t, key, "", "old", ""),
	); err != nil {
		t.Fatal(err)
	}
	_, beforeMeta := readSnapshotForTest(t, store, key)
	beforeFiles := directoryFileBytes(t, store.dir)

	err := store.AddFullMessage(ctx, key, providers.Message{
		Role:    "user",
		Content: strings.Repeat("x", maxLineSize),
	})
	if err == nil || !strings.Contains(err.Error(), "maximum JSONL line size") {
		t.Fatalf("oversized append error = %v", err)
	}
	if afterFiles := directoryFileBytes(t, store.dir); !reflect.DeepEqual(afterFiles, beforeFiles) {
		t.Fatalf("rejected oversized append mutated disk\nbefore=%v\nafter=%v", beforeFiles, afterFiles)
	}
	history, afterMeta := readSnapshotForTest(t, store, key)
	if len(history) != 1 || history[0].Content != "old" ||
		afterMeta.Revision != beforeMeta.Revision {
		t.Fatalf("snapshot changed after rejected append: history=%+v meta=%+v", history, afterMeta)
	}
}

func TestReplaceSessionSnapshot_RejectsAliasRebinding(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	const alias = "review:case:already-bound"
	if err := store.UpsertSessionMeta(ctx, "existing-owner", nil, []string{alias}); err != nil {
		t.Fatal(err)
	}
	err := store.ReplaceSessionSnapshot(
		ctx,
		snapshotReplacementFor(t, "new-owner", alias, "new", ""),
	)
	if err == nil || !strings.Contains(err.Error(), "multiple canonical keys") {
		t.Fatalf("alias rebinding error = %v", err)
	}
	if _, _, _, found, readErr := store.ReadSessionSnapshot(ctx, "new-owner"); readErr != nil || found {
		t.Fatalf("rejected alias rebind created session: found=%v err=%v", found, readErr)
	}
}

func TestReplaceSessionSnapshot_PreservesSharedLegacyAliasWithoutNewClaims(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	const sharedAlias = "agent:main:direct:pico-user"
	for _, key := range []string{"shared-alias-a", "shared-alias-b"} {
		writeRawMetaForTest(t, store, key, SessionMeta{
			Key:     key,
			Scope:   json.RawMessage(`{"version":1}`),
			Aliases: []string{sharedAlias},
		})
		if err := store.AddMessage(ctx, key, "user", key); err != nil {
			t.Fatal(err)
		}
	}
	_, before := readSnapshotForTest(t, store, "shared-alias-a")
	replacement := snapshotReplacementFor(
		t,
		"shared-alias-a",
		sharedAlias,
		"updated",
		before.Revision,
	)
	if err := store.ReplaceSessionSnapshot(ctx, replacement); err != nil {
		t.Fatalf("preserved shared alias replacement error = %v", err)
	}
	history, _ := readSnapshotForTest(t, store, "shared-alias-a")
	if len(history) != 1 || history[0].Content != "updated" {
		t.Fatalf("preserved shared alias history = %+v", history)
	}

	err := store.ReplaceSessionSnapshot(
		ctx,
		snapshotReplacementFor(t, "shared-alias-new-owner", sharedAlias, "new", ""),
	)
	if err == nil || !strings.Contains(err.Error(), "multiple canonical keys") {
		t.Fatalf("new shared alias claim error = %v", err)
	}
	if _, _, _, found, readErr := store.ReadSessionSnapshot(
		ctx,
		"shared-alias-new-owner",
	); readErr != nil || found {
		t.Fatalf("rejected shared alias claim = (found=%v, err=%v)", found, readErr)
	}
}

func TestReplaceSessionSnapshot_MainAliasMustBePreexistingWhenShared(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	const (
		owner = "main-alias-existing-owner"
		alias = "agent:main:main"
	)
	if err := store.UpsertSessionMeta(
		ctx,
		owner,
		json.RawMessage(`{"version":1}`),
		[]string{alias},
	); err != nil {
		t.Fatal(err)
	}
	if err := store.AddMessage(ctx, owner, "user", "before"); err != nil {
		t.Fatal(err)
	}
	_, before := readSnapshotForTest(t, store, owner)
	if err := store.ReplaceSessionSnapshot(
		ctx,
		snapshotReplacementFor(t, owner, alias, "preserved", before.Revision),
	); err != nil {
		t.Fatalf("preserve main alias replacement error = %v", err)
	}

	err := store.ReplaceSessionSnapshot(
		ctx,
		snapshotReplacementFor(t, "main-alias-new-owner", alias, "new", ""),
	)
	if err == nil || !strings.Contains(err.Error(), "multiple canonical keys") {
		t.Fatalf("new shared main alias error = %v", err)
	}
}

func TestReplaceSessionSnapshot_ConcurrentReadersSeeWholeTuples(t *testing.T) {
	store := newTestStore(t)
	const key = "snapshot-concurrent"
	if err := store.ReplaceSessionSnapshot(
		context.Background(),
		snapshotReplacementFor(t, key, "", "0", ""),
	); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	writerErr := make(chan error, 1)
	go func() {
		defer close(done)
		for version := 1; version <= 50; version++ {
			_, _, meta, found, err := store.ReadSessionSnapshot(context.Background(), key)
			if err != nil || !found {
				writerErr <- fmt.Errorf("read current snapshot: found=%v: %w", found, err)
				return
			}
			marker := fmt.Sprintf("%d", version)
			if err := store.ReplaceSessionSnapshot(
				context.Background(),
				snapshotReplacementFor(t, key, "", marker, meta.Revision),
			); err != nil {
				writerErr <- err
				return
			}
		}
		writerErr <- nil
	}()
	for {
		history, meta := readSnapshotForTest(t, store, key)
		var scope map[string]string
		if err := json.Unmarshal(meta.Scope, &scope); err != nil {
			t.Fatal(err)
		}
		if len(history) != 1 || history[0].Content != meta.Summary ||
			scope["marker"] != meta.Summary {
			t.Fatalf("torn tuple: history=%+v meta=%+v scope=%v", history, meta, scope)
		}
		select {
		case <-done:
			if err := <-writerErr; err != nil {
				t.Fatal(err)
			}
			return
		default:
		}
	}
}

func TestAliasMutationHoldsOwnershipThroughWrite(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	const (
		canonical = "alias-atomic-owner"
		alias     = "review:case:atomic-alias"
	)
	if err := store.ReplaceSessionSnapshot(
		ctx,
		snapshotReplacementFor(t, canonical, alias, "before", ""),
	); err != nil {
		t.Fatal(err)
	}
	_, before := readSnapshotForTest(t, store, canonical)

	resolved := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	store.hooks.afterResolve = func(requestedKey, canonicalKey string) {
		if requestedKey != alias || canonicalKey != canonical {
			return
		}
		once.Do(func() { close(resolved) })
		<-release
	}
	addDone := make(chan error, 1)
	go func() {
		addDone <- store.AddMessage(ctx, alias, "assistant", "concurrent append")
	}()
	select {
	case <-resolved:
	case <-time.After(2 * time.Second):
		t.Fatal("alias append did not reach the resolved access boundary")
	}

	replaceDone := make(chan error, 1)
	go func() {
		replacement := snapshotReplacementFor(
			t,
			canonical,
			"",
			"replacement",
			before.Revision,
		)
		replaceDone <- store.ReplaceSessionSnapshot(ctx, replacement)
	}()
	close(release)
	if err := <-addDone; err != nil {
		t.Fatalf("AddMessage() error = %v", err)
	}
	if err := <-replaceDone; !errors.Is(err, ErrSnapshotConflict) {
		t.Fatalf("concurrent replacement error = %v, want conflict", err)
	}
	store.hooks.afterResolve = nil

	history, after := readSnapshotForTest(t, store, canonical)
	if len(history) != 2 || history[1].Content != "concurrent append" ||
		!slices.Equal(after.Aliases, []string{alias}) {
		t.Fatalf("atomic alias mutation tuple = history=%+v meta=%+v", history, after)
	}
}

func TestDeleteSessionsRecoversInterruptedGroupBeforeReopen(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	keys := []string{"delete-canonical", "agent:main:pico:direct:pico:delete-shadow"}
	for _, key := range keys {
		if err := store.AddMessage(ctx, key, "user", key); err != nil {
			t.Fatal(err)
		}
	}

	removeFile := store.hooks.removeFile
	removeCalls := 0
	store.hooks.removeFile = func(path string) error {
		removeCalls++
		if removeCalls == 3 {
			return errInjectedSnapshotWrite
		}
		return removeFile(path)
	}
	deleted, err := store.DeleteSessions(ctx, keys)
	if !deleted || err == nil {
		t.Fatalf("DeleteSessions() = (deleted=%v, err=%v)", deleted, err)
	}
	entries, err := os.ReadDir(store.dir)
	if err != nil {
		t.Fatal(err)
	}
	foundManifest := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), deleteManifestPrefix) {
			foundManifest = true
		}
	}
	if !foundManifest {
		t.Fatal("interrupted deletion did not retain its recovery manifest")
	}

	restarted, err := NewJSONLStore(store.dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() recovery error = %v", err)
	}
	for _, key := range keys {
		for _, path := range restarted.sessionDataPaths(key) {
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Fatalf("recovered deletion retained %s: %v", path, statErr)
			}
		}
	}
	entries, err = os.ReadDir(store.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), deleteManifestPrefix) {
			t.Fatalf("recovered deletion retained manifest %s", entry.Name())
		}
	}
	if deleted, err := restarted.DeleteSessions(ctx, keys); err != nil || deleted {
		t.Fatalf("idempotent DeleteSessions() = (deleted=%v, err=%v)", deleted, err)
	}
}

func TestPendingDeletionBlocksAlreadyOpenStoresUntilRecovery(t *testing.T) {
	storeA := newTestStore(t)
	storeB, err := NewJSONLStore(storeA.dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const key = "pending-delete-generation"
	if addErr := storeA.AddMessage(ctx, key, "user", "old generation"); addErr != nil {
		t.Fatal(addErr)
	}

	storeA.hooks.removeFile = func(string) error {
		return errInjectedSnapshotWrite
	}
	deleted, err := storeA.DeleteSession(ctx, key)
	if !deleted || err == nil {
		t.Fatalf("DeleteSession() = (deleted=%v, err=%v)", deleted, err)
	}
	if addErr := storeB.AddMessage(ctx, key, "user", "must be blocked"); !errors.Is(addErr, ErrPendingSessionDeletion) {
		t.Fatalf("already-open store AddMessage() error = %v, want pending deletion", addErr)
	}

	recoveryStore, err := NewJSONLStore(storeA.dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() recovery error = %v", err)
	}
	if addErr := storeB.AddMessage(ctx, key, "user", "new generation"); addErr != nil {
		t.Fatalf("AddMessage() after recovery error = %v", addErr)
	}
	if closeErr := recoveryStore.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	restarted, err := NewJSONLStore(storeA.dir)
	if err != nil {
		t.Fatal(err)
	}
	history, err := restarted.GetHistory(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Content != "new generation" {
		t.Fatalf("restarted history = %#v", history)
	}
}

func TestDeleteSessionsFinishesVisibleManifestAfterWriteError(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	const key = "delete-visible-manifest"
	if err := store.AddMessage(ctx, key, "user", "delete me"); err != nil {
		t.Fatal(err)
	}

	writeManifest := store.hooks.writeDeleteManifest
	store.hooks.writeDeleteManifest = func(path string, data []byte, mode os.FileMode) error {
		if err := writeManifest(path, data, mode); err != nil {
			return err
		}
		return errInjectedSnapshotWrite
	}
	deleted, err := store.DeleteSessions(ctx, []string{key})
	if err != nil || !deleted {
		t.Fatalf("DeleteSessions() = (deleted=%v, err=%v)", deleted, err)
	}
	for _, path := range store.sessionDataPaths(key) {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("completed deletion retained %s: %v", path, statErr)
		}
	}

	// A caller may reuse the already-open store after the completed delete.
	// No stale manifest may erase that newly created session on restart.
	if addErr := store.AddMessage(ctx, key, "user", "new generation"); addErr != nil {
		t.Fatal(addErr)
	}
	restarted, err := NewJSONLStore(store.dir)
	if err != nil {
		t.Fatal(err)
	}
	history, err := restarted.GetHistory(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Content != "new generation" {
		t.Fatalf("restarted history = %#v", history)
	}
}

func TestDeleteSessionsMatchingRejectsMetadataPathCollisionAsOrphan(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	const (
		candidate = "collision:key"
		owner     = "collision_key"
	)
	if store.metaPath(candidate) != store.metaPath(owner) ||
		store.jsonlPath(candidate) != store.jsonlPath(owner) {
		t.Fatal("test keys do not collide after filename sanitization")
	}
	if err := store.AddMessage(ctx, owner, "user", "keep colliding owner"); err != nil {
		t.Fatal(err)
	}

	deleted, err := store.DeleteSessionsWithAliasesMatching(
		ctx,
		[]string{candidate},
		func(meta SessionMeta, metadataFound bool) bool {
			return !metadataFound && meta.Key == candidate
		},
		nil,
	)
	if err != nil || deleted {
		t.Fatalf("DeleteSessionsWithAliasesMatching() = (deleted=%v, err=%v)", deleted, err)
	}
	history, err := store.GetHistory(ctx, owner)
	if err != nil || len(history) != 1 || history[0].Content != "keep colliding owner" {
		t.Fatalf("colliding owner history = %#v, err=%v", history, err)
	}
}

func TestDeleteSessionsDoesNotCommitAfterCancellationWhileWaiting(t *testing.T) {
	store := newTestStore(t)
	const key = "delete-canceled-before-manifest"
	if err := store.AddMessage(context.Background(), key, "user", "keep me"); err != nil {
		t.Fatal(err)
	}
	before := directoryFileBytes(t, store.dir)

	ctx, cancel := context.WithCancel(context.Background())
	directoryLock := store.directoryLock()
	directoryLock.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := store.DeleteSession(ctx, key)
		done <- err
	}()
	cancel()
	directoryLock.Unlock()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteSession() error = %v, want context.Canceled", err)
	}
	if after := directoryFileBytes(t, store.dir); !reflect.DeepEqual(after, before) {
		t.Fatalf("canceled deletion mutated disk\nbefore=%v\nafter=%v", before, after)
	}
}
