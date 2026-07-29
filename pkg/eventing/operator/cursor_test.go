package operator

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

func TestEventCursorRoundTripAndFilterBinding(t *testing.T) {
	filter := eventFilterBinding{
		Source:        "github",
		Connector:     "primary",
		Type:          "issues.opened",
		RoutingStatus: eventing.RoutingPending,
	}
	encoded, err := encodeEventCursor(eventing.EventCursor{
		ReceivedAt: testTime,
		ID:         testEventID,
	}, filter)
	if err != nil {
		t.Fatalf("encodeEventCursor() error = %v", err)
	}
	decoded, err := decodeEventCursor(encoded, filter)
	if err != nil {
		t.Fatalf("decodeEventCursor() error = %v", err)
	}
	if decoded.ID != testEventID || !decoded.ReceivedAt.Equal(testTime) {
		t.Fatalf("decoded cursor = %#v", decoded)
	}

	mismatched := filter
	mismatched.Source = "email"
	_, err = decodeEventCursor(encoded, mismatched)
	requireErrorIs(t, err, ErrInvalidRequest)

	_, err = decodeDispatchCursor(encoded, dispatchFilterBinding{})
	requireErrorIs(t, err, ErrInvalidRequest)
}

func TestDispatchCursorRoundTripAndFilterBinding(t *testing.T) {
	filter := dispatchFilterBinding{
		EventID:     testEventID,
		WorkflowRef: "workflows/issues.yml",
		Status:      eventing.DispatchRunning,
	}
	encoded, err := encodeDispatchCursor(eventing.DispatchCursor{
		CreatedAt: testTime,
		ID:        testDispatchID,
	}, filter)
	if err != nil {
		t.Fatalf("encodeDispatchCursor() error = %v", err)
	}
	decoded, err := decodeDispatchCursor(encoded, filter)
	if err != nil {
		t.Fatalf("decodeDispatchCursor() error = %v", err)
	}
	if decoded.ID != testDispatchID || !decoded.CreatedAt.Equal(testTime) {
		t.Fatalf("decoded dispatch cursor = %#v", decoded)
	}

	mismatched := filter
	mismatched.Status = eventing.DispatchSucceeded
	_, err = decodeDispatchCursor(encoded, mismatched)
	requireErrorIs(t, err, ErrInvalidRequest)
}

func TestCursorStrictlyRejectsMalformedOrMismatchedWireValues(t *testing.T) {
	filter := eventFilterBinding{Source: "github"}
	valid := wireCursor{
		Version:      cursorVersion,
		Kind:         cursorKindEvents,
		FilterDigest: filterDigest(filter),
		UnixNano:     testTime.UnixNano(),
		ID:           testEventID,
	}
	validEncoded, err := encodeCursor(valid)
	if err != nil {
		t.Fatalf("encodeCursor() error = %v", err)
	}

	tests := map[string]string{
		"not base64url":           "*",
		"padding is noncanonical": validEncoded + "=",
		"oversized": strings.Repeat(
			"A",
			maxCursorBytes+1,
		),
		"unknown field": encodeRawCursor(t,
			`{"v":1,"kind":"events","filter":"x","unix_nano":1,"id":"`+
				testEventID+`","extra":true}`),
		"duplicate field": encodeRawCursor(t,
			`{"v":1,"v":1,"kind":"events","filter":"x","unix_nano":1,"id":"`+
				testEventID+`"}`),
		"noncanonical JSON": encodeRawCursor(t,
			`{ "v":1,"kind":"events","filter":"x","unix_nano":1,"id":"`+
				testEventID+`"}`),
	}

	invalidWireValues := map[string]wireCursor{
		"wrong version": {
			Version:      cursorVersion + 1,
			Kind:         valid.Kind,
			FilterDigest: valid.FilterDigest,
			UnixNano:     valid.UnixNano,
			ID:           valid.ID,
		},
		"wrong kind": {
			Version:      valid.Version,
			Kind:         cursorKindDispatch,
			FilterDigest: valid.FilterDigest,
			UnixNano:     valid.UnixNano,
			ID:           valid.ID,
		},
		"wrong filter digest": {
			Version:      valid.Version,
			Kind:         valid.Kind,
			FilterDigest: "different",
			UnixNano:     valid.UnixNano,
			ID:           valid.ID,
		},
		"zero timestamp": {
			Version:      valid.Version,
			Kind:         valid.Kind,
			FilterDigest: valid.FilterDigest,
			UnixNano:     0,
			ID:           valid.ID,
		},
		"invalid ID": {
			Version:      valid.Version,
			Kind:         valid.Kind,
			FilterDigest: valid.FilterDigest,
			UnixNano:     valid.UnixNano,
			ID:           "ev_invalid",
		},
	}
	for name, wire := range invalidWireValues {
		tests[name], err = encodeCursor(wire)
		if err != nil {
			t.Fatalf("encodeCursor(%s) error = %v", name, err)
		}
	}

	for name, encoded := range tests {
		t.Run(name, func(t *testing.T) {
			_, decodeErr := decodeEventCursor(encoded, filter)
			requireErrorIs(t, decodeErr, ErrInvalidRequest)
		})
	}
}

func TestCursorEncoderRejectsInvalidStorePositions(t *testing.T) {
	_, err := encodeEventCursor(eventing.EventCursor{
		ReceivedAt: time.Time{},
		ID:         testEventID,
	}, eventFilterBinding{})
	if err == nil {
		t.Fatal("encodeEventCursor(zero time) error = nil")
	}
	_, err = encodeEventCursor(eventing.EventCursor{
		ReceivedAt: testTime,
		ID:         "ev_invalid",
	}, eventFilterBinding{})
	if err == nil {
		t.Fatal("encodeEventCursor(invalid ID) error = nil")
	}
	_, err = encodeEventCursor(eventing.EventCursor{
		ReceivedAt: time.Date(3000, time.January, 1, 0, 0, 0, 0, time.UTC),
		ID:         testEventID,
	}, eventFilterBinding{})
	if err == nil {
		t.Fatal("encodeEventCursor(out-of-range time) error = nil")
	}
	_, err = encodeDispatchCursor(eventing.DispatchCursor{
		CreatedAt: testTime,
		ID:        testEventID,
	}, dispatchFilterBinding{})
	if err == nil {
		t.Fatal("encodeDispatchCursor(invalid ID) error = nil")
	}
}

func TestCursorJSONIsVersionedBase64URLWithoutPadding(t *testing.T) {
	encoded, err := encodeEventCursor(eventing.EventCursor{
		ReceivedAt: testTime,
		ID:         testEventID,
	}, eventFilterBinding{})
	if err != nil {
		t.Fatalf("encodeEventCursor() error = %v", err)
	}
	if strings.Contains(encoded, "=") {
		t.Fatalf("cursor %q contains padding", encoded)
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode raw cursor: %v", err)
	}
	var wire map[string]any
	if err = json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("decode cursor JSON: %v", err)
	}
	if wire["v"] != float64(cursorVersion) || wire["kind"] != cursorKindEvents {
		t.Fatalf("cursor wire = %#v", wire)
	}
}

func encodeRawCursor(t *testing.T, raw string) string {
	t.Helper()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}
