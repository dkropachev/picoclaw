//go:build !unix

package databasemigration

import "os"

func openExactBackupRoot(path string) (*os.Root, error) {
	return os.OpenRoot(path)
}
