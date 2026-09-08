// Package fileidentity resolves stable identities for existing local files.
// It rejects unsafe filesystem object types and unsupported platforms so
// callers can fail closed when enforcing physical alias boundaries.
package fileidentity

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

var (
	// ErrInvalidPath means identity lookup received an unusable input path or handle.
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

// ObjectType is the identity-bound filesystem type returned by
// ExistingWithType and Opened.
type ObjectType uint8

const (
	ObjectTypeRegular ObjectType = iota + 1
	ObjectTypeDirectory
)

// String returns the platform-qualified identity. It is intended only for
// deriving opaque hashes; paths and other user-controlled data are absent.
func (identity Identity) String() string {
	return identity.key
}

// Valid reports whether Identity came from a successful Existing,
// ExistingWithType, or Opened call.
func (identity Identity) Valid() bool {
	return identity.key != ""
}

func newIdentity(key string) Identity {
	return Identity{key: key}
}

// windowsFileIdentity mirrors the stable fields in Windows FILE_ID_INFO. It
// lives in the common file so full-width identity and transition semantics can
// be exercised on non-Windows CI as well as cross-compiled there.
type windowsFileIdentity struct {
	volumeSerialNumber uint64
	fileID             [16]byte
}

func (identity windowsFileIdentity) opaqueIdentity() Identity {
	return newIdentity(fmt.Sprintf(
		"windows:%016x:%032x",
		identity.volumeSerialNumber,
		identity.fileID,
	))
}

func stableWindowsFileIdentity(first, second windowsFileIdentity) (Identity, error) {
	if first.fileID == ([16]byte{}) || second.fileID == ([16]byte{}) {
		return Identity{}, ErrUnsupported
	}
	if first != second {
		return Identity{}, fmt.Errorf("%w: object changed during inspection", ErrUnsafeType)
	}
	return first.opaqueIdentity(), nil
}

func validPath(path string) bool {
	return path != "" && utf8.ValidString(path) && !strings.ContainsRune(path, 0)
}
