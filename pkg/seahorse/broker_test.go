package seahorse

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

type seahorseBrokerFixture struct {
	home, primary, agent       string
	handler                    *BrokerHandler
	server                     *database.Server
	client                     *database.Client
	primaryEngine, agentEngine *Engine
}

func newSeahorseBrokerFixture(t *testing.T) *seahorseBrokerFixture {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	primary := filepath.Join(home, "workspace")
	agent := filepath.Join(home, "agent")
	for _, dir := range []string{primary, agent} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
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
		t.Fatalf("handler: %v", err)
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
	fixture := &seahorseBrokerFixture{
		home:    home,
		primary: primary,
		agent:   agent,
		handler: handler,
		server:  server,
		client:  client,
	}
	targets, _ := configuredSeahorseTargets(home, cfg)
	for id, target := range targets {
		engine := newEngineWithStore(Config{}, nil, &Store{broker: client, storeID: id})
		if target.path == filepath.Join(primary, "sessions", "seahorse.db") {
			fixture.primaryEngine = engine
		} else {
			fixture.agentEngine = engine
		}
	}
	if fixture.primaryEngine == nil || fixture.agentEngine == nil {
		t.Fatal("dynamic engines missing")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Close(ctx); err != nil {
			t.Errorf("close: %v", err)
		}
		for _, engine := range handler.engines {
			if engine.store.db != nil && engine.store.db.Ping() == nil {
				t.Error("pool remained usable")
			}
		}
	})
	return fixture
}

func TestSeahorseBrokerDynamicEngineIngestAndRetrieval(t *testing.T) {
	f := newSeahorseBrokerFixture(t)
	for index, engine := range []*Engine{f.primaryEngine, f.agentEngine} {
		session := fmt.Sprintf("agent:%d:session", index)
		result, err := engine.Ingest(
			t.Context(),
			session,
			[]Message{{Role: "user", Content: "hello searchable world", TokenCount: 3, CreatedAt: time.Now().UTC()}},
		)
		if err != nil || result.MessageCount != 1 {
			t.Fatalf("ingest=%#v %v", result, err)
		}
		status, err := engine.store.GetSessionStatus(t.Context(), session)
		if err != nil || status.Messages != 1 {
			t.Fatalf("status=%#v %v", status, err)
		}
		grep, err := engine.GetRetrieval().
			Grep(t.Context(), GrepInput{Pattern: "searchable", Scope: "message", AllConversations: true, Limit: 10})
		if err != nil || len(grep.Messages) != 1 {
			t.Fatalf("grep=%#v %v", grep, err)
		}
	}
	if f.primaryEngine.store.StoreID() == f.agentEngine.store.StoreID() {
		t.Fatal("dynamic StoreIDs collide")
	}
}

func TestSeahorseBrokerStoreAtomicContextAndPagination(t *testing.T) {
	f := newSeahorseBrokerFixture(t)
	s := f.primaryEngine.store
	conv, err := s.GetOrCreateConversation(t.Context(), "pagination")
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, 0, 215)
	for i := 0; i < 215; i++ {
		message, addErr := s.AddMessage(t.Context(), conv.ConversationID, "user", fmt.Sprintf("message-%03d", i), 1)
		if addErr != nil {
			t.Fatal(addErr)
		}
		ids = append(ids, message.ID)
	}
	messages, err := s.GetMessages(t.Context(), conv.ConversationID, 215, 0)
	if err != nil || len(messages) != 215 {
		t.Fatalf("messages=%d %v", len(messages), err)
	}
	if appendErr := s.AppendContextMessages(t.Context(), conv.ConversationID, ids); appendErr != nil {
		t.Fatal(appendErr)
	}
	items, err := s.GetContextItems(t.Context(), conv.ConversationID)
	if err != nil || len(items) != 215 {
		t.Fatalf("context=%d %v", len(items), err)
	}
	summary, err := s.CreateSummary(
		t.Context(),
		CreateSummaryInput{
			ConversationID:      conv.ConversationID,
			Kind:                SummaryKindLeaf,
			Content:             "summary",
			TokenCount:          1,
			SourceMessageTokens: 215,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LinkSummaryToMessages(t.Context(), summary.SummaryID, ids[:2]); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceContextRangeWithSummary(
		t.Context(),
		conv.ConversationID,
		items[0].Ordinal,
		items[1].Ordinal,
		summary.SummaryID,
	); err != nil {
		t.Fatal(err)
	}
	if count, err := s.GetContextTokenCount(t.Context(), conv.ConversationID); err != nil || count < 1 {
		t.Fatalf("tokens=%d %v", count, err)
	}
}

func TestSeahorseBrokerConcurrentClientsAndAuthorization(t *testing.T) {
	f := newSeahorseBrokerFixture(t)
	const clients = 20
	var wg sync.WaitGroup
	errs := make(chan error, clients)
	for i := range clients {
		client, err := database.Connect(f.home)
		if err != nil {
			t.Fatal(err)
		}
		engine := newEngineWithStore(Config{}, nil, &Store{broker: client, storeID: f.primaryEngine.store.storeID})
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := engine.Ingest(
				context.Background(),
				fmt.Sprintf("concurrent-%02d", i),
				[]Message{{Role: "user", Content: "content", TokenCount: 1}},
			)
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
	statuses, err := f.primaryEngine.store.GetAllSessionStatuses(t.Context())
	if err != nil || len(statuses) != clients {
		t.Fatalf("statuses=%d %v", len(statuses), err)
	}
	var out seahorseResponse
	err = f.client.Call(
		t.Context(),
		BrokerDomain,
		BrokerVersion,
		opGetStatuses,
		seahorseRequest{StoreID: "global.auth"},
		&out,
	)
	if database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("foreign ID=%v", err)
	}
}

func TestSeahorseRuntimeConstructorMatchesCatalogAndFailsClosed(t *testing.T) {
	if os.Getenv("PICOCLAW_SEAHORSE_BROKER_HELPER") == "1" {
		if _, _, err := database.ConnectInherited(context.Background()); err != nil {
			t.Fatal(err)
		}
		for _, workspace := range []string{os.Getenv("PICOCLAW_SEAHORSE_PRIMARY"), os.Getenv("PICOCLAW_SEAHORSE_AGENT")} {
			engine, err := NewEngine(Config{Workspace: workspace}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if engine.store.db != nil || !engine.store.StoreID().Valid() {
				t.Fatalf("runtime engine=%#v", engine.store)
			}
			if _, err := engine.Ingest(
				context.Background(),
				"runtime:"+filepath.Base(workspace),
				[]Message{{Role: "user", Content: "runtime", TokenCount: 1}},
			); err != nil {
				t.Fatal(err)
			}
		}
		forbidden := filepath.Join(os.Getenv("PICOCLAW_SEAHORSE_PRIMARY"), "forbidden")
		if _, err := NewEngine(Config{Workspace: forbidden}, nil); err == nil {
			t.Fatal("uncataloged path accepted")
		}
		if _, err := os.Stat(forbidden); !os.IsNotExist(err) {
			t.Fatalf("forbidden DB created: %v", err)
		}
		return
	}
	f := newSeahorseBrokerFixture(t)
	authority, err := database.InheritedAuthorityEnvironment(f.home)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestSeahorseRuntimeConstructorMatchesCatalogAndFailsClosed$")
	cmd.Env = append(
		os.Environ(),
		"PICOCLAW_SEAHORSE_BROKER_HELPER=1",
		"PICOCLAW_SEAHORSE_PRIMARY="+f.primary,
		"PICOCLAW_SEAHORSE_AGENT="+f.agent,
		config.EnvHome+"="+f.home,
		config.EnvConfig+"="+filepath.Join(f.home, "config.json"),
		authority,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helper: %v\n%s", err, output)
	}
}

func TestSeahorseRuntimeConstructorRequiresBrokerWithoutCreatingWorkspace(t *testing.T) {
	previous := database.RuntimeClient()
	database.InstallProcessClient(nil)
	t.Cleanup(func() { database.InstallProcessClient(previous) })
	workspace := filepath.Join(t.TempDir(), "must-not-exist")
	engine, err := NewEngine(Config{Workspace: workspace}, nil)
	if engine != nil || database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("NewEngine() = %#v, %v", engine, err)
	}
	if _, statErr := os.Lstat(workspace); !os.IsNotExist(statErr) {
		t.Fatalf("runtime constructor touched provider workspace: %v", statErr)
	}
}

func TestSeahorseLocalProviderRejectsLauncherOnlineFence(t *testing.T) {
	restoreProviderAuthority := database.SuspendProviderTestAuthority()
	allowUnfencedSeahorseProviderForTests.Store(false)
	t.Cleanup(func() {
		allowUnfencedSeahorseProviderForTests.Store(true)
		restoreProviderAuthority()
	})
	onlineFence, err := database.AcquireOnlineFence(filepath.Join(t.TempDir(), "launcher-home"))
	if err != nil {
		t.Fatal(err)
	}
	defer onlineFence.Close()
	path := filepath.Join(t.TempDir(), "must-not-exist", "seahorse.db")
	engine, err := newLocalEngine(Config{databasePath: path}, nil)
	if engine != nil || database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("online-fenced newLocalEngine() = %#v, %v", engine, err)
	}
	if _, statErr := os.Lstat(filepath.Dir(path)); !os.IsNotExist(statErr) {
		t.Fatalf("online-fenced local opener touched provider root: %v", statErr)
	}
}

func TestSeahorseOfflineMigrationUsesExclusiveFence(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	path := filepath.Join(home, "workspace", "sessions", "seahorse.db")
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	if migrationErr := RunOfflineDatabaseMigration(path); migrationErr != nil {
		_ = fence.Close()
		t.Fatalf("RunOfflineDatabaseMigration() error = %v", migrationErr)
	}
	if closeErr := fence.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	db, err := sqliteprovider.OpenStore(path, 5*time.Second)
	if err != nil {
		t.Fatalf("reopen migrated Seahorse store: %v", err)
	}
	defer db.Close()
	if err := validateCurrentSchema(db); err != nil {
		t.Fatalf("migrated Seahorse schema is not current: %v", err)
	}
	store := &Store{db: db}
	if _, err := store.GetOrCreateConversation(t.Context(), "offline-migrated"); err != nil {
		t.Fatalf("use migrated store: %v", err)
	}
}
