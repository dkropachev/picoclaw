package gitworkspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestInventoryBrokerSerializesClientsAndRetainsPool(t *testing.T) {
	fixture := newInventoryBrokerFixture(t)
	firstClient := fixture.client
	secondClient, err := database.Connect(fixture.home)
	if err != nil {
		t.Fatal(err)
	}
	first := newInventoryBrokerTestManager(t, fixture.root, firstClient)
	second := newInventoryBrokerTestManager(t, fixture.root, secondClient)
	pool := fixture.handler.database

	unlock, err := first.lockInventory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	state, err := first.loadLocked()
	if err != nil {
		t.Fatal(err)
	}
	state.History = append(state.History, brokerTestHistory("shared", "first"))
	err = first.saveLocked(state)
	if err != nil {
		t.Fatal(err)
	}
	unlock()

	unlock, err = second.lockInventory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := second.loadLocked()
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	if len(loaded.History) != 1 || loaded.History[0].Detail != "first" {
		t.Fatalf("shared broker inventory = %#v", loaded.History)
	}
	if fixture.handler.database != pool || pool == nil {
		t.Fatal("broker replaced its retained inventory pool")
	}
}

func TestRuntimeManagerDoesNotFallbackToLocalInventory(t *testing.T) {
	fixture := newInventoryBrokerFixture(t)
	if err := fixture.close(); err != nil {
		t.Fatal(err)
	}
	poisonRoot := filepath.Join(t.TempDir(), ".git-workspaces")
	previous := database.RuntimeClient()
	database.InstallProcessClient(fixture.client)
	t.Cleanup(func() { database.InstallProcessClient(previous) })
	if _, err := NewManager(Options{RootDir: poisonRoot}); err == nil {
		t.Fatal("NewManager() succeeded after broker shutdown")
	}
	if _, err := os.Lstat(filepath.Join(poisonRoot, inventoryDatabaseFilename)); !os.IsNotExist(
		err,
	) {
		t.Fatalf("runtime fallback inventory exists: %v", err)
	}
}

func TestInventoryBrokerLeaseExpiresAndHeartbeatRenews(t *testing.T) {
	fixture := newInventoryBrokerFixture(t)
	fixture.handler.leaseTTL = 90 * time.Millisecond
	manager := newInventoryBrokerTestManager(t, fixture.root, fixture.client)
	unlock, err := manager.lockInventory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	fixture.handler.mu.Lock()
	heartbeatLease := fixture.handler.lease.id
	fixture.handler.mu.Unlock()
	time.Sleep(5 * fixture.handler.leaseTTL)
	fixture.handler.mu.Lock()
	stillHeld := fixture.handler.lease != nil && fixture.handler.lease.id == heartbeatLease
	fixture.handler.mu.Unlock()
	if !stillHeld {
		t.Fatal("heartbeat did not preserve the broker lease")
	}
	unlock()

	response, err := fixture.handler.acquireLease(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	lease := response.(inventoryBrokerLeaseResponse)
	time.Sleep(3 * fixture.handler.leaseTTL)
	if _, err := fixture.handler.renewLease(lease.LeaseID); database.CodeOf(
		err,
	) != database.CodeConflict {
		t.Fatalf("expired lease renewal error = %v", err)
	}
}

func TestInventoryBrokerChunksLargeSnapshots(t *testing.T) {
	fixture := newInventoryBrokerFixture(t)
	manager := newInventoryBrokerTestManager(t, fixture.root, fixture.client)
	unlock, err := manager.lockInventory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	state, err := manager.loadLocked()
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 40; index++ {
		state.History = append(state.History, brokerTestHistory(
			fmt.Sprintf("page-%02d", index), strings.Repeat("x", 12<<10),
		))
	}
	err = manager.saveLocked(state)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := manager.loadLocked()
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	if len(loaded.History) != 40 || loaded.History[39].Detail != strings.Repeat("x", 12<<10) {
		t.Fatalf("paged inventory history count = %d", len(loaded.History))
	}
	fixture.handler.mu.Lock()
	loadCalls, saveCalls := fixture.handler.loadChunkCalls, fixture.handler.saveChunkCalls
	fixture.handler.mu.Unlock()
	if loadCalls < 3 || saveCalls < 2 {
		t.Fatalf("chunk calls = load:%d save:%d", loadCalls, saveCalls)
	}
}

func TestInventoryProviderAndOfflineMigrationRequireAuthority(t *testing.T) {
	restore := database.SuspendProviderTestAuthority()
	defer restore()
	previous := database.RuntimeClient()
	database.InstallProcessClient(nil)
	defer database.InstallProcessClient(previous)
	root := filepath.Join(t.TempDir(), ".git-workspaces")
	if _, err := NewManager(Options{RootDir: root}); database.CodeOf(
		err,
	) != database.CodeUnauthorized {
		t.Fatalf("unfenced manager error = %v", err)
	}
	if err := RunOfflineDatabaseMigration(t.Context(), root); database.CodeOf(
		err,
	) != database.CodeConflict {
		t.Fatalf("unfenced migration error = %v", err)
	}
}

type inventoryBrokerFixture struct {
	t       *testing.T
	home    string
	root    string
	handler *BrokerHandler
	server  *database.Server
	client  *database.Client
	closed  bool
}

func newInventoryBrokerFixture(t *testing.T) *inventoryBrokerFixture {
	t.Helper()
	home := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = filepath.Join(home, "workspace")
	cfg.GitWorkspaces.RootDir = ""
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
		_ = server.Close(context.Background())
		t.Fatal(err)
	}
	fixture := &inventoryBrokerFixture{
		t: t, home: home, root: filepath.Join(home, "workspace", ".git-workspaces"),
		handler: handler, server: server, client: client,
	}
	t.Cleanup(func() { _ = fixture.close() })
	return fixture
}

func (fixture *inventoryBrokerFixture) close() error {
	if fixture.closed {
		return nil
	}
	fixture.closed = true
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return fixture.server.Close(ctx)
}

func newInventoryBrokerTestManager(
	t *testing.T,
	root string,
	client *database.Client,
) *Manager {
	t.Helper()
	previous := database.RuntimeClient()
	database.InstallProcessClient(client)
	manager, err := NewManager(Options{RootDir: root})
	database.InstallProcessClient(previous)
	if err != nil {
		t.Fatal(err)
	}
	if manager.StoreID() != InventoryStoreID || manager.broker != client {
		t.Fatalf("runtime manager broker identity = %q/%p", manager.StoreID(), manager.broker)
	}
	return manager
}

func brokerTestHistory(id, detail string) HistoryEntry {
	return HistoryEntry{
		ID: id, Time: time.Unix(1_700_000_000, 0).UTC(), Action: "broker-test", Detail: detail,
	}
}
