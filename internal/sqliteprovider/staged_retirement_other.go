//go:build (!unix || aix) && !windows

package sqliteprovider

import (
	"context"
	"errors"
)

type stagedRetirementPlatform struct{}

func retainStagedGenerationPlatform(string) (*stagedRetirementPlatform, error) {
	return nil, errors.New("SQLite identity-bound staged retirement is unsupported")
}

func (*stagedRetirementPlatform) retire(context.Context) error {
	return errors.New("SQLite identity-bound staged retirement is unsupported")
}

func (*stagedRetirementPlatform) check(string) error {
	return errors.New("SQLite identity-bound staged retention is unsupported")
}

func (*stagedRetirementPlatform) close() error { return nil }
