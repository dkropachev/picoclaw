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
	lockPath := root + ".lock"
	if info, err := os.Lstat(lockPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, errors.New("repository evaluation lock must be a regular file")
		}
		if info.Mode().Perm()&0o077 != 0 {
			return nil, errors.New("repository evaluation lock permissions are too broad")
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
	if err := repositoryEvaluationFlock(int(file.Fd()), unix.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock repository evaluation store: %w", err)
	}
	return func() {
		_ = repositoryEvaluationFlock(int(file.Fd()), unix.LOCK_UN)
		_ = file.Close()
	}, nil
}
