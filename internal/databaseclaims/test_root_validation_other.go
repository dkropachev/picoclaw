//go:build !unix && !windows

package databaseclaims

import "github.com/sipeed/picoclaw/pkg/database"

func validateExplicitTestClaimRoot(string) (string, error) {
	return "", database.NewError(database.CodeUnsupported, "test physical claims are unsupported on this platform")
}

func prepareExplicitTestClaimRoot(string) (string, error) {
	return "", database.NewError(database.CodeUnsupported, "test physical claims are unsupported on this platform")
}
