package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

func connectorNameSafeEventWebhookSecret(fill string) string {
	return genericWebhookSecretPrefix +
		base64.StdEncoding.EncodeToString([]byte(strings.Repeat(fill, 33)))
}

func TestEventIngressConfigRejectsSecretBearingConnectorIdentity(t *testing.T) {
	t.Parallel()
	firstSecret := connectorNameSafeEventWebhookSecret("A")
	secondSecret := connectorNameSafeEventWebhookSecret("B")
	credentialBearingName := "prefix-" + firstSecret + "-suffix"
	invalidIdentity := strings.Repeat("V", 33)
	tests := []struct {
		name           string
		masterDisabled bool
		webhooks       map[string]GenericWebhookConfig
	}{
		{
			name: "own secret",
			webhooks: map[string]GenericWebhookConfig{
				credentialBearingName: {
					Enabled: true,
					Secret:  *NewSecureString(firstSecret),
				},
			},
		},
		{
			name: "other enabled connector secret",
			webhooks: map[string]GenericWebhookConfig{
				"owner": {
					Enabled: true,
					Secret:  *NewSecureString(firstSecret),
				},
				credentialBearingName: {
					Enabled: true,
					Secret:  *NewSecureString(secondSecret),
				},
			},
		},
		{
			name: "other secret before invalid own secret",
			webhooks: map[string]GenericWebhookConfig{
				"owner": {
					Enabled: true,
					Secret:  *NewSecureString(firstSecret),
				},
				credentialBearingName: {
					Enabled: true,
					Secret:  *NewSecureString("invalid-own-secret"),
				},
			},
		},
		{
			name: "active invalid own secret",
			webhooks: map[string]GenericWebhookConfig{
				invalidIdentity: {
					Enabled: true,
					Secret:  *NewSecureString(invalidIdentity),
				},
			},
		},
		{
			name: "enabled secret in disabled connector name",
			webhooks: map[string]GenericWebhookConfig{
				"owner": {
					Enabled: true,
					Secret:  *NewSecureString(firstSecret),
				},
				credentialBearingName: {
					Enabled: false,
				},
			},
		},
		{
			name:           "master disabled canonical secret",
			masterDisabled: true,
			webhooks: map[string]GenericWebhookConfig{
				"owner": {
					Enabled: false,
					Secret:  *NewSecureString(firstSecret),
				},
				credentialBearingName: {
					Enabled: false,
				},
			},
		},
		{
			name: "disabled owner canonical secret",
			webhooks: map[string]GenericWebhookConfig{
				"owner": {
					Enabled: false,
					Secret:  *NewSecureString(firstSecret),
				},
				credentialBearingName: {
					Enabled: false,
				},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := (EventIngressConfig{
				Enabled:  !test.masterDisabled,
				Webhooks: test.webhooks,
			}).Validate()
			if err == nil {
				t.Fatal("Validate() error = nil, want connector identity conflict")
			}
			if err.Error() != genericWebhookConnectorSecretConflictMessage {
				t.Fatalf("Validate() error = %q, want generic identity conflict", err)
			}
			for connector, webhook := range test.webhooks {
				if strings.Contains(err.Error(), connector) {
					t.Fatal("Validate() error exposes a connector identity")
				}
				secret := webhook.Secret.String()
				if secret != "" && strings.Contains(err.Error(), secret) {
					t.Fatal("Validate() error exposes a signing credential")
				}
			}
		})
	}
}

func TestEventIngressConfigInactiveNoncanonicalSecretsRemainInert(t *testing.T) {
	t.Parallel()
	for _, secret := range []string{
		"file://missing-webhook-secret.txt",
		"enc://unavailable-webhook-secret",
		"not-a-standard-webhooks-secret",
	} {
		cfg := EventIngressConfig{
			Webhooks: map[string]GenericWebhookConfig{
				"not/a/runtime/name": {
					Enabled: true,
					Secret:  unresolvedEventWebhookSecret(secret),
				},
			},
		}
		if err := cfg.Validate(); err != nil {
			t.Fatal("inactive noncanonical secret was validated or resolved")
		}
	}
}
