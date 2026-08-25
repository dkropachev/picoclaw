package tools

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	picomcp "github.com/sipeed/picoclaw/pkg/mcp"
	"github.com/sipeed/picoclaw/pkg/media"
)

type mcpFactoryTestCall struct {
	server string
	tool   string
	args   map[string]any
}

type mcpFactoryTestManager struct {
	mu         sync.Mutex
	result     *sdkmcp.CallToolResult
	err        error
	calls      []mcpFactoryTestCall
	closeCalls atomic.Int64
}

func (manager *mcpFactoryTestManager) CallTool(
	_ context.Context,
	serverName, toolName string,
	arguments map[string]any,
) (*sdkmcp.CallToolResult, error) {
	manager.mu.Lock()
	manager.calls = append(manager.calls, mcpFactoryTestCall{
		server: serverName, tool: toolName, args: arguments,
	})
	result, err := manager.result, manager.err
	manager.mu.Unlock()
	if result == nil && err == nil {
		result = &sdkmcp.CallToolResult{Content: []sdkmcp.Content{
			&sdkmcp.TextContent{Text: "factory result"},
		}}
	}
	return result, err
}

func (manager *mcpFactoryTestManager) Close() error {
	manager.closeCalls.Add(1)
	return nil
}

func (manager *mcpFactoryTestManager) snapshotCalls() []mcpFactoryTestCall {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return append([]mcpFactoryTestCall(nil), manager.calls...)
}

type mcpFactorySchemaStruct struct {
	Type       string                    `json:"type"`
	Properties map[string]map[string]any `json:"properties"`
	Required   []string                  `json:"required"`
}

type panickingMCPFactorySchema struct{}

func (panickingMCPFactorySchema) MarshalJSON() ([]byte, error) {
	panic("untrusted schema panic")
}

func TestNewMCPToolWithFactorySchemaRepresentations(t *testing.T) {
	structSchema := mcpFactorySchemaStruct{
		Type: "object",
		Properties: map[string]map[string]any{
			"count": {"type": "integer", "minimum": 1},
		},
		Required: []string{"count"},
	}
	tests := []struct {
		name   string
		schema any
		check  func(*testing.T, map[string]any)
	}{
		{
			name: "nil",
			check: func(t *testing.T, schema map[string]any) {
				t.Helper()
				if schema["type"] != "object" ||
					len(schema["properties"].(map[string]any)) != 0 ||
					len(schema["required"].([]string)) != 0 {
					t.Fatalf("nil schema snapshot = %#v", schema)
				}
			},
		},
		{
			name: "direct map",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "minLength": 1},
				},
				"required": []string{"query"},
			},
			check: func(t *testing.T, schema map[string]any) {
				t.Helper()
				query := schema["properties"].(map[string]any)["query"].(map[string]any)
				if query["type"] != "string" || query["minLength"] != 1 {
					t.Fatalf("direct map snapshot = %#v", schema)
				}
			},
		},
		{
			name: "raw message",
			schema: json.RawMessage(
				`{"type":"object","properties":{"limit":{"type":"integer"}},"required":["limit"]}`,
			),
			check: func(t *testing.T, schema map[string]any) {
				t.Helper()
				limit := schema["properties"].(map[string]any)["limit"].(map[string]any)
				if limit["type"] != "integer" {
					t.Fatalf("raw schema snapshot = %#v", schema)
				}
			},
		},
		{
			name: "bytes",
			schema: []byte(
				`{"type":"object","properties":{"enabled":{"type":"boolean"}}}`,
			),
			check: func(t *testing.T, schema map[string]any) {
				t.Helper()
				enabled := schema["properties"].(map[string]any)["enabled"].(map[string]any)
				if enabled["type"] != "boolean" {
					t.Fatalf("byte schema snapshot = %#v", schema)
				}
			},
		},
		{
			name:   "marshalable struct",
			schema: structSchema,
			check: func(t *testing.T, schema map[string]any) {
				t.Helper()
				count := schema["properties"].(map[string]any)["count"].(map[string]any)
				if count["type"] != "integer" || count["minimum"] != json.Number("1") {
					t.Fatalf("struct schema snapshot = %#v", schema)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := &mcpFactoryTestManager{}
			remote := &sdkmcp.Tool{
				Name: "search", Description: "Search repositories", InputSchema: test.schema,
			}
			live, factory, err := NewMCPToolWithFactory(
				manager,
				"GitHub Server",
				remote,
				"  /tmp/mcp-workspace  ",
				123,
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			if live == nil || factory == nil {
				t.Fatalf("factory result = live:%#v factory:%#v", live, factory)
			}
			if got, want := live.Name(), picomcp.CanonicalToolName("GitHub Server", "search"); got != want {
				t.Fatalf("canonical name = %q, want %q", got, want)
			}
			if live.Description() != "[MCP:GitHub Server] Search repositories" {
				t.Fatalf("description = %q", live.Description())
			}
			if server, tool := live.MCPIdentity(); server != "GitHub Server" || tool != "search" {
				t.Fatalf("identity = %q/%q", server, tool)
			}
			metadata := live.PromptMetadata()
			if metadata != (PromptMetadata{
				Layer: ToolPromptLayerCapability, Slot: ToolPromptSlotMCP,
				Source: "mcp:" + picomcp.CanonicalToolNameComponent("GitHub Server"),
			}) {
				t.Fatalf("prompt metadata = %#v", metadata)
			}
			test.check(t, live.Parameters())
			if actual, descriptorErr := safeToolDescriptor(live); descriptorErr != nil ||
				!descriptorsEqual(actual, factory.Descriptor()) {
				t.Fatalf("live/factory descriptor parity = %#v / %#v, %v",
					actual, factory.Descriptor(), descriptorErr)
			}
		})
	}
}

func TestNewMCPToolWithFactoryRejectsInvalidInputs(t *testing.T) {
	validManager := &mcpFactoryTestManager{}
	validRemote := func(schema any) *sdkmcp.Tool {
		return &sdkmcp.Tool{Name: "valid", Description: "valid", InputSchema: schema}
	}
	cyclic := map[string]any{"type": "object"}
	cyclic["cycle"] = cyclic
	var typedNilManager *mcpFactoryTestManager
	var typedNilMap map[string]any
	var typedNilRaw json.RawMessage
	var typedNilBytes []byte
	var typedNilStruct *mcpFactorySchemaStruct

	tests := []struct {
		name    string
		manager MCPManager
		server  string
		remote  *sdkmcp.Tool
	}{
		{name: "nil manager", server: "server", remote: validRemote(nil)},
		{
			name: "typed nil manager", manager: typedNilManager,
			server: "server", remote: validRemote(nil),
		},
		{name: "empty server", manager: validManager, remote: validRemote(nil)},
		{name: "inexact server", manager: validManager, server: " server ", remote: validRemote(nil)},
		{name: "nil remote", manager: validManager, server: "server"},
		{name: "empty tool", manager: validManager, server: "server", remote: &sdkmcp.Tool{}},
		{
			name: "inexact tool", manager: validManager, server: "server",
			remote: &sdkmcp.Tool{Name: " tool "},
		},
		{
			name: "malformed raw", manager: validManager, server: "server",
			remote: validRemote(json.RawMessage(`{"type":`)),
		},
		{
			name: "trailing raw", manager: validManager, server: "server",
			remote: validRemote(json.RawMessage(`{} {}`)),
		},
		{
			name: "trailing garbage", manager: validManager, server: "server",
			remote: validRemote([]byte(`{} nope`)),
		},
		{
			name: "scalar", manager: validManager, server: "server",
			remote: validRemote(json.RawMessage(`1`)),
		},
		{name: "string", manager: validManager, server: "server", remote: validRemote("schema")},
		{name: "array", manager: validManager, server: "server", remote: validRemote([]any{"object"})},
		{
			name: "null", manager: validManager, server: "server",
			remote: validRemote(json.RawMessage(`null`)),
		},
		{
			name: "marshal failure", manager: validManager, server: "server",
			remote: validRemote(make(chan int)),
		},
		{name: "cycle", manager: validManager, server: "server", remote: validRemote(cyclic)},
		{
			name: "unsupported direct value", manager: validManager, server: "server",
			remote: validRemote(map[string]any{"fn": func() {}}),
		},
		{
			name: "nan", manager: validManager, server: "server",
			remote: validRemote(map[string]any{"number": math.NaN()}),
		},
		{
			name: "positive infinity", manager: validManager, server: "server",
			remote: validRemote(map[string]any{"number": math.Inf(1)}),
		},
		{
			name: "negative infinity", manager: validManager, server: "server",
			remote: validRemote(map[string]any{"number": math.Inf(-1)}),
		},
		{
			name: "invalid JSON number", manager: validManager, server: "server",
			remote: validRemote(map[string]any{"number": json.Number("NaN")}),
		},
		{
			name: "nested malformed raw", manager: validManager, server: "server",
			remote: validRemote(map[string]any{"schema": json.RawMessage(`{"type":`)}),
		},
		{
			name: "panicking marshaler", manager: validManager, server: "server",
			remote: validRemote(panickingMCPFactorySchema{}),
		},
		{name: "typed nil map", manager: validManager, server: "server", remote: validRemote(typedNilMap)},
		{name: "typed nil raw", manager: validManager, server: "server", remote: validRemote(typedNilRaw)},
		{name: "typed nil bytes", manager: validManager, server: "server", remote: validRemote(typedNilBytes)},
		{
			name: "typed nil struct", manager: validManager, server: "server",
			remote: validRemote(typedNilStruct),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			live, factory, err := NewMCPToolWithFactory(
				test.manager,
				test.server,
				test.remote,
				"",
				0,
				nil,
			)
			if err == nil || live != nil || factory != nil {
				t.Fatalf("invalid input result = live:%#v factory:%#v error:%v", live, factory, err)
			}
		})
	}
}

func TestNewMCPToolWithFactoryFreezesSDKDefinitionAndProducts(t *testing.T) {
	nondestructive := false
	closedWorld := false
	parameters := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type": "string",
				"enum": []string{"issues", "pulls"},
			},
		},
		"required": []string{"query"},
	}
	remote := &sdkmcp.Tool{
		Name: "search", Description: "Search repositories", InputSchema: parameters,
		Annotations: &sdkmcp.ToolAnnotations{
			ReadOnlyHint: true, IdempotentHint: true,
			DestructiveHint: &nondestructive, OpenWorldHint: &closedWorld,
		},
	}
	manager := &mcpFactoryTestManager{}
	live, factory, err := NewMCPToolWithFactory(
		manager,
		"github",
		remote,
		"workspace",
		64,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	frozenDescriptor := factory.Descriptor()
	remote.Name = "mutated"
	remote.Description = "mutated"
	remote.InputSchema = map[string]any{"type": "array"}
	remote.Annotations.ReadOnlyHint = false
	parameters["type"] = "array"
	parameters["required"].([]string)[0] = "mutated"
	parameters["properties"].(map[string]any)["query"].(map[string]any)["type"] = "number"

	if live.Name() != picomcp.CanonicalToolName("github", "search") ||
		live.Description() != "[MCP:github] Search repositories" {
		t.Fatalf("live retained mutable SDK metadata: %q / %q", live.Name(), live.Description())
	}
	liveParameters := live.Parameters()
	query := liveParameters["properties"].(map[string]any)["query"].(map[string]any)
	if liveParameters["type"] != "object" || query["type"] != "string" ||
		liveParameters["required"].([]string)[0] != "query" {
		t.Fatalf("live schema retained SDK aliases: %#v", liveParameters)
	}
	liveParameters["type"] = "caller mutation"
	query["enum"].([]string)[0] = "caller mutation"
	if fresh := live.Parameters(); fresh["type"] != "object" ||
		fresh["properties"].(map[string]any)["query"].(map[string]any)["enum"].([]string)[0] != "issues" {
		t.Fatalf("live Parameters returned an alias: %#v", fresh)
	}
	if !descriptorsEqual(factory.Descriptor(), frozenDescriptor) {
		t.Fatal("factory descriptor changed after SDK/live schema mutations")
	}

	wantTraits := ToolTraits{
		Risk: ToolRiskUnknown, Parallel: ToolParallelSerialized,
		Idempotency: ToolIdempotencyUnknown, Sharing: ToolSharingPerOwner,
	}
	if traits := factory.Traits(); traits != wantTraits {
		t.Fatalf("untrusted annotations changed traits = %#v, want %#v", traits, wantTraits)
	}
	firstRaw, err := factory.New(ToolBuildContext{})
	if err != nil {
		t.Fatal(err)
	}
	secondRaw, err := factory.New(ToolBuildContext{})
	if err != nil {
		t.Fatal(err)
	}
	first := firstRaw.(*MCPTool)
	second := secondRaw.(*MCPTool)
	if first == live || second == live || first == second {
		t.Fatalf("factory products alias = live:%p first:%p second:%p", live, first, second)
	}
	for index, product := range []*MCPTool{first, second} {
		actual, descriptorErr := safeToolDescriptor(product)
		if descriptorErr != nil || !descriptorsEqual(actual, frozenDescriptor) {
			t.Fatalf("product %d descriptor = %#v, %v", index, actual, descriptorErr)
		}
	}
	firstParameters := first.Parameters()
	firstParameters["type"] = "product mutation"
	if second.Parameters()["type"] != "object" || live.Parameters()["type"] != "object" {
		t.Fatal("factory products share mutable parameter state")
	}

	for _, product := range []*MCPTool{live, first, second} {
		result := product.Execute(context.Background(), map[string]any{"query": "picoclaw"})
		if result == nil || result.IsError {
			t.Fatalf("product execution = %#v", result)
		}
	}
	for index, call := range manager.snapshotCalls() {
		if call.server != "github" || call.tool != "search" || call.args["query"] != "picoclaw" {
			t.Fatalf("manager call %d = %#v", index, call)
		}
	}
}

func TestNewMCPToolWithFactoryCapturesRuntimeSettings(t *testing.T) {
	workspace := t.TempDir()
	largeText := strings.Repeat("captured runtime settings ", 8)
	manager := &mcpFactoryTestManager{result: &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: largeText}},
	}}
	eventBus := runtimeevents.NewBus()
	defer func() { _ = eventBus.Close() }()
	_, eventChannel, err := eventBus.Channel().OfKind(
		runtimeevents.KindMCPToolCallStart,
		runtimeevents.KindMCPToolCallEnd,
	).SubscribeChan(t.Context(), runtimeevents.SubscribeOptions{
		Name: "mcp-parent-factory-events", Buffer: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	live, factory, err := NewMCPToolWithFactory(
		manager,
		"server",
		&sdkmcp.Tool{Name: "artifact", InputSchema: nil},
		"  "+workspace+"  ",
		8,
		eventBus,
	)
	if err != nil {
		t.Fatal(err)
	}
	productRaw, err := factory.New(ToolBuildContext{})
	if err != nil {
		t.Fatal(err)
	}
	product := productRaw.(*MCPTool)
	// Strict snapshot wrappers ignore legacy setters for frozen runtime
	// definition fields; owner-local media remains independently injectable.
	product.SetWorkspace("")
	product.SetMaxInlineTextRunes(1 << 20)
	product.SetEventPublisher(nil)
	result := product.Execute(WithToolContext(context.Background(), "cli", "test"), nil)
	if result == nil || result.IsError || len(result.ArtifactTags) != 1 {
		t.Fatalf("captured workspace/limit result = %#v", result)
	}
	artifactPath := strings.TrimSuffix(
		strings.TrimPrefix(result.ArtifactTags[0], "[file:"),
		"]",
	)
	if !strings.HasPrefix(artifactPath, workspace) {
		t.Fatalf("artifact path = %q, want workspace %q", artifactPath, workspace)
	}
	contents, readErr := os.ReadFile(artifactPath)
	if readErr != nil || string(contents) != strings.TrimSpace(largeText) {
		t.Fatalf("artifact contents = %q, %v", contents, readErr)
	}
	for index := 0; index < 2; index++ {
		select {
		case event := <-eventChannel:
			if event.Kind != runtimeevents.KindMCPToolCallStart &&
				event.Kind != runtimeevents.KindMCPToolCallEnd {
				t.Fatalf("runtime event %d = %#v", index, event)
			}
		case <-time.After(time.Second):
			t.Fatalf("missing captured publisher event %d", index)
		}
	}
	if live == product {
		t.Fatal("live and factory product alias")
	}
}

type mcpFactoryMediaStore struct {
	mu    sync.Mutex
	refs  map[string]string
	calls atomic.Int64
}

func (store *mcpFactoryMediaStore) Store(
	localPath string,
	_ media.MediaMeta,
	_ string,
) (string, error) {
	call := store.calls.Add(1)
	ref := "media://factory-" + string(rune('a'+call-1))
	store.mu.Lock()
	if store.refs == nil {
		store.refs = make(map[string]string)
	}
	store.refs[ref] = localPath
	store.mu.Unlock()
	return ref, nil
}

func (store *mcpFactoryMediaStore) Resolve(ref string) (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	path, ok := store.refs[ref]
	if !ok {
		return "", errors.New("not found")
	}
	return path, nil
}

func (store *mcpFactoryMediaStore) ResolveWithMeta(ref string) (string, media.MediaMeta, error) {
	path, err := store.Resolve(ref)
	return path, media.MediaMeta{}, err
}

func (*mcpFactoryMediaStore) ReleaseAll(string) error { return nil }

func TestNewMCPToolWithFactoryMediaAndBorrowedLifetimeAreOwnerLocal(t *testing.T) {
	manager := &mcpFactoryTestManager{result: &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.ImageContent{
			Data: []byte("aW1hZ2U="), MIMEType: "image/png",
		}},
	}}
	live, factory, err := NewMCPToolWithFactory(
		manager,
		"images",
		&sdkmcp.Tool{Name: "render"},
		"",
		0,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	source := NewToolRegistry()
	if registerErr := source.RegisterFactoryBacked(live, factory); registerErr != nil {
		t.Fatal(registerErr)
	}
	child, err := source.InstantiateForOwnerSelection(
		ToolOwner{Scope: ToolOwnerScopeAgent, AgentID: "mcp-child"},
		[]string{live.Name()},
	)
	if err != nil {
		t.Fatal(err)
	}
	childRaw, ok := child.GetRegistered(live.Name())
	if !ok {
		t.Fatal("child MCP wrapper missing")
	}
	childTool := childRaw.(*MCPTool)
	if childTool == live {
		t.Fatal("owner construction reused the compatibility wrapper")
	}
	liveStore := &mcpFactoryMediaStore{}
	childStore := &mcpFactoryMediaStore{}
	live.SetMediaStore(liveStore)
	childTool.SetMediaStore(childStore)
	ctx := WithToolContext(context.Background(), "telegram", "chat")
	if result := live.Execute(ctx, nil); result == nil || result.IsError {
		t.Fatalf("live media execution = %#v", result)
	}
	if result := childTool.Execute(ctx, nil); result == nil || result.IsError {
		t.Fatalf("child media execution = %#v", result)
	}
	if liveStore.calls.Load() != 1 || childStore.calls.Load() != 1 {
		t.Fatalf("media stores = live:%d child:%d", liveStore.calls.Load(), childStore.calls.Load())
	}
	if err := child.Close(); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if manager.closeCalls.Load() != 0 {
		t.Fatal("wrapper owner closed the borrowed MCP manager")
	}
	if result := live.Execute(context.Background(), nil); result == nil || result.IsError {
		t.Fatalf("borrowed manager unusable after registry close: %#v", result)
	}
}

func TestNewMCPToolWithFactoryFallbackDescriptionAndDefaultLimit(t *testing.T) {
	manager := &mcpFactoryTestManager{result: &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: strings.Repeat("x", 128)}},
	}}
	live, _, err := NewMCPToolWithFactory(
		manager,
		"fallback",
		&sdkmcp.Tool{Name: "describe"},
		t.TempDir(),
		0,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if live.Description() != "[MCP:fallback] MCP tool from fallback server" {
		t.Fatalf("fallback description = %q", live.Description())
	}
	result := live.Execute(context.Background(), nil)
	if result == nil || result.IsError || len(result.ArtifactTags) != 0 ||
		!strings.Contains(result.ForLLM, strings.Repeat("x", 128)) {
		t.Fatalf("effective default inline limit result = %#v", result)
	}
}

func TestSnapshotMCPInputSchemaPreservesJSONNumber(t *testing.T) {
	schema, err := snapshotMCPInputSchema(json.RawMessage(
		`{"type":"object","maximum":9007199254740993}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if got := schema["maximum"]; !reflect.DeepEqual(got, json.Number("9007199254740993")) {
		t.Fatalf("large JSON number = %#v (%T)", got, got)
	}
}
