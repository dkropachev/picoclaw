package agent

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type recursionToolPolicyDeny struct {
	calls atomic.Int64

	mu       sync.Mutex
	requests []tools.ToolPolicyRequest
}

func (policy *recursionToolPolicyDeny) EvaluateTool(
	_ context.Context,
	request tools.ToolPolicyRequest,
) (tools.ToolPolicyDecision, error) {
	policy.calls.Add(1)
	policy.mu.Lock()
	policy.requests = append(policy.requests, request)
	policy.mu.Unlock()
	return tools.ToolPolicyDecision{
		Kind:       tools.ToolPolicyDecisionDeny,
		ReasonCode: "recursion_test_deny",
	}, nil
}

func (policy *recursionToolPolicyDeny) snapshotRequests() []tools.ToolPolicyRequest {
	policy.mu.Lock()
	defer policy.mu.Unlock()
	return append([]tools.ToolPolicyRequest(nil), policy.requests...)
}

type recursionToolPolicyProvider struct {
	toolName string

	mu    sync.Mutex
	calls int
}

func (provider *recursionToolPolicyProvider) Chat(
	_ context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	provider.mu.Lock()
	provider.calls++
	call := provider.calls
	provider.mu.Unlock()

	// Each manager run is exactly two iterations: request the canary, then
	// finish after receiving the policy-denied tool result.
	if call%2 == 1 {
		return &providers.LLMResponse{
			FinishReason: "tool_calls",
			ToolCalls: []providers.ToolCall{{
				ID:        fmt.Sprintf("recursion-policy-call-%d", call),
				Name:      provider.toolName,
				Arguments: map[string]any{},
			}},
		}, nil
	}
	return &providers.LLMResponse{Content: "done", FinishReason: "stop"}, nil
}

type recursionToolPolicyEffect struct {
	calls atomic.Int64
}

func (*recursionToolPolicyEffect) Name() string { return "recursion_policy_effect" }

func (*recursionToolPolicyEffect) Description() string {
	return "records whether an Agent-owned legacy subagent bypassed tool policy"
}

func (*recursionToolPolicyEffect) Parameters() map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"additionalProperties": false,
	}
}

func (effect *recursionToolPolicyEffect) Execute(
	context.Context,
	map[string]any,
) *tools.ToolResult {
	effect.calls.Add(1)
	return tools.SilentResult("effect executed")
}

func waitForRecursionToolPolicyTask(
	t *testing.T,
	manager *tools.SubagentManager,
	wantTaskID string,
) tools.SubagentTask {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if task, ok := manager.GetTaskCopy(wantTaskID); ok && task.Status != "running" {
			return task
		}
		if time.Now().After(deadline) {
			t.Fatalf("legacy recursion task %q did not finish: %#v", wantTaskID, manager.ListTaskCopies())
		}
		time.Sleep(time.Millisecond)
	}
}

func runRecursionToolPolicyManager(
	t *testing.T,
	manager *tools.SubagentManager,
	label string,
) {
	t.Helper()
	manager.SetSpawner(nil) // Exercise the manager's legacy RunToolLoop fallback.
	ctx := tools.WithToolSessionContext(
		context.Background(),
		"source-agent",
		"source-session-"+label,
		nil,
	)
	ack, err := manager.Spawn(
		ctx,
		"attempt the policy canary",
		label,
		"target-agent",
		"test",
		"chat-"+label,
		nil,
	)
	if err != nil {
		t.Fatalf("manager.Spawn(%q) error = %v", label, err)
	}
	if ack == "" {
		t.Fatalf("manager.Spawn(%q) returned an empty acknowledgement", label)
	}
	task := waitForRecursionToolPolicyTask(t, manager, "subagent-1")
	if task.Status != "completed" || task.Result == "" {
		t.Fatalf("legacy recursion task %q = %#v, want completed result", label, task)
	}
}

func TestRecursionCatalogAgentOwnedManagersInheritPreparedToolPolicy(t *testing.T) {
	fixture := newRecursionCatalogFixture(t, "main")
	fixture.cfg.Tools.Spawn.Enabled = true
	fixture.cfg.Tools.Subagent.Enabled = true

	effect := &recursionToolPolicyEffect{}
	fixture.agents["main"].Tools.Register(effect)
	provider := &recursionToolPolicyProvider{toolName: effect.Name()}
	fixture.provider = provider

	strictPolicy := &recursionToolPolicyDeny{}
	fixture.loop.toolPolicy = strictPolicy
	constructed := make([]*tools.SubagentManager, 0, 2)
	dependencies := defaultRecursionCatalogDependencies()
	dependencies.newManager = func(
		provider providers.LLMProvider,
		model string,
		workspace string,
	) *tools.SubagentManager {
		manager := tools.NewSubagentManager(provider, model, workspace)
		constructed = append(constructed, manager)
		return manager
	}

	candidate := fixture.prepare(t, "main", dependencies)
	if len(constructed) != 1 {
		t.Fatalf("prepared root manager count = %d, want 1", len(constructed))
	}
	if err := installRecursionCatalog(
		[]recursionCatalogCandidate{candidate},
		dependencies.install,
	); err != nil {
		t.Fatal(err)
	}

	// Model a later generation replacing the loop's current policy. The owner
	// factory must retain the exact policy captured by candidate preparation.
	fixture.loop.toolPolicy = tools.CompatibilityAllowToolPolicy{}
	owned, err := fixture.agents["main"].Tools.InstantiateForOwnerSelection(
		tools.ToolOwner{Scope: tools.ToolOwnerScopeTurn, TurnID: "policy-owner"},
		[]string{"spawn"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer owned.Close()
	if len(constructed) != 2 {
		t.Fatalf("owner manager count = %d, want root plus one owner", len(constructed))
	}

	runRecursionToolPolicyManager(t, constructed[0], "prepared-root")
	runRecursionToolPolicyManager(t, constructed[1], "factory-owner")

	if got := effect.calls.Load(); got != 0 {
		t.Fatalf("policy-denied Agent-owned recursion effects = %d, want 0", got)
	}
	if got := strictPolicy.calls.Load(); got != 2 {
		t.Fatalf("strict recursion policy evaluations = %d, want 2", got)
	}
	requests := strictPolicy.snapshotRequests()
	if len(requests) != 2 {
		t.Fatalf("strict recursion policy requests = %#v", requests)
	}
	for index, request := range requests {
		if request.Tool != effect.Name() ||
			request.Subject.Source != tools.ToolPolicySourceLegacySubagent ||
			request.Subject.AgentID != "target-agent" ||
			request.Subject.ToolCallID == "" {
			t.Fatalf("strict recursion request %d = %#v", index, request)
		}
	}
}
