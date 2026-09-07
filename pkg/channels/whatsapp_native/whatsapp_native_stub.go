//go:build !whatsapp_native

package whatsapp

import (
	"fmt"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

// NewWhatsAppNativeChannel returns an error when the binary was not built with -tags whatsapp_native.
// Build with: go build -tags whatsapp_native ./cmd/...
func NewWhatsAppNativeChannel(
	bc *config.Channel,
	name string,
	cfg *config.WhatsAppSettings,
	bus *bus.MessageBus,
	storeID database.StoreID,
) (channels.Channel, error) {
	_ = bc
	_ = name
	_ = cfg
	_ = bus
	_ = storeID
	return nil, fmt.Errorf("whatsapp native not compiled in; build with -tags whatsapp_native")
}
