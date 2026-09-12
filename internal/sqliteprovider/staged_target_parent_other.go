//go:build (!unix && !windows) || aix

package sqliteprovider

import (
	"context"
	"errors"
	"os"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

type stagedTargetParentPlatform struct{}

func createRetainedStagedTargetParentPlatform(
	context.Context,
	string,
) (*stagedTargetParentPlatform, error) {
	return nil, errors.Join(
		errors.New("SQLite staged target parent proof is unsupported"),
		fileidentity.ErrUnsupported,
	)
}

func checkRetainedStagedTargetParentPlatform(
	context.Context,
	string,
	os.FileInfo,
	*stagedTargetParentPlatform,
) (fileidentity.Identity, error) {
	return fileidentity.Identity{}, fileidentity.ErrUnsupported
}

func checkRetainedStagedTargetParentSoleStagePlatform(
	context.Context,
	string,
	string,
	os.FileInfo,
	fileidentity.Identity,
	*stagedTargetParentPlatform,
) (fileidentity.Identity, error) {
	return fileidentity.Identity{}, fileidentity.ErrUnsupported
}

func checkRetainedStagedTargetParentSoleInstalledPlatform(
	context.Context,
	string,
	string,
	os.FileInfo,
	fileidentity.Identity,
	*stagedTargetParentPlatform,
) (fileidentity.Identity, error) {
	return fileidentity.Identity{}, fileidentity.ErrUnsupported
}

func closeRetainedStagedTargetParentPlatform(*stagedTargetParentPlatform, bool) error { return nil }

func replaceRetainedStagedTargetParentStagePlatform(
	context.Context,
	string,
	string,
	string,
	os.FileInfo,
	*os.File,
	fileidentity.Identity,
	*stagedTargetParentPlatform,
) (bool, error) {
	return false, fileidentity.ErrUnsupported
}
