package storecatalog

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
)

const (
	catalogFingerprintPrefix      = "sha256:"
	catalogFingerprintVersion     = "picoclaw/database-catalog-fingerprint/v1"
	maximumFingerprintDomainBytes = 64
)

var errCatalogFingerprintInput = errors.New("database catalog fingerprint input is invalid")

// Fingerprint returns an opaque, deterministic binding between this complete
// physical catalog and the exact configuration revision used to derive it. A
// fingerprint is an equality token, not authority to inspect or open a store.
func (c *Catalog) Fingerprint(configRevision string) (string, error) {
	if c == nil || !validFingerprintConfigRevision(configRevision) {
		return "", errCatalogFingerprintInput
	}
	canonicalHome, err := exactCatalogHome(c.Home)
	if err != nil || canonicalHome != c.Home {
		return "", errCatalogFingerprintInput
	}

	specs := c.All()
	if len(specs) == 0 {
		return "", errCatalogFingerprintInput
	}
	sort.Slice(specs, func(left, right int) bool {
		return specs[left].ID < specs[right].ID
	})
	for index := range specs {
		spec := specs[index]
		canonicalPath, pathErr := absoluteCatalogPath(spec.Path)
		if pathErr != nil || canonicalPath != spec.Path ||
			!validStoreID(spec.ID) || !validFingerprintDomain(spec.Domain) ||
			index > 0 && specs[index-1].ID == spec.ID {
			return "", errCatalogFingerprintInput
		}
		for _, legacyRoot := range spec.LegacyRoots {
			canonicalLegacy, legacyErr := absoluteCatalogPath(legacyRoot)
			if legacyErr != nil || canonicalLegacy != legacyRoot {
				return "", errCatalogFingerprintInput
			}
		}
	}
	if err := validateSpecsMode(specs, false); err != nil {
		return "", errCatalogFingerprintInput
	}

	digest := sha256.New()
	writeFingerprintString := func(value string) {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(value)))
		_, _ = digest.Write(size[:])
		_, _ = digest.Write([]byte(value))
	}
	writeFingerprintCount := func(value int) {
		var count [8]byte
		binary.BigEndian.PutUint64(count[:], uint64(value))
		_, _ = digest.Write(count[:])
	}

	writeFingerprintString(catalogFingerprintVersion)
	writeFingerprintString(c.Home)
	writeFingerprintString(configRevision)
	writeFingerprintCount(len(specs))
	for _, spec := range specs {
		writeFingerprintString(spec.ID)
		writeFingerprintString(spec.Domain)
		writeFingerprintString(spec.Path)
		required := byte(0)
		if spec.Required {
			required = 1
		}
		_, _ = digest.Write([]byte{required})
		writeFingerprintCount(len(spec.LegacyRoots))
		for _, legacyRoot := range spec.LegacyRoots {
			writeFingerprintString(legacyRoot)
		}
	}
	return catalogFingerprintPrefix + hex.EncodeToString(digest.Sum(nil)), nil
}

func validFingerprintConfigRevision(value string) bool {
	if value == "missing" {
		return true
	}
	if len(value) != len(catalogFingerprintPrefix)+sha256.Size*2 ||
		!strings.HasPrefix(value, catalogFingerprintPrefix) {
		return false
	}
	for _, character := range value[len(catalogFingerprintPrefix):] {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func validFingerprintDomain(value string) bool {
	if value == "" || len(value) > maximumFingerprintDomainBytes {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		alphaNumeric := character >= 'a' && character <= 'z' ||
			character >= '0' && character <= '9'
		if alphaNumeric {
			continue
		}
		if character != '-' || index == 0 || index == len(value)-1 {
			return false
		}
	}
	return true
}
