package database

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCatalogFingerprintValidation(t *testing.T) {
	value := catalogFingerprintPrefix + strings.Repeat("a", 63) + "g"
	if validCatalogFingerprint(value) {
		t.Fatalf("non-hex catalog fingerprint %q was accepted", value)
	}
	if !validCatalogFingerprint(catalogFingerprintPrefix + strings.Repeat("a", 64)) {
		t.Fatal("valid catalog fingerprint was rejected")
	}
}

func TestMigrationFenceRejectsInvalidHome(t *testing.T) {
	fence, err := AcquireMigrationFence("invalid\x00home")
	if fence != nil || CodeOf(err) != CodeInvalid {
		t.Fatalf("migration fence for invalid home = %#v, %v", fence, err)
	}
}

func TestFenceOwnershipStateTracksLiveFences(t *testing.T) {
	home := t.TempDir()
	online, err := AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	if !OnlineFenceHeld() || MigrationFenceHeld() {
		t.Fatal("online fence ownership state is incorrect")
	}
	if closeErr := online.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	migration, err := AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer migration.Close()
	if OnlineFenceHeld() || !MigrationFenceHeld() {
		t.Fatal("migration fence ownership state is incorrect")
	}
}

func TestRediscoveringClientPreservesFailureWhenManifestDisappears(t *testing.T) {
	server, err := StartServer(context.Background(), ServerOptions{Home: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	client, err := Connect(server.home)
	if err != nil {
		_ = server.Close(context.Background())
		t.Fatal(err)
	}
	if client.Epoch() != server.Manifest().Epoch {
		t.Fatal("connected client did not retain the manifest epoch")
	}
	if closeErr := server.Close(context.Background()); closeErr != nil {
		t.Fatal(closeErr)
	}
	var output EmptyPayload
	err = client.Call(t.Context(), "test-domain", 1, "read", EmptyPayload{}, &output)
	if databaseErr := (*Error)(nil); !errors.As(err, &databaseErr) || CodeOf(err) != CodeUnavailable {
		t.Fatalf("stale rediscovering client error = %v", err)
	}
}

func TestNilClientShutdownReturnsUnavailable(t *testing.T) {
	var client *Client
	if err := client.Shutdown(t.Context()); CodeOf(err) != CodeUnavailable {
		t.Fatalf("nil client shutdown error = %v", err)
	}
}
