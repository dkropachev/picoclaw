//go:build windows

package sqliteprovider

import "golang.org/x/sys/windows"

func replaceStagedGeneration(stage, target string) (bool, error) {
	source, err := windows.UTF16PtrFromString(stage)
	if err != nil {
		return false, err
	}
	destination, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return false, err
	}
	err = windows.MoveFileEx(
		source,
		destination,
		windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH,
	)
	if err != nil {
		// MoveFileEx can cross the replacement boundary before a write-through
		// failure is reported, so preserve both names and report uncertainty.
		return true, err
	}
	return true, nil
}

func syncStagedMigrationDirectory(string) error { return nil }
