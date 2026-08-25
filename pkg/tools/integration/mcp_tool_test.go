package integrationtools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	picomcp "github.com/sipeed/picoclaw/pkg/mcp"
	"github.com/sipeed/picoclaw/pkg/media"
	toolshared "github.com/sipeed/picoclaw/pkg/tools/shared"
)

// MockMCPManager is a mock implementation of MCPManager interface for testing
type MockMCPManager struct {
	callToolFunc func(ctx context.Context, serverName, toolName string, arguments map[string]any) (*mcp.CallToolResult, error)
}

type snapshotMCPCall struct {
	server string
	tool   string
}

type snapshotMCPManager struct {
	mu         sync.Mutex
	calls      []snapshotMCPCall
	result     func() *mcp.CallToolResult
	closeCalls atomic.Int64
}

func (manager *snapshotMCPManager) CallTool(
	_ context.Context,
	serverName, toolName string,
	_ map[string]any,
) (*mcp.CallToolResult, error) {
	manager.mu.Lock()
	manager.calls = append(manager.calls, snapshotMCPCall{server: serverName, tool: toolName})
	manager.mu.Unlock()
	if manager.result != nil {
		return manager.result(), nil
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: "snapshot result"}},
	}, nil
}

func (manager *snapshotMCPManager) Close() error {
	manager.closeCalls.Add(1)
	return nil
}

func (manager *snapshotMCPManager) lastCall() snapshotMCPCall {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if len(manager.calls) == 0 {
		return snapshotMCPCall{}
	}
	return manager.calls[len(manager.calls)-1]
}

type discardMCPMediaStore struct {
	calls atomic.Int64
}

func (store *discardMCPMediaStore) Store(
	localPath string,
	_ media.MediaMeta,
	_ string,
) (string, error) {
	call := store.calls.Add(1)
	_ = os.Remove(localPath)
	return fmt.Sprintf("media://discard-%d", call), nil
}

func (*discardMCPMediaStore) Resolve(string) (string, error) {
	return "", fmt.Errorf("discard media store does not retain paths")
}

func (*discardMCPMediaStore) ResolveWithMeta(string) (string, media.MediaMeta, error) {
	return "", media.MediaMeta{}, fmt.Errorf("discard media store does not retain paths")
}

func (*discardMCPMediaStore) ReleaseAll(string) error { return nil }

func (m *MockMCPManager) CallTool(
	ctx context.Context,
	serverName, toolName string,
	arguments map[string]any,
) (*mcp.CallToolResult, error) {
	if m.callToolFunc != nil {
		return m.callToolFunc(ctx, serverName, toolName, arguments)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: "mock result"},
		},
		IsError: false,
	}, nil
}

// TestNewMCPTool verifies MCP tool creation
func TestNewMCPTool(t *testing.T) {
	manager := &MockMCPManager{}
	tool := &mcp.Tool{
		Name:        "test_tool",
		Description: "A test tool",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"input": map[string]any{
					"type":        "string",
					"description": "Test input",
				},
			},
		},
	}

	mcpTool := NewMCPTool(manager, "test_server", tool)

	if mcpTool == nil {
		t.Fatal("NewMCPTool should not return nil")
	}
	// Verify tool properties we can access
	if mcpTool.Name() != "mcp_test_server_test_tool" {
		t.Errorf("Expected tool name with prefix, got '%s'", mcpTool.Name())
	}
}

// TestMCPTool_Name verifies tool name with server prefix
func TestMCPTool_Name(t *testing.T) {
	tests := []struct {
		name       string
		serverName string
		toolName   string
		expected   string
	}{
		{
			name:       "simple name",
			serverName: "github",
			toolName:   "create_issue",
			expected:   "mcp_github_create_issue",
		},
		{
			name:       "filesystem server",
			serverName: "filesystem",
			toolName:   "read_file",
			expected:   "mcp_filesystem_read_file",
		},
		{
			name:       "remote server",
			serverName: "remote-api",
			toolName:   "fetch_data",
			expected:   "mcp_remote-api_fetch_data",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := &MockMCPManager{}
			tool := &mcp.Tool{Name: tt.toolName}
			mcpTool := NewMCPTool(manager, tt.serverName, tool)

			result := mcpTool.Name()
			if result != tt.expected {
				t.Errorf("Expected name '%s', got '%s'", tt.expected, result)
			}
		})
	}
}

func TestMCPToolNameUsesSharedCanonicalAlgorithm(t *testing.T) {
	tests := []struct {
		server string
		tool   string
	}{
		{server: "GitHub", tool: "Create_Issue"},
		{server: "GitHub Server", tool: "issues.list"},
		{server: strings.Repeat("server", 20), tool: strings.Repeat("tool", 30)},
	}
	for _, test := range tests {
		wrapped := NewMCPTool(&MockMCPManager{}, test.server, &mcp.Tool{Name: test.tool})
		if got, want := wrapped.Name(), picomcp.CanonicalToolName(test.server, test.tool); got != want {
			t.Fatalf("MCPTool.Name() = %q, want shared canonical name %q", got, want)
		}
	}
}

func TestMCPToolIdentityReturnsExactNames(t *testing.T) {
	wrapped := NewMCPTool(
		&MockMCPManager{},
		"GitHub Server",
		&mcp.Tool{Name: "issues.list"},
	)

	serverName, toolName := wrapped.MCPIdentity()
	if serverName != "GitHub Server" || toolName != "issues.list" {
		t.Fatalf(
			"MCPIdentity() = %q/%q, want GitHub Server/issues.list",
			serverName,
			toolName,
		)
	}
}

func TestMCPTool_PromptMetadata(t *testing.T) {
	manager := &MockMCPManager{}
	tool := NewMCPTool(manager, "GitHub Server", &mcp.Tool{Name: "create_issue"})

	metadata := tool.PromptMetadata()
	if metadata.Layer != toolshared.ToolPromptLayerCapability {
		t.Fatalf("metadata.Layer = %q, want %q", metadata.Layer, toolshared.ToolPromptLayerCapability)
	}
	if metadata.Slot != toolshared.ToolPromptSlotMCP {
		t.Fatalf("metadata.Slot = %q, want %q", metadata.Slot, toolshared.ToolPromptSlotMCP)
	}
	if metadata.Source != "mcp:github_server" {
		t.Fatalf("metadata.Source = %q, want mcp:github_server", metadata.Source)
	}
}

// TestMCPTool_Description verifies tool description generation
func TestMCPTool_Description(t *testing.T) {
	tests := []struct {
		name            string
		serverName      string
		toolDescription string
		expectContains  []string
	}{
		{
			name:            "with description",
			serverName:      "github",
			toolDescription: "Create a GitHub issue",
			expectContains:  []string{"[MCP:github]", "Create a GitHub issue"},
		},
		{
			name:            "empty description",
			serverName:      "filesystem",
			toolDescription: "",
			expectContains:  []string{"[MCP:filesystem]", "MCP tool from filesystem server"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := &MockMCPManager{}
			tool := &mcp.Tool{
				Name:        "test_tool",
				Description: tt.toolDescription,
			}
			mcpTool := NewMCPTool(manager, tt.serverName, tool)

			result := mcpTool.Description()

			for _, expected := range tt.expectContains {
				if !strings.Contains(result, expected) {
					t.Errorf("Description should contain '%s', got: %s", expected, result)
				}
			}
		})
	}
}

// TestMCPTool_Parameters verifies parameter schema conversion
func TestMCPTool_Parameters(t *testing.T) {
	tests := []struct {
		name           string
		inputSchema    any
		expectType     string
		checkProperty  string
		expectProperty bool
	}{
		{
			name: "map schema",
			inputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Search query",
					},
				},
				"required": []string{"query"},
			},
			expectType:     "object",
			checkProperty:  "query",
			expectProperty: true,
		},
		{
			name:           "nil schema",
			inputSchema:    nil,
			expectType:     "object",
			expectProperty: false,
		},
		{
			name: "json.RawMessage schema",
			inputSchema: []byte(`{
				"type": "object",
				"properties": {
					"repo": {
						"type": "string",
						"description": "Repository name"
					},
					"stars": {
						"type": "integer",
						"description": "Minimum stars"
					}
				},
				"required": ["repo"]
			}`),
			expectType:     "object",
			checkProperty:  "repo",
			expectProperty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := &MockMCPManager{}
			tool := &mcp.Tool{
				Name:        "test_tool",
				InputSchema: tt.inputSchema,
			}
			mcpTool := NewMCPTool(manager, "test_server", tool)

			params := mcpTool.Parameters()

			if params == nil {
				t.Fatal("Parameters should not be nil")
			}

			if params["type"] != tt.expectType {
				t.Errorf("Expected type '%s', got '%v'", tt.expectType, params["type"])
			}

			// Check if property exists when expected
			if tt.checkProperty != "" {
				properties, ok := params["properties"].(map[string]any)
				if !ok && tt.expectProperty {
					t.Errorf("Expected properties to be a map")
					return
				}
				if ok {
					_, hasProperty := properties[tt.checkProperty]
					if hasProperty != tt.expectProperty {
						t.Errorf("Expected property '%s' existence: %v, got: %v",
							tt.checkProperty, tt.expectProperty, hasProperty)
					}
				}
			}
		})
	}
}

// TestMCPTool_Execute_Success tests successful tool execution
func TestMCPTool_Execute_Success(t *testing.T) {
	manager := &MockMCPManager{
		callToolFunc: func(ctx context.Context, serverName, toolName string, arguments map[string]any) (*mcp.CallToolResult, error) {
			// Verify correct parameters passed
			if serverName != "github" {
				t.Errorf("Expected serverName 'github', got '%s'", serverName)
			}
			if toolName != "search_repos" {
				t.Errorf("Expected toolName 'search_repos', got '%s'", toolName)
			}

			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: "Found 3 repositories"},
				},
				IsError: false,
			}, nil
		},
	}

	tool := &mcp.Tool{
		Name:        "search_repos",
		Description: "Search GitHub repositories",
	}
	mcpTool := NewMCPTool(manager, "github", tool)

	ctx := context.Background()
	args := map[string]any{
		"query": "golang mcp",
	}

	result := mcpTool.Execute(ctx, args)

	if result == nil {
		t.Fatal("Result should not be nil")
	}
	if result.IsError {
		t.Errorf("Expected no error, got error: %s", result.ForLLM)
	}
	if result.ForLLM != "Found 3 repositories" {
		t.Errorf("Expected 'Found 3 repositories', got '%s'", result.ForLLM)
	}
}

func TestMCPTool_Execute_PublishesRuntimeEvents(t *testing.T) {
	eventBus := runtimeevents.NewBus()
	defer func() {
		if err := eventBus.Close(); err != nil {
			t.Errorf("event bus close failed: %v", err)
		}
	}()

	_, eventsCh, err := eventBus.Channel().OfKind(
		runtimeevents.KindMCPToolCallStart,
		runtimeevents.KindMCPToolCallEnd,
	).SubscribeChan(t.Context(), runtimeevents.SubscribeOptions{Name: "mcp-tool-events", Buffer: 2})
	if err != nil {
		t.Fatalf("SubscribeChan failed: %v", err)
	}

	manager := &MockMCPManager{}
	mcpTool := NewMCPTool(manager, "github", &mcp.Tool{Name: "search_repos"})
	mcpTool.SetEventPublisher(eventBus)

	ctx := toolshared.WithToolContext(context.Background(), "telegram", "chat-1")
	ctx = toolshared.WithToolMessageContext(ctx, "msg-1", "")
	ctx = toolshared.WithToolSessionContext(ctx, "main", "session-1", nil)
	result := mcpTool.Execute(ctx, map[string]any{"query": "picoclaw"})
	if result == nil || result.IsError {
		t.Fatalf("Execute result = %+v", result)
	}

	started := receiveMCPToolRuntimeEvent(t, eventsCh)
	if started.Kind != runtimeevents.KindMCPToolCallStart ||
		started.Scope.AgentID != "main" ||
		started.Scope.SessionKey != "session-1" ||
		started.Scope.Channel != "telegram" ||
		started.Scope.ChatID != "chat-1" ||
		started.Scope.MessageID != "msg-1" {
		t.Fatalf("started event = %+v", started)
	}

	ended := receiveMCPToolRuntimeEvent(t, eventsCh)
	if ended.Kind != runtimeevents.KindMCPToolCallEnd || ended.Severity != runtimeevents.SeverityInfo {
		t.Fatalf("ended event = %+v", ended)
	}
	payload, ok := ended.Payload.(MCPToolCallPayload)
	if !ok {
		t.Fatalf("ended payload = %T, want MCPToolCallPayload", ended.Payload)
	}
	if payload.Server != "github" || payload.Tool != "search_repos" || payload.IsError {
		t.Fatalf("ended payload = %+v", payload)
	}
	if ended.Attrs["server"] != "github" ||
		ended.Attrs["tool"] != "search_repos" ||
		ended.Attrs["duration_ms"] == nil {
		t.Fatalf("ended attrs = %#v", ended.Attrs)
	}
}

func receiveMCPToolRuntimeEvent(t *testing.T, ch <-chan runtimeevents.Event) runtimeevents.Event {
	t.Helper()

	select {
	case evt, ok := <-ch:
		if !ok {
			t.Fatal("runtime event channel closed before expected event")
		}
		return evt
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for runtime event")
		return runtimeevents.Event{}
	}
}

// TestMCPTool_Execute_ManagerError tests execution when manager returns error
func TestMCPTool_Execute_ManagerError(t *testing.T) {
	manager := &MockMCPManager{
		callToolFunc: func(ctx context.Context, serverName, toolName string, arguments map[string]any) (*mcp.CallToolResult, error) {
			return nil, fmt.Errorf("connection failed")
		},
	}

	tool := &mcp.Tool{Name: "test_tool"}
	mcpTool := NewMCPTool(manager, "test_server", tool)

	ctx := context.Background()
	result := mcpTool.Execute(ctx, map[string]any{})

	if result == nil {
		t.Fatal("Result should not be nil")
	}
	if !result.IsError {
		t.Error("Expected IsError to be true")
	}
	if !strings.Contains(result.ForLLM, "MCP tool execution failed") {
		t.Errorf("Error message should mention execution failure, got: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "connection failed") {
		t.Errorf("Error message should include original error, got: %s", result.ForLLM)
	}
}

// TestMCPTool_Execute_ServerError tests execution when server returns error
func TestMCPTool_Execute_ServerError(t *testing.T) {
	manager := &MockMCPManager{
		callToolFunc: func(ctx context.Context, serverName, toolName string, arguments map[string]any) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: "Invalid API key"},
				},
				IsError: true,
			}, nil
		},
	}

	tool := &mcp.Tool{Name: "test_tool"}
	mcpTool := NewMCPTool(manager, "test_server", tool)

	ctx := context.Background()
	result := mcpTool.Execute(ctx, map[string]any{})

	if result == nil {
		t.Fatal("Result should not be nil")
	}
	if !result.IsError {
		t.Error("Expected IsError to be true")
	}
	if !strings.Contains(result.ForLLM, "MCP tool returned error") {
		t.Errorf("Error message should mention server error, got: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "Invalid API key") {
		t.Errorf("Error message should include server message, got: %s", result.ForLLM)
	}
}

// TestMCPTool_Execute_MultipleContent tests execution with multiple content items
func TestMCPTool_Execute_MultipleContent(t *testing.T) {
	manager := &MockMCPManager{
		callToolFunc: func(ctx context.Context, serverName, toolName string, arguments map[string]any) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: "First line"},
					&mcp.TextContent{Text: "Second line"},
					&mcp.TextContent{Text: "Third line"},
				},
				IsError: false,
			}, nil
		},
	}

	tool := &mcp.Tool{Name: "multi_output"}
	mcpTool := NewMCPTool(manager, "test_server", tool)

	ctx := context.Background()
	result := mcpTool.Execute(ctx, map[string]any{})

	if result.IsError {
		t.Errorf("Expected no error, got: %s", result.ForLLM)
	}

	expected := "First line\nSecond line\nThird line"
	if result.ForLLM != expected {
		t.Errorf("Expected '%s', got '%s'", expected, result.ForLLM)
	}
}

// TestExtractContentText_TextContent tests text content extraction
func TestExtractContentText_TextContent(t *testing.T) {
	content := []mcp.Content{
		&mcp.TextContent{Text: "Hello World"},
		&mcp.TextContent{Text: "Second message"},
	}

	result := extractContentText(content)
	expected := "Hello World\nSecond message"

	if result != expected {
		t.Errorf("Expected '%s', got '%s'", expected, result)
	}
}

// TestExtractContentText_ImageContent tests image content extraction
func TestExtractContentText_ImageContent(t *testing.T) {
	content := []mcp.Content{
		&mcp.ImageContent{
			Data:     []byte("base64data"),
			MIMEType: "image/png",
		},
	}

	result := extractContentText(content)

	if !strings.Contains(result, "[Image:") {
		t.Errorf("Expected image indicator, got: %s", result)
	}
	if !strings.Contains(result, "image/png") {
		t.Errorf("Expected MIME type in output, got: %s", result)
	}
}

// TestExtractContentText_MixedContent tests mixed content types
func TestExtractContentText_MixedContent(t *testing.T) {
	content := []mcp.Content{
		&mcp.TextContent{Text: "Description"},
		&mcp.ImageContent{
			Data:     []byte("data"),
			MIMEType: "image/jpeg",
		},
		&mcp.TextContent{Text: "More text"},
	}

	result := extractContentText(content)

	if !strings.Contains(result, "Description") {
		t.Errorf("Should contain text content, got: %s", result)
	}
	if !strings.Contains(result, "[Image:") {
		t.Errorf("Should contain image indicator, got: %s", result)
	}
	if !strings.Contains(result, "More text") {
		t.Errorf("Should contain second text, got: %s", result)
	}
}

// TestExtractContentText_EmptyContent tests empty content array
func TestExtractContentText_EmptyContent(t *testing.T) {
	content := []mcp.Content{}

	result := extractContentText(content)

	if result != "" {
		t.Errorf("Expected empty string for empty content, got: %s", result)
	}
}

// TestMCPTool_InterfaceCompliance verifies MCPTool implements Tool interface
func TestMCPTool_InterfaceCompliance(t *testing.T) {
	manager := &MockMCPManager{}
	tool := &mcp.Tool{Name: "test"}
	mcpTool := NewMCPTool(manager, "test_server", tool)

	// Verify it implements Tool interface
	var _ Tool = mcpTool
}

// TestMCPTool_Parameters_MapSchema tests schema that's already a map
func TestMCPTool_Parameters_MapSchema(t *testing.T) {
	manager := &MockMCPManager{}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "The name parameter",
			},
		},
		"required": []string{"name"},
	}

	tool := &mcp.Tool{
		Name:        "test_tool",
		InputSchema: schema,
	}
	mcpTool := NewMCPTool(manager, "test_server", tool)

	params := mcpTool.Parameters()

	// Should return the schema as-is when it's already a map
	if params["type"] != "object" {
		t.Errorf("Expected type 'object', got '%v'", params["type"])
	}

	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Error("Properties should be a map")
	}

	nameParam, ok := props["name"].(map[string]any)
	if !ok {
		t.Error("Name parameter should exist")
	}

	if nameParam["type"] != "string" {
		t.Errorf("Name type should be 'string', got '%v'", nameParam["type"])
	}
}

func TestMCPTool_Execute_ImageContentStoredAsMedia(t *testing.T) {
	store := media.NewFileMediaStore()
	manager := &MockMCPManager{
		callToolFunc: func(ctx context.Context, serverName, toolName string, arguments map[string]any) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.ImageContent{
						Data:     []byte("fake-image-bytes"),
						MIMEType: "image/png",
					},
				},
			}, nil
		},
	}

	mcpTool := NewMCPTool(manager, "screenshoto", &mcp.Tool{Name: "take_screenshot"})
	mcpTool.SetMediaStore(store)

	result := mcpTool.Execute(WithToolContext(context.Background(), "telegram", "chat-42"), nil)

	if result.IsError {
		t.Fatalf("expected success, got %q", result.ForLLM)
	}
	if len(result.Media) != 1 {
		t.Fatalf("expected 1 media ref, got %d", len(result.Media))
	}
	if result.ResponseHandled {
		t.Fatal("expected MCP image artifact not to mark response as handled")
	}
	if !strings.Contains(result.ForLLM, "stored as a local media artifact") {
		t.Fatalf("expected local media artifact note, got %q", result.ForLLM)
	}

	path, meta, err := store.ResolveWithMeta(result.Media[0])
	if err != nil {
		t.Fatalf("expected stored media ref to resolve: %v", err)
	}
	if meta.ContentType != "image/png" {
		t.Fatalf("expected image/png content type, got %q", meta.ContentType)
	}
	if filepath.Ext(path) != ".png" {
		t.Fatalf("expected png temp file, got %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected stored media file to be readable: %v", err)
	}
	if string(data) != "fake-image-bytes" {
		t.Fatalf("expected stored media bytes to match input, got %q", string(data))
	}
}

func TestMCPTool_Execute_EmbeddedResourceBlobStoredAsMedia(t *testing.T) {
	store := media.NewFileMediaStore()
	manager := &MockMCPManager{
		callToolFunc: func(ctx context.Context, serverName, toolName string, arguments map[string]any) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.EmbeddedResource{
						Resource: &mcp.ResourceContents{
							URI:      "file:///tmp/report.png",
							MIMEType: "image/png",
							Blob:     []byte("blob-bytes"),
						},
					},
				},
			}, nil
		},
	}

	mcpTool := NewMCPTool(manager, "grafana", &mcp.Tool{Name: "get_dashboard_image"})
	mcpTool.SetMediaStore(store)

	result := mcpTool.Execute(WithToolContext(context.Background(), "telegram", "chat-42"), nil)

	if len(result.Media) != 1 {
		t.Fatalf("expected embedded resource blob to be stored as media, got %d refs", len(result.Media))
	}
	path, _, err := store.ResolveWithMeta(result.Media[0])
	if err != nil {
		t.Fatalf("expected stored media ref to resolve: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected stored media file to be readable: %v", err)
	}
	if string(data) != "blob-bytes" {
		t.Fatalf("expected stored blob bytes to match input, got %q", string(data))
	}
}

func TestMCPTool_Execute_RespectsUserAudienceForBinaryContent(t *testing.T) {
	store := media.NewFileMediaStore()
	manager := &MockMCPManager{
		callToolFunc: func(ctx context.Context, serverName, toolName string, arguments map[string]any) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.ImageContent{
						Data:        []byte("assistant-only"),
						MIMEType:    "image/png",
						Annotations: &mcp.Annotations{Audience: []mcp.Role{"assistant"}},
					},
				},
			}, nil
		},
	}

	mcpTool := NewMCPTool(manager, "screenshoto", &mcp.Tool{Name: "take_screenshot"})
	mcpTool.SetMediaStore(store)

	result := mcpTool.Execute(WithToolContext(context.Background(), "telegram", "chat-42"), nil)

	if len(result.Media) != 0 {
		t.Fatalf("expected no media ref for non-user audience, got %d", len(result.Media))
	}
	if !strings.Contains(result.ForLLM, "non-user audience") {
		t.Fatalf("expected audience note, got %q", result.ForLLM)
	}
}

func TestMCPTool_Execute_LargeBase64TextIsOmittedFromContext(t *testing.T) {
	manager := &MockMCPManager{
		callToolFunc: func(ctx context.Context, serverName, toolName string, arguments map[string]any) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: strings.Repeat("QUJD", 400)},
				},
			}, nil
		},
	}

	mcpTool := NewMCPTool(manager, "test_server", &mcp.Tool{Name: "dump_payload"})

	result := mcpTool.Execute(context.Background(), nil)

	if result.ForLLM != largeBase64OmittedMessage {
		t.Fatalf("expected sanitized large base64 note, got %q", result.ForLLM)
	}
}

func TestMCPTool_Execute_LargeBase64TextArtifactPreservesRawPayload(t *testing.T) {
	workspace := t.TempDir()
	largeBase64 := strings.Repeat("QUJD", 400)
	manager := &MockMCPManager{
		callToolFunc: func(ctx context.Context, serverName, toolName string, arguments map[string]any) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: largeBase64},
				},
			}, nil
		},
	}

	mcpTool := NewMCPTool(manager, "test_server", &mcp.Tool{Name: "dump_payload"})
	mcpTool.SetWorkspace(workspace)
	mcpTool.SetMaxInlineTextRunes(32)

	result := mcpTool.Execute(context.Background(), nil)

	if !strings.Contains(result.ForLLM, "saved as a local artifact") {
		t.Fatalf("expected artifact note, got %q", result.ForLLM)
	}
	if result.ForLLM == largeBase64OmittedMessage {
		t.Fatalf("expected artifact note instead of sanitized base64 placeholder")
	}
	if len(result.ArtifactTags) != 1 {
		t.Fatalf("expected 1 artifact tag, got %d", len(result.ArtifactTags))
	}
	tag := result.ArtifactTags[0]
	const prefix = "[file:"
	if !strings.HasPrefix(tag, prefix) || !strings.HasSuffix(tag, "]") {
		t.Fatalf("expected file artifact tag, got %q", tag)
	}
	path := strings.TrimSuffix(strings.TrimPrefix(tag, prefix), "]")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected artifact file to be readable: %v", err)
	}
	if string(data) != largeBase64 {
		t.Fatalf("expected artifact file contents to preserve raw MCP payload")
	}
}

func TestMCPTool_Execute_LargeTextStoredAsArtifact(t *testing.T) {
	workspace := t.TempDir()
	largeText := strings.Repeat("This is a large MCP text payload.\n", 800)
	manager := &MockMCPManager{
		callToolFunc: func(ctx context.Context, serverName, toolName string, arguments map[string]any) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: largeText},
				},
			}, nil
		},
	}

	mcpTool := NewMCPTool(manager, "test_server", &mcp.Tool{Name: "dump_payload"})
	mcpTool.SetWorkspace(workspace)

	result := mcpTool.Execute(context.Background(), nil)

	if strings.Contains(result.ForLLM, "This is a large MCP text payload") {
		t.Fatalf("expected large MCP text to be omitted from ForLLM, got %q", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "saved as a local artifact") {
		t.Fatalf("expected artifact note, got %q", result.ForLLM)
	}
	if len(result.ArtifactTags) != 1 {
		t.Fatalf("expected 1 artifact tag, got %d", len(result.ArtifactTags))
	}
	tag := result.ArtifactTags[0]
	const prefix = "[file:"
	if !strings.HasPrefix(tag, prefix) || !strings.HasSuffix(tag, "]") {
		t.Fatalf("expected file artifact tag, got %q", tag)
	}
	path := strings.TrimSuffix(strings.TrimPrefix(tag, prefix), "]")
	if !strings.HasPrefix(path, workspace) {
		t.Fatalf("expected artifact inside workspace, got %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected artifact file to be readable: %v", err)
	}
	if string(data) != strings.TrimSpace(largeText) {
		t.Fatalf("expected artifact file contents to match source text")
	}
}

func TestMCPTool_Execute_CustomInlineTextThreshold(t *testing.T) {
	workspace := t.TempDir()
	text := strings.Repeat("small custom threshold text\n", 20)
	manager := &MockMCPManager{
		callToolFunc: func(ctx context.Context, serverName, toolName string, arguments map[string]any) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: text},
				},
			}, nil
		},
	}

	mcpTool := NewMCPTool(manager, "test_server", &mcp.Tool{Name: "dump_payload"})
	mcpTool.SetWorkspace(workspace)
	mcpTool.SetMaxInlineTextRunes(32)

	result := mcpTool.Execute(context.Background(), nil)

	if len(result.ArtifactTags) != 1 {
		t.Fatalf("expected custom threshold to persist artifact, got %+v", result)
	}
	if strings.Contains(result.ForLLM, "small custom threshold text") {
		t.Fatalf("expected text to be omitted from ForLLM, got %q", result.ForLLM)
	}
}

func TestMCPTool_Execute_LargeTextArtifactFailureStillOmitsContext(t *testing.T) {
	workspaceRoot := t.TempDir()
	workspaceFile := filepath.Join(workspaceRoot, "not-a-directory")
	if err := os.WriteFile(workspaceFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("failed to create workspace file: %v", err)
	}

	largeText := strings.Repeat("This is a large MCP text payload.\n", 800)
	manager := &MockMCPManager{
		callToolFunc: func(ctx context.Context, serverName, toolName string, arguments map[string]any) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: largeText},
				},
			}, nil
		},
	}

	mcpTool := NewMCPTool(manager, "test_server", &mcp.Tool{Name: "dump_payload"})
	mcpTool.SetWorkspace(workspaceFile)

	result := mcpTool.Execute(context.Background(), nil)

	if strings.Contains(result.ForLLM, "This is a large MCP text payload") {
		t.Fatalf("expected large MCP text to be omitted from ForLLM, got %q", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "artifact persistence failed") {
		t.Fatalf("expected persistence failure note, got %q", result.ForLLM)
	}
	if len(result.ArtifactTags) != 0 {
		t.Fatalf("expected no artifact tags on persistence failure, got %+v", result.ArtifactTags)
	}
}

func TestMCPTool_Execute_WhitespaceWorkspaceDisablesArtifactPersistence(t *testing.T) {
	largeText := strings.Repeat("This is a large MCP text payload.\n", 800)
	manager := &MockMCPManager{
		callToolFunc: func(ctx context.Context, serverName, toolName string, arguments map[string]any) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: largeText},
				},
			}, nil
		},
	}

	mcpTool := NewMCPTool(manager, "test_server", &mcp.Tool{Name: "dump_payload"})
	mcpTool.SetWorkspace(" \n\t ")

	result := mcpTool.Execute(context.Background(), nil)

	if len(result.ArtifactTags) != 0 {
		t.Fatalf("expected no artifact tags for whitespace workspace, got %+v", result.ArtifactTags)
	}
	if !strings.Contains(result.ForLLM, "This is a large MCP text payload") {
		t.Fatalf("expected large text to remain inline when workspace is blank, got %q", result.ForLLM)
	}
}

func TestNewMCPToolDetachesSDKDefinitionAndReturnedParameters(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "original nested description",
			},
		},
		"required": []string{"query"},
	}
	sdkTool := &mcp.Tool{
		Name: "original_tool", Description: "original description", InputSchema: schema,
	}
	wrapped := NewMCPTool(&MockMCPManager{}, "Original Server", sdkTool)

	sdkTool.Name = "mutated_tool"
	sdkTool.Description = "mutated description"
	schema["properties"].(map[string]any)["query"].(map[string]any)["description"] = "mutated nested description"
	schema["required"].([]string)[0] = "mutated"

	if got, want := wrapped.Name(), picomcp.CanonicalToolName("Original Server", "original_tool"); got != want {
		t.Fatalf("detached name = %q, want %q", got, want)
	}
	if got := wrapped.Description(); got != "[MCP:Original Server] original description" {
		t.Fatalf("detached description = %q", got)
	}
	if server, tool := wrapped.MCPIdentity(); server != "Original Server" || tool != "original_tool" {
		t.Fatalf("detached identity = %q/%q", server, tool)
	}

	first := wrapped.Parameters()
	query := first["properties"].(map[string]any)["query"].(map[string]any)
	if query["description"] != "original nested description" ||
		first["required"].([]string)[0] != "query" {
		t.Fatalf("detached parameters = %#v", first)
	}
	query["description"] = "caller mutation"
	first["required"].([]string)[0] = "caller mutation"
	first["type"] = "caller mutation"

	second := wrapped.Parameters()
	secondQuery := second["properties"].(map[string]any)["query"].(map[string]any)
	if second["type"] != "object" ||
		secondQuery["description"] != "original nested description" ||
		second["required"].([]string)[0] != "query" {
		t.Fatalf("Parameters retained a caller alias: %#v", second)
	}
}

func TestNewMCPToolFromSnapshotFreezesRuntimeDefinitionAndKeepsMediaMutable(t *testing.T) {
	workspace := t.TempDir()
	ignoredWorkspace := t.TempDir()
	events := runtimeevents.NewBus()
	ignoredEvents := runtimeevents.NewBus()
	defer func() {
		_ = events.Close()
		_ = ignoredEvents.Close()
	}()
	_, eventsCh, err := events.Channel().OfKind(
		runtimeevents.KindMCPToolCallStart,
		runtimeevents.KindMCPToolCallEnd,
	).SubscribeChan(t.Context(), runtimeevents.SubscribeOptions{
		Name: "strict-mcp-snapshot", Buffer: 4,
	})
	if err != nil {
		t.Fatal(err)
	}

	parameters := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"value": map[string]any{"type": "string"},
		},
		"required": []string{"value"},
	}
	manager := &snapshotMCPManager{result: func() *mcp.CallToolResult {
		return &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.TextContent{Text: strings.Repeat("frozen payload ", 16)},
			&mcp.ImageContent{Data: []byte("frozen-image"), MIMEType: "image/png"},
		}}
	}}
	wrapped := NewMCPToolFromSnapshot(manager, MCPToolSnapshot{
		ServerName:         "Frozen Server",
		ToolName:           "Frozen.Tool",
		CanonicalName:      "mcp_frozen_server_frozen_tool",
		Description:        "[MCP:Frozen Server] frozen description",
		Parameters:         parameters,
		Workspace:          workspace,
		MaxInlineTextRunes: 8,
		EventPublisher:     events,
	})

	parameters["type"] = "mutated"
	parameters["required"].([]string)[0] = "mutated"
	wrapped.SetWorkspace(ignoredWorkspace)
	wrapped.SetMaxInlineTextRunes(1 << 20)
	wrapped.SetEventPublisher(ignoredEvents)
	firstStore := media.NewFileMediaStore()
	latestStore := media.NewFileMediaStore()
	wrapped.SetMediaStore(firstStore)
	wrapped.SetMediaStore(latestStore)

	if wrapped.Name() != "mcp_frozen_server_frozen_tool" ||
		wrapped.Description() != "[MCP:Frozen Server] frozen description" {
		t.Fatalf("strict definition = %q / %q", wrapped.Name(), wrapped.Description())
	}
	if server, tool := wrapped.MCPIdentity(); server != "Frozen Server" || tool != "Frozen.Tool" {
		t.Fatalf("strict identity = %q/%q", server, tool)
	}
	if got := wrapped.Parameters(); got["type"] != "object" ||
		got["required"].([]string)[0] != "value" {
		t.Fatalf("strict parameters retained snapshot aliases: %#v", got)
	}

	ctx := WithToolContext(context.Background(), "telegram", "strict-chat")
	result := wrapped.Execute(ctx, map[string]any{"value": "test"})
	if result == nil || result.IsError || len(result.Media) != 1 || len(result.ArtifactTags) != 1 {
		t.Fatalf("strict execute result = %#v", result)
	}
	if call := manager.lastCall(); call != (snapshotMCPCall{server: "Frozen Server", tool: "Frozen.Tool"}) {
		t.Fatalf("manager call used mutable identity: %#v", call)
	}

	const artifactPrefix = "[file:"
	artifactPath := strings.TrimSuffix(strings.TrimPrefix(result.ArtifactTags[0], artifactPrefix), "]")
	if !strings.HasPrefix(artifactPath, workspace+string(os.PathSeparator)) ||
		strings.HasPrefix(artifactPath, ignoredWorkspace+string(os.PathSeparator)) {
		t.Fatalf("strict artifact path = %q", artifactPath)
	}
	wantArtifactPrefix := picomcp.CanonicalToolNameComponent("Frozen Server") + "_" +
		picomcp.CanonicalToolNameComponent("Frozen.Tool") + "_"
	if !strings.HasPrefix(filepath.Base(artifactPath), wantArtifactPrefix) {
		t.Fatalf("artifact filename = %q, want prefix %q", filepath.Base(artifactPath), wantArtifactPrefix)
	}

	mediaPath, meta, err := latestStore.ResolveWithMeta(result.Media[0])
	if err != nil {
		t.Fatalf("latest media store did not receive strict output: %v", err)
	}
	defer os.Remove(mediaPath)
	if _, err := firstStore.Resolve(result.Media[0]); err == nil {
		t.Fatal("strict SetMediaStore retained the superseded owner store")
	}
	wantFilename := picomcp.CanonicalToolNameComponent("Frozen Server") + "_" +
		picomcp.CanonicalToolNameComponent("Frozen.Tool") + ".png"
	if meta.Filename != wantFilename ||
		meta.Source != "tool:mcp:frozen_server:frozen_tool" {
		t.Fatalf("frozen media metadata = %#v", meta)
	}

	for range 2 {
		event := receiveMCPToolRuntimeEvent(t, eventsCh)
		payload, ok := event.Payload.(MCPToolCallPayload)
		if !ok || payload.Server != "Frozen Server" || payload.Tool != "Frozen.Tool" ||
			event.Source.Name != "Frozen Server" {
			t.Fatalf("strict runtime event = %#v", event)
		}
	}
	if ignoredEvents.Stats().Published != 0 {
		t.Fatal("strict SetEventPublisher replaced the frozen event bus")
	}

	if closer, ok := any(wrapped).(interface{ Close() error }); ok {
		if err := closer.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if manager.closeCalls.Load() != 0 {
		t.Fatal("MCP wrapper assumed ownership of its borrowed manager")
	}
}

func TestMCPToolConcurrentMutableStateAndExecute(t *testing.T) {
	for _, strict := range []bool{false, true} {
		label := "legacy"
		if strict {
			label = "strict"
		}
		t.Run(label, func(t *testing.T) {
			manager := &snapshotMCPManager{result: func() *mcp.CallToolResult {
				return &mcp.CallToolResult{Content: []mcp.Content{
					&mcp.TextContent{Text: strings.Repeat("race payload ", 8)},
					&mcp.ImageContent{Data: []byte("race-image"), MIMEType: "image/png"},
				}}
			}}
			firstEvents := runtimeevents.NewBus()
			secondEvents := runtimeevents.NewBus()
			defer func() {
				_ = firstEvents.Close()
				_ = secondEvents.Close()
			}()
			firstWorkspace := t.TempDir()
			secondWorkspace := t.TempDir()
			var wrapped *MCPTool
			if strict {
				wrapped = NewMCPToolFromSnapshot(manager, MCPToolSnapshot{
					ServerName: "race-server", ToolName: "race-tool",
					CanonicalName: picomcp.CanonicalToolName("race-server", "race-tool"),
					Description:   "[MCP:race-server] race tool",
					Parameters:    map[string]any{"type": "object"},
					Workspace:     firstWorkspace, MaxInlineTextRunes: 8,
					EventPublisher: firstEvents,
				})
			} else {
				wrapped = NewMCPTool(manager, "race-server", &mcp.Tool{Name: "race-tool"})
				wrapped.SetWorkspace(firstWorkspace)
				wrapped.SetMaxInlineTextRunes(8)
				wrapped.SetEventPublisher(firstEvents)
			}
			firstStore := &discardMCPMediaStore{}
			secondStore := &discardMCPMediaStore{}
			wrapped.SetMediaStore(firstStore)

			const executions = 32
			var wg sync.WaitGroup
			errorsCh := make(chan string, executions)
			for range 4 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for range executions / 4 {
						result := wrapped.Execute(
							WithToolContext(context.Background(), "race", "chat"),
							nil,
						)
						if result == nil || result.IsError {
							errorsCh <- fmt.Sprintf("execute result = %#v", result)
						}
					}
				}()
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				for iteration := range executions {
					if iteration%2 == 0 {
						wrapped.SetMediaStore(firstStore)
						wrapped.SetWorkspace(firstWorkspace)
						wrapped.SetMaxInlineTextRunes(8)
						wrapped.SetEventPublisher(firstEvents)
					} else {
						wrapped.SetMediaStore(secondStore)
						wrapped.SetWorkspace(secondWorkspace)
						wrapped.SetMaxInlineTextRunes(1 << 20)
						wrapped.SetEventPublisher(secondEvents)
					}
					_ = wrapped.Parameters()
					_, _ = wrapped.MCPIdentity()
				}
			}()
			wg.Wait()
			close(errorsCh)
			for message := range errorsCh {
				t.Error(message)
			}
			manager.mu.Lock()
			callCount := len(manager.calls)
			manager.mu.Unlock()
			if callCount != executions {
				t.Fatalf("manager calls = %d, want %d", callCount, executions)
			}
			if firstStore.calls.Load()+secondStore.calls.Load() != executions {
				t.Fatalf(
					"media stores received %d calls, want %d",
					firstStore.calls.Load()+secondStore.calls.Load(),
					executions,
				)
			}
		})
	}
}

func TestMCPToolLegacySchemaNormalizationAndCloneBoundaries(t *testing.T) {
	empty := func(value map[string]any) bool {
		properties, propertiesOK := value["properties"].(map[string]any)
		required, requiredOK := value["required"].([]string)
		return value["type"] == "object" && propertiesOK && len(properties) == 0 &&
			requiredOK && len(required) == 0
	}

	wrapped := NewMCPTool(nil, "nil-sdk", nil)
	if wrapped == nil {
		t.Fatal("nil SDK snapshot returned nil wrapper")
	}
	server, tool := wrapped.MCPIdentity()
	if server != "nil-sdk" || tool != "" || !empty(wrapped.Parameters()) {
		t.Fatalf("nil SDK snapshot = %#v, %q/%q", wrapped, server, tool)
	}
	defaultLimit := NewMCPToolFromSnapshot(nil, MCPToolSnapshot{
		ServerName: "default", ToolName: "limit", CanonicalName: "mcp_default_limit",
		Description: "default limit", Parameters: map[string]any{"type": "object"},
	})
	defaultLimit.stateMu.RLock()
	gotLimit := defaultLimit.maxInlineTextRunes
	defaultLimit.stateMu.RUnlock()
	if gotLimit != maxMCPInlineTextRunes {
		t.Fatalf("default strict inline limit = %d", gotLimit)
	}

	validSchemas := []any{
		json.RawMessage(`{"type":"object","raw":true}`),
		[]byte(`{"type":"object","bytes":true}`),
		struct {
			Type string `json:"type"`
		}{Type: "object"},
	}
	for index, schema := range validSchemas {
		if got := normalizeLegacyMCPParameters(schema); got["type"] != "object" {
			t.Fatalf("valid legacy schema %d = %#v", index, got)
		}
	}
	for index, schema := range []any{
		make(chan int),
		json.RawMessage(`{"type":`),
		json.RawMessage(`null`),
	} {
		if got := normalizeLegacyMCPParameters(schema); !empty(got) {
			t.Fatalf("invalid legacy schema %d did not fall back: %#v", index, got)
		}
	}
	if got := detachMCPParameters(nil); !empty(got) {
		t.Fatalf("nil detached schema = %#v", got)
	}

	clone := func(value any, depth int) bool {
		_, ok := cloneMCPParameterValue(
			reflect.ValueOf(value),
			make(map[mcpParameterVisit]struct{}),
			depth,
		)
		return ok
	}
	if _, ok := cloneMCPParameterValue(
		reflect.Value{}, make(map[mcpParameterVisit]struct{}), 0,
	); ok || clone("too deep", 129) {
		t.Fatal("invalid or over-depth schema clone succeeded")
	}
	if !clone(map[string]any{
		"nil": nil, "bool": true, "int": int64(1), "uint": uint64(2),
		"float": 3.5, "array": [2]string{"a", "b"},
	}, 0) {
		t.Fatal("supported scalar/array schema clone failed")
	}
	if clone(map[int]string{1: "bad"}, 0) {
		t.Fatal("non-string-key schema map clone succeeded")
	}
	var nilMap map[string]any
	if !clone(nilMap, 0) {
		t.Fatal("typed nil schema map clone failed")
	}
	cyclicMap := map[string]any{}
	cyclicMap["self"] = cyclicMap
	if clone(cyclicMap, 0) || clone(map[string]any{"bad": func() {}}, 0) {
		t.Fatal("cyclic or unsupported map schema clone succeeded")
	}
	if got := detachMCPParameters(cyclicMap); !empty(got) {
		t.Fatalf("cyclic detached schema did not fall back: %#v", got)
	}
	var nilSlice []any
	if !clone(nilSlice, 0) {
		t.Fatal("typed nil schema slice clone failed")
	}
	cyclicSlice := make([]any, 1)
	cyclicSlice[0] = cyclicSlice
	if clone(cyclicSlice, 0) || clone([]any{func() {}}, 0) ||
		clone([1]any{func() {}}, 0) || clone(struct{}{}, 0) {
		t.Fatal("cyclic or unsupported slice/array/value schema clone succeeded")
	}
}
