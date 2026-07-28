//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreReplayPreservesRedactionMarkers(t *testing.T) {
	t.Parallel()

	clock := newMutableClock(time.Date(2026, 7, 28, 16, 0, 0, 0, time.UTC))
	store, _ := openTestStore(t, clock, WithRedaction(nil, []string{"REDA"}))
	event := testEnvelope("replay-redaction")
	event.Payload = json.RawMessage(`{"message":"REDA"}`)
	event.Attributes = map[string]string{"message": "REDA"}

	original, err := store.Insert(context.Background(), event)
	require.NoError(t, err)
	firstReplay, err := store.Replay(context.Background(), original.Event.Envelope.ID)
	require.NoError(t, err)
	secondReplay, err := store.Replay(context.Background(), firstReplay.Event.Envelope.ID)
	require.NoError(t, err)

	for _, result := range []InsertResult{original, firstReplay, secondReplay} {
		assert.JSONEq(t, `{"message":"[REDACTED]"}`, string(result.Event.Envelope.Payload))
		assert.Equal(t, RedactedValue, result.Event.Envelope.Attributes["message"])
	}
	assert.Len(t, firstReplay.Event.Envelope.Payload, len(original.Event.Envelope.Payload))
	assert.Len(t, secondReplay.Event.Envelope.Payload, len(original.Event.Envelope.Payload))
}
