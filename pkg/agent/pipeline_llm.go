// PicoClaw - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/accountrouter"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/constants"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	providercommon "github.com/sipeed/picoclaw/pkg/providers/common"
	picotools "github.com/sipeed/picoclaw/pkg/tools"
)

// CallLLM performs an LLM call with fallback support, hook invocation, and retry logic.
// It handles PreLLM setup, the actual LLM invocation with retry, and AfterLLM processing.
// Returns Control indicating what the coordinator should do next.
func (p *Pipeline) CallLLM(
	ctx context.Context,
	turnCtx context.Context,
	ts *turnState,
	exec *turnExecution,
	iteration int,
) (Control, error) {
	al := p.al
	// BeforeLLM narrowing is request-local. Preserve it across retries and
	// fallback candidates for this request, then let the next iteration's hook
	// decide again within the frozen turn capability cap.
	exec.nativeSearchNarrowed = false
	exec.useNativeSearch = false
	maxMediaSize := p.Cfg.Agents.Defaults.GetMaxMediaSize()

	// PreLLM: resolve media refs (except on iteration 1 where user media is already resolved)
	if iteration > 1 {
		exec.messages = resolveMediaRefs(exec.messages, p.MediaStore, maxMediaSize, exec.currentTurnStart)
	}

	// PreLLM: graceful terminal handling
	exec.gracefulTerminal, _ = ts.gracefulInterruptRequested()
	baseProviderToolDefs := ts.agent.Tools.ToProviderDefs()
	baseProviderToolDefs = filterToolsByTurnProfile(baseProviderToolDefs, ts.profile)
	exec.visibleToolSurface = effectiveToolAdaptationSurfaceForTurn(p.Cfg, ts, exec)
	exec.providerToolDefs = applyToolAdaptationSurface(exec.visibleToolSurface, baseProviderToolDefs)

	exec.callMessages = exec.messages
	if exec.gracefulTerminal {
		exec.callMessages = append(append([]providers.Message(nil), exec.messages...), ts.interruptHintMessage())
		baseProviderToolDefs = nil
		exec.providerToolDefs = nil
		ts.markGracefulTerminalUsed()
	}
	if err := p.routeMediaTurn(ts, exec); err != nil {
		return ControlBreak, err
	}
	exec.llmOpts = map[string]any{
		"max_tokens":  ts.agent.MaxTokens,
		"temperature": ts.agent.Temperature,
	}
	if !ts.opts.DisablePromptCache {
		cacheKey := strings.TrimSpace(ts.opts.PromptCacheKey)
		if cacheKey == "" {
			cacheKey = ts.agent.ID
		}
		exec.llmOpts["prompt_cache_key"] = cacheKey
	}
	if !exec.gracefulTerminal {
		projectedDefinitions := baseProviderToolDefs
		projectedDefinitions, exec.useNativeSearch = projectNativeSearchForProvider(
			p.Cfg,
			ts,
			exec.activeProvider,
			!exec.nativeSearchNarrowed,
			projectedDefinitions,
			exec.llmOpts,
		)
		exec.visibleToolSurface = effectiveToolAdaptationSurfaceForTurn(p.Cfg, ts, exec)
		exec.providerToolDefs = applyToolAdaptationSurface(
			exec.visibleToolSurface,
			projectedDefinitions,
		)
	}
	applyTurnThinkingOptions(exec, ts.agent, exec.activeProvider, true)
	applyReasoningEffortOption(exec.llmOpts, exec.activeModelConfig)
	applyReasoningEffortOverride(exec.llmOpts, ts.opts.ReasoningEffortOverride)

	exec.llmModel = exec.activeModel

	// BeforeLLM hook
	if p.Hooks != nil {
		hookInputToolDefs := exec.providerToolDefs
		llmReq, decision := p.Hooks.BeforeLLM(turnCtx, &LLMHookRequest{
			Meta:             ts.eventMeta("runTurn", "turn.llm.request"),
			Context:          cloneTurnContext(ts.turnCtx),
			Model:            exec.llmModelName,
			Messages:         exec.callMessages,
			Tools:            exec.providerToolDefs,
			Options:          exec.llmOpts,
			GracefulTerminal: exec.gracefulTerminal,
		})
		switch decision.normalizedAction() {
		case HookActionContinue, HookActionModify:
			if llmReq != nil {
				prevAlias := exec.llmModelName
				hookSawNativeSearch := exec.useNativeSearch
				hookSawPhysicalSearch := toolDefinitionsContainFoldedName(
					hookInputToolDefs,
					subTurnNativeSearchCapability,
				)
				exec.callMessages = llmReq.Messages
				baseProviderToolDefs = mergeHookToolDefinitionChanges(
					baseProviderToolDefs,
					hookInputToolDefs,
					filterToolsByTurnProfile(llmReq.Tools, ts.profile),
				)
				baseProviderToolDefs = filterSubTurnHookDefinitionsToPhysicalTools(
					ts,
					baseProviderToolDefs,
				)
				if llmReq.Options == nil {
					llmReq.Options = make(map[string]any)
				}
				if nativeValue, present := llmReq.Options["native_search"]; present {
					nativeEnabled, valid := nativeValue.(bool)
					if !valid || !nativeEnabled {
						exec.nativeSearchNarrowed = true
					}
				} else if hookSawNativeSearch {
					exec.nativeSearchNarrowed = true
				}
				if hookSawPhysicalSearch && !toolDefinitionsContainFoldedName(
					baseProviderToolDefs,
					subTurnNativeSearchCapability,
				) {
					exec.nativeSearchNarrowed = true
				}
				exec.llmOpts = llmReq.Options
				if strings.TrimSpace(llmReq.Model) != strings.TrimSpace(prevAlias) {
					if err := p.applyBeforeLLMModelRewrite(ts, exec, llmReq.Model); err != nil {
						return ControlBreak, err
					}
					applyTurnThinkingOptions(exec, ts.agent, exec.activeProvider, true)
					applyReasoningEffortOption(exec.llmOpts, exec.activeModelConfig)
				}
				exec.visibleToolSurface = effectiveToolAdaptationSurfaceForTurn(p.Cfg, ts, exec)
				projectedDefinitions, nativeSearch := projectNativeSearchForProvider(
					p.Cfg,
					ts,
					exec.activeProvider,
					!exec.gracefulTerminal && !exec.nativeSearchNarrowed,
					baseProviderToolDefs,
					exec.llmOpts,
				)
				exec.useNativeSearch = nativeSearch
				exec.providerToolDefs = applyToolAdaptationSurface(
					exec.visibleToolSurface,
					projectedDefinitions,
				)
			}
		case HookActionAbortTurn:
			cancelConfiguredStreamingLLM(turnCtx, exec)
			exec.abortedByHook = true
			return ControlBreak, nil
		case HookActionHardAbort:
			cancelConfiguredStreamingLLM(turnCtx, exec)
			_ = ts.requestHardAbort()
			exec.abortedByHardAbort = true
			return ControlBreak, nil
		}
	}

	al.emitEvent(
		runtimeevents.KindAgentLLMRequest,
		ts.eventMeta("runTurn", "turn.llm.request"),
		LLMRequestPayload{
			Model:         exec.llmModel,
			MessagesCount: len(exec.callMessages),
			ToolsCount:    len(exec.providerToolDefs),
			MaxTokens:     ts.agent.MaxTokens,
			Temperature:   ts.agent.Temperature,
		},
	)

	logger.DebugCF("agent", "LLM request",
		map[string]any{
			"agent_id":          ts.agent.ID,
			"iteration":         iteration,
			"model":             exec.llmModel,
			"messages_count":    len(exec.callMessages),
			"tools_count":       len(exec.providerToolDefs),
			"max_tokens":        ts.agent.MaxTokens,
			"temperature":       ts.agent.Temperature,
			"system_prompt_len": len(exec.callMessages[0].Content),
		})
	logger.DebugCF("agent", "Full LLM request",
		map[string]any{
			"iteration":     iteration,
			"messages_json": formatMessagesForLog(exec.callMessages),
			"tools_json":    formatToolsForLog(exec.providerToolDefs),
		})

	// LLM call closure with fallback support
	callLLM := func(messagesForCall []providers.Message, toolDefsForCall []providers.ToolDefinition) (*providers.LLMResponse, error) {
		if usageErr := ts.workflowAgentUsageError(); usageErr != nil {
			return nil, usageErr
		}
		if !exec.gracefulTerminal {
			projectedDefinitions, nativeSearch := projectNativeSearchForProvider(
				p.Cfg,
				ts,
				exec.activeProvider,
				!exec.nativeSearchNarrowed,
				baseProviderToolDefs,
				exec.llmOpts,
			)
			exec.useNativeSearch = nativeSearch
			exec.visibleToolSurface = effectiveToolAdaptationSurfaceForTurn(p.Cfg, ts, exec)
			toolDefsForCall = applyToolAdaptationSurface(
				exec.visibleToolSurface,
				projectedDefinitions,
			)
			exec.providerToolDefs = toolDefsForCall
		}
		providerCtx, providerCancel := context.WithCancel(turnCtx)
		ts.setProviderCancel(providerCancel)
		defer func() {
			providerCancel()
			ts.clearProviderCancel(providerCancel)
		}()

		al.activeRequestsInc()
		defer al.activeRequestsDec()

		streamStartedAt := time.Now()
		if response, handled, streamErr := p.tryConfiguredStreamingLLM(
			providerCtx,
			ts,
			exec,
			messagesForCall,
			toolDefsForCall,
		); handled {
			if observeErr := ts.observeWorkflowAgentResponse(
				exec.llmModel,
				response,
				time.Since(streamStartedAt),
			); observeErr != nil {
				return response, observeErr
			}
			if streamErr == nil {
				streamErr = providers.ResponseSafetyFilterError(response, "", exec.llmModel)
			}
			return response, streamErr
		}

		runCandidate := func(
			ctx context.Context,
			candidate providers.FallbackCandidate,
		) (*providers.LLMResponse, error) {
			if admissionErr := admitWorkflowAgentCall(ts.opts.callAdmission); admissionErr != nil {
				return nil, admissionErr
			}
			if usageErr := ts.workflowAgentUsageError(); usageErr != nil {
				return nil, usageErr
			}
			candidateProvider, err := providerForFallbackCandidate(
				ts.agent,
				exec.activeProvider,
				candidate,
			)
			if err != nil {
				return nil, err
			}
			callOpts := shallowCloneLLMOptions(exec.llmOpts)
			delete(callOpts, "thinking_level")
			candidateCfg := resolveActiveModelConfig(
				p.Cfg,
				ts.agent.Workspace,
				[]providers.FallbackCandidate{candidate},
				candidate.Model,
				p.Cfg.Agents.Defaults.Provider,
			)
			candidateThinking := thinkingSettingsFromModelConfig(candidateCfg)
			applyThinkingOption(callOpts, candidateProvider, candidateThinking, true, ts.agent.ID)
			applyReasoningEffortOption(callOpts, candidateCfg)
			applyReasoningEffortOverride(callOpts, ts.opts.ReasoningEffortOverride)
			candidateBaseDefinitions, candidateNativeSearch := projectNativeSearchForProvider(
				p.Cfg,
				ts,
				candidateProvider,
				!exec.nativeSearchNarrowed,
				baseProviderToolDefs,
				callOpts,
			)
			candidateSurface, candidateToolDefs := toolAdaptationForCandidate(
				p.Cfg,
				ts,
				exec,
				candidate,
				candidateCfg,
				candidateBaseDefinitions,
			)
			startedAt := time.Now()
			response, err := candidateProvider.Chat(
				ctx,
				messagesForCall,
				candidateToolDefs,
				candidate.Model,
				callOpts,
			)
			if observeErr := ts.observeWorkflowAgentResponse(
				candidate.Model,
				response,
				time.Since(startedAt),
			); observeErr != nil {
				return response, observeErr
			}
			if err == nil {
				err = providers.ResponseSafetyFilterError(response, candidate.Provider, candidate.Model)
			}
			if err == nil {
				exec.visibleToolSurface = candidateSurface
				exec.providerToolDefs = candidateToolDefs
				exec.useNativeSearch = candidateNativeSearch
				exec.suppressReasoning = shouldSuppressReasoningFor(candidateThinking)
			}
			return response, err
		}

		if len(exec.activeCandidates) > 1 && p.Fallback != nil {
			var (
				fbResult *providers.FallbackResult
				fbErr    error
			)
			if hasMediaRefs(messagesForCall) {
				fbResult, fbErr = p.Fallback.ExecuteImage(
					providerCtx,
					exec.activeCandidates,
					func(ctx context.Context, candidate providers.FallbackCandidate) (*providers.LLMResponse, error) {
						return runCandidate(ctx, candidate)
					},
				)
			} else {
				fbResult, fbErr = p.Fallback.ExecuteCandidate(
					providerCtx,
					exec.activeCandidates,
					runCandidate,
				)
			}
			if usageErr := ts.workflowAgentUsageError(); usageErr != nil {
				return nil, usageErr
			}
			if fbErr != nil {
				if exec.accountRouter != nil {
					exec.accountRouter.RecordFallbackResult(
						exec.routerSelection,
						fallbackResultFromError(fbErr, exec.activeCandidates...),
						fbErr,
					)
				}
				return nil, fbErr
			}
			if exec.accountRouter != nil {
				exec.accountRouter.RecordFallbackResult(exec.routerSelection, fbResult, nil)
			}
			if fbResult.Provider != "" && len(fbResult.Attempts) > 0 {
				logger.InfoCF(
					"agent",
					fmt.Sprintf("Fallback: succeeded with %s/%s after %d attempts",
						fbResult.Provider, fbResult.Model, len(fbResult.Attempts)+1),
					map[string]any{"agent_id": ts.agent.ID, "iteration": iteration},
				)
			}
			p.applySuccessfulFallbackCandidate(ts, exec, fbResult)
			return fbResult.Response, nil
		}
		if admissionErr := admitWorkflowAgentCall(ts.opts.callAdmission); admissionErr != nil {
			return nil, admissionErr
		}
		startedAt := time.Now()
		resp, err := exec.activeProvider.Chat(
			providerCtx,
			messagesForCall,
			toolDefsForCall,
			exec.llmModel,
			exec.llmOpts,
		)
		if observeErr := ts.observeWorkflowAgentResponse(
			exec.llmModel,
			resp,
			time.Since(startedAt),
		); observeErr != nil {
			return resp, observeErr
		}
		if err == nil {
			err = providers.ResponseSafetyFilterError(resp, "", exec.llmModel)
		}
		if exec.accountRouter != nil {
			candidate := providers.FallbackCandidate{}
			if len(exec.activeCandidates) > 0 {
				candidate = exec.activeCandidates[0]
			}
			exec.accountRouter.RecordFallbackResult(
				exec.routerSelection,
				fallbackResultFromSingleCandidate(candidate, resp, err),
				err,
			)
		}
		return resp, err
	}

	// Retry loop
	var err error
	maxRetries := p.Cfg.Agents.Defaults.MaxLLMRetries
	if maxRetries <= 0 {
		maxRetries = 2
	}
	backoffSecs := p.Cfg.Agents.Defaults.LLMRetryBackoffSecs
	if backoffSecs <= 0 {
		backoffSecs = 2
	}
	for retry := 0; retry <= maxRetries; retry++ {
		exec.response, err = callLLM(exec.callMessages, exec.providerToolDefs)
		if usageErr := ts.workflowAgentUsageError(); usageErr != nil {
			return ControlBreak, usageErr
		}
		if err == nil {
			break
		}
		if ts.hardAbortRequested() && errors.Is(err, context.Canceled) {
			_ = ts.requestHardAbort()
			exec.abortedByHardAbort = true
			return ControlBreak, nil
		}
		if isConfiguredStreamingVisibleError(err) {
			break
		}

		if hasMediaRefs(exec.callMessages) && isVisionUnsupportedError(err) {
			return ControlBreak, visionUnsupportedModelError(
				exec.llmModelName,
				len(ts.agent.ImageCandidates) > 0,
			)
		}

		errMsg := strings.ToLower(err.Error())
		retryReason, isTransientError := transientLLMRetryReason(err)
		isContextError := !isTransientError && (strings.Contains(errMsg, "context_length_exceeded") ||
			strings.Contains(errMsg, "context window") ||
			strings.Contains(errMsg, "context_window") ||
			strings.Contains(errMsg, "maximum context length") ||
			strings.Contains(errMsg, "token limit") ||
			strings.Contains(errMsg, "too many tokens") ||
			strings.Contains(errMsg, "max_tokens") ||
			strings.Contains(errMsg, "invalidparameter") ||
			strings.Contains(errMsg, "prompt is too long") ||
			strings.Contains(errMsg, "request too large"))

		if isTransientError && retry < maxRetries {
			backoff := time.Duration(retry+1) * time.Duration(backoffSecs) * time.Second
			al.emitEvent(
				runtimeevents.KindAgentLLMRetry,
				ts.eventMeta("runTurn", "turn.llm.retry"),
				LLMRetryPayload{
					Attempt:    retry + 1,
					MaxRetries: maxRetries,
					Reason:     retryReason,
					Error:      err.Error(),
					Backoff:    backoff,
				},
			)
			logger.WarnCF("agent", "Transient LLM error, retrying after backoff", map[string]any{
				"error":   err.Error(),
				"reason":  retryReason,
				"retry":   retry,
				"backoff": backoff.String(),
			})
			if sleepErr := sleepWithContext(turnCtx, backoff); sleepErr != nil {
				if ts.hardAbortRequested() {
					_ = ts.requestHardAbort()
					return ControlBreak, nil
				}
				err = sleepErr
				break
			}
			continue
		}

		if isContextError && retry < maxRetries && !ts.opts.NoHistory {
			al.emitEvent(
				runtimeevents.KindAgentLLMRetry,
				ts.eventMeta("runTurn", "turn.llm.retry"),
				LLMRetryPayload{
					Attempt:    retry + 1,
					MaxRetries: maxRetries,
					Reason:     "context_limit",
					Error:      err.Error(),
				},
			)
			logger.WarnCF(
				"agent",
				"Context window error detected, attempting compression",
				map[string]any{
					"error": err.Error(),
					"retry": retry,
				},
			)

			if retry == 0 && !constants.IsInternalChannel(ts.channel) {
				al.bus.PublishOutbound(ctx, outboundMessageForTurn(
					ts,
					"Context window exceeded. Compressing history and retrying...",
				))
			}

			if compactErr := p.ContextManager.Compact(ctx, &CompactRequest{
				SessionKey: ts.sessionKey,
				Reason:     ContextCompressReasonRetry,
				Budget:     ts.agent.ContextWindow,
			}); compactErr != nil {
				logger.WarnCF("agent", "Context overflow compact failed", map[string]any{
					"session_key": ts.sessionKey,
					"error":       compactErr.Error(),
				})
			}
			ts.refreshRestorePointFromSession(ts.agent)
			if asmResp, asmErr := p.ContextManager.Assemble(ctx, &AssembleRequest{
				SessionKey: ts.sessionKey,
				Budget:     ts.agent.ContextWindow,
				MaxTokens:  ts.agent.MaxTokens,
			}); asmErr == nil && asmResp != nil {
				exec.history = asmResp.History
				exec.summary = asmResp.Summary
			}
			contextualSkills := ts.activeSkills
			if ts.agent.ContextBuilder != nil {
				contextualSkills = ts.agent.ContextBuilder.ResolveActiveSkillsForContext(ts.activeSkills)
			}
			ts.recordSkillContextSnapshot(skillContextTriggerContextRetryRebuild, contextualSkills)
			stableHistory, protectedTurnTail := splitHistoryForActiveTurn(
				exec.history,
				ts.persistedMessagesSnapshot(),
			)
			buildMessages := func(trimmedHistory []providers.Message) []providers.Message {
				fullHistory := append(append([]providers.Message(nil), trimmedHistory...), protectedTurnTail...)
				rebuildPromptReq := promptBuildRequestForTurn(ts, fullHistory, exec.summary, "", nil, p.Cfg)
				rebuildPromptReq.ActiveSkills = append([]string(nil), contextualSkills...)
				rebuilt := ts.agent.ContextBuilder.BuildMessagesFromPrompt(rebuildPromptReq)
				return resolveMediaRefs(
					rebuilt,
					p.MediaStore,
					maxMediaSize,
					len(rebuilt)-len(protectedTurnTail),
				)
			}
			originalHistoryCount := len(exec.history)
			var fit bool
			var trimmedStableHistory []providers.Message
			trimmedStableHistory, exec.callMessages, fit = trimHistoryToFitContextWindow(
				stableHistory,
				func(trimmedHistory []providers.Message) []providers.Message {
					rebuilt := buildMessages(trimmedHistory)
					if exec.gracefulTerminal {
						return append(append([]providers.Message(nil), rebuilt...), ts.interruptHintMessage())
					}
					return rebuilt
				},
				ts.agent.ContextWindow,
				exec.providerToolDefs,
				ts.agent.MaxTokens,
			)
			exec.history = append(trimmedStableHistory, protectedTurnTail...)
			exec.messages = buildMessages(trimmedStableHistory)
			exec.currentTurnStart = len(exec.messages) - len(protectedTurnTail)
			if exec.gracefulTerminal {
				msgs := append([]providers.Message(nil), exec.messages...)
				exec.callMessages = append(msgs, ts.interruptHintMessage())
			}
			if dropped := originalHistoryCount - len(exec.history); dropped > 0 {
				logger.WarnCF("agent", "Trimmed rebuilt history after context retry compaction", map[string]any{
					"session_key":     ts.sessionKey,
					"retry":           retry,
					"dropped_msgs":    dropped,
					"remaining_msgs":  len(exec.history),
					"context_window":  ts.agent.ContextWindow,
					"max_tokens":      ts.agent.MaxTokens,
					"still_overlimit": !fit,
				})
			} else if !fit {
				logger.WarnCF("agent", "Context still exceeds budget after retry compaction rebuild", map[string]any{
					"session_key":         ts.sessionKey,
					"retry":               retry,
					"history_msgs":        len(exec.history),
					"protected_turn_msgs": len(protectedTurnTail),
					"context_window":      ts.agent.ContextWindow,
					"max_tokens":          ts.agent.MaxTokens,
				})
			}
			if !fit {
				err = fmt.Errorf(
					"context window still exceeded after retry compaction; refusing to drop active turn messages: %w",
					err,
				)
				break
			}
			p.reselectAccountRouterAfterCompression(ts, exec)
			continue
		}
		break
	}

	if err != nil {
		al.emitEvent(
			runtimeevents.KindAgentError,
			ts.eventMeta("runTurn", "turn.error"),
			ErrorPayload{
				Stage:   "llm",
				Message: err.Error(),
			},
		)
		logger.ErrorCF("agent", "LLM call failed",
			map[string]any{
				"agent_id":  ts.agent.ID,
				"iteration": iteration,
				"model":     exec.llmModel,
				"error":     err.Error(),
			})
		return ControlBreak, fmt.Errorf("LLM call failed after retries: %w", err)
	}

	// AfterLLM hook
	if p.Hooks != nil {
		llmResp, decision := p.Hooks.AfterLLM(turnCtx, &LLMHookResponse{
			Meta:     ts.eventMeta("runTurn", "turn.llm.response"),
			Context:  cloneTurnContext(ts.turnCtx),
			Model:    exec.llmModelName,
			Response: exec.response,
		})
		switch decision.normalizedAction() {
		case HookActionContinue, HookActionModify:
			if llmResp != nil && llmResp.Response != nil {
				exec.response = llmResp.Response
			}
		case HookActionAbortTurn:
			cancelConfiguredStreamingLLM(turnCtx, exec)
			exec.abortedByHook = true
			return ControlBreak, nil
		case HookActionHardAbort:
			cancelConfiguredStreamingLLM(turnCtx, exec)
			_ = ts.requestHardAbort()
			exec.abortedByHardAbort = true
			return ControlBreak, nil
		}
	}

	// Save finishReason and usage on the turn state. Use ts directly (the
	// authoritative turn state for this call) rather than a context lookup:
	// the raw ctx passed to CallLLM is not seeded with turnState (only turnCtx
	// is), so turnStateFromContext(ctx) returns nil here and silently dropped
	// both the finish reason and the per-turn token usage. ts is also exactly
	// what the streaming publisher reads via GetLastUsage at finalize.
	if ts != nil {
		ts.SetLastFinishReason(exec.response.FinishReason)
		if exec.response.Usage != nil {
			ts.SetLastUsage(exec.response.Usage)
			observeToolAdaptationCache(p.Cfg, ts, exec)
		}
	}

	if exec.suppressReasoning {
		exec.response.Reasoning = ""
		exec.response.ReasoningContent = ""
		exec.response.ReasoningDetails = nil
	}
	reasoningContent := responseReasoningContent(exec.response)
	shouldPublishPicoToolCallInterim := ts.channel == "pico" && len(exec.response.ToolCalls) > 0
	if shouldPublishPicoToolCallInterim {
		// Pico tool-call turns publish their reasoning/content/tool summary as a
		// structured sequence after the tool-call payload is normalized below.
	} else if ts.channel == "pico" {
		if exec.streamingPublisher != nil && exec.streamingPublisher.ReasoningPublished() {
			if err := exec.streamingPublisher.FinalizeReasoning(turnCtx, reasoningContent); err != nil {
				logger.WarnCF("agent", "Failed to finalize streamed pico reasoning", map[string]any{
					"channel": ts.channel,
					"chat_id": ts.chatID,
					"error":   err.Error(),
				})
			}
		} else {
			// Publish pico thoughts before the turn context is canceled at return time.
			// The async variant can race with turn teardown and intermittently drop the
			// thought message in CI even though the LLM produced reasoning content.
			al.publishPicoReasoning(turnCtx, reasoningContent, ts.chatID, ts.sessionKey, exec.llmModelName)
		}
	} else {
		reasoningCtx := context.WithoutCancel(turnCtx)
		go al.handleReasoning(
			reasoningCtx,
			reasoningContent,
			ts.channel,
			al.targetReasoningChannelID(ts.channel),
		)
	}
	al.emitEvent(
		runtimeevents.KindAgentLLMResponse,
		ts.eventMeta("runTurn", "turn.llm.response"),
		LLMResponsePayload{
			ContentLen:   len(exec.response.Content),
			ToolCalls:    len(exec.response.ToolCalls),
			HasReasoning: exec.response.Reasoning != "" || exec.response.ReasoningContent != "",
		},
	)

	llmResponseFields := map[string]any{
		"agent_id":       ts.agent.ID,
		"iteration":      iteration,
		"content_chars":  len(exec.response.Content),
		"tool_calls":     len(exec.response.ToolCalls),
		"reasoning":      exec.response.Reasoning,
		"target_channel": al.targetReasoningChannelID(ts.channel),
		"channel":        ts.channel,
	}
	if exec.response.Usage != nil {
		llmResponseFields["prompt_tokens"] = exec.response.Usage.PromptTokens
		llmResponseFields["completion_tokens"] = exec.response.Usage.CompletionTokens
		llmResponseFields["total_tokens"] = exec.response.Usage.TotalTokens
	}
	logger.DebugCF("agent", "LLM response", llmResponseFields)

	// No-tool-call path: steering check and direct response
	if len(exec.response.ToolCalls) == 0 || exec.gracefulTerminal {
		responseContent := exec.response.Content
		if responseContent == "" && exec.response.ReasoningContent != "" && ts.channel != "pico" {
			responseContent = exec.response.ReasoningContent
		}
		if steerMsgs := al.dequeueSteeringMessagesForScope(ts.sessionKey); len(steerMsgs) > 0 {
			cancelConfiguredStreamingLLM(turnCtx, exec)
			logger.InfoCF("agent", "Steering arrived after direct LLM response; continuing turn",
				map[string]any{
					"agent_id":       ts.agent.ID,
					"iteration":      iteration,
					"steering_count": len(steerMsgs),
				})
			exec.pendingMessages = append(exec.pendingMessages, steerMsgs...)
			return ControlContinue, nil
		}

		exec.finalContent = responseContent
		logger.InfoCF("agent", "LLM response without tool calls (direct answer)",
			map[string]any{
				"agent_id":      ts.agent.ID,
				"iteration":     iteration,
				"content_chars": len(exec.finalContent),
			})
		return ControlBreak, nil
	}
	cancelConfiguredStreamingLLM(turnCtx, exec)

	// Tool-call path: normalize and prepare for tool execution
	exec.normalizedToolCalls = make([]providers.ToolCall, 0, len(exec.response.ToolCalls))
	for _, tc := range exec.response.ToolCalls {
		exec.normalizedToolCalls = append(exec.normalizedToolCalls, providers.NormalizeToolCall(tc))
	}

	toolNames := make([]string, 0, len(exec.normalizedToolCalls))
	for _, tc := range exec.normalizedToolCalls {
		toolNames = append(toolNames, tc.Name)
	}
	logger.InfoCF("agent", "LLM requested tool calls",
		map[string]any{
			"agent_id":  ts.agent.ID,
			"tools":     toolNames,
			"count":     len(exec.normalizedToolCalls),
			"iteration": iteration,
		})

	exec.allResponsesHandled = len(exec.normalizedToolCalls) > 0
	assistantMsg := providers.Message{
		Role:             "assistant",
		Content:          exec.response.Content,
		ModelName:        exec.llmModelName,
		ReasoningContent: reasoningContent,
	}
	for _, tc := range exec.normalizedToolCalls {
		argumentsJSON, _ := json.Marshal(tc.Arguments)
		toolFeedbackExplanation := toolFeedbackExplanationForToolCall(
			exec.response,
			tc,
			exec.messages,
		)
		extraContent := tc.ExtraContent
		if strings.TrimSpace(toolFeedbackExplanation) != "" {
			if extraContent == nil {
				extraContent = &providers.ExtraContent{}
			}
			extraContent.ToolFeedbackExplanation = toolFeedbackExplanation
		}
		thoughtSignature := ""
		if tc.Function != nil {
			thoughtSignature = tc.Function.ThoughtSignature
		}
		assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, providers.ToolCall{
			ID:   tc.ID,
			Type: "function",
			Name: tc.Name,
			Function: &providers.FunctionCall{
				Name:             tc.Name,
				Arguments:        string(argumentsJSON),
				ThoughtSignature: thoughtSignature,
			},
			ExtraContent:     extraContent,
			ThoughtSignature: thoughtSignature,
		})
	}
	exec.messages = append(exec.messages, assistantMsg)
	if !ts.opts.NoHistory {
		ts.agent.Sessions.AddFullMessage(ts.sessionKey, assistantMsg)
		ts.recordPersistedMessage(assistantMsg)
		ts.ingestMessage(turnCtx, al, assistantMsg)
	}
	if shouldPublishPicoToolCallInterim {
		al.publishPicoToolCallInterim(
			turnCtx,
			ts,
			exec.llmModelName,
			reasoningContent,
			exec.response.Content,
			assistantMsg.ToolCalls,
		)
	}

	return ControlToolLoop, nil
}

func (p *Pipeline) applyBeforeLLMModelRewrite(
	ts *turnState,
	exec *turnExecution,
	modelAlias string,
) error {
	if p == nil || ts == nil || ts.agent == nil || exec == nil {
		return fmt.Errorf("cannot resolve hook model alias without an active agent")
	}
	modelAlias = strings.TrimSpace(modelAlias)
	if err := validateModelAliasReferences(p.Cfg, modelAlias, nil); err != nil {
		return err
	}
	accountRef := concreteAccountRefForCandidates(exec.activeCandidates)
	if accountRef == "" {
		accountRef = firstConcreteAccountRef(p.Cfg, ts.agent.AccountRef)
	}
	candidates, err := candidatesForAccountAliases(
		p.Cfg,
		accountRef,
		modelAlias,
		nil,
		ts.agent.Workspace,
		ts.agent.CandidateProviders,
	)
	if err != nil {
		return err
	}
	activeProvider := workflowProviderForCandidates(ts.agent, nil, candidates)
	if activeProvider == nil {
		return fmt.Errorf(
			"model alias %q with account %q has no runnable provider",
			modelAlias,
			accountRef,
		)
	}
	if !exec.gracefulTerminal && !exec.nativeSearchNarrowed &&
		subTurnRequiresPseudoOnlyNativeSearch(ts) &&
		!subTurnCandidatesSupportNativeSearch(ts.agent, candidates) {
		return fmt.Errorf(
			"model alias %q is unavailable for pseudo-only native web search",
			modelAlias,
		)
	}
	exec.activeCandidates = candidates
	exec.activeModel = resolvedCandidateModel(candidates, modelAlias)
	exec.llmModel = exec.activeModel
	exec.llmModelName = modelAlias
	exec.activeProvider = activeProvider
	exec.activeModelConfig = resolveActiveModelConfig(
		p.Cfg,
		ts.agent.Workspace,
		candidates,
		exec.activeModel,
		"",
	)
	associateRouterSelectionCandidate(&exec.routerSelection, candidates[0], accountRef)
	return nil
}

func (p *Pipeline) applySuccessfulFallbackCandidate(
	ts *turnState,
	exec *turnExecution,
	fbResult *providers.FallbackResult,
) {
	if p == nil || ts == nil || ts.agent == nil || exec == nil || fbResult == nil {
		return
	}

	var selected providers.FallbackCandidate
	for _, candidate := range exec.activeCandidates {
		if candidate.StableKey() == fbResult.IdentityKey {
			selected = candidate
			break
		}
	}
	if selected.Model == "" {
		selected = providers.FallbackCandidate{
			Provider:    fbResult.Provider,
			Model:       fbResult.Model,
			IdentityKey: fbResult.IdentityKey,
		}
	}
	if selected.Model == "" {
		return
	}

	exec.activeCandidates = []providers.FallbackCandidate{selected}
	exec.activeModel = selected.Model
	exec.llmModel = selected.Model
	exec.llmModelName = resolvedCandidateModelName(
		[]providers.FallbackCandidate{selected},
		exec.llmModelName,
	)
	if provider, err := providerForFallbackCandidate(
		ts.agent,
		exec.activeProvider,
		selected,
	); err == nil &&
		provider != nil {
		exec.activeProvider = provider
	}

	defaultProvider := "openai"
	if p.Cfg != nil {
		defaultProvider = p.Cfg.Agents.Defaults.Provider
	}
	exec.activeModelConfig = resolveActiveModelConfig(
		p.Cfg,
		ts.agent.Workspace,
		[]providers.FallbackCandidate{selected},
		selected.Model,
		defaultProvider,
	)
}

func (p *Pipeline) reselectAccountRouterAfterCompression(ts *turnState, exec *turnExecution) {
	if p == nil || ts == nil || ts.agent == nil || exec == nil || exec.accountRouter == nil {
		return
	}
	selection := exec.accountRouter.Select(ts.sessionKey, accountrouter.SelectReasonCompression)
	if len(selection.Candidates) == 0 {
		return
	}
	exec.activeCandidates = selection.Candidates
	exec.routerSelection = selection
	exec.activeModel = resolvedCandidateModel(selection.Candidates, ts.agent.Model)
	exec.llmModel = exec.activeModel
	exec.llmModelName = resolvedCandidateModelName(selection.Candidates, ts.agent.Model)
	exec.activeProvider = workflowProviderForCandidates(ts.agent, ts.agent.Provider, selection.Candidates)
	exec.activeModelConfig = resolveActiveModelConfig(
		p.Cfg,
		ts.agent.Workspace,
		selection.Candidates,
		exec.activeModel,
		p.Cfg.Agents.Defaults.Provider,
	)
	logger.InfoCF("agent", "Account router reselected after context compression",
		map[string]any{
			"agent_id":    ts.agent.ID,
			"session_key": ts.sessionKey,
			"router":      selection.RouterName,
			"model":       exec.llmModelName,
		})
}

func providerForFallbackCandidate(
	agent *AgentInstance,
	activeProvider providers.LLMProvider,
	candidate providers.FallbackCandidate,
) (providers.LLMProvider, error) {
	if agent != nil {
		if cp := agent.candidateProviderForCandidate(candidate); cp != nil {
			return cp, nil
		}
	}
	if activeProvider == nil {
		return nil, fmt.Errorf("fallback model %q has no active provider", candidate.Model)
	}
	return activeProvider, nil
}

func observeToolAdaptationCache(cfg *config.Config, ts *turnState, exec *turnExecution) {
	if ts == nil || ts.agent == nil || exec == nil || exec.response == nil {
		return
	}
	if !ts.agent.ToolAdaptation.Enabled || !ts.agent.ToolAdaptation.LearnFromToolCalls {
		return
	}

	profile := toolAdaptationProfileForTurn(cfg, exec)

	observation, ok := picotools.ObserveToolAdaptationCache(
		profile,
		toolAdaptationSurfaceForObservation(ts, exec),
		exec.providerToolDefs,
		exec.response.Usage,
	)
	if ok {
		logger.DebugCF("agent", "Observed tool adaptation cache behavior", map[string]any{
			"provider":             observation.Profile.Provider,
			"model":                observation.Profile.Model,
			"visible_tool_surface": observation.VisibleToolSurface,
			"tool_schema_hash":     observation.ToolSchemaHash,
			"prompt_tokens":        observation.PromptTokens,
			"cached_tokens":        observation.CachedTokens,
			"cache_hit_ratio":      observation.CacheHitRatio,
			"cache_sensitive":      observation.CacheSensitive,
		})
	}
}

func observeToolAdaptationOutcome(
	cfg *config.Config,
	ts *turnState,
	exec *turnExecution,
	toolName string,
	result *picotools.ToolResult,
	duration time.Duration,
) {
	if ts == nil || ts.agent == nil || exec == nil || result == nil {
		return
	}
	if !ts.agent.ToolAdaptation.Enabled || !ts.agent.ToolAdaptation.LearnFromToolCalls {
		return
	}

	outcome, ok := picotools.ObserveToolAdaptationToolOutcome(
		toolAdaptationProfileForTurn(cfg, exec),
		toolAdaptationSurfaceForObservation(ts, exec),
		toolName,
		!result.IsError,
		toolErrorSummary(result),
		duration,
	)
	if ok {
		logger.DebugCF("agent", "Observed tool adaptation outcome", map[string]any{
			"provider":             outcome.Profile.Provider,
			"model":                outcome.Profile.Model,
			"visible_tool_surface": outcome.VisibleToolSurface,
			"tool":                 outcome.ToolName,
			"successes":            outcome.Successes,
			"failures":             outcome.Failures,
			"last_duration_ms":     outcome.LastDurationMS,
		})
	}
}

func toolAdaptationSurfaceForObservation(ts *turnState, exec *turnExecution) string {
	if exec != nil && strings.TrimSpace(exec.visibleToolSurface) != "" {
		return exec.visibleToolSurface
	}
	if ts != nil && ts.agent != nil {
		return ts.agent.ToolAdaptation.PinnedToolSurface
	}
	return ""
}

func toolAdaptationProfileForTurn(cfg *config.Config, exec *turnExecution) picotools.ToolAdaptationProfile {
	if exec == nil {
		return picotools.ToolAdaptationProfile{}
	}
	providerName := ""
	modelName := ""
	if len(exec.activeCandidates) > 0 {
		providerName = exec.activeCandidates[0].Provider
		modelName = exec.activeCandidates[0].Model
	}
	if exec.activeModelConfig != nil {
		if strings.TrimSpace(providerName) == "" {
			providerName = exec.activeModelConfig.Provider
		}
		if strings.TrimSpace(modelName) == "" {
			_, modelName = providers.ExtractProtocol(exec.activeModelConfig)
		}
	}
	if strings.TrimSpace(providerName) == "" && cfg != nil {
		providerName = cfg.Agents.Defaults.Provider
	}
	if strings.TrimSpace(modelName) == "" {
		modelName = strings.TrimSpace(exec.llmModel)
		if modelName == "" {
			modelName = strings.TrimSpace(exec.activeModel)
		}
	}
	return picotools.ToolAdaptationProfile{
		Provider: providers.NormalizeProvider(providerName),
		Model:    strings.TrimSpace(modelName),
	}
}

func effectiveToolAdaptationSurfaceForTurn(
	cfg *config.Config,
	ts *turnState,
	exec *turnExecution,
) string {
	if ts == nil || ts.agent == nil || !ts.agent.ToolAdaptation.Enabled {
		return config.ToolSurfacePicoClaw
	}
	profile := toolAdaptationProfileForTurn(cfg, exec)
	latest := ts.agent.ToolAdaptation
	hasProfileSurfaceOverride := false
	hasConcreteProfileSurfaceOverride := false
	if cfg != nil {
		latest = picotools.ResolveToolAdaptation(
			cfg.Tools.Adaptation,
			profile.Provider,
			profile.Model,
		)
		profileSurfaceOverride, hasOverride := toolAdaptationProfileSurfaceOverride(
			cfg.Tools.Adaptation,
			profile,
		)
		hasProfileSurfaceOverride = hasOverride
		hasConcreteProfileSurfaceOverride = hasOverride &&
			profileSurfaceOverride != config.ToolSurfaceAuto
	}

	// A routed or fallback model may have a different explicit profile than the
	// model selected at agent startup. Apply that profile for this turn even
	// when learned surface changes are otherwise pinned until the next session.
	if hasConcreteProfileSurfaceOverride {
		candidate := config.NormalizeToolSurface(latest.PinnedToolSurface)
		if candidate == config.ToolSurfaceAuto {
			candidate = config.NormalizeToolSurface(latest.VisibleToolSurface)
		}
		if candidate != config.ToolSurfaceAuto {
			return candidate
		}
	}

	pinned := config.NormalizeToolSurface(ts.agent.ToolAdaptation.PinnedToolSurface)
	if pinned == config.ToolSurfaceAuto {
		pinned = config.NormalizeToolSurface(ts.agent.ToolAdaptation.VisibleToolSurface)
	}
	if pinned == config.ToolSurfaceAuto {
		pinned = config.ToolSurfacePicoClaw
	}

	switch latest.ApplyVisibleChanges {
	case config.ToolVisibleChangeImmediate, config.ToolVisibleChangeContextBoundary:
	default:
		return pinned
	}
	if cfg == nil ||
		(!hasProfileSurfaceOverride &&
			config.NormalizeToolSurface(cfg.Tools.Adaptation.VisibleToolSurface) != config.ToolSurfaceAuto) {
		return pinned
	}

	candidate := config.NormalizeToolSurface(latest.VisibleToolSurface)
	if candidate == config.ToolSurfaceAuto || candidate == pinned {
		return pinned
	}
	if isToolSurfacePromotion(pinned, candidate) {
		if latest.RuntimePromotion {
			return candidate
		}
		return pinned
	}
	if latest.RuntimeDowngrade {
		return candidate
	}
	return pinned
}

func toolAdaptationProfileSurfaceOverride(
	cfg config.ToolAdaptationConfig,
	profile picotools.ToolAdaptationProfile,
) (string, bool) {
	profileKey := providers.ModelKey(profile.Provider, profile.Model)
	if profileKey == "/" {
		return "", false
	}
	var matched *config.ToolAdaptationProfileOverride
	normalized := cfg.Normalized()
	for i := range normalized.ProfileOverrides {
		override := &normalized.ProfileOverrides[i]
		if providers.ModelKey(override.Provider, override.Model) == profileKey {
			matched = override
		}
	}
	if matched == nil || strings.TrimSpace(matched.VisibleToolSurface) == "" {
		return "", false
	}
	return config.NormalizeToolSurface(matched.VisibleToolSurface), true
}

func toolAdaptationForCandidate(
	cfg *config.Config,
	ts *turnState,
	exec *turnExecution,
	candidate providers.FallbackCandidate,
	candidateCfg *config.ModelConfig,
	baseToolDefs []providers.ToolDefinition,
) (string, []providers.ToolDefinition) {
	if exec == nil {
		return config.ToolSurfacePicoClaw, applyToolAdaptationSurface(
			config.ToolSurfacePicoClaw,
			baseToolDefs,
		)
	}
	candidateExec := *exec
	candidateExec.activeCandidates = []providers.FallbackCandidate{candidate}
	candidateExec.activeModel = candidate.Model
	candidateExec.llmModel = candidate.Model
	candidateExec.activeModelConfig = candidateCfg
	surface := effectiveToolAdaptationSurfaceForTurn(cfg, ts, &candidateExec)
	return surface, applyToolAdaptationSurface(surface, baseToolDefs)
}

func mergeHookToolDefinitionChanges(
	base []providers.ToolDefinition,
	before []providers.ToolDefinition,
	after []providers.ToolDefinition,
) []providers.ToolDefinition {
	if llmHookToolDefinitionsUnchanged(before, after) {
		return base
	}
	if len(after) == 0 {
		return nil
	}

	beforeByName := make(map[string]providers.ToolDefinition, len(before))
	afterByName := make(map[string]providers.ToolDefinition, len(after))
	for _, def := range before {
		beforeByName[def.Function.Name] = def
	}
	for _, def := range after {
		afterByName[def.Function.Name] = def
	}

	removed := make(map[string]struct{})
	for name := range beforeByName {
		if _, ok := afterByName[name]; !ok {
			removed[name] = struct{}{}
		}
	}
	expandAdaptationEquivalentToolRemovals(removed)
	replacements := make(map[string]providers.ToolDefinition)
	for name, def := range afterByName {
		previous, existed := beforeByName[name]
		if !existed || !llmHookToolDefinitionsUnchanged(
			[]providers.ToolDefinition{previous},
			[]providers.ToolDefinition{def},
		) {
			replacements[name] = def
		}
	}

	merged := make([]providers.ToolDefinition, 0, len(base)+len(replacements))
	applied := make(map[string]struct{}, len(replacements))
	for _, def := range base {
		name := def.Function.Name
		if _, drop := removed[name]; drop {
			continue
		}
		if replacement, ok := replacements[name]; ok {
			merged = append(merged, replacement)
			applied[name] = struct{}{}
			continue
		}
		merged = append(merged, def)
	}
	for _, def := range after {
		name := def.Function.Name
		if _, exists := beforeByName[name]; exists {
			continue
		}
		if _, exists := applied[name]; exists {
			continue
		}
		merged = append(merged, def)
		applied[name] = struct{}{}
	}
	return merged
}

func expandAdaptationEquivalentToolRemovals(removed map[string]struct{}) {
	equivalentGroups := [][]string{
		{"exec", "exec_command", "write_stdin"},
		{"write_file", "edit_file", "append_file", "apply_patch"},
		{"load_image", "view_image"},
	}
	for _, group := range equivalentGroups {
		removeGroup := false
		for _, name := range group {
			if _, ok := removed[name]; ok {
				removeGroup = true
				break
			}
		}
		if !removeGroup {
			continue
		}
		for _, name := range group {
			removed[name] = struct{}{}
		}
	}
}

func isToolSurfacePromotion(from string, to string) bool {
	return toolSurfaceRank(to) > toolSurfaceRank(from)
}

func toolSurfaceRank(surface string) int {
	switch config.NormalizeToolSurface(surface) {
	case config.ToolSurfacePicoClaw:
		return 1
	case config.ToolSurfaceSimple:
		return 2
	case config.ToolSurfaceCodex:
		return 3
	default:
		return 0
	}
}

func applyToolAdaptationSurface(
	surface string,
	defs []providers.ToolDefinition,
) []providers.ToolDefinition {
	if len(defs) == 0 {
		return defs
	}
	defs = filterToolDefinitionsForAdaptationSurface(surface, defs)
	if config.NormalizeToolSurface(surface) != config.ToolSurfaceSimple {
		return defs
	}
	transformed, err := providercommon.TransformToolDefinitions(defs, providercommon.ToolSchemaTransformSimple)
	if err != nil {
		logger.WarnCF("agent", "Failed to apply simple tool surface", map[string]any{
			"error": err.Error(),
		})
		return defs
	}
	return transformed
}

func filterToolDefinitionsForAdaptationSurface(
	surface string,
	defs []providers.ToolDefinition,
) []providers.ToolDefinition {
	surface = config.NormalizeToolSurface(surface)
	filtered := make([]providers.ToolDefinition, 0, len(defs))
	for _, def := range defs {
		name := def.Function.Name
		if surface == config.ToolSurfaceCodex {
			if nativeToolReplacedByCodexSurface(name) {
				continue
			}
		} else if codexCompatibleToolName(name) {
			continue
		}
		filtered = append(filtered, def)
	}
	return filtered
}

func codexCompatibleToolName(name string) bool {
	switch name {
	case "apply_patch", "exec_command", "write_stdin", "update_plan", "view_image":
		return true
	default:
		return false
	}
}

func nativeToolReplacedByCodexSurface(name string) bool {
	switch name {
	case "exec", "write_file", "edit_file", "append_file", "load_image":
		return true
	default:
		return false
	}
}

func transientLLMRetryReason(err error) (string, bool) {
	if err == nil {
		return "", false
	}

	if failErr := providers.ClassifyError(err, "", ""); failErr != nil {
		switch failErr.Reason {
		case providers.FailoverTimeout:
			if failErr.Status >= 500 {
				return "server_error", true
			}
			return "timeout", true
		case providers.FailoverNetwork:
			return "network", true
		case providers.FailoverRateLimit, providers.FailoverOverloaded:
			return "rate_limit", true
		case providers.FailoverSafetyFilter:
			return "safety_filter", true
		}
	}

	errMsg := strings.ToLower(err.Error())
	if errors.Is(err, context.DeadlineExceeded) ||
		strings.Contains(errMsg, "deadline exceeded") ||
		strings.Contains(errMsg, "client.timeout") ||
		strings.Contains(errMsg, "timed out") ||
		strings.Contains(errMsg, "timeout exceeded") {
		return "timeout", true
	}

	if strings.Contains(errMsg, "connection reset") ||
		strings.Contains(errMsg, "connection refused") ||
		strings.Contains(errMsg, "broken pipe") ||
		strings.Contains(errMsg, "no such host") ||
		strings.Contains(errMsg, "network is unreachable") ||
		strings.Contains(errMsg, "read tcp") ||
		strings.Contains(errMsg, "write tcp") ||
		strings.Contains(errMsg, "eof") {
		return "network", true
	}

	return "", false
}
