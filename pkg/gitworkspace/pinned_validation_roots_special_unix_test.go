//go:build android || darwin || dragonfly || freebsd || ios || linux || netbsd || openbsd

package gitworkspace

import "syscall"

const pinnedValidationSpecialNodeSupported = true

func createPinnedValidationSpecialNode(name string) error {
	return syscall.Mkfifo(name, 0o600)
}
