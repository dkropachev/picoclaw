//go:build !whatsapp_native

package whatsapp

import (
	"context"
	"fmt"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

// MigrateDatabase reports that this build cannot upgrade WhatsApp storage.
func MigrateDatabase(context.Context, string) error {
	return database.NewError(
		database.CodeUnsupported,
		"WhatsApp database migration requires a whatsapp_native build",
	)
}

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
