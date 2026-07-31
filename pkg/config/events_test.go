package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/sipeed/picoclaw/pkg/credential"
)

func TestDefaultEventLoggingConfig(t *testing.T) {
	cfg := DefaultConfig()
	logCfg := EffectiveEventLoggingConfig(cfg)

	if !logCfg.Enabled {
		t.Fatal("default event logging should be enabled")
	}
	if !reflect.DeepEqual(logCfg.Include, []string{"agent.*"}) {
		t.Fatalf("default include = %#v, want agent.*", logCfg.Include)
	}
	if logCfg.MinSeverity != "info" {
		t.Fatalf("default min severity = %q, want info", logCfg.MinSeverity)
	}
	if logCfg.IncludePayload {
		t.Fatal("default event logging should not include raw payloads")
	}
}

func TestLoadConfigEventLoggingOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := []byte(`{
		"version": 5,
		"events": {
			"logging": {
				"enabled": false,
				"include": ["gateway.*"],
				"exclude": ["gateway.ready"],
				"min_severity": "warn",
				"include_payload": true
			}
		}
	}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	logCfg := EffectiveEventLoggingConfig(cfg)

	if logCfg.Enabled {
		t.Fatal("loaded event logging enabled = true, want false")
	}
	if !reflect.DeepEqual(logCfg.Include, []string{"gateway.*"}) {
		t.Fatalf("loaded include = %#v, want gateway.*", logCfg.Include)
	}
	if !reflect.DeepEqual(logCfg.Exclude, []string{"gateway.ready"}) {
		t.Fatalf("loaded exclude = %#v, want gateway.ready", logCfg.Exclude)
	}
	if logCfg.MinSeverity != "warn" {
		t.Fatalf("loaded min severity = %q, want warn", logCfg.MinSeverity)
	}
	if !logCfg.IncludePayload {
		t.Fatal("loaded include_payload = false, want true")
	}
}

func TestLoadConfigEventLoggingEnvOverrides(t *testing.T) {
	t.Setenv("PICOCLAW_EVENTS_LOGGING_ENABLED", "false")
	t.Setenv("PICOCLAW_EVENTS_LOGGING_INCLUDE", "gateway.*,channel.lifecycle.*")
	t.Setenv("PICOCLAW_EVENTS_LOGGING_EXCLUDE", "gateway.ready")
	t.Setenv("PICOCLAW_EVENTS_LOGGING_MIN_SEVERITY", "error")
	t.Setenv("PICOCLAW_EVENTS_LOGGING_INCLUDE_PAYLOAD", "true")

	path := filepath.Join(t.TempDir(), "config.json")
	data := []byte(`{"version": 5}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	logCfg := EffectiveEventLoggingConfig(cfg)

	if logCfg.Enabled {
		t.Fatal("env enabled override = true, want false")
	}
	if !reflect.DeepEqual(logCfg.Include, []string{"gateway.*", "channel.lifecycle.*"}) {
		t.Fatalf("env include = %#v, want gateway/channel lifecycle", logCfg.Include)
	}
	if !reflect.DeepEqual(logCfg.Exclude, []string{"gateway.ready"}) {
		t.Fatalf("env exclude = %#v, want gateway.ready", logCfg.Exclude)
	}
	if logCfg.MinSeverity != "error" {
		t.Fatalf("env min severity = %q, want error", logCfg.MinSeverity)
	}
	if !logCfg.IncludePayload {
		t.Fatal("env include_payload = false, want true")
	}
}

func TestDefaultEventIngressConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Events.Ingress.Enabled {
		t.Fatal("durable event ingress must be opt-in")
	}

	workspace := filepath.Join(t.TempDir(), "workspace")
	got := EffectiveEventIngressConfig(cfg, workspace)

	if got.Enabled {
		t.Fatal("effective ingress enabled = true, want false")
	}
	if got.DatabasePath != filepath.Join(workspace, "eventing", "events.db") {
		t.Fatalf("database path = %q, want workspace eventing database", got.DatabasePath)
	}
	if got.RetentionDays != DefaultEventIngressRetentionDays {
		t.Fatalf("retention days = %d, want %d", got.RetentionDays, DefaultEventIngressRetentionDays)
	}
	if got.MaxPayloadBytes != DefaultEventIngressMaxPayloadBytes {
		t.Fatalf(
			"max payload bytes = %d, want %d",
			got.MaxPayloadBytes,
			DefaultEventIngressMaxPayloadBytes,
		)
	}
	defaultFields := DefaultEventIngressRedactFields()
	if !reflect.DeepEqual(got.RedactFields, defaultFields) {
		t.Fatalf("redact fields = %#v, want mandatory defaults", got.RedactFields)
	}

	got.RedactFields[0] = "changed"
	if DefaultEventIngressRedactFields()[0] == "changed" {
		t.Fatal("effective ingress must not expose mutable default redaction fields")
	}
}

func TestEffectiveEventIngressConfigNilAndBlankWorkspace(t *testing.T) {
	got := EffectiveEventIngressConfig(nil, " ")

	if got.Enabled {
		t.Fatal("nil config should leave ingress disabled")
	}
	if got.DatabasePath != filepath.Join("eventing", "events.db") {
		t.Fatalf("database path = %q, want relative eventing/events.db", got.DatabasePath)
	}
	if got.RetentionDays != DefaultEventIngressRetentionDays {
		t.Fatalf("retention days = %d, want default", got.RetentionDays)
	}
	if got.MaxPayloadBytes != DefaultEventIngressMaxPayloadBytes {
		t.Fatalf("max payload bytes = %d, want default", got.MaxPayloadBytes)
	}
}

func TestEffectiveEventIngressConfigPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace := filepath.Join(t.TempDir(), "workspace")
	absolutePath := filepath.Join(t.TempDir(), "absolute", "..", "events.db")

	tests := []struct {
		name         string
		workspace    string
		databasePath string
		wantPath     string
	}{
		{
			name:         "default",
			workspace:    workspace,
			databasePath: "",
			wantPath:     filepath.Join(workspace, "eventing", "events.db"),
		},
		{
			name:         "relative override",
			workspace:    workspace,
			databasePath: filepath.Join("state", "events.db"),
			wantPath:     filepath.Join(workspace, "state", "events.db"),
		},
		{
			name:         "absolute override",
			workspace:    workspace,
			databasePath: absolutePath,
			wantPath:     filepath.Clean(absolutePath),
		},
		{
			name:         "home override",
			workspace:    workspace,
			databasePath: filepath.Join("~", "eventing.db"),
			wantPath:     filepath.Join(home, "eventing.db"),
		},
		{
			name:         "Windows-style home override",
			workspace:    workspace,
			databasePath: `~\eventing.db`,
			wantPath:     filepath.Join(home, "eventing.db"),
		},
		{
			name:         "tilde-prefixed relative override",
			workspace:    workspace,
			databasePath: "~backup/events.db",
			wantPath:     filepath.Join(workspace, "~backup", "events.db"),
		},
		{
			name:         "home workspace",
			workspace:    filepath.Join("~", "workspace"),
			databasePath: "",
			wantPath:     filepath.Join(home, "workspace", "eventing", "events.db"),
		},
		{
			name:         "Windows-style home workspace",
			workspace:    `~\workspace`,
			databasePath: "",
			wantPath:     filepath.Join(home, "workspace", "eventing", "events.db"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Events: EventsConfig{
					Ingress: EventIngressConfig{DatabasePath: tt.databasePath},
				},
			}
			got := EffectiveEventIngressConfig(cfg, tt.workspace)
			if got.DatabasePath != tt.wantPath {
				t.Fatalf("database path = %q, want %q", got.DatabasePath, tt.wantPath)
			}
		})
	}
}

func TestLoadConfigEventIngressJSONOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := []byte(`{
		"version": 5,
		"events": {
			"ingress": {
				"enabled": true,
				"database_path": "state/events.sqlite",
				"retention_days": 45,
				"max_payload_bytes": 2097152,
				"redact_fields": ["tenant_secret", "TOKEN", " tenant_secret "]
			}
		}
	}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	workspace := filepath.Join(t.TempDir(), "workspace")
	got := EffectiveEventIngressConfig(cfg, workspace)

	if !got.Enabled {
		t.Fatal("loaded event ingress enabled = false, want true")
	}
	if got.DatabasePath != filepath.Join(workspace, "state", "events.sqlite") {
		t.Fatalf("database path = %q, want workspace-relative override", got.DatabasePath)
	}
	if got.RetentionDays != 45 {
		t.Fatalf("retention days = %d, want 45", got.RetentionDays)
	}
	if got.MaxPayloadBytes != 2*DefaultEventIngressMaxPayloadBytes {
		t.Fatalf("max payload bytes = %d, want 2 MiB", got.MaxPayloadBytes)
	}

	wantFields := append(DefaultEventIngressRedactFields(), "tenant_secret")
	if !reflect.DeepEqual(got.RedactFields, wantFields) {
		t.Fatalf("redact fields = %#v, want %#v", got.RedactFields, wantFields)
	}
}

func TestLoadConfigEventIngressEnvOverrides(t *testing.T) {
	t.Setenv("PICOCLAW_EVENTS_INGRESS_ENABLED", "true")
	t.Setenv("PICOCLAW_EVENTS_INGRESS_DATABASE_PATH", "env/events.db")
	t.Setenv("PICOCLAW_EVENTS_INGRESS_RETENTION_DAYS", "60")
	t.Setenv("PICOCLAW_EVENTS_INGRESS_MAX_PAYLOAD_BYTES", "3145728")
	t.Setenv("PICOCLAW_EVENTS_INGRESS_REDACT_FIELDS", "tenantSecret,TOKEN")

	path := filepath.Join(t.TempDir(), "config.json")
	data := []byte(`{
		"version": 5,
		"events": {
			"ingress": {
				"enabled": false,
				"database_path": "json/events.db",
				"retention_days": 5,
				"max_payload_bytes": 1024,
				"redact_fields": ["json_secret"]
			}
		}
	}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	workspace := filepath.Join(t.TempDir(), "workspace")
	got := EffectiveEventIngressConfig(cfg, workspace)

	if !got.Enabled {
		t.Fatal("env ingress enabled override = false, want true")
	}
	if got.DatabasePath != filepath.Join(workspace, "env", "events.db") {
		t.Fatalf("database path = %q, want env workspace-relative path", got.DatabasePath)
	}
	if got.RetentionDays != 60 {
		t.Fatalf("retention days = %d, want 60", got.RetentionDays)
	}
	if got.MaxPayloadBytes != 3*DefaultEventIngressMaxPayloadBytes {
		t.Fatalf("max payload bytes = %d, want 3 MiB", got.MaxPayloadBytes)
	}

	wantFields := append(DefaultEventIngressRedactFields(), "tenantSecret")
	if !reflect.DeepEqual(got.RedactFields, wantFields) {
		t.Fatalf("redact fields = %#v, want %#v", got.RedactFields, wantFields)
	}
}

func TestSaveLoadConfigEventIngress(t *testing.T) {
	mustSetupSSHKey(t)
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := DefaultConfig()
	// A decoded empty array is still semantically zero and should not force the
	// new section into an otherwise legacy-compatible v3 save.
	cfg.Events.Ingress.RedactFields = []string{}

	if err := SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig(default) error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read default config: %v", err)
	}
	var raw struct {
		Events map[string]json.RawMessage `json:"events"`
	}
	if unmarshalErr := json.Unmarshal(data, &raw); unmarshalErr != nil {
		t.Fatalf("unmarshal default config: %v", unmarshalErr)
	}
	if _, exists := raw.Events["ingress"]; exists {
		t.Fatalf("zero ingress should be omitted from saved v3 config:\n%s", data)
	}

	cfg.Events.Ingress = EventIngressConfig{
		Enabled:         true,
		DatabasePath:    filepath.Join("state", "events.db"),
		RetentionDays:   14,
		MaxPayloadBytes: 4096,
		RedactFields:    []string{"tenant_secret"},
	}
	if saveErr := SaveConfig(path, cfg); saveErr != nil {
		t.Fatalf("SaveConfig(configured) error: %v", saveErr)
	}

	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig(configured) error: %v", err)
	}
	if !reflect.DeepEqual(loaded.Events.Ingress, cfg.Events.Ingress) {
		t.Fatalf(
			"ingress after save/load = %#v, want %#v",
			loaded.Events.Ingress,
			cfg.Events.Ingress,
		)
	}

	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read configured config: %v", err)
	}
	if !strings.Contains(string(data), `"ingress"`) {
		t.Fatalf("configured ingress missing from saved config:\n%s", data)
	}
}

func TestEffectiveEventIngressConfigDeepCopiesWebhooks(t *testing.T) {
	secret := eventWebhookTestSecret("deep-copy")
	cfg := &Config{
		Events: EventsConfig{
			Ingress: EventIngressConfig{
				Webhooks: map[string]GenericWebhookConfig{
					"build": {
						Enabled: true,
						Secret:  *NewSecureString(secret),
					},
				},
			},
		},
	}

	got := EffectiveEventIngressConfig(cfg, t.TempDir())
	webhook := got.Webhooks["build"]
	webhook.Enabled = false
	webhook.Secret.Set(eventWebhookTestSecret("changed"))
	got.Webhooks["build"] = webhook
	delete(got.Webhooks, "build")

	original, exists := cfg.Events.Ingress.Webhooks["build"]
	if !exists {
		t.Fatal("mutating effective webhooks deleted the source connector")
	}
	if !original.Enabled {
		t.Fatal("mutating effective webhooks changed the source connector")
	}
	if original.Secret.String() != secret {
		t.Fatal("mutating effective webhook secret changed the source secret")
	}
}

func TestEventIngressConfigValidateWebhooks(t *testing.T) {
	validSecret := eventWebhookTestSecret("valid")
	shortSecret := genericWebhookSecretPrefix +
		base64.StdEncoding.EncodeToString([]byte("too short"))
	nonCanonicalSecret := validSecret[:len(validSecret)-1] + "\n" +
		validSecret[len(validSecret)-1:]
	longName := "a" + strings.Repeat("b", genericWebhookMaxConnectorBytes)

	tests := []struct {
		name    string
		cfg     EventIngressConfig
		wantErr bool
	}{
		{
			name: "master disabled is inert",
			cfg: EventIngressConfig{
				Webhooks: map[string]GenericWebhookConfig{
					"not/a/name": {
						Enabled: true,
						Secret:  *NewSecureString("not-a-secret"),
					},
				},
			},
		},
		{
			name: "no generic connectors",
			cfg:  EventIngressConfig{Enabled: true},
		},
		{
			name: "valid connector",
			cfg: EventIngressConfig{
				Enabled: true,
				Webhooks: map[string]GenericWebhookConfig{
					"Build_hook-2": {
						Enabled: true,
						Secret:  *NewSecureString(validSecret),
					},
				},
			},
		},
		{
			name: "disabled connector does not require secret",
			cfg: EventIngressConfig{
				Enabled: true,
				Webhooks: map[string]GenericWebhookConfig{
					"disabled": {Enabled: false},
				},
			},
		},
		{
			name: "empty connector name",
			cfg: EventIngressConfig{
				Enabled: true,
				Webhooks: map[string]GenericWebhookConfig{
					"": {Enabled: false},
				},
			},
			wantErr: true,
		},
		{
			name: "unsafe connector name",
			cfg: EventIngressConfig{
				Enabled: true,
				Webhooks: map[string]GenericWebhookConfig{
					"build/hook": {Enabled: false},
				},
			},
			wantErr: true,
		},
		{
			name: "connector name too long",
			cfg: EventIngressConfig{
				Enabled: true,
				Webhooks: map[string]GenericWebhookConfig{
					longName: {Enabled: false},
				},
			},
			wantErr: true,
		},
		{
			name: "case collision",
			cfg: EventIngressConfig{
				Enabled: true,
				Webhooks: map[string]GenericWebhookConfig{
					"Build": {Enabled: false},
					"build": {Enabled: false},
				},
			},
			wantErr: true,
		},
		{
			name: "enabled connector missing secret",
			cfg: EventIngressConfig{
				Enabled: true,
				Webhooks: map[string]GenericWebhookConfig{
					"build": {Enabled: true},
				},
			},
			wantErr: true,
		},
		{
			name: "wrong secret prefix",
			cfg: EventIngressConfig{
				Enabled: true,
				Webhooks: map[string]GenericWebhookConfig{
					"build": {
						Enabled: true,
						Secret:  *NewSecureString(strings.TrimPrefix(validSecret, genericWebhookSecretPrefix)),
					},
				},
			},
			wantErr: true,
		},
		{
			name: "weak secret",
			cfg: EventIngressConfig{
				Enabled: true,
				Webhooks: map[string]GenericWebhookConfig{
					"build": {
						Enabled: true,
						Secret:  *NewSecureString(shortSecret),
					},
				},
			},
			wantErr: true,
		},
		{
			name: "noncanonical base64",
			cfg: EventIngressConfig{
				Enabled: true,
				Webhooks: map[string]GenericWebhookConfig{
					"build": {
						Enabled: true,
						Secret:  *NewSecureString(nonCanonicalSecret),
					},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr && err == nil {
				t.Fatal("Validate() error = nil, want error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Validate() error = %v, want nil", err)
			}
		})
	}
}

func TestSaveLoadConfigGenericWebhookSeparatesSecrets(t *testing.T) {
	mustSetupSSHKey(t)
	t.Setenv(credential.PassphraseEnvVar, "")

	path := filepath.Join(t.TempDir(), "config.json")
	secret := eventWebhookTestSecret("round-trip")
	cfg := DefaultConfig()
	cfg.Events.Ingress = EventIngressConfig{
		Enabled:         true,
		DatabasePath:    "state/events.db",
		RetentionDays:   17,
		MaxPayloadBytes: 8192,
		RedactFields:    []string{"tenant_key"},
		Webhooks: map[string]GenericWebhookConfig{
			"deploy": {
				Enabled: true,
				Secret:  *NewSecureString(secret),
			},
		},
	}

	if err := SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig() error: %v", err)
	}

	configData, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	if strings.Contains(string(configData), secret) {
		t.Fatal("config.json contains the webhook secret")
	}
	if !strings.Contains(string(configData), `"[NOT_HERE]"`) {
		t.Fatalf("config.json does not contain the masked webhook secret:\n%s", configData)
	}

	var savedJSON struct {
		Events struct {
			Ingress struct {
				Webhooks map[string]struct {
					Enabled bool   `json:"enabled"`
					Secret  string `json:"secret"`
				} `json:"webhooks"`
			} `json:"ingress"`
		} `json:"events"`
	}
	if err = json.Unmarshal(configData, &savedJSON); err != nil {
		t.Fatalf("decode config.json: %v", err)
	}
	deployJSON := savedJSON.Events.Ingress.Webhooks["deploy"]
	if !deployJSON.Enabled || deployJSON.Secret != "[NOT_HERE]" {
		t.Fatalf("saved webhook JSON = %#v, want enabled with masked secret", deployJSON)
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
	deploySecurity := savedSecurity.Events.Ingress.Webhooks["deploy"]
	if len(deploySecurity) != 1 || deploySecurity["secret"] != secret {
		t.Fatalf("saved webhook security data = %#v, want only secret", deploySecurity)
	}

	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	deploy := loaded.Events.Ingress.Webhooks["deploy"]
	if !deploy.Enabled || deploy.Secret.String() != secret {
		t.Fatalf("loaded webhook = %#v, want enabled with resolved secret", deploy)
	}
	if loaded.Events.Ingress.DatabasePath != "state/events.db" ||
		loaded.Events.Ingress.RetentionDays != 17 ||
		loaded.Events.Ingress.MaxPayloadBytes != 8192 ||
		!reflect.DeepEqual(loaded.Events.Ingress.RedactFields, []string{"tenant_key"}) {
		t.Fatalf("non-secret ingress config changed after round trip: %#v", loaded.Events.Ingress)
	}
}

func TestLoadGenericWebhookSecurityOverlayIsNarrow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	secret := eventWebhookTestSecret("kept")
	configData := []byte(`{
		"version": 5,
		"events": {
			"ingress": {
				"enabled": true,
				"database_path": "wanted/events.db",
				"retention_days": 11,
				"webhooks": {
					"kept": {
						"enabled": true,
						"secret": "[NOT_HERE]"
					}
				}
			}
		}
	}`)
	if err := os.WriteFile(path, configData, 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
	securityData := []byte(`events:
  logging:
    enabled: false
  ingress:
    enabled: false
    database_path: replaced/events.db
    retention_days: 1
    webhooks:
      kept:
        enabled: false
        secret: ` + secret + `
      ghost:
        enabled: true
        secret: file://deleted-connector-secret.txt
`)
	if err := os.WriteFile(securityPath(path), securityData, 0o600); err != nil {
		t.Fatalf("write .security.yml: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	if !cfg.Events.Ingress.Enabled {
		t.Fatal("security overlay changed the master enabled flag")
	}
	if cfg.Events.Ingress.DatabasePath != "wanted/events.db" ||
		cfg.Events.Ingress.RetentionDays != 11 {
		t.Fatalf("security overlay changed non-secret ingress fields: %#v", cfg.Events.Ingress)
	}
	if _, exists := cfg.Events.Ingress.Webhooks["ghost"]; exists {
		t.Fatal("security-only connector was added to runtime configuration")
	}
	kept := cfg.Events.Ingress.Webhooks["kept"]
	if !kept.Enabled {
		t.Fatal("security overlay changed connector enabled flag")
	}
	if kept.Secret.String() != secret {
		t.Fatalf("resolved secret = %q, want configured kept secret", kept.Secret.String())
	}
}

func TestGenericWebhookSecurityReferencesRoundTrip(t *testing.T) {
	mustSetupSSHKey(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	const passphrase = "webhook-test-passphrase"
	originalPassphraseProvider := credential.PassphraseProvider
	credential.PassphraseProvider = func() string { return passphrase }
	t.Cleanup(func() { credential.PassphraseProvider = originalPassphraseProvider })

	encryptedSecretValue := eventWebhookTestSecret("encrypted")
	encryptedSecret, err := credential.Encrypt(passphrase, "", encryptedSecretValue)
	if err != nil {
		t.Fatalf("encrypt webhook secret: %v", err)
	}
	fileSecret := eventWebhookTestSecret("file")
	if err = os.WriteFile(filepath.Join(dir, "webhook-secret.txt"), []byte(fileSecret+"\n"), 0o600); err != nil {
		t.Fatalf("write webhook secret file: %v", err)
	}

	configData := []byte(`{
		"version": 5,
		"events": {
			"ingress": {
				"enabled": true,
				"webhooks": {
					"encrypted": {"enabled": true, "secret": "[NOT_HERE]"},
					"from-file": {"enabled": true, "secret": "[NOT_HERE]"}
				}
			}
		}
	}`)
	if err = os.WriteFile(path, configData, 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
	securityData := []byte(`events:
  ingress:
    webhooks:
      encrypted:
        secret: ` + encryptedSecret + `
      from-file:
        secret: file://webhook-secret.txt
`)
	if err = os.WriteFile(securityPath(path), securityData, 0o600); err != nil {
		t.Fatalf("write .security.yml: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error: %v", err)
	}
	encryptedWebhook := cfg.Events.Ingress.Webhooks["encrypted"]
	if got := encryptedWebhook.Secret.String(); got != encryptedSecretValue {
		t.Fatalf("resolved enc:// secret = %q, want original secret", got)
	}
	fileWebhook := cfg.Events.Ingress.Webhooks["from-file"]
	if got := fileWebhook.Secret.String(); got != fileSecret {
		t.Fatalf("resolved file:// secret = %q, want file content", got)
	}

	if err = SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig() error: %v", err)
	}
	savedSecurity, err := os.ReadFile(securityPath(path))
	if err != nil {
		t.Fatalf("read saved .security.yml: %v", err)
	}
	if !strings.Contains(string(savedSecurity), encryptedSecret) {
		t.Fatal("SaveConfig did not preserve the enc:// webhook secret reference")
	}
	if !strings.Contains(string(savedSecurity), "file://webhook-secret.txt") {
		t.Fatal("SaveConfig did not preserve the file:// webhook secret reference")
	}
	if strings.Contains(string(savedSecurity), encryptedSecretValue) ||
		strings.Contains(string(savedSecurity), fileSecret) {
		t.Fatal("saved .security.yml expanded a secure webhook reference")
	}
}

func TestDisabledGenericWebhookPreservesUnresolvedSecurityReference(t *testing.T) {
	mustSetupSSHKey(t)
	t.Setenv(credential.PassphraseEnvVar, "")

	tests := []struct {
		name             string
		ingressEnabled   bool
		connectorEnabled bool
	}{
		{
			name:             "master disabled",
			ingressEnabled:   false,
			connectorEnabled: true,
		},
		{
			name:             "connector disabled",
			ingressEnabled:   true,
			connectorEnabled: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			configData := fmt.Sprintf(`{
				"version": 5,
				"events": {
					"ingress": {
						"enabled": %t,
						"webhooks": {
							"deploy": {
								"enabled": %t,
								"secret": "[NOT_HERE]"
							}
						}
					}
				}
			}`, test.ingressEnabled, test.connectorEnabled)
			if err := os.WriteFile(path, []byte(configData), 0o600); err != nil {
				t.Fatalf("write config.json: %v", err)
			}
			const missingReference = "file://missing-webhook-secret.txt"
			securityData := []byte(`events:
  ingress:
    webhooks:
      deploy:
        secret: ` + missingReference + `
`)
			if err := os.WriteFile(
				securityPath(path),
				securityData,
				0o600,
			); err != nil {
				t.Fatalf("write .security.yml: %v", err)
			}

			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatalf("LoadConfig(disabled reference) error = %v", err)
			}
			webhook := cfg.Events.Ingress.Webhooks["deploy"]
			if got := webhook.Secret.String(); got != missingReference {
				t.Fatalf("preserved unresolved secret = %q, want %q", got, missingReference)
			}
			if saveErr := SaveConfig(path, cfg); saveErr != nil {
				t.Fatalf("SaveConfig(disabled reference) error = %v", saveErr)
			}
			savedSecurity, err := os.ReadFile(securityPath(path))
			if err != nil {
				t.Fatalf("read saved .security.yml: %v", err)
			}
			if !strings.Contains(string(savedSecurity), missingReference) {
				t.Fatalf(
					"saved disabled security config lost reference:\n%s",
					savedSecurity,
				)
			}
		})
	}
}

func TestResolveWebhookSecretsErrorDoesNotExposeConnectorName(t *testing.T) {
	exposedSecret := genericWebhookSecretPrefix +
		base64.StdEncoding.EncodeToString(make([]byte, genericWebhookMinSecretBytes+1))
	updateResolver(t.TempDir())

	cfg := EventIngressConfig{
		Enabled: true,
		Webhooks: map[string]GenericWebhookConfig{
			"safe": {
				Enabled: true,
				Secret:  unresolvedEventWebhookSecret(exposedSecret),
			},
			exposedSecret: {
				Enabled: true,
				Secret: unresolvedEventWebhookSecret(
					"file://" + exposedSecret,
				),
			},
		},
	}

	err := cfg.resolveWebhookSecrets()
	if err == nil {
		t.Fatal("resolveWebhookSecrets() error = nil, want missing file error")
	}
	if strings.Contains(err.Error(), exposedSecret) {
		t.Fatalf("resolveWebhookSecrets() error exposed connector name: %v", err)
	}
	if err.Error() != "resolve event webhook signing secret" {
		t.Fatalf("resolveWebhookSecrets() error = %q, want static opaque error", err)
	}
}

func TestLoadWebhookSecurityShapeErrorDoesNotExposeConnectorName(t *testing.T) {
	exposedSecret := genericWebhookSecretPrefix +
		base64.StdEncoding.EncodeToString(make([]byte, genericWebhookMinSecretBytes+1))

	tests := []struct {
		name string
		body string
	}{
		{
			name: "connector entry is not a mapping",
			body: "        not-a-mapping",
		},
		{
			name: "secret is not a scalar",
			body: "        secret:\n          - invalid",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			configData := fmt.Sprintf(`{
				"version": 5,
				"events": {
					"ingress": {
						"enabled": true,
						"webhooks": {
							"owner": {
								"enabled": true,
								"secret": %q
							},
							%s: {
								"enabled": false,
								"secret": "[NOT_HERE]"
							}
						}
					}
				}
			}`, exposedSecret, fmt.Sprintf("%q", exposedSecret))
			if err := os.WriteFile(path, []byte(configData), 0o600); err != nil {
				t.Fatalf("write config.json: %v", err)
			}
			securityData := fmt.Sprintf(
				"events:\n  ingress:\n    webhooks:\n      %s:\n%s\n",
				exposedSecret,
				test.body,
			)
			if err := os.WriteFile(
				securityPath(path),
				[]byte(securityData),
				0o600,
			); err != nil {
				t.Fatalf("write .security.yml: %v", err)
			}

			_, err := LoadConfig(path)
			if err == nil {
				t.Fatal("LoadConfig() error = nil, want malformed security error")
			}
			if strings.Contains(err.Error(), exposedSecret) {
				t.Fatalf("LoadConfig() error exposed connector name: %v", err)
			}
			if !strings.Contains(err.Error(), "event webhook secret") {
				t.Fatalf("LoadConfig() error = %v, want opaque secret context", err)
			}
		})
	}
}

func TestLoadGenericWebhookSecurityUsesMasterEnvOverride(t *testing.T) {
	mustSetupSSHKey(t)
	t.Setenv(credential.PassphraseEnvVar, "")

	tests := []struct {
		name              string
		jsonEnabled       bool
		envEnabled        string
		securityOverlay   bool
		writeSecretFile   bool
		wantEnabled       bool
		wantResolvedValue bool
	}{
		{
			name:              "environment enables JSON-disabled security reference",
			jsonEnabled:       false,
			envEnabled:        "true",
			securityOverlay:   true,
			writeSecretFile:   true,
			wantEnabled:       true,
			wantResolvedValue: true,
		},
		{
			name:            "environment disables JSON-enabled security reference",
			jsonEnabled:     true,
			envEnabled:      "false",
			securityOverlay: true,
			wantEnabled:     false,
		},
		{
			name:              "environment enables JSON-disabled direct reference",
			jsonEnabled:       false,
			envEnabled:        "true",
			writeSecretFile:   true,
			wantEnabled:       true,
			wantResolvedValue: true,
		},
		{
			name:        "environment disables JSON-enabled direct reference",
			jsonEnabled: true,
			envEnabled:  "false",
			wantEnabled: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("PICOCLAW_EVENTS_INGRESS_ENABLED", test.envEnabled)
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			const reference = "file://webhook-secret.txt"
			jsonSecret := reference
			if test.securityOverlay {
				jsonSecret = "[NOT_HERE]"
			}
			configData := fmt.Sprintf(`{
				"version": 5,
				"events": {
					"ingress": {
						"enabled": %t,
						"webhooks": {
							"deploy": {
								"enabled": true,
								"secret": %q
							}
						}
					}
				}
			}`, test.jsonEnabled, jsonSecret)
			if err := os.WriteFile(path, []byte(configData), 0o600); err != nil {
				t.Fatalf("write config.json: %v", err)
			}

			resolvedSecret := eventWebhookTestSecret("env-override")
			if test.writeSecretFile {
				if err := os.WriteFile(
					filepath.Join(dir, "webhook-secret.txt"),
					[]byte(resolvedSecret+"\n"),
					0o600,
				); err != nil {
					t.Fatalf("write webhook secret: %v", err)
				}
			}
			if test.securityOverlay {
				securityData := []byte(`events:
  ingress:
    webhooks:
      deploy:
        secret: ` + reference + `
`)
				if err := os.WriteFile(securityPath(path), securityData, 0o600); err != nil {
					t.Fatalf("write .security.yml: %v", err)
				}
			}

			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatalf("LoadConfig() error = %v", err)
			}
			if cfg.Events.Ingress.Enabled != test.wantEnabled {
				t.Fatalf(
					"effective ingress enabled = %t, want %t",
					cfg.Events.Ingress.Enabled,
					test.wantEnabled,
				)
			}
			webhook := cfg.Events.Ingress.Webhooks["deploy"]
			wantSecret := reference
			if test.wantResolvedValue {
				wantSecret = resolvedSecret
			}
			if got := webhook.Secret.String(); got != wantSecret {
				t.Fatalf("loaded webhook secret = %q, want %q", got, wantSecret)
			}
		})
	}
}

func TestSecurityCopyFromUsesCandidateWebhookMasterEnablement(t *testing.T) {
	tests := []struct {
		name             string
		candidateEnabled bool
		envEnabled       string
		writeSecretFile  bool
		wantResolved     bool
	}{
		{
			name:             "enabled candidate resolves despite disabled process env",
			candidateEnabled: true,
			envEnabled:       "false",
			writeSecretFile:  true,
			wantResolved:     true,
		},
		{
			name:             "disabled candidate stays unresolved despite enabled process env",
			candidateEnabled: false,
			envEnabled:       "true",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("PICOCLAW_EVENTS_INGRESS_ENABLED", test.envEnabled)
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			updateResolver(dir)

			const reference = "file://webhook-secret.txt"
			resolvedSecret := eventWebhookTestSecret("security-copy")
			if test.writeSecretFile {
				if err := os.WriteFile(
					filepath.Join(dir, "webhook-secret.txt"),
					[]byte(resolvedSecret+"\n"),
					0o600,
				); err != nil {
					t.Fatalf("write webhook secret: %v", err)
				}
			}
			securityData := []byte(`events:
  ingress:
    webhooks:
      deploy:
        secret: ` + reference + `
`)
			if err := os.WriteFile(securityPath(path), securityData, 0o600); err != nil {
				t.Fatalf("write .security.yml: %v", err)
			}

			cfg := &Config{
				Events: EventsConfig{
					Ingress: EventIngressConfig{
						Enabled: test.candidateEnabled,
						Webhooks: map[string]GenericWebhookConfig{
							"deploy": {Enabled: true},
						},
					},
				},
			}
			if err := cfg.SecurityCopyFrom(path); err != nil {
				t.Fatalf("SecurityCopyFrom() error = %v", err)
			}

			wantSecret := reference
			if test.wantResolved {
				wantSecret = resolvedSecret
			}
			webhook := cfg.Events.Ingress.Webhooks["deploy"]
			if got := webhook.Secret.String(); got != wantSecret {
				t.Fatalf("copied webhook secret = %q, want %q", got, wantSecret)
			}
		})
	}
}

func TestLoadGenericWebhookConnectorEnablementIsJSONOnly(t *testing.T) {
	mustSetupSSHKey(t)
	t.Setenv(credential.PassphraseEnvVar, "")
	t.Setenv("PICOCLAW_EVENTS_INGRESS_ENABLED", "true")
	// Webhooks are a named map with no env mapping. This lookalike variable is
	// intentionally unsupported and must not activate a JSON-disabled connector.
	t.Setenv("PICOCLAW_EVENTS_INGRESS_WEBHOOKS_DEPLOY_ENABLED", "true")

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	configData := []byte(`{
		"version": 5,
		"events": {
			"ingress": {
				"enabled": true,
				"webhooks": {
					"deploy": {
						"enabled": false,
						"secret": "[NOT_HERE]"
					}
				}
			}
		}
	}`)
	if err := os.WriteFile(path, configData, 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
	const missingReference = "file://missing-webhook-secret.txt"
	securityData := []byte(`events:
  ingress:
    webhooks:
      deploy:
        secret: ` + missingReference + `
`)
	if err := os.WriteFile(securityPath(path), securityData, 0o600); err != nil {
		t.Fatalf("write .security.yml: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	webhook := cfg.Events.Ingress.Webhooks["deploy"]
	if webhook.Enabled {
		t.Fatal("unsupported connector env variable activated the connector")
	}
	if got := webhook.Secret.String(); got != missingReference {
		t.Fatalf("disabled connector secret = %q, want unresolved reference", got)
	}
}

func TestLoadConfigRejectsInvalidEnabledWebhook(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	configData := []byte(`{
		"version": 5,
		"events": {
			"ingress": {
				"enabled": true,
				"webhooks": {
					"deploy": {
						"enabled": true,
						"secret": "not-standard-webhooks"
					}
				}
			}
		}
	}`)
	if err := os.WriteFile(path, configData, 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	if _, err := LoadConfig(path); err == nil {
		t.Fatal("LoadConfig() error = nil, want invalid webhook secret error")
	}
}

func eventWebhookTestSecret(seed string) string {
	material := []byte(strings.Repeat(seed+"-", 8))
	if len(material) < genericWebhookMinSecretBytes {
		material = append(material, make([]byte, genericWebhookMinSecretBytes-len(material))...)
	}
	return genericWebhookSecretPrefix + base64.StdEncoding.EncodeToString(material)
}
