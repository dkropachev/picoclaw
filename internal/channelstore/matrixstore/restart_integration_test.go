package matrixstore_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/sipeed/picoclaw/internal/channelstore/matrixstore"
	"github.com/sipeed/picoclaw/internal/channelstore/matrixstore/sqliteadapter"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	restartMatrixUserID    = id.UserID("@restart-bot:matrix.test")
	restartMatrixDeviceID  = id.DeviceID("RESTART")
	restartMatrixPickleKey = "restart-pickle-key"
)

type restartMatrixBroker struct {
	handler *sqliteadapter.BrokerHandler
	server  *database.Server
}

func TestTypedStorePersistsAcrossBrokerRestart(t *testing.T) {
	home := t.TempDir()
	storeRoot := filepath.Join(home, "matrix-data")
	channel := &config.Channel{Enabled: true, Type: config.ChannelMatrix}
	if err := channel.Decode(&config.MatrixSettings{
		Homeserver:         "https://matrix.test",
		UserID:             restartMatrixUserID.String(),
		DeviceID:           restartMatrixDeviceID.String(),
		CryptoDatabasePath: storeRoot,
		CryptoPassphrase:   restartMatrixPickleKey,
	}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Agents:   config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: home}},
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

	first := startRestartMatrixBroker(t, home, cfg)
	defer func() { _ = closeRestartMatrixBroker(first) }()
	firstClient, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	storeID, err := matrixstore.ResolveStore(t.Context(), firstClient, "restart")
	if err != nil {
		t.Fatal(err)
	}
	firstStore, err := matrixstore.New(
		firstClient, storeID, restartMatrixDeviceID, []byte(restartMatrixPickleKey),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = matrixstore.Preflight(t.Context(), firstClient, storeID); err != nil {
		t.Fatal(err)
	}

	account := crypto.NewOlmAccount()
	if err = firstStore.PutAccount(t.Context(), account); err != nil {
		t.Fatal(err)
	}
	if err = firstStore.SaveNextBatch(t.Context(), restartMatrixUserID, "restart-batch"); err != nil {
		t.Fatal(err)
	}
	if err = firstStore.PutSecret(t.Context(), "restart-secret", "durable-secret"); err != nil {
		t.Fatal(err)
	}
	roomID := id.RoomID("!restart:matrix.test")
	memberID := id.UserID("@alice:matrix.test")
	member := &event.MemberEventContent{
		Membership:  event.MembershipJoin,
		Displayname: "Alice after restart",
	}
	if err = firstStore.SetMember(t.Context(), roomID, memberID, member); err != nil {
		t.Fatal(err)
	}
	if err = firstStore.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}

	oldEpoch := first.server.Manifest().Epoch
	staleClient, err := database.ConnectWithManifest(home, first.server.Manifest())
	if err != nil {
		t.Fatal(err)
	}
	staleStore, err := matrixstore.New(
		staleClient, storeID, restartMatrixDeviceID, []byte(restartMatrixPickleKey),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = closeRestartMatrixBroker(first); err != nil {
		t.Fatalf("close first Matrix broker: %v", err)
	}

	second := startRestartMatrixBroker(t, home, cfg)
	defer func() {
		if closeErr := closeRestartMatrixBroker(second); closeErr != nil {
			t.Errorf("close second Matrix broker: %v", closeErr)
		}
	}()
	if second.server.Manifest().Epoch == oldEpoch {
		t.Fatal("restarted Matrix broker reused its predecessor's epoch")
	}

	secondClient, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	secondStoreID, err := matrixstore.ResolveStore(t.Context(), secondClient, "restart")
	if err != nil {
		t.Fatal(err)
	}
	if secondStoreID != storeID {
		t.Fatalf("store ID changed across restart: %q != %q", secondStoreID, storeID)
	}
	secondStore, err := matrixstore.New(
		secondClient, secondStoreID, restartMatrixDeviceID, []byte(restartMatrixPickleKey),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = matrixstore.Preflight(t.Context(), secondClient, secondStoreID); err != nil {
		t.Fatal(err)
	}

	loadedAccount, err := secondStore.GetAccount(t.Context())
	if err != nil || loadedAccount == nil || loadedAccount.IdentityKey() != account.IdentityKey() {
		t.Fatalf("account after restart = %#v, %v", loadedAccount, err)
	}
	if batch, loadErr := secondStore.LoadNextBatch(
		t.Context(), restartMatrixUserID,
	); loadErr != nil || batch != "restart-batch" {
		t.Fatalf("next batch after restart = %q, %v", batch, loadErr)
	}
	if secret, loadErr := secondStore.GetSecret(
		t.Context(), "restart-secret",
	); loadErr != nil || secret != "durable-secret" {
		t.Fatalf("secret after restart = %q, %v", secret, loadErr)
	}
	loadedMember, err := secondStore.GetMember(t.Context(), roomID, memberID)
	if err != nil || loadedMember == nil || loadedMember.Membership != event.MembershipJoin ||
		loadedMember.Displayname != member.Displayname {
		t.Fatalf("member after restart = %#v, %v", loadedMember, err)
	}

	if _, err = staleStore.LoadNextBatch(
		t.Context(),
		restartMatrixUserID,
	); database.CodeOf(
		err,
	) != database.CodeConflict {
		t.Fatalf("pre-restart Matrix client error = %v, want Conflict", err)
	}
}

func startRestartMatrixBroker(
	t *testing.T,
	home string,
	cfg *config.Config,
) *restartMatrixBroker {
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
	return &restartMatrixBroker{handler: handler, server: server}
}

func closeRestartMatrixBroker(broker *restartMatrixBroker) error {
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
