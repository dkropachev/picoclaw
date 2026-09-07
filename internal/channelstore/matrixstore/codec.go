package matrixstore

import (
	"fmt"

	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/crypto/olm"
)

func EncodeAccount(account *crypto.OlmAccount, pickleKey []byte) (*AccountRecord, error) {
	if account == nil || account.Internal == nil {
		return nil, nil
	}
	pickled, err := account.Internal.Pickle(pickleKey)
	if err != nil {
		return nil, fmt.Errorf("pickle Matrix Olm account: %w", err)
	}
	return &AccountRecord{
		Pickle: pickled, Shared: account.Shared, KeyBackupVersion: account.KeyBackupVersion,
	}, nil
}

func DecodeAccount(record *AccountRecord, pickleKey []byte) (*crypto.OlmAccount, error) {
	if record == nil {
		return nil, nil
	}
	internal := olm.NewBlankAccount()
	if internal == nil {
		return nil, fmt.Errorf("Matrix Olm account codec is unavailable")
	}
	if err := internal.Unpickle(record.Pickle, pickleKey); err != nil {
		return nil, fmt.Errorf("unpickle Matrix Olm account: %w", err)
	}
	return &crypto.OlmAccount{
		Internal: internal, Shared: record.Shared, KeyBackupVersion: record.KeyBackupVersion,
	}, nil
}

func EncodeOlmSession(session *crypto.OlmSession, pickleKey []byte) (*OlmSessionRecord, error) {
	if session == nil || session.Internal == nil {
		return nil, nil
	}
	pickled, err := session.Internal.Pickle(pickleKey)
	if err != nil {
		return nil, fmt.Errorf("pickle Matrix Olm session: %w", err)
	}
	return &OlmSessionRecord{
		Pickle:            pickled,
		CreationTime:      session.CreationTime,
		LastEncryptedTime: session.LastEncryptedTime,
		LastDecryptedTime: session.LastDecryptedTime,
	}, nil
}

func DecodeOlmSession(record *OlmSessionRecord, pickleKey []byte) (*crypto.OlmSession, error) {
	if record == nil {
		return nil, nil
	}
	internal := olm.NewBlankSession()
	if internal == nil {
		return nil, fmt.Errorf("Matrix Olm session codec is unavailable")
	}
	if err := internal.Unpickle(record.Pickle, pickleKey); err != nil {
		return nil, fmt.Errorf("unpickle Matrix Olm session: %w", err)
	}
	return &crypto.OlmSession{
		Internal: internal,
		ExpirationMixin: crypto.ExpirationMixin{TimeMixin: crypto.TimeMixin{
			CreationTime:      record.CreationTime,
			LastEncryptedTime: record.LastEncryptedTime,
			LastDecryptedTime: record.LastDecryptedTime,
		}},
	}, nil
}

func EncodeInboundGroupSession(
	session *crypto.InboundGroupSession,
	pickleKey []byte,
) (*InboundGroupSessionRecord, error) {
	if session == nil || session.Internal == nil {
		return nil, nil
	}
	pickled, err := session.Internal.Pickle(pickleKey)
	if err != nil {
		return nil, fmt.Errorf("pickle Matrix inbound group session: %w", err)
	}
	return &InboundGroupSessionRecord{
		Pickle:           pickled,
		SigningKey:       session.SigningKey,
		SenderKey:        session.SenderKey,
		RoomID:           session.RoomID,
		ForwardingChains: append([]string(nil), session.ForwardingChains...),
		RatchetSafety:    session.RatchetSafety,
		ReceivedAt:       session.ReceivedAt,
		MaxAge:           session.MaxAge,
		MaxMessages:      session.MaxMessages,
		IsScheduled:      session.IsScheduled,
		KeyBackupVersion: session.KeyBackupVersion,
		KeySource:        session.KeySource,
	}, nil
}

func DecodeInboundGroupSession(
	record *InboundGroupSessionRecord,
	pickleKey []byte,
) (*crypto.InboundGroupSession, error) {
	if record == nil {
		return nil, nil
	}
	internal := olm.NewBlankInboundGroupSession()
	if internal == nil {
		return nil, fmt.Errorf("Matrix inbound group session codec is unavailable")
	}
	if err := internal.Unpickle(record.Pickle, pickleKey); err != nil {
		return nil, fmt.Errorf("unpickle Matrix inbound group session: %w", err)
	}
	return &crypto.InboundGroupSession{
		Internal:         internal,
		SigningKey:       record.SigningKey,
		SenderKey:        record.SenderKey,
		RoomID:           record.RoomID,
		ForwardingChains: append([]string(nil), record.ForwardingChains...),
		RatchetSafety:    record.RatchetSafety,
		ReceivedAt:       record.ReceivedAt,
		MaxAge:           record.MaxAge,
		MaxMessages:      record.MaxMessages,
		IsScheduled:      record.IsScheduled,
		KeyBackupVersion: record.KeyBackupVersion,
		KeySource:        record.KeySource,
	}, nil
}

func EncodeOutboundGroupSession(
	session *crypto.OutboundGroupSession,
	pickleKey []byte,
) (*OutboundGroupSessionRecord, error) {
	if session == nil || session.Internal == nil {
		return nil, nil
	}
	pickled, err := session.Internal.Pickle(pickleKey)
	if err != nil {
		return nil, fmt.Errorf("pickle Matrix outbound group session: %w", err)
	}
	return &OutboundGroupSessionRecord{
		Pickle:            pickled,
		RoomID:            session.RoomID,
		Shared:            session.Shared,
		MaxMessages:       session.MaxMessages,
		MessageCount:      session.MessageCount,
		MaxAge:            session.MaxAge,
		CreationTime:      session.CreationTime,
		LastEncryptedTime: session.LastEncryptedTime,
	}, nil
}

func DecodeOutboundGroupSession(
	record *OutboundGroupSessionRecord,
	pickleKey []byte,
) (*crypto.OutboundGroupSession, error) {
	if record == nil {
		return nil, nil
	}
	internal := olm.NewBlankOutboundGroupSession()
	if internal == nil {
		return nil, fmt.Errorf("Matrix outbound group session codec is unavailable")
	}
	if err := internal.Unpickle(record.Pickle, pickleKey); err != nil {
		return nil, fmt.Errorf("unpickle Matrix outbound group session: %w", err)
	}
	return &crypto.OutboundGroupSession{
		Internal:     internal,
		RoomID:       record.RoomID,
		Shared:       record.Shared,
		MaxMessages:  record.MaxMessages,
		MessageCount: record.MessageCount,
		ExpirationMixin: crypto.ExpirationMixin{
			TimeMixin: crypto.TimeMixin{
				CreationTime:      record.CreationTime,
				LastEncryptedTime: record.LastEncryptedTime,
			},
			MaxAge: record.MaxAge,
		},
	}, nil
}
