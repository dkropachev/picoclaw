//go:build (!unix && !windows) || aix

package databasereadiness

import (
	"os"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

func openLegacyNoFollow(string, fileidentity.ObjectType) (*os.File, bool, error) {
	return nil, false, fileidentity.ErrUnsupported
}

func openLegacyChildNoFollow(
	*os.File,
	string,
	string,
	fileidentity.ObjectType,
) (*os.File, bool, error) {
	return nil, false, fileidentity.ErrUnsupported
}
