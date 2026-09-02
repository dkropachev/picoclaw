package threads

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
	"github.com/sipeed/picoclaw/pkg/memory"
)

func TestSessionThreadBrokerCataloguesPrimaryAndAgentWorkspaces(t *testing.T) {
	home := t.TempDir()
	primary := filepath.Join(home, "primary")
	agent := filepath.Join(home, "agent-workspace")
	cfg := &config.Config{}
	cfg.Agents.Defaults.Workspace = primary
	cfg.Agents.List = []config.AgentConfig{{ID: "secondary", Workspace: agent}}
	handler, server, client := startThreadBroker(t, home, cfg)
	database.InstallProcessClient(client)
	t.Cleanup(func() { database.InstallProcessClient(nil) })

	primaryMemory, err := memory.NewStore(ResolveSessionsDir(primary))
	if err != nil {
		t.Fatal(err)
	}
	agentMemory, err := memory.NewStore(ResolveSessionsDir(agent))
	if err != nil {
		t.Fatal(err)
	}
	if primaryMemory.StoreID() != memory.SessionsStoreID {
		t.Fatalf("primary StoreID = %q", primaryMemory.StoreID())
	}
	if agentMemory.StoreID() == memory.SessionsStoreID || !agentMemory.StoreID().Valid() {
		t.Fatalf("agent StoreID = %q", agentMemory.StoreID())
	}
	if primaryMemory.StoreID() == agentMemory.StoreID() {
		t.Fatal("distinct configured workspaces share StoreID")
	}
	uncataloged := filepath.Join(home, "uncataloged", "sessions")
	if store, openErr := memory.NewStore(uncataloged); openErr == nil || store != nil ||
		database.CodeOf(openErr) != database.CodeUnauthorized {
		t.Fatalf("uncataloged memory store = %#v, %v", store, openErr)
	}
	if _, statErr := os.Lstat(uncataloged); !os.IsNotExist(statErr) {
		t.Fatalf("uncataloged constructor created local state: %v", statErr)
	}

	primaryThreads := NewStoreFromWorkspace(primary)
	agentThreads := NewStoreFromWorkspace(agent)
	request := CreateRequest{
		ID: "same-id", PrimarySessionKey: "primary-session", Title: "Primary",
	}
	primaryThread, err := primaryThreads.CreateThread(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.PrimarySessionKey = "agent-session"
	request.Title = "Agent"
	agentThread, err := agentThreads.CreateThread(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if primaryThread.ID != agentThread.ID || primaryThread.Title == agentThread.Title {
		t.Fatalf("workspace isolation threads = %#v / %#v", primaryThread, agentThread)
	}
	if got, found, getErr := primaryThreads.Get("same-id"); getErr != nil || !found || got.Title != "Primary" {
		t.Fatalf("primary get = %#v, %v, %v", got, found, getErr)
	}
	if got, found, getErr := agentThreads.Get("same-id"); getErr != nil || !found || got.Title != "Agent" {
		t.Fatalf("agent get = %#v, %v, %v", got, found, getErr)
	}

	const concurrentThreads = 24
	var wait sync.WaitGroup
	errors := make(chan error, concurrentThreads)
	for index := 0; index < concurrentThreads; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, createErr := NewStoreFromWorkspace(primary).CreateThread(
				context.Background(), CreateRequest{
					ID:                fmt.Sprintf("thread-%03d", index),
					PrimarySessionKey: fmt.Sprintf("session-%03d", index),
					Title:             "Concurrent",
				},
			)
			errors <- createErr
		}(index)
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Errorf("concurrent create: %v", err)
		}
	}
	items, err := primaryThreads.ListAll(ListOptions{IncludeDropped: true})
	if err != nil || len(items) != concurrentThreads+1 {
		t.Fatalf("primary list count = %d, %v", len(items), err)
	}

	for _, workspace := range handler.workspaces {
		if workspace.adapter.LocalStore().ThreadStore() == (&memory.SQLiteStore{}).ThreadStore() {
			t.Fatal("broker workspace has no retained pool capability")
		}
	}
	if err := closeThreadBroker(server); err != nil {
		t.Fatal(err)
	}
	for _, workspace := range handler.workspaces {
		if workspace.adapter.LocalStore().ThreadStore() != (&memory.SQLiteStore{}).ThreadStore() {
			t.Fatal("CloseHandler retained pool capability")
		}
	}
}

func TestThreadBrokerPaginatesListsAndFailsClosed(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	cfg := &config.Config{}
	cfg.Agents.Defaults.Workspace = workspace
	handler, server, client := startThreadBroker(t, home, cfg)
	database.InstallProcessClient(client)
	t.Cleanup(func() { database.InstallProcessClient(nil) })

	local := handler.workspaces[memory.SessionsStoreID].store
	for index := 0; index < threadBrokerPageLimit+7; index++ {
		_, err := local.CreateThread(context.Background(), CreateRequest{
			ID:                fmt.Sprintf("paged-%03d", index),
			PrimarySessionKey: fmt.Sprintf("paged-session-%03d", index),
			Title:             "Paged",
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	clientStore := NewStoreFromWorkspace(workspace)
	items, err := clientStore.ListAll(ListOptions{IncludeDropped: true})
	if err != nil || len(items) != threadBrokerPageLimit+7 {
		t.Fatalf("paginated list = %d, %v", len(items), err)
	}

	var response threadBrokerResponse
	err = client.Call(
		context.Background(), memory.SessionsBrokerDomain, memory.SessionsBrokerVersion,
		threadOperationPing,
		threadStoreRequest{StoreID: "workspace.deadbeef.sessions"}, &response,
	)
	if database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("spoofed StoreID error = %v", err)
	}
	if err := closeThreadBroker(server); err != nil {
		t.Fatal(err)
	}
	if _, _, err := clientStore.Get("paged-000"); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("post-shutdown thread error = %v", err)
	}
}

func startThreadBroker(
	t *testing.T,
	home string,
	cfg *config.Config,
) (*BrokerHandler, *database.Server, *database.Client) {
	t.Helper()
	handler, err := NewBrokerHandler(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.StartServer(context.Background(), database.ServerOptions{
		Home: home, Handler: handler, CloseHandler: handler.Close,
	})
	if err != nil {
		_ = handler.Close()
		t.Fatal(err)
	}
	client, err := database.Connect(home)
	if err != nil {
		_ = closeThreadBroker(server)
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeThreadBroker(server) })
	return handler, server, client
}

func closeThreadBroker(server *database.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return server.Close(ctx)
}
