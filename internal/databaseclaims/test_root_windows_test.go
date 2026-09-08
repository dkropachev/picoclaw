//go:build windows

package databaseclaims

import (
	"testing"
)

func isolatedTestClaimRoot(t *testing.T) string {
	t.Helper()
	root, err := PrepareRootForTesting(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}
