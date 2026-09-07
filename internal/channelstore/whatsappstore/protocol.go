// Package whatsappstore defines the typed broker protocol used by the native
// WhatsApp channel. The protocol intentionally exposes WhatsApp domain values
// only: provider handles, paths, statements, and transaction capabilities stay
// in the broker process.
package whatsappstore

import (
	"context"
	"strings"

	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	Domain  = "channel-whatsapp"
	Version = 1

	ResolveOperation         = "resolve-store"
	PreflightOperation       = "preflight"
	InvokeOperation          = "invoke"
	BeginDecryptionOperation = "begin-decryption"
	DecryptionOperation      = "commit-decryption"
	AbortDecryptionOperation = "abort-decryption"
)

// DecryptionLeaseID is an opaque broker capability that identifies a retained
// decryption lease. It authorizes typed reads for exactly one store and device;
// it never contains or represents a provider transaction handle.
type DecryptionLeaseID string

// Valid reports whether the lease has the broker-issued wire shape. Possession
// and target binding are validated separately by the broker.
func (leaseID DecryptionLeaseID) Valid() bool {
	if len(leaseID) != 64 {
		return false
	}
	for _, character := range leaseID {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

// Method is the closed set of whatsmeow store calls accepted by the broker.
type Method string

const (
	MethodGetFirstDevice Method = "device.get-first"
	MethodPutDevice      Method = "device.put"
	MethodDeleteDevice   Method = "device.delete"

	MethodPutIdentity         Method = "identity.put"
	MethodDeleteAllIdentities Method = "identity.delete-all"
	MethodDeleteIdentity      Method = "identity.delete"
	MethodIsTrustedIdentity   Method = "identity.is-trusted"

	MethodGetSession        Method = "session.get"
	MethodHasSession        Method = "session.has"
	MethodGetManySessions   Method = "session.get-many"
	MethodPutSession        Method = "session.put"
	MethodPutManySessions   Method = "session.put-many"
	MethodDeleteAllSessions Method = "session.delete-all"
	MethodDeleteSession     Method = "session.delete"
	MethodMigratePNToLID    Method = "session.migrate-pn-to-lid"

	MethodGetOrGenPreKeys       Method = "prekey.get-or-generate"
	MethodGenOnePreKey          Method = "prekey.generate-one"
	MethodGetPreKey             Method = "prekey.get"
	MethodRemovePreKey          Method = "prekey.remove"
	MethodMarkPreKeysAsUploaded Method = "prekey.mark-uploaded"
	MethodUploadedPreKeyCount   Method = "prekey.uploaded-count"

	MethodPutSenderKey Method = "sender-key.put"
	MethodGetSenderKey Method = "sender-key.get"

	MethodPutAppStateSyncKey      Method = "app-state-key.put"
	MethodGetAppStateSyncKey      Method = "app-state-key.get"
	MethodGetLatestAppStateKeyID  Method = "app-state-key.get-latest-id"
	MethodGetAllAppStateSyncKeys  Method = "app-state-key.get-all"
	MethodPutAppStateVersion      Method = "app-state.put-version"
	MethodGetAppStateVersion      Method = "app-state.get-version"
	MethodDeleteAppStateVersion   Method = "app-state.delete-version"
	MethodPutAppStateMutationMACs Method = "app-state.put-macs"
	MethodDeleteAppStateMACs      Method = "app-state.delete-macs"
	MethodGetAppStateMutationMAC  Method = "app-state.get-mac"

	MethodPutPushName           Method = "contact.put-push-name"
	MethodPutBusinessName       Method = "contact.put-business-name"
	MethodPutContactName        Method = "contact.put-name"
	MethodPutAllContactNames    Method = "contact.put-all-names"
	MethodPutManyRedactedPhones Method = "contact.put-redacted-phones"
	MethodGetContact            Method = "contact.get"
	MethodGetAllContacts        Method = "contact.get-all"

	MethodPutMutedUntil   Method = "chat.put-muted-until"
	MethodPutPinned       Method = "chat.put-pinned"
	MethodPutArchived     Method = "chat.put-archived"
	MethodGetChatSettings Method = "chat.get-settings"

	MethodPutMessageSecrets Method = "message-secret.put-many"
	MethodPutMessageSecret  Method = "message-secret.put"
	MethodGetMessageSecret  Method = "message-secret.get"

	MethodPutPrivacyTokens Method = "privacy-token.put-many"
	MethodGetPrivacyToken  Method = "privacy-token.get"

	MethodGetBufferedEvent            Method = "event-buffer.get"
	MethodPutBufferedEvent            Method = "event-buffer.put"
	MethodClearBufferedEventPlaintext Method = "event-buffer.clear-plaintext"
	MethodDeleteOldBufferedHashes     Method = "event-buffer.delete-old"
	MethodGetOutgoingEvent            Method = "retry-buffer.get"
	MethodAddOutgoingEvent            Method = "retry-buffer.add"
	MethodDeleteOldOutgoingEvents     Method = "retry-buffer.delete-old"

	MethodPutManyLIDMappings Method = "lid.put-many"
	MethodPutLIDMapping      Method = "lid.put"
	MethodGetPNForLID        Method = "lid.get-pn"
	MethodGetLIDForPN        Method = "lid.get-lid"
	MethodGetManyLIDsForPNs  Method = "lid.get-many"
)

type ResolveRequest struct {
	ChannelName string `json:"channel_name"`
}

type ResolveResponse struct {
	StoreID database.StoreID `json:"store_id"`
}

type Target struct {
	StoreID database.StoreID `json:"store_id"`
}

type ReadyResponse struct {
	Ready bool `json:"ready"`
}

// JID is the detached wire representation of types.JID. Keeping every field
// explicit avoids depending on its textual parser or JSON implementation.
type JID struct {
	User       string `json:"user,omitempty"`
	RawAgent   uint8  `json:"raw_agent,omitempty"`
	Device     uint16 `json:"device,omitempty"`
	Integrator uint16 `json:"integrator,omitempty"`
	Server     string `json:"server,omitempty"`
}

type Time struct {
	Set         bool  `json:"set,omitempty"`
	UTC         bool  `json:"utc,omitempty"`
	Seconds     int64 `json:"seconds,omitempty"`
	Nanoseconds int32 `json:"nanoseconds,omitempty"`
}

type Account struct {
	Details             []byte `json:"details,omitempty"`
	AccountSignature    []byte `json:"account_signature,omitempty"`
	AccountSignatureKey []byte `json:"account_signature_key,omitempty"`
	DeviceSignature     []byte `json:"device_signature,omitempty"`
}

// Device is an explicit DTO rather than a serialization of store.Device. Store
// interfaces and loggers are reconstructed locally and never cross the wire.
type Device struct {
	NoisePrivate          []byte   `json:"noise_private"`
	IdentityPrivate       []byte   `json:"identity_private"`
	SignedPreKeyPrivate   []byte   `json:"signed_pre_key_private"`
	SignedPreKeyID        uint32   `json:"signed_pre_key_id"`
	SignedPreKeySignature []byte   `json:"signed_pre_key_signature"`
	RegistrationID        uint32   `json:"registration_id"`
	AdvSecretKey          []byte   `json:"adv_secret_key,omitempty"`
	ID                    *JID     `json:"id,omitempty"`
	LID                   JID      `json:"lid"`
	Account               *Account `json:"account,omitempty"`
	Platform              string   `json:"platform,omitempty"`
	BusinessName          string   `json:"business_name,omitempty"`
	PushName              string   `json:"push_name,omitempty"`
	LIDMigrationTimestamp int64    `json:"lid_migration_timestamp,omitempty"`
	FacebookUUID          string   `json:"facebook_uuid,omitempty"`
	Initialized           bool     `json:"initialized,omitempty"`
}

type Session struct {
	Address string `json:"address"`
	Data    []byte `json:"data,omitempty"`
}

type PreKey struct {
	Private   []byte `json:"private"`
	ID        uint32 `json:"id"`
	Signature []byte `json:"signature,omitempty"`
}

type AppStateSyncKey struct {
	Data        []byte `json:"data,omitempty"`
	Fingerprint []byte `json:"fingerprint,omitempty"`
	Timestamp   int64  `json:"timestamp"`
}

type AppStateMutationMAC struct {
	IndexMAC []byte `json:"index_mac"`
	ValueMAC []byte `json:"value_mac"`
}

type ContactEntry struct {
	JID       JID    `json:"jid"`
	FirstName string `json:"first_name,omitempty"`
	FullName  string `json:"full_name,omitempty"`
}

type RedactedPhoneEntry struct {
	JID           JID    `json:"jid"`
	RedactedPhone string `json:"redacted_phone,omitempty"`
}

type ContactInfo struct {
	JID           JID    `json:"jid,omitempty"`
	Found         bool   `json:"found,omitempty"`
	FirstName     string `json:"first_name,omitempty"`
	FullName      string `json:"full_name,omitempty"`
	PushName      string `json:"push_name,omitempty"`
	BusinessName  string `json:"business_name,omitempty"`
	RedactedPhone string `json:"redacted_phone,omitempty"`
}

type ChatSettings struct {
	Found      bool `json:"found,omitempty"`
	MutedUntil Time `json:"muted_until"`
	Pinned     bool `json:"pinned,omitempty"`
	Archived   bool `json:"archived,omitempty"`
}

type MessageSecret struct {
	Chat   JID    `json:"chat"`
	Sender JID    `json:"sender"`
	ID     string `json:"id"`
	Secret []byte `json:"secret,omitempty"`
}

type PrivacyToken struct {
	User      JID    `json:"user"`
	Token     []byte `json:"token,omitempty"`
	Timestamp Time   `json:"timestamp"`
}

type BufferedEvent struct {
	Plaintext  []byte `json:"plaintext,omitempty"`
	InsertTime Time   `json:"insert_time"`
	ServerTime Time   `json:"server_time"`
}

type LIDMapping struct {
	LID JID `json:"lid"`
	PN  JID `json:"pn"`
}

// InvokeRequest is a closed, domain-specific argument envelope. Operation
// semantics are selected by Method; no field can contain a provider query.
type InvokeRequest struct {
	StoreID         database.StoreID  `json:"store_id"`
	DeviceJID       JID               `json:"device_jid,omitempty"`
	Method          Method            `json:"method"`
	DecryptionLease DecryptionLeaseID `json:"decryption_lease,omitempty"`

	Device         *Device               `json:"device,omitempty"`
	Address        string                `json:"address,omitempty"`
	Addresses      []string              `json:"addresses,omitempty"`
	Phone          string                `json:"phone,omitempty"`
	Group          string                `json:"group,omitempty"`
	Name           string                `json:"name,omitempty"`
	Text           string                `json:"text,omitempty"`
	Text2          string                `json:"text2,omitempty"`
	MessageID      string                `json:"message_id,omitempty"`
	Format         string                `json:"format,omitempty"`
	Data           []byte                `json:"data,omitempty"`
	Data2          []byte                `json:"data2,omitempty"`
	Count          uint32                `json:"count,omitempty"`
	KeyID          uint32                `json:"key_id,omitempty"`
	Version        uint64                `json:"version,omitempty"`
	Flag           bool                  `json:"flag,omitempty"`
	JID            JID                   `json:"jid,omitempty"`
	JID2           JID                   `json:"jid2,omitempty"`
	JIDs           []JID                 `json:"jids,omitempty"`
	Timestamp      Time                  `json:"timestamp,omitempty"`
	Sessions       []Session             `json:"sessions,omitempty"`
	AppStateKey    AppStateSyncKey       `json:"app_state_key,omitempty"`
	Mutations      []AppStateMutationMAC `json:"mutations,omitempty"`
	IndexMACs      [][]byte              `json:"index_macs,omitempty"`
	Contacts       []ContactEntry        `json:"contacts,omitempty"`
	RedactedPhones []RedactedPhoneEntry  `json:"redacted_phones,omitempty"`
	MessageSecrets []MessageSecret       `json:"message_secrets,omitempty"`
	PrivacyTokens  []PrivacyToken        `json:"privacy_tokens,omitempty"`
	LIDMappings    []LIDMapping          `json:"lid_mappings,omitempty"`
}

type InvokeResponse struct {
	OK            bool              `json:"ok,omitempty"`
	Found         bool              `json:"found,omitempty"`
	Flag          bool              `json:"flag,omitempty"`
	Text          string            `json:"text,omitempty"`
	Data          []byte            `json:"data,omitempty"`
	Data2         []byte            `json:"data2,omitempty"`
	Count         int               `json:"count,omitempty"`
	Version       uint64            `json:"version,omitempty"`
	Device        *Device           `json:"device,omitempty"`
	JID           JID               `json:"jid,omitempty"`
	PreKey        *PreKey           `json:"pre_key,omitempty"`
	PreKeys       []PreKey          `json:"pre_keys,omitempty"`
	Sessions      []Session         `json:"sessions,omitempty"`
	AppStateKey   *AppStateSyncKey  `json:"app_state_key,omitempty"`
	AppStateKeys  []AppStateSyncKey `json:"app_state_keys,omitempty"`
	Contact       ContactInfo       `json:"contact,omitempty"`
	Contacts      []ContactInfo     `json:"contacts,omitempty"`
	ChatSettings  ChatSettings      `json:"chat_settings,omitempty"`
	PrivacyToken  *PrivacyToken     `json:"privacy_token,omitempty"`
	BufferedEvent *BufferedEvent    `json:"buffered_event,omitempty"`
	LIDMappings   []LIDMapping      `json:"lid_mappings,omitempty"`
}

// DecryptionMutation is limited to the writes performed by the pinned Signal
// decrypt path. The client additionally maintains read-your-writes overlays
// while collecting these mutations.
type DecryptionMutation struct {
	Method    Method `json:"method"`
	Address   string `json:"address,omitempty"`
	Phone     string `json:"phone,omitempty"`
	Group     string `json:"group,omitempty"`
	Data      []byte `json:"data,omitempty"`
	Data2     []byte `json:"data2,omitempty"`
	KeyID     uint32 `json:"key_id,omitempty"`
	Timestamp Time   `json:"timestamp,omitempty"`
}

type DecryptionRequest struct {
	StoreID         database.StoreID     `json:"store_id"`
	DeviceJID       JID                  `json:"device_jid"`
	DecryptionLease DecryptionLeaseID    `json:"decryption_lease"`
	Mutations       []DecryptionMutation `json:"mutations"`
}

type BeginDecryptionRequest struct {
	StoreID   database.StoreID `json:"store_id"`
	DeviceJID JID              `json:"device_jid"`
}

type BeginDecryptionResponse struct {
	DecryptionLease DecryptionLeaseID `json:"decryption_lease"`
	ExpiresAt       Time              `json:"expires_at"`
}

type AbortDecryptionRequest struct {
	StoreID         database.StoreID  `json:"store_id"`
	DeviceJID       JID               `json:"device_jid"`
	DecryptionLease DecryptionLeaseID `json:"decryption_lease"`
}

type AbortDecryptionResponse struct {
	Released bool `json:"released"`
}

type MutationResponse struct {
	Applied bool `json:"applied"`
}

// ResolveStore asks the trusted broker catalog for the WhatsApp store assigned
// to channelName. Runtime callers never derive a physical path.
func ResolveStore(
	ctx context.Context,
	client *database.Client,
	channelName string,
) (database.StoreID, error) {
	if client == nil {
		return "", database.NewError(database.CodeUnavailable, "WhatsApp database broker client is unavailable")
	}
	if strings.TrimSpace(channelName) == "" || channelName != strings.TrimSpace(channelName) ||
		strings.ContainsRune(channelName, 0) {
		return "", database.NewError(database.CodeInvalid, "WhatsApp channel identity is invalid")
	}
	var response ResolveResponse
	if err := client.Call(
		ctx, Domain, Version, ResolveOperation,
		ResolveRequest{ChannelName: channelName}, &response,
	); err != nil {
		return "", err
	}
	if !response.StoreID.Valid() || !strings.HasPrefix(string(response.StoreID), "channel/whatsapp/") {
		return "", database.NewError(database.CodeIntegrity, "WhatsApp store identity is invalid")
	}
	return response.StoreID, nil
}
