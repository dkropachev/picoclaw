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
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/logger"
	picomcp "github.com/sipeed/picoclaw/pkg/mcp"
	"github.com/sipeed/picoclaw/pkg/media"
	toolshared "github.com/sipeed/picoclaw/pkg/tools/shared"
)

// MCPManager defines the interface for MCP manager operations
// This allows for easier testing with mock implementations
type MCPManager interface {
	CallTool(
		ctx context.Context,
		serverName, toolName string,
		arguments map[string]any,
	) (*mcp.CallToolResult, error)
}

// MCPTool wraps an MCP tool to implement the Tool interface
type MCPTool struct {
	manager       MCPManager
	serverName    string
	toolName      string
	canonicalName string
	description   string
	parameters    map[string]any

	stateMu            sync.RWMutex
	mediaStore         media.MediaStore
	workspace          string
	maxInlineTextRunes int
	runtimeEvents      runtimeevents.Bus
	definitionFrozen   bool
}

// MCPToolSnapshot is a detached, generation-stable MCP tool definition. The
// manager remains a borrowed runtime-generation service; the snapshot contains
// no SDK tool pointer and NewMCPToolFromSnapshot recursively detaches Parameters
// before retaining them.
type MCPToolSnapshot struct {
	ServerName         string
	ToolName           string
	CanonicalName      string
	Description        string
	Parameters         map[string]any
	Workspace          string
	MaxInlineTextRunes int
	EventPublisher     runtimeevents.Bus
}

// MCPToolCallPayload describes MCP tool execution runtime events.
type MCPToolCallPayload struct {
	Server     string `json:"server"`
	Tool       string `json:"tool"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
	Error      string `json:"error,omitempty"`
}

// NewMCPTool creates a source-compatible MCP wrapper while immediately
// snapshotting the mutable SDK definition. Malformed non-nil schemas preserve
// the legacy empty-object fallback.
func NewMCPTool(manager MCPManager, serverName string, tool *mcp.Tool) *MCPTool {
	toolName := ""
	description := ""
	var parameters map[string]any
	if tool != nil {
		toolName = tool.Name
		description = tool.Description
		parameters = normalizeLegacyMCPParameters(tool.InputSchema)
	} else {
		parameters = emptyMCPParameters()
	}
	return newMCPToolFromSnapshot(manager, MCPToolSnapshot{
		ServerName:         serverName,
		ToolName:           toolName,
		CanonicalName:      picomcp.CanonicalToolName(serverName, toolName),
		Description:        finalMCPToolDescription(serverName, description),
		Parameters:         parameters,
		MaxInlineTextRunes: maxMCPInlineTextRunes,
	}, false)
}

// NewMCPToolFromSnapshot builds a strict wrapper whose definition and runtime
// configuration cannot be changed through the legacy setters. SetMediaStore
// remains mutable so each destination registry can inject owner-local media.
func NewMCPToolFromSnapshot(manager MCPManager, snapshot MCPToolSnapshot) *MCPTool {
	return newMCPToolFromSnapshot(manager, snapshot, true)
}

func newMCPToolFromSnapshot(
	manager MCPManager,
	snapshot MCPToolSnapshot,
	definitionFrozen bool,
) *MCPTool {
	limit := snapshot.MaxInlineTextRunes
	if limit <= 0 {
		limit = maxMCPInlineTextRunes
	}
	return &MCPTool{
		manager:            manager,
		serverName:         snapshot.ServerName,
		toolName:           snapshot.ToolName,
		canonicalName:      snapshot.CanonicalName,
		description:        snapshot.Description,
		parameters:         detachMCPParameters(snapshot.Parameters),
		workspace:          strings.TrimSpace(snapshot.Workspace),
		maxInlineTextRunes: limit,
		runtimeEvents:      snapshot.EventPublisher,
		definitionFrozen:   definitionFrozen,
	}
}

func (t *MCPTool) SetMediaStore(store media.MediaStore) {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	t.mediaStore = store
}

func (t *MCPTool) SetWorkspace(workspace string) {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	if t.definitionFrozen {
		return
	}
	t.workspace = strings.TrimSpace(workspace)
}

func (t *MCPTool) SetMaxInlineTextRunes(limit int) {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	if t.definitionFrozen {
		return
	}
	if limit > 0 {
		t.maxInlineTextRunes = limit
	}
}

// SetEventPublisher injects the runtime event bus used for MCP tool observations.
func (t *MCPTool) SetEventPublisher(eventBus runtimeevents.Bus) {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	if t.definitionFrozen {
		return
	}
	t.runtimeEvents = eventBus
}

const maxMCPInlineTextRunes = 16 * 1024

func emptyMCPParameters() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
		"required":   []string{},
	}
}

func finalMCPToolDescription(serverName, description string) string {
	if description == "" {
		description = fmt.Sprintf("MCP tool from %s server", serverName)
	}
	return fmt.Sprintf("[MCP:%s] %s", serverName, description)
}

func normalizeLegacyMCPParameters(schema any) map[string]any {
	if schema == nil {
		return emptyMCPParameters()
	}
	if schemaMap, ok := schema.(map[string]any); ok {
		return detachMCPParameters(schemaMap)
	}

	var jsonData []byte
	switch raw := schema.(type) {
	case json.RawMessage:
		jsonData = raw
	case []byte:
		jsonData = raw
	default:
		encoded, err := json.Marshal(schema)
		if err != nil {
			return emptyMCPParameters()
		}
		jsonData = encoded
	}

	var result map[string]any
	if err := json.Unmarshal(jsonData, &result); err != nil || result == nil {
		return emptyMCPParameters()
	}
	return detachMCPParameters(result)
}

type mcpParameterVisit struct {
	typeOf reflect.Type
	kind   reflect.Kind
	ptr    uintptr
}

func detachMCPParameters(source map[string]any) map[string]any {
	if source == nil {
		return emptyMCPParameters()
	}
	cloned, ok := cloneMCPParameterValue(
		reflect.ValueOf(source),
		make(map[mcpParameterVisit]struct{}),
		0,
	)
	if !ok || !cloned.IsValid() {
		return emptyMCPParameters()
	}
	// source is a non-nil map[string]any and the clone preserves exact map
	// types, so a successful clone has this exact result type.
	return cloned.Interface().(map[string]any)
}

func cloneMCPParameterValue(
	value reflect.Value,
	active map[mcpParameterVisit]struct{},
	depth int,
) (reflect.Value, bool) {
	if depth > 128 || !value.IsValid() {
		return reflect.Value{}, false
	}
	if value.Kind() == reflect.Interface {
		if value.IsNil() {
			return reflect.Zero(value.Type()), true
		}
		cloned, ok := cloneMCPParameterValue(value.Elem(), active, depth+1)
		if !ok {
			return reflect.Value{}, false
		}
		result := reflect.New(value.Type()).Elem()
		result.Set(cloned)
		return result, true
	}

	switch value.Kind() {
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return reflect.Value{}, false
		}
		if value.IsNil() {
			return reflect.Zero(value.Type()), true
		}
		visit := mcpParameterVisit{
			typeOf: value.Type(), kind: value.Kind(), ptr: value.Pointer(),
		}
		if _, exists := active[visit]; exists {
			return reflect.Value{}, false
		}
		active[visit] = struct{}{}
		defer delete(active, visit)
		result := reflect.MakeMapWithSize(value.Type(), value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			cloned, ok := cloneMCPParameterValue(iterator.Value(), active, depth+1)
			if !ok {
				return reflect.Value{}, false
			}
			result.SetMapIndex(iterator.Key(), cloned)
		}
		return result, true
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type()), true
		}
		visit := mcpParameterVisit{
			typeOf: value.Type(), kind: value.Kind(), ptr: value.Pointer(),
		}
		if _, exists := active[visit]; exists {
			return reflect.Value{}, false
		}
		active[visit] = struct{}{}
		defer delete(active, visit)
		result := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for index := range value.Len() {
			cloned, ok := cloneMCPParameterValue(value.Index(index), active, depth+1)
			if !ok {
				return reflect.Value{}, false
			}
			result.Index(index).Set(cloned)
		}
		return result, true
	case reflect.Array:
		result := reflect.New(value.Type()).Elem()
		for index := range value.Len() {
			cloned, ok := cloneMCPParameterValue(value.Index(index), active, depth+1)
			if !ok {
				return reflect.Value{}, false
			}
			result.Index(index).Set(cloned)
		}
		return result, true
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return value, true
	default:
		return reflect.Value{}, false
	}
}

// Name returns the tool name, prefixed with the server name.
// The total length is capped at 64 characters (OpenAI-compatible API limit).
// A short hash of the original (unsanitized) server and tool names is appended
// whenever sanitization is lossy or the name is truncated. MCP initialization
// rejects any remaining canonical-name collision before registration.
func (t *MCPTool) Name() string {
	return t.canonicalName
}

// MCPIdentity returns the exact MCP server and tool names represented by this
// wrapper. It deliberately exposes immutable strings rather than the mutable
// SDK tool descriptor.
func (t *MCPTool) MCPIdentity() (serverName, toolName string) {
	if t == nil {
		return "", ""
	}
	return t.serverName, t.toolName
}

// Description returns the tool description
func (t *MCPTool) Description() string {
	return t.description
}

func (t *MCPTool) PromptMetadata() toolshared.PromptMetadata {
	return toolshared.PromptMetadata{
		Layer:  toolshared.ToolPromptLayerCapability,
		Slot:   toolshared.ToolPromptSlotMCP,
		Source: "mcp:" + picomcp.CanonicalToolNameComponent(t.serverName),
	}
}

// Parameters returns the tool parameters schema
func (t *MCPTool) Parameters() map[string]any {
	return detachMCPParameters(t.parameters)
}

// Execute executes the MCP tool
func (t *MCPTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	startedAt := time.Now()
	t.publishRuntimeEvent(ctx, runtimeevents.KindMCPToolCallStart, startedAt, false, "")

	result, err := t.manager.CallTool(ctx, t.serverName, t.toolName, args)
	if err != nil {
		t.publishRuntimeEvent(ctx, runtimeevents.KindMCPToolCallEnd, startedAt, true, err.Error())
		return ErrorResult(fmt.Sprintf("MCP tool execution failed: %v", err)).WithError(err)
	}

	if result == nil {
		nilErr := fmt.Errorf("MCP tool returned nil result without error")
		t.publishRuntimeEvent(ctx, runtimeevents.KindMCPToolCallEnd, startedAt, true, nilErr.Error())
		return ErrorResult("MCP tool execution failed: nil result").WithError(nilErr)
	}

	// Handle error result from server
	if result.IsError {
		errMsg := extractContentText(result.Content)
		t.publishRuntimeEvent(ctx, runtimeevents.KindMCPToolCallEnd, startedAt, true, errMsg)
		return ErrorResult(fmt.Sprintf("MCP tool returned error: %s", errMsg)).
			WithError(fmt.Errorf("MCP tool error: %s", errMsg))
	}

	t.publishRuntimeEvent(ctx, runtimeevents.KindMCPToolCallEnd, startedAt, false, "")
	return t.normalizeResultContent(ctx, result.Content)
}

func (t *MCPTool) publishRuntimeEvent(
	ctx context.Context,
	kind runtimeevents.Kind,
	startedAt time.Time,
	isError bool,
	errMsg string,
) {
	if t == nil {
		return
	}
	t.stateMu.RLock()
	eventPublisher := t.runtimeEvents
	t.stateMu.RUnlock()
	if eventPublisher == nil {
		return
	}

	scope := runtimeevents.Scope{
		AgentID:    toolshared.ToolAgentID(ctx),
		SessionKey: toolshared.ToolSessionKey(ctx),
		Channel:    toolshared.ToolChannel(ctx),
		ChatID:     toolshared.ToolChatID(ctx),
		MessageID:  toolshared.ToolMessageID(ctx),
	}
	payload := MCPToolCallPayload{
		Server:     t.serverName,
		Tool:       t.toolName,
		DurationMS: time.Since(startedAt).Milliseconds(),
		IsError:    isError,
		Error:      errMsg,
	}
	severity := runtimeevents.SeverityInfo
	if isError {
		severity = runtimeevents.SeverityError
	}

	eventPublisher.PublishNonBlocking(runtimeevents.Event{
		Kind:     kind,
		Source:   runtimeevents.Source{Component: "mcp", Name: t.serverName},
		Scope:    scope,
		Severity: severity,
		Payload:  payload,
		Attrs:    mcpToolCallEventAttrs(payload),
	})
}

func mcpToolCallEventAttrs(payload MCPToolCallPayload) map[string]any {
	attrs := map[string]any{
		"server":      payload.Server,
		"tool":        payload.Tool,
		"duration_ms": payload.DurationMS,
	}
	if payload.IsError {
		attrs["is_error"] = payload.IsError
	}
	if payload.Error != "" {
		attrs["error"] = payload.Error
	}
	return attrs
}

// extractContentText extracts text from MCP content array
func extractContentText(content []mcp.Content) string {
	var parts []string
	for _, c := range content {
		switch v := c.(type) {
		case *mcp.TextContent:
			parts = append(parts, sanitizeToolLLMContent(v.Text))
		case *mcp.ImageContent:
			parts = append(parts, fmt.Sprintf("[Image: %s]", normalizedMIMEType(v.MIMEType)))
		case *mcp.AudioContent:
			parts = append(parts, fmt.Sprintf("[Audio: %s]", normalizedMIMEType(v.MIMEType)))
		case *mcp.ResourceLink:
			parts = append(parts, summarizeResourceLink(v))
		case *mcp.EmbeddedResource:
			parts = append(parts, summarizeEmbeddedResource(v))
		default:
			// For other content types, use string representation
			parts = append(parts, fmt.Sprintf("[Content: %T]", v))
		}
	}
	return sanitizeToolLLMContent(strings.Join(parts, "\n"))
}

func (t *MCPTool) normalizeResultContent(ctx context.Context, content []mcp.Content) *ToolResult {
	llmParts := make([]string, 0, len(content))
	rawTextParts := make([]string, 0, len(content))
	mediaRefs := make([]string, 0, len(content))

	for _, c := range content {
		switch v := c.(type) {
		case *mcp.TextContent:
			rawText := strings.TrimSpace(v.Text)
			if rawText != "" {
				rawTextParts = append(rawTextParts, rawText)
			}
			safeText := strings.TrimSpace(sanitizeToolLLMContent(v.Text))
			if safeText != "" {
				llmParts = append(llmParts, safeText)
			}
		case *mcp.ImageContent:
			ref, note := t.storeBinaryContent(
				ctx,
				"image",
				normalizedMIMEType(v.MIMEType),
				v.Data,
				v.Annotations,
			)
			if ref != "" {
				mediaRefs = append(mediaRefs, ref)
			}
			if note != "" {
				llmParts = append(llmParts, note)
			}
		case *mcp.AudioContent:
			ref, note := t.storeBinaryContent(
				ctx,
				"audio",
				normalizedMIMEType(v.MIMEType),
				v.Data,
				v.Annotations,
			)
			if ref != "" {
				mediaRefs = append(mediaRefs, ref)
			}
			if note != "" {
				llmParts = append(llmParts, note)
			}
		case *mcp.ResourceLink:
			llmParts = append(llmParts, summarizeResourceLink(v))
		case *mcp.EmbeddedResource:
			ref, note, rawText := t.storeEmbeddedResource(ctx, v)
			if ref != "" {
				mediaRefs = append(mediaRefs, ref)
			}
			if rawText != "" {
				rawTextParts = append(rawTextParts, rawText)
			}
			if note != "" {
				llmParts = append(llmParts, note)
			}
		default:
			llmParts = append(llmParts, fmt.Sprintf("[MCP returned unsupported content type %T]", v))
		}
	}

	forLLM := strings.Join(compactStrings(llmParts), "\n")
	rawText := strings.Join(compactStrings(rawTextParts), "\n")
	if artifactResult := t.persistLargeTextArtifact(rawText); artifactResult != nil {
		artifactResult.Media = mediaRefs
		return artifactResult
	}

	result := &ToolResult{
		ForLLM: forLLM,
		Media:  mediaRefs,
	}
	return result
}

func (t *MCPTool) persistLargeTextArtifact(text string) *ToolResult {
	text = strings.TrimSpace(text)
	t.stateMu.RLock()
	workspace := t.workspace
	limit := t.maxInlineTextRunes
	t.stateMu.RUnlock()
	size := utf8.RuneCountInString(text)
	if text == "" || size <= limit || workspace == "" {
		return nil
	}

	dir := filepath.Join(workspace, ".artifacts", "mcp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return t.largeTextArtifactFallback(text, err)
	}
	// TODO: Add lifecycle cleanup/retention for MCP artifact files.

	pattern := fmt.Sprintf(
		"%s_%s_*.txt",
		picomcp.CanonicalToolNameComponent(t.serverName),
		picomcp.CanonicalToolNameComponent(t.toolName),
	)
	tmpFile, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return t.largeTextArtifactFallback(text, err)
	}
	path := tmpFile.Name()
	if _, err = tmpFile.WriteString(text); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(path)
		return t.largeTextArtifactFallback(text, err)
	}
	if err = tmpFile.Close(); err != nil {
		_ = os.Remove(path)
		return t.largeTextArtifactFallback(text, err)
	}

	return &ToolResult{
		ForLLM: fmt.Sprintf(
			"[MCP returned a large text result (%d chars); omitted from model context and saved as a local artifact.]",
			size,
		),
		ArtifactTags: []string{"[file:" + path + "]"},
	}
}

func (t *MCPTool) largeTextArtifactFallback(text string, err error) *ToolResult {
	size := utf8.RuneCountInString(text)
	logger.WarnCF("tool", "Failed to persist large MCP text artifact", map[string]any{
		"server": t.serverName,
		"tool":   t.toolName,
		"chars":  size,
		"error":  err.Error(),
	})
	return &ToolResult{
		ForLLM: fmt.Sprintf(
			"[MCP returned a large text result (%d chars); omitted from model context because artifact persistence failed.]",
			size,
		),
	}
}

func (t *MCPTool) storeEmbeddedResource(ctx context.Context, content *mcp.EmbeddedResource) (string, string, string) {
	if content == nil || content.Resource == nil {
		return "", "[MCP returned an embedded resource without data.]", ""
	}

	resource := content.Resource
	if len(resource.Blob) > 0 {
		ref, note := t.storeBinaryContent(
			ctx,
			"resource",
			normalizedMIMEType(resource.MIMEType),
			resource.Blob,
			content.Annotations,
		)
		return ref, note, ""
	}

	rawText := strings.TrimSpace(resource.Text)
	if rawText != "" {
		return "", sanitizeToolLLMContent(resource.Text), rawText
	}

	return "", summarizeEmbeddedResource(content), ""
}

func (t *MCPTool) storeBinaryContent(
	ctx context.Context,
	kind string,
	mimeType string,
	data []byte,
	annotations *mcp.Annotations,
) (string, string) {
	if len(data) == 0 {
		return "", fmt.Sprintf("[MCP returned %s content (%s) but it was empty.]", kind, mimeType)
	}
	if !annotationsAllowUser(annotations) {
		return "", fmt.Sprintf(
			"[MCP returned %s content (%s) for non-user audience; omitted from model context.]",
			kind,
			mimeType,
		)
	}
	t.stateMu.RLock()
	mediaStore := t.mediaStore
	t.stateMu.RUnlock()
	if mediaStore == nil {
		return "", fmt.Sprintf(
			"[MCP returned %s content (%s); omitted from model context because media delivery is unavailable.]",
			kind,
			mimeType,
		)
	}

	channel := ToolChannel(ctx)
	chatID := ToolChatID(ctx)
	if channel == "" || chatID == "" {
		return "", fmt.Sprintf(
			"[MCP returned %s content (%s); omitted from model context because no target chat was available.]",
			kind,
			mimeType,
		)
	}

	dir := media.TempDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Sprintf("[MCP returned %s content (%s) but it could not be stored.]", kind, mimeType)
	}

	ext := extensionForMIMEType(mimeType)
	tmpFile, err := os.CreateTemp(dir, "mcp-*"+ext)
	if err != nil {
		return "", fmt.Sprintf("[MCP returned %s content (%s) but it could not be stored.]", kind, mimeType)
	}
	tmpPath := tmpFile.Name()
	if _, err = tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return "", fmt.Sprintf("[MCP returned %s content (%s) but it could not be stored.]", kind, mimeType)
	}
	if err = tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Sprintf("[MCP returned %s content (%s) but it could not be stored.]", kind, mimeType)
	}

	scope := fmt.Sprintf(
		"tool:mcp:%s:%s:%s:%d",
		picomcp.CanonicalToolNameComponent(t.serverName),
		channel,
		chatID,
		time.Now().UnixNano(),
	)
	filename := fmt.Sprintf(
		"%s_%s%s",
		picomcp.CanonicalToolNameComponent(t.serverName),
		picomcp.CanonicalToolNameComponent(t.toolName),
		ext,
	)

	ref, err := mediaStore.Store(tmpPath, media.MediaMeta{
		Filename:    filename,
		ContentType: mimeType,
		Source: fmt.Sprintf(
			"tool:mcp:%s:%s",
			picomcp.CanonicalToolNameComponent(t.serverName),
			picomcp.CanonicalToolNameComponent(t.toolName),
		),
	}, scope)
	if err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Sprintf(
			"[MCP returned %s content (%s) but it could not be registered as media.]",
			kind,
			mimeType,
		)
	}

	return ref, fmt.Sprintf(
		"[MCP returned %s content (%s); omitted from model context and stored as a local media artifact.]",
		kind,
		mimeType,
	)
}

func summarizeResourceLink(content *mcp.ResourceLink) string {
	if content == nil {
		return "[MCP returned an empty resource link.]"
	}

	parts := []string{"[MCP returned resource link"}
	if content.Name != "" {
		parts = append(parts, fmt.Sprintf("name=%q", content.Name))
	}
	if content.URI != "" {
		parts = append(parts, fmt.Sprintf("uri=%q", content.URI))
	}
	if content.MIMEType != "" {
		parts = append(parts, fmt.Sprintf("mime=%q", content.MIMEType))
	}
	if content.Description != "" {
		desc := strings.TrimSpace(content.Description)
		if len(desc) > 200 {
			desc = desc[:200] + "..."
		}
		parts = append(parts, fmt.Sprintf("description=%q", desc))
	}
	return strings.Join(parts, ", ") + "]"
}

func summarizeEmbeddedResource(content *mcp.EmbeddedResource) string {
	if content == nil || content.Resource == nil {
		return "[MCP returned an embedded resource.]"
	}

	resource := content.Resource
	if resource.URI != "" {
		return fmt.Sprintf(
			"[MCP returned embedded resource %q (%s).]",
			resource.URI,
			normalizedMIMEType(resource.MIMEType),
		)
	}
	return fmt.Sprintf("[MCP returned embedded resource (%s).]", normalizedMIMEType(resource.MIMEType))
}

func annotationsAllowUser(annotations *mcp.Annotations) bool {
	if annotations == nil || len(annotations.Audience) == 0 {
		return true
	}
	for _, audience := range annotations.Audience {
		if strings.EqualFold(string(audience), "user") {
			return true
		}
	}
	return false
}

func normalizedMIMEType(mimeType string) string {
	if strings.TrimSpace(mimeType) == "" {
		return "application/octet-stream"
	}
	return mimeType
}

func compactStrings(parts []string) []string {
	compact := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			continue
		}
		compact = append(compact, part)
	}
	return compact
}
