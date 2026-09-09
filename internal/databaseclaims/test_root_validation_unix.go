//go:build unix

package databaseclaims

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/sipeed/picoclaw/pkg/database"
)

func validateExplicitTestClaimRoot(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", database.NewError(database.CodeIntegrity, "test claim root must be canonical and absolute")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || filepath.Clean(resolved) != path {
		return "", database.NewError(database.CodeIntegrity, "test claim root contains an alias")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return "", database.NewError(database.CodeIntegrity, "test claim root is not a private directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil || stat.Uid != uint32(os.Geteuid()) {
		return "", database.NewError(database.CodeUnauthorized, "test claim root has an invalid owner")
	}
	return path, nil
}

func prepareExplicitTestClaimRoot(path string) (result string, resultErr error) {
	canonical, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", database.NewError(database.CodeIntegrity, "test claim root path is invalid")
	}
	canonical, err = filepath.EvalSymlinks(canonical)
	if err != nil {
		return "", database.NewError(database.CodeIntegrity, "test claim root cannot be canonicalized")
	}
	canonical = filepath.Clean(canonical)
	fd, err := unix.Open(
		canonical, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0,
	)
	if err != nil {
		return "", errors.Join(database.NewError(database.CodeIntegrity, "test claim root is unsafe"), err)
	}
	defer func() { resultErr = errors.Join(resultErr, unix.Close(fd)) }()
	var opened unix.Stat_t
	if statErr := unix.Fstat(fd, &opened); statErr != nil || opened.Mode&unix.S_IFMT != unix.S_IFDIR ||
		opened.Uid != uint32(os.Geteuid()) {
		return "", errors.Join(
			database.NewError(database.CodeUnauthorized, "test claim root owner is invalid"),
			statErr,
		)
	}
	if chmodErr := unix.Fchmod(fd, 0o700); chmodErr != nil {
		return "", errors.Join(database.NewError(database.CodeIntegrity, "secure test claim root"), chmodErr)
	}
	info, err := os.Lstat(canonical)
	stat, ok := infoSyscallStat(info)
	if err != nil || !ok || stat.Dev != opened.Dev || stat.Ino != opened.Ino {
		return "", errors.Join(database.NewError(database.CodeIntegrity, "test claim root changed"), err)
	}
	return validateExplicitTestClaimRoot(canonical)
}

func infoSyscallStat(info os.FileInfo) (*syscall.Stat_t, bool) {
	if info == nil {
		return nil, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return stat, ok && stat != nil
}
