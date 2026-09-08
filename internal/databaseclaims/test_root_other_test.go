//go:build !unix && !windows

package databaseclaims

import "testing"

func isolatedTestClaimRoot(t *testing.T) string {
	t.Helper()
	return secureTestDir(t)
}
