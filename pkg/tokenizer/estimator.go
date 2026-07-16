package tokenizer

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/providers/promptir"
)

// EstimateMessageTokens estimates the token count for a single message,
// including Content, ReasoningContent, ToolCalls arguments, ToolCallID
// metadata, and Media items. Uses a heuristic of 2.5 characters per token.
func EstimateMessageTokens(msg providers.Message) int {
	return EstimatePromptTokens(promptir.FromMessages([]providers.Message{msg}))
}

// EstimatePromptTokens estimates the token count for a provider-neutral prompt.
func EstimatePromptTokens(prompt promptir.Prompt) int {
	total := 0
	for _, item := range prompt.Items {
		total += EstimatePromptItemTokens(item)
	}
	return total
}

// EstimateStableInstructionTokens estimates the cacheable/stable instruction
// portion of a prompt separately from runtime context and history.
func EstimateStableInstructionTokens(prompt promptir.Prompt) int {
	total := 0
	for _, item := range prompt.Items {
		if promptir.IsStableInstruction(item) {
			total += EstimatePromptItemTokens(item)
		}
	}
	return total
}

// EstimatePromptItemTokens estimates one IR item. Text is counted with the
// package's existing 2.5 characters/token heuristic; media and files use fixed
// per-part estimates because provider pricing depends on native media handling.
func EstimatePromptItemTokens(item promptir.Item) int {
	chars := 0
	chars += utf8.RuneCountInString(item.ToolCallID)
	chars += utf8.RuneCountInString(item.ToolName)
	chars += utf8.RuneCountInString(item.ToolArguments)
	parts := item.Parts
	if item.Type == promptir.ItemTypeToolResult && len(item.ToolOutput) > 0 {
		parts = item.ToolOutput
	}
	chars += textChars(parts)

	// Per-item overhead for role labels, JSON/content-block structure, and separators.
	const itemOverhead = 12
	chars += itemOverhead

	tokens := chars * 2 / 5
	tokens += mediaTokens(parts)
	return tokens
}

// EstimateToolDefsTokens estimates the total token cost of tool definitions
// as they appear in the LLM request.
func EstimateToolDefsTokens(defs []providers.ToolDefinition) int {
	if len(defs) == 0 {
		return 0
	}

	totalChars := 0
	for _, d := range defs {
		totalChars += len(d.Function.Name) + len(d.Function.Description)

		if d.Function.Parameters != nil {
			if paramJSON, err := json.Marshal(d.Function.Parameters); err == nil {
				totalChars += len(paramJSON)
			}
		}

		// Per-tool overhead: type field, JSON structure, separators.
		totalChars += 20
	}

	return totalChars * 2 / 5
}

func textChars(parts []promptir.Part) int {
	total := 0
	for _, part := range parts {
		if part.Type == string(promptir.PartTypeText) || part.Type == "" {
			total += utf8.RuneCountInString(part.Text)
		}
	}
	return total
}

func mediaTokens(parts []promptir.Part) int {
	total := 0
	for _, part := range parts {
		switch part.Type {
		case string(promptir.PartTypeImage):
			total += 256
		case string(promptir.PartTypeAudio):
			total += 128
		case string(promptir.PartTypeFile):
			total += 64
		}
	}
	return total
}
