//go:build unix

package sqliteprovider

import (
	"errors"
	"os"
	"syscall"
)

func secureProviderDirectory(path string) error { return secureUnixProviderPath(path, true) }
func secureProviderFile(path string) error      { return secureUnixProviderPath(path, false) }

func secureUnixProviderPath(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || directory != info.IsDir() || !directory && !info.Mode().IsRegular() {
		return errors.New("SQLite provider security boundary is unsafe")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Uid != uint32(os.Geteuid()) {
		return errors.New("SQLite provider security boundary is owned by another user")
	}
	mode := os.FileMode(0o600)
	if directory {
		mode = 0o700
	}
	return os.Chmod(path, mode)
}
