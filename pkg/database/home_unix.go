//go:build unix

package database

import (
	"os"
	"path/filepath"
	"syscall"
)

func sameCanonicalPath(first, second string) bool {
	return filepath.Clean(first) == filepath.Clean(second)
}

func validateTrustedHomeDirectory(_ string, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return NewError(CodeIntegrity, "PicoClaw home ownership is unavailable")
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return NewError(CodeUnauthorized, "PicoClaw home is owned by another user")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return NewError(CodeIntegrity, "PicoClaw home is writable by another user")
	}
	return nil
}

func validateOwnerOnlyDirectory(_ string, info os.FileInfo) error {
	if info.Mode().Perm() != 0o700 {
		return NewError(CodeIntegrity, "database state directory must have mode 0700")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Uid != uint32(os.Geteuid()) {
		return NewError(CodeUnauthorized, "database state directory is owned by another user")
	}
	return nil
}

func validateOwnerOnlyFile(_ string, info os.FileInfo, expected os.FileMode) error {
	if !info.Mode().IsRegular() || info.Mode().Perm() != expected.Perm() {
		return NewError(CodeIntegrity, "database broker file permissions are invalid")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Uid != uint32(os.Geteuid()) {
		return NewError(CodeUnauthorized, "database broker file is owned by another user")
	}
	return nil
}

func validateOwnerOnlySocket(_ string, info os.FileInfo) error {
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 {
		return NewError(CodeIntegrity, "database broker socket permissions are invalid")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Uid != uint32(os.Geteuid()) {
		return NewError(CodeUnauthorized, "database broker socket is owned by another user")
	}
	return nil
}
