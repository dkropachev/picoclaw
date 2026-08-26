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
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/skills"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type recursionCatalogFixture struct {
	loop     *AgentLoop
	cfg      *config.Config
	registry *AgentRegistry
	agents   map[string]*AgentInstance
	provider providers.LLMProvider
}

type recursionCatalogInstallRegistry struct {
	calls         atomic.Int64
	firstEntered  chan struct{}
	releaseFirst  chan struct{}
	secondEntered chan struct{}
	firstOnce     sync.Once
	secondOnce    sync.Once
}

type recursionCatalogLegacyTool struct{ name string }

func (tool *recursionCatalogLegacyTool) Name() string { return tool.name }

func (*recursionCatalogLegacyTool) Description() string { return "collision fixture" }

func (*recursionCatalogLegacyTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (*recursionCatalogLegacyTool) Execute(
	context.Context,
	map[string]any,
) *tools.ToolResult {
	return tools.SilentResult("collision fixture")
}

func (*recursionCatalogInstallRegistry) Name() string { return "clawhub" }

func (*recursionCatalogInstallRegistry) ResolveInstallDirName(target string) (string, error) {
	return target, nil
}

func (*recursionCatalogInstallRegistry) SkillURL(slug, _ string) string { return slug }

func (*recursionCatalogInstallRegistry) Search(
	context.Context,
	string,
	int,
) ([]skills.SearchResult, error) {
	return nil, nil
}

func (*recursionCatalogInstallRegistry) GetSkillMeta(
	context.Context,
	string,
) (*skills.SkillMeta, error) {
	return nil, nil
}

func (registry *recursionCatalogInstallRegistry) DownloadAndInstall(
	_ context.Context,
	slug string,
	_ string,
	targetDir string,
) (*skills.InstallResult, error) {
	call := registry.calls.Add(1)
	if call == 1 {
		registry.firstOnce.Do(func() { close(registry.firstEntered) })
		<-registry.releaseFirst
	}
	if call == 2 {
		registry.secondOnce.Do(func() { close(registry.secondEntered) })
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return nil, err
	}
	markdown := fmt.Sprintf(
		"---\nname: %s\ndescription: Catalog install\n---\n# Catalog install\n",
		slug,
	)
	if err := os.WriteFile(filepath.Join(targetDir, "SKILL.md"), []byte(markdown), 0o600); err != nil {
		return nil, err
	}
	return &skills.InstallResult{Version: "catalog-test"}, nil
}

func newRecursionCatalogFixture(
	t *testing.T,
	agentIDs ...string,
) *recursionCatalogFixture {
	t.Helper()
	if len(agentIDs) == 0 {
		agentIDs = []string{routing.DefaultAgentID}
	}
	cfg := config.DefaultConfig()
	cfg.Tools.Spawn.Enabled = false
	cfg.Tools.SpawnStatus.Enabled = false
	cfg.Tools.Subagent.Enabled = false
	cfg.Tools.Skills.Enabled = false
	cfg.Tools.InstallSkill.Enabled = false
	cfg.Agents.List = make([]config.AgentConfig, 0, len(agentIDs))
	agents := make(map[string]*AgentInstance, len(agentIDs))
	for index, agentID := range agentIDs {
		workspace := filepath.Join(t.TempDir(), agentID)
		if err := os.MkdirAll(workspace, 0o755); err != nil {
			t.Fatal(err)
		}
		cfg.Agents.List = append(cfg.Agents.List, config.AgentConfig{
			ID: agentID, Default: index == 0, Workspace: workspace,
		})
		agents[agentID] = &AgentInstance{
			ID: agentID, Workspace: workspace,
			Model:     "model-" + agentID,
			Fallbacks: []string{"fallback-" + agentID},
			MaxTokens: 4096, Temperature: 0.25,
			Tools:     tools.NewToolRegistry(),
			Subagents: &config.SubagentsConfig{AllowAgents: []string{"*"}},
		}
	}
	registry := &AgentRegistry{
		cfg: cfg, agents: agents, resolver: routing.NewRouteResolver(cfg),
	}
	loop := &AgentLoop{cfg: cfg, registry: registry}
	provider := &simpleMockProvider{response: "done"}
	t.Cleanup(registry.Close)
	return &recursionCatalogFixture{
		loop: loop, cfg: cfg, registry: registry, agents: agents, provider: provider,
	}
}

func (fixture *recursionCatalogFixture) prepare(
	t *testing.T,
	agentID string,
	dependencies recursionCatalogDependencies,
) recursionCatalogCandidate {
	t.Helper()
	agent := fixture.agents[agentID]
	candidate, err := prepareRecursionCatalogCandidate(
		fixture.loop,
		fixture.cfg,
		fixture.registry,
		fixture.provider,
		agent,
		agentID,
		nil,
		nil,
		dependencies,
	)
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}

func recursionCandidateNames(candidate recursionCatalogCandidate) []string {
	names := make([]string, 0, len(candidate.installs))
	for _, install := range candidate.installs {
		names = append(names, install.Live.Name())
	}
	return names
}

func waitForRecursionReloadPause(
	t *testing.T,
	loop *AgentLoop,
	reloadDone <-chan error,
) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		loop.runtimeGateMu.Lock()
		paused := loop.runtimeGatePaused
		active := loop.runtimeGateActive
		loop.runtimeGateMu.Unlock()
		if paused && active > 0 {
			return
		}
		select {
		case err := <-reloadDone:
			t.Fatalf("reload escaped retained recursion work: %v", err)
		case <-deadline.C:
			t.Fatalf(
				"reload pause state = paused %v, active %d; want paused with retained work",
				paused,
				active,
			)
		case <-ticker.C:
		}
	}
}

func TestRecursionCatalogExactEnablementMatrix(t *testing.T) {
	tests := []struct {
		name     string
		agentIDs []string
		spawn    bool
		status   bool
		subagent bool
		want     []string
	}{
		{name: "none", agentIDs: []string{"main"}},
		{name: "subagent alone", agentIDs: []string{"main"}, subagent: true},
		{name: "spawn without subagent", agentIDs: []string{"main"}, spawn: true},
		{name: "status without subagent", agentIDs: []string{"main"}, status: true},
		{
			name: "spawn and status without subagent", agentIDs: []string{"main"},
			spawn: true, status: true,
		},
		{
			name: "status with subagent", agentIDs: []string{"main"},
			status: true, subagent: true, want: []string{"spawn_status"},
		},
		{
			name: "spawn with subagent", agentIDs: []string{"main"},
			spawn: true, subagent: true, want: []string{"spawn", "subagent"},
		},
		{
			name: "complete bundle", agentIDs: []string{"main"},
			spawn: true, status: true, subagent: true,
			want: []string{"spawn", "subagent", "spawn_status"},
		},
		{
			name: "delegate only multi agent", agentIDs: []string{"alpha", "beta"},
			want: []string{"delegate"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecursionCatalogFixture(t, test.agentIDs...)
			fixture.cfg.Tools.Spawn.Enabled = test.spawn
			fixture.cfg.Tools.SpawnStatus.Enabled = test.status
			fixture.cfg.Tools.Subagent.Enabled = test.subagent
			candidate := fixture.prepare(
				t,
				test.agentIDs[0],
				defaultRecursionCatalogDependencies(),
			)
			if got := recursionCandidateNames(candidate); !slices.Equal(got, test.want) {
				t.Fatalf("candidate names = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRecursionCatalogOwnerBundlesAreFreshAndSharedWithinOwner(t *testing.T) {
	fixture := newRecursionCatalogFixture(t, "main")
	fixture.cfg.Tools.Spawn.Enabled = true
	fixture.cfg.Tools.SpawnStatus.Enabled = true
	fixture.cfg.Tools.Subagent.Enabled = true
	constructed := make([]*tools.SubagentManager, 0, 3)
	var constructedMu sync.Mutex
	dependencies := defaultRecursionCatalogDependencies()
	dependencies.newManager = func(
		provider providers.LLMProvider,
		model string,
		workspace string,
	) *tools.SubagentManager {
		manager := tools.NewSubagentManager(provider, model, workspace)
		constructedMu.Lock()
		constructed = append(constructed, manager)
		constructedMu.Unlock()
		return manager
	}
	candidate := fixture.prepare(t, "main", dependencies)
	if len(constructed) != 1 {
		t.Fatalf("root manager constructions = %d, want 1", len(constructed))
	}
	installErr := installRecursionCatalog(
		[]recursionCatalogCandidate{candidate},
		dependencies.install,
	)
	if installErr != nil {
		t.Fatal(installErr)
	}
	roots := []string{"spawn", "subagent", "spawn_status"}
	first, err := fixture.agents["main"].Tools.InstantiateForOwnerSelection(
		tools.ToolOwner{Scope: tools.ToolOwnerScopeTurn, TurnID: "owner-first"},
		roots,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if len(constructed) != 2 {
		t.Fatalf("first owner manager constructions = %d, want 2 total", len(constructed))
	}
	second, err := fixture.agents["main"].Tools.InstantiateForOwnerSelection(
		tools.ToolOwner{Scope: tools.ToolOwnerScopeTurn, TurnID: "owner-second"},
		roots,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if len(constructed) != 3 || constructed[0] == constructed[1] ||
		constructed[1] == constructed[2] || constructed[0] == constructed[2] {
		t.Fatalf("manager identities = %#v", constructed)
	}
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	for index, manager := range constructed[1:] {
		ack, spawnErr := manager.Spawn(
			canceledCtx,
			fmt.Sprintf("owner-%d task", index+1),
			"",
			"",
			"catalog",
			fmt.Sprintf("owner-%d", index+1),
			nil,
		)
		if spawnErr != nil || !strings.Contains(ack, "task_id=subagent-1") {
			t.Fatalf("owner %d tracked spawn = %q, %v", index+1, ack, spawnErr)
		}
	}
	for index, manager := range constructed[1:] {
		deadline := time.Now().Add(time.Second)
		for {
			task, ok := manager.GetTaskCopy("subagent-1")
			if ok && task.Status == "canceled" {
				if !strings.Contains(task.Task, fmt.Sprintf("owner-%d", index+1)) {
					t.Fatalf("owner %d task = %#v", index+1, task)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("owner %d tracked task did not cancel: %#v", index+1, task)
			}
			time.Sleep(time.Millisecond)
		}
	}
	if _, ok := constructed[0].GetTaskCopy("subagent-1"); ok {
		t.Fatal("strict owner task leaked into root manager")
	}
	for _, name := range roots {
		source, _ := fixture.agents["main"].Tools.GetRegistered(name)
		firstTool, _ := first.GetRegistered(name)
		secondTool, _ := second.GetRegistered(name)
		if source == firstTool || source == secondTool || firstTool == secondTool {
			t.Fatalf("tool %q wrapper identity was reused", name)
		}
		if !reflect.DeepEqual(source.Parameters(), firstTool.Parameters()) ||
			!reflect.DeepEqual(source.Parameters(), secondTool.Parameters()) {
			t.Fatalf("tool %q parameters changed across owners", name)
		}
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if !fixture.agents["main"].Tools.HasRegistered("spawn") ||
		!second.HasRegistered("spawn") {
		t.Fatal("closing one owner changed source or sibling recursion tools")
	}
}

func TestRecursionCatalogDelegateDoesNotConstructManagerService(t *testing.T) {
	fixture := newRecursionCatalogFixture(t, "alpha", "beta")
	dependencies := defaultRecursionCatalogDependencies()
	managerConstructions := 0
	dependencies.newManager = func(
		providers.LLMProvider,
		string,
		string,
	) *tools.SubagentManager {
		managerConstructions++
		return nil
	}
	candidate := fixture.prepare(t, "alpha", dependencies)
	if got := recursionCandidateNames(candidate); !slices.Equal(got, []string{"delegate"}) {
		t.Fatalf("delegate-only candidate names = %v", got)
	}
	if managerConstructions != 0 {
		t.Fatalf("delegate root constructed %d managers", managerConstructions)
	}
	if err := installRecursionCatalog(
		[]recursionCatalogCandidate{candidate},
		dependencies.install,
	); err != nil {
		t.Fatal(err)
	}
	owned, err := fixture.agents["alpha"].Tools.InstantiateForOwnerSelection(
		tools.ToolOwner{Scope: tools.ToolOwnerScopeTurn, TurnID: "delegate-owner"},
		[]string{"delegate"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer owned.Close()
	if managerConstructions != 0 {
		t.Fatalf("delegate owner constructed %d managers", managerConstructions)
	}
	source, _ := fixture.agents["alpha"].Tools.GetRegistered("delegate")
	product, _ := owned.GetRegistered("delegate")
	if source == nil || product == nil || source == product {
		t.Fatalf("delegate wrappers = %T/%T", source, product)
	}
}

func TestRecursionCatalogAuthorizationUsesSourceAgentNotToolOwner(t *testing.T) {
	fixture := newRecursionCatalogFixture(t, "alpha", "beta", "gamma")
	fixture.agents["alpha"].Subagents = &config.SubagentsConfig{
		AllowAgents: []string{"beta"},
	}
	dependencies := defaultRecursionCatalogDependencies()
	candidate := fixture.prepare(t, "alpha", dependencies)
	if err := installRecursionCatalog(
		[]recursionCatalogCandidate{candidate},
		dependencies.install,
	); err != nil {
		t.Fatal(err)
	}
	owned, err := fixture.agents["alpha"].Tools.InstantiateForOwnerSelection(
		tools.ToolOwner{
			Scope:   tools.ToolOwnerScopeAgent,
			AgentID: "beta",
		},
		[]string{"delegate"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer owned.Close()
	delegate, ok := owned.GetRegistered("delegate")
	if !ok {
		t.Fatal("owner delegate is unavailable")
	}
	result := delegate.Execute(context.Background(), map[string]any{
		"agent_id": "gamma",
		"task":     "must remain unauthorized by alpha",
	})
	if result == nil || !result.IsError ||
		!strings.Contains(result.ForLLM, `not allowed to delegate to agent "gamma"`) {
		t.Fatalf("source authorization result = %#v", result)
	}
}

func TestRecursionCatalogTraitsCapabilitiesAndAllowlistAdmissions(t *testing.T) {
	fixture := newRecursionCatalogFixture(t, "all", "spawn-only", "none")
	fixture.cfg.Tools.Spawn.Enabled = true
	fixture.cfg.Tools.SpawnStatus.Enabled = true
	fixture.cfg.Tools.Subagent.Enabled = true
	fixture.agents["spawn-only"].Tools.SetAllowlist([]string{"spawn"})
	fixture.agents["none"].Tools.SetAllowlist([]string{})
	dependencies := defaultRecursionCatalogDependencies()
	candidates := make([]recursionCatalogCandidate, 0, 3)
	for _, agentID := range []string{"none", "all", "spawn-only"} {
		candidates = append(candidates, fixture.prepare(t, agentID, dependencies))
	}
	if err := installRecursionCatalog(candidates, dependencies.install); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"all":        {"delegate", "spawn", "spawn_status", "subagent"},
		"spawn-only": {"spawn"},
		"none":       {},
	}
	mutationTraits := tools.ToolTraits{
		Risk: tools.ToolRiskProcess, Parallel: tools.ToolParallelSerialized,
		Idempotency: tools.ToolIdempotencyNonIdempotent,
		Sharing:     tools.ToolSharingPerOwner,
	}
	statusTraits := tools.ToolTraits{
		Risk: tools.ToolRiskReadOnly, Parallel: tools.ToolParallelSafe,
		Idempotency: tools.ToolIdempotencyIdempotent,
		Sharing:     tools.ToolSharingPerOwner,
	}
	for agentID, names := range want {
		agent := fixture.agents[agentID]
		actual := make([]string, 0)
		for _, capability := range agent.Tools.InstantiationCapabilities() {
			if capability.FactoryBacked {
				actual = append(actual, capability.Name)
			}
		}
		slices.Sort(actual)
		if !slices.Equal(actual, names) {
			t.Fatalf("agent %q capabilities = %v, want %v", agentID, actual, names)
		}
		for _, name := range names {
			traits, ok := agent.Tools.Traits(name)
			wantTraits := mutationTraits
			if name == "spawn_status" {
				wantTraits = statusTraits
			}
			if !ok || traits != wantTraits {
				t.Fatalf("agent %q traits %q = %#v, %t", agentID, name, traits, ok)
			}
		}
	}
}

func TestRecursionCatalogStagesSortedFixedInstallOrderAndTraits(t *testing.T) {
	fixture := newRecursionCatalogFixture(t, "gamma", "alpha", "beta")
	fixture.cfg.Tools.Spawn.Enabled = true
	fixture.cfg.Tools.SpawnStatus.Enabled = true
	fixture.cfg.Tools.Subagent.Enabled = true
	fixture.cfg.Tools.Skills.Enabled = true
	fixture.cfg.Tools.InstallSkill.Enabled = true
	dependencies := defaultRecursionCatalogDependencies()
	registryManager := skills.NewRegistryManager()
	candidates := make([]recursionCatalogCandidate, 0, 3)
	for _, agentID := range []string{"gamma", "beta", "alpha"} {
		candidate, err := prepareRecursionCatalogCandidate(
			fixture.loop,
			fixture.cfg,
			fixture.registry,
			fixture.provider,
			fixture.agents[agentID],
			agentID,
			registryManager,
			&sync.Mutex{},
			dependencies,
		)
		if err != nil {
			t.Fatal(err)
		}
		candidates = append(candidates, candidate)
	}
	stage, err := stageRecursionCatalog(candidates)
	if err != nil {
		t.Fatal(err)
	}
	if len(stage.batches) != 3 {
		t.Fatalf("batch count = %d", len(stage.batches))
	}
	expectedAgents := []string{"alpha", "beta", "gamma"}
	expectedNames := []string{
		"install_skill", "spawn", "subagent", "spawn_status", "delegate",
	}
	for batchIndex, batch := range stage.batches {
		agentID := expectedAgents[batchIndex]
		if batch.Registry != fixture.agents[agentID].Tools {
			t.Fatalf("batch %d registry is not agent %q", batchIndex, agentID)
		}
		actualNames := make([]string, 0, len(batch.Installs))
		for _, install := range batch.Installs {
			actualNames = append(actualNames, install.Live.Name())
			if install.Expected != nil || install.Hidden {
				t.Fatalf("install %q expected=%T hidden=%t", install.Live.Name(), install.Expected, install.Hidden)
			}
			traits := install.Factory.Traits()
			switch install.Live.Name() {
			case "install_skill":
				if traits.Risk != tools.ToolRiskExternalWrite ||
					traits.Parallel != tools.ToolParallelSerialized ||
					traits.Idempotency != tools.ToolIdempotencyNonIdempotent {
					t.Fatalf("install_skill traits = %#v", traits)
				}
			case "spawn_status":
				if traits.Risk != tools.ToolRiskReadOnly ||
					traits.Parallel != tools.ToolParallelSafe ||
					traits.Idempotency != tools.ToolIdempotencyIdempotent {
					t.Fatalf("spawn_status traits = %#v", traits)
				}
			default:
				if traits.Risk != tools.ToolRiskProcess {
					t.Fatalf("%s risk = %q", install.Live.Name(), traits.Risk)
				}
			}
			if traits.Sharing != tools.ToolSharingPerOwner {
				t.Fatalf("%s sharing = %q", install.Live.Name(), traits.Sharing)
			}
		}
		if !slices.Equal(actualNames, expectedNames) {
			t.Fatalf("batch %q names = %v", agentID, actualNames)
		}
	}
}

func TestRecursionCatalogEnabledInstallRequiresConstructionDependencies(t *testing.T) {
	fixture := newRecursionCatalogFixture(t, "main")
	fixture.cfg.Tools.Skills.Enabled = true
	fixture.cfg.Tools.InstallSkill.Enabled = true
	_, err := prepareRecursionCatalogCandidate(
		fixture.loop,
		fixture.cfg,
		fixture.registry,
		fixture.provider,
		fixture.agents["main"],
		"main",
		nil,
		&sync.Mutex{},
		defaultRecursionCatalogDependencies(),
	)
	if err == nil || !strings.Contains(err.Error(), "registry manager is nil") {
		t.Fatalf("missing install dependency error = %v", err)
	}
	_, err = prepareRecursionCatalogCandidate(
		fixture.loop,
		fixture.cfg,
		fixture.registry,
		fixture.provider,
		fixture.agents["main"],
		"main",
		skills.NewRegistryManager(),
		nil,
		defaultRecursionCatalogDependencies(),
	)
	if err == nil || !strings.Contains(err.Error(), "workspace lock is nil") {
		t.Fatalf("missing install lock error = %v", err)
	}
}

func TestRecursionCatalogCandidateAndBundleValidationFailures(t *testing.T) {
	t.Run("incomplete input", func(t *testing.T) {
		fixture := newRecursionCatalogFixture(t, "main")
		_, err := prepareRecursionCatalogCandidate(
			nil,
			fixture.cfg,
			fixture.registry,
			fixture.provider,
			fixture.agents["main"],
			"main",
			nil,
			nil,
			defaultRecursionCatalogDependencies(),
		)
		if err == nil || !strings.Contains(err.Error(), "input is incomplete") {
			t.Fatalf("incomplete input error = %v", err)
		}
	})
	t.Run("inexact identity", func(t *testing.T) {
		fixture := newRecursionCatalogFixture(t, "main")
		_, err := prepareRecursionCatalogCandidate(
			fixture.loop,
			fixture.cfg,
			fixture.registry,
			fixture.provider,
			fixture.agents["main"],
			" main",
			nil,
			nil,
			defaultRecursionCatalogDependencies(),
		)
		if err == nil || !strings.Contains(err.Error(), "identity") {
			t.Fatalf("identity error = %v", err)
		}
	})
	t.Run("nil registry", func(t *testing.T) {
		fixture := newRecursionCatalogFixture(t, "main")
		fixture.agents["main"].Tools = nil
		_, err := prepareRecursionCatalogCandidate(
			fixture.loop,
			fixture.cfg,
			fixture.registry,
			fixture.provider,
			fixture.agents["main"],
			"main",
			nil,
			nil,
			defaultRecursionCatalogDependencies(),
		)
		if err == nil || !strings.Contains(err.Error(), "tool registry is nil") {
			t.Fatalf("nil registry error = %v", err)
		}
	})
	t.Run("owned registry", func(t *testing.T) {
		fixture := newRecursionCatalogFixture(t, "main")
		owned, err := tools.NewOwnedToolRegistry(tools.ToolOwner{
			Scope: tools.ToolOwnerScopeRegistry,
		})
		if err != nil {
			t.Fatal(err)
		}
		fixture.agents["main"].Tools = owned
		_, err = prepareRecursionCatalogCandidate(
			fixture.loop,
			fixture.cfg,
			fixture.registry,
			fixture.provider,
			fixture.agents["main"],
			"main",
			nil,
			nil,
			defaultRecursionCatalogDependencies(),
		)
		if err == nil || !strings.Contains(err.Error(), "compatibility registry") {
			t.Fatalf("owned registry error = %v", err)
		}
	})
	t.Run("nil installer", func(t *testing.T) {
		fixture := newRecursionCatalogFixture(t, "main")
		dependencies := defaultRecursionCatalogDependencies()
		dependencies.install = nil
		_, err := prepareRecursionCatalogCandidate(
			fixture.loop,
			fixture.cfg,
			fixture.registry,
			fixture.provider,
			fixture.agents["main"],
			"main",
			nil,
			nil,
			dependencies,
		)
		if err == nil || !strings.Contains(err.Error(), "dependencies are incomplete") {
			t.Fatalf("nil installer error = %v", err)
		}
	})
	t.Run("nil manager constructor", func(t *testing.T) {
		fixture := newRecursionCatalogFixture(t, "main")
		fixture.cfg.Tools.Spawn.Enabled = true
		fixture.cfg.Tools.Subagent.Enabled = true
		dependencies := defaultRecursionCatalogDependencies()
		dependencies.newManager = nil
		_, err := prepareRecursionCatalogCandidate(
			fixture.loop,
			fixture.cfg,
			fixture.registry,
			fixture.provider,
			fixture.agents["main"],
			"main",
			nil,
			nil,
			dependencies,
		)
		if err == nil || !strings.Contains(err.Error(), "manager constructor is nil") {
			t.Fatalf("nil manager constructor error = %v", err)
		}
	})
	t.Run("manager constructor returns nil", func(t *testing.T) {
		fixture := newRecursionCatalogFixture(t, "main")
		fixture.cfg.Tools.Spawn.Enabled = true
		fixture.cfg.Tools.Subagent.Enabled = true
		dependencies := defaultRecursionCatalogDependencies()
		dependencies.newManager = func(
			providers.LLMProvider,
			string,
			string,
		) *tools.SubagentManager {
			return nil
		}
		_, err := prepareRecursionCatalogCandidate(
			fixture.loop,
			fixture.cfg,
			fixture.registry,
			fixture.provider,
			fixture.agents["main"],
			"main",
			nil,
			nil,
			dependencies,
		)
		if err == nil || !strings.Contains(err.Error(), "returned nil") {
			t.Fatalf("nil manager result error = %v", err)
		}
	})
	if bundle, err := buildRecursionOwnerBundle(
		recursionCatalogSpec{},
		defaultRecursionCatalogDependencies(),
	); err == nil || bundle != nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("incomplete bundle = %#v, %v", bundle, err)
	}
	if bundle, err := recursionOwnerBundleForBuild(
		tools.ToolBuildContext{},
		recursionCatalogSpec{},
		defaultRecursionCatalogDependencies(),
	); err == nil || bundle != nil {
		t.Fatalf("inactive build context bundle = %#v, %v", bundle, err)
	}
}

func TestLegacyRecursionSpawnerPreservesCompatibilityInputs(t *testing.T) {
	fixture := newRecursionCatalogFixture(t, "alpha", "beta")
	fixture.loop.runtimeGateMu.Lock()
	fixture.loop.runtimeGateStopped = true
	fixture.loop.runtimeGateMu.Unlock()
	legacyTools := tools.NewToolRegistry()
	legacyTools.Register(&recursionCatalogLegacyTool{name: "fixture_tool"})
	spawn := legacyRecursionSpawner(recursionCatalogSpec{
		al:        fixture.loop,
		registry:  fixture.registry,
		provider:  fixture.provider,
		agentID:   "alpha",
		workspace: fixture.agents["alpha"].Workspace,
		model:     "model-alpha",
		fallbacks: []string{},
	})
	result, err := spawn(
		context.Background(),
		"compatibility task",
		"compatibility label",
		"beta",
		legacyTools,
		1234,
		0.75,
		true,
		true,
	)
	if result != nil || !errors.Is(err, errAgentRuntimeStopped) {
		t.Fatalf("legacy spawn result = %#v, %v", result, err)
	}
}

func TestRecursionCatalogSmallFailureBoundaries(t *testing.T) {
	if err := installRecursionCatalog(nil, nil); err == nil ||
		!strings.Contains(err.Error(), "installer is nil") {
		t.Fatalf("nil installer error = %v", err)
	}
	if err := installRecursionCatalog(
		[]recursionCatalogCandidate{{}},
		defaultRecursionCatalogDependencies().install,
	); err == nil || !strings.Contains(err.Error(), "candidate is incomplete") {
		t.Fatalf("invalid candidate error = %v", err)
	}
	fixture := newRecursionCatalogFixture(t, "alpha", "beta")
	fixture.agents["alpha"].Subagents = &config.SubagentsConfig{
		AllowAgents: []string{"*"},
	}
	bundle := &recursionOwnerBundle{
		manager: tools.NewSubagentManager(
			fixture.provider,
			fixture.agents["alpha"].Model,
			fixture.agents["alpha"].Workspace,
		),
		spawner: NewSubTurnSpawner(fixture.loop),
	}
	spawn := buildSpawnTool(bundle, recursionCatalogSpec{
		registry: fixture.registry,
		agentID:  "alpha",
	})
	result := spawn.Execute(context.Background(), map[string]any{
		"task":     "must remain denied",
		"agent_id": "gamma",
	})
	if result == nil || !result.IsError ||
		!strings.Contains(result.ForLLM, "not allowed") {
		t.Fatalf("spawn authorization result = %#v", result)
	}
	if tasks := bundle.manager.ListTaskCopies(); len(tasks) != 0 {
		t.Fatalf("missing wildcard target created records: %#v", tasks)
	}
}

func TestRecursionCatalogStageAndAdmissionInvariantFailures(t *testing.T) {
	sharedRegistry := tools.NewToolRegistry()
	if _, err := stageRecursionCatalog([]recursionCatalogCandidate{
		{agentID: "alpha", registry: sharedRegistry},
		{agentID: "beta", registry: sharedRegistry},
	}); err == nil || !strings.Contains(err.Error(), "share one registry") {
		t.Fatalf("shared registry stage error = %v", err)
	}
	if _, err := stageRecursionCatalog([]recursionCatalogCandidate{
		{agentID: "alpha", registry: tools.NewToolRegistry()},
		{agentID: "alpha", registry: tools.NewToolRegistry()},
	}); err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("duplicate agent stage error = %v", err)
	}

	newStage := func() (stagedRecursionCatalog, *tools.ToolRegistry, tools.Tool) {
		registry := tools.NewToolRegistry()
		live := &recursionCatalogLegacyTool{name: "fixture"}
		return stagedRecursionCatalog{
			batches: []tools.FactoryBackedBatch{{Registry: registry}},
			sidecars: []recursionInstallSidecar{{
				batchIndex: 0, installIndex: 0, agentID: "alpha",
				name: "fixture", registry: registry, live: live,
			}},
			beforeVersions: []uint64{0},
		}, registry, live
	}
	t.Run("admission identity", func(t *testing.T) {
		stage, _, _ := newStage()
		err := verifyRecursionAdmissions(stage, []tools.FactoryBackedAdmission{{
			BatchIndex: 1, InstallIndex: 0, Name: "fixture",
		}})
		if err == nil || !strings.Contains(err.Error(), "identity") {
			t.Fatalf("admission identity error = %v", err)
		}
	})
	t.Run("admitted occupant", func(t *testing.T) {
		stage, _, _ := newStage()
		err := verifyRecursionAdmissions(stage, []tools.FactoryBackedAdmission{{
			BatchIndex: 0, InstallIndex: 0, Name: "fixture", Admitted: true,
		}})
		if err == nil || !strings.Contains(err.Error(), "did not publish") {
			t.Fatalf("missing admitted occupant error = %v", err)
		}
	})
	t.Run("denied occupant", func(t *testing.T) {
		stage, registry, live := newStage()
		registry.Register(live)
		err := verifyRecursionAdmissions(stage, []tools.FactoryBackedAdmission{{
			BatchIndex: 0, InstallIndex: 0, Name: "fixture",
		}})
		if err == nil || !strings.Contains(err.Error(), "denied") {
			t.Fatalf("published denied occupant error = %v", err)
		}
	})
	t.Run("version delta", func(t *testing.T) {
		stage, _, _ := newStage()
		stage.beforeVersions[0] = 1
		err := verifyRecursionAdmissions(stage, []tools.FactoryBackedAdmission{{
			BatchIndex: 0, InstallIndex: 0, Name: "fixture",
		}})
		if err == nil || !strings.Contains(err.Error(), "version") {
			t.Fatalf("version delta error = %v", err)
		}
	})
}

func TestRecursionCatalogSubTurnCompatibilityBoundaryHelpers(t *testing.T) {
	background := context.Background()
	if loop := AgentLoopFromContext(background); loop != nil {
		t.Fatalf("background AgentLoop = %p", loop)
	}
	if result, err := SpawnSubTurn(background, SubTurnConfig{}); result != nil ||
		err == nil || !strings.Contains(err.Error(), "AgentLoop not found") {
		t.Fatalf("missing loop SpawnSubTurn = %#v, %v", result, err)
	}
	fixture := newRecursionCatalogFixture(t, "main")
	loopContext := WithAgentLoop(background, fixture.loop)
	if loop := AgentLoopFromContext(loopContext); loop != fixture.loop {
		t.Fatalf("context AgentLoop = %p, want %p", loop, fixture.loop)
	}
	if result, err := SpawnSubTurn(loopContext, SubTurnConfig{}); result != nil ||
		err == nil || !strings.Contains(err.Error(), "parent turnState not found") {
		t.Fatalf("missing parent SpawnSubTurn = %#v, %v", result, err)
	}
	var nilSpawner *AgentLoopSpawner
	_, release, err := nilSpawner.PrepareAsyncSubTurn(background)
	if release == nil || err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("nil spawner preparation = release:%t error:%v", release != nil, err)
	}
	release()
	spawner := NewSubTurnSpawner(fixture.loop)
	missingParentResult, missingParentErr := spawner.SpawnSubTurn(
		background,
		tools.SubTurnConfig{},
	)
	if missingParentResult != nil || missingParentErr == nil ||
		!strings.Contains(missingParentErr.Error(), "parent turnState not found") {
		t.Fatalf("spawner missing parent = %#v, %v", missingParentResult, missingParentErr)
	}
	fixture.loop.runtimeGateMu.Lock()
	fixture.loop.runtimeGateStopped = true
	fixture.loop.runtimeGateMu.Unlock()
	parent := fixture.loop.newAdHocRootTurnState(loopContext)
	parent.agent = fixture.agents["main"]
	parentContext := withTurnState(loopContext, parent)
	result, err := SpawnSubTurn(
		parentContext,
		SubTurnConfig{Model: "model-main"},
	)
	if result != nil || !errors.Is(err, errAgentRuntimeStopped) {
		t.Fatalf("stopped runtime SpawnSubTurn = %#v, %v", result, err)
	}
}

func TestRecursionCatalogInitialAndPostStageCollisionRollBackEveryAgent(t *testing.T) {
	for _, mode := range []string{"initial", "post-stage"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newRecursionCatalogFixture(t, "alpha", "beta")
			fixture.cfg.Tools.Spawn.Enabled = true
			fixture.cfg.Tools.Subagent.Enabled = true
			dependencies := defaultRecursionCatalogDependencies()
			candidates := []recursionCatalogCandidate{
				fixture.prepare(t, "alpha", dependencies),
				fixture.prepare(t, "beta", dependencies),
			}
			interloper := &recursionCatalogLegacyTool{name: "spawn"}
			if mode == "initial" {
				fixture.agents["beta"].Tools.Register(interloper)
			}
			var staged []tools.FactoryBackedBatch
			installer := func(
				batches []tools.FactoryBackedBatch,
			) ([]tools.FactoryBackedAdmission, error) {
				staged = batches
				if mode == "post-stage" {
					fixture.agents["beta"].Tools.Register(interloper)
				}
				return tools.InstallFactoryBackedTransaction(batches)
			}
			if err := installRecursionCatalog(candidates, installer); err == nil {
				t.Fatal("collision unexpectedly committed")
			}
			if fixture.agents["alpha"].Tools.HasRegistered("spawn") ||
				fixture.agents["alpha"].Tools.HasRegistered("subagent") ||
				fixture.agents["alpha"].Tools.HasRegistered("delegate") {
				t.Fatal("collision partially published alpha recursion roots")
			}
			occupant, ok := fixture.agents["beta"].Tools.GetRegistered("spawn")
			if !ok || occupant != interloper {
				t.Fatalf("beta occupant = %T, %t", occupant, ok)
			}
			probe := tools.NewToolRegistry()
			if err := probe.RegisterFactoryBacked(
				staged[0].Installs[0].Live,
				staged[0].Installs[0].Factory,
			); err != nil {
				t.Fatalf("rollback leaked candidate reservation: %v", err)
			}
			_ = probe.Close()
		})
	}
}

func TestRecursionCatalogDeniedExistingOccupantIsUntouched(t *testing.T) {
	fixture := newRecursionCatalogFixture(t, "alpha", "beta")
	fixture.cfg.Tools.Spawn.Enabled = true
	fixture.cfg.Tools.Subagent.Enabled = true
	interloper := &recursionCatalogLegacyTool{name: "spawn"}
	fixture.agents["beta"].Tools.Register(interloper)
	fixture.agents["beta"].Tools.SetAllowlist([]string{"unrelated_tool"})
	dependencies := defaultRecursionCatalogDependencies()
	candidates := []recursionCatalogCandidate{
		fixture.prepare(t, "beta", dependencies),
		fixture.prepare(t, "alpha", dependencies),
	}
	if err := installRecursionCatalog(candidates, dependencies.install); err != nil {
		t.Fatal(err)
	}
	occupant, ok := fixture.agents["beta"].Tools.GetRegistered("spawn")
	if !ok || occupant != interloper {
		t.Fatalf("denied occupant = %T, %t", occupant, ok)
	}
	for _, name := range []string{"subagent", "delegate"} {
		if fixture.agents["beta"].Tools.HasRegistered(name) {
			t.Fatalf("denied agent published %q", name)
		}
	}
	for _, name := range []string{"spawn", "subagent", "delegate"} {
		if !fixture.agents["alpha"].Tools.HasRegistered(name) {
			t.Fatalf("allowed sibling did not publish %q", name)
		}
	}
}

func TestRecursionCatalogInstallerFailuresAndMalformedSuccess(t *testing.T) {
	fixture := newRecursionCatalogFixture(t, "alpha", "beta")
	fixture.cfg.Tools.Spawn.Enabled = true
	fixture.cfg.Tools.Subagent.Enabled = true
	dependencies := defaultRecursionCatalogDependencies()
	candidates := []recursionCatalogCandidate{
		fixture.prepare(t, "alpha", dependencies),
		fixture.prepare(t, "beta", dependencies),
	}
	if err := installRecursionCatalog(
		candidates,
		func([]tools.FactoryBackedBatch) ([]tools.FactoryBackedAdmission, error) {
			return nil, errors.New("injected installer failure")
		},
	); err == nil {
		t.Fatal("installer error was ignored")
	}
	for _, agent := range fixture.agents {
		if agent.Tools.HasRegistered("spawn") || agent.Tools.HasRegistered("subagent") ||
			agent.Tools.HasRegistered("delegate") {
			t.Fatal("installer error published recursion roots")
		}
	}
	panicErr := installRecursionCatalog(
		candidates,
		func([]tools.FactoryBackedBatch) ([]tools.FactoryBackedAdmission, error) {
			panic("injected installer panic")
		},
	)
	if panicErr == nil || !strings.Contains(panicErr.Error(), "installer panicked") {
		t.Fatalf("installer panic error = %v", panicErr)
	}
	for _, agent := range fixture.agents {
		if agent.Tools.HasRegistered("spawn") || agent.Tools.HasRegistered("subagent") ||
			agent.Tools.HasRegistered("delegate") {
			t.Fatal("installer panic published recursion roots")
		}
	}
	if err := installRecursionCatalog(
		candidates,
		func(batches []tools.FactoryBackedBatch) ([]tools.FactoryBackedAdmission, error) {
			admissions, err := tools.InstallFactoryBackedTransaction(batches)
			if err != nil {
				return nil, err
			}
			return admissions[:len(admissions)-1], nil
		},
	); err != nil {
		t.Fatalf("postcommit malformed admissions returned error: %v", err)
	}
	for _, agent := range fixture.agents {
		if !agent.Tools.HasRegistered("spawn") || !agent.Tools.HasRegistered("delegate") {
			t.Fatal("postcommit projection fault lost committed roots")
		}
	}
}

func TestWorkspaceInstallLockCoordinatorAliasesAndIsolation(t *testing.T) {
	coordinator := &workspaceInstallLockCoordinator{}
	workspace := t.TempDir()
	alias := filepath.Join(t.TempDir(), "workspace-alias")
	if err := os.Symlink(workspace, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	first, err := coordinator.lockFor(workspace)
	if err != nil {
		t.Fatal(err)
	}
	second, err := coordinator.lockFor(alias)
	if err != nil {
		t.Fatal(err)
	}
	third, err := coordinator.lockFor(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first == third {
		t.Fatalf("workspace lock identities = %p/%p/%p", first, second, third)
	}
	if lock, err := coordinator.lockFor(" "); err == nil || lock != nil {
		t.Fatalf("blank workspace lock = %p, %v", lock, err)
	}
	if lock, err := coordinator.lockFor("invalid\x00workspace"); err == nil || lock != nil {
		t.Fatalf("invalid workspace lock = %p, %v", lock, err)
	}
}

func TestRecursionCatalogInstallSkillRootAndOwnerShareWorkspaceLock(t *testing.T) {
	fixture := newRecursionCatalogFixture(t, "main")
	fixture.cfg.Tools.Skills.Enabled = true
	fixture.cfg.Tools.InstallSkill.Enabled = true
	installRegistry := &recursionCatalogInstallRegistry{
		firstEntered:  make(chan struct{}),
		releaseFirst:  make(chan struct{}),
		secondEntered: make(chan struct{}),
	}
	registryManager := skills.NewRegistryManager()
	registryManager.AddRegistry(installRegistry)
	dependencies := defaultRecursionCatalogDependencies()
	candidate, err := prepareRecursionCatalogCandidate(
		fixture.loop,
		fixture.cfg,
		fixture.registry,
		fixture.provider,
		fixture.agents["main"],
		"main",
		registryManager,
		&sync.Mutex{},
		dependencies,
	)
	if err != nil {
		t.Fatal(err)
	}
	installErr := installRecursionCatalog(
		[]recursionCatalogCandidate{candidate},
		dependencies.install,
	)
	if installErr != nil {
		t.Fatal(installErr)
	}
	owned, err := fixture.agents["main"].Tools.InstantiateForOwnerSelection(
		tools.ToolOwner{Scope: tools.ToolOwnerScopeTurn, TurnID: "install-owner"},
		[]string{"install_skill"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer owned.Close()
	rootTool, rootOK := fixture.agents["main"].Tools.GetRegistered("install_skill")
	ownerTool, ownerOK := owned.GetRegistered("install_skill")
	if !rootOK || !ownerOK || rootTool == ownerTool {
		t.Fatalf("install wrappers = %T/%T, %t/%t", rootTool, ownerTool, rootOK, ownerOK)
	}
	released := false
	defer func() {
		if !released {
			close(installRegistry.releaseFirst)
		}
	}()
	results := make(chan *tools.ToolResult, 2)
	go func() {
		results <- rootTool.Execute(context.Background(), map[string]any{
			"slug": "root-skill", "registry": "clawhub",
		})
	}()
	select {
	case <-installRegistry.firstEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("root install did not enter registry")
	}
	go func() {
		results <- ownerTool.Execute(context.Background(), map[string]any{
			"slug": "owner-skill", "registry": "clawhub",
		})
	}()
	select {
	case <-installRegistry.secondEntered:
		t.Fatal("owner install entered while root held the workspace lock")
	case <-time.After(100 * time.Millisecond):
	}
	close(installRegistry.releaseFirst)
	released = true
	for index := 0; index < 2; index++ {
		select {
		case result := <-results:
			if result == nil || result.IsError {
				t.Fatalf("install result %d = %#v", index, result)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("install result %d did not finish", index)
		}
	}
	if installRegistry.calls.Load() != 2 {
		t.Fatalf("install calls = %d", installRegistry.calls.Load())
	}
}

func TestProductionRecursionCatalogSupportsStrictRuntimeSelection(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	messageBus := bus.NewMessageBus()
	loop := newTestAgentLoopWithStrictModels(cfg, messageBus, &simpleMockProvider{response: "done"})
	t.Cleanup(func() {
		loop.Close()
		messageBus.Close()
	})
	agent := loop.GetRegistry().GetDefaultAgent()
	for _, name := range []string{"install_skill", "spawn", "subagent"} {
		if !toolCapabilityIsFactoryBacked(agent.Tools, name) {
			t.Fatalf("production recursion tool %q is not factory-backed", name)
		}
	}
	owned, err := agent.Tools.InstantiateForOwnerSelection(
		tools.ToolOwner{Scope: tools.ToolOwnerScopeTurn, TurnID: "production-recursion-owner"},
		[]string{"install_skill", "spawn", "subagent"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer owned.Close()
	for _, name := range []string{"install_skill", "spawn", "subagent"} {
		source, sourceOK := agent.Tools.GetRegistered(name)
		selected, selectedOK := owned.GetRegistered(name)
		if !sourceOK || !selectedOK || source == selected {
			t.Fatalf("strict recursion selection %q = %T/%T", name, source, selected)
		}
	}
}

func TestRecursionCatalogOwnerSpawnRetainsRuntimeAcrossReload(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Tools.SpawnStatus.Enabled = true
	messageBus := bus.NewMessageBus()
	providerA := &simpleMockProvider{response: "generation-a"}
	providerB := &simpleMockProvider{response: "generation-b"}
	loop := newTestAgentLoopWithStrictModels(cfg, messageBus, providerA)
	t.Cleanup(func() {
		loop.Close()
		messageBus.Close()
	})
	agent := loop.GetRegistry().GetDefaultAgent()
	owned, err := agent.Tools.InstantiateForOwnerSelection(
		tools.ToolOwner{Scope: tools.ToolOwnerScopeTurn, TurnID: "retained-spawn-owner"},
		[]string{"spawn", "spawn_status"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer owned.Close()
	spawn, ok := owned.GetRegistered("spawn")
	if !ok {
		t.Fatal("owner spawn is unavailable")
	}
	asyncSpawn, ok := spawn.(tools.AsyncExecutor)
	if !ok {
		t.Fatalf("owner spawn type = %T, want AsyncExecutor", spawn)
	}
	statusTool, ok := owned.GetRegistered("spawn_status")
	if !ok {
		t.Fatal("paired owner spawn_status is unavailable")
	}
	rootCtx, releaseRoot, err := loop.acquireRuntimeUse(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	parent := &turnState{
		ctx:            rootCtx,
		turnID:         "retained-spawn-parent",
		agent:          agent,
		session:        newEphemeralSession(nil),
		pendingResults: make(chan *tools.ToolResult, 4),
		concurrencySem: make(chan struct{}, 1),
		opts: processOptions{
			Dispatch: DispatchRequest{SessionKey: "retained-spawn-parent"},
		},
	}
	parent.concurrencySem <- struct{}{}
	semaphoreDrained := false
	defer func() {
		if semaphoreDrained {
			return
		}
		select {
		case <-parent.concurrencySem:
		default:
		}
	}()
	rootCtx = withTurnState(rootCtx, parent)
	rootCtx = WithAgentLoop(rootCtx, loop)
	parent.ctx = rootCtx
	callbackDone := make(chan struct{})
	result := asyncSpawn.ExecuteAsync(
		rootCtx,
		map[string]any{"task": "finish after admission opens"},
		func(context.Context, *tools.ToolResult) { close(callbackDone) },
	)
	if result == nil || result.IsError || !result.Async {
		releaseRoot()
		t.Fatalf("owner spawn acknowledgement = %#v", result)
	}
	if !strings.Contains(result.ForLLM, "task_id=subagent-1") {
		releaseRoot()
		t.Fatalf("owner spawn acknowledgement has no task ID: %#v", result)
	}
	runningStatus := statusTool.Execute(context.Background(), map[string]any{
		"task_id": "subagent-1",
	})
	if runningStatus == nil || runningStatus.IsError ||
		!strings.Contains(runningStatus.ForLLM, "status=running") {
		releaseRoot()
		t.Fatalf("owner tracked running status = %#v", runningStatus)
	}
	releaseRoot()
	reloadDone := make(chan error, 1)
	go func() {
		reloadDone <- loop.ReloadProviderAndConfig(context.Background(), providerB, cfg)
	}()
	waitForRecursionReloadPause(t, loop, reloadDone)
	select {
	case err := <-reloadDone:
		t.Fatalf("reload escaped retained catalog spawn: %v", err)
	default:
	}
	<-parent.concurrencySem
	semaphoreDrained = true
	select {
	case <-callbackDone:
	case <-time.After(2 * time.Second):
		t.Fatal("catalog spawn did not complete")
	}
	completedStatus := statusTool.Execute(context.Background(), map[string]any{
		"task_id": "subagent-1",
	})
	if completedStatus == nil || completedStatus.IsError ||
		!strings.Contains(completedStatus.ForLLM, "status=completed") {
		t.Fatalf("owner tracked completed status = %#v", completedStatus)
	}
	select {
	case err := <-reloadDone:
		if err != nil {
			t.Fatalf("reload after catalog spawn = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reload remained blocked after catalog spawn completed")
	}
}

func TestRecursionCatalogTrackedSpawnRecordsHardAbortAsCanceled(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Tools.SpawnStatus.Enabled = true
	messageBus := bus.NewMessageBus()
	provider := &subTurnBlockingProvider{started: make(chan struct{})}
	loop := newTestAgentLoopWithStrictModels(cfg, messageBus, provider)
	t.Cleanup(func() {
		loop.Close()
		messageBus.Close()
	})
	agent := loop.GetRegistry().GetDefaultAgent()
	owned, err := agent.Tools.InstantiateForOwnerSelection(
		tools.ToolOwner{Scope: tools.ToolOwnerScopeTurn, TurnID: "tracked-abort-owner"},
		[]string{"spawn", "spawn_status"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer owned.Close()
	spawnRaw, _ := owned.GetRegistered("spawn")
	spawn, ok := spawnRaw.(tools.AsyncExecutor)
	if !ok {
		t.Fatalf("tracked abort spawn type = %T", spawnRaw)
	}
	status, ok := owned.GetRegistered("spawn_status")
	if !ok {
		t.Fatal("tracked abort status tool is unavailable")
	}
	parent := &turnState{
		turnID: "tracked-abort-parent", agent: agent,
		session:        newEphemeralSession(nil),
		pendingResults: make(chan *tools.ToolResult, 2),
		concurrencySem: make(chan struct{}, 1),
		opts: processOptions{
			Dispatch: DispatchRequest{SessionKey: "tracked-abort-parent"},
		},
	}
	loop.prepareTurnState(parent)
	ctx := withTurnState(WithAgentLoop(context.Background(), loop), parent)
	callback := make(chan *tools.ToolResult, 1)
	ack := spawn.ExecuteAsync(
		ctx,
		map[string]any{"task": "block until hard abort"},
		func(_ context.Context, result *tools.ToolResult) { callback <- result },
	)
	if ack == nil || ack.IsError || !strings.Contains(ack.ForLLM, "task_id=subagent-1") {
		t.Fatalf("tracked abort acknowledgement = %#v", ack)
	}
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("tracked abort child provider did not start")
	}
	var childID string
	deadline := time.Now().Add(2 * time.Second)
	for childID == "" && time.Now().Before(deadline) {
		parent.mu.RLock()
		if len(parent.childTurnIDs) > 0 {
			childID = parent.childTurnIDs[0]
		}
		parent.mu.RUnlock()
		if childID == "" {
			time.Sleep(time.Millisecond)
		}
	}
	if childID == "" {
		t.Fatal("tracked abort child was not attached")
	}
	if err := loop.HardAbort(childID); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-callback:
		if result == nil || !result.IsError || !errors.Is(result.Err, context.Canceled) {
			t.Fatalf("tracked abort callback = %#v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tracked abort callback did not complete")
	}
	select {
	case pending := <-parent.pendingResults:
		if pending == nil || !pending.IsError {
			t.Fatalf("tracked abort pending result = %#v", pending)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tracked abort pending-result path changed before P007")
	}
	statusResult := status.Execute(context.Background(), map[string]any{
		"task_id": "subagent-1",
	})
	if statusResult == nil || statusResult.IsError ||
		!strings.Contains(statusResult.ForLLM, "status=canceled") {
		t.Fatalf("tracked abort status = %#v", statusResult)
	}
}

func TestRecursionCatalogReloadInstallerFailurePreservesCurrentGeneration(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	messageBus := bus.NewMessageBus()
	providerA := &simpleMockProvider{response: "generation-a"}
	providerB := &simpleMockProvider{response: "generation-b"}
	loop := newTestAgentLoopWithStrictModels(cfg, messageBus, providerA)
	t.Cleanup(func() {
		loop.Close()
		messageBus.Close()
	})
	oldRegistry := loop.GetRegistry()
	oldAgent := oldRegistry.GetDefaultAgent()
	var candidateRegistry *AgentRegistry
	var candidateAgent *AgentInstance
	loop.registryFactory = func(
		candidateConfig *config.Config,
		candidateProvider providers.LLMProvider,
	) *AgentRegistry {
		candidateRegistry = NewAgentRegistry(candidateConfig, candidateProvider)
		candidateAgent = candidateRegistry.GetDefaultAgent()
		return candidateRegistry
	}
	sentinel := errors.New("injected reload catalog failure")
	loop.recursionInstaller = func(
		[]tools.FactoryBackedBatch,
	) ([]tools.FactoryBackedAdmission, error) {
		return nil, sentinel
	}
	err := loop.ReloadProviderAndConfig(context.Background(), providerB, cfg)
	if err == nil || !errors.Is(err, sentinel) {
		t.Fatalf("reload error = %v", err)
	}
	if loop.GetRegistry() != oldRegistry ||
		loop.GetRegistry().GetDefaultAgent() != oldAgent ||
		oldAgent.Provider != providerA {
		t.Fatal("failed reload replaced the current registry or provider")
	}
	if candidateRegistry == nil || candidateAgent == nil {
		t.Fatal("reload candidate was not constructed")
	}
	if got := candidateAgent.Tools.List(); len(got) != 0 {
		t.Fatalf("closed candidate registry retained tools: %v", got)
	}
	if !oldAgent.Tools.HasRegistered("spawn") ||
		!oldAgent.Tools.HasRegistered("subagent") {
		t.Fatal("failed reload damaged current recursion catalog")
	}
}

func TestRecursionCatalogInitialInstallerFailureMarksEveryAgent(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.List = []config.AgentConfig{
		{ID: "alpha", Default: true, Workspace: filepath.Join(t.TempDir(), "alpha")},
		{ID: "beta", Workspace: filepath.Join(t.TempDir(), "beta")},
	}
	messageBus := bus.NewMessageBus()
	sentinel := errors.New("injected initial catalog failure")
	loop := newTestAgentLoopWithStrictModels(
		cfg,
		messageBus,
		&simpleMockProvider{response: "unused"},
		func(loop *AgentLoop) {
			loop.recursionInstaller = func(
				[]tools.FactoryBackedBatch,
			) ([]tools.FactoryBackedAdmission, error) {
				return nil, sentinel
			}
		},
	)
	t.Cleanup(func() {
		loop.Close()
		messageBus.Close()
	})
	for _, agentID := range []string{"alpha", "beta"} {
		agent, ok := loop.GetRegistry().GetAgent(agentID)
		if !ok || agent == nil {
			t.Fatalf("agent %q is unavailable", agentID)
		}
		if !errors.Is(agent.ConfigurationError, sentinel) {
			t.Fatalf("agent %q configuration error = %v", agentID, agent.ConfigurationError)
		}
		for _, name := range []string{
			"install_skill", "spawn", "subagent", "spawn_status", "delegate",
		} {
			if agent.Tools.HasRegistered(name) {
				t.Fatalf("failed initial catalog published %q for %q", name, agentID)
			}
		}
	}
}

func TestMarkRecursionCatalogConfigurationError(t *testing.T) {
	fixture := newRecursionCatalogFixture(t, "alpha", "beta")
	sentinel := errors.New("catalog failed")
	markRecursionCatalogConfigurationError(fixture.registry, sentinel)
	for _, agent := range fixture.agents {
		if !errors.Is(agent.ConfigurationError, sentinel) {
			t.Fatalf("agent %q configuration error = %v", agent.ID, agent.ConfigurationError)
		}
	}
	markRecursionCatalogConfigurationError(nil, sentinel)
	markRecursionCatalogConfigurationError(fixture.registry, nil)
}
