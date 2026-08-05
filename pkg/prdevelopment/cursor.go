package prdevelopment

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

const maxCaseCursorBytes = 1024

type cursorFilter struct {
	Repository string `json:"repository"`
	PullNumber int64  `json:"pull_number"`
}

type wireCursor struct {
	Version      int    `json:"v"`
	Kind         string `json:"kind"`
	FilterDigest string `json:"filter"`
	UnixNano     int64  `json:"unix_nano"`
	ID           string `json:"id"`
}

func encodeCaseCursor(
	cursor eventing.PRDevelopmentCaseCursor,
	filter cursorFilter,
) (string, error) {
	if !validCursorTime(cursor.UpdatedAt) || !validCaseID(cursor.ID) {
		return "", errors.New("invalid pull request development cursor position")
	}
	raw, err := json.Marshal(wireCursor{
		Version:      1,
		Kind:         "pr-development",
		FilterDigest: caseFilterDigest(filter),
		UnixNano:     cursor.UpdatedAt.UnixNano(),
		ID:           cursor.ID,
	})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeCaseCursor(
	encoded string,
	filter cursorFilter,
) (*eventing.PRDevelopmentCaseCursor, error) {
	if encoded == "" {
		return nil, nil
	}
	if len(encoded) > maxCaseCursorBytes {
		return nil, invalidCaseCursor()
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil ||
		base64.RawURLEncoding.EncodeToString(raw) != encoded ||
		len(raw) == 0 ||
		len(raw) > maxCaseCursorBytes {
		return nil, invalidCaseCursor()
	}
	var cursor wireCursor
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&cursor); err != nil {
		return nil, invalidCaseCursor()
	}
	var trailing json.RawMessage
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, invalidCaseCursor()
	}
	canonical, marshalErr := json.Marshal(cursor)
	if marshalErr != nil ||
		!bytes.Equal(raw, canonical) ||
		cursor.Version != 1 ||
		cursor.Kind != "pr-development" ||
		cursor.FilterDigest != caseFilterDigest(filter) ||
		!validCaseID(cursor.ID) ||
		!validCursorTimestamp(cursor.UnixNano) {
		return nil, invalidCaseCursor()
	}
	return &eventing.PRDevelopmentCaseCursor{
		UpdatedAt: time.Unix(0, cursor.UnixNano).UTC(),
		ID:        cursor.ID,
	}, nil
}

func caseFilterDigest(filter cursorFilter) string {
	raw, err := json.Marshal(filter)
	if err != nil {
		panic(fmt.Sprintf("marshal pull request development cursor filter: %v", err))
	}
	digest := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func validCursorTimestamp(unixNano int64) bool {
	position := time.Unix(0, unixNano).UTC()
	return !position.IsZero() && position.UnixNano() == unixNano
}

func validCursorTime(position time.Time) bool {
	if position.IsZero() {
		return false
	}
	unixNano := position.UnixNano()
	return position.Equal(time.Unix(0, unixNano))
}

func invalidCaseCursor() error {
	return fmt.Errorf(
		"%w: cursor is invalid or does not match its filters",
		ErrInvalidRequest,
	)
}
