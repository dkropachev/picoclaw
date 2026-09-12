//go:build linux || android

package sqliteprovider

import "golang.org/x/sys/unix"

func renameStagedRetirementNoReplace(
	oldDirectory int,
	oldName string,
	newDirectory int,
	newName string,
) error {
	return unix.Renameat2(
		oldDirectory,
		oldName,
		newDirectory,
		newName,
		unix.RENAME_NOREPLACE,
	)
}
