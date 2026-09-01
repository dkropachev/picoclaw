//go:build !windows

package gateway

import "os"

func privatePRWorkspaceCheckpointDirectory(info os.FileInfo) bool {
	return info != nil && info.IsDir() && info.Mode().Perm()&0o077 == 0
}

func privatePRWorkspaceCheckpointFile(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode().Perm()&0o077 == 0
}
