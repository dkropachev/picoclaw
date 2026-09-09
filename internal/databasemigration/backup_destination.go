package databasemigration

import (
	"errors"
	"fmt"
	"path/filepath"
)

func legacyBackupDestination(
	storeDirectory string,
	rootIndex int,
	root,
	source string,
) (string, error) {
	relative, inside, err := legacySourceRelative(root, source)
	if err != nil || !inside {
		return "", errors.Join(errors.New("legacy input is outside its declared root"), err)
	}
	prefix := filepath.Join(
		"stores", storeDirectory, "legacy", fmt.Sprintf("root-%06d", rootIndex),
	)
	if relative == "." {
		return filepath.Join(prefix, "root"), nil
	}
	return filepath.Join(prefix, "tree", relative), nil
}
