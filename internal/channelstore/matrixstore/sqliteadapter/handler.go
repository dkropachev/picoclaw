// Package sqliteadapter owns the broker-side Matrix database adapter. SQL and
// provider dependencies are intentionally confined to this package.
package sqliteadapter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/sqlstatestore"

	"github.com/sipeed/picoclaw/internal/channelstore/matrixstore"
	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

const busyTimeout = 5 * time.Second

type targetConfig struct {
	path      string
	pickleKey []byte
	deviceID  id.DeviceID
}

type storeBundle struct {
	database  *dbutil.Database
	state     *sqlstatestore.SQLStateStore
	pickleKey []byte
	deviceID  id.DeviceID
	storeID   database.StoreID
	pages     *groupSessionPages
}

func (bundle *storeBundle) cryptoStore() *crypto.SQLCryptoStore {
	return crypto.NewSQLCryptoStore(
		bundle.database,
		dbutil.ZeroLogger(zerolog.Nop()),
		"",
		bundle.deviceID,
		bundle.pickleKey,
	)
}

// BrokerHandler owns all online Matrix provider state and retained pools.
type BrokerHandler struct {
	mu      sync.Mutex
	targets map[database.StoreID]targetConfig
	byName  map[string]database.StoreID
	stores  map[database.StoreID]*storeBundle
	pages   *groupSessionPages
	closed  bool
}

// NewBrokerHandler builds the trusted Matrix catalog without opening a store.
func NewBrokerHandler(home string, cfg *config.Config) (*BrokerHandler, error) {
	if !database.BrokerAuthorityHeld() && !database.ProviderTestAuthorityHeld() {
		return nil, database.NewError(
			database.CodeUnauthorized,
			"Matrix broker handler requires database broker authority",
		)
	}
	catalog, err := storecatalog.Build(home, cfg)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		cfg = &config.Config{}
	}
	handler := &BrokerHandler{
		targets: make(map[database.StoreID]targetConfig),
		byName:  make(map[string]database.StoreID),
		stores:  make(map[database.StoreID]*storeBundle),
		pages:   newGroupSessionPages(defaultGroupSessionSnapshotTTL),
	}
	for name, channel := range cfg.Channels {
		if channel == nil || !channel.Enabled || channel.Type != config.ChannelMatrix {
			continue
		}
		decoded, decodeErr := channel.GetDecoded()
		if decodeErr != nil {
			return nil, fmt.Errorf("decode Matrix channel %q: %w", name, decodeErr)
		}
		settings, ok := decoded.(*config.MatrixSettings)
		if !ok || settings == nil {
			return nil, database.NewError(database.CodeInvalid, "Matrix channel settings are invalid")
		}
		logicalID, ok := storecatalog.ChannelStoreID(config.ChannelMatrix, name)
		if !ok {
			return nil, database.NewError(database.CodeIntegrity, "Matrix store identity is invalid")
		}
		storeID, parseErr := database.ParseStoreID(logicalID)
		if parseErr != nil {
			return nil, database.NewError(database.CodeIntegrity, "Matrix store identity is invalid")
		}
		spec, found := catalog.Lookup(logicalID)
		if !found || spec.Domain != matrixstore.Domain {
			return nil, database.NewError(database.CodeIntegrity, "Matrix store is not broker-cataloged")
		}
		handler.byName[name] = storeID
		handler.targets[storeID] = targetConfig{
			path:      spec.Path,
			pickleKey: []byte(settings.CryptoPassphrase),
			deviceID:  id.DeviceID(settings.DeviceID),
		}
	}
	return handler, nil
}

func (handler *BrokerHandler) Handle(ctx context.Context, request database.Request) (any, error) {
	if handler == nil || request.Domain != matrixstore.Domain || request.Version != matrixstore.Version {
		return nil, database.NewError(database.CodeUnsupported, "Matrix database domain is unsupported")
	}
	switch request.Operation {
	case matrixstore.ResolveOperation:
		var input matrixstore.ResolveRequest
		if request.DecodePayload(&input) != nil || input.ChannelName == "" ||
			input.ChannelName != strings.TrimSpace(input.ChannelName) {
			return nil, database.NewError(database.CodeInvalid, "Matrix channel identity is invalid")
		}
		handler.mu.Lock()
		storeID, ok := handler.byName[input.ChannelName]
		closed := handler.closed
		handler.mu.Unlock()
		if closed {
			return nil, database.NewError(database.CodeUnavailable, "Matrix broker handler is closed")
		}
		if !ok {
			return nil, database.NewError(database.CodeUnauthorized, "Matrix channel is not broker-cataloged")
		}
		return matrixstore.ResolveResponse{StoreID: storeID}, nil
	case matrixstore.PreflightOperation:
		var input matrixstore.StoreTarget
		if request.DecodePayload(&input) != nil || !input.StoreID.Valid() {
			return nil, database.NewError(database.CodeInvalid, "Matrix preflight target is invalid")
		}
		bundle, err := handler.bundle(ctx, input.StoreID, "")
		if err != nil {
			return nil, err
		}
		if _, err = bundle.state.HasFetchedMembers(ctx, id.RoomID("!preflight:local")); err != nil {
			return nil, backendError(err)
		}
		if _, err = bundle.cryptoStore().GetAccount(ctx); err != nil {
			return nil, backendError(err)
		}
		return matrixstore.PreflightResponse{Ready: true}, nil
	}

	var input matrixstore.Request
	if request.DecodePayload(&input) != nil || !input.StoreID.Valid() || input.DeviceID == "" {
		return nil, database.NewError(database.CodeInvalid, "Matrix store request is invalid")
	}
	bundle, err := handler.bundle(ctx, input.StoreID, input.DeviceID)
	if err != nil {
		return nil, err
	}
	return dispatch(ctx, request.Operation, input, bundle)
}

func (handler *BrokerHandler) bundle(
	ctx context.Context,
	storeID database.StoreID,
	deviceID id.DeviceID,
) (*storeBundle, error) {
	if !database.BrokerAuthorityHeld() && !database.ProviderTestAuthorityHeld() {
		return nil, database.NewError(database.CodeUnauthorized, "Matrix provider authority is unavailable")
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.closed {
		return nil, database.NewError(database.CodeUnavailable, "Matrix broker handler is closed")
	}
	target, ok := handler.targets[storeID]
	if !ok {
		return nil, database.NewError(database.CodeUnauthorized, "Matrix store is not broker-cataloged")
	}
	if len(target.pickleKey) == 0 {
		return nil, database.NewError(database.CodeUnavailable, "Matrix crypto storage is not configured")
	}
	if existing := handler.stores[storeID]; existing != nil {
		if err := bindDevice(ctx, existing, deviceID); err != nil {
			return nil, err
		}
		return existing, nil
	}
	db, err := sqliteprovider.OpenStore(target.path, busyTimeout)
	if err != nil {
		return nil, database.NewError(database.CodeUnavailable, "Matrix store is unavailable")
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	if err = sqliteprovider.Configure(ctx, db, busyTimeout, false); err != nil {
		_ = db.Close()
		return nil, database.NewError(database.CodeUnavailable, "Matrix store is unavailable")
	}
	wrapped, err := dbutil.NewWithDB(db, sqliteprovider.DriverName())
	if err != nil {
		_ = db.Close()
		return nil, database.NewError(database.CodeUnavailable, "Matrix store is unavailable")
	}
	log := dbutil.ZeroLogger(zerolog.Nop())
	initialDevice := target.deviceID
	if deviceID != "" {
		if initialDevice != "" && initialDevice != deviceID {
			_ = wrapped.Close()
			return nil, database.NewError(database.CodeConflict, "Matrix configured device does not match whoami")
		}
		initialDevice = deviceID
	}
	bundle := &storeBundle{
		database:  wrapped,
		state:     sqlstatestore.NewSQLStateStore(wrapped, log, false),
		pickleKey: append([]byte(nil), target.pickleKey...),
		deviceID:  initialDevice,
		storeID:   storeID,
		pages:     handler.pages,
	}
	if err = bindDevice(ctx, bundle, deviceID); err != nil {
		_ = wrapped.Close()
		return nil, err
	}
	handler.stores[storeID] = bundle
	return bundle, nil
}

func bindDevice(ctx context.Context, bundle *storeBundle, deviceID id.DeviceID) error {
	if deviceID == "" {
		return nil
	}
	if bundle.deviceID != "" && bundle.deviceID != deviceID {
		return database.NewError(database.CodeConflict, "Matrix store device identity changed")
	}
	store := bundle.cryptoStore()
	storedDeviceID, err := store.FindDeviceID(ctx)
	if err != nil {
		return backendError(err)
	}
	if storedDeviceID != "" && storedDeviceID != deviceID {
		return database.NewError(database.CodeConflict, "Matrix stored device identity does not match whoami")
	}
	bundle.deviceID = deviceID
	return nil
}

func (handler *BrokerHandler) Close() error {
	if handler == nil {
		return nil
	}
	handler.mu.Lock()
	if handler.closed {
		handler.mu.Unlock()
		return nil
	}
	handler.closed = true
	stores := handler.stores
	handler.stores = nil
	pages := handler.pages
	handler.mu.Unlock()
	pages.close()
	var result error
	for _, bundle := range stores {
		result = errors.Join(result, bundle.database.Close())
	}
	return result
}

func backendError(err error) error {
	if err == nil {
		return nil
	}
	if database.CodeOf(err) != database.CodeInternal {
		return err
	}
	return database.NewError(database.CodeUnavailable, "Matrix store operation failed")
}

var _ database.Handler = (*BrokerHandler)(nil)
