//go:build unix

package wecom

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func lockWecomReqIDFile(path string) (func(), error) {
	file, err := openWecomReqIDLockFile(path)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock WeCom request-route store: %w", err)
	}
	return func() {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
		_ = file.Close()
	}, nil
}
