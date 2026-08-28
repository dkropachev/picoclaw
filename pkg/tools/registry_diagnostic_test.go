package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/media"
)

func TestToolRegistryDiagnosticPolicyTruthTable(t *testing.T) {
	enabled := logger.NewDiagnosticPolicy(true, logger.DEBUG)
	disabled := logger.NewDiagnosticPolicy(false, logger.DEBUG)
	zero := logger.DiagnosticPolicy{}

	enabledContext, revokeEnabled := logger.BindRootDiagnosticPolicy(
		context.Background(),
		enabled,
	)
	defer revokeEnabled()
	disabledContext, revokeDisabled := logger.BindRootDiagnosticPolicy(
		context.Background(),
		disabled,
	)
	defer revokeDisabled()
	inheritedSuppressedContext := WithToolLogDetailsSuppressed(enabledContext)

	tests := []struct {
		name       string
		registry   *ToolRegistry
		ctx        context.Context
		suppressed bool
		want       logger.DiagnosticPolicy
	}{
		{
			name:     "owner and request enabled",
			registry: NewToolRegistryWithDiagnosticPolicy(enabled),
			ctx:      enabledContext,
			want:     enabled,
		},
		{
			name:     "request false dominates",
			registry: NewToolRegistryWithDiagnosticPolicy(enabled),
			ctx:      disabledContext,
			want:     disabled,
		},
		{
			name:     "owner false dominates",
			registry: NewToolRegistryWithDiagnosticPolicy(disabled),
			ctx:      enabledContext,
			want:     disabled,
		},
		{
			name:     "compatibility owner is zero",
			registry: NewToolRegistry(),
			ctx:      enabledContext,
			want:     zero,
		},
		{
			name:     "missing request provenance",
			registry: NewToolRegistryWithDiagnosticPolicy(enabled),
			ctx:      context.Background(),
			want:     zero,
		},
		{
			name:     "nil request provenance",
			registry: NewToolRegistryWithDiagnosticPolicy(enabled),
			ctx:      nil,
			want:     zero,
		},
		{
			name:       "suppression false cap",
			registry:   NewToolRegistryWithDiagnosticPolicy(enabled),
			ctx:        enabledContext,
			suppressed: true,
			want:       zero,
		},
		{
			name:     "inherited suppression false cap",
			registry: NewToolRegistryWithDiagnosticPolicy(enabled),
			ctx:      inheritedSuppressedContext,
			want:     zero,
		},
		{
			name:     "nil registry",
			registry: nil,
			ctx:      enabledContext,
			want:     zero,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.registry.diagnosticPolicyForContext(
				test.ctx,
				test.suppressed,
			); got != test.want {
				t.Fatalf("diagnosticPolicyForContext() = %#v, want %#v", got, test.want)
			}
		})
	}

	revokedContext, revoke := logger.BindRootDiagnosticPolicy(
		context.Background(),
		enabled,
	)
	revoke()
	if got := NewToolRegistryWithDiagnosticPolicy(enabled).
		diagnosticPolicyForContext(revokedContext, false); got != zero {
		t.Fatalf("revoked policy = %#v, want zero", got)
	}
}

func TestToolRegistryDiagnosticPolicyPropagation(t *testing.T) {
	enabled := logger.NewDiagnosticPolicy(true, logger.DEBUG)
	ctx, revoke := logger.BindRootDiagnosticPolicy(context.Background(), enabled)
	defer revoke()

	compatibility := NewToolRegistryWithDiagnosticPolicy(enabled)
	clone := compatibility.Clone()
	if got := clone.diagnosticPolicyForContext(ctx, false); got != enabled {
		t.Fatalf("compatibility clone policy = %#v", got)
	}

	owned, err := NewOwnedToolRegistryWithDiagnosticPolicy(
		factoryTestOwner(ToolOwnerScopeRegistry, "diagnostic-owned"),
		enabled,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owned.Close() }()
	failClosedClone := owned.Clone()
	if failClosedClone.Count() != 0 {
		t.Fatal("owned shallow clone did not remain empty")
	}
	if _, isOwned := failClosedClone.Owner(); isOwned {
		t.Fatal("owned shallow clone duplicated lifecycle ownership")
	}
	if got := failClosedClone.diagnosticPolicyForContext(ctx, false); got != enabled {
		t.Fatalf("owned fail-closed clone policy = %#v", got)
	}

	seenDuringFactory := make(chan logger.DiagnosticPolicy, 3)
	prototype := newMockTool("diagnostic_factory", "diagnostic factory")
	factory := mustFactoryForTool(
		t,
		prototype,
		ToolTraits{},
		func(buildContext ToolBuildContext) (Tool, error) {
			destination := destinationRegistryForBuild(buildContext)
			if destination == nil {
				return nil, fmt.Errorf("destination registry unavailable")
			}
			seenDuringFactory <- destination.diagnosticPolicyForContext(ctx, false)
			return newMockTool("diagnostic_factory", "diagnostic factory"), nil
		},
	)
	if registerErr := owned.RegisterFactory(factory); registerErr != nil {
		t.Fatal(registerErr)
	}

	full, err := owned.InstantiateForOwner(
		factoryTestOwner(ToolOwnerScopeAgent, "diagnostic-full"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = full.Close() }()
	selected, err := owned.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeAgent, "diagnostic-selected"),
		[]string{"diagnostic_factory"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = selected.Close() }()
	empty, err := owned.InstantiateForOwnerSelection(
		factoryTestOwner(ToolOwnerScopeAgent, "diagnostic-empty"),
		[]string{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = empty.Close() }()

	for name, registry := range map[string]*ToolRegistry{
		"full":     full,
		"selected": selected,
		"empty":    empty,
	} {
		if got := registry.diagnosticPolicyForContext(ctx, false); got != enabled {
			t.Fatalf("%s destination policy = %#v", name, got)
		}
	}
	for index := 0; index < 3; index++ {
		if got := <-seenDuringFactory; got != enabled {
			t.Fatalf("factory %d observed policy %#v", index, got)
		}
	}
}

func TestToolRegistryDiagnosticPolicyConcurrentRevocationAndOppositeCaps(t *testing.T) {
	enabled := logger.NewDiagnosticPolicy(true, logger.DEBUG)
	disabled := logger.NewDiagnosticPolicy(false, logger.DEBUG)
	zero := logger.DiagnosticPolicy{}
	ctx, revoke := logger.BindRootDiagnosticPolicy(context.Background(), enabled)
	enabledOwner := NewToolRegistryWithDiagnosticPolicy(enabled)
	disabledOwner := NewToolRegistryWithDiagnosticPolicy(disabled)

	var wait sync.WaitGroup
	var widened atomic.Bool
	started := make(chan struct{})
	start := make(chan struct{})
	sampled := make(chan struct{})
	for index := 0; index < 32; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			started <- struct{}{}
			<-start
			for iteration := 0; iteration < 1000; iteration++ {
				enabledResult := enabledOwner.diagnosticPolicyForContext(ctx, false)
				if enabledResult != enabled && enabledResult != zero {
					widened.Store(true)
				}
				disabledResult := disabledOwner.diagnosticPolicyForContext(ctx, false)
				if disabledResult != disabled && disabledResult != zero {
					widened.Store(true)
				}
				if disabledResult == enabled {
					widened.Store(true)
				}
				if iteration == 0 {
					sampled <- struct{}{}
				}
			}
		}()
	}
	for range 32 {
		<-started
	}
	close(start)
	for range 32 {
		<-sampled
	}
	revoke()
	wait.Wait()
	if widened.Load() {
		t.Fatal("concurrent revocation or opposite owner cap widened diagnostic policy")
	}
}

type diagnosticPanicNameTool struct{}

func (*diagnosticPanicNameTool) Name() string        { panic("name panic") }
func (*diagnosticPanicNameTool) Description() string { return "panic name" }
func (*diagnosticPanicNameTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (*diagnosticPanicNameTool) Execute(context.Context, map[string]any) *ToolResult {
	return SilentResult("unused")
}

type diagnosticPanicMediaTool struct {
	*mockRegistryTool
}

func (*diagnosticPanicMediaTool) SetMediaStore(media.MediaStore) {
	panic("media store panic")
}

func TestToolRegistryRegistrationPanicReleasesMutex(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*ToolRegistry)
		tool    Tool
	}{
		{
			name: "Name",
			tool: &diagnosticPanicNameTool{},
		},
		{
			name: "SetMediaStore",
			prepare: func(registry *ToolRegistry) {
				registry.SetMediaStore(media.NewFileMediaStore())
			},
			tool: &diagnosticPanicMediaTool{
				mockRegistryTool: newMockTool("panic_media", "panic media"),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := NewToolRegistry()
			if test.prepare != nil {
				test.prepare(registry)
			}
			panicked := false
			func() {
				defer func() {
					panicked = recover() != nil
				}()
				registry.Register(test.tool)
			}()
			if !panicked {
				t.Fatal("Register() did not propagate caller panic")
			}

			responsive := make(chan struct{})
			go func() {
				_ = registry.Count()
				registry.TickTTL()
				close(responsive)
			}()
			select {
			case <-responsive:
			case <-time.After(time.Second):
				t.Fatal("registration panic left registry mutex locked")
			}
		})
	}
}

type diagnosticCountingError struct {
	calls  *atomic.Int64
	canary string
}

type diagnosticNestedCapTool struct {
	name          string
	inner         *ToolRegistry
	innerToolName string
	innerCanary   string
	directCanary  string
	rebind        bool
	rebindPolicy  logger.DiagnosticPolicy
	entered       chan struct{}
	release       <-chan struct{}
	seenPolicy    chan logger.DiagnosticPolicy
}

func (tool *diagnosticNestedCapTool) Name() string { return tool.name }

func (*diagnosticNestedCapTool) Description() string { return "nested diagnostic cap probe" }

func (*diagnosticNestedCapTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (tool *diagnosticNestedCapTool) Execute(
	ctx context.Context,
	_ map[string]any,
) *ToolResult {
	if tool.entered != nil {
		close(tool.entered)
	}
	if tool.release != nil {
		<-tool.release
	}
	if tool.rebind {
		var revoke func()
		ctx, revoke = logger.BindRootDiagnosticPolicy(ctx, tool.rebindPolicy)
		defer revoke()
	}
	directArguments := map[string]any{"direct": tool.directCanary}
	logger.DebugSensitiveCF(
		logger.DiagnosticPolicyFromContext(ctx),
		logger.ComponentTool,
		logger.DiagnosticMessageToolArguments,
		logger.NewSafeFields(),
		logger.SensitivityToolArguments,
		logger.ObservationDomainToolArguments,
		directArguments,
	)
	if tool.seenPolicy != nil {
		tool.seenPolicy <- tool.inner.diagnosticPolicyForContext(ctx, false)
	}
	return tool.inner.Execute(
		ctx,
		tool.innerToolName,
		map[string]any{"inner": tool.innerCanary},
	)
}

type diagnosticRetainedContextAsyncTool struct {
	name     string
	captured chan context.Context
}

func (tool *diagnosticRetainedContextAsyncTool) Name() string { return tool.name }

func (*diagnosticRetainedContextAsyncTool) Description() string {
	return "retained diagnostic context probe"
}

func (*diagnosticRetainedContextAsyncTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (*diagnosticRetainedContextAsyncTool) Execute(
	context.Context,
	map[string]any,
) *ToolResult {
	return ErrorResult("unexpected synchronous execution")
}

func (tool *diagnosticRetainedContextAsyncTool) ExecuteAsync(
	ctx context.Context,
	_ map[string]any,
	_ AsyncCallback,
) *ToolResult {
	tool.captured <- ctx
	return AsyncResult("diagnostic context captured")
}

func TestToolRegistryNestedDiagnosticCapCannotWiden(t *testing.T) {
	enabled := logger.NewDiagnosticPolicy(true, logger.DEBUG)
	disabled := logger.NewDiagnosticPolicy(false, logger.DEBUG)
	zero := logger.DiagnosticPolicy{}
	request, revokeRequest := logger.BindRootDiagnosticPolicy(context.Background(), enabled)
	defer revokeRequest()

	initialLevel := logger.GetLevel()
	logger.SetLevel(logger.DEBUG)
	logger.DisableConsole()
	defer func() {
		logger.DisableFileLogging()
		logger.EnableConsole()
		logger.SetLevel(initialLevel)
	}()

	tests := []struct {
		name        string
		outerCap    logger.DiagnosticPolicy
		wantPolicy  logger.DiagnosticPolicy
		prepared    bool
		wantPreview bool
	}{
		{
			name: "enabled positive control", outerCap: enabled,
			wantPolicy: enabled, wantPreview: true,
		},
		{
			name: "enabled prepared positive control", outerCap: enabled,
			wantPolicy: enabled, prepared: true, wantPreview: true,
		},
		{name: "disabled direct", outerCap: disabled, wantPolicy: disabled},
		{name: "disabled prepared", outerCap: disabled, wantPolicy: disabled, prepared: true},
		{name: "zero direct", outerCap: zero, wantPolicy: zero},
		{name: "zero prepared", outerCap: zero, wantPolicy: zero, prepared: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			innerCanary := "P015_NESTED_OWNER_INNER_" + strings.ReplaceAll(test.name, " ", "_")
			directCanary := "P015_NESTED_OWNER_DIRECT_" + strings.ReplaceAll(test.name, " ", "_")
			inner := NewToolRegistryWithDiagnosticPolicy(enabled)
			innerResult := NewToolResult("nested diagnostic result")
			inner.Register(&mockRegistryTool{
				name: "nested_owner_inner", desc: "nested owner inner",
				params: map[string]any{"type": "object"}, result: innerResult,
			})
			seenPolicy := make(chan logger.DiagnosticPolicy, 1)
			outer := NewToolRegistryWithDiagnosticPolicy(test.outerCap)
			outer.Register(&diagnosticNestedCapTool{
				name: "nested_owner_outer", inner: inner,
				innerToolName: "nested_owner_inner", innerCanary: innerCanary,
				directCanary: directCanary, rebind: true, rebindPolicy: enabled,
				seenPolicy: seenPolicy,
			})

			var result *ToolResult
			logged := captureRegistryDiagnostics(t, func() {
				if !test.prepared {
					result = outer.Execute(request, "nested_owner_outer", map[string]any{})
					return
				}
				catalog, err := outer.SnapshotModelToolCatalog()
				if err != nil {
					t.Fatal(err)
				}
				invocation, err := catalog.PrepareInvocation(
					"nested_owner_outer",
					map[string]any{},
				)
				if err != nil {
					t.Fatal(err)
				}
				claim, err := outer.ClaimPrepared(request, invocation)
				if err != nil {
					t.Fatal(err)
				}
				result, err = outer.DispatchClaimed(request, claim, "", "", nil, false)
				if err != nil {
					t.Fatal(err)
				}
			})
			if result != innerResult {
				t.Fatalf("nested functional result = %#v, want exact %#v", result, innerResult)
			}
			if got := <-seenPolicy; got != test.wantPolicy {
				t.Fatalf("nested policy = %#v, want %#v", got, test.wantPolicy)
			}
			for _, canary := range []string{innerCanary, directCanary} {
				if got := strings.Contains(logged, canary); got != test.wantPreview {
					t.Fatalf(
						"nested preview for %q = %t, want %t: %s",
						canary,
						got,
						test.wantPreview,
						logged,
					)
				}
			}
		})
	}
}

func TestToolRegistryNestedDiagnosticCapTracksLiveRequestRevoke(t *testing.T) {
	const (
		innerCanary  = "P015_NESTED_REVOKE_INNER_41cad2"
		directCanary = "P015_NESTED_REVOKE_DIRECT_8de475"
	)
	enabled := logger.NewDiagnosticPolicy(true, logger.DEBUG)
	request, revokeRequest := logger.BindRootDiagnosticPolicy(context.Background(), enabled)
	defer revokeRequest()
	inner := NewToolRegistryWithDiagnosticPolicy(enabled)
	inner.Register(&mockRegistryTool{
		name: "nested_revoke_inner", desc: "nested revoke inner",
		params: map[string]any{"type": "object"}, result: NewToolResult("done"),
	})
	entered := make(chan struct{})
	release := make(chan struct{})
	seenPolicy := make(chan logger.DiagnosticPolicy, 1)
	outer := NewToolRegistryWithDiagnosticPolicy(enabled)
	outer.Register(&diagnosticNestedCapTool{
		name: "nested_revoke_outer", inner: inner,
		innerToolName: "nested_revoke_inner", innerCanary: innerCanary,
		directCanary: directCanary, entered: entered, release: release,
		seenPolicy: seenPolicy,
	})

	initialLevel := logger.GetLevel()
	logger.SetLevel(logger.DEBUG)
	logger.DisableConsole()
	defer func() {
		logger.DisableFileLogging()
		logger.EnableConsole()
		logger.SetLevel(initialLevel)
	}()
	logged := captureRegistryDiagnostics(t, func() {
		done := make(chan *ToolResult, 1)
		go func() {
			done <- outer.Execute(request, "nested_revoke_outer", map[string]any{})
		}()
		<-entered
		revokeRequest()
		close(release)
		if result := <-done; result == nil || result.IsError {
			t.Fatalf("nested revoked result = %#v", result)
		}
	})
	if got := <-seenPolicy; got != (logger.DiagnosticPolicy{}) {
		t.Fatalf("nested policy after request revoke = %#v, want zero", got)
	}
	for _, canary := range []string{innerCanary, directCanary} {
		if strings.Contains(logged, canary) {
			t.Fatalf("request revoke failed to suppress %q: %s", canary, logged)
		}
	}
}

func TestToolRegistryRetainedAsyncContextIsRevoked(t *testing.T) {
	const canary = "P015_RETAINED_ASYNC_CONTEXT_7af10e"
	enabled := logger.NewDiagnosticPolicy(true, logger.DEBUG)
	request, revokeRequest := logger.BindRootDiagnosticPolicy(context.Background(), enabled)
	defer revokeRequest()
	captured := make(chan context.Context, 1)
	outer := NewToolRegistryWithDiagnosticPolicy(enabled)
	outer.Register(&diagnosticRetainedContextAsyncTool{
		name: "retained_context_outer", captured: captured,
	})
	inner := NewToolRegistryWithDiagnosticPolicy(enabled)
	inner.Register(&mockRegistryTool{
		name: "retained_context_inner", desc: "retained context inner",
		params: map[string]any{"type": "object"}, result: NewToolResult("done"),
	})

	initialLevel := logger.GetLevel()
	logger.SetLevel(logger.DEBUG)
	logger.DisableConsole()
	defer func() {
		logger.DisableFileLogging()
		logger.EnableConsole()
		logger.SetLevel(initialLevel)
	}()
	logged := captureRegistryDiagnostics(t, func() {
		result := outer.ExecuteWithContext(
			request,
			"retained_context_outer",
			map[string]any{},
			"",
			"",
			func(context.Context, *ToolResult) {},
		)
		if result == nil || !result.Async {
			t.Fatalf("async functional result = %#v", result)
		}
		retained := <-captured
		if got := logger.DiagnosticPolicyFromContext(retained); got != (logger.DiagnosticPolicy{}) {
			t.Fatalf("retained context policy = %#v, want zero", got)
		}
		inner.Execute(
			retained,
			"retained_context_inner",
			map[string]any{"retained": canary},
		)
	})
	if strings.Contains(logged, canary) {
		t.Fatalf("retained async context previewed after return: %s", logged)
	}
}

func (err *diagnosticCountingError) Error() string {
	err.calls.Add(1)
	return err.canary
}

func TestToolRegistrySafeDiagnosticsAndArgumentPreviewCaps(t *testing.T) {
	const (
		argumentCanary = "P015_REGISTRY_ARGUMENT_7f4cb49f"
		resultCanary   = "P015_REGISTRY_RESULT_a81d227c"
		nameCanary     = "P015_REGISTRY_NAME_41e7d88b"
		errorCanary    = "P015_REGISTRY_ERROR_bbb93a8d"
	)
	enabled := logger.NewDiagnosticPolicy(true, logger.DEBUG)
	disabled := logger.NewDiagnosticPolicy(false, logger.DEBUG)
	enabledContext, revokeEnabled := logger.BindRootDiagnosticPolicy(
		context.Background(),
		enabled,
	)
	defer revokeEnabled()
	disabledContext, revokeDisabled := logger.BindRootDiagnosticPolicy(
		context.Background(),
		disabled,
	)
	defer revokeDisabled()
	revokedContext, revokeBeforeUse := logger.BindRootDiagnosticPolicy(
		context.Background(),
		enabled,
	)
	revokeBeforeUse()

	initialLevel := logger.GetLevel()
	logger.SetLevel(logger.DEBUG)
	logger.DisableConsole()
	logger.DisableFileLogging()
	defer func() {
		logger.DisableFileLogging()
		logger.EnableConsole()
		logger.SetLevel(initialLevel)
	}()

	tests := []struct {
		name        string
		ownerPolicy logger.DiagnosticPolicy
		ctx         context.Context
		suppressed  bool
		wantPreview bool
	}{
		{
			name:        "enabled owner and request",
			ownerPolicy: enabled,
			ctx:         enabledContext,
			wantPreview: true,
		},
		{
			name:        "disabled owner",
			ownerPolicy: disabled,
			ctx:         enabledContext,
		},
		{
			name:        "disabled request",
			ownerPolicy: enabled,
			ctx:         disabledContext,
		},
		{
			name:        "missing provenance",
			ownerPolicy: enabled,
			ctx:         context.Background(),
		},
		{
			name:        "revoked provenance",
			ownerPolicy: enabled,
			ctx:         revokedContext,
		},
		{
			name:        "suppression",
			ownerPolicy: enabled,
			ctx:         enabledContext,
			suppressed:  true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var errorCalls atomic.Int64
			registry := NewToolRegistryWithDiagnosticPolicy(test.ownerPolicy)
			tool := &mockRegistryTool{
				name:   nameCanary,
				desc:   "diagnostic test tool",
				params: map[string]any{"type": "object"},
				result: &ToolResult{
					ForLLM:  resultCanary,
					IsError: true,
					Err:     &diagnosticCountingError{calls: &errorCalls, canary: errorCanary},
				},
			}
			logged := captureRegistryDiagnostics(t, func() {
				registry.Register(tool)
				result := registry.executeWithContext(
					test.ctx,
					nameCanary,
					map[string]any{"secret": argumentCanary},
					"",
					"",
					nil,
					test.suppressed,
				)
				if result != tool.result {
					t.Fatal("registry changed the functional tool result pointer")
				}
				registry.Unregister(nameCanary)
			})
			if got := strings.Contains(logged, argumentCanary); got != test.wantPreview {
				t.Fatalf("argument preview presence = %v, want %v; log=%s", got, test.wantPreview, logged)
			}
			for _, forbidden := range []string{nameCanary, resultCanary, errorCanary} {
				if strings.Contains(logged, forbidden) {
					t.Fatalf("raw diagnostic canary %q entered log: %s", forbidden, logged)
				}
			}
			if errorCalls.Load() != 0 {
				t.Fatalf("logging invoked hostile Error %d times", errorCalls.Load())
			}
			for _, useful := range []string{
				"identity_tool_digest",
				"tool_arguments_digest",
				"tool_result_digest",
				"error_digest",
				"suppressed",
			} {
				if !strings.Contains(logged, useful) {
					t.Fatalf("safe field %q missing from log: %s", useful, logged)
				}
			}
		})
	}
}

func TestToolRegistryPanicLogOmitsRawValueAndStack(t *testing.T) {
	const (
		panicCanary = "P015_REGISTRY_PANIC_095ac74a"
		nameCanary  = "P015_REGISTRY_PANIC_NAME_dbd6bb1b"
	)
	initialLevel := logger.GetLevel()
	logger.SetLevel(logger.DEBUG)
	logger.DisableConsole()
	defer func() {
		logger.DisableFileLogging()
		logger.EnableConsole()
		logger.SetLevel(initialLevel)
	}()

	registry := NewToolRegistry()
	registry.Register(&mockPanicTool{name: nameCanary, panicValue: panicCanary})
	var result *ToolResult
	logged := captureRegistryDiagnostics(t, func() {
		result = registry.Execute(context.Background(), nameCanary, nil)
	})
	if result == nil || !strings.Contains(result.ForLLM, panicCanary) {
		t.Fatalf("functional panic result changed: %#v", result)
	}
	for _, forbidden := range []string{panicCanary, nameCanary, "goroutine ", "stack"} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("panic log contains %q: %s", forbidden, logged)
		}
	}
	for _, useful := range []string{"panic_digest", "tool_result_digest", "identity_tool_digest"} {
		if !strings.Contains(logged, useful) {
			t.Fatalf("panic safe field %q missing: %s", useful, logged)
		}
	}
}

func captureRegistryDiagnostics(t *testing.T, emit func()) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "registry.log")
	logger.DisableFileLogging()
	if err := logger.EnableFileLogging(path); err != nil {
		t.Fatalf("EnableFileLogging() error = %v", err)
	}
	emit()
	logger.DisableFileLogging()
	logged, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(log) error = %v", err)
	}
	return string(logged)
}
