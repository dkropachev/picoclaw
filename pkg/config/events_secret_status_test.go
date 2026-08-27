package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEventWebhookSecretStatusDoesNotExposeCredential(t *testing.T) {
	root := t.TempDir()
	updateResolver(root)
	t.Cleanup(func() { updateResolver("") })

	standard := eventWebhookTestSecret("status-standard")
	github := strings.Repeat("g", githubWebhookMinSecretBytes)
	files := map[string]string{
		"standard-valid":   standard,
		"standard-invalid": "not-a-standard-secret",
		"github-valid":     github,
		"github-invalid":   "short",
	}
	for name, value := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name   string
		format string
		secret SecureString
		want   WebhookSecretStatus
	}{
		{
			name: "standard empty", format: EventWebhookFormatStandard,
			want: EventWebhookSecretUnconfigured,
		},
		{
			name: "standard invalid literal", format: EventWebhookFormatStandard,
			secret: unresolvedEventWebhookSecret("not-a-standard-secret"),
			want:   EventWebhookSecretInvalid,
		},
		{
			name: "standard unresolved reference", format: EventWebhookFormatStandard,
			secret: unresolvedEventWebhookSecret("file://missing-standard"),
			want:   EventWebhookSecretUnreachable,
		},
		{
			name: "standard readable invalid reference", format: EventWebhookFormatStandard,
			secret: unresolvedEventWebhookSecret("file://standard-invalid"),
			want:   EventWebhookSecretInvalid,
		},
		{
			name: "standard valid literal", format: EventWebhookFormatStandard,
			secret: unresolvedEventWebhookSecret(standard),
			want:   EventWebhookSecretAvailable,
		},
		{
			name: "standard readable valid reference", format: EventWebhookFormatStandard,
			secret: unresolvedEventWebhookSecret("file://standard-valid"),
			want:   EventWebhookSecretAvailable,
		},
		{
			name: "github empty", format: EventWebhookFormatGitHub,
			want: EventWebhookSecretUnconfigured,
		},
		{
			name: "github invalid literal", format: EventWebhookFormatGitHub,
			secret: unresolvedEventWebhookSecret("short"),
			want:   EventWebhookSecretInvalid,
		},
		{
			name: "github unresolved reference", format: EventWebhookFormatGitHub,
			secret: unresolvedEventWebhookSecret("file://missing-github"),
			want:   EventWebhookSecretUnreachable,
		},
		{
			name: "github readable invalid reference", format: EventWebhookFormatGitHub,
			secret: unresolvedEventWebhookSecret("file://github-invalid"),
			want:   EventWebhookSecretInvalid,
		},
		{
			name: "github valid literal", format: EventWebhookFormatGitHub,
			secret: unresolvedEventWebhookSecret(github),
			want:   EventWebhookSecretAvailable,
		},
		{
			name: "github readable valid reference", format: EventWebhookFormatGitHub,
			secret: unresolvedEventWebhookSecret("file://github-valid"),
			want:   EventWebhookSecretAvailable,
		},
		{
			name: "unknown format", format: "unknown",
			secret: unresolvedEventWebhookSecret(standard),
			want:   EventWebhookSecretInvalid,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := test.secret.String()
			webhook := GenericWebhookConfig{Format: test.format, Secret: test.secret}
			if got := EventWebhookSecretStatus(webhook); got != test.want {
				t.Fatalf("EventWebhookSecretStatus() = %q, want %q", got, test.want)
			}
			if after := webhook.Secret.String(); after != before {
				t.Fatal("EventWebhookSecretStatus mutated or resolved the credential")
			}
		})
	}
}

func TestEventConfigurationCoverageMargin(t *testing.T) {
	var webhook GenericWebhookConfig
	if err := webhook.UnmarshalJSON([]byte("{")); err == nil {
		t.Fatal("malformed webhook JSON decoded")
	}
	if err := webhook.UnmarshalJSON([]byte(`{"secret":{"invalid":true}}`)); err == nil {
		t.Fatal("non-string webhook secret decoded")
	}

	repositories := make([]string, githubWebhookMaxRepositories+1)
	for index := range repositories {
		repositories[index] = "owner/repository"
	}
	if err := validateGitHubWebhookScope(GenericWebhookConfig{
		Format:       EventWebhookFormatGitHub,
		Repositories: repositories,
	}); err == nil {
		t.Fatal("oversized GitHub repository scope validated")
	}
	if err := validateEventWebhookSecret(GenericWebhookConfig{
		Format: "unsupported",
	}); err == nil {
		t.Fatal("unsupported webhook format validated")
	}

	if got := effectiveEventChannelType("mailbox", nil); got != "" {
		t.Fatalf("nil channel type = %q", got)
	}
	if got := effectiveEventChannelType("mailbox", &Channel{}); got != "mailbox" {
		t.Fatalf("implicit channel type = %q", got)
	}
	logging := EffectiveEventLoggingConfig(nil)
	if logging.MinSeverity != "info" || len(logging.Include) == 0 {
		t.Fatalf("default event logging = %#v", logging)
	}
}
