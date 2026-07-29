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
	"unicode/utf8"

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
	// Per-connector enablement remains JSON-owned; the named maps have no env mapping.
	Webhooks map[string]GenericWebhookConfig      `json:"webhooks,omitempty"`
	Channels map[string]ChannelEventIngressConfig `json:"channels,omitempty"`
}

// ChannelEventIngressConfig controls durable event ingestion for one existing
// channel instance. Empty Source and Mode values receive stable defaults from
// EffectiveEventChannelAdapters.
type ChannelEventIngressConfig struct {
	Enabled              bool   `json:"enabled"`
	Source               string `json:"source,omitempty"`
	Mode                 string `json:"mode,omitempty"`
	AllowUnverifiedEmail bool   `json:"allow_unverified_email,omitempty"`
}

// EffectiveEventChannelAdapterConfig is the resolved runtime configuration for
// an enabled channel event adapter.
type EffectiveEventChannelAdapterConfig struct {
	Source               string
	Mode                 string
	ChannelType          string
	AllowUnverifiedEmail bool
}

// GenericWebhookConfig controls one named event endpoint. An omitted Format
// preserves the original Standard Webhooks behavior. Secret is stored in
// .security.yml; config.json contains only the [NOT_HERE] marker when a secret
// is configured.
type GenericWebhookConfig struct {
	Enabled bool         `json:"enabled"`
	Format  string       `json:"format,omitempty"`
	Secret  SecureString `json:"secret,omitzero"  yaml:"secret,omitempty"`
}

// MarshalJSON keeps a stable marker in config.json without ever exposing the
// webhook signing secret. A connector with no secret omits the field.
func (c GenericWebhookConfig) MarshalJSON() ([]byte, error) {
	type genericWebhookJSON struct {
		Enabled bool          `json:"enabled"`
		Format  string        `json:"format,omitempty"`
		Secret  *SecureString `json:"secret,omitempty"`
	}

	var secret *SecureString
	if c.Secret.raw != "" || c.Secret.resolved != "" {
		secretCopy := c.Secret
		secret = &secretCopy
	}
	return json.Marshal(genericWebhookJSON{
		Enabled: c.Enabled,
		Format:  c.Format,
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
		Format  *string         `json:"format"`
		Secret  json.RawMessage `json:"secret"`
	}
	if err := json.Unmarshal(value, &decoded); err != nil {
		return err
	}
	if decoded.Enabled != nil {
		c.Enabled = *decoded.Enabled
	}
	if decoded.Format != nil {
		c.Format = *decoded.Format
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
		len(c.Webhooks) == 0 &&
		len(c.Channels) == 0
}

const (
	genericWebhookSecretPrefix                   = "whsec_"
	genericWebhookMinSecretBytes                 = 32
	genericWebhookMaxConnectorBytes              = 64
	genericWebhookConnectorSecretConflictMessage = "webhook connector identity conflicts with a signing secret"
	genericWebhookSecretResolutionMessage        = "resolve event webhook signing secret"
	eventChannelSecretConflictMessage            = "event channel identity conflicts with a configured secret"

	// EventChannelSourceChat normalizes channel messages as chat events.
	EventChannelSourceChat = "chat"
	// EventChannelSourceEmail normalizes Delta Chat messages as email events.
	EventChannelSourceEmail = "email"
	// EventChannelModeMirror preserves the existing chat path and also emits a
	// durable event.
	EventChannelModeMirror = "mirror"
	// EventChannelModeEventOnly emits a durable event without forwarding the
	// message through the existing chat path.
	EventChannelModeEventOnly = "event_only"

	// EventWebhookFormatStandard verifies Standard Webhooks signatures.
	EventWebhookFormatStandard = "standard"
	// EventWebhookFormatGitHub verifies GitHub webhook signatures.
	EventWebhookFormatGitHub = "github"

	eventChannelMaxNameBytes    = 256
	githubWebhookMinSecretBytes = 32
	githubWebhookMaxSecretBytes = 256
)

var genericWebhookConnectorNamePattern = regexp.MustCompile(
	`^[A-Za-z][A-Za-z0-9_-]{0,63}$`,
)

var eventChannelSupportedTypes = map[string]struct{}{
	ChannelDeltaChat: {},
}

// Validate checks the generic webhook portion of durable-ingress
// configuration. The master Enabled flag is an inert kill switch: when it is
// false, connector details are deliberately not validated or activated.
// Credential-bearing map keys are always rejected because configuration
// persistence is independent of runtime activation.
func (c EventIngressConfig) Validate() error {
	if err := c.validateWebhookPublicIdentities(); err != nil {
		return err
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
		switch webhook.Format {
		case "", EventWebhookFormatStandard, EventWebhookFormatGitHub:
		default:
			return fmt.Errorf(
				"webhook connector %q has unsupported format %q",
				name,
				webhook.Format,
			)
		}
		if err := validateEventWebhookSecret(webhook); err != nil {
			return fmt.Errorf("webhook connector %q: %w", name, err)
		}
	}
	return nil
}

func (c EventIngressConfig) validateWebhookPublicIdentities() error {
	secrets := make([]string, 0, len(c.Webhooks))
	for _, webhook := range c.Webhooks {
		secret := webhook.Secret.String()
		if secret != "" &&
			(validateGenericWebhookSecret(secret) == nil ||
				validateGitHubWebhookSecret(secret) == nil ||
				c.Enabled && webhook.Enabled) {
			secrets = append(secrets, secret)
		}
	}
	for name, webhook := range c.Webhooks {
		for _, publicValue := range [...]string{name, webhook.Format} {
			for _, secret := range secrets {
				if strings.Contains(publicValue, secret) {
					return errors.New(genericWebhookConnectorSecretConflictMessage)
				}
			}
		}
	}
	return nil
}

// EffectiveEventWebhookFormat returns the format used by a webhook connector.
// Empty values retain the original Standard Webhooks behavior.
func EffectiveEventWebhookFormat(webhook GenericWebhookConfig) string {
	if webhook.Format == "" {
		return EventWebhookFormatStandard
	}
	return webhook.Format
}

func validateEventWebhookSecret(webhook GenericWebhookConfig) error {
	switch EffectiveEventWebhookFormat(webhook) {
	case EventWebhookFormatStandard:
		return validateGenericWebhookSecret(webhook.Secret.String())
	case EventWebhookFormatGitHub:
		return validateGitHubWebhookSecret(webhook.Secret.String())
	default:
		return errors.New("unsupported webhook format")
	}
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

func validateGitHubWebhookSecret(secret string) error {
	if !utf8.ValidString(secret) {
		return fmt.Errorf("GitHub secret must be valid UTF-8")
	}
	if secret != strings.TrimSpace(secret) {
		return fmt.Errorf("GitHub secret must not have leading or trailing whitespace")
	}
	if len(secret) < githubWebhookMinSecretBytes ||
		len(secret) > githubWebhookMaxSecretBytes {
		return fmt.Errorf(
			"GitHub secret must be between %d and %d bytes",
			githubWebhookMinSecretBytes,
			githubWebhookMaxSecretBytes,
		)
	}
	return nil
}

// ValidateEventChannelAdapters validates channel event adapters against the
// initialized channel list. The master ingress switch leaves structural bodies
// inert, but credential-bearing names are always rejected because map keys are
// persisted independently of runtime activation.
func (c EventIngressConfig) ValidateEventChannelAdapters(
	channels ChannelsConfig,
	sensitiveValues ...string,
) error {
	for name := range c.Channels {
		for _, secret := range sensitiveValues {
			if len(secret) > 3 && strings.Contains(name, secret) {
				// Connector identities persist as unredacted JSON map keys even
				// while master ingress is disabled. Never echo either side of
				// this conflict.
				return errors.New(eventChannelSecretConflictMessage)
			}
		}
	}
	if !c.Enabled {
		return nil
	}

	names := make([]string, 0, len(c.Channels))
	for name := range c.Channels {
		names = append(names, name)
	}
	sort.Strings(names)

	for index, name := range names {
		if name == "" ||
			name != strings.TrimSpace(name) ||
			!utf8.ValidString(name) ||
			len(name) > eventChannelMaxNameBytes {
			return fmt.Errorf(
				"event channel name %q must be non-empty, valid UTF-8, exactly trimmed, and at most %d bytes",
				name,
				eventChannelMaxNameBytes,
			)
		}
		for _, previous := range names[:index] {
			if strings.EqualFold(previous, name) {
				return fmt.Errorf(
					"event channel names %q and %q differ only by case",
					previous,
					name,
				)
			}
		}

		adapter := c.Channels[name]
		if !adapter.Enabled {
			continue
		}

		channel, exists := channels[name]
		if !exists || channel == nil {
			return fmt.Errorf("event channel %q does not reference an existing channel", name)
		}
		if !channel.Enabled {
			return fmt.Errorf("event channel %q references a disabled channel", name)
		}

		channelType := effectiveEventChannelType(name, channel)
		if _, supported := eventChannelSupportedTypes[channelType]; !supported {
			return fmt.Errorf(
				"event channel %q uses unsupported channel type %q",
				name,
				channelType,
			)
		}
		effective := effectiveEventChannelAdapterConfig(adapter, channelType)
		if channelType == ChannelDeltaChat &&
			effective.Source != EventChannelSourceEmail {
			return fmt.Errorf(
				"event channel %q type %q requires source %q",
				name,
				ChannelDeltaChat,
				EventChannelSourceEmail,
			)
		}
		switch effective.Source {
		case EventChannelSourceChat:
		case EventChannelSourceEmail:
			if channelType != ChannelDeltaChat {
				return fmt.Errorf(
					"event channel %q source %q requires channel type %q",
					name,
					EventChannelSourceEmail,
					ChannelDeltaChat,
				)
			}
		default:
			return fmt.Errorf(
				"event channel %q has unsupported source %q",
				name,
				effective.Source,
			)
		}

		switch effective.Mode {
		case EventChannelModeMirror, EventChannelModeEventOnly:
		default:
			return fmt.Errorf(
				"event channel %q has unsupported mode %q",
				name,
				effective.Mode,
			)
		}
		if adapter.AllowUnverifiedEmail &&
			effective.Source != EventChannelSourceEmail {
			return fmt.Errorf(
				"event channel %q allows unverified email but source is %q",
				name,
				effective.Source,
			)
		}
	}
	return nil
}

// EffectiveEventChannelAdapters returns independent, fully resolved runtime
// adapter configuration for enabled entries. A disabled master ingress switch
// produces no adapters, preserving existing channel behavior.
func EffectiveEventChannelAdapters(
	cfg *Config,
) map[string]EffectiveEventChannelAdapterConfig {
	if cfg == nil || !cfg.Events.Ingress.Enabled {
		return nil
	}

	var out map[string]EffectiveEventChannelAdapterConfig
	for name, adapter := range cfg.Events.Ingress.Channels {
		if !adapter.Enabled {
			continue
		}
		channel := cfg.Channels[name]
		if channel == nil || !channel.Enabled {
			// Validation reports the configuration error. Keeping this helper
			// defensive prevents callers from constructing a partial adapter
			// against an invalid or not-yet-initialized candidate.
			continue
		}
		if out == nil {
			out = make(map[string]EffectiveEventChannelAdapterConfig)
		}
		channelType := effectiveEventChannelType(name, channel)
		out[name] = effectiveEventChannelAdapterConfig(adapter, channelType)
	}
	return out
}

func effectiveEventChannelAdapterConfig(
	adapter ChannelEventIngressConfig,
	channelType string,
) EffectiveEventChannelAdapterConfig {
	source := adapter.Source
	if source == "" {
		source = EventChannelSourceChat
		if channelType == ChannelDeltaChat {
			source = EventChannelSourceEmail
		}
	}
	mode := adapter.Mode
	if mode == "" {
		mode = EventChannelModeMirror
	}
	return EffectiveEventChannelAdapterConfig{
		Source:               source,
		Mode:                 mode,
		ChannelType:          channelType,
		AllowUnverifiedEmail: adapter.AllowUnverifiedEmail,
	}
}

func effectiveEventChannelType(name string, channel *Channel) string {
	if channel == nil {
		return ""
	}
	if channel.Type == "" {
		return name
	}
	return channel.Type
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
			webhook.Format = EffectiveEventWebhookFormat(webhook)
			out.Webhooks[name] = webhook
		}
	}
	if len(out.Channels) > 0 {
		out.Channels = make(map[string]ChannelEventIngressConfig, len(out.Channels))
		for name, adapter := range cfg.Events.Ingress.Channels {
			out.Channels[name] = adapter
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
