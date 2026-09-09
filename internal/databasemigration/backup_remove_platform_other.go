//go:build !linux && !android && !darwin && !windows

package databasemigration

import (
	"errors"
	"os"
)

func renameBackupRemovalRootNoReplace(
	*os.Root,
	string,
	string,
	*os.File,
) (*os.File, error) {
	return nil, errors.New("retained-root database backup removal is unsupported")
}

func removeBackupRemovalRootEntry(*os.Root, string, *os.File, bool) error {
	return errors.New("retained-root database backup removal is unsupported")
}

func syncBackupRemovalRoot(*os.Root) error {
	return errors.New("retained-root database backup removal is unsupported")
}
