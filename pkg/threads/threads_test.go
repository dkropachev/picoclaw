package threads

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = filepath.Join(t.TempDir(), "workspace")
	cfg.Session.Dimensions = []string{"chat"}
	return cfg
}

func TestCreatePicoThreadPersistsSearchableContext(t *testing.T) {
	cfg := testConfig(t)
	store := NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)

	thread, err := store.CreatePicoThread(context.Background(), cfg, CreateRequest{
		Type:        TypeCoding,
		Title:       "Implement launcher tabs",
		SourceQuery: "code in /extra/dkropachev/picoclaw repo: git@github.com:dkropachev/picoclaw.git branch main",
	})
	if err != nil {
		t.Fatalf("CreatePicoThread() error = %v", err)
	}

	if thread.ID == "" {
		t.Fatal("thread ID is empty")
	}
	if thread.Type != TypeCoding {
		t.Fatalf("thread.Type = %q, want %q", thread.Type, TypeCoding)
	}
	if got := thread.Context["location"]; got != "/extra/dkropachev/picoclaw" {
		t.Fatalf("location context = %q", got)
	}
	if got := thread.Context["repo"]; got != "git@github.com:dkropachev/picoclaw.git" {
		t.Fatalf("repo context = %q", got)
	}
	if got := thread.Context["branch"]; got != "main" {
		t.Fatalf("branch context = %q", got)
	}

	items, err := store.Search(SearchOptions{Query: "/extra/dkropachev/picoclaw", Type: TypeCoding})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(items) != 1 || items[0].ID != thread.ID {
		t.Fatalf("Search() = %#v, want created thread", items)
	}
}

func TestSearchRanksUpdatedThreadAndFiltersContext(t *testing.T) {
	cfg := testConfig(t)
	store := NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)

	coding, err := store.CreatePicoThread(context.Background(), cfg, CreateRequest{
		Type:        TypeCoding,
		Title:       "Picoclaw coding",
		Context:     map[string]string{"location": "/extra/dkropachev/picoclaw"},
		SourceQuery: "picoclaw coding",
	})
	if err != nil {
		t.Fatalf("CreatePicoThread(coding) error = %v", err)
	}
	_, err = store.CreatePicoThread(context.Background(), cfg, CreateRequest{
		Type:        TypeReviewing,
		Title:       "Release PR review",
		Context:     map[string]string{"pr": "42"},
		SourceQuery: "review release pr",
	})
	if err != nil {
		t.Fatalf("CreatePicoThread(review) error = %v", err)
	}

	items, err := store.Search(SearchOptions{
		Query: "location:/extra/dkropachev/picoclaw",
		Type:  TypeCoding,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(items) != 1 || items[0].ID != coding.ID {
		t.Fatalf("Search() = %#v, want coding thread", items)
	}
}

func TestListIncludesExistingPicoSessionMetadata(t *testing.T) {
	cfg := testConfig(t)
	dir := ResolveSessionsDir(cfg.Agents.Defaults.Workspace)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}
	allocation := AllocatePicoThread(cfg, "session-existing")
	store.UpsertSessionMeta(
		context.Background(),
		allocation.Key,
		mustMarshalScope(t, allocation.Scope),
		allocation.Aliases,
	)
	if addErr := store.AddFullMessage(context.Background(), allocation.Key, providers.Message{
		Role:    "user",
		Content: "Investigate a websocket regression",
	}); addErr != nil {
		t.Fatalf("AddFullMessage() error = %v", addErr)
	}

	items, err := NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace).Search(SearchOptions{
		Query: "websocket regression",
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if items[0].ID != "session-existing" {
		t.Fatalf("items[0].ID = %q, want session-existing", items[0].ID)
	}
	if items[0].Type != TypeInvestigating {
		t.Fatalf("items[0].Type = %q, want %q", items[0].Type, TypeInvestigating)
	}
}

func TestListExcludesPlainOpaqueSessions(t *testing.T) {
	cfg := testConfig(t)
	dir := ResolveSessionsDir(cfg.Agents.Defaults.Workspace)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}
	key := session.BuildOpaqueSessionKey("agent:main:test:plain")
	if addErr := store.AddFullMessage(context.Background(), key, providers.Message{
		Role:    "user",
		Content: "plain transport session",
	}); addErr != nil {
		t.Fatalf("AddFullMessage() error = %v", addErr)
	}

	items, err := NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace).List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("List() = %#v, want no registered threads", items)
	}
}

func TestAttachCurrentLinksSessionAndCreatesHandoff(t *testing.T) {
	cfg := testConfig(t)
	store := NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)
	thread, err := store.CreatePicoThread(context.Background(), cfg, CreateRequest{
		Type:        TypeCoding,
		Title:       "Fix CI",
		SourceQuery: "fix CI",
	})
	if err != nil {
		t.Fatalf("CreatePicoThread() error = %v", err)
	}

	currentKey := session.BuildOpaqueSessionKey("agent:main:pico:direct:other")
	attached, handoff, err := store.AttachCurrent(context.Background(), AttachRequest{
		ThreadID:        thread.ID,
		SessionKey:      currentKey,
		OriginSessionID: "other",
		Summary:         "User clarified this is the CI thread.",
	})
	if err != nil {
		t.Fatalf("AttachCurrent() error = %v", err)
	}
	if attached.ID != thread.ID {
		t.Fatalf("attached.ID = %q, want %q", attached.ID, thread.ID)
	}
	if handoff.TargetSessionID != thread.UISessionID {
		t.Fatalf("handoff.TargetSessionID = %q, want %q", handoff.TargetSessionID, thread.UISessionID)
	}

	metaStore, err := memory.NewJSONLStore(ResolveSessionsDir(cfg.Agents.Defaults.Workspace))
	if err != nil {
		t.Fatalf("NewJSONLStore() error = %v", err)
	}
	meta, err := metaStore.GetSessionMeta(context.Background(), currentKey)
	if err != nil {
		t.Fatalf("GetSessionMeta() error = %v", err)
	}
	if meta.ThreadID != thread.ID {
		t.Fatalf("meta.ThreadID = %q, want %q", meta.ThreadID, thread.ID)
	}
}

func mustMarshalScope(t *testing.T, scope any) []byte {
	t.Helper()
	data, err := json.Marshal(scope)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	return data
}
