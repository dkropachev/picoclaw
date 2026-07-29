package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
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

func TestEventIngressConfigRejectsExternalSecretsInPublicIdentities(t *testing.T) {
	t.Parallel()
	const secret = "external-model-credential"

	tests := []struct {
		name    string
		ingress EventIngressConfig
		want    string
	}{
		{
			name: "webhook name",
			ingress: EventIngressConfig{
				Webhooks: map[string]GenericWebhookConfig{
					"hook-" + secret: {},
				},
			},
			want: genericWebhookConnectorSecretConflictMessage,
		},
		{
			name: "webhook format",
			ingress: EventIngressConfig{
				Webhooks: map[string]GenericWebhookConfig{
					"hook": {Format: "format-" + secret},
				},
			},
			want: genericWebhookConnectorSecretConflictMessage,
		},
		{
			name: "channel name",
			ingress: EventIngressConfig{
				Channels: map[string]ChannelEventIngressConfig{
					"channel-" + secret: {},
				},
			},
			want: eventChannelSecretConflictMessage,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := test.ingress.ValidatePublicIdentities(secret)
			if err == nil || err.Error() != test.want {
				t.Fatalf(
					"ValidatePublicIdentities() error = %v, want %q",
					err,
					test.want,
				)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatal("validation error exposed an external credential")
			}
		})
	}
}

func TestSaveConfigRejectsExternalSecretInEventPublicIdentity(t *testing.T) {
	t.Parallel()
	const secret = "external-model-credential"

	path := filepath.Join(t.TempDir(), "config.json")
	cfg := DefaultConfig()
	cfg.ModelList = []*ModelConfig{{
		ModelName: "protected",
		Model:     "openai/gpt-4o",
		APIKeys:   SimpleSecureStrings(secret),
	}}
	cfg.Events.Ingress.Webhooks = map[string]GenericWebhookConfig{
		"hook-" + secret: {},
	}

	err := SaveConfig(path, cfg)
	if err == nil || err.Error() != genericWebhookConnectorSecretConflictMessage {
		t.Fatalf("SaveConfig() error = %v, want opaque identity conflict", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("save error exposed an external credential")
	}
	for _, candidate := range []string{path, securityPath(path)} {
		if _, statErr := os.Stat(candidate); !os.IsNotExist(statErr) {
			t.Fatalf(
				"%s was persisted before public identity validation: %v",
				candidate,
				statErr,
			)
		}
	}
}
