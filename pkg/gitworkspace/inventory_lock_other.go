//go:build !windows && !android && !darwin && !dragonfly && !freebsd && !ios && !linux && !netbsd && !openbsd && !solaris

package gitworkspace

import (
	"context"
	"errors"
)

func lockInventoryFile(_ context.Context, _ string) (func(), error) {
	return nil, errors.New("advisory inventory locking is unsupported on this platform")
}
