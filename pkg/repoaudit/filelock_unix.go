//go:build unix

package repoaudit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

var (
	repositoryReviewMkdirLockDir = os.MkdirAll
	repositoryReviewOpenLockFile = os.OpenFile
	repositoryReviewFlock        = unix.Flock
)

func lockRepositoryReviewStore(root string) (func(), error) {
	lockPath := root + ".lock"
	if info, err := os.Lstat(lockPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, errors.New("repository review lock must be a regular file")
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
	if err := repositoryReviewFlock(int(file.Fd()), unix.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock repository review store: %w", err)
	}
	return func() {
		_ = repositoryReviewFlock(int(file.Fd()), unix.LOCK_UN)
		_ = file.Close()
	}, nil
}
