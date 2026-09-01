//go:build unix

package accountrouter

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func lockAccountRouterFile(path string) (func(), error) {
	file, err := openAccountRouterLockFile(path)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock account-router store: %w", err)
	}
	return func() {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
		_ = file.Close()
	}, nil
}
