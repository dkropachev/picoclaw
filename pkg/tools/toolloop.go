// PicoClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"sync"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/utils"
)

// ToolLoopConfig configures the tool execution loop.
type ToolLoopConfig struct {
	Provider      providers.LLMProvider
	Model         string
	Tools         *ToolRegistry
	MaxIterations int
	LLMOptions    map[string]any
	// SequentialToolCalls executes one model-authored tool call at a time in
	// response order. The default remains parallel execution for existing
	// read-oriented callers; mutation-capable controllers should enable this so
	// one response cannot race two writes against the same state.
	SequentialToolCalls bool
	// SuppressToolArguments keeps model-authored arguments and result-derived
	// error details out of loop, registry, and suppression-aware tool logs. Tool
	// names, counts, and timings remain observable.
	SuppressToolArguments bool

	// MediaResolver resolves media:// refs in messages before each LLM call.
	// This is optional and is mainly used by subagent legacy fallback execution
	// so subagents can reuse the same multimodal media handling as the main loop.
	MediaResolver func(messages []providers.Message) []providers.Message
}

// ToolLoopResult contains the result of running the tool loop.
type ToolLoopResult struct {
	Content    string
	Iterations int
}

// RunToolLoop executes the LLM + tool call iteration loop.
// This is the core agent logic that can be reused by both main agent and subagents.
func RunToolLoop(
	ctx context.Context,
	config ToolLoopConfig,
	messages []providers.Message,
	channel, chatID string,
) (*ToolLoopResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	iteration := 0
	var finalContent string

	for iteration < config.MaxIterations {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		iteration++

		logger.DebugCF("toolloop", "LLM iteration",
			map[string]any{
				"iteration": iteration,
				"max":       config.MaxIterations,
			})

		// 1. Build tool definitions
		var providerToolDefs []providers.ToolDefinition
		if config.Tools != nil {
			providerToolDefs = config.Tools.ToProviderDefs()
		}

		// 2. Set default LLM options
		llmOpts := config.LLMOptions
		if llmOpts == nil {
			llmOpts = map[string]any{}
		}

		// 3. Resolve media:// refs and Call LLM.
		// Tools like load_image produce media:// refs in their result messages.
		// Without this step, the LLM would receive raw "media://uuid" strings
		// instead of base64-encoded image data URLs.
		//
		// We build a separate callMessages slice so that:
		//   (a) the resolver output is used for the LLM call only,
		//   (b) the original `messages` slice keeps the unresolved refs for
		//       subsequent iterations — the resolver is idempotent but working
		//       on the original avoids double-encoding issues.
		//
		// On iteration 1 the initial user messages typically have no media://
		// refs (they come from plain text), so this is effectively a no-op;
		// it becomes relevant from iteration 2 onward when tool results may
		// contain media refs.
		callMessages := messages
		if config.MediaResolver != nil && iteration > 1 {
			callMessages = config.MediaResolver(messages)
		}
		response, err := config.Provider.Chat(ctx, callMessages, providerToolDefs, config.Model, llmOpts)
		if err != nil {
			fields := map[string]any{"iteration": iteration}
			if !config.SuppressToolArguments {
				fields["error"] = err.Error()
			}
			logger.ErrorCF("toolloop", "LLM call failed", fields)
			return nil, fmt.Errorf("LLM call failed: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if response == nil {
			return nil, fmt.Errorf("LLM call returned no response")
		}

		// 4. If no tool calls, we're done
		if len(response.ToolCalls) == 0 {
			finalContent = response.Content
			logger.InfoCF("toolloop", "LLM response without tool calls (direct answer)",
				map[string]any{
					"iteration":     iteration,
					"content_chars": len(finalContent),
				})
			break
		}

		normalizedToolCalls := make([]providers.ToolCall, 0, len(response.ToolCalls))
		for _, tc := range response.ToolCalls {
			normalizedToolCalls = append(normalizedToolCalls, providers.NormalizeToolCall(tc))
		}

		// 5. Log tool calls
		toolNames := make([]string, 0, len(normalizedToolCalls))
		for _, tc := range normalizedToolCalls {
			toolNames = append(toolNames, tc.Name)
		}
		logger.InfoCF("toolloop", "LLM requested tool calls",
			map[string]any{
				"tools":     toolNames,
				"count":     len(normalizedToolCalls),
				"iteration": iteration,
			})

		// 6. Build assistant message with tool calls
		assistantMsg := providers.Message{
			Role:             "assistant",
			Content:          response.Content,
			ReasoningContent: response.ReasoningContent,
		}
		for _, tc := range normalizedToolCalls {
			argumentsJSON, err := json.Marshal(tc.Arguments)
			if err != nil {
				logger.Warnf("toolloop: failed to marshal tool call arguments for %s: %v", tc.Name, err)
				argumentsJSON = []byte("{}")
			}
			functionThoughtSignature := tc.ThoughtSignature
			if tc.Function != nil && tc.Function.ThoughtSignature != "" {
				functionThoughtSignature = tc.Function.ThoughtSignature
			}
			assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, providers.ToolCall{
				ID:        tc.ID,
				Type:      "function",
				Name:      tc.Name,
				Arguments: tc.Arguments,
				Function: &providers.FunctionCall{
					Name:             tc.Name,
					Arguments:        string(argumentsJSON),
					ThoughtSignature: functionThoughtSignature,
				},
				ThoughtSignature: tc.ThoughtSignature,
				ExtraContent:     cloneToolLoopExtraContent(tc.ExtraContent),
			})
		}
		messages = append(messages, assistantMsg)

		// 7. Execute tool calls. Most existing callers benefit from parallel
		// execution, while mutation-capable controllers opt into response-order
		// serialization through SequentialToolCalls.
		type indexedResult struct {
			result *ToolResult
			tc     providers.ToolCall
		}

		results := make([]indexedResult, len(normalizedToolCalls))
		execute := func(i int, tc providers.ToolCall) {
			results[i].tc = tc
			defer func() {
				if r := recover(); r != nil {
					fields := map[string]any{"tool": tc.Name}
					if !config.SuppressToolArguments {
						fields["panic"] = fmt.Sprintf("%v", r)
						fields["stack"] = string(debug.Stack())
					}
					logger.ErrorCF("toolloop", "tool execution panic recovered", fields)
					results[i].result = ErrorResult(fmt.Sprintf("internal panic in tool %s", tc.Name))
				}
			}()

			if config.SuppressToolArguments {
				logger.InfoCF("toolloop", fmt.Sprintf("Tool call: %s", tc.Name),
					map[string]any{
						"tool":      tc.Name,
						"iteration": iteration,
					})
			} else {
				argsJSON, _ := json.Marshal(tc.Arguments)
				argsPreview := utils.Truncate(string(argsJSON), 200)
				logger.InfoCF("toolloop", fmt.Sprintf("Tool call: %s(%s)", tc.Name, argsPreview),
					map[string]any{
						"tool":      tc.Name,
						"iteration": iteration,
					})
			}

			if config.Tools != nil {
				results[i].result = config.Tools.executeWithContext(
					ctx,
					tc.Name,
					tc.Arguments,
					channel,
					chatID,
					nil,
					config.SuppressToolArguments,
				)
			} else {
				results[i].result = ErrorResult("No tools available")
			}
			if results[i].result == nil {
				results[i].result = ErrorResult(fmt.Sprintf("tool %s returned no result", tc.Name))
			}
		}

		if config.SequentialToolCalls {
			for i, tc := range normalizedToolCalls {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				execute(i, tc)
			}
		} else {
			var wg sync.WaitGroup
			for i, tc := range normalizedToolCalls {
				wg.Add(1)
				go func(idx int, call providers.ToolCall) {
					defer wg.Done()
					execute(idx, call)
				}(i, tc)
			}
			wg.Wait()
		}

		// Append results in original order
		for _, r := range results {
			contentForLLM := r.result.ContentForLLM()

			toolMsg := providers.Message{
				Role:       "tool",
				Content:    contentForLLM,
				ToolCallID: r.tc.ID,
			}
			if len(r.result.Media) > 0 && !r.result.ResponseHandled {
				toolMsg.Media = append(toolMsg.Media, r.result.Media...)
			}
			messages = append(messages, toolMsg)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return &ToolLoopResult{
		Content:    finalContent,
		Iterations: iteration,
	}, nil
}

func cloneToolLoopExtraContent(value *providers.ExtraContent) *providers.ExtraContent {
	if value == nil {
		return nil
	}
	cloned := &providers.ExtraContent{
		ToolFeedbackExplanation: value.ToolFeedbackExplanation,
	}
	if value.Google != nil {
		cloned.Google = &providers.GoogleExtra{
			ThoughtSignature: value.Google.ThoughtSignature,
		}
	}
	return cloned
}
