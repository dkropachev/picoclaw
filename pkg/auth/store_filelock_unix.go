//go:build unix

package auth

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func lockAuthStore(path string) (func(), error) {
	lockPath := path + ".lock"
	file, err := openAuthLockFile(lockPath)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock auth store: %w", err)
	}
	return func() {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
		_ = file.Close()
	}, nil
}
