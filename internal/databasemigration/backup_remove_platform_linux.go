//go:build linux || android

package databasemigration

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func renameBackupRemovalRootNoReplace(
	root *os.Root,
	source string,
	target string,
	_ *os.File,
) (*os.File, error) {
	parent, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	renameErr := unix.Renameat2(
		int(parent.Fd()), source, int(parent.Fd()), target, unix.RENAME_NOREPLACE,
	)
	return nil, errors.Join(renameErr, parent.Close())
}

func removeBackupRemovalRootEntry(
	root *os.Root,
	leaf string,
	_ *os.File,
	_ bool,
) error {
	return root.Remove(leaf)
}

func syncBackupRemovalRoot(root *os.Root) error {
	parent, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(parent.Sync(), parent.Close())
}
