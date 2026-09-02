package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestSessionRuntimeConstructorRequiresBrokerAndDoesNotOpenProvider(t *testing.T) {
	previous := database.RuntimeClient()
	database.InstallProcessClient(nil)
	restoreProviderAuthority := database.SuspendProviderTestAuthority()
	allowUnfencedSessionsProviderForTests.Store(false)
	t.Cleanup(func() {
		allowUnfencedSessionsProviderForTests.Store(true)
		restoreProviderAuthority()
		database.InstallProcessClient(previous)
	})
	dir := filepath.Join(t.TempDir(), "must-not-exist", "sessions")
	onlineFence, err := database.AcquireOnlineFence(filepath.Join(t.TempDir(), "launcher-home"))
	if err != nil {
		t.Fatal(err)
	}
	if local, localErr := openLocalSQLiteStore(t.Context(), dir); local != nil ||
		database.CodeOf(localErr) != database.CodeUnauthorized {
		t.Fatalf("online-fenced local opener = %#v, %v", local, localErr)
	}
	if closeErr := onlineFence.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	store, err := NewStore(dir)
	if store != nil || database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("NewStore() = %#v, %v", store, err)
	}
	if _, statErr := os.Lstat(filepath.Dir(dir)); !os.IsNotExist(statErr) {
		t.Fatalf("runtime constructor touched provider root: %v", statErr)
	}
}

func TestSessionBrokerMultiClientPaginationAndAtomicOperations(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "workspace", "sessions")
	handler, server, client := startSessionBroker(t, home, dir)
	database.InstallProcessClient(client)
	t.Cleanup(func() { database.InstallProcessClient(nil) })

	clients := make([]*SQLiteStore, 8)
	for index := range clients {
		store, err := NewStore(dir)
		if err != nil {
			t.Fatalf("client %d: %v", index, err)
		}
		clients[index] = store
		if store.db != nil || store.brokerClient != client {
			t.Fatalf("client %d opened local storage", index)
		}
	}

	const messagesPerClient = 6
	var wait sync.WaitGroup
	errors := make(chan error, len(clients)*messagesPerClient)
	for clientIndex, store := range clients {
		for messageIndex := 0; messageIndex < messagesPerClient; messageIndex++ {
			wait.Add(1)
			go func(clientIndex, messageIndex int, store *SQLiteStore) {
				defer wait.Done()
				errors <- store.AddFullMessage(context.Background(), "shared", providers.Message{
					Role: "user", Content: fmt.Sprintf("%d/%d", clientIndex, messageIndex),
				})
			}(clientIndex, messageIndex, store)
		}
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Errorf("concurrent add: %v", err)
		}
	}
	history, err := clients[0].GetHistory(context.Background(), "shared")
	if err != nil || len(history) != len(clients)*messagesPerClient {
		t.Fatalf("history length = %d, %v", len(history), err)
	}

	if setErr := clients[1].SetSummary(context.Background(), "shared", "summary"); setErr != nil {
		t.Fatal(setErr)
	}
	if got, getErr := clients[2].GetSummary(context.Background(), "shared"); getErr != nil || got != "summary" {
		t.Fatalf("summary = %q, %v", got, getErr)
	}
	meta, err := clients[3].GetSessionMeta(context.Background(), "shared")
	if err != nil || meta.Count != len(history) {
		t.Fatalf("meta = %#v, %v", meta, err)
	}
	if upsertErr := clients[4].UpsertSessionMeta(
		context.Background(), "shared",
		[]byte(`{"version":1,"agent_id":"main","channel":"pico","account":"","dimensions":[],"values":{}}`),
		[]string{"shared-alias"},
	); upsertErr != nil {
		t.Fatal(upsertErr)
	}
	if key, found, resolveErr := clients[5].ResolveSessionKey(
		context.Background(), "shared-alias",
	); resolveErr != nil || !found || key != "shared" {
		t.Fatalf("resolved alias = %q, %v, %v", key, found, resolveErr)
	}
	if updateErr := clients[6].UpdateSessionMeta(context.Background(), "shared", func(meta *SessionMeta) error {
		meta.Summary = "updated"
		return nil
	}); updateErr != nil {
		t.Fatal(updateErr)
	}
	meta, err = clients[7].GetSessionMeta(context.Background(), "shared")
	if err != nil || meta.Summary != "updated" {
		t.Fatalf("updated meta = %#v, %v", meta, err)
	}

	for index := 0; index < sessionListPageLimit+37; index++ {
		if err := handler.LocalStore().EnsureSessionHistory(
			context.Background(), fmt.Sprintf("paged-%04d", index),
		); err != nil {
			t.Fatal(err)
		}
	}
	keys := clients[0].ListSessions()
	if len(keys) != sessionListPageLimit+38 {
		t.Fatalf("paginated session count = %d, want %d", len(keys), sessionListPageLimit+38)
	}

	pool := handler.store.db
	if pool == nil {
		t.Fatal("broker did not retain local pool")
	}
	for _, store := range clients {
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if handler.store.db != pool {
		t.Fatal("client Close changed broker pool")
	}
	if err := closeSessionBroker(server); err != nil {
		t.Fatal(err)
	}
	if handler.store.db != nil {
		t.Fatal("CloseHandler retained broker pool")
	}
}

func TestSessionBrokerFailureNeverFallsBackToCallerPath(t *testing.T) {
	home := t.TempDir()
	handler, server, client := startSessionBroker(
		t, home, filepath.Join(home, "workspace", "sessions"),
	)
	database.InstallProcessClient(client)
	t.Cleanup(func() { database.InstallProcessClient(nil) })

	poison := filepath.Join(home, "caller-must-not-open")
	if unauthorized, openErr := NewStore(poison); openErr == nil || unauthorized != nil ||
		database.CodeOf(openErr) != database.CodeUnauthorized {
		t.Fatalf("uncataloged constructor = %#v, %v", unauthorized, openErr)
	}
	if _, err := os.Lstat(poison); !os.IsNotExist(err) {
		t.Fatalf("uncataloged constructor created caller path: %v", err)
	}
	store, err := NewStore(filepath.Join(home, "workspace", "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	var response sessionBrokerResponse
	err = client.Call(
		context.Background(), SessionsBrokerDomain, SessionsBrokerVersion,
		sessionOperationPing, sessionStoreRequest{StoreID: "workspace.workflows"}, &response,
	)
	if database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("wrong StoreID error = %v", err)
	}
	if err := closeSessionBroker(server); err != nil {
		t.Fatal(err)
	}
	if err := store.AddMessage(context.Background(), "key", "user", "value"); database.CodeOf(
		err,
	) != database.CodeUnavailable {
		t.Fatalf("post-shutdown error = %v", err)
	}
	if store.db != nil || handler.store.db != nil {
		t.Fatal("failure installed or retained a local pool")
	}
	if _, err := os.Lstat(poison); !os.IsNotExist(err) {
		t.Fatalf("failure created caller path: %v", err)
	}
}

func startSessionBroker(
	t *testing.T,
	home,
	dir string,
) (*BrokerAdapter, *database.Server, *database.Client) {
	t.Helper()
	startupFence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Agents.Defaults.Workspace = filepath.Dir(dir)
	handler, err := NewBrokerAdapter(home, cfg, SessionsStoreID)
	if err != nil {
		_ = startupFence.Close()
		t.Fatal(err)
	}
	server, err := database.StartServer(context.Background(), database.ServerOptions{
		Home: home, Handler: handler, CloseHandler: handler.Close,
	})
	if err != nil {
		_ = handler.Close()
		_ = startupFence.Close()
		t.Fatal(err)
	}
	if closeErr := startupFence.Close(); closeErr != nil {
		_ = server.Close(context.Background())
		t.Fatal(closeErr)
	}
	client, err := database.Connect(home)
	if err != nil {
		_ = closeSessionBroker(server)
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeSessionBroker(server) })
	return handler, server, client
}

func closeSessionBroker(server *database.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return server.Close(ctx)
}
