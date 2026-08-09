//go:build !linux

package localci

import (
	"errors"
	"os"
	"path/filepath"
)

func publishEvidenceNoReplace(directory *os.File, oldName, newName string) error {
	oldPath := filepath.Join(directory.Name(), oldName)
	newPath := filepath.Join(directory.Name(), newName)
	if err := os.Link(oldPath, newPath); err != nil {
		return err
	}
	if err := os.Remove(oldPath); err != nil {
		return errors.Join(err, os.Remove(newPath))
	}
	return nil
}
