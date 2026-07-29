// PicoClaw - Ultra-lightweight personal AI agent

package interfaces

import (
	"context"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
)

// MessageBus publishes inbound and outbound messages.
// It is the primary communication channel for the agent loop.
type MessageBus interface {
	// PublishInbound sends an inbound message to be processed.
	PublishInbound(ctx context.Context, msg bus.InboundMessage) error

	// PublishOutbound sends an outbound message to the appropriate channel.
	PublishOutbound(ctx context.Context, msg bus.OutboundMessage) error

	// PublishOutboundMedia sends an outbound media message.
	PublishOutboundMedia(ctx context.Context, msg bus.OutboundMediaMessage) error

	// GetStreamer returns a channel streamer when the active channel supports streaming.
	GetStreamer(
		ctx context.Context,
		channel, chatID, sessionKey string,
	) (bus.Streamer, bool)

	// InboundChan returns the channel for receiving inbound messages.
	InboundChan() <-chan bus.InboundMessage
}

// TurnScopedMessageBus is an additive streaming capability. Agent integrations
// implementing only the legacy MessageBus contract remain source-compatible.
type TurnScopedMessageBus interface {
	GetStreamerForTurn(
		ctx context.Context,
		channel, chatID, sessionKey, turnUXID string,
	) (bus.Streamer, bool)
}

// ChannelManager manages channel lifecycle and provides channel access.
type ChannelManager interface {
	// GetChannel returns the channel with the given name.
	GetChannel(name string) (channels.Channel, bool)

	// GetEnabledChannels returns the list of enabled channel names.
	GetEnabledChannels() []string

	// InvokeTypingStop signals that typing has stopped.
	InvokeTypingStop(channel, chatID string)

	// SendMessage sends a text message to the specified channel and chat.
	SendMessage(ctx context.Context, msg bus.OutboundMessage) error

	// SendMedia sends a media message to the specified channel and chat.
	SendMedia(ctx context.Context, msg bus.OutboundMediaMessage) error

	// SendPlaceholder sends a placeholder message (e.g., for audio transcription).
	SendPlaceholder(ctx context.Context, channel, chatID string) bool

	// DismissToolFeedback clears any tracked tool feedback animation for the
	// given channel/chat. Call this when a turn ends without a final response
	// (e.g., ResponseHandled tools) to avoid orphaned animation goroutines.
	// outboundCtx carries topic/thread info needed for channels that use
	// scoped tracker keys (e.g., Telegram forum topics); may be nil.
	DismissToolFeedback(ctx context.Context, channel, chatID string, outboundCtx *bus.InboundContext)
}

// MessageScopedTypingStopper stops only the typing registration owned by one
// opaque turn UX identity.
type MessageScopedTypingStopper interface {
	InvokeTypingStopForMessage(channel, chatID, turnUXID string)
}

// MessageScopedTurnUXCleaner removes transient artifacts owned by one
// abandoned turn without touching a newer turn.
type MessageScopedTurnUXCleaner interface {
	CleanupTurnUXForMessage(
		ctx context.Context,
		channel, chatID, turnUXID string,
	)
}

// MessageScopedTurnUXRebinder assigns a steering message's transient UX to the
// already-active turn that will produce its response.
type MessageScopedTurnUXRebinder interface {
	RebindTurnUXForMessage(channel, chatID, fromTurnUXID, toTurnUXID string)
}

// MessageScopedPlaceholderSender creates a placeholder owned by one inbound
// turn.
type MessageScopedPlaceholderSender interface {
	SendPlaceholderForMessage(
		ctx context.Context,
		channel, chatID, turnUXID string,
	) bool
}
