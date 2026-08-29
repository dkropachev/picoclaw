package agent

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type p015B3AReentrantCloseProvider struct {
	once    sync.Once
	closeFn func()
}

type p015B3ACountedProvider struct {
	name   string
	closes atomic.Int64
}

func (provider *p015B3ACountedProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{Content: provider.name}, nil
}

func (provider *p015B3ACountedProvider) Close() {
	provider.closes.Add(1)
}

func (*p015B3AReentrantCloseProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{Content: "unused"}, nil
}

func (provider *p015B3AReentrantCloseProvider) Close() {
	provider.once.Do(func() {
		if provider.closeFn != nil {
			provider.closeFn()
		}
	})
}

func TestP015B3AGatewayRollbackRetainsExactPolicy(t *testing.T) {
	cfgA := executionPolicyAgentConfig(t, "runtime-transaction-a")
	cfgB := executionPolicyAgentConfig(t, "runtime-transaction-b")
	executionA := executionPolicyAgentSnapshot(t, cfgA, "A")
	executionB := executionPolicyAgentSnapshot(t, cfgB, "B")
	diagnosticA := logger.NewDiagnosticPolicy(true, logger.DEBUG)
	diagnosticB := logger.NewDiagnosticPolicy(false, logger.DEBUG)
	messageBus := bus.NewMessageBus()
	loop := NewAgentLoopWithRuntimePolicies(
		cfgA,
		messageBus,
		&mockProvider{},
		executionA,
		diagnosticA,
	)
	t.Cleanup(func() {
		loop.Close()
		messageBus.Close()
	})

	identityA, err := loop.SnapshotRuntimeGenerationIdentity()
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := loop.BeginRuntimeReloadTransaction(identityA)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Close()

	retainedA, err := transaction.PublishRetainingPrevious(
		context.Background(),
		&mockProvider{},
		cfgB,
		executionB,
		diagnosticB,
	)
	if err != nil {
		t.Fatal(err)
	}
	executionCurrent, diagnosticCurrent, err := loop.RuntimePoliciesForGeneration(cfgB)
	if err != nil {
		t.Fatal(err)
	}
	if executionCurrent != executionB || diagnosticCurrent != diagnosticB {
		t.Fatal("forward publication tore the B runtime-policy tuple")
	}
	identityB, err := loop.SnapshotRuntimeGenerationIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if identityB.id == identityA.id {
		t.Fatal("forward publication reused A generation identity")
	}

	retainedB, err := transaction.Rollback(context.Background(), retainedA)
	if err != nil {
		t.Fatal(err)
	}
	executionCurrent, diagnosticCurrent, err = loop.RuntimePoliciesForGeneration(cfgA)
	if err != nil {
		t.Fatal(err)
	}
	if executionCurrent != executionA || diagnosticCurrent != diagnosticA {
		t.Fatal("rollback did not restore exact retained A policies")
	}
	restoredA, err := loop.SnapshotRuntimeGenerationIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if restoredA.id == identityA.id || restoredA.id == identityB.id {
		t.Fatal("rollback did not publish a fresh non-ABA A identity")
	}
	if err := transaction.CommitRetained(context.Background(), retainedB); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.Rollback(context.Background(), retainedA); err == nil ||
		(!strings.Contains(err.Error(), "consumed") &&
			!strings.Contains(err.Error(), "closed")) {
		t.Fatalf("consumed retained A rollback error = %v", err)
	}
}

func TestP015B3AGatewayConcurrentGenerationRejectsStaleSnapshot(t *testing.T) {
	cfgA := executionPolicyAgentConfig(t, "runtime-stale-a")
	cfgC := executionPolicyAgentConfig(t, "runtime-stale-c")
	executionA := executionPolicyAgentSnapshot(t, cfgA, "A")
	executionC := executionPolicyAgentSnapshot(t, cfgC, "C")
	diagnosticA := logger.NewDiagnosticPolicy(true, logger.DEBUG)
	diagnosticC := logger.NewDiagnosticPolicy(false, logger.DEBUG)
	messageBus := bus.NewMessageBus()
	loop := NewAgentLoopWithRuntimePolicies(
		cfgA,
		messageBus,
		&mockProvider{},
		executionA,
		diagnosticA,
	)
	t.Cleanup(func() {
		loop.Close()
		messageBus.Close()
	})

	staleA, err := loop.SnapshotRuntimeGenerationIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if reloadErr := loop.ReloadProviderAndConfigWithRuntimePolicies(
		context.Background(),
		&mockProvider{},
		cfgC,
		executionC,
		diagnosticC,
	); reloadErr != nil {
		t.Fatal(reloadErr)
	}
	if transaction, beginErr := loop.BeginRuntimeReloadTransaction(staleA); beginErr == nil {
		transaction.Close()
		t.Fatal("stale A snapshot began a transaction after C publication")
	}
	executionCurrent, diagnosticCurrent, err := loop.RuntimePoliciesForGeneration(cfgC)
	if err != nil {
		t.Fatal(err)
	}
	if executionCurrent != executionC || diagnosticCurrent != diagnosticC {
		t.Fatal("rejected stale transaction changed exact C")
	}
}

func TestP015B3ARuntimeGenerationIdentityExhaustionFailsBeforePublication(
	t *testing.T,
) {
	cfgA := executionPolicyAgentConfig(t, "runtime-exhaustion-a")
	cfgB := executionPolicyAgentConfig(t, "runtime-exhaustion-b")
	messageBus := bus.NewMessageBus()
	loop := NewAgentLoopWithRuntimePolicies(
		cfgA,
		messageBus,
		&mockProvider{},
		executionPolicyAgentSnapshot(t, cfgA, "A"),
		logger.NewDiagnosticPolicy(true, logger.DEBUG),
	)
	t.Cleanup(func() {
		loop.Close()
		messageBus.Close()
	})
	loop.mu.Lock()
	loop.runtimeGenerationID = ^uint64(0)
	registryA := loop.registry
	loop.mu.Unlock()

	if err := loop.ReloadProviderAndConfigWithRuntimePolicies(
		context.Background(),
		&mockProvider{},
		cfgB,
		executionPolicyAgentSnapshot(t, cfgB, "B"),
		logger.NewDiagnosticPolicy(false, logger.DEBUG),
	); err == nil || !strings.Contains(err.Error(), "identity exhausted") {
		t.Fatalf("identity exhaustion reload error = %v", err)
	}
	if loop.GetConfig() != cfgA || loop.GetRegistry() != registryA {
		t.Fatal("identity exhaustion published a replacement generation")
	}
}

func TestP015B3ARetainedProviderGenerationRestoresAndClosesCompleteSet(
	t *testing.T,
) {
	cfgA := executionPolicyAgentConfig(t, "provider-set-a")
	cfgB := executionPolicyAgentConfig(t, "provider-set-b")
	cfgC := executionPolicyAgentConfig(t, "provider-set-c")
	configureAgents := func(cfg *config.Config, suffix string) {
		t.Helper()
		cfg.Agents.List = []config.AgentConfig{
			{ID: "main", Default: true, Name: "main-" + suffix, Workspace: t.TempDir()},
			{ID: "worker", Name: "worker-" + suffix, Workspace: t.TempDir()},
		}
	}
	configureAgents(cfgA, "a")
	configureAgents(cfgB, "b")
	configureAgents(cfgC, "c")
	providerKeys := []string{
		"bootstrap",
		"main-primary", "main-light", "main-fallback", "main-image", "main-routed",
		"worker-primary", "worker-light", "worker-fallback", "worker-image", "worker-routed",
		"shared",
	}
	newProviderSet := func(prefix string) map[string]*p015B3ACountedProvider {
		result := make(map[string]*p015B3ACountedProvider, len(providerKeys))
		for _, key := range providerKeys {
			result[key] = &p015B3ACountedProvider{name: prefix + "-" + key}
		}
		return result
	}
	providersA := newProviderSet("a")
	providersB := newProviderSet("b")
	bootstrapC := &p015B3ACountedProvider{name: "c-bootstrap"}
	messageBus := bus.NewMessageBus()
	loop := NewAgentLoopWithRuntimePolicies(
		cfgA,
		messageBus,
		providersA["bootstrap"],
		executionPolicyAgentSnapshot(t, cfgA, "A"),
		logger.DiagnosticPolicy{},
	)
	t.Cleanup(func() {
		loop.Close()
		messageBus.Close()
	})
	addProviderBindings := func(registry *AgentRegistry, set map[string]*p015B3ACountedProvider) {
		t.Helper()
		agentCandidateProvidersMu.Lock()
		defer agentCandidateProvidersMu.Unlock()
		for _, agentID := range []string{"main", "worker"} {
			runtimeAgent, ok := registry.GetAgent(agentID)
			if !ok || runtimeAgent == nil {
				t.Fatalf("runtime agent %q is missing", agentID)
			}
			runtimeAgent.Provider = set[agentID+"-primary"]
			runtimeAgent.LightProvider = set[agentID+"-light"]
			runtimeAgent.CandidateProviders = map[string]providers.LLMProvider{
				agentID + ":fallback": set[agentID+"-fallback"],
				agentID + ":image":    set[agentID+"-image"],
				agentID + ":routed":   set[agentID+"-routed"],
				agentID + ":shared-a": set["shared"],
				agentID + ":shared-b": set["shared"],
			}
		}
	}
	assertProviderBindings := func(
		registry *AgentRegistry,
		set map[string]*p015B3ACountedProvider,
	) {
		t.Helper()
		agentCandidateProvidersMu.RLock()
		defer agentCandidateProvidersMu.RUnlock()
		for _, agentID := range []string{"main", "worker"} {
			runtimeAgent, ok := registry.GetAgent(agentID)
			if !ok || runtimeAgent == nil {
				t.Fatalf("restored runtime agent %q is missing", agentID)
			}
			if runtimeAgent.Provider != set[agentID+"-primary"] ||
				runtimeAgent.LightProvider != set[agentID+"-light"] ||
				runtimeAgent.CandidateProviders[agentID+":fallback"] != set[agentID+"-fallback"] ||
				runtimeAgent.CandidateProviders[agentID+":image"] != set[agentID+"-image"] ||
				runtimeAgent.CandidateProviders[agentID+":routed"] != set[agentID+"-routed"] ||
				runtimeAgent.CandidateProviders[agentID+":shared-a"] != set["shared"] ||
				runtimeAgent.CandidateProviders[agentID+":shared-b"] != set["shared"] {
				t.Fatalf("restored %s provider bindings are incomplete: %#v", agentID, runtimeAgent)
			}
		}
	}
	assertCloseCounts := func(
		label string,
		set map[string]*p015B3ACountedProvider,
		want int64,
	) {
		t.Helper()
		for _, key := range providerKeys {
			if got := set[key].closes.Load(); got != want {
				t.Errorf("%s %s close count = %d, want %d", label, key, got, want)
			}
		}
	}
	addProviderBindings(loop.GetRegistry(), providersA)

	identityA, err := loop.SnapshotRuntimeGenerationIdentity()
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := loop.BeginRuntimeReloadTransaction(identityA)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Close()
	retainedA, err := transaction.PublishRetainingPrevious(
		context.Background(),
		providersB["bootstrap"],
		cfgB,
		executionPolicyAgentSnapshot(t, cfgB, "B"),
		logger.DiagnosticPolicy{},
	)
	if err != nil {
		t.Fatal(err)
	}
	addProviderBindings(loop.GetRegistry(), providersB)

	retainedB, err := transaction.Rollback(context.Background(), retainedA)
	if err != nil {
		t.Fatal(err)
	}
	assertProviderBindings(loop.GetRegistry(), providersA)
	if err := transaction.CommitRetained(context.Background(), retainedB); err != nil {
		t.Fatal(err)
	}
	assertCloseCounts("retired B", providersB, 1)
	assertCloseCounts("restored A", providersA, 0)

	if err := loop.ReloadProviderAndConfigWithRuntimePolicies(
		context.Background(),
		bootstrapC,
		cfgC,
		executionPolicyAgentSnapshot(t, cfgC, "C"),
		logger.DiagnosticPolicy{},
	); err != nil {
		t.Fatal(err)
	}
	assertCloseCounts("retired A", providersA, 1)
}

func TestP015B3ARetainedProviderCommitClosesOutsideReloadLocks(t *testing.T) {
	cfgA := executionPolicyAgentConfig(t, "reentrant-close-a")
	cfgB := executionPolicyAgentConfig(t, "reentrant-close-b")
	cfgC := executionPolicyAgentConfig(t, "reentrant-close-c")
	providerA := &p015B3AReentrantCloseProvider{}
	providerB := &runtimeGateProvider{name: "B", closed: make(chan struct{})}
	providerC := &runtimeGateProvider{name: "C", closed: make(chan struct{})}
	messageBus := bus.NewMessageBus()
	loop := NewAgentLoopWithRuntimePolicies(
		cfgA,
		messageBus,
		providerA,
		executionPolicyAgentSnapshot(t, cfgA, "A"),
		logger.DiagnosticPolicy{},
	)
	t.Cleanup(func() {
		loop.Close()
		messageBus.Close()
		providerC.Close()
	})
	identityA, err := loop.SnapshotRuntimeGenerationIdentity()
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := loop.BeginRuntimeReloadTransaction(identityA)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Close()
	retainedA, err := transaction.PublishRetainingPrevious(
		context.Background(),
		providerB,
		cfgB,
		executionPolicyAgentSnapshot(t, cfgB, "B"),
		logger.DiagnosticPolicy{},
	)
	if err != nil {
		t.Fatal(err)
	}
	reentry := make(chan error, 1)
	providerA.closeFn = func() {
		transaction.Close()
		reentry <- loop.ReloadProviderAndConfigWithRuntimePolicies(
			context.Background(),
			providerC,
			cfgC,
			executionPolicyAgentSnapshot(t, cfgC, "C"),
			logger.DiagnosticPolicy{},
		)
	}
	commitDone := make(chan error, 1)
	go func() {
		commitDone <- transaction.CommitRetained(context.Background(), retainedA)
	}()
	select {
	case err := <-commitDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("provider close deadlocked under reload transaction locks")
	}
	select {
	case err := <-reentry:
		if err != nil {
			t.Fatalf("provider close reload reentry error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("provider close did not complete reload reentry")
	}
	if loop.GetConfig() != cfgC {
		t.Fatal("provider close reentry did not publish C")
	}
}

func TestP015B3AFailedRollbackKeepsBorrowedProviderGenerationLive(t *testing.T) {
	cfgA := executionPolicyAgentConfig(t, "borrowed-provider-a")
	cfgB := executionPolicyAgentConfig(t, "borrowed-provider-b")
	providerA := &runtimeGateProvider{name: "A", closed: make(chan struct{})}
	extraA := &runtimeGateProvider{name: "A-extra", closed: make(chan struct{})}
	providerB := &runtimeGateProvider{name: "B", closed: make(chan struct{})}
	messageBus := bus.NewMessageBus()
	loop := NewAgentLoopWithRuntimePolicies(
		cfgA,
		messageBus,
		providerA,
		executionPolicyAgentSnapshot(t, cfgA, "A"),
		logger.DiagnosticPolicy{},
	)
	t.Cleanup(func() {
		loop.Close()
		messageBus.Close()
	})
	runtimeAgent := loop.GetRegistry().GetDefaultAgent()
	agentCandidateProvidersMu.Lock()
	runtimeAgent.CandidateProviders["retained:borrowed"] = extraA
	agentCandidateProvidersMu.Unlock()
	identityA, err := loop.SnapshotRuntimeGenerationIdentity()
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := loop.BeginRuntimeReloadTransaction(identityA)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Close()
	retainedA, err := transaction.PublishRetainingPrevious(
		context.Background(),
		providerB,
		cfgB,
		executionPolicyAgentSnapshot(t, cfgB, "B"),
		logger.DiagnosticPolicy{},
	)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, rollbackErr := transaction.Rollback(canceled, retainedA); rollbackErr == nil {
		t.Fatal("canceled rollback unexpectedly succeeded")
	}
	for name, provider := range map[string]*runtimeGateProvider{
		"A":       providerA,
		"A-extra": extraA,
	} {
		select {
		case <-provider.closed:
			t.Fatalf("failed rollback closed borrowed provider %s", name)
		default:
		}
	}
	retainedB, err := transaction.Rollback(context.Background(), retainedA)
	if err != nil {
		t.Fatalf("retry rollback error = %v", err)
	}
	restored := loop.GetRegistry().GetDefaultAgent()
	if restored.CandidateProviders["retained:borrowed"] != extraA {
		t.Fatal("retry rollback did not restore borrowed provider")
	}
	if err := transaction.CommitRetained(context.Background(), retainedB); err != nil {
		t.Fatal(err)
	}
}

func TestP015B3ATransactionCloseCommitsAndClosesPendingGeneration(t *testing.T) {
	cfgA := executionPolicyAgentConfig(t, "transaction-close-a")
	cfgB := executionPolicyAgentConfig(t, "transaction-close-b")
	providerA := &runtimeGateProvider{name: "A", closed: make(chan struct{})}
	providerB := &runtimeGateProvider{name: "B", closed: make(chan struct{})}
	messageBus := bus.NewMessageBus()
	loop := NewAgentLoopWithRuntimePolicies(
		cfgA,
		messageBus,
		providerA,
		executionPolicyAgentSnapshot(t, cfgA, "A"),
		logger.DiagnosticPolicy{},
	)
	t.Cleanup(func() {
		loop.Close()
		messageBus.Close()
		providerB.Close()
	})
	identityA, err := loop.SnapshotRuntimeGenerationIdentity()
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := loop.BeginRuntimeReloadTransaction(identityA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.PublishRetainingPrevious(
		context.Background(),
		providerB,
		cfgB,
		executionPolicyAgentSnapshot(t, cfgB, "B"),
		logger.DiagnosticPolicy{},
	); err != nil {
		transaction.Close()
		t.Fatal(err)
	}
	transaction.Close()
	select {
	case <-providerA.closed:
	default:
		t.Fatal("transaction Close leaked pending retained A provider")
	}
	if loop.GetConfig() != cfgB {
		t.Fatal("transaction Close did not leave published B current")
	}
	if _, err := loop.BeginRuntimeReloadTransaction(identityA); err == nil {
		t.Fatal("closed transaction made stale A current")
	}
}
