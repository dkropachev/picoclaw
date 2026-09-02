//go:build unix

package repoeval

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

var (
	repositoryEvaluationMkdirLockDir = os.MkdirAll
	repositoryEvaluationOpenLockFile = os.OpenFile
	repositoryEvaluationFlock        = unix.Flock
)

func lockRepositoryEvaluationStore(root string) (func(), error) {
	lockPath, lockPathErr := repositoryEvaluationLockPath(root, "store.lock")
	if lockPathErr != nil {
		return nil, lockPathErr
	}
	if info, inspectErr := os.Lstat(lockPath); inspectErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, errors.New("repository evaluation lock must be a regular file")
		}
		if info.Mode().Perm()&0o077 != 0 {
			return nil, errors.New("repository evaluation lock permissions are too broad")
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
	if err := repositoryEvaluationFlock(int(file.Fd()), unix.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock repository evaluation store: %w", err)
	}
	return func() {
		_ = repositoryEvaluationFlock(int(file.Fd()), unix.LOCK_UN)
		_ = file.Close()
	}, nil
}
