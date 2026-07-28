package eventing

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeEnvelopeAssignsFieldsAndClones(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 12, 0, 0, 123, time.FixedZone("test", -4*60*60))
	occurredAt := now.Add(-time.Minute)
	payload := json.RawMessage(`{"nested":{"value":"original"}}`)
	attributes := map[string]string{"tenant": "one"}
	actorAttributes := map[string]string{"role": "bot"}
	input := Envelope{
		Source:     " github ",
		Connector:  " production ",
		Type:       " issues.opened ",
		DedupeKey:  " delivery-1 ",
		OccurredAt: &occurredAt,
		Payload:    payload,
		Attributes: attributes,
		Actor: &Actor{
			ID:         "actor-1",
			Attributes: actorAttributes,
		},
	}

	got, err := NormalizeEnvelope(input, now)
	require.NoError(t, err)
	assert.Regexp(t, `^ev_[0-9a-f]{32}$`, got.ID)
	assert.Equal(t, "github", got.Source)
	assert.Equal(t, "production", got.Connector)
	assert.Equal(t, "issues.opened", got.Type)
	assert.Equal(t, "delivery-1", got.DedupeKey)
	assert.Equal(t, now.UTC(), got.ReceivedAt)
	require.NotNil(t, got.OccurredAt)
	assert.Equal(t, occurredAt.UTC(), *got.OccurredAt)

	payload[2] = 'X'
	attributes["tenant"] = "changed"
	actorAttributes["role"] = "changed"
	occurredAt = occurredAt.Add(time.Hour)
	assert.JSONEq(t, `{"nested":{"value":"original"}}`, string(got.Payload))
	assert.Equal(t, "one", got.Attributes["tenant"])
	assert.Equal(t, "bot", got.Actor.Attributes["role"])
	assert.NotEqual(t, occurredAt, *got.OccurredAt)

	clone := got.Clone()
	clone.Payload[2] = 'X'
	clone.Attributes["tenant"] = "clone"
	clone.Actor.Attributes["role"] = "clone"
	assert.JSONEq(t, `{"nested":{"value":"original"}}`, string(got.Payload))
	assert.Equal(t, "one", got.Attributes["tenant"])
	assert.Equal(t, "bot", got.Actor.Attributes["role"])
}

func TestNormalizeEnvelopeValidation(t *testing.T) {
	t.Parallel()

	valid := Envelope{
		Source:    "github",
		Connector: "default",
		Type:      "issue.opened",
		DedupeKey: "delivery",
		Payload:   json.RawMessage(`{}`),
	}
	tests := []struct {
		name   string
		mutate func(*Envelope)
	}{
		{"source required", func(event *Envelope) { event.Source = "" }},
		{"connector required", func(event *Envelope) { event.Connector = "" }},
		{"type required", func(event *Envelope) { event.Type = "" }},
		{"dedupe key required", func(event *Envelope) { event.DedupeKey = "" }},
		{"invalid ID prefix", func(event *Envelope) { event.ID = "event_deadbeef" }},
		{"invalid ID shape", func(event *Envelope) { event.ID = "ev_deadbeef" }},
		{"invalid replay ID", func(event *Envelope) { event.ReplayOf = "ev_bad" }},
		{"empty payload", func(event *Envelope) { event.Payload = nil }},
		{"null payload", func(event *Envelope) { event.Payload = json.RawMessage(`null`) }},
		{"array payload", func(event *Envelope) { event.Payload = json.RawMessage(`[]`) }},
		{"scalar payload", func(event *Envelope) { event.Payload = json.RawMessage(`1`) }},
		{"trailing payload", func(event *Envelope) { event.Payload = json.RawMessage(`{} garbage`) }},
		{"multiple payloads", func(event *Envelope) { event.Payload = json.RawMessage(`{} {}`) }},
		{"oversized source", func(event *Envelope) { event.Source = strings.Repeat("s", maxSourceLength+1) }},
		{"too many attributes", func(event *Envelope) {
			event.Attributes = make(map[string]string, maxAttributeCount+1)
			for i := 0; i <= maxAttributeCount; i++ {
				event.Attributes[string(rune(i+1))] = "value"
			}
		}},
		{"empty attribute key", func(event *Envelope) { event.Attributes = map[string]string{" ": "value"} }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := valid.Clone()
			test.mutate(&event)
			_, err := NormalizeEnvelope(event, time.Now())
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidEnvelope)
		})
	}
}

func TestEnvelopeValidateRequiresPreassignedID(t *testing.T) {
	t.Parallel()

	event := Envelope{
		Source:    "github",
		Connector: "default",
		Type:      "issue.opened",
		DedupeKey: "delivery",
		Payload:   json.RawMessage(`{}`),
	}
	assert.ErrorIs(t, event.Validate(), ErrInvalidEnvelope)

	normalized, err := NormalizeEnvelope(event, time.Now())
	require.NoError(t, err)
	assert.NoError(t, normalized.Validate())
	assert.False(t, errors.Is(normalized.Validate(), ErrInvalidEnvelope))
}
