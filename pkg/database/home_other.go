//go:build !unix && !windows

package database

import (
	"os"
	"path/filepath"
)

func sameCanonicalPath(first, second string) bool {
	return filepath.Clean(first) == filepath.Clean(second)
}

func validateOwnerOnlyDirectory(_ string, info os.FileInfo) error {
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		return NewError(CodeIntegrity, "database state directory is not owner-only")
	}
	return nil
}

func validateOwnerOnlyFile(_ string, info os.FileInfo, expected os.FileMode) error {
	if !info.Mode().IsRegular() || info.Mode().Perm() != expected.Perm() {
		return NewError(CodeIntegrity, "database broker file permissions are invalid")
	}
	return nil
}

func validateOwnerOnlySocket(string, os.FileInfo) error {
	return NewError(CodeUnsupported, "local database transport is unsupported")
}
