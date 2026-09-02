package evolution

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

type evolutionBrokerFixture struct {
	home         string
	primary      string
	agent        string
	cfg          *config.Config
	handler      *BrokerHandler
	server       *database.Server
	client       *database.Client
	primaryStore *Store
	agentStore   *Store
}

func TestEvolutionRuntimeConstructorRequiresBrokerAndDoesNotOpenProvider(t *testing.T) {
	previous := database.RuntimeClient()
	database.InstallProcessClient(nil)
	restoreProviderAuthority := database.SuspendProviderTestAuthority()
	allowUnfencedEvolutionProviderForTests.Store(false)
	t.Cleanup(func() {
		allowUnfencedEvolutionProviderForTests.Store(true)
		restoreProviderAuthority()
		database.InstallProcessClient(previous)
	})
	workspace := filepath.Join(t.TempDir(), "must-not-exist")
	onlineFence, err := database.AcquireOnlineFence(filepath.Join(t.TempDir(), "launcher-home"))
	if err != nil {
		t.Fatal(err)
	}
	if local := newLocalStore(NewPaths(workspace, "")); local.brokerErr == nil ||
		database.CodeOf(local.brokerErr) != database.CodeUnauthorized {
		t.Fatalf("online-fenced local store = %#v", local)
	}
	if err := onlineFence.Close(); err != nil {
		t.Fatal(err)
	}
	store := NewSQLiteStore(NewPaths(workspace, ""))
	if _, err := store.LoadTaskRecords(); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("LoadTaskRecords() error = %v", err)
	}
	if _, statErr := os.Lstat(workspace); !os.IsNotExist(statErr) {
		t.Fatalf("runtime constructor touched provider root: %v", statErr)
	}
}

func newEvolutionBrokerFixture(t *testing.T) *evolutionBrokerFixture {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	primary := filepath.Join(home, "workspace")
	agent := filepath.Join(home, "agent-workspace")
	for _, path := range []string{primary, agent} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = primary
	cfg.Agents.List = []config.AgentConfig{{ID: "worker", Workspace: agent}}
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
	targets, err := configuredEvolutionTargets(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &evolutionBrokerFixture{
		home:    home,
		primary: primary,
		agent:   agent,
		cfg:     cfg,
		handler: handler,
		server:  server,
		client:  client,
	}
	for id, target := range targets {
		store := &Store{paths: Paths{Workspace: target.paths.Workspace}, broker: client, storeID: id}
		if id == "workspace.evolution" {
			fixture.primaryStore = store
		} else {
			fixture.agentStore = store
		}
	}
	if fixture.primaryStore == nil || fixture.agentStore == nil {
		t.Fatalf("dynamic stores missing: %#v", targets)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Close(ctx); err != nil {
			t.Errorf("close: %v", err)
		}
		for _, store := range handler.stores {
			if store.retained != nil {
				t.Error("retained pool remained open")
			}
		}
	})
	return fixture
}

func brokerRecord(id, workspace string, kind RecordKind) LearningRecord {
	return LearningRecord{
		ID:          id,
		Kind:        kind,
		WorkspaceID: workspace,
		CreatedAt:   time.Unix(1_700_000_000, 0).UTC(),
		Summary:     id,
		Status:      RecordStatus("new"),
	}
}

func TestEvolutionBrokerPrimaryAndDynamicWorkspaceStores(t *testing.T) {
	f := newEvolutionBrokerFixture(t)
	primary := brokerRecord("primary-task", f.primary, RecordKindTask)
	agent := brokerRecord("agent-task", f.agent, RecordKindTask)
	if err := f.primaryStore.AppendTaskRecord(t.Context(), primary); err != nil {
		t.Fatal(err)
	}
	if err := f.agentStore.AppendTaskRecord(t.Context(), agent); err != nil {
		t.Fatal(err)
	}
	p, err := f.primaryStore.LoadTaskRecords()
	if err != nil || len(p) != 1 || p[0].ID != primary.ID {
		t.Fatalf("primary=%#v %v", p, err)
	}
	a, err := f.agentStore.LoadTaskRecords()
	if err != nil || len(a) != 1 || a[0].ID != agent.ID {
		t.Fatalf("agent=%#v %v", a, err)
	}
	if f.primaryStore.StoreID() == f.agentStore.StoreID() {
		t.Fatal("dynamic workspaces share StoreID")
	}
	pattern := brokerRecord("pattern", f.primary, RecordKindPattern)
	pattern.Status = RecordStatus("ready")
	if mergeErr := f.primaryStore.MergePatternRecords([]LearningRecord{pattern}); mergeErr != nil {
		t.Fatal(mergeErr)
	}
	patterns, err := f.primaryStore.LoadPatternRecords()
	if err != nil || len(patterns) != 1 {
		t.Fatalf("patterns=%#v %v", patterns, err)
	}
	if err := f.primaryStore.MarkTaskRecordsClustered([]string{primary.ID}); err != nil {
		t.Fatal(err)
	}
	tasks, _ := f.primaryStore.LoadTaskRecords()
	if tasks[0].Status != "clustered" {
		t.Fatalf("clustered=%#v", tasks[0])
	}
}

func TestEvolutionBrokerDraftProfileAndAtomicUpdate(t *testing.T) {
	f := newEvolutionBrokerFixture(t)
	draft := SkillDraft{
		ID:              "draft",
		WorkspaceID:     f.primary,
		CreatedAt:       time.Now().UTC(),
		SourceRecordID:  "record",
		TargetSkillName: "weather",
		DraftType:       DraftTypeShortcut,
		ChangeKind:      ChangeKindAppend,
		HumanSummary:    "summary",
		BodyOrPatch:     "body",
		Status:          DraftStatusCandidate,
	}
	if err := f.primaryStore.SaveDrafts([]SkillDraft{draft}); err != nil {
		t.Fatal(err)
	}
	drafts, err := f.primaryStore.LoadDrafts()
	if err != nil || len(drafts) != 1 {
		t.Fatalf("drafts=%#v %v", drafts, err)
	}
	profile := SkillProfile{
		SkillName:    "weather",
		WorkspaceID:  f.primary,
		Status:       SkillStatusActive,
		Origin:       "manual",
		HumanSummary: "Weather",
	}
	if saveErr := f.primaryStore.SaveProfile(profile); saveErr != nil {
		t.Fatal(saveErr)
	}
	if updateErr := f.primaryStore.UpdateProfile(f.primary, "weather", func(value *SkillProfile, exists bool) error {
		if !exists {
			t.Fatal("profile absent")
		}
		value.UseCount++
		return nil
	}); updateErr != nil {
		t.Fatal(updateErr)
	}
	loaded, err := f.primaryStore.LoadProfile("weather")
	if err != nil || loaded.UseCount != 1 {
		t.Fatalf("profile=%#v %v", loaded, err)
	}
	profiles, err := f.primaryStore.LoadProfiles()
	if err != nil || len(profiles) != 1 {
		t.Fatalf("profiles=%#v %v", profiles, err)
	}
}

func TestEvolutionBrokerConcurrentClientsRetainPools(t *testing.T) {
	f := newEvolutionBrokerFixture(t)
	const clients = 20
	var wg sync.WaitGroup
	errs := make(chan error, clients)
	for i := range clients {
		client, err := database.Connect(f.home)
		if err != nil {
			t.Fatal(err)
		}
		store := &Store{paths: Paths{Workspace: f.primary}, broker: client, storeID: f.primaryStore.storeID}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := store.AppendTaskRecord(
				context.Background(),
				brokerRecord(fmt.Sprintf("task-%02d", i), f.primary, RecordKindTask),
			); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	records, err := f.primaryStore.LoadTaskRecords()
	if err != nil || len(records) != clients {
		t.Fatalf("records=%d %v", len(records), err)
	}
	if f.handler.stores[f.primaryStore.storeID].retained == nil {
		t.Fatal("primary pool not retained")
	}
	var out evolutionBrokerResponse
	err = f.client.Call(
		t.Context(),
		BrokerDomain,
		BrokerVersion,
		evolutionOpLoadRecords,
		evolutionBrokerRequest{StoreID: "global.auth", Class: "task"},
		&out,
	)
	if database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("foreign ID=%v", err)
	}
}

func TestEvolutionRuntimeConstructorMatchesCatalogAndFailsClosed(t *testing.T) {
	if os.Getenv("PICOCLAW_EVOLUTION_BROKER_HELPER") == "1" {
		if _, _, err := database.ConnectInherited(context.Background()); err != nil {
			t.Fatal(err)
		}
		primary := os.Getenv("PICOCLAW_EVOLUTION_PRIMARY")
		agent := os.Getenv("PICOCLAW_EVOLUTION_AGENT")
		for _, workspace := range []string{primary, agent} {
			store := NewStore(NewPaths(workspace, ""))
			if store.broker == nil || store.retained != nil || !store.StoreID().Valid() {
				t.Fatalf("runtime store=%#v", store)
			}
			if err := store.AppendTaskRecord(
				context.Background(),
				brokerRecord("runtime-"+filepath.Base(workspace), workspace, RecordKindTask),
			); err != nil {
				t.Fatal(err)
			}
		}
		forbidden := filepath.Join(primary, "uncataloged")
		store := NewStore(NewPaths(forbidden, ""))
		if store.brokerErr == nil || store.retained != nil {
			t.Fatalf("uncataloged store=%#v", store)
		}
		if err := store.AppendTaskRecord(
			context.Background(),
			brokerRecord("forbidden", forbidden, RecordKindTask),
		); err == nil {
			t.Fatal("uncataloged write succeeded")
		}
		if _, err := os.Stat(NewPaths(forbidden, "").Database); !os.IsNotExist(err) {
			t.Fatalf("uncataloged DB created: %v", err)
		}
		return
	}
	f := newEvolutionBrokerFixture(t)
	authority, err := database.InheritedAuthorityEnvironment(f.home)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestEvolutionRuntimeConstructorMatchesCatalogAndFailsClosed$")
	cmd.Env = append(
		os.Environ(),
		"PICOCLAW_EVOLUTION_BROKER_HELPER=1",
		"PICOCLAW_EVOLUTION_PRIMARY="+f.primary,
		"PICOCLAW_EVOLUTION_AGENT="+f.agent,
		config.EnvHome+"="+f.home,
		config.EnvConfig+"="+filepath.Join(f.home, "config.json"),
		authority,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helper: %v\n%s", err, output)
	}
}

func TestEvolutionConfiguredRelativeRootMatchesPrimaryCatalog(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Evolution.StateDir = "custom-evolution"
	targets, err := configuredEvolutionTargets(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	target := targets["workspace.evolution"]
	want := filepath.Join(workspace, "custom-evolution", "evolution.db")
	if target.paths.Database != want {
		t.Fatalf("relative evolution database = %q, want %q", target.paths.Database, want)
	}
}
