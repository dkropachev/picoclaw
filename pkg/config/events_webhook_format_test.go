package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestGenericWebhookFormatJSONCompatibility(t *testing.T) {
	standardSecret := eventWebhookTestSecret("legacy-json")
	tests := []struct {
		name       string
		webhook    GenericWebhookConfig
		wantJSON   string
		wantFormat string
	}{
		{
			name:       "legacy Standard webhook remains byte-for-byte compatible",
			webhook:    GenericWebhookConfig{Enabled: true, Secret: *NewSecureString(standardSecret)},
			wantJSON:   `{"enabled":true,"secret":"[NOT_HERE]"}`,
			wantFormat: EventWebhookFormatStandard,
		},
		{
			name: "explicit GitHub format is persisted",
			webhook: GenericWebhookConfig{
				Enabled: true,
				Format:  EventWebhookFormatGitHub,
				Secret:  *NewSecureString(strings.Repeat("g", githubWebhookMinSecretBytes)),
			},
			wantJSON:   `{"enabled":true,"format":"github","secret":"[NOT_HERE]"}`,
			wantFormat: EventWebhookFormatGitHub,
		},
		{
			name:       "legacy connector without secret stays unchanged",
			webhook:    GenericWebhookConfig{Enabled: false},
			wantJSON:   `{"enabled":false}`,
			wantFormat: EventWebhookFormatStandard,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(test.webhook)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			if string(encoded) != test.wantJSON {
				t.Fatalf("json.Marshal() = %s, want %s", encoded, test.wantJSON)
			}

			var decoded GenericWebhookConfig
			if err = json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}
			if got := EffectiveEventWebhookFormat(decoded); got != test.wantFormat {
				t.Fatalf("effective decoded format = %q, want %q", got, test.wantFormat)
			}
		})
	}
}

func TestEffectiveEventIngressConfigResolvesWebhookFormatsIndependently(t *testing.T) {
	cfg := &Config{
		Events: EventsConfig{
			Ingress: EventIngressConfig{
				Webhooks: map[string]GenericWebhookConfig{
					"legacy": {Enabled: true},
					"github": {
						Enabled: true,
						Format:  EventWebhookFormatGitHub,
					},
				},
			},
		},
	}

	effective := EffectiveEventIngressConfig(cfg, t.TempDir())
	if got := effective.Webhooks["legacy"].Format; got != EventWebhookFormatStandard {
		t.Fatalf("effective legacy format = %q, want %q", got, EventWebhookFormatStandard)
	}
	if got := effective.Webhooks["github"].Format; got != EventWebhookFormatGitHub {
		t.Fatalf("effective GitHub format = %q, want %q", got, EventWebhookFormatGitHub)
	}
	if got := cfg.Events.Ingress.Webhooks["legacy"].Format; got != "" {
		t.Fatalf("effective resolution mutated source format to %q", got)
	}

	legacy := effective.Webhooks["legacy"]
	legacy.Format = EventWebhookFormatGitHub
	effective.Webhooks["legacy"] = legacy
	if got := cfg.Events.Ingress.Webhooks["legacy"].Format; got != "" {
		t.Fatalf("effective map mutation changed source format to %q", got)
	}
}

func TestEventIngressConfigValidateWebhookFormats(t *testing.T) {
	validStandard := eventWebhookTestSecret("format-valid")
	validGitHub := strings.Repeat("G", githubWebhookMinSecretBytes)

	tests := []struct {
		name    string
		format  string
		secret  string
		enabled bool
		wantErr string
	}{
		{
			name:    "omitted format is Standard",
			secret:  validStandard,
			enabled: true,
		},
		{
			name:    "explicit Standard format",
			format:  EventWebhookFormatStandard,
			secret:  validStandard,
			enabled: true,
		},
		{
			name:    "GitHub accepts opaque high entropy text",
			format:  EventWebhookFormatGitHub,
			secret:  validGitHub,
			enabled: true,
		},
		{
			name:    "GitHub rejects short secret",
			format:  EventWebhookFormatGitHub,
			secret:  strings.Repeat("g", githubWebhookMinSecretBytes-1),
			enabled: true,
			wantErr: "GitHub secret must be between 32 and 256 bytes",
		},
		{
			name:    "GitHub rejects oversized secret",
			format:  EventWebhookFormatGitHub,
			secret:  strings.Repeat("g", githubWebhookMaxSecretBytes+1),
			enabled: true,
			wantErr: "GitHub secret must be between 32 and 256 bytes",
		},
		{
			name:    "GitHub rejects invalid UTF-8",
			format:  EventWebhookFormatGitHub,
			secret:  strings.Repeat("g", githubWebhookMinSecretBytes) + string([]byte{0xff}),
			enabled: true,
			wantErr: "GitHub secret must be valid UTF-8",
		},
		{
			name:    "GitHub rejects surrounding whitespace",
			format:  EventWebhookFormatGitHub,
			secret:  " " + validGitHub,
			enabled: true,
			wantErr: "GitHub secret must not have leading or trailing whitespace",
		},
		{
			name:    "unknown active format is rejected",
			format:  "gitlab",
			secret:  validGitHub,
			enabled: true,
			wantErr: `webhook connector "build" has unsupported format "gitlab"`,
		},
		{
			name:    "disabled connector body remains inert",
			format:  "gitlab",
			secret:  "short",
			enabled: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := EventIngressConfig{
				Enabled: true,
				Webhooks: map[string]GenericWebhookConfig{
					"build": {
						Enabled: test.enabled,
						Format:  test.format,
						Secret:  *NewSecureString(test.secret),
					},
				},
			}
			err := cfg.Validate()
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

func TestDisabledIngressLeavesWebhookFormatInert(t *testing.T) {
	cfg := EventIngressConfig{
		Webhooks: map[string]GenericWebhookConfig{
			"not/a/connector": {
				Enabled: true,
				Format:  "unknown",
				Secret:  *NewSecureString("short"),
			},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with disabled master error = %v", err)
	}
}

func TestGitHubWebhookIdentitySecretConflictIsRejectedWhileDisabled(t *testing.T) {
	secret := strings.Repeat("G", githubWebhookMinSecretBytes)
	cfg := EventIngressConfig{
		Webhooks: map[string]GenericWebhookConfig{
			"hook" + secret: {
				Format: EventWebhookFormatGitHub,
				Secret: *NewSecureString(secret),
			},
		},
	}

	err := cfg.Validate()
	if err == nil || err.Error() != genericWebhookConnectorSecretConflictMessage {
		t.Fatalf(
			"Validate() error = %v, want %q",
			err,
			genericWebhookConnectorSecretConflictMessage,
		)
	}
}

func TestGitHubWebhookFormatSurvivesSecretCopy(t *testing.T) {
	secret := strings.Repeat("G", githubWebhookMinSecretBytes)
	candidate := EventIngressConfig{
		Webhooks: map[string]GenericWebhookConfig{
			"build": {
				Enabled: true,
				Format:  EventWebhookFormatGitHub,
			},
		},
	}
	existing := EventIngressConfig{
		Webhooks: map[string]GenericWebhookConfig{
			"build": {
				Format: EventWebhookFormatStandard,
				Secret: *NewSecureString(secret),
			},
		},
	}

	candidate.CopyWebhookSecretsFrom(existing)
	got := candidate.Webhooks["build"]
	if got.Format != EventWebhookFormatGitHub {
		t.Fatalf("copied candidate format = %q, want GitHub", got.Format)
	}
	if got.Secret.String() != secret {
		t.Fatal("persisted secret was not copied")
	}
}

func TestApplyWebhookSecretsPreservesGitHubFormat(t *testing.T) {
	secret := strings.Repeat("G", githubWebhookMinSecretBytes)
	cfg := EventIngressConfig{
		Enabled: true,
		Webhooks: map[string]GenericWebhookConfig{
			"build": {
				Enabled: true,
				Format:  EventWebhookFormatGitHub,
			},
		},
	}

	if err := cfg.ApplyWebhookSecrets(map[string]string{"build": secret}); err != nil {
		t.Fatalf("ApplyWebhookSecrets() error = %v", err)
	}
	got := cfg.Webhooks["build"]
	if got.Format != EventWebhookFormatGitHub {
		t.Fatalf("updated candidate format = %q, want GitHub", got.Format)
	}
	if got.Secret.String() != secret {
		t.Fatal("updated GitHub secret was not resolved")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() after secret update error = %v", err)
	}
}

func TestSaveLoadGitHubWebhookSeparatesFormatAndSecret(t *testing.T) {
	mustSetupSSHKey(t)
	path := filepath.Join(t.TempDir(), "config.json")
	secret := strings.Repeat("G", githubWebhookMinSecretBytes)
	cfg := DefaultConfig()
	cfg.Events.Ingress = EventIngressConfig{
		Enabled: true,
		Webhooks: map[string]GenericWebhookConfig{
			"github": {
				Enabled: true,
				Format:  EventWebhookFormatGitHub,
				Secret:  *NewSecureString(secret),
			},
		},
	}

	if err := SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	configData, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	if strings.Contains(string(configData), secret) {
		t.Fatal("config.json contains GitHub webhook secret")
	}
	var savedJSON struct {
		Events struct {
			Ingress struct {
				Webhooks map[string]struct {
					Format string `json:"format"`
					Secret string `json:"secret"`
				} `json:"webhooks"`
			} `json:"ingress"`
		} `json:"events"`
	}
	if err = json.Unmarshal(configData, &savedJSON); err != nil {
		t.Fatalf("decode config.json: %v", err)
	}
	savedWebhook := savedJSON.Events.Ingress.Webhooks["github"]
	if savedWebhook.Format != EventWebhookFormatGitHub || savedWebhook.Secret != "[NOT_HERE]" {
		t.Fatalf("saved GitHub webhook = %#v", savedWebhook)
	}

	securityData, err := os.ReadFile(securityPath(path))
	if err != nil {
		t.Fatalf("read .security.yml: %v", err)
	}
	var savedSecurity struct {
		Events struct {
			Ingress struct {
				Webhooks map[string]map[string]any `yaml:"webhooks"`
			} `yaml:"ingress"`
		} `yaml:"events"`
	}
	if err = yaml.Unmarshal(securityData, &savedSecurity); err != nil {
		t.Fatalf("decode .security.yml: %v", err)
	}
	securityWebhook := savedSecurity.Events.Ingress.Webhooks["github"]
	if len(securityWebhook) != 1 || securityWebhook["secret"] != secret {
		t.Fatalf("saved security webhook = %#v, want only secret", securityWebhook)
	}

	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	loadedWebhook := loaded.Events.Ingress.Webhooks["github"]
	if loadedWebhook.Format != EventWebhookFormatGitHub ||
		loadedWebhook.Secret.String() != secret {
		t.Fatalf("loaded GitHub webhook = %#v", loadedWebhook)
	}
}

func TestEventIngressConfigRejectsSecretBearingWebhookFormat(t *testing.T) {
	t.Parallel()
	secret := strings.Repeat("G", githubWebhookMinSecretBytes)
	tests := []struct {
		name    string
		ingress EventIngressConfig
	}{
		{
			name: "active connector own secret",
			ingress: EventIngressConfig{
				Enabled: true,
				Webhooks: map[string]GenericWebhookConfig{
					"github": {
						Enabled: true,
						Format:  secret,
						Secret:  *NewSecureString(secret),
					},
				},
			},
		},
		{
			name: "disabled master own secret",
			ingress: EventIngressConfig{
				Webhooks: map[string]GenericWebhookConfig{
					"github": {
						Format: secret,
						Secret: *NewSecureString(secret),
					},
				},
			},
		},
		{
			name: "disabled connector other secret",
			ingress: EventIngressConfig{
				Enabled: true,
				Webhooks: map[string]GenericWebhookConfig{
					"owner": {
						Secret: *NewSecureString(secret),
					},
					"public": {
						Format: "prefix-" + secret + "-suffix",
					},
				},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := test.ingress.Validate()
			if err == nil ||
				err.Error() != genericWebhookConnectorSecretConflictMessage {
				t.Fatalf(
					"Validate() error = %v, want opaque public identity conflict",
					err,
				)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatal("validation error exposed a webhook signing secret")
			}
		})
	}
}

func TestInactiveUnknownFormatCannotBypassSecretBearingMapKey(t *testing.T) {
	t.Parallel()
	secret := strings.Repeat("G", githubWebhookMinSecretBytes)
	err := (EventIngressConfig{
		Webhooks: map[string]GenericWebhookConfig{
			"prefix-" + secret: {
				Format: "unknown",
				Secret: *NewSecureString(secret),
			},
		},
	}).Validate()
	if err == nil || err.Error() != genericWebhookConnectorSecretConflictMessage {
		t.Fatalf("Validate() error = %v, want opaque public identity conflict", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("validation error exposed a webhook signing secret")
	}
}

func TestSaveConfigRejectsSecretBearingWebhookFormatBeforePersistence(t *testing.T) {
	t.Parallel()
	secret := strings.Repeat("G", githubWebhookMinSecretBytes)
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := DefaultConfig()
	cfg.Events.Ingress = EventIngressConfig{
		Webhooks: map[string]GenericWebhookConfig{
			"github": {
				Format: "prefix-" + secret,
				Secret: *NewSecureString(secret),
			},
		},
	}

	err := SaveConfig(path, cfg)
	if err == nil || err.Error() != genericWebhookConnectorSecretConflictMessage {
		t.Fatalf("SaveConfig() error = %v, want opaque public identity conflict", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("save error exposed a webhook signing secret")
	}
	for _, candidate := range []string{path, securityPath(path)} {
		if _, statErr := os.Stat(candidate); !os.IsNotExist(statErr) {
			t.Fatalf("%s was persisted before public identity validation: %v", candidate, statErr)
		}
	}
}
