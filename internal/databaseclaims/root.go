package databaseclaims

import (
	"crypto/sha256"
	"errors"
	"path/filepath"

	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	claimApplicationDirectory = "picoclaw"
	claimDirectoryName        = "database-store-claims"
)

var errClaimBusy = errors.New("physical database claim is busy")

type claimRootOps struct {
	stable   func() (string, error)
	absolute func(string) (string, error)
	prepare  func(string, string) error
}

func defaultClaimRootOps() claimRootOps {
	return claimRootOps{
		stable: stableClaimCacheRoot, absolute: filepath.Abs, prepare: preparePlatformClaimRoot,
	}
}

func validClaimIdentity(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func prepareClaimRoot() (string, error) {
	return prepareClaimRootWithOps(defaultClaimRootOps())
}

func prepareClaimRootWithOps(ops claimRootOps) (string, error) {
	if ops.stable == nil || ops.absolute == nil || ops.prepare == nil {
		return "", database.NewError(database.CodeIntegrity, "physical database claim root operations are invalid")
	}
	cache, err := ops.stable()
	if err != nil {
		return "", database.NewError(database.CodeUnavailable, "physical database claim root is unavailable")
	}
	cache, err = ops.absolute(cache)
	if err != nil {
		return "", database.NewError(database.CodeUnavailable, "physical database claim root is unavailable")
	}
	cache = filepath.Clean(cache)
	root := filepath.Join(cache, claimApplicationDirectory, claimDirectoryName)
	if err := ops.prepare(cache, root); err != nil {
		return "", err
	}
	return root, nil
}
