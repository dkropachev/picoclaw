//go:build unix

package localci

import "golang.org/x/sys/unix"

func syscallMkfifo(path string) error {
	return unix.Mkfifo(path, 0o600)
}
