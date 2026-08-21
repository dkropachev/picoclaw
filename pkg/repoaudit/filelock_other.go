//go:build !unix && !windows

package repoaudit

func lockRepositoryReviewStore(_ string) (func(), error) {
	return func() {}, nil
}
