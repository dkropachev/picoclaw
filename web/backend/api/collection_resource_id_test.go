package api

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestCollectionResourceIDIsDeterministicFixedLengthAndURLSafe(t *testing.T) {
	t.Parallel()

	identities := []string{
		"owner/repository",
		"workflow/ref@v2",
		"組織/répertoire with spaces",
		strings.Repeat("w", collectionResourceIDIdentityMaxBytes),
	}
	for index, identity := range identities {
		identity := identity
		name := identity
		if len(name) > 80 {
			name = "maximum identity"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			first, err := encodeCollectionResourceID("workflow-definition", identity)
			if err != nil {
				t.Fatalf("encode collection resource ID: %v", err)
			}
			second, err := encodeCollectionResourceID("workflow-definition", identity)
			if err != nil {
				t.Fatalf("encode collection resource ID again: %v", err)
			}
			if first != second {
				t.Fatalf("collection resource ID is not deterministic: %q != %q", first, second)
			}
			if len(first) != collectionResourceIDEncodedBytes {
				t.Fatalf(
					"collection resource ID length = %d, want %d for input %d",
					len(first),
					collectionResourceIDEncodedBytes,
					index,
				)
			}
			if strings.ContainsAny(first, "+/=") {
				t.Fatalf("collection resource ID is not unpadded base64url: %q", first)
			}
			for _, character := range first {
				if character >= 'a' && character <= 'z' ||
					character >= 'A' && character <= 'Z' ||
					character >= '0' && character <= '9' ||
					character == '-' || character == '_' {
					continue
				}
				t.Fatalf("collection resource ID contains unsafe character %q: %q", character, first)
			}
			if !validCollectionResourceID(first) {
				t.Fatalf("emitted collection resource ID is not valid: %q", first)
			}
			if !collectionResourceIDMatches("workflow-definition", first, identity) {
				t.Fatal("collection resource ID did not match its canonical identity")
			}
		})
	}
}

func TestCollectionResourceIDBindsNamespaceAndCandidateIdentity(t *testing.T) {
	t.Parallel()

	encoded, err := encodeCollectionResourceID("repository-assignment", "owner/repository")
	if err != nil {
		t.Fatalf("encode collection resource ID: %v", err)
	}
	if !collectionResourceIDMatches(
		"repository-assignment",
		encoded,
		"owner/repository",
	) {
		t.Fatal("collection resource ID did not match its namespace and identity")
	}
	if collectionResourceIDMatches("workflow-definition", encoded, "owner/repository") {
		t.Fatal("collection resource ID matched a different namespace")
	}
	if collectionResourceIDMatches("repository-assignment", encoded, "owner/other") {
		t.Fatal("collection resource ID matched a different identity")
	}
	if collectionResourceIDMatches("Repository", encoded, "owner/repository") {
		t.Fatal("collection resource ID matched an invalid namespace")
	}
}

func TestCollectionResourceIDIdentityBoundaryIsUTF8ByteBased(t *testing.T) {
	t.Parallel()

	maximumASCII := strings.Repeat("x", collectionResourceIDIdentityMaxBytes)
	maximumASCIIID, err := encodeCollectionResourceID("workflow", maximumASCII)
	if err != nil {
		t.Fatalf("encode ASCII identity at 16 KiB boundary: %v", err)
	}
	if !collectionResourceIDMatches("workflow", maximumASCIIID, maximumASCII) {
		t.Fatal("ASCII identity at 16 KiB boundary did not match")
	}

	maximumUTF8 := strings.Repeat("é", collectionResourceIDIdentityMaxBytes/2)
	maximumUTF8ID, err := encodeCollectionResourceID("workflow", maximumUTF8)
	if err != nil {
		t.Fatalf("encode UTF-8 identity at 16 KiB boundary: %v", err)
	}
	if !collectionResourceIDMatches("workflow", maximumUTF8ID, maximumUTF8) {
		t.Fatal("UTF-8 identity at 16 KiB boundary did not match")
	}

	oversize := []string{
		strings.Repeat("x", collectionResourceIDIdentityMaxBytes+1),
		strings.Repeat("é", collectionResourceIDIdentityMaxBytes/2+1),
	}
	for _, identity := range oversize {
		if _, err = encodeCollectionResourceID("workflow", identity); !errors.Is(err, errInvalidCollectionResourceID) {
			t.Fatalf("oversize identity error = %v, want %v", err, errInvalidCollectionResourceID)
		}
		if collectionResourceIDMatches("workflow", maximumASCIIID, identity) {
			t.Fatal("collection resource ID matched an oversize identity")
		}
	}
}

func TestCollectionResourceIDRejectsInvalidSourceInputs(t *testing.T) {
	t.Parallel()

	invalidUTF8 := string([]byte{0xff})
	tests := []struct {
		name      string
		namespace string
		identity  string
	}{
		{name: "empty namespace", namespace: "", identity: "identity"},
		{name: "uppercase namespace", namespace: "Workflow", identity: "identity"},
		{name: "numeric namespace prefix", namespace: "1workflow", identity: "identity"},
		{name: "namespace separator", namespace: "workflow_definition", identity: "identity"},
		{name: "invalid UTF-8 namespace", namespace: invalidUTF8, identity: "identity"},
		{
			name: "oversize namespace",
			namespace: "n" +
				strings.Repeat("a", collectionResourceIDNamespaceMaxBytes),
			identity: "identity",
		},
		{name: "empty identity", namespace: "workflow", identity: ""},
		{name: "NUL identity", namespace: "workflow", identity: "identity\x00suffix"},
		{name: "invalid UTF-8 identity", namespace: "workflow", identity: invalidUTF8},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if _, err := encodeCollectionResourceID(test.namespace, test.identity); !errors.Is(err, errInvalidCollectionResourceID) {
				t.Fatalf("encode error = %v, want %v", err, errInvalidCollectionResourceID)
			}
		})
	}
}

func TestCollectionResourceIDStrictWireValidation(t *testing.T) {
	t.Parallel()

	canonical, err := encodeCollectionResourceID("workflow", "identity")
	if err != nil {
		t.Fatalf("encode collection resource ID: %v", err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(canonical)
	if err != nil {
		t.Fatalf("decode canonical test fixture: %v", err)
	}
	// SHA-256 encodes to 43 base64url characters, leaving two unused bits.
	// Altering only those bits yields the same decoded bytes in a permissive
	// decoder but must fail strict canonical validation.
	alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	lastIndex := strings.IndexByte(alphabet, canonical[len(canonical)-1])
	noncanonicalTrailingBits := canonical[:len(canonical)-1] +
		string(alphabet[lastIndex|1])
	noncanonicalRaw, decodeErr := base64.RawURLEncoding.DecodeString(noncanonicalTrailingBits)
	if decodeErr != nil || string(noncanonicalRaw) != string(raw) {
		t.Fatalf("noncanonical test fixture does not decode to canonical bytes: %v", decodeErr)
	}

	tests := []struct {
		name    string
		encoded string
	}{
		{name: "empty", encoded: ""},
		{name: "too short", encoded: canonical[:len(canonical)-1]},
		{name: "too long", encoded: canonical + "A"},
		{name: "invalid alphabet", encoded: strings.Repeat("%", collectionResourceIDEncodedBytes)},
		{name: "padded", encoded: canonical[:len(canonical)-1] + "="},
		{name: "noncanonical trailing bits", encoded: noncanonicalTrailingBits},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if validCollectionResourceID(test.encoded) {
				t.Fatalf("validCollectionResourceID(%q) = true", test.encoded)
			}
			if collectionResourceIDMatches("workflow", test.encoded, "identity") {
				t.Fatalf("malformed collection resource ID %q matched", test.encoded)
			}
		})
	}
}
