//go:build darwin

package databasemigration

import "golang.org/x/sys/unix"

func publishBackupDirectory(source, target string) error {
	return unix.RenamexNp(source, target, unix.RENAME_EXCL)
}
