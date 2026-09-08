//go:build !unix && !windows

package sqliteprovider

import "os"

func replaceStagedGeneration(stage, target string) (bool, error) {
	if err := os.Rename(stage, target); err != nil {
		return false, err
	}
	return true, nil
}

func syncStagedMigrationDirectory(string) error { return nil }
