//go:build unix

package repoaudit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// LockAutomationController acquires a non-blocking workspace-wide controller
// lease. Only the holder may reconcile or execute automations in this store.
func (s Store) LockAutomationController() (func(), error) {
	if s.broker != nil {
		return s.brokerAcquireNamedLease(
			context.Background(),
			reviewLeaseAutomationController,
			reviewNamedLeaseRequest{},
		)
	}
	if err := s.localProviderError(); err != nil {
		return nil, err
	}
	lockPath, lockPathErr := repositoryReviewLockPath(s.root, "controller.lock")
	if lockPathErr != nil {
		return nil, lockPathErr
	}
	if info, inspectErr := os.Lstat(lockPath); inspectErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
			info.Mode().Perm()&0o077 != 0 {
			return nil, errors.New("repository review controller lock must be a private regular file")
		}
	} else if !os.IsNotExist(inspectErr) {
		return nil, inspectErr
	}
	if mkdirErr := repositoryReviewMkdirLockDir(filepath.Dir(lockPath), 0o700); mkdirErr != nil {
		return nil, mkdirErr
	}
	file, err := repositoryReviewOpenLockFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := secureRepositoryReviewLockFile(lockPath, file); err != nil {
		_ = file.Close()
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
