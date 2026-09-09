//go:build darwin

package databasemigration

import "testing"

func TestDarwinBackupPathKeyFoldsCase(t *testing.T) {
	if backupPathKey("/tmp/Store.db") != backupPathKey("/tmp/store.db") {
		t.Fatal("Darwin lexical backup key did not conservatively collapse case aliases")
	}
}
