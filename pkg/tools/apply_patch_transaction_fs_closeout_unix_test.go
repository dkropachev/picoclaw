//go:build unix

package tools

import "golang.org/x/sys/unix"

func createApplyPatchTxnCloseoutFIFO(path string) error {
	return unix.Mkfifo(path, 0o600)
}
