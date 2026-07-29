package channels

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/media"
)

type baseAdmissionRecorder struct {
	calls int
	msg   bus.InboundMessage
}

func (r *baseAdmissionRecorder) AdmitInbound(
	_ context.Context,
	msg bus.InboundMessage,
) (bool, error) {
	r.calls++
	r.msg = msg
	return false, nil
}

type baseTurnUXChannel struct {
	*BaseChannel
	typingCalls      int
	reactionCalls    int
	placeholderCalls int
}

func (c *baseTurnUXChannel) Start(context.Context) error {
	c.SetRunning(true)
	return nil
}

func (c *baseTurnUXChannel) Stop(context.Context) error {
	c.SetRunning(false)
	return nil
}

func (c *baseTurnUXChannel) Send(
	context.Context,
	bus.OutboundMessage,
) ([]string, error) {
	return nil, nil
}

func (c *baseTurnUXChannel) StartTyping(
	context.Context,
	string,
) (func(), error) {
	c.typingCalls++
	return func() {}, nil
}

func (c *baseTurnUXChannel) ReactToMessage(
	context.Context,
	string,
	string,
) (func(), error) {
	c.reactionCalls++
	return func() {}, nil
}

func (c *baseTurnUXChannel) SendPlaceholder(
	context.Context,
	string,
) (string, error) {
	c.placeholderCalls++
	return "placeholder-1", nil
}

type baseTurnUXRecorder struct {
	typing       int
	reaction     int
	placeholder  int
	rollback     int
	replacements int
	identity     string
	recorded     chan struct{}
	beforeBuild  func()
}

func (r *baseTurnUXRecorder) RecordPlaceholder(string, string, string) {
	r.placeholder++
}

func (r *baseTurnUXRecorder) RecordTypingStop(string, string, func()) {
	r.typing++
}

func (r *baseTurnUXRecorder) RecordReactionUndo(string, string, func()) {
	r.reaction++
}

func (r *baseTurnUXRecorder) RecordTurnUX(
	_ context.Context,
	_, _ string,
	registration TurnUXRegistration,
) func(context.Context) {
	r.identity = registration.Identity
	if registration.TypingStop != nil {
		r.typing++
	}
	if registration.ReactionUndo != nil {
		r.reaction++
	}
	if registration.Placeholder != "" {
		r.placeholder++
	}
	if r.recorded != nil {
		close(r.recorded)
	}
	return func(context.Context) { r.rollback++ }
}

func (r *baseTurnUXRecorder) ReplaceTurnUX(
	ctx context.Context,
	channel, chatID string,
	build func() TurnUXRegistration,
) func(context.Context) {
	r.replacements++
	if r.beforeBuild != nil {
		r.beforeBuild()
	}
	return r.RecordTurnUX(ctx, channel, chatID, build())
}

type baseMediaStore struct {
	released []string
}

func (*baseMediaStore) Store(string, media.MediaMeta, string) (string, error) {
	return "", nil
}

func (*baseMediaStore) Resolve(string) (string, error) {
	return "", nil
}

func (*baseMediaStore) ResolveWithMeta(string) (string, media.MediaMeta, error) {
	return "", media.MediaMeta{}, nil
}

func (store *baseMediaStore) ReleaseAll(scope string) error {
	store.released = append(store.released, scope)
	return nil
}

func TestTypingGenerationOnlyNewestOwnsProviderStop(t *testing.T) {
	base := &BaseChannel{}
	first := base.BeginTypingGeneration("chat")
	second := base.BeginTypingGeneration("chat")

	if first.End() {
		t.Fatal("superseded typing generation still owned provider stop")
	}
	if !second.End() {
		t.Fatal("newest typing generation did not own provider stop")
	}
	if second.End() {
		t.Fatal("typing generation End was not idempotent")
	}
}

func TestReactionGenerationOnlyNewestOwnsProviderUndo(t *testing.T) {
	base := &BaseChannel{}
	first := base.BeginReactionGeneration("chat/message/eyes")
	second := base.BeginReactionGeneration("chat/message/eyes")
	unrelated := base.BeginReactionGeneration("chat/other/eyes")

	if first.End() {
		t.Fatal("superseded reaction generation still owned provider undo")
	}
	if !second.End() {
		t.Fatal("newest reaction generation did not own provider undo")
	}
	if !unrelated.End() {
		t.Fatal("unrelated reaction resource was incorrectly superseded")
	}
	if second.End() {
		t.Fatal("reaction generation End was not idempotent")
	}
}

func TestBaseChannelIsAllowed(t *testing.T) {
	tests := []struct {
		name      string
		allowList []string
		senderID  string
		want      bool
	}{
		{
			name:      "empty allowlist allows all",
			allowList: nil,
			senderID:  "anyone",
			want:      true,
		},
		{
			name:      "compound sender matches numeric allowlist",
			allowList: []string{"123456"},
			senderID:  "123456|alice",
			want:      true,
		},
		{
			name:      "compound sender matches username allowlist",
			allowList: []string{"@alice"},
			senderID:  "123456|alice",
			want:      true,
		},
		{
			name:      "numeric sender matches legacy compound allowlist",
			allowList: []string{"123456|alice"},
			senderID:  "123456",
			want:      true,
		},
		{
			name:      "non matching sender is denied",
			allowList: []string{"123456"},
			senderID:  "654321|bob",
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := NewBaseChannel("test", nil, nil, tt.allowList)
			if got := ch.IsAllowed(tt.senderID); got != tt.want {
				t.Fatalf("IsAllowed(%q) = %v, want %v", tt.senderID, got, tt.want)
			}
		})
	}
}

func TestShouldRespondInGroup(t *testing.T) {
	tests := []struct {
		name        string
		gt          config.GroupTriggerConfig
		isMentioned bool
		content     string
		wantRespond bool
		wantContent string
	}{
		{
			name:        "no config - permissive default",
			gt:          config.GroupTriggerConfig{},
			isMentioned: false,
			content:     "hello world",
			wantRespond: true,
			wantContent: "hello world",
		},
		{
			name:        "no config - mentioned",
			gt:          config.GroupTriggerConfig{},
			isMentioned: true,
			content:     "hello world",
			wantRespond: true,
			wantContent: "hello world",
		},
		{
			name:        "mention_only - not mentioned",
			gt:          config.GroupTriggerConfig{MentionOnly: true},
			isMentioned: false,
			content:     "hello world",
			wantRespond: false,
			wantContent: "hello world",
		},
		{
			name:        "mention_only - mentioned",
			gt:          config.GroupTriggerConfig{MentionOnly: true},
			isMentioned: true,
			content:     "hello world",
			wantRespond: true,
			wantContent: "hello world",
		},
		{
			name:        "prefix match",
			gt:          config.GroupTriggerConfig{Prefixes: []string{"/ask"}},
			isMentioned: false,
			content:     "/ask hello",
			wantRespond: true,
			wantContent: "hello",
		},
		{
			name:        "prefix no match - not mentioned",
			gt:          config.GroupTriggerConfig{Prefixes: []string{"/ask"}},
			isMentioned: false,
			content:     "hello world",
			wantRespond: false,
			wantContent: "hello world",
		},
		{
			name:        "prefix no match - but mentioned",
			gt:          config.GroupTriggerConfig{Prefixes: []string{"/ask"}},
			isMentioned: true,
			content:     "hello world",
			wantRespond: true,
			wantContent: "hello world",
		},
		{
			name:        "multiple prefixes - second matches",
			gt:          config.GroupTriggerConfig{Prefixes: []string{"/ask", "/bot"}},
			isMentioned: false,
			content:     "/bot help me",
			wantRespond: true,
			wantContent: "help me",
		},
		{
			name:        "mention_only with prefixes - mentioned overrides",
			gt:          config.GroupTriggerConfig{MentionOnly: true, Prefixes: []string{"/ask"}},
			isMentioned: true,
			content:     "hello",
			wantRespond: true,
			wantContent: "hello",
		},
		{
			name:        "mention_only with prefixes - not mentioned, no prefix",
			gt:          config.GroupTriggerConfig{MentionOnly: true, Prefixes: []string{"/ask"}},
			isMentioned: false,
			content:     "hello",
			wantRespond: false,
			wantContent: "hello",
		},
		{
			name:        "empty prefix in list is skipped",
			gt:          config.GroupTriggerConfig{Prefixes: []string{"", "/ask"}},
			isMentioned: false,
			content:     "/ask test",
			wantRespond: true,
			wantContent: "test",
		},
		{
			name:        "prefix strips leading whitespace after prefix",
			gt:          config.GroupTriggerConfig{Prefixes: []string{"/ask "}},
			isMentioned: false,
			content:     "/ask hello",
			wantRespond: true,
			wantContent: "hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := NewBaseChannel("test", nil, nil, nil, WithGroupTrigger(tt.gt))
			gotRespond, gotContent := ch.ShouldRespondInGroup(tt.isMentioned, tt.content)
			if gotRespond != tt.wantRespond {
				t.Errorf("ShouldRespondInGroup() respond = %v, want %v", gotRespond, tt.wantRespond)
			}
			if gotContent != tt.wantContent {
				t.Errorf("ShouldRespondInGroup() content = %q, want %q", gotContent, tt.wantContent)
			}
		})
	}
}

func TestIsAllowedSender(t *testing.T) {
	tests := []struct {
		name      string
		allowList []string
		sender    bus.SenderInfo
		want      bool
	}{
		{
			name:      "empty allowlist allows all",
			allowList: nil,
			sender:    bus.SenderInfo{PlatformID: "anyone"},
			want:      true,
		},
		{
			name:      "numeric ID matches PlatformID",
			allowList: []string{"123456"},
			sender: bus.SenderInfo{
				Platform:    "telegram",
				PlatformID:  "123456",
				CanonicalID: "telegram:123456",
			},
			want: true,
		},
		{
			name:      "canonical format matches",
			allowList: []string{"telegram:123456"},
			sender: bus.SenderInfo{
				Platform:    "telegram",
				PlatformID:  "123456",
				CanonicalID: "telegram:123456",
			},
			want: true,
		},
		{
			name:      "canonical format wrong platform",
			allowList: []string{"discord:123456"},
			sender: bus.SenderInfo{
				Platform:    "telegram",
				PlatformID:  "123456",
				CanonicalID: "telegram:123456",
			},
			want: false,
		},
		{
			name:      "@username matches",
			allowList: []string{"@alice"},
			sender: bus.SenderInfo{
				Platform:    "telegram",
				PlatformID:  "123456",
				CanonicalID: "telegram:123456",
				Username:    "alice",
			},
			want: true,
		},
		{
			name:      "compound id|username matches by ID",
			allowList: []string{"123456|alice"},
			sender: bus.SenderInfo{
				Platform:    "telegram",
				PlatformID:  "123456",
				CanonicalID: "telegram:123456",
				Username:    "alice",
			},
			want: true,
		},
		{
			name:      "non matching sender denied",
			allowList: []string{"654321"},
			sender: bus.SenderInfo{
				Platform:    "telegram",
				PlatformID:  "123456",
				CanonicalID: "telegram:123456",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := NewBaseChannel("test", nil, nil, tt.allowList)
			if got := ch.IsAllowedSender(tt.sender); got != tt.want {
				t.Fatalf("IsAllowedSender(%+v) = %v, want %v", tt.sender, got, tt.want)
			}
		})
	}
}

func TestHandleInboundContext_PublishesNormalizedContext(t *testing.T) {
	tests := []struct {
		name       string
		inbound    bus.InboundContext
		wantChat   string
		wantSender string
	}{
		{
			name: "direct uses sender as peer",
			inbound: bus.InboundContext{
				Channel:   "test",
				ChatID:    "chat-1",
				ChatType:  "direct",
				SenderID:  "user-1",
				MessageID: "msg-1",
			},
			wantChat:   "chat-1",
			wantSender: "user-1",
		},
		{
			name: "group uses chat as peer",
			inbound: bus.InboundContext{
				Channel:   "test",
				ChatID:    "group-1",
				ChatType:  "group",
				SenderID:  "user-2",
				MessageID: "msg-2",
			},
			wantChat:   "group-1",
			wantSender: "user-2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msgBus := bus.NewMessageBus()
			defer msgBus.Close()

			ch := NewBaseChannel("test", nil, msgBus, nil)
			ch.HandleInboundContext(context.Background(), tt.inbound.ChatID, "hello", nil, tt.inbound)

			msg := <-msgBus.InboundChan()
			if msg.ChatID != tt.wantChat {
				t.Fatalf("ChatID = %q, want %q", msg.ChatID, tt.wantChat)
			}
			if msg.SenderID != tt.wantSender {
				t.Fatalf("SenderID = %q, want %q", msg.SenderID, tt.wantSender)
			}
			if msg.Context.ChatType != tt.inbound.ChatType {
				t.Fatalf("ChatType = %q, want %q", msg.Context.ChatType, tt.inbound.ChatType)
			}
		})
	}
}

func TestHandleInboundContext_RejectedSenderBypassesAdmission(t *testing.T) {
	msgBus := bus.NewMessageBus()
	defer msgBus.Close()
	admission := &baseAdmissionRecorder{}
	msgBus.SetInboundAdmission(admission)

	ch := NewBaseChannel("test", nil, msgBus, []string{"allowed"})
	err := ch.HandleInboundContext(context.Background(), "chat-1", "hello", nil, bus.InboundContext{
		ChatID:    "chat-1",
		ChatType:  "direct",
		SenderID:  "denied",
		MessageID: "msg-1",
	})
	if err != nil {
		t.Fatalf("HandleInboundContext failed: %v", err)
	}
	if admission.calls != 0 {
		t.Fatalf("admission calls = %d, want 0", admission.calls)
	}
	if got := len(msgBus.InboundChan()); got != 0 {
		t.Fatalf("inbound queue depth = %d, want 0", got)
	}
}

func TestHandleInboundContext_AcceptedSenderReachesAdmissionAsChannelOrigin(t *testing.T) {
	msgBus := bus.NewMessageBus()
	defer msgBus.Close()
	admission := &baseAdmissionRecorder{}
	msgBus.SetInboundAdmission(admission)

	ch := NewBaseChannel("test", nil, msgBus, []string{"allowed"})
	err := ch.HandleInboundContext(context.Background(), "chat-1", "hello", nil, bus.InboundContext{
		ChatID:    "chat-1",
		ChatType:  "direct",
		SenderID:  "allowed",
		MessageID: "msg-1",
	})
	if err != nil {
		t.Fatalf("HandleInboundContext failed: %v", err)
	}
	if admission.calls != 1 {
		t.Fatalf("admission calls = %d, want 1", admission.calls)
	}
	if !admission.msg.ChannelOrigin {
		t.Fatal("accepted BaseChannel message did not carry ChannelOrigin")
	}
	if admission.msg.Context.Channel != "test" {
		t.Fatalf("admission channel = %q, want test", admission.msg.Context.Channel)
	}
	if got := len(msgBus.InboundChan()); got != 0 {
		t.Fatalf("inbound queue depth = %d, want event-only consume", got)
	}
}

func TestHandleInboundContextTurnUXRunsOnlyWhenMessageIsForwarded(t *testing.T) {
	tests := []struct {
		name       string
		eventOnly  bool
		wantCalls  int
		wantQueued int
	}{
		{
			name:       "ordinary turn",
			wantCalls:  1,
			wantQueued: 1,
		},
		{
			name:      "event only",
			eventOnly: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			msgBus := bus.NewMessageBus()
			defer msgBus.Close()
			if test.eventOnly {
				msgBus.SetInboundAdmission(&baseAdmissionRecorder{})
			}

			base := NewBaseChannel("test", nil, msgBus, []string{"allowed"})
			owner := &baseTurnUXChannel{BaseChannel: base}
			recorder := &baseTurnUXRecorder{
				beforeBuild: func() {
					if owner.typingCalls != 0 ||
						owner.reactionCalls != 0 ||
						owner.placeholderCalls != 0 {
						t.Fatal("provider UX started before recorder opened the transition")
					}
				},
			}
			mediaStore := &baseMediaStore{}
			base.SetOwner(owner)
			base.SetPlaceholderRecorder(recorder)
			base.SetMediaStore(mediaStore)

			err := base.HandleInboundContext(
				context.Background(),
				"chat-1",
				"hello",
				[]string{"media://one"},
				bus.InboundContext{
					ChatID:    "chat-1",
					SenderID:  "allowed",
					MessageID: "message-1",
				},
			)
			if err != nil {
				t.Fatalf("HandleInboundContext() error = %v", err)
			}
			if owner.typingCalls != test.wantCalls ||
				owner.reactionCalls != test.wantCalls ||
				owner.placeholderCalls != test.wantCalls {
				t.Fatalf(
					"owner UX calls = typing:%d reaction:%d placeholder:%d, want %d each",
					owner.typingCalls,
					owner.reactionCalls,
					owner.placeholderCalls,
					test.wantCalls,
				)
			}
			if recorder.typing != test.wantCalls ||
				recorder.reaction != test.wantCalls ||
				recorder.placeholder != test.wantCalls {
				t.Fatalf(
					"recorded UX = typing:%d reaction:%d placeholder:%d, want %d each",
					recorder.typing,
					recorder.reaction,
					recorder.placeholder,
					test.wantCalls,
				)
			}
			if recorder.replacements != test.wantCalls {
				t.Fatalf(
					"Turn UX transitions = %d, want %d",
					recorder.replacements,
					test.wantCalls,
				)
			}
			if got := len(msgBus.InboundChan()); got != test.wantQueued {
				t.Fatalf("inbound queue depth = %d, want %d", got, test.wantQueued)
			}
			if test.wantQueued == 1 {
				if recorder.identity == "" {
					t.Fatal("turn UX registration did not receive a process-local identity")
				}
				queued := <-msgBus.InboundChan()
				if queued.Context.TurnUXID != recorder.identity {
					t.Fatalf(
						"queued TurnUXID = %q, registration identity = %q",
						queued.Context.TurnUXID,
						recorder.identity,
					)
				}
			} else if recorder.identity != "" {
				t.Fatalf("event-only message registered turn UX identity %q", recorder.identity)
			}
			wantReleases := 0
			if test.eventOnly {
				wantReleases = 1
			}
			if len(mediaStore.released) != wantReleases {
				t.Fatalf(
					"released media scopes = %#v, want %d release(s)",
					mediaStore.released,
					wantReleases,
				)
			}
			if wantReleases == 1 {
				wantScope := BuildMediaScope("test", "chat-1", "message-1")
				if mediaStore.released[0] != wantScope {
					t.Fatalf("released media scope = %q, want %q", mediaStore.released[0], wantScope)
				}
			}
		})
	}
}

func TestHandleInboundContextRollsBackTurnUXWhenQueueingIsCanceled(t *testing.T) {
	messageBus := bus.NewMessageBus()
	defer messageBus.Close()
	for index := 0; index < cap(messageBus.InboundChan()); index++ {
		err := messageBus.PublishInbound(context.Background(), bus.InboundMessage{
			Context: bus.InboundContext{
				Channel:  "occupied",
				ChatID:   "occupied",
				SenderID: "occupied",
			},
		})
		if err != nil {
			t.Fatalf("fill inbound queue: %v", err)
		}
	}

	base := NewBaseChannel("test", nil, messageBus, []string{"allowed"})
	owner := &baseTurnUXChannel{BaseChannel: base}
	recorder := &baseTurnUXRecorder{recorded: make(chan struct{})}
	mediaStore := &baseMediaStore{}
	base.SetOwner(owner)
	base.SetPlaceholderRecorder(recorder)
	base.SetMediaStore(mediaStore)

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- base.HandleInboundContext(
			ctx,
			"chat-1",
			"hello",
			[]string{"media://one"},
			bus.InboundContext{
				ChatID:    "chat-1",
				SenderID:  "allowed",
				MessageID: "message-1",
			},
		)
	}()
	select {
	case <-recorder.recorded:
	case <-time.After(time.Second):
		t.Fatal("turn UX was not prepared")
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("HandleInboundContext() error = %v, want context.Canceled", err)
	}
	if recorder.rollback != 1 {
		t.Fatalf("turn UX rollback calls = %d, want 1", recorder.rollback)
	}
	if len(mediaStore.released) != 1 {
		t.Fatalf("released media scopes = %#v, want one", mediaStore.released)
	}
}
