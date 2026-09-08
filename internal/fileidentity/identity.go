// Package fileidentity resolves stable identities for existing local files.
// It rejects unsafe filesystem object types and unsupported platforms so
// callers can fail closed when enforcing physical alias boundaries.
package fileidentity

import (
	"errors"
	"strings"
	"unicode/utf8"
)

var (
	// ErrInvalidPath means identity lookup received an unusable path string.
	ErrInvalidPath = errors.New("physical file identity path is invalid")
	// ErrUnsafeType means an existing object is not a regular file or directory.
	ErrUnsafeType = errors.New("physical file identity object has an unsafe type")
	// ErrUnsupported means this platform cannot provide a stable identity.
	ErrUnsupported = errors.New("physical file identity is unsupported on this platform")
)

// Identity is an opaque, comparable physical filesystem identity.
type Identity struct {
	key string
}

// String returns the platform-qualified identity. It is intended only for
// deriving opaque hashes; paths and other user-controlled data are absent.
func (identity Identity) String() string {
	return identity.key
}

// Valid reports whether Identity came from a successful Existing call.
func (identity Identity) Valid() bool {
	return identity.key != ""
}

func newIdentity(key string) Identity {
	return Identity{key: key}
}

func validPath(path string) bool {
	return path != "" && utf8.ValidString(path) && !strings.ContainsRune(path, 0)
}
