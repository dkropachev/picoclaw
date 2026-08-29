// PicoClaw - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"errors"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/logger"
)

func (al *AgentLoop) processMessageSync(ctx context.Context, msg bus.InboundMessage) {
	msg = bus.NormalizeInboundMessage(msg)
	response, err := al.processMessage(ctx, msg)
	outboundEnqueued := al.publishResponseOrError(
		ctx,
		msg.Channel,
		msg.ChatID,
		msg.SessionKey,
		response,
		err,
		&msg.Context,
	)
	if al.channelManager == nil {
		return
	}
	if outboundEnqueued {
		// Buffered delivery still owns the reaction and placeholder.
		invokeTypingStopForMessage(
			al.channelManager,
			msg.Channel,
			msg.ChatID,
			msg.Context.TurnUXID,
		)
		return
	}
	cleanupTurnUXForMessage(
		ctx,
		al.channelManager,
		msg.Channel,
		msg.ChatID,
		msg.Context.TurnUXID,
	)
}

func (al *AgentLoop) runTurnWithSteering(
	ctx context.Context,
	initialMsg bus.InboundMessage,
	prepared bool,
) bool {
	outboundEnqueued := false
	trackedResultOwner := &trackedSubagentResultOutputOwner{}
	ctx = withTrackedSubagentResultOutputOwner(ctx, trackedResultOwner)
	defer trackedResultOwner.release(al)

	// Process the initial message
	var response string
	var err error
	if prepared {
		response, err = al.processPreparedMessage(ctx, initialMsg)
	} else {
		response, err = al.processMessage(ctx, initialMsg)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return false // context canceled
		}
		outboundEnqueued = al.maybePublishError(
			ctx,
			initialMsg.Channel,
			initialMsg.ChatID,
			initialMsg.SessionKey,
			err,
			&initialMsg.Context,
		)
		response = ""
	}
	finalResponse := response

	// Build continuation target
	target, targetErr := al.buildContinuationTarget(initialMsg)
	if targetErr != nil {
		logger.WarnSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentFailedToBuildSteeringContinuationTarget,
			logger.NewSafeFields(
				agentDiagnosticChannelField(initialMsg.Channel),
				agentDiagnosticErrorField(logger.ErrorClassValidation, targetErr),
			),
		)
		return outboundEnqueued
	}
	if target == nil {
		// System message or non-routable, response already published
		return outboundEnqueued
	}

	continued, continueErr := al.drainQueuedSteeringContinuations(ctx, target, nil)
	if continueErr != nil {
		logger.WarnSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentFailedToContinueQueuedSteering,
			logger.NewSafeFields(
				agentDiagnosticChannelField(target.Channel),
				agentDiagnosticChatField(target.ChatID),
				agentDiagnosticErrorField(logger.ErrorClassInternal, continueErr),
			),
		)
	} else if continued != "" {
		finalResponse = continued
	}

	// Publish final response
	if finalResponse != "" {
		outboundEnqueued = al.publishResponseIfNeeded(
			ctx,
			target.Channel,
			target.ChatID,
			target.SessionKey,
			finalResponse,
			target.InboundContext,
		) || outboundEnqueued
	}
	if al.messageToolSentTo(target.SessionKey, target.Channel, target.ChatID) {
		outboundEnqueued = true
	}
	return outboundEnqueued
}

func (al *AgentLoop) drainQueuedSteeringContinuations(
	ctx context.Context,
	target *continuationTarget,
	diagnosticOrigin *runtimeDiagnosticOrigin,
) (string, error) {
	if target == nil {
		return "", nil
	}

	finalResponse := ""
	for al.pendingSteeringCountForScope(target.SessionKey) > 0 {
		if err := ctx.Err(); err != nil {
			return finalResponse, err
		}

		logger.InfoSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentContinuingQueuedSteeringAfterTurnEnd,
			logger.NewSafeFields(
				agentDiagnosticChannelField(target.Channel),
				agentDiagnosticChatField(target.ChatID),
				agentDiagnosticSessionField(target.SessionKey),
				logger.SafeInt(logger.FieldQueueDepth, al.pendingSteeringCountForScope(target.SessionKey)),
			),
		)

		continued, continueErr := al.continueWithInboundContext(
			ctx,
			target.SessionKey,
			target.Channel,
			target.ChatID,
			target.InboundContext,
			diagnosticOrigin,
		)
		if continueErr != nil {
			return finalResponse, continueErr
		}
		if continued == "" {
			break
		}
		finalResponse = continued
	}

	return finalResponse, nil
}

func (al *AgentLoop) resolveSteeringTarget(msg bus.InboundMessage) (string, string, bool) {
	if msg.Channel == "system" {
		return "", "", false
	}

	route, agent, err := al.resolveMessageRoute(msg)
	if err != nil || agent == nil {
		return "", "", false
	}
	allocation := al.allocateRouteSession(route, msg)

	return resolveScopeKey(allocation.SessionKey, msg.SessionKey), agent.ID, true
}
