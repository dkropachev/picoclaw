//go:build unix && !aix

package databaseclaims

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/sipeed/picoclaw/pkg/database"
	"golang.org/x/sys/unix"
)

type unixClaimHandle struct {
	file *os.File
}

func acquireClaim(root string, identity string) (claimHandle, error) {
	if !validClaimIdentity(identity) {
		return nil, database.NewError(database.CodeIntegrity, "physical database claim identity is invalid")
	}
	path := filepath.Join(root, identity+".lock")
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, database.NewError(database.CodeIntegrity, "physical database claim lock cannot be a symlink")
		}
		return nil, fmt.Errorf("open physical database claim lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open physical database claim lock returned no file")
	}
	valid := false
	defer func() {
		if !valid {
			_ = file.Close()
		}
	}()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, fmt.Errorf("inspect physical database claim lock: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o777 != 0o600 {
		return nil, database.NewError(database.CodeIntegrity, "physical database claim lock boundary is invalid")
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, errClaimBusy
		}
		return nil, fmt.Errorf("lock physical database claim: %w", err)
	}
	opened, statErr := file.Stat()
	current, lstatErr := os.Lstat(path)
	if statErr != nil || lstatErr != nil || opened == nil || current == nil ||
		!os.SameFile(opened, current) || current.Mode()&os.ModeSymlink != 0 {
		_ = unix.Flock(fd, unix.LOCK_UN)
		return nil, errors.Join(
			database.NewError(database.CodeIntegrity, "physical database claim lock changed"),
			statErr,
			lstatErr,
		)
	}
	valid = true
	return &unixClaimHandle{file: file}, nil
}

func (claim *unixClaimHandle) close() error {
	if claim == nil || claim.file == nil {
		return nil
	}
	file := claim.file
	claim.file = nil
	return errors.Join(unix.Flock(int(file.Fd()), unix.LOCK_UN), file.Close())
}

func (claim *unixClaimHandle) valid() bool {
	if claim == nil || claim.file == nil {
		return false
	}
	opened, statErr := claim.file.Stat()
	current, lstatErr := os.Lstat(claim.file.Name())
	if statErr != nil || lstatErr != nil || opened == nil || current == nil ||
		!opened.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(opened, current) || current.Mode().Perm() != 0o600 {
		return false
	}
	stat, ok := opened.Sys().(*syscall.Stat_t)
	return ok && stat != nil && stat.Uid == uint32(os.Geteuid()) && stat.Nlink == 1
}
