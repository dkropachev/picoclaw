package prworkspace

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

const maxPublicationPayloadBytes = 1 << 20

func encodePublicationPayload(value any) (json.RawMessage, string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, "", err
	}
	if len(encoded) < 2 || len(encoded) > maxPublicationPayloadBytes {
		return nil, "", ErrInvalid
	}
	digest, err := fingerprintValue(value)
	if err != nil {
		return nil, "", err
	}
	return append(json.RawMessage(nil), encoded...), digest, nil
}

func decodePublicationPayload(raw json.RawMessage, target any) error {
	if len(raw) < 2 || len(raw) > maxPublicationPayloadBytes || target == nil {
		return ErrConflict
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return ErrConflict
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrConflict
	}
	return nil
}

func decodePinnedPublicationPayload(publication Publication, target any) error {
	if publication.PayloadDigest == "" || decodePublicationPayload(publication.payload, target) != nil {
		return ErrConflict
	}
	digest, err := fingerprintValue(target)
	if err != nil || digest != publication.PayloadDigest {
		return ErrConflict
	}
	return nil
}
