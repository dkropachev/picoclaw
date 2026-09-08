package api

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestLauncherManagementChannelAndQueryHelperCoverage(t *testing.T) {
	if defaultChannelConfig(config.ChannelTelegram) == nil || defaultChannelConfig("missing") != nil {
		t.Fatal("default channel lookup mismatch")
	}
	defaultResponse := buildChannelConfigResponse(
		&config.Config{Channels: config.ChannelsConfig{}},
		channelCatalogItem{Name: "telegram", ConfigKey: config.ChannelTelegram},
	)
	if defaultResponse.Config == nil {
		t.Fatal("missing configured channel did not use defaults")
	}
	missingResponse := buildChannelConfigResponse(
		&config.Config{Channels: config.ChannelsConfig{}},
		channelCatalogItem{Name: "missing", ConfigKey: "missing"},
	)
	if !reflect.DeepEqual(missingResponse.Config, map[string]any{}) {
		t.Fatalf("unknown default channel response = %#v", missingResponse)
	}

	common := &config.Channel{
		Enabled: true, AllowFrom: config.FlexibleStringSlice{"one"},
		ReasoningChannelID: "reasoning",
		GroupTrigger:       config.GroupTriggerConfig{MentionOnly: true, Prefixes: []string{"!"}},
		Typing:             config.TypingConfig{Enabled: true},
		Placeholder: config.PlaceholderConfig{
			Enabled: true, Text: config.FlexibleStringSlice{"working"},
		},
	}
	settings := map[string]any{}
	addChannelCommonConfig(settings, common, config.ChannelTelegram)
	for _, key := range []string{
		"enabled", "allow_from", "reasoning_channel_id", "group_trigger", "typing", "placeholder",
	} {
		if _, found := settings[key]; !found {
			t.Fatalf("common channel omitted %q: %#v", key, settings)
		}
	}

	delta := &config.Channel{
		GroupTrigger: config.GroupTriggerConfig{Prefixes: []string{"/"}},
		Placeholder:  config.PlaceholderConfig{Text: config.FlexibleStringSlice{"wait"}},
	}
	deltaSettings := map[string]any{}
	addChannelCommonConfig(deltaSettings, delta, config.ChannelDeltaChat)
	prefixes := deltaSettings["group_trigger"].(map[string]any)["prefixes"].([]string)
	placeholder := deltaSettings["placeholder"].(map[string]any)["text"].([]string)
	if !reflect.DeepEqual(prefixes, []string{"/"}) || !reflect.DeepEqual(placeholder, []string{"wait"}) {
		t.Fatalf("delta channel settings = %#v", deltaSettings)
	}

	if streaming, ok := channelStreamingConfig(nil); ok || !streaming.IsZero() {
		t.Fatalf("nil streaming = %#v, %t", streaming, ok)
	}
	badChannel := &config.Channel{Type: config.ChannelTelegram, Settings: config.RawNode(`{`)}
	if streaming, ok := channelStreamingConfig(badChannel); ok || !streaming.IsZero() {
		t.Fatalf("invalid streaming = %#v, %t", streaming, ok)
	}
	streamChannel := &config.Channel{
		Type:     config.ChannelTelegram,
		Settings: config.RawNode(`{"streaming":{"enabled":true,"throttle_seconds":2}}`),
	}
	streaming, ok := channelStreamingConfig(streamChannel)
	if !ok || !streaming.Enabled || streaming.ThrottleSeconds != 2 {
		t.Fatalf("decoded streaming = %#v, %t", streaming, ok)
	}
	streamSettings := map[string]any{}
	addChannelCommonConfig(streamSettings, streamChannel, config.ChannelTelegram)
	if _, found := streamSettings["streaming"]; !found {
		t.Fatalf("streaming channel settings = %#v", streamSettings)
	}
	nonStruct := &config.Channel{Settings: config.RawNode(`1`)}
	var decodedNumber int
	if err := nonStruct.Decode(&decodedNumber); err != nil {
		t.Fatal(err)
	}
	if streaming, ok := channelStreamingConfig(nonStruct); ok || !streaming.IsZero() {
		t.Fatalf("non-struct streaming = %#v, %t", streaming, ok)
	}

	secrets := detectConfiguredSecrets(
		config.RawNode(`{"bot_token":"plain","app_token":{"s":"wrapped"}}`), "slack",
	)
	if !reflect.DeepEqual(secrets, []string{"bot_token", "app_token"}) ||
		detectConfiguredSecrets(config.RawNode(`{`), "slack") != nil ||
		detectConfiguredSecrets(config.RawNode(`{}`), "unknown") != nil ||
		len(detectConfiguredSecrets(config.RawNode(`{}`), "slack")) != 0 {
		t.Fatalf("configured secret detection = %#v", secrets)
	}

	if parseThreadContextQuery(" ") != nil || parseThreadContextQuery("broken") != nil {
		t.Fatal("empty thread context was accepted")
	}
	threadContext := parseThreadContextQuery(" Agent : main , ignored, Channel: pico, blank: ")
	if !reflect.DeepEqual(threadContext, map[string]string{"agent": "main", "channel": "pico"}) {
		t.Fatalf("thread context = %#v", threadContext)
	}
	for _, value := range []string{"1", "true", "YES", " on "} {
		if !parseThreadBoolQuery(value) {
			t.Fatalf("thread bool %q was false", value)
		}
	}
	if parseThreadBoolQuery("off") {
		t.Fatal("false thread bool was true")
	}

	if _, err := parseWorkflowTriggerScheduledAt(""); !errors.Is(err, errWorkflowTriggerSimulationRequest) {
		t.Fatalf("empty schedule error = %v", err)
	}
	if _, err := parseWorkflowTriggerScheduledAt(" padded "); !errors.Is(err, errWorkflowTriggerSimulationRequest) {
		t.Fatalf("padded schedule error = %v", err)
	}
	if _, err := parseWorkflowTriggerScheduledAt("not-time"); !errors.Is(err, errWorkflowTriggerSimulationRequest) {
		t.Fatalf("invalid schedule error = %v", err)
	}
	wantTime := time.Date(2026, 8, 29, 12, 0, 0, 123, time.UTC)
	parsed, err := parseWorkflowTriggerScheduledAt(wantTime.Format(time.RFC3339Nano))
	if err != nil || !parsed.Equal(wantTime) {
		t.Fatalf("parsed schedule = %s, %v", parsed, err)
	}
}
