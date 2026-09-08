package database

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type oversizedExponentJSON struct{}

func (oversizedExponentJSON) MarshalJSON() ([]byte, error) {
	return []byte("1e2147483648"), nil
}

func TestCanonicalMarshalRejectsRealOversizedExponent(t *testing.T) {
	if _, err := MarshalCanonical(map[string]any{"number": oversizedExponentJSON{}}); err == nil ||
		!strings.Contains(err.Error(), "exponent") {
		t.Fatalf("oversized canonical exponent error = %v", err)
	}
}

func TestCatalogFingerprintRejectsNonHexDigest(t *testing.T) {
	value := catalogFingerprintPrefix + strings.Repeat("a", 63) + "g"
	if validCatalogFingerprint(value) {
		t.Fatalf("non-hex catalog fingerprint %q was accepted", value)
	}
}

func TestMigrationFenceRejectsInvalidHome(t *testing.T) {
	fence, err := AcquireMigrationFence("invalid\x00home")
	if fence != nil || CodeOf(err) != CodeInvalid {
		t.Fatalf("migration fence for invalid home = %#v, %v", fence, err)
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
	if err := server.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	var output EmptyPayload
	err = client.Call(t.Context(), "test-domain", 1, "read", EmptyPayload{}, &output)
	if databaseErr := (*Error)(nil); !errors.As(err, &databaseErr) || CodeOf(err) != CodeUnavailable {
		t.Fatalf("stale rediscovering client error = %v", err)
	}
}
