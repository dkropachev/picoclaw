package channels

import (
	"context"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/commands"
)

// TypingCapable — channels that can show a typing/thinking indicator.
// StartTyping begins the indicator and returns a stop function.
// The stop function MUST be idempotent and safe to call multiple times.
// It MUST also be pinned to the exact provider generation it created: a
// delayed stop from an older turn must not clear a newer turn's indicator.
type TypingCapable interface {
	StartTyping(ctx context.Context, chatID string) (stop func(), err error)
}

// MessageEditor — channels that can edit an existing message.
// messageID is always string; channels convert platform-specific types internally.
type MessageEditor interface {
	EditMessage(ctx context.Context, chatID string, messageID string, content string) error
}

// MessageEditorWithPayload extends MessageEditor for channels that can update
// structured message metadata in addition to plain text content.
type MessageEditorWithPayload interface {
	EditMessageWithPayload(
		ctx context.Context,
		chatID string,
		messageID string,
		payload map[string]any,
	) error
}

// MessageDeleter — channels that can delete a message by ID.
type MessageDeleter interface {
	DeleteMessage(ctx context.Context, chatID string, messageID string) error
}

// ReactionCapable — channels that can add a reaction (e.g. 👀) to an inbound message.
// ReactToMessage adds a reaction and returns an undo function to remove it.
// The undo function MUST be idempotent and safe to call multiple times.
// It MUST also be pinned to the exact provider reaction it created: a delayed
// undo from an older turn must not remove a newer turn's reaction.
type ReactionCapable interface {
	ReactToMessage(ctx context.Context, chatID, messageID string) (undo func(), err error)
}

// PlaceholderCapable — channels that can send a placeholder message
// (e.g. "Thinking... 💭") that will later be edited to the actual response.
// The channel MUST also implement MessageEditor for the placeholder to be useful.
// SendPlaceholder returns the platform message ID of the placeholder so that
// Manager.preSend can later edit it via MessageEditor.EditMessage.
type PlaceholderCapable interface {
	SendPlaceholder(ctx context.Context, chatID string) (messageID string, err error)
}

// StreamingCapable — channels that can show partial LLM output in real-time.
// The channel SHOULD gracefully degrade if the platform rejects streaming
// (e.g. Telegram bot without forum mode). In that case, Update becomes a no-op
// and Finalize still delivers the final message.
type StreamingCapable interface {
	BeginStream(ctx context.Context, chatID string) (Streamer, error)
}

// Streamer is defined in pkg/bus to avoid circular imports.
// This alias keeps channel implementations using channels.Streamer unchanged.
type Streamer = bus.Streamer

// PlaceholderRecorder is injected into channels by Manager.
// Channels call these methods on inbound to register typing/placeholder state.
// Manager uses the registered state on outbound to stop typing and edit placeholders.
type PlaceholderRecorder interface {
	RecordPlaceholder(channel, chatID, placeholderID string)
	RecordTypingStop(channel, chatID string, stop func())
	RecordReactionUndo(channel, chatID string, undo func())
}

// TurnUXRegistration groups the transient artifacts created immediately before
// an admitted inbound message enters the agent queue.
type TurnUXRegistration struct {
	TypingStop   func()
	ReactionUndo func()
	Placeholder  string
	// Identity is an opaque process-local ID that binds these artifacts to the
	// inbound turn that created them.
	// A later outbound for an older turn must not consume a newer registration
	// that happens to share the same channel and chat.
	Identity string
	// Owner is the exact channel generation that created Placeholder. Cleanup
	// must never resolve the name through a potentially replaced manager map.
	Owner Channel
}

// TransactionalPlaceholderRecorder lets the message bus roll back transient
// UX when cancellation or shutdown wins after preparation but before queueing.
// The returned function must affect only this registration, not a newer turn
// for the same chat.
type TransactionalPlaceholderRecorder interface {
	RecordTurnUX(
		ctx context.Context,
		channel, chatID string,
		registration TurnUXRegistration,
	) (rollback func(context.Context))
}

// TurnUXTransitionRecorder serializes replacement of one chat's transient UX.
// build is invoked synchronously only after the prior registration has been
// detached and its provider effects have been given a bounded cleanup window.
// The returned rollback must affect only the registration built by this call.
type TurnUXTransitionRecorder interface {
	ReplaceTurnUX(
		ctx context.Context,
		channel, chatID string,
		build func() TurnUXRegistration,
	) (rollback func(context.Context))
}

// CommandRegistrarCapable is implemented by channels that can register
// command menus with their upstream platform (e.g. Telegram BotCommand).
// Channels that do not support platform-level command menus can ignore it.
type CommandRegistrarCapable interface {
	RegisterCommands(ctx context.Context, defs []commands.Definition) error
}
