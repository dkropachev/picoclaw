//go:build !whatsapp_native

package sqliteadapter

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/database"
)

func TestDefaultBuildStubIsClosed(t *testing.T) {
	handler, err := NewBrokerHandler(t.TempDir(), nil)
	if err != nil || handler == nil {
		t.Fatalf("NewBrokerHandler() = %#v, %v", handler, err)
	}
	if _, err := handler.Handle(t.Context(), database.Request{}); database.CodeOf(err) != database.CodeUnsupported {
		t.Fatalf("Handle() error = %v", err)
	}
	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}
	if err := MigrateDatabase(t.Context(), "ignored"); database.CodeOf(err) != database.CodeUnsupported {
		t.Fatalf("MigrateDatabase() error = %v", err)
	}
}
