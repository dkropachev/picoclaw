// PicoClaw - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/accountrouter"
	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/modelrouter"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func (al *AgentLoop) runTurn(
	ctx context.Context,
	ts *turnState,
	pipeline *Pipeline,
) (turnResult, error) {
	turnCtx, turnCancel := context.WithCancel(ctx)
	defer turnCancel()
	ts.setTurnCancel(turnCancel)

	// Inject turnState and AgentLoop into context so tools (e.g. spawn) can retrieve them.
	turnCtx = withTurnState(turnCtx, ts)
	turnCtx = WithAgentLoop(turnCtx, al)

	if !al.registerActiveTurn(ts) {
		return turnResult{}, fmt.Errorf(
			"session %q is already owned by another turn",
			ts.sessionKey,
		)
	}
	defer al.clearActiveTurn(ts)
	defer al.releaseGitWorkspacesForTurn(turnCtx, ts)

	if al.takePendingStop(ts.sessionKey) {
		_ = ts.requestHardAbort()
	}

	turnStatus := TurnEndStatusCompleted
	defer func() {
		attemptedSkills := ts.attemptedSkillsSnapshot()
		skillContextSnapshots := ts.skillContextSnapshotsSnapshot()
		finalSuccessfulPath := []string(nil)
		if turnStatus == TurnEndStatusCompleted {
			if latest := ts.latestSkillContextSnapshot(); len(latest) > 0 {
				finalSuccessfulPath = latest
			} else {
				finalSuccessfulPath = append([]string(nil), attemptedSkills...)
			}
		}
		al.emitEvent(
			runtimeevents.KindAgentTurnEnd,
			ts.eventMeta("runTurn", "turn.end"),
			TurnEndPayload{
				Status:                turnStatus,
				Workspace:             ts.workspace,
				Iterations:            ts.currentIteration(),
				Duration:              time.Since(ts.startedAt),
				FinalContentLen:       ts.finalContentLen(),
				UserMessage:           ts.userMessage,
				FinalContent:          ts.finalContentSnapshot(),
				ActiveSkills:          append([]string(nil), ts.activeSkills...),
				AttemptedSkills:       attemptedSkills,
				FinalSuccessfulPath:   finalSuccessfulPath,
				SkillContextSnapshots: skillContextSnapshots,
				ToolKinds:             ts.toolKindsSnapshot(),
				ToolExecutions:        ts.toolExecutionsSnapshot(),
			},
		)
	}()

	if ts.hardAbortRequested() {
		turnStatus = TurnEndStatusAborted
		return al.abortTurn(ts)
	}

	al.emitEvent(
		runtimeevents.KindAgentTurnStart,
		ts.eventMeta("runTurn", "turn.start"),
		TurnStartPayload{
			UserMessage: ts.userMessage,
			MediaCount:  len(ts.media),
		},
	)

	// SetupTurn extracts the one-time initialization phase.
	exec, err := pipeline.SetupTurn(turnCtx, ts)
	if err != nil {
		return turnResult{}, err
	}

	// Convenience references to exec fields used throughout the turn loop.
	messages := exec.messages
	pendingMessages := exec.pendingMessages
	maxMediaSize := pipeline.Cfg.Agents.Defaults.GetMaxMediaSize()
	finalContent := exec.finalContent

	for ts.currentIteration() < ts.agent.MaxIterations || len(exec.pendingMessages) > 0 || func() bool {
		graceful, _ := ts.gracefulInterruptRequested()
		return graceful
	}() {
		if ts.hardAbortRequested() {
			turnStatus = TurnEndStatusAborted
			return al.abortTurn(ts)
		}

		iteration := ts.currentIteration() + 1
		ts.setIteration(iteration)
		ts.setPhase(TurnPhaseRunning)

		if iteration > 1 {
			// For subsequent iterations, read from exec.pendingMessages which
			// is where ExecuteTools (or initial poll) deposits steering.
			// We do NOT call dequeueSteeringMessagesForScope here because
			// steering was already consumed from al.steering by ExecuteTools.
			if len(exec.pendingMessages) > 0 {
				pendingMessages = append(pendingMessages, exec.pendingMessages...)
				exec.pendingMessages = nil
			}
		} else if !ts.opts.SkipInitialSteeringPoll {
			if steerMsgs := al.dequeueSteeringMessagesForScopeWithFallback(ts.sessionKey); len(steerMsgs) > 0 {
				pendingMessages = append(pendingMessages, steerMsgs...)
			}
		}

		// Check if parent turn has ended (SubTurn support from HEAD)
		if ts.parentTurnState != nil && ts.IsParentEnded() {
			if !ts.critical {
				logger.InfoCF(
					"agent",
					"Parent turn ended, non-critical SubTurn exiting gracefully",
					map[string]any{
						"agent_id":  ts.agentID,
						"iteration": iteration,
						"turn_id":   ts.turnID,
					},
				)
				break
			}
			logger.InfoCF(
				"agent",
				"Parent turn ended, critical SubTurn continues running",
				map[string]any{
					"agent_id":  ts.agentID,
					"iteration": iteration,
					"turn_id":   ts.turnID,
				},
			)
		}

		// Poll for pending SubTurn results (from HEAD)
		if ts.pendingResults != nil {
			select {
			case result, ok := <-ts.pendingResults:
				if ok && result != nil && result.ForLLM != "" {
					content := al.cfg.FilterSensitiveData(result.ForLLM)
					msg := subTurnResultPromptMessage(content)
					pendingMessages = append(pendingMessages, msg)
				}
			default:
				// No results available
			}
		}

		// Inject pending steering messages
		if len(pendingMessages) > 0 {
			resolvedPending := resolveMediaRefs(pendingMessages, al.mediaStore, maxMediaSize, 0)
			totalContentLen := 0
			for i, pm := range pendingMessages {
				messages = append(messages, resolvedPending[i])
				totalContentLen += len(pm.Content)
				if !ts.opts.NoHistory {
					ts.agent.Sessions.AddFullMessage(ts.sessionKey, pm)
					ts.recordPersistedMessage(pm)
					ts.ingestMessage(turnCtx, al, pm)
				}
				logger.InfoCF("agent", "Injected steering message into context",
					map[string]any{
						"agent_id":    ts.agent.ID,
						"iteration":   iteration,
						"content_len": len(pm.Content),
						"media_count": len(pm.Media),
					})
			}
			al.emitEvent(
				runtimeevents.KindAgentSteeringInjected,
				ts.eventMeta("runTurn", "turn.steering.injected"),
				SteeringInjectedPayload{
					Count:           len(pendingMessages),
					TotalContentLen: totalContentLen,
				},
			)
			// Clear exec.pendingMessages after injection so InitialSteeringMessages
			// are not re-injected on subsequent iterations (Issue 2 fix).
			exec.pendingMessages = nil
		}
		// Always sync messages into exec.messages so CallLLM sees the updated state
		exec.messages = messages

		logger.DebugCF("agent", "LLM iteration",
			map[string]any{
				"agent_id":  ts.agent.ID,
				"iteration": iteration,
				"max":       ts.agent.MaxIterations,
			})

		// Execute LLM call via Pipeline
		ts.setPhase(TurnPhaseRunning)
		ctrl, callErr := pipeline.CallLLM(ctx, turnCtx, ts, exec, iteration)
		if callErr != nil {
			turnStatus = TurnEndStatusError
			return turnResult{}, callErr
		}
		messages = exec.messages
		pendingMessages = exec.pendingMessages
		finalContent = exec.finalContent

		switch ctrl {
		case ControlContinue:
			continue
		case ControlBreak:
			// Hard abort: delegate to abortTurn (sets TurnEndStatusAborted)
			if exec.abortedByHardAbort {
				turnStatus = TurnEndStatusAborted
				return al.abortTurn(ts)
			}
			// Hook abort (HookActionAbortTurn): sets TurnEndStatusError, returns error
			if exec.abortedByHook {
				turnStatus = TurnEndStatusError
				return turnResult{}, fmt.Errorf("hook requested turn abort")
			}
			// Ensure empty response falls back to DefaultResponse
			if finalContent == "" {
				finalContent = ts.opts.DefaultResponse
			}
			result, finalizeErr := pipeline.Finalize(
				ctx,
				turnCtx,
				ts,
				exec,
				turnStatus,
				finalContent,
			)
			if finalizeErr != nil {
				turnStatus = TurnEndStatusError
			}
			return result, finalizeErr
		case ControlToolLoop:
			// Execute tools via Pipeline
			toolCtrl := pipeline.ExecuteTools(ctx, turnCtx, ts, exec, iteration)
			switch toolCtrl {
			case ToolControlContinue:
				// Re-read exec.messages since ExecuteTools may have updated it
				// (added tool results/skipped messages) before returning ControlContinue
				messages = exec.messages
				continue
			case ToolControlBreak:
				// Hard abort: delegate to abortTurn (sets TurnEndStatusAborted)
				if exec.abortedByHardAbort {
					turnStatus = TurnEndStatusAborted
					return al.abortTurn(ts)
				}
				// Hook abort (HookActionAbortTurn): sets TurnEndStatusError, returns error
				if exec.abortedByHook {
					turnStatus = TurnEndStatusError
					return turnResult{}, fmt.Errorf("hook requested turn abort")
				}
				// ExecuteTools returned ControlBreak:
				// - allResponsesHandled=true: finalize without DefaultResponse (exec.finalContent empty)
				// - allResponsesHandled=false: coordinator applies DefaultResponse before finalize
				if exec.allResponsesHandled {
					finalContent = ""
				}
				result, finalizeErr := pipeline.Finalize(
					ctx,
					turnCtx,
					ts,
					exec,
					turnStatus,
					finalContent,
				)
				if finalizeErr != nil {
					turnStatus = TurnEndStatusError
				}
				return result, finalizeErr
			}
		}
	}

	if ts.hardAbortRequested() {
		turnStatus = TurnEndStatusAborted
		return al.abortTurn(ts)
	}

	if finalContent == "" {
		if ts.currentIteration() >= ts.agent.MaxIterations && ts.agent.MaxIterations > 0 {
			finalContent = toolLimitResponse
		} else {
			finalContent = ts.opts.DefaultResponse
		}
	}

	// Check hard abort before finalizing (may have been set during tool execution)
	if ts.hardAbortRequested() {
		turnStatus = TurnEndStatusAborted
		return al.abortTurn(ts)
	}

	result, err := pipeline.Finalize(ctx, turnCtx, ts, exec, turnStatus, finalContent)
	if err != nil {
		turnStatus = TurnEndStatusError
	}
	return result, err
}

func (al *AgentLoop) abortTurn(ts *turnState) (turnResult, error) {
	ts.setPhase(TurnPhaseAborted)
	if !ts.opts.NoHistory {
		if err := ts.restoreSession(ts.agent); err != nil {
			al.emitEvent(
				runtimeevents.KindAgentError,
				ts.eventMeta("abortTurn", "turn.error"),
				ErrorPayload{
					Stage:   "session_restore",
					Message: err.Error(),
				},
			)
			return turnResult{}, err
		}
	}
	return turnResult{
		usage:  ts.workflowAgentUsageSnapshot(),
		status: TurnEndStatusAborted,
	}, nil
}

func (al *AgentLoop) selectCandidates(
	agent *AgentInstance,
	userMsg string,
	history []providers.Message,
	sessionKey string,
	reason accountrouter.SelectReason,
) (candidates []providers.FallbackCandidate, model string, usedLight bool, activeRouter *accountrouter.Router, routerSelection accountrouter.Selection) {
	selectTarget := func(target string) ([]providers.FallbackCandidate, string, *accountrouter.Router, accountrouter.Selection) {
		if router := buildAccountRouterWithAliases(
			al.GetConfig(),
			agent.AccountRef,
			target,
			agent.Fallbacks,
			agent.Workspace,
			agent.CandidateProviders,
		); router != nil {
			selection := router.Select(sessionKey, reason)
			if len(selection.Candidates) > 0 {
				return selection.Candidates, resolvedCandidateModel(
					selection.Candidates,
					target,
				), router, selection
			}
			return nil, "", router, selection
		}
		targetCandidates, err := candidatesForAccountAliases(
			al.GetConfig(),
			agent.AccountRef,
			target,
			agent.Fallbacks,
			agent.Workspace,
			agent.CandidateProviders,
		)
		if err != nil {
			return nil, "", nil, accountrouter.Selection{}
		}
		return targetCandidates, resolvedCandidateModel(
			targetCandidates,
			target,
		), nil, accountrouter.Selection{}
	}
	selectPrimary := func() ([]providers.FallbackCandidate, string, *accountrouter.Router, accountrouter.Selection) {
		if agent.AccountRouter != nil {
			selection := agent.AccountRouter.Select(sessionKey, reason)
			if len(selection.Candidates) > 0 {
				return selection.Candidates, resolvedCandidateModel(
					selection.Candidates,
					agent.Model,
				), agent.AccountRouter, selection
			}
			return nil, "", agent.AccountRouter, selection
		}
		return agent.Candidates, resolvedCandidateModel(
			agent.Candidates,
			agent.Model,
		), nil, accountrouter.Selection{}
	}

	if agent.ModelRouter != nil {
		selection := agent.ModelRouter.Select(modelrouter.Input{
			UserMessage: userMsg,
			Messages:    history,
			HasMedia:    messagesHaveMedia(history),
		})
		if strings.TrimSpace(selection.Target) != "" {
			logger.InfoCF("agent", "Model router selected target",
				map[string]any{
					"agent_id": agent.ID,
					"router":   selection.RouterName,
					"target":   selection.Target,
					"block":    selection.BlockID,
				})
			candidates, model, activeRouter, routerSelection = selectTarget(selection.Target)
			return candidates, model, false, activeRouter, routerSelection
		}
	}

	if agent.Router == nil || len(agent.LightCandidates) == 0 {
		candidates, model, activeRouter, routerSelection = selectPrimary()
		return candidates, model, false, activeRouter, routerSelection
	}

	_, usedLight, score := agent.Router.SelectModel(userMsg, history, agent.Model)
	if !usedLight {
		logger.DebugCF("agent", "Model routing: primary model selected",
			map[string]any{
				"agent_id":  agent.ID,
				"score":     score,
				"threshold": agent.Router.Threshold(),
			})
		candidates, model, activeRouter, routerSelection = selectPrimary()
		return candidates, model, false, activeRouter, routerSelection
	}

	logger.InfoCF("agent", "Model routing: light model selected",
		map[string]any{
			"agent_id":    agent.ID,
			"light_model": agent.Router.LightModel(),
			"score":       score,
			"threshold":   agent.Router.Threshold(),
		})
	if agent.LightAccountRouter != nil {
		selection := agent.LightAccountRouter.Select(sessionKey, reason)
		return selection.Candidates, resolvedCandidateModel(
			selection.Candidates,
			agent.Router.LightModel(),
		), true, agent.LightAccountRouter, selection
	}
	return agent.LightCandidates, resolvedCandidateModel(
		agent.LightCandidates,
		agent.Router.LightModel(),
	), true, nil, routerSelection
}

func (al *AgentLoop) selectOverrideCandidates(
	agent *AgentInstance,
	accountRef string,
	modelAlias string,
	modelFallbacks []string,
	sessionKey string,
	reason accountrouter.SelectReason,
) (candidates []providers.FallbackCandidate, model string, displayName string, activeRouter *accountrouter.Router, routerSelection accountrouter.Selection) {
	if agent == nil {
		return nil, "", "", nil, accountrouter.Selection{}
	}
	accountRef = firstNonEmpty(accountRef, agent.AccountRef)
	modelAlias = firstNonEmpty(modelAlias, agent.Model)
	if accountRef == "" || modelAlias == "" {
		return nil, "", modelAlias, nil, accountrouter.Selection{}
	}
	if err := validateModelAliasReferences(al.GetConfig(), modelAlias, nil); err != nil {
		return nil, "", modelAlias, nil, accountrouter.Selection{}
	}
	fallbacks := agent.Fallbacks
	if modelAlias != agent.Model {
		fallbacks = nil
	}
	if modelFallbacks != nil {
		fallbacks = modelFallbacks
	}
	if router := buildAccountRouterWithAliases(
		al.GetConfig(),
		accountRef,
		modelAlias,
		fallbacks,
		agent.Workspace,
		agent.CandidateProviders,
	); router != nil {
		selection := router.Select(sessionKey, reason)
		if len(selection.Candidates) > 0 {
			model = resolvedCandidateModel(selection.Candidates, modelAlias)
			return selection.Candidates, model, modelAlias, router, selection
		}
		return nil, "", modelAlias, router, selection
	}
	candidates, err := candidatesForAccountAliases(
		al.GetConfig(),
		accountRef,
		modelAlias,
		fallbacks,
		agent.Workspace,
		agent.CandidateProviders,
	)
	if err != nil {
		return nil, "", modelAlias, nil, accountrouter.Selection{}
	}
	return candidates, resolvedCandidateModel(candidates, modelAlias), modelAlias, nil, accountrouter.Selection{}
}

func resolveModelSelectorAlias(
	cfg *config.Config,
	selector string,
	userMessage string,
	messages []providers.Message,
) (string, error) {
	selector = strings.TrimSpace(selector)
	if router := buildModelRouter(cfg, selector); router != nil {
		if err := validateModelRouterAliases(cfg, router); err != nil {
			return "", err
		}
		selection := router.Select(modelrouter.Input{
			UserMessage: userMessage,
			Messages:    messages,
			HasMedia:    messagesHaveMedia(messages),
		})
		if strings.TrimSpace(selection.Target) == "" {
			return "", fmt.Errorf("model router %q selected no model alias", selector)
		}
		return strings.TrimSpace(selection.Target), nil
	}
	if _, err := cfg.GetModelAlias(selector); err != nil {
		return "", err
	}
	return selector, nil
}

func messagesHaveMedia(messages []providers.Message) bool {
	for _, msg := range messages {
		if len(msg.Media) > 0 {
			return true
		}
	}
	return false
}

func (al *AgentLoop) resolveContextManager() ContextManager {
	cfg := al.GetConfig()
	if cfg == nil {
		return &legacyContextManager{al: al}
	}
	name := cfg.Agents.Defaults.ContextManager
	if name == "" || name == "legacy" {
		return &legacyContextManager{al: al}
	}
	factory, ok := lookupContextManager(name)
	if !ok {
		logger.WarnCF("agent", "Unknown context manager, falling back to legacy", map[string]any{
			"name": name,
		})
		return &legacyContextManager{al: al}
	}
	cm, err := factory(cfg.Agents.Defaults.ContextManagerConfig, al)
	if err != nil {
		logger.WarnCF(
			"agent",
			"Failed to create context manager, falling back to legacy",
			map[string]any{
				"name":  name,
				"error": err.Error(),
			},
		)
		return &legacyContextManager{al: al}
	}
	return cm
}

type sideQuestionContextSnapshot struct {
	history []providers.Message
	summary string
}

type sideQuestionExecutionOptions struct {
	contextSnapshot        *sideQuestionContextSnapshot
	promptCacheKey         string
	disablePromptCache     bool
	disableSessionAffinity bool
	detachProviderMessages bool
	skipHooks              bool
	rejectToolCalls        bool
	requireResponseContent bool
	privateExecution       bool
	resultModelName        *string
	resultUsage            *[]workflows.AgentUsage
	usageObserver          workflows.AgentUsageObserver
	callAdmission          workflows.AgentCallAdmission
}

func (al *AgentLoop) askSideQuestion(
	ctx context.Context,
	agent *AgentInstance,
	opts *processOptions,
	question string,
) (string, error) {
	return al.askSideQuestionWithOptions(
		ctx,
		agent,
		opts,
		question,
		sideQuestionExecutionOptions{},
	)
}

func (al *AgentLoop) askSideQuestionWithOptions(
	ctx context.Context,
	agent *AgentInstance,
	opts *processOptions,
	question string,
	execution sideQuestionExecutionOptions,
) (string, error) {
	usage := newWorkflowAgentUsageAccumulator(execution.usageObserver)
	if execution.resultUsage != nil {
		defer func() {
			*execution.resultUsage = cloneWorkflowAgentUsage(usage.Snapshot())
		}()
	}
	if agent == nil {
		return "", fmt.Errorf("askSideQuestion: no agent available for /btw")
	}
	if agent.ConfigurationError != nil {
		return "", agent.ConfigurationError
	}

	question = strings.TrimSpace(question)
	if question == "" {
		return "", fmt.Errorf("askSideQuestion: %w", fmt.Errorf("Usage: /btw <question>"))
	}

	if opts != nil {
		normalizeProcessOptionsInPlace(opts)
		resolved, err := resolveTurnProfileOptions(al.GetConfig(), *opts)
		if err != nil {
			return "", err
		}
		*opts = resolved
	}

	var media []string
	var channel, chatID, senderID, senderDisplayName string
	if opts != nil {
		media = opts.Media
		channel = opts.Channel
		chatID = opts.ChatID
		senderID = opts.SenderID
		senderDisplayName = opts.SenderDisplayName
	}

	// Build messages with context but WITHOUT adding to session history
	var history []providers.Message
	var summary string
	if execution.contextSnapshot != nil && (opts == nil || !opts.NoHistory) {
		history = session.CloneMessages(execution.contextSnapshot.history)
		summary = execution.contextSnapshot.summary
	} else if opts != nil && !opts.NoHistory {
		if resp, err := al.contextManager.Assemble(ctx, &AssembleRequest{
			SessionKey: opts.SessionKey,
			Budget:     agent.ContextWindow,
			MaxTokens:  agent.MaxTokens,
		}); err == nil && resp != nil {
			history = resp.History
			summary = resp.Summary
		}
	}

	var promptReq PromptBuildRequest
	if opts == nil {
		promptReq = PromptBuildRequest{
			History:           history,
			Summary:           summary,
			CurrentMessage:    question,
			Media:             append([]string(nil), media...),
			Channel:           channel,
			ChatID:            chatID,
			SenderID:          senderID,
			SenderDisplayName: senderDisplayName,
		}
	} else {
		promptReq = promptBuildRequestForProcessOptions(
			agent,
			*opts,
			history,
			summary,
			question,
			media,
		)
	}
	promptReq.SuppressToolUseRule = true
	promptReq.ToolUseFallback = false
	messages := agent.ContextBuilder.BuildMessagesFromPrompt(promptReq)

	maxMediaSize := al.GetConfig().Agents.Defaults.GetMaxMediaSize()
	currentTurnStart := len(messages)
	if strings.TrimSpace(question) != "" || len(media) > 0 {
		currentTurnStart = len(messages) - 1
	}
	messages = resolveMediaRefs(messages, al.mediaStore, maxMediaSize, currentTurnStart)
	if execution.disablePromptCache {
		messages = messagesWithoutPromptCacheControls(messages)
	}

	selectionSessionKey := optsSessionKey(opts)
	if execution.disableSessionAffinity {
		selectionSessionKey = ""
	}
	activeCandidates, activeModel, usedLight, activeAccountRouter, routerSelection := al.selectCandidates(
		agent,
		question,
		messages,
		selectionSessionKey,
		accountrouter.SelectReasonInitial,
	)
	selectedModelName := sideQuestionModelName(agent, usedLight)
	if opts != nil {
		if strings.TrimSpace(opts.ModelNameOverride) != "" ||
			strings.TrimSpace(opts.AccountRefOverride) != "" ||
			opts.ModelFallbacksOverride != nil {
			overrideSelector := firstNonEmpty(opts.ModelNameOverride, agent.Model)
			overrideAlias, err := resolveModelSelectorAlias(
				al.GetConfig(),
				overrideSelector,
				question,
				messages,
			)
			if err != nil {
				return "", err
			}
			overrideFallbacks := opts.ModelFallbacksOverride
			if overrideFallbacks == nil && overrideSelector == agent.Model {
				overrideFallbacks = agent.Fallbacks
			}
			if err := validateModelAliasReferences(
				al.GetConfig(),
				overrideAlias,
				overrideFallbacks,
			); err != nil {
				return "", err
			}
			if firstNonEmpty(opts.AccountRefOverride, agent.AccountRef) == "" {
				return "", fmt.Errorf("no account configured")
			}
			activeCandidates, activeModel, selectedModelName, activeAccountRouter, routerSelection = al.selectOverrideCandidates(
				agent,
				opts.AccountRefOverride,
				overrideAlias,
				overrideFallbacks,
				selectionSessionKey,
				accountrouter.SelectReasonInitial,
			)
			if !candidateSelectionHasProvider(agent, activeCandidates) {
				return "", fmt.Errorf(
					"model alias %q with account %q has no runnable provider",
					selectedModelName,
					firstNonEmpty(opts.AccountRefOverride, agent.AccountRef),
				)
			}
		}
	}
	if !candidateSelectionHasProvider(agent, activeCandidates) {
		if activeAccountRouter != nil {
			return "", fmt.Errorf(
				"account router %q has no runnable account provider",
				activeAccountRouter.Name,
			)
		}
		return "", fmt.Errorf(
			"model alias %q with account %q has no runnable provider",
			selectedModelName,
			agent.AccountRef,
		)
	}

	llmOpts := map[string]any{
		"max_tokens":  agent.MaxTokens,
		"temperature": agent.Temperature,
	}
	if !execution.disablePromptCache {
		cacheKey := strings.TrimSpace(execution.promptCacheKey)
		if cacheKey == "" {
			cacheKey = agent.ID + ":btw"
		}
		llmOpts["prompt_cache_key"] = cacheKey
	}

	hookModelChanged := false
	sideSuppressReasoning := false
	callProvider := func(
		ctx context.Context,
		candidate providers.FallbackCandidate,
		model string,
		forceModel bool,
		callMessages []providers.Message,
	) (*providers.LLMResponse, error) {
		if admissionErr := admitWorkflowAgentCall(execution.callAdmission); admissionErr != nil {
			return nil, admissionErr
		}
		if usageErr := usage.Err(); usageErr != nil {
			return nil, usageErr
		}
		baseModelName := selectedModelName
		if forceModel && strings.TrimSpace(model) != "" {
			baseModelName = model
		}
		provider, providerModel, modelCfg, cleanup, err := al.isolatedSideQuestionProvider(
			agent,
			baseModelName,
			candidate,
		)
		if err != nil {
			return nil, err
		}
		defer cleanup()
		if !forceModel || strings.TrimSpace(model) == "" {
			model = providerModel
		}
		callOpts := shallowCloneLLMOptions(llmOpts)
		settings := thinkingSettingsFromModelConfig(modelCfg)
		sideSuppressReasoning = shouldSuppressReasoningFor(settings)
		if _, exists := callOpts["thinking_level"]; !exists {
			if settings.configured || hasReasoningEffortConfig(modelCfg) {
				applyThinkingOption(callOpts, provider, settings, false, agent.ID)
				applyReasoningEffortOption(callOpts, modelCfg)
			}
		}
		if opts != nil {
			applyReasoningEffortOverride(callOpts, opts.ReasoningEffortOverride)
		}
		providerMessages := callMessages
		if execution.contextSnapshot != nil || execution.detachProviderMessages {
			providerMessages = session.CloneMessages(callMessages)
		}
		startedAt := time.Now()
		response, callErr := provider.Chat(ctx, providerMessages, nil, model, callOpts)
		if observeErr := func() error {
			observed, ok := workflowAgentUsageFromResponse(model, response, time.Since(startedAt))
			if !ok {
				return nil
			}
			return usage.Observe(observed)
		}(); observeErr != nil {
			return response, observeErr
		}
		if callErr == nil {
			callErr = providers.ResponseSafetyFilterError(
				response, candidate.Provider, model,
			)
		}
		return response, callErr
	}

	turnCtx := newTurnContext(nil, nil, nil)
	if opts != nil {
		turnCtx = newTurnContext(
			opts.Dispatch.InboundContext,
			opts.Dispatch.RouteResult,
			opts.Dispatch.SessionScope,
		)
	}
	llmModel := activeModel
	if al.hooks != nil && !execution.skipHooks {
		llmReq, decision := al.hooks.BeforeLLM(ctx, &LLMHookRequest{
			Meta: HookMeta{
				Source:      "askSideQuestion",
				TracePath:   "turn.llm.request",
				turnContext: cloneTurnContext(turnCtx),
			},
			Context:          cloneTurnContext(turnCtx),
			Model:            selectedModelName,
			Messages:         messages,
			Tools:            nil,
			Options:          llmOpts,
			GracefulTerminal: false,
		})
		switch decision.normalizedAction() {
		case HookActionContinue, HookActionModify:
			if llmReq != nil {
				nextAlias := strings.TrimSpace(llmReq.Model)
				if nextAlias != strings.TrimSpace(selectedModelName) {
					if err := validateModelAliasReferences(al.GetConfig(), nextAlias, nil); err != nil {
						return "", err
					}
					accountRef := concreteAccountRefForCandidates(activeCandidates)
					if accountRef == "" {
						accountRef = firstConcreteAccountRef(al.GetConfig(), agent.AccountRef)
					}
					nextCandidates, err := candidatesForAccountAliases(
						al.GetConfig(),
						accountRef,
						nextAlias,
						nil,
						agent.Workspace,
						agent.CandidateProviders,
					)
					if err != nil {
						return "", err
					}
					if !candidateSelectionHasProvider(agent, nextCandidates) {
						return "", fmt.Errorf(
							"model alias %q with account %q has no runnable provider",
							nextAlias,
							accountRef,
						)
					}
					hookModelChanged = true
					activeCandidates = nextCandidates
					activeModel = resolvedCandidateModel(nextCandidates, nextAlias)
					selectedModelName = nextAlias
					llmModel = activeModel
					associateRouterSelectionCandidate(
						&routerSelection,
						nextCandidates[0],
						accountRef,
					)
				}
				messages = llmReq.Messages
				llmOpts = llmReq.Options
				delete(llmOpts, "native_search")
			}
		case HookActionAbortTurn:
			reason := decision.Reason
			if reason == "" {
				reason = "hook requested turn abort"
			}
			return "", fmt.Errorf("hook aborted turn during before_llm: %s", reason)
		case HookActionHardAbort:
			reason := decision.Reason
			if reason == "" {
				reason = "hook requested turn abort"
			}
			return "", fmt.Errorf("hook aborted turn during before_llm: %s", reason)
		}
	}
	callSideLLM := func(callMessages []providers.Message) (*providers.LLMResponse, error) {
		if usageErr := usage.Err(); usageErr != nil {
			return nil, usageErr
		}
		if len(activeCandidates) > 1 && al.fallback != nil {
			fbResult, err := al.fallback.ExecuteCandidate(
				ctx,
				activeCandidates,
				func(ctx context.Context, candidate providers.FallbackCandidate) (*providers.LLMResponse, error) {
					return callProvider(ctx, candidate, candidate.Model, false, callMessages)
				},
			)
			if usageErr := usage.Err(); usageErr != nil {
				return nil, usageErr
			}
			if err != nil {
				if activeAccountRouter != nil {
					result := fallbackResultFromError(err, activeCandidates...)
					if execution.privateExecution {
						activeAccountRouter.RecordPrivateFallbackResult(routerSelection, result, err)
					} else {
						activeAccountRouter.RecordFallbackResult(routerSelection, result, err)
					}
				}
				return nil, err
			}
			if activeAccountRouter != nil {
				if execution.privateExecution {
					activeAccountRouter.RecordPrivateFallbackResult(routerSelection, fbResult, nil)
				} else {
					activeAccountRouter.RecordFallbackResult(routerSelection, fbResult, nil)
				}
			}
			if execution.resultModelName != nil {
				actual := selectedModelName
				for _, candidate := range activeCandidates {
					if candidate.StableKey() == fbResult.IdentityKey {
						actual = resolvedCandidateModelName([]providers.FallbackCandidate{candidate}, actual)
						break
					}
				}
				*execution.resultModelName = strings.TrimSpace(actual)
			}
			return fbResult.Response, nil
		}

		var candidate providers.FallbackCandidate
		if len(activeCandidates) > 0 {
			candidate = activeCandidates[0]
		}
		resp, err := callProvider(ctx, candidate, llmModel, hookModelChanged, callMessages)
		if usageErr := usage.Err(); usageErr != nil {
			return resp, usageErr
		}
		if err == nil && execution.resultModelName != nil {
			*execution.resultModelName = resolvedCandidateModelName(
				[]providers.FallbackCandidate{candidate}, selectedModelName,
			)
		}
		if activeAccountRouter != nil {
			result := fallbackResultFromSingleCandidate(candidate, resp, err)
			if execution.privateExecution {
				activeAccountRouter.RecordPrivateFallbackResult(routerSelection, result, err)
			} else {
				activeAccountRouter.RecordFallbackResult(routerSelection, result, err)
			}
		}
		return resp, err
	}

	// Retry without media if vision is unsupported
	// Note: Vision retry is only applied to the initial call. If fallback chain
	// is used, vision errors from fallback providers will not trigger retry.
	var resp *providers.LLMResponse
	var err error
	resp, err = callSideLLM(messages)
	if usageErr := usage.Err(); usageErr != nil {
		return "", usageErr
	}
	if failErr := providers.ClassifyError(err, "", selectedModelName); err != nil &&
		failErr != nil && failErr.Reason == providers.FailoverSafetyFilter {
		resp, err = callSideLLM(messages)
		if usageErr := usage.Err(); usageErr != nil {
			return "", usageErr
		}
	}
	if err != nil && hasMediaRefs(messages) && isVisionUnsupportedError(err) {
		if !execution.privateExecution {
			al.emitEvent(
				runtimeevents.KindAgentLLMRetry,
				HookMeta{
					Source:      "askSideQuestion",
					TracePath:   "turn.llm.retry",
					turnContext: cloneTurnContext(turnCtx),
				},
				LLMRetryPayload{
					Attempt:    1,
					MaxRetries: 1,
					Reason:     "vision_unsupported",
					Error:      err.Error(),
					Backoff:    0,
				},
			)
		}
		messagesWithoutMedia := stripMessageMedia(messages)
		resp, err = callSideLLM(messagesWithoutMedia)
		if usageErr := usage.Err(); usageErr != nil {
			return "", usageErr
		}
	}
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", nil
	}

	// Apply after_llm hooks
	if al.hooks != nil && !execution.skipHooks {
		llmResp, decision := al.hooks.AfterLLM(ctx, &LLMHookResponse{
			Meta: HookMeta{
				Source:      "askSideQuestion",
				TracePath:   "turn.llm.response",
				turnContext: cloneTurnContext(turnCtx),
			},
			Context:  cloneTurnContext(turnCtx),
			Model:    selectedModelName,
			Response: resp,
		})
		switch decision.normalizedAction() {
		case HookActionContinue, HookActionModify:
			if llmResp != nil && llmResp.Response != nil {
				resp = llmResp.Response
			}
		case HookActionAbortTurn, HookActionHardAbort:
			reason := decision.Reason
			if reason == "" {
				reason = "hook requested turn abort"
			}
			return "", fmt.Errorf("hook aborted turn during after_llm: %s", reason)
		}
	}
	if sideSuppressReasoning {
		resp.Reasoning = ""
		resp.ReasoningContent = ""
		resp.ReasoningDetails = nil
	}
	if execution.rejectToolCalls && len(resp.ToolCalls) > 0 {
		return "", fmt.Errorf("isolated agent decision returned tool calls")
	}
	if execution.requireResponseContent {
		return resp.Content, nil
	}

	return sideQuestionResponseContent(resp), nil
}

func messagesWithoutPromptCacheControls(messages []providers.Message) []providers.Message {
	cloned := session.CloneMessages(messages)
	for messageIndex := range cloned {
		for blockIndex := range cloned[messageIndex].SystemParts {
			cloned[messageIndex].SystemParts[blockIndex].CacheControl = nil
		}
	}
	return cloned
}

func (al *AgentLoop) isolatedSideQuestionProvider(
	agent *AgentInstance,
	baseModelName string,
	candidate providers.FallbackCandidate,
) (providers.LLMProvider, string, *config.ModelConfig, func(), error) {
	if agent == nil {
		return nil, "", nil, func() {}, fmt.Errorf(
			"isolatedSideQuestionProvider: no agent available for /btw",
		)
	}

	modelCfg, err := al.sideQuestionModelConfig(agent, baseModelName, candidate)
	if err != nil {
		return nil, "", nil, func() {}, fmt.Errorf("isolatedSideQuestionProvider: %w", err)
	}

	factory := al.providerFactory
	if factory == nil {
		factory = providers.CreateProviderFromConfig
	}
	provider, modelID, err := factory(modelCfg)
	if err != nil {
		return nil, "", nil, func() {}, fmt.Errorf("isolatedSideQuestionProvider: %w", err)
	}

	cleanup := func() {
		closeProviderIfStateful(provider)
	}
	return provider, modelID, modelCfg, cleanup, nil
}

func (al *AgentLoop) sideQuestionModelConfig(
	agent *AgentInstance,
	baseModelName string,
	candidate providers.FallbackCandidate,
) (*config.ModelConfig, error) {
	if agent == nil {
		return nil, fmt.Errorf("sideQuestionModelConfig: no agent available for /btw")
	}

	if accountRef := accountRefFromCandidateIdentityKey(candidate.IdentityKey); accountRef != "" {
		modelAlias := modelAliasFromCandidateIdentityKey(candidate.IdentityKey)
		modelCfg, err := concreteAccountModelConfig(
			al.GetConfig(),
			accountRef,
			modelAlias,
			agent.Workspace,
		)
		if err != nil {
			return nil, err
		}
		modelCfg.Model = strings.TrimSpace(candidate.Model)
		return modelCfg, nil
	}
	return nil, fmt.Errorf(
		"side-question candidate for model alias %q has no concrete account identity",
		strings.TrimSpace(baseModelName),
	)
}
