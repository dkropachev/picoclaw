package anthropicprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

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
	defaultBaseURL      = "https://api.anthropic.com"
	anthropicBetaHeader = "oauth-2025-04-20"
)

type Provider struct {
	client      *anthropic.Client
	tokenSource func() (string, error)
	baseURL     string
}

// SupportsThinking implements providers.ThinkingCapable.
func (p *Provider) SupportsThinking() bool { return true }

func NewProvider(token string) *Provider {
	return NewProviderWithBaseURL(token, "")
}

func NewProviderWithBaseURL(token, apiBase string) *Provider {
	baseURL := common.NormalizeBaseURL(apiBase, defaultBaseURL, false)
	client := anthropic.NewClient(
		option.WithAuthToken(token),
		option.WithBaseURL(baseURL),
	)
	return &Provider{
		client:  &client,
		baseURL: baseURL,
	}
}

func NewProviderWithClient(client *anthropic.Client) *Provider {
	return &Provider{
		client:  client,
		baseURL: defaultBaseURL,
	}
}

func NewProviderWithTokenSource(token string, tokenSource func() (string, error)) *Provider {
	return NewProviderWithTokenSourceAndBaseURL(token, tokenSource, "")
}

func NewProviderWithTokenSourceAndBaseURL(token string, tokenSource func() (string, error), apiBase string) *Provider {
	p := NewProviderWithBaseURL(token, apiBase)
	p.tokenSource = tokenSource
	return p
}

func (p *Provider) Chat(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
) (*LLMResponse, error) {
	var opts []option.RequestOption
	if p.tokenSource != nil {
		tok, err := p.tokenSource()
		if err != nil {
			return nil, fmt.Errorf("refreshing token: %w", err)
		}
		opts = append(opts,
			option.WithAuthToken(tok),
			option.WithHeader("anthropic-beta", anthropicBetaHeader),
		)
	}

	params, err := buildParams(messages, tools, model, options)
	if err != nil {
		return nil, err
	}

	// OAuth/setup-tokens require streaming; API keys use non-streaming.
	if p.tokenSource != nil {
		return p.chatStreaming(ctx, params, opts)
	}

	resp, err := p.client.Messages.New(ctx, params, opts...)
	if err != nil {
		return nil, fmt.Errorf("claude API call: %w", err)
	}

	return parseResponse(resp), nil
}

func (p *Provider) chatStreaming(
	ctx context.Context,
	params anthropic.MessageNewParams,
	opts []option.RequestOption,
) (*LLMResponse, error) {
	stream := p.client.Messages.NewStreaming(ctx, params, opts...)
	defer stream.Close()

	var msg anthropic.Message
	for stream.Next() {
		event := stream.Current()
		if err := msg.Accumulate(event); err != nil {
			return nil, fmt.Errorf("claude streaming accumulate: %w", err)
		}
	}
	if err := stream.Err(); err != nil {
		return nil, fmt.Errorf("claude API call: %w", err)
	}

	return parseResponse(&msg), nil
}

func (p *Provider) GetDefaultModel() string {
	return "claude-sonnet-4.6"
}

func (p *Provider) BaseURL() string {
	return p.baseURL
}

func buildParams(
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
) (anthropic.MessageNewParams, error) {
	var system []anthropic.TextBlockParam
	var anthropicMessages []anthropic.MessageParam
	var pendingRole anthropic.MessageParamRole
	var pendingBlocks []anthropic.ContentBlockParamUnion

	flush := func() {
		if len(pendingBlocks) == 0 {
			return
		}
		switch pendingRole {
		case anthropic.MessageParamRoleAssistant:
			anthropicMessages = append(anthropicMessages, anthropic.NewAssistantMessage(pendingBlocks...))
		default:
			anthropicMessages = append(anthropicMessages, anthropic.NewUserMessage(pendingBlocks...))
		}
		pendingRole = ""
		pendingBlocks = nil
	}

	appendBlocks := func(role anthropic.MessageParamRole, blocks ...anthropic.ContentBlockParamUnion) {
		if len(blocks) == 0 {
			return
		}
		if pendingRole != "" && pendingRole != role {
			flush()
		}
		pendingRole = role
		pendingBlocks = append(pendingBlocks, blocks...)
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
				flush()
				block := anthropic.TextBlockParam{Text: text}
				if item.Cache == promptir.CacheEphemeral {
					block.CacheControl = anthropic.NewCacheControlEphemeralParam()
				}
				system = append(system, block)
				continue
			}
			appendBlocks(
				anthropic.MessageParamRoleUser,
				anthropic.NewTextBlock("["+promptir.ContextLabel(item)+"]\n"+text),
			)

		case promptir.ItemTypeMessage:
			blocks := anthropicBlocksFromParts(item.Parts)
			switch item.Role {
			case promptir.RoleAssistant:
				if len(blocks) == 0 {
					flush()
					anthropicMessages = append(anthropicMessages, anthropic.NewAssistantMessage())
				} else {
					appendBlocks(anthropic.MessageParamRoleAssistant, blocks...)
				}
			default:
				appendBlocks(anthropic.MessageParamRoleUser, blocks...)
			}

		case promptir.ItemTypeToolCall:
			if item.ToolCallID == "" || item.ToolName == "" {
				continue
			}
			appendBlocks(
				anthropic.MessageParamRoleAssistant,
				anthropic.NewToolUseBlock(item.ToolCallID, promptir.ToolArgumentsMap(item), item.ToolName),
			)

		case promptir.ItemTypeToolResult:
			output := promptir.ReadableParts(item.ToolOutput)
			if output == "" {
				output = promptir.ReadableParts(item.Parts)
			}
			appendBlocks(
				anthropic.MessageParamRoleUser,
				anthropic.NewToolResultBlock(item.ToolCallID, output, false),
			)

		case promptir.ItemTypeReasoning:
			if text := promptir.ReadableParts(item.Parts); text != "" {
				appendBlocks(anthropic.MessageParamRoleAssistant, anthropic.NewTextBlock("[reasoning]\n"+text))
			}
		}
	}
	flush()

	maxTokens := int64(4096)
	if mt, ok := options["max_tokens"].(int); ok {
		maxTokens = int64(mt)
	}

	// Normalize model ID: Anthropic API uses hyphens (claude-sonnet-4-6),
	// but config may use dots (claude-sonnet-4.6).
	apiModel := strings.ReplaceAll(model, ".", "-")

	params := anthropic.MessageNewParams{
		Model:     apiModel,
		Messages:  anthropicMessages,
		MaxTokens: maxTokens,
	}

	if len(system) > 0 {
		params.System = system
	}

	if temp, ok := options["temperature"].(float64); ok {
		params.Temperature = anthropic.Float(temp)
	}

	if len(tools) > 0 {
		params.Tools = translateTools(tools)
	}

	// Extended Thinking / Adaptive Thinking
	// The thinking_level value directly determines the API parameter format:
	//   "adaptive" → {thinking: {type: "adaptive"}} + output_config.effort
	//   "low/medium/high/xhigh" → {thinking: {type: "enabled", budget_tokens: N}}
	if level, ok := options["thinking_level"].(string); ok && level != "" && level != "off" {
		applyThinkingConfig(&params, level)
	}

	return params, nil
}

func anthropicBlocksFromParts(parts []promptir.Part) []anthropic.ContentBlockParamUnion {
	blocks := make([]anthropic.ContentBlockParamUnion, 0, len(parts))
	for _, part := range parts {
		if part.Type == string(promptir.PartTypeText) || part.Type == "" {
			if part.Text != "" {
				blocks = append(blocks, anthropic.NewTextBlock(part.Text))
			}
			continue
		}
		if text := promptir.ReadableParts([]promptir.Part{part}); text != "" {
			blocks = append(blocks, anthropic.NewTextBlock(text))
		}
	}
	return blocks
}

// applyThinkingConfig sets thinking parameters based on the level value.
// "adaptive" uses the adaptive thinking API (Claude 4.6+).
// All other levels use budget_tokens which is universally supported.
//
// Anthropic API constraint: temperature must not be set when thinking is enabled.
// budget_tokens must be strictly less than max_tokens.
func applyThinkingConfig(params *anthropic.MessageNewParams, level string) {
	// Anthropic API rejects requests with temperature set alongside thinking.
	// Reset to zero value (omitted from JSON serialization).
	if params.Temperature.Valid() {
		log.Printf("anthropic: temperature cleared because thinking is enabled (level=%s)", level)
	}
	params.Temperature = anthropic.MessageNewParams{}.Temperature

	if level == "adaptive" {
		adaptive := anthropic.ThinkingConfigAdaptiveParam{
			Display: anthropic.ThinkingConfigAdaptiveDisplaySummarized,
		}
		params.Thinking = anthropic.ThinkingConfigParamUnion{OfAdaptive: &adaptive}
		params.OutputConfig = anthropic.OutputConfigParam{
			Effort: anthropic.OutputConfigEffortHigh,
		}
		return
	}

	budget := int64(levelToBudget(level))
	if budget <= 0 {
		return
	}

	// budget_tokens must be < max_tokens; clamp to respect user's max_tokens setting.
	if budget >= params.MaxTokens {
		log.Printf("anthropic: budget_tokens (%d) clamped to %d (max_tokens-1)", budget, params.MaxTokens-1)
		budget = params.MaxTokens - 1
	} else if budget > params.MaxTokens*80/100 {
		log.Printf("anthropic: thinking budget (%d) exceeds 80%% of max_tokens (%d), output may be truncated",
			budget, params.MaxTokens)
	}
	params.Thinking = anthropic.ThinkingConfigParamOfEnabled(budget)
}

// levelToBudget maps a thinking level to budget_tokens.
// Values are based on Anthropic's recommendations and community best practices:
//
//	low    =  4,096  — simple reasoning, quick debugging (Claude Code "think")
//	medium = 16,384  — Anthropic recommended sweet spot for most tasks
//	high   = 32,000  — complex architecture, deep analysis (diminishing returns above this)
//	xhigh  = 64,000  — extreme reasoning, research problems, benchmarks
//
// Note: For Claude 4.6+, prefer adaptive thinking over manual budget_tokens.
func levelToBudget(level string) int {
	switch level {
	case "low":
		return 4096
	case "medium":
		return 16384
	case "high":
		return 32000
	case "xhigh":
		return 64000
	default:
		return 0
	}
}

func translateTools(tools []ToolDefinition) []anthropic.ToolUnionParam {
	result := make([]anthropic.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		tool := anthropic.ToolParam{
			Name: t.Function.Name,
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: t.Function.Parameters["properties"],
			},
		}
		if desc := t.Function.Description; desc != "" {
			tool.Description = anthropic.String(desc)
		}
		if req, ok := t.Function.Parameters["required"].([]any); ok {
			required := make([]string, 0, len(req))
			for _, r := range req {
				if s, ok := r.(string); ok {
					required = append(required, s)
				}
			}
			tool.InputSchema.Required = required
		}
		result = append(result, anthropic.ToolUnionParam{OfTool: &tool})
	}
	return result
}

func parseResponse(resp *anthropic.Message) *LLMResponse {
	var content strings.Builder
	var reasoning strings.Builder
	var toolCalls []ToolCall

	for _, block := range resp.Content {
		switch block.Type {
		case "thinking":
			tb := block.AsThinking()
			reasoning.WriteString(tb.Thinking)
		case "text":
			tb := block.AsText()
			content.WriteString(tb.Text)
		case "tool_use":
			tu := block.AsToolUse()
			var args map[string]any
			if err := json.Unmarshal(tu.Input, &args); err != nil {
				log.Printf("anthropic: failed to decode tool call input for %q: %v", tu.Name, err)
				args = map[string]any{"raw": string(tu.Input)}
			}
			toolCalls = append(toolCalls, ToolCall{
				ID:        tu.ID,
				Name:      tu.Name,
				Arguments: args,
			})
		}
	}

	finishReason := "stop"
	switch resp.StopReason {
	case anthropic.StopReasonToolUse:
		finishReason = "tool_calls"
	case anthropic.StopReasonMaxTokens:
		finishReason = "length"
	case anthropic.StopReasonEndTurn:
		finishReason = "stop"
	}

	return &LLMResponse{
		Content:      content.String(),
		Reasoning:    reasoning.String(),
		ToolCalls:    toolCalls,
		FinishReason: finishReason,
		Usage: &UsageInfo{
			PromptTokens:     int(resp.Usage.InputTokens),
			CompletionTokens: int(resp.Usage.OutputTokens),
			TotalTokens:      int(resp.Usage.InputTokens + resp.Usage.OutputTokens),
		},
	}
}
