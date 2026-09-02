//go:build !mipsle && !netbsd && !(freebsd && arm)

package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/providers/promptir"
	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/seahorse"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type seahorseCatalogAgentSpec struct {
	id        string
	workspace string
	allowlist []string
	sessions  []string
	defaultID bool
}

type seahorseCatalogFixture struct {
	loop     *AgentLoop
	registry *AgentRegistry
	agents   map[string]*AgentInstance
}

func newSeahorseCatalogFixture(
	t *testing.T,
	specs ...seahorseCatalogAgentSpec,
) *seahorseCatalogFixture {
	t.Helper()
	if len(specs) == 0 {
		specs = []seahorseCatalogAgentSpec{{id: routing.DefaultAgentID, defaultID: true}}
	}
	cfg := &config.Config{}
	cfg.Agents.List = make([]config.AgentConfig, 0, len(specs))
	agents := make(map[string]*AgentInstance, len(specs))
	for index, spec := range specs {
		workspace := spec.workspace
		if workspace == "" {
			workspace = filepath.Join(t.TempDir(), spec.id)
		}
		store := session.NewSessionManager("")
		for _, sessionKey := range spec.sessions {
			store.AddFullMessage(sessionKey, providers.Message{
				Role: "user", Content: "bootstrap " + sessionKey,
			})
		}
		registry := tools.NewToolRegistry()
		if spec.allowlist != nil {
			registry.SetAllowlist(spec.allowlist)
		}
		agent := &AgentInstance{
			ID: spec.id, Workspace: workspace,
			Tools: registry, Sessions: store,
		}
		agents[spec.id] = agent
		isDefault := spec.defaultID
		if index == 0 && !slices.ContainsFunc(specs, func(item seahorseCatalogAgentSpec) bool {
			return item.defaultID
		}) {
			isDefault = true
		}
		cfg.Agents.List = append(cfg.Agents.List, config.AgentConfig{
			ID: spec.id, Default: isDefault, Workspace: workspace,
		})
	}
	registry := &AgentRegistry{cfg: cfg, agents: agents}
	fixture := &seahorseCatalogFixture{
		loop:     &AgentLoop{cfg: cfg, registry: registry},
		registry: registry,
		agents:   agents,
	}
	t.Cleanup(registry.Close)
	return fixture
}

func (fixture *seahorseCatalogFixture) noShortRoots(t *testing.T) {
	t.Helper()
	for agentID, agent := range fixture.agents {
		for _, name := range []string{
			seahorse.ShortGrepToolName,
			seahorse.ShortExpandToolName,
		} {
			if agent.Tools.HasRegistered(name) {
				t.Fatalf("agent %q unexpectedly registered %q", agentID, name)
			}
		}
	}
}

type seahorseCatalogLegacyTool struct {
	name string
}

func (tool *seahorseCatalogLegacyTool) Name() string { return tool.name }

func (*seahorseCatalogLegacyTool) Description() string { return "Seahorse catalog collision" }

func (*seahorseCatalogLegacyTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (*seahorseCatalogLegacyTool) Execute(
	context.Context,
	map[string]any,
) *tools.ToolResult {
	return tools.SilentResult("collision")
}

func trackedSeahorseCloser(
	mu *sync.Mutex,
	closed *[]*seahorse.Engine,
) seahorseEngineCloser {
	return func(engine *seahorse.Engine) error {
		mu.Lock()
		*closed = append(*closed, engine)
		mu.Unlock()
		return engine.Close()
	}
}

func TestSeahorseCatalogDependencyAndSnapshotFailuresPrecedeEngineIO(t *testing.T) {
	fixture := newSeahorseCatalogFixture(t, seahorseCatalogAgentSpec{
		id: routing.DefaultAgentID, defaultID: true,
	})
	valid := defaultSeahorseContextDependencies()
	tests := []struct {
		name string
		deps seahorseContextDependencies
	}{
		{name: "nil engine factory", deps: func() seahorseContextDependencies {
			deps := valid
			deps.newEngine = nil
			return deps
		}()},
		{name: "nil engine closer", deps: func() seahorseContextDependencies {
			deps := valid
			deps.closeEngine = nil
			return deps
		}()},
		{name: "nil installer", deps: func() seahorseContextDependencies {
			deps := valid
			deps.install = nil
			return deps
		}()},
		{name: "nil bootstrap", deps: func() seahorseContextDependencies {
			deps := valid
			deps.bootstrap = nil
			return deps
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, err := newSeahorseContextManagerWithDependencies(
				context.Background(), nil, fixture.loop, test.deps,
			)
			if err == nil || manager != nil {
				t.Fatalf("dependency validation = %T, %v", manager, err)
			}
		})
	}
	if manager, err := newSeahorseContextManagerWithDependencies(
		context.Background(), nil, nil, valid,
	); err == nil || manager != nil {
		t.Fatalf("nil loop validation = %T, %v", manager, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	engineCalls := 0
	canceledDeps := valid
	canceledDeps.newEngine = func(
		seahorse.Config,
		seahorse.CompleteFn,
	) (*seahorse.Engine, error) {
		engineCalls++
		return nil, errors.New("must not create")
	}
	if manager, err := newSeahorseContextManagerWithDependencies(
		ctx, nil, fixture.loop, canceledDeps,
	); !errors.Is(err, context.Canceled) || manager != nil || engineCalls != 0 {
		t.Fatalf("pre-snapshot cancellation = %T, %v, calls=%d", manager, err, engineCalls)
	}

	t.Run("nil registry", func(t *testing.T) {
		loop := &AgentLoop{cfg: &config.Config{}}
		calls := 0
		deps := valid
		deps.newEngine = func(seahorse.Config, seahorse.CompleteFn) (*seahorse.Engine, error) {
			calls++
			return nil, errors.New("must not create")
		}
		manager, err := newSeahorseContextManagerWithDependencies(
			context.Background(), nil, loop, deps,
		)
		if err == nil || manager != nil || calls != 0 {
			t.Fatalf("nil registry = %T, %v, calls=%d", manager, err, calls)
		}
	})

	t.Run("empty and nil-agent registries", func(t *testing.T) {
		if candidates, defaultAgent, err := snapshotSeahorseAgents(&AgentRegistry{
			agents: map[string]*AgentInstance{},
		}); err == nil || candidates != nil || defaultAgent != nil {
			t.Fatalf("empty snapshot = %#v, %#v, %v", candidates, defaultAgent, err)
		}
		mainAgent := fixture.agents[routing.DefaultAgentID]
		registry := &AgentRegistry{
			cfg: fixture.registry.cfg,
			agents: map[string]*AgentInstance{
				routing.DefaultAgentID: mainAgent,
				"z-nil":                nil,
			},
		}
		if candidates, defaultAgent, err := snapshotSeahorseAgents(registry); err == nil ||
			candidates != nil || defaultAgent != nil {
			t.Fatalf("nil-agent snapshot = %#v, %#v, %v", candidates, defaultAgent, err)
		}
	})

	t.Run("invalid agent topology", func(t *testing.T) {
		tests := []struct {
			name   string
			mutate func(*seahorseCatalogFixture)
		}{
			{name: "nil tools", mutate: func(f *seahorseCatalogFixture) {
				f.agents[routing.DefaultAgentID].Tools = nil
			}},
			{name: "noncanonical identity", mutate: func(f *seahorseCatalogFixture) {
				f.agents[routing.DefaultAgentID].ID = " Main "
			}},
			{name: "blank workspace", mutate: func(f *seahorseCatalogFixture) {
				f.agents[routing.DefaultAgentID].Workspace = ""
			}},
			{name: "owned tools", mutate: func(f *seahorseCatalogFixture) {
				owned, err := tools.NewOwnedToolRegistry(tools.ToolOwner{
					Scope: tools.ToolOwnerScopeRegistry,
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = owned.Close() })
				f.agents[routing.DefaultAgentID].Tools = owned
			}},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				invalid := newSeahorseCatalogFixture(t, seahorseCatalogAgentSpec{
					id: routing.DefaultAgentID, defaultID: true,
				})
				test.mutate(invalid)
				calls := 0
				deps := valid
				deps.newEngine = func(
					seahorse.Config,
					seahorse.CompleteFn,
				) (*seahorse.Engine, error) {
					calls++
					return nil, errors.New("must not create")
				}
				manager, err := newSeahorseContextManagerWithDependencies(
					context.Background(), nil, invalid.loop, deps,
				)
				if err == nil || manager != nil || calls != 0 {
					t.Fatalf("invalid topology = %T, %v, calls=%d", manager, err, calls)
				}
			})
		}
	})

	t.Run("aliased registries", func(t *testing.T) {
		aliased := newSeahorseCatalogFixture(t,
			seahorseCatalogAgentSpec{id: "alpha", defaultID: true},
			seahorseCatalogAgentSpec{id: "beta"},
		)
		aliased.agents["beta"].Tools = aliased.agents["alpha"].Tools
		calls := 0
		deps := valid
		deps.newEngine = func(seahorse.Config, seahorse.CompleteFn) (*seahorse.Engine, error) {
			calls++
			return nil, errors.New("must not create")
		}
		manager, err := newSeahorseContextManagerWithDependencies(
			context.Background(), nil, aliased.loop, deps,
		)
		if err == nil || manager != nil || calls != 0 {
			t.Fatalf("registry alias = %T, %v, calls=%d", manager, err, calls)
		}
	})
}

func TestSeahorseCatalogCreatesAllEnginesBeforeSortedBootstrapAndInstall(t *testing.T) {
	fixture := newSeahorseCatalogFixture(t,
		seahorseCatalogAgentSpec{
			id: "zeta", sessions: []string{"z-2", "z-1"},
		},
		seahorseCatalogAgentSpec{
			id: "alpha", sessions: []string{"a-2", "a-1"}, defaultID: true,
		},
	)
	workspaceOwners := map[string]string{}
	for id, agent := range fixture.agents {
		workspaceOwners[agent.Workspace] = id
	}
	events := make([]string, 0, 7)
	deps := defaultSeahorseContextDependencies()
	deps.newEngine = func(
		cfg seahorse.Config,
		complete seahorse.CompleteFn,
	) (*seahorse.Engine, error) {
		events = append(events, "engine:"+workspaceOwners[cfg.Workspace])
		return newRuntimeSeahorseEngine(cfg, complete)
	}
	deps.bootstrap = func(
		_ context.Context,
		_ *seahorseContextManager,
		agent *AgentInstance,
		_ *seahorse.Engine,
		sessionKey string,
	) error {
		for _, current := range fixture.agents {
			if current.Tools.Count() != 0 {
				return fmt.Errorf("tool published before bootstrap")
			}
		}
		events = append(events, "bootstrap:"+agent.ID+":"+sessionKey)
		return nil
	}
	deps.install = func(
		batches []tools.FactoryBackedBatch,
	) ([]tools.FactoryBackedAdmission, error) {
		events = append(events, "install")
		return tools.InstallFactoryBackedTransaction(batches)
	}
	managerRaw, err := newSeahorseContextManagerWithDependencies(
		context.Background(), nil, fixture.loop, deps,
	)
	if err != nil {
		t.Fatal(err)
	}
	manager := managerRaw.(*seahorseContextManager)
	t.Cleanup(func() { _ = manager.Close() })
	want := []string{
		"engine:alpha", "engine:zeta",
		"bootstrap:alpha:a-1", "bootstrap:alpha:a-2",
		"bootstrap:zeta:z-1", "bootstrap:zeta:z-2",
		"install",
	}
	if !slices.Equal(events, want) {
		t.Fatalf("constructor events = %#v, want %#v", events, want)
	}
	if len(manager.engines) != 2 || manager.engine != manager.engines["alpha"] {
		t.Fatalf("constructed engines = %#v, default=%p", manager.engines, manager.engine)
	}
}

func TestSeahorseCatalogDefaultAdaptersAndNilContext(t *testing.T) {
	t.Run("background adapter bootstraps production session", func(t *testing.T) {
		fixture := newSeahorseCatalogFixture(t, seahorseCatalogAgentSpec{
			id: routing.DefaultAgentID, defaultID: true,
			sessions: []string{"adapter-bootstrap-session"},
		})
		managerRaw, err := newSeahorseContextManager(nil, fixture.loop)
		if err != nil {
			t.Fatal(err)
		}
		manager := managerRaw.(*seahorseContextManager)
		t.Cleanup(func() { _ = manager.Close() })
		conversation, err := manager.engine.GetRetrieval().Store().GetConversationBySessionKey(
			context.Background(),
			"adapter-bootstrap-session",
		)
		if err != nil || conversation == nil {
			t.Fatalf("production bootstrap conversation = %#v, %v", conversation, err)
		}
	})

	t.Run("nil context and nil session store", func(t *testing.T) {
		fixture := newSeahorseCatalogFixture(t, seahorseCatalogAgentSpec{
			id: routing.DefaultAgentID, defaultID: true,
		})
		fixture.agents[routing.DefaultAgentID].Sessions = nil
		managerRaw, err := newSeahorseContextManagerWithDependencies(
			nil,
			nil,
			fixture.loop,
			defaultSeahorseContextDependencies(),
		)
		if err != nil {
			t.Fatal(err)
		}
		manager := managerRaw.(*seahorseContextManager)
		if manager.engine == nil || len(manager.engines) != 1 {
			t.Fatalf("nil-input manager engines = %#v", manager.engines)
		}
		if err := manager.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestSeahorseCatalogValidatesOpaqueStoreIdentitiesWithoutProviderPaths(t *testing.T) {
	if err := validateSeahorseStoreIdentities([]seahorseAgentCandidate{{
		id: "missing-engine",
	}}); err == nil || !strings.Contains(err.Error(), "engine is unavailable") {
		t.Fatalf("missing engine validation = %v", err)
	}

	first, err := seahorse.NewOfflineEngine(seahorse.OfflineConfig{DatabasePath: ":memory:"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := seahorse.NewOfflineEngine(seahorse.OfflineConfig{DatabasePath: ":memory:"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if first.StoreID().Valid() || second.StoreID().Valid() {
		t.Fatal("offline test engines unexpectedly exposed a broker StoreID")
	}
	if err := validateSeahorseStoreIdentities([]seahorseAgentCandidate{
		{id: "alpha", engine: first},
		{id: "beta", engine: second},
	}); err != nil {
		t.Fatalf("offline injected engine validation = %v", err)
	}
}

func TestSeahorseContextManagerOperationAndCloseEdges(t *testing.T) {
	fixture := newSeahorseCatalogFixture(t, seahorseCatalogAgentSpec{
		id: routing.DefaultAgentID, defaultID: true,
	})
	managerRaw, err := newSeahorseContextManagerWithContext(t.Context(), nil, fixture.loop)
	if err != nil {
		t.Fatal(err)
	}
	manager := managerRaw.(*seahorseContextManager)
	if _, err := manager.Assemble(t.Context(), nil); err == nil {
		t.Fatal("Assemble accepted a nil request")
	}
	if err := manager.Compact(t.Context(), nil); err != nil {
		t.Fatalf("Compact(nil) = %v", err)
	}
	if err := manager.Ingest(t.Context(), nil); err != nil {
		t.Fatalf("Ingest(nil) = %v", err)
	}
	const sessionKey = "clear-edge-session"
	fixture.agents[routing.DefaultAgentID].Sessions.AddFullMessage(
		sessionKey,
		providers.Message{Role: "user", Content: "clear me"},
	)
	if err := manager.Ingest(t.Context(), &IngestRequest{
		SessionKey: sessionKey,
		Message:    providers.Message{Role: "user", Content: "clear me"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Clear(t.Context(), sessionKey); err != nil {
		t.Fatal(err)
	}
	if history := fixture.agents[routing.DefaultAgentID].Sessions.GetHistory(sessionKey); len(history) != 0 {
		t.Fatalf("history after Clear = %#v", history)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Clear(t.Context(), sessionKey); err == nil {
		t.Fatal("Clear succeeded after manager Close")
	}

	for _, test := range []struct {
		mime string
		want string
	}{
		{mime: "image/png", want: string(promptir.PartTypeImage)},
		{mime: "audio/mpeg", want: string(promptir.PartTypeAudio)},
		{mime: "application/pdf", want: string(promptir.PartTypeFile)},
	} {
		if got := promptIRPartTypeFromMime(test.mime); got != test.want {
			t.Fatalf("prompt part for %q = %q, want %q", test.mime, got, test.want)
		}
	}
}

func TestSeahorseCatalogLaterEngineFailuresCloseEveryPrivateEngine(t *testing.T) {
	for _, mode := range []string{"error", "panic", "nil"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newSeahorseCatalogFixture(t,
				seahorseCatalogAgentSpec{id: "gamma"},
				seahorseCatalogAgentSpec{id: "alpha", defaultID: true},
				seahorseCatalogAgentSpec{id: "beta"},
			)
			calls := 0
			created := make([]*seahorse.Engine, 0, 2)
			closed := make([]*seahorse.Engine, 0, 2)
			var closeMu sync.Mutex
			deps := defaultSeahorseContextDependencies()
			deps.newEngine = func(
				cfg seahorse.Config,
				complete seahorse.CompleteFn,
			) (*seahorse.Engine, error) {
				calls++
				if calls == 3 {
					switch mode {
					case "error":
						return nil, errors.New("later engine failure")
					case "panic":
						panic("later engine panic")
					case "nil":
						return nil, nil
					}
				}
				engine, err := newRuntimeSeahorseEngine(cfg, complete)
				if err == nil {
					created = append(created, engine)
				}
				return engine, err
			}
			deps.closeEngine = trackedSeahorseCloser(&closeMu, &closed)
			bootstrapCalls := 0
			installCalls := 0
			deps.bootstrap = func(
				context.Context,
				*seahorseContextManager,
				*AgentInstance,
				*seahorse.Engine,
				string,
			) error {
				bootstrapCalls++
				return nil
			}
			deps.install = func(
				[]tools.FactoryBackedBatch,
			) ([]tools.FactoryBackedAdmission, error) {
				installCalls++
				return nil, nil
			}
			manager, err := newSeahorseContextManagerWithDependencies(
				context.Background(), nil, fixture.loop, deps,
			)
			if err == nil || manager != nil || calls != 3 ||
				bootstrapCalls != 0 || installCalls != 0 {
				t.Fatalf(
					"later %s failure = %T, %v calls=%d bootstrap=%d install=%d",
					mode, manager, err, calls, bootstrapCalls, installCalls,
				)
			}
			if len(created) != 2 || !slices.Equal(closed, created) {
				t.Fatalf("created/closed = %p / %p", created, closed)
			}
			fixture.noShortRoots(t)
		})
	}
}

func TestSeahorseCatalogInvalidEngineRetrievalClosesPrivateEngine(t *testing.T) {
	fixture := newSeahorseCatalogFixture(t, seahorseCatalogAgentSpec{
		id: routing.DefaultAgentID, defaultID: true,
	})
	invalidEngine := &seahorse.Engine{}
	closeCalls := 0
	installCalls := 0
	deps := defaultSeahorseContextDependencies()
	deps.newEngine = func(
		seahorse.Config,
		seahorse.CompleteFn,
	) (*seahorse.Engine, error) {
		return invalidEngine, nil
	}
	deps.closeEngine = func(engine *seahorse.Engine) error {
		closeCalls++
		return engine.Close()
	}
	deps.install = func(
		[]tools.FactoryBackedBatch,
	) ([]tools.FactoryBackedAdmission, error) {
		installCalls++
		return nil, nil
	}
	manager, err := newSeahorseContextManagerWithDependencies(
		context.Background(), nil, fixture.loop, deps,
	)
	if err == nil || manager != nil || closeCalls != 1 || installCalls != 0 ||
		!strings.Contains(err.Error(), "retrieval engine is nil") {
		t.Fatalf(
			"invalid retrieval = %T, %v close=%d install=%d",
			manager,
			err,
			closeCalls,
			installCalls,
		)
	}
	fixture.noShortRoots(t)
}

func TestSeahorseCatalogBootstrapFailuresAndCancellationRollBack(t *testing.T) {
	for _, mode := range []string{"error", "panic", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newSeahorseCatalogFixture(t,
				seahorseCatalogAgentSpec{
					id: "beta", sessions: []string{"beta-session"},
				},
				seahorseCatalogAgentSpec{
					id: "alpha", sessions: []string{"alpha-session"}, defaultID: true,
				},
			)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			created := make([]*seahorse.Engine, 0, 2)
			closed := make([]*seahorse.Engine, 0, 2)
			var closeMu sync.Mutex
			deps := defaultSeahorseContextDependencies()
			deps.newEngine = func(
				cfg seahorse.Config,
				complete seahorse.CompleteFn,
			) (*seahorse.Engine, error) {
				engine, err := newRuntimeSeahorseEngine(cfg, complete)
				if err == nil {
					created = append(created, engine)
				}
				return engine, err
			}
			deps.closeEngine = trackedSeahorseCloser(&closeMu, &closed)
			bootstrapCalls := 0
			deps.bootstrap = func(
				context.Context,
				*seahorseContextManager,
				*AgentInstance,
				*seahorse.Engine,
				string,
			) error {
				bootstrapCalls++
				switch mode {
				case "error":
					return errors.New("bootstrap failure")
				case "panic":
					panic("bootstrap panic")
				case "cancel":
					cancel()
					return nil
				}
				return nil
			}
			installCalls := 0
			deps.install = func(
				[]tools.FactoryBackedBatch,
			) ([]tools.FactoryBackedAdmission, error) {
				installCalls++
				return nil, nil
			}
			manager, err := newSeahorseContextManagerWithDependencies(
				ctx, nil, fixture.loop, deps,
			)
			if err == nil || manager != nil || bootstrapCalls != 1 || installCalls != 0 {
				t.Fatalf(
					"bootstrap %s = %T, %v bootstrap=%d install=%d",
					mode, manager, err, bootstrapCalls, installCalls,
				)
			}
			if len(created) != 2 || !slices.Equal(closed, created) {
				t.Fatalf("bootstrap %s created/closed = %p / %p", mode, created, closed)
			}
			fixture.noShortRoots(t)
		})
	}
}

func TestSeahorseCatalogAllowlistCapabilitiesTraitsAndVersions(t *testing.T) {
	fixture := newSeahorseCatalogFixture(t,
		seahorseCatalogAgentSpec{id: "main", defaultID: true},
		seahorseCatalogAgentSpec{
			id: "grep", allowlist: []string{seahorse.ShortGrepToolName},
		},
		seahorseCatalogAgentSpec{
			id: "expand", allowlist: []string{seahorse.ShortExpandToolName},
		},
		seahorseCatalogAgentSpec{id: "none", allowlist: []string{}},
	)
	var admissions []tools.FactoryBackedAdmission
	var stagedBatches []tools.FactoryBackedBatch
	deps := defaultSeahorseContextDependencies()
	deps.install = func(
		batches []tools.FactoryBackedBatch,
	) ([]tools.FactoryBackedAdmission, error) {
		stagedBatches = batches
		var err error
		admissions, err = tools.InstallFactoryBackedTransaction(batches)
		return admissions, err
	}
	managerRaw, err := newSeahorseContextManagerWithDependencies(
		context.Background(), nil, fixture.loop, deps,
	)
	if err != nil {
		t.Fatal(err)
	}
	manager := managerRaw.(*seahorseContextManager)
	t.Cleanup(func() { _ = manager.Close() })
	if len(manager.engines) != 4 || len(admissions) != 8 {
		t.Fatalf("manager/admissions = %d/%#v", len(manager.engines), admissions)
	}
	if len(stagedBatches) != 4 {
		t.Fatalf("staged batches = %#v", stagedBatches)
	}
	for batchIndex, batch := range stagedBatches {
		if len(batch.Installs) != 2 {
			t.Fatalf("batch %d installs = %#v", batchIndex, batch.Installs)
		}
		for installIndex, install := range batch.Installs {
			if install.Hidden || install.Expected != nil {
				t.Fatalf("insert-only core install %d/%d = %#v", batchIndex, installIndex, install)
			}
			descriptor := install.Factory.Descriptor()
			if descriptor.Name != install.Live.Name() ||
				descriptor.Description != install.Live.Description() ||
				!reflect.DeepEqual(descriptor.Parameters, install.Live.Parameters()) {
				t.Fatalf(
					"live/factory descriptor %d/%d = %#v / %q/%q/%#v",
					batchIndex,
					installIndex,
					descriptor,
					install.Live.Name(),
					install.Live.Description(),
					install.Live.Parameters(),
				)
			}
		}
	}
	for _, admission := range admissions {
		if admission.Replaced {
			t.Fatalf("insert-only admission replaced an occupant: %#v", admission)
		}
	}
	want := map[string]struct {
		grep, expand bool
		version      uint64
	}{
		"main":   {grep: true, expand: true, version: 2},
		"grep":   {grep: true, version: 1},
		"expand": {expand: true, version: 1},
		"none":   {},
	}
	wantTraits := tools.ToolTraits{
		Risk: tools.ToolRiskReadOnly, Parallel: tools.ToolParallelSafe,
		Idempotency: tools.ToolIdempotencyIdempotent,
		Sharing:     tools.ToolSharingPerOwner,
	}
	for agentID, expected := range want {
		agent := fixture.agents[agentID]
		if agent.Tools.HasRegistered(seahorse.ShortGrepToolName) != expected.grep ||
			agent.Tools.HasRegistered(seahorse.ShortExpandToolName) != expected.expand ||
			agent.Tools.Version() != expected.version {
			t.Fatalf(
				"agent %q surface/version = %v/%v/%d, want %#v",
				agentID,
				agent.Tools.HasRegistered(seahorse.ShortGrepToolName),
				agent.Tools.HasRegistered(seahorse.ShortExpandToolName),
				agent.Tools.Version(),
				expected,
			)
		}
		capabilities := make(map[string]tools.ToolInstantiationCapability)
		for _, capability := range agent.Tools.InstantiationCapabilities() {
			capabilities[capability.Name] = capability
		}
		for name, admitted := range map[string]bool{
			seahorse.ShortGrepToolName:   expected.grep,
			seahorse.ShortExpandToolName: expected.expand,
		} {
			capability, exists := capabilities[name]
			if exists != admitted || admitted && (!capability.FactoryBacked || capability.ImmutableShared) {
				t.Fatalf("agent %q capability %q = %#v, %t", agentID, name, capability, exists)
			}
			if admitted {
				traits, ok := agent.Tools.Traits(name)
				if !ok || traits != wantTraits {
					t.Fatalf("agent %q traits %q = %#v, %t", agentID, name, traits, ok)
				}
			}
		}
	}
}

func TestSeahorseCatalogInitialAndLateCollisionsRollBackAllRegistries(t *testing.T) {
	for _, mode := range []string{"initial", "late"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newSeahorseCatalogFixture(t,
				seahorseCatalogAgentSpec{id: "alpha", defaultID: true},
				seahorseCatalogAgentSpec{id: "beta"},
			)
			interloper := &seahorseCatalogLegacyTool{name: seahorse.ShortGrepToolName}
			if mode == "initial" {
				fixture.agents["beta"].Tools.Register(interloper)
			}
			closed := make([]*seahorse.Engine, 0, 2)
			var closeMu sync.Mutex
			var staged []tools.FactoryBackedBatch
			deps := defaultSeahorseContextDependencies()
			deps.closeEngine = trackedSeahorseCloser(&closeMu, &closed)
			deps.install = func(
				batches []tools.FactoryBackedBatch,
			) ([]tools.FactoryBackedAdmission, error) {
				staged = batches
				if mode == "late" {
					fixture.agents["beta"].Tools.Register(interloper)
				}
				return tools.InstallFactoryBackedTransaction(batches)
			}
			manager, err := newSeahorseContextManagerWithDependencies(
				context.Background(), nil, fixture.loop, deps,
			)
			if err == nil || manager != nil || len(closed) != 2 || len(staged) != 2 {
				t.Fatalf("%s collision = %T, %v closed=%d staged=%d", mode, manager, err, len(closed), len(staged))
			}
			if fixture.agents["alpha"].Tools.Count() != 0 {
				t.Fatal("collision partially published alpha roots")
			}
			occupant, ok := fixture.agents["beta"].Tools.GetRegistered(seahorse.ShortGrepToolName)
			if !ok || occupant != interloper ||
				fixture.agents["beta"].Tools.HasRegistered(seahorse.ShortExpandToolName) {
				t.Fatalf("beta collision state = %T, %t", occupant, ok)
			}
			probe := tools.NewToolRegistry()
			if registerErr := probe.RegisterFactoryBacked(
				staged[0].Installs[0].Live,
				staged[0].Installs[0].Factory,
			); registerErr != nil {
				t.Fatalf("collision leaked candidate reservation: %v", registerErr)
			}
			_ = probe.Close()
		})
	}
}

func TestSeahorseCatalogInstallerErrorsAndPanicsClosePrivateEngines(t *testing.T) {
	for _, mode := range []string{"error", "panic"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newSeahorseCatalogFixture(t,
				seahorseCatalogAgentSpec{id: "alpha", defaultID: true},
				seahorseCatalogAgentSpec{id: "beta"},
			)
			closed := make([]*seahorse.Engine, 0, 2)
			var closeMu sync.Mutex
			deps := defaultSeahorseContextDependencies()
			deps.closeEngine = trackedSeahorseCloser(&closeMu, &closed)
			deps.install = func(
				[]tools.FactoryBackedBatch,
			) ([]tools.FactoryBackedAdmission, error) {
				if mode == "panic" {
					panic("installer panic")
				}
				return nil, errors.New("installer error")
			}
			manager, err := newSeahorseContextManagerWithDependencies(
				context.Background(), nil, fixture.loop, deps,
			)
			if err == nil || manager != nil || len(closed) != 2 {
				t.Fatalf("installer %s = %T, %v closed=%d", mode, manager, err, len(closed))
			}
			fixture.noShortRoots(t)
		})
	}
}

func TestSeahorseCatalogPostCommitMalformedAdmissionsRetainManager(t *testing.T) {
	for _, mode := range []string{"short", "replacement"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newSeahorseCatalogFixture(t,
				seahorseCatalogAgentSpec{id: "alpha", defaultID: true},
				seahorseCatalogAgentSpec{id: "beta"},
			)
			closed := make([]*seahorse.Engine, 0, 2)
			var closeMu sync.Mutex
			deps := defaultSeahorseContextDependencies()
			deps.closeEngine = trackedSeahorseCloser(&closeMu, &closed)
			deps.install = func(
				batches []tools.FactoryBackedBatch,
			) ([]tools.FactoryBackedAdmission, error) {
				admissions, err := tools.InstallFactoryBackedTransaction(batches)
				if err != nil {
					return nil, err
				}
				if mode == "short" {
					return admissions[:len(admissions)-1], nil
				}
				admissions[0].Replaced = true
				return admissions, nil
			}
			managerRaw, err := newSeahorseContextManagerWithDependencies(
				context.Background(), nil, fixture.loop, deps,
			)
			if err != nil || managerRaw == nil || len(closed) != 0 {
				t.Fatalf("postcommit %s = %T, %v closed=%d", mode, managerRaw, err, len(closed))
			}
			manager := managerRaw.(*seahorseContextManager)
			for agentID, agent := range fixture.agents {
				if !agent.Tools.HasRegistered(seahorse.ShortGrepToolName) ||
					!agent.Tools.HasRegistered(seahorse.ShortExpandToolName) {
					t.Fatalf("agent %q lost committed roots", agentID)
				}
			}
			if closeErr := manager.Close(); closeErr != nil || len(closed) != 2 {
				t.Fatalf("postcommit manager close = %v, closed=%d", closeErr, len(closed))
			}
		})
	}
}

func TestSeahorseVerifyAdmissionsRejectsMalformedProjection(t *testing.T) {
	registry := tools.NewToolRegistry()
	t.Cleanup(func() { _ = registry.Close() })
	engine, err := seahorse.NewOfflineEngine(seahorse.OfflineConfig{
		DatabasePath: filepath.Join(t.TempDir(), "verify.db"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	grep, grepFactory, err := seahorse.NewGrepToolWithFactory(engine.GetRetrieval())
	if err != nil {
		t.Fatal(err)
	}
	expand, expandFactory, err := seahorse.NewExpandToolWithFactory(engine.GetRetrieval())
	if err != nil {
		t.Fatal(err)
	}
	stage := stageSeahorseCatalog([]seahorseAgentCandidate{{
		id: "main", registry: registry,
		grep: grep, grepFactory: grepFactory,
		expand: expand, expandFactory: expandFactory,
	}})
	admissions, err := tools.InstallFactoryBackedTransaction(stage.batches)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifySeahorseAdmissions(stage, admissions); err != nil {
		t.Fatalf("valid admissions rejected: %v", err)
	}
	tests := []struct {
		name       string
		stage      stagedSeahorseCatalog
		admissions []tools.FactoryBackedAdmission
	}{
		{name: "count", stage: stage, admissions: admissions[:1]},
		{name: "identity", stage: stage, admissions: func() []tools.FactoryBackedAdmission {
			cloned := append([]tools.FactoryBackedAdmission(nil), admissions...)
			cloned[0].Name = "wrong"
			return cloned
		}()},
		{name: "replacement", stage: stage, admissions: func() []tools.FactoryBackedAdmission {
			cloned := append([]tools.FactoryBackedAdmission(nil), admissions...)
			cloned[0].Replaced = true
			return cloned
		}()},
		{name: "denied published", stage: stage, admissions: func() []tools.FactoryBackedAdmission {
			cloned := append([]tools.FactoryBackedAdmission(nil), admissions...)
			cloned[0].Admitted = false
			return cloned
		}()},
		{name: "wrong exact pointer", stage: func() stagedSeahorseCatalog {
			cloned := stage
			cloned.sidecars = append([]seahorseInstallSidecar(nil), stage.sidecars...)
			cloned.sidecars[0].live = &seahorseCatalogLegacyTool{name: seahorse.ShortGrepToolName}
			return cloned
		}(), admissions: admissions},
		{name: "version", stage: func() stagedSeahorseCatalog {
			cloned := stage
			cloned.beforeVersions = append([]uint64(nil), stage.beforeVersions...)
			cloned.beforeVersions[0]++
			return cloned
		}(), admissions: admissions},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if projectionErr := verifySeahorseAdmissions(
				test.stage,
				test.admissions,
			); projectionErr == nil {
				t.Fatal("malformed admission projection was accepted")
			}
		})
	}
}

func TestSeahorseContextManagerCloseIsDeterministicPanicSafeAndIdempotent(t *testing.T) {
	engines := make(map[string]*seahorse.Engine)
	engineName := make(map[*seahorse.Engine]string)
	for _, id := range []string{"gamma", "alpha", "beta"} {
		engine, err := seahorse.NewOfflineEngine(seahorse.OfflineConfig{
			DatabasePath: filepath.Join(t.TempDir(), id+".db"),
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		engines[id] = engine
		engineName[engine] = id
	}
	calls := make([]string, 0, 3)
	manager := &seahorseContextManager{
		engine: engines["alpha"], engines: engines,
		engineIDs: []string{"gamma", "alpha", "beta"},
		closeEngine: func(engine *seahorse.Engine) error {
			id := engineName[engine]
			calls = append(calls, id)
			defer func() { _ = engine.Close() }()
			switch id {
			case "alpha":
				return errors.New("alpha close error")
			case "beta":
				panic("beta close panic")
			default:
				return nil
			}
		},
	}
	err := manager.Close()
	if !slices.Equal(calls, []string{"alpha", "beta", "gamma"}) {
		t.Fatalf("close order = %#v", calls)
	}
	if err == nil || !strings.Contains(err.Error(), "alpha close error") ||
		!strings.Contains(err.Error(), "beta close panic") ||
		strings.Index(err.Error(), "alpha close error") > strings.Index(err.Error(), "beta close panic") {
		t.Fatalf("deterministic joined close error = %v", err)
	}
	if secondErr := manager.Close(); secondErr != nil || len(calls) != 3 {
		t.Fatalf("second Close = %v, calls=%#v", secondErr, calls)
	}
	var nilManager *seahorseContextManager
	if nilErr := nilManager.Close(); nilErr != nil {
		t.Fatalf("nil Close = %v", nilErr)
	}
	if nilEngineErr := closeSeahorseEngine(nil, nil); nilEngineErr != nil {
		t.Fatalf("nil engine Close = %v", nilEngineErr)
	}
	defaultEngine, defaultErr := seahorse.NewOfflineEngine(seahorse.OfflineConfig{
		DatabasePath: filepath.Join(t.TempDir(), "default-close.db"),
	}, nil)
	if defaultErr != nil {
		t.Fatal(defaultErr)
	}
	if defaultErr = closeSeahorseEngine(nil, defaultEngine); defaultErr != nil {
		t.Fatalf("default engine Close = %v", defaultErr)
	}
}

func TestSeahorseCatalogOwnerProductsAndAgentStoresAreIsolated(t *testing.T) {
	fixture := newSeahorseCatalogFixture(t,
		seahorseCatalogAgentSpec{id: "alpha", defaultID: true},
		seahorseCatalogAgentSpec{id: "beta"},
	)
	managerRaw, err := newSeahorseContextManagerWithDependencies(
		context.Background(), nil, fixture.loop,
		defaultSeahorseContextDependencies(),
	)
	if err != nil {
		t.Fatal(err)
	}
	manager := managerRaw.(*seahorseContextManager)
	t.Cleanup(func() { _ = manager.Close() })
	if manager.engines["alpha"] == manager.engines["beta"] ||
		manager.engines["alpha"].GetRetrieval() == manager.engines["beta"].GetRetrieval() {
		t.Fatal("agents share a Seahorse engine or retrieval")
	}
	for _, agent := range fixture.agents {
		if _, statErr := os.Lstat(
			filepath.Join(agent.Workspace, "sessions", "seahorse.db"),
		); !errors.Is(
			statErr,
			os.ErrNotExist,
		) {
			t.Fatalf("agent %q reconstructed a Seahorse database path: %v", agent.ID, statErr)
		}
	}
	_, err = manager.engines["alpha"].Ingest(
		context.Background(),
		"alpha-session",
		[]seahorse.Message{{
			Role: "user", Content: "alpha-isolation-canary", TokenCount: 3,
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	alphaGrep, _ := fixture.agents["alpha"].Tools.GetRegistered(seahorse.ShortGrepToolName)
	betaGrep, _ := fixture.agents["beta"].Tools.GetRegistered(seahorse.ShortGrepToolName)
	args := map[string]any{
		"pattern": "%alpha-isolation-canary%", "scope": "message",
		"all_conversations": true,
	}
	alphaResult := alphaGrep.Execute(context.Background(), args)
	betaResult := betaGrep.Execute(context.Background(), args)
	if alphaResult == nil || alphaResult.IsError ||
		!strings.Contains(alphaResult.ForLLM, "alpha-isolation-canary") {
		t.Fatalf("alpha grep result = %#v", alphaResult)
	}
	if betaResult == nil || betaResult.IsError ||
		strings.Contains(betaResult.ForLLM, "alpha-isolation-canary") {
		t.Fatalf("beta grep result = %#v", betaResult)
	}
	child, err := fixture.agents["alpha"].Tools.InstantiateForOwnerSelection(
		tools.ToolOwner{Scope: tools.ToolOwnerScopeTurn, TurnID: "seahorse-owner"},
		[]string{seahorse.ShortGrepToolName, seahorse.ShortExpandToolName},
	)
	if err != nil {
		t.Fatal(err)
	}
	childGrep, _ := child.GetRegistered(seahorse.ShortGrepToolName)
	if childGrep == alphaGrep || childGrep == betaGrep {
		t.Fatal("owner construction reused a compatibility wrapper")
	}
	childResult := childGrep.Execute(context.Background(), args)
	if childResult == nil || childResult.IsError ||
		!strings.Contains(childResult.ForLLM, "alpha-isolation-canary") {
		t.Fatalf("child grep result = %#v", childResult)
	}
	if err := child.Close(); err != nil {
		t.Fatal(err)
	}
	if sourceResult := alphaGrep.Execute(context.Background(), args); sourceResult == nil || sourceResult.IsError ||
		!strings.Contains(sourceResult.ForLLM, "alpha-isolation-canary") {
		t.Fatalf("source after child Close = %#v", sourceResult)
	}
}
