package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestWorkflowPromptCacheKey(t *testing.T) {
	tests := []struct {
		name       string
		mode       string
		agentID    string
		sessionKey string
		wantKey    string
		wantOff    bool
	}{
		{
			name:       "default uses session key",
			sessionKey: "workflow:chat:123",
			agentID:    "main",
			wantKey:    "workflow:chat:123",
		},
		{
			name:       "session uses session key",
			mode:       "session",
			sessionKey: "workflow:chat:123",
			agentID:    "main",
			wantKey:    "workflow:chat:123",
		},
		{
			name:       "agent uses agent id",
			mode:       "agent",
			sessionKey: "workflow:chat:123",
			agentID:    "main",
			wantKey:    "main",
		},
		{
			name:       "none disables prompt cache key",
			mode:       "none",
			sessionKey: "workflow:chat:123",
			agentID:    "main",
			wantOff:    true,
		},
		{
			name:       "custom key",
			mode:       "key:shared-summarizer",
			sessionKey: "workflow:chat:123",
			agentID:    "main",
			wantKey:    "shared-summarizer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotKey, gotOff := workflowPromptCacheKey(tt.mode, tt.agentID, tt.sessionKey)
			if gotKey != tt.wantKey || gotOff != tt.wantOff {
				t.Fatalf(
					"workflowPromptCacheKey(%q) = (%q, %v), want (%q, %v)",
					tt.mode,
					gotKey,
					gotOff,
					tt.wantKey,
					tt.wantOff,
				)
			}
		})
	}
}

func TestWorkflowManagedScopeSplitCombinesStructuredOutputs(t *testing.T) {
	contract := &workflows.AgentOutputContract{
		Format:         "json",
		RepairAttempts: 1,
		Schema: map[string]any{
			"type":     "object",
			"required": []any{"summary", "findings"},
			"properties": map[string]any{
				"summary":  map[string]any{"type": "string"},
				"findings": map[string]any{"type": "array"},
			},
		},
	}
	req := workflows.AgentRequest{
		Prompt: "Review assigned scope.",
		Managed: map[string]any{
			"mode":                "auto",
			"max_items_per_chunk": 2,
			"calibration": map[string]any{
				"enabled":     true,
				"sample_size": 3,
			},
		},
		Scope: []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
			map[string]any{"id": "c"},
			map[string]any{"id": "d"},
			map[string]any{"id": "e"},
		},
		Output: contract,
	}
	runOnce := func(message string, _ bool, _ workflowAgentRunOptions) (string, error) {
		ids := workflowTestScopeIDs(t, message)
		findings := make([]string, 0, len(ids))
		for _, id := range ids {
			findings = append(findings, fmt.Sprintf(`{"scope_id":%q,"title":"finding %s"}`, id, id))
		}
		return fmt.Sprintf(`{"summary":%q,"findings":[%s]}`, strings.Join(ids, ","), strings.Join(findings, ",")), nil
	}

	outputs, err := (&workflowAgentRunner{}).runManagedSplit(
		req,
		&AgentInstance{ID: "reviewer", Model: "mock-model"},
		"reviewer",
		"workflow:test",
		"none",
		"none",
		"",
		"scope_split",
		runOnce,
	)
	if err != nil {
		t.Fatalf("runManagedSplit() error = %v", err)
	}
	structured, ok := outputs["structured"].(map[string]any)
	if !ok {
		t.Fatalf("structured = %#v, want object", outputs["structured"])
	}
	findings, ok := structured["findings"].([]any)
	if !ok || len(findings) != 5 {
		t.Fatalf("findings = %#v, want five combined findings", structured["findings"])
	}
	managed, ok := outputs["managed"].(map[string]any)
	if !ok {
		t.Fatalf("managed = %#v, want metadata", outputs["managed"])
	}
	if managed["strategy"] != "scope_split" {
		t.Fatalf("strategy = %#v, want scope_split", managed["strategy"])
	}
	calibration := managed["calibration"].(map[string]any)
	if calibration["match"] != true {
		t.Fatalf("calibration = %#v, want match", calibration)
	}
	split := managed["split"].(map[string]any)
	if split["child_count"] != 3 {
		t.Fatalf("split child_count = %#v, want 3", split["child_count"])
	}
	tokenEfficiency, ok := split["token_efficiency"].(map[string]any)
	if !ok {
		t.Fatalf("token_efficiency = %#v, want metadata", split["token_efficiency"])
	}
	childTokens, ok := tokenEfficiency["child_prompt_tokens"].([]int)
	if !ok || len(childTokens) != 3 {
		t.Fatalf("child_prompt_tokens = %#v, want one estimate per child", tokenEfficiency["child_prompt_tokens"])
	}
}

func TestWorkflowAgentRunnerRejectsUnknownExplicitAgent(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	msgBus := bus.NewMessageBus()
	defer msgBus.Close()
	al := NewAgentLoop(cfg, msgBus, &mockProvider{})
	defer al.Close()

	_, err := (&workflowAgentRunner{loop: al}).RunAgent(context.Background(), workflows.AgentRequest{
		AgentID: "reviewer",
		Prompt:  "Review this.",
	})
	if err == nil || !strings.Contains(err.Error(), `workflow agent "reviewer" not found`) {
		t.Fatalf("RunAgent() error = %v, want unknown agent error", err)
	}
}

func TestWorkflowManagedChildrenDisableTools(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Prompt: "Review assigned scope.",
		Managed: map[string]any{
			"mode":                  "auto",
			"max_items_per_chunk":   1,
			"max_parallel_children": 1,
			"calibration": map[string]any{
				"enabled": false,
			},
		},
		Scope: []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
			map[string]any{"id": "c"},
		},
		Output: contract,
	}
	seen := make([]workflowAgentRunOptions, 0)
	runOnce := func(message string, _ bool, options workflowAgentRunOptions) (string, error) {
		seen = append(seen, options)
		ids := workflowTestScopeIDs(t, message)
		findings := make([]string, 0, len(ids))
		for _, id := range ids {
			findings = append(findings, fmt.Sprintf(`{"scope_id":%q,"title":"finding %s"}`, id, id))
		}
		return fmt.Sprintf(`{"summary":%q,"findings":[%s]}`, strings.Join(ids, ","), strings.Join(findings, ",")), nil
	}

	_, err := (&workflowAgentRunner{}).runManagedSplit(
		req,
		&AgentInstance{ID: "reviewer", Model: "mock-model"},
		"reviewer",
		"workflow:test",
		"none",
		"none",
		"",
		"scope_split",
		runOnce,
	)
	if err != nil {
		t.Fatalf("runManagedSplit() error = %v", err)
	}
	if len(seen) != 3 {
		t.Fatalf("child run count = %d, want 3", len(seen))
	}
	for i, options := range seen {
		if !options.NoTools {
			t.Fatalf("child %d run options = %#v, want NoTools", i, options)
		}
	}
}

func TestWorkflowManagedSplitStrategyRequiresStructuredOutputAndSplittableDimensions(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	scope := []any{
		map[string]any{"id": "a"},
		map[string]any{"id": "b"},
		map[string]any{"id": "c"},
	}
	agentWithTasks := &AgentInstance{
		Definition: AgentContextDefinition{Agent: &AgentPromptDefinition{Tasks: []string{
			"Find correctness bugs.",
			"Find security risks.",
			"Find missing tests.",
		}}},
	}
	tests := []struct {
		name  string
		req   workflows.AgentRequest
		agent *AgentInstance
		want  string
	}{
		{
			name: "managed off",
			req: workflows.AgentRequest{
				Managed: "off",
				Scope:   scope,
				Output:  contract,
			},
			agent: agentWithTasks,
		},
		{
			name: "missing structured output",
			req: workflows.AgentRequest{
				Managed: map[string]any{"mode": "auto", "max_items_per_chunk": 2},
				Scope:   scope,
			},
			agent: agentWithTasks,
		},
		{
			name: "auto prefers hybrid",
			req: workflows.AgentRequest{
				Managed: map[string]any{
					"mode":                "auto",
					"max_items_per_chunk": 2,
					"max_tasks_per_chunk": 2,
				},
				Scope:  scope,
				Output: contract,
			},
			agent: agentWithTasks,
			want:  "hybrid_split",
		},
		{
			name: "auto scope only",
			req: workflows.AgentRequest{
				Managed: map[string]any{"mode": "auto", "max_items_per_chunk": 2},
				Scope:   scope,
				Output:  contract,
			},
			agent: &AgentInstance{},
			want:  "scope_split",
		},
		{
			name: "auto task only",
			req: workflows.AgentRequest{
				Managed: map[string]any{"mode": "auto", "max_tasks_per_chunk": 2},
				Output:  contract,
			},
			agent: agentWithTasks,
			want:  "task_split",
		},
		{
			name: "requested scope alias",
			req: workflows.AgentRequest{
				Managed: map[string]any{
					"strategy":            "by_scope",
					"max_items_per_chunk": 2,
				},
				Scope:  scope,
				Output: contract,
			},
			agent: agentWithTasks,
			want:  "scope_split",
		},
		{
			name: "requested task ignored when not splittable",
			req: workflows.AgentRequest{
				Managed: map[string]any{
					"strategy":            "task_split",
					"max_tasks_per_chunk": 10,
				},
				Output: contract,
			},
			agent: agentWithTasks,
		},
		{
			name: "requested none",
			req: workflows.AgentRequest{
				Managed: map[string]any{"split": "none"},
				Scope:   scope,
				Output:  contract,
			},
			agent: agentWithTasks,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := workflowManagedSplitStrategy(tt.req, tt.agent)
			if got != tt.want {
				t.Fatalf("workflowManagedSplitStrategy() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWorkflowManagedSplitStrategyUsesScopeTokenBudget(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Prompt: "Review assigned scope.",
		Scope: []any{
			map[string]any{"id": "a", "content": strings.Repeat("a", 2048)},
			map[string]any{"id": "b", "content": strings.Repeat("b", 2048)},
			map[string]any{"id": "c", "content": strings.Repeat("c", 2048)},
		},
		Output: contract,
	}
	singleItemTokens := workflowScopeChunkPromptTokens(req, workflowScopeItems(req.Scope)[:1])
	req.Managed = map[string]any{
		"mode":                       "auto",
		"max_items_per_chunk":        8,
		"target_child_prompt_tokens": singleItemTokens + 1,
	}

	if got := workflowManagedSplitStrategy(req, &AgentInstance{}); got != "scope_split" {
		t.Fatalf("workflowManagedSplitStrategy() = %q, want scope_split", got)
	}
}

func TestWorkflowManagedAdaptiveScopeChunkingPacksLargerChunks(t *testing.T) {
	req := workflows.AgentRequest{
		Prompt: "Review assigned scope.",
		Scope: []any{
			map[string]any{"id": "a", "content": strings.Repeat("a", 128)},
			map[string]any{"id": "b", "content": strings.Repeat("b", 128)},
			map[string]any{"id": "c", "content": strings.Repeat("c", 128)},
			map[string]any{"id": "d", "content": strings.Repeat("d", 128)},
			map[string]any{"id": "e", "content": strings.Repeat("e", 128)},
		},
		Output: workflowManagedTestOutputContract(),
	}
	scope := workflowScopeItems(req.Scope)
	twoItemTokens := workflowScopeChunkPromptTokens(req, scope[:2])
	options := workflowManagedOptions(map[string]any{
		"mode":                       "auto",
		"max_items_per_chunk":        8,
		"target_child_prompt_tokens": twoItemTokens,
	})

	chunks := workflowManagedScopeChunks(req, options)
	counts := make([]int, 0, len(chunks))
	for _, chunk := range chunks {
		counts = append(counts, len(chunk))
	}
	if fmt.Sprint(counts) != "[2 2 1]" {
		t.Fatalf("chunk counts = %#v, want [2 2 1]", counts)
	}
}

func TestWorkflowManagedSplitUsesAssignedTasksForStrategyAndMetadata(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Managed: map[string]any{
			"mode":                "auto",
			"max_tasks_per_chunk": 2,
		},
		Context: workflowManagedTaskMessage([]string{
			"Review correctness.",
			"Review security.",
			"Review test coverage.",
		}),
		Output: contract,
	}
	agent := &AgentInstance{}

	strategy := workflowManagedSplitStrategy(req, agent)
	if strategy != "task_split" {
		t.Fatalf("workflowManagedSplitStrategy() = %q, want task_split", strategy)
	}
	options := workflowManagedOptions(req.Managed)
	plans := workflowManagedChildPlans(req, agent, options, strategy)
	if len(plans) != 2 {
		t.Fatalf("plans len = %d, want 2", len(plans))
	}
	split := workflowManagedSplitMetadata(req, agent, options, strategy, plans)
	if split["task_count"] != 3 {
		t.Fatalf("split task_count = %#v, want 3", split["task_count"])
	}
	counts, ok := split["child_task_counts"].([]int)
	if !ok || len(counts) != 2 || counts[0] != 2 || counts[1] != 1 {
		t.Fatalf("child_task_counts = %#v, want [2 1]", split["child_task_counts"])
	}
}

func TestWorkflowManagedTaskSplitCombinesStructuredOutputs(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Prompt: "Analyze assigned task subset.",
		Managed: map[string]any{
			"mode":                "auto",
			"max_tasks_per_chunk": 2,
			"calibration": map[string]any{
				"enabled":          true,
				"task_sample_size": 3,
			},
		},
		Output: contract,
	}
	agent := &AgentInstance{
		ID:    "reviewer",
		Model: "mock-model",
		Definition: AgentContextDefinition{Agent: &AgentPromptDefinition{Tasks: []string{
			"Find correctness bugs.",
			"Find security risks.",
			"Find missing tests.",
			"Find performance issues.",
			"Find API contract issues.",
		}}},
	}
	runOnce := func(message string, _ bool, _ workflowAgentRunOptions) (string, error) {
		tasks := workflowTestAssignedTasks(t, message)
		findings := make([]string, 0, len(tasks))
		for _, task := range tasks {
			findings = append(findings, fmt.Sprintf(`{"task":%q,"title":"%s"}`, task, task))
		}
		return fmt.Sprintf(`{"summary":%q,"findings":[%s]}`, strings.Join(tasks, ","), strings.Join(findings, ",")), nil
	}

	outputs, err := (&workflowAgentRunner{}).runManagedSplit(
		req,
		agent,
		"reviewer",
		"workflow:test",
		"none",
		"none",
		"",
		"task_split",
		runOnce,
	)
	if err != nil {
		t.Fatalf("runManagedSplit() error = %v", err)
	}
	structured := outputs["structured"].(map[string]any)
	findings := structured["findings"].([]any)
	if len(findings) != 5 {
		t.Fatalf("findings = %#v, want five task findings", findings)
	}
	managed := outputs["managed"].(map[string]any)
	if managed["strategy"] != "task_split" {
		t.Fatalf("strategy = %#v, want task_split", managed["strategy"])
	}
	split := managed["split"].(map[string]any)
	if split["child_count"] != 3 {
		t.Fatalf("split child_count = %#v, want 3", split["child_count"])
	}
}

func TestWorkflowManagedHybridSplitCombinesStructuredOutputs(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Prompt: "Analyze assigned scope and task subset.",
		Managed: map[string]any{
			"mode":                "auto",
			"max_items_per_chunk": 2,
			"max_tasks_per_chunk": 2,
			"calibration": map[string]any{
				"enabled":          true,
				"sample_size":      3,
				"task_sample_size": 3,
			},
		},
		Scope: []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
			map[string]any{"id": "c"},
			map[string]any{"id": "d"},
		},
		Output: contract,
	}
	agent := &AgentInstance{
		ID:    "reviewer",
		Model: "mock-model",
		Definition: AgentContextDefinition{Agent: &AgentPromptDefinition{Tasks: []string{
			"Find correctness bugs.",
			"Find security risks.",
			"Find missing tests.",
		}}},
	}
	runOnce := func(message string, _ bool, _ workflowAgentRunOptions) (string, error) {
		ids := workflowTestScopeIDs(t, message)
		tasks := workflowTestAssignedTasks(t, message)
		findings := make([]string, 0, len(ids)*len(tasks))
		for _, id := range ids {
			for _, task := range tasks {
				findings = append(findings, fmt.Sprintf(`{"scope_id":%q,"task":%q}`, id, task))
			}
		}
		return fmt.Sprintf(`{"summary":%q,"findings":[%s]}`, strings.Join(ids, ","), strings.Join(findings, ",")), nil
	}

	outputs, err := (&workflowAgentRunner{}).runManagedSplit(
		req,
		agent,
		"reviewer",
		"workflow:test",
		"none",
		"none",
		"",
		"hybrid_split",
		runOnce,
	)
	if err != nil {
		t.Fatalf("runManagedSplit() error = %v", err)
	}
	structured := outputs["structured"].(map[string]any)
	findings := structured["findings"].([]any)
	if len(findings) != 12 {
		t.Fatalf("findings = %#v, want twelve hybrid findings", findings)
	}
	managed := outputs["managed"].(map[string]any)
	if managed["strategy"] != "hybrid_split" {
		t.Fatalf("strategy = %#v, want hybrid_split", managed["strategy"])
	}
	split := managed["split"].(map[string]any)
	if split["child_count"] != 4 {
		t.Fatalf("split child_count = %#v, want 4", split["child_count"])
	}
	if fmt.Sprint(split["child_scope_counts"]) != "[2 2 2 2]" {
		t.Fatalf("child_scope_counts = %#v, want [2 2 2 2]", split["child_scope_counts"])
	}
	if fmt.Sprint(split["child_task_counts"]) != "[2 1 2 1]" {
		t.Fatalf("child_task_counts = %#v, want [2 1 2 1]", split["child_task_counts"])
	}
	calibration := managed["calibration"].(map[string]any)
	if calibration["status"] != "passed" || calibration["sample_scope"] != 3 || calibration["sample_tasks"] != 3 {
		t.Fatalf("calibration = %#v, want passed three-scope three-task sample", calibration)
	}
	children := outputs["managed_children"].([]map[string]any)
	if len(children) != 4 {
		t.Fatalf("managed_children len = %d, want 4", len(children))
	}
	for _, child := range children {
		label := fmt.Sprint(child["label"])
		if !strings.Contains(label, "scope chunk") || !strings.Contains(label, "task chunk") {
			t.Fatalf("child label = %q, want both split axes", label)
		}
		if child["scope_count"] != 2 {
			t.Fatalf("child scope_count = %#v, want 2", child["scope_count"])
		}
		if child["task_count"] != len(child["tasks"].([]string)) {
			t.Fatalf("child task metadata = %#v, want matching task count and labels", child)
		}
	}
}

func TestWorkflowManagedCalibrationCacheDecaysAfterEarlyRuns(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Prompt: "Analyze assigned scope.",
		Managed: map[string]any{
			"mode":                  "auto",
			"max_items_per_chunk":   1,
			"max_parallel_children": 1,
			"calibration": map[string]any{
				"enabled":            true,
				"sample_size":        2,
				"cache_max_interval": 8,
			},
		},
		Scope: []any{
			map[string]any{"id": "a", "path": "pkg/a.go"},
			map[string]any{"id": "b", "path": "pkg/b.go"},
			map[string]any{"id": "c", "path": "pkg/c.go"},
		},
		Output: contract,
	}
	agent := &AgentInstance{ID: "reviewer", Model: "mock-model"}
	baselineCalls := 0
	runOnce := func(message string, _ bool, _ workflowAgentRunOptions) (string, error) {
		if strings.Contains(message, "Agent execution optimization split calibration.") &&
			strings.Contains(message, "grouped baseline") {
			baselineCalls++
		}
		ids := workflowTestScopeIDs(t, message)
		return workflowManagedTestFindingsJSON(ids), nil
	}

	statuses := make([]string, 0, 4)
	decisions := make([]string, 0, 4)
	for range 4 {
		outputs, err := (&workflowAgentRunner{}).runManagedSplit(
			req,
			agent,
			"reviewer",
			"workflow:test",
			"none",
			"none",
			"",
			"scope_split",
			runOnce,
		)
		if err != nil {
			t.Fatalf("runManagedSplit() error = %v", err)
		}
		calibration := outputs["managed"].(map[string]any)["calibration"].(map[string]any)
		statuses = append(statuses, fmt.Sprint(calibration["status"]))
		cache := calibration["cache"].(map[string]any)
		decisions = append(decisions, fmt.Sprint(cache["decision"]))
	}
	if baselineCalls != 3 {
		t.Fatalf("baseline calls = %d, want 3 with marginal split fit forcing run 3 calibration", baselineCalls)
	}
	if fmt.Sprint(statuses) != "[passed passed passed trusted_cache]" {
		t.Fatalf("calibration statuses = %#v, want passed passed passed trusted_cache", statuses)
	}
	if fmt.Sprint(decisions) != "[miss due due hit]" {
		t.Fatalf("cache decisions = %#v, want miss due due hit", decisions)
	}
}

func TestWorkflowManagedCalibrationCacheSoftReusesModelAndLanguageChanges(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	newReq := func(ext string) workflows.AgentRequest {
		return workflows.AgentRequest{
			Prompt: "Analyze assigned scope.",
			Managed: map[string]any{
				"mode":                  "auto",
				"max_items_per_chunk":   1,
				"max_parallel_children": 1,
				"calibration": map[string]any{
					"enabled":     true,
					"sample_size": 2,
				},
			},
			Scope: []any{
				map[string]any{"id": "a", "path": "pkg/a." + ext},
				map[string]any{"id": "b", "path": "pkg/b." + ext},
				map[string]any{"id": "c", "path": "pkg/c." + ext},
			},
			Output: contract,
		}
	}
	agent := &AgentInstance{ID: "reviewer", Model: "model-a"}
	baselineCalls := 0
	runOnce := func(message string, _ bool, _ workflowAgentRunOptions) (string, error) {
		if strings.Contains(message, "Agent execution optimization split calibration.") &&
			strings.Contains(message, "grouped baseline") {
			baselineCalls++
		}
		ids := workflowTestScopeIDs(t, message)
		return workflowManagedTestFindingsJSON(ids), nil
	}
	run := func(req workflows.AgentRequest) map[string]any {
		t.Helper()
		outputs, err := (&workflowAgentRunner{}).runManagedSplit(
			req,
			agent,
			"reviewer",
			"workflow:test",
			"none",
			"none",
			"",
			"scope_split",
			runOnce,
		)
		if err != nil {
			t.Fatalf("runManagedSplit() error = %v", err)
		}
		return outputs["managed"].(map[string]any)["calibration"].(map[string]any)
	}

	run(newReq("go"))
	run(newReq("go"))
	agent.Model = "model-b"
	modelChanged := run(newReq("go"))
	if modelChanged["status"] != "trusted_cache" {
		t.Fatalf("model-changed calibration = %#v, want borrowed trusted_cache", modelChanged)
	}
	modelCache := modelChanged["cache"].(map[string]any)
	if modelCache["decision"] != "similar_hit" || modelCache["provisional"] != true {
		t.Fatalf("model-changed cache = %#v, want provisional similar_hit", modelCache)
	}
	modelVerified := run(newReq("go"))
	if modelVerified["status"] != "passed" {
		t.Fatalf("model verification calibration = %#v, want passed", modelVerified)
	}
	modelVerifiedCache := modelVerified["cache"].(map[string]any)
	if modelVerifiedCache["decision"] != "borrowed_due" || modelVerifiedCache["provisional"] != false {
		t.Fatalf("model verification cache = %#v, want promoted borrowed_due", modelVerifiedCache)
	}
	agent.Model = "model-a"
	languageChanged := run(newReq("py"))
	if languageChanged["status"] != "trusted_cache" {
		t.Fatalf("language-changed calibration = %#v, want borrowed trusted_cache", languageChanged)
	}
	languageCache := languageChanged["cache"].(map[string]any)
	if languageCache["decision"] != "similar_hit" || languageCache["provisional"] != true {
		t.Fatalf("language-changed cache = %#v, want provisional similar_hit", languageCache)
	}
	if fmt.Sprint(languageCache["languages"]) != "[python]" {
		t.Fatalf("language cache metadata = %#v, want python", languageCache["languages"])
	}
	languageVerified := run(newReq("py"))
	if languageVerified["status"] != "passed" {
		t.Fatalf("language verification calibration = %#v, want passed", languageVerified)
	}
	languageVerifiedCache := languageVerified["cache"].(map[string]any)
	if languageVerifiedCache["decision"] != "borrowed_due" || languageVerifiedCache["provisional"] != false {
		t.Fatalf("language verification cache = %#v, want promoted borrowed_due", languageVerifiedCache)
	}
	if baselineCalls != 4 {
		t.Fatalf("baseline calls = %d, want 4", baselineCalls)
	}
}

func TestWorkflowManagedCalibrationCacheBorrowFailureResetsFresh(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	newReq := func(prompt string) workflows.AgentRequest {
		return workflows.AgentRequest{
			Prompt: prompt,
			Managed: map[string]any{
				"mode":                  "auto",
				"max_items_per_chunk":   1,
				"max_parallel_children": 1,
				"calibration": map[string]any{
					"enabled":              true,
					"sample_size":          2,
					"similarity_threshold": 0.70,
				},
			},
			Scope: []any{
				map[string]any{"id": "a", "path": "pkg/a.go"},
				map[string]any{"id": "b", "path": "pkg/b.go"},
				map[string]any{"id": "c", "path": "pkg/c.go"},
			},
			Output: contract,
		}
	}
	agent := &AgentInstance{ID: "reviewer", Model: "mock-model"}
	baselineCalls := 0
	forceMismatch := false
	runOnce := func(message string, _ bool, _ workflowAgentRunOptions) (string, error) {
		if strings.Contains(message, "Agent execution optimization split calibration.") &&
			strings.Contains(message, "grouped baseline") {
			baselineCalls++
			if forceMismatch {
				return `{"summary":"baseline","findings":[{"scope_id":"baseline-only"}]}`, nil
			}
		}
		ids := workflowTestScopeIDs(t, message)
		return workflowManagedTestFindingsJSON(ids), nil
	}
	run := func(req workflows.AgentRequest) map[string]any {
		t.Helper()
		outputs, err := (&workflowAgentRunner{}).runManagedSplit(
			req,
			agent,
			"reviewer",
			"workflow:test",
			"none",
			"none",
			"",
			"scope_split",
			runOnce,
		)
		if err != nil {
			t.Fatalf("runManagedSplit() error = %v", err)
		}
		return outputs["managed"].(map[string]any)["calibration"].(map[string]any)
	}

	run(newReq("Analyze assigned scope."))
	run(newReq("Analyze assigned scope."))
	borrowed := run(newReq("Analyze assigned scope carefully."))
	borrowedCache := borrowed["cache"].(map[string]any)
	if borrowed["status"] != "trusted_cache" || borrowedCache["decision"] != "similar_hit" {
		t.Fatalf("borrowed calibration = %#v, want similar trusted cache", borrowed)
	}

	forceMismatch = true
	failed := run(newReq("Analyze assigned scope carefully."))
	failedCache := failed["cache"].(map[string]any)
	if failed["status"] != "failed" || failedCache["decision"] != "borrowed_due" {
		t.Fatalf("failed borrowed verification = %#v, want failed borrowed_due", failed)
	}
	if failedCache["trusted"] != false || failedCache["success_streak"] != 0 {
		t.Fatalf("failed borrowed cache = %#v, want reset confidence", failedCache)
	}

	forceMismatch = false
	fresh := run(newReq("Analyze assigned scope carefully."))
	freshCache := fresh["cache"].(map[string]any)
	if fresh["status"] != "passed" || freshCache["decision"] != "previous_not_trusted" {
		t.Fatalf("fresh calibration = %#v, want fresh previous_not_trusted pass", fresh)
	}
	if freshCache["success_streak"] != 1 {
		t.Fatalf("fresh cache = %#v, want success streak 1", freshCache)
	}
	if baselineCalls != 4 {
		t.Fatalf("baseline calls = %d, want 4", baselineCalls)
	}
}

func TestWorkflowManagedCalibrationCacheIntervalDependsOnSplitFit(t *testing.T) {
	if got := workflowManagedCalibrationCacheInterval(5, 16, 0.95); got != 16 {
		t.Fatalf("high-fit interval = %d, want 16", got)
	}
	if got := workflowManagedCalibrationCacheInterval(5, 16, 0.60); got != 8 {
		t.Fatalf("medium-fit interval = %d, want 8", got)
	}
	if got := workflowManagedCalibrationCacheInterval(5, 16, 0.30); got != 1 {
		t.Fatalf("low-fit interval = %d, want 1", got)
	}
}

func TestWorkflowManagedOptimizationSelectsCheaperModelAndEffort(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Prompt: "Analyze assigned scope.",
		Managed: map[string]any{
			"mode":                    "auto",
			"max_items_per_chunk":     1,
			"estimated_output_tokens": 500,
			"calibration": map[string]any{
				"enabled": false,
			},
			"optimization": map[string]any{
				"model": map[string]any{
					"enabled": true,
					"candidates": []any{
						map[string]any{
							"name":                "expensive-model",
							"input_price_per_1m":  5.0,
							"output_price_per_1m": 15.0,
						},
						map[string]any{
							"name":                "cheap-model",
							"input_price_per_1m":  0.1,
							"output_price_per_1m": 0.4,
						},
					},
				},
				"effort": map[string]any{"enabled": true},
			},
		},
		Scope: []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
			map[string]any{"id": "c"},
		},
		Output: contract,
	}
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{Provider: "openai"},
		},
		ModelList: []*config.ModelConfig{
			{
				ModelName:          "expensive-model",
				Provider:           "openai",
				Model:              "openai/expensive-model",
				InputPricePerMTok:  5.0,
				OutputPricePerMTok: 15.0,
			},
			{
				ModelName:          "cheap-model",
				Provider:           "openai",
				Model:              "openai/cheap-model",
				InputPricePerMTok:  0.1,
				OutputPricePerMTok: 0.4,
			},
		},
	}
	agent := &AgentInstance{
		ID:                 "reviewer",
		Model:              "expensive-model",
		Workspace:          t.TempDir(),
		CandidateProviders: map[string]providers.LLMProvider{},
	}
	cheapProtocol, cheapModel := providers.ExtractProtocol(cfg.ModelList[1])
	agent.CandidateProviders[providers.ModelKey(cheapProtocol, cheapModel)] = workflowManagedTestProvider{
		model: "openai/cheap-model",
	}
	var seenMu sync.Mutex
	var seen []workflowAgentRunOptions
	runOnce := func(message string, _ bool, options workflowAgentRunOptions) (string, error) {
		seenMu.Lock()
		seen = append(seen, options)
		seenMu.Unlock()
		ids := workflowTestScopeIDs(t, message)
		findings := make([]string, 0, len(ids))
		for _, id := range ids {
			findings = append(findings, fmt.Sprintf(`{"scope_id":%q}`, id))
		}
		return fmt.Sprintf(`{"summary":%q,"findings":[%s]}`, strings.Join(ids, ","), strings.Join(findings, ",")), nil
	}

	outputs, err := (&workflowAgentRunner{loop: &AgentLoop{cfg: cfg}}).runManagedSplit(
		req,
		agent,
		"reviewer",
		"workflow:test",
		"none",
		"none",
		"",
		"scope_split",
		runOnce,
	)
	if err != nil {
		t.Fatalf("runManagedSplit() error = %v", err)
	}
	if len(seen) != 3 {
		t.Fatalf("run count = %d, want 3", len(seen))
	}
	for _, options := range seen {
		if options.ModelName != "cheap-model" {
			t.Fatalf("ModelName = %q, want cheap-model", options.ModelName)
		}
		if options.ReasoningEffort == "" {
			t.Fatalf("ReasoningEffort is empty, want optimized effort")
		}
	}
	managed := outputs["managed"].(map[string]any)
	optimization := managed["optimization"].(map[string]any)
	model := optimization["model"].(map[string]any)
	if model["changed"] != true {
		t.Fatalf("model optimization = %#v, want changed", model)
	}
	cost := optimization["cost"].(map[string]any)
	savings, _ := cost["estimated_savings_usd"].(float64)
	if savings <= 0 {
		t.Fatalf("estimated savings = %#v, want positive", cost["estimated_savings_usd"])
	}
}

func TestWorkflowManagedOptimizationSkipsCheaperModelWithoutProvider(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{Provider: "openai"},
		},
		ModelList: []*config.ModelConfig{
			{
				ModelName:          "default-model",
				Provider:           "openai",
				Model:              "openai/default-model",
				InputPricePerMTok:  5.0,
				OutputPricePerMTok: 15.0,
			},
			{
				ModelName:          "cheap-model",
				Provider:           "openai",
				Model:              "openai/cheap-model",
				InputPricePerMTok:  0.1,
				OutputPricePerMTok: 0.4,
			},
		},
	}
	agent := &AgentInstance{
		ID:                 "reviewer",
		Model:              "default-model",
		Workspace:          t.TempDir(),
		CandidateProviders: map[string]providers.LLMProvider{},
	}
	options := workflowManagedOptions(map[string]any{
		"optimization": map[string]any{
			"model": map[string]any{
				"enabled":    true,
				"candidates": []any{"cheap-model"},
			},
		},
	})

	choice := workflowManagedRunChoice(
		workflows.AgentRequest{Prompt: "Analyze."},
		agent,
		cfg,
		options,
		"scope_split",
		workflowManagedChildPlan{index: 1, scope: []any{map[string]any{"id": "a"}}, tasks: []string{"review"}},
	)
	if choice.modelName != "default-model" {
		t.Fatalf("modelName = %q, want default-model", choice.modelName)
	}
	if changed, _ := choice.modelMeta["changed"].(bool); changed {
		t.Fatalf("model metadata = %#v, want unchanged without candidate provider", choice.modelMeta)
	}
}

func TestWorkflowManagedCalibrationMismatchFallsBackToSingleRun(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Prompt: "Analyze assigned scope.",
		Managed: map[string]any{
			"mode":                  "auto",
			"max_items_per_chunk":   1,
			"max_parallel_children": 1,
			"calibration": map[string]any{
				"enabled":     true,
				"sample_size": 2,
			},
		},
		Scope: []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
			map[string]any{"id": "c"},
		},
		Output: contract,
	}
	calls := 0
	runOnce := func(message string, _ bool, _ workflowAgentRunOptions) (string, error) {
		calls++
		if strings.Contains(message, "Agent execution optimization split calibration.") {
			if strings.Contains(message, "grouped baseline") {
				return `{"summary":"baseline","findings":[{"scope_id":"baseline"}]}`, nil
			}
			ids := workflowTestScopeIDs(t, message)
			return workflowManagedTestFindingsJSON(ids), nil
		}
		ids := workflowTestScopeIDs(t, message)
		return workflowManagedTestFindingsJSON(ids), nil
	}

	outputs, err := (&workflowAgentRunner{}).runManagedSplit(
		req,
		&AgentInstance{ID: "reviewer", Model: "mock-model"},
		"reviewer",
		"workflow:test",
		"none",
		"none",
		"",
		"scope_split",
		runOnce,
	)
	if err != nil {
		t.Fatalf("runManagedSplit() error = %v", err)
	}
	if calls != 4 {
		t.Fatalf("run count = %d, want 4", calls)
	}
	if _, exists := outputs["managed_children"]; exists {
		t.Fatalf("managed_children present after calibration fallback: %#v", outputs["managed_children"])
	}
	structured := outputs["structured"].(map[string]any)
	findings := structured["findings"].([]any)
	if len(findings) != 3 {
		t.Fatalf("fallback findings = %#v, want full-scope result", findings)
	}
	managed := outputs["managed"].(map[string]any)
	calibration := managed["calibration"].(map[string]any)
	if calibration["status"] != "failed" || calibration["phase"] != "compare" || calibration["match"] != false {
		t.Fatalf("calibration = %#v, want failed compare mismatch", calibration)
	}
}

func TestWorkflowManagedCalibrationSampleExpandsAndCatchesMismatch(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Prompt: "Analyze assigned scope.",
		Managed: map[string]any{
			"mode":                  "auto",
			"max_items_per_chunk":   3,
			"max_parallel_children": 1,
			"calibration": map[string]any{
				"enabled":     true,
				"sample_size": 1,
			},
		},
		Scope: []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
			map[string]any{"id": "c"},
			map[string]any{"id": "d"},
		},
		Output: contract,
	}
	baselineCalls := 0
	runOnce := func(message string, _ bool, _ workflowAgentRunOptions) (string, error) {
		if strings.Contains(message, "Agent execution optimization split calibration.") {
			if strings.Contains(message, "grouped baseline") {
				baselineCalls++
				return `{"summary":"baseline","findings":[{"scope_id":"baseline-only"}]}`, nil
			}
			ids := workflowTestScopeIDs(t, message)
			return workflowManagedTestFindingsJSON(ids), nil
		}
		ids := workflowTestScopeIDs(t, message)
		return workflowManagedTestFindingsJSON(ids), nil
	}

	outputs, err := (&workflowAgentRunner{}).runManagedSplit(
		req,
		&AgentInstance{ID: "reviewer", Model: "mock-model"},
		"reviewer",
		"workflow:test",
		"none",
		"none",
		"",
		"scope_split",
		runOnce,
	)
	if err != nil {
		t.Fatalf("runManagedSplit() error = %v", err)
	}
	if baselineCalls != 1 {
		t.Fatalf("baseline calls = %d, want 1", baselineCalls)
	}
	if _, exists := outputs["managed_children"]; exists {
		t.Fatalf("managed_children present after calibration fallback: %#v", outputs["managed_children"])
	}
	calibration := outputs["managed"].(map[string]any)["calibration"].(map[string]any)
	if calibration["status"] != "failed" || calibration["phase"] != "compare" {
		t.Fatalf("calibration = %#v, want failed compare mismatch", calibration)
	}
	if calibration["sample_scope"] != 4 {
		t.Fatalf("sample_scope = %#v, want expanded sample of 4", calibration["sample_scope"])
	}
}

func TestWorkflowManagedChildInvalidOutputReturnsDiagnostics(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Prompt: "Analyze assigned scope.",
		Managed: map[string]any{
			"mode":                  "auto",
			"max_items_per_chunk":   1,
			"max_parallel_children": 1,
			"calibration":           map[string]any{"enabled": false},
		},
		Scope: []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
		},
		Output: contract,
	}
	runOnce := func(message string, _ bool, _ workflowAgentRunOptions) (string, error) {
		if strings.Contains(message, "Your previous response did not satisfy") {
			return `{"summary":"still missing findings"}`, nil
		}
		ids := workflowTestScopeIDs(t, message)
		if len(ids) == 1 && ids[0] == "b" {
			return `{"summary":"missing findings"}`, nil
		}
		return workflowManagedTestFindingsJSON(ids), nil
	}

	outputs, err := (&workflowAgentRunner{}).runManagedSplit(
		req,
		&AgentInstance{ID: "reviewer", Model: "mock-model"},
		"reviewer",
		"workflow:test",
		"none",
		"none",
		"",
		"scope_split",
		runOnce,
	)
	if err == nil {
		t.Fatal("runManagedSplit() error = nil, want child validation error")
	}
	children := outputs["managed_children"].([]map[string]any)
	if len(children) != 2 {
		t.Fatalf("managed_children len = %d, want 2", len(children))
	}
	invalid := 0
	for _, child := range children {
		if child["valid"] == false {
			invalid++
			if child["error"] == "" || child["run_error"] == "" {
				t.Fatalf("invalid child diagnostics = %#v, want error and run_error", child)
			}
		}
	}
	if invalid != 1 {
		t.Fatalf("invalid child count = %d, want 1; children=%#v", invalid, children)
	}
}

func TestWorkflowManagedChildRepairsInvalidJSONAndAggregatesRepairs(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Prompt: "Analyze assigned scope.",
		Managed: map[string]any{
			"mode":                  "auto",
			"max_items_per_chunk":   1,
			"max_parallel_children": 1,
			"calibration":           map[string]any{"enabled": false},
		},
		Scope: []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
		},
		Output: contract,
	}
	repairs := 0
	runOnce := func(message string, _ bool, _ workflowAgentRunOptions) (string, error) {
		if strings.Contains(message, "Your previous response did not satisfy") {
			repairs++
			id := fmt.Sprintf("repaired-%d", repairs)
			return workflowManagedTestFindingsJSON([]string{id}), nil
		}
		return "not json", nil
	}

	outputs, err := (&workflowAgentRunner{}).runManagedSplit(
		req,
		&AgentInstance{ID: "reviewer", Model: "mock-model"},
		"reviewer",
		"workflow:test",
		"none",
		"none",
		"",
		"scope_split",
		runOnce,
	)
	if err != nil {
		t.Fatalf("runManagedSplit() error = %v", err)
	}
	if outputs["structured_repairs"] != 2 {
		t.Fatalf("structured_repairs = %#v, want 2", outputs["structured_repairs"])
	}
	structured := outputs["structured"].(map[string]any)
	findings := structured["findings"].([]any)
	if len(findings) != 2 {
		t.Fatalf("findings = %#v, want two repaired findings", findings)
	}
}

func TestWorkflowManagedCombinedOutputIsValidated(t *testing.T) {
	contract := &workflows.AgentOutputContract{
		Format:         "json",
		RepairAttempts: 1,
		Schema: map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"scope_id": map[string]any{"type": "string"},
				},
			},
		},
	}
	req := workflows.AgentRequest{
		Prompt: "Analyze assigned scope.",
		Managed: map[string]any{
			"mode":                "auto",
			"max_items_per_chunk": 1,
			"calibration":         map[string]any{"enabled": false},
		},
		Scope: []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
		},
		Output: contract,
	}
	runOnce := func(message string, _ bool, _ workflowAgentRunOptions) (string, error) {
		ids := workflowTestScopeIDs(t, message)
		return fmt.Sprintf(`[{"scope_id":%q}]`, ids[0]), nil
	}

	outputs, err := (&workflowAgentRunner{}).runManagedSplit(
		req,
		&AgentInstance{ID: "reviewer", Model: "mock-model"},
		"reviewer",
		"workflow:test",
		"none",
		"none",
		"",
		"scope_split",
		runOnce,
	)
	if err == nil {
		t.Fatal("runManagedSplit() error = nil, want combined schema validation error")
	}
	if outputs["structured_valid"] != false {
		t.Fatalf("structured_valid = %#v, want false", outputs["structured_valid"])
	}
	if outputs["structured_error"] == "" {
		t.Fatalf("structured_error is empty in outputs %#v", outputs)
	}
}

func TestWorkflowManagedProviderInitializationRegistersCandidate(t *testing.T) {
	cfg := &config.Config{
		ModelList: []*config.ModelConfig{
			{
				ModelName: "default-model",
				Provider:  "openai",
				Model:     "openai/default-model",
			},
			{
				ModelName: "cheap-model",
				Provider:  "openai",
				Model:     "openai/cheap-model",
			},
		},
	}
	agent := &AgentInstance{
		ID:        "reviewer",
		Model:     "default-model",
		Workspace: t.TempDir(),
	}
	var created []string
	runner := &workflowAgentRunner{loop: &AgentLoop{
		cfg: cfg,
		providerFactory: func(mc *config.ModelConfig) (providers.LLMProvider, string, error) {
			created = append(created, mc.ModelName)
			return workflowManagedTestProvider{model: mc.Model}, mc.Model, nil
		},
	}}
	raw := map[string]any{
		"optimization": map[string]any{
			"model": map[string]any{
				"enabled":    true,
				"candidates": []any{"cheap-model"},
			},
		},
	}

	if err := runner.ensureWorkflowManagedProviders(agent, raw); err != nil {
		t.Fatalf("ensureWorkflowManagedProviders() error = %v", err)
	}
	if len(created) != 1 || created[0] != "cheap-model" {
		t.Fatalf("created providers = %#v, want cheap-model once", created)
	}
	protocol, modelID := providers.ExtractProtocol(cfg.ModelList[1])
	if agent.CandidateProviders[providers.ModelKey(protocol, modelID)] == nil {
		t.Fatalf("candidate provider for %s/%s not registered: %#v", protocol, modelID, agent.CandidateProviders)
	}
	if err := runner.ensureWorkflowManagedProviders(agent, raw); err != nil {
		t.Fatalf("second ensureWorkflowManagedProviders() error = %v", err)
	}
	if len(created) != 1 {
		t.Fatalf("created providers after second call = %#v, want no duplicate", created)
	}
}

func TestWorkflowManagedProviderInitializationReportsCandidateFailures(t *testing.T) {
	runner := &workflowAgentRunner{loop: &AgentLoop{cfg: &config.Config{}}}
	err := runner.ensureWorkflowManagedProviders(&AgentInstance{Model: "default-model"}, map[string]any{
		"optimization": map[string]any{
			"model": map[string]any{
				"enabled":    true,
				"candidates": []any{"missing-model"},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "missing-model") {
		t.Fatalf("ensureWorkflowManagedProviders() error = %v, want missing-model failure", err)
	}
}

func TestWorkflowManagedModelCandidateMapUsesModelFallback(t *testing.T) {
	candidates := parseWorkflowManagedModelCandidates([]any{
		map[string]any{
			"model":              "cheap-model",
			"input_price_per_1m": "0.25",
			"subscription":       true,
		},
	})
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want one candidate", candidates)
	}
	candidate := candidates[0]
	if candidate.name != "cheap-model" {
		t.Fatalf("candidate name = %q, want cheap-model", candidate.name)
	}
	if candidate.equivalentModelName != "" {
		t.Fatalf("equivalent model = %q, want empty", candidate.equivalentModelName)
	}
	if !candidate.priceKnown || candidate.inputPricePerMTok != 0.25 {
		t.Fatalf("candidate pricing = %#v, want known input price", candidate)
	}
	if !candidate.subscription {
		t.Fatal("candidate subscription = false, want true")
	}
}

func TestWorkflowManagedModelProfileUsesSubscriptionEquivalentPricing(t *testing.T) {
	cfg := &config.Config{
		ModelList: []*config.ModelConfig{
			{
				ModelName:                   "subscription-model",
				Provider:                    "openai",
				Model:                       "openai/subscription-model",
				Subscription:                true,
				SubscriptionEquivalentModel: "metered-model",
			},
			{
				ModelName:          "metered-model",
				Provider:           "openai",
				Model:              "openai/metered-model",
				InputPricePerMTok:  2.5,
				OutputPricePerMTok: 10,
			},
		},
	}

	profile := workflowModelCandidateProfile(cfg, "subscription-model")
	if !profile.subscription || !profile.priceKnown {
		t.Fatalf("profile = %#v, want subscription with known equivalent price", profile)
	}
	if profile.inputPricePerMTok != 2.5 || profile.outputPricePerMTok != 10 {
		t.Fatalf(
			"profile prices = (%v, %v), want equivalent model prices",
			profile.inputPricePerMTok,
			profile.outputPricePerMTok,
		)
	}
	if profile.source != "subscription_equivalent_model_config" {
		t.Fatalf("profile source = %q, want subscription_equivalent_model_config", profile.source)
	}
}

func TestWorkflowManagedModeNormalization(t *testing.T) {
	tests := []struct {
		raw  any
		want string
	}{
		{raw: nil, want: "off"},
		{raw: false, want: "off"},
		{raw: true, want: "auto"},
		{raw: "none", want: "off"},
		{raw: "TRUE", want: "auto"},
		{raw: "custom", want: "custom"},
		{raw: map[string]any{"enabled": false}, want: "off"},
		{raw: map[string]any{"enabled": false, "mode": "task_split"}, want: "off"},
		{raw: map[string]any{"mode": "task_split"}, want: "task_split"},
		{raw: map[string]any{"max_items_per_chunk": 1}, want: "auto"},
	}
	for _, tt := range tests {
		if got := workflowManagedMode(tt.raw); got != tt.want {
			t.Fatalf("workflowManagedMode(%#v) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}

func TestWorkflowManagedScopeItemsAndPlanPreserveObjectWrapper(t *testing.T) {
	scope := map[string]any{
		"kind":  "files",
		"limit": 2,
		"items": []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
		},
	}
	items := workflowScopeItems(scope)
	if len(items) != 2 {
		t.Fatalf("workflowScopeItems() = %#v, want two items", items)
	}

	planned := workflowScopeForPlan(scope, []any{map[string]any{"id": "a"}})
	mapped, ok := planned.(map[string]any)
	if !ok {
		t.Fatalf("planned scope = %#v, want map wrapper", planned)
	}
	if mapped["kind"] != "files" || mapped["limit"] != 2 {
		t.Fatalf("planned scope metadata = %#v, want original wrapper metadata", mapped)
	}
	plannedItems, ok := mapped["items"].([]any)
	if !ok || len(plannedItems) != 1 {
		t.Fatalf("planned items = %#v, want one scoped item", mapped["items"])
	}
}

func workflowManagedTestOutputContract() *workflows.AgentOutputContract {
	return &workflows.AgentOutputContract{
		Format:         "json",
		RepairAttempts: 1,
		Schema: map[string]any{
			"type":     "object",
			"required": []any{"summary", "findings"},
			"properties": map[string]any{
				"summary":  map[string]any{"type": "string"},
				"findings": map[string]any{"type": "array"},
			},
		},
	}
}

type workflowManagedTestProvider struct {
	model string
}

func (p workflowManagedTestProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{Content: "{}"}, nil
}

func (p workflowManagedTestProvider) GetDefaultModel() string {
	return p.model
}

func workflowTestScopeIDs(t *testing.T, message string) []string {
	t.Helper()
	_, parsed, err := workflows.ExtractJSONValue(message)
	if err != nil {
		t.Fatalf("extract assigned scope from message: %v\n%s", err, message)
	}
	items, ok := parsed.([]any)
	if !ok {
		t.Fatalf("assigned scope = %#v, want array", parsed)
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		mapped, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("scope item = %#v, want object", item)
		}
		ids = append(ids, fmt.Sprint(mapped["id"]))
	}
	return ids
}

func workflowManagedTestFindingsJSON(ids []string) string {
	findings := make([]string, 0, len(ids))
	for _, id := range ids {
		findings = append(findings, fmt.Sprintf(`{"scope_id":%q,"title":"finding %s"}`, id, id))
	}
	return fmt.Sprintf(`{"summary":%q,"findings":[%s]}`, strings.Join(ids, ","), strings.Join(findings, ","))
}

func workflowTestAssignedTasks(t *testing.T, message string) []string {
	t.Helper()
	tasks := workflowAssignedTasks(workflows.AgentRequest{Context: message})
	if len(tasks) == 0 {
		t.Fatalf("assigned tasks not found in message:\n%s", message)
	}
	return tasks
}

func TestWorkflowToolRunnerDeliversHandledMedia(t *testing.T) {
	store := media.NewFileMediaStore()
	path := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(path, []byte("workflow report"), 0o644); err != nil {
		t.Fatal(err)
	}
	ref, err := store.Store(path, media.MediaMeta{
		Filename:    "report.txt",
		ContentType: "text/plain",
		Source:      "test:workflow",
	}, "test:workflow")
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewToolRegistry()
	registry.Register(&workflowHandledMediaTool{ref: ref})
	manager := &workflowMediaChannelManager{}
	msgBus := bus.NewMessageBus()
	defer msgBus.Close()
	loop := &AgentLoop{
		bus:            msgBus,
		channelManager: manager,
		mediaStore:     store,
	}

	outputs, err := (&workflowToolRunner{
		agentID:  "main",
		registry: registry,
		loop:     loop,
	}).RunTool(context.Background(), workflows.ToolRequest{
		Name:    "workflow_handled_media",
		Session: "workflow:session",
		Delivery: workflows.Delivery{
			Channel:          "telegram",
			ChatID:           "chat1",
			TopicID:          "42",
			MessageID:        "m1",
			ReplyToMessageID: "m1",
		},
	})
	if err != nil {
		t.Fatalf("RunTool failed: %v", err)
	}
	if outputs["response_handled"] != true {
		t.Fatalf("outputs = %#v, want response_handled=true", outputs)
	}
	if len(manager.sentMedia) != 1 {
		t.Fatalf("sent media = %d, want 1", len(manager.sentMedia))
	}
	got := manager.sentMedia[0]
	if got.Channel != "telegram" || got.ChatID != "chat1" {
		t.Fatalf("target = %#v", got)
	}
	if got.Context.TopicID != "42" || got.Context.ReplyToMessageID != "m1" {
		t.Fatalf("context = %#v", got.Context)
	}
	if len(got.Parts) != 1 || got.Parts[0].Ref != ref || got.Parts[0].Type != "file" {
		t.Fatalf("parts = %#v", got.Parts)
	}
}

func TestWorkflowToolResultOutputsExposesJSONFields(t *testing.T) {
	outputs := workflowToolResultOutputs(tools.SilentResult(`{
  "workspace": {
    "id": "gw-test",
    "path": "/tmp/repo"
  },
  "next": "inspect path"
}`))

	workspace, ok := outputs["workspace"].(map[string]any)
	if !ok {
		t.Fatalf("workspace output = %#v, want parsed object", outputs["workspace"])
	}
	if workspace["path"] != "/tmp/repo" {
		t.Fatalf("workspace.path = %#v, want /tmp/repo", workspace["path"])
	}
	jsonOutput, ok := outputs["json"].(map[string]any)
	if !ok || jsonOutput["next"] != "inspect path" {
		t.Fatalf("json output = %#v, want parsed tool JSON", outputs["json"])
	}
	if outputs["text"] == "" {
		t.Fatalf("text output should preserve original content: %#v", outputs)
	}
}

type workflowHandledMediaTool struct {
	ref string
}

func (t *workflowHandledMediaTool) Name() string { return "workflow_handled_media" }

func (t *workflowHandledMediaTool) Description() string { return "returns handled media" }

func (t *workflowHandledMediaTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (t *workflowHandledMediaTool) Execute(context.Context, map[string]any) *tools.ToolResult {
	return tools.MediaResult("Attachment delivered.", []string{t.ref}).WithResponseHandled()
}

type workflowMediaChannelManager struct {
	sentMedia []bus.OutboundMediaMessage
}

func (m *workflowMediaChannelManager) GetChannel(string) (channels.Channel, bool) { return nil, false }

func (m *workflowMediaChannelManager) GetEnabledChannels() []string { return nil }

func (m *workflowMediaChannelManager) InvokeTypingStop(string, string) {}

func (m *workflowMediaChannelManager) SendMessage(context.Context, bus.OutboundMessage) error {
	return nil
}

func (m *workflowMediaChannelManager) SendMedia(_ context.Context, msg bus.OutboundMediaMessage) error {
	m.sentMedia = append(m.sentMedia, msg)
	return nil
}

func (m *workflowMediaChannelManager) SendPlaceholder(context.Context, string, string) bool {
	return false
}

func (m *workflowMediaChannelManager) DismissToolFeedback(context.Context, string, string, *bus.InboundContext) {
}
