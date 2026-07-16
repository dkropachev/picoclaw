// Package openai_responses_common provides shared utilities for providers
// that use the OpenAI Responses API (e.g., Azure, Codex).
package openai_responses_common

import (
	"encoding/json"
	"io"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"

	"github.com/sipeed/picoclaw/pkg/providers/common"
	"github.com/sipeed/picoclaw/pkg/providers/promptir"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

// TranslateMessages converts internal Message entries to the OpenAI Responses API
// input format. System messages are extracted as instructions (returned separately),
// user/assistant/tool messages become ResponseInputItemUnionParam entries.
// Supports multipart media (images, audio).
func TranslateMessages(messages []protocoltypes.Message) (input responses.ResponseInputParam, instructions string) {
	return TranslatePrompt(promptir.FromMessages(messages))
}

// TranslatePrompt converts Prompt IR to OpenAI Responses API input format.
func TranslatePrompt(prompt promptir.Prompt) (input responses.ResponseInputParam, instructions string) {
	input = make(responses.ResponseInputParam, 0, len(prompt.Items))
	instructionParts := make([]string, 0)

	for _, item := range prompt.Items {
		if promptir.IsStableInstruction(item) {
			if text := promptir.ReadableParts(item.Parts); text != "" {
				instructionParts = append(instructionParts, text)
			}
			continue
		}

		switch item.Type {
		case promptir.ItemTypeContext:
			text := promptir.ReadableParts(item.Parts)
			if text == "" {
				continue
			}
			input = appendEasyMessage(input, responses.EasyInputMessageRoleUser,
				"["+promptir.ContextLabel(item)+"]\n"+text)

		case promptir.ItemTypeMessage:
			switch item.Role {
			case promptir.RoleUser:
				if hasNativeResponseParts(item.Parts) {
					input = append(input, responses.ResponseInputItemUnionParam{
						OfInputMessage: &responses.ResponseInputItemMessageParam{
							Role:    "user",
							Content: BuildMultipartContentFromParts(item.Parts),
						},
					})
				} else {
					input = appendEasyMessage(
						input,
						responses.EasyInputMessageRoleUser,
						promptir.ReadableParts(item.Parts),
					)
				}
			case promptir.RoleAssistant:
				input = appendEasyMessage(
					input,
					responses.EasyInputMessageRoleAssistant,
					promptir.ReadableParts(item.Parts),
				)
			default:
				text := promptir.ReadableParts(item.Parts)
				if text != "" {
					input = appendEasyMessage(input, responses.EasyInputMessageRoleUser,
						"["+string(item.Role)+"]\n"+text)
				}
			}

		case promptir.ItemTypeToolCall:
			if item.ToolCallID == "" || item.ToolName == "" {
				continue
			}
			args := item.ToolArguments
			if strings.TrimSpace(args) == "" {
				args = "{}"
			}
			input = append(input, responses.ResponseInputItemUnionParam{
				OfFunctionCall: &responses.ResponseFunctionToolCallParam{
					CallID:    item.ToolCallID,
					Name:      item.ToolName,
					Arguments: args,
				},
			})

		case promptir.ItemTypeToolResult:
			output := promptir.ReadableParts(item.ToolOutput)
			if output == "" {
				output = promptir.ReadableParts(item.Parts)
			}
			input = append(input, responses.ResponseInputItemUnionParam{
				OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
					CallID: item.ToolCallID,
					Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{
						OfString: openai.Opt(output),
					},
				},
			})

		case promptir.ItemTypeReasoning:
			if text := promptir.ReadableParts(item.Parts); text != "" {
				input = appendEasyMessage(input, responses.EasyInputMessageRoleAssistant, "[reasoning]\n"+text)
			}
		}
	}

	instructions = strings.Join(instructionParts, "\n\n")
	return input, instructions
}

// BuildMultipartContent constructs a ResponseInputMessageContentListParam from
// text content and media URLs (data:image/... and data:audio/... URIs).
func BuildMultipartContent(text string, media []string) responses.ResponseInputMessageContentListParam {
	parts := make([]promptir.Part, 0, 1+len(media))
	if text != "" {
		parts = append(parts, promptir.Part{Type: string(promptir.PartTypeText), Text: text})
	}
	for _, mediaURL := range media {
		parts = append(parts, promptir.PartFromURI(mediaURL, "", ""))
	}
	return BuildMultipartContentFromParts(parts)
}

func BuildMultipartContentFromParts(parts []promptir.Part) responses.ResponseInputMessageContentListParam {
	content := make(responses.ResponseInputMessageContentListParam, 0, len(parts))

	for _, part := range parts {
		switch part.Type {
		case string(promptir.PartTypeText), "":
			if part.Text == "" {
				continue
			}
			content = append(content, responses.ResponseInputContentUnionParam{
				OfInputText: &responses.ResponseInputTextParam{
					Text: part.Text,
				},
			})
		case string(promptir.PartTypeImage):
			if part.URI == "" {
				continue
			}
			content = append(content, responses.ResponseInputContentUnionParam{
				OfInputImage: &responses.ResponseInputImageParam{
					ImageURL: openai.Opt(part.URI),
					Detail:   responses.ResponseInputImageDetailAuto,
				},
			})
		case string(promptir.PartTypeAudio):
			if format, data, ok := common.ParseDataAudioURL(part.URI); ok {
				content = append(content, responses.ResponseInputContentUnionParam{
					OfInputFile: &responses.ResponseInputFileParam{
						FileData: openai.Opt(data),
						Filename: openai.Opt("audio." + format),
					},
				})
			} else if text := promptir.ReadableParts([]promptir.Part{part}); text != "" {
				content = append(content, responses.ResponseInputContentUnionParam{
					OfInputText: &responses.ResponseInputTextParam{Text: text},
				})
			}
		default:
			text := promptir.ReadableParts([]promptir.Part{part})
			if text == "" {
				continue
			}
			content = append(content, responses.ResponseInputContentUnionParam{
				OfInputText: &responses.ResponseInputTextParam{Text: text},
			})
		}
	}

	if len(content) == 0 {
		content = append(content, responses.ResponseInputContentUnionParam{
			OfInputText: &responses.ResponseInputTextParam{
				Text: "",
			},
		})
	}

	return content
}

func appendEasyMessage(
	input responses.ResponseInputParam,
	role responses.EasyInputMessageRole,
	text string,
) responses.ResponseInputParam {
	return append(input, responses.ResponseInputItemUnionParam{
		OfMessage: &responses.EasyInputMessageParam{
			Role:    role,
			Content: responses.EasyInputMessageContentUnionParam{OfString: openai.Opt(text)},
		},
	})
}

func hasNativeResponseParts(parts []promptir.Part) bool {
	for _, part := range parts {
		switch part.Type {
		case string(promptir.PartTypeImage):
			return true
		case string(promptir.PartTypeAudio):
			if _, _, ok := common.ParseDataAudioURL(part.URI); ok {
				return true
			}
		}
	}
	return false
}

// ResolveToolCall extracts the function name and JSON arguments string from a ToolCall.
// Returns ok=false if the tool call has no name or if arguments fail to marshal.
func ResolveToolCall(tc protocoltypes.ToolCall) (name string, arguments string, ok bool) {
	name = tc.Name
	if name == "" && tc.Function != nil {
		name = tc.Function.Name
	}
	if name == "" {
		return "", "", false
	}

	if len(tc.Arguments) > 0 {
		argsJSON, err := json.Marshal(tc.Arguments)
		if err != nil {
			return "", "", false
		}
		return name, string(argsJSON), true
	}

	if tc.Function != nil && tc.Function.Arguments != "" {
		return name, tc.Function.Arguments, true
	}

	return name, "{}", true
}

// TranslateTools converts internal ToolDefinition entries to the OpenAI Responses API
// tool format. If enableWebSearch is true, a web_search tool is appended and any
// user-defined tool named "web_search" is skipped to avoid duplicates.
func TranslateTools(tools []protocoltypes.ToolDefinition, enableWebSearch bool) []responses.ToolUnionParam {
	capHint := len(tools)
	if enableWebSearch {
		capHint++
	}
	result := make([]responses.ToolUnionParam, 0, capHint)

	for _, t := range tools {
		if t.Type != "function" {
			continue
		}
		if enableWebSearch && strings.EqualFold(t.Function.Name, "web_search") {
			continue
		}
		ft := responses.FunctionToolParam{
			Name:       t.Function.Name,
			Parameters: t.Function.Parameters,
			Strict:     openai.Opt(false),
		}
		if t.Function.Description != "" {
			ft.Description = openai.Opt(t.Function.Description)
		}
		result = append(result, responses.ToolUnionParam{OfFunction: &ft})
	}

	if enableWebSearch {
		result = append(result, responses.ToolParamOfWebSearch(responses.WebSearchToolTypeWebSearch))
	}

	return result
}

// ParseResponseBody parses an OpenAI Responses API JSON body into an LLMResponse.
// Handles output item types: "message" (output_text + refusal), "function_call", and "reasoning".
func ParseResponseBody(body io.Reader) (*protocoltypes.LLMResponse, error) {
	var apiResp responses.Response
	if err := json.NewDecoder(body).Decode(&apiResp); err != nil {
		return nil, err
	}

	return parseResponse(&apiResp), nil
}

// ParseResponseFromStruct converts a decoded responses.Response into an LLMResponse.
// Used by providers that receive the Response struct directly (e.g., via streaming SDK).
func ParseResponseFromStruct(resp *responses.Response) *protocoltypes.LLMResponse {
	return parseResponse(resp)
}

// parseResponse is the shared implementation for extracting LLMResponse fields
// from a decoded responses.Response.
func parseResponse(apiResp *responses.Response) *protocoltypes.LLMResponse {
	var content strings.Builder
	var reasoningContent strings.Builder
	var toolCalls []protocoltypes.ToolCall

	for _, item := range apiResp.Output {
		switch item.Type {
		case "message":
			for _, c := range item.Content {
				switch c.Type {
				case "output_text":
					content.WriteString(c.Text)
				case "refusal":
					content.WriteString(c.Refusal)
				}
			}
		case "function_call":
			var args map[string]any
			if err := json.Unmarshal([]byte(item.Arguments), &args); err != nil {
				args = map[string]any{"raw": item.Arguments}
			}
			toolCalls = append(toolCalls, protocoltypes.ToolCall{
				ID:        item.CallID,
				Name:      item.Name,
				Arguments: args,
			})
		case "reasoning":
			for _, s := range item.Summary {
				reasoningContent.WriteString(s.Text)
			}
		}
	}

	finishReason := "stop"
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
	}
	switch apiResp.Status {
	case responses.ResponseStatusIncomplete:
		finishReason = "length"
	case responses.ResponseStatusFailed:
		finishReason = "error"
	case responses.ResponseStatusCancelled:
		finishReason = "canceled"
	}

	var usage *protocoltypes.UsageInfo
	if apiResp.Usage.TotalTokens > 0 {
		usage = &protocoltypes.UsageInfo{
			PromptTokens:     int(apiResp.Usage.InputTokens),
			CompletionTokens: int(apiResp.Usage.OutputTokens),
			TotalTokens:      int(apiResp.Usage.TotalTokens),
		}
	}

	return &protocoltypes.LLMResponse{
		Content:          content.String(),
		ReasoningContent: reasoningContent.String(),
		ToolCalls:        toolCalls,
		FinishReason:     finishReason,
		Usage:            usage,
	}
}
