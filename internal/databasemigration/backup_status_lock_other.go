//go:build (!unix && !windows) || aix

package databasemigration

import (
	"errors"
	"os"
)

func lockBackupStatusHandle(*os.File) (func() error, error) {
	return nil, errors.New("database backup migration status locking is unsupported")
}
