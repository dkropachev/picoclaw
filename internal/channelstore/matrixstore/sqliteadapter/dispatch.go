package sqliteadapter

import (
	"context"
	"errors"

	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/event"

	"github.com/sipeed/picoclaw/internal/channelstore/matrixstore"
	"github.com/sipeed/picoclaw/pkg/database"
)

func dispatch(
	ctx context.Context,
	operation string,
	input matrixstore.Request,
	bundle *storeBundle,
) (any, error) {
	cryptoStore := bundle.cryptoStore()
	switch operation {
	case "crypto.flush":
		return mutationResult(cryptoStore.Flush(ctx))
	case "crypto.put-account":
		account, err := matrixstore.DecodeAccount(input.Account, cryptoStore.PickleKey)
		if err != nil || account == nil {
			return nil, invalidCodec(err)
		}
		_, err = cryptoStore.GetAccount(ctx)
		if err != nil {
			return nil, backendError(err)
		}
		return mutationResult(cryptoStore.PutAccount(ctx, account))
	case "crypto.get-account":
		account, err := cryptoStore.GetAccount(ctx)
		if err != nil {
			return nil, backendError(err)
		}
		record, err := matrixstore.EncodeAccount(account, cryptoStore.PickleKey)
		if err != nil {
			return nil, invalidCodec(err)
		}
		return matrixstore.Response{Account: record}, nil
	case "crypto.add-session", "crypto.update-session", "crypto.delete-session":
		session, err := matrixstore.DecodeOlmSession(input.Session, cryptoStore.PickleKey)
		if err != nil || session == nil {
			return nil, invalidCodec(err)
		}
		switch operation {
		case "crypto.add-session":
			err = cryptoStore.AddSession(ctx, input.SenderKey, session)
		case "crypto.update-session":
			err = cryptoStore.UpdateSession(ctx, input.SenderKey, session)
		default:
			err = cryptoStore.DeleteSession(ctx, input.SenderKey, session)
		}
		return mutationResult(err)
	case "crypto.has-session":
		return matrixstore.Response{OK: cryptoStore.HasSession(ctx, input.SenderKey)}, nil
	case "crypto.get-sessions":
		sessions, err := cryptoStore.GetSessions(ctx, input.SenderKey)
		if err != nil {
			return nil, backendError(err)
		}
		records, err := encodeOlmSessions(sessions, cryptoStore.PickleKey)
		if err != nil {
			return nil, invalidCodec(err)
		}
		return matrixstore.Response{Sessions: records}, nil
	case "crypto.get-latest-session":
		session, err := cryptoStore.GetLatestSession(ctx, input.SenderKey)
		if err != nil {
			return nil, backendError(err)
		}
		record, err := matrixstore.EncodeOlmSession(session, cryptoStore.PickleKey)
		if err != nil {
			return nil, invalidCodec(err)
		}
		return matrixstore.Response{Session: record}, nil
	case "crypto.get-newest-session-creation-ts":
		at, err := cryptoStore.GetNewestSessionCreationTS(ctx, input.SenderKey)
		if err != nil {
			return nil, backendError(err)
		}
		return matrixstore.Response{At: at}, nil
	case "crypto.put-olm-hash", "crypto.get-olm-hash":
		if len(input.MessageHash) != 32 {
			return nil, database.NewError(database.CodeInvalid, "Matrix Olm message hash is invalid")
		}
		var hash [32]byte
		copy(hash[:], input.MessageHash)
		if operation == "crypto.put-olm-hash" {
			return mutationResult(cryptoStore.PutOlmHash(ctx, hash, input.At))
		}
		at, err := cryptoStore.GetOlmHash(ctx, hash)
		if err != nil {
			return nil, backendError(err)
		}
		return matrixstore.Response{At: at}, nil
	case "crypto.delete-old-olm-hashes":
		return mutationResult(cryptoStore.DeleteOldOlmHashes(ctx, input.At))
	case "crypto.put-group-session":
		session, err := matrixstore.DecodeInboundGroupSession(input.GroupSession, cryptoStore.PickleKey)
		if err != nil || session == nil {
			return nil, invalidCodec(err)
		}
		return mutationResult(cryptoStore.PutGroupSession(ctx, session))
	case "crypto.get-group-session":
		session, err := cryptoStore.GetGroupSession(ctx, input.RoomID, input.SessionID)
		if err != nil {
			var withheld *event.RoomKeyWithheldEventContent
			if errors.As(err, &withheld) {
				return matrixstore.Response{Withheld: withheld}, nil
			}
			return nil, backendError(err)
		}
		record, err := matrixstore.EncodeInboundGroupSession(session, cryptoStore.PickleKey)
		if err != nil {
			return nil, invalidCodec(err)
		}
		return matrixstore.Response{GroupSession: record}, nil
	case "crypto.redact-group-session":
		return mutationResult(cryptoStore.RedactGroupSession(
			ctx, input.RoomID, input.SessionID, input.Reason,
		))
	case "crypto.redact-group-sessions":
		ids, err := cryptoStore.RedactGroupSessions(ctx, input.RoomID, input.SenderKey, input.Reason)
		if err != nil {
			return nil, backendError(err)
		}
		return matrixstore.Response{SessionIDs: ids}, nil
	case "crypto.redact-expired-group-sessions":
		ids, err := cryptoStore.RedactExpiredGroupSessions(ctx)
		if err != nil {
			return nil, backendError(err)
		}
		return matrixstore.Response{SessionIDs: ids}, nil
	case "crypto.redact-outdated-group-sessions":
		ids, err := cryptoStore.RedactOutdatedGroupSessions(ctx)
		if err != nil {
			return nil, backendError(err)
		}
		return matrixstore.Response{SessionIDs: ids}, nil
	case "crypto.put-withheld-group-session":
		if input.Withheld == nil {
			return nil, database.NewError(database.CodeInvalid, "Matrix withheld session is invalid")
		}
		return mutationResult(cryptoStore.PutWithheldGroupSession(ctx, *input.Withheld))
	case "crypto.get-withheld-group-session":
		content, err := cryptoStore.GetWithheldGroupSession(ctx, input.RoomID, input.SessionID)
		if err != nil {
			return nil, backendError(err)
		}
		return matrixstore.Response{Withheld: content}, nil
	case "crypto.get-group-sessions-for-room",
		"crypto.get-all-group-sessions", "crypto.get-group-sessions-without-backup":
		if input.Cursor < 0 || input.Limit < 1 ||
			input.Limit > maximumGroupSessionPageSize ||
			(input.SnapshotID == "" && input.Cursor != 0) {
			return nil, invalidGroupSessionPage()
		}
		target := groupSessionPageTarget{
			storeID:       bundle.storeID,
			deviceID:      input.DeviceID,
			operation:     operation,
			roomID:        input.RoomID,
			backupVersion: input.BackupVersion,
		}
		if input.SnapshotID != "" {
			return bundle.pages.next(input.SnapshotID, target, input.Cursor, input.Limit)
		}
		var (
			sessions []*crypto.InboundGroupSession
			err      error
		)
		switch operation {
		case "crypto.get-group-sessions-for-room":
			sessions, err = cryptoStore.GetGroupSessionsForRoom(ctx, input.RoomID).AsList()
		case "crypto.get-all-group-sessions":
			sessions, err = cryptoStore.GetAllGroupSessions(ctx).AsList()
		default:
			sessions, err = cryptoStore.GetGroupSessionsWithoutKeyBackupVersion(
				ctx, input.BackupVersion,
			).AsList()
		}
		if err != nil {
			return nil, backendError(err)
		}
		records, err := encodeInboundGroupSessions(sessions, cryptoStore.PickleKey)
		if err != nil {
			return nil, invalidCodec(err)
		}
		return bundle.pages.first(target, records, input.Limit)
	case "crypto.add-outbound-group-session", "crypto.update-outbound-group-session":
		session, err := matrixstore.DecodeOutboundGroupSession(
			input.OutboundGroupSession, cryptoStore.PickleKey,
		)
		if err != nil || session == nil {
			return nil, invalidCodec(err)
		}
		if operation == "crypto.add-outbound-group-session" {
			err = cryptoStore.AddOutboundGroupSession(ctx, session)
		} else {
			err = cryptoStore.UpdateOutboundGroupSession(ctx, session)
		}
		return mutationResult(err)
	case "crypto.get-outbound-group-session":
		session, err := cryptoStore.GetOutboundGroupSession(ctx, input.RoomID)
		if err != nil {
			return nil, backendError(err)
		}
		record, err := matrixstore.EncodeOutboundGroupSession(session, cryptoStore.PickleKey)
		if err != nil {
			return nil, invalidCodec(err)
		}
		return matrixstore.Response{OutboundGroupSession: record}, nil
	case "crypto.remove-outbound-group-session":
		return mutationResult(cryptoStore.RemoveOutboundGroupSession(ctx, input.RoomID))
	case "crypto.mark-outbound-group-session-shared":
		return mutationResult(cryptoStore.MarkOutboundGroupSessionShared(
			ctx, input.UserID, input.IdentityKey, input.SessionID,
		))
	case "crypto.is-outbound-group-session-shared":
		shared, err := cryptoStore.IsOutboundGroupSessionShared(
			ctx, input.UserID, input.IdentityKey, input.SessionID,
		)
		if err != nil {
			return nil, backendError(err)
		}
		return matrixstore.Response{OK: shared}, nil
	case "crypto.validate-message-index":
		valid, err := cryptoStore.ValidateMessageIndex(
			ctx, input.SenderKey, input.SessionID, input.EventID, input.Index, input.Timestamp,
		)
		if err != nil {
			return nil, backendError(err)
		}
		return matrixstore.Response{OK: valid}, nil
	}
	return dispatchIdentitiesAndState(ctx, operation, input, bundle)
}

func encodeOlmSessions(
	sessions crypto.OlmSessionList,
	pickleKey []byte,
) ([]matrixstore.OlmSessionRecord, error) {
	result := make([]matrixstore.OlmSessionRecord, 0, len(sessions))
	for _, session := range sessions {
		record, err := matrixstore.EncodeOlmSession(session, pickleKey)
		if err != nil {
			return nil, err
		}
		if record != nil {
			result = append(result, *record)
		}
	}
	return result, nil
}

func encodeInboundGroupSessions(
	sessions []*crypto.InboundGroupSession,
	pickleKey []byte,
) ([]matrixstore.InboundGroupSessionRecord, error) {
	result := make([]matrixstore.InboundGroupSessionRecord, 0, len(sessions))
	for _, session := range sessions {
		record, err := matrixstore.EncodeInboundGroupSession(session, pickleKey)
		if err != nil {
			return nil, err
		}
		if record != nil {
			result = append(result, *record)
		}
	}
	return result, nil
}

func mutationResult(err error) (any, error) {
	if err != nil {
		return nil, backendError(err)
	}
	return matrixstore.Response{OK: true}, nil
}

func invalidCodec(err error) error {
	if err == nil {
		return database.NewError(database.CodeInvalid, "Matrix crypto record is missing")
	}
	return database.NewError(database.CodeIntegrity, "Matrix crypto record is invalid")
}
