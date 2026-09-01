//go:build android || darwin || dragonfly || freebsd || ios || linux || netbsd || openbsd || solaris

package gitworkspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const inventoryFileLockRetryInterval = 50 * time.Millisecond

func lockInventoryFile(ctx context.Context, path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	fd, err := unix.Open(
		path,
		unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open git workspace inventory lock")
	}
	if secureErr := secureOpenedInventoryLock(file); secureErr != nil {
		_ = file.Close()
		return nil, secureErr
	}
	return lockOpenedInventoryFile(ctx, file, fd)
}

func lockInventoryFileInDirectory(
	ctx context.Context,
	directory,
	filename string,
	expected os.FileInfo,
) (func(), error) {
	if filename == "" || filepath.Base(filename) != filename || filename == "." || filename == ".." {
		return nil, errors.New("git workspace operation lock filename is invalid")
	}
	directoryFD, err := unix.Open(
		directory,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY,
		0,
	)
	if err != nil {
		return nil, err
	}
	directoryFile := os.NewFile(uintptr(directoryFD), directory)
	if directoryFile == nil {
		_ = unix.Close(directoryFD)
		return nil, errors.New("open git workspace operation lock root")
	}
	defer directoryFile.Close()
	openedIdentity, err := directoryFile.Stat()
	if err != nil || expected == nil || !managedDirectoryModePrivate(directory, openedIdentity) ||
		!os.SameFile(openedIdentity, expected) {
		return nil, errors.Join(
			errors.New("git workspace operation lock root changed while opening"),
			err,
		)
	}
	fd, err := unix.Openat(
		directoryFD,
		filename,
		unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), filepath.Join(directory, filename))
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open git workspace operation lock")
	}
	if secureErr := secureOpenedInventoryLock(file); secureErr != nil {
		_ = file.Close()
		return nil, secureErr
	}
	unlock, err := lockOpenedInventoryFile(ctx, file, fd)
	if err != nil {
		return nil, err
	}
	if validateErr := validatePrivateManagedDirectory(
		directory,
		expected,
		"git workspace operation lock root",
	); validateErr != nil {
		unlock()
		return nil, validateErr
	}
	return unlock, nil
}

func secureOpenedInventoryLock(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("git workspace inventory lock is not a regular file")
	}
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	return nil
}

func lockOpenedInventoryFile(ctx context.Context, file *os.File, fd int) (func(), error) {
	for {
		err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			var once sync.Once
			return func() {
				once.Do(func() {
					_ = unix.Flock(fd, unix.LOCK_UN)
					_ = file.Close()
				})
			}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = file.Close()
			return nil, fmt.Errorf("acquire advisory file lock: %w", err)
		}
		timer := time.NewTimer(inventoryFileLockRetryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
