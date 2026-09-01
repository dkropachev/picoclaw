//go:build !windows && !android && !darwin && !dragonfly && !freebsd && !ios && !linux && !netbsd && !openbsd && !solaris

package gitworkspace

import (
	"context"
	"errors"
	"os"
)

func lockInventoryFile(_ context.Context, _ string) (func(), error) {
	return nil, errors.New("advisory inventory locking is unsupported on this platform")
}

func lockInventoryFileInDirectory(
	_ context.Context,
	_, _ string,
	_ os.FileInfo,
) (func(), error) {
	return nil, errors.New("advisory inventory locking is unsupported on this platform")
}
