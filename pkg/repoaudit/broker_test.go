package repoaudit

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestReviewBrokerAtomicCorePaginationAndFailClosed(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	handler := newReviewBrokerHandlerForTest(t, home, workspace)
	server := startReviewBroker(t, home, handler)
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	setReviewBrokerClientForTest(t, client)
	store := NewSQLiteStore(workspace)
	if store.root != "" || store.database != "" || store.broker != client || store.StoreID() != ReviewStoreID {
		t.Fatalf("store = %#v", store)
	}
	decoy := filepath.Join(t.TempDir(), "must-not-open")
	decoyStore := NewSQLiteStore(decoy)
	if decoyStore.StoreID() != "" || decoyStore.brokerErr == nil {
		t.Fatalf("uncataloged store = %#v", decoyStore)
	}
	if preflightErr := decoyStore.Preflight(t.Context()); preflightErr == nil {
		t.Fatal("uncataloged workspace reached primary review store")
	}
	for index := 0; index < 3; index++ {
		plan, planErr := store.PlanWithProfileLimitAuthoritative(
			t.Context(), "owner/repo-"+string(rune('a'+index)), "commit", "inventory", "profile",
			nil, false, maxReviewFiles, true,
		)
		if planErr != nil {
			t.Fatal(planErr)
		}
		if _, finalizeErr := store.FinalizeNoopPlan(plan); finalizeErr != nil {
			t.Fatal(finalizeErr)
		}
	}
	states, err := store.List()
	if err != nil || len(states) != 3 {
		t.Fatalf("List = %d/%v", len(states), err)
	}
	summaries, err := store.ListSummaries()
	if err != nil || len(summaries) != 3 {
		t.Fatalf("Summaries = %d/%v", len(summaries), err)
	}
	if preflightErr := store.Preflight(t.Context()); preflightErr != nil {
		t.Fatal(preflightErr)
	}
	profile, err := store.CreateProfile(t.Context(), validProfileForTest("rrpf_broker", "Broker profile"))
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := store.ListProfiles(t.Context())
	if err != nil || len(profiles) != 1 || profiles[0].ID != profile.ID {
		t.Fatalf("profiles = %#v/%v", profiles, err)
	}
	profile, err = store.UpdateProfile(
		t.Context(),
		profile.ID,
		profile.Version,
		func(value *RepositoryReviewProfile) error { value.Name = "Updated broker profile"; return nil },
	)
	if err != nil || profile.Version != 2 {
		t.Fatalf("profile update = %#v/%v", profile, err)
	}
	automation, err := store.CreateAutomation(t.Context(), validAutomationForTest("rra_broker", "Broker automation"))
	if err != nil {
		t.Fatal(err)
	}
	automations, err := store.ListAutomations(t.Context())
	if err != nil || len(automations) != 1 || automations[0].ID != automation.ID {
		t.Fatalf("automations = %#v/%v", automations, err)
	}
	if handler.workspaces[ReviewStoreID].store.retained == nil {
		t.Fatal("handler did not retain its pool")
	}
	closeReviewBroker(t, server)
	if _, _, err := store.Get("owner/repo-a"); err == nil {
		t.Fatal("broker loss fell back locally")
	}
	if _, err := os.Stat(filepath.Join(decoy, storeDirectory, repositoryReviewDatabaseFilename)); !os.IsNotExist(err) {
		t.Fatalf("decoy DB exists: %v", err)
	}
}

func TestReviewBrokerMultiClientWorkerAtomicity(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	handler := newReviewBrokerHandlerForTest(t, home, workspace)
	server := startReviewBroker(t, home, handler)
	defer closeReviewBroker(t, server)
	firstClient, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	setReviewBrokerClientForTest(t, firstClient)
	first := NewSQLiteStore(workspace)
	secondClient, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	reviewBrokerClient = func() *database.Client { return secondClient }
	second := NewSQLiteStore(workspace)
	const writers = 10
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
			repository := "owner/repo-" + string(rune('a'+index))
			plan, operationErr := store.PlanWithProfileLimitAuthoritative(
				t.Context(), repository, "commit", "inventory", "profile", nil,
				false, maxReviewFiles, true,
			)
			if operationErr == nil {
				_, operationErr = store.FinalizeNoopPlan(plan)
			}
			errorsByWriter <- operationErr
		}(index)
	}
	wait.Wait()
	close(errorsByWriter)
	for err := range errorsByWriter {
		if err != nil {
			t.Errorf("write: %v", err)
		}
	}
	items, err := first.List()
	if err != nil || len(items) != writers {
		t.Fatalf("List = %d/%v", len(items), err)
	}
}

func TestReviewBrokerRediscoveryAfterRestart(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	firstHandler := newReviewBrokerHandlerForTest(t, home, workspace)
	firstServer := startReviewBroker(t, home, firstHandler)
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	setReviewBrokerClientForTest(t, client)
	store := NewSQLiteStore(workspace)
	plan, err := store.PlanWithProfileLimitAuthoritative(
		t.Context(),
		"owner/restart",
		"commit",
		"inventory",
		"profile",
		nil,
		false,
		maxReviewFiles,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, finalizeErr := store.FinalizeNoopPlan(plan); finalizeErr != nil {
		t.Fatal(finalizeErr)
	}
	firstEpoch := firstServer.Manifest().Epoch
	closeReviewBroker(t, firstServer)
	secondHandler := newReviewBrokerHandlerForTest(t, home, workspace)
	secondServer := startReviewBroker(t, home, secondHandler)
	defer closeReviewBroker(t, secondServer)
	if secondServer.Manifest().Epoch == firstEpoch {
		t.Fatal("broker epoch did not change")
	}
	state, found, err := store.Get("owner/restart")
	if err != nil || !found || state.Repository != "owner/restart" {
		t.Fatalf("rediscovered Get = %#v/%v/%v", state, found, err)
	}
}

func TestReviewBrokerDynamicWorkspaceIsolationAndFailClosedSelection(t *testing.T) {
	home := t.TempDir()
	primary := filepath.Join(home, "primary")
	agent := filepath.Join(home, "agent")
	for _, workspace := range []string{primary, agent} {
		if err := os.MkdirAll(workspace, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	handler := newReviewBrokerHandlerForTest(t, home, primary, agent)
	server := startReviewBroker(t, home, handler)
	defer closeReviewBroker(t, server)
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	setReviewBrokerClientForTest(t, client)
	primaryStore := NewSQLiteStore(primary)
	agentStore := NewSQLiteStore(agent)
	if primaryStore.StoreID() == agentStore.StoreID() || !primaryStore.StoreID().Valid() ||
		!agentStore.StoreID().Valid() {
		t.Fatalf("dynamic StoreIDs = %q/%q", primaryStore.StoreID(), agentStore.StoreID())
	}
	for store, repository := range map[*Store]string{
		&primaryStore: "owner/primary",
		&agentStore:   "owner/agent",
	} {
		plan, planErr := store.PlanWithProfileLimitAuthoritative(
			t.Context(), repository, "commit", "inventory", "profile", nil,
			false, maxReviewFiles, true,
		)
		if planErr != nil {
			t.Fatal(planErr)
		}
		if _, finalizeErr := store.FinalizeNoopPlan(plan); finalizeErr != nil {
			t.Fatal(finalizeErr)
		}
	}
	primaryItems, err := primaryStore.List()
	if err != nil || len(primaryItems) != 1 || primaryItems[0].Repository != "owner/primary" {
		t.Fatalf("primary items = %#v/%v", primaryItems, err)
	}
	agentItems, err := agentStore.List()
	if err != nil || len(agentItems) != 1 || agentItems[0].Repository != "owner/agent" {
		t.Fatalf("agent items = %#v/%v", agentItems, err)
	}
	if handler.workspaces[primaryStore.StoreID()].store.retained.db ==
		handler.workspaces[agentStore.StoreID()].store.retained.db {
		t.Fatal("dynamic workspaces share retained review pool")
	}
	uncataloged := NewSQLiteStore(filepath.Join(home, "uncataloged"))
	if uncataloged.StoreID() != "" || uncataloged.Preflight(t.Context()) == nil {
		t.Fatal("uncataloged workspace did not fail closed")
	}
}

func TestReviewBrokerLeaseExpiryUnlockFailureAndShutdownWaiter(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	handler := newReviewBrokerHandlerForTest(t, home, workspace)
	child := handler.workspaces[ReviewStoreID]
	child.leaseTTL = 40 * time.Millisecond
	server := startReviewBroker(t, home, handler)
	t.Cleanup(func() { closeReviewBroker(t, server) })
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	setReviewBrokerClientForTest(t, client)
	kept := NewSQLiteStore(workspace)
	keptUnlock, err := kept.brokerLock("heartbeat")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * child.leaseTTL)
	var blocked reviewLeaseResponse
	blockedCtx, cancelBlocked := context.WithTimeout(context.Background(), 50*time.Millisecond)
	err = client.CallWithOptions(
		blockedCtx, reviewBrokerDomain, reviewBrokerVersion, reviewOperationLock,
		reviewLockRequest{StoreID: ReviewStoreID, Key: "heartbeat"}, &blocked,
		database.CallOptions{Mutation: true},
	)
	cancelBlocked()
	if database.CodeOf(err) != database.CodeOutcomeUnknown {
		t.Fatalf("heartbeat did not retain lease: %v", err)
	}
	keptUnlock()
	if leaseErr := kept.BrokerLeaseError(); leaseErr != nil {
		t.Fatalf("heartbeat lease release error = %v", leaseErr)
	}

	var lost reviewLeaseResponse
	if acquireErr := client.CallWithOptions(
		t.Context(), reviewBrokerDomain, reviewBrokerVersion, reviewOperationLock,
		reviewLockRequest{StoreID: ReviewStoreID, Key: "lost"}, &lost,
		database.CallOptions{Mutation: true},
	); acquireErr != nil {
		t.Fatal(acquireErr)
	}
	time.Sleep(3 * child.leaseTTL)
	var replacement reviewLeaseResponse
	if acquireErr := client.CallWithOptions(
		t.Context(), reviewBrokerDomain, reviewBrokerVersion, reviewOperationLock,
		reviewLockRequest{StoreID: ReviewStoreID, Key: "lost"}, &replacement,
		database.CallOptions{Mutation: true},
	); acquireErr != nil {
		t.Fatalf("expired lease was not reclaimed: %v", acquireErr)
	}
	var released reviewMutationResponse
	if releaseErr := client.CallWithOptions(
		t.Context(), reviewBrokerDomain, reviewBrokerVersion, reviewOperationUnlock,
		reviewLeaseRequest{StoreID: ReviewStoreID, LeaseID: replacement.LeaseID}, &released,
		database.CallOptions{Mutation: true},
	); releaseErr != nil {
		t.Fatal(releaseErr)
	}

	child.mu.Lock()
	child.leaseTTL = time.Minute
	child.mu.Unlock()
	store := NewSQLiteStore(workspace)
	unlock, err := store.brokerLock("forced-expiry")
	if err != nil {
		t.Fatal(err)
	}
	leaseID := store.brokerState.leaseID
	child.mu.Lock()
	lease := child.leases[leaseID]
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
		var response reviewReadyResponse
		requestDone <- client.CallWithOptions(
			context.Background(), reviewBrokerDomain, reviewBrokerVersion, reviewOperationPreflight,
			reviewTarget{StoreID: ReviewStoreID}, &response,
			database.CallOptions{Mutation: true},
		)
	}()
	time.Sleep(20 * time.Millisecond)
	closeReviewBroker(t, server)
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel request waiting for handler admission")
	}
}

func startReviewBroker(t *testing.T, home string, handler *BrokerHandler) *database.Server {
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

func closeReviewBroker(t *testing.T, server *database.Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func setReviewBrokerClientForTest(t *testing.T, client *database.Client) {
	t.Helper()
	previous := reviewBrokerClient
	reviewBrokerClient = func() *database.Client { return client }
	t.Cleanup(func() { reviewBrokerClient = previous })
}

func newReviewBrokerHandlerForTest(
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
