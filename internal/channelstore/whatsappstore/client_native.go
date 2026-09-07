//go:build whatsapp_native

package whatsappstore

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/util/keys"
	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/sipeed/picoclaw/pkg/database"
)

// Container is the runtime typed client for one broker-cataloged WhatsApp
// store. Closing it releases only local references; the broker owns the
// retained provider pool.
type Container struct {
	client  *database.Client
	storeID database.StoreID
	log     waLog.Logger

	mu     sync.RWMutex
	closed bool

	transactionMu sync.RWMutex
}

func NewContainer(
	client *database.Client,
	storeID database.StoreID,
	log waLog.Logger,
) (*Container, error) {
	if client == nil {
		return nil, database.NewError(database.CodeUnavailable, "WhatsApp database broker client is unavailable")
	}
	if !storeID.Valid() || !strings.HasPrefix(string(storeID), "channel/whatsapp/") {
		return nil, database.NewError(database.CodeInvalid, "WhatsApp StoreID is invalid")
	}
	if log == nil {
		log = waLog.Noop
	}
	return &Container{client: client, storeID: storeID, log: log}, nil
}

func (container *Container) GetFirstDevice(ctx context.Context) (*store.Device, error) {
	response, err := container.invoke(ctx, InvokeRequest{Method: MethodGetFirstDevice}, false)
	if err != nil {
		return nil, err
	}
	device, err := DeviceFromDTO(response.Device, container.log)
	if err != nil {
		return nil, database.NewError(database.CodeIntegrity, "WhatsApp device response is invalid")
	}
	adapter := &Adapter{container: container}
	adapter.bindDevice(device)
	return device, nil
}

func (container *Container) Close() error {
	if container == nil {
		return nil
	}
	container.transactionMu.Lock()
	defer container.transactionMu.Unlock()
	container.mu.Lock()
	container.closed = true
	container.mu.Unlock()
	return nil
}

func (container *Container) invoke(
	ctx context.Context,
	request InvokeRequest,
	mutation bool,
) (InvokeResponse, error) {
	var response InvokeResponse
	if container == nil || container.client == nil {
		return response, database.NewError(database.CodeUnavailable, "WhatsApp store client is unavailable")
	}
	collector := decryptionCollectorFrom(ctx)
	if collector == nil {
		container.transactionMu.RLock()
		defer container.transactionMu.RUnlock()
	} else {
		if collector.storeID != container.storeID || collector.deviceJID != request.DeviceJID {
			return response, database.NewError(
				database.CodeConflict,
				"WhatsApp decryption transaction target does not match the active lease",
			)
		}
		request.DecryptionLease = collector.leaseID
	}
	container.mu.RLock()
	closed := container.closed
	container.mu.RUnlock()
	if closed {
		return response, database.NewError(database.CodeUnavailable, "WhatsApp store client is closed")
	}
	request.StoreID = container.storeID
	if mutation {
		err := container.client.CallWithOptions(
			ctx, Domain, Version, InvokeOperation, request, &response,
			database.CallOptions{Mutation: true},
		)
		return response, err
	}
	err := container.client.Call(ctx, Domain, Version, InvokeOperation, request, &response)
	return response, err
}

// Adapter implements the complete pinned whatsmeow storage surface for one
// device. All calls are translated into the closed DTO protocol above.
type Adapter struct {
	container *Container
	mu        sync.RWMutex
	deviceJID JID
}

var (
	_ store.DeviceContainer      = (*Adapter)(nil)
	_ store.IdentityStore        = (*Adapter)(nil)
	_ store.SessionStore         = (*Adapter)(nil)
	_ store.PreKeyStore          = (*Adapter)(nil)
	_ store.SenderKeyStore       = (*Adapter)(nil)
	_ store.AppStateSyncKeyStore = (*Adapter)(nil)
	_ store.AppStateStore        = (*Adapter)(nil)
	_ store.ContactStore         = (*Adapter)(nil)
	_ store.ChatSettingsStore    = (*Adapter)(nil)
	_ store.MsgSecretStore       = (*Adapter)(nil)
	_ store.PrivacyTokenStore    = (*Adapter)(nil)
	_ store.EventBuffer          = (*Adapter)(nil)
	_ store.LIDStore             = (*Adapter)(nil)
	_ store.AllStores            = (*Adapter)(nil)
)

func (adapter *Adapter) bindDevice(device *store.Device) {
	if device.ID != nil {
		adapter.mu.Lock()
		adapter.deviceJID = JIDFromValue(*device.ID)
		adapter.mu.Unlock()
	}
	device.Container = adapter
	if !device.Initialized {
		return
	}
	device.Identities = adapter
	device.Sessions = adapter
	device.PreKeys = adapter
	device.SenderKeys = adapter
	device.AppStateKeys = adapter
	device.AppState = adapter
	device.Contacts = adapter
	device.ChatSettings = adapter
	device.MsgSecrets = adapter
	device.PrivacyTokens = adapter
	device.EventBuffer = adapter
	device.LIDs = adapter
}

func (adapter *Adapter) initializeDevice(device *store.Device) {
	device.Initialized = true
	adapter.bindDevice(device)
}

func (adapter *Adapter) target(method Method) (InvokeRequest, error) {
	if adapter == nil || adapter.container == nil {
		return InvokeRequest{}, database.NewError(database.CodeUnavailable, "WhatsApp store adapter is unavailable")
	}
	adapter.mu.RLock()
	jid := adapter.deviceJID
	adapter.mu.RUnlock()
	return InvokeRequest{DeviceJID: jid, Method: method}, nil
}

func (adapter *Adapter) call(
	ctx context.Context,
	request InvokeRequest,
	mutation bool,
) (InvokeResponse, error) {
	target, err := adapter.target(request.Method)
	if err != nil {
		return InvokeResponse{}, err
	}
	request.DeviceJID = target.DeviceJID
	return adapter.container.invoke(ctx, request, mutation)
}

func (adapter *Adapter) collector(ctx context.Context) (*decryptionCollector, error) {
	collector := decryptionCollectorFrom(ctx)
	if collector == nil {
		return nil, nil
	}
	target, err := adapter.target("")
	if err != nil {
		return nil, err
	}
	if collector.storeID != adapter.container.storeID || collector.deviceJID != target.DeviceJID {
		return nil, database.NewError(
			database.CodeConflict,
			"WhatsApp decryption transaction target does not match the active lease",
		)
	}
	return collector, nil
}

func (adapter *Adapter) mutate(
	ctx context.Context,
	request InvokeRequest,
	collected *DecryptionMutation,
) error {
	collector, err := adapter.collector(ctx)
	if err != nil {
		return err
	}
	if collector != nil {
		if collected == nil {
			return database.NewError(
				database.CodeUnsupported,
				"WhatsApp mutation is unsupported in a decryption transaction",
			)
		}
		collector.append(*collected)
		return nil
	}
	response, err := adapter.call(ctx, request, true)
	if err != nil {
		return err
	}
	if !response.OK {
		return database.NewError(database.CodeIntegrity, "WhatsApp mutation response is invalid")
	}
	return nil
}

func (adapter *Adapter) PutDevice(ctx context.Context, device *store.Device) error {
	if device == nil || device.ID == nil {
		return database.NewError(database.CodeInvalid, "WhatsApp device ID is missing")
	}
	dto, err := DeviceToDTO(device)
	if err != nil {
		return database.NewError(database.CodeInvalid, "WhatsApp device is invalid")
	}
	response, err := adapter.call(ctx, InvokeRequest{Method: MethodPutDevice, Device: dto}, true)
	if err != nil {
		return err
	}
	if !response.OK {
		return database.NewError(database.CodeIntegrity, "WhatsApp device mutation response is invalid")
	}
	if !device.Initialized {
		adapter.initializeDevice(device)
	} else {
		adapter.bindDevice(device)
	}
	return nil
}

func (adapter *Adapter) DeleteDevice(ctx context.Context, device *store.Device) error {
	if device == nil || device.ID == nil {
		return database.NewError(database.CodeInvalid, "WhatsApp device ID is missing")
	}
	dto, err := DeviceToDTO(device)
	if err != nil {
		return database.NewError(database.CodeInvalid, "WhatsApp device is invalid")
	}
	response, err := adapter.call(ctx, InvokeRequest{Method: MethodDeleteDevice, Device: dto}, true)
	if err != nil {
		return err
	}
	if !response.OK {
		return database.NewError(database.CodeIntegrity, "WhatsApp device mutation response is invalid")
	}
	return nil
}

func (adapter *Adapter) PutIdentity(ctx context.Context, address string, key [32]byte) error {
	data := append([]byte(nil), key[:]...)
	return adapter.mutate(
		ctx,
		InvokeRequest{Method: MethodPutIdentity, Address: address, Data: data},
		&DecryptionMutation{Method: MethodPutIdentity, Address: address, Data: data},
	)
}

func (adapter *Adapter) DeleteAllIdentities(ctx context.Context, phone string) error {
	return adapter.mutate(
		ctx,
		InvokeRequest{Method: MethodDeleteAllIdentities, Phone: phone},
		&DecryptionMutation{Method: MethodDeleteAllIdentities, Phone: phone},
	)
}

func (adapter *Adapter) DeleteIdentity(ctx context.Context, address string) error {
	return adapter.mutate(
		ctx,
		InvokeRequest{Method: MethodDeleteIdentity, Address: address},
		&DecryptionMutation{Method: MethodDeleteIdentity, Address: address},
	)
}

func (adapter *Adapter) IsTrustedIdentity(
	ctx context.Context,
	address string,
	key [32]byte,
) (bool, error) {
	collector, err := adapter.collector(ctx)
	if err != nil {
		return false, err
	}
	if collector != nil {
		if trusted, handled := collector.identity(address, key); handled {
			return trusted, nil
		}
	}
	response, err := adapter.call(ctx, InvokeRequest{
		Method: MethodIsTrustedIdentity, Address: address, Data: append([]byte(nil), key[:]...),
	}, false)
	return response.Flag, err
}

func (adapter *Adapter) GetSession(ctx context.Context, address string) ([]byte, error) {
	collector, err := adapter.collector(ctx)
	if err != nil {
		return nil, err
	}
	if collector != nil {
		if data, handled := collector.session(address); handled {
			return data, nil
		}
	}
	response, err := adapter.call(ctx, InvokeRequest{Method: MethodGetSession, Address: address}, false)
	return cloneBytes(response.Data), err
}

func (adapter *Adapter) HasSession(ctx context.Context, address string) (bool, error) {
	collector, err := adapter.collector(ctx)
	if err != nil {
		return false, err
	}
	if collector != nil {
		if data, handled := collector.session(address); handled {
			return data != nil, nil
		}
	}
	response, err := adapter.call(ctx, InvokeRequest{Method: MethodHasSession, Address: address}, false)
	return response.Flag, err
}

func (adapter *Adapter) GetManySessions(
	ctx context.Context,
	addresses []string,
) (map[string][]byte, error) {
	collector, err := adapter.collector(ctx)
	if err != nil {
		return nil, err
	}
	response, err := adapter.call(ctx, InvokeRequest{
		Method: MethodGetManySessions, Addresses: append([]string(nil), addresses...),
	}, false)
	if err != nil {
		return nil, err
	}
	result := make(map[string][]byte, len(response.Sessions))
	for _, session := range response.Sessions {
		result[session.Address] = cloneBytes(session.Data)
	}
	if collector != nil {
		for _, address := range addresses {
			if data, handled := collector.session(address); handled {
				result[address] = data
			}
		}
	}
	return result, nil
}

func (adapter *Adapter) PutSession(ctx context.Context, address string, session []byte) error {
	data := cloneBytes(session)
	return adapter.mutate(
		ctx,
		InvokeRequest{Method: MethodPutSession, Address: address, Data: data},
		&DecryptionMutation{Method: MethodPutSession, Address: address, Data: data},
	)
}

func (adapter *Adapter) PutManySessions(ctx context.Context, sessions map[string][]byte) error {
	addresses := make([]string, 0, len(sessions))
	for address := range sessions {
		addresses = append(addresses, address)
	}
	sort.Strings(addresses)
	collector, err := adapter.collector(ctx)
	if err != nil {
		return err
	}
	if collector != nil {
		for _, address := range addresses {
			collector.append(DecryptionMutation{
				Method: MethodPutSession, Address: address, Data: cloneBytes(sessions[address]),
			})
		}
		return nil
	}
	items := make([]Session, 0, len(addresses))
	for _, address := range addresses {
		items = append(items, Session{Address: address, Data: cloneBytes(sessions[address])})
	}
	return adapter.mutate(ctx, InvokeRequest{Method: MethodPutManySessions, Sessions: items}, nil)
}

func (adapter *Adapter) DeleteAllSessions(ctx context.Context, phone string) error {
	return adapter.mutate(
		ctx,
		InvokeRequest{Method: MethodDeleteAllSessions, Phone: phone},
		&DecryptionMutation{Method: MethodDeleteAllSessions, Phone: phone},
	)
}

func (adapter *Adapter) DeleteSession(ctx context.Context, address string) error {
	return adapter.mutate(
		ctx,
		InvokeRequest{Method: MethodDeleteSession, Address: address},
		&DecryptionMutation{Method: MethodDeleteSession, Address: address},
	)
}

func (adapter *Adapter) MigratePNToLID(ctx context.Context, pn, lid types.JID) error {
	return adapter.mutate(ctx, InvokeRequest{
		Method: MethodMigratePNToLID, JID: JIDFromValue(pn), JID2: JIDFromValue(lid),
	}, nil)
}

func (adapter *Adapter) GetOrGenPreKeys(ctx context.Context, count uint32) ([]*keys.PreKey, error) {
	if decryptionCollectorFrom(ctx) != nil {
		return nil, database.NewError(
			database.CodeUnsupported,
			"WhatsApp pre-key generation is unsupported in a decryption transaction",
		)
	}
	response, err := adapter.call(ctx, InvokeRequest{Method: MethodGetOrGenPreKeys, Count: count}, true)
	if err != nil {
		return nil, err
	}
	return decodePreKeys(response.PreKeys)
}

func (adapter *Adapter) GenOnePreKey(ctx context.Context) (*keys.PreKey, error) {
	if decryptionCollectorFrom(ctx) != nil {
		return nil, database.NewError(
			database.CodeUnsupported,
			"WhatsApp pre-key generation is unsupported in a decryption transaction",
		)
	}
	response, err := adapter.call(ctx, InvokeRequest{Method: MethodGenOnePreKey}, true)
	if err != nil {
		return nil, err
	}
	return PreKeyFromDTO(response.PreKey)
}

func (adapter *Adapter) GetPreKey(ctx context.Context, id uint32) (*keys.PreKey, error) {
	collector, err := adapter.collector(ctx)
	if err != nil {
		return nil, err
	}
	if collector != nil && collector.preKeyRemoved(id) {
		return nil, nil
	}
	response, err := adapter.call(ctx, InvokeRequest{Method: MethodGetPreKey, KeyID: id}, false)
	if err != nil {
		return nil, err
	}
	return PreKeyFromDTO(response.PreKey)
}

func (adapter *Adapter) RemovePreKey(ctx context.Context, id uint32) error {
	return adapter.mutate(
		ctx,
		InvokeRequest{Method: MethodRemovePreKey, KeyID: id},
		&DecryptionMutation{Method: MethodRemovePreKey, KeyID: id},
	)
}

func (adapter *Adapter) MarkPreKeysAsUploaded(ctx context.Context, upToID uint32) error {
	return adapter.mutate(
		ctx,
		InvokeRequest{Method: MethodMarkPreKeysAsUploaded, KeyID: upToID},
		&DecryptionMutation{Method: MethodMarkPreKeysAsUploaded, KeyID: upToID},
	)
}

func (adapter *Adapter) UploadedPreKeyCount(ctx context.Context) (int, error) {
	response, err := adapter.call(ctx, InvokeRequest{Method: MethodUploadedPreKeyCount}, false)
	return response.Count, err
}

func (adapter *Adapter) PutSenderKey(
	ctx context.Context,
	group, user string,
	session []byte,
) error {
	data := cloneBytes(session)
	return adapter.mutate(
		ctx,
		InvokeRequest{Method: MethodPutSenderKey, Group: group, Address: user, Data: data},
		&DecryptionMutation{Method: MethodPutSenderKey, Group: group, Address: user, Data: data},
	)
}

func (adapter *Adapter) GetSenderKey(ctx context.Context, group, user string) ([]byte, error) {
	collector, err := adapter.collector(ctx)
	if err != nil {
		return nil, err
	}
	if collector != nil {
		if data, handled := collector.senderKey(group, user); handled {
			return data, nil
		}
	}
	response, err := adapter.call(ctx, InvokeRequest{
		Method: MethodGetSenderKey, Group: group, Address: user,
	}, false)
	return cloneBytes(response.Data), err
}

func (adapter *Adapter) PutAppStateSyncKey(
	ctx context.Context,
	id []byte,
	key store.AppStateSyncKey,
) error {
	return adapter.mutate(ctx, InvokeRequest{
		Method: MethodPutAppStateSyncKey, Data: cloneBytes(id), AppStateKey: appStateKeyToDTO(key),
	}, nil)
}

func (adapter *Adapter) GetAppStateSyncKey(
	ctx context.Context,
	id []byte,
) (*store.AppStateSyncKey, error) {
	response, err := adapter.call(ctx, InvokeRequest{
		Method: MethodGetAppStateSyncKey, Data: cloneBytes(id),
	}, false)
	if err != nil || response.AppStateKey == nil {
		return nil, err
	}
	key := appStateKeyFromDTO(*response.AppStateKey)
	return &key, nil
}

func (adapter *Adapter) GetLatestAppStateSyncKeyID(ctx context.Context) ([]byte, error) {
	response, err := adapter.call(ctx, InvokeRequest{Method: MethodGetLatestAppStateKeyID}, false)
	return cloneBytes(response.Data), err
}

func (adapter *Adapter) GetAllAppStateSyncKeys(ctx context.Context) ([]*store.AppStateSyncKey, error) {
	response, err := adapter.call(ctx, InvokeRequest{Method: MethodGetAllAppStateSyncKeys}, false)
	if err != nil {
		return nil, err
	}
	result := make([]*store.AppStateSyncKey, 0, len(response.AppStateKeys))
	for _, item := range response.AppStateKeys {
		key := appStateKeyFromDTO(item)
		result = append(result, &key)
	}
	return result, nil
}

func (adapter *Adapter) PutAppStateVersion(
	ctx context.Context,
	name string,
	version uint64,
	hash [128]byte,
) error {
	return adapter.mutate(ctx, InvokeRequest{
		Method: MethodPutAppStateVersion, Name: name, Version: version,
		Data: append([]byte(nil), hash[:]...),
	}, nil)
}

func (adapter *Adapter) GetAppStateVersion(
	ctx context.Context,
	name string,
) (uint64, [128]byte, error) {
	response, err := adapter.call(ctx, InvokeRequest{Method: MethodGetAppStateVersion, Name: name}, false)
	var hash [128]byte
	if err != nil {
		return 0, hash, err
	}
	if len(response.Data) != 128 {
		if response.Version == 0 && len(response.Data) == 0 {
			return 0, hash, nil
		}
		return 0, hash, database.NewError(database.CodeIntegrity, "WhatsApp app-state response is invalid")
	}
	copy(hash[:], response.Data)
	return response.Version, hash, nil
}

func (adapter *Adapter) DeleteAppStateVersion(ctx context.Context, name string) error {
	return adapter.mutate(ctx, InvokeRequest{Method: MethodDeleteAppStateVersion, Name: name}, nil)
}

func (adapter *Adapter) PutAppStateMutationMACs(
	ctx context.Context,
	name string,
	version uint64,
	mutations []store.AppStateMutationMAC,
) error {
	items := make([]AppStateMutationMAC, 0, len(mutations))
	for _, mutation := range mutations {
		items = append(items, AppStateMutationMAC{
			IndexMAC: cloneBytes(mutation.IndexMAC), ValueMAC: cloneBytes(mutation.ValueMAC),
		})
	}
	return adapter.mutate(ctx, InvokeRequest{
		Method: MethodPutAppStateMutationMACs, Name: name, Version: version, Mutations: items,
	}, nil)
}

func (adapter *Adapter) DeleteAppStateMutationMACs(
	ctx context.Context,
	name string,
	indexMACs [][]byte,
) error {
	return adapter.mutate(ctx, InvokeRequest{
		Method: MethodDeleteAppStateMACs, Name: name, IndexMACs: cloneByteSlices(indexMACs),
	}, nil)
}

func (adapter *Adapter) GetAppStateMutationMAC(
	ctx context.Context,
	name string,
	indexMAC []byte,
) ([]byte, error) {
	response, err := adapter.call(ctx, InvokeRequest{
		Method: MethodGetAppStateMutationMAC, Name: name, Data: cloneBytes(indexMAC),
	}, false)
	return cloneBytes(response.Data), err
}

func (adapter *Adapter) PutPushName(
	ctx context.Context,
	user types.JID,
	pushName string,
) (bool, string, error) {
	if decryptionCollectorFrom(ctx) != nil {
		return false, "", database.NewError(
			database.CodeUnsupported,
			"WhatsApp contact mutation is unsupported in a decryption transaction",
		)
	}
	response, err := adapter.call(ctx, InvokeRequest{
		Method: MethodPutPushName, JID: JIDFromValue(user), Text: pushName,
	}, true)
	return response.Flag, response.Text, err
}

func (adapter *Adapter) PutBusinessName(
	ctx context.Context,
	user types.JID,
	businessName string,
) (bool, string, error) {
	if decryptionCollectorFrom(ctx) != nil {
		return false, "", database.NewError(
			database.CodeUnsupported,
			"WhatsApp contact mutation is unsupported in a decryption transaction",
		)
	}
	response, err := adapter.call(ctx, InvokeRequest{
		Method: MethodPutBusinessName, JID: JIDFromValue(user), Text: businessName,
	}, true)
	return response.Flag, response.Text, err
}

func (adapter *Adapter) PutContactName(
	ctx context.Context,
	user types.JID,
	fullName, firstName string,
) error {
	return adapter.mutate(ctx, InvokeRequest{
		Method: MethodPutContactName, JID: JIDFromValue(user), Text: fullName, Text2: firstName,
	}, nil)
}

func (adapter *Adapter) PutAllContactNames(ctx context.Context, contacts []store.ContactEntry) error {
	items := make([]ContactEntry, 0, len(contacts))
	for _, contact := range contacts {
		items = append(items, ContactEntry{
			JID: JIDFromValue(contact.JID), FirstName: contact.FirstName, FullName: contact.FullName,
		})
	}
	return adapter.mutate(ctx, InvokeRequest{Method: MethodPutAllContactNames, Contacts: items}, nil)
}

func (adapter *Adapter) PutManyRedactedPhones(
	ctx context.Context,
	entries []store.RedactedPhoneEntry,
) error {
	items := make([]RedactedPhoneEntry, 0, len(entries))
	for _, entry := range entries {
		items = append(items, RedactedPhoneEntry{
			JID: JIDFromValue(entry.JID), RedactedPhone: entry.RedactedPhone,
		})
	}
	return adapter.mutate(ctx, InvokeRequest{Method: MethodPutManyRedactedPhones, RedactedPhones: items}, nil)
}

func (adapter *Adapter) GetContact(ctx context.Context, user types.JID) (types.ContactInfo, error) {
	response, err := adapter.call(ctx, InvokeRequest{Method: MethodGetContact, JID: JIDFromValue(user)}, false)
	return contactInfoFromDTO(response.Contact), err
}

func (adapter *Adapter) GetAllContacts(ctx context.Context) (map[types.JID]types.ContactInfo, error) {
	response, err := adapter.call(ctx, InvokeRequest{Method: MethodGetAllContacts}, false)
	if err != nil {
		return nil, err
	}
	result := make(map[types.JID]types.ContactInfo, len(response.Contacts))
	for _, item := range response.Contacts {
		result[item.JID.Value()] = contactInfoFromDTO(item)
	}
	return result, nil
}

func (adapter *Adapter) PutMutedUntil(ctx context.Context, chat types.JID, mutedUntil time.Time) error {
	return adapter.mutate(ctx, InvokeRequest{
		Method: MethodPutMutedUntil, JID: JIDFromValue(chat), Timestamp: TimeFromValue(mutedUntil),
	}, nil)
}

func (adapter *Adapter) PutPinned(ctx context.Context, chat types.JID, pinned bool) error {
	return adapter.mutate(ctx, InvokeRequest{
		Method: MethodPutPinned, JID: JIDFromValue(chat), Flag: pinned,
	}, nil)
}

func (adapter *Adapter) PutArchived(ctx context.Context, chat types.JID, archived bool) error {
	return adapter.mutate(ctx, InvokeRequest{
		Method: MethodPutArchived, JID: JIDFromValue(chat), Flag: archived,
	}, nil)
}

func (adapter *Adapter) GetChatSettings(
	ctx context.Context,
	chat types.JID,
) (types.LocalChatSettings, error) {
	response, err := adapter.call(ctx, InvokeRequest{Method: MethodGetChatSettings, JID: JIDFromValue(chat)}, false)
	if err != nil {
		return types.LocalChatSettings{}, err
	}
	mutedUntil, err := response.ChatSettings.MutedUntil.Value()
	if err != nil {
		return types.LocalChatSettings{}, database.NewError(
			database.CodeIntegrity,
			"WhatsApp chat-settings response is invalid",
		)
	}
	return types.LocalChatSettings{
		Found: response.ChatSettings.Found, MutedUntil: mutedUntil,
		Pinned: response.ChatSettings.Pinned, Archived: response.ChatSettings.Archived,
	}, nil
}

func (adapter *Adapter) PutMessageSecrets(
	ctx context.Context,
	inserts []store.MessageSecretInsert,
) error {
	items := make([]MessageSecret, 0, len(inserts))
	for _, insert := range inserts {
		items = append(items, MessageSecret{
			Chat: JIDFromValue(insert.Chat), Sender: JIDFromValue(insert.Sender),
			ID: insert.ID, Secret: cloneBytes(insert.Secret),
		})
	}
	return adapter.mutate(ctx, InvokeRequest{Method: MethodPutMessageSecrets, MessageSecrets: items}, nil)
}

func (adapter *Adapter) PutMessageSecret(
	ctx context.Context,
	chat, sender types.JID,
	id types.MessageID,
	secret []byte,
) error {
	return adapter.mutate(ctx, InvokeRequest{
		Method: MethodPutMessageSecret, JID: JIDFromValue(chat), JID2: JIDFromValue(sender),
		MessageID: id, Data: cloneBytes(secret),
	}, nil)
}

func (adapter *Adapter) GetMessageSecret(
	ctx context.Context,
	chat, sender types.JID,
	id types.MessageID,
) ([]byte, types.JID, error) {
	response, err := adapter.call(ctx, InvokeRequest{
		Method: MethodGetMessageSecret, JID: JIDFromValue(chat), JID2: JIDFromValue(sender),
		MessageID: id,
	}, false)
	return cloneBytes(response.Data), response.JID.Value(), err
}

func (adapter *Adapter) PutPrivacyTokens(ctx context.Context, tokens ...store.PrivacyToken) error {
	items := make([]PrivacyToken, 0, len(tokens))
	for _, token := range tokens {
		items = append(items, PrivacyToken{
			User: JIDFromValue(token.User), Token: cloneBytes(token.Token),
			Timestamp: TimeFromValue(token.Timestamp),
		})
	}
	return adapter.mutate(ctx, InvokeRequest{Method: MethodPutPrivacyTokens, PrivacyTokens: items}, nil)
}

func (adapter *Adapter) GetPrivacyToken(
	ctx context.Context,
	user types.JID,
) (*store.PrivacyToken, error) {
	response, err := adapter.call(ctx, InvokeRequest{Method: MethodGetPrivacyToken, JID: JIDFromValue(user)}, false)
	if err != nil || response.PrivacyToken == nil {
		return nil, err
	}
	timestamp, err := response.PrivacyToken.Timestamp.Value()
	if err != nil {
		return nil, database.NewError(database.CodeIntegrity, "WhatsApp privacy-token response is invalid")
	}
	return &store.PrivacyToken{
		User: response.PrivacyToken.User.Value(), Token: cloneBytes(response.PrivacyToken.Token), Timestamp: timestamp,
	}, nil
}

func (adapter *Adapter) GetBufferedEvent(
	ctx context.Context,
	ciphertextHash [32]byte,
) (*store.BufferedEvent, error) {
	response, err := adapter.call(ctx, InvokeRequest{
		Method: MethodGetBufferedEvent, Data: append([]byte(nil), ciphertextHash[:]...),
	}, false)
	if err != nil || response.BufferedEvent == nil {
		return nil, err
	}
	insertTime, err := response.BufferedEvent.InsertTime.Value()
	if err != nil {
		return nil, database.NewError(database.CodeIntegrity, "WhatsApp buffered-event response is invalid")
	}
	serverTime, err := response.BufferedEvent.ServerTime.Value()
	if err != nil {
		return nil, database.NewError(database.CodeIntegrity, "WhatsApp buffered-event response is invalid")
	}
	return &store.BufferedEvent{
		Plaintext:  cloneBytes(response.BufferedEvent.Plaintext),
		InsertTime: insertTime, ServerTime: serverTime,
	}, nil
}

func (adapter *Adapter) PutBufferedEvent(
	ctx context.Context,
	ciphertextHash [32]byte,
	plaintext []byte,
	serverTimestamp time.Time,
) error {
	hash := append([]byte(nil), ciphertextHash[:]...)
	data := cloneBytes(plaintext)
	timestamp := TimeFromValue(serverTimestamp)
	return adapter.mutate(
		ctx,
		InvokeRequest{
			Method: MethodPutBufferedEvent, Data: hash, Data2: data, Timestamp: timestamp,
		},
		&DecryptionMutation{
			Method: MethodPutBufferedEvent, Data: hash, Data2: data, Timestamp: timestamp,
		},
	)
}

func (adapter *Adapter) DoDecryptionTxn(
	ctx context.Context,
	fn func(context.Context) error,
) error {
	if fn == nil {
		return database.NewError(database.CodeInvalid, "WhatsApp decryption transaction callback is missing")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	target, err := adapter.target("")
	if err != nil {
		return err
	}
	if collector := decryptionCollectorFrom(ctx); collector != nil {
		if collector.storeID != adapter.container.storeID || collector.deviceJID != target.DeviceJID {
			return database.NewError(
				database.CodeConflict,
				"nested WhatsApp decryption transaction target does not match the active lease",
			)
		}
		return fn(ctx)
	}
	// This local lock keeps Close and calls through this Container ordered. The
	// broker lease below is the authoritative serialization boundary shared by
	// every independently connected Container.
	adapter.container.transactionMu.Lock()
	defer adapter.container.transactionMu.Unlock()
	adapter.container.mu.RLock()
	closed := adapter.container.closed
	adapter.container.mu.RUnlock()
	if closed {
		return database.NewError(database.CodeUnavailable, "WhatsApp store client is closed")
	}
	var begun BeginDecryptionResponse
	err = adapter.container.client.CallWithOptions(
		ctx, Domain, Version, BeginDecryptionOperation,
		BeginDecryptionRequest{
			StoreID: adapter.container.storeID, DeviceJID: target.DeviceJID,
		},
		&begun, database.CallOptions{Mutation: true},
	)
	if err != nil {
		return err
	}
	if !begun.DecryptionLease.Valid() {
		return database.NewError(database.CodeIntegrity, "WhatsApp decryption lease response is invalid")
	}
	collector := &decryptionCollector{
		storeID: adapter.container.storeID, deviceJID: target.DeviceJID,
		leaseID: begun.DecryptionLease,
	}
	txnContext := context.WithValue(ctx, decryptionCollectorContextKey{}, collector)
	leaseActive := true
	abort := func() error {
		if !leaseActive {
			return nil
		}
		leaseActive = false
		abortContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		var response AbortDecryptionResponse
		abortErr := adapter.container.client.CallWithOptions(
			abortContext, Domain, Version, AbortDecryptionOperation,
			AbortDecryptionRequest{
				StoreID: adapter.container.storeID, DeviceJID: target.DeviceJID,
				DecryptionLease: begun.DecryptionLease,
			},
			&response, database.CallOptions{Mutation: true},
		)
		if abortErr != nil {
			return abortErr
		}
		if !response.Released {
			return database.NewError(database.CodeIntegrity, "WhatsApp decryption abort response is invalid")
		}
		return nil
	}
	defer func() {
		// This also releases the broker lease if the callback panics.
		_ = abort()
	}()
	if callbackErr := fn(txnContext); callbackErr != nil {
		return errors.Join(callbackErr, abort())
	}
	mutations := collector.snapshot()
	var response MutationResponse
	err = adapter.container.client.CallWithOptions(
		ctx, Domain, Version, DecryptionOperation,
		DecryptionRequest{
			StoreID: adapter.container.storeID, DeviceJID: target.DeviceJID,
			DecryptionLease: begun.DecryptionLease, Mutations: mutations,
		},
		&response, database.CallOptions{Mutation: true},
	)
	if err != nil {
		return errors.Join(err, abort())
	}
	leaseActive = false
	if !response.Applied {
		return database.NewError(database.CodeIntegrity, "WhatsApp decryption response is invalid")
	}
	return nil
}

func (adapter *Adapter) ClearBufferedEventPlaintext(
	ctx context.Context,
	ciphertextHash [32]byte,
) error {
	return adapter.mutate(ctx, InvokeRequest{
		Method: MethodClearBufferedEventPlaintext,
		Data:   append([]byte(nil), ciphertextHash[:]...),
	}, nil)
}

func (adapter *Adapter) DeleteOldBufferedHashes(ctx context.Context) error {
	return adapter.mutate(ctx, InvokeRequest{Method: MethodDeleteOldBufferedHashes}, nil)
}

func (adapter *Adapter) GetOutgoingEvent(
	ctx context.Context,
	chatJID, altChatJID types.JID,
	id types.MessageID,
) (string, []byte, error) {
	response, err := adapter.call(ctx, InvokeRequest{
		Method: MethodGetOutgoingEvent, JID: JIDFromValue(chatJID), JID2: JIDFromValue(altChatJID),
		MessageID: id,
	}, false)
	return response.Format(), cloneBytes(response.Data), err
}

func (response InvokeResponse) Format() string { return response.Text }

func (adapter *Adapter) AddOutgoingEvent(
	ctx context.Context,
	chatJID types.JID,
	id types.MessageID,
	format string,
	plaintext []byte,
) error {
	return adapter.mutate(ctx, InvokeRequest{
		Method: MethodAddOutgoingEvent, JID: JIDFromValue(chatJID), MessageID: id,
		Format: format, Data: cloneBytes(plaintext),
	}, nil)
}

func (adapter *Adapter) DeleteOldOutgoingEvents(ctx context.Context) error {
	return adapter.mutate(ctx, InvokeRequest{Method: MethodDeleteOldOutgoingEvents}, nil)
}

func (adapter *Adapter) PutManyLIDMappings(
	ctx context.Context,
	mappings []store.LIDMapping,
) error {
	items := make([]LIDMapping, 0, len(mappings))
	for _, mapping := range mappings {
		items = append(items, LIDMapping{LID: JIDFromValue(mapping.LID), PN: JIDFromValue(mapping.PN)})
	}
	return adapter.mutate(ctx, InvokeRequest{Method: MethodPutManyLIDMappings, LIDMappings: items}, nil)
}

func (adapter *Adapter) PutLIDMapping(ctx context.Context, lid, jid types.JID) error {
	return adapter.mutate(ctx, InvokeRequest{
		Method: MethodPutLIDMapping, JID: JIDFromValue(lid), JID2: JIDFromValue(jid),
	}, nil)
}

func (adapter *Adapter) GetPNForLID(ctx context.Context, lid types.JID) (types.JID, error) {
	response, err := adapter.call(ctx, InvokeRequest{Method: MethodGetPNForLID, JID: JIDFromValue(lid)}, false)
	return response.JID.Value(), err
}

func (adapter *Adapter) GetLIDForPN(ctx context.Context, pn types.JID) (types.JID, error) {
	response, err := adapter.call(ctx, InvokeRequest{Method: MethodGetLIDForPN, JID: JIDFromValue(pn)}, false)
	return response.JID.Value(), err
}

func (adapter *Adapter) GetManyLIDsForPNs(
	ctx context.Context,
	pns []types.JID,
) (map[types.JID]types.JID, error) {
	items := make([]JID, 0, len(pns))
	for _, pn := range pns {
		items = append(items, JIDFromValue(pn))
	}
	response, err := adapter.call(ctx, InvokeRequest{Method: MethodGetManyLIDsForPNs, JIDs: items}, false)
	if err != nil {
		return nil, err
	}
	result := make(map[types.JID]types.JID, len(response.LIDMappings))
	for _, mapping := range response.LIDMappings {
		result[mapping.PN.Value()] = mapping.LID.Value()
	}
	return result, nil
}

type decryptionCollectorContextKey struct{}

type decryptionCollector struct {
	mu        sync.Mutex
	storeID   database.StoreID
	deviceJID JID
	leaseID   DecryptionLeaseID
	mutations []DecryptionMutation
}

func decryptionCollectorFrom(ctx context.Context) *decryptionCollector {
	if ctx == nil {
		return nil
	}
	collector, _ := ctx.Value(decryptionCollectorContextKey{}).(*decryptionCollector)
	return collector
}

func (collector *decryptionCollector) append(mutation DecryptionMutation) {
	mutation.Data = cloneBytes(mutation.Data)
	mutation.Data2 = cloneBytes(mutation.Data2)
	collector.mu.Lock()
	collector.mutations = append(collector.mutations, mutation)
	collector.mu.Unlock()
}

func (collector *decryptionCollector) snapshot() []DecryptionMutation {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	result := make([]DecryptionMutation, len(collector.mutations))
	copy(result, collector.mutations)
	for index := range result {
		result[index].Data = cloneBytes(result[index].Data)
		result[index].Data2 = cloneBytes(result[index].Data2)
	}
	return result
}

func (collector *decryptionCollector) identity(address string, key [32]byte) (bool, bool) {
	mutations := collector.snapshot()
	for index := len(mutations) - 1; index >= 0; index-- {
		mutation := mutations[index]
		switch mutation.Method {
		case MethodPutIdentity:
			if mutation.Address == address {
				return bytes.Equal(mutation.Data, key[:]), true
			}
		case MethodDeleteIdentity:
			if mutation.Address == address {
				return true, true
			}
		case MethodDeleteAllIdentities:
			if strings.HasPrefix(address, mutation.Phone+":") {
				return true, true
			}
		}
	}
	return false, false
}

func (collector *decryptionCollector) session(address string) ([]byte, bool) {
	mutations := collector.snapshot()
	for index := len(mutations) - 1; index >= 0; index-- {
		mutation := mutations[index]
		switch mutation.Method {
		case MethodPutSession:
			if mutation.Address == address {
				return cloneBytes(mutation.Data), true
			}
		case MethodDeleteSession:
			if mutation.Address == address {
				return nil, true
			}
		case MethodDeleteAllSessions:
			if strings.HasPrefix(address, mutation.Phone+":") {
				return nil, true
			}
		}
	}
	return nil, false
}

func (collector *decryptionCollector) preKeyRemoved(id uint32) bool {
	mutations := collector.snapshot()
	for index := len(mutations) - 1; index >= 0; index-- {
		if mutations[index].Method == MethodRemovePreKey && mutations[index].KeyID == id {
			return true
		}
	}
	return false
}

func (collector *decryptionCollector) senderKey(group, user string) ([]byte, bool) {
	mutations := collector.snapshot()
	for index := len(mutations) - 1; index >= 0; index-- {
		mutation := mutations[index]
		if mutation.Method == MethodPutSenderKey && mutation.Group == group && mutation.Address == user {
			return cloneBytes(mutation.Data), true
		}
	}
	return nil, false
}

func decodePreKeys(items []PreKey) ([]*keys.PreKey, error) {
	result := make([]*keys.PreKey, 0, len(items))
	for index := range items {
		preKey, err := PreKeyFromDTO(&items[index])
		if err != nil {
			return nil, database.NewError(database.CodeIntegrity, "WhatsApp pre-key response is invalid")
		}
		result = append(result, preKey)
	}
	return result, nil
}

func appStateKeyToDTO(key store.AppStateSyncKey) AppStateSyncKey {
	return AppStateSyncKey{
		Data: cloneBytes(key.Data), Fingerprint: cloneBytes(key.Fingerprint), Timestamp: key.Timestamp,
	}
}

func appStateKeyFromDTO(key AppStateSyncKey) store.AppStateSyncKey {
	return store.AppStateSyncKey{
		Data: cloneBytes(key.Data), Fingerprint: cloneBytes(key.Fingerprint), Timestamp: key.Timestamp,
	}
}

func contactInfoFromDTO(info ContactInfo) types.ContactInfo {
	return types.ContactInfo{
		Found: info.Found, FirstName: info.FirstName, FullName: info.FullName,
		PushName: info.PushName, BusinessName: info.BusinessName, RedactedPhone: info.RedactedPhone,
	}
}

func cloneByteSlices(items [][]byte) [][]byte {
	if items == nil {
		return nil
	}
	result := make([][]byte, len(items))
	for index := range items {
		result[index] = cloneBytes(items[index])
	}
	return result
}
