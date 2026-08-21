//go:build unix

package repoaudit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// LockAutomationController acquires a non-blocking workspace-wide controller
// lease. Only the holder may reconcile or execute automations in this store.
func (s Store) LockAutomationController() (func(), error) {
	lockPath := s.root + ".controller.lock"
	if info, err := os.Lstat(lockPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, errors.New("repository review controller lock must be a regular file")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := repositoryReviewMkdirLockDir(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, err
	}
	file, err := repositoryReviewOpenLockFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := repositoryReviewFlock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrAutomationControllerLocked
		}
		return nil, fmt.Errorf("lock repository review controller: %w", err)
	}
	return func() {
		_ = repositoryReviewFlock(int(file.Fd()), unix.LOCK_UN)
		_ = file.Close()
	}, nil
}
