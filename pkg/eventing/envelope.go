// Package eventing provides durable ingestion and dispatch bookkeeping for
// external events. It is deliberately separate from pkg/events, which is the
// in-process runtime event bus.
package eventing

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	eventIDPrefix           = "ev_"
	maxSourceLength         = 128
	maxConnectorLength      = 256
	maxEventTypeLength      = 256
	maxDedupeKeyLength      = 1024
	maxEntityFieldLength    = 2048
	maxAttributeCount       = 128
	maxAttributeKeyLength   = 256
	maxAttributeValueLength = 8192
)

var (
	// ErrInvalidEnvelope reports an event that cannot be safely persisted.
	ErrInvalidEnvelope = errors.New("invalid event envelope")
	// ErrIDGeneration reports failure to obtain cryptographically secure
	// randomness for an automatically assigned event ID.
	ErrIDGeneration = errors.New("generate event ID")
)

// Actor identifies the person, service, or automation that caused an event.
type Actor struct {
	ID          string            `json:"id,omitempty"`
	Type        string            `json:"type,omitempty"`
	DisplayName string            `json:"display_name,omitempty"`
	Attributes  map[string]string `json:"attributes,omitempty"`
}

// Subject identifies the resource that an event concerns.
type Subject struct {
	ID         string            `json:"id,omitempty"`
	Type       string            `json:"type,omitempty"`
	Name       string            `json:"name,omitempty"`
	URL        string            `json:"url,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// Envelope is the connector-independent representation persisted by the
// durable inbox. Payload must be a JSON object, not an array or scalar.
//
// Treat Envelope values as immutable after normalization. Store entry and exit
// points clone all reference-backed fields so callers cannot mutate persisted
// or returned state.
type Envelope struct {
	ID         string            `json:"id"`
	Source     string            `json:"source"`
	Connector  string            `json:"connector"`
	Type       string            `json:"type"`
	DedupeKey  string            `json:"dedupe_key"`
	Actor      *Actor            `json:"actor,omitempty"`
	Subject    *Subject          `json:"subject,omitempty"`
	OccurredAt *time.Time        `json:"occurred_at,omitempty"`
	ReceivedAt time.Time         `json:"received_at"`
	Payload    json.RawMessage   `json:"payload"`
	Attributes map[string]string `json:"attributes,omitempty"`
	ReplayOf   string            `json:"replay_of,omitempty"`
}

// NormalizeEnvelope validates input, assigns missing server-owned fields, and
// returns a deep copy. now is used only when ReceivedAt is absent.
func NormalizeEnvelope(input Envelope, now time.Time) (Envelope, error) {
	event := input.Clone()
	event.ID = strings.TrimSpace(event.ID)
	event.Source = strings.TrimSpace(event.Source)
	event.Connector = strings.TrimSpace(event.Connector)
	event.Type = strings.TrimSpace(event.Type)
	event.DedupeKey = strings.TrimSpace(event.DedupeKey)
	event.ReplayOf = strings.TrimSpace(event.ReplayOf)

	switch {
	case event.Source == "":
		return Envelope{}, fmt.Errorf("%w: source is required", ErrInvalidEnvelope)
	case event.Connector == "":
		return Envelope{}, fmt.Errorf("%w: connector is required", ErrInvalidEnvelope)
	case event.Type == "":
		return Envelope{}, fmt.Errorf("%w: type is required", ErrInvalidEnvelope)
	case event.DedupeKey == "":
		return Envelope{}, fmt.Errorf("%w: dedupe_key is required", ErrInvalidEnvelope)
	}
	if err := validateBoundedString("source", event.Source, maxSourceLength); err != nil {
		return Envelope{}, err
	}
	if err := validateBoundedString("connector", event.Connector, maxConnectorLength); err != nil {
		return Envelope{}, err
	}
	if err := validateBoundedString("type", event.Type, maxEventTypeLength); err != nil {
		return Envelope{}, err
	}
	if err := validateBoundedString("dedupe_key", event.DedupeKey, maxDedupeKeyLength); err != nil {
		return Envelope{}, err
	}

	if event.ID == "" {
		id, err := newPrefixedID(eventIDPrefix)
		if err != nil {
			return Envelope{}, err
		}
		event.ID = id
	} else if !validEventID(event.ID) {
		return Envelope{}, fmt.Errorf("%w: id must be %q followed by 32 lowercase hexadecimal characters", ErrInvalidEnvelope, eventIDPrefix)
	}
	if event.ReplayOf != "" && !validEventID(event.ReplayOf) {
		return Envelope{}, fmt.Errorf("%w: replay_of is not a valid event ID", ErrInvalidEnvelope)
	}

	if now.IsZero() {
		now = time.Now()
	}
	if event.ReceivedAt.IsZero() {
		event.ReceivedAt = now
	}
	event.ReceivedAt = event.ReceivedAt.UTC()
	if event.OccurredAt != nil {
		occurredAt := event.OccurredAt.UTC()
		event.OccurredAt = &occurredAt
	}

	if err := validateJSONObject(event.Payload); err != nil {
		return Envelope{}, err
	}
	if err := validateActor(event.Actor); err != nil {
		return Envelope{}, err
	}
	if err := validateSubject(event.Subject); err != nil {
		return Envelope{}, err
	}
	if err := validateAttributes("attributes", event.Attributes); err != nil {
		return Envelope{}, err
	}
	event.Payload = cloneBytes(event.Payload)
	return event, nil
}

// Validate checks an already populated envelope without assigning fields.
func (e Envelope) Validate() error {
	if e.ID == "" {
		return fmt.Errorf("%w: id is required", ErrInvalidEnvelope)
	}
	_, err := NormalizeEnvelope(e, time.Time{})
	return err
}

// Clone returns a deep copy of e.
func (e Envelope) Clone() Envelope {
	out := e
	out.Payload = cloneBytes(e.Payload)
	out.Attributes = cloneStringMap(e.Attributes)
	if e.OccurredAt != nil {
		occurredAt := *e.OccurredAt
		out.OccurredAt = &occurredAt
	}
	if e.Actor != nil {
		actor := *e.Actor
		actor.Attributes = cloneStringMap(e.Actor.Attributes)
		out.Actor = &actor
	}
	if e.Subject != nil {
		subject := *e.Subject
		subject.Attributes = cloneStringMap(e.Subject.Attributes)
		out.Subject = &subject
	}
	return out
}

func validateJSONObject(payload json.RawMessage) error {
	if len(bytes.TrimSpace(payload)) == 0 {
		return fmt.Errorf("%w: payload is required", ErrInvalidEnvelope)
	}

	var object map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&object); err != nil {
		return fmt.Errorf("%w: payload must be valid JSON: %v", ErrInvalidEnvelope, err)
	}
	if object == nil {
		return fmt.Errorf("%w: payload must be a JSON object", ErrInvalidEnvelope)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("%w: payload contains trailing JSON", ErrInvalidEnvelope)
		}
		return fmt.Errorf("%w: payload contains trailing data: %v", ErrInvalidEnvelope, err)
	}
	return nil
}

func validEventID(id string) bool {
	if len(id) != len(eventIDPrefix)+32 || !strings.HasPrefix(id, eventIDPrefix) {
		return false
	}
	for _, char := range id[len(eventIDPrefix):] {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func validateActor(actor *Actor) error {
	if actor == nil {
		return nil
	}
	for field, value := range map[string]string{
		"actor.id":           actor.ID,
		"actor.type":         actor.Type,
		"actor.display_name": actor.DisplayName,
	} {
		if err := validateBoundedString(field, value, maxEntityFieldLength); err != nil {
			return err
		}
	}
	return validateAttributes("actor.attributes", actor.Attributes)
}

func validateSubject(subject *Subject) error {
	if subject == nil {
		return nil
	}
	for field, value := range map[string]string{
		"subject.id":   subject.ID,
		"subject.type": subject.Type,
		"subject.name": subject.Name,
		"subject.url":  subject.URL,
	} {
		if err := validateBoundedString(field, value, maxEntityFieldLength); err != nil {
			return err
		}
	}
	return validateAttributes("subject.attributes", subject.Attributes)
}

func validateAttributes(field string, attributes map[string]string) error {
	if len(attributes) > maxAttributeCount {
		return fmt.Errorf("%w: %s has more than %d entries", ErrInvalidEnvelope, field, maxAttributeCount)
	}
	for key, value := range attributes {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("%w: %s contains an empty key", ErrInvalidEnvelope, field)
		}
		if err := validateBoundedString(field+" key", key, maxAttributeKeyLength); err != nil {
			return err
		}
		if err := validateBoundedString(field+" value", value, maxAttributeValueLength); err != nil {
			return err
		}
	}
	return nil
}

func validateBoundedString(field, value string, maximum int) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s is not valid UTF-8", ErrInvalidEnvelope, field)
	}
	if len(value) > maximum {
		return fmt.Errorf("%w: %s exceeds %d bytes", ErrInvalidEnvelope, field, maximum)
	}
	return nil
}

func newPrefixedID(prefix string) (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("%w: %v", ErrIDGeneration, err)
	}
	return prefix + hex.EncodeToString(random[:]), nil
}

func cloneBytes(value []byte) []byte {
	if value == nil {
		return nil
	}
	return append([]byte(nil), value...)
}

func cloneStringMap(value map[string]string) map[string]string {
	if value == nil {
		return nil
	}
	out := make(map[string]string, len(value))
	for key, item := range value {
		out[key] = item
	}
	return out
}
