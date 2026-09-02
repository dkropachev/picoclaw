package cron

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestCronBrokerMultiClientCRUDUsesOneOwner(t *testing.T) {
	home := t.TempDir()
	databasePath := filepath.Join(home, "workspace", "cron", "jobs.db")
	handler, server, client := startCronBroker(t, home, databasePath)
	database.InstallProcessClient(client)
	t.Cleanup(func() { database.InstallProcessClient(nil) })

	services := make([]*CronService, 6)
	for index := range services {
		services[index] = NewForWorkspace(databasePath, nil)
		if services[index].initErr != nil {
			t.Fatalf("client %d initialization: %v", index, services[index].initErr)
		}
		if services[index].storage != nil || services[index].brokerClient != client {
			t.Fatalf("client %d acquired local storage", index)
		}
	}

	const jobsPerClient = 24
	var wait sync.WaitGroup
	errors := make(chan error, len(services)*jobsPerClient)
	for clientIndex, service := range services {
		for jobIndex := 0; jobIndex < jobsPerClient; jobIndex++ {
			wait.Add(1)
			go func(clientIndex, jobIndex int, service *CronService) {
				defer wait.Done()
				every := int64((clientIndex + jobIndex + 1) * 60_000)
				_, err := service.AddJob(
					"multi-client", CronSchedule{Kind: "every", EveryMS: &every},
					"payload", "test", "target",
				)
				if err != nil {
					errors <- err
				}
			}(clientIndex, jobIndex, service)
		}
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		t.Errorf("concurrent AddJob: %v", err)
	}

	jobs := services[0].ListJobs(true)
	if len(jobs) != len(services)*jobsPerClient {
		t.Fatalf("job count = %d, want %d", len(jobs), len(services)*jobsPerClient)
	}
	seen := make(map[string]struct{}, len(jobs))
	for _, job := range jobs {
		if _, duplicate := seen[job.ID]; duplicate {
			t.Fatalf("duplicate job ID %q", job.ID)
		}
		seen[job.ID] = struct{}{}
	}

	selected, found := services[1].GetJob(jobs[0].ID)
	if !found || selected == nil {
		t.Fatal("second client did not observe added job")
	}
	selected.Name = "updated-by-second-client"
	if err := services[1].UpdateJob(selected); err != nil {
		t.Fatal(err)
	}
	if disabled := services[2].EnableJob(selected.ID, false); disabled == nil || disabled.Enabled {
		t.Fatalf("third client disable = %#v", disabled)
	}
	updated, found := services[3].GetJob(selected.ID)
	if !found || updated.Name != "updated-by-second-client" || updated.Enabled {
		t.Fatalf("cross-client update = %#v, found %v", updated, found)
	}
	if enabled := services[4].ListJobs(false); len(enabled) != len(jobs)-1 {
		t.Fatalf("enabled job count = %d, want %d", len(enabled), len(jobs)-1)
	}
	status := services[5].Status()
	if status["jobs"] != len(jobs) || status["enabled"] != false {
		t.Fatalf("status = %#v", status)
	}
	if !services[4].RemoveJob(selected.ID) {
		t.Fatal("fourth client did not remove job")
	}
	if _, found := services[0].GetJob(selected.ID); found {
		t.Fatal("first client retained removed job")
	}

	pool := cronBrokerPool(handler)
	if pool == nil {
		t.Fatal("broker did not retain its one local pool")
	}
	if jobs := services[2].ListJobs(true); len(jobs) != len(services)*jobsPerClient-1 {
		t.Fatalf("post-remove job count = %d", len(jobs))
	}
	if cronBrokerPool(handler) != pool {
		t.Fatal("broker replaced its stable pool across clients")
	}
	if err := closeCronBroker(server); err != nil {
		t.Fatal(err)
	}
	if cronBrokerPool(handler) != nil {
		t.Fatal("broker pool remained open after CloseHandler")
	}
}

func TestCronBrokerSchedulerClaimsAndCompletesThroughRPC(t *testing.T) {
	home := t.TempDir()
	databasePath := filepath.Join(home, "workspace", "cron", "jobs.db")
	handler, server, client := startCronBroker(t, home, databasePath)
	database.InstallProcessClient(client)
	t.Cleanup(func() {
		database.InstallProcessClient(nil)
		_ = closeCronBroker(server)
	})

	executed := make(chan string, 1)
	service := NewForWorkspace(databasePath, func(job *CronJob) (string, error) {
		executed <- job.ID
		return "ok", nil
	})
	at := time.Now().Add(150 * time.Millisecond).UnixMilli()
	job, err := service.AddJob(
		"broker-scheduled", CronSchedule{Kind: "at", AtMS: &at},
		"payload", "test", "target",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	defer service.Stop()

	select {
	case got := <-executed:
		if got != job.ID {
			t.Fatalf("executed job = %q, want %q", got, job.ID)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("broker-backed scheduled job did not execute")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, found := service.GetJob(job.ID); !found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("one-shot job was not completed and removed through broker")
		}
		time.Sleep(10 * time.Millisecond)
	}
	service.Stop()
	if cronBrokerPool(handler) == nil {
		t.Fatal("runtime Stop closed the supervisor-owned pool")
	}

	other := NewForWorkspace(databasePath, nil)
	every := int64(time.Hour / time.Millisecond)
	if _, err := other.AddJob(
		"after-runtime-stop",
		CronSchedule{Kind: "every", EveryMS: &every},
		"",
		"",
		"",
	); err != nil {
		t.Fatalf("launcher-style client after runtime stop: %v", err)
	}
}

func TestCronBrokerFailuresAreStructuredAndNeverFallBack(t *testing.T) {
	home := t.TempDir()
	databasePath := filepath.Join(home, "workspace", "cron", "jobs.db")
	_, server, client := startCronBroker(t, home, databasePath)
	database.InstallProcessClient(client)
	t.Cleanup(func() { database.InstallProcessClient(nil) })

	service := NewForWorkspace(databasePath, nil)
	missing := &CronJob{ID: "missing", Name: "missing", Schedule: CronSchedule{Kind: "every"}}
	if err := service.UpdateJob(missing); database.CodeOf(err) != database.CodeNotFound {
		t.Fatalf("missing update error = %v", err)
	}

	var raw cronBrokerResponse
	err := client.Call(
		context.Background(), BrokerDomain, BrokerVersion, cronOperationList,
		cronListRequest{StoreID: "workspace.sessions", IncludeDisabled: true}, &raw,
	)
	if database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("wrong StoreID error = %v", err)
	}
	err = client.Call(
		context.Background(), BrokerDomain, BrokerVersion, "raw-sql",
		cronStoreRequest{StoreID: BrokerStoreID}, &raw,
	)
	if database.CodeOf(err) != database.CodeUnsupported {
		t.Fatalf("unknown operation error = %v", err)
	}

	forbidden := filepath.Join(home, "uncataloged", "cron", "jobs.db")
	uncataloged := NewForWorkspace(forbidden, nil)
	if database.CodeOf(uncataloged.initErr) != database.CodeUnauthorized ||
		uncataloged.storage != nil {
		t.Fatalf("uncataloged service = error %v, storage %#v", uncataloged.initErr, uncataloged.storage)
	}
	if _, err := os.Lstat(forbidden); !os.IsNotExist(err) {
		t.Fatalf("uncataloged constructor created local storage: %v", err)
	}

	if err := closeCronBroker(server); err != nil {
		t.Fatal(err)
	}
	every := int64(60_000)
	if _, err := service.AddJob(
		"unavailable",
		CronSchedule{Kind: "every", EveryMS: &every},
		"",
		"",
		"",
	); database.CodeOf(
		err,
	) != database.CodeUnavailable {
		t.Fatalf("post-shutdown AddJob error = %v", err)
	}
	if service.storage != nil {
		t.Fatal("failed broker operation installed local storage")
	}
}

func TestCronBrokerConfiguredWorkspacesAreIsolated(t *testing.T) {
	home := t.TempDir()
	primaryWorkspace := filepath.Join(home, "workspace")
	agentWorkspace := filepath.Join(home, "agents", "worker")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = primaryWorkspace
	cfg.Agents.List = []config.AgentConfig{
		{ID: "worker", Workspace: agentWorkspace},
		{ID: "shared", Workspace: agentWorkspace},
	}
	handler, err := NewBrokerHandler(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(handler.workspaces) != 2 || len(handler.selectors) != 2 {
		t.Fatalf("configured broker maps = workspaces %d selectors %d", len(handler.workspaces), len(handler.selectors))
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
		_ = closeCronBroker(server)
		t.Fatal(err)
	}
	database.InstallProcessClient(client)
	t.Cleanup(func() {
		database.InstallProcessClient(nil)
		_ = closeCronBroker(server)
	})

	primary := NewForWorkspace(filepath.Join(primaryWorkspace, "cron", "jobs.db"), nil)
	agent := NewForWorkspace(filepath.Join(agentWorkspace, "cron", "jobs.json"), nil)
	if primary.initErr != nil || agent.initErr != nil {
		t.Fatalf("runtime resolution = primary %v agent %v", primary.initErr, agent.initErr)
	}
	if primary.StoreID() != BrokerStoreID || !agent.StoreID().Valid() ||
		agent.StoreID() == primary.StoreID() {
		t.Fatalf("resolved IDs = primary %q agent %q", primary.StoreID(), agent.StoreID())
	}

	const perWorkspace = 12
	var wait sync.WaitGroup
	errorsOut := make(chan error, perWorkspace*2)
	for index := range perWorkspace {
		for _, target := range []*CronService{primary, agent} {
			wait.Add(1)
			go func(index int, target *CronService) {
				defer wait.Done()
				every := int64((index + 1) * 60_000)
				_, addErr := target.AddJob(
					"isolated", CronSchedule{Kind: "every", EveryMS: &every},
					"payload", "test", "target",
				)
				if addErr != nil {
					errorsOut <- addErr
				}
			}(index, target)
		}
	}
	wait.Wait()
	close(errorsOut)
	for operationErr := range errorsOut {
		t.Error(operationErr)
	}
	if got := len(primary.ListJobs(true)); got != perWorkspace {
		t.Fatalf("primary jobs = %d", got)
	}
	if got := len(agent.ListJobs(true)); got != perWorkspace {
		t.Fatalf("agent jobs = %d", got)
	}

	primaryPool := cronBrokerPoolForStore(handler, primary.StoreID())
	agentPool := cronBrokerPoolForStore(handler, agent.StoreID())
	if primaryPool == nil || agentPool == nil || primaryPool == agentPool {
		t.Fatalf("retained pools = primary %p agent %p", primaryPool, agentPool)
	}

	forbidden := filepath.Join(home, "uncataloged", "cron", "jobs.db")
	unknown := NewForWorkspace(forbidden, nil)
	if database.CodeOf(unknown.initErr) != database.CodeUnauthorized || unknown.storage != nil {
		t.Fatalf("uncataloged runtime = error %v storage %#v", unknown.initErr, unknown.storage)
	}
	if _, statErr := os.Stat(forbidden); !os.IsNotExist(statErr) {
		t.Fatalf("uncataloged runtime created storage: %v", statErr)
	}

	database.InstallProcessClient(nil)
	if err := closeCronBroker(server); err != nil {
		t.Fatal(err)
	}
	if cronBrokerPoolForStore(handler, primary.StoreID()) != nil ||
		cronBrokerPoolForStore(handler, agent.StoreID()) != nil {
		t.Fatal("configured cron pools remained open after handler close")
	}
}

func TestCronRuntimeLocatorDerivesOnlyConventionalWorkspace(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	t.Setenv(config.EnvHome, home)
	wanted, err := canonicalCronWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, locator := range []string{
		filepath.Join(workspace, "cron"),
		filepath.Join(workspace, "cron", "jobs.db"),
		filepath.Join(workspace, "cron", "jobs.json"),
		filepath.Join("workspace", "cron", "jobs.db"),
	} {
		got, resolveErr := cronWorkspaceFromLocator(locator)
		if resolveErr != nil || got != wanted {
			t.Errorf("cronWorkspaceFromLocator(%q) = %q, %v", locator, got, resolveErr)
		}
	}
	for _, locator := range []string{
		"", " workspace/cron/jobs.db", "workspace/cron/other.db",
		"workspace/jobs.db", "workspace/cron/jobs.sqlite", "workspace/cron/jobs.db\x00tail",
	} {
		if _, err := cronWorkspaceFromLocator(locator); database.CodeOf(err) != database.CodeInvalid {
			t.Errorf("cronWorkspaceFromLocator(%q) error = %v", locator, err)
		}
	}
}

func startCronBroker(
	t *testing.T,
	home,
	databasePath string,
) (*BrokerHandler, *database.Server, *database.Client) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = filepath.Dir(filepath.Dir(databasePath))
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
		_ = closeCronBroker(server)
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeCronBroker(server) })
	return handler, server, client
}

func closeCronBroker(server *database.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return server.Close(ctx)
}

func cronBrokerPool(handler *BrokerHandler) *sql.DB {
	if handler == nil || handler.service == nil || handler.service.storage == nil {
		return nil
	}
	handler.service.storage.dbMu.Lock()
	defer handler.service.storage.dbMu.Unlock()
	return handler.service.storage.db
}

func cronBrokerPoolForStore(handler *BrokerHandler, storeID database.StoreID) *sql.DB {
	if handler == nil || handler.workspaces[storeID] == nil ||
		handler.workspaces[storeID].service == nil ||
		handler.workspaces[storeID].service.storage == nil {
		return nil
	}
	storage := handler.workspaces[storeID].service.storage
	storage.dbMu.Lock()
	defer storage.dbMu.Unlock()
	return storage.db
}
