//go:build unix

package weixin

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func lockWeixinStateFile(path string) (func(), error) {
	file, err := openWeixinStateLockFile(path)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock Weixin state store: %w", err)
	}
	return func() {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
		_ = file.Close()
	}, nil
}
