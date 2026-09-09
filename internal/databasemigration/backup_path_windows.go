//go:build windows

package databasemigration

import (
	"path/filepath"
)

func validBackupPlatformPath(path string, absolute bool) bool {
	if !validWindowsBackupPathComponents(path, absolute) {
		return false
	}
	volume := filepath.VolumeName(path)
	if !absolute && volume != "" {
		return false
	}
	if absolute && volume == "" {
		return false
	}
	return true
}

func validBackupPlatformComponent(component string) bool {
	return validWindowsBackupComponent(component)
}
