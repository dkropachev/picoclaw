//go:build !unix && !windows

package sqliteprovider

import "os"

func classifyGenerationLinkCount(string, os.FileInfo) generationLinkClass {
	return generationLinkUnavailable
}

func classifyGenerationOwner(string, os.FileInfo) generationOwnerClass {
	return generationOwnerUnavailable
}
