//go:build whatsapp_native

package whatsapp

import (
	"context"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestWhatsAppStartRequiresBroker(t *testing.T) {
	previous := database.RuntimeClient()
	database.InstallProcessClient(nil)
	t.Cleanup(func() { database.InstallProcessClient(previous) })
	created, err := NewWhatsAppNativeChannel(
		&config.Channel{},
		config.ChannelWhatsAppNative,
		&config.WhatsAppSettings{},
		nil,
		database.StoreID("channel.whatsapp.default"),
	)
	if err != nil {
		t.Fatal(err)
	}
	channel := created.(*WhatsAppNativeChannel)
	if err := channel.Start(t.Context()); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("Start() error = %v", err)
	}
}

func TestHandleIncoming_DoesNotConsumeGenericCommandsLocally(t *testing.T) {
	messageBus := bus.NewMessageBus()
	ch := &WhatsAppNativeChannel{
		BaseChannel: channels.NewBaseChannel("whatsapp_native", config.WhatsAppSettings{}, messageBus, nil),
		runCtx:      context.Background(),
	}

	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Sender: types.NewJID("1001", types.DefaultUserServer),
				Chat:   types.NewJID("1001", types.DefaultUserServer),
			},
			ID:       "mid1",
			PushName: "Alice",
		},
		Message: &waE2E.Message{
			Conversation: proto.String("/new"),
		},
	}

	ch.handleIncoming(evt)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	select {
	case <-ctx.Done():
		t.Fatal("timeout waiting for message to be forwarded")
		return
	case inbound, ok := <-messageBus.InboundChan():
		if !ok {
			t.Fatal("expected inbound message to be forwarded")
		}
		if inbound.Channel != "whatsapp_native" {
			t.Fatalf("channel=%q", inbound.Channel)
		}
		if inbound.Content != "/new" {
			t.Fatalf("content=%q", inbound.Content)
		}
	}
}
