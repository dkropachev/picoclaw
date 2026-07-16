package promptir

import "github.com/sipeed/picoclaw/pkg/providers/protocoltypes"

type Prompt struct {
	Items    []Item
	Tools    []ToolDefinition
	Metadata PromptMetadata
}

type PromptMetadata map[string]any

type ToolDefinition = protocoltypes.ToolDefinition

type Item struct {
	Type  ItemType
	Role  Role
	Parts []Part

	ToolCallID    string
	ToolName      string
	ToolArguments string
	ToolOutput    []Part
	ToolInput     map[string]any

	ToolThoughtSignature string

	Cache  CacheHint
	Scope  ContextScope
	Source PromptSource
}

type Part = protocoltypes.PromptPart

type ItemType string

const (
	ItemTypeMessage    ItemType = "message"
	ItemTypeToolCall   ItemType = "tool_call"
	ItemTypeToolResult ItemType = "tool_result"
	ItemTypeReasoning  ItemType = "reasoning"
	ItemTypeContext    ItemType = "context"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleDeveloper Role = "developer"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type PartType string

const (
	PartTypeText  PartType = "text"
	PartTypeImage PartType = "image"
	PartTypeAudio PartType = "audio"
	PartTypeFile  PartType = "file"
)

type ContextScope string

const (
	ScopeStableInstruction ContextScope = "stable_instruction"
	ScopeRuntime           ContextScope = "runtime"
	ScopeSummary           ContextScope = "summary"
	ScopeHistory           ContextScope = "history"
	ScopeCurrentTurn       ContextScope = "current_turn"
)

type CacheHint string

const (
	CacheNone      CacheHint = "none"
	CacheStable    CacheHint = "stable"
	CacheEphemeral CacheHint = "ephemeral"
)

type PromptSource string
