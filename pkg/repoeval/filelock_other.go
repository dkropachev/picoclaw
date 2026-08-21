//go:build !unix && !windows

package repoeval

func lockRepositoryEvaluationStore(_ string) (func(), error) {
	return func() {}, nil
}
