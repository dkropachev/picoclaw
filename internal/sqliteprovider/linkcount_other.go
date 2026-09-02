//go:build !unix && !windows

package sqliteprovider

import "os"

func generationHasSingleLink(string, os.FileInfo) bool {
	// Platforms exposing link counts through FileInfo should add a native
	// implementation; catalog and owner-only store claims still fence paths.
	return true
}

func generationOwnedByCurrentUser(string, os.FileInfo) bool { return true }
