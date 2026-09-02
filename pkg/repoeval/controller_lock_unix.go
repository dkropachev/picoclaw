//go:build unix

package repoeval

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// LockController acquires the workspace-wide non-blocking evaluation
// controller lease. Store file locks protect individual CAS operations; this
// lease prevents two launcher processes from recovering the same durable run.
func (s Store) LockController() (func(), error) {
	if s.broker != nil {
		return s.brokerLockController()
	}
	if err := s.localProviderError(); err != nil {
		return nil, err
	}
	lockPath, lockPathErr := repositoryEvaluationLockPath(s.root, "controller.lock")
	if lockPathErr != nil {
		return nil, lockPathErr
	}
	if info, inspectErr := os.Lstat(lockPath); inspectErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return nil, errors.New("repository evaluation controller lock must be a private regular file")
		}
	} else if !os.IsNotExist(inspectErr) {
		return nil, inspectErr
	}
	if mkdirErr := repositoryEvaluationMkdirLockDir(filepath.Dir(lockPath), 0o700); mkdirErr != nil {
		return nil, mkdirErr
	}
	file, err := repositoryEvaluationOpenLockFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := secureRepositoryEvaluationLockFile(lockPath, file); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := repositoryEvaluationFlock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrControllerLocked
		}
		return nil, fmt.Errorf("lock repository evaluation controller: %w", err)
	}
	return func() {
		_ = repositoryEvaluationFlock(int(file.Fd()), unix.LOCK_UN)
		_ = file.Close()
	}, nil
}
