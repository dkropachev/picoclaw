package whatsapp

import (
	"context"

	"github.com/sipeed/picoclaw/internal/sqlbridge"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func init() {
	channels.RegisterFactory(
		config.ChannelWhatsAppNative,
		func(channelName, channelType string, cfg *config.Config, b *bus.MessageBus) (channels.Channel, error) {
			bc := cfg.Channels[channelName]
			decoded, err := bc.GetDecoded()
			if err != nil {
				return nil, err
			}
			c, ok := decoded.(*config.WhatsAppSettings)
			if !ok {
				return nil, channels.ErrSendFailed
			}
			if database.RuntimeClient() == nil {
				return nil, database.NewError(
					database.CodeUnavailable,
					"WhatsApp database broker client is unavailable",
				)
			}
			storeID, lookupErr := sqlbridge.ResolveChannelStore(
				context.Background(), database.RuntimeClient(), config.ChannelWhatsAppNative, channelName,
			)
			if lookupErr != nil {
				return nil, lookupErr
			}
			ch, err := NewWhatsAppNativeChannel(bc, channelName, c, b, storeID)
			if err != nil {
				return nil, err
			}
			return ch, nil
		},
	)
}
