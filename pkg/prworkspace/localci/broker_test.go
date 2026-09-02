package localci

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestLocalCICacheBrokerLazilyIsolatesMigrationRequiredWorkspace(t *testing.T) {
	home := t.TempDir()
	primary := filepath.Join(home, "primary")
	sibling := filepath.Join(home, "sibling")
	unopenableSibling := filepath.Join(home, "unopenable-sibling")
	cfg := &config.Config{}
	cfg.Agents.Defaults.Workspace = primary
	cfg.Agents.List = []config.AgentConfig{
		{ID: "sibling", Workspace: sibling},
		{ID: "unopenable", Workspace: unopenableSibling},
	}
	configured, err := configuredCacheStores(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	var primaryRoot, siblingRoot, unopenableRoot string
	var siblingID, unopenableID database.StoreID
	for _, item := range configured {
		if item.storeID == CacheStoreID {
			primaryRoot = item.root
		} else if siblingRoot == "" {
			siblingRoot = item.root
			siblingID = item.storeID
		} else {
			unopenableRoot = item.root
			unopenableID = item.storeID
		}
	}
	if primaryRoot == "" || siblingRoot == "" || unopenableRoot == "" ||
		!siblingID.Valid() || !unopenableID.Valid() {
		t.Fatalf("configured stores = %#v", configured)
	}
	legacyDir := filepath.Join(siblingRoot, "cache")
	if mkdirErr := os.MkdirAll(legacyDir, 0o700); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	legacyPath := filepath.Join(legacyDir, stringsOf("a", 64)+".json")
	if writeErr := os.WriteFile(legacyPath, []byte(`{"legacy":true}`), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if mkdirErr := os.MkdirAll(filepath.Dir(unopenableRoot), 0o700); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	if writeErr := os.WriteFile(unopenableRoot, []byte("not a directory"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	handler, err := NewBrokerHandler(home, cfg)
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}
	t.Cleanup(func() { _ = handler.Close() })
	for id, item := range handler.stores {
		if item.store != nil {
			t.Fatalf("constructor opened %q", id)
		}
	}
	for _, path := range []string{
		filepath.Join(primaryRoot, localCICacheDatabaseName),
		filepath.Join(siblingRoot, localCICacheDatabaseName),
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
	if _, err = handler.Handle(t.Context(), cacheBrokerTestRequest(
		t, cacheOperationPreflight, cacheStoreRequest{StoreID: CacheStoreID},
	)); err != nil {
		t.Fatalf("primary preflight: %v", err)
	}
	primaryStore := handler.stores[CacheStoreID].store
	if primaryStore == nil || primaryStore.cacheDB == nil {
		t.Fatal("primary preflight did not retain its pool")
	}
	primaryPool := primaryStore.cacheDB
	if _, err = handler.Handle(t.Context(), cacheBrokerTestRequest(
		t, cacheOperationPreflight, cacheStoreRequest{StoreID: siblingID},
	)); database.CodeOf(err) != database.CodeMigrationRequired {
		t.Fatalf("sibling preflight error = %v", err)
	}
	if handler.stores[siblingID].store != nil {
		t.Fatal("failed sibling open was retained")
	}
	if _, err = handler.Handle(t.Context(), cacheBrokerTestRequest(
		t, cacheOperationPreflight, cacheStoreRequest{StoreID: unopenableID},
	)); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("unopenable sibling preflight error = %v", err)
	}
	if handler.stores[unopenableID].store != nil {
		t.Fatal("unopenable sibling store was retained")
	}
	response, err := handler.Handle(t.Context(), cacheBrokerTestRequest(
		t,
		cacheOperationLookup,
		cacheLookupRequest{StoreID: CacheStoreID, ResultKey: stringsOf("b", 64)},
	))
	if err != nil || response.(cacheResponse).Found {
		t.Fatalf("primary lookup after sibling failure = %#v, %v", response, err)
	}
	if handler.stores[CacheStoreID].store.cacheDB != primaryPool {
		t.Fatal("primary retained pool changed")
	}
	if _, statErr := os.Lstat(legacyPath); statErr != nil {
		t.Fatalf("failed sibling migration source changed: %v", statErr)
	}
}

func cacheBrokerTestRequest(t *testing.T, operation string, payload any) database.Request {
	t.Helper()
	raw, err := database.MarshalCanonical(payload)
	if err != nil {
		t.Fatal(err)
	}
	return database.Request{
		Domain: CacheBrokerDomain, Version: CacheBrokerVersion,
		Operation: operation, Payload: raw,
	}
}

func TestLocalCIRuntimeConstructorRequiresBrokerAndDoesNotOpenProvider(t *testing.T) {
	previous := database.RuntimeClient()
	database.InstallProcessClient(nil)
	restoreProviderAuthority := database.SuspendProviderTestAuthority()
	allowUnfencedLocalCIProviderForTests.Store(false)
	t.Cleanup(func() {
		allowUnfencedLocalCIProviderForTests.Store(true)
		restoreProviderAuthority()
		database.InstallProcessClient(previous)
	})
	root := filepath.Join(t.TempDir(), "must-not-exist")
	onlineFence, err := database.AcquireOnlineFence(filepath.Join(t.TempDir(), "launcher-home"))
	if err != nil {
		t.Fatal(err)
	}
	if local, localErr := openFileEvidenceStoreLocal(root); local != nil ||
		database.CodeOf(localErr) != database.CodeUnauthorized {
		t.Fatalf("online-fenced local opener = %#v, %v", local, localErr)
	}
	if closeErr := onlineFence.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	store, err := OpenFileEvidenceStore(root)
	if store != nil || database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("OpenFileEvidenceStore() = %#v, %v", store, err)
	}
	if _, statErr := os.Lstat(root); !os.IsNotExist(statErr) {
		t.Fatalf("runtime constructor touched provider root: %v", statErr)
	}
}

func TestLocalCICacheBrokerKeepsImmutableEvidenceFileBacked(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	cfg := &config.Config{}
	cfg.Agents.Defaults.Workspace = workspace
	handler, server, client := startLocalCICacheBroker(t, home, cfg)
	database.InstallProcessClient(client)
	t.Cleanup(func() { database.InstallProcessClient(nil) })

	root := filepath.Join(workspace, "eventing", "pr-workspace-local-ci", "evidence")
	first, err := OpenFileEvidenceStore(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := OpenFileEvidenceStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if first.cacheDB != nil || second.cacheDB != nil || first.cacheBroker != client {
		t.Fatal("runtime evidence store opened a local passing-cache database")
	}
	plan := validTestPlan(t)
	if putErr := first.PutPlan(context.Background(), plan); putErr != nil {
		t.Fatal(putErr)
	}
	execution := validTestExecution(t, plan, time.Now().UTC(), StatusPassed)
	if putErr := first.PutExecution(context.Background(), execution); putErr != nil {
		t.Fatal(putErr)
	}
	if promoteErr := first.PromotePassing(
		context.Background(),
		execution.ResultKey,
		execution.Digest,
	); promoteErr != nil {
		t.Fatal(promoteErr)
	}
	cached, found, err := second.LookupPassing(context.Background(), execution.ResultKey)
	if err != nil || !found || cached.Digest != execution.Digest {
		t.Fatalf("broker cache lookup = %#v, %v, %v", cached, found, err)
	}
	item := handler.stores[CacheStoreID]
	pool := item.store.cacheDB
	if pool == nil {
		t.Fatal("broker did not retain passing-cache pool")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if item.store.cacheDB != pool {
		t.Fatal("runtime evidence Close changed broker pool")
	}
	if err := closeLocalCICacheBroker(server); err != nil {
		t.Fatal(err)
	}
	if item.store.cacheDB != nil {
		t.Fatal("CloseHandler retained passing-cache pool")
	}
}

func TestLocalCICacheBrokerRejectsUncatalogedAndSpoofedRoots(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	cfg := &config.Config{}
	cfg.Agents.Defaults.Workspace = workspace
	_, server, client := startLocalCICacheBroker(t, home, cfg)
	database.InstallProcessClient(client)
	t.Cleanup(func() { database.InstallProcessClient(nil) })

	uncataloged := filepath.Join(home, "uncataloged-evidence")
	if store, err := OpenFileEvidenceStore(uncataloged); err == nil || store != nil ||
		database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("uncataloged evidence store = %#v, %v", store, err)
	}
	if _, err := os.Lstat(uncataloged); !os.IsNotExist(err) {
		t.Fatalf("uncataloged cache created local root: %v", err)
	}
	var response cacheResponse
	err := client.Call(
		context.Background(), CacheBrokerDomain, CacheBrokerVersion, cacheOperationLookup,
		cacheLookupRequest{
			StoreID: "workspace.deadbeef.local-ci", ResultKey: string(make([]byte, 64)),
		},
		&response,
	)
	if database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("spoofed StoreID error = %v", err)
	}
	root := filepath.Join(workspace, "eventing", "pr-workspace-local-ci", "evidence")
	store, err := OpenFileEvidenceStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := closeLocalCICacheBroker(server); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.LookupPassing(context.Background(), stringsOf("a", 64)); database.CodeOf(
		err,
	) != database.CodeUnavailable {
		t.Fatalf("post-shutdown lookup error = %v", err)
	}
}

func TestLocalCICacheBrokerConfiguredAgentIsolation(t *testing.T) {
	home := t.TempDir()
	primaryWorkspace := filepath.Join(home, "primary")
	agentWorkspace := filepath.Join(home, "agent")
	cfg := &config.Config{}
	cfg.Agents.Defaults.Workspace = primaryWorkspace
	cfg.Agents.List = []config.AgentConfig{{ID: "secondary", Workspace: agentWorkspace}}
	_, _, client := startLocalCICacheBroker(t, home, cfg)
	database.InstallProcessClient(client)
	t.Cleanup(func() { database.InstallProcessClient(nil) })

	primaryRoot := filepath.Join(
		primaryWorkspace, "eventing", "pr-workspace-local-ci", "evidence",
	)
	agentRoot := filepath.Join(
		agentWorkspace, "eventing", "pr-workspace-local-ci", "evidence",
	)
	primary, err := OpenFileEvidenceStore(primaryRoot)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := OpenFileEvidenceStore(agentRoot)
	if err != nil {
		t.Fatal(err)
	}
	if primary.cacheStoreID != CacheStoreID || agent.cacheStoreID == CacheStoreID ||
		primary.cacheStoreID == agent.cacheStoreID {
		t.Fatalf("cache StoreIDs = %q / %q", primary.cacheStoreID, agent.cacheStoreID)
	}
	plan := validTestPlan(t)
	execution := validTestExecution(t, plan, time.Now().UTC(), StatusPassed)
	for _, store := range []*FileEvidenceStore{primary, agent} {
		if err := store.PutPlan(context.Background(), plan); err != nil {
			t.Fatal(err)
		}
		if err := store.PutExecution(context.Background(), execution); err != nil {
			t.Fatal(err)
		}
	}
	if err := primary.PromotePassing(context.Background(), execution.ResultKey, execution.Digest); err != nil {
		t.Fatal(err)
	}
	if _, found, err := agent.LookupPassing(context.Background(), execution.ResultKey); err != nil || found {
		t.Fatalf("agent observed primary cache = %v, %v", found, err)
	}
	if err := agent.PromotePassing(context.Background(), execution.ResultKey, execution.Digest); err != nil {
		t.Fatal(err)
	}
	if _, found, err := agent.LookupPassing(context.Background(), execution.ResultKey); err != nil || !found {
		t.Fatalf("agent cache lookup = %v, %v", found, err)
	}
}

func startLocalCICacheBroker(
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
		_ = closeLocalCICacheBroker(server)
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeLocalCICacheBroker(server) })
	return handler, server, client
}

func closeLocalCICacheBroker(server *database.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return server.Close(ctx)
}

func stringsOf(value string, count int) string {
	result := ""
	for range count {
		result += value
	}
	return result
}
