package session

import (
	"context"
	"os"
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestPersistentSessionManagerCompatibilityFacade(t *testing.T) {
	dir := t.TempDir()
	manager := NewSessionManager(dir)
	const key = "persistent"

	created := manager.GetOrCreate(key)
	if created.Key != key || len(created.Messages) != 0 {
		t.Fatalf("created session = %#v", created)
	}
	if again := manager.GetOrCreate(key); again != created {
		t.Fatal("GetOrCreate did not preserve cached identity")
	}
	manager.AddMessage(key, "user", "one")
	manager.AddFullMessage(key, providers.Message{Role: "assistant", Content: "two"})
	manager.AddFullMessage(key, providers.Message{Role: "assistant", ReasoningContent: "transient"})
	if history := manager.GetHistory(key); len(history) != 2 {
		t.Fatalf("history = %#v", history)
	}
	if history := manager.GetHistory("missing"); len(history) != 0 {
		t.Fatalf("missing history = %#v", history)
	}
	manager.SetSummary(key, "summary")
	if got := manager.GetSummary(key); got != "summary" {
		t.Fatalf("summary = %q", got)
	}
	if got := manager.GetSummary("missing"); got != "" {
		t.Fatalf("missing summary = %q", got)
	}
	snapshot, found, err := manager.ReadSessionSnapshot(context.Background(), key)
	if err != nil || !found || len(snapshot.History) != 2 || snapshot.Summary != "summary" {
		t.Fatalf("cached snapshot = (%#v, %v, %v)", snapshot, found, err)
	}
	snapshot, found, err = manager.ReadSessionSnapshot(context.Background(), "missing")
	if err != nil || found || snapshot.Key != "" {
		t.Fatalf("persistent missing snapshot = (%#v, %v, %v)", snapshot, found, err)
	}

	manager.SetHistory(key, []providers.Message{{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"}})
	manager.TruncateHistory(key, 1)
	if history := manager.GetHistory(key); len(history) != 1 || history[0].Content != "b" {
		t.Fatalf("truncated history = %#v", history)
	}
	manager.TruncateHistory(key, 0)
	if history := manager.GetHistory(key); len(history) != 0 {
		t.Fatalf("cleared history = %#v", history)
	}
	if err := manager.Save(key); err != nil {
		t.Fatal(err)
	}
	if err := manager.Save("missing"); err != nil {
		t.Fatal(err)
	}
	keys := manager.ListSessions()
	if len(keys) != 1 || keys[0] != key {
		t.Fatalf("ListSessions() = %v", keys)
	}

	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := NewSessionManager(dir)
	defer reopened.Close()
	if got := reopened.GetSummary(key); got != "summary" {
		t.Fatalf("reopened summary = %q", got)
	}
}

func TestPersistentSessionManagerConstructorFailsClosed(t *testing.T) {
	blocked := t.TempDir() + "/blocked"
	if err := os.WriteFile(blocked, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	deferred := func() (recovered any) {
		defer func() { recovered = recover() }()
		_ = NewSessionManager(blocked)
		return nil
	}()
	if deferred != "open SQLite-backed SessionManager" {
		t.Fatalf("constructor panic = %#v", deferred)
	}
}

func TestSQLiteBackendCompatibilityDelegates(t *testing.T) {
	backend, err := NewPersistentBackend(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	const key = "backend"
	backend.AddMessage(key, "user", "one")
	backend.AddFullMessage(key, providers.Message{Role: "assistant", Content: "two"})
	backend.SetSummary(key, "summary")
	if history := backend.GetHistory(key); len(history) != 2 {
		t.Fatalf("backend history = %#v", history)
	}
	if summary := backend.GetSummary(key); summary != "summary" {
		t.Fatalf("backend summary = %q", summary)
	}
	meta, err := backend.GetSessionMeta(context.Background(), key)
	if err != nil || meta.Key != key || meta.Count != 2 {
		t.Fatalf("backend metadata = (%#v, %v)", meta, err)
	}
	backend.SetHistory(key, []providers.Message{{Role: "user", Content: "replacement"}})
	backend.TruncateHistory(key, 1)
	if err := backend.Save(key); err != nil {
		t.Fatal(err)
	}
	if keys := backend.ListSessions(); len(keys) != 1 || keys[0] != key {
		t.Fatalf("backend sessions = %v", keys)
	}
}
