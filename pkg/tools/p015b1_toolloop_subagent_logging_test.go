package tools

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type p015b1LoggingTool struct {
	name   string
	result string
}

func (tool *p015b1LoggingTool) Name() string { return tool.name }

func (*p015b1LoggingTool) Description() string { return "P015b1 logging probe" }

func (*p015b1LoggingTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (tool *p015b1LoggingTool) Execute(context.Context, map[string]any) *ToolResult {
	return NewToolResult(tool.result)
}

type p015b1ScriptedProvider struct {
	toolCall providers.ToolCall
	direct   string
	calls    int
	messages [][]providers.Message
}

func (provider *p015b1ScriptedProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	provider.messages = append(
		provider.messages,
		append([]providers.Message(nil), messages...),
	)
	provider.calls++
	if provider.calls == 1 {
		return &providers.LLMResponse{ToolCalls: []providers.ToolCall{provider.toolCall}}, nil
	}
	return &providers.LLMResponse{Content: provider.direct}, nil
}

func p015b1RunLoggingProbe(
	ctx context.Context,
	registry *ToolRegistry,
	toolName, callID string,
	arguments map[string]any,
	direct string,
	suppressed bool,
) (*ToolLoopResult, *p015b1ScriptedProvider, error) {
	provider := &p015b1ScriptedProvider{
		toolCall: providers.ToolCall{
			ID:        callID,
			Name:      toolName,
			Arguments: arguments,
		},
		direct: direct,
	}
	result, err := RunToolLoop(
		ctx,
		ToolLoopConfig{
			Provider:              provider,
			Model:                 "p015b1-model",
			Tools:                 registry,
			Policy:                CompatibilityAllowToolPolicy{},
			MaxIterations:         2,
			SequentialToolCalls:   true,
			SuppressToolArguments: suppressed,
		},
		[]providers.Message{{Role: "user", Content: "run probe"}},
		"",
		"",
	)
	return result, provider, err
}

func p015b1CaptureFileLogs(t *testing.T, run func()) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "p015b1.log")
	initialLevel := logger.GetLevel()
	logger.SetLevel(logger.DEBUG)
	if err := logger.EnableFileLogging(path); err != nil {
		logger.SetLevel(initialLevel)
		t.Fatalf("EnableFileLogging() error = %v", err)
	}
	loggingEnabled := true
	defer func() {
		if loggingEnabled {
			logger.DisableFileLogging()
		}
		logger.SetLevel(initialLevel)
	}()

	run()
	logger.DisableFileLogging()
	loggingEnabled = false
	logged, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(log) error = %v", err)
	}
	return string(logged)
}

func p015b1ComponentJSONLogs(t *testing.T, logged, component string) string {
	t.Helper()
	var filtered strings.Builder
	for index, line := range strings.Split(strings.TrimSpace(logged), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode log record %d: %v; line=%q", index, err, line)
		}
		if record[logger.Component] != component {
			continue
		}
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("encode filtered log record %d: %v", index, err)
		}
		filtered.Write(encoded)
		filtered.WriteByte('\n')
	}
	return filtered.String()
}

func TestP015b1ToolLoopSafeDefaultAndFunctionalMessages(t *testing.T) {
	const (
		toolNameCanary = "P015_TOOL_NAME_7d1c4a"
		callIDCanary   = "P015_TOOL_CALL_ID_3f62c8"
		argumentCanary = "P015_TOOL_ARGUMENT_b80d37"
		resultCanary   = "P015_TOOL_RESULT_f51a20"
		directCanary   = "P015_DIRECT_RESPONSE_90ae14"
	)
	registry := NewToolRegistry()
	registry.Register(&p015b1LoggingTool{name: toolNameCanary, result: resultCanary})

	var result *ToolLoopResult
	var provider *p015b1ScriptedProvider
	logs := p015b1CaptureFileLogs(t, func() {
		var err error
		result, provider, err = p015b1RunLoggingProbe(
			context.Background(),
			registry,
			toolNameCanary,
			callIDCanary,
			map[string]any{"secret": argumentCanary},
			directCanary,
			false,
		)
		if err != nil {
			t.Fatalf("RunToolLoop() error = %v", err)
		}
	})

	for _, canary := range []string{
		toolNameCanary, callIDCanary, argumentCanary, resultCanary, directCanary,
	} {
		if strings.Contains(logs, canary) {
			t.Fatalf("safe-default ToolLoop log contains %q: %s", canary, logs)
		}
	}
	toolLoopLogs := p015b1ComponentJSONLogs(t, logs, "toolloop")
	for _, field := range []string{
		"identity_tool_digest", "identity_tool_call_digest", "tool_arguments_digest",
		"tool_call_count", "content_bytes", "LLM requested tool calls", "Tool call",
	} {
		if !strings.Contains(toolLoopLogs, field) {
			t.Fatalf("safe-default ToolLoop log lacks %q: %s", field, toolLoopLogs)
		}
	}

	if result == nil || result.Content != directCanary || result.Iterations != 2 {
		t.Fatalf("RunToolLoop() result = %#v", result)
	}
	if provider == nil || len(provider.messages) != 2 || len(provider.messages[1]) != 3 {
		t.Fatalf("functional provider messages = %#v", provider)
	}
	assistant := provider.messages[1][1]
	toolResult := provider.messages[1][2]
	if len(assistant.ToolCalls) != 1 ||
		assistant.ToolCalls[0].Arguments["secret"] != argumentCanary ||
		assistant.ToolCalls[0].ID != callIDCanary ||
		assistant.ToolCalls[0].Name != toolNameCanary ||
		toolResult.Content != resultCanary || toolResult.ToolCallID != callIDCanary {
		t.Fatalf("functional ToolLoop messages changed: %#v", provider.messages[1])
	}
}

func TestP015b1ToolLoopDiagnosticPolicyTruthTable(t *testing.T) {
	enabled := logger.NewDiagnosticPolicy(true, logger.DEBUG)
	disabled := logger.NewDiagnosticPolicy(false, logger.DEBUG)
	tests := []struct {
		name       string
		owner      logger.DiagnosticPolicy
		request    logger.DiagnosticPolicy
		suppressed bool
		inherited  bool
		wantRaw    bool
	}{
		{name: "owner and request allow", owner: enabled, request: enabled, wantRaw: true},
		{name: "owner denies", owner: disabled, request: enabled},
		{name: "request denies", owner: enabled, request: disabled},
		{name: "suppression denies", owner: enabled, request: enabled, suppressed: true},
		{name: "inherited suppression denies", owner: enabled, request: enabled, inherited: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			canary := "P015_POLICY_ARGUMENT_" + strings.ReplaceAll(test.name, " ", "_")
			registry := NewToolRegistryWithDiagnosticPolicy(test.owner)
			registry.Register(&p015b1LoggingTool{name: "p015_policy_probe", result: "ok"})
			parent := context.Background()
			if test.inherited {
				parent = WithToolLogDetailsSuppressed(parent)
			}
			ctx, revoke := logger.BindRootDiagnosticPolicy(parent, test.request)
			defer revoke()

			logs := p015b1CaptureFileLogs(t, func() {
				result, _, err := p015b1RunLoggingProbe(
					ctx,
					registry,
					"p015_policy_probe",
					"p015-policy-call",
					map[string]any{"value": canary},
					"done",
					test.suppressed,
				)
				if err != nil || result == nil || result.Content != "done" {
					t.Fatalf("RunToolLoop() = %#v, %v", result, err)
				}
			})
			toolLoopLogs := p015b1ComponentJSONLogs(t, logs, "toolloop")
			if got := strings.Contains(toolLoopLogs, canary); got != test.wantRaw {
				t.Fatalf(
					"ToolLoop raw preview present = %t, want %t: %s",
					got,
					test.wantRaw,
					toolLoopLogs,
				)
			}
			if !strings.Contains(toolLoopLogs, "tool_arguments_digest") {
				t.Fatalf("ToolLoop safe argument observation missing: %s", toolLoopLogs)
			}
			wantSuppressed := test.suppressed || test.inherited
			if !strings.Contains(
				toolLoopLogs,
				`"suppressed":`+strconv.FormatBool(wantSuppressed),
			) {
				t.Fatalf("ToolLoop effective suppression missing: %s", toolLoopLogs)
			}
		})
	}
}

func TestP015b1ToolLoopInvalidDiagnosticNormalizationFailsClosed(t *testing.T) {
	const canary = "P015_INVALID_NORMALIZATION_96ea31"
	arguments := make(map[string]any, maxDiagnosticArgumentMembers+1)
	for index := 0; index <= maxDiagnosticArgumentMembers; index++ {
		arguments["member-"+strconv.Itoa(index)] = index
	}
	arguments["member-0"] = canary

	enabled := logger.NewDiagnosticPolicy(true, logger.DEBUG)
	registry := NewToolRegistryWithDiagnosticPolicy(enabled)
	registry.Register(&p015b1LoggingTool{name: "p015_large_argument_probe", result: "ok"})
	ctx, revoke := logger.BindRootDiagnosticPolicy(context.Background(), enabled)
	defer revoke()

	var provider *p015b1ScriptedProvider
	logs := p015b1CaptureFileLogs(t, func() {
		result, capturedProvider, err := p015b1RunLoggingProbe(
			ctx,
			registry,
			"p015_large_argument_probe",
			"p015-large-argument-call",
			arguments,
			"done",
			false,
		)
		provider = capturedProvider
		if err != nil || result == nil || result.Content != "done" {
			t.Fatalf("RunToolLoop() = %#v, %v", result, err)
		}
	})
	toolLoopLogs := p015b1ComponentJSONLogs(t, logs, "toolloop")
	if strings.Contains(logs, canary) || strings.Contains(toolLoopLogs, canary) {
		t.Fatalf("invalid normalized arguments were previewed: %s", toolLoopLogs)
	}
	for _, field := range []string{
		`"tool_arguments_state":"unavailable"`,
		`"tool_arguments_reason_code":"unsupported_type"`,
	} {
		if !strings.Contains(toolLoopLogs, field) {
			t.Fatalf("invalid-normalization ToolLoop record lacks %s: %s", field, toolLoopLogs)
		}
	}
	if strings.Contains(toolLoopLogs, "sensitive_preview") {
		t.Fatalf("invalid normalized arguments produced preview wire: %s", toolLoopLogs)
	}
	if provider == nil || len(provider.messages) != 2 ||
		provider.messages[1][1].ToolCalls[0].Arguments["member-0"] != canary {
		t.Fatalf("functional arguments changed: %#v", provider)
	}
}

type p015b1PanicProbeTool struct {
	name       string
	panicValue string
}

func (tool *p015b1PanicProbeTool) Name() string { return tool.name }

func (*p015b1PanicProbeTool) Description() string { return "P015b1 panic probe" }

func (*p015b1PanicProbeTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (tool *p015b1PanicProbeTool) Execute(context.Context, map[string]any) *ToolResult {
	panic(tool.panicValue)
}

type p015b1NestedRegistryTool struct {
	name          string
	inner         *ToolRegistry
	innerToolName string
	innerCanary   string
	sawSuppressed atomic.Bool
}

func (tool *p015b1NestedRegistryTool) Name() string { return tool.name }

func (*p015b1NestedRegistryTool) Description() string { return "P015b1 nested registry probe" }

func (*p015b1NestedRegistryTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (tool *p015b1NestedRegistryTool) Execute(ctx context.Context, _ map[string]any) *ToolResult {
	tool.sawSuppressed.Store(ToolLogDetailsSuppressed(ctx))
	return tool.inner.Execute(
		ctx,
		tool.innerToolName,
		map[string]any{"nested": tool.innerCanary},
	)
}

func TestP015b1InheritedSuppressionDominatesNestedRegistry(t *testing.T) {
	const (
		outerCanary = "P015_NESTED_OUTER_ARGUMENT_424fe8"
		innerCanary = "P015_NESTED_INNER_ARGUMENT_ae55f1"
		panicCanary = "P015_NESTED_PANIC_DETAIL_b5cd13"
	)
	enabled := logger.NewDiagnosticPolicy(true, logger.DEBUG)
	inner := NewToolRegistryWithDiagnosticPolicy(enabled)
	inner.Register(&p015b1PanicProbeTool{
		name: "p015_nested_panic_probe", panicValue: panicCanary,
	})
	outer := NewToolRegistryWithDiagnosticPolicy(enabled)
	nested := &p015b1NestedRegistryTool{
		name: "p015_nested_registry_probe", inner: inner,
		innerToolName: "p015_nested_panic_probe", innerCanary: innerCanary,
	}
	outer.Register(nested)
	parent := WithToolLogDetailsSuppressed(context.Background())
	ctx, revoke := logger.BindRootDiagnosticPolicy(parent, enabled)
	defer revoke()

	var result *ToolResult
	logs := p015b1CaptureFileLogs(t, func() {
		result = outer.Execute(
			ctx,
			"p015_nested_registry_probe",
			map[string]any{"outer": outerCanary},
		)
	})
	if !nested.sawSuppressed.Load() {
		t.Fatal("outer registry removed inherited suppression before nested dispatch")
	}
	if result == nil || !result.IsError || strings.Contains(result.ForLLM, panicCanary) {
		t.Fatalf("inherited suppression exposed functional panic detail: %#v", result)
	}
	if result.Err == nil || !strings.Contains(result.Err.Error(), panicCanary) {
		t.Fatalf("existing internal panic error compatibility changed: %#v", result)
	}
	for _, canary := range []string{outerCanary, innerCanary, panicCanary} {
		if strings.Contains(logs, canary) {
			t.Fatalf("nested registry log contains %q: %s", canary, logs)
		}
	}
	if !strings.Contains(logs, `"suppressed":true`) ||
		strings.Contains(logs, `"suppressed":false`) {
		t.Fatalf("nested registry did not retain effective suppression: %s", logs)
	}
}

type p015b1SuppressionProbeTool struct {
	name          string
	result        string
	sawSuppressed atomic.Bool
}

func (tool *p015b1SuppressionProbeTool) Name() string { return tool.name }

func (*p015b1SuppressionProbeTool) Description() string { return "P015b1 suppression probe" }

func (*p015b1SuppressionProbeTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (tool *p015b1SuppressionProbeTool) Execute(ctx context.Context, _ map[string]any) *ToolResult {
	tool.sawSuppressed.Store(ToolLogDetailsSuppressed(ctx))
	return NewToolResult(tool.result)
}

func TestP015b1LegacySubagentFallbackRetainsInheritedSuppression(t *testing.T) {
	const canary = "P015_LEGACY_SUBAGENT_ARGUMENT_c63874"
	enabled := logger.NewDiagnosticPolicy(true, logger.DEBUG)
	registry := NewToolRegistryWithDiagnosticPolicy(enabled)
	tool := &p015b1SuppressionProbeTool{name: "p015_legacy_suppression_probe", result: "tool done"}
	registry.Register(tool)
	provider := &p015b1ScriptedProvider{
		toolCall: providers.ToolCall{
			ID: "p015-legacy-subagent-call", Name: tool.name,
			Arguments: map[string]any{"private": canary},
		},
		direct: "legacy done",
	}
	manager := NewSubagentManager(provider, "p015b1-model", t.TempDir())
	manager.SetTools(registry)
	runner := manager.legacyTaskRunnerSnapshot()
	parent := WithToolLogDetailsSuppressed(context.Background())
	ctx, revoke := logger.BindRootDiagnosticPolicy(parent, enabled)
	defer revoke()

	var result *ToolResult
	logs := p015b1CaptureFileLogs(t, func() {
		var err error
		result, err = runner(ctx, SubagentTask{
			ID: "p015-legacy-task", Task: "run", Label: "legacy",
		})
		if err != nil {
			t.Fatalf("legacy runner error = %v", err)
		}
	})
	if result == nil || !strings.Contains(result.ForLLM, "legacy done") {
		t.Fatalf("legacy functional result changed: %#v", result)
	}
	if !tool.sawSuppressed.Load() {
		t.Fatal("legacy SubagentManager fallback dropped inherited suppression")
	}
	toolLoopLogs := p015b1ComponentJSONLogs(t, logs, "toolloop")
	if strings.Contains(logs, canary) || strings.Contains(toolLoopLogs, canary) {
		t.Fatalf("legacy subagent previewed inherited-suppressed arguments: %s", toolLoopLogs)
	}
	if !strings.Contains(toolLoopLogs, `"suppressed":true`) ||
		strings.Contains(toolLoopLogs, `"suppressed":false`) {
		t.Fatalf("legacy ToolLoop suppression projection = %s", toolLoopLogs)
	}
	if len(provider.messages) != 2 ||
		provider.messages[1][2].ToolCalls[0].Arguments["private"] != canary {
		t.Fatalf("legacy functional arguments changed: %#v", provider.messages)
	}
}

type p015b1BlockingProvider struct {
	entered chan struct{}
	release chan struct{}
	call    providers.ToolCall
	calls   int
}

func (provider *p015b1BlockingProvider) Chat(
	_ context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	provider.calls++
	if provider.calls == 1 {
		close(provider.entered)
		<-provider.release
		return &providers.LLMResponse{ToolCalls: []providers.ToolCall{provider.call}}, nil
	}
	return &providers.LLMResponse{Content: "done"}, nil
}

func TestP015b1ToolLoopRechecksRevocationAfterProviderReturns(t *testing.T) {
	const canary = "P015_REVOKED_AFTER_PROVIDER_642ac0"
	enabled := logger.NewDiagnosticPolicy(true, logger.DEBUG)
	registry := NewToolRegistryWithDiagnosticPolicy(enabled)
	registry.Register(&p015b1LoggingTool{name: "p015_blocking_probe", result: "ok"})
	ctx, revoke := logger.BindRootDiagnosticPolicy(context.Background(), enabled)
	provider := &p015b1BlockingProvider{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		call: providers.ToolCall{
			ID:        "p015-blocked-call",
			Name:      "p015_blocking_probe",
			Arguments: map[string]any{"value": canary},
		},
	}

	logs := p015b1CaptureFileLogs(t, func() {
		done := make(chan error, 1)
		go func() {
			_, err := RunToolLoop(ctx, ToolLoopConfig{
				Provider: provider, Model: "p015b1-model", Tools: registry,
				Policy: CompatibilityAllowToolPolicy{}, MaxIterations: 2,
				SequentialToolCalls: true,
			}, []providers.Message{{Role: "user", Content: "run"}}, "", "")
			done <- err
		}()
		<-provider.entered
		revoke()
		close(provider.release)
		if err := <-done; err != nil {
			t.Fatalf("RunToolLoop() error = %v", err)
		}
	})
	if strings.Contains(logs, canary) {
		t.Fatalf("revoked request previewed provider-returned arguments: %s", logs)
	}
	if strings.Contains(p015b1ComponentJSONLogs(t, logs, "toolloop"), canary) {
		t.Fatalf("revoked ToolLoop previewed provider-returned arguments: %s", logs)
	}
}

type p015b1HostileProviderError struct {
	canary string
	calls  *atomic.Int64
}

func (value *p015b1HostileProviderError) Error() string {
	value.calls.Add(1)
	return value.canary
}

type p015b1ErrorProvider struct{ err error }

func (provider p015b1ErrorProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	return nil, provider.err
}

var (
	p015b1HostileMarshalCalls atomic.Int64
	p015b1HostileStringCalls  atomic.Int64
)

type p015b1HostileScalar string

func (value p015b1HostileScalar) MarshalJSON() ([]byte, error) {
	p015b1HostileMarshalCalls.Add(1)
	return []byte(`"` + string(value) + `"`), nil
}

func (value p015b1HostileScalar) String() string {
	p015b1HostileStringCalls.Add(1)
	return string(value)
}

func TestP015b1ToolLoopLoggingDoesNotInvokeHostileMethods(t *testing.T) {
	t.Run("provider error", func(t *testing.T) {
		const canary = "P015_HOSTILE_PROVIDER_ERROR_a635de"
		var errorCalls atomic.Int64
		hostile := &p015b1HostileProviderError{canary: canary, calls: &errorCalls}
		logs := p015b1CaptureFileLogs(t, func() {
			_, err := RunToolLoop(context.Background(), ToolLoopConfig{
				Provider: p015b1ErrorProvider{err: hostile},
				Model:    "p015b1-model", MaxIterations: 1,
			}, nil, "", "")
			if err == nil {
				t.Fatal("RunToolLoop() error = nil")
			}
		})
		if strings.Contains(logs, canary) {
			t.Fatalf("provider error text entered logs: %s", logs)
		}
		// One invocation belongs to the functional wrapped return error. The log
		// projection itself must not add a second invocation.
		if calls := errorCalls.Load(); calls != 1 {
			t.Fatalf("hostile Error() calls = %d, want one functional call", calls)
		}
	})

	t.Run("tool argument marshal and string", func(t *testing.T) {
		const canary = "P015_HOSTILE_MARSHAL_58c3a1"
		p015b1HostileMarshalCalls.Store(0)
		p015b1HostileStringCalls.Store(0)
		registry := NewToolRegistry()
		registry.Register(&p015b1LoggingTool{name: "p015_hostile_argument", result: "ok"})
		logs := p015b1CaptureFileLogs(t, func() {
			result, _, err := p015b1RunLoggingProbe(
				context.Background(), registry, "p015_hostile_argument", "hostile-call",
				map[string]any{"value": p015b1HostileScalar(canary)}, "done", false,
			)
			if err != nil || result == nil || result.Content != "done" {
				t.Fatalf("RunToolLoop() = %#v, %v", result, err)
			}
		})
		if strings.Contains(logs, canary) {
			t.Fatalf("hostile argument entered safe-default logs: %s", logs)
		}
		// NormalizeToolCall and assistant-message construction each serialize
		// functional protocol arguments. Diagnostics must add no third call.
		if calls := p015b1HostileMarshalCalls.Load(); calls != 2 {
			t.Fatalf("MarshalJSON() calls = %d, want two functional protocol calls", calls)
		}
		if calls := p015b1HostileStringCalls.Load(); calls != 0 {
			t.Fatalf("String() calls = %d, want zero", calls)
		}
	})
}

type p015b1HostilePanicError struct {
	canary       string
	stringCalls  *atomic.Int64
	errorCalls   *atomic.Int64
	marshalCalls *atomic.Int64
}

func (value *p015b1HostilePanicError) String() string {
	value.stringCalls.Add(1)
	return value.canary
}

func (value *p015b1HostilePanicError) Error() string {
	value.errorCalls.Add(1)
	return value.canary
}

func (value *p015b1HostilePanicError) MarshalJSON() ([]byte, error) {
	value.marshalCalls.Add(1)
	return []byte(`"` + value.canary + `"`), nil
}

func TestP015b1SubagentPanicPathsAreFixedAndMethodFree(t *testing.T) {
	const canary = "P015_SUBAGENT_PANIC_VALUE_8e072b"
	var stringCalls atomic.Int64
	var errorCalls atomic.Int64
	var marshalCalls atomic.Int64
	hostile := &p015b1HostilePanicError{
		canary: canary, stringCalls: &stringCalls,
		errorCalls: &errorCalls, marshalCalls: &marshalCalls,
	}

	logs := p015b1CaptureFileLogs(t, func() {
		result, err := callSubagentTaskRunner(
			context.Background(),
			SubagentTask{},
			func(context.Context, SubagentTask) (*ToolResult, error) { panic(hostile) },
		)
		if result != nil || err == nil || err.Error() != "subagent task panicked: "+canary {
			t.Fatalf("panic runner result = %#v, %v", result, err)
		}
		callSubagentTaskCallback(
			context.Background(),
			func(context.Context, *ToolResult) { panic(hostile) },
			NewToolResult("unchanged"),
		)
		callSubagentTaskFinalizer(func() { panic(hostile) })
	})

	if strings.Contains(logs, canary) {
		t.Fatalf("subagent panic value entered file log: %s", logs)
	}
	for _, message := range []string{
		"Subagent task runner panic recovered",
		"Subagent callback panic recovered",
		"Subagent finalizer panic recovered",
		"panic_digest",
	} {
		if !strings.Contains(logs, message) {
			t.Fatalf("subagent panic log lacks %q: %s", message, logs)
		}
	}
	if got := stringCalls.Load() + errorCalls.Load(); got != 1 || marshalCalls.Load() != 0 {
		t.Fatalf(
			"hostile methods String/Error/Marshal = %d/%d/%d; want String+Error=1 and Marshal=0",
			stringCalls.Load(), errorCalls.Load(), marshalCalls.Load(),
		)
	}
}

func TestP015b1ToolLoopOppositePolicyRaceIsolation(t *testing.T) {
	const allowedCanary = "P015_RACE_ALLOWED_bde392"
	const deniedCanary = "P015_RACE_DENIED_15a6f0"
	enabled := logger.NewDiagnosticPolicy(true, logger.DEBUG)
	disabled := logger.NewDiagnosticPolicy(false, logger.DEBUG)
	allowedRegistry := NewToolRegistryWithDiagnosticPolicy(enabled)
	deniedRegistry := NewToolRegistryWithDiagnosticPolicy(disabled)
	allowedRegistry.Register(&p015b1LoggingTool{name: "p015_race_tool", result: "ok"})
	deniedRegistry.Register(&p015b1LoggingTool{name: "p015_race_tool", result: "ok"})
	allowedCtx, revokeAllowed := logger.BindRootDiagnosticPolicy(context.Background(), enabled)
	defer revokeAllowed()
	deniedCtx, revokeDenied := logger.BindRootDiagnosticPolicy(context.Background(), enabled)
	defer revokeDenied()

	logs := p015b1CaptureFileLogs(t, func() {
		const pairs = 12
		errors := make(chan error, pairs*2)
		var group sync.WaitGroup
		for index := 0; index < pairs; index++ {
			for _, run := range []struct {
				ctx      context.Context
				registry *ToolRegistry
				canary   string
			}{
				{ctx: allowedCtx, registry: allowedRegistry, canary: allowedCanary},
				{ctx: deniedCtx, registry: deniedRegistry, canary: deniedCanary},
			} {
				group.Add(1)
				go func(run struct {
					ctx      context.Context
					registry *ToolRegistry
					canary   string
				},
				) {
					defer group.Done()
					_, _, err := p015b1RunLoggingProbe(
						run.ctx, run.registry, "p015_race_tool", "p015-race-call",
						map[string]any{"value": run.canary}, "done", false,
					)
					errors <- err
				}(run)
			}
		}
		group.Wait()
		close(errors)
		for err := range errors {
			if err != nil {
				t.Fatalf("concurrent RunToolLoop() error = %v", err)
			}
		}
	})
	toolLoopLogs := p015b1ComponentJSONLogs(t, logs, "toolloop")
	if !strings.Contains(toolLoopLogs, allowedCanary) {
		t.Fatalf("allowed ToolLoop race preview missing: %s", toolLoopLogs)
	}
	if strings.Contains(toolLoopLogs, deniedCanary) {
		t.Fatalf("denied race preview crossed ToolLoop registry policy: %s", toolLoopLogs)
	}
}

type p015b1ToolSubagentSink struct {
	file      string
	function  string
	emitter   string
	component string
	message   string
}

func TestP015b1ToolLoopSubagentLogStructure(t *testing.T) {
	toolLoopSink := func(emitter, message string) p015b1ToolSubagentSink {
		return p015b1ToolSubagentSink{
			file: "toolloop.go", function: "RunToolLoop", emitter: emitter,
			component: "ComponentToolLoop", message: message,
		}
	}
	subagentSink := func(function, message string) p015b1ToolSubagentSink {
		return p015b1ToolSubagentSink{
			file: "subagent.go", function: function, emitter: "ErrorSafeCF",
			component: "ComponentTools", message: message,
		}
	}
	want := []p015b1ToolSubagentSink{
		toolLoopSink("DebugSafeCF", "DiagnosticMessageLLMIteration"),
		toolLoopSink("ErrorSafeCF", "DiagnosticMessageLLMCallFailed"),
		toolLoopSink("InfoSafeCF", "DiagnosticMessageLLMDirectResponse"),
		toolLoopSink("InfoSafeCF", "DiagnosticMessageLLMRequestedToolCalls"),
		toolLoopSink("InfoSafeCF", "DiagnosticMessageToolCall"),
		toolLoopSink("DebugSensitiveCF", "DiagnosticMessageToolArguments"),
		subagentSink("callSubagentTaskRunner", "DiagnosticMessageSubagentRunnerPanic"),
		subagentSink("callSubagentTaskCallback", "DiagnosticMessageSubagentCallbackPanic"),
		subagentSink("callSubagentTaskFinalizer", "DiagnosticMessageSubagentFinalizerPanic"),
	}

	var got []p015b1ToolSubagentSink
	functionalMarshalCalls := 0
	for _, filename := range []string{"toolloop.go", "subagent.go"} {
		fileSet := token.NewFileSet()
		parsed, err := parser.ParseFile(fileSet, filename, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("ParseFile(%s) error = %v", filename, err)
		}
		for _, imported := range parsed.Imports {
			if imported.Path.Value == `"github.com/sipeed/picoclaw/pkg/utils"` {
				t.Errorf("%s retains logging-side truncate dependency", filename)
			}
		}

		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			functionName := function.Name.Name
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				packageName, callName, selector := p015b1SelectorCall(call)
				if selector && strings.HasPrefix(callName, "Recover") {
					t.Errorf("%s: recovery call %s", fileSet.Position(call.Pos()), callName)
				}
				if identifier, identified := call.Fun.(*ast.Ident); identified &&
					strings.HasPrefix(identifier.Name, "Recover") {
					t.Errorf("%s: recovery call %s", fileSet.Position(call.Pos()), identifier.Name)
				}
				if selector && packageName == "debug" && callName == "Stack" {
					t.Errorf("%s: raw stack capture", fileSet.Position(call.Pos()))
				}
				if selector && packageName == "utils" && callName == "Truncate" {
					t.Errorf("%s:%s contains utils.Truncate", filename, functionName)
				}
				if selector && packageName == "json" && callName == "Marshal" {
					functionalMarshalCalls++
					p015b1AssertFunctionalAssistantMarshal(
						t, fileSet, filename, functionName, call,
					)
				}
				if !selector || packageName != "logger" {
					return true
				}
				if p015b1LegacyLoggerCall(callName) {
					t.Errorf(
						"%s: forbidden logger.%s",
						fileSet.Position(call.Pos()),
						callName,
					)
					return true
				}

				componentIndex, messageIndex, safeSink := p015b1SafeSinkIndexes(callName)
				if !safeSink {
					if strings.HasSuffix(callName, "SafeCF") ||
						strings.HasSuffix(callName, "SensitiveCF") {
						t.Errorf(
							"%s: unrecognized safe logger sink logger.%s",
							fileSet.Position(call.Pos()),
							callName,
						)
					}
					return true
				}
				if len(call.Args) <= messageIndex {
					t.Errorf("%s: incomplete logger.%s envelope", fileSet.Position(call.Pos()), callName)
					return true
				}
				component, componentOK := p015b1LoggerConstant(call.Args[componentIndex], "Component")
				message, messageOK := p015b1LoggerConstant(call.Args[messageIndex], "DiagnosticMessage")
				if !componentOK || !messageOK {
					t.Errorf("%s: dynamic logger component or message", fileSet.Position(call.Pos()))
				}
				for _, argument := range call.Args {
					p015b1AssertSinkArgumentSafe(t, fileSet, argument)
				}
				got = append(got, p015b1ToolSubagentSink{
					file: filename, function: functionName, emitter: callName,
					component: component, message: message,
				})
				return true
			})
		}
	}

	if functionalMarshalCalls != 1 {
		t.Errorf("functional assistant json.Marshal calls = %d; want 1", functionalMarshalCalls)
	}
	sort.Slice(want, func(left, right int) bool {
		return p015b1ToolSubagentSinkKey(want[left]) < p015b1ToolSubagentSinkKey(want[right])
	})
	sort.Slice(got, func(left, right int) bool {
		return p015b1ToolSubagentSinkKey(got[left]) < p015b1ToolSubagentSinkKey(got[right])
	})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ToolLoop/Subagent sink manifest changed:\n got: %#v\nwant: %#v", got, want)
	}
}

const (
	p015b1LoggerImportPath = "github.com/sipeed/picoclaw/pkg/logger"
	p015b1UtilsImportPath  = "github.com/sipeed/picoclaw/pkg/utils"
)

func TestP015b1CentralToolClosedASTGate(t *testing.T) {
	toolLoopSink := func(emitter, message string) p015b1ToolSubagentSink {
		return p015b1ToolSubagentSink{
			file: "toolloop.go", function: "RunToolLoop", emitter: emitter,
			component: "ComponentToolLoop", message: message,
		}
	}
	subagentSink := func(function, message string) p015b1ToolSubagentSink {
		return p015b1ToolSubagentSink{
			file: "subagent.go", function: function, emitter: "ErrorSafeCF",
			component: "ComponentTools", message: message,
		}
	}
	registrySink := func(
		function, emitter, component, message string,
	) p015b1ToolSubagentSink {
		return p015b1ToolSubagentSink{
			file: "registry.go", function: function, emitter: emitter,
			component: component, message: message,
		}
	}
	want := []p015b1ToolSubagentSink{
		toolLoopSink("DebugSafeCF", "DiagnosticMessageLLMIteration"),
		toolLoopSink("ErrorSafeCF", "DiagnosticMessageLLMCallFailed"),
		toolLoopSink("InfoSafeCF", "DiagnosticMessageLLMDirectResponse"),
		toolLoopSink("InfoSafeCF", "DiagnosticMessageLLMRequestedToolCalls"),
		toolLoopSink("InfoSafeCF", "DiagnosticMessageToolCall"),
		toolLoopSink("DebugSensitiveCF", "DiagnosticMessageToolArguments"),
		subagentSink("callSubagentTaskRunner", "DiagnosticMessageSubagentRunnerPanic"),
		subagentSink("callSubagentTaskCallback", "DiagnosticMessageSubagentCallbackPanic"),
		subagentSink("callSubagentTaskFinalizer", "DiagnosticMessageSubagentFinalizerPanic"),
		registrySink(
			"registerLegacy", "DebugSafeCF", "ComponentTools",
			"DiagnosticMessageToolRegistrationSkipped",
		),
		registrySink(
			"registerLegacy", "WarnSafeCF", "ComponentTools",
			"DiagnosticMessageToolRegistrationCollision",
		),
		registrySink(
			"registerLegacy", "WarnSafeCF", "ComponentTools",
			"DiagnosticMessageToolRegistrationOverwritten",
		),
		registrySink(
			"registerLegacy", "DebugSafeCF", "ComponentTools",
			"DiagnosticMessageToolRegistered",
		),
		registrySink(
			"Unregister", "DebugSafeCF", "ComponentTools",
			"DiagnosticMessageToolUnregistered",
		),
		registrySink(
			"PromoteTools", "DebugSafeCF", "ComponentTools",
			"DiagnosticMessageToolPromotionCompleted",
		),
		registrySink(
			"executeWithContext", "ErrorSafeCF", "ComponentTool",
			"DiagnosticMessageToolNotFound",
		),
		registrySink(
			"executeWithContext", "WarnSafeCF", "ComponentTool",
			"DiagnosticMessageToolArgumentValidationFailed",
		),
		registrySink(
			"logToolExecutionStart", "InfoSafeCF", "ComponentTool",
			"DiagnosticMessageToolExecutionStarted",
		),
		registrySink(
			"logToolExecutionStart", "DebugSensitiveCF", "ComponentTool",
			"DiagnosticMessageToolArguments",
		),
		registrySink(
			"executeResolvedToolWithContext", "ErrorSafeCF", "ComponentTool",
			"DiagnosticMessageToolExecutionPanic",
		),
		registrySink(
			"executeResolvedToolWithContext", "ErrorSafeCF", "ComponentTool",
			"DiagnosticMessageToolExecutionFailed",
		),
		registrySink(
			"executeResolvedToolWithContext", "InfoSafeCF", "ComponentTool",
			"DiagnosticMessageToolAsyncStarted",
		),
		registrySink(
			"executeResolvedToolWithContext", "InfoSafeCF", "ComponentTool",
			"DiagnosticMessageToolExecutionCompleted",
		),
	}
	if len(want) != 23 {
		t.Fatalf("central tool sink manifest entries = %d; want 23", len(want))
	}
	allowedLoggerCalls := map[string]bool{
		"DebugSafeCF":                 true,
		"DebugSensitiveCF":            true,
		"DiagnosticPolicyFromContext": true,
		"ErrorSafeCF":                 true,
		"InfoSafeCF":                  true,
		"NarrowDiagnosticPolicy":      true,
		"NewSafeFields":               true,
		"ObserveErrorType":            true,
		"ObserveIdentity":             true,
		"ObserveJSONValue":            true,
		"ObservePanic":                true,
		"ObserveText":                 true,
		"SafeBool":                    true,
		"SafeInt":                     true,
		"SafeInt64":                   true,
		"SafeObservation":             true,
		"WarnSafeCF":                  true,
	}

	var got []p015b1ToolSubagentSink
	functionalMarshalCalls := 0
	for _, filename := range []string{
		"toolloop.go", "subagent.go", "registry.go", "registry_invocation.go",
	} {
		fileSet := token.NewFileSet()
		parsed, err := parser.ParseFile(fileSet, filename, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("ParseFile(%s) error = %v", filename, err)
		}
		imports := p015b1ResolvedImports(t, fileSet, parsed)
		p015b1AssertNoMonitoredImportShadowing(t, fileSet, parsed, imports)
		p015b1AssertMonitoredSelectorsAreDirectCalls(t, fileSet, parsed, imports)
		functions := p015b1EnclosingFunctions(parsed)

		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			functionName := functions[call.Pos()]
			if functionName == "" {
				functionName = "<initializer>"
			}
			if identifier, identified := call.Fun.(*ast.Ident); identified {
				if identifier.Name == "print" || identifier.Name == "println" {
					t.Errorf("%s: builtin output sink %s", fileSet.Position(call.Pos()), identifier.Name)
				}
				if strings.HasPrefix(identifier.Name, "Recover") {
					t.Errorf("%s: recovery call %s", fileSet.Position(call.Pos()), identifier.Name)
				}
			}

			importPath, callName, imported := p015b1ImportedCall(call, imports)
			if !imported {
				if selector, selected := call.Fun.(*ast.SelectorExpr); selected &&
					strings.HasPrefix(selector.Sel.Name, "Recover") {
					t.Errorf("%s: recovery call %s", fileSet.Position(call.Pos()), selector.Sel.Name)
				}
				return true
			}
			switch importPath {
			case p015b1LoggerImportPath:
				if !allowedLoggerCalls[callName] {
					t.Errorf(
						"%s: unrecognized or legacy logger call logger.%s",
						fileSet.Position(call.Pos()), callName,
					)
					return true
				}
				componentIndex, messageIndex, safeSink := p015b1SafeSinkIndexes(callName)
				if !safeSink {
					return true
				}
				if len(call.Args) <= messageIndex {
					t.Errorf("%s: incomplete logger.%s envelope", fileSet.Position(call.Pos()), callName)
					return true
				}
				component, componentOK := p015b1ImportedLoggerConstant(
					call.Args[componentIndex], "Component", imports,
				)
				message, messageOK := p015b1ImportedLoggerConstant(
					call.Args[messageIndex], "DiagnosticMessage", imports,
				)
				if !componentOK || !messageOK {
					t.Errorf("%s: dynamic logger component or message", fileSet.Position(call.Pos()))
				}
				for _, argument := range call.Args {
					p015b1AssertClosedSinkArgumentSafe(t, fileSet, argument, imports)
				}
				if callName == "DebugSensitiveCF" {
					p015b1AssertCentralSensitiveCall(
						t, fileSet, filename, functionName, call, imports,
					)
				}
				got = append(got, p015b1ToolSubagentSink{
					file: filename, function: functionName, emitter: callName,
					component: component, message: message,
				})
			case "encoding/json":
				if callName != "Marshal" {
					t.Errorf("%s: unmanifested encoding/json call %s", fileSet.Position(call.Pos()), callName)
					return true
				}
				functionalMarshalCalls++
				p015b1AssertFunctionalAssistantMarshal(
					t, fileSet, filename, functionName, call,
				)
			case p015b1UtilsImportPath:
				t.Errorf("%s: logging-side utils.%s", fileSet.Position(call.Pos()), callName)
			case "log", "log/slog":
				t.Errorf("%s: standard logger sink %s.%s", fileSet.Position(call.Pos()), importPath, callName)
			case "fmt":
				if p015b1FmtPrintLike(callName) {
					t.Errorf("%s: fmt output sink fmt.%s", fileSet.Position(call.Pos()), callName)
				}
			case "runtime/debug":
				t.Errorf("%s: runtime/debug call %s", fileSet.Position(call.Pos()), callName)
			case "os", "io", "runtime":
				t.Errorf(
					"%s: forbidden output-capable import call %s.%s",
					fileSet.Position(call.Pos()),
					importPath,
					callName,
				)
			default:
				if importPath == "github.com/rs/zerolog" ||
					strings.HasPrefix(importPath, "github.com/rs/zerolog/") {
					t.Errorf("%s: zerolog output call %s.%s", fileSet.Position(call.Pos()), importPath, callName)
				}
			}
			return true
		})
	}

	if functionalMarshalCalls != 1 {
		t.Errorf("functional assistant json.Marshal calls = %d; want 1", functionalMarshalCalls)
	}
	sort.Slice(want, func(left, right int) bool {
		return p015b1ToolSubagentSinkKey(want[left]) < p015b1ToolSubagentSinkKey(want[right])
	})
	sort.Slice(got, func(left, right int) bool {
		return p015b1ToolSubagentSinkKey(got[left]) < p015b1ToolSubagentSinkKey(got[right])
	})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("closed ToolLoop/Subagent sink manifest changed:\n got: %#v\nwant: %#v", got, want)
	}
}

func p015b1ResolvedImports(
	t *testing.T,
	fileSet *token.FileSet,
	file *ast.File,
) map[string]string {
	t.Helper()
	resolved := make(map[string]string, len(file.Imports))
	for _, imported := range file.Imports {
		importPath, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			t.Errorf("%s: invalid import path: %v", fileSet.Position(imported.Pos()), err)
			continue
		}
		localName := importPath
		if separator := strings.LastIndexByte(localName, '/'); separator >= 0 {
			localName = localName[separator+1:]
		}
		if imported.Name != nil {
			localName = imported.Name.Name
			if localName == "." {
				t.Errorf("%s: dot imports are forbidden", fileSet.Position(imported.Pos()))
			}
			if importPath == p015b1LoggerImportPath || importPath == "encoding/json" {
				t.Errorf(
					"%s: logger/json alias or dot import is forbidden",
					fileSet.Position(imported.Pos()),
				)
			}
		}
		if importPath == p015b1UtilsImportPath {
			t.Errorf("%s: utils logging dependency is forbidden", fileSet.Position(imported.Pos()))
		}
		if importPath == "runtime/debug" {
			t.Errorf("%s: runtime/debug import is forbidden", fileSet.Position(imported.Pos()))
		}
		if importPath == "os" || importPath == "io" || importPath == "runtime" {
			t.Errorf("%s: output-capable import %s is forbidden", fileSet.Position(imported.Pos()), importPath)
		}
		if importPath == "github.com/rs/zerolog" ||
			strings.HasPrefix(importPath, "github.com/rs/zerolog/") {
			t.Errorf("%s: zerolog import is forbidden", fileSet.Position(imported.Pos()))
		}
		if (importPath == "log" || importPath == "log/slog") && localName == "." {
			t.Errorf("%s: dot-imported standard logger is forbidden", fileSet.Position(imported.Pos()))
		}
		resolved[localName] = importPath
	}
	return resolved
}

func p015b1AssertNoMonitoredImportShadowing(
	t *testing.T,
	fileSet *token.FileSet,
	file *ast.File,
	imports map[string]string,
) {
	t.Helper()
	monitoredNames := map[string]bool{
		"logger": true, "fmt": true, "os": true, "io": true,
		"runtime": true, "debug": true, "log": true, "slog": true,
		"zerolog": true, "print": true, "println": true,
	}
	for localName, importPath := range imports {
		if p015b1MonitoredImportPath(importPath) && localName != "." && localName != "_" {
			monitoredNames[localName] = true
		}
	}
	reject := func(identifier *ast.Ident) {
		if identifier != nil && monitoredNames[identifier.Name] {
			t.Errorf(
				"%s: local declaration shadows monitored import %s",
				fileSet.Position(identifier.Pos()), identifier.Name,
			)
		}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.AssignStmt:
			if value.Tok == token.DEFINE {
				for _, left := range value.Lhs {
					if identifier, ok := left.(*ast.Ident); ok {
						reject(identifier)
					}
				}
			}
		case *ast.RangeStmt:
			if value.Tok == token.DEFINE {
				if identifier, ok := value.Key.(*ast.Ident); ok {
					reject(identifier)
				}
				if identifier, ok := value.Value.(*ast.Ident); ok {
					reject(identifier)
				}
			}
		case *ast.ValueSpec:
			for _, name := range value.Names {
				reject(name)
			}
		case *ast.TypeSpec:
			reject(value.Name)
		case *ast.FuncDecl:
			reject(value.Name)
			p015b1RejectMonitoredFieldNames(value.Recv, reject)
			p015b1RejectMonitoredFieldNames(value.Type.Params, reject)
			p015b1RejectMonitoredFieldNames(value.Type.Results, reject)
		case *ast.FuncLit:
			p015b1RejectMonitoredFieldNames(value.Type.Params, reject)
			p015b1RejectMonitoredFieldNames(value.Type.Results, reject)
		}
		return true
	})
}

func p015b1RejectMonitoredFieldNames(
	fields *ast.FieldList,
	reject func(*ast.Ident),
) {
	if fields == nil {
		return
	}
	for _, field := range fields.List {
		for _, name := range field.Names {
			reject(name)
		}
	}
}

func p015b1AssertMonitoredSelectorsAreDirectCalls(
	t *testing.T,
	fileSet *token.FileSet,
	file *ast.File,
	imports map[string]string,
) {
	t.Helper()
	directCalls := make(map[token.Pos]bool)
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
			directCalls[selector.Pos()] = true
		}
		return true
	})
	ast.Inspect(file, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		root := p015b1SelectorRootIdentifier(selector.X)
		importPath, imported := imports[root]
		if !imported || !p015b1MonitoredSelector(importPath, selector.Sel.Name) {
			return true
		}
		if !directCalls[selector.Pos()] {
			t.Errorf(
				"%s: monitored logging symbol %s.%s used as a function value",
				fileSet.Position(selector.Pos()), importPath, selector.Sel.Name,
			)
		}
		return true
	})
}

func p015b1MonitoredImportPath(importPath string) bool {
	if importPath == "github.com/rs/zerolog" ||
		strings.HasPrefix(importPath, "github.com/rs/zerolog/") {
		return true
	}
	switch importPath {
	case p015b1LoggerImportPath, p015b1UtilsImportPath, "encoding/json", "fmt",
		"log", "log/slog", "runtime/debug", "os", "io", "runtime":
		return true
	default:
		return false
	}
}

func p015b1MonitoredSelector(importPath, name string) bool {
	if importPath == "github.com/rs/zerolog" ||
		strings.HasPrefix(importPath, "github.com/rs/zerolog/") {
		return true
	}
	switch importPath {
	case p015b1LoggerImportPath:
		for _, prefix := range []string{
			"Component", "DiagnosticMessage", "DiagnosticPolicy", "ErrorClass", "Field",
			"ObservationDomain", "ObservationPrefix", "Sensitivity",
		} {
			if strings.HasPrefix(name, prefix) {
				return false
			}
		}
		for _, helper := range []string{
			"NewSafeFields", "ObserveErrorType", "ObserveIdentity", "ObserveJSONValue",
			"ObservePanic", "ObserveText", "SafeBool", "SafeField", "SafeInt", "SafeInt64",
			"SafeObservation",
		} {
			if name == helper {
				return false
			}
		}
		return true
	case "encoding/json":
		return name == "Marshal"
	case "fmt":
		return p015b1FmtPrintLike(name)
	case "runtime/debug":
		return true
	case "log", "log/slog", "os", "io", "runtime", p015b1UtilsImportPath:
		return true
	default:
		return false
	}
}

func p015b1EnclosingFunctions(file *ast.File) map[token.Pos]string {
	functions := make(map[token.Pos]string)
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			if call, ok := node.(*ast.CallExpr); ok {
				functions[call.Pos()] = function.Name.Name
			}
			return true
		})
	}
	return functions
}

func p015b1ImportedCall(
	call *ast.CallExpr,
	imports map[string]string,
) (string, string, bool) {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", "", false
	}
	root := p015b1SelectorRootIdentifier(selector.X)
	importPath, imported := imports[root]
	return importPath, selector.Sel.Name, imported
}

func p015b1SelectorRootIdentifier(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		return p015b1SelectorRootIdentifier(value.X)
	case *ast.CallExpr:
		return p015b1SelectorRootIdentifier(value.Fun)
	case *ast.IndexExpr:
		return p015b1SelectorRootIdentifier(value.X)
	case *ast.IndexListExpr:
		return p015b1SelectorRootIdentifier(value.X)
	case *ast.ParenExpr:
		return p015b1SelectorRootIdentifier(value.X)
	default:
		return ""
	}
}

func p015b1ImportedLoggerConstant(
	expression ast.Expr,
	prefix string,
	imports map[string]string,
) (string, bool) {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || !strings.HasPrefix(selector.Sel.Name, prefix) {
		return "", false
	}
	identifier, ok := selector.X.(*ast.Ident)
	if !ok || imports[identifier.Name] != p015b1LoggerImportPath {
		return "", false
	}
	return selector.Sel.Name, true
}

func p015b1AssertClosedSinkArgumentSafe(
	t *testing.T,
	fileSet *token.FileSet,
	expression ast.Expr,
	imports map[string]string,
) {
	t.Helper()
	ast.Inspect(expression, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.MapType:
			t.Errorf("%s: dynamic map in logger sink argument", fileSet.Position(value.Pos()))
		case *ast.BasicLit:
			if value.Kind == token.STRING {
				t.Errorf("%s: raw string literal in logger sink argument", fileSet.Position(value.Pos()))
			}
		case *ast.CallExpr:
			importPath, callName, imported := p015b1ImportedCall(value, imports)
			if imported && (importPath == "fmt" || importPath == "log" ||
				importPath == "log/slog" || importPath == "encoding/json" ||
				importPath == p015b1UtilsImportPath || importPath == "runtime/debug" ||
				importPath == "os" || importPath == "io" || importPath == "runtime" ||
				importPath == "github.com/rs/zerolog" ||
				strings.HasPrefix(importPath, "github.com/rs/zerolog/")) {
				t.Errorf(
					"%s: forbidden %s.%s in logger sink argument",
					fileSet.Position(value.Pos()), importPath, callName,
				)
			}
			if selector, ok := value.Fun.(*ast.SelectorExpr); ok {
				switch selector.Sel.Name {
				case "Error", "Unwrap", "Is", "As", "String", "Format", "ContentForLLM":
					t.Errorf(
						"%s: forbidden .%s in logger sink argument",
						fileSet.Position(value.Pos()), selector.Sel.Name,
					)
				}
				if strings.HasPrefix(selector.Sel.Name, "Recover") {
					t.Errorf("%s: recovery call in logger sink argument", fileSet.Position(value.Pos()))
				}
			}
			if identifier, ok := value.Fun.(*ast.Ident); ok &&
				strings.HasPrefix(identifier.Name, "Recover") {
				t.Errorf("%s: recovery call in logger sink argument", fileSet.Position(value.Pos()))
			}
		}
		return true
	})
}

func p015b1AssertCentralSensitiveCall(
	t *testing.T,
	fileSet *token.FileSet,
	filename string,
	functionName string,
	call *ast.CallExpr,
	imports map[string]string,
) {
	t.Helper()
	if len(call.Args) != 7 {
		t.Errorf("%s: DebugSensitiveCF arguments = %d; want 7", fileSet.Position(call.Pos()), len(call.Args))
		return
	}
	policyCall, ok := call.Args[0].(*ast.CallExpr)
	if !ok || len(policyCall.Args) != 2 {
		t.Errorf("%s: sensitive policy is not an immediate two-input meet", fileSet.Position(call.Pos()))
	} else {
		selector, selectorOK := policyCall.Fun.(*ast.SelectorExpr)
		receiverOK := false
		if selectorOK && selector.Sel.Name == "diagnosticPolicyForContext" {
			switch {
			case filename == "toolloop.go" && functionName == "RunToolLoop":
				receiver, ok := selector.X.(*ast.SelectorExpr)
				if ok {
					configIdent, configOK := receiver.X.(*ast.Ident)
					receiverOK = configOK && receiver.Sel.Name == "Tools" &&
						configIdent.Name == "config"
				}
			case filename == "registry.go" && functionName == "logToolExecutionStart":
				receiverOK = p015b1IdentNamed(selector.X, "r")
			}
		}
		ctxIdent, ctxOK := policyCall.Args[0].(*ast.Ident)
		suppressedIdent, suppressedOK := policyCall.Args[1].(*ast.Ident)
		if !receiverOK || !ctxOK || ctxIdent.Name != "ctx" ||
			!suppressedOK || suppressedIdent.Name != "effectiveSuppressed" {
			t.Errorf(
				"%s: sensitive policy does not meet registry/context/effective suppression",
				fileSet.Position(call.Pos()),
			)
		}
	}
	class, classOK := p015b1ImportedLoggerConstant(call.Args[4], "Sensitivity", imports)
	domain, domainOK := p015b1ImportedLoggerConstant(call.Args[5], "ObservationDomain", imports)
	raw, rawOK := call.Args[6].(*ast.Ident)
	wantRaw := ""
	switch {
	case filename == "toolloop.go" && functionName == "RunToolLoop":
		wantRaw = "diagnosticArguments"
	case filename == "registry.go" && functionName == "logToolExecutionStart":
		wantRaw = "normalizedArguments"
	}
	if !classOK || class != "SensitivityToolArguments" ||
		!domainOK || domain != "ObservationDomainToolArguments" ||
		!rawOK || wantRaw == "" || raw.Name != wantRaw {
		t.Errorf("%s: sensitive central class/domain/raw argument changed", fileSet.Position(call.Pos()))
	}
}

func p015b1FmtPrintLike(name string) bool {
	switch name {
	case "Print", "Printf", "Println", "Fprint", "Fprintf", "Fprintln":
		return true
	default:
		return false
	}
}

func TestP015b1EffectiveSuppressionStructure(t *testing.T) {
	type assignmentSpec struct {
		file      string
		function  string
		leftIdent string
		config    bool
	}
	specs := []assignmentSpec{
		{file: "toolloop.go", function: "RunToolLoop", config: true},
		{file: "registry.go", function: "diagnosticPolicyForContext", leftIdent: "suppressed"},
		{file: "registry.go", function: "executeWithContext", leftIdent: "suppressLogDetails"},
		{file: "registry.go", function: "logToolExecutionStart", leftIdent: "suppressLogDetails"},
		{file: "registry.go", function: "executeResolvedToolWithContext", leftIdent: "suppressLogDetails"},
		{file: "registry_invocation.go", function: "DispatchClaimed", leftIdent: "suppressLogDetails"},
	}
	files := make(map[string]*ast.File)
	for _, filename := range []string{
		"toolloop.go", "registry.go", "registry_invocation.go", "subagent.go",
	} {
		parsed, err := parser.ParseFile(token.NewFileSet(), filename, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("ParseFile(%s) error = %v", filename, err)
		}
		files[filename] = parsed
	}
	for _, spec := range specs {
		function := p015b1FindFunction(t, files[spec.file], spec.function)
		p015b1AssertEffectiveSuppressionAssignment(t, spec, function)
	}

	runToolLoop := p015b1FindFunction(t, files["toolloop.go"], "RunToolLoop")
	configReads := 0
	dispatchUses := 0
	bindingCalls := 0
	ast.Inspect(runToolLoop.Body, func(node ast.Node) bool {
		if selector, ok := node.(*ast.SelectorExpr); ok && selector.Sel.Name == "SuppressToolArguments" {
			if identifier, identifierOK := selector.X.(*ast.Ident); identifierOK && identifier.Name == "config" {
				configReads++
			}
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if p015b1IdentNamed(call.Fun, "WithToolLogDetailsSuppressed") {
			bindingCalls++
			if len(call.Args) != 1 || !p015b1IdentNamed(call.Args[0], "ctx") {
				t.Errorf("RunToolLoop suppression binding changed")
			}
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "DispatchClaimed" {
			return true
		}
		dispatchUses++
		if len(call.Args) == 0 || !p015b1IdentNamed(call.Args[len(call.Args)-1], "effectiveSuppressed") {
			t.Errorf("RunToolLoop DispatchClaimed does not propagate effectiveSuppressed")
		}
		return true
	})
	if configReads != 1 || dispatchUses != 1 || bindingCalls != 1 {
		t.Fatalf(
			"RunToolLoop suppression reads/dispatches/bindings = %d/%d/%d; want 1/1/1",
			configReads,
			dispatchUses,
			bindingCalls,
		)
	}

	legacy := p015b1FindFunction(t, files["subagent.go"], "legacyTaskRunnerSnapshot")
	legacyFields := 0
	ast.Inspect(legacy.Body, func(node ast.Node) bool {
		keyValue, ok := node.(*ast.KeyValueExpr)
		if !ok || !p015b1IdentNamed(keyValue.Key, "SuppressToolArguments") {
			return true
		}
		legacyFields++
		call, ok := keyValue.Value.(*ast.CallExpr)
		if !ok || !p015b1IdentNamed(call.Fun, "ToolLogDetailsSuppressed") ||
			len(call.Args) != 1 || !p015b1IdentNamed(call.Args[0], "ctx") {
			t.Errorf("legacy SubagentManager fallback does not copy inherited suppression")
		}
		return true
	})
	if legacyFields != 1 {
		t.Fatalf("legacy SubagentManager suppression fields = %d; want 1", legacyFields)
	}
}

func p015b1FindFunction(t *testing.T, file *ast.File, name string) *ast.FuncDecl {
	t.Helper()
	for _, declaration := range file.Decls {
		if function, ok := declaration.(*ast.FuncDecl); ok && function.Name.Name == name {
			return function
		}
	}
	t.Fatalf("function %s not found", name)
	return nil
}

func p015b1AssertEffectiveSuppressionAssignment(
	t *testing.T,
	spec struct {
		file      string
		function  string
		leftIdent string
		config    bool
	},
	function *ast.FuncDecl,
) {
	t.Helper()
	matches := 0
	ast.Inspect(function.Body, func(node ast.Node) bool {
		assignment, ok := node.(*ast.AssignStmt)
		if !ok || assignment.Tok != token.DEFINE || len(assignment.Lhs) != 1 ||
			!p015b1IdentNamed(assignment.Lhs[0], "effectiveSuppressed") || len(assignment.Rhs) != 1 {
			return true
		}
		matches++
		expression, ok := assignment.Rhs[0].(*ast.BinaryExpr)
		if !ok || expression.Op != token.LOR {
			t.Errorf("%s:%s effectiveSuppressed is not false-dominating OR", spec.file, spec.function)
			return true
		}
		leftOK := p015b1IdentNamed(expression.X, spec.leftIdent)
		if spec.config {
			selector, selectorOK := expression.X.(*ast.SelectorExpr)
			leftOK = selectorOK && selector.Sel.Name == "SuppressToolArguments" &&
				p015b1IdentNamed(selector.X, "config")
		}
		call, callOK := expression.Y.(*ast.CallExpr)
		rightOK := callOK && p015b1IdentNamed(call.Fun, "ToolLogDetailsSuppressed") &&
			len(call.Args) == 1 && p015b1IdentNamed(call.Args[0], "ctx")
		if !leftOK || !rightOK {
			t.Errorf("%s:%s effective suppression inputs changed", spec.file, spec.function)
		}
		return true
	})
	if matches != 1 {
		t.Errorf("%s:%s effective suppression assignments = %d; want 1", spec.file, spec.function, matches)
	}
}

func p015b1IdentNamed(expression ast.Expr, name string) bool {
	identifier, ok := expression.(*ast.Ident)
	return ok && identifier.Name == name
}

func p015b1SelectorCall(call *ast.CallExpr) (string, string, bool) {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", "", false
	}
	identifier, ok := selector.X.(*ast.Ident)
	if !ok {
		return "", selector.Sel.Name, true
	}
	return identifier.Name, selector.Sel.Name, true
}

func p015b1LegacyLoggerCall(name string) bool {
	for _, prefix := range []string{"Debug", "Info", "Warn", "Error", "Fatal"} {
		if name == prefix || name == prefix+"C" || name == prefix+"F" ||
			name == prefix+"CF" || name == prefix+"f" {
			return true
		}
	}
	return false
}

func p015b1SafeSinkIndexes(name string) (component, message int, ok bool) {
	switch name {
	case "DebugSafeCF", "InfoSafeCF", "WarnSafeCF", "ErrorSafeCF", "FatalSafeCF":
		return 0, 1, true
	case "DebugSensitiveCF":
		return 1, 2, true
	default:
		return 0, 0, false
	}
}

func p015b1LoggerConstant(expression ast.Expr, prefix string) (string, bool) {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || !strings.HasPrefix(selector.Sel.Name, prefix) {
		return "", false
	}
	identifier, ok := selector.X.(*ast.Ident)
	if !ok || identifier.Name != "logger" {
		return "", false
	}
	return selector.Sel.Name, true
}

func p015b1AssertSinkArgumentSafe(
	t *testing.T,
	fileSet *token.FileSet,
	expression ast.Expr,
) {
	t.Helper()
	ast.Inspect(expression, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.MapType:
			t.Errorf("%s: dynamic map in logger sink argument", fileSet.Position(value.Pos()))
		case *ast.BasicLit:
			if value.Kind == token.STRING {
				t.Errorf("%s: raw string literal in logger sink argument", fileSet.Position(value.Pos()))
			}
		case *ast.CallExpr:
			packageName, callName, selector := p015b1SelectorCall(value)
			if selector && (packageName == "fmt" ||
				packageName == "debug" && callName == "Stack" ||
				packageName == "json" && callName == "Marshal" ||
				packageName == "utils" && callName == "Truncate") {
				t.Errorf(
					"%s: forbidden %s.%s in logger sink argument",
					fileSet.Position(value.Pos()), packageName, callName,
				)
			}
			if selector {
				switch callName {
				case "Error", "Unwrap", "Is", "As", "String", "Format", "ContentForLLM":
					t.Errorf(
						"%s: forbidden .%s in logger sink argument",
						fileSet.Position(value.Pos()), callName,
					)
				}
				if strings.HasPrefix(callName, "Recover") {
					t.Errorf(
						"%s: recovery call in logger sink argument",
						fileSet.Position(value.Pos()),
					)
				}
			}
			if identifier, ok := value.Fun.(*ast.Ident); ok &&
				strings.HasPrefix(identifier.Name, "Recover") {
				t.Errorf(
					"%s: recovery call in logger sink argument",
					fileSet.Position(value.Pos()),
				)
			}
		}
		return true
	})
}

func p015b1AssertFunctionalAssistantMarshal(
	t *testing.T,
	fileSet *token.FileSet,
	filename string,
	function string,
	call *ast.CallExpr,
) {
	t.Helper()
	valid := filename == "toolloop.go" && function == "RunToolLoop" && len(call.Args) == 1
	if valid {
		argument, ok := call.Args[0].(*ast.SelectorExpr)
		if !ok {
			valid = false
		} else {
			identifier, identifierOK := argument.X.(*ast.Ident)
			valid = identifierOK && identifier.Name == "tc" && argument.Sel.Name == "Arguments"
		}
	}
	if !valid {
		t.Errorf(
			"%s: json.Marshal is not the exact functional assistant argument serialization",
			fileSet.Position(call.Pos()),
		)
	}
}

func p015b1ToolSubagentSinkKey(sink p015b1ToolSubagentSink) string {
	return sink.file + "|" + sink.function + "|" + sink.emitter + "|" +
		sink.component + "|" + sink.message
}
