//go:build !unix && !windows

package databaseclaims

import (
	"os"

	"github.com/sipeed/picoclaw/pkg/database"
)

func stableClaimCacheRoot() (string, error) { return os.UserCacheDir() }

func preparePlatformClaimRoot(string, string) error {
	return database.NewError(database.CodeUnsupported, "physical database claims are unsupported on this platform")
}

func acquireClaim(string, string) (claimHandle, error) {
	return nil, database.NewError(database.CodeUnsupported, "physical database claims are unsupported on this platform")
}
