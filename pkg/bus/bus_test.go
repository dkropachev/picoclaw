package bus

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
)

type inboundAdmissionFunc func(context.Context, InboundMessage) (bool, error)

func (f inboundAdmissionFunc) AdmitInbound(ctx context.Context, msg InboundMessage) (bool, error) {
	return f(ctx, msg)
}

type pointerInboundAdmission struct{}

func (*pointerInboundAdmission) AdmitInbound(
	context.Context,
	InboundMessage,
) (bool, error) {
	return false, nil
}

type streamCompatibilityStreamer struct{}

func (*streamCompatibilityStreamer) Update(context.Context, string) error {
	return nil
}

func (*streamCompatibilityStreamer) Finalize(context.Context, string) error {
	return nil
}

func (*streamCompatibilityStreamer) Cancel(context.Context) {}

type legacyStreamDelegate struct {
	streamer   Streamer
	calls      int
	sessionKey string
}

func (delegate *legacyStreamDelegate) GetStreamer(
	_ context.Context,
	_, _, sessionKey string,
) (Streamer, bool) {
	delegate.calls++
	delegate.sessionKey = sessionKey
	return delegate.streamer, delegate.streamer != nil
}

type turnScopedStreamDelegate struct {
	legacyStreamDelegate
	scopedCalls int
	turnUXID    string
}

func (delegate *turnScopedStreamDelegate) GetStreamerForTurn(
	_ context.Context,
	_, _, _, turnUXID string,
) (Streamer, bool) {
	delegate.scopedCalls++
	delegate.turnUXID = turnUXID
	return delegate.streamer, delegate.streamer != nil
}

func TestMessageBusStreamingDelegateCompatibility(t *testing.T) {
	t.Run("legacy delegate serves both entry points", func(t *testing.T) {
		messageBus := NewMessageBus()
		defer messageBus.Close()

		streamer := &streamCompatibilityStreamer{}
		delegate := &legacyStreamDelegate{streamer: streamer}
		messageBus.SetStreamDelegate(delegate)

		got, ok := messageBus.GetStreamer(
			context.Background(),
			"legacy",
			"chat",
			"legacy-session",
		)
		if !ok || got != streamer {
			t.Fatalf("legacy GetStreamer() = (%T, %t), want configured streamer", got, ok)
		}
		got, ok = messageBus.GetStreamerForTurn(
			context.Background(),
			"legacy",
			"chat",
			"fallback-session",
			"turn-ignored-by-legacy",
		)
		if !ok || got != streamer {
			t.Fatalf("turn-scoped fallback = (%T, %t), want configured streamer", got, ok)
		}
		if delegate.calls != 2 || delegate.sessionKey != "fallback-session" {
			t.Fatalf(
				"legacy delegate calls/session = %d/%q, want 2/fallback-session",
				delegate.calls,
				delegate.sessionKey,
			)
		}
	})

	t.Run("turn-scoped entry point prefers additive capability", func(t *testing.T) {
		messageBus := NewMessageBus()
		defer messageBus.Close()

		streamer := &streamCompatibilityStreamer{}
		delegate := &turnScopedStreamDelegate{
			legacyStreamDelegate: legacyStreamDelegate{streamer: streamer},
		}
		messageBus.SetStreamDelegate(delegate)

		got, ok := messageBus.GetStreamerForTurn(
			context.Background(),
			"scoped",
			"chat",
			"session",
			"turn-exact",
		)
		if !ok || got != streamer {
			t.Fatalf("GetStreamerForTurn() = (%T, %t), want configured streamer", got, ok)
		}
		if delegate.scopedCalls != 1 || delegate.turnUXID != "turn-exact" {
			t.Fatalf(
				"turn-scoped calls/identity = %d/%q, want 1/turn-exact",
				delegate.scopedCalls,
				delegate.turnUXID,
			)
		}
		if delegate.calls != 0 {
			t.Fatalf("legacy delegate calls = %d, want 0", delegate.calls)
		}

		_, _ = messageBus.GetStreamer(
			context.Background(),
			"scoped",
			"chat",
			"legacy-session",
		)
		if delegate.calls != 1 {
			t.Fatalf("legacy entry point calls = %d, want 1", delegate.calls)
		}
	})
}

func TestPublishConsume(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	ctx := context.Background()

	msg := InboundMessage{
		Context: InboundContext{
			Channel:  "test",
			ChatID:   "chat1",
			ChatType: "direct",
			SenderID: "user1",
		},
		Content: "hello",
	}

	if err := mb.PublishInbound(ctx, msg); err != nil {
		t.Fatalf("PublishInbound failed: %v", err)
	}

	got, ok := <-mb.InboundChan()
	if !ok {
		t.Fatal("ConsumeInbound returned ok=false")
	}
	if got.Content != "hello" {
		t.Fatalf("expected content 'hello', got %q", got.Content)
	}
	if got.Channel != "test" {
		t.Fatalf("expected channel 'test', got %q", got.Channel)
	}
	if got.Context.Channel != "test" {
		t.Fatalf("expected context channel 'test', got %q", got.Context.Channel)
	}
	if got.Context.ChatID != "chat1" {
		t.Fatalf("expected context chat ID 'chat1', got %q", got.Context.ChatID)
	}
	if got.Context.SenderID != "user1" {
		t.Fatalf("expected context sender ID 'user1', got %q", got.Context.SenderID)
	}
}

func TestPublishInbound_NilAdmissionPreservesQueueBehavior(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	called := false
	mb.SetInboundAdmission(inboundAdmissionFunc(func(context.Context, InboundMessage) (bool, error) {
		called = true
		return false, nil
	}))
	mb.SetInboundAdmission(nil)

	msg := InboundMessage{
		Context: InboundContext{
			Channel:  "test",
			ChatID:   "chat-1",
			SenderID: "user-1",
		},
		Content:       "hello",
		ChannelOrigin: true,
	}
	if err := mb.PublishInbound(context.Background(), msg); err != nil {
		t.Fatalf("PublishInbound failed: %v", err)
	}
	if called {
		t.Fatal("cleared admission hook was called")
	}
	if got := <-mb.InboundChan(); got.Content != "hello" {
		t.Fatalf("queued content = %q, want hello", got.Content)
	}
}

func TestRegisterInboundAdmissionIsCollisionSafeAndOwnerScoped(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	firstCalls := 0
	first := inboundAdmissionFunc(func(context.Context, InboundMessage) (bool, error) {
		firstCalls++
		return false, nil
	})
	firstRelease, err := mb.RegisterInboundAdmission(first)
	if err != nil {
		t.Fatalf("RegisterInboundAdmission(first) error = %v", err)
	}

	secondCalls := 0
	second := inboundAdmissionFunc(func(context.Context, InboundMessage) (bool, error) {
		secondCalls++
		return false, nil
	})
	if _, err = mb.RegisterInboundAdmission(second); !errors.Is(
		err,
		ErrInboundAdmissionRegistered,
	) {
		t.Fatalf(
			"RegisterInboundAdmission(collision) error = %v, want %v",
			err,
			ErrInboundAdmissionRegistered,
		)
	}

	message := InboundMessage{
		Context: InboundContext{
			Channel:  "test",
			ChatID:   "chat-1",
			SenderID: "user-1",
		},
		ChannelOrigin: true,
	}
	if err = mb.PublishInbound(context.Background(), message); err != nil {
		t.Fatalf("PublishInbound(first) error = %v", err)
	}
	if firstCalls != 1 || secondCalls != 0 {
		t.Fatalf("admission calls after collision = first:%d second:%d", firstCalls, secondCalls)
	}

	firstRelease()
	secondRelease, err := mb.RegisterInboundAdmission(second)
	if err != nil {
		t.Fatalf("RegisterInboundAdmission(second) error = %v", err)
	}
	// A stale release is harmless after a replacement acquires the seam.
	firstRelease()
	if err = mb.PublishInbound(context.Background(), message); err != nil {
		t.Fatalf("PublishInbound(second) error = %v", err)
	}
	if secondCalls != 1 {
		t.Fatalf("second admission calls = %d, want 1", secondCalls)
	}

	replacementCalls := 0
	mb.SetInboundAdmission(inboundAdmissionFunc(
		func(context.Context, InboundMessage) (bool, error) {
			replacementCalls++
			return false, nil
		},
	))
	// Releasing an owner that was externally replaced must not clear the newer
	// admission hook.
	secondRelease()
	if err = mb.PublishInbound(context.Background(), message); err != nil {
		t.Fatalf("PublishInbound(replacement) error = %v", err)
	}
	if replacementCalls != 1 {
		t.Fatalf("replacement admission calls = %d, want 1", replacementCalls)
	}
}

func TestRegisterInboundAdmissionRejectsTypedNil(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	var admission *pointerInboundAdmission
	if _, err := mb.RegisterInboundAdmission(admission); !errors.Is(
		err,
		ErrInvalidInboundAdmission,
	) {
		t.Fatalf(
			"RegisterInboundAdmission(typed nil) error = %v, want %v",
			err,
			ErrInvalidInboundAdmission,
		)
	}

	// The legacy replacement API treats the same value as clearing the hook,
	// so a later publish cannot dispatch through a nil concrete pointer.
	mb.SetInboundAdmission(admission)
	message := InboundMessage{
		Context: InboundContext{
			Channel:  "test",
			ChatID:   "chat-1",
			SenderID: "user-1",
		},
		ChannelOrigin: true,
	}
	if err := mb.PublishInbound(context.Background(), message); err != nil {
		t.Fatalf("PublishInbound after typed nil Set error = %v", err)
	}
	select {
	case <-mb.InboundChan():
	case <-time.After(time.Second):
		t.Fatal("typed nil Set did not preserve direct queue behavior")
	}
}

func TestPublishInbound_AdmissionRunsBeforeQueuePublication(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	called := false
	mb.SetInboundAdmission(inboundAdmissionFunc(func(_ context.Context, msg InboundMessage) (bool, error) {
		called = true
		if got := len(mb.inbound); got != 0 {
			t.Errorf("inbound queue depth in admission = %d, want 0", got)
		}
		if msg.Content != "hello" {
			t.Errorf("admitted content = %q, want hello", msg.Content)
		}
		return true, nil
	}))

	msg := InboundMessage{
		Context: InboundContext{
			Channel:  "test",
			ChatID:   "chat-1",
			SenderID: "user-1",
		},
		Content:       "hello",
		ChannelOrigin: true,
	}
	if err := mb.PublishInbound(context.Background(), msg); err != nil {
		t.Fatalf("PublishInbound failed: %v", err)
	}
	if !called {
		t.Fatal("admission hook was not called")
	}
	if got := len(mb.inbound); got != 1 {
		t.Fatalf("inbound queue depth after publish = %d, want 1", got)
	}
}

func TestPublishInbound_AdmissionErrorPublishesFailureWithoutQueueing(t *testing.T) {
	eventBus := runtimeevents.NewBus()
	defer func() {
		if err := eventBus.Close(); err != nil {
			t.Errorf("event bus close failed: %v", err)
		}
	}()
	_, eventsCh, err := eventBus.Channel().OfKind(runtimeevents.KindBusPublishFailed).
		SubscribeChan(t.Context(), runtimeevents.SubscribeOptions{Name: "admission-failure", Buffer: 1})
	if err != nil {
		t.Fatalf("SubscribeChan failed: %v", err)
	}

	mb := NewMessageBus()
	defer mb.Close()
	mb.SetEventPublisher(eventBus)
	admissionErr := errors.New("durable event insert failed")
	mb.SetInboundAdmission(inboundAdmissionFunc(func(context.Context, InboundMessage) (bool, error) {
		return false, admissionErr
	}))

	err = mb.PublishInbound(context.Background(), InboundMessage{
		Context: InboundContext{
			Channel:  "test",
			ChatID:   "chat-1",
			SenderID: "user-1",
		},
		ChannelOrigin: true,
	})
	if !errors.Is(err, admissionErr) {
		t.Fatalf("PublishInbound error = %v, want %v", err, admissionErr)
	}
	if got := len(mb.inbound); got != 0 {
		t.Fatalf("inbound queue depth = %d, want 0", got)
	}

	failed := receiveBusRuntimeEvent(t, eventsCh)
	if failed.Kind != runtimeevents.KindBusPublishFailed || failed.Source.Name != "inbound" {
		t.Fatalf("publish failure event = %+v", failed)
	}
	if failed.Attrs["error"] != admissionErr.Error() {
		t.Fatalf("publish failure error = %#v, want %q", failed.Attrs["error"], admissionErr)
	}
}

func TestPublishInbound_AdmissionCanConsumeWithoutQueueing(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	mb.SetInboundAdmission(inboundAdmissionFunc(func(context.Context, InboundMessage) (bool, error) {
		return false, nil
	}))
	err := mb.PublishInbound(context.Background(), InboundMessage{
		Context: InboundContext{
			Channel:  "test",
			ChatID:   "chat-1",
			SenderID: "user-1",
		},
		ChannelOrigin: true,
	})
	if err != nil {
		t.Fatalf("PublishInbound failed: %v", err)
	}
	if got := len(mb.inbound); got != 0 {
		t.Fatalf("inbound queue depth = %d, want 0", got)
	}
}

func TestPublishInbound_AdmissionSeesNormalizedIsolatedMetadata(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	originalTime := time.Date(2026, time.July, 28, 12, 30, 0, 123, time.FixedZone("test", -4*60*60))
	originalRaw := map[string]string{"safe": "original"}
	originalHandles := map[string]string{"thread": "reply-1"}
	originalMedia := []string{"media://one"}
	originalAttachments := []InboundAttachment{{
		Filename:    "report.pdf",
		ContentType: "application/pdf",
		Kind:        "file",
		SizeBytes:   42,
	}}

	mb.SetInboundAdmission(inboundAdmissionFunc(func(_ context.Context, msg InboundMessage) (bool, error) {
		if msg.Context.Channel != "slack" || msg.Context.ChatType != "group" {
			t.Errorf("admission context = %+v, want trimmed channel and normalized chat type", msg.Context)
		}
		if msg.EventDedupeID != "event-1" ||
			msg.EventSubject != "Release notice" ||
			msg.ConversationName != "Release room" {
			t.Errorf(
				"admission event metadata = %q/%q/%q",
				msg.EventDedupeID,
				msg.EventSubject,
				msg.ConversationName,
			)
		}
		if msg.Context.EventDedupeID != msg.EventDedupeID ||
			msg.Context.EventSubject != msg.EventSubject ||
			msg.Context.ConversationName != msg.ConversationName {
			t.Errorf("context metadata was not mirrored to message: %+v", msg)
		}
		if !msg.EventSenderVerified ||
			!msg.EventTransportAuthenticated ||
			!msg.Context.EventSenderVerified ||
			!msg.Context.EventTransportAuthenticated {
			t.Errorf("email trust metadata was not mirrored: %+v", msg)
		}
		if msg.OccurredAt == nil || !msg.OccurredAt.Equal(originalTime.UTC()) ||
			msg.OccurredAt.Location() != time.UTC {
			t.Errorf("admission occurred_at = %v, want UTC %v", msg.OccurredAt, originalTime.UTC())
		}
		if msg.Context.OccurredAt == msg.OccurredAt ||
			&msg.Context.Attachments[0] == &msg.Attachments[0] {
			t.Error("context and top-level event metadata share mutable storage")
		}

		msg.Context.Raw["safe"] = "hook"
		msg.Context.ReplyHandles["thread"] = "hook"
		msg.Media[0] = "hook"
		msg.Attachments[0].Filename = "hook"
		msg.Context.Attachments[0].Filename = "context-hook"
		*msg.OccurredAt = time.Time{}
		*msg.Context.OccurredAt = time.Time{}
		return true, nil
	}))

	input := InboundMessage{
		Context: InboundContext{
			Channel:                     " slack ",
			ChatID:                      " chat-1 ",
			ChatType:                    " GROUP ",
			SenderID:                    " user-1 ",
			Raw:                         originalRaw,
			ReplyHandles:                originalHandles,
			EventDedupeID:               " event-1 ",
			OccurredAt:                  &originalTime,
			EventSubject:                " Release notice ",
			ConversationName:            " Release room ",
			Attachments:                 originalAttachments,
			EventSenderVerified:         true,
			EventTransportAuthenticated: true,
		},
		Content:       "hello",
		Media:         originalMedia,
		ChannelOrigin: true,
	}
	if err := mb.PublishInbound(context.Background(), input); err != nil {
		t.Fatalf("PublishInbound failed: %v", err)
	}

	if originalRaw["safe"] != "original" ||
		originalHandles["thread"] != "reply-1" ||
		originalMedia[0] != "media://one" ||
		originalAttachments[0].Filename != "report.pdf" ||
		originalTime.IsZero() {
		t.Fatal("admission mutated caller-owned metadata")
	}

	queued := <-mb.InboundChan()
	if queued.Context.Raw["safe"] != "original" ||
		queued.Context.ReplyHandles["thread"] != "reply-1" ||
		queued.Media[0] != "media://one" ||
		queued.Attachments[0].Filename != "report.pdf" ||
		queued.Context.Attachments[0].Filename != "report.pdf" ||
		queued.EventSubject != "Release notice" ||
		queued.Context.EventSubject != "Release notice" ||
		!queued.EventSenderVerified ||
		!queued.EventTransportAuthenticated ||
		!queued.Context.EventSenderVerified ||
		!queued.Context.EventTransportAuthenticated ||
		queued.OccurredAt == nil || queued.OccurredAt.IsZero() ||
		queued.Context.OccurredAt == nil || queued.Context.OccurredAt.IsZero() {
		t.Fatalf("admission mutated queued metadata: %+v", queued)
	}
}

func TestPublishInbound_InternalMessageBypassesAdmission(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	calls := 0
	mb.SetInboundAdmission(inboundAdmissionFunc(func(context.Context, InboundMessage) (bool, error) {
		calls++
		return false, nil
	}))
	err := mb.PublishInbound(context.Background(), InboundMessage{
		Context: InboundContext{
			Channel:  "internal",
			ChatID:   "job-1",
			SenderID: "system",
		},
		Content: "run",
	})
	if err != nil {
		t.Fatalf("PublishInbound failed: %v", err)
	}
	if calls != 0 {
		t.Fatalf("admission calls = %d, want 0", calls)
	}
	if got := <-mb.InboundChan(); got.Content != "run" {
		t.Fatalf("queued content = %q, want run", got.Content)
	}
}

func TestPublishInboundPreparationRunsAfterAdmissionOnlyForForwardedMessage(t *testing.T) {
	t.Run("forwarded", func(t *testing.T) {
		mb := NewMessageBus()
		defer mb.Close()

		order := make([]string, 0, 2)
		mb.SetInboundAdmission(inboundAdmissionFunc(
			func(context.Context, InboundMessage) (bool, error) {
				order = append(order, "admission")
				return true, nil
			},
		))
		err := mb.PublishInboundWithPreparation(
			context.Background(),
			InboundMessage{
				Context: InboundContext{
					Channel:  "test",
					ChatID:   "chat-1",
					SenderID: "user-1",
				},
				ChannelOrigin: true,
			},
			func() { order = append(order, "prepare") },
		)
		if err != nil {
			t.Fatalf("PublishInboundWithPreparation failed: %v", err)
		}
		if len(order) != 2 || order[0] != "admission" || order[1] != "prepare" {
			t.Fatalf("callback order = %#v, want [admission prepare]", order)
		}
		if got := len(mb.InboundChan()); got != 1 {
			t.Fatalf("inbound queue depth = %d, want 1", got)
		}
	})

	t.Run("consumed", func(t *testing.T) {
		mb := NewMessageBus()
		defer mb.Close()

		prepared := false
		mb.SetInboundAdmission(inboundAdmissionFunc(
			func(context.Context, InboundMessage) (bool, error) {
				return false, nil
			},
		))
		err := mb.PublishInboundWithPreparation(
			context.Background(),
			InboundMessage{
				Context: InboundContext{
					Channel:  "test",
					ChatID:   "chat-1",
					SenderID: "user-1",
				},
				ChannelOrigin: true,
			},
			func() { prepared = true },
		)
		if err != nil {
			t.Fatalf("PublishInboundWithPreparation failed: %v", err)
		}
		if prepared {
			t.Fatal("preparation ran for admission-consumed message")
		}
		if got := len(mb.InboundChan()); got != 0 {
			t.Fatalf("inbound queue depth = %d, want 0", got)
		}
	})

	t.Run("rejected", func(t *testing.T) {
		mb := NewMessageBus()
		defer mb.Close()

		wantErr := errors.New("durable insert failed")
		prepared := false
		mb.SetInboundAdmission(inboundAdmissionFunc(
			func(context.Context, InboundMessage) (bool, error) {
				return false, wantErr
			},
		))
		err := mb.PublishInboundWithPreparation(
			context.Background(),
			InboundMessage{
				Context: InboundContext{
					Channel:  "test",
					ChatID:   "chat-1",
					SenderID: "user-1",
				},
				ChannelOrigin: true,
			},
			func() { prepared = true },
		)
		if !errors.Is(err, wantErr) {
			t.Fatalf("PublishInboundWithPreparation error = %v, want %v", err, wantErr)
		}
		if prepared {
			t.Fatal("preparation ran for admission-rejected message")
		}
		if got := len(mb.InboundChan()); got != 0 {
			t.Fatalf("inbound queue depth = %d, want 0", got)
		}
	})
}

func TestPublishInboundTransactionalPreparationRollsBackWhenQueueingIsCanceled(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()
	for index := 0; index < cap(mb.inbound); index++ {
		err := mb.PublishInbound(context.Background(), InboundMessage{
			Context: InboundContext{
				Channel:  "test",
				ChatID:   "occupied",
				SenderID: "user",
			},
		})
		if err != nil {
			t.Fatalf("fill inbound queue: %v", err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	prepared := make(chan struct{})
	rolledBack := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, err := mb.PublishInboundWithTransactionalPreparationResult(
			ctx,
			InboundMessage{
				Context: InboundContext{
					Channel:  "test",
					ChatID:   "chat-1",
					SenderID: "user-1",
				},
			},
			func() func() {
				close(prepared)
				return func() { close(rolledBack) }
			},
		)
		result <- err
	}()

	select {
	case <-prepared:
	case <-time.After(time.Second):
		t.Fatal("transactional preparation did not run")
	}
	cancel()
	select {
	case <-rolledBack:
	case <-time.After(time.Second):
		t.Fatal("transactional preparation was not rolled back")
	}
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("publish error = %v, want context.Canceled", err)
	}
	if got := len(mb.inbound); got != cap(mb.inbound) {
		t.Fatalf("inbound queue depth = %d, want %d", got, cap(mb.inbound))
	}
}

func TestPublishInboundTransactionalRollbackDoesNotBlockBusClose(t *testing.T) {
	mb := NewMessageBus()
	for index := 0; index < cap(mb.inbound); index++ {
		err := mb.PublishInbound(context.Background(), InboundMessage{
			Context: InboundContext{
				Channel:  "test",
				ChatID:   "occupied",
				SenderID: "user",
			},
		})
		if err != nil {
			t.Fatalf("fill inbound queue: %v", err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	prepared := make(chan struct{})
	rollbackStarted := make(chan struct{})
	releaseRollback := make(chan struct{})
	publishResult := make(chan error, 1)
	go func() {
		_, err := mb.PublishInboundWithTransactionalPreparationResult(
			ctx,
			InboundMessage{
				Context: InboundContext{
					Channel:  "test",
					ChatID:   "chat-1",
					SenderID: "user-1",
				},
			},
			func() func() {
				close(prepared)
				return func() {
					close(rollbackStarted)
					<-releaseRollback
				}
			},
		)
		publishResult <- err
	}()

	select {
	case <-prepared:
	case <-time.After(time.Second):
		t.Fatal("transactional preparation did not run")
	}
	cancel()
	select {
	case <-rollbackStarted:
	case <-time.After(time.Second):
		t.Fatal("transactional rollback did not start")
	}

	closeDone := make(chan struct{})
	go func() {
		mb.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("MessageBus.Close waited for provider rollback")
	}

	close(releaseRollback)
	if err := <-publishResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("publish error = %v, want context.Canceled", err)
	}
}

func TestPublishInbound_ClosedBusDoesNotRunAdmission(t *testing.T) {
	mb := NewMessageBus()
	calls := 0
	mb.SetInboundAdmission(inboundAdmissionFunc(func(context.Context, InboundMessage) (bool, error) {
		calls++
		return false, nil
	}))
	mb.Close()

	err := mb.PublishInbound(context.Background(), InboundMessage{
		Context: InboundContext{
			Channel:  "test",
			ChatID:   "chat-1",
			SenderID: "user-1",
		},
		ChannelOrigin: true,
	})
	if !errors.Is(err, ErrBusClosed) {
		t.Fatalf("PublishInbound error = %v, want ErrBusClosed", err)
	}
	if calls != 0 {
		t.Fatalf("admission calls = %d, want 0", calls)
	}
}

func TestCloseCancelsInFlightAdmission(t *testing.T) {
	mb := NewMessageBus()
	admissionStarted := make(chan struct{})
	mb.SetInboundAdmission(inboundAdmissionFunc(
		func(ctx context.Context, _ InboundMessage) (bool, error) {
			close(admissionStarted)
			<-ctx.Done()
			return false, ctx.Err()
		},
	))

	published := make(chan error, 1)
	go func() {
		published <- mb.PublishInbound(context.Background(), InboundMessage{
			Context: InboundContext{
				Channel:  "test",
				ChatID:   "chat-1",
				SenderID: "user-1",
			},
			ChannelOrigin: true,
		})
	}()
	select {
	case <-admissionStarted:
	case <-time.After(time.Second):
		t.Fatal("admission did not start")
	}

	closed := make(chan struct{})
	go func() {
		mb.Close()
		close(closed)
	}()
	select {
	case err := <-published:
		if !errors.Is(err, ErrBusClosed) {
			t.Fatalf("PublishInbound() error = %v, want ErrBusClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("PublishInbound did not unblock during Close")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not wait for and finish canceled admission")
	}
}

func TestInboundAdmissionMetadataIsNotSerialized(t *testing.T) {
	now := time.Now()
	encoded, err := json.Marshal(InboundMessage{
		Context: InboundContext{
			TurnUXID:         "process-local-turn-ux",
			EventDedupeID:    "context-event-secret",
			OccurredAt:       &now,
			EventSubject:     "context private subject",
			ConversationName: "context private room",
			Attachments: []InboundAttachment{{
				Filename: "context-private.txt",
			}},
			EventSenderVerified:         true,
			EventTransportAuthenticated: true,
		},
		ChannelOrigin:               true,
		EventDedupeID:               "event-secret",
		OccurredAt:                  &now,
		EventSubject:                "private subject",
		ConversationName:            "private room",
		EventSenderVerified:         true,
		EventTransportAuthenticated: true,
		Attachments: []InboundAttachment{{
			Filename:    "private.txt",
			ContentType: "text/plain",
			Kind:        "file",
			SizeBytes:   10,
		}},
	})
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	for _, forbidden := range []string{
		"event-secret",
		"private room",
		"private.txt",
		"context-event-secret",
		"context private subject",
		"context private room",
		"context-private.txt",
		"process-local-turn-ux",
		"private subject",
		"channel_origin",
		"occurred_at",
		"EventSenderVerified",
		"EventTransportAuthenticated",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("serialized inbound message contains admission metadata %q: %s", forbidden, encoded)
		}
	}
}

func TestPublishInbound_NormalizesContext(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	msg := InboundMessage{
		Context: InboundContext{
			Channel:          "slack",
			Account:          "workspace-a",
			ChatID:           "C456/1712",
			ChatType:         "group",
			TopicID:          "1712",
			SpaceID:          "T001",
			SpaceType:        "team",
			SenderID:         "U123",
			MessageID:        "1712.01",
			ReplyToMessageID: "1700.01",
			Mentioned:        true,
		},
		Content: "hello",
	}

	if err := mb.PublishInbound(context.Background(), msg); err != nil {
		t.Fatalf("PublishInbound failed: %v", err)
	}

	got := <-mb.InboundChan()
	if got.Context.Channel != "slack" {
		t.Fatalf("expected context channel slack, got %q", got.Context.Channel)
	}
	if got.Context.Account != "workspace-a" {
		t.Fatalf("expected context account workspace-a, got %q", got.Context.Account)
	}
	if got.Context.ChatType != "group" {
		t.Fatalf("expected context chat type group, got %q", got.Context.ChatType)
	}
	if got.Context.TopicID != "1712" {
		t.Fatalf("expected topic 1712, got %q", got.Context.TopicID)
	}
	if got.Context.SpaceType != "team" || got.Context.SpaceID != "T001" {
		t.Fatalf("expected team space T001, got %q/%q", got.Context.SpaceType, got.Context.SpaceID)
	}
	if !got.Context.Mentioned {
		t.Fatal("expected mentioned=true in context")
	}
	if got.Context.ReplyToMessageID != "1700.01" {
		t.Fatalf("expected reply_to_message_id 1700.01, got %q", got.Context.ReplyToMessageID)
	}
}

func TestPublishInbound_MirrorsContextIntoConvenienceFields(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	msg := InboundMessage{
		Context: InboundContext{
			Channel:          "telegram",
			Account:          "bot-a",
			ChatID:           "-1001",
			ChatType:         "group",
			TopicID:          "42",
			SpaceID:          "guild-9",
			SpaceType:        "guild",
			SenderID:         "user-1",
			MessageID:        "777",
			Mentioned:        true,
			ReplyToMessageID: "666",
		},
		Content: "hi",
	}

	if err := mb.PublishInbound(context.Background(), msg); err != nil {
		t.Fatalf("PublishInbound failed: %v", err)
	}

	got := <-mb.InboundChan()
	if got.Channel != "telegram" {
		t.Fatalf("expected legacy channel telegram, got %q", got.Channel)
	}
	if got.ChatID != "-1001" {
		t.Fatalf("expected legacy chat ID -1001, got %q", got.ChatID)
	}
	if got.SenderID != "user-1" {
		t.Fatalf("expected legacy sender ID user-1, got %q", got.SenderID)
	}
	if got.MessageID != "777" {
		t.Fatalf("expected legacy message ID 777, got %q", got.MessageID)
	}
	if got.Context.Account != "bot-a" || got.Context.SpaceID != "guild-9" || got.Context.TopicID != "42" {
		t.Fatalf("unexpected normalized context: %+v", got.Context)
	}
}

func TestPublishInbound_BackfillsContextFromLegacyFields(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	msg := InboundMessage{
		Channel:   "pico",
		ChatID:    "session-1",
		SenderID:  "user-1",
		MessageID: "msg-1",
		Content:   "hello",
	}

	if err := mb.PublishInbound(context.Background(), msg); err != nil {
		t.Fatalf("PublishInbound failed: %v", err)
	}

	got := <-mb.InboundChan()
	if got.Context.Channel != "pico" {
		t.Fatalf("expected context channel pico, got %q", got.Context.Channel)
	}
	if got.Context.ChatID != "session-1" {
		t.Fatalf("expected context chat ID session-1, got %q", got.Context.ChatID)
	}
	if got.Context.SenderID != "user-1" {
		t.Fatalf("expected context sender ID user-1, got %q", got.Context.SenderID)
	}
	if got.Context.MessageID != "msg-1" {
		t.Fatalf("expected context message ID msg-1, got %q", got.Context.MessageID)
	}
}

func TestMessageBusPublishesRuntimeFailureAndCloseEvents(t *testing.T) {
	eventBus := runtimeevents.NewBus()
	defer func() {
		if err := eventBus.Close(); err != nil {
			t.Errorf("event bus close failed: %v", err)
		}
	}()

	_, eventsCh, err := eventBus.Channel().OfKind(
		runtimeevents.KindBusPublishFailed,
		runtimeevents.KindBusCloseStarted,
		runtimeevents.KindBusCloseDrained,
		runtimeevents.KindBusCloseCompleted,
	).SubscribeChan(t.Context(), runtimeevents.SubscribeOptions{Name: "bus-events", Buffer: 4})
	if err != nil {
		t.Fatalf("SubscribeChan failed: %v", err)
	}

	mb := NewMessageBus()
	mb.SetEventPublisher(eventBus)

	if err := mb.PublishInbound(context.Background(), InboundMessage{}); err == nil {
		t.Fatal("expected PublishInbound to fail")
	}
	failed := receiveBusRuntimeEvent(t, eventsCh)
	if failed.Kind != runtimeevents.KindBusPublishFailed ||
		failed.Source.Name != "inbound" ||
		failed.Severity != runtimeevents.SeverityError {
		t.Fatalf("publish failed event = %+v", failed)
	}
	if failed.Attrs["stream"] != "inbound" || failed.Attrs["error"] == "" {
		t.Fatalf("publish failed attrs = %#v, want stream and error", failed.Attrs)
	}

	if err := mb.PublishOutbound(context.Background(), OutboundMessage{
		Context: NewOutboundContext("telegram", "chat-1", ""),
		Content: "queued",
	}); err != nil {
		t.Fatalf("PublishOutbound failed: %v", err)
	}
	mb.Close()

	seen := map[runtimeevents.Kind]bool{}
	var drainedAttrs map[string]any
	for range 3 {
		evt := receiveBusRuntimeEvent(t, eventsCh)
		seen[evt.Kind] = true
		if evt.Kind == runtimeevents.KindBusCloseDrained {
			drainedAttrs = evt.Attrs
		}
	}
	for _, kind := range []runtimeevents.Kind{
		runtimeevents.KindBusCloseStarted,
		runtimeevents.KindBusCloseDrained,
		runtimeevents.KindBusCloseCompleted,
	} {
		if !seen[kind] {
			t.Fatalf("missing %s event, seen=%v", kind, seen)
		}
	}
	if drainedAttrs["drained"] != 1 {
		t.Fatalf("bus close drained attrs = %#v, want drained count", drainedAttrs)
	}
}

func receiveBusRuntimeEvent(t *testing.T, ch <-chan runtimeevents.Event) runtimeevents.Event {
	t.Helper()

	select {
	case evt, ok := <-ch:
		if !ok {
			t.Fatal("runtime event channel closed before expected event")
		}
		return evt
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for runtime event")
		return runtimeevents.Event{}
	}
}

func TestPublishOutboundSubscribe(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	ctx := context.Background()

	msg := OutboundMessage{
		Context: InboundContext{
			Channel: "telegram",
			ChatID:  "123",
		},
		Content: "world",
	}

	if err := mb.PublishOutbound(ctx, msg); err != nil {
		t.Fatalf("PublishOutbound failed: %v", err)
	}

	got, ok := <-mb.OutboundChan()
	if !ok {
		t.Fatal("SubscribeOutbound returned ok=false")
	}
	if got.Content != "world" {
		t.Fatalf("expected content 'world', got %q", got.Content)
	}
	if got.Context.Channel != "telegram" || got.Context.ChatID != "123" {
		t.Fatalf("expected normalized outbound context, got %+v", got.Context)
	}
}

func TestPublishOutbound_MirrorsContextToLegacyFields(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	msg := OutboundMessage{
		Context: InboundContext{
			Channel:          "telegram",
			ChatID:           "chat-42",
			ReplyToMessageID: "msg-9",
		},
		AgentID:    "main",
		SessionKey: "sk_v1_123",
		Scope: &OutboundScope{
			Version:    1,
			AgentID:    "main",
			Channel:    "telegram",
			Account:    "bot-a",
			Dimensions: []string{"chat", "sender"},
			Values: map[string]string{
				"chat":   "direct:chat-42",
				"sender": "user-1",
			},
		},
		Content: "reply",
	}

	if err := mb.PublishOutbound(context.Background(), msg); err != nil {
		t.Fatalf("PublishOutbound failed: %v", err)
	}

	got := <-mb.OutboundChan()
	if got.Channel != "telegram" {
		t.Fatalf("expected legacy channel telegram, got %q", got.Channel)
	}
	if got.ChatID != "chat-42" {
		t.Fatalf("expected legacy chat ID chat-42, got %q", got.ChatID)
	}
	if got.ReplyToMessageID != "msg-9" {
		t.Fatalf("expected mirrored reply_to_message_id msg-9, got %q", got.ReplyToMessageID)
	}
	if got.AgentID != "main" || got.SessionKey != "sk_v1_123" {
		t.Fatalf("unexpected outbound turn metadata: agent=%q session=%q", got.AgentID, got.SessionKey)
	}
	if got.Scope == nil || got.Scope.AgentID != "main" || got.Scope.Values["chat"] != "direct:chat-42" {
		t.Fatalf("unexpected outbound scope: %+v", got.Scope)
	}
	if got.Context.Channel != "telegram" || got.Context.ChatID != "chat-42" {
		t.Fatalf("unexpected outbound context: %+v", got.Context)
	}
}

func TestPublishOutbound_PreservesExplicitReplyToMessageID(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	msg := OutboundMessage{
		Context: InboundContext{
			Channel: "telegram",
			ChatID:  "chat-42",
		},
		ReplyToMessageID: "msg-9",
		Content:          "reply",
	}

	if err := mb.PublishOutbound(context.Background(), msg); err != nil {
		t.Fatalf("PublishOutbound failed: %v", err)
	}

	got := <-mb.OutboundChan()
	if got.ReplyToMessageID != "msg-9" {
		t.Fatalf("expected mirrored reply_to_message_id msg-9, got %q", got.ReplyToMessageID)
	}
	if got.Context.ReplyToMessageID != "msg-9" {
		t.Fatalf("expected context reply_to_message_id msg-9, got %q", got.Context.ReplyToMessageID)
	}
}

func TestPublishOutbound_PreservesExplicitReplyToMessageIDWhenContextReplyIsBlank(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	msg := OutboundMessage{
		Context: InboundContext{
			Channel:          "telegram",
			ChatID:           "chat-42",
			ReplyToMessageID: "   ",
		},
		ReplyToMessageID: "msg-9",
		Content:          "reply",
	}

	if err := mb.PublishOutbound(context.Background(), msg); err != nil {
		t.Fatalf("PublishOutbound failed: %v", err)
	}

	got := <-mb.OutboundChan()
	if got.ReplyToMessageID != "msg-9" {
		t.Fatalf("expected mirrored reply_to_message_id msg-9, got %q", got.ReplyToMessageID)
	}
	if got.Context.ReplyToMessageID != "msg-9" {
		t.Fatalf("expected context reply_to_message_id msg-9, got %q", got.Context.ReplyToMessageID)
	}
}

func TestPublishOutboundMedia_MirrorsContextToLegacyFields(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	msg := OutboundMediaMessage{
		Context: InboundContext{
			Channel: "slack",
			ChatID:  "C001",
		},
		AgentID:    "support",
		SessionKey: "sk_v1_media",
		Scope: &OutboundScope{
			Version:    1,
			AgentID:    "support",
			Channel:    "slack",
			Dimensions: []string{"chat"},
			Values: map[string]string{
				"chat": "channel:c001",
			},
		},
		Parts: []MediaPart{{Type: "image", Ref: "media://1"}},
	}

	if err := mb.PublishOutboundMedia(context.Background(), msg); err != nil {
		t.Fatalf("PublishOutboundMedia failed: %v", err)
	}

	got := <-mb.OutboundMediaChan()
	if got.Channel != "slack" {
		t.Fatalf("expected legacy channel slack, got %q", got.Channel)
	}
	if got.ChatID != "C001" {
		t.Fatalf("expected legacy chat ID C001, got %q", got.ChatID)
	}
	if got.AgentID != "support" || got.SessionKey != "sk_v1_media" {
		t.Fatalf("unexpected outbound media turn metadata: agent=%q session=%q", got.AgentID, got.SessionKey)
	}
	if got.Scope == nil || got.Scope.Values["chat"] != "channel:c001" {
		t.Fatalf("unexpected outbound media scope: %+v", got.Scope)
	}
	if got.Context.Channel != "slack" || got.Context.ChatID != "C001" {
		t.Fatalf("unexpected outbound media context: %+v", got.Context)
	}
}

func TestPublishAudioChunkSubscribe(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	chunk := AudioChunk{
		SessionID: "voice-1",
		SpeakerID: "speaker-1",
		ChatID:    "chat-1",
		Channel:   "discord",
		Sequence:  7,
		Format:    "opus",
		Data:      []byte{0x01, 0x02},
	}

	if err := mb.PublishAudioChunk(context.Background(), chunk); err != nil {
		t.Fatalf("PublishAudioChunk failed: %v", err)
	}

	got, ok := <-mb.AudioChunksChan()
	if !ok {
		t.Fatal("AudioChunksChan returned ok=false")
	}
	if got.SessionID != "voice-1" || got.Sequence != 7 {
		t.Fatalf("unexpected audio chunk: %+v", got)
	}
}

func TestPublishAudioChunk_BackpressureDropPublishesRuntimeEvent(t *testing.T) {
	eventBus := runtimeevents.NewBus()
	defer func() {
		if err := eventBus.Close(); err != nil {
			t.Errorf("event bus close failed: %v", err)
		}
	}()

	_, eventsCh, err := eventBus.Channel().OfKind(runtimeevents.KindBusMessageDropped).SubscribeChan(
		t.Context(),
		runtimeevents.SubscribeOptions{Name: "bus-drop-events", Buffer: 1},
	)
	if err != nil {
		t.Fatalf("SubscribeChan failed: %v", err)
	}

	mb := NewMessageBus()
	defer mb.Close()
	mb.SetEventPublisher(eventBus)

	for i := range defaultBusBufferSize * 4 {
		if pubErr := mb.PublishAudioChunk(context.Background(), AudioChunk{
			SessionID: "voice-1",
			SpeakerID: "speaker-1",
			ChatID:    "chat-1",
			Channel:   "discord",
			Sequence:  uint64(i),
			Format:    "opus",
			Data:      []byte{0x01},
		}); pubErr != nil {
			t.Fatalf("fill failed at %d: %v", i, pubErr)
		}
	}

	err = mb.PublishAudioChunk(context.Background(), AudioChunk{
		SessionID: "voice-1",
		SpeakerID: "speaker-1",
		ChatID:    "chat-1",
		Channel:   "discord",
		Sequence:  999,
		Format:    "opus",
		Data:      []byte{0x01},
	})
	if !errors.Is(err, ErrBusBackpressure) {
		t.Fatalf("PublishAudioChunk() error = %v, want %v", err, ErrBusBackpressure)
	}

	evt := receiveBusRuntimeEvent(t, eventsCh)
	if evt.Kind != runtimeevents.KindBusMessageDropped ||
		evt.Source.Name != "audio_chunk" ||
		evt.Severity != runtimeevents.SeverityWarn {
		t.Fatalf("drop event = %+v", evt)
	}
	if evt.Scope.Channel != "discord" || evt.Scope.ChatID != "chat-1" {
		t.Fatalf("drop event scope = %+v", evt.Scope)
	}
	if evt.Attrs["stream"] != "audio_chunk" ||
		evt.Attrs["reason"] != "queue_full_timeout" ||
		evt.Attrs["wait_ms"] != defaultAudioPublishTimeout.Milliseconds() ||
		evt.Attrs["queue_depth"] != defaultBusBufferSize*4 ||
		evt.Attrs["queue_capacity"] != defaultBusBufferSize*4 ||
		evt.Attrs["dropped_total"] != uint64(1) {
		t.Fatalf("drop event attrs = %#v", evt.Attrs)
	}

	stats := mb.Stats()
	if stats.AudioChunks.DroppedTotal != 1 {
		t.Fatalf("AudioChunks dropped = %d, want 1", stats.AudioChunks.DroppedTotal)
	}
	if stats.AudioChunks.Depth != defaultBusBufferSize*4 {
		t.Fatalf("AudioChunks depth = %d, want %d", stats.AudioChunks.Depth, defaultBusBufferSize*4)
	}
	wantWaitMS := defaultAudioPublishTimeout.Milliseconds()
	if stats.AudioChunks.LastDropWaitMillis != wantWaitMS {
		t.Fatalf("AudioChunks last wait ms = %d, want %d", stats.AudioChunks.LastDropWaitMillis, wantWaitMS)
	}
}

func TestPublishVoiceControlSubscribe(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	ctrl := VoiceControl{
		SessionID: "voice-1",
		ChatID:    "chat-1",
		Type:      "command",
		Action:    "start",
	}

	if err := mb.PublishVoiceControl(context.Background(), ctrl); err != nil {
		t.Fatalf("PublishVoiceControl failed: %v", err)
	}

	got, ok := <-mb.VoiceControlsChan()
	if !ok {
		t.Fatal("VoiceControlsChan returned ok=false")
	}
	if got.Type != "command" || got.Action != "start" {
		t.Fatalf("unexpected voice control: %+v", got)
	}
}

func TestNewOutboundContext_NormalizesReplyAddress(t *testing.T) {
	ctx := NewOutboundContext(" telegram ", " chat-42 ", " msg-9 ")
	if ctx.Channel != "telegram" {
		t.Fatalf("expected channel telegram, got %q", ctx.Channel)
	}
	if ctx.ChatID != "chat-42" {
		t.Fatalf("expected chat_id chat-42, got %q", ctx.ChatID)
	}
	if ctx.ReplyToMessageID != "msg-9" {
		t.Fatalf("expected reply_to_message_id msg-9, got %q", ctx.ReplyToMessageID)
	}
}

func TestPublishInbound_ContextCancel(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	// Fill the buffer
	ctx := context.Background()
	for i := range defaultBusBufferSize {
		if err := mb.PublishInbound(ctx, InboundMessage{
			Context: InboundContext{
				Channel:  "test",
				ChatID:   "chat-fill",
				ChatType: "direct",
				SenderID: "user-fill",
			},
			Content: "fill",
		}); err != nil {
			t.Fatalf("fill failed at %d: %v", i, err)
		}
	}

	// Now buffer is full; publish with a canceled context
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()

	err := mb.PublishInbound(cancelCtx, InboundMessage{
		Context: InboundContext{
			Channel:  "test",
			ChatID:   "chat-overflow",
			ChatType: "direct",
			SenderID: "user-overflow",
		},
		Content: "overflow",
	})
	if err == nil {
		t.Fatal("expected error from canceled context, got nil")
	}
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestPublishInbound_BusClosed(t *testing.T) {
	mb := NewMessageBus()
	mb.Close()

	err := mb.PublishInbound(context.Background(), InboundMessage{
		Context: InboundContext{
			Channel:  "test",
			ChatID:   "chat1",
			ChatType: "direct",
			SenderID: "user1",
		},
		Content: "test",
	})
	if err != ErrBusClosed {
		t.Fatalf("expected ErrBusClosed, got %v", err)
	}
}

func TestPublishOutbound_BusClosed(t *testing.T) {
	mb := NewMessageBus()
	mb.Close()

	err := mb.PublishOutbound(context.Background(), OutboundMessage{
		Context: InboundContext{
			Channel: "test",
			ChatID:  "chat1",
		},
		Content: "test",
	})
	if err != ErrBusClosed {
		t.Fatalf("expected ErrBusClosed, got %v", err)
	}
}

func TestConsumeInbound_ContextCancel(t *testing.T) {
	mb := NewMessageBus()

	defer mb.Close()

	for i := range defaultBusBufferSize {
		if err := mb.PublishInbound(context.Background(), InboundMessage{
			Context: InboundContext{
				Channel:  "test",
				ChatID:   "chat-fill",
				ChatType: "direct",
				SenderID: "user-fill",
			},
			Content: "fill",
		}); err != nil {
			t.Fatalf("fill failed at %d: %v", i, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	mb.PublishInbound(ctx, InboundMessage{
		Context: InboundContext{
			Channel:  "test",
			ChatID:   "chat-cancel",
			ChatType: "direct",
			SenderID: "user-cancel",
		},
		Content: "ContextCancel",
	})

	select {
	case <-ctx.Done():
		t.Log("context canceled, as expected")

	case msg, ok := <-mb.InboundChan():
		if !ok {
			t.Fatal("expected ok=false when context is canceled")
		}
		if msg.Content == "ContextCancel" {
			t.Fatalf("expected content 'ContextCancel', got %q", msg.Content)
		}
	}
}

func TestConsumeInbound_BusClosed(t *testing.T) {
	mb := NewMessageBus()

	timer := time.AfterFunc(100*time.Millisecond, func() {
		mb.Close()
	})

	select {
	case <-timer.C:
		t.Log("context canceled, as expected")

	case _, ok := <-mb.InboundChan():
		if ok {
			t.Fatal("expected ok=false when context is canceled")
		}
	}
}

func TestSubscribeOutbound_BusClosed(t *testing.T) {
	mb := NewMessageBus()
	mb.Close()

	_, ok := <-mb.OutboundChan()
	if ok {
		t.Fatal("expected ok=false when bus is closed")
	}
}

func TestConcurrentPublishClose(t *testing.T) {
	mb := NewMessageBus()
	ctx := context.Background()

	const numGoroutines = 100
	var wg sync.WaitGroup
	wg.Add(numGoroutines + 1)

	// Spawn many goroutines trying to publish
	for range numGoroutines {
		go func() {
			defer wg.Done()
			// Use a short timeout context so we don't block forever after close
			publishCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
			defer cancel()
			// Errors are expected; we just must not panic or deadlock
			_ = mb.PublishInbound(publishCtx, InboundMessage{Content: "concurrent"})
		}()
	}

	// Close from another goroutine
	go func() {
		defer wg.Done()
		time.Sleep(5 * time.Millisecond)
		mb.Close()
	}()

	// Must complete without deadlock
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// success
	case <-time.After(5 * time.Second):
		t.Fatal("test timed out - possible deadlock")
	}
}

func TestPublishInbound_FullBuffer(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	ctx := context.Background()

	// Fill the buffer
	for i := range defaultBusBufferSize {
		if err := mb.PublishInbound(ctx, InboundMessage{
			Context: InboundContext{
				Channel:  "test",
				ChatID:   "chat-fill",
				ChatType: "direct",
				SenderID: "user-fill",
			},
			Content: "fill",
		}); err != nil {
			t.Fatalf("fill failed at %d: %v", i, err)
		}
	}

	// Buffer is full; publish with short timeout
	timeoutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	err := mb.PublishInbound(timeoutCtx, InboundMessage{
		Context: InboundContext{
			Channel:  "test",
			ChatID:   "chat-overflow",
			ChatType: "direct",
			SenderID: "user-overflow",
		},
		Content: "overflow",
	})
	if err == nil {
		t.Fatal("expected error when buffer is full and context times out")
	}
	if err != context.DeadlineExceeded {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}
}

// TestPublishInbound_FullBufferUsesBusBackpressureBudget exercises the generic
// publish() backpressure path directly (with a short 20ms timeout) rather than
// going through PublishInbound(). This avoids waiting for a long context timeout
// and keeps the test fast. Context validation and public-API wiring are covered
// by TestPublishInbound_FullBuffer and TestPublishInbound_ContextCancel.
func TestPublishInbound_FullBufferUsesBusBackpressureBudget(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	ch := make(chan InboundMessage, 1)
	ch <- InboundMessage{Content: "fill"}

	scope := runtimeevents.Scope{Channel: "test", ChatID: "chat-overflow"}
	err := publish(context.Background(), mb, ch, InboundMessage{Content: "overflow"}, publishPolicy{
		stream:  "inbound",
		timeout: 20 * time.Millisecond,
	}, &mb.inboundStats, scope)
	if !errors.Is(err, ErrBusBackpressure) {
		t.Fatalf("publish() error = %v, want %v", err, ErrBusBackpressure)
	}

	stats := mb.Stats()
	if stats.Inbound.DroppedTotal != 1 {
		t.Fatalf("Inbound dropped = %d, want 1", stats.Inbound.DroppedTotal)
	}
	if stats.Inbound.LastDropWaitMillis != 20 {
		t.Fatalf("Inbound last wait ms = %d, want 20", stats.Inbound.LastDropWaitMillis)
	}
}

func TestMessageBusHealthCheckIncludesQueueDepthAndDrops(t *testing.T) {
	mb := NewMessageBus()
	defer mb.Close()

	ok, msg := mb.HealthCheck()
	if !ok {
		t.Fatal("HealthCheck should remain ok for backpressure telemetry")
	}
	if msg == "" {
		t.Fatal("HealthCheck message should not be empty")
	}

	for i := range cap(mb.audioChunks) {
		if err := mb.PublishAudioChunk(context.Background(), AudioChunk{
			Channel:  "discord",
			ChatID:   "voice-room",
			Sequence: uint64(i),
			Data:     []byte("fill"),
		}); err != nil {
			t.Fatalf("fill audio buffer at %d: %v", i, err)
		}
	}
	_ = mb.PublishAudioChunk(context.Background(), AudioChunk{
		Channel:  "discord",
		ChatID:   "voice-room",
		Sequence: 999,
		Data:     []byte("overflow"),
	})

	stats := mb.Stats()
	if stats.AudioChunks.Depth != cap(mb.audioChunks) {
		t.Fatalf("audio depth = %d, want %d", stats.AudioChunks.Depth, cap(mb.audioChunks))
	}
	if stats.AudioChunks.DroppedTotal != 1 {
		t.Fatalf("audio dropped = %d, want 1", stats.AudioChunks.DroppedTotal)
	}
}

func TestCloseIdempotent(t *testing.T) {
	mb := NewMessageBus()

	// Multiple Close calls must not panic
	mb.Close()
	mb.Close()
	mb.Close()

	// After close, publish should return ErrBusClosed
	err := mb.PublishInbound(context.Background(), InboundMessage{
		Context: InboundContext{
			Channel:  "test",
			ChatID:   "chat1",
			ChatType: "direct",
			SenderID: "user1",
		},
		Content: "test",
	})
	if err != ErrBusClosed {
		t.Fatalf("expected ErrBusClosed after multiple closes, got %v", err)
	}
}
