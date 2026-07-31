package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEventIngressConfigValidatesGitHubRepositoryScope(t *testing.T) {
	t.Parallel()

	validSecret := strings.Repeat("g", githubWebhookMinSecretBytes)
	tests := []struct {
		name         string
		format       string
		repositories []string
		targetUser   string
		enabled      bool
		wantErr      string
	}{
		{
			name:         "watched repositories and target user",
			format:       EventWebhookFormatGitHub,
			repositories: []string{"scylladb/gocql", "scylladb/scylla-rust-driver"},
			targetUser:   "review-user",
			enabled:      true,
		},
		{
			name:         "empty scope keeps all repositories",
			format:       EventWebhookFormatGitHub,
			repositories: nil,
			enabled:      true,
		},
		{
			name:         "scope requires GitHub format",
			format:       EventWebhookFormatStandard,
			repositories: []string{"scylladb/gocql"},
			enabled:      true,
			wantErr:      "repository and target-user filters require GitHub format",
		},
		{
			name:       "whitespace target still requires GitHub format",
			format:     EventWebhookFormatStandard,
			targetUser: " ",
			enabled:    true,
			wantErr:    "repository and target-user filters require GitHub format",
		},
		{
			name:         "repository must be owner slash name",
			format:       EventWebhookFormatGitHub,
			repositories: []string{"scylladb"},
			enabled:      true,
			wantErr:      "must be a trimmed owner/repo",
		},
		{
			name:         "repository is exactly trimmed",
			format:       EventWebhookFormatGitHub,
			repositories: []string{" scylladb/gocql"},
			enabled:      true,
			wantErr:      "must be a trimmed owner/repo",
		},
		{
			name:         "repository duplicates ignore case",
			format:       EventWebhookFormatGitHub,
			repositories: []string{"scylladb/gocql", "ScyllaDB/GOCQL"},
			enabled:      true,
			wantErr:      "differ only by case",
		},
		{
			name:       "target user rejects leading hyphen",
			format:     EventWebhookFormatGitHub,
			targetUser: "-reviewer",
			enabled:    true,
			wantErr:    "GitHub target user must be a trimmed login",
		},
		{
			name:         "disabled connector scope remains inert",
			format:       EventWebhookFormatStandard,
			repositories: []string{"not a repository"},
			targetUser:   "-invalid",
			enabled:      false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			secret := validSecret
			if test.format == EventWebhookFormatStandard {
				secret = eventWebhookTestSecret("github-scope")
			}
			ingress := EventIngressConfig{
				Enabled: true,
				Webhooks: map[string]GenericWebhookConfig{
					"github": {
						Enabled:      test.enabled,
						Format:       test.format,
						Repositories: test.repositories,
						TargetUser:   test.targetUser,
						Secret:       *NewSecureString(secret),
					},
				},
			}
			err := ingress.Validate()
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestGitHubWebhookScopeJSONRoundTripAndEffectiveCopy(t *testing.T) {
	t.Parallel()

	input := GenericWebhookConfig{
		Enabled:           true,
		Format:            EventWebhookFormatGitHub,
		Repositories:      []string{"scylladb/gocql", "scylladb/python-driver"},
		TargetUser:        "reviewer",
		PollNotifications: true,
	}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var decoded GenericWebhookConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if decoded.TargetUser != input.TargetUser ||
		decoded.PollNotifications != input.PollNotifications ||
		len(decoded.Repositories) != len(input.Repositories) ||
		decoded.Repositories[1] != input.Repositories[1] {
		t.Fatalf("decoded webhook = %#v, want scope %#v", decoded, input)
	}

	cfg := DefaultConfig()
	cfg.Events.Ingress.Webhooks = map[string]GenericWebhookConfig{"github": input}
	effective := EffectiveEventIngressConfig(cfg, t.TempDir())
	effectiveWebhook := effective.Webhooks["github"]
	effectiveWebhook.Repositories[0] = "changed/repository"
	effective.Webhooks["github"] = effectiveWebhook
	if got := cfg.Events.Ingress.Webhooks["github"].Repositories[0]; got != "scylladb/gocql" {
		t.Fatalf("effective scope mutation changed source repository to %q", got)
	}
}

func TestEventIngressConfigAllowsOptInGitHubNotificationPollingWithoutWebhookSecret(
	t *testing.T,
) {
	t.Parallel()

	ingress := EventIngressConfig{
		Enabled: true,
		Webhooks: map[string]GenericWebhookConfig{
			"github": {
				Enabled:           true,
				Format:            EventWebhookFormatGitHub,
				Repositories:      []string{"scylladb/picoclaw"},
				TargetUser:        "reviewer",
				PollNotifications: true,
			},
		},
	}
	if err := ingress.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	connector := ingress.Webhooks["github"]
	connector.PollNotifications = false
	ingress.Webhooks["github"] = connector
	if err := ingress.Validate(); err == nil ||
		!strings.Contains(err.Error(), "GitHub secret") {
		t.Fatalf("Validate() error = %v, want missing GitHub webhook secret", err)
	}
}

func TestEventIngressConfigRejectsNotificationPollingForEnabledStandardConnector(
	t *testing.T,
) {
	t.Parallel()

	ingress := EventIngressConfig{
		Enabled: true,
		Webhooks: map[string]GenericWebhookConfig{
			"standard": {
				Enabled:           true,
				Format:            EventWebhookFormatStandard,
				PollNotifications: true,
				Secret: *NewSecureString(
					eventWebhookTestSecret("poll-standard"),
				),
			},
		},
	}
	if err := ingress.Validate(); err == nil ||
		!strings.Contains(err.Error(), "requires GitHub format") {
		t.Fatalf("Validate() error = %v, want GitHub-only polling rejection", err)
	}

	connector := ingress.Webhooks["standard"]
	connector.Enabled = false
	ingress.Webhooks["standard"] = connector
	if err := ingress.Validate(); err != nil {
		t.Fatalf("disabled connector should remain inert: %v", err)
	}
}

func TestGitHubWebhookScopeCannotPersistSensitiveIdentity(t *testing.T) {
	t.Parallel()

	secret := strings.Repeat("s", githubWebhookMinSecretBytes)
	ingress := EventIngressConfig{
		Enabled: true,
		Webhooks: map[string]GenericWebhookConfig{
			"github": {
				Enabled:      true,
				Format:       EventWebhookFormatGitHub,
				Repositories: []string{"owner/repo-" + secret},
				Secret:       *NewSecureString(secret),
			},
		},
	}
	err := ingress.Validate()
	if err == nil || err.Error() != genericWebhookConnectorSecretConflictMessage {
		t.Fatalf("Validate() error = %v, want opaque identity conflict", err)
	}
}
