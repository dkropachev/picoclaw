//go:build (!unix && !windows) || aix

package fileidentity

import "os"

// Existing fails closed where stable physical filesystem identities are not
// implemented.
func Existing(string) (identity Identity, exists bool, err error) {
	return Identity{}, false, ErrUnsupported
}

func ExistingWithType(string) (identity Identity, objectType ObjectType, exists bool, err error) {
	return Identity{}, 0, false, ErrUnsupported
}

func Opened(*os.File) (identity Identity, objectType ObjectType, err error) {
	return Identity{}, 0, ErrUnsupported
}
