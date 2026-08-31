package weixin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestWeixinStartPollPersistAndInboundCoverageCloseout(t *testing.T) {
	setWeixinPersistenceHome(t)
	var (
		requestMu       sync.Mutex
		getUpdatesCalls int
	)
	secondPoll := make(chan struct{})
	pollCanceled := make(chan struct{})
	releasePoll := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/ilink/bot/getupdates":
			requestMu.Lock()
			getUpdatesCalls++
			call := getUpdatesCalls
			requestMu.Unlock()
			if call == 1 {
				_ = json.NewEncoder(writer).Encode(GetUpdatesResp{
					GetUpdatesBuf:        "cursor-next",
					LongpollingTimeoutMs: 25,
					Msgs: []WeixinMessage{{
						FromUserID:   "user-new",
						ClientID:     "message-1",
						SessionID:    "session-1",
						ContextToken: "context-new",
						ItemList: []MessageItem{{
							Type:     MessageItemTypeText,
							TextItem: &TextItem{Text: "hello from Weixin"},
						}},
					}},
				})
				return
			}
			select {
			case <-secondPoll:
			default:
				close(secondPoll)
			}
			select {
			case <-request.Context().Done():
			case <-releasePoll:
			}
			select {
			case <-pollCanceled:
			default:
				close(pollCanceled)
			}
		case "/ilink/bot/sendmessage":
			_ = json.NewEncoder(writer).Encode(SendMessageResp{})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	settings := &config.WeixinSettings{BaseURL: server.URL + "/"}
	settings.SetToken("token")
	cursorPath := buildWeixinSyncBufPath(settings)
	tokenPath := buildWeixinContextTokensPath(settings)
	if err := saveGetUpdatesBuf(cursorPath, "cursor-old"); err != nil {
		t.Fatal(err)
	}
	if err := saveContextToken(tokenPath, "user-old", "context-old"); err != nil {
		t.Fatal(err)
	}

	messageBus := bus.NewMessageBus()
	defer messageBus.Close()
	channel, err := NewWeixinChannel(
		&config.Channel{Type: config.ChannelWeixin, Enabled: true},
		settings,
		messageBus,
	)
	if err != nil {
		t.Fatal(err)
	}
	if startErr := channel.Start(t.Context()); startErr != nil {
		t.Fatal(startErr)
	}
	if !channel.IsRunning() {
		t.Fatal("channel is not running after Start")
	}

	select {
	case inbound := <-messageBus.InboundChan():
		if inbound.Content != "hello from Weixin" || inbound.ChatID != "user-new" ||
			inbound.MessageID != "message-1" ||
			inbound.Context.ReplyHandles["context_token"] != "context-new" {
			t.Fatalf("inbound message = %#v", inbound)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Weixin inbound message")
	}
	select {
	case <-secondPoll:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for second poll")
	}

	if cursor, loadErr := loadGetUpdatesBuf(cursorPath); loadErr != nil || cursor != "cursor-next" {
		t.Fatalf("persisted cursor = %q, %v", cursor, loadErr)
	}
	tokens, err := loadContextTokens(tokenPath)
	if err != nil || tokens["user-old"] != "context-old" || tokens["user-new"] != "context-new" {
		t.Fatalf("persisted context tokens = %#v, %v", tokens, err)
	}
	if value, ok := channel.contextTokens.Load("user-old"); !ok || value != "context-old" {
		t.Fatalf("restored context token = (%v, %v)", value, ok)
	}

	if _, err := channel.Send(t.Context(), bus.OutboundMessage{
		ChatID: "user-new", Content: "outbound reply",
	}); err != nil {
		t.Fatalf("Send(success) error = %v", err)
	}
	if result, err := channel.Send(t.Context(), bus.OutboundMessage{
		ChatID: "user-new",
	}); err != nil || result != nil {
		t.Fatalf("Send(empty) = (%#v, %v)", result, err)
	}
	if _, err := channel.Send(t.Context(), bus.OutboundMessage{
		ChatID: "missing", Content: "reply",
	}); !errors.Is(err, channels.ErrSendFailed) {
		t.Fatalf("Send(missing token) error = %v", err)
	}
	capabilities := channel.VoiceCapabilities()
	if !capabilities.ASR || !capabilities.TTS {
		t.Fatalf("voice capabilities = %#v", capabilities)
	}

	channel.handleInboundMessage(t.Context(), WeixinMessage{})
	channel.handleInboundMessage(t.Context(), WeixinMessage{FromUserID: "empty"})
	if err := channel.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	close(releasePoll)
	if channel.IsRunning() {
		t.Fatal("channel remains running after Stop")
	}
	select {
	case <-pollCanceled:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for canceled poll")
	}
	// Stop remains idempotent and must tolerate an already-canceled poll loop.
	if err := channel.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}
