//go:build !unix && !windows

package sqliteprovider

import "os"

func makeProviderDirectories(string, os.FileMode) error {
	return unsupportedProviderFilesystem()
}
