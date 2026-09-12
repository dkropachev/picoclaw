//go:build (!unix && !windows) || aix

package sqlitestore

import (
	"context"
	"errors"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

type sealedAbsentLegacyRootPlatform struct{}

func captureSealedAbsentLegacyRootPlatform(
	context.Context,
	string,
) (*sealedAbsentLegacyRootPlatform, string, []string, error) {
	return nil, "", nil, errors.Join(
		errors.New("sealed absent legacy root proof is unsupported on this platform"),
		fileidentity.ErrUnsupported,
	)
}

func revalidateSealedAbsentLegacyRootPlatform(
	context.Context,
	string,
	string,
	[]string,
	*sealedAbsentLegacyRootPlatform,
) error {
	return errors.Join(
		errors.New("sealed absent legacy root proof is unsupported on this platform"),
		fileidentity.ErrUnsupported,
	)
}

func closeSealedAbsentLegacyRootPlatform(*sealedAbsentLegacyRootPlatform) error { return nil }
