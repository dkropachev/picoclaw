package agent

import (
	"os"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

func privateAgentCheckpointDirectory(path string, info os.FileInfo) bool {
	return fileutil.ValidatePrivateDirectory(path, info) == nil
}

func privateAgentCheckpointFile(path string, info os.FileInfo) bool {
	return fileutil.ValidatePrivateFile(path, info) == nil
}
