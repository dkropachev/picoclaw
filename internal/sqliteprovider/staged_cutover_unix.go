//go:build unix

package sqliteprovider

import (
	"os"
	"path/filepath"
)

func replaceStagedGeneration(stage, target string) (bool, error) {
	return replaceStagedGenerationWithOps(stage, target, os.Rename, syncStagedMigrationDirectory)
}

func replaceStagedGenerationWithOps(
	stage string,
	target string,
	rename func(string, string) error,
	syncDirectory func(string) error,
) (bool, error) {
	if err := rename(stage, target); err != nil {
		return false, err
	}
	if err := syncDirectory(filepath.Dir(target)); err != nil {
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
