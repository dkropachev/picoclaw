//go:build !windows

package databasemigration

func validBackupPlatformPath(string, bool) bool { return true }
func validBackupPlatformComponent(string) bool  { return true }
