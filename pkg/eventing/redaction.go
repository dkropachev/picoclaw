package eventing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

const (
	RedactedValue         = "[REDACTED]"
	maxRedactionPasses    = 8
	maxRedactionIntervals = 4096
)

var builtInSensitiveKeys = map[string]struct{}{
	"authorization":      {},
	"proxyauthorization": {},
	"cookie":             {},
	"setcookie":          {},
	"password":           {},
	"passwd":             {},
	"secret":             {},
	"token":              {},
	"accesstoken":        {},
	"refreshtoken":       {},
	"apikey":             {},
	"clientsecret":       {},
	"privatekey":         {},
	"webhooksecret":      {},
	"signature":          {},
	"xhubsignature":      {},
	"xhubsignature256":   {},
}

// Redactor recursively removes sensitive values before an event reaches disk.
// Key matching ignores case and camelCase, hyphen, underscore, whitespace, and
// punctuation differences.
type Redactor struct {
	keys      map[string]struct{}
	exactKeys map[string]struct{}
	secrets   []string
}

// NewRedactor returns a redactor with mandatory built-in keys plus additional
// installation-specific keys and exact secret values. Empty secret values are
// ignored. All other values, including short secrets, are honored. Overlapping
// secret occurrences and existing markers are coalesced so every configured
// secret is removed without corrupting markers during later passes such as
// replay.
func NewRedactor(additionalKeys, secretValues []string) *Redactor {
	keys := make(map[string]struct{}, len(builtInSensitiveKeys)+len(additionalKeys))
	exactKeys := make(map[string]struct{}, len(additionalKeys))
	for key := range builtInSensitiveKeys {
		keys[key] = struct{}{}
	}
	for _, key := range additionalKeys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		exactKeys[key] = struct{}{}
		if normalized := normalizeSensitiveKey(key); normalized != "" {
			keys[normalized] = struct{}{}
		}
	}

	seenSecrets := make(map[string]struct{}, len(secretValues))
	secrets := make([]string, 0, len(secretValues))
	for _, secret := range secretValues {
		if secret == "" {
			continue
		}
		if _, exists := seenSecrets[secret]; exists {
			continue
		}
		seenSecrets[secret] = struct{}{}
		secrets = append(secrets, secret)
	}
	sort.Slice(secrets, func(i, j int) bool {
		return len(secrets[i]) > len(secrets[j])
	})

	return &Redactor{
		keys:      keys,
		exactKeys: exactKeys,
		secrets:   append([]string(nil), secrets...),
	}
}

// RedactEnvelope returns a deep-copy of event with sensitive payload and
// metadata values removed.
func (r *Redactor) RedactEnvelope(event Envelope) (Envelope, error) {
	if r == nil {
		r = NewRedactor(nil, nil)
	}
	out := event.Clone()
	payload, err := r.RedactJSON(out.Payload)
	if err != nil {
		return Envelope{}, err
	}
	out.Payload = payload
	out.Attributes, err = r.redactStringMap("attributes", out.Attributes)
	if err != nil {
		return Envelope{}, err
	}
	if out.Actor != nil {
		out.Actor.ID = r.redactString(out.Actor.ID)
		out.Actor.Type = r.redactString(out.Actor.Type)
		out.Actor.DisplayName = r.redactString(out.Actor.DisplayName)
		out.Actor.Attributes, err = r.redactStringMap("actor.attributes", out.Actor.Attributes)
		if err != nil {
			return Envelope{}, err
		}
	}
	if out.Subject != nil {
		out.Subject.ID = r.redactString(out.Subject.ID)
		out.Subject.Type = r.redactString(out.Subject.Type)
		out.Subject.Name = r.redactString(out.Subject.Name)
		out.Subject.URL = r.redactString(out.Subject.URL)
		out.Subject.Attributes, err = r.redactStringMap("subject.attributes", out.Subject.Attributes)
		if err != nil {
			return Envelope{}, err
		}
	}
	return out, nil
}

// RedactJSON recursively redacts a JSON object while preserving JSON value
// types for non-sensitive fields.
func (r *Redactor) RedactJSON(payload json.RawMessage) (json.RawMessage, error) {
	if r == nil {
		r = NewRedactor(nil, nil)
	}
	if err := validateJSONObject(payload); err != nil {
		return nil, err
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	redacted, err := r.redactValue(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(redacted)
}

func (r *Redactor) redactValue(value any) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if r.containsSecret(key) {
				return nil, fmt.Errorf(
					"%w: JSON object key contains a configured secret",
					ErrInvalidEnvelope,
				)
			}
			if r.sensitiveKey(key) {
				out[key] = RedactedValue
				continue
			}
			redacted, err := r.redactValue(item)
			if err != nil {
				return nil, err
			}
			out[key] = redacted
		}
		return out, nil
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			redacted, err := r.redactValue(item)
			if err != nil {
				return nil, err
			}
			out[i] = redacted
		}
		return out, nil
	case string:
		return r.redactString(typed), nil
	default:
		return value, nil
	}
}

func (r *Redactor) redactStringMap(
	field string,
	values map[string]string,
) (map[string]string, error) {
	if r == nil {
		r = NewRedactor(nil, nil)
	}
	if values == nil {
		return nil, nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		if r.containsSecret(key) {
			return nil, fmt.Errorf(
				"%w: %s key contains a configured secret",
				ErrInvalidEnvelope,
				field,
			)
		}
		if r.sensitiveKey(key) {
			out[key] = RedactedValue
			continue
		}
		out[key] = r.redactString(value)
	}
	return out, nil
}

func (r *Redactor) redactString(value string) string {
	if r == nil || len(r.secrets) == 0 {
		return value
	}
	for range maxRedactionPasses {
		redacted := r.redactStringPass(value)
		if redacted == value {
			return value
		}
		value = redacted
	}
	// A pathological secret can be synthesized repeatedly where a replacement
	// marker meets the remaining text. Fail closed instead of returning a
	// non-idempotent or partially redacted field.
	return RedactedValue
}

func (r *Redactor) redactStringPass(value string) string {
	intervals, overflow := appendRedactionIntervals(nil, value, RedactedValue)
	if overflow {
		return RedactedValue
	}
	for _, secret := range r.secrets {
		intervals, overflow = appendRedactionIntervals(intervals, value, secret)
		if overflow {
			return RedactedValue
		}
	}
	if len(intervals) == 0 {
		return value
	}
	sort.Slice(intervals, func(i, j int) bool {
		if intervals[i].start == intervals[j].start {
			return intervals[i].end > intervals[j].end
		}
		return intervals[i].start < intervals[j].start
	})

	var out strings.Builder
	cursor := 0
	start := intervals[0].start
	end := intervals[0].end
	for _, interval := range intervals[1:] {
		if interval.start <= end {
			if interval.end > end {
				end = interval.end
			}
			continue
		}
		out.WriteString(value[cursor:start])
		out.WriteString(RedactedValue)
		cursor = end
		start = interval.start
		end = interval.end
	}
	out.WriteString(value[cursor:start])
	out.WriteString(RedactedValue)
	out.WriteString(value[end:])
	return out.String()
}

type redactionInterval struct {
	start int
	end   int
}

func appendRedactionIntervals(
	intervals []redactionInterval,
	value string,
	pattern string,
) ([]redactionInterval, bool) {
	if pattern == "" {
		return intervals, false
	}
	firstPatternInterval := len(intervals)
	for offset := 0; offset < len(value); {
		index := strings.Index(value[offset:], pattern)
		if index < 0 {
			break
		}
		start := offset + index
		end := start + len(pattern)
		if len(intervals) > firstPatternInterval &&
			start <= intervals[len(intervals)-1].end {
			if end > intervals[len(intervals)-1].end {
				intervals[len(intervals)-1].end = end
			}
		} else {
			if len(intervals) >= maxRedactionIntervals {
				return nil, true
			}
			intervals = append(intervals, redactionInterval{start: start, end: end})
		}
		offset = start + 1
	}
	return intervals, false
}

// RedactText replaces configured secret values in non-JSON operational text
// such as durable error details.
func (r *Redactor) RedactText(value string) string {
	return r.redactString(value)
}

func (r *Redactor) containsSecret(value string) bool {
	if r == nil {
		return false
	}
	for _, secret := range r.secrets {
		if strings.Contains(value, secret) {
			return true
		}
	}
	return false
}

func (r *Redactor) sensitiveKey(key string) bool {
	if r != nil {
		if _, exists := r.exactKeys[key]; exists {
			return true
		}
	}
	normalized := normalizeSensitiveKey(key)
	if _, exists := builtInSensitiveKeys[normalized]; exists {
		return true
	}
	if r != nil {
		if _, exists := r.keys[normalized]; exists {
			return true
		}
	}
	for _, suffix := range []string{
		"authorization",
		"cookie",
		"password",
		"passwd",
		"secret",
		"token",
		"apikey",
		"privatekey",
		"signature",
	} {
		if strings.HasSuffix(normalized, suffix) {
			return true
		}
	}
	return false
}

func normalizeSensitiveKey(key string) string {
	var out strings.Builder
	for _, char := range strings.TrimSpace(key) {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			out.WriteRune(unicode.ToLower(char))
		}
	}
	return out.String()
}
