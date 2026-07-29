package deltachat

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/media"
)

type deltaAdmissionFunc func(context.Context, bus.InboundMessage) (bool, error)

func (fn deltaAdmissionFunc) AdmitInbound(
	ctx context.Context,
	message bus.InboundMessage,
) (bool, error) {
	return fn(ctx, message)
}

func TestNewDeltaChatChannel(t *testing.T) {
	msgBus := bus.NewMessageBus()

	// A fake rpc server so resolveServerPath succeeds regardless of host setup.
	fakeServer := filepath.Join(t.TempDir(), "deltachat-rpc-server")
	if err := os.WriteFile(fakeServer, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("missing email", func(t *testing.T) {
		bc := &config.Channel{Type: config.ChannelDeltaChat, Enabled: true}
		cfg := &config.DeltaChatSettings{Password: *config.NewSecureString("pw"), RPCServerPath: fakeServer}
		_, err := NewDeltaChatChannel(bc, cfg, msgBus)
		if err == nil {
			t.Fatal("expected error for missing email")
		}
		if !strings.Contains(err.Error(), "@nine.testrun.org") {
			t.Fatalf("error = %v, want bootstrap server guidance", err)
		}
		if !strings.Contains(err.Error(), "Next step:") || !strings.Contains(err.Error(), "picoclaw g") {
			t.Fatalf("error = %v, want next-step guidance", err)
		}
	})

	t.Run("bootstrap server marker", func(t *testing.T) {
		bc := &config.Channel{Type: config.ChannelDeltaChat, Enabled: true}
		cfg := &config.DeltaChatSettings{Email: "@mehl.cloud", RPCServerPath: fakeServer}
		if _, err := NewDeltaChatChannel(bc, cfg, msgBus); err != nil {
			t.Fatalf("unexpected error for bootstrap marker: %v", err)
		}
	})

	t.Run("password optional for existing account reference", func(t *testing.T) {
		bc := &config.Channel{Type: config.ChannelDeltaChat, Enabled: true}
		cfg := &config.DeltaChatSettings{Email: "bot@example.org", RPCServerPath: fakeServer}
		if _, err := NewDeltaChatChannel(bc, cfg, msgBus); err != nil {
			t.Fatalf("unexpected error without password: %v", err)
		}
	})

	t.Run("missing rpc server", func(t *testing.T) {
		bc := &config.Channel{Type: config.ChannelDeltaChat, Enabled: true}
		cfg := &config.DeltaChatSettings{
			Email:         "bot@example.org",
			Password:      *config.NewSecureString("pw"),
			RPCServerPath: filepath.Join(t.TempDir(), "does-not-exist"),
		}
		if _, err := NewDeltaChatChannel(bc, cfg, msgBus); err == nil {
			t.Error("expected error for missing rpc server path")
		}
	})

	t.Run("valid config", func(t *testing.T) {
		bc := &config.Channel{Type: config.ChannelDeltaChat, Enabled: true}
		cfg := &config.DeltaChatSettings{
			Email:         "bot@example.org",
			Password:      *config.NewSecureString("pw"),
			RPCServerPath: fakeServer,
			DataDir:       t.TempDir(),
		}
		ch, err := NewDeltaChatChannel(bc, cfg, msgBus)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ch.Name() != config.ChannelDeltaChat {
			t.Errorf("Name() = %q, want %q", ch.Name(), config.ChannelDeltaChat)
		}
		if ch.IsRunning() {
			t.Error("new channel should not be running")
		}
	})
}

func TestResolveServerPathUsesPATH(t *testing.T) {
	dir := t.TempDir()
	fakeServer := filepath.Join(dir, "deltachat-rpc-server")
	if err := os.WriteFile(fakeServer, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	got, err := resolveServerPath("")
	if err != nil {
		t.Fatalf("resolveServerPath: %v", err)
	}
	if got != fakeServer {
		t.Fatalf("resolveServerPath() = %q, want %q", got, fakeServer)
	}
}

func TestMentionsBot(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		displayName string
		email       string
		want        bool
	}{
		{"display name", "hey PicoBot can you help", "PicoBot", "bot@example.org", true},
		{"case insensitive name", "hey picobot", "PicoBot", "bot@example.org", true},
		{"short display name exact", "hey bot can you help", "bot", "bot@example.org", true},
		{"short display name with punctuation", "AI, summarize this", "ai", "bot@example.org", true},
		{"multi word display name", "hey PicoClaw Bot, can you help", "PicoClaw Bot", "bot@example.org", true},
		{"email local part", "@bot please summarize", "", "bot@example.org", true},
		{"email local part with punctuation", "please summarize, @bot.", "", "bot@example.org", true},
		{"no mention", "just chatting here", "PicoBot", "bot@example.org", false},
		{"local part without @", "the robot is cool", "", "bot@example.org", false},
		{"short display name inside word", "the robot is cool", "bot", "bot@example.org", false},
		{"short display name inside mail", "please email me later", "ai", "bot@example.org", false},
		{"display name with prefix word", "hey SuperPicoClaw Bot", "PicoClaw Bot", "bot@example.org", false},
		{"email local part inside handle", "hello @botanic", "", "bot@example.org", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mentionsBot(tt.content, tt.displayName, tt.email); got != tt.want {
				t.Errorf("mentionsBot(%q, %q, %q) = %v, want %v", tt.content, tt.displayName, tt.email, got, tt.want)
			}
		})
	}
}

func TestExpandHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"/abs/path", "/abs/path"},
		{"~", home},
		{"~/sub", filepath.Join(home, "sub")},
		{"relative", "relative"},
	}
	for _, tt := range tests {
		if got := expandHome(tt.in); got != tt.want {
			t.Errorf("expandHome(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestResolveDataDir(t *testing.T) {
	if got := resolveDataDir("/explicit/dir", "x"); got != "/explicit/dir" {
		t.Errorf("explicit data dir = %q, want /explicit/dir", got)
	}
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".picoclaw", "deltachat", "mychan")
	if got := resolveDataDir("", "mychan"); got != want {
		t.Errorf("default data dir = %q, want %q", got, want)
	}
}

func TestHandleMessageMarksSeenOnlyAfterDispatch(t *testing.T) {
	tests := []struct {
		name        string
		chatType    string
		mentionOnly bool
		closeBus    bool
		wantSeen    bool
	}{
		{name: "successful dispatch", chatType: chatTypeSingle, wantSeen: true},
		{
			name:        "ignored group trigger advances provider cursor",
			chatType:    "Group",
			mentionOnly: true,
			wantSeen:    true,
		},
		{name: "failed local publish", chatType: chatTypeSingle, closeBus: true},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			messageID := int64(42 + i)
			chat := dcChat{ID: 99, Name: "chat", ChatType: tt.chatType}
			msgBus := bus.NewMessageBus()
			if tt.closeBus {
				msgBus.Close()
			} else {
				defer msgBus.Close()
			}

			ch := newTestChannelWithBus(t, msgBus, func(bc *config.Channel) {
				bc.GroupTrigger.MentionOnly = tt.mentionOnly
			})
			ch.ctx = context.Background()
			ch.accountID = 7
			ch.selfAddr = "bot@example.org"

			markSeen := make(chan struct{}, 1)
			rpc, cleanup := newMockRPC(t, func(req rpcRequest) string {
				switch req.Method {
				case "get_message":
					return rpcResult(req, dcMessage{
						ID:     messageID,
						ChatID: chat.ID,
						Text:   "hello",
						Sender: &dcContact{Address: "alice@example.org", DisplayName: "Alice"},
					})
				case "get_full_chat_by_id":
					return rpcResult(req, chat)
				case "markseen_msgs":
					markSeen <- struct{}{}
					return rpcResult(req, nil)
				default:
					return rpcUnexpectedMethod(req)
				}
			})
			defer cleanup()
			ch.rpc = rpc

			ch.handleMessage(messageID)

			gotSeen := false
			select {
			case <-markSeen:
				gotSeen = true
			default:
			}
			if gotSeen != tt.wantSeen {
				t.Fatalf("markseen called = %v, want %v", gotSeen, tt.wantSeen)
			}
		})
	}
}

func TestHandleMessageExposesSafeStableEventMetadataBeforeAcknowledgement(t *testing.T) {
	msgBus := bus.NewMessageBus()
	defer msgBus.Close()

	ch := newTestChannelWithBus(t, msgBus, nil)
	ch.SetName("support-mail")
	ch.ctx = context.Background()
	ch.accountID = 7
	ch.selfAddr = "bot@example.org"

	privatePath := filepath.Join(t.TempDir(), "private", "blob-42")
	occurredAt := time.Date(2026, time.July, 28, 12, 34, 56, 0, time.UTC)
	senderControlledDate := occurredAt.Add(-30 * 24 * time.Hour)
	var admitted bus.InboundMessage
	order := make([]string, 0, 2)
	msgBus.SetInboundAdmission(deltaAdmissionFunc(
		func(_ context.Context, message bus.InboundMessage) (bool, error) {
			order = append(order, "admit")
			admitted = message
			return false, nil
		},
	))

	rpc, cleanup := newMockRPC(t, func(req rpcRequest) string {
		switch req.Method {
		case "get_message":
			return rpcResult(req, dcMessage{
				ID:                    42,
				ChatID:                99,
				Text:                  "hello from email",
				Subject:               "Release status",
				File:                  privatePath,
				FileName:              `folder\report.pdf`,
				FileMime:              "application/pdf",
				FileBytes:             1234,
				ViewType:              "File",
				DownloadState:         deltaChatDownloadDone,
				Timestamp:             senderControlledDate.Unix(),
				ReceivedTimestamp:     occurredAt.Unix(),
				HasDeviatingTimestamp: true,
				ShowPadlock:           true,
				Sender: &dcContact{
					Address:     "alice@example.org",
					DisplayName: "Alice",
					IsVerified:  true,
				},
			})
		case "get_full_chat_by_id":
			return rpcResult(req, dcChat{
				ID:       99,
				Name:     "Support Inbox",
				ChatType: chatTypeSingle,
			})
		case "markseen_msgs":
			order = append(order, "ack")
			return rpcResult(req, nil)
		default:
			return rpcUnexpectedMethod(req)
		}
	})
	defer cleanup()
	ch.rpc = rpc

	if err := ch.handleMessage(42); err != nil {
		t.Fatalf("handleMessage() error = %v", err)
	}
	if len(order) != 2 || order[0] != "admit" || order[1] != "ack" {
		t.Fatalf("operation order = %#v, want [admit ack]", order)
	}
	if admitted.Context.Channel != "support-mail" {
		t.Fatalf("admitted connector = %q, want support-mail", admitted.Context.Channel)
	}
	if admitted.EventDedupeID != "local:42" {
		t.Fatalf("stable event identity = %q", admitted.EventDedupeID)
	}
	if admitted.OccurredAt == nil || !admitted.OccurredAt.Equal(occurredAt) {
		t.Fatalf("occurred_at = %v, want %v", admitted.OccurredAt, occurredAt)
	}
	if admitted.ConversationName != "Support Inbox" {
		t.Fatalf("conversation name = %q", admitted.ConversationName)
	}
	if admitted.EventSubject != "Release status" {
		t.Fatalf("event subject = %q", admitted.EventSubject)
	}
	if !admitted.EventSenderVerified || !admitted.EventTransportAuthenticated {
		t.Fatalf(
			"email trust metadata = verified:%v authenticated:%v, want both true",
			admitted.EventSenderVerified,
			admitted.EventTransportAuthenticated,
		)
	}
	if len(admitted.Attachments) != 1 {
		t.Fatalf("attachments = %#v, want one", admitted.Attachments)
	}
	attachment := admitted.Attachments[0]
	if attachment.Filename != "report.pdf" ||
		attachment.ContentType != "application/pdf" ||
		attachment.Kind != "File" ||
		attachment.SizeBytes != 1234 {
		t.Fatalf("safe attachment metadata = %#v", attachment)
	}
	encoded, err := json.Marshal(admitted)
	if err != nil {
		t.Fatalf("Marshal(admitted) error = %v", err)
	}
	if strings.Contains(string(encoded), privatePath) ||
		strings.Contains(string(encoded), "sender_verified") ||
		strings.Contains(string(encoded), "transport_authenticated") {
		t.Fatalf("serialized message exposed process-local event metadata: %s", encoded)
	}
	if got := len(msgBus.InboundChan()); got != 0 {
		t.Fatalf("event-only admission queued %d chat messages, want 0", got)
	}
}

func TestHandleMessageAdmissionFailureRemainsUnacknowledged(t *testing.T) {
	msgBus := bus.NewMessageBus()
	defer msgBus.Close()
	wantErr := errors.New("event store unavailable")
	msgBus.SetInboundAdmission(deltaAdmissionFunc(
		func(context.Context, bus.InboundMessage) (bool, error) {
			return false, wantErr
		},
	))

	ch := newTestChannelWithBus(t, msgBus, nil)
	ch.ctx = context.Background()
	ch.accountID = 7
	ch.selfAddr = "bot@example.org"
	markSeenCalls := 0
	rpc, cleanup := newMockRPC(t, func(req rpcRequest) string {
		switch req.Method {
		case "get_message":
			return rpcResult(req, dcMessage{
				ID:            42,
				ChatID:        99,
				Text:          "retry me",
				DownloadState: deltaChatDownloadDone,
				Sender:        &dcContact{Address: "alice@example.org"},
			})
		case "get_full_chat_by_id":
			return rpcResult(req, dcChat{
				ID:       99,
				Name:     "Inbox",
				ChatType: chatTypeSingle,
			})
		case "markseen_msgs":
			markSeenCalls++
			return rpcResult(req, nil)
		default:
			return rpcUnexpectedMethod(req)
		}
	})
	defer cleanup()
	ch.rpc = rpc

	err := ch.handleMessage(42)
	if !errors.Is(err, wantErr) {
		t.Fatalf("handleMessage() error = %v, want %v", err, wantErr)
	}
	if markSeenCalls != 0 {
		t.Fatalf("markseen calls = %d, want 0", markSeenCalls)
	}
	if got := len(msgBus.InboundChan()); got != 0 {
		t.Fatalf("failed admission queued %d messages, want 0", got)
	}
}

func TestHandleMessageRetryIdentityIgnoresChangingMessageInfoAvailability(t *testing.T) {
	msgBus := bus.NewMessageBus()
	defer msgBus.Close()

	wantErr := errors.New("event store unavailable")
	var admittedIDs []string
	admissionCalls := 0
	msgBus.SetInboundAdmission(deltaAdmissionFunc(
		func(_ context.Context, message bus.InboundMessage) (bool, error) {
			admissionCalls++
			admittedIDs = append(admittedIDs, message.EventDedupeID)
			if admissionCalls == 1 {
				return false, wantErr
			}
			return false, nil
		},
	))

	ch := newTestChannelWithBus(t, msgBus, nil)
	ch.ctx = context.Background()
	ch.accountID = 7
	ch.selfAddr = "bot@example.org"

	messageInfoAvailable := false
	messageInfoCalls := 0
	markSeenCalls := 0
	rpc, cleanup := newMockRPC(t, func(req rpcRequest) string {
		switch req.Method {
		case "get_message":
			return rpcResult(req, dcMessage{
				ID:            42,
				ChatID:        99,
				Text:          "retry me with one durable identity",
				DownloadState: deltaChatDownloadDone,
				Sender:        &dcContact{Address: "alice@example.org"},
			})
		case "get_full_chat_by_id":
			return rpcResult(req, dcChat{
				ID:       99,
				Name:     "Inbox",
				ChatType: chatTypeSingle,
			})
		case "get_message_info_object":
			messageInfoCalls++
			if !messageInfoAvailable {
				return rpcUnexpectedMethod(req)
			}
			return rpcResult(req, map[string]string{
				"rfc724Mid": "<later-available@example.org>",
			})
		case "markseen_msgs":
			markSeenCalls++
			return rpcResult(req, nil)
		default:
			return rpcUnexpectedMethod(req)
		}
	})
	defer cleanup()
	ch.rpc = rpc

	if err := ch.handleMessage(42); !errors.Is(err, wantErr) {
		t.Fatalf("first handleMessage() error = %v, want %v", err, wantErr)
	}
	messageInfoAvailable = true
	if err := ch.handleMessage(42); err != nil {
		t.Fatalf("second handleMessage() error = %v", err)
	}

	if len(admittedIDs) != 2 ||
		admittedIDs[0] != "local:42" ||
		admittedIDs[1] != "local:42" {
		t.Fatalf("retry event identities = %#v, want [local:42 local:42]", admittedIDs)
	}
	if messageInfoCalls != 0 {
		t.Fatalf(
			"get_message_info_object calls = %d, want 0; optional RFC metadata must not influence dedupe identity",
			messageInfoCalls,
		)
	}
	if markSeenCalls != 1 {
		t.Fatalf("markseen calls = %d, want 1 after successful retry", markSeenCalls)
	}
}

func TestDrainStartupBacklogProcessesOldestMessageFirst(t *testing.T) {
	msgBus := bus.NewMessageBus()
	defer msgBus.Close()
	ch := newTestChannelWithBus(t, msgBus, nil)
	ch.ctx = context.Background()
	ch.accountID = 7
	ch.selfAddr = "bot@example.org"
	ch.SetRunning(true)

	backlogCalls := 0
	var processed []int64
	rpc, cleanup := newMockRPC(t, func(req rpcRequest) string {
		switch req.Method {
		case "get_next_msgs":
			backlogCalls++
			if backlogCalls == 1 {
				return rpcResult(req, []int64{9, 4})
			}
			return rpcResult(req, []int64{})
		case "get_message":
			messageID := int64(req.Params[1].(float64))
			processed = append(processed, messageID)
			return rpcResult(req, dcMessage{
				ID:            messageID,
				ChatID:        99,
				Text:          "backlog",
				DownloadState: deltaChatDownloadDone,
				Sender:        &dcContact{Address: "alice@example.org"},
			})
		case "get_full_chat_by_id":
			return rpcResult(req, dcChat{
				ID:       99,
				Name:     "Inbox",
				ChatType: chatTypeSingle,
			})
		case "markseen_msgs":
			return rpcResult(req, nil)
		default:
			return rpcUnexpectedMethod(req)
		}
	})
	defer cleanup()
	ch.rpc = rpc

	if state := ch.drainStartupBacklog(
		newDeltaChatListenerState(),
	); state != deltaChatProcessComplete {
		t.Fatalf("drainStartupBacklog() = %v, want complete", state)
	}
	if len(processed) != 2 || processed[0] != 4 || processed[1] != 9 {
		t.Fatalf("processed message IDs = %#v, want [4 9]", processed)
	}
}

func TestEventChannelOverflowWithContextZeroDrainsOrderedQueue(t *testing.T) {
	msgBus := bus.NewMessageBus()
	defer msgBus.Close()

	ch := newTestChannelWithBus(t, msgBus, nil)
	ch.ctx = context.Background()
	ch.accountID = 7
	ch.selfAddr = "bot@example.org"
	ch.SetRunning(true)

	backlogCalls := 0
	getMessageCalls := 0
	markSeenCalls := 0
	rpc, cleanup := newMockRPC(t, func(req rpcRequest) string {
		switch req.Method {
		case "get_next_msgs":
			backlogCalls++
			if backlogCalls == 1 {
				return rpcResult(req, []int64{42})
			}
			return rpcResult(req, []int64{})
		case "get_message":
			getMessageCalls++
			return rpcResult(req, dcMessage{
				ID:            42,
				ChatID:        99,
				Text:          "recovered after overflow",
				DownloadState: deltaChatDownloadDone,
				Sender:        &dcContact{Address: "alice@example.org"},
			})
		case "get_full_chat_by_id":
			return rpcResult(req, dcChat{
				ID:       99,
				Name:     "Inbox",
				ChatType: chatTypeSingle,
			})
		case "markseen_msgs":
			markSeenCalls++
			return rpcResult(req, nil)
		default:
			return rpcUnexpectedMethod(req)
		}
	})
	defer cleanup()
	ch.rpc = rpc

	state := ch.processListenerEvent(dcEvent{
		ContextID: 0,
		Event: dcEventType{
			Kind: "EventChannelOverflow",
		},
	}, newDeltaChatListenerState())
	if state != deltaChatProcessComplete {
		t.Fatalf("overflow event state = %v, want complete", state)
	}
	if backlogCalls != 2 || getMessageCalls != 1 || markSeenCalls != 1 {
		t.Fatalf(
			"queue/fetch/ack calls = %d/%d/%d, want 2/1/1",
			backlogCalls,
			getMessageCalls,
			markSeenCalls,
		)
	}
}

func TestInitialPreMessageMsgsChangedStartsOrderedDownload(t *testing.T) {
	msgBus := bus.NewMessageBus()
	defer msgBus.Close()

	ch := newTestChannelWithBus(t, msgBus, nil)
	ch.ctx = context.Background()
	ch.accountID = 7
	ch.selfAddr = "bot@example.org"
	ch.SetRunning(true)

	listenerState := newDeltaChatListenerState()
	downloadComplete := false
	acknowledged := false
	backlogCalls := 0
	getMessageCalls := 0
	messageInfoCalls := 0
	downloadCalls := 0
	markSeenCalls := 0
	rpc, cleanup := newMockRPC(t, func(req rpcRequest) string {
		switch req.Method {
		case "get_next_msgs":
			backlogCalls++
			if acknowledged {
				return rpcResult(req, []int64{})
			}
			return rpcResult(req, []int64{41})
		case "get_message":
			getMessageCalls++
			downloadState := "Available"
			if downloadComplete {
				downloadState = deltaChatDownloadDone
			}
			return rpcResult(req, dcMessage{
				ID:            41,
				ChatID:        99,
				Text:          "large message",
				DownloadState: downloadState,
				Sender:        &dcContact{Address: "alice@example.org"},
			})
		case "get_message_info_object":
			messageInfoCalls++
			return rpcResult(req, dcMessageInfo{
				RFC724MID: "<large-message@example.org>",
			})
		case "download_full_message":
			downloadCalls++
			return rpcResult(req, nil)
		case "get_full_chat_by_id":
			return rpcResult(req, dcChat{
				ID:       99,
				Name:     "Inbox",
				ChatType: chatTypeSingle,
			})
		case "markseen_msgs":
			markSeenCalls++
			acknowledged = true
			return rpcResult(req, nil)
		default:
			return rpcUnexpectedMethod(req)
		}
	})
	defer cleanup()
	ch.rpc = rpc

	state := ch.processListenerEvent(dcEvent{
		ContextID: 7,
		Event: dcEventType{
			Kind:  "MsgsChanged",
			MsgID: 41,
		},
	}, listenerState)
	if state != deltaChatProcessDownloadPending {
		t.Fatalf("initial Pre-Message event state = %v, want download pending", state)
	}
	if backlogCalls != 1 || getMessageCalls != 1 || messageInfoCalls != 1 ||
		downloadCalls != 1 || markSeenCalls != 0 {
		t.Fatalf(
			"queue/fetch/info/download/ack calls = %d/%d/%d/%d/%d, want 1/1/1/1/0",
			backlogCalls,
			getMessageCalls,
			messageInfoCalls,
			downloadCalls,
			markSeenCalls,
		)
	}
	if _, pending := listenerState.pendingDownloads[41]; !pending {
		t.Fatal("initial Pre-Message MsgsChanged did not retain pending download state")
	}

	downloadComplete = true
	state = ch.processListenerEvent(dcEvent{
		ContextID: 7,
		Event: dcEventType{
			Kind:  "IncomingMsg",
			MsgID: 41,
		},
	}, listenerState)
	if state != deltaChatProcessComplete {
		t.Fatalf("completed message event state = %v, want complete", state)
	}
	if backlogCalls != 3 || getMessageCalls != 2 || messageInfoCalls != 1 ||
		downloadCalls != 1 || markSeenCalls != 1 {
		t.Fatalf(
			"final queue/fetch/info/download/ack calls = %d/%d/%d/%d/%d, want 3/2/1/1/1",
			backlogCalls,
			getMessageCalls,
			messageInfoCalls,
			downloadCalls,
			markSeenCalls,
		)
	}
	if _, pending := listenerState.pendingDownloads[41]; pending {
		t.Fatal("completed message retained stale pending download state")
	}
	if got := len(msgBus.InboundChan()); got != 1 {
		t.Fatalf("forwarded messages = %d, want 1 after full download", got)
	}
}

func TestUnrelatedBacklogDoesNotRetirePendingDownload(t *testing.T) {
	msgBus := bus.NewMessageBus()
	defer msgBus.Close()

	ch := newTestChannelWithBus(t, msgBus, nil)
	ch.ctx = context.Background()
	ch.accountID = 7
	ch.SetRunning(true)

	listenerState := newDeltaChatListenerState()
	listenerState.pendingDownloads[41] = &deltaChatPendingDownload{
		messageID:         41,
		chatID:            99,
		rfc724MID:         "<original@example.org>",
		downloadRequested: true,
	}

	getMessageCalls := 0
	markSeenCalls := 0
	rpc, cleanup := newMockRPC(t, func(req rpcRequest) string {
		switch req.Method {
		case "get_next_msgs":
			return rpcResult(req, []int64{42})
		case "get_message_info_object":
			return rpcResult(req, dcMessageInfo{
				RFC724MID: "<unrelated@example.org>",
			})
		case "get_message":
			getMessageCalls++
			return rpcUnexpectedMethod(req)
		case "markseen_msgs":
			markSeenCalls++
			return rpcUnexpectedMethod(req)
		default:
			return rpcUnexpectedMethod(req)
		}
	})
	defer cleanup()
	ch.rpc = rpc

	if state := ch.drainStartupBacklog(listenerState); state != deltaChatProcessDownloadPending {
		t.Fatalf("unrelated batch state = %v, want download pending", state)
	}
	if getMessageCalls != 0 || markSeenCalls != 0 {
		t.Fatalf(
			"unrelated batch fetched/acknowledged %d/%d messages, want 0/0",
			getMessageCalls,
			markSeenCalls,
		)
	}
	if _, pending := listenerState.pendingDownloads[41]; !pending {
		t.Fatal("unrelated complete batch retired the pending original")
	}
}

func TestInitialInProgressSignalDoesNotRetirePendingDownload(t *testing.T) {
	msgBus := bus.NewMessageBus()
	defer msgBus.Close()

	ch := newTestChannelWithBus(t, msgBus, nil)
	ch.ctx = context.Background()
	ch.accountID = 7
	ch.SetRunning(true)

	listenerState := newDeltaChatListenerState()
	listenerState.pendingDownloads[41] = &deltaChatPendingDownload{
		messageID:         41,
		chatID:            99,
		rfc724MID:         "<original@example.org>",
		downloadRequested: true,
	}

	getMessageCalls := 0
	messageInfoCalls := 0
	downloadCalls := 0
	markSeenCalls := 0
	rpc, cleanup := newMockRPC(t, func(req rpcRequest) string {
		switch req.Method {
		case "get_next_msgs":
			return rpcResult(req, []int64{41})
		case "get_message":
			getMessageCalls++
			return rpcResult(req, dcMessage{
				ID:            41,
				ChatID:        99,
				DownloadState: "InProgress",
			})
		case "get_message_info_object":
			messageInfoCalls++
			return rpcUnexpectedMethod(req)
		case "download_full_message":
			downloadCalls++
			return rpcUnexpectedMethod(req)
		case "markseen_msgs":
			markSeenCalls++
			return rpcUnexpectedMethod(req)
		default:
			return rpcUnexpectedMethod(req)
		}
	})
	defer cleanup()
	ch.rpc = rpc

	state := ch.processListenerEvent(dcEvent{
		ContextID: 7,
		Event: dcEventType{
			Kind:  "MsgsChanged",
			MsgID: 41,
		},
	}, listenerState)
	if state != deltaChatProcessDownloadPending {
		t.Fatalf("initial InProgress signal state = %v, want download pending", state)
	}
	if getMessageCalls != 1 || messageInfoCalls != 0 || downloadCalls != 0 || markSeenCalls != 0 {
		t.Fatalf(
			"fetch/info/download/ack calls = %d/%d/%d/%d, want 1/0/0/0",
			getMessageCalls,
			messageInfoCalls,
			downloadCalls,
			markSeenCalls,
		)
	}
	if _, pending := listenerState.pendingDownloads[41]; !pending {
		t.Fatal("initial InProgress notification retired the pending original")
	}
}

func TestListenTreatsAcknowledgedStartupEventAsQueueNotification(t *testing.T) {
	msgBus := bus.NewMessageBus()
	defer msgBus.Close()

	ch := newTestChannelWithBus(t, msgBus, nil)
	ch.ctx = context.Background()
	ch.accountID = 7
	ch.selfAddr = "bot@example.org"
	ch.SetRunning(true)

	backlogCalls := 0
	getMessageCalls := 0
	markSeenCalls := 0
	eventCalls := 0
	rpc, cleanup := newMockRPC(t, func(req rpcRequest) string {
		switch req.Method {
		case "get_next_msgs":
			backlogCalls++
			if backlogCalls == 1 {
				return rpcResult(req, []int64{42})
			}
			if backlogCalls == 3 {
				ch.SetRunning(false)
			}
			return rpcResult(req, []int64{})
		case "get_next_event":
			eventCalls++
			// The provider queued this event while the startup queue drain
			// handled and acknowledged the same message from get_next_msgs.
			return rpcResult(req, dcEvent{
				ContextID: 7,
				Event: dcEventType{
					Kind:  "IncomingMsg",
					MsgID: 42,
				},
			})
		case "get_message":
			getMessageCalls++
			return rpcResult(req, dcMessage{
				ID:            42,
				ChatID:        99,
				Text:          "startup overlap",
				DownloadState: deltaChatDownloadDone,
				Sender:        &dcContact{Address: "alice@example.org"},
			})
		case "get_full_chat_by_id":
			return rpcResult(req, dcChat{
				ID:       99,
				Name:     "Inbox",
				ChatType: chatTypeSingle,
			})
		case "markseen_msgs":
			markSeenCalls++
			return rpcResult(req, nil)
		default:
			return rpcUnexpectedMethod(req)
		}
	})
	defer cleanup()
	ch.rpc = rpc

	done := make(chan struct{})
	go func() {
		defer close(done)
		ch.listen()
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		ch.SetRunning(false)
		t.Fatal("listen did not drain the stale startup notification")
	}

	if eventCalls != 1 {
		t.Fatalf("get_next_event calls = %d, want 1", eventCalls)
	}
	if backlogCalls != 3 {
		t.Fatalf("get_next_msgs calls = %d, want startup batch, empty drain, and event drain", backlogCalls)
	}
	if getMessageCalls != 1 {
		t.Fatalf("get_message calls = %d, want one startup fetch", getMessageCalls)
	}
	if markSeenCalls != 1 {
		t.Fatalf("markseen calls = %d, want one startup acknowledgement", markSeenCalls)
	}
	if got := len(msgBus.InboundChan()); got != 1 {
		t.Fatalf("forwarded messages = %d, want one startup turn", got)
	}
}

func TestListenMoreThan256StaleEventsDoNotReplayMessages(t *testing.T) {
	msgBus := bus.NewMessageBus()
	defer msgBus.Close()

	const messageCount = 300
	ch := newTestChannelWithBus(t, msgBus, nil)
	ch.ctx = context.Background()
	ch.accountID = 7
	ch.selfAddr = "bot@example.org"
	ch.SetRunning(true)

	admissionCalls := 0
	msgBus.SetInboundAdmission(deltaAdmissionFunc(
		func(context.Context, bus.InboundMessage) (bool, error) {
			admissionCalls++
			return false, nil
		},
	))

	messageIDs := make([]int64, messageCount)
	for index := range messageIDs {
		messageIDs[index] = int64(index + 1)
	}
	backlogCalls := 0
	eventCalls := 0
	getMessageCalls := 0
	markSeenCalls := 0
	rpc, cleanup := newMockRPC(t, func(req rpcRequest) string {
		switch req.Method {
		case "get_next_msgs":
			backlogCalls++
			if backlogCalls == 1 {
				return rpcResult(req, messageIDs)
			}
			if backlogCalls == messageCount+2 {
				ch.SetRunning(false)
			}
			return rpcResult(req, []int64{})
		case "get_next_event":
			eventCalls++
			return rpcResult(req, dcEvent{
				ContextID: 7,
				Event: dcEventType{
					Kind:  "IncomingMsg",
					MsgID: int64((eventCalls-1)%messageCount + 1),
				},
			})
		case "get_message":
			getMessageCalls++
			messageID := int64(req.Params[1].(float64))
			return rpcResult(req, dcMessage{
				ID:            messageID,
				ChatID:        99,
				Text:          "startup replay",
				DownloadState: deltaChatDownloadDone,
				Sender:        &dcContact{Address: "alice@example.org"},
			})
		case "get_full_chat_by_id":
			return rpcResult(req, dcChat{
				ID:       99,
				Name:     "Inbox",
				ChatType: chatTypeSingle,
			})
		case "markseen_msgs":
			markSeenCalls++
			return rpcResult(req, nil)
		default:
			return rpcUnexpectedMethod(req)
		}
	})
	defer cleanup()
	ch.rpc = rpc

	done := make(chan struct{})
	go func() {
		defer close(done)
		ch.listen()
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		ch.SetRunning(false)
		t.Fatal("listen did not drain stale events")
	}

	if eventCalls != messageCount {
		t.Fatalf("get_next_event calls = %d, want %d", eventCalls, messageCount)
	}
	if backlogCalls != messageCount+2 {
		t.Fatalf("get_next_msgs calls = %d, want %d", backlogCalls, messageCount+2)
	}
	if getMessageCalls != messageCount || admissionCalls != messageCount || markSeenCalls != messageCount {
		t.Fatalf(
			"fetch/admit/ack calls = %d/%d/%d, want %d each without replay",
			getMessageCalls,
			admissionCalls,
			markSeenCalls,
			messageCount,
		)
	}
}

func TestFailedAcknowledgementStopsOrderedProcessing(t *testing.T) {
	msgBus := bus.NewMessageBus()
	defer msgBus.Close()

	ch := newTestChannelWithBus(t, msgBus, nil)
	ctx, cancel := context.WithCancel(context.Background())
	ch.ctx = ctx
	ch.accountID = 7
	ch.selfAddr = "bot@example.org"
	ch.SetRunning(true)

	markSeenCalls := 0
	rpc, cleanup := newMockRPC(t, func(req rpcRequest) string {
		switch req.Method {
		case "get_message":
			return rpcResult(req, dcMessage{
				ID:            42,
				ChatID:        99,
				Text:          "must retry",
				DownloadState: deltaChatDownloadDone,
				Sender:        &dcContact{Address: "alice@example.org"},
			})
		case "get_full_chat_by_id":
			return rpcResult(req, dcChat{
				ID:       99,
				Name:     "Inbox",
				ChatType: chatTypeSingle,
			})
		case "markseen_msgs":
			markSeenCalls++
			cancel()
			return rpcUnexpectedMethod(req)
		default:
			return rpcUnexpectedMethod(req)
		}
	})
	defer cleanup()
	ch.rpc = rpc

	listenerState := newDeltaChatListenerState()
	if state := ch.processMessageWithRetry(42, listenerState); state != deltaChatProcessStopped {
		t.Fatalf("process state = %v, want stopped after acknowledgement cancellation", state)
	}
	if markSeenCalls != 1 {
		t.Fatalf("markseen calls = %d, want 1", markSeenCalls)
	}
	if len(listenerState.pendingDownloads) != 0 {
		t.Fatal("ordinary acknowledgement failure was misclassified as a pending download")
	}
}

func TestListenReconcilesReplacementBeforeLaterMessage(t *testing.T) {
	msgBus := bus.NewMessageBus()
	defer msgBus.Close()

	ch := newTestChannelWithBus(t, msgBus, nil)
	ch.ctx = context.Background()
	ch.accountID = 7
	ch.selfAddr = "bot@example.org"
	ch.SetRunning(true)

	transientStoreErr := errors.New("transient event store failure")
	var admissionOrder []string
	replacementAttempts := 0
	msgBus.SetInboundAdmission(deltaAdmissionFunc(
		func(_ context.Context, message bus.InboundMessage) (bool, error) {
			admissionOrder = append(admissionOrder, message.MessageID)
			if message.MessageID == "42" {
				replacementAttempts++
				if replacementAttempts == 1 {
					return false, transientStoreErr
				}
			}
			return true, nil
		},
	))

	eventIndex := 0
	backlogCalls := 0
	downloadCalls := 0
	originalFetches := 0
	var identityDownloadOrder []string
	var acknowledged []int64
	rpc, cleanup := newMockRPC(t, func(req rpcRequest) string {
		switch req.Method {
		case "get_next_msgs":
			backlogCalls++
			switch backlogCalls {
			case 1:
				return rpcResult(req, []int64{41})
			case 2:
				// download_full_message emits this change immediately when it
				// moves the original into InProgress.
				return rpcResult(req, []int64{41})
			case 3:
				// Completion replaced 41 with 42. The unrelated later 43 is in
				// the same ordered queue batch and must wait behind 42.
				return rpcResult(req, []int64{42, 43})
			default:
				return rpcResult(req, []int64{})
			}
		case "get_next_event":
			events := []dcEvent{
				{
					ContextID: 7,
					Event: dcEventType{
						Kind:  "MsgsChanged",
						MsgID: 41,
					},
				},
				{
					ContextID: 7,
					Event: dcEventType{
						Kind:  "IncomingMsg",
						MsgID: 42,
					},
				},
			}
			if eventIndex >= len(events) {
				return rpcUnexpectedMethod(req)
			}
			event := events[eventIndex]
			eventIndex++
			return rpcResult(req, event)
		case "get_message":
			messageID := int64(req.Params[1].(float64))
			switch messageID {
			case 41:
				originalFetches++
				downloadState := "InProgress"
				if originalFetches == 1 {
					downloadState = "Available"
				}
				return rpcResult(req, dcMessage{
					ID:            41,
					ChatID:        99,
					Text:          "partial",
					DownloadState: downloadState,
					Sender:        &dcContact{Address: "alice@example.org"},
				})
			case 42:
				return rpcResult(req, dcMessage{
					ID:            42,
					ChatID:        99,
					Text:          "replacement",
					DownloadState: deltaChatDownloadDone,
					Sender:        &dcContact{Address: "alice@example.org"},
				})
			case 43:
				return rpcResult(req, dcMessage{
					ID:            43,
					ChatID:        99,
					Text:          "later",
					DownloadState: deltaChatDownloadDone,
					Sender:        &dcContact{Address: "alice@example.org"},
				})
			default:
				return rpcUnexpectedMethod(req)
			}
		case "get_message_info_object":
			messageID := int64(req.Params[1].(float64))
			switch messageID {
			case 41:
				identityDownloadOrder = append(identityDownloadOrder, "identity")
				return rpcResult(req, dcMessageInfo{
					RFC724MID: "<original@example.org>",
				})
			case 42:
				return rpcResult(req, dcMessageInfo{
					RFC724MID: "<original@example.org>",
				})
			case 43:
				return rpcResult(req, dcMessageInfo{
					RFC724MID: "<unrelated@example.org>",
				})
			default:
				return rpcUnexpectedMethod(req)
			}
		case "download_full_message":
			downloadCalls++
			identityDownloadOrder = append(identityDownloadOrder, "download")
			return rpcResult(req, true)
		case "get_full_chat_by_id":
			return rpcResult(req, dcChat{
				ID:       99,
				Name:     "Inbox",
				ChatType: chatTypeSingle,
			})
		case "markseen_msgs":
			ids, ok := req.Params[1].([]any)
			if !ok || len(ids) != 1 {
				t.Fatalf("markseen message IDs = %#v, want one ID", req.Params[1])
			}
			messageID := int64(ids[0].(float64))
			acknowledged = append(acknowledged, messageID)
			if messageID == 43 {
				ch.SetRunning(false)
			}
			return rpcResult(req, nil)
		default:
			return rpcUnexpectedMethod(req)
		}
	})
	defer cleanup()
	ch.rpc = rpc

	done := make(chan struct{})
	go func() {
		defer close(done)
		ch.listen()
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		ch.SetRunning(false)
		t.Fatal("listen did not process the replacement and later message")
	}

	if downloadCalls != 1 {
		t.Fatalf("download_full_message calls = %d, want 1", downloadCalls)
	}
	if originalFetches != 2 {
		t.Fatalf("original message fetches = %d, want Available then InProgress", originalFetches)
	}
	if got := strings.Join(identityDownloadOrder, ","); got != "identity,download" {
		t.Fatalf("identity/download order = %q, want identity before download", got)
	}
	if got := strings.Join(admissionOrder, ","); got != "42,42,43" {
		t.Fatalf("admission order = %q, want replacement retry before later message", got)
	}
	if len(acknowledged) != 2 || acknowledged[0] != 42 || acknowledged[1] != 43 {
		t.Fatalf("acknowledged IDs = %#v, want [42 43]", acknowledged)
	}
	if got := len(msgBus.InboundChan()); got != 2 {
		t.Fatalf("forwarded messages = %d, want replacement and later message", got)
	}
	first := <-msgBus.InboundChan()
	second := <-msgBus.InboundChan()
	if first.MessageID != "42" || second.MessageID != "43" {
		t.Fatalf(
			"forwarded message IDs = [%s %s], want [42 43]",
			first.MessageID,
			second.MessageID,
		)
	}
}

func TestDeltaChatSettingsDecode(t *testing.T) {
	raw := []byte(`{
		"enabled": true,
		"type": "deltachat",
		"allow_from": ["alice@example.org"],
		"settings": {
			"email": "bot@example.org",
			"display_name": "PicoBot",
			"avatar_image": "/tmp/picobot.png",
			"allow_crosspost": true,
			"imap_port": 993
		}
	}`)
	var bc config.Channel
	if err := json.Unmarshal(raw, &bc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	bc.Type = config.ChannelDeltaChat
	decoded, err := bc.GetDecoded()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	cfg, ok := decoded.(*config.DeltaChatSettings)
	if !ok {
		t.Fatalf("decoded type = %T, want *config.DeltaChatSettings", decoded)
	}
	if cfg.Email != "bot@example.org" {
		t.Errorf("email = %q, want bot@example.org", cfg.Email)
	}
	if cfg.DisplayName != "PicoBot" {
		t.Errorf("display_name = %q, want PicoBot", cfg.DisplayName)
	}
	if cfg.AvatarImage != "/tmp/picobot.png" {
		t.Errorf("avatar_image = %q, want /tmp/picobot.png", cfg.AvatarImage)
	}
	if cfg.IMAPPort != 993 {
		t.Errorf("imap_port = %d, want 993", cfg.IMAPPort)
	}
	if !cfg.AllowCrosspost {
		t.Error("allow_crosspost = false, want true")
	}
}

func TestEnsureAccountReconfiguresConfiguredAccountWhenSettingsChange(t *testing.T) {
	ch := newTestChannel(t)
	ch.config.DisplayName = "New Bot"
	ch.config.IMAPServer = "imap.example.org"
	ch.config.IMAPPort = 993
	ch.config.SMTPServer = "smtp.example.org"
	ch.config.SMTPPort = 587

	configureCalls := 0
	accountConfigCalls := 0
	var capturedConfig map[string]any

	rpc, cleanup := newMockRPC(t, func(req rpcRequest) string {
		switch req.Method {
		case "get_all_accounts":
			return rpcResult(req, []dcAccount{{ID: 7, Kind: "Configured", Addr: "bot@example.org"}})
		case "is_configured":
			return rpcResult(req, true)
		case "get_config":
			key, _ := req.Params[1].(string)
			current := map[string]*string{
				"addr":        strPtr("bot@example.org"),
				"mail_pw":     strPtr("old-pw"),
				"displayname": strPtr("Old Bot"),
			}
			return rpcResult(req, current[key])
		case "batch_set_config":
			if cfg, ok := req.Params[1].(map[string]any); ok {
				if _, ok := cfg["mail_pw"]; ok {
					accountConfigCalls++
					capturedConfig = cfg
				}
			}
			return rpcResult(req, nil)
		case "configure":
			configureCalls++
			return rpcResult(req, nil)
		case "select_account", "start_io":
			return rpcResult(req, nil)
		default:
			return rpcUnexpectedMethod(req)
		}
	})
	defer cleanup()
	ch.rpc = rpc

	if err := ch.ensureAccount(context.Background()); err != nil {
		t.Fatalf("ensureAccount: %v", err)
	}
	if configureCalls != 1 {
		t.Fatalf("configure calls = %d, want 1", configureCalls)
	}
	if accountConfigCalls != 1 {
		t.Fatalf("account batch_set_config calls = %d, want 1", accountConfigCalls)
	}
	if capturedConfig["mail_pw"] != "pw" {
		t.Errorf("mail_pw = %v, want pw", capturedConfig["mail_pw"])
	}
	if capturedConfig["mail_server"] != "imap.example.org" {
		t.Errorf("mail_server = %v, want imap.example.org", capturedConfig["mail_server"])
	}
	if capturedConfig["mail_port"] != "993" {
		t.Errorf("mail_port = %v, want 993", capturedConfig["mail_port"])
	}
	if capturedConfig["send_server"] != "smtp.example.org" {
		t.Errorf("send_server = %v, want smtp.example.org", capturedConfig["send_server"])
	}
	if capturedConfig["send_port"] != "587" {
		t.Errorf("send_port = %v, want 587", capturedConfig["send_port"])
	}
}

func TestEnsureAccountSkipsConfiguredAccountWhenSettingsMatch(t *testing.T) {
	ch := newTestChannel(t)
	ch.config.DisplayName = "Pico Bot"
	ch.config.IMAPServer = "imap.example.org"
	ch.config.IMAPPort = 993
	ch.config.SMTPServer = "smtp.example.org"
	ch.config.SMTPPort = 587

	configureCalls := 0
	accountConfigCalls := 0

	rpc, cleanup := newMockRPC(t, func(req rpcRequest) string {
		switch req.Method {
		case "get_all_accounts":
			return rpcResult(req, []dcAccount{{ID: 7, Kind: "Configured", Addr: "bot@example.org"}})
		case "is_configured":
			return rpcResult(req, true)
		case "get_config":
			key, _ := req.Params[1].(string)
			current := map[string]*string{
				"addr":        strPtr("bot@example.org"),
				"mail_pw":     strPtr("pw"),
				"displayname": strPtr("Pico Bot"),
				"mail_server": strPtr("imap.example.org"),
				"mail_port":   strPtr("993"),
				"send_server": strPtr("smtp.example.org"),
				"send_port":   strPtr("587"),
			}
			return rpcResult(req, current[key])
		case "batch_set_config":
			if cfg, ok := req.Params[1].(map[string]any); ok {
				if _, ok := cfg["mail_pw"]; ok {
					accountConfigCalls++
				}
			}
			return rpcResult(req, nil)
		case "configure":
			configureCalls++
			return rpcResult(req, nil)
		case "select_account", "start_io":
			return rpcResult(req, nil)
		default:
			return rpcUnexpectedMethod(req)
		}
	})
	defer cleanup()
	ch.rpc = rpc

	if err := ch.ensureAccount(context.Background()); err != nil {
		t.Fatalf("ensureAccount: %v", err)
	}
	if configureCalls != 0 {
		t.Fatalf("configure calls = %d, want 0", configureCalls)
	}
	if accountConfigCalls != 0 {
		t.Fatalf("account batch_set_config calls = %d, want 0", accountConfigCalls)
	}
}

func TestEnsureAccountCreatesBootstrapAccountAndStops(t *testing.T) {
	ch := newTestChannel(t)
	ch.config.Password = config.SecureString{}
	ch.config.Email = "@mehl.cloud"

	rpc, cleanup := newMockRPC(t, func(req rpcRequest) string {
		switch req.Method {
		case "add_account":
			return rpcResult(req, int64(9))
		case "add_transport_from_qr":
			if req.Params[0] != float64(9) {
				t.Fatalf("account id = %v, want 9", req.Params[0])
			}
			if req.Params[1] != "DCACCOUNT:https://mehl.cloud/new" {
				t.Fatalf("qr = %v", req.Params[1])
			}
			return rpcResult(req, nil)
		case "get_config":
			if req.Params[1] != "addr" {
				t.Fatalf("get_config key = %v, want addr", req.Params[1])
			}
			return rpcResult(req, "bot123@mehl.cloud")
		default:
			return rpcUnexpectedMethod(req)
		}
	})
	defer cleanup()
	ch.rpc = rpc

	err := ch.ensureAccount(context.Background())
	if err == nil {
		t.Fatal("expected created-account instruction error")
	}
	if !strings.Contains(err.Error(), "bot123@mehl.cloud") || !strings.Contains(err.Error(), "run PicoClaw again") {
		t.Fatalf("error = %v, want generated email and rerun instruction", err)
	}
}

func TestEnsureAccountUsesConfiguredAccountWithoutPassword(t *testing.T) {
	ch := newTestChannel(t)
	ch.config.Password = config.SecureString{}
	ch.config.DisplayName = "Local Bot"
	avatar := filepath.Join(t.TempDir(), "avatar.png")
	if err := os.WriteFile(avatar, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	ch.config.AvatarImage = avatar

	profileConfigCalls := 0
	rpc, cleanup := newMockRPC(t, func(req rpcRequest) string {
		switch req.Method {
		case "get_all_accounts":
			return rpcResult(req, []dcAccount{{ID: 7, Kind: "Configured", Addr: "bot@example.org"}})
		case "is_configured":
			return rpcResult(req, true)
		case "batch_set_config":
			cfg, _ := req.Params[1].(map[string]any)
			if _, ok := cfg["bot"]; ok {
				return rpcResult(req, nil)
			}
			profileConfigCalls++
			if cfg["displayname"] != "Local Bot" {
				t.Fatalf("displayname = %v, want Local Bot", cfg["displayname"])
			}
			if cfg["selfavatar"] != avatar {
				t.Fatalf("selfavatar = %v, want %s", cfg["selfavatar"], avatar)
			}
			return rpcResult(req, nil)
		case "select_account", "start_io":
			return rpcResult(req, nil)
		default:
			return rpcUnexpectedMethod(req)
		}
	})
	defer cleanup()
	ch.rpc = rpc

	if err := ch.ensureAccount(context.Background()); err != nil {
		t.Fatalf("ensureAccount: %v", err)
	}
	if profileConfigCalls != 1 {
		t.Fatalf("profile config calls = %d, want 1", profileConfigCalls)
	}
	if ch.accountID != 7 {
		t.Fatalf("accountID = %d, want 7", ch.accountID)
	}
}

func TestEnsureAccountSkipsMissingAvatarImage(t *testing.T) {
	ch := newTestChannel(t)
	ch.config.Password = config.SecureString{}
	ch.config.AvatarImage = filepath.Join(t.TempDir(), "missing.png")

	profileConfigCalls := 0
	rpc, cleanup := newMockRPC(t, func(req rpcRequest) string {
		switch req.Method {
		case "get_all_accounts":
			return rpcResult(req, []dcAccount{{ID: 7, Kind: "Configured", Addr: "bot@example.org"}})
		case "is_configured":
			return rpcResult(req, true)
		case "batch_set_config":
			cfg, _ := req.Params[1].(map[string]any)
			if _, ok := cfg["bot"]; !ok {
				profileConfigCalls++
			}
			return rpcResult(req, nil)
		case "select_account", "start_io":
			return rpcResult(req, nil)
		default:
			return rpcUnexpectedMethod(req)
		}
	})
	defer cleanup()
	ch.rpc = rpc

	if err := ch.ensureAccount(context.Background()); err != nil {
		t.Fatalf("ensureAccount: %v", err)
	}
	if profileConfigCalls != 0 {
		t.Fatalf("profile config calls = %d, want 0", profileConfigCalls)
	}
}

func TestEnsureAccountRequiresPasswordWhenAccountMissing(t *testing.T) {
	ch := newTestChannel(t)
	ch.config.Password = config.SecureString{}

	rpc, cleanup := newMockRPC(t, func(req rpcRequest) string {
		switch req.Method {
		case "get_all_accounts":
			return rpcResult(req, []dcAccount{})
		default:
			return rpcUnexpectedMethod(req)
		}
	})
	defer cleanup()
	ch.rpc = rpc

	err := ch.ensureAccount(context.Background())
	if err == nil {
		t.Fatal("expected password-required error")
	}
	if !strings.Contains(err.Error(), "is not configured") {
		t.Fatalf("error = %v, want not-configured error", err)
	}
}

func TestEnsureAccountClearsRemovedOptionalSettings(t *testing.T) {
	ch := newTestChannel(t)

	var capturedConfig map[string]any

	rpc, cleanup := newMockRPC(t, func(req rpcRequest) string {
		switch req.Method {
		case "get_all_accounts":
			return rpcResult(req, []dcAccount{{ID: 7, Kind: "Configured", Addr: "bot@example.org"}})
		case "is_configured":
			return rpcResult(req, true)
		case "get_config":
			key, _ := req.Params[1].(string)
			current := map[string]*string{
				"addr":        strPtr("bot@example.org"),
				"mail_pw":     strPtr("pw"),
				"displayname": strPtr("Old Bot"),
				"mail_server": strPtr("imap.example.org"),
				"mail_port":   strPtr("993"),
				"send_server": strPtr("smtp.example.org"),
				"send_port":   strPtr("587"),
			}
			return rpcResult(req, current[key])
		case "batch_set_config":
			if cfg, ok := req.Params[1].(map[string]any); ok {
				if _, ok := cfg["mail_pw"]; ok {
					capturedConfig = cfg
				}
			}
			return rpcResult(req, nil)
		case "configure", "select_account", "start_io":
			return rpcResult(req, nil)
		default:
			return rpcUnexpectedMethod(req)
		}
	})
	defer cleanup()
	ch.rpc = rpc

	if err := ch.ensureAccount(context.Background()); err != nil {
		t.Fatalf("ensureAccount: %v", err)
	}
	if capturedConfig == nil {
		t.Fatal("account batch_set_config was not called")
	}
	for _, key := range []string{"mail_server", "mail_port", "send_server", "send_port"} {
		if value, ok := capturedConfig[key]; !ok || value != nil {
			t.Errorf("%s = %v (present %v), want explicit null", key, value, ok)
		}
	}
	if capturedConfig["addr"] != "bot@example.org" {
		t.Errorf("addr = %v, want bot@example.org", capturedConfig["addr"])
	}
	if capturedConfig["mail_pw"] != "pw" {
		t.Errorf("mail_pw = %v, want pw", capturedConfig["mail_pw"])
	}
}

// TestRPCClientRoundTrip drives the JSON-RPC client against an in-process mock
// server over pipes, verifying id correlation and error propagation.
func TestRPCClientRoundTrip(t *testing.T) {
	reqR, reqW := io.Pipe()   // client -> server
	respR, respW := io.Pipe() // server -> client

	c := &rpcClient{
		stdin:   reqW,
		stdout:  respR,
		pending: make(map[uint64]chan rpcResponse),
	}
	go c.readLoop()

	// Mock server: echo method "ping" -> "pong", anything else -> error.
	go func() {
		scanner := bufio.NewScanner(reqR)
		for scanner.Scan() {
			var req rpcRequest
			if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
				continue
			}
			var resp string
			if req.Method == "ping" {
				resp = `{"jsonrpc":"2.0","id":` + itoa(req.ID) + `,"result":"pong"}`
			} else {
				resp = `{"jsonrpc":"2.0","id":` + itoa(req.ID) + `,"error":{"code":-1,"message":"boom"}}`
			}
			_, _ = respW.Write([]byte(resp + "\n"))
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	raw, err := c.call(ctx, "ping")
	if err != nil {
		t.Fatalf("ping call: %v", err)
	}
	var result string
	if err := json.Unmarshal(raw, &result); err != nil || result != "pong" {
		t.Fatalf("ping result = %q (err %v), want pong", result, err)
	}

	if _, err := c.call(ctx, "explode"); err == nil {
		t.Fatal("expected error from explode call")
	}
}

func itoa(n uint64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// newTestChannel builds a DeltaChatChannel with a valid config (backed by a fake
// rpc-server binary) but without starting any IO, for unit-testing methods in
// isolation.
func newTestChannel(t *testing.T) *DeltaChatChannel {
	return newTestChannelWithBus(t, bus.NewMessageBus(), nil)
}

func newTestChannelWithBus(t *testing.T, msgBus *bus.MessageBus, configure func(*config.Channel)) *DeltaChatChannel {
	t.Helper()
	fakeServer := filepath.Join(t.TempDir(), "deltachat-rpc-server")
	if err := os.WriteFile(fakeServer, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	bc := &config.Channel{Type: config.ChannelDeltaChat, Enabled: true}
	if configure != nil {
		configure(bc)
	}
	cfg := &config.DeltaChatSettings{
		Email:         "bot@example.org",
		Password:      *config.NewSecureString("pw"),
		RPCServerPath: fakeServer,
		DataDir:       t.TempDir(),
	}
	ch, err := NewDeltaChatChannel(bc, cfg, msgBus)
	if err != nil {
		t.Fatalf("new channel: %v", err)
	}
	return ch
}

// newMockRPC wires an rpcClient to an in-process server that replies to every
// request with handler(req), so methods that call the rpc can be tested without
// a real deltachat-rpc-server.
func newMockRPC(t *testing.T, handler func(req rpcRequest) string) (*rpcClient, func()) {
	t.Helper()
	reqR, reqW := io.Pipe()
	respR, respW := io.Pipe()
	c := &rpcClient{
		stdin:   reqW,
		stdout:  respR,
		pending: make(map[uint64]chan rpcResponse),
	}
	go c.readLoop()
	go func() {
		scanner := bufio.NewScanner(reqR)
		for scanner.Scan() {
			var req rpcRequest
			if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
				continue
			}
			_, _ = respW.Write([]byte(handler(req) + "\n"))
		}
	}()
	return c, func() { _ = reqW.Close(); _ = respW.Close() }
}

func rpcResult(req rpcRequest, result any) string {
	raw, _ := json.Marshal(result)
	return `{"jsonrpc":"2.0","id":` + itoa(req.ID) + `,"result":` + string(raw) + `}`
}

func rpcUnexpectedMethod(req rpcRequest) string {
	return `{"jsonrpc":"2.0","id":` + itoa(req.ID) + `,"error":{"code":-32601,"message":"unexpected method"}}`
}

func strPtr(value string) *string {
	return &value
}

// TestMessageDataJSON pins the camelCase keys and omitempty behavior expected by
// Delta Chat's send_msg MessageData parameter.
func TestMessageDataJSON(t *testing.T) {
	raw, err := json.Marshal(dcMessageData{File: "/tmp/x.png"})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); got != `{"file":"/tmp/x.png"}` {
		t.Errorf("json = %s, want only the file field", got)
	}

	raw, _ = json.Marshal(dcMessageData{Text: "hi", File: "/f", Filename: "f.bin"})
	if got := string(raw); got != `{"text":"hi","file":"/f","filename":"f.bin"}` {
		t.Errorf("json = %s, want text/file/filename in camelCase", got)
	}
}

// TestRegisterInboundFile checks that an inbound attachment is copied out of
// Delta Chat's account directory into the tool-readable media temp dir and
// registered with delete-on-cleanup, and that the absence of a store yields an
// empty ref for the annotation fallback.
func TestRegisterInboundFile(t *testing.T) {
	ch := newTestChannel(t)

	tmp := filepath.Join(t.TempDir(), "doc.pdf")
	if err := os.WriteFile(tmp, []byte("%PDF-1.4"), 0o644); err != nil {
		t.Fatal(err)
	}
	msg := &dcMessage{File: tmp, FileName: "doc.pdf", FileMime: "application/pdf"}

	if ref := ch.registerInboundFile("scope", msg); ref != "" {
		t.Errorf("ref without media store = %q, want empty", ref)
	}

	store := media.NewFileMediaStore()
	ch.SetMediaStore(store)
	ref := ch.registerInboundFile("scope", msg)
	if !strings.HasPrefix(ref, "media://") {
		t.Fatalf("ref = %q, want a media:// ref", ref)
	}
	path, meta, err := store.ResolveWithMeta(ref)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })

	// The registered path must be a copy in the media temp dir (tool-readable),
	// not the original blob path, and must have the same contents.
	if path == tmp {
		t.Errorf("path = %q, want a copy in the media temp dir, not the blob path", path)
	}
	if !strings.HasPrefix(filepath.Clean(path), filepath.Clean(media.TempDir())) {
		t.Errorf("path = %q, want it under media temp dir %q", path, media.TempDir())
	}
	if data, rerr := os.ReadFile(path); rerr != nil || string(data) != "%PDF-1.4" {
		t.Errorf("copied file contents = %q (err %v), want %q", string(data), rerr, "%PDF-1.4")
	}
	if meta.ContentType != "application/pdf" {
		t.Errorf("content type = %q, want application/pdf", meta.ContentType)
	}
	if meta.CleanupPolicy != media.CleanupPolicyDeleteOnCleanup {
		t.Errorf("cleanup policy = %q, want delete_on_cleanup", meta.CleanupPolicy)
	}
}

func TestRegisterInboundFileRejectsDeclaredOversize(t *testing.T) {
	ch := newTestChannel(t)
	store := media.NewFileMediaStore()
	ch.SetMediaStore(store)
	defer func() { _ = store.ReleaseAll("oversize-scope") }()

	source := filepath.Join(t.TempDir(), "declared-oversize.bin")
	if err := os.WriteFile(source, []byte("small"), 0o600); err != nil {
		t.Fatal(err)
	}
	msg := &dcMessage{
		ID:        42,
		File:      source,
		FileName:  "declared-oversize.bin",
		FileBytes: int64(config.DefaultMaxMediaSize) + 1,
	}

	if ref := ch.registerInboundFile("oversize-scope", msg); ref != "" {
		t.Fatalf("declared oversized attachment ref = %q, want empty", ref)
	}
}

func TestCopyToMediaTempEnforcesActualLimitWithoutPathLeak(t *testing.T) {
	privateDir := filepath.Join(t.TempDir(), "private-account-credential-db")
	if err := os.MkdirAll(privateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(privateDir, "private-blob")
	if err := os.WriteFile(source, []byte("123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	filename := "oversize-" + filepath.Base(t.TempDir()) + ".bin"
	pattern := filepath.Join(media.TempDir(), "*_"+filename)
	before, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}

	copied, err := copyToMediaTempWithLimit(source, filename, 8)
	if !errors.Is(err, errDeltaChatAttachmentTooLarge) {
		t.Fatalf("copy error = %v, want attachment-too-large", err)
	}
	if copied != "" {
		t.Fatalf("oversized copy path = %q, want empty", copied)
	}
	if strings.Contains(err.Error(), privateDir) ||
		strings.Contains(err.Error(), source) {
		t.Fatalf("oversize error exposed private path: %v", err)
	}
	after, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("partial oversized copies before=%v after=%v", before, after)
	}
}

func TestCopyToMediaTempFailureDoesNotExposeSourcePath(t *testing.T) {
	privatePath := filepath.Join(
		t.TempDir(),
		"private-account-credential-db",
		"missing-blob",
	)
	copied, err := copyToMediaTempWithLimit(privatePath, "missing.bin", 8)
	if !errors.Is(err, errDeltaChatAttachmentCopy) {
		t.Fatalf("copy error = %v, want sanitized attachment-copy failure", err)
	}
	if copied != "" {
		t.Fatalf("failed copy path = %q, want empty", copied)
	}
	if strings.Contains(err.Error(), privatePath) ||
		strings.Contains(err.Error(), filepath.Dir(privatePath)) {
		t.Fatalf("copy error exposed private path: %v", err)
	}
	if reason := deltaChatAttachmentFailureReason(err); reason != "copy_failed" {
		t.Fatalf("sanitized failure reason = %q, want copy_failed", reason)
	}
}

func TestSend_CurrentNumericChatIDAllowedWithoutRecipientResolution(t *testing.T) {
	ch := newTestChannel(t)
	ch.rpc, _ = newMockRPC(t, func(req rpcRequest) string {
		if req.Method != "misc_send_msg" {
			return rpcUnexpectedMethod(req)
		}
		if got, _ := req.Params[1].(float64); got != 42 {
			t.Fatalf("chat id = %v, want 42", req.Params[1])
		}
		return rpcResult(req, []any{int64(1001), map[string]any{}})
	})
	ch.accountID = 7
	ch.SetRunning(true)

	ids, err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  "42",
		Content: "hello",
		Context: bus.InboundContext{ChatID: "42", SenderID: "friend@example.org"},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(ids) != 1 || ids[0] != "1001" {
		t.Fatalf("ids = %v, want [1001]", ids)
	}
}

func TestSend_CrossChatNumericDeniedByDefault(t *testing.T) {
	ch := newTestChannel(t)
	ch.rpc, _ = newMockRPC(t, func(req rpcRequest) string {
		t.Fatalf("unexpected rpc call: %s", req.Method)
		return rpcUnexpectedMethod(req)
	})
	ch.accountID = 7
	ch.SetRunning(true)

	_, err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  "99",
		Content: "hello",
		Context: bus.InboundContext{ChatID: "42", SenderID: "admin@example.org"},
	})
	if err == nil || !strings.Contains(err.Error(), "allow_crosspost") {
		t.Fatalf("Send error = %v, want crosspost recipient gate", err)
	}
}

func TestSend_EmailRecipientDeniedByDefault(t *testing.T) {
	ch := newTestChannel(t)
	ch.rpc, _ = newMockRPC(t, func(req rpcRequest) string {
		t.Fatalf("unexpected rpc call: %s", req.Method)
		return rpcUnexpectedMethod(req)
	})
	ch.accountID = 7
	ch.SetRunning(true)

	_, err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  "friend@example.org",
		Content: "hello",
		Context: bus.InboundContext{ChatID: "42", SenderID: "admin@example.org"},
	})
	if err == nil || !strings.Contains(err.Error(), "allow_crosspost") {
		t.Fatalf("Send error = %v, want crosspost recipient gate", err)
	}
}

func TestSend_EmailRecipientRequiresAllowFrom(t *testing.T) {
	ch := newTestChannelWithBus(t, bus.NewMessageBus(), func(bc *config.Channel) {
		bc.AllowFrom = config.FlexibleStringSlice{"admin@example.org"}
	})
	ch.config.AllowCrosspost = true
	ch.rpc, _ = newMockRPC(t, func(req rpcRequest) string {
		t.Fatalf("unexpected rpc call: %s", req.Method)
		return rpcUnexpectedMethod(req)
	})
	ch.accountID = 7
	ch.SetRunning(true)

	_, err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  "friend@example.org",
		Content: "hello",
		Context: bus.InboundContext{ChatID: "42", SenderID: "other@example.org"},
	})
	if err == nil || !strings.Contains(err.Error(), "allow_from") {
		t.Fatalf("Send error = %v, want allow_from gate", err)
	}
}

func TestSend_EmailRecipientUsesSessionScopeSenderForAdmin(t *testing.T) {
	ch := newTestChannelWithBus(t, bus.NewMessageBus(), func(bc *config.Channel) {
		bc.AllowFrom = config.FlexibleStringSlice{"admin@example.org"}
	})
	ch.config.AllowCrosspost = true

	var sentChatID float64
	ch.rpc, _ = newMockRPC(t, func(req rpcRequest) string {
		switch req.Method {
		case "lookup_contact_id_by_addr":
			return rpcResult(req, int64(11))
		case "get_chat_id_by_contact_id":
			return rpcResult(req, int64(99))
		case "misc_send_msg":
			sentChatID, _ = req.Params[1].(float64)
			return rpcResult(req, []any{int64(1233), map[string]any{}})
		default:
			return rpcUnexpectedMethod(req)
		}
	})
	ch.accountID = 7
	ch.SetRunning(true)

	_, err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  "friend@example.org",
		Content: "hello",
		Context: bus.InboundContext{ChatID: "friend@example.org"},
		Scope: &bus.OutboundScope{
			Channel: config.ChannelDeltaChat,
			Values: map[string]string{
				"chat":   "direct:42",
				"sender": "admin@example.org",
			},
		},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if sentChatID != 99 {
		t.Fatalf("sent chat id = %v, want 99", sentChatID)
	}
}

func TestSend_EmailRecipientResolvesForAllowFromWildcard(t *testing.T) {
	ch := newTestChannelWithBus(t, bus.NewMessageBus(), func(bc *config.Channel) {
		bc.AllowFrom = config.FlexibleStringSlice{"*"}
	})
	ch.config.AllowCrosspost = true

	var sentChatID float64
	ch.rpc, _ = newMockRPC(t, func(req rpcRequest) string {
		switch req.Method {
		case "lookup_contact_id_by_addr":
			return rpcResult(req, int64(11))
		case "get_chat_id_by_contact_id":
			return rpcResult(req, int64(99))
		case "misc_send_msg":
			sentChatID, _ = req.Params[1].(float64)
			return rpcResult(req, []any{int64(1236), map[string]any{}})
		default:
			return rpcUnexpectedMethod(req)
		}
	})
	ch.accountID = 7
	ch.SetRunning(true)

	_, err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  "friend@example.org",
		Content: "hello",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if sentChatID != 99 {
		t.Fatalf("sent chat id = %v, want 99", sentChatID)
	}
}

func TestSend_CrossChatNumericUsesSessionScopeChatForGate(t *testing.T) {
	ch := newTestChannel(t)
	ch.rpc, _ = newMockRPC(t, func(req rpcRequest) string {
		t.Fatalf("unexpected rpc call: %s", req.Method)
		return rpcUnexpectedMethod(req)
	})
	ch.accountID = 7
	ch.SetRunning(true)

	_, err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  "99",
		Content: "hello",
		Context: bus.InboundContext{ChatID: "99", SenderID: "admin@example.org"},
		Scope: &bus.OutboundScope{
			Channel: config.ChannelDeltaChat,
			Values:  map[string]string{"chat": "direct:42"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "allow_crosspost") {
		t.Fatalf("Send error = %v, want crosspost recipient gate", err)
	}
}

func TestSend_EmailRecipientResolvesForAdmin(t *testing.T) {
	ch := newTestChannelWithBus(t, bus.NewMessageBus(), func(bc *config.Channel) {
		bc.AllowFrom = config.FlexibleStringSlice{"admin@example.org"}
	})
	ch.config.AllowCrosspost = true

	var sentChatID float64
	ch.rpc, _ = newMockRPC(t, func(req rpcRequest) string {
		switch req.Method {
		case "lookup_contact_id_by_addr":
			if req.Params[1] != "friend@example.org" {
				t.Fatalf("lookup addr = %v, want friend@example.org", req.Params[1])
			}
			return rpcResult(req, int64(11))
		case "get_chat_id_by_contact_id":
			return rpcResult(req, nil)
		case "create_chat_by_contact_id":
			return rpcResult(req, int64(99))
		case "misc_send_msg":
			sentChatID, _ = req.Params[1].(float64)
			return rpcResult(req, []any{int64(1234), map[string]any{}})
		default:
			return rpcUnexpectedMethod(req)
		}
	})
	ch.accountID = 7
	ch.SetRunning(true)

	ids, err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  "Friend <friend@example.org>",
		Content: "hello",
		Context: bus.InboundContext{ChatID: "42", SenderID: "admin@example.org"},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if sentChatID != 99 {
		t.Fatalf("sent chat id = %v, want 99", sentChatID)
	}
	if len(ids) != 1 || ids[0] != "1234" {
		t.Fatalf("ids = %v, want [1234]", ids)
	}
}

func TestSend_AliasRecipientResolvesForAdmin(t *testing.T) {
	ch := newTestChannelWithBus(t, bus.NewMessageBus(), func(bc *config.Channel) {
		bc.AllowFrom = config.FlexibleStringSlice{"admin@example.org"}
	})
	ch.config.AllowCrosspost = true

	var sentChatID float64
	ch.rpc, _ = newMockRPC(t, func(req rpcRequest) string {
		switch req.Method {
		case "get_contacts":
			if req.Params[2] != "Alice" {
				t.Fatalf("contact query = %v, want Alice", req.Params[2])
			}
			return rpcResult(req, []dcContact{{ID: 12, Address: "alice@example.org", DisplayName: "Alice"}})
		case "get_chat_id_by_contact_id":
			return rpcResult(req, int64(88))
		case "misc_send_msg":
			sentChatID, _ = req.Params[1].(float64)
			return rpcResult(req, []any{int64(1235), map[string]any{}})
		default:
			return rpcUnexpectedMethod(req)
		}
	})
	ch.accountID = 7
	ch.SetRunning(true)

	_, err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  "Alice",
		Content: "hello",
		Context: bus.InboundContext{ChatID: "42", SenderID: "admin@example.org"},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if sentChatID != 88 {
		t.Fatalf("sent chat id = %v, want 88", sentChatID)
	}
}

// TestSendMedia verifies SendMedia resolves a media ref to a local path and
// drives send_msg with the expected MessageData, returning the new message id.
func TestSendMedia(t *testing.T) {
	ch := newTestChannel(t)

	tmp := filepath.Join(t.TempDir(), "photo.png")
	if err := os.WriteFile(tmp, []byte("\x89PNGfake"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := media.NewFileMediaStore()
	ch.SetMediaStore(store)
	ref, err := store.Store(tmp, media.MediaMeta{Filename: "photo.png"}, "scope")
	if err != nil {
		t.Fatal(err)
	}

	captured := make(chan rpcRequest, 1)
	rpc, cleanup := newMockRPC(t, func(req rpcRequest) string {
		captured <- req
		return `{"jsonrpc":"2.0","id":` + itoa(req.ID) + `,"result":4242}`
	})
	defer cleanup()
	ch.rpc = rpc
	ch.accountID = 7
	ch.SetRunning(true)

	ids, err := ch.SendMedia(context.Background(), bus.OutboundMediaMessage{
		ChatID: "99",
		Parts: []bus.MediaPart{{
			Type:     "image",
			Ref:      ref,
			Caption:  "hello",
			Filename: "photo.png",
		}},
	})
	if err != nil {
		t.Fatalf("SendMedia: %v", err)
	}
	if len(ids) != 1 || ids[0] != "4242" {
		t.Fatalf("ids = %v, want [4242]", ids)
	}

	select {
	case req := <-captured:
		if req.Method != "send_msg" {
			t.Errorf("method = %q, want send_msg", req.Method)
		}
		if len(req.Params) != 3 {
			t.Fatalf("params = %v, want [accountID, chatID, data]", req.Params)
		}
		if got, _ := req.Params[0].(float64); got != 7 {
			t.Errorf("account id = %v, want 7", req.Params[0])
		}
		if got, _ := req.Params[1].(float64); got != 99 {
			t.Errorf("chat id = %v, want 99", req.Params[1])
		}
		data, ok := req.Params[2].(map[string]any)
		if !ok {
			t.Fatalf("data param = %T, want object", req.Params[2])
		}
		if data["file"] != tmp {
			t.Errorf("file = %v, want %s", data["file"], tmp)
		}
		if data["text"] != "hello" {
			t.Errorf("text = %v, want hello", data["text"])
		}
		if data["filename"] != "photo.png" {
			t.Errorf("filename = %v, want photo.png", data["filename"])
		}
		if _, present := data["viewtype"]; present {
			t.Errorf("viewtype should be omitted (Delta Chat infers it), got %v", data["viewtype"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("mock server never received the request")
	}
}

// TestSendMediaNoStore ensures SendMedia fails cleanly without a media store.
func TestSendMediaNoStore(t *testing.T) {
	ch := newTestChannel(t)
	ch.SetRunning(true)
	if _, err := ch.SendMedia(context.Background(), bus.OutboundMediaMessage{ChatID: "1"}); err == nil {
		t.Error("expected error when no media store is configured")
	}
}

// TestSendMediaVoice verifies that a send_tts-sourced audio part is delivered
// with viewtype "Voice" so Delta Chat renders it as a voice bubble.
func TestSendMediaVoice(t *testing.T) {
	ch := newTestChannel(t)

	tmp := filepath.Join(t.TempDir(), "tts-123.ogg")
	if err := os.WriteFile(tmp, []byte("OggSfake"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := media.NewFileMediaStore()
	ch.SetMediaStore(store)
	ref, err := store.Store(tmp, media.MediaMeta{
		Filename:    "tts-123.ogg",
		ContentType: "audio/ogg",
		Source:      "tool:send_tts",
	}, "scope")
	if err != nil {
		t.Fatal(err)
	}

	captured := make(chan rpcRequest, 1)
	rpc, cleanup := newMockRPC(t, func(req rpcRequest) string {
		captured <- req
		return `{"jsonrpc":"2.0","id":` + itoa(req.ID) + `,"result":7}`
	})
	defer cleanup()
	ch.rpc = rpc
	ch.accountID = 1
	ch.SetRunning(true)

	if _, err := ch.SendMedia(context.Background(), bus.OutboundMediaMessage{
		ChatID: "5",
		Parts:  []bus.MediaPart{{Type: "audio", Ref: ref, ContentType: "audio/ogg"}},
	}); err != nil {
		t.Fatalf("SendMedia: %v", err)
	}

	select {
	case req := <-captured:
		data, ok := req.Params[2].(map[string]any)
		if !ok {
			t.Fatalf("data param = %T, want object", req.Params[2])
		}
		if data["viewtype"] != "Voice" {
			t.Errorf("viewtype = %v, want Voice", data["viewtype"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("mock server never received the request")
	}
}

// TestDeltaChatViewtype pins the rule that only voice audio is forced to a view
// type; everything else is left to Delta Chat's own detection.
func TestDeltaChatViewtype(t *testing.T) {
	tests := []struct {
		name string
		part bus.MediaPart
		meta media.MediaMeta
		want string
	}{
		{
			"tts audio",
			bus.MediaPart{Type: "audio"},
			media.MediaMeta{Source: "tool:send_tts", ContentType: "audio/ogg"},
			"Voice",
		},
		{"voice filename", bus.MediaPart{Type: "audio", Filename: "my-voice.mp3"}, media.MediaMeta{}, "Voice"},
		{
			"plain audio",
			bus.MediaPart{Type: "audio", Filename: "song.mp3"},
			media.MediaMeta{ContentType: "audio/mpeg"},
			"",
		},
		{"image", bus.MediaPart{Type: "image", Filename: "photo.png"}, media.MediaMeta{ContentType: "image/png"}, ""},
		{"file", bus.MediaPart{Type: "file", Filename: "doc.pdf"}, media.MediaMeta{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deltaChatViewtype(tt.part, tt.meta); got != tt.want {
				t.Errorf("deltaChatViewtype() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestVoiceCapabilities checks that Delta Chat advertises ASR and TTS so the
// gateway's startup capability log is accurate.
func TestVoiceCapabilities(t *testing.T) {
	ch := newTestChannel(t)
	caps := ch.VoiceCapabilities()
	if !caps.ASR || !caps.TTS {
		t.Errorf("VoiceCapabilities() = %+v, want both ASR and TTS true", caps)
	}
}
