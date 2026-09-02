//go:build unix

package database

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func acquirePlatformFileLock(path string, shared bool) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, NewError(CodeIntegrity, "database storage lock cannot be a symlink")
		}
		return nil, fmt.Errorf("open database storage lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open database storage lock returned no file")
	}
	if err := unix.Fchmod(fd, 0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("secure database storage lock: %w", err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect database storage lock: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Geteuid()) {
		_ = file.Close()
		return nil, NewError(CodeIntegrity, "database storage lock boundary is invalid")
	}
	operation := unix.LOCK_EX | unix.LOCK_NB
	if shared {
		operation = unix.LOCK_SH | unix.LOCK_NB
	}
	if err := unix.Flock(fd, operation); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, errFileLockBusy
		}
		return nil, fmt.Errorf("lock database storage root: %w", err)
	}
	return file, nil
}

func releasePlatformFileLock(file *os.File) error {
	if file == nil {
		return nil
	}
	unlockErr := unix.Flock(int(file.Fd()), unix.LOCK_UN)
	closeErr := file.Close()
	return errors.Join(unlockErr, closeErr)
}
