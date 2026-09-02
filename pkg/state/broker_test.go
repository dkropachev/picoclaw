package state

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
)

func TestRuntimeStateBrokerLazilyIsolatesMigrationRequiredWorkspace(t *testing.T) {
	home := t.TempDir()
	primary := filepath.Join(home, "primary")
	sibling := filepath.Join(home, "sibling")
	if err := os.MkdirAll(sibling, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(sibling, "state.json")
	if err := os.WriteFile(legacyPath, []byte(`{"last_channel":"legacy"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Agents.Defaults.Workspace = primary
	cfg.Agents.List = []config.AgentConfig{{ID: "sibling", Workspace: sibling}}
	handler, err := NewBrokerHandler(home, cfg)
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}
	t.Cleanup(func() { _ = handler.Close() })
	for id, workspace := range handler.workspaces {
		if workspace.manager != nil {
			t.Fatalf("constructor opened %q", id)
		}
	}
	for _, path := range []string{
		filepath.Join(primary, "state", runtimeDatabaseFilename),
		filepath.Join(sibling, "state", runtimeDatabaseFilename),
	} {
		if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
			t.Fatalf("constructor touched %q: %v", path, statErr)
		}
	}
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fence.Close() })
	if _, err = handler.Handle(t.Context(), runtimeBrokerTestRequest(
		t, runtimeStateOperationPreflight, runtimeStateTarget{StoreID: RuntimeStateStoreID},
	)); err != nil {
		t.Fatalf("primary preflight: %v", err)
	}
	primaryManager := handler.workspaces[RuntimeStateStoreID].manager
	if primaryManager == nil || primaryManager.retained == nil || primaryManager.retained.db == nil {
		t.Fatal("primary preflight did not retain its pool")
	}
	primaryPool := primaryManager.retained.db
	var siblingID database.StoreID
	for id := range handler.workspaces {
		if id != RuntimeStateStoreID {
			siblingID = id
		}
	}
	if !siblingID.Valid() {
		t.Fatal("sibling StoreID missing")
	}
	if _, err = handler.Handle(t.Context(), runtimeBrokerTestRequest(
		t, runtimeStateOperationPreflight, runtimeStateTarget{StoreID: siblingID},
	)); database.CodeOf(err) != database.CodeMigrationRequired {
		t.Fatalf("sibling preflight error = %v", err)
	}
	if handler.workspaces[siblingID].manager != nil {
		t.Fatal("failed sibling open was retained")
	}
	if _, err = handler.Handle(t.Context(), runtimeBrokerTestRequest(
		t,
		runtimeStateOperationSetLastChannel,
		runtimeStateSetRequest{StoreID: RuntimeStateStoreID, Value: "ready"},
	)); err != nil {
		t.Fatalf("primary mutation after sibling failure: %v", err)
	}
	response, err := handler.Handle(t.Context(), runtimeBrokerTestRequest(
		t, runtimeStateOperationSnapshot, runtimeStateTarget{StoreID: RuntimeStateStoreID},
	))
	if err != nil || response.(runtimeStateSnapshotResponse).State.LastChannel != "ready" {
		t.Fatalf("primary snapshot after sibling failure = %#v, %v", response, err)
	}
	if handler.workspaces[RuntimeStateStoreID].manager.retained.db != primaryPool {
		t.Fatal("primary retained pool changed")
	}
	if _, statErr := os.Lstat(legacyPath); statErr != nil {
		t.Fatalf("failed sibling migration source changed: %v", statErr)
	}
}

func runtimeBrokerTestRequest(t *testing.T, operation string, payload any) database.Request {
	t.Helper()
	raw, err := database.MarshalCanonical(payload)
	if err != nil {
		t.Fatal(err)
	}
	return database.Request{
		Domain: RuntimeStateDomain, Version: RuntimeStateVersion,
		Operation: operation, Payload: raw,
	}
}

func TestRuntimeStateBrokerConfiguredWorkspaceIsolation(t *testing.T) {
	home := t.TempDir()
	primary := filepath.Join(home, "primary")
	agent := filepath.Join(home, "agent")
	cfg := &config.Config{}
	cfg.Agents.Defaults.Workspace = primary
	cfg.Agents.List = []config.AgentConfig{{ID: "secondary", Workspace: agent}}
	handler, server, client := startRuntimeStateBroker(t, home, cfg)
	database.InstallProcessClient(client)
	t.Cleanup(func() { database.InstallProcessClient(nil) })

	primaryManager, err := NewSQLiteManager(primary)
	if err != nil {
		t.Fatal(err)
	}
	agentManager, err := NewSQLiteManager(agent)
	if err != nil {
		t.Fatal(err)
	}
	if primaryManager.StoreID() != RuntimeStateStoreID {
		t.Fatalf("primary StoreID = %q", primaryManager.StoreID())
	}
	if agentManager.StoreID() == RuntimeStateStoreID || !agentManager.StoreID().Valid() {
		t.Fatalf("agent StoreID = %q", agentManager.StoreID())
	}
	if primaryManager.StoreID() == agentManager.StoreID() {
		t.Fatal("configured workspaces share StoreID")
	}

	var wait sync.WaitGroup
	for index := 0; index < 24; index++ {
		wait.Add(2)
		go func(index int) {
			defer wait.Done()
			if err := primaryManager.SetLastChannel(fmt.Sprintf("primary-%02d", index)); err != nil {
				t.Errorf("primary set: %v", err)
			}
		}(index)
		go func(index int) {
			defer wait.Done()
			if err := agentManager.SetLastChatID(fmt.Sprintf("agent-%02d", index)); err != nil {
				t.Errorf("agent set: %v", err)
			}
		}(index)
	}
	wait.Wait()
	if primaryManager.GetLastChannel() == "" || primaryManager.GetLastChatID() != "" {
		t.Fatalf("primary state = channel %q chat %q", primaryManager.GetLastChannel(), primaryManager.GetLastChatID())
	}
	if agentManager.GetLastChatID() == "" || agentManager.GetLastChannel() != "" {
		t.Fatalf("agent state = channel %q chat %q", agentManager.GetLastChannel(), agentManager.GetLastChatID())
	}

	primaryPool := handler.workspaces[RuntimeStateStoreID].manager.retained.db
	agentPool := handler.workspaces[agentManager.StoreID()].manager.retained.db
	if primaryPool == nil || agentPool == nil || primaryPool == agentPool {
		t.Fatal("handler did not retain one distinct pool per workspace")
	}
	if err := primaryManager.Close(); err != nil {
		t.Fatal(err)
	}
	if handler.workspaces[RuntimeStateStoreID].manager.retained.db != primaryPool {
		t.Fatal("runtime manager Close changed broker pool")
	}
	if err := closeRuntimeStateBroker(server); err != nil {
		t.Fatal(err)
	}
	if handler.workspaces[RuntimeStateStoreID].manager.retained.db != nil ||
		handler.workspaces[agentManager.StoreID()].manager.retained.db != nil {
		t.Fatal("CloseHandler retained runtime-state pool")
	}
}

func TestRuntimeStateBrokerRejectsUncatalogedAndSpoofedStoresWithoutFallback(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	cfg := &config.Config{}
	cfg.Agents.Defaults.Workspace = workspace
	_, server, client := startRuntimeStateBroker(t, home, cfg)
	database.InstallProcessClient(client)
	t.Cleanup(func() { database.InstallProcessClient(nil) })

	uncataloged := filepath.Join(home, "uncataloged")
	if manager, err := NewSQLiteManager(uncataloged); err == nil || manager != nil ||
		database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("uncataloged manager = %#v, %v", manager, err)
	}
	compatibility := NewManager(uncataloged)
	if err := compatibility.SetLastChannel("must-fail"); database.CodeOf(
		errorsUnwrap(err),
	) != database.CodeUnauthorized {
		t.Fatalf("compatibility manager error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(uncataloged, "state")); !os.IsNotExist(err) {
		t.Fatalf("uncataloged manager created local state: %v", err)
	}

	var response runtimeStateSnapshotResponse
	err := client.Call(
		context.Background(), RuntimeStateDomain, RuntimeStateVersion,
		runtimeStateOperationSnapshot,
		runtimeStateTarget{StoreID: "workspace.deadbeef.runtime-state"}, &response,
	)
	if database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("spoofed StoreID error = %v", err)
	}
	manager, err := NewSQLiteManager(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := closeRuntimeStateBroker(server); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetLastChatID("after-close"); database.CodeOf(errorsUnwrap(err)) != database.CodeUnavailable {
		t.Fatalf("post-shutdown error = %v", err)
	}
}

func startRuntimeStateBroker(
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
		_ = closeRuntimeStateBroker(server)
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeRuntimeStateBroker(server) })
	return handler, server, client
}

func closeRuntimeStateBroker(server *database.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return server.Close(ctx)
}

func errorsUnwrap(err error) error {
	for err != nil {
		type unwrapper interface{ Unwrap() error }
		wrapped, ok := err.(unwrapper)
		if !ok || wrapped.Unwrap() == nil {
			return err
		}
		err = wrapped.Unwrap()
	}
	return nil
}
