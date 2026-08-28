// PicoClaw - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/utils"
)

func (al *AgentLoop) maybePublishError(
	ctx context.Context,
	channel, chatID, sessionKey string,
	err error,
	inboundContext ...*bus.InboundContext,
) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	return al.publishResponseIfNeeded(
		ctx,
		channel,
		chatID,
		sessionKey,
		formatProcessingError(err),
		firstInboundContext(inboundContext),
	)
}

func (al *AgentLoop) publishResponseOrError(
	ctx context.Context,
	channel, chatID, sessionKey string,
	response string,
	err error,
	inboundContext *bus.InboundContext,
) bool {
	if err != nil {
		if !al.maybePublishError(
			ctx,
			channel,
			chatID,
			sessionKey,
			err,
			inboundContext,
		) {
			return false
		}
		return true
	}
	return al.publishResponseIfNeeded(
		ctx,
		channel,
		chatID,
		sessionKey,
		response,
		inboundContext,
	)
}

func (al *AgentLoop) PublishResponseIfNeeded(ctx context.Context, channel, chatID, sessionKey, response string) {
	al.publishResponseIfNeeded(ctx, channel, chatID, sessionKey, response, nil)
}

func (al *AgentLoop) publishResponseIfNeeded(
	ctx context.Context,
	channel, chatID, sessionKey, response string,
	inboundContext *bus.InboundContext,
) bool {
	if response == "" {
		return false
	}

	if al.messageToolSentTo(sessionKey, channel, chatID) {
		if al.channelManager != nil && channel != "" && chatID != "" {
			dismissCtx, dismissCancel := context.WithTimeout(ctx, 5*time.Second)
			al.channelManager.DismissToolFeedback(
				dismissCtx,
				channel,
				chatID,
				nil,
			)
			dismissCancel()
		}
		logger.DebugSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentSkippedOutboundMessageToolAlreadySentToSameChat,
			logger.NewSafeFields(
				agentDiagnosticChannelField(channel),
				agentDiagnosticChatField(chatID),
				logger.SafeEnum(logger.FieldReason, logger.SafeEnumSkipped),
			),
		)
		return true
	}

	outboundContext := bus.NewOutboundContext(channel, chatID, "")
	if inboundContext != nil {
		outboundContext = outboundContextFromInbound(
			inboundContext,
			channel,
			chatID,
			inboundContext.ReplyToMessageID,
		)
	}
	msg := bus.OutboundMessage{
		Context:    outboundContext,
		SessionKey: sessionKey,
		Content:    response,
	}
	if sessionKey != "" {
		msg.ContextUsage = computeContextUsage(al.agentForSession(sessionKey), sessionKey)
	}
	markFinalOutbound(&msg)
	err := al.bus.PublishOutbound(ctx, msg)
	// Preserve the legacy diagnostic ordering: this fixed record is emitted for
	// both successful and failed publication, while the return value reports the
	// actual result.
	logger.InfoSafeCF(
		logger.ComponentAgent,
		logger.DiagnosticMessageAgentPublishedOutboundResponse,
		logger.NewSafeFields(
			agentDiagnosticChannelField(channel),
			agentDiagnosticChatField(chatID),
			logger.SafeInt64(logger.FieldContentBytes, int64(len(response))),
		),
	)
	return err == nil
}

func (al *AgentLoop) messageToolSentTo(sessionKey, channel, chatID string) bool {
	registry := al.GetRegistry()
	if registry == nil {
		return false
	}
	defaultAgent := registry.GetDefaultAgent()
	if defaultAgent == nil {
		return false
	}
	tool, ok := defaultAgent.Tools.Get("message")
	if !ok {
		return false
	}
	messageTool, ok := tool.(*tools.MessageTool)
	return ok && messageTool.HasSentTo(sessionKey, channel, chatID)
}

func firstInboundContext(contexts []*bus.InboundContext) *bus.InboundContext {
	for _, inboundContext := range contexts {
		if inboundContext != nil {
			return inboundContext
		}
	}
	return nil
}

func (al *AgentLoop) targetReasoningChannelID(channelName string) (chatID string) {
	if al.channelManager == nil {
		return ""
	}
	if ch, ok := al.channelManager.GetChannel(channelName); ok {
		return ch.ReasoningChannelID()
	}
	return ""
}

func (al *AgentLoop) publishPicoReasoning(
	ctx context.Context,
	reasoningContent, chatID, sessionKey, modelName string,
) {
	if reasoningContent == "" || chatID == "" {
		return
	}

	if ctx.Err() != nil {
		return
	}

	pubCtx, pubCancel := context.WithTimeout(ctx, 5*time.Second)
	defer pubCancel()

	raw := map[string]string{metadataKeyMessageKind: messageKindThought}
	if trimmedModelName := strings.TrimSpace(modelName); trimmedModelName != "" {
		raw["model_name"] = trimmedModelName
	}

	if err := al.bus.PublishOutbound(pubCtx, bus.OutboundMessage{
		Context: bus.InboundContext{
			Channel: "pico",
			ChatID:  chatID,
			Raw:     raw,
		},
		SessionKey: sessionKey,
		Content:    reasoningContent,
	}); err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) ||
			errors.Is(err, bus.ErrBusClosed) {
			errorClass := logger.ErrorClassCanceled
			if errors.Is(err, context.DeadlineExceeded) {
				errorClass = logger.ErrorClassDeadline
			}
			logger.DebugSafeCF(
				logger.ComponentAgent,
				logger.DiagnosticMessageAgentPicoReasoningPublishSkippedTimeoutCancel,
				logger.NewSafeFields(
					agentDiagnosticChannelField("pico"),
					agentDiagnosticErrorField(errorClass, err),
				),
			)
		} else {
			logger.WarnSafeCF(
				logger.ComponentAgent,
				logger.DiagnosticMessageAgentFailedToPublishPicoReasoningBestEffort,
				logger.NewSafeFields(
					agentDiagnosticChannelField("pico"),
					agentDiagnosticErrorField(logger.ErrorClassTransport, err),
				),
			)
		}
	}
}

func (al *AgentLoop) publishPicoToolCallInterim(
	ctx context.Context,
	ts *turnState,
	modelName string,
	reasoningContent string,
	content string,
	toolCalls []providers.ToolCall,
) {
	if ts == nil || ts.chatID == "" || al == nil || al.bus == nil {
		return
	}

	if strings.TrimSpace(reasoningContent) != "" {
		pubCtx, pubCancel := context.WithTimeout(ctx, 3*time.Second)
		err := al.bus.PublishOutbound(
			pubCtx,
			outboundMessageForTurnWithOptions(
				ts,
				reasoningContent,
				outboundTurnMessageOptions{
					kind:      messageKindThought,
					modelName: modelName,
				},
			),
		)
		pubCancel()
		if err != nil && !errors.Is(err, context.DeadlineExceeded) &&
			!errors.Is(err, context.Canceled) &&
			!errors.Is(err, bus.ErrBusClosed) {
			logger.WarnSafeCF(
				logger.ComponentAgent,
				logger.DiagnosticMessageAgentFailedToPublishPicoReasoning,
				logger.NewSafeFields(
					agentDiagnosticChannelField(ts.channel),
					agentDiagnosticChatField(ts.chatID),
					agentDiagnosticErrorField(logger.ErrorClassTransport, err),
				),
			)
		}
	}

	if !ts.opts.AllowInterimPicoPublish {
		return
	}

	visibleToolCalls := utils.BuildVisibleToolCalls(
		toolCalls,
		al.cfg.Agents.Defaults.GetToolFeedbackMaxArgsLength(),
	)
	duplicateToolCallContent := len(visibleToolCalls) > 0 &&
		utils.ToolCallExplanationDuplicatesContent(content, toolCalls)

	if strings.TrimSpace(content) != "" && !duplicateToolCallContent {
		pubCtx, pubCancel := context.WithTimeout(ctx, 3*time.Second)
		err := al.bus.PublishOutbound(
			pubCtx,
			outboundMessageForTurnWithOptions(ts, content, outboundTurnMessageOptions{
				modelName: modelName,
			}),
		)
		pubCancel()
		if err != nil && !errors.Is(err, context.DeadlineExceeded) &&
			!errors.Is(err, context.Canceled) &&
			!errors.Is(err, bus.ErrBusClosed) {
			logger.WarnSafeCF(
				logger.ComponentAgent,
				logger.DiagnosticMessageAgentFailedToPublishPicoInterimAssistantContent,
				logger.NewSafeFields(
					agentDiagnosticChannelField(ts.channel),
					agentDiagnosticChatField(ts.chatID),
					agentDiagnosticErrorField(logger.ErrorClassTransport, err),
				),
			)
		}
	}

	if len(visibleToolCalls) == 0 {
		return
	}

	rawToolCalls, err := json.Marshal(visibleToolCalls)
	if err != nil {
		logger.WarnSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentFailedToSerializePicoToolCalls,
			logger.NewSafeFields(
				agentDiagnosticChannelField(ts.channel),
				agentDiagnosticChatField(ts.chatID),
				agentDiagnosticErrorField(logger.ErrorClassInternal, err),
			),
		)
		return
	}

	msg := outboundMessageForTurnWithOptions(ts, "", outboundTurnMessageOptions{
		kind:      messageKindToolCalls,
		modelName: modelName,
		raw: map[string]string{
			metadataKeyToolCalls: string(rawToolCalls),
		},
	})

	pubCtx, pubCancel := context.WithTimeout(ctx, 3*time.Second)
	err = al.bus.PublishOutbound(pubCtx, msg)
	pubCancel()
	if err != nil && !errors.Is(err, context.DeadlineExceeded) &&
		!errors.Is(err, context.Canceled) &&
		!errors.Is(err, bus.ErrBusClosed) {
		logger.WarnSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentFailedToPublishPicoToolCalls,
			logger.NewSafeFields(
				agentDiagnosticChannelField(ts.channel),
				agentDiagnosticChatField(ts.chatID),
				agentDiagnosticErrorField(logger.ErrorClassTransport, err),
			),
		)
	}
}

func (al *AgentLoop) handleReasoning(
	ctx context.Context,
	reasoningContent, channelName, channelID string,
) {
	if reasoningContent == "" || channelName == "" || channelID == "" {
		return
	}

	// Check context cancellation before attempting to publish,
	// since PublishOutbound's select may race between send and ctx.Done().
	if ctx.Err() != nil {
		return
	}

	// Use a short timeout so the goroutine does not block indefinitely when
	// the outbound bus is full.  Reasoning output is best-effort; dropping it
	// is acceptable to avoid goroutine accumulation.
	pubCtx, pubCancel := context.WithTimeout(ctx, 5*time.Second)
	defer pubCancel()

	if err := al.bus.PublishOutbound(pubCtx, bus.OutboundMessage{
		Context: bus.NewOutboundContext(channelName, channelID, ""),
		Content: reasoningContent,
	}); err != nil {
		// Treat context.DeadlineExceeded / context.Canceled as expected
		// (bus full under load, or parent canceled).  Check the error
		// itself rather than ctx.Err(), because pubCtx may time out
		// (5 s) while the parent ctx is still active.
		// Also treat ErrBusClosed as expected — it occurs during normal
		// shutdown when the bus is closed before all goroutines finish.
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) ||
			errors.Is(err, bus.ErrBusClosed) {
			errorClass := logger.ErrorClassCanceled
			if errors.Is(err, context.DeadlineExceeded) {
				errorClass = logger.ErrorClassDeadline
			}
			logger.DebugSafeCF(
				logger.ComponentAgent,
				logger.DiagnosticMessageAgentReasoningPublishSkippedTimeoutCancel,
				logger.NewSafeFields(
					agentDiagnosticChannelField(channelName),
					agentDiagnosticErrorField(errorClass, err),
				),
			)
		} else {
			logger.WarnSafeCF(
				logger.ComponentAgent,
				logger.DiagnosticMessageAgentFailedToPublishReasoningBestEffort,
				logger.NewSafeFields(
					agentDiagnosticChannelField(channelName),
					agentDiagnosticErrorField(logger.ErrorClassTransport, err),
				),
			)
		}
	}
}
