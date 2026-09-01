//go:build !windows

package fstools

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func fileIdentityFromOpenedHandle(_ *os.File, info os.FileInfo) (string, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return "", errors.New("file identity is unavailable")
	}
	return fmt.Sprintf("%x:%x", stat.Dev, stat.Ino), nil
}
