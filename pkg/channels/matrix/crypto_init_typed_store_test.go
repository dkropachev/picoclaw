package matrix

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/channelstore/matrixstore"
	"github.com/sipeed/picoclaw/internal/channelstore/matrixstore/sqliteadapter"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestMatrixCryptoInitializationUsesTypedBrokerStore(t *testing.T) {
	const (
		userID   = "@bot:matrix.test"
		deviceID = "DEVICE"
	)
	var callsMu sync.Mutex
	var calls []string
	matrixServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		callsMu.Lock()
		calls = append(calls, request.URL.Path)
		callsMu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(request.URL.Path, "/account/whoami"):
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"user_id": userID, "device_id": deviceID,
			})
		case strings.Contains(request.URL.Path, "/keys/query"):
			_ = json.NewEncoder(writer).Encode(map[string]any{"device_keys": map[string]any{}})
		case strings.Contains(request.URL.Path, "/keys/upload"):
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"one_time_key_counts": map[string]int{"signed_curve25519": 50},
			})
		default:
			http.Error(writer, `{"errcode":"M_UNRECOGNIZED","error":"unexpected"}`, http.StatusNotFound)
		}
	}))
	defer matrixServer.Close()

	home := t.TempDir()
	storeRoot := filepath.Join(home, "matrix-data")
	settings := &config.MatrixSettings{
		Homeserver:         matrixServer.URL,
		UserID:             userID,
		AccessToken:        *config.NewSecureString("token"),
		DeviceID:           deviceID,
		CryptoDatabasePath: storeRoot,
		CryptoPassphrase:   "pickle-key",
	}
	configuredChannel := &config.Channel{Enabled: true, Type: config.ChannelMatrix}
	if err := configuredChannel.Decode(settings); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Agents:   config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: home}},
		Channels: config.ChannelsConfig{"secure": configuredChannel},
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
	handler, err := sqliteadapter.NewBrokerHandler(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	broker, err := database.StartServer(t.Context(), database.ServerOptions{
		Home: home, Handler: handler, CloseHandler: handler.Close,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if closeErr := broker.Close(ctx); closeErr != nil {
			t.Errorf("close database broker: %v", closeErr)
		}
	})
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	previous := database.RuntimeClient()
	database.InstallProcessClient(client)
	t.Cleanup(func() { database.InstallProcessClient(previous) })
	storeID, err := matrixstore.ResolveStore(t.Context(), client, "secure")
	if err != nil {
		t.Fatal(err)
	}
	channel, err := newMatrixChannel(configuredChannel, settings, nil, storeID)
	if err != nil {
		t.Fatal(err)
	}
	if err = channel.initCrypto(t.Context()); err != nil {
		t.Fatal(err)
	}
	if channel.cryptoHelper == nil || channel.client.Crypto != channel.cryptoHelper ||
		channel.client.StateStore == nil || channel.client.Store == nil {
		t.Fatal("Matrix typed crypto stores were not installed")
	}
	callsMu.Lock()
	defer callsMu.Unlock()
	if len(calls) < 3 || !strings.Contains(calls[0], "/account/whoami") {
		t.Fatalf("Matrix API call order = %v", calls)
	}
	if err := channel.cryptoHelper.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMatrixCryptoInitializationRejectsWhoamiMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"user_id":"@other:matrix.test","device_id":"OTHER"}`))
	}))
	defer server.Close()
	previous := database.RuntimeClient()
	database.InstallProcessClient(&database.Client{})
	t.Cleanup(func() { database.InstallProcessClient(previous) })
	settings := &config.MatrixSettings{
		Homeserver: server.URL, UserID: "@bot:matrix.test",
		AccessToken: *config.NewSecureString("token"), CryptoPassphrase: "pickle-key",
	}
	channel, err := newMatrixChannel(
		&config.Channel{}, settings, nil, "channel/matrix/secure",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = channel.initCrypto(t.Context()); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("mismatched whoami error = %v", err)
	}
}

func TestMatrixCryptoInitializationRetainsConfiguredDeviceWhenWhoamiOmitsIt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"user_id":"@bot:matrix.test"}`))
	}))
	defer server.Close()
	previous := database.RuntimeClient()
	database.InstallProcessClient(&database.Client{})
	t.Cleanup(func() { database.InstallProcessClient(previous) })
	settings := &config.MatrixSettings{
		Homeserver: server.URL, UserID: "@bot:matrix.test", DeviceID: "CONFIGURED",
		AccessToken: *config.NewSecureString("token"), CryptoPassphrase: "pickle-key",
	}
	channel, err := newMatrixChannel(
		&config.Channel{}, settings, nil, "channel/matrix/secure",
	)
	if err != nil {
		t.Fatal(err)
	}
	err = channel.initCrypto(t.Context())
	if database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("configured device init error = %v, want downstream broker unavailable", err)
	}
	if channel.client.DeviceID != "CONFIGURED" {
		t.Fatalf("Matrix device ID = %q, want configured identity", channel.client.DeviceID)
	}
}

func TestMatrixCryptoInitializationRejectsConfiguredDeviceMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"user_id":"@bot:matrix.test","device_id":"OTHER"}`))
	}))
	defer server.Close()
	previous := database.RuntimeClient()
	database.InstallProcessClient(&database.Client{})
	t.Cleanup(func() { database.InstallProcessClient(previous) })
	settings := &config.MatrixSettings{
		Homeserver: server.URL, UserID: "@bot:matrix.test", DeviceID: "CONFIGURED",
		AccessToken: *config.NewSecureString("token"), CryptoPassphrase: "pickle-key",
	}
	channel, err := newMatrixChannel(
		&config.Channel{}, settings, nil, "channel/matrix/secure",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = channel.initCrypto(t.Context()); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("mismatched configured device error = %v", err)
	}
}
