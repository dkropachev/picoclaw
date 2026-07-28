package eventing

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRedactorRecursesAndNormalizesSensitiveKeys(t *testing.T) {
	t.Parallel()

	redactor := NewRedactor([]string{"TenantCredential"}, []string{"known-secret", "abc"})
	payload := json.RawMessage(`{
		"Authorization":"Bearer value",
		"proxy-authorization":"value",
		"nested":[{
			"accessToken":"value",
			"custom_service_secret":"value",
			"Tenant_Credential":"value",
			"github_signature":"value",
			"db-passwd":"value",
			"session-cookie":"value",
			"x-hub-signature-256":"value",
			"key":"safe",
			"message":"prefix known-secret suffix"
		}]
	}`)
	got, err := redactor.RedactJSON(payload)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"Authorization":"[REDACTED]",
		"proxy-authorization":"[REDACTED]",
		"nested":[{
			"accessToken":"[REDACTED]",
			"custom_service_secret":"[REDACTED]",
			"Tenant_Credential":"[REDACTED]",
			"github_signature":"[REDACTED]",
			"db-passwd":"[REDACTED]",
			"session-cookie":"[REDACTED]",
			"x-hub-signature-256":"[REDACTED]",
			"key":"safe",
			"message":"prefix [REDACTED] suffix"
		}]
	}`, string(got))
}

func TestRedactorEnvelopeAndNilSafety(t *testing.T) {
	t.Parallel()

	event := Envelope{
		Payload:    json.RawMessage(`{"safe":"top-secret","password":42}`),
		Attributes: map[string]string{"API-Key": "value", "safe": "top-secret"},
		Actor: &Actor{
			ID:         "top-secret",
			Attributes: map[string]string{"refreshToken": "value"},
		},
		Subject: &Subject{URL: "https://example.test/top-secret"},
	}
	var nilRedactor *Redactor
	_, err := nilRedactor.RedactJSON(event.Payload)
	require.NoError(t, err)

	got, err := NewRedactor(nil, []string{"top-secret"}).RedactEnvelope(event)
	require.NoError(t, err)
	assert.JSONEq(t, `{"safe":"[REDACTED]","password":"[REDACTED]"}`, string(got.Payload))
	assert.Equal(t, RedactedValue, got.Attributes["API-Key"])
	assert.Equal(t, RedactedValue, got.Attributes["safe"])
	assert.Equal(t, RedactedValue, got.Actor.ID)
	assert.Equal(t, RedactedValue, got.Actor.Attributes["refreshToken"])
	assert.Equal(t, "https://example.test/[REDACTED]", got.Subject.URL)
	assert.Equal(t, "top-secret", stringValueFromJSON(t, event.Payload, "safe"))
}

func TestRedactorHonorsShortSecretsWithoutRescanningMarkers(t *testing.T) {
	t.Parallel()

	redactor := NewRedactor(nil, []string{"x", "REDA"})
	once := redactor.RedactText("x REDA")
	assert.Equal(
		t,
		"[REDACTED] [REDACTED]",
		once,
	)
	assert.Equal(t, once, redactor.RedactText(once))
}

func TestRedactorReplacesSecretsThatOverlapMarker(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		secret string
		value  string
		want   string
	}{
		{
			name:   "contains marker",
			secret: "before[REDACTED]after",
			value:  "before[REDACTED]after",
			want:   RedactedValue,
		},
		{
			name:   "starts inside marker",
			secret: "REDACTED]after",
			value:  "[REDACTED]after",
			want:   RedactedValue,
		},
		{
			name:   "ends inside marker",
			secret: "before[REDACTED",
			value:  "before[REDACTED]",
			want:   RedactedValue,
		},
		{
			name:   "contained by marker",
			secret: "REDA",
			value:  RedactedValue,
			want:   RedactedValue,
		},
		{
			name:   "overlapping secrets",
			secret: "aba",
			value:  "ababa",
			want:   RedactedValue,
		},
		{
			name:   "adjacent markers canonicalize stably",
			secret: "REDA",
			value:  RedactedValue + RedactedValue,
			want:   RedactedValue,
		},
		{
			name:   "replacement synthesizes left boundary match",
			secret: "a[",
			value:  "aa[",
			want:   RedactedValue,
		},
		{
			name:   "replacement synthesizes right boundary match",
			secret: "]a",
			value:  "]aa",
			want:   RedactedValue,
		},
		{
			name:   "pathological boundary cascade fails closed",
			secret: "a[",
			value:  "aaaaaaaaaaaaaaaa[",
			want:   RedactedValue,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			redactor := NewRedactor(nil, []string{test.secret})
			once := redactor.RedactText(test.value)
			assert.Equal(t, test.want, once)
			assert.Equal(t, once, redactor.RedactText(once))
			if !strings.Contains(RedactedValue, test.secret) {
				assert.NotContains(t, once, test.secret)
			}
		})
	}
}

func TestRedactorRejectsConfiguredSecretsInKeys(t *testing.T) {
	t.Parallel()

	redactor := NewRedactor(nil, []string{"known-secret"})
	_, err := redactor.RedactJSON(json.RawMessage(
		`{"nested":{"prefix-known-secret-suffix":"value"}}`,
	))
	assert.ErrorIs(t, err, ErrInvalidEnvelope)

	event := Envelope{
		Payload:    json.RawMessage(`{}`),
		Attributes: map[string]string{"known-secret": "value"},
	}
	_, err = redactor.RedactEnvelope(event)
	assert.ErrorIs(t, err, ErrInvalidEnvelope)
}

func TestRedactorHonorsPunctuationOnlyConfiguredKeys(t *testing.T) {
	t.Parallel()

	redactor := NewRedactor([]string{"***", "🔒"}, nil)
	got, err := redactor.RedactJSON(json.RawMessage(
		`{"***":"first","🔒":"second","safe":"value"}`,
	))
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"***":"[REDACTED]",
		"🔒":"[REDACTED]",
		"safe":"value"
	}`, string(got))
}

func TestRedactorExactSecretOutputIsFixedPoint(t *testing.T) {
	t.Parallel()

	values := redactionTestStrings([]byte{'a', '[', ']', 'R'}, 4)
	secrets := redactionTestStrings([]byte{'a', '[', ']', 'R'}, 3)[1:]
	for _, secret := range secrets {
		redactor := NewRedactor(nil, []string{secret})
		for _, value := range values {
			once := redactor.RedactText(value)
			if twice := redactor.RedactText(once); twice != once {
				t.Fatalf(
					"redaction is not idempotent: secret=%q value=%q once=%q twice=%q",
					secret,
					value,
					once,
					twice,
				)
			}
			if secretOutsideRedactionMarker(once, secret) {
				t.Fatalf(
					"redaction left secret outside marker: secret=%q value=%q output=%q",
					secret,
					value,
					once,
				)
			}
		}
	}
}

func TestRedactorMultiSecretBoundarySynthesisIsFixedPoint(t *testing.T) {
	t.Parallel()

	redactor := NewRedactor(nil, []string{"seed", "prefix" + RedactedValue})
	once := redactor.RedactText("prefixseed")
	assert.Equal(t, RedactedValue, once)
	assert.Equal(t, once, redactor.RedactText(once))
}

func TestRedactorManyDisjointMatchesFailClosed(t *testing.T) {
	t.Parallel()

	redactor := NewRedactor(nil, []string{"a"})
	value := strings.Repeat("ab", maxRedactionIntervals+1)
	assert.Equal(t, RedactedValue, redactor.RedactText(value))
}

func TestRedactorZeroValueStillEnforcesMandatoryKeys(t *testing.T) {
	t.Parallel()

	var redactor Redactor
	got, err := redactor.RedactJSON(json.RawMessage(`{"password":"secret","safe":"value"}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"password":"[REDACTED]","safe":"value"}`, string(got))
}

func redactionTestStrings(alphabet []byte, maxLength int) []string {
	capacity := 1
	width := 1
	for range maxLength {
		width *= len(alphabet)
		capacity += width
	}
	values := make([]string, 1, capacity)
	values[0] = ""
	frontier := []string{""}
	for range maxLength {
		next := make([]string, 0, len(frontier)*len(alphabet))
		for _, prefix := range frontier {
			for _, char := range alphabet {
				next = append(next, prefix+string(char))
			}
		}
		values = append(values, next...)
		frontier = next
	}
	return values
}

func secretOutsideRedactionMarker(value, secret string) bool {
	for offset := 0; offset < len(value); {
		index := strings.Index(value[offset:], secret)
		if index < 0 {
			return false
		}
		start := offset + index
		end := start + len(secret)
		covered := false
		for markerOffset := 0; markerOffset < len(value); {
			markerIndex := strings.Index(value[markerOffset:], RedactedValue)
			if markerIndex < 0 {
				break
			}
			markerStart := markerOffset + markerIndex
			if start >= markerStart && end <= markerStart+len(RedactedValue) {
				covered = true
				break
			}
			markerOffset = markerStart + 1
		}
		if !covered {
			return true
		}
		offset = start + 1
	}
	return false
}

func stringValueFromJSON(t *testing.T, payload []byte, key string) string {
	t.Helper()
	var object map[string]any
	require.NoError(t, json.Unmarshal(payload, &object))
	value, _ := object[key].(string)
	return value
}
