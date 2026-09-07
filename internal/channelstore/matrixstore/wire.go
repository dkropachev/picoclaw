// Package matrixstore provides the typed broker client used by the Matrix
// runtime. It deliberately contains no SQL, provider, path, or DSN concepts.
package matrixstore

import (
	"encoding/base64"
	"time"

	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	Domain  = "channel-matrix"
	Version = 1

	PreflightOperation = "preflight"
	ResolveOperation   = "resolve"
)

const (
	opFlush                          = "crypto.flush"
	opPutAccount                     = "crypto.put-account"
	opGetAccount                     = "crypto.get-account"
	opAddSession                     = "crypto.add-session"
	opHasSession                     = "crypto.has-session"
	opGetSessions                    = "crypto.get-sessions"
	opGetLatestSession               = "crypto.get-latest-session"
	opGetNewestSessionCreationTS     = "crypto.get-newest-session-creation-ts"
	opUpdateSession                  = "crypto.update-session"
	opDeleteSession                  = "crypto.delete-session"
	opPutOlmHash                     = "crypto.put-olm-hash"
	opGetOlmHash                     = "crypto.get-olm-hash"
	opDeleteOldOlmHashes             = "crypto.delete-old-olm-hashes"
	opPutGroupSession                = "crypto.put-group-session"
	opGetGroupSession                = "crypto.get-group-session"
	opRedactGroupSession             = "crypto.redact-group-session"
	opRedactGroupSessions            = "crypto.redact-group-sessions"
	opRedactExpiredGroupSessions     = "crypto.redact-expired-group-sessions"
	opRedactOutdatedGroupSessions    = "crypto.redact-outdated-group-sessions"
	opPutWithheldGroupSession        = "crypto.put-withheld-group-session"
	opGetWithheldGroupSession        = "crypto.get-withheld-group-session"
	opGetGroupSessionsForRoom        = "crypto.get-group-sessions-for-room"
	opGetAllGroupSessions            = "crypto.get-all-group-sessions"
	opGetGroupSessionsWithoutBackup  = "crypto.get-group-sessions-without-backup"
	opAddOutboundGroupSession        = "crypto.add-outbound-group-session"
	opUpdateOutboundGroupSession     = "crypto.update-outbound-group-session"
	opGetOutboundGroupSession        = "crypto.get-outbound-group-session"
	opRemoveOutboundGroupSession     = "crypto.remove-outbound-group-session"
	opMarkOutboundGroupSessionShared = "crypto.mark-outbound-group-session-shared"
	opIsOutboundGroupSessionShared   = "crypto.is-outbound-group-session-shared"
	opValidateMessageIndex           = "crypto.validate-message-index"
	opGetDevices                     = "crypto.get-devices"
	opGetDevice                      = "crypto.get-device"
	opPutDevice                      = "crypto.put-device"
	opPutDevices                     = "crypto.put-devices"
	opFindDeviceByKey                = "crypto.find-device-by-key"
	opFilterTrackedUsers             = "crypto.filter-tracked-users"
	opMarkTrackedUsersOutdated       = "crypto.mark-tracked-users-outdated"
	opGetOutdatedTrackedUsers        = "crypto.get-outdated-tracked-users"
	opPutCrossSigningKey             = "crypto.put-cross-signing-key"
	opGetCrossSigningKeys            = "crypto.get-cross-signing-keys"
	opPutSignature                   = "crypto.put-signature"
	opIsKeySignedBy                  = "crypto.is-key-signed-by"
	opDropSignaturesByKey            = "crypto.drop-signatures-by-key"
	opGetSignaturesForKeyBy          = "crypto.get-signatures-for-key-by"
	opPutSecret                      = "crypto.put-secret"
	opGetSecret                      = "crypto.get-secret"
	opDeleteSecret                   = "crypto.delete-secret"
	opSaveFilterID                   = "sync.save-filter-id"
	opLoadFilterID                   = "sync.load-filter-id"
	opSaveNextBatch                  = "sync.save-next-batch"
	opLoadNextBatch                  = "sync.load-next-batch"
	opIsInRoom                       = "state.is-in-room"
	opIsInvited                      = "state.is-invited"
	opIsMembership                   = "state.is-membership"
	opGetMember                      = "state.get-member"
	opTryGetMember                   = "state.try-get-member"
	opSetMembership                  = "state.set-membership"
	opSetMember                      = "state.set-member"
	opIsConfusableName               = "state.is-confusable-name"
	opClearCachedMembers             = "state.clear-cached-members"
	opReplaceCachedMembers           = "state.replace-cached-members"
	opSetPowerLevels                 = "state.set-power-levels"
	opGetPowerLevels                 = "state.get-power-levels"
	opSetCreate                      = "state.set-create"
	opGetCreate                      = "state.get-create"
	opGetJoinRules                   = "state.get-join-rules"
	opSetJoinRules                   = "state.set-join-rules"
	opHasFetchedMembers              = "state.has-fetched-members"
	opMarkMembersFetched             = "state.mark-members-fetched"
	opGetAllMembers                  = "state.get-all-members"
	opSetEncryptionEvent             = "state.set-encryption-event"
	opGetEncryptionEvent             = "state.get-encryption-event"
	opIsEncrypted                    = "state.is-encrypted"
	opFindSharedRooms                = "state.find-shared-rooms"
	opGetRoomJoinedOrInvitedMembers  = "state.get-room-joined-or-invited-members"
)

type StoreTarget struct {
	StoreID database.StoreID `json:"store_id"`
}

type ResolveRequest struct {
	ChannelName string `json:"channel_name"`
}

type ResolveResponse struct {
	StoreID database.StoreID `json:"store_id"`
}

type PreflightResponse struct {
	Ready bool `json:"ready"`
}

// GroupSessionSnapshotID is an opaque broker-issued handle for one immutable
// group-session result set. Clients must return it unchanged while paging.
type GroupSessionSnapshotID string

// Valid reports whether the snapshot ID has the one canonical wire shape used
// by the broker. The value remains opaque to clients.
func (snapshotID GroupSessionSnapshotID) Valid() bool {
	const rawLength = 24
	if len(snapshotID) != base64.RawURLEncoding.EncodedLen(rawLength) {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(string(snapshotID))
	return err == nil && len(raw) == rawLength &&
		base64.RawURLEncoding.EncodeToString(raw) == string(snapshotID)
}

// Request is the closed, Matrix-specific operation envelope. Fields represent
// Matrix identities and typed records only; it cannot express SQL text.
type Request struct {
	StoreID  database.StoreID `json:"store_id"`
	DeviceID id.DeviceID      `json:"device_id,omitempty"`

	RoomID          id.RoomID            `json:"room_id,omitempty"`
	UserID          id.UserID            `json:"user_id,omitempty"`
	TargetDeviceID  id.DeviceID          `json:"target_device_id,omitempty"`
	SenderKey       id.SenderKey         `json:"sender_key,omitempty"`
	IdentityKey     id.IdentityKey       `json:"identity_key,omitempty"`
	SessionID       id.SessionID         `json:"session_id,omitempty"`
	EventID         id.EventID           `json:"event_id,omitempty"`
	BackupVersion   id.KeyBackupVersion  `json:"backup_version,omitempty"`
	SecretName      id.Secret            `json:"secret_name,omitempty"`
	CrossUsage      id.CrossSigningUsage `json:"cross_usage,omitempty"`
	SignedKey       id.Ed25519           `json:"signed_key,omitempty"`
	SignerUserID    id.UserID            `json:"signer_user_id,omitempty"`
	SignerKey       id.Ed25519           `json:"signer_key,omitempty"`
	CrossSigningKey id.Ed25519           `json:"cross_signing_key,omitempty"`

	Index       uint                   `json:"index,omitempty"`
	SnapshotID  GroupSessionSnapshotID `json:"snapshot_id,omitempty"`
	Cursor      int                    `json:"cursor,omitempty"`
	Limit       int                    `json:"limit,omitempty"`
	Timestamp   int64                  `json:"timestamp,omitempty"`
	At          time.Time              `json:"at,omitempty"`
	Reason      string                 `json:"reason,omitempty"`
	Value       string                 `json:"value,omitempty"`
	FilterID    string                 `json:"filter_id,omitempty"`
	NextBatch   string                 `json:"next_batch,omitempty"`
	Name        string                 `json:"name,omitempty"`
	Signature   string                 `json:"signature,omitempty"`
	MessageHash []byte                 `json:"message_hash,omitempty"`

	Membership      event.Membership   `json:"membership,omitempty"`
	Memberships     []event.Membership `json:"memberships,omitempty"`
	OnlyMemberships []event.Membership `json:"only_memberships,omitempty"`

	Account              *AccountRecord                     `json:"account,omitempty"`
	Session              *OlmSessionRecord                  `json:"session,omitempty"`
	GroupSession         *InboundGroupSessionRecord         `json:"group_session,omitempty"`
	OutboundGroupSession *OutboundGroupSessionRecord        `json:"outbound_group_session,omitempty"`
	Withheld             *event.RoomKeyWithheldEventContent `json:"withheld,omitempty"`
	Device               *id.Device                         `json:"device,omitempty"`
	Devices              map[id.DeviceID]*id.Device         `json:"devices,omitempty"`
	Users                []id.UserID                        `json:"users,omitempty"`

	Member       *event.MemberEventContent      `json:"member,omitempty"`
	MemberEvents []MemberEventRecord            `json:"member_events,omitempty"`
	PowerLevels  *event.PowerLevelsEventContent `json:"power_levels,omitempty"`
	Create       *event.Event                   `json:"create,omitempty"`
	JoinRules    *event.JoinRulesEventContent   `json:"join_rules,omitempty"`
	Encryption   *event.EncryptionEventContent  `json:"encryption,omitempty"`
}

type Response struct {
	OK         bool                   `json:"ok,omitempty"`
	At         time.Time              `json:"at,omitempty"`
	Value      string                 `json:"value,omitempty"`
	Count      int64                  `json:"count,omitempty"`
	SnapshotID GroupSessionSnapshotID `json:"snapshot_id,omitempty"`
	NextCursor int                    `json:"next_cursor,omitempty"`
	More       bool                   `json:"more,omitempty"`

	Account              *AccountRecord                     `json:"account,omitempty"`
	Session              *OlmSessionRecord                  `json:"session,omitempty"`
	Sessions             []OlmSessionRecord                 `json:"sessions,omitempty"`
	GroupSession         *InboundGroupSessionRecord         `json:"group_session,omitempty"`
	GroupSessions        []InboundGroupSessionRecord        `json:"group_sessions,omitempty"`
	OutboundGroupSession *OutboundGroupSessionRecord        `json:"outbound_group_session,omitempty"`
	Withheld             *event.RoomKeyWithheldEventContent `json:"withheld,omitempty"`
	SessionIDs           []id.SessionID                     `json:"session_ids,omitempty"`

	Device           *id.Device                                  `json:"device,omitempty"`
	Devices          map[id.DeviceID]*id.Device                  `json:"devices,omitempty"`
	Users            []id.UserID                                 `json:"users,omitempty"`
	CrossSigningKeys map[id.CrossSigningUsage]id.CrossSigningKey `json:"cross_signing_keys,omitempty"`
	Signatures       map[id.Ed25519]string                       `json:"signatures,omitempty"`

	Member      *event.MemberEventContent               `json:"member,omitempty"`
	Members     map[id.UserID]*event.MemberEventContent `json:"members,omitempty"`
	PowerLevels *event.PowerLevelsEventContent          `json:"power_levels,omitempty"`
	Create      *event.Event                            `json:"create,omitempty"`
	JoinRules   *event.JoinRulesEventContent            `json:"join_rules,omitempty"`
	Encryption  *event.EncryptionEventContent           `json:"encryption,omitempty"`
	RoomIDs     []id.RoomID                             `json:"room_ids,omitempty"`
}

type MemberEventRecord struct {
	UserID  id.UserID                `json:"user_id"`
	Content event.MemberEventContent `json:"content"`
}

type AccountRecord struct {
	Pickle           []byte              `json:"pickle"`
	Shared           bool                `json:"shared"`
	KeyBackupVersion id.KeyBackupVersion `json:"key_backup_version,omitempty"`
}

type OlmSessionRecord struct {
	Pickle            []byte    `json:"pickle"`
	CreationTime      time.Time `json:"creation_time"`
	LastEncryptedTime time.Time `json:"last_encrypted_time"`
	LastDecryptedTime time.Time `json:"last_decrypted_time"`
}

type InboundGroupSessionRecord struct {
	Pickle           []byte               `json:"pickle"`
	SigningKey       id.Ed25519           `json:"signing_key"`
	SenderKey        id.Curve25519        `json:"sender_key"`
	RoomID           id.RoomID            `json:"room_id"`
	ForwardingChains []string             `json:"forwarding_chains"`
	RatchetSafety    crypto.RatchetSafety `json:"ratchet_safety"`
	ReceivedAt       time.Time            `json:"received_at"`
	MaxAge           int64                `json:"max_age"`
	MaxMessages      int                  `json:"max_messages"`
	IsScheduled      bool                 `json:"is_scheduled"`
	KeyBackupVersion id.KeyBackupVersion  `json:"key_backup_version,omitempty"`
	KeySource        id.KeySource         `json:"key_source,omitempty"`
}

type OutboundGroupSessionRecord struct {
	Pickle            []byte        `json:"pickle"`
	RoomID            id.RoomID     `json:"room_id"`
	Shared            bool          `json:"shared"`
	MaxMessages       int           `json:"max_messages"`
	MessageCount      int           `json:"message_count"`
	MaxAge            time.Duration `json:"max_age"`
	CreationTime      time.Time     `json:"creation_time"`
	LastEncryptedTime time.Time     `json:"last_encrypted_time"`
}
