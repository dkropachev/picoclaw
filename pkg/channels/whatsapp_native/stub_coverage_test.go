//go:build !whatsapp_native

package whatsapp

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/database"
)

func TestWhatsAppNativeStubBoundaries(t *testing.T) {
	if database.CodeOf(MigrateDatabase(t.Context(), "ignored")) != database.CodeUnsupported {
		t.Fatal("stub migration did not report Unsupported")
	}
	channel, err := NewWhatsAppNativeChannel(nil, "name", nil, nil, "channel.whatsapp.name")
	if err == nil || channel != nil {
		t.Fatalf("stub channel = %#v, %v", channel, err)
	}
}
