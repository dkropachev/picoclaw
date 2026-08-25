package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	picomcp "github.com/sipeed/picoclaw/pkg/mcp"
	integrationtools "github.com/sipeed/picoclaw/pkg/tools/integration"
)

// NewMCPToolWithFactory snapshots one untrusted SDK tool declaration and
// returns both the compatibility-generation wrapper and its per-owner factory.
// The manager and event publisher are borrowed generation services: neither
// the live wrapper nor owner products close them.
func NewMCPToolWithFactory(
	manager MCPManager,
	serverName string,
	remote *sdkmcp.Tool,
	workspace string,
	maxInlineTextRunes int,
	publisher runtimeevents.Bus,
) (*MCPTool, ToolFactory, error) {
	if isTypedNil(manager) {
		return nil, nil, fmt.Errorf("MCP manager is nil")
	}
	if serverName == "" || serverName != strings.TrimSpace(serverName) {
		return nil, nil, fmt.Errorf("MCP server name must be exact and non-empty")
	}
	if remote == nil {
		return nil, nil, fmt.Errorf("MCP SDK tool is nil")
	}
	toolName := remote.Name
	description := remote.Description
	inputSchema := remote.InputSchema
	if toolName == "" || toolName != strings.TrimSpace(toolName) {
		return nil, nil, fmt.Errorf("MCP tool name must be exact and non-empty")
	}

	parameters, err := snapshotMCPInputSchema(inputSchema)
	if err != nil {
		return nil, nil, fmt.Errorf(
			"snapshot MCP tool %q/%q input schema: %w",
			serverName,
			toolName,
			err,
		)
	}

	canonicalName := picomcp.CanonicalToolName(serverName, toolName)
	if description == "" {
		description = fmt.Sprintf("MCP tool from %s server", serverName)
	}
	description = fmt.Sprintf("[MCP:%s] %s", serverName, description)
	descriptor, err := freezeToolDescriptor(ToolDescriptor{
		Name:        canonicalName,
		Description: description,
		Parameters:  parameters,
		PromptMetadata: PromptMetadata{
			Layer:  ToolPromptLayerCapability,
			Slot:   ToolPromptSlotMCP,
			Source: "mcp:" + picomcp.CanonicalToolNameComponent(serverName),
		},
	})
	if err != nil {
		return nil, nil, fmt.Errorf(
			"freeze MCP tool %q/%q descriptor: %w",
			serverName,
			toolName,
			err,
		)
	}

	effectiveInlineLimit := maxInlineTextRunes
	if effectiveInlineLimit <= 0 {
		effectiveInlineLimit = config.DefaultMCPMaxInlineTextChars
	}
	baseSnapshot := integrationtools.MCPToolSnapshot{
		ServerName:         serverName,
		ToolName:           toolName,
		CanonicalName:      descriptor.Name,
		Description:        descriptor.Description,
		Parameters:         cloneToolDescriptor(descriptor).Parameters,
		Workspace:          strings.TrimSpace(workspace),
		MaxInlineTextRunes: effectiveInlineLimit,
		EventPublisher:     publisher,
	}
	build := func() *MCPTool {
		snapshot := baseSnapshot
		snapshot.Parameters = cloneToolDescriptor(descriptor).Parameters
		return integrationtools.NewMCPToolFromSnapshot(manager, snapshot)
	}

	live := build()
	factory, err := NewToolFactory(
		descriptor,
		ToolTraits{
			Risk:        ToolRiskUnknown,
			Parallel:    ToolParallelSerialized,
			Idempotency: ToolIdempotencyUnknown,
			Sharing:     ToolSharingPerOwner,
		},
		func(ToolBuildContext) (Tool, error) {
			return build(), nil
		},
	)
	if err != nil {
		return nil, nil, fmt.Errorf(
			"build MCP tool %q/%q factory: %w",
			serverName,
			toolName,
			err,
		)
	}
	return live, factory, nil
}

func snapshotMCPInputSchema(schema any) (result map[string]any, err error) {
	defer func() {
		if recover() != nil {
			result = nil
			err = fmt.Errorf("input schema conversion panicked")
		}
	}()
	if schema == nil {
		return emptyMCPInputSchema(), nil
	}
	if isTypedNil(schema) {
		return nil, fmt.Errorf("input schema is typed nil")
	}
	if direct, ok := schema.(map[string]any); ok {
		return freezeMCPInputSchemaMap(direct)
	}

	var encoded []byte
	switch value := schema.(type) {
	case json.RawMessage:
		encoded = append([]byte(nil), value...)
	case []byte:
		encoded = append([]byte(nil), value...)
	default:
		var err error
		encoded, err = json.Marshal(schema)
		if err != nil {
			return nil, fmt.Errorf("marshal input schema: %w", err)
		}
	}

	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode input schema: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("decode input schema: multiple JSON values")
		}
		return nil, fmt.Errorf("decode input schema trailing data: %w", err)
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("input schema must be one JSON object")
	}
	return freezeMCPInputSchemaMap(object)
}

func freezeMCPInputSchemaMap(schema map[string]any) (map[string]any, error) {
	frozen, err := freezeToolDescriptor(ToolDescriptor{
		Name:       "mcp_input_schema",
		Parameters: schema,
	})
	if err != nil {
		return nil, err
	}
	if _, err := json.Marshal(frozen.Parameters); err != nil {
		return nil, fmt.Errorf("encode input schema: %w", err)
	}
	return frozen.Parameters, nil
}

func emptyMCPInputSchema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
		"required":   []string{},
	}
}
