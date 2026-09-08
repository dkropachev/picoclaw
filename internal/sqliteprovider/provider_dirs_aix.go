//go:build aix

package sqliteprovider

import (
	"os"

	"github.com/sipeed/picoclaw/pkg/database"
)

func makeProviderDirectories(string, os.FileMode) error {
	return database.NewError(
		database.CodeUnsupported,
		"secure provider directory creation is unsupported on AIX",
	)
}
