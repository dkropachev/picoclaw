package channelmessage

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/eventing"
)

type recordingInserter struct {
	mu sync.Mutex

	seen      map[string]eventing.StoredEvent
	attempts  []eventing.Envelope
	inserted  []eventing.Envelope
	insertErr error

	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newRecordingInserter() *recordingInserter {
	return &recordingInserter{seen: make(map[string]eventing.StoredEvent)}
}

func (store *recordingInserter) Insert(
	ctx context.Context,
	input eventing.Envelope,
) (eventing.InsertResult, error) {
	if store.started != nil {
		store.once.Do(func() { close(store.started) })
	}
	if store.release != nil {
		select {
		case <-store.release:
		case <-ctx.Done():
			return eventing.InsertResult{}, ctx.Err()
		}
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	store.attempts = append(store.attempts, input.Clone())
	if store.insertErr != nil {
		return eventing.InsertResult{}, store.insertErr
	}
	key := input.Source + "\x00" + input.Connector + "\x00" + input.DedupeKey
	if existing, ok := store.seen[key]; ok {
		return eventing.InsertResult{Event: existing, Inserted: false}, nil
	}
	stored := eventing.StoredEvent{Envelope: input.Clone()}
	store.seen[key] = stored
	store.inserted = append(store.inserted, input.Clone())
	return eventing.InsertResult{Event: stored, Inserted: true}, nil
}

func (store *recordingInserter) recordedAttempts() []eventing.Envelope {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := make([]eventing.Envelope, len(store.attempts))
	for index := range store.attempts {
		result[index] = store.attempts[index].Clone()
	}
	return result
}

func (store *recordingInserter) recordedInserts() []eventing.Envelope {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := make([]eventing.Envelope, len(store.inserted))
	for index := range store.inserted {
		result[index] = store.inserted[index].Clone()
	}
	return result
}

func testBackend(
	t *testing.T,
	store Inserter,
	adapters map[string]AdapterConfig,
	maxPayload int,
) *Backend {
	t.Helper()
	backend, err := NewBackend(BackendConfig{
		Store:           store,
		Adapters:        adapters,
		MaxPayloadBytes: maxPayload,
	})
	require.NoError(t, err)
	return backend
}

func testMessage(connector, stableID string) bus.InboundMessage {
	return bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:   connector,
			Account:   "account-a",
			ChatID:    "chat-a",
			SenderID:  "sender-a",
			MessageID: "local-message-a",
		},
		Sender: bus.SenderInfo{
			Platform:    "test",
			PlatformID:  "sender-a",
			CanonicalID: "test:sender-a",
		},
		Content:                     "hello",
		ChannelOrigin:               true,
		EventDedupeID:               stableID,
		EventSenderVerified:         true,
		EventTransportAuthenticated: true,
	}
}

func TestNewBackendValidationAndCopy(t *testing.T) {
	t.Parallel()
	store := newRecordingInserter()
	valid := BackendConfig{
		Store: store,
		Adapters: map[string]AdapterConfig{
			"chat-main": {
				Source:      SourceChat,
				Mode:        ModeMirror,
				ChannelType: "slack",
			},
		},
		MaxPayloadBytes: 1024,
	}
	tests := []struct {
		name   string
		mutate func(*BackendConfig)
	}{
		{name: "store", mutate: func(config *BackendConfig) { config.Store = nil }},
		{name: "payload maximum", mutate: func(config *BackendConfig) {
			config.MaxPayloadBytes = len(fallbackPayload) - 1
		}},
		{name: "adapters", mutate: func(config *BackendConfig) { config.Adapters = nil }},
		{name: "connector", mutate: func(config *BackendConfig) {
			config.Adapters = map[string]AdapterConfig{" bad ": valid.Adapters["chat-main"]}
		}},
		{name: "source", mutate: func(config *BackendConfig) {
			adapter := valid.Adapters["chat-main"]
			adapter.Source = "calendar"
			config.Adapters = map[string]AdapterConfig{"chat-main": adapter}
		}},
		{name: "mode", mutate: func(config *BackendConfig) {
			adapter := valid.Adapters["chat-main"]
			adapter.Mode = "sometimes"
			config.Adapters = map[string]AdapterConfig{"chat-main": adapter}
		}},
		{name: "channel type", mutate: func(config *BackendConfig) {
			adapter := valid.Adapters["chat-main"]
			adapter.ChannelType = ""
			config.Adapters = map[string]AdapterConfig{"chat-main": adapter}
		}},
		{name: "Delta Chat source", mutate: func(config *BackendConfig) {
			adapter := valid.Adapters["chat-main"]
			adapter.ChannelType = deltaChatChannelType
			config.Adapters = map[string]AdapterConfig{"chat-main": adapter}
		}},
		{name: "email channel type", mutate: func(config *BackendConfig) {
			adapter := valid.Adapters["chat-main"]
			adapter.Source = SourceEmail
			config.Adapters = map[string]AdapterConfig{"chat-main": adapter}
		}},
		{name: "unverified policy", mutate: func(config *BackendConfig) {
			adapter := valid.Adapters["chat-main"]
			adapter.AllowUnverifiedEmail = true
			config.Adapters = map[string]AdapterConfig{"chat-main": adapter}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			config := valid
			test.mutate(&config)
			backend, err := NewBackend(config)
			require.Error(t, err)
			assert.Nil(t, backend)
		})
	}

	backend, err := NewBackend(valid)
	require.NoError(t, err)
	assert.Equal(t, 1, backend.ConnectorCount())
	assert.Equal(t, []string{"chat-main"}, backend.ConnectorNames())
	assert.True(t, backend.HasConnector("chat-main"))
	assert.Equal(t, 0, (*Backend)(nil).ConnectorCount())

	adapter := valid.Adapters["chat-main"]
	adapter.Mode = ModeEventOnly
	valid.Adapters["chat-main"] = adapter
	forward, err := backend.AdmitInbound(
		context.Background(),
		testMessage("chat-main", "copy-test"),
	)
	require.NoError(t, err)
	assert.True(t, forward, "backend must own an immutable adapter copy")
}

func TestBackendGoldenChatAndEmailMappings(t *testing.T) {
	t.Parallel()
	occurredAt := time.Date(2026, time.July, 28, 12, 34, 56, 789, time.FixedZone("test", -4*60*60))
	tests := []struct {
		name       string
		connector  string
		adapter    AdapterConfig
		message    bus.InboundMessage
		dedupeKey  string
		payload    string
		wantActor  *eventing.Actor
		wantTarget *eventing.Subject
	}{
		{
			name:      "chat",
			connector: "support-bot",
			adapter: AdapterConfig{
				Source:      SourceChat,
				Mode:        ModeMirror,
				ChannelType: "slack",
			},
			message: bus.InboundMessage{
				Context: bus.InboundContext{
					Channel:          "support-bot",
					Account:          "workspace-a",
					ChatID:           "room-7",
					ChatType:         "group",
					TopicID:          "thread-2",
					SpaceID:          "org-3",
					SpaceType:        "workspace",
					SenderID:         "U42",
					MessageID:        "local-99",
					Mentioned:        true,
					ReplyToMessageID: "parent-1",
					ReplyToSenderID:  "U7",
				},
				Sender: bus.SenderInfo{
					Platform:    "slack",
					PlatformID:  "U42",
					CanonicalID: "slack:U42",
					Username:    "alice",
					DisplayName: "Alice Example",
				},
				Content:          "hello",
				ChannelOrigin:    true,
				EventDedupeID:    "event-123",
				OccurredAt:       &occurredAt,
				ConversationName: "Incident Room",
				Attachments: []bus.InboundAttachment{{
					Kind:        "file",
					Filename:    "report.txt",
					ContentType: "text/plain",
					SizeBytes:   1234,
				}},
			},
			dedupeKey: "a2471781e2c2b0fb37f4eb3b0ad46f605f414750fd516c180603ed8c12aa650d",
			payload: `{
				"text":"hello",
				"message_id":"local-99",
				"reply_to_message_id":"parent-1",
				"reply_to_sender_id":"U7",
				"attachments":[{
					"kind":"file",
					"filename":"report.txt",
					"content_type":"text/plain",
					"size_bytes":1234
				}]
			}`,
			wantActor: &eventing.Actor{
				ID:          "slack:U42",
				Type:        "user",
				DisplayName: "Alice Example",
				Attributes: map[string]string{
					"platform": "slack",
					"username": "alice",
				},
			},
			wantTarget: &eventing.Subject{
				ID:   "room-7",
				Type: "conversation",
				Name: "Incident Room",
				Attributes: map[string]string{
					"account":    "workspace-a",
					"chat_type":  "group",
					"mentioned":  "true",
					"topic_id":   "thread-2",
					"space_id":   "org-3",
					"space_type": "workspace",
				},
			},
		},
		{
			name:      "email",
			connector: "mailbox",
			adapter: AdapterConfig{
				Source:      SourceEmail,
				Mode:        ModeEventOnly,
				ChannelType: "deltachat",
			},
			message: bus.InboundMessage{
				Context: bus.InboundContext{
					Channel:   "mailbox",
					Account:   "mailbox-a",
					ChatID:    "thread-55",
					ChatType:  "group",
					SenderID:  "alice@example.test",
					MessageID: "77",
				},
				Sender: bus.SenderInfo{
					Platform:    "email",
					PlatformID:  "alice@example.test",
					CanonicalID: "email:alice@example.test",
					DisplayName: "Alice",
				},
				Content:                     "status update",
				ChannelOrigin:               true,
				EventDedupeID:               "<mail-1@example.test>",
				OccurredAt:                  &occurredAt,
				EventSubject:                "Release status",
				ConversationName:            "Release thread",
				EventSenderVerified:         true,
				EventTransportAuthenticated: true,
			},
			dedupeKey: "9f284a3c8c8683ffe58489330d98aadb2b8cdc7917c366564816f93dcc77e574",
			payload:   `{"text":"status update","subject":"Release status","message_id":"77"}`,
			wantActor: &eventing.Actor{
				ID:          "email:alice@example.test",
				Type:        "email_address",
				DisplayName: "Alice",
				Attributes: map[string]string{
					"platform":                "email",
					"sender_verified":         "true",
					"transport_authenticated": "true",
				},
			},
			wantTarget: &eventing.Subject{
				ID:   "thread-55",
				Type: "conversation",
				Name: "Release thread",
				Attributes: map[string]string{
					"account":   "mailbox-a",
					"chat_type": "group",
				},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newRecordingInserter()
			backend := testBackend(t, store, map[string]AdapterConfig{
				test.connector: test.adapter,
			}, 4096)

			forward, err := backend.AdmitInbound(context.Background(), test.message)
			require.NoError(t, err)
			assert.Equal(t, test.adapter.Mode == ModeMirror, forward)
			inputs := store.recordedInserts()
			require.Len(t, inputs, 1)
			got := inputs[0]
			assert.Equal(t, string(test.adapter.Source), got.Source)
			assert.Equal(t, test.connector, got.Connector)
			assert.Equal(t, EventTypeMessageReceived, got.Type)
			assert.Equal(t, test.dedupeKey, got.DedupeKey)
			assert.Equal(t, test.wantActor, got.Actor)
			assert.Equal(t, test.wantTarget, got.Subject)
			require.NotNil(t, got.OccurredAt)
			assert.Equal(t, occurredAt, *got.OccurredAt)
			wantAttributes := map[string]string{"channel_type": test.adapter.ChannelType}
			if test.adapter.Source == SourceEmail {
				wantAttributes["email_trust"] = "verified"
			}
			assert.Equal(t, wantAttributes, got.Attributes)
			assert.JSONEq(t, test.payload, string(got.Payload))
			assert.NotContains(
				t,
				string(got.Payload),
				test.message.EventDedupeID,
				"stable dedupe identity must never be persisted",
			)
		})
	}
}

func TestBackendDedupeIsUnambiguousAndStoreScoped(t *testing.T) {
	t.Parallel()
	store := newRecordingInserter()
	backend := testBackend(t, store, map[string]AdapterConfig{
		"chat-a":  {Source: SourceChat, Mode: ModeMirror, ChannelType: "slack"},
		"chat-b":  {Source: SourceChat, Mode: ModeMirror, ChannelType: "slack"},
		"mailbox": {Source: SourceEmail, Mode: ModeMirror, ChannelType: "deltachat"},
	}, 1024)

	first := testMessage("chat-a", "same")
	first.Context.Account = "ab"
	first.Context.ChatID = "c"
	second := testMessage("chat-a", "same")
	second.Context.Account = "a"
	second.Context.ChatID = "bc"
	for _, message := range []bus.InboundMessage{first, second} {
		forward, err := backend.AdmitInbound(context.Background(), message)
		require.NoError(t, err)
		assert.True(t, forward)
	}
	inserted := store.recordedInserts()
	require.Len(t, inserted, 2)
	assert.NotEqual(t, inserted[0].DedupeKey, inserted[1].DedupeKey)

	scoped := testMessage("chat-a", "store-scoped")
	for _, connector := range []string{"chat-a", "chat-b", "mailbox"} {
		scoped.Context.Channel = connector
		scoped.Channel = connector
		_, err := backend.AdmitInbound(context.Background(), scoped)
		require.NoError(t, err)
	}
	inserted = store.recordedInserts()
	require.Len(t, inserted, 5)
	assert.Equal(t, inserted[2].DedupeKey, inserted[3].DedupeKey)
	assert.Equal(t, inserted[3].DedupeKey, inserted[4].DedupeKey)
	assert.Equal(t, []string{"chat", "chat", "email"}, []string{
		inserted[2].Source,
		inserted[3].Source,
		inserted[4].Source,
	})
}

func TestBackendDuplicateKeepsConfiguredMode(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		mode        Mode
		wantForward bool
	}{
		{name: "mirror", mode: ModeMirror, wantForward: true},
		{name: "event only", mode: ModeEventOnly, wantForward: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newRecordingInserter()
			backend := testBackend(t, store, map[string]AdapterConfig{
				"configured": {
					Source:      SourceChat,
					Mode:        test.mode,
					ChannelType: "test",
				},
			}, 1024)
			message := testMessage("configured", "duplicate")
			for attempt := 0; attempt < 2; attempt++ {
				forward, err := backend.AdmitInbound(context.Background(), message)
				require.NoError(t, err)
				assert.Equal(t, test.wantForward, forward)
			}
			assert.Len(t, store.recordedAttempts(), 2)
			assert.Len(t, store.recordedInserts(), 1)
		})
	}
}

func TestBackendIdentityAndInsertFailuresStopForwarding(t *testing.T) {
	t.Parallel()
	insertFailure := errors.New("durable store unavailable")
	for _, test := range []struct {
		name    string
		message bus.InboundMessage
		store   *recordingInserter
		wantErr error
	}{
		{
			name:    "missing identity",
			message: testMessage("configured", ""),
			store:   newRecordingInserter(),
			wantErr: ErrStableIdentityRequired,
		},
		{
			name:    "insert",
			message: testMessage("configured", "stable"),
			store: &recordingInserter{
				seen:      make(map[string]eventing.StoredEvent),
				insertErr: insertFailure,
			},
			wantErr: insertFailure,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			test.message.Context.MessageID = ""
			backend := testBackend(t, test.store, map[string]AdapterConfig{
				"configured": {
					Source:      SourceChat,
					Mode:        ModeMirror,
					ChannelType: "test",
				},
			}, 1024)
			forward, err := backend.AdmitInbound(context.Background(), test.message)
			assert.False(t, forward)
			require.ErrorIs(t, err, test.wantErr)
		})
	}

	store := newRecordingInserter()
	backend := testBackend(t, store, map[string]AdapterConfig{
		"configured": {Source: SourceChat, Mode: ModeMirror, ChannelType: "test"},
	}, 1024)
	message := testMessage("configured", "")
	message.Context.MessageID = "fallback-id"
	forward, err := backend.AdmitInbound(context.Background(), message)
	require.NoError(t, err)
	assert.True(t, forward)
	require.Len(t, store.recordedInserts(), 1)
	assert.Contains(t, string(store.recordedInserts()[0].Payload), "fallback-id")
}

func TestBackendExcludesUnsafeChannelFields(t *testing.T) {
	t.Parallel()
	const secret = "MUST-NOT-BE-PERSISTED-9f639b"
	store := newRecordingInserter()
	backend := testBackend(t, store, map[string]AdapterConfig{
		"configured": {Source: SourceChat, Mode: ModeMirror, ChannelType: "test"},
	}, 4096)
	message := testMessage("configured", secret)
	message.Context.Raw = map[string]string{"secret": secret}
	message.Context.ReplyHandles = map[string]string{"token": secret}
	message.Media = []string{secret}
	message.MediaScope = secret
	message.SessionKey = secret

	forward, err := backend.AdmitInbound(context.Background(), message)
	require.NoError(t, err)
	assert.True(t, forward)
	inputs := store.recordedInserts()
	require.Len(t, inputs, 1)
	encoded, err := json.Marshal(inputs[0])
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), secret)
	assert.NotContains(t, string(encoded), "reply_handles")
	assert.NotContains(t, string(encoded), "media_scope")
	assert.NotContains(t, string(encoded), "session_key")
}

func TestBackendDeterministicPayloadTruncation(t *testing.T) {
	t.Parallel()
	store := newRecordingInserter()
	const maxPayload = 112
	backend := testBackend(t, store, map[string]AdapterConfig{
		"configured": {Source: SourceChat, Mode: ModeMirror, ChannelType: "test"},
	}, maxPayload)
	message := testMessage("configured", "truncate-id")
	message.Content = strings.Repeat("héllo<>", 100)

	for attempt := 0; attempt < 2; attempt++ {
		_, err := backend.AdmitInbound(context.Background(), message)
		require.NoError(t, err)
	}
	attempts := store.recordedAttempts()
	require.Len(t, attempts, 2)
	assert.Equal(t, attempts[0].Payload, attempts[1].Payload)
	assert.LessOrEqual(t, len(attempts[0].Payload), maxPayload)
	assert.True(t, utf8.Valid(attempts[0].Payload))
	var payload inboundPayload
	require.NoError(t, json.Unmarshal(attempts[0].Payload, &payload))
	assert.True(t, payload.Truncated)
	assert.NotEmpty(t, payload.Text)
	assert.Less(t, len(payload.Text), len(message.Content))

	fallbackStore := newRecordingInserter()
	fallbackBackend := testBackend(t, fallbackStore, map[string]AdapterConfig{
		"configured": {Source: SourceChat, Mode: ModeMirror, ChannelType: "test"},
	}, len(fallbackPayload))
	message.Attachments = []bus.InboundAttachment{{
		Filename: strings.Repeat("x", 100),
	}}
	_, err := fallbackBackend.AdmitInbound(context.Background(), message)
	require.NoError(t, err)
	fallbackInputs := fallbackStore.recordedInserts()
	require.Len(t, fallbackInputs, 1)
	assert.Equal(t, fallbackPayload, string(fallbackInputs[0].Payload))
}

func TestBackendBoundsLargeInputPreprocessing(t *testing.T) {
	t.Parallel()
	const maxPayload = 128
	store := newRecordingInserter()
	backend := testBackend(t, store, map[string]AdapterConfig{
		"configured": {Source: SourceEmail, Mode: ModeEventOnly, ChannelType: "deltachat"},
	}, maxPayload)
	message := testMessage("configured", "large-input")
	message.Content = strings.Repeat("界", 1<<18)
	message.Attachments = make([]bus.InboundAttachment, 1<<14)
	message.Attachments[0] = bus.InboundAttachment{
		Kind:        strings.Repeat("k", 1<<20),
		Filename:    strings.Repeat("f", 1<<20),
		ContentType: strings.Repeat("m", 1<<20),
		SizeBytes:   42,
	}

	forward, err := backend.AdmitInbound(context.Background(), message)
	require.NoError(t, err)
	assert.False(t, forward)
	inputs := store.recordedInserts()
	require.Len(t, inputs, 1)
	assert.LessOrEqual(t, len(inputs[0].Payload), maxPayload)
	assert.True(t, utf8.Valid(inputs[0].Payload))
	var payload map[string]any
	require.NoError(t, json.Unmarshal(inputs[0].Payload, &payload))
	assert.Equal(t, true, payload["truncated"])

	attachments, truncated := safeAttachments(message.Attachments, maxPayload)
	assert.True(t, truncated)
	assert.LessOrEqual(t, len(attachments), maxSafeAttachmentCount)
	if len(attachments) > 0 {
		assert.LessOrEqual(t, len(attachments[0].Kind), maxPayload)
	}
}

func TestBackendPassesInternalAndUnconfiguredMessages(t *testing.T) {
	t.Parallel()
	store := newRecordingInserter()
	backend := testBackend(t, store, map[string]AdapterConfig{
		"configured": {Source: SourceChat, Mode: ModeEventOnly, ChannelType: "test"},
	}, 1024)
	for _, message := range []bus.InboundMessage{
		testMessage("unconfigured", ""),
		func() bus.InboundMessage {
			message := testMessage("configured", "")
			message.ChannelOrigin = false
			return message
		}(),
	} {
		forward, err := backend.AdmitInbound(context.Background(), message)
		require.NoError(t, err)
		assert.True(t, forward)
	}
	assert.Empty(t, store.recordedAttempts())
}

func TestBackendEmailTrustPolicyIsSecureByDefault(t *testing.T) {
	t.Parallel()
	for _, mode := range []Mode{ModeMirror, ModeEventOnly} {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()
			store := newRecordingInserter()
			backend := testBackend(t, store, map[string]AdapterConfig{
				"mailbox": {
					Source:      SourceEmail,
					Mode:        mode,
					ChannelType: "deltachat",
				},
			}, 1024)
			message := testMessage("mailbox", "untrusted-email")
			message.EventSenderVerified = false
			message.EventTransportAuthenticated = true

			forward, err := backend.AdmitInbound(context.Background(), message)
			require.NoError(t, err)
			assert.Equal(t, mode == ModeMirror, forward)
			assert.Empty(t, store.recordedAttempts())
		})
	}

	store := newRecordingInserter()
	backend := testBackend(t, store, map[string]AdapterConfig{
		"mailbox": {
			Source:               SourceEmail,
			Mode:                 ModeEventOnly,
			ChannelType:          "deltachat",
			AllowUnverifiedEmail: true,
		},
	}, 1024)
	message := testMessage("mailbox", "explicit-untrusted-email")
	message.EventSenderVerified = false
	message.EventTransportAuthenticated = false
	forward, err := backend.AdmitInbound(context.Background(), message)
	require.NoError(t, err)
	assert.False(t, forward)
	inserted := store.recordedInserts()
	require.Len(t, inserted, 1)
	assert.Equal(t, "unverified", inserted[0].Attributes["email_trust"])
	require.NotNil(t, inserted[0].Actor)
	assert.Equal(t, "false", inserted[0].Actor.Attributes["sender_verified"])
	assert.Equal(t, "false", inserted[0].Actor.Attributes["transport_authenticated"])
}
