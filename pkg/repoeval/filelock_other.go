//go:build !unix && !windows

package repoeval

func lockRepositoryEvaluationStore(_ string) (func(), error) {
	if err := evaluationProviderAuthorityError(); err != nil {
		return nil, err
	}
	return func() {}, nil
}

func tryLockRepositoryEvaluationStore(_ string) (func(), error) {
	if err := evaluationProviderAuthorityError(); err != nil {
		return nil, err
	}
	return func() {}, nil
}
