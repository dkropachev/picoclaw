//go:build !unix && !windows

package sqliteprovider

import "os"

func generationHasSingleLink(string, os.FileInfo) bool {
	return false
}

func generationOwnedByCurrentUser(string, os.FileInfo) bool { return false }
