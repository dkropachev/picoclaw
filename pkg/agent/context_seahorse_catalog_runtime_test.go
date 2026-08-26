//go:build !mipsle && !netbsd && !(freebsd && arm)

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/seahorse"
)

func TestSeahorseCatalogRuntimeReloadBuildsFreshFactoryTopologyAfterClosingA(t *testing.T) {
	root := t.TempDir()
	cfgA := seahorseCatalogRuntimeConfig(t, root, []string{"alpha"})
	providerA := &seahorseTestProvider{}
	messageBus := bus.NewMessageBus()
	loop := newTestAgentLoopWithStrictModels(cfgA, messageBus, providerA)
	t.Cleanup(func() {
		loop.Close()
		messageBus.Close()
	})

	managerA := requireSeahorseCatalogRuntimeManager(t, loop)
	registryA := loop.GetRegistry()
	alpha := requireSeahorseCatalogRuntimeAgent(t, registryA, "alpha")
	assertSeahorseCatalogRuntimeRoots(t, alpha, true)
	if _, err := managerA.engines["alpha"].Ingest(
		context.Background(),
		"alpha:generation-a",
		[]seahorse.Message{{Role: "user", Content: "generation-a-private-canary"}},
	); err != nil {
		t.Fatal(err)
	}

	cfgB := seahorseCatalogRuntimeConfig(t, root, []string{"beta", "gamma"})
	providerB := &seahorseTestProvider{}
	ensureStrictTestModelSelection(cfgB, providerB)
	var resolverCalled bool
	var aClosedBeforeB bool
	var aRegistryOpenDuringB bool
	loop.contextResolver = func(
		ctx context.Context,
		name string,
		raw json.RawMessage,
		owner *AgentLoop,
	) (ContextManager, error) {
		resolverCalled = true
		if name != "seahorse" || owner != loop {
			return nil, fmt.Errorf("unexpected context resolver input %q/%p", name, owner)
		}
		engine, _ := managerA.engineForSession("generation-a-close-probe")
		aClosedBeforeB = managerA.closed && engine == nil
		aRegistryOpenDuringB = alpha.Tools.HasRegistered(seahorse.ShortGrepToolName) &&
			alpha.Tools.HasRegistered(seahorse.ShortExpandToolName)
		return newSeahorseContextManagerWithDependencies(
			ctx,
			raw,
			owner,
			defaultSeahorseContextDependencies(),
		)
	}

	if err := loop.ReloadProviderAndConfig(context.Background(), providerB, cfgB); err != nil {
		t.Fatal(err)
	}
	if !resolverCalled || !aClosedBeforeB || !aRegistryOpenDuringB {
		t.Fatalf(
			"reload order = resolver:%t A-closed:%t A-registry-open:%t",
			resolverCalled,
			aClosedBeforeB,
			aRegistryOpenDuringB,
		)
	}
	if alpha.Tools.Count() != 0 {
		t.Fatalf("generation A registry retained %d tool(s) after reload", alpha.Tools.Count())
	}

	managerB := requireSeahorseCatalogRuntimeManager(t, loop)
	if managerB == managerA || len(managerB.engines) != 2 ||
		managerB.engines["beta"] == nil || managerB.engines["gamma"] == nil {
		t.Fatalf("generation B manager/engines = %p %#v", managerB, managerB.engines)
	}
	registryB := loop.GetRegistry()
	beta := requireSeahorseCatalogRuntimeAgent(t, registryB, "beta")
	gamma := requireSeahorseCatalogRuntimeAgent(t, registryB, "gamma")
	for _, agent := range []*AgentInstance{beta, gamma} {
		assertSeahorseCatalogRuntimeRoots(t, agent, true)
		if _, err := os.Stat(filepath.Join(agent.Workspace, "sessions", "seahorse.db")); err != nil {
			t.Fatalf("agent %q Seahorse DB: %v", agent.ID, err)
		}
	}
	betaGrep, _ := beta.Tools.GetRegistered(seahorse.ShortGrepToolName)
	gammaGrep, _ := gamma.Tools.GetRegistered(seahorse.ShortGrepToolName)
	if betaGrep == gammaGrep || managerB.engines["beta"] == managerB.engines["gamma"] {
		t.Fatal("generation B agents share a retrieval wrapper or engine")
	}
	result := betaGrep.Execute(context.Background(), map[string]any{
		"pattern":           "generation-a-private-canary",
		"all_conversations": true,
	})
	if result == nil || result.IsError || strings.Contains(result.ForLLM, "generation-a-private-canary") {
		t.Fatalf("generation B retrieval observed A data: %#v", result)
	}

	clone := beta.Tools.Clone()
	cloneGrep, ok := clone.GetRegistered(seahorse.ShortGrepToolName)
	if !ok || cloneGrep != betaGrep {
		t.Fatal("shallow compatibility Clone stopped sharing the live Seahorse wrapper")
	}
	clone.Unregister(seahorse.ShortGrepToolName)
	if !beta.Tools.HasRegistered(seahorse.ShortGrepToolName) {
		t.Fatal("mutating a shallow clone changed its source registry")
	}
	if err := clone.Close(); err != nil {
		t.Fatal(err)
	}
	if requireSeahorseCatalogRuntimeManager(t, loop) != managerB {
		t.Fatal("closing a shallow clone retired the context manager")
	}
}

func TestSeahorseCatalogRuntimeReloadCandidateFailureFallsBackWithoutRoots(t *testing.T) {
	for _, mode := range []string{"engine", "bootstrap"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			cfgA := seahorseCatalogRuntimeConfig(t, root, []string{"alpha"})
			messageBus := bus.NewMessageBus()
			loop := newTestAgentLoopWithStrictModels(
				cfgA,
				messageBus,
				&seahorseTestProvider{},
			)
			t.Cleanup(func() {
				loop.Close()
				messageBus.Close()
			})
			managerA := requireSeahorseCatalogRuntimeManager(t, loop)
			registryA := loop.GetRegistry()
			alpha := requireSeahorseCatalogRuntimeAgent(t, registryA, "alpha")

			cfgB := seahorseCatalogRuntimeConfig(t, root, []string{"beta", "gamma"})
			providerB := &seahorseTestProvider{}
			ensureStrictTestModelSelection(cfgB, providerB)
			base := defaultSeahorseContextDependencies()
			created := make([]*seahorse.Engine, 0, 2)
			closed := make(map[*seahorse.Engine]int)
			var creationCalls int
			var bootstrapCalls int
			var candidateErr error
			var aClosedBeforeB bool
			loop.contextResolver = func(
				ctx context.Context,
				_ string,
				raw json.RawMessage,
				owner *AgentLoop,
			) (ContextManager, error) {
				engine, _ := managerA.engineForSession("generation-a-failure-probe")
				aClosedBeforeB = managerA.closed && engine == nil &&
					alpha.Tools.HasRegistered(seahorse.ShortGrepToolName)
				if mode == "bootstrap" {
					if err := seedSeahorseCatalogRuntimeSessions(owner.GetRegistry()); err != nil {
						return nil, err
					}
				}
				deps := base
				deps.newEngine = func(
					cfg seahorse.Config,
					complete seahorse.CompleteFn,
				) (*seahorse.Engine, error) {
					creationCalls++
					if mode == "engine" && creationCalls == 2 {
						return nil, errors.New("injected later engine failure")
					}
					candidate, err := base.newEngine(cfg, complete)
					if candidate != nil {
						created = append(created, candidate)
					}
					return candidate, err
				}
				deps.closeEngine = func(engine *seahorse.Engine) error {
					closed[engine]++
					return base.closeEngine(engine)
				}
				if mode == "bootstrap" {
					deps.bootstrap = func(
						context.Context,
						*seahorseContextManager,
						*AgentInstance,
						*seahorse.Engine,
						string,
					) error {
						bootstrapCalls++
						if bootstrapCalls == 2 {
							return errors.New("injected later bootstrap failure")
						}
						return nil
					}
				}
				candidate, err := newSeahorseContextManagerWithDependencies(
					ctx,
					raw,
					owner,
					deps,
				)
				candidateErr = err
				return candidate, err
			}

			if err := loop.ReloadProviderAndConfig(context.Background(), providerB, cfgB); err != nil {
				t.Fatal(err)
			}
			if !aClosedBeforeB || candidateErr == nil ||
				!strings.Contains(candidateErr.Error(), "injected later "+mode+" failure") {
				t.Fatalf("%s fallback order/error = %t / %v", mode, aClosedBeforeB, candidateErr)
			}
			if _, ok := loop.contextManager.(*legacyContextManager); !ok {
				t.Fatalf("%s candidate manager = %T, want legacy fallback", mode, loop.contextManager)
			}
			registryB := loop.GetRegistry()
			for _, agentID := range []string{"beta", "gamma"} {
				assertSeahorseCatalogRuntimeRoots(
					t,
					requireSeahorseCatalogRuntimeAgent(t, registryB, agentID),
					false,
				)
			}
			if alpha.Tools.Count() != 0 {
				t.Fatalf("%s reload did not retire generation A registry", mode)
			}
			wantCreated := 1
			if mode == "bootstrap" {
				wantCreated = 2
			}
			if len(created) != wantCreated {
				t.Fatalf("%s created engines = %d, want %d", mode, len(created), wantCreated)
			}
			for _, engine := range created {
				if closed[engine] != 1 {
					t.Fatalf("%s engine %p close calls = %d, want 1", mode, engine, closed[engine])
				}
			}
		})
	}
}

func TestSeahorseCatalogRuntimeCanceledReloadBootstrapClosesCandidate(t *testing.T) {
	root := t.TempDir()
	cfgA := seahorseCatalogRuntimeConfig(t, root, []string{"alpha"})
	messageBus := bus.NewMessageBus()
	loop := newTestAgentLoopWithStrictModels(cfgA, messageBus, &seahorseTestProvider{})
	t.Cleanup(func() {
		loop.Close()
		messageBus.Close()
	})
	managerA := requireSeahorseCatalogRuntimeManager(t, loop)

	cfgB := seahorseCatalogRuntimeConfig(t, root, []string{"beta", "gamma"})
	providerB := &seahorseTestProvider{}
	ensureStrictTestModelSelection(cfgB, providerB)
	base := defaultSeahorseContextDependencies()
	entered := make(chan struct{})
	created := make([]*seahorse.Engine, 0, 2)
	closed := make(map[*seahorse.Engine]int)
	var candidateErr error
	loop.contextResolver = func(
		ctx context.Context,
		_ string,
		raw json.RawMessage,
		owner *AgentLoop,
	) (ContextManager, error) {
		if err := seedSeahorseCatalogRuntimeSessions(owner.GetRegistry()); err != nil {
			return nil, err
		}
		deps := base
		deps.newEngine = func(
			cfg seahorse.Config,
			complete seahorse.CompleteFn,
		) (*seahorse.Engine, error) {
			engine, err := base.newEngine(cfg, complete)
			if engine != nil {
				created = append(created, engine)
			}
			return engine, err
		}
		deps.closeEngine = func(engine *seahorse.Engine) error {
			closed[engine]++
			return base.closeEngine(engine)
		}
		first := true
		deps.bootstrap = func(
			ctx context.Context,
			_ *seahorseContextManager,
			_ *AgentInstance,
			_ *seahorse.Engine,
			_ string,
		) error {
			if first {
				first = false
				close(entered)
			}
			<-ctx.Done()
			return ctx.Err()
		}
		candidate, err := newSeahorseContextManagerWithDependencies(
			ctx,
			raw,
			owner,
			deps,
		)
		candidateErr = err
		return candidate, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- loop.ReloadProviderAndConfig(ctx, providerB, cfgB)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("reload did not reach the candidate bootstrap")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled reload error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled Seahorse reload did not return")
	}
	if candidateErr == nil || !errors.Is(candidateErr, context.Canceled) {
		t.Fatalf("candidate cancellation error = %v", candidateErr)
	}
	if !managerA.closed {
		t.Fatal("reload did not close generation A before canceled B bootstrap")
	}
	if _, ok := loop.contextManager.(*legacyContextManager); !ok {
		t.Fatalf("canceled B manager = %T, want legacy fallback", loop.contextManager)
	}
	for _, agentID := range []string{"beta", "gamma"} {
		assertSeahorseCatalogRuntimeRoots(
			t,
			requireSeahorseCatalogRuntimeAgent(t, loop.GetRegistry(), agentID),
			false,
		)
	}
	if len(created) != 2 {
		t.Fatalf("canceled B created engines = %d, want 2", len(created))
	}
	for _, engine := range created {
		if closed[engine] != 1 {
			t.Fatalf("canceled B engine %p close calls = %d, want 1", engine, closed[engine])
		}
	}
}

func TestSeahorseCatalogRuntimeShutdownClosesEnginesBeforeSourceRegistries(t *testing.T) {
	root := t.TempDir()
	cfg := seahorseCatalogRuntimeConfig(t, root, []string{"alpha", "beta"})
	messageBus := bus.NewMessageBus()
	t.Cleanup(messageBus.Close)
	loop := newTestAgentLoopWithStrictModels(cfg, messageBus, &seahorseTestProvider{})
	manager := requireSeahorseCatalogRuntimeManager(t, loop)
	registry := loop.GetRegistry()
	agents := []*AgentInstance{
		requireSeahorseCatalogRuntimeAgent(t, registry, "alpha"),
		requireSeahorseCatalogRuntimeAgent(t, registry, "beta"),
	}
	for _, agent := range agents {
		assertSeahorseCatalogRuntimeRoots(t, agent, true)
	}

	baseClose := manager.closeEngine
	closeCalls := make(map[*seahorse.Engine]int)
	registryOpenAtClose := true
	manager.closeEngine = func(engine *seahorse.Engine) error {
		for _, agent := range agents {
			registryOpenAtClose = registryOpenAtClose &&
				agent.Tools.HasRegistered(seahorse.ShortGrepToolName) &&
				agent.Tools.HasRegistered(seahorse.ShortExpandToolName)
		}
		closeCalls[engine]++
		return baseClose(engine)
	}
	engines := []*seahorse.Engine{manager.engines["alpha"], manager.engines["beta"]}

	loop.Close()
	if !registryOpenAtClose {
		t.Fatal("source registry closed before its borrowed Seahorse engine")
	}
	if !manager.closed {
		t.Fatal("shutdown did not close the Seahorse context manager")
	}
	for _, engine := range engines {
		if closeCalls[engine] != 1 {
			t.Fatalf("engine %p close calls = %d, want 1", engine, closeCalls[engine])
		}
	}
	for _, agent := range agents {
		if agent.Tools.Count() != 0 {
			t.Fatalf("agent %q registry retained %d tool(s) after shutdown", agent.ID, agent.Tools.Count())
		}
	}

	loop.Close()
	for _, engine := range engines {
		if closeCalls[engine] != 1 {
			t.Fatalf("idempotent shutdown reclosed engine %p: %d", engine, closeCalls[engine])
		}
	}
}

func seahorseCatalogRuntimeConfig(
	t *testing.T,
	root string,
	agentIDs []string,
) *config.Config {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = filepath.Join(root, "default")
	cfg.Agents.Defaults.ContextManager = "seahorse"
	cfg.Agents.List = make([]config.AgentConfig, 0, len(agentIDs))
	for index, agentID := range agentIDs {
		workspace := filepath.Join(root, "workspace-"+agentID)
		if err := os.MkdirAll(workspace, 0o755); err != nil {
			t.Fatal(err)
		}
		cfg.Agents.List = append(cfg.Agents.List, config.AgentConfig{
			ID: agentID, Default: index == 0, Workspace: workspace,
		})
	}
	return cfg
}

func requireSeahorseCatalogRuntimeManager(
	t *testing.T,
	loop *AgentLoop,
) *seahorseContextManager {
	t.Helper()
	manager, ok := loop.contextManager.(*seahorseContextManager)
	if !ok || manager == nil {
		t.Fatalf("context manager = %T, want *seahorseContextManager", loop.contextManager)
	}
	return manager
}

func requireSeahorseCatalogRuntimeAgent(
	t *testing.T,
	registry *AgentRegistry,
	agentID string,
) *AgentInstance {
	t.Helper()
	agent, ok := registry.GetAgent(agentID)
	if !ok || agent == nil {
		t.Fatalf("agent %q is unavailable", agentID)
	}
	return agent
}

func assertSeahorseCatalogRuntimeRoots(
	t *testing.T,
	agent *AgentInstance,
	want bool,
) {
	t.Helper()
	for _, name := range []string{
		seahorse.ShortGrepToolName,
		seahorse.ShortExpandToolName,
	} {
		registered := agent.Tools.HasRegistered(name)
		factoryBacked := toolCapabilityIsFactoryBacked(agent.Tools, name)
		if registered != want || factoryBacked != want {
			t.Fatalf(
				"agent %q root %q = registered:%t factory:%t, want %t",
				agent.ID,
				name,
				registered,
				factoryBacked,
				want,
			)
		}
	}
}

func seedSeahorseCatalogRuntimeSessions(registry *AgentRegistry) error {
	for _, agentID := range registry.ListAgentIDs() {
		agent, ok := registry.GetAgent(agentID)
		if !ok || agent == nil || agent.Sessions == nil {
			return fmt.Errorf("agent %q session store is unavailable", agentID)
		}
		key := agentID + ":seahorse-runtime-bootstrap"
		agent.Sessions.SetHistory(key, []providers.Message{{
			Role: "user", Content: "bootstrap " + agentID,
		}})
		if err := agent.Sessions.Save(key); err != nil {
			return fmt.Errorf("save agent %q bootstrap session: %w", agentID, err)
		}
	}
	return nil
}
