package tools

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sipeed/picoclaw/pkg/config"
)

type miscCloseoutTool struct {
	name string
}

func (tool *miscCloseoutTool) Name() string               { return tool.name }
func (tool *miscCloseoutTool) Description() string        { return "closeout" }
func (tool *miscCloseoutTool) Parameters() map[string]any { return map[string]any{"type": "object"} }

func (tool *miscCloseoutTool) Execute(context.Context, map[string]any) *ToolResult {
	return NewToolResult("ok")
}

type miscCloseoutSpawner struct {
	result *ToolResult
	err    error
}

func (spawner miscCloseoutSpawner) SpawnSubTurn(context.Context, SubTurnConfig) (*ToolResult, error) {
	return spawner.result, spawner.err
}

func TestIntegrationFacadeCloseoutConstructors(t *testing.T) {
	if NewMCPTool(nil, "server", &mcp.Tool{Name: "tool"}) == nil {
		t.Fatal("MCP facade returned nil")
	}
	if NewFindSkillsTool(nil, nil) == nil || NewInstallSkillTool(nil, t.TempDir()) == nil {
		t.Fatal("skills facade returned nil")
	}
	if NewReactionTool() == nil || NewSendTTSTool(nil, nil) == nil {
		t.Fatal("delivery facade returned nil")
	}
	if NewAPIKeyPool([]string{"key"}) == nil {
		t.Fatal("API key pool facade returned nil")
	}
	opts := WebSearchToolOptionsFromConfig(&config.Config{})
	_ = WebSearchProviderReady(opts, "unknown")
	_, _ = ResolveWebSearchProviderName(opts, "query")
	_, _ = NewWebSearchTool(opts)
	if tool, err := NewWebFetchTool(0, "markdown", 0); err != nil || tool == nil {
		t.Fatalf("web fetch facade = %#v, %v", tool, err)
	}
	if tool, err := NewWebFetchToolWithProxy(1, "", "text", 1, nil); err != nil || tool == nil {
		t.Fatalf("proxy web fetch facade = %#v, %v", tool, err)
	}
	if tool, err := NewWebFetchToolWithConfig(1, "", "text", 1, nil); err != nil || tool == nil {
		t.Fatalf("configured web fetch facade = %#v, %v", tool, err)
	}
	if NewGitWorkspaceTool(nil) == nil {
		t.Fatal("Git workspace facade returned nil")
	}
}

func TestValidateToolArgsCloseoutBranches(t *testing.T) {
	if err := validateToolArgs(map[string]any{"properties": "invalid"}, nil); err != nil {
		t.Fatalf("invalid properties compatibility = %v", err)
	}
	if err := validateToolArgs(
		map[string]any{
			"properties":           map[string]any{},
			"additionalProperties": true,
		},
		map[string]any{"extra": true},
	); err != nil {
		t.Fatalf("allowed additional property = %v", err)
	}
	if err := validateToolArgs(
		map[string]any{
			"properties": map[string]any{"opaque": "invalid"},
		},
		map[string]any{"opaque": true},
	); err != nil {
		t.Fatalf("opaque property schema = %v", err)
	}
	if err := checkRequired(
		map[string]any{"required": []any{1, "present"}},
		map[string]any{"present": true},
	); err != nil {
		t.Fatalf("mixed required compatibility = %v", err)
	}
	if err := checkRequired(
		map[string]any{"required": "invalid"}, map[string]any{},
	); err != nil {
		t.Fatalf("opaque required compatibility = %v", err)
	}
	if err := checkType("value", true, map[string]any{}); err != nil {
		t.Fatalf("missing type compatibility = %v", err)
	}
	if err := checkType("value", true, map[string]any{"type": 7}); err != nil {
		t.Fatalf("non-string type compatibility = %v", err)
	}
	for _, value := range []any{
		json.Number("1.5"),
		json.Number("invalid"),
		math.NaN(),
		math.Inf(1),
		struct{}{},
	} {
		kind := "number"
		if number, ok := value.(json.Number); ok && number.String() == "1.5" {
			kind = "integer"
		}
		if err := checkType("value", value, map[string]any{"type": kind}); err == nil {
			t.Fatalf("invalid %s value %#v succeeded", kind, value)
		}
	}
	if err := checkType("value", "false", map[string]any{"type": "boolean"}); err == nil {
		t.Fatal("string boolean succeeded")
	}
	if err := checkType("value", "array", map[string]any{"type": "array"}); err == nil {
		t.Fatal("string array succeeded")
	}
	if err := checkType("value", "object", map[string]any{"type": "object"}); err == nil {
		t.Fatal("string object succeeded")
	}
	if err := checkType(
		"value",
		map[string]any{},
		map[string]any{
			"type":     "object",
			"required": []string{"nested"},
		},
	); err == nil {
		t.Fatal("invalid nested object succeeded")
	}
	if err := checkArrayItems("items", []any{true}, map[string]any{}); err != nil {
		t.Fatalf("missing items compatibility = %v", err)
	}
	if err := checkArrayItems(
		"items", []any{true}, map[string]any{"items": "invalid"},
	); err != nil {
		t.Fatalf("opaque items compatibility = %v", err)
	}
	if err := checkArrayItems(
		"items", []any{"wrong"},
		map[string]any{"items": map[string]any{"type": "boolean"}},
	); err == nil {
		t.Fatal("invalid array item succeeded")
	}
	if jsonExponentAtLeast("999999999999999999", 1, 2) != true ||
		jsonExponentAtLeast("-999999999999999999", -1, 2) != false {
		t.Fatal("saturated JSON exponent comparison failed")
	}
	if !jsonExponentAtLeast("+2", 2, 3) {
		t.Fatal("positive JSON exponent comparison failed")
	}
	if err := checkEnum(
		"enum", 7, map[string]any{"enum": []string{"one"}},
	); err == nil {
		t.Fatal("non-string string-enum value succeeded")
	}
	if err := checkEnum("enum", "value", map[string]any{"enum": 7}); err != nil {
		t.Fatalf("opaque enum compatibility = %v", err)
	}
}

func TestRegistryCloseoutRemainingBranches(t *testing.T) {
	if schema := (CoreToolSnapshotEntry{}).ParameterSchema(); schema != nil {
		t.Fatalf("nil snapshot schema = %#v", schema)
	}
	live := &miscCloseoutTool{name: "live"}
	if schema := (CoreToolSnapshotEntry{Tool: live}).ParameterSchema(); schema == nil {
		t.Fatal("live snapshot schema is nil")
	}
	registry := NewToolRegistry()
	registry.Unregister("")
	registry.Unregister("missing")
	registry.RegisterHidden(&miscCloseoutTool{name: "hidden"})
	if definitions := registry.GetDefinitions(); len(definitions) != 0 {
		t.Fatalf("hidden definitions = %#v", definitions)
	}
	if providerDefs := registry.ToProviderDefs(); len(providerDefs) != 0 {
		t.Fatalf("hidden provider definitions = %#v", providerDefs)
	}
	if summaries := registry.GetSummaries(); len(summaries) != 0 {
		t.Fatalf("hidden summaries = %#v", summaries)
	}
	registry.SetAllowlist([]string{"", " LIVE "})
	registry.exactRegistrationCap = map[string]struct{}{"live": {}}
	clone := registry.Clone()
	if clone.allowlist == nil || clone.exactRegistrationCap == nil {
		t.Fatal("clone omitted frozen registration policies")
	}
	registry.closed = true
	registry.SetAllowlist(nil)
	registry.Unregister("hidden")
	registry.SetMediaStore(nil)
	registry.PromoteTools([]string{"hidden"}, 3)
	registry.TickTTL()
	registry.closed = false
	registry.tools["nil-core"] = &ToolEntry{IsCore: true}
	visited := false
	if err := registry.VisitCoreTools(context.Background(), func(CoreToolSnapshotEntry) bool {
		visited = true
		return true
	}); err != nil {
		t.Fatalf("nil core registry visit = %v", err)
	}
	if visited {
		t.Fatal("nil core entry was visited")
	}
}

func TestSubagentCloseoutRemainingBranches(t *testing.T) {
	manager := NewSubagentManager(nil, "model", t.TempDir())
	registry := NewToolRegistry()
	manager.SetTools(registry)
	manager.RegisterTool(&miscCloseoutTool{name: "registered"})
	if _, err := (*SubagentManager)(nil).spawnTracked(
		context.Background(), "task", "label", "agent", subagentTaskOrigin{},
		func(context.Context, SubagentTask) (*ToolResult, error) { return NewToolResult("ok"), nil },
		nil, nil,
	); err == nil {
		t.Fatal("nil manager spawn succeeded")
	}
	if _, err := manager.spawnTracked(
		context.Background(), "task", "label", "agent", subagentTaskOrigin{}, nil, nil, nil,
	); err == nil {
		t.Fatal("nil runner spawn succeeded")
	}
	if runner := (*SubagentManager)(nil).legacyTaskRunnerSnapshot(); runner != nil {
		t.Fatal("nil manager legacy runner is non-nil")
	}
	if _, err := (*SubagentManager)(nil).Spawn(
		nil, "task", "label", "agent", "channel", "chat", nil,
	); err == nil {
		t.Fatal("nil manager Spawn succeeded")
	}
	done := make(chan struct{})
	if _, err := manager.spawnTracked(
		nil,
		"task",
		"label",
		"agent",
		subagentTaskOrigin{},
		func(context.Context, SubagentTask) (*ToolResult, error) {
			return NewToolResult("ok"), nil
		},
		nil,
		func() { close(done) },
	); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for closeout subagent completion")
	}
	if task, ok := manager.GetTask("missing"); ok || task != nil {
		t.Fatalf("missing task = %#v, %t", task, ok)
	}

	tool := NewSubagentTool(manager)
	injected := errors.New("spawn failed")
	tool.SetSpawner(miscCloseoutSpawner{err: injected})
	if result := tool.Execute(
		context.Background(), map[string]any{"task": "task"},
	); result == nil || !result.IsError || !errors.Is(result.Err, injected) {
		t.Fatalf("failed subagent result = %#v", result)
	}
	tool.SetSpawner(miscCloseoutSpawner{result: &ToolResult{
		ForLLM:  strings.Repeat("l", 600),
		ForUser: strings.Repeat("u", 600),
	}})
	result := tool.Execute(context.Background(), map[string]any{"task": "task"})
	if result == nil || result.IsError || len(result.ForUser) != 503 {
		t.Fatalf("truncated subagent result = %#v", result)
	}
}
