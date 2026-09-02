package repoeval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestEvaluationBrokerCRUDPaginationAndFailClosed(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	handler := newEvaluationBrokerHandlerForTest(t, home, workspace)
	server := startEvaluationBroker(t, home, handler)
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	setEvaluationBrokerClientForTest(t, client)
	store := NewSQLiteStore(workspace)
	if store.root != "" || store.database != "" || store.broker != client || store.StoreID() != EvaluationStoreID {
		t.Fatalf("broker store leaked local authority: %#v", store)
	}
	decoy := filepath.Join(t.TempDir(), "must-not-open")
	decoyStore := NewSQLiteStore(decoy)
	if decoyStore.StoreID() != "" || decoyStore.brokerErr == nil || decoyStore.Preflight(t.Context()) == nil {
		t.Fatalf("uncataloged store = %#v", decoyStore)
	}
	created := make([]Evaluation, 0, 5)
	for index := 0; index < 5; index++ {
		value, createErr := store.Create(t.Context(), validCreateRequest())
		if createErr != nil {
			t.Fatal(createErr)
		}
		created = append(created, value)
	}
	items, err := store.List(t.Context())
	if err != nil || len(items) != 5 {
		t.Fatalf("List() = %d, %v", len(items), err)
	}
	loaded, found, err := store.Get(t.Context(), created[0].ID)
	if err != nil || !found || loaded.ID != created[0].ID {
		t.Fatalf("Get() = %#v/%v/%v", loaded, found, err)
	}
	updated, err := store.Update(t.Context(), loaded.ID, loaded.Version, func(value *Evaluation) error {
		value.Focus.FreeText = "broker mutation"
		return nil
	})
	if err != nil || updated.Version != loaded.Version+1 {
		t.Fatalf("Update() = %#v/%v", updated, err)
	}
	if deleteErr := store.Delete(t.Context(), created[1].ID, created[1].Version); deleteErr != nil {
		t.Fatal(deleteErr)
	}
	release, err := store.LockController()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LockController(); !errors.Is(err, ErrControllerLocked) {
		t.Fatalf("second controller lock = %v", err)
	}
	release()
	if handler.workspaces[EvaluationStoreID].store.retained == nil {
		t.Fatal("handler did not retain its pool")
	}
	closeEvaluationBroker(t, server)
	if _, err := store.Create(t.Context(), validCreateRequest()); err == nil {
		t.Fatal("broker loss fell back locally")
	}
	if _, err := os.Stat(filepath.Join(decoy, storeDirectory, evaluationDatabaseFilename)); !os.IsNotExist(err) {
		t.Fatalf("decoy database exists: %v", err)
	}
}

func TestEvaluationBrokerMultiClientConcurrency(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	handler := newEvaluationBrokerHandlerForTest(t, home, workspace)
	server := startEvaluationBroker(t, home, handler)
	defer closeEvaluationBroker(t, server)
	firstClient, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	setEvaluationBrokerClientForTest(t, firstClient)
	first := NewSQLiteStore(workspace)
	secondClient, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	evaluationBrokerClient = func() *database.Client { return secondClient }
	second := NewSQLiteStore(workspace)
	const writers = 12
	var wait sync.WaitGroup
	errorsByWriter := make(chan error, writers)
	for index := 0; index < writers; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			store := first
			if index%2 != 0 {
				store = second
			}
			_, createErr := store.Create(t.Context(), validCreateRequest())
			errorsByWriter <- createErr
		}(index)
	}
	wait.Wait()
	close(errorsByWriter)
	for err := range errorsByWriter {
		if err != nil {
			t.Errorf("Create: %v", err)
		}
	}
	items, err := first.List(t.Context())
	if err != nil || len(items) != writers {
		t.Fatalf("List = %d/%v", len(items), err)
	}
}

func TestEvaluationBrokerRediscoveryAfterRestart(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	firstHandler := newEvaluationBrokerHandlerForTest(t, home, workspace)
	firstServer := startEvaluationBroker(t, home, firstHandler)
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	setEvaluationBrokerClientForTest(t, client)
	store := NewSQLiteStore(workspace)
	created, err := store.Create(t.Context(), validCreateRequest())
	if err != nil {
		t.Fatal(err)
	}
	firstEpoch := firstServer.Manifest().Epoch
	closeEvaluationBroker(t, firstServer)
	secondHandler := newEvaluationBrokerHandlerForTest(t, home, workspace)
	secondServer := startEvaluationBroker(t, home, secondHandler)
	defer closeEvaluationBroker(t, secondServer)
	if secondServer.Manifest().Epoch == firstEpoch {
		t.Fatal("broker epoch did not change")
	}
	loaded, found, err := store.Get(t.Context(), created.ID)
	if err != nil || !found || loaded.ID != created.ID {
		t.Fatalf("rediscovered Get = %#v/%v/%v", loaded, found, err)
	}
}

func TestEvaluationBrokerDynamicWorkspaceIsolationAndFailClosedSelection(t *testing.T) {
	home := t.TempDir()
	primary := filepath.Join(home, "primary")
	agent := filepath.Join(home, "agent")
	for _, workspace := range []string{primary, agent} {
		if err := os.MkdirAll(workspace, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	handler := newEvaluationBrokerHandlerForTest(t, home, primary, agent)
	server := startEvaluationBroker(t, home, handler)
	defer closeEvaluationBroker(t, server)
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	setEvaluationBrokerClientForTest(t, client)
	primaryStore := NewSQLiteStore(primary)
	agentStore := NewSQLiteStore(agent)
	if primaryStore.StoreID() == agentStore.StoreID() || !primaryStore.StoreID().Valid() ||
		!agentStore.StoreID().Valid() {
		t.Fatalf("dynamic StoreIDs = %q/%q", primaryStore.StoreID(), agentStore.StoreID())
	}
	primaryValue, err := primaryStore.Create(t.Context(), validCreateRequest())
	if err != nil {
		t.Fatal(err)
	}
	agentValue, err := agentStore.Create(t.Context(), validCreateRequest())
	if err != nil {
		t.Fatal(err)
	}
	primaryItems, err := primaryStore.List(t.Context())
	if err != nil || len(primaryItems) != 1 || primaryItems[0].ID != primaryValue.ID {
		t.Fatalf("primary items = %#v/%v", primaryItems, err)
	}
	agentItems, err := agentStore.List(t.Context())
	if err != nil || len(agentItems) != 1 || agentItems[0].ID != agentValue.ID {
		t.Fatalf("agent items = %#v/%v", agentItems, err)
	}
	if handler.workspaces[primaryStore.StoreID()].store.retained.db ==
		handler.workspaces[agentStore.StoreID()].store.retained.db {
		t.Fatal("dynamic workspaces share retained evaluation pool")
	}
	uncataloged := NewSQLiteStore(filepath.Join(home, "uncataloged"))
	if uncataloged.StoreID() != "" || uncataloged.Preflight(t.Context()) == nil {
		t.Fatal("uncataloged workspace did not fail closed")
	}
}

func TestEvaluationBrokerLeaseExpiryUnlockFailureAndShutdownWaiter(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	handler := newEvaluationBrokerHandlerForTest(t, home, workspace)
	child := handler.workspaces[EvaluationStoreID]
	child.leaseTTL = 40 * time.Millisecond
	server := startEvaluationBroker(t, home, handler)
	t.Cleanup(func() { closeEvaluationBroker(t, server) })
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	setEvaluationBrokerClientForTest(t, client)
	kept := NewSQLiteStore(workspace)
	keptUnlock, err := kept.LockController()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * child.leaseTTL)
	if _, lockErr := kept.LockController(); !errors.Is(lockErr, ErrControllerLocked) {
		t.Fatalf("heartbeat did not retain lease: %v", lockErr)
	}
	keptUnlock()
	if leaseErr := kept.BrokerLeaseError(); leaseErr != nil {
		t.Fatalf("heartbeat lease release error = %v", leaseErr)
	}
	var lost evaluationLeaseResponse
	if acquireErr := client.CallWithOptions(
		t.Context(), evaluationBrokerDomain, evaluationBrokerVersion, evaluationOperationLock,
		evaluationTarget{StoreID: EvaluationStoreID}, &lost,
		database.CallOptions{Mutation: true},
	); acquireErr != nil {
		t.Fatal(acquireErr)
	}
	time.Sleep(3 * child.leaseTTL)
	var replacement evaluationLeaseResponse
	if acquireErr := client.CallWithOptions(
		t.Context(), evaluationBrokerDomain, evaluationBrokerVersion, evaluationOperationLock,
		evaluationTarget{StoreID: EvaluationStoreID}, &replacement,
		database.CallOptions{Mutation: true},
	); acquireErr != nil {
		t.Fatalf("expired lease was not reclaimed: %v", acquireErr)
	}
	var released evaluationMutationResponse
	if releaseErr := client.CallWithOptions(
		t.Context(), evaluationBrokerDomain, evaluationBrokerVersion, evaluationOperationUnlock,
		evaluationLeaseRequest{StoreID: EvaluationStoreID, LeaseID: replacement.LeaseID}, &released,
		database.CallOptions{Mutation: true},
	); releaseErr != nil {
		t.Fatal(releaseErr)
	}

	child.mu.Lock()
	child.leaseTTL = time.Minute
	child.mu.Unlock()
	store := NewSQLiteStore(workspace)
	unlock, err := store.LockController()
	if err != nil {
		t.Fatal(err)
	}
	child.mu.Lock()
	var leaseID string
	var lease *evaluationLease
	for id, candidate := range child.leases {
		leaseID, lease = id, candidate
	}
	generation := lease.generation
	child.mu.Unlock()
	child.expireLease(leaseID, lease, generation)
	unlock()
	if err := store.BrokerLeaseError(); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("lost unlock error = %v", err)
	}

	<-child.requestGate
	requestDone := make(chan error, 1)
	go func() {
		var response evaluationMutationResponse
		requestDone <- client.CallWithOptions(
			context.Background(), evaluationBrokerDomain, evaluationBrokerVersion,
			evaluationOperationPreflight, evaluationTarget{StoreID: EvaluationStoreID},
			&response, database.CallOptions{Mutation: true},
		)
	}()
	time.Sleep(20 * time.Millisecond)
	closeEvaluationBroker(t, server)
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel request waiting for handler admission")
	}
}

func startEvaluationBroker(t *testing.T, home string, handler *BrokerHandler) *database.Server {
	t.Helper()
	server, err := database.StartServer(
		context.Background(),
		database.ServerOptions{Home: home, Handler: handler, CloseHandler: handler.Close},
	)
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func closeEvaluationBroker(t *testing.T, server *database.Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func setEvaluationBrokerClientForTest(t *testing.T, client *database.Client) {
	t.Helper()
	previous := evaluationBrokerClient
	evaluationBrokerClient = func() *database.Client { return client }
	t.Cleanup(func() { evaluationBrokerClient = previous })
}

func newEvaluationBrokerHandlerForTest(
	t *testing.T,
	home,
	workspace string,
	agentWorkspaces ...string,
) *BrokerHandler {
	t.Helper()
	cfg := &config.Config{Agents: config.AgentsConfig{
		Defaults: config.AgentDefaults{Workspace: workspace},
	}}
	for index, agentWorkspace := range agentWorkspaces {
		cfg.Agents.List = append(cfg.Agents.List, config.AgentConfig{
			ID: "agent-" + string(rune('a'+index)), Workspace: agentWorkspace,
		})
	}
	handler, err := NewBrokerHandler(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}
