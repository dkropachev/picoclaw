package operator

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

const (
	cursorVersion      = 1
	cursorKindEvents   = "events"
	cursorKindDispatch = "dispatches"
	maxCursorBytes     = 1024
)

type eventFilterBinding struct {
	Source        string                 `json:"source"`
	Connector     string                 `json:"connector"`
	Type          string                 `json:"type"`
	RoutingStatus eventing.RoutingStatus `json:"routing_status"`
}

type dispatchFilterBinding struct {
	EventID     string                  `json:"event_id"`
	WorkflowRef string                  `json:"workflow_ref"`
	Status      eventing.DispatchStatus `json:"status"`
}

type wireCursor struct {
	Version      int    `json:"v"`
	Kind         string `json:"kind"`
	FilterDigest string `json:"filter"`
	UnixNano     int64  `json:"unix_nano"`
	ID           string `json:"id"`
}

func encodeEventCursor(
	cursor eventing.EventCursor,
	filter eventFilterBinding,
) (string, error) {
	if !validCursorTime(cursor.ReceivedAt) || !validEventID(cursor.ID) {
		return "", errors.New("invalid event cursor position")
	}
	return encodeCursor(wireCursor{
		Version:      cursorVersion,
		Kind:         cursorKindEvents,
		FilterDigest: filterDigest(filter),
		UnixNano:     cursor.ReceivedAt.UnixNano(),
		ID:           cursor.ID,
	})
}

func decodeEventCursor(
	encoded string,
	filter eventFilterBinding,
) (*eventing.EventCursor, error) {
	if encoded == "" {
		return nil, nil
	}
	cursor, err := decodeCursor(encoded, cursorKindEvents, filterDigest(filter), validEventID)
	if err != nil {
		return nil, err
	}
	return &eventing.EventCursor{
		ReceivedAt: time.Unix(0, cursor.UnixNano).UTC(),
		ID:         cursor.ID,
	}, nil
}

func encodeDispatchCursor(
	cursor eventing.DispatchCursor,
	filter dispatchFilterBinding,
) (string, error) {
	if !validCursorTime(cursor.CreatedAt) || !validDispatchID(cursor.ID) {
		return "", errors.New("invalid dispatch cursor position")
	}
	return encodeCursor(wireCursor{
		Version:      cursorVersion,
		Kind:         cursorKindDispatch,
		FilterDigest: filterDigest(filter),
		UnixNano:     cursor.CreatedAt.UnixNano(),
		ID:           cursor.ID,
	})
}

func decodeDispatchCursor(
	encoded string,
	filter dispatchFilterBinding,
) (*eventing.DispatchCursor, error) {
	if encoded == "" {
		return nil, nil
	}
	cursor, err := decodeCursor(
		encoded,
		cursorKindDispatch,
		filterDigest(filter),
		validDispatchID,
	)
	if err != nil {
		return nil, err
	}
	return &eventing.DispatchCursor{
		CreatedAt: time.Unix(0, cursor.UnixNano).UTC(),
		ID:        cursor.ID,
	}, nil
}

func encodeCursor(cursor wireCursor) (string, error) {
	raw, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeCursor(
	encoded, kind, digest string,
	validID func(string) bool,
) (wireCursor, error) {
	if encoded == "" || len(encoded) > maxCursorBytes {
		return wireCursor{}, invalidCursor()
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil ||
		base64.RawURLEncoding.EncodeToString(raw) != encoded ||
		len(raw) == 0 ||
		len(raw) > maxCursorBytes {
		return wireCursor{}, invalidCursor()
	}

	var cursor wireCursor
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&cursor); err != nil {
		return wireCursor{}, invalidCursor()
	}
	var trailing json.RawMessage
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return wireCursor{}, invalidCursor()
	}
	canonical, err := json.Marshal(cursor)
	if err != nil || !bytes.Equal(raw, canonical) {
		return wireCursor{}, invalidCursor()
	}
	if cursor.Version != cursorVersion ||
		cursor.Kind != kind ||
		cursor.FilterDigest != digest ||
		!validCursorTimestamp(cursor.UnixNano) ||
		!validID(cursor.ID) {
		return wireCursor{}, invalidCursor()
	}
	return cursor, nil
}

func validCursorTimestamp(unixNano int64) bool {
	if unixNano == 0 {
		return false
	}
	position := time.Unix(0, unixNano).UTC()
	return !position.IsZero() && position.UnixNano() == unixNano
}

func validCursorTime(position time.Time) bool {
	if position.IsZero() {
		return false
	}
	unixNano := position.UnixNano()
	return unixNano != 0 && position.Equal(time.Unix(0, unixNano))
}

func filterDigest(filter any) string {
	raw, err := json.Marshal(filter)
	if err != nil {
		panic(fmt.Sprintf("marshal event operator cursor filter: %v", err))
	}
	digest := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func invalidCursor() error {
	return fmt.Errorf("%w: cursor is invalid or does not match its filters", ErrInvalidRequest)
}
