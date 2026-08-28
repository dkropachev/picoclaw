package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/isolation"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

func TestAgentExecutionPolicyEnvironmentHelper(t *testing.T) {
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != "p014-agent-policy-helper" {
		return
	}
	_, _ = os.Stdout.WriteString(os.Getenv("P014_AGENT_OWNER"))
	os.Exit(0)
}

func TestAgentLoopExecutionPolicyConstructedBeforeRegistryAndTools(t *testing.T) {
	cfg := executionPolicyAgentConfig(t, "constructor")
	policy := executionPolicyAgentSnapshot(t, cfg, "A")
	if err := os.Setenv("P014_AGENT_OWNER", "LIVE"); err != nil {
		t.Fatal(err)
	}

	messageBus := bus.NewMessageBus()
	t.Cleanup(messageBus.Close)
	loop := NewAgentLoopWithExecutionPolicy(
		cfg,
		messageBus,
		&mockProvider{},
		policy,
	)
	t.Cleanup(loop.Close)

	got, err := loop.ExecutionPolicyForGeneration(cfg)
	if err != nil {
		t.Fatal(err)
	}
	assertAgentExecutionPolicyValue(t, got, "A")
	assertAgentExecutionPolicyValue(t, loop.registry.executionPolicy, "A")
	agent := loop.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("default agent unavailable")
	}
	assertAgentExecutionPolicyValue(t, agent.executionPolicy, "A")
	if got := executeAgentPolicyOwner(t, loop); got != "A" {
		t.Fatalf("exec owner = %q, want A", got)
	}
}

func TestAgentLoopFailedCandidateCannotSwitchLiveOrIndependentGeneration(t *testing.T) {
	cfgA := executionPolicyAgentConfig(t, "failed-a")
	cfgB := executionPolicyAgentConfig(t, "failed-b")
	cfgC := executionPolicyAgentConfig(t, "failed-c")
	policyA := executionPolicyAgentSnapshot(t, cfgA, "A")
	policyB := executionPolicyAgentSnapshot(t, cfgB, "B")
	policyC := executionPolicyAgentSnapshot(t, cfgC, "C")

	busA := bus.NewMessageBus()
	busC := bus.NewMessageBus()
	t.Cleanup(busA.Close)
	t.Cleanup(busC.Close)
	loopA := NewAgentLoopWithExecutionPolicy(cfgA, busA, &mockProvider{}, policyA)
	loopC := NewAgentLoopWithExecutionPolicy(cfgC, busC, &mockProvider{}, policyC)
	t.Cleanup(loopA.Close)
	t.Cleanup(loopC.Close)

	entered := make(chan struct{})
	release := make(chan struct{})
	loopA.policyRegistryFactory = func(
		gotCfg *config.Config,
		_ providers.LLMProvider,
		gotPolicy isolation.ExecutionPolicy,
	) *AgentRegistry {
		if gotCfg != cfgB {
			t.Errorf("candidate config = %p, want %p", gotCfg, cfgB)
		}
		if value, ok := gotPolicy.LookupEnvironment("P014_AGENT_OWNER"); !ok || value != "B" {
			t.Errorf("candidate policy owner = %q, %t", value, ok)
		}
		close(entered)
		<-release
		return nil
	}

	reloadDone := make(chan error, 1)
	go func() {
		reloadDone <- loopA.ReloadProviderAndConfigWithExecutionPolicy(
			context.Background(),
			&mockProvider{},
			cfgB,
			policyB,
		)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("candidate registry did not start")
	}

	isolation.Configure(&config.Config{Isolation: config.IsolationConfig{
		EnvironmentAllowlist: []string{"P014_AGENT_OWNER"},
	}})
	t.Cleanup(func() { isolation.Configure(nil) })
	if got := executeAgentPolicyOwner(t, loopA); got != "A" {
		t.Fatalf("live A owner = %q", got)
	}
	if got := executeAgentPolicyOwner(t, loopC); got != "C" {
		t.Fatalf("independent C owner = %q", got)
	}

	close(release)
	if err := <-reloadDone; err == nil || !strings.Contains(err.Error(), "registry creation failed") {
		t.Fatalf("failed candidate reload error = %v", err)
	}
	if got := executeAgentPolicyOwner(t, loopA); got != "A" {
		t.Fatalf("post-failure A owner = %q", got)
	}
	if _, err := loopA.ExecutionPolicyForGeneration(cfgB); err == nil {
		t.Fatal("failed candidate config became current")
	}
	current, err := loopA.ExecutionPolicyForGeneration(cfgA)
	if err != nil {
		t.Fatal(err)
	}
	assertAgentExecutionPolicyValue(t, current, "A")
}

func TestAgentLoopSuccessfulReloadPublishesExactPolicyTuple(t *testing.T) {
	cfgA := executionPolicyAgentConfig(t, "reload-a")
	cfgB := executionPolicyAgentConfig(t, "reload-b")
	policyA := executionPolicyAgentSnapshot(t, cfgA, "A")
	policyB := executionPolicyAgentSnapshot(t, cfgB, "B")
	messageBus := bus.NewMessageBus()
	t.Cleanup(messageBus.Close)
	loop := NewAgentLoopWithExecutionPolicy(cfgA, messageBus, &mockProvider{}, policyA)
	t.Cleanup(loop.Close)

	if err := loop.ReloadProviderAndConfigWithExecutionPolicy(
		context.Background(),
		&mockProvider{},
		cfgB,
		policyB,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := loop.ExecutionPolicyForGeneration(cfgA); err == nil {
		t.Fatal("old generation remained current")
	}
	current, err := loop.ExecutionPolicyForGeneration(cfgB)
	if err != nil {
		t.Fatal(err)
	}
	assertAgentExecutionPolicyValue(t, current, "B")
	assertAgentExecutionPolicyValue(t, loop.registry.executionPolicy, "B")
	if got := executeAgentPolicyOwner(t, loop); got != "B" {
		t.Fatalf("reloaded exec owner = %q", got)
	}
}

func TestAgentLoopStrictReloadRejectsInvalidAndReusedGeneration(t *testing.T) {
	cfgA := executionPolicyAgentConfig(t, "strict-a")
	policyA := executionPolicyAgentSnapshot(t, cfgA, "A")
	messageBus := bus.NewMessageBus()
	t.Cleanup(messageBus.Close)
	loop := NewAgentLoopWithExecutionPolicy(cfgA, messageBus, &mockProvider{}, policyA)
	t.Cleanup(loop.Close)
	registryA := loop.GetRegistry()

	if err := loop.ReloadProviderAndConfigWithExecutionPolicy(
		context.Background(),
		&mockProvider{},
		cfgA,
		policyA,
	); err == nil || !strings.Contains(err.Error(), "new generation") {
		t.Fatalf("same-pointer strict reload error = %v", err)
	}
	cfgB := executionPolicyAgentConfig(t, "strict-b")
	invalidPolicy := isolation.NewExecutionPolicy(config.IsolationConfig{
		EnvironmentAllowlist: []string{"BAD-NAME"},
	})
	if err := loop.ReloadProviderAndConfigWithExecutionPolicy(
		context.Background(),
		&mockProvider{},
		cfgB,
		invalidPolicy,
	); err == nil || !strings.Contains(err.Error(), "policy is invalid") {
		t.Fatalf("invalid-policy strict reload error = %v", err)
	}
	if loop.GetConfig() != cfgA || loop.GetRegistry() != registryA {
		t.Fatal("rejected strict reload changed the live generation")
	}
	current, err := loop.ExecutionPolicyForGeneration(cfgA)
	if err != nil {
		t.Fatal(err)
	}
	assertAgentExecutionPolicyValue(t, current, "A")
}

func TestAgentLoopCanceledDuringCandidateMediaCannotPublish(t *testing.T) {
	cfgA := executionPolicyAgentConfig(t, "cancel-a")
	cfgB := executionPolicyAgentConfig(t, "cancel-b")
	policyA := executionPolicyAgentSnapshot(t, cfgA, "A")
	policyB := executionPolicyAgentSnapshot(t, cfgB, "B")
	messageBus := bus.NewMessageBus()
	t.Cleanup(messageBus.Close)
	loop := NewAgentLoopWithExecutionPolicy(cfgA, messageBus, &mockProvider{}, policyA)
	t.Cleanup(loop.Close)
	registryA := loop.GetRegistry()
	blocker := &reloadMediaSetterBlock{
		first: nil, started: make(chan struct{}), release: make(chan struct{}),
	}
	loop.policyRegistryFactory = func(
		cfg *config.Config,
		provider providers.LLMProvider,
		policy isolation.ExecutionPolicy,
	) *AgentRegistry {
		candidate := NewAgentRegistryWithExecutionPolicy(cfg, provider, policy)
		candidate.GetDefaultAgent().Tools.Register(blocker)
		return candidate
	}
	ctx, cancel := context.WithCancel(context.Background())
	reloadDone := make(chan error, 1)
	go func() {
		reloadDone <- loop.ReloadProviderAndConfigWithExecutionPolicy(
			ctx,
			&mockProvider{},
			cfgB,
			policyB,
		)
	}()
	select {
	case <-blocker.started:
	case <-time.After(5 * time.Second):
		t.Fatal("candidate media application did not block")
	}
	cancel()
	close(blocker.release)
	if err := <-reloadDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled reload error = %v", err)
	}
	if loop.GetConfig() != cfgA || loop.GetRegistry() != registryA {
		t.Fatal("canceled reload published candidate generation")
	}
}

func TestAgentLoopCloseCancelsBlockedCandidatePublicationAndIsIdempotent(t *testing.T) {
	cfgA := executionPolicyAgentConfig(t, "close-a")
	cfgB := executionPolicyAgentConfig(t, "close-b")
	policyA := executionPolicyAgentSnapshot(t, cfgA, "A")
	policyB := executionPolicyAgentSnapshot(t, cfgB, "B")
	messageBus := bus.NewMessageBus()
	t.Cleanup(messageBus.Close)
	loop := NewAgentLoopWithExecutionPolicy(cfgA, messageBus, &mockProvider{}, policyA)
	registryA := loop.GetRegistry()
	entered := make(chan struct{})
	release := make(chan struct{})
	loop.policyRegistryFactory = func(
		cfg *config.Config,
		provider providers.LLMProvider,
		policy isolation.ExecutionPolicy,
	) *AgentRegistry {
		close(entered)
		<-release
		return NewAgentRegistryWithExecutionPolicy(cfg, provider, policy)
	}
	reloadDone := make(chan error, 1)
	go func() {
		reloadDone <- loop.ReloadProviderAndConfigWithExecutionPolicy(
			context.Background(),
			&mockProvider{},
			cfgB,
			policyB,
		)
	}()
	<-entered
	closeDone := make(chan struct{})
	go func() {
		loop.Close()
		close(closeDone)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !loop.closing.Load() {
		if time.Now().After(deadline) {
			t.Fatal("Close did not begin closing the loop")
		}
		runtime.Gosched()
	}
	close(release)
	if err := <-reloadDone; err == nil || !strings.Contains(err.Error(), "closing") {
		t.Fatalf("reload during Close error = %v", err)
	}
	select {
	case <-closeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not complete")
	}
	loop.Close()
	if loop.GetConfig() != cfgA || loop.GetRegistry() != registryA {
		t.Fatal("Close race published candidate generation")
	}
	if err := loop.ReloadProviderAndConfigWithExecutionPolicy(
		context.Background(),
		&mockProvider{},
		cfgB,
		policyB,
	); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("reload after Close error = %v", err)
	}
}

func TestAgentRegistryCloseCandidateClosesOnlyInternalProviders(t *testing.T) {
	cfg := executionPolicyAgentConfig(t, "candidate-close")
	bootstrap := &runtimeGateProvider{name: "bootstrap", closed: make(chan struct{})}
	internal := &runtimeGateProvider{name: "internal", closed: make(chan struct{})}
	registry := NewAgentRegistryWithExecutionPolicy(
		cfg,
		bootstrap,
		executionPolicyAgentSnapshot(t, cfg, "A"),
	)
	agent := registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("default agent missing")
	}
	agentCandidateProvidersMu.Lock()
	agent.CandidateProviders["p014-internal"] = internal
	agentCandidateProvidersMu.Unlock()
	registry.CloseCandidate()
	select {
	case <-internal.closed:
	case <-time.After(time.Second):
		t.Fatal("internal candidate provider was not closed")
	}
	select {
	case <-bootstrap.closed:
		t.Fatal("externally-owned bootstrap provider was closed")
	default:
	}
}

func executionPolicyAgentConfig(t *testing.T, name string) *config.Config {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.RestrictToWorkspace = false
	cfg.Tools.Exec.Enabled = true
	cfg.Isolation.EnvironmentAllowlist = []string{
		"PATH",
		"HOME",
		"GOCOVERDIR",
		"P014_AGENT_OWNER",
	}
	if len(cfg.Agents.List) > 0 {
		cfg.Agents.List[0].Name = name
	}
	return cfg
}

func executionPolicyAgentSnapshot(
	t *testing.T,
	cfg *config.Config,
	owner string,
) isolation.ExecutionPolicy {
	t.Helper()
	previous, hadPrevious := os.LookupEnv("P014_AGENT_OWNER")
	if err := os.Setenv("P014_AGENT_OWNER", owner); err != nil {
		t.Fatal(err)
	}
	policy := isolation.NewExecutionPolicy(cfg.Isolation)
	if hadPrevious {
		if err := os.Setenv("P014_AGENT_OWNER", previous); err != nil {
			t.Fatal(err)
		}
	} else if err := os.Unsetenv("P014_AGENT_OWNER"); err != nil {
		t.Fatal(err)
	}
	return policy
}

func assertAgentExecutionPolicyValue(
	t *testing.T,
	policy isolation.ExecutionPolicy,
	want string,
) {
	t.Helper()
	got, ok := policy.LookupEnvironment("P014_AGENT_OWNER")
	if !ok || got != want {
		t.Fatalf("policy owner = %q, %t; want %q", got, ok, want)
	}
}

func executeAgentPolicyOwner(t *testing.T, loop *AgentLoop) string {
	t.Helper()
	registry := loop.GetRegistry()
	if registry == nil {
		t.Fatal("registry unavailable")
	}
	agent := registry.GetDefaultAgent()
	if agent == nil || agent.Tools == nil {
		t.Fatal("default agent tools unavailable")
	}
	tool, ok := agent.Tools.Get("exec")
	if !ok {
		t.Fatal("exec tool unavailable")
	}
	execTool, ok := tool.(*tools.ExecTool)
	if !ok {
		t.Fatalf("exec tool type = %T", tool)
	}
	result := execTool.Execute(context.Background(), map[string]any{
		"action":  "run",
		"command": executionPolicyAgentHelperCommand(),
	})
	if result == nil || result.IsError {
		t.Fatalf("exec result = %#v", result)
	}
	return strings.TrimSpace(result.ForLLM)
}

func executionPolicyAgentHelperCommand() string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf(
			`& %q -test.run=^TestAgentExecutionPolicyEnvironmentHelper$ -- p014-agent-policy-helper`,
			os.Args[0],
		)
	}
	return fmt.Sprintf(
		"'%s' -test.run=^TestAgentExecutionPolicyEnvironmentHelper$ -- p014-agent-policy-helper",
		strings.ReplaceAll(os.Args[0], "'", "'\\''"),
	)
}
