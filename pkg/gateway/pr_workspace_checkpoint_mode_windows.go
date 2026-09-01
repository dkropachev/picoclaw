//go:build windows

package gateway

import "os"

// Windows FileMode permission bits do not describe ACL privacy. The checkpoint
// root is canonicalized and SQLite/Root handles fence reparse traversal.
func privatePRWorkspaceCheckpointDirectory(info os.FileInfo) bool {
	return info != nil && info.IsDir()
}

func privatePRWorkspaceCheckpointFile(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular()
}
