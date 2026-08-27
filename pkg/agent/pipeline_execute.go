// PicoClaw - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/constants"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/utils"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func toolErrorSummary(result *tools.ToolResult) string {
	if result == nil || !result.IsError {
		return ""
	}
	content := strings.TrimSpace(result.ContentForLLM())
	if content == "" && result.Err != nil {
		content = strings.TrimSpace(result.Err.Error())
	}
	return utils.Truncate(content, 200)
}

func inferSkillNamesFromToolCall(ts *turnState, toolName string, toolArgs map[string]any) []string {
	if ts == nil || toolName != "read_file" {
		return nil
	}

	rawPath, ok := toolArgs["path"].(string)
	if !ok {
		return nil
	}
	path := strings.TrimSpace(rawPath)
	if path == "" {
		return nil
	}

	cleanPath := filepath.Clean(path)
	if !filepath.IsAbs(cleanPath) {
		cleanPath = filepath.Join(ts.workspace, cleanPath)
	}
	if filepath.Base(cleanPath) != "SKILL.md" {
		return nil
	}

	var roots []string
	if ts.agent != nil && ts.agent.ContextBuilder != nil {
		roots = ts.agent.ContextBuilder.skillRoots()
	}
	if len(roots) == 0 && strings.TrimSpace(ts.workspace) != "" {
		roots = []string{filepath.Join(ts.workspace, "skills")}
	}

	found := make(map[string]struct{})
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		rel, err := filepath.Rel(filepath.Clean(root), cleanPath)
		if err != nil {
			continue
		}
		if rel == "." || rel == "" || strings.HasPrefix(rel, "..") {
			continue
		}
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) != 2 || parts[1] != "SKILL.md" {
			continue
		}

		skillName := strings.TrimSpace(parts[0])
		if skillName == "" {
			continue
		}
		if ts.agent != nil && ts.agent.ContextBuilder != nil {
			if canonical, ok := ts.agent.ContextBuilder.ResolveSkillName(skillName); ok {
				skillName = canonical
			}
		}
		found[skillName] = struct{}{}
	}

	if len(found) == 0 {
		return nil
	}

	names := make([]string, 0, len(found))
	for skillName := range found {
		names = append(names, skillName)
	}
	sort.Strings(names)
	return names
}

// ExecuteTools executes the tool loop, handling BeforeTool/ApproveTool/AfterTool hooks,
// tool execution with async callbacks, media delivery, and steering injection.
// Returns ToolControl indicating what the coordinator should do next:
//   - ToolControlContinue: all tool results handled, pendingMessages or steering exists, continue turn
//   - ToolControlBreak: tool loop exited, proceed to coordinator's hardAbort/finalContent/finalize
func (p *Pipeline) ExecuteTools(
	ctx context.Context,
	turnCtx context.Context,
	ts *turnState,
	exec *turnExecution,
	iteration int,
) (ToolControl, error) {
	al := p.al
	normalizedToolCalls := exec.normalizedToolCalls

	ts.setPhase(TurnPhaseTools)
	messages := exec.messages
	defer func() {
		exec.messages = messages
	}()
	handledAttachments := make([]providers.Attachment, 0)

toolLoop:
	for i, tc := range normalizedToolCalls {
		if ts.hardAbortRequested() {
			exec.abortedByHardAbort = true
			return ToolControlBreak, nil
		}

		toolName := tc.Name
		toolArgs, detachErr := tools.DetachToolArguments(tc.Arguments)
		if detachErr != nil {
			return ToolControlBreak, fmt.Errorf("detach tool call %q arguments: %w", tc.ID, detachErr)
		}
		hookProvenance := tools.ToolHookProvenance{}
		if i < len(exec.toolCallProvenance) {
			hookProvenance = exec.toolCallProvenance[i]
		}
		skipToolCall := func(reason string) {
			exec.allResponsesHandled = false
			al.emitEvent(
				runtimeevents.KindAgentToolExecSkipped,
				ts.eventMeta("runTurn", "turn.tool.skipped"),
				ToolExecSkippedPayload{
					Tool:   toolName,
					Reason: reason,
				},
			)
			deniedMsg := providers.Message{
				Role:       "tool",
				Content:    reason,
				ToolCallID: tc.ID,
			}
			messages = append(messages, deniedMsg)
			if !ts.opts.NoHistory {
				ts.agent.Sessions.AddFullMessage(ts.sessionKey, deniedMsg)
				ts.recordPersistedMessage(deniedMsg)
			}
		}
		denyReservedModelTool := func() bool {
			if !isReservedModelToolName(toolName) {
				return false
			}
			skipToolCall(fmt.Sprintf(
				"Tool %q is unavailable until durable plan support is available.",
				toolName,
			))
			return true
		}
		denyByTurnProfile := func() bool {
			if turnProfileToolAllowed(ts.profile, toolName) {
				return false
			}
			skipToolCall(fmt.Sprintf(
				"Tool %q is not allowed by the active turn profile.",
				toolName,
			))
			return true
		}
		closeOutAuthorizationFailure := func(reason string) {
			skipToolCall(reason)
			for j := i + 1; j < len(normalizedToolCalls); j++ {
				skippedTC := normalizedToolCalls[j]
				al.emitEvent(
					runtimeevents.KindAgentToolExecSkipped,
					ts.eventMeta("runTurn", "turn.tool.skipped"),
					ToolExecSkippedPayload{Tool: skippedTC.Name, Reason: reason},
				)
				skippedMsg := providers.Message{
					Role: "tool", Content: reason, ToolCallID: skippedTC.ID,
				}
				messages = append(messages, skippedMsg)
				if !ts.opts.NoHistory {
					ts.agent.Sessions.AddFullMessage(ts.sessionKey, skippedMsg)
					ts.recordPersistedMessage(skippedMsg)
				}
			}
		}
		denyUnofferedModelTool := func() bool {
			if _, offered := exactOfferedToolDefinition(exec.providerToolDefs, toolName); offered {
				return false
			}
			skipToolCall(fmt.Sprintf("Tool %q was not offered to this model request.", toolName))
			return true
		}

		if denyReservedModelTool() {
			continue
		}
		if denyByTurnProfile() {
			continue
		}
		if denyUnofferedModelTool() {
			continue
		}

		if al.hooks != nil {
			toolReq, decision := al.hooks.BeforeTool(turnCtx, &ToolCallHookRequest{
				Meta:      ts.eventMeta("runTurn", "turn.tool.before"),
				Context:   cloneTurnContext(ts.turnCtx),
				Tool:      toolName,
				Arguments: toolArgs,
			})
			switch decision.normalizedAction() {
			case HookActionContinue, HookActionModify:
				if toolReq != nil {
					if nameErr := tools.ValidateToolPolicyName(toolReq.Tool); nameErr != nil {
						toolName = tc.Name
						skipToolCall("Tool hook returned an invalid tool name.")
						continue
					}
					toolName = toolReq.Tool
					toolArgs = toolReq.Arguments
					if provenance := toolReq.policyHookProvenance(); provenance.Trusted {
						hookProvenance = provenance
					}
				}
			case HookActionRespond:
				if toolReq != nil && toolReq.HookResult != nil {
					if nameErr := tools.ValidateToolPolicyName(toolReq.Tool); nameErr != nil {
						toolName = tc.Name
						skipToolCall("Tool hook returned an invalid tool name.")
						continue
					}
					toolName = toolReq.Tool
					var hookArgsErr error
					toolArgs, hookArgsErr = tools.DetachToolArguments(toolReq.Arguments)
					if hookArgsErr != nil {
						skipToolCall("Tool hook returned invalid arguments.")
						continue
					}
					if denyReservedModelTool() || denyByTurnProfile() || denyUnofferedModelTool() {
						continue
					}
					invocation, prepareErr := prepareAgentToolInvocation(exec, toolName, toolArgs)
					if prepareErr != nil {
						if errors.Is(prepareErr, workflows.ErrToolCallNotDispatched) {
							skipToolCall("Tool hook returned arguments outside the offered schema.")
							continue
						}
						closeOutAuthorizationFailure(toolPolicyUnavailableMessage)
						return ToolControlBreak, fmt.Errorf("prepare hook response authorization: %w", prepareErr)
					}
					authorization, policyErr := p.authorizeAgentTool(
						turnCtx,
						ts,
						tc,
						invocation,
						tools.ToolFulfillmentHookRespond,
						toolReq.policyHookProvenance(),
					)
					if policyErr != nil {
						outcome, reasonCode := toolPolicyFailureOutcome(policyErr)
						p.emitToolPolicyDecision(
							ts, invocation, tools.ToolFulfillmentHookRespond, outcome, reasonCode,
						)
						closeOutAuthorizationFailure(toolPolicyUnavailableMessage)
						return ToolControlBreak, policyErr
					}
					if !authorization.allowed {
						p.emitToolPolicyDecision(
							ts,
							invocation,
							tools.ToolFulfillmentHookRespond,
							ToolPolicyOutcomeDeny,
							authorization.reasonCode,
						)
						skipToolCall(authorization.message)
						continue
					}
					if _, claimErr := ts.agent.Tools.ClaimPrepared(turnCtx, invocation); claimErr != nil {
						outcome, reasonCode := toolPolicyFailureOutcome(claimErr)
						if outcome == ToolPolicyOutcomeError {
							reasonCode = "dispatch_stale"
						}
						p.emitToolPolicyDecision(
							ts, invocation, tools.ToolFulfillmentHookRespond, outcome, reasonCode,
						)
						closeOutAuthorizationFailure(toolPolicyUnavailableMessage)
						return ToolControlBreak, fmt.Errorf("claim hook response authorization: %w", claimErr)
					}
					if cancelErr := turnCancellationError(ctx, turnCtx, ts); cancelErr != nil {
						outcome, reasonCode := toolPolicyFailureOutcome(cancelErr)
						p.emitToolPolicyDecision(
							ts, invocation, tools.ToolFulfillmentHookRespond, outcome, reasonCode,
						)
						closeOutAuthorizationFailure(toolPolicyUnavailableMessage)
						return ToolControlBreak, cancelErr
					}
					p.emitToolPolicyDecision(
						ts,
						invocation,
						tools.ToolFulfillmentHookRespond,
						ToolPolicyOutcomeAllow,
						authorization.reasonCode,
					)
					if ts.hardAbortRequested() {
						exec.abortedByHardAbort = true
						return ToolControlBreak, nil
					}
					hookResult := toolReq.HookResult

					argsJSON, _ := json.Marshal(toolArgs)
					argsPreview := utils.Truncate(string(argsJSON), 200)
					logger.InfoCF("agent", fmt.Sprintf("Tool call (hook respond): %s(%s)", toolName, argsPreview),
						map[string]any{
							"agent_id":  ts.agent.ID,
							"tool":      toolName,
							"iteration": iteration,
						})

					al.emitEvent(
						runtimeevents.KindAgentToolExecStart,
						ts.eventMeta("runTurn", "turn.tool.start"),
						ToolExecStartPayload{
							Tool:      toolName,
							Arguments: cloneEventArguments(toolArgs),
						},
					)

					if shouldPublishToolFeedback(al.cfg, ts) && ts.channel != "pico" {
						toolFeedbackMaxLen := al.cfg.Agents.Defaults.GetToolFeedbackMaxArgsLength()
						toolFeedbackExplanation := toolFeedbackExplanationForToolCall(
							exec.response,
							tc,
							messages,
						)
						feedbackMsg := utils.FormatToolFeedbackMessage(
							toolName,
							toolFeedbackExplanation,
							toolFeedbackArgsPreview(toolArgs, toolFeedbackMaxLen),
						)
						fbCtx, fbCancel := context.WithTimeout(turnCtx, 3*time.Second)
						_ = al.bus.PublishOutbound(fbCtx, outboundMessageForTurnWithOptions(
							ts,
							feedbackMsg,
							outboundTurnMessageOptions{kind: messageKindToolFeedback},
						))
						fbCancel()
					}
					if cancelErr := turnCancellationError(ctx, turnCtx, ts); cancelErr != nil {
						closeOutAuthorizationFailure(toolPolicyUnavailableMessage)
						return ToolControlBreak, cancelErr
					}

					toolDuration := time.Duration(0)

					shouldSendForUser := !hookResult.Silent && hookResult.ForUser != "" &&
						(ts.opts.SendResponse || hookResult.ResponseHandled)
					if shouldSendForUser {
						outboundCtx := outboundContextFromInbound(
							ts.opts.Dispatch.InboundContext,
							ts.channel,
							ts.chatID,
							ts.opts.Dispatch.ReplyToMessageID(),
						)
						if outboundCtx.Raw == nil {
							outboundCtx.Raw = make(map[string]string, 1)
						}
						outboundCtx.Raw["is_tool_call"] = "true"
						al.bus.PublishOutbound(ctx, bus.OutboundMessage{
							Context: outboundCtx,
							Content: hookResult.ForUser,
						})
					}

					if len(hookResult.Media) > 0 && hookResult.ResponseHandled {
						parts := make([]bus.MediaPart, 0, len(hookResult.Media))
						for _, ref := range hookResult.Media {
							part := bus.MediaPart{Ref: ref}
							if al.mediaStore != nil {
								if _, meta, err := al.mediaStore.ResolveWithMeta(ref); err == nil {
									part.Filename = meta.Filename
									part.ContentType = meta.ContentType
									part.Type = inferMediaType(meta.Filename, meta.ContentType)
								}
							}
							parts = append(parts, part)
						}
						outboundMedia := bus.OutboundMediaMessage{
							Channel: ts.channel,
							ChatID:  ts.chatID,
							Context: outboundContextFromInbound(
								ts.opts.Dispatch.InboundContext,
								ts.channel,
								ts.chatID,
								ts.opts.Dispatch.ReplyToMessageID(),
							),
							AgentID:    ts.agent.ID,
							SessionKey: ts.sessionKey,
							Scope:      outboundScopeFromSessionScope(ts.opts.Dispatch.SessionScope),
							Parts:      parts,
						}
						if al.channelManager != nil && ts.channel != "" && !constants.IsInternalChannel(ts.channel) {
							if err := al.channelManager.SendMedia(ctx, outboundMedia); err != nil {
								logger.WarnCF("agent", "Failed to deliver hook media",
									map[string]any{
										"agent_id": ts.agent.ID,
										"tool":     toolName,
										"channel":  ts.channel,
										"chat_id":  ts.chatID,
										"error":    err.Error(),
									})
								hookResult.IsError = true
								hookResult.ForLLM = fmt.Sprintf("failed to deliver attachment: %v", err)
							} else {
								handledAttachments = append(
									handledAttachments,
									buildProviderAttachments(al.mediaStore, hookResult.Media)...,
								)
							}
						} else if al.bus != nil {
							al.bus.PublishOutboundMedia(ctx, outboundMedia)
							hookResult.ResponseHandled = false
						}
					}

					if !hookResult.ResponseHandled {
						exec.allResponsesHandled = false
					}

					contentForLLM := hookResult.ContentForLLM()
					if al.cfg.Tools.IsFilterSensitiveDataEnabled() {
						contentForLLM = al.cfg.FilterSensitiveData(contentForLLM)
					}

					var toolResultMedia []string
					if len(hookResult.Media) > 0 && !hookResult.ResponseHandled {
						hookResult.ArtifactTags = buildArtifactTags(al.mediaStore, hookResult.Media)
						contentForLLM = hookResult.ContentForLLM()
						if al.cfg.Tools.IsFilterSensitiveDataEnabled() {
							contentForLLM = al.cfg.FilterSensitiveData(contentForLLM)
						}
						toolResultMedia = append(toolResultMedia, hookResult.Media...)
					}
					toolResultMsg := toolResultPromptMessage(contentForLLM, tc.ID, toolResultMedia)

					al.emitEvent(
						runtimeevents.KindAgentToolExecEnd,
						ts.eventMeta("runTurn", "turn.tool.end"),
						ToolExecEndPayload{
							Tool:       toolName,
							Duration:   toolDuration,
							ForLLMLen:  len(contentForLLM),
							ForUserLen: len(hookResult.ForUser),
							IsError:    hookResult.IsError,
							Async:      hookResult.Async,
						},
					)
					ts.recordToolExecution(
						toolName,
						!hookResult.IsError,
						toolErrorSummary(hookResult),
						inferSkillNamesFromToolCall(ts, toolName, toolArgs),
					)

					messages = append(messages, toolResultMsg)
					if !ts.opts.NoHistory {
						ts.agent.Sessions.AddFullMessage(ts.sessionKey, toolResultMsg)
						ts.recordPersistedMessage(toolResultMsg)
						ts.ingestMessage(turnCtx, al, toolResultMsg)
					}

					if steerMsgs := al.dequeueSteeringMessagesForScope(ts.sessionKey); len(steerMsgs) > 0 {
						exec.pendingMessages = append(exec.pendingMessages, steerMsgs...)
					}

					skipReason := ""
					skipMessage := ""
					if len(exec.pendingMessages) > 0 {
						skipReason = "queued user steering message"
						skipMessage = "Skipped due to queued user message."
					} else if gracefulPending, _ := ts.gracefulInterruptRequested(); gracefulPending {
						skipReason = "graceful interrupt requested"
						skipMessage = "Skipped due to graceful interrupt."
					}

					if skipReason != "" {
						remaining := len(normalizedToolCalls) - i - 1
						if remaining > 0 {
							logger.InfoCF("agent", "Turn checkpoint: skipping remaining tools after hook respond",
								map[string]any{
									"agent_id":  ts.agent.ID,
									"completed": i + 1,
									"skipped":   remaining,
									"reason":    skipReason,
								})
							for j := i + 1; j < len(normalizedToolCalls); j++ {
								skippedTC := normalizedToolCalls[j]
								al.emitEvent(
									runtimeevents.KindAgentToolExecSkipped,
									ts.eventMeta("runTurn", "turn.tool.skipped"),
									ToolExecSkippedPayload{
										Tool:   skippedTC.Name,
										Reason: skipReason,
									},
								)
								skippedMsg := providers.Message{
									Role:       "tool",
									Content:    skipMessage,
									ToolCallID: skippedTC.ID,
								}
								messages = append(messages, skippedMsg)
								if !ts.opts.NoHistory {
									ts.agent.Sessions.AddFullMessage(ts.sessionKey, skippedMsg)
									ts.recordPersistedMessage(skippedMsg)
								}
							}
						}
						break toolLoop
					}

					if trackedResult, ok := al.dequeueTrackedSubagentResult(ts); ok {
						exec.allResponsesHandled = false
						exec.pendingResultMessages = append(
							exec.pendingResultMessages,
							trackedResult,
						)
					}

					if ts.pendingResults != nil {
						select {
						case result, ok := <-ts.pendingResults:
							if ok && result != nil && result.ForLLM != "" {
								content := al.cfg.FilterSensitiveData(result.ForLLM)
								msg := subTurnResultPromptMessage(content)
								messages = append(messages, msg)
								if !ts.opts.NoHistory {
									ts.agent.Sessions.AddFullMessage(ts.sessionKey, msg)
								}
							}
						default:
						}
					}

					continue
				}
				skipToolCall("Tool hook returned no response result.")
				logger.WarnCF("agent", "Hook returned respond action but no HookResult provided",
					map[string]any{
						"agent_id": ts.agent.ID,
						"tool":     toolName,
						"action":   "respond",
					})
				continue
			case HookActionDenyTool:
				skipToolCall(hookDeniedToolContent(
					"Tool execution denied by hook",
					decision.Reason,
				))
				continue
			case HookActionAbortTurn:
				exec.abortedByHook = true
				return ToolControlBreak, nil
			case HookActionHardAbort:
				_ = ts.requestHardAbort()
				exec.abortedByHardAbort = true
				return ToolControlBreak, nil
			}
		}

		if denyReservedModelTool() {
			continue
		}
		if denyByTurnProfile() {
			continue
		}
		if denyUnofferedModelTool() {
			continue
		}

		invocation, prepareErr := prepareAgentToolInvocation(exec, toolName, toolArgs)
		if prepareErr != nil {
			if errors.Is(prepareErr, workflows.ErrToolCallNotDispatched) {
				skipToolCall("Tool arguments do not match the offered schema.")
				continue
			}
			closeOutAuthorizationFailure(toolPolicyUnavailableMessage)
			return ToolControlBreak, fmt.Errorf("prepare tool authorization: %w", prepareErr)
		}
		authorization, policyErr := p.authorizeAgentTool(
			turnCtx,
			ts,
			tc,
			invocation,
			tools.ToolFulfillmentExecute,
			hookProvenance,
		)
		if policyErr != nil {
			outcome, reasonCode := toolPolicyFailureOutcome(policyErr)
			p.emitToolPolicyDecision(
				ts, invocation, tools.ToolFulfillmentExecute, outcome, reasonCode,
			)
			closeOutAuthorizationFailure(toolPolicyUnavailableMessage)
			return ToolControlBreak, policyErr
		}
		if !authorization.allowed {
			p.emitToolPolicyDecision(
				ts,
				invocation,
				tools.ToolFulfillmentExecute,
				ToolPolicyOutcomeDeny,
				authorization.reasonCode,
			)
			skipToolCall(authorization.message)
			continue
		}
		claimedInvocation, claimErr := ts.agent.Tools.ClaimPrepared(turnCtx, invocation)
		if claimErr != nil {
			outcome, reasonCode := toolPolicyFailureOutcome(claimErr)
			if outcome == ToolPolicyOutcomeError {
				reasonCode = "dispatch_stale"
			}
			p.emitToolPolicyDecision(
				ts, invocation, tools.ToolFulfillmentExecute, outcome, reasonCode,
			)
			closeOutAuthorizationFailure(toolPolicyUnavailableMessage)
			return ToolControlBreak, fmt.Errorf("claim tool authorization: %w", claimErr)
		}
		if cancelErr := turnCancellationError(ctx, turnCtx, ts); cancelErr != nil {
			outcome, reasonCode := toolPolicyFailureOutcome(cancelErr)
			p.emitToolPolicyDecision(
				ts, invocation, tools.ToolFulfillmentExecute, outcome, reasonCode,
			)
			closeOutAuthorizationFailure(toolPolicyUnavailableMessage)
			return ToolControlBreak, cancelErr
		}
		p.emitToolPolicyDecision(
			ts,
			invocation,
			tools.ToolFulfillmentExecute,
			ToolPolicyOutcomeAllow,
			authorization.reasonCode,
		)
		if ts.hardAbortRequested() {
			exec.abortedByHardAbort = true
			return ToolControlBreak, nil
		}

		argsJSON, _ := json.Marshal(toolArgs)
		argsPreview := utils.Truncate(string(argsJSON), 200)
		logger.InfoCF("agent", fmt.Sprintf("Tool call: %s(%s)", toolName, argsPreview),
			map[string]any{
				"agent_id":  ts.agent.ID,
				"tool":      toolName,
				"iteration": iteration,
			})
		al.emitEvent(
			runtimeevents.KindAgentToolExecStart,
			ts.eventMeta("runTurn", "turn.tool.start"),
			ToolExecStartPayload{
				Tool:      toolName,
				Arguments: cloneEventArguments(toolArgs),
			},
		)

		if shouldPublishToolFeedback(al.cfg, ts) && ts.channel != "pico" {
			toolFeedbackMaxLen := al.cfg.Agents.Defaults.GetToolFeedbackMaxArgsLength()
			toolFeedbackExplanation := toolFeedbackExplanationForToolCall(
				exec.response,
				tc,
				messages,
			)
			feedbackMsg := utils.FormatToolFeedbackMessage(
				toolName,
				toolFeedbackExplanation,
				toolFeedbackArgsPreview(toolArgs, toolFeedbackMaxLen),
			)
			fbCtx, fbCancel := context.WithTimeout(turnCtx, 3*time.Second)
			_ = al.bus.PublishOutbound(fbCtx, outboundMessageForTurnWithOptions(
				ts,
				feedbackMsg,
				outboundTurnMessageOptions{kind: messageKindToolFeedback},
			))
			fbCancel()
		}
		if cancelErr := turnCancellationError(ctx, turnCtx, ts); cancelErr != nil {
			closeOutAuthorizationFailure(toolPolicyUnavailableMessage)
			return ToolControlBreak, cancelErr
		}

		toolCallID := tc.ID
		asyncToolName := toolName
		var (
			trackedSpawnRoute    trackedSubagentResultRoute
			trackedSpawnRouteErr error
		)
		asyncCallback := func(callbackCtx context.Context, result *tools.ToolResult) {
			if tools.IsTrackedSpawnCompletionCallback(callbackCtx) {
				completion, trackedSpawn := tools.SubagentCompletionFromContext(callbackCtx)
				if !trackedSpawn {
					al.emitEvent(
						runtimeevents.KindAgentSubTurnOrphan,
						ts.eventMeta("runTurn", "subturn.orphan"),
						SubTurnOrphanPayload{
							ParentTurnID: ts.turnID,
							TaskID:       completion.TaskID,
							Reason:       "missing_task_identity",
						},
					)
					return
				}
				if trackedSpawnRouteErr != nil {
					logger.WarnCF(
						"agent",
						"Tracked spawn completion has no valid parent route",
						map[string]any{
							"task_id": completion.TaskID,
							"turn_id": ts.turnID,
							"reason":  "invalid_parent_route",
						},
					)
					al.emitEvent(
						runtimeevents.KindAgentSubTurnOrphan,
						ts.eventMeta("runTurn", "subturn.orphan"),
						SubTurnOrphanPayload{
							ParentTurnID: ts.turnID,
							TaskID:       completion.TaskID,
							Reason:       "invalid_parent_route",
						},
					)
					return
				}
				al.acceptTrackedSubagentResult(trackedSpawnRoute, completion, result)
				return
			}

			if !result.Silent && result.ForUser != "" {
				outCtx, outCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer outCancel()
				_ = al.bus.PublishOutbound(outCtx, outboundMessageForTurn(ts, result.ForUser))
			}

			content := result.ContentForLLM()
			if content == "" {
				return
			}

			content = al.cfg.FilterSensitiveData(content)

			logger.InfoCF("agent", "Async tool completed, publishing result",
				map[string]any{
					"tool":        asyncToolName,
					"content_len": len(content),
					"channel":     ts.channel,
				})
			al.emitEvent(
				runtimeevents.KindAgentFollowUpQueued,
				ts.scope.meta(iteration, "runTurn", "turn.follow_up.queued"),
				FollowUpQueuedPayload{
					SourceTool: asyncToolName,
					ContentLen: len(content),
				},
			)
			pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer pubCancel()
			_ = al.bus.PublishInbound(pubCtx, bus.InboundMessage{
				Context: bus.InboundContext{
					Channel:  "system",
					ChatID:   fmt.Sprintf("%s:%s", ts.channel, ts.chatID),
					ChatType: "direct",
					SenderID: fmt.Sprintf("async:%s", asyncToolName),
				},
				Content: content,
			})
		}

		toolStart := time.Now()
		execCtx := tools.WithToolInboundContext(
			turnCtx,
			ts.channel,
			ts.chatID,
			ts.opts.Dispatch.MessageID(),
			ts.opts.Dispatch.ReplyToMessageID(),
		)
		execCtx = tools.WithToolTurnUXContext(execCtx, ts.turnUXID)
		if ts.opts.Dispatch.InboundContext != nil {
			execCtx = tools.WithToolTopicContext(execCtx, ts.opts.Dispatch.InboundContext.TopicID)
		}
		execCtx = tools.WithToolSessionContext(
			execCtx,
			ts.agent.ID,
			ts.sessionKey,
			ts.opts.Dispatch.SessionScope,
		)
		execCtx = tools.WithTrackedSpawnAdmissionObserver(execCtx, func() error {
			trackedSpawnRoute, trackedSpawnRouteErr = snapshotTrackedSubagentResultRoute(ts)
			if trackedSpawnRouteErr != nil {
				return trackedSpawnRouteErr
			}
			al.watchTrackedSubagentResultRoute(trackedSpawnRoute)
			return nil
		})
		toolResult, dispatchErr := ts.agent.Tools.DispatchClaimed(
			execCtx,
			claimedInvocation,
			ts.channel,
			ts.chatID,
			asyncCallback,
			false,
		)
		if dispatchErr != nil {
			closeOutAuthorizationFailure(toolPolicyUnavailableMessage)
			return ToolControlBreak, fmt.Errorf("dispatch authorized tool: %w", dispatchErr)
		}
		toolDuration := time.Since(toolStart)

		if ts.hardAbortRequested() {
			exec.abortedByHardAbort = true
			return ToolControlBreak, nil
		}

		if al.hooks != nil {
			toolResp, decision := al.hooks.AfterTool(turnCtx, &ToolResultHookResponse{
				Meta:      ts.eventMeta("runTurn", "turn.tool.after"),
				Context:   cloneTurnContext(ts.turnCtx),
				Tool:      toolName,
				Arguments: toolArgs,
				Result:    toolResult,
				Duration:  toolDuration,
			})
			switch decision.normalizedAction() {
			case HookActionContinue, HookActionModify:
				if toolResp != nil {
					if toolResp.Result != nil {
						toolResult = toolResp.Result
					}
				}
			case HookActionAbortTurn:
				exec.abortedByHook = true
				return ToolControlBreak, nil
			case HookActionHardAbort:
				_ = ts.requestHardAbort()
				exec.abortedByHardAbort = true
				return ToolControlBreak, nil
			}
		}

		if toolResult == nil {
			toolResult = tools.ErrorResult("hook returned nil tool result")
		}

		if len(toolResult.Media) > 0 && toolResult.ResponseHandled {
			parts := make([]bus.MediaPart, 0, len(toolResult.Media))
			for _, ref := range toolResult.Media {
				part := bus.MediaPart{Ref: ref}
				if al.mediaStore != nil {
					if _, meta, err := al.mediaStore.ResolveWithMeta(ref); err == nil {
						part.Filename = meta.Filename
						part.ContentType = meta.ContentType
						part.Type = inferMediaType(meta.Filename, meta.ContentType)
					}
				}
				parts = append(parts, part)
			}
			outboundMedia := bus.OutboundMediaMessage{
				Channel: ts.channel,
				ChatID:  ts.chatID,
				Context: outboundContextFromInbound(
					ts.opts.Dispatch.InboundContext,
					ts.channel,
					ts.chatID,
					ts.opts.Dispatch.ReplyToMessageID(),
				),
				AgentID:    ts.agent.ID,
				SessionKey: ts.sessionKey,
				Scope:      outboundScopeFromSessionScope(ts.opts.Dispatch.SessionScope),
				Parts:      parts,
			}
			if al.channelManager != nil && ts.channel != "" && !constants.IsInternalChannel(ts.channel) {
				if err := al.channelManager.SendMedia(ctx, outboundMedia); err != nil {
					logger.WarnCF("agent", "Failed to deliver handled tool media",
						map[string]any{
							"agent_id": ts.agent.ID,
							"tool":     toolName,
							"channel":  ts.channel,
							"chat_id":  ts.chatID,
							"error":    err.Error(),
						})
					toolResult = tools.ErrorResult(fmt.Sprintf("failed to deliver attachment: %v", err)).WithError(err)
				} else {
					handledAttachments = append(
						handledAttachments,
						buildProviderAttachments(al.mediaStore, toolResult.Media)...,
					)
				}
			} else if al.bus != nil {
				al.bus.PublishOutboundMedia(ctx, outboundMedia)
				toolResult.ResponseHandled = false
			}
		}

		if len(toolResult.Media) > 0 && !toolResult.ResponseHandled {
			toolResult.ArtifactTags = buildArtifactTags(al.mediaStore, toolResult.Media)
		}

		if !toolResult.ResponseHandled {
			exec.allResponsesHandled = false
		}

		shouldSendForUser := !toolResult.Silent &&
			toolResult.ForUser != "" &&
			(ts.opts.SendResponse || toolResult.ResponseHandled)
		if shouldSendForUser {
			al.bus.PublishOutbound(ctx, outboundMessageForTurn(ts, toolResult.ForUser))
			logger.DebugCF("agent", "Sent tool result to user",
				map[string]any{
					"tool":        toolName,
					"content_len": len(toolResult.ForUser),
				})
		}
		contentForLLM := toolResult.ContentForLLM()

		if al.cfg.Tools.IsFilterSensitiveDataEnabled() {
			contentForLLM = al.cfg.FilterSensitiveData(contentForLLM)
		}

		var toolResultMedia []string
		if len(toolResult.Media) > 0 && !toolResult.ResponseHandled {
			toolResultMedia = append(toolResultMedia, toolResult.Media...)
		}
		toolResultMsg := toolResultPromptMessage(contentForLLM, toolCallID, toolResultMedia)
		al.emitEvent(
			runtimeevents.KindAgentToolExecEnd,
			ts.eventMeta("runTurn", "turn.tool.end"),
			ToolExecEndPayload{
				Tool:       toolName,
				Duration:   toolDuration,
				ForLLMLen:  len(contentForLLM),
				ForUserLen: len(toolResult.ForUser),
				IsError:    toolResult.IsError,
				Async:      toolResult.Async,
			},
		)
		ts.recordToolExecution(
			toolName,
			!toolResult.IsError,
			toolErrorSummary(toolResult),
			inferSkillNamesFromToolCall(ts, toolName, toolArgs),
		)
		observeToolAdaptationOutcome(al.cfg, ts, exec, toolName, toolResult, toolDuration)
		messages = append(messages, toolResultMsg)
		if !ts.opts.NoHistory {
			ts.agent.Sessions.AddFullMessage(ts.sessionKey, toolResultMsg)
			ts.recordPersistedMessage(toolResultMsg)
			ts.ingestMessage(turnCtx, al, toolResultMsg)
		}

		if steerMsgs := al.dequeueSteeringMessagesForScope(ts.sessionKey); len(steerMsgs) > 0 {
			exec.pendingMessages = append(exec.pendingMessages, steerMsgs...)
		}

		skipReason := ""
		skipMessage := ""
		if len(exec.pendingMessages) > 0 {
			skipReason = "queued user steering message"
			skipMessage = "Skipped due to queued user message."
		} else if gracefulPending, _ := ts.gracefulInterruptRequested(); gracefulPending {
			skipReason = "graceful interrupt requested"
			skipMessage = "Skipped due to graceful interrupt."
		}

		if skipReason != "" {
			remaining := len(normalizedToolCalls) - i - 1
			if remaining > 0 {
				logger.InfoCF("agent", "Turn checkpoint: skipping remaining tools",
					map[string]any{
						"agent_id":  ts.agent.ID,
						"completed": i + 1,
						"skipped":   remaining,
						"reason":    skipReason,
					})
				for j := i + 1; j < len(normalizedToolCalls); j++ {
					skippedTC := normalizedToolCalls[j]
					al.emitEvent(
						runtimeevents.KindAgentToolExecSkipped,
						ts.eventMeta("runTurn", "turn.tool.skipped"),
						ToolExecSkippedPayload{
							Tool:   skippedTC.Name,
							Reason: skipReason,
						},
					)
					skippedMsg := providers.Message{
						Role:       "tool",
						Content:    skipMessage,
						ToolCallID: skippedTC.ID,
					}
					messages = append(messages, skippedMsg)
					if !ts.opts.NoHistory {
						ts.agent.Sessions.AddFullMessage(ts.sessionKey, skippedMsg)
						ts.recordPersistedMessage(skippedMsg)
					}
				}
			}
			break toolLoop
		}

		if trackedResult, ok := al.dequeueTrackedSubagentResult(ts); ok {
			exec.allResponsesHandled = false
			exec.pendingResultMessages = append(exec.pendingResultMessages, trackedResult)
		}

		if ts.pendingResults != nil {
			select {
			case result, ok := <-ts.pendingResults:
				if ok && result != nil && result.ForLLM != "" {
					content := al.cfg.FilterSensitiveData(result.ForLLM)
					msg := subTurnResultPromptMessage(content)
					messages = append(messages, msg)
					if !ts.opts.NoHistory {
						ts.agent.Sessions.AddFullMessage(ts.sessionKey, msg)
					}
				}
			default:
			}
		}
	}

	exec.messages = messages

	// Continue if pending steering exists (regardless of allResponsesHandled).
	// This covers the case where tools were partially executed and skipped due to steering,
	// but one tool had ResponseHandled=false (so allResponsesHandled=false).
	if len(exec.pendingMessages) > 0 {
		logger.InfoCF("agent", "Pending steering after partial tool execution; continuing turn",
			map[string]any{
				"agent_id":            ts.agent.ID,
				"pending_count":       len(exec.pendingMessages),
				"allResponsesHandled": exec.allResponsesHandled,
			})
		exec.allResponsesHandled = false
		return ToolControlContinue, nil
	}

	// Poll for newly arrived steering
	if steerMsgs := al.dequeueSteeringMessagesForScope(ts.sessionKey); len(steerMsgs) > 0 {
		logger.InfoCF("agent", "Steering arrived after tool delivery; continuing turn",
			map[string]any{
				"agent_id":       ts.agent.ID,
				"steering_count": len(steerMsgs),
			})
		exec.pendingMessages = append(exec.pendingMessages, steerMsgs...)
		exec.allResponsesHandled = false
		return ToolControlContinue, nil
	}

	// No pending steering: finalize or break depending on allResponsesHandled
	if exec.allResponsesHandled {
		summaryMsg := providers.Message{
			Role:        "assistant",
			Content:     handledToolResponseSummary,
			Attachments: append([]providers.Attachment(nil), handledAttachments...),
		}
		if !ts.opts.NoHistory {
			ts.agent.Sessions.AddFullMessage(ts.sessionKey, summaryMsg)
			ts.recordPersistedMessage(summaryMsg)
			ts.ingestMessage(turnCtx, al, summaryMsg)
			if err := ts.agent.Sessions.Save(ts.sessionKey); err != nil {
				logger.WarnCF("agent", "Failed to save session after tool delivery",
					map[string]any{
						"agent_id": ts.agent.ID,
						"error":    err.Error(),
					})
			}
		}
		if !ts.opts.NoHistory && ts.opts.EnableSummary {
			al.contextManager.Compact(turnCtx, &CompactRequest{
				SessionKey: ts.sessionKey,
				Reason:     ContextCompressReasonSummarize,
				Budget:     ts.agent.ContextWindow,
			})
		}
		ts.setPhase(TurnPhaseCompleted)
		ts.setFinalContent("")
		if al.channelManager != nil && ts.channel != "" {
			al.channelManager.DismissToolFeedback(ctx, ts.channel, ts.chatID, ts.opts.InboundContext)
		}
		logger.InfoCF("agent", "Tool output satisfied delivery; ending turn without follow-up LLM",
			map[string]any{
				"agent_id":   ts.agent.ID,
				"iteration":  iteration,
				"tool_count": len(normalizedToolCalls),
			})
		return ToolControlBreak, nil
	}

	// allResponsesHandled=false and no pending steering: continue so coordinator
	// makes another LLM call. The tool result is in messages and the LLM will
	// return it as finalContent in the next iteration.
	ts.agent.Tools.TickTTL()
	logger.DebugCF("agent", "TTL tick after tool execution", map[string]any{
		"agent_id": ts.agent.ID, "iteration": iteration,
	})
	return ToolControlContinue, nil
}
