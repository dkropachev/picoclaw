//go:build whatsapp_native

package sqliteadapter

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/util/keys"
	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/sipeed/picoclaw/internal/channelstore/whatsappstore"
	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	busyTimeout               = 5 * time.Second
	defaultDecryptionLeaseTTL = 30 * time.Second
)

type BrokerHandler struct {
	catalog *storecatalog.Catalog

	mu         sync.Mutex
	containers map[database.StoreID]*retainedContainer
	closed     bool
	closeOnce  sync.Once
	closeErr   error

	decryptionLeaseTTL time.Duration
}

type retainedContainer struct {
	container *sqlstore.Container

	mu      sync.Mutex
	devices map[string]*sqlstore.SQLStore

	serialization chan struct{}
	leaseMu       sync.Mutex
	lease         *decryptionLease
	closed        bool
}

type decryptionLease struct {
	id          whatsappstore.DecryptionLeaseID
	deviceJID   whatsappstore.JID
	deviceStore *sqlstore.SQLStore
	expiresAt   time.Time
	timer       *time.Timer
	operationMu sync.Mutex
	active      bool
}

func (retained *retainedContainer) device(jid whatsappstore.JID) *sqlstore.SQLStore {
	key := jid.Value().String()
	retained.mu.Lock()
	defer retained.mu.Unlock()
	if existing := retained.devices[key]; existing != nil {
		return existing
	}
	created := sqlstore.NewSQLStore(retained.container, jid.Value())
	retained.devices[key] = created
	return created
}

func (retained *retainedContainer) acquire(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-retained.serialization:
		if err := ctx.Err(); err != nil {
			retained.release()
			return err
		}
		return nil
	}
}

func (retained *retainedContainer) release() {
	retained.serialization <- struct{}{}
}

func (retained *retainedContainer) invoke(
	ctx context.Context,
	request whatsappstore.InvokeRequest,
) (whatsappstore.InvokeResponse, error) {
	if request.DecryptionLease != "" {
		if !isDecryptionLeaseRead(request.Method) || !validDeviceJID(request.DeviceJID) {
			return whatsappstore.InvokeResponse{}, database.NewError(
				database.CodeUnsupported,
				"WhatsApp decryption leases authorize typed reads only",
			)
		}
		return retained.invokeWithLease(ctx, request)
	}
	if err := retained.acquire(ctx); err != nil {
		return whatsappstore.InvokeResponse{}, err
	}
	defer retained.release()
	retained.leaseMu.Lock()
	closed := retained.closed
	retained.leaseMu.Unlock()
	if closed {
		return whatsappstore.InvokeResponse{}, database.NewError(
			database.CodeUnavailable, "WhatsApp retained store is closed",
		)
	}
	return handleInvoke(ctx, retained, request)
}

func (retained *retainedContainer) beginDecryption(
	ctx context.Context,
	deviceJID whatsappstore.JID,
	ttl time.Duration,
) (whatsappstore.BeginDecryptionResponse, error) {
	leaseID, err := newDecryptionLeaseID()
	if err != nil {
		return whatsappstore.BeginDecryptionResponse{}, database.NewError(
			database.CodeInternal, "WhatsApp decryption lease generation failed",
		)
	}
	if ttl <= 0 || ttl > defaultDecryptionLeaseTTL {
		ttl = defaultDecryptionLeaseTTL
	}
	if err := retained.acquire(ctx); err != nil {
		return whatsappstore.BeginDecryptionResponse{}, err
	}
	expiresAt := time.Now().Add(ttl)
	lease := &decryptionLease{
		id: leaseID, deviceJID: deviceJID, deviceStore: retained.device(deviceJID),
		expiresAt: expiresAt, active: true,
	}
	retained.leaseMu.Lock()
	if retained.closed {
		retained.leaseMu.Unlock()
		retained.release()
		return whatsappstore.BeginDecryptionResponse{}, database.NewError(
			database.CodeUnavailable, "WhatsApp retained store is closed",
		)
	}
	if retained.lease != nil {
		retained.leaseMu.Unlock()
		retained.release()
		return whatsappstore.BeginDecryptionResponse{}, database.NewError(
			database.CodeInternal, "WhatsApp decryption serialization state is invalid",
		)
	}
	retained.lease = lease
	lease.timer = time.AfterFunc(ttl, func() { retained.expireDecryption(lease) })
	retained.leaseMu.Unlock()
	return whatsappstore.BeginDecryptionResponse{
		DecryptionLease: leaseID, ExpiresAt: whatsappstore.TimeFromValue(expiresAt),
	}, nil
}

func (retained *retainedContainer) invokeWithLease(
	ctx context.Context,
	request whatsappstore.InvokeRequest,
) (whatsappstore.InvokeResponse, error) {
	lease, err := retained.lookupDecryptionLease(request.DeviceJID, request.DecryptionLease)
	if err != nil {
		return whatsappstore.InvokeResponse{}, err
	}
	lease.operationMu.Lock()
	defer lease.operationMu.Unlock()

	retained.leaseMu.Lock()
	if err := retained.validateDecryptionLeaseLocked(
		lease, request.DeviceJID, request.DecryptionLease,
	); err != nil {
		retained.leaseMu.Unlock()
		return whatsappstore.InvokeResponse{}, err
	}
	retained.leaseMu.Unlock()
	return handleInvoke(ctx, retained, request)
}

func (retained *retainedContainer) commitDecryption(
	ctx context.Context,
	request whatsappstore.DecryptionRequest,
) (err error) {
	lease, err := retained.lookupDecryptionLease(request.DeviceJID, request.DecryptionLease)
	if err != nil {
		return err
	}
	lease.operationMu.Lock()
	defer lease.operationMu.Unlock()

	retained.leaseMu.Lock()
	if err = retained.validateDecryptionLeaseLocked(
		lease, request.DeviceJID, request.DecryptionLease,
	); err != nil {
		retained.leaseMu.Unlock()
		return err
	}
	lease.active = false
	lease.timer.Stop()
	retained.leaseMu.Unlock()
	defer retained.finishDecryption(lease)

	return lease.deviceStore.DoDecryptionTxn(ctx, func(txnContext context.Context) error {
		for _, mutation := range request.Mutations {
			if applyErr := applyDecryptionMutation(txnContext, lease.deviceStore, mutation); applyErr != nil {
				return applyErr
			}
		}
		return nil
	})
}

func (retained *retainedContainer) abortDecryption(
	deviceJID whatsappstore.JID,
	leaseID whatsappstore.DecryptionLeaseID,
) error {
	lease, err := retained.lookupDecryptionLease(deviceJID, leaseID)
	if err != nil {
		return err
	}
	lease.operationMu.Lock()
	defer lease.operationMu.Unlock()

	retained.leaseMu.Lock()
	defer retained.leaseMu.Unlock()
	if err := retained.validateDecryptionLeaseLocked(lease, deviceJID, leaseID); err != nil {
		return err
	}
	retained.finishDecryptionLocked(lease)
	return nil
}

func (retained *retainedContainer) lookupDecryptionLease(
	deviceJID whatsappstore.JID,
	leaseID whatsappstore.DecryptionLeaseID,
) (*decryptionLease, error) {
	retained.leaseMu.Lock()
	defer retained.leaseMu.Unlock()
	if retained.closed {
		return nil, database.NewError(database.CodeUnavailable, "WhatsApp retained store is closed")
	}
	lease := retained.lease
	if lease == nil || !lease.active || lease.id != leaseID || lease.deviceJID != deviceJID {
		return nil, inactiveDecryptionLease()
	}
	return lease, nil
}

// validateDecryptionLeaseLocked runs after operationMu is acquired. Rechecking
// here prevents an invocation that raced the expiry timer from using a lease
// after the timer released the retained-store serialization gate.
func (retained *retainedContainer) validateDecryptionLeaseLocked(
	lease *decryptionLease,
	deviceJID whatsappstore.JID,
	leaseID whatsappstore.DecryptionLeaseID,
) error {
	if retained.lease != lease || !lease.active || lease.id != leaseID || lease.deviceJID != deviceJID {
		return inactiveDecryptionLease()
	}
	if !time.Now().Before(lease.expiresAt) {
		retained.finishDecryptionLocked(lease)
		return inactiveDecryptionLease()
	}
	return nil
}

func (retained *retainedContainer) expireDecryption(lease *decryptionLease) {
	lease.operationMu.Lock()
	defer lease.operationMu.Unlock()
	retained.leaseMu.Lock()
	defer retained.leaseMu.Unlock()
	if retained.lease != lease || !lease.active {
		return
	}
	if remaining := time.Until(lease.expiresAt); remaining > 0 {
		lease.timer.Reset(remaining)
		return
	}
	retained.finishDecryptionLocked(lease)
}

func (retained *retainedContainer) finishDecryption(lease *decryptionLease) {
	retained.leaseMu.Lock()
	defer retained.leaseMu.Unlock()
	retained.finishDecryptionLocked(lease)
}

func (retained *retainedContainer) finishDecryptionLocked(lease *decryptionLease) {
	if retained.lease != lease {
		return
	}
	lease.active = false
	if lease.timer != nil {
		lease.timer.Stop()
	}
	retained.lease = nil
	retained.release()
}

func (retained *retainedContainer) close() error {
	retained.leaseMu.Lock()
	retained.closed = true
	lease := retained.lease
	retained.leaseMu.Unlock()

	if lease != nil {
		lease.operationMu.Lock()
		retained.leaseMu.Lock()
		retained.finishDecryptionLocked(lease)
		retained.leaseMu.Unlock()
		lease.operationMu.Unlock()
	}
	// Waiting for the token also waits for an ordinary invocation that was
	// already executing when shutdown marked the retained store closed.
	<-retained.serialization
	err := retained.container.Close()
	retained.release()
	return err
}

func inactiveDecryptionLease() error {
	return database.NewError(database.CodeConflict, "WhatsApp decryption lease is not active")
}

func newDecryptionLeaseID() (whatsappstore.DecryptionLeaseID, error) {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return whatsappstore.DecryptionLeaseID(hex.EncodeToString(token[:])), nil
}

// NewBrokerHandler creates the broker-owned WhatsApp domain adapter. No store
// is opened until preflight or the first typed operation.
func NewBrokerHandler(home string, cfg *config.Config) (*BrokerHandler, error) {
	if !database.BrokerAuthorityHeld() && !database.ProviderTestAuthorityHeld() {
		return nil, database.NewError(
			database.CodeUnauthorized,
			"WhatsApp broker handler requires database broker authority",
		)
	}
	catalog, err := storecatalog.Build(home, cfg)
	if err != nil {
		return nil, err
	}
	return &BrokerHandler{
		catalog: catalog, containers: make(map[database.StoreID]*retainedContainer),
		decryptionLeaseTTL: defaultDecryptionLeaseTTL,
	}, nil
}

func (handler *BrokerHandler) Handle(ctx context.Context, request database.Request) (any, error) {
	if handler == nil || request.Domain != whatsappstore.Domain || request.Version != whatsappstore.Version {
		return nil, database.NewError(database.CodeUnsupported, "database domain is unsupported")
	}
	switch request.Operation {
	case whatsappstore.ResolveOperation:
		var input whatsappstore.ResolveRequest
		if request.DecodePayload(&input) != nil || strings.TrimSpace(input.ChannelName) == "" ||
			input.ChannelName != strings.TrimSpace(input.ChannelName) || strings.ContainsRune(input.ChannelName, 0) {
			return nil, invalidRequest()
		}
		logicalID, ok := storecatalog.ChannelStoreID(config.ChannelWhatsAppNative, input.ChannelName)
		if !ok {
			return nil, database.NewError(database.CodeIntegrity, "WhatsApp store identity could not be resolved")
		}
		spec, ok := handler.catalog.Lookup(logicalID)
		if !ok || spec.Domain != whatsappstore.Domain {
			return nil, database.NewError(database.CodeUnauthorized, "WhatsApp store is not broker-cataloged")
		}
		storeID, err := database.ParseStoreID(logicalID)
		if err != nil {
			return nil, database.NewError(database.CodeIntegrity, "WhatsApp store identity is invalid")
		}
		return whatsappstore.ResolveResponse{StoreID: storeID}, nil

	case whatsappstore.PreflightOperation:
		var input whatsappstore.Target
		if request.DecodePayload(&input) != nil || !validStoreID(input.StoreID) {
			return nil, invalidRequest()
		}
		if _, err := handler.open(ctx, input.StoreID); err != nil {
			return nil, err
		}
		return whatsappstore.ReadyResponse{Ready: true}, nil

	case whatsappstore.InvokeOperation:
		var input whatsappstore.InvokeRequest
		if request.DecodePayload(&input) != nil || !validStoreID(input.StoreID) || input.Method == "" {
			return nil, invalidRequest()
		}
		if input.DecryptionLease != "" && !input.DecryptionLease.Valid() {
			return nil, invalidRequest()
		}
		container, err := handler.open(ctx, input.StoreID)
		if err != nil {
			return nil, err
		}
		response, err := container.invoke(ctx, input)
		if err != nil {
			return nil, mapOperationError(err)
		}
		return response, nil

	case whatsappstore.BeginDecryptionOperation:
		var input whatsappstore.BeginDecryptionRequest
		if request.DecodePayload(&input) != nil || !validStoreID(input.StoreID) ||
			!validDeviceJID(input.DeviceJID) {
			return nil, invalidRequest()
		}
		container, err := handler.open(ctx, input.StoreID)
		if err != nil {
			return nil, err
		}
		response, err := container.beginDecryption(ctx, input.DeviceJID, handler.decryptionLeaseTTL)
		if err != nil {
			return nil, mapOperationError(err)
		}
		return response, nil

	case whatsappstore.DecryptionOperation:
		var input whatsappstore.DecryptionRequest
		if request.DecodePayload(&input) != nil || !validStoreID(input.StoreID) ||
			!validDeviceJID(input.DeviceJID) || !input.DecryptionLease.Valid() ||
			len(input.Mutations) > 1024 {
			return nil, invalidRequest()
		}
		for _, mutation := range input.Mutations {
			if err := validateDecryptionMutation(mutation); err != nil {
				return nil, invalidRequest()
			}
		}
		container, err := handler.open(ctx, input.StoreID)
		if err != nil {
			return nil, err
		}
		err = container.commitDecryption(ctx, input)
		if err != nil {
			return nil, mapOperationError(err)
		}
		return whatsappstore.MutationResponse{Applied: true}, nil

	case whatsappstore.AbortDecryptionOperation:
		var input whatsappstore.AbortDecryptionRequest
		if request.DecodePayload(&input) != nil || !validStoreID(input.StoreID) ||
			!validDeviceJID(input.DeviceJID) || !input.DecryptionLease.Valid() {
			return nil, invalidRequest()
		}
		container, err := handler.open(ctx, input.StoreID)
		if err != nil {
			return nil, err
		}
		if err := container.abortDecryption(input.DeviceJID, input.DecryptionLease); err != nil {
			return nil, mapOperationError(err)
		}
		return whatsappstore.AbortDecryptionResponse{Released: true}, nil
	default:
		return nil, database.NewError(database.CodeUnsupported, "WhatsApp broker operation is unsupported")
	}
}

func (handler *BrokerHandler) open(
	ctx context.Context,
	storeID database.StoreID,
) (*retainedContainer, error) {
	if handler == nil || handler.catalog == nil {
		return nil, database.NewError(database.CodeUnavailable, "WhatsApp broker handler is unavailable")
	}
	if !database.BrokerAuthorityHeld() && !database.ProviderTestAuthorityHeld() {
		return nil, database.NewError(database.CodeUnauthorized, "WhatsApp provider access requires broker authority")
	}
	spec, ok := handler.catalog.Lookup(string(storeID))
	if !ok || spec.Domain != whatsappstore.Domain {
		return nil, database.NewError(database.CodeUnauthorized, "WhatsApp store is not broker-cataloged")
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.closed {
		return nil, database.NewError(database.CodeUnavailable, "WhatsApp broker handler is closed")
	}
	if existing := handler.containers[storeID]; existing != nil {
		return existing, nil
	}
	ready, err := sqliteprovider.HasSchemaObjects(
		ctx, spec.Path, busyTimeout, "whatsmeow_version", "whatsmeow_device",
	)
	if err != nil || !ready {
		return nil, database.NewError(database.CodeMigrationRequired, "WhatsApp database migration is required")
	}
	db, err := sqliteprovider.OpenStore(spec.Path, busyTimeout)
	if err != nil {
		return nil, database.NewError(database.CodeUnavailable, "WhatsApp store is unavailable")
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	if err := sqliteprovider.Configure(ctx, db, busyTimeout, false); err != nil {
		_ = db.Close()
		return nil, database.NewError(database.CodeUnavailable, "WhatsApp store is unavailable")
	}
	container := sqlstore.NewWithDB(db, sqliteprovider.DriverName(), waLog.Noop)
	// Runtime deliberately does not call Container.Upgrade. Schema changes are
	// confined to the offline migration adapter below.
	if _, err := container.GetAllDevices(ctx); err != nil {
		_ = container.Close()
		return nil, database.NewError(database.CodeMigrationRequired, "WhatsApp database migration is required")
	}
	retained := &retainedContainer{
		container: container, devices: make(map[string]*sqlstore.SQLStore),
		serialization: make(chan struct{}, 1),
	}
	retained.serialization <- struct{}{}
	handler.containers[storeID] = retained
	return retained, nil
}

func (handler *BrokerHandler) Close() error {
	if handler == nil {
		return nil
	}
	handler.closeOnce.Do(func() {
		handler.mu.Lock()
		handler.closed = true
		containers := handler.containers
		handler.containers = make(map[database.StoreID]*retainedContainer)
		handler.mu.Unlock()
		for _, container := range containers {
			handler.closeErr = errors.Join(handler.closeErr, container.close())
		}
	})
	return handler.closeErr
}

func handleInvoke(
	ctx context.Context,
	retained *retainedContainer,
	request whatsappstore.InvokeRequest,
) (whatsappstore.InvokeResponse, error) {
	container := retained.container
	switch request.Method {
	case whatsappstore.MethodGetFirstDevice:
		device, err := container.GetFirstDevice(ctx)
		if err != nil {
			return whatsappstore.InvokeResponse{}, err
		}
		dto, err := whatsappstore.DeviceToDTO(device)
		return whatsappstore.InvokeResponse{Device: dto}, err
	case whatsappstore.MethodPutDevice:
		device, err := whatsappstore.DeviceFromDTO(request.Device, waLog.Noop)
		if err != nil || device.ID == nil || !validDeviceJID(whatsappstore.JIDFromValue(*device.ID)) ||
			device.Account == nil || device.SignedPreKey.Signature == nil {
			return whatsappstore.InvokeResponse{}, invalidRequest()
		}
		if err := container.PutDevice(ctx, device); err != nil {
			return whatsappstore.InvokeResponse{}, err
		}
		return okResponse(), nil
	case whatsappstore.MethodDeleteDevice:
		device, err := whatsappstore.DeviceFromDTO(request.Device, waLog.Noop)
		if err != nil || device.ID == nil || !validDeviceJID(whatsappstore.JIDFromValue(*device.ID)) {
			return whatsappstore.InvokeResponse{}, invalidRequest()
		}
		if err := container.DeleteDevice(ctx, device); err != nil {
			return whatsappstore.InvokeResponse{}, err
		}
		return okResponse(), nil
	}

	if !validDeviceJID(request.DeviceJID) && !isGlobalMethod(request.Method) {
		return whatsappstore.InvokeResponse{}, invalidRequest()
	}
	deviceStore := retained.device(request.DeviceJID)

	switch request.Method {
	case whatsappstore.MethodPutIdentity:
		key, ok := fixed32(request.Data)
		if !ok {
			return whatsappstore.InvokeResponse{}, invalidRequest()
		}
		return mutationResult(deviceStore.PutIdentity(ctx, request.Address, key))
	case whatsappstore.MethodDeleteAllIdentities:
		return mutationResult(deviceStore.DeleteAllIdentities(ctx, request.Phone))
	case whatsappstore.MethodDeleteIdentity:
		return mutationResult(deviceStore.DeleteIdentity(ctx, request.Address))
	case whatsappstore.MethodIsTrustedIdentity:
		key, ok := fixed32(request.Data)
		if !ok {
			return whatsappstore.InvokeResponse{}, invalidRequest()
		}
		trusted, err := deviceStore.IsTrustedIdentity(ctx, request.Address, key)
		return whatsappstore.InvokeResponse{Flag: trusted}, err

	case whatsappstore.MethodGetSession:
		data, err := deviceStore.GetSession(ctx, request.Address)
		return whatsappstore.InvokeResponse{Data: cloneBytes(data)}, err
	case whatsappstore.MethodHasSession:
		has, err := deviceStore.HasSession(ctx, request.Address)
		return whatsappstore.InvokeResponse{Flag: has}, err
	case whatsappstore.MethodGetManySessions:
		sessions, err := deviceStore.GetManySessions(ctx, request.Addresses)
		if err != nil {
			return whatsappstore.InvokeResponse{}, err
		}
		items := make([]whatsappstore.Session, 0, len(request.Addresses))
		for _, address := range request.Addresses {
			items = append(items, whatsappstore.Session{Address: address, Data: cloneBytes(sessions[address])})
		}
		return whatsappstore.InvokeResponse{Sessions: items}, nil
	case whatsappstore.MethodPutSession:
		return mutationResult(deviceStore.PutSession(ctx, request.Address, request.Data))
	case whatsappstore.MethodPutManySessions:
		sessions := make(map[string][]byte, len(request.Sessions))
		for _, item := range request.Sessions {
			if _, duplicate := sessions[item.Address]; duplicate {
				return whatsappstore.InvokeResponse{}, invalidRequest()
			}
			sessions[item.Address] = cloneBytes(item.Data)
		}
		return mutationResult(deviceStore.PutManySessions(ctx, sessions))
	case whatsappstore.MethodDeleteAllSessions:
		return mutationResult(deviceStore.DeleteAllSessions(ctx, request.Phone))
	case whatsappstore.MethodDeleteSession:
		return mutationResult(deviceStore.DeleteSession(ctx, request.Address))
	case whatsappstore.MethodMigratePNToLID:
		return mutationResult(deviceStore.MigratePNToLID(ctx, request.JID.Value(), request.JID2.Value()))

	case whatsappstore.MethodGetOrGenPreKeys:
		preKeys, err := deviceStore.GetOrGenPreKeys(ctx, request.Count)
		if err != nil {
			return whatsappstore.InvokeResponse{}, err
		}
		items, err := encodePreKeys(preKeys)
		return whatsappstore.InvokeResponse{OK: true, PreKeys: items}, err
	case whatsappstore.MethodGenOnePreKey:
		preKey, err := deviceStore.GenOnePreKey(ctx)
		if err != nil {
			return whatsappstore.InvokeResponse{}, err
		}
		dto, err := whatsappstore.PreKeyToDTO(preKey)
		return whatsappstore.InvokeResponse{OK: true, PreKey: dto}, err
	case whatsappstore.MethodGetPreKey:
		preKey, err := deviceStore.GetPreKey(ctx, request.KeyID)
		if err != nil {
			return whatsappstore.InvokeResponse{}, err
		}
		dto, err := whatsappstore.PreKeyToDTO(preKey)
		return whatsappstore.InvokeResponse{PreKey: dto}, err
	case whatsappstore.MethodRemovePreKey:
		return mutationResult(deviceStore.RemovePreKey(ctx, request.KeyID))
	case whatsappstore.MethodMarkPreKeysAsUploaded:
		return mutationResult(deviceStore.MarkPreKeysAsUploaded(ctx, request.KeyID))
	case whatsappstore.MethodUploadedPreKeyCount:
		count, err := deviceStore.UploadedPreKeyCount(ctx)
		return whatsappstore.InvokeResponse{Count: count}, err

	case whatsappstore.MethodPutSenderKey:
		return mutationResult(deviceStore.PutSenderKey(ctx, request.Group, request.Address, request.Data))
	case whatsappstore.MethodGetSenderKey:
		data, err := deviceStore.GetSenderKey(ctx, request.Group, request.Address)
		return whatsappstore.InvokeResponse{Data: cloneBytes(data)}, err

	case whatsappstore.MethodPutAppStateSyncKey:
		return mutationResult(deviceStore.PutAppStateSyncKey(
			ctx, request.Data, appStateKeyFromDTO(request.AppStateKey),
		))
	case whatsappstore.MethodGetAppStateSyncKey:
		key, err := deviceStore.GetAppStateSyncKey(ctx, request.Data)
		if err != nil || key == nil {
			return whatsappstore.InvokeResponse{}, err
		}
		dto := appStateKeyToDTO(*key)
		return whatsappstore.InvokeResponse{AppStateKey: &dto}, nil
	case whatsappstore.MethodGetLatestAppStateKeyID:
		data, err := deviceStore.GetLatestAppStateSyncKeyID(ctx)
		return whatsappstore.InvokeResponse{Data: cloneBytes(data)}, err
	case whatsappstore.MethodGetAllAppStateSyncKeys:
		keys, err := deviceStore.GetAllAppStateSyncKeys(ctx)
		if err != nil {
			return whatsappstore.InvokeResponse{}, err
		}
		items := make([]whatsappstore.AppStateSyncKey, 0, len(keys))
		for _, key := range keys {
			if key != nil {
				items = append(items, appStateKeyToDTO(*key))
			}
		}
		return whatsappstore.InvokeResponse{AppStateKeys: items}, nil
	case whatsappstore.MethodPutAppStateVersion:
		hash, ok := fixed128(request.Data)
		if !ok {
			return whatsappstore.InvokeResponse{}, invalidRequest()
		}
		return mutationResult(deviceStore.PutAppStateVersion(ctx, request.Name, request.Version, hash))
	case whatsappstore.MethodGetAppStateVersion:
		version, hash, err := deviceStore.GetAppStateVersion(ctx, request.Name)
		return whatsappstore.InvokeResponse{
			Version: version, Data: append([]byte(nil), hash[:]...),
		}, err
	case whatsappstore.MethodDeleteAppStateVersion:
		return mutationResult(deviceStore.DeleteAppStateVersion(ctx, request.Name))
	case whatsappstore.MethodPutAppStateMutationMACs:
		items := make([]store.AppStateMutationMAC, 0, len(request.Mutations))
		for _, item := range request.Mutations {
			items = append(items, store.AppStateMutationMAC{
				IndexMAC: cloneBytes(item.IndexMAC), ValueMAC: cloneBytes(item.ValueMAC),
			})
		}
		return mutationResult(deviceStore.PutAppStateMutationMACs(ctx, request.Name, request.Version, items))
	case whatsappstore.MethodDeleteAppStateMACs:
		return mutationResult(deviceStore.DeleteAppStateMutationMACs(ctx, request.Name, request.IndexMACs))
	case whatsappstore.MethodGetAppStateMutationMAC:
		data, err := deviceStore.GetAppStateMutationMAC(ctx, request.Name, request.Data)
		return whatsappstore.InvokeResponse{Data: cloneBytes(data)}, err

	case whatsappstore.MethodPutPushName:
		changed, previous, err := deviceStore.PutPushName(ctx, request.JID.Value(), request.Text)
		return whatsappstore.InvokeResponse{OK: err == nil, Flag: changed, Text: previous}, err
	case whatsappstore.MethodPutBusinessName:
		changed, previous, err := deviceStore.PutBusinessName(ctx, request.JID.Value(), request.Text)
		return whatsappstore.InvokeResponse{OK: err == nil, Flag: changed, Text: previous}, err
	case whatsappstore.MethodPutContactName:
		return mutationResult(deviceStore.PutContactName(ctx, request.JID.Value(), request.Text, request.Text2))
	case whatsappstore.MethodPutAllContactNames:
		items := make([]store.ContactEntry, 0, len(request.Contacts))
		for _, item := range request.Contacts {
			items = append(items, store.ContactEntry{
				JID: item.JID.Value(), FirstName: item.FirstName, FullName: item.FullName,
			})
		}
		return mutationResult(deviceStore.PutAllContactNames(ctx, items))
	case whatsappstore.MethodPutManyRedactedPhones:
		items := make([]store.RedactedPhoneEntry, 0, len(request.RedactedPhones))
		for _, item := range request.RedactedPhones {
			items = append(items, store.RedactedPhoneEntry{
				JID: item.JID.Value(), RedactedPhone: item.RedactedPhone,
			})
		}
		return mutationResult(deviceStore.PutManyRedactedPhones(ctx, items))
	case whatsappstore.MethodGetContact:
		info, err := deviceStore.GetContact(ctx, request.JID.Value())
		return whatsappstore.InvokeResponse{Contact: contactInfoToDTO(info)}, err
	case whatsappstore.MethodGetAllContacts:
		contacts, err := deviceStore.GetAllContacts(ctx)
		if err != nil {
			return whatsappstore.InvokeResponse{}, err
		}
		jids := make([]types.JID, 0, len(contacts))
		for jid := range contacts {
			jids = append(jids, jid)
		}
		sort.Slice(jids, func(i, j int) bool { return jids[i].String() < jids[j].String() })
		items := make([]whatsappstore.ContactInfo, 0, len(jids))
		for _, jid := range jids {
			item := contactInfoToDTO(contacts[jid])
			item.JID = whatsappstore.JIDFromValue(jid)
			items = append(items, item)
		}
		return whatsappstore.InvokeResponse{Contacts: items}, nil

	case whatsappstore.MethodPutMutedUntil:
		mutedUntil, err := request.Timestamp.Value()
		if err != nil {
			return whatsappstore.InvokeResponse{}, invalidRequest()
		}
		return mutationResult(deviceStore.PutMutedUntil(ctx, request.JID.Value(), mutedUntil))
	case whatsappstore.MethodPutPinned:
		return mutationResult(deviceStore.PutPinned(ctx, request.JID.Value(), request.Flag))
	case whatsappstore.MethodPutArchived:
		return mutationResult(deviceStore.PutArchived(ctx, request.JID.Value(), request.Flag))
	case whatsappstore.MethodGetChatSettings:
		settings, err := deviceStore.GetChatSettings(ctx, request.JID.Value())
		return whatsappstore.InvokeResponse{ChatSettings: whatsappstore.ChatSettings{
			Found: settings.Found, MutedUntil: whatsappstore.TimeFromValue(settings.MutedUntil),
			Pinned: settings.Pinned, Archived: settings.Archived,
		}}, err

	case whatsappstore.MethodPutMessageSecrets:
		items := make([]store.MessageSecretInsert, 0, len(request.MessageSecrets))
		for _, item := range request.MessageSecrets {
			items = append(items, store.MessageSecretInsert{
				Chat: item.Chat.Value(), Sender: item.Sender.Value(), ID: item.ID,
				Secret: cloneBytes(item.Secret),
			})
		}
		return mutationResult(deviceStore.PutMessageSecrets(ctx, items))
	case whatsappstore.MethodPutMessageSecret:
		return mutationResult(deviceStore.PutMessageSecret(
			ctx, request.JID.Value(), request.JID2.Value(), request.MessageID, request.Data,
		))
	case whatsappstore.MethodGetMessageSecret:
		data, realSender, err := deviceStore.GetMessageSecret(
			ctx, request.JID.Value(), request.JID2.Value(), request.MessageID,
		)
		return whatsappstore.InvokeResponse{Data: cloneBytes(data), JID: whatsappstore.JIDFromValue(realSender)}, err

	case whatsappstore.MethodPutPrivacyTokens:
		if len(request.PrivacyTokens) == 0 {
			return okResponse(), nil
		}
		items := make([]store.PrivacyToken, 0, len(request.PrivacyTokens))
		for _, item := range request.PrivacyTokens {
			timestamp, err := item.Timestamp.Value()
			if err != nil {
				return whatsappstore.InvokeResponse{}, invalidRequest()
			}
			items = append(items, store.PrivacyToken{
				User: item.User.Value(), Token: cloneBytes(item.Token), Timestamp: timestamp,
			})
		}
		return mutationResult(deviceStore.PutPrivacyTokens(ctx, items...))
	case whatsappstore.MethodGetPrivacyToken:
		token, err := deviceStore.GetPrivacyToken(ctx, request.JID.Value())
		if err != nil || token == nil {
			return whatsappstore.InvokeResponse{}, err
		}
		dto := &whatsappstore.PrivacyToken{
			User: whatsappstore.JIDFromValue(token.User), Token: cloneBytes(token.Token),
			Timestamp: whatsappstore.TimeFromValue(token.Timestamp),
		}
		return whatsappstore.InvokeResponse{PrivacyToken: dto}, nil

	case whatsappstore.MethodGetBufferedEvent:
		hash, ok := fixed32(request.Data)
		if !ok {
			return whatsappstore.InvokeResponse{}, invalidRequest()
		}
		event, err := deviceStore.GetBufferedEvent(ctx, hash)
		if err != nil || event == nil {
			return whatsappstore.InvokeResponse{}, err
		}
		return whatsappstore.InvokeResponse{BufferedEvent: &whatsappstore.BufferedEvent{
			Plaintext: cloneBytes(event.Plaintext), InsertTime: whatsappstore.TimeFromValue(event.InsertTime),
			ServerTime: whatsappstore.TimeFromValue(event.ServerTime),
		}}, nil
	case whatsappstore.MethodPutBufferedEvent:
		hash, ok := fixed32(request.Data)
		if !ok {
			return whatsappstore.InvokeResponse{}, invalidRequest()
		}
		serverTime, err := request.Timestamp.Value()
		if err != nil {
			return whatsappstore.InvokeResponse{}, invalidRequest()
		}
		return mutationResult(deviceStore.PutBufferedEvent(ctx, hash, request.Data2, serverTime))
	case whatsappstore.MethodClearBufferedEventPlaintext:
		hash, ok := fixed32(request.Data)
		if !ok {
			return whatsappstore.InvokeResponse{}, invalidRequest()
		}
		return mutationResult(deviceStore.ClearBufferedEventPlaintext(ctx, hash))
	case whatsappstore.MethodDeleteOldBufferedHashes:
		return mutationResult(deviceStore.DeleteOldBufferedHashes(ctx))
	case whatsappstore.MethodGetOutgoingEvent:
		format, data, err := deviceStore.GetOutgoingEvent(
			ctx, request.JID.Value(), request.JID2.Value(), request.MessageID,
		)
		return whatsappstore.InvokeResponse{Text: format, Data: cloneBytes(data)}, err
	case whatsappstore.MethodAddOutgoingEvent:
		return mutationResult(deviceStore.AddOutgoingEvent(
			ctx, request.JID.Value(), request.MessageID, request.Format, request.Data,
		))
	case whatsappstore.MethodDeleteOldOutgoingEvents:
		return mutationResult(deviceStore.DeleteOldOutgoingEvents(ctx))

	case whatsappstore.MethodPutManyLIDMappings:
		items := make([]store.LIDMapping, 0, len(request.LIDMappings))
		for _, item := range request.LIDMappings {
			items = append(items, store.LIDMapping{LID: item.LID.Value(), PN: item.PN.Value()})
		}
		return mutationResult(container.LIDMap.PutManyLIDMappings(ctx, items))
	case whatsappstore.MethodPutLIDMapping:
		return mutationResult(container.LIDMap.PutLIDMapping(ctx, request.JID.Value(), request.JID2.Value()))
	case whatsappstore.MethodGetPNForLID:
		jid, err := container.LIDMap.GetPNForLID(ctx, request.JID.Value())
		return whatsappstore.InvokeResponse{JID: whatsappstore.JIDFromValue(jid)}, err
	case whatsappstore.MethodGetLIDForPN:
		jid, err := container.LIDMap.GetLIDForPN(ctx, request.JID.Value())
		return whatsappstore.InvokeResponse{JID: whatsappstore.JIDFromValue(jid)}, err
	case whatsappstore.MethodGetManyLIDsForPNs:
		pns := make([]types.JID, 0, len(request.JIDs))
		for _, jid := range request.JIDs {
			pns = append(pns, jid.Value())
		}
		mappings, err := container.LIDMap.GetManyLIDsForPNs(ctx, pns)
		if err != nil {
			return whatsappstore.InvokeResponse{}, err
		}
		items := make([]whatsappstore.LIDMapping, 0, len(mappings))
		for _, pn := range pns {
			if lid, ok := mappings[pn]; ok {
				items = append(items, whatsappstore.LIDMapping{
					PN: whatsappstore.JIDFromValue(pn), LID: whatsappstore.JIDFromValue(lid),
				})
			}
		}
		return whatsappstore.InvokeResponse{LIDMappings: items}, nil
	default:
		return whatsappstore.InvokeResponse{}, database.NewError(
			database.CodeUnsupported, "WhatsApp store method is unsupported",
		)
	}
}

func validateDecryptionMutation(mutation whatsappstore.DecryptionMutation) error {
	switch mutation.Method {
	case whatsappstore.MethodPutIdentity:
		_, ok := fixed32(mutation.Data)
		if !ok {
			return errors.New("invalid identity key")
		}
	case whatsappstore.MethodDeleteAllIdentities,
		whatsappstore.MethodDeleteIdentity,
		whatsappstore.MethodPutSession,
		whatsappstore.MethodDeleteAllSessions,
		whatsappstore.MethodDeleteSession,
		whatsappstore.MethodRemovePreKey,
		whatsappstore.MethodMarkPreKeysAsUploaded,
		whatsappstore.MethodPutSenderKey:
		return nil
	case whatsappstore.MethodPutBufferedEvent:
		if _, ok := fixed32(mutation.Data); !ok {
			return errors.New("invalid buffered-event hash")
		}
		_, err := mutation.Timestamp.Value()
		return err
	default:
		return errors.New("unsupported decryption mutation")
	}
	return nil
}

func applyDecryptionMutation(
	ctx context.Context,
	deviceStore *sqlstore.SQLStore,
	mutation whatsappstore.DecryptionMutation,
) error {
	switch mutation.Method {
	case whatsappstore.MethodPutIdentity:
		key, _ := fixed32(mutation.Data)
		return deviceStore.PutIdentity(ctx, mutation.Address, key)
	case whatsappstore.MethodDeleteAllIdentities:
		return deviceStore.DeleteAllIdentities(ctx, mutation.Phone)
	case whatsappstore.MethodDeleteIdentity:
		return deviceStore.DeleteIdentity(ctx, mutation.Address)
	case whatsappstore.MethodPutSession:
		return deviceStore.PutSession(ctx, mutation.Address, mutation.Data)
	case whatsappstore.MethodDeleteAllSessions:
		return deviceStore.DeleteAllSessions(ctx, mutation.Phone)
	case whatsappstore.MethodDeleteSession:
		return deviceStore.DeleteSession(ctx, mutation.Address)
	case whatsappstore.MethodRemovePreKey:
		return deviceStore.RemovePreKey(ctx, mutation.KeyID)
	case whatsappstore.MethodMarkPreKeysAsUploaded:
		return deviceStore.MarkPreKeysAsUploaded(ctx, mutation.KeyID)
	case whatsappstore.MethodPutSenderKey:
		return deviceStore.PutSenderKey(ctx, mutation.Group, mutation.Address, mutation.Data)
	case whatsappstore.MethodPutBufferedEvent:
		hash, _ := fixed32(mutation.Data)
		serverTime, _ := mutation.Timestamp.Value()
		return deviceStore.PutBufferedEvent(ctx, hash, mutation.Data2, serverTime)
	default:
		return errors.New("unsupported decryption mutation")
	}
}

func mutationResult(err error) (whatsappstore.InvokeResponse, error) {
	return whatsappstore.InvokeResponse{OK: err == nil}, err
}

func okResponse() whatsappstore.InvokeResponse { return whatsappstore.InvokeResponse{OK: true} }

func fixed32(value []byte) ([32]byte, bool) {
	var result [32]byte
	if len(value) != len(result) {
		return result, false
	}
	copy(result[:], value)
	return result, true
}

func fixed128(value []byte) ([128]byte, bool) {
	var result [128]byte
	if len(value) != len(result) {
		return result, false
	}
	copy(result[:], value)
	return result, true
}

func encodePreKeys(preKeys []*keys.PreKey) ([]whatsappstore.PreKey, error) {
	items := make([]whatsappstore.PreKey, 0, len(preKeys))
	for _, preKey := range preKeys {
		dto, err := whatsappstore.PreKeyToDTO(preKey)
		if err != nil {
			return nil, err
		}
		if dto != nil {
			items = append(items, *dto)
		}
	}
	return items, nil
}

func appStateKeyToDTO(key store.AppStateSyncKey) whatsappstore.AppStateSyncKey {
	return whatsappstore.AppStateSyncKey{
		Data: cloneBytes(key.Data), Fingerprint: cloneBytes(key.Fingerprint), Timestamp: key.Timestamp,
	}
}

func appStateKeyFromDTO(key whatsappstore.AppStateSyncKey) store.AppStateSyncKey {
	return store.AppStateSyncKey{
		Data: cloneBytes(key.Data), Fingerprint: cloneBytes(key.Fingerprint), Timestamp: key.Timestamp,
	}
}

func contactInfoToDTO(info types.ContactInfo) whatsappstore.ContactInfo {
	return whatsappstore.ContactInfo{
		Found: info.Found, FirstName: info.FirstName, FullName: info.FullName,
		PushName: info.PushName, BusinessName: info.BusinessName, RedactedPhone: info.RedactedPhone,
	}
}

func isGlobalMethod(method whatsappstore.Method) bool {
	switch method {
	case whatsappstore.MethodPutManyLIDMappings,
		whatsappstore.MethodPutLIDMapping,
		whatsappstore.MethodGetPNForLID,
		whatsappstore.MethodGetLIDForPN,
		whatsappstore.MethodGetManyLIDsForPNs:
		return true
	default:
		return false
	}
}

func isDecryptionLeaseRead(method whatsappstore.Method) bool {
	switch method {
	case whatsappstore.MethodIsTrustedIdentity,
		whatsappstore.MethodGetSession,
		whatsappstore.MethodHasSession,
		whatsappstore.MethodGetManySessions,
		whatsappstore.MethodGetPreKey,
		whatsappstore.MethodUploadedPreKeyCount,
		whatsappstore.MethodGetSenderKey,
		whatsappstore.MethodGetAppStateSyncKey,
		whatsappstore.MethodGetLatestAppStateKeyID,
		whatsappstore.MethodGetAllAppStateSyncKeys,
		whatsappstore.MethodGetAppStateVersion,
		whatsappstore.MethodGetAppStateMutationMAC,
		whatsappstore.MethodGetContact,
		whatsappstore.MethodGetAllContacts,
		whatsappstore.MethodGetChatSettings,
		whatsappstore.MethodGetMessageSecret,
		whatsappstore.MethodGetPrivacyToken,
		whatsappstore.MethodGetBufferedEvent,
		whatsappstore.MethodGetOutgoingEvent,
		whatsappstore.MethodGetPNForLID,
		whatsappstore.MethodGetLIDForPN,
		whatsappstore.MethodGetManyLIDsForPNs:
		return true
	default:
		return false
	}
}

func validStoreID(storeID database.StoreID) bool {
	return storeID.Valid() && strings.HasPrefix(string(storeID), "channel/whatsapp/")
}

func validDeviceJID(jid whatsappstore.JID) bool {
	return strings.TrimSpace(jid.User) != "" && strings.TrimSpace(jid.Server) != "" &&
		jid.User == strings.TrimSpace(jid.User) && jid.Server == strings.TrimSpace(jid.Server) &&
		!strings.ContainsRune(jid.User, 0) && !strings.ContainsRune(jid.Server, 0)
}

func invalidRequest() *database.Error {
	return database.NewError(database.CodeInvalid, "WhatsApp broker request is invalid")
}

func mapOperationError(err error) error {
	if err == nil {
		return nil
	}
	if code := database.CodeOf(err); code != database.CodeInternal {
		return err
	}
	return database.NewError(database.CodeInternal, "WhatsApp store operation failed")
}

func cloneBytes(value []byte) []byte {
	if value == nil {
		return nil
	}
	return append([]byte(nil), value...)
}
