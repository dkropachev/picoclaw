package config

import (
	"path/filepath"
	"strings"
)

// EventsConfig groups process-local observability and durable ingress
// configuration.
type EventsConfig struct {
	Logging EventLoggingConfig `json:"logging,omitempty" envPrefix:"PICOCLAW_EVENTS_LOGGING_"`
	Ingress EventIngressConfig `json:"ingress,omitzero"  envPrefix:"PICOCLAW_EVENTS_INGRESS_"`
}

// EventIngressConfig controls durable external-event ingestion.
//
// Ingress is opt-in. RetentionDays, MaxPayloadBytes, DatabasePath, and
// RedactFields receive their stable defaults through EffectiveEventIngressConfig
// so the zero value remains disabled and can be omitted from saved v3 configs.
type EventIngressConfig struct {
	Enabled         bool     `json:"enabled"                     env:"ENABLED"`
	DatabasePath    string   `json:"database_path,omitempty"     env:"DATABASE_PATH"`
	RetentionDays   int      `json:"retention_days,omitempty"    env:"RETENTION_DAYS"`
	MaxPayloadBytes int      `json:"max_payload_bytes,omitempty" env:"MAX_PAYLOAD_BYTES"`
	RedactFields    []string `json:"redact_fields,omitempty"     env:"REDACT_FIELDS"`
}

// IsZero reports whether durable event ingress has no explicit configuration.
// encoding/json uses this with omitzero to preserve the shape of older v3 files.
func (c EventIngressConfig) IsZero() bool {
	return !c.Enabled &&
		strings.TrimSpace(c.DatabasePath) == "" &&
		c.RetentionDays == 0 &&
		c.MaxPayloadBytes == 0 &&
		len(c.RedactFields) == 0
}

const (
	// DefaultEventIngressRetentionDays is the durable inbox retention window.
	DefaultEventIngressRetentionDays = 30
	// DefaultEventIngressMaxPayloadBytes limits a normalized event payload to 1 MiB.
	DefaultEventIngressMaxPayloadBytes = 1 << 20
)

var defaultEventIngressRedactFields = [...]string{
	"authorization",
	"proxy_authorization",
	"cookie",
	"set_cookie",
	"password",
	"passwd",
	"secret",
	"token",
	"access_token",
	"refresh_token",
	"api_key",
	"client_secret",
	"private_key",
	"webhook_secret",
	"signature",
	"x_hub_signature",
	"x_hub_signature_256",
}

// DefaultEventIngressRedactFields returns an independent copy of the mandatory,
// case-insensitive field names removed before durable storage.
func DefaultEventIngressRedactFields() []string {
	return append([]string(nil), defaultEventIngressRedactFields[:]...)
}

// EventLoggingConfig controls centralized runtime event logging.
type EventLoggingConfig struct {
	// Enabled controls whether runtime events are printed by the built-in logger.
	Enabled bool `json:"enabled" env:"ENABLED"`
	// Include contains exact event kinds or glob patterns such as "agent.*" or "*".
	Include []string `json:"include,omitempty" env:"INCLUDE"`
	// Exclude contains exact event kinds or glob patterns to suppress after Include matches.
	Exclude []string `json:"exclude,omitempty" env:"EXCLUDE"`
	// MinSeverity filters out events below the configured severity: debug, info, warn, or error.
	MinSeverity string `json:"min_severity,omitempty" env:"MIN_SEVERITY"`
	// IncludePayload adds the raw payload to logs. Leave disabled unless detailed diagnostics are needed.
	IncludePayload bool `json:"include_payload,omitempty" env:"INCLUDE_PAYLOAD"`
}

// DefaultEventLoggingInclude keeps the pre-existing behavior where agent events
// are printed, while non-agent runtime events are published for subscribers only.
var DefaultEventLoggingInclude = []string{"agent.*"}

// EffectiveEventLoggingConfig returns a logging config with stable defaults.
func EffectiveEventLoggingConfig(cfg *Config) EventLoggingConfig {
	if cfg == nil {
		return defaultEventLoggingConfig()
	}

	out := cfg.Events.Logging
	if out.MinSeverity == "" {
		out.MinSeverity = "info"
	}
	if len(out.Include) == 0 {
		out.Include = append([]string(nil), DefaultEventLoggingInclude...)
	}
	return out
}

func defaultEventLoggingConfig() EventLoggingConfig {
	return EventLoggingConfig{
		Enabled:     true,
		Include:     append([]string(nil), DefaultEventLoggingInclude...),
		MinSeverity: "info",
	}
}

// EffectiveEventIngressConfig returns durable-ingress configuration with stable
// defaults. workspace is explicit so callers control where the database lives;
// an empty workspace intentionally produces the relative eventing/events.db
// path instead of consulting a process-global home directory.
func EffectiveEventIngressConfig(cfg *Config, workspace string) EventIngressConfig {
	var out EventIngressConfig
	if cfg != nil {
		out = cfg.Events.Ingress
	}

	workspace = strings.TrimSpace(workspace)
	if workspace != "" {
		workspace = expandEventIngressHome(workspace)
	}

	databasePath := strings.TrimSpace(out.DatabasePath)
	switch {
	case databasePath == "":
		out.DatabasePath = filepath.Join(workspace, "eventing", "events.db")
	case isEventIngressHomePath(databasePath):
		out.DatabasePath = filepath.Clean(expandEventIngressHome(databasePath))
	case filepath.IsAbs(databasePath):
		out.DatabasePath = filepath.Clean(databasePath)
	default:
		out.DatabasePath = filepath.Join(workspace, databasePath)
	}

	if out.RetentionDays <= 0 {
		out.RetentionDays = DefaultEventIngressRetentionDays
	}
	if out.MaxPayloadBytes <= 0 {
		out.MaxPayloadBytes = DefaultEventIngressMaxPayloadBytes
	}
	out.RedactFields = effectiveEventIngressRedactFields(out.RedactFields)

	return out
}

func isEventIngressHomePath(path string) bool {
	return path == "~" ||
		strings.HasPrefix(path, "~/") ||
		strings.HasPrefix(path, `~\`)
}

func expandEventIngressHome(path string) string {
	if !isEventIngressHomePath(path) {
		return path
	}
	home := expandHome("~")
	if home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	remainder := strings.TrimLeft(path[2:], `/\`)
	remainder = filepath.FromSlash(strings.ReplaceAll(remainder, `\`, "/"))
	return filepath.Join(home, remainder)
}

func effectiveEventIngressRedactFields(configured []string) []string {
	fields := make([]string, 0, len(defaultEventIngressRedactFields)+len(configured))
	seen := make(map[string]struct{}, cap(fields))

	add := func(field string) {
		field = strings.TrimSpace(field)
		if field == "" {
			return
		}
		key := strings.ToLower(field)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		fields = append(fields, field)
	}

	for _, field := range defaultEventIngressRedactFields {
		add(field)
	}
	for _, field := range configured {
		add(field)
	}
	return fields
}
