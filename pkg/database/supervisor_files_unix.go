//go:build unix

package database

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func createOwnerOnlyExclusiveFile(path string, mode os.FileMode) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func createOwnerOnlyExclusiveAppendFile(path string, mode os.FileMode) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, mode)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func openOwnerOnlyAppendFile(path string, mode os.FileMode) (*os.File, error) {
	return openOwnerOnlyAppendFileWith(path, mode, supervisorUnixAppendOpenOps{
		open:    unix.Open,
		newFile: os.NewFile,
	})
}

type supervisorUnixAppendOpenOps struct {
	open    func(string, int, uint32) (int, error)
	newFile func(uintptr, string) *os.File
}

func openOwnerOnlyAppendFileWith(
	path string,
	mode os.FileMode,
	ops supervisorUnixAppendOpenOps,
) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || validateOwnerOnlyFile(path, info, mode) != nil {
		return nil, NewError(CodeIntegrity, "database broker log boundary is invalid")
	}
	fd, err := ops.open(path, unix.O_WRONLY|unix.O_APPEND|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, NewError(CodeIntegrity, "database broker log boundary is invalid")
		}
		return nil, err
	}
	file := ops.newFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, NewError(CodeIntegrity, "database broker log handle is unavailable")
	}
	if err := validateSupervisorOpenedFile(path, file, mode); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func supervisorFileLinkCount(file *os.File) (int64, error) {
	return supervisorFileLinkCountWith(file, file.Stat)
}

func supervisorFileLinkCountWith(
	file *os.File,
	statFile func() (os.FileInfo, error),
) (int64, error) {
	if file == nil {
		return 0, NewError(CodeIntegrity, "database supervisor file handle is unavailable")
	}
	info, err := statFile()
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return 0, NewError(CodeIntegrity, "database supervisor file link count is unavailable")
	}
	return int64(stat.Nlink), nil
}

func supervisorExecutableModeValid(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 &&
		info.Mode().Perm()&0o022 == 0
}

func validateSupervisorExecutableTrust(_ string, info os.FileInfo) error {
	if !supervisorExecutableModeValid(info) || !supervisorTrustedUnixOwner(info) {
		return NewError(CodeIntegrity, "database supervisor executable trust is invalid")
	}
	return nil
}

func validateSupervisorExecutableAncestorTrust(_ string, info os.FileInfo) error {
	if info == nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() ||
		!supervisorTrustedUnixOwner(info) {
		return NewError(CodeIntegrity, "database supervisor executable ancestor trust is invalid")
	}
	if info.Mode().Perm()&0o022 != 0 &&
		(info.Mode()&os.ModeSticky == 0 || info.Mode().Perm()&0o002 == 0) {
		return NewError(CodeIntegrity, "database supervisor executable ancestor is publicly mutable")
	}
	return nil
}

func supervisorTrustedUnixOwner(info os.FileInfo) bool {
	if info == nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat != nil && (stat.Uid == 0 || stat.Uid == uint32(os.Geteuid()))
}
