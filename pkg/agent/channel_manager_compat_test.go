package agent

import (
	"context"
	"testing"

	"github.com/sipeed/picoclaw/pkg/agent/interfaces"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
)

type legacyCompatChannelManager struct {
	typingStops       int
	placeholders      int
	dismissals        int
	placeholderResult bool
	dismissContext    *bus.InboundContext
	dismissContextErr error
}

func (*legacyCompatChannelManager) GetChannel(string) (channels.Channel, bool) {
	return nil, false
}

func (*legacyCompatChannelManager) GetEnabledChannels() []string {
	return nil
}

func (manager *legacyCompatChannelManager) InvokeTypingStop(string, string) {
	manager.typingStops++
}

func (*legacyCompatChannelManager) SendMessage(
	context.Context,
	bus.OutboundMessage,
) error {
	return nil
}

func (*legacyCompatChannelManager) SendMedia(
	context.Context,
	bus.OutboundMediaMessage,
) error {
	return nil
}

func (manager *legacyCompatChannelManager) SendPlaceholder(
	context.Context,
	string,
	string,
) bool {
	manager.placeholders++
	return manager.placeholderResult
}

func (manager *legacyCompatChannelManager) DismissToolFeedback(
	ctx context.Context,
	_, _ string,
	outboundCtx *bus.InboundContext,
) {
	manager.dismissals++
	manager.dismissContext = outboundCtx
	manager.dismissContextErr = ctx.Err()
}

type scopedCompatChannelManager struct {
	*legacyCompatChannelManager
	scopedTypingStops  int
	scopedCleanups     int
	scopedRebinds      int
	scopedPlaceholders int
	lastTurnUXID       string
}

func (manager *scopedCompatChannelManager) InvokeTypingStopForMessage(
	_, _, turnUXID string,
) {
	manager.scopedTypingStops++
	manager.lastTurnUXID = turnUXID
}

func (manager *scopedCompatChannelManager) CleanupTurnUXForMessage(
	_ context.Context,
	_, _, turnUXID string,
) {
	manager.scopedCleanups++
	manager.lastTurnUXID = turnUXID
}

func (manager *scopedCompatChannelManager) RebindTurnUXForMessage(
	_, _, _, toTurnUXID string,
) {
	manager.scopedRebinds++
	manager.lastTurnUXID = toTurnUXID
}

func (manager *scopedCompatChannelManager) SendPlaceholderForMessage(
	_ context.Context,
	_, _, turnUXID string,
) bool {
	manager.scopedPlaceholders++
	manager.lastTurnUXID = turnUXID
	return true
}

func TestChannelManagerCompatibilityFallbacks(t *testing.T) {
	t.Run("legacy implementation remains usable", func(t *testing.T) {
		manager := &legacyCompatChannelManager{placeholderResult: true}

		invokeTypingStopForMessage(manager, "legacy", "chat", "turn-typing")
		if manager.typingStops != 1 {
			t.Fatalf("legacy typing stops = %d, want 1", manager.typingStops)
		}
		if !sendPlaceholderForMessage(
			context.Background(),
			manager,
			"legacy",
			"chat",
			"turn-placeholder",
		) {
			t.Fatal("legacy placeholder fallback returned false")
		}
		rebindTurnUXForMessage(
			manager,
			"legacy",
			"chat",
			"turn-from",
			"turn-to",
		)

		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		cleanupTurnUXForMessage(
			canceled,
			manager,
			"legacy",
			"chat",
			"turn-cleanup",
		)
		if manager.typingStops != 2 ||
			manager.placeholders != 1 ||
			manager.dismissals != 1 {
			t.Fatalf(
				"legacy fallback calls = typing:%d placeholder:%d dismiss:%d, want 2/1/1",
				manager.typingStops,
				manager.placeholders,
				manager.dismissals,
			)
		}
		if manager.dismissContextErr != nil {
			t.Fatalf(
				"legacy cleanup inherited canceled context: %v",
				manager.dismissContextErr,
			)
		}
		if manager.dismissContext == nil ||
			manager.dismissContext.TurnUXID != "turn-cleanup" {
			t.Fatalf(
				"legacy cleanup context = %#v, want turn-cleanup",
				manager.dismissContext,
			)
		}
	})

	t.Run("additive capabilities receive exact turn identity", func(t *testing.T) {
		legacy := &legacyCompatChannelManager{}
		manager := &scopedCompatChannelManager{
			legacyCompatChannelManager: legacy,
		}

		invokeTypingStopForMessage(manager, "scoped", "chat", "turn-typing")
		cleanupTurnUXForMessage(
			context.Background(),
			manager,
			"scoped",
			"chat",
			"turn-cleanup",
		)
		rebindTurnUXForMessage(
			manager,
			"scoped",
			"chat",
			"turn-from",
			"turn-to",
		)
		if !sendPlaceholderForMessage(
			context.Background(),
			manager,
			"scoped",
			"chat",
			"turn-placeholder",
		) {
			t.Fatal("scoped placeholder returned false")
		}

		if manager.scopedTypingStops != 1 ||
			manager.scopedCleanups != 1 ||
			manager.scopedRebinds != 1 ||
			manager.scopedPlaceholders != 1 {
			t.Fatalf(
				"scoped calls = typing:%d cleanup:%d rebind:%d placeholder:%d, want 1 each",
				manager.scopedTypingStops,
				manager.scopedCleanups,
				manager.scopedRebinds,
				manager.scopedPlaceholders,
			)
		}
		if manager.lastTurnUXID != "turn-placeholder" {
			t.Fatalf(
				"last scoped turn identity = %q, want turn-placeholder",
				manager.lastTurnUXID,
			)
		}
		if legacy.typingStops != 0 ||
			legacy.placeholders != 0 ||
			legacy.dismissals != 0 {
			t.Fatalf(
				"legacy fallbacks ran for scoped manager: typing:%d placeholder:%d dismiss:%d",
				legacy.typingStops,
				legacy.placeholders,
				legacy.dismissals,
			)
		}
	})
}

var (
	_ interfaces.ChannelManager                 = (*legacyCompatChannelManager)(nil)
	_ interfaces.ChannelManager                 = (*scopedCompatChannelManager)(nil)
	_ interfaces.MessageScopedTypingStopper     = (*scopedCompatChannelManager)(nil)
	_ interfaces.MessageScopedTurnUXCleaner     = (*scopedCompatChannelManager)(nil)
	_ interfaces.MessageScopedTurnUXRebinder    = (*scopedCompatChannelManager)(nil)
	_ interfaces.MessageScopedPlaceholderSender = (*scopedCompatChannelManager)(nil)
)
