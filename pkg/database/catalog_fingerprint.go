package database

import (
	"crypto/sha256"
	"strings"
)

const catalogFingerprintPrefix = "sha256:"

func validCatalogFingerprint(value string) bool {
	encoded := strings.TrimPrefix(value, catalogFingerprintPrefix)
	if len(encoded) != sha256.Size*2 || encoded == value {
		return false
	}
	for _, character := range encoded {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}
