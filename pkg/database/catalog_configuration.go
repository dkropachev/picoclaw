package database

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
)

const catalogFingerprintPrefix = "sha256:"

// LoadCatalogConfiguration atomically loads the validated configuration and
// returns the opaque fingerprint that must identify the broker catalog built
// from that snapshot. The fingerprint never exposes configuration contents or
// physical store locations.
func LoadCatalogConfiguration(configFile string) (*config.Config, string, error) {
	configuration, revision, err := config.LoadConfigSnapshot(configFile)
	if err != nil {
		return nil, "", fmt.Errorf("load database catalog configuration: %w", err)
	}
	encoded, err := json.Marshal(configuration)
	if err != nil {
		return nil, "", NewError(CodeInternal, "database catalog fingerprint failed")
	}
	absoluteConfigFile, err := filepath.Abs(configFile)
	if err != nil {
		return nil, "", NewError(CodeInvalid, "database catalog configuration is invalid")
	}
	digest := sha256.New()
	writeCatalogFingerprintPart(digest, "database-catalog-v1")
	writeCatalogFingerprintPart(digest, filepath.Clean(absoluteConfigFile))
	writeCatalogFingerprintPart(digest, revision)
	writeCatalogFingerprintPart(digest, string(encoded))
	return configuration, catalogFingerprintPrefix + hex.EncodeToString(digest.Sum(nil)), nil
}

func writeCatalogFingerprintPart(digest interface {
	Write(data []byte) (int, error)
}, value string,
) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = digest.Write(size[:])
	_, _ = digest.Write([]byte(value))
}

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
