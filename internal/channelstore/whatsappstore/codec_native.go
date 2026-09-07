//go:build whatsapp_native

package whatsappstore

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/util/keys"
	waLog "go.mau.fi/whatsmeow/util/log"
)

func JIDFromValue(value types.JID) JID {
	return JID{
		User: value.User, RawAgent: value.RawAgent, Device: value.Device,
		Integrator: value.Integrator, Server: value.Server,
	}
}

func (value JID) Value() types.JID {
	return types.JID{
		User: value.User, RawAgent: value.RawAgent, Device: value.Device,
		Integrator: value.Integrator, Server: value.Server,
	}
}

func TimeFromValue(value time.Time) Time {
	if value.IsZero() {
		return Time{}
	}
	return Time{
		Set: true, UTC: value.Location() == time.UTC,
		Seconds: value.Unix(), Nanoseconds: int32(value.Nanosecond()),
	}
}

func (value Time) Value() (time.Time, error) {
	if !value.Set {
		if value.UTC || value.Seconds != 0 || value.Nanoseconds != 0 {
			return time.Time{}, fmt.Errorf("unset timestamp contains a value")
		}
		return time.Time{}, nil
	}
	if value.Nanoseconds < 0 || value.Nanoseconds >= int32(time.Second) {
		return time.Time{}, fmt.Errorf("timestamp nanoseconds are invalid")
	}
	result := time.Unix(value.Seconds, int64(value.Nanoseconds))
	if value.UTC {
		result = result.UTC()
	}
	return result, nil
}

func DeviceToDTO(device *store.Device) (*Device, error) {
	if device == nil || device.NoiseKey == nil || device.NoiseKey.Priv == nil ||
		device.IdentityKey == nil || device.IdentityKey.Priv == nil ||
		device.SignedPreKey == nil || device.SignedPreKey.Priv == nil {
		return nil, fmt.Errorf("WhatsApp device key material is incomplete")
	}
	dto := &Device{
		NoisePrivate:          cloneBytes(device.NoiseKey.Priv[:]),
		IdentityPrivate:       cloneBytes(device.IdentityKey.Priv[:]),
		SignedPreKeyPrivate:   cloneBytes(device.SignedPreKey.Priv[:]),
		SignedPreKeyID:        device.SignedPreKey.KeyID,
		RegistrationID:        device.RegistrationID,
		AdvSecretKey:          cloneBytes(device.AdvSecretKey),
		LID:                   JIDFromValue(device.LID),
		Platform:              device.Platform,
		BusinessName:          device.BusinessName,
		PushName:              device.PushName,
		LIDMigrationTimestamp: device.LIDMigrationTimestamp,
		Initialized:           device.Initialized,
	}
	if device.SignedPreKey.Signature != nil {
		dto.SignedPreKeySignature = cloneBytes(device.SignedPreKey.Signature[:])
	}
	if device.ID != nil {
		id := JIDFromValue(*device.ID)
		dto.ID = &id
	}
	if device.Account != nil {
		dto.Account = &Account{
			Details:             cloneBytes(device.Account.GetDetails()),
			AccountSignature:    cloneBytes(device.Account.GetAccountSignature()),
			AccountSignatureKey: cloneBytes(device.Account.GetAccountSignatureKey()),
			DeviceSignature:     cloneBytes(device.Account.GetDeviceSignature()),
		}
	}
	if device.FacebookUUID != uuid.Nil {
		dto.FacebookUUID = device.FacebookUUID.String()
	}
	return dto, nil
}

func DeviceFromDTO(dto *Device, log waLog.Logger) (*store.Device, error) {
	if dto == nil {
		return nil, fmt.Errorf("WhatsApp device is missing")
	}
	noise, err := keyPairFromPrivate(dto.NoisePrivate)
	if err != nil {
		return nil, fmt.Errorf("WhatsApp noise key: %w", err)
	}
	identity, err := keyPairFromPrivate(dto.IdentityPrivate)
	if err != nil {
		return nil, fmt.Errorf("WhatsApp identity key: %w", err)
	}
	signedPair, err := keyPairFromPrivate(dto.SignedPreKeyPrivate)
	if err != nil {
		return nil, fmt.Errorf("WhatsApp signed pre-key: %w", err)
	}
	preKey := &keys.PreKey{KeyPair: *signedPair, KeyID: dto.SignedPreKeyID}
	if dto.SignedPreKeySignature != nil {
		if len(dto.SignedPreKeySignature) != 64 {
			return nil, fmt.Errorf("WhatsApp signed pre-key signature has invalid length")
		}
		var signature [64]byte
		copy(signature[:], dto.SignedPreKeySignature)
		preKey.Signature = &signature
	}
	if log == nil {
		log = waLog.Noop
	}
	device := &store.Device{
		Log:                   log,
		NoiseKey:              noise,
		IdentityKey:           identity,
		SignedPreKey:          preKey,
		RegistrationID:        dto.RegistrationID,
		AdvSecretKey:          cloneBytes(dto.AdvSecretKey),
		LID:                   dto.LID.Value(),
		Platform:              dto.Platform,
		BusinessName:          dto.BusinessName,
		PushName:              dto.PushName,
		LIDMigrationTimestamp: dto.LIDMigrationTimestamp,
		Initialized:           dto.Initialized,
	}
	if dto.ID != nil {
		id := dto.ID.Value()
		device.ID = &id
	}
	if dto.Account != nil {
		device.Account = &waAdv.ADVSignedDeviceIdentity{
			Details:             cloneBytes(dto.Account.Details),
			AccountSignature:    cloneBytes(dto.Account.AccountSignature),
			AccountSignatureKey: cloneBytes(dto.Account.AccountSignatureKey),
			DeviceSignature:     cloneBytes(dto.Account.DeviceSignature),
		}
	}
	if dto.FacebookUUID != "" {
		parsed, parseErr := uuid.Parse(dto.FacebookUUID)
		if parseErr != nil {
			return nil, fmt.Errorf("WhatsApp Facebook UUID is invalid")
		}
		device.FacebookUUID = parsed
	}
	return device, nil
}

func PreKeyToDTO(preKey *keys.PreKey) (*PreKey, error) {
	if preKey == nil {
		return nil, nil
	}
	if preKey.Priv == nil {
		return nil, fmt.Errorf("WhatsApp pre-key material is incomplete")
	}
	dto := &PreKey{Private: cloneBytes(preKey.Priv[:]), ID: preKey.KeyID}
	if preKey.Signature != nil {
		dto.Signature = cloneBytes(preKey.Signature[:])
	}
	return dto, nil
}

func PreKeyFromDTO(dto *PreKey) (*keys.PreKey, error) {
	if dto == nil {
		return nil, nil
	}
	pair, err := keyPairFromPrivate(dto.Private)
	if err != nil {
		return nil, err
	}
	preKey := &keys.PreKey{KeyPair: *pair, KeyID: dto.ID}
	if dto.Signature != nil {
		if len(dto.Signature) != 64 {
			return nil, fmt.Errorf("WhatsApp pre-key signature has invalid length")
		}
		var signature [64]byte
		copy(signature[:], dto.Signature)
		preKey.Signature = &signature
	}
	return preKey, nil
}

func keyPairFromPrivate(value []byte) (*keys.KeyPair, error) {
	if len(value) != 32 {
		return nil, fmt.Errorf("private key has invalid length")
	}
	var private [32]byte
	copy(private[:], value)
	return keys.NewKeyPairFromPrivateKey(private), nil
}

func cloneBytes(value []byte) []byte {
	if value == nil {
		return nil
	}
	return append([]byte(nil), value...)
}
