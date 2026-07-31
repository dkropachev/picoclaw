package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func eventDeltaChatChannel(t *testing.T, password string) *Channel {
	t.Helper()
	channel := &Channel{
		Enabled: true,
		Type:    ChannelDeltaChat,
	}
	settings := &DeltaChatSettings{Email: "events@example.org"}
	if password != "" {
		settings.Password = *NewSecureString(password)
	}
	if err := channel.Decode(settings); err != nil {
		t.Fatalf("Decode(DeltaChatSettings) error = %v", err)
	}
	return channel
}

func TestEventChannelIngressDisabledIsInert(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Events.Ingress.Channels = map[string]ChannelEventIngressConfig{
		" missing ": {
			Enabled: true,
			Source:  "unsupported",
			Mode:    "unsupported",
		},
		"MISSING": {Enabled: true},
	}

	if err := cfg.Events.Ingress.ValidateEventChannelAdapters(cfg.Channels); err != nil {
		t.Fatalf("disabled master validation error = %v, want nil", err)
	}
	if got := EffectiveEventChannelAdapters(cfg); got != nil {
		t.Fatalf("disabled master adapters = %#v, want nil", got)
	}

	cfg.Events.Ingress.Enabled = true
	cfg.Events.Ingress.Channels = map[string]ChannelEventIngressConfig{
		"missing": {
			Enabled: false,
			Source:  "unsupported",
			Mode:    "unsupported",
		},
	}
	if err := cfg.Events.Ingress.ValidateEventChannelAdapters(cfg.Channels); err != nil {
		t.Fatalf("disabled entry validation error = %v, want nil", err)
	}
	if got := EffectiveEventChannelAdapters(cfg); got != nil {
		t.Fatalf("disabled entry adapters = %#v, want nil", got)
	}
}

func TestEventIngressConfigIsZeroIncludesChannels(t *testing.T) {
	if !(EventIngressConfig{}).IsZero() {
		t.Fatal("zero event ingress config should report zero")
	}
	if !(EventIngressConfig{
		Channels: map[string]ChannelEventIngressConfig{},
	}).IsZero() {
		t.Fatal("empty channel map should remain semantically zero")
	}
	if (EventIngressConfig{
		Channels: map[string]ChannelEventIngressConfig{
			"telegram": {Enabled: false},
		},
	}).IsZero() {
		t.Fatal("configured channel adapter should make ingress non-zero")
	}
}

func TestEffectiveEventChannelAdaptersResolvesDefaultsAndInstances(t *testing.T) {
	cfg := &Config{
		Channels: ChannelsConfig{
			"team-inbox":    eventDeltaChatChannel(t, ""),
			"primary-inbox": eventDeltaChatChannel(t, ""),
			"explicit":      eventDeltaChatChannel(t, ""),
			"disabled": {
				Enabled: true,
				Type:    ChannelTelegram,
			},
		},
		Events: EventsConfig{
			Ingress: EventIngressConfig{
				Enabled: true,
				Channels: map[string]ChannelEventIngressConfig{
					"team-inbox": {Enabled: true},
					"primary-inbox": {
						Enabled:              true,
						AllowUnverifiedEmail: true,
					},
					"explicit": {
						Enabled: true,
						Source:  EventChannelSourceEmail,
						Mode:    EventChannelModeEventOnly,
					},
					"disabled": {Enabled: false},
				},
			},
		},
	}

	got := EffectiveEventChannelAdapters(cfg)
	want := map[string]EffectiveEventChannelAdapterConfig{
		"team-inbox": {
			Source:      EventChannelSourceEmail,
			Mode:        EventChannelModeMirror,
			ChannelType: ChannelDeltaChat,
		},
		"primary-inbox": {
			Source:               EventChannelSourceEmail,
			Mode:                 EventChannelModeMirror,
			ChannelType:          ChannelDeltaChat,
			AllowUnverifiedEmail: true,
		},
		"explicit": {
			Source:      EventChannelSourceEmail,
			Mode:        EventChannelModeEventOnly,
			ChannelType: ChannelDeltaChat,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("effective adapters = %#v, want %#v", got, want)
	}

	got["team-inbox"] = EffectiveEventChannelAdapterConfig{Source: "changed"}
	delete(got, "primary-inbox")
	if cfg.Events.Ingress.Channels["team-inbox"].Source != "" {
		t.Fatal("mutating effective adapters changed source configuration")
	}
	if _, exists := cfg.Events.Ingress.Channels["primary-inbox"]; !exists {
		t.Fatal("mutating effective adapters deleted source configuration")
	}
}

func TestEffectiveEventIngressConfigDeepCopiesChannels(t *testing.T) {
	cfg := &Config{
		Events: EventsConfig{
			Ingress: EventIngressConfig{
				Channels: map[string]ChannelEventIngressConfig{
					"mail": {
						Enabled: true,
						Source:  EventChannelSourceEmail,
						Mode:    EventChannelModeMirror,
					},
				},
			},
		},
	}

	got := EffectiveEventIngressConfig(cfg, t.TempDir())
	adapter := got.Channels["mail"]
	adapter.Source = "changed"
	got.Channels["mail"] = adapter
	delete(got.Channels, "mail")

	original, exists := cfg.Events.Ingress.Channels["mail"]
	if !exists {
		t.Fatal("mutating effective channels deleted source configuration")
	}
	if original.Source != EventChannelSourceEmail {
		t.Fatal("mutating effective channels changed source configuration")
	}
}

func TestValidateEventChannelAdapters(t *testing.T) {
	enabledChannels := ChannelsConfig{
		"mail": eventDeltaChatChannel(t, ""),
		"disabled": {
			Enabled: false,
			Type:    ChannelDeltaChat,
		},
		"telegram": {
			Enabled: true,
			Type:    ChannelTelegram,
		},
		"mqtt": {
			Enabled: true,
			Type:    ChannelMQTT,
		},
	}

	tests := []struct {
		name     string
		adapters map[string]ChannelEventIngressConfig
		want     string
	}{
		{
			name: "email defaults",
			adapters: map[string]ChannelEventIngressConfig{
				"mail": {Enabled: true},
			},
		},
		{
			name: "explicit email event only",
			adapters: map[string]ChannelEventIngressConfig{
				"mail": {
					Enabled: true,
					Source:  EventChannelSourceEmail,
					Mode:    EventChannelModeEventOnly,
				},
			},
		},
		{
			name: "explicit unverified email opt in",
			adapters: map[string]ChannelEventIngressConfig{
				"mail": {
					Enabled:              true,
					Source:               EventChannelSourceEmail,
					AllowUnverifiedEmail: true,
				},
			},
		},
		{
			name: "missing channel",
			adapters: map[string]ChannelEventIngressConfig{
				"missing": {Enabled: true},
			},
			want: "does not reference an existing channel",
		},
		{
			name: "disabled channel",
			adapters: map[string]ChannelEventIngressConfig{
				"disabled": {Enabled: true},
			},
			want: "references a disabled channel",
		},
		{
			name: "Telegram is unsupported",
			adapters: map[string]ChannelEventIngressConfig{
				"telegram": {Enabled: true},
			},
			want: "unsupported channel type",
		},
		{
			name: "MQTT is unsupported",
			adapters: map[string]ChannelEventIngressConfig{
				"mqtt": {Enabled: true},
			},
			want: "unsupported channel type",
		},
		{
			name: "invalid source",
			adapters: map[string]ChannelEventIngressConfig{
				"mail": {
					Enabled: true,
					Source:  "EMAIL",
				},
			},
			want: `requires source "email"`,
		},
		{
			name: "invalid mode",
			adapters: map[string]ChannelEventIngressConfig{
				"mail": {
					Enabled: true,
					Mode:    "event-only",
				},
			},
			want: "unsupported mode",
		},
		{
			name: "Delta Chat requires email source",
			adapters: map[string]ChannelEventIngressConfig{
				"mail": {
					Enabled: true,
					Source:  EventChannelSourceChat,
				},
			},
			want: `requires source "email"`,
		},
		{
			name: "empty name",
			adapters: map[string]ChannelEventIngressConfig{
				"": {Enabled: false},
			},
			want: "exactly trimmed",
		},
		{
			name: "untrimmed name",
			adapters: map[string]ChannelEventIngressConfig{
				" chat": {Enabled: false},
			},
			want: "exactly trimmed",
		},
		{
			name: "long name",
			adapters: map[string]ChannelEventIngressConfig{
				strings.Repeat("x", eventChannelMaxNameBytes+1): {Enabled: false},
			},
			want: "at most 256 bytes",
		},
		{
			name: "invalid UTF-8 name",
			adapters: map[string]ChannelEventIngressConfig{
				string([]byte{0xff}): {Enabled: false},
			},
			want: "valid UTF-8",
		},
		{
			name: "case collision",
			adapters: map[string]ChannelEventIngressConfig{
				"Chat": {Enabled: false},
				"chat": {Enabled: false},
			},
			want: "differ only by case",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := (EventIngressConfig{
				Enabled:  true,
				Channels: tt.adapters,
			}).ValidateEventChannelAdapters(enabledChannels)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("ValidateEventChannelAdapters() error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateEventChannelAdapters() error = nil, want containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateEventChannelAdapters() error = %q, want containing %q", err, tt.want)
			}
		})
	}
}

func TestValidateEventChannelAdaptersRejectsEveryUnsupportedChannelType(t *testing.T) {
	unsupportedTypes := []string{
		ChannelPico,
		ChannelPicoClient,
		ChannelTelegram,
		ChannelDiscord,
		ChannelFeishu,
		ChannelWeixin,
		ChannelWeCom,
		ChannelDingTalk,
		ChannelSlack,
		ChannelMatrix,
		ChannelLINE,
		ChannelOneBot,
		ChannelQQ,
		ChannelIRC,
		ChannelVK,
		ChannelMaixCam,
		ChannelWhatsApp,
		ChannelWhatsAppNative,
		ChannelTeamsWebHook,
		ChannelMQTT,
		ChannelSlackWebHook,
		"custom_transport",
	}

	for _, channelType := range unsupportedTypes {
		t.Run(channelType, func(t *testing.T) {
			channels := ChannelsConfig{
				"candidate": {
					Enabled: true,
					Type:    channelType,
				},
			}
			err := (EventIngressConfig{
				Enabled: true,
				Channels: map[string]ChannelEventIngressConfig{
					"candidate": {Enabled: true},
				},
			}).ValidateEventChannelAdapters(channels)
			if err == nil ||
				!strings.Contains(err.Error(), "unsupported channel type") ||
				!strings.Contains(err.Error(), channelType) {
				t.Fatalf(
					"ValidateEventChannelAdapters(%q) error = %v, want unsupported type",
					channelType,
					err,
				)
			}
		})
	}
}

func TestValidateEventChannelAdaptersRejectsSecretBearingIdentityOpaquely(t *testing.T) {
	const secret = "resolved-channel-secret"
	connector := "mail-" + secret
	for _, test := range []struct {
		name          string
		masterEnabled bool
	}{
		{name: "enabled master", masterEnabled: true},
		{name: "disabled master", masterEnabled: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := &Config{
				Channels: ChannelsConfig{
					connector: eventDeltaChatChannel(t, secret),
				},
				Events: EventsConfig{
					Ingress: EventIngressConfig{
						Enabled: test.masterEnabled,
						Channels: map[string]ChannelEventIngressConfig{
							connector: {Enabled: true},
						},
					},
				},
			}
			err := cfg.Events.Ingress.ValidateEventChannelAdapters(
				cfg.Channels,
				cfg.SensitiveDataValues()...,
			)
			if err == nil {
				t.Fatal("ValidateEventChannelAdapters() error = nil")
			}
			if err.Error() != eventChannelSecretConflictMessage {
				t.Fatalf("validation error = %q, want opaque conflict", err)
			}
			if strings.Contains(err.Error(), secret) ||
				strings.Contains(err.Error(), connector) {
				t.Fatalf("validation error exposed protected identity: %q", err)
			}
		})
	}
}

func TestLoadConfigValidatesEventChannelAdaptersAfterChannelInitialization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := []byte(`{
		"version": 4,
		"events": {
			"ingress": {
				"enabled": true,
				"channels": {
					"missing-instance": {"enabled": true}
				}
			}
		}
	}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("LoadConfig() error = nil, want missing event channel error")
	}
	if !strings.Contains(err.Error(), "invalid event channel ingress config") {
		t.Fatalf("LoadConfig() error = %q, want event channel validation context", err)
	}
}

func TestLoadConfigResolvesCustomDeltaChatEventChannelInstance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := []byte(`{
		"version": 4,
		"channel_list": {
			"primary-inbox": {
				"enabled": true,
				"type": "deltachat"
			}
		},
		"events": {
			"ingress": {
				"enabled": true,
				"channels": {
					"primary-inbox": {"enabled": true}
				}
			}
		}
	}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	got := EffectiveEventChannelAdapters(cfg)
	if got["primary-inbox"] != (EffectiveEventChannelAdapterConfig{
		Source:      EventChannelSourceEmail,
		Mode:        EventChannelModeMirror,
		ChannelType: ChannelDeltaChat,
	}) {
		t.Fatalf("custom Delta Chat adapter = %#v, want email mirror defaults", got["primary-inbox"])
	}
}

func TestEventChannelIngressJSONRoundTrip(t *testing.T) {
	want := EventIngressConfig{
		Enabled: true,
		Channels: map[string]ChannelEventIngressConfig{
			"mail": {
				Enabled:              true,
				Source:               EventChannelSourceEmail,
				Mode:                 EventChannelModeEventOnly,
				AllowUnverifiedEmail: false,
			},
		},
		Webhooks: map[string]GenericWebhookConfig{
			"hook": {Enabled: false},
		},
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var got EventIngressConfig
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("JSON round trip = %#v, want %#v", got, want)
	}
}

func TestEventChannelIngressDoesNotChangeWebhookSecuritySerialization(t *testing.T) {
	mustSetupSSHKey(t)
	path := filepath.Join(t.TempDir(), "config.json")
	secret := eventWebhookTestSecret("channel-security")
	cfg := DefaultConfig()
	cfg.Events.Ingress = EventIngressConfig{
		Enabled: true,
		Channels: map[string]ChannelEventIngressConfig{
			"telegram": {Enabled: false},
		},
		Webhooks: map[string]GenericWebhookConfig{
			"build": {
				Enabled: true,
				Secret:  *NewSecureString(secret),
			},
		},
	}

	if err := SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	securityData, err := os.ReadFile(securityPath(path))
	if err != nil {
		t.Fatalf("read security config: %v", err)
	}
	var security struct {
		Events struct {
			Ingress map[string]yaml.Node `yaml:"ingress"`
		} `yaml:"events"`
	}
	if unmarshalErr := yaml.Unmarshal(securityData, &security); unmarshalErr != nil {
		t.Fatalf("unmarshal security config: %v", unmarshalErr)
	}
	if _, exists := security.Events.Ingress["channels"]; exists {
		t.Fatalf("event channels leaked into security config:\n%s", securityData)
	}
	if _, exists := security.Events.Ingress["webhooks"]; !exists {
		t.Fatalf("webhook security config missing:\n%s", securityData)
	}

	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if _, exists := loaded.Events.Ingress.Channels["telegram"]; !exists {
		t.Fatal("event channel config missing after save/load")
	}
	loadedWebhook := loaded.Events.Ingress.Webhooks["build"]
	if loadedWebhook.Secret.String() != secret {
		t.Fatal("webhook secret changed after save/load")
	}
}
