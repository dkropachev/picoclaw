package matrixstore

import (
	"context"
	"fmt"
	"time"

	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func (store *Store) Flush(ctx context.Context) error {
	return store.call(ctx, opFlush, Request{}, &Response{}, true)
}

func (store *Store) PutAccount(ctx context.Context, account *crypto.OlmAccount) error {
	record, err := EncodeAccount(account, store.pickleKey)
	if err != nil {
		return err
	}
	if record == nil {
		return fmt.Errorf("Matrix Olm account is nil")
	}
	return store.call(ctx, opPutAccount, Request{Account: record}, &Response{}, true)
}

func (store *Store) GetAccount(ctx context.Context) (*crypto.OlmAccount, error) {
	var response Response
	if err := store.call(ctx, opGetAccount, Request{}, &response, false); err != nil {
		return nil, err
	}
	return DecodeAccount(response.Account, store.pickleKey)
}

func (store *Store) AddSession(
	ctx context.Context,
	key id.SenderKey,
	session *crypto.OlmSession,
) error {
	record, err := EncodeOlmSession(session, store.pickleKey)
	if err != nil {
		return err
	}
	if record == nil {
		return fmt.Errorf("Matrix Olm session is nil")
	}
	return store.call(ctx, opAddSession, Request{SenderKey: key, Session: record}, &Response{}, true)
}

func (store *Store) HasSession(ctx context.Context, key id.SenderKey) bool {
	var response Response
	return store.call(ctx, opHasSession, Request{SenderKey: key}, &response, false) == nil && response.OK
}

func (store *Store) GetSessions(ctx context.Context, key id.SenderKey) (crypto.OlmSessionList, error) {
	var response Response
	if err := store.call(ctx, opGetSessions, Request{SenderKey: key}, &response, false); err != nil {
		return nil, err
	}
	result := make(crypto.OlmSessionList, 0, len(response.Sessions))
	for index := range response.Sessions {
		session, err := DecodeOlmSession(&response.Sessions[index], store.pickleKey)
		if err != nil {
			return nil, err
		}
		result = append(result, session)
	}
	return result, nil
}

func (store *Store) GetLatestSession(
	ctx context.Context,
	key id.SenderKey,
) (*crypto.OlmSession, error) {
	var response Response
	if err := store.call(ctx, opGetLatestSession, Request{SenderKey: key}, &response, false); err != nil {
		return nil, err
	}
	return DecodeOlmSession(response.Session, store.pickleKey)
}

func (store *Store) GetNewestSessionCreationTS(
	ctx context.Context,
	key id.SenderKey,
) (time.Time, error) {
	var response Response
	err := store.call(ctx, opGetNewestSessionCreationTS, Request{SenderKey: key}, &response, false)
	return response.At, err
}

func (store *Store) UpdateSession(
	ctx context.Context,
	key id.SenderKey,
	session *crypto.OlmSession,
) error {
	record, err := EncodeOlmSession(session, store.pickleKey)
	if err != nil {
		return err
	}
	if record == nil {
		return fmt.Errorf("Matrix Olm session is nil")
	}
	return store.call(ctx, opUpdateSession, Request{SenderKey: key, Session: record}, &Response{}, true)
}

func (store *Store) DeleteSession(
	ctx context.Context,
	key id.SenderKey,
	session *crypto.OlmSession,
) error {
	record, err := EncodeOlmSession(session, store.pickleKey)
	if err != nil {
		return err
	}
	if record == nil {
		return fmt.Errorf("Matrix Olm session is nil")
	}
	return store.call(ctx, opDeleteSession, Request{SenderKey: key, Session: record}, &Response{}, true)
}

func (store *Store) PutOlmHash(ctx context.Context, hash [32]byte, receivedAt time.Time) error {
	return store.call(ctx, opPutOlmHash, Request{MessageHash: hash[:], At: receivedAt}, &Response{}, true)
}

func (store *Store) GetOlmHash(ctx context.Context, hash [32]byte) (time.Time, error) {
	var response Response
	err := store.call(ctx, opGetOlmHash, Request{MessageHash: hash[:]}, &response, false)
	return response.At, err
}

func (store *Store) DeleteOldOlmHashes(ctx context.Context, before time.Time) error {
	return store.call(ctx, opDeleteOldOlmHashes, Request{At: before}, &Response{}, true)
}

func (store *Store) PutGroupSession(ctx context.Context, session *crypto.InboundGroupSession) error {
	record, err := EncodeInboundGroupSession(session, store.pickleKey)
	if err != nil {
		return err
	}
	if record == nil {
		return fmt.Errorf("Matrix inbound group session is nil")
	}
	return store.call(ctx, opPutGroupSession, Request{GroupSession: record}, &Response{}, true)
}

func (store *Store) GetGroupSession(
	ctx context.Context,
	roomID id.RoomID,
	sessionID id.SessionID,
) (*crypto.InboundGroupSession, error) {
	var response Response
	if err := store.call(
		ctx, opGetGroupSession, Request{RoomID: roomID, SessionID: sessionID}, &response, false,
	); err != nil {
		return nil, err
	}
	if response.Withheld != nil {
		return nil, response.Withheld
	}
	return DecodeInboundGroupSession(response.GroupSession, store.pickleKey)
}

func (store *Store) RedactGroupSession(
	ctx context.Context,
	roomID id.RoomID,
	sessionID id.SessionID,
	reason string,
) error {
	return store.call(ctx, opRedactGroupSession, Request{
		RoomID: roomID, SessionID: sessionID, Reason: reason,
	}, &Response{}, true)
}

func (store *Store) RedactGroupSessions(
	ctx context.Context,
	roomID id.RoomID,
	senderKey id.SenderKey,
	reason string,
) ([]id.SessionID, error) {
	var response Response
	err := store.call(ctx, opRedactGroupSessions, Request{
		RoomID: roomID, SenderKey: senderKey, Reason: reason,
	}, &response, true)
	return response.SessionIDs, err
}

func (store *Store) RedactExpiredGroupSessions(ctx context.Context) ([]id.SessionID, error) {
	var response Response
	err := store.call(ctx, opRedactExpiredGroupSessions, Request{}, &response, true)
	return response.SessionIDs, err
}

func (store *Store) RedactOutdatedGroupSessions(ctx context.Context) ([]id.SessionID, error) {
	var response Response
	err := store.call(ctx, opRedactOutdatedGroupSessions, Request{}, &response, true)
	return response.SessionIDs, err
}

func (store *Store) PutWithheldGroupSession(
	ctx context.Context,
	content event.RoomKeyWithheldEventContent,
) error {
	return store.call(ctx, opPutWithheldGroupSession, Request{Withheld: &content}, &Response{}, true)
}

func (store *Store) GetWithheldGroupSession(
	ctx context.Context,
	roomID id.RoomID,
	sessionID id.SessionID,
) (*event.RoomKeyWithheldEventContent, error) {
	var response Response
	err := store.call(ctx, opGetWithheldGroupSession, Request{
		RoomID: roomID, SessionID: sessionID,
	}, &response, false)
	return response.Withheld, err
}

func (store *Store) GetGroupSessionsForRoom(
	ctx context.Context,
	roomID id.RoomID,
) dbutil.RowIter[*crypto.InboundGroupSession] {
	return store.groupSessions(ctx, opGetGroupSessionsForRoom, Request{RoomID: roomID})
}

func (store *Store) GetAllGroupSessions(
	ctx context.Context,
) dbutil.RowIter[*crypto.InboundGroupSession] {
	return store.groupSessions(ctx, opGetAllGroupSessions, Request{})
}

func (store *Store) GetGroupSessionsWithoutKeyBackupVersion(
	ctx context.Context,
	version id.KeyBackupVersion,
) dbutil.RowIter[*crypto.InboundGroupSession] {
	return store.groupSessions(ctx, opGetGroupSessionsWithoutBackup, Request{BackupVersion: version})
}

func (store *Store) groupSessions(
	ctx context.Context,
	operation string,
	request Request,
) dbutil.RowIter[*crypto.InboundGroupSession] {
	const pageSize = 128
	result := make([]*crypto.InboundGroupSession, 0)
	var snapshotID GroupSessionSnapshotID
	cursor := 0
	for {
		request.SnapshotID = snapshotID
		request.Cursor = cursor
		request.Limit = pageSize
		var response Response
		if err := store.call(ctx, operation, request, &response, false); err != nil {
			return dbutil.NewSliceIterWithError([]*crypto.InboundGroupSession(nil), err)
		}
		pageLength := len(response.GroupSessions)
		validProgression := pageLength <= pageSize && response.NextCursor == cursor+pageLength
		if response.More {
			validProgression = validProgression && pageLength > 0 && response.SnapshotID.Valid() &&
				(snapshotID == "" || response.SnapshotID == snapshotID)
		} else {
			validProgression = validProgression && response.SnapshotID == ""
		}
		if !validProgression {
			return dbutil.NewSliceIterWithError(
				[]*crypto.InboundGroupSession(nil),
				fmt.Errorf("Matrix broker returned an invalid group session page"),
			)
		}
		for index := range response.GroupSessions {
			session, err := DecodeInboundGroupSession(&response.GroupSessions[index], store.pickleKey)
			if err != nil {
				return dbutil.NewSliceIterWithError([]*crypto.InboundGroupSession(nil), err)
			}
			result = append(result, session)
		}
		if !response.More {
			break
		}
		snapshotID = response.SnapshotID
		cursor = response.NextCursor
	}
	return dbutil.NewSliceIter(result)
}

func (store *Store) AddOutboundGroupSession(
	ctx context.Context,
	session *crypto.OutboundGroupSession,
) error {
	return store.putOutboundGroupSession(ctx, opAddOutboundGroupSession, session)
}

func (store *Store) UpdateOutboundGroupSession(
	ctx context.Context,
	session *crypto.OutboundGroupSession,
) error {
	return store.putOutboundGroupSession(ctx, opUpdateOutboundGroupSession, session)
}

func (store *Store) putOutboundGroupSession(
	ctx context.Context,
	operation string,
	session *crypto.OutboundGroupSession,
) error {
	record, err := EncodeOutboundGroupSession(session, store.pickleKey)
	if err != nil {
		return err
	}
	if record == nil {
		return fmt.Errorf("Matrix outbound group session is nil")
	}
	return store.call(ctx, operation, Request{OutboundGroupSession: record}, &Response{}, true)
}

func (store *Store) GetOutboundGroupSession(
	ctx context.Context,
	roomID id.RoomID,
) (*crypto.OutboundGroupSession, error) {
	var response Response
	if err := store.call(
		ctx, opGetOutboundGroupSession, Request{RoomID: roomID}, &response, false,
	); err != nil {
		return nil, err
	}
	return DecodeOutboundGroupSession(response.OutboundGroupSession, store.pickleKey)
}

func (store *Store) RemoveOutboundGroupSession(ctx context.Context, roomID id.RoomID) error {
	return store.call(ctx, opRemoveOutboundGroupSession, Request{RoomID: roomID}, &Response{}, true)
}

func (store *Store) MarkOutboundGroupSessionShared(
	ctx context.Context,
	userID id.UserID,
	identityKey id.IdentityKey,
	sessionID id.SessionID,
) error {
	return store.call(ctx, opMarkOutboundGroupSessionShared, Request{
		UserID: userID, IdentityKey: identityKey, SessionID: sessionID,
	}, &Response{}, true)
}

func (store *Store) IsOutboundGroupSessionShared(
	ctx context.Context,
	userID id.UserID,
	identityKey id.IdentityKey,
	sessionID id.SessionID,
) (bool, error) {
	var response Response
	err := store.call(ctx, opIsOutboundGroupSessionShared, Request{
		UserID: userID, IdentityKey: identityKey, SessionID: sessionID,
	}, &response, false)
	return response.OK, err
}

func (store *Store) ValidateMessageIndex(
	ctx context.Context,
	senderKey id.SenderKey,
	sessionID id.SessionID,
	eventID id.EventID,
	index uint,
	timestamp int64,
) (bool, error) {
	var response Response
	err := store.call(ctx, opValidateMessageIndex, Request{
		SenderKey: senderKey, SessionID: sessionID, EventID: eventID,
		Index: index, Timestamp: timestamp,
	}, &response, true)
	return response.OK, err
}

func (store *Store) GetDevices(
	ctx context.Context,
	userID id.UserID,
) (map[id.DeviceID]*id.Device, error) {
	var response Response
	err := store.call(ctx, opGetDevices, Request{UserID: userID}, &response, false)
	return response.Devices, err
}

func (store *Store) GetDevice(
	ctx context.Context,
	userID id.UserID,
	deviceID id.DeviceID,
) (*id.Device, error) {
	var response Response
	err := store.call(ctx, opGetDevice, Request{
		UserID: userID, TargetDeviceID: deviceID,
	}, &response, false)
	return response.Device, err
}

func (store *Store) PutDevice(ctx context.Context, userID id.UserID, device *id.Device) error {
	return store.call(ctx, opPutDevice, Request{UserID: userID, Device: device}, &Response{}, true)
}

func (store *Store) PutDevices(
	ctx context.Context,
	userID id.UserID,
	devices map[id.DeviceID]*id.Device,
) error {
	return store.call(ctx, opPutDevices, Request{UserID: userID, Devices: devices}, &Response{}, true)
}

func (store *Store) FindDeviceByKey(
	ctx context.Context,
	userID id.UserID,
	identityKey id.IdentityKey,
) (*id.Device, error) {
	var response Response
	err := store.call(ctx, opFindDeviceByKey, Request{
		UserID: userID, IdentityKey: identityKey,
	}, &response, false)
	return response.Device, err
}

func (store *Store) FilterTrackedUsers(
	ctx context.Context,
	users []id.UserID,
) ([]id.UserID, error) {
	var response Response
	err := store.call(ctx, opFilterTrackedUsers, Request{Users: users}, &response, false)
	return response.Users, err
}

func (store *Store) MarkTrackedUsersOutdated(ctx context.Context, users []id.UserID) error {
	return store.call(ctx, opMarkTrackedUsersOutdated, Request{Users: users}, &Response{}, true)
}

func (store *Store) GetOutdatedTrackedUsers(ctx context.Context) ([]id.UserID, error) {
	var response Response
	err := store.call(ctx, opGetOutdatedTrackedUsers, Request{}, &response, false)
	return response.Users, err
}

func (store *Store) PutCrossSigningKey(
	ctx context.Context,
	userID id.UserID,
	usage id.CrossSigningUsage,
	key id.Ed25519,
) error {
	return store.call(ctx, opPutCrossSigningKey, Request{
		UserID: userID, CrossUsage: usage, CrossSigningKey: key,
	}, &Response{}, true)
}

func (store *Store) GetCrossSigningKeys(
	ctx context.Context,
	userID id.UserID,
) (map[id.CrossSigningUsage]id.CrossSigningKey, error) {
	var response Response
	err := store.call(ctx, opGetCrossSigningKeys, Request{UserID: userID}, &response, false)
	return response.CrossSigningKeys, err
}

func (store *Store) PutSignature(
	ctx context.Context,
	signedUser id.UserID,
	signedKey id.Ed25519,
	signerUser id.UserID,
	signerKey id.Ed25519,
	signature string,
) error {
	return store.call(ctx, opPutSignature, Request{
		UserID: signedUser, SignedKey: signedKey, SignerUserID: signerUser,
		SignerKey: signerKey, Signature: signature,
	}, &Response{}, true)
}

func (store *Store) IsKeySignedBy(
	ctx context.Context,
	userID id.UserID,
	key id.Ed25519,
	signedByUser id.UserID,
	signedByKey id.Ed25519,
) (bool, error) {
	var response Response
	err := store.call(ctx, opIsKeySignedBy, Request{
		UserID: userID, SignedKey: key, SignerUserID: signedByUser, SignerKey: signedByKey,
	}, &response, false)
	return response.OK, err
}

func (store *Store) DropSignaturesByKey(
	ctx context.Context,
	userID id.UserID,
	key id.Ed25519,
) (int64, error) {
	var response Response
	err := store.call(ctx, opDropSignaturesByKey, Request{
		UserID: userID, SignedKey: key,
	}, &response, true)
	return response.Count, err
}

func (store *Store) GetSignaturesForKeyBy(
	ctx context.Context,
	userID id.UserID,
	key id.Ed25519,
	signerID id.UserID,
) (map[id.Ed25519]string, error) {
	var response Response
	err := store.call(ctx, opGetSignaturesForKeyBy, Request{
		UserID: userID, SignedKey: key, SignerUserID: signerID,
	}, &response, false)
	return response.Signatures, err
}

func (store *Store) PutSecret(ctx context.Context, name id.Secret, value string) error {
	return store.call(ctx, opPutSecret, Request{SecretName: name, Value: value}, &Response{}, true)
}

func (store *Store) GetSecret(ctx context.Context, name id.Secret) (string, error) {
	var response Response
	err := store.call(ctx, opGetSecret, Request{SecretName: name}, &response, false)
	return response.Value, err
}

func (store *Store) DeleteSecret(ctx context.Context, name id.Secret) error {
	return store.call(ctx, opDeleteSecret, Request{SecretName: name}, &Response{}, true)
}

var _ crypto.Store = (*Store)(nil)
