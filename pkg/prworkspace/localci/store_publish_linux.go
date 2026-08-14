//go:build linux

package localci

import (
	"os"

	"golang.org/x/sys/unix"
)

func publishEvidenceNoReplace(directory *os.File, oldName, newName string) error {
	return unix.Renameat2(
		int(directory.Fd()),
		oldName,
		int(directory.Fd()),
		newName,
		unix.RENAME_NOREPLACE,
	)
}
