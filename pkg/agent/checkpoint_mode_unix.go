//go:build !windows

package agent

import "os"

func privateAgentCheckpointDirectory(info os.FileInfo) bool {
	return info != nil && info.IsDir() && info.Mode().Perm()&0o077 == 0
}

func privateAgentCheckpointFile(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode().Perm()&0o077 == 0
}
