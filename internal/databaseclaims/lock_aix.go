//go:build aix

package databaseclaims

import "github.com/sipeed/picoclaw/pkg/database"

func acquireClaim(string, string) (claimHandle, error) {
	return nil, database.NewError(database.CodeUnsupported, "physical database claims are unsupported on AIX")
}
