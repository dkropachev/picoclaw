//go:build (!unix && !windows) || aix

package fileidentity

// Existing fails closed where stable physical filesystem identities are not
// implemented.
func Existing(string) (identity Identity, exists bool, err error) {
	return Identity{}, false, ErrUnsupported
}
