//go:build !whatsapp_native

package whatsapp

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/channelstore/whatsappstore"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestWhatsAppNativeStubBoundaries(t *testing.T) {
	channel, err := NewWhatsAppNativeChannel(nil, "name", nil, nil, "channel/whatsapp/name")
	if err == nil || channel != nil {
		t.Fatalf("stub channel = %#v, %v", channel, err)
	}
}

func TestDefaultFactoryResolvesTypedStoreBeforeNativeStub(t *testing.T) {
	home := t.TempDir()
	var called atomic.Bool
	server, err := database.StartServer(context.Background(), database.ServerOptions{
		Home: home,
		Handler: database.HandlerFunc(func(_ context.Context, request database.Request) (any, error) {
			if request.Domain != whatsappstore.Domain || request.Version != whatsappstore.Version ||
				request.Operation != whatsappstore.ResolveOperation {
				t.Fatalf("unexpected request: %#v", request)
			}
			called.Store(true)
			return whatsappstore.ResolveResponse{StoreID: "channel/whatsapp/primary-12345678"}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	previous := database.RuntimeClient()
	database.InstallProcessClient(client)
	t.Cleanup(func() { database.InstallProcessClient(previous) })

	channel := &config.Channel{Enabled: true, Type: config.ChannelWhatsAppNative}
	if decodeErr := channel.Decode(&config.WhatsAppSettings{UseNative: true}); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	cfg := &config.Config{Channels: config.ChannelsConfig{"primary": channel}}
	manager, err := channels.NewManager(cfg, bus.NewMessageBus(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !called.Load() {
		t.Fatal("WhatsApp factory did not resolve its typed store")
	}
	if _, ok := manager.GetChannel("primary"); ok {
		t.Fatal("default build unexpectedly created a native WhatsApp channel")
	}
}
