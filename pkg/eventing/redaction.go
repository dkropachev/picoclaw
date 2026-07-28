package eventing

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"unicode"
)

const RedactedValue = "[REDACTED]"

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
	keys     map[string]struct{}
	replacer *strings.Replacer
}

// NewRedactor returns a redactor with mandatory built-in keys plus additional
// installation-specific keys and exact secret values. Empty secret values are
// ignored. All other values, including short secrets, are honored. Longer
// values are matched first, and replacements are made in one pass so inserted
// redaction markers are never scanned again.
func NewRedactor(additionalKeys, secretValues []string) *Redactor {
	keys := make(map[string]struct{}, len(builtInSensitiveKeys)+len(additionalKeys))
	for key := range builtInSensitiveKeys {
		keys[key] = struct{}{}
	}
	for _, key := range additionalKeys {
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

	var replacer *strings.Replacer
	if len(secrets) > 0 {
		replacements := make([]string, 0, len(secrets)*2)
		for _, secret := range secrets {
			replacements = append(replacements, secret, RedactedValue)
		}
		replacer = strings.NewReplacer(replacements...)
	}

	return &Redactor{keys: keys, replacer: replacer}
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
	out.Attributes = r.redactStringMap(out.Attributes)
	if out.Actor != nil {
		out.Actor.ID = r.redactString(out.Actor.ID)
		out.Actor.Type = r.redactString(out.Actor.Type)
		out.Actor.DisplayName = r.redactString(out.Actor.DisplayName)
		out.Actor.Attributes = r.redactStringMap(out.Actor.Attributes)
	}
	if out.Subject != nil {
		out.Subject.ID = r.redactString(out.Subject.ID)
		out.Subject.Type = r.redactString(out.Subject.Type)
		out.Subject.Name = r.redactString(out.Subject.Name)
		out.Subject.URL = r.redactString(out.Subject.URL)
		out.Subject.Attributes = r.redactStringMap(out.Subject.Attributes)
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
	redacted := r.redactValue(value)
	return json.Marshal(redacted)
}

func (r *Redactor) redactValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if r.sensitiveKey(key) {
				out[key] = RedactedValue
				continue
			}
			out[key] = r.redactValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = r.redactValue(item)
		}
		return out
	case string:
		return r.redactString(typed)
	default:
		return value
	}
}

func (r *Redactor) redactStringMap(values map[string]string) map[string]string {
	if r == nil {
		r = NewRedactor(nil, nil)
	}
	if values == nil {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		if r.sensitiveKey(key) {
			out[key] = RedactedValue
			continue
		}
		out[key] = r.redactString(value)
	}
	return out
}

func (r *Redactor) redactString(value string) string {
	if r == nil || r.replacer == nil {
		return value
	}
	return r.replacer.Replace(value)
}

// RedactText replaces configured secret values in non-JSON operational text
// such as durable error details.
func (r *Redactor) RedactText(value string) string {
	return r.redactString(value)
}

func (r *Redactor) sensitiveKey(key string) bool {
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
