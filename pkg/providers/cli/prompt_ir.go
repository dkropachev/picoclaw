package cliprovider

import (
	"strings"

	"github.com/sipeed/picoclaw/pkg/providers/promptir"
)

func cliSystemPrompt(messages []Message, tools []ToolDefinition) string {
	prompt := promptir.FromMessagesWithTools(messages, tools)
	parts := make([]string, 0)
	for _, item := range prompt.Items {
		if !promptir.IsStableInstruction(item) {
			continue
		}
		if text := promptir.ReadableParts(item.Parts); text != "" {
			parts = append(parts, text)
		}
	}

	if len(tools) > 0 {
		parts = append(parts, buildCLIToolsPrompt(tools))
	}

	return strings.Join(parts, "\n\n")
}

func cliConversationParts(messages []Message, prefixUser bool) []string {
	prompt := promptir.FromMessages(messages)
	parts := make([]string, 0, len(prompt.Items))

	for _, item := range prompt.Items {
		switch item.Type {
		case promptir.ItemTypeContext:
			if promptir.IsStableInstruction(item) {
				continue
			}
			if text := promptir.ReadableParts(item.Parts); text != "" {
				parts = append(parts, "["+promptir.ContextLabel(item)+"]\n"+text)
			}

		case promptir.ItemTypeMessage:
			text := promptir.ReadableParts(item.Parts)
			switch item.Role {
			case promptir.RoleAssistant:
				parts = append(parts, "Assistant: "+text)
			case promptir.RoleUser:
				if prefixUser {
					parts = append(parts, "User: "+text)
				} else {
					parts = append(parts, text)
				}
			default:
				parts = append(parts, "["+string(item.Role)+"] "+text)
			}

		case promptir.ItemTypeToolCall:
			parts = append(parts, "[Tool Call "+item.ToolCallID+" "+item.ToolName+"]: "+item.ToolArguments)

		case promptir.ItemTypeToolResult:
			output := promptir.ReadableParts(item.ToolOutput)
			if output == "" {
				output = promptir.ReadableParts(item.Parts)
			}
			parts = append(parts, "[Tool Result for "+item.ToolCallID+"]: "+output)

		case promptir.ItemTypeReasoning:
			if text := promptir.ReadableParts(item.Parts); text != "" {
				parts = append(parts, "Assistant reasoning: "+text)
			}
		}
	}

	return parts
}
