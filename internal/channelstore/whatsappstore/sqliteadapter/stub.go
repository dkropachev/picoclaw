//go:build !whatsapp_native

package sqliteadapter

import (
	"context"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

// BrokerHandler keeps default builds source-compatible with unconditional
// broker wiring. Native WhatsApp operations remain unavailable without the tag.
type BrokerHandler struct{}

func NewBrokerHandler(string, *config.Config) (*BrokerHandler, error) {
	return &BrokerHandler{}, nil
}

func (*BrokerHandler) Handle(context.Context, database.Request) (any, error) {
	return nil, database.NewError(
		database.CodeUnsupported,
		"WhatsApp database support requires a whatsapp_native build",
	)
}

func (*BrokerHandler) Close() error { return nil }

func MigrateDatabase(context.Context, string) error {
	return database.NewError(
		database.CodeUnsupported,
		"WhatsApp database migration requires a whatsapp_native build",
	)
}
