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
	lockPath := s.root + ".controller.lock"
	if info, err := os.Lstat(lockPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return nil, errors.New("repository evaluation controller lock must be a private regular file")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := repositoryEvaluationMkdirLockDir(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, err
	}
	file, err := repositoryEvaluationOpenLockFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
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
