package tools

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type invocationRecordingTool struct {
	name        string
	description string
	parameters  map[string]any
	output      string

	calls atomic.Int64
	mu    sync.Mutex
	args  map[string]any
}

type invocationAsyncTool struct {
	*invocationRecordingTool
	asyncCalls atomic.Int64
}

func (tool *invocationAsyncTool) ExecuteAsync(
	ctx context.Context,
	args map[string]any,
	callback AsyncCallback,
) *ToolResult {
	tool.asyncCalls.Add(1)
	if callback != nil {
		callback(ctx, SilentResult("async-complete"))
	}
	return &ToolResult{ForLLM: "async-started", Async: true, Silent: true}
}

type invocationNonComparableTool struct {
	name   string
	marker []string
	calls  *atomic.Int64
}

func (tool invocationNonComparableTool) Name() string        { return tool.name }
func (tool invocationNonComparableTool) Description() string { return "non-comparable tool" }
func (tool invocationNonComparableTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (tool invocationNonComparableTool) Execute(context.Context, map[string]any) *ToolResult {
	tool.calls.Add(1)
	return SilentResult("non-comparable")
}

func (tool *invocationRecordingTool) Name() string { return tool.name }

func (tool *invocationRecordingTool) Description() string { return tool.description }

func (tool *invocationRecordingTool) Parameters() map[string]any { return tool.parameters }

func (tool *invocationRecordingTool) Execute(_ context.Context, args map[string]any) *ToolResult {
	tool.calls.Add(1)
	detached, _ := DetachToolArguments(args)
	tool.mu.Lock()
	tool.args = detached
	tool.mu.Unlock()
	return SilentResult(tool.output)
}

func (tool *invocationRecordingTool) arguments() map[string]any {
	tool.mu.Lock()
	defer tool.mu.Unlock()
	detached, _ := DetachToolArguments(tool.args)
	return detached
}

func newInvocationRecordingTool(
	name string,
	parameters map[string]any,
	output string,
) *invocationRecordingTool {
	return &invocationRecordingTool{
		name:        name,
		description: name + " description",
		parameters:  parameters,
		output:      output,
	}
}

func newInvocationTestRegistry(t *testing.T) *ToolRegistry {
	t.Helper()
	registry := NewToolRegistry()
	t.Cleanup(func() {
		if err := registry.Close(); err != nil {
			t.Errorf("ToolRegistry.Close() error = %v", err)
		}
	})
	return registry
}

func registerInvocationFactoryBacked(
	t *testing.T,
	registry *ToolRegistry,
	tool *invocationRecordingTool,
	traits ToolTraits,
) {
	t.Helper()
	factory, err := NewToolFactoryFromPrototype(
		tool,
		traits,
		func(ToolBuildContext) (Tool, error) {
			parameters, cloneErr := cloneToolSchemaMap(tool.parameters)
			if cloneErr != nil {
				return nil, cloneErr
			}
			return newInvocationRecordingTool(tool.name, parameters, tool.output), nil
		},
	)
	if err != nil {
		t.Fatalf("NewToolFactoryFromPrototype() error = %v", err)
	}
	if err := registry.RegisterFactoryBacked(tool, factory); err != nil {
		t.Fatalf("RegisterFactoryBacked() error = %v", err)
	}
}

func prepareInvocationForTest(
	t *testing.T,
	registry *ToolRegistry,
	name string,
	arguments map[string]any,
) (*ModelToolCatalog, *PreparedToolInvocation) {
	t.Helper()
	catalog, err := registry.SnapshotModelToolCatalog()
	if err != nil {
		t.Fatalf("SnapshotModelToolCatalog() error = %v", err)
	}
	invocation, err := catalog.PrepareInvocation(name, arguments)
	if err != nil {
		t.Fatalf("PrepareInvocation(%q) error = %v", name, err)
	}
	return catalog, invocation
}

func TestModelToolCatalogSnapshotsDefinitionsAndTraits(t *testing.T) {
	registry := newInvocationTestRegistry(t)
	alphaSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"value": map[string]any{"type": "string"},
		},
	}
	alpha := newInvocationRecordingTool("alpha", alphaSchema, "alpha")
	registry.Register(alpha)

	zetaSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"count": map[string]any{"type": "integer"},
		},
	}
	zeta := newInvocationRecordingTool("zeta", zetaSchema, "zeta")
	zetaTraits := ToolTraits{
		Risk:        ToolRiskDestructive,
		Parallel:    ToolParallelSafe,
		Idempotency: ToolIdempotencyNonIdempotent,
		Sharing:     ToolSharingPerOwner,
	}
	registerInvocationFactoryBacked(t, registry, zeta, zetaTraits)

	catalog, err := registry.SnapshotModelToolCatalog()
	if err != nil {
		t.Fatalf("SnapshotModelToolCatalog() error = %v", err)
	}
	if got, want := catalog.Names(), []string{"alpha", "zeta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("catalog.Names() = %v, want %v", got, want)
	}
	if !catalog.Contains("alpha") || !catalog.Contains("zeta") || catalog.Contains("ZETA") {
		t.Fatalf("catalog membership is not exact: names=%v", catalog.Names())
	}

	definitions := catalog.ProviderDefinitions()
	if len(definitions) != 2 {
		t.Fatalf("ProviderDefinitions() length = %d, want 2", len(definitions))
	}
	if definitions[0].Type != "function" || definitions[0].Function.Name != "alpha" ||
		definitions[0].Function.Description != "alpha description" ||
		definitions[1].Function.Name != "zeta" ||
		definitions[1].Function.Description != "zeta description" {
		t.Fatalf("ProviderDefinitions() = %#v", definitions)
	}
	alphaProperties := definitions[0].Function.Parameters["properties"].(map[string]any)
	if alphaProperties["value"].(map[string]any)["type"] != "string" {
		t.Fatalf("alpha schema = %#v", definitions[0].Function.Parameters)
	}

	definitions[0].Function.Parameters["type"] = "array"
	alphaProperties["value"].(map[string]any)["type"] = "number"
	alphaSchema["type"] = "null"
	freshDefinitions := catalog.ProviderDefinitions()
	freshProperties := freshDefinitions[0].Function.Parameters["properties"].(map[string]any)
	if freshDefinitions[0].Function.Parameters["type"] != "object" ||
		freshProperties["value"].(map[string]any)["type"] != "string" {
		t.Fatalf("catalog definitions retained an external mutation: %#v", freshDefinitions[0])
	}

	alphaInvocation, err := catalog.PrepareInvocation("alpha", map[string]any{"value": "ok"})
	if err != nil {
		t.Fatalf("PrepareInvocation(alpha) error = %v", err)
	}
	if got, want := alphaInvocation.Traits(), conservativeLegacyToolTraits(); got != want {
		t.Fatalf("alpha traits = %#v, want %#v", got, want)
	}
	zetaInvocation, err := catalog.PrepareInvocation("zeta", map[string]any{"count": 1})
	if err != nil {
		t.Fatalf("PrepareInvocation(zeta) error = %v", err)
	}
	if got := zetaInvocation.Traits(); got != zetaTraits {
		t.Fatalf("zeta traits = %#v, want %#v", got, zetaTraits)
	}
}

func TestPreparedToolInvocationDetachesPolicyAndExecutionArguments(t *testing.T) {
	registry := newInvocationTestRegistry(t)
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"payload": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"label": map[string]any{"type": "string"},
					"tags": map[string]any{
						"type":  "array",
						"items": map[string]any{"type": "string"},
					},
				},
				"required": []any{"label", "tags"},
			},
		},
		"required": []any{"payload"},
	}
	tool := newInvocationRecordingTool("detached", schema, "detached-result")
	registry.Register(tool)

	original := map[string]any{
		"payload": map[string]any{
			"label": "original",
			"tags":  []any{"one", "two"},
		},
	}
	_, invocation := prepareInvocationForTest(t, registry, "detached", original)

	originalPayload := original["payload"].(map[string]any)
	originalPayload["label"] = "caller-mutated"
	originalPayload["tags"].([]any)[0] = "caller-mutated"

	policyArguments, err := invocation.PolicyArguments()
	if err != nil {
		t.Fatalf("PolicyArguments() error = %v", err)
	}
	policyPayload := policyArguments["payload"].(map[string]any)
	policyPayload["label"] = "policy-mutated"
	policyPayload["tags"].([]any)[1] = "policy-mutated"
	policyArguments["extra"] = "policy-only"

	freshPolicyArguments, err := invocation.PolicyArguments()
	if err != nil {
		t.Fatalf("second PolicyArguments() error = %v", err)
	}
	wantArguments := map[string]any{
		"payload": map[string]any{
			"label": "original",
			"tags":  []any{"one", "two"},
		},
	}
	if !reflect.DeepEqual(freshPolicyArguments, wantArguments) {
		t.Fatalf("fresh policy arguments = %#v, want %#v", freshPolicyArguments, wantArguments)
	}

	result, err := registry.DispatchPrepared(
		context.Background(),
		invocation,
		"cli",
		"chat",
		nil,
		true,
	)
	if err != nil {
		t.Fatalf("DispatchPrepared() error = %v", err)
	}
	if result == nil || result.IsError || result.ContentForLLM() != "detached-result" {
		t.Fatalf("DispatchPrepared() result = %#v", result)
	}
	if got := tool.arguments(); !reflect.DeepEqual(got, wantArguments) {
		t.Fatalf("executed arguments = %#v, want %#v", got, wantArguments)
	}
}

func TestPreparedToolInvocationRejectsStaleRegistryState(t *testing.T) {
	t.Run("same-name replacement", func(t *testing.T) {
		registry := newInvocationTestRegistry(t)
		original := newInvocationRecordingTool("replace", map[string]any{"type": "object"}, "old")
		registry.Register(original)
		_, invocation := prepareInvocationForTest(t, registry, "replace", nil)
		replacement := newInvocationRecordingTool("replace", map[string]any{"type": "object"}, "new")
		registry.Register(replacement)

		assertPreparedInvocationStale(t, registry, invocation)
		if original.calls.Load() != 0 || replacement.calls.Load() != 0 {
			t.Fatalf("stale replacement executed: old=%d new=%d", original.calls.Load(), replacement.calls.Load())
		}
	})

	t.Run("unregister", func(t *testing.T) {
		registry := newInvocationTestRegistry(t)
		tool := newInvocationRecordingTool("unregister", map[string]any{"type": "object"}, "unused")
		registry.Register(tool)
		_, invocation := prepareInvocationForTest(t, registry, "unregister", nil)
		registry.Unregister("unregister")

		assertPreparedInvocationStale(t, registry, invocation)
		if tool.calls.Load() != 0 {
			t.Fatalf("unregistered prepared tool executed %d times", tool.calls.Load())
		}
	})

	t.Run("TTL visibility revision", func(t *testing.T) {
		registry := newInvocationTestRegistry(t)
		tool := newInvocationRecordingTool("ttl", map[string]any{"type": "object"}, "unused")
		registry.RegisterHidden(tool)
		registry.PromoteTools([]string{"ttl"}, 2)
		_, invocation := prepareInvocationForTest(t, registry, "ttl", nil)
		registry.TickTTL()

		assertPreparedInvocationStale(t, registry, invocation)
		if tool.calls.Load() != 0 {
			t.Fatalf("TTL-stale prepared tool executed %d times", tool.calls.Load())
		}
	})

	t.Run("media generation", func(t *testing.T) {
		registry := newInvocationTestRegistry(t)
		registry.SetMediaStore(media.NewFileMediaStore())
		tool := newInvocationRecordingTool("media", map[string]any{"type": "object"}, "unused")
		registry.Register(tool)
		_, invocation := prepareInvocationForTest(t, registry, "media", nil)
		registry.SetMediaStore(media.NewFileMediaStore())

		assertPreparedInvocationStale(t, registry, invocation)
		if tool.calls.Load() != 0 {
			t.Fatalf("media-stale prepared tool executed %d times", tool.calls.Load())
		}
	})

	t.Run("owned registry close", func(t *testing.T) {
		registry, err := NewOwnedToolRegistry(ToolOwner{Scope: ToolOwnerScopeRegistry})
		if err != nil {
			t.Fatalf("NewOwnedToolRegistry() error = %v", err)
		}
		tool := newInvocationRecordingTool("closed", map[string]any{"type": "object"}, "unused")
		registry.Register(tool)
		_, invocation := prepareInvocationForTest(t, registry, "closed", nil)
		if err := registry.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		assertPreparedInvocationStale(t, registry, invocation)
		if tool.calls.Load() != 0 {
			t.Fatalf("closed prepared tool executed %d times", tool.calls.Load())
		}
	})

	t.Run("TTL expiry and repromotion ABA", func(t *testing.T) {
		registry := newInvocationTestRegistry(t)
		tool := newInvocationRecordingTool("ttl_aba", map[string]any{"type": "object"}, "unused")
		registry.RegisterHidden(tool)
		registry.PromoteTools([]string{"ttl_aba"}, 1)
		_, invocation := prepareInvocationForTest(t, registry, "ttl_aba", nil)
		registry.TickTTL()
		registry.PromoteTools([]string{"ttl_aba"}, 1)
		assertPreparedInvocationStale(t, registry, invocation)
		if tool.calls.Load() != 0 {
			t.Fatalf("TTL ABA prepared tool executed %d times", tool.calls.Load())
		}
	})
}

func TestPreparedToolInvocationAllowsUnrelatedRegistryMutation(t *testing.T) {
	registry := newInvocationTestRegistry(t)
	tool := newInvocationRecordingTool("retained", map[string]any{"type": "object"}, "retained")
	registry.Register(tool)
	_, invocation := prepareInvocationForTest(t, registry, "retained", nil)
	registry.Register(newInvocationRecordingTool("unrelated", map[string]any{"type": "object"}, "other"))

	result, err := registry.DispatchPrepared(context.Background(), invocation, "", "", nil, true)
	if err != nil || result == nil || result.IsError || result.ContentForLLM() != "retained" {
		t.Fatalf("DispatchPrepared() after unrelated mutation = %#v, %v", result, err)
	}
	if tool.calls.Load() != 1 {
		t.Fatalf("retained tool calls = %d, want 1", tool.calls.Load())
	}
}

func TestPreparedToolInvocationAsyncAndNonComparableCompatibility(t *testing.T) {
	t.Run("async callback", func(t *testing.T) {
		registry := newInvocationTestRegistry(t)
		tool := &invocationAsyncTool{invocationRecordingTool: newInvocationRecordingTool(
			"async_prepared", map[string]any{"type": "object"}, "sync-unused",
		)}
		registry.Register(tool)
		_, invocation := prepareInvocationForTest(t, registry, "async_prepared", nil)
		var callbacks atomic.Int64
		result, err := registry.DispatchPrepared(
			context.Background(), invocation, "", "",
			func(context.Context, *ToolResult) { callbacks.Add(1) }, true,
		)
		if err != nil || result == nil || result.IsError || !result.Async {
			t.Fatalf("async DispatchPrepared() = %#v, %v", result, err)
		}
		if tool.asyncCalls.Load() != 1 || tool.calls.Load() != 0 || callbacks.Load() != 1 {
			t.Fatalf(
				"async/sync/callback calls = %d/%d/%d",
				tool.asyncCalls.Load(),
				tool.calls.Load(),
				callbacks.Load(),
			)
		}
	})

	t.Run("non-comparable value tool", func(t *testing.T) {
		registry := newInvocationTestRegistry(t)
		calls := &atomic.Int64{}
		registry.Register(invocationNonComparableTool{
			name: "non_comparable", marker: []string{"value"}, calls: calls,
		})
		_, invocation := prepareInvocationForTest(t, registry, "non_comparable", nil)
		result, err := registry.DispatchPrepared(context.Background(), invocation, "", "", nil, true)
		if err != nil || result == nil || result.IsError || result.ContentForLLM() != "non-comparable" {
			t.Fatalf("non-comparable DispatchPrepared() = %#v, %v", result, err)
		}
		if calls.Load() != 1 {
			t.Fatalf("non-comparable calls = %d, want 1", calls.Load())
		}
	})
}

func assertPreparedInvocationStale(
	t *testing.T,
	registry *ToolRegistry,
	invocation *PreparedToolInvocation,
) {
	t.Helper()
	result, err := registry.DispatchPrepared(
		context.Background(),
		invocation,
		"",
		"",
		nil,
		true,
	)
	if !errors.Is(err, ErrToolInvocationStale) {
		t.Fatalf("DispatchPrepared() error = %v, want ErrToolInvocationStale", err)
	}
	if result != nil {
		t.Fatalf("DispatchPrepared() stale result = %#v, want nil", result)
	}
}

func TestPreparedToolInvocationDispatchIsSingleUseAcrossGoroutines(t *testing.T) {
	registry := newInvocationTestRegistry(t)
	tool := newInvocationRecordingTool("single_use", map[string]any{"type": "object"}, "winner")
	registry.Register(tool)
	_, invocation := prepareInvocationForTest(t, registry, "single_use", nil)

	const workers = 32
	type dispatchOutcome struct {
		result *ToolResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan dispatchOutcome, workers)
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			<-start
			result, err := registry.DispatchPrepared(
				context.Background(),
				invocation,
				"",
				"",
				nil,
				true,
			)
			outcomes <- dispatchOutcome{result: result, err: err}
		}()
	}
	close(start)
	wait.Wait()
	close(outcomes)

	successes := 0
	used := 0
	for outcome := range outcomes {
		switch {
		case outcome.err == nil:
			successes++
			if outcome.result == nil || outcome.result.IsError ||
				outcome.result.ContentForLLM() != "winner" {
				t.Fatalf("successful DispatchPrepared() result = %#v", outcome.result)
			}
		case errors.Is(outcome.err, ErrToolInvocationUsed):
			used++
			if outcome.result != nil {
				t.Fatalf("used invocation returned result %#v", outcome.result)
			}
		default:
			t.Fatalf("DispatchPrepared() unexpected error = %v", outcome.err)
		}
	}
	if successes != 1 || used != workers-1 {
		t.Fatalf("dispatch outcomes: successes=%d used=%d, want 1/%d", successes, used, workers-1)
	}
	if got := tool.calls.Load(); got != 1 {
		t.Fatalf("tool executions = %d, want 1", got)
	}
}

func TestModelToolCatalogRejectsSchemaBeforeEffect(t *testing.T) {
	registry := newInvocationTestRegistry(t)
	tool := newInvocationRecordingTool("schema", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"value": map[string]any{"type": "string"},
		},
		"required":             []any{"value"},
		"additionalProperties": false,
	}, "unused")
	registry.Register(tool)
	catalog, err := registry.SnapshotModelToolCatalog()
	if err != nil {
		t.Fatalf("SnapshotModelToolCatalog() error = %v", err)
	}

	invocation, err := catalog.PrepareInvocation("schema", map[string]any{"value": 42})
	if !errors.Is(err, workflows.ErrToolCallNotDispatched) {
		t.Fatalf("PrepareInvocation() error = %v, want ErrToolCallNotDispatched", err)
	}
	if invocation != nil {
		t.Fatalf("PrepareInvocation() = %#v, want nil", invocation)
	}
	if got := tool.calls.Load(); got != 0 {
		t.Fatalf("schema-rejected tool executions = %d, want 0", got)
	}
}

func TestToolRegistryDirectExecuteRemainsCompatible(t *testing.T) {
	registry := newInvocationTestRegistry(t)
	tool := newInvocationRecordingTool("direct", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"value": map[string]any{"type": "string"},
		},
		"required":             []any{"value"},
		"additionalProperties": false,
	}, "direct-result")
	registry.Register(tool)

	result := registry.Execute(context.Background(), "direct", map[string]any{"value": "ok"})
	if result == nil || result.IsError || result.ContentForLLM() != "direct-result" {
		t.Fatalf("Execute() result = %#v", result)
	}
	if got := tool.calls.Load(); got != 1 {
		t.Fatalf("direct tool executions = %d, want 1", got)
	}

	invalid := registry.Execute(context.Background(), "direct", map[string]any{"value": 42})
	if invalid == nil || !invalid.IsError ||
		!errors.Is(invalid.Err, workflows.ErrToolCallNotDispatched) {
		t.Fatalf("invalid direct Execute() result = %#v", invalid)
	}
	if got := tool.calls.Load(); got != 1 {
		t.Fatalf("schema-invalid direct call changed executions to %d", got)
	}
}
