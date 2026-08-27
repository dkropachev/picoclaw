package api

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	collectionResourceIDNamespaceMaxBytes = 64
	// collectionResourceIDIdentityMaxBytes accommodates the largest persisted
	// canonical identity currently projected by administrative collections.
	collectionResourceIDIdentityMaxBytes = 16 << 10
	collectionResourceIDEncodedBytes     = (sha256.Size*8 + 5) / 6
)

var (
	errInvalidCollectionResourceID = errors.New("invalid collection resource id")
	collectionResourceIDEncoding   = base64.RawURLEncoding.Strict()
)

// encodeCollectionResourceID returns a deterministic, fixed-length, URL-safe
// identifier for a canonical resource identity. Callers remain responsible for
// applying resource-specific canonicalization before encoding the identity.
func encodeCollectionResourceID(namespace, identity string) (string, error) {
	if !validCollectionResourceIDNamespace(namespace) ||
		!validCollectionResourceIdentity(identity) {
		return "", errInvalidCollectionResourceID
	}

	digest := sha256.New()
	_, _ = digest.Write([]byte(namespace))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write([]byte(identity))
	return collectionResourceIDEncoding.EncodeToString(digest.Sum(nil)), nil
}

// validCollectionResourceID reports whether encoded has the exact canonical
// wire shape emitted by encodeCollectionResourceID. It does not resolve the
// one-way digest to an identity.
func validCollectionResourceID(encoded string) bool {
	if len(encoded) != collectionResourceIDEncodedBytes {
		return false
	}
	raw, err := collectionResourceIDEncoding.DecodeString(encoded)
	return err == nil && len(raw) == sha256.Size &&
		collectionResourceIDEncoding.EncodeToString(raw) == encoded
}

// collectionResourceIDMatches validates encoded and compares it with the ID
// derived from one canonical candidate identity. Collection handlers can use
// this predicate while scanning their already-bounded resource projections.
func collectionResourceIDMatches(namespace, encoded, candidateIdentity string) bool {
	if !validCollectionResourceID(encoded) {
		return false
	}
	candidateID, err := encodeCollectionResourceID(namespace, candidateIdentity)
	return err == nil && candidateID == encoded
}

func validCollectionResourceIDNamespace(namespace string) bool {
	if namespace == "" || len(namespace) > collectionResourceIDNamespaceMaxBytes ||
		!utf8.ValidString(namespace) || namespace[0] < 'a' || namespace[0] > 'z' {
		return false
	}
	for index := 1; index < len(namespace); index++ {
		character := namespace[index]
		if character >= 'a' && character <= 'z' ||
			character >= '0' && character <= '9' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func validCollectionResourceIdentity(identity string) bool {
	return identity != "" && len(identity) <= collectionResourceIDIdentityMaxBytes &&
		utf8.ValidString(identity) && !strings.ContainsRune(identity, 0)
}
