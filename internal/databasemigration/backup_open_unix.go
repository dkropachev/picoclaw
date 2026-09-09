//go:build unix

package databasemigration

import (
	"os"
	"syscall"
)

func openPinnedBackupChild(root *os.Root, leaf string) (*os.File, error) {
	// A pathname can be exchanged for a FIFO or device after lstat. A
	// nonblocking open prevents that race from hanging backup verification;
	// the subsequent descriptor type and full-identity checks still fail shut.
	return root.OpenFile(leaf, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
