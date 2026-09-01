//go:build windows

package agent

import "os"

// Windows FileMode permission bits do not describe ACL privacy.
func privateAgentCheckpointDirectory(info os.FileInfo) bool {
	return info != nil && info.IsDir()
}

func privateAgentCheckpointFile(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular()
}
