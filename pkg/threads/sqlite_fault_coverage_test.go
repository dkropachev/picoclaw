package threads

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sipeed/picoclaw/pkg/database"
	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
)

func TestThreadStorePropagatesRelationalFaultsAcrossTypedOperations(t *testing.T) {
	tests := map[string]struct {
		seed     func(*testing.T, Store, *memory.SQLiteStore)
		breakSQL string
		invoke   func(Store) error
	}{
		"search registry": {
			breakSQL: `DROP TABLE threads`,
			invoke:   func(store Store) error { _, err := store.Search(SearchOptions{}); return err },
		},
		"list registry": {
			breakSQL: `DROP TABLE threads`,
			invoke:   func(store Store) error { _, err := store.ListAll(ListOptions{}); return err },
		},
		"get registry": {
			breakSQL: `DROP TABLE threads`,
			invoke:   func(store Store) error { _, _, err := store.Get("id"); return err },
		},
		"get metadata": {
			breakSQL: `DROP TABLE threads`,
			invoke:   func(store Store) error { _, _, err := store.GetMeta("id"); return err },
		},
		"create session resolution": {
			breakSQL: `DROP TABLE sessions`,
			invoke: func(store Store) error {
				_, err := store.CreateThread(t.Context(), CreateRequest{ID: "id", PrimarySessionKey: "session"})
				return err
			},
		},
		"create registry": {
			breakSQL: `DROP TABLE threads`,
			invoke: func(store Store) error {
				_, err := store.CreateThread(t.Context(), CreateRequest{ID: "id", PrimarySessionKey: "session"})
				return err
			},
		},
		"create link": {
			breakSQL: `DROP TABLE session_thread_links`,
			invoke: func(store Store) error {
				_, err := store.CreateThread(t.Context(), CreateRequest{ID: "id", PrimarySessionKey: "session"})
				return err
			},
		},
		"update registry": {
			seed:     seedCoverageThread,
			breakSQL: `DROP TABLE threads`,
			invoke: func(store Store) error {
				_, _, err := store.UpdateThread("thread", UpdateRequest{Title: "x"})
				return err
			},
		},
		"attach registry": {
			seed:     seedCoverageThreadAndOrigin,
			breakSQL: `DROP TABLE threads`,
			invoke: func(store Store) error {
				_, _, err := store.AttachCurrent(t.Context(), AttachRequest{ThreadID: "thread", SessionKey: "origin"})
				return err
			},
		},
		"attach handoff": {
			seed:     seedCoverageThreadAndOrigin,
			breakSQL: `DROP TABLE thread_handoffs`,
			invoke: func(store Store) error {
				_, _, err := store.AttachCurrent(t.Context(), AttachRequest{ThreadID: "thread", SessionKey: "origin"})
				return err
			},
		},
		"attach link": {
			seed:     seedCoverageThreadAndOrigin,
			breakSQL: `DROP TABLE session_thread_links`,
			invoke: func(store Store) error {
				_, _, err := store.AttachCurrent(t.Context(), AttachRequest{ThreadID: "thread", SessionKey: "origin"})
				return err
			},
		},
		"detach resolution": {
			breakSQL: `DROP TABLE sessions`,
			invoke:   func(store Store) error { return store.DetachCurrent("session") },
		},
		"detach link": {
			seed:     seedCoverageThread,
			breakSQL: `DROP TABLE session_thread_links`,
			invoke:   func(store Store) error { return store.DetachCurrent("session") },
		},
		"return handoff": {
			breakSQL: `DROP TABLE thread_handoffs`,
			invoke:   func(store Store) error { _, _, err := store.ReturnToOrigin("handoff"); return err },
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			store, memoryStore := newThreadSQLiteCoverageStore(t)
			if test.seed != nil {
				test.seed(t, store, memoryStore)
			}
			if _, err := threadSessionDatabase(memoryStore).Exec(test.breakSQL); err != nil {
				t.Fatal(err)
			}
			if err := test.invoke(store); err == nil {
				t.Fatal("relational fault was hidden")
			}
		})
	}
}

func TestThreadStoreInputAndHookFailureBoundaries(t *testing.T) {
	store, memoryStore := newThreadSQLiteCoverageStore(t)
	if _, err := store.CreateThread(t.Context(), CreateRequest{}); err == nil {
		t.Fatal("empty primary session accepted")
	}
	if _, _, err := store.AttachCurrent(t.Context(), AttachRequest{}); err == nil {
		t.Fatal("empty attach identity accepted")
	}
	if _, err := store.RegisterCurrent(t.Context(), CreateRequest{}, nil); err == nil {
		t.Fatal("empty current session accepted")
	}
	want := context.Canceled
	store.testHooks = &threadStoreTestHooks{writeThreadMeta: func(ThreadMeta) error { return want }}
	if _, err := store.CreateThread(t.Context(), CreateRequest{
		ID: "hook-create", PrimarySessionKey: "hook-session",
	}); err == nil {
		t.Fatal("create hook failure was hidden")
	}
	store.testHooks = nil
	seedCoverageThread(t, store, memoryStore)
	store.testHooks = &threadStoreTestHooks{writeThreadMeta: func(ThreadMeta) error { return want }}
	if _, _, err := store.UpdateThread("thread", UpdateRequest{Title: "changed"}); err == nil {
		t.Fatal("update hook failure was hidden")
	}
}

func TestThreadStorePropagatesDeepRelationalFaults(t *testing.T) {
	tests := map[string]struct {
		seed     func(*testing.T, Store, *memory.SQLiteStore)
		breakSQL string
		invoke   func(Store) error
	}{
		"metadata context": {
			seedCoverageThread, `DROP TABLE thread_context`,
			func(store Store) error { _, _, err := store.GetMeta("thread"); return err },
		},
		"metadata aliases": {
			seedCoverageThread, `DROP TABLE thread_aliases`,
			func(store Store) error { _, _, err := store.GetMeta("thread"); return err },
		},
		"create children": {
			nil, `DROP TABLE thread_context`,
			func(store Store) error {
				_, err := store.CreateThread(t.Context(), CreateRequest{
					ID: "id", PrimarySessionKey: "session", Context: map[string]string{"repo": "x"},
				})
				return err
			},
		},
		"update children": {
			seedCoverageThread, `DROP TABLE thread_context`,
			func(store Store) error {
				_, _, err := store.UpdateThread("thread", UpdateRequest{Context: map[string]string{"repo": "new"}})
				return err
			},
		},
		"attach scope": {
			seedCoverageThreadAndOrigin, `DROP TABLE session_scopes`,
			func(store Store) error {
				_, _, err := store.AttachCurrent(t.Context(), AttachRequest{ThreadID: "thread", SessionKey: "origin"})
				return err
			},
		},
		"detach scope": {
			seedCoverageThread, `DROP TABLE session_scopes`,
			func(store Store) error { return store.DetachCurrent("session") },
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			store, memoryStore := newThreadSQLiteCoverageStore(t)
			if test.seed != nil {
				test.seed(t, store, memoryStore)
			}
			if _, err := threadSessionDatabase(memoryStore).Exec(test.breakSQL); err != nil {
				t.Fatal(err)
			}
			if err := test.invoke(store); err == nil {
				t.Fatal("deep relational fault was hidden")
			}
		})
	}
}

func TestThreadStoreRemainingUtilityBoundaries(t *testing.T) {
	if store, release, err := (Store{brokerClient: &database.Client{}}).borrowSessionStore(); store != nil ||
		release == nil ||
		database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("unavailable borrowed store = %#v error:%v", store, err)
	}
	_ = threadSessionAdapter(nil)
	if threadSessionDatabase(nil) != nil {
		t.Fatal("nil session store exposed database")
	}
	store, _ := newThreadSQLiteCoverageStore(t)
	if state, err := store.readOrdinarySessionState(t.Context(), "", false); err != nil || state.found {
		t.Fatalf("optional blank ordinary state = %#v, %v", state, err)
	}
	if _, err := store.readOrdinarySessionState(t.Context(), "", true); !errors.Is(err, errSessionMissing) {
		t.Fatalf("required blank ordinary state error = %v", err)
	}
	if state, err := store.readOrdinarySessionState(t.Context(), "missing", false); err != nil || state.found {
		t.Fatalf("optional missing ordinary state = %#v, %v", state, err)
	}
	if _, err := store.readOrdinarySessionState(t.Context(), "missing", true); !errors.Is(err, errSessionMissing) {
		t.Fatalf("required missing ordinary state error = %v", err)
	}
	if err := rejectReviewThreadScope(json.RawMessage(`{`)); err == nil {
		t.Fatal("invalid thread scope accepted")
	}
	if err := rejectReviewThreadScope(json.RawMessage(`{"channel":"review"}`)); !errors.Is(err, errReviewScope) {
		t.Fatalf("review thread scope error = %v", err)
	}
	if err := rejectReviewThreadSessionScope(
		&session.SessionScope{Channel: " REVIEW "},
	); !errors.Is(
		err,
		errReviewScope,
	) {
		t.Fatalf("review session scope error = %v", err)
	}
	if _, err := store.createPicoThreadWithAllocation(
		t.Context(), PicoAllocation{}, CreateRequest{},
	); err == nil {
		t.Fatal("invalid Pico allocation accepted")
	}
	want := errors.New("resolution failed")
	brokerStore := Store{brokerClient: &database.Client{}, brokerResolveErr: want}
	if _, err := brokerStore.CreatePicoThread(t.Context(), nil, CreateRequest{ID: "pico"}); !errors.Is(err, want) {
		t.Fatalf("Pico resolution error = %v", err)
	}
	if _, err := brokerStore.Search(SearchOptions{}); !errors.Is(err, want) {
		t.Fatalf("search resolution error = %v", err)
	}
	threadType, filters := ParseContextFilters("type:code repo:owner/repo http:ignored pr:#42")
	if threadType != TypeCoding || filters["repo"] != "owner/repo" || filters["http"] != "" {
		t.Fatalf("context filters = type:%q filters:%#v", threadType, filters)
	}
	visible := visibleMessages([]providers.Message{
		{Role: "assistant", ReasoningContent: "thought"},
		{Role: "tool", Content: "hidden"},
		{Role: "assistant"},
		{Role: "user", Media: []string{"image"}},
	})
	if len(visible) != 1 || len(visible[0].Media) != 1 {
		t.Fatalf("visible messages = %#v", visible)
	}
	if aliases := normalizeAliases(
		"key",
		[]string{"", "key", " alias ", "alias"},
	); len(aliases) != 1 ||
		aliases[0] != "alias" {
		t.Fatalf("normalized thread aliases = %#v", aliases)
	}
	if normalizeAliases("key", []string{"", "key"}) != nil {
		t.Fatal("empty normalized aliases are non-nil")
	}
}

func newThreadSQLiteCoverageStore(t *testing.T) (Store, *memory.SQLiteStore) {
	t.Helper()
	database.InstallProcessClient(nil)
	workspace := t.TempDir()
	memoryStore, err := memory.NewStore(ResolveSessionsDir(workspace))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memoryStore.Close() })
	store := newBrokerThreadStore(workspace, memory.SessionsStoreID)
	store.brokerStore = memoryStore
	return store, memoryStore
}

func seedCoverageThread(t *testing.T, store Store, _ *memory.SQLiteStore) {
	t.Helper()
	if _, err := store.CreateThread(t.Context(), CreateRequest{
		ID: "thread", PrimarySessionKey: "session", Title: "Thread",
	}); err != nil {
		t.Fatal(err)
	}
}

func seedCoverageThreadAndOrigin(t *testing.T, store Store, memoryStore *memory.SQLiteStore) {
	t.Helper()
	seedCoverageThread(t, store, memoryStore)
	if err := memoryStore.EnsureSessionHistory(t.Context(), "origin"); err != nil {
		t.Fatal(err)
	}
}
