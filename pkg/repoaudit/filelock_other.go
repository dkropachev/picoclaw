//go:build !unix && !windows

package repoaudit

func lockRepositoryReviewStore(_ string) (func(), error) {
	if err := reviewProviderAuthorityError(); err != nil {
		return nil, err
	}
	return func() {}, nil
}

func tryLockRepositoryReviewStore(_ string) (func(), error) {
	if err := reviewProviderAuthorityError(); err != nil {
		return nil, err
	}
	return func() {}, nil
}
