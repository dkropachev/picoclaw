//go:build whatsapp_native

package whatsappstore_test

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/sipeed/picoclaw/internal/channelstore/whatsappstore"
	"github.com/sipeed/picoclaw/internal/channelstore/whatsappstore/sqliteadapter"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

type restartWhatsAppBroker struct {
	handler *sqliteadapter.BrokerHandler
	server  *database.Server
}

func TestNativeTypedStorePersistsAcrossBrokerRestart(t *testing.T) {
	home := t.TempDir()
	storeRoot := filepath.Join(home, "whatsapp-data")
	channel := &config.Channel{Enabled: true, Type: config.ChannelWhatsAppNative}
	if err := channel.Decode(&config.WhatsAppSettings{
		UseNative: true, SessionStorePath: storeRoot,
	}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
			Workspace: filepath.Join(home, "workspace"),
		}},
		Channels: config.ChannelsConfig{"restart": channel},
	}

	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	if err = sqliteadapter.MigrateDatabase(t.Context(), filepath.Join(storeRoot, "store.db")); err != nil {
		_ = fence.Close()
		t.Fatal(err)
	}
	if err = fence.Close(); err != nil {
		t.Fatal(err)
	}

	first := startRestartWhatsAppBroker(t, home, cfg)
	defer func() { _ = closeRestartWhatsAppBroker(first) }()
	firstClient, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	storeID, err := whatsappstore.ResolveStore(t.Context(), firstClient, "restart")
	if err != nil {
		t.Fatal(err)
	}
	var ready whatsappstore.ReadyResponse
	if err = firstClient.Call(
		t.Context(), whatsappstore.Domain, whatsappstore.Version, whatsappstore.PreflightOperation,
		whatsappstore.Target{StoreID: storeID}, &ready,
	); err != nil || !ready.Ready {
		t.Fatalf("first preflight = %#v, %v", ready, err)
	}
	firstContainer, err := whatsappstore.NewContainer(firstClient, storeID, waLog.Noop)
	if err != nil {
		t.Fatal(err)
	}
	device, err := firstContainer.GetFirstDevice(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	deviceID := types.NewJID("15551234567", types.DefaultUserServer)
	device.ID = &deviceID
	device.LID = types.NewJID("700", types.HiddenUserServer)
	device.Account = &waAdv.ADVSignedDeviceIdentity{
		Details:             []byte("restart-details"),
		AccountSignature:    bytes.Repeat([]byte{1}, 64),
		AccountSignatureKey: bytes.Repeat([]byte{2}, 32),
		DeviceSignature:     bytes.Repeat([]byte{3}, 64),
	}
	device.Platform = "restart-test"
	device.PushName = "PicoClaw Restart"
	if err = device.Save(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !device.Initialized {
		t.Fatal("saved WhatsApp device was not initialized")
	}
	if err = device.Sessions.PutSession(t.Context(), "15550000001:1", []byte("durable-session")); err != nil {
		t.Fatal(err)
	}
	contactID := types.NewJID("15550000002", types.DefaultUserServer)
	if changed, _, putErr := device.Contacts.PutPushName(
		t.Context(), contactID, "Durable Contact",
	); putErr != nil || !changed {
		t.Fatalf("put durable contact = %t, %v", changed, putErr)
	}
	if err = device.SenderKeys.PutSenderKey(
		t.Context(), "restart-group", "restart-sender", []byte("durable-sender-key"),
	); err != nil {
		t.Fatal(err)
	}

	oldEpoch := first.server.Manifest().Epoch
	staleClient, err := database.ConnectWithManifest(home, first.server.Manifest())
	if err != nil {
		t.Fatal(err)
	}
	staleContainer, err := whatsappstore.NewContainer(staleClient, storeID, waLog.Noop)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = staleContainer.Close() }()
	if err = firstContainer.Close(); err != nil {
		t.Fatal(err)
	}
	if err = closeRestartWhatsAppBroker(first); err != nil {
		t.Fatalf("close first WhatsApp broker: %v", err)
	}

	second := startRestartWhatsAppBroker(t, home, cfg)
	defer func() {
		if closeErr := closeRestartWhatsAppBroker(second); closeErr != nil {
			t.Errorf("close second WhatsApp broker: %v", closeErr)
		}
	}()
	if second.server.Manifest().Epoch == oldEpoch {
		t.Fatal("restarted WhatsApp broker reused its predecessor's epoch")
	}

	secondClient, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	secondStoreID, err := whatsappstore.ResolveStore(t.Context(), secondClient, "restart")
	if err != nil {
		t.Fatal(err)
	}
	if secondStoreID != storeID {
		t.Fatalf("store ID changed across restart: %q != %q", secondStoreID, storeID)
	}
	ready = whatsappstore.ReadyResponse{}
	if err = secondClient.Call(
		t.Context(), whatsappstore.Domain, whatsappstore.Version, whatsappstore.PreflightOperation,
		whatsappstore.Target{StoreID: secondStoreID}, &ready,
	); err != nil || !ready.Ready {
		t.Fatalf("second preflight = %#v, %v", ready, err)
	}
	secondContainer, err := whatsappstore.NewContainer(secondClient, secondStoreID, waLog.Noop)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = secondContainer.Close() }()
	loadedDevice, err := secondContainer.GetFirstDevice(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if loadedDevice.ID == nil || *loadedDevice.ID != deviceID || !loadedDevice.Initialized ||
		loadedDevice.PushName != device.PushName || loadedDevice.Platform != device.Platform ||
		loadedDevice.Account == nil ||
		!bytes.Equal(loadedDevice.Account.GetDetails(), device.Account.GetDetails()) {
		t.Fatalf("device after restart = %#v", loadedDevice)
	}
	if session, loadErr := loadedDevice.Sessions.GetSession(t.Context(), "15550000001:1"); loadErr != nil ||
		!bytes.Equal(session, []byte("durable-session")) {
		t.Fatalf("session after restart = %q, %v", session, loadErr)
	}
	if contact, loadErr := loadedDevice.Contacts.GetContact(t.Context(), contactID); loadErr != nil ||
		!contact.Found || contact.PushName != "Durable Contact" {
		t.Fatalf("contact after restart = %#v, %v", contact, loadErr)
	}
	if key, loadErr := loadedDevice.SenderKeys.GetSenderKey(
		t.Context(), "restart-group", "restart-sender",
	); loadErr != nil || !bytes.Equal(key, []byte("durable-sender-key")) {
		t.Fatalf("sender key after restart = %q, %v", key, loadErr)
	}

	if _, err = staleContainer.GetFirstDevice(t.Context()); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("pre-restart WhatsApp client error = %v, want Conflict", err)
	}
}

func startRestartWhatsAppBroker(
	t *testing.T,
	home string,
	cfg *config.Config,
) *restartWhatsAppBroker {
	t.Helper()
	handler, err := sqliteadapter.NewBrokerHandler(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.StartServer(context.Background(), database.ServerOptions{
		Home: home, Handler: handler, CloseHandler: handler.Close,
	})
	if err != nil {
		_ = handler.Close()
		t.Fatal(err)
	}
	return &restartWhatsAppBroker{handler: handler, server: server}
}

func closeRestartWhatsAppBroker(broker *restartWhatsAppBroker) error {
	if broker == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := broker.server.Close(ctx); err != nil {
		return err
	}
	return broker.handler.Close()
}
