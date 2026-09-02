//go:build !unix && !windows

package repoaudit

import "os"

func syncRepositoryReviewPurgeRoot(_ *os.Root) error { return nil }

func removeRepositoryReviewPurgeRootEntry(root *os.Root, name string) error {
	return root.Remove(name)
}
