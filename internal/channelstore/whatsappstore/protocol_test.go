package whatsappstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/database"
)

func TestDecryptionLeaseIDValidation(t *testing.T) {
	valid := DecryptionLeaseID(strings.Repeat("0a", 32))
	if !valid.Valid() {
		t.Fatal("lowercase 32-byte lease token was rejected")
	}
	for _, value := range []DecryptionLeaseID{
		"", "abc", DecryptionLeaseID(strings.Repeat("0", 63)),
		DecryptionLeaseID(strings.Repeat("0", 65)),
		DecryptionLeaseID(strings.Repeat("A", 64)),
		DecryptionLeaseID(strings.Repeat("g", 64)),
	} {
		if value.Valid() {
			t.Fatalf("invalid lease token %q was accepted", value)
		}
	}
}

func TestResolveStoreUsesTypedWhatsAppDomain(t *testing.T) {
	home := t.TempDir()
	handler := database.HandlerFunc(func(_ context.Context, request database.Request) (any, error) {
		if request.Domain != Domain || request.Version != Version || request.Operation != ResolveOperation {
			t.Fatalf("unexpected request: %#v", request)
		}
		var input ResolveRequest
		if err := request.DecodePayload(&input); err != nil {
			t.Fatal(err)
		}
		switch input.ChannelName {
		case "primary":
			return ResolveResponse{StoreID: "channel/whatsapp/primary-12345678"}, nil
		case "foreign":
			return ResolveResponse{StoreID: "channel/matrix/primary-12345678"}, nil
		case "empty":
			return ResolveResponse{}, nil
		default:
			return nil, database.NewError(database.CodeUnauthorized, "not cataloged")
		}
	})
	server, err := database.StartServer(context.Background(), database.ServerOptions{
		Home: home, Handler: handler,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if closeErr := server.Close(ctx); closeErr != nil {
			t.Error(closeErr)
		}
	})
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	storeID, err := ResolveStore(t.Context(), client, "primary")
	if err != nil || storeID != "channel/whatsapp/primary-12345678" {
		t.Fatalf("resolved store = %q, %v", storeID, err)
	}
	for _, name := range []string{"foreign", "empty"} {
		if _, err := ResolveStore(t.Context(), client, name); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("ResolveStore(%q) error = %v", name, err)
		}
	}
	if _, err := ResolveStore(t.Context(), client, "missing"); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("missing store error = %v", err)
	}
}

func TestResolveStoreRejectsInvalidClientAndChannel(t *testing.T) {
	if _, err := ResolveStore(t.Context(), nil, "primary"); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("nil client error = %v", err)
	}
	for _, name := range []string{"", " primary", "primary ", "bad\x00name"} {
		if _, err := ResolveStore(t.Context(), &database.Client{}, name); database.CodeOf(err) != database.CodeInvalid {
			t.Fatalf("ResolveStore(%q) error = %v", name, err)
		}
	}
}
