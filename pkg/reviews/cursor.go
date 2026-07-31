package reviews

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

const maxReviewCursorBytes = 1024

type reviewCursorFilter struct {
	Status     eventing.ReviewCaseStatus `json:"status"`
	Repository string                    `json:"repository"`
}

type reviewWireCursor struct {
	Version      int    `json:"v"`
	Kind         string `json:"kind"`
	FilterDigest string `json:"filter"`
	UnixNano     int64  `json:"unix_nano"`
	ID           string `json:"id"`
}

func encodeReviewCursor(
	cursor eventing.ReviewCaseCursor,
	filter reviewCursorFilter,
) (string, error) {
	if !validReviewCursorTime(cursor.UpdatedAt) || !validReviewID(cursor.ID, "prc_") {
		return "", errors.New("invalid review cursor position")
	}
	raw, err := json.Marshal(reviewWireCursor{
		Version:      1,
		Kind:         "reviews",
		FilterDigest: reviewFilterDigest(filter),
		UnixNano:     cursor.UpdatedAt.UnixNano(),
		ID:           cursor.ID,
	})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeReviewCursor(
	encoded string,
	filter reviewCursorFilter,
) (*eventing.ReviewCaseCursor, error) {
	if encoded == "" {
		return nil, nil
	}
	if len(encoded) > maxReviewCursorBytes {
		return nil, invalidReviewCursor()
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil ||
		base64.RawURLEncoding.EncodeToString(raw) != encoded ||
		len(raw) == 0 ||
		len(raw) > maxReviewCursorBytes {
		return nil, invalidReviewCursor()
	}
	var cursor reviewWireCursor
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return nil, invalidReviewCursor()
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, invalidReviewCursor()
	}
	canonical, err := json.Marshal(cursor)
	if err != nil ||
		!bytes.Equal(raw, canonical) ||
		cursor.Version != 1 ||
		cursor.Kind != "reviews" ||
		cursor.FilterDigest != reviewFilterDigest(filter) ||
		!validReviewID(cursor.ID, "prc_") ||
		!validReviewCursorTimestamp(cursor.UnixNano) {
		return nil, invalidReviewCursor()
	}
	return &eventing.ReviewCaseCursor{
		UpdatedAt: time.Unix(0, cursor.UnixNano).UTC(),
		ID:        cursor.ID,
	}, nil
}

func reviewFilterDigest(filter reviewCursorFilter) string {
	raw, err := json.Marshal(filter)
	if err != nil {
		panic(fmt.Sprintf("marshal review cursor filter: %v", err))
	}
	digest := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func validReviewCursorTimestamp(unixNano int64) bool {
	if unixNano == 0 {
		return false
	}
	position := time.Unix(0, unixNano).UTC()
	return !position.IsZero() && position.UnixNano() == unixNano
}

func validReviewCursorTime(position time.Time) bool {
	if position.IsZero() {
		return false
	}
	unixNano := position.UnixNano()
	return unixNano != 0 && position.Equal(time.Unix(0, unixNano))
}

func validReviewID(value, prefix string) bool {
	if len(value) != len(prefix)+32 || !strings.HasPrefix(value, prefix) {
		return false
	}
	for _, char := range value[len(prefix):] {
		if char < '0' || char > '9' {
			if char < 'a' || char > 'f' {
				return false
			}
		}
	}
	return true
}

func invalidReviewCursor() error {
	return fmt.Errorf(
		"%w: cursor is invalid or does not match its filters",
		ErrInvalidRequest,
	)
}
