package eventing

import (
	"encoding/json"
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

func stringValueFromJSON(t *testing.T, payload []byte, key string) string {
	t.Helper()
	var object map[string]any
	require.NoError(t, json.Unmarshal(payload, &object))
	value, _ := object[key].(string)
	return value
}
