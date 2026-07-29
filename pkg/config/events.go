package config

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// EventsConfig groups process-local observability and durable ingress
// configuration.
//
//nolint:recvcheck // YAML requires value marshal/zero methods and pointer unmarshal.
type EventsConfig struct {
	Logging EventLoggingConfig `json:"logging,omitempty" envPrefix:"PICOCLAW_EVENTS_LOGGING_"`
	Ingress EventIngressConfig `json:"ingress,omitzero"  envPrefix:"PICOCLAW_EVENTS_INGRESS_"`
}

// EventIngressConfig controls durable external-event ingestion.
//
// Ingress is opt-in. RetentionDays, MaxPayloadBytes, DatabasePath, and
// RedactFields receive their stable defaults through EffectiveEventIngressConfig
// so the zero value remains disabled and can be omitted from saved v3 configs.
//
//nolint:recvcheck // resolution mutates after env parsing; validation and zero checks do not.
type EventIngressConfig struct {
	Enabled         bool     `json:"enabled"                     env:"ENABLED"`
	DatabasePath    string   `json:"database_path,omitempty"     env:"DATABASE_PATH"`
	RetentionDays   int      `json:"retention_days,omitempty"    env:"RETENTION_DAYS"`
	MaxPayloadBytes int      `json:"max_payload_bytes,omitempty" env:"MAX_PAYLOAD_BYTES"`
	RedactFields    []string `json:"redact_fields,omitempty"     env:"REDACT_FIELDS"`
	// Per-connector enablement remains JSON-owned; the named map has no env mapping.
	Webhooks map[string]GenericWebhookConfig `json:"webhooks,omitempty"`
}

// GenericWebhookConfig controls one named, Standard Webhooks-compatible event
// endpoint. Secret is stored in .security.yml; config.json contains only the
// [NOT_HERE] marker when a secret is configured.
type GenericWebhookConfig struct {
	Enabled bool         `json:"enabled"`
	Secret  SecureString `json:"secret,omitzero" yaml:"secret,omitempty"`
}

// MarshalJSON keeps a stable marker in config.json without ever exposing the
// webhook signing secret. A connector with no secret omits the field.
func (c GenericWebhookConfig) MarshalJSON() ([]byte, error) {
	type genericWebhookJSON struct {
		Enabled bool          `json:"enabled"`
		Secret  *SecureString `json:"secret,omitempty"`
	}

	var secret *SecureString
	if c.Secret.raw != "" || c.Secret.resolved != "" {
		secretCopy := c.Secret
		secret = &secretCopy
	}
	return json.Marshal(genericWebhookJSON{
		Enabled: c.Enabled,
		Secret:  secret,
	})
}

// UnmarshalJSON preserves a webhook secret's raw representation until the
// complete config, including the supported master env override, is available.
// This prevents an inactive file:// or enc:// reference from being resolved
// before env parsing can disable ingress.
func (c *GenericWebhookConfig) UnmarshalJSON(value []byte) error {
	var decoded struct {
		Enabled *bool           `json:"enabled"`
		Secret  json.RawMessage `json:"secret"`
	}
	if err := json.Unmarshal(value, &decoded); err != nil {
		return err
	}
	if decoded.Enabled != nil {
		c.Enabled = *decoded.Enabled
	}
	if len(decoded.Secret) == 0 || string(decoded.Secret) == notHere {
		return nil
	}

	var secret string
	if err := json.Unmarshal(decoded.Secret, &secret); err != nil {
		return err
	}
	c.Secret = unresolvedEventWebhookSecret(secret)
	return nil
}

// IsZero reports whether durable event ingress has no explicit configuration.
// encoding/json uses this with omitzero to preserve the shape of older v3 files.
func (c EventIngressConfig) IsZero() bool {
	return !c.Enabled &&
		strings.TrimSpace(c.DatabasePath) == "" &&
		c.RetentionDays == 0 &&
		c.MaxPayloadBytes == 0 &&
		len(c.RedactFields) == 0 &&
		len(c.Webhooks) == 0
}

const (
	genericWebhookSecretPrefix                   = "whsec_"
	genericWebhookMinSecretBytes                 = 32
	genericWebhookMaxConnectorBytes              = 64
	genericWebhookConnectorSecretConflictMessage = "webhook connector identity conflicts with a signing secret"
	genericWebhookSecretResolutionMessage        = "resolve event webhook signing secret"
)

var genericWebhookConnectorNamePattern = regexp.MustCompile(
	`^[A-Za-z][A-Za-z0-9_-]{0,63}$`,
)

// Validate checks the generic webhook portion of durable-ingress
// configuration. The master Enabled flag is an inert kill switch: when it is
// false, connector details are deliberately not validated or activated.
// Credential-bearing map keys are always rejected because configuration
// persistence is independent of runtime activation.
func (c EventIngressConfig) Validate() error {
	identitySecrets := make([]string, 0, len(c.Webhooks))
	for _, webhook := range c.Webhooks {
		secret := webhook.Secret.String()
		if secret == "" {
			continue
		}
		if validateGenericWebhookSecret(secret) == nil ||
			(c.Enabled && webhook.Enabled) {
			identitySecrets = append(identitySecrets, secret)
		}
	}
	for name := range c.Webhooks {
		for _, secret := range identitySecrets {
			if strings.Contains(name, secret) {
				return errors.New(genericWebhookConnectorSecretConflictMessage)
			}
		}
	}
	if !c.Enabled {
		return nil
	}

	names := make([]string, 0, len(c.Webhooks))
	for name := range c.Webhooks {
		names = append(names, name)
	}
	sort.Strings(names)

	caseFolded := make(map[string]string, len(names))
	for _, name := range names {
		if len(name) > genericWebhookMaxConnectorBytes ||
			!genericWebhookConnectorNamePattern.MatchString(name) {
			return fmt.Errorf(
				"webhook connector %q must match %s and be at most %d bytes",
				name,
				genericWebhookConnectorNamePattern.String(),
				genericWebhookMaxConnectorBytes,
			)
		}

		folded := strings.ToLower(name)
		if previous, exists := caseFolded[folded]; exists {
			return fmt.Errorf(
				"webhook connector names %q and %q differ only by case",
				previous,
				name,
			)
		}
		caseFolded[folded] = name

		webhook := c.Webhooks[name]
		if !webhook.Enabled {
			continue
		}
		if err := validateGenericWebhookSecret(webhook.Secret.String()); err != nil {
			return fmt.Errorf("webhook connector %q: %w", name, err)
		}
	}
	return nil
}

func validateGenericWebhookSecret(secret string) error {
	if !strings.HasPrefix(secret, genericWebhookSecretPrefix) {
		return fmt.Errorf("secret must use the %s Standard Webhooks format", genericWebhookSecretPrefix)
	}

	encoded := strings.TrimPrefix(secret, genericWebhookSecretPrefix)
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil ||
		base64.StdEncoding.EncodeToString(decoded) != encoded {
		return fmt.Errorf("secret must contain canonical base64")
	}
	if len(decoded) < genericWebhookMinSecretBytes {
		return fmt.Errorf(
			"secret must decode to at least %d bytes",
			genericWebhookMinSecretBytes,
		)
	}
	return nil
}

type genericWebhookYAML struct {
	Secret SecureString `yaml:"secret,omitempty"`
}

// IsZero lets .security.yml omit the events subtree unless it has webhook
// secrets to persist. JSON serialization is unaffected because EventsConfig is
// not tagged with omitzero.
func (c EventsConfig) IsZero() bool {
	for _, webhook := range c.Ingress.Webhooks {
		if webhook.Secret.raw != "" || webhook.Secret.resolved != "" {
			return false
		}
	}
	return true
}

// MarshalYAML persists only webhook secrets. Operational ingress fields and
// event-logging settings remain exclusively in config.json.
func (c EventsConfig) MarshalYAML() (any, error) {
	webhooks := make(map[string]genericWebhookYAML)
	for name, webhook := range c.Ingress.Webhooks {
		if webhook.Secret.raw == "" && webhook.Secret.resolved == "" {
			continue
		}
		webhooks[name] = genericWebhookYAML{Secret: webhook.Secret}
	}

	return struct {
		Ingress struct {
			Webhooks map[string]genericWebhookYAML `yaml:"webhooks,omitempty"`
		} `yaml:"ingress,omitempty"`
	}{
		Ingress: struct {
			Webhooks map[string]genericWebhookYAML `yaml:"webhooks,omitempty"`
		}{
			Webhooks: webhooks,
		},
	}, nil
}

// UnmarshalYAML overlays webhook secrets onto connector names already loaded
// from config.json. It intentionally ignores operational fields and
// security-only connector names, so stale .security.yml entries cannot
// resurrect deleted connectors.
func (c *EventsConfig) UnmarshalYAML(value *yaml.Node) error {
	ingressNode, err := eventSecurityYAMLField(value, "ingress")
	if err != nil || ingressNode == nil {
		return err
	}
	webhooksNode, err := eventSecurityYAMLField(ingressNode, "webhooks")
	if err != nil || webhooksNode == nil {
		return err
	}
	if webhooksNode.Kind != yaml.MappingNode {
		return fmt.Errorf("webhooks must be a mapping")
	}

	for index := 0; index < len(webhooksNode.Content); index += 2 {
		name := webhooksNode.Content[index].Value
		webhook, exists := c.Ingress.Webhooks[name]
		if !exists {
			// Do not decode stale security-only secrets. Besides preventing
			// resurrection, this lets operators remove a connector whose old
			// file:// or enc:// reference is no longer resolvable.
			continue
		}

		secureWebhookNode := webhooksNode.Content[index+1]
		secretNode, fieldErr := eventSecurityYAMLField(secureWebhookNode, "secret")
		if fieldErr != nil {
			return fmt.Errorf("event webhook secret: %w", fieldErr)
		}
		if secretNode == nil {
			continue
		}
		if secretNode.Kind != yaml.ScalarNode {
			return fmt.Errorf("event webhook secret must be a scalar")
		}

		webhook.Secret = unresolvedEventWebhookSecret(secretNode.Value)
		c.Ingress.Webhooks[name] = webhook
	}
	return nil
}

func unresolvedEventWebhookSecret(raw string) SecureString {
	return SecureString{raw: raw, resolved: raw}
}

// CopyWebhookSecretsFrom restores only secrets for connector names that exist
// in the persisted operational config. Candidate-only names are cleared so a
// stale .security.yml entry cannot resurrect a deleted connector credential.
func (c *EventIngressConfig) CopyWebhookSecretsFrom(existing EventIngressConfig) {
	if c == nil {
		return
	}
	for name, webhook := range c.Webhooks {
		if persisted, exists := existing.Webhooks[name]; exists {
			webhook.Secret = persisted.Secret
		} else {
			webhook.Secret = SecureString{}
		}
		c.Webhooks[name] = webhook
	}
}

// ApplyWebhookSecrets replaces explicit raw values as one candidate batch, then
// quietly resolves all active webhook references. Inactive references remain
// unresolved so management requests can preserve unavailable credentials.
func (c *EventIngressConfig) ApplyWebhookSecrets(secrets map[string]string) error {
	if c == nil {
		return nil
	}
	for name, raw := range secrets {
		webhook, exists := c.Webhooks[name]
		if !exists {
			continue
		}
		webhook.Secret = unresolvedEventWebhookSecret(raw)
		c.Webhooks[name] = webhook
	}
	return c.resolveWebhookSecrets()
}

// resolveWebhookSecrets resolves references only for connectors made active by
// the final candidate config. Disabled entries retain their raw representation
// so load/save operations preserve references without touching unavailable
// credential files or encryption keys.
func (c *EventIngressConfig) resolveWebhookSecrets() error {
	if c == nil || !c.Enabled {
		return nil
	}
	names := make([]string, 0, len(c.Webhooks))
	for name := range c.Webhooks {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		webhook := c.Webhooks[name]
		if !webhook.Enabled || webhook.Secret.raw == "" {
			continue
		}
		resolved, err := resolveKeyQuiet(webhook.Secret.raw)
		if err != nil {
			return errors.New(genericWebhookSecretResolutionMessage)
		}
		webhook.Secret.resolved = resolved
		c.Webhooks[name] = webhook
	}
	return nil
}

func eventSecurityYAMLField(node *yaml.Node, field string) (*yaml.Node, error) {
	if node == nil {
		return nil, nil
	}
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s must be a mapping", field)
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == field {
			return node.Content[index+1], nil
		}
	}
	return nil, nil
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
	if len(out.Webhooks) > 0 {
		out.Webhooks = make(map[string]GenericWebhookConfig, len(out.Webhooks))
		for name, webhook := range cfg.Events.Ingress.Webhooks {
			out.Webhooks[name] = webhook
		}
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
