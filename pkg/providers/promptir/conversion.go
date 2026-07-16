package promptir

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

func FromMessages(messages []protocoltypes.Message) Prompt {
	return FromMessagesWithTools(messages, nil)
}

func FromMessagesWithTools(messages []protocoltypes.Message, tools []protocoltypes.ToolDefinition) Prompt {
	prompt := Prompt{
		Items: make([]Item, 0, len(messages)),
		Tools: append([]protocoltypes.ToolDefinition(nil), tools...),
	}

	for _, msg := range messages {
		prompt.Items = append(prompt.Items, itemsFromMessage(msg)...)
	}

	return prompt
}

func ToMessages(prompt Prompt) []protocoltypes.Message {
	if len(prompt.Items) == 0 {
		return nil
	}

	messages := make([]protocoltypes.Message, 0, len(prompt.Items))
	for _, item := range prompt.Items {
		switch item.Type {
		case ItemTypeToolCall:
			tc := ToolCallFromIRItem(item)
			if tc.ID == "" {
				continue
			}
			last := len(messages) - 1
			if last >= 0 && messages[last].Role == string(RoleAssistant) && messages[last].ToolCallID == "" {
				messages[last].ToolCalls = append(messages[last].ToolCalls, tc)
			} else {
				messages = append(messages, protocoltypes.Message{
					Role:      string(RoleAssistant),
					ToolCalls: []protocoltypes.ToolCall{tc},
				})
			}

		case ItemTypeToolResult:
			messages = append(messages, MessageFromIRItem(item))

		case ItemTypeReasoning:
			text := TextFromParts(item.Parts)
			if text == "" {
				continue
			}
			last := len(messages) - 1
			if last >= 0 && messages[last].Role == string(RoleAssistant) && messages[last].ToolCallID == "" {
				messages[last].ReasoningContent = appendWithSeparator(messages[last].ReasoningContent, text, "\n")
			} else {
				messages = append(messages, protocoltypes.Message{
					Role:             string(RoleAssistant),
					ReasoningContent: text,
				})
			}

		default:
			msg := MessageFromIRItem(item)
			if msg.Role == "" {
				continue
			}
			messages = append(messages, msg)
		}
	}

	return messages
}

func MessageFromIRItem(item Item) protocoltypes.Message {
	role := string(item.Role)
	if role == "" {
		role = string(RoleUser)
	}

	switch item.Type {
	case ItemTypeToolCall:
		return protocoltypes.Message{
			Role:      string(RoleAssistant),
			ToolCalls: []protocoltypes.ToolCall{ToolCallFromIRItem(item)},
		}
	case ItemTypeToolResult:
		parts := item.ToolOutput
		if len(parts) == 0 {
			parts = item.Parts
		}
		return messageFromParts(string(RoleTool), parts, func(msg *protocoltypes.Message) {
			msg.ToolCallID = item.ToolCallID
		})
	case ItemTypeReasoning:
		return protocoltypes.Message{
			Role:             string(RoleAssistant),
			ReasoningContent: TextFromParts(item.Parts),
		}
	case ItemTypeContext:
		if role == "" {
			role = string(RoleSystem)
		}
		return messageFromParts(role, item.Parts, func(msg *protocoltypes.Message) {
			if role == string(RoleSystem) {
				msg.SystemParts = []protocoltypes.ContentBlock{contentBlockFromIRItem(item)}
			}
		})
	default:
		return messageFromParts(role, item.Parts, nil)
	}
}

func ToolCallFromIRItem(item Item) protocoltypes.ToolCall {
	args := strings.TrimSpace(item.ToolArguments)
	if args == "" {
		args = "{}"
	}

	parsed := ToolArgumentsMap(item)

	return protocoltypes.ToolCall{
		ID:               item.ToolCallID,
		Type:             "function",
		Name:             item.ToolName,
		Arguments:        parsed,
		ThoughtSignature: item.ToolThoughtSignature,
		Function: &protocoltypes.FunctionCall{
			Name:             item.ToolName,
			Arguments:        args,
			ThoughtSignature: item.ToolThoughtSignature,
		},
	}
}

func ToolArgumentsMap(item Item) map[string]any {
	if item.ToolInput != nil {
		return cloneMap(item.ToolInput)
	}
	args := strings.TrimSpace(item.ToolArguments)
	if args == "" {
		return map[string]any{}
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(args), &parsed); err != nil || parsed == nil {
		return map[string]any{}
	}
	return parsed
}

func ContentPartsForMessage(msg protocoltypes.Message) []Part {
	if len(msg.Parts) > 0 {
		return cloneParts(msg.Parts)
	}

	parts := make([]Part, 0, 1+len(msg.Media)+len(msg.Attachments))
	if msg.Content != "" {
		parts = append(parts, Part{Type: string(PartTypeText), Text: msg.Content})
	}
	for _, mediaURI := range msg.Media {
		parts = append(parts, PartFromURI(mediaURI, "", ""))
	}
	for _, attachment := range msg.Attachments {
		parts = append(parts, PartFromAttachment(attachment))
	}
	return parts
}

func TextFromParts(parts []Part) string {
	var text []string
	for _, part := range parts {
		if normalizePartType(part.Type) == string(PartTypeText) && part.Text != "" {
			text = append(text, part.Text)
		}
	}
	return strings.Join(text, "")
}

func ReadableParts(parts []Part) string {
	var out []string
	for _, part := range parts {
		switch normalizePartType(part.Type) {
		case string(PartTypeText):
			if part.Text != "" {
				out = append(out, part.Text)
			}
		case string(PartTypeImage):
			out = append(out, readableReference("image", part))
		case string(PartTypeAudio):
			out = append(out, readableReference("audio", part))
		case string(PartTypeFile):
			out = append(out, readableReference("file", part))
		default:
			if part.Text != "" {
				out = append(out, part.Text)
			} else if part.URI != "" || part.Filename != "" {
				out = append(out, readableReference("part", part))
			}
		}
	}
	return strings.Join(out, "\n")
}

func PartFromURI(uri, mimeType, filename string) Part {
	if mimeType == "" {
		mimeType = mimeTypeFromURI(uri)
	}
	partType := string(PartTypeFile)
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		partType = string(PartTypeImage)
	case strings.HasPrefix(mimeType, "audio/"):
		partType = string(PartTypeAudio)
	case strings.HasPrefix(mimeType, "text/") || strings.Contains(mimeType, "pdf"):
		partType = string(PartTypeFile)
	case looksLikeImageURI(uri):
		partType = string(PartTypeImage)
	}
	return Part{
		Type:     partType,
		URI:      uri,
		MIMEType: mimeType,
		Filename: filename,
	}
}

func PartFromAttachment(attachment protocoltypes.Attachment) Part {
	uri := attachment.URL
	if uri == "" {
		uri = attachment.Ref
	}
	part := PartFromURI(uri, attachment.ContentType, attachment.Filename)
	if attachment.Type != "" {
		switch strings.ToLower(strings.TrimSpace(attachment.Type)) {
		case string(PartTypeText), string(PartTypeImage), string(PartTypeAudio), string(PartTypeFile):
			part.Type = strings.ToLower(strings.TrimSpace(attachment.Type))
		}
	}
	return part
}

func IsStableInstruction(item Item) bool {
	return item.Type == ItemTypeContext &&
		(item.Role == RoleSystem || item.Role == RoleDeveloper || item.Role == "") &&
		item.Scope == ScopeStableInstruction
}

func ContextLabel(item Item) string {
	scope := strings.TrimSpace(string(item.Scope))
	source := strings.TrimSpace(string(item.Source))
	switch {
	case scope != "" && source != "":
		return fmt.Sprintf("context:%s source:%s", scope, source)
	case scope != "":
		return "context:" + scope
	case source != "":
		return "context source:" + source
	default:
		return "context"
	}
}

func itemsFromMessage(msg protocoltypes.Message) []Item {
	role := normalizeRole(msg.Role)

	if role == RoleSystem && len(msg.SystemParts) > 0 {
		items := make([]Item, 0, len(msg.SystemParts))
		for _, block := range msg.SystemParts {
			if block.Text == "" {
				continue
			}
			items = append(items, itemFromSystemPart(block))
		}
		if len(items) > 0 {
			return items
		}
	}

	if msg.ReasoningContent != "" {
		items := nonReasoningItemsFromMessage(msg, role)
		return append(items, Item{
			Type:  ItemTypeReasoning,
			Role:  RoleAssistant,
			Parts: []Part{{Type: string(PartTypeText), Text: msg.ReasoningContent}},
			Scope: ScopeHistory,
		})
	}

	return nonReasoningItemsFromMessage(msg, role)
}

func nonReasoningItemsFromMessage(msg protocoltypes.Message, role Role) []Item {
	if msg.ToolCallID != "" && (role == RoleTool || role == RoleUser) {
		parts := ContentPartsForMessage(msg)
		return []Item{{
			Type:       ItemTypeToolResult,
			Role:       RoleTool,
			Parts:      cloneParts(parts),
			ToolCallID: msg.ToolCallID,
			ToolOutput: cloneParts(parts),
			Scope:      ScopeHistory,
		}}
	}

	var items []Item
	parts := ContentPartsForMessage(msg)
	if len(parts) > 0 || (len(msg.ToolCalls) == 0 && role != "") {
		itemType := ItemTypeMessage
		scope := ScopeHistory
		cache := CacheNone
		if role == RoleSystem || role == RoleDeveloper {
			itemType = ItemTypeContext
			scope = scopeFromPromptMetadata(msg.PromptSlot, msg.PromptSource)
			cache = cacheFromScope(scope)
		}
		items = append(items, Item{
			Type:   itemType,
			Role:   role,
			Parts:  parts,
			Scope:  scope,
			Cache:  cache,
			Source: PromptSource(msg.PromptSource),
		})
	}

	validToolCalls := 0
	for _, tc := range msg.ToolCalls {
		item, ok := itemFromToolCall(tc)
		if ok {
			items = append(items, item)
			validToolCalls++
		}
	}
	if role == RoleAssistant && len(parts) == 0 && len(msg.ToolCalls) > 0 && validToolCalls == 0 {
		items = append(items, Item{
			Type:  ItemTypeMessage,
			Role:  RoleAssistant,
			Scope: ScopeHistory,
		})
	}

	return items
}

func itemFromSystemPart(block protocoltypes.ContentBlock) Item {
	scope := scopeFromPromptMetadata(block.PromptSlot, block.PromptSource)
	return Item{
		Type:   ItemTypeContext,
		Role:   RoleSystem,
		Parts:  []Part{{Type: string(PartTypeText), Text: block.Text}},
		Scope:  scope,
		Cache:  cacheFromContentBlock(block, scope),
		Source: PromptSource(block.PromptSource),
	}
}

func itemFromToolCall(tc protocoltypes.ToolCall) (Item, bool) {
	name := tc.Name
	args := "{}"
	if tc.Function != nil {
		if name == "" {
			name = tc.Function.Name
		}
		if tc.Function.Arguments != "" {
			args = tc.Function.Arguments
		}
	}
	if len(tc.Arguments) > 0 {
		if encoded, err := json.Marshal(tc.Arguments); err == nil {
			args = string(encoded)
		}
	}
	if strings.TrimSpace(tc.ID) == "" || strings.TrimSpace(name) == "" {
		return Item{}, false
	}
	thoughtSignature := tc.ThoughtSignature
	if tc.Function != nil && tc.Function.ThoughtSignature != "" {
		thoughtSignature = tc.Function.ThoughtSignature
	}
	if thoughtSignature == "" && tc.ExtraContent != nil && tc.ExtraContent.Google != nil {
		thoughtSignature = tc.ExtraContent.Google.ThoughtSignature
	}
	return Item{
		Type:                 ItemTypeToolCall,
		Role:                 RoleAssistant,
		ToolCallID:           tc.ID,
		ToolName:             name,
		ToolArguments:        args,
		ToolInput:            cloneMap(tc.Arguments),
		ToolThoughtSignature: thoughtSignature,
		Scope:                ScopeHistory,
	}, true
}

func messageFromParts(role string, parts []Part, mutate func(*protocoltypes.Message)) protocoltypes.Message {
	msg := protocoltypes.Message{Role: role}
	if len(parts) > 0 {
		msg.Content = TextFromParts(parts)
		if msg.Content == "" {
			msg.Content = ReadableParts(parts)
		}
		if shouldPreserveParts(parts) {
			msg.Parts = cloneParts(parts)
		}
		for _, part := range parts {
			switch normalizePartType(part.Type) {
			case string(PartTypeImage), string(PartTypeAudio), string(PartTypeFile):
				if part.URI != "" {
					msg.Media = append(msg.Media, part.URI)
				}
			}
		}
	}
	if mutate != nil {
		mutate(&msg)
	}
	return msg
}

func shouldPreserveParts(parts []Part) bool {
	if len(parts) != 1 {
		return len(parts) > 0
	}
	part := parts[0]
	if normalizePartType(part.Type) != string(PartTypeText) {
		return true
	}
	return part.URI != "" || part.MIMEType != "" || part.Filename != "" || part.Detail != ""
}

func contentBlockFromIRItem(item Item) protocoltypes.ContentBlock {
	block := protocoltypes.ContentBlock{
		Type:         "text",
		Text:         TextFromParts(item.Parts),
		PromptSource: string(item.Source),
	}
	if block.Text == "" {
		block.Text = ReadableParts(item.Parts)
	}
	if item.Cache == CacheEphemeral {
		block.CacheControl = &protocoltypes.CacheControl{Type: "ephemeral"}
	}
	return block
}

func cacheFromContentBlock(block protocoltypes.ContentBlock, scope ContextScope) CacheHint {
	if block.CacheControl != nil && block.CacheControl.Type == "ephemeral" {
		return CacheEphemeral
	}
	return cacheFromScope(scope)
}

func cacheFromScope(scope ContextScope) CacheHint {
	if scope == ScopeStableInstruction {
		return CacheStable
	}
	return CacheNone
}

func scopeFromPromptMetadata(slot, source string) ContextScope {
	switch {
	case source == "context.summary" || slot == "summary":
		return ScopeSummary
	case source == "runtime.context" || slot == "runtime":
		return ScopeRuntime
	default:
		return ScopeStableInstruction
	}
}

func normalizeRole(role string) Role {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case string(RoleSystem):
		return RoleSystem
	case string(RoleDeveloper):
		return RoleDeveloper
	case string(RoleAssistant):
		return RoleAssistant
	case string(RoleTool):
		return RoleTool
	default:
		return RoleUser
	}
}

func cloneParts(parts []Part) []Part {
	if len(parts) == 0 {
		return nil
	}
	return append([]Part(nil), parts...)
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	out := make(map[string]any, len(input))
	for k, v := range input {
		out[k] = v
	}
	return out
}

func normalizePartType(partType string) string {
	partType = strings.ToLower(strings.TrimSpace(partType))
	switch partType {
	case "input_text":
		return string(PartTypeText)
	case "input_image", "image_url":
		return string(PartTypeImage)
	case "input_audio":
		return string(PartTypeAudio)
	default:
		return partType
	}
}

func appendWithSeparator(a, b, sep string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + sep + b
}

func readableReference(kind string, part Part) string {
	target := part.Filename
	if target == "" {
		target = part.URI
	}
	if target == "" {
		target = "inline"
	}

	var attrs []string
	if part.MIMEType != "" {
		attrs = append(attrs, part.MIMEType)
	}
	if part.Detail != "" {
		attrs = append(attrs, "detail="+part.Detail)
	}
	if len(attrs) > 0 {
		return fmt.Sprintf("[%s: %s (%s)]", kind, target, strings.Join(attrs, ", "))
	}
	return fmt.Sprintf("[%s: %s]", kind, target)
}

func mimeTypeFromURI(uri string) string {
	if strings.HasPrefix(uri, "data:") {
		header := strings.TrimPrefix(uri, "data:")
		if before, _, ok := strings.Cut(header, ";"); ok {
			return before
		}
		if before, _, ok := strings.Cut(header, ","); ok {
			return before
		}
	}

	switch strings.ToLower(filepath.Ext(strings.Split(uri, "?")[0])) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".ogg":
		return "audio/ogg"
	case ".pdf":
		return "application/pdf"
	default:
		return ""
	}
}

func looksLikeImageURI(uri string) bool {
	ext := strings.ToLower(filepath.Ext(strings.Split(uri, "?")[0]))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp":
		return true
	default:
		return false
	}
}
