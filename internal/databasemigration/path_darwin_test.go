//go:build darwin

package databasemigration

import "testing"

func TestDarwinBackupPathKeyPreservesCase(t *testing.T) {
	if backupPathKey("/tmp/Store.db") == backupPathKey("/tmp/store.db") {
		t.Fatal("Darwin lexical backup key collapsed case-sensitive paths")
	}
}
