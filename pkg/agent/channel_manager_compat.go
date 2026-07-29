package agent

import (
	"context"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent/interfaces"
	"github.com/sipeed/picoclaw/pkg/bus"
)

const legacyTurnUXCleanupTimeout = 5 * time.Second

func invokeTypingStopForMessage(
	manager interfaces.ChannelManager,
	channel, chatID, turnUXID string,
) {
	if manager == nil {
		return
	}
	if scoped, ok := manager.(interfaces.MessageScopedTypingStopper); ok {
		scoped.InvokeTypingStopForMessage(channel, chatID, turnUXID)
		return
	}
	manager.InvokeTypingStop(channel, chatID)
}

func cleanupTurnUXForMessage(
	ctx context.Context,
	manager interfaces.ChannelManager,
	channel, chatID, turnUXID string,
) {
	if manager == nil {
		return
	}
	if scoped, ok := manager.(interfaces.MessageScopedTurnUXCleaner); ok {
		scoped.CleanupTurnUXForMessage(ctx, channel, chatID, turnUXID)
		return
	}

	// Legacy managers have no exact reaction/placeholder ownership API. Stop
	// their chat-scoped typing indicator and give tool-feedback cleanup one
	// detached, bounded best-effort attempt.
	manager.InvokeTypingStop(channel, chatID)
	if ctx == nil {
		ctx = context.Background()
	}
	cleanupCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		legacyTurnUXCleanupTimeout,
	)
	defer cancel()
	manager.DismissToolFeedback(
		cleanupCtx,
		channel,
		chatID,
		&bus.InboundContext{
			Channel:  channel,
			ChatID:   chatID,
			TurnUXID: turnUXID,
		},
	)
}

func rebindTurnUXForMessage(
	manager interfaces.ChannelManager,
	channel, chatID, fromTurnUXID, toTurnUXID string,
) {
	if scoped, ok := manager.(interfaces.MessageScopedTurnUXRebinder); ok {
		scoped.RebindTurnUXForMessage(
			channel,
			chatID,
			fromTurnUXID,
			toTurnUXID,
		)
	}
}

func sendPlaceholderForMessage(
	ctx context.Context,
	manager interfaces.ChannelManager,
	channel, chatID, turnUXID string,
) bool {
	if manager == nil {
		return false
	}
	if scoped, ok := manager.(interfaces.MessageScopedPlaceholderSender); ok {
		return scoped.SendPlaceholderForMessage(
			ctx,
			channel,
			chatID,
			turnUXID,
		)
	}
	return manager.SendPlaceholder(ctx, channel, chatID)
}
