// PicoClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

// ToolLoopConfig configures the tool execution loop.
type ToolLoopConfig struct {
	Provider providers.LLMProvider
	Model    string
	Tools    *ToolRegistry
	// Policy is mandatory for model-authored tool dispatch. Nil fails closed
	// when a provider returns a tool call; callers preserving legacy behavior
	// must pass CompatibilityAllowToolPolicy explicitly.
	Policy        ToolPolicy
	PolicySubject ToolPolicySubject
	MaxIterations int
	LLMOptions    map[string]any
	// SequentialToolCalls executes one model-authored tool call at a time in
	// response order. The default remains parallel execution for existing
	// read-oriented callers; mutation-capable controllers should enable this so
	// one response cannot race two writes against the same state.
	SequentialToolCalls bool
	// SuppressToolArguments keeps raw model-authored arguments and result-derived
	// error details out of loop, registry, and suppression-aware tool logs.
	// Bounded observations, names, counts, and timings remain observable. An
	// inherited ToolLogDetailsSuppressed context marker is false-dominating too.
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
	effectiveSuppressed := config.SuppressToolArguments || ToolLogDetailsSuppressed(ctx)
	if effectiveSuppressed {
		ctx = WithToolLogDetailsSuppressed(ctx)
	}
	iteration := 0
	var finalContent string

	for iteration < config.MaxIterations {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		iteration++

		logger.DebugSafeCF(
			logger.ComponentToolLoop,
			logger.DiagnosticMessageLLMIteration,
			logger.NewSafeFields(
				logger.SafeInt(logger.FieldIteration, iteration),
				logger.SafeInt(logger.FieldMaxIterations, config.MaxIterations),
			),
		)

		// 1. Build tool definitions
		var providerToolDefs []providers.ToolDefinition
		var toolCatalog *ModelToolCatalog
		if config.Tools != nil {
			var catalogErr error
			toolCatalog, catalogErr = config.Tools.SnapshotModelToolCatalog()
			if catalogErr != nil {
				return nil, fmt.Errorf("build model tool catalog: %w", catalogErr)
			}
			providerToolDefs = toolCatalog.ProviderDefinitions()
		}
		if err := ValidateOfferedToolDefinitions(providerToolDefs); err != nil {
			return nil, fmt.Errorf("invalid model tool definitions: %w", err)
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
		providerDefinitions := providerToolDefs
		if toolCatalog != nil {
			providerDefinitions = toolCatalog.ProviderDefinitions()
		}
		response, err := config.Provider.Chat(ctx, callMessages, providerDefinitions, config.Model, llmOpts)
		if err != nil {
			logger.ErrorSafeCF(
				logger.ComponentToolLoop,
				logger.DiagnosticMessageLLMCallFailed,
				logger.NewSafeFields(
					logger.SafeInt(logger.FieldIteration, iteration),
					logger.SafeObservation(
						logger.ObservationPrefixError,
						logger.ObserveErrorType(logger.ErrorClassProvider, err),
					),
				),
			)
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
			logger.InfoSafeCF(
				logger.ComponentToolLoop,
				logger.DiagnosticMessageLLMDirectResponse,
				logger.NewSafeFields(
					logger.SafeInt(logger.FieldIteration, iteration),
					logger.SafeInt64(logger.FieldContentBytes, int64(len(finalContent))),
				),
			)
			break
		}

		normalizedToolCalls := make([]providers.ToolCall, 0, len(response.ToolCalls))
		for _, tc := range response.ToolCalls {
			normalizedToolCalls = append(normalizedToolCalls, providers.NormalizeToolCall(tc))
		}
		if err := ValidateModelToolCallBatch(normalizedToolCalls, providerToolDefs); err != nil {
			return nil, fmt.Errorf("invalid model tool-call batch: %w", err)
		}
		for index := range normalizedToolCalls {
			normalized := normalizedToolCalls[index]
			arguments, detachErr := DetachToolArguments(normalized.Arguments)
			if detachErr != nil {
				return nil, fmt.Errorf("detach model tool arguments: %w", detachErr)
			}
			normalized.Arguments = arguments
			normalizedToolCalls[index] = normalized
		}

		// 5. Log tool calls
		toolNames := make([]any, 0, len(normalizedToolCalls))
		for _, tc := range normalizedToolCalls {
			toolNames = append(toolNames, tc.Name)
		}
		logger.InfoSafeCF(
			logger.ComponentToolLoop,
			logger.DiagnosticMessageLLMRequestedToolCalls,
			logger.NewSafeFields(
				logger.SafeInt(logger.FieldIteration, iteration),
				logger.SafeInt(logger.FieldToolCallCount, len(normalizedToolCalls)),
				logger.SafeObservation(
					logger.ObservationPrefixIdentityTool,
					logger.ObserveJSONValue(logger.ObservationDomainIdentityTool, toolNames),
				),
			),
		)

		// 6. Build assistant message with tool calls
		assistantMsg := providers.Message{
			Role:             "assistant",
			Content:          response.Content,
			ReasoningContent: response.ReasoningContent,
		}
		for _, tc := range normalizedToolCalls {
			argumentsJSON, _ := json.Marshal(tc.Arguments)
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
			result     *ToolResult
			tc         providers.ToolCall
			invocation *PreparedToolInvocation
			claim      *ClaimedToolInvocation
			decision   ToolPolicyDecision
			err        error
		}

		results := make([]indexedResult, len(normalizedToolCalls))
		authorize := func(i int, tc providers.ToolCall) {
			results[i].tc = tc
			invocation, prepareErr := toolCatalog.PrepareInvocation(tc.Name, tc.Arguments)
			if prepareErr != nil {
				if errors.Is(prepareErr, workflows.ErrToolCallNotDispatched) {
					results[i].result = ErrorResult(fmt.Sprintf("invalid tool call for %q", tc.Name)).
						WithError(prepareErr)
					return
				}
				results[i].err = prepareErr
				return
			}
			offeredDefinition, _ := findExactToolDefinition(providerToolDefs, tc.Name)
			if offeredErr := invocation.ValidateOfferedDefinition(offeredDefinition); offeredErr != nil {
				results[i].result = ErrorResult(fmt.Sprintf("invalid tool call for %q", tc.Name)).
					WithError(offeredErr)
				return
			}
			policyArguments, detachErr := invocation.PolicyArguments()
			if detachErr != nil {
				results[i].err = detachErr
				return
			}
			subject := config.PolicySubject
			subject.ToolCallID = tc.ID
			if subject.Source == "" {
				subject.Source = ToolPolicySourceGenericLoop
			}
			decision, policyErr := EvaluateToolPolicy(ctx, config.Policy, ToolPolicyRequest{
				Subject:     subject,
				Tool:        invocation.Name(),
				Arguments:   policyArguments,
				Traits:      invocation.Traits(),
				Fulfillment: ToolFulfillmentExecute,
			})
			if policyErr != nil {
				results[i].err = policyErr
				return
			}
			results[i].invocation = invocation
			results[i].decision = decision
			if decision.Kind == ToolPolicyDecisionDeny {
				results[i].result = ErrorResult("Tool execution denied by policy.").
					WithError(workflows.ErrToolCallNotDispatched)
			}
		}

		// Parallel loops authorize the complete provider batch before any effect.
		// This prevents a policy infrastructure failure from racing an allowed
		// sibling dispatch.
		if !config.SequentialToolCalls {
			var wg sync.WaitGroup
			for i, tc := range normalizedToolCalls {
				wg.Add(1)
				go func(idx int, call providers.ToolCall) {
					defer wg.Done()
					authorize(idx, call)
				}(i, tc)
			}
			wg.Wait()
			for i := range results {
				if results[i].err != nil {
					return nil, fmt.Errorf("authorize tool call %d: %w", i+1, results[i].err)
				}
			}
			// Claim every allowed invocation as a second batch barrier. A stale
			// entry or cancellation prevents all effects from this response,
			// including siblings that already claimed successfully.
			for i := range results {
				if results[i].result != nil {
					continue
				}
				claim, claimErr := config.Tools.ClaimPrepared(ctx, results[i].invocation)
				if claimErr != nil {
					return nil, fmt.Errorf("claim tool call %d: %w", i+1, claimErr)
				}
				results[i].claim = claim
			}
		}

		execute := func(i int) {
			tc := results[i].tc
			if results[i].result != nil {
				return
			}

			diagnosticArguments := normalizeToolArgumentsForDiagnostics(tc.Arguments)
			callFields := logger.NewSafeFields(
				logger.SafeInt(logger.FieldIteration, iteration),
				logger.SafeInt(logger.FieldArgumentCount, len(tc.Arguments)),
				logger.SafeBool(logger.FieldSuppressed, effectiveSuppressed),
				logger.SafeObservation(
					logger.ObservationPrefixIdentityTool,
					logger.ObserveIdentity(logger.ObservationDomainIdentityTool, tc.Name),
				),
				logger.SafeObservation(
					logger.ObservationPrefixIdentityToolCall,
					logger.ObserveIdentity(logger.ObservationDomainIdentityToolCall, tc.ID),
				),
				logger.SafeObservation(
					logger.ObservationPrefixToolArguments,
					logger.ObserveJSONValue(
						logger.ObservationDomainToolArguments,
						diagnosticArguments,
					),
				),
			)
			logger.InfoSafeCF(
				logger.ComponentToolLoop,
				logger.DiagnosticMessageToolCall,
				callFields,
			)
			logger.DebugSensitiveCF(
				config.Tools.diagnosticPolicyForContext(ctx, effectiveSuppressed),
				logger.ComponentToolLoop,
				logger.DiagnosticMessageToolArguments,
				logger.NewSafeFields(
					logger.SafeInt(logger.FieldIteration, iteration),
					logger.SafeInt(logger.FieldArgumentCount, len(tc.Arguments)),
					logger.SafeBool(logger.FieldSuppressed, effectiveSuppressed),
					logger.SafeObservation(
						logger.ObservationPrefixIdentityTool,
						logger.ObserveIdentity(logger.ObservationDomainIdentityTool, tc.Name),
					),
					logger.SafeObservation(
						logger.ObservationPrefixIdentityToolCall,
						logger.ObserveIdentity(logger.ObservationDomainIdentityToolCall, tc.ID),
					),
				),
				logger.SensitivityToolArguments,
				logger.ObservationDomainToolArguments,
				diagnosticArguments,
			)

			if results[i].claim == nil {
				claim, claimErr := config.Tools.ClaimPrepared(ctx, results[i].invocation)
				if claimErr != nil {
					results[i].err = claimErr
					return
				}
				results[i].claim = claim
			}
			var dispatchErr error
			results[i].result, dispatchErr = config.Tools.DispatchClaimed(
				ctx,
				results[i].claim,
				channel,
				chatID,
				nil,
				effectiveSuppressed,
			)
			if dispatchErr != nil {
				results[i].err = dispatchErr
				return
			}
		}

		if config.SequentialToolCalls {
			for i, tc := range normalizedToolCalls {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				authorize(i, tc)
				if results[i].err != nil {
					return nil, fmt.Errorf("authorize tool call %d: %w", i+1, results[i].err)
				}
				execute(i)
				if results[i].err != nil {
					return nil, fmt.Errorf("dispatch tool call %d: %w", i+1, results[i].err)
				}
			}
		} else {
			var wg sync.WaitGroup
			for i := range normalizedToolCalls {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					execute(idx)
				}(i)
			}
			wg.Wait()
			for i := range results {
				if results[i].err != nil {
					return nil, fmt.Errorf("dispatch tool call %d: %w", i+1, results[i].err)
				}
			}
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

func findExactToolDefinition(
	definitions []providers.ToolDefinition,
	name string,
) (providers.ToolDefinition, bool) {
	for _, definition := range definitions {
		if definition.Function.Name == name {
			return definition, true
		}
	}
	return providers.ToolDefinition{}, false
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
