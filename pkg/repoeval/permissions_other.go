//go:build !unix

package repoeval

import "os"

// Windows and other non-POSIX targets do not expose meaningful owner/group/
// world permission bits through os.FileMode. The platform's file ACL and the
// requested 0600 creation mode remain authoritative there.
func repositoryEvaluationPermissionsSafe(_ os.FileMode) bool {
	return true
}
