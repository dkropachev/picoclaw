//go:build !android && !darwin && !dragonfly && !freebsd && !ios && !linux && !netbsd && !openbsd

package gitworkspace

import "errors"

const pinnedValidationSpecialNodeSupported = false

func createPinnedValidationSpecialNode(string) error {
	return errors.New("special nodes are unavailable")
}
