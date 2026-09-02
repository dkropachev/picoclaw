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
	return lockRepositoryReviewStoreMode(root, false)
}

func tryLockRepositoryReviewStore(root string) (func(), error) {
	return lockRepositoryReviewStoreMode(root, true)
}

func lockRepositoryReviewStoreMode(root string, nonblocking bool) (func(), error) {
	if err := reviewProviderAuthorityError(); err != nil {
		return nil, err
	}
	lockPath, lockPathErr := repositoryReviewLockPath(root, "store.lock")
	if lockPathErr != nil {
		return nil, lockPathErr
	}
	if info, inspectErr := os.Lstat(lockPath); inspectErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
			info.Mode().Perm()&0o077 != 0 {
			return nil, errors.New("repository review lock must be a private regular file")
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
	operation := unix.LOCK_EX
	if nonblocking {
		operation |= unix.LOCK_NB
	}
	if err := repositoryReviewFlock(int(file.Fd()), operation); err != nil {
		_ = file.Close()
		if nonblocking && (errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN)) {
			return nil, ErrConflict
		}
		return nil, fmt.Errorf("lock repository review store: %w", err)
	}
	return func() {
		_ = repositoryReviewFlock(int(file.Fd()), unix.LOCK_UN)
		_ = file.Close()
	}, nil
}
