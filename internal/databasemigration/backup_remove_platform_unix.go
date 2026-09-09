//go:build linux || android || darwin

package databasemigration

import (
	"errors"
	"os"
)

func removeBackupRemovalRootEntry(root *os.Root, leaf string, _ *os.File, _ bool) error {
	return root.Remove(leaf)
}

func syncBackupRemovalRoot(root *os.Root) error {
	parent, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(parent.Sync(), parent.Close())
}
