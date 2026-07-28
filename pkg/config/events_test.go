package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
		"version": 3,
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
	data := []byte(`{"version": 3}`)
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
			name:         "home workspace",
			workspace:    filepath.Join("~", "workspace"),
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
		"version": 3,
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
		"version": 3,
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
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal default config: %v", err)
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
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig(configured) error: %v", err)
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
