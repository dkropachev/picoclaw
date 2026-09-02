//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
	developmentnotifications "github.com/sipeed/picoclaw/pkg/developmentnotifications"
)

type eventingBrokerFixture struct {
	home      string
	workspace string
	path      string
	handler   *BrokerHandler
	server    *database.Server
	client    *database.Client
	store     *Store
}

func newEventingBrokerFixture(t *testing.T) *eventingBrokerFixture {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	workspace := filepath.Join(home, "workspace")
	path := filepath.Join(workspace, "eventing", "events.db")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Events.Ingress.Enabled = true
	cfg.Events.Ingress.DatabasePath = path
	if err := config.SaveConfig(filepath.Join(home, "config.json"), cfg); err != nil {
		t.Fatal(err)
	}
	handler, err := NewBrokerHandler(home, cfg)
	if err != nil {
		t.Fatalf("NewBrokerHandler: %v", err)
	}
	server, err := database.StartServer(
		context.Background(),
		database.ServerOptions{Home: home, Handler: handler, CloseHandler: handler.Close},
	)
	if err != nil {
		t.Fatal(err)
	}
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &eventingBrokerFixture{
		home:      home,
		workspace: workspace,
		path:      path,
		handler:   handler,
		server:    server,
		client:    client,
	}
	fixture.store = &Store{
		broker:          client,
		storeID:         handler.storeID,
		now:             time.Now,
		redactor:        NewRedactor(nil, nil),
		maxPayloadBytes: 1 << 20,
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
		if !handler.store.closed.Load() || handler.store.db == nil || handler.store.db.Ping() == nil {
			t.Error("broker pool remained usable after Close")
		}
	})
	return fixture
}

func TestEventingBrokerEventRoutingAndDispatchLifecycle(t *testing.T) {
	f := newEventingBrokerFixture(t)
	s := f.store
	input := testEnvelope("broker-event-1")
	inserted, err := s.Insert(t.Context(), input)
	if err != nil || !inserted.Inserted {
		t.Fatalf("Insert=%#v %v", inserted, err)
	}
	id := inserted.Event.Envelope.ID
	got, err := s.Get(t.Context(), id)
	if err != nil || got.Envelope.ID != id {
		t.Fatalf("Get=%#v %v", got, err)
	}
	page, err := s.List(t.Context(), EventFilter{Limit: 1})
	if err != nil || len(page.Events) != 1 {
		t.Fatalf("List=%#v %v", page, err)
	}
	meta, err := s.GetEventMetadata(t.Context(), id)
	if err != nil || meta.Envelope.ID != id {
		t.Fatalf("metadata=%#v %v", meta, err)
	}
	payload, err := s.GetEventPayload(t.Context(), id)
	if err != nil || !json.Valid(payload) {
		t.Fatalf("payload=%s %v", payload, err)
	}
	claimed, err := s.ClaimRouting(t.Context(), "worker", 1, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimRouting=%#v %v", claimed, err)
	}
	token := claimed[0].Routing.LeaseToken
	if renewErr := s.RenewRoutingLease(t.Context(), id, token, time.Minute); renewErr != nil {
		t.Fatal(renewErr)
	}
	dispatch, created, err := s.CreateRevisionedDispatchForRoutingClaim(
		t.Context(),
		id,
		token,
		"workflows/test.yml",
		"sha256:test",
	)
	if err != nil || !created {
		t.Fatalf("dispatch=%#v %t %v", dispatch, created, err)
	}
	if ackErr := s.AckRouting(t.Context(), id, token); ackErr != nil {
		t.Fatal(ackErr)
	}
	dispatches, err := s.ClaimDispatches(t.Context(), "dispatch-worker", 1, time.Minute)
	if err != nil || len(dispatches) != 1 {
		t.Fatalf("ClaimDispatches=%#v %v", dispatches, err)
	}
	dtoken := dispatches[0].LeaseToken
	if linkErr := s.LinkDispatchRun(t.Context(), dispatch.ID, dtoken, dispatch.RunID); linkErr != nil {
		t.Fatal(linkErr)
	}
	if renewErr := s.RenewDispatchLease(t.Context(), dispatch.ID, dtoken, time.Minute); renewErr != nil {
		t.Fatal(renewErr)
	}
	if finishErr := s.FinishDispatch(t.Context(), dispatch.ID, dtoken, DispatchSucceeded, ""); finishErr != nil {
		t.Fatal(finishErr)
	}
	dpage, err := s.ListDispatchMetadata(t.Context(), DispatchFilter{EventID: id, Limit: 10})
	if err != nil || len(dpage.Dispatches) != 1 {
		t.Fatalf("dispatch metadata=%#v %v", dpage, err)
	}
	replay, err := s.Replay(t.Context(), id)
	if err != nil || !replay.Inserted {
		t.Fatalf("Replay=%#v %v", replay, err)
	}
}

func TestEventingBrokerPRWorkspaceAndDevelopmentNotifications(t *testing.T) {
	f := newEventingBrokerFixture(t)
	s := f.store
	now := time.Now().UTC()
	aggregate, created, err := s.CreatePRWorkspace(
		t.Context(),
		PRWorkspaceCreate{
			RequestID:      "req_broker_create",
			WorkspaceID:    "devw_00000000000000000000000000000091",
			Provider:       testPRProviderSnapshot(now),
			Phase:          PRWorkspaceCharter,
			ExecutionState: PRExecutionWaitingUser,
		},
	)
	if err != nil || !created {
		t.Fatalf("CreatePRWorkspace=%#v %t %v", aggregate, created, err)
	}
	loaded, err := s.GetPRWorkspace(t.Context(), aggregate.Workspace.ID)
	if err != nil || loaded.Workspace.ID != aggregate.Workspace.ID {
		t.Fatalf("GetPRWorkspace=%#v %v", loaded, err)
	}
	page, err := s.ListPRWorkspaces(t.Context(), PRWorkspaceFilter{RepositoryID: "repo-42", Limit: 1})
	if err != nil || len(page.Workspaces) != 1 {
		t.Fatalf("ListPR=%#v %v", page, err)
	}
	watermark := PRIngressCutoverWatermark{
		Source:          "github",
		Connector:       "primary",
		InboxReceivedAt: now,
		InboxEventID:    "ev_00000000000000000000000000000091",
	}
	if setErr := s.SetPRWorkspaceIngressCutover(t.Context(), watermark); setErr != nil {
		t.Fatal(setErr)
	}
	if got, getErr := s.GetPRWorkspaceIngressCutover(t.Context(), "github", "primary"); getErr != nil ||
		got.InboxEventID != watermark.InboxEventID {
		t.Fatalf("cutover=%#v %v", got, getErr)
	}
	draft := developmentnotifications.Draft{
		ID:          "dnt_11111111111111111111111111111191",
		SourceKey:   "broker:notification",
		Generation:  1,
		WorkspaceID: aggregate.Workspace.ID,
		Repository:  aggregate.Workspace.Repository,
		Intent:      developmentnotifications.IntentPickupPR,
		SourceKind:  developmentnotifications.SourcePullRequest,
		Phase:       "triage",
		Reason:      developmentnotifications.ReasonScopeException,
		Title:       "Decision",
		Summary:     "Choose",
		Target:      developmentnotifications.Target{Panel: "scope", EntityID: "pgr_11111111111111111111111111111191"},
	}
	upsert, err := s.UpsertDevelopmentNotification(t.Context(), draft)
	if err != nil || !upsert.Created {
		t.Fatalf("upsert=%#v %v", upsert, err)
	}
	notifications, err := s.ListDevelopmentNotifications(t.Context())
	if err != nil || len(notifications) != 1 {
		t.Fatalf("notifications=%#v %v", notifications, err)
	}
	mutated, err := s.MutateDevelopmentNotification(
		t.Context(),
		notifications[0].ID,
		notifications[0].Version,
		"mark_read",
		nil,
	)
	if err != nil || !mutated.Read {
		t.Fatalf("mutate=%#v %v", mutated, err)
	}
	for index := 0; index < eventingNotificationPageSize+3; index++ {
		paged := draft
		paged.ID = fmt.Sprintf("dnt_%032x", index+1000)
		paged.SourceKey = fmt.Sprintf("broker:notification:%03d", index)
		paged.Target.EntityID = fmt.Sprintf("pgr_%032x", index+1000)
		if _, upsertErr := s.UpsertDevelopmentNotification(t.Context(), paged); upsertErr != nil {
			t.Fatalf("paged upsert %d: %v", index, upsertErr)
		}
	}
	if notifications, err = s.ListDevelopmentNotifications(t.Context()); err != nil ||
		len(notifications) != eventingNotificationPageSize+4 {
		t.Fatalf("paginated notifications=%d %v", len(notifications), err)
	}
	push, err := s.PutDevelopmentPushState(t.Context(), json.RawMessage(`{"enabled":true}`), 1)
	if err != nil || push.Version != 2 {
		t.Fatalf("push=%#v %v", push, err)
	}
}

func TestEventingBrokerConcurrentClientsRetainOnePool(t *testing.T) {
	f := newEventingBrokerFixture(t)
	const clients = 20
	var wg sync.WaitGroup
	errs := make(chan error, clients)
	for i := range clients {
		client, err := database.Connect(f.home)
		if err != nil {
			t.Fatal(err)
		}
		store := &Store{
			broker:          client,
			storeID:         EventingStoreID,
			now:             time.Now,
			redactor:        NewRedactor(nil, nil),
			maxPayloadBytes: 1 << 20,
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := store.Insert(context.Background(), testEnvelope(fmt.Sprintf("concurrent-%02d", i)))
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	page, err := f.store.List(t.Context(), EventFilter{Limit: 100})
	if err != nil || len(page.Events) != clients {
		t.Fatalf("events=%d %v", len(page.Events), err)
	}
	if f.handler.store.db == nil {
		t.Fatal("broker pool not retained")
	}
	var out eventingBrokerResponse
	err = f.client.Call(
		t.Context(),
		BrokerDomain,
		BrokerVersion,
		eventingOpList,
		eventingBrokerRequest{StoreID: "global.auth", EventFilter: EventFilter{Limit: 1}},
		&out,
	)
	if database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("foreign ID=%v", err)
	}
}

func TestEventingBrokerConfiguredWorkspacesAreIsolated(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	primaryWorkspace := filepath.Join(home, "workspace")
	agentWorkspace := filepath.Join(home, "agents", "worker")
	primaryPath := filepath.Join(home, "event-data", "primary.db")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = primaryWorkspace
	cfg.Agents.List = []config.AgentConfig{
		{ID: "worker", Workspace: agentWorkspace},
		{ID: "shared", Workspace: agentWorkspace},
	}
	cfg.Events.Ingress.Enabled = true
	cfg.Events.Ingress.DatabasePath = primaryPath
	handler, err := NewBrokerHandler(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(handler.workspaces) != 2 || len(handler.selectors) != 4 {
		t.Fatalf("configured maps = workspaces %d selectors %d", len(handler.workspaces), len(handler.selectors))
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
	database.InstallProcessClient(client)
	closed := false
	t.Cleanup(func() {
		database.InstallProcessClient(nil)
		if !closed {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Close(ctx)
		}
	})

	primary, err := OpenForWorkspace(t.Context(), primaryWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := OpenForWorkspace(t.Context(), agentWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	if primary.StoreID() != EventingStoreID || !agent.StoreID().Valid() ||
		agent.StoreID() == primary.StoreID() {
		t.Fatalf("resolved IDs = primary %q agent %q", primary.StoreID(), agent.StoreID())
	}
	for label, store := range map[string]*Store{"primary": primary, "agent": agent} {
		result, insertErr := store.Insert(t.Context(), testEnvelope("dynamic-shared"))
		if insertErr != nil || !result.Inserted {
			t.Fatalf("Insert(%s) = %#v, %v", label, result, insertErr)
		}
	}
	for label, store := range map[string]*Store{"primary": primary, "agent": agent} {
		page, listErr := store.List(t.Context(), EventFilter{Limit: 10})
		if listErr != nil || len(page.Events) != 1 {
			t.Fatalf("List(%s) = %d, %v", label, len(page.Events), listErr)
		}
	}

	const perWorkspace = 10
	var wait sync.WaitGroup
	errorsOut := make(chan error, perWorkspace*2)
	for index := range perWorkspace {
		for label, store := range map[string]*Store{"primary": primary, "agent": agent} {
			wait.Add(1)
			go func(index int, label string, store *Store) {
				defer wait.Done()
				_, insertErr := store.Insert(
					context.Background(), testEnvelope(fmt.Sprintf("dynamic-%s-%02d", label, index)),
				)
				if insertErr != nil {
					errorsOut <- insertErr
				}
			}(index, label, store)
		}
	}
	wait.Wait()
	close(errorsOut)
	for operationErr := range errorsOut {
		t.Error(operationErr)
	}
	for label, store := range map[string]*Store{"primary": primary, "agent": agent} {
		page, listErr := store.List(t.Context(), EventFilter{Limit: 100})
		if listErr != nil || len(page.Events) != perWorkspace+1 {
			t.Fatalf("concurrent List(%s) = %d, %v", label, len(page.Events), listErr)
		}
	}

	primaryWorkspaceStore := handler.workspaces[primary.StoreID()].store
	agentWorkspaceStore := handler.workspaces[agent.StoreID()].store
	if primaryWorkspaceStore == nil || agentWorkspaceStore == nil ||
		primaryWorkspaceStore.db == nil || agentWorkspaceStore.db == nil ||
		primaryWorkspaceStore.db == agentWorkspaceStore.db {
		t.Fatalf("retained stores = primary %#v agent %#v", primaryWorkspaceStore, agentWorkspaceStore)
	}
	if closeErr := primary.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	primaryAgain, err := Open(t.Context(), primaryPath)
	if err != nil {
		t.Fatalf("Open after runtime wrapper close: %v", err)
	}
	if page, listErr := primaryAgain.List(t.Context(), EventFilter{Limit: 100}); listErr != nil ||
		len(page.Events) != perWorkspace+1 {
		t.Fatalf("broker pool after runtime close = %d, %v", len(page.Events), listErr)
	}

	forbidden := filepath.Join(home, "uncataloged", "eventing", "events.db")
	if _, err := Open(t.Context(), forbidden); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("uncataloged Open error = %v", err)
	}
	if _, statErr := os.Stat(forbidden); !os.IsNotExist(statErr) {
		t.Fatalf("uncataloged Open created storage: %v", statErr)
	}

	database.InstallProcessClient(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Close(ctx); err != nil {
		t.Fatal(err)
	}
	closed = true
	for storeID, workspace := range handler.workspaces {
		if !workspace.store.closed.Load() || workspace.store.db.Ping() == nil {
			t.Fatalf("eventing pool %q remained open", storeID)
		}
	}
}

func TestEventingRuntimeOpenResolvesConfiguredPathAndFailsClosed(t *testing.T) {
	if os.Getenv("PICOCLAW_EVENTING_BROKER_HELPER") == "1" {
		if _, _, err := database.ConnectInherited(context.Background()); err != nil {
			t.Fatal(err)
		}
		configured := os.Getenv("PICOCLAW_EVENTING_CONFIGURED")
		forbidden := os.Getenv("PICOCLAW_EVENTING_FORBIDDEN")
		store, err := Open(context.Background(), configured)
		if err != nil {
			t.Fatal(err)
		}
		if store.db != nil || store.StoreID() != EventingStoreID {
			t.Fatalf("runtime store=%#v", store)
		}
		if _, err := store.Insert(context.Background(), testEnvelope("runtime-constructor")); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(context.Background(), forbidden); database.CodeOf(err) != database.CodeUnauthorized {
			t.Fatalf("uncataloged Open error = %v", err)
		}
		if _, err := os.Stat(forbidden); !os.IsNotExist(err) {
			t.Fatalf("path opened: %v", err)
		}
		return
	}
	f := newEventingBrokerFixture(t)
	authority, err := database.InheritedAuthorityEnvironment(f.home)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := filepath.Join(t.TempDir(), "must-not-exist", "events.db")
	cmd := exec.Command(os.Args[0], "-test.run=^TestEventingRuntimeOpenResolvesConfiguredPathAndFailsClosed$")
	cmd.Env = append(os.Environ(), "PICOCLAW_EVENTING_BROKER_HELPER=1",
		"PICOCLAW_EVENTING_CONFIGURED="+f.path,
		"PICOCLAW_EVENTING_FORBIDDEN="+forbidden, config.EnvHome+"="+f.home,
		config.EnvConfig+"="+filepath.Join(f.home, "config.json"), authority)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helper: %v\n%s", err, output)
	}
}
