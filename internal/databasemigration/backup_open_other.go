//go:build !unix

package databasemigration

import "os"

func openPinnedBackupChild(root *os.Root, leaf string) (*os.File, error) {
	return root.Open(leaf)
}
