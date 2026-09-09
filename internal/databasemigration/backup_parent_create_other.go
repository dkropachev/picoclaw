//go:build !windows && (!unix || aix)

package databasemigration

import (
	"os"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

func validateBackupParentCreatedDirectoryHandle(*os.File, fileidentity.Identity) error {
	return fileidentity.ErrUnsupported
}

func secureBackupParentCreatedDirectoryHandle(*os.File, fileidentity.Identity) error {
	return fileidentity.ErrUnsupported
}

func syncBackupParentCreatedDirectoryHandle(*os.File, fileidentity.Identity) error {
	return fileidentity.ErrUnsupported
}
