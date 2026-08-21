//go:build unix

package repoeval

import "os"

func repositoryEvaluationPermissionsSafe(mode os.FileMode) bool {
	return mode.Perm()&0o077 == 0
}
