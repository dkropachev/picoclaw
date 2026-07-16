// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package anthropicmessages

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers/common"
	"github.com/sipeed/picoclaw/pkg/providers/promptir"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

type (
	ToolCall               = protocoltypes.ToolCall
	FunctionCall           = protocoltypes.FunctionCall
	LLMResponse            = protocoltypes.LLMResponse
	UsageInfo              = protocoltypes.UsageInfo
	Message                = protocoltypes.Message
	ToolDefinition         = protocoltypes.ToolDefinition
	ToolFunctionDefinition = protocoltypes.ToolFunctionDefinition
	PromptPart             = protocoltypes.PromptPart
)

const (
	defaultAPIVersion     = "2023-06-01"
	defaultBaseURL        = "https://api.anthropic.com/v1"
	defaultRequestTimeout = 120 * time.Second
)

// Provider implements Anthropic Messages API via HTTP (without SDK).
// It supports custom endpoints that use Anthropic's native message format.
type Provider struct {
	apiKey     string
	apiBase    string
	httpClient *http.Client
	userAgent  string
}

// NewProvider creates a new Anthropic Messages API provider.
func NewProvider(apiKey, apiBase, userAgent string) *Provider {
	return NewProviderWithTimeout(apiKey, apiBase, userAgent, 0)
}

// NewProviderWithTimeout creates a provider with custom request timeout.
func NewProviderWithTimeout(apiKey, apiBase, userAgent string, timeoutSeconds int) *Provider {
	baseURL := common.NormalizeBaseURL(apiBase, defaultBaseURL, true)
	timeout := defaultRequestTimeout
	if timeoutSeconds > 0 {
		timeout = time.Duration(timeoutSeconds) * time.Second
	}

	return &Provider{
		apiKey:    apiKey,
		apiBase:   baseURL,
		userAgent: userAgent,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// Chat sends messages to the Anthropic Messages API and returns the response.
func (p *Provider) Chat(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
) (*LLMResponse, error) {
	if p.apiKey == "" {
		return nil, fmt.Errorf("API key not configured")
	}

	// Build request body
	requestBody, err := buildRequestBody(messages, tools, model, options)
	if err != nil {
		return nil, fmt.Errorf("building request body: %w", err)
	}

	// Serialize to JSON
	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("serializing request body: %w", err)
	}

	// Build request URL
	endpointURL, err := url.JoinPath(p.apiBase, "messages")
	if err != nil {
		return nil, fmt.Errorf("building endpoint URL: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", endpointURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("creating HTTP request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", p.apiKey) //nolint:canonicalheader // Anthropic API requires exact header name
	req.Header.Set("Anthropic-Version", defaultAPIVersion)
	if p.userAgent != "" {
		req.Header.Set("User-Agent", p.userAgent)
	}

	// Execute request
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing HTTP request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	// Check for HTTP errors with detailed messages
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return nil, fmt.Errorf("authentication failed (401): check your API key")
	case http.StatusTooManyRequests:
		return nil, fmt.Errorf("rate limited (429): %s", string(body))
	case http.StatusBadRequest:
		return nil, fmt.Errorf("bad request (400): %s", string(body))
	case http.StatusNotFound:
		return nil, fmt.Errorf("endpoint not found (404): %s", string(body))
	case http.StatusInternalServerError:
		return nil, fmt.Errorf("internal server error (500): %s", string(body))
	case http.StatusServiceUnavailable:
		return nil, fmt.Errorf("service unavailable (503): %s", string(body))
	default:
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
		}
	}

	// Parse response
	return parseResponseBody(body)
}

// GetDefaultModel returns the default model for this provider.
func (p *Provider) GetDefaultModel() string {
	return "claude-sonnet-4.6"
}

// buildRequestBody converts internal message format to Anthropic Messages API format.
func buildRequestBody(
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
) (map[string]any, error) {
	// max_tokens is required and guaranteed by agent loop
	maxTokens, ok := common.AsInt(options["max_tokens"])
	if !ok {
		return nil, fmt.Errorf("max_tokens is required in options")
	}

	result := map[string]any{
		"model":      model,
		"max_tokens": int64(maxTokens),
		"messages":   []any{},
	}

	// Set temperature from options
	if temp, ok := common.AsFloat(options["temperature"]); ok {
		result["temperature"] = temp
	}

	// Process messages
	var systemParts []string
	var apiMessages []any
	var pendingRole string
	var pendingBlocks []any

	flush := func() {
		if len(pendingBlocks) == 0 {
			return
		}
		content := any(pendingBlocks)
		if pendingRole == "user" {
			if toolResultBlocks, ok := allToolResultBlocks(pendingBlocks); ok {
				content = toolResultBlocks
			}
		}
		apiMessages = append(apiMessages, map[string]any{
			"role":    pendingRole,
			"content": content,
		})
		pendingRole = ""
		pendingBlocks = nil
	}

	appendBlocks := func(role string, blocks ...map[string]any) {
		if len(blocks) == 0 {
			return
		}
		if pendingRole != "" && pendingRole != role {
			flush()
		}
		pendingRole = role
		for _, block := range blocks {
			pendingBlocks = append(pendingBlocks, block)
		}
	}

	appendUserContent := func(parts []promptir.Part) {
		if isSingleTextPart(parts) {
			flush()
			apiMessages = append(apiMessages, map[string]any{
				"role":    "user",
				"content": promptir.TextFromParts(parts),
			})
			return
		}
		appendBlocks("user", anthropicMessageBlocksFromParts(parts)...)
	}

	prompt := promptir.FromMessagesWithTools(messages, tools)
	for _, item := range prompt.Items {
		switch item.Type {
		case promptir.ItemTypeContext:
			text := promptir.ReadableParts(item.Parts)
			if text == "" {
				continue
			}
			if promptir.IsStableInstruction(item) {
				systemParts = append(systemParts, text)
			} else {
				appendBlocks("user", map[string]any{
					"type": "text",
					"text": "[" + promptir.ContextLabel(item) + "]\n" + text,
				})
			}

		case promptir.ItemTypeMessage:
			if item.Role == promptir.RoleAssistant {
				blocks := anthropicMessageBlocksFromParts(item.Parts)
				if len(blocks) == 0 {
					flush()
					apiMessages = append(apiMessages, map[string]any{
						"role":    "assistant",
						"content": []any{},
					})
				} else {
					appendBlocks("assistant", blocks...)
				}
			} else {
				appendUserContent(item.Parts)
			}

		case promptir.ItemTypeToolCall:
			if item.ToolCallID == "" || item.ToolName == "" {
				continue
			}
			appendBlocks("assistant", map[string]any{
				"type":  "tool_use",
				"id":    item.ToolCallID,
				"name":  item.ToolName,
				"input": promptir.ToolArgumentsMap(item),
			})

		case promptir.ItemTypeToolResult:
			output := promptir.ReadableParts(item.ToolOutput)
			if output == "" {
				output = promptir.ReadableParts(item.Parts)
			}
			appendBlocks("user", map[string]any{
				"type":        "tool_result",
				"tool_use_id": item.ToolCallID,
				"content":     output,
			})

		case promptir.ItemTypeReasoning:
			if text := promptir.ReadableParts(item.Parts); text != "" {
				appendBlocks("assistant", map[string]any{
					"type": "text",
					"text": "[reasoning]\n" + text,
				})
			}
		}
	}
	flush()

	result["messages"] = apiMessages

	// Set system prompt if present
	if len(systemParts) > 0 {
		result["system"] = strings.Join(systemParts, "\n\n")
	}

	// Add tools if present
	if len(tools) > 0 {
		result["tools"] = buildTools(tools)
	}

	return result, nil
}

func allToolResultBlocks(blocks []any) ([]map[string]any, bool) {
	if len(blocks) == 0 {
		return nil, false
	}
	out := make([]map[string]any, 0, len(blocks))
	for _, block := range blocks {
		m, ok := block.(map[string]any)
		if !ok || m["type"] != "tool_result" {
			return nil, false
		}
		out = append(out, m)
	}
	return out, true
}

func isSingleTextPart(parts []promptir.Part) bool {
	return len(parts) == 1 &&
		(parts[0].Type == string(promptir.PartTypeText) || parts[0].Type == "") &&
		parts[0].Text != ""
}

func anthropicMessageBlocksFromParts(parts []promptir.Part) []map[string]any {
	blocks := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		if part.Type == string(promptir.PartTypeText) || part.Type == "" {
			if part.Text != "" {
				blocks = append(blocks, map[string]any{
					"type": "text",
					"text": part.Text,
				})
			}
			continue
		}
		if text := promptir.ReadableParts([]promptir.Part{part}); text != "" {
			blocks = append(blocks, map[string]any{
				"type": "text",
				"text": text,
			})
		}
	}
	return blocks
}

// buildTools converts tool definitions to Anthropic format.
func buildTools(tools []ToolDefinition) []any {
	result := make([]any, len(tools))
	for i, tool := range tools {
		toolDef := map[string]any{
			"name":         tool.Function.Name,
			"description":  tool.Function.Description,
			"input_schema": tool.Function.Parameters,
		}
		result[i] = toolDef
	}
	return result
}

// parseResponseBody parses Anthropic Messages API response.
func parseResponseBody(body []byte) (*LLMResponse, error) {
	var resp anthropicMessageResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parsing JSON response: %w", err)
	}

	// Extract content and tool calls
	var content strings.Builder
	toolCalls := make([]ToolCall, 0) // Initialize as empty slice (not nil) for consistent JSON serialization

	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			content.WriteString(block.Text)
		case "tool_use":
			argsJSON, _ := json.Marshal(block.Input)
			toolCalls = append(toolCalls, ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: block.Input,
				Function: &FunctionCall{
					Name:      block.Name,
					Arguments: string(argsJSON),
				},
			})
		}
	}

	// Map stop_reason
	finishReason := "stop"
	switch resp.StopReason {
	case "tool_use":
		finishReason = "tool_calls"
	case "max_tokens":
		finishReason = "length"
	case "end_turn":
		finishReason = "stop"
	case "stop_sequence":
		finishReason = "stop"
	}

	return &LLMResponse{
		Content:      content.String(),
		ToolCalls:    toolCalls,
		FinishReason: finishReason,
		Usage: &UsageInfo{
			PromptTokens:     int(resp.Usage.InputTokens),
			CompletionTokens: int(resp.Usage.OutputTokens),
			TotalTokens:      int(resp.Usage.InputTokens + resp.Usage.OutputTokens),
		},
	}, nil
}

// Anthropic API response structures

type anthropicMessageResponse struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Role       string         `json:"role"`
	Content    []contentBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
	Model      string         `json:"model"`
	Usage      usageInfo      `json:"usage"`
}

type contentBlock struct {
	Type  string         `json:"type"`
	Text  string         `json:"text,omitempty"`
	ID    string         `json:"id,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`
}

type usageInfo struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}
