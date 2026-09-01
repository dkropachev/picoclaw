package gateway

import (
	"os"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

func privatePRWorkspaceCheckpointDirectory(path string, info os.FileInfo) bool {
	return fileutil.ValidatePrivateDirectory(path, info) == nil
}

func privatePRWorkspaceCheckpointFile(path string, info os.FileInfo) bool {
	return fileutil.ValidatePrivateFile(path, info) == nil
}
