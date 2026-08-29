// PicoClaw - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/constants"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/session"
	threadstore "github.com/sipeed/picoclaw/pkg/threads"
)

func (al *AgentLoop) buildContinuationTarget(msg bus.InboundMessage) (*continuationTarget, error) {
	if msg.Channel == "system" {
		return nil, nil
	}

	route, _, err := al.resolveMessageRoute(msg)
	if err != nil {
		return nil, err
	}
	allocation := al.allocateRouteSession(route, msg)

	return &continuationTarget{
		SessionKey:     resolveScopeKey(allocation.SessionKey, msg.SessionKey),
		Channel:        msg.Channel,
		ChatID:         msg.ChatID,
		InboundContext: cloneInboundContext(&msg.Context),
	}, nil
}

func (al *AgentLoop) ProcessDirect(
	ctx context.Context,
	content, sessionKey string,
) (string, error) {
	return al.ProcessDirectWithChannel(ctx, content, sessionKey, "cli", "direct")
}

func (al *AgentLoop) ProcessDirectWithChannel(
	ctx context.Context,
	content, sessionKey, channel, chatID string,
) (string, error) {
	return al.processDirectWithChannel(ctx, content, sessionKey, channel, chatID, false)
}

// ProcessDirectWithChannelAndPublish keeps the direct turn's output ownership
// through publication. Scheduled/background callers use this boundary so a
// late tracked result cannot overtake the response returned by the root turn.
func (al *AgentLoop) ProcessDirectWithChannelAndPublish(
	ctx context.Context,
	content, sessionKey, channel, chatID string,
) (string, error) {
	return al.processDirectWithChannel(ctx, content, sessionKey, channel, chatID, true)
}

func (al *AgentLoop) processDirectWithChannel(
	ctx context.Context,
	content, sessionKey, channel, chatID string,
	publish bool,
) (string, error) {
	leaseCtx, releaseRuntime, err := al.acquireTrustedRuntimeRoot(ctx)
	if err != nil {
		return "", err
	}
	defer releaseRuntime()
	ctx = leaseCtx

	if hookErr := al.ensureHooksInitialized(ctx); hookErr != nil {
		return "", hookErr
	}
	if mcpErr := al.ensureMCPInitialized(ctx); mcpErr != nil {
		return "", mcpErr
	}

	msg := bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  channel,
			ChatID:   chatID,
			ChatType: "direct",
			SenderID: "cron",
		},
		Content:    content,
		SessionKey: sessionKey,
	}
	outputOwner := &trackedSubagentResultOutputOwner{}
	ctx = withTrackedSubagentResultOutputOwner(ctx, outputOwner)
	defer outputOwner.release(al)
	response, err := al.processMessage(ctx, msg)
	if err == nil && publish && response != "" {
		al.PublishResponseIfNeeded(ctx, channel, chatID, sessionKey, response)
	}
	return response, err
}

func (al *AgentLoop) ProcessHeartbeat(
	ctx context.Context,
	content, channel, chatID string,
) (string, error) {
	leaseCtx, releaseRuntime, err := al.acquireTrustedRuntimeRoot(ctx)
	if err != nil {
		return "", err
	}
	defer releaseRuntime()
	ctx = leaseCtx

	if err := al.ensureHooksInitialized(ctx); err != nil {
		return "", err
	}
	if err := al.ensureMCPInitialized(ctx); err != nil {
		return "", err
	}

	agent := al.GetRegistry().GetDefaultAgent()
	if agent == nil {
		return "", fmt.Errorf("no default agent for heartbeat")
	}
	dispatch := DispatchRequest{
		SessionKey:  "heartbeat",
		UserMessage: content,
	}
	if channel != "" || chatID != "" {
		dispatch.InboundContext = &bus.InboundContext{
			Channel:  channel,
			ChatID:   chatID,
			ChatType: "direct",
			SenderID: "heartbeat",
		}
	}
	return al.runAgentLoop(ctx, agent, processOptions{
		Dispatch:             dispatch,
		DefaultResponse:      defaultResponse,
		EnableSummary:        false,
		SendResponse:         false,
		SuppressToolFeedback: true,
		NoHistory:            true, // Don't load session history for heartbeat
	})
}

type threadMetaReader interface {
	GetSessionMeta(ctx context.Context, sessionKey string) (memory.SessionMeta, error)
}

func (al *AgentLoop) resolveAttachedThreadSession(
	ctx context.Context,
	agent *AgentInstance,
	sessionKey string,
) string {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" || agent == nil || agent.Sessions == nil {
		return sessionKey
	}
	reader, ok := agent.Sessions.(threadMetaReader)
	if !ok {
		return sessionKey
	}
	meta, err := reader.GetSessionMeta(ctx, sessionKey)
	if err != nil || strings.TrimSpace(meta.ThreadID) == "" {
		return sessionKey
	}
	threadMeta, ok, err := threadstore.NewStoreFromWorkspace(agent.Workspace).GetMeta(meta.ThreadID)
	if err != nil || !ok || strings.TrimSpace(threadMeta.PrimarySessionKey) == "" {
		return sessionKey
	}
	return threadMeta.PrimarySessionKey
}

func (al *AgentLoop) prepareInboundMessageForAgent(
	ctx context.Context,
	msg bus.InboundMessage,
) bus.InboundMessage {
	msg = bus.NormalizeInboundMessage(msg)

	var hadAudio bool
	msg, hadAudio = al.transcribeAudioInMessage(ctx, msg)

	// For audio messages the placeholder was deferred by the channel.
	// Now that transcription (and optional feedback) is done, send it.
	if hadAudio && al.channelManager != nil {
		sendPlaceholderForMessage(
			ctx,
			al.channelManager,
			msg.Channel,
			msg.ChatID,
			msg.Context.TurnUXID,
		)
	}

	return msg
}

func (al *AgentLoop) processMessage(ctx context.Context, msg bus.InboundMessage) (string, error) {
	return al.processMessageWithPreparation(ctx, msg, false)
}

// admitInboundMessageSession fences the Run-loop fast paths that handle /stop
// and steering without entering processMessage. Those paths must establish the
// same atomic ordinary-session ownership as a full turn before they mutate any
// turn, queue, command, or message-tool state.
func (al *AgentLoop) admitInboundMessageSession(
	ctx context.Context,
	msg bus.InboundMessage,
	sessionKey string,
) error {
	route, agent, err := al.resolveMessageRoute(msg)
	if err != nil {
		return err
	}
	allocation := al.allocateRouteSession(route, msg)
	scopeKey := resolveScopeKey(allocation.SessionKey, msg.SessionKey)
	originKey := strings.TrimSpace(sessionKey)
	if originKey == "" {
		originKey = scopeKey
	}
	aliases := buildSessionAliases(
		originKey,
		append(allocation.SessionAliases, msg.SessionKey, scopeKey)...,
	)
	if err := admitSessionMetadata(
		ctx,
		agent.Sessions,
		originKey,
		session.CloneScope(&allocation.Scope),
		aliases,
		agent.ID,
	); err != nil {
		return fmt.Errorf("admit live session scope: %w", err)
	}

	// Thread linkage can redirect execution after route allocation. Claim its
	// authoritative target too, while retaining the origin claim used for turn
	// serialization and trigger identity. A stale link to review must fail before
	// workflow-trigger handling or inbound preparation.
	targetKey := al.resolveAttachedThreadSession(ctx, agent, scopeKey)
	if targetKey == "" || targetKey == originKey {
		return nil
	}
	if err := admitSessionMetadata(
		ctx,
		agent.Sessions,
		targetKey,
		nil,
		nil,
		agent.ID,
	); err != nil {
		return fmt.Errorf("admit attached live session scope: %w", err)
	}
	return nil
}

func (al *AgentLoop) processPreparedMessage(
	ctx context.Context,
	msg bus.InboundMessage,
) (string, error) {
	return al.processMessageWithPreparation(ctx, msg, true)
}

func (al *AgentLoop) processMessageWithPreparation(
	ctx context.Context,
	msg bus.InboundMessage,
	prepared bool,
) (string, error) {
	leaseCtx, releaseRuntime, err := al.acquireTrustedRuntimeRoot(ctx)
	if err != nil {
		return "", err
	}
	defer releaseRuntime()
	ctx = leaseCtx

	// Route system messages to processSystemMessage
	if msg.Channel == "system" {
		return al.processSystemMessage(ctx, msg)
	}

	route, agent, routeErr := al.resolveMessageRoute(msg)
	if routeErr != nil {
		return "", routeErr
	}

	allocation := al.allocateRouteSession(route, msg)

	// Resolve session key from the route allocation, while preserving explicit
	// agent-scoped keys supplied by the caller.
	scopeKey := resolveScopeKey(allocation.SessionKey, msg.SessionKey)
	originKey := scopeKey
	originAliases := buildSessionAliases(
		originKey,
		append(allocation.SessionAliases, msg.SessionKey, scopeKey)...,
	)
	if admissionErr := admitSessionMetadata(
		ctx,
		agent.Sessions,
		originKey,
		session.CloneScope(&allocation.Scope),
		originAliases,
		agent.ID,
	); admissionErr != nil {
		return "", fmt.Errorf("admit live origin session scope: %w", admissionErr)
	}

	sessionKey := al.resolveAttachedThreadSession(ctx, agent, originKey)
	sessionScope := session.CloneScope(&allocation.Scope)
	sessionAliases := originAliases
	if sessionKey != originKey {
		if admissionErr := admitSessionMetadata(
			ctx,
			agent.Sessions,
			sessionKey,
			nil,
			nil,
			agent.ID,
		); admissionErr != nil {
			return "", fmt.Errorf("admit attached live session scope: %w", admissionErr)
		}
		// Attached targets own their existing scope and aliases. Nil aliases mean
		// preserve the locked set; never rewrite target identity using the origin
		// route allocation.
		sessionAliases = nil
		if metadata, ok := agent.Sessions.(session.MetadataAwareSessionStore); ok {
			sessionScope = session.CloneScope(metadata.GetSessionScope(sessionKey))
		} else {
			sessionScope = nil
		}
	}

	// Audio transcription and placeholder feedback may invoke external services
	// or mutate UX state. Run them only after the final attached-thread key has
	// won ordinary ownership.
	if !prepared {
		msg = al.prepareInboundMessageForAgent(ctx, msg)
	}

	logger.InfoSafeCF(
		logger.ComponentAgent,
		logger.DiagnosticMessageInboundMessage,
		logger.NewSafeFields(
			agentDiagnosticChannelField(msg.Channel),
			agentDiagnosticChatField(msg.ChatID),
			agentDiagnosticSenderField(msg.SenderID),
			agentDiagnosticSessionField(msg.SessionKey),
			logger.SafeInt(logger.FieldMediaCount, len(msg.Media)),
			logger.SafeObservation(
				logger.ObservationPrefixMessageGraph,
				logger.ObserveText(logger.ObservationDomainMessageGraph, msg.Content),
			),
		),
	)
	logger.DebugSensitiveCF(
		logger.DiagnosticPolicyFromContext(ctx),
		logger.ComponentAgent,
		logger.DiagnosticMessageInboundMessage,
		logger.NewSafeFields(
			agentDiagnosticChannelField(msg.Channel),
			agentDiagnosticChatField(msg.ChatID),
			agentDiagnosticSenderField(msg.SenderID),
			agentDiagnosticSessionField(msg.SessionKey),
			logger.SafeInt(logger.FieldMediaCount, len(msg.Media)),
		),
		logger.SensitivityInboundMessage,
		logger.ObservationDomainMessageGraph,
		msg.Content,
	)

	logger.InfoSafeCF(
		logger.ComponentAgent,
		logger.DiagnosticMessageAgentRoutedMessage,
		logger.NewSafeFields(
			agentDiagnosticAgentField(agent.ID),
			agentDiagnosticScopeField(scopeKey),
			agentDiagnosticSessionField(sessionKey),
			agentDiagnosticRouteField(route.MatchedBy),
			agentDiagnosticRouteAgentField(route.AgentID),
			agentDiagnosticRouteChannelField(route.Channel),
			agentDiagnosticRouteSessionField(allocation.MainSessionKey),
		),
	)

	opts := processOptions{
		Dispatch: DispatchRequest{
			SessionKey:     sessionKey,
			SessionAliases: sessionAliases,
			InboundContext: cloneInboundContext(&msg.Context),
			RouteResult:    cloneResolvedRoute(&route),
			SessionScope:   sessionScope,
			UserMessage:    msg.Content,
			Media:          append([]string(nil), msg.Media...),
		},
		SenderID:                 msg.SenderID,
		SenderDisplayName:        msg.Sender.DisplayName,
		DefaultResponse:          defaultResponse,
		EnableSummary:            true,
		SendResponse:             false,
		AllowInterimPicoPublish:  true,
		trackedResultOutputOwner: trackedSubagentResultOutputOwnerFromContext(ctx),
		turnReservation:          turnReservationFromContext(ctx),
	}
	if msg.Context.Raw != nil {
		opts.ModelNameOverride = strings.TrimSpace(msg.Context.Raw["model_name"])
		opts.AccountRefOverride = strings.TrimSpace(msg.Context.Raw["account_ref"])
	}
	opts, err = resolveTurnProfileOptions(al.GetConfig(), opts)
	if err != nil {
		return "", err
	}

	// Reset message-tool state only after session ownership is admitted, so a
	// losing live caller has no command/tool side effects.
	if tool, ok := agent.Tools.Get("message"); ok {
		if resetter, ok := tool.(interface{ ResetSentInRound(sessionKey string) }); ok {
			resetter.ResetSentInRound(sessionKey)
		}
	}

	// context-dependent commands check their own Runtime fields and report
	// "unavailable" when the required capability is nil.
	if response, handled := al.handleCommand(ctx, msg, agent, &opts); handled {
		return response, nil
	}

	if pending := al.takePendingSkills(opts.Dispatch.SessionKey); len(pending) > 0 {
		opts.ForcedSkills = append(opts.ForcedSkills, pending...)
		logger.InfoSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentApplyingPendingSkillOverride,
			logger.NewSafeFields(
				agentDiagnosticSessionField(opts.Dispatch.SessionKey),
				logger.SafeInt(logger.FieldSkillCount, len(pending)),
			),
		)
	}

	return al.runAgentLoop(ctx, agent, opts)
}

func (al *AgentLoop) resolveMessageRoute(msg bus.InboundMessage) (routing.ResolvedRoute, *AgentInstance, error) {
	registry := al.GetRegistry()
	inboundCtx := normalizedInboundContext(msg)
	route := registry.ResolveRoute(inboundCtx)

	agent, ok := registry.GetAgent(route.AgentID)
	if !ok {
		agent = registry.GetDefaultAgent()
	}
	if agent == nil {
		return routing.ResolvedRoute{}, nil, fmt.Errorf("no agent available for route (agent_id=%s)", route.AgentID)
	}

	return route, agent, nil
}

func (al *AgentLoop) allocateRouteSession(route routing.ResolvedRoute, msg bus.InboundMessage) session.Allocation {
	return session.AllocateRouteSession(session.AllocationInput{
		AgentID:       route.AgentID,
		Context:       normalizedInboundContext(msg),
		SessionPolicy: route.SessionPolicy,
	})
}

func (al *AgentLoop) processSystemMessage(
	ctx context.Context,
	msg bus.InboundMessage,
) (string, error) {
	if msg.Channel != "system" {
		return "", fmt.Errorf(
			"processSystemMessage called with non-system message channel: %s",
			msg.Channel,
		)
	}

	logger.InfoSafeCF(
		logger.ComponentAgent,
		logger.DiagnosticMessageAgentProcessingSystemMessage,
		logger.NewSafeFields(
			agentDiagnosticSenderField(msg.SenderID),
			agentDiagnosticChatField(msg.ChatID),
		),
	)

	// Parse origin channel from chat_id (format: "channel:chat_id")
	var originChannel, originChatID string
	if idx := strings.Index(msg.ChatID, ":"); idx > 0 {
		originChannel = msg.ChatID[:idx]
		originChatID = msg.ChatID[idx+1:]
	} else {
		originChannel = "cli"
		originChatID = msg.ChatID
	}

	// Extract subagent result from message content
	// Format: "Task 'label' completed.\n\nResult:\n<actual content>"
	content := msg.Content
	if idx := strings.Index(content, "Result:\n"); idx >= 0 {
		content = content[idx+8:] // Extract just the result part
	}

	// Skip internal channels - only log, don't send to user
	if constants.IsInternalChannel(originChannel) {
		logger.InfoSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentSubagentCompletedInternalChannel,
			logger.NewSafeFields(
				agentDiagnosticSenderField(msg.SenderID),
				agentDiagnosticChannelField(originChannel),
				logger.SafeInt64(logger.FieldContentBytes, int64(len(content))),
			),
		)
		return "", nil
	}

	// Use default agent for system messages
	agent := al.GetRegistry().GetDefaultAgent()
	if agent == nil {
		return "", fmt.Errorf("no default agent for system message")
	}

	// Use the origin session for context
	sessionKey := session.BuildMainSessionKey(agent.ID)
	dispatch := DispatchRequest{
		SessionKey:  sessionKey,
		UserMessage: fmt.Sprintf("[System: %s] %s", msg.SenderID, msg.Content),
	}
	if originChannel != "" || originChatID != "" {
		dispatch.InboundContext = &bus.InboundContext{
			Channel:  originChannel,
			ChatID:   originChatID,
			ChatType: "direct",
			SenderID: msg.SenderID,
		}
	}

	return al.runAgentLoop(ctx, agent, processOptions{
		Dispatch:        dispatch,
		DefaultResponse: "Background task completed.",
		EnableSummary:   false,
		SendResponse:    true,
	})
}
