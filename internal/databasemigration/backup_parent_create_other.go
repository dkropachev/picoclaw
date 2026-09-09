//go:build !windows && (!unix || aix)

package databasemigration

import (
	"errors"
	"os"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

func validateBackupParentCreationContainer(*os.Root) error {
	return errors.New("database backup parent creation container is unsupported")
}

func validateBackupParentCreatedDirectoryHandle(*os.File, fileidentity.Identity) error {
	return fileidentity.ErrUnsupported
}

func secureBackupParentCreatedDirectoryHandle(*os.File, fileidentity.Identity) error {
	return fileidentity.ErrUnsupported
}

func syncBackupParentCreatedDirectoryHandle(*os.File, fileidentity.Identity) error {
	return fileidentity.ErrUnsupported
}
