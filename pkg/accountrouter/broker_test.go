package accountrouter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type accountRouterBrokerFixture struct {
	home      string
	workspace string
	cfg       *config.Config
	handler   *BrokerHandler
	server    *database.Server
	client    *database.Client
}

func TestAccountRouterRuntimeConstructorRequiresBrokerAndHidesProviderPath(t *testing.T) {
	previous := database.RuntimeClient()
	database.InstallProcessClient(nil)
	restoreProviderAuthority := database.SuspendProviderTestAuthority()
	allowUnfencedAccountRouterProviderForTests.Store(false)
	t.Cleanup(func() {
		allowUnfencedAccountRouterProviderForTests.Store(true)
		restoreProviderAuthority()
		database.InstallProcessClient(previous)
	})
	workspace := filepath.Join(t.TempDir(), "must-not-exist")
	onlineFence, err := database.AcquireOnlineFence(filepath.Join(t.TempDir(), "launcher-home"))
	if err != nil {
		t.Fatal(err)
	}
	local, localErr := newSQLiteRouter(
		"router-main", testAccountRouterConfig(), testAccountRouterAccounts(), databasePath(workspace),
	)
	if local != nil || database.CodeOf(localErr) != database.CodeUnauthorized {
		t.Fatalf("online-fenced local constructor = %#v, %v", local, localErr)
	}
	if err := onlineFence.Close(); err != nil {
		t.Fatal(err)
	}
	router := NewForWorkspace(
		"router-main", testAccountRouterConfig(), testAccountRouterAccounts(), workspace,
	)
	if router != nil {
		t.Fatalf("NewForWorkspace() = %#v", router)
	}
	if err := InvalidateCredentialAuthFailureForWorkspace(
		workspace, "openai:work",
	); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("InvalidateCredentialAuthFailureForWorkspace() error = %v", err)
	}
	if _, statErr := os.Lstat(workspace); !os.IsNotExist(statErr) {
		t.Fatalf("runtime constructor touched provider root: %v", statErr)
	}
}

func newAccountRouterBrokerFixture(t *testing.T) *accountRouterBrokerFixture {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	if err := config.SaveConfig(filepath.Join(home, "config.json"), cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	handler, err := NewBrokerHandler(home, cfg)
	if err != nil {
		t.Fatalf("NewBrokerHandler() error = %v", err)
	}
	server, err := database.StartServer(context.Background(), database.ServerOptions{
		Home: home, Handler: handler, CloseHandler: handler.Close,
	})
	if err != nil {
		t.Fatalf("StartServer() error = %v", err)
	}
	client, err := database.Connect(home)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	fixture := &accountRouterBrokerFixture{
		home: home, workspace: workspace, cfg: cfg, handler: handler, server: server, client: client,
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Close(ctx); err != nil {
			t.Errorf("server.Close() error = %v", err)
		}
		handler.mu.RLock()
		closed := handler.closed
		handler.mu.RUnlock()
		handler.store.mu.Lock()
		poolOpen := handler.store.retainedDB != nil
		handler.store.mu.Unlock()
		if !closed || poolOpen {
			t.Errorf("handler cleanup = closed=%t pool_open=%t", closed, poolOpen)
		}
	})
	return fixture
}

func (fixture *accountRouterBrokerFixture) router(
	client *database.Client,
	name string,
	routerConfig *config.AccountRouterConfig,
	accounts map[string]Account,
) *Router {
	router := newRouterWithStore(name, routerConfig, accounts, nil)
	router.broker = client
	router.storeID = fixture.handler.storeID
	return router
}

func TestAccountRouterBrokerSelectionRecordingAndSnapshots(t *testing.T) {
	fixture := newAccountRouterBrokerFixture(t)
	router := fixture.router(fixture.client, "router-main", testAccountRouterConfig(), testAccountRouterAccounts())
	if router.StoreID() != AccountRoutingStoreID || router.store != nil {
		t.Fatalf("broker router leaked local state: %#v", router)
	}
	selection := router.Select("session-broker", SelectReasonInitial)
	if got := selectedAccount(t, selection); got != "account-a" {
		t.Fatalf("selected account = %q, want account-a", got)
	}
	router.RecordFallbackResult(selection, successResult(selection, 42), nil)
	state, found := router.AccountStateSnapshot("account-a")
	if !found || state.Requests != 1 || state.TotalTokens != 42 {
		t.Fatalf("AccountStateSnapshot() = %#v, %t", state, found)
	}
	keys, err := fixture.clientSessionKeys("router-main")
	if err != nil || len(keys) != 1 || keys[0] != "session-broker" {
		t.Fatalf("session keys = %v, %v", keys, err)
	}

	failure := &providers.FallbackResult{Attempts: []providers.FallbackAttempt{{
		Provider: "openai", Model: "model-a", IdentityKey: selection.Candidates[0].StableKey(),
		Reason: providers.FailoverAuth, Error: errors.New("private upstream detail"),
	}}}
	router.RecordPrivateFallbackResult(selection, failure, errors.New("private upstream detail"))
	state, found = router.AccountStateSnapshot("account-a")
	if !found || state.Reason != providers.FailoverAuth || state.LastError != errPrivateProviderRequest.Error() {
		t.Fatalf("private failure snapshot = %#v, %t", state, found)
	}

	var response accountRouterSessionKeysResponse
	err = fixture.client.Call(
		t.Context(), BrokerDomain, BrokerVersion, accountRouterOperationSessionKeys,
		accountRouterNamedRequest{StoreID: "global.auth", RouterName: "router-main"}, &response,
	)
	if database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("foreign StoreID error = %v", err)
	}
}

func (fixture *accountRouterBrokerFixture) clientSessionKeys(routerName string) ([]string, error) {
	var response accountRouterSessionKeysResponse
	err := fixture.client.Call(
		context.Background(), BrokerDomain, BrokerVersion, accountRouterOperationSessionKeys,
		accountRouterNamedRequest{StoreID: fixture.handler.storeID, RouterName: routerName}, &response,
	)
	return response.Keys, err
}

func TestAccountRouterBrokerCredentialInvalidation(t *testing.T) {
	fixture := newAccountRouterBrokerFixture(t)
	target := "credential:copilot:work"
	sibling := "credential:copilot:worker"
	routerConfig := &config.AccountRouterConfig{
		Enabled: true, Entry: "target",
		Blocks: []config.AccountRouterBlock{
			{ID: "target", Type: config.AccountRouterBlockTypeAccount, Account: target, Fallback: "sibling"},
			{ID: "sibling", Type: config.AccountRouterBlockTypeAccount, Account: sibling},
		},
	}
	accounts := map[string]Account{
		target:  {Candidates: []providers.FallbackCandidate{credentialCandidate(target)}},
		sibling: {Candidates: []providers.FallbackCandidate{credentialCandidate(sibling)}},
	}
	router := fixture.router(fixture.client, "router-invalidation", routerConfig, accounts)
	selection := router.Select("session", SelectReasonInitial)
	recordSelectionFailure(t, router, selection, target, providers.FailoverAuth)
	if state, ok := router.AccountStateSnapshot(target); !ok || state.Reason != providers.FailoverAuth {
		t.Fatalf("pre-invalidation state = %#v, %t", state, ok)
	}
	var response accountRouterMutationResponse
	err := fixture.client.CallWithOptions(
		t.Context(), BrokerDomain, BrokerVersion, accountRouterOperationInvalidate,
		accountRouterInvalidationRequest{StoreID: AccountRoutingStoreID, CredentialID: "github-copilot:work"},
		&response, database.CallOptions{Mutation: true},
	)
	if err != nil || !response.Updated {
		t.Fatalf("invalidate RPC = %#v, %v", response, err)
	}
	selection = router.Select("session", SelectReasonInitial)
	if got := selectedAccount(t, selection); got != target {
		t.Fatalf("account after invalidation = %q, want %q", got, target)
	}
}

func TestAccountRouterBrokerConcurrentClientsShareRetainedPool(t *testing.T) {
	fixture := newAccountRouterBrokerFixture(t)
	const clients = 24
	var wait sync.WaitGroup
	errorsOut := make(chan error, clients)
	for index := range clients {
		client, err := database.Connect(fixture.home)
		if err != nil {
			t.Fatalf("Connect(%d) error = %v", index, err)
		}
		router := fixture.router(client, "router-concurrent", testAccountRouterConfig(), testAccountRouterAccounts())
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			selection := router.Select(fmt.Sprintf("session-%02d", index), SelectReasonInitial)
			if len(selection.Candidates) == 0 {
				errorsOut <- errors.New("selection unavailable")
				return
			}
			router.RecordFallbackResult(selection, successResult(selection, index+1), nil)
		}(index)
	}
	wait.Wait()
	close(errorsOut)
	for err := range errorsOut {
		t.Errorf("concurrent operation: %v", err)
	}
	keys, err := fixture.clientSessionKeys("router-concurrent")
	if err != nil || len(keys) != clients {
		t.Fatalf("concurrent session keys = %d, %v", len(keys), err)
	}
	fixture.handler.store.mu.Lock()
	poolOpen := fixture.handler.store.retainedDB != nil
	fixture.handler.store.mu.Unlock()
	if !poolOpen {
		t.Fatal("broker did not retain account-router pool")
	}
}

func TestAccountRouterRuntimeConstructorIgnoresPathAndFailsClosed(t *testing.T) {
	if os.Getenv("PICOCLAW_ACCOUNT_ROUTER_BROKER_HELPER") == "1" {
		if _, _, err := database.ConnectInherited(context.Background()); err != nil {
			t.Fatalf("ConnectInherited() error = %v", err)
		}
		forbidden := os.Getenv("PICOCLAW_ACCOUNT_ROUTER_FORBIDDEN_PATH")
		router, err := newSQLiteRouter(
			"router-runtime", testAccountRouterConfig(), testAccountRouterAccounts(), forbidden,
		)
		if err != nil || router == nil {
			t.Fatalf("newSQLiteRouter() = %#v, %v", router, err)
		}
		if router.StoreID() != AccountRoutingStoreID || router.store != nil {
			t.Fatalf("runtime router leaked path/local store: %#v", router)
		}
		if selection := router.Select("runtime-session", SelectReasonInitial); len(selection.Candidates) == 0 {
			t.Fatal("runtime broker selection failed")
		}
		keys, err := SessionKeys(forbidden, "router-runtime")
		if err != nil || len(keys) != 1 {
			t.Fatalf("SessionKeys(runtime) = %v, %v", keys, err)
		}
		if _, err := os.Stat(forbidden); !os.IsNotExist(err) {
			t.Fatalf("runtime created forbidden state path: %v", err)
		}
		return
	}

	fixture := newAccountRouterBrokerFixture(t)
	authority, err := database.InheritedAuthorityEnvironment(fixture.home)
	if err != nil {
		t.Fatalf("InheritedAuthorityEnvironment() error = %v", err)
	}
	forbidden := filepath.Join(t.TempDir(), "must-not-exist", "state.db")
	command := exec.Command(os.Args[0], "-test.run=^TestAccountRouterRuntimeConstructorIgnoresPathAndFailsClosed$")
	command.Env = append(
		os.Environ(),
		"PICOCLAW_ACCOUNT_ROUTER_BROKER_HELPER=1",
		"PICOCLAW_ACCOUNT_ROUTER_FORBIDDEN_PATH="+forbidden,
		config.EnvHome+"="+fixture.home,
		config.EnvConfig+"="+filepath.Join(fixture.home, "config.json"),
		authority,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("runtime helper: %v\n%s", err, output)
	}
}
