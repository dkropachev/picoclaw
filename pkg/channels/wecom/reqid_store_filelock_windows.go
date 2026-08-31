//go:build windows

package wecom

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func lockWecomReqIDFile(path string) (func(), error) {
	file, err := openWecomReqIDLockFile(path)
	if err != nil {
		return nil, err
	}
	overlapped := new(windows.Overlapped)
	if err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK,
		0,
		1,
		0,
		overlapped,
	); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock WeCom request-route store: %w", err)
	}
	return func() {
		_ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlapped)
		_ = file.Close()
	}, nil
}
