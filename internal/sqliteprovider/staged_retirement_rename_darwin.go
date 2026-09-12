//go:build darwin

package sqliteprovider

import "golang.org/x/sys/unix"

func renameStagedRetirementNoReplace(
	oldDirectory int,
	oldName string,
	newDirectory int,
	newName string,
) error {
	return unix.RenameatxNp(
		oldDirectory,
		oldName,
		newDirectory,
		newName,
		unix.RENAME_EXCL,
	)
}
