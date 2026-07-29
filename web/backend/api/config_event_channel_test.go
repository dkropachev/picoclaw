package api

import (
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestValidateConfigChecksEnabledEventChannelAdapters(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Events.Ingress.Enabled = true
	cfg.Events.Ingress.Channels = map[string]config.ChannelEventIngressConfig{
		"missing-channel": {
			Enabled: true,
			Source:  config.EventChannelSourceEmail,
			Mode:    config.EventChannelModeMirror,
		},
	}

	errors := validateConfig(cfg)
	found := false
	for _, message := range errors {
		if strings.Contains(message, "events.ingress.channels") &&
			strings.Contains(message, "does not reference an existing channel") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("validateConfig() errors = %#v, want event channel reference error", errors)
	}

	channel := &config.Channel{
		Enabled: true,
		Type:    config.ChannelDeltaChat,
	}
	channel.SetName("missing-channel")
	if err := channel.Decode(&config.DeltaChatSettings{
		Email:    "events@example.org",
		Password: *config.NewSecureString("delta-api-secret"),
	}); err != nil {
		t.Fatalf("Decode(DeltaChatSettings) error = %v", err)
	}
	cfg.Channels["missing-channel"] = channel
	errors = validateConfig(cfg)
	for _, message := range errors {
		if strings.Contains(message, "events.ingress.channels") {
			t.Fatalf("validateConfig() valid event channel error = %q", message)
		}
	}
}

func TestValidateConfigRejectsUnsupportedEventChannelTypes(t *testing.T) {
	tests := []struct {
		name        string
		channelType string
	}{
		{name: "Pico", channelType: config.ChannelPico},
		{name: "Pico client", channelType: config.ChannelPicoClient},
		{name: "Telegram", channelType: config.ChannelTelegram},
		{name: "Discord", channelType: config.ChannelDiscord},
		{name: "Feishu", channelType: config.ChannelFeishu},
		{name: "Weixin", channelType: config.ChannelWeixin},
		{name: "WeCom", channelType: config.ChannelWeCom},
		{name: "DingTalk", channelType: config.ChannelDingTalk},
		{name: "Slack", channelType: config.ChannelSlack},
		{name: "Matrix", channelType: config.ChannelMatrix},
		{name: "LINE", channelType: config.ChannelLINE},
		{name: "OneBot", channelType: config.ChannelOneBot},
		{name: "QQ", channelType: config.ChannelQQ},
		{name: "IRC", channelType: config.ChannelIRC},
		{name: "VK", channelType: config.ChannelVK},
		{name: "MaixCam", channelType: config.ChannelMaixCam},
		{name: "WhatsApp", channelType: config.ChannelWhatsApp},
		{name: "WhatsApp native", channelType: config.ChannelWhatsAppNative},
		{name: "Teams webhook", channelType: config.ChannelTeamsWebHook},
		{name: "MQTT", channelType: config.ChannelMQTT},
		{name: "Slack webhook", channelType: config.ChannelSlackWebHook},
		{name: "unknown custom", channelType: "custom_transport"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Events.Ingress.Enabled = true
			cfg.Events.Ingress.Channels = map[string]config.ChannelEventIngressConfig{
				"unsupported": {Enabled: true},
			}
			channel := &config.Channel{
				Enabled: true,
				Type:    test.channelType,
			}
			channel.SetName("unsupported")
			cfg.Channels["unsupported"] = channel

			errors := validateConfig(cfg)
			for _, message := range errors {
				if strings.Contains(message, "events.ingress.channels") &&
					strings.Contains(message, "unsupported channel type") &&
					strings.Contains(message, test.channelType) {
					return
				}
			}
			t.Fatalf(
				"validateConfig() errors = %#v, want unsupported %s event channel",
				errors,
				test.channelType,
			)
		})
	}
}
