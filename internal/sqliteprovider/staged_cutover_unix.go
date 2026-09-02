//go:build unix

package sqliteprovider

import (
	"os"
	"path/filepath"
)

func replaceStagedGeneration(stage, target string) (bool, error) {
	if err := os.Rename(stage, target); err != nil {
		return false, err
	}
	if err := stagedCutoverDirectorySync(filepath.Dir(target)); err != nil {
		return true, err
	}
	return true, nil
}

func syncStagedMigrationDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
