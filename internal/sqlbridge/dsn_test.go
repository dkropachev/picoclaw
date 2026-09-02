package sqlbridge

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/database"
)

func TestDSNRoundTripIsOpaqueAndDomainBound(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		id   string
		mode Mode
	}{
		{id: "channel.matrix.primary-a1b2c3d4", mode: ModeRuntime},
		{id: "channel.matrix.team.eu-a1b2c3d4", mode: ModeOffline},
		{id: "channel.whatsapp.default-01234567", mode: ModeRuntime},
		{id: "channel.whatsapp.support-89abcdef", mode: ModeOffline},
	} {
		t.Run(test.id+"/"+string(test.mode), func(t *testing.T) {
			t.Parallel()
			storeID := mustStoreID(t, test.id)
			encoded, err := EncodeDSN(storeID, test.mode)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(encoded, test.id) || strings.ContainsAny(encoded, "/:\\?&=") {
				t.Fatalf("DSN exposes non-opaque authority: %q", encoded)
			}
			again, err := EncodeDSN(storeID, test.mode)
			if err != nil || again != encoded {
				t.Fatalf("EncodeDSN() = %q, %v; want deterministic %q", again, err, encoded)
			}
			decoded, err := ParseDSN(encoded)
			if err != nil {
				t.Fatal(err)
			}
			if decoded.StoreID != storeID || decoded.Mode != test.mode {
				t.Fatalf("ParseDSN() = %#v, want ID %q mode %q", decoded, storeID, test.mode)
			}
		})
	}
}

func TestDSNRejectsPathsURIsUnknownStoresAndNoncanonicalEncoding(t *testing.T) {
	t.Parallel()

	invalidIDs := []database.StoreID{
		"",
		"global.auth",
		"channel.wecom.default",
		"channel.matrix",
		"channel.matrix.",
		"channel.matrix..primary",
		"channel.matrix.primary/child",
		"channel.matrixx.primary",
		"channel.whatsapp",
		"channel.whatsapp.",
		"channel.whatsapp..primary",
		"/tmp/matrix.db",
		"file:matrix.db",
		`C:\matrix\store.db`,
	}
	for _, id := range invalidIDs {
		if encoded, err := EncodeDSN(id, ModeRuntime); err == nil {
			t.Errorf("EncodeDSN(%q) = %q, want error", id, encoded)
		}
	}
	valid := mustStoreID(t, "channel.matrix.primary-a1b2c3d4")
	for _, mode := range []Mode{"", "Runtime", "offline ", "migration"} {
		if encoded, err := EncodeDSN(valid, mode); err == nil {
			t.Errorf("EncodeDSN(mode %q) = %q, want error", mode, encoded)
		}
	}

	invalidPayloads := [][]byte{
		{2, 0, 'c'},
		{1, 2, 'c'},
		append([]byte{1, 0}, []byte("global.auth")...),
		append([]byte{1, 0}, []byte("channel.matrix.")...),
		append([]byte{1, 0}, []byte("channel.matrix.primary/child")...),
	}
	invalidDSNs := make([]string, 0, 11+len(invalidPayloads)+2)
	invalidDSNs = append(invalidDSNs,
		"",
		" ",
		"channel.matrix.primary-a1b2c3d4",
		"/tmp/store.db",
		"file:/tmp/store.db",
		"sqlite://store.db",
		`C:\matrix\store.db`,
		"pclawsql1_",
		"pclawsql1_====",
		" pclawsql1_AQBh ",
		strings.Repeat("x", maximumDSNSize+1),
	)
	for _, payload := range invalidPayloads {
		invalidDSNs = append(invalidDSNs, dsnPrefix+base64.RawURLEncoding.EncodeToString(payload))
	}
	canonical, err := EncodeDSN(valid, ModeRuntime)
	if err != nil {
		t.Fatal(err)
	}
	invalidDSNs = append(invalidDSNs, canonical+"=", canonical+"\n")
	for _, encoded := range invalidDSNs {
		if decoded, err := ParseDSN(encoded); err == nil {
			t.Errorf("ParseDSN(%q) = %#v, want error", encoded, decoded)
		}
	}
}

func mustStoreID(t *testing.T, value string) database.StoreID {
	t.Helper()
	id, err := database.ParseStoreID(value)
	if err != nil {
		t.Fatalf("ParseStoreID(%q): %v", value, err)
	}
	return id
}
