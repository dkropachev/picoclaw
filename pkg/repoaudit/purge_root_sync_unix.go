//go:build unix

package repoaudit

import (
	"errors"
	"os"
)

func syncRepositoryReviewPurgeRoot(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}

func removeRepositoryReviewPurgeRootEntry(root *os.Root, name string) error {
	if err := root.Remove(name); err != nil {
		return err
	}
	return syncRepositoryReviewPurgeRoot(root)
}
