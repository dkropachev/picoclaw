//go:build windows

package sqlitestore

import (
	"os"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

func safeLegacyDirectory(path string, info os.FileInfo) bool {
	return fileutil.ValidatePrivateDirectory(path, info) == nil
}

func safeLegacyRegularFile(path string, info os.FileInfo) bool {
	return fileutil.ValidatePrivateFile(path, info) == nil
}
