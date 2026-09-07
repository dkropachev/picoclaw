//go:build whatsapp_native

package sqliteadapter

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/sipeed/picoclaw/internal/channelstore/whatsappstore"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestDecryptionLeaseRejectsInvalidUseWithoutUnlocking(t *testing.T) {
	fixture := openLeaseBroker(t, time.Second)
	lease := fixture.begin(t)
	wrongLease := whatsappstore.DecryptionLeaseID(strings.Repeat("0", 64))
	if wrongLease == lease {
		wrongLease = whatsappstore.DecryptionLeaseID(strings.Repeat("1", 64))
	}

	var read whatsappstore.InvokeResponse
	err := fixture.client.Call(
		t.Context(), whatsappstore.Domain, whatsappstore.Version, whatsappstore.InvokeOperation,
		whatsappstore.InvokeRequest{
			StoreID: fixture.storeID, DeviceJID: fixture.deviceJID,
			Method: whatsappstore.MethodGetSession, Address: "invalid:1",
			DecryptionLease: wrongLease,
		},
		&read,
	)
	if database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("invalid lease read error = %v", err)
	}

	err = fixture.client.CallWithOptions(
		t.Context(), whatsappstore.Domain, whatsappstore.Version, whatsappstore.InvokeOperation,
		whatsappstore.InvokeRequest{
			StoreID: fixture.storeID, DeviceJID: fixture.deviceJID,
			Method: whatsappstore.MethodPutSession, Address: "invalid:1", Data: []byte("bad"),
			DecryptionLease: lease,
		},
		&read, database.CallOptions{Mutation: true},
	)
	if database.CodeOf(err) != database.CodeUnsupported {
		t.Fatalf("leased mutation error = %v", err)
	}

	blockedContext, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	err = fixture.client.Call(
		blockedContext, whatsappstore.Domain, whatsappstore.Version, whatsappstore.InvokeOperation,
		whatsappstore.InvokeRequest{
			StoreID: fixture.storeID, DeviceJID: fixture.deviceJID,
			Method: whatsappstore.MethodGetSession, Address: "invalid:1",
		},
		&read,
	)
	if database.CodeOf(err) != database.CodeDeadline {
		t.Fatalf("ordinary read was not held by valid lease: %v", err)
	}

	var aborted whatsappstore.AbortDecryptionResponse
	err = fixture.client.CallWithOptions(
		t.Context(), whatsappstore.Domain, whatsappstore.Version, whatsappstore.AbortDecryptionOperation,
		whatsappstore.AbortDecryptionRequest{
			StoreID: fixture.storeID, DeviceJID: fixture.deviceJID, DecryptionLease: lease,
		},
		&aborted, database.CallOptions{Mutation: true},
	)
	if err != nil || !aborted.Released {
		t.Fatalf("abort = %#v, %v", aborted, err)
	}
	if err := fixture.client.Call(
		t.Context(), whatsappstore.Domain, whatsappstore.Version, whatsappstore.InvokeOperation,
		whatsappstore.InvokeRequest{
			StoreID: fixture.storeID, DeviceJID: fixture.deviceJID,
			Method: whatsappstore.MethodGetSession, Address: "invalid:1",
		},
		&read,
	); err != nil {
		t.Fatalf("ordinary read after abort = %v", err)
	}
}

func TestDecryptionLeaseExpiryReleasesSerializationGate(t *testing.T) {
	const ttl = 80 * time.Millisecond
	fixture := openLeaseBroker(t, ttl)
	lease := fixture.begin(t)

	started := time.Now()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	var read whatsappstore.InvokeResponse
	err := fixture.client.Call(
		ctx, whatsappstore.Domain, whatsappstore.Version, whatsappstore.InvokeOperation,
		whatsappstore.InvokeRequest{
			StoreID: fixture.storeID, DeviceJID: fixture.deviceJID,
			Method: whatsappstore.MethodGetSession, Address: "expiry:1",
		},
		&read,
	)
	if err != nil {
		t.Fatalf("ordinary read after expiry = %v", err)
	}
	if elapsed := time.Since(started); elapsed < ttl/2 {
		t.Fatalf("ordinary read bypassed unexpired lease after %s", elapsed)
	}

	err = fixture.client.Call(
		t.Context(), whatsappstore.Domain, whatsappstore.Version, whatsappstore.InvokeOperation,
		whatsappstore.InvokeRequest{
			StoreID: fixture.storeID, DeviceJID: fixture.deviceJID,
			Method: whatsappstore.MethodGetSession, Address: "expiry:1",
			DecryptionLease: lease,
		},
		&read,
	)
	if database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("expired lease read error = %v", err)
	}
	var committed whatsappstore.MutationResponse
	err = fixture.client.CallWithOptions(
		t.Context(), whatsappstore.Domain, whatsappstore.Version, whatsappstore.DecryptionOperation,
		whatsappstore.DecryptionRequest{
			StoreID: fixture.storeID, DeviceJID: fixture.deviceJID, DecryptionLease: lease,
		},
		&committed, database.CallOptions{Mutation: true},
	)
	if database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("expired lease commit error = %v", err)
	}
}

func TestBrokerCloseRevokesOutstandingDecryptionLease(t *testing.T) {
	fixture := openLeaseBroker(t, 100*time.Millisecond)
	_ = fixture.begin(t)
	fixture.handler.mu.Lock()
	retained := fixture.handler.containers[fixture.storeID]
	fixture.handler.mu.Unlock()
	if retained == nil {
		t.Fatal("retained container is missing")
	}

	closed := make(chan error, 1)
	go func() { closed <- fixture.handler.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("broker close waited for outstanding lease TTL")
	}
	// Let the original timer deadline pass to exercise the timer/Close race.
	time.Sleep(150 * time.Millisecond)
	retained.leaseMu.Lock()
	lease, isClosed := retained.lease, retained.closed
	retained.leaseMu.Unlock()
	if lease != nil || !isClosed || len(retained.serialization) != 1 {
		t.Fatalf("retained close state = lease %#v, closed %t, gate %d", lease, isClosed, len(retained.serialization))
	}
}

type leaseBrokerFixture struct {
	client    *database.Client
	handler   *BrokerHandler
	storeID   database.StoreID
	deviceJID whatsappstore.JID
}

func (fixture leaseBrokerFixture) begin(t *testing.T) whatsappstore.DecryptionLeaseID {
	t.Helper()
	var response whatsappstore.BeginDecryptionResponse
	err := fixture.client.CallWithOptions(
		t.Context(), whatsappstore.Domain, whatsappstore.Version, whatsappstore.BeginDecryptionOperation,
		whatsappstore.BeginDecryptionRequest{
			StoreID: fixture.storeID, DeviceJID: fixture.deviceJID,
		},
		&response, database.CallOptions{Mutation: true},
	)
	if err != nil || !response.DecryptionLease.Valid() {
		t.Fatalf("begin decryption = %#v, %v", response, err)
	}
	return response.DecryptionLease
}

func openLeaseBroker(t *testing.T, ttl time.Duration) leaseBrokerFixture {
	t.Helper()
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	storeRoot := filepath.Join(home, "whatsapp")
	channel := &config.Channel{Enabled: true, Type: config.ChannelWhatsAppNative}
	if err := channel.Decode(&config.WhatsAppSettings{
		UseNative: true, SessionStorePath: storeRoot,
	}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Agents:   config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: workspace}},
		Channels: config.ChannelsConfig{"primary": channel},
	}
	fence, acquireErr := database.AcquireMigrationFence(home)
	if acquireErr != nil {
		t.Fatal(acquireErr)
	}
	if err := MigrateDatabase(t.Context(), filepath.Join(storeRoot, "store.db")); err != nil {
		_ = fence.Close()
		t.Fatal(err)
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
	handler, handlerErr := NewBrokerHandler(home, cfg)
	if handlerErr != nil {
		t.Fatal(handlerErr)
	}
	handler.decryptionLeaseTTL = ttl
	server, serverErr := database.StartServer(context.Background(), database.ServerOptions{
		Home: home, Handler: handler, CloseHandler: handler.Close,
	})
	if serverErr != nil {
		_ = handler.Close()
		t.Fatal(serverErr)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	client, connectErr := database.Connect(home)
	if connectErr != nil {
		t.Fatal(connectErr)
	}
	storeID, resolveErr := whatsappstore.ResolveStore(t.Context(), client, "primary")
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	var ready whatsappstore.ReadyResponse
	if err := client.Call(
		t.Context(), whatsappstore.Domain, whatsappstore.Version, whatsappstore.PreflightOperation,
		whatsappstore.Target{StoreID: storeID}, &ready,
	); err != nil || !ready.Ready {
		t.Fatalf("preflight = %#v, %v", ready, err)
	}
	return leaseBrokerFixture{
		client: client, handler: handler, storeID: storeID,
		deviceJID: whatsappstore.JIDFromValue(types.NewJID("15551234567", types.DefaultUserServer)),
	}
}
