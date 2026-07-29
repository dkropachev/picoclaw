//go:build windows

package workflows

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/sipeed/picoclaw/pkg/fileutil"
	"golang.org/x/sys/windows"
)

func lockWorkflowMutationFile(path string) (func(), error) {
	if err := fileutil.MkdirAllDurable(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	var overlapped windows.Overlapped
	if err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK,
		0,
		1,
		0,
		&overlapped,
	); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock workflow mutations: %w", err)
	}
	return func() {
		_ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped)
		_ = file.Close()
	}, nil
}
