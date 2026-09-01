package gitworkspace

import (
	"os"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

func managedDirectoryModePrivate(path string, info os.FileInfo) bool {
	return fileutil.ValidatePrivateDirectory(path, info) == nil
}

func managedFileModePrivate(path string, info os.FileInfo) bool {
	return fileutil.ValidatePrivateFile(path, info) == nil
}
