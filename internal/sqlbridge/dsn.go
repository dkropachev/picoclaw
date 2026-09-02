// Package sqlbridge contains the temporary private SQL compatibility boundary
// for Matrix and WhatsApp. It never accepts or exposes a physical database
// path, SQLite DSN, or provider handle.
package sqlbridge

import (
	"encoding/base64"
	"strings"

	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	dsnPrefix      = "pclawsql1_"
	dsnVersion     = byte(1)
	maximumDSNSize = 256
)

// Mode selects the bridge policy. Runtime permits only ordinary library data
// access. Offline additionally permits the bounded schema operations needed by
// the fenced database migrator.
type Mode string

const (
	ModeRuntime Mode = "runtime"
	ModeOffline Mode = "offline"
)

// Valid reports whether mode belongs to the closed bridge policy.
func (mode Mode) Valid() bool {
	return mode == ModeRuntime || mode == ModeOffline
}

// Target is the complete decoded bridge authority. StoreID remains a logical
// broker identity; no physical location can be represented here.
type Target struct {
	StoreID database.StoreID `json:"store_id"`
	Mode    Mode             `json:"mode"`
}

// EncodeDSN returns a deterministic opaque database/sql DSN containing only a
// bridge version, policy mode, and allow-listed logical store ID.
func EncodeDSN(storeID database.StoreID, mode Mode) (string, error) {
	target := Target{StoreID: storeID, Mode: mode}
	if err := validateTarget(target); err != nil {
		return "", err
	}
	payload := make([]byte, 2+len(storeID))
	payload[0] = dsnVersion
	switch mode {
	case ModeRuntime:
		payload[1] = 0
	case ModeOffline:
		payload[1] = 1
	}
	copy(payload[2:], string(storeID))
	return dsnPrefix + base64.RawURLEncoding.EncodeToString(payload), nil
}

// ParseDSN decodes and validates one canonical bridge DSN. Raw store IDs,
// paths, URIs, padded/alternate base64, and non-Matrix/WhatsApp identities are
// rejected.
func ParseDSN(encoded string) (Target, error) {
	if encoded == "" || len(encoded) > maximumDSNSize || encoded != strings.TrimSpace(encoded) ||
		!strings.HasPrefix(encoded, dsnPrefix) {
		return Target{}, invalidDSN()
	}
	rawEncoding := strings.TrimPrefix(encoded, dsnPrefix)
	if rawEncoding == "" || strings.Contains(rawEncoding, "=") {
		return Target{}, invalidDSN()
	}
	payload, err := base64.RawURLEncoding.DecodeString(rawEncoding)
	if err != nil || len(payload) < 3 || payload[0] != dsnVersion ||
		base64.RawURLEncoding.EncodeToString(payload) != rawEncoding {
		return Target{}, invalidDSN()
	}
	var mode Mode
	switch payload[1] {
	case 0:
		mode = ModeRuntime
	case 1:
		mode = ModeOffline
	default:
		return Target{}, invalidDSN()
	}
	storeID, err := database.ParseStoreID(string(payload[2:]))
	if err != nil {
		return Target{}, invalidDSN()
	}
	target := Target{StoreID: storeID, Mode: mode}
	if err := validateTarget(target); err != nil {
		return Target{}, err
	}
	return target, nil
}

func validateTarget(target Target) error {
	if !target.Mode.Valid() || !target.StoreID.Valid() || !bridgeStoreIDAllowed(target.StoreID) {
		return invalidDSN()
	}
	return nil
}

func bridgeStoreIDAllowed(storeID database.StoreID) bool {
	value := string(storeID)
	if strings.Contains(value, "/") {
		return false
	}
	for _, prefix := range []string{"channel.matrix.", "channel.whatsapp."} {
		if strings.HasPrefix(value, prefix) {
			suffix := strings.TrimPrefix(value, prefix)
			return suffix != "" && suffix[0] != '.' && suffix[len(suffix)-1] != '.' &&
				!strings.Contains(suffix, "..")
		}
	}
	return false
}

func invalidDSN() error {
	return database.NewError(database.CodeInvalid, "SQL bridge DSN is invalid")
}
